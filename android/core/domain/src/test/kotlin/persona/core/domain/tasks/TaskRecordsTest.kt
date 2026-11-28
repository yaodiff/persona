package persona.core.domain.tasks

import java.time.Instant
import java.time.ZoneOffset
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.AssistantTaskCard
import persona.core.contracts.FeedItem
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.PlanTask
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.LineItem
import persona.core.domain.lines.chatLines

/**
 * #4339 C13: on Glass the conversation draws the hub's task records as iOS's `FeedLine` does, message cards included; only
 * plans and task cards keep their other places. Brief and Board keep every record behind the trays, as before.
 */
class TaskRecordsTest {
    private fun at(n: Int) = "2026-11-12T10:00:${n.toString().padStart(2, '0')}Z"
    private val plan = TaskPlan("plan", "main", listOf(PlanTask("Fix the login", todoId = "t")), "started", at(1))
    private val todos = listOf(Todo("t", "Fix the login", origin = "main", planId = "plan", createdAt = at(1)))
    private fun item(id: String, n: Int, kind: String, ref: String? = "t", vararg fields: Pair<String, String>) =
        FeedItem(id, "main", at(n), kind, ref, buildJsonObject { fields.forEach { (k, v) -> put(k, v) } })
    private val feed = listOf(
        item("plan-feed", 1, "plan", "plan"),
        item("task-feed", 2, "task"),
        item("handoff", 3, "routed", "t", "from" to "assistant", "text" to "Fix the login crash"),
        item("asked", 4, "routed", "t", "text" to "Use the staging key"),
        item("report", 5, "report", "t", "kind" to "question", "text" to "Which key?"),
        item("review", 6, "review", "t", "verdict" to "rework"),
        item("receipt", 7, "receipt", "t", "title" to "Fix the login", "summary" to "Done"),
        item("notice", 8, "notice", null, "text" to "The morning brief runs again"),
        item("message", 9, "message", "inbox-1", "text" to "When is it fixed?", "draft" to "Today"),
    )
    private fun feedIds(lines: List<ChatLine>) =
        lines.filterIsInstance<ChatLine.Extra>().mapNotNull { (it.payload as? AssistantExtra.Feed)?.item?.id }
    private fun hints(lines: List<ChatLine>) = lines.filterIsInstance<ChatLine.Extra>().mapNotNull { it.payload as? DispatchHint }

    @Test fun glassDrawsTheRecordsAmongTheLines() {
        val lines = AssistantTaskCards.lines(emptyList(), feed, mapOf(plan.id to plan), todos, inline = true)
        assertEquals(listOf("handoff", "asked", "notice", "message"), feedIds(lines))
        assertEquals(listOf("handoff", "asked", "report", "review", "receipt", "notice", "message"), feed.map { it.id }.filter { it in setOf("handoff", "asked", "report", "review", "receipt", "notice", "message") })
        // the plan and the task card are the dispatched line, once
        assertEquals(listOf(listOf("Fix the login")), hints(lines).map { hint -> hint.tasks.map { it.title } })
    }

    @Test fun glassKeepsOnlyPlansAndTaskCardsBehind() {
        assertEquals(listOf("plan-feed", "task-feed"), AssistantTaskCards.backgroundFeed(feed, inline = true).map { it.id })
    }

    @Test fun briefAndBoardKeepTheRecordsBehindTheTrays() {
        val lines = AssistantTaskCards.lines(emptyList(), feed, mapOf(plan.id to plan), todos)
        // the user's own word to the task stays, as before
        assertEquals(listOf("asked"), feedIds(lines))
        assertEquals(listOf("plan-feed", "task-feed", "handoff", "notice", "message"),
            AssistantTaskCards.backgroundFeed(feed).map { it.id })
    }

    // A plan nobody started yet: the hub's `plan` item and its tasks, none of them a to-do yet.
    private val open = TaskPlan("open", "main", listOf(PlanTask("Write the release notes", machine = "mac", cwd = "/w/shop"),
        PlanTask("Look into the crash report", machine = "box", cwd = "/srv/app", readonly = true)), "open", at(10))
    private val openFeed = listOf(item("open-feed", 10, "plan", "open"))

