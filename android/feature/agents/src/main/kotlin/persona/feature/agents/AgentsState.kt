package persona.feature.agents

import persona.core.designsystem.resolve
import android.content.res.Resources
import persona.core.designsystem.PendingStatus
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.*
import persona.core.contracts.*
import persona.core.contracts.api.HubPreferences
import persona.core.contracts.api.AgentsDisplayHub
import persona.core.contracts.api.AgentsDisplayHubs
import persona.core.domain.sort.*
import persona.core.domain.answer.requestSourceLabel
import persona.core.domain.answer.RequestExpiry
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*
import java.time.Duration
import java.time.Instant
import java.util.concurrent.ConcurrentHashMap
import javax.inject.Inject
import javax.inject.Singleton

private fun AgentPreferences.hubJson() = buildJsonObject {
    put("spaces", JsonArray(spaces.map(::JsonPrimitive)))
    put("sessions", JsonArray(sessions.map(::JsonPrimitive)))
    put("allowed", JsonArray(allowed.sorted().map(::JsonPrimitive)))
}.toString()

private fun hubPreferences(raw: String): AgentPreferences {
    val json = JsonCodec.parseToJsonElement(raw).jsonObject
    fun strings(key: String) = (json[key] as? JsonArray).orEmpty().mapNotNull { (it as? JsonPrimitive)?.contentOrNull }
    return AgentPreferences(strings("spaces"), strings("sessions"), strings("allowed").toSet())
}

