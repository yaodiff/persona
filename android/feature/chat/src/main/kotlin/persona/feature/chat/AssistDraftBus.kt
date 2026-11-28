package persona.feature.chat

import android.content.Context
import android.os.SystemClock
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.withContext
import persona.core.contracts.ImageLimits
import persona.core.contracts.SessionKey

/** Destination that claimed a digital-assistant handoff: an open chat or Persona's starter page. */
sealed interface AssistDraftTarget {
    data class Session(val key: SessionKey) : AssistDraftTarget
    data object Starter : AssistDraftTarget
}

/** Keep what is typed and add the handoff under it, or replace it (an extension's own quick prompt). */
enum class AssistDraftTextMode { Append, Replace }

/**
 * Text, screenshot and/or bounded non-sensitive page context waiting to prefill Persona's composer draft, from Android
 * Digital Assistant (`ROLE_ASSISTANT`) or an extension's compose link. Kept in memory only; never auto-sends.
 */
data class PendingAssistDraft(
    val id: String,
    val createdAtMillis: Long,
    val screenshot: ComposerAttachment? = null,
    val context: ChatLinkContext? = null,
    val text: String = "",
    val target: AssistDraftTarget? = null,
    val screenshotApplied: Boolean = false,
    val contextApplied: Boolean = false,
    val textApplied: Boolean = false,
    val textMode: AssistDraftTextMode = AssistDraftTextMode.Append,
) {
    val hasUnappliedContent: Boolean
        get() = (screenshot != null && !screenshotApplied) ||
            (context != null && !contextApplied) ||
            (text.isNotBlank() && !textApplied)
}

/**
 * Thread-safe, order-independent handoff queue connecting the system assist session to the active composer.
 * Handles screenshots, unsent overlay text, and assist metadata arriving in either order, including late
 * updates after the target composer has already claimed the handoff.
 */
class AssistDraftQueue(
    private val clock: () -> Long = SystemClock::elapsedRealtime,
    private val maxAgeMillis: Long = MAX_AGE_MILLIS,
) {
    private val state = MutableStateFlow<PendingAssistDraft?>(null)
    val pending: StateFlow<PendingAssistDraft?> = state.asStateFlow()

    /** Publishes or updates a handoff identified by [id], preserving any already-claimed target on the same [id]. */
    fun publish(
        id: String,
        screenshot: ComposerAttachment? = null,
        context: ChatLinkContext? = null,
        text: String = "",
        textMode: AssistDraftTextMode = AssistDraftTextMode.Append,
    ) {
        val now = clock()
        val nextText = if (textMode == AssistDraftTextMode.Replace) text else text.trim()
        state.update { current ->
            val reusable = current?.takeIf { it.id == id && !isExpired(it, now) }
            if (reusable != null) {
                reusable.copy(
                    screenshot = screenshot ?: reusable.screenshot,
                    screenshotApplied = if (screenshot != null) false else reusable.screenshotApplied,
                    context = context ?: reusable.context,
                    contextApplied = if (context != null) false else reusable.contextApplied,
                    text = nextText.ifEmpty { reusable.text },
                    textApplied = if (nextText.isNotEmpty()) false else reusable.textApplied,
                    textMode = if (nextText.isNotEmpty()) textMode else reusable.textMode,
                )
            } else {
                PendingAssistDraft(
                    id = id,
                    createdAtMillis = now,
                    screenshot = screenshot,
                    context = context,
                    text = nextText,
                    textMode = textMode,
                )
            }
        }
    }

    /** Attaches a screenshot to handoff [id], even if [id] was already claimed by the open composer. */
    fun attachScreenshot(id: String, attachment: ComposerAttachment) {
        publish(id = id, screenshot = attachment)
    }

    /** Attaches safe page context to handoff [id], preserving any screenshot or claimed target on [id]. */
    fun attachContext(id: String, context: ChatLinkContext) {
        publish(id = id, context = context)
    }

    /** Assigns an unclaimed, non-expired handoff to [target] (or returns it if already claimed by [target]). */
    fun claim(target: AssistDraftTarget): PendingAssistDraft? {
        val now = clock()
        var claimed: PendingAssistDraft? = null
        state.update { current ->
            if (current == null || isExpired(current, now)) {
                claimed = null
                null
            } else if (current.target == null || current.target == target) {
                current.copy(target = target).also { claimed = it }
            } else {
                claimed = null
                current
            }
        }
        return claimed
    }

    /** Moves a fresh handoff whose content has not yet been applied (e.g. cold start before main session loaded). */
    fun retarget(target: AssistDraftTarget): PendingAssistDraft? {
        val now = clock()
        var updated: PendingAssistDraft? = null
        state.update { current ->
            if (current == null || isExpired(current, now)) {
                updated = null
                null
            } else if (!current.screenshotApplied && !current.contextApplied && !current.textApplied) {
                current.copy(target = target).also { updated = it }
            } else {
                updated = null
                current
            }
        }
        return updated
    }

    fun markScreenshotApplied(id: String) {
        state.update { current ->
            if (current?.id == id) current.copy(screenshotApplied = true) else current
        }
    }

    fun markContextApplied(id: String) {
        state.update { current ->
            if (current?.id == id) current.copy(contextApplied = true) else current
        }
    }

    fun markTextApplied(id: String) {
        state.update { current ->
            if (current?.id == id) current.copy(textApplied = true) else current
        }
    }

    /** Returns the current non-expired handoff, or null if none is active. */
    fun current(): PendingAssistDraft? {
        val now = clock()
        return state.value?.takeUnless { isExpired(it, now) }
    }

    fun clear() {
        state.value = null
    }

    private fun isExpired(draft: PendingAssistDraft, now: Long): Boolean {
        val elapsed = now - draft.createdAtMillis
        return elapsed < 0L || elapsed > maxAgeMillis
    }

    companion object {
        const val MAX_AGE_MILLIS = 60_000L
    }
}

