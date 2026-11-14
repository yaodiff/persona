package persona.core.domain.tasks

import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.domain.lines.*

class AssistantConversationTest {
    private fun user(n: Int, text: String, source: MessageSource? = null, injected: Injected? = null, queued: Boolean? = null) =
        Item("u$n", n, ts = "2026-11-10T10:00:${n.toString().padStart(2, '0')}Z", role = "user", kind = "text",
            text = text, source = source, injected = injected, queued = queued)
    private fun reply(n: Int, text: String) = Item("a$n", n, role = "assistant", kind = "text", text = text)
    private fun project(items: List<Item>) = AssistantConversation.project(chatLines(items.map(::LineItem)).lines, items)

    @Test fun backgroundOwnershipSurvivesRecapsButEndsAtGenuineQueuedInput() {
        val items = listOf(user(1, "A real question"), reply(2, "A real answer"),
            user(3, "A background check", MessageSource("supervision")), reply(4, "Nothing to decide"),
            Item("compact", 5, role = "system", kind = "recap", text = "Compacted"), reply(6, "Still background"),
            user(7, "A genuine queued request", queued = true), reply(8, "The requested final answer"))
        val result = project(items)
        val visible = result.foreground.filterIsInstance<ChatLine.Row>().map { it.id }
        assertEquals(listOf("u1", "a2", "u7", "a8"), visible)
        assertTrue(result.background.any { it.id == "a4" })
        assertTrue(result.background.any { it.id == "a6" })
        val backgroundInput = result.background.filterIsInstance<ChatLine.Row>().single { it.id == "u3" }.row.item!!.item
        assertSame(items[2], backgroundInput)
        assertEquals("supervision", backgroundInput.source?.kind)
    }

    @Test fun prefixesAndFutureExternalSourcesDoNotClassifyHumanMessages() {
        val quoted = user(1, "⏰ Routine (scheduled task)\nPlease quote this example.")
        val unknown = user(3, "External context", MessageSource("future-source"))
        val authored = user(5, """<persona-context kind="message-source">{"author":"user","kind":"patrol"}</persona-context>Quoted routine""")
        val client = user(7, "A user quotation", injected = null).copy(clientMsgId = "client")
        val malformed = user(9, """<persona-context kind="message-source">{"author":true,"kind":"patrol"}</persona-context>Malformed metadata""")
        val items = listOf(quoted, reply(2, "Answer one"), unknown, reply(4, "Answer two"), authored,
            reply(6, "Answer three"), client, reply(8, "Answer four"), malformed, reply(10, "Answer five"))
        assertEquals(items.map { it.id }, project(items).foreground.map { it.id })
        assertTrue(project(items).background.isEmpty())
    }

    @Test fun scheduledMetadataIsQuietWhileWakeContinuesTheGenuineExchange() {
        val items = listOf(user(1, "A question"), reply(2, "An answer"),
            user(3, "Scheduled model input", injected = Injected("followup", "interval")), reply(4, "Scheduled result"),
            user(5, "A second question"), user(6, "Automatic wake", injected = Injected("followup", "wake")), reply(7, "Final answer"))
        val visible = project(items).foreground.map { it.id }
        assertFalse("u3" in visible)
        assertFalse("a4" in visible)
        assertTrue("u6" in visible)
        assertTrue("a7" in visible)
    }

    @Test fun aSourceOnlyUsageContinuationStaysWithItsGenuineUserAnswer() {
        val items = listOf(user(1, "A genuine question"), reply(2, "Initial answer"),
            user(3, "Continue after usage reset", source = MessageSource("followup")), reply(4, "The final user-facing answer"),
            user(5, "A scheduled one-off", source = MessageSource("followup", "main"), injected = Injected("followup", "once")),
            reply(6, "The scheduled background result"))
        val visible = project(items).foreground.map { it.id }
        assertTrue("u3" in visible)
        assertTrue("a4" in visible)
        assertFalse("u5" in visible)
        assertFalse("a6" in visible)
    }