/** Row presentation stays local; each paired hub owns its botmux display setting, pins and read receipts. */
@Singleton
class AgentsPreferences @Inject constructor(
    private val store: DataStore<Preferences>, private val hub: HubPreferences,
    private val displayHubs: AgentsDisplayHubs = AgentsDisplayHubs { flowOf(emptyList()) },
) : AgentListDisplaySettings {
    private val displayLocks = ConcurrentHashMap<String, Mutex>()
    val snapshots: Flow<AgentsPreferencesSnapshot> get() = snapshots("")
    fun snapshots(scope: String): Flow<AgentsPreferencesSnapshot> = combine(store.data, hub.preferences, displayHubs.paired()) { p, remote, hubs ->
        AgentsPreferencesSnapshot(
            range = AgentRange.entries.firstOrNull { it.name == p[agentRangeKey(scope)] } ?: AgentRange.Running,
            grouping = AgentGrouping.fromPreference(p[stringPreferencesKey("grouping")]),
            compact = p[compactKey] ?: false,
            rowStyle = AgentRowStyle.fromPreference(p[rowStyleKey]),
            sort = AgentSort.fromPreference(p[stringPreferencesKey("sort")]),
            hiddenBotmuxHubs = hubs.filter { pair ->
                p[showBotmuxKey(pair.scope)]?.let { !it } ?: (p[legacyHideBotmux] == true && p.ownsLegacy(pair))
            }.mapTo(mutableSetOf()) { it.namespace },
            herdrTabNameHubs = hubs.filter { pair ->
                p[useHerdrTabNameKey(pair.scope)] == true
            }.mapTo(mutableSetOf()) { it.namespace },
            foldedTabs = p[foldedTabsKey].orEmpty(),
            folds = AgentGrouping.entries.associateWith { p[foldsKey(it)].orEmpty() },
            pins = AgentPreferences(remote.pins.spaces, remote.pins.sessions, remote.pins.allowed),
            mutes = AgentPreferences(remote.mutes.spaces, remote.mutes.sessions, remote.mutes.allowed),
            seen = remote.seen,
            seenRows = AgentSeenRows.fromPreference(p[seenRowsKey]),
            seenRowsNoticed = p[seenRowsNoticedKey] ?: false,
        )
    }
    /** Hub pins and mutes as last read for each pairing scope. */
    internal val hubCache: AgentPreferencesCache = object : AgentPreferencesCache {
        override suspend fun read(scope: String): Pair<AgentPreferences, AgentPreferences>? {
            val p = store.data.first()
            val pins = p[stringPreferencesKey("hub-pins:$scope")] ?: return null
            val mutes = p[stringPreferencesKey("hub-mutes:$scope")] ?: return null
            return hubPreferences(pins) to hubPreferences(mutes)
        }
        override suspend fun write(scope: String, pins: AgentPreferences, mutes: AgentPreferences) {
            store.edit {
                it[stringPreferencesKey("hub-pins:$scope")] = pins.hubJson()
                it[stringPreferencesKey("hub-mutes:$scope")] = mutes.hubJson()
            }
        }
    }
    suspend fun grouping(value: AgentGrouping) { store.edit { it[stringPreferencesKey("grouping")] = value.name } }
    suspend fun sort(value: AgentSort) { store.edit { it[stringPreferencesKey("sort")] = value.name } }
    suspend fun watchDisplaySettings(refresh: Flow<Int>) = displayHubs.paired().collectLatest { hubs ->
        coroutineScope {
            for (pair in hubs) launch {
                combine(pair.hello, refresh) { hello, _ -> hello }.collectLatest { hello ->
                    if (hello != null) try { refreshDisplay(pair) }
                    catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) { /* Offline and older hubs keep the last value and pending migration. */ }
                }
            }
        }
    }

    private suspend fun refreshDisplay(pair: AgentsDisplayHub) = displayLocks.getOrPut(pair.scope) { Mutex() }.withLock {
        // Bind the old phone-wide key to one pairing before network I/O. A failed request must not migrate it elsewhere.
        val local = store.data.first().let { p ->
            if (p[legacyHideBotmux] != null && p[migrationScope] == null && pair.selected) store.edit {
                if (it[legacyHideBotmux] != null && it[migrationScope] == null) it[migrationScope] = pair.scope
            } else p
        }
        val legacy = local[legacyHideBotmux].takeIf { local[migrationScope] == pair.scope }
        var acknowledged = pair.api.read()
        if (legacy == true && !acknowledged.configured) {
            acknowledged = pair.api.migrateHidden()
            check(acknowledged.configured)
        }
        store.edit {
            it[showBotmuxKey(pair.scope)] = acknowledged.showBotmux
            acknowledged.useHerdrTabName?.let { enabled -> it[useHerdrTabNameKey(pair.scope)] = enabled }
                ?: it.remove(useHerdrTabNameKey(pair.scope))
            if (it[migrationScope] == pair.scope) {
                it.remove(legacyHideBotmux)
                it.remove(migrationScope)
            }
        }
    }

    override val useHerdrTabName: Flow<Boolean?>
        get() = combine(store.data, displayHubs.paired()) { p, hubs ->
            val selected = hubs.firstOrNull { it.selected } ?: hubs.singleOrNull() ?: return@combine null
            p[useHerdrTabNameKey(selected.scope)]
        }

    override suspend fun refreshHerdrTabName() {
        val hubs = displayHubs.paired().first()
        val selected = hubs.firstOrNull { it.selected } ?: hubs.singleOrNull() ?: return
        try { refreshDisplay(selected) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* Offline and older hubs keep the last acknowledged value. */ }
    }

    override suspend fun setUseHerdrTabName(enabled: Boolean) {
        val hubs = displayHubs.paired().first()
        val selected = hubs.firstOrNull { it.selected } ?: hubs.singleOrNull()
            ?: throw IllegalStateException("No paired hub selected")
        displayLocks.getOrPut(selected.scope) { Mutex() }.withLock {
            val acknowledged = selected.api.setUseHerdrTabName(enabled)
            store.edit {
                it[showBotmuxKey(selected.scope)] = acknowledged.showBotmux
                acknowledged.useHerdrTabName?.let { value -> it[useHerdrTabNameKey(selected.scope)] = value }
                    ?: it.remove(useHerdrTabNameKey(selected.scope))
            }
        }
    }
    suspend fun range(scope: String, value: AgentRange) { store.edit { it[agentRangeKey(scope)] = value.name } }
    suspend fun compact(value: Boolean) { store.edit { it[compactKey] = value } }
    /**
     * The list's display, in the two values iOS keeps it in (`AgentListDisplayChoice`): Compact List is the `compact` the
     * phone always kept (Brief and Board's Compact), so a phone that chose Compact before the styles came keeps it, and one
     * that chose Detailed, with no style stored, reads as Avatar, the row Glass's Detailed list already drew. Compact List
     * keeps the style chosen before it.
     */
    override val display: Flow<AgentListDisplay> get() = store.data.map { AgentListDisplay.of(AgentRowStyle.fromPreference(it[rowStyleKey]), it[compactKey] ?: false) }
    override val seenRows: Flow<AgentSeenRows> get() = store.data.map { AgentSeenRows.fromPreference(it[seenRowsKey]) }
    override suspend fun setSeenRows(value: AgentSeenRows) {
        store.edit {
            it[seenRowsKey] = value.name
            it[seenRowsNoticedKey] = true
        }
    }
    suspend fun noticeSeenRows(): Boolean {
        var newlyNoticed = false
        store.edit {
            if (it[seenRowsNoticedKey] != true) {
                newlyNoticed = true
                it[seenRowsNoticedKey] = true
            }
        }
        return newlyNoticed
    }

    override suspend fun setDisplay(value: AgentListDisplay) {
        store.edit { p ->
            value.style?.let { p[rowStyleKey] = it.name }
            p[compactKey] = value == AgentListDisplay.CompactList
        }
    }
    /** Folds or opens a herdr tab's agents in the list (#2320), kept on this phone by [AgentTab.foldKey]. */
    suspend fun foldTab(key: String, folded: Boolean) {
        store.edit { it[foldedTabsKey] = it[foldedTabsKey].orEmpty().let { keys -> if (folded) keys + key else keys - key } }
    }
    /** The list's folded sections for [grouping], kept on this phone through relaunches, as iOS keeps them (#4338 A6). */
    suspend fun folds(grouping: AgentGrouping, folded: Set<String>) { store.edit { it[foldsKey(grouping)] = folded } }
    /** "Expand all" opens every tab group too, as it opens every space (#2792). */
    suspend fun openAllTabs() { store.edit { it.remove(foldedTabsKey) } }
    suspend fun togglePin(id: String) = hub.togglePin(id)
    suspend fun toggleMute(id: String) = hub.toggleMute(id)
    suspend fun markSeen(sessions: List<Session>) {
        sessions.forEach { hub.markSeen(it.key, it.readVersion) }
    }
}

