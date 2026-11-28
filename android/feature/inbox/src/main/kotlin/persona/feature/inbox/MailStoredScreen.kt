package persona.feature.inbox

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
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
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.inbox.*
import persona.core.navigation.Navigator

/**
 * A mail's page on a hub whose mail module is off ([MailAccess.StoredOnly], #15995). The whole message, its HTML and its
 * attachments are read from the mail server each time, through the module's routes, so they cannot be shown; what the
 * hub stored for the Inbox item can: who it is from, when, its summary and up to 3,000 characters of the newest
 * message's text (the item's `context`, a core route). The page shows those and one line saying why there is no more.
 *
 * It asks no mail route: no message, no accounts, no flags (opening it marks nothing read), no attachment. Nothing that
 * would send a mail is offered (reply, reply all, forward, the AI's draft). Hand to AI and Done are the Inbox's own and stay.
 */
@Composable
internal fun MailStoredScreen(repository: InboxRepository, route: MailRoute, navigator: Navigator) {
    val scope = rememberCoroutineScope()
    val res = LocalResources.current
    val inbox by repository.snapshot.collectAsStateWithLifecycle()
    val listed = inbox.records.inboxRecord(route.itemId)
    var lookup by remember { mutableStateOf<InboxItemLookupResult?>(null) }
    var reload by remember { mutableIntStateOf(0) }
    LaunchedEffect(repository, listed == null, reload) {
        lookup = if (listed == null) readInboxItem(repository, route.itemId) else null
    }
    val record = listed ?: (lookup as? InboxItemLookupResult.Found)?.record
    // The stored text is read once per item (and again on a pull): null while it is on its way.
    var stored by remember { mutableStateOf<PersonaContext?>(null) }
    var read by remember { mutableStateOf(false) }
    LaunchedEffect(repository, record?.item?.id, reload) {
        val id = record?.item?.id ?: return@LaunchedEffect
        stored = repository.personaContext(id)
        read = true
    }
    var refreshing by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var handingOff by remember { mutableStateOf(false) }
    val text = stored?.text?.takeIf { it.isNotBlank() } ?: record?.item?.preview.orEmpty()
    // Until the route answers, the notice stays today's wording (Unknown); never claim "whole" or "clipped".
    val clipping = stored?.mailClipping ?: StoredMailClipping.Unknown

    JListPage(title = record?.item?.title?.ifBlank { stringResource(R.string.inbox_mail_no_subject) } ?: stringResource(R.string.inbox_mail_detail_title),
        onBack = navigator::back, refreshing = refreshing,
        onRefresh = {
            scope.launch {
                refreshing = true
                try { repository.refresh() } catch (e: CancellationException) { throw e } catch (_: Exception) { }
                finally { refreshing = false; reload++ }
            }
        },
        bottomBar = {
            if (record != null) DecisionButtons(JAction(stringResource(R.string.inbox_hand_to_ai)) { handingOff = true },
                Modifier.imePadding().padding(Tokens.Space.m))
        }) {
        item("status") {
            InboxExtensionNotice(route.itemId, route)
            if (record == null) when (lookup) {
                null -> JLoading(text = stringResource(R.string.inbox_loading))
                is InboxItemLookupResult.Failed -> JNotice(stringResource(R.string.inbox_refresh_failed))
                InboxItemLookupResult.Missing -> StatusText(stringResource(R.string.inbox_item_missing))
                is InboxItemLookupResult.Found -> Unit
            }
            error?.let { JNotice(it, status = Status.NeedsYou) }
        }
        if (record == null) return@JListPage
        item("metadata") {
            JCard {
                SelectionContainer {
                    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        MailHeaderRow(stringResource(R.string.inbox_mail_from), record.item.sender?.takeIf { it.isNotBlank() }
                            ?: stringResource(R.string.inbox_mail_not_given))
                        record.item.updatedAt?.takeIf { it.isNotBlank() }?.let { MailHeaderRow(stringResource(R.string.inbox_mail_time), mailTime(res, it)) }
                    }
                }
            }
        }
        record.item.summary?.takeIf { it.isNotBlank() }?.let { summary ->
            item("summary") { JNotice(summary, Modifier.padding(top = Tokens.Space.m), title = stringResource(R.string.inbox_ai_summary), icon = PersonaIcons.ai) }
        }
        item("body") {
            Column(Modifier.padding(top = Tokens.Space.m)) {
                when {
                    text.isNotBlank() -> SelectionContainer {
                        ReadingContent { Text(text, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink) }
                    }
                    !read -> JLoading(text = stringResource(R.string.inbox_mail_loading))
                    else -> Text(stringResource(R.string.inbox_mail_stored_empty), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink2)
                }
            }
        }
        item("mail-off") {
            // Only a hub `true` adds that the text is cut short. Android's existing sentence makes no clipping claim,
            // so Whole and Unknown both keep it exactly (#15995, the web StoredMailClipping wording rule).
            val offNotice = when (clipping) {
                StoredMailClipping.Clipped -> R.string.inbox_mail_module_off_clipped
                StoredMailClipping.Whole, StoredMailClipping.Unknown -> R.string.inbox_mail_module_off
            }
            JNotice(stringResource(offNotice), Modifier.padding(top = Tokens.Space.m))
        }
        item("item-state") {
            JTextButton(stringResource(if (record.item.isDone) R.string.inbox_move_back else R.string.inbox_mail_mark_done), enabled = !busy, onClick = {
                busy = true
                scope.launch {
                    try { repository.setDone(route.itemId, !record.item.isDone); repository.refresh(); error = null }
                    catch (e: CancellationException) { throw e }
                    catch (_: Exception) { error = res.getString(R.string.inbox_mail_item_state_failed) }
                    finally { busy = false }
                }
            })
        }
    }
    if (handingOff && record != null) InboxHandOffSheet(repository, record, navigator, onDismiss = { handingOff = false },
        context = text.takeIf { it.isNotBlank() })
}
