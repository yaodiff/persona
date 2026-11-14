package persona.feature.terminal

import persona.core.contracts.AgentState
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.PaneItem
import persona.core.contracts.api.PaneSpace
import persona.core.contracts.api.PaneTab
import persona.core.contracts.api.PanesResponse
import persona.core.contracts.api.TerminalAddress

// The terminal browser's tree (docs/integrations/terminal.md "Standalone terminal"), as the web builds it (web/src/lib/terminals.ts): what a
// machine's `GET /machines/{mid}/panes` lists turned into workspaces → tabs → panes, each pane with the key its
// terminal opens by. Pure, so it is tested without Android.

enum class TermPaneKind { Agent, Finished, Plain }

/** One pane's row. The wire identity decides its kind, independently of the Agent's displayed state. */
data class TermPane(
    /** What the terminal opens by: the agent's session when there is one (it shares that stream), else the pane's address. */
    val key: SessionKey,
    /** The Agent's session title; empty for a plain terminal (the page says "Terminal"). */
    val title: String,
    /** The line under the title: the agent, the folder. */
    val detail: String,
    val state: AgentState?,
    /** The host's own id for the pane ("w1:p2"). */
    val pane: String,
    val kind: TermPaneKind = TermPaneKind.Plain,
) {
    val finished: Boolean get() = kind == TermPaneKind.Finished
    val canClose: Boolean get() = kind != TermPaneKind.Agent
}

data class TermTab(val id: String, val label: String, val panes: List<TermPane>)

data class TermSpace(
    val id: String,
    /** The host kind the workspace belongs to: "New tab" names it. */
    val host: String,
    val label: String,
    val branch: String,
    /** A shell tab can be opened here ("New tab"). */
    val canOpen: Boolean,
    val tabs: List<TermTab>,
) {
    /** Workspace ids are the host's own: a machine with two hosts can list the same one twice, so the page keys by both. */
    val key: String get() = "$host/$id"
}

/** The page's item keys, unique across a machine's hosts (tab and pane ids are host-local too). */
internal fun spaceItemKey(machineId: String, space: TermSpace) = "space-$machineId-${space.key}"
internal fun tabItemKey(machineId: String, space: TermSpace, tab: TermTab) = "tab-$machineId-${space.key}/${tab.id}"
/** "New tab"'s in-flight marker: the workspace on its machine and host. */
internal fun openingKey(machineId: String, space: TermSpace) = "$machineId\n${space.key}"
internal fun homeOpeningKey(machineId: String, kind: String) = "$machineId\n$kind"
internal fun paneItemKey(machineId: String, space: TermSpace, pane: TermPane) = "pane-$machineId-${space.host}/${pane.pane}"

/** The host that opens a home-directory terminal when this computer has no workspace yet. */
fun homeShellHost(response: PanesResponse?): String? = response?.hosts?.firstOrNull { it.newTab }?.kind

/**
 * [machineId]'s workspaces → tabs → panes. [sessions] are the machine's agent sessions (their titles and states);
 * [home] shortens folders to `~`. Panes whose workspace or tab the host did not list still show, under one of their
 * own id; a workspace with no panes shows only when a tab can be opened in it.
 */
fun terminalTree(machineId: String, response: PanesResponse?, sessions: List<Session> = emptyList(), home: String? = null): List<TermSpace> {
    val bySession = sessions.filter { it.machineId == machineId }.associateBy { it.sessionId }
    val out = mutableListOf<TermSpace>()
    for (host in response?.hosts.orEmpty()) {
        val tabsOf = host.tabs.groupBy { it.workspaceId }
        // A pane goes under its own workspace and tab, so a tab id repeated in two workspaces (or empty) lists each pane once.
        val panesOf = host.panes.groupBy { it.workspaceId to it.tabId }
        val known = host.workspaces.mapTo(HashSet()) { it.id }
        val orphans = host.panes.map { it.workspaceId }.distinct().filter { it !in known }.map { PaneSpace(it, it) }
        for (space in host.workspaces.sortedWith(byNumber({ it.number }, { it.id })) + orphans) {
            val listed = tabsOf[space.id].orEmpty().sortedWith(byNumber({ it.number }, { it.id }))
            val listedIds = listed.mapTo(HashSet()) { it.id }
            val strays = host.panes.filter { it.workspaceId == space.id && it.tabId !in listedIds }.map { it.tabId }.distinct()
                .map { PaneTab(it, space.id, it) }
            val tabs = (listed + strays).map { tab ->
                TermTab(tab.id, tab.label.ifEmpty { tab.id }, panesOf[space.id to tab.id].orEmpty()
                    .sortedWith(compareBy<PaneItem> { paneNumber(it.pane) }.thenBy { it.pane })
                    .map { pane(machineId, it, (it.sessionId?.takeIf(String::isNotEmpty) ?: it.agentSession)?.let(bySession::get), home) })
            }.filter { it.panes.isNotEmpty() }
            if (tabs.isEmpty() && !host.newTab) continue
            out += TermSpace(space.id, host.kind, space.label.ifEmpty { space.id }, space.branch.orEmpty(), host.newTab, tabs)
        }
    }
    return out
}

