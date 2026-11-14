package persona.feature.inbox

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.contracts.api.hubCodedFailure
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JNotice
import persona.core.designsystem.JSheet
import persona.core.designsystem.JTextButton
import persona.core.designsystem.JTextField
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons

/** The handling line's instruction entry: Say something, or Try again with the localized default words on failure. */
@Composable
internal fun InboxSayAction(
    repository: InboxRepository,
    record: InboxRecord,
    mark: EventMark?,
    onStale: (Boolean) -> Unit,
) {
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    var showSheet by rememberSaveable(record.item.id) { mutableStateOf(false) }
    var words by rememberSaveable(record.item.id) { mutableStateOf("") }
    var busy by remember(record.item.id) { mutableStateOf(false) }
    var readOnly by remember(record.item.id) { mutableStateOf(false) }
    var writeFailed by remember(record.item.id) { mutableStateOf(false) }
    var last by remember(record.item.id) { mutableStateOf<SuggestionRequest?>(null) }
    val canSay = !record.item.isDone && record.event.suggestion == null && !readOnly
    val failed = canSay && (mark is EventMark.Failed || record.event.now == "failed")

    fun send(text: String) {
        val instruction = text.trim()
        if (instruction.isEmpty() || busy) return
        val request = last?.takeIf { it.text == instruction }
            ?: SuggestionRequest(record.item.id, version = 0, action = SuggestionAction.Instruct, text = instruction)
        last = request
        busy = true; writeFailed = false; onStale(false)
        scope.launch {
            try {
                when (repository.actOnSuggestion(request)) {
                    SuggestionOutcome.Done -> {
                        last = null; words = ""; showSheet = false
                        feedback?.notice(res.getString(R.string.inbox_line_instructed), FeedbackStyle.Success)
                    }
                    SuggestionOutcome.Stale -> {
                        onStale(true)
                        // The repository usually applied the item carried by suggestion_stale. Only a missing answer needs a list read.
                        if (repository.snapshot.value.records.inboxRecord(record.item.id) === record) repository.refresh()
                        feedback?.notice(res.getString(R.string.inbox_suggestion_stale))
                    }
                }
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                if (hubCodedFailure(e)?.status == 403) { readOnly = true; showSheet = false }
                if (feedback?.failure(e, operation = true) != true) writeFailed = true
            } finally { busy = false }
        }
    }

    if (canSay) JTextButton(stringResource(if (failed) R.string.inbox_line_retry else R.string.inbox_line_say),
        onClick = { if (failed) send(res.getString(R.string.inbox_line_retry_text)) else showSheet = true },
        enabled = !busy, icon = if (failed) PersonaIcons.retry else PersonaIcons.agentChat)
    if (showSheet && canSay && !failed) JSheet(onDismiss = { showSheet = false }, title = stringResource(R.string.inbox_line_say),
        dismissible = !busy, scrollable = true) {
        Column(Modifier.fillMaxWidth().imePadding()) {
            JTextField(words, { words = it }, Modifier.fillMaxWidth(), label = stringResource(R.string.inbox_suggestion_say),
                placeholder = stringResource(R.string.inbox_suggestion_say_hint), minLines = 2, enabled = !busy)
            if (writeFailed) JNotice(stringResource(R.string.inbox_failed_retry), Modifier.padding(top = Tokens.Space.s))
            JButton(stringResource(R.string.inbox_suggestion_say_send), { send(words) }, Modifier.fillMaxWidth().padding(top = Tokens.Space.m),
                size = JButtonSize.Medium, enabled = !busy && words.isNotBlank(), loading = busy)
            JTextButton(stringResource(persona.core.designsystem.R.string.designsystem_cancel), { showSheet = false },
                Modifier.align(Alignment.End).padding(top = Tokens.Space.s), enabled = !busy)
        }
    }
}

/** The standalone handling row used by component tests; the event page instead composes [InboxSayAction] after its own state. */
@Composable
internal fun InboxSaySomething(
    repository: InboxRepository,
    record: InboxRecord,
    mark: EventMark?,
    onStale: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
) {
    Row(modifier.fillMaxWidth().padding(bottom = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.weight(1f)) { if (mark != null) EventStateLine(mark) }
        InboxSayAction(repository, record, mark, onStale)
    }
}