private val foldedTabsKey = stringSetPreferencesKey("folded-tabs")
private val compactKey = booleanPreferencesKey("compact")
private val rowStyleKey = stringPreferencesKey("row-style")
private val seenRowsKey = stringPreferencesKey("agents-seen-rows")
private val seenRowsNoticedKey = booleanPreferencesKey("agents-seen-rows-noticed")
private val legacyHideBotmux = booleanPreferencesKey("hide-botmux")
private val migrationScope = stringPreferencesKey("hide-botmux-migration-scope")
private fun showBotmuxKey(scope: String) = booleanPreferencesKey("agents-show-botmux:$scope")
private fun useHerdrTabNameKey(scope: String) = booleanPreferencesKey("agents-use-herdr-tab-name:$scope")
private fun Preferences.ownsLegacy(pair: AgentsDisplayHub) = this[migrationScope]?.let { it == pair.scope } ?: pair.selected
private fun foldsKey(grouping: AgentGrouping) = stringSetPreferencesKey("folds-${grouping.name}")

data class AgentsPreferencesSnapshot(
    val grouping: AgentGrouping = AgentGrouping.Repo,
    /** Compact List (Brief and Board: Compact) over the [rowStyle]. */
    val compact: Boolean = false,
    /** The row style Glass draws while the list is not compact ([AgentsPreferences.display]). */
    val rowStyle: AgentRowStyle = AgentRowStyle.Default,
    val sort: AgentSort = AgentSort.State,
    /** Hub namespaces whose sessions from botmux are hidden in the merged Agents list. */
    val hiddenBotmuxHubs: Set<String?> = emptySet(),
    /** Hub namespaces whose `use_herdr_tab_name` preference is enabled (#13899). */
    val herdrTabNameHubs: Set<String?> = emptySet(),
    /** The herdr tabs whose agents the list folds to the tab's row ([AgentTab.foldKey]); every other tab is open. */
    val foldedTabs: Set<String> = emptySet(),
    /** Each grouping's folded sections and computers ([AgentsPreferences.folds]). */
    val folds: Map<AgentGrouping, Set<String>> = emptyMap(),
    val pins: AgentPreferences = AgentPreferences(),
    val mutes: AgentPreferences = AgentPreferences(),
    val seen: Map<String, String> = emptyMap(),
    val range: AgentRange = AgentRange.Running,
    val seenRows: AgentSeenRows = AgentSeenRows.Stay,
    val seenRowsNoticed: Boolean = false,
) {
    internal fun hides(session: Session) = session.isBotmux && GroupKeys.namespaceOf(session.machineId) in hiddenBotmuxHubs
    fun prefersHerdrTabName(session: Session): Boolean = GroupKeys.namespaceOf(session.machineId) in herdrTabNameHubs
    /** What Glass draws the agents as (iOS `AgentListDisplay`); Brief and Board read [compact] alone. */
    val display: AgentListDisplay get() = AgentListDisplay.of(rowStyle, compact)
}