/** An Agent keeps its title; a plain terminal puts its program and folder under the localized "Terminal" label. */
internal fun pane(machineId: String, item: PaneItem, session: Session?, home: String?): TermPane {
    val folder = shortPath(item.cwd, home)
    val agent = item.agent.orEmpty()
    val previous = item.agentSession
    // Running and stopped Agents open by session, as on iOS, so a retained shell keeps Resume. Plain panes use their address.
    val sessionId = item.sessionId?.takeIf(String::isNotEmpty) ?: item.agentSession?.takeIf(String::isNotEmpty)
    val key = if (sessionId != null) SessionKey(machineId, sessionId)
        else TerminalAddress.key(machineId, item.terminal.ifEmpty { item.pane })
    return when {
        agent.isEmpty() && item.sessionId.isNullOrEmpty() && !previous.isNullOrEmpty() -> {
            val title = finishedTitle(session, previous)
            TermPane(key, title, plainDetails(title, item.foreground, terminalFolder(item.cwd)),
                null, item.pane, TermPaneKind.Finished)
        }
        session != null -> TermPane(key, session.displayTitle, listOf(agent, folder).filter(String::isNotEmpty).joinToString(" · "),
            session.agentState, item.pane, TermPaneKind.Agent)
        agent.isNotEmpty() || !item.sessionId.isNullOrEmpty() -> TermPane(key, item.title?.trim().orEmpty().ifEmpty { agent },
            listOf(agent, folder).filter(String::isNotEmpty).joinToString(" · "), AgentState.of(null, item.status), item.pane, TermPaneKind.Agent)
        else -> {
            val program = item.foreground?.trim().orEmpty().ifEmpty { item.title?.trim().orEmpty() }
            TermPane(key, "", listOf(program, folder).filter(String::isNotEmpty).joinToString(" · "), null, item.pane)
        }
    }
}

/** iOS TerminalsView.title(finished:): an actual title, else the first eight characters of the session id. */
internal fun finishedTitle(session: Session?, id: String): String = session?.title?.takeIf(String::isNotEmpty) ?: id.take(8)

/** iOS TerminalsList.plainDetails: the title already names some programs, so say those only once. */
internal fun plainDetails(title: String, foreground: String?, folder: String): String =
    listOfNotNull(foreground?.takeIf { it.isNotEmpty() && !title.contains(it, ignoreCase = true) }, folder.takeIf(String::isNotEmpty))
        .joinToString(" · ")

internal fun terminalFolder(cwd: String?): String {
    val path = cwd.orEmpty().trimEnd('/')
    return if (path.isEmpty() && !cwd.isNullOrEmpty()) "/" else path.substringAfterLast('/')
}

/** The cleanup action is offered only when the hub lists a retained Agent terminal, as on iOS. */
internal fun hasFinished(response: PanesResponse?): Boolean = response?.hosts.orEmpty().any { host ->
    host.panes.any { !it.agentSession.isNullOrEmpty() }
}

/** The pane's place, for the terminal's title: space › tab › title (a tab named like its workspace, or a pane like its tab, once). */
fun paneTrail(space: TermSpace, tab: TermTab, pane: TermPane, untitled: String): String {
    val title = pane.title.ifEmpty { untitled }
    return listOf(space.label, tab.label.takeIf { it != space.label }, title.takeIf { it != tab.label })
        .filterNot { it.isNullOrEmpty() }.joinToString(" › ")
}

/** `/home/me/x` → `~/x` under the machine's home; other folders as they are. */
internal fun shortPath(path: String?, home: String?): String {
    val value = path.orEmpty().trimEnd('/')
    val root = home.orEmpty().trimEnd('/')
    return when {
        value.isEmpty() -> ""
        root.isNotEmpty() && value == root -> "~"
        root.isNotEmpty() && value.startsWith("$root/") -> "~" + value.removePrefix(root)
        else -> value
    }
}

/** herdr numbers a tab's panes ("w9:p1", "w9:p2"); ids with no number go last. */
private fun paneNumber(pane: String): Int = Regex(":p(\\d+)$").find(pane)?.groupValues?.get(1)?.toIntOrNull() ?: Int.MAX_VALUE

/** By the host's number (absent counts as 0), then by id with its digits compared as numbers. */
private fun <T> byNumber(number: (T) -> Int?, id: (T) -> String): Comparator<T> =
    compareBy<T> { number(it) ?: 0 }.then { a, b -> naturalOrder(id(a), id(b)) }

private fun naturalOrder(a: String, b: String): Int {
    val parts = Regex("\\d+|\\D+")
    val left = parts.findAll(a).map { it.value }.toList()
    val right = parts.findAll(b).map { it.value }.toList()
    for (i in 0 until minOf(left.size, right.size)) {
        val x = left[i]; val y = right[i]
        val c = if (x[0].isDigit() && y[0].isDigit()) x.toBigInteger().compareTo(y.toBigInteger()) else x.compareTo(y)
        if (c != 0) return c
    }
    return left.size - right.size
}
