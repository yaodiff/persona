package persona.feature.assistant

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.click
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Direction
import persona.core.contracts.DirectionTree
import persona.core.contracts.ExternalDirection
import persona.core.contracts.HubScope
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.AssistantNotes
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.DraftKey
import persona.core.contracts.api.DraftRepository
import persona.core.contracts.api.DraftSnapshot
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.MemoryFile
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.Run
import persona.core.contracts.api.ScheduledTask
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AssistantRoute
import persona.core.navigation.TodoDetailRoute

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1500dp")
class AssistantSidebarContentTest {
    @get:Rule val rule = createComposeRule()
    private val operations = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    @After fun close() { operations.cancel() }

    @Test fun historyAndSettingsWorkWithoutRenderingTheAssistantPage() {
        val choice = AssistantManagerChoice.current.value
        val chat = Session(machineId = "example", sessionId = "saved", title = "Saved conversation", assistant = true)
        val initial = AssistantSnapshot(conversations = listOf(chat), manager = choice.manager, managerId = choice.shown)
        var snapshot by mutableStateOf(initial)
        val repository = object : AssistantRepository {
            override val assistant = MutableStateFlow(initial)
            override suspend fun refresh() = Unit
        }
        val hidden = listOf(AssistantRoute, TodoDetailRoute("unsaved-draft"))
        var settings = false
        var opened: SessionKey? = null
        rule.setContent {
            PersonaTheme(style = AppStyle.Brief, reduceMotion = true) {
                Box(Modifier.width(400.dp).height(1200.dp)) {
                    AssistantSidebarContent(snapshot, repository, null, operations, demo = true,
                        onOpen = { opened = it }, onAction = {}, onSettings = { settings = true },
                        onInbox = {}, onKnowledge = {}, onTodos = {}, todoCount = 2, showTodos = true,
                        inboxCount = 3, settingsCount = 0, settingsDot = false, settingsBadge = null,
                        pages = emptyList(), onPage = {}, onRetired = {})
                }
            }
        }
        // The user's own newest chats are listed under Recent (#4922); every chat but the main one and the leaders also
        // waits under one All chats row (#4331).
        rule.onNodeWithText("最近").assertExists()
        rule.onNodeWithText("Saved conversation").assertExists()
        rule.onNodeWithText("全部对话（1）").performClick()
        rule.onAllNodesWithText("Saved conversation").assertCountEquals(2)
        rule.onNodeWithContentDescription("设置").performClick()
        assertTrue(settings)
        assertEquals(listOf(AssistantRoute, TodoDetailRoute("unsaved-draft")), hidden)
        rule.onAllNodesWithText("Saved conversation")[0].performClick()
        assertEquals(chat.key, opened)
        snapshot = initial.copy(conversations = listOf(chat.copy(title = "Fresh conversation")))
        rule.waitForIdle()
        rule.onAllNodesWithText("Fresh conversation").assertCountEquals(2)
        rule.onNodeWithText("Saved conversation").assertDoesNotExist()
    }

