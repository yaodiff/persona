package persona.feature.assistant

import androidx.activity.ComponentActivity
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.LocalManagerRoster
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.navigation.UsageCalendarRoute

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class UsageCalendarIntegrationTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val main = SessionKey("mac", "main")

    @Before fun setup() = AssistantManagerChoice.choose("hub", null, "assistant")
    @After fun cleanup() = AssistantManagerChoice.reset()

    private class Hub : AssistantRepository, MessageSender, MainConversationSender, UsageHistoryApi {
        override val assistant = MutableStateFlow(AssistantSnapshot(mainSession = SessionKey("mac", "main"), conversations = listOf(
            Session(machineId = "mac", sessionId = "main", title = "Main", assistant = true),
        )))
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun refresh() = Unit
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        override suspend fun usageDays(days: Int) = UsageDays(zone = "Asia/Shanghai", days = listOf(
            UsageDay(date = "2026-11-17", input = 10, cacheWrite = 20, cacheRead = 40, output = 80),
        ))
        override suspend fun usageHours(days: Int) = UsageHours(zone = "Asia/Shanghai")
        override suspend fun usageProjects(): UsageProjects? = null
        override suspend fun usageProjectDetail(key: String): UsageProjectDetail = error("not used")
    }

    @Test fun theRealHomeOpensTheUsageDetailsRoute() {
        val keys = mutableListOf<NavKey>()
        val navigator = object : Navigator {
            override fun navigate(key: NavKey) { keys += key }
            override fun back() = Unit
            override fun selectTab(tab: AppTab) = Unit
        }
        val hub = Hub()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides ManagerRoster(listOf(
                    HubManager("assistant", "Helper", online = true, isDefault = true),
                ), "assistant")) {
                    AssistantScreen(navigator, hub, hub, { SessionKey("mac", "new") }, composerService = null,
                        mainConversationSender = hub, usageHistory = hub, chatContent = { Text("chat:${it.sessionId}") })
                }
            }
        }

        rule.onNodeWithContentDescription("New chat").performClick()
        rule.onNodeWithTag("assistant-usage-calendar").assertExists()
        rule.onNodeWithTag("usage-calendar-open").performClick()
        assertEquals(UsageCalendarRoute, keys.last())
    }
}
