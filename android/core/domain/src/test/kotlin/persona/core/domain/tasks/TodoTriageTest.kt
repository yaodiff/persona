package persona.core.domain.tasks

import java.time.Instant
import java.time.ZoneOffset
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.DecisionReview
import persona.core.contracts.ReviewCheck
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Supervision
import persona.core.contracts.TaskProgress
import persona.core.contracts.TaskReview
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.TodoAsk
import persona.core.contracts.TodoDelivery
import persona.core.contracts.TodoPRStatus
import persona.core.contracts.generated.TodoWait
import persona.core.contracts.generated.TodoWaitKind
import persona.core.contracts.generated.TodoWaitPRBlocker
import persona.core.contracts.TodoState
import persona.core.contracts.WorkState

// The same cases as web/src/lib/todo-triage.test.ts (docs/assistant/todo-status-sop.md, decision table), so the clients
// cannot drift apart.
class TodoTriageTest {
    private val now = Instant.parse("2026-11-10T12:00:00Z").toEpochMilli()
    private val zone = ZoneOffset.UTC
    private fun hoursAgo(h: Int) = Instant.ofEpochMilli(now - h * 3_600_000L).toString()

    private fun session(sid: String, state: String, updatedHoursAgo: Int = 1) =
        Session(machineId = "m", sessionId = sid, state = state, status = if (state == "working") "working" else "idle", updatedAt = hoursAgo(updatedHoursAgo))

    private fun agent(sid: String) = TodoAgent(machineId = "m", sessionId = sid, linkedAt = hoursAgo(200))

    private fun todo(
        id: String, title: String = id, updated: Int = 1, doneAt: String? = null, agents: List<TodoAgent> = emptyList(),
        origin: String? = "conv", supervision: Supervision? = null, ask: TodoAsk? = null, delivery: TodoDelivery? = null,
        prUrl: String? = null, prStatus: TodoPRStatus? = null, reopenedAt: String? = null, dueDate: String? = null, startError: String? = null, quiet: Boolean = false,
        planId: String? = null,
    ) = Todo(
        id = id, title = title, createdAt = hoursAgo(300), updatedAt = hoursAgo(updated), doneAt = doneAt, agents = agents,
        origin = origin, supervision = supervision, ask = ask, delivery = delivery, prUrl = prUrl, prStatus = prStatus, reopenedAt = reopenedAt,
        dueDate = dueDate, startError = startError, quiet = quiet, planId = planId,
    )

    /** An accepted task the hub reopened when its agent was sent more work: eight criteria, the review judged five. */
    private fun acceptedReopened(): Supervision {
        val crit = (1..8).map { "c$it" }
        return Supervision(
            supervisor = "conv", doneWhen = crit, state = "accepted", round = 1,
            reviews = listOf(TaskReview(at = hoursAgo(5), verdict = "accepted", checks = crit.take(5).map { ReviewCheck(it, true) })),
        )
    }

    private fun lookupOf(vararg sessions: Session): (SessionKey) -> Session? {
        val map = sessions.associateBy { it.key }
        return { map[it] }
    }

    private val lookup = lookupOf(
        session("ask", "needs_you"), session("run", "working"), session("unseen", "unread"),
        session("starting", "idle").copy(starting = true),
        session("idle", "idle", 2), session("old", "idle", 100),
    )

    private fun at(td: Todo, restoredAt: Long = 0) = td.triage(lookup, now, restoredAt, zone)

    private fun Todo.withKind(kind: String) = copy(kind = kind)

    private fun sharedAgent(sid: String, linkedAt: String) = TodoAgent(machineId = "m", sessionId = sid, linkedAt = linkedAt)

    private fun atShared(td: Todo, vararg sessions: Session) = td.triage(lookupOf(*sessions), now, zone = zone)

    @Test fun interruptedAgentDoesNotHideAnotherAgentsBackgroundWait() {
        val cut = session("cut", "idle").copy(turnError = "stopped")
        val blocked = session("blocked", "idle").copy(work = WorkState(phase = "blocked", on = "external"))
        val td = todo("t", agents = listOf(agent("cut"), agent("blocked")))
        assertEquals(TriageStatus.Waiting, atShared(td, cut, blocked).let { td.triageStatus(lookupOf(cut, blocked), now) })
        assertEquals(TriageSection.Working, atShared(td, cut, blocked).section)
    }

    private fun openPr(url: String) = TodoPRStatus(url = url, state = "open", checkedAt = Instant.ofEpochMilli(now).toString())

    private fun sharedTodo(
        id: String,
        title: String,
        createdAt: String,
        updatedAt: String,
        kind: String = "concern",
        doneAt: String? = null,
        doneBy: String? = null,
        agents: List<TodoAgent> = emptyList(),
        supervision: Supervision? = null,
        delivery: TodoDelivery? = null,
        prUrl: String? = null,
        prStatus: TodoPRStatus? = null,
        stage: String? = null,
        seenAt: String? = null,
        startError: String? = null,
        planId: String? = null,
    ) = Todo(
        id = id, title = title, kind = kind, createdAt = createdAt, updatedAt = updatedAt, doneAt = doneAt, doneBy = doneBy,
        agents = agents, origin = "main", supervision = supervision, delivery = delivery, prUrl = prUrl, prStatus = prStatus, stage = stage,
        seenAt = seenAt, startError = startError, planId = planId,
    )

    /** The web's "says when a finished to-do finished" (#10922): the moment its rule reads, else its last move. */
    @Test
    fun finishedAtIsTheMomentItsRuleReadsElseItsLastMove() {
        fun finished(td: Todo, superseded: Boolean = false) = td.finishedAt(td.triage(lookup, now, zone = zone, superseded = superseded))
        fun ms(at: String) = Instant.parse(at).toEpochMilli()
        // closed: its close, however late it moved after (a seen mark is no move)
        assertEquals(ms(hoursAgo(3)), finished(todo("t", doneAt = hoursAgo(3)).copy(seenAt = minutesAgo(30))))
        // accepted: its accepting review; deployed: its deploy, else its last move
        assertEquals(ms(hoursAgo(5)), finished(todo("t", agents = listOf(agent("idle")), supervision = acceptedReopened())))
        assertEquals(ms(hoursAgo(2)), finished(todo("t", delivery = TodoDelivery(state = "live", at = hoursAgo(2)))))
        assertEquals(ms(hoursAgo(6)), finished(todo("t", updated = 6, delivery = TodoDelivery(state = "live"))))
        // a finished turn the owner saw (Done, rule 9a): its last move, here its Agent's an hour ago
        assertEquals(ms(hoursAgo(1)), finished(todo("t", updated = 3, agents = listOf(agent("unseen"))).copy(seenAt = minutesAgo(30))))
        // a failed start a related to-do finished: its close
        assertEquals(ms(hoursAgo(2)), finished(todo("f", startError = "x", doneAt = hoursAgo(2)), superseded = true))
    }

