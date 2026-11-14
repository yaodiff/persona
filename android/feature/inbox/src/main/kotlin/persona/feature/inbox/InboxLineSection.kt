package persona.feature.inbox

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JTextButton
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.Tokens
import persona.core.designsystem.currentLocale
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.text
import persona.core.domain.inbox.InboxTier
import persona.core.domain.sort.stateLabel
import persona.core.navigation.Navigator
import persona.core.navigation.TodoDetailRoute

/** The item's Thread section. Loading, an empty line and a failed first read draw nothing, as on iOS and the web. */
@Composable
internal fun InboxLineSection(repository: InboxRepository, record: InboxRecord, navigator: Navigator, reload: Int = 0, modifier: Modifier = Modifier) {
    val snapshot by repository.snapshot.collectAsStateWithLifecycle()
    val pages = LocalInboxItemPages.current
    val feedback = LocalAppFeedback.current
    val scope = rememberCoroutineScope()
    var tree by remember(record.item.id) { mutableStateOf<InboxLineage?>(null) }
    var busy by remember(record.item.id) { mutableStateOf<String?>(null) }
    var showSame by remember(record.item.id) { mutableStateOf(false) }
    LaunchedEffect(record.item.id, record.manager, reload) {
        tree = null
        try { tree = repository.inboxLine(record) }
        catch (e: CancellationException) { throw e }
        catch (_: Exception) { tree = null }
    }
    val rows = tree?.let(::inboxLineRows).orEmpty()
    if (rows.isEmpty()) return

    fun open(row: InboxLineRow): (() -> Unit)? {
        row.item?.let { return { navigator.navigate(inboxRowPage(it, pages)) } }
        return when (row.parent.kind) {
            "todo" -> ({ navigator.navigate(TodoDetailRoute(row.parent.id)) })
            "session" -> row.parent.id.split('/', limit = 2).takeIf { it.size == 2 && it.all(String::isNotBlank) }?.let { (machine, session) ->
                { navigator.navigate(inboxConversationRoute(machine, session, record.item.id)) }
            }
            else -> null
        }
    }

    Column(modifier) {
        val notSameDone = stringResource(R.string.inbox_line_not_same_done)
        JSectionHeader(stringResource(R.string.inbox_line_title))
        rows.forEachIndexed { index, row ->
            val session = if (row.parent.kind == "session") snapshot.sessions.firstOrNull { it.session.key.path == row.parent.id } else null
            val liveTitle = row.item?.item?.title?.takeIf { it.isNotBlank() }
                ?: session?.session?.displayTitle?.takeIf { it.isNotBlank() }
            val title = liveTitle ?: when (row.parent.kind) {
                "inbox" -> stringResource(R.string.inbox_line_gone)
                else -> "" // As on the web: no invented title, just the one kind word in titleLead.
            }
            val kind = inboxLineKind(row)
            val status = row.item?.let { inboxLineStatus(it) } ?: session?.stateLabel?.text()
            val unlink = row.unlink
            JGroupItem(GroupPosition.of(index, rows.size)) {
                JRow(title = title, titleLead = if (title.isEmpty()) kind else "$kind ", subtitle = status, leadingIcon = when (row.parent.kind) {
                    "todo" -> PersonaIcons.todo
                    "session" -> PersonaIcons.agents
                    else -> PersonaIcons.inbox
                }, enabled = busy == null, onClick = open(row), trailing = {
                    if (unlink != null) JTextButton(stringResource(R.string.inbox_line_not_same), enabled = busy == null, onClick = {
                        busy = row.key
                        scope.launch {
                            try {
                                repository.setInboxParent(unlink, null)
                                // The write has succeeded. A failed reread is intentionally the same as a failed
                                // first read: hide the section rather than misreport the completed write as failed.
                                tree = try { repository.inboxLine(record) }
                                catch (e: CancellationException) { throw e }
                                catch (_: Exception) { null }
                                feedback?.notice(notSameDone, FeedbackStyle.Success)
                            } catch (e: CancellationException) { throw e }
                            catch (e: Exception) { feedback?.failure(e, operation = true) }
                            finally { busy = null }
                        }
                    })
                })
            }
        }
        JTextButton(stringResource(R.string.inbox_line_same), onClick = { showSame = true },
            Modifier.padding(horizontal = Tokens.Space.s), icon = PersonaIcons.link)
    }
    if (showSame) InboxSameMatterSheet(repository, record, initialTree = tree,
        onDismiss = { showSame = false }, onLinked = { tree = it })
}

/** The shared `now` words when the hub supplies them, with the pre-`now` words as the compatibility fallback. */
@Composable
internal fun inboxLineStatus(record: InboxRecord): String {
    val clock = LocalInboxClock.current
    inboxNowForRow(record, LocalResources.current, currentLocale, now = clock(), zone = LocalInboxZone.current)?.let { return it }
    if (record.item.isDone) return stringResource(if (record.event.doneBy == "manager") R.string.inbox_done_by_manager else R.string.inbox_done)
    return when (val mark = eventMark(record)) {
        is EventMark.Working -> stringResource(mark.word)
        EventMark.Preparing -> stringResource(R.string.inbox_handling_preparing)
        is EventMark.Failed -> mark.reason?.let { stringResource(R.string.inbox_handling_failed_reason, it) }
            ?: stringResource(R.string.inbox_handling_failed)
        is EventMark.Suggests -> stringResource(mark.kind)
        is EventMark.Now -> mark.text
        null -> when (record.item.section) {
            "working" -> stringResource(R.string.inbox_section_working)
            InboxTier.Action.wire -> stringResource(R.string.inbox_bay_needs_you)
            else -> inboxTierTitle(record.item.triage)
        }
    }
}

@Composable
private fun inboxLineKind(row: InboxLineRow): String = stringResource(when {
    !row.up -> R.string.inbox_line_next
    row.parent.kind == "todo" -> R.string.inbox_line_kind_todo
    row.parent.kind == "session" -> R.string.inbox_line_kind_session
    else -> R.string.inbox_line_kind_inbox
})
