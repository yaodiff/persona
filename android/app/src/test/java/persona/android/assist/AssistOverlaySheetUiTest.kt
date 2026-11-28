package persona.android.assist

import android.app.Application
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.hapticfeedback.HapticFeedback
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.SoftwareKeyboardController
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.height
import androidx.compose.ui.unit.width
import kotlin.math.abs
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.RuleChain
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.android.ui.ComponentActivityHostRule
import persona.core.contracts.ModelPick
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DigitalAssistantModelCatalog
import persona.core.contracts.api.DigitalAssistantModelChoice
import persona.core.contracts.api.DigitalAssistantModelGroup
import persona.core.contracts.api.DigitalAssistantModelOption
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens

@RunWith(RobolectricTestRunner::class)
@Config(application = Application::class, sdk = [35], qualifiers = "en-w411dp-h915dp-xhdpi")
class AssistOverlaySheetUiTest {
    private val rule = createComposeRule()
    @get:Rule val host = RuleChain.outerRule(ComponentActivityHostRule()).around(rule)

    @Test
    fun unifiedComposerCardAdaptsCleanlyAcrossGlassBriefAndBoardInLightAndDark() {
        var currentStyle by mutableStateOf(AppStyle.Glass)
        var darkTheme by mutableStateOf(false)

        rule.setContent {
            PersonaTheme(darkTheme = darkTheme, reduceMotion = true, style = currentStyle) {
                AssistOverlaySheet(
                    ui = AssistOverlayUiState(
                        invocationId = "inv-styles",
                        captureStatus = AssistCaptureStatus.Available,
                        hubStatus = AssistHubStatus.Ready,
                    ),
                    onDraftChange = {},
                    onDictationHeard = {},
                    onNotice = {},
                    onAttachScreen = {},
                    onRemoveScreen = {},
                    onSend = {},
                    onOpenInPersona = {},
                    onDismiss = {},
                )
            }
        }

        val matrix = listOf(
            AppStyle.Glass to false,
            AppStyle.Glass to true,
            AppStyle.Brief to false,
            AppStyle.Brief to true,
            AppStyle.Board to false,
            AppStyle.Board to true,
        )
        for ((style, dark) in matrix) {
            rule.runOnIdle {
                currentStyle = style
                darkTheme = dark
            }
            rule.waitForIdle()

            val tag = "${style.name}/${if (dark) "Dark" else "Light"}"
            val rootBounds = rule.onNodeWithTag("assist-overlay-root").getUnclippedBoundsInRoot()
            val cardBounds = rule.onNodeWithTag("assist-composer-island").getUnclippedBoundsInRoot()
            val brandBounds = rule.onNodeWithTag("assist-brand-orb").getUnclippedBoundsInRoot()
            val screenBounds = rule.onNodeWithTag("assist-attach-screen-chip").getUnclippedBoundsInRoot()
            val modelBounds = rule.onNodeWithTag("assist-model-pill").getUnclippedBoundsInRoot()
            val sendBounds = rule.onNodeWithTag("assist-send-button").getUnclippedBoundsInRoot()

            assertTrue("$tag: card must keep left margin", cardBounds.left > rootBounds.left)
            assertTrue("$tag: card must keep right margin", cardBounds.right < rootBounds.right)
            assertTrue("$tag: card must float above bottom edge", cardBounds.bottom < rootBounds.bottom)
            assertTrue("$tag: unified card height should be compact (~110-156dp), got ${cardBounds.height}", cardBounds.height in 108.dp..156.dp)
            assertTrue("$tag: PersonaFace anchor must sit inside card", brandBounds.left >= cardBounds.left && brandBounds.top >= cardBounds.top)
            assertTrue("$tag: attach screen chip must be >= 44dp tall, got ${screenBounds.height}", screenBounds.height >= 44.dp)
            assertTrue("$tag: model pill must be >= 44dp tall, got ${modelBounds.height}", modelBounds.height >= 44.dp)
            assertTrue("$tag: send button must be >= 44dp tall, got ${sendBounds.height}", sendBounds.height >= 44.dp)
            assertTrue("$tag: send button must be >= 44dp wide, got ${sendBounds.width}", sendBounds.width >= 44.dp)
            val expectedSendRightPx = with(rule.density) { (cardBounds.right - Tokens.Space.m).toPx() }
            val actualSendRightPx = with(rule.density) { sendBounds.right.toPx() }
            assertTrue(
                "$tag: send button right edge must sit flush to card inner padding (<= 2px), diff=${abs(expectedSendRightPx - actualSendRightPx)}px",
                abs(expectedSendRightPx - actualSendRightPx) <= 2f,
            )
            val modelToSendGapPx = with(rule.density) { (sendBounds.left - modelBounds.right).toPx() }
            val expectedGapPx = with(rule.density) { Tokens.Space.xs.toPx() }
            assertTrue(
                "$tag: model pill must hug send button without trailing dead space, gap=${modelToSendGapPx}px",
                abs(modelToSendGapPx - expectedGapPx) <= 2f,
            )
            assertTrue("$tag: screen chip must stay left of model pill", screenBounds.right <= modelBounds.left)

            // Concise non-truncating screen label, top drag handle, zero screenshot thumbnail,
            // zero X close clutter, zero bottom-bar open-in-Persona icon, and zero bottom-bar mic button.
            rule.onNodeWithText("Attach screen").assertExists()
            rule.onNodeWithTag("assist-drag-handle").assertExists()
            rule.onNodeWithTag("assist-screen-thumbnail", useUnmergedTree = true).assertDoesNotExist()
            rule.onNodeWithTag("assist-close-button").assertDoesNotExist()
            rule.onNodeWithTag("assist-open-in-persona", useUnmergedTree = true).assertDoesNotExist()
            rule.onNodeWithTag("assist-dictate-button", useUnmergedTree = true).assertDoesNotExist()
            rule.onNodeWithTag("assist-input-field").assertExists()
        }
    }

