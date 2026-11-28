package persona.core.domain.sort

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.AgentState
import persona.core.contracts.DirectionTree
import persona.core.contracts.Machine
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.contracts.api.AgentWorkspace
import persona.core.contracts.state

/** Kept IDs retain membership, not a previous row position. Reselecting a filter replaces kept. */
data class AgentListSettings(
    val grouping: AgentGrouping = AgentGrouping.Repo,
    val quick: AgentQuickFilter = AgentQuickFilter.All,
    val machineFilter: String = "",
    val query: String = "",
    val queryAncestors: Set<String> = emptySet(),
    val includeCodexHistory: Boolean = false,
    val kept: Set<String> = emptySet(),
    val folded: Set<String> = emptySet(),
    val expandedIdle: Set<String> = emptySet(),
    val expandedQuiet: Set<String> = emptySet(),
    /** "By state" (default) or "Recently updated" (iOS `AgentSort`, kept on this phone). */
    val sort: AgentSort = AgentSort.State,
    /** botmux's sessions hidden from this list (web #2786, kept on this phone); "Lark group" shows them again. */
    val hideBotmux: Boolean = false,
    /** sessions_hierarchy: a folded child has its parent's row; an orphan subagent has no row or count. */
    val hierarchy: Boolean = false,
    /** UI-only row-cap expansion, distinct from [expandedIdle] so Show All never unfolds idle agents. */
    val expandedAll: Set<String> = emptySet(),
    /** The caller's instant for `since:`; the list projection never reads a clock. */
    val now: java.time.Instant = java.time.Instant.now(),
    /** Read-only source catalog for the repository tree; no preference is changed by a source pin. */
    val workspaces: List<persona.core.contracts.api.AgentWorkspace> = emptyList(),
    /** Which tap produced this list. A page built before the tap carries an older one (#9600). */
    val tapEpoch: Int = 0,
    /**
     * Grouping chosen by a tap before the phone has finished saving it (`AgentsMenus.swift:405`).
     * Null: use the saved grouping. Cleared once the save lands, or set back when the save fails
     * (`PinsStore.swift:131`, `:139`, `:163`).
     */
    val pendingGrouping: AgentGrouping? = null,
    /**
     * Tabs folded by a tap before the phone has finished saving them (`ChatsView.swift:2080`).
     * Null: use the saved set.
     */
    val pendingFoldedTabs: Set<String>? = null,
    /** Where rows seen during this visit stay until the next list refresh. */
    val heldSeen: Map<String, AgentSeenPlace> = emptyMap(),
) {
    private val nativePinnedWorkspaces by lazy {
        workspaces.filter { it.inRepositoryCatalog && it.nativePinTime != null }
            .mapTo(mutableSetOf()) { it.repoWorkspaceKey }
    }

    /** A closed, unarchived chat still belongs to the workspace its user pinned on the host. */
    fun nativePinned(s: persona.core.contracts.Session): Boolean = grouping == AgentGrouping.Repo &&
        !s.archived && s.repoWorkspaceKey() in nativePinnedWorkspaces
}

/**
 * How the list orders its agents (iOS `AgentSort`): the state first, or the latest update only. Under "By state" the
 * sections stay the states; only the rows' order inside them is by time either way.
 */
enum class AgentSort {
    State, Updated;
    companion object {
        fun fromPreference(value: String?): AgentSort = entries.firstOrNull { it.name == value } ?: State
    }
}

enum class AgentGrouping {
    Repo, MachineSpace, State, Todo,
    /**
     * By Machine (#9319, iOS `DevGrouping.machine`): one foldable section per computer with live agents, flat rows in the
     * list's sort order, idle agents behind "N more idle".
     */
    Machine,
    /** By App (#9199, iOS `DevGrouping.app`): a section per desktop app, then terminal engines, then Other. */
    App,
    /** Recently Used (#9199, iOS `DevGrouping.recent`): one flat latest-first list, no group headings. */
    Recent,
    /** The hub's direction tree (#4352): direction › to-do › agent, the rest under "No to-do"; never the default. */
    Direction,
    // Retained for callers of the original list projection; a stored "Space" preference migrates to MachineSpace.
    Space;

    companion object {
        /** A phone that never chose a grouping gets "By repository" (#2252, as iOS #2108); any stored choice keeps its own. */
        fun fromPreference(value: String?): AgentGrouping = when (value) {
            null, Repo.name -> Repo
            State.name -> State
            Todo.name -> Todo
            Machine.name -> Machine
            App.name -> App
            Recent.name -> Recent
            Direction.name -> Direction
            else -> MachineSpace
        }
        /**
         * Menu order matches iOS `DevGrouping` relative to the groupings Android shows: By Machine after By to-do and
         * before By App / Recently Used / By pinned (#9319).
         */
        val selectable = listOf(Repo, MachineSpace, State, Todo, Machine, App, Recent, Direction)
    }
}

