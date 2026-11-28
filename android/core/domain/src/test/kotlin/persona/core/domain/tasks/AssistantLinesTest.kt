package persona.core.domain.tasks

import persona.core.domain.zhAny

import persona.core.domain.tasks.DayLabel
import persona.core.contracts.CoreText
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneOffset
import java.time.temporal.ChronoUnit
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.AgentState
import persona.core.contracts.FeedItem
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.contracts.Todo
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.ChatRow
import persona.core.domain.lines.LineItem
import persona.core.domain.lines.ToolCall
import persona.core.domain.lines.WorkItem

/** The assistant's feed among the chat's lines (docs/assistant/unified-entry.md), as iOS ChatLines.mergeFeed and web displayLines. */
class AssistantLinesTest {
    private val now = Instant.now().truncatedTo(ChronoUnit.SECONDS)
    private fun ago(min: Long, from: Instant = now) = from.minus(min, ChronoUnit.MINUTES).toString()
    private fun user(id: String, min: Long, from: Instant = now) = ChatLine.Row(ChatRow("user", LineItem(Item(id, role = "user", kind = "text", ts = ago(min, from)))))
    private fun reply(id: String, min: Long, from: Instant = now) = ChatLine.Row(ChatRow("reply", LineItem(Item(id, role = "assistant", kind = "text", ts = ago(min, from)))))
    private fun feed(id: String, min: Long, from: Instant = now) = FeedItem(id, "c1", ago(min, from), "task", "t")

    @Test fun anItemGoesAtTheEndOfItsExchangeWithADayLinePerDay() {
        // A fixed noon: counted back from the real clock, "30 and 20 minutes ago" straddle midnight between 00:00 and 00:30 UTC.
        val noon = Instant.parse("2026-11-06T12:00:00Z")
        val lines = listOf(user("u1", 60 * 26, noon), reply("a1", 60 * 26 - 1, noon), user("u2", 30, noon), reply("a2", 29, noon))
        val out = AssistantLines.lines(lines, listOf(feed("early", 60 * 26 - 1, noon), feed("mid", 20, noon)), ZoneOffset.UTC).map { it.id }
        assertEquals(2, out.count { it.startsWith("day-") })
        assertEquals(listOf("u1", "a1", "feed-early", "u2", "a2", "feed-mid"), (out.filterNot { it.startsWith("day-") }).zhAny())
    }

    @Test fun earliestHoldsBackOlderFeedItemsAndDayLinesUntilEarlierPagesLoad() {
        val day1 = Instant.parse("2026-11-04T12:00:00Z")
        val day2 = Instant.parse("2026-11-05T12:00:00Z")
        val day3 = Instant.parse("2026-11-06T12:00:00Z")
        val allFeed = listOf(
            FeedItem("untimed", "c1", at = null, kind = "notice"),
            FeedItem("days-ago", "c1", day1.toString(), "brief"),
            FeedItem("day2-card", "c1", day2.plusSeconds(60).toString(), "task", "t2"),
            FeedItem("before-page", "c1", "2026-11-06T16:00:00+08:00", "today"), // 08:00Z on day 3
            FeedItem("at-boundary", "c1", day3.toString(), "notice"),
            FeedItem("in-page", "c1", day3.plusSeconds(120).toString(), "task", "t3"),
        )
        val page3Lines = listOf(
            ChatLine.Row(ChatRow("user", LineItem(Item("u3", role = "user", kind = "text", ts = day3.toString())))),
            ChatLine.Row(ChatRow("reply", LineItem(Item("a3", role = "assistant", kind = "text", ts = day3.plusSeconds(30).toString())))),
            ChatLine.Row(ChatRow("user", LineItem(Item("u4", role = "user", kind = "text", ts = day3.plusSeconds(300).toString())))),
        )
        // Newest page only (hasOlder = true): older days and same-day morning card stay hidden; boundary, in-page and untimed stay.
        val partial = AssistantLines.lines(page3Lines, allFeed, ZoneOffset.UTC, earliest = day3).map { it.id }
        assertEquals(listOf("day-2026-11-06"), partial.filter { it.startsWith("day-") })
        assertEquals(
            listOf("feed-untimed", "u3", "a3", "feed-at-boundary", "feed-in-page", "u4"),
            partial.filterNot { it.startsWith("day-") },
        )

        // Loading an older page moves earliest back to day 2: day 2's card and day 3's morning card interleave chronologically.
        val page2Lines = listOf(
            ChatLine.Row(ChatRow("user", LineItem(Item("u2", role = "user", kind = "text", ts = day2.toString())))),
            ChatLine.Row(ChatRow("reply", LineItem(Item("a2", role = "assistant", kind = "text", ts = day2.plusSeconds(30).toString())))),
        ) + page3Lines
        val expanded = AssistantLines.lines(page2Lines, allFeed, ZoneOffset.UTC, earliest = day2).map { it.id }
        assertEquals(listOf("day-2026-11-05", "day-2026-11-06"), expanded.filter { it.startsWith("day-") })
        assertEquals(
            listOf("feed-untimed", "u2", "a2", "feed-day2-card", "feed-before-page", "u3", "a3", "feed-at-boundary", "feed-in-page", "u4"),
            expanded.filterNot { it.startsWith("day-") },
        )

        // Complete history (earliest = null / hasOlder = false): every day and feed item appears.
        val complete = AssistantLines.lines(page2Lines, allFeed, ZoneOffset.UTC, earliest = null).map { it.id }
        assertEquals(listOf("day-2026-11-04", "day-2026-11-05", "day-2026-11-06"), complete.filter { it.startsWith("day-") })
        assertTrue("feed-days-ago" in complete)

        // Empty partial page (earliest = Instant.MAX): all timed historical items wait; untimed item is not dropped.
        val emptyPartial = AssistantLines.lines(emptyList(), allFeed, ZoneOffset.UTC, earliest = Instant.MAX).map { it.id }
        assertEquals(listOf("feed-untimed"), emptyPartial)
    }

