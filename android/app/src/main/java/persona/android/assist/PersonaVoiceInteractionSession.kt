package persona.android.assist

import android.app.KeyguardManager
import android.app.assist.AssistContent
import android.app.assist.AssistStructure
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.drawable.ColorDrawable
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.service.voice.VoiceInteractionSession
import android.util.Log
import android.view.View
import android.view.ViewGroup
import android.view.Window
import android.view.WindowManager
import android.view.inputmethod.InputMethodManager
import android.widget.FrameLayout
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.platform.ComposeView
import androidx.compose.ui.platform.ViewCompositionStrategy
import androidx.core.view.WindowCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.withContext
import persona.android.MainActivity
import persona.android.R
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppLanguages
import persona.core.navigation.AskPersonaLink
import persona.core.navigation.NotificationRoute
import persona.feature.chat.AssistDraftBus
import persona.feature.chat.AssistDraftTarget
import persona.feature.chat.ChatLinkContext

/**
 * Snapshot of Android's system digital assistant screen-context switches inspected at `onPrepareShow` / `onShow`.
 * Allows immediate user guidance when screenshots are turned off in system settings instead of waiting for a timeout.
 */
internal data class AssistScreenshotSettingsProbe(
    val disabledShowContext: Int,
    val userDisabledShowContext: Int,
    val secureAssistScreenshotEnabled: Int,
    val secureAssistStructureEnabled: Int,
) {
    val screenshotDisabledBySettings: Boolean
        get() = (userDisabledShowContext and VoiceInteractionSession.SHOW_WITH_SCREENSHOT) != 0 ||
            (disabledShowContext and VoiceInteractionSession.SHOW_WITH_SCREENSHOT) != 0 ||
            secureAssistScreenshotEnabled == 0
}

/**
 * Hosts Persona's Muse/Gemini-style floating quick overlay directly above the foreground app when
 * triggered as Android's Default Digital Assistant (`ROLE_ASSISTANT`).
 *
 * - Keeps the session alive (`setUiEnabled(true)`) so async `onHandleScreenshot` and `onHandleAssist`
 *   callbacks reliably arrive (paired with Android 17's `usesAssistData`, `usesAssistScreenshots`,
 *   and `usesAssistStructureScreenContent` declarations in `voice_interaction_service.xml`).
 * - Gates lockscreen invocations (`KeyguardManager.isKeyguardLocked || isDeviceLocked`) so screen
 *   content is never captured and nothing is sent to the Hub while the device is locked.
 * - Immediately detects system-disabled screenshots via `userDisabledShowContext` / `Settings.Secure`
 *   and offers a one-tap shortcut to system Digital Assistant settings.
 * - Configures `SOFT_INPUT_ADJUST_NOTHING` in `onCreateContentView`, immediately after `super.onShow`,
 *   and via `frame.post` so `VoiceInteractionWindow` never stacks `ADJUST_PAN` over Compose insets.
 */
class PersonaVoiceInteractionSession(context: Context) : VoiceInteractionSession(context) {
    private val uiContext: Context by lazy { AppLanguages.wrap(context) }
    private val sessionScope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val lifecycleOwner = AssistSessionLifecycleOwner(fallbackOnBackPressed = ::dismissOverlay)

    private lateinit var bridge: AssistHubBridge
    private lateinit var controller: AssistOverlayController
    private var rootFrame: FrameLayout? = null
    private var composeView: ComposeView? = null

    override fun onCreate() {
        super.onCreate()
        lifecycleOwner.onCreate()
        bridge = AssistHubBridge(
            context = uiContext,
            scope = sessionScope,
            onSnapshot = { snapshot -> controller.onHubUpdate(snapshot) },
            onModelChoice = { choice -> controller.onModelChoiceUpdated(choice) },
        )
        controller = createOverlayController(
            uiContext = uiContext,
            scope = sessionScope,
            bridge = bridge,
            launchFullApp = { targetSession -> launchMainActivity(targetSession) },
            dismiss = ::dismissOverlay,
        )
    }

