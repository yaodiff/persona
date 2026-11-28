package persona.feature.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.Todo
import persona.core.contracts.TodoAsk
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.tasks.AssistantExtra
import persona.core.domain.tasks.AssistantTaskCards
import persona.core.domain.tasks.decisionOptions
import persona.core.domain.tasks.reviewState
import persona.core.domain.tasks.todoRuleCard

/** Whether [TaskOwnerDecision] renders a primary action button for this task, so bottom sheet actions demote to secondary. */
internal fun Todo.hasOwnerPrimaryAction(): Boolean {
    val active = ask ?: return false
    if (active.outcome == "decide") return !isDone && cancelledAt == null
    return active.askedAt.isNotBlank() && (active.outcome != "decide" || !active.recommend.isNullOrBlank())
}

/**
 * Suppresses stale or contradictory receipt cards inside Task Detail when the task already displays an active
 * owner acceptance/decision card, while preserving receipt cards and navigation when no owner ask supersedes them.
 */
internal fun shouldShowTaskReceiptInDetail(todo: Todo): Boolean = todo.ask == null

/** Acceptance carries the exact round the user read, independently of any later task refresh. */
@Composable
internal fun TaskOwnerDecision(
    todo: Todo,
    host: AssistantTaskHost,
    prUrl: String? = null,
    enabled: Boolean = true,
    onOpen: (() -> Unit)? = null,
) {
    val ask = todo.ask ?: return
    if (ask.outcome == "decide") {
        TaskDecisionCard(todo, host, enabled, onOpen)
        return
    }
    val busy by host.tasks.busy.collectAsStateWithLifecycle()
    val resources = LocalResources.current
    val palette = PersonaTheme.palette
    val parsed = remember(ask.verdict) { parseTaskVerdict(ask.verdict) }
    val userCheck = todo.reviewState == "user_check"
    val (icon, tint) = when {
        parsed.tone == VerdictTone.Done || (parsed.tone == VerdictTone.Neutral && ask.outcome == "done") ->
            PersonaIcons.success to palette.positive
        parsed.tone == VerdictTone.Cannot || (parsed.tone == VerdictTone.Neutral && ask.outcome == "cannot") ->
            PersonaIcons.error to palette.danger.dot
        parsed.tone == VerdictTone.Decide -> PersonaIcons.help to palette.needsYou.dot
        else -> PersonaIcons.info to palette.needsYou.dot
    }
    val pr = (todo.prUrl?.takeIf(String::isNotBlank) ?: prUrl)?.let { url -> trayPullRequestNumber(url)?.let { url to it } }
    JCard(Modifier.fillMaxWidth().testTag("assistant-owner-decision-${todo.id}"), padding = Tokens.Space.l) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Row(
                verticalAlignment = Alignment.Top,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
            ) {
                JIcon(
                    icon,
                    decorative = true,
                    size = Tokens.Size.iconXS,
                    tint = tint,
                    modifier = Modifier.padding(top = Tokens.Space.xxs),
                )
                Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    if (onOpen != null) {
                        Text(todo.title, style = PersonaTheme.type.bodyStrong, color = palette.ink)
                    }
                    Text(
                        parsed.text,
                        style = if (onOpen != null) PersonaTheme.type.callout else PersonaTheme.type.bodyStrong,
                        color = palette.ink,
                    )
                    ask.summary?.takeIf(String::isNotBlank)?.let { summary ->
                        Text(summary, style = PersonaTheme.type.caption, color = palette.ink2)
                    }
                    ask.recommend?.takeIf(String::isNotBlank)?.let { recommendation ->
                        Text(
                            stringResource(R.string.chat_tray_recommended, recommendation),
                            style = PersonaTheme.type.caption,
                            color = palette.ink2,
                        )
                    }
                }
            }
            if (ask.askedAt.isNotBlank() && (ask.outcome != "decide" || !ask.recommend.isNullOrBlank())) {
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    JButton(
                        stringResource(if (userCheck) R.string.chat_tray_pass else R.string.chat_tray_accept),
                        {
                            host.tasks.acceptTask(todo, host.onError) {
                                host.onError(IllegalStateException(resources.getString(R.string.chat_tray_accept_changed)))
                            }
                        },
                        Modifier.fillMaxWidth().testTag("assistant-accept-${todo.id}"),
                        kind = JButtonKind.Primary,
                        size = JButtonSize.Medium,
                        enabled = enabled,
                        loading = "accept:${todo.id}" in busy,
                    )
                    if (onOpen != null) {
                        JButton(
                            stringResource(R.string.chat_link_open),
                            onOpen,
                            Modifier.testTag("assistant-open-${todo.id}"),
                            kind = JButtonKind.Secondary,
                            size = JButtonSize.Small,
                            enabled = enabled,
                        )
                    }
                    if (pr != null) {
                        val uri = LocalUriHandler.current
                        JButton(
                            stringResource(R.string.chat_tray_pr_number, pr.second),
                            { uri.openUri(pr.first) },
                            Modifier.testTag("receipt-pr"),
                            kind = JButtonKind.Secondary,
                            size = JButtonSize.Small,
                            icon = PersonaIcons.pullRequest,
                        )
                    }
                }
            } else if (pr != null) {
                val uri = LocalUriHandler.current
                JButton(
                    stringResource(R.string.chat_tray_pr_number, pr.second),
                    { uri.openUri(pr.first) },
                    Modifier.testTag("receipt-pr"),
                    kind = JButtonKind.Secondary,
                    size = JButtonSize.Small,
                    icon = PersonaIcons.pullRequest,
                )
            }
        }
    }
}

