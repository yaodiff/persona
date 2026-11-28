package persona.feature.chat

import android.view.KeyEvent
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import persona.core.contracts.SessionKey
import persona.core.designsystem.LocalLargeWindowInput
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
class ComposerKeyboardInstrumentedTest {
    @get:Rule val rule = createAndroidComposeRule<ComposerInstrumentationActivity>()
    private val instrumentation = InstrumentationRegistry.getInstrumentation()

    private fun show(state: ChatComposerState, onSend: () -> Unit) {
        rule.setContent {
            CompositionLocalProvider(LocalLargeWindowInput provides true) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    LargeWindowComposer(SessionKey("box", "s1"), state, working = false, sending = false,
                        canClear = false, service = null, onSend = onSend, onCommand = {}, onNotice = {})
                }
            }
        }
        rule.onNode(hasSetTextAction()).performClick()
        rule.waitForIdle()
    }

    private fun enter(action: Int, repeatCount: Int, modifiers: Int) {
        instrumentation.runOnMainSync {
            val event = KeyEvent(0, 0, action, KeyEvent.KEYCODE_ENTER, repeatCount, modifiers)
            val target = rule.activity.currentFocus ?: rule.activity.findViewById(android.R.id.content)
            target.dispatchKeyEvent(event)
        }
    }

    @Test fun realMetaAndCtrlEventsSendOnceWithoutRepeatOrKeyUpDuplicates() {
        val state = ChatComposerState()
        state.insert("draft", atSelection = false)
        var sent = 0
        show(state) { sent++ }
        enter(KeyEvent.ACTION_DOWN, 0, KeyEvent.META_CTRL_ON)
        enter(KeyEvent.ACTION_DOWN, 1, KeyEvent.META_CTRL_ON)
        enter(KeyEvent.ACTION_UP, 0, KeyEvent.META_CTRL_ON)
        assertEquals(1, sent)
        enter(KeyEvent.ACTION_DOWN, 0, KeyEvent.META_META_ON)
        enter(KeyEvent.ACTION_UP, 0, KeyEvent.META_META_ON)
        assertEquals(2, sent)
    }

    @Test fun inputConnectionCompositionCommitsBeforeCtrlReturnCanSend() {
        val state = ChatComposerState().apply { insert("中文", atSelection = false) }
        var sent = 0
        show(state) { sent++ }
        var connection: InputConnection? = null
        instrumentation.runOnMainSync {
            connection = rule.activity.currentFocus?.onCreateInputConnection(EditorInfo())
        }
        assertNotNull(connection)
        // Keep setComposingRegion and the composing Ctrl+Return chord in one main-thread turn:
        // pumping the looper via waitForIdle() lets the system IME (Gboard on API 35) receive
        // updateSelection and send an asynchronous RemoteInputConnectionImpl.finishComposingText()
        // that clears our synthetic composing span before the chord is dispatched.
        instrumentation.runOnMainSync {
            connection!!.setComposingRegion(0, state.value.text.length)
            assertNotNull(state.value.composition)
            val target = rule.activity.currentFocus ?: rule.activity.findViewById(android.R.id.content)
            target.dispatchKeyEvent(KeyEvent(0, 0, KeyEvent.ACTION_DOWN, KeyEvent.KEYCODE_ENTER, 0, KeyEvent.META_CTRL_ON))
            target.dispatchKeyEvent(KeyEvent(0, 0, KeyEvent.ACTION_UP, KeyEvent.KEYCODE_ENTER, 0, KeyEvent.META_CTRL_ON))
            assertEquals(0, sent)
            connection!!.finishComposingText()
        }
        rule.waitForIdle()
        assertNull(state.value.composition)
        // Composition owns the first chord and may commit it as a newline. The committed chord must add nothing else.
        val committed = state.value.text
        enter(KeyEvent.ACTION_DOWN, 0, KeyEvent.META_CTRL_ON)
        enter(KeyEvent.ACTION_UP, 0, KeyEvent.META_CTRL_ON)
        assertEquals(1, sent)
        assertEquals(committed, state.value.text)
    }
}
