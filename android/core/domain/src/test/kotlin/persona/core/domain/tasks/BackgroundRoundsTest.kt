package persona.core.domain.tasks

import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.AssistantConfig
import persona.core.domain.lines.*

/** #4314: the main conversation folds the rounds another conversation, a scheduled task or a follow-up started. */
class BackgroundRoundsTest {
    private fun user(n: Int, text: String, source: MessageSource? = null, injected: Injected? = null, queued: Boolean? = null) =
        Item("u$n", n, ts = "2026-11-10T10:00:${n.toString().padStart(2, '0')}Z", role = "user", kind = "text",
            text = text, source = source, injected = injected, queued = queued)
    private fun reply(n: Int, text: String) = Item("a$n", n, role = "assistant", kind = "text", text = text)
    private fun use(n: Int, tool: String, input: kotlinx.serialization.json.JsonObject? = null) =
        Item("use$n", n, role = "assistant", kind = "tool_use", toolUseId = "call$n", toolName = tool, toolInput = input)
    private fun result(n: Int, call: Int, text: String = "ok", error: Boolean? = null) =
        Item("res$n", n, role = "user", kind = "tool_result", toolUseId = "call$call", text = text, isError = error)

    private fun project(items: List<Item>, folding: BackgroundFolding? = BackgroundFolding(), live: Boolean = false) =
        AssistantConversation.project(chatLines(items.map(::LineItem), LineOptions(live = live)).lines, items, folding)

    private fun folds(lines: List<ChatLine>) = lines.mapNotNull { (it as? ChatLine.Extra)?.payload as? BackgroundRound }

    @Test fun aSupervisorMessageRoundFoldsIntoOneLineThatOpensToItsWholeTranscript() {
        val supervisor = MessageSource("conversation", "c2", "Release plan")
        val items = listOf(user(1, "My question"), reply(2, "My answer"),
            user(3, "Please check the build", supervisor), use(4, "Bash"), result(5, 4), reply(6, "Build is green"),
            user(7, "Next question"), reply(8, "Next answer"))
        val closed = project(items).foreground
        val round = folds(closed).single()
        assertEquals("bg-u3", round.id)
        assertEquals(supervisor, round.source)
        assertFalse(round.live)
        assertFalse(round.open)
        assertEquals(listOf("u1", "a2", "bg-u3", "u7", "a8"), closed.filter { it !is ChatLine.Work }.map { it.id })
        assertTrue("The round's lines are all inside the fold", round.lines.map { it.id }.containsAll(listOf("u3", "a6")))
        assertTrue(closed.none { it.id == "u3" || it.id == "a6" })
        // Opened by hand: the full transcript, the supervisor's message included, right under the line.
        val opened = project(items, BackgroundFolding(open = setOf("bg-u3"))).foreground
        val at = opened.indexOfFirst { it.id == "bg-u3" }
        assertTrue(folds(opened).single().open)
        assertEquals(round.lines.map { it.id }, opened.subList(at + 1, at + 1 + round.lines.size).map { it.id })
        assertTrue(opened.any { it.id == "u3" })
    }

    @Test fun scheduledAndFollowUpRoundsFoldAndAreNotMovedToTheBackgroundRecord() {
        val items = listOf(user(1, "My question"), reply(2, "My answer"),
            user(3, "Daily: summarize the inbox", injected = Injected("followup", "daily", "Inbox digest", line = "⏰ Inbox digest", detail = "Summarize the inbox")),
            reply(4, "Digest done"),
            user(5, "Remind about the PR", source = MessageSource("followup", "main", "PR reminder")), reply(6, "Reminded"))
        val projection = project(items)
        assertEquals(listOf("patrol", "followup"), folds(projection.foreground).map { it.source.kind })
        assertEquals("Inbox digest", folds(projection.foreground).first().source.title)
        assertTrue(projection.foreground.none { it.id in setOf("u3", "a4", "u5", "a6") })
        assertTrue("Folded, not moved into the background record", projection.background.none { it.id in setOf("u3", "a4", "u5", "a6") })
    }

