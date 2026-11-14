package persona.feature.chat

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import persona.core.contracts.Session
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.resultUnknown
import persona.core.designsystem.*

@Composable
internal fun OutboxStatus(
    message: OutboxMessage, busy: Boolean, onAction: ((DeliveryAction) -> Unit)?,
    receipt: persona.core.contracts.api.MessageSendReceipt? = null,
    canSend: Boolean = true,
    onDetails: (() -> Unit)? = null,
) {
    var retryConfirmation by remember(message.clientMessageId) { mutableStateOf(false) }
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        val pending = message.state.pendingStatus
        if (receipt?.processing == true) Text(stringResource(persona.core.designsystem.R.string.designsystem_message_processing),
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        else if (receipt?.state == "sending") PendingBadge(PendingStatus.Sending)
        else if (pending != null) PendingBadge(pending, reason = message.refusalReason())
        else Text(stringResource(R.string.chat_delivery_delivered_waiting), style = PersonaTheme.type.caption,
            color = PersonaTheme.palette.ink2)
        if (onAction != null) Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (message.state.allows(DeliveryAction.Cancel)) {
                JButton(kind = JButtonKind.Secondary, size = JButtonSize.Small, text = stringResource(R.string.chat_delivery_cancel), onClick = { onAction(DeliveryAction.Cancel) }, enabled = !busy)
            }
            if (message.state.allows(DeliveryAction.Cancel) && message.state.allows(DeliveryAction.Edit, canSend))
                JButton(kind = JButtonKind.Secondary, size = JButtonSize.Small, text = stringResource(R.string.chat_delivery_edit), onClick = { onAction(DeliveryAction.Edit) }, enabled = !busy)
            if (message.state.allows(DeliveryAction.Retry, canSend)) JButton(kind = JButtonKind.Secondary, size = JButtonSize.Small, text = stringResource(R.string.chat_delivery_retry), onClick = {
                if (message.state.resultUnknown) retryConfirmation = true else onAction(DeliveryAction.Retry)
            }, enabled = !busy)
            // Nothing of a refused message reached the computer: it can simply go.
            if (message.state.allows(DeliveryAction.Delete))
                JButton(kind = JButtonKind.Secondary, size = JButtonSize.Small, text = stringResource(R.string.chat_delivery_delete), onClick = { onAction(DeliveryAction.Delete) }, enabled = !busy)
            if (message.state.resultUnknown && onDetails != null)
                JButton(kind = JButtonKind.Secondary, size = JButtonSize.Small, text = stringResource(R.string.chat_delivery_check), onClick = onDetails, enabled = !busy)
            if (message.state.allows(DeliveryAction.Discard))
                JButton(kind = JButtonKind.Secondary, size = JButtonSize.Small, text = stringResource(R.string.chat_delivery_discard), onClick = { onAction(DeliveryAction.Discard) }, enabled = !busy)
        }
    }
    LaunchedEffect(canSend) { if (!canSend) retryConfirmation = false }
    if (retryConfirmation && canSend) JDialog(onDismiss = { retryConfirmation = false }, title = stringResource(R.string.chat_delivery_resend_title),
        text = stringResource(R.string.chat_delivery_resend_text),
        confirm = stringResource(R.string.chat_delivery_resend_confirm) to { retryConfirmation = false; onAction?.invoke(DeliveryAction.Retry) },
        confirmEnabled = !busy, dismiss = stringResource(R.string.chat_delivery_resend_dismiss) to { retryConfirmation = false })
}

/** Keep session state and secondary facts on one line; details open in the status sheet. */
@Composable
internal fun ChatSessionStatusLine(
    state: StatusLineState, session: Session?, recordedDuration: Long?, tools: Int,
    onDetails: () -> Unit, terminal: (() -> Unit)?, draftSaveError: String? = null,
    /** The header shows the loop and background work (Glass, #4339 C8): their chips are not repeated here. */
    activityInHeader: Boolean = false,
) {
    val chips = listOfNotNull(
        session?.queued?.takeIf { it.isNotEmpty() }?.let { stringResource(R.string.chat_status_chip_queued, it.size) },
        session?.loop?.takeIf { !activityInHeader }?.let { stringResource(R.string.chat_status_loop) },
        session?.background?.takeIf { it.isNotEmpty() && !activityInHeader }?.let { stringResource(R.string.chat_status_chip_background, it.size) },
        draftSaveError?.let { stringResource(R.string.chat_status_draft_failed) },
    )
    // The header's title line says the state, once (#3957; iOS shows none above its composer): this line keeps only what
    // the header does not say, the facts and an unconfirmed delivery, and is gone without them. The header's title still
    // opens the status sheet it opened.
    StatusLine(state, chips = chips, onClick = { onDetails() }, sayState = false)
}
