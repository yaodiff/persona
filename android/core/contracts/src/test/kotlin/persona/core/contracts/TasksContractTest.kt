package persona.core.contracts

import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.ScheduledTask
import persona.core.contracts.generated.TodoWaitKind
import persona.core.contracts.generated.TodoWaitPRBlocker

/** The unified entry's wire shapes (docs/protocol/protocol.md "Tasks and the chat feed") decode, and its events enter the stream. */
class TasksContractTest {
    @Test fun eventsDecode() {
        assertTrue(ServerEvent.decode("""{"type":"todos"}""") is TodosEvent)
        val feed = ServerEvent.decode("""{"type":"assistant_feed","conversation":"c1","item":{"id":"f1","conversation":"c1",
            "at":"2026-11-03T10:00:00Z","kind":"receipt","ref":"t1","body":{"title":"修登录崩溃","state":"done","pr":"https://github.com/o/r/pull/9"}}}""")
        val item = (feed as AssistantFeedEvent).item
        assertEquals("receipt", (item.kind).zhAny()); assertEquals("修登录崩溃", (item.field("title")).zhAny()); assertEquals("t1", (item.ref).zhAny())
        val plan = ServerEvent.decode("""{"type":"task_plan","plan":{"id":"p1","conversation":"c1","state":"open",
            "tasks":[{"title":"t","prompt":"p","machine":"box","cwd":"/code/app","readonly":true}]}}""")
        assertEquals("app", ((plan as TaskPlanEvent).plan.tasks.single().project).zhAny())
    }

    /** A to-do's stage kind and the end of its stage bar (#3380, #11590), and their absence on an older hub. */
    @Test fun todoStageKindAndDoneDecode() {
        val done = JsonCodec.decodeFromString(Todo.serializer(),
            """{"id":"t1","title":"t","done_at":"2026-11-20T03:00:00Z","stage":"develop","stage_kind":"code","stage_done":true}""")
        assertEquals("code", done.stageKind)
        assertTrue(done.stageDone)
        val general = JsonCodec.decodeFromString(Todo.serializer(), """{"id":"t2","title":"t","stage":"develop","stage_kind":"general"}""")
        assertEquals("general", general.stageKind)
        assertFalse(general.stageDone)
        val older = JsonCodec.decodeFromString(Todo.serializer(), """{"id":"t3","title":"t","stage":"develop"}""")
        assertEquals(null, older.stageKind)
        assertFalse(older.stageDone)
    }

    /** Agent presets on planned tasks (#2225), including their absence on an older hub. */
    @Test fun planTaskTiersDecodeAndAnOlderHubHasNone() {
        val event = ServerEvent.decode("""{"type":"task_plan","plan":{"id":"p1","conversation":"c1","state":"open",
            "tasks":[{"title":"Review","prompt":"p","machine":"box","cwd":"/code/app","launcher":"fast","tier":"review"}]}}""")
            as TaskPlanEvent
        // Assert the wire shape too: this is a runtime failure, not a compile-only reference to a future property.
        val encoded = Json(JsonCodec) { encodeDefaults = true }.encodeToJsonElement(TaskPlan.serializer(), event.plan).jsonObject
        val task = encoded["tasks"]!!.jsonArray.single().jsonObject
        assertEquals("review", task["tier"]?.jsonPrimitive?.content)
        assertEquals("fast", task["launcher"]?.jsonPrimitive?.content)

        val older = JsonCodec.decodeFromString(PlanTask.serializer(), """{"title":"Default","prompt":"p"}""")
        val olderEncoded = Json(JsonCodec) { encodeDefaults = true }.encodeToJsonElement(PlanTask.serializer(), older).jsonObject
        assertFalse("tier" in olderEncoded)
    }

