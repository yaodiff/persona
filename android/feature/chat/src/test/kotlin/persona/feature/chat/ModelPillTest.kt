package persona.feature.chat

import android.content.Context
import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.JsonCodec
import persona.core.contracts.ModelPick
import persona.core.contracts.PendingSwitch
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHubFeatures
import persona.core.designsystem.ModelPillWidth
import persona.core.designsystem.PersonaTheme
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

    @Test fun onlyTheUsersMessageOnGlassCarriesAWaitingSwitchOut() {
        val waiting = agent.copy(pendingSwitch = PendingSwitch("work"))
        assertTrue(switchesNext(waiting, on = true))
        assertFalse("Brief and Board show no switch: their messages leave it waiting", switchesNext(waiting, on = false))
        assertFalse("none waits", switchesNext(agent, on = true))
        assertFalse("its own launcher is no switch", switchesNext(agent.copy(pendingSwitch = PendingSwitch("example")), on = true))
        assertFalse(switchesNext(null, on = true))
    }

    @Test fun aSwitchDrawnAtOnceStandsUntilTheNextPush() {
        val drawn = DrawnSwitch(PendingSwitch("work", "glm-5.3"), rev = 7)
        assertEquals(PendingSwitch("work", "glm-5.3"), drawn.over(agent).pendingSwitch)
        assertNull("the next push says what the hub has", drawn.over(agent.copy(rev = 8)).pendingSwitch)
        assertNull("dropped at once", DrawnSwitch(null, 7).over(agent.copy(pendingSwitch = PendingSwitch("work"))).pendingSwitch)
        assertEquals(agent, (null as DrawnSwitch?).over(agent))
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

    /** Records what the pill asks the hub. */
    private class Hub : AssistantManagement {
        val asked = mutableListOf<String>()
        var launcherListReads = 0
            private set
        override suspend fun config() = AssistantConfig(machineId = "box", launcher = "claude", main = "main")
        override suspend fun assistantLauncherList(machineId: String): AssistantLaunchers {
            launcherListReads++
            return AssistantLaunchers(
                listOf(AgentLauncher("claude"), AgentLauncher("team", displayName = "Team API")), LauncherDefault("claude", "opus", "high"))
        }
        override suspend fun launcherModels(machineId: String, launcher: String) = when (launcher) {
            "claude" -> LauncherModels("claude", listOf(AgentModel("default", "Default", resolved = "claude-opus-5-5", isDefault = true),
                AgentModel("opus", "Opus 5.5", resolved = "claude-opus-5-5")))
            else -> LauncherModels("claude", listOf(AgentModel("sonnet", "Sonnet 4.5")))
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

    private fun hasDialogText(text: String) = hasText(text) and hasAnyAncestor(isDialog())

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
        // another chat with nothing picked keeps what it started with
        assertTrue(rule.onAllNodes(hasText("Same as when this chat started", substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
        rule.onNode(hasDialogText("Sonnet 4.5"), useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { hub.asked.isNotEmpty() }
        assertEquals(listOf("PUT main ModelPick(launcher=team, model=sonnet, effort=null)"), hub.asked)
        // drawn at once: the pill says the pick before the hub's push
        rule.waitUntil(5_000) { rule.onAllNodes(hasText("Sonnet 4.5", substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("model-pill").performClick()
        rule.waitUntil(5_000) { rule.onAllNodes(hasDialogText("Undo pick"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
        assertTrue(rule.onAllNodes(hasText("Picked for this chat · Team API · Sonnet 4.5"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
        rule.onNode(hasDialogText("Undo pick"), useUnmergedTree = true).performClick()
        rule.waitUntil(5_000) { hub.asked.size == 2 }
        assertEquals("DELETE main", hub.asked.last())
    }

    @Test fun aNonMainAssistantConversationDrawsNoPill() {
        val hub = Hub()
        val side = JsonCodec.decodeFromString<Session>("""{"machine_id":"box","session_id":"side","assistant":true,"agent":"claude","status":"idle",
            "launcher":"claude","model":"claude-opus-5-5","effort":"high","rev":3}""")
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf("assistant_model_pick")) {
                PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                    Box { PersonaModelPill(side, "box", hub, ModelPillWidth.Wide, pagePick = null, onPagePick = { }, onNotice = {}) }
                }
            }
        }
        rule.waitUntil(5_000) { hub.launcherListReads >= 1 } // the config is read, but the picker is never drawn
        rule.onNodeWithTag("model-pill").assertDoesNotExist()
        assertTrue("nothing is written for a conversation without a pick", hub.asked.isEmpty())
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