/** Shared metadata supplied by the composition root for the active hub scope. */
data class AgentsMetadata(
    val rows: Map<SessionKey, AgentRowMetadata> = emptyMap(),
    val requestSources: Map<String, String> = emptyMap(),
)

data class AgentRowMetadata(
    val draft: String = "",
    val hasAttachments: Boolean = false,
    val needsConfirmation: Boolean = false,
    val pendingCount: Int = 0,
    val sending: Boolean = false,
    val verifying: Boolean = false,
) {
    /** Where the row's outgoing messages stand, in [res]' language. */
    fun deliveryLabels(res: Resources): List<String> = buildList {
        if (needsConfirmation) add(res.getString(if (verifying) PendingStatus.Verifying.labelRes else PendingStatus.Unconfirmed.labelRes))
        if (pendingCount > 0) add("${res.getString(PendingStatus.Waiting.labelRes)} · $pendingCount")
        if (sending) add(res.getString(PendingStatus.Sending.labelRes))
    }
    /** A draft or an attachment waits in the composer; the row says which (AgentsScreen.kt). */
    val hasDraft: Boolean get() = draft.isNotBlank() || hasAttachments
}

internal val Session.readVersion: String get() = "${updatedAt.orEmpty()}|${rev ?: ""}"

/**
 * The session as the list shows it: one seen on this phone and not yet recorded by the hub is seen at once, no longer
 * Unread nor Just finished (#9786, the web's `seenCopy`).
 */
fun Session.listSession(preferences: AgentsPreferencesSnapshot): AgentSession {
    val unseen = agentState == AgentState.Unread || work?.justFinished == true
    val visible = if (unseen && preferences.seen[key.path] == readVersion)
        copy(state = if (agentState == AgentState.Unread) "idle" else state, work = work?.copy(justFinished = false))
    else this
    return AgentSession(visible, stateSince, (runningSubagents ?: subagents.orEmpty().count { it.running == true }) > 0)
}

/**
 * The [kept] rows [previous] drew as rows of their own. They stay so while the quick filter remains selected: a subtask
 * that needed the user, opened and answered, would otherwise hang under its parent again and leave the list at once
 * (#4494), where [retainOpenedRows] keeps every other opened row. One opened from under "Subtasks" stays there.
 */
internal fun openedRowsOfTheirOwn(previous: AgentListResult?, kept: Set<String>): Set<String> =
    if (previous == null || kept.isEmpty()) emptySet()
    else previous.groups.flatMapTo(HashSet()) { group -> group.rows.mapNotNull { row -> row.id.takeIf { it in kept } } }

