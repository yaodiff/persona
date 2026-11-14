package persona.core.rustcore

import java.io.File
import java.security.MessageDigest
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.rustcore.wire.Action
import persona.core.rustcore.wire.ChatEvent
import persona.core.rustcore.wire.ChatPatch
import persona.core.rustcore.wire.ChatSnapshot
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.ListOp

/** Host-only segmented reference; never a replacement for the original Android benchmark or its budgets. */
class RustCoreBoundaryCostTest {
    @Test fun recordRealRuntimeJniAndCodecSegments() {
        val revision = System.getenv("PERSONA_MEASUREMENT_REVISION") ?: "unreported"
        val nonce = System.getenv("PERSONA_MEASUREMENT_NONCE") ?: error("missing measurement nonce")
        val library = File(checkNotNull(System.getProperty("persona.core.boundary.library")))
        val output = File(checkNotNull(System.getenv("PERSONA_MEASUREMENT_OUTPUT"))).apply { mkdirs() }
        val chat = "benchmark/continuity"
        val action = Action.Chat(chat, ChatEvent.Reconnected)
        val snapshot = ChatSnapshot(
            """[{"id":"benchmark-item","kind":"text","text":"benchmark","seq":1}]""",
            "history", 1, null, true,
        )
        val snapshotBytes = Action.OpenChat(chat, snapshot).bincodeSerialize()
        val encoded = action.bincodeSerialize()
        val handle = NativeBridge.create(HostBridge(object : CoreHost {
            override fun changed(version: Long) {}
            override fun run(effect: persona.core.rustcore.wire.Effect) {}
        }), Config().bincodeSerialize())
        assertTrue(handle != 0L)
        val diagnosticsEnabled = System.getenv("PERSONA_MEASUREMENT_DIAGNOSTICS") != "0"
        if (diagnosticsEnabled) NativeDiagnostics.enableDiagnostics(handle)
        val initialCounters = if (diagnosticsEnabled) counters(handle) else LongArray(25)
        println("PERSONA_BOUNDARY_INITIAL_COUNTERS=${initialCounters.joinToString()}")
        println("PERSONA_BOUNDARY_NATIVE=${System.getProperty("persona.core.library")}")
        var patchBytes = ByteArray(0)
        var patchOps = 0
        var patchVersion = 0uL
        val samples = linkedMapOf(
            "jniRegistryArcLease" to Samples(),
            "kotlinActionEncode" to Samples(),
            "publicJniDispatch" to Samples(),
            "publicFrameDecodeApply" to Samples(),
            "nativeQueue" to Samples(),
            "nativeQueueSend" to Samples(),
            "nativePendingLock" to Samples(),
            "nativePendingLockWithoutProc" to Samples(),
            "nativePatchLock" to Samples(),
            "nativePatchClone" to Samples(),
            "nativePatchEncode" to Samples(),
            "publicJniDrain" to Samples(),
            "kotlinPatchDecode" to Samples(),
            "listApply" to Samples(),
        )
        try {
            for (index in 0 until WARM_UP + SAMPLES) {
                timed(samples.getValue("jniRegistryArcLease"), index) { check(NativeDiagnostics.lease(handle)) }
                timed(samples.getValue("kotlinActionEncode"), index) { action.bincodeSerialize() }
                val beforeDispatch = if (diagnosticsEnabled) counters(handle) else LongArray(25)
                val dispatchStart = System.nanoTime()
                assertTrue(NativeBridge.dispatch(handle, encoded))
                samples.getValue("publicJniDispatch").add(System.nanoTime() - dispatchStart, index)
                if (diagnosticsEnabled) {
                    recordDelta(samples, beforeDispatch, counters(handle), index, "nativeQueue", "nativeQueueSend", "nativePendingLock", "nativePendingLockWithoutProc")
                }
                assertTrue(NativeBridge.flush(handle, 5_000))
                assertTrue(NativeBridge.dispatch(handle, snapshotBytes))
                assertTrue(NativeBridge.flush(handle, 5_000))
                val beforeFrame = if (diagnosticsEnabled) counters(handle) else LongArray(25)
                val frameStart = System.nanoTime()
                val drainStart = frameStart
                patchBytes = checkNotNull(NativeBridge.drain(handle, chat))
                val decodedAt = System.nanoTime()
                val patch = ChatPatch.bincodeDeserialize(patchBytes)
                val appliedAt = System.nanoTime()
                val list = mutableListOf<String>()
                for (op in patch.ops) {
                    when (op) {
                        is ListOp.Remove -> list.removeAt(op.index.toInt())
                        is ListOp.Insert -> list.add(op.index.toInt(), op.json)
                        is ListOp.Update -> list[op.index.toInt()] = op.json
                    }
                }
                val frameEnd = System.nanoTime()
                check(patch.ops.isNotEmpty()) { "the frame must contain a real patch" }
                check(list.single().contains("benchmark-item"))
                patchOps = patch.ops.size
                patchVersion = patch.version
                samples.getValue("publicFrameDecodeApply").add(frameEnd - frameStart, index)
                samples.getValue("publicJniDrain").add(decodedAt - drainStart, index)
                samples.getValue("kotlinPatchDecode").add(appliedAt - decodedAt, index)
                samples.getValue("listApply").add(frameEnd - appliedAt, index)
                if (diagnosticsEnabled) {
                    recordDelta(samples, beforeFrame, counters(handle), index, "nativePatchLock", "nativePatchClone", "nativePatchEncode")
                }
            }
            val report = buildJsonObject {
                put("nonce", nonce)
                put("reportVersion", 2)
                put("scope", "host diagnostic reference only; not Android emulator or physical-device acceptance")
                put("sourceRevision", revision)
                put("hostLibrarySha256", library.sha256())
                put("hostLibraryPath", library.absolutePath)
                put("diagnosticsEnabled", diagnosticsEnabled)
                put("initialCounters", initialCounters.joinToString(","))
                put("warmUp", WARM_UP)
                put("samples", SAMPLES)
                putJsonObject("payload") {
                    put("action", "Reconnected followed by one identical bounded-item OpenChat per iteration")
                    put("frameSpan", "JNI drain, Kotlin decode of the same bytes, apply of the same nonempty patch; OpenChat and flush excluded")
                    put("leaseSpan", "JNI registry lookup + Arc clone/drop; no dispatch")
                    put("nativeSpans", "Actual runtime diagnostic counter deltas. Native fields are inclusive and overlap; never add them into wall time")
                    put("hostProfile", "release; test-support feature only")
                    put("kotlinTimer", "System.nanoTime; p50 index 499 and p99 index 989 after sorting 1000 samples")
                }
                putJsonObject("inputEvidence") {
                    put("actionBytes", encoded.size)
                    put("actionSha256", encoded.sha256())
                    put("openChatBytes", snapshotBytes.size)
                    put("openChatSha256", snapshotBytes.sha256())
                    put("lastPatchBytes", patchBytes.size)
                    put("lastPatchOps", patchOps)
                    put("lastPatchVersion", patchVersion.toString())
                    put("lastPatchSha256", patchBytes.sha256())
                }
                putJsonObject("environment") {
                    put("java", System.getProperty("java.version"))
                    put("javaVm", System.getProperty("java.vm.name"))
                    put("os", System.getProperty("os.name"))
                    put("arch", System.getProperty("os.arch"))
                    put("gradle", System.getenv("PERSONA_MEASUREMENT_GRADLE") ?: "unreported")
                    put("rustc", System.getenv("PERSONA_MEASUREMENT_RUSTC") ?: "unreported")
                    put("cargoProfile", "release")
                    put("cargoFeatures", "test-support")
                }
                put("testSourceSha256", File(System.getenv("PERSONA_MEASUREMENT_TEST_SOURCE") ?: "").takeIf { it.isFile }?.sha256() ?: "unreported")
                put("instrumentationDiffSha256", File(System.getenv("PERSONA_MEASUREMENT_DIFF") ?: "").takeIf { it.isFile }?.sha256() ?: "unreported")
                for ((name, sample) in samples) put(name, sample.json())
            }
            output.resolve("boundary-cost.json").writeText(report.toString() + "\n")
            println("PERSONA_BOUNDARY_COST=${report}")
        } finally {
            NativeBridge.destroy(handle)
        }
    }

