package persona.feature.chat

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.semantics.ProgressBarRangeInfo
import androidx.compose.ui.semantics.progressBarRangeInfo
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.Dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.*
import persona.core.contracts.api.AssistantInboxDraft
import persona.core.contracts.api.ScheduledTask
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.answer.*
import persona.core.domain.tasks.BackgroundWork
import persona.core.domain.tasks.TriageStatus
import persona.core.domain.tasks.NeedsYouEntry
import persona.core.domain.tasks.NeedsYouGroup
import persona.core.domain.tasks.needsYouGroups
import persona.core.domain.tasks.RunningEntry
import persona.core.domain.tasks.TaskState
import persona.core.domain.tasks.notDoneReason
import persona.core.domain.tasks.orphanedLine
import persona.core.domain.tasks.assistantEntries
import kotlinx.coroutines.delay
import persona.core.domain.sort.canApprove

/** The two entries above the Assistant's composer (#3387): Needs you, everything waiting on the user, and In progress,
 *  everything running that does not; each thing in one of them only (`persona.core.domain.tasks.assistantEntries`). */
internal enum class AssistantTray { NeedsYou, Running }

@Stable
internal class AssistantTrayState {
    var expanded by mutableStateOf<AssistantTray?>(null)
    fun toggle(tray: AssistantTray) { expanded = tray.takeUnless { expanded == tray } }
}

/** Only policy-valid one-shot approvals are eligible; drafts and field-bearing permissions never are. */
internal fun trayApproval(request: Interaction, session: Session?): Resolution? {
    if (session?.canApprove != true || request.needsFieldValues) return null
    val action = when (request.kind) {
        "permission" -> AnswerAction.Allow
        "plan" -> AnswerAction.ApproveEdits
        else -> return null
    }
    return AnswerBuilder.build(request, action, AnswerDraft(), AnswerContext()).resolution
}

internal data class TrayProgress(val id: String, val title: String, val todo: Todo? = null,
    val plan: TaskPlan? = null, val done: Boolean = false, val blocked: Boolean = false, val cancelled: Boolean = false,
    val text: String? = null, val completed: Int? = null, val total: Int? = null, val at: String? = null, val pr: String? = null,
    /** Why the stopped task is not done, from the hub's completion check (#3180): its row reads "Stopped, not done yet · …"
     *  in place of [text]; "" with no reason, null when there is nothing to say. */
    val whyNotDone: String? = null,
    /** "Nobody is working on this · closed after an error" (#3618): the hub found the open task orphaned; in place of
     *  [text] and [whyNotDone], unless an agent or the owner's ask says what it waits for. */
    val orphaned: persona.core.contracts.CoreText? = null,
    /** Open, waiting on nobody, and none of its agents works (unread or seen, #3576): the row says Stopped, not In progress,
     *  and draws only a counted bar. Back to In progress once one of them works again. */
    val stopped: Boolean = false)