    @Test fun mergeKeepsOneCopyOldestFirst() {
        val a = feed("a", 5); val b = feed("b", 10)
        assertEquals(listOf("b", "a"), (AssistantLines.merge(listOf(a), listOf(b, a)).map { it.id }).zhAny())
    }

    private fun call(name: String, result: String?, failed: Boolean = false) = ToolCall(
        LineItem(Item("use-$name", kind = "tool_use", toolName = name, ts = ago(5))),
        result?.let { LineItem(Item("res-$name", kind = "tool_result", text = it, isError = failed)) }, emptyList())

    @Test fun quickResultsComeOnlyFromThePhonesWritesAndFollowTheirWork() {
        val ok = call("mcp__persona__phone_reminders_create", """{"id":"R1","title":"打电话","due":"2026-11-07T10:00:00+08:00","list":"提醒事项"}""")
        val r = QuickResult.of(ok)
        assertNotNull(r); assertEquals(true, r!!.reminder); assertEquals("打电话", (r.title).zhAny()); assertEquals("提醒事项", (r.place).zhAny())
        assertNull(QuickResult.of(call("mcp__persona__phone_reminders_list", """{"id":"x"}""")))
        assertNull(QuickResult.of(call("phone_calendar_create_event", "denied", failed = true)))
        assertNull(QuickResult.of(call("phone_reminders_create", null)))
        val work = ChatLine.Work("wt-1", listOf(WorkItem.Tool(ok)), live = false, start = now, end = now, open = false)
        val out = AssistantLines.lines(listOf(user("u1", 6), work, reply("a1", 4)), emptyList(), ZoneOffset.UTC).map { it.id }
        assertEquals(listOf("u1", "wt-1", "quick-use-mcp__persona__phone_reminders_create", "a1"), (out.filterNot { it.startsWith("day-") }).zhAny())
    }

    @Test fun quickResultKeepsAnAndroidCalendarEventsFields() {
        val call = call("phone_calendar_create_event", """{"id":"E1","title":"Dentist","start":"2026-11-13T15:30:00Z","end":"2026-11-13T16:00:00Z","calendar":"Personal","source_device_name":"Pixel 9","source_device_platform":"android"}""")
        val result = QuickResult.of(call)
        assertNotNull(result)
        assertEquals(false, result!!.reminder)
        assertEquals("use-phone_calendar_create_event", result.callId)
        assertEquals("Dentist", result.title)
        assertEquals("2026-11-13T15:30:00Z", result.time)
        assertEquals("Personal", result.place)
        assertEquals("Pixel 9", result.sourceDeviceName)
        assertEquals("android", result.sourceDevicePlatform)
        // The id is not a number, so it is not an event this phone's calendar can be asked to delete.
        assertNull(result.itemId)
    }

    @Test fun quickResultKeepsOtherKnownSourceIdentities() {
        val iphone = QuickResult.of(call("phone_reminders_create",
            """{"id":"R1","title":"Call","due":"2026-11-13T15:30:00Z","list":"Personal","source_device_name":"Work iPhone","source_device_platform":"ios"}"""))
        assertNotNull(iphone)
        assertEquals("Work iPhone", iphone!!.sourceDeviceName)
        assertEquals("ios", iphone.sourceDevicePlatform)
        assertEquals("Call", iphone.title)
        assertEquals("Personal", iphone.place)

        val harmony = QuickResult.of(call("phone_calendar_create_event",
            """{"id":"E2","title":"Review","source_device_name":"Mate 70","source_device_platform":"harmony"}"""))
        assertNotNull(harmony)
        assertEquals("Mate 70", harmony!!.sourceDeviceName)
        assertEquals("harmony", harmony.sourceDevicePlatform)
        assertEquals("Review", harmony.title)
    }

