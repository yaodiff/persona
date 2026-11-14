package persona.core.contracts

import org.junit.Assert.*
import org.junit.Test

class SessionHierarchyTest {
    @Test fun fullAndSummaryRowsDecodeAdditiveHierarchyAndOldRowsKeepTheirOwnState() {
        val json = """{"machine_id":"m","session_id":"p","state":"idle","children":{"total":3,"working":1,"needs_you":1,"workers":1},"started_at":"2026-11-14T10:00:00Z","asking_parent_session_id":"ancestor","folded_under":"ancestor","folded_as":"worker"}"""
        val row = JsonCodec.decodeFromString(Session.serializer(), json)
        assertEquals(SessionChildren(3, 1, 1, 1), row.children)
        assertEquals("ancestor", row.foldedUnder)
        assertEquals("ancestor", row.askingParentSessionId)
        assertEquals("worker", row.foldedAs)
        assertNotNull(parseTime(row.startedAt))
        assertEquals(AgentState.NeedsYou, row.foldedState)
        assertEquals(AgentState.Idle, row.agentState)
        val old = JsonCodec.decodeFromString(Session.serializer(), """{"session_id":"old","state":"unread"}""")
        assertNull(old.children)
        assertEquals(old.agentState, old.foldedState)
    }

    @Test fun interactionsHaveAParentHomeAndKeepTheOriginalAnswerTarget() {
        val row = JsonCodec.decodeFromString(Interaction.serializer(), """{"id":"q","machine_id":"m","session_id":"child","folded_under":"parent","session_title":"Review"}""")
        assertEquals(SessionKey("m", "parent"), row.homeKey)
        assertEquals(SessionKey("m", "child"), row.sessionKey)
        assertEquals("q", row.id)
    }
}