    @Test fun glassDrawsAPlanNobodyStartedAsACardAmongTheLines() {
        val lines = AssistantTaskCards.lines(emptyList(), feed + openFeed, mapOf(plan.id to plan, open.id to open), todos, inline = true)
        // iOS's `FeedLine`: the open plan is `PlanCard`, at the end of the exchange it happened in; the started one is the dispatched line
        assertEquals(listOf("handoff", "asked", "notice", "message", "open-feed"), feedIds(lines))
        assertEquals(listOf(listOf("Fix the login")), hints(lines).map { hint -> hint.tasks.map { it.title } })
        // Brief and Board keep every plan behind the trays
        val behind = AssistantTaskCards.lines(emptyList(), feed + openFeed, mapOf(plan.id to plan, open.id to open), todos)
        assertEquals(listOf("asked"), feedIds(behind))
        assertEquals(emptyList<DispatchHint>(), hints(behind).filter { hint -> hint.tasks.any { it.title == "Write the release notes" } })
    }

    @Test fun theRecordKeepsEveryPlanWhetherOrNotTheConversationDrawsIt() {
        // the Background Records sheet lists plans and task cards as the hub's records (iOS has no such sheet; Android keeps it)
        assertEquals(listOf("plan-feed", "task-feed", "open-feed"), AssistantTaskCards.backgroundFeed(feed + openFeed, inline = true).map { it.id })
    }

    @Test fun aPlanIsTheCardUntilItIsStartedThenTheDispatchedLineInItsPlace() {
        val tasks = open.tasks
        fun lines(plan: TaskPlan, todos: List<Todo> = emptyList()) =
            AssistantTaskCards.lines(emptyList(), openFeed, mapOf(plan.id to plan), todos, inline = true)
        // open: the card, no dispatched line
        lines(open).let { assertEquals(listOf("open-feed"), feedIds(it)); assertEquals(0, hints(it).size) }
        // Start came back: the plan is started and its to-dos exist: the dispatched line, in the card's place, no card
        val started = open.copy(state = "started", tasks = listOf(tasks[0].copy(todoId = "n1"), tasks[1].copy(todoId = "n2")))
        val made = listOf(Todo("n1", "Write the release notes", origin = "main", planId = "open", createdAt = at(11)),
            Todo("n2", "Look into the crash report", origin = "main", planId = "open", createdAt = at(11)))
        lines(started, made).let {
            assertEquals(emptyList<String>(), feedIds(it))
            assertEquals(listOf(listOf("Write the release notes", "Look into the crash report")), hints(it).map { hint -> hint.tasks.map { t -> t.title } })
        }
        // the hub started it but a task did not come up: still the dispatched line, the failed task as plain text (no to-do to open)
        val partly = open.copy(state = "started", tasks = listOf(tasks[0].copy(todoId = "n1"), tasks[1].copy(error = "launcher not found")))
        lines(partly, made.take(1)).let {
            assertEquals(emptyList<String>(), feedIds(it))
            assertEquals(listOf(listOf("n1" to "Write the release notes", null to "Look into the crash report")),
                hints(it).map { hint -> hint.tasks.map { t -> t.todoId to t.title } })
        }
        // Don't start: the line stays, as "Not started · N tasks"
        lines(open.copy(state = "dismissed")).let { assertEquals(listOf("open-feed"), feedIds(it)); assertEquals(0, hints(it).size) }
        // a plan this build can't read leaves nothing to draw
        assertEquals(emptyList<String>(), feedIds(AssistantTaskCards.lines(emptyList(), openFeed, emptyMap(), emptyList(), inline = true)))
        // the item may carry its plan itself, as the hub's event does (docs/protocol/protocol.md: a plan card's body)
        val carrying = JsonCodec.decodeFromString(FeedItem.serializer(), """{"id":"open-feed","conversation":"main","at":"${at(10)}","kind":"plan","ref":"open",
            "body":{"plan":{"id":"open","conversation":"main","state":"open","tasks":[{"title":"Write the release notes","machine":"mac","cwd":"/w/shop"}]}}}""")
        assertEquals(listOf("open-feed"), feedIds(AssistantTaskCards.lines(emptyList(), listOf(carrying), emptyMap(), emptyList(), inline = true)))
    }

    @Test fun aPlanLandsAfterTheAnswerOfItsExchangeAndBeforeTheNextMessage() {
        val ask = { id: String, n: Int -> persona.core.domain.lines.LineItem(persona.core.contracts.Item(id, n, ts = at(n), role = "user", kind = "text", text = id)) }
        val answer = persona.core.domain.lines.LineItem(persona.core.contracts.Item("answer", 2, ts = at(2), role = "assistant", kind = "text", text = "Here is a plan"))
        val transcript = persona.core.domain.lines.chatLines(listOf(ask("first", 1), answer, ask("second", 20))).lines
        val ids = AssistantTaskCards.lines(transcript, openFeed, mapOf(open.id to open), emptyList(), inline = true).map { it.id }
        // the plan item sits after the first exchange's answer and before the second message, as every other record does
        assertEquals(listOf("feed-open-feed"), ids.filter { it.startsWith("feed-") })
        val at = ids.indexOf("feed-open-feed")
        assertEquals(true, ids.indexOf(answer.id) < at && at < ids.indexOf("second"))
    }