    private fun counters(handle: Long): LongArray = checkNotNull(NativeDiagnostics.diagnostics(handle))

    private fun recordDelta(samples: Map<String, Samples>, before: LongArray, after: LongArray, sampleIndex: Int, vararg fields: String) {
        val indices = mapOf(
            "nativePendingLock" to 0,
            "nativePendingLockWithoutProc" to 24,
            "nativeQueue" to 4,
            "nativeQueueSend" to 5,
            "nativePatchLock" to 6,
            "nativePatchClone" to 12,
            "nativePatchEncode" to 13,
        )
        for (field in fields) {
            val index = indices.getValue(field)
            samples.getValue(field).add(after[index] - before[index], sampleIndex)
        }
    }

    private class Samples {
        val values = LongArray(SAMPLES)
        fun add(value: Long, index: Int) {
            if (index >= WARM_UP && index < WARM_UP + SAMPLES) values[index - WARM_UP] = value
        }
        fun json() = buildJsonObject {
            val sorted = values.sorted()
            put("p50Micros", sorted[499] / 1000.0)
            put("p99Micros", sorted[989] / 1000.0)
            put("maxMicros", sorted.last() / 1000.0)
            putJsonArray("rawNanoseconds") { values.forEach { add(JsonPrimitive(it)) } }
        }
    }

    private inline fun timed(sample: Samples, index: Int, block: () -> Unit) {
        val start = System.nanoTime()
        block()
        sample.add(System.nanoTime() - start, index)
    }

    private fun ByteArray.sha256(): String = MessageDigest.getInstance("SHA-256")
        .digest(this).joinToString("") { "%02x".format(it) }

    private fun File.sha256(): String = MessageDigest.getInstance("SHA-256")
        .digest(readBytes()).joinToString("") { "%02x".format(it) }

    companion object {
        private const val WARM_UP = 100
        private const val SAMPLES = 1000
    }
}
