package persona.feature.chat

import androidx.compose.runtime.*
import android.content.res.Resources
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import persona.core.contracts.Session
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.hubCodedFailure
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JAction
import persona.core.designsystem.JActionSheet
import persona.core.designsystem.LocalPersonaName
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.rememberCopy
import persona.core.designsystem.QueueStrip
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.operationFeedback
import persona.core.domain.sort.canSend
import persona.core.domain.sort.ownQueued
import persona.core.domain.sort.steersQueue
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** "2 queued · Claude will handle them in order": how many wait, and for whom (iOS QueueStrip's line). */
internal fun queueTitle(count: Int, agent: String, resources: Resources): String =
    resources.getQuantityString(R.plurals.chat_queue_title, count, count, agent)

/**
 * What to put back when one of the user's [own] queued messages is pulled out: every other one of them, in order (an
 * empty list is meaningful). `keep` indexes only the user's own (docs/protocol/protocol.md, POST …/queue).
 */
internal fun queueKeep(own: Int, index: Int): List<Int> = (0 until own).filter { it != index }

/**
 * The `keep` of POST …/queue that moves the message at [index] [step] places (-1 up, 1 down) and re-queues them all in
 * the new order (iOS `QueueOrder.moved`; `docs/protocol/protocol.md`, POST …/queue); null when that is past either end.
 */
internal fun queueMoved(size: Int, index: Int, step: Int): List<Int>? {
    val to = index + step
    if (step == 0 || index !in 0 until size || to !in 0 until size) return null
    return (0 until size).toMutableList().apply { removeAt(index); add(to, index) }
}

/** What a queued message's sheet offers, in iOS's order. */
internal enum class QueueAction { Edit, MoveUp, MoveDown, Copy, Delete }

/**
 * The actions of the message at [index] among the user's [own] ones, each with whether it can be taken there (iOS
 * QueueStrip's menu): Move Up and Move Down only where the queue can be reordered ([steers]) and the user has more than
 * one there, greyed at the ends. A message past [own], relayed from elsewhere, is only copied.
 */
internal fun queueActions(index: Int, own: Int, steers: Boolean): List<Pair<QueueAction, Boolean>> = buildList {
    if (index >= own) { add(QueueAction.Copy to true); return@buildList }
    add(QueueAction.Edit to true)
    if (steers && own > 1) {
        add(QueueAction.MoveUp to (index > 0))
        add(QueueAction.MoveDown to (index < own - 1))
    }
    add(QueueAction.Copy to true)
    add(QueueAction.Delete to true)
}

/**
 * Its own queued messages can be sent now and reordered (iOS QueueStrip's `steers`): Claude Code in a terminal pane
 * ([steersQueue]), or a Persona conversation, whose Claude Code takes them as an interrupt over stream-json
 * (docs/protocol/protocol.md, POST …/queue/send-now). Only where the phone may send.
 */
internal val Session.reordersQueue: Boolean get() = canSend && (steersQueue || assistant)

/**
 * What a refused Send Now says: the hub's own sentence for a 409 it words in the reader's language (nothing_queued,
 * send_now_…, node_outdated, as iOS shows them), else the app's words for the failure.
 */
internal fun sendNowFailure(error: Throwable, resources: Resources): String {
    val words = hubCodedFailure(error)?.takeIf { it.status == 409 && it.code in SEND_NOW_REFUSALS }
        ?.let { (it as? Throwable)?.message }?.takeIf { it.isNotBlank() && !it.startsWith("HTTP ") }
    return words ?: operationFeedback(error).text.resolve(resources)
}

private val SEND_NOW_REFUSALS = setOf("nothing_queued", "send_now_unsupported", "send_now_unrecognized", "send_now_in_doubt", "node_outdated",
    // A Persona conversation whose Claude Code refused the interrupt.
    "assistant_control_failed")

/**
 * Queue strip (iOS QueueStrip): what was typed while the agent works and not taken yet, right above the composer (on
 * Glass folded to its one line until tapped). Tapping a message offers "Take back to edit" (back into the box, after the
 * draft on a new line), "Copy" and "Delete"; for Claude in a terminal pane and Persona's conversations
 * ([reordersQueue]) also "Move up" / "Move down", and on Glass the line's "Send now" at one press (#7170, as the web
 * since #7154). TalkBack reads what that confirmation used to say with the button. In Persona's conversation the line
 * names Persona, and the messages relayed from other conversations or the system come last ([ownQueued]), each saying
 * it arrives when the chat is free and offering only "Copy". Editing and moving go
 * through the node's session.queue.edit (POST …/queue {keep}): it pulls the whole queue out of the agent and puts the
 * kept ones back in the order given, and answers with what was queued, so a pulled-back message is read from that
 * answer. Send now is POST …/queue/send-now. Only for a session the phone may send to. [initiallyExpanded]: opened from
 * the start (screenshots).
 */
@Composable
internal fun ChatQueueStrip(
    session: Session?,
    actions: ChatActions?,
    composer: ChatComposerState,
    onNotice: (String) -> Unit,
    refresh: suspend () -> Unit,
    initiallyExpanded: Boolean = false,
) {
    val queued = session?.queued.orEmpty()
    if (session == null || queued.isEmpty()) return
    val scope = rememberCoroutineScope()
    val copy = rememberCopy()
    val resources = LocalResources.current
    val context = LocalContext.current
    var busy by remember { mutableStateOf(false) }
    // The row picked, with its words: if the queue moves under the open menu (the agent took one), the menu closes.
    var picked by remember { mutableStateOf<Pair<Int, String>?>(null) }
    LaunchedEffect(queued) { picked?.let { (index, text) -> if (queued.getOrNull(index) != text) picked = null } }
    val enabled = session.canSend && actions != null
    val steers = enabled && session.reordersQueue
    // The user's own come first; only they are changed (an index past them is a relayed message).
    val own = session.ownQueued
    val persona = LocalPersonaName.current.takeIf { session.assistant }
    // One operation at a time, busy until the queue on screen is the new one, so a stale row cannot be picked.
    fun operate(failed: (Exception) -> String, operation: suspend (ChatActions) -> Unit) {
        val service = actions ?: return
        if (busy || !session.canSend) return
        busy = true
        scope.launch {
            try { operation(service) }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { onNotice(failed(e)) }
            finally {
                try { refresh() } catch (e: CancellationException) { throw e } catch (_: Exception) { }
                busy = false
            }
        }
    }
    val changeFailed = { e: Exception -> resources.getString(R.string.chat_queue_failed, operationFeedback(e).text.resolve(resources)) }
    fun edit(index: Int, delete: Boolean) {
        if (index >= own) return
        operate(changeFailed) { service ->
            val pulled = service.editQueue(session, queueKeep(own, index))
            if (delete) { onNotice(resources.getString(R.string.chat_queue_deleted)); return@operate }
            val taken = pulled.queue.getOrNull(index)
            if (taken == null) { onNotice(resources.getString(R.string.chat_queue_not_found)); return@operate }
            composer.restoreQueued(taken)
            // A Persona conversation's photos come beside the queue as base64 (the words carry no paths for them): they
            // go back into the draft as photos, and what could not is said (iOS `restoreQueued`).
            val photos = pulled.imagesOf(index)
            val restored = if (photos.isEmpty()) null else withContext(Dispatchers.IO) { restoredPhotos(context, photos) }
            restored?.let { composer.restorePhotos(it.attachments) }
            onNotice(if (restored != null && restored.lost > 0) resources.getQuantityString(R.plurals.chat_queue_photos_lost, restored.lost, restored.lost)
                else resources.getString(R.string.chat_queue_restored))
        }
    }
    fun move(index: Int, step: Int) {
        val keep = queueMoved(own, index, step) ?: return
        operate(changeFailed) { it.editQueue(session, keep) }
    }
    fun sendNow() = operate({ sendNowFailure(it, resources) }) {
        it.sendQueuedNow(session)
        onNotice(resources.getString(R.string.chat_queue_sent_now))
    }
    val sendNowLabel = stringResource(R.string.chat_queue_send_now)
    // What the confirmation used to say, now spoken with the button and not drawn (#7170).
    val sendNowDescription = persona?.let { stringResource(R.string.chat_queue_send_now_message_persona, it) }
        ?: stringResource(R.string.chat_queue_send_now_message)
    val photoMark = stringResource(R.string.chat_composer_photo_placeholder)
    val videoMark = stringResource(R.string.chat_composer_video_placeholder)
    // Glass says how many photos a Persona message carries under its words (iOS's "N images", from what this phone sent), not as a mark among them.
    val photoCounts = if (persona != null && PersonaTheme.style == AppStyle.Glass) queued.take(own).mapIndexed { _, text -> composerPhotoCount(text) }
        else emptyList()
    // The owner's own rows and the ones other conversations sent in are separate: since #6394 the latter collapse to
    // one summary line inside QueueStrip instead of spreading over the composer.
    val ownItems = queued.take(own).mapIndexed { index, text ->
        composerMessagePreview(text, if (photoCounts.getOrElse(index) { 0 } > 0) "" else photoMark, videoMark)
    }
    val relayedItems = queued.drop(own).map { text -> composerMessagePreview(text, photoMark, videoMark) }
    val relayedSummary = relayedItems.takeIf { it.isNotEmpty() }?.let {
        resources.getQuantityString(R.plurals.chat_queue_relayed_summary, it.size, it.size)
    }
    // A new session's queue starts folded (Glass), as each chat's does on iOS. The title counts everything waiting;
    // inside, the owner's own rows stay editable and the relayed backlog is the collapsed summary line (#6394).
    key(session.key) {
        QueueStrip(queueTitle(queued.size, persona ?: session.agentLabel, resources),
            ownItems,
            onItem = { index -> queued.getOrNull(index)?.let { picked = index to it } }, busy = busy, enabled = enabled,
            sendNow = sendNowLabel.takeIf { steers && own > 0 },
            sendNowDescription = sendNowDescription.takeIf { steers && own > 0 }, onSendNow = { sendNow() },
            initiallyExpanded = initiallyExpanded, photos = photoCounts,
            relayedItems = relayedItems, relayedSummary = relayedSummary,
            onRelayedItem = { index -> queued.getOrNull(index)?.let { picked = index to it } })
    }
    val (index, text) = picked ?: return
    val labels = mapOf(
        QueueAction.Edit to stringResource(R.string.chat_queue_edit), QueueAction.MoveUp to stringResource(R.string.chat_queue_move_up),
        QueueAction.MoveDown to stringResource(R.string.chat_queue_move_down), QueueAction.Copy to stringResource(R.string.chat_queue_copy),
        QueueAction.Delete to stringResource(R.string.chat_queue_delete),
    )
    JActionSheet(queueActions(index, own, steers).map { (action, allowed) ->
        val writes = action != QueueAction.Copy
        JAction(labels.getValue(action), when (action) {
            QueueAction.Edit -> PersonaIcons.edit
            QueueAction.MoveUp -> PersonaIcons.queueUp
            QueueAction.MoveDown -> PersonaIcons.queueDown
            QueueAction.Copy -> PersonaIcons.copy
            QueueAction.Delete -> PersonaIcons.delete
        }, destructive = action == QueueAction.Delete, enabled = allowed && (!writes || (enabled && !busy))) {
            when (action) {
                QueueAction.Edit -> edit(index, delete = false)
                QueueAction.MoveUp -> move(index, -1)
                QueueAction.MoveDown -> move(index, 1)
                QueueAction.Copy -> copy(text, null)
                QueueAction.Delete -> edit(index, delete = true)
            }
        }
    }, onDismiss = { picked = null }, title = composerMessagePreview(text))
}