/** Keep opened rows in their old group and slot while the quick filter remains selected. */
internal fun retainOpenedRows(
    current: AgentListResult,
    previous: AgentListResult?,
    kept: Set<String>,
): AgentListResult {
    if (previous == null || kept.isEmpty()) return current
    val currentRows = current.groups.flatMap { it.rows }.associateBy { it.id }
    // "By repository": a repository with working trees takes no rows of its own, and a repository or tree group that is gone is
    // not brought back as a stray section outside its computer; such a row stays where the list put it now.
    val currentIds = current.groups.map { it.id }.toSet()
    val parents = current.groups.mapNotNull { it.under }.toSet()
    val homes = previous.groups.filter { it.id !in parents && (it.id in currentIds || it.repo == null) }.map { it.id }.toSet()
    val retained = kept.intersect(currentRows.keys).filter { id -> previous.groups.any { it.id in homes && it.rows.any { row -> row.id == id } } }.toSet()
    val groups = current.groups.map { group -> group.copy(
        rows = group.rows.filterNot { it.id in retained },
        visibleRows = group.visibleRows.filterNot { it.id in retained },
    ) }.toMutableList()
    previous.groups.forEachIndexed { groupIndex, old ->
        if (old.id !in homes) return@forEachIndexed
        val rowsToKeep = old.rows.filter { it.id in retained }
        if (rowsToKeep.isEmpty()) return@forEachIndexed
        val index = groups.indexOfFirst { it.id == old.id }
        val group = if (index >= 0) groups[index] else old.copy(rows = emptyList(), visibleRows = emptyList())
        fun insert(existing: List<AgentSession>, oldRows: List<AgentSession>): List<AgentSession> {
            val result = existing.toMutableList()
            oldRows.forEachIndexed { position, row ->
                if (row.id in retained) result.add(position.coerceAtMost(result.size), currentRows.getValue(row.id))
            }
            return result
        }
        val restored = group.copy(rows = insert(group.rows, old.rows), visibleRows =
            if (group.folded || current.machines.any { it.folded && group.id in it.groups }) emptyList()
            else insert(group.visibleRows, if (old.folded) old.rows else old.visibleRows))
        if (index >= 0) groups[index] = restored else groups.add(groupIndex.coerceAtMost(groups.size), restored)
    }
    // A repository with working trees holds no rows itself; it stays while a tree under it does.
    return current.copy(groups = groups.filter { group -> group.rows.isNotEmpty() || groups.any { it.under == group.id && it.rows.isNotEmpty() } })
}

internal fun Interaction.quickQuestion(): Question? = questions?.singleOrNull()?.takeIf {
    kind == "question" && !it.multiSelect && it.options.size in 1..4 && secret != true
}

internal fun Interaction.sourceLabel(res: Resources, metadata: AgentsMetadata): String? =
    (if (kind == "screen") res.getString(R.string.agents_source_terminal) else requestSourceLabel()?.resolve(res)) ?: metadata.requestSources[id]?.trim()?.takeIf(String::isNotEmpty)

internal fun countdown(expiresAt: String?, now: Instant): String? = RequestExpiry.label(expiresAt, now)?.shown
internal fun Interaction.expired(now: Instant): Boolean = RequestExpiry.expired(expiresAt, now)
internal fun relativeTime(res: Resources, time: Instant?, now: Instant): String {
    time ?: return ""
    val elapsed = Duration.between(time, now)
    fun count(id: Int, n: Long) = n.toInt().let { res.getQuantityString(id, it, it) }
    return when {
        elapsed.seconds < 60 -> res.getString(R.string.agents_time_now)
        elapsed.toMinutes() < 60 -> count(R.plurals.agents_time_minutes, elapsed.toMinutes())
        elapsed.toHours() < 24 -> count(R.plurals.agents_time_hours, elapsed.toHours())
        else -> count(R.plurals.agents_time_days, elapsed.toDays())
    }
}
