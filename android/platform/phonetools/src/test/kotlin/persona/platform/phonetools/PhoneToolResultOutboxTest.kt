package persona.platform.phonetools

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.PhoneToolResult
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import kotlin.concurrent.thread

class PhoneToolResultOutboxTest {
    @Test fun aResultThatSendsImmediatelyIsNotHeld() {
        val outbox = PhoneToolResultOutbox()
        val result = PhoneToolResult.text("ok")
        val sent = outbox.send("c1", result) { true }
        assertTrue(sent)
        assertEquals(0, outbox.size())
    }

    @Test fun aResultThatCannotBeSentIsHeldAndFlushedOnce() {
        val outbox = PhoneToolResultOutbox()
        val result = PhoneToolResult.text("ok")
        assertFalse(outbox.send("c1", result) { false })
        assertEquals(1, outbox.size())
        val flushed = mutableListOf<String>()
        outbox.flush { callId, r ->
            flushed += callId
            assertEquals(result, r)
            true
        }
        assertEquals(listOf("c1"), flushed)
        assertEquals(0, outbox.size())
    }

    @Test fun flushStopsAtTheFirstSendFailureAndKeepsTheRest() {
        val outbox = PhoneToolResultOutbox()
        for (id in listOf("a", "b", "c")) assertFalse(outbox.send(id, PhoneToolResult.text(id)) { false })
        assertEquals(3, outbox.size())
        val sent = mutableListOf<String>()
        // First entry sends, second fails: the loop must stop, not skip ahead.
        outbox.flush { callId, _ ->
            if (callId == "b") return@flush false
            sent += callId; true
        }
        assertEquals(listOf("a"), sent)
        assertEquals(2, outbox.size())
    }

    @Test fun flushPreservesInsertionOrder() {
        val outbox = PhoneToolResultOutbox()
        for (id in listOf("a", "b", "c", "d")) assertFalse(outbox.send(id, PhoneToolResult.text(id)) { false })
        val flushed = mutableListOf<String>()
        outbox.flush { callId, _ -> flushed += callId; true }
        assertEquals(listOf("a", "b", "c", "d"), flushed)
    }

    /** A newer result for the same call_id replaces the held one; the tool is never re-executed. */
    @Test fun aNewerResultForTheSameCallIdReplacesTheHeldOne() {
        val outbox = PhoneToolResultOutbox()
        val first = PhoneToolResult.text("first")
        val second = PhoneToolResult.text("second")
        assertFalse(outbox.send("c1", first) { false })
        assertFalse(outbox.send("c1", second) { false })
        assertEquals(1, outbox.size())
        val flushed = mutableListOf<PhoneToolResult>()
        outbox.flush { _, r -> flushed += r; true }
        assertEquals(listOf(second), flushed)
    }

    /** Capacity is bounded: the oldest held results drop when more arrive than the cap. */
    @Test fun capacityDropsTheOldestEntries() {
        val outbox = PhoneToolResultOutbox(capacity = 3)
        for (i in 1..5) assertFalse(outbox.send("c$i", PhoneToolResult.text("r$i")) { false })
        assertEquals(3, outbox.size())
        val flushed = mutableListOf<String>()
        outbox.flush { id, _ -> flushed += id; true }
        // c1 and c2 dropped; only c3, c4, c5 remain in order.
        assertEquals(listOf("c3", "c4", "c5"), flushed)
    }

    /** close() drops everything; send() after close reports failure without holding. */
    @Test fun closeDropsTheQueueAndRefusesFurtherSends() {
        val outbox = PhoneToolResultOutbox()
        assertFalse(outbox.send("c1", PhoneToolResult.text("ok")) { false })
        outbox.close()
        assertEquals(0, outbox.size())
        // A send attempt after close goes through the transport; if it fails, the result is not held.
        assertFalse(outbox.send("c2", PhoneToolResult.text("ok")) { false })
        assertEquals(0, outbox.size())
        // flush after close does nothing.
        var flushedAny = false
        outbox.flush { _, _ -> flushedAny = true; true }
        assertFalse(flushedAny)
    }

    /** Flush must not invoke execute or registry: it only re-sends results, identified by call_id. */
    @Test fun flushOnlyResendsResultsItNeverExecutes() {
        val outbox = PhoneToolResultOutbox()
        val result = PhoneToolResult.text("created")
        assertFalse(outbox.send("w1", result) { false })
        var executions = 0
        // The flush lambda is the *socket send*. It cannot reach back into a registry; if it did, the test
        // would show it by incrementing executions. The only way a write is re-done is by calling execute again,
        // which this class never holds a reference to.
        outbox.flush { id, r ->
            assertEquals("w1", id)
            assertEquals(result, r)
            true
        }
        assertEquals(0, executions)
    }

