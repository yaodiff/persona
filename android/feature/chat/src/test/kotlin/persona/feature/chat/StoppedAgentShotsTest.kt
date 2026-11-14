package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/** The real stopped chat: retained transcript, no composer, and the existing terminal/resume callbacks in every look. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class StoppedAgentShotsTest {
    @get:Rule val rule = createComposeRule()
    private val key = SessionKey("desk", "release")
    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(Machine("desk", "Workstation", online = true, engineHosts = listOf("herdr"))))
        override val sessions = MutableStateFlow(listOf(Session(machineId = key.machineId, sessionId = key.sessionId,
            title = "Release notes", status = "offline", agent = "claude", paneId = "w1:p1", cwd = "/home/me/project",
            capabilities = SessionCapabilities(read = true, terminal = true))))
        override suspend fun refresh() = Unit
    }
    private val chats = object : ChatRepository {
        override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false, items = listOf(
            Item("u", 1, role = "user", kind = "text", text = "Review the release notes.", ts = "2026-11-14T08:00:00Z"),
            Item("a", 2, role = "assistant", kind = "text", text = "The release notes are ready. The conversation and terminal are kept for reference.",
                ts = "2026-11-14T08:01:00Z"))))
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
    }
    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = error("stopped Agent must not send")
        override suspend fun cancel(clientMessageId: String): Boolean {
            outbox.value = outbox.value.filterNot { it.clientMessageId == clientMessageId }
            return true
        }
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = error("stopped Agent must not answer")
    }

    @Test fun refusedStoppedChatHasNoRetryOrEditButKeepsDeleteAndTerminal() {
        sender.outbox.value = listOf(OutboxMessage("refused", key, "Check the examples too.", DeliveryState.Refused, error = "agent_stopped"))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var terminals = 0
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                    ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, onTerminal = { terminals++ }, onResumeInTerminal = {})
                }
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/stopped-agent-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            rule.onNodeWithTag("chat-stopped").assertExists()
            rule.onNodeWithText("Check the examples too.").assertExists()
            for (label in listOf("Retry", "Retry now", "Resend", "Edit")) rule.onNodeWithText(label).assertDoesNotExist()
            rule.onAllNodes(hasSetTextAction()).assertCountEquals(0)
            rule.onNodeWithText("Open terminal").assertExists()
            if (s != AppStyle.Glass) rule.onNodeWithText("Delete").assertExists()
            File(out, "stopped-refused-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                assertTrue(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
        rule.onNodeWithText("Open terminal").performClick()
        assertEquals(1, terminals)
        rule.onNodeWithText("Delete").performClick()
        rule.onNodeWithText("Check the examples too.").assertDoesNotExist()
        assertTrue(sender.outbox.value.isEmpty())
    }

    @Test fun anOfflineComputerKeepsTheDisabledComposerInsteadOfTheStoppedNote() {
        sessions.machines.value = sessions.machines.value.map { it.copy(online = false) }
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, onTerminal = {}, onResumeInTerminal = {})
            }
        }
        rule.onNodeWithTag("chat-stopped").assertDoesNotExist()
        rule.onNode(SemanticsMatcher.keyIsDefined(SemanticsProperties.EditableText), useUnmergedTree = true).assertIsNotEnabled()
    }

    @Test fun stoppedChatEveryStyleAndAppearance() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var terminals = 0
        var resumes = 0
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                    ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, onTerminal = { terminals++ },
                        onResumeInTerminal = { resumes++ })
                }
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/stopped-agent-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitUntil(15_000) { rule.onAllNodesWithText("The release notes are ready. The conversation and terminal are kept for reference.")
                .fetchSemanticsNodes().isNotEmpty() }
            rule.onNodeWithTag("chat-stopped").assertExists()
            rule.onAllNodes(hasSetTextAction()).assertCountEquals(0)
            File(out, "stopped-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                assertTrue(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
        rule.onNode(hasText("Terminal") and hasAnyAncestor(hasTestTag("chat-stopped"))).performClick()
        rule.onNode(hasText("Resume") and hasAnyAncestor(hasTestTag("chat-stopped"))).performClick()
        assertEquals(1, terminals)
        assertEquals(1, resumes)
    }

    @Test fun routedSuccessorUsesTheWireIdentityBeforeItsSessionIsListed() {
        val item = FeedItem(id = "routed", kind = "routed", body = buildJsonObject {
            put("new_agent", "successor"); put("new_agent_machine", "second-computer")
        })
        assertEquals(SessionKey("second-computer", "successor"), newAgentKey(item, null))
        assertNull(newAgentKey(item.copy(body = buildJsonObject { put("new_agent", "successor") }), null))
        assertEquals(persona.core.domain.FailureAction.OpenTerminal,
            persona.core.domain.mapOperationFailure("agent_stopped", "none", true).action)
    }
}
