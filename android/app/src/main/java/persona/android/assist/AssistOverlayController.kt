package persona.android.assist

import android.graphics.Bitmap
import android.util.Log
import java.util.UUID
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import persona.core.contracts.ModelPick
import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.DigitalAssistantModelCatalog
import persona.core.contracts.api.DigitalAssistantModelChoice
import persona.core.contracts.api.OutboxMessage
import persona.feature.chat.ChatLinkContext
import persona.feature.chat.ComposerAttachment

internal const val ASSIST_LOG_TAG = "PersonaAssist"

internal enum class AssistCaptureStatus {
    /** VoiceInteractionSession opened; awaiting async `onHandleScreenshot` callback. */
    Waiting,

    /** Screenshot is staged strictly in memory; NOT written to disk and NOT attached yet. */
    Available,

    /** User manually tapped "Attach current screen"; written to `cacheDir/assist/` for next send. */
    Attached,

    /** System explicitly returned null (`FLAG_SECURE` / protected page) or bitmap preparation failed. */
    Unavailable,

    /** System did not deliver `onHandleScreenshot` before timeout; still upgrades if a late bitmap arrives. */
    TimedOut,

    /** User disabled system Digital Assistant screenshots (e.g. `assist_screenshot_enabled = 0` or userDisabledShowContext). */
    DisabledBySettings,

    /** Entry point does not provide screenshots, device is locked, or capture was released after send. */
    NotSupported,
}

internal enum class AssistHubStatus {
    Connecting,
    Ready,
    Offline,
    Unpaired,
}

internal enum class AssistTurnDelivery {
    Sending,
    Sent,
    Queued,
    Failed,
}

internal data class AssistOverlayTurn(
    val id: String,
    val fromUser: Boolean,
    val text: String,
    val streaming: Boolean = false,
    val hasScreenAttachment: Boolean = false,
    val delivery: AssistTurnDelivery = AssistTurnDelivery.Sent,
)

/** Target conversation routed by the Digital Assistant floating overlay. */
internal sealed interface AssistTargetConversation {
    /** Default: the Hub's main conversation (never auto-creates side conversations). */
    data object Main : AssistTargetConversation

    /** An existing non-main assistant conversation (or a newly created standalone conversation). */
    data class Existing(
        val key: SessionKey,
        val title: String,
        val modelPick: ModelPick? = null,
    ) : AssistTargetConversation

    /** Staged new conversation: next send creates a standalone non-main chat with the chosen model. */
    data object NewChat : AssistTargetConversation
}

/** One non-archived, sendable non-main assistant conversation exposed in the overlay picker. */
internal data class AssistConversationOption(
    val key: SessionKey,
    val title: String,
    val modelPick: ModelPick? = null,
    val working: Boolean = false,
)

internal data class AssistHubSnapshot(
    val status: AssistHubStatus = AssistHubStatus.Connecting,
    val mainSession: SessionKey? = null,
    val mainModelPick: ModelPick? = null,
    val mainWorking: Boolean = false,
    val activeSession: SessionKey? = mainSession,
    val activeTitle: String? = null,
    val activeModelPick: ModelPick? = mainModelPick,
    val activeWorking: Boolean = mainWorking,
    val conversations: List<AssistConversationOption> = emptyList(),
    val chatSnapshot: ChatSnapshot? = null,
    val outbox: List<OutboxMessage> = emptyList(),
)

internal data class AssistOverlayUiState(
    val invocationId: String = "",
    val deviceLocked: Boolean = false,
    val captureStatus: AssistCaptureStatus = AssistCaptureStatus.Waiting,
    val attachedScreenshot: ComposerAttachment? = null,
    val attachedContext: ChatLinkContext? = null,
    val hasStagedContext: Boolean = false,
    val attachingScreen: Boolean = false,
    val hubStatus: AssistHubStatus = AssistHubStatus.Connecting,
    val mainSession: SessionKey? = null,
    val targetConversation: AssistTargetConversation = AssistTargetConversation.Main,
    val availableConversations: List<AssistConversationOption> = emptyList(),
    val modelChoice: DigitalAssistantModelChoice = DigitalAssistantModelChoice(),
    val modelPickerOpen: Boolean = false,
    val modelCatalog: DigitalAssistantModelCatalog = DigitalAssistantModelCatalog(),
    val modelFallbackAvailable: Boolean = false,
    val draftText: String = "",
    val sending: Boolean = false,
    val awaitingReply: Boolean = false,
    val errorMessage: String? = null,
    val turns: List<AssistOverlayTurn> = emptyList(),
) {
    val expanded: Boolean get() = !deviceLocked && (turns.isNotEmpty() || sending)
    val canSend: Boolean
        get() = !deviceLocked &&
            !sending &&
            !attachingScreen &&
            hubStatus == AssistHubStatus.Ready &&
            (draftText.isNotBlank() || attachedScreenshot != null)
}

internal data class SentOverlayTurn(
    val clientMessageId: String,
    val displayText: String,
    val hasScreenAttachment: Boolean,
)

/**
 * Pure state machine and coordinator for Persona's floating Digital Assistant overlay.
 *
 * Privacy, routing, and lifecycle guarantees:
 * - While the device is locked (`deviceLocked == true`), incoming screenshots and assist context
 *   are discarded immediately and sending to the Hub is blocked until unlocked.
 * - Incoming `onHandleScreenshot` bitmaps and filtered `onHandleAssist` context are staged ONLY
 *   in memory until the user explicitly taps "Attach current screen".
 * - Disk writes (`cacheDir/assist/`) happen strictly upon manual user confirmation, and are deleted
 *   immediately upon removal (`×`), successful upload/send, or overlay dismissal.
 * - By default every invocation targets the Hub's main conversation without creating side chats.
 *   Users can optionally switch to another non-archived conversation or stage a new standalone
 *   conversation so each conversation runs on its own independent `model_pick` without overwriting
 *   the main conversation's model or history.
 */
