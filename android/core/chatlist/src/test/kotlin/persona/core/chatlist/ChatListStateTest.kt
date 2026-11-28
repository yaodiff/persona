package persona.core.chatlist

import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.AbstractApplier
import androidx.compose.runtime.BroadcastFrameClock
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Composition
import androidx.compose.runtime.Recomposer
import androidx.compose.runtime.snapshots.Snapshot
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import kotlinx.coroutines.yield
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * Scrolling writes a new offset into the reading anchor on every frame; the chat page reads the open work groups and
 * whether the list follows the end, and must not recompose for an offset (#4039). Counted in a real composition.
 */
@RunWith(RobolectricTestRunner::class)
class ChatListStateTest {
    private object NoNodes : AbstractApplier<Unit>(Unit) {
        override fun insertTopDown(index: Int, instance: Unit) = Unit
        override fun insertBottomUp(index: Int, instance: Unit) = Unit
        override fun remove(index: Int, count: Int) = Unit
        override fun move(from: Int, to: Int, count: Int) = Unit
        override fun onClear() = Unit
    }

    @Composable private fun Reads(read: () -> Any, counted: () -> Unit) {
        val _ = read()
        counted()
    }

    @Test fun offsetsWrittenWhileScrollingRecomposeNoReaderOfExpansionOrFollowing() = runBlocking {
        val clock = BroadcastFrameClock()
        withContext(clock) {
            val recomposer = Recomposer(coroutineContext)
            val running = launch { recomposer.runRecomposeAndApplyChanges() }
            val state = ChatListState(LazyListState(), null, null, ChatReadingPosition())
            var expandedReads = 0
            var followsEndReads = 0
            val composition = Composition(NoNodes, recomposer)
            composition.setContent {
                Reads({ state.expanded }) { expandedReads++ }
                Reads({ state.followsEnd }) { followsEndReads++ }
            }
            suspend fun write(change: () -> Unit) {
                Snapshot.withMutableSnapshot(change)
                Snapshot.sendApplyNotifications()
                repeat(4) { yield(); clock.sendFrame(System.nanoTime()); yield() }
            }
            suspend fun scroll() {
                // What capture() writes on each frame of a scroll: the top row and its offset.
                for (frame in 1..20) write { state.position = state.position.copy(rowId = "row-${frame / 5}", offsetPx = frame * 7) }
            }
            fun reads() = expandedReads to followsEndReads
            try {
                assertEquals(1 to 1, reads())
                // The first write after the first composition may recompose each reader once: the derived values were
                // first computed inside it. Never again for an offset (before #4039: once per frame, 20 per scroll).
                scroll()
                val settled = reads()
                assertTrue("$settled", settled.first <= 2 && settled.second <= 2)
                scroll()
                assertEquals(settled, reads())

                write { state.setExpanded(setOf("work-1")) }
                assertEquals(settled.first + 1 to settled.second, reads())
                scroll()
                assertEquals(settled.first + 1 to settled.second, reads())

                write { state.position = state.position.copy(followsEnd = false, offsetPx = 3) }
                assertEquals(settled.first + 1 to settled.second + 1, reads())
                scroll()
                assertEquals(settled.first + 1 to settled.second + 1, reads())
                assertEquals(setOf("work-1"), state.expanded)
                assertEquals(false, state.followsEnd)
            } finally {
                composition.dispose()
                recomposer.cancel()
                running.cancel()
            }
        }
    }

    @Test fun aOnePixelOvershootSettlesTheRiseToTopHold() {
        assertTrue(expansionNeedsScroll(visibleOffset = 102, target = 100, riseToTop = true))
        assertFalse("a 1px rounding difference must settle", expansionNeedsScroll(101, 100, true))
        assertFalse("a rise-to-top hold never moves the header back down", expansionNeedsScroll(98, 100, true))
        assertTrue(expansionNeedsScroll(102, 100, riseToTop = false))
        assertFalse(expansionNeedsScroll(101, 100, riseToTop = false))
    }
}