/** Prefer an explicit supervisor report, then the agent's counted plan; do not estimate progress from status. */
internal fun trayProgress(todos: List<Todo>, plans: List<TaskPlan>, host: AssistantTaskHost,
    feed: List<FeedItem> = emptyList(), projected: List<AssistantTaskCard> = emptyList(), everyTodo: Boolean = false): List<TrayProgress> {
    // the main conversation watches every task (docs/assistant/unified-entry.md): a row for each to-do, whatever its origin (#3331)
    val own = { todo: Todo -> everyTodo || todo.origin == host.conversation }
    val bound = plans.flatMap { plan -> plan.tasks.mapIndexedNotNull { index, task ->
        task.todoId?.takeIf { id -> todos.firstOrNull { it.id == id }?.planId?.let { it == plan.id } != false }
            ?.let { it to "plan:${plan.id}:$index" }
    } }.toMap()
    val rows = todos.filter(own).map { todo ->
        val lead = host.tasks.lead(todo, host.sessions)
        val state = host.tasks.stateOf(todo, host.sessions)
        val report = todo.supervision?.progress
        val plan = lead?.plan
        val reportAt = parseTime(report?.at)
        val planAt = parseTime(plan?.at)
        val reportIsCurrent = planAt == null || (reportAt != null && !reportAt.isBefore(planAt))
        val countedReport = report?.takeIf { reportIsCurrent && it.steps > 0 && it.step in 0..it.steps }
        val countedPlan = plan?.takeIf { it.total > 0 && it.done in 0..it.total }
        val cancelled = todo.isDone && (todo.cancelledAt != null || todo.doneBy == "cancelled" || todo.supervision?.state == "cancelled")
        val done = todo.ask == null && todo.isDone && !cancelled && state != TaskState.Failed
        val completedText = todo.supervision?.reviews?.lastOrNull()?.note?.takeIf(String::isNotBlank)
            ?: todo.doneReason?.takeIf(String::isNotBlank) ?: lead?.lastText?.takeIf(String::isNotBlank)
        // stopped and not done: the hub's check says why; an owner's ask or an agent waiting for the user says more
        val whyNotDone = todo.notDoneReason.takeUnless { done || cancelled || todo.ask != null || state == TaskState.Working || state == TaskState.NeedsYou }
        val blocked = !cancelled && (todo.ask != null || state in setOf(TaskState.NeedsYou, TaskState.Interrupted, TaskState.Failed) || todo.supervision?.state == "needs_user")
        TrayProgress(bound[todo.id] ?: "todo:${todo.id}", todo.title, todo = todo,
            done = done, cancelled = cancelled, blocked = blocked,
            stopped = !done && !cancelled && !blocked && (state == TaskState.Unread || state == TaskState.Followup),
            text = if (done || cancelled) completedText else todo.ask?.verdict ?: report?.takeIf { reportIsCurrent }?.rowText?.takeIf(String::isNotBlank) ?: plan?.current?.takeIf(String::isNotBlank)
                ?: todo.startError?.takeIf(String::isNotBlank) ?: lead?.attentionReason?.takeIf(String::isNotBlank)
                ?: lead?.activity?.takeIf(String::isNotBlank) ?: lead?.lastText?.takeIf(String::isNotBlank),
            completed = countedReport?.step ?: countedPlan?.done, total = countedReport?.steps ?: countedPlan?.total,
            at = report?.at?.takeIf { reportIsCurrent } ?: plan?.at ?: todo.updatedAt,
            pr = todo.prUrl?.takeIf(String::isNotBlank), whyNotDone = whyNotDone,
            orphaned = todo.orphanedLine.takeUnless { done || cancelled || todo.ask != null || state == TaskState.NeedsYou })
    }
    val pending = plans.filter { it.conversation == host.conversation && it.state == "open" }.flatMap { plan ->
        plan.tasks.mapIndexedNotNull { index, task ->
            if (task.todoId != null && todos.any { it.id == task.todoId }) null
            else TrayProgress("plan:${plan.id}:$index", task.title, plan = plan, text = task.why, blocked = task.error != null)
        }
    }
    val fallback = (pending + rows).distinctBy { it.id }
    val merged = persona.core.domain.tasks.AssistantTaskCards.merge(feed,
        plans.filter { it.conversation == host.conversation }.associateBy { it.id },
        todos.filter(own), projected)
    val byTodo = rows.associateBy { it.todo?.id }
    val byId = fallback.associateBy { it.id }
    val authoritative = merged.filter { card ->
        card.todo?.planId?.let { it == card.identity.planId } != false
    }.groupBy { it.identity.todoId?.let { id -> "todo:$id" } ?: it.identity.id }.values.map { cards ->
        cards.maxBy { parseTime(it.identity.updatedAt) ?: java.time.Instant.MIN }
    }.map { card ->
        val row = byTodo[card.todo?.id] ?: byId[card.identity.id]
            ?: TrayProgress(card.identity.id, card.plan?.tasks?.getOrNull(card.identity.index ?: -1)?.title.orEmpty(),
                todo = card.todo, plan = card.plan)
        val receipt = card.receipt
        val report = card.report
        val latestSummary = report?.field("summary") ?: receipt?.field("summary")
        val pr = report?.field("pr") ?: receipt?.field("pr")
        val acceptedText = card.todo?.supervision?.reviews?.lastOrNull { it.verdict == "accepted" }?.note?.takeIf(String::isNotBlank)
            ?: card.todo?.doneReason?.takeIf(String::isNotBlank)
        val completed = card.todo?.ask == null && (row.done || (card.todo == null && card.identity.state == "accepted"))
        val cancelled = row.cancelled || (card.todo == null && card.identity.state == "cancelled")
        row.copy(id = card.identity.id,
            done = completed && !cancelled, cancelled = cancelled,
            blocked = !cancelled && (row.blocked || (card.todo == null && card.identity.state in setOf("needs_user", "needs_you", "blocked", "failed", "interrupted"))),
            text = if (completed || cancelled) latestSummary ?: (if (row.done || row.cancelled) row.text else acceptedText) else row.text ?: latestSummary,
            whyNotDone = row.whyNotDone.takeUnless { completed || cancelled },
            orphaned = row.orphaned.takeUnless { completed || cancelled },
            pr = row.pr ?: pr,
            at = listOfNotNull(card.identity.updatedAt, row.at).maxByOrNull { parseTime(it) ?: java.time.Instant.MIN })
            .let { it.copy(stopped = it.stopped && !it.done && !it.cancelled && !it.blocked) }
    }
    val known = authoritative.map { it.todo?.id }.filterNotNull().toSet()
    val identities = authoritative.map { it.id }.toSet()
    return (authoritative + fallback.filter { it.id !in identities && (it.todo == null || it.todo.id !in known) })
        .distinctBy { it.id }.sortedBy { it.done || it.cancelled }
}