    @Test fun fullWindowSearchExcludesArchivedOpensPicksAndCloses() {
        val choice = AssistantManagerChoice.current.value
        val trip = Session(machineId = "hub", sessionId = "trip", title = "Weekend trip",
            updatedAt = "2026-11-16T12:00:00Z", assistant = true)
        val report = Session(machineId = "hub", sessionId = "report", title = "Weekly report",
            updatedAt = "2026-11-15T12:00:00Z", assistant = true)
        val archived = Session(machineId = "hub", sessionId = "arch", title = "Archived trip",
            archived = true, updatedAt = "2026-11-17T12:00:00Z", assistant = true)
        val snapshot = AssistantSnapshot(
            conversations = listOf(trip, report),
            manager = choice.manager, managerId = choice.shown,
        )
        val repository = object : AssistantRepository {
            override val assistant = MutableStateFlow(snapshot)
            override suspend fun refresh() = Unit
        }
        val management = testManagement(archived = listOf(archived))
        val bindings = testBindings(management, repository)
        var opened: SessionKey? = null
        rule.setContent {
            PersonaTheme(style = AppStyle.Brief, reduceMotion = true) {
                CompositionLocalProvider(LocalAssistantHost provides bindings) {
                    Box(Modifier.width(400.dp).height(1200.dp)) {
                        AssistantSidebarContent(
                            snapshot, repository, null, operations, demo = false,
                            onOpen = { opened = it }, onAction = {}, onSettings = {},
                            onInbox = {}, onKnowledge = {}, onTodos = {}, todoCount = 0, showTodos = false,
                            inboxCount = 0, settingsCount = 0, settingsDot = false, settingsBadge = null,
                            pages = emptyList(), onPage = {}, onRetired = {},
                            fullWindowSearch = true,
                        )
                    }
                }
            }
        }
        rule.waitForIdle()
        // Regular window hides the inline field and opens full search from the Search control.
        rule.onAllNodesWithText("搜索对话").assertCountEquals(0)
        rule.onNodeWithContentDescription("搜索对话").performClick()
        rule.onNodeWithTag("full-search").assertExists()
        rule.onNode(hasText("Weekend trip") and hasAnyAncestor(hasTestTag("full-search"))).assertExists()
        rule.onNode(hasText("Weekly report") and hasAnyAncestor(hasTestTag("full-search"))).assertExists()
        rule.onAllNodesWithText("Archived trip").assertCountEquals(0)

        // Picking a row opens that conversation and dismisses full search.
        rule.onNode(hasText("Weekend trip") and hasAnyAncestor(hasTestTag("full-search"))).performClick()
        assertEquals(trip.key, opened)
        rule.onAllNodesWithTag("full-search").assertCountEquals(0)

        // Close button dismisses full search.
        rule.onNodeWithContentDescription("搜索对话").performClick()
        rule.onNodeWithTag("full-search").assertExists()
        rule.onNodeWithTag("full-search-close").performClick()
        rule.onAllNodesWithTag("full-search").assertCountEquals(0)
    }

    @Test fun fullWindowSearchListsOutsideDirectionsAndRoutesOnlyCompleteTargets() {
        val choice = AssistantManagerChoice.current.value
        val target = SessionKey("work", "outside-agent")
        val tree = DirectionTree(listOf(
            Direction("bound", "Release outside", external = ExternalDirection(target.machineId, target.sessionId)),
            Direction("targetless", "Research outside", external = ExternalDirection()),
        ))
        val snapshot = AssistantSnapshot(
            leaders = tree.ids, directionNames = tree.names, directionTree = tree,
            manager = choice.manager, managerId = choice.shown,
        )
        val repository = object : AssistantRepository {
            override val assistant = MutableStateFlow(snapshot)
            override suspend fun refresh() = Unit
        }
        val openedChats = mutableListOf<SessionKey>()
        val openedAgents = mutableListOf<SessionKey>()
        rule.setContent {
            PersonaTheme(style = AppStyle.Brief, reduceMotion = true) {
                Box(Modifier.width(400.dp).height(1200.dp)) {
                    AssistantSidebarContent(
                        snapshot, repository, null, operations, demo = true,
                        onOpen = { openedChats += it }, onAction = {}, onSettings = {},
                        onInbox = {}, onKnowledge = {}, onTodos = {}, todoCount = 0, showTodos = false,
                        inboxCount = 0, settingsCount = 0, settingsDot = false, settingsBadge = null,
                        pages = emptyList(), onPage = {}, onRetired = {},
                        fullWindowSearch = true, onOpenAgent = { openedAgents += it },
                    )
                }
            }
        }
        rule.onNodeWithContentDescription("搜索对话").performClick()
        rule.onNode(hasSetTextAction() and hasAnyAncestor(hasTestTag("full-search-field"))).performTextInput("outside")
        val bound = rule.onNode(hasText("Release outside") and hasAnyAncestor(hasTestTag("full-search")))
        val targetless = rule.onNode(hasText("Research outside") and hasAnyAncestor(hasTestTag("full-search")))
        bound.assertExists()
        targetless.assertExists()
        targetless.performTouchInput { click() }
        assertTrue(openedChats.isEmpty())
        assertTrue(openedAgents.isEmpty())
        rule.onNodeWithTag("full-search").assertExists()
        bound.performClick()
        assertEquals(listOf(target), openedAgents)
        assertTrue(openedChats.isEmpty())
        rule.onAllNodesWithTag("full-search").assertCountEquals(0)
    }