    @Test fun aNoticeKeepsItsKindOnTheWire() {
        val decoded = JsonCodec.decodeFromString(FeedItem.serializer(),
            """{"id":"n","conversation":"main","at":"${at(8)}","kind":"notice","body":{"text":"Ready again"}}""")
        assertEquals("notice", decoded.kind)
        assertEquals("Ready again", decoded.field("text"))
        // a kind this build doesn't know still reads as unknown
        assertEquals("unknown", JsonCodec.decodeFromString(FeedItem.serializer(), """{"id":"x","kind":"later_kind"}""").kind)
    }

    @Test fun mergeHandlesReplannedTodosProjectedOverridesAndReceiptReportIds() {
        val oldPlan = TaskPlan("plan-old", "main", listOf(PlanTask("Old task", todoId = "t1")), "started", at(1))
        val newPlan = TaskPlan("plan-new", "main", listOf(PlanTask("New task", todoId = "t1")), "started", at(5))
        val todo = Todo("t1", "Replanned task", origin = "main", planId = "plan-new", createdAt = at(2))
        val projected = listOf(
            AssistantTaskCard(
                id = "plan:plan-old:0",
                todoId = "t1",
                planId = "plan-old",
                index = 0,
                at = at(1),
                state = "started",
                receiptId = "rc-extra",
                reportId = "rp-extra",
            ),
            AssistantTaskCard(
                id = "plan:plan-new:0",
                todoId = "t1",
                planId = "plan-new",
                index = 0,
                at = at(5),
                state = "started",
                receiptId = "rc-extra",
                reportId = "rp-extra",
            ),
        )
        val mergedFeed = listOf(
            item("plan-old-feed", 1, "plan", "plan-old"),
            item("plan-new-feed", 5, "plan", "plan-new"),
            item("task-t1", 6, "task", "t1"),
            item("rc-extra", 7, "receipt", null, "title" to "Replanned task", "summary" to "Done"),
            item("rp-extra", 8, "report", null, "text" to "Progress"),
            item("task-t1", 9, "task", "t1"),
        )
        val merged = AssistantTaskCards.merge(
            feed = mergedFeed,
            plans = mapOf(oldPlan.id to oldPlan, newPlan.id to newPlan),
            todos = listOf(todo),
            projected = projected,
        )
        assertEquals(1, merged.size)
        val card = merged.single()
        assertEquals("plan-new", card.identity.planId)
        assertEquals(newPlan, card.plan)
        assertEquals(todo, card.todo)
        assertEquals(listOf("task-t1", "rc-extra", "rp-extra"), card.records.map { it.id })
        assertEquals("rc-extra", card.receipt?.id)
        assertEquals("rp-extra", card.report?.id)
    }

