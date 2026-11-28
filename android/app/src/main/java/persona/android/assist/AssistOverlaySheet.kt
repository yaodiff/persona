package persona.android.assist

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import android.widget.Toast
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectVerticalDragGestures
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.ime
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.union
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.composed
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.onLongClick
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import persona.android.PersonaApp
import persona.android.R
import persona.core.contracts.api.DigitalAssistantModelCatalog
import persona.core.contracts.api.DigitalAssistantModelChoice
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JDivider
import persona.core.designsystem.JIcon
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JSpinner
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.LocalAppStyleStore
import persona.core.designsystem.LocalPersonaDesign
import persona.core.designsystem.LocalPersonaName
import persona.core.designsystem.LocalPersonaPack
import persona.core.designsystem.LocalPersonaStore
import persona.core.designsystem.PersonaFace
import persona.core.designsystem.PersonaState
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.SharedMotion
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.feature.chat.StreamingReplyMarkdown

/** Wraps the Digital Assistant overlay in Persona's active theme, appearance, feedback, and safe link handler. */
@Composable
internal fun AssistOverlayEnvironment(
    onOpenExternalUri: (String) -> Unit,
    content: @Composable () -> Unit,
) {
    val context = LocalContext.current
    val app = context.applicationContext as? PersonaApp
    val currentStyle = app?.appearance?.style?.collectAsState()?.value ?: AppStyle.Default
    val face = app?.appearance?.face?.collectAsState()?.value
    val pack = app?.personas?.current?.collectAsState()?.value
    val openExternal by rememberUpdatedState(onOpenExternalUri)
    val failedMessage = stringResource(R.string.app_error_failed)
    val feedback = remember(context, failedMessage) {
        object : AppFeedback {
            override fun failure(error: Throwable, operation: Boolean): Boolean =
                notice(error.message ?: failedMessage)

            override fun notice(message: String): Boolean {
                Toast.makeText(context.applicationContext, message, Toast.LENGTH_SHORT).show()
                return true
            }
        }
    }
    val uriHandler = remember {
        object : UriHandler {
            override fun openUri(uri: String) {
                val parsed = runCatching { Uri.parse(uri) }.getOrNull() ?: return
                val scheme = parsed.scheme?.lowercase()
                if ((scheme == "https" || scheme == "http") && !parsed.host.isNullOrBlank()) {
                    openExternal(uri)
                }
            }
        }
    }
    PersonaTheme(style = currentStyle) {
        val providers = buildList<androidx.compose.runtime.ProvidedValue<*>> {
            if (app != null) {
                add(LocalAppStyleStore provides app.appearance)
                add(LocalPersonaStore provides app.personas)
            }
            if (face != null) add(LocalPersonaDesign provides face)
            add(LocalPersonaPack provides pack)
            add(LocalPersonaName provides (pack?.manifest?.name ?: "Persona"))
            add(LocalAppFeedback provides feedback)
            add(LocalUriHandler provides uriHandler)
        }
        CompositionLocalProvider(*providers.toTypedArray(), content = content)
    }
}

/**
 * Unified floating quick-ask card shown over the foreground app when Persona is invoked as
 * Android's Default Digital Assistant (`ROLE_ASSISTANT`) or via `ACTION_ASSIST`.
 *
 * Adapts natively to Persona's three official styles (`AppStyle.Glass`, `AppStyle.Brief`,
 * `AppStyle.Board`), active `ThemeColor`, and `LayoutDensity`:
 * - Single clean opaque composer card (`~125dp` tall) integrating a subtle top drag handle
 *   (also an accessible `Open in Persona` action), `PersonaFace` + borderless multi-line input
 *   (long-press toggles voice dictation when supported) on the top row, and a minimalist 3-control
 *   bottom row: privacy-first screen toggle, restrained model switcher, and high-contrast Send button.
 * - Independent single-layer floating cards (streaming reply transcript, model picker, Hub/error
 *   notices) grow cleanly `8dp` above the unified composer card only when needed — zero nested boxes.
 * - Bidirectional vertical drag: swipe down to dismiss, swipe up to expand directly into full-screen
 *   Persona carrying the current draft, attached screen, and conversation target.
 */
