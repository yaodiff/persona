package persona.feature.chat

import android.content.res.Resources
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.listSaver
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.api.*
import persona.core.contracts.JsonCodec
import persona.core.designsystem.resolve
import persona.core.domain.AttachedFile
import persona.core.domain.ComposerMessage
import persona.core.domain.MessageContext
import persona.core.domain.contextSummary
import persona.core.domain.hasContextTag
import persona.core.domain.isInterjectContext
import persona.core.domain.parseAttachedFiles
import persona.core.domain.parseContexts
import kotlinx.serialization.encodeToString

data class ComposerAttachment(
    val id: String,
    val name: String,
    val uri: String,
    val image: Boolean,
    val path: String? = null,
    val error: String? = null,
    val size: Long? = null,
    val uploadState: DraftUploadState = DraftUploadState.Pending,
    /**
     * A video from the photo picker (iOS `VideoAttachments`, docs/protocol/protocol.md "Sending videos"): uploaded as it is,
     * like a file, and sent as a `<persona-file kind="video">` tag after the words rather than as a path in them.
     */
    val video: Boolean = false,
    /** A video's length in seconds, when the phone could read it. */
    val duration: Double? = null,
    /** Actual scaled/staged JPEG byte count for an inline photo, persisted for body-budget accounting. */
    val scaledBytes: Long? = null,
) {
    /** A photo or a video (videos keep their own cap of 4 while pictures follow the hub's image limits). */
    val media: Boolean get() = image || video
}

data class ChatComposerDraft(
    val text: String = "", val attachments: List<ComposerAttachment> = emptyList(),
    val sessionVersion: DraftSessionVersion? = null,
    val hasPickedSavedPrompt: Boolean = false,
)

private fun DraftAttachment.composerAttachment() = ComposerAttachment(
    id, name, uri, type == DraftAttachmentType.Image, uploadedPath, error, size, uploadState,
    video = type == DraftAttachmentType.Video, duration = duration, scaledBytes = scaledBytes)

internal fun DraftSnapshot.composerDraft() =
    ChatComposerDraft(text, attachments.map { it.composerAttachment() }, sessionVersion, hasPickedSavedPrompt)

internal fun ChatComposerDraft.storedAttachments() = attachments.map {
    DraftAttachment(it.id, when {
            it.image -> DraftAttachmentType.Image
            it.video -> DraftAttachmentType.Video
            else -> DraftAttachmentType.File
        }, it.uri, it.name, it.size, when {
            it.uploadState == DraftUploadState.Invalid -> DraftUploadState.Invalid
            it.path != null -> DraftUploadState.Uploaded
            it.error != null -> DraftUploadState.Failed
            else -> it.uploadState
        }, it.path, it.error, it.duration, it.scaledBytes)
}

@Stable
class ChatComposerState(initial: ChatComposerDraft = ChatComposerDraft()) {
    var focused by mutableStateOf(false)
    var focusRequest by mutableIntStateOf(0)
    /** What [message] says for photos sent without words; [ChatComposer] keeps it in the app's language. */
    var photosOnlyText: String = ""
    /** What [message] says for videos sent without words or photos ("Please look at the video", iOS); kept like [photosOnlyText]. */
    var videosOnlyText: String = ""
    /** What [message] says for ordinary files sent without words or photos; kept like [photosOnlyText]. */
    var filesOnlyText: String = ""
    private var fieldValue by mutableStateOf(TextFieldValue(initial.text, TextRange(initial.text.length)))
    internal var hasPickedSavedPrompt by mutableStateOf(initial.hasPickedSavedPrompt)
        private set
    var value: TextFieldValue
        get() = fieldValue
        set(value) {
            if (value.text != fieldValue.text) hasPickedSavedPrompt = false
            fieldValue = value
        }
    var draftLoaded by mutableStateOf(false)
    private var attachmentOrder by mutableStateOf(initial.attachments.map { it.id })
    private val attachmentStates = mutableStateMapOf<String, ComposerAttachment>().apply { putAll(initial.attachments.associateBy { it.id }) }
    /** Edited photos' sources, edits and history by attachment id (#4992): kept in memory beside the draft, never sent. */
    internal var photoEdits by mutableStateOf<Map<String, PhotoEdits>>(emptyMap())
    /** The draft's attachments; a photo that leaves it takes its kept edits along. */
    var attachments: List<ComposerAttachment>
        get() = attachmentOrder.mapNotNull(attachmentStates::get)
        set(value) {
            val ids = value.mapTo(mutableSetOf()) { it.id }
            attachmentStates.keys.filter { it !in ids }.forEach(attachmentStates::remove)
            value.forEach { attachmentStates[it.id] = it }
            attachmentOrder = value.map { it.id }
            if (photoEdits.keys.any { id -> id !in ids }) photoEdits = photoEdits.filterKeys { it in ids }
        }