    @Test fun usageContinuationsWakeUpsAndUntitledFollowUpsAreNotFolded() {
        val items = listOf(user(1, "My question"), reply(2, "Partial answer"),
            user(3, "Continue after the usage limit", MessageSource("followup", title = "Usage limit"),
                Injected("followup", "limit_continue", "Usage limit", line = "Continued")), reply(4, "The answer, continued"),
            user(5, "Background task finished", MessageSource("followup", title = "Wake"),
                Injected("followup", "wake", "Wake", line = "Woken")), reply(6, "The answer after the wake"),
            user(7, "A follow-up without a title", MessageSource("followup")), reply(8, "The untitled answer"),
            user(9, "Usage reset", injected = Injected("limit_continue", line = "Continued")), reply(10, "Continued again"))
        val foreground = project(items).foreground
        assertTrue(folds(foreground).isEmpty())
        for (id in listOf("a4", "a6", "a8", "a10")) assertTrue(id, foreground.any { it.id == id })
        // a titled scheduled follow-up still folds
        val scheduled = listOf(user(1, "Remind me", MessageSource("followup", "main", "PR reminder"),
            Injected("followup", "once", "PR reminder", line = "PR reminder")), reply(2, "Reminded"))
        assertEquals(1, folds(project(scheduled).foreground).size)
    }

    @Test fun aRoundThatAskedTheUserStaysShown() {
        val items = listOf(user(1, "Please check the build", MessageSource("conversation", "c2", "Release plan")),
            use(2, "mcp__persona__ask_user"), result(3, 2, "Yes"), reply(4, "Done"),
            user(5, "Nightly check", MessageSource("patrol", title = "Nightly")), use(6, "mcp__persona__notify_user"),
            result(7, 6), reply(8, "Told you"))
        val projection = project(items)
        assertTrue(folds(projection.foreground).isEmpty())
        assertTrue(projection.foreground.any { it.id == "u1" })
        assertTrue(projection.foreground.any { it.id == "a8" })
    }

    @Test fun onlySuccessfulOrStillPendingVisibilityToolsKeepARoundShown() {
        val source = MessageSource("conversation", "c2", "Release plan")
        // failed notify, another server's ask_user, a rejected review and a quiet finish: folded
        val quiet = listOf(user(1, "Check", source), use(2, "mcp__persona__notify_user"), result(3, 2, "failed", error = true),
            use(4, "mcp__other__ask_user"), result(5, 4), use(6, "task_review", buildJsonObject { put("verdict", "rejected") }), result(7, 6),
            use(8, "supervision_finish"), result(9, 8, """{"visibility":"quiet"}"""), reply(10, "Done"))
        assertEquals(1, folds(project(quiet).foreground).size)
        val accepted = listOf(user(1, "Check", source), use(2, "task_review", buildJsonObject { put("verdict", "accepted") }), result(3, 2), reply(4, "Done"))
        assertTrue(folds(project(accepted).foreground).isEmpty())
        val finish = listOf(user(1, "Check", source), use(2, "mcp__persona__supervision_finish"), result(3, 2, """{"visibility":"user"}"""), reply(4, "Done"))
        assertTrue(folds(project(finish).foreground).isEmpty())
        // ask_user waiting for the user while the round runs: never buried
        val asking = listOf(user(1, "Check", source), use(2, "mcp__persona__ask_user"))
        assertTrue(folds(project(asking, live = true).foreground).isEmpty())
        val promoted = listOf(user(1, "Check", source), Item("f", 2, role = "assistant", kind = "fold",
            fold = persona.core.contracts.Fold(items = 3, throughSeq = 4, throughId = "x", supervisionUser = true)), reply(5, "Done"))
        assertTrue(folds(project(promoted).foreground).isEmpty())
    }

    @Test fun aSupervisionRoundMarkedLevelUserStaysShownWhileTriageFolds() {
        // web turns.ts supervisionVisible: injected.state == "supervise" && injected.level == "user" keeps the round open,
        // the same rule on iOS. A failed notify alone would fold it; the user-level mark wins.
        val source = MessageSource("conversation", "c2", "Release plan")
        val quietTools = listOf(
            use(2, "mcp__persona__notify_user"), result(3, 2, "failed", error = true), reply(4, "Done"))
        val userLevel = listOf(
            user(1, "Check", source, injected = Injected("system", "supervise", line = "督办：2 个派出去的任务要处理", level = "user")),
        ) + quietTools
        assertTrue("level=user unfolds the round like the web's", folds(project(userLevel).foreground).isEmpty())
        val triage = listOf(
            user(1, "Check", source, injected = Injected("system", "supervise", line = "督办：2 个派出去的任务要处理", level = "triage")),
        ) + quietTools
        assertEquals(1, folds(project(triage).foreground).size)
        val noLevel = listOf(
            user(1, "Check", source, injected = Injected("system", "supervise", line = "督办：2 个派出去的任务要处理")),
        ) + quietTools
        assertEquals("An older hub without level keeps folding", 1, folds(project(noLevel).foreground).size)
    }