@Composable
internal fun AssistOverlaySheet(
    ui: AssistOverlayUiState,
    onDraftChange: (String) -> Unit,
    onDictationHeard: (String) -> Unit,
    onNotice: (String) -> Unit,
    onAttachScreen: () -> Unit,
    onRemoveScreen: () -> Unit,
    onOpenAssistantSettings: () -> Unit = {},
    onToggleModelPicker: () -> Unit = {},
    onCloseModelPicker: () -> Unit = {},
    onRefreshModels: () -> Unit = {},
    onSelectMainConversation: () -> Unit = {},
    onSelectExistingConversation: (AssistConversationOption) -> Unit = {},
    onSelectNewConversation: () -> Unit = {},
    onSelectDefaultModel: () -> Unit = {},
    onSelectModel: (DigitalAssistantModelChoice) -> Unit = {},
    onRetryWithDefaultMain: () -> Unit = {},
    onSend: () -> Unit,
    onOpenInPersona: () -> Unit,
    onDismiss: () -> Unit,
    onUnlock: () -> Unit = onOpenInPersona,
    dictationOverride: AssistDictationController? = null,
) {
    val palette = PersonaTheme.palette
    val shapes = PersonaTheme.shapes
    val style = PersonaTheme.style
    val reduceMotion = PersonaTheme.reduceMotion
    val density = LocalDensity.current
    val haptic = LocalHapticFeedback.current
    val focusManager = LocalFocusManager.current
    val keyboardController = LocalSoftwareKeyboardController.current

    val dismissThresholdPx = with(density) { Tokens.MinTouch.android.toPx() } // 48dp swipe-down dismiss
    val expandThresholdPx = with(density) { (Tokens.MinTouch.android + Tokens.Space.s).toPx() } // 56dp swipe-up open
    val maxUpVisualOffsetPx = with(density) { (Tokens.Size.box120 + Tokens.Space.l).toPx() }
    val enterOffsetPx = with(density) { Tokens.Space.xl.toPx() }

    var entered by remember(ui.invocationId) { mutableStateOf(reduceMotion) }
    LaunchedEffect(ui.invocationId, reduceMotion) {
        entered = true
    }
    val enterProgress by animateFloatAsState(
        targetValue = if (entered) 1f else 0f,
        animationSpec = if (reduceMotion) SharedMotion.instant() else SharedMotion.reveal(),
        label = "assist-overlay-enter",
    )

    val dictation = dictationOverride ?: rememberOverlayDictation(onHeard = onDictationHeard, onError = onNotice)
    val dictating = dictation?.listening == true
    val dictationPartialText = dictation?.partialText.orEmpty()
    val latestDictation by rememberUpdatedState(dictation)
    var composerFocused by remember(ui.invocationId) { mutableStateOf(false) }

    var dragging by remember(ui.invocationId) { mutableStateOf(false) }
    var rawDragPx by remember(ui.invocationId) { mutableFloatStateOf(0f) }
    var expandHapticFired by remember(ui.invocationId) { mutableStateOf(false) }
    val expandDragReady = !ui.deviceLocked && dragging && rawDragPx <= -expandThresholdPx
    val targetDragOffsetPx = when {
        rawDragPx >= 0f -> rawDragPx
        else -> {
            val up = -rawDragPx
            val damped = if (up <= expandThresholdPx) {
                up * 0.85f
            } else {
                (expandThresholdPx * 0.85f) + ((up - expandThresholdPx) * 0.35f)
            }
            -damped.coerceAtMost(maxUpVisualOffsetPx)
        }
    }
    val animatedDragOffsetPx by animateFloatAsState(
        targetValue = targetDragOffsetPx,
        animationSpec = if (dragging || reduceMotion) SharedMotion.instant() else SharedMotion.springBack(),
        label = "assist-overlay-drag",
    )

    val aiActive = !ui.deviceLocked && (ui.awaitingReply || ui.sending || ui.turns.any { it.streaming })
    val personaState = when {
        ui.hubStatus == AssistHubStatus.Offline || ui.hubStatus == AssistHubStatus.Unpaired -> PersonaState.Offline
        dictating -> PersonaState.Working
        ui.turns.any { it.streaming } -> PersonaState.Working
        ui.sending || ui.awaitingReply -> PersonaState.Thinking
        else -> PersonaState.Idle
    }
    val glowAlpha = if (aiActive && !reduceMotion) {
        val transition = rememberInfiniteTransition(label = "assist-ai-rim")
        transition.animateFloat(
            initialValue = 0.28f,
            targetValue = 0.64f,
            animationSpec = infiniteRepeatable(
                animation = tween(durationMillis = PersonaTheme.motion.breathe, easing = LinearEasing),
                repeatMode = RepeatMode.Reverse,
            ),
            label = "assist-ai-rim-alpha",
        ).value
    } else if (aiActive) {
        0.50f
    } else {
        0f
    }

    val scrimInteraction = remember { MutableInteractionSource() }
    val latestOnCloseModelPicker by rememberUpdatedState(onCloseModelPicker)
    val latestOnOpenInPersona by rememberUpdatedState(onOpenInPersona)
    val latestOnDismiss by rememberUpdatedState(onDismiss)
    val latestDeviceLocked by rememberUpdatedState(ui.deviceLocked)
    val latestKeyboardController by rememberUpdatedState(keyboardController)
    val latestHaptic by rememberUpdatedState(haptic)
    val closeDescription = stringResource(R.string.app_assist_close)

    // Single unified inset layer: max(IME, navigation bar) when IME is open, navigation bar when closed.
    val imeBottomPx = WindowInsets.ime.getBottom(density)
    val imeOpen = imeBottomPx > 0
    val bottomInsetDp = with(density) {
        WindowInsets.ime.union(WindowInsets.navigationBars).getBottom(density).toDp()
    }
    val topInsetDp = with(density) {
        WindowInsets.statusBars.getTop(density).toDp()
    }
    val horizontalMargin = Tokens.Space.m // 12dp outer margin maximizing comfortable tap area
    val floatingBottomLift = if (imeOpen) {
        Tokens.Space.s + Tokens.Space.xxs // 10dp tight float above IME
    } else {
        Tokens.Space.l // 16dp spatial lift above navigation bar
    }
    val cardGap = Tokens.Space.s // 8dp gap between floating cards when expanded/open

    // Style-aware shapes honoring Glass / Brief / Board + LayoutDensity.radiusScale.
    val cardShape = shapes.panelShape
    val chipShape = shapes.chipShape
    val buttonRadius = shapes.button
    val buttonShape = remember(buttonRadius) { RoundedCornerShape(buttonRadius) }

    // Deep bottom-weighted dimming scrim so the overlay lifts crisply off text-dense apps.
    val scrimBrush = remember(palette.scrim, palette.dark) {
        Brush.verticalGradient(
            0.0f to palette.scrim.copy(alpha = if (palette.dark) 0.40f else 0.26f),
            0.55f to palette.scrim.copy(alpha = if (palette.dark) 0.52f else 0.36f),
            0.80f to palette.scrim.copy(alpha = if (palette.dark) 0.74f else 0.54f),
            1.0f to palette.scrim.copy(alpha = if (palette.dark) 0.86f else 0.66f),
        )
    }

    val verticalDragModifier = Modifier.pointerInput(ui.invocationId, dismissThresholdPx, expandThresholdPx) {
        detectVerticalDragGestures(
            onDragStart = {
                dragging = true
                expandHapticFired = false
            },
            onDragEnd = {
                dragging = false
                val finalDrag = rawDragPx
                rawDragPx = 0f
                expandHapticFired = false
                when {
                    finalDrag >= dismissThresholdPx -> {
                        latestDictation?.cancel()
                        latestKeyboardController?.hide()
                        latestOnDismiss()
                    }
                    !latestDeviceLocked && finalDrag <= -expandThresholdPx -> {
                        latestDictation?.cancel()
                        latestKeyboardController?.hide()
                        latestOnOpenInPersona()
                    }
                }
            },
            onDragCancel = {
                dragging = false
                rawDragPx = 0f
                expandHapticFired = false
            },
            onVerticalDrag = { _, dragAmount ->
                val updated = if (latestDeviceLocked) {
                    (rawDragPx + dragAmount).coerceAtLeast(0f)
                } else {
                    rawDragPx + dragAmount
                }
                rawDragPx = updated
                if (!latestDeviceLocked) {
                    if (updated <= -expandThresholdPx && !expandHapticFired) {
                        expandHapticFired = true
                        latestHaptic.performHapticFeedback(HapticFeedbackType.TextHandleMove)
                    } else if (updated > -expandThresholdPx * 0.75f) {
                        expandHapticFired = false
                    }
                }
            },
        )
    }

    BoxWithConstraints(
        Modifier
            .fillMaxSize()
            .testTag("assist-overlay-root"),
    ) {
        val availableHeight = (maxHeight - bottomInsetDp - topInsetDp - floatingBottomLift - Tokens.Space.l)
            .coerceAtLeast(Tokens.Size.box150)
        val replyMaxHeight = (availableHeight * 0.56f).coerceAtLeast(Tokens.Size.box120)
        val pickerMaxHeight = (availableHeight * if (ui.expanded) 0.40f else 0.48f).coerceAtLeast(Tokens.Size.box120)

        // Full-viewport dimmed backdrop scrim; tapping dismisses cleanly without an X button.
        Box(
            Modifier
                .fillMaxSize()
                .graphicsLayer { alpha = enterProgress }
                .background(scrimBrush)
                .clickable(
                    interactionSource = scrimInteraction,
                    indication = null,
                    role = Role.Button,
                    onClickLabel = closeDescription,
                    onClick = {
                        dictation?.cancel()
                        keyboardController?.hide()
                        onDismiss()
                    },
                )
                .semantics { contentDescription = closeDescription }
                .testTag("assist-overlay-scrim"),
        )

        Box(
            Modifier
                .align(Alignment.BottomCenter)
                .fillMaxWidth()
                .padding(
                    start = horizontalMargin,
                    end = horizontalMargin,
                    bottom = bottomInsetDp + floatingBottomLift,
                )
                .graphicsLayer {
                    val scale = 0.96f + 0.04f * enterProgress
                    scaleX = scale
                    scaleY = scale
                    alpha = enterProgress
                    translationY = ((1f - enterProgress) * enterOffsetPx) + animatedDragOffsetPx
                },
            contentAlignment = Alignment.BottomCenter,
        ) {
            Column(
                Modifier
                    .fillMaxWidth()
                    .widthIn(max = Tokens.Size.readable)
                    .heightIn(max = availableHeight)
                    .testTag("assist-overlay-stack"),
                verticalArrangement = Arrangement.spacedBy(cardGap),
            ) {
                if (ui.deviceLocked) {
                    AssistLockedIsland(
                        shape = cardShape,
                        swipeModifier = verticalDragModifier,
                        onUnlock = {
                            dictation?.cancel()
                            keyboardController?.hide()
                            onUnlock()
                        },
                    )
                } else {
                    // On-demand floating streaming reply card above the unified composer card.
                    if (ui.expanded) {
                        AssistReplyIsland(
                            turns = ui.turns,
                            awaitingReply = ui.awaitingReply,
                            targetConversation = ui.targetConversation,
                            personaState = personaState,
                            shape = cardShape,
                            maxHeight = replyMaxHeight,
                            aiActive = aiActive,
                            glowAlpha = glowAlpha,
                            modelPickerOpen = ui.modelPickerOpen,
                            swipeModifier = verticalDragModifier,
                            onCloseModelPicker = { latestOnCloseModelPicker() },
                            modifier = Modifier
                                .weight(1f, fill = false)
                                .fillMaxWidth(),
                        )
                    }

                    // Single-layer floating Hub connectivity or error notice cards (zero nested borders).
                    AssistFloatingNoticeIslands(
                        ui = ui,
                        cardShape = cardShape,
                        buttonShape = buttonShape,
                        onOpenInPersona = {
                            dictation?.cancel()
                            keyboardController?.hide()
                            onOpenInPersona()
                        },
                        onRetryWithDefaultMain = onRetryWithDefaultMain,
                    )

                    // Single-layer floating Conversation + Model Picker card above the unified composer card.
                    if (ui.modelPickerOpen) {
                        AssistModelPickerIsland(
                            targetConversation = ui.targetConversation,
                            conversations = ui.availableConversations,
                            choice = ui.modelChoice,
                            catalog = ui.modelCatalog,
                            hubStatus = ui.hubStatus,
                            shape = cardShape,
                            maxHeight = pickerMaxHeight,
                            onRefresh = onRefreshModels,
                            onSelectMainConversation = onSelectMainConversation,
                            onSelectExistingConversation = onSelectExistingConversation,
                            onSelectNewConversation = onSelectNewConversation,
                            onSelectDefault = onSelectDefaultModel,
                            onSelectModel = onSelectModel,
                        )
                    }

                    // Unified Flagship Composer Card (drag handle + PersonaFace + input on top; actions on bottom).
                    AssistUnifiedComposerCard(
                        ui = ui,
                        style = style,
                        cardShape = cardShape,
                        chipShape = chipShape,
                        buttonShape = buttonShape,
                        personaState = personaState,
                        dictating = dictating,
                        dictationPartialText = dictationPartialText,
                        focused = composerFocused || dictating,
                        aiActive = aiActive,
                        glowAlpha = glowAlpha,
                        expandDragReady = expandDragReady,
                        swipeModifier = verticalDragModifier,
                        onFocusChange = { isFocused ->
                            composerFocused = isFocused
                            if (isFocused && ui.modelPickerOpen) {
                                latestOnCloseModelPicker()
                            }
                        },
                        onDraftChange = onDraftChange,
                        onAttachScreen = onAttachScreen,
                        onRemoveScreen = onRemoveScreen,
                        onOpenAssistantSettings = onOpenAssistantSettings,
                        onToggleModelPicker = onToggleModelPicker,
                        onCloseModelPicker = { latestOnCloseModelPicker() },
                        onOpenInPersona = {
                            dictation?.cancel()
                            keyboardController?.hide()
                            onOpenInPersona()
                        },
                        onStartVoiceInput = dictation?.let { control ->
                            {
                                keyboardController?.hide()
                                focusManager.clearFocus(force = true)
                                control.start()
                            }
                        },
                        onFinishDictation = dictation?.let { control ->
                            {
                                control.finish()
                            }
                        },
                        onSend = {
                            dictation?.cancel()
                            onSend()
                        },
                    )
                }
            }
        }
    }
}

/**
 * Style-aware opaque floating card surface backed strictly by `PersonaTheme` tokens:
 * - `AppStyle.Glass`: solid `surfaceOpaque` backing + restrained single-layer subtle outline + layered shadow.
 * - `AppStyle.Brief`: pure paper `background` backing (matching `Sheets.kt` so `surface` tonal chips stay
 *   crisp on paper) + borderless at rest + clean elevation shadow.
 * - `AppStyle.Board`: solid `surfaceOpaque` backing + architectural `1dp` `line` border + restrained shadow.
 * Uses `.pointerInput { detectTapGestures(...) }` instead of `.clickable` to preserve child semantics testTags.
 */