    @Test
    fun r1ConcernKeepsFailedStartWaitingAndRunningRules() {
        val failed = at(todo("failed", startError = "no launcher").withKind("concern"))
        assertEquals(TriageSection.Waiting to TriageRule.FailedStart, failed.section to failed.rule)
        val asking = at(todo("asking", agents = listOf(agent("ask"))).withKind("concern"))
        assertEquals(TriageSection.Waiting to TriageRule.AgentAsks, asking.section to asking.rule)
        val deciding = at(todo("deciding", supervision = Supervision(supervisor = "c", state = "needs_user")).withKind("concern"))
        assertEquals(TriageSection.Waiting to TriageRule.OwnerDecides, deciding.section to deciding.rule)
        val running = at(todo("running", updated = 400, agents = listOf(agent("run"))).withKind("concern"))
        assertEquals(TriageSection.Working to TriageRule.AgentRunning, running.section to running.rule)
        val starting = at(todo("starting", updated = 400, agents = listOf(agent("starting"))).withKind("concern"))
        assertEquals(TriageSection.Working to TriageRule.AgentRunning, starting.section to starting.rule)
    }

    @Test
    fun r2ConcernFailedStartAfter48HoursIsOtherOpenNotDone() {
        val t = at(todo("failed", updated = 100, startError = "no launcher").withKind("concern"))
        assertEquals(TriageSection.Other to TriageRule.Rest, t.section to t.rule)
        val ordinary = at(todo("ordinary", updated = 100, startError = "no launcher"))
        assertEquals(TriageSection.Done to TriageRule.FailedStart, ordinary.section to ordinary.rule)
    }

    @Test
    fun r3ConcernNeverUsesAcceptedOrDeployed() {
        val accepted = at(todo("accepted", agents = listOf(agent("idle")), supervision = acceptedReopened()).withKind("concern"))
        assertEquals(TriageSection.Other to TriageRule.Rest, accepted.section to accepted.rule)
        val deployed = at(todo("deployed", delivery = TodoDelivery(state = "live", at = hoursAgo(2))).withKind("concern"))
        assertEquals(TriageSection.Other to TriageRule.Rest, deployed.section to deployed.rule)
    }

    @Test
    fun liveConcernDoesNotUseOrdinaryDeliveryStatusOrAcceptance() {
        val live = todo("live", delivery = TodoDelivery(state = "live", at = hoursAgo(2))).withKind("concern")
        assertFalse(live.deliveredAwaitingReview(lookup))
        assertEquals(TodoState.NotStarted, live.shownState(lookup))

        val seen = todo("seen-live", agents = listOf(agent("unseen")), delivery = TodoDelivery(state = "live", at = hoursAgo(2)))
            .copy(seenAt = minutesAgo(30)).withKind("concern")
        assertEquals(TriageSection.Other to TriageRule.Rest, at(seen).let { it.section to it.rule })
        assertTrue(seen.seenCanFile)
        assertFalse(seen.deliveredAwaitingReview(lookup))
    }

    @Test
    fun pendingReviewAndSupervisedConcernsStillRequireAcceptance() {
        val pending = todo("pending", supervision = Supervision(supervisor = "main", state = "pending_review"))
            .copy(seenAt = Instant.ofEpochMilli(now).toString()).withKind("concern")
        assertTrue(pending.deliveredAwaitingReview(lookup))
        assertFalse(pending.seenCanFile)
        assertEquals(TriageSection.JustDone to TriageRule.FinishedUnseen, at(pending).let { it.section to it.rule })

        val supervised = todo("supervised", agents = listOf(agent("unseen")), supervision = Supervision(supervisor = "main", state = "reviewing"))
            .copy(seenAt = Instant.ofEpochMilli(now).toString()).withKind("concern")
        assertFalse(supervised.seenCanFile)
        assertEquals(TriageSection.JustDone to TriageRule.FinishedUnseen, at(supervised).let { it.section to it.rule })
    }

    @Test
    fun r4ConcernKeepsFinishedUnseenAndOpenPrRules() {
        val unseen = at(todo("unseen", agents = listOf(agent("unseen"))).withKind("concern"))
        assertEquals(TriageSection.JustDone to TriageRule.FinishedUnseen, unseen.section to unseen.rule)
        val pr = at(todo("pr", updated = 300, agents = listOf(agent("old")), delivery = TodoDelivery(state = "pr"), prUrl = "https://example.com/pull/1", prStatus = openPr("https://example.com/pull/1")).withKind("concern"))
        assertEquals(TriageSection.Pr to TriageRule.PrOpen, pr.section to pr.rule)
    }

    @Test
    fun r5ConcernFinishedTurnSeenByTheOwnerStaysOtherOpen() {
        val td = todo("seen", agents = listOf(agent("unseen"))).copy(seenAt = minutesAgo(30)).withKind("concern")
        val t = at(td)
        assertEquals(TriageSection.Other to TriageRule.Rest, t.section to t.rule)
        assertTrue(td.countsAsOpen(t))
    }

    @Test
    fun r6ConcernIsNeverShelvedHoweverLongItIsQuiet() {
        val t = at(todo("quiet", updated = 100, agents = listOf(agent("old"))).withKind("concern"))
        assertEquals(TriageSection.Other to TriageRule.Rest, t.section to t.rule)
    }

    @Test
    fun r7ConcernWithNoEarlierSignalIsOtherOpen() {
        val t = at(todo("open", origin = null).withKind("concern"))
        assertEquals(TriageSection.Other to TriageRule.Rest, t.section to t.rule)
    }

    @Test
    fun closedConcernIsJustFinishedForADayThenDone() {
        val fresh = at(todo("fresh", doneAt = hoursAgo(3)).withKind("concern"))
        assertEquals(TriageSection.JustDone to TriageRule.Closed, fresh.section to fresh.rule)
        val old = at(todo("old", doneAt = hoursAgo(30), updated = 30).withKind("concern"))
        assertEquals(TriageSection.Done to TriageRule.Closed, old.section to old.rule)
    }

    @Test
    fun unknownTodoKindKeepsTheOrdinaryRules() {
        val t = at(todo("unknown", updated = 100, agents = listOf(agent("old"))).withKind("future-kind"))
        assertEquals(TriageSection.Shelved to TriageRule.Stale, t.section to t.rule)
    }

    @Test
    fun rowsOpenConcernAndRelatedOpenTaskStaySeparate() {
        val list = listOf(
            todo("concern", title = "Morning answer", planId = "p1").withKind("concern"),
            todo("task", title = "Morning work", planId = "p1"),
        )
        val r = triageTodos(list, groupLookup, now, zone = zone)
        assertEquals(2, r.rows(TriageSection.Other).size)
        assertEquals(listOf("concern", "task"), r.rows(TriageSection.Other).map { it.lead.id }.sorted())
        assertTrue(r.rows(TriageSection.Other).all { it.members.size == 1 })
    }

    @Test
    fun rowsTwoRelatedOpenConcernsStaySeparate() {
        val list = listOf(
            todo("concern-a", title = "Same concern", planId = "p1").withKind("concern"),
            todo("concern-b", title = "Same concern", planId = "p1").withKind("concern"),
        )
        val r = triageTodos(list, groupLookup, now, zone = zone)
        assertEquals(2, r.rows(TriageSection.Other).size)
        assertTrue(r.rows(TriageSection.Other).all { it.members.size == 1 })
    }

    @Test
    fun rowsClosedConcernGroupsAsToday() {
        val list = listOf(
            todo("concern", title = "Morning answer", doneAt = hoursAgo(2), planId = "p1").withKind("concern"),
            todo("task", title = "Morning work", doneAt = hoursAgo(3), planId = "p1"),
        )
        val r = triageTodos(list, groupLookup, now, zone = zone)
        assertEquals(listOf(listOf("concern", "task")), r.rows(TriageSection.JustDone).map { g -> g.members.map(Todo::id) })
    }