internal sealed interface TrayDraft {
    val id: String
    val title: String
    val text: String
    data class Inbox(val draft: AssistantInboxDraft) : TrayDraft {
        override val id get() = draft.itemId
        override val title get() = draft.title
        override val text get() = draft.suggestion.text
    }
    data class Feed(val item: FeedItem) : TrayDraft {
        override val id get() = item.field("item_id") ?: item.ref ?: item.id
        override val title get() = item.field("chat") ?: item.field("title").orEmpty()
        override val text get() = item.field("draft").orEmpty()
    }
}

internal fun trayDrafts(inbox: List<AssistantInboxDraft>, feed: List<FeedItem>): List<TrayDraft> =
    (inbox.map { TrayDraft.Inbox(it) } + feed.filter { it.kind == "message" && it.canSend && it.field("draft")?.isNotBlank() == true }
        .map { TrayDraft.Feed(it) }).distinctBy { it.id }

/** Real task/session/interaction projections, with one bounded panel above its permanent two-entry bar. [requests] are
 *  the cards this screen may show: the conversation's own, and its tasks' and Agents' (each goes to its task's or Agent's
 *  row; one that belongs to neither is not this conversation's). */
@Composable
internal fun AssistantTrays(host: AssistantTaskHost, state: AssistantTrayState,
    requests: List<Interaction>, inbox: List<AssistantInboxDraft>, schedules: List<ScheduledTask>,
    maximumHeight: Dp, busy: Boolean, enabled: Boolean,
    onApprove: (List<Pair<Interaction, Resolution>>) -> Unit, onReview: (Interaction) -> Unit,
    onSend: (TrayDraft) -> Unit, onSchedule: (ScheduledTask) -> Unit,
    backgroundRecords: Boolean = false, onBackground: (() -> Unit)? = null,
    onPlan: (TaskPlan) -> Unit = {},
    /** The main conversation folds background rounds (#4314): Needs you groups its rows by supervising conversation. */
    groupByOrigin: Boolean = false,
    /**
     * Under the entries: the pill while the conversation's model operates a Mac. iOS puts it after them in their row
     * (`AssistantActionBar`); beside Android's two entries a phone leaves it too little room for its words.
     */
    under: (@Composable () -> Unit)? = null) {
    val todos by host.tasks.todos.collectAsStateWithLifecycle()
    val plans by host.tasks.plans.collectAsStateWithLifecycle()
    val feeds by host.tasks.feeds.collectAsStateWithLifecycle()
    val later by host.tasks.later.collectAsStateWithLifecycle()
    val main by host.tasks.mainConversation.collectAsStateWithLifecycle()
    val taskBusy by host.tasks.busy.collectAsStateWithLifecycle()
    val taskCards by host.tasks.taskCards.collectAsStateWithLifecycle()
    val feed = feeds[host.conversation].orEmpty()
    val progress = trayProgress(todos, plans.values.toList(), host, feed, taskCards[host.conversation].orEmpty(),
        everyTodo = main != null && main == host.conversation)
    val progressByTodo = progress.mapNotNull { row -> row.todo?.let { it.id to row } }.toMap()
    val bySession = host.sessions.associateBy { it.key }
    val context = LocalContext.current
    val restores = remember(context) { TodoRestores(context) }
    // a minute's tick moves tasks across the 48 h shelving line while nothing else changes, as on the To-dos page
    var nowMs by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(Unit) { while (true) { delay(60_000); nowMs = System.currentTimeMillis() } }
    val entries = assistantEntries(host.conversation, main, todos, host.sessions, requests, plans.values, feed.filterNot { it.id in later },
        maxOf(nowMs, System.currentTimeMillis()), restores.read())
    // "Later" is only a local, card-scoped choice; it never hides a newer draft for the same source item.
    // the conversation's own cards (Persona asking) are always the user's; they come first after the asking rows
    val own = requests.filter { it.sessionId == host.conversation }
    val asking = entries.needsYou.filter { (it as? NeedsYouEntry.Task)?.asks?.isNotEmpty() == true || (it as? NeedsYouEntry.Agent)?.asks?.isNotEmpty() == true }
    val plansWaiting = entries.needsYou.filterIsInstance<NeedsYouEntry.Plan>()
    val deciding = entries.needsYou.filter { it is NeedsYouEntry.Task && it !in asking }.map { it as NeedsYouEntry.Task }
    val drafts = trayDrafts(inbox, entries.needsYou.filterIsInstance<NeedsYouEntry.Message>().map { it.item })
    val needsCount = asking.size + own.size + plansWaiting.size + deciding.size + drafts.size
    val needsGroups = needsYouGroups(entries.needsYou, host.conversation, groupByOrigin)
    val shownRequests = own + asking.flatMap { (it as? NeedsYouEntry.Task)?.asks ?: (it as NeedsYouEntry.Agent).asks }
    val approvals = shownRequests.mapNotNull { r -> trayApproval(r, bySession[GroupKeys.rawKey(r.sessionKey)])?.let { r to it } }
    val standalone = persona.core.domain.tasks.dockAgents(host.conversation, main, host.sessions, todos)
    // Glass folds the shelved tasks into one line under In progress, as iOS (#4339 C18); Brief and Board keep the tray as it was.
    val shelvedLine = PersonaTheme.style == AppStyle.Glass
    // the tray closes once its last thing is handled; tapped while empty it still opens "All caught up"
    LaunchedEffect(needsCount) { if (needsCount == 0 && state.expanded == AssistantTray.NeedsYou) state.expanded = null }
    BackHandler(state.expanded != null) { state.expanded = null }
    Column(Modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        state.expanded?.let { tray ->
            JCard(Modifier.fillMaxWidth().testTag("assistant-tray-${tray.tag}"), padding = Tokens.Space.xs) {
                LazyColumn(Modifier.heightIn(max = minOf(maximumHeight, AssistantTrayTokens.maximumHeight)),
                    contentPadding = PaddingValues(bottom = Tokens.Space.s)) {
                    item {
                        val count = when (tray) {
                            AssistantTray.NeedsYou -> needsCount
                            AssistantTray.Running -> entries.running.size
                        }
                        JSectionHeader(stringResource(tray.label), trailing = pluralStringResource(R.plurals.chat_tray_items, count, count), action = if (tray == AssistantTray.NeedsYou && approvals.isNotEmpty()) ({
                            JTextButton(stringResource(R.string.chat_tray_approve_all), { onApprove(approvals) },
                                enabled = enabled && !busy, modifier = Modifier.testTag("assistant-approve-all"))
                        }) else if (tray == AssistantTray.Running) ({
                            JTextButton(stringResource(R.string.chat_tasks_dock_all, progress.size + standalone.size),
                                { host.sheet.value = TaskSheet.Board }, modifier = Modifier.testTag("assistant-task-board"))
                        }) else null)
                    }
                    when (tray) {
                        AssistantTray.NeedsYou -> {
                            if (needsCount == 0) item { JEmptyState(stringResource(R.string.chat_tray_caught_up), "", icon = PhIcons.CheckCircle) }
                            // #4314: one group, as before; with several supervising conversations, each under its title,
                            // this conversation's first (its own cards, plans and drafts are in it), the rows unchanged.
                            for (group in needsGroups) {
                                if (needsGroups.size > 1 && (group.entries.isNotEmpty() || (group.conversation == host.conversation && (own.isNotEmpty() || drafts.isNotEmpty())))) {
                                    item(key = "needs-group:${group.conversation}") {
                                        JSectionHeader(host.sessions.firstOrNull { it.sessionId == group.conversation }?.displayTitle?.takeIf(String::isNotBlank)
                                            ?: stringResource(R.string.chat_tray_other_conversation))
                                    }
                                }
                                items(asking.filter(group::has), key = { it.key }) { entry ->
                                    val (title, asks, tag) = when (entry) {
                                        is NeedsYouEntry.Task -> Triple(entry.todo.title, entry.asks, "assistant-needs-task-${entry.todo.id}")
                                        is NeedsYouEntry.Agent -> Triple(entry.session.displayTitle, entry.asks, "assistant-needs-agent-${entry.session.sessionId}")
                                        else -> return@items
                                    }
                                    // one row for the task or Agent: its oldest card, the next one once that is answered
                                    val request = asks.first()
                                    val approval = trayApproval(request, bySession[GroupKeys.rawKey(request.sessionKey)])
                                    Box(Modifier.testTag(tag)) {
                                        TrayDecisionRow(title, request.headline.shown, if (approval != null) R.string.chat_tray_approve else R.string.chat_tray_review,
                                            "assistant-decision-${request.id}", enabled && !busy, onOpen = { onReview(request) }) {
                                            if (approval != null) onApprove(listOf(request to approval)) else onReview(request)
                                        }
                                    }
                                }
                                items(own.takeIf { group.conversation == host.conversation }.orEmpty(), key = { "request:${it.id}" }) { request ->
                                    val approval = trayApproval(request, bySession[GroupKeys.rawKey(request.sessionKey)])
                                    TrayDecisionRow(request.sessionTitle ?: bySession[GroupKeys.rawKey(request.sessionKey)]?.displayTitle ?: request.headline.shown,
                                        request.headline.shown, if (approval != null) R.string.chat_tray_approve else R.string.chat_tray_review,
                                        "assistant-decision-${request.id}", enabled && !busy, onOpen = { onReview(request) }) {
                                        if (approval != null) onApprove(listOf(request to approval)) else onReview(request)
                                    }
                                }
                                items(plansWaiting.filter(group::has), key = { it.key }) { entry ->
                                    val plan = entry.plan
                                    TrayDecisionRow(plan.tasks.joinToString(stringResource(R.string.chat_tasks_list_separator)) { it.title },
                                        stringResource(R.string.chat_tray_plan_review), R.string.chat_tray_review, "assistant-plan-${plan.id}", enabled && plan.id !in taskBusy,
                                        onOpen = { onPlan(plan) }) { onPlan(plan) }
                                }
                                items(deciding.filter(group::has), key = { it.key }) { entry ->
                                    val todo = entry.todo
                                    val row = progressByTodo[todo.id]
                                    if (todo.ask?.outcome == "decide") TaskDecisionCard(todo, host, enabled) { openTask(host, todo) }
                                    else TrayDecisionRow(todo.title, todo.ask?.verdict ?: row?.text.orEmpty(), R.string.chat_tray_review,
                                        "assistant-review-${row?.id ?: "todo:${todo.id}"}", enabled, onOpen = { openTask(host, todo) }) { openTask(host, todo) }
                                }
                                val groupDrafts = drafts.takeIf { group.conversation == host.conversation }.orEmpty()
                                if (groupDrafts.isNotEmpty()) item { JSectionHeader(stringResource(R.string.chat_tray_drafts)) }
                                items(groupDrafts, key = { "draft:${if (it is TrayDraft.Feed) it.item.id else it.id}" }) { draft ->
                                    if (draft is TrayDraft.Feed) {
                                        Box(Modifier.testTag("assistant-draft-${draft.id}").padding(horizontal = Tokens.Space.s)) {
                                            MessageCard(draft.item, host, todos, enabled && !busy)
                                        }
                                    } else TrayDecisionRow(draft.title, draft.text, R.string.chat_tray_send, "assistant-draft-${draft.id}",
                                        enabled && !busy && draftPlaceholder(draft.text) == null) { onSend(draft) }
                                }
                            }
                        }
                        AssistantTray.Running -> {
                            items(entries.running, key = { it.key }) { entry ->
                                when (entry) {
                                    is RunningEntry.Task -> {
                                        val projected = progressByTodo[entry.todo.id] ?: TrayProgress("todo:${entry.todo.id}", entry.todo.title, todo = entry.todo)
                                        // Only replace the projection's state when a shared Agent's requests belong elsewhere (#7531).
                                        val row = if (entry.hasAgentsElsewhere) projected.copy(blocked = false,
                                            stopped = entry.status == TriageStatus.Unread || entry.status == TriageStatus.FollowUp)
                                        else projected
                                        TrayProgressRow(row, activity = entry.activity, waiting = entry.waiting,
                                            working = entry.status == TriageStatus.Working, onClick = { openTask(host, entry.todo) })
                                    }
                                    is RunningEntry.Agent -> {
                                        val agent = entry.session
                                        val detail = trayBackgroundLine(entry.activity) ?: stringResource(R.string.chat_tray_agent_detail, host.machineName(agent.machineId),
                                            agent.activity?.takeIf(String::isNotBlank) ?: agent.lastText.orEmpty())
                                        JRow(onClick = { host.onOpen(agent.key) }, modifier = Modifier.testTag("assistant-agent-${agent.sessionId}"), trailing = {
                                            Text(stringResource(if (entry.waiting) R.string.chat_tray_waiting else R.string.chat_tray_working),
                                                style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
                                        }) {
                                            // iOS: a clock for an agent whose background work only waits, the working mark otherwise
                                            if (entry.waiting) TrayWaitClock() else JSpinner(Modifier.testTag("assistant-work-spinner"))
                                            Spacer(Modifier.width(Tokens.Space.s))
                                            Column(Modifier.weight(1f)) {
                                                Text(agent.displayTitle, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink,
                                                    maxLines = 1, overflow = TextOverflow.Ellipsis)
                                                TrayBackgroundSubtitle(entry.activity, detail)
                                            }
                                        }
                                    }
                                }
                            }
                            // Open tasks with no progress for 48 hours: one quiet line under the rows, not counted; the board
                            // lists them (iOS `shelvedLine`, Glass).
                            if (shelvedLine && entries.shelved.isNotEmpty()) item(key = "shelved") {
                                TrayShelvedLine(entries.shelved.size) { state.expanded = null; host.sheet.value = TaskSheet.Board }
                            }
                            if (schedules.isNotEmpty() || backgroundRecords) item { JSectionHeader(stringResource(R.string.chat_tray_quiet)) }
                            items(schedules, key = { "schedule:${it.id}" }) { schedule ->
                                JRow(schedule.title, subtitle = schedule.lastError?.takeIf(String::isNotBlank)
                                    ?: schedule.nextRun?.let { stringResource(R.string.chat_tray_next_run, it) }
                                    ?: schedule.lastStatus, subtitleLines = 1, onClick = { onSchedule(schedule) },
                                    leading = { AssistantTrayMark(null) }, trailing = {
                                        Text(when (schedule.repeats) {
                                            ScheduledTask.INTERVAL -> stringResource(R.string.chat_tray_interval, schedule.everyMinutes)
                                            ScheduledTask.DAILY -> stringResource(R.string.chat_tray_daily)
                                            else -> stringResource(R.string.chat_tray_once)
                                        }, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
                                    })
                            }
                            if (backgroundRecords && onBackground != null) item {
                                JRow(stringResource(R.string.chat_tray_background), onClick = onBackground, leadingIcon = PhIcons.Clock)
                            }
                            if (entries.running.isEmpty() && schedules.isEmpty() && !backgroundRecords &&
                                !(shelvedLine && entries.shelved.isNotEmpty())) item {
                                JEmptyState(stringResource(R.string.chat_tray_no_progress), "", icon = PhIcons.ListChecks)
                            }
                        }
                    }
                }
            }
        }
        // Content-width chips at the leading edge, like the old All / task chips; a large font scrolls rather than wraps.
        Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).testTag("assistant-tray-bar"),
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            for (tray in AssistantTray.entries) AssistantTrayEntry(stringResource(tray.label), when (tray) {
                AssistantTray.NeedsYou -> needsCount
                AssistantTray.Running -> entries.running.size
            }, when (tray) {
                AssistantTray.NeedsYou -> AssistantTrayTone.Pending
                AssistantTray.Running -> AssistantTrayTone.Progress
            }, state.expanded == tray,
                stringResource(if (state.expanded == tray) R.string.chat_tray_expanded else R.string.chat_tray_collapsed),
                { state.toggle(tray) }, Modifier.testTag("assistant-entry-${tray.tag}"),
                enabled = true)
        }
        under?.invoke()
    }
}

