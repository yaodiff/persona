package persona.feature.knowledge

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*

class ChildSessionsTest {
    @Test fun aProjectListsTheParentWithItsFoldedState() {
        val parent = Session("m", "p", state = "idle", children = SessionChildren(1, needsYou = 1))
        val child = Session("m", "c", state = "needs_you", foldedUnder = "p")
        assertEquals(listOf("p"), projectAgents(listOf(parent, child), true).map { it.sessionId })
        assertEquals(AgentState.NeedsYou, projectAgents(listOf(parent, child), true).single().agentState)
        assertEquals(listOf("c"), projectAgents(listOf(child), true).map { it.sessionId })
    }
}