internal class AssistOverlayController(
    private val mainScope: CoroutineScope,
    private val prepareScreenshot: suspend (Bitmap) -> Bitmap?,
    private val saveScreenshot: suspend (Bitmap, String) -> ComposerAttachment?,
    private val deleteScreenshot: suspend (ComposerAttachment) -> Unit,
    private val prepareMainModel: suspend (pick: ModelPick?, clearIfDefault: Boolean) -> SessionKey? = { _, _ -> null },
    private val prepareConversationModel: suspend (targetSession: SessionKey?, pick: ModelPick?, clearIfDefault: Boolean) -> SessionKey? =
        { target, pick, clearIfDefault ->
            if (target == null) prepareMainModel(pick, clearIfDefault) else target
        },
    private val createNewConversation: suspend (pick: ModelPick?) -> SessionKey = {
        throw UnsupportedOperationException()
    },
    private val onSelectObservedSession: (SessionKey?) -> Unit = {},
    private val sendTextToMain: suspend (SessionKey?, String, String) -> Unit,
    private val sendWithScreenToMain: suspend (SessionKey?, String, ComposerAttachment, ChatLinkContext?, String) -> Unit,
    private val loadModelCatalog: suspend (Boolean) -> DigitalAssistantModelCatalog = {
        DigitalAssistantModelCatalog(loading = false, groups = emptyList(), hubAvailable = false)
    },
    private val onPersistDefaultModel: () -> Unit = {},
    private val onPersistModelChoice: (DigitalAssistantModelChoice) -> Unit = {},
    private val publishDraftForFullApp: (String, ComposerAttachment?, ChatLinkContext?, String, SessionKey?) -> Unit,
    private val onWakeHub: () -> Unit,
    private val onLaunchFullApp: (SessionKey?) -> Unit,
    private val onDismiss: () -> Unit,
    private val screenOnlyDisplayLabel: () -> String,
    private val defaultSendError: () -> String,
    private val defaultModelStartError: () -> String = defaultSendError,
    private val initialModelChoice: DigitalAssistantModelChoice = DigitalAssistantModelChoice(),
    private val screenshotWaitTimeoutMillis: Long = SCREENSHOT_WAIT_TIMEOUT_MILLIS,
    private val noFlagScreenshotGraceMillis: Long = NO_FLAG_SCREENSHOT_GRACE_MILLIS,
    private val nextId: () -> String = { UUID.randomUUID().toString() },
    private val log: (String) -> Unit = { message -> Log.i(ASSIST_LOG_TAG, message) },
) {
    private val mutableState = MutableStateFlow(AssistOverlayUiState(modelChoice = initialModelChoice))
    val state: StateFlow<AssistOverlayUiState> = mutableState.asStateFlow()

    private var invocationOpen = false
    private var invocationId = ""
    private var deviceLockedForInvocation = false
    private var captureDiscardedForInvocation = false
    private var stagedBitmap: Bitmap? = null
    private var stagedContext: ChatLinkContext? = null
    private var stagedContextFocused = false
    private var handedOffAttachment: ComposerAttachment? = null

    private var hubSnapshot = AssistHubSnapshot()
    private var resolvedMainSession: SessionKey? = null
    private var baselineItemIds: Set<String> = emptySet()
    private var baselineCaptured = false
    private val sentTurns = mutableListOf<SentOverlayTurn>()
    private var persistedModelChoice: DigitalAssistantModelChoice = initialModelChoice
    private var clearMainModelOnNextSend = false
    private var userModifiedTargetModel = false
    private var clearTargetModelOnNextSend = false
    private var cachedCatalog: DigitalAssistantModelCatalog? = null

    private var screenshotWaitJob: Job? = null
    private var stageJob: Job? = null
    private var attachJob: Job? = null
    private var sendJob: Job? = null
    private var catalogJob: Job? = null

    /** Starts a fresh overlay session; idempotent within an active invocation (e.g. `onPrepareShow` -> `onShow`). */
    fun beginSession(
        supportsScreenshot: Boolean,
        showFlags: Int = 0,
        deviceLocked: Boolean = false,
        screenshotDisabledBySettings: Boolean = false,
        expectScreenshotFromFlags: Boolean = true,
    ) {
        if (invocationOpen) {
            if (deviceLocked && !deviceLockedForInvocation) {
                lockSession()
            } else if (!deviceLockedForInvocation && screenshotDisabledBySettings) {
                val status = mutableState.value.captureStatus
                val hasLiveScreenshot = stagedBitmap?.let { !it.isRecycled } == true ||
                    status == AssistCaptureStatus.Available ||
                    status == AssistCaptureStatus.Attached
                if (!hasLiveScreenshot) {
                    screenshotWaitJob?.cancel()
                    screenshotWaitJob = null
                    if (status != AssistCaptureStatus.DisabledBySettings) {
                        log("screenshotDisabledBySettings correctedFrom=$status status=DisabledBySettings")
                        mutableState.update { it.copy(captureStatus = AssistCaptureStatus.DisabledBySettings) }
                    }
                }
            }
            return
        }
        cleanupJobsAndResources(deleteAttached = true)
        invocationOpen = true
        invocationId = nextId()
        deviceLockedForInvocation = deviceLocked
        val allowCapture = supportsScreenshot && !deviceLocked
        captureDiscardedForInvocation = !allowCapture
        handedOffAttachment = null
        resolvedMainSession = hubSnapshot.mainSession
        baselineItemIds = emptySet()
        baselineCaptured = false
        sentTurns.clear()
        userModifiedTargetModel = false
        clearTargetModelOnNextSend = false
        onSelectObservedSession(null)
        val initialCaptureStatus = when {
            !allowCapture -> AssistCaptureStatus.NotSupported
            screenshotDisabledBySettings -> AssistCaptureStatus.DisabledBySettings
            else -> AssistCaptureStatus.Waiting
        }
        log(
            "beginSession supportsScreenshot=$supportsScreenshot showFlags=$showFlags " +
                "deviceLocked=$deviceLocked disabledBySettings=$screenshotDisabledBySettings " +
                "expectFromFlags=$expectScreenshotFromFlags initialCapture=$initialCaptureStatus",
        )
        if (!deviceLocked) {
            onWakeHub()
        }
        mutableState.value = AssistOverlayUiState(
            invocationId = invocationId,
            deviceLocked = deviceLocked,
            captureStatus = initialCaptureStatus,
            hubStatus = hubSnapshot.status,
            mainSession = hubSnapshot.mainSession,
            targetConversation = AssistTargetConversation.Main,
            availableConversations = hubSnapshot.conversations,
            modelChoice = persistedModelChoice,
            modelCatalog = cachedCatalog ?: DigitalAssistantModelCatalog(),
        )
        val effectiveWaitMillis = if (expectScreenshotFromFlags) {
            screenshotWaitTimeoutMillis
        } else {
            minOf(screenshotWaitTimeoutMillis, noFlagScreenshotGraceMillis)
        }
        if (initialCaptureStatus == AssistCaptureStatus.Waiting && effectiveWaitMillis > 0L) {
            val activeId = invocationId
            screenshotWaitJob = mainScope.launch {
                delay(effectiveWaitMillis)
                if (invocationOpen && invocationId == activeId &&
                    mutableState.value.captureStatus == AssistCaptureStatus.Waiting
                ) {
                    log("screenshotWaitTimeout status=TimedOut waitMs=$effectiveWaitMillis expectFromFlags=$expectScreenshotFromFlags")
                    mutableState.update { it.copy(captureStatus = AssistCaptureStatus.TimedOut) }
                }
            }
        }
    }

    /** Transitions an already-open locked session into unlocked state (e.g., after keyguard dismissal). */
    fun onDeviceUnlocked() {
        if (!invocationOpen || !deviceLockedForInvocation) return
        deviceLockedForInvocation = false
        onWakeHub()
        mutableState.update {
            it.copy(
                deviceLocked = false,
                hubStatus = hubSnapshot.status,
                mainSession = hubSnapshot.mainSession,
            )
        }
    }

    /** Keeps the overlay model pill synced live with Hub-scoped preferences when targeting Main chat. */
    fun onModelChoiceUpdated(choice: DigitalAssistantModelChoice) {
        if (choice.isDefault && !persistedModelChoice.isDefault) {
            clearMainModelOnNextSend = true
        } else if (!choice.isDefault) {
            clearMainModelOnNextSend = false
        }
        persistedModelChoice = choice
        mutableState.update { current ->
            if (current.targetConversation is AssistTargetConversation.Main) {
                current.copy(modelChoice = choice)
            } else {
                current
            }
        }
    }

    /** Updates live Hub connection, conversation list, target metadata, and streaming transcript snapshot. */
    fun onHubUpdate(snapshot: AssistHubSnapshot) {
        hubSnapshot = snapshot
        if (snapshot.mainSession != null) {
            resolvedMainSession = snapshot.mainSession
        }
        val currentState = mutableState.value
        val updatedTarget = when (val target = currentState.targetConversation) {
            AssistTargetConversation.Main -> target
            AssistTargetConversation.NewChat -> target
            is AssistTargetConversation.Existing -> {
                val matchingOption = snapshot.conversations.firstOrNull { it.key == target.key }
                val updatedTitle = matchingOption?.title?.takeIf(String::isNotBlank)
                    ?: snapshot.activeTitle?.takeIf { snapshot.activeSession == target.key && it.isNotBlank() }
                    ?: target.title
                val updatedPick = when {
                    snapshot.activeSession == target.key &&
                        (snapshot.activeModelPick != null || matchingOption != null || snapshot.activeTitle != null) ->
                        snapshot.activeModelPick
                    matchingOption != null -> matchingOption.modelPick
                    else -> target.modelPick
                }
                target.copy(title = updatedTitle, modelPick = updatedPick)
            }
        }
        if (sentTurns.isNotEmpty() && !baselineCaptured && snapshotMatchesTarget(snapshot, updatedTarget)) {
            val items = snapshot.chatSnapshot?.items
            if (items != null) {
                val sentIds = sentTurns.mapTo(HashSet()) { it.clientMessageId }
                val firstSentIndex = items.indexOfFirst { it.clientMsgId != null && it.clientMsgId in sentIds }
                baselineItemIds = if (firstSentIndex >= 0) {
                    items.take(firstSentIndex).mapTo(HashSet()) { it.id }
                } else {
                    items.mapTo(HashSet()) { it.id }
                }
                baselineCaptured = true
            }
        }
        val projection = projectForTarget(snapshot, updatedTarget)
        val updatedConversations = mergeAvailableConversations(
            remote = snapshot.conversations,
            target = updatedTarget,
            activeSession = snapshot.activeSession,
            activeWorking = snapshot.activeWorking,
        )
        mutableState.update { current ->
            val syncedModelChoice = if (
                updatedTarget is AssistTargetConversation.Existing &&
                !userModifiedTargetModel &&
                !current.sending
            ) {
                resolveModelChoiceForPick(updatedTarget.modelPick, cachedCatalog ?: current.modelCatalog)
            } else {
                current.modelChoice
            }
            current.copy(
                hubStatus = snapshot.status,
                mainSession = snapshot.mainSession ?: resolvedMainSession,
                targetConversation = updatedTarget,
                availableConversations = updatedConversations,
                modelChoice = syncedModelChoice,
                turns = projection.turns,
                awaitingReply = projection.awaitingReply,
            )
        }
    }

    /** Handles `onHandleScreenshot(Bitmap?)`: stages safely in memory ONLY; never writes to disk or attaches. */
    fun onScreenshotReceived(
        bitmap: Bitmap?,
        screenshotDisabledBySettings: Boolean = false,
    ) {
        if (!invocationOpen) {
            beginSession(
                supportsScreenshot = true,
                screenshotDisabledBySettings = screenshotDisabledBySettings,
            )
        }
        if (deviceLockedForInvocation) {
            log("onHandleScreenshot dropped=deviceLocked")
            return
        }
        val activeId = invocationId
        val valid = bitmap != null && !bitmap.isRecycled && bitmap.width > 0 && bitmap.height > 0
        log(
            "onHandleScreenshot received=${bitmap != null} valid=$valid " +
                "disabledBySettings=$screenshotDisabledBySettings " +
                "size=${bitmap?.width ?: 0}x${bitmap?.height ?: 0} config=${bitmap?.config?.name ?: "none"}",
        )
        screenshotWaitJob?.cancel()
        screenshotWaitJob = null
        if (captureDiscardedForInvocation) return
        if (!valid) {
            val status = mutableState.value.captureStatus
            val hasLiveScreenshot = stagedBitmap?.let { !it.isRecycled } == true ||
                status == AssistCaptureStatus.Available ||
                status == AssistCaptureStatus.Attached
            if (!hasLiveScreenshot) {
                val targetStatus = if (screenshotDisabledBySettings || status == AssistCaptureStatus.DisabledBySettings) {
                    AssistCaptureStatus.DisabledBySettings
                } else {
                    AssistCaptureStatus.Unavailable
                }
                mutableState.update { it.copy(captureStatus = targetStatus) }
            }
            return
        }
        val incoming = requireNotNull(bitmap)
        stageJob?.cancel()
        stageJob = mainScope.launch {
            val prepared = try {
                prepareScreenshot(incoming)
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                null
            }
            if (!invocationOpen || invocationId != activeId || captureDiscardedForInvocation || deviceLockedForInvocation) {
                prepared?.safeRecycle()
                return@launch
            }
            if (prepared == null) {
                log("screenshotPrepareFailed status=Unavailable")
                if (mutableState.value.captureStatus != AssistCaptureStatus.Attached) {
                    mutableState.update { it.copy(captureStatus = AssistCaptureStatus.Unavailable) }
                }
                return@launch
            }
            val oldStaged = stagedBitmap
            stagedBitmap = prepared
            log("screenshotStagedInMemory size=${prepared.width}x${prepared.height}")
            mutableState.update { current ->
                current.copy(
                    captureStatus = if (current.captureStatus == AssistCaptureStatus.Attached) {
                        AssistCaptureStatus.Attached
                    } else {
                        AssistCaptureStatus.Available
                    },
                )
            }
            oldStaged?.safeRecycle()
        }
    }

    /** Handles filtered `onHandleAssist` context: staged in memory ONLY until user confirms attaching the screen. */
    fun onAssistContextReceived(context: ChatLinkContext?, isFocused: Boolean) {
        if (deviceLockedForInvocation) {
            log("onHandleAssist dropped=deviceLocked")
            return
        }
        log("onHandleAssist safeContext=${context != null} focused=$isFocused")
        if (context == null) return
        if (!invocationOpen) {
            beginSession(supportsScreenshot = true)
        }
        if (captureDiscardedForInvocation) return
        if (stagedContext == null || isFocused || !stagedContextFocused) {
            stagedContext = context
            stagedContextFocused = isFocused
        }
        mutableState.update { current ->
            current.copy(
                hasStagedContext = stagedContext != null,
                attachedContext = if (current.captureStatus == AssistCaptureStatus.Attached) {
                    current.attachedContext ?: stagedContext
                } else {
                    current.attachedContext
                },
            )
        }
    }

    /** Toggles the lightweight inline model picker inside the overlay sheet. */
    fun toggleModelPicker() {
        val current = mutableState.value
        if (!invocationOpen || current.deviceLocked || current.sending) return
        val opening = !current.modelPickerOpen
        mutableState.update { it.copy(modelPickerOpen = opening) }
        if (opening) {
            val existing = cachedCatalog
            val shouldRefresh = existing == null || existing.error != null || !existing.hubAvailable || existing.groups.isEmpty()
            if (shouldRefresh) {
                fetchModelCatalog(refresh = existing?.error != null)
            }
        }
    }

    fun closeModelPicker() {
        mutableState.update { it.copy(modelPickerOpen = false) }
    }

    fun refreshModelCatalog() {
        if (!invocationOpen || deviceLockedForInvocation) return
        fetchModelCatalog(refresh = true)
    }

    /** Routes the overlay back to the Hub's main conversation and restores its persisted model choice. */
    fun selectMainConversation() {
        val current = mutableState.value
        if (!invocationOpen || current.deviceLocked || current.sending) return
        if (current.targetConversation is AssistTargetConversation.Main) {
            mutableState.update { it.copy(errorMessage = null, modelFallbackAvailable = false) }
            return
        }
        resetProjectionForConversationSwitch()
        onSelectObservedSession(null)
        mutableState.update {
            it.copy(
                targetConversation = AssistTargetConversation.Main,
                availableConversations = hubSnapshot.conversations,
                modelChoice = persistedModelChoice,
                turns = emptyList(),
                awaitingReply = false,
                modelFallbackAvailable = false,
                errorMessage = null,
            )
        }
    }

    /** Routes the overlay to an existing non-main conversation with its own independent model and stream. */
    fun selectExistingConversation(option: AssistConversationOption) {
        val current = mutableState.value
        if (!invocationOpen || current.deviceLocked || current.sending) return
        val existingTarget = current.targetConversation as? AssistTargetConversation.Existing
        if (existingTarget?.key == option.key) {
            mutableState.update { it.copy(errorMessage = null, modelFallbackAvailable = false) }
            return
        }
        val nextTarget = AssistTargetConversation.Existing(
            key = option.key,
            title = option.title,
            modelPick = option.modelPick,
        )
        resetProjectionForConversationSwitch()
        onSelectObservedSession(option.key)
        val targetModel = resolveModelChoiceForPick(option.modelPick, cachedCatalog ?: current.modelCatalog)
        mutableState.update {
            it.copy(
                targetConversation = nextTarget,
                availableConversations = mergeAvailableConversations(hubSnapshot.conversations, nextTarget),
                modelChoice = targetModel,
                turns = emptyList(),
                awaitingReply = false,
                modelFallbackAvailable = false,
                errorMessage = null,
            )
        }
    }

    /** Stages a new standalone conversation (created lazily on next send without touching Main chat). */
    fun selectNewConversation() {
        val current = mutableState.value
        if (!invocationOpen || current.deviceLocked || current.sending) return
        if (current.targetConversation is AssistTargetConversation.NewChat) {
            mutableState.update { it.copy(errorMessage = null, modelFallbackAvailable = false) }
            return
        }
        resetProjectionForConversationSwitch()
        onSelectObservedSession(null)
        mutableState.update {
            it.copy(
                targetConversation = AssistTargetConversation.NewChat,
                availableConversations = hubSnapshot.conversations,
                modelChoice = DigitalAssistantModelChoice(),
                turns = emptyList(),
                awaitingReply = false,
                modelFallbackAvailable = false,
                errorMessage = null,
            )
        }
    }

    fun selectDefaultModel() {
        val target = mutableState.value.targetConversation
        if (target is AssistTargetConversation.Main) {
            persistedModelChoice = DigitalAssistantModelChoice()
            clearMainModelOnNextSend = true
            onPersistDefaultModel()
            mutableState.update {
                it.copy(
                    modelChoice = persistedModelChoice,
                    modelPickerOpen = false,
                    modelFallbackAvailable = false,
                    errorMessage = null,
                )
            }
            return
        }
        userModifiedTargetModel = true
        clearTargetModelOnNextSend = true
        mutableState.update {
            it.copy(
                modelChoice = DigitalAssistantModelChoice(),
                modelPickerOpen = false,
                modelFallbackAvailable = false,
                errorMessage = null,
            )
        }
    }

    fun selectModel(choice: DigitalAssistantModelChoice) {
        if (choice.pick == null) {
            selectDefaultModel()
            return
        }
        val target = mutableState.value.targetConversation
        if (target is AssistTargetConversation.Main) {
            persistedModelChoice = choice
            clearMainModelOnNextSend = false
            onPersistModelChoice(choice)
            mutableState.update {
                it.copy(
                    modelChoice = choice,
                    modelPickerOpen = false,
                    modelFallbackAvailable = false,
                    errorMessage = null,
                )
            }
            return
        }
        userModifiedTargetModel = true
        clearTargetModelOnNextSend = false
        mutableState.update {
            it.copy(
                modelChoice = choice,
                modelPickerOpen = false,
                modelFallbackAvailable = false,
                errorMessage = null,
            )
        }
    }

    /** Manual user confirmation: persists the staged screen to private `cacheDir/assist/` and previews it. */
    fun attachCurrentScreen() {
        val current = mutableState.value
        val source = stagedBitmap
        if (!invocationOpen || current.deviceLocked || current.attachingScreen || current.sending ||
            current.captureStatus != AssistCaptureStatus.Available || source == null || source.isRecycled
        ) {
            return
        }
        val activeId = invocationId
        mutableState.update { it.copy(attachingScreen = true, errorMessage = null) }
        attachJob?.cancel()
        attachJob = mainScope.launch {
            val saved = try {
                saveScreenshot(source, activeId)
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                null
            }
            if (!invocationOpen || invocationId != activeId || captureDiscardedForInvocation || deviceLockedForInvocation) {
                if (saved != null) { val _ = runCatching { deleteScreenshot(saved) } }
                return@launch
            }
            if (saved == null) {
                log("attachCurrentScreenFailed")
                mutableState.update {
                    it.copy(
                        attachingScreen = false,
                        errorMessage = defaultSendError(),
                    )
                }
                return@launch
            }
            log("attachCurrentScreenConfirmed bytes=${saved.size ?: 0L} withContext=${stagedContext != null}")
            mutableState.update {
                it.copy(
                    captureStatus = AssistCaptureStatus.Attached,
                    attachedScreenshot = saved,
                    attachedContext = stagedContext,
                    attachingScreen = false,
                    errorMessage = null,
                )
            }
        }
    }

    /** Removes the manually attached screen (and paired context) and deletes its temporary cache file immediately. */
    fun removeAttachedScreen() {
        val current = mutableState.value
        if (current.sending) return
        attachJob?.cancel()
        attachJob = null
        val previous = current.attachedScreenshot
        val canReattach = !deviceLockedForInvocation && !captureDiscardedForInvocation &&
            stagedBitmap?.let { !it.isRecycled } == true
        mutableState.update {
            it.copy(
                captureStatus = if (canReattach) AssistCaptureStatus.Available else AssistCaptureStatus.NotSupported,
                attachedScreenshot = null,
                attachedContext = null,
                attachingScreen = false,
                errorMessage = null,
            )
        }
        if (previous != null) {
            mainScope.launch { val _ = runCatching { deleteScreenshot(previous) } }
        }
    }

    fun updateDraftText(value: String) {
        mutableState.update { it.copy(draftText = value, errorMessage = null, modelFallbackAvailable = false) }
    }

    fun appendDictation(heard: String) {
        val trimmed = heard.trim()
        if (trimmed.isEmpty() || deviceLockedForInvocation) return
        mutableState.update { current ->
            val updated = persona.feature.chat.withDictation(current.draftText, trimmed)
            current.copy(draftText = updated, errorMessage = null, modelFallbackAvailable = false)
        }
    }

    fun showNotice(message: String) {
        mutableState.update { it.copy(errorMessage = message) }
    }

    /** Sends the message to the selected conversation (main, existing, or newly created standalone chat). */
    fun send() = sendInternal(forceDefaultModel = false)

    /** One-tap fallback after a custom model fails: clears/skips the custom pick and sends on default model. */
    fun retryWithDefaultMain() = sendInternal(forceDefaultModel = true)

    private fun sendInternal(forceDefaultModel: Boolean) {
        val current = mutableState.value
        if (!invocationOpen || !current.canSend) return
        val prompt = current.draftText.trim()
        val screenshot = current.attachedScreenshot
        val contextCard = current.attachedContext
        val clientId = nextId()
        val activeId = invocationId
        val targetAtSend = current.targetConversation
        val selectedPick = if (forceDefaultModel) null else current.modelChoice.pick

        mutableState.update {
            it.copy(
                sending = true,
                modelPickerOpen = false,
                modelFallbackAvailable = false,
                errorMessage = null,
            )
        }
        sendJob = mainScope.launch {
            var modelApplied = false
            var effectiveTarget = targetAtSend
            try {
                val targetKey: SessionKey? = when (targetAtSend) {
                    AssistTargetConversation.NewChat -> {
                        val createdKey = createNewConversation(selectedPick)
                        if (!invocationOpen || invocationId != activeId) return@launch
                        modelApplied = true
                        userModifiedTargetModel = false
                        clearTargetModelOnNextSend = false
                        baselineItemIds = emptySet()
                        baselineCaptured = true
                        sentTurns.clear()
                        val boundTarget = AssistTargetConversation.Existing(
                            key = createdKey,
                            title = "",
                            modelPick = selectedPick,
                        )
                        effectiveTarget = boundTarget
                        onSelectObservedSession(createdKey)
                        val boundModelChoice = if (forceDefaultModel) {
                            DigitalAssistantModelChoice()
                        } else {
                            mutableState.value.modelChoice
                        }
                        mutableState.update {
                            it.copy(
                                targetConversation = boundTarget,
                                availableConversations = mergeAvailableConversations(hubSnapshot.conversations, boundTarget),
                                modelChoice = boundModelChoice,
                            )
                        }
                        createdKey
                    }

                    is AssistTargetConversation.Existing -> {
                        if (sentTurns.isEmpty()) {
                            if (snapshotMatchesTarget(hubSnapshot, targetAtSend) && hubSnapshot.chatSnapshot != null) {
                                baselineItemIds = hubSnapshot.chatSnapshot?.items.orEmpty().mapTo(HashSet()) { it.id }
                                baselineCaptured = true
                            } else {
                                baselineItemIds = emptySet()
                                baselineCaptured = false
                            }
                        }
                        val remotePick = if (hubSnapshot.activeSession == targetAtSend.key) {
                            hubSnapshot.activeModelPick
                        } else {
                            targetAtSend.modelPick
                        }
                        val shouldClearDefault = (forceDefaultModel || clearTargetModelOnNextSend) && remotePick != null
                        val shouldApplyPick = selectedPick != null &&
                            (userModifiedTargetModel || selectedPick != remotePick)
                        if (shouldApplyPick || shouldClearDefault) {
                            val _ = prepareConversationModel(targetAtSend.key, selectedPick, shouldClearDefault)
                            if (!invocationOpen || invocationId != activeId) return@launch
                            modelApplied = true
                            userModifiedTargetModel = false
                            clearTargetModelOnNextSend = false
                            effectiveTarget = targetAtSend.copy(modelPick = selectedPick)
                        }
                        targetAtSend.key
                    }

                    AssistTargetConversation.Main -> {
                        if (sentTurns.isEmpty()) {
                            if (snapshotMatchesTarget(hubSnapshot, targetAtSend)) {
                                baselineItemIds = hubSnapshot.chatSnapshot?.items.orEmpty().mapTo(HashSet()) { it.id }
                                baselineCaptured = true
                            } else {
                                baselineItemIds = emptySet()
                                baselineCaptured = false
                            }
                        }
                        val shouldClearDefault = forceDefaultModel ||
                            clearMainModelOnNextSend ||
                            (selectedPick == null && hubSnapshot.mainModelPick != null)
                        if (selectedPick != null || shouldClearDefault) {
                            val prepared = prepareConversationModel(null, selectedPick, shouldClearDefault)
                            if (!invocationOpen || invocationId != activeId) return@launch
                            modelApplied = true
                            clearMainModelOnNextSend = false
                            if (prepared != null) {
                                resolvedMainSession = prepared
                            }
                            prepared ?: hubSnapshot.mainSession ?: resolvedMainSession
                        } else {
                            hubSnapshot.mainSession ?: resolvedMainSession
                        }
                    }
                }

                if (screenshot != null) {
                    sendWithScreenToMain(targetKey, prompt, screenshot, contextCard, clientId)
                } else {
                    sendTextToMain(targetKey, prompt, clientId)
                }

                if (!invocationOpen || invocationId != activeId) {
                    if (screenshot != null) { val _ = runCatching { deleteScreenshot(screenshot) } }
                    return@launch
                }
                sentTurns += SentOverlayTurn(
                    clientMessageId = clientId,
                    displayText = prompt.ifBlank { screenOnlyDisplayLabel() },
                    hasScreenAttachment = screenshot != null,
                )
                if (screenshot != null) {
                    val _ = runCatching { deleteScreenshot(screenshot) }
                }
                // Release staged capture immediately after any successful turn (text-only or with screen).
                releaseStagedCaptureOnly()
                val projection = projectForTarget(hubSnapshot, effectiveTarget)
                mutableState.update {
                    it.copy(
                        draftText = "",
                        sending = false,
                        mainSession = hubSnapshot.mainSession ?: resolvedMainSession,
                        targetConversation = effectiveTarget,
                        availableConversations = mergeAvailableConversations(hubSnapshot.conversations, effectiveTarget),
                        modelFallbackAvailable = false,
                        errorMessage = null,
                        turns = projection.turns,
                        awaitingReply = projection.awaitingReply,
                    )
                }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                if (!invocationOpen || invocationId != activeId) return@launch
                val failedCustomModel = selectedPick != null && !modelApplied
                val fallbackMessage = if (failedCustomModel) defaultModelStartError() else defaultSendError()
                mutableState.update {
                    it.copy(
                        sending = false,
                        modelFallbackAvailable = failedCustomModel,
                        errorMessage = error.message?.takeIf(String::isNotBlank) ?: fallbackMessage,
                    )
                }
            }
        }
    }

    /**
     * Opens Persona's full `MainActivity` directly at the active target conversation (main, selected
     * existing conversation, or newly created conversation). Preserves any unsent text or
     * manually-attached screen (never unconfirmed screenshots, never while device is locked), then
     * dismisses the overlay.
     */
    fun openInPersona() {
        val current = mutableState.value
        val targetSession = when (val target = current.targetConversation) {
            AssistTargetConversation.Main -> current.mainSession ?: resolvedMainSession
            is AssistTargetConversation.Existing -> target.key
            AssistTargetConversation.NewChat -> null
        }
        if (!current.deviceLocked) {
            val unsentText = current.draftText.trim()
            val confirmedShot = current.attachedScreenshot
            val confirmedContext = if (confirmedShot != null) current.attachedContext else null
            if (unsentText.isNotEmpty() || confirmedShot != null) {
                handedOffAttachment = confirmedShot
                publishDraftForFullApp(
                    invocationId.ifEmpty { nextId() },
                    confirmedShot,
                    confirmedContext,
                    unsentText,
                    targetSession,
                )
            }
        }
        val oldStaged = stagedBitmap
        stagedBitmap = null
        stagedContext = null
        stagedContextFocused = false
        oldStaged?.safeRecycle()
        onLaunchFullApp(if (current.deviceLocked) null else targetSession)
        onDismiss()
    }

    /** Ends the current overlay session (on hide/dismiss/destroy) and releases all bitmaps & un-sent files. */
    fun endSession() {
        if (!invocationOpen && stagedBitmap == null && mutableState.value.attachedScreenshot == null) {
            return
        }
        invocationOpen = false
        deviceLockedForInvocation = false
        resolvedMainSession = null
        baselineItemIds = emptySet()
        baselineCaptured = false
        sentTurns.clear()
        userModifiedTargetModel = false
        clearTargetModelOnNextSend = false
        onSelectObservedSession(null)
        val previousState = mutableState.value
        mutableState.value = AssistOverlayUiState(
            captureStatus = AssistCaptureStatus.NotSupported,
            hubStatus = hubSnapshot.status,
            mainSession = hubSnapshot.mainSession,
            targetConversation = AssistTargetConversation.Main,
            availableConversations = hubSnapshot.conversations,
            modelChoice = persistedModelChoice,
            modelCatalog = cachedCatalog ?: DigitalAssistantModelCatalog(),
        )
        cleanupJobsAndResources(deleteAttached = true, attachedToDelete = previousState.attachedScreenshot)
        handedOffAttachment = null
    }

    private fun lockSession() {
        deviceLockedForInvocation = true
        releaseStagedCaptureOnly()
        mutableState.update {
            it.copy(
                deviceLocked = true,
                modelPickerOpen = false,
                modelFallbackAvailable = false,
                errorMessage = null,
                turns = emptyList(),
                awaitingReply = false,
            )
        }
    }

    private fun fetchModelCatalog(refresh: Boolean) {
        val activeId = invocationId
        catalogJob?.cancel()
        mutableState.update {
            it.copy(modelCatalog = it.modelCatalog.copy(loading = true, error = null))
        }
        catalogJob = mainScope.launch {
            val loaded = try {
                loadModelCatalog(refresh)
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                DigitalAssistantModelCatalog(
                    loading = false,
                    groups = emptyList(),
                    error = error.message?.takeIf(String::isNotBlank) ?: defaultSendError(),
                    hubAvailable = true,
                )
            }
            if (!invocationOpen || invocationId != activeId) return@launch
            cachedCatalog = loaded
            mutableState.update { current ->
                val target = current.targetConversation
                val upgradedChoice = if (
                    target is AssistTargetConversation.Existing &&
                    !userModifiedTargetModel &&
                    target.modelPick != null
                ) {
                    resolveModelChoiceForPick(target.modelPick, loaded)
                } else {
                    current.modelChoice
                }
                current.copy(modelCatalog = loaded, modelChoice = upgradedChoice)
            }
        }
    }

    private fun resetProjectionForConversationSwitch() {
        sentTurns.clear()
        baselineItemIds = emptySet()
        baselineCaptured = false
        userModifiedTargetModel = false
        clearTargetModelOnNextSend = false
    }

    private fun snapshotMatchesTarget(
        snapshot: AssistHubSnapshot,
        target: AssistTargetConversation,
    ): Boolean = when (target) {
        AssistTargetConversation.Main -> {
            val expectedMain = snapshot.mainSession ?: resolvedMainSession
            snapshot.activeSession == null || expectedMain == null || snapshot.activeSession == expectedMain
        }
        is AssistTargetConversation.Existing -> snapshot.activeSession == target.key
        AssistTargetConversation.NewChat -> false
    }

    private fun projectForTarget(
        snapshot: AssistHubSnapshot,
        target: AssistTargetConversation,
    ): OverlayTurnsProjection {
        if (deviceLockedForInvocation || sentTurns.isEmpty()) {
            return OverlayTurnsProjection(emptyList(), awaitingReply = false)
        }
        val safeSnapshot = if (snapshotMatchesTarget(snapshot, target)) {
            snapshot
        } else {
            snapshot.copy(chatSnapshot = null, activeWorking = false, mainWorking = false)
        }
        return projectOverlayTurns(safeSnapshot, sentTurns, baselineItemIds, baselineCaptured = baselineCaptured)
    }

    private fun mergeAvailableConversations(
        remote: List<AssistConversationOption>,
        target: AssistTargetConversation,
        activeSession: SessionKey? = null,
        activeWorking: Boolean = false,
    ): List<AssistConversationOption> {
        val existing = target as? AssistTargetConversation.Existing ?: return remote
        if (remote.any { it.key == existing.key }) return remote
        return listOf(
            AssistConversationOption(
                key = existing.key,
                title = existing.title,
                modelPick = existing.modelPick,
                working = activeSession == existing.key && activeWorking,
            ),
        ) + remote
    }

    private fun releaseStagedCaptureOnly() {
        captureDiscardedForInvocation = true
        screenshotWaitJob?.cancel()
        screenshotWaitJob = null
        stageJob?.cancel()
        stageJob = null
        attachJob?.cancel()
        attachJob = null
        val oldStaged = stagedBitmap
        stagedBitmap = null
        stagedContext = null
        stagedContextFocused = false
        mutableState.update {
            it.copy(
                captureStatus = AssistCaptureStatus.NotSupported,
                attachedScreenshot = null,
                attachedContext = null,
                hasStagedContext = false,
                attachingScreen = false,
            )
        }
        oldStaged?.safeRecycle()
    }

    private fun cleanupJobsAndResources(
        deleteAttached: Boolean,
        attachedToDelete: ComposerAttachment? = mutableState.value.attachedScreenshot,
    ) {
        screenshotWaitJob?.cancel()
        screenshotWaitJob = null
        stageJob?.cancel()
        stageJob = null
        attachJob?.cancel()
        attachJob = null
        sendJob?.cancel()
        sendJob = null
        catalogJob?.cancel()
        catalogJob = null

        val oldStaged = stagedBitmap
        stagedBitmap = null
        stagedContext = null
        stagedContextFocused = false

        if (deleteAttached && attachedToDelete != null && attachedToDelete !== handedOffAttachment) {
            mainScope.launch { val _ = runCatching { deleteScreenshot(attachedToDelete) } }
        }
        oldStaged?.safeRecycle()
    }

    companion object {
        const val SCREENSHOT_WAIT_TIMEOUT_MILLIS = 4_000L
        const val NO_FLAG_SCREENSHOT_GRACE_MILLIS = 1_200L
    }
}