/**
 * "Message “x”" above the composer while the next message goes straight to a task (iOS `RouteChip`, web `RouteChip`;
 * docs/assistant/unified-entry.md "Routing"); its × hands the message back to Persona. The route is the store's one per
 * conversation, set by a task's Message or by picking the task after @. Every style: where a message goes is behaviour,
 * and each style draws its own chip.
 */
@Composable
internal fun TaskRouteChip(host: AssistantTaskHost, modifier: Modifier = Modifier) {
    val routes by host.tasks.routes.collectAsStateWithLifecycle()
    val todos by host.tasks.todos.collectAsStateWithLifecycle()
    val task = routedTask(routes, todos, host.conversation) ?: return
    // At the tray bar's inset, so the chip lines up with its chips above it.
    Row(modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m)) {
        JChip(stringResource(R.string.chat_tasks_route_chip, task.title), Modifier.testTag("assistant-route-chip"),
            icon = PhIcons.PaperPlaneTilt, accent = true, onRemove = { host.tasks.setRoute(host.conversation, null) },
            removeLabel = stringResource(R.string.chat_tasks_route_clear))
    }
}

private val AssistantTray.label get() = when (this) {
    AssistantTray.NeedsYou -> R.string.chat_tray_entry_needs_you
    AssistantTray.Running -> R.string.chat_tray_entry_running
}

