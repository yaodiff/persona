package persona.core.domain.lines

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Item
import persona.core.contracts.Session
import persona.core.contracts.SessionChildren
import java.time.Instant

/** The exchange and step placement cases of web child-cards.test.ts. */
class ChildSessionCardsTest {
    private fun at(n: Long) = Instant.parse("2026-11-14T10:00:00Z").plusSeconds(n)
    private fun user(id: String, n: Long) = ChatLine.Row(ChatRow("user", LineItem(Item(id, ts = at(n).toString(), role = "user", kind = "text"))))
    private fun tool(id: String, n: Long) = WorkItem.Tool(ToolCall(LineItem(Item(id, ts = at(n).toString(), kind = "tool_use", toolName = "Read")), null, emptyList()))
    private val work = ChatLine.Work("w-u1", listOf(tool("first", 12), tool("last", 18)), false, at(10), at(20), false)
    private val lines = listOf(user("u1", 10), work, user("u2", 30))

    @Test fun cardsStayInTheirExchangeAndInsideItsCollapsedWork() {
        val placed = ChildSessionCards.lines(lines, listOf(ChildPlace("kid", at(15))), true, emptySet())
        val w = placed.filterIsInstance<ChatLine.Work>().single()
        assertEquals(listOf("first", "child:kid", "last"), w.steps.map { it.id })
        assertEquals(at(12), w.steps[1].start)
        assertFalse(w.open)
        assertTrue(placed.none { it is ChatLine.Step })
        val opened = ChildSessionCards.lines(lines, listOf(ChildPlace("kid", at(15))), true, setOf(work.id))
        assertEquals(3, opened.filterIsInstance<ChatLine.Step>().size)
        assertEquals(lines.size + 4, opened.size)
    }

    @Test fun earlierHistoryMustLoadBeforeAChildIsPlacedAndUndatedChildrenUseTheLatestExchange() {
        val old = listOf(ChildPlace("old", at(1)))
        assertEquals(lines, ChildSessionCards.lines(lines, old, false, emptySet()))
        assertTrue(ChildSessionCards.lines(lines, old, true, emptySet()).filterIsInstance<ChatLine.Work>().first().steps.any { it.id == "child:old" })
        val latest = ChildSessionCards.lines(lines, listOf(ChildPlace("unknown", null)), false, emptySet())
        assertEquals("children-u2", latest.filterIsInstance<ChatLine.Work>().last().id)
    }

    @Test fun missingStartedAtKeepsTheFirstUpdatedTimeAcrossPushes() {
        val seen = ChildSessionPlaces()
        val child = Session("m", "kid", updatedAt = at(15).toString())
        assertEquals(seen.places(listOf(child)), seen.places(listOf(child.copy(updatedAt = at(35).toString()))))
        assertEquals(at(2), seen.places(listOf(child.copy(startedAt = at(2).toString()))).single().at)
    }

    @Test fun cardStatusUsesItsOwnStateAndErrorsRatherThanTheParentsRollup() {
        val row = Session("m", "kid", state = "idle", children = SessionChildren(1, working = 1))
        assertEquals(SubagentStatus.Completed, ChildSessionCards.status(row))
        assertEquals(SubagentStatus.Running, ChildSessionCards.status(row.copy(state = "needs_you")))
        assertEquals(SubagentStatus.Running, ChildSessionCards.status(row.copy(state = "working", turnError = "retrying")))
        assertEquals(SubagentStatus.Failed, ChildSessionCards.status(row.copy(turnError = "failed")))
        assertEquals(SubagentStatus.Stopped, ChildSessionCards.status(row.copy(status = "interrupted")))
    }
}
