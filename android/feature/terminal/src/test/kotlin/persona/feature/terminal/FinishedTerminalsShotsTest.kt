package persona.feature.terminal

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
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
import persona.core.navigation.*
import java.io.File

/** Populated terminal browser and its destructive preview in Glass, Brief and Board, light and dark. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class FinishedTerminalsShotsTest {
    @get:Rule val rule = createComposeRule()
    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(Machine("desk", "Workstation", online = true, home = "/home/me")))
        override val sessions = MutableStateFlow(listOf(
            Session(machineId = "desk", sessionId = "active", title = "Weather widget", status = "working"),
            Session(machineId = "desk", sessionId = "finished", title = "Release notes", status = "offline")))
        override suspend fun refresh() = Unit
    }
    private val panes = object : TerminalPanes {
        override val available = MutableStateFlow(true)
        override suspend fun panes(machineId: String) = PanesResponse(listOf(PaneHost("herdr", newTab = true,
            workspaces = listOf(PaneSpace("w1", "Project")), tabs = listOf(PaneTab("t", "w1", "Project")), panes = listOf(
                PaneItem("w1:p1", "pane:w1:p1", "t", "w1", "claude", "working", cwd = "/home/me/project", sessionId = "active"),
                PaneItem("w1:p2", "pane:w1:p2", "t", "w1", cwd = "/home/me/project", foreground = "zsh", agentSession = "finished"),
                PaneItem("w1:p3", "pane:w1:p3", "t", "w1", cwd = "/home/me/project/web", foreground = "vim"),
                PaneItem("w1:p4", "pane:w1:p4", "t", "w1", cwd = "/home/me/project", foreground = "npm")))))
        override suspend fun newTab(machineId: String, workspaceId: String, kind: String?, cwd: String?) = error("unused")
        override suspend fun closeFinished(machineId: String, preview: Boolean, panes: List<String>) =
            FinishedPanes(listOf(FinishedPane("w1:p2", "finished")), listOf(FinishedPane("w1:p4", why = "program")))
    }

    @Test fun listOpenAndConfirmationEveryStyleAndAppearance() {
        val model = TerminalPanesViewModel(panes, sessions)
        val opened = mutableListOf<NavKey>()
        val routes = object : Navigator {
            override fun navigate(key: NavKey) { opened += key }
            override fun back() = Unit
            override fun selectTab(tab: AppTab) = Unit
        }
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                    TerminalPanesScreen(model, routes)
                }
            }
        }
        model.refresh()
        val out = File(System.getProperty("persona.shots") ?: "build/finished-terminals-shots").also { it.mkdirs() }
        fun save(name: String, node: SemanticsNodeInteraction) {
            File(out, "$name.png").outputStream().use { assertTrue(node.captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            rule.mainClock.advanceTimeBy(400)
            rule.onNodeWithText("Finished: Release notes").assertExists()
            rule.onNodeWithText("Terminal · zsh · project").assertExists()
            // The Agent opens its terminal, but has no destructive close action.
            rule.onAllNodesWithContentDescription("Close terminal").assertCountEquals(3)
            rule.onNode(hasText("Weather widget") and hasAnyDescendant(hasContentDescription("Close terminal"))).assertDoesNotExist()
            rule.onNodeWithText("vim · ~/project/web").assertExists()
            val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            save("terminals-$suffix", rule.onNodeWithTag("shot"))
            rule.onNodeWithText("Close finished terminals").performClick()
            rule.waitForIdle()
            rule.onNodeWithText("Close 1 finished terminal?").assertExists()
            rule.onNode(hasText("Finished: Release notes") and hasAnyAncestor(isDialog())).assertExists()
            rule.onNode(hasText("Terminal · zsh · project") and hasAnyAncestor(isDialog())).assertExists()
            save("close-finished-$suffix", rule.onNode(isDialog()))
            rule.onNodeWithText("Cancel").performClick()
        }
        rule.onNodeWithText("Finished: Release notes").performClick()
        assertEquals(TerminalRoute("desk", "finished", ""), opened.last())
        rule.onNodeWithText("vim · ~/project/web").performClick()
        assertEquals("pane:w1:p3", (opened.last() as TerminalRoute).sessionId)
    }
}