/** Process-wide assist handoff bus read by Persona's main chat and starter composer. */
val AssistDraftBus = AssistDraftQueue()

/** Outcome of prefilling a [PendingAssistDraft] into a composer without sending. */
data class AssistDraftApplyResult(
    val screenshotAdded: Boolean = false,
    val screenshotRejectedForLimit: Boolean = false,
    val contextAdded: Boolean = false,
    val textAdded: Boolean = false,
)

/**
 * Prefills [draft] into [composer] and optional [onContext] callback without overwriting existing text
 * and without sending. Deduplicates by attachment id/URI and context id.
 */
fun prefillAssistDraft(
    composer: ChatComposerState,
    draft: PendingAssistDraft,
    existingContextIds: Set<String> = emptySet(),
    limits: ImageLimits = ImageLimits.Standard,
    onContext: (ChatLinkContext) -> Unit = {},
): AssistDraftApplyResult {
    var screenshotAdded = false
    var screenshotRejected = false
    var contextAdded = false
    var textAdded = false
    val shot = draft.screenshot
    if (shot != null && !draft.screenshotApplied) {
        val alreadyPresent = composer.attachments.any {
            it.id == shot.id || (shot.uri.isNotEmpty() && it.uri == shot.uri)
        }
        if (!alreadyPresent) {
            if (composerPhotoRoom(composer.attachments, limits) > 0) {
                composer.beginEdit()
                composer.attachments = composer.attachments + shot
                screenshotAdded = true
            } else {
                screenshotRejected = true
            }
        }
    }
    val ctx = draft.context
    if (ctx != null && !draft.contextApplied) {
        if (ctx.id !in existingContextIds) {
            onContext(ctx)
            contextAdded = true
        }
    }
    val note = if (draft.textMode == AssistDraftTextMode.Replace) draft.text else draft.text.trim()
    if (note.isNotEmpty() && !draft.textApplied) {
        composer.beginEdit()
        if (draft.textMode == AssistDraftTextMode.Replace) {
            if (composer.value.text != note) {
                composer.value = androidx.compose.ui.text.input.TextFieldValue(
                    note,
                    androidx.compose.ui.text.TextRange(note.length),
                )
                textAdded = true
            }
        } else {
            val existing = composer.value.text
            val combined = when {
                existing.isBlank() -> note
                existing.trimEnd().endsWith(note) -> existing
                else -> "${existing.trimEnd()}\n$note"
            }
            if (combined != existing) {
                composer.value = androidx.compose.ui.text.input.TextFieldValue(
                    combined,
                    androidx.compose.ui.text.TextRange(combined.length),
                )
                textAdded = true
            }
        }
    }
    return AssistDraftApplyResult(screenshotAdded, screenshotRejected, contextAdded, textAdded)
}

/** Applies [draft] to [composer], records completion in [bus], and cleans up any cap-rejected assist screenshot. */
suspend fun applyAssistDraft(
    context: Context,
    composer: ChatComposerState,
    draft: PendingAssistDraft,
    bus: AssistDraftQueue = AssistDraftBus,
    existingContextIds: Set<String> = emptySet(),
    limits: ImageLimits = ImageLimits.Standard,
    onContext: (ChatLinkContext) -> Unit = {},
    onPhotoLimit: () -> Unit = {},
): AssistDraftApplyResult {
    val result = prefillAssistDraft(
        composer = composer,
        draft = draft,
        existingContextIds = existingContextIds,
        limits = limits,
        onContext = onContext,
    )
    if (draft.screenshot != null && !draft.screenshotApplied) {
        bus.markScreenshotApplied(draft.id)
    }
    if (draft.context != null && !draft.contextApplied) {
        bus.markContextApplied(draft.id)
    }
    if (draft.text.isNotBlank() && !draft.textApplied) {
        bus.markTextApplied(draft.id)
    }
    if (result.screenshotRejectedForLimit) {
        draft.screenshot?.uri?.let { uri ->
            withContext(Dispatchers.IO) { deleteCapture(context, uri) }
        }
        onPhotoLimit()
    }
    return result
}

/** Keeps [ChatPage] below the ART compile limit (#13747) while applying pending digital-assistant drafts. */
@Composable
internal fun BindPendingAssistDraft(
    key: SessionKey,
    context: Context,
    composer: ChatComposerState,
    readOnly: Boolean,
    canSend: Boolean,
    limits: ImageLimits = ImageLimits.Standard,
    existingContextIds: () -> Set<String>,
    onContext: (ChatLinkContext) -> Unit,
    onPhotoLimit: () -> Unit,
) {
    val pendingAssist by AssistDraftBus.pending.collectAsStateWithLifecycle()
    LaunchedEffect(pendingAssist, composer.draftLoaded, readOnly, canSend, key, limits) {
        val draft = pendingAssist ?: return@LaunchedEffect
        if (draft.target != AssistDraftTarget.Session(key) || !draft.hasUnappliedContent) return@LaunchedEffect
        if (!composer.draftLoaded || readOnly || !canSend) return@LaunchedEffect
        val result = applyAssistDraft(
            context = context,
            composer = composer,
            draft = draft,
            bus = AssistDraftBus,
            existingContextIds = existingContextIds(),
            onContext = onContext,
            limits = limits,
            onPhotoLimit = onPhotoLimit,
        )
        if (result.screenshotAdded || result.contextAdded) {
            composer.focusRequest++
        }
    }
}
