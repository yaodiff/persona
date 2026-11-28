package persona.core.chatlist

import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import org.junit.Assert.*
import org.junit.Test

class ChatListTest {
    private data class Message(val id: String, val seq: Int, val text: String = "原文", val incoming: Boolean = true)

    @Test fun frozenWindowAdmitsPagesAndInteriorHolesButDefersNewIncomingMessages() {
        val window = ChatListWindow<Message>({ it.id }, compareBy { it.seq }, { it.incoming })
        val first = Message("first", 10)
        val last = Message("last", 30)
        window.freeze(listOf(first, last))
        val old = Message("old", 1)
        val hole = Message("hole", 20)
        val new = Message("new", 31)
        val own = Message("own", 32, incoming = false)
        val live = listOf(old, first.copy(text = "修改"), hole, last, new, new, own)
        assertEquals(listOf(old, first, hole, last), window.items(live))
        assertEquals(1, window.newMessageCount(live))
        assertEquals(2, window.newMessageCount(live, below = hole))
        assertEquals(1, window.newMessageCount(live, below = hole, lastRead = last))
        window.freeze(live)
        assertFalse(window.items(live).any { it.id == "new" })
        assertTrue(window.items(listOf(last)).any { it.id == "hole" })
        window.follow()
        assertEquals(live, window.items(live))
        assertEquals(0, window.newMessageCount(live))
    }

    @Test fun deferredStreamingCountDoesNotRescanAndSettlesWithOriginalRule() {
        var idCalls = 0
        var unreadCalls = 0
        val window = ChatListWindow<Message>(
            { idCalls++; it.id },
            compareBy { it.seq },
            { unreadCalls++; it.incoming },
        )
        val last = Message("last", 10)
        window.freeze(listOf(last))
        val draft = Message("draft", 11)
        assertEquals(1, window.newMessageCount(listOf(last, draft, draft)))
        idCalls = 0
        unreadCalls = 0

        val firstChunk = listOf(last, draft.copy(text = "一"), draft.copy(text = "一"))
        val secondChunk = listOf(
            last,
            draft.copy(text = "一二"),
            draft.copy(text = "一二"),
            Message("own", 12, incoming = false),
        )
        val settled = listOf(
            last,
            draft.copy(text = "完成"),
            draft.copy(text = "完成"),
            Message("new", 12),
            Message("own", 13, incoming = false),
            Message("own", 13, incoming = false),
        )
        listOf(firstChunk, secondChunk, settled).forEach { update ->
            assertEquals(1, window.newMessageCount(update, defer = true))
            assertEquals(0, idCalls)
            assertEquals(0, unreadCalls)
        }

        // The oracle is the original rule: de-duplicate by ID, then count unread rows after the boundary.
        val expected = settled.distinctBy { it.id }.count { it.seq > last.seq && it.incoming }
        assertEquals(2, expected)
        assertEquals(expected, window.newMessageCount(settled, defer = false))
        assertTrue(idCalls > 0)
        assertTrue(unreadCalls > 0)

        window.follow()
        assertEquals(0, window.newMessageCount(settled, defer = true))
        window.freeze(listOf(last))
        idCalls = 0
        unreadCalls = 0
        assertEquals(expected, window.newMessageCount(settled, defer = true))
        assertTrue(idCalls > 0)
        assertTrue(unreadCalls > 0)
    }

    @Test fun prefetchRequiresUserIntentWhileLastLineUsesMeasuredExtent() {
        assertFalse(shouldPrefetch(false, true, false, 95, 100, 5))
        assertFalse(shouldPrefetch(true, true, true, 95, 100, 5))
        assertFalse(shouldPrefetch(true, false, false, 95, 100, 5))
        assertFalse(shouldPrefetch(true, true, false, 93, 100, 5))
        assertTrue(shouldPrefetch(true, true, false, 94, 100, 5))
        assertEquals(-672, lastLineScrollOffset(700, 28))
        assertEquals(0, lastLineScrollOffset(20, 28))
        // Under a 100px top bar the last line sits right below it.
        assertEquals(-572, lastLineScrollOffset(700, 28, topPadding = 100))
        assertEquals(0, lastLineScrollOffset(120, 28, topPadding = 100))
    }