private fun Modifier.assistSurfaceCard(
    shape: Shape,
    aiActive: Boolean = false,
    glowAlpha: Float = 0f,
    focused: Boolean = false,
    modelPickerOpen: Boolean = false,
    onCloseModelPicker: (() -> Unit)? = null,
): Modifier = composed {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val fill = if (style == AppStyle.Brief) palette.background else palette.surfaceOpaque
    val shadowElevation = if (style == AppStyle.Board) Tokens.Space.m else Tokens.Space.xl

    val idleBorderWidth = when (style) {
        AppStyle.Board -> Tokens.Size.line
        AppStyle.Glass -> Tokens.Size.hairline
        AppStyle.Brief -> Tokens.Space.flush
    }
    val idleBorderColor = remember(palette.line, palette.dark, style) {
        when (style) {
            AppStyle.Board -> palette.line
            AppStyle.Brief -> palette.line.copy(alpha = 0f)
            AppStyle.Glass -> if (palette.dark) {
                palette.line.copy(alpha = minOf(palette.line.alpha, 0.14f).coerceAtLeast(palette.line.alpha * 0.45f))
            } else {
                palette.line.copy(alpha = minOf(palette.line.alpha, 0.16f).coerceAtLeast(palette.line.alpha * 0.65f))
            }
        }
    }
    val activeBorderWidth = Tokens.Size.line
    val focusedColor = remember(palette.accent, palette.dark, style) {
        when (style) {
            AppStyle.Board -> palette.accent.copy(alpha = if (palette.dark) 0.78f else 0.72f)
            else -> palette.accent.copy(alpha = if (palette.dark) 0.55f else 0.48f)
        }
    }
    val borderBrush: Brush? = remember(
        palette.ai,
        palette.accent,
        idleBorderColor,
        focusedColor,
        style,
        aiActive,
        glowAlpha,
        focused,
    ) {
        when {
            aiActive && style == AppStyle.Glass -> Brush.linearGradient(
                colors = listOf(
                    palette.ai.copy(alpha = glowAlpha),
                    palette.accent.copy(alpha = glowAlpha * 0.82f),
                    idleBorderColor,
                    palette.ai.copy(alpha = glowAlpha * 0.44f),
                ),
            )
            aiActive -> SolidColor(palette.ai.copy(alpha = glowAlpha.coerceAtLeast(0.48f)))
            focused -> SolidColor(focusedColor)
            style == AppStyle.Brief -> null
            else -> SolidColor(idleBorderColor)
        }
    }
    val borderWidth = if (aiActive || focused) activeBorderWidth else idleBorderWidth

    this
        .shadow(
            elevation = shadowElevation,
            shape = shape,
            ambientColor = palette.scrim,
            spotColor = if (aiActive) palette.ai else palette.scrim,
            clip = false,
        )
        .then(
            if (style != AppStyle.Board) {
                Modifier.shadow(
                    elevation = Tokens.Space.xs,
                    shape = shape,
                    ambientColor = palette.scrim,
                    spotColor = palette.scrim,
                    clip = false,
                )
            } else {
                Modifier
            },
        )
        .clip(shape)
        .background(fill)
        .then(
            if (borderBrush != null) {
                Modifier.border(width = borderWidth, brush = borderBrush, shape = shape)
            } else {
                Modifier
            },
        )
        .pointerInput(modelPickerOpen, onCloseModelPicker) {
            detectTapGestures(
                onTap = {
                    if (modelPickerOpen) onCloseModelPicker?.invoke()
                },
            )
        }
}

/** Tonal chip/control fill that stays crisp against the card fill in every Persona style (`Brief` uses `surface` on paper `background`). */
@Composable
private fun assistTonalFill(style: AppStyle): Color {
    val palette = PersonaTheme.palette
    return if (style == AppStyle.Brief) palette.surface else palette.sunken
}

