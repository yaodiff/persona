package persona.feature.chat

import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class ChildSessionListTest {
    private val parent = Session("m", "parent", children = SessionChildren(1, working = 1))
    private val child = Session("m", "child", state = "working", foldedUnder = "parent")

    @Test fun settledHeldChildrenStopAutomaticReadsAndInvalidationsReadIncrementalChanges() = runTest {
        val list = ChildSessionList(parent.key)
        val events = MutableSharedFlow<String>()
        val reads = mutableListOf<String?>()
        var current = parent
        val read: suspend (String?) -> SessionListPage = { since ->
            reads += since
            when (reads.size) {
                1 -> SessionListPage(listOf(child), revision = "r1", total = 1)
                2 -> SessionListPage(listOf(child.copy(state = "idle")), revision = "r2", total = 1)
                else -> SessionListPage(remove = listOf("""["m","child"]"""), revision = "r3")
            }
        }
        fun watch() = backgroundScope.launch {
            list.watch(events, { needsChildSessionRead(current, list.state.value.rows) }, read)
        }
        var job = watch()
        runCurrent()
        advanceTimeBy(60_000); runCurrent()
        assertEquals(listOf<String?>(null), reads)
        current = parent.copy(children = SessionChildren(1))
        events.emit("r2"); runCurrent()
        assertEquals(listOf(null, "r1"), reads)
        assertFalse(needsChildSessionRead(current, list.state.value.rows))
        advanceTimeBy(60_000); runCurrent()
        assertEquals(2, reads.size)
        job.cancelAndJoin()
        job = watch(); runCurrent()
        assertEquals("A complete held list needs no read when resumed", 2, reads.size)
        events.emit("r2"); runCurrent()
        assertEquals("The committed checkpoint is not another invalidation", 2, reads.size)
        events.emit("r3"); runCurrent()
        assertEquals(listOf(null, "r1", "r2"), reads)
        assertTrue(list.state.value.rows.isEmpty())
        assertEquals("r3", list.revision)
        job.cancelAndJoin()
    }

    @Test fun failedReadsKeepThePreviousCheckpointAndExpiredDeltasReplaceTheBaseline() = runTest {
        val list = ChildSessionList(parent.key)
        list.refresh { SessionListPage(listOf(child), revision = "r1", total = 1) }
        try {
            list.refresh { since -> assertEquals("r1", since); error("Second cursor page failed") }
            fail("A partial response is not a committed delta")
        } catch (_: IllegalStateException) { }
        assertEquals(listOf(child), list.state.value.rows)
        assertEquals("r1", list.revision)
        val reads = mutableListOf<String?>()
        val replacement = child.copy(sessionId = "new", state = "idle")
        list.refresh { since ->
            reads += since
            if (since != null) throw SessionListExpired()
            SessionListPage(listOf(replacement), revision = "fresh", total = 1)
        }
        assertEquals(listOf("r1", null), reads)
        assertEquals(listOf(replacement), list.state.value.rows)
        assertEquals("fresh", list.revision)
    }

    @Test fun aCommittedRemovalCannotBeRevivedByOldLocalRowsOrColdLinkMetadata() = runTest {
        val list = ChildSessionList(parent.key)
        assertEquals(listOf(child), childSessionRows(parent.key, emptyList(), list.state.value, listOf(child)))
        list.refresh { SessionListPage(listOf(child), revision = "r1", total = 1) }
        list.refresh { SessionListPage(remove = listOf("""["m","child"]"""), revision = "r2") }
        assertTrue(list.state.value.loaded)
        assertTrue(childSessionRows(parent.key, listOf(child), list.state.value, listOf(child)).isEmpty())
        assertTrue(needsChildSessionRead(parent.copy(children = SessionChildren()), listOf(child.copy(state = "idle"))))
    }

    @Test fun anInvalidationRetainedWhileHiddenIsReadOnResumeWithoutPolling() = runTest {
        val list = ChildSessionList(parent.key)
        val done = parent.copy(children = SessionChildren(1))
        val events = MutableStateFlow("r1")
        list.refresh { SessionListPage(listOf(child.copy(state = "idle")), revision = "r1", total = 1) }
        events.value = "r2"
        val reads = mutableListOf<String?>()
        val job = backgroundScope.launch {
            list.watch(events, { needsChildSessionRead(done, list.state.value.rows) }) { since ->
                reads += since
                SessionListPage(listOf(child.copy(state = "idle", title = "Final result")), revision = "r2", total = 1)
            }
        }
        runCurrent()
        assertEquals(listOf("r1"), reads)
        assertEquals("Final result", list.state.value.rows.single().title)
        advanceTimeBy(60_000); runCurrent()
        assertEquals(1, reads.size)
        job.cancelAndJoin()
    }

    @Test fun invalidationsDuringTheBaselineAreCoalescedWithoutCancellingItsPages() = runTest {
        val list = ChildSessionList(parent.key)
        val events = MutableStateFlow("r1")
        val first = CompletableDeferred<SessionListPage>()
        val reads = mutableListOf<String?>()
        val job = backgroundScope.launch {
            list.watch(events, { needsChildSessionRead(parent, list.state.value.rows) }) { since ->
                reads += since
                if (reads.size == 1) first.await()
                else SessionListPage(listOf(child.copy(title = "Latest")), revision = "r3", total = 1)
            }
        }
        runCurrent()
        events.value = "r2"; runCurrent()
        events.value = "r3"; runCurrent()
        assertEquals(listOf<String?>(null), reads)
        assertNull(list.revision)
        first.complete(SessionListPage(listOf(child), revision = "r2", total = 1))
        runCurrent()
        assertEquals(listOf(null, "r2"), reads)
        assertEquals("r3", list.revision)
        assertEquals("Latest", list.state.value.rows.single().title)
        job.cancelAndJoin()
    }
}