    // Shared case: concern_agent_running
    @Test fun concernAgentRunningSharedCase() {
        val t = atShared(
            sharedTodo(
                "concern-run", "Answer the backup question", "2026-10-01T00:00:00.000Z", "2026-10-20T00:00:00.000Z",
                agents = listOf(sharedAgent("run", "2026-11-10T10:00:00.000Z")),
            ),
            Session("m", "run", state = "working", status = "working", updatedAt = "2026-11-10T11:00:00.000Z"),
        )
        assertEquals(TriageSection.Working to TriageRule.AgentRunning, t.section to t.rule)
    }

    // Shared case: concern_failed_start_old
    @Test fun concernFailedStartOldSharedCase() {
        val t = atShared(sharedTodo(
            "concern-failed", "Answer the launch question", "2026-11-01T00:00:00.000Z", "2026-11-08T00:00:00.000Z",
            startError = "no launcher",
        ))
        assertEquals(TriageSection.Other to TriageRule.Rest, t.section to t.rule)
    }

    // Shared case: concern_closed_failed_start_old
    @Test fun concernClosedFailedStartOldSharedCase() {
        val t = atShared(sharedTodo(
            "concern-closed-failed", "Answer the closed launch question", "2026-11-01T00:00:00.000Z", "2026-11-08T00:00:00.000Z",
            doneAt = "2026-11-08T00:00:00.000Z", doneBy = "reported", startError = "no launcher",
        ))
        assertEquals(TriageSection.Done to TriageRule.FailedStart, t.section to t.rule)
    }

    // Shared case: concern_accepted
    @Test fun concernAcceptedSharedCase() {
        val t = atShared(sharedTodo(
            "concern-accepted", "Answer the review question", "2026-11-09T00:00:00.000Z", "2026-11-10T10:00:00.000Z",
            supervision = Supervision(
                supervisor = "main", doneWhen = emptyList(), state = "accepted",
                reviews = listOf(TaskReview(at = "2026-11-10T09:00:00.000Z", verdict = "accepted", checks = emptyList())),
            ),
        ))
        assertEquals(TriageSection.Other to TriageRule.Rest, t.section to t.rule)
    }

    // Shared case: concern_deployed
    @Test fun concernDeployedSharedCase() {
        val t = atShared(sharedTodo(
            "concern-deployed", "Answer the deployment question", "2026-11-09T00:00:00.000Z", "2026-11-10T10:00:00.000Z",
            delivery = TodoDelivery(state = "live", at = "2026-11-10T09:00:00.000Z"),
        ))
        assertEquals(TriageSection.Other to TriageRule.Rest, t.section to t.rule)
    }

    // Shared case: concern_finished_unseen
    @Test fun concernFinishedUnseenSharedCase() {
        val t = atShared(
            sharedTodo(
                "concern-unseen", "Answer the report question", "2026-11-09T00:00:00.000Z", "2026-11-10T10:00:00.000Z",
                agents = listOf(sharedAgent("unseen", "2026-11-10T09:00:00.000Z")),
            ),
            Session("m", "unseen", state = "unread", status = "idle", updatedAt = "2026-11-10T11:00:00.000Z"),
        )
        assertEquals(TriageSection.JustDone to TriageRule.FinishedUnseen, t.section to t.rule)
    }

    // Shared case: concern_pr_open
    @Test fun concernPrOpenSharedCase() {
        val t = atShared(sharedTodo(
            "concern-pr", "Answer the merge question", "2026-11-01T00:00:00.000Z", "2026-11-04T10:00:00.000Z",
            prUrl = "https://example.com/pull/7", prStatus = openPr("https://example.com/pull/7"), stage = "review",
        ))
        assertEquals(TriageSection.Pr to TriageRule.PrOpen, t.section to t.rule)
    }

    // Shared case: concern_finished_seen
    @Test fun concernFinishedSeenSharedCase() {
        val t = atShared(
            sharedTodo(
                "concern-seen", "Answer the seen question", "2026-11-09T00:00:00.000Z", "2026-11-10T10:00:00.000Z",
                agents = listOf(sharedAgent("unseen", "2026-11-10T09:00:00.000Z")), seenAt = "2026-11-10T11:30:00.000Z",
            ),
            Session("m", "unseen", state = "unread", status = "idle", updatedAt = "2026-11-10T11:00:00.000Z"),
        )
        assertEquals(TriageSection.Other to TriageRule.Rest, t.section to t.rule)
    }

    // Shared case: concern_stale
    @Test fun concernStaleSharedCase() {
        val t = atShared(
            sharedTodo(
                "concern-stale", "Answer the quiet question", "2026-10-01T00:00:00.000Z", "2026-10-20T12:00:00.000Z",
                agents = listOf(sharedAgent("old", "2026-10-02T00:00:00.000Z")),
            ),
            Session("m", "old", state = "idle", status = "idle", updatedAt = "2026-10-20T12:00:00.000Z"),
        )
        assertEquals(TriageSection.Other to TriageRule.Rest, t.section to t.rule)
    }

    // Shared case: concern_stale_ordinary_task_control
    @Test fun concernStaleOrdinaryTaskControlSharedCase() {
        val t = atShared(
            sharedTodo(
                "task-stale-control", "Answer the quiet question", "2026-10-01T00:00:00.000Z", "2026-10-20T12:00:00.000Z",
                kind = "", agents = listOf(sharedAgent("old", "2026-10-02T00:00:00.000Z")),
            ),
            Session("m", "old", state = "idle", status = "idle", updatedAt = "2026-10-20T12:00:00.000Z"),
        )
        assertEquals(TriageSection.Shelved to TriageRule.Stale, t.section to t.rule)
    }

    // Shared case: concern_rest
    @Test fun concernRestSharedCase() {
        val t = atShared(sharedTodo(
            "concern-rest", "Answer the ordinary question", "2026-11-09T00:00:00.000Z", "2026-11-10T11:00:00.000Z",
        ))
        assertEquals(TriageSection.Other to TriageRule.Rest, t.section to t.rule)
    }

    // Shared case: concern_closed_fresh
    @Test fun concernClosedFreshSharedCase() {
        val t = atShared(sharedTodo(
            "concern-closed-fresh", "Answer the fresh report question", "2026-11-09T00:00:00.000Z", "2026-11-10T10:00:00.000Z",
            doneAt = "2026-11-10T10:00:00.000Z", doneBy = "reported",
        ))
        assertEquals(TriageSection.JustDone to TriageRule.Closed, t.section to t.rule)
    }

    // Shared case: concern_closed_old
    @Test fun concernClosedOldSharedCase() {
        val t = atShared(sharedTodo(
            "concern-closed-old", "Answer the old report question", "2026-11-01T00:00:00.000Z", "2026-11-08T10:00:00.000Z",
            doneAt = "2026-11-08T10:00:00.000Z", doneBy = "reported",
        ))
        assertEquals(TriageSection.Done to TriageRule.Closed, t.section to t.rule)
    }

    // Shared case: concern_open_related_task
    @Test fun concernOpenRelatedTaskSharedCase() {
        val r = triageTodos(listOf(
            sharedTodo(
                "concern-open", "Answer the launch concern", "2026-11-09T00:00:00.000Z", "2026-11-10T11:00:00.000Z",
                planId = "plan-concern",
            ),
            sharedTodo(
                "task-open", "Implement the launch task", "2026-11-09T00:00:00.000Z", "2026-11-10T10:00:00.000Z",
                kind = "", planId = "plan-concern",
            ),
        ), lookupOf(), now, quiet = true, zone = zone)
        assertEquals(listOf(listOf("concern-open"), listOf("task-open")), r.rows(TriageSection.Other).map { it.members.map(Todo::id) })
        assertEquals(Triple(2, 0, 0), Triple(r.open, r.shelved, r.finished))
    }