    @Test fun aLiveRoundSaysItRunsInTheBackground() {
        val items = listOf(user(1, "My question"), reply(2, "My answer"), user(3, "Nightly", MessageSource("patrol", title = "Nightly")))
        val round = folds(project(items, live = true).foreground).single()
        assertTrue(round.live)
        assertEquals("bg-u3", round.id)
    }

    @Test fun aQueuedUserMessageEndsTheRound() {
        val items = listOf(user(1, "Check", MessageSource("conversation", "c2", "Release plan")), reply(2, "Checked"),
            user(3, "My own queued question", queued = true), reply(4, "My answer"))
        val foreground = project(items).foreground
        assertEquals(listOf("u1", "a2"), folds(foreground).single().lines.filterNot { it is ChatLine.Work }.map { it.id })
        assertTrue(foreground.any { it.id == "u3" } && foreground.any { it.id == "a4" })
    }

    @Test fun aSystemHintInsideARoundDoesNotEndIt() {
        val items = listOf(user(1, "Check", MessageSource("conversation", "c2", "Release plan")), reply(2, "Checking"),
            user(3, "Internal restart notification", injected = Injected("system", "lost_tasks")), reply(4, "Checked"),
            user(5, "My question"), reply(6, "My answer"))
        val foreground = project(items).foreground
        val round = folds(foreground).single()
        assertTrue(round.lines.any { it.id == "a4" || (it is ChatLine.Work && it.steps.any { s -> s is WorkItem.Narration && s.item.id == "a2" }) })
        assertTrue(foreground.none { it.id == "a4" })
        assertTrue(foreground.any { it.id == "u5" } && foreground.any { it.id == "a6" })
    }

    @Test fun aFoldedRoundKeepsItsPlaceAmongTheFeedAndDays() {
        val items = listOf(user(1, "My question"), reply(2, "My answer"),
            user(3, "Check", MessageSource("conversation", "c2", "Release plan")), reply(4, "Checked"))
        val before = FeedItem("before", "main", "2026-11-10T10:00:02Z", "routed", body = buildJsonObject { put("text", "Earlier") })
        for (open in listOf(emptySet(), setOf("bg-u3"))) {
            val lines = AssistantLines.lines(project(items, BackgroundFolding(open)).foreground, listOf(before), java.time.ZoneOffset.UTC)
            val ids = lines.map { it.id }
            assertTrue("$ids", ids.indexOf("feed-before") < ids.indexOf("bg-u3"))
            assertEquals("One day line, over the first line: $ids", 1, ids.count { it.startsWith("day-") })
            if (open.isNotEmpty()) assertEquals(ids.indexOf("bg-u3") + 1, ids.indexOf("u3"))
        }
    }

    @Test fun switchedOffNothingFolds() {
        val items = listOf(user(1, "Check", MessageSource("conversation", "c2", "Release plan")), reply(2, "Checked"))
        val off = project(items, folding = null)
        assertTrue(folds(off.foreground).isEmpty())
        assertEquals(AssistantConversation.project(chatLines(items.map(::LineItem)).lines, items), off)
    }

    @Test fun aConversationOtherThanTheMainOneDoesNotFold() {
        // The chat passes a folding only for the main conversation with the switch on; any other gets none.
        val items = listOf(user(1, "Please check", MessageSource("conversation", "c2", "Release plan")), reply(2, "Checked"))
        assertTrue(folds(project(items, folding = null).foreground).isEmpty())
        assertTrue(project(items, folding = null).foreground.any { it.id == "u1" })
    }

    @Test fun aSearchHitOpensItsRound() {
        val items = listOf(user(1, "Check", MessageSource("conversation", "c2", "Release plan")), reply(2, "Checked"))
        val foreground = project(items, BackgroundFolding(reveal = "a2")).foreground
        assertTrue(folds(foreground).single().open)
        assertTrue(foreground.any { it.id == "a2" })
    }

