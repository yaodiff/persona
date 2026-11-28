package persona.core.designsystem

import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.composed
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEvent
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.isCtrlPressed
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onKeyEvent
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type

internal val LocalHardwareShortcutController = staticCompositionLocalOf<HardwareShortcutController?> { null }
internal val LocalHardwareShortcutHelpOwner = staticCompositionLocalOf<Any?> { null }

private fun HardwareShortcutController.handle(event: KeyEvent, owner: Any? = null): Boolean {
    val shortcut = when {
        event.key == Key.Escape -> HardwareShortcut.Escape
        event.key == Key.Slash && event.isCtrlPressed -> HardwareShortcut.Help
        else -> return false
    }
    return dispatch(shortcut, if (event.type == KeyEventType.KeyDown) HardwareKeyPhase.Down else HardwareKeyPhase.Up,
        repeated = event.nativeKeyEvent.repeatCount > 0, owner = owner)
}

/**
 * Large windows own their keyboard help and transient stack. The key listener runs after focused children, so a
 * terminal, an IME, the expanded composer and pointer menus keep the keys they consume. The content and its parent
 * stay in place when the window changes; compact windows only remove the key modifier and controller.
 */
@Composable
fun HardwareShortcutHost(modifier: Modifier = Modifier, requestFocusWhenLarge: Boolean = true, content: @Composable () -> Unit) {
    val large = LocalLargeWindowInput.current
    val controller = remember { HardwareShortcutController() }
    val focus = remember { FocusRequester() }
    var focusWithin by remember { mutableStateOf(false) }
    val currentFocusWithin by rememberUpdatedState(focusWithin)
    LaunchedEffect(large) {
        if (large) {
            // Fill an empty focus tree so Ctrl+/ has a receiver, but never take an existing child or dialog focus.
            if (requestFocusWhenLarge && !currentFocusWithin) { val _ = runCatching { focus.requestFocus() } }
        } else controller.closeHelp()
    }
    val keys = if (large) Modifier.onKeyEvent { controller.handle(it, focus) }.focusable() else Modifier
    CompositionLocalProvider(
        LocalHardwareShortcutController provides (if (large) controller else null),
        LocalHardwareShortcutHelpOwner provides focus,
    ) {
        Box(modifier.onFocusChanged { focusWithin = it.hasFocus }.focusRequester(focus).then(keys)) {
            content()
            if (large && controller.helpVisible && (controller.helpOwner == null || controller.helpOwner === focus)) {
                KeyboardShortcutsSheet(onDismiss = controller::closeHelp)
            }
        }
    }
}

/** Show help in the window that received the shortcut, rather than behind its sheet or dialog. */
@Composable
internal fun HardwareShortcutHelp(owner: Any) {
    val controller = LocalHardwareShortcutController.current ?: return
    if (controller.helpVisible && controller.helpOwner === owner) KeyboardShortcutsSheet(onDismiss = controller::closeHelp)
}

/** Let a text input open help before its editor consumes the key; all other keys keep their ordinary path. */
fun Modifier.hardwareShortcutTextInput(enabled: () -> Boolean = { true }): Modifier = composed {
    val controller = LocalHardwareShortcutController.current ?: return@composed this
    val owner = LocalHardwareShortcutHelpOwner.current
    val currentEnabled by rememberUpdatedState(enabled)
    onPreviewKeyEvent { event ->
        if (currentEnabled() && event.key == Key.Slash && event.isCtrlPressed) controller.handle(event, owner) else false
    }
}

/** Register one dismissible surface and handle only keys its focused children did not consume. */
fun Modifier.hardwareShortcutDismiss(onDismiss: () -> Unit): Modifier = hardwareShortcutDismiss(onDismiss, null)

/** Register help in this surface's own dialog window when it has one. */
fun Modifier.hardwareShortcutDismiss(onDismiss: () -> Unit, helpOwner: Any?): Modifier = composed {
    // Tracked in every window size, so a child's focus is known when a compact window becomes large.
    var focusWithin by remember { mutableStateOf(false) }
    val currentFocusWithin by rememberUpdatedState(focusWithin)
    val tracked = onFocusChanged { focusWithin = it.hasFocus }
    val controller = LocalHardwareShortcutController.current ?: return@composed tracked
    val currentDismiss by rememberUpdatedState(onDismiss)
    val token = remember { Any() }
    val focus = remember { FocusRequester() }
    DisposableEffect(controller, token) {
        val registration = controller.register(token) { currentDismiss() }
        onDispose { controller.unregister(registration) }
    }
    // Becoming a large window registers this surface again; a child that already holds focus keeps it.
    LaunchedEffect(controller, token) { if (!currentFocusWithin) { val _ = runCatching { focus.requestFocus() } } }
    tracked.onKeyEvent { controller.handle(it, helpOwner) }.focusRequester(focus).focusable()
}