    @Test fun quickResultLeavesMissingOrBlankSourceIdentityUnknown() {
        for (raw in listOf(
            """{"id":"E1","title":"Dentist"}""",
            """{"id":"E1","title":"Dentist","source_device_name":"","source_device_platform":""}""",
            """{"id":"E1","title":"Dentist","source_device_name":"  ","source_device_platform":"\t"}""",
        )) {
            val result = QuickResult.of(call("phone_calendar_create_event", raw))
            assertNotNull(raw, result)
            assertNull(raw, result!!.sourceDeviceName)
            assertNull(raw, result.sourceDevicePlatform)
            assertEquals(raw, "Dentist", result.title)
        }
    }

    @Test fun quickResultKeepsTheCreatedItemsIdForUndo() {
        // The phone's calendar ids are numbers: only those name something Undo can take back (iOS QuickResult.itemID).
        assertEquals(42L, QuickResult.of(call("phone_calendar_create_event", """{"id":"42","title":"Dentist"}"""))!!.itemId)
        // A result with no id says nothing, so it draws no card at all, as on iOS.
        for (raw in listOf("""{"title":"Dentist"}""", """{"id":"","title":"Dentist"}""", """{"id":"  "}""",
            """{"id":null,"title":"Dentist"}""", """{"id":0,"title":"Dentist"}""")) {
            assertNull(raw, QuickResult.of(call("phone_calendar_create_event", raw)))
        }
        // An id that is not a positive number (an iPhone's event or reminder) still draws the card, without Undo.
        for (raw in listOf("""{"id":"E1","title":"Dentist"}""", """{"id":"0","title":"Dentist"}""",
            """{"id":"-3","title":"Dentist"}""", """{"id":"1.5","title":"Dentist"}""")) {
            val result = QuickResult.of(call("phone_calendar_create_event", raw))
            assertNotNull(raw, result)
            assertNull(raw, result!!.itemId)
        }
    }

    @Test fun aTasksStateFollowsItsAgents() {
        val todo = Todo("t", "t")
        assertEquals(TaskState.NotStarted, TaskState.of(todo, emptyList()))
        assertEquals(TaskState.NeedsYou, TaskState.of(todo, listOf(AgentState.Working, AgentState.NeedsYou)))
        assertEquals(TaskState.Unread, TaskState.of(todo, listOf(AgentState.Working, AgentState.Unread)))
        assertEquals(TaskState.Working, TaskState.of(todo, listOf(AgentState.Working, AgentState.Idle)))
        assertEquals(TaskState.Followup, TaskState.of(todo, listOf(AgentState.Idle)))
        assertEquals(TaskState.Done, TaskState.of(todo.copy(doneAt = ago(1)), listOf(AgentState.NeedsYou)))
        // Its agent did not start (#1077): failed, not done, until "Retry" reopens it without the error (#1396).
        val failed = todo.copy(doneAt = ago(1), startError = "claude did not become ready within 1m0s")
        assertEquals(TaskState.Failed, TaskState.of(failed, emptyList()))
        assertEquals("没开起来", (TaskState.Failed.label).zhAny())
        assertEquals(TaskState.NotStarted, TaskState.of(failed.copy(doneAt = null, startError = null), emptyList()))
    }