    /** A background upload updates its own row, never a stale copy of another attachment's state. */
    internal fun updateAttachment(id: String, update: (ComposerAttachment) -> ComposerAttachment) {
        attachmentStates[id]?.let { attachmentStates[id] = update(it) }
    }
    var sessionVersion by mutableStateOf(initial.sessionVersion)
    var currentSessionVersion by mutableStateOf<DraftSessionVersion?>(null)
    val oldVersion get() = hasContent && (currentSessionVersion?.replaced == true || sessionVersion?.let { origin ->
        currentSessionVersion?.let(origin::differsFrom)
    } == true)
    val draft get() = ChatComposerDraft(value.text, attachments, sessionVersion, hasPickedSavedPrompt)
    val ready get() = attachments.all { it.path != null && it.uploadState != DraftUploadState.Invalid }
    val hasContent get() = value.text.isNotBlank() || attachments.isNotEmpty()

    internal fun beginEdit() {
        if (!hasContent) sessionVersion = currentSessionVersion
    }

    internal fun fillSavedPrompt(text: String) {
        beginEdit()
        value = TextFieldValue(text, TextRange(text.length))
        hasPickedSavedPrompt = true
        focusRequest++
    }

    fun insert(text: String, atSelection: Boolean) {
        beginEdit()
        val start = if (atSelection) value.selection.min.coerceIn(0, value.text.length) else value.text.length
        val end = if (atSelection) value.selection.max.coerceIn(start, value.text.length) else start
        val prefix = value.text.take(start)
        val suffix = value.text.drop(end)
        val addition = (if (prefix.isNotEmpty() && !prefix.last().isWhitespace()) " " else "") + text +
            (if (suffix.isNotEmpty() && !suffix.first().isWhitespace()) " " else "")
        value = TextFieldValue(prefix + addition + suffix, TextRange(start + addition.length))
    }

    fun message(): String {
        check(ready)
        val images = attachments.filter { it.image }.mapNotNull { it.path }
        val files = attachments.filterNot { it.media }.mapNotNull { it.attachedFile() }
        val videos = attachments.filter { it.video }.mapNotNull { it.attachedFile() }
        return ComposerMessage.text(value.text, images, files, videos,
            photosOnly = photosOnlyText, videosOnly = videosOnlyText, filesOnly = filesOnlyText)
    }

    fun clearIfUnchanged(sent: ChatComposerDraft) {
        if (draft == sent) { value = TextFieldValue(); attachments = emptyList(); sessionVersion = currentSessionVersion }
    }

    /** Fills an empty composer with a stored draft: the composer is drawn before the draft is read (#4045). */
    internal fun load(draft: ChatComposerDraft) {
        val tagged = !draft.hasPickedSavedPrompt && parseAttachedFiles(draft.text).first.any { it.kind == "file" || it.kind == "video" }
        if (tagged) {
            // Drafts written by an older build can still carry tags as words; bring their files back as ready rows.
            editMessage(draft.text)
            val restored = attachments
            attachments = restored + draft.attachments.filter { item ->
                restored.none { it.path == item.path && it.image == item.image && it.video == item.video }
            }
        } else {
            value = TextFieldValue(draft.text, TextRange(draft.text.length))
            attachments = draft.attachments
        }
        hasPickedSavedPrompt = draft.hasPickedSavedPrompt
        sessionVersion = draft.sessionVersion
        draftLoaded = true
    }

