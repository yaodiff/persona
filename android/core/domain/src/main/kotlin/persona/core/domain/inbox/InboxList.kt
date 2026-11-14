package persona.core.domain.inbox

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import java.time.Instant
import persona.core.contracts.parseTime
import persona.core.domain.sort.AgentPreferences
import persona.core.domain.sort.AgentSession

/** List-only input; read flags and agent execution state do not determine triage or completion. */
data class InboxItem(
    val id: String,
    val account: String = "",
    val kind: String = "",
    val title: String = "",
    val sender: String? = null,
    val preview: String = "",
    val summary: String? = null,
    val tier: String? = null,
    val updatedAt: String? = null,
    val doneAt: String? = null,
    /** What the item holds as listed (the hub's `version`): a bulk "Done" sends it back. */
    val version: String? = null,
    val notice: String? = null,
    /**
     * The section the hub puts an open item in (inbox phase 1: action / working / fyi / noise); null from an older hub,
     * which sections by [tier] as before. [tier] keeps its meaning, so a working item still says what it was triaged as.
     */
    val status: String? = null,
    /** Urgent: someone is waiting or a deadline is near (it stays in Needs you while the AI prepares a suggestion). */
    val urgent: Boolean = false,
    /** Where the item happens, such as a chat or repository; absent from older hubs. */
    val where: String? = null,
) {
    val triage: InboxTier get() = InboxTier.entries.firstOrNull { it.wire == tier } ?: InboxTier.Action
    val isTriaged: Boolean get() = !tier.isNullOrEmpty()
    val isDone: Boolean get() = doneAt != null
    /** The hub says the AI or an Agent is on it, or it waits for the other side (In progress). */
    val isWorking: Boolean get() = status == WORKING
    /** The section of an open item: [status] when the hub sent a known one, else the [triage] tier. */
    val section: String get() = status?.takeIf { it in SECTIONS } ?: triage.wire
    /** Open and the user's to decide (Needs you). */
    val needsYou: Boolean get() = !isDone && section == InboxTier.Action.wire

    companion object {
        /** The id of the In progress section, between Needs you and FYI. */
        const val WORKING = "working"
        private val SECTIONS = setOf("action", WORKING, "fyi", "noise")
    }
}

enum class InboxTier(val wire: String, val title: CoreText) {
    Action("action", CoreText.of(Key.InboxAction)), Fyi("fyi", CoreText.of(Key.InboxFyi)), Noise("noise", CoreText.of(Key.InboxNoise))
}

data class InboxInteraction(
    val id: String,
    val machineId: String,
    val sessionId: String,
    val createdAt: String? = null,
    val sessionTitle: String = "",
    val summary: String? = null,
    val title: String = "",
    /** The manager that reported the card (docs/assistant/managers.md); null: the default manager. */
    val manager: String? = null,
) {
    val sessionKey: String get() = "$machineId/$sessionId"
}

data class InboxListSettings(
    val source: String = "",
    val kind: String = "",
    val query: String = "",
    val expanded: Set<String> = setOf("action", "fyi"),
)

enum class InboxRowType { Item, Interaction }
data class InboxRow(val type: InboxRowType, val id: String)
data class InboxGroup(
    val id: String,
    val title: CoreText,
    val rows: List<InboxRow>,
    val visibleRows: List<InboxRow>,
    val folded: Boolean,
) {
    val count: Int get() = rows.size
}
data class InboxListResult(
    val groups: List<InboxGroup>,
    val actionCount: Int,
    val filterCounts: Map<String, Int> = emptyMap(),
)

fun orderHubInbox(items: List<InboxItem>): List<InboxItem> = items.byTime(newestFirst = true, { it.updatedAt }, { it.id })

/**
 * Sorted by [time] (unset times last when newest first, first otherwise, as `compareBy` puts nulls), then by [id]. Each
 * time is parsed once up front, not twice per comparison: the inbox is sorted on the main thread (#932).
 */
private fun <T> List<T>.byTime(newestFirst: Boolean, time: (T) -> String?, id: (T) -> String): List<T> {
    val keyed = map { it to parseTime(time(it)) }
    val byInstant = if (newestFirst) compareByDescending<Pair<T, Instant?>> { it.second } else compareBy { it.second }
    return keyed.sortedWith(byInstant.thenBy { id(it.first) }).map { it.first }
}

/** Preserve cached ordering across different timestamps; sort only members sharing a real timestamp. */
private fun breakUpdateTies(items: List<InboxItem>): List<InboxItem> {
    val times = items.map { parseTime(it.updatedAt) }
    val ties = items.indices.filter { times[it] != null }.groupBy { times[it] }
        .mapValues { (_, members) -> members.map { items[it] }.sortedBy { it.id }.iterator() }
    return items.mapIndexed { i, item -> ties[times[i]]?.next() ?: item }
}

fun buildInboxList(
    items: List<InboxItem>,
    interactions: List<InboxInteraction> = emptyList(),
    sessions: List<AgentSession> = emptyList(),
    mutes: AgentPreferences = AgentPreferences(),
    settings: InboxListSettings = InboxListSettings(),
): InboxListResult {
    fun matchesText(vararg fields: String?): Boolean = settings.query.isEmpty() ||
        fields.any { it?.contains(settings.query, ignoreCase = true) == true }
    val shown = items.filter { item ->
        (settings.source.isEmpty() || item.account == settings.source) &&
            (settings.kind.isEmpty() || item.kind == settings.kind || settings.kind == "group" && item.kind == "bot" ||
                settings.kind == "mention" && item.kind == "moment" && item.notice?.startsWith("atIn") == true) &&
            matchesText(item.title, item.sender, item.preview, item.summary, item.where)
    }
    val bySession = sessions.associateBy { it.id }
    val waiting = if (settings.source.isEmpty() && settings.kind in setOf("", "agent")) {
        interactions.filter {
            !mutes.mutes(it.sessionKey, bySession[it.sessionKey]?.spacePreferenceKey) &&
                matchesText(it.sessionTitle, it.summary, it.title)
        }.byTime(newestFirst = false, { it.createdAt }, { it.id })
    } else emptyList()
    val groups = mutableListOf<InboxGroup>()
    fun addGroup(id: String, title: CoreText, rows: List<InboxRow>) {
        if (rows.isEmpty()) return
        // In progress is the AI's, not the user's to sort through: it always shows what is in it, as the app always has.
        val folded = id != InboxItem.WORKING && id !in settings.expanded
        groups += InboxGroup(id, title, rows, if (folded) emptyList() else rows, folded)
    }
    // Needs you · In progress · FYI · Collapsed by each open item's section; In progress has items only from a hub that sends `status`.
    val sections = listOf(InboxTier.Action.wire to InboxTier.Action.title, InboxItem.WORKING to CoreText.of(Key.InboxWorking),
        InboxTier.Fyi.wire to InboxTier.Fyi.title, InboxTier.Noise.wire to InboxTier.Noise.title)
    for ((id, title) in sections) {
        val rows = breakUpdateTies(shown.filter { !it.isDone && it.section == id })
            .map { InboxRow(InboxRowType.Item, it.id) }
        val agents = if (id == InboxTier.Action.wire) waiting.map { InboxRow(InboxRowType.Interaction, it.id) } else emptyList()
        addGroup(id, title, agents + rows)
    }
    val done = shown.filter { it.isDone }
        .byTime(newestFirst = true, { it.doneAt }, { it.id })
        .map { InboxRow(InboxRowType.Item, it.id) }
    addGroup("done", CoreText.of(Key.InboxDone), done)
    return InboxListResult(groups, items.count { it.needsYou })
}