    @Test fun freezeIsLazyAndCachedWindowAvoidsResortingWhenOnlyNewerItemsArrive() {
        var idCalls = 0
        var orderCalls = 0
        val window = ChatListWindow<Message>(
            { idCalls++; it.id },
            Comparator { a, b -> orderCalls++; a.seq.compareTo(b.seq) },
            { it.incoming },
        )
        val first = Message("first", 10)
        val last = Message("last", 30)
        window.freeze(listOf(first, last))
        assertTrue(window.isFrozen)
        assertEquals(0, idCalls)
        assertEquals(0, orderCalls)

        val initial = listOf(first, last)
        val frozen1 = window.items(initial)
        assertEquals(listOf(first, last), frozen1)

        // Newer items past the boundary do not mutate the frozen snapshot or force a re-sort.
        val withNewer = listOf(first, last, Message("new-1", 31), Message("new-2", 32))
        val frozen2 = window.items(withNewer)
        assertSame(frozen1, frozen2)

        // Older pages and interior holes before the boundary are still admitted and sorted once.
        val older = Message("old", 1)
        val hole = Message("hole", 20)
        val withOlderAndHole = listOf(older, first, hole, last, Message("new-1", 31), Message("new-2", 32))
        val frozen3 = window.items(withOlderAndHole)
        assertEquals(listOf(older, first, hole, last), frozen3)

        val withMoreNewer = withOlderAndHole + Message("new-3", 33)
        val frozen4 = window.items(withMoreNewer)
        assertSame(frozen3, frozen4)
    }

    @Test fun backgroundItemsDoesNotBlockMainThreadFreezeFollowIsFrozenOrUnreadCount() {
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)
        val workerRef = AtomicReference<Thread?>(null)
        val workerError = AtomicReference<Throwable?>(null)
        val window = ChatListWindow<Message>(
            { message ->
                if (Thread.currentThread() === workerRef.get()) {
                    entered.countDown()
                    assertTrue(release.await(5, TimeUnit.SECONDS))
                }
                message.id
            },
            compareBy { it.seq },
            { it.incoming },
        )
        val first = Message("first", 10)
        val last = Message("last", 20)
        val newer1 = Message("new-1", 21)
        val newer2 = Message("new-2", 22)
        val live = listOf(first, last, newer1, newer2)
        window.freeze(listOf(first, last))

