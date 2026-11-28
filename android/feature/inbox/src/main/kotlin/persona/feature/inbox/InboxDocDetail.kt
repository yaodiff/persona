package persona.feature.inbox

import androidx.compose.ui.platform.LocalResources
import android.content.res.Resources
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.withStyle
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.InboxItemUiRoute
import persona.core.navigation.Navigator
import persona.core.inbox.*

// A Feishu document's notice in the inbox (docs/assistant/inbox.md "Docs: mentions in sheets, bases, and documents", #2323): a sheet / Base / docx that @'d the user or
// commented to them. It has no chat behind it, so it opens its own page: what happened, the rows about the user, and
// "Open document" in the app's Feishu document reader. The same page is where an item of a shape this app does not know opens,
// instead of the mail page (which can only fail for it).

/** What kind of document, as Feishu names it. */
internal fun docTypeName(res: Resources, type: String?): String = res.getString(when (type) {
    "sheet" -> R.string.inbox_doc_sheet
    "base" -> R.string.inbox_doc_base
    "docx", "doc" -> R.string.inbox_doc_doc
    else -> R.string.inbox_doc_other
})

/** The button that opens the document, named for its kind. */
private fun docOpenLabel(type: String?): Int = when (type) {
    "sheet" -> R.string.inbox_doc_open_sheet
    "base" -> R.string.inbox_doc_open_base
    "docx", "doc" -> R.string.inbox_doc_open_doc
    else -> R.string.inbox_doc_open_other
}

/** The list row's square in place of a chat avatar. */
internal fun docIcon(type: String?): PhIcon = when (type) {
    "sheet" -> PhIcons.SquaresFour
    "base" -> PhIcons.Kanban
    else -> PhIcons.FileText
}

/** "@me" or "Comment", as the web and iOS tag a document's notice. */
internal fun docKindLabel(res: Resources, doc: InboxDocRef?): String =
    res.getString(if (doc?.notice in setOf("comment", "reply")) R.string.inbox_doc_comment else R.string.inbox_mention_me)

/** The item's own page when it is neither a chat, a Moments post nor a mail: a document's notice, or an unknown item. */
@Composable
fun InboxItemDetailScreen(repository: InboxRepository, route: InboxItemDetailRoute, navigator: Navigator) {
    val inbox by repository.snapshot.collectAsStateWithLifecycle()
    val listedRecord = inbox.records.inboxRecord(route.itemId)
    var lookup by remember(route.itemId) { mutableStateOf<InboxItemLookupResult?>(null) }
    var lookupAttempt by remember(route.itemId) { mutableIntStateOf(0) }
    LaunchedEffect(repository, route.itemId, listedRecord, lookupAttempt) {
        if (listedRecord == null) {
            lookup = null
            lookup = readInboxItem(repository, route.itemId)
        } else lookup = null
    }
    val record = listedRecord ?: (lookup as? InboxItemLookupResult.Found)?.record
    var contextRevision by remember { mutableIntStateOf(0) }
    var assistantContext by remember(record?.item, contextRevision) { mutableStateOf<String?>(null) }
    LaunchedEffect(repository, record?.item, contextRevision) {
        val current = record ?: return@LaunchedEffect
        assistantContext = if (current.isAssistantRow) repository.personaContext(current.item.id)?.text else null
    }
    val open = LocalLarkLinkOpener.current
    val scope = rememberCoroutineScope()
    var refreshing by remember { mutableStateOf(false) }
    InboxItemExtensionScreen(repository, InboxItemUiRoute(route.itemId, "", "", record?.item?.title.orEmpty()), navigator,
        title = null, loading = false, refreshing = refreshing,
        onRefresh = {
            scope.launch {
                refreshing = true
                try {
                    repository.refreshNow()
                    if (record == null) lookupAttempt++
                } catch (e: CancellationException) { throw e } catch (_: Exception) { } finally {
                    refreshing = false
                    contextRevision++
                }
            }
        }) {
        item("item-extension-notice") { InboxExtensionNotice(route.itemId, route) }
        if (record == null) {
            when (val result = lookup) {
                null -> item("item-loading") { JLoading(text = stringResource(R.string.inbox_loading)) }
                is InboxItemLookupResult.Failed -> item("item-failed") {
                    JNotice(stringResource(R.string.inbox_refresh_failed), actionLabel = stringResource(R.string.inbox_retry),
                        onAction = { lookupAttempt++ })
                }
                InboxItemLookupResult.Missing -> item("item-missing") { StatusText(stringResource(R.string.inbox_item_missing)) }
                is InboxItemLookupResult.Found -> Unit
            }
        } else inboxItemBody(record, assistantContext, inbox.contentSource?.transport?.replyImages) { url -> open?.invoke(url, inbox.contentSource) }
    }
}

