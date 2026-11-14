package persona.feature.terminal

import android.content.ClipboardManager
import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.FiniteAnimationSpec
import androidx.compose.animation.core.animate
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutVertically
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.*
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import persona.core.contracts.SessionKey
import persona.core.contracts.api.TerminalTransport
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import kotlin.math.roundToInt
import persona.core.terminal.ModifierState
import persona.core.terminal.TerminalModifiers

@Composable
fun TerminalScreen(model: TerminalViewModel, onBack: () -> Unit, sessionTitle: String = model.key.sessionId) =
    TerminalPage(model, onBack, sessionTitle, grab = null)

/**
 * The terminal, as a page of its own ([grab] null: the title, what it shows and the controls in the page's bar) or over
 * a chat ([TerminalCover]): no title, and a bar that drags it down, with Close.
 */
@Composable
internal fun TerminalPage(
    model: TerminalViewModel,
    onBack: () -> Unit,
    sessionTitle: String,
    grab: TerminalGrab?,
    /** Resume in Terminal (#5565): resume once when the not-running state offers it. */
    resumeOnOpen: Boolean = false,
) {
    val state by model.state.collectAsStateWithLifecycle()
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val context = LocalContext.current
    var keyboard by remember { mutableStateOf(false) }
    var wrap by remember { mutableStateOf(true) }
    var canvas by remember { mutableStateOf<TerminalCanvasView?>(null) }
    var paste by remember { mutableStateOf<String?>(null) }
    var selecting by remember { mutableStateOf<String?>(null) }
    var display by remember { mutableStateOf(false) }
    // Keyed by the session and whether this open asked to resume: a rotation after a failed resume keeps the flag and
    // does not fire again; closing (resumeOnOpen false) or a later Resume in Terminal starts a fresh attempt.
    var resumedOnOpen by rememberSaveable(model.key.machineId, model.key.sessionId, resumeOnOpen) { mutableStateOf(false) }
    val active = state.driving
    // Keys work whenever the hub is reachable: straight into the stream while driving, through /keys while watching.
    val typing = state.online
    val palette = PersonaTheme.palette
    val feedback = LocalAppFeedback.current
    LaunchedEffect(model, lifecycle) { lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) { model.run() } }
    val resources = LocalResources.current
    LaunchedEffect(model, feedback, resources) { model.notices.collect { feedback?.notice(resources.getString(it.text)) } }
    LaunchedEffect(model, feedback, resources) {
        model.resumeFailures.collect { feedback?.notice(it ?: resources.getString(R.string.terminal_resume_failed)) }
    }
    // Resume in Terminal (#5565): once, the way its Resume Session does, so the progress and the Agent show here.
    LaunchedEffect(resumeOnOpen, state.resumable, state.resuming) {
        if (resumeOnOpen && !resumedOnOpen && state.resumable && !state.resuming) {
            resumedOnOpen = true
            model.resume()
        }
    }
    LaunchedEffect(typing) { if (!typing) { keyboard = false; paste = null } }
    DisposableEffect(model) { onDispose { canvas?.keyboard(false) } }
    fun pasteClipboard() {
        val clip = context.getSystemService(ClipboardManager::class.java).primaryClip ?: return
        if (clip.itemCount == 0) return
        val text = clip.getItemAt(0).coerceToText(context).toString()
        if (text.contains('\n') || text.contains('\r')) paste = text else model.type(text)
    }
    // A terminal redraws all the time (every frame of the program, every wheel line): glass over it re-blurred its
    // bars and edges on each of those frames, on top of a slow link (2026-11-01). Its page is opaque in every style.
    CompositionLocalProvider(LocalGlassLevel provides GlassLevel.Solid) {
    val control: @Composable () -> Unit = {
        val watching = active || state.control == TerminalControl.Starting
        // Taking over needs the stream: a screen read once a second (an old hub or node) can only be watched.
        JChip(stringResource(when {
            active -> R.string.terminal_control_active; watching -> R.string.terminal_control_starting; else -> R.string.terminal_control_watching
        }), selected = active,
            onClick = if (watching) ({ model.watch() }) else if (state.online && state.streaming) ({ model.takeOver() }) else null)
    }
    val tools: @Composable () -> Unit = {
        JIconButton(PhIcons.Copy, stringResource(R.string.terminal_select_text), { selecting = model.screenText() }, enabled = state.loaded)
        JIconButton(PhIcons.SlidersHorizontal, stringResource(R.string.terminal_display_settings), { display = true })
    }
    JPage(sessionTitle, modifier = Modifier.imePadding(), edges = grab == null, topBar = {
        if (grab == null) JTopBar(sessionTitle, subtitle = terminalSubtitle(state), onBack = onBack, actions = { control(); tools() })
        else TerminalCoverBar(terminalSubtitle(state), grab, control, tools)
    }, bottomBar = {
        TerminalKeyBar(state.modifiers, typing, { key, action, quick -> if (typing) model.modifier(key, action, quick) }, model::press, keyboard,
            toggleKeyboard = { if (typing) keyboard = !keyboard }, paste = { if (typing) pasteClipboard() })
    }) { padding ->
        Column(Modifier.fillMaxSize().padding(padding)) {
            // OSC 8 links open where every link opens: the shell's in-app card (other schemes are confirmed first).
            val uriHandler = LocalUriHandler.current
            // The notice sits above the terminal, never over its text.
            val notRunning = state.note == TerminalNote.NoTerminal
            if (state.resumable && !notRunning) JNotice(stringResource(R.string.terminal_stopped),
                Modifier.padding(horizontal = pagePadding, vertical = Tokens.Space.s),
                actionLabel = stringResource(if (state.resuming) R.string.terminal_resuming else R.string.terminal_resume),
                onAction = if (state.resuming) null else model::resume)
            state.note?.takeUnless { notRunning }?.let { note -> JNotice(stringResource(note.text), Modifier.padding(horizontal = pagePadding, vertical = Tokens.Space.s),
                actionLabel = stringResource(R.string.terminal_retry), onAction = model::retry) }
            Box(Modifier.fillMaxWidth().weight(1f).background(palette.terminalBackground)) {
                AndroidView(factory = { ctx -> TerminalCanvasView(ctx, palette.terminalText.toArgb(), palette.terminalBackground.toArgb()).also { view ->
                    canvas = view
                    view.onGrid = model::grid; view.onText = model::type; view.onKey = model::press
                    view.onHardware = model::hardware; view.onMarked = model::marked; view.onWheel = model::wheel; view.onCell = model::click
                    view.onLink = { url -> runCatching { uriHandler.openUri(url) } }
                } }, modifier = Modifier.fillMaxSize().clipToBounds(), update = { view ->
                    view.font(state.fontSize)
                    view.render(state, wrap, palette.terminalText.toArgb(), palette.terminalBackground.toArgb()); view.keyboard(keyboard && typing)
                }, onRelease = { it.keyboard(false); canvas = null })
                // Over a chat it drags down like the bar: there is no output under the finger to scroll (iOS #4328).
                if (notRunning) TerminalNotRunning(state.resumable, state.resuming, model::resume, Modifier.matchParentSize().then(grab?.drag ?: Modifier))
                else if (!state.loaded) JSpinner(Modifier.align(Alignment.Center))
            }
        }
    }
    }
    if (state.control == TerminalControl.Busy) JDialog(onDismiss = { model.watch(remember = false) }, title = stringResource(R.string.terminal_busy_title),
        text = stringResource(R.string.terminal_busy_text),
        confirm = stringResource(R.string.terminal_take_over) to { model.takeOver(force = true) },
        dismiss = stringResource(R.string.terminal_control_watching) to { model.watch(remember = false) })
    paste?.let { text -> JDialog(onDismiss = { paste = null }, title = stringResource(R.string.terminal_paste_title),
        content = { Text(text, style = PersonaTheme.type.code, color = PersonaTheme.palette.ink, maxLines = 8, overflow = TextOverflow.Ellipsis) },
        confirm = stringResource(R.string.terminal_paste) to { model.type(text); paste = null }) }
    selecting?.let { text -> TerminalSelectText(text) { selecting = null } }
    if (display) TerminalDisplaySheet(state.fontSize, model::font, wrap, { wrap = it }, wrapEnabled = !active, onDismiss = { display = false })
}

