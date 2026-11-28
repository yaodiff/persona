package persona.feature.chat

import persona.core.moduleapi.asMenuActions
import android.Manifest
import android.content.ActivityNotFoundException
import android.graphics.Bitmap
import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.testTagsAsResourceId
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.core.content.FileProvider
import persona.core.contracts.ImageLimits
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ComposerOperationException
import persona.core.contracts.api.DraftUploadState
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.dismissKeyboard
import persona.core.designsystem.icons.PhIcons
import persona.feature.chat.media.MediaShare
import java.io.File
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext

/** The upload boundary of an ordinary composer attachment; tests can complete it without native image decoding. */
typealias ComposerAttachmentUpload = suspend (ChatComposerService, SessionKey, ComposerAttachment, suspend (Long) -> Unit) -> String

/** How long an upload waits for the hub connection to be ready again before it fails (a setting, not measured). */
internal const val UPLOAD_READY_WAIT_MILLIS = 15_000L

@OptIn(androidx.compose.ui.ExperimentalComposeUiApi::class)
@Composable
fun ChatComposer(
    session: SessionKey,
    state: ChatComposerState,
    working: Boolean,
    sending: Boolean,
    canClear: Boolean,
    service: ChatComposerService?,
    /** An ordinary file's own destination, such as a Health conversation's reports; media never uses it. */
    uploadDestination: String? = null,
    onSend: () -> Unit,
    onCommand: (String) -> Unit,
    onNotice: (String) -> Unit,
    // Content queued outside the text field (selected page context) also makes the draft sendable.
    extraContent: Boolean = false,
    /** Persona's conversation, including a new one: its own command list, never an Agent session's quick commands. */
    assistantChat: Boolean = false,
    enabled: Boolean = true,
    placeholder: String = stringResource(R.string.chat_composer_placeholder),
    leading: (@Composable RowScope.() -> Unit)? = null,
    accessory: (@Composable ColumnScope.() -> Unit)? = null,
    /** Short chips sharing one wrapping row with [modeHint] above [accessory] (e.g., Brief/Board's launcher chip). */
    hintChips: (@Composable FlowRowScope.() -> Unit)? = null,
    modeHint: String? = null,
    onOverlay: (Boolean) -> Unit = {},
    visible: Boolean = true,
    /** Who /clear restarts, for its confirmation ("Claude will start a new session…"). */
    agentName: String = "Claude",
    /** False for a session whose source does not let the phone interrupt it: no stop while it works. */
    canInterrupt: Boolean = true,
    /** The @ menu's files and sessions (iOS PromptItems); null: no @ button and no list while typing @. */
    mentions: ComposerMentions? = null,
    /** A session picked in the @ menu, as the context that goes with the next message. */
    onMention: (ChatLinkContext) -> Unit = {},
    /** A task picked in the @ menu (Persona's conversations, [ComposerMentions.tasks]): the next message goes to it. */
    onMentionTask: (String) -> Unit = {},
    /** Whether the @ menu is open over the field (iOS `MentionMenuOpen`): New chat's page makes room for it. */
    onMentionOpen: (Boolean) -> Unit = {},
    /** /model in the "/" list, which opens the model choices instead of typing (Claude in a terminal pane). */
    onModel: (() -> Unit)? = null,
    /**
     * /permissions in the "/" list (#6513): opens the chat's permission mode menu with every mode, nothing sent. Null where
     * the chat has no mode picker, so the words are an ordinary message there.
     */
    onPermissions: (() -> Unit)? = null,
    onWorkflows: (() -> Unit)? = null,
    /** /loop in the "/" list, with a line under the box saying what it will do (Claude Code only, web `hasLoop`). */
    loop: Boolean = false,
    /**
     * What has to happen before anything can be sent ("Connect a model first", "Add a computer first": docs/assistant/onboarding.md 4.4): the send button says
     * it and [onBlocked] opens that step. The box still takes typing and keeps the draft; nothing is queued.
     */
    blockedLabel: String? = null,
    onBlocked: () -> Unit = {},
    useShellSurface: Boolean = false,
    /**
     * The model pill (#5055, Glass): at the right of the toolbar row, before the mic and Send, while the toolbar shows; it
     * gets how much room the composer has ([ModelPillWidth], the web's container rules). Null: none.
     */
    modelPill: (@Composable (ModelPillWidth) -> Unit)? = null,
    upload: ComposerAttachmentUpload? = null,
    wrapPlaceholder: Boolean = false,
    dictate: DictationControl? = rememberComposerDictation(session, state, visible && enabled, onNotice),
    /** The hub's picture limits for this session's agent (docs/protocol/protocol.md "Pictures in a message"). */
    imageLimits: ImageLimits = LocalHubImageLimits.current.forAgent(null),
) {
    val context = LocalContext.current
    val currentImageLimits by rememberUpdatedState(imageLimits)
    val performUpload: ComposerAttachmentUpload = upload ?: { uploadService, uploadSession, attachment, onStaged ->
        uploadAttachment(context, uploadService, uploadSession, attachment, destination = uploadDestination,
            limits = currentImageLimits, onStaged = onStaged)
    }
    val compactShell = useShellSurface && PersonaTheme.style == AppStyle.Glass
    val adaptiveProbePage = "chat:${session.path}"
    val resources = LocalResources.current
    val photosOnly = stringResource(R.string.chat_composer_photos_only)
    val videosOnly = stringResource(R.string.chat_composer_videos_only)
    val filesOnly = stringResource(R.string.chat_composer_files_only)
    SideEffect { state.photosOnlyText = photosOnly; state.videosOnlyText = videosOnly; state.filesOnlyText = filesOnly }
    val hasContent = state.hasContent || extraContent
    val scope = rememberCoroutineScope()
    // Picker callbacks can overlap while a provider is read; serialize them so each sees the places already taken.
    val pickerLock = remember { Mutex() }
    val uploadLimiter = remember(state, session, service) { ComposerUploadLimiter() }
    var menu by remember { mutableStateOf(false) }
    var confirmClear by remember { mutableStateOf(false) }
    var cameraDenied by remember { mutableStateOf(false) }
    val focusRequester = remember { androidx.compose.ui.focus.FocusRequester() }
    val focusManager = LocalFocusManager.current
    val keyboard = LocalSoftwareKeyboardController.current
    // A photo in the draft being marked up (iOS editingIndex); [editOpening] while it is read for the editor.
    var editing by remember { mutableStateOf<ComposerPhotoEdit?>(null) }
    var editOpening by remember { mutableStateOf(false) }
    // The draft full screen (#5688): offered once the field has grown to its line limit and scrolls.
    var expandedEditor by rememberSaveable { mutableStateOf(false) }
    val composerCovered = menu || confirmClear || cameraDenied || editing != null || expandedEditor
    LaunchedEffect(composerCovered, dictate) {
        if (composerCovered) dictate?.stopListening()
        onOverlay(composerCovered)
    }
    DisposableEffect(Unit) { onDispose { onOverlay(false) } }
    var cameraUri by rememberSaveable { mutableStateOf<String?>(null) }
    var interruptPending by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(working) { if (!working) interruptPending = false }
    // A photo taken here waits in cache/chat-captures until it leaves this draft: removed, or sent and cleared. Only the
    // draft's own captures go; the folder is never swept (another conversation's draft may still hold one).
    LaunchedEffect(state) {
        var before = state.attachments to state.photoEdits.values.map { it.sourceUri }
        snapshotFlow { state.attachments to state.photoEdits.filterKeys { id -> state.attachments.any { it.id == id } }.values.map { it.sourceUri } }.collect { now ->
            val gone = leftCaptures(before.first, now.first, context.packageName, before.second, now.second)
            before = now
            if (gone.isNotEmpty()) withContext(Dispatchers.IO) { gone.forEach { deleteCapture(context, it) } }
        }
    }

    // [media]: from the photo picker, the camera or the clipboard; pictures follow the hub's image limits (spilling
    // past count or body budget into file attachments when a computer is available) while videos keep their own cap of 4.
    fun select(uris: List<Uri>, media: Boolean, capture: Boolean = false) {
        scope.launch {
            pickerLock.withLock {
                val limits = currentImageLimits
                if (!media) {
                    var overflow = false
                    for (uri in uris) {
                        if (composerFileRoom(state.attachments) == 0) {
                            overflow = true
                            continue
                        }
                        try {
                            val attachment = withContext(Dispatchers.IO) { selectAttachment(context, uri, image = false, video = false) }
                            state.beginEdit()
                            state.attachments += attachment
                            attachment.error?.let(onNotice)
                        } catch (e: CancellationException) { throw e }
                        catch (_: Exception) { onNotice(resources.getString(R.string.chat_composer_attachment_unreadable)) }
                    }
                    if (overflow) onNotice(resources.getString(R.string.chat_composer_file_limit))
                    return@withLock
                }
                var videoOverflow = false
                val photoCandidates = mutableListOf<PhotoCandidate>()
                for (uri in uris) {
                    try {
                        val video = withContext(Dispatchers.IO) { isVideo(context, uri) }
                        if (video) {
                            if (composerVideoRoom(state.attachments) == 0) {
                                videoOverflow = true
                                if (capture) withContext(Dispatchers.IO) { deleteCapture(context, uri.toString()) }
                                continue
                            }
                            val attachment = withContext(Dispatchers.IO) { selectAttachment(context, uri, image = false, video = true) }
                            state.beginEdit()
                            state.attachments += attachment
                            attachment.error?.let(onNotice)
                        } else {
                            val attachment = withContext(Dispatchers.IO) { selectAttachment(context, uri, image = true, video = false) }
                            if (attachment.uploadState == DraftUploadState.Invalid) {
                                state.beginEdit()
                                state.attachments += attachment
                                attachment.error?.let(onNotice)
                            } else {
                                val scaled = withContext(Dispatchers.IO) { measurePhotoBytes(context, uri, limits) }
                                photoCandidates += PhotoCandidate(attachment, scaled)
                            }
                        }
                    } catch (e: CancellationException) { throw e }
                    catch (_: Exception) {
                        if (capture) withContext(Dispatchers.IO) { deleteCapture(context, uri.toString()) }
                        onNotice(resources.getString(R.string.chat_composer_attachment_unreadable))
                    }
                }
                if (photoCandidates.isNotEmpty()) {
                    val existing = refreshHeldPhotoSizes(context, state.attachments, limits) { id, bytes ->
                        state.updateAttachment(id) { if (it.scaledBytes == null) it.copy(scaledBytes = bytes) else it }
                    }
                    val plan = planComposerPhotos(existing, photoCandidates, limits, canSpill = service != null)
                    if (plan.added.isNotEmpty()) {
                        state.beginEdit()
                        state.attachments += plan.added
                    }
                    if (capture && plan.refused.isNotEmpty()) {
                        withContext(Dispatchers.IO) { plan.refused.forEach { deleteCapture(context, it.uri) } }
                    }
                    plan.notices(resources, limits).forEach(onNotice)
                }
                if (videoOverflow) onNotice(resources.getString(R.string.chat_composer_video_limit, MAX_COMPOSER_VIDEOS))
            }
        }
    }
    val photos = rememberLauncherForActivityResult(DynamicPickVisualMedia()) { select(it, true) }
    val files = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { select(it, false) }
    val camera = rememberLauncherForActivityResult(ActivityResultContracts.TakePicture()) { success ->
        cameraUri?.let { value ->
            if (success) select(listOf(Uri.parse(value)), true, capture = true)
            else runCatching { context.contentResolver.delete(Uri.parse(value), null, null) }
        }
        cameraUri = null
    }
    fun openCamera() {
        val folder = File(context.cacheDir, "chat-captures").apply { mkdirs() }
        val file = File.createTempFile("camera-", ".jpg", folder)
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.chat.files", file)
        cameraUri = uri.toString()
        try { camera.launch(uri) }
        catch (error: Exception) {
            file.delete(); cameraUri = null
            onNotice(resources.getString(if (error is ActivityNotFoundException) R.string.chat_composer_no_camera_app else R.string.chat_composer_camera_failed))
        }
    }
    // The app declares CAMERA (pairing scans codes), so Android starts another app's camera for it only once granted.
    val cameraPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) openCamera() else cameraDenied = true
    }
    fun takePhoto() {
        menu = false
        if (cameraAllowed(context)) openCamera()
        else try { cameraPermission.launch(Manifest.permission.CAMERA) } catch (_: Exception) { cameraDenied = true }
    }
    fun launchPicker(action: () -> Unit) {
        menu = false
        try { action() } catch (_: Exception) { onNotice(resources.getString(R.string.chat_composer_picker_failed)) }
    }

    // A picture on the clipboard, offered over the focused composer on Glass (iOS PasteImageChip): Paste copies it in as a photo.
    val clipboardImages = rememberClipboardImages(state.focused)
    fun pasteImages() {
        val clip = runCatching { clipboardImages.clipboard?.primaryClip }.getOrNull()
        clipboardImages.dismiss()
        if (clip == null) return
        scope.launch {
            val pasted = withContext(Dispatchers.IO) { pastedImages(context, clip, currentImageLimits.perMessage + MAX_COMPOSER_FILES) }
            if (pasted.isEmpty()) onNotice(resources.getString(R.string.chat_composer_attachment_unreadable))
            else select(pasted, media = true, capture = true)
        }
    }

    // A tap on a photo that is on the computer: the editor opens on it, and "Done" puts the marked-up copy in its place.
    fun editPhoto(photo: ComposerAttachment) {
        if (editing != null || editOpening || sending) return
        editOpening = true
        scope.launch {
            try {
                // An edited photo opens from its source with its edits, still undoable; its source gone, it starts over on itself.
                val maxSide = currentImageLimits.maxSide
                val prior = state.photoEdits[photo.id]
                val source = prior?.let { runCatching { withContext(Dispatchers.IO) { decodePhoto(context, Uri.parse(it.sourceUri), maxSide) } }.getOrNull() }
                editing = if (prior != null && source != null) ComposerPhotoEdit(photo, source, prior.sourceUri, prior)
                    else ComposerPhotoEdit(photo, withContext(Dispatchers.IO) { decodePhoto(context, Uri.parse(photo.uri), maxSide) })
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { onNotice(resources.getString(R.string.chat_attach_edit_unreadable)) }
            finally { editOpening = false }
        }
    }
    // "Done": the editor spins while the new photo is written (JPEG like a picked photo) and uploaded, then closes. Only an
    // upload that worked replaces the original; the old path leaves the message with it.
    fun finishEdit(edit: ComposerPhotoEdit, result: Bitmap, done: ImageEditState) {
        if (result === edit.source && edit.prior == null) { editing = null; return } // nothing done to a photo never edited
        scope.launch {
            var edited: ComposerAttachment? = null
            var kept = false
            try {
                val limits = currentImageLimits
                edited = MediaShare.writeEditedPhoto(context, result, limits)
                val carried = PhotoEdits(edit.sourceUri, done).takeUnless { done.edits.isEmpty }
                var stagedBytes = edited.scaledBytes
                val outcome = state.replaceWithEdited(edit.original, edited, carried) { photo ->
                    uploadLimiter.withSlot {
                        uploadAttachment(context, checkNotNull(service), session, photo, limits = limits) { size ->
                            if (size > 0L) stagedBytes = size
                        }
                    }
                }
                kept = outcome == PhotoEditOutcome.Replaced
                if (kept && stagedBytes != null) {
                    state.updateAttachment(edited.id) { it.copy(size = stagedBytes, scaledBytes = stagedBytes) }
                }
                if (outcome is PhotoEditOutcome.Failed) onNotice(resources.getString(R.string.chat_attach_edit_failed))
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { onNotice(resources.getString(R.string.chat_attach_edit_failed)) }
            finally {
                // A copy that did not make it into the draft is not left in cache/chat-captures.
                if (!kept) edited?.let { withContext(NonCancellable + Dispatchers.IO) { deleteCapture(context, it.uri) } }
                if (editing === edit) editing = null
            }
        }
    }

    state.attachments.forEach { attachment ->
        key(attachment.id) {
            LaunchedEffect(attachment.id, attachment.error, service, uploadDestination, upload) {
                if (attachment.uploadState == DraftUploadState.Invalid) return@LaunchedEffect
                // Once the computer has it, a revoked local picker grant cannot make the attachment unsendable.
                if (attachment.path != null) return@LaunchedEffect
                if (!withContext(Dispatchers.IO) { attachmentReadable(context, attachment) }) {
                    val reason = resources.getString(R.string.chat_composer_attachment_gone)
                    state.updateAttachment(attachment.id) {
                        it.copy(uploadState = DraftUploadState.Invalid, error = reason)
                    }
                    onNotice(reason)
                    return@LaunchedEffect
                }
                if (attachment.error != null || service == null) return@LaunchedEffect
                try {
                    val path = uploadLimiter.withSlot {
                        state.updateAttachment(attachment.id) { it.copy(uploadState = DraftUploadState.Uploading) }
                        performUpload(service, session, attachment) { size ->
                            state.updateAttachment(attachment.id) {
                                if (it.image) {
                                    val staged = size.takeIf { v -> v > 0L }
                                    it.copy(size = staged ?: it.size, scaledBytes = staged ?: it.scaledBytes)
                                } else it.copy(size = size)
                            }
                        }
                    }
                    if (state.attachments.any { it.id == attachment.id }) {
                        state.updateAttachment(attachment.id) { it.copy(path = path, uploadState = DraftUploadState.Uploaded) }
                    }
                } catch (e: CancellationException) { throw e }
                catch (e: Exception) {
                    val reason = (e as? ComposerOperationException)?.message ?: resources.getString(R.string.chat_composer_upload_failed)
                    state.updateAttachment(attachment.id) {
                        it.copy(error = reason, uploadState = DraftUploadState.Failed)
                    }
                }
            }
        }
    }

    LaunchedEffect(visible) { if (!visible) { menu = false; confirmClear = false; cameraDenied = false; editing = null; expandedEditor = false } }
    editing?.let { edit ->
        var done by remember(edit) { mutableStateOf(ImageEditState()) }
        key(edit) { ImageEditor(edit.source, onCancel = { if (editing === edit) editing = null }, onDone = { finishEdit(edit, it, done) },
            start = edit.prior?.state ?: ImageEditState(), onEdited = { done = it }) }
    }
    if (!visible) return
    if (AdaptiveProbe.enabled) {
        AdaptiveComposerProbe(adaptiveProbePage, state.value, state.attachments.map { it.id to it.uploadState.name }, state.draftLoaded)
    }
    LaunchedEffect(state.focusRequest) { if (state.focusRequest > 0) focusRequester.requestFocus() }
    // A read-only session (enabled = false) offers nothing to type with: no +, no mic, no "/" list, no send.
    // The main conversation's /clear (#5100): offered while it works too (the hub stops the turn), and it never asks first.
    val mainClear = LocalMainChatClear.current?.takeIf { enabled }
    // Both the "/" list and the + sheet offer only what an Assistant conversation runs, never an Agent's quick messages.
    val useMainCommands = assistantChat || mainClear != null
    val slashDraft = state.value.text.takeIf { enabled && !state.hasPickedSavedPrompt }.orEmpty()
    val saved = matchingSavedPrompts(LocalSavedPrompts.current, slashDraft)
    val suggestions = if (useMainCommands) {
        SlashCommand.mainSuggestions(slashDraft, clear = mainClear != null, resources = resources,
            permissions = onPermissions != null)
    } else {
        SlashCommand.suggestions(slashDraft, canClear && !working, resources, model = onModel != null, loop = loop,
            permissions = onPermissions != null, keepExact = saved.isNotEmpty(),
            workflows = onWorkflows != null && canOpenWorkflows(state, extraContent))
    }
    val hint = if (loop) loopHint(slashDraft, resources) else null
    // @: the word being typed just before the caret, while nothing is selected and no "/" list is open.
    val selection = state.value.selection
    val mention = if (enabled && mentions != null && suggestions.isEmpty() && selection.collapsed) mentionAt(state.value.text, selection.start) else null
    val mentionOpen = mention != null
    LaunchedEffect(mentionOpen) { onMentionOpen(mentionOpen) }
    DisposableEffect(Unit) { onDispose { onMentionOpen(false) } }
    // A typed folder (New chat's page): read from the computer while the list is open.
    val browsing = rememberMentionListing(mentions, mention?.query)
    val mentionRows = remember(mentions, mention?.query, browsing.listing) {
        if (mentions != null && mention != null) mentionRows(mentions, mention.query, browsing.listing) else emptyList()
    }
    val usedFile = stringResource(R.string.chat_mention_used_file)
    val folderWord = stringResource(R.string.chat_mention_folder)
    val sessionFolderWord = stringResource(R.string.chat_mention_session_folder)
    // With tasks in the list each group says what a pick does: iOS's "Message a Task", then the files and sessions.
    val taskSection = stringResource(R.string.chat_tasks_mention_title)
    val otherSection = stringResource(R.string.chat_mention).takeIf { mentionRows.any { it is MentionRow.Task } }
    val mentionItems = mentionRows.map { row ->
        when (row) {
            is MentionRow.Task -> ComposerSuggestion(row.task.title, row.task.place, key = row.key, flexible = true,
                section = taskSection, status = row.task.status)
            is MentionRow.File -> {
                val relative = row.insert.removePrefix("@").trimEnd()
                ComposerSuggestion(relative.substringAfterLast('/'), relative.substringBeforeLast('/', "").ifEmpty { usedFile }, key = row.key,
                    flexible = true, section = otherSection)
            }
            is MentionRow.Quote -> ComposerSuggestion(row.session.displayTitle,
                stringResource(R.string.chat_mention_session_detail, sessionMentionDetail(row.session)), key = row.key, flexible = true,
                section = otherSection)
            // An entry of a folder: its name, a folder's with a slash, and the folder typed so far (iOS `MentionRows`); one of
            // the session's own folder says so ("Folder" for a folder, "Session folder" for a file), among the other groups.
            is MentionRow.Entry -> ComposerSuggestion(if (row.folder) "${row.name}/" else row.name,
                if (!row.top) row.typed else if (row.folder) folderWord else sessionFolderWord, key = row.key, flexible = true,
                section = otherSection.takeIf { row.top })
        }
    }
    // `@` at the caret, whose list opens: the @ button's, and the + menu's Mention a file or session (iOS `typeMention`).
    fun startMention() { state.beginEdit(); state.value = typeMention(state.value); state.focusRequest++ }
    fun pickMention(picked: ComposerSuggestion) {
        val token = mention ?: return
        val row = mentionRows.firstOrNull { it.key == picked.key } ?: return
        state.beginEdit()
        when (row) {
            // The @word goes; the next message goes to the task (iOS `TaskMentionList`), as its Message sets it.
            is MentionRow.Task -> { onMentionTask(row.task.id); state.value = replaceMention(state.value, token, "") }
            is MentionRow.File -> state.value = replaceMention(state.value, token, row.insert)
            // A folder stays typed with its slash, and the list browses it.
            is MentionRow.Entry -> state.value = replaceMention(state.value, token, row.insert)
            // A session goes with the message as a quote, not as text (web pickMention).
            is MentionRow.Quote -> { onMention(sessionQuote(row.session, resources)); state.value = replaceMention(state.value, token, "") }
        }
        state.focusRequest++
    }
    val extensionPrompts = if (enabled) LocalExtensionPrompts.current else emptyList()
    val quick = ExtensionPromptText.matching(extensionPrompts, slashDraft)
    val savedSection = stringResource(R.string.chat_slash_saved_prompts)
    val slashItems = suggestions.map { ComposerSuggestion(it.word, it.detail, destructive = it.clear && mainClear == null) } +
        saved.map { ComposerSuggestion("/${it.name}", it.text.lineSequence().first(), key = "saved:${it.id}",
            flexible = true, section = savedSection) } +
        quick.map { ComposerSuggestion(it.title, it.extensionName, key = "extension:${it.extension}/${it.id}", flexible = true) }
    // The main conversation's /clear: the box empties and the hub clears it; /clear goes back into the box if it did not.
    fun clearMain(clear: MainChatClear) {
        state.beginEdit()
        state.value = TextFieldValue()
        clear.clear { state.value = TextFieldValue(SlashCommand.CLEAR, TextRange(SlashCommand.CLEAR.length)) }
    }
    // /clear sent alone in the main conversation clears it instead of going to Persona (web `isClearCommand`);
    // /permissions typed out opens the mode picker (shared with LargeWindowComposer's hardware send path).
    fun send() {
        if (dictate?.active == true) return
        dispatchComposerSend(state, extraContent, mainClear, onPermissions, onWorkflows, onSend)
    }
    // Picking a quick message puts its words in the box to send; /clear empties the box and asks first (the main
    // conversation's clears at once).
    fun pick(command: SlashCommand) {
        state.beginEdit()
        when {
            command.clear && mainClear != null -> clearMain(mainClear)
            command.clear -> { state.value = TextFieldValue(); confirmClear = true }
            command.model -> { state.value = TextFieldValue(); onModel?.invoke() }
            command.permissions -> { state.value = TextFieldValue(); onPermissions?.invoke() }
            command.workflows && onWorkflows != null && canOpenWorkflows(state, extraContent) -> {
                state.value = TextFieldValue()
                onWorkflows()
            }
            else -> state.value = TextFieldValue(command.text, TextRange(command.text.length))
        }
    }

    // The field grows through six lines. On a compact Agent chat the editor is also a shortcut while typing or for a
    // multiline draft, as on the iPhone. The text keeps clear of the button only while it shows.
    val growLines = ComposerGrowLines
    var lineCount by remember { mutableIntStateOf(1) }
    val offersExpand = enabled && (composerOffersExpand(lineCount, growLines) ||
        compactShell && (state.focused || '\n' in state.value.text))
    LaunchedEffect(enabled) { if (!enabled) expandedEditor = false }
    val canSend = enabled && state.ready && !sending && dictate?.active != true
    val homeAssistant = LocalHomeChrome.current?.let { it.items.firstOrNull()?.key == it.selected } == true
    val fullToolbar = !homeAssistant || state.focused || hasContent
    // Expand once on focus, not when the first character arrives. ComposerFrame keeps the field in the same row across
    // this change, so moving the controls cannot dispose the field and cancel the tap or the IME.
    val expanded = state.focused || hasContent
    val composerPlaceholder = if (homeAssistant) stringResource(R.string.chat_composer_ask_persona, LocalPersonaName.current) else placeholder

    // The composer's own width, for how much of the model pill fits (the web measures its composer card the same way).
    val density = androidx.compose.ui.platform.LocalDensity.current
    var composerDp by remember { mutableFloatStateOf(0f) }
    ComposerFrame(
        modifier = if (!compactShell && modelPill != null) Modifier.onSizeChanged { composerDp = with(density) { it.width.toDp().value } } else Modifier,
        // Blank space around the field (its padding, beside the buttons) types too: one tap, not a hunt for the line.
        onBlankTap = { if (enabled) { focusRequester.requestFocus(); keyboard?.show() } },
        expanded = expanded,
        useShellSurface = useShellSurface,
        leading = {
            if (!compactShell || enabled)
                JIconButton(PhIcons.Plus, stringResource(R.string.chat_composer_add), { menu = true }, enabled = !sending && enabled)
            if (compactShell && state.focused) {
                JIconButton(PersonaIcons.dismissKeyboard, stringResource(R.string.chat_composer_hide_keyboard), {
                    focusManager.clearFocus()
                    keyboard?.hide()
                }, raised = false)
            }
            if (expanded) {
                // The + sheet opens the same mention picker. Keep the direct entry while the + sheet is disabled by sending.
                if (mentions != null && (!compactShell || sending))
                    JIconButton(PhIcons.At, stringResource(R.string.chat_mention), ::startMention, enabled = enabled)
                leading?.invoke(this)
            }
        },
        trailing = {
            if (expanded && compactShell) {
                if (enabled && modelPill != null) {
                    // A model pill that cannot fit its touch target would overlap a neighbor.
                    BoxWithConstraints(Modifier.weight(1f), contentAlignment = Alignment.CenterEnd) {
                        when {
                            maxWidth >= Tokens.Size.box80 -> modelPill(ModelPillWidth.AgentComposer)
                            maxWidth >= Tokens.MinTouch.android -> modelPill(ModelPillWidth.Compact)
                            else -> Unit
                        }
                    }
                } else Spacer(Modifier.weight(1f))
            } else if (expanded && enabled) modelPill?.invoke(ModelPillWidth.of(composerDp))
            // The mic stays (iOS ShellComposerBar): more can be dictated after what is typed. Send beside it once there is
            // something to send, else a stop while the agent works; nothing else while the box is empty. On Glass the button
            // changing grows and shrinks as iOS's does (ComposerActionSlot, #4210 T3).
            dictate?.let { DictationMic(it, stringResource(R.string.chat_composer_dictate), enabled = !sending) }
            if (blockedLabel != null && enabled) BlockedSendButton(blockedLabel, onBlocked)
            else ComposerActionSlot(
                when {
                    hasContent && (!compactShell || enabled) -> ComposerAction(SendKind.Send, enabled = canSend, loading = sending)
                    working && canInterrupt && (!compactShell || enabled) ->
                        ComposerAction(SendKind.Stop, enabled = !sending && !interruptPending && service != null)
                    dictate == null && (!compactShell || enabled && sending) -> ComposerAction(SendKind.Send, enabled = false, loading = sending)
                    else -> null
                },
                // A lambda, not `::send`: references to a local function compare equal whatever they capture, so the slot was
                // skipped and kept calling the `send` from before a quote joined the draft (New chat's first message lost it).
                onSend = { send() },
                onStop = {
                    if (service != null) {
                        interruptPending = true
                        scope.launch {
                            try { service.interrupt(session); onNotice(resources.getString(R.string.chat_composer_interrupt_requested)) }
                            catch (e: CancellationException) { throw e }
                            catch (_: Exception) { onNotice(resources.getString(R.string.chat_composer_interrupt_unknown)) }
                        }
                    }
                },
            )
        },
        accessory = {
            CompactAccessoryStack(gap = Tokens.Space.xs) {
                dictate?.let { DictationStatus(it) }
                if (state.oldVersion) Text(stringResource(R.string.chat_composer_old_version), style = PersonaTheme.type.caption,
                    color = PersonaTheme.palette.cautionText)
                if (PersonaTheme.style == AppStyle.Glass && enabled && !sending && service != null && state.focused && clipboardImages.offered)
                    PasteImageChip(onPaste = ::pasteImages, onDismiss = clipboardImages::dismiss)
                ComposerAttachmentPreviews(state, service, sending, enabled, onEdit = ::editPhoto)
                if (fullToolbar && (hintChips != null || modeHint != null)) {
                    ComposerMetaStrip {
                        hintChips?.invoke(this)
                        modeHint?.let {
                            ComposerMetaChip(
                                text = it,
                                icon = PhIcons.SlidersHorizontal,
                                modifier = Modifier.testTag("chat-composer-mode-hint"),
                                onClick = onPermissions?.takeIf { enabled },
                            )
                        }
                    }
                }
                if (fullToolbar && accessory != null) {
                    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) { accessory.invoke(this) }
                }
                if (working && hasContent && enabled) Text(stringResource(R.string.chat_composer_will_queue), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                ComposerSuggestions(slashItems,
                    onPick = { row ->
                        when {
                            row.key.startsWith("saved:") -> saved.firstOrNull { "saved:${it.id}" == row.key }?.let { state.fillSavedPrompt(it.text) }
                            row.key.startsWith("extension:") -> quick.firstOrNull { "extension:${it.extension}/${it.id}" == row.key }?.let { state.fillQuickPrompt(it) }
                            else -> suggestions.firstOrNull { it.word == row.key }?.let(::pick)
                        }
                    },
                    modifier = Modifier.padding(vertical = Tokens.Space.xs))
                // The @ menu: Persona's open tasks, files this chat used, the session folder's entries, then other sessions; a word
                // that matches nothing says so. On Glass it grows in and out in iOS's state motion, rising from the field, as the
                // lines over New chat's composer step aside for it (`NeedsStepAside`).
                Column(Modifier.fillMaxWidth().then(if (PersonaTheme.style == AppStyle.Glass && !PersonaTheme.reduceMotion)
                    Modifier.animateContentSize(PersonaTheme.motion.enter(), Alignment.BottomStart) else Modifier)) {
                    ComposerSuggestions(mentionItems, onPick = ::pickMention, modifier = Modifier.padding(vertical = Tokens.Space.xs))
                    if (mention != null && mentionItems.isEmpty()) Text(stringResource(when {
                            browsing.reading -> R.string.chat_mention_reading_folder
                            browsing.browse != null -> R.string.chat_mention_no_match_folder
                            else -> R.string.chat_mention_no_match
                        }), Modifier.padding(vertical = Tokens.Space.xs), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                }
                hint?.let { Text(it.text, style = PersonaTheme.type.caption, color = if (it.missing) PersonaTheme.palette.cautionText else PersonaTheme.palette.ink2) }
            }
        },
    ) {
        ReadingContent {
            BasicTextField(state.value, { state.beginEdit(); state.value = it },
                modifier = Modifier.fillMaxWidth().then(if (AdaptiveProbe.enabled) Modifier.semantics { testTagsAsResourceId = true }
                    .testTag("chat-composer-field") else Modifier)
                    .then(if (offersExpand) Modifier.padding(end = ComposerExpandInset) else Modifier)
                    .focusRequester(focusRequester).onFocusChanged {
                        state.focused = it.isFocused
                        if (AdaptiveProbe.enabled) AdaptiveProbe.publishComposerFocus(adaptiveProbePage, it.isFocused)
                    },
                maxLines = growLines, onTextLayout = { lineCount = it.lineCount },
                enabled = enabled, textStyle = PersonaTheme.type.body.copy(color = PersonaTheme.palette.ink),
                cursorBrush = SolidColor(PersonaTheme.palette.ink),
                decorationBox = { field ->
                    Box {
                        if (state.value.text.isEmpty()) InterfaceContent {
                            Text(if (working && enabled && !homeAssistant) stringResource(R.string.chat_composer_placeholder_working) else composerPlaceholder,
                                style = PersonaTheme.type.body, color = PersonaTheme.palette.ink3, maxLines = if (wrapPlaceholder) growLines else if (compactShell) 1 else 2,
                                overflow = TextOverflow.Ellipsis)
                        }
                        field()
                    }
                })
        }
        if (offersExpand) ComposerExpandButton(expanded = false, onClick = { expandedEditor = true }, modifier = Modifier.align(Alignment.TopEnd))
    }
    // Same draft and caret as the field; collapsing puts the caret back in the field. Sending closes it.
    if (expandedEditor) ExpandedComposer(state.value, { state.beginEdit(); state.value = it }, composerPlaceholder,
        onCollapse = { expandedEditor = false; state.focusRequest++ }, enabled = enabled) {
        SendButton(onClick = { expandedEditor = false; send() }, enabled = canSend && hasContent && blockedLabel == null, loading = sending)
    }
    // The modules' composer actions (#13847), asked only while the menu is open.
    val moduleActions = if (menu) persona.core.moduleapi.LocalModuleActions.current.actions(persona.core.moduleapi.MenuSlot.ComposerActions,
        persona.core.moduleapi.ModuleActionTarget(session.machineId, session.sessionId)) else emptyList()
    if (menu) ChatActionsSheet(onDismiss = { menu = false }, title = stringResource(R.string.chat_composer_menu_title), actions = buildList {
        // Photos picks pictures (hub image limits + overflow file room) and videos (up to 4) sized by what is left.
        val mediaRoom = composerMediaPickerRoom(state.attachments, currentImageLimits, canSpill = service != null)
        val photoRoom = composerCanAddPhoto(state.attachments, currentImageLimits, canSpill = service != null)
        add(JAction(stringResource(R.string.chat_composer_photos), enabled = service != null && mediaRoom > 0) {
            launchPicker { photos.launch(PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageAndVideo) to mediaRoom) }
        })
        add(JAction(stringResource(R.string.chat_composer_take_photo), enabled = service != null && photoRoom) { takePhoto() })
        add(JAction(stringResource(R.string.chat_composer_files), enabled = service != null) { launchPicker { files.launch(arrayOf("*/*")) } })
        // iOS's Mention a File or Session, after the attachments (`AttachMoreSection.common`): where the composer has an @ list.
        if (mentions != null) add(JAction(stringResource(R.string.chat_mention)) { menu = false; startMention() })
        // An Agent's quick messages go out at once and /clear asks first; the main sheet has only its in-place clear.
        extensionPrompts.forEach { prompt -> add(JAction(prompt.title) { menu = false; state.fillQuickPrompt(prompt) }) }
        if (!useMainCommands) {
            SlashCommand.all(resources).forEach { command -> add(JAction(command.title) { menu = false; onCommand(command.text) }) }
            if (canClear && !working) add(JAction(SlashCommand.clearCommand(resources).title, destructive = true) { menu = false; confirmClear = true })
        } else if (mainClear != null) add(JAction(SlashCommand.mainClearCommand(resources).title) { menu = false; clearMain(mainClear) })
        addAll(moduleActions.asMenuActions { menu = false })
    })
    if (confirmClear) JDialog(onDismiss = { confirmClear = false }, title = stringResource(R.string.chat_composer_clear_title),
        text = stringResource(R.string.chat_composer_clear_text, agentName),
        confirm = stringResource(R.string.chat_composer_clear) to { confirmClear = false; onCommand(SlashCommand.CLEAR) }, destructive = true)
    if (cameraDenied) JDialog(onDismiss = { cameraDenied = false }, title = stringResource(R.string.chat_composer_camera_title),
        text = stringResource(R.string.chat_composer_camera_text, appLabel(context)),
        confirm = stringResource(R.string.chat_composer_open_settings) to { cameraDenied = false; openAppSettings(context) })
}

/** Stacks non-zero-height composer accessory items with [gap], skipping zero-height placeholders so empty slots add no gap. */
@Composable
private fun CompactAccessoryStack(
    gap: Dp,
    content: @Composable () -> Unit,
) {
    Layout(content = content) { measurables, constraints ->
        val childConstraints = constraints.copy(minWidth = 0, minHeight = 0)
        val visible = measurables.map { it.measure(childConstraints) }.filter { it.height > 0 }
        if (visible.isEmpty()) {
            layout(0, 0) {}
        } else {
            val gapPx = gap.roundToPx()
            val width = visible.maxOf { it.width }.coerceIn(constraints.minWidth, constraints.maxWidth)
            val height = (visible.sumOf { it.height } + gapPx * (visible.size - 1))
                .coerceIn(constraints.minHeight, constraints.maxHeight)
            layout(width, height) {
                var y = 0
                visible.forEach { placeable ->
                    placeable.placeRelative(0, y)
                    y += placeable.height + gapPx
                }
            }
        }
    }
}

/** True when [dispatchComposerSend] opens the permission picker instead of sending the draft. */
internal fun isPermissionsCommand(state: ChatComposerState, extraContent: Boolean, onPermissions: (() -> Unit)?): Boolean =
    onPermissions != null && !state.hasPickedSavedPrompt && SlashCommand.isPermissions(state.value.text) &&
        state.attachments.isEmpty() && !extraContent

private fun canOpenWorkflows(state: ChatComposerState, extraContent: Boolean): Boolean =
    !state.hasPickedSavedPrompt && state.attachments.isEmpty() && !extraContent

/**
 * Shared send dispatcher for both touch/IME send (`ChatComposer`) and hardware keyboard send (`LargeWindowComposer`):
 * intercepts `/clear` in the main conversation and bare `/permissions` or `/workflows` queries.
 */
internal fun dispatchComposerSend(
    state: ChatComposerState,
    extraContent: Boolean,
    mainClear: MainChatClear?,
    onPermissions: (() -> Unit)?,
    onWorkflows: (() -> Unit)? = null,
    onSend: () -> Unit,
) {
    when {
        mainClear != null && !state.hasPickedSavedPrompt && isMainClearCommand(state.value.text, state.attachments.size, extraContent) -> {
            state.beginEdit()
            state.value = TextFieldValue()
            mainClear.clear { state.value = TextFieldValue(SlashCommand.CLEAR, TextRange(SlashCommand.CLEAR.length)) }
        }
        isPermissionsCommand(state, extraContent, onPermissions) -> {
            state.beginEdit()
            state.value = TextFieldValue()
            onPermissions?.invoke()
        }
        onWorkflows != null && canOpenWorkflows(state, extraContent) && SlashCommand.isWorkflows(state.value.text) -> {
            state.beginEdit()
            state.value = TextFieldValue()
            onWorkflows()
        }
        else -> onSend()
    }
}