/**
 * The owner's decision, with the rule it suggests under it once the owner gave the same go-ahead three times (#4929).
 * A decision with options is answered on the row (#3804); one from an older hub keeps the recommendation's button.
 */
@Composable
internal fun TaskDecisionCard(todo: Todo, host: AssistantTaskHost, enabled: Boolean = true, onOpen: (() -> Unit)? = null) {
    val ask = todo.ask?.takeIf { it.outcome == "decide" } ?: return
    if (todo.isDone || todo.cancelledAt != null) return
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        if (ask.decisionOptions.isNotEmpty()) TaskDecisionAnswers(todo, ask, host, enabled, onOpen)
        else TaskDecisionRecommendation(todo, ask, host, enabled, onOpen)
        RuleSuggestionLine(todoRuleCard(todo.id, ask.askedAt), ask.suggestion, host.tasks, host.onError, todo.manager, enabled)
    }
}

/**
 * A decision held for the next briefing (#4930, docs/assistant/todos.md "Decisions that can wait"): nothing was pushed for
 * it, and it is answered here all the same.
 */
@Composable
internal fun ForBriefingMark(modifier: Modifier = Modifier) {
    Row(modifier.testTag("decision-for-briefing"), verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        JIcon(PersonaIcons.schedule, decorative = true, size = Tokens.Space.m, tint = PersonaTheme.palette.ink3)
        Text(stringResource(R.string.chat_decision_for_briefing), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/** The owner's decision keeps direct completion separate from following Persona's recommendation. */
@Composable
private fun TaskDecisionRecommendation(todo: Todo, ask: TodoAsk, host: AssistantTaskHost, enabled: Boolean, onOpen: (() -> Unit)?) {
    val busy by host.tasks.busy.collectAsStateWithLifecycle()
    val sending = "accept:${todo.id}" in busy
    var failed by remember(todo.id, ask) { mutableStateOf(false) }
    var changed by remember(todo.id, ask) { mutableStateOf(false) }
    val onChanged = { changed = true }
    val parsed = remember(ask.verdict) { parseTaskVerdict(ask.verdict) }
    JCard(Modifier.fillMaxWidth().testTag("assistant-task-decision-${todo.id}"), padding = Tokens.Space.l) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (onOpen != null) Text(todo.title, style = PersonaTheme.type.bodyStrong, color = PersonaTheme.palette.ink)
            Text(parsed.text, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
            ask.recommend?.takeIf(String::isNotBlank)?.let {
                Text(stringResource(R.string.chat_tray_recommended, it), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
            if (ask.forBriefing) ForBriefingMark()
            if (failed) Text(stringResource(R.string.chat_tray_finish_failed), style = PersonaTheme.type.caption, color = PersonaTheme.palette.danger.text)
            if (changed) Text(stringResource(R.string.chat_tray_accept_changed), style = PersonaTheme.type.caption, color = PersonaTheme.palette.cautionText)
            val finish = JAction(stringResource(R.string.chat_tray_finish_now), enabled = enabled) {
                failed = false; changed = false
                host.tasks.finishTaskNow(todo, { failed = true }, onChanged)
            }
            val follow = if (ask.askedAt.isNotBlank() && !ask.recommend.isNullOrBlank()) JAction(
                stringResource(R.string.chat_tray_follow_recommendation), enabled = enabled) {
                failed = false; changed = false
                host.tasks.acceptTask(todo, host.onError, onChanged)
            } else null
            DecisionActions(follow, listOfNotNull(onOpen?.let { open -> JAction(stringResource(R.string.chat_link_open), enabled = enabled, onClick = open) }) + finish,
                stringResource(R.string.chat_task_detail_more), busy = sending)
            if (sending) JSpinner()
        }
    }
}

/** The row's detail retains the current execution's receipt/PR and report without contradicting an active acceptance card. */
@Composable
internal fun TaskLifecycleDetails(
    todo: Todo,
    host: AssistantTaskHost,
) {
    val feeds by host.tasks.feeds.collectAsStateWithLifecycle()
    val plans by host.tasks.plans.collectAsStateWithLifecycle()
    val cards by host.tasks.taskCards.collectAsStateWithLifecycle()
    val merged = AssistantTaskCards.merge(feeds[host.conversation].orEmpty(), plans, listOf(todo), cards[host.conversation].orEmpty())
        .firstOrNull { it.todo?.id == todo.id && (todo.planId == null || it.identity.planId == todo.planId) }
    val items = listOfNotNull(
        merged?.receipt?.takeIf { shouldShowTaskReceiptInDetail(todo) },
        merged?.report?.takeIf { todo.ask == null },
    )
    if (items.isEmpty()) return
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        items.forEach { item -> AssistantExtraLine(AssistantExtra.Feed(item), host) }
    }
}

/** Extracts any PR URL carried on a task's merged receipt/report so suppressed cards never drop PR access. */
@Composable
internal fun taskLifecyclePrUrl(todo: Todo, host: AssistantTaskHost): String? {
    val feeds by host.tasks.feeds.collectAsStateWithLifecycle()
    val plans by host.tasks.plans.collectAsStateWithLifecycle()
    val cards by host.tasks.taskCards.collectAsStateWithLifecycle()
    val merged = AssistantTaskCards.merge(feeds[host.conversation].orEmpty(), plans, listOf(todo), cards[host.conversation].orEmpty())
        .firstOrNull { it.todo?.id == todo.id && (todo.planId == null || it.identity.planId == todo.planId) }
    return merged?.report?.field("pr")?.takeIf(String::isNotBlank)
        ?: merged?.receipt?.field("pr")?.takeIf(String::isNotBlank)
}