/**
 * The session isn't running (#4345): in place of the terminal, as on iOS (#4328), with Resume session for a Claude Code
 * session. A retained shell stays visible with the same explicit Resume action above it.
 */
@Composable
private fun TerminalNotRunning(resumable: Boolean, resuming: Boolean, onResume: () -> Unit, modifier: Modifier) {
    Box(modifier.background(PersonaTheme.palette.terminalBackground), contentAlignment = Alignment.Center) {
        // The terminal is dark in either appearance, so what stands in for it is drawn in the dark palette, as on iOS.
        PersonaTheme(darkTheme = true) {
            JEmptyState(stringResource(R.string.terminal_no_terminal_title),
                stringResource(if (resumable) R.string.terminal_note_no_terminal_resume else R.string.terminal_note_no_terminal),
                icon = PhIcons.TerminalWindow,
                action = if (resumable) ({ JButton(stringResource(if (resuming) R.string.terminal_resuming else R.string.terminal_resume), onResume,
                    size = JButtonSize.Small, loading = resuming, enabled = !resuming) }) else null)
        }
    }
}

/**
 * A session's live terminal over its chat (iOS `TerminalCover`, #4339 C7): it comes up from the bottom and takes the
 * page under the status bar, with no title. Its bar drags it back down and has Close; Back closes it too. The model
 * lives in the page's own store, so the terminal comes back up on its last screen.
 */