/** A group's place in "By repository": a repository, or one of its working trees (the main checkout or another). */
enum class RepoLevel { Repository, MainCheckout, Worktree }
enum class AgentQuickFilter(val state: AgentState?, val source: AgentSource? = null) {
    All(null), NeedsYou(AgentState.NeedsYou), Unread(AgentState.Unread), Working(AgentState.Working),
    /**
     * Working, but all it still runs in the background only waits (#6045, iOS `AgentFilter.backgroundWaiting`): a filter of
     * its own, and not Working. The hub has no state for it: a paged list asks for working rows ([state]) and keeps these.
     */
    BackgroundWaiting(AgentState.Working),
    /** Where the agent was opened (#2526): a botmux session (its Feishu group) or a herdr pane. */
    Feishu(null, AgentSource.Feishu), Terminal(null, AgentSource.Terminal);
    fun matches(session: AgentSession) = when {
        source != null -> session.session.agentSource == source
        this == BackgroundWaiting -> session.waitsInBackground
        this == Working -> session.state == AgentState.Working && !session.waitsInBackground
        else -> state == null || session.state == state
    }
    companion object {
        /**
         * "All", and the filters with agents right now; the chosen one stays while it empties, so it can be left (iOS
         * `QuickFilterBar`, web `shownFilters`). When the rest still do not fit a row, the bar scrolls sideways.
         */
        fun shown(selected: AgentQuickFilter, counts: Map<AgentQuickFilter, Int>): List<AgentQuickFilter> =
            entries.filter { it == All || (it != Feishu && it == selected) || (counts[it] ?: 0) > 0 }
    }
}

data class AgentListGroup(
    val id: String,
    val title: CoreText,
    val rows: List<AgentSession>,
    val visibleRows: List<AgentSession>,
    val folded: Boolean = false,
    val parent: String? = null,
    val stateCounts: Map<AgentState, Int>? = null,
    val spoken: CoreText? = null,
    /** "By repository": the repository or working tree this group is. */
    val repo: RepoLevel? = null,
    /** The group this one sits in: a working tree's repository, whose own [rows] are then empty (its trees hold them). */
    val under: String? = null,
    /** "By repository": what a working tree's line says beside its name (#4338 A7); null for every other group. */
    val worktree: WorktreeLine? = null,
    /** Server membership for this exact group; null means its rows/counts describe the loaded window. */
    val totalCount: Int? = null,
    /**
     * The idle agents a busy working tree (By Repository) or space (By project) folds behind its quiet line or its "N more
     * idle" row (#4338 A8); null for none, and while it is a quiet group, searched or under a quick filter.
     */
    val idleFold: IdleFold? = null,
    /**
     * How many of [stateCounts]'s (or [rows]') working agents only wait in the background (#6045): a heading counts them
     * apart from Working, with a clock, as iOS's `StateCounts` does.
     */
    val waitingInBackground: Int = 0,
    /** The exact source target when a Herdr workspace shares its repository heading. */
    val workspace: AgentWorkspace? = null,
    /** The local folder for a heading in By project, when its session rows identify one. */
    val projectFolder: AgentProjectFolder? = null,
) {
    val heldSeen: List<String> get() = rows.mapNotNull { row -> row.id.takeIf { row.seenPlace != null } }
    val sectionCount: Int get() = totalCount ?: rows.count { it.seenPlace == null }
}

data class AgentListResult(
    val counts: Map<AgentQuickFilter, Int>,
    val groups: List<AgentListGroup>,
    val unreadShown: List<String>,
    val quietTitle: CoreText? = null,
    val machines: List<AgentMachineGroup> = emptyList(),
    val children: Map<String, List<AgentSession>> = emptyMap(),
)

data class AgentMachineGroup(
    val machine: Machine,
    val groups: List<String>,
    val sessionCount: Int,
    val spaceCount: Int,
    val stateCounts: Map<AgentState, Int>,
    val folded: Boolean,
    /** "By repository": how many repositories it has. */
    val repoCount: Int? = null,
    /** How many of [stateCounts]'s working only wait in the background (#6045): its heading counts them apart. */
    val waitingInBackground: Int = 0,
) {
    val id: String get() = "machine/${machine.id}"
}

/**
 * State First's order, the index into By Status's sections ([AgentStateSections], iOS `AgentListOrder.rank`): Needs You →
 * Unread → Awaiting Reply → Working → Waiting in the background ([AgentSession.waitsInBackground]) → Just finished →
 * Waiting → Muted → Seen.
 * Muted agents that are working, finished-unread or waiting rank as Muted; a muted one that only waits for the user's
 * reply or is just finished is just Seen ([AgentSession.awaitsReply], [AgentSession.justFinished]); one that needs you stays first.
 */
fun agentRank(session: AgentSession, mutes: AgentPreferences = AgentPreferences()): Int = when {
    session.state == AgentState.NeedsYou -> 0
    (session.state == AgentState.Working || session.state == AgentState.Unread ||
        session.isQuiet && session.heldInList || session.isWaiting) && mutes.mutes(session) -> 7
    session.state == AgentState.Unread || session.isQuiet && session.heldInList -> 1
    session.awaitsReply(mutes) -> 2
    session.state == AgentState.Working -> if (session.waitsInBackground) 4 else 3
    session.isWaiting -> 6
    session.justFinished(mutes) -> 5
    else -> 8
}

/** By Status's sections in rank order ([agentRank]): each one's key (its group is `state/<key>`) and title (iOS `stateTitles`). */
val AgentStateSections: List<Pair<String, CoreText.Key>> = listOf("needs" to CoreText.Key.StateNeedsYou, "unread" to CoreText.Key.StateUnread,
    "awaiting" to CoreText.Key.StateAwaitingReply, "working" to CoreText.Key.StateWorking, "background" to CoreText.Key.StateBackgroundWaiting,
    "just_finished" to CoreText.Key.StateJustFinished, "waiting" to CoreText.Key.StateWaiting,
    "muted" to CoreText.Key.StateMuted, "idle" to CoreText.Key.StateIdle)

