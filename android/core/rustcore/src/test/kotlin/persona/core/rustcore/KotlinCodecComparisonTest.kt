package persona.core.rustcore

import java.io.File
import java.security.MessageDigest
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import persona.core.rustcore.wire.ChatPatch
import persona.core.rustcore.wire.ListOp

/** Fixed-input host allocation microbenchmark, not a full codec-equivalence or frame-performance proof. */
class KotlinCodecComparisonTest {
    @Test fun compareStrictRangeDecoderWithStrictCopiedDecoder() {
        val patch = ChatPatch(
            version = 3u, covered = 2, history = "history", has_more = true, loaded_once = true, verified = true, acked = null,
            ops = listOf(ListOp.Insert(index = 0u, id = "item#0", json = "{\"text\":\"中文🦀\"}")),
        )
        val bytes = patch.bincodeSerialize()
        val ranged = LongArray(SAMPLES)
        val copied = LongArray(SAMPLES)
        val expected = ChatPatch.bincodeDeserialize(bytes)
        assertEquals(expected, patch)
        assertEquals(expected, copiedPatchDecode(bytes))
        val malformed = byteArrayOf(3, 0, 0, 0, 0, 0, 0, 0, 0xc3.toByte(), 0x28, 0xa0.toByte())
        assertThrows(com.novi.serde.DeserializationError::class.java) {
            com.novi.bincode.BincodeDeserializer(malformed).deserialize_str()
        }
        assertThrows(com.novi.serde.DeserializationError::class.java) {
            CopiedStringDeserializer(malformed).deserialize_str()
        }
        val first = System.getenv("PERSONA_CODEC_ORDER") != "copied-first"
        val pairedRange = LongArray(SAMPLES)
        val pairedCopied = LongArray(SAMPLES)
        if (System.getenv("PERSONA_CODEC_INTERLEAVED") == "1") {
            for (index in 0 until WARM_UP + SAMPLES) {
                val startRange = System.nanoTime()
                ChatPatch.bincodeDeserialize(bytes)
                val startCopied = System.nanoTime()
                copiedPatchDecode(bytes)
                val end = System.nanoTime()
                if (index >= WARM_UP) {
                    pairedRange[index - WARM_UP] = startCopied - startRange
                    pairedCopied[index - WARM_UP] = end - startCopied
                }
            }
        } else if (first) {
            measure(ranged) { ChatPatch.bincodeDeserialize(bytes) }
            measure(copied) { copiedPatchDecode(bytes) }
        } else {
            measure(copied) { copiedPatchDecode(bytes) }
            measure(ranged) { ChatPatch.bincodeDeserialize(bytes) }
        }
        val reportNonce = System.getenv("PERSONA_MEASUREMENT_NONCE") ?: error("missing measurement nonce")
        val report = buildJsonObject {
            put("nonce", reportNonce)
            put("scope", "host codec A/B only; not a frame or device performance acceptance")
            put("order", if (System.getenv("PERSONA_CODEC_INTERLEAVED") == "1") "range/copy interleaved per iteration" else if (first) "range-then-copy" else "copy-then-range")
            put("inputBytes", bytes.size)
            put("inputSha256", bytes.sha256())
            put("warmUp", WARM_UP)
            put("samples", SAMPLES)
            put("rangeDecoder", stats(if (System.getenv("PERSONA_CODEC_INTERLEAVED") == "1") pairedRange else ranged))
            put("copiedStrictDecoder", stats(if (System.getenv("PERSONA_CODEC_INTERLEAVED") == "1") pairedCopied else copied))
            put("note", "Allocation microbenchmark with strict UTF-8 and matching validation on the fixed input; order/interleaving is explicit; no production or device performance claim")
        }
        val output = File(checkNotNull(System.getenv("PERSONA_CODEC_COMPARISON_OUTPUT"))).apply { mkdirs() }
        output.resolve("codec-comparison.json").writeText(report.toString() + "\n")
        println("PERSONA_CODEC_COMPARISON=$report")
    }

    private inline fun measure(values: LongArray, block: () -> Unit) {
        for (index in 0 until WARM_UP + SAMPLES) {
            val start = System.nanoTime()
            block()
            if (index >= WARM_UP) values[index - WARM_UP] = System.nanoTime() - start
        }
    }

    private fun copiedPatchDecode(bytes: ByteArray): ChatPatch {
        val input = CopiedStringDeserializer(bytes)
        return ChatPatch.deserialize(input)
    }

    private class CopiedStringDeserializer(private val input: ByteArray) : com.novi.serde.Deserializer {
        private var position = 0
        override fun deserialize_u64(): ULong {
            var value = 0L
            repeat(8) { value = value or ((next().toLong() and 0xffL) shl (it * 8)) }
            return value.toULong()
        }
        override fun deserialize_u32(): UInt = deserialize_variant_index().toUInt()
        override fun deserialize_i64(): Long = deserialize_u64().toLong()
        override fun deserialize_bool(): Boolean = when (val value = next().toInt()) {
            0 -> false
            1 -> true
            else -> throw com.novi.serde.DeserializationError("Incorrect boolean value")
        }
        override fun deserialize_option_tag(): Boolean = deserialize_bool()
        override fun deserialize_len(): Long = deserialize_u64().toLong().also {
            if (it < 0 || it > Int.MAX_VALUE.toLong()) throw com.novi.serde.DeserializationError("Incorrect length value")
        }
        override fun deserialize_variant_index(): Int {
            var value = 0
            repeat(4) { value = value or ((next().toInt() and 0xff) shl (it * 8)) }
            return value
        }
        override fun deserialize_str(): String {
            val length = deserialize_len().toInt()
            if (position + length > input.size) throw com.novi.serde.DeserializationError("Input is not large enough")
            val content = input.copyOfRange(position, position + length)
            position += length
            return try {
                content.decodeToString(0, content.size, throwOnInvalidSequence = true)
            } catch (_: Throwable) {
                throw com.novi.serde.DeserializationError("Incorrect UTF8 string")
            }
        }
        override fun get_buffer_offset(): Int = position
        private fun next(): Byte = input[position++]

        override fun deserialize_bytes() = error("not used by this fixed ChatPatch experiment")
        override fun deserialize_unit() = Unit
        override fun deserialize_char() = error("not used")
        override fun deserialize_f32() = error("not used")
        override fun deserialize_f64() = error("not used")
        override fun deserialize_u8() = error("not used")
        override fun deserialize_u16() = error("not used")
        override fun deserialize_u128() = error("not used")
        override fun deserialize_i8() = error("not used")
        override fun deserialize_i16() = error("not used")
        override fun deserialize_i32() = error("not used")
        override fun deserialize_i128() = error("not used")
        override fun increase_container_depth() = Unit
        override fun decrease_container_depth() = Unit
        override fun check_that_key_slices_are_increasing(key1: com.novi.serde.Slice, key2: com.novi.serde.Slice) = Unit
    }

    private fun stats(values: LongArray) = buildJsonObject {
        val sorted = values.sorted()
        put("p50Micros", sorted[499] / 1000.0)
        put("p99Micros", sorted[989] / 1000.0)
        put("maxMicros", sorted.last() / 1000.0)
        putJsonArray("rawNanoseconds") { values.forEach { add(JsonPrimitive(it)) } }
    }

    private fun ByteArray.sha256() = MessageDigest.getInstance("SHA-256").digest(this)
        .joinToString("") { "%02x".format(it) }

    companion object {
        private const val WARM_UP = 100
        private const val SAMPLES = 1000
    }
}