/** The test tag part: `assistant-entry-needs` / `assistant-tray-running`. */
internal val AssistantTray.tag get() = when (this) {
    AssistantTray.NeedsYou -> "needs"
    AssistantTray.Running -> "running"
}

@Composable
private fun TrayDecisionRow(title: String, subtitle: String, action: Int, tag: String, enabled: Boolean,
    onOpen: (() -> Unit)? = null, onClick: () -> Unit) {
    JRow(title, subtitle = subtitle, subtitleLines = 1, chevron = false, onClick = onOpen.takeIf { enabled },
        leading = { AssistantTrayMark(AssistantTrayTone.Pending) }, trailing = {
            JButton(stringResource(action), onClick, Modifier.testTag(tag), kind = JButtonKind.Secondary,
                size = JButtonSize.Small, enabled = enabled)
        })
}

@Composable
internal fun trayBackgroundLine(work: BackgroundWork?): String? {
    work ?: return null
    val parts = mutableListOf<String>()
    if (work.workflows > 0) {
        parts += listOfNotNull(stringResource(R.string.chat_tray_background_workflow), work.workflow?.phase,
            work.workflow?.let { persona.core.domain.sort.WorkflowTimeline.counts(it) },
            (work.workflows - 1).takeIf { it > 0 }?.let { stringResource(R.string.chat_tray_workflow_more, it) }).joinToString(" · ")
    }
    if (work.tasks > 0) parts += stringResource(R.string.chat_tray_background_tasks_running, work.tasks)
    if (work.subagents > 0) parts += stringResource(R.string.chat_tray_subagents_running, work.subagents)
    return parts.joinToString(" · ").ifEmpty { stringResource(R.string.chat_tray_background_running) }
}

