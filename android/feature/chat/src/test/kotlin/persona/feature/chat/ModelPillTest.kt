package persona.feature.chat

import android.content.Context
import android.graphics.Bitmap
import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.assertContentDescriptionContains
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File
import java.net.SocketTimeoutException
import persona.core.contracts.JsonCodec
import persona.core.contracts.ModelPick
import persona.core.contracts.PendingSwitch
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.AgentMarkKind
import persona.core.designsystem.AgentMarkSpec
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHubFeatures
import persona.core.designsystem.JLoadable
import persona.core.designsystem.ModelPillWidth
import persona.core.designsystem.PersonaTheme
import persona.core.domain.models.AgentModelPanel
import persona.core.domain.models.PanelConfig
import persona.core.domain.models.PanelFavorite
import persona.core.domain.models.PanelOverride
import persona.core.domain.models.PanelPrefs

/**
 * The model pill's rules on the page (#5055): which message carries a waiting switch out, a switch drawn at once until
 * the next push, what the phone keeps, where Glass shows the mode, the pill's words; and a Persona chat's pick written
 * (`PUT …/model`) and cleared (`DELETE`) through the real pill and panel.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class ModelPillTest {
    @get:Rule val rule = createComposeRule()
    private val res = ApplicationProvider.getApplicationContext<Context>().resources
    private val agent = Session("box", "s1", agent = "claude", launcher = "example", status = "idle", state = "idle", paneId = "p1", rev = 7)

    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun mainAndSharedCatalogCachesStaySeparateAcrossRefreshFailure() = kotlinx.coroutines.test.runTest {
        val lists = ModelLists(this) { it.message.orEmpty() }
        val shared = AssistantLaunchers(default = LauncherDefault("claude", "shared", "low"))
        val main = AssistantLaunchers(default = LauncherDefault("claude", "main", "high"))
        lists.readAssistantCatalog("box") { shared }
        lists.readAssistantCatalog("box", main = true) { main }
        testScheduler.advanceUntilIdle()
        assertEquals(shared, (lists.assistantCatalog("box") as JLoadable.Loaded<AssistantLaunchers>).value)
        assertEquals(main, (lists.assistantCatalog("box", main = true) as JLoadable.Loaded<AssistantLaunchers>).value)
        lists.readAssistantCatalog("box", main = true) { throw SocketTimeoutException("main temporarily unavailable") }
        testScheduler.advanceUntilIdle()
        val retained = lists.assistantCatalog("box", main = true) as JLoadable.Loaded<AssistantLaunchers>
        assertEquals(main, retained.value)
        assertTrue(retained.failure != null)
        assertEquals(shared, (lists.assistantCatalog("box") as JLoadable.Loaded<AssistantLaunchers>).value)
    }

    @Test fun mainPillReadsItsOwnDefaultAndNewChatKeepsTheSharedDefault() {
        var mainReads = 0
        val hub = object : Hub() {
            override suspend fun mainAssistantLauncherList(machineId: String, refresh: Boolean): AssistantLaunchers {
                mainReads++
                return assistantLauncherList(machineId, refresh).copy(default = LauncherDefault("team", "sonnet", "low"))
            }
        }
        var showMain by mutableStateOf(true)
        val main = agent.copy(sessionId = "main", assistant = true, launcher = "claude", model = "opus")
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    PersonaModelPill(if (showMain) main else null, "box", hub, ModelPillWidth.Wide,
                        pagePick = null, onPagePick = {}, onNotice = {})
                }
            }
        }
        rule.onNodeWithTag("model-pill").assertContentDescriptionContains("Sonnet", substring = true)
        rule.runOnIdle { assertTrue(mainReads > 0); showMain = false }
        rule.onNodeWithTag("model-pill").assertContentDescriptionContains("Opus", substring = true)
    }

    @Test fun onlyTheUsersMessageOnGlassCarriesAWaitingSwitchOut() {
        val waiting = agent.copy(pendingSwitch = PendingSwitch("work"))
        assertTrue(switchesNext(waiting, on = true))
        assertFalse("Brief and Board show no switch: their messages leave it waiting", switchesNext(waiting, on = false))
        assertFalse("none waits", switchesNext(agent, on = true))
        assertFalse("its own launcher is no switch", switchesNext(agent.copy(pendingSwitch = PendingSwitch("example")), on = true))
        assertFalse(switchesNext(null, on = true))
    }

    @Test fun aSwitchDrawnAtOnceStandsUntilAWholeRowIsObserved() {
        val drawn = DrawnSwitch(PendingSwitch("work", "glm-5.3"), observation = 1L)
        assertEquals(PendingSwitch("work", "glm-5.3"), drawn.over(agent, 1L).pendingSwitch)
        assertEquals("a transcript size change isn't evidence of a new switch", drawn.switch,
            drawn.over(agent.copy(rev = 8), 1L).pendingSwitch)
        assertNull("a fresh whole row wins even at the same transcript size", drawn.over(agent, 2L).pendingSwitch)
        assertNull("a local cancel gives immediate feedback",
            DrawnSwitch(null, 1L).over(agent.copy(pendingSwitch = PendingSwitch("work")), 1L).pendingSwitch)
        assertEquals(agent, (null as DrawnSwitch?).over(agent, 1L))
    }

    @Test fun whatThePhoneKeepsReadsBack() {
        val prefs = PanelPrefs(listOf(PanelFavorite("f1", PanelConfig("work", "glm-5.3", "high"))), listOf(PanelConfig("claude", "opus", fast = true)),
            mapOf(PanelPrefs.overrideKey("claude", "sonnet") to PanelOverride(effort = "low")))
        assertEquals(prefs, PanelPrefsStore.decode(PanelPrefsStore.encode(prefs).toString()))
        assertEquals(PanelPrefs(), PanelPrefsStore.decode("not json"))
        assertEquals(PanelPrefs(), PanelPrefsStore.decode(null))
    }

    @Test fun glassShowsTheModeInTheComposer() {
        assertTrue("Claude Code that can change it", SessionModes.showsInComposer(agent.copy(permissionMode = "plan")))
        assertTrue("a Persona chat that can change it", SessionModes.showsInComposer(Session("box", "c1", assistant = true, status = "idle")))
        assertFalse("a Codex thread", SessionModes.showsInComposer(Session("box", "x", agent = "codex", status = "idle")))
    }

    @Test fun thePillSaysVeryHighAsExtra() {
        assertEquals("Extra", pillEffort("xhigh", res))
        assertEquals("High", pillEffort("high", res))
        val (content, spoken) = pillContent(persona.core.domain.models.PillView(persona.core.domain.models.PillSetup("claude", "work", "Opus 5.5", "xhigh")),
            null, ultracode = true, res)
        assertEquals("Extra", content.effort)
        assertTrue(content.ultracode)
        assertEquals("Select model. Current: Opus 5.5, thinking effort: Very high", spoken)
    }

    /** #8143: a Paseo provider is named and marked as itself on the pill and in the panel's words, never as "paseo". */
    @Test fun aPaseoProviderIsNamedAndMarkedAsItself() {
        val grok = Session("box", "paseo-1", agent = "paseo", agentLabelHint = "My Grok", launcher = "grok", status = "idle", state = "idle")
        val names = persona.core.domain.models.AgentModelPanel.engineNames(grok,
            listOf(AgentLauncher("claude"), AgentLauncher("grok", agentType = "paseo"), AgentLauncher("traecli", agentType = "paseo")))
        val words = panelWords(res, names)
        assertEquals("Switching from My Grok to Claude Code needs a new conversation",
            words.reason(persona.core.domain.models.PanelReason.OtherEngine("grok", "claude")))
        assertEquals("My Grok", words.engine("grok"))
        assertEquals(persona.core.designsystem.AgentMarkSpec(persona.core.designsystem.AgentMarkKind.Grok, "My Grok", null), words.mark("grok"))
        // A provider whose name is a mark the design system has draws it.
        assertEquals(persona.core.designsystem.AgentMarkKind.Trae, words.mark("traecli")?.kind)
        assertEquals("TRAE CLI", words.engine("traecli"))
        assertEquals("TRAE CLI", words.engine("traex"))
        assertEquals("Claude Code", words.engine("claude"))
        val (content, spoken) = pillContent(persona.core.domain.models.PillView(persona.core.domain.models.PillSetup("grok", "grok", "Grok 5", null)),
            null, ultracode = false, res, names)
        assertEquals(persona.core.designsystem.AgentMarkSpec(persona.core.designsystem.AgentMarkKind.Grok, "My Grok", null), content.engine)
        assertEquals("Select model. Current: Grok 5", spoken)
        // Without names (a Persona chat's pill): the engine's id, as before.
        assertEquals("paseo", panelWords(res).engine("paseo"))
        assertEquals(persona.core.designsystem.AgentMarkSpec(null, "custom-provider", null),
            panelWords(res).mark("custom-provider"))
    }

    @Test fun thePaseoCliUsesItsTitleAndKeepsItsMarkForEveryIdentifier() {
        val cliKinds = mapOf("traex" to AgentMarkKind.Trae, "traecli" to AgentMarkKind.Trae, "trae-cli" to AgentMarkKind.Trae)
        for ((engine, kind) in cliKinds) {
            for ((title, expected) in listOf(null to "TRAE CLI", "TRAEX" to "TRAE CLI", "My CLI" to "My CLI")) {
                val launcher = AgentLauncher(engine, agentType = "paseo", displayName = title)
                val words = panelWords(res, AgentModelPanel.engineNames(agent, listOf(launcher)))
                assertEquals(AgentMarkSpec(kind, expected, null), words.mark(engine))
                assertEquals(expected, words.engine(engine))
            }
            assertEquals(AgentMarkSpec(kind, "TRAE CLI", null), engineMark(engine, " "))
        }
        assertEquals(AgentMarkSpec(AgentMarkKind.Trae, "TRAE", null), engineMark("trae"))
    }

    /** Records what the pill asks the hub. */
    private open class Hub : AssistantManagement {
        val asked = mutableListOf<String>()
        val launcherListRefreshes = mutableListOf<Boolean>()
        var configReads = 0
            private set
        var launcherListReads = 0
            private set
        override suspend fun config(): AssistantConfig {
            configReads++
            return AssistantConfig(machineId = "box", launcher = "claude", main = "main")
        }
        override suspend fun mainAssistantLauncherList(machineId: String, refresh: Boolean): AssistantLaunchers =
            assistantLauncherList(machineId, refresh)
        override suspend fun assistantLauncherList(machineId: String): AssistantLaunchers {
            launcherListReads++
            return AssistantLaunchers(
                listOf(AgentLauncher("claude"), AgentLauncher("team", displayName = "Team API")), LauncherDefault("claude", "opus", "high"))
        }
        override suspend fun assistantLauncherList(machineId: String, refresh: Boolean): AssistantLaunchers {
            launcherListRefreshes += refresh
            return assistantLauncherList(machineId)
        }
        override suspend fun launcherModels(machineId: String, launcher: String) = when (launcher) {
            "claude" -> LauncherModels("claude", listOf(AgentModel("default", "Default", resolved = "claude-opus-5-5", isDefault = true),
                AgentModel("opus", "Opus 5.5", resolved = "claude-opus-5-5")))
            else -> LauncherModels("claude", listOf(AgentModel("sonnet", "Sonnet 4.5", resolved = "claude-sonnet-4-5")))
        }
        override suspend fun setModelPick(session: SessionKey, pick: ModelPick) = pick.also { asked += "PUT ${session.sessionId} $it" }
        override suspend fun clearModelPick(session: SessionKey) { asked += "DELETE ${session.sessionId}" }
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

    private fun hasDialogText(text: String, substring: Boolean = false) = hasText(text, substring = substring) and hasAnyAncestor(isDialog())

    private fun showAssistantPill(hub: Hub) {
        val session = Session("box", "main", assistant = true, launcher = "claude", status = "idle", rev = 3)
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    Box {
                        PersonaModelPill(session, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = {}, onNotice = {})
                    }
                }
            }
        }
    }

    private fun waitForDialogText(text: String) = rule.waitUntil(5_000) {
        rule.onAllNodes(hasDialogText(text), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
    }

    @Test fun anAssistantProviderTimeoutCanBeRetriedInTheOpenSheet() = assertAssistantProviderRetry(SocketTimeoutException())

    @Test fun aDiscardedAssistantRouteCanBeRetriedInTheOpenSheet() = assertAssistantProviderRetry(CancellationException("Pairing changed"))

    private fun assertAssistantProviderRetry(failure: Exception) {
        val first = CompletableDeferred<Unit>()
        val retry = CompletableDeferred<Unit>()
        val hub = object : Hub() {
            override suspend fun assistantLauncherList(machineId: String): AssistantLaunchers {
                val result = super.assistantLauncherList(machineId)
                (if (launcherListReads == 1) first else retry).await()
                return result
            }
        }
        showAssistantPill(hub)
        rule.onNodeWithTag("model-pill").performClick()
        waitForDialogText("Loading models…")
        rule.runOnIdle { first.completeExceptionally(failure) }
        waitForDialogText("Couldn't load models")
        rule.onNode(hasDialogText("Loading models…"), useUnmergedTree = true).assertDoesNotExist()

        rule.onNode(hasDialogText("Retry"), useUnmergedTree = true).performClick()
        waitForDialogText("Loading models…")
        rule.runOnIdle { retry.complete(Unit) }
        waitForDialogText("Sonnet 4.5")
        rule.onNode(hasDialogText("Retry"), useUnmergedTree = true).assertDoesNotExist()
        rule.runOnIdle { assertEquals(listOf(false, true), hub.launcherListRefreshes) }
    }

    @Test fun openingTheAssistantModelSheetRetriesAFailedPrefetch() {
        val first = CompletableDeferred<Unit>()
        val retry = CompletableDeferred<Unit>()
        val hub = object : Hub() {
            override suspend fun assistantLauncherList(machineId: String): AssistantLaunchers {
                val result = super.assistantLauncherList(machineId)
                (if (launcherListReads == 1) first else retry).await()
                return result
            }
        }
        showAssistantPill(hub)
        rule.waitUntil(5_000) { hub.launcherListReads == 1 }
        rule.runOnIdle { first.completeExceptionally(SocketTimeoutException()) }
        rule.onNode(isDialog()).assertDoesNotExist()

        rule.onNodeWithTag("model-pill").performClick()
        waitForDialogText("Loading models…")
        rule.runOnIdle {
            assertEquals(listOf(false, true), hub.launcherListRefreshes)
            retry.complete(Unit)
        }
        waitForDialogText("Sonnet 4.5")
        rule.onNode(hasDialogText("Retry"), useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun aFailedAssistantProviderRefreshKeepsTheModelsInTheOpenSheet() {
        val refresh = CompletableDeferred<Unit>()
        val hub = object : Hub() {
            override suspend fun assistantLauncherList(machineId: String): AssistantLaunchers {
                val result = super.assistantLauncherList(machineId)
                if (launcherListReads > 1) refresh.await()
                return result
            }
        }
        showAssistantPill(hub)
        rule.waitUntil(5_000) { hub.launcherListReads == 1 }
        rule.onNodeWithTag("model-pill").performClick()
        waitForDialogText("Sonnet 4.5")
        rule.waitUntil(5_000) { hub.launcherListReads == 2 }
        rule.runOnIdle { refresh.completeExceptionally(SocketTimeoutException()) }
        waitForDialogText("Retry")
        rule.runOnIdle { assertEquals(listOf(false, false), hub.launcherListRefreshes) }
        rule.onNodeWithTag("model-panel-list").performScrollToNode(hasText("Sonnet 4.5"))
        rule.onNode(hasDialogText("Sonnet 4.5"), useUnmergedTree = true).assertExists()
        rule.onNode(hasDialogText("Loading models…"), useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun aFallbackProviderCanRetryAfterTheRouteChangedAndShowAnEmptyModelList() {
        val first = CompletableDeferred<LauncherModels>()
        val retry = CompletableDeferred<LauncherModels>()
        val hub = object : Hub() {
            private var reads = 0

            override suspend fun assistantLauncherList(machineId: String) = AssistantLaunchers()
            override suspend fun launcherModels(machineId: String, launcher: String): LauncherModels =
                (if (++reads == 1) first else retry).await()
        }
        showAssistantPill(hub)
        rule.onNodeWithTag("model-pill").performClick()
        rule.runOnIdle { first.completeExceptionally(CancellationException("Pairing changed")) }
        waitForDialogText("Retry")
        rule.onNode(hasDialogText("Retry"), useUnmergedTree = true).performClick()
        rule.onNode(hasDialogText("Retry"), useUnmergedTree = true).assertDoesNotExist()
        rule.runOnIdle { retry.complete(LauncherModels()) }
        waitForDialogText("No models available")
        rule.onNode(hasDialogText("Loading models…"), useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun aPersonaChatsPickIsKeptOnTheHubAndUndone() {
        val hub = Hub()
        val chat = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"main","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-opus-5-5","effort":"high","rev":3}""")
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    Box { PersonaModelPill(chat, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = { error("kept on the hub") }, onNotice = {}) }
                }
            }
        }
        rule.waitUntil(5_000) { rule.onAllNodes(hasText("Opus 5.5", substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("model-pill").performClick()
        rule.waitUntil(5_000) { rule.onAllNodes(hasDialogText("Sonnet 4.5"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
        // the main chat with nothing picked follows the default
        assertTrue(rule.onAllNodes(hasText("Following the default", substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
        rule.onNode(hasDialogText("Sonnet 4.5"), useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { hub.asked.isNotEmpty() }
        assertEquals(listOf("PUT main ModelPick(launcher=team, model=sonnet, effort=null)"), hub.asked)
        // drawn at once: the pill says the pick before the hub's push
        rule.waitUntil(5_000) { rule.onAllNodes(hasText("Sonnet 4.5", substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("model-pill").performClick()
        rule.waitUntil(5_000) { rule.onAllNodes(hasDialogText("Follow default"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
        assertTrue(rule.onAllNodes(hasText("Picked for this chat · Team API · Sonnet 4.5"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
        rule.onNode(hasDialogText("Follow default"), useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { hub.asked.size == 2 }
        assertEquals("DELETE main", hub.asked.last())
    }

    @Test fun theModelRouteRemedyOpensAndClearsANonMainPersonaPicker() {
        val hub = Hub()
        val chat = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"leader","assistant":true,"agent":"claude","status":"idle",
            "launcher":"team","model":"sonnet","model_pick":{"launcher":"team","model":"sonnet"},"rev":3}""")
        var open by mutableStateOf(false)
        var clear by mutableStateOf(false)
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    Box { PersonaModelPill(chat, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = {}, onNotice = {},
                        openRequested = open, onOpenHandled = { open = false }, clearRequested = clear, onClearHandled = { clear = false }) }
                }
            }
        }
        rule.onNodeWithTag("model-pill").assertExists()
        rule.runOnIdle { open = true }
        rule.waitUntil(5_000) { rule.onAllNodes(hasDialogText("Sonnet 4.5"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
        assertFalse(open)
        assertTrue(hub.asked.isEmpty())
        rule.runOnIdle { clear = true }
        rule.waitUntil(5_000) { hub.asked.contains("DELETE leader") }
        assertFalse(clear)
        assertEquals(listOf("DELETE leader"), hub.asked)
    }

    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    @Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
    @Test fun aSavedAliasSelectsTheFoldedRowInLightAndDark() = aliasPanelScene(false)

    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    @Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi", fontScale = 2f)
    @Test fun aSavedAliasSelectsTheFoldedRowWithLargeText() = aliasPanelScene(true)

    private fun aliasPanelScene(largeText: Boolean) {
        org.robolectric.RuntimeEnvironment.setFontScale(if (largeText) 2f else 1f)
        val base = Hub()
        val folded = AgentModel("claude-opus-5-5", "Opus 5.5", aliases = listOf("opus", "fable"), efforts = listOf("low", "high"))
        val hub = object : AssistantManagement by base {
            override suspend fun launcherModels(machineId: String, launcher: String) = LauncherModels("claude",
                listOf(folded, AgentModel("claude-sonnet-4-5", "Sonnet 4.5", efforts = listOf("low", "high"))))
        }
        val chat = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"main","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-opus-5-5","model_pick":{"launcher":"claude","model":"opus","effort":"high"},"rev":3}""")
        var open by mutableStateOf(false)
        var dark by mutableStateOf(false)
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick")) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                    Box { PersonaModelPill(chat, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = {}, onNotice = {},
                        openRequested = open, onOpenHandled = { open = false }) }
                }
            }
        }
        rule.runOnIdle { open = true }
        rule.waitUntil(5_000) { rule.onAllNodes(hasDialogText("Sonnet 4.5"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
        val out = File(System.getProperty("persona.shots") ?: "build/model-alias-shots").apply { mkdirs() }
        for (night in listOf(false, true)) {
            rule.runOnIdle { dark = night }
            rule.waitForIdle()
            val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
            File(out, "model-alias-${if (largeText) "large" else "default"}-${if (night) "dark" else "light"}.png")
                .outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    @Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
    @Test fun aPersonaChatsAllListNamesItsLauncher() {
        val hub = Hub()
        val chat = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"main","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-opus-5-5","effort":"high","rev":3}""")
        var dark by mutableStateOf(false)
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick")) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                    Box { PersonaModelPill(chat, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = {}, onNotice = {}) }
                }
            }
        }
        rule.onNodeWithTag("model-pill").performClick()
        rule.waitUntil(5_000) { rule.onAllNodes(hasDialogText("Sonnet 4.5"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
        val out = File(System.getProperty("persona.shots") ?: "build/model-pill-launcher-shots").apply { mkdirs() }
        for (night in listOf(false, true)) {
            rule.runOnIdle { dark = night }
            rule.waitForIdle()
            val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
            File(out, "model-panel-${if (night) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
        rule.onNode(hasDialogText("Team API · claude-sonnet-4-5"), useUnmergedTree = true).assertExists()
    }

    @Test fun nonMainAssistantConversationsExposeTheModelPillAndKeepPicks() {
        val hub = object : Hub() {
            override suspend fun config(): AssistantConfig =
                super.config().copy(leaders = listOf("leader-1"))
        }
        val side = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"side","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-opus-5-5","effort":"high","rev":3}""")
        val leader = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"leader-1","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-opus-5-5","effort":"high","rev":3}""")
        var current by mutableStateOf(side)
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    key(current.key) {
                        Box { PersonaModelPill(current, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = { error("kept on hub") }, onNotice = {}) }
                    }
                }
            }
        }
        rule.waitUntil(5_000) { hub.configReads >= 1 && hub.launcherListReads >= 1 }
        rule.onNodeWithTag("model-pill").assertExists().performClick()
        waitForDialogText("Sonnet 4.5")
        rule.onNode(hasDialogText("Sonnet 4.5"), useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { hub.asked.contains("PUT side ModelPick(launcher=team, model=sonnet, effort=null)") }
        rule.onNodeWithTag("model-pill").performClick()
        waitForDialogText("Undo pick")
        rule.onNode(hasDialogText("Undo pick"), useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { hub.asked.contains("DELETE side") }

        rule.runOnIdle { current = leader }
        rule.waitUntil(5_000) { rule.onAllNodes(hasText("Opus 5.5", substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("model-pill").assertExists().performClick()
        waitForDialogText("Sonnet 4.5")
        assertTrue(rule.onAllNodes(hasText("Following the default", substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
        rule.onNode(hasDialogText("Sonnet 4.5"), useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { hub.asked.contains("PUT leader-1 ModelPick(launcher=team, model=sonnet, effort=null)") }
        rule.onNodeWithTag("model-pill").performClick()
        waitForDialogText("Follow default")
        rule.onNode(hasDialogText("Follow default"), useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { hub.asked.contains("DELETE leader-1") }
    }

    private open class SwitchHub(
        var segments: ConversationSwitchState = ConversationSwitchState(enabled = true, epoch = 4, supervisionRequired = false),
        var submitBehavior: (ConversationSwitchRequest) -> Unit = {},
    ) : Hub() {
        var segmentReads = 0
        val submitted = mutableListOf<ConversationSwitchRequest>()

        override suspend fun assistantLauncherList(machineId: String): AssistantLaunchers = AssistantLaunchers(
            launchers = listOf(
                AgentLauncher("claude", source = "builtin"),
                AgentLauncher("agy", agentType = "agy", displayName = "Antigravity CLI", source = "agy", assistant = true, supervisionPolicy = false),
            ),
            default = LauncherDefault("claude", "opus", "high"),
        )

        override suspend fun launcherModels(machineId: String, launcher: String) = when (launcher) {
            "agy" -> LauncherModels("agy", listOf(AgentModel("gemini-3", "Gemini 3", efforts = listOf("low", "high"))))
            else -> super.launcherModels(machineId, launcher)
        }

        override suspend fun conversationSwitchState(session: SessionKey): ConversationSwitchState {
            segmentReads++
            return segments
        }

        override suspend fun submitConversationSwitch(session: SessionKey, request: ConversationSwitchRequest) {
            submitted += request
            submitBehavior(request)
        }
    }

    @Test fun crossEngineSwitchRequiresConfirmationAndCancelSendsNothing() {
        val hub = SwitchHub()
        val chat = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"main","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-opus-5-5","effort":"high","permission_mode":"acceptEdits","rev":3}""")
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick", "assistant_segments")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    Box { PersonaModelPill(chat, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = {}, onNotice = {}) }
                }
            }
        }
        rule.onNodeWithTag("model-pill").performClick()
        waitForDialogText("Gemini 3")
        rule.onNode(hasDialogText("Gemini 3"), useUnmergedTree = true).performClick()
        waitForDialogText("Switch this conversation?")
        assertTrue(rule.onAllNodes(hasDialogText("Continue with Antigravity CLI", substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
        rule.runOnIdle {
            assertTrue("picking alone must not call setModelPick", hub.asked.isEmpty())
            assertTrue("picking alone must not submit switch", hub.submitted.isEmpty())
        }
        rule.onNode(hasDialogText("Cancel"), useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { rule.onAllNodes(hasDialogText("Switch this conversation?"), useUnmergedTree = true).fetchSemanticsNodes().isEmpty() }
        rule.runOnIdle {
            assertTrue(hub.asked.isEmpty())
            assertTrue(hub.submitted.isEmpty())
        }
    }

    @Test fun crossEngineSwitchPreservesOperationIdAfterUncertainFailureAndRetriesIdempotently() {
        var failFirst = true
        val hub = SwitchHub(submitBehavior = {
            if (failFirst) {
                failFirst = false
                throw persona.core.hub.HubFailure(
                    persona.core.hub.HubError.Server,
                    message = "response unconfirmed",
                    status = 502,
                    code = "timeout",
                    delivered = "unknown",
                )
            }
        })
        val notices = mutableListOf<String>()
        var switchedCount = 0
        val chat = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"leader-2","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-opus-5-5","effort":"high","permission_mode":"acceptEdits","rev":3}""")
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick", "assistant_segments")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    Box {
                        PersonaModelPill(
                            chat,
                            "box",
                            hub,
                            ModelPillWidth.Wide,
                            pagePick = null,
                            onPagePick = {},
                            onNotice = { notices += it },
                            onSwitched = { switchedCount++ },
                        )
                    }
                }
            }
        }
        rule.onNodeWithTag("model-pill").performClick()
        waitForDialogText("Gemini 3")
        rule.waitUntil(5_000) { hub.segmentReads == 1 }
        rule.onNode(hasDialogText("Gemini 3"), useUnmergedTree = true).performClick()
        waitForDialogText("Switch connection")
        rule.onNode(hasDialogText("Switch connection"), useUnmergedTree = true).performClick()

        waitForDialogText("The switch result is unconfirmed. Retrying uses the same request; it will not start another switch. response unconfirmed")
        rule.onNode(hasDialogText("Close"), useUnmergedTree = true).assertExists()
        rule.onNode(hasDialogText("Cancel"), useUnmergedTree = true).assertDoesNotExist()
        rule.runOnIdle {
            assertEquals(1, hub.submitted.size)
            assertEquals(2, hub.segmentReads)
            assertEquals(4L, hub.submitted.single().expectedEpoch)
            assertEquals("agy", hub.submitted.single().launcher)
            assertEquals("gemini-3", hub.submitted.single().model)
            assertEquals("high", hub.submitted.single().effort)
            assertEquals("acceptEdits", hub.submitted.single().permissionMode)
            assertEquals("box", hub.submitted.single().sourceMachineId)
            assertEquals("box", hub.submitted.single().machineId)
            assertTrue(hub.asked.isEmpty())
        }

        // Retrying sends the exact same operation_id and expected_epoch without re-reading /segments.
        rule.onNode(hasDialogText("Switch connection"), useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { switchedCount == 1 }
        rule.runOnIdle {
            assertEquals(2, hub.submitted.size)
            assertEquals(hub.submitted[0], hub.submitted[1])
            assertEquals(2, hub.segmentReads)
            assertEquals(listOf("Switch requested. You can continue in this conversation."), notices)
            assertTrue("ordinary setModelPick must never be called for an explicit switch", hub.asked.isEmpty())
        }
        rule.onNode(isDialog()).assertDoesNotExist()
    }

    @Test fun crossEngineSwitchDefinitiveRefusalKeepsCancelButtonAndShowsHubReason() {
        val hub = SwitchHub(submitBehavior = {
            throw persona.core.hub.HubFailure(
                persona.core.hub.HubError.Server,
                message = "Conversation is busy",
                status = 409,
                code = "conversation_busy",
                delivered = "none",
            )
        })
        val chat = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"main","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-opus-5-5","effort":"high","rev":3}""")
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick", "assistant_segments")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    Box { PersonaModelPill(chat, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = {}, onNotice = {}) }
                }
            }
        }
        rule.onNodeWithTag("model-pill").performClick()
        waitForDialogText("Gemini 3")
        rule.onNode(hasDialogText("Gemini 3"), useUnmergedTree = true).performClick()
        waitForDialogText("Switch connection")
        rule.onNode(hasDialogText("Switch connection"), useUnmergedTree = true).performClick()
        waitForDialogText("Conversation is busy")
        rule.onNode(hasDialogText("Cancel"), useUnmergedTree = true).assertExists()
        rule.onNode(hasDialogText("Close"), useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun supervisionRequiredConversationMarksIneligibleConnectionsUnavailable() {
        val hub = SwitchHub(segments = ConversationSwitchState(enabled = true, epoch = 2, supervisionRequired = true))
        val chat = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"leader-1","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-opus-5-5","effort":"high","rev":3}""")
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick", "assistant_segments")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    Box { PersonaModelPill(chat, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = {}, onNotice = {}) }
                }
            }
        }
        rule.onNodeWithTag("model-pill").performClick()
        waitForDialogText("This connection cannot receive separate task follow-up instructions.")
    }

    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    @Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
    @Test fun personaSwitchConfirmationRendersInLightAndDark() = switchConfirmScene(false)

    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    @Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi", fontScale = 2f)
    @Test fun personaSwitchConfirmationRendersAtLargestTextSize() = switchConfirmScene(true)

    private fun switchConfirmScene(largeText: Boolean) {
        org.robolectric.RuntimeEnvironment.setFontScale(if (largeText) 2f else 1f)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                PersonaSwitchConfirmation(
                    targetName = "Antigravity CLI",
                    error = null,
                    switching = false,
                    uncertain = false,
                    onDismiss = {},
                    onConfirm = {},
                )
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/persona-switch-shots").apply { mkdirs() }
        for (night in listOf(false, true)) {
            rule.runOnIdle { dark = night }
            rule.waitForIdle()
            rule.onNode(hasDialogText("Switch connection"), useUnmergedTree = true).assertExists()
            rule.onNode(hasDialogText("Cancel"), useUnmergedTree = true).assertExists()
            val bitmap = rule.onNode(isDialog() and hasAnyDescendant(hasText("Switch this conversation?")), useUnmergedTree = true)
                .captureToImage().asAndroidBitmap()
            File(out, "persona-switch-${if (largeText) "large" else "default"}-${if (night) "dark" else "light"}.png")
                .outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    @Test fun aHubPushThatBumpsTheRevisionReReadsTheEffectiveMainChatModel() {
        val hub = Hub()
        val first = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"main","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-haiku-5-5","effort":"high","rev":3}""")
        var shown by mutableStateOf(first)
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    key(shown.key, shown.rev) {
                        Box { PersonaModelPill(shown, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = {}, onNotice = {}) }
                    }
                }
            }
        }
        rule.waitUntil(5_000) { hub.launcherListReads >= 1 }
        val afterMount = hub.launcherListReads
        // the same revision re-renders nothing again (the page-life cache stands)
        rule.runOnUiThread { shown = first.copy() }
        rule.waitForIdle()
        assertEquals(afterMount, hub.launcherListReads)
        // a hub push with a new revision forces a fresh read so the pill matches the effective model (#6542)
        rule.runOnUiThread { shown = first.copy(rev = 4) }
        rule.waitForIdle()
        rule.waitUntil(5_000) { hub.launcherListReads > afterMount }
    }
}
