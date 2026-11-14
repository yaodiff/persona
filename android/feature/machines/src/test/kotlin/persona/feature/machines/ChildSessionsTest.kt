package persona.feature.machines

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*

class ChildSessionsTest {
    @Test fun aChildNeedsNoMachineRowOrCountAndAnOrphanStaysVisible() {
        val parent = Session("m", "p", state = "idle", children = SessionChildren(2, working = 1, needsYou = 1))
        val child = Session("m", "c", state = "needs_you", foldedUnder = "p")
        val orphan = Session("m", "orphan", agent = "codex", sourceKind = "codex_app_server", isSubagent = true,
            parentSessionId = "missing", state = "working")
        val rows = machineSessions(listOf(parent, child, orphan), "m", hierarchy = true)
        assertEquals(listOf("p", "orphan"), rows.map { it.sessionId })
        assertEquals(AgentTally(needsYou = 1, working = 1), AgentTally(rows))
        assertEquals(listOf("c"), machineSessions(listOf(child), "m", hierarchy = true).map { it.sessionId })
    }
}