    fun editMessage(text: String) {
        beginEdit()
        val masked = maskComposerContexts(text)
        val paths = composerImagePaths(masked.text)
        // Videos and ordinary files come back already on the machine; kinds this composer does not own keep their tags.
        val (files, rest) = parseAttachedFiles(masked.text)
        // The media cap limits new picks; older or hand-written messages keep every video and document on edit.
        val videos = files.filter { it.kind == "video" }
        val documents = files.filter { it.kind == "file" }
        val restored = files.filter { it.kind == "video" || it.kind == "file" }
        val source = if (restored.isEmpty()) masked.text else rest + files.filter { it !in restored }.joinToString("") { "\n\n" + it.toXml() }
        val plain = masked.unmask(source.replace(composerImagePlaceholders, "").replace(composerImageTags, "")).trimEnd()
        value = TextFieldValue(plain, TextRange(plain.length))
        attachments = paths.mapIndexed { index, path ->
            ComposerAttachment("restored-$index-$path", path.substringAfterLast('/'), "", true, path,
                uploadState = DraftUploadState.Uploaded)
        } + videos.mapIndexed { index, file ->
            ComposerAttachment("restored-video-$index-${file.path}", file.name, "", false, file.path, size = file.size.takeIf { it > 0 },
                uploadState = DraftUploadState.Uploaded, video = true, duration = file.duration)
        } + documents.mapIndexed { index, file ->
            ComposerAttachment("restored-file-$index-${file.path}", file.name, "", false, file.path, size = file.size.takeIf { it > 0 },
                uploadState = DraftUploadState.Uploaded)
        }
    }

    internal fun restoreMessage(text: String) {
        if (!hasContent) { editMessage(text); return }
        val restored = ChatComposerState().apply { editMessage(text) }
        val combined = listOf(value.text, restored.value.text).filter(String::isNotBlank).joinToString("\n\n")
        value = TextFieldValue(combined, TextRange(combined.length))
        attachments = attachments + restored.attachments.filter { item -> attachments.none { it.id == item.id } }
    }

    /**
     * A queued message pulled back (the queue strip's "Take back to edit"): the draft if empty, else after it on a new line (iOS);
     * photos and files it carried come back as attachments rather than as tags, and its context blocks are returned for the
     * composer's context bar rather than dumped into the text field.
     */
    internal fun restoreQueued(text: String, photosBeside: Int = 0): List<ChatLinkContext> {
        beginEdit()
        val (parsedContexts, withoutContexts) = parseComposerContexts(text)
        val contexts = parsedContexts.map { it.toChatLinkContext() }
        val restored = ChatComposerState().apply { editMessage(withoutContexts) }
        val trimmed = restored.value.text.trim()
        val hasMediaOrFiles = restored.attachments.isNotEmpty() || photosBeside > 0
        val hasContexts = contexts.isNotEmpty()
        val isStandIn = (hasMediaOrFiles && (trimmed in composerPhotoStandIns ||
            trimmed == photosOnlyText || trimmed == videosOnlyText || trimmed == filesOnlyText)) ||
            (hasContexts && trimmed in composerContextStandIns)
        val words = if (isStandIn) "" else restored.value.text
        val combined = when {
            value.text.isEmpty() -> words
            words.isEmpty() -> value.text
            else -> value.text + "\n" + words
        }
        value = TextFieldValue(combined, TextRange(combined.length))
        attachments = attachments + restored.attachments.filter { item -> attachments.none { it.id == item.id } }
        return contexts
    }

    /**
     * Photos taken back with a queued message (the hub returns a Persona conversation's beside its queue, [restoredPhotos]),
     * kept in the draft as the photos they were. All of them stay, as on iOS: a message went out with at most four, and the
     * draft's four-place limit is the pickers', not the send's.
     */
    internal fun restorePhotos(photos: List<ComposerAttachment>) {
        if (photos.isEmpty()) return
        beginEdit()
        attachments = attachments + photos
    }

