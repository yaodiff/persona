package persona.feature.agents

import android.text.format.DateFormat
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import java.net.URI
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.temporal.ChronoUnit
import java.util.Locale
import kotlin.math.abs
import persona.core.contracts.parseTime
import persona.core.contracts.generated.TodoWait
import persona.core.contracts.generated.TodoWaitKind
import persona.core.contracts.generated.TodoWaitPRBlocker
import persona.core.designsystem.ContentTokens
import persona.core.designsystem.JCard
import persona.core.designsystem.JIcon
import persona.core.designsystem.JTag
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.Tone
import persona.core.designsystem.icons.PersonaIcons

private const val WaitSeparator = " · "

/** One line under a waiting To-dos row: the server's plain-language reason, then its next wake or deadline. */
@Composable
internal fun TodoWaitLine(wait: TodoWait, now: Instant, modifier: Modifier = Modifier) {
    val zone = ZoneId.systemDefault()
    val locale = LocalConfiguration.current.locales[0]
    val summary = todoWaitRowSummary(wait, now, zone, locale)
    val palette = PersonaTheme.palette
    Row(
        modifier.fillMaxWidth().padding(start = Tokens.Space.xxl, top = Tokens.Space.xxs).testTag("todo-wait-line"),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        verticalAlignment = Alignment.Top,
    ) {
        JIcon(
            PersonaIcons.time,
            null,
            size = ContentTokens.statusIcon,
            tint = if (summary.isDue) palette.cautionText else palette.ink3,
            modifier = Modifier.padding(top = Tokens.Space.xxs),
        )
        Column(
            modifier = Modifier.weight(1f),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
        ) {
            Text(
                summary.lead,
                style = PersonaTheme.type.caption,
                color = palette.ink,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
            )
            summary.timing?.let { timing ->
                Text(
                    timing,
                    style = PersonaTheme.type.caption,
                    color = if (summary.isDue) palette.cautionText else palette.ink3,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
    }
}

/**
 * The complete server-computed waiting facts on a To-do detail page (#16170, iOS `TodoWaitSection`): target, reason,
 * PR blockers, wake condition/time, deadline, contact receipt, source link and when the wait began.
 */
@Composable
internal fun TodoWaitSection(
    wait: TodoWait,
    now: Instant,
    onOpenTodo: (String) -> Unit,
    modifier: Modifier = Modifier,
) {
    val zone = ZoneId.systemDefault()
    val locale = LocalConfiguration.current.locales[0]
    val detail = todoWaitDetail(wait, now, zone, locale)
    val palette = PersonaTheme.palette
    val links = LocalUriHandler.current
    JCard(modifier.testTag("todo-wait-section")) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            FlowRow(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                Row(
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    JIcon(PersonaIcons.time, null, size = ContentTokens.statusIcon, tint = palette.ink3)
                    Text(
                        stringResource(R.string.agents_todo_wait_section_title),
                        style = PersonaTheme.type.headline,
                        color = palette.ink,
                        maxLines = 1,
                    )
                }
                JTag(
                    detail.kindTitle,
                    if (detail.untilIsDue) Tone.Caution else Tone.Neutral,
                )
            }

            detail.step?.let { step ->
                Text(
                    stringResource(R.string.agents_todo_wait_from_step, step),
                    style = PersonaTheme.type.caption,
                    color = palette.ink3,
                )
            }

            val handoffId = detail.handoffTodoId
            val waitingOn = detail.waitingOn
            if (handoffId != null) {
                WaitFact(stringResource(R.string.agents_todo_wait_waiting_on)) {
                    JTextButton(
                        waitingOn ?: handoffId,
                        { onOpenTodo(handoffId) },
                        modifier = Modifier.testTag("todo-wait-handoff"),
                        icon = PersonaIcons.openDocument,
                    )
                }
            } else if (waitingOn != null) {
                WaitFact(stringResource(R.string.agents_todo_wait_waiting_on), waitingOn)
            }
            detail.reason?.let { WaitFact(stringResource(R.string.agents_todo_wait_details), it) }
            detail.blockersText?.let { WaitFact(stringResource(R.string.agents_todo_wait_blocked_by), it) }
            detail.wakeCondition?.let { WaitFact(stringResource(R.string.agents_todo_wait_wakes_when), it) }
            detail.wakeAtText?.let { WaitFact(stringResource(R.string.agents_todo_wait_scheduled_wake), it) }
            detail.untilText?.let {
                WaitFact(
                    stringResource(if (detail.untilIsDue) R.string.agents_todo_wait_check_in_due
                    else R.string.agents_todo_wait_until),
                    it,
                    caution = detail.untilIsDue,
                )
            }

            detail.contact?.let { contact ->
                val meta = listOfNotNull(contact.sender, contact.sentAtText).joinToString(WaitSeparator)
                Column(
                    modifier = Modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape)
                        .background(palette.surfaceStrong).padding(Tokens.Space.s),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                ) {
                    Text(
                        if (meta.isEmpty()) stringResource(R.string.agents_todo_wait_sent_via, contact.channel)
                        else stringResource(R.string.agents_todo_wait_sent_via_meta, contact.channel, meta),
                        style = PersonaTheme.type.caption,
                        color = palette.ink3,
                    )
                    contact.request?.let {
                        Text(it, style = PersonaTheme.type.body, color = palette.ink)
                    }
                    contact.url?.let { url ->
                        JTextButton(
                            stringResource(R.string.agents_todo_wait_open_message),
                            { val _ = runCatching { links.openUri(url) } },
                            modifier = Modifier.testTag("todo-wait-contact"),
                            icon = PersonaIcons.openDocument,
                        )
                    }
                }
            }

            detail.sourceUrl?.let { url ->
                JTextButton(
                    stringResource(if (detail.isPr) R.string.agents_todo_wait_view_pr else R.string.agents_todo_wait_open_source),
                    { val _ = runCatching { links.openUri(url) } },
                    modifier = Modifier.testTag("todo-wait-source"),
                    icon = PersonaIcons.openDocument,
                )
            }

            detail.sinceText?.let {
                Text(
                    stringResource(R.string.agents_todo_wait_since, it),
                    style = PersonaTheme.type.caption,
                    color = palette.ink3,
                )
            }
        }
    }
}

