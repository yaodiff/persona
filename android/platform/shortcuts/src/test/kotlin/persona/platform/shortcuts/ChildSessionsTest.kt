package persona.platform.shortcuts

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*

class ChildSessionsTest {
    @Test fun recentAgentsShowOneParentAndKeepAnUnlistedParentsChildReachable() {
        val parent = Session("m", "p", state = "idle", children = SessionChildren(1, working = 1))
        val child = Session("m", "c", state = "working", foldedUnder = "p")
        assertEquals(listOf("p"), RecentAgents.pick(listOf(parent, child), true).map { it.sessionId })
        assertEquals(AgentState.Working, RecentAgents.pick(listOf(parent, child), true).single().agentState)
        assertEquals(listOf("c"), RecentAgents.pick(listOf(child), true).map { it.sessionId })
    }
}
