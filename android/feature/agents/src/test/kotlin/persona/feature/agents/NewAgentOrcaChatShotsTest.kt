package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * New Agent on a computer that starts new Agents as Orca chats (#17650): the line over Start saying the default start opens
 * as the hub's Orca chat. Every style, light and dark; PNGs go to `build/orca-chat-shots/` (or `-Dpersona.shots=<dir>`).
 * Machines and folders are made up.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-xhdpi")
class NewAgentOrcaChatShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/orca-chat-shots").also { it.mkdirs() }

    private val machines = listOf(Machine("box", "Workstation", online = true, engineHosts = listOf("herdr", "orca"), orcaChatStarts = "on"))

    private class MemoryDataStore : DataStore<Preferences> {
        override val data = MutableStateFlow<Preferences>(emptyPreferences())
        override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
            transform(data.value).also { data.value = it }
    }

    private class Service : AgentCreationService {
        override val scope = "hub/pairing"
        override suspend fun launchers(machineId: String) = listOf(AgentLauncher("claude"), AgentLauncher("orca-claude"))
        override suspend fun launcherList(machineId: String) = AssistantLaunchers(launchers(machineId),
            defaultOpens = DefaultOpens("orca-claude", "Orca chat · Claude Code", listOf("default", "acceptEdits", "auto", "bypassPermissions")))
        override suspend fun directories(machineId: String, path: String) =
            AgentDirectories(path.ifEmpty { "/code" }, recent = listOf(AgentRecentDirectory("/code/notes-app")))
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult =
            AgentStartResult.Created(Session(machineId = request.machineId, sessionId = "s1"))
        override suspend fun operation(operationId: String): AgentStartResult = AgentStartResult.Unknown
        override suspend fun newAgentModel() = ModelPurposeDetail(ModelPurpose("agent.new", effective = ModelBinding("launcher.claude", "")),
            listOf(ModelProvider("launcher.claude", "claude")))
        override suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean) =
            LauncherModels("claude", listOf(AgentModel("default", "Default", isDefault = true)))
    }

    @Test fun theLineOverStartInEveryStyle() {
        val model = NewAgentViewModel(Service(), AgentCreationPreferences(RuntimeEnvironment.getApplication(), MemoryDataStore()), MutableStateFlow(machines))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    NewAgentPage(model, machines, online = true, onBack = {})
                }
            }
        }
        val line = rule.activity.getString(R.string.agents_new_opens_orca_chat, "Orca chat · Claude Code")
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithTag("newAgent.orcaChat").assertTextEquals(line)
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "new-agent-orca-chat-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
    }
}