    @Test fun earliestClipsOlderTaskCardsBriefsAndDayLinesWhileKeepingBackgroundAndTraysIntact() {
        val day1Plan = TaskPlan("p1", "main", listOf(PlanTask("Old cleanup", todoId = "t1")), "started", "2026-11-10T09:05:00Z")
        val day1Open = TaskPlan("p1-open", "main", listOf(PlanTask("Old unstarted plan")), "open", "2026-11-10T09:10:00Z")
        val day2Plan = TaskPlan("p2", "main", listOf(PlanTask("Middle task", todoId = "t2")), "started", "2026-11-11T14:05:00Z")
        val day3Plan = TaskPlan("p3", "main", listOf(PlanTask("Current task", todoId = "t3")), "started", "2026-11-12T13:05:00Z")
        val day3Open = TaskPlan("p3-open", "main", listOf(PlanTask("Pending confirmation")), "open", "2026-11-12T13:10:00Z")
        val untimedOpen = TaskPlan("p-untimed", "main", listOf(PlanTask("Untimed open plan")), "open", createdAt = null)
        val allPlans = listOf(day1Plan, day1Open, day2Plan, day3Plan, day3Open, untimedOpen).associateBy { it.id }
        val allTodos = listOf(
            Todo("t1", "Old cleanup", origin = "main", planId = "p1", createdAt = "2026-11-10T09:05:00Z"),
            Todo("t2", "Middle task", origin = "main", planId = "p2", createdAt = "2026-11-11T14:05:00Z"),
            Todo("t3", "Current task", origin = "main", planId = "p3", createdAt = "2026-11-12T13:05:00Z"),
        )
        val multiDayFeed = listOf(
            FeedItem("day1-today", "main", "2026-11-10T08:00:00Z", "today"),
            FeedItem("day1-plan", "main", "2026-11-10T09:05:00Z", "plan", "p1"),
            FeedItem("day1-open-feed", "main", "2026-11-10T09:10:00Z", "plan", "p1-open"),
            FeedItem("day2-brief", "main", "2026-11-11T08:00:00Z", "brief"),
            FeedItem("day2-plan", "main", "2026-11-11T14:05:00Z", "plan", "p2"),
            FeedItem("day3-today", "main", "2026-11-12T08:00:00Z", "today"),
            FeedItem("day3-plan", "main", "2026-11-12T13:05:00Z", "plan", "p3"),
            FeedItem("day3-open-feed", "main", "2026-11-12T13:10:00Z", "plan", "p3-open"),
            FeedItem("untimed-open-feed", "main", at = null, kind = "plan", ref = "p-untimed"),
        )
        val day3Transcript = chatLines(listOf(
            LineItem(Item("u3", 30, ts = "2026-11-12T13:00:00Z", role = "user", kind = "text", text = "Latest ask")),
            LineItem(Item("a3", 31, ts = "2026-11-12T13:01:00Z", role = "assistant", kind = "text", text = "Started")),
        )).lines

        // Page 1 (hasOlder = true): only Day 3 >= 13:00 and untimed open plan show; older days and 08:00 today card wait.
        val page1Glass = AssistantTaskCards.lines(
            day3Transcript, multiDayFeed, allPlans, allTodos,
            inline = true, earliest = Instant.parse("2026-11-12T13:00:00Z"), zone = ZoneOffset.UTC,
        )
        assertEquals(listOf("day-2026-11-12"), page1Glass.map { it.id }.filter { it.startsWith("day-") })
        assertEquals(listOf("untimed-open-feed", "day3-open-feed"), feedIds(page1Glass))
        assertEquals(listOf(listOf("Current task")), hints(page1Glass).map { hint -> hint.tasks.map { it.title } })

        // Brief/Board (inline = false): older dispatched task cards and briefs are also clipped.
        val page1Brief = AssistantTaskCards.lines(
            day3Transcript, multiDayFeed, allPlans, allTodos,
            inline = false, earliest = Instant.parse("2026-11-12T13:00:00Z"), zone = ZoneOffset.UTC,
        )
        assertEquals(listOf("day-2026-11-12"), page1Brief.map { it.id }.filter { it.startsWith("day-") })
        assertEquals(emptyList<String>(), feedIds(page1Brief))
        assertEquals(listOf(listOf("Current task")), hints(page1Brief).map { hint -> hint.tasks.map { it.title } })

        // Background records and merged cards for trays still keep the full raw feed history.
        assertEquals(
            listOf("day1-plan", "day1-open-feed", "day2-plan", "day3-plan", "day3-open-feed", "untimed-open-feed"),
            AssistantTaskCards.backgroundFeed(multiDayFeed, inline = true).map { it.id },
        )
        assertEquals(listOf("p-untimed", "p1", "p1-open", "p2", "p3", "p3-open"),
            AssistantTaskCards.merge(multiDayFeed, allPlans, allTodos).mapNotNull { it.identity.planId })

        // Page 2 (loadOlder prepends Day 2 14:00): Day 2 task card and Day 3 08:00 today card reappear chronologically.
        val day2And3Transcript = chatLines(listOf(
            LineItem(Item("u2", 20, ts = "2026-11-11T14:00:00Z", role = "user", kind = "text", text = "Yesterday ask")),
            LineItem(Item("a2", 21, ts = "2026-11-11T14:01:00Z", role = "assistant", kind = "text", text = "On it")),
            LineItem(Item("u3", 30, ts = "2026-11-12T13:00:00Z", role = "user", kind = "text", text = "Latest ask")),
            LineItem(Item("a3", 31, ts = "2026-11-12T13:01:00Z", role = "assistant", kind = "text", text = "Started")),
        )).lines
        val page2Glass = AssistantTaskCards.lines(
            day2And3Transcript, multiDayFeed, allPlans, allTodos,
            inline = true, earliest = Instant.parse("2026-11-11T14:00:00Z"), zone = ZoneOffset.UTC,
        )
        assertEquals(listOf("day-2026-11-11", "day-2026-11-12"), page2Glass.map { it.id }.filter { it.startsWith("day-") })
        assertEquals(
            listOf("feed-untimed-open-feed", "u2", "a2", "dispatch-p2", "feed-day3-today", "u3", "a3", "dispatch-p3", "feed-day3-open-feed"),
            page2Glass.map { it.id }.filterNot { it.startsWith("day-") },
        )

        // Complete history (hasOlder = false / earliest = null): Day 1, Day 2, and Day 3 all appear in order.
        val completeGlass = AssistantTaskCards.lines(
            day2And3Transcript, multiDayFeed, allPlans, allTodos,
            inline = true, earliest = null, zone = ZoneOffset.UTC,
        )
        assertEquals(
            listOf("day-2026-11-10", "day-2026-11-11", "day-2026-11-12"),
            completeGlass.map { it.id }.filter { it.startsWith("day-") },
        )
        assertEquals(
            listOf("Old cleanup", "Middle task", "Current task"),
            hints(completeGlass).flatMap { hint -> hint.tasks.map { it.title } },
        )
        assertEquals(
            listOf("untimed-open-feed", "day1-today", "day1-open-feed", "day2-brief", "day3-today", "day3-open-feed"),
            feedIds(completeGlass),
        )
    }

