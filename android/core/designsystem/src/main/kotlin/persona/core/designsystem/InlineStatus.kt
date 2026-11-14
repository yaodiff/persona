package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

/** What an [JInlineStatus] says: something under way, something to look at, something that went wrong, or that it is done. */
enum class InlineTone { Busy, Caution, Failed, Done }

/** The detail's leading icon for a shared Inbox state key; tone keeps the existing state colors. */
data class InboxNowIcon(val tone: InlineTone, val icon: PhIcon?)

fun inboxNowIcon(key: String?): InboxNowIcon = when (key) {
    "waiting" -> InboxNowIcon(InlineTone.Busy, PersonaIcons.time)
    "queued", "looking", "agent_working" -> InboxNowIcon(InlineTone.Busy, null)
    "failed" -> InboxNowIcon(InlineTone.Failed, null)
    "confirm_reply", "confirm_agent", "decide", "follow_up_ask", "check_due", "needs_you" ->
        InboxNowIcon(InlineTone.Caution, null)
    else -> InboxNowIcon(InlineTone.Done, null)
}

/**
 * A short status set inside a card or above the composer, up to three lines: a spinner while [tone] is busy, a mark
 * otherwise, on a neutral surface (the chat's "Starting", a machine's "Redeploy" line). [action] sits at the end, for one small
 * button that acts on it. An explicit [icon] replaces the tone's default spinner or mark without changing its colors.
 */
@Composable
fun JInlineStatus(
    text: String,
    modifier: Modifier = Modifier,
    tone: InlineTone = InlineTone.Busy,
    icon: PhIcon? = null,
    action: (@Composable () -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val (fill, ink) = when (tone) {
        InlineTone.Busy -> palette.statusSurface to palette.ink2
        InlineTone.Caution -> palette.statusSurface to palette.cautionText
        InlineTone.Failed -> palette.statusSurface to palette.danger.text
        InlineTone.Done -> palette.statusSurface to palette.done.text
    }
    Row(
        modifier.fillMaxWidth().semantics(mergeDescendants = true) {}.clip(PersonaTheme.shapes.controlShape).background(fill)
            .padding(horizontal = 12.dp.dense, vertical = 8.dp.dense),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp.dense),
    ) {
        when {
            icon != null -> JIcon(icon, null, size = Tokens.Space.l, tint = ink)
            tone == InlineTone.Busy -> JSpinner(Modifier.size(14.dp), color = ink, stroke = 1.8.dp)
            tone == InlineTone.Caution -> JIcon(PhIcons.Warning, null, size = 16.dp, tint = palette.caution)
            tone == InlineTone.Failed -> JIcon(PhIcons.WarningCircle, null, size = 16.dp, tint = palette.danger.dot)
            tone == InlineTone.Done -> JIcon(PhIcons.CheckCircle, null, size = 16.dp, tint = palette.positive)
        }
        Text(text, Modifier.weight(1f), style = PersonaTheme.type.caption, color = ink, maxLines = 3, overflow = TextOverflow.Ellipsis)
        action?.invoke()
    }
}
