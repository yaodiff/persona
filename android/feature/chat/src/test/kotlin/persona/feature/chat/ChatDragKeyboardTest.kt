package persona.feature.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeDown
import androidx.compose.ui.test.click
import androidx.compose.runtime.snapshots.Snapshot
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.SessionRenamer

/**
 * Dragging the conversation puts the keyboard away on the real chat pages (#4210, iOS `ChatView` calls `Keyboard.dismiss`
 * when a drag starts): an Agent's chat and Persona's conversation, which draw one list with the Lark chats
 * (`LarkDragKeyboardTest`). The composer's field and a request card's field both lose the keyboard; a message arriving
 * while someone types moves the list by itself and leaves it.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi", application = QuestionTestApplication::class)
class ChatDragKeyboardTest {
    @get:Rule val rule = createComposeRule()
    private val probe = KeyboardProbe()

    private val agent = SessionKey("computer-a", "importer")
    private val persona = SessionKey("computer-a", "main")
    private val renamer = object : SessionRenamer {
        override fun project(source: Flow<List<Session>>) = source
        override suspend fun rename(session: Session, title: String?, tabName: String?) = false
    }
    private val question = Interaction("ask", machineId = agent.machineId, sessionId = agent.sessionId, kind = "question", createdAt = "2026-11-12T02:00:00Z",
        questions = listOf(Question("Which branch should I push to?", options = listOf(QuestionOption("main"), QuestionOption("release/1.4")))))
    private val personaQuestion = question.copy(id = "own-ask", sessionId = persona.sessionId)

    private fun history() = (1..30).map {
        Item("m$it", it, role = if (it % 2 == 1) "user" else "assistant", kind = "text", text = "Message number $it", ts = "2026-11-12T01:%02d:00Z".format(it))
    }
    private val transcript = MutableStateFlow(ChatSnapshot(loading = false, items = history()))

    private fun mount(key: SessionKey, composer: ChatComposerState, requests: List<Interaction> = emptyList()) {
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(agent.machineId, "Computer A", online = true)))
            override val sessions = MutableStateFlow(listOf(
                Session(agent.machineId, agent.sessionId, title = "Fix the importer retry", agent = "claude", status = "idle", state = "idle"),
                Session(persona.machineId, persona.sessionId, title = "Persona", assistant = true, status = "idle", state = "idle")))
            override suspend fun refresh() = Unit
        }
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = transcript
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(requests)
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                probe.Around {
                    Box(Modifier.fillMaxSize()) {
                        if (key == persona) ChatScreen(key, chats, sender, interactions, sessions, {}, chrome = ChatChromeBindings(renamer = renamer),
                            renderStyle = ChatRenderStyle.Assistant, composerState = composer, showTopBar = false)
                        else ChatScreen(key, chats, sender, interactions, sessions, {}, chrome = ChatChromeBindings(renamer = renamer), composerState = composer)
                    }
                }
            }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithText("Message number 30").fetchSemanticsNodes().isNotEmpty() }
        settle()
    }

    private fun settle() { Snapshot.sendApplyNotifications(); rule.waitForIdle() }

    private val inCard = hasAnyAncestor(hasTestTag("pending-card-ask")) or hasAnyAncestor(hasTestTag("pending-card-own-ask"))
    private fun composerField() = rule.onNode(hasSetTextAction() and !inCard)
    private fun drag() {
        // On a message of the list, clear of the bars: the finger moves down it, past the touch slop.
        rule.onNodeWithText("Message number 28").performTouchInput { swipeDown() }
        settle()
    }

    private fun draggingPutsTheKeyboardAwayFromTheComposer(key: SessionKey) {
        val composer = ChatComposerState()
        mount(key, composer)
        composerField().performTouchInput { click() }
        settle()
        assertTrue("the composer takes the focus when tapped", composer.focused)
        probe.reset()
        drag()
        assertTrue("a drag asks the focus to clear: ${probe.focusClears}", probe.focusClears.isNotEmpty() && probe.focusClears.none { it })
        assertTrue("and the keyboard to hide", probe.hides > 0)
    }

    @Test fun draggingAnAgentsChatPutsTheKeyboardAway() = draggingPutsTheKeyboardAwayFromTheComposer(agent)
    @Test fun draggingPersonasConversationPutsTheKeyboardAway() = draggingPutsTheKeyboardAwayFromTheComposer(persona)

    private fun draggingPutsTheKeyboardAwayFromACard(key: SessionKey, request: Interaction) {
        val composer = ChatComposerState()
        mount(key, composer, listOf(request))
        // The card's own "Other answer" field, which opens with the cursor in it.
        rule.onNodeWithText("Other answer…").performClick()
        settle()
        rule.onNode(hasSetTextAction() and inCard).assertExists()
        probe.reset()
        drag()
        assertTrue("a drag asks the focus to clear, the card's field's too: ${probe.focusClears}", probe.focusClears.isNotEmpty())
        assertTrue("and the keyboard to hide", probe.hides > 0)
    }

    @Test fun draggingAnAgentsChatTakesTheKeyboardFromACardsField() = draggingPutsTheKeyboardAwayFromACard(agent, question)
    @Test fun draggingPersonasConversationTakesTheKeyboardFromACardsField() = draggingPutsTheKeyboardAwayFromACard(persona, personaQuestion)

    // The page follows the newest message by itself: no finger, so the person typing keeps the keyboard.
    private fun aNewMessageWhileTypingKeepsTheKeyboard(key: SessionKey) {
        val composer = ChatComposerState()
        mount(key, composer)
        composerField().performTouchInput { click() }
        settle()
        assertTrue(composer.focused)
        probe.reset()
        transcript.value = ChatSnapshot(loading = false, items = history() + Item("m31", 31, role = "assistant", kind = "text",
            text = "A new reply arrives", ts = "2026-11-12T01:31:00Z"))
        rule.waitUntil(10_000) { rule.onAllNodesWithText("A new reply arrives").fetchSemanticsNodes().isNotEmpty() }
        settle()
        assertEquals("the focus is left alone", emptyList<Boolean>(), probe.focusClears)
        assertEquals("and so is the keyboard", 0, probe.hides)
        assertTrue(composer.focused)
    }

    @Test fun aNewMessageWhileTypingInAnAgentsChatKeepsTheKeyboard() = aNewMessageWhileTypingKeepsTheKeyboard(agent)
    @Test fun aNewMessageWhileTypingInPersonasConversationKeepsTheKeyboard() = aNewMessageWhileTypingKeepsTheKeyboard(persona)
}