        val worker = Thread {
            try {
                val resolved = window.items(live)
                assertEquals(listOf(first, last), resolved)
            } catch (t: Throwable) {
                workerError.set(t)
            }
        }
        workerRef.set(worker)
        worker.start()
        try {
            assertTrue("background items() did not enter mergeLock", entered.await(5, TimeUnit.SECONDS))
            // All UI-thread queries and transitions must complete without waiting for mergeLock.
            assertTrue(window.isFrozen)
            assertEquals(2, window.newMessageCount(live))
            window.freeze(live)
            window.follow()
            assertFalse(window.isFrozen)
            assertEquals(0, window.newMessageCount(live))
            window.freeze(live)
            assertTrue(window.isFrozen)
        } finally {
            release.countDown()
            worker.join(5_000)
        }
        assertFalse("worker thread did not finish", worker.isAlive)
        assertNull(workerError.get())
    }

    @Test fun newMessageCountUsesFastPathAtBottomAndMatchesDistinctByOracleAcrossScrollPositions() {
        var idCalls = 0
        var unreadCalls = 0
        val window = ChatListWindow<Message>(
            { idCalls++; it.id },
            compareBy { it.seq },
            { unreadCalls++; it.incoming },
        )
        val m1 = Message("m1", 10)
        val m2 = Message("m2", 20)
        val m3 = Message("m3", 30)
        val initial = listOf(m1, m2, m3)
        window.freeze(initial)

        // At or past the newest live item, the O(1) max <= end fast path avoids scanning IDs or unread predicates.
        assertEquals(0, window.newMessageCount(initial))
        assertEquals(0, idCalls)
        assertEquals(0, unreadCalls)

        // With duplicates and mixed incoming flags, first occurrence by ID wins, matching distinctBy(id).
        val live = listOf(
            m1,
            m2,
            m3,
            Message("dup-own-first", 31, incoming = false),
            Message("dup-own-first", 32, incoming = true),
            Message("dup-in-first", 33, incoming = true),
            Message("dup-in-first", 34, incoming = false),
            Message("dup-old-first", 15, incoming = true),
            Message("dup-old-first", 35, incoming = true),
            Message("new-a", 36, incoming = true),
            Message("new-own", 37, incoming = false),
            Message("new-b", 38, incoming = true),
        )

        fun oracle(end: Message) = live.distinctBy { it.id }.count { it.seq > end.seq && it.incoming }

        assertEquals(oracle(m3), window.newMessageCount(live))
        assertTrue(idCalls > 0)
        assertTrue(unreadCalls > 0)

        // Repeated scroll queries against the same live list reuse the sorted unread index with 0 new ID/predicate calls.
        idCalls = 0
        unreadCalls = 0
        for (below in listOf(m1, m2, m3, Message("mid", 34), Message("tail", 38))) {
            assertEquals("below=${below.seq}", oracle(below), window.newMessageCount(live, below = below))
        }
        assertEquals(oracle(m3), window.newMessageCount(live, below = m1, lastRead = m3))
        assertEquals(0, idCalls)
        assertEquals(0, unreadCalls)
    }

    @Test fun bottomThresholdsRejectOverscrollAndTolerateSmallSettleJitter() {
        val leavePx = 32
        val returnPx = 8

        // At the newest edge or overscrolling downward (cannot scroll backward): never leaves bottom.
        assertFalse(hasLeftBottom(firstVisibleItemIndex = 0, firstVisibleItemScrollOffset = 0, canScrollBackward = false, leaveThresholdPx = leavePx))
        assertFalse(hasLeftBottom(firstVisibleItemIndex = 1, firstVisibleItemScrollOffset = 100, canScrollBackward = false, leaveThresholdPx = leavePx))
        // Small micro-wiggle within leave threshold does not exit followsEnd.
        assertFalse(hasLeftBottom(firstVisibleItemIndex = 0, firstVisibleItemScrollOffset = 10, canScrollBackward = true, leaveThresholdPx = leavePx))
        assertFalse(hasLeftBottom(firstVisibleItemIndex = 0, firstVisibleItemScrollOffset = leavePx, canScrollBackward = true, leaveThresholdPx = leavePx))
        // Crossing threshold or scrolling to an older item exits followsEnd.
        assertTrue(hasLeftBottom(firstVisibleItemIndex = 0, firstVisibleItemScrollOffset = leavePx + 1, canScrollBackward = true, leaveThresholdPx = leavePx))
        assertTrue(hasLeftBottom(firstVisibleItemIndex = 1, firstVisibleItemScrollOffset = 0, canScrollBackward = true, leaveThresholdPx = leavePx))

        // Direction check relative to resting/insertion anchor.
        assertFalse(movedAwayFromBottom(0, 0, anchorIndex = 0, anchorOffset = 0))
        assertFalse(movedAwayFromBottom(0, 4, anchorIndex = 0, anchorOffset = 10))
        assertFalse(movedAwayFromBottom(0, 100, anchorIndex = 1, anchorOffset = 0))
        assertTrue(movedAwayFromBottom(0, 33, anchorIndex = 0, anchorOffset = 0))
        assertTrue(movedAwayFromBottom(1, 0, anchorIndex = 0, anchorOffset = 0))

        // Auto-return accepts !canScrollBackward or small settle offset at index 0.
        assertTrue(isAtBottom(firstVisibleItemIndex = 0, firstVisibleItemScrollOffset = 0, canScrollBackward = false, returnTolerancePx = returnPx))
        assertTrue(isAtBottom(firstVisibleItemIndex = 0, firstVisibleItemScrollOffset = 2, canScrollBackward = true, returnTolerancePx = returnPx))
        assertTrue(isAtBottom(firstVisibleItemIndex = 0, firstVisibleItemScrollOffset = returnPx, canScrollBackward = true, returnTolerancePx = returnPx))
        assertFalse(isAtBottom(firstVisibleItemIndex = 0, firstVisibleItemScrollOffset = returnPx + 1, canScrollBackward = true, returnTolerancePx = returnPx))
        assertFalse(isAtBottom(firstVisibleItemIndex = 1, firstVisibleItemScrollOffset = 0, canScrollBackward = true, returnTolerancePx = returnPx))
    }
}
