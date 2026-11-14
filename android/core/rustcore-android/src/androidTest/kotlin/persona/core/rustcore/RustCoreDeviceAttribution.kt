package persona.core.rustcore

import android.os.Build
import android.os.Debug
import android.os.Process
import android.system.Os
import android.system.OsConstants
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.rustcore.wire.Action
import persona.core.rustcore.wire.ChatEvent
import persona.core.rustcore.wire.ChatPatch
import persona.core.rustcore.wire.ChatSnapshot
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.ListOp

/** Opt-in, instrumented breakdown. These are not substitute results for [RustCoreDeviceBenchmark]'s original spans. */
class RustCoreDeviceAttribution {
    private data class RuntimeCounters(
        val pendingLockNs: Long = 0,
        val pendingSwitches: Long = 0,
        val pendingLockCalls: Long = 0,
        val pendingContended: Long = 0,
        val queueNs: Long = 0,
        val queueSendNs: Long = 0,
        val patchLockNs: Long = 0,
        val patchSwitches: Long = 0,
        val patchLockCalls: Long = 0,
        val patchContended: Long = 0,
        val patchLockWithoutProcNs: Long = 0,
        val patchHeldWorkNs: Long = 0,
        val patchCloneNs: Long = 0,
        val patchEncodeNs: Long = 0,
        val workerDecodeApplyNs: Long = 0,
        val workerEffectEncodeNs: Long = 0,
        val workerHostRunNs: Long = 0,
        val workerHostChangedNs: Long = 0,
        val workerActions: Long = 0,
        val workerTid: Long = 0,
        val workerVoluntarySwitches: Long = 0,
        val workerInvoluntarySwitches: Long = 0,
        val callerVoluntarySwitches: Long = 0,
        val callerInvoluntarySwitches: Long = 0,
    ) {
        constructor(raw: LongArray) : this(
            pendingLockNs = raw[0], pendingSwitches = raw[1], pendingLockCalls = raw[2],
            pendingContended = raw[3], queueNs = raw[4], queueSendNs = raw[5],
            patchLockNs = raw[6], patchSwitches = raw[7], patchLockCalls = raw[8],
            patchContended = raw[9], patchLockWithoutProcNs = raw[10], patchHeldWorkNs = raw[11],
            patchCloneNs = raw[12], patchEncodeNs = raw[13], workerDecodeApplyNs = raw[14],
            workerEffectEncodeNs = raw[15], workerHostRunNs = raw[16], workerHostChangedNs = raw[17],
            workerActions = raw[18], workerTid = raw[19], workerVoluntarySwitches = raw[20],
            workerInvoluntarySwitches = raw[21], callerVoluntarySwitches = raw[22],
            callerInvoluntarySwitches = raw[23],
        )

        operator fun minus(before: RuntimeCounters): RuntimeCounters = RuntimeCounters(
            pendingLockNs - before.pendingLockNs, pendingSwitches - before.pendingSwitches,
            pendingLockCalls - before.pendingLockCalls, pendingContended - before.pendingContended,
            queueNs - before.queueNs, queueSendNs - before.queueSendNs,
            patchLockNs - before.patchLockNs, patchSwitches - before.patchSwitches,
            patchLockCalls - before.patchLockCalls, patchContended - before.patchContended,
            patchLockWithoutProcNs - before.patchLockWithoutProcNs,
            patchHeldWorkNs - before.patchHeldWorkNs,
            patchCloneNs - before.patchCloneNs,
            patchEncodeNs - before.patchEncodeNs, workerDecodeApplyNs - before.workerDecodeApplyNs,
            workerEffectEncodeNs - before.workerEffectEncodeNs, workerHostRunNs - before.workerHostRunNs,
            workerHostChangedNs - before.workerHostChangedNs, workerActions - before.workerActions,
            workerTid, workerVoluntarySwitches - before.workerVoluntarySwitches,
            workerInvoluntarySwitches - before.workerInvoluntarySwitches,
            callerVoluntarySwitches - before.callerVoluntarySwitches,
            callerInvoluntarySwitches - before.callerInvoluntarySwitches,
        )

        fun json(): JSONObject = JSONObject()
            .put("pendingLockNs", pendingLockNs).put("pendingSwitches", pendingSwitches)
            .put("pendingLockCalls", pendingLockCalls).put("pendingContended", pendingContended)
            .put("queueNs", queueNs).put("queueSendNs", queueSendNs)
            .put("patchLockNs", patchLockNs).put("patchSwitches", patchSwitches)
            .put("patchLockCalls", patchLockCalls).put("patchContended", patchContended)
            .put("patchLockWithoutProcNs", patchLockWithoutProcNs).put("patchHeldWorkNs", patchHeldWorkNs).put("patchCloneNs", patchCloneNs)
            .put("patchEncodeNs", patchEncodeNs).put("workerDecodeApplyNs", workerDecodeApplyNs)
            .put("workerEffectEncodeNs", workerEffectEncodeNs).put("workerHostRunNs", workerHostRunNs)
            .put("workerHostChangedNs", workerHostChangedNs).put("workerActions", workerActions)
            .put("workerTid", workerTid).put("workerVoluntarySwitches", workerVoluntarySwitches)
            .put("workerInvoluntarySwitches", workerInvoluntarySwitches)
            .put("callerVoluntarySwitches", callerVoluntarySwitches)
            .put("callerInvoluntarySwitches", callerInvoluntarySwitches)
    }