internal data class TodoWaitRowSummary(
    val lead: String,
    val timing: String?,
    val timed: Boolean,
    val isDue: Boolean,
)

internal data class TodoWaitDetailModel(
    val kindTitle: String,
    val step: String?,
    val waitingOn: String?,
    val handoffTodoId: String?,
    val reason: String?,
    val blockersText: String?,
    val wakeCondition: String?,
    val wakeAtText: String?,
    val untilText: String?,
    val untilIsDue: Boolean,
    val sinceText: String?,
    val sourceUrl: String?,
    val isPr: Boolean,
    val contact: TodoWaitContactDetail?,
)

internal data class TodoWaitContactDetail(
    val channel: String,
    val sender: String?,
    val sentAtText: String?,
    val request: String?,
    val url: String?,
)

@Composable
internal fun todoWaitRowSummary(
    wait: TodoWait,
    now: Instant,
    zone: ZoneId,
    locale: Locale,
): TodoWaitRowSummary {
    val target = wait.waitingOn.trimmed()
    val reason = wait.reason.trimmed()
    val blockers = todoWaitBlockerSummary(wait.prBlockers)
    var lead = when (wait.kind) {
        TodoWaitKind.UsageLimit -> target?.let {
            stringResource(R.string.agents_todo_wait_limit_reached, it)
        } ?: todoWaitKindLabel(wait.kind)
        TodoWaitKind.PrMerge -> {
            val detail = blockers ?: reason
            when {
                target != null && detail != null -> target + WaitSeparator + detail
                target != null -> stringResource(R.string.agents_todo_wait_to_merge, target)
                detail != null -> detail
                else -> todoWaitKindLabel(wait.kind)
            }
        }
        else -> when {
            target != null && reason != null ->
                if (reason.contains(target, ignoreCase = true)) reason else target + WaitSeparator + reason
            reason != null -> reason
            target != null -> stringResource(R.string.agents_todo_wait_on, target)
            else -> todoWaitKindLabel(wait.kind)
        }
    }
    wait.step.trimmed()?.let { step ->
        if (!lead.startsWith(step)) lead = step + WaitSeparator + lead
    }

    val wakeAt = parseTime(wait.wakeAt)
    val until = parseTime(wait.until)
    val since = parseTime(wait.since)
    var timing: String? = null
    var timed = false
    var isDue = false
    when {
        wakeAt != null -> {
            val formatted = todoWaitShortMoment(wakeAt, now, zone, locale)
            timed = true
            timing = if (wait.kind == TodoWaitKind.UsageLimit) {
                stringResource(R.string.agents_todo_wait_back_at, formatted)
            } else {
                stringResource(R.string.agents_todo_wait_wakes_at, formatted)
            }
        }
        until != null -> {
            val formatted = todoWaitShortMoment(until, now, zone, locale)
            timed = true
            if (!until.isAfter(now)) {
                isDue = true
                timing = stringResource(R.string.agents_todo_wait_check_in_at, formatted)
            } else {
                timing = stringResource(R.string.agents_todo_wait_until_at, formatted)
            }
        }
        wait.wakeCondition.trimmed() != null -> timing = wait.wakeCondition.trimmed()
        since != null -> {
            timed = true
            timing = stringResource(R.string.agents_todo_wait_since_at, todoWaitShortMoment(since, now, zone, locale))
        }
    }
    return TodoWaitRowSummary(lead, timing, timed, isDue)
}

