package persona.feature.terminal

import persona.core.contracts.*
import persona.core.contracts.api.*
import java.util.Base64
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class TerminalPanesTest {
    // The mock hub's tree (scripts/mock/mock_hub.py mock_panes), with a pane from an older node and a stray tab.
    private val response = JsonCodec.decodeFromString<PanesResponse>("""{"hosts":[{"kind":"herdr","stream":true,"control":true,"new_tab":true,"worktree":true,
        "workspaces":[{"id":"w2","label":"notes","number":2,"cwd":"/home/me/notes"},{"id":"w1","label":"persona","number":1,"cwd":"/home/me/persona","branch":"main"}],
        "tabs":[{"id":"w1:t2","workspace_id":"w1","label":"shell","number":2},{"id":"w1:t1","workspace_id":"w1","label":"agent-1","number":1},
                {"id":"w2:t1","workspace_id":"w2","label":"notes","number":1}],
        "panes":[{"pane":"w1:p3","terminal":"pane:w1:p3","tab_id":"w1:t2","workspace_id":"w1","title":"npm run dev","cwd":"/home/me/persona/web"},
                 {"pane":"w1:p2","terminal":"pane:w1:p2","tab_id":"w1:t2","workspace_id":"w1","title":"zsh","cwd":"/home/me/persona"},
                 {"pane":"w1:p1","terminal":"pane:w1:p1","tab_id":"w1:t1","workspace_id":"w1","agent":"claude","status":"working","title":"claude","cwd":"/home/me/persona","session_id":"s-1"},
                 {"pane":"w1:p9","terminal":"pane:w1:p9","tab_id":"w1:t1","workspace_id":"w1","agent":"codex","status":"blocked","cwd":"/home/me/persona"},
                 {"pane":"w2:p1","tab_id":"w2:t1","workspace_id":"w2","cwd":"/home/me/notes"},
                 {"pane":"w3:p1","terminal":"pane:w3:p1","tab_id":"w3:t1","workspace_id":"w3","title":"htop"}]}]}""")
    private val session = Session(machineId = "desk", sessionId = "s-1", title = "Fix login", status = "done", state = "unread")

    @Test fun theTreeIsWorkspacesTabsAndPanesInTheHostsOrder() {
        val tree = terminalTree("desk", response, listOf(session), home = "/home/me")
        assertEquals(listOf("w1", "w2", "w3"), tree.map { it.id })
        val persona = tree[0]
        assertEquals("main", persona.branch)
        assertTrue(persona.canOpen)
        assertEquals("herdr", persona.host)
        assertEquals(listOf("w1:t1", "w1:t2"), persona.tabs.map { it.id })
        assertEquals(listOf("w1:p2", "w1:p3"), persona.tabs[1].panes.map { it.pane })
        // A workspace the host did not list still shows, under its id.
        assertEquals("w3", tree[2].label)
    }

    @Test fun anAgentPaneOpensByItsSessionAndAShellByItsAddress() {
        val tree = terminalTree("desk~g2", response, listOf(session.copy(machineId = "desk~g2")), home = "/home/me")
        val (agent, other) = tree[0].tabs[0].panes
        // The agent with a session: its session's title and state, opened by the session (its chat's own stream).
        assertEquals(SessionKey("desk~g2", "s-1"), agent.key)
        assertEquals("Fix login", agent.title)
        assertEquals("claude · ~/persona", agent.detail)
        assertEquals(AgentState.Unread, agent.state)
        // An agent the hub has no session for: the host's title (else its kind) and status, opened by address.
        assertEquals(SessionKey("desk~g2", "pane:w1:p9"), other.key)
        assertEquals("codex", other.title)
        assertEquals(AgentState.NeedsYou, other.state)
        val shell = tree[0].tabs[1].panes[0]
        assertEquals(SessionKey("desk~g2", "pane:w1:p2"), shell.key)
        assertNull(shell.state)
        assertEquals("", shell.title)
        assertEquals("zsh · ~/persona", shell.detail)
        // An older node without `terminal`: the address is the pane id; a shell with no title goes by its folder.
        val notes = tree[1].tabs.single().panes.single()
        assertEquals(SessionKey("desk~g2", "pane:w2:p1"), notes.key)
        assertEquals("", notes.title)
        assertEquals("~/notes", notes.detail)
        assertTrue(notes.key.isPaneAddress && !agent.key.isPaneAddress)
    }

    @Test fun aPanesTitleIsItsPlace() {
        val tree = terminalTree("desk", response, emptyList())
        val persona = tree[0]
        assertEquals("persona › shell › Terminal", paneTrail(persona, persona.tabs[1], persona.tabs[1].panes[0], "Terminal"))
        val notes = tree[1]
        assertEquals("notes", paneTrail(notes, notes.tabs[0], notes.tabs[0].panes[0].copy(title = ""), "notes"))
    }

    @Test fun aWorkspaceWithoutPanesShowsOnlyWhereATabCanBeOpened() {
        val empty = PanesResponse(listOf(PaneHost("herdr", newTab = true, workspaces = listOf(PaneSpace("w1", "persona")))))
        assertEquals(listOf("w1"), terminalTree("desk", empty).map { it.id })
        assertTrue(terminalTree("desk", empty.copy(hosts = empty.hosts.map { it.copy(newTab = false) })).isEmpty())
        assertTrue(terminalTree("desk", null).isEmpty())
    }

    @Test fun twoHostsWithTheSameWorkspaceIdKeepTheirItemsApart() {
        // herdr and orca on one machine both number their workspaces from w1; panes without a tab id sit in two workspaces.
        val both = JsonCodec.decodeFromString<PanesResponse>("""{"hosts":[
            {"kind":"herdr","new_tab":true,"workspaces":[{"id":"w1","label":"persona"},{"id":"w2","label":"notes"}],
             "panes":[{"pane":"w1:p1","tab_id":"","workspace_id":"w1"},{"pane":"w2:p1","tab_id":"","workspace_id":"w2"},
                      {"pane":"x:p1","tab_id":"","workspace_id":""}]},
            {"kind":"orca","new_tab":true,"workspaces":[{"id":"w1","label":"persona"}],
             "panes":[{"pane":"w1:p1","tab_id":"","workspace_id":"w1"},{"pane":"y:p1","tab_id":"","workspace_id":""}]}]}""")
        val tree = terminalTree("desk", both)
        assertEquals(listOf("herdr/w1", "herdr/w2", "herdr/", "orca/w1", "orca/"), tree.map { it.key })
        // Each pane is listed once, under its own workspace, however its tab id repeats.
        assertEquals(listOf(listOf("w1:p1"), listOf("w2:p1"), listOf("x:p1"), listOf("w1:p1"), listOf("y:p1")),
            tree.map { space -> space.tabs.flatMap { it.panes }.map { it.pane } })
        // The page's item keys: LazyColumn throws on a repeated one.
        val keys = tree.flatMap { space -> listOf(spaceItemKey("desk", space)) +
            space.tabs.flatMap { tab -> listOf(tabItemKey("desk", space, tab)) + tab.panes.map { paneItemKey("desk", space, it) } } }
        assertEquals(keys.size, keys.toSet().size)
        assertTrue("pane-desk-orca/w1:p1" in keys && "pane-desk-herdr/w1:p1" in keys)
        // "New tab"'s spinner marks one workspace, not every host's w1.
        assertNotEquals(openingKey("desk", tree[0]), openingKey("desk", tree[3]))
    }

    @Test fun aHomeTerminalUsesTheFirstHostThatCanOpenOne() {
        val hosts = PanesResponse(listOf(PaneHost("orca"), PaneHost("herdr", newTab = true), PaneHost("other", newTab = true)))
        assertEquals("herdr", homeShellHost(hosts))
        assertNull(homeShellHost(PanesResponse(listOf(PaneHost("herdr")))))
        assertNull(homeShellHost(null))
    }

    @Test fun newTabAsksForTheWorkspaceAndOpensTheReturnedPane() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        try {
            val panes = FakePanes()
            val sessions = FakeSessions(listOf(Machine("desk", "Desk", online = true), Machine("old", online = false)))
            val model = TerminalPanesViewModel(panes, sessions)
            val events = mutableListOf<TerminalPanesEvent>()
            val collect = launch { model.events.toList(events) }
            model.refresh(); runCurrent()
            // Only online machines are read.
            assertEquals(listOf("desk"), panes.reads)
            val space = model.state.value.machines.first { it.machine.id == "desk" }.spaces.first()
            model.newTab("desk", space); runCurrent()
            assertEquals(listOf(NewTabCall("desk", "w1", "herdr", null)), panes.opened)
            assertEquals(TerminalPanesEvent.Open("desk", "pane:w1:p4", "persona"), events.single())
            assertNull(model.state.value.opening)
            panes.fail = true
            model.newTab("desk", space); runCurrent()
            assertEquals(TerminalPanesEvent.NewTabFailed("opening a terminal needs a full device"), events.last())
            collect.cancel()
        } finally { Dispatchers.resetMain() }
    }

    @Test fun newTerminalOpensHomeOnceAndReportsFailure() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        try {
            val panes = FakePanes()
            val gate = CompletableDeferred<Unit>()
            panes.gate = gate
            val model = TerminalPanesViewModel(panes, FakeSessions(listOf(Machine("desk", "Desk", online = true))))
            val events = mutableListOf<TerminalPanesEvent>()
            val collect = launch { model.events.toList(events) }
            model.newTerminal("desk", "herdr"); runCurrent()
            assertEquals(listOf(NewTabCall("desk", "", "herdr", "~")), panes.opened)
            assertNotNull(model.state.value.opening)
            model.newTerminal("desk", "herdr"); runCurrent()
            assertEquals(1, panes.opened.size)
            gate.complete(Unit); runCurrent()
            assertEquals(TerminalPanesEvent.Open("desk", "pane:w1:p4", ""), events.single())
            assertNull(model.state.value.opening)
            panes.fail = true
            model.newTerminal("desk", "herdr"); runCurrent()
            assertEquals(TerminalPanesEvent.NewTabFailed("opening a terminal needs a full device"), events.last())
            assertNull(model.state.value.opening)
            collect.cancel()
        } finally { Dispatchers.resetMain() }
    }

    @Test fun aPanesTerminalSubscribesAndReadsFramesByItsAddress() = runTest {
        val dispatcher = StandardTestDispatcher(testScheduler)
        Dispatchers.setMain(dispatcher)
        try {
            val transport = FakeTerminal()
            val key = TerminalAddress.key("desk", "pane:w1:p2")
            val model = TerminalViewModel(key, transport, dispatcher, MemoryTerminalPreferences(drive = false))
            val run = launch { model.run() }
            advanceTimeBy(250); runCurrent()
            assertEquals(listOf(key), transport.subscribedKeys)
            transport.events.emit(TerminalEvent("desk", "pane:w1:p2", "e", 1, true, 20, 4, Base64.getEncoder().encodeToString("$ ls".toByteArray())))
            runCurrent()
            assertTrue(model.state.value.loaded)
            run.cancelAndJoin()
        } finally { Dispatchers.resetMain() }
    }

    private data class NewTabCall(val machineId: String, val workspaceId: String, val kind: String?, val cwd: String?)

    private class FakePanes : TerminalPanes {
        override val available = MutableStateFlow(true)
        val reads = mutableListOf<String>()
        val opened = mutableListOf<NewTabCall>()
        var fail = false
        var gate: CompletableDeferred<Unit>? = null
        override suspend fun panes(machineId: String): PanesResponse {
            reads += machineId
            return PanesResponse(listOf(PaneHost("herdr", newTab = true, workspaces = listOf(PaneSpace("w1", "persona")))))
        }
        override suspend fun newTab(machineId: String, workspaceId: String, kind: String?, cwd: String?): NewPaneTab {
            opened += NewTabCall(machineId, workspaceId, kind, cwd)
            gate?.await()
            if (fail) throw IllegalStateException("opening a terminal needs a full device")
            return NewPaneTab("w1:p4", "pane:w1:p4", "w1:t4")
        }
    }

    private class FakeSessions(machines: List<Machine>) : SessionsRepository {
        override val machines = MutableStateFlow(machines)
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun refresh() = Unit
    }
}
