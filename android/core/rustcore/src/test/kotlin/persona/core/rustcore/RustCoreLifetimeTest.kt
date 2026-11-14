package persona.core.rustcore

import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.rustcore.wire.Action
import persona.core.rustcore.wire.ChatEvent
import persona.core.rustcore.wire.ChatSnapshot
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.WireBatch

/** The real host JNI boundary, not a mock native implementation. */
class RustCoreLifetimeTest {
    @Test fun closeAndCallsMayRaceWithoutUsingAReleasedNativeHandle() {
        repeat(32) {
            val core = RustCore(Config(), object : CoreHost {
                override fun changed(version: Long) {}
                override fun run(effect: persona.core.rustcore.wire.Effect) {}
            })
            assertTrue(core.dispatch(Action.OpenChat("chat", ChatSnapshot("[]", null, 0, null, false))))
            assertTrue(core.flush(5_000))
            val start = CountDownLatch(1)
            val done = CountDownLatch(3)
            val failures = ConcurrentLinkedQueue<Throwable>()
            val callers = List(3) {
                Thread {
                    try {
                        start.await()
                    val end = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(20)
                    while (System.nanoTime() < end) {
                        try {
                            core.dispatch(Action.Chat("chat", ChatEvent.Latest(WireBatch(
                                """[{"id":"1#0","seq":1,"kind":"text","text":"item"}]""",
                                null, null, 1L, false, null, false,
                            ))))
                            core.drain("chat")
                            core.flush(0)
                        } catch (_: IllegalStateException) {
                            // close() revokes new calls; an already leased native runtime is allowed to finish.
                        } catch (_: NullPointerException) {
                            // JNI drain returns null after close, matching its existing Kotlin adapter.
                        }
                    }
                    } catch (failure: Throwable) {
                        failures += failure
                    } finally {
                        done.countDown()
                    }
                }.apply { start() }
            }
            start.countDown()
            core.close()
            assertTrue("all JNI callers must finish", done.await(5, TimeUnit.SECONDS))
            callers.forEach {
                it.join(5_000)
                assertFalse("JNI caller must terminate", it.isAlive)
            }
            failures.firstOrNull()?.let { throw it }
            try {
                core.flush(0)
                throw AssertionError("closed Kotlin flush must not succeed")
            } catch (_: IllegalStateException) {
                // The public adapter has always reported a closed handle this way.
            }
            core.close()
        }
    }

    @Test fun closeWaitsForTheWorkerCallbackItself() {
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)
        val closed = CountDownLatch(1)
        val core = RustCore(Config(), object : CoreHost {
            override fun changed(version: Long) {
                entered.countDown()
                release.await(5, TimeUnit.SECONDS)
            }
            override fun run(effect: persona.core.rustcore.wire.Effect) {}
        })
        assertTrue(core.dispatch(Action.OpenChat("chat", ChatSnapshot("[]", null, 0, null, false))))
        assertTrue("callback entered", entered.await(5, TimeUnit.SECONDS))
        val closer = Thread {
            core.close()
            closed.countDown()
        }.apply { start() }
        assertFalse("close must not return before its callback", closed.await(100, TimeUnit.MILLISECONDS))
        release.countDown()
        assertTrue("close completes after callback", closed.await(5, TimeUnit.SECONDS))
        closer.join(5_000)
        assertFalse("close thread must terminate", closer.isAlive)
    }
}