    @Test fun earliestClipsSparseProjectedTaskCardUsingPlanTimestampAndKeepsUntimedCard() {
        val oldPlan = TaskPlan("old", "main", listOf(PlanTask("Sparse old task", todoId = "t-old")), "started", "2026-11-10T09:00:00Z")
        val oldTodo = Todo("t-old", "Sparse old task", origin = "main", planId = "old")
        val sparseOld = AssistantTaskCard("plan:old:0", "t-old", "old", 0, at = null, state = "accepted")
        val untimedTodo = Todo("t-untimed", "Untimed active task", origin = "main")
        val untimedProjected = AssistantTaskCard("todo:t-untimed", "t-untimed", at = null, state = "working")

        val clipped = AssistantTaskCards.lines(
            emptyList(),
            emptyList(),
            mapOf(oldPlan.id to oldPlan),
            listOf(oldTodo, untimedTodo),
            projected = listOf(sparseOld, untimedProjected),
            inline = true,
            earliest = Instant.parse("2026-11-12T13:00:00Z"),
            zone = ZoneOffset.UTC,
        )
        assertEquals(listOf(listOf("Untimed active task")), hints(clipped).map { hint -> hint.tasks.map { it.title } })

        val full = AssistantTaskCards.lines(
            emptyList(),
            emptyList(),
            mapOf(oldPlan.id to oldPlan),
            listOf(oldTodo, untimedTodo),
            projected = listOf(sparseOld, untimedProjected),
            inline = true,
            earliest = null,
            zone = ZoneOffset.UTC,
        )
        assertEquals(
            listOf("Untimed active task", "Sparse old task"),
            hints(full).flatMap { hint -> hint.tasks.map { it.title } },
        )
    }

    @Test fun duplicateRecordIdPreservesEarliestFeedMatchAcrossReceiptAndTodoRef() {
        val receiptFirst = persona.core.contracts.FeedItem(
            id = "dup_rec",
            at = "2026-11-25T09:00:00Z",
            kind = "task",
            ref = "other_todo",
            body = kotlinx.serialization.json.JsonPrimitive("earliest-receipt-body"),
        )
        val todoRefLater = persona.core.contracts.FeedItem(
            id = "dup_rec",
            at = "2026-11-25T09:05:00Z",
            kind = "task",
            ref = "todo_target",
            body = kotlinx.serialization.json.JsonPrimitive("later-todo-ref-body"),
        )
        val card = persona.core.contracts.AssistantTaskCard(
            id = "todo:todo_target",
            todoId = "todo_target",
            at = "2026-11-25T08:55:00Z",
            receiptId = "dup_rec",
        )
        val merged = AssistantTaskCards.merge(
            feed = listOf(receiptFirst, todoRefLater),
            plans = emptyMap(),
            todos = emptyList(),
            projected = listOf(card),
        )
        val target = merged.single { it.identity.todoId == "todo_target" }
        org.junit.Assert.assertEquals(1, target.records.size)
        org.junit.Assert.assertEquals(kotlinx.serialization.json.JsonPrimitive("earliest-receipt-body"), target.records.single().body)
        org.junit.Assert.assertEquals("2026-11-25T09:00:00Z", target.records.single().at)
    }
}
