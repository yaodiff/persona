package persona.feature.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import coil3.ImageLoader
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.designsystem.JSheet
import persona.core.designsystem.Tokens
import persona.core.domain.lines.ChatLine

/** Explicit inspection uses the original renderer, including nested work and lazy server-fold loading. */
@Composable
internal fun AssistantBackgroundRecords(
    lines: List<ChatLine>, session: SessionKey, loader: ImageLoader,
    onToggle: (String) -> Unit, onFold: (Item) -> Unit,
    folding: Set<String>, foldErrors: Map<String, String>, onFullText: (Item) -> Unit,
    onDismiss: () -> Unit,
    hideTaskPrompts: Boolean = false,
) {
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.chat_assistant_background_records)) {
        LazyColumn(contentPadding = PaddingValues(Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            items(lines.filterNot { it is ChatLine.Step || it is ChatLine.End }, key = { it.id }) { line ->
                val activity = (line as? ChatLine.Extra)?.payload as? persona.core.domain.tasks.AssistantExtra.Feed
                if (activity != null) {
                    // Task events are immutable audit data here, never a second actionable task card.
                    CodeText(persona.core.contracts.JsonCodec.encodeToString(persona.core.contracts.FeedItem.serializer(), activity.item), "json")
                } else ChatLineContent(line, session, loader, null, onToggle, onFold, folding, foldErrors,
                    onFullText = onFullText, hideTaskPrompts = hideTaskPrompts)
                if (line is ChatLine.Work && line.open) line.steps.forEachIndexed { index, step ->
                    ChatLineContent(ChatLine.Step(line.id, step, index == 0), session, loader, null,
                        onToggle, onFold, folding, foldErrors, onFullText = onFullText,
                        workRunning = line.live, lastWorkStep = index == line.steps.lastIndex, showToolViews = false)
                }
            }
        }
    }
}