internal data class OverlayTurnsProjection(
    val turns: List<AssistOverlayTurn>,
    val awaitingReply: Boolean,
)

/**
 * Projects only the conversation turns belonging to the current overlay session from the active
 * conversation's live [AssistHubSnapshot], preserving real-time streaming state and immediate
 * optimistic user bubbles.
 */
internal fun resolveModelChoiceForPick(
    pick: ModelPick?,
    catalog: DigitalAssistantModelCatalog,
): DigitalAssistantModelChoice {
    if (pick == null) return DigitalAssistantModelChoice()
    for (group in catalog.groups) {
        val exact = group.options.firstOrNull { it.pick == pick }
        if (exact != null) return exact.toChoice()
    }
    for (group in catalog.groups) {
        val sameModel = group.options.firstOrNull {
            it.pick.launcher == pick.launcher && it.pick.model == pick.model
        }
        if (sameModel != null) {
            return DigitalAssistantModelChoice(
                pick = pick,
                modelLabel = sameModel.modelLabel,
                launcherLabel = sameModel.launcherLabel,
            )
        }
    }
    val launcherTitle = catalog.groups.firstOrNull { it.launcher == pick.launcher }?.title.orEmpty()
    return DigitalAssistantModelChoice(
        pick = pick,
        modelLabel = pick.model.orEmpty(),
        launcherLabel = launcherTitle.ifBlank { pick.launcher },
    )
}