@Composable
fun TerminalCover(
    key: SessionKey,
    open: Boolean,
    onClose: () -> Unit,
    transport: TerminalTransport,
    resumer: TerminalResumer? = null,
    /** Resume in Terminal (#5565): resume the stopped Agent as the cover comes up. */
    resumeOnOpen: Boolean = false,
) {
    val context = LocalContext.current.applicationContext
    TerminalCoverFrame(open, onClose) { grab ->
        val model = viewModel(key = "terminal-cover:${key.machineId}:${key.sessionId}") {
            TerminalViewModel(key, transport, preferences = SharedTerminalPreferences(context), resumer = resumer)
        }
        TerminalPage(model, onClose, key.sessionId, grab, resumeOnOpen = resumeOnOpen)
    }
}

/** What drags the cover down ([drag]: its bar, and the not-running state) and what closes it. */
internal class TerminalGrab(val drag: Modifier, val close: () -> Unit)

/** DESIGN_SYSTEM.md 4.2, "Terminal cover drag": let go past this, it closes (iOS 140 pt). */
private val CoverCloseDistance = 140.dp

/** How far a flick carries on, in seconds at its speed (UIKit's normal deceleration, iOS's predicted end). */
private const val CoverGlide = 0.5f

/** iOS `TerminalCover`'s release: past [closeAt], or flung so its glide would end past three times that (420 pt). */
internal fun coverCloses(distance: Float, velocity: Float, closeAt: Float): Boolean =
    distance > closeAt || distance + velocity * CoverGlide > closeAt * 3

/**
 * The cover: the page under it dimmed, less as the cover goes down, and its taps kept from the page; [content] slides
 * up into the space under the status bar. Only the bar drags it ([TerminalGrab]): the terminal under the bar keeps its
 * own scrolling and taps. Let go short of [coverCloses], it springs back.
 */
@Composable
internal fun TerminalCoverFrame(open: Boolean, onClose: () -> Unit, content: @Composable (TerminalGrab) -> Unit) {
    val close by rememberUpdatedState(onClose)
    val reduce = PersonaTheme.reduceMotion
    val motion = PersonaTheme.motion
    val palette = PersonaTheme.palette
    // A host's bar above the page (LocalChromeInsets): the cover starts under it, as the page's own bar does.
    val hostTop = LocalChromeInsets.current.top
    BackHandler(open) { close() }
    val slide: FiniteAnimationSpec<IntOffset> = motion.layout?.spec() ?: tween(motion.standard, easing = FastOutSlowInEasing)
    AnimatedVisibility(open, Modifier.fillMaxSize(), enter = EnterTransition.None, exit = ExitTransition.None) {
        BoxWithConstraints(Modifier.fillMaxSize()) {
            val closeAt = with(LocalDensity.current) { CoverCloseDistance.toPx() }
            val height = constraints.maxHeight.toFloat().coerceAtLeast(1f)
            var offset by remember { mutableFloatStateOf(0f) }
            val scope = rememberCoroutineScope()
            var settle by remember { mutableStateOf<Job?>(null) }
            val dragging = rememberDraggableState { delta -> settle?.cancel(); offset = (offset + delta).coerceAtLeast(0f) }
            val drag = Modifier.draggable(dragging, Orientation.Vertical, onDragStopped = { velocity ->
                if (coverCloses(offset, velocity, closeAt)) close()
                else settle = scope.launch {
                    if (reduce) offset = 0f else animate(offset, 0f, animationSpec = motion.enter()) { value, _ -> offset = value }
                }
            })
            Box(Modifier.fillMaxSize()
                .animateEnterExit(enter = if (reduce) EnterTransition.None else fadeIn(tween(motion.standard)),
                    exit = if (reduce) ExitTransition.None else fadeOut(tween(motion.standard)))
                .graphicsLayer { alpha = 1f - (offset / height).coerceIn(0f, 1f) }
                .background(palette.scrim)
                .pointerInput(Unit) {})
            Box(Modifier.fillMaxSize()
                .animateEnterExit(enter = if (reduce) EnterTransition.None else slideInVertically(slide) { it },
                    exit = if (reduce) ExitTransition.None else slideOutVertically(slide) { it })
                .offset { IntOffset(0, offset.roundToInt()) }
                .padding(top = hostTop).statusBarsPadding().padding(top = Tokens.Space.xs)
                .clip(PersonaTheme.shapes.sheetShape)
                .testTag("terminal_cover")) {
                CompositionLocalProvider(LocalChromeInsets provides ChromeInsets()) { content(TerminalGrab(drag) { close() }) }
            }
        }
    }
}

