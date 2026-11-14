package persona.feature.chat

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.FeedItem
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.designsystem.AssistantAnswerFrame
import persona.core.designsystem.JCard
import persona.core.designsystem.JTag
import persona.core.designsystem.JIcon
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.UserBubbleFrame
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.core.domain.tasks.interrupting

// What Persona and a task's agent say to each other in Persona's conversation on Glass (#4339 C13; iOS `AgentExchange.swift`,
// docs/apple/ios.md "New conversation layout"): Persona's message to a task (feed `routed` with `from` "assistant") reads
// "Persona → @task", the agent's report (`report`) "task → @Persona", what the user sent a task (`routed` without `from`)
// "Me → @task" in the user's bubble. Persona's side starts with its first three lines and Expand under them.

/** Persona in the headers: the assistant's role here, whatever its avatar is called (iOS `ChatNames.persona`). */
internal const val PERSONA_NAME = "Persona"

/** How many lines of an exchange show before Expand (iOS `ChatLayoutSettings.defaultLines`). */
private const val EXCHANGE_LINES = 3

/**
 * Who talks to whom over an exchange (iOS `ExchangeHeader`): "Persona → @task", with the [identity] before it when it has
 * one ("Interrupted"). One line, the addressee in the accent; [icon] in [tint].
 */
@Composable
internal fun ExchangeHeader(from: String, to: String, modifier: Modifier = Modifier, identity: String? = null,
    icon: PhIcon? = null, tint: Color = PersonaTheme.palette.ink2) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val addressee = "@$to"
    val words = stringResource(R.string.chat_exchange_header, from, addressee)
    val text = remember(words, addressee, palette.accent) {
        buildAnnotatedString {
            append(words)
            val at = words.lastIndexOf(addressee)
            if (at >= 0) addStyle(SpanStyle(color = palette.accent), at, at + addressee.length)
        }
    }
    Row(modifier.semantics(mergeDescendants = true) {}.testTag("exchange-header"), verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        icon?.let { JIcon(it, decorative = true, size = Tokens.Space.m, tint = tint) }
        identity?.let {
            Text(it, style = type.caption.copy(fontWeight = FontWeight.SemiBold), color = palette.ink, maxLines = 1)
            Text("·", style = type.caption, color = palette.ink3, maxLines = 1)
        }
        Text(text, Modifier.weight(1f, fill = false), style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/**
 * An exchange's words (iOS `ExchangeText`): Markdown styled, web links linked, the first three lines with Expand under
 * them when there is more, and Collapse once open. A short one has no button. [id] keeps it open across recomposition.
 */
@Composable
internal fun ExchangeText(text: String, id: String, modifier: Modifier = Modifier) {
    var open by rememberSaveable(id) { mutableStateOf(false) }
    var cut by remember(text) { mutableStateOf(false) }
    val words = agentWords(text)
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        SelectionContainer {
            Text(words, Modifier.fillMaxWidth().testTag("exchange-text"), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink,
                maxLines = if (open) Int.MAX_VALUE else EXCHANGE_LINES, overflow = TextOverflow.Ellipsis,
                onTextLayout = { if (!open) cut = it.hasVisualOverflow })
        }
        if (cut || open) JTextButton(stringResource(if (open) R.string.chat_tasks_collapse else R.string.chat_tasks_expand), { open = !open },
            Modifier.testTag("exchange-toggle"))
    }
}

/**
 * An agent's words as the chat shows them in a record (iOS `ExchangeText.linked`): inline Markdown styled (a heading's
 * marks dropped, [ReceiptMarkdown]), its links and bare web links tappable.
 */
@Composable
internal fun agentWords(md: String): AnnotatedString {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    return remember(md, palette, type) {
        val styled = ReceiptMarkdown.parse(md)
        val link = TextLinkStyles(SpanStyle(color = palette.accent, textDecoration = TextDecoration.Underline))
        buildAnnotatedString {
            append(styled.text)
            for (span in styled.spans) when (span.mark) {
                ReceiptMarkdown.Mark.Strong -> addStyle(SpanStyle(fontWeight = type.bodyStrong.fontWeight), span.start, span.end)
                ReceiptMarkdown.Mark.Emphasis -> addStyle(SpanStyle(fontStyle = FontStyle.Italic), span.start, span.end)
                ReceiptMarkdown.Mark.Strike -> addStyle(SpanStyle(textDecoration = TextDecoration.LineThrough), span.start, span.end)
                ReceiptMarkdown.Mark.Code -> addStyle(SpanStyle(fontFamily = type.code.fontFamily, background = palette.sunken), span.start, span.end)
                ReceiptMarkdown.Mark.Link -> addLink(LinkAnnotation.Url(span.url.orEmpty(), link), span.start, span.end)
            }
            val linked = styled.spans.filter { it.mark == ReceiptMarkdown.Mark.Link }
            ChatLinks.matches(styled.text).filter { m -> linked.none { m.start < it.end && it.start < m.end } }
                .forEach { addLink(LinkAnnotation.Url(it.url, link), it.start, it.end) }
        }
    }
}

/** A header that opens the task when the list has it ([onOpenTask] null: plain words). */
private fun Modifier.opensTask(label: String, onOpenTask: (() -> Unit)?): Modifier =
    if (onOpenTask == null) this else clickable(onClickLabel = label, onClick = onOpenTask)

/** Persona to a task's agent (`routed` it wrote, iOS `RoutedExchange`): its words go to the task, in Persona's box. */
@Composable
internal fun RoutedExchange(item: FeedItem, title: String, onOpenTask: (() -> Unit)?) {
    Column(Modifier.fillMaxWidth().padding(end = Tokens.Space.xxl).testTag("exchange-routed"),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        // Persona stopped the agent's turn and then told it (agent_interrupt with then).
        ExchangeHeader(PERSONA_NAME, title, Modifier.opensTask(stringResource(R.string.chat_tasks_open_task), onOpenTask),
            identity = if (item.interrupting) stringResource(R.string.chat_exchange_interrupted) else null,
            icon = if (item.interrupting) PersonaIcons.interrupt else null, tint = PersonaTheme.palette.cautionText)
        AssistantAnswerFrame(boxed = true) { ExchangeText(item.field("text").orEmpty(), "routed-${item.id}") }
    }
}

/** What the user said straight to a task (`routed` without `from`, iOS `UserRoutedExchange`): the user's words, never folded. */
@Composable
internal fun UserRoutedExchange(item: FeedItem, title: String) {
    Column(Modifier.fillMaxWidth().padding(start = Tokens.Space.xxl).testTag("exchange-user-routed"), horizontalAlignment = Alignment.End,
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        ExchangeHeader(stringResource(R.string.chat_exchange_me), title)
        UserBubbleFrame {
            SelectionContainer {
                Text(agentWords(item.field("text").orEmpty()), style = PersonaTheme.type.body, color = PersonaTheme.palette.userBubbleText ?: PersonaTheme.palette.ink)
            }
        }
    }
}

/** A successor belongs to the routed message, even before it appears in a paged Agent list. */
internal fun newAgentKey(item: FeedItem, todo: Todo?): SessionKey? {
    val id = item.field("new_agent")?.takeIf(String::isNotBlank) ?: return null
    val machine = item.field("new_agent_machine")?.takeIf(String::isNotBlank)
        ?: todo?.agents?.firstOrNull { it.sessionId == id }?.machineId ?: return null
    return SessionKey(machine, id)
}

/** The same New handoff as iOS and web when messaging a task starts a successor Agent. */
@Composable
internal fun NewAgentHandoff(item: FeedItem, title: String, todo: Todo?, host: AssistantTaskHost, openTask: (() -> Unit)?) {
    val key = newAgentKey(item, todo)
    val session = key?.let { target -> host.sessions.firstOrNull { it.key == target } }
    val open = key?.let { target -> { host.onOpen(target) } } ?: openTask
    JCard(Modifier.padding(top = Tokens.Space.s).testTag("new-agent-handoff"), onClick = open, padding = Tokens.Space.m) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(session?.displayTitle ?: title, Modifier.weight(1f), style = PersonaTheme.type.bodyStrong,
                color = PersonaTheme.palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            JTag(stringResource(R.string.chat_new_agent_started))
        }
        Text(session?.status()?.localizedLabel ?: stringResource(R.string.chat_tasks_now_starting),
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1)
    }
}

