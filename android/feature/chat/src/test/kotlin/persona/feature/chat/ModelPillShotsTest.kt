package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionInfo
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHubFeatures
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * The model pill in an Agent's chat and a Persona chat, and the unified model panel (#5055, the web's ModelPill and
 * ModelPanel; DESIGN_SYSTEM.md 3.8), drawn by the real chat page with its composer open: the page (the header without
 * its mode button on Glass, Q11; the model change divider; the mode button and the pill in the composer), then the
 * panel. Glass has the new look; Brief and Board keep theirs (decision 8): their pages are drawn too, to set beside main.
 * On a phone the pill is the provider's tile and the chevron (the web's under-420 rule); the wide scenes show it whole.
 * PNGs `model-*` in `build/model-pill-shots/` (or `-Dpersona.shots=<dir>`). The wire fields come as JSON, so the same
 * scenes draw on main.
 */
/** Main has no pill to open: its run of these scenes draws the pages alone. */
private const val PANELS = true // branch-only
//main: private const val PANELS = false

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h760dp-xhdpi")
class ModelPillShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun session(json: String) = JsonCodec.decodeFromString<Session>(json)
    private val agent = session("""{"machine_id":"box","session_id":"s1","title":"Tidy the sync worker","agent":"claude","status":"idle",
        "state":"idle","pane_id":"w1:p1","cwd":"/srv/app","launcher":"example","model":"claude-sonnet-4-5","effort":"high",
        "permission_mode":"acceptEdits"}""")
    private val waiting = session("""{"machine_id":"box","session_id":"s1","title":"Tidy the sync worker","agent":"claude","status":"idle",
        "state":"idle","pane_id":"w1:p1","cwd":"/srv/app","launcher":"example","model":"claude-sonnet-4-5","effort":"high",
        "permission_mode":"acceptEdits","pending_switch":{"launcher":"work","model":"glm-5.3","effort":"high","at":"2026-11-12T10:00:00Z"}}""")
    private val persona = session("""{"machine_id":"box","session_id":"main","title":"Plan the week","assistant":true,"agent":"claude",
        "status":"idle","state":"idle","launcher":"claude","model":"claude-opus-5-5","effort":"high","permission_mode":"default",
        "model_pick":{"launcher":"team","model":"sonnet"}}""")

    private fun item(json: String) = JsonCodec.decodeFromString<Item>(json)
    private val items = listOf(
        item("""{"id":"u1#0","seq":1,"role":"user","kind":"text","text":"Rename the sync worker's retry flag","ts":"2026-11-12T09:00:00Z"}"""),
        item("""{"id":"a2#0","seq":2,"role":"assistant","kind":"text","text":"Renamed it to `retryOnConflict` in the worker and its tests.","model":"claude-opus-5-5","ts":"2026-11-12T09:01:00Z"}"""),
        item("""{"id":"u3#0","seq":3,"role":"user","kind":"text","text":"Now check the retry path once more","ts":"2026-11-12T09:05:00Z"}"""),
        item("""{"id":"a4#0","seq":4,"role":"assistant","kind":"text","text":"The retry path waits for the lock and tries once; the test covers it.","model":"claude-sonnet-4-5","ts":"2026-11-12T09:06:00Z"}"""),
    )

    private fun model(id: String, name: String, resolved: String? = null, default: Boolean = false, efforts: List<String> = emptyList(),
        description: String? = null) = AgentModel(id, name, description = description, resolved = resolved, isDefault = default,
        efforts = efforts, defaultEffort = if (efforts.isEmpty()) null else "high")
    private val claudeEfforts = listOf("low", "medium", "high", "xhigh", "max")
    private val lists = mapOf(
        "example" to LauncherModels("claude", listOf(model("default", "Default", resolved = "claude-opus-5-5", default = true),
            model("opus", "Opus 5.5", "claude-opus-5-5", efforts = claudeEfforts, description = "Most capable for complex work"),
            model("sonnet", "Sonnet 4.5", "claude-sonnet-4-5", efforts = claudeEfforts, description = "Fast and capable for everyday work"))),
        "work" to LauncherModels("claude", listOf(model("default", "Default", default = true), model("glm-5.3", "GLM 5.3", efforts = claudeEfforts))),
        "cx" to LauncherModels("codex", listOf(model("gpt-5.5", "GPT-5.5"))),
        "claude" to LauncherModels("claude", listOf(model("default", "Default", resolved = "claude-opus-5-5", default = true),
            model("opus", "Opus 5.5", "claude-opus-5-5", efforts = claudeEfforts), model("sonnet", "Sonnet 4.5", efforts = claudeEfforts))),
        "team" to LauncherModels("claude", listOf(model("sonnet", "Sonnet 4.5", efforts = claudeEfforts), model("haiku", "Haiku 4.5"))),
    )

    private val creation = object : AgentCreationService {
        override val scope = "test"
        override suspend fun launchers(machineId: String) = listOf(AgentLauncher("example"), AgentLauncher("work"), AgentLauncher("cx", agentType = "codex"))
        override suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean) = lists[launcher]
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path)
        override suspend fun start(request: AgentStartRequest, operationId: String) = AgentStartResult.Unknown
        override suspend fun operation(operationId: String) = AgentStartResult.Unknown
    }

    private val management = object : AssistantManagement {
        override suspend fun config() = AssistantConfig(machineId = "box", launcher = "claude", main = "main")
        override suspend fun assistantLaunchers(machineId: String) = listOf(AgentLauncher("claude"), AgentLauncher("team"), AgentLauncher("cx", agentType = "codex"))
        override suspend fun assistantLauncherList(machineId: String) = AssistantLaunchers( // branch-only
            listOf(AgentLauncher("claude"), AgentLauncher("team", displayName = "Team API"), AgentLauncher("cx", agentType = "codex")), // branch-only
            LauncherDefault("claude", "opus", "high"), // branch-only
            listOf(UnavailableLauncher("resp", "Responses API", "api", api = "responses", code = "api_protocol", // branch-only
                reason = "Speaks the Responses protocol, which Persona chats can't use"))) // branch-only
        override suspend fun launcherModels(machineId: String, launcher: String) = lists[launcher]
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = Unit
        override suspend fun tasks() = emptyList<ScheduledTask>()
        override suspend fun saveTask(task: ScheduledTask) = task
        override suspend fun deleteTask(id: String) = Unit
        override suspend fun runTask(id: String) = SessionKey("box", "main")
        override suspend fun freshTask(id: String) = ScheduledTask(id)
        override suspend fun runs() = emptyList<Run>()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = Unit
        override suspend fun memory() = emptyList<MemoryFile>()
        override suspend fun saveMemory(name: String, content: String) = Unit
        override suspend fun deleteMemory(name: String) = Unit
    }

    private val actions = object : ChatActions {
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun info(session: Session) = SessionInfo(session.sessionId)
    }

    private fun texts(): List<String> = rule.onAllNodes(SemanticsMatcher("any node") { true }, useUnmergedTree = true).fetchSemanticsNodes().flatMap { node ->
        node.config.getOrNull(SemanticsProperties.Text).orEmpty().map { it.text } + node.config.getOrNull(SemanticsProperties.ContentDescription).orEmpty()
    }

    private fun settle(what: String, condition: (List<String>) -> Boolean) {
        val deadline = System.currentTimeMillis() + 30_000
        while (true) {
            Snapshot.sendApplyNotifications()
            rule.mainClock.advanceTimeByFrame()
            if (condition(texts())) break
            check(System.currentTimeMillis() < deadline) { "$what never showed; shown: ${texts()}" }
            Thread.sleep(20)
        }
        repeat(40) { rule.mainClock.advanceTimeByFrame() }
    }

    private fun save(name: String, style: AppStyle, dark: Boolean, image: Bitmap) {
        val output = File(System.getProperty("persona.shots") ?: "build/model-pill-shots").also { it.mkdirs() }
        File(output, "model-$name-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream()
            .use { assertTrue(image.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }

    /** The chat page of [session] with its composer open; then, on Glass, its model panel when [panel]. */
    private fun page(name: String, session: Session, style: AppStyle, dark: Boolean, panel: Boolean = false) {
        val key = session.key
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine("box", "Workstation", online = true)))
            override val sessions = MutableStateFlow(listOf(session))
            override suspend fun refresh() = Unit
        }
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(items = items, loading = false))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        rule.mainClock.autoAdvance = false
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("launcher_switch", "assistant_model_pick", "typed_models", "sessions_lookup")) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                        ChatScreen(key, chats, sender, interactions, sessions, onBack = {},
                            chrome = ChatChromeBindings(actions = actions, worktrees = { creation }, assistantManagement = management),
                            renderStyle = if (session.assistant) ChatRenderStyle.Assistant else ChatRenderStyle.Agent)
                    }
                }
            }
        }
        settle("the chat") { "Now check the retry path once more" in it }
        rule.onNode(hasSetTextAction()).performClick()
        var opened: (List<String>) -> Boolean = { true }
        opened = { shown -> style != AppStyle.Glass || shown.any { it.startsWith("Select model") } } // branch-only
        settle("the open composer", opened)
        save(name, style, dark, rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap())
        if (!panel || !PANELS) return
        rule.onNodeWithTag("model-pill").performClick()
        settle("the panel") { "Search models…" in it }
        save("$name-panel", style, dark, rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
    }

    @Test fun agentGlass() = page("agent", agent, AppStyle.Glass, false, panel = true)
    @Test fun agentGlassDark() = page("agent", agent, AppStyle.Glass, true, panel = true)
    @Test fun agentBrief() = page("agent", agent, AppStyle.Brief, false)
    @Test fun agentBriefDark() = page("agent", agent, AppStyle.Brief, true)
    @Test fun agentBoard() = page("agent", agent, AppStyle.Board, false)
    @Test fun agentBoardDark() = page("agent", agent, AppStyle.Board, true)
    @Test fun personaGlass() = page("persona", persona, AppStyle.Glass, false, panel = true)
    @Test fun personaGlassDark() = page("persona", persona, AppStyle.Glass, true, panel = true)
    @Test fun personaBrief() = page("persona", persona, AppStyle.Brief, false)
    @Test fun personaBriefDark() = page("persona", persona, AppStyle.Brief, true)
    @Test fun personaBoard() = page("persona", persona, AppStyle.Board, false)
    @Test fun personaBoardDark() = page("persona", persona, AppStyle.Board, true)

    // A tablet's composer has room for the whole pill: provider, model, engine, effort.
    @Config(qualifiers = "en-w720dp-h760dp-xhdpi") @Test fun agentWideGlass() = page("agent-wide", agent, AppStyle.Glass, false)
    @Config(qualifiers = "en-w720dp-h760dp-xhdpi") @Test fun agentWideGlassDark() = page("agent-wide", agent, AppStyle.Glass, true)
    @Config(qualifiers = "en-w720dp-h760dp-xhdpi") @Test fun waitingWideGlass() = page("waiting-wide", waiting, AppStyle.Glass, false, panel = true)
    @Config(qualifiers = "en-w720dp-h760dp-xhdpi") @Test fun waitingWideGlassDark() = page("waiting-wide", waiting, AppStyle.Glass, true, panel = true)
    @Config(qualifiers = "en-w720dp-h760dp-xhdpi") @Test fun personaWideGlass() = page("persona-wide", persona, AppStyle.Glass, false)
    @Config(qualifiers = "en-w720dp-h760dp-xhdpi") @Test fun personaWideGlassDark() = page("persona-wide", persona, AppStyle.Glass, true)

    /** What the pill says on Glass and the panel holds (branch only: main has neither). */
    @Test fun thePillAndThePanelSayWhatRuns() { // branch-only
        page("check", waiting, AppStyle.Glass, false, panel = true) // branch-only
        val shown = texts() // branch-only
        assertTrue(shown.toString(), shown.any { it == "Select model. Next message: work · GLM 5.3, thinking effort: High" }) // branch-only
        assertTrue(shown.toString(), "Switches on your next message: the Agent restarts on work and keeps this conversation" in shown) // branch-only
        assertTrue(shown.toString(), "Switching from Claude Code to Codex needs a new conversation" in shown) // branch-only
        assertEquals("the header has no mode button on Glass (Q11)", 1, shown.count { it.startsWith("Permission mode:") }) // branch-only
    } // branch-only
}