private fun InboxRecord.additionalAssistantText(text: String, loadedContext: Boolean): String {
    val summary = item.summary?.trim()?.takeIf { it.isNotEmpty() } ?: return text
    val trimmed = text.trim()
    fun String.afterSummary() = if (startsWith(summary) && getOrNull(summary.length)?.isWhitespace() != false)
        substring(summary.length).trimStart() else null
    val title = item.title.trim()
    if (loadedContext && title.isNotEmpty() && trimmed.startsWith(title) && trimmed.getOrNull(title.length)?.isWhitespace() != false) {
        val content = trimmed.substring(title.length).trimStart().afterSummary()
        if (content != null) return content
        if (title != summary) return text
    }
    return trimmed.afterSummary() ?: text
}

internal fun LazyListScope.inboxItemBody(record: InboxRecord, context: String? = null,
    images: persona.core.contracts.api.ReplyImageSource? = null, onOpen: (String) -> Unit) {
    val doc = record.doc
    if (doc == null) {
        if (record.isAssistantRow) {
            record.connector?.url?.takeIf { it.isNotBlank() }?.let { url ->
                item("assistant-open-chat") {
                    JButton(stringResource(R.string.inbox_open_chat), { onOpen(url) }, kind = JButtonKind.Secondary,
                        size = JButtonSize.Medium, icon = PersonaIcons.openExternal)
                }
            }
            if (!record.onlyTitleAndSummary(context)) {
                val loadedContext = context?.takeIf { it.isNotBlank() }
                (loadedContext ?: record.item.preview).takeIf { it.isNotBlank() }?.let { text ->
                    val content = record.additionalAssistantText(text, loadedContext != null)
                    if (content.isNotBlank()) {
                        item("assistant-content-header") { JSectionHeader(stringResource(R.string.inbox_content)) }
                        item("item-preview") { InboxPictureText(content, images) }
                    }
                }
            }
            return
        }
        record.item.preview.takeIf { it.isNotBlank() }?.let { text ->
            item("item-preview") { InboxPictureText(text, images) }
        }
        return
    }
    item("doc-open") {
        val kind = docTypeName(LocalResources.current, doc.type)
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(listOf(kind, record.item.preview).filter { it.isNotBlank() }.joinToString(" · "),
                style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
            JButton(stringResource(docOpenLabel(doc.type)), { onOpen(doc.url) }, kind = JButtonKind.Secondary, size = JButtonSize.Medium, icon = PhIcons.ArrowSquareOut)
            doc.reply?.let { Text(stringResource(R.string.inbox_doc_reply_to, it), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
        }
    }
    doc.error?.let { error ->
        item("doc-error") { JNotice(stringResource(R.string.inbox_doc_error, error), Modifier.padding(top = Tokens.Space.s), status = Status.NeedsYou) }
    }
    if (doc.rows.isEmpty()) return
    item("doc-rows") { JSectionHeader(stringResource(R.string.inbox_doc_rows), Modifier.padding(top = Tokens.Space.l)) }
    // A row's table is worth naming when the rows come from more than one, or from a Base's tables.
    val tables = doc.type == "base" || doc.rows.mapNotNull { it.table }.distinct().size > 1
    itemsIndexed(doc.rows, key = { index, _ -> "doc-row-$index" }) { _, row ->
        JCard(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.s), padding = Tokens.Space.m) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                Text(row.title, style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink, modifier = Modifier.weight(1f))
                row.table?.takeIf { tables }?.let { JTag(it) }
            }
            row.fields.forEach { field ->
                Text(buildAnnotatedString {
                    withStyle(SpanStyle(color = PersonaTheme.palette.ink3)) { append(field.name + "  ") }
                    append(field.value)
                }, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink, maxLines = 3)
            }
            if (row.status.isNotEmpty()) Row(Modifier.horizontalScroll(rememberScrollState()).padding(top = Tokens.Space.xs),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                row.status.forEach { JTag(stringResource(R.string.inbox_doc_field, it.name, it.value.ifBlank { stringResource(R.string.inbox_doc_not_set) })) }
            }
        }
    }
}