/** By Status's Seen, the long tail: it starts folded, and its id is kept in the folds once it is opened (iOS `foldedByDefault`). */
const val StateSeenGroup = "state/idle"
const val StateSeenOpened = "!state/idle"

/**
 * The list's own filter before the quick filter and the botmux switch: not the assistant, the machine, the search ([AgentQuery]:
 * its words in the session's fields, the branch among them, #4497, and its conditions, #4338 A10). [pins] and
 * [machineNames] are what `is:pinned` and `machine:` read; [now] dates `since:`.
 */
fun AgentListSettings.admits(s: persona.core.contracts.Session, pins: AgentPreferences = AgentPreferences(),
                             machineNames: Map<String, String> = emptyMap(), now: java.time.Instant = java.time.Instant.now()): Boolean =
    !s.assistant && (machineFilter.isEmpty() || machineFilter == s.machineId) &&
        (query.isEmpty() || s.key.path in queryAncestors ||
            AgentQuery.parse(query).admits(s, s.key.path in pins.sessions, machineNames[s.machineId], now))

/** The list before the query's words: its conditions still scope it, but no word alone moves a project heading. */
private fun AgentListSettings.scopeAdmits(s: persona.core.contracts.Session, query: AgentQuery?, pins: AgentPreferences,
                                          machineNames: Map<String, String>, now: java.time.Instant): Boolean =
    !s.assistant && (machineFilter.isEmpty() || machineFilter == s.machineId) &&
        (query == null || s.key.path in queryAncestors ||
            query.admits(s, s.key.path in pins.sessions, machineNames[s.machineId], now, words = false))

private fun AgentListSettings.admitsWords(s: persona.core.contracts.Session, query: AgentQuery?, pins: AgentPreferences,
                                          machineNames: Map<String, String>, now: java.time.Instant): Boolean =
    query == null || s.key.path in queryAncestors ||
        query.admits(s, s.key.path in pins.sessions, machineNames[s.machineId], now)

/**
 * Whether the list of running sessions draws [row], its search, quick filter and botmux switch aside: pinned (under
 * Pinned), live, or a Codex thread of the past while those are included. [buildFlatAgentList] draws these rows, and a
 * subtask among them needs one of them above it to hang under ([codexTaskForest]).
 */
fun AgentListSettings.draws(row: AgentSession, pins: AgentPreferences): Boolean =
    row.id in pins.sessions || row.isLive || nativePinned(row.session) || includeCodexHistory && row.session.isCodexTask

fun buildAgentList(
    sessions: List<AgentSession>, machines: List<Machine> = emptyList(),
    pins: AgentPreferences = AgentPreferences(), mutes: AgentPreferences = AgentPreferences(),
    settings: AgentListSettings = AgentListSettings(), todos: List<Todo> = emptyList(),
    /** The hub's direction tree, which "By direction" ([AgentGrouping.Direction]) is built from; null: none to group by. */
    directions: DirectionTree? = null,
    /** The open agent that was Unread when opened (iOS `AgentListOrder.held`), until its chat is left. */
    held: String? = null,
): AgentListResult {
    val listed = foldRows(sessions.map { it.copy(heldInList = it.id == held) }, settings.hierarchy)
    // By App and Recently Used list every agent (offline included), so a subtask hangs under its parent even when the
    // parent is offline (iOS `CodexTasks.build` with `runs: nil`).
    val runs: ((AgentSession) -> Boolean)? = when (settings.grouping) {
        AgentGrouping.App, AgentGrouping.Recent -> null
        else -> { row -> settings.draws(row, pins) }
    }
    val tree = codexTaskForest(listed, runs = runs)
    val names = machines.associate { it.id to it.name }
    // A side session is never a row (#7944): a search that finds one keeps its topmost parent's row instead.
    val search = tree.searchSettings(settings, pins, names).let { s ->
        sideSearchHits(sessions, listed, s, names).takeIf { it.isNotEmpty() }?.let { s.copy(queryAncestors = s.queryAncestors + it) } ?: s
    }
    return buildFlatAgentList(tree.roots, machines, pins, mutes, search, todos, directions)
        .let { it.copy(children = tree.children.plusChildren(it.children)) }
}

/** [this] list's children with [more] added under the same parents (each child once). */
fun Map<String, List<AgentSession>>.plusChildren(more: Map<String, List<AgentSession>>): Map<String, List<AgentSession>> =
    if (more.isEmpty()) this else (keys + more.keys).associateWith { id -> (this[id].orEmpty() + more[id].orEmpty()).distinctBy { it.id } }

/**
 * The list of [sessions] as they come, no subtask hung under another here: the roots of the [codexTaskForest] its caller
 * built for the rows it draws, whose subtasks the caller hangs under them ([AgentListResult.children]).
 */
