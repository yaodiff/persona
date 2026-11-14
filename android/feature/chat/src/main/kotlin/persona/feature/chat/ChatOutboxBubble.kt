package persona.feature.chat

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.semantics
import persona.core.contracts.api.DeliveryState
import persona.core.designsystem.JAction
import persona.core.designsystem.JIcon
import persona.core.designsystem.JMenu
import persona.core.designsystem.PendingStatus
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons

/**
 * The mark before an unsent message's bubble on Glass (iOS `PendingBubble`): a quiet clock while it sends; a clock with a
 * small menu while it waits (queued, or for the network); a red mark with a small menu once it did not go (refused, or
 * turned down). A delivery the phone has to check keeps Android's status line, and a delivered one has no mark.
 */
internal enum class OutboxMark { Sending, Waiting, Failed }

internal fun outboxMark(state: DeliveryState): OutboxMark? = when (state) {
    DeliveryState.Sending -> OutboxMark.Sending
    DeliveryState.Queued, DeliveryState.WaitingForNetwork -> OutboxMark.Waiting
    DeliveryState.Refused, DeliveryState.Rejected -> OutboxMark.Failed
    DeliveryState.Sent, DeliveryState.Unknown, DeliveryState.Verifying, DeliveryState.NeedsConfirmation -> null
}

/** Glass draws [state] as iOS's bubble: one with a [OutboxMark], or one delivered and waiting for the chat history. */
internal fun drawsAsPendingBubble(state: DeliveryState): Boolean = state == DeliveryState.Sent || outboxMark(state) != null

/** The mark's menu, in iOS's order: Retry now and Don't send while it waits, Resend and Delete once it failed. */
internal fun outboxMarkActions(mark: OutboxMark, canSend: Boolean = true): List<DeliveryAction> = when (mark) {
    OutboxMark.Sending -> emptyList()
    OutboxMark.Waiting -> listOf(DeliveryAction.RetryNow, DeliveryAction.Cancel)
    OutboxMark.Failed -> listOf(DeliveryAction.Retry, DeliveryAction.Delete)
}.filter { it.available(canSend) }

/**
 * An unsent message on Glass, as iOS's `PendingBubble`: its [OutboxMark] before the bubble, a tap on it opens the small menu
 * ([outboxMarkActions], also TalkBack's actions); the bubble faded while it waits or after it failed, "Sends when connected"
 * under it while it waits. Its long press is the bubble's own (Copy, Select, Share, then Don't send or Delete). [onAction]
 * null: a read-only chat, the mark has no menu.
 */
@Composable
internal fun OutboxBubble(text: String, id: String, state: DeliveryState, busy: Boolean, canSend: Boolean = true, onAction: ((DeliveryAction) -> Unit)?) {
    val mark = outboxMark(state)
    Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        UserBubble(text, id, state, faded = mark == OutboxMark.Waiting || mark == OutboxMark.Failed,
            leading = mark?.let { { OutboxMarkButton(it, busy, canSend, onAction) } })
        if (mark == OutboxMark.Waiting) Text(PendingStatus.Waiting.localizedLabel, style = PersonaTheme.type.caption,
            color = PersonaTheme.palette.ink2, maxLines = 1)
    }
}

@Composable
private fun OutboxMarkButton(mark: OutboxMark, busy: Boolean, canSend: Boolean, onAction: ((DeliveryAction) -> Unit)?) {
    val palette = PersonaTheme.palette
    val res = LocalResources.current
    var open by remember { mutableStateOf(false) }
    val actions = if (onAction == null || busy) emptyList() else outboxMarkActions(mark, canSend)
    fun label(action: DeliveryAction) = res.getString(when (action) {
        DeliveryAction.RetryNow -> R.string.chat_outbox_retry_now
        DeliveryAction.Cancel -> R.string.chat_outbox_dont_send
        DeliveryAction.Retry -> R.string.chat_outbox_resend
        else -> R.string.chat_delivery_delete
    })
    val spoken = if (!canSend && mark != OutboxMark.Sending) PendingStatus.Refused.localizedLabel else when (mark) {
        OutboxMark.Sending -> PendingStatus.Sending.localizedLabel
        OutboxMark.Waiting -> stringResource(R.string.chat_outbox_waiting_spoken)
        OutboxMark.Failed -> stringResource(R.string.chat_outbox_failed_spoken)
    }
    Box(
        Modifier.size(Tokens.MinTouch.android).clip(CircleShape)
            .then(if (actions.isEmpty()) Modifier else Modifier.clickable(role = Role.Button) { open = true })
            .semantics {
                contentDescription = spoken
                if (actions.isNotEmpty()) customActions = actions.map { action ->
                    CustomAccessibilityAction(label(action)) { onAction?.invoke(action); true }
                }
            },
        contentAlignment = Alignment.Center,
    ) {
        when (mark) {
            OutboxMark.Sending -> JIcon(PersonaIcons.time, null, size = Tokens.Size.markS, tint = palette.ink3, decorative = true)
            OutboxMark.Waiting -> JIcon(PersonaIcons.time, null, size = Tokens.Size.markS, tint = palette.ink2, decorative = true)
            OutboxMark.Failed -> JIcon(PersonaIcons.errorFilled, null, size = Tokens.Size.iconXS, tint = palette.danger.dot, decorative = true)
        }
        JMenu(open, { open = false }, actions.map { action ->
            JAction(label(action), if (action == DeliveryAction.Cancel || action == DeliveryAction.Delete) PersonaIcons.delete else PersonaIcons.retry,
                destructive = action == DeliveryAction.Cancel || action == DeliveryAction.Delete) { onAction?.invoke(action) }
        })
    }
}
