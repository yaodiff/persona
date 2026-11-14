package persona.feature.chat

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import persona.core.contracts.api.*
import persona.core.designsystem.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

@Composable
internal fun ComposerRefusalActions(
    message: OutboxMessage,
    service: ChatComposerService?,
    delivery: ComposerDelivery?,
    terminal: (() -> Unit)?,
    canSend: Boolean = true,
    edit: () -> Unit,
    notice: (String) -> Unit,
) {
    val scope = rememberCoroutineScope()
    val resources = androidx.compose.ui.platform.LocalResources.current
    var confirm by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    val refusal = delivery?.refusal
    val code = refusal?.code ?: message.error
    fun retry(clear: Boolean) {
        if (service == null || busy || !canSend) return
        busy = true
        scope.launch {
            try { service.retryRefused(message.clientMessageId, clear) }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { notice(operationFeedback(e).text.resolve(resources)) }
            finally { busy = false }
        }
    }
    Column {
        when (code) {
            "input_draft" -> Text(stringResource(if (refusal?.clearable.isNullOrEmpty())
                R.string.chat_refusal_draft_terminal else R.string.chat_refusal_draft_clearable), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            "dialog_open" -> Text(stringResource(R.string.chat_refusal_dialog_open), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        }
        Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (canSend && code == "input_draft" && !refusal?.clearable.isNullOrEmpty()) JButton(stringResource(R.string.chat_refusal_clear_and_send), { confirm = true }, size = JButtonSize.Small, kind = JButtonKind.Secondary, enabled = !busy)
            if (terminal != null) JButton(stringResource(R.string.chat_refusal_open_terminal), terminal, size = JButtonSize.Small, kind = JButtonKind.Secondary, enabled = !busy)
            if (canSend) JButton(stringResource(R.string.chat_refusal_edit), edit, size = JButtonSize.Small, kind = JButtonKind.Secondary, enabled = !busy)
        }
    }
    LaunchedEffect(canSend) { if (!canSend) confirm = false }
    if (confirm && canSend) JDialog(onDismiss = { confirm = false }, title = stringResource(R.string.chat_refusal_confirm_title),
        content = {
            Column(Modifier.heightIn(max = 280.dp).verticalScroll(rememberScrollState())) {
                Text(stringResource(R.string.chat_refusal_confirm_text), style = PersonaTheme.type.callout)
                JCodeBlock(refusal?.draft ?: refusal?.clearable.orEmpty(), Modifier.padding(vertical = Tokens.Space.s))
                Text(stringResource(R.string.chat_refusal_confirm_note), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
        },
        confirm = stringResource(R.string.chat_refusal_clear_and_send) to { confirm = false; retry(true) }, destructive = true)
}