    @Test fun aTaskCutOffByTheModelServiceIsInterrupted() {
        // #1516: its agent's turn ended in an API error — not done; going on is in progress again.
        val todo = Todo("t", "t")
        assertEquals(TaskState.Interrupted, TaskState.of(todo, listOf(AgentState.Unread), interrupted = true))
        assertEquals(TaskState.NeedsYou, TaskState.of(todo, listOf(AgentState.NeedsYou, AgentState.Unread), interrupted = true))
        assertEquals(TaskState.Working, TaskState.of(todo, listOf(AgentState.Working)))
        assertEquals(TaskState.Unread, TaskState.of(todo, listOf(AgentState.Unread)))
        assertEquals("中断了", (TaskState.Interrupted.label).zhAny())
        val cut = "API Error: Connection lost mid-response. The response above may be incomplete."
        fun s(state: String, turnError: String?) = Session(machineId = "m", sessionId = "s", state = state, turnError = turnError)
        assertTrue(s("unread", cut).interrupted)
        assertTrue(s("idle", cut).interrupted)
        assertFalse(s("working", cut).interrupted)
        assertFalse(s("unread", null).interrupted)
        assertEquals(Interruption(CoreText.of(CoreText.Key.ApiConnection), true), Interruption.of(s("unread", cut)))
        assertEquals(Interruption(CoreText.of(CoreText.Key.ApiLogin), false), Interruption.of(s("unread", "API Error: 401 authentication_error")))
        assertNull(Interruption.of(s("working", cut)))
        // A turn with no output is the hub's own sentence, never a model service error (#10187).
        for (kind in listOf("no_output", "stopped")) {
            val quiet = Session(machineId = "m", sessionId = "s", state = "unread", turnError = "随对话一起停下了", turnErrorKind = kind)
            assertEquals(Interruption(CoreText.plain("随对话一起停下了"), true, plain = true), Interruption.of(quiet))
            val sealed = quiet.copy(turnError = "This conversation was compacted natively by <account>: the compaction is sealed (encrypted) to that account")
            assertEquals(Interruption(CoreText.of(CoreText.Key.ApiConversationInvalid), false, invalid = true), Interruption.of(sealed))
        }
        fun receipt(body: String) = FeedItem("r", kind = "receipt", body = JsonCodec.parseToJsonElement(body))
        assertEquals("interrupted", (receiptState(receipt("""{"state":"interrupted","summary":"模型服务出错，中断了"}"""))).zhAny())
        assertEquals("interrupted", (receiptState(receipt("""{"state":"done","summary":"$cut"}"""))).zhAny())
        assertEquals("done", (receiptState(receipt("""{"state":"done","summary":"修好了 API Error: Connection lost 时的崩溃"}"""))).zhAny())
        assertEquals("failed", (receiptState(receipt("""{"state":"failed","summary":"没开起来：x"}"""))).zhAny())
        // The task's name as it is now (#3036), else as the receipt was written.
        val named = receipt("""{"state":"done","title":"Example task"}""")
        assertEquals("Renamed task", receiptTitle(named, Todo("t", title = "Renamed task")))
        assertEquals("Example task", receiptTitle(named, null))
        assertEquals("Example task", receiptTitle(named, Todo("t", title = "  ")))
        assertNull(receiptTitle(receipt("""{"state":"done"}"""), null))
    }

    @Test fun dayLabels() {
        val today = LocalDate.of(2026, 11, 3)
        assertEquals(DayLabel(today, DayLabel.Kind.Today), AssistantLines.dayLabel(today, today))
        assertEquals(DayLabel(today.minusDays(1), DayLabel.Kind.Yesterday), AssistantLines.dayLabel(today.minusDays(1), today))
        assertEquals(DayLabel(LocalDate.of(2025, 11, 3), DayLabel.Kind.OtherYear), AssistantLines.dayLabel(LocalDate.of(2025, 11, 3), today))
    }

    @Test fun timeFastPathMatchesInstantParseAcrossValidAndInvalidInputs() {
        for (iso in listOf(
            "2026-11-23T12:34:56Z",
            "2026-11-23T12:34:56.789Z",
            "2026-11-23T12:34:56.123456789Z",
            "2026-11-23T12:34:56z",
            "2026-11-23T20:34:56+08:00",
        )) {
            assertEquals(iso, Instant.parse(iso), AssistantLines.time(iso))
        }
        for (invalid in listOf(null, "", "   ", "not-a-timestamp", "0001-01-01T00:00:00Z", "1999-12-31T23:59:59Z", "2026-13-40T99:00:00Z")) {
            assertNull(invalid.toString(), AssistantLines.time(invalid))
        }
    }

    @Test fun lineItemTimeReturnsNullForMalformedOrBlankTimestampWithoutThrowing() {
        val valid = persona.core.domain.lines.LineItem(
            persona.core.contracts.Item(id = "a", kind = "assistant", seq = 1, text = "ok", ts = "2026-11-25T10:00:00Z"),
        )
        val blank = persona.core.domain.lines.LineItem(
            persona.core.contracts.Item(id = "b", kind = "assistant", seq = 2, text = "blank", ts = ""),
        )
        val bad = persona.core.domain.lines.LineItem(
            persona.core.contracts.Item(id = "c", kind = "assistant", seq = 3, text = "bad", ts = "2026-13-40T99:00:00Z"),
        )
        val garbage = persona.core.domain.lines.LineItem(
            persona.core.contracts.Item(id = "d", kind = "assistant", seq = 4, text = "garbage", ts = "not-a-timestamp"),
        )
        org.junit.Assert.assertEquals(java.time.Instant.parse("2026-11-25T10:00:00Z"), valid.time)
        org.junit.Assert.assertNull(blank.time)
        org.junit.Assert.assertNull(bad.time)
        org.junit.Assert.assertNull(garbage.time)
    }
}
