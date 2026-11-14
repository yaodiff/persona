package persona.feature.terminal

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*

@OptIn(ExperimentalCoroutinesApi::class)
class FinishedTerminalsTest {
    @Test fun retainedAgentsUseTheirTitleAndSessionWhilePlainTerminalsNameTheirProgram() {
        val item = JsonCodec.decodeFromString<PaneItem>("""{"pane":"w1:p1","agent_session":"s","foreground":"zsh","cwd":"/home/me/project"}""")
        val session = Session(machineId = "desk~second", sessionId = "s", title = "Review release")
        val finished = pane("desk~second", item, session, "/home/me")
        assertTrue(finished.finished)
        assertEquals(session.key, finished.key)
        assertEquals("Review release", finished.title)
        assertEquals("zsh · project", finished.detail)
        assertTrue(finished.canClose)
        val plain = pane("desk", item.copy(agentSession = null, foreground = "vim", title = "old title"), null, "/home/me")
        assertFalse(plain.finished)
        assertEquals("pane:w1:p1", plain.key.sessionId)
        assertEquals("", plain.title)
        assertEquals("vim · ~/project", plain.detail)
        assertTrue(plain.canClose)
    }

    @Test fun wireAgentIdentityNeverOffersCloseEvenWithoutASessionOrStatus() {
        val item = PaneItem("w1:p1", agentSession = "previous")
        for (agent in listOf(item.copy(agent = "claude"), item.copy(sessionId = "current"))) {
            val row = pane("desk", agent, null, null)
            assertEquals(TermPaneKind.Agent, row.kind)
            assertFalse(row.canClose)
        }
        assertTrue(hasFinished(PanesResponse(listOf(PaneHost("herdr", panes = listOf(item))))))
        assertFalse(hasFinished(PanesResponse(listOf(PaneHost("herdr", panes = listOf(item.copy(agentSession = null)))))))
        assertFalse(hasFinished(null))
    }

    @Test fun finishedTextUsesTheRawSessionTitleOrIdAndDoesNotRepeatItsProgram() {
        val item = PaneItem("w1:p1", title = "Host title", agentSession = "1234567890", foreground = "zsh", cwd = "/home/me/project/")
        assertEquals("12345678", pane("desk", item, null, null).title)
        val session = Session(sessionId = "1234567890", title = "")
        assertEquals("12345678", pane("desk", item, session, null).title)
        assertEquals("zsh · project", pane("desk", item, session, null).detail)
        assertEquals("project", pane("desk", item, session.copy(title = "Review ZSH history"), null).detail)
        assertEquals("  ", pane("desk", item, session.copy(title = "  "), null).title)
        assertEquals("zsh · /", pane("desk", item.copy(cwd = "/"), session, null).detail)
        assertEquals("", terminalFolder(null))
        val titled = session.copy(machineId = "desk", title = "Release notes")
        val response = PanesResponse(listOf(PaneHost("herdr", panes = listOf(item.copy(sessionId = "")))))
        assertEquals(titled.title, terminalTree("desk", response, listOf(titled)).single().tabs.single().panes.single().title)
    }

    @Test fun previewNeverClosesAndConfirmationOnlySendsThePreviewedIds() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        try {
            val panes = Panes()
            val model = TerminalPanesViewModel(panes, Sessions())
            model.previewFinished("desk"); runCurrent()
            assertEquals(listOf(Call("desk", true, emptyList())), panes.calls)
            assertEquals(listOf("Release notes"), model.confirmation.value?.rows?.map { it.title })
            // Another terminal becomes finished while the confirmation is open; it was not approved.
            panes.preview = FinishedPanes(panes.preview.closed + FinishedPane("w1:p3", "new"))
            model.confirmFinished(); runCurrent()
            assertEquals(Call("desk", false, listOf("w1:p1")), panes.calls.last())
            assertNull(model.confirmation.value)
            assertEquals(listOf("desk"), panes.reads)
            model.close("desk", "w1:p2"); runCurrent()
            assertEquals(listOf("desk" to "w1:p2"), panes.closed)
        } finally { Dispatchers.resetMain() }
    }

    @Test fun previewUsesTheSameFinishedTitlesAndDetailsAsTheList() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        try {
            val panes = Panes()
            panes.response = PanesResponse(listOf(PaneHost("herdr", panes = listOf(
                PaneItem("w1:p1", foreground = "zsh", cwd = "/home/me/project", agentSession = "s"),
                PaneItem("w1:p2", title = "Host title", foreground = "vim", cwd = "/home/me/web", agentSession = "1234567890")))))
            panes.preview = FinishedPanes(listOf(FinishedPane("w1:p1", "s"), FinishedPane("w1:p2", "1234567890")))
            val model = TerminalPanesViewModel(panes, Sessions())
            model.refresh(); runCurrent()
            model.previewFinished("desk"); runCurrent()
            val rows = model.confirmation.value!!.rows
            assertEquals(listOf("Release notes", "12345678"), rows.map { it.title })
            assertEquals(listOf("zsh · project", "vim · web"), rows.map { it.detail })
            assertTrue(rows.all { it.finished })
        } finally { Dispatchers.resetMain() }
    }

    @Test fun cancelEmptyPreviewAndFailureDoNotCloseAnything() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        try {
            val panes = Panes()
            val model = TerminalPanesViewModel(panes, Sessions())
            model.previewFinished("desk"); runCurrent()
            model.dismissFinished(); model.confirmFinished(); runCurrent()
            assertEquals(1, panes.calls.size)
            panes.preview = FinishedPanes()
            model.previewFinished("desk"); runCurrent()
            assertNull(model.confirmation.value)
            panes.fail = true
            model.previewFinished("desk"); runCurrent()
            assertNull(model.confirmation.value)
            assertFalse(model.closing.value)
            assertTrue(panes.calls.all { it.preview })
            assertTrue(panes.closed.isEmpty())
        } finally { Dispatchers.resetMain() }
    }

    private data class Call(val machine: String, val preview: Boolean, val panes: List<String>)
    private class Panes : TerminalPanes {
        override val available = MutableStateFlow(true)
        var preview = FinishedPanes(listOf(FinishedPane("w1:p1", "s")), listOf(FinishedPane("w1:p2", "busy", why = "program")))
        val calls = mutableListOf<Call>()
        val reads = mutableListOf<String>()
        val closed = mutableListOf<Pair<String, String>>()
        var fail = false
        var response = PanesResponse()
        override suspend fun panes(machineId: String): PanesResponse { reads += machineId; return response }
        override suspend fun newTab(machineId: String, workspaceId: String, kind: String?, cwd: String?) = error("unused")
        override suspend fun close(machineId: String, pane: String) { closed += machineId to pane }
        override suspend fun closeFinished(machineId: String, preview: Boolean, panes: List<String>): FinishedPanes {
            calls += Call(machineId, preview, panes)
            if (fail) error("offline")
            return if (preview) this.preview else FinishedPanes(panes.map { FinishedPane(it) })
        }
    }
    private class Sessions : SessionsRepository {
        override val machines = MutableStateFlow(listOf(Machine("desk", "Workstation", online = true)))
        override val sessions = MutableStateFlow(listOf(Session(machineId = "desk", sessionId = "s", title = "Release notes")))
        override suspend fun refresh() = Unit
    }
}
