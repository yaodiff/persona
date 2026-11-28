package persona.core.contracts

import java.io.File
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

/** `Item.botmux` and `fold.botmux` (server proto.Botmux, docs/protocol/protocol.md "BotMux messages") as the hub sends them. */
class BotmuxWireTest {
    private val codec = Json { ignoreUnknownKeys = true }

    /** Every decoded message and `botmux send` in spec/fixtures/botmux/cases.json reads back with what the node set. */
    @Test fun fixtureWantsDecode() {
        val root = File(System.getProperty("persona.repo.root"))
        val cases = Json.parseToJsonElement(File(root, "spec/fixtures/botmux/cases.json").readText()).jsonObject
        val wants = (cases.getValue("messages").jsonArray + cases.getValue("sends").jsonArray)
            .mapNotNull { (it.jsonObject["want"] as? JsonObject)?.let { want -> it.jsonObject.getValue("name").jsonPrimitive.content to want } }
        assertTrue("no BotMux cases", wants.size > 20)
        for ((name, want) in wants) {
            val b = codec.decodeFromJsonElement(Botmux.serializer(), want)
            fun flag(key: String) = want[key]?.jsonPrimitive?.boolean ?: false
            assertEquals(name, want.getValue("kind").jsonPrimitive.content, b.kind)
            assertEquals(name, want["text"]?.jsonPrimitive?.content.orEmpty(), b.text)
            assertEquals(name, want["sender"]?.jsonPrimitive?.content, b.sender)
            assertEquals(name, want["sender_type"]?.jsonPrimitive?.content, b.senderType)
            assertEquals(name, flag("quoted"), b.quoted)
            assertEquals(name, flag("topic_context"), b.topicContext)
            assertEquals(name, flag("thread"), b.thread)
            assertEquals(name, flag("final"), b.final)
            assertEquals(name, flag("voice"), b.voice)
            assertEquals(name, flag("truncated"), b.truncated)
            assertEquals(name, want["attachments"]?.jsonPrimitive?.int ?: 0, b.attachments)
            assertEquals(name, want["mentions"]?.jsonArray?.map { it.jsonPrimitive.content }.orEmpty(), b.mentions)
            assertEquals(name, b.kind == "reply", b.isReply)
        }
    }

    @Test fun itemAndFoldCarryBotmux() {
        val item = codec.decodeFromString(Item.serializer(), """{"id":"u1","role":"user","kind":"text","text":"<user_message>ship</user_message>",
            "botmux":{"kind":"message","text":"ship","sender":"Example User","sender_type":"user","thread":true,"attachments":2,
            "attachment_meta":[{"kind":"image","name":"a.png","mime_type":"image/png"}],"mentions":["Review Bot"],"future_field":1}}""")
        val b = requireNotNull(item.botmux)
        assertEquals(Botmux("message", "ship", "Example User", "user", thread = true, attachments = 2,
            attachmentMeta = listOf(BotmuxAttachment("image", "a.png", "image/png")), mentions = listOf("Review Bot")), b)
        assertEquals("<user_message>ship</user_message>", item.text)

        val fold = codec.decodeFromString(Item.serializer(), """{"id":"f1","kind":"fold","fold":{"items":4,"tools":2,
            "botmux":[{"kind":"reply","text":"one"},{"kind":"reply","text":"two","final":true}]}}""")
        assertEquals(listOf("one", "two"), fold.fold!!.botmux.map { it.text })
        assertTrue(fold.fold!!.botmux[1].final)
    }

    /** Older nodes send neither; a kind this app does not know reads as unknown, a value of the wrong type as its default. */
    @Test fun olderAndUnexpectedValues() {
        val plain = codec.decodeFromString(Item.serializer(), """{"id":"a","role":"assistant","kind":"text","text":"hi"}""")
        assertNull(plain.botmux)
        assertTrue(codec.decodeFromString(Item.serializer(), """{"id":"f","kind":"fold","fold":{"items":1}}""").fold!!.botmux.isEmpty())

        val odd = codec.decodeFromString(Botmux.serializer(), """{"kind":"forward","text":"x","attachments":"two","quoted":"yes"}""")
        assertEquals("unknown", odd.kind)
        assertFalse(odd.isReply)
        assertEquals(0, odd.attachments)
        assertFalse(odd.quoted)
        assertEquals("x", odd.text)
    }
}
