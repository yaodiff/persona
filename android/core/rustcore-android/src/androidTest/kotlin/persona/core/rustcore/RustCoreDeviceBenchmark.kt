package persona.core.rustcore

import android.os.Build
import android.system.Os
import android.system.OsConstants
import android.util.Log
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import java.util.concurrent.CopyOnWriteArrayList
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.rustcore.wire.Action
import persona.core.rustcore.wire.ChatEvent
import persona.core.rustcore.wire.ChatSnapshot
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.ListOp
import persona.core.rustcore.wire.WireBatch

/** A release-test-APK diagnostic, not a physical-device or production-screen performance acceptance result. */
class RustCoreDeviceBenchmark {
    internal class Host : CoreHost {
        val versions = CopyOnWriteArrayList<Long>()
        override fun changed(version: Long) { versions.add(version) }
        override fun run(effect: persona.core.rustcore.wire.Effect) = Unit
    }

    @Test fun dispatchAndFrameCosts() {
        val host = Host()
        val chat = "benchmark/continuity"
        RustCore(Config(), host).use { core ->
            assertTrue(core.dispatch(Action.OpenChat(chat, ChatSnapshot("[]", null, 0, null, false))))
            assertTrue("warm-up flush", core.flush(5_000))
            val dispatch = LongArray(SAMPLES)
            val frame = LongArray(SAMPLES)
            val list = mutableListOf<String>()
            // Reconnected performs real bincode encoding and the public JNI dispatch, including a host effect, without
            // worker list mutation. Worker synchronization is outside both timed spans.
            for (index in 0 until SAMPLES + WARM_UP) {
                val start = System.nanoTime()
                assertTrue(core.dispatch(Action.Chat(chat, ChatEvent.Reconnected)))
                val dispatchNanos = System.nanoTime() - start
                assertTrue("flush before next dispatch", core.flush(5_000))
                if (index >= WARM_UP) dispatch[index - WARM_UP] = dispatchNanos
            }
            // Each frame has a real patch, not an empty drain after the first duplicate item. The fresh snapshot
            // replaces one bounded item; flush and OpenChat's snapshot encoding are deliberately outside the frame
            // span. core.drain still includes Rust patch bincode encoding and JNI byte-array creation.
            val snapshot = ChatSnapshot(
                """[{"id":"benchmark-item","kind":"text","text":"benchmark","seq":1}]""",
                "history", 1, null, true,
            )
            for (index in 0 until SAMPLES + WARM_UP) {
                assertTrue(core.dispatch(Action.OpenChat(chat, snapshot)))
                assertTrue("flush before drain", core.flush(5_000))
                list.clear()
                val frameStart = System.nanoTime()
                val patch = core.drain(chat)
                for (op in patch.ops) {
                    when (op) {
                        is ListOp.Remove -> list.removeAt(op.index.toInt())
                        is ListOp.Insert -> list.add(op.index.toInt(), op.json)
                        is ListOp.Update -> list[op.index.toInt()] = op.json
                    }
                }
                val frameNanos = System.nanoTime() - frameStart
                check(patch.ops.isNotEmpty()) { "benchmark measured an empty patch" }
                check(list.single().contains("benchmark-item"))
                if (index >= WARM_UP) frame[index - WARM_UP] = frameNanos
            }
            assertEquals(1, list.size)
            assertTrue("the real worker emitted patches", host.versions.isNotEmpty())
            val context = InstrumentationRegistry.getInstrumentation().targetContext
            val nonce = InstrumentationRegistry.getArguments().getString("rustCoreBenchmarkNonce")
                ?: error("missing per-run benchmark nonce")
            val dispatchStatistics = statistics(dispatch, DISPATCH_BUDGET_MICROS)
            val frameStatistics = statistics(frame, FRAME_BUDGET_MICROS)
            val metadata = JSONObject()
                .put("reportVersion", 1)
                .put("nonce", nonce)
                .put("scope", "optimized instrumentation APK; emulator diagnostics, not physical-device acceptance")
                .put("samples", SAMPLES).put("warmUp", WARM_UP)
                .put("model", Build.MODEL).put("device", Build.DEVICE)
                .put("abi", Build.SUPPORTED_ABIS.firstOrNull()).put("api", Build.VERSION.SDK_INT)
                .put("build", Build.DISPLAY).put("kernelPageBytes", pageSize())
                .put("dispatchMicros", dispatchStatistics)
                .put("drainDecodeApplyMicros", frameStatistics)
                .put("rawSamples", JSONObject()
                    .put("unit", "ns")
                    .put("dispatch", JSONArray(dispatch.toList()))
                    .put("drainDecodeApply", JSONArray(frame.toList())))
            val report = File(context.getExternalFilesDir(null), "rust-core-benchmark.json")
            val reportError = runCatching { report.writeText(metadata.toString(2) + "\n") }.exceptionOrNull()
            if (reportError != null) {
                metadata.put("reportFileError", reportError.javaClass.name + ": " + reportError.message)
            }
            // Code 2 is evidence only. AndroidJUnitRunner still emits its own status 0 after a real PASS;
            // the runner ignores this report-only code and keeps the assertions below as the budget verdict.
            InstrumentationRegistry.getInstrumentation().sendStatus(2, android.os.Bundle().apply {
                putString("rustCoreBenchmarkReport", metadata.toString())
            })
            Log.i("RustCoreBenchmark", metadata.toString())
            println("RustCoreBenchmark: $metadata")
            assertTrue(
                "dispatch p99 must be <= $DISPATCH_BUDGET_MICROS us: $dispatchStatistics",
                dispatchStatistics.getDouble("p99") <= DISPATCH_BUDGET_MICROS,
            )
            assertTrue(
                "drain+decode+apply p99 must be <= $FRAME_BUDGET_MICROS us: $frameStatistics",
                frameStatistics.getDouble("p99") <= FRAME_BUDGET_MICROS,
            )
            check(reportError == null) { "benchmark report could not be written: $reportError" }
        }
    }

    private fun pageSize(): Long = try { Os.sysconf(OsConstants._SC_PAGESIZE) } catch (_: Exception) { -1 }

    private fun statistics(values: LongArray, budgetMicros: Double): JSONObject {
        val sorted = values.sorted()
        fun percentile(percent: Double): Double = sorted[((sorted.size - 1) * percent).toInt()] / 1_000.0
        return JSONObject()
            .put("p50", percentile(.50)).put("p99", percentile(.99))
            .put("max", sorted.last() / 1_000.0)
            .put("budgetMicros", budgetMicros)
            .put("passesBudget", sorted[((sorted.size - 1) * .99).toInt()] <= budgetMicros * 1_000)
    }

    companion object {
        private const val SAMPLES = 1_000
        private const val WARM_UP = 100
        private const val DISPATCH_BUDGET_MICROS = 50.0
        private const val FRAME_BUDGET_MICROS = 1_000.0
    }
}
