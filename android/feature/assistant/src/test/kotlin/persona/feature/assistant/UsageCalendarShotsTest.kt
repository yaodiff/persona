package persona.feature.assistant

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.time.LocalDate

/** Real-page before/after captures; images are review artifacts, never repository fixtures. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class UsageCalendarShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val main = SessionKey("mac", "main")
    private val out get() = File(System.getProperty("persona.shots") ?: "build/usage-calendar-shots").also { it.mkdirs() }

    @Before fun setup() = AssistantManagerChoice.choose("hub", null, "assistant")
    @After fun cleanup() = AssistantManagerChoice.reset()

    private class Hub(val history: UsageDays, val hourly: UsageHours) : AssistantRepository, MessageSender, MainConversationSender, UsageHistoryApi {
        override val assistant = MutableStateFlow(AssistantSnapshot(mainSession = SessionKey("mac", "main"), conversations = listOf(
            Session(machineId = "mac", sessionId = "main", title = "Main", assistant = true),
        )))
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        var dayReads = 0
        var hourReads = 0
        override suspend fun refresh() = Unit
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        override suspend fun usageDays(days: Int): UsageDays { dayReads++; return history }
        override suspend fun usageHours(days: Int): UsageHours { hourReads++; return hourly }
        override suspend fun usageProjects(): UsageProjects? = null
        override suspend fun usageProjectDetail(key: String): UsageProjectDetail = error("not used")
    }

    private fun hub(): Hub {
        val end = LocalDate.of(2026, 11, 17)
        val days = (0 until 371).map { offset ->
            val date = end.minusDays((370 - offset).toLong())
            val input = if (offset % 17 == 0) 50_000L else (offset % 9 * 700).toLong()
            UsageDay(date.toString(), input = input, cacheWrite = input / 3, cacheRead = input * 2, output = input / 5)
        }
        val hours = (0 until 7 * 24).map { index ->
            val date = end.minusDays((6 - index / 24).toLong())
            val input = (index % 11 * 400).toLong()
            persona.core.contracts.api.UsageHour(t = 1_760_000_000L + index * 3600, date = date.toString(), hour = index % 24,
                input = input, cacheWrite = input / 2, cacheRead = input * 2, output = input / 4)
        }
        return Hub(
            UsageDays("Asia/Shanghai", days, stored = true),
            UsageHours("Asia/Shanghai", hours,
                machines = listOf(
                    UsageMachineShare("mac", "MacBook Pro", input = 50_000, cacheWrite = 12_000, cacheRead = 90_000, output = 9_000),
                    UsageMachineShare("old", "", input = 12_000, cacheWrite = 3_000, cacheRead = 20_000, output = 2_000),
                ),
                agents = listOf(
                    UsageAgentShare("claude", input = 30_000, cacheWrite = 7_000, cacheRead = 50_000, output = 5_000),
                    UsageAgentShare("codex", input = 20_000, cacheWrite = 5_000, cacheRead = 40_000, output = 4_000),
                    UsageAgentShare("", input = 7_000, cacheWrite = 2_000, cacheRead = 14_000, output = 1_000),
                    UsageAgentShare("studio-runtime", input = 5_000, cacheWrite = 1_000, cacheRead = 6_000, output = 1_000),
                ),
                total = UsageTokens(input = 62_000, cacheWrite = 15_000, cacheRead = 110_000, output = 11_000), stored = true),
        )
    }

    @Test fun phoneHomeAndDetailsInEveryStyleThemeAndTextSize() {
        val hub = hub()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var large by mutableStateOf(false)
        var includeUsage by mutableStateOf(false)
        var showDetails by mutableStateOf(false)
        val detailsState = LazyListState()
        val navigator = object : Navigator {
            override fun navigate(key: NavKey) { showDetails = true }
            override fun back() { showDetails = false }
            override fun selectTab(tab: AppTab) = Unit
        }
        rule.setContent {
            val base = LocalConfiguration.current
            val configuration = Configuration(base).apply {
                fontScale = if (large) 2f else 1f
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(
                    LocalConfiguration provides configuration,
                    LocalDensity provides Density(LocalDensity.current.density, if (large) 2f else 1f),
                    LocalManagerRoster provides ManagerRoster(listOf(HubManager("assistant", "Helper", online = true, isDefault = true)), "assistant"),
                ) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1000.dp).background(PersonaTheme.palette.background)) {
                        if (showDetails) UsageCalendarPage(hub, onBack = { showDetails = false }, listState = detailsState)
                        else AssistantScreen(navigator, hub, hub, { SessionKey("mac", "new") }, composerService = null,
                            mainConversationSender = hub, usageHistory = if (includeUsage) hub else null,
                            chatContent = { Text("chat:${it.sessionId}") })
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (big in listOf(false, true)) {
            style = s; dark = d; large = big; showDetails = false; includeUsage = false
            rule.waitForIdle()
            rule.onNodeWithContentDescription("New chat").performClick()
            rule.onNodeWithTag("assistant-new-chat-grid").performScrollToNode(hasText("Online devices"))
            val prefix = "phone-${s.name.lowercase()}-${if (d) "dark" else "light"}-${if (big) "large" else "default"}"
            if (!big) capture("$prefix-before")
            includeUsage = true
            waitForTag("assistant-usage-calendar")
            rule.onNodeWithTag("assistant-new-chat-grid").performScrollToNode(hasTestTag("assistant-usage-calendar"))
            capture("$prefix-home")
            rule.onNodeWithTag("usage-day-2026-11-16").performClick()
            if (!big) capture("$prefix-selected")
            rule.onNodeWithTag("usage-card-summary", useUnmergedTree = true).performClick()
            waitForText("Token usage")
            capture("$prefix-details-top")
            rule.runOnIdle { detailsState.requestScrollToItem(5 + hub.hourly.machines.size) }
            rule.onNodeWithContentDescription("Claude Code, 92,000 tokens").assertExists()
            capture("$prefix-details-agents")
            rule.runOnIdle { detailsState.dispatchRawDelta(100_000f) }
            capture("$prefix-details-bottom")
        }
    }

    @Test
    @Config(sdk = [35], qualifiers = "en-w840dp-h1100dp-xhdpi")
    fun tabletCardsInEveryStyleAndTheme() {
        val hub = hub()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var showDetails by mutableStateOf(false)
        val detailsState = LazyListState()
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(840.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                    if (showDetails) UsageCalendarPage(hub, onBack = { showDetails = false }, listState = detailsState)
                    else UsageCalendarCard(hub.history, onOpen = { showDetails = true })
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; showDetails = false
            val prefix = "tablet-${s.name.lowercase()}-${if (d) "dark" else "light"}"
            waitForTag("assistant-usage-calendar")
            capture("$prefix-home")
            showDetails = true
            waitForText("Token usage")
            capture("$prefix-details-top")
            rule.runOnIdle { detailsState.dispatchRawDelta(100_000f) }
            capture("$prefix-details-bottom")
        }
    }

    @Test
    @Config(sdk = [35], qualifiers = "zh-w400dp-h1000dp-xhdpi")
    fun chineseHomeAndDetails() = languageShots("zh", "今天", "Token 用量", "按 Agent · 最近 7 天", "未区分（更新前）", "9.2万")

    @Test
    @Config(sdk = [35], qualifiers = "ja-w400dp-h1000dp-xhdpi")
    fun japaneseHomeAndDetails() = languageShots("ja", "今日", "トークン使用量", "Agent別 · 過去7日間", "区別なし（更新前）", "9.2万")

    @Test
    @Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
    fun englishHomeAndDetails() = languageShots("en", "Today", "Token usage", "By Agent · Last 7 Days", "Not split (before the update)", "92K")

    private fun languageShots(language: String, today: String, title: String, agentHeading: String, historical: String, largest: String) {
        val hub = hub()
        val detailsState = LazyListState()
        var showDetails by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(1000.dp).background(PersonaTheme.palette.background)) {
                    if (showDetails) UsageCalendarPage(hub, onBack = { showDetails = false }, listState = detailsState)
                    else UsageCalendarCard(hub.history, onOpen = { showDetails = true })
                }
            }
        }
        waitForTag("assistant-usage-calendar")
        rule.onNodeWithText(today, substring = true).assertExists()
        capture("$language-home")
        showDetails = true
        waitForText(title)
        capture("$language-details")
        rule.runOnIdle { detailsState.requestScrollToItem(5 + hub.hourly.machines.size) }
        rule.onNodeWithText(agentHeading).assertExists()
        rule.onNodeWithText(historical, useUnmergedTree = true).assertExists()
        rule.onNodeWithText(largest, useUnmergedTree = true).assertExists()
        capture("$language-details-agents")
    }

    private fun waitForTag(tag: String) {
        rule.waitUntil(5_000) { rule.onAllNodesWithTag(tag).fetchSemanticsNodes().isNotEmpty() }
    }

    private fun waitForText(text: String) {
        rule.waitUntil(5_000) { rule.onAllNodesWithText(text).fetchSemanticsNodes().isNotEmpty() }
    }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
}