    companion object {
        val Saver = listSaver<ChatComposerState, String>(
            save = { state -> listOf(state.value.text, JsonCodec.encodeToString(state.draft.storedAttachments()),
                state.sessionVersion?.let { JsonCodec.encodeToString(it) }.orEmpty(), state.hasPickedSavedPrompt.toString()) },
            restore = { values ->
                if (values.size == 3 || values.size == 4) ChatComposerState(ChatComposerDraft(values[0],
                    JsonCodec.decodeFromString<List<DraftAttachment>>(values[1]).map { it.composerAttachment() },
                    values[2].takeIf(String::isNotEmpty)?.let { JsonCodec.decodeFromString<DraftSessionVersion>(it) },
                    values.getOrNull(3).toBoolean()))
                else ChatComposerState(ChatComposerDraft(values.first(), values.drop(1).chunked(6).map {
                    ComposerAttachment(it[0], it[1], it[2], it[3].toBoolean(), it[4].ifEmpty { null }, it[5].ifEmpty { null })
                }))
            },
        )
    }
}

@Composable
fun rememberChatComposerState(initial: ChatComposerDraft = ChatComposerDraft()): ChatComposerState {
    val state = rememberSaveable(saver = ChatComposerState.Saver) { ChatComposerState(initial) }
    SideEffect { state.draftLoaded = true }
    return state
}

internal fun quoteComposerPath(path: String): String = ComposerMessage.quotePath(path)

private val composerContextTags = ProtocolCompatibility.contextTag

private data class MaskedContexts(val text: String, val blocks: List<Pair<String, String>>) {
    fun unmask(value: String): String {
        var restored = value
        blocks.forEach { (marker, context) -> restored = restored.replace(marker, context) }
        return restored
    }
}

/** Mask canonical and legacy context blocks so nested file or image tags stay inside their context, byte for byte. */
private fun maskComposerContexts(text: String): MaskedContexts {
    if (!hasContextTag(text)) return MaskedContexts(text, emptyList())
    val blocks = mutableListOf<Pair<String, String>>()
    var nonce = 0
    val masked = composerContextTags.replace(text) { match ->
        var marker: String
        do { marker = "\u0000${nonce++}\u0000" } while (marker in text)
        blocks += marker to match.value
        marker
    }
    return MaskedContexts(masked, blocks)
}

/** Parse user-visible context blocks (both canonical and legacy) while dropping internal hub metadata blocks. */
private fun parseComposerContexts(text: String): Pair<List<MessageContext>, String> {
    if (!hasContextTag(text)) return emptyList<MessageContext>() to text
    val (contexts, rest) = parseContexts(text)
    return contexts.filterNot { it.isInterjectContext() || it.kind == "skill-instructions" } to rest
}

private val composerImageTags = Regex("<attached_images>([\\s\\S]*?)</attached_images>")
private val composerImagePlaceholders = Regex("\\A(?:\\[Image #\\d+]\\s*)+")
// Photo-only words sent by current and older clients: wire data, not interface copy.
private val composerPhotoStandIns = setOf(
    "Please look at the image", "Please look at the images", "Please look at the photos", "See the images", "Please look at the video",
    "Please look at the attached files",
    "请看图片", // i18n-ignore
    "请看视频", // i18n-ignore
    "请看附件", // i18n-ignore
    "画像を見てください", // i18n-ignore
    "動画を見てください", // i18n-ignore
    "添付ファイルを見てください", // i18n-ignore
)
// Context- and forward-only stand-in words sent by current and older clients: wire data, not interface copy.
private val composerContextStandIns = setOf(
    "See the content above.", "See the context above.", "Refer to the content above.",
    "Please look at the forwarded message above.", "Please look at the forwarded messages above.",
    "参考上面的内容。", // i18n-ignore
    "请看上面转发的消息。", // i18n-ignore
    "请看上面转发的这些消息。", // i18n-ignore
    "上の内容を参考にしてください。", // i18n-ignore
    "上の内容を参照してください。", // i18n-ignore
    "上の転送されたメッセージを見てください。", // i18n-ignore
    "上に転送したメッセージを確認してください。", // i18n-ignore
)

/** How many photos a message carries as paths on the computer (its `<attached_images>` block): what a queued row says it carries. */
internal fun composerPhotoCount(text: String): Int = composerImagePaths(parseComposerContexts(text).second).size

private fun composerImagePaths(text: String): List<String> =
    composerImageTags.findAll(text).flatMap { it.groupValues[1].lineSequence() }
        .map { it.trim().removePrefix("path:").trim() }
        .filter { it.startsWith('/') || it.startsWith("~/") }.distinct().toList()