internal fun projectOverlayTurns(
    hub: AssistHubSnapshot,
    sentTurns: List<SentOverlayTurn>,
    baselineItemIds: Set<String>,
    baselineCaptured: Boolean = true,
): OverlayTurnsProjection {
    if (sentTurns.isEmpty()) return OverlayTurnsProjection(emptyList(), awaitingReply = false)
    val items = hub.chatSnapshot?.items.orEmpty()
    val streamingIds = hub.chatSnapshot?.streamingItemIds.orEmpty()
    val sentById = sentTurns.associateBy { it.clientMessageId }
    val matchedClientIds = mutableSetOf<String>()

    val firstIndex = items.indexOfFirst { item ->
        item.clientMsgId != null && item.clientMsgId in sentById
    }.takeIf { it >= 0 } ?: if (baselineCaptured) {
        items.indexOfFirst { item ->
            item.id !in baselineItemIds && item.role == "user" && item.kind == "text"
        }
    } else {
        -1
    }

    val projected = mutableListOf<AssistOverlayTurn>()
    if (firstIndex >= 0) {
        for (i in firstIndex until items.size) {
            val item = items[i]
            if (item.kind != "text") continue
            when (item.role) {
                "user" -> {
                    val raw = item.text.orEmpty()
                    val stripped = stripOverlayWireTags(raw)
                    val matched = item.clientMsgId?.let(sentById::get)
                        ?: sentTurns.firstOrNull { candidate ->
                            candidate.clientMessageId !in matchedClientIds &&
                                (candidate.displayText == stripped.text ||
                                    (stripped.hasAttachedImage && candidate.hasScreenAttachment && stripped.text.isBlank()))
                        }
                    if (matched != null) matchedClientIds += matched.clientMessageId
                    val display = stripped.text.ifBlank { matched?.displayText.orEmpty() }
                    if (display.isNotBlank() || stripped.hasAttachedImage || matched?.hasScreenAttachment == true) {
                        projected += AssistOverlayTurn(
                            id = item.id,
                            fromUser = true,
                            text = display,
                            hasScreenAttachment = stripped.hasAttachedImage || matched?.hasScreenAttachment == true,
                            delivery = AssistTurnDelivery.Sent,
                        )
                    }
                }

                "assistant" -> {
                    val body = item.text.orEmpty()
                    if (body.isNotBlank()) {
                        projected += AssistOverlayTurn(
                            id = item.id,
                            fromUser = false,
                            text = body,
                            streaming = item.id in streamingIds,
                            delivery = AssistTurnDelivery.Sent,
                        )
                    }
                }
            }
        }
    }

    val outboxById = hub.outbox.associateBy { it.clientMessageId }
    for (sent in sentTurns) {
        if (sent.clientMessageId in matchedClientIds) continue
        val outbox = outboxById[sent.clientMessageId]
        val delivery = when (outbox?.state) {
            DeliveryState.Refused, DeliveryState.Rejected -> AssistTurnDelivery.Failed
            DeliveryState.Queued, DeliveryState.WaitingForNetwork -> AssistTurnDelivery.Queued
            DeliveryState.Sent -> AssistTurnDelivery.Sent
            else -> AssistTurnDelivery.Sending
        }
        projected += AssistOverlayTurn(
            id = "pending:${sent.clientMessageId}",
            fromUser = true,
            text = sent.displayText,
            hasScreenAttachment = sent.hasScreenAttachment,
            delivery = delivery,
        )
    }

    val last = projected.lastOrNull()
    val awaitingReply = last != null &&
        last.fromUser &&
        last.delivery != AssistTurnDelivery.Failed &&
        (hub.activeWorking || last.delivery == AssistTurnDelivery.Sending || last.delivery == AssistTurnDelivery.Sent)

    return OverlayTurnsProjection(projected, awaitingReply)
}

internal data class StrippedWireMessage(
    val text: String,
    val hasAttachedImage: Boolean,
)

private val ATTACHED_IMAGES_REGEX = Regex("<attached_images>[\\s\\S]*?</attached_images>\\s*")

internal fun stripOverlayWireTags(raw: String): StrippedWireMessage {
    val hasImage = ATTACHED_IMAGES_REGEX.containsMatchIn(raw)
    val cleaned = raw
        .replace(ProtocolCompatibility.contextBlock, "")
        .replace(ProtocolCompatibility.fileTag, "\n")
        .replace(ATTACHED_IMAGES_REGEX, "")
        .trim()
    return StrippedWireMessage(text = cleaned, hasAttachedImage = hasImage)
}

private fun Bitmap.safeRecycle() {
    if (!isRecycled) { val _ = runCatching { recycle() } }
}
