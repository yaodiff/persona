package persona.feature.agents

import androidx.activity.ComponentActivity
import androidx.compose.runtime.DisposableEffect
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.preferencesOf
import androidx.navigation3.runtime.NavKey
import androidx.lifecycle.viewModelScope
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.cancel
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** Exercises the actual Agents menu, confirmation, archive page and restore action without a device. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class AgentArchivesFlowTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    @Test fun claudeCanBeArchivedAndRestoredFromAgents() = archiveAndRestore("claude")

    @Test fun codexCanBeArchivedAndRestoredFromAgents() = archiveAndRestore("codex")

    /** #8143: a Paseo provider's agent (Grok's) archives and restores the same way, as the manager takes it and the web offers it. */
    @Test fun aPaseoProvidersAgentCanBeArchivedAndRestoredFromAgents() = archiveAndRestore("paseo", ArchiveFake(listOf(
        Session("computer", "paseo-agent-1", title = "Grok history", agent = "paseo", agentLabelHint = "Grok", launcher = "grok",
            sourceKind = "runtime", spaceKind = "paseo", status = "offline", state = "offline", cwd = "/projects/albums"))))

    private fun archiveAndRestore(agent: String, api: ArchiveFake = ArchiveFake()) {
        val session = api.source.single { it.agent == agent }
        val repository = object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(session))
            override val machines = MutableStateFlow(listOf(Machine("computer", "Development computer", online = true)))
            override suspend fun refresh() {
                sessions.value = listOf(session).filter { source -> api.rows.none { it.key == source.key } }
            }
        }
        val stored = MutableStateFlow(preferencesOf(agentRangeKey("") to AgentRange.All.name))
        val model = AgentsViewModel(repository, object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }, object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }, object : MessageSender {
            override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }, AgentsPreferences(object : DataStore<Preferences> {
            override val data = stored
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
                transform(stored.value).also { stored.value = it }
        }, object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot())
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        }), archiveService = api)
        rule.setContent {
            DisposableEffect(model) { onDispose { model.viewModelScope.cancel() } }
            PersonaTheme(darkTheme = false, reduceMotion = true) { AgentsScreen(model, Nowhere, AgentsMetadata()) }
        }
        rule.waitUntil(5_000) { model.page.value.loaded }
        rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(session.displayTitle))
        rule.onNodeWithText(session.displayTitle).performTouchInput { longClick() }
        rule.onNode(hasText("Archive session") and hasClickAction()).performClick()
        rule.onNodeWithText("Archive “${session.displayTitle}”?", substring = true).assertIsDisplayed()
        rule.onNode(hasText("Archive session") and hasClickAction()).performClick()
        rule.waitUntil(5_000) {
            api.rows.any { it.key == session.key } && model.page.value.sessions.none { it.key == session.key }
        }
        rule.onNodeWithText(session.displayTitle).assertDoesNotExist()

        // Restore must reveal offline history even if the user switched to Running after archiving it.
        rule.onNodeWithContentDescription("List settings").performClick()
        rule.onNodeWithText("Range: Running").performScrollTo().performClick()
        rule.waitUntil(5_000) { model.page.value.preferences.range == AgentRange.Running }
        rule.onNodeWithContentDescription("List settings").performClick()
        rule.onNodeWithText("Archived sessions").performClick()
        rule.onNodeWithText(session.displayTitle).assertIsDisplayed()
        rule.onNodeWithTag("restore:${session.key.path}").assertIsEnabled().performClick()
        rule.waitUntil(5_000) {
            api.rows.isEmpty() && stored.value[agentRangeKey("")] == AgentRange.All.name
        }
        rule.onNodeWithText("No archived sessions").assertIsDisplayed()
        rule.onNodeWithContentDescription("Back").performClick()
        rule.waitUntil(5_000) {
            model.page.value.preferences.range == AgentRange.All &&
                model.page.value.list.groups.any { group -> group.visibleRows.any { it.session.key == session.key } }
        }
        rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(session.displayTitle))
        rule.onNodeWithText(session.displayTitle).assertIsDisplayed()
        rule.runOnIdle {
            assertEquals(2, api.writes)
            assertEquals(AgentRange.All.name, stored.value[agentRangeKey("")])
            assertTrue(repository.sessions.value.any { it.key == session.key })
        }
    }

    private object Nowhere : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
}