    // Shared case: concern_open_related_concern
    @Test fun concernOpenRelatedConcernSharedCase() {
        val r = triageTodos(listOf(
            sharedTodo(
                "concern-first", "Answer the first concern", "2026-11-09T00:00:00.000Z", "2026-11-10T11:00:00.000Z",
                planId = "plan-two-concerns",
            ),
            sharedTodo(
                "concern-second", "Answer the second concern", "2026-11-09T00:00:00.000Z", "2026-11-10T10:00:00.000Z",
                planId = "plan-two-concerns",
            ),
        ), lookupOf(), now, quiet = true, zone = zone)
        assertEquals(listOf(listOf("concern-first"), listOf("concern-second")), r.rows(TriageSection.Other).map { it.members.map(Todo::id) })
        assertEquals(Triple(2, 0, 0), Triple(r.open, r.shelved, r.finished))
    }

    // Shared case: concern_closed_related_todo
    @Test fun concernClosedRelatedTodoSharedCase() {
        val r = triageTodos(listOf(
            sharedTodo(
                "concern-closed", "Answer the reported concern", "2026-11-09T00:00:00.000Z", "2026-11-10T11:00:00.000Z",
                doneAt = "2026-11-10T11:00:00.000Z", doneBy = "reported", planId = "plan-closed-concern",
            ),
            sharedTodo(
                "task-closed", "Finish the reported task", "2026-11-09T00:00:00.000Z", "2026-11-10T10:00:00.000Z",
                kind = "", doneAt = "2026-11-10T10:00:00.000Z", planId = "plan-closed-concern",
            ),
        ), lookupOf(), now, quiet = true, zone = zone)
        assertEquals(listOf(listOf("concern-closed", "task-closed")), r.rows(TriageSection.JustDone).map { it.members.map(Todo::id) })
        assertEquals(Triple(0, 0, 2), Triple(r.open, r.shelved, r.finished))
    }

    // Shared case: concern_stale_next_to_task
    @Test fun concernStaleNextToTaskSharedCase() {
        val r = triageTodos(listOf(
            sharedTodo(
                "stale-concern", "Answer the quiet concern", "2026-10-20T12:00:00.000Z", "2026-10-20T12:00:00.000Z",
            ),
            sharedTodo(
                "stale-task", "Finish the quiet task", "2026-10-20T12:00:00.000Z", "2026-10-20T12:00:00.000Z", kind = "",
            ),
        ), lookupOf(), now, quiet = true, zone = zone)
        assertEquals(listOf(listOf("stale-concern")), r.rows(TriageSection.Other).map { it.members.map(Todo::id) })
        assertEquals(listOf(listOf("stale-task")), r.rows(TriageSection.Shelved).map { it.members.map(Todo::id) })
        assertEquals(Triple(1, 1, 0), Triple(r.open, r.shelved, r.finished))
    }

    @Test fun acceptedConcernAtRestKeepsOpenState() {
        val td = todo("accepted", agents = listOf(agent("idle")), supervision = acceptedReopened()).withKind("concern")
        assertEquals(TriageStatus.FollowUp, td.triageStatus(lookup))
        assertTrue(td.checksAsOpen(at(td)))
        assertFalse(td.acceptedAtRest(lookup))
        assertTrue("ordinary accepted task remains the control", td.copy(kind = "").acceptedAtRest(lookup))
    }

    @Test
    fun anAgentAskingIsWaitingWhateverElseHolds() {
        val t = at(todo("t", agents = listOf(agent("ask"), agent("run"))))
        assertEquals(TriageSection.Waiting, t.section)
        assertEquals(TriageRule.AgentAsks, t.rule)
    }

    @Test
    fun aDecisionForTheOwnerIsWaiting() {
        assertEquals(TriageRule.OwnerDecides, at(todo("t", agents = listOf(agent("idle")), supervision = Supervision(supervisor = "c", state = "needs_user"))).rule)
        assertEquals(TriageSection.Waiting, at(todo("t", ask = TodoAsk(outcome = "decide", verdict = "v", askedAt = hoursAgo(1)))).section)
    }

    @Test
    fun pleaseVerifyOrADoneConclusionIsJustFinishedNotWaiting() {
        val idle = listOf(agent("idle"))
        val verify = at(todo("t", agents = idle, ask = TodoAsk(verdict = "done, deployed: verify below", askedAt = hoursAgo(1))))
        assertEquals(TriageSection.JustDone, verify.section)
        assertEquals(TriageRule.FinishedUnseen, verify.rule)
        assertEquals(TriageSection.JustDone, at(todo("t", agents = idle, ask = TodoAsk(outcome = "done", verdict = "done", askedAt = hoursAgo(1)))).section)
    }

    @Test
    fun anAgentRunningIsWorkingEvenOnAnOldTodo() {
        val t = at(todo("t", updated = 400, agents = listOf(agent("run"))))
        assertEquals(TriageSection.Working, t.section)
        assertEquals(TriageRule.AgentRunning, t.rule)
    }

    @Test
    fun aClosedTodoIsImmediatelyDone() {
        assertEquals(TriageSection.Done, at(todo("t", doneAt = hoursAgo(3))).section)
        assertEquals(TriageSection.Done, at(todo("t", doneAt = hoursAgo(30), updated = 30)).section)
    }

    @Test
    fun anAcceptedTaskReopenedByMoreWorkIsFinishedNotFollowUp() {
        val td = todo("t", agents = listOf(agent("idle")), reopenedAt = hoursAgo(4), supervision = acceptedReopened())
        val t = at(td)
        assertEquals(TriageSection.Done, t.section)
        assertEquals(TriageRule.Accepted, t.rule)
        assertTrue(td.acceptedAtRest(lookup))
        assertEquals(ReviewStage.Accepted, td.reviewStage)
    }

    @Test
    fun anOwnerDecisionOutranksAcceptedAtRest() {
        val accepted = todo("t", agents = listOf(agent("idle")), reopenedAt = hoursAgo(4), supervision = acceptedReopened())
        assertTrue(accepted.acceptedAtRest(lookup, now))
        assertEquals(TodoState.Done, accepted.shownState(lookup, now))
        for (outcome in listOf("decide", "cannot")) {
            val waiting = accepted.copy(ask = TodoAsk(outcome = outcome, verdict = "needs_user", askedAt = hoursAgo(1)))
            assertTrue(waiting.waitsForOwner())
            assertEquals(TriageRule.OwnerDecides, at(waiting).rule)
            assertFalse("accepted must not hide an owner decision ($outcome)", waiting.acceptedAtRest(lookup, now))
            assertEquals(TodoState.NeedsYou, waiting.shownState(lookup, now))
        }
    }