/** A task's agent to Persona (`report`, iOS `ReportExchange`): a question, a block, done, or how far it got. */
@Composable
internal fun ReportExchange(item: FeedItem, title: String, onOpenTask: (() -> Unit)?) {
    val kind = item.field("kind").orEmpty()
    // A question or a block wants the supervisor's (and the user's) eye.
    val attention = kind == "question" || kind == "blocked"
    Column(Modifier.fillMaxWidth().padding(end = Tokens.Space.xxl).testTag("exchange-report"),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        ExchangeHeader(title, PERSONA_NAME, Modifier.opensTask(stringResource(R.string.chat_tasks_open_task), onOpenTask),
            icon = PersonaIcons.reported, tint = if (attention) PersonaTheme.palette.cautionText else PersonaTheme.palette.ink2)
        AssistantAnswerFrame(boxed = true) { ExchangeText(reportLine(item), "report-${item.id}") }
    }
}

/** A report in a line (iOS `ReviewText.reportLine`): what kind it is, else the step it is on, before the agent's words. */
@Composable
internal fun reportLine(item: FeedItem): String {
    val text = item.field("text").orEmpty()
    val step = item.number("step")?.takeIf { it > 0 }
    val steps = item.number("steps")?.takeIf { it > 0 }
    return when (item.field("kind")) {
        "question" -> stringResource(R.string.chat_report_question, text)
        "blocked" -> stringResource(R.string.chat_report_blocked, text)
        "done" -> stringResource(R.string.chat_report_done, text)
        else -> when {
            step != null && steps != null -> stringResource(R.string.chat_report_step_of, step, steps, text)
            step != null -> stringResource(R.string.chat_report_step, step, text)
            else -> text
        }
    }
}

/**
 * A line the hub wrote in the user's language (feed `notice`, docs/protocol/protocol.md "Tasks and the chat feed"; iOS
 * `FeedLine`): centered small words, nothing without them.
 */
@Composable
internal fun NoticeLine(item: FeedItem) {
    val text = item.field("text")?.trim()?.takeIf(String::isNotEmpty) ?: return
    Text(text, Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.xl).testTag("feed-notice"), style = PersonaTheme.type.caption,
        color = PersonaTheme.palette.ink2, textAlign = TextAlign.Center)
}
