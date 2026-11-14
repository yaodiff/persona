package persona.feature.inbox

import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.longOrNull
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import androidx.test.ext.junit.runners.AndroidJUnit4
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxChainLink
import persona.core.contracts.InboxSuggestion
import persona.core.contracts.JsonCodec
import persona.core.contracts.inboxEventFields
import persona.core.domain.inbox.InboxItem
import persona.core.domain.inbox.buildInboxList
import persona.core.domain.inbox.InboxListSettings
import persona.core.navigation.ChatRoute
import persona.core.navigation.RouteSource
import java.time.Instant
import java.time.ZoneOffset
import java.util.Locale

/** The shared phase-1 fixture (every state, made-up data) as the list's records, the way the data layer reads them. */
internal fun phase1Records(): List<InboxRecord> {
    val text = requireNotNull(InboxEventsTest::class.java.classLoader?.getResource("inbox/phase1.json")).readText()
    return JsonCodec.parseToJsonElement(text).jsonObject.getValue("items").jsonArray.map { it.jsonObject.record() }
}

private fun JsonObject.str(key: String): String? = (get(key) as? JsonPrimitive)?.contentOrNull

private fun JsonObject.record(): InboxRecord {
    val event = inboxEventFields(this)
    val lark = get("lark") as? JsonObject
    val mail = get("mail") as? JsonObject
    return InboxRecord(
        InboxItem(str("id")!!, str("account").orEmpty(), str("kind").orEmpty(), str("title").orEmpty(), str("sender"),
            str("preview").orEmpty(), str("summary"), str("tier"), str("updated_at"), str("done_at"),
            status = event.status, urgent = event.urgent),
        sourceLabel = str("account_name").orEmpty(), feedId = lark?.str("feed_id"), startPosition = lark?.str("start_position")?.toLongOrNull(),
        count = str("count")?.toIntOrNull() ?: 0, source = str("source").orEmpty(),
        mail = mail?.let { m -> InboxMailRef(m.str("account_id")!!, uids = m["uids"]!!.jsonArray.mapNotNull { (it as JsonPrimitive).longOrNull }) },
        isGroup = lark?.str("is_group")?.toBoolean(), event = event,
    )
}

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxEventsTest {
    private val records = phase1Records().associateBy { it.item.id }
    private fun record(id: String) = records.getValue(id)

    @Test fun theListSectionsByStatusWithWorkingBetweenNeedsYouAndFyi() {
        val list = buildInboxList(records.values.map { it.item }, settings = InboxListSettings(expanded = setOf("action", "fyi", "noise", "done")))
        assertEquals(listOf("action", "working", "fyi", "noise", "done"), list.groups.map { it.id })
        assertEquals(listOf("lark:oc_demo1:100", "lark:oc_demo2:200", "mail:a1:contract:88", "lark:oc_demo6:600"),
            list.groups.first().rows.map { it.id })
        assertEquals(listOf("lark:oc_demo3:300", "mail:a1:ci:77"), list.groups[1].rows.map { it.id })
        // Working items were triaged Needs you but are the AI's now: the badge counts only what needs the user.
        assertEquals(4, list.actionCount)
    }

    @Test fun rowsSayTheSuggestionOrWhereTheProcessingStands() {
        assertEquals(EventMark.Suggests(R.string.inbox_suggestion_reply, "可以，我周四下班前把报价发你。"), eventMark(record("lark:oc_demo1:100")))
        assertEquals(EventMark.Preparing, eventMark(record("lark:oc_demo2:200")))
        assertEquals(EventMark.Working(R.string.inbox_handling_waiting), eventMark(record("lark:oc_demo3:300")))
        assertEquals(EventMark.Working(R.string.inbox_handling_agent), eventMark(record("mail:a1:ci:77")))
        assertEquals(EventMark.Suggests(R.string.inbox_suggestion_agent, "对比新旧两版合同，列出改动的条款和风险"), eventMark(record("mail:a1:contract:88")))
        assertEquals(EventMark.Failed("处理超时了"), eventMark(record("lark:oc_demo6:600")))
        assertEquals(EventMark.Suggests(R.string.inbox_suggestion_conclusion, "周报里你负责的两项都按计划，不用回复。"), eventMark(record("lark:oc_demo7:700")))
        assertNull(eventMark(record("mail:a1:news:99")))
        assertNull(eventMark(record("lark:oc_demo9:900")))
        // Not urgent and still looking: only the section says so (Needs you without a mark).
        val looking = record("lark:oc_demo2:200").let { it.copy(item = it.item.copy(urgent = false)) }
        assertNull(eventMark(looking))
        assertEquals("第一行", firstLine("\n  第一行  \n第二行"))
    }

    @Test fun eachKindHasItsButtons() {
        fun s(kind: String, handback: Boolean = false) = InboxSuggestion(1, kind, "示例", handback = handback)
        assertEquals(SuggestionButtons(R.string.inbox_suggestion_send, true), suggestionButtons(s("reply")))
        assertEquals(SuggestionButtons(R.string.inbox_suggestion_start, true), suggestionButtons(s("agent")))
        assertEquals(SuggestionButtons(R.string.inbox_suggestion_ack, false), suggestionButtons(s("conclusion")))
        assertEquals(SuggestionButtons(R.string.inbox_suggestion_confirm, true), suggestionButtons(s("reply", handback = true)))
        assertEquals(SuggestionButtons(R.string.inbox_suggestion_confirm, true), suggestionButtons(s("unknown")))
        assertEquals(R.string.inbox_suggestion_handback, suggestionKindLabel(s("agent", handback = true)))
        // Edit sends the text only when it was changed.
        assertNull(confirmText(s("reply"), " 示例 "))
        assertNull(confirmText(s("reply"), "  "))
        assertEquals("改过的", confirmText(s("reply"), "改过的 "))
    }

    @Test fun eventAndNonEventChatRowsOpenTheCollapsedItemPage() {
        val pages = InboxItemPages()
        assertEquals(InboxEventRoute("lark:oc_demo1:100"), inboxRowPage(record("lark:oc_demo1:100"), pages))
        val old = record("lark:oc_demo1:100").copy(event = persona.core.contracts.InboxEventFields.None)
        assertEquals(InboxEventRoute("lark:oc_demo1:100"), inboxRowPage(old, pages))
        assertEquals(true, isInboxItemSelected(InboxEventRoute("x"), "x", null))
    }

    @Test fun chainEntriesGoWhereTheirLinksSay() {
        val chat = record("lark:oc_demo3:300")
        val byId = chat.event.chain.associateBy { it.id }
        assertEquals(ChainTarget.Page(LarkRoute("oc_demo3", "lark:oc_demo3:300", 300)), chainTarget(byId.getValue("c1"), chat))
        assertEquals(ChainTarget.Inline, chainTarget(byId.getValue("c2"), chat))
        assertEquals(ChainTarget.Page(ChatRoute("m1", "s-demo3", source = RouteSource.InboxItem("lark:oc_demo3:300"))), chainTarget(byId.getValue("c3"), chat))
        assertEquals(ChainTarget.Inline, chainTarget(byId.getValue("c4"), chat))
        assertEquals(ChainTarget.Page(LarkRoute("oc_demo3", "lark:oc_demo3:300", 305)), chainTarget(byId.getValue("c5"), chat))
        val mail = record("mail:a1:ci:77")
        assertEquals(ChainTarget.Page(MailRoute("mail:a1:ci:77")), chainTarget(mail.event.chain.first(), mail))
        assertEquals(ChainTarget.Page(ChatRoute("m1", "s-demo4", source = RouteSource.InboxItem("mail:a1:ci:77"))), chainTarget(mail.event.chain.last(), mail))
        fun entry(link: InboxChainLink?, detail: String? = null) = InboxChainEntry("x", kind = "sent", link = link, detail = detail)
        assertEquals(ChainTarget.Url("https://example.com/pr/1"), chainTarget(entry(InboxChainLink("source", url = "https://example.com/pr/1")), chat))
        assertEquals(ChainTarget.Source, chainTarget(entry(InboxChainLink("source")), chat))
        assertEquals(ChainTarget.Item("mail:a1:review:55"), chainTarget(entry(InboxChainLink("item", id = "mail:a1:review:55")), chat))
        // A link this app does not know, or a session without its ids, opens what it can in place.
        assertEquals(ChainTarget.Inline, chainTarget(entry(InboxChainLink("later"), detail = "依据"), chat))
        assertEquals(ChainTarget.None, chainTarget(entry(InboxChainLink("session", machineId = "m1")), chat))
        // A position on an item with no chat (a connector's) opens its source page.
        assertEquals(ChainTarget.Source, chainTarget(entry(InboxChainLink("source", position = JsonPrimitive("3"))), mail))
        assertEquals(persona.core.designsystem.icons.PhIcons.Info, chainIcon("from_the_future", chat))
    }

    @Test fun theChainFoldsToItsThreeNewestEntries() {
        val chain = record("lark:oc_demo3:300").event.chain
        assertEquals(listOf("c4", "c5", "c6"), shownChain(chain, expanded = false).map { it.id })
        assertEquals(chain, shownChain(chain, expanded = true))
        assertEquals(chain.take(2), shownChain(chain.take(2), expanded = false))
    }

    @Test fun chainTimesUseThe24HourClockInEveryLocale() {
        val now = Instant.parse("2026-11-05T12:00:00Z")
        val at = "2026-11-05T14:05:00Z"
        assertEquals("14:05", chainTime(at, Locale.US, now, ZoneOffset.UTC))
        assertEquals("14:05", chainTime(at, Locale.SIMPLIFIED_CHINESE, now, ZoneOffset.UTC))
        assertEquals("14:05", chainTime(at, Locale.JAPAN, now, ZoneOffset.UTC))
    }

    @Test fun chainTimesShowTheLocalizedShortDateBeforeToday() {
        val now = Instant.parse("2026-11-05T12:00:00Z")
        val at = "2026-11-04T14:05:00Z"
        assertEquals("Nov 4, 14:05", chainTime(at, Locale.US, now, ZoneOffset.UTC))
        assertEquals("11月4日 14:05", chainTime(at, Locale.SIMPLIFIED_CHINESE, now, ZoneOffset.UTC))
        assertEquals("11月4日 14:05", chainTime(at, Locale.JAPAN, now, ZoneOffset.UTC))
    }

    @Test fun chainTimesAreEmptyForMissingOrMalformedValues() {
        val now = Instant.parse("2026-11-05T12:00:00Z")
        assertEquals("", chainTime(null, Locale.US, now, ZoneOffset.UTC))
        assertEquals("", chainTime("not-a-time", Locale.US, now, ZoneOffset.UTC))
    }

    @Test fun chainTimeUsesTheInjectedZoneAndNowToChooseToday() {
        val now = Instant.parse("2026-11-05T00:30:00Z")
        val at = "2026-11-04T23:45:00Z"
        assertEquals("Nov 4, 23:45", chainTime(at, Locale.US, now, ZoneOffset.UTC))
        assertEquals("01:45", chainTime(at, Locale.US, now, ZoneOffset.ofHours(2)))
    }
}