    private data class VmCounters(
        val thread: String,
        val tid: Int,
        val voluntarySwitches: Long,
        val involuntarySwitches: Long,
        val gcCount: Long,
        val gcTimeMs: Long,
        val allocatedBytes: Long,
    )

    @Test fun encodingJniQueueLockDecodeApplyAndGc() {
        val nonce = InstrumentationRegistry.getArguments().getString("rustCoreAttributionNonce")
            ?: error("missing per-run attribution nonce")
        val chat = "benchmark/continuity"
        val action = Action.Chat(chat, ChatEvent.Reconnected)
        val snapshot = ChatSnapshot(
            """[{"id":"benchmark-item","kind":"text","text":"benchmark","seq":1}]""",
            "history", 1, null, true,
        )
        val report = JSONObject()
            .put("reportVersion", 1)
            .put("nonce", nonce)
            .put("scope", "opt-in instrumented diagnostics; original spans and 50 us / 1 ms budgets unchanged")
            .put("samples", SAMPLES).put("warmUp", WARM_UP)
            .put("model", Build.MODEL).put("device", Build.DEVICE)
            .put("abi", Build.SUPPORTED_ABIS.firstOrNull()).put("api", Build.VERSION.SDK_INT)
            .put("build", Build.DISPLAY).put("kernelPageBytes", pageSize())
            .put("callerTid", Process.myTid())
            .put("callerThread", Thread.currentThread().name)
            .put("notes", "Queue Ns is JNI-to-Runtime dispatch return, not worker latency; lock switch counts are a contention signal, not exact blocking time. /proc deltas and GC counters have measurement overhead. Native timings are inclusive and overlap.")
        val host = RustCoreDeviceBenchmark.Host()
        RustCore(Config(), host).use { core ->
            NativeDiagnostics.enableDiagnostics(core.handleForAttribution())
            val dispatch = LongArray(SAMPLES)
            val frame = LongArray(SAMPLES)
            val dispatchDetails = mutableListOf<JSONObject>()
            val frameDetails = mutableListOf<JSONObject>()
            val list = mutableListOf<String>()
            for (index in 0 until SAMPLES + WARM_UP) {
                val beforeVm = vmCounters()
                val beforeRuntime = counters(core)
                val start = System.nanoTime()
                val encoded = action.bincodeSerialize()
                val encodedAt = System.nanoTime()
                assertTrue(NativeBridge.dispatch(core.handleForAttribution(), encoded))
                val end = System.nanoTime()
                assertTrue("diagnostic flush before next dispatch", NativeDiagnostics.flushDiagnostic(core.handleForAttribution(), 5_000))
                val afterVm = vmCounters()
                val afterRuntime = counters(core)
                if (index >= WARM_UP) {
                    dispatch[index - WARM_UP] = end - start
                    dispatchDetails += detail(start, end, encodedAt - start, beforeVm, afterVm, beforeRuntime, afterRuntime)
                }
            }
            for (index in 0 until SAMPLES + WARM_UP) {
                assertTrue(core.dispatch(Action.OpenChat(chat, snapshot)))
                assertTrue("diagnostic flush before drain", NativeDiagnostics.flushDiagnostic(core.handleForAttribution(), 5_000))
                list.clear()
                val beforeVm = vmCounters()
                val beforeRuntime = counters(core)
                val start = System.nanoTime()
                val bytes = checkNotNull(NativeBridge.drain(core.handleForAttribution(), chat))
                val nativeAt = System.nanoTime()
                val patch = ChatPatch.bincodeDeserialize(bytes)
                val decodedAt = System.nanoTime()
                for (op in patch.ops) {
                    when (op) {
                        is ListOp.Remove -> list.removeAt(op.index.toInt())
                        is ListOp.Insert -> list.add(op.index.toInt(), op.json)
                        is ListOp.Update -> list[op.index.toInt()] = op.json
                    }
                }
                val end = System.nanoTime()
                val afterVm = vmCounters()
                val afterRuntime = counters(core)
                check(patch.ops.isNotEmpty()) { "diagnostic measured an empty patch" }
                check(list.single().contains("benchmark-item"))
                if (index >= WARM_UP) {
                    frame[index - WARM_UP] = end - start
                    frameDetails += detail(start, end, nativeAt - start, beforeVm, afterVm, beforeRuntime, afterRuntime).apply {
                        put("kotlinDecodeNs", decodedAt - nativeAt)
                        put("applyNs", end - decodedAt)
                    }
                }
            }
            assertEquals(1, list.size)
            report.put("dispatchMicros", statistics(dispatch))
                .put("drainDecodeApplyMicros", statistics(frame))
                .put("rawSamples", JSONObject().put("unit", "ns")
                    .put("dispatch", JSONArray(dispatch.toList()))
                    .put("drainDecodeApply", JSONArray(frame.toList())))
                .put("dispatchAttribution", JSONArray(dispatchDetails))
                .put("frameAttribution", JSONArray(frameDetails))
                .put("nativeQueueEncodingPatch", "implemented")
        }
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val file = File(context.getExternalFilesDir(null), "rust-core-attribution.json")
        val reportError = runCatching { file.writeText(report.toString(2) + "\n") }.exceptionOrNull()
        if (reportError != null) report.put("reportFileError", reportError.javaClass.name + ": " + reportError.message)
        // The runner reads this nonce-fenced file after the real JUnit completion. Do not replace its final status.
        check(reportError == null) { "attribution report could not be written: $reportError" }
    }