@Composable
private fun TrayBackgroundSubtitle(work: BackgroundWork?, fallback: String?) {
    val workflow = work?.workflow
    val palette = PersonaTheme.palette
    if (work != null && workflow != null && work.workflows > 0) {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Text(workflow.phase ?: stringResource(R.string.chat_tray_background_workflow), Modifier.weight(1f),
                style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(" · " + persona.core.domain.sort.WorkflowTimeline.counts(workflow) +
                if (work.workflows > 1) " · " + stringResource(R.string.chat_tray_workflow_more, work.workflows - 1) else "",
                style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1)
        }
        if (work.tasks > 0 || work.subagents > 0) {
            Text(trayBackgroundLine(work.copy(workflows = 0, workflow = null)).orEmpty(), style = PersonaTheme.type.caption,
                color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    } else fallback?.let { Text(it, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis) }
}

/**
 * The clock of a row that waits to be brought back (iOS draws it in `StatusPalette.backgroundWaiting`): Glass in that
 * state's lighter tone ([trayWaitTint]); Brief and Board keep the row's content color.
 */
@Composable
private fun TrayWaitClock() {
    JIcon(PersonaIcons.backgroundWaiting, null, decorative = true, modifier = Modifier.testTag("assistant-wait-clock"),
        tint = trayWaitTint(PersonaTheme.style == AppStyle.Glass, PersonaTheme.palette, LocalContentColor.current))
}

/** The tone of [TrayWaitClock]: the palette's background-waiting dot on Glass, else the [content] color. */
internal fun trayWaitTint(glass: Boolean, palette: PersonaPalette, content: Color): Color =
    if (glass) palette.backgroundWaiting.dot else content

/** "Shelved" and how many, quiet, under In progress's rows (iOS `shelvedLine`): a tap opens the board, which lists them. */
@Composable
private fun TrayShelvedLine(count: Int, onClick: () -> Unit) {
    val palette = PersonaTheme.palette
    JRow(onClick = onClick, modifier = Modifier.testTag("assistant-tray-shelved"), chevron = false, trailing = {
        Text("$count", style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1)
    }) {
        JIcon(persona.core.designsystem.icons.PersonaIcons.disclosure, null, decorative = true, tint = palette.ink3)
        Spacer(Modifier.width(Tokens.Space.s))
        Text(stringResource(R.string.chat_tray_shelved), style = PersonaTheme.type.body, color = palette.ink2, maxLines = 1,
            overflow = TextOverflow.Ellipsis)
    }
}

internal fun trayPullRequestNumber(reference: String): String? =
    Regex("/(?:pull|merge_requests)/(\\d+)(?:$|[/?#])").find(reference)?.groupValues?.get(1)

@Composable
private fun TrayProgressRow(row: TrayProgress, activity: BackgroundWork? = null, waiting: Boolean = false,
    working: Boolean = false, onClick: () -> Unit) {
    val live = !row.done && !row.cancelled
    val work = activity.takeIf { live }
    val detail = trayBackgroundLine(work) ?: row.text
    val palette = PersonaTheme.palette
    JRow(onClick = onClick, modifier = Modifier.testTag("assistant-progress-${row.id}"), chevron = false, trailing = {
        if (row.done) JIcon(PhIcons.Check, stringResource(if (row.todo?.supervision?.state == "accepted")
            R.string.chat_tray_accepted else R.string.chat_tray_done), tint = palette.positive)
        else Text(stringResource(if (work != null) R.string.chat_tray_working else if (live && waiting) R.string.chat_tray_waiting
            else if (row.cancelled) R.string.chat_tray_cancelled else if (row.blocked) R.string.chat_tray_blocked else if (row.plan != null) R.string.chat_tray_planned
            else if (row.stopped) R.string.chat_tray_stopped_state else R.string.chat_tray_in_progress),
            style = PersonaTheme.type.micro, color = if (row.blocked || row.orphaned != null) palette.cautionText else palette.ink3, maxLines = 1)
    }) {
        if (live && (work != null || (working && !waiting))) {
            JSpinner(Modifier.testTag("assistant-work-spinner"))
            Spacer(Modifier.width(Tokens.Space.s))
        } else if (live && waiting) {
            TrayWaitClock()
            Spacer(Modifier.width(Tokens.Space.s))
        }
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Text(row.title, style = PersonaTheme.type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            val text = row.whyNotDone?.let { if (it.isEmpty()) stringResource(R.string.chat_tray_stopped) else stringResource(R.string.chat_tray_stopped_reason, it) } ?: row.text
            // nobody works on it: said where the row says what it does, in the caution color (never only the color: the words say it)
            val orphaned = row.orphaned?.takeIf { work == null && !waiting }
            if (orphaned != null) Text(orphaned.text(), Modifier.testTag("assistant-orphaned-${row.id}"), style = PersonaTheme.type.caption,
                color = palette.cautionText, maxLines = 1, overflow = TextOverflow.Ellipsis)
            else TrayBackgroundSubtitle(work, if (work != null) detail else if (waiting) row.text else text)
            row.pr?.let { reference ->
                val number = trayPullRequestNumber(reference)
                Text(if (number == null) reference else stringResource(R.string.chat_tray_pr_number, number),
                    style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            val done = row.completed; val total = row.total
            if (!row.done && !row.cancelled) {
                val counted = done != null && total != null && total > 0
                if (counted) Text(pluralStringResource(R.plurals.chat_tray_steps, total, done, total),
                    style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1)
                val fraction = if (counted) done!!.toFloat() / total!! else if (row.plan != null) 0f else null
                // a stopped task draws only a real count: nothing runs, so no indeterminate bar (#3576)
                if (fraction != null || !row.stopped) JLinearProgress(fraction, Modifier.semantics {
                    progressBarRangeInfo = if (counted) ProgressBarRangeInfo(done!!.toFloat(), 0f..total!!.toFloat())
                        else if (row.plan != null) ProgressBarRangeInfo(0f, 0f..1f) else ProgressBarRangeInfo.Indeterminate
                }, tone = if (row.blocked) Tone.Caution else null,
                    color = if (row.blocked) null else assistantTrayColor(AssistantTrayTone.Progress))
            }
        }
    }
}

/** Whether [entry] is one of this group's rows. */
private fun NeedsYouGroup.has(entry: NeedsYouEntry): Boolean = entries.any { it.key == entry.key }
