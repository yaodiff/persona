package persona.core.contracts

import java.io.ByteArrayInputStream
import kotlinx.serialization.ExperimentalSerializationApi
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.decodeFromStream
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

@OptIn(ExperimentalSerializationApi::class)
class WireSerializerStreamingTest {
    @Test
    fun streamingKnownFieldsPreservesD5OptionalFieldRecoveryAndSkipsUnknownSubtrees() {
        val raw = """
            {
              "todos": [
                {
                  "id": "t1",
                  "title": "Valid title",
                  "created_at": "not-a-timestamp",
                  "agents": "malformed-not-an-array",
                  "brief": {"log": [{"nested": [1, 2, 3]}]},
                  "view": {"phase": "working"},
                  "notes": "Sibling kept"
                }
              ],
              "has_more": false
            }
        """.trimIndent()
        val decoded = JsonCodec.decodeFromStream(TodosPageResponse.serializer(), ByteArrayInputStream(raw.toByteArray()))
        val todo = decoded.todos.single()
        assertEquals("t1", todo.id)
        assertEquals("Valid title", todo.title)
        assertEquals("Sibling kept", todo.notes)
        assertNull("Invalid @WireTime becomes null without dropping siblings", todo.createdAt)
        assertTrue("Malformed optional list falls back to default without dropping siblings", todo.agents.isEmpty())
    }

    @Test
    fun missingRequiredFieldStillFailsUnderStreamingWireSerializer() {
        val raw = """{"todos":[{"title":"Missing id"}],"has_more":false}"""
        try {
            val _ = JsonCodec.decodeFromStream(TodosPageResponse.serializer(), ByteArrayInputStream(raw.toByteArray()))
            fail("Missing required id was accepted")
        } catch (_: SerializationException) {
            // Expected
        }
    }
}
