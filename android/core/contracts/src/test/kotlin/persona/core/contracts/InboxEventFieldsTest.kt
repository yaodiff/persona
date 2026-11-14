package persona.core.contracts

import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Inbox phase 1's item fields, read from the shared fixture with every state (`inbox/phase1.json`) and from odd input. */
class InboxEventFieldsTest {
    private val items: Map<String, JsonObject> = JsonCodec.parseToJsonElement(
        requireNotNull(javaClass.classLoader?.getResource("inbox/phase1.json")).readText(),
    ).jsonObject.getValue("items").jsonArray.associate { it.jsonObject.getValue("id").toString().trim('"') to it.jsonObject }

    private fun fields(id: String) = inboxEventFields(items.getValue(id))

    @Test fun readsEveryStateOfTheFixture() {
        fields("lark:oc_demo1:100").let {
            assertEquals("action", it.status)
            assertEquals("done", it.handling?.state)
            assertEquals(2L, it.suggestion?.version)
            assertEquals("reply", it.suggestion?.kind)
            assertEquals("知识库里上次报价是 10 月 12 日", it.suggestion?.note)
            assertEquals(listOf("received", "triaged", "handling"), it.chain.map { e -> e.kind })
            assertEquals(3, it.chain.first().messages.size)
            assertEquals(100L, it.chain.first().link?.positionValue)
            assertNull(it.chain.first().via)
            assertEquals("s-demo1", it.chain.last().link?.sessionId)
            assertTrue(it.present)
        }
        fields("lark:oc_demo2:200").let {
            assertTrue(it.urgent)
            assertEquals("looking", it.handling?.state)
            assertNull(it.suggestion)
        }
        fields("lark:oc_demo3:300").let {
            assertEquals("working", it.status)
            assertEquals("waiting", it.handling?.state)
            assertTrue(it.handling!!.working)
            assertTrue(it.chain.single { e -> e.kind == "confirmed" }.edited)
            assertEquals(listOf("mail:a1:review:55"), it.related.map { r -> r.id })
        }
        assertEquals(77L, fields("mail:a1:ci:77").chain.first().link?.uid)
        assertEquals("agent", fields("mail:a1:contract:88").suggestion?.kind)
        fields("lark:oc_demo6:600").let {
            assertEquals("failed", it.handling?.state)
            assertEquals("处理超时了", it.handling?.error)
        }
        assertEquals("conclusion", fields("lark:oc_demo7:700").suggestion?.kind)
        assertEquals("noise", fields("mail:a1:news:99").status)
        fields("lark:oc_demo9:900").let {
            assertNull(it.status)
            assertEquals("self_replied", it.chain.last().kind)
            assertEquals("replied", it.doneBy)
            assertTrue(it.present)
        }
        // The wire model reads them too.
        val listed = JsonCodec.decodeFromJsonElement<InboxResponse>(JsonCodec.parseToJsonElement(
            requireNotNull(javaClass.classLoader?.getResource("inbox/phase1.json")).readText()))
        assertEquals(9, listed.items.size)
        assertEquals("working", listed.items[2].status)
        assertEquals(6, listed.items[2].chain.size)
    }

    @Test fun followUpFieldsAndChainViaAreReadWithoutOpeningAnEvent() {
        val item = JsonCodec.parseToJsonElement("""{
            "id":"x","where":"Launch group",
            "follow":{"who":"Alex to confirm","next":"Ask about Friday","check_at":"2026-01-02T09:00:00Z","checks":2},
            "now":"a_future_state",
            "chain":[{"id":"c","kind":"received","text":"New","via":"connector:reviews"}]
        }""").jsonObject
        val fields = inboxEventFields(item)
        assertEquals("Launch group", fields.where)
        assertEquals("Alex to confirm", fields.follow?.who)
        assertEquals("Ask about Friday", fields.follow?.next)
        assertEquals("2026-01-02T09:00:00Z", fields.follow?.checkAt)
        assertEquals("a_future_state", fields.now)
        assertEquals("connector:reviews", fields.chain.single().via)
        // Display metadata alone never turns an item without phase-1 fields into an event.
        assertFalse(inboxEventFields(JsonObject(item - "chain")).present)

        // The wire model exposes the same fields and keeps an arbitrary source id in via.
        val wire = JsonCodec.decodeFromJsonElement<InboxItem>(item)
        assertEquals("Launch group", wire.where)
        assertEquals(fields.follow, wire.follow)
        assertEquals("a_future_state", wire.now)
        assertEquals("connector:reviews", wire.chain.single().via)
    }

    @Test fun anOlderHubsItemHasNone() {
        val old = JsonCodec.parseToJsonElement("""{"id":"lark:x:1","tier":"action","agent":{"state":"drafted","draft":"好"}}""").jsonObject
        assertEquals(InboxEventFields.None, inboxEventFields(old))
        assertEquals("manager", inboxEventFields(JsonCodec.parseToJsonElement("""{"id":"x","done_by":"manager"}""").jsonObject).doneBy)
        assertFalse(inboxEventFields(old).present)
        val wire = JsonCodec.decodeFromJsonElement<InboxItem>(old)
        assertNull(wire.where)
        assertNull(wire.follow)
        assertNull(wire.now)
    }

    @Test fun oddValuesAreLeftOutAndUnknownKindsKept() {
        val odd = JsonCodec.parseToJsonElement("""{
            "id":"x","status":"later","urgent":"yes","recap":"  ",
            "where":"  ","follow":"later","now":"  ",
            "handling":{"state":"thinking"},
            "suggestion":{"version":"2","kind":"reply","text":"好"},
            "chain":[{"id":"a","kind":"from_the_future","text":"新动作","link":{"type":"source","position":12}},
                     {"id":"b","kind":"received","count":"many"}, 7],
            "related":[{"id":""},{"id":"y","title":"有关的"}]
        }""").jsonObject
        val fields = inboxEventFields(odd)
        assertNull(fields.status)
        assertFalse(fields.urgent)
        assertNull(fields.recap)
        assertNull(fields.where)
        assertNull(fields.follow)
        assertNull(fields.now)
        assertEquals("unknown", fields.handling?.state)
        // A version that is not a number is dropped: the suggestion confirms with 0 and the hub says it is stale.
        assertEquals(0L, fields.suggestion?.version)
        assertEquals(listOf("from_the_future", "received"), fields.chain.map { it.kind })
        assertEquals(12L, fields.chain.first().link?.positionValue)
        assertNull(fields.chain[1].count)
        assertEquals(listOf("y"), fields.related.map { it.id })
    }

    @Test fun malformedFollowUpMembersKeepTheirValidSiblings() {
        val fields = inboxEventFields(JsonCodec.parseToJsonElement("""{
            "id":"x","recap":"Keep this","where":7,
            "follow":{"who":7,"next":"Ask about Friday","check_at":"not a time"},
            "now":"waiting"
        }""").jsonObject)
        assertEquals("Keep this", fields.recap)
        assertNull(fields.where)
        assertNull(fields.follow?.who)
        assertEquals("Ask about Friday", fields.follow?.next)
        assertNull(fields.follow?.checkAt)
        assertEquals("waiting", fields.now)
    }
}