    @Test fun needsYouGroupsTaskRowsBySupervisingConversationOnlyWithMoreThanOne() {
        fun task(id: String, origin: String?) = NeedsYouEntry.Task(Todo(id, id, origin = origin), TriageStatus.NeedsYou, emptyList())
        val plan = NeedsYouEntry.Plan(TaskPlan("p", "main", emptyList(), "open", "2026-11-10T10:00:00Z"))
        val rows = listOf(task("a", "other"), task("b", "main"), plan, task("c", "other"), task("d", null))
        val groups = needsYouGroups(rows, "main", folded = true)
        assertEquals(listOf("main", "other"), groups.map { it.conversation })
        assertEquals(listOf("task:b", "plan:p", "task:d"), groups[0].entries.map { it.key })
        assertEquals(listOf("task:a", "task:c"), groups[1].entries.map { it.key })
        assertEquals(rows.size, groups.sumOf { it.entries.size })
        // one supervisor, or the switch off: one group, exactly as before
        val one = listOf(task("a", "other"), plan, task("c", "other"))
        assertEquals(listOf(NeedsYouGroup("main", one)), needsYouGroups(one, "main", folded = true))
        assertEquals(listOf(NeedsYouGroup("main", rows)), needsYouGroups(rows, "main", folded = false))
    }

    @Test fun theSwitchIsOnWhenTheHubDoesNotSendIt() {
        val old = JsonCodec.decodeFromString(AssistantConfig.serializer(), """{"machine_id":"m","launcher":"claude","cwd":""}""")
        assertNull(old.mainBackgroundFolded)
        assertTrue(old.backgroundFolded)
        val off = JsonCodec.decodeFromString(AssistantConfig.serializer(), """{"machine_id":"m","main_background_folded":false}""")
        assertFalse(off.backgroundFolded)
    }

    @Test fun leaderConversationsFoldExactlyLikeTheMainConversation() {
        val leaders = setOf("lead-1", "lead-2")
        // the main conversation and every leader fold while the switch is on
        assertTrue(BackgroundFolding.foldsConversation("main", "main", on = true, leaders))
        assertTrue(BackgroundFolding.foldsConversation("lead-1", "main", on = true, leaders))
        assertTrue(BackgroundFolding.foldsConversation("lead-2", "main", on = true, leaders))
        // an ordinary side conversation never folds
        assertFalse(BackgroundFolding.foldsConversation("side", "main", on = true, leaders))
        // the switch off disables both the main and the leaders
        assertFalse(BackgroundFolding.foldsConversation("main", "main", on = false, leaders))
        assertFalse(BackgroundFolding.foldsConversation("lead-1", "main", on = false, leaders))
        // a missing main still lets a leader fold; a blank conversation id never does
        assertTrue(BackgroundFolding.foldsConversation("lead-1", null, on = true, leaders))
        assertFalse(BackgroundFolding.foldsConversation("", "main", on = true, leaders))
        // no leaders from an older hub: only the main folds
        assertTrue(BackgroundFolding.foldsConversation("main", "main", on = true, emptySet()))
        assertFalse(BackgroundFolding.foldsConversation("lead-1", "main", on = true, emptySet()))
    }

    @Test fun aLeaderConversationFoldsTheSameRoundsTheMainChatDoes() {
        // the gate on for a leader id drives the same folding: relayed and patrol rounds fold, the owner's stays open
        val items = listOf(
            user(1, "Check the build", MessageSource("conversation", "w1", "Release desk")),
            use(2, "Read"), result(3, 2), reply(4, "Build is green"),
            user(5, "Scheduled prompt", MessageSource("patrol", title = "Half-hour patrol"),
                Injected(kind = "followup", state = "daily", title = "Half-hour patrol", line = "Scheduled task started")),
            reply(6, "Nothing new"),
            user(7, "A real request", queued = null).copy(clientMsgId = "owner-1"), reply(8, "On it"),
        )
        val folded = project(items, BackgroundFolding(rounds = true)).foreground
        val rounds = folds(folded)
        assertEquals(listOf("conversation", "patrol"), rounds.map { it.source.kind })
        // the owner's exchange remains a normal foreground row, not a folded round
        assertTrue(folded.any { line -> (line as? ChatLine.Row)?.row?.item?.item?.clientMsgId == "owner-1" })
        // folding off keeps every exchange open
        val open = project(items, BackgroundFolding(rounds = false)).foreground
        assertTrue(folds(open).isEmpty())
    }
}