    @Test
    fun aDismissedOwnerDecisionDoesNotProjectNeedsYou() {
        val reviewed = Supervision(supervisor = "c", state = "needs_user",
            reviews = listOf(TaskReview(at = hoursAgo(1), verdict = "needs_user")))
        val waiting = todo("t", agents = listOf(agent("idle")), supervision = reviewed,
            prUrl = "https://example.com/pull/1", prStatus = openPr("https://example.com/pull/1"))
        val dismissed = waiting.copy(decisionReview = DecisionReview(reviewAt = hoursAgo(1), dismissalId = "dismiss-current"))
        assertTrue(dismissed.decisionDismissed())
        assertFalse(dismissed.waitsForOwner())
        assertEquals(TriageRule.PrOpen, at(dismissed).rule)
        assertEquals(TodoState.FollowUp, dismissed.shownState(lookup, now))
        val working = dismissed.copy(agents = listOf(agent("run")))
        assertFalse(working.waitsForOwner())
        assertEquals(TodoState.Working, working.shownState(lookup, now))

        assertTrue(waiting.waitsForOwner())
        assertEquals(TodoState.NeedsYou, waiting.shownState(lookup, now))
        val newer = dismissed.copy(supervision = reviewed.copy(reviews = listOf(TaskReview(at = hoursAgo(0), verdict = "needs_user"))))
        assertFalse(newer.decisionDismissed())
        assertTrue(newer.waitsForOwner())
        assertEquals(TodoState.NeedsYou, newer.shownState(lookup, now))
    }

    @Test
    fun aDismissedReviewDecisionKeepsIndependentDeliveryApproval() {
        val reviewed = Supervision(supervisor = "c", state = "needs_user",
            reviews = listOf(TaskReview(at = hoursAgo(1), verdict = "needs_user")))
        val dismissed = todo("t", agents = listOf(agent("idle")), supervision = reviewed,
            delivery = TodoDelivery(state = "pr", gate = "owner", unblock = "user"))
            .copy(decisionReview = DecisionReview(reviewAt = hoursAgo(1), dismissalId = "dismiss-current"))
        assertTrue(dismissed.decisionDismissed())
        assertEquals(HoldUnblock.User, dismissed.hold?.unblock)
        assertEquals(TriageSection.Waiting to TriageRule.OwnerDecides, at(dismissed).let { it.section to it.rule })
        assertEquals("dismissal-only control", TriageRule.Rest, at(dismissed.copy(delivery = null)).rule)
    }

    @Test
    fun aDismissedOwnerDecisionRestoresAcceptedAtRest() {
        val accepted = todo("t", agents = listOf(agent("idle")), reopenedAt = hoursAgo(4), supervision = acceptedReopened())
        val waiting = listOf("decide", "cannot").map { outcome ->
            accepted.copy(ask = TodoAsk(outcome = outcome, verdict = "needs_user", askedAt = hoursAgo(1)))
        }
        val dismissed = waiting.map {
            it.copy(decisionReview = DecisionReview(
                reviewAt = accepted.supervision!!.latestReview!!.at, dismissalId = "dismiss-current-round",
            ))
        }
        assertEquals(listOf(false, false), dismissed.map { it.waitsForOwner() })
        assertEquals(listOf(true, true), dismissed.map { it.decisionDismissed() })
        assertEquals(listOf(TriageRule.Accepted, TriageRule.Accepted), dismissed.map { at(it).rule })
        assertEquals("dismissed decide/cannot restore accepted at rest", listOf(true, true),
            dismissed.map { it.acceptedAtRest(lookup, now) })
        assertEquals(listOf(TodoState.Done, TodoState.Done), dismissed.map { it.shownState(lookup, now) })
        assertEquals("genuinely open decide/cannot still suppress accepted", listOf(false, false),
            waiting.map { it.acceptedAtRest(lookup, now) })
        assertEquals(listOf(TodoState.NeedsYou, TodoState.NeedsYou), waiting.map { it.shownState(lookup, now) })
    }

    @Test
    fun anAcceptedTaskWhoseAgentRunsAgainIsWorking() {
        val td = todo("t", agents = listOf(agent("run")), supervision = acceptedReopened())
        assertEquals(TriageSection.Working, at(td).section)
        assertFalse(td.acceptedAtRest(lookup))
    }

    @Test
    fun anAcceptedTaskAtUsageLimitStaysAcceptedAtRest() {
        val limitedSession = session("limited", "unread").copy(
            turnError = "usage limit reached", turnErrorKind = "usage_limit",
            turnErrorResetsAt = Instant.now().plusSeconds(300).toString(),
        )
        val limitedLookup = lookupOf(limitedSession)
        val td = todo("t", agents = listOf(agent("limited")), reopenedAt = hoursAgo(4), supervision = acceptedReopened())
        assertEquals(TriageStatus.UsageLimit, td.triageStatus(limitedLookup, now))
        assertTrue(td.acceptedAtRest(limitedLookup))
        assertEquals(TodoState.Done, td.shownState(limitedLookup, now))
        assertFalse("working is not at rest", td.copy(agents = listOf(agent("run"))).acceptedAtRest(lookup))
        assertFalse("needs-you is not at rest", td.copy(agents = listOf(agent("ask"))).acceptedAtRest(lookup))
        val expiredLookup = lookupOf(limitedSession.copy(turnErrorResetsAt = Instant.ofEpochMilli(now - 11 * 60_000L).toString()))
        assertEquals(TriageStatus.Interrupted, td.triageStatus(expiredLookup, now))
        assertFalse("an expired reset is interrupted, not accepted at rest", td.acceptedAtRest(expiredLookup))
    }

    @Test
    fun aLiveDeliveryWaitsForReviewUntilAccepted() {
        val recent = todo("t", delivery = TodoDelivery(state = "live", at = hoursAgo(2)))
        assertEquals(TriageSection.JustDone, at(recent).section)
        assertEquals(TriageRule.FinishedUnseen, at(recent).rule)
        assertTrue(recent.countsAsOpen(at(recent)))
        val old = todo("t", updated = 300, delivery = TodoDelivery(state = "live", at = hoursAgo(300)))
        assertEquals(TriageSection.JustDone, at(old).section)
        assertEquals(TriageRule.FinishedUnseen, at(old).rule)
        assertTrue(old.countsAsOpen(at(old)))
    }

    @Test
    fun aFinishedTurnNobodySawIsJustFinished() {
        val t = at(todo("t", agents = listOf(agent("unseen"))))
        assertEquals(TriageSection.JustDone, t.section)
        assertEquals(TriageRule.FinishedUnseen, t.rule)
    }

    @Test
    fun anOpenPRIsPrToMergeHoweverOldAMergedOneIsNot() {
        val old = listOf(agent("old"))
        assertEquals(TriageSection.Pr, at(todo("t", updated = 300, agents = old, delivery = TodoDelivery(state = "pr"), prUrl = "https://example.com/pull/1", prStatus = openPr("https://example.com/pull/1"))).section)
        assertEquals(TriageSection.Pr, at(todo("t", updated = 300, agents = old, prUrl = "https://example.com/pull/1", prStatus = openPr("https://example.com/pull/1"))).section)
        assertEquals(TriageSection.Shelved, at(todo("t", updated = 300, agents = old, prUrl = "https://example.com/pull/1", delivery = TodoDelivery(state = "merged"))).section)
    }

    @Test
    fun prFactsMustBePresentFreshOpenMatchingAndNonDraft() {
        val url = "https://example.com/pull/1"
        val old = listOf(agent("old"))
        fun section(facts: TodoPRStatus?) = at(todo("t", updated = 300, agents = old, delivery = TodoDelivery(state = "pr"), prUrl = url, prStatus = facts)).section
        assertEquals(TriageSection.Pr, section(openPr(url)))
        assertEquals(TriageSection.Shelved, section(null))
        for (facts in listOf(
            openPr(url).copy(state = "merged"),
            openPr(url).copy(draft = true),
            openPr(url).copy(checkedAt = hoursAgo(1)),
            openPr("https://example.com/pull/2"),
        )) {
            assertEquals(TriageSection.Shelved, section(facts))
        }
    }

