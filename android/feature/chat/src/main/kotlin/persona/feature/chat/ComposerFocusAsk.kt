package persona.feature.chat

import android.os.SystemClock
import androidx.compose.runtime.Composable
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import kotlinx.coroutines.delay
import persona.core.contracts.SessionKey

/**
 * The launcher's "Ask Persona" for a conversation shown inside the assistant page (#4339 C11: Android has no home page,
 * so it goes to the main conversation, as iOS's does): [session] is the conversation whose composer takes the cursor and
 * the keyboard once it shows; the chat page takes it once and sets it back to null.
 */
class ComposerFocusAsk {
    var session by mutableStateOf<SessionKey?>(null)
}

/** Provided by the assistant page around the conversation it shows; null elsewhere. */
val LocalComposerFocusAsk = staticCompositionLocalOf<ComposerFocusAsk?> { null }

/** Puts the cursor in [composer] and brings up the keyboard when [LocalComposerFocusAsk] asks it for [session]. */
@Composable
internal fun ComposerFocusAskEffect(session: SessionKey, composer: ChatComposerState?) {
    val ask = LocalComposerFocusAsk.current
    val keyboard = LocalSoftwareKeyboardController.current
    val pendingChatFocus by ChatComposerFocus.requests.collectAsState()
    LaunchedEffect(ask, session, composer) {
        val state = composer ?: return@LaunchedEffect
        val targetAsk = ask ?: return@LaunchedEffect
        snapshotFlow { targetAsk.session }.collect { asked ->
            if (asked != session) return@collect
            targetAsk.session = null
            // After the page's first layout: the text field has to be attached (as the assistant page's own composer waits).
            delay(300)
            state.focusRequest++
            delay(100)
            keyboard?.show()
        }
    }
    LaunchedEffect(session, composer, pendingChatFocus) {
        val state = composer ?: return@LaunchedEffect
        val entry = pendingChatFocus ?: return@LaunchedEffect
        if (!ChatComposerFocus.take(session, entry)) return@LaunchedEffect
        delay(300)
        state.focusRequest++
        delay(100)
        keyboard?.show()
    }
}

/**
 * Process-wide composer focus request targeting a specific conversation ([ChatRoute] with `focus=composer`),
 * e.g. from a conversation-bound Ask Persona widget. Mirrors `AssistantComposerFocus` with a 15-second expiry
 * so stale requests never pop the keyboard unexpectedly.
 */
class SessionComposerFocusRequests(private val clock: () -> Long = SystemClock::elapsedRealtime) {
    data class Entry(val session: SessionKey, val at: Long)

    private val requested = MutableStateFlow<Entry?>(null)
    val requests: StateFlow<Entry?> = requested.asStateFlow()

    fun request(session: SessionKey) {
        requested.value = Entry(session, clock())
    }

    fun take(session: SessionKey, entry: Entry): Boolean =
        entry.session == session && requested.compareAndSet(entry, null) && clock() - entry.at <= MAX_AGE_MILLIS

    private companion object { const val MAX_AGE_MILLIS = 15_000L }
}

/** Process-wide composer focus requests for explicit [ChatRoute] destinations. */
val ChatComposerFocus = SessionComposerFocusRequests()