/**
 * The cover's bar (iOS `TerminalPanel.grabBar`), there in every state: the handle, then the take-over chip, what the
 * terminal shows ([subtitle]), the tools and Close. Dragged down it takes the cover with it.
 */
@Composable
private fun TerminalCoverBar(subtitle: String?, grab: TerminalGrab, control: @Composable () -> Unit, tools: @Composable () -> Unit) {
    val palette = PersonaTheme.palette
    Column(Modifier.fillMaxWidth().then(grab.drag).padding(top = Tokens.Space.s).testTag("terminal_bar")) {
        Box(Modifier.align(Alignment.CenterHorizontally).size(Tokens.Space.Standard.xxl, Tokens.Space.Standard.xs)
            .clip(PersonaTheme.shapes.controlShape).background(palette.ink3))
        Row(Modifier.fillMaxWidth().padding(start = pagePadding, end = Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            control()
            Text(subtitle.orEmpty(), Modifier.weight(1f), style = PersonaTheme.type.caption, color = palette.ink2,
                maxLines = 1, overflow = TextOverflow.Ellipsis)
            Row(verticalAlignment = Alignment.CenterVertically) {
                tools()
                JIconButton(PersonaIcons.close, stringResource(R.string.terminal_close), grab.close)
            }
        }
    }
}

/** Under the title: the grid the phone drives, taking it over, the pane it mirrors, or the screen read once a second. */
@Composable
private fun terminalSubtitle(state: TerminalUiState): String? = when {
    state.driving && state.cols > 0 -> stringResource(R.string.terminal_subtitle_phone, state.cols, state.rows)
    state.online && state.control == TerminalControl.Starting -> stringResource(R.string.terminal_subtitle_starting)
    !state.loaded || !state.online -> null
    state.streaming && state.cols > 0 -> stringResource(R.string.terminal_subtitle_computer, state.cols, state.rows)
    !state.streaming -> stringResource(R.string.terminal_subtitle_polling)
    else -> null
}

/** "Display": the font size (a step at a time, the sheet stays open) and folding what is only watched. */
@Composable
private fun TerminalDisplaySheet(
    fontSize: Float, onFont: (Boolean) -> Unit, wrap: Boolean, onWrap: (Boolean) -> Unit, wrapEnabled: Boolean, onDismiss: () -> Unit,
) {
    val palette = PersonaTheme.palette
    JSheet(onDismiss, title = stringResource(R.string.terminal_display)) {
        JRow(stringResource(R.string.terminal_font_size), subtitle = stringResource(R.string.terminal_font_size_note), trailing = {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                JButton(stringResource(R.string.terminal_font_down), { onFont(false) }, kind = JButtonKind.Secondary, size = JButtonSize.Small, enabled = fontSize > TerminalFontSize.MIN)
                val sizeDescription = stringResource(R.string.terminal_font_size_value, TerminalFontSize.label(fontSize))
                Text(TerminalFontSize.label(fontSize), style = PersonaTheme.type.micro, color = palette.ink, textAlign = TextAlign.Center,
                    maxLines = 1, modifier = Modifier.widthIn(min = Tokens.Space.xxl).semantics { contentDescription = sizeDescription })
                JButton(stringResource(R.string.terminal_font_up), { onFont(true) }, kind = JButtonKind.Secondary, size = JButtonSize.Small, enabled = fontSize < TerminalFontSize.MAX)
            }
        })
        JSwitchRow(stringResource(R.string.terminal_wrap), wrap, onWrap, enabled = wrapEnabled,
            subtitle = stringResource(if (wrapEnabled) R.string.terminal_wrap_on_note else R.string.terminal_wrap_off_note))
    }
}

