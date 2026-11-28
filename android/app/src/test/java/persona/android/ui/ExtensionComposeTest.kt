package persona.android.ui

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantConversations
import persona.core.contracts.api.AssistantSnapshot
import persona.feature.chat.AssistDraftQueue
import persona.feature.chat.AssistDraftTarget
import persona.feature.chat.AssistDraftTextMode

class ExtensionComposeTest {
    @Test fun composeUsesTheOwnersMainConversationDraftAndNeverCreatesOne() = runTest {
        val main = SessionKey("mac", "main-work")
        val assistant = Assistant(main)
        val bus = AssistDraftQueue(clock = { 1_000L })

        val handoff = prepareExtensionCompose(assistant, "What happened?", "work", "assistant", bus)

        assertEquals("work", handoff.manager)
        assertEquals(main, handoff.main)
        assertEquals(listOf("work"), assistant.asked)
        assertEquals(0, assistant.starts)
        assertEquals(AssistDraftTarget.Session(main), bus.current()!!.target)
        assertEquals("What happened?", bus.current()!!.text)
        assertEquals(AssistDraftTextMode.Replace, bus.current()!!.textMode)
    }

    @Test fun composeWithoutAMainUsesTheManagersStarterDraft() = runTest {
        val assistant = Assistant(null)
        val bus = AssistDraftQueue(clock = { 1_000L })

        val handoff = prepareExtensionCompose(assistant, "What happened?", "work", "assistant", bus)

        assertNull(handoff.main)
        assertEquals(AssistDraftTarget.Starter, bus.current()!!.target)
        assertEquals(0, assistant.starts)
    }

    @Test fun composeWithoutAManagerUsesTheDefaultsStarterDraft() = runTest {
        val assistant = Assistant(null)
        val handoff = prepareExtensionCompose(assistant, "What happened?", null, "assistant", AssistDraftQueue(clock = { 1_000L }))

        assertEquals("assistant", handoff.manager)
        assertEquals(listOf("assistant"), assistant.asked)
    }

    private class Assistant(private val main: SessionKey?) : AssistantConversations {
        override val assistant = MutableStateFlow(AssistantSnapshot())
        override val pinned = MutableStateFlow(emptySet<SessionKey>())
        val asked = mutableListOf<String>()
        var starts = 0

        override suspend fun refresh() = Unit
        override suspend fun startConversation(): SessionKey {
            starts++
            error("compose must not start a conversation")
        }

        override suspend fun mainConversationOrNull(manager: String): SessionKey? {
            asked += manager
            return main
        }
    }
}