fun buildFlatAgentList(
    sessions: List<AgentSession>,
    machines: List<Machine> = emptyList(),
    pins: AgentPreferences = AgentPreferences(),
    mutes: AgentPreferences = AgentPreferences(),
    settings: AgentListSettings = AgentListSettings(),
    todos: List<Todo> = emptyList(),
    directions: DirectionTree? = null,
): AgentListResult {
    val machineNames = machines.associate { it.id to it.name }
    val roots = foldRows(sessions, settings.hierarchy)
    val query = settings.query.takeIf(String::isNotEmpty)?.let { AgentQuery.parse(it) }
    val scoped = roots.filter {
        settings.scopeAdmits(it.session, query, pins, machineNames, settings.now) &&
            !(settings.hideBotmux && it.session.isBotmux)
    }
    val base = scoped.filter { settings.admitsWords(it.session, query, pins, machineNames, settings.now) }
    val counts = AgentQuickFilter.entries.filter { it != AgentQuickFilter.All }
        .associateWith { filter ->
            base.count { row -> !row.isNestedSubtask && filter.matches(row) &&
                (filter != AgentQuickFilter.Feishu || settings.grouping == AgentGrouping.Recent || settings.draws(row, pins)) }
        }
    val filtered = base.map { it.placingSeenRow(settings.heldSeen[it.id], mutes) }.filter {
        settings.quick.matches(it) || it.id in settings.kept ||
            settings.quick == AgentQuickFilter.Unread && it.seenPlace == AgentSeenPlace.Unread
    }
    val pinned = filtered.filter { it.id in pins.sessions }.sortedBy { pins.sessions.indexOf(it.id) }
    val live = filtered.filter { it.id !in pins.sessions && settings.draws(it, pins) }
    val quick = settings.quick != AgentQuickFilter.All
    // Under a quick filter, or sorted by "Recently updated", rows go by time only (iOS `AgentListOrder.byRank`).
    val byRank = !quick && settings.sort == AgentSort.State
    val latest = settings.sort == AgentSort.Updated
    val comparator = compareBy<AgentSession> { if (byRank) agentRank(it, mutes) else 0 }
        .thenByDescending { it.sortDate }.thenBy { it.session.sessionId }.thenBy { it.id }
    fun ordered(rows: List<AgentSession>) = rows.sortedWith(comparator)
    fun stays(row: AgentSession) = row.stays(mutes)
    fun showIdle(key: String) = key in settings.expandedIdle || settings.query.isNotEmpty()
    // The working ones among [rows] that only wait in the background: a heading counts them apart from Working.
    fun waiting(rows: List<AgentSession>) = rows.count { !it.isNestedSubtask && it.waitsInBackground }
    fun group(id: String, title: CoreText, rows: List<AgentSession>, visible: List<AgentSession>): AgentListGroup {
        val folded = id in settings.folded
        return AgentListGroup(id, title, rows, if (folded) emptyList() else visible, folded, waitingInBackground = waiting(rows))
    }
    val result = mutableListOf<AgentListGroup>()
    if (pinned.isNotEmpty()) result += if (settings.grouping == AgentGrouping.MachineSpace || settings.grouping == AgentGrouping.Repo)
        group("pinned", CoreText.of(Key.AgentsPinned), pinned, pinned)
        else AgentListGroup("pinned", CoreText.of(Key.AgentsPinned), pinned, pinned, waitingInBackground = waiting(pinned))
    var quietTitle: CoreText? = null
    fun stateCounts(rows: List<AgentSession>) = AgentState.entries.associateWith { state ->
        rows.count { !it.isNestedSubtask && it.state == state }
    }
    // "By direction"'s parts (#4352): a heading holds no rows itself (the to-dos under it do).
    fun leaf(id: String, title: CoreText, members: List<AgentSession>, under: String) {
        val rows = ordered(members)
        val folded = id in settings.folded
        result += AgentListGroup(id, title, rows, if (folded) emptyList() else rows, folded, stateCounts = stateCounts(rows), under = under)
    }
    fun heading(id: String, title: CoreText, members: List<AgentSession>, folded: Boolean) {
        result += AgentListGroup(id, title, emptyList(), emptyList(), folded, stateCounts = stateCounts(members))
    }
    // "By to-do"'s flat sections (#4338 A5), and "No to-do" in "By to-do" and "By direction".
    fun todoSection(id: String, title: CoreText, members: List<AgentSession>, folded: Boolean) {
        val rows = ordered(members)
        result += AgentListGroup(id, title, rows, if (folded) emptyList() else rows, folded, stateCounts = stateCounts(rows))
    }
    /** "No to-do" after the sections above ([above]): folded under them unless something in it needs you, open alone. */
    fun noTodo(rest: List<AgentSession>, above: Boolean) {
        if (rest.isEmpty()) return
        // Open with none above, as it is the whole list (iOS `TodoAgentGroup.folded`). The user's own fold wins either way
        // ([TodoNoneOpened] once they opened it), and a search opens it.
        val byDefault = above && rest.none { it.state == AgentState.NeedsYou }
        val folded = settings.query.isEmpty() &&
            (TodoNoneGroup in settings.folded || (byDefault && TodoNoneOpened !in settings.expandedIdle))
        todoSection(TodoNoneGroup, CoreText.of(Key.AgentsNoTodo), rest, folded)
    }
    // Agents the direction tree puts under another agent of the list (#4352): drawn as that agent's children.
    val placedChildren = mutableMapOf<String, List<AgentSession>>()
    when (settings.grouping) {
        AgentGrouping.Repo -> {
            // Computer → repository → working tree → sessions, from each cwd (web `buildRepoMachineTree`, iOS `RepoTree`).
            // A repository with nothing going on is quiet: folded, after the busy ones, like a quiet space; a quiet working
            // tree is folded too but keeps its place (main checkout first, then by name, as on iOS).
            fun quiet(members: List<AgentSession>) = !quick && members.none(::stays)
            fun section(id: String, title: CoreText, members: List<AgentSession>, level: RepoLevel, under: String?, machineFolded: Boolean,
                        tree: AgentWorktree? = null, online: Boolean = true): AgentListGroup {
                val nativePinned = tree?.pinnedAt != null
                val carriesWorkspace = level == RepoLevel.Repository && tree?.workspace?.source == "herdr"
                val isQuiet = !nativePinned && !carriesWorkspace && quiet(members)
                val rows = ordered(members)
                val folded = if (isQuiet) id !in settings.expandedQuiet && settings.query.isEmpty() else id in settings.folded
                val visible = when {
                    folded || machineFolded -> emptyList()
                    isQuiet || nativePinned || quick || showIdle(id) -> rows
                    else -> rows.filter(::stays)
                }
                // Its idle agents behind one line, as iOS's `agentRows`; a search shows them with no line.
                val idle = if (isQuiet || nativePinned || quick || settings.query.isNotEmpty()) emptyList() else rows.filterNot(::stays)
                return AgentListGroup(id, title, rows, visible, folded, if (isQuiet) "quiet" else null, stateCounts(rows), repo = level, under = under,
                    worktree = tree?.let { worktreeLine(it, isQuiet, rows.count { row -> !row.isNestedSubtask && row.seenPlace == null }, online) },
                    idleFold = idle.takeIf { it.isNotEmpty() }?.let { IdleFold(it, open = id in settings.expandedIdle) },
                    waitingInBackground = waiting(rows),
                    workspace = tree?.workspace?.takeIf { carriesWorkspace })
            }
            val shownWorkspaces = live.mapNotNull { it.session.repoWorkspaceKey() }.toSet()
            val catalog = settings.workspaces.filter { w ->
                w.inRepositoryCatalog && (settings.machineFilter.isEmpty() || w.machineId == settings.machineFilter) &&
                    ((!quick && settings.query.isEmpty()) || w.repoWorkspaceKey in shownWorkspaces)
            }
            val knownRows = roots.filter { !it.session.assistant && !it.session.archived }.groupBy { it.session.machineId }
            val known = machines + (live.map { it.session.machineId } + catalog.map { it.machineId }).distinct()
                .filter { id -> machines.none { it.id == id } }.map { Machine(it, "") }
            val machineGroups = known.sortedBy { !it.online }.mapNotNull { machine ->
                val members = live.filter { it.session.machineId == machine.id }
                val workspaces = catalog.filter { it.machineId == machine.id }
                if (members.isEmpty() && workspaces.isEmpty()) return@mapNotNull null
                val machineFolded = "machine/${machine.id}" in settings.folded
                // "Recently updated": the busy repositories by their latest agent (iOS `RepoTree` under `AgentSort.updated`).
                val repos = agentRepos(members, workspaces, mapOf(machine.id to machine.home), knownRows[machine.id].orEmpty()).let { all ->
                    if (latest) all.sortedWith(compareByDescending<AgentRepo> { it.pinnedAt }.thenBy { quiet(it.members) }
                        .thenByDescending { repo -> repo.members.mapNotNull { it.sortDate }.maxOrNull() }.thenBy { it.id })
                    else all.sortedWith(compareByDescending<AgentRepo> { it.pinnedAt }.thenBy { quiet(it.members) })
                }
                if (repos.isEmpty()) return@mapNotNull null
                val groups = repos.flatMap { repo ->
                    // A quiet repository opens to all its sessions in one list, as iOS's "Idle repositories" does. A repository with only
                    // its main checkout is drawn as one level but keeps the checkout's id, so a session opened under a quick
                    // filter stays in the same group when a second working tree appears or goes (retainOpenedRows).
                    if (repo.flat) {
                        val tree = repo.worktrees.single()
                        listOf(section(tree.id, repoTitle(repo.name), repo.members, RepoLevel.Repository, null, machineFolded,
                            tree = tree.takeIf { it.workspace?.source == "herdr" }, online = machine.online))
                    } else if (repo.pinnedAt == null && repo.worktrees.none { it.workspace?.source == "herdr" } && quiet(repo.members)) {
                        listOf(section(repo.id, repoTitle(repo.name), repo.members, RepoLevel.Repository, null, machineFolded))
                    } else listOf(AgentListGroup(repo.id, repoTitle(repo.name), emptyList(), emptyList(), repo.id in settings.folded,
                        stateCounts = stateCounts(repo.members.filter { it.seenPlace == null }),
                        repo = RepoLevel.Repository, waitingInBackground = waiting(repo.members))) +
                        repo.worktrees.map { tree ->
                            section(tree.id, repoTitle(tree.name), tree.members, if (tree.main) RepoLevel.MainCheckout else RepoLevel.Worktree, repo.id, machineFolded,
                                tree, online = machine.online || machines.none { it.id == machine.id })
                        }
                }
                result += groups
                val counted = members.filter { !it.isNestedSubtask && it.seenPlace == null }
                AgentMachineGroup(machine, groups.map { it.id }, counted.size, agentSpaces(members).size, stateCounts(counted),
                    machineFolded, repoCount = repos.size, waitingInBackground = members.count { !it.isNestedSubtask && it.waitsInBackground })
            }
            return AgentListResult(counts, result, filtered.filter { it.state == AgentState.Unread }.map { it.id },
                machines = machineGroups)
        }
        AgentGrouping.MachineSpace -> {
            // Keep each session in a single leaf group; machine headers only reference those leaves.
            val treeRows = filtered.filter { it.isLive || settings.includeCodexHistory && it.session.isCodexTask }
            // Quick filters and search words choose the rows; project metadata still comes from the complete scope so a
            // minority folder cannot become the New Agent target.
            val scopeRows = scoped.filter { it.isLive || settings.includeCodexHistory && it.session.isCodexTask }
            val known = machines + treeRows.map { it.session.machineId }.distinct()
                .filter { id -> machines.none { it.id == id } }.map { Machine(it, "") }
            val machineGroups = known.sortedBy { !it.online }.mapNotNull { machine ->
                val members = treeRows.filter { it.session.machineId == machine.id }
                if (members.isEmpty()) return@mapNotNull null
                val scopeMembers = scopeRows.filter { it.session.machineId == machine.id }
                val nested = buildFlatAgentList(members, machines = listOf(machine), pins = pins, mutes = mutes,
                    settings = settings.copy(grouping = AgentGrouping.Space))
                val projectFolders = projectFolders(agentSpaces(scopeMembers, byProject = true), machines)
                val machineFolded = "machine/${machine.id}" in settings.folded
                result += nested.groups.filterNot { it.id == "pinned" }.map { group ->
                    val folded = group.folded || group.id in settings.folded
                    group.copy(folded = folded, visibleRows = if (machineFolded || folded) emptyList() else group.visibleRows,
                        projectFolder = projectFolders[group.id] ?: group.projectFolder)
                }
                val spaces = agentSpaces(members)
                val counted = members.filter { !it.isNestedSubtask && it.seenPlace == null }
                AgentMachineGroup(machine, nested.groups.filterNot { it.id == "pinned" }.map { it.id }, counted.size, spaces.size,
                    stateCounts(counted),
                    machineFolded, waitingInBackground = members.count { !it.isNestedSubtask && it.waitsInBackground })
            }
            return AgentListResult(counts, result, filtered.filter { it.state == AgentState.Unread }.map { it.id },
                machines = machineGroups)
        }
        AgentGrouping.State -> {
            // A section per rank, each folded from its header; Seen starts folded and its header is its one fold, with no
            // "N more idle" row inside (iOS `stateSections`). A search opens them all.
            val byRank = live.groupBy { agentRank(it, mutes) }
            AgentStateSections.forEachIndexed { rank, (key, title) ->
                val rows = ordered(byRank[rank].orEmpty())
                val id = "state/$key"
                val folded = settings.query.isEmpty() &&
                    (id in settings.folded || id == StateSeenGroup && StateSeenOpened !in settings.folded)
                if (rows.isNotEmpty()) result += AgentListGroup(id, CoreText.of(title), rows, if (folded) emptyList() else rows, folded)
            }
        }
        AgentGrouping.Todo -> {
            // As iOS's By To-do (`TodosLogic.buildTodoAgentGroups`, #4338 A5): a section per open to-do with agents on it, in
            // the to-dos' order; then "Opened by hand" (open) and "No to-do", one list each, as the web's todos.ts (#2608).
            val sessionMap = sessions.associateBy { it.session.key }
            val uncompletedTodos = todos.filter { !it.isDone }.sortedWith(
                compareBy<Todo> { it.state { k -> sessionMap[k]?.session }.rank }
                    .thenByDescending { it.created ?: java.time.Instant.EPOCH }
                    .thenBy { it.id }
            )
            // Each agent sits under one to-do only, as on the web (`assignedSessionKeys`): a to-do's agents list can still hold
            // an agent the session's own `todo` (pushed with the session, so newer) already moved to another, and one row twice
            // would repeat its list key. Unlike the web, which takes the first to-do listing it, the session's own open to-do
            // wins here, so a moved agent shows under its new to-do at once; else the first to-do listing it.
            val openIds = uncompletedTodos.map { it.id }.toSet()
            val home = live.associate { row ->
                val own = row.session.todo?.id?.takeIf { it.isNotEmpty() && it in openIds }
                row.session.key to (own ?: uncompletedTodos.firstOrNull { todo -> todo.currentAgents.any { it.key == row.session.key } }?.id)
            }
            val claimed = home.filterValues { it != null }.keys
            val linked = uncompletedTodos.mapNotNull { todo ->
                val matched = live.filter { home[it.session.key] == todo.id }
                if (matched.isEmpty()) null else todo to matched
            }
            linked.forEach { (todo, rows) -> todoSection("todo/${todo.id}", CoreText.plain(todo.title), rows, "todo/${todo.id}" in settings.folded) }
            val unlinked = live.filter { it.session.key !in claimed }
            // Opened by hand in herdr or Orca and on no to-do: a section of its own before "No to-do", open.
            val manual = unlinked.filter { it.session.isOpenedByHand }
            if (manual.isNotEmpty()) todoSection(TodoManualGroup, CoreText.of(Key.AgentsManual), manual, TodoManualGroup in settings.folded)
            // Folded under the sections above ("Opened by hand" included) unless something in it needs you.
            noTodo(unlinked.filterNot { it.session.isOpenedByHand }, above = linked.isNotEmpty() || manual.isNotEmpty())
        }
        AgentGrouping.Direction -> {
            // Direction › To-do › Agent, as the hub's tree has them (#4352): no regrouping here. Each agent shows once, where
            // the tree first has it; one the tree puts under another agent of the list is that agent's child. Agents the
            // tree does not have (or every agent, without a tree) go under "No to-do", so none goes missing.
            val byKey = live.associateBy { it.session.key }
            val placed = mutableSetOf<SessionKey>()
            fun take(key: SessionKey): AgentSession? = byKey[key]?.takeIf { placed.add(key) }
            var above = false
            directions?.directions.orEmpty().forEach { direction ->
                val id = DirectionGroupPrefix + direction.id
                val tasks = direction.tasks.mapNotNull { task ->
                    val rows = mutableListOf<AgentSession>()
                    fun place(agents: List<persona.core.contracts.DirectionAgent>, parent: AgentSession?) {
                        for (agent in agents) {
                            // An in-session sub-agent is part of its parent's row already.
                            val row = if (agent.isSubagent) null else take(agent.key)
                            if (row != null) {
                                if (parent == null) rows += row
                                else placedChildren[parent.id] = placedChildren[parent.id].orEmpty() + row
                            }
                            place(agent.children, row ?: parent)
                        }
                    }
                    place(task.agents, null)
                    if (rows.isEmpty()) null else task to rows
                }
                if (tasks.isEmpty()) return@forEach
                above = true
                val title = direction.title
                // Every agent under the heading counts, children of children too (each is placed once, so this ends).
                fun withChildren(row: AgentSession): List<AgentSession> = listOf(row) + placedChildren[row.id].orEmpty().flatMap(::withChildren)
                heading(id, title, tasks.flatMap { it.second }.flatMap(::withChildren), id in settings.folded)
                tasks.forEach { (task, rows) -> leaf("$DirectionTaskPrefix${direction.id}/${task.id}", CoreText.plain(task.title), rows, id) }
            }
            noTodo(live.filter { it.session.key !in placed }, above)
        }
        AgentGrouping.Machine -> {
            // iOS `machineSections` / `machineOrder`: computers that have live agents, in hub order; under Recently
            // Updated the one with the latest agent first. Flat rows (no project/engine tabs), idle folded behind
            // "N more idle" with key `machine-<id>` (AgentsViewModel.idleKey).
            val shown = (if (latest) machines.sortedByDescending { m ->
                live.filter { it.session.machineId == m.id }.mapNotNull { it.sortDate }.maxOrNull()
            } else machines).filter { m -> live.any { it.session.machineId == m.id } }
            shown.forEach { machine ->
                val rows = ordered(live.filter { it.session.machineId == machine.id })
                val id = "machine/${machine.id}"
                val folded = id in settings.folded
                val idleKey = "machine-${machine.id}"
                val idle = if (quick || settings.query.isNotEmpty()) emptyList() else rows.filterNot(::stays)
                val visible = when {
                    folded -> emptyList()
                    quick || showIdle(idleKey) -> rows
                    else -> rows.filter(::stays)
                }
                // stateCounts stays null: the shared agents_list fixtures and Rust/iOS/Harmony projections emit none for
                // Machine groups; the Agents screen counts from [rows] for the section header (#9319 rework).
                result += AgentListGroup(id, CoreText.plain(machine.displayName), rows, visible, folded,
                    idleFold = idle.takeIf { it.isNotEmpty() }?.let { IdleFold(it, open = idleKey in settings.expandedIdle) },
                    waitingInBackground = waiting(rows))
            }
        }
        AgentGrouping.App -> {
            // Offline included (iOS By App): every unpinned row the filter left, not only live ones.
            val members = filtered.filter { it.id !in pins.sessions }
            AppGroups.build(members).forEach { app ->
                val folded = app.id in settings.folded
                if (app.showsProjects) {
                    result += AgentListGroup(app.id, app.title, emptyList(), emptyList(), folded,
                        stateCounts = stateCounts(app.sessions), waitingInBackground = waiting(app.sessions))
                    if (!folded) app.projects.forEach { project ->
                        val id = project.id
                        val projectFolded = id in settings.folded
                        val rows = project.sessions
                        result += AgentListGroup(id, project.title, rows, if (projectFolded) emptyList() else rows,
                            projectFolded, stateCounts = stateCounts(rows), under = app.id,
                            waitingInBackground = waiting(rows))
                    }
                } else {
                    val rows = app.sessions
                    result += AgentListGroup(app.id, app.title, rows, if (folded) emptyList() else rows, folded,
                        stateCounts = stateCounts(rows), waitingInBackground = waiting(rows))
                }
            }
        }
        AgentGrouping.Recent -> {
            // iOS draws no section when the list is empty (ChatsView `recentSection`); keep loading/empty on
            // `groups.isEmpty()` in AgentsScreen. The section never folds.
            val rows = recentAgentOrder(filtered.filter { it.id !in pins.sessions })
            if (rows.isNotEmpty()) {
                result += AgentListGroup(RecentGroup, CoreText.of(Key.AgentsRecentlyUsed), rows, rows, folded = false,
                    stateCounts = stateCounts(rows), waitingInBackground = waiting(rows))
            }
        }
        AgentGrouping.Space -> {
            val spaces = agentSpaces(live, byProject = true)
            // Folder metadata comes from every row the scope admits, before quick filters and pinned presentation:
            // filtering to one minority folder must not move the heading's qualifier or New Agent target.
            val projectFolders = projectFolders(agentSpaces(scoped, byProject = true), machines)
            fun pinnedSpace(space: AgentSpace) = space.preferenceKey in pins.spaces
            fun mutedSpace(space: AgentSpace) = space.preferenceKey in mutes.spaces
            fun leading(space: AgentSpace) = pinnedSpace(space) && !mutedSpace(space)
            val active = spaces.filter(::leading) + if (quick) {
                spaces.filterNot(::leading).sortedWith(compareByDescending<AgentSpace> { it.updatedAt }.thenBy { it.id })
            } else {
                // "Recently updated": the busy spaces by their latest agent, not by state (muted ones still after the rest).
                val busy = if (latest) spaces.sortedWith(compareByDescending<AgentSpace> { space -> space.members.mapNotNull { it.sortDate }.maxOrNull() }
                    .thenBy { it.id }) else spaces
                busy.filter { !it.quiet(mutes) && !pinnedSpace(it) && !mutedSpace(it) } +
                    busy.filter { !it.quiet(mutes) && mutedSpace(it) }
            }
            val quiet = if (quick) emptyList() else spaces.filter { it.quiet(mutes) && !leading(it) }
            if (quiet.isNotEmpty()) quietTitle = CoreText.of(Key.AgentsQuietSpaces)
            fun spaceGroup(space: AgentSpace, isQuiet: Boolean): AgentListGroup {
                val rows = if (isQuiet) space.members else ordered(space.members)
                val folded = if (isQuiet) space.id !in settings.expandedQuiet && settings.query.isEmpty()
                    else space.id in settings.folded
                val visible = when {
                    folded -> emptyList()
                    isQuiet || quick || showIdle(space.id) -> rows
                    else -> rows.filter(::stays)
                }
                val states = listOf(AgentState.NeedsYou, AgentState.Working, AgentState.Unread, AgentState.Idle, AgentState.Unknown)
                val stateCounts = stateCounts(rows).filterKeys { it in states }
                val labels = listOf(AgentState.NeedsYou to Key.CountNeedsYou, AgentState.Working to Key.CountWorking, AgentState.Unread to Key.CountUnread)
                val spoken = labels.mapNotNull { (state, label) -> stateCounts.getValue(state).takeIf { it > 0 }?.let { CoreText.of(label, it) } } +
                    (stateCounts.getValue(AgentState.Idle) + stateCounts.getValue(AgentState.Unknown))
                        .takeIf { it > 0 }?.let { listOf(CoreText.of(Key.CountIdle, it)) }.orEmpty()
                // Its idle agents, for the "Hide N Idle" row that folds them again once opened (a quiet space folds whole).
                val idle = if (isQuiet || quick || settings.query.isNotEmpty()) emptyList() else rows.filterNot(::stays)
                return AgentListGroup(space.id, CoreText.plain(space.name), rows, visible, folded,
                    if (isQuiet) "quiet" else null, stateCounts, CoreText.joined(CoreText.of(Key.ListSeparator), spoken),
                    idleFold = idle.takeIf { it.isNotEmpty() }?.let { IdleFold(it, open = space.id in settings.expandedIdle) },
                    waitingInBackground = waiting(rows), projectFolder = projectFolders[space.id])
            }
            result += active.map { spaceGroup(it, false) }
            result += quiet.map { spaceGroup(it, true) }
        }
    }
    return AgentListResult(counts, result, filtered.filter { it.state == AgentState.Unread }.map { it.id }, quietTitle,
        children = placedChildren)
}

