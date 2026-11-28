package persona.feature.chat

import androidx.compose.runtime.Stable
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import persona.core.contracts.Item
import persona.core.contracts.UndeliveredMessage

/**
 * ChatPage's own transient UI state: which overlay or sheet is up, the bars' measured heights, one-off open requests
 * and busy flags. Each property is the state object ChatPage remembered on its own before (#17578); one remembered
 * holder keeps them out of the page's method, which ART stops compiling past 10,000 code units. Nothing here is saved
 * across process death: the saveable values stay in ChatPage's `rememberSaveable` calls.
 */
@Stable
internal class ChatPageFlags {
    val headerMenuOpen = mutableStateOf(false)
    val headerOverlay = mutableStateOf(false)
    val modeOverlay = mutableStateOf(false)
    val pillOverlay = mutableStateOf(false)
    val composerOverlay = mutableStateOf(false)
    // PendingStack.cap uses the bars' heights and the visible stack or To Decide panel's height.
    val topBarHeight = mutableIntStateOf(0)
    val footerHeight = mutableIntStateOf(0)
    val stackHeight = mutableIntStateOf(0)
    // The page's height above the keyboard, in pixels, read only where it is used (PendingStack.cap, the trays' room): it moves
    // on every frame the keyboard slides, and a BoxWithConstraints here composed the whole page again on each of them.
    val pageHeightPx = mutableIntStateOf(-1)
    val undeliveredEdits = mutableStateOf<Map<String, UndeliveredEdit>>(emptyMap())
    // /model in the "/" list asked for the "Mode" sheet: a one-off, cleared once the sheet opened (not replayed when the bar returns).
    val openModes = mutableStateOf(false)
    // The model-route card opens only Persona's model picker, never Brief/Board's separate Mode sheet as well.
    val openModelRoutePicker = mutableStateOf(false)
    // The model-route card asks the existing Persona pill to clear its kept pick with the pill's optimistic rollback.
    val clearModelPick = mutableStateOf(false)
    // `/permissions` in the "/" list asked for the permission mode menu (#6513): Glass's mode button, the others' "Mode" sheet
    val openPermissions = mutableStateOf(false)
    val statusSheet = mutableStateOf(false)
    // The loop and background work's sheet, from the header's pill (Glass, #4339 C8).
    val activitySheet = mutableStateOf<BackgroundPanelRequest?>(null)
    val undeliveredBusy = mutableStateOf(emptySet<String>())
    val timelineFollow = mutableStateOf<(() -> Unit)?>(null)
    val submittedContexts = mutableStateOf(emptyList<ChatLinkContext>())
    // A card open on its full page, in either place of cards.
    val stackOverlay = mutableStateOf(false)
    val terminalHelp = mutableStateOf(false)
    val taskPageVisible = mutableStateOf(false)
    val fullText = mutableStateOf<String?>(null)
    val linkVisible = mutableStateOf(false)
    // Which items the lines were last built from: until the first build finishes, the page is still loading, not empty.
    val projectedFrom = mutableStateOf<List<Item>?>(null)
    val previousLiveWork = mutableStateOf(emptySet<String>())
    val overviewOpen = mutableStateOf(false)
    // "Branch to new chat" (#2137) from a reply, "Rewind to here" (#2436) on your message: the menu's session-wide extras (web ⋯).
    val forking = mutableStateOf(false)
    val rewindBusy = mutableStateOf(false)
}

/** A successful assistant action shown while the row is unchanged; any newer authoritative value replaces it. */
internal data class UndeliveredEdit(val before: UndeliveredMessage, val shown: UndeliveredMessage?)
