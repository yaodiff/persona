package persona.core.domain.sort

import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Resume in Terminal (#5565): what the chat's ⋯ offers for an Agent that isn't running, the same cases as iOS
 * `TerminalResumeTests` and web `agent-resume.test.ts`.
 */
class TerminalResumeTest {
    private val stoppedCaps = SessionCapabilities(read = true, terminal = true)

    private fun session(
        status: String = "offline",
        agent: String? = "claude",
        surface: String? = "cli",
        sourceKind: String? = null,
        capabilities: SessionCapabilities? = null,
        readOnlyReason: String? = null,
        assistant: Boolean = false,
        starting: Boolean = false,
    ) = Session(
        machineId = "m",
        sessionId = "s1",
        title = "Tidy the notes",
        cwd = "/tmp/p",
        status = status,
        agent = agent,
        agentSurface = surface,
        sourceKind = sourceKind,
        capabilities = capabilities,
        readOnlyReason = readOnlyReason,
        assistant = assistant,
        starting = starting,
    )

    private fun machine(hosts: List<String>?): Machine =
        JsonCodec.decodeFromString(
            buildString {
                append("""{"id":"m","name":"Mac mini","kind":"ssh","online":true""")
                if (hosts != null) append(""","engine_hosts":${hosts.joinToString(",", "[", "]") { "\"$it\"" }}""")
                append("}")
            },
        )

    private fun stopped(
        status: String = "offline",
        agent: String? = "claude",
        sourceKind: String? = null,
        starting: Boolean = false,
        assistant: Boolean = false,
        surface: String? = "cli",
    ) = session(
        status = status,
        agent = agent,
        surface = surface,
        sourceKind = sourceKind,
        capabilities = stoppedCaps,
        readOnlyReason = "stopped",
        starting = starting,
        assistant = assistant,
    )

    @Test fun aStoppedAgentIsOffered() {
        val herdr = machine(listOf("herdr"))
        assertEquals(TerminalResume.Available, terminalResume(stopped(), herdr))
        assertEquals(TerminalResume.Available, terminalResume(stopped(status = "interrupted"), herdr))
        val codex = stopped(agent = "codex", sourceKind = "codex_history")
        assertTrue("a Codex thread nothing has open is a stopped Agent too", codex.isStoppedAgent)
        assertEquals(TerminalResume.Available, terminalResume(codex, herdr))
        assertEquals(
            "a hub before engine sets: the node's word holds",
            TerminalResume.Available,
            terminalResume(stopped(), machine(null)),
        )
        assertEquals(TerminalResume.Available, terminalResume(stopped(), null))
    }

    @Test fun unknownStatusIsHidden() {
        val herdr = machine(listOf("herdr"))
        assertEquals(TerminalResume.Hidden, terminalResume(stopped(status = "unknown"), herdr))
        assertFalse(stopped(status = "unknown").isStoppedAgent)
    }

    @Test fun noTerminalEngineDisablesItSayingWhy() {
        val none = machine(emptyList())
        assertEquals(
            "listed there without capabilities, as the node does",
            TerminalResume.NoEngine,
            terminalResume(session(), none),
        )
        assertEquals(
            TerminalResume.NoEngine,
            terminalResume(
                session(agent = "codex", sourceKind = "codex_history", capabilities = SessionCapabilities(read = true)),
                none,
            ),
        )
        assertEquals(
            "engines turned off since the row was listed",
            TerminalResume.NoEngine,
            terminalResume(stopped(), none),
        )
    }

    @Test fun notWhileItRuns() {
        val herdr = machine(listOf("herdr"))
        val none = machine(emptyList())
        assertEquals(TerminalResume.Hidden, terminalResume(session(status = "idle"), herdr))
        val elsewhere = session(
            status = "idle",
            sourceKind = "claude_cli",
            capabilities = SessionCapabilities(read = true, send = true),
        )
        assertEquals(
            "a second claude would write the same transcript",
            TerminalResume.Hidden,
            terminalResume(elsewhere, herdr),
        )
        assertEquals(TerminalResume.Hidden, terminalResume(elsewhere, none))
        assertEquals(TerminalResume.Hidden, terminalResume(stopped(starting = true), herdr))
    }

    @Test fun leftOutRows() {
        val herdr = machine(listOf("herdr"))
        val none = machine(emptyList())
        assertEquals(TerminalResume.Hidden, terminalResume(stopped(assistant = true), herdr))
        assertEquals(TerminalResume.Hidden, terminalResume(session(assistant = true), none))
        assertEquals(
            "a message reopens it in the app",
            TerminalResume.Hidden,
            terminalResume(session(surface = "desktop"), none),
        )
        assertEquals(
            TerminalResume.Hidden,
            terminalResume(
                session(agent = "dsh", sourceKind = "runtime", capabilities = SessionCapabilities(read = true)),
                none,
            ),
        )
        assertEquals(
            "an older node's row does not say it can",
            TerminalResume.Hidden,
            terminalResume(session(), herdr),
        )
        val appThread = session(agent = "codex", sourceKind = "codex_history", capabilities = SessionCapabilities(read = true))
        assertFalse("a Codex App thread has no terminal", appThread.isStoppedAgent)
        assertEquals(TerminalResume.Hidden, terminalResume(appThread, herdr))
        assertEquals(TerminalResume.Hidden, terminalResume(null, herdr))
    }

    @Test fun stoppedAgentMatchesCapabilities() {
        val stopped = stopped()
        assertTrue(stopped.isStoppedAgent)
        assertFalse(stopped.copy(status = "idle").isStoppedAgent)
        assertFalse(stopped.copy(capabilities = SessionCapabilities(read = true, terminal = true, send = true)).isStoppedAgent)
        assertFalse(stopped.copy(capabilities = SessionCapabilities(read = true)).isStoppedAgent)
        assertFalse(stopped.copy(sourceKind = "claude_cli").isStoppedAgent)
        assertTrue(stopped.copy(agent = "codex", sourceKind = "codex_history").isStoppedAgent)
        assertFalse(stopped.copy(assistant = true).isStoppedAgent)
    }

    @Test fun stoppedAgentsKeepReadingAndTerminalButTakeNoMessagesOrAnswers() {
        for (status in listOf("offline", "interrupted")) {
            val session = stopped(status = status)
            assertTrue(session.canRead)
            assertTrue(session.canTerminal)
            assertFalse(session.canSend)
            assertFalse(session.canApprove)
            assertFalse(session.canInterrupt)
            assertFalse(session.canChangeModes)
        }
        assertTrue("older nodes without capabilities keep their behavior", session().canSend)
        assertTrue(stopped().copy(status = "idle", capabilities = stoppedCaps.copy(send = true)).canSend)
        assertTrue("assistant conversations can be reopened by messaging", stopped(assistant = true).canSend)
    }
}