/**
 * [text] with its context blocks stripped, its photos as one [photo] mark ("[Image]") and, given a [video] mark ("[Video]"),
 * each `<persona-file>` (or legacy file) tag as that mark (a video) or its name, on lines of their own: the words that only
 * stand in for attachments or contexts are left out. When nothing besides contexts remains, falls back to [contextText].
 */
internal fun composerMessagePreview(
    text: String,
    photo: String,
    video: String? = null,
    contextText: (List<MessageContext>) -> String = { contextSummary(it).shown },
): String {
    val (contexts, withoutContexts) = parseComposerContexts(text)
    val (files, rest) = if (video == null) emptyList<AttachedFile>() to withoutContexts else parseAttachedFiles(withoutContexts)
    val marks = files.map { if (it.kind == "video") video.orEmpty() else it.name }
    val clean = rest.replace(composerImagePlaceholders, "")
    val hasImages = composerImagePaths(clean).isNotEmpty()
    val plain = clean.replace(composerImageTags, "").trim()
    val standIn = (plain in composerPhotoStandIns && (hasImages || marks.isNotEmpty())) ||
        (plain in composerContextStandIns && contexts.isNotEmpty())
    val body = when {
        standIn && hasImages -> photo
        standIn -> ""
        else -> clean.replace(composerImageTags, photo).trim()
    }
    val shown = if (marks.isEmpty()) body else (listOf(body) + marks).filter(String::isNotBlank).joinToString("\n")
    return shown.ifBlank { if (contexts.isNotEmpty()) contextText(contexts) else "" }
}

internal fun composerMessagePreview(text: String, photo: String, video: String? = null, resources: Resources): String =
    composerMessagePreview(text, photo, video) { contextSummary(it).resolve(resources) }

/** Secondary context caption for a queued row whose [preview] already shows user words or attachments. */
internal fun composerContextNote(
    text: String,
    preview: String,
    contextText: (List<MessageContext>) -> String = { contextSummary(it).shown },
): String? {
    val (contexts, _) = parseComposerContexts(text)
    if (contexts.isEmpty()) return null
    val summary = contextText(contexts)
    return summary.takeIf { it.isNotBlank() && it != preview }
}

/**
 * Clean text copied from a queued message (never raw internal context, file, or image XML tags): prefers what the user
 * wrote, else quoted context text (skipping whole-page markers), else the localized preview.
 */
internal fun composerMessageCopyText(
    text: String,
    photo: String,
    video: String? = null,
    contextText: (List<MessageContext>) -> String = { contextSummary(it).shown },
): String {
    val (contexts, withoutContexts) = parseComposerContexts(text)
    val (files, rest) = if (video == null) emptyList<AttachedFile>() to withoutContexts else parseAttachedFiles(withoutContexts)
    val clean = rest.replace(composerImagePlaceholders, "")
    val hasImages = composerImagePaths(clean).isNotEmpty()
    val plain = clean.replace(composerImageTags, "").trim()
    val standIn = (plain in composerPhotoStandIns && (hasImages || files.isNotEmpty())) ||
        (plain in composerContextStandIns && contexts.isNotEmpty())
    if (!standIn && plain.isNotEmpty()) return plain
    val quotedBodies = contexts.mapNotNull { it.body.takeIf { body -> body.isNotBlank() && body != ChatLinkContext.WHOLE } }
        .joinToString("\n\n")
    if (quotedBodies.isNotEmpty()) return quotedBodies
    return composerMessagePreview(text, photo, video, contextText)
}

internal fun composerMessageCopyText(text: String, photo: String, video: String? = null, resources: Resources): String =
    composerMessageCopyText(text, photo, video) { contextSummary(it).resolve(resources) }

@Composable
internal fun composerMessagePreview(text: String): String {
    val resources = LocalResources.current
    return composerMessagePreview(
        text,
        stringResource(R.string.chat_composer_photo_placeholder),
        stringResource(R.string.chat_composer_video_placeholder),
        resources,
    )
}

/** A video or ordinary file on the session's machine as the tag its message carries; null for a photo or until uploaded. */
fun ComposerAttachment.attachedFile(): AttachedFile? =
    path?.takeIf { !image }?.let { AttachedFile(if (video) "video" else "file", it, name, size ?: 0, duration) }
