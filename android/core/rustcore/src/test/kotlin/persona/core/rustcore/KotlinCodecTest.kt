package persona.core.rustcore

import com.novi.serde.DeserializationError
import java.util.Base64
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.rustcore.wire.ChatPatch
import persona.core.rustcore.wire.ChatSnapshot
import persona.core.rustcore.wire.ListOp

/** Exact Rust/bincode bytes and strict UTF-8 behavior of the allocation-reduced generated runtime. */
class KotlinCodecTest {
    @Test fun stringsKeepTheIndependentRustBytesAndStrictErrors() {
        val golden = JsonCodec.parseToJsonElement(
            checkNotNull(javaClass.getResource("/wire-golden.json")).readText(),
        ).jsonObject
        val text = { key: String -> (golden.getValue(key) as JsonPrimitive).content }
        val snapshot = ChatSnapshot(
            items_json = text("itemsJson"),
            history = golden.getValue("history").jsonPrimitive.content,
            covered = 1,
            has_more = null,
            loaded_once = true,
        )
        val encoded = Base64.getDecoder().decode(text("encodedBase64"))
        assertArrayEquals(encoded, snapshot.bincodeSerialize())
        assertEquals(snapshot, ChatSnapshot.bincodeDeserialize(encoded))
        assertEquals("nul: \u0000", text("nulText"))

        val malformed = byteArrayOf(3, 0, 0, 0, 0, 0, 0, 0, 0xc3.toByte(), 0x28, 0xa0.toByte())
        val utf8Error = assertThrows(DeserializationError::class.java) {
            ChatSnapshot.bincodeDeserialize(malformed)
        }
        assertEquals("Incorrect UTF8 string", utf8Error.message)
        val truncated = assertThrows(DeserializationError::class.java) {
            ChatSnapshot.bincodeDeserialize(encoded.copyOf(13))
        }
        assertEquals("Input is not large enough", truncated.message)
        val trailing = assertThrows(DeserializationError::class.java) {
            ChatSnapshot.bincodeDeserialize(encoded + 0)
        }
        assertEquals("Some input bytes were not read", trailing.message)
    }

    @Test fun patchesDecodeAtTheSameByteOffsets() {
        val bytes = ChatPatch(
            version = 3u,
            covered = 2,
            history = "history",
            has_more = true,
            loaded_once = true,
            verified = true,
            acked = null,
            ops = listOf(ListOp.Insert(
                index = 0u, id = "item#0", json = "{\"text\":\"中文🦀\"}",
            )),
        ).bincodeSerialize()
        assertEquals("{\"text\":\"中文🦀\"}",
            (ChatPatch.bincodeDeserialize(bytes).ops.single() as ListOp.Insert).json)
        val truncated = assertThrows(DeserializationError::class.java) {
            ChatPatch.bincodeDeserialize(bytes.copyOf(bytes.size - 1))
        }
        assertEquals("Input is not large enough", truncated.message)
    }

    @Test fun anImpossibleLengthCannotOverflowTheCursorOrAllocate() {
        val bytes = ByteArray(9) { if (it < 8) 0xff.toByte() else 1 }
        bytes[7] = 0x7f
        val error = assertThrows(DeserializationError::class.java) {
            ChatSnapshot.bincodeDeserialize(bytes)
        }
        assertEquals("Incorrect length value", error.message)
        val deserializer = com.novi.bincode.BincodeDeserializer(bytes)
        assertEquals("Incorrect length value",
            assertThrows(DeserializationError::class.java) { deserializer.deserialize_len() }.message)
        val before = deserializer.get_buffer_offset()
        assertEquals("the length prefix was consumed once", 8, before)
        assertEquals("Input is not large enough",
            assertThrows(DeserializationError::class.java) { deserializer.deserialize_str() }.message)
        assertEquals("the next one-byte length is consumed before its bounds failure", 9, deserializer.get_buffer_offset())
    }
}