    override fun onCreateContentView(): View {
        configureOverlayWindow(window?.window)
        val frame = FrameLayout(uiContext).apply {
            layoutParams = ViewGroup.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            )
        }
        lifecycleOwner.attachTo(window?.window?.decorView, frame)
        val compose = ComposeView(uiContext).apply {
            setViewCompositionStrategy(ViewCompositionStrategy.DisposeOnViewTreeLifecycleDestroyed)
            setContent {
                val ui by controller.state.collectAsState()
                AssistOverlayEnvironment(onOpenExternalUri = ::openExternalUri) {
                    AssistOverlaySheet(
                        ui = ui,
                        onDraftChange = controller::updateDraftText,
                        onDictationHeard = controller::appendDictation,
                        onNotice = controller::showNotice,
                        onAttachScreen = controller::attachCurrentScreen,
                        onRemoveScreen = controller::removeAttachedScreen,
                        onOpenAssistantSettings = ::openAssistantSettings,
                        onToggleModelPicker = controller::toggleModelPicker,
                        onCloseModelPicker = controller::closeModelPicker,
                        onRefreshModels = controller::refreshModelCatalog,
                        onSelectMainConversation = controller::selectMainConversation,
                        onSelectExistingConversation = controller::selectExistingConversation,
                        onSelectNewConversation = controller::selectNewConversation,
                        onSelectDefaultModel = controller::selectDefaultModel,
                        onSelectModel = controller::selectModel,
                        onRetryWithDefaultMain = controller::retryWithDefaultMain,
                        onSend = controller::send,
                        onOpenInPersona = controller::openInPersona,
                        onDismiss = ::dismissOverlay,
                        onUnlock = controller::openInPersona,
                    )
                }
            }
        }
        frame.addView(
            compose,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT,
            ),
        )
        rootFrame = frame
        composeView = compose
        return frame
    }

    override fun onPrepareShow(args: Bundle?, showFlags: Int) {
        super.onPrepareShow(args, showFlags)
        setUiEnabled(true)
        val locked = isDeviceLocked(context)
        val probe = probeScreenshotSettings()
        val hasAssist = (showFlags and SHOW_WITH_ASSIST) != 0
        val hasShot = (showFlags and SHOW_WITH_SCREENSHOT) != 0
        val hasScreenContent = hasAssistStructureScreenContentFlag(showFlags)
        Log.i(
            ASSIST_LOG_TAG,
            "onPrepareShow showFlags=$showFlags withAssist=$hasAssist withScreenshot=$hasShot " +
                "withAssistScreenContent=$hasScreenContent disabledShowContext=${probe.disabledShowContext} " +
                "userDisabledShowContext=${probe.userDisabledShowContext} " +
                "secureAssistScreenshot=${probe.secureAssistScreenshotEnabled} " +
                "secureAssistStructure=${probe.secureAssistStructureEnabled} " +
                "hasArgs=${args != null} locked=$locked",
        )
        if (::bridge.isInitialized && ::controller.isInitialized) {
            if (!locked) bridge.start()
            controller.beginSession(
                supportsScreenshot = !locked,
                showFlags = showFlags,
                deviceLocked = locked,
                screenshotDisabledBySettings = probe.screenshotDisabledBySettings,
                expectScreenshotFromFlags = hasShot || showFlags == 0,
            )
        }
    }

    override fun onShow(args: Bundle?, showFlags: Int) {
        super.onShow(args, showFlags)
        // Re-apply AFTER super.onShow() to override VoiceInteractionWindow's default ADJUST_PAN.
        configureOverlayWindow(window?.window)
        val locked = isDeviceLocked(context)
        val probe = probeScreenshotSettings()
        val hasAssist = (showFlags and SHOW_WITH_ASSIST) != 0
        val hasShot = (showFlags and SHOW_WITH_SCREENSHOT) != 0
        val hasScreenContent = hasAssistStructureScreenContentFlag(showFlags)
        Log.i(
            ASSIST_LOG_TAG,
            "onShow showFlags=$showFlags withAssist=$hasAssist withScreenshot=$hasShot " +
                "withAssistScreenContent=$hasScreenContent disabledShowContext=${probe.disabledShowContext} " +
                "userDisabledShowContext=${probe.userDisabledShowContext} " +
                "secureAssistScreenshot=${probe.secureAssistScreenshotEnabled} " +
                "secureAssistStructure=${probe.secureAssistStructureEnabled} " +
                "hasArgs=${args != null} locked=$locked",
        )
        rootFrame?.let { frame ->
            lifecycleOwner.attachTo(window?.window?.decorView, frame)
            frame.post { configureOverlayWindow(window?.window) }
        }
        lifecycleOwner.onShow()
        if (::bridge.isInitialized && ::controller.isInitialized) {
            if (!locked) bridge.start()
            controller.beginSession(
                supportsScreenshot = !locked,
                showFlags = showFlags,
                deviceLocked = locked,
                screenshotDisabledBySettings = probe.screenshotDisabledBySettings,
                expectScreenshotFromFlags = hasShot || showFlags == 0,
            )
        }
    }

    override fun onHandleAssist(state: AssistState) {
        if (!::controller.isInitialized) return
        if (isDeviceLocked(context)) {
            Log.i(ASSIST_LOG_TAG, "onHandleAssist(state) dropped=deviceLocked")
            return
        }
        val contextCard = buildSafeContextCard(state.assistData, state.assistStructure, state.assistContent)
        Log.i(
            ASSIST_LOG_TAG,
            "onHandleAssist(state) hasStructure=${state.assistStructure != null} " +
                "hasContent=${state.assistContent != null} focused=${state.isFocused} safeContext=${contextCard != null}",
        )
        controller.onAssistContextReceived(contextCard, isFocused = state.isFocused)
    }

    @Deprecated("Deprecated in Java")
    @Suppress("DEPRECATION")
    override fun onHandleAssist(data: Bundle?, structure: AssistStructure?, content: AssistContent?) {
        if (!::controller.isInitialized) return
        if (isDeviceLocked(context)) {
            Log.i(ASSIST_LOG_TAG, "onHandleAssist(legacy) dropped=deviceLocked")
            return
        }
        val contextCard = buildSafeContextCard(data, structure, content)
        Log.i(
            ASSIST_LOG_TAG,
            "onHandleAssist(legacy) hasStructure=${structure != null} " +
                "hasContent=${content != null} safeContext=${contextCard != null}",
        )
        controller.onAssistContextReceived(contextCard, isFocused = true)
    }

    override fun onHandleScreenshot(screenshot: Bitmap?) {
        if (!::controller.isInitialized) return
        if (isDeviceLocked(context)) {
            Log.i(ASSIST_LOG_TAG, "onHandleScreenshot dropped=deviceLocked")
            return
        }
        val probe = if (screenshot == null) probeScreenshotSettings() else null
        controller.onScreenshotReceived(
            bitmap = screenshot,
            screenshotDisabledBySettings = probe?.screenshotDisabledBySettings == true,
        )
    }

    override fun onComputeInsets(outInsets: Insets) {
        super.onComputeInsets(outInsets)
        // Full-frame touchable region so tapping the backdrop scrim dismisses cleanly while keeping
        // the underlying app unresized and system navigation gestures intact.
        outInsets.touchableInsets = Insets.TOUCHABLE_INSETS_FRAME
    }

    override fun onBackPressed() {
        if (::controller.isInitialized && controller.state.value.modelPickerOpen) {
            controller.closeModelPicker()
            return
        }
        lifecycleOwner.dispatchBackPressed()
    }

    override fun onHide() {
        Log.i(ASSIST_LOG_TAG, "onHide")
        hideKeyboard()
        if (::controller.isInitialized) controller.endSession()
        if (::bridge.isInitialized) bridge.stop()
        lifecycleOwner.onHide()
        super.onHide()
    }

    override fun onDestroy() {
        Log.i(ASSIST_LOG_TAG, "onDestroy")
        hideKeyboard()
        if (::controller.isInitialized) controller.endSession()
        if (::bridge.isInitialized) bridge.stop()
        composeView?.disposeComposition()
        composeView = null
        rootFrame = null
        lifecycleOwner.onDestroy()
        sessionScope.cancel()
        super.onDestroy()
    }

    private fun probeScreenshotSettings(): AssistScreenshotSettingsProbe {
        val disabled = runCatching { disabledShowContext }.getOrDefault(0)
        val userDisabled = runCatching { userDisabledShowContext }.getOrDefault(0)
        val secureShot = runCatching {
            Settings.Secure.getInt(context.contentResolver, SECURE_ASSIST_SCREENSHOT_ENABLED, -1)
        }.getOrDefault(-1)
        val secureStructure = runCatching {
            Settings.Secure.getInt(context.contentResolver, SECURE_ASSIST_STRUCTURE_ENABLED, -1)
        }.getOrDefault(-1)
        return AssistScreenshotSettingsProbe(
            disabledShowContext = disabled,
            userDisabledShowContext = userDisabled,
            secureAssistScreenshotEnabled = secureShot,
            secureAssistStructureEnabled = secureStructure,
        )
    }

    private fun dismissOverlay() {
        // Hide the IME while rootFrame / decorView is still attached and holds a valid windowToken.
        hideKeyboard()
        if (::controller.isInitialized) controller.endSession()
        if (::bridge.isInitialized) bridge.stop()
        val _ = runCatching { hide() }
        val _ = runCatching { finish() }
    }

    private fun hideKeyboard() {
        val targetView = rootFrame?.takeIf { it.isAttachedToWindow }
            ?: window?.window?.peekDecorView()?.takeIf { it.isAttachedToWindow }
            ?: rootFrame
            ?: window?.window?.peekDecorView()
        targetView?.clearFocus()
        val token = targetView?.windowToken ?: return
        val _ = runCatching {
            context.getSystemService(InputMethodManager::class.java)?.hideSoftInputFromWindow(token, 0)
        }
    }

    private fun buildSafeContextCard(
        data: Bundle?,
        structure: AssistStructure?,
        content: AssistContent?,
    ): ChatLinkContext? {
        val snapshot = AssistContextExtractor.snapshotFromAssist(data, structure, content)
        val safe = AssistContextExtractor.extract(snapshot, context.packageName)
        return AssistContextExtractor.toLinkContext(
            safe = safe,
            id = controller.state.value.invocationId.ifEmpty { "assist-context" },
            fallbackTitle = uiContext.getString(R.string.app_assist_context_title),
        )
    }

    private fun launchMainActivity(targetSession: SessionKey?) {
        hideKeyboard()
        val intent = assistMainIntent(context, targetSession)
        val launched = runCatching {
            startAssistantActivity(intent)
            true
        }.getOrDefault(false)
        if (!launched) {
            val _ = runCatching { context.startActivity(intent) }
        }
    }

    private fun openAssistantSettings() {
        hideKeyboard()
        openSystemAssistSettings(context) { intent ->
            val launched = runCatching {
                startAssistantActivity(intent)
                true
            }.getOrDefault(false)
            if (!launched) {
                context.startActivity(intent)
            }
        }
        dismissOverlay()
    }

    private fun openExternalUri(uri: String) {
        hideKeyboard()
        val intent = Intent(Intent.ACTION_VIEW, Uri.parse(uri)).apply {
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        val launched = runCatching {
            startAssistantActivity(intent)
            true
        }.getOrDefault(false)
        if (!launched) {
            val _ = runCatching { context.startActivity(intent) }
        }
        dismissOverlay()
    }

    companion object {
        private const val SECURE_ASSIST_SCREENSHOT_ENABLED = "assist_screenshot_enabled"
        private const val SECURE_ASSIST_STRUCTURE_ENABLED = "assist_structure_enabled"
    }
}

