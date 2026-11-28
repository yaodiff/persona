package persona.android.assist

import android.app.KeyguardManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.lifecycle.lifecycleScope
import persona.core.designsystem.AppLanguages

/**
 * Fallback entry point for launchers and OEM paths that dispatch `android.intent.action.ASSIST`
 * directly rather than binding a [android.service.voice.VoiceInteractionSession]. Renders the
 * same transparent bottom floating overlay card over the previous app.
 *
 * Security: because this Activity is exported for `ACTION_ASSIST`, caller-supplied Intent extras
 * are never trusted or converted into `<persona-context>`; only system `VoiceInteractionSession`
 * callbacks provide page context.
 */
class AssistActivity : ComponentActivity() {
    override fun attachBaseContext(base: Context) = super.attachBaseContext(AppLanguages.wrap(base))

    private lateinit var bridge: AssistHubBridge
    private lateinit var controller: AssistOverlayController

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        configureOverlayWindow(window)

        bridge = AssistHubBridge(
            context = this,
            scope = lifecycleScope,
            onSnapshot = { snapshot -> controller.onHubUpdate(snapshot) },
            onModelChoice = { choice -> controller.onModelChoiceUpdated(choice) },
        )
        controller = createOverlayController(
            uiContext = this,
            scope = lifecycleScope,
            bridge = bridge,
            launchFullApp = { targetSession ->
                val _ = runCatching { startActivity(assistMainIntent(this, targetSession)) }
            },
            dismiss = { finish() },
        )

        val locked = isDeviceLocked(this)
        if (!locked) {
            bridge.start()
        }
        controller.beginSession(supportsScreenshot = false, deviceLocked = locked)

        setContent {
            val ui by controller.state.collectAsState()
            BackHandler {
                if (ui.modelPickerOpen) {
                    controller.closeModelPicker()
                } else {
                    controller.endSession()
                    finish()
                }
            }
            AssistOverlayEnvironment(onOpenExternalUri = ::openExternalUri) {
                AssistOverlaySheet(
                    ui = ui,
                    onDraftChange = controller::updateDraftText,
                    onDictationHeard = controller::appendDictation,
                    onNotice = controller::showNotice,
                    onAttachScreen = controller::attachCurrentScreen,
                    onRemoveScreen = controller::removeAttachedScreen,
                    onOpenAssistantSettings = {
                        openSystemAssistSettings(this@AssistActivity)
                        controller.endSession()
                        finish()
                    },
                    onToggleModelPicker = controller::toggleModelPicker,
                    onCloseModelPicker = controller::closeModelPicker,
                    onRefreshModels = controller::refreshModelCatalog,
                    onSelectMainConversation = controller::selectMainConversation,
                    onSelectExistingConversation = controller::selectExistingConversation,
                    onSelectNewConversation = controller::selectNewConversation,
                    onSelectDefaultModel = controller::selectDefaultModel,
                    onSelectModel = controller::selectModel,
                    onRetryWithDefaultMain = controller::retryWithDefaultMain,
                    onUnlock = ::requestUnlock,
                    onSend = controller::send,
                    onOpenInPersona = controller::openInPersona,
                    onDismiss = {
                        controller.endSession()
                        finish()
                    },
                )
            }
        }
    }

    override fun onDestroy() {
        if (::controller.isInitialized) controller.endSession()
        if (::bridge.isInitialized) bridge.stop()
        super.onDestroy()
    }

    private fun requestUnlock() {
        val km = getSystemService(KeyguardManager::class.java)
        if (km != null && (km.isKeyguardLocked || km.isDeviceLocked)) {
            val _ = runCatching {
                km.requestDismissKeyguard(
                    this,
                    object : KeyguardManager.KeyguardDismissCallback() {
                        override fun onDismissSucceeded() {
                            bridge.start()
                            controller.onDeviceUnlocked()
                        }
                    },
                )
                return
            }
        }
        controller.openInPersona()
    }

    private fun openExternalUri(uri: String) {
        val target = Intent(Intent.ACTION_VIEW, Uri.parse(uri)).apply {
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        val _ = runCatching { startActivity(target) }
        controller.endSession()
        finish()
    }
}
