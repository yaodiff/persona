package persona.feature.chat

import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChildSessionHomeStateTest {
    @get:Rule val rule = createComposeRule()

    @Test fun anOfflineCachedPageStaysMountedWhenHelloStartsASlowBackgroundLookup() {
        val key = SessionKey("m", "ordinary")
        var connected by mutableStateOf(false)
        var features by mutableStateOf(emptyList<String>())
        val answer = CompletableDeferred<Session?>()
        var reads = 0
        var mounts = 0
        var disposals = 0
        val actions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun completeRow(session: Session): Session? { reads++; return answer.await() }
        }
        rule.setContent {
            PersonaTheme {
                val home = rememberChildSessionHome(key, emptyList(), actions, features, connected)
                DisposableEffect(home.key) { mounts++; onDispose { disposals++ } }
                Text(home.key.sessionId)
            }
        }
        rule.onNodeWithText("ordinary").assertIsDisplayed()
        rule.runOnIdle { features = listOf("sessions_lookup", "sessions_hierarchy") }
        rule.runOnIdle { assertEquals(0, reads); connected = true }
        rule.onNodeWithText("ordinary").assertIsDisplayed()
        rule.runOnIdle {
            assertEquals(1, reads)
            assertEquals(1, mounts)
            assertEquals(0, disposals)
            answer.complete(Session("m", "ordinary", title = "Loaded metadata"))
        }
        rule.onNodeWithText("ordinary").assertIsDisplayed()
        rule.runOnIdle { assertEquals(1, mounts); assertEquals(0, disposals) }
    }
}