    // The cards' snapshot (docs/assistant/assistant-views.md): every live card, answer and registered block, each read on its own.
    @Test fun theCardsSnapshotDecodes() {
        val res = JsonCodec.decodeFromString<FeedResponse>("""{"items":[{"id":"f","conversation":"c","kind":"view","ref":"view_1"}],
            "views":{"view_1":{"id":"view_1","version":2,"view":{"fallback":"f","sections":[]}},"view_2":"not a card"},
            "view_actions":{"i/0":{"key":"i/0","action":"a","client_msg_id":"m","status":"sent","revision":3,"at":"t"}},
            "view_blocks":["i/0","i/1"]}""")
        assertEquals("view", res.items.single().kind)
        assertEquals(setOf("view_1", "view_2"), res.views.keys)
        assertEquals(setOf("i/0"), res.viewActions.keys)
        assertEquals(listOf("i/0", "i/1"), res.viewBlocks)
        // An older hub has none of them.
        assertTrue(JsonCodec.decodeFromString<FeedResponse>("""{"items":[]}""").views.isEmpty())
        assertTrue(ServerEvent.decode("""{"type":"assistant_view_block","conversation":"","key":"k"}""") == null)
        assertTrue(ServerEvent.decode("""{"type":"assistant_view","conversation":"c","view":{"id":"v"}}""") is AssistantViewEvent)
    }

    // A supervised task's verdict (#5190): kept as "review", not folded into "unknown", so the conversation draws it.
    @Test fun aReviewKeepsItsKind() {
        val item = JsonCodec.decodeFromString<FeedItem>("""{"id":"f1","kind":"review","ref":"t1","body":{"verdict":"accepted"}}""")
        assertEquals("review", item.kind)
        assertEquals("accepted", item.field("verdict"))
    }

    @Test fun scheduledTaskInboxValuesDecodeAndOnlySetValuesAreWritten() {
        for (mode in listOf("", "worthy", "every")) {
            val task = JsonCodec.decodeFromString<ScheduledTask>("""{"id":"t1","title":"Briefing","prompt":"Report","inbox":"$mode","push":"asked"}""")
            val edited = task.editable()
            assertEquals(mode, edited["inbox"]?.jsonPrimitive?.content)
            assertEquals("asked", edited["push"]?.jsonPrimitive?.content)
        }
        val olderHub = JsonCodec.decodeFromString<ScheduledTask>("""{"id":"t1","title":"Briefing","prompt":"Report"}""")
        assertFalse(olderHub.editable().containsKey("inbox"))
    }

    /** A scheduled briefing must reach the feed renderer instead of decoding as an unknown item (#4053). */
    @Test fun aBriefKeepsItsKind() {
        val item = JsonCodec.decodeFromString<FeedItem>("""{"id":"brief1","kind":"brief","ref":"routine1",
            "body":{"title":"Morning briefing","hours":12,"waiting":[],"done":[],"accept":[],"progress":[]}}""")
        assertEquals("brief", item.kind)
        assertEquals("Morning briefing", item.field("title"))
        assertEquals(12, item.number("hours"))
    }

    @Test fun briefRowsDecodeIndependently() {
        val item = JsonCodec.decodeFromString<FeedItem>("""{"id":"brief2","kind":"brief","body":{
            "title":"Evening briefing","hours":12.0,"waiting_unknown":true,
            "waiting":[{"id":"w1","title":"Answer?","waited_s":1.5,"machine_id":"box","session_id":"agent"},{"title":"missing id"},"bad"],
            "done":[{"id":"d1","title":"Done"}],"done_total":0,
            "accept":[{"id":"a1","outcome":"decide","recommend":"Canary"},{"id":"a2","outcome":"decide"},{"id":"a3","outcome":"done"}],
            "later":[{"id":"l1","title":"Later"}],"later_total":3,
            "progress":[{"id":"p1","stage":"future-stage","stage_kind":"code"}],"progress_total":2}}""")
        val brief = item.brief ?: error("brief body missing")
        assertEquals("Evening briefing", brief.title)
        assertEquals(12, brief.hours)
        assertTrue(brief.waitingUnknown)
        assertEquals(listOf("w1"), brief.waiting.map { it.id })
        assertEquals(2, brief.waiting.single().waitedS)
        assertEquals(SessionKey("box", "agent"), brief.waiting.single().chat(waiting = true, pending = emptyList()))
        assertEquals(1, brief.doneTotal)
        assertEquals(listOf(BriefTake.Recommendation, null, BriefTake.GotIt), brief.accept.map { it.take })
        assertEquals(2, brief.laterMore)
        assertEquals("future-stage", brief.progress.single().stage)
        assertEquals("code", brief.progress.single().stageKind)
        assertEquals(1, brief.progressMore)
    }

