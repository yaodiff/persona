package persona.feature.chat

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.relocation.BringIntoViewRequester
import androidx.compose.foundation.relocation.bringIntoViewRequester
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.LocalMinimumInteractiveComponentSize
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.MeasurePolicy
import androidx.compose.ui.layout.Placeable
import androidx.compose.ui.layout.layoutId
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.constrainHeight
import androidx.compose.ui.unit.constrainWidth
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.semantics.ProgressBarRangeInfo
import androidx.compose.ui.semantics.progressBarRangeInfo
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.Dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.repeatOnLifecycle
import java.time.Instant
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle
import java.time.ZoneId
import java.util.Locale
import persona.core.contracts.*
import persona.core.contracts.api.AssistantInboxDraft
import persona.core.contracts.api.ScheduledTask
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.answer.*
import persona.core.domain.tasks.BackgroundWork
import persona.core.domain.tasks.FinishedEntry
import persona.core.domain.tasks.HoldUnblock
import persona.core.domain.tasks.hold
import persona.core.domain.tasks.TriageStatus
import persona.core.domain.tasks.NeedsYouEntry
import persona.core.domain.tasks.NeedsYouGroup
import persona.core.domain.tasks.needsYouGroups
import persona.core.domain.tasks.RunningEntry
import persona.core.domain.tasks.TaskState
import persona.core.domain.tasks.Interruption
import persona.core.domain.tasks.notDoneReason
import persona.core.domain.tasks.orphanedLine
import persona.core.domain.tasks.planTaskOf
import persona.core.domain.tasks.reviewState
import persona.core.domain.tasks.retryLauncher
import persona.core.domain.tasks.TasksStore
import persona.core.domain.tasks.assistantEntries
import persona.core.domain.tasks.finishedSummary
import persona.core.domain.tasks.showsJustFinished
import persona.core.domain.tasks.triage
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import persona.core.domain.tasks.HeldPushItem
import persona.core.domain.tasks.HeldPushDestination
import persona.core.domain.tasks.heldPushDestination
import kotlinx.coroutines.delay
import persona.core.domain.sort.canApprove

/** The entries above the Assistant's composer (#3387): Needs you, everything waiting on the user, and In progress,
 *  everything running that does not; each thing in one of them only (`persona.core.domain.tasks.assistantEntries`).
 *  Finished (#10922) lists what the conversation handed out that finished in the last 24 hours, seen or not, while it has
 *  rows or its tray is open; Held pushes is the main conversation's, with low-distraction mode on. */
internal enum class AssistantTray { NeedsYou, Running, Finished, HeldPushes }

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
    val todosById = todos.associateBy { it.id }
    val sessionsByKey = host.sessions.associateBy { it.key }
    val bound = plans.flatMap { plan -> plan.tasks.mapIndexedNotNull { index, task ->
        task.todoId?.takeIf { id -> todosById[id]?.planId?.let { it == plan.id } != false }
            ?.let { it to "plan:${plan.id}:$index" }
    } }.toMap()
    val ownTodos = todos.filter(own)
    val rows = ownTodos.map { todo ->
        val agents = host.tasks.agentsOf(todo, sessionsByKey)
        val lead = host.tasks.leadOfAgents(agents)
        val state = host.tasks.stateOfAgents(todo, agents)
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
            if (task.todoId != null && task.todoId in todosById) null
            else TrayProgress("plan:${plan.id}:$index", task.title, plan = plan, text = task.why, blocked = task.error != null)
        }
    }
    val fallback = (pending + rows).distinctBy { it.id }
    val merged = persona.core.domain.tasks.AssistantTaskCards.merge(feed,
        plans.filter { it.conversation == host.conversation }.associateBy { it.id },
        ownTodos, projected)
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

/**
 * Maximum scrollable list height for an expanded [AssistantTrays] panel: bounded by [AssistantTrayTokens.maximumHeight],
 * [AssistantTrayTokens.maximumHeightFraction] of [page], and the vertical room left after [header], [bottomInset],
 * conversation [floor], and [occupied] (everything else in the footer: tray entry capsules, card padding/spacing,
 * route chip, pending cards, queue strip, and composer with attachments, chips, and toolbar).
 */
internal fun assistantTrayMaxHeight(
    page: Dp,
    header: Dp = Tokens.Space.flush,
    occupied: Dp = Tokens.Space.flush,
    bottomInset: Dp = Tokens.Space.flush,
    floor: Dp = Tokens.Space.flush,
    maximum: Dp = AssistantTrayTokens.maximumHeight,
): Dp {
    val share = (page * AssistantTrayTokens.maximumHeightFraction).coerceAtLeast(Tokens.Space.flush)
    val room = (page - header - bottomInset - occupied - floor).coerceAtLeast(Tokens.Space.flush)
    return minOf(share, maximum, room)
}

private object AssistantTrayCardSlot

/** Real task/session/interaction projections, with one bounded panel above its permanent two-entry bar. [requests] are
 *  the cards this screen may show: the conversation's own, and its tasks' and Agents' (each goes to its task's or Agent's
 *  row; one that belongs to neither is not this conversation's). */