    private fun counters(core: RustCore): RuntimeCounters {
        val raw = checkNotNull(NativeDiagnostics.diagnostics(core.handleForAttribution()))
        check(raw.size == 24) { "Unexpected native diagnostic layout: ${raw.size}" }
        return RuntimeCounters(raw)
    }

    /** Reflect only the test's own existing handle, outside both measured spans; production RustCore stays verbatim. */
    private fun RustCore.handleForAttribution(): Long {
        val field = RustCore::class.java.getDeclaredField("handle").apply { isAccessible = true }
        return (field.get(this) as java.util.concurrent.atomic.AtomicLong).get()
            .also { check(it != 0L) { "The core is closed" } }
    }

    private fun detail(
        start: Long,
        end: Long,
        encodingNs: Long,
        beforeVm: VmCounters,
        afterVm: VmCounters,
        beforeRuntime: RuntimeCounters,
        afterRuntime: RuntimeCounters,
    ): JSONObject = JSONObject()
        .put("startNs", start).put("durationNs", end - start).put("kotlinEncodingNs", encodingNs)
        .put("vmBefore", vmJson(beforeVm)).put("vmAfter", vmJson(afterVm))
        .put("runtimeBefore", beforeRuntime.json()).put("runtimeAfter", afterRuntime.json())
        .put("runtimeDelta", (afterRuntime - beforeRuntime).json())
        .put("runtimeMeasurementAvailable", afterRuntime.workerTid != 0L
            && afterRuntime.workerTid != Process.myTid().toLong()
            && (afterRuntime.workerVoluntarySwitches >= 0 && afterRuntime.workerInvoluntarySwitches >= 0)
            && if (afterRuntime.patchLockCalls > beforeRuntime.patchLockCalls) {
            // This is a drain: its preceding flush already committed the OpenChat action before the before-snapshot.
            afterRuntime.patchCloneNs > beforeRuntime.patchCloneNs
                && afterRuntime.patchEncodeNs > beforeRuntime.patchEncodeNs
        } else {
            // Dispatch: pending + flush are measured after the JNI call; worker activity must also have advanced.
            (afterRuntime.pendingLockCalls > beforeRuntime.pendingLockCalls
                && afterRuntime.workerActions == beforeRuntime.workerActions + 1)
        })

    private fun vmJson(value: VmCounters): JSONObject = JSONObject()
        .put("thread", value.thread).put("tid", value.tid)
        .put("voluntarySwitches", value.voluntarySwitches)
        .put("involuntarySwitches", value.involuntarySwitches)
        .put("gcCount", value.gcCount).put("gcTimeMs", value.gcTimeMs)
        .put("allocatedBytes", value.allocatedBytes)
        .put("schedulerAvailable", value.voluntarySwitches >= 0 && value.involuntarySwitches >= 0)
        .put("gcAvailable", value.gcCount >= 0 && value.gcTimeMs >= 0 && value.allocatedBytes >= 0)

    private fun vmCounters(): VmCounters {
        val status = File("/proc/self/task/${Process.myTid()}/status").readLines()
        fun switches(prefix: String): Long = status.firstOrNull { it.startsWith(prefix) }
            ?.substringAfter(':')?.trim()?.toLongOrNull() ?: -1
        return VmCounters(
            thread = Thread.currentThread().name,
            tid = Process.myTid(),
            voluntarySwitches = switches("voluntary_ctxt_switches:"),
            involuntarySwitches = switches("nonvoluntary_ctxt_switches:"),
            gcCount = Debug.getRuntimeStat("art.gc.gc-count")?.toLongOrNull() ?: -1,
            gcTimeMs = Debug.getRuntimeStat("art.gc.gc-time")?.toLongOrNull() ?: -1,
            allocatedBytes = Debug.getRuntimeStat("art.gc.bytes-allocated")?.toLongOrNull() ?: -1,
        )
    }

    private fun pageSize(): Long = try { Os.sysconf(OsConstants._SC_PAGESIZE) } catch (_: Exception) { -1 }

    private fun statistics(values: LongArray): JSONObject {
        val sorted = values.sorted()
        fun percentile(percent: Double): Double = sorted[((sorted.size - 1) * percent).toInt()] / 1_000.0
        return JSONObject()
            .put("p50", percentile(.50)).put("p99", percentile(.99))
            .put("max", sorted.last() / 1_000.0)
    }

    companion object {
        private const val SAMPLES = 1_000
        private const val WARM_UP = 100
    }
}
