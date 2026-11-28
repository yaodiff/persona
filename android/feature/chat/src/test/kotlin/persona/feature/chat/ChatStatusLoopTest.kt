package persona.feature.chat

import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.semantics.SemanticsActions
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import java.io.File
import androidx.compose.ui.text.TextLayoutResult
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.LoopInfo
import persona.core.contracts.Session
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.StatusLineState

// The status sheet is the only place that shows what a session's loop does: its prompt is shown in full, however long (#2708).
// Lines are measured with real fonts (NATIVE); a tall screen keeps the loop's row composed in the sheet's lazy list.
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "w400dp-h2400dp")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ChatStatusLoopTest {
    @get:Rule val rule = createComposeRule()

    @Test fun aLongLoopPromptIsShownInFull() {
        val prompt = "Every half hour, check the CI of the open pull requests, read the logs of any job that failed, " +
            "fix what is clearly ours with a new commit, leave a note on the pull request for anything that is not, " +
            "and when everything is green, post a short summary of what changed in the team chat with the links."
        val session = Session(machineId = "mac", sessionId = "s1", title = "Watch CI", status = "running",
            loop = LoopInfo(intervalSeconds = 1800, prompt = prompt))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                val resources = LocalResources.current
                val scope = rememberCoroutineScope()
                val recovery = remember { ChatStatusRecovery(resources, null, scope) {} }
                ChatStatusSheet(StatusLineState(), session, emptyList(), emptySet(), onAction = null, onDismiss = {},
                    replacement = null, onOpenReplacement = null, onConnectionDetails = null, terminal = null,
                    recordedDuration = null, notice = null, recovery = recovery, machine = null, draftSaveError = null,
                    readOnly = false, onOpenSession = null)
            }
        }
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = rule.onNodeWithText(prompt, useUnmergedTree = true).fetchSemanticsNode()
            .config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        val layout = layouts.single()
        assertFalse("the prompt is cut after ${layout.lineCount} lines", layout.hasVisualOverflow)
        assertTrue("the prompt fits in ${layout.lineCount} lines: too short to test the cut", layout.lineCount > 3)
    }

    @Test fun queuedMessagesInStatusSheetHideRawContextXml() {
        val legacy = persona.core.contracts.ProtocolCompatibility.messageForHub(
            "${ChatLinkContext.quote("Quoted body", "Antigravity", "1@0").xml()}\n\n先检查队列预览")
        val session = Session(machineId = "mac", sessionId = "s1", title = "Watch CI", status = "working",
            queued = listOf(legacy))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                val resources = LocalResources.current
                val scope = rememberCoroutineScope()
                val recovery = remember { ChatStatusRecovery(resources, null, scope) {} }
                ChatStatusSheet(StatusLineState(), session, emptyList(), emptySet(), onAction = null, onDismiss = {},
                    replacement = null, onOpenReplacement = null, onConnectionDetails = null, terminal = null,
                    recordedDuration = null, notice = null, recovery = recovery, machine = null, draftSaveError = null,
                    readOnly = false, onOpenSession = null)
            }
        }
        rule.onAllNodesWithText("-context", substring = true).assertCountEquals(0)
        rule.onNodeWithText("先检查队列预览").assertExists()
    }

    @Test fun quietSessionShowsSummaryRowInsteadOfBlankSheet() {
        val session = Session(machineId = "mac", sessionId = "s2", title = "Migrate blog to Astro", state = "idle", status = "idle")
        var dark by mutableStateOf(false)
        val outDir = File(System.getProperty("persona.shots") ?: "build/status-sheet-shots").also { it.mkdirs() }
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("status-sheet").width(400.dp).background(PersonaTheme.palette.background)) {
                    val resources = LocalResources.current
                    val scope = rememberCoroutineScope()
                    val recovery = remember { ChatStatusRecovery(resources, null, scope) {} }
                    ChatStatusSheet(StatusLineState(), session, emptyList(), emptySet(), onAction = null, onDismiss = {},
                        replacement = null, onOpenReplacement = null, onConnectionDetails = null, terminal = {},
                        recordedDuration = null, notice = null, recovery = recovery, machine = null, draftSaveError = null,
                        readOnly = false, onOpenSession = null)
                }
            }
        }
        for (isDark in listOf(false, true)) {
            rule.runOnIdle { dark = isDark }
            rule.waitForIdle()
            rule.onNodeWithText("Migrate blog to Astro").assertIsDisplayed()
            val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
            val name = if (isDark) "status-sheet-quiet-dark.png" else "status-sheet-quiet-light.png"
            File(outDir, name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