@Composable
internal fun AssistantTrays(host: AssistantTaskHost, state: AssistantTrayState,
    requests: List<Interaction>, inbox: List<AssistantInboxDraft>, schedules: List<ScheduledTask>,
    maximumHeight: () -> Dp, busy: Boolean, enabled: Boolean,
    onApprove: (List<Pair<Interaction, Resolution>>) -> Unit, onReview: (Interaction) -> Unit,
    onSend: (TrayDraft) -> Unit, onSchedule: (ScheduledTask) -> Unit,
    modifier: Modifier = Modifier,
    backgroundRecords: Boolean = false, onBackground: (() -> Unit)? = null,
    onPlan: (TaskPlan) -> Unit = {},
    /** The main conversation folds background rounds (#4314): Needs you groups its rows by supervising conversation. */
    groupByOrigin: Boolean = false,
    /** To Decide shares the footer bar but keeps its card count separate from the tray's rows. */
    leading: (@Composable () -> Unit)? = null,
    /**
     * Optional dynamic cap for the expanded panel's scrollable list height given the tray's own measured non-list
     * vertical chrome (capsule bar, [under], outer spacing, and card vertical padding). Read in layout measurement so
     * keyboard frames never trigger recomposition.
     */
    maximumHeightForChrome: ((Dp) -> Dp)? = null,
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
    val features by (host.tasks.features ?: remember { kotlinx.coroutines.flow.MutableStateFlow(emptySet<String>()) }).collectAsStateWithLifecycle()
    val hierarchy = "sessions_hierarchy" in features
    val lnModeEnabled by host.tasks.lnModeEnabled.collectAsStateWithLifecycle()
    val heldGroups by host.tasks.heldPushes.collectAsStateWithLifecycle()
    val heldLoading by host.tasks.heldPushesLoading.collectAsStateWithLifecycle()
    val heldFailed by host.tasks.heldPushesFailed.collectAsStateWithLifecycle()
    val feed = feeds[host.conversation].orEmpty()
    val showHeldEntry = main != null && main == host.conversation && lnModeEnabled
    var p2Open by remember { mutableStateOf(false) }
    val lifecycleOwner = LocalLifecycleOwner.current
    LaunchedEffect(main, host.conversation, lifecycleOwner) {
        if (main == host.conversation && main != null) {
            lifecycleOwner.repeatOnLifecycle(Lifecycle.State.RESUMED) {
                while (true) {
                    host.tasks.refreshPushIntercepts()
                    delay(60_000)
                }
            }
        }
    }
    LaunchedEffect(showHeldEntry, state.expanded) {
        if (!showHeldEntry && state.expanded == AssistantTray.HeldPushes) state.expanded = null
        if (state.expanded != AssistantTray.HeldPushes) p2Open = false
    }
    val everyTodo = main != null && main == host.conversation
    val conversationTaskCards = taskCards[host.conversation].orEmpty()
    val progress = remember(todos, plans, host.conversation, host.sessions, host.tasks, feed, conversationTaskCards, everyTodo) {
        trayProgress(todos, plans.values.toList(), host, feed, conversationTaskCards, everyTodo = everyTodo)
    }
    val progressByTodo = remember(progress) {
        progress.mapNotNull { row -> row.todo?.let { it.id to row } }.toMap()
    }
    val bySession = remember(host.sessions) { host.sessions.associateBy { it.key } }
    val context = LocalContext.current
    val restores = remember(context) { TodoRestores(context) }
    var restoredMap by remember(restores) { mutableStateOf(restores.read()) }
    DisposableEffect(restores) {
        val unregister = restores.observe { restoredMap = it }
        onDispose { unregister() }
    }
    val scope = rememberCoroutineScope()
    // a minute's tick moves tasks across the 48 h shelving line, and a usage limit across its grace, while nothing else changes
    var nowMs by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(Unit) { while (true) { delay(60_000); nowMs = System.currentTimeMillis() } }
    val visibleFeed = remember(feed, later) { feed.filterNot { it.id in later } }
    val entries = remember(host.conversation, main, todos, host.sessions, requests, plans, visibleFeed, nowMs, restoredMap, hierarchy) {
        val clock = maxOf(nowMs, System.currentTimeMillis())
        assistantEntries(host.conversation, main, todos, host.sessions, requests, plans.values, visibleFeed, clock, restoredMap, hierarchy)
    }
    // "Later" is only a local, card-scoped choice; it never hides a newer draft for the same source item.
    // the conversation's own cards (Persona asking) are always the user's; they come first after the asking rows
    val own = remember(requests, host.conversation) { requests.filter { it.sessionId == host.conversation } }
    val asking = remember(entries.needsYou) {
        entries.needsYou.filter { (it as? NeedsYouEntry.Task)?.asks?.isNotEmpty() == true || (it as? NeedsYouEntry.Agent)?.asks?.isNotEmpty() == true }
    }
    val plansWaiting = remember(entries.needsYou) { entries.needsYou.filterIsInstance<NeedsYouEntry.Plan>() }
    val deciding = remember(entries.needsYou, asking) {
        entries.needsYou.filter { it is NeedsYouEntry.Task && it !in asking }.map { it as NeedsYouEntry.Task }
    }
    val drafts = remember(inbox, entries.needsYou) {
        trayDrafts(inbox, entries.needsYou.filterIsInstance<NeedsYouEntry.Message>().map { it.item })
    }
    val needsCount = asking.size + own.size + plansWaiting.size + deciding.size + drafts.size
    val needsGroups = remember(entries.needsYou, host.conversation, groupByOrigin) {
        needsYouGroups(entries.needsYou, host.conversation, groupByOrigin)
    }
    val shownRequests = remember(own, asking) {
        own + asking.flatMap { (it as? NeedsYouEntry.Task)?.asks ?: (it as NeedsYouEntry.Agent).asks }
    }
    val approvals = remember(shownRequests, bySession) {
        shownRequests.mapNotNull { r -> trayApproval(r, bySession[GroupKeys.rawKey(r.sessionKey)])?.let { r to it } }
    }
    val standalone = remember(host.conversation, main, host.sessions, todos, hierarchy) {
        persona.core.domain.tasks.dockAgents(host.conversation, main, host.sessions, todos, hierarchy)
    }
    // Glass folds the shelved tasks into one line under In progress, as iOS (#4339 C18); Brief and Board keep the tray as it was.
    val shelvedLine = PersonaTheme.style == AppStyle.Glass
    // the tray closes once its last thing is handled; tapped while empty it still opens "All caught up"
    LaunchedEffect(needsCount) { if (needsCount == 0 && state.expanded == AssistantTray.NeedsYou) state.expanded = null }
    BackHandler(state.expanded != null) { state.expanded = null }
    // Entry pills keep 48dp touch targets through Compose clickable without inflating layout height, so keep compact
    // token margins around the bar. Measure the entry bar (and optional under-slot) first so an expanded tray card
    // only takes the remaining room and never squeezes the entry bar or pushes the composer below out of view when
    // the IME is up.
    val horizontalPadding = Tokens.Space.m
    val expandedTopPadding = Tokens.Space.xs
    val collapsedTopPadding = Tokens.Space.xxs
    val bottomPadding = Tokens.Space.xxs
    val verticalGap = Tokens.Space.xs
    val cardPadding = Tokens.Space.xs
    val listBottomPadding = Tokens.Space.xs
    val minimumListHeight = Tokens.MinTouch.android
    AssistantTrayLayout(
        modifier = modifier,
        horizontalPadding = horizontalPadding,
        expandedTopPadding = expandedTopPadding,
        collapsedTopPadding = collapsedTopPadding,
        bottomPadding = bottomPadding,
        verticalGap = verticalGap,
        cardPadding = cardPadding,
        minimumListHeight = minimumListHeight,
        maximumListHeight = { trayChromeHeight ->
            maximumHeightForChrome?.invoke(trayChromeHeight) ?: maximumHeight()
        },
    ) {
        state.expanded?.let { tray ->
            JCard(
                Modifier.layoutId(AssistantTrayCardSlot).fillMaxWidth().testTag("assistant-tray-${tray.tag}"),
                padding = cardPadding,
            ) {
                LazyColumn(contentPadding = PaddingValues(bottom = listBottomPadding)) {
                    item {
                        val count = when (tray) {
                            AssistantTray.NeedsYou -> needsCount
                            AssistantTray.Running -> entries.running.size
                            AssistantTray.Finished -> entries.finished.size
                            AssistantTray.HeldPushes -> heldGroups.total
                        }
                        TrayPanelHeader(
                            title = stringResource(tray.label),
                            countText = pluralStringResource(R.plurals.chat_tray_items, count, count),
                            action = if (tray == AssistantTray.NeedsYou && approvals.isNotEmpty()) ({
                                JTextButton(stringResource(R.string.chat_tray_approve_all), { onApprove(approvals) },
                                    enabled = enabled && !busy, modifier = Modifier.testTag("assistant-approve-all"))
                            }) else if (tray == AssistantTray.Running || tray == AssistantTray.Finished) ({
                                JTextButton(stringResource(R.string.chat_tasks_dock_all, progress.size + standalone.size),
                                    { host.sheet.value = TaskSheet.Board }, modifier = Modifier.testTag("assistant-task-board"))
                            }) else if (tray == AssistantTray.HeldPushes) ({
                                JTextButton(stringResource(R.string.chat_tasks_retry), { host.tasks.refreshPushIntercepts() },
                                    enabled = !heldLoading, modifier = Modifier.testTag("assistant-held-retry"))
                            }) else null,
                        )
                    }
                    when (tray) {
                        AssistantTray.NeedsYou -> {
                            if (needsCount == 0) item { JEmptyState(stringResource(R.string.chat_tray_caught_up), "", icon = PhIcons.CheckCircle) }
                            // #4314: one group, as before; with several supervising conversations, each under its title,
                            // this conversation's first (its own cards, plans and drafts are in it), the rows unchanged.
                            var shownGroupCount = 0
                            for (group in needsGroups) {
                                val showGroupHeader = needsGroups.size > 1 && (group.entries.isNotEmpty() || (group.conversation == host.conversation && (own.isNotEmpty() || drafts.isNotEmpty())))
                                if (showGroupHeader) {
                                    val firstGroup = shownGroupCount == 0
                                    shownGroupCount++
                                    item(key = "needs-group:${group.conversation}") {
                                        TrayGroupHeader(
                                            host.sessions.firstOrNull { it.sessionId == group.conversation }?.displayTitle?.takeIf(String::isNotBlank)
                                                ?: stringResource(R.string.chat_tray_other_conversation),
                                            first = firstGroup,
                                            modifier = Modifier.testTag("assistant-needs-group-${group.conversation}"),
                                        )
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
                                    val gate = todo.hold?.takeIf { it.unblock == HoldUnblock.User }?.gate
                                    when {
                                        todo.ask?.outcome == "decide" -> TaskDecisionCard(todo, host, enabled) { openTask(host, todo) }
                                        todo.reviewState == "user_check" && todo.ask?.askedAt?.isNotBlank() == true ->
                                            TaskOwnerDecision(todo, host, enabled = enabled, onOpen = { openTask(host, todo) })
                                        entry.status == TriageStatus.Failed || entry.status == TriageStatus.Interrupted ->
                                            TrayOutcomeRow(entry, host, enabled, taskBusy)
                                        else -> {
                                            val readyToVerify = stringResource(R.string.chat_tray_ready_to_verify)
                                            val parsed = todo.ask?.verdict?.let { trayVerdictText(it, readyToVerify) }
                                            val subtitle = parsed?.text ?: gate ?: row?.text.orEmpty()
                                            TrayDecisionRow(
                                                todo.title,
                                                subtitle,
                                                R.string.chat_tray_review,
                                                "assistant-review-${row?.id ?: "todo:${todo.id}"}",
                                                enabled,
                                                verdictTone = parsed?.tone,
                                                onOpen = { openTask(host, todo) },
                                            ) { openTask(host, todo) }
                                        }
                                    }
                                }
                                val groupDrafts = drafts.takeIf { group.conversation == host.conversation }.orEmpty()
                                if (groupDrafts.isNotEmpty()) item {
                                    TrayGroupHeader(stringResource(R.string.chat_tray_drafts), first = shownGroupCount == 0 && asking.isEmpty() && own.isEmpty() && plansWaiting.isEmpty() && deciding.isEmpty())
                                }
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
                        // How a task ended, instead of a card in the chat (#8237): what finished in the last 24 hours,
                        // newest first, seen or not; one nobody opened yet is new (#10922). The board lists the older ones.
                        AssistantTray.Finished -> {
                            if (entries.finished.isEmpty()) item {
                                JEmptyState(stringResource(R.string.chat_tray_finished_empty), "", icon = PersonaIcons.success)
                            }
                            items(entries.finished, key = { "finished:${it.key}" }) { entry ->
                                FinishedRow(entry, host, feed, bySession, scope)
                            }
                        }
                        AssistantTray.HeldPushes -> {
                            if (heldFailed) item {
                                JRow(stringResource(R.string.chat_tray_held_load_failed), modifier = Modifier.testTag("assistant-held-error"))
                            }
                            if (heldGroups.total == 0 && !heldLoading) item {
                                JEmptyState(stringResource(R.string.chat_tray_held_empty), "", icon = PhIcons.BellSimple)
                            }
                            if (heldGroups.p1.isNotEmpty()) {
                                item { JSectionHeader(stringResource(R.string.chat_tray_held_p1, heldGroups.p1.size)) }
                                items(heldGroups.p1, key = { it.id }) { item ->
                                    HeldPushRow(item, host, todos)
                                }
                            }
                            if (heldGroups.p2.isNotEmpty()) {
                                item {
                                    Row(verticalAlignment = Alignment.CenterVertically) {
                                        JSectionHeader(stringResource(R.string.chat_tray_held_p2, heldGroups.p2.size), modifier = Modifier.weight(1f))
                                        JDisclosureButton(p2Open, { p2Open = !p2Open }, stringResource(R.string.chat_tray_held_p2_toggle))
                                    }
                                }
                                if (p2Open) items(heldGroups.p2, key = { it.id }) { item ->
                                    HeldPushRow(item, host, todos)
                                }
                            }
                        }
                        AssistantTray.Running -> {
                            items(entries.running, key = { it.key }) { entry ->
                                when (entry) {
                                    is RunningEntry.Task -> {
                                        if (entry.status == TriageStatus.UsageLimit) {
                                            TrayUsageLimitRow(entry, host, enabled, taskBusy, nowMs)
                                        } else {
                                            val projected = progressByTodo[entry.todo.id] ?: TrayProgress("todo:${entry.todo.id}", entry.todo.title, todo = entry.todo)
                                            // Only replace the projection's state when a shared Agent's requests belong elsewhere (#7531).
                                            val row = if (entry.hasAgentsElsewhere) projected.copy(blocked = false,
                                                stopped = entry.status == TriageStatus.Unread || entry.status == TriageStatus.FollowUp)
                                            else projected
                                            TrayProgressRow(row, activity = entry.activity, waiting = entry.waiting,
                                                working = entry.status == TriageStatus.Working, supervisorReview = entry.supervisorReview,
                                                onClick = { openTask(host, entry.todo) })
                                        }
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
                            if (schedules.isNotEmpty() || backgroundRecords) item {
                                TrayGroupHeader(stringResource(R.string.chat_tray_quiet), first = entries.running.isEmpty() && !(shelvedLine && entries.shelved.isNotEmpty()))
                            }
                            items(schedules, key = { "schedule:${it.id}" }) { schedule ->
                                JRow(schedule.title, subtitle = schedule.lastError?.takeIf(String::isNotBlank)
                                    ?: schedule.nextRun?.let { stringResource(R.string.chat_tray_next_run, formatTrayScheduleTime(it, currentLocale)) }
                                    ?: schedule.lastStatus, subtitleLines = 2, onClick = { onSchedule(schedule) },
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
        // Content-width entries at the leading edge; overflowing trays or large fonts scroll with soft edge fades.
        AssistantTrayBar(Modifier.testTag("assistant-tray-bar")) {
            leading?.invoke()
            // Finished is there while it has rows, or its tray is open (an emptied tray stays reachable till it closes)
            for (tray in AssistantTray.entries.filter {
                (it != AssistantTray.HeldPushes || showHeldEntry) &&
                    (it != AssistantTray.Finished || entries.finished.isNotEmpty() || state.expanded == AssistantTray.Finished)
            }) key(tray) {
                // the open entry shows whole, its count and caret included, however far the row scrolls for it (the web's
                // scrollIntoView): with three entries a phone's row can end in the middle of the last one
                val reveal = remember { BringIntoViewRequester() }
                val open = state.expanded == tray
                LaunchedEffect(open) { if (open) reveal.bringIntoView() }
                AssistantTrayEntry(stringResource(tray.label), when (tray) {
                    AssistantTray.NeedsYou -> needsCount
                    AssistantTray.Running -> entries.running.size
                    AssistantTray.Finished -> entries.finished.size
                    AssistantTray.HeldPushes -> heldGroups.total
                }, when (tray) {
                    AssistantTray.NeedsYou -> AssistantTrayTone.Pending
                    AssistantTray.Running -> AssistantTrayTone.Progress
                    AssistantTray.Finished -> AssistantTrayTone.Finished
                    AssistantTray.HeldPushes -> AssistantTrayTone.Pending
                }, open,
                    stringResource(if (open) R.string.chat_tray_expanded else R.string.chat_tray_collapsed),
                    { state.toggle(tray) }, Modifier.testTag("assistant-entry-${tray.tag}").bringIntoViewRequester(reveal),
                    enabled = true)
            }
        }
        under?.invoke()
    }
}

/**
 * Inline layout for the optional expanded tray card above the permanent entry bar (and optional [under] slot).
 * Non-card children are measured first so the entry bar stays visible; the expanded card receives at most
 * the remaining height inside [constraints] and [maximumListHeight], collapsing cleanly to zero height
 * when no list room remains instead of drawing an empty padded shell or crowding the composer.
 */
@Composable
private inline fun AssistantTrayLayout(
    modifier: Modifier,
    horizontalPadding: Dp,
    expandedTopPadding: Dp,
    collapsedTopPadding: Dp,
    bottomPadding: Dp,
    verticalGap: Dp,
    cardPadding: Dp,
    minimumListHeight: Dp,
    noinline maximumListHeight: (Dp) -> Dp,
    content: @Composable () -> Unit,
) {
    val latestMax by rememberUpdatedState(maximumListHeight)
    val policy = remember(
        horizontalPadding, expandedTopPadding, collapsedTopPadding, bottomPadding, verticalGap, cardPadding, minimumListHeight,
    ) {
        assistantTrayMeasurePolicy(
            horizontalPadding, expandedTopPadding, collapsedTopPadding, bottomPadding, verticalGap, cardPadding, minimumListHeight,
        ) {
            latestMax(it)
        }
    }
    Layout(content = content, modifier = modifier, measurePolicy = policy)
}

private fun assistantTrayMeasurePolicy(
    horizontalPadding: Dp,
    expandedTopPadding: Dp,
    collapsedTopPadding: Dp,
    bottomPadding: Dp,
    verticalGap: Dp,
    cardPadding: Dp,
    minimumListHeight: Dp,
    maximumListHeight: (Dp) -> Dp,
) = MeasurePolicy { measurables, constraints ->
    val horizontalPadPx = horizontalPadding.roundToPx()
    val expandedTopPadPx = expandedTopPadding.roundToPx()
    val collapsedTopPadPx = collapsedTopPadding.roundToPx()
    val bottomPadPx = bottomPadding.roundToPx()
    val gapPx = verticalGap.roundToPx()
    val cardVertPadPx = cardPadding.roundToPx() * 2
    val minListHeightPx = minimumListHeight.roundToPx()
    val contentMaxWidth = (constraints.maxWidth - horizontalPadPx * 2).coerceAtLeast(0)
    val childLoose = constraints.copy(minWidth = 0, maxWidth = contentMaxWidth, minHeight = 0)

    val placeables = arrayOfNulls<Placeable>(measurables.size)
    var cardIndex = -1
    var nonCardHeight = 0
    var nonCardCount = 0
    for (index in measurables.indices) {
        val measurable = measurables[index]
        if (measurable.layoutId === AssistantTrayCardSlot) {
            cardIndex = index
            continue
        }
        val gapBefore = if (nonCardCount > 0) gapPx else 0
        val maxChildHeight = if (constraints.hasBoundedHeight) {
            (constraints.maxHeight - collapsedTopPadPx - bottomPadPx - nonCardHeight - gapBefore).coerceAtLeast(0)
        } else {
            constraints.maxHeight
        }
        val placeable = measurable.measure(childLoose.copy(maxHeight = maxChildHeight))
        placeables[index] = placeable
        nonCardHeight += gapBefore + placeable.height
        nonCardCount++
    }

    var cardPlaced = false
    if (cardIndex >= 0) {
        val cardGapPx = if (nonCardCount > 0) gapPx else 0
        val trayChromeHeightPx = nonCardHeight + expandedTopPadPx + bottomPadPx + cardGapPx + cardVertPadPx
        val requestedCapPx = minOf(maximumListHeight(trayChromeHeightPx.toDp()), AssistantTrayTokens.maximumHeight)
            .roundToPx().coerceAtLeast(0)
        val constraintListRoomPx = if (constraints.hasBoundedHeight) {
            (constraints.maxHeight - trayChromeHeightPx).coerceAtLeast(0)
        } else {
            requestedCapPx
        }
        val finalListMaxPx = minOf(requestedCapPx, constraintListRoomPx)
        // Only place the expanded tray card when at least one touch-height row fits; otherwise collapse it
        // cleanly to zero height so no empty padded sliver appears and the composer keeps full priority.
        if (finalListMaxPx >= minListHeightPx) {
            val cardPlaceable = measurables[cardIndex].measure(
                childLoose.copy(maxHeight = finalListMaxPx + cardVertPadPx),
            )
            placeables[cardIndex] = cardPlaceable
            cardPlaced = true
        }
    }

    val maxChildWidth = placeables.maxOfOrNull { it?.width ?: 0 } ?: 0
    val totalWidth = constraints.constrainWidth(maxChildWidth + horizontalPadPx * 2)
    var placedCount = 0
    var placedContentHeight = 0
    for (placeable in placeables) {
        if (placeable != null) {
            if (placedCount > 0) placedContentHeight += gapPx
            placedContentHeight += placeable.height
            placedCount++
        }
    }
    val topPadPx = if (cardPlaced) expandedTopPadPx else if (placedCount > 0) collapsedTopPadPx else 0
    val appliedBottomPadPx = if (placedCount > 0) bottomPadPx else 0
    val totalHeight = topPadPx + placedContentHeight + appliedBottomPadPx
    layout(totalWidth, constraints.constrainHeight(totalHeight)) {
        var y = topPadPx
        var indexPlaced = 0
        for (placeable in placeables) {
            if (placeable != null) {
                if (indexPlaced > 0) y += gapPx
                placeable.placeRelative(horizontalPadPx, y)
                y += placeable.height
                indexPlaced++
            }
        }
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
    // At the tray bar's inset, so the chip lines up with its chips above it without leaking 48dp box padding.
    CompositionLocalProvider(LocalMinimumInteractiveComponentSize provides Dp.Unspecified) {
        Row(modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.xxs)) {
            JChip(stringResource(R.string.chat_tasks_route_chip, task.title), Modifier.testTag("assistant-route-chip"),
                icon = PhIcons.PaperPlaneTilt, accent = true, onRemove = { host.tasks.setRoute(host.conversation, null) },
                removeLabel = stringResource(R.string.chat_tasks_route_clear))
        }
    }
}

private val AssistantTray.label get() = when (this) {
    AssistantTray.NeedsYou -> R.string.chat_tray_entry_needs_you
    AssistantTray.Running -> R.string.chat_tray_entry_running
    AssistantTray.Finished -> R.string.chat_tray_entry_finished
    AssistantTray.HeldPushes -> R.string.chat_tray_held_entry
}

/** The test tag part: `assistant-entry-needs` / `assistant-tray-running` / `assistant-tray-finished`. */
internal val AssistantTray.tag get() = when (this) {
    AssistantTray.NeedsYou -> "needs"
    AssistantTray.Running -> "running"
    AssistantTray.Finished -> "finished"
    AssistantTray.HeldPushes -> "held-pushes"
}

@Composable
private fun HeldPushRow(item: HeldPushItem, host: AssistantTaskHost, todos: List<Todo>) {
    val uriHandler = LocalUriHandler.current
    val destination = heldPushDestination(item.record, todos.mapTo(mutableSetOf()) { it.id })
    val action = when (destination) {
        is HeldPushDestination.Todo -> todos.firstOrNull { it.id == destination.id }?.let { todo -> { openTask(host, todo) } }
        is HeldPushDestination.Session -> ({ host.onOpen(SessionKey(destination.machineId, destination.sessionId)) })
        is HeldPushDestination.Url -> ({ uriHandler.openUri(destination.url) })
        null -> null
    }
    val time = remember(item.date) {
        DateTimeFormatter.ofLocalizedTime(FormatStyle.SHORT).withLocale(Locale.getDefault()).withZone(ZoneId.systemDefault()).format(item.date)
    }
    JRow(modifier = Modifier.testTag("assistant-held-${item.id}"), onClick = action, trailing = {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(time, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
            if (action != null) JIcon(PersonaIcons.forward, decorative = true, size = Tokens.Space.m, tint = PersonaTheme.palette.ink3)
        }
    }) {
        Column(Modifier.weight(1f)) {
            Text(item.summaries.firstOrNull()?.takeIf(String::isNotBlank) ?: stringResource(R.string.chat_tray_held_untitled),
                style = PersonaTheme.type.body, color = PersonaTheme.palette.ink, maxLines = 3, overflow = TextOverflow.Ellipsis)
            item.summaries.drop(1).forEach { summary ->
                Text(summary, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            Text(listOfNotNull(
                when (item.record.reason) {
                    "ln_dedup_12h" -> stringResource(R.string.chat_tray_held_reason_dedup)
                    "ln_p2_silent" -> stringResource(R.string.chat_tray_held_reason_p2)
                    else -> stringResource(R.string.chat_tray_held_reason_p1)
                },
                item.record.manager?.takeIf(String::isNotBlank),
            ).joinToString(" · "), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
    }
}

@Composable
private fun TrayPanelHeader(
    title: String,
    countText: String,
    action: (@Composable RowScope.() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    Row(
        Modifier.fillMaxWidth()
            .padding(start = Tokens.Space.m, end = Tokens.Space.m, top = Tokens.Space.s, bottom = Tokens.Space.xs)
            .semantics { heading() }
            .testTag("assistant-tray-header"),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm),
    ) {
        Text(
            title,
            Modifier.weight(1f, fill = false),
            style = PersonaTheme.type.callout.copy(fontWeight = FontWeight.SemiBold),
            color = palette.ink,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
        Text("·", style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1)
        Text(
            countText,
            style = PersonaTheme.type.caption,
            color = palette.ink3,
            maxLines = 1,
        )
        Spacer(Modifier.weight(1f))
        if (action != null) Row(verticalAlignment = Alignment.CenterVertically, content = action)
    }
}

@Composable
private fun TrayGroupHeader(title: String, first: Boolean, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    Column(modifier.fillMaxWidth()) {
        if (!first) JDivider(Modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.xs))
        Text(
            title,
            modifier = Modifier
                .fillMaxWidth()
                .padding(
                    start = Tokens.Space.m,
                    end = Tokens.Space.m,
                    top = if (first) Tokens.Space.xs else Tokens.Space.xxs,
                    bottom = Tokens.Space.xxs,
                )
                .semantics { heading() },
            style = PersonaTheme.type.labelSmall.copy(fontWeight = FontWeight.Medium),
            color = palette.ink3,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

@Composable
private fun TrayDecisionRow(
    title: String,
    subtitle: String,
    action: Int,
    tag: String,
    enabled: Boolean,
    verdictTone: VerdictTone? = null,
    onOpen: (() -> Unit)? = null,
    onClick: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val shape = PersonaTheme.shapes.controlShape
    Row(
        Modifier.fillMaxWidth()
            .heightIn(min = Tokens.MinTouch.android)
            .clip(shape)
            .then(if (onOpen != null && enabled) Modifier.clickable(role = Role.Button, onClick = onOpen) else Modifier)
            .stableAlpha(if (enabled) 1f else 0.45f)
            .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        Box(Modifier.size(Tokens.Size.iconXS), contentAlignment = Alignment.Center) {
            when (verdictTone) {
                VerdictTone.Done -> JIcon(PersonaIcons.success, decorative = true, size = Tokens.Size.markS, tint = palette.positive)
                VerdictTone.Cannot -> JIcon(PersonaIcons.error, decorative = true, size = Tokens.Size.markS, tint = palette.danger.dot)
                VerdictTone.Decide -> JIcon(PersonaIcons.help, decorative = true, size = Tokens.Size.markS, tint = palette.needsYou.dot)
                else -> AssistantTrayMark(AssistantTrayTone.Pending)
            }
        }
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            Text(
                title,
                style = PersonaTheme.type.callout.copy(fontWeight = FontWeight.Medium),
                color = palette.ink,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            if (subtitle.isNotBlank()) {
                Text(
                    subtitle,
                    style = PersonaTheme.type.caption,
                    color = palette.ink2,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
        JButton(
            stringResource(action),
            onClick,
            Modifier.testTag(tag),
            kind = JButtonKind.Secondary,
            size = JButtonSize.Small,
            enabled = enabled,
        )
    }
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

/** Formats a wire schedule timestamp for the tray and its detail sheet: clock alone today, month/day + clock on another day. */
internal fun formatTrayScheduleTime(raw: String, locale: Locale, zone: ZoneId = ZoneId.systemDefault(), now: Instant = Instant.now()): String {
    val at = parseTime(raw) ?: return raw
    val today = at.atZone(zone).toLocalDate() == now.atZone(zone).toLocalDate()
    return LocalizedFormats.format(at, if (today) LocalizedFormats.TIME else LocalizedFormats.MONTH_DAY_TIME, locale, zone)
}

@Composable
private fun TrayProgressRow(row: TrayProgress, activity: BackgroundWork? = null, waiting: Boolean = false,
    working: Boolean = false, supervisorReview: Boolean = false, onClick: () -> Unit) {
    val live = !row.done && !row.cancelled
    val work = activity.takeIf { live }
    val detail = trayBackgroundLine(work) ?: row.text
    val palette = PersonaTheme.palette
    JRow(onClick = onClick, modifier = Modifier.testTag("assistant-progress-${row.id}"), chevron = false, trailing = {
        if (row.done) JIcon(PersonaIcons.done, stringResource(if (row.todo?.supervision?.state == "accepted")
            R.string.chat_tray_accepted else R.string.chat_tray_done), tint = palette.positive)
        // judged finished and with its supervisor for review (`RunningEntry.Task.supervisorReview`): the row says whose turn it is
        else Text(stringResource(if (work != null) R.string.chat_tray_working else if (live && waiting) R.string.chat_tray_waiting
            else if (live && supervisorReview) R.string.chat_tray_supervisor_review
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

/**
 * A usage limit still in force (#8237): in progress, caution words, and when it resets. Continue sits under the row,
 * its own tap, only while nothing continues the turn. [nowMs] is the tray's minute tick.
 */
@Composable
private fun TrayUsageLimitRow(entry: RunningEntry.Task, host: AssistantTaskHost, enabled: Boolean, taskBusy: Set<String>, nowMs: Long) {
    val palette = PersonaTheme.palette
    val todo = entry.todo
    val lead = host.tasks.agentsOf(todo, host.sessions).firstOrNull { it.isUsageLimited(nowMs) }
    val subtitle = lead?.let { usageLimitRowSubtitle(it, nowMs) }?.takeIf { it.isNotBlank() }
    val retrying = TasksStore.retryKey(todo.id) in taskBusy
    Column(Modifier.fillMaxWidth().testTag("assistant-tray-usage-limit")) {
        JRow(todo.title, subtitle = subtitle, onClick = { openTask(host, todo) }, trailing = {
            Text(stringResource(R.string.chat_usage_limit), style = PersonaTheme.type.micro, color = palette.cautionText, maxLines = 1)
        })
        if (lead != null && !lead.usageLimitContinues()) {
            Row(Modifier.padding(start = Tokens.Space.m, end = Tokens.Space.s, bottom = Tokens.Space.xs)) {
                JButton(stringResource(R.string.chat_tasks_continue), { host.tasks.retryTask(todo, host.onError) },
                    size = JButtonSize.Small, enabled = enabled, loading = retrying, modifier = Modifier.testTag("assistant-tray-continue"))
            }
        }
    }
}

/**
 * A failed start or a cut-off turn (#8237): a danger wash, ink words, and its own buttons under the row. The row opens
 * the task. Retry and Remove, or Continue, are separate taps.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun TrayOutcomeRow(entry: NeedsYouEntry.Task, host: AssistantTaskHost, enabled: Boolean, taskBusy: Set<String>) {
    val palette = PersonaTheme.palette
    val todo = entry.todo
    val failed = entry.status == TriageStatus.Failed
    val lead = host.tasks.lead(todo, host.sessions)
    val cut = Interruption.of(lead)
    val plans by host.tasks.plans.collectAsStateWithLifecycle()
    val features by (host.tasks.features ?: remember { MutableStateFlow(emptySet<String>()) }).collectAsStateWithLifecycle()
    val failedTask = if (failed) planTaskOf(todo, plans) else null
    val instead = retryLauncher(failedTask, features)
    val why = if (failed) startFailureCodeLine(failedTask?.errorCode, failedTask?.errorArgs)
        ?: startFailureRowLine(todo.startError.orEmpty())
        else if (cut?.invalid == true) cut.reason.text() else interruptedWhy(lead?.turnError)
    val retrying = TasksStore.retryKey(todo.id) in taskBusy
    Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(Tokens.Radius.card)).background(palette.danger.tint)
        .testTag(if (failed) "assistant-tray-failed" else "assistant-tray-interrupted")) {
        JRow(todo.title, subtitle = why.takeIf { it.isNotBlank() }, subtitleLines = if (cut?.invalid == true) Int.MAX_VALUE else 2,
            onClick = { openTask(host, todo) },
            leading = { JIcon(PhIcons.WarningCircle, decorative = true, size = Tokens.Size.iconXS, tint = palette.danger.dot) },
            trailing = {
                Text((if (cut?.invalid == true) CoreText.of(CoreText.Key.ApiConversationInvalidLabel)
                    else (if (failed) TaskState.Failed else TaskState.Interrupted).label).text(),
                    style = PersonaTheme.type.micro, color = palette.ink, maxLines = 1)
            })
        if (failed && failedTask != null) TaskShellQuestion(todo, failedTask, host.tasks,
            Modifier.padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs))
        val actions: @Composable () -> Unit = {
            if (failed) {
                if (instead != null) JButton(
                    stringResource(persona.core.designsystem.R.string.designsystem_use_launcher_instead, instead),
                    { host.tasks.retryTaskOnLauncher(todo, instead, host.onError) },
                    modifier = Modifier.testTag("assistant-tray-retry-launcher"),
                    size = JButtonSize.Small,
                    icon = PersonaIcons.retry,
                    enabled = enabled && !retrying,
                    loading = retrying,
                )
                JButton(stringResource(R.string.chat_tasks_retry), { host.tasks.retryTask(todo, host.onError) },
                    kind = if (instead == null) JButtonKind.Primary else JButtonKind.Secondary,
                    size = JButtonSize.Small, enabled = enabled, loading = retrying, modifier = Modifier.testTag("assistant-tray-retry"))
                JButton(stringResource(R.string.chat_tasks_remove), { host.tasks.removeTask(todo, host.onError) },
                    kind = JButtonKind.Destructive, size = JButtonSize.Small, enabled = enabled,
                    modifier = Modifier.testTag("assistant-tray-remove"))
            } else if (cut?.canContinue != false) {
                JButton(stringResource(R.string.chat_tasks_continue), { host.tasks.retryTask(todo, host.onError) },
                    size = JButtonSize.Small, enabled = enabled, loading = retrying, modifier = Modifier.testTag("assistant-tray-continue"))
            }
        }
        val actionModifier = Modifier.padding(start = Tokens.Space.m, end = Tokens.Space.s, bottom = Tokens.Space.s)
        if (instead == null) Row(actionModifier, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) { actions() }
        else FlowRow(actionModifier, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) { actions() }
    }
}

/**
 * A row of Finished (#10922): a task the conversation handed out that finished in the last 24 hours, with what it did.
 * One nobody has opened since it finished ([FinishedEntry.fresh], the To-dos page's Just finished) is new: the Just
 * finished dot and word in Unread's green, as the Agents list draws it (#9786); a seen one the done check and Done.
 * Opening it opens the task and, while it is new, marks it seen where the hub keeps the mark: it stays, as a seen row.
 */
@Composable
private fun FinishedRow(entry: FinishedEntry, host: AssistantTaskHost, feed: List<FeedItem>, sessions: Map<SessionKey, Session>,
    scope: kotlinx.coroutines.CoroutineScope) {
    val palette = PersonaTheme.palette
    val todo = entry.todo
    val lead = host.tasks.lead(todo, sessions.values.toList())
    val summary = finishedSummary(todo, feed, lead)
    val unseen = palette.status(Status.Unread)
    JRow(todo.title, subtitle = summary, subtitleLines = 2, onClick = {
        openTask(host, todo)
        if (entry.fresh && host.tasks.features?.value?.contains("todo_seen") == true) scope.launch {
            try {
                val placed = todo.triage({ sessions[it] }, System.currentTimeMillis())
                if (todo.showsJustFinished(placed, System.currentTimeMillis())) host.tasks.setTodoSeen(todo.id, true, after = placed.last)
            } catch (e: CancellationException) {
                throw e
            } catch (_: Throwable) {
            }
        }
    }, modifier = Modifier.testTag(if (entry.fresh) "assistant-finished-row-new" else "assistant-finished-row"), leading = {
        if (entry.fresh) {
            Box(Modifier.size(Tokens.Size.iconXS), contentAlignment = Alignment.Center) {
                JStatusDot(Status.Unread, size = AssistantTrayTokens.markSize)
            }
        } else JIcon(PersonaIcons.done, decorative = true, size = Tokens.Size.iconXS, tint = palette.positive)
    }, trailing = {
        Text(if (entry.fresh) CoreText.of(CoreText.Key.StateJustFinished).text() else stringResource(R.string.chat_tray_finished_done),
            style = PersonaTheme.type.micro, color = if (entry.fresh) unseen.text else palette.ink3, maxLines = 1)
    })
}
