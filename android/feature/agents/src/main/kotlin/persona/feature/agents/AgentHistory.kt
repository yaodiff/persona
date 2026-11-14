package persona.feature.agents

import androidx.datastore.preferences.core.stringPreferencesKey
import persona.core.contracts.Machine
import persona.core.domain.sort.*

/** Which sessions the list shows; its words are `AgentRange.label` (AgentsScreen.kt). */
enum class AgentRange { Running, All, History }

internal fun agentRangeKey(scope: String) = stringPreferencesKey("agent-range:$scope")

/** [ownRows]: rows opened under the quick filter while they were rows of their own, which stay so ([openedRowsOfTheirOwn]). */
internal fun scopedAgentList(
    rows: List<AgentSession>, machines: List<Machine>, preferences: AgentsPreferencesSnapshot, settings: AgentListSettings,
    todos: List<persona.core.contracts.Todo> = emptyList(), ownRows: Set<String> = emptySet(),
    /** The hub's direction tree, for "By direction" (#4352). */
    directions: persona.core.contracts.DirectionTree? = null,
): AgentListResult {
    // The rows this range's list of running sessions draws, where it has one: a subtask among them hangs under its
    // parent only while a row above it is among them too, so a working agent whose creator was let go is not lost with
    // it (#4494, codexTaskForest). They are what scopedRootAgentList hands buildFlatAgentList, and what that draws.
    val allRunning = settings.copy(includeCodexHistory = true)
    val runs: ((AgentSession) -> Boolean)? = when (preferences.range) {
        AgentRange.Running -> { row -> settings.draws(row, preferences.pins) }
        AgentRange.All -> { row -> row.session.runsInAll && allRunning.draws(row, preferences.pins) }
        AgentRange.History -> null
    }
    val tree = codexTaskForest(foldRows(rows, settings.hierarchy), ownRows, runs)
    return scopedRootAgentList(tree.roots, machines, preferences, tree.searchSettings(settings), todos, directions)
        .let { it.copy(children = tree.children.plusChildren(it.children)) }
}

/** "All" lists these with the running ones (Codex threads of the past too), every other session with the history. */
private val persona.core.contracts.Session.runsInAll: Boolean get() = isLive || isCodexTask ||
    (children != null && foldedState != persona.core.contracts.AgentState.Offline)

/** [rows] are a forest's roots ([scopedAgentList]), so no list here hangs subtasks again. */
private fun scopedRootAgentList(
    rows: List<AgentSession>, machines: List<Machine>, preferences: AgentsPreferencesSnapshot, settings: AgentListSettings,
    todos: List<persona.core.contracts.Todo> = emptyList(),
    directions: persona.core.contracts.DirectionTree? = null,
): AgentListResult {
    if (preferences.range == AgentRange.Running)
        return buildFlatAgentList(rows, machines, preferences.pins, preferences.mutes, settings, todos, directions)
    val history = rows.filter { !it.session.isLive && !(preferences.range == AgentRange.All && it.session.isCodexTask) && settings.admitsHistory(it.session) && !(settings.hideBotmux && it.session.isBotmux) }
    val filtered = history.filter { settings.quick.matches(it) }
    val groups = filtered.groupBy { it.session.machineId to if (it.session.isCodexTask) repoOf(it.session.cwd, it.session.repoRoot).repo else it.session.cwd }.map { (project, members) ->
        val id = "history/${project.first}/${project.second}"
        val ordered = members.sortedWith(compareByDescending<AgentSession> { it.updatedAt }.thenBy { it.id })
        val folded = id in settings.folded
        AgentListGroup(id, persona.core.contracts.CoreText.plain(project.second), ordered, if (folded) emptyList() else ordered, folded)
    }.sortedWith(compareByDescending<AgentListGroup> { it.rows.first().updatedAt }.thenBy { it.id })
    val running = if (preferences.range == AgentRange.All)
        buildFlatAgentList(rows.filter { it.session.runsInAll }, machines, preferences.pins, preferences.mutes, settings.copy(includeCodexHistory = true), todos, directions)
    else AgentListResult(emptyMap(), emptyList(), emptyList())
    return running.copy(groups = running.groups + groups, counts = AgentQuickFilter.entries.associateWith { filter ->
        (running.counts[filter] ?: 0) + history.count { !it.isNestedSubtask && filter.matches(it) }
    })
}

private fun AgentListSettings.admitsHistory(s: persona.core.contracts.Session): Boolean =
    !s.assistant && (machineFilter.isEmpty() || machineFilter == s.machineId) &&
        (query.isEmpty() || s.key.path in queryAncestors || listOf(s.displayTitle, s.cwd, s.lastText, s.branch).any { it?.contains(query, true) == true })

/**
 * The botmux sessions the "Show botmux" switch counts: what [scopedAgentList] would count under "Lark groups" with botmux shown,
 * without building the list again (it runs on every push).
 */
internal fun botmuxCount(rows: List<AgentSession>, preferences: AgentsPreferencesSnapshot, settings: AgentListSettings): Int =
    foldRows(rows).count { row ->
        val s = row.session
        s.isBotmux && when (preferences.range) {
            AgentRange.Running -> settings.admits(s)
            AgentRange.All -> if (s.isLive) settings.admits(s) else settings.admitsHistory(s)
            AgentRange.History -> !s.isLive && settings.admitsHistory(s)
        }
    }