    @Test fun systemHintsLeaveActualAnswersAndToolsInspectable() {
        val items = listOf(user(1, "Please check"), Item("tool", 2, role = "assistant", kind = "tool_use", toolUseId = "tool", toolName = "Read"),
            Item("result", 3, kind = "tool_result", toolUseId = "tool", text = "Source evidence"),
            Item("hint", 4, role = "system", kind = "notice", text = "Node restarted"), reply(5, "Final answer"),
            user(6, "Internal restart notification", injected = Injected("system", "lost_tasks")), reply(7, "Another final answer"),
            Item("plain-system", 8, role = "system", kind = "text", text = "Context metadata changed"), reply(9, "Final result"))
        val projection = project(items)
        assertTrue(projection.foreground.none { it.id == "hint" || it.id == "u6" || it.id == "plain-system" })
        assertTrue(projection.foreground.any { it.id == "a9" })
        assertTrue(projection.foreground.any { it.id == "a5" })
        assertFalse("Only the latest segment is an answer row", projection.foreground.filterIsInstance<ChatLine.Row>().any { it.id == "a7" })
        assertTrue("Earlier narration remains manually inspectable", projection.foreground.filterIsInstance<ChatLine.Work>()
            .flatMap { it.steps }.any { it is WorkItem.Narration && it.item.id == "a7" })
        val work = projection.foreground.filterIsInstance<ChatLine.Work>().single { it.tools > 0 }
        assertEquals(1, work.tools)
        assertFalse(work.steps.any { it is WorkItem.Thought && it.item.id == "hint" })
        assertTrue(projection.background.isNotEmpty())
    }

    @Test fun stableTaskIdentityMergesPlanExecutionReceiptAndLiveReopen() {
        val plan = TaskPlan("p", "main", listOf(PlanTask("First", todoId = "t"), PlanTask("Second", todoId = "s")),
            "started", "2026-11-10T10:00:00Z")
        val todo = Todo("t", "First", origin = "main", planId = "p", quiet = true)
        val feed = listOf(FeedItem("p-event", "main", plan.createdAt, "plan", "p"),
            FeedItem("t-event", "main", "2026-11-10T10:01:00Z", "task", "t"),
            FeedItem("receipt", "main", "2026-11-10T10:02:00Z", "receipt", "t", buildJsonObject { put("summary", "Checks passed") }),
            FeedItem("report", "main", "2026-11-10T10:03:00Z", "report", "t", buildJsonObject { put("text", "Evidence retained") }))
        val initial = AssistantTaskCards.merge(feed, mapOf("p" to plan), listOf(todo))
        assertEquals(listOf("plan:p:0", "plan:p:1"), initial.map { it.identity.id })
        assertEquals("receipt", initial.first().receipt?.id)
        assertEquals("report", initial.first().report?.id)
        val accepted = todo.copy(doneAt = "2026-11-10T10:04:00Z", supervision = Supervision(state = "accepted"))
        val updated = AssistantTaskCards.merge(feed, mapOf("p" to plan), listOf(accepted))
        assertEquals(initial.first().identity.id, updated.first().identity.id)
        assertEquals(initial.first().identity.at, updated.first().identity.at)
        assertEquals("accepted", updated.first().todo?.supervision?.state)
        val reopened = AssistantTaskCards.merge(feed, mapOf("p" to plan), listOf(todo))
        assertNull(reopened.first().todo?.doneAt)
        val visible = AssistantTaskCards.lines(emptyList(), feed, mapOf("p" to plan), listOf(accepted))
        assertEquals(1, visible.filterIsInstance<ChatLine.Extra>().count { it.payload is DispatchHint })
        assertTrue(visible.none { (it as? ChatLine.Extra)?.payload is AssistantExtra.Feed })
        assertEquals(2, (visible.filterIsInstance<ChatLine.Extra>().single { it.payload is DispatchHint }.payload as DispatchHint).count)
    }

