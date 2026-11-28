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
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.domain.sort.AgentGrouping
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.time.Instant

/**
 * Orca Agents on Android (#17054): By to-do, the ones opened by hand in Orca (a terminal and one of Orca's own chats) share
 * **Opened by hand** with herdr's; an Orca terminal carries the **In a tab** mark and the host badge "Orca", an Orca chat
 * the **Desktop app** mark; the Rename dialog of an Orca tab names Orca, not herdr. Every style, light and dark. Written as
 * PNGs to `build/orca-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class AgentsOrcaShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/orca-shots").also { it.mkdirs() }

    private val now = Instant.now()
    private fun ago(minutes: Long) = now.minusSeconds(minutes * 60).toString()
    private val machines = listOf(Machine("mac", "MacBook Pro", online = true, engine = "orca"))
    private val orcaTab = Session("mac", "o1", title = "Fix the login form", agent = "claude", cwd = "/code/app", state = "needs_you",
        spaceKind = "orca", spaceId = "wt-login", spaceName = "login-form", slotId = "orca:tab1", slotName = "login",
        lastText = "Run the tests now?", updatedAt = ago(5), paneId = "orca:tab1:leaf1")
    private val orcaChat = Session("mac", "o2", title = "Plan the release notes", agent = "claude", cwd = "/code/app", state = "unread",
        spaceKind = "orca", spaceId = "wt-notes", spaceName = "release-notes", sourceKind = "runtime", sourceInstanceId = "orca",
        capabilities = SessionCapabilities(read = true, send = true), lastText = "Drafted the notes.", updatedAt = ago(12))
    private val herdrByHand = Session("mac", "h1", title = "Update the setup guide", agent = "claude", cwd = "/code/docs", state = "idle",
        spaceKind = "herdr", spaceId = "w2", spaceName = "docs", slotName = "1", lastText = "Done.", updatedAt = ago(30), paneId = "w2:p1")
    private val herdrStarted = Session("mac", "h2", title = "Refactor the API client", agent = "codex", cwd = "/code/api", state = "working",
        spaceKind = "herdr", spaceId = "w3", spaceName = "api", slotName = "1", lastText = "Moving the retries.", updatedAt = ago(2),
        paneId = "w3:p1", startedByPersona = true)
    private val sessions = listOf(orcaTab, orcaChat, herdrByHand, herdrStarted)

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override val lastHello: Hello get() = Hello(serverVersion = "1.0.0", protocol = 1)
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private class Interactions : InteractionCenter {
        override val pending = MutableStateFlow<List<Interaction>>(emptyList())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private class Sender : MessageSender {
        override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private class Repository(sessions: List<Session>, machines: List<Machine>) : SessionsRepository {
        override val sessions = MutableStateFlow(sessions)
        override val machines = MutableStateFlow(machines)
        override suspend fun refresh() = Unit
    }
    private object Nowhere : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private class Api : TasksApi {
        override suspend fun todos() = emptyList<Todo>()
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList(), emptyMap())
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    /** The list's model, By to-do (set before it starts, as `AgentsTodoTreeShotsTest` does), with no to-dos. */
    private fun model(): AgentsViewModel {
        val store = object : DataStore<Preferences> {
            override val data = MutableStateFlow<Preferences>(emptyPreferences())
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
                transform(data.value).also { data.value = it }
        }
        val hub = object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot())
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        }
        val prefs = AgentsPreferences(store, hub)
        kotlinx.coroutines.runBlocking { prefs.grouping(AgentGrouping.Todo) }
        val tasks = persona.core.domain.tasks.TasksStore(Api(), Connection(), kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.Dispatchers.Unconfined))
        tasks.reloadTodos()
        return AgentsViewModel(Repository(sessions, machines), Interactions(), Connection(), Sender(), prefs, tasks = tasks)
    }

    private fun save(node: SemanticsNodeInteraction, name: String) {
        val bitmap = node.captureToImage().asAndroidBitmap()
        File(out(), "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }

    private fun exists(text: String, substring: Boolean = false) =
        assertTrue(text, rule.onAllNodesWithText(text, substring = substring, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())

    @Test fun orcaRowsByToDo() {
        val model = model()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1000.dp).background(PersonaTheme.palette.background)) {
                    AgentsScreen(model, Nowhere, AgentsMetadata())
                }
            }
        }
        rule.waitForIdle()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            exists("Opened by hand")
            exists("Desktop app")
            exists("In a tab")
            // the host's name beside the place: Glass's row line (Brief and Board keep their facts line)
            if (s == AppStyle.Glass) exists("Orca", substring = true)
            save(rule.onNodeWithTag("shot"), "orca-todo-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }

    // 320dp wide: on this JVM a JDialog holding a JTextField never goes idle at 360dp and wider (FileEditsShotsTest).
    @Config(sdk = [35], qualifiers = "en-w320dp-h800dp-xhdpi")
    @Test fun renamingAnOrcaTabNamesOrca() {
        val model = model()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                AgentOperationDialog(dialog = AgentOperationDialog.Rename(orcaTab), model = model, machineName = "MacBook Pro", dismiss = {})
            }
        }
        rule.waitForIdle()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            exists("Tab name (Orca tab)")
            exists("The tab name syncs to Orca", substring = true)
            assertTrue(rule.onAllNodesWithText("herdr", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
            save(rule.onNode(isDialog()), "orca-rename-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }
}
