package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.domain.answer.*
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.WorkItem
import persona.core.designsystem.resolveStatusLine
import persona.core.domain.lines.isLiveStatus
import org.junit.Assert.*
import org.junit.Test
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChatStateTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    @Test fun recordedDurationsStayWithHistoricalWorkAndMetadataAddsNoVisibleRow() {
        val user = Item("user#0", 1, role = "user", kind = "text")
        val thought = Item("thought#0", 2, role = "assistant", kind = "thinking", text = "检查示例")
        val answer = Item("reply#0", 3, role = "assistant", kind = "text")
        val marker = Item("duration#0", 4, role = "system", kind = "turn_duration",
            turnDuration = TurnDuration(12345, "claude_turn_duration", parentUuid = "reply"))
        val next = user.copy(id = "next#0", seq = 5)
        val timeline = ChatTimeline()
        val plain = timeline.lines(listOf(user, thought, answer, next), true, emptySet())
        val withDuration = timeline.lines(listOf(user, thought, answer, marker, next), true, emptySet())
        assertEquals(plain.lines, withDuration.lines)
        val durations = mapOf(user.id to 12L)
        val work = withDuration.lines.filterIsInstance<ChatLine.Work>()
        assertEquals(12L, work.first().recordedDuration(durations))
        assertNull(work.last().recordedDuration(durations))
        assertEquals("用时 12 秒", recordedDurationLabel(res, 12))
        assertEquals("用时 0 秒", recordedDurationLabel(res, 0))
        assertNull(recordedDurationLabel(res, null))
        assertNull(recordedDurationLabel(res, -1))
    }

    @Test fun historicalSnapshotSurvivesTurnRegroupingAndFoldExpansion() {
        val timeline = ChatTimeline()
        val user = Item("user", 10, role = "user", kind = "text", text = "检查改动")
        val fold = Item("fold", 11, kind = "fold", fold = Fold(items = 2, tools = 1, throughSeq = 12, throughId = "result"))
        val answer = Item("answer", 13, role = "assistant", kind = "text", text = "已完成")
        val initial = listOf(user, fold, answer)
        val original = timeline.lines(initial, true, emptySet())
        timeline.freeze(initial)
        val newerUser = Item("new-user", 14, role = "user", kind = "text", text = "继续")
        val rewrittenAnswer = answer.copy(text = "内容已变化")
        val older = Item("older", 1, role = "user", kind = "text", text = "更早的记录")
        val updated = listOf(older, user, fold, rewrittenAnswer, newerUser)
        val frozen = timeline.lines(updated, true, emptySet())
        assertTrue(frozen.lines.map { it.id }.containsAll(original.lines.map { it.id }))
        assertFalse(frozen.lines.any { it.holds(newerUser.id) })
        assertEquals("已完成", frozen.lines.filterIsInstance<ChatLine.Row>().first { it.row.id == answer.id }.row.item?.text)
        assertTrue(frozen.lines.any { it.holds(older.id) })
        val use = Item("call", 11, kind = "tool_use", toolUseId = "tool", toolName = "Read")
        val result = Item("result", 12, kind = "tool_result", toolUseId = "tool", isError = true)
        timeline.unfold(fold, listOf(use, result))
        val work = frozen.lines.filterIsInstance<ChatLine.Work>().last()
        val expanded = timeline.lines(updated, true, setOf(work.id))
        assertEquals(1, expanded.lines.filterIsInstance<ChatLine.Work>().last().knownFailedTools)
        assertTrue(expanded.lines.filterIsInstance<ChatLine.Step>().any { it.step is WorkItem.Tool })
        assertFalse(expanded.lines.filterIsInstance<ChatLine.Step>().any { it.step is WorkItem.Folded })
        assertEquals(expanded.lines.size, expanded.lines.map { it.id }.distinct().size)
        timeline.follow()
        assertTrue(timeline.lines(updated, true, emptySet()).lines.any { it.holds(newerUser.id) })
    }

    @Test fun aFoldOpenedWhileReadingHistoryCountsEachStepOnce() {
        val timeline = ChatTimeline()
        val user = Item("user#0", 10, role = "user", kind = "text", text = "先出个计划")
        val fold = Item("fold:plan#0", 11, role = "assistant", kind = "fold",
            fold = Fold(items = 4, tools = 2, throughSeq = 14, throughId = "asked#0"))
        val answer = Item("answer#0", 15, role = "assistant", kind = "text", text = "补好了")
        timeline.freeze(listOf(user, fold, answer))
        // Opening the fold: the store swaps it for its work, and the timeline is told to do the same.
        val work = listOf(
            Item("plan#0", 11, role = "assistant", kind = "tool_use", toolUseId = "p", toolName = "ExitPlanMode"),
            Item("planned#0", 12, role = "user", kind = "tool_result", toolUseId = "p", text = "approved"),
            Item("ask#0", 13, role = "assistant", kind = "tool_use", toolUseId = "q", toolName = "AskUserQuestion"),
            Item("asked#0", 14, role = "user", kind = "tool_result", toolUseId = "q", text = "answered"),
        )
        timeline.unfold(fold, work)
        val store = listOf(user) + work + answer
        val closed = timeline.lines(store, false, emptySet()).lines.filterIsInstance<ChatLine.Work>().single()
        assertEquals(2, closed.steps.size)
        assertEquals("工具 2", workSummary(res, closed, null))
        val open = timeline.lines(store, false, setOf(closed.id)).lines
        assertEquals(2, open.filterIsInstance<ChatLine.Step>().size)
        assertEquals(2, open.filterIsInstance<ChatLine.End>().single().count)
        assertEquals(store.map { it.id }.toSet(), timeline.searchItems(store).map { it.id }.toSet())
        assertEquals(store.size, timeline.searchItems(store).size)
    }

    @Test fun aMessageTheHubHasNotPickedUpYetShowsTheTurnRunning() {
        val at = java.time.Instant.parse("2026-11-05T10:00:00Z")
        val earlier = Item("old#0", 1, ts = "2026-11-05T09:59:00Z", role = "user", kind = "text", text = "先看看")
        val answer = Item("answer#0", 2, ts = "2026-11-05T09:59:30Z", role = "assistant", kind = "text", text = "看过了")
        val user = Item("user#0", 3, ts = "2026-11-05T10:00:00Z", role = "user", kind = "text", text = "继续")
        val items = listOf(earlier, answer, user)
        val soon = at.plusSeconds(2)
        // The transcript has your message, the session list still says idle (or done): the turn has started.
        val awaited = awaitedMessage(items)
        assertEquals(user, awaited)
        for (status in listOf("idle", "done", "unknown", null)) assertTrue(status.toString(), awaitingTurn(awaited, status, false, soon))
        assertFalse(awaitingTurn(awaited, "idle", false, at.plusMillis(AWAIT_TURN_MILLIS)))
        assertNull(awaitedMessage(listOf(earlier, answer)))
        assertNull(awaitedMessage(items + Item("reply#0", 4, role = "assistant", kind = "thinking")))
        // Out of list order, the newest by seq still decides.
        assertEquals(user, awaitedMessage(listOf(user, earlier, answer)))
        for (status in listOf("working", "blocked", "offline")) assertFalse(awaitingTurn(awaited, status, false, soon))
        // A failed start says so; it does not look worked on.
        assertFalse(awaitingTurn(awaited, "idle", true, soon))
        // Its work line is live, and the status line says "Working", before the hub's push.
        val session = Session(sessionId = "session", status = "idle")
        val work = ChatTimeline().lines(items, isLiveStatus(session.status) || awaitingTurn(awaited, session.status, false, soon), emptySet())
            .lines.filterIsInstance<ChatLine.Work>()
        assertTrue(work.last().live)
        val snapshot = ChatSnapshot(loading = false)
        assertNull(chatStatus(res, snapshot, session, null, emptyList(), emptyList(), false))
        assertEquals(StatusKind.Working, resolveStatusLine(chatStatusState(res, snapshot, session, null, emptyList(), emptyList(), false,
            awaiting = true))?.kind)
        // Delivered, its copy not in the transcript yet: the same.
        val sent = OutboxMessage("m", SessionKey("machine", "session"), "继续", DeliveryState.Sent)
        assertTrue(awaitingDelivered(listOf(sent), "idle", false))
        assertFalse(awaitingDelivered(listOf(sent), "blocked", false))
        assertFalse(awaitingDelivered(listOf(sent), "idle", true))
        assertFalse(awaitingDelivered(listOf(sent.copy(state = DeliveryState.Sending)), "idle", false))
    }

    // #2483: the turn's line and the header go by the same word. A question a hook holds keeps herdr's status at working while
    // the hub's state says needs you; one of the session's own requests counts before that state arrives; a task agent's
    // request is the header's alone, as this turn may still be at work.
    @Test fun theTurnWaitsOnYouWheneverTheHeaderSaysSoForThisSession() {
        val key = SessionKey("machine", "session")
        val working = Session(machineId = "machine", sessionId = "session", status = "working")
        val own = Interaction("own", machineId = "machine", sessionId = "session", kind = "question")
        val task = Interaction("task", machineId = "machine", sessionId = "task-agent", kind = "permission")
        val snapshot = ChatSnapshot(loading = false)
        for ((session, requests, turn, header) in listOf(
            WaitCase(working, emptyList(), turn = false, header = StatusKind.Working),
            WaitCase(working.copy(state = "needs_you"), emptyList(), turn = true, header = StatusKind.NeedsYou),
            WaitCase(working, listOf(own), turn = true, header = StatusKind.NeedsYou),
            WaitCase(working, listOf(task), turn = false, header = StatusKind.NeedsYou),
            WaitCase(working.copy(status = "blocked", state = null), emptyList(), turn = true, header = StatusKind.NeedsYou),
        )) {
            val case = "${session.status}/${session.state} ${requests.map { it.id }}"
            assertEquals(case, turn, turnWaitsOnYou(session, key, requests))
            assertEquals(case, header, chatStatus(res, snapshot, session, null, emptyList(), requests, false)?.kind)
        }
    }

    private data class WaitCase(val session: Session, val requests: List<Interaction>, val turn: Boolean, val header: StatusKind)

    @Test fun messageReceiptsOverrideOnlyTheirOwnLocalSendAndNeverManufactureRuntimeWork() {
        val session = Session("machine", "session", status = "idle")
        val message = OutboxMessage("client", session.key, "hello", DeliveryState.Sending)
        val receipt = MessageSendReceipt(session.key, "client", "processing", java.time.Instant.parse("2026-11-12T10:00:00Z"))
        val snapshot = ChatSnapshot(loading = false)
        fun state(receipts: List<MessageSendReceipt>, local: String? = "client") =
            chatStatusState(res, snapshot, session, null, listOf(message), emptyList(), false,
                submittingId = local, receipts = receipts)
        assertEquals(StatusKind.Submitting, resolveStatusLine(state(emptyList()))?.kind)
        assertEquals(StatusKind.Processing, resolveStatusLine(state(listOf(receipt)))?.kind)
        assertFalse(state(listOf(receipt)).working)
        assertEquals("idle", session.status)
        assertEquals(StatusKind.Submitting, resolveStatusLine(state(listOf(receipt.copy(clientMessageId = "another"))))?.kind)
        assertEquals(StatusKind.Unconfirmed, resolveStatusLine(state(listOf(receipt.copy(state = "unknown"))))?.kind)
        assertNull(resolveStatusLine(state(listOf(receipt.copy(state = "failed")))))
    }

    @Test fun statusPriorityAndApprovalRulesUseDomainPolicy() {
        val session = Session(sessionId = "session", status = "working")
        val key = SessionKey("machine", "session")
        val message = OutboxMessage("operation", key, "消息", DeliveryState.NeedsConfirmation)
        val permission = Interaction("permission", sessionId = "session", kind = "permission")
        val snapshot = ChatSnapshot(loading = false)
        assertEquals(StatusKind.Unconfirmed, chatStatus(res, snapshot, session, null, listOf(message), listOf(permission), false)?.kind)
        assertEquals(StatusKind.Submitting, chatStatus(res, snapshot, session, null, listOf(message), listOf(permission), true)?.kind)
        assertEquals(StatusKind.Ended, chatStatus(res, snapshot.copy(ended = true), session, null, listOf(message), listOf(permission), true)?.kind)
        assertFalse(AnswerBuilder.build(permission, AnswerAction.Allow, AnswerDraft(message = "先别执行")).allowed)
        assertEquals("先别执行", AnswerBuilder.build(permission, AnswerAction.Deny, AnswerDraft(message = "先别执行")).resolution?.message)
        val plan = Interaction("plan", kind = "plan")
        assertFalse(AnswerBuilder.build(plan, AnswerAction.ApproveEdits, AnswerDraft(feedback = "请修改")).allowed)
        assertFalse(AnswerBuilder.build(plan, AnswerAction.ApproveAll).allowed)
        val question = Interaction("question", kind = "question", questions = listOf(Question("选择", options = listOf(QuestionOption("选项（推荐）")))))
        assertEquals(mapOf("选择" to "选项（推荐）"), AnswerBuilder.build(question, AnswerAction.PickOption).resolution?.answers)
    }

    @Test fun backgroundLinesNeverBlocksMainThreadFreezeIsFrozenFollowOrUnreadCount() {
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)
        val workerRef = AtomicReference<Thread?>(null)
        val workerError = AtomicReference<Throwable?>(null)
        val timeline = ChatTimeline(beforeMemo = {
            if (Thread.currentThread() === workerRef.get()) {
                entered.countDown()
                assertTrue(release.await(5, TimeUnit.SECONDS))
            }
        })
        val user = Item("user#0", 1, role = "user", kind = "text", text = "Start")
        val thought = Item("thought#0", 2, role = "assistant", kind = "thinking", text = "Working")
        val initial = listOf(user, thought)
        val newerReply = Item("reply#0", 3, role = "assistant", kind = "text", text = "New reply")
        val withNewer = initial + newerReply

        val worker = Thread {
            try {
                val _ = timeline.lines(initial, live = true, open = emptySet())
            } catch (t: Throwable) {
                workerError.set(t)
            }
        }
        workerRef.set(worker)
        worker.start()
        try {
            assertTrue("background lines() did not enter memoLock", entered.await(5, TimeUnit.SECONDS))
            // Main-thread timeline queries and freeze transitions must never contend with memoLock.
            assertFalse(timeline.isFrozen())
            assertTrue(timeline.freezeChanged(initial))
            assertTrue(timeline.isFrozen())
            assertFalse(timeline.freezeChanged(initial))
            assertEquals(1, timeline.newMessageCount(withNewer, below = null, lastRead = null))
            // An unfrozen lines() pass is still in flight, so projection must still reconcile once it settles.
            assertFalse(timeline.matchesLastProjection(initial, live = true, open = emptySet()))
        } finally {
            release.countDown()
            worker.join(5_000)
        }
        assertFalse("worker thread did not finish", worker.isAlive)
        assertNull(workerError.get())

        // The racing unfrozen pass backfilled frozenLiveId so the frozen view preserves the live turn that was running.
        val frozen = timeline.lines(withNewer, live = false, open = emptySet())
        assertFalse(frozen.lines.any { it.holds(newerReply.id) })
        assertTrue(frozen.lines.filterIsInstance<ChatLine.Work>().last().live)

        assertTrue(timeline.followChanged())
        assertFalse(timeline.followChanged())
        val resumed = timeline.lines(withNewer, live = false, open = emptySet())
        assertTrue(resumed.lines.any { it.holds(newerReply.id) })
    }

    @Test fun frozenLinesStartedBeforeRacingUnfrozenPassFinishesReturnsBackfilledTurnDirectly() {
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)
        val unfrozenWorkerRef = AtomicReference<Thread?>(null)
        val unfrozenError = AtomicReference<Throwable?>(null)
        val frozenError = AtomicReference<Throwable?>(null)
        val frozenResult = AtomicReference<persona.core.domain.lines.ChatSnapshot?>(null)
        val timeline = ChatTimeline(beforeMemo = {
            if (Thread.currentThread() === unfrozenWorkerRef.get()) {
                entered.countDown()
                assertTrue(release.await(5, TimeUnit.SECONDS))
            }
        })

        // First live turn populates shownLiveId with turn 1 before turn 2 begins.
        val turn1 = listOf(
            Item("u1#0", 1, role = "user", kind = "text", text = "First question"),
            Item("t1#0", 2, role = "assistant", kind = "thinking", text = "First thought"),
        )
        val firstSnapshot = timeline.lines(turn1, live = true, open = emptySet())
        val turn1LiveId = checkNotNull(firstSnapshot.liveTurnId)

        // Turn 2 starts running while its unfrozen lines() stalls inside memoLock.
        val turn2 = turn1 + listOf(
            Item("a1#0", 3, role = "assistant", kind = "text", text = "First answer"),
            Item("u2#0", 4, role = "user", kind = "text", text = "Second question"),
            Item("t2#0", 5, role = "assistant", kind = "thinking", text = "Second thought"),
        )
        val newerReply = Item("a2#0", 6, role = "assistant", kind = "text", text = "Arrived after freeze")
        val withNewer = turn2 + newerReply

        val unfrozenWorker = Thread {
            try {
                val _ = timeline.lines(turn2, live = true, open = emptySet())
            } catch (t: Throwable) {
                unfrozenError.set(t)
            }
        }
        unfrozenWorkerRef.set(unfrozenWorker)
        val frozenStarted = CountDownLatch(1)
        val frozenWorker = Thread {
            try {
                frozenStarted.countDown()
                frozenResult.set(timeline.lines(withNewer, live = false, open = emptySet()))
            } catch (t: Throwable) {
                frozenError.set(t)
            }
        }

        unfrozenWorker.start()
        try {
            assertTrue("unfrozen lines() did not enter memoLock", entered.await(5, TimeUnit.SECONDS))
            // Freeze captures stale turn1LiveId while unfrozenWorker is still computing turn 2.
            assertTrue(timeline.freezeChanged(turn2))
            // Start the frozen lines() call BEFORE releasing the stalled unfrozen pass.
            frozenWorker.start()
            assertTrue(frozenStarted.await(5, TimeUnit.SECONDS))
            val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5)
            while (frozenWorker.state != Thread.State.WAITING &&
                frozenWorker.state != Thread.State.TIMED_WAITING &&
                frozenWorker.state != Thread.State.BLOCKED &&
                System.nanoTime() < deadline
            ) {
                Thread.yield()
            }
            assertTrue("frozen lines() did not reach memoLock wait before release: ${frozenWorker.state}",
                frozenWorker.state == Thread.State.WAITING ||
                    frozenWorker.state == Thread.State.TIMED_WAITING ||
                    frozenWorker.state == Thread.State.BLOCKED)
        } finally {
            release.countDown()
            unfrozenWorker.join(5_000)
            frozenWorker.join(5_000)
        }

        assertFalse("unfrozen worker did not finish", unfrozenWorker.isAlive)
        assertFalse("frozen worker did not finish", frozenWorker.isAlive)
        assertNull(unfrozenError.get())
        assertNull(frozenError.get())

        // Without any extra lines() call, the racing frozen snapshot must already reflect turn 2's backfilled live turn.
        val frozen = checkNotNull(frozenResult.get())
        assertNotEquals(turn1LiveId, frozen.liveTurnId)
        assertNotNull(frozen.liveTurnId)
        assertFalse(frozen.lines.any { it.holds(newerReply.id) })
        val works = frozen.lines.filterIsInstance<ChatLine.Work>()
        assertEquals(2, works.size)
        assertFalse("turn 1 work line must not stay live", works.first().live)
        assertTrue("turn 2 work line must use the backfilled live turn", works.last().live)

        // The cached frozen snapshot matches on subsequent frozen reads too.
        val cachedAgain = timeline.lines(withNewer, live = false, open = emptySet())
        assertSame(frozen, cachedAgain)
    }

    private fun awaitBlocked(thread: Thread, label: String) {
        val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5)
        while (thread.state != Thread.State.WAITING &&
            thread.state != Thread.State.TIMED_WAITING &&
            thread.state != Thread.State.BLOCKED &&
            System.nanoTime() < deadline
        ) {
            Thread.yield()
        }
        assertTrue(
            "$label did not reach lock wait: ${thread.state}",
            thread.state == Thread.State.WAITING ||
                thread.state == Thread.State.TIMED_WAITING ||
                thread.state == Thread.State.BLOCKED,
        )
    }

    @Test fun overlappingUnfrozenPassesWaitForNewestTargetWhenOlderFinishesFirst() {
        val enteredOlder = CountDownLatch(1)
        val releaseOlder = CountDownLatch(1)
        val enteredNewer = CountDownLatch(1)
        val releaseNewer = CountDownLatch(1)
        val olderWorkerRef = AtomicReference<Thread?>(null)
        val newerWorkerRef = AtomicReference<Thread?>(null)
        val olderError = AtomicReference<Throwable?>(null)
        val newerError = AtomicReference<Throwable?>(null)
        val frozenError = AtomicReference<Throwable?>(null)
        val olderResult = AtomicReference<persona.core.domain.lines.ChatSnapshot?>(null)
        val newerResult = AtomicReference<persona.core.domain.lines.ChatSnapshot?>(null)
        val frozenResult = AtomicReference<persona.core.domain.lines.ChatSnapshot?>(null)

        val timeline = ChatTimeline(beforeMemo = {
            when (Thread.currentThread()) {
                olderWorkerRef.get() -> {
                    enteredOlder.countDown()
                    assertTrue(releaseOlder.await(5, TimeUnit.SECONDS))
                }
                newerWorkerRef.get() -> {
                    enteredNewer.countDown()
                    assertTrue(releaseNewer.await(5, TimeUnit.SECONDS))
                }
            }
        })

        val turn1 = listOf(
            Item("u1#0", 1, role = "user", kind = "text", text = "First question"),
            Item("t1#0", 2, role = "assistant", kind = "thinking", text = "First thought"),
        )
        val turn2 = turn1 + listOf(
            Item("a1#0", 3, role = "assistant", kind = "text", text = "First answer"),
            Item("u2#0", 4, role = "user", kind = "text", text = "Second question"),
            Item("t2#0", 5, role = "assistant", kind = "thinking", text = "Second thought"),
        )
        val newerReply = Item("a2#0", 6, role = "assistant", kind = "text", text = "Arrived after freeze")
        val withNewer = turn2 + newerReply

        val olderWorker = Thread {
            try {
                olderResult.set(timeline.lines(turn1, live = true, open = emptySet()))
            } catch (t: Throwable) {
                olderError.set(t)
            }
        }
        val newerStarted = CountDownLatch(1)
        val newerWorker = Thread {
            try {
                newerStarted.countDown()
                newerResult.set(timeline.lines(turn2, live = true, open = emptySet()))
            } catch (t: Throwable) {
                newerError.set(t)
            }
        }
        val frozenStarted = CountDownLatch(1)
        val frozenWorker = Thread {
            try {
                frozenStarted.countDown()
                frozenResult.set(timeline.lines(withNewer, live = false, open = emptySet()))
            } catch (t: Throwable) {
                frozenError.set(t)
            }
        }
        olderWorkerRef.set(olderWorker)
        newerWorkerRef.set(newerWorker)

        olderWorker.start()
        try {
            assertTrue("older pass did not enter memoLock", enteredOlder.await(5, TimeUnit.SECONDS))

            // Start a newer unfrozen pass so both older (seq 1) and newer (seq 2) are in flight before freeze.
            newerWorker.start()
            assertTrue(newerStarted.await(5, TimeUnit.SECONDS))
            awaitBlocked(newerWorker, "newer unfrozen pass")

            // Freeze while both passes are in flight: target must be the newer pass (seq 2), not whichever finishes first.
            assertTrue(timeline.freezeChanged(turn2))

            // Start the frozen lines() waiter before releasing either unfrozen pass.
            frozenWorker.start()
            assertTrue(frozenStarted.await(5, TimeUnit.SECONDS))
            awaitBlocked(frozenWorker, "frozen pass")

            // Let the older pass finish FIRST while holding the newer pass inside beforeMemo.
            releaseOlder.countDown()
            olderWorker.join(5_000)
            assertFalse("older worker did not finish", olderWorker.isAlive)
            assertTrue("newer pass did not enter memoLock after older pass finished", enteredNewer.await(5, TimeUnit.SECONDS))

            // The frozen waiter must still be waiting for the newer target pass rather than settling on turn 1.
            assertNull("frozen pass settled prematurely on older pass", frozenResult.get())
            assertTrue("frozen waiter must remain blocked until newer target pass completes", frozenWorker.isAlive)
        } finally {
            releaseOlder.countDown()
            releaseNewer.countDown()
            olderWorker.join(5_000)
            newerWorker.join(5_000)
            frozenWorker.join(5_000)
        }

        assertFalse("newer worker did not finish", newerWorker.isAlive)
        assertFalse("frozen worker did not finish", frozenWorker.isAlive)
        assertNull(olderError.get())
        assertNull(newerError.get())
        assertNull(frozenError.get())

        val olderLiveId = checkNotNull(olderResult.get()?.liveTurnId)
        val newerLiveId = checkNotNull(newerResult.get()?.liveTurnId)
        assertNotEquals(olderLiveId, newerLiveId)

        val frozen = checkNotNull(frozenResult.get())
        assertEquals("frozen snapshot must use the newest in-flight pass's liveTurnId", newerLiveId, frozen.liveTurnId)
        assertFalse(frozen.lines.any { it.holds(newerReply.id) })
        val works = frozen.lines.filterIsInstance<ChatLine.Work>()
        assertEquals(2, works.size)
        assertFalse("turn 1 work line must not stay live", works.first().live)
        assertTrue("turn 2 work line must use the newer pass's live turn", works.last().live)
        assertSame(frozen, timeline.lines(withNewer, live = false, open = emptySet()))
    }

    @Test fun failingNewestTargetPassReleasesFrozenWaiterWithoutDeadlock() {
        val enteredOlder = CountDownLatch(1)
        val releaseOlder = CountDownLatch(1)
        val olderWorkerRef = AtomicReference<Thread?>(null)
        val newerWorkerRef = AtomicReference<Thread?>(null)
        val olderError = AtomicReference<Throwable?>(null)
        val newerError = AtomicReference<Throwable?>(null)
        val frozenError = AtomicReference<Throwable?>(null)
        val olderResult = AtomicReference<persona.core.domain.lines.ChatSnapshot?>(null)
        val frozenResult = AtomicReference<persona.core.domain.lines.ChatSnapshot?>(null)

        val timeline = ChatTimeline(beforeMemo = {
            when (Thread.currentThread()) {
                olderWorkerRef.get() -> {
                    enteredOlder.countDown()
                    assertTrue(releaseOlder.await(5, TimeUnit.SECONDS))
                }
                newerWorkerRef.get() -> throw IllegalStateException("simulated projection failure")
            }
        })

        val turn1 = listOf(
            Item("u1#0", 1, role = "user", kind = "text", text = "First question"),
            Item("t1#0", 2, role = "assistant", kind = "thinking", text = "First thought"),
        )
        val turn2 = turn1 + listOf(
            Item("a1#0", 3, role = "assistant", kind = "text", text = "First answer"),
            Item("u2#0", 4, role = "user", kind = "text", text = "Second question"),
            Item("t2#0", 5, role = "assistant", kind = "thinking", text = "Second thought"),
        )
        val newerReply = Item("a2#0", 6, role = "assistant", kind = "text", text = "Arrived after freeze")
        val withNewer = turn2 + newerReply

        val olderWorker = Thread {
            try {
                olderResult.set(timeline.lines(turn1, live = true, open = emptySet()))
            } catch (t: Throwable) {
                olderError.set(t)
            }
        }
        val newerStarted = CountDownLatch(1)
        val newerWorker = Thread {
            try {
                newerStarted.countDown()
                val _ = timeline.lines(turn2, live = true, open = emptySet())
            } catch (t: Throwable) {
                newerError.set(t)
            }
        }
        val frozenStarted = CountDownLatch(1)
        val frozenWorker = Thread {
            try {
                frozenStarted.countDown()
                frozenResult.set(timeline.lines(withNewer, live = false, open = emptySet()))
            } catch (t: Throwable) {
                frozenError.set(t)
            }
        }
        olderWorkerRef.set(olderWorker)
        newerWorkerRef.set(newerWorker)

        olderWorker.start()
        try {
            assertTrue("older pass did not enter memoLock", enteredOlder.await(5, TimeUnit.SECONDS))
            newerWorker.start()
            assertTrue(newerStarted.await(5, TimeUnit.SECONDS))
            awaitBlocked(newerWorker, "newer unfrozen pass")

            assertTrue(timeline.freezeChanged(turn2))

            frozenWorker.start()
            assertTrue(frozenStarted.await(5, TimeUnit.SECONDS))
            awaitBlocked(frozenWorker, "frozen pass")

            releaseOlder.countDown()
            olderWorker.join(5_000)
            newerWorker.join(5_000)
            frozenWorker.join(5_000)
        } finally {
            releaseOlder.countDown()
            olderWorker.join(5_000)
            newerWorker.join(5_000)
            frozenWorker.join(5_000)
        }

        assertFalse("older worker did not finish", olderWorker.isAlive)
        assertFalse("newer worker did not finish", newerWorker.isAlive)
        assertFalse("frozen worker deadlocked after target pass failed", frozenWorker.isAlive)
        assertNull(olderError.get())
        assertTrue("newer pass should have failed as simulated", newerError.get() is IllegalStateException)
        assertNull(frozenError.get())

        val olderLiveId = checkNotNull(olderResult.get()?.liveTurnId)
        val frozen = checkNotNull(frozenResult.get())
        assertEquals("frozen waiter falls back to the freshest completed pass when target fails", olderLiveId, frozen.liveTurnId)
        assertFalse(frozen.lines.any { it.holds(newerReply.id) })
    }

    @Test fun concurrentSearchItemsAndUiControlsNeverDeadlockOrThrow() {
        val timeline = ChatTimeline()
        val base = (1..200).map { seq ->
            Item("m$seq#0", seq, role = if (seq % 2 == 1) "user" else "assistant", kind = "text", text = "Message $seq")
        }
        val extended = base + Item("m201#0", 201, role = "assistant", kind = "text", text = "Followup")
        val executor = Executors.newFixedThreadPool(3)
        try {
            val backgroundLines = executor.submit {
                repeat(150) { i ->
                    val list = if (i % 2 == 0) base else extended
                    val snap = timeline.lines(list, live = i % 3 == 0, open = emptySet())
                    assertTrue(snap.lines.isNotEmpty())
                }
            }
            val backgroundSearch = executor.submit {
                repeat(150) { i ->
                    val list = if (i % 2 == 0) base else extended
                    assertTrue(timeline.searchItems(list).isNotEmpty())
                }
            }
            val uiLoop = executor.submit {
                repeat(200) { i ->
                    if (i % 2 == 0) {
                        val _ = timeline.freezeChanged(base)
                        val _ = timeline.isFrozen()
                        val _ = timeline.newMessageCount(extended, below = null, lastRead = null)
                    } else {
                        val _ = timeline.followChanged()
                        val _ = timeline.isFrozen()
                    }
                }
            }
            backgroundLines.get(15, TimeUnit.SECONDS)
            backgroundSearch.get(15, TimeUnit.SECONDS)
            uiLoop.get(15, TimeUnit.SECONDS)
        } finally {
            executor.shutdownNow()
            assertTrue(executor.awaitTermination(10, TimeUnit.SECONDS))
        }
    }

    @Test fun matchesLastProjectionAndFrozenLinesCacheAvoidRedundantRecomputation() {
        val memoCalls = AtomicInteger(0)
        val timeline = ChatTimeline(beforeMemo = { memoCalls.incrementAndGet() })
        val items = listOf(
            Item("u1#0", 1, role = "user", kind = "text", text = "Question"),
            Item("a1#0", 2, role = "assistant", kind = "text", text = "Answer"),
        )
        val initial = timeline.lines(items, live = false, open = emptySet())
        assertEquals(1, memoCalls.get())

        // Freezing at the bottom with the already-projected items requires no new background projection.
        assertTrue(timeline.freezeChanged(items))
        assertTrue(timeline.matchesLastProjection(items, live = false, open = emptySet()))
        assertFalse(timeline.freezeChanged(items))

        // Returning to the bottom while nothing changed also matches the existing projection.
        assertTrue(timeline.followChanged())
        assertTrue(timeline.matchesLastProjection(items, live = false, open = emptySet()))

        // Freezing with a streaming overlay (different list instance) must trigger a projection refresh.
        val streamingOverlay = items.toList()
        assertTrue(timeline.freezeChanged(streamingOverlay))
        assertFalse(timeline.matchesLastProjection(items, live = false, open = emptySet()))

        // While frozen, newer messages past the boundary hit the LinesCache after the first frozen build.
        val withNew1 = items + Item("a2#0", 3, role = "assistant", kind = "text", text = "New 1")
        val withNew2 = withNew1 + Item("a3#0", 4, role = "assistant", kind = "text", text = "New 2")
        val frozen1 = timeline.lines(withNew1, live = false, open = emptySet())
        assertEquals(2, memoCalls.get())
        val frozen2 = timeline.lines(withNew2, live = false, open = emptySet())
        assertSame(frozen1, frozen2)
        assertEquals(2, memoCalls.get())
        assertEquals(initial.lines, frozen1.lines)

        // Following after newer messages arrived invalidates the match so live items are projected.
        assertTrue(timeline.followChanged())
        assertFalse(timeline.matchesLastProjection(withNew2, live = false, open = emptySet()))
    }

    @Test fun itemBlockPartMatchesOriginalSubstringParsingOnEdgeCases() {
        for (id in listOf(
            "",
            "plain",
            "#",
            "a#",
            "msg#0",
            "msg#12",
            "a#1#2",
            "msg#-3",
            "msg#+4",
            "msg#abc",
            "msg#12a",
            "msg#2147483647",
            "msg#2147483648",
            "msg#9999999999999",
        )) {
            val expected = id.substringAfterLast('#', "").toIntOrNull() ?: 0
            assertEquals("id=$id", expected, itemBlockPart(id))
        }
    }
}
