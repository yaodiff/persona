package persona.feature.chat

import androidx.compose.ui.text.input.TextFieldValue
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DraftAttachment
import persona.core.contracts.api.DraftKey
import persona.core.contracts.api.DraftRepository
import persona.core.contracts.api.DraftSessionVersion
import persona.core.contracts.api.DraftSnapshot

/** #4045: the chat draws before its draft is read, and the draft is neither lost nor written over meanwhile. */
@OptIn(ExperimentalCoroutinesApi::class)
class ChatDraftEditorTest {
    private val key = DraftKey.Chat(SessionKey("machine", "session"))

    /** Holds every read until [gate] opens. */
    private inner class Drafts(text: String, override val structured: Boolean = false) : DraftRepository {
        var stored = DraftSnapshot(HubScope("hub", "pairing"), key, text, version = 1)
        val gate = CompletableDeferred<Unit>()
        val saves = mutableListOf<String>()
        override fun observe(key: DraftKey) = emptyFlow<DraftSnapshot>()
        override suspend fun read(key: DraftKey): DraftSnapshot { gate.await(); return stored }
        override suspend fun save(key: DraftKey, text: String, expectedVersion: Long): DraftSnapshot =
            save(key, text, expectedVersion, emptyList(), null, false)
        override suspend fun save(
            key: DraftKey, text: String, expectedVersion: Long,
            attachments: List<DraftAttachment>, sessionVersion: DraftSessionVersion?, hasPickedSavedPrompt: Boolean,
        ): DraftSnapshot {
            check(expectedVersion == stored.version)
            saves += text
            stored = stored.copy(text = text, version = stored.version + 1, attachments = attachments,
                sessionVersion = sessionVersion, hasPickedSavedPrompt = hasPickedSavedPrompt)
            return stored
        }
    }

    @Test fun theComposerIsThereAtOnceAndTheDraftFillsItWithoutBeingWrittenOver() = runTest {
        val drafts = Drafts("half a sentence")
        val editor = ChatDraftEditor(drafts, backgroundScope, key)
        val composer = editor.composer.value!!
        // The page reports the empty composer it draws before the draft is read: not saved over the draft.
        editor.changed(composer.draft); runCurrent()
        assertFalse(editor.ready.value)
        drafts.gate.complete(Unit); runCurrent()
        assertTrue(editor.ready.value)
        assertSame(composer, editor.composer.value)
        assertEquals("half a sentence", composer.value.text)
        // A report overtaken by the composer (the empty one, arriving late) isn't saved either.
        editor.changed(ChatComposerDraft()); runCurrent()
        assertEquals(emptyList<String>(), drafts.saves)
        composer.editMessage("half a sentence, finished"); editor.changed(composer.draft); runCurrent()
        assertEquals(listOf("half a sentence, finished"), drafts.saves)
    }

    @Test fun whatIsTypedBeforeTheDraftArrivesStays() = runTest {
        val drafts = Drafts("old draft")
        val editor = ChatDraftEditor(drafts, backgroundScope, key)
        val composer = editor.composer.value!!
        composer.editMessage("typed first"); editor.changed(composer.draft)
        drafts.gate.complete(Unit); runCurrent()
        assertEquals("typed first", composer.value.text)
        assertEquals(listOf("typed first"), drafts.saves)
        // Typed and sent (cleared) before the draft arrived: the old draft doesn't come back either.
        val sent = Drafts("old draft")
        val other = ChatDraftEditor(sent, backgroundScope, key)
        val cleared = other.composer.value!!
        cleared.editMessage("sent at once"); other.changed(cleared.draft)
        cleared.clearIfUnchanged(cleared.draft); other.changed(cleared.draft)
        sent.gate.complete(Unit); runCurrent()
        assertEquals("", cleared.value.text)
        assertEquals(listOf(""), sent.saves)
    }

    @Test fun anExtensionComposeHandoffIsSavedThroughTheMountedComposer() = runTest {
        val drafts = Drafts("earlier words").also { it.gate.complete(Unit) }
        val editor = ChatDraftEditor(drafts, backgroundScope, key)
        runCurrent()
        val composer = editor.composer.value!!
        val bus = AssistDraftQueue(clock = { 1_000L })
        bus.publish(id = "compose", text = "What happened?", textMode = AssistDraftTextMode.Replace)
        val handoff = bus.claim(AssistDraftTarget.Session(SessionKey("machine", "session")))!!

        assertTrue(prefillAssistDraft(composer, handoff).textAdded)
        editor.changed(composer.draft)
        runCurrent()

        assertEquals("What happened?", composer.value.text)
        assertEquals(listOf("What happened?"), drafts.saves)
    }

    @Test fun reopeningDispatchesSavedPromptBodiesAsMessagesAndRetypedBodiesAsCommands() = runTest {
        for (word in listOf("/clear", "/permissions")) {
            val drafts = Drafts(word, structured = true)
            drafts.gate.complete(Unit)
            val initial = ChatDraftEditor(drafts, backgroundScope, key)
            runCurrent()
            // Picking the same body changes only provenance, which must still be saved on close.
            initial.composer.value!!.fillSavedPrompt(word)
            initial.close()
            runCurrent()
            assertEquals(listOf(word), drafts.saves)
            assertTrue(drafts.stored.hasPickedSavedPrompt)

            val reopened = ChatDraftEditor(drafts, backgroundScope, key)
            runCurrent()
            val restored = reopened.composer.value!!
            val actions = mutableListOf<String>()
            val clear = MainChatClear { actions += "/clear" }
            val permissions: () -> Unit = { actions += "/permissions" }
            assertEquals(word, restored.value.text)
            dispatchComposerSend(restored, false, clear, permissions) { actions += "send" }
            assertEquals(listOf("send"), actions)

            restored.value = TextFieldValue("/")
            restored.value = TextFieldValue(word)
            reopened.close()
            runCurrent()
            assertEquals(listOf(word, word), drafts.saves)
            assertFalse(drafts.stored.hasPickedSavedPrompt)

            val manual = ChatDraftEditor(drafts, backgroundScope, key)
            runCurrent()
            dispatchComposerSend(manual.composer.value!!, false, clear, permissions) { actions += "send" }
            assertEquals(listOf("send", word), actions)
            manual.close()
        }
    }
}
