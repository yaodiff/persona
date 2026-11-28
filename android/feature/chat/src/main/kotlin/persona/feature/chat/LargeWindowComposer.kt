package persona.feature.chat

import android.view.DragEvent
import androidx.compose.foundation.draganddrop.dragAndDropTarget
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.FlowRowScope
import androidx.compose.foundation.layout.RowScope
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draganddrop.toAndroidDragEvent
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.isCtrlPressed
import androidx.compose.ui.input.key.isMetaPressed
import androidx.compose.ui.input.key.isShiftPressed
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlin.coroutines.coroutineContext
import persona.core.contracts.ImageLimits
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatComposerService
import persona.core.designsystem.ComposerDropTargetFrame
import persona.core.designsystem.ComposerHardwareActions
import persona.core.designsystem.LocalComposerHardware
import persona.core.designsystem.LocalHubImageLimits
import persona.core.designsystem.LocalLargeWindowInput
import persona.core.designsystem.ModelPillWidth
import persona.core.designsystem.hardwareShortcutTextInput

/**
 * The production chat composer with large-window input integrations. Compact windows disable every integration but keep
 * the same neutral composition identity, so resizing cannot cancel an upload or move the composer to another parent.
 */
@Composable
fun LargeWindowComposer(
    session: SessionKey,
    state: ChatComposerState,
    working: Boolean,
    sending: Boolean,
    canClear: Boolean,
    service: ChatComposerService?,
    uploadDestination: String? = null,
    onSend: () -> Unit,
    onCommand: (String) -> Unit,
    onNotice: (String) -> Unit,
    extraContent: Boolean = false,
    assistantChat: Boolean = false,
    enabled: Boolean = true,
    placeholder: String = stringResource(R.string.chat_composer_placeholder),
    leading: (@Composable RowScope.() -> Unit)? = null,
    accessory: (@Composable ColumnScope.() -> Unit)? = null,
    hintChips: (@Composable FlowRowScope.() -> Unit)? = null,
    modeHint: String? = null,
    onOverlay: (Boolean) -> Unit = {},
    visible: Boolean = true,
    agentName: String = "Claude",
    canInterrupt: Boolean = true,
    mentions: ComposerMentions? = null,
    onMention: (ChatLinkContext) -> Unit = {},
    onMentionTask: (String) -> Unit = {},
    onMentionOpen: (Boolean) -> Unit = {},
    onModel: (() -> Unit)? = null,
    // /permissions opens the chat's permission mode picker (#6513); null where the chat has none.
    onPermissions: (() -> Unit)? = null,
    onWorkflows: (() -> Unit)? = null,
    loop: Boolean = false,
    blockedLabel: String? = null,
    onBlocked: () -> Unit = {},
    useShellSurface: Boolean = false,
    modelPill: (@Composable (ModelPillWidth) -> Unit)? = null,
    upload: ComposerAttachmentUpload? = null,
    wrapPlaceholder: Boolean = false,
    covered: Boolean = false,
    imageLimits: ImageLimits = LocalHubImageLimits.current.forAgent(null),
) {
    val largeWindow = LocalLargeWindowInput.current
    val dictate = rememberComposerDictation(session, state, visible && enabled, onNotice)
    if (covered) SideEffect { dictate?.session?.cover() }
    val composer: @Composable () -> Unit = {
        ChatComposer(
            session = session, state = state, working = working, sending = sending, canClear = canClear,
            service = service, uploadDestination = uploadDestination, upload = upload, onSend = onSend, onCommand = onCommand, onNotice = onNotice,
            extraContent = extraContent, assistantChat = assistantChat, enabled = enabled, placeholder = placeholder, leading = leading,
            accessory = accessory, hintChips = hintChips, modeHint = modeHint, onOverlay = onOverlay, visible = visible, dictate = dictate.takeUnless { covered },
            agentName = agentName, canInterrupt = canInterrupt, mentions = mentions, onMention = onMention,
            onMentionTask = onMentionTask, onMentionOpen = onMentionOpen, onModel = onModel, onPermissions = onPermissions, onWorkflows = onWorkflows, loop = loop,
            blockedLabel = blockedLabel, onBlocked = onBlocked, useShellSurface = useShellSurface, modelPill = modelPill, wrapPlaceholder = wrapPlaceholder,
            imageLimits = imageLimits,
        )
    }

    val mainClear = LocalMainChatClear.current?.takeIf { enabled }
    val currentSubmit by rememberUpdatedState {
        if (!covered && dictate?.active != true) dispatchComposerSend(state, extraContent, mainClear, onPermissions, onWorkflows, onSend)
    }
    val sendAction = remember { { currentSubmit() } }
    val hasContent = state.hasContent || extraContent
    val canSend = visible && !covered && enabled && state.ready && !sending && dictate?.active != true
    val sendEnabled = canSend && hasContent && blockedLabel == null
    val collapseForSend = remember(state, extraContent, onPermissions) {
        { collapse: () -> Unit ->
            val focusRequest = state.focusRequest
            val opensPermissions = isPermissionsCommand(state, extraContent, onPermissions)
            collapse()
            state.focusRequest = focusRequest
            // The leading mode control owns the requested picker; keep it composed until that picker is dismissed.
            state.focused = opensPermissions
        }
    }
    val collapseForEscape = remember(state) {
        { collapse: () -> Unit ->
            collapse()
            state.focused = true
        }
    }
    val hardware = remember(sendEnabled, collapseForSend, collapseForEscape) {
        ComposerHardwareActions(send = if (sendEnabled) sendAction else null,
            collapseForSend = collapseForSend, collapseForEscape = collapseForEscape)
    }

    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var dropHover by remember { mutableStateOf(false) }
    var dropImporting by remember { mutableStateOf(false) }
    val canDrop = largeWindow && visible && enabled && service != null && !sending && blockedLabel == null && !dropImporting
    val currentCanDrop by rememberUpdatedState(canDrop)
    val dropFailure = stringResource(R.string.chat_composer_drop_failed)
    val currentDrop by rememberUpdatedState<(DragEvent) -> Boolean> { event ->
        val uris = composerDropUris(event.clipData)
        if (uris.isEmpty()) {
            false
        } else {
            val permissions = context.requestComposerDropPermissions(event)
            dropImporting = true
            scope.launch {
                try {
                    for (uri in uris) {
                        val result = withContext(NonCancellable + Dispatchers.IO) {
                            importDroppedAttachment(context, uri, state.attachments, limits = imageLimits, canSpill = service != null)
                        }
                        result.attachment?.let { attachment ->
                            ComposerDropCleanup.track(state, context)
                            state.beginEdit()
                            state.attachments += attachment
                        }
                        result.notice?.let(onNotice)
                        // A cancelled wrapper still owns the completed copy through the draft; stop before the next URI.
                        coroutineContext.ensureActive()
                    }
                } catch (cancelled: CancellationException) {
                    throw cancelled
                } catch (_: Exception) {
                    onNotice(dropFailure)
                } finally {
                    permissions?.release()
                    dropImporting = false
                    dropHover = false
                }
            }
            true
        }
    }
    val dropTarget = remember {
        ComposerDropTarget(
            accepts = { currentCanDrop },
            hovering = { dropHover = it },
            drop = { currentDrop(it) },
        )
    }
    LaunchedEffect(canDrop) { if (!canDrop) dropHover = false }
    SideEffect {
        if (state.attachments.any { isDropUri(it.uri, context.packageName) }) ComposerDropCleanup.track(state, context)
    }

    val integrationModifier = if (largeWindow) Modifier.onPreviewKeyEvent { event ->
        val action = composerKeyAction(ComposerKeyInput(
            key = when (event.key) {
                Key.Enter, Key.NumPadEnter -> ComposerKey.Enter
                Key.Escape -> ComposerKey.Escape
                else -> ComposerKey.Other
            },
            press = if (event.type == KeyEventType.KeyDown) ComposerKeyPress.Down else ComposerKeyPress.Up,
            repeatCount = event.nativeKeyEvent.repeatCount,
            ctrlPressed = event.isCtrlPressed,
            metaPressed = event.isMetaPressed,
            shiftPressed = event.isShiftPressed,
            composing = state.value.composition != null,
            canSend = canSend,
            hasContent = hasContent,
            blocked = blockedLabel != null,
        ))
        when (action) {
            ComposerKeyAction.Send -> { sendAction(); true }
            ComposerKeyAction.Consume -> true
            ComposerKeyAction.Collapse,
            ComposerKeyAction.PassThrough -> false
        }
    }.hardwareShortcutTextInput { state.value.composition == null }.dragAndDropTarget(
        shouldStartDragAndDrop = { event ->
            currentCanDrop && composerDropAccepts(event.toAndroidDragEvent().clipDescription)
        },
        target = dropTarget,
    ) else Modifier

    ComposerDropTargetFrame(
        active = largeWindow && (dropHover || dropImporting),
        importing = dropImporting,
        label = stringResource(R.string.chat_composer_drop_target),
        importingLabel = stringResource(R.string.chat_composer_drop_importing),
        modifier = integrationModifier,
        enabled = largeWindow,
    ) {
        CompositionLocalProvider(LocalComposerHardware provides (if (largeWindow) hardware else null)) {
            composer()
        }
    }
}