    @Test
    fun aTaskWithNoMoveFor48HoursIsShelvedANewerAgentMoveKeepsIt() {
        val t = at(todo("t", updated = 100, agents = listOf(agent("old"))))
        assertEquals(TriageSection.Shelved, t.section)
        assertEquals(TriageRule.Stale, t.rule)
        assertEquals(TriageSection.Other, at(todo("t", updated = 100, agents = listOf(agent("idle")))).section)
    }

    /** #3331: the assistant's Progress entry counts by the same bucket as the To-dos page's "Not done". */
    @Test
    fun bucketIsTheHeaderCountShelvedAndAcceptedAreNotOpen() {
        val stale = todo("stale", updated = 100, agents = listOf(agent("old")))
        val accepted = todo("accepted", agents = listOf(agent("idle")), reopenedAt = hoursAgo(4), supervision = acceptedReopened())
        val open = todo("open", agents = listOf(agent("idle")))
        val done = todo("done", doneAt = hoursAgo(3))
        assertEquals(TodoBucket.Shelved, stale.bucket(lookup, now))
        assertEquals(TodoBucket.Open, stale.bucket(lookup, now, restoredAt = now - 3_600_000L))
        assertEquals(TodoBucket.Closed, accepted.bucket(lookup, now))
        assertEquals(TodoBucket.Open, open.bucket(lookup, now))
        assertEquals(TodoBucket.Closed, done.bucket(lookup, now))
        val all = listOf(stale, accepted, open, done)
        val header = triageTodos(all, lookup, now, zone = zone)
        assertEquals(header.open, all.count { it.bucket(lookup, now) == TodoBucket.Open })
        assertEquals(header.shelved, all.count { it.bucket(lookup, now) == TodoBucket.Shelved })
    }

    @Test
    fun cancelledIsNeitherOpenNorFinished() {
        val cancelled = todo("cancelled").copy(cancelledAt = hoursAgo(1))
        val alone = triageTodos(listOf(cancelled), groupLookup, now, zone = zone)
        assertEquals(0, alone.open)
        assertEquals(1, alone.cancelled)
        assertEquals(0, alone.finished)
        val controls = triageTodos(listOf(cancelled, todo("open"), todo("done", doneAt = hoursAgo(2))), groupLookup, now, zone = zone)
        assertEquals(1, controls.open)
        assertEquals(1, controls.cancelled)
        assertEquals(1, controls.finished)
    }

    @Test
    fun mainTriageUsesTheCurrentTimeForTheUsageLimitGrace() {
        val limited = todo("limited", agents = listOf(agent("limited")))
        val expiredSession = session("limited", "unread").copy(
            turnError = "usage limit reached", turnErrorKind = "usage_limit",
            turnErrorResetsAt = Instant.ofEpochMilli(now - 11 * 60_000L).toString(),
        )
        val expiredLookup = lookupOf(expiredSession)
        assertEquals(TriageStatus.Interrupted, limited.triageStatus(expiredLookup, now))
        assertEquals(TriageSection.Waiting, limited.triage(expiredLookup, now, zone = zone).section)

        val recentLookup = lookupOf(expiredSession.copy(turnErrorResetsAt = Instant.ofEpochMilli(now - 5 * 60_000L).toString()))
        assertEquals(TriageStatus.UsageLimit, limited.triageStatus(recentLookup, now))

        val defaultExpired = expiredSession.copy(turnErrorResetsAt = Instant.now().minusSeconds(11 * 60).toString())
        assertEquals("the default is real current time, not epoch zero", TriageStatus.Interrupted,
            limited.triageStatus(lookupOf(defaultExpired)))
    }

    @Test
    fun theUsersOwnTodoWithoutAgentsNeverShelvesNorOneDueAhead() {
        assertEquals(TriageSection.Other, at(todo("t", updated = 100, origin = null)).section)
        assertEquals(TriageSection.Other, at(todo("t", updated = 100, agents = listOf(agent("old")), dueDate = "2026-12-01")).section)
    }

    @Test
    fun aRestoreCountsAsAMove() {
        val td = todo("t", updated = 100, agents = listOf(agent("old")))
        assertEquals(TriageSection.Other, at(td, now - 3_600_000L).section)
        assertEquals(TriageSection.Shelved, at(td, now - SHELVE_AFTER_MS - 1).section)
    }

    @Test
    fun lastActivityTakesTheLatestOfTodoSupervisionAndAgents() {
        val td = todo("t", updated = 50, agents = listOf(agent("idle")),
            supervision = Supervision(supervisor = "c", progress = TaskProgress(kind = "progress", text = "x", at = hoursAgo(1))))
        assertEquals(now - 3_600_000L, td.lastActivity(lookup))
    }

    @Test
    fun criteriaAddedAfterAStandingAcceptanceAreUncheckedAndNotCounted() {
        val sup = acceptedReopened()
        assertEquals(List(5) { CheckMark.Met } + List(3) { CheckMark.Unchecked }, sup.checklist().map { it.mark })
        assertEquals(5 to 5, sup.checkCount())
    }

    @Test
    fun beforeAnyAcceptanceTheyStayOpenAndCount() {
        val sup = acceptedReopened().copy(state = "rework", reviews = listOf(TaskReview(at = hoursAgo(5), verdict = "rework", checks = listOf(ReviewCheck("c1", true)))))
        assertEquals(7, sup.checklist().count { it.mark == CheckMark.Open })
        assertEquals(1 to 8, sup.checkCount())
    }

    private val groupLookup = lookupOf(session("run", "working"), session("old", "idle", 100), session("old2", "idle", 120))

    @Test
    fun mergesTheSameIssuePrTitleOrPlanLedByTheMostUrgent() {
        val list = listOf(
            todo("fix", title = "Fix #412 lost pushes", agents = listOf(agent("run"))),
            todo("test", title = "#412 lost pushes: regression test", updated = 100, agents = listOf(agent("old"))),
            todo("pr-a", title = "A", prUrl = "https://example.com/pull/9", prStatus = openPr("https://example.com/pull/9"), agents = listOf(agent("old2")), updated = 120),
            todo("pr-b", title = "B", prUrl = "https://example.com/pull/9/", prStatus = openPr("https://example.com/pull/9/"), agents = listOf(agent("old2")), updated = 121),
            todo("lone", title = "Something else", updated = 1),
        )
        val r = triageTodos(list, groupLookup, now, zone = zone)
        assertEquals(listOf("fix" to listOf("fix", "test")), r.rows(TriageSection.Working).map { it.lead.id to it.members.map(Todo::id) })
        assertEquals(listOf(listOf("pr-a", "pr-b")), r.rows(TriageSection.Pr).map { it.members.map(Todo::id) })
        assertEquals(listOf("lone"), r.rows(TriageSection.Other).map { it.lead.id })
    }

    @Test
    fun mergesTasksOfOnePlan() {
        val r = triageTodos(listOf(todo("a", title = "One", planId = "p1"), todo("b", title = "Two", planId = "p1")), groupLookup, now, zone = zone)
        assertEquals(listOf(listOf("a", "b")), r.rows(TriageSection.Other).map { it.members.map(Todo::id).sorted() })
    }

