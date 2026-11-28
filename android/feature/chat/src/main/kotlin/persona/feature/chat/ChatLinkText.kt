package persona.feature.chat

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.*
import androidx.compose.ui.text.style.TextOverflow
import persona.core.designsystem.*
import androidx.compose.ui.text.style.TextDecoration

@Composable
internal fun chatLinkAnnotations(text: String): AnnotatedString {
    val color = PersonaTheme.palette.accent
    val links = remember(text) { ChatLinks.matches(text) }
    return remember(text, color) {
        buildAnnotatedString {
            append(text)
            links.forEach { addLink(LinkAnnotation.Url(it.url, TextLinkStyles(SpanStyle(color = color, textDecoration = TextDecoration.Underline))), it.start, it.end) }
        }
    }
}

@Composable
fun ChatLinkText(text: String, modifier: Modifier = Modifier, style: TextStyle = PersonaTheme.type.body.copy(color = PersonaTheme.palette.ink)) {
    val annotated = chatLinkAnnotations(text)
    val layout = remember { mutableStateOf<TextLayoutResult?>(null) }
    Text(annotated, modifier.chatLinkLongPress(annotated) { layout.value }, style = style, onTextLayout = { layout.value = it })
}

/**
 * A context the next message carries (a page's picked text). [compact] is one line, for when the link card leaves
 * the conversation four tenths of the screen: the whole card pushed the composer under the link card (2026-11-02).
 */
@Composable
internal fun ChatContextCard(context: ChatLinkContext, compact: Boolean = false, lines: Int = 2, onRemove: (() -> Unit)? = null) {
    if (context.isForward) { ForwardContextCard(context, compact, onRemove, lines); return }
    val uriHandler = LocalUriHandler.current
    val quote = context.kind == ChatLinkContext.QUOTE
    val session = isSessionContextUrl(context.url)
    // A quoted message ("Quote") and a session picked with @ say whom they quote (iOS `quoteHeading`); no title line under it.
    val label = when {
        // Words picked in a message ("Select", then Quote) say so, as iOS's `quoteHeading` does.
        quote && context.part == ChatLinkContext.PART_SELECTION -> stringResource(R.string.chat_quote_selection,
            if (context.title == ChatLinkContext.QUOTE_ME) stringResource(R.string.chat_exchange_me) else context.title)
        quote && context.title == ChatLinkContext.QUOTE_ME -> stringResource(R.string.chat_quote_yours)
        quote -> stringResource(R.string.chat_quote_from, context.title)
        session -> stringResource(R.string.chat_quote_session, context.title)
        context.kind == "edit" -> stringResource(R.string.chat_context_ai_edit)
        else -> stringResource(R.string.chat_context_linked)
    }
    val title = context.title.ifBlank { context.url.ifBlank { stringResource(R.string.chat_context_web_selection) } }
    if (compact) JCard(Modifier.fillMaxWidth(), padding = Tokens.Space.xs) {
        Row(Modifier.padding(start = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.chat_label_value, label, context.text.lineSequence().firstOrNull { it.isNotBlank() }?.trim() ?: title), Modifier.weight(1f),
                style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (onRemove != null) JTextButton(stringResource(R.string.chat_context_remove), onRemove)
        }
    } else JCard(Modifier.fillMaxWidth()) {
        Row(Modifier.padding(Tokens.Space.s)) {
            Column(Modifier.weight(1f)) {
                if (quote || session) Text(label, style = PersonaTheme.type.callout, color = PersonaTheme.palette.accent, maxLines = 1, overflow = TextOverflow.Ellipsis)
                else {
                    Text(label, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink2)
                    Text(title, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                Text(if (session) context.text.lines().drop(1).joinToString(" ") else context.text, maxLines = 2, overflow = TextOverflow.Ellipsis, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                if (ChatLinks.webUrl(context.url) != null) JTextButton(stringResource(R.string.chat_context_view_source), { uriHandler.openUri(context.url) })
            }
            if (onRemove != null) JTextButton(stringResource(R.string.chat_context_remove), onRemove)
        }
    }
}

/** Where a session picked with @ points ([sessionQuote]): `persona://session/<machine>/<session>`. */
internal const val SESSION_CONTEXT_URL = "persona://session/"

internal fun isSessionContextUrl(url: String): Boolean =
    persona.core.contracts.ProtocolCompatibility.appSchemes.any { url.startsWith("$it://session/") }

/**
 * A forward over the composer (#3901, iOS `QuoteBarRow` with the forward arrow and route): the arrow and "Forwarded from
 * Claude · chat", its first [lines] lines, and the line saying which computer it comes from and where it goes
 * ([ChatLinkContext.route]); [onRemove] takes it off. [compact] is one line, as [ChatContextCard]'s.
 */
@Composable
private fun ForwardContextCard(context: ChatLinkContext, compact: Boolean, onRemove: (() -> Unit)?, lines: Int) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val resources = androidx.compose.ui.platform.LocalResources.current
    val heading = ForwardMessage.heading(context.extra, resources)
    val summary = ForwardMessage.summary(context.text, resources)
    val remove = stringResource(R.string.chat_context_remove)
    if (compact) JCard(Modifier.fillMaxWidth(), padding = Tokens.Space.xs) {
        Row(Modifier.padding(start = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            JIcon(persona.core.designsystem.icons.PersonaIcons.forwardMessage, null, size = Tokens.Space.l, tint = palette.accent, decorative = true)
            Spacer(Modifier.width(Tokens.Space.xs))
            Text(stringResource(R.string.chat_label_value, heading, summary.lineSequence().first()), Modifier.weight(1f),
                style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (onRemove != null) JTextButton(remove, onRemove)
        }
    } else JCard(Modifier.fillMaxWidth().testTag(FORWARD_CARD_TAG)) {
        Row(Modifier.padding(Tokens.Space.s)) {
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    JIcon(persona.core.designsystem.icons.PersonaIcons.forwardMessage, null, size = Tokens.Space.l, tint = palette.accent, decorative = true)
                    Spacer(Modifier.width(Tokens.Space.xs))
                    Text(heading, style = type.callout, color = palette.accent, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                Text(summary.replace('\n', ' '), maxLines = lines, overflow = TextOverflow.Ellipsis, style = type.caption, color = palette.ink2)
                // Which computer it comes from and where it goes: two lines at most, so neither computer is cut out.
                context.route?.let {
                    Text(it, Modifier.testTag(FORWARD_ROUTE_TAG), style = type.micro, color = palette.ink2, maxLines = 2,
                        overflow = TextOverflow.Ellipsis)
                }
            }
            if (onRemove != null) JTextButton(remove, onRemove)
        }
    }
}

/**
 * A forward over a composer outside a chat page (#3901): Persona's new conversation, the new-agent form's first message.
 * [lines] of the original, as a chat's composer shows it; [onRemove] takes it off.
 */
@Composable
fun ForwardComposerCard(context: ChatLinkContext, lines: Int = FORWARD_LINES, onRemove: (() -> Unit)? = null) =
    ChatContextCard(context, lines = lines, onRemove = onRemove)

internal const val FORWARD_CARD_TAG = "forward-card"
internal const val FORWARD_ROUTE_TAG = "forward-route"