    /** Reports retain their original body and evidence metadata; presentation does not alter the wire record. */
    @Test fun aReportKeepsItsBodyAndEvidence() {
        val raw = """{"id":"report1","conversation":"main","kind":"report","ref":"task1","body":{
            "kind":"progress","text":"Verified the change.\nSee https://example.com/results and /tmp/example-check.log.",
            "evidence":["https://example.com/metadata-only","/tmp/example-evidence.png"]}}"""
        val item = JsonCodec.decodeFromString<FeedItem>(raw)
        assertEquals("report", item.kind)
        assertEquals("Verified the change.\nSee https://example.com/results and /tmp/example-check.log.", item.field("text"))
        assertEquals(JsonCodec.parseToJsonElement(raw).let { (it as kotlinx.serialization.json.JsonObject)["body"] }, item.body)
        val event = ServerEvent.decode("""{"type":"assistant_feed","conversation":"main","item":$raw}""") as AssistantFeedEvent
        assertEquals(item, event.item)
        assertEquals(item, JsonCodec.decodeFromString<FeedItem>(JsonCodec.encodeToString(FeedItem.serializer(), item)))
    }

    @Test fun todoKindDecodesAndAnOlderHubDefaultsToOrdinary() {
        assertEquals("concern", JsonCodec.decodeFromString<Todo>("""{"id":"c","kind":"concern"}""").kind)
        assertEquals("", JsonCodec.decodeFromString<Todo>("""{"id":"t"}""").kind)
        assertEquals("future-kind", JsonCodec.decodeFromString<Todo>("""{"id":"u","kind":"future-kind"}""").kind)
    }

    @Test fun todoCarriesTheServersTypedWaitAndAnOlderHubHasNone() {
        val todo = JsonCodec.decodeFromString<Todo>("""{"id":"t1","title":"Ship the release",
            "wait":{"kind":"pr_merge","waiting_on":"PR #88","reason":"Fix CSV escaping",
              "wake_condition":"Checks pass","wake_at":"2026-11-19T10:15:00Z",
              "until":"2026-11-19T11:30:00Z","since":"2026-11-18T09:45:00Z",
              "url":"https://github.com/example/app/pull/88",
              "pr_blockers":["review_required","checks_pending"]}}""")
        val wait = todo.wait ?: error("wait missing")
        assertEquals(TodoWaitKind.PrMerge, wait.kind)
        assertEquals("PR #88", wait.waitingOn)
        assertEquals("Checks pass", wait.wakeCondition)
        assertEquals(listOf(TodoWaitPRBlocker.ReviewRequired, TodoWaitPRBlocker.ChecksPending), wait.prBlockers)
        assertNull(JsonCodec.decodeFromString<Todo>("""{"id":"older","title":"Older hub"}""").wait)
    }