    @Test fun theDispatchedLineCarriesEachTaskToOpenInPlanOrder() {
        // #3700: the line opens the dispatched tasks themselves, so it carries their to-dos and titles; a task that did not
        // start has no to-do, and a to-do's current title wins over the plan's
        val plan = TaskPlan("p", "main", listOf(PlanTask("First", todoId = "t"), PlanTask("Second", todoId = "s"),
            PlanTask("Failed", error = "machine offline")), "started", "2026-11-10T10:00:00Z")
        val todos = listOf(Todo("t", "First, renamed", origin = "main", planId = "p"), Todo("s", "Second", origin = "main", planId = "p"))
        val feed = listOf(FeedItem("p-event", "main", plan.createdAt, "plan", "p"))
        val hint = AssistantTaskCards.lines(emptyList(), feed, mapOf("p" to plan), todos)
            .filterIsInstance<ChatLine.Extra>().single { it.payload is DispatchHint }.payload as DispatchHint
        assertEquals(listOf(
            DispatchedTask("t", "First, renamed", key = "plan:p:0"),
            DispatchedTask("s", "Second", key = "plan:p:1"),
            DispatchedTask(null, "Failed", error = "machine offline", key = "plan:p:2"),
        ), hint.tasks)
        val one = TaskPlan("q", "main", listOf(PlanTask("Only", todoId = "o")), "started", "2026-11-10T11:00:00Z")
        val single = AssistantTaskCards.lines(emptyList(), listOf(FeedItem("q-event", "main", one.createdAt, "plan", "q")), mapOf("q" to one), emptyList())
            .filterIsInstance<ChatLine.Extra>().single { it.payload is DispatchHint }.payload as DispatchHint
        assertEquals(listOf(DispatchedTask("o", "Only", key = "plan:q:0")), single.tasks)
    }

    @Test fun aPlanTaskIsNewEvenWhenItsTaskRecordSaysOtherwise() {
        val plan = TaskPlan("p", "main", listOf(PlanTask("First", todoId = "t")), "started", "2026-11-10T10:00:00Z")
        val active = FeedItem("t-event", "main", "2026-11-10T10:01:00Z", "task", "t", buildJsonObject { put("wake", "active") })
        val planned = AssistantTaskCards.lines(emptyList(), listOf(FeedItem("p-event", "main", plan.createdAt, "plan", "p"), active),
            mapOf("p" to plan), listOf(Todo("t", "First", origin = "main", planId = "p")))
            .filterIsInstance<ChatLine.Extra>().single { it.payload is DispatchHint }.payload as DispatchHint
        assertNull(planned.tasks.single().wake)
        val resumed = FeedItem("z-event", "main", "2026-11-10T10:02:00Z", "task", "z", buildJsonObject { put("wake", "resumed") })
        val standalone = AssistantTaskCards.lines(emptyList(), listOf(resumed), emptyMap(), listOf(Todo("z", "Standalone", origin = "main")))
            .filterIsInstance<ChatLine.Extra>().single { it.payload is DispatchHint }.payload as DispatchHint
        assertEquals("resumed", standalone.tasks.single().wake)
    }

    @Test fun aReusedTodoChoosesItsCurrentPlanOverBothHeldCacheSlots() {
        val old = TaskPlan("old", "main", listOf(PlanTask("Previous run", todoId = "t")), "started", "2026-11-09T10:00:00Z")
        val current = TaskPlan("new", "main", listOf(PlanTask("Current run", todoId = "t")), "started", "2026-11-10T10:00:00Z")
        val todo = Todo("t", "Current run", origin = "main", planId = "new", createdAt = old.createdAt)
        val feed = listOf(FeedItem("old-plan", "main", old.createdAt, "plan", "old"),
            FeedItem("old-receipt", "main", "2026-11-09T11:00:00Z", "receipt", "t"),
            FeedItem("new-plan", "main", current.createdAt, "plan", "new"))
        val cached = listOf(AssistantTaskCard("plan:old:0", "t", "old", 0, old.createdAt,
            updatedAt = "2026-11-10T11:00:00Z", state = "accepted"),
            AssistantTaskCard("plan:new:0", "t", "new", 0, current.createdAt, state = "working"))
        val plans = mapOf("old" to old, "new" to current)
        val merged = AssistantTaskCards.merge(feed, plans, listOf(todo), cached)
        assertEquals(listOf("plan:new:0"), merged.map { it.identity.id })
        assertNull("Previous acceptance is audit evidence, not the new run's receipt", merged.single().receipt)
        assertEquals(listOf("old-receipt"), merged.single().records.map { it.id })
        // Incremental upserts do not delete the old slot; an absent new slot still chooses the current plan.
        val staleOnly = AssistantTaskCards.merge(feed.take(2), plans, listOf(todo), cached.take(1))
        assertEquals(listOf("plan:new:0"), staleOnly.map { it.identity.id })
        val missingPlan = AssistantTaskCards.merge(feed.take(2), mapOf("old" to old), listOf(todo), cached.take(1))
        assertEquals(listOf("todo:t"), missingPlan.map { it.identity.id })
        assertEquals("new", missingPlan.single().identity.planId)
    }