/** A repository's or working tree's heading: its name, "Other" for sessions without a cwd ([UnnamedRepo]). */
private fun repoTitle(name: String): CoreText = if (name == UnnamedRepo) CoreText.of(Key.RepoOther) else CoreText.plain(name)

/** "By direction"'s headings (#4352): `direction/<leader conversation id>` (the main one's for the secretary group). */
const val DirectionGroupPrefix = "direction/"
/** "By direction"'s to-dos: `direction-task/<direction id>/<to-do id>`. */
const val DirectionTaskPrefix = "direction-task/"

/** "By to-do"'s sections that are not a to-do: agents opened by hand, then those on none. */
/**
 * A long run of a section's rows (iOS `AgentRowCap`): the first [ROWS], then "Show All N"; a run only a few over the cap
 * shows them all (no "Show All 22").
 */
object AgentRowCap {
    const val ROWS = 20
    fun capped(count: Int, cap: Int = ROWS): Int? = if (count <= cap + 5) null else cap
    /** In `expandedIdle` while the section [groupId] shows all its rows. */
    fun key(groupId: String) = "all:$groupId"
}

const val TodoManualGroup = "todo/manual"
const val TodoNoneGroup = "todo/none"
/** In `expandedIdle` once the user opened "No to-do", which is folded by default under to-dos. */
const val TodoNoneOpened = "todo-none"

/** Recently Used's single section (#9199, iOS `recentSection`). */
const val RecentGroup = "recent"

/**
 * Opened by hand in herdr or Orca (its terminals and its own chats, #17054): not started by Persona (nor spawned through
 * it), web `isManualHerdr`.
 */
val persona.core.contracts.Session.isOpenedByHand: Boolean get() = (spaceKind == "herdr" || spaceKind == "orca") && !startedByPersona