    @Test fun unknownKindsAndTodosDecode() {
        val res = JsonCodec.decodeFromString<FeedResponse>("""{"items":[{"id":"f","conversation":"c","at":"2026-11-03T10:00:00Z","kind":"new_kind"}],
            "plans":{"p":{"id":"p","state":"started","tasks":[]}}}""")
        assertEquals("unknown", (res.items.single().kind).zhAny())
        assertEquals("started", (res.plans.getValue("p").state).zhAny())
        val todos = JsonCodec.decodeFromString<TodosResponse>("""{"todos":[{"id":"t","title":"x","origin":"c","plan_id":"p",
            "agents":[{"machine_id":"box","session_id":"s1"},{"machine_id":"box","session_id":"s0","released_at":"2026-11-03T09:00:00Z"}]}]}""")
        assertEquals(listOf("s1"), (todos.todos.single().activeAgents.map { it.sessionId }).zhAny())
        // the completion check (#3180): kept as sent; a todo from an older hub has none
        val checked = JsonCodec.decodeFromString<Todo>("""{"id":"t","check":{"verdict":"waiting","reason":"Waiting for CI","at":"2026-11-10T08:00:00Z",
            "work_since":"2026-11-10T07:00:00Z","later":1}}""")
        assertEquals(TodoCheck("waiting", "Waiting for CI", "2026-11-10T08:00:00Z", "2026-11-10T07:00:00Z"), checked.check)
        assertEquals(null, JsonCodec.decodeFromString<Todo>("""{"id":"t"}""").check)
        val failed = JsonCodec.decodeFromString<Todo>("""{"id":"t","done_at":"2026-11-04T07:52:25Z","start_error":"claude did not become ready within 1m0s"}""")
        assertEquals("claude did not become ready within 1m0s", (failed.startError).zhAny())
        val ix = JsonCodec.decodeFromString<Interaction>("""{"id":"ix","machine_id":"box","session_id":"s1","kind":"permission","task_title":"修登录崩溃"}""")
        assertEquals("修登录崩溃", (ix.taskTitle).zhAny())
    }

    /** Cancellation and takeover history survive the wire, while an older hub's payload still decodes (#5732). */
    @Test fun cancellationAndLineageDecode() {
        val todo = JsonCodec.decodeFromString<Todo>("""{"id":"new","done_at":"2026-11-12T08:00:00Z",
            "cancelled_at":"2026-11-12T08:00:00Z","cancelled_reason":"Superseded by the revised plan",
            "lineage":[{"todo_id":"old","title":"First attempt","agent":"claude","machine":"box","session_id":"s1",
              "started":"2026-11-11T08:00:00Z","ended":"2026-11-12T07:00:00Z","why":"continues",
              "superseded_by":"new"},{"todo_id":"new","title":"Revised attempt","continues":"old","current":true}]}""")
        assertTrue(todo.isCancelled)
        assertEquals("Superseded by the revised plan", todo.cancellationReason)
        assertEquals(listOf("old", "new"), todo.lineage?.map { it.todoId })
        assertEquals("new", todo.lineage?.first()?.supersededBy)
        assertEquals("old", todo.lineage?.last()?.continues)
        assertEquals(true, todo.lineage?.last()?.current)

        val legacy = JsonCodec.decodeFromString<Todo>("""{"id":"old","done_at":"2026-11-12T08:00:00Z",
            "done_by":"review","done_reason":"superseded","supervision":{"state":"cancelled"}}""")
        assertTrue(legacy.isCancelled)
        assertEquals("superseded", legacy.cancellationReason)
        assertEquals(null, legacy.lineage)
        val oldHub = JsonCodec.decodeFromString<Todo>("""{"id":"plain","title":"Unchanged"}""")
        assertEquals(null, oldHub.cancelledReason)
        assertEquals(null, oldHub.lineage)
    }

    @Test fun cancelledReopenedAndExplicitlyCompletedIsNotCancelled() {
        for (doneBy in listOf("", "auto")) {
            val marker = if (doneBy.isEmpty()) "" else """"done_by":"$doneBy","""
            val completed = JsonCodec.decodeFromString<Todo>("""{"id":"reopened","done_at":"2026-11-13T08:00:00Z",
                $marker"reopened_at":"2026-11-13T07:00:00Z","supervision":{"state":"cancelled"}}""")
            assertFalse(completed.isCancelled)
            assertEquals(null, completed.cancellationReason)
        }
    }