    @Test
    fun privacyScreenToggleAndDisabledSettingsWorkWithoutThumbnails() {
        var attachClicks = 0
        var removeClicks = 0
        var openedSettings = 0
        var uiState by mutableStateOf(
            AssistOverlayUiState(
                invocationId = "inv-privacy-toggle",
                captureStatus = AssistCaptureStatus.Available,
                hubStatus = AssistHubStatus.Ready,
            ),
        )

        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true, style = AppStyle.Glass) {
                AssistOverlaySheet(
                    ui = uiState,
                    onDraftChange = {},
                    onDictationHeard = {},
                    onNotice = {},
                    onAttachScreen = { attachClicks++ },
                    onRemoveScreen = { removeClicks++ },
                    onOpenAssistantSettings = { openedSettings++ },
                    onSend = {},
                    onOpenInPersona = {},
                    onDismiss = {},
                )
            }
        }

        rule.onNodeWithText("Attach screen").assertExists()
        rule.onNodeWithTag("assist-screen-thumbnail", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithTag("assist-attach-screen-chip").performClick()
        assertEquals(1, attachClicks)

        // Transition to Attached: check badge renders while still revealing zero screen pixels.
        rule.runOnIdle {
            uiState = uiState.copy(captureStatus = AssistCaptureStatus.Attached)
        }
        rule.waitForIdle()

        val attachedBounds = rule.onNodeWithTag("assist-screen-attached-row").getUnclippedBoundsInRoot()
        assertTrue("Attached chip must be >= 44dp tall, got ${attachedBounds.height}", attachedBounds.height >= 44.dp)
        rule.onNodeWithTag("assist-screen-thumbnail", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithTag("assist-screen-attached-badge", useUnmergedTree = true).assertExists()
        rule.onNodeWithTag("assist-screen-attached-row").performClick()
        assertEquals(1, removeClicks)

        // Transition to DisabledBySettings: direct Enable action stays >= 44dp tall and clickable.
        rule.runOnIdle {
            uiState = uiState.copy(captureStatus = AssistCaptureStatus.DisabledBySettings)
        }
        rule.waitForIdle()

        val settingsButtonBounds = rule.onNodeWithTag("assist-open-settings-button").getUnclippedBoundsInRoot()
        assertTrue("Enable settings button must be >= 44dp tall, got ${settingsButtonBounds.height}", settingsButtonBounds.height >= 44.dp)
        rule.onNodeWithTag("assist-screen-disabled").assertExists()
        rule.onNodeWithTag("assist-open-settings-button").performClick()
        assertEquals(1, openedSettings)
    }

    @Test
    @Config(application = Application::class, sdk = [35], qualifiers = "en-w360dp-h780dp-xhdpi")
    fun narrow360dpPhoneKeepsShortAttachLabelAndControlsComfortable() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Brief) {
                AssistOverlaySheet(
                    ui = AssistOverlayUiState(
                        invocationId = "inv-narrow-360",
                        captureStatus = AssistCaptureStatus.Available,
                        hubStatus = AssistHubStatus.Ready,
                    ),
                    onDraftChange = {},
                    onDictationHeard = {},
                    onNotice = {},
                    onAttachScreen = {},
                    onRemoveScreen = {},
                    onSend = {},
                    onOpenInPersona = {},
                    onDismiss = {},
                )
            }
        }

        val cardBounds = rule.onNodeWithTag("assist-composer-island").getUnclippedBoundsInRoot()
        val screenBounds = rule.onNodeWithTag("assist-attach-screen-chip").getUnclippedBoundsInRoot()
        val modelBounds = rule.onNodeWithTag("assist-model-pill").getUnclippedBoundsInRoot()
        val sendBounds = rule.onNodeWithTag("assist-send-button").getUnclippedBoundsInRoot()

        rule.onNodeWithText("Attach screen").assertExists()
        rule.onNodeWithTag("assist-open-in-persona", useUnmergedTree = true).assertDoesNotExist()
        assertTrue("360dp attach chip >= 44dp tall", screenBounds.height >= 44.dp)
        assertTrue("360dp model pill >= 44dp tall", modelBounds.height >= 44.dp)
        assertTrue("360dp send button >= 44dp tall", sendBounds.height >= 44.dp)
        val expectedSendRightPx = with(rule.density) { (cardBounds.right - Tokens.Space.m).toPx() }
        val actualSendRightPx = with(rule.density) { sendBounds.right.toPx() }
        assertTrue(
            "360dp send button must stay flush to card inner right edge (<= 2px), diff=${abs(expectedSendRightPx - actualSendRightPx)}px",
            abs(expectedSendRightPx - actualSendRightPx) <= 2f,
        )
        assertTrue("Controls must not overlap horizontally", screenBounds.right <= modelBounds.left)
        assertTrue("Model pill must stay left of send button", modelBounds.right <= sendBounds.left)
    }

    @Test
    @Config(application = Application::class, sdk = [35], qualifiers = "en-w320dp-h640dp-xhdpi")
    fun ultraNarrow320dpWithLongModelNameAndAttachedBadgeNeverClipsTrailingButtons() {
        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true, style = AppStyle.Glass) {
                AssistOverlaySheet(
                    ui = AssistOverlayUiState(
                        invocationId = "inv-narrow-320-long-model",
                        captureStatus = AssistCaptureStatus.Attached,
                        hubStatus = AssistHubStatus.Ready,
                        modelChoice = DigitalAssistantModelChoice(
                            pick = ModelPick("custom", "enterprise-long-model-name-2026"),
                            modelLabel = "Enterprise Super Long Custom Model Name v9.9.9",
                            launcherLabel = "Custom",
                        ),
                    ),
                    onDraftChange = {},
                    onDictationHeard = {},
                    onNotice = {},
                    onAttachScreen = {},
                    onRemoveScreen = {},
                    onSend = {},
                    onOpenInPersona = {},
                    onDismiss = {},
                )
            }
        }

        val cardBounds = rule.onNodeWithTag("assist-composer-island").getUnclippedBoundsInRoot()
        val screenBounds = rule.onNodeWithTag("assist-screen-attached-row").getUnclippedBoundsInRoot()
        val modelBounds = rule.onNodeWithTag("assist-model-pill").getUnclippedBoundsInRoot()
        val sendBounds = rule.onNodeWithTag("assist-send-button").getUnclippedBoundsInRoot()

        rule.onNodeWithTag("assist-open-in-persona", useUnmergedTree = true).assertDoesNotExist()
        assertTrue("320dp send button must stay >= 44dp wide, got ${sendBounds.width}", sendBounds.width >= 44.dp)
        assertTrue("320dp send button must stay inside card right edge", sendBounds.right <= cardBounds.right)
        val expectedSendRightPx = with(rule.density) { (cardBounds.right - Tokens.Space.m).toPx() }
        val actualSendRightPx = with(rule.density) { sendBounds.right.toPx() }
        assertTrue(
            "320dp send button right edge must stay flush to card inner padding (<= 2px), diff=${abs(expectedSendRightPx - actualSendRightPx)}px",
            abs(expectedSendRightPx - actualSendRightPx) <= 2f,
        )
        assertTrue("320dp model pill must stay left of send button", modelBounds.right <= sendBounds.left)
        assertTrue("320dp screen chip must stay left of model pill", screenBounds.right <= modelBounds.left)
    }

    @Test
    @Config(application = Application::class, sdk = [35], qualifiers = "zh-w411dp-h915dp-xhdpi")
    fun shortChineseModelLabelKeepsSendButtonFlushToCardRightEdge() {
        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true, style = AppStyle.Glass) {
                AssistOverlaySheet(
                    ui = AssistOverlayUiState(
                        invocationId = "inv-zh-right-flush",
                        captureStatus = AssistCaptureStatus.Available,
                        hubStatus = AssistHubStatus.Ready,
                    ),
                    onDraftChange = {},
                    onDictationHeard = {},
                    onNotice = {},
                    onAttachScreen = {},
                    onRemoveScreen = {},
                    onSend = {},
                    onOpenInPersona = {},
                    onDismiss = {},
                )
            }
        }

        val cardBounds = rule.onNodeWithTag("assist-composer-island").getUnclippedBoundsInRoot()
        val screenBounds = rule.onNodeWithTag("assist-attach-screen-chip").getUnclippedBoundsInRoot()
        val modelBounds = rule.onNodeWithTag("assist-model-pill").getUnclippedBoundsInRoot()
        val sendBounds = rule.onNodeWithTag("assist-send-button").getUnclippedBoundsInRoot()

        rule.onNodeWithText("附带屏幕").assertExists()
        rule.onNodeWithText("主对话").assertExists()
        rule.onNodeWithTag("assist-open-in-persona", useUnmergedTree = true).assertDoesNotExist()

        val expectedSendRightPx = with(rule.density) { (cardBounds.right - Tokens.Space.m).toPx() }
        val actualSendRightPx = with(rule.density) { sendBounds.right.toPx() }
        assertTrue(
            "Chinese short model label must keep send button flush to card inner right edge (<= 2px), diff=${abs(expectedSendRightPx - actualSendRightPx)}px",
            abs(expectedSendRightPx - actualSendRightPx) <= 2f,
        )
        val modelToSendGapPx = with(rule.density) { (sendBounds.left - modelBounds.right).toPx() }
        val expectedGapPx = with(rule.density) { Tokens.Space.xs.toPx() }
        assertTrue(
            "Model pill must stay anchored next to send button with no trailing whitespace, gap=${modelToSendGapPx}px",
            abs(modelToSendGapPx - expectedGapPx) <= 2f,
        )
        assertTrue("Screen chip must not overlap model pill", screenBounds.right <= modelBounds.left)
        assertTrue("Model pill must not overlap send button", modelBounds.right <= sendBounds.left)
    }

    @Test
    fun expandedReplyIslandAndSingleLayerNoticesFloatAboveUnifiedCardAcrossBoardAndBrief() {
        var openedPersona = 0
        var retriedMain = 0
        var currentStyle by mutableStateOf(AppStyle.Board)
        var darkTheme by mutableStateOf(true)

        rule.setContent {
            PersonaTheme(darkTheme = darkTheme, reduceMotion = true, style = currentStyle) {
                AssistOverlaySheet(
                    ui = AssistOverlayUiState(
                        invocationId = "inv-expanded-notices",
                        captureStatus = AssistCaptureStatus.Available,
                        hubStatus = AssistHubStatus.Offline,
                        errorMessage = "Selected model is unavailable right now",
                        modelFallbackAvailable = true,
                        awaitingReply = true,
                        turns = listOf(
                            AssistOverlayTurn(
                                id = "u1",
                                fromUser = true,
                                text = "Summarize this page",
                            ),
                            AssistOverlayTurn(
                                id = "a1",
                                fromUser = false,
                                text = "Here are the key takeaways…",
                                streaming = true,
                            ),
                        ),
                    ),
                    onDraftChange = {},
                    onDictationHeard = {},
                    onNotice = {},
                    onAttachScreen = {},
                    onRemoveScreen = {},
                    onRetryWithDefaultMain = { retriedMain++ },
                    onSend = {},
                    onOpenInPersona = { openedPersona++ },
                    onDismiss = {},
                )
            }
        }

        // Verify Board Dark expanded stack & single-layer notice actions.
        val replyBounds = rule.onNodeWithTag("assist-reply-island").getUnclippedBoundsInRoot()
        val offlineBounds = rule.onNodeWithTag("assist-hub-offline").getUnclippedBoundsInRoot()
        val errorBounds = rule.onNodeWithTag("assist-error-notice").getUnclippedBoundsInRoot()
        val composerBounds = rule.onNodeWithTag("assist-composer-island").getUnclippedBoundsInRoot()
        val offlineActionBounds = rule.onNodeWithTag("assist-hub-offline-action").getUnclippedBoundsInRoot()
        val errorActionBounds = rule.onNodeWithTag("assist-error-action").getUnclippedBoundsInRoot()

        assertTrue("Reply island sits above offline notice", offlineBounds.top - replyBounds.bottom >= 7.dp)
        assertTrue("Offline notice sits above error notice", errorBounds.top - offlineBounds.bottom >= 7.dp)
        assertTrue("Error notice sits above unified composer card", composerBounds.top - errorBounds.bottom >= 7.dp)
        assertTrue("Offline notice action must be >= 44dp tall", offlineActionBounds.height >= 44.dp)
        assertTrue("Error fallback action must be >= 44dp tall", errorActionBounds.height >= 44.dp)

        rule.onNodeWithTag("assist-reply-brand").assertExists()
        rule.onNodeWithTag("assist-transcript").assertExists()
        rule.onNodeWithTag("assist-hub-offline-action").performClick()
        assertEquals(1, openedPersona)
        rule.onNodeWithTag("assist-error-action").performClick()
        assertEquals(1, retriedMain)

        // Switch live to Brief Light and ensure every layer remains cleanly rendered and reachable.
        rule.runOnIdle {
            currentStyle = AppStyle.Brief
            darkTheme = false
        }
        rule.waitForIdle()
        rule.onNodeWithTag("assist-reply-island").assertExists()
        rule.onNodeWithTag("assist-hub-offline").assertExists()
        rule.onNodeWithTag("assist-error-notice").assertExists()
        rule.onNodeWithTag("assist-composer-island").assertExists()
    }

    @Test
    fun longPressInputHoldToSpeakStreamsLivePartialPreviewAndFinishesGracefullyOnRelease() {
        val haptics = mutableListOf<HapticFeedbackType>()
        var keyboardShows = 0
        var keyboardHides = 0
        val fakeKeyboard = object : SoftwareKeyboardController {
            override fun show() { keyboardShows++ }
            override fun hide() { keyboardHides++ }
        }
        val fakeHaptics = object : HapticFeedback {
            override fun performHapticFeedback(hapticFeedbackType: HapticFeedbackType) {
                haptics += hapticFeedbackType
            }
        }
        val fakeDictation = FakeDictationController()
        var dictationControl by mutableStateOf<AssistDictationController?>(fakeDictation)
        var uiState by mutableStateOf(
            AssistOverlayUiState(
                invocationId = "inv-voice-long-press",
                captureStatus = AssistCaptureStatus.Available,
                hubStatus = AssistHubStatus.Ready,
            ),
        )

        rule.setContent {
            CompositionLocalProvider(
                LocalHapticFeedback provides fakeHaptics,
                LocalSoftwareKeyboardController provides fakeKeyboard,
            ) {
                PersonaTheme(darkTheme = true, reduceMotion = true, style = AppStyle.Glass) {
                    AssistOverlaySheet(
                        ui = uiState,
                        onDraftChange = { uiState = uiState.copy(draftText = it) },
                        onDictationHeard = { words ->
                            uiState = uiState.copy(
                                draftText = persona.feature.chat.withDictation(uiState.draftText, words),
                            )
                        },
                        onNotice = {},
                        onAttachScreen = {},
                        onRemoveScreen = {},
                        onSend = {},
                        onOpenInPersona = {},
                        onDismiss = {},
                        dictationOverride = dictationControl,
                    )
                }
            }
        }

        // Bottom bar has no standalone mic button, overlay never auto-opens keyboard,
        // and placeholder naturally hints holding for voice.
        rule.onNodeWithTag("assist-dictate-button", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("Ask Persona · hold for voice").assertExists()
        assertEquals(0, keyboardShows)

        // Seed an existing draft to prove live partial preview combines cleanly without dropping it.
        rule.runOnIdle { uiState = uiState.copy(draftText = "Check") }
        rule.waitForIdle()

        // Press and hold past long-press timeout without releasing yet -> starts voice dictation.
        rule.onNodeWithTag("assist-input-field").performTouchInput { down(center) }
        rule.mainClock.advanceTimeBy(600L)
        rule.waitForIdle()
        assertTrue("Holding input should start voice dictation", fakeDictation.listening)
        assertEquals(1, fakeDictation.startCount)
        assertEquals(0, fakeDictation.finishCount)
        assertEquals(0, fakeDictation.cancelCount)
        assertTrue("Long-press should fire LongPress haptic", haptics.contains(HapticFeedbackType.LongPress))
        assertTrue("Starting dictation should hide soft keyboard", keyboardHides >= 1)
        rule.onNodeWithTag("assist-dictate-listening").assertExists()
        rule.onNodeWithText("Listening… Release to finish").assertExists()

        // Live partial results render immediately inside the listening bar combined with existing draft.
        rule.runOnIdle { fakeDictation.emitPartial("today's weather") }
        rule.waitForIdle()
        rule.onNodeWithTag("assist-dictate-partial", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Check today's weather").assertExists()

        // Releasing the finger calls graceful finish() (never hard cancel()), keeping preview until onResults commits.
        rule.onRoot().performTouchInput { up() }
        rule.waitForIdle()
        assertEquals("Releasing hold must trigger graceful finish()", 1, fakeDictation.finishCount)
        assertEquals("Releasing hold must never hard-cancel dictation", 0, fakeDictation.cancelCount)
        assertTrue("Listening preview remains visible while awaiting final onResults", fakeDictation.listening)

        // Once final results commit, handle exits listening cleanly without popping the keyboard.
        rule.runOnIdle {
            fakeDictation.completeResult()
            uiState = uiState.copy(
                draftText = persona.feature.chat.withDictation(uiState.draftText, "today's weather"),
            )
        }
        rule.waitForIdle()
        assertFalse(fakeDictation.listening)
        rule.onNodeWithTag("assist-input-field").assertExists()
        assertEquals("Check today's weather", uiState.draftText)
        assertEquals(0, keyboardShows)

        // Accessibility onLongClick starts dictation without a held pointer; tapping listening bar finishes gracefully.
        rule.onNodeWithTag("assist-input-field").performSemanticsAction(SemanticsActions.OnLongClick)
        rule.waitForIdle()
        assertEquals(2, fakeDictation.startCount)
        assertTrue(fakeDictation.listening)
        rule.onNodeWithTag("assist-dictate-listening").performClick()
        rule.waitForIdle()
        assertEquals("Tap on listening bar should invoke graceful finish()", 2, fakeDictation.finishCount)
        assertEquals(0, fakeDictation.cancelCount)
        rule.runOnIdle { fakeDictation.completeResult() }
        rule.waitForIdle()
        assertFalse(fakeDictation.listening)
        rule.onNodeWithTag("assist-input-field").assertExists()

        // When speech recognition is unavailable, placeholder gracefully falls back without mic UI.
        rule.runOnIdle {
            uiState = uiState.copy(draftText = "")
            dictationControl = null
        }
        rule.waitForIdle()
        rule.onNodeWithText("Ask Persona…").assertExists()
        rule.onNodeWithTag("assist-dictate-button", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithTag("assist-input-field").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onNodeWithTag("assist-dictate-listening").assertDoesNotExist()
    }

    @Test
    fun bidirectionalVerticalDragUpOpensFullPersonaDownDismissesAndShortDragSpringsBack() {
        val haptics = mutableListOf<HapticFeedbackType>()
        val fakeHaptics = object : HapticFeedback {
            override fun performHapticFeedback(hapticFeedbackType: HapticFeedbackType) {
                haptics += hapticFeedbackType
            }
        }
        val fakeDictation = FakeDictationController()
        var openedPersona = 0
        var dismissed = 0
        var uiState by mutableStateOf(
            AssistOverlayUiState(
                invocationId = "inv-bidirectional-drag",
                captureStatus = AssistCaptureStatus.Available,
                hubStatus = AssistHubStatus.Ready,
                draftText = "Check this screen",
            ),
        )

        rule.setContent {
            CompositionLocalProvider(LocalHapticFeedback provides fakeHaptics) {
                PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Board) {
                    AssistOverlaySheet(
                        ui = uiState,
                        onDraftChange = {},
                        onDictationHeard = {},
                        onNotice = {},
                        onAttachScreen = {},
                        onRemoveScreen = {},
                        onSend = {},
                        onOpenInPersona = { openedPersona++ },
                        onDismiss = { dismissed++ },
                        dictationOverride = fakeDictation,
                    )
                }
            }
        }

        val restingTop = rule.onNodeWithTag("assist-composer-island").getUnclippedBoundsInRoot().top
        val shortUpPx = with(rule.density) { 24.dp.toPx() }
        val shortDownPx = with(rule.density) { 20.dp.toPx() }
        val stepPx = with(rule.density) { 20.dp.toPx() }

        // Short upward drag below threshold springs back cleanly without opening Persona.
        rule.onNodeWithTag("assist-composer-island").performTouchInput {
            down(center)
            moveBy(Offset(0f, -shortUpPx))
            up()
        }
        rule.waitForIdle()
        assertEquals(0, openedPersona)
        assertEquals(0, dismissed)
        assertEquals(
            restingTop.value,
            rule.onNodeWithTag("assist-composer-island").getUnclippedBoundsInRoot().top.value,
            1f,
        )

        // Short downward drag below threshold springs back cleanly without dismissing.
        rule.onNodeWithTag("assist-composer-island").performTouchInput {
            down(center)
            moveBy(Offset(0f, shortDownPx))
            up()
        }
        rule.waitForIdle()
        assertEquals(0, openedPersona)
        assertEquals(0, dismissed)
        assertEquals(
            restingTop.value,
            rule.onNodeWithTag("assist-composer-island").getUnclippedBoundsInRoot().top.value,
            1f,
        )

        // Upward drag past 56dp threshold triggers detent haptic, cancels dictation, and opens Persona.
        rule.runOnIdle { fakeDictation.start() }
        rule.waitForIdle()
        assertTrue(fakeDictation.listening)
        rule.onNodeWithTag("assist-composer-island").performTouchInput {
            down(Offset(centerX, top + 8f))
            repeat(5) {
                advanceEventTime(16L)
                moveBy(Offset(0f, -stepPx))
            }
            up()
        }
        rule.waitForIdle()
        assertTrue(
            "Crossing swipe-up threshold should trigger light haptic feedback",
            haptics.contains(HapticFeedbackType.TextHandleMove),
        )
        assertFalse("Swipe-up into Persona should cancel active dictation", fakeDictation.listening)
        assertEquals("Swipe-up should hard-cancel dictation rather than waiting for finish", 1, fakeDictation.cancelCount)
        assertEquals(0, fakeDictation.finishCount)
        assertEquals(1, openedPersona)
        assertEquals(0, dismissed)

        // Accessible click on top drag handle also opens full Persona (and cancels dictation) without a bottom icon.
        rule.onNodeWithTag("assist-open-in-persona", useUnmergedTree = true).assertDoesNotExist()
        rule.runOnIdle { fakeDictation.start() }
        rule.waitForIdle()
        assertTrue(fakeDictation.listening)
        rule.onNodeWithTag("assist-drag-handle").performClick()
        rule.waitForIdle()
        assertFalse("Top drag handle accessibility action should cancel active dictation", fakeDictation.listening)
        assertEquals(2, fakeDictation.cancelCount)
        assertEquals(2, openedPersona)

        // Downward drag past 48dp threshold dismisses cleanly.
        rule.onNodeWithTag("assist-composer-island").performTouchInput {
            down(Offset(centerX, top + 8f))
            repeat(5) {
                advanceEventTime(16L)
                moveBy(Offset(0f, stepPx))
            }
            up()
        }
        rule.waitForIdle()
        assertEquals(2, openedPersona)
        assertEquals(1, dismissed)

        // Unpaired guidance card retains its explicit "Open Persona" action button.
        rule.runOnIdle { uiState = uiState.copy(hubStatus = AssistHubStatus.Unpaired) }
        rule.waitForIdle()
        rule.onNodeWithTag("assist-hub-unpaired-action").performClick()
        rule.waitForIdle()
        assertEquals("Unpaired guidance action should still open Persona", 3, openedPersona)

        // Lockscreen state ignores upward open-Persona drag while keeping its Unlock button & swipe-down dismiss.
        rule.runOnIdle { uiState = uiState.copy(deviceLocked = true, hubStatus = AssistHubStatus.Ready) }
        rule.waitForIdle()
        rule.onNodeWithTag("assist-locked-card").performTouchInput {
            down(center)
            repeat(5) {
                advanceEventTime(16L)
                moveBy(Offset(0f, -stepPx))
            }
            up()
        }
        rule.waitForIdle()
        assertEquals("Locked overlay must not open full app via swipe-up", 3, openedPersona)
        rule.onNodeWithTag("assist-unlock-button").performClick()
        rule.waitForIdle()
        assertEquals("Locked overlay Unlock CTA should still invoke onUnlock/onOpenInPersona", 4, openedPersona)
        rule.onNodeWithTag("assist-locked-card").performTouchInput {
            down(center)
            repeat(5) {
                advanceEventTime(16L)
                moveBy(Offset(0f, stepPx))
            }
            up()
        }
        rule.waitForIdle()
        assertEquals("Locked overlay still dismisses via swipe-down", 2, dismissed)
    }

    @Test
    fun conversationAndModelPickerSwitchesTargetsAndUpdatesPillAndReplyHeader() {
        val convA = AssistConversationOption(
            key = SessionKey("m1", "conv-a"),
            title = "Travel Plan",
            modelPick = ModelPick("gemini", "gemini-2.5-flash"),
        )
        val convB = AssistConversationOption(
            key = SessionKey("m1", "conv-b"),
            title = "Budget",
        )
        val fastChoice = DigitalAssistantModelChoice(
            pick = ModelPick("gemini", "gemini-2.5-flash"),
            modelLabel = "Gemini 2.5 Flash",
            launcherLabel = "Gemini",
        )
        val catalog = DigitalAssistantModelCatalog(
            groups = listOf(
                DigitalAssistantModelGroup(
                    launcher = "gemini",
                    title = "Gemini",
                    options = listOf(
                        DigitalAssistantModelOption(
                            pick = ModelPick("gemini", "gemini-2.5-flash"),
                            modelLabel = "Gemini 2.5 Flash",
                            launcherLabel = "Gemini",
                        ),
                    ),
                ),
            ),
        )
        var selectedMain = 0
        var selectedNew = 0
        val selectedExisting = mutableListOf<AssistConversationOption>()
        var uiState by mutableStateOf(
            AssistOverlayUiState(
                invocationId = "inv-conv-picker",
                captureStatus = AssistCaptureStatus.Available,
                hubStatus = AssistHubStatus.Ready,
                modelPickerOpen = true,
                availableConversations = listOf(convA, convB),
                modelCatalog = catalog,
            ),
        )

        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                AssistOverlaySheet(
                    ui = uiState,
                    onDraftChange = {},
                    onDictationHeard = {},
                    onNotice = {},
                    onAttachScreen = {},
                    onRemoveScreen = {},
                    onSelectMainConversation = { selectedMain++ },
                    onSelectExistingConversation = { selectedExisting += it },
                    onSelectNewConversation = { selectedNew++ },
                    onSend = {},
                    onOpenInPersona = {},
                    onDismiss = {},
                )
            }
        }

        rule.onNodeWithTag("assist-conversation-strip").assertExists()
        val mainChip = rule.onNodeWithTag("assist-conversation-option-main").getUnclippedBoundsInRoot()
        val newChip = rule.onNodeWithTag("assist-conversation-option-new").getUnclippedBoundsInRoot()
        val existingChip = rule.onNodeWithTag("assist-conversation-option-conv-a").getUnclippedBoundsInRoot()
        assertTrue("Main chip >= 44dp tall", mainChip.height >= 44.dp)
        assertTrue("New chat chip >= 44dp tall", newChip.height >= 44.dp)
        assertTrue("Existing conversation chip >= 44dp tall", existingChip.height >= 44.dp)

        rule.onNodeWithTag("assist-conversation-option-new").performClick()
        assertEquals(1, selectedNew)
        rule.onNodeWithTag("assist-conversation-option-conv-a").performClick()
        assertEquals(listOf(convA), selectedExisting)
        rule.onNodeWithTag("assist-conversation-option-main").performClick()
        assertEquals(1, selectedMain)

        // NewChat + custom model shows concise combined label on bottom pill.
        rule.runOnIdle {
            uiState = uiState.copy(
                modelPickerOpen = false,
                targetConversation = AssistTargetConversation.NewChat,
                modelChoice = fastChoice,
            )
        }
        rule.waitForIdle()
        rule.onNodeWithText("New · Gemini 2.5 Flash").assertExists()

        // Existing non-main conversation shows its title on both pill and expanded reply header.
        rule.runOnIdle {
            uiState = uiState.copy(
                targetConversation = AssistTargetConversation.Existing(convA.key, convA.title, convA.modelPick),
                modelChoice = fastChoice,
                turns = listOf(
                    AssistOverlayTurn(id = "u-1", fromUser = true, text = "Check flights"),
                    AssistOverlayTurn(id = "a-1", fromUser = false, text = "Checking options"),
                ),
            )
        }
        rule.waitForIdle()
        rule.onNodeWithText("Travel Plan · Gemini 2.5 Flash").assertExists()
        rule.onNodeWithTag("assist-reply-target-label").assertExists()
    }

    private class FakeDictationController : AssistDictationController {
        override var listening by mutableStateOf(false)
            private set
        override var partialText by mutableStateOf("")
            private set
        var startCount = 0
            private set
        var finishCount = 0
            private set
        var cancelCount = 0
            private set

        override fun start() {
            startCount++
            partialText = ""
            listening = true
        }

        override fun finish() {
            if (!listening) return
            finishCount++
        }

        override fun cancel() {
            cancelCount++
            partialText = ""
            listening = false
        }

        fun emitPartial(text: String) {
            partialText = text
        }

        fun completeResult() {
            partialText = ""
            listening = false
        }
    }
}