internal fun hasAssistStructureScreenContentFlag(showFlags: Int): Boolean =
    Build.VERSION.SDK_INT >= 37 &&
        (showFlags and VoiceInteractionSession.SHOW_WITH_ASSIST_STRUCTURE_SCREEN_CONTENT) != 0

internal fun openSystemAssistSettings(context: Context, launch: (Intent) -> Unit = { context.startActivity(it) }): Boolean {
    val candidates = listOf(
        Intent(Settings.ACTION_VOICE_INPUT_SETTINGS),
        Intent(Settings.ACTION_MANAGE_DEFAULT_APPS_SETTINGS),
        Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", context.packageName, null)),
    )
    for (intent in candidates) {
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        val opened = runCatching { launch(intent); true }.getOrDefault(false)
        if (opened) return true
    }
    return false
}

internal fun isDeviceLocked(context: Context): Boolean = runCatching {
    val km = context.getSystemService(KeyguardManager::class.java) ?: return@runCatching false
    km.isKeyguardLocked || km.isDeviceLocked
}.getOrDefault(false)

/**
 * Configures the transparent floating overlay window so WindowManager never applies `ADJUST_PAN`
 * on top of Compose's single-layer `WindowInsets.ime.union(WindowInsets.navigationBars)` avoidance.
 */
