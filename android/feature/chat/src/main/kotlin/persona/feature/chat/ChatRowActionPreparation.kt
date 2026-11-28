package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CoroutineScope
import persona.core.contracts.Item
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.ChatSnapshot

/** Row actions prepared outside the large page composable, with the same state owners and remember keys. */
internal class ChatRowActionPreparation(
    val menu: MessageMenuState,
    val viewer: MutableState<ViewerRequest?>,
    val actions: ChatRowActions,
    val retryId: String?,
    val exhausted: ExhaustedRecovery?,
    val turnFix: TurnErrorFix?,
)

@Composable
internal fun rememberChatRowActionPreparation(
    key: SessionKey,
    repository: ChatRepository,
    chrome: ChatChromeBindings,
    scope: CoroutineScope,
    renderStyle: ChatRenderStyle,
    session: Session?,
    projection: ChatSnapshot,
    readOnly: Boolean,
    canSend: Boolean,
    pending: Boolean,
    currentItems: () -> List<Item>,
    menu: MessageMenuState,
    onRetry: (String) -> Unit,
): ChatRowActionPreparation {
    val viewer = remember { mutableStateOf<ViewerRequest?>(null) }
    val photos = remember(chrome.files, key, session?.cwd) {
        chrome.files?.let { AttachedPhotos(it, key.machineId, session?.cwd.orEmpty()) }
    }
    val localCacheDir = androidx.compose.ui.platform.LocalContext.current.cacheDir
    val localImages = remember(chrome.files, chrome.hubScope, key.machineId, session?.cwd, localCacheDir) {
        chrome.files?.let { LocalMarkdownImages(it, chrome.hubScope, key.machineId, session?.cwd.orEmpty(), localCacheDir) }
    }
    val rawTurnFix = remember(projection.lines) {
        (projection.lines.lastOrNull() as? ChatLine.ApiError)
            ?.takeIf { it.info.toolSearchUnsupported }?.let { TurnErrorFix.ToolSearchUnsupported }
    }
    val turnFix = if (renderStyle == ChatRenderStyle.Assistant) {
        if (session?.turnErrorCode.isNullOrBlank()) rawTurnFix else TurnErrorFix.of(session.turnErrorCode)
    } else null
    val exhausted = remember(projection.lines, session?.modelFallback) {
        exhaustedRecovery(projection.lines, session?.modelFallback)
    }
    val retryAllowed = !readOnly && canSend && session?.turnErrorCode != FALLBACK_UNAVAILABLE
    val retryId = remember(projection.lines, pending, retryAllowed, exhausted != null, turnFix) {
        retryLine(projection.lines, pending, retryAllowed, exhausted = exhausted != null, turnFix = turnFix)
    }
    val pathPresence = remember(chrome.files, key, session?.cwd, chrome.onOpenPath != null) {
        val files = chrome.files
        val cwd = session?.cwd.orEmpty().ifBlank { "~" }
        if (files == null || key.machineId.isBlank() || chrome.onOpenPath == null) null
        else PathPresence(scope, { paths -> files.stat(key.machineId, cwd, paths) })
    }
    val continueText = stringResource(R.string.chat_screen_continue_message)
    val imageDescription = stringResource(R.string.chat_content_image)
    val replyImages = remember(key, repository) { (repository as? ChatLocalStorage)?.replyImages(key) }
        ?.collectAsStateWithLifecycle(initialValue = null)?.value
    val currentViewer by rememberUpdatedState(viewer)
    val currentSend by rememberUpdatedState(onRetry)
    val actions = rememberChatRowActions(key, repository, replyImages, localImages, photos, retryId, exhausted,
        chrome.onOpenPath, pathPresence, chrome.files, session?.cwd, chrome.onOpenPath,
        { images, index -> currentViewer.value = ViewerRequest(images, index) }, currentItems, menu,
        { currentSend(continueText) }, imageDescription)
    return ChatRowActionPreparation(menu, viewer, actions, retryId, exhausted, turnFix)
}