@Composable
internal fun todoWaitDetail(
    wait: TodoWait,
    now: Instant,
    zone: ZoneId,
    locale: Locale,
): TodoWaitDetailModel {
    val wakeAt = parseTime(wait.wakeAt)
    val until = parseTime(wait.until)
    val since = parseTime(wait.since)
    val wakeAtText = wakeAt?.let { todoWaitFullMoment(it, now, zone, locale) }
    val untilIsDue = until?.isAfter(now) == false
    val untilText = until?.takeUnless { wakeAt != null && abs(it.epochSecond - wakeAt.epochSecond) < 1 }
        ?.let { todoWaitFullMoment(it, now, zone, locale) }
    val contact = wait.contact?.let { c ->
        c.channel.trimmed()?.let { channel ->
            TodoWaitContactDetail(
                channel = channel,
                sender = c.sender.trimmed(),
                sentAtText = parseTime(c.sentAt)?.let { todoWaitFullMoment(it, now, zone, locale) },
                request = c.request.trimmed(),
                url = webUrl(c.url),
            )
        }
    }
    val sourceUrl = webUrl(wait.url)?.takeUnless { it == contact?.url }
    return TodoWaitDetailModel(
        kindTitle = todoWaitKindLabel(wait.kind),
        step = wait.step.trimmed(),
        waitingOn = wait.waitingOn.trimmed(),
        handoffTodoId = wait.handoffTodoId?.raw?.trimmed(),
        reason = wait.reason.trimmed(),
        blockersText = todoWaitBlockerSummary(wait.prBlockers),
        wakeCondition = wait.wakeCondition.trimmed(),
        wakeAtText = wakeAtText,
        untilText = untilText,
        untilIsDue = untilIsDue,
        sinceText = since?.let { todoWaitFullMoment(it, now, zone, locale) },
        sourceUrl = sourceUrl,
        isPr = wait.kind == TodoWaitKind.PrMerge || wait.kind == TodoWaitKind.MergeReview,
        contact = contact,
    )
}