    @Test fun sparseProjectionUsesTheCurrentPlansRealStartForReceiptAndReportEvidence() {
        val plan = TaskPlan("new", "main", listOf(PlanTask("Current run", todoId = "t")), "started", "2026-11-10T10:00:00Z")
        val todo = Todo("t", "Current run", origin = "main", planId = "new")
        val sparse = AssistantTaskCard("plan:new:0", "t", "new", 0, state = "working")
        val old = listOf(FeedItem("old-receipt", "main", "2026-11-09T11:00:00Z", "receipt", "t"),
            FeedItem("old-report", "main", "2026-11-09T11:00:00Z", "report", "t", buildJsonObject { put("pr", "https://example.com/pull/1") }))
        val card = AssistantTaskCards.merge(old, mapOf(plan.id to plan), listOf(todo), listOf(sparse)).single()
        assertNull(card.identity.at)
        assertNull(card.receipt)
        assertNull(card.report)
        assertEquals(old.map { it.id }, card.records.map { it.id })
    }

    @Test fun housekeepingDraftsAndAssistantInstructionsStayOnlyInTheAudit() {
        val notice = FeedItem("housekeeping", kind = "notice", body = buildJsonObject { put("text", "Schedules resumed") })
        val draft = FeedItem("draft", kind = "message", body = buildJsonObject { put("draft", "A reply awaiting Send") })
        val delegated = FeedItem("delegated", kind = "routed", body = buildJsonObject { put("from", "assistant"); put("text", "Continue the task") })
        val genuine = FeedItem("genuine", kind = "routed", body = buildJsonObject { put("text", "My own instruction") })
        val feed = listOf(notice, draft, delegated, genuine)
        val visible = AssistantTaskCards.lines(emptyList(), feed, emptyMap(), emptyList())
            .filterIsInstance<ChatLine.Extra>().mapNotNull { (it.payload as? AssistantExtra.Feed)?.item?.id }
        assertEquals(listOf("genuine"), visible)
        assertEquals(listOf("housekeeping", "draft", "delegated"), AssistantTaskCards.backgroundFeed(feed).map { it.id })
    }

    @Test fun pagedTaskProjectionResolvesWithoutTheOriginalPlanFeed() {
        val card = AssistantTaskCard("plan:p:3", "t", "p", 3, "2026-11-10T10:00:00Z", state = "accepted", receiptId = "r")
        val feed = listOf(FeedItem("r", "main", "2026-11-10T12:00:00Z", "receipt", "t"))
        val merged = AssistantTaskCards.merge(feed, emptyMap(), listOf(Todo("t", planId = "p")), listOf(card))
        assertEquals(1, merged.size)
        assertEquals("plan:p:3", merged.single().identity.id)
        assertEquals(card.at, merged.single().identity.at)
        assertEquals("r", merged.single().receipt?.id)
    }

    /** #2528: Persona's chats hide system notices, but not an upstream refusal. */
    @Test fun aRefusalNoticeIsShown() {
        val refusal = Item("n2", 2, role = "system", kind = "notice", text = "no",
            modelRefusal = persona.core.contracts.ModelRefusal("model_refusal_fallback", "a", "b"))
        val plain = Item("n3", 3, role = "system", kind = "notice", text = "hint")
        val ids = project(listOf(user(1, "go"), refusal, plain, reply(4, "API Error: 404 model: b is not available"))).foreground
            .mapNotNull { (it as? ChatLine.Row)?.row?.id }
        assertTrue("$ids", "n2" in ids)
        assertTrue("$ids", "n3" !in ids)
    }
}