    @Test
    fun neverMergesAClosedTodoIntoAnOpenOnesRow() {
        val r = triageTodos(listOf(todo("a", title = "Same title"), todo("b", title = "Same title", doneAt = hoursAgo(1))), groupLookup, now, zone = zone)
        assertEquals(1, r.rows(TriageSection.Other).size)
        assertEquals(0, r.rows(TriageSection.JustDone).size)
        assertEquals(1, r.rows(TriageSection.Done).size)
    }

    @Test
    fun countsAFailedStartWaitingOnTheOwnerAsOpen() {
        val r = triageTodos(listOf(todo("f", startError = "no launcher", doneAt = hoursAgo(1))), groupLookup, now, zone = zone)
        assertEquals(listOf("f"), r.rows(TriageSection.Waiting).map { it.lead.id })
        assertEquals(1, r.open)
    }

    /** spec/fixtures/triage/groups.json `failed_start_superseded_by_finished_work` (#3713). */
    @Test
    fun movesAFailedStartOutOfWaitingOnceItsRowsWorkFinished() {
        val failed = todo("f1", title = "Deploy the self-hosted panel", doneAt = hoursAgo(1), startError = "no launcher", planId = "p1")
        val done = todo("done1", title = "Panel: verify it on the hub", doneAt = hoursAgo(2), planId = "p1")
        val other = todo("f2", title = "Rotate the backup keys", doneAt = hoursAgo(3), startError = "no launcher", planId = "p2")
        val twin = todo("f3", title = "Rotate the backup keys", doneAt = hoursAgo(4), startError = "no launcher")
        val list = listOf(failed, done, other, twin)
        assertEquals(setOf("f1"), supersededFailedStarts(list))
        val r = triageTodos(list, groupLookup, now, zone = zone)
        assertEquals(0, r.rows(TriageSection.JustDone).size)
        assertEquals(listOf(listOf("done1", "f1")), r.rows(TriageSection.Done).map { g -> g.members.map { it.id } })
        assertEquals(listOf(listOf("f2", "f3")), r.rows(TriageSection.Waiting).map { g -> g.members.map { it.id } })
        assertEquals(2, r.open)
        assertEquals(TodoBucket.Closed, failed.bucket(groupLookup, now, superseded = true))
        assertEquals(TodoBucket.Open, failed.bucket(groupLookup, now))
        // the checkbox: open while it waits on the owner, done once superseded
        assertEquals(true, other.checksAsOpen(r.rows(TriageSection.Waiting)[0].triage["f2"]))
        assertEquals(false, failed.checksAsOpen(r.rows(TriageSection.Done)[0].triage["f1"]))
    }

    @Test
    fun listsQuietTasksOnThePageLeavesThemOutForABadge() {
        val list = listOf(todo("q", quiet = true))
        assertEquals(1, triageTodos(list, groupLookup, now, zone = zone).open)
        assertEquals(0, triageTodos(list, groupLookup, now, quiet = false, zone = zone).open)
    }

    @Test
    fun countsShelvedAndFinishedApartFromOpen() {
        val r = triageTodos(
            listOf(
                todo("run", agents = listOf(agent("run"))),
                todo("stale", updated = 100, agents = listOf(agent("old"))),
                todo("closed", doneAt = hoursAgo(2)),
                todo("accepted", supervision = acceptedReopened(), agents = listOf(agent("old"))),
                todo("quiet", quiet = true),
            ),
            groupLookup, now, quiet = false, zone = zone,
        )
        assertEquals(listOf(1, 1, 2), listOf(r.open, r.shelved, r.finished))
    }

    @Test
    fun relatedKeysIgnoreCaseSpacesPunctuationAndATrailingSlash() {
        assertEquals(
            listOf("pr:https://example.com/pull/3", "title:fixthewidget"),
            todo("a", title = "Fix: the  Widget!", prUrl = "https://Example.com/pull/3/").relatedKeys(),
        )
    }

    // #5707 + #5732: Seen files ordinary turns, never closure, acceptance or an unaccepted delivery
    private fun minutesAgo(m: Int) = Instant.ofEpochMilli(now - m * 60_000L).toString()

    @Test
    fun seenFilesOrdinaryTurnsButNotAcceptanceOrAnUnacceptedDelivery() {
        val closed = { seenMinutesAgo: Int -> at(todo("t", doneAt = hoursAgo(3)).copy(seenAt = minutesAgo(seenMinutesAgo))) }
        assertEquals(TriageSection.Done, closed(120).section)
        assertEquals(TriageRule.Closed, closed(120).rule)
        assertEquals(TriageSection.Done, closed(180).section)
        assertEquals(TriageSection.Done, closed(240).section)
        val accepted = at(todo("t", supervision = acceptedReopened(), agents = listOf(agent("idle"))).copy(seenAt = hoursAgo(4)))
        assertEquals(TriageSection.Done to TriageRule.Accepted, accepted.section to accepted.rule)
        val deployed = at(todo("t", delivery = TodoDelivery(state = "live", at = hoursAgo(2))).copy(seenAt = hoursAgo(1)))
        assertEquals(TriageSection.JustDone to TriageRule.FinishedUnseen, deployed.section to deployed.rule)
        val supervised = at(todo("t", agents = listOf(agent("unseen")), supervision = Supervision(supervisor = "main")).copy(seenAt = minutesAgo(30)))
        assertEquals(TriageSection.JustDone to TriageRule.FinishedUnseen, supervised.section to supervised.rule)
        assertTrue(todo("ordinary", agents = listOf(agent("unseen"))).seenCanFile)
        assertFalse(todo("supervised", agents = listOf(agent("unseen")), supervision = Supervision(supervisor = "main")).seenCanFile)
        assertFalse(todo("live", delivery = TodoDelivery(state = "live", at = hoursAgo(2))).seenCanFile)
    }

    @Test
    fun seenAwaitingReviewStaysOpenButIsNotNewInFinished() {
        val unseen = todo("live", delivery = TodoDelivery(state = "live", at = hoursAgo(2)))
        assertTrue(unseen.showsJustFinished(at(unseen), now))

        val seen = unseen.copy(seenAt = hoursAgo(1))
        val placed = at(seen)
        assertEquals(TriageSection.JustDone to TriageRule.FinishedUnseen, placed.section to placed.rule)
        assertFalse(seen.showsJustFinished(placed, now))
    }

    @Test
    fun aFinishedTurnSeenSinceItLastMovedIsDoneUnlessItsPrIsOpen() {
        // the unseen session last moved an hour ago
        val seen = at(todo("t", agents = listOf(agent("unseen"))).copy(seenAt = minutesAgo(30)))
        assertEquals(TriageSection.Done to TriageRule.Seen, seen.section to seen.rule)
        val movedSince = at(todo("t", agents = listOf(agent("unseen"))).copy(seenAt = hoursAgo(2)))
        assertEquals(TriageSection.JustDone to TriageRule.FinishedUnseen, movedSince.section to movedSince.rule)
        val pr = at(todo("t", agents = listOf(agent("unseen")), prUrl = "https://example.com/pull/1", prStatus = openPr("https://example.com/pull/1")).copy(seenAt = minutesAgo(30)))
        assertEquals(TriageSection.Pr to TriageRule.PrOpen, pr.section to pr.rule)
        // seen, it is no longer open work
        assertEquals(TodoBucket.Closed, todo("t", agents = listOf(agent("unseen"))).copy(seenAt = minutesAgo(30)).bucket(lookup, now))
        assertEquals(TodoBucket.Open, todo("t", agents = listOf(agent("unseen"))).bucket(lookup, now))
    }