internal fun configureOverlayWindow(window: Window?) {
    if (window == null) return
    window.setBackgroundDrawable(ColorDrawable(android.graphics.Color.TRANSPARENT))
    window.setLayout(WindowManager.LayoutParams.MATCH_PARENT, WindowManager.LayoutParams.MATCH_PARENT)
    window.clearFlags(
        WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or WindowManager.LayoutParams.FLAG_ALT_FOCUSABLE_IM,
    )
    window.setSoftInputMode(
        WindowManager.LayoutParams.SOFT_INPUT_ADJUST_NOTHING or WindowManager.LayoutParams.SOFT_INPUT_STATE_HIDDEN,
    )
    WindowCompat.setDecorFitsSystemWindows(window, false)
}

internal fun createOverlayController(
    uiContext: Context,
    scope: CoroutineScope,
    bridge: AssistHubBridge,
    launchFullApp: (SessionKey?) -> Unit,
    dismiss: () -> Unit,
): AssistOverlayController {
    val appContext = uiContext.applicationContext
    return AssistOverlayController(
        mainScope = scope,
        prepareScreenshot = { bitmap ->
            withContext(Dispatchers.Default) { AssistScreenshotStore.prepareInMemory(bitmap) }
        },
        saveScreenshot = { bitmap, id ->
            withContext(Dispatchers.IO) { AssistScreenshotStore.save(appContext, bitmap, id) }
        },
        deleteScreenshot = { attachment ->
            withContext(Dispatchers.IO) { AssistScreenshotStore.deleteSaved(appContext, attachment) }
        },
        prepareMainModel = bridge::prepareMainModel,
        prepareConversationModel = bridge::prepareConversationModel,
        createNewConversation = bridge::createNewConversation,
        onSelectObservedSession = bridge::setObservedSession,
        sendTextToMain = bridge::sendTextToMain,
        sendWithScreenToMain = bridge::sendWithScreenToMain,
        loadModelCatalog = bridge::loadModelCatalog,
        onPersistDefaultModel = bridge::selectDefaultModel,
        onPersistModelChoice = bridge::selectModel,
        publishDraftForFullApp = { id, screenshot, contextCard, text, targetSession ->
            AssistDraftBus.publish(id = id, screenshot = screenshot, context = contextCard, text = text)
            if (targetSession != null) {
                AssistDraftBus.claim(AssistDraftTarget.Session(targetSession))
            }
        },
        onWakeHub = bridge::wakeHub,
        onLaunchFullApp = launchFullApp,
        onDismiss = dismiss,
        screenOnlyDisplayLabel = { uiContext.getString(R.string.app_assist_screen_only_prompt) },
        defaultSendError = { uiContext.getString(R.string.app_assist_send_failed) },
        defaultModelStartError = { uiContext.getString(R.string.app_assist_model_start_failed) },
        initialModelChoice = bridge.currentModelChoice(),
    )
}

/** Builds the launch intent that opens Persona's main chat (or specific [targetSession]) in full screen. */
internal fun assistMainIntent(context: Context, targetSession: SessionKey? = null): Intent {
    val targetUri = if (targetSession != null) {
        NotificationRoute.conversation(targetSession.machineId, targetSession.sessionId, assistant = true).uri()
    } else {
        Uri.parse(AskPersonaLink)
    }
    return Intent(Intent.ACTION_VIEW, targetUri, context, MainActivity::class.java).apply {
        addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
    }
}
