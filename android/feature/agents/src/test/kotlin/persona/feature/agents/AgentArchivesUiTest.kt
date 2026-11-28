package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import org.junit.After
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class AgentArchivesUiTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val api = ArchiveFake().apply { rows = source.map { it.copy(archived = true) } }
    private val model = AgentArchivesState(api, scope) { _, _ -> }
    private var opened: Session? = null
    private var back = false
    @After fun cleanup() { scope.cancel() }

    private fun show() {
        rule.setContent { PersonaTheme(darkTheme = false, reduceMotion = true) {
            AgentArchivesScreen(model, mapOf("computer" to "Development computer"), { back = true }, { opened = it })
        } }
        rule.waitForIdle()
    }

    @Test fun searchReadsBothSourcesAndRestoreRemovesOnlyItsRow() {
        show()
        rule.onNodeWithText("Claude history").performClick()
        assertEquals("claude", opened?.agent)
        rule.onNodeWithText("Find archived sessions").performTextInput("Codex")
        rule.onNodeWithText("Claude history").assertDoesNotExist()
        rule.onNodeWithText("Codex history").performClick()
        assertEquals("codex", opened?.agent)
        rule.onNodeWithTag("restore:computer/codex-session").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Codex history").assertDoesNotExist()
        rule.onNodeWithText("No matching sessions").assertExists()
        rule.onNodeWithText("Codex").performTextClearance()
        rule.onNodeWithText("Claude history").assertExists()
        rule.onNodeWithTag("restore:computer/claude-session").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("No archived sessions").assertExists()
        assertEquals(2, api.writes)
    }

    @Test fun failedRestoreStaysVisibleAndRetryCanFinish() {
        show()
        rule.runOnIdle { api.failWrite = true }
        rule.onNodeWithTag("restore:computer/claude-session").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Claude history").assertExists()
        assertNotNull(model.state.value.error)
        rule.runOnIdle { api.failWrite = false }
        rule.onNodeWithTag("restore:computer/claude-session").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Claude history").assertDoesNotExist()
        assertNull(model.state.value.error)
    }

    @Test fun allStylesHaveLightAndDarkArchivePages() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent { PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
            Box(Modifier.width(400.dp).height(800.dp).background(PersonaTheme.palette.background).testTag("archive-shot")) {
                AgentArchivesScreen(model, mapOf("computer" to "Development computer"), {}, {})
            }
        } }
        rule.waitForIdle()
        val output = File("build/archive-shots").apply { mkdirs() }
        for (look in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { style = look; dark = night }
            rule.waitForIdle()
            rule.onNodeWithText("Claude history").assertExists()
            rule.onNodeWithText("Codex history").assertExists()
            val bitmap = rule.onNodeWithTag("archive-shot").captureToImage().asAndroidBitmap()
            File(output, "${look.name.lowercase()}-${if (night) "dark" else "light"}.png").outputStream().use {
                assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
    }
}