    /** An answered record has every question with its answer in full (#1231); one from before it shows its line. */
    @Test fun answeredItemsInFullOrTheLine() {
        val res = JsonCodec.decodeFromString<FeedResponse>("""{"items":[
            {"id":"f1","conversation":"c","at":"2026-11-03T10:00:00Z","kind":"answered","ref":"t",
             "body":{"question":"先补哪一块？","answer":"重试；假服务器","items":[{"question":"先补哪一块？","answer":"重试\n和超时"},
               {"question":"请求怎么处理？","answer":"假服务器"},{"question":"","answer":""},"x"]}},
            {"id":"f2","conversation":"c","at":"2026-11-03T10:00:00Z","kind":"answered","body":{"question":"使用 Bash","answer":"批准了"}},
            {"id":"f3","conversation":"c","at":"2026-11-03T10:00:00Z","kind":"answered","body":{"items":[]}}]}""")
        assertEquals(listOf(AnsweredItem("先补哪一块？", "重试\n和超时"), AnsweredItem("请求怎么处理？", "假服务器")), res.items[0].answeredItems())
        assertEquals("重试；假服务器", (res.items[0].field("answer")).zhAny())
        assertEquals(listOf(AnsweredItem("使用 Bash", "批准了")), res.items[1].answeredItems())
        assertTrue(res.items[2].answeredItems().isEmpty())
    }

    private val ownerBriefWire = """{
        "created_at":"2026-11-03T12:01:00Z",
        "request":{"text":"Please fix the sign-in crash before Friday.\nAdd a landscape regression test.","truncated":false,
            "at":"2026-11-03T12:00:00Z","conversation":"main","message_id":"message-owner"},
        "started_by":"owner","goal":"Landscape sign-in works without crashing",
        "done_when":["The original fix is merged"],"expect_minutes":30,
        "criteria":["The current landscape regression passes"],"expect_minutes_now":150,
        "out_of_bounds":["Do not change payments"],"reconstructed":false
    }"""
    private val assistantBriefWire = """{
        "created_at":"2026-11-04T09:30:00Z","started_by":"assistant",
        "reason":"The earlier repair stopped","trigger_title":"Repair follow-up",
        "goal":"The worker completes its repair","done_when":["The worker regression passes"],
        "expect_minutes":45,"out_of_bounds":["Do not deploy the worker"]
    }"""
    private val reconstructedBriefWire = """{
        "created_at":"2026-11-05T08:00:00Z","started_by":"unknown","reconstructed":true,
        "handover":"Take over the existing branch and finish the repair.",
        "goal":"The older branch is finished","done_when":["The older branch passes its checks"],
        "expect_minutes":20,"out_of_bounds":["Do not rewrite history"]
    }"""

    /** Decode through the same event as the conversation; the task also carries its pre-existing edit fields. */
    private fun decodePlanWithBrief(brief: String? = null): TaskPlan {
        val briefField = brief?.let { ""","brief":$it""" } ?: ""
        val event = ServerEvent.decode("""{"type":"task_plan","plan":{"id":"brief-plan","conversation":"main","state":"open",
            "created_at":"2026-11-12T10:00:06Z","tasks":[{"title":"Repair sign-in","prompt":"Restore landscape sign-in",
            "machine":"box","cwd":"/srv/shop","launcher":"claude","model":"opus","permission":"acceptEdits","readonly":true,
            "why":"Keep the change scoped","tier":"review"$briefField}]}}""") as TaskPlanEvent
        return event.plan
    }

    /** #3822: the owner's verbatim request, its provenance, and both the original and currently effective scope survive. */
    @Test fun ownerPlanTaskBriefDecodesThroughServerEvent() {
        val brief = decodePlanWithBrief(ownerBriefWire).tasks.single().brief ?: error("owner brief missing")
        val request = brief.request ?: error("owner request missing")
        assertEquals("Please fix the sign-in crash before Friday.\nAdd a landscape regression test.", request.text)
        assertEquals(false, request.truncated)
        assertEquals("2026-11-03T12:00:00Z", request.at)
        assertEquals("main", request.conversation)
        assertEquals("message-owner", request.messageId)
        assertEquals("2026-11-03T12:01:00Z", brief.createdAt)
        assertEquals("owner", brief.startedBy)
        assertEquals("Landscape sign-in works without crashing", brief.goal)
        assertEquals(listOf("The original fix is merged"), brief.doneWhen)
        assertEquals(listOf("The current landscape regression passes"), brief.criteria)
        assertEquals(30, brief.expectMinutes)
        assertEquals(150, brief.expectNow)
        assertEquals(listOf("Do not change payments"), brief.outOfBounds)
        assertEquals(false, brief.reconstructed)
        assertEquals(null, brief.handover)
    }