    @Test fun foldingClosesFullSearchAndUnfoldingClearsStaleInlineQuery() {
        val choice = AssistantManagerChoice.current.value
        val trip = Session(machineId = "hub", sessionId = "trip", title = "Weekend trip",
            updatedAt = "2026-11-16T12:00:00Z", assistant = true)
        val report = Session(machineId = "hub", sessionId = "report", title = "Weekly report",
            updatedAt = "2026-11-15T12:00:00Z", assistant = true)
        val snapshot = AssistantSnapshot(
            conversations = listOf(trip, report),
            manager = choice.manager, managerId = choice.shown,
        )
        val repository = object : AssistantRepository {
            override val assistant = MutableStateFlow(snapshot)
            override suspend fun refresh() = Unit
        }
        var fullWindow by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = AppStyle.Brief, reduceMotion = true) {
                Box(Modifier.width(400.dp).height(1200.dp)) {
                    AssistantSidebarContent(
                        snapshot, repository, null, operations, demo = true,
                        onOpen = {}, onAction = {}, onSettings = {},
                        onInbox = {}, onKnowledge = {}, onTodos = {}, todoCount = 0, showTodos = false,
                        inboxCount = 0, settingsCount = 0, settingsDot = false, settingsBadge = null,
                        pages = emptyList(), onPage = {}, onRetired = {},
                        fullWindowSearch = fullWindow,
                    )
                }
            }
        }
        // Compact: type into the inline field so only Weekend trip matches.
        rule.onNodeWithText("搜索对话").performTextInput("Weekend")
        rule.onNodeWithText("Weekend trip").assertExists()
        rule.onNodeWithText("Weekly report").assertDoesNotExist()

        // Unfolding clears the stale query so the regular sidebar is not left filtered with a hidden field.
        fullWindow = true
        rule.waitForIdle()
        rule.onNodeWithText("Weekly report").assertExists()
        rule.onAllNodesWithText("搜索对话").assertCountEquals(0)

        // Open full search while unfolded, then fold: full search closes and stays closed on re-unfolding.
        rule.onNodeWithContentDescription("搜索对话").performClick()
        rule.onNodeWithTag("full-search").assertExists()
        fullWindow = false
        rule.waitForIdle()
        rule.onAllNodesWithTag("full-search").assertCountEquals(0)
        fullWindow = true
        rule.waitForIdle()
        rule.onAllNodesWithTag("full-search").assertCountEquals(0)
    }

    @Test fun foldColumnButtonShowsOnlyWhenProvidedAndInvokesCallback() {
        val choice = AssistantManagerChoice.current.value
        val snapshot = AssistantSnapshot(conversations = emptyList(), manager = choice.manager, managerId = choice.shown)
        val repository = object : AssistantRepository {
            override val assistant = MutableStateFlow(snapshot)
            override suspend fun refresh() = Unit
        }
        var canFold by mutableStateOf(true)
        var folds = 0
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                Box(Modifier.width(340.dp).height(800.dp)) {
                    AssistantSidebarContent(
                        snapshot, repository, null, operations, demo = true,
                        onOpen = {}, onAction = {}, onSettings = {},
                        onInbox = {}, onKnowledge = {}, onTodos = {}, todoCount = 0, showTodos = false,
                        inboxCount = 0, settingsCount = 0, settingsDot = false, settingsBadge = null,
                        pages = emptyList(), onPage = {}, onRetired = {},
                        fullWindowSearch = true,
                        onFoldColumn = if (canFold) ({ folds++ }) else null,
                    )
                }
            }
        }
        rule.onNodeWithContentDescription("隐藏侧边栏").performClick()
        assertEquals(1, folds)

        canFold = false
        rule.waitForIdle()
        rule.onAllNodesWithContentDescription("隐藏侧边栏").assertCountEquals(0)
    }

    @Test fun archivingDoesNotRetireTheOpenConversationButEndingDoes() {
        val choice = AssistantManagerChoice.current.value
        val main = Session(machineId = "hub", sessionId = "main", title = "主对话", assistant = true)
        val first = Session(machineId = "hub", sessionId = "first", title = "First conversation", assistant = true)
        val second = Session(machineId = "hub", sessionId = "second", title = "Second conversation", assistant = true)
        var snapshot by mutableStateOf(AssistantSnapshot(
            mainSession = main.key,
            conversations = listOf(main, first, second),
            manager = choice.manager,
            managerId = choice.shown,
        ))
        val archived = mutableListOf<SessionKey>()
        val ended = mutableListOf<SessionKey>()
        val retired = mutableListOf<SessionKey>()
        val management = object : AssistantManagement by testManagement(emptyList()) {
            override fun forManager(manager: String?): AssistantManagement = this
            override suspend fun archiveConversation(session: SessionKey) { archived += session }
            override suspend fun endConversation(session: SessionKey) { ended += session }
            override suspend fun archivedConversations(): List<Session> =
                listOf(first, second).filter { it.key in archived }.map { it.copy(archived = true) }
        }
        val repository = object : AssistantRepository {
            override val assistant = MutableStateFlow(snapshot)
            override suspend fun refresh() {
                val next = snapshot.copy(conversations = snapshot.conversations.filter { it.key !in archived })
                snapshot = next
                assistant.value = next
            }
        }
        var active by mutableStateOf(first.key)
        rule.setContent {
            PersonaTheme(style = AppStyle.Brief, reduceMotion = true) {
                CompositionLocalProvider(LocalAssistantHost provides testBindings(management, repository)) {
                    Box(Modifier.width(400.dp).height(1200.dp)) {
                        AssistantSidebarContent(
                            snapshot, repository, active, operations, demo = false,
                            onOpen = {}, onAction = {}, onSettings = {},
                            onInbox = {}, onKnowledge = {}, onTodos = {}, todoCount = 0, showTodos = false,
                            inboxCount = 0, settingsCount = 0, settingsDot = false, settingsBadge = null,
                            pages = emptyList(), onPage = {}, onRetired = { retired += it },
                        )
                    }
                }
            }
        }
        rule.onAllNodesWithContentDescription("对话操作")[1].performClick()
        rule.onNodeWithText("归档").performClick()
        rule.waitUntil(5_000) { archived == listOf(first.key) }
        rule.waitForIdle()
        assertEquals(listOf(first.key), archived)
        assertTrue(retired.isEmpty())
        rule.onAllNodesWithText(first.title!!).assertCountEquals(0)
        rule.onNodeWithText("已归档（1）").assertExists()

        active = second.key
        rule.waitForIdle()
        rule.onAllNodesWithContentDescription("对话操作")[1].performClick()
        rule.onNodeWithText("结束…").performClick()
        rule.onNodeWithText("结束对话").performClick()
        rule.waitUntil(5_000) { ended == listOf(second.key) && retired == listOf(second.key) }
        rule.waitForIdle()
        assertEquals(listOf(second.key), ended)
        assertEquals(listOf(second.key), retired)
    }

    // The shell draws this sidebar outside AssistantAppHost, so LocalAssistantHost is null there. The management passed
    // in must keep the conversation menu's Pin/Rename/Archive/End actions instead of falling back to the two copies
    // (#17123).
    @Test fun theConversationMenuKeepsManagementActionsWithoutAnAssistantHost() {
        val choice = AssistantManagerChoice.current.value
        val main = Session(machineId = "hub", sessionId = "main", title = "主对话", assistant = true)
        val first = Session(machineId = "hub", sessionId = "first", title = "First conversation", assistant = true)
        val snapshot = AssistantSnapshot(mainSession = main.key, conversations = listOf(main, first),
            manager = choice.manager, managerId = choice.shown)
        val repository = object : AssistantRepository {
            override val assistant = MutableStateFlow(snapshot)
            override suspend fun refresh() = Unit
        }
        val management = object : AssistantManagement by testManagement(emptyList()) {
            override fun forManager(manager: String?): AssistantManagement = this
        }
        rule.setContent {
            PersonaTheme(style = AppStyle.Brief, reduceMotion = true) {
                Box(Modifier.width(400.dp).height(1200.dp)) {
                    AssistantSidebarContent(snapshot, repository, first.key, operations, demo = false,
                        onOpen = {}, onAction = {}, onSettings = {}, onInbox = {}, onKnowledge = {}, onTodos = {},
                        todoCount = 0, showTodos = false, inboxCount = 0, settingsCount = 0, settingsDot = false,
                        settingsBadge = null, pages = emptyList(), onPage = {}, onRetired = {},
                        management = management)
                }
            }
        }
        rule.onAllNodesWithContentDescription("对话操作")[1].performClick()
        rule.onNodeWithText("重命名…").assertExists()
        rule.onNodeWithText("归档").assertExists()
        rule.onNodeWithText("拷贝会话 ID").assertExists()
    }

    private fun testManagement(archived: List<Session>) = object : AssistantManagement {
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = Unit
        override suspend fun archiveConversation(session: SessionKey) = Unit
        override suspend fun unarchiveConversation(session: SessionKey) = Unit
        override suspend fun archivedConversations(): List<Session> = archived
        override suspend fun renameConversation(session: SessionKey, title: String) = Unit
        override suspend fun tasks(): List<ScheduledTask> = emptyList()
        override suspend fun saveTask(task: ScheduledTask): ScheduledTask = task
        override suspend fun deleteTask(id: String) = Unit
        override suspend fun runTask(id: String): SessionKey = SessionKey("hub", "task")
        override suspend fun freshTask(id: String): ScheduledTask = error("Unexpected")
        override suspend fun runs(): List<Run> = emptyList()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = Unit
        override suspend fun memory(): List<MemoryFile> = emptyList()
        override suspend fun saveMemory(name: String, content: String) = Unit
        override suspend fun deleteMemory(name: String) = Unit
    }

    private fun testBindings(management: AssistantManagement, conversations: AssistantRepository): AssistantHostBindings {
        val drafts = object : DraftRepository {
            override fun observe(key: DraftKey): Flow<DraftSnapshot> =
                flowOf(DraftSnapshot(HubScope("hub", "gen"), key))
            override suspend fun read(key: DraftKey): DraftSnapshot =
                DraftSnapshot(HubScope("hub", "gen"), key)
            override suspend fun save(key: DraftKey, text: String, expectedVersion: Long): DraftSnapshot =
                DraftSnapshot(HubScope("hub", "gen"), key, text, expectedVersion + 1)
        }
        val mainSender = object : MainConversationSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
        }
        return AssistantHostBindings(
            identity = "test", management = management, drafts = drafts, coroutineScope = operations,
            phoneToolsAvailable = false, machines = emptyList(), conversations = conversations, mainSender = mainSender,
            chatContent = { _, _ -> },
        )
    }
}