@Composable
private fun todoWaitKindLabel(kind: TodoWaitKind): String = stringResource(when (kind) {
    TodoWaitKind.PersonReply -> R.string.agents_todo_wait_kind_person_reply
    TodoWaitKind.MergeReview -> R.string.agents_todo_wait_kind_merge_review
    TodoWaitKind.PrMerge -> R.string.agents_todo_wait_kind_pr_merge
    TodoWaitKind.ExternalEvent -> R.string.agents_todo_wait_kind_external_event
    TodoWaitKind.Date -> R.string.agents_todo_wait_kind_date
    TodoWaitKind.TodoHandoff -> R.string.agents_todo_wait_kind_todo_handoff
    TodoWaitKind.Agent -> R.string.agents_todo_wait_kind_agent
    TodoWaitKind.UsageLimit -> R.string.agents_todo_wait_kind_usage_limit
    TodoWaitKind.Parked -> R.string.agents_todo_wait_kind_parked
    is TodoWaitKind.Unknown -> R.string.agents_todo_wait_kind_unknown
})

@Composable
private fun todoWaitBlockerSummary(blockers: List<TodoWaitPRBlocker>?): String? {
    if (blockers.isNullOrEmpty()) return null
    val labels = ArrayList<String>(blockers.size)
    for (blocker in blockers) {
        val label = todoWaitBlockerLabel(blocker) ?: continue
        labels += label
    }
    return labels.takeIf { it.isNotEmpty() }?.joinToString(WaitSeparator)
}

@Composable
private fun todoWaitBlockerLabel(blocker: TodoWaitPRBlocker): String? = when (blocker) {
    TodoWaitPRBlocker.ReviewRequired -> stringResource(R.string.agents_todo_wait_blocker_review_required)
    TodoWaitPRBlocker.ChangesRequested -> stringResource(R.string.agents_todo_wait_blocker_changes_requested)
    TodoWaitPRBlocker.ChecksPending -> stringResource(R.string.agents_todo_wait_blocker_checks_pending)
    TodoWaitPRBlocker.ChecksFailing -> stringResource(R.string.agents_todo_wait_blocker_checks_failing)
    TodoWaitPRBlocker.Conflicts -> stringResource(R.string.agents_todo_wait_blocker_conflicts)
    is TodoWaitPRBlocker.Unknown -> null
}

@Composable
private fun WaitFact(label: String, value: String, caution: Boolean = false) {
    val palette = PersonaTheme.palette
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        Text(label, style = PersonaTheme.type.caption, color = palette.ink3)
        Text(
            value,
            style = PersonaTheme.type.body,
            color = if (caution) palette.cautionText else palette.ink,
        )
    }
}

@Composable
private fun WaitFact(label: String, content: @Composable () -> Unit) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        Text(label, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
        content()
    }
}

internal fun todoWaitShortMoment(date: Instant, now: Instant, zone: ZoneId, locale: Locale): String {
    val at = date.atZone(zone)
    val today = now.atZone(zone).toLocalDate()
    val day = at.toLocalDate()
    val days = abs(ChronoUnit.DAYS.between(today, day))
    val skeleton = when {
        day == today -> "HHmm"
        days <= 6 -> "EEEHHmm"
        day.year == today.year -> "MMMd"
        else -> "yMMMd"
    }
    return momentFormatter(locale, skeleton).format(at)
}

internal fun todoWaitFullMoment(date: Instant, now: Instant, zone: ZoneId, locale: Locale): String {
    val at = date.atZone(zone)
    val sameYear = at.year == now.atZone(zone).year
    return momentFormatter(locale, if (sameYear) "EEEMMMdHHmm" else "yEEEMMMdHHmm").format(at)
}

private fun momentFormatter(locale: Locale, skeleton: String): DateTimeFormatter =
    DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, skeleton), locale)

private fun String?.trimmed(): String? = this?.trim()?.takeIf { it.isNotEmpty() }

private fun webUrl(raw: String?): String? {
    val text = raw.trimmed() ?: return null
    val uri = runCatching { URI(text) }.getOrNull() ?: return null
    if (uri.scheme?.lowercase() !in setOf("http", "https")) return null
    return uri.toString()
}
