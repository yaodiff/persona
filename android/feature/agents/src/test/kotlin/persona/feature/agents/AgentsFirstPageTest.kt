package persona.feature.agents

import androidx.activity.ComponentActivity
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.assertContentDescriptionEquals
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertHasNoClickAction
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.HomeChrome
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * Opening the Agents tab (#5377): the sessions and the local preferences arrive asynchronously, and until the first
 * page is built the list must not claim there are no computers or sessions; the first built page has them.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class AgentsFirstPageTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    /** Local preferences that have not been read yet: null until [stored] gets a value. */
    private val stored = MutableStateFlow<Preferences?>(null)

    @Test fun firstPagePlaceholdersFollowTheControlsAndRefreshKeepsSessions() {
        val rows = MutableStateFlow(listOf(Session("box", "s1", title = "Example task", state = "working")))
        val finishRefresh = CompletableDeferred<Unit>()
        val model = model(object : SessionsRepository {
            override val sessions = rows
            override val machines = MutableStateFlow(listOf(Machine("box", "Example computer", online = true)))
            override suspend fun refresh() = finishRefresh.await()
        })
        showPage(model)
        rule.waitForIdle()
        assertFalse(model.page.value.loaded)
        val placeholders = rule.onNodeWithTag("agents-initial-placeholder")
        placeholders.assertIsDisplayed().assertHasNoClickAction().assertContentDescriptionEquals("Loading")
        rule.onAllNodesWithContentDescription("Loading").assertCountEquals(1)
        val placeholdersTop = placeholders.fetchSemanticsNode().boundsInRoot.top
        for ((label, control) in listOf("All" to rule.onNodeWithTag("agents-status-0"),
            "Grouping" to rule.onNodeWithTag("agents-grouping"))) {
            control.assertIsDisplayed()
            assertTrue("placeholders appear below \"$label\"", control.fetchSemanticsNode().boundsInRoot.bottom <= placeholdersTop)
        }
        listOf("No computers yet", "Start your first coding session").forEach {
            assertTrue("no \"$it\" before the first page",
                rule.onAllNodesWithText(it, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        }

        rule.runOnIdle { stored.value = emptyPreferences() }
        rule.waitUntil(5_000) { model.page.value.loaded }
        val first = model.page.value
        assertEquals(listOf("s1"), first.sessions.map { it.sessionId })
        rule.waitForIdle()
        fun shown(text: String) = rule.onAllNodesWithText(text, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
        assertFalse(shown("No computers yet"))
        rule.onNodeWithText("Example task").assertIsDisplayed()
        placeholders.assertDoesNotExist()

        rule.runOnIdle { model.refresh() }
        try {
            rule.waitUntil(5_000) { model.refreshing.value }
            rule.onNodeWithText("Example task").assertIsDisplayed()
            placeholders.assertDoesNotExist()
        } finally {
            finishRefresh.complete(Unit)
        }
        rule.waitUntil(5_000) { !model.refreshing.value }

        rule.runOnIdle { rows.value = emptyList() }
        rule.waitForIdle()
        assertTrue("a loaded empty page keeps its empty state", shown("Start your first coding session"))
        placeholders.assertDoesNotExist()
    }

    @Test fun cancelledInitialFilterStillShowsSessionsWhenTheRepositoryLoads() {
        val filterStarted = CompletableDeferred<Unit>()
        val filterResponse = CompletableDeferred<Unit>()
        val filterFinished = CompletableDeferred<Unit>()
        val rows = MutableStateFlow(emptyList<Session>())
        val status = MutableStateFlow(SessionListStatus(loading = true))
        val model = model(object : SessionsRepository {
            override val sessions = rows
            override val machines = MutableStateFlow(listOf(Machine("box", "Example computer", online = true)))
            override val listStatus = status
            override suspend fun refresh() = Unit
            override suspend fun setListFilter(filter: SessionListFilter) {
                filterStarted.complete(Unit)
                try {
                    filterResponse.await()
                } finally {
                    filterFinished.complete(Unit)
                }
            }
        })
        stored.value = emptyPreferences()
        showPage(model)

        val placeholders = rule.onNodeWithTag("agents-initial-placeholder")
        try {
            rule.waitUntil(5_000) { filterStarted.isCompleted }
            placeholders.assertIsDisplayed()
        } finally {
            // A startup Hello supersedes the repository request while the Agents screen stays subscribed.
            filterResponse.cancel()
        }
        rule.waitUntil(5_000) { filterFinished.isCompleted }
        rule.runOnIdle {
            rows.value = listOf(Session("box", "s1", title = "Recovered task", state = "working"))
            status.value = SessionListStatus(loaded = true, total = 1)
        }

        rule.waitUntil(5_000) { model.page.value.loaded }
        rule.onNodeWithText("Recovered task").assertIsDisplayed()
        placeholders.assertDoesNotExist()
    }

    private fun showPage(model: AgentsViewModel) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalHomeChrome provides HomeChrome(emptyList(), "Agents", onSelect = {}, leading = {})) {
                    AgentsScreen(model, Nowhere, AgentsMetadata())
                }
            }
        }
    }

    private fun model(repository: SessionsRepository) = AgentsViewModel(repository, object : InteractionCenter {
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
        override val data = stored.filterNotNull()
        override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
            transform(stored.value ?: emptyPreferences()).also { stored.value = it }
    }, object : HubPreferences {
        override val preferences = MutableStateFlow(HubPreferencesSnapshot())
        override suspend fun refresh() = Unit
        override suspend fun togglePin(path: String, space: Boolean) = Unit
        override suspend fun toggleMute(path: String, space: Boolean) = Unit
        override suspend fun markSeen(session: SessionKey, version: String) = Unit
    }))

    private object Nowhere : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
}