@Composable
private fun TerminalKeyBar(
    modifiers: Map<TerminalModifiers.Key, ModifierState>, enabled: Boolean, modifierAction: (TerminalModifiers.Key, String, Boolean) -> Unit,
    press: (String) -> Unit, keyboard: Boolean, toggleKeyboard: () -> Unit, paste: () -> Unit,
) {
    val currentModifierAction by rememberUpdatedState(modifierAction)
    val currentToggle by rememberUpdatedState(toggleKeyboard)
    val currentPress by rememberUpdatedState(press)
    val keyboardDescription = stringResource(if (keyboard) R.string.terminal_keyboard_hide_description else R.string.terminal_keyboard_show_description)
    val modifierStates = ModifierState.entries.associateWith { stringResource(it.description) }
    val keyNames = keyNameSources.mapValues { (_, name) -> if (name is Int) stringResource(name) else name.toString() }
    ComposerFrame(trailing = {
        JChip(stringResource(if (keyboard) R.string.terminal_keyboard_hide else R.string.terminal_keyboard_show), selected = keyboard, icon = PhIcons.Keyboard,
                modifier = Modifier.heightIn(min = Tokens.MinTouch.android).semantics {
                    role = Role.Button; contentDescription = keyboardDescription
                    onClick { currentToggle(); true }
                }.pointerInput(Unit) { detectTapGestures(onTap = { currentToggle() }, onDoubleTap = { currentPress("enter") }) })
    }, field = {
        Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
            TerminalModifiers.Key.entries.forEach { key ->
                val value = modifiers[key] ?: ModifierState.OFF
                val label = when (key) { TerminalModifiers.Key.CTRL -> "Ctrl"; TerminalModifiers.Key.ALT -> "Alt"; TerminalModifiers.Key.SHIFT -> "⇧" }
                val name = when (key) { TerminalModifiers.Key.CTRL -> "Ctrl"; TerminalModifiers.Key.ALT -> "Alt"; TerminalModifiers.Key.SHIFT -> "Shift" }
                JKeyCap(label, state = value.keyCap, enabled = enabled, modifier = Modifier
                    .semantics {
                        role = Role.Button; contentDescription = name; selected = value != ModifierState.OFF
                        stateDescription = modifierStates.getValue(value)
                        onClick { currentModifierAction(key, "tap", false); true }
                    }.pointerInput(key) {
                        var lastTap = Long.MIN_VALUE / 2
                        awaitEachGesture {
                            val down = awaitFirstDown(requireUnconsumed = false); down.consume()
                            var canceled = false
                            val up = withTimeoutOrNull(viewConfiguration.longPressTimeoutMillis) {
                                waitForUpOrCancellation().also { canceled = it == null }
                            }
                            if (canceled) return@awaitEachGesture
                            if (up != null) {
                                currentModifierAction(key, "tap", up.uptimeMillis - lastTap <= viewConfiguration.doubleTapTimeoutMillis)
                                lastTap = up.uptimeMillis; up.consume()
                            } else {
                                currentModifierAction(key, "hold", false)
                                try { waitForUpOrCancellation()?.consume() } finally { currentModifierAction(key, "release", false) }
                                lastTap = Long.MIN_VALUE / 2
                            }
                        }
                    })
            }
            // ⇧Tab cycles Claude Code's modes (one key, not ⇧ then Tab).
            listOf("Esc" to "esc", "Tab" to "tab", "⇧Tab" to "shift+tab", "⌃C" to "ctrl+c", "←" to "left", "↓" to "down", "↑" to "up",
                "→" to "right", "↵" to "enter", "⌫" to "backspace").forEach { (label, key) ->
                JKeyCap(label, onClick = { press(key) }, enabled = enabled, contentDescription = keyNames[key])
            }
            JKeyCap(stringResource(R.string.terminal_paste), onClick = paste, enabled = enabled)
        }
    })
}

/** What a screen reader says a modifier is doing. */
private val ModifierState.description: Int get() = when (this) {
    ModifierState.OFF -> R.string.terminal_modifier_off
    ModifierState.ONCE -> R.string.terminal_modifier_once
    ModifierState.HELD -> R.string.terminal_modifier_held
    ModifierState.LOCKED -> R.string.terminal_modifier_locked
}

private val ModifierState.keyCap: KeyCapState get() = when (this) {
    ModifierState.OFF -> KeyCapState.Off
    ModifierState.ONCE -> KeyCapState.Once
    ModifierState.HELD -> KeyCapState.Held
    ModifierState.LOCKED -> KeyCapState.Locked
}

/** What a screen reader says for the keys whose labels are symbols: key names, or resources for the words. */
private val keyNameSources: Map<String, Any> = mapOf("shift+tab" to "Shift Tab", "ctrl+c" to "Control C",
    "left" to R.string.terminal_key_left, "down" to R.string.terminal_key_down, "up" to R.string.terminal_key_up,
    "right" to R.string.terminal_key_right, "enter" to R.string.terminal_key_enter, "backspace" to R.string.terminal_key_backspace)