@Composable
private fun AssistUnifiedComposerCard(
    ui: AssistOverlayUiState,
    style: AppStyle,
    cardShape: Shape,
    chipShape: Shape,
    buttonShape: Shape,
    personaState: PersonaState,
    dictating: Boolean,
    dictationPartialText: String,
    focused: Boolean,
    aiActive: Boolean,
    glowAlpha: Float,
    expandDragReady: Boolean,
    swipeModifier: Modifier,
    onFocusChange: (Boolean) -> Unit,
    onDraftChange: (String) -> Unit,
    onAttachScreen: () -> Unit,
    onRemoveScreen: () -> Unit,
    onOpenAssistantSettings: () -> Unit,
    onToggleModelPicker: () -> Unit,
    onCloseModelPicker: () -> Unit,
    onOpenInPersona: () -> Unit,
    onStartVoiceInput: (() -> Unit)?,
    onFinishDictation: (() -> Unit)?,
    onSend: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val reduceMotion = PersonaTheme.reduceMotion
    val haptic = LocalHapticFeedback.current
    val personaName = LocalPersonaName.current
    val tonalFill = assistTonalFill(style)
    val inputEnabled = !ui.sending && ui.hubStatus != AssistHubStatus.Unpaired
    val voiceAvailable = onStartVoiceInput != null && onFinishDictation != null
    val defaultShortLabel = stringResource(R.string.app_assist_model_default_short)
    val newChatLabel = stringResource(R.string.app_assist_conversation_new)
    val newChatShortLabel = stringResource(R.string.app_assist_conversation_new_short)
    val untitledChatLabel = stringResource(R.string.app_assist_conversation_untitled)
    val openInPersonaLabel = stringResource(R.string.app_assist_open_in_persona)
    val dictateLabel = stringResource(R.string.app_assist_dictate)
    val stopDictateLabel = stringResource(R.string.app_assist_dictate_stop)
    val modelCompactLabel = ui.modelChoice.compactLabel
    val modelPillLabel = when (val target = ui.targetConversation) {
        AssistTargetConversation.Main -> if (ui.modelChoice.isDefault) {
            defaultShortLabel
        } else {
            modelCompactLabel.ifBlank { defaultShortLabel }
        }
        AssistTargetConversation.NewChat -> if (ui.modelChoice.isDefault || modelCompactLabel.isBlank()) {
            newChatLabel
        } else {
            stringResource(R.string.app_assist_target_model_format, newChatShortLabel, modelCompactLabel)
        }
        is AssistTargetConversation.Existing -> {
            val chatLabel = target.title.ifBlank { untitledChatLabel }
            if (ui.modelChoice.isDefault || modelCompactLabel.isBlank()) {
                chatLabel
            } else {
                stringResource(R.string.app_assist_target_model_format, chatLabel, modelCompactLabel)
            }
        }
    }
    val modelPillHighlighted = !ui.modelChoice.isDefault || ui.targetConversation !is AssistTargetConversation.Main
    val placeholder = stringResource(
        if (voiceAvailable) {
            R.string.app_assist_input_placeholder_voice
        } else {
            R.string.app_assist_input_placeholder
        },
    )
    val latestInputEnabled by rememberUpdatedState(inputEnabled)
    val latestDictating by rememberUpdatedState(dictating)
    val latestHaptic by rememberUpdatedState(haptic)
    val latestOnStartVoiceInput by rememberUpdatedState(onStartVoiceInput)
    val latestOnFinishDictation by rememberUpdatedState(onFinishDictation)

    val glassOrbBrush = remember(palette.ai, palette.accent, palette.surface, palette.dark) {
        Brush.radialGradient(
            colors = listOf(
                palette.ai.copy(alpha = if (palette.dark) 0.28f else 0.18f),
                palette.accent.copy(alpha = if (palette.dark) 0.10f else 0.06f),
                palette.surface.copy(alpha = 0f),
            ),
        )
    }
    val handleShape = if (style == AppStyle.Board) chipShape else CircleShape

    Column(
        Modifier
            .fillMaxWidth()
            .assistSurfaceCard(
                shape = cardShape,
                aiActive = aiActive,
                glowAlpha = glowAlpha,
                focused = focused,
                modelPickerOpen = ui.modelPickerOpen,
                onCloseModelPicker = onCloseModelPicker,
            )
            .then(swipeModifier)
            .testTag("assist-composer-island")
            .padding(
                start = Tokens.Space.m,
                end = Tokens.Space.m,
                top = Tokens.Space.sm,
                bottom = Tokens.Space.s,
            ),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        // Restrained top drag handle hinting bidirectional drag (pull up for full Persona, down to dismiss),
        // and exposing an accessible tap target for screen-reader users without extra button chrome.
        val dragHandleInteraction = remember { MutableInteractionSource() }
        Box(
            Modifier
                .fillMaxWidth()
                .clickable(
                    enabled = !ui.deviceLocked,
                    interactionSource = dragHandleInteraction,
                    indication = null,
                    role = Role.Button,
                    onClickLabel = openInPersonaLabel,
                    onClick = onOpenInPersona,
                )
                .semantics { contentDescription = openInPersonaLabel }
                .testTag("assist-drag-handle"),
            contentAlignment = Alignment.Center,
        ) {
            Box(
                Modifier
                    .size(width = Tokens.Space.Standard.xxl, height = Tokens.Size.bar)
                    .clip(handleShape)
                    .background(
                        if (expandDragReady) {
                            palette.accent.copy(alpha = 0.72f)
                        } else {
                            palette.ink3.copy(alpha = 0.22f)
                        },
                    ),
            )
        }

        Column(
            Modifier.fillMaxWidth(),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            // Top row: Official PersonaFace identity anchor + spacious borderless multi-line input
            // (long-press toggles voice dictation when supported).
            Row(
                Modifier
                    .fillMaxWidth()
                    .heightIn(min = Tokens.MinTouch.android),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
            ) {
                Box(
                    Modifier
                        .size(Tokens.Size.slimBar) // 40dp borderless identity anchor
                        .then(
                            if (style == AppStyle.Glass) {
                                Modifier
                                    .clip(CircleShape)
                                    .background(glassOrbBrush)
                            } else {
                                Modifier
                            },
                        )
                        .semantics { contentDescription = personaName }
                        .testTag("assist-brand-orb"),
                    contentAlignment = Alignment.Center,
                ) {
                    PersonaFace(
                        state = personaState,
                        size = Tokens.Size.wellXS + Tokens.Space.s, // 36dp crisp PersonaFace
                    )
                }

                Box(
                    Modifier
                        .weight(1f)
                        .then(
                            if (voiceAvailable) {
                                Modifier
                                    .pointerInput(Unit) {
                                        val touchSlop = viewConfiguration.touchSlop
                                        awaitEachGesture {
                                            val down = awaitFirstDown(
                                                requireUnconsumed = false,
                                                pass = PointerEventPass.Initial,
                                            )
                                            if (!latestInputEnabled) return@awaitEachGesture
                                            val startedDictating = latestDictating
                                            // Fire just ahead of BasicTextField's internal long-press selection
                                            // menu while staying well above normal taps.
                                            val longPressTimeout =
                                                (viewConfiguration.longPressTimeoutMillis - 50L).coerceAtLeast(300L)
                                            var movedOrMultiTouch = false
                                            val endedEarly = withTimeoutOrNull(longPressTimeout) {
                                                while (true) {
                                                    val event = awaitPointerEvent(PointerEventPass.Initial)
                                                    if (event.changes.size > 1) {
                                                        movedOrMultiTouch = true
                                                        return@withTimeoutOrNull true
                                                    }
                                                    val change = event.changes.firstOrNull { it.id == down.id }
                                                    if (change == null || !change.pressed) {
                                                        return@withTimeoutOrNull true
                                                    }
                                                    if ((change.position - down.position).getDistance() > touchSlop) {
                                                        movedOrMultiTouch = true
                                                        return@withTimeoutOrNull true
                                                    }
                                                }
                                            }
                                            if (endedEarly == null && !movedOrMultiTouch) {
                                                latestHaptic.performHapticFeedback(HapticFeedbackType.LongPress)
                                                if (startedDictating) {
                                                    latestOnFinishDictation?.invoke()
                                                } else {
                                                    latestOnStartVoiceInput?.invoke()
                                                }
                                                // Swallow pointer events while held so BasicTextField never opens a
                                                // selection menu; releasing all pointers finishes hold-to-speak gracefully.
                                                while (true) {
                                                    val event = awaitPointerEvent(PointerEventPass.Initial)
                                                    event.changes.forEach { it.consume() }
                                                    if (!event.changes.any { it.pressed }) break
                                                }
                                                if (!startedDictating) {
                                                    latestOnFinishDictation?.invoke()
                                                }
                                            }
                                        }
                                    }
                            } else {
                                Modifier
                            },
                        )
                        .padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xs),
                    contentAlignment = Alignment.CenterStart,
                ) {
                    if (dictating && onFinishDictation != null) {
                        val livePreview = remember(ui.draftText, dictationPartialText) {
                            if (dictationPartialText.isNotBlank()) {
                                persona.feature.chat.withDictation(ui.draftText, dictationPartialText)
                            } else {
                                ui.draftText
                            }
                        }
                        val pulseAlpha = if (!reduceMotion) {
                            val transition = rememberInfiniteTransition(label = "assist-dictate-pulse")
                            transition.animateFloat(
                                initialValue = 0.55f,
                                targetValue = 1f,
                                animationSpec = infiniteRepeatable(
                                    animation = tween(
                                        durationMillis = PersonaTheme.motion.breathe,
                                        easing = LinearEasing,
                                    ),
                                    repeatMode = RepeatMode.Reverse,
                                ),
                                label = "assist-dictate-pulse-alpha",
                            ).value
                        } else {
                            1f
                        }
                        Row(
                            Modifier
                                .fillMaxWidth()
                                .heightIn(min = Tokens.MinTouch.ios)
                                .clip(chipShape)
                                .background(palette.accentSoft)
                                .border(Tokens.Size.hairline, palette.accent.copy(alpha = 0.45f), chipShape)
                                .clickable(
                                    role = Role.Button,
                                    onClickLabel = stopDictateLabel,
                                    onClick = onFinishDictation,
                                )
                                .semantics {
                                    contentDescription = stopDictateLabel
                                    selected = true
                                }
                                .testTag("assist-dictate-listening")
                                .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                        ) {
                            Box(
                                Modifier
                                    .size(Tokens.Size.wellXS)
                                    .clip(handleShape)
                                    .background(palette.accent.copy(alpha = 0.16f * pulseAlpha)),
                                contentAlignment = Alignment.Center,
                            ) {
                                JIcon(
                                    icon = PersonaIcons.dictating,
                                    contentDescription = null,
                                    size = Tokens.Size.iconXS,
                                    tint = palette.accent.copy(alpha = pulseAlpha),
                                )
                            }
                            Column(
                                Modifier.weight(1f),
                                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
                            ) {
                                Text(
                                    text = stringResource(R.string.app_assist_dictate_listening),
                                    style = if (livePreview.isNotBlank()) {
                                        type.caption.copy(fontWeight = FontWeight.Medium)
                                    } else {
                                        type.body.copy(fontWeight = FontWeight.Medium)
                                    },
                                    color = palette.accent,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                )
                                if (livePreview.isNotBlank()) {
                                    Text(
                                        text = livePreview,
                                        style = if (dictationPartialText.isNotBlank()) type.body else type.caption,
                                        color = if (dictationPartialText.isNotBlank()) palette.ink else palette.ink2,
                                        maxLines = 2,
                                        overflow = TextOverflow.Ellipsis,
                                        modifier = Modifier.testTag("assist-dictate-partial"),
                                    )
                                }
                            }
                        }
                    } else {
                        if (ui.draftText.isEmpty()) {
                            Text(
                                text = placeholder,
                                style = type.body,
                                color = palette.ink3,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                            )
                        }
                        BasicTextField(
                            value = ui.draftText,
                            onValueChange = onDraftChange,
                            enabled = inputEnabled,
                            textStyle = type.body.copy(color = palette.ink),
                            cursorBrush = SolidColor(palette.accent),
                            maxLines = 4,
                            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Send),
                            keyboardActions = KeyboardActions(onSend = { if (ui.canSend) onSend() }),
                            modifier = Modifier
                                .fillMaxWidth()
                                .onFocusChanged { onFocusChange(it.isFocused) }
                                .then(
                                    if (voiceAvailable && inputEnabled && !dictating) {
                                        Modifier.semantics {
                                            onLongClick(label = dictateLabel) {
                                                latestOnStartVoiceInput?.invoke()
                                                true
                                            }
                                        }
                                    } else {
                                        Modifier
                                    },
                                )
                                .testTag("assist-input-field"),
                        )
                    }
                }
            }

            // Bottom integrated action row: minimalist 3-control bar where left screen toggle and
            // right-aligned model switcher fill remaining space so the fixed 44dp Send button stays
            // strictly flush to the card's right inner edge across all locales and screen widths.
            Row(
                Modifier
                    .fillMaxWidth()
                    .heightIn(min = Tokens.MinTouch.android),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                Box(
                    Modifier.weight(1.1f),
                    contentAlignment = Alignment.CenterStart,
                ) {
                    AssistScreenContextControl(
                        ui = ui,
                        style = style,
                        chipShape = chipShape,
                        tonalFill = tonalFill,
                        onAttachScreen = onAttachScreen,
                        onRemoveScreen = onRemoveScreen,
                        onOpenAssistantSettings = onOpenAssistantSettings,
                    )
                }

                Box(
                    Modifier.weight(1f),
                    contentAlignment = Alignment.CenterEnd,
                ) {
                    AssistModelSwitcherButton(
                        label = modelPillLabel,
                        open = ui.modelPickerOpen,
                        enabled = !ui.sending,
                        isCustom = modelPillHighlighted,
                        style = style,
                        chipShape = chipShape,
                        onClick = onToggleModelPicker,
                    )
                }

                val sendLabel = stringResource(R.string.app_send)
                Box(
                    Modifier
                        .size(Tokens.MinTouch.ios) // 44dp high-contrast Send button
                        .clip(buttonShape)
                        .background(if (ui.canSend) palette.action else tonalFill)
                        .clickable(
                            enabled = ui.canSend && !ui.sending,
                            role = Role.Button,
                            onClickLabel = sendLabel,
                            onClick = onSend,
                        )
                        .semantics { contentDescription = sendLabel }
                        .testTag("assist-send-button"),
                    contentAlignment = Alignment.Center,
                ) {
                    if (ui.sending) {
                        JSpinner(
                            Modifier.size(Tokens.Size.iconXS),
                            color = if (ui.canSend) palette.onAction else palette.ink2,
                        )
                    } else {
                        JIcon(
                            icon = PersonaIcons.send,
                            contentDescription = null,
                            size = Tokens.Size.iconS,
                            tint = if (ui.canSend) palette.onAction else palette.ink3,
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun AssistScreenContextControl(
    ui: AssistOverlayUiState,
    style: AppStyle,
    chipShape: Shape,
    tonalFill: Color,
    onAttachScreen: () -> Unit,
    onRemoveScreen: () -> Unit,
    onOpenAssistantSettings: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type

    when (ui.captureStatus) {
        AssistCaptureStatus.Available, AssistCaptureStatus.Attached -> {
            val attached = ui.captureStatus == AssistCaptureStatus.Attached
            val actionLabel = stringResource(
                if (attached) R.string.app_assist_remove_screen else R.string.app_assist_attach_screen,
            )
            val accentTint = if (style == AppStyle.Glass) palette.ai else palette.accent
            val selectedFill = if (style == AppStyle.Glass) palette.aiSoft else palette.accentSoft

            // Privacy-first toggle chip: pure icon + concise short label + clear checkmark when attached (zero thumbnail).
            Row(
                Modifier
                    .heightIn(min = Tokens.MinTouch.ios) // 44dp comfortable touch target
                    .clip(chipShape)
                    .background(if (attached) selectedFill else tonalFill)
                    .then(
                        if (attached) {
                            Modifier.border(Tokens.Size.hairline, accentTint.copy(alpha = 0.45f), chipShape)
                        } else {
                            Modifier
                        },
                    )
                    .clickable(
                        enabled = !ui.attachingScreen && !ui.sending,
                        role = Role.Checkbox,
                        onClickLabel = actionLabel,
                        onClick = if (attached) onRemoveScreen else onAttachScreen,
                    )
                    .semantics {
                        selected = attached
                        contentDescription = actionLabel
                    }
                    .testTag(if (attached) "assist-screen-attached-row" else "assist-attach-screen-chip")
                    .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                if (ui.attachingScreen) {
                    JSpinner(Modifier.size(Tokens.Size.iconXS), color = accentTint)
                } else {
                    JIcon(
                        icon = PersonaIcons.screen,
                        contentDescription = null,
                        size = Tokens.Size.iconXS,
                        tint = if (attached) accentTint else palette.ink2,
                    )
                }

                Text(
                    text = stringResource(R.string.app_assist_attach_screen_short),
                    style = type.labelSmall.copy(
                        fontWeight = if (attached) FontWeight.SemiBold else FontWeight.Medium,
                    ),
                    color = if (attached) palette.ink else palette.ink2,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false),
                )

                if (attached) {
                    val badgeShape = if (style == AppStyle.Board) chipShape else CircleShape
                    Box(
                        Modifier
                            .size(Tokens.Size.badge)
                            .clip(badgeShape)
                            .background(accentTint)
                            .testTag("assist-screen-attached-badge"),
                        contentAlignment = Alignment.Center,
                    ) {
                        JIcon(
                            icon = PersonaIcons.done,
                            contentDescription = null,
                            size = Tokens.Size.markXS,
                            tint = palette.onAccentFill(accentTint),
                        )
                    }
                }
            }
        }

        AssistCaptureStatus.DisabledBySettings -> {
            val openSettingsLabel = stringResource(R.string.app_assist_screen_open_settings)
            Row(
                Modifier
                    .fillMaxWidth()
                    .testTag("assist-screen-disabled"),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                JIcon(
                    icon = PersonaIcons.settings,
                    contentDescription = null,
                    size = Tokens.Size.markS,
                    tint = palette.ink2,
                )
                Text(
                    text = stringResource(R.string.app_assist_screen_disabled_by_settings),
                    style = type.caption,
                    color = palette.ink2,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false),
                )
                Box(
                    Modifier
                        .heightIn(min = Tokens.MinTouch.ios) // 44dp comfortable touch target
                        .clip(chipShape)
                        .background(palette.accentSoft)
                        .clickable(
                            role = Role.Button,
                            onClickLabel = openSettingsLabel,
                            onClick = onOpenAssistantSettings,
                        )
                        .testTag("assist-open-settings-button")
                        .padding(horizontal = Tokens.Space.s),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        text = openSettingsLabel,
                        style = type.labelSmall.copy(fontWeight = FontWeight.SemiBold),
                        color = palette.accent,
                        maxLines = 1,
                    )
                }
            }
        }

        AssistCaptureStatus.Waiting -> {
            Row(
                Modifier
                    .padding(start = Tokens.Space.xs)
                    .testTag("assist-screen-waiting"),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm),
            ) {
                JSpinner(Modifier.size(Tokens.Size.markS), color = palette.ink3)
                Text(
                    text = stringResource(R.string.app_assist_screen_preparing),
                    style = type.caption,
                    color = palette.ink3,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }

        AssistCaptureStatus.Unavailable -> {
            Row(
                Modifier
                    .padding(start = Tokens.Space.xs)
                    .testTag("assist-screen-unavailable"),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                JIcon(
                    icon = PersonaIcons.privacy,
                    contentDescription = null,
                    size = Tokens.Size.markS,
                    tint = palette.ink3,
                )
                Text(
                    text = stringResource(R.string.app_assist_screen_unavailable),
                    style = type.caption,
                    color = palette.ink3,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }

        AssistCaptureStatus.TimedOut -> {
            Row(
                Modifier
                    .padding(start = Tokens.Space.xs)
                    .testTag("assist-screen-timeout"),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                JIcon(
                    icon = PersonaIcons.time,
                    contentDescription = null,
                    size = Tokens.Size.markS,
                    tint = palette.ink3,
                )
                Text(
                    text = stringResource(R.string.app_assist_screen_timeout),
                    style = type.caption,
                    color = palette.ink3,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }

        AssistCaptureStatus.NotSupported -> {
            if (ui.hubStatus == AssistHubStatus.Connecting) {
                Text(
                    text = stringResource(R.string.app_assist_connecting),
                    style = type.caption,
                    color = palette.ink3,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(start = Tokens.Space.xs),
                )
            } else {
                Spacer(Modifier.size(Tokens.Space.xxs))
            }
        }
    }
}

/**
 * Restrained `JModelPill`-style model selector: frameless at rest (`Main chat ⌄` / `主对话 ⌄`),
 * highlighted with a soft tonal pill when open or pinned to a dedicated quick-window model.
 */
@Composable
private fun AssistModelSwitcherButton(
    label: String,
    open: Boolean,
    enabled: Boolean,
    isCustom: Boolean,
    style: AppStyle,
    chipShape: Shape,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val description = stringResource(R.string.app_assist_model_picker_description, label)
    val highlighted = open || isCustom
    val fill = when {
        highlighted -> palette.accentSoft
        style == AppStyle.Board -> palette.sunken
        else -> palette.surface.copy(alpha = 0f)
    }
    val contentTint = if (highlighted) palette.accent else palette.ink2

    Row(
        modifier
            .heightIn(min = Tokens.MinTouch.ios) // 44dp comfortable touch target
            .clip(chipShape)
            .background(fill)
            .then(
                if (highlighted) {
                    Modifier.border(Tokens.Size.hairline, palette.accent.copy(alpha = 0.45f), chipShape)
                } else {
                    Modifier
                },
            )
            .clickable(
                enabled = enabled,
                role = Role.Button,
                onClickLabel = description,
                onClick = onClick,
            )
            .semantics { contentDescription = description }
            .testTag("assist-model-pill")
            .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
    ) {
        Text(
            text = label,
            style = type.labelSmall.copy(
                fontWeight = if (highlighted) FontWeight.SemiBold else FontWeight.Medium,
            ),
            color = contentTint,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f, fill = false),
        )
        JIcon(
            icon = if (open) PersonaIcons.collapse else PersonaIcons.expand,
            contentDescription = null,
            size = Tokens.Size.markS,
            tint = contentTint,
        )
    }
}

@Composable
private fun AssistReplyIsland(
    turns: List<AssistOverlayTurn>,
    awaitingReply: Boolean,
    targetConversation: AssistTargetConversation,
    personaState: PersonaState,
    shape: Shape,
    maxHeight: Dp,
    aiActive: Boolean,
    glowAlpha: Float,
    modelPickerOpen: Boolean,
    swipeModifier: Modifier,
    onCloseModelPicker: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val personaName = LocalPersonaName.current
    val untitledChatLabel = stringResource(R.string.app_assist_conversation_untitled)
    val replyTargetLabel = when (targetConversation) {
        AssistTargetConversation.Main -> null
        AssistTargetConversation.NewChat -> stringResource(R.string.app_assist_conversation_new)
        is AssistTargetConversation.Existing -> targetConversation.title.ifBlank { untitledChatLabel }
    }

    Column(
        modifier
            .heightIn(max = maxHeight)
            .assistSurfaceCard(
                shape = shape,
                aiActive = aiActive,
                glowAlpha = glowAlpha,
                modelPickerOpen = modelPickerOpen,
                onCloseModelPicker = onCloseModelPicker,
            )
            .animateContentSize(animationSpec = PersonaTheme.motion.size())
            .testTag("assist-reply-island")
            .padding(
                start = Tokens.Space.l,
                end = Tokens.Space.l,
                top = Tokens.Space.s,
                bottom = Tokens.Space.m,
            ),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        // Minimal drag pill + compact Persona brand identity header.
        Column(
            Modifier
                .fillMaxWidth()
                .then(swipeModifier),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.sm),
        ) {
            Box(
                Modifier
                    .padding(top = Tokens.Space.xxs)
                    .size(width = Tokens.Size.slimBar, height = Tokens.Size.bar)
                    .clip(CircleShape)
                    .background(palette.ink3.copy(alpha = 0.28f)),
            )
            Row(
                Modifier
                    .fillMaxWidth()
                    .testTag("assist-reply-brand"),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
            ) {
                PersonaFace(
                    state = personaState,
                    size = Tokens.Size.iconS, // 22dp crisp mini Persona identity
                )
                Text(
                    text = personaName,
                    style = type.labelSmall.copy(fontWeight = FontWeight.SemiBold),
                    color = palette.ink,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                if (!replyTargetLabel.isNullOrBlank()) {
                    Text(
                        text = replyTargetLabel,
                        style = type.caption,
                        color = palette.ink3,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier
                            .weight(1f, fill = false)
                            .testTag("assist-reply-target-label"),
                    )
                }
            }
        }

        AssistStreamingTranscript(
            turns = turns,
            awaitingReply = awaitingReply,
            modifier = Modifier
                .weight(1f, fill = false)
                .fillMaxWidth()
                .testTag("assist-transcript"),
        )
    }
}

@Composable
private fun AssistFloatingNoticeIslands(
    ui: AssistOverlayUiState,
    cardShape: Shape,
    buttonShape: Shape,
    onOpenInPersona: () -> Unit,
    onRetryWithDefaultMain: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val neutralIconBg = assistTonalFill(style)

    when (ui.hubStatus) {
        AssistHubStatus.Unpaired -> AssistSingleLayerNoticeIsland(
            cardShape = cardShape,
            buttonShape = buttonShape,
            icon = PersonaIcons.offline,
            iconTint = palette.offline.text,
            iconBg = neutralIconBg,
            title = stringResource(R.string.app_assist_hub_unpaired_title),
            body = stringResource(R.string.app_assist_hub_unpaired_body),
            actionLabel = stringResource(R.string.app_open_persona),
            onAction = onOpenInPersona,
            tag = "assist-hub-unpaired",
            actionTag = "assist-hub-unpaired-action",
        )

        AssistHubStatus.Offline -> AssistSingleLayerNoticeIsland(
            cardShape = cardShape,
            buttonShape = buttonShape,
            icon = PersonaIcons.offline,
            iconTint = palette.offline.text,
            iconBg = neutralIconBg,
            title = stringResource(R.string.app_assist_hub_offline_title),
            body = stringResource(R.string.app_assist_hub_offline_body),
            actionLabel = stringResource(R.string.app_open_persona),
            onAction = onOpenInPersona,
            tag = "assist-hub-offline",
            actionTag = "assist-hub-offline-action",
        )

        else -> Unit
    }

    ui.errorMessage?.let { error ->
        AssistSingleLayerNoticeIsland(
            cardShape = cardShape,
            buttonShape = buttonShape,
            icon = PersonaIcons.error,
            iconTint = palette.danger.text,
            iconBg = palette.danger.tint,
            title = null,
            body = error,
            actionLabel = if (ui.modelFallbackAvailable) {
                stringResource(
                    if (ui.targetConversation is AssistTargetConversation.Main) {
                        R.string.app_assist_model_fallback_main
                    } else {
                        R.string.app_assist_model_fallback_default
                    },
                )
            } else {
                null
            },
            onAction = if (ui.modelFallbackAvailable) onRetryWithDefaultMain else null,
            tag = "assist-error-notice",
            actionTag = "assist-error-action",
        )
    }
}

/** Single-layer floating notice card (zero nested boxes) inheriting the active Persona style. */
@Composable
private fun AssistSingleLayerNoticeIsland(
    cardShape: Shape,
    buttonShape: Shape,
    icon: PhIcon,
    iconTint: Color,
    iconBg: Color,
    title: String?,
    body: String,
    actionLabel: String?,
    onAction: (() -> Unit)?,
    tag: String,
    actionTag: String,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type

    Column(
        Modifier
            .fillMaxWidth()
            .assistSurfaceCard(shape = cardShape)
            .testTag(tag)
            .padding(Tokens.Space.l),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.m),
    ) {
        Row(
            Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
        ) {
            Box(
                Modifier
                    .size(Tokens.Size.slimBar)
                    .clip(buttonShape)
                    .background(iconBg),
                contentAlignment = Alignment.Center,
            ) {
                JIcon(
                    icon = icon,
                    contentDescription = null,
                    size = Tokens.Size.iconXS,
                    tint = iconTint,
                )
            }
            Column(
                Modifier.weight(1f),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
            ) {
                if (!title.isNullOrBlank()) {
                    Text(
                        text = title,
                        style = type.labelSmall.copy(fontWeight = FontWeight.SemiBold),
                        color = palette.ink,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
                Text(
                    text = body,
                    style = type.caption,
                    color = palette.ink2,
                    maxLines = 3,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
        if (!actionLabel.isNullOrBlank() && onAction != null) {
            Box(
                Modifier
                    .fillMaxWidth()
                    .heightIn(min = Tokens.MinTouch.ios) // 44dp borderless soft tonal action button
                    .clip(buttonShape)
                    .background(palette.accentSoft)
                    .clickable(
                        role = Role.Button,
                        onClickLabel = actionLabel,
                        onClick = onAction,
                    )
                    .testTag(actionTag)
                    .padding(horizontal = Tokens.Space.l),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    text = actionLabel,
                    style = type.labelSmall.copy(fontWeight = FontWeight.SemiBold),
                    color = palette.accent,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
    }
}

@Composable
private fun AssistModelPickerIsland(
    targetConversation: AssistTargetConversation,
    conversations: List<AssistConversationOption>,
    choice: DigitalAssistantModelChoice,
    catalog: DigitalAssistantModelCatalog,
    hubStatus: AssistHubStatus,
    shape: Shape,
    maxHeight: Dp,
    onRefresh: () -> Unit,
    onSelectMainConversation: () -> Unit,
    onSelectExistingConversation: (AssistConversationOption) -> Unit,
    onSelectNewConversation: () -> Unit,
    onSelectDefault: () -> Unit,
    onSelectModel: (DigitalAssistantModelChoice) -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val style = PersonaTheme.style
    val selectedDescription = stringResource(R.string.app_assist_model_selected)
    val untitledChatLabel = stringResource(R.string.app_assist_conversation_untitled)
    val isMainTarget = targetConversation is AssistTargetConversation.Main

    Column(
        Modifier
            .fillMaxWidth()
            .heightIn(max = maxHeight)
            .assistSurfaceCard(shape = shape)
            .testTag("assist-model-picker")
            .padding(vertical = Tokens.Space.s),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        Row(
            Modifier
                .fillMaxWidth()
                .padding(horizontal = Tokens.Space.m),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            Text(
                text = stringResource(R.string.app_assist_model_picker_title),
                style = type.caption.copy(fontWeight = FontWeight.SemiBold),
                color = palette.ink2,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            if (hubStatus == AssistHubStatus.Ready && !catalog.loading) {
                JIconButton(
                    icon = PersonaIcons.retry,
                    contentDescription = stringResource(R.string.app_refresh),
                    onClick = onRefresh,
                    tint = palette.ink2,
                    modifier = Modifier.testTag("assist-model-refresh"),
                )
            }
        }
        JDivider()

        // Compact horizontal target conversation selector (Main / New chat / recent non-archived chats).
        Column(
            Modifier
                .fillMaxWidth()
                .testTag("assist-conversation-strip"),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        ) {
            Text(
                text = stringResource(R.string.app_assist_conversation_section_title),
                style = type.micro.copy(fontWeight = FontWeight.SemiBold),
                color = palette.ink3,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.padding(horizontal = Tokens.Space.m),
            )
            LazyRow(
                Modifier.fillMaxWidth(),
                contentPadding = PaddingValues(horizontal = Tokens.Space.m),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                item(key = "assist-conv-main") {
                    AssistConversationChip(
                        label = stringResource(R.string.app_assist_conversation_main),
                        icon = PersonaIcons.home,
                        isSelected = isMainTarget,
                        working = false,
                        style = style,
                        onClick = onSelectMainConversation,
                        modifier = Modifier.testTag("assist-conversation-option-main"),
                    )
                }
                item(key = "assist-conv-new") {
                    AssistConversationChip(
                        label = stringResource(R.string.app_assist_conversation_new),
                        icon = PersonaIcons.newConversation,
                        isSelected = targetConversation is AssistTargetConversation.NewChat,
                        working = false,
                        style = style,
                        onClick = onSelectNewConversation,
                        modifier = Modifier.testTag("assist-conversation-option-new"),
                    )
                }
                items(
                    items = conversations,
                    key = { "assist-conv-${it.key.path}" },
                ) { option ->
                    val optionSelected = (targetConversation as? AssistTargetConversation.Existing)?.key == option.key
                    AssistConversationChip(
                        label = option.title.ifBlank { untitledChatLabel },
                        icon = PersonaIcons.conversation,
                        isSelected = optionSelected,
                        working = option.working,
                        style = style,
                        onClick = { onSelectExistingConversation(option) },
                        modifier = Modifier.testTag("assist-conversation-option-${option.key.sessionId}"),
                    )
                }
            }
        }

        JDivider()

        LazyColumn(
            Modifier
                .weight(1f, fill = false)
                .fillMaxWidth(),
            contentPadding = PaddingValues(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
        ) {
            item(key = "assist-model-section-header") {
                Text(
                    text = stringResource(R.string.app_assist_model_section_title),
                    style = type.micro.copy(fontWeight = FontWeight.SemiBold),
                    color = palette.ink3,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(
                        start = Tokens.Space.s,
                        end = Tokens.Space.s,
                        bottom = Tokens.Space.xxs,
                    ),
                )
            }
            item(key = "assist-model-default") {
                AssistModelPickerRow(
                    title = stringResource(
                        if (isMainTarget) {
                            R.string.app_assist_model_default_title
                        } else {
                            R.string.app_assist_model_follow_default_title
                        },
                    ),
                    subtitle = stringResource(
                        if (isMainTarget) {
                            R.string.app_assist_model_default_subtitle
                        } else {
                            R.string.app_assist_model_follow_default_subtitle
                        },
                    ),
                    selected = choice.isDefault,
                    selectedDescription = selectedDescription,
                    onClick = onSelectDefault,
                    modifier = Modifier.testTag("assist-model-option-default"),
                )
            }
            when {
                catalog.loading -> {
                    item(key = "assist-model-loading") {
                        Row(
                            Modifier
                                .fillMaxWidth()
                                .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.s),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                        ) {
                            JSpinner(Modifier.size(Tokens.Size.markS), color = palette.accent)
                            Text(
                                text = stringResource(R.string.app_assist_model_loading),
                                style = type.caption,
                                color = palette.ink3,
                            )
                        }
                    }
                }

                catalog.error != null -> {
                    item(key = "assist-model-error") {
                        Text(
                            text = catalog.error.orEmpty(),
                            style = type.caption,
                            color = palette.ink3,
                            modifier = Modifier.padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.s),
                        )
                    }
                }

                catalog.groups.isEmpty() -> {
                    item(key = "assist-model-empty") {
                        Text(
                            text = stringResource(R.string.app_assist_model_empty),
                            style = type.caption,
                            color = palette.ink3,
                            modifier = Modifier.padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.s),
                        )
                    }
                }

                else -> {
                    catalog.groups.forEach { group ->
                        item(key = "assist-model-group-${group.launcher}") {
                            Text(
                                text = group.title,
                                style = type.micro.copy(fontWeight = FontWeight.SemiBold),
                                color = palette.ink3,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                                modifier = Modifier.padding(
                                    start = Tokens.Space.s,
                                    end = Tokens.Space.s,
                                    top = Tokens.Space.s,
                                    bottom = Tokens.Space.xxs,
                                ),
                            )
                        }
                        items(
                            items = group.options,
                            key = { "assist-model-opt-${it.pick.launcher}-${it.pick.model.orEmpty()}" },
                        ) { option ->
                            AssistModelPickerRow(
                                title = option.modelLabel.ifBlank { group.title },
                                subtitle = option.description,
                                selected = choice.pick == option.pick,
                                selectedDescription = selectedDescription,
                                onClick = { onSelectModel(option.toChoice()) },
                                modifier = Modifier.testTag(
                                    "assist-model-option-${option.pick.launcher}-${option.pick.model.orEmpty()}",
                                ),
                            )
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun AssistConversationChip(
    label: String,
    icon: PhIcon,
    isSelected: Boolean,
    working: Boolean,
    style: AppStyle,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val chipShape = PersonaTheme.shapes.chipShape
    val fill = if (isSelected) palette.accentSoft else assistTonalFill(style)
    val contentTint = if (isSelected) palette.accent else palette.ink2

    Row(
        modifier
            .heightIn(min = Tokens.MinTouch.ios)
            .widthIn(max = Tokens.Size.box150)
            .clip(chipShape)
            .background(fill)
            .then(
                if (isSelected) {
                    Modifier.border(Tokens.Size.hairline, palette.accent.copy(alpha = 0.45f), chipShape)
                } else {
                    Modifier
                },
            )
            .clickable(
                role = Role.Button,
                onClickLabel = label,
                onClick = onClick,
            )
            .semantics { selected = isSelected }
            .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        if (working) {
            JSpinner(Modifier.size(Tokens.Size.markS), color = contentTint)
        } else {
            JIcon(
                icon = icon,
                contentDescription = null,
                size = Tokens.Size.markS,
                tint = contentTint,
            )
        }
        Text(
            text = label,
            style = type.labelSmall.copy(
                fontWeight = if (isSelected) FontWeight.SemiBold else FontWeight.Medium,
            ),
            color = if (isSelected) palette.accent else palette.ink,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

@Composable
private fun AssistModelPickerRow(
    title: String,
    subtitle: String?,
    selected: Boolean,
    selectedDescription: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val shapes = PersonaTheme.shapes
    Row(
        modifier
            .fillMaxWidth()
            .heightIn(min = Tokens.MinTouch.ios)
            .clip(shapes.controlShape)
            .background(if (selected) palette.accentSoft else palette.surface.copy(alpha = 0f))
            .clickable(role = Role.Button, onClick = onClick)
            .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        Column(
            Modifier.weight(1f),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
        ) {
            Text(
                text = title,
                style = type.labelSmall.copy(
                    fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Medium,
                ),
                color = if (selected) palette.accent else palette.ink,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            if (!subtitle.isNullOrBlank()) {
                Text(
                    text = subtitle,
                    style = type.caption,
                    color = palette.ink3,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
        if (selected) {
            JIcon(
                icon = PersonaIcons.done,
                contentDescription = selectedDescription,
                size = Tokens.Size.markS,
                tint = palette.accent,
            )
        }
    }
}

@Composable
private fun AssistLockedIsland(
    shape: Shape,
    swipeModifier: Modifier,
    onUnlock: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Column(
        Modifier
            .fillMaxWidth()
            .assistSurfaceCard(shape = shape)
            .then(swipeModifier)
            .testTag("assist-locked-card")
            .padding(Tokens.Space.l),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.m),
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            JIcon(
                icon = PersonaIcons.locked,
                contentDescription = null,
                size = Tokens.Size.iconXS,
                tint = palette.accent,
            )
            Text(
                text = stringResource(R.string.app_assist_locked_title),
                style = type.label.copy(fontWeight = FontWeight.SemiBold),
                color = palette.ink,
            )
        }
        Text(
            text = stringResource(R.string.app_assist_locked_body),
            style = type.caption,
            color = palette.ink2,
        )
        JButton(
            text = stringResource(R.string.app_assist_unlock_action),
            onClick = onUnlock,
            size = JButtonSize.Small,
            modifier = Modifier
                .fillMaxWidth()
                .testTag("assist-unlock-button"),
        )
    }
}

@Composable
private fun AssistStreamingTranscript(
    turns: List<AssistOverlayTurn>,
    awaitingReply: Boolean,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val listState = rememberLazyListState()
    var followBottom by remember { mutableStateOf(true) }

    LaunchedEffect(listState) {
        snapshotFlow {
            val info = listState.layoutInfo
            val lastVisible = info.visibleItemsInfo.lastOrNull()?.index ?: -1
            val total = info.totalItemsCount
            total == 0 || lastVisible >= total - 2
        }.collect { nearBottom ->
            followBottom = nearBottom
        }
    }

    val lastTurnTextLen = turns.lastOrNull()?.text?.length ?: 0
    LaunchedEffect(turns.size, lastTurnTextLen, awaitingReply) {
        if (followBottom && (turns.isNotEmpty() || awaitingReply)) {
            val target = (turns.size + if (awaitingReply) 1 else 0) - 1
            if (target >= 0) {
                listState.scrollToItem(target)
            }
        }
    }

    LazyColumn(
        state = listState,
        modifier = modifier,
        contentPadding = PaddingValues(vertical = Tokens.Space.xs),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.m),
    ) {
        items(turns, key = { it.id }) { turn ->
            if (turn.fromUser) {
                Column(
                    Modifier.fillMaxWidth(),
                    horizontalAlignment = Alignment.End,
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                ) {
                    Column(
                        Modifier
                            .clip(PersonaTheme.shapes.cardShape)
                            .background(palette.userBubble)
                            .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
                        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                    ) {
                        if (turn.hasScreenAttachment) {
                            Row(
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                            ) {
                                JIcon(
                                    icon = PersonaIcons.screen,
                                    contentDescription = null,
                                    size = Tokens.Size.markS,
                                    tint = palette.userBubbleText ?: palette.ink2,
                                )
                                Text(
                                    text = stringResource(R.string.app_assist_screen_attached),
                                    style = type.micro,
                                    color = palette.userBubbleText ?: palette.ink2,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                )
                            }
                        }
                        if (turn.text.isNotBlank()) {
                            Text(
                                text = turn.text,
                                style = type.body,
                                color = palette.userBubbleText ?: palette.ink,
                            )
                        }
                    }
                    val deliveryLabel = when (turn.delivery) {
                        AssistTurnDelivery.Sending -> stringResource(R.string.app_assist_delivery_sending)
                        AssistTurnDelivery.Queued -> stringResource(R.string.app_assist_delivery_queued)
                        AssistTurnDelivery.Failed -> stringResource(R.string.app_assist_delivery_failed)
                        AssistTurnDelivery.Sent -> null
                    }
                    if (deliveryLabel != null) {
                        Text(
                            text = deliveryLabel,
                            style = type.micro,
                            color = if (turn.delivery == AssistTurnDelivery.Failed) palette.danger.text else palette.ink3,
                            maxLines = 1,
                        )
                    }
                }
            } else {
                Column(
                    Modifier
                        .fillMaxWidth()
                        .padding(vertical = Tokens.Space.xxs),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                ) {
                    StreamingReplyMarkdown(
                        text = turn.text,
                        id = turn.id,
                        streaming = turn.streaming,
                        bodyStyle = type.body,
                        color = palette.ink,
                        selectable = false,
                    )
                }
            }
        }
        if (awaitingReply) {
            item(key = "assist-awaiting-reply") {
                Row(
                    Modifier
                        .fillMaxWidth()
                        .padding(vertical = Tokens.Space.xs),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                ) {
                    JSpinner(Modifier.size(Tokens.Size.markS), color = palette.accent)
                    Text(
                        text = stringResource(R.string.app_assist_replying),
                        style = type.caption,
                        color = palette.ink2,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
        }
    }
}

internal const val OVERLAY_DICTATION_FINISH_TIMEOUT_MS = 2_500L
private const val EXTRA_UNSTABLE_TEXT = "android.speech.extra.UNSTABLE_TEXT"

private fun extractResultsWords(results: Bundle?): String =
    results?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)
        ?.firstOrNull { it.isNotBlank() }?.trim().orEmpty()

private fun extractPartialWords(partialResults: Bundle?): String {
    if (partialResults == null) return ""
    val stable = extractResultsWords(partialResults)
    val unstable = partialResults.getStringArrayList(EXTRA_UNSTABLE_TEXT)
        ?.firstOrNull { it.isNotBlank() }?.trim().orEmpty()
    return when {
        unstable.isEmpty() -> stable
        stable.isEmpty() -> unstable
        stable.endsWith(unstable) -> stable
        else -> persona.feature.chat.withDictation(stable, unstable)
    }
}

private fun defaultMainTimeoutScheduler(): (Long, () -> Unit) -> () -> Unit = { delayMs, onTimeout ->
    val handler = Handler(Looper.getMainLooper())
    val runnable = Runnable { onTimeout() }
    val _ = handler.postDelayed(runnable, delayMs)
    val cancel: () -> Unit = { handler.removeCallbacks(runnable) }
    cancel
}

/** Controls voice dictation inside the Digital Assistant overlay without requiring Activity result APIs. */
internal interface AssistDictationController {
    val listening: Boolean
    val partialText: String get() = ""
    fun start()
    fun finish()
    fun cancel()
}

/** Minimal engine contract around [SpeechRecognizer] so dictation fallback and lifecycle logic are unit-testable. */
internal interface OverlaySpeechEngine {
    fun setRecognitionListener(listener: RecognitionListener)
    fun startListening(intent: Intent)
    fun stopListening()
    fun destroy()
}

private class PlatformOverlaySpeechEngine(
    private val delegate: SpeechRecognizer,
) : OverlaySpeechEngine {
    override fun setRecognitionListener(listener: RecognitionListener) = delegate.setRecognitionListener(listener)
    override fun startListening(intent: Intent) = delegate.startListening(intent)
    override fun stopListening() = delegate.stopListening()
    override fun destroy() = delegate.destroy()
}

/**
 * Overlay-safe voice dictation helper that never requires `LocalActivityResultRegistryOwner`
 * (which is unavailable inside a `VoiceInteractionSession`). Activates only when `RECORD_AUDIO`
 * is already granted and platform speech recognition is available; hides automatically otherwise.
 *
 * Separates graceful [finish] (stops mic capture and waits up to [finishTimeoutMs] for final
 * `onResults`, falling back to latest non-empty [partialText] if the engine times out or emits
 * `ERROR_NO_MATCH` / `ERROR_SPEECH_TIMEOUT`) from immediate [cancel] (tears down and destroys
 * immediately, dropping any late callbacks).
 *
 * When started with the on-device recognizer on API 31+, seamlessly falls back once per handle
 * session to the system default recognizer (`SpeechRecognizer.createSpeechRecognizer`) if the
 * on-device engine does not support or have the requested locale (`ERROR_LANGUAGE_NOT_SUPPORTED` /
 * `ERROR_LANGUAGE_UNAVAILABLE`) or fails to initialize/start while a system recognizer is available.
 */
internal class OverlayDictationHandle(
    private val preferOnDevice: Boolean,
    private val hasSystemRecognizer: () -> Boolean,
    private val createOnDeviceEngine: () -> OverlaySpeechEngine?,
    private val createSystemEngine: () -> OverlaySpeechEngine?,
    private val intent: Intent,
    private val onHeard: () -> (String) -> Unit,
    private val onError: () -> (String) -> Unit,
    private val stoppedMessage: String,
    private val finishTimeoutMs: Long = OVERLAY_DICTATION_FINISH_TIMEOUT_MS,
    private val scheduleTimeout: (Long, () -> Unit) -> () -> Unit = defaultMainTimeoutScheduler(),
) : AssistDictationController {
    private enum class State { Idle, Listening, Finishing }

    private var state by mutableStateOf(State.Idle)

    override val listening: Boolean
        get() = state != State.Idle

    override var partialText by mutableStateOf("")
        private set

    private var recognizer: OverlaySpeechEngine? = null
    private var activeAttemptToken: Int = 0
    private var fallenBackToSystem: Boolean = false
    private var pendingFinishTimeout: (() -> Unit)? = null

    override fun start() {
        if (state == State.Listening) return
        if (state == State.Finishing) {
            completeFromFinishing(deliverPartial = true)
        }
        startEngine(useOnDevice = preferOnDevice && !fallenBackToSystem)
    }

    override fun finish() {
        if (state != State.Listening) return
        val current = recognizer
        if (current == null) {
            completeFromFinishing(deliverPartial = true)
            return
        }
        val token = activeAttemptToken
        state = State.Finishing
        cancelPendingTimeout()
        pendingFinishTimeout = scheduleTimeout(finishTimeoutMs) {
            if (activeAttemptToken == token && state == State.Finishing) {
                completeFromFinishing(deliverPartial = true)
            }
        }
        val stopped = runCatching {
            current.stopListening()
            true
        }.getOrDefault(false)
        if (!stopped && activeAttemptToken == token && state == State.Finishing) {
            completeFromFinishing(deliverPartial = true)
        }
    }

    override fun cancel() {
        teardown(stopBeforeDestroy = true)
    }

    private fun startEngine(useOnDevice: Boolean) {
        teardown(stopBeforeDestroy = true)
        val speech = runCatching {
            if (useOnDevice) createOnDeviceEngine() else createSystemEngine()
        }.getOrNull()
        if (speech == null) {
            if (canFallbackToSystem(useOnDevice)) {
                fallenBackToSystem = true
                startEngine(useOnDevice = false)
            } else {
                state = State.Idle
                partialText = ""
                onError().invoke(stoppedMessage)
            }
            return
        }
        val token = ++activeAttemptToken
        recognizer = speech
        partialText = ""
        state = State.Listening
        val started = runCatching {
            speech.setRecognitionListener(createListener(token = token, usingOnDevice = useOnDevice))
            speech.startListening(intent)
            true
        }.getOrDefault(false)
        if (!started) {
            if (activeAttemptToken != token) return
            if (canFallbackToSystem(useOnDevice)) {
                fallenBackToSystem = true
                startEngine(useOnDevice = false)
            } else {
                teardown(stopBeforeDestroy = true)
                onError().invoke(stoppedMessage)
            }
        }
    }

    private fun createListener(token: Int, usingOnDevice: Boolean): RecognitionListener =
        object : RecognitionListener {
            override fun onPartialResults(partialResults: Bundle?) {
                if (activeAttemptToken != token) return
                val words = extractPartialWords(partialResults)
                if (words.isNotEmpty()) {
                    partialText = words
                }
            }

            override fun onResults(results: Bundle?) {
                if (activeAttemptToken != token) return
                val words = extractResultsWords(results).ifEmpty { partialText.trim() }
                teardown(stopBeforeDestroy = false)
                if (words.isNotEmpty()) onHeard().invoke(words)
            }

            override fun onError(error: Int) {
                if (activeAttemptToken != token) return
                if (canFallbackToSystem(usingOnDevice) && isOnDeviceFallbackError(error)) {
                    fallenBackToSystem = true
                    if (state == State.Listening) {
                        startEngine(useOnDevice = false)
                    } else {
                        completeFromFinishing(deliverPartial = true)
                    }
                    return
                }
                val rescuePartial = state == State.Finishing ||
                    error == SpeechRecognizer.ERROR_NO_MATCH ||
                    error == SpeechRecognizer.ERROR_SPEECH_TIMEOUT
                val fallbackWords = if (rescuePartial) partialText.trim() else ""
                teardown(stopBeforeDestroy = false)
                if (fallbackWords.isNotEmpty()) {
                    onHeard().invoke(fallbackWords)
                    return
                }
                if (error != SpeechRecognizer.ERROR_NO_MATCH && error != SpeechRecognizer.ERROR_SPEECH_TIMEOUT) {
                    onError().invoke(stoppedMessage)
                }
            }

            override fun onReadyForSpeech(params: Bundle?) = Unit
            override fun onBeginningOfSpeech() = Unit
            override fun onRmsChanged(rmsdB: Float) = Unit
            override fun onBufferReceived(buffer: ByteArray?) = Unit
            override fun onEndOfSpeech() = Unit
            override fun onEvent(eventType: Int, params: Bundle?) = Unit
        }

    private fun canFallbackToSystem(usingOnDevice: Boolean): Boolean =
        usingOnDevice && !fallenBackToSystem && runCatching { hasSystemRecognizer() }.getOrDefault(false)

    private fun isOnDeviceFallbackError(error: Int): Boolean =
        Build.VERSION.SDK_INT >= 31 && (
            error == SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED ||
                error == SpeechRecognizer.ERROR_LANGUAGE_UNAVAILABLE
            )

    private fun completeFromFinishing(deliverPartial: Boolean) {
        val words = if (deliverPartial) partialText.trim() else ""
        teardown(stopBeforeDestroy = false)
        if (words.isNotEmpty()) onHeard().invoke(words)
    }

    private fun cancelPendingTimeout() {
        val cancelTimer = pendingFinishTimeout
        pendingFinishTimeout = null
        cancelTimer?.invoke()
    }

    private fun teardown(stopBeforeDestroy: Boolean) {
        activeAttemptToken++
        cancelPendingTimeout()
        state = State.Idle
        partialText = ""
        val current = recognizer
        recognizer = null
        if (current != null) {
            if (stopBeforeDestroy) {
                val _ = runCatching { current.stopListening() }
            }
            val _ = runCatching { current.destroy() }
        }
    }

    companion object {
        internal fun forContext(
            context: Context,
            preferOnDevice: Boolean,
            intent: Intent,
            onHeard: () -> (String) -> Unit,
            onError: () -> (String) -> Unit,
            stoppedMessage: String,
        ): OverlayDictationHandle = OverlayDictationHandle(
            preferOnDevice = preferOnDevice,
            hasSystemRecognizer = { SpeechRecognizer.isRecognitionAvailable(context) },
            createOnDeviceEngine = {
                if (Build.VERSION.SDK_INT >= 31) {
                    PlatformOverlaySpeechEngine(SpeechRecognizer.createOnDeviceSpeechRecognizer(context))
                } else {
                    null
                }
            },
            createSystemEngine = {
                PlatformOverlaySpeechEngine(SpeechRecognizer.createSpeechRecognizer(context))
            },
            intent = intent,
            onHeard = onHeard,
            onError = onError,
            stoppedMessage = stoppedMessage,
        )
    }
}

@Composable
private fun rememberOverlayDictation(
    onHeard: (String) -> Unit,
    onError: (String) -> Unit,
): AssistDictationController? {
    val context = LocalContext.current
    val resources = LocalResources.current
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val latestHeard by rememberUpdatedState(onHeard)
    val latestError by rememberUpdatedState(onError)
    val stoppedMessage = stringResource(R.string.app_assist_dictate_stopped)

    val granted = remember(context) {
        ContextCompat.checkSelfPermission(context, Manifest.permission.RECORD_AUDIO) ==
            PackageManager.PERMISSION_GRANTED
    }
    if (!granted) return null

    val onDevice = remember(context) {
        Build.VERSION.SDK_INT >= 31 && SpeechRecognizer.isOnDeviceRecognitionAvailable(context)
    }
    val available = remember(context, onDevice) {
        onDevice || SpeechRecognizer.isRecognitionAvailable(context)
    }
    if (!available) return null

    val locale = remember(resources) { resources.configuration.locales[0] }
    val language = remember(locale) {
        if (locale?.language == "zh") "zh-CN" else locale?.toLanguageTag()
    }
    val intent = remember(context.packageName, language) {
        Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH).apply {
            putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
            putExtra(RecognizerIntent.EXTRA_CALLING_PACKAGE, context.packageName)
            putExtra(RecognizerIntent.EXTRA_MAX_RESULTS, 1)
            putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, true)
            language?.let { putExtra(RecognizerIntent.EXTRA_LANGUAGE, it) }
        }
    }
    val handle = remember(context, onDevice, intent, stoppedMessage) {
        OverlayDictationHandle.forContext(
            context = context,
            preferOnDevice = onDevice,
            intent = intent,
            onHeard = { latestHeard },
            onError = { latestError },
            stoppedMessage = stoppedMessage,
        )
    }

    DisposableEffect(lifecycle, handle) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_STOP) handle.cancel()
        }
        lifecycle.addObserver(observer)
        onDispose {
            lifecycle.removeObserver(observer)
            handle.cancel()
        }
    }
    return handle
}
