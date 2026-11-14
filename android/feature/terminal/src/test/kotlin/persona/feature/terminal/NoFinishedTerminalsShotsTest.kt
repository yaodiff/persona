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
import org.junit.Assert.assertTrue
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

/** The existing Agent-only browser must keep main's appearance when there is nothing to clean up. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class NoFinishedTerminalsShotsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun nothingFinishedEveryStyleAndAppearance() {
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine("desk", "Workstation", online = true, home = "/home/me")))
            override val sessions = MutableStateFlow(listOf(
                Session(machineId = "desk", sessionId = "active", title = "Weather widget", status = "idle")))
            override suspend fun refresh() = Unit
        }
        val panes = object : TerminalPanes {
            override val available = MutableStateFlow(true)
            override suspend fun panes(machineId: String) = PanesResponse(listOf(PaneHost("herdr", newTab = true,
                workspaces = listOf(PaneSpace("w1", "Project")), tabs = listOf(PaneTab("t", "w1", "Project")), panes = listOf(
                    PaneItem("w1:p1", "pane:w1:p1", "t", "w1", "claude", "idle", cwd = "/home/me/project", sessionId = "active")))))
            override suspend fun newTab(machineId: String, workspaceId: String, kind: String?, cwd: String?) = error("unused")
        }
        val routes = object : Navigator {
            override fun navigate(key: NavKey) = Unit
            override fun back() = Unit
            override fun selectTab(tab: AppTab) = Unit
        }
        val model = TerminalPanesViewModel(panes, sessions)
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
        val out = File(System.getProperty("persona.shots") ?: "build/no-finished-terminal-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            rule.mainClock.advanceTimeBy(400)
            rule.onNodeWithText("Weather widget").assertExists()
            rule.onNodeWithText("Close finished terminals").assertDoesNotExist()
            rule.onNodeWithContentDescription("Close terminal").assertDoesNotExist()
            File(out, "no-finished-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                assertTrue(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
    }
}
