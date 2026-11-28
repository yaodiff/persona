package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.core.content.FileProvider
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.HubScope
import persona.core.contracts.JsonCodec
import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.domain.AttachedFile
import kotlinx.serialization.encodeToString

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChatComposerStateTest {
    private val res = ApplicationProvider.getApplicationContext<Context>().resources
    @Test fun structuredDraftRestoresPendingInvalidAndUploadedFilesAndKeepsOriginalHistory() {
        val version = DraftSessionVersion("original", "pane")
        val draft = ChatComposerDraft("检查附件", listOf(
            ComposerAttachment("image", "image.jpg", "content://example.com/image", true,
                size = 123, uploadState = DraftUploadState.Uploading),
            ComposerAttachment("file", "notes.txt", "content://example.com/file", false,
                "/tmp/notes.txt", size = 456, uploadState = DraftUploadState.Uploaded),
            ComposerAttachment("invalid", "missing.txt", "content://example.com/missing", false,
                error = "附件已失效", size = 789, uploadState = DraftUploadState.Invalid),
        ), version)
        val stored = JsonCodec.encodeToString(draft.storedAttachments())
        val snapshot = DraftSnapshot(HubScope("hub", "pairing"), DraftKey.Chat(SessionKey("machine", "session")),
            draft.text, 7, JsonCodec.decodeFromString(stored), version)
        val state = ChatComposerState(snapshot.composerDraft())
        assertEquals(draft, state.draft)
        assertFalse(state.ready)
        state.currentSessionVersion = version
        assertFalse(state.oldVersion)
        state.currentSessionVersion = version.copy(replaced = true)
        assertTrue(state.oldVersion)
        state.currentSessionVersion = DraftSessionVersion("replacement", "pane")
        assertTrue(state.oldVersion)
        state.insert("补充", false)
        assertEquals(version, state.draft.sessionVersion)
        state.attachments = state.attachments.filter { it.id == "file" }
        assertTrue(state.ready)
        assertEquals("检查附件 补充\n\n<persona-file kind=\"file\" path=\"/tmp/notes.txt\" name=\"notes.txt\" size=\"456\"/>", state.message())
        state.clearIfUnchanged(state.draft)
        state.insert("新草稿", false)
        assertFalse(state.oldVersion)
    }

    @Test fun insertsAtSelectionAndRetainsDraftUntilAllUploadsSucceed() {
        val state = ChatComposerState(ChatComposerDraft("看这里"))
        state.value = TextFieldValue("看这里", TextRange(1, 3))
        state.insert(quoteComposerPath("/tmp/a'b c.txt"), true)
        assertEquals("看 '/tmp/a'\\''b c.txt'", state.value.text)
        state.insert("末尾", false)
        assertTrue(state.value.text.endsWith(" 末尾"))
        state.attachments = listOf(ComposerAttachment("photo", "照片", "content://example.com/photo", true, error = "上传失败"))
        assertFalse(state.ready)
        assertThrows(IllegalStateException::class.java) { state.message() }
        assertTrue(state.value.text.contains("看"))
        state.attachments = state.attachments.map { it.copy(path = "/tmp/photo.jpg", error = null) }
        assertTrue(state.message().endsWith("<attached_images>\n/tmp/photo.jpg\n</attached_images>"))
        val restored = ChatComposerState().apply { editMessage(state.message()) }
        assertEquals(state.message(), restored.message())
        assertFalse(composerMessagePreview(state.message(), "[图片]").contains("<attached_images>"))
        val sent = state.draft
        state.insert("补充", false)
        state.clearIfUnchanged(sent)
        assertTrue(state.hasContent)
        state.clearIfUnchanged(state.draft)
        assertFalse(state.hasContent)
    }

    @Test fun independentAttachmentUpdatesDoNotRestoreAnotherUploadsStaleState() = runTest {
        val first = ComposerAttachment("first", "first.png", "content://example.com/first", true)
        val second = ComposerAttachment("second", "second.png", "content://example.com/second", true)
        val state = ChatComposerState(ChatComposerDraft(attachments = listOf(first, second)))
        val firstRead = CompletableDeferred<Unit>()
        val finish = CompletableDeferred<Unit>()
        fun upload(attachment: ComposerAttachment) = async(Dispatchers.Default + SupervisorJob()) {
            // Each upload used to read and later replace this whole list. Per-id updates must keep the other result.
            state.updateAttachment(attachment.id) { it.copy(uploadState = DraftUploadState.Uploading) }
            firstRead.complete(Unit)
            finish.await()
            state.updateAttachment(attachment.id) {
                it.copy(path = "/up/${attachment.name}", uploadState = DraftUploadState.Uploaded)
            }
        }
        val uploads = listOf(upload(first), upload(second))
        firstRead.await()
        finish.complete(Unit)
        uploads.awaitAll()

        assertEquals(listOf("/up/first.png", "/up/second.png"), state.attachments.map { it.path })
        assertEquals(listOf("first", "second"), state.attachments.map { it.id })
        assertTrue(state.attachments.all { it.uploadState == DraftUploadState.Uploaded })
        assertTrue(state.ready)
        assertTrue(state.message().endsWith("<attached_images>\n/up/first.png\n/up/second.png\n</attached_images>"))
    }

    @Test fun photosAloneSayPleaseLookAndKeepTheirTag() {
        val photo = ComposerAttachment("photo", "photo.jpg", "content://example.com/photo", true, "/tmp/photo.jpg",
            uploadState = DraftUploadState.Uploaded)
        val tag = "\n\n<attached_images>\n/tmp/photo.jpg\n</attached_images>"
        assertEquals("请看图片$tag", ChatComposerState(ChatComposerDraft("", listOf(photo))).apply { photosOnlyText = "请看图片" }.message())
        assertEquals("请看图片$tag", ChatComposerState(ChatComposerDraft("  \n", listOf(photo))).apply { photosOnlyText = "请看图片" }.message())
        assertEquals("看这里$tag", ChatComposerState(ChatComposerDraft("看这里", listOf(photo))).message())
        // Photos keep their stand-in; the file is a tag, not a quoted path on the words' line.
        val file = ComposerAttachment("file", "notes.txt", "content://example.com/file", false, "/tmp/notes.txt", size = 12,
            uploadState = DraftUploadState.Uploaded)
        assertEquals("请看图片\n\n${file.attachedFile()!!.toXml()}$tag",
            ChatComposerState(ChatComposerDraft("", listOf(file, photo))).apply {
                photosOnlyText = "请看图片"; filesOnlyText = "请看附件"
            }.message())
        assertEquals("只有字", ChatComposerState(ChatComposerDraft("只有字")).message())
        // Editing it again keeps the same message, words and photo.
        val sent = ChatComposerState(ChatComposerDraft("", listOf(photo))).apply { photosOnlyText = "请看图片" }.message()
        val restored = ChatComposerState().apply { editMessage(sent) }
        assertEquals("请看图片", restored.value.text)
        assertEquals(sent, restored.message())
    }

    @Test fun fileTagsRoundTripThroughEditQueuedTakeBackAndStoredDrafts() {
        val file = ComposerAttachment("file", "a & b.txt", "content://example.com/file", false,
            "/up/a & b.txt", size = 86, uploadState = DraftUploadState.Uploaded)
        val sent = ChatComposerState(ChatComposerDraft("Read exactly", listOf(file))).message()
        assertEquals("Read exactly\n\n<persona-file kind=\"file\" path=\"/up/a &amp; b.txt\" name=\"a &amp; b.txt\" size=\"86\"/>", sent)
        for (restore in listOf<(ChatComposerState, String) -> Unit>(
            { state, text -> state.editMessage(text) },
            { state, text -> state.restoreMessage(text) },
            { state, text -> state.load(ChatComposerDraft(text)) },
        )) {
            val edited = ChatComposerState()
            restore(edited, sent)
            assertEquals("Read exactly", edited.value.text)
            assertFalse(edited.value.text.contains("persona-file"))
            assertEquals(file.attachedFile(), edited.attachments.single().attachedFile())
            assertTrue(edited.ready)
            assertEquals(sent, edited.message())
        }
        val alone = ChatComposerState(ChatComposerDraft("", listOf(file))).apply { filesOnlyText = "Please look at the attached files" }.message()
        val queued = ChatComposerState().apply {
            filesOnlyText = "Please look at the attached files"; restoreQueued(alone)
        }
        assertEquals("", queued.value.text)
        assertEquals(file.attachedFile(), queued.attachments.single().attachedFile())
        assertEquals("a & b.txt", composerMessagePreview(alone, "[Photo]", "[Video]"))
    }

    @Test fun forwardedContextKeepsItsOwnFileTagsOutOfTheNewAttachments() {
        val nested = AttachedFile("file", "/up/quoted.txt", "quoted.txt", 10).toXml()
        val own = AttachedFile("file", "/up/own.txt", "own.txt", 20).toXml()
        val canonical = "Review\n\n<persona-context kind=\"quote\">\n$nested\n<attached_images>\n/up/quoted.jpg\n</attached_images>\n</persona-context>\n\n$own"
        val legacy = ProtocolCompatibility.messageForHub(canonical)
        assertNotEquals(canonical, legacy)
        for (raw in listOf(canonical, legacy)) {
            val edited = ChatComposerState().apply { editMessage(raw) }
            assertEquals(listOf("/up/own.txt"), edited.attachments.map { it.path })
            assertTrue(edited.value.text.contains("/up/quoted.txt"))
            assertTrue(edited.value.text.contains("/up/quoted.jpg"))
            assertFalse(edited.value.text.contains("/up/own.txt"))
            if (raw == canonical) assertEquals(raw, edited.message())
        }
    }

    @Test fun tenFilesFitButTheEleventhHasNoPlace() {
        val files = List(10) { index ->
            ComposerAttachment("f$index", "$index.txt", "content://docs/$index", false)
        }
        assertEquals(1, composerFileRoom(files.take(9)))
        assertEquals(0, composerFileRoom(files))
        val retained = files.take(9).toMutableList()
        val picked = listOf(files[9].copy(id = "invalid", uploadState = DraftUploadState.Invalid), files[9],
            ComposerAttachment("eleventh", "11.txt", "content://docs/11", false))
        picked.forEach { pick -> if (composerFileRoom(retained) > 0) retained += pick }
        assertTrue(retained.any { it.id == "invalid" })
        assertEquals(1, retained.count { it.id == "f9" })
        assertFalse(retained.any { it.id == "eleventh" })
        assertEquals(0, composerFileRoom(retained))
    }

    @Test fun uploadDestinationAppliesOnlyToOrdinaryFilesAndHealthKeepsItsOwn() = runTest {
        val context = ApplicationProvider.getApplicationContext<Context>()
        useChatFileProvider(context)
        val folder = java.io.File(context.cacheDir, "chat-captures").apply { mkdirs() }
        val source = java.io.File(folder, "destination.txt").apply { writeText("abc") }
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.chat.files", source)
        val service = object : ChatComposerService {
            override val deliveries = kotlinx.coroutines.flow.MutableStateFlow(emptyMap<String, ComposerDelivery>())
            val destinations = mutableListOf<String?>()
            override suspend fun upload(session: SessionKey, name: String, file: java.io.File): String {
                destinations += null; return "/tmp/$name"
            }
            override suspend fun upload(session: SessionKey, name: String, file: java.io.File, destination: String): String {
                destinations += destination; return "/up/$name"
            }
            override suspend fun interrupt(session: SessionKey) = Unit
            override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean) = ComposerDelivery(DeliveryState.Sending)
        }
        val session = SessionKey("machine", "session")
        val video = ComposerAttachment("video", "clip.mov", uri.toString(), false, video = true)
        val document = ComposerAttachment("file", "notes.txt", uri.toString(), false)
        uploadAttachment(context, service, session, video)
        var stagedSize: Long? = null
        uploadAttachment(context, service, session, document.copy(size = null), onStaged = { stagedSize = it })
        uploadAttachment(context, service, session, document, destination = "health")
        assertEquals(listOf(null, "attachment", "health"), service.destinations)
        assertEquals(3L, stagedSize)
    }

    @Test fun filesOverTheLimitAreRefusedWhenPicked() {
        assertEquals("「录屏.mov」超过 200 MB，传不了", oversizeReason("录屏.mov", MAX_COMPOSER_FILE_BYTES + 1, res))
        assertNull(oversizeReason("录屏.mov", MAX_COMPOSER_FILE_BYTES, res))
        // An unknown size is counted while the file is staged instead.
        assertNull(oversizeReason("录屏.mov", null, res))
    }

    @Test fun thumbnailsCoverTheirSquareWithoutEnlarging() {
        assertEquals(176.0 / 3000, thumbnailScale(4000, 3000, 176), 1e-9)
        assertEquals(176.0 / 1000, thumbnailScale(1000, 4000, 176), 1e-9)
        assertEquals(1.0, thumbnailScale(120, 80, 176), 0.0)
        assertEquals(1.0, thumbnailScale(0, 80, 176), 0.0)
    }

    @Test fun photoThumbnailsShowWhereTheUploadIs() {
        val photo = ComposerAttachment("photo", "photo.jpg", "content://example.com/photo", true)
        val service = object : ChatComposerService {
            override val deliveries = kotlinx.coroutines.flow.MutableStateFlow(emptyMap<String, ComposerDelivery>())
            override suspend fun upload(session: SessionKey, name: String, file: java.io.File) = "/tmp/$name"
            override suspend fun interrupt(session: SessionKey) = Unit
            override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean) = ComposerDelivery(DeliveryState.Sending)
        }
        assertEquals(persona.core.designsystem.AttachmentState.Uploading, photo.thumbnailState(service))
        assertEquals(persona.core.designsystem.AttachmentState.Waiting, photo.thumbnailState(null))
        assertEquals(persona.core.designsystem.AttachmentState.Uploading,
            photo.copy(uploadState = DraftUploadState.Uploading).thumbnailState(null))
        assertEquals(persona.core.designsystem.AttachmentState.Ready,
            photo.copy(path = "/tmp/photo.jpg", uploadState = DraftUploadState.Uploaded).thumbnailState(service))
        assertEquals(persona.core.designsystem.AttachmentState.Failed,
            photo.copy(error = "上传失败", uploadState = DraftUploadState.Failed).thumbnailState(service))
        assertEquals(persona.core.designsystem.AttachmentState.Invalid,
            photo.copy(error = "附件已失效", uploadState = DraftUploadState.Invalid).thumbnailState(service))
        // Restored from a sent message: only on the computer, nothing left to upload.
        assertEquals(persona.core.designsystem.AttachmentState.Ready, photo.copy(uri = "", path = "/tmp/photo.jpg").thumbnailState(service))
    }
}