    /** A brief started by the assistant keeps who/why; missing effective fields remain absent so the card can use the originals. */
    @Test fun assistantPlanTaskBriefDecodesWithReasonAndOriginalScope() {
        val brief = decodePlanWithBrief(assistantBriefWire).tasks.single().brief ?: error("assistant brief missing")
        assertEquals(null, brief.request)
        assertEquals("2026-11-04T09:30:00Z", brief.createdAt)
        assertEquals("assistant", brief.startedBy)
        assertEquals("The earlier repair stopped", brief.reason)
        assertEquals("Repair follow-up", brief.triggerTitle)
        assertEquals("The worker completes its repair", brief.goal)
        assertEquals(listOf("The worker regression passes"), brief.doneWhen)
        assertEquals(null, brief.criteria)
        assertEquals(45, brief.expectMinutes)
        assertEquals(null, brief.expectNow)
        assertEquals(listOf("Do not deploy the worker"), brief.outOfBounds)
        assertEquals(null, brief.reconstructed)
        assertEquals(null, brief.handover)
    }

    @Test fun reconstructedPlanTaskBriefDecodesItsHandover() {
        val brief = decodePlanWithBrief(reconstructedBriefWire).tasks.single().brief ?: error("reconstructed brief missing")
        assertEquals(null, brief.request)
        assertEquals("unknown", brief.startedBy)
        assertEquals(true, brief.reconstructed)
        assertEquals("Take over the existing branch and finish the repair.", brief.handover)
        assertEquals("The older branch is finished", brief.goal)
        assertEquals(listOf("The older branch passes its checks"), brief.doneWhen)
        assertEquals(20, brief.expectMinutes)
        assertEquals(listOf("Do not rewrite history"), brief.outOfBounds)
    }

    @Test fun absentPlanTaskBriefStaysAbsentAndPlanFieldsDecodeUnchanged() {
        val plan = decodePlanWithBrief()
        assertEquals("brief-plan", plan.id)
        assertEquals("main", plan.conversation)
        assertEquals("open", plan.state)
        assertEquals("2026-11-12T10:00:06Z", plan.createdAt)
        val task = plan.tasks.single()
        assertEquals(null, task.brief)
        assertEquals("Repair sign-in", task.title)
        assertEquals("Restore landscape sign-in", task.prompt)
        assertEquals("box", task.machine)
        assertEquals("/srv/shop", task.cwd)
        assertEquals("claude", task.launcher)
        assertEquals("opus", task.model)
        assertEquals("acceptEdits", task.permission)
        assertEquals(true, task.readonly)
        assertEquals("Keep the change scoped", task.why)
        assertEquals("review", task.tier)
    }

    /** The brief is hub-owned: an edited Start writes the editable task fields, never the decoded brief back to the hub. */
    @Test fun editedStartDoesNotEncodeTheDecodedBrief() {
        val decoded = decodePlanWithBrief(ownerBriefWire).tasks.single()
        val encoded = Json(JsonCodec) { encodeDefaults = true }
            .encodeToJsonElement(PlanTask.serializer(), decoded.copy(title = "Edited sign-in repair")).jsonObject
        assertFalse("a decoded brief must not be sent by an edited Start", "brief" in encoded)
        assertEquals("Edited sign-in repair", encoded["title"]?.jsonPrimitive?.content)
        assertEquals("Restore landscape sign-in", encoded["prompt"]?.jsonPrimitive?.content)
        assertEquals("box", encoded["machine"]?.jsonPrimitive?.content)
        assertEquals("/srv/shop", encoded["cwd"]?.jsonPrimitive?.content)
        assertEquals("review", encoded["tier"]?.jsonPrimitive?.content)
    }
}
