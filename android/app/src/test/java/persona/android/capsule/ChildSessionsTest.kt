package persona.android.capsule

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.feature.agents.AgentsPreferencesSnapshot

class ChildSessionsTest {
    @Test fun unreadCountsOnlyTheParentWhileAChildWithoutItsParentStaysReachable() {
        val parent = Session("m", "p", state = "unread", children = SessionChildren(1))
        val child = Session("m", "c", state = "unread", foldedUnder = "p")
        val preferences = AgentsPreferencesSnapshot()
        assertEquals(listOf("p"), capsuleUnreadSessions(listOf(parent, child), preferences, true).map { it.sessionId })
        assertEquals(listOf("c"), capsuleUnreadSessions(listOf(child), preferences, true).map { it.sessionId })
    }
}