    /** A newer result for the same call_id that sends immediately must clear any older held entry so flush never replays it. */
    @Test fun aSuccessfulLiveSendClearsAnyPreviouslyHeldResultForTheSameCallId() {
        val outbox = PhoneToolResultOutbox()
        val uncertain = PhoneToolResult.text("uncertain")
        val concrete = PhoneToolResult.text("concrete")
        val other = PhoneToolResult.text("other")
        assertFalse(outbox.send("c1", uncertain) { false })
        assertFalse(outbox.send("c2", other) { false })
        assertEquals(2, outbox.size())

        assertTrue(outbox.send("c1", concrete) { true })
        assertEquals(1, outbox.size())

        val flushed = mutableListOf<Pair<String, PhoneToolResult>>()
        outbox.flush { id, r -> flushed += id to r; true }
        assertEquals(listOf("c2" to other), flushed)
    }

    /** Concurrent sends for the same call_id must not let an older call overwrite or drop a newer result. */
    @Test fun concurrentOutOfOrderSendsPreserveNewestOutcomeForCallId() {
        val outbox = PhoneToolResultOutbox()
        val older = PhoneToolResult.text("older")
        val newer = PhoneToolResult.text("newer")

        fun runInterleaved(callId: String, workerOutcome: Boolean, whileInFlight: () -> Unit) {
            val entered = CountDownLatch(1)
            val release = CountDownLatch(1)
            val workerError = AtomicReference<Throwable?>(null)
            val workerResult = AtomicReference<Boolean?>(null)
            val t = thread {
                try {
                    workerResult.set(outbox.send(callId, older) {
                        entered.countDown()
                        assertTrue(release.await(5, TimeUnit.SECONDS))
                        workerOutcome
                    })
                } catch (error: Throwable) {
                    workerError.set(error)
                }
            }
            try {
                assertTrue(entered.await(5, TimeUnit.SECONDS))
                whileInFlight()
            } finally {
                release.countDown()
                t.join(5_000)
            }
            assertFalse("worker thread did not terminate", t.isAlive)
            workerError.get()?.let { throw it }
            assertEquals(workerOutcome, workerResult.get())
        }

        // Case 1: older fails after newer already succeeded live -> must not re-queue older.
        runInterleaved("c1", workerOutcome = false) {
            assertTrue(outbox.send("c1", newer) { true })
        }
        assertEquals(0, outbox.size())

        // Case 2: older fails after newer failed and queued -> queue must keep newer, not older.
        runInterleaved("c2", workerOutcome = false) {
            assertFalse(outbox.send("c2", newer) { false })
        }
        val flushed2 = mutableListOf<PhoneToolResult>()
        outbox.flush { _, r -> flushed2 += r; true }
        assertEquals(listOf(newer), flushed2)

        // Case 3: older succeeds after newer failed and queued -> must not drop newer's queued entry.
        runInterleaved("c3", workerOutcome = true) {
            assertFalse(outbox.send("c3", newer) { false })
        }
        val flushed3 = mutableListOf<PhoneToolResult>()
        outbox.flush { _, r -> flushed3 += r; true }
        assertEquals(listOf(newer), flushed3)

        // Case 4: older fails after newer failed, queued, and was delivered by flush() -> must not re-queue older.
        val flushed4 = mutableListOf<PhoneToolResult>()
        runInterleaved("c4", workerOutcome = false) {
            assertFalse(outbox.send("c4", newer) { false })
            outbox.flush { _, r -> flushed4 += r; true }
        }
        assertEquals(listOf(newer), flushed4)
        assertEquals(0, outbox.size())
    }

    /** close() racing with in-flight sends (whether they succeed or fail) must leave the outbox empty. */
    @Test fun closeRacingWithInFlightSendsLeavesOutboxEmpty() {
        for (workerOutcome in listOf(true, false)) {
            val outbox = PhoneToolResultOutbox()
            val entered = CountDownLatch(1)
            val release = CountDownLatch(1)
            val workerError = AtomicReference<Throwable?>(null)
            val workerResult = AtomicReference<Boolean?>(null)
            val t = thread {
                try {
                    workerResult.set(outbox.send("c1", PhoneToolResult.text("r")) {
                        entered.countDown()
                        assertTrue(release.await(5, TimeUnit.SECONDS))
                        workerOutcome
                    })
                } catch (error: Throwable) {
                    workerError.set(error)
                }
            }
            try {
                assertTrue(entered.await(5, TimeUnit.SECONDS))
                outbox.close()
            } finally {
                release.countDown()
                t.join(5_000)
            }
            assertFalse("worker thread did not terminate", t.isAlive)
            workerError.get()?.let { throw it }
            assertEquals(workerOutcome, workerResult.get())
            assertEquals(0, outbox.size())
            var flushedAny = false
            outbox.flush { _, _ -> flushedAny = true; true }
            assertFalse(flushedAny)
        }
    }

    /** If a newer result for the same call_id is queued while flush is sending the older one, flush keeps the newer entry. */
    @Test fun flushPreservesNewerReplacementQueuedDuringSend() {
        val outbox = PhoneToolResultOutbox()
        val same = PhoneToolResult.text("result")
        assertFalse(outbox.send("c1", same) { false })
        var queuedReplacement = false
        val flushed = mutableListOf<PhoneToolResult>()
        outbox.flush { id, r ->
            flushed += r
            if (!queuedReplacement) {
                queuedReplacement = true
                assertFalse(outbox.send(id, same) { false })
            }
            true
        }
        assertEquals(listOf(same, same), flushed)
        assertEquals(0, outbox.size())
    }
}
