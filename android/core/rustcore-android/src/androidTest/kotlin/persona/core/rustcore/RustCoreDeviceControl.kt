package persona.core.rustcore

import android.os.Build
import android.system.Os
import android.system.OsConstants
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.rustcore.wire.Action
import persona.core.rustcore.wire.ChatEvent
import persona.core.rustcore.wire.ChatSnapshot
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.ListOp

/** Low-overhead control: no per-iteration /proc, GC, atomics, or JSON construction inside its timed spans. */
class RustCoreDeviceControl {
    @Test fun cpuAndJniWithoutProcInstrumentation() {
        val nonce = InstrumentationRegistry.getArguments().getString("rustCoreControlNonce")
            ?: error("missing per-run control nonce")
        val chat = "benchmark/continuity"
        val action = Action.Chat(chat, ChatEvent.Reconnected)
        val snapshot = ChatSnapshot(
            """[{"id":"benchmark-item","kind":"text","text":"benchmark","seq":1}]""",
            "history", 1, null, true,
        )
        val cpu = LongArray(SAMPLES)
        val encoding = LongArray(SAMPLES)
        val dispatch = LongArray(SAMPLES)
        val frame = LongArray(SAMPLES)
        val host = RustCoreDeviceBenchmark.Host()
        // The original two loops keep their exact order. Standalone CPU/encoding controls run afterward,
        // so their work cannot change the original dispatch/frame sequence's scheduling background.
        RustCore(Config(), host).use { core ->
            assertTrue(core.dispatch(Action.OpenChat(chat, ChatSnapshot("[]", null, 0, null, false))))
            assertTrue(core.flush(5_000))
            for (index in 0 until SAMPLES + WARM_UP) {
                val start = System.nanoTime()
                assertTrue(core.dispatch(action))
                val end = System.nanoTime()
                assertTrue(core.flush(5_000))
                if (index >= WARM_UP) dispatch[index - WARM_UP] = end - start
            }
            val list = mutableListOf<String>()
            for (index in 0 until SAMPLES + WARM_UP) {
                assertTrue(core.dispatch(Action.OpenChat(chat, snapshot)))
                assertTrue(core.flush(5_000))
                list.clear()
                val start = System.nanoTime()
                val patch = core.drain(chat)
                for (op in patch.ops) {
                    when (op) {
                        is ListOp.Remove -> list.removeAt(op.index.toInt())
                        is ListOp.Insert -> list.add(op.index.toInt(), op.json)
                        is ListOp.Update -> list[op.index.toInt()] = op.json
                    }
                }
                val end = System.nanoTime()
                check(patch.ops.isNotEmpty())
                check(list.single().contains("benchmark-item"))
                if (index >= WARM_UP) frame[index - WARM_UP] = end - start
            }
            for (index in 0 until SAMPLES + WARM_UP) {
                val start = System.nanoTime()
                var value = 0L
                for (step in 0 until 100) value = value * 31 + step
                check(value != Long.MIN_VALUE)
                if (index >= WARM_UP) cpu[index - WARM_UP] = System.nanoTime() - start
            }
            for (index in 0 until SAMPLES + WARM_UP) {
                val start = System.nanoTime()
                action.bincodeSerialize()
                if (index >= WARM_UP) encoding[index - WARM_UP] = System.nanoTime() - start
            }
        }
        val metadata = JSONObject()
            .put("reportVersion", 1).put("nonce", nonce)
            .put("scope", "low-overhead control without /proc/GC/atomic diagnostics; no performance budget assertion")
            .put("samples", SAMPLES).put("warmUp", WARM_UP)
            .put("model", Build.MODEL).put("device", Build.DEVICE)
            .put("abi", Build.SUPPORTED_ABIS.firstOrNull()).put("api", Build.VERSION.SDK_INT)
            .put("build", Build.DISPLAY).put("kernelPageBytes", pageSize())
            .put("cpuLoopMicros", statistics(cpu))
            .put("encodingMicros", statistics(encoding))
            .put("dispatchMicros", statistics(dispatch))
            .put("drainDecodeApplyMicros", statistics(frame))
            .put("rawSamples", JSONObject().put("unit", "ns")
                .put("cpu", JSONArray(cpu.toList())).put("encoding", JSONArray(encoding.toList()))
                .put("dispatch", JSONArray(dispatch.toList())).put("drainDecodeApply", JSONArray(frame.toList())))
        val file = File(InstrumentationRegistry.getInstrumentation().targetContext.getExternalFilesDir(null),
            "rust-core-control.json")
        file.writeText(metadata.toString(2) + "\n")
    }

    private fun pageSize(): Long = try { Os.sysconf(OsConstants._SC_PAGESIZE) } catch (_: Exception) { -1 }

    private fun statistics(values: LongArray): JSONObject {
        val sorted = values.sorted()
        fun percentile(percent: Double): Double = sorted[((sorted.size - 1) * percent).toInt()] / 1_000.0
        return JSONObject().put("p50", percentile(.50)).put("p99", percentile(.99)).put("max", sorted.last() / 1_000.0)
    }

    companion object {
        private const val SAMPLES = 1_000
        private const val WARM_UP = 100
    }
}
