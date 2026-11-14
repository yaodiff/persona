package persona.core.domain

import persona.core.contracts.Item
import persona.core.domain.ContinuityReducer.Gap
import persona.core.domain.ContinuitySync.Action
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** A read covers the rev moves heard when it was sent, not when it came back (#3601). */
class ContinuitySyncRevMovesTest {
    private fun item(seq: Int) = Item("$seq#0", seq)

    private fun opened(): ContinuitySync {
        val reducer = ContinuityReducer()
        reducer.restore(listOf(item(1), item(2), item(3)), "h", 3, false, true)
        return ContinuitySync(reducer)
    }

    @Test fun aMoveHeardWhileTheReadIsOnItsWayReadsOnceMore() {
        val sync = opened()
        assertEquals(listOf(Action.FetchAfter(3)), sync.settle(Gap.Fill)) // lines 4 and 5: their push is lost
        assertEquals(emptyList<Action>(), sync.continueFill())
        assertFalse(sync.behind)
        sync.settle(Gap.Fill) // line 6 is written while the read for 4 and 5 is out
        assertTrue(sync.behind)
        // The read was answered from before line 6: it is complete as far as it goes, and one more read follows.
        val first = sync.after(listOf(item(4), item(5)), "h", 3, 5)
        assertEquals(Gap.None, first.gap)
        assertEquals(listOf(Action.FetchAfter(5)), first.actions)
        assertFalse(sync.behind)
        val second = sync.after(listOf(item(6)), "h", 5, 6)
        assertEquals(emptyList<Action>(), second.actions)
        assertEquals(listOf(4, 5, 6), sync.reducer.items.map { it.seq }.drop(3))
        // The fill ended: a new move schedules a new fill as before.
        assertEquals(listOf(Action.FetchAfter(6)), sync.continueFill())
    }

    @Test fun aMoveHeardBeforeTheReadWasSentIsCoveredByIt() {
        val sync = opened()
        sync.settle(Gap.Fill)
        sync.settle(Gap.Fill) // a second gap while the read is in flight: its completion reads again
        assertEquals(emptyList<Action>(), sync.continueFill())
        val done = sync.after(listOf(item(4), item(5)), "h", 3, 5)
        assertEquals(listOf(Action.FetchAfter(5)), done.actions)
        assertFalse(sync.behind)
    }

    @Test fun movesThatKeepComingStopAtThePageCapWithTheLatestPage() {
        val sync = opened()
        sync.settle(Gap.Fill)
        var actions = sync.continueFill()
        var seq = 3
        repeat(7) {
            sync.settle(Gap.Fill)
            seq++
            actions = sync.after(listOf(item(seq)), "h", seq - 1, seq).actions
            assertEquals(listOf(Action.FetchAfter(seq)), actions)
        }
        sync.settle(Gap.Fill)
        seq++
        assertEquals(listOf(Action.FetchLatest()), sync.after(listOf(item(seq)), "h", seq - 1, seq).actions)
        sync.reload(listOf(item(seq + 1)), "h", seq + 1, false)
        assertFalse(sync.behind)
    }
}
