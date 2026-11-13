package persona.feature.inbox

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.remember
import androidx.compose.ui.focus.FocusDirection
import androidx.compose.ui.focus.FocusManager
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.SoftwareKeyboardController

/**
 * Writes down what a page asks of the focus and of the soft keyboard, over the real focus manager. Robolectric is not in
 * touch mode, where Android hands the focus straight back to the first field once it is cleared, so a test tells that a
 * gesture put the keyboard away by these calls, not by the field staying unfocused.
 */
internal class KeyboardProbe {
    /** The `force` of each `clearFocus` the page asked for. */
    val focusClears = mutableListOf<Boolean>()
    /** How many times the soft keyboard was asked to hide. */
    var hides = 0
        private set

    fun reset() { focusClears.clear(); hides = 0 }

    private val keyboard = object : SoftwareKeyboardController {
        override fun show() = Unit
        override fun hide() { hides++ }
    }

    /** Provides the probe's keyboard and a focus manager that forwards to the real one. */
    @Composable fun Around(content: @Composable () -> Unit) {
        val real = LocalFocusManager.current
        val focus = remember(real) {
            object : FocusManager {
                override fun clearFocus(force: Boolean) { focusClears += force; real.clearFocus(force) }
                override fun moveFocus(focusDirection: FocusDirection) = real.moveFocus(focusDirection)
            }
        }
        CompositionLocalProvider(LocalFocusManager provides focus, LocalSoftwareKeyboardController provides keyboard, content = content)
    }
}
