package persona.feature.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.After
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
import persona.core.domain.tasks.TasksStore

/**
 * The stack above the composer in the real chat page (#4210 T6, #4339 C10): the composer stays usable while a card waits
 * and a message typed then is sent as any other (as iOS's `sendText`); past half the page they scroll under "N pending";
 * each card folds on its own; Persona's conversation shows its tasks' oldest question under the task's name, and a card
 * the Needs you tray's Review opens joins the stack until it is folded.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp", application = QuestionTestApplication::class)
class PendingStackUiTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    @After fun close() { scope.cancel() }

    private val key = SessionKey("computer-a", "importer")
    private val renamer = object : SessionRenamer {
        override fun project(source: Flow<List<Session>>) = source
        override suspend fun rename(session: Session, title: String?, tabName: String?) = false
    }
    private val permission = Interaction("perm", machineId = key.machineId, sessionId = key.sessionId, kind = "permission", toolName = "Bash",
        summary = "npm test", createdAt = "2026-11-12T01:03:00Z")
    private val question = Interaction("ask", machineId = key.machineId, sessionId = key.sessionId, kind = "question", createdAt = "2026-11-12T01:04:00Z",
        questions = listOf(Question("Which branch should I push to?", options = listOf(QuestionOption("main"), QuestionOption("release/1.4")))))
    private val plan = Interaction("plan", machineId = key.machineId, sessionId = key.sessionId, kind = "plan", createdAt = "2026-11-12T01:05:00Z",
        summary = "Retry the import once", plan = "1. Catch the restart error\n2. Retry once")
    private val sent = mutableListOf<String>()
    private val answers = mutableListOf<Pair<String, Resolution>>()

    private fun settle() { Snapshot.sendApplyNotifications(); rule.waitForIdle() }

    private fun agentChat(requests: List<Interaction>, dark: Boolean = false) {
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(key.machineId, "Computer A", online = true)))
            override val sessions = MutableStateFlow(listOf(Session(key.machineId, key.sessionId, title = "Fix the importer retry",
                agent = "claude", status = "blocked", state = "needs_you", pendingInteractions = requests.size)))
            override suspend fun refresh() = Unit
        }
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false, items = listOf(
                Item("u1", 1, role = "user", kind = "text", text = "Make the importer retry once", ts = "2026-11-12T01:01:00Z"))))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) { sent += text }
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(requests)
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) { answers += id to resolution }
        }
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true) {
                Box(Modifier.fillMaxSize()) {
                    ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, chrome = ChatChromeBindings(renamer = renamer))
                }
            }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithText("Make the importer retry once").fetchSemanticsNodes().isNotEmpty() }
        settle()
    }

    @Test fun theComposerStaysAndAMessageTypedWhileAPermissionWaitsIsSent() {
        agentChat(listOf(permission))
        rule.onNodeWithTag("pending-card-perm").assertExists()
        val composer = hasSetTextAction() and !hasAnyAncestor(hasTestTag("pending-card-perm"))
        rule.onNode(composer).performTextInput("Also bump the version")
        settle()
        rule.onNode(hasContentDescription("Send") and !hasAnyAncestor(hasTestTag("pending-card-perm"))).performClick()
        settle()
        assertEquals(listOf("Also bump the version"), sent)
        // the card is still there, unanswered
        rule.onNodeWithTag("pending-card-perm").assertExists()
    }

    private fun assertPreparedCardAnswers(dark: Boolean) {
        // The original window has ended; this is the replacement snapshot with the server's current lifetime.
        val retained = JsonCodec.decodeFromString<Interaction>(
            """{"id":"prepared","machine_id":"${key.machineId}","session_id":"${key.sessionId}",""" +
                """"kind":"permission","tool_name":"Bash","expires_at":"${java.time.Instant.now().plusSeconds(3_600)}",""" +
                """"can_clarify":true}""",
        )
        agentChat(listOf(retained), dark = dark)
        val inCard = hasAnyAncestor(hasTestTag("pending-card-prepared"))
        rule.onNode(hasText("Allow") and inCard).assertIsEnabled().performClick()
        settle()
        assertEquals(listOf(retained.id to Resolution(allow = true)), answers)
    }

    @Test fun aPreparedCardStillAnswersAfterItsOriginalWindow() = assertPreparedCardAnswers(dark = false)

    @Config(fontScale = 2f)
    @Test fun aPreparedCardStillAnswersWithLargeTextInDarkMode() = assertPreparedCardAnswers(dark = true)

    /** Past half the page they scroll under "3 pending": the session's requests, the number its Agents row says
     *  ("3 need your decision", one count per card of the session). */
    @Test fun pastHalfThePageTheyScrollUnderNPending() {
        agentChat(listOf(question, plan, permission))
        rule.onNodeWithText("3 pending").assertExists()
        for (id in listOf("perm", "ask", "plan")) rule.onNodeWithTag("pending-card-$id").assertExists()
    }

    @Test fun eachCardFoldsOnItsOwn() {
        agentChat(listOf(permission, question))
        val inQuestion = hasAnyAncestor(hasTestTag("pending-card-ask"))
        rule.onNode(hasContentDescription("Collapse") and hasAnyAncestor(hasTestTag("pending-card-perm"))).performClick()
        settle()
        rule.onNode(hasContentDescription("Expand") and hasAnyAncestor(hasTestTag("pending-card-perm"))).assertExists()
        // the other card is as it was, its options still there
        rule.onNode(hasText("1. main") and inQuestion).assertExists()
        rule.onNode(hasContentDescription("Collapse") and inQuestion).assertExists()
    }
}
