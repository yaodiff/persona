package persona.feature.agents

import android.os.Looper
import androidx.activity.ComponentActivity
import androidx.compose.runtime.*
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.lifecycle.viewModelScope
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import java.io.File
import java.time.Duration

/**
 * New Agent with an Orca workspace picked (#17050): the "In a new worktree" switch shows for it, as for a Paseo one,
 * and Orca is asked where the worktree goes. Every style, light and dark, the switch on with its branch field. PNGs in
 * `build/new-agent-orca-worktree-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-xhdpi")
class NewAgentOrcaWorktreeShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val computers = listOf(Machine("computer", "Studio", online = true, engine = "orca", home = "/home/user"))
    private val orca = AgentWorkspace("computer/orca/repo::/home/user/code/weather", "computer", "orca", "repo::/home/user/code/weather",
        "weather", "/home/user/code/weather", "main", projectId = "repo", projectName = "weather", isMain = true, workspaceKind = "git")
    private val asked = mutableListOf<String>()
    private val service = object : AgentCreationService {
        override val scope = "hub/pairing"
        override suspend fun launchers(machineId: String) = listOf(AgentLauncher("claude"))
        override suspend fun directories(machineId: String, path: String) = AgentDirectories("/home/user/code")
        override suspend fun start(request: AgentStartRequest, operationId: String) = AgentStartResult.Unknown
        override suspend fun operation(operationId: String) = AgentStartResult.Unknown
        override suspend fun workspaces(machineId: String) = listOf(orca)
        override suspend fun worktreeStatus(machineId: String, cwd: String, kind: String?): WorktreeStatus? {
            asked += "$cwd:$kind"
            return WorktreeStatus(path = cwd, branch = "main", checkouts = "/home/user/orca/workspaces/weather")
        }
    }
    private val store = object : AgentCreationStore {
        override suspend fun lastMachine(scope: String) = "computer"
        override suspend fun defaults(scope: String, machine: String) = AgentDefaults(cwd = "/home/user/code", launcher = "claude")
        override suspend fun saveDefaults(scope: String, machine: String, defaults: AgentDefaults) = Unit
        override suspend fun pending(scope: String): AgentStartOperation? = null
        override suspend fun pending(scope: String, value: AgentStartOperation?) = Unit
    }

    @Test fun anOrcaWorkspaceOffersAWorktreeOfItsOwn() {
        val model = NewAgentViewModel(service, store, MutableStateFlow(computers))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                NewAgentPage(model, computers, online = true, onBack = {})
            }
        }
        try {
            rule.waitUntil(10_000) { !model.state.value.loading && model.state.value.workspaces.isNotEmpty() }
            rule.runOnIdle { model.pickWorkspace(orca) }
            // worktree/status is asked a moment after the folder settles (a delay on the main looper's clock)
            rule.waitUntil(10_000) {
                shadowOf(Looper.getMainLooper()).idleFor(Duration.ofMillis(100))
                model.state.value.worktreeOffered
            }
            assertEquals("orca", model.state.value.worktreeEngine)
            assertTrue(asked.last() == "/home/user/code/weather:orca")
            rule.runOnIdle { model.inWorktree(true) }
            val switch = rule.activity.getString(R.string.agents_worktree_switch)
            val out = File(System.getProperty("persona.shots") ?: "build/new-agent-orca-worktree-shots").also { it.mkdirs() }
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                rule.runOnIdle { style = s; dark = d }
                rule.waitForIdle()
                rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(switch))
                rule.onNodeWithText(switch).assertIsDisplayed()
                rule.onRoot().captureToImage().asAndroidBitmap().let { bitmap ->
                    out.resolve("new-agent-orca-worktree-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                        bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it)
                    }
                }
            }
        } finally { model.viewModelScope.cancel() }
    }
}