    @Test
    fun parkedOpenPrIsNotAwaitingAcceptance() {
        // A parked verdict is the supervisor's reading of the latest turn. An unread session left behind must not hide
        // its fresh open PR behind the supervised acceptance rule; Apple and the web already exempt parked here.
        val parked = at(
            todo(
                "t",
                agents = listOf(agent("unseen")),
                supervision = Supervision(supervisor = "main", state = "parked"),
                prUrl = "https://example.com/pull/1",
                prStatus = openPr("https://example.com/pull/1"),
            ).copy(seenAt = minutesAgo(30)),
        )
        assertEquals(TriageSection.Pr to TriageRule.PrOpen, parked.section to parked.rule)
    }

    @Test
    fun unfinishedSectionsComeFirstAndOpenShelvedAndDoneFoldedDoneLast() {
        assertEquals(
            listOf(TriageSection.Waiting, TriageSection.Working, TriageSection.Pr, TriageSection.Other, TriageSection.JustDone, TriageSection.Shelved, TriageSection.Done, TriageSection.Cancelled),
            TriageSection.entries.toList(),
        )
        assertEquals(listOf(TriageSection.Shelved, TriageSection.Done, TriageSection.Cancelled), TriageSection.entries.filter { it.folded })
    }

    @Test
    fun unfinishedToDosLeadARowAndComeBeforeJustFinishedSeenOnesAreDone() {
        val look = lookupOf(session("unseen", "unread"), session("unseen2", "unread"), session("idle", "idle", 2))
        val r = triageTodos(
            listOf(
                todo("fix", title = "Fix #512 crash on launch", agents = listOf(agent("unseen"))),
                todo("test", title = "#512 crash: add a regression test", updated = 2, agents = listOf(agent("idle"))),
                todo("notes", title = "Write the release notes", updated = 3, doneAt = hoursAgo(3)).copy(seenAt = minutesAgo(150)),
                todo("speed", title = "Speed up the web page", agents = listOf(agent("unseen2"))).copy(seenAt = minutesAgo(30)),
                todo("logs", title = "Rotate the logs", updated = 3, doneAt = hoursAgo(3)),
            ),
            look, now, zone = zone,
        )
        assertEquals(listOf(listOf("test", "fix")), r.rows(TriageSection.Other).map { g -> g.members.map { it.id } })
        assertEquals(0, r.rows(TriageSection.JustDone).size)
        assertEquals(listOf("speed", "logs", "notes"), r.rows(TriageSection.Done).map { it.lead.id })
        assertEquals(listOf(2, 0, 3), listOf(r.open, r.shelved, r.finished))
    }

    @Test
    fun aRestoreDropsOnesThatWouldHaveShelvedAgain() {
        val kept = withRestore(mapOf("old" to now - SHELVE_AFTER_MS - 1), "new", now)
        assertEquals(mapOf("new" to now), kept)
    }

    @Test
    fun supersededFailedStartsEarlyExitsAndStripsUrlQueryOrFragment() {
        // No failed starts or no completed non-failed todos -> empty set via early exit.
        assertEquals(emptySet<String>(), supersededFailedStarts(listOf(todo("open", prUrl = "https://example.com/pull/42"))))
        assertEquals(
            emptySet<String>(),
            supersededFailedStarts(listOf(todo("failed", startError = "boom", prUrl = "https://example.com/pull/42#discussion"))),
        )
        // Matching PR URL after stripping #fragment and ?query supersedes the closed failed start.
        val failed = todo("failed", startError = "boom", doneAt = hoursAgo(3), prUrl = "https://example.com/pull/42#issuecomment-1")
        val done = todo("done", doneAt = hoursAgo(2), prUrl = "https://example.com/pull/42?w=1")
        val unrelated = todo("other-failed", startError = "boom", doneAt = hoursAgo(3), prUrl = "https://example.com/pull/99?tab=files")
        assertEquals(setOf("failed"), supersededFailedStarts(listOf(failed, done, unrelated)))
    }

    @Test fun restStatusCoversOpenPRsParkedAndConcernsWithoutOverridingActiveOrDraft() {
        val fresh = minutesAgo(5)
        val stale = hoursAgo(2)
        val prUrl = "https://github.com/org/repo/pull/7"
        fun withPr(
            draft: Boolean = false,
            state: String = "open",
            review: String = "approved",
            checks: String = "success",
            conflicts: Boolean = false,
            checkedAt: String = fresh,
        ) = todo("pr", prUrl = prUrl, prStatus = TodoPRStatus(prUrl, state, draft, review, checks, conflicts, checkedAt))

        assertEquals(TodoRestStatus.PrWaitingMerge, withPr().restStatus(lookup, now))
        assertEquals(TodoRestStatus.PrInReview, withPr(review = "required").restStatus(lookup, now))
        assertEquals(TodoRestStatus.PrChecksPending, withPr(review = "pending", checks = "pending").restStatus(lookup, now))
        assertEquals(TodoRestStatus.PrChecksFailed, withPr(checks = "failure").restStatus(lookup, now))
        assertEquals(TodoRestStatus.PrChangesRequested, withPr(review = "changes_requested").restStatus(lookup, now))
        assertEquals(TodoRestStatus.PrConflicts, withPr(conflicts = true, review = "changes_requested", checks = "failure").restStatus(lookup, now))
        // Draft, stale, merged, closed stay null (standard NotStarted).
        assertNull(withPr(draft = true).restStatus(lookup, now))
        assertNull(withPr(checkedAt = stale).restStatus(lookup, now))
        assertNull(withPr(state = "merged").restStatus(lookup, now))
        assertNull(withPr(state = "closed").restStatus(lookup, now))

        // Projected TodoWait (including bound repository PR waits) maps cleanly.
        val waitPr = todo("wait-pr").copy(wait = TodoWait(kind = TodoWaitKind.PrMerge, prBlockers = listOf(TodoWaitPRBlocker.Conflicts)))
        assertEquals(TodoRestStatus.PrConflicts, waitPr.restStatus(lookup, now))
        val waitMergeReview = todo("wait-mr").copy(wait = TodoWait(kind = TodoWaitKind.MergeReview))
        assertEquals(TodoRestStatus.ParkedMergeReview, waitMergeReview.restStatus(lookup, now))
        val parked = todo("parked", supervision = Supervision(supervisor = "c", state = "parked"))
        assertEquals(TodoRestStatus.Parked, parked.restStatus(lookup, now))

        // Open concern without active agent -> Tracking; with open PR -> PR status; with active/idle agent -> null (Working/FollowUp).
        val openConcern = todo("concern").withKind("concern")
        assertEquals(TodoRestStatus.ConcernTracking, openConcern.restStatus(lookup, now))
        assertEquals(TodoRestStatus.PrWaitingMerge, withPr().withKind("concern").restStatus(lookup, now))
        assertNull(todo("working-concern", agents = listOf(agent("run"))).withKind("concern").restStatus(lookup, now))
        assertNull(todo("idle-concern", agents = listOf(agent("idle"))).withKind("concern").restStatus(lookup, now))
        assertNull(todo("done-concern", doneAt = minutesAgo(10)).withKind("concern").restStatus(lookup, now))
        assertNull(withPr().copy(supervision = Supervision(supervisor = "c", state = "needs_user")).restStatus(lookup, now))
        assertNull(todo("ordinary").restStatus(lookup, now))
    }
}
