package persona.feature.inbox

import android.graphics.Bitmap
import android.content.res.Configuration
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.inboxEventFields
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.resolve
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.stateWord
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.time.Instant
import java.time.ZoneOffset
import java.util.Locale

/** The shared showcase as real list and detail pages, with its wire fields and clock, in every style and appearance. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1800dp-xhdpi")
class InboxShowcaseShotsTest {
    @get:Rule val rule = createComposeRule()

    private class ShowcaseInbox(records: List<InboxRecord>, sessions: List<AgentSession> = emptyList()) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records, sessions = sessions))
        override val connectionState = flowOf(ConnectionState.Online)
        override val preferences = MutableStateFlow(InboxPreferences(expanded = listOf("action", "working", "fyi", "noise", "done")))
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun JsonObject.text(key: String) = (this[key] as? JsonPrimitive)?.contentOrNull

    private fun showcase(): Triple<List<InboxRecord>, List<AgentSession>, Instant> {
        val root = System.getProperty("persona.repo.root")?.let(::File) ?: generateSequence(File(".").canonicalFile) { it.parentFile }
            .first { File(it, ".git").exists() && File(it, "android").isDirectory }
        val fixture = Json.parseToJsonElement(File(root, "spec/fixtures/inbox/showcase.json").readText()).jsonObject
        val records = fixture.getValue("items").jsonArray.map { value ->
            val item = value.jsonObject
            val event = inboxEventFields(item)
            InboxRecord(
                InboxItem(
                    id = item.getValue("id").jsonPrimitive.content,
                    account = item.text("account").orEmpty(), kind = item.text("kind").orEmpty(),
                    title = item.text("title").orEmpty(), sender = item.text("sender"), preview = item.text("preview").orEmpty(),
                    summary = item.text("summary"), tier = item.text("tier"), updatedAt = item.text("updated_at"),
                    doneAt = item.text("done_at"), status = event.status, urgent = event.urgent, where = event.where,
                ),
                sourceLabel = item.text("account_name").orEmpty(), count = item.text("count")?.toIntOrNull() ?: 0,
                source = item.text("source").orEmpty(), event = event,
            )
        }
        val sessions = fixture["sessions"]?.jsonArray?.map { AgentSession.fromJson(it.jsonObject) }.orEmpty()
        return Triple(records, sessions, Instant.parse(fixture.getValue("now_clock").jsonPrimitive.content))
    }

    private fun saveShot(name: String, tag: String) {
        val bitmap = rule.onNodeWithTag(tag).captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        val out = File(System.getProperty("persona.shots") ?: "build/inbox-showcase-shots").apply { mkdirs() }
        File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun localizedConfiguration(base: Configuration, locale: Locale, dark: Boolean) = Configuration(base).apply {
        setLocale(locale)
        setLayoutDirection(locale)
        uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
            if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
    }

    private fun resourcesFor(locale: Locale, dark: Boolean): android.content.res.Resources {
        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        return context.createConfigurationContext(localizedConfiguration(context.resources.configuration, locale, dark)).resources
    }

    @Test fun drawsTheSharedListInEveryLanguageStyleAndAppearance() {
        val (records, _, clock) = showcase()
        val inbox = ShowcaseInbox(records)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var locale by mutableStateOf(Locale.SIMPLIFIED_CHINESE)
        var run by mutableIntStateOf(0)
        rule.setContent {
            val configuration = localizedConfiguration(LocalConfiguration.current, locale, dark)
            val localizedContext = LocalContext.current.createConfigurationContext(configuration)
            CompositionLocalProvider(
                LocalContext provides localizedContext,
                LocalConfiguration provides configuration,
                LocalInboxClock provides { clock }, LocalInboxZone provides ZoneOffset.UTC,
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    key(run) {
                        Box(Modifier.testTag("showcase-list").width(400.dp).height(1800.dp).background(PersonaTheme.palette.background)) {
                            InboxScreen(inbox, navigator)
                        }
                    }
                }
            }
        }
        for (language in listOf(Locale.SIMPLIFIED_CHINESE, Locale.ENGLISH)) for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { locale = language; style = appearance; dark = night; run++ }
            rule.waitForIdle()
            val english = language == Locale.ENGLISH
            val separator = if (english) ": " else "："
            for (words in listOf(
                "春季订单的报价", "订单群 · 张三${separator}好的，我看一下新的", "张三发现税率错了，正在等他确认更正后的报价",
                if (english) "Waiting for 张三确认新报价" else "等张三确认新报价", "问更正后的报价行不行",
                "周五午饭", "李四${separator}周五还约吗？", if (english) "Waiting for you to confirm a reply" else "等你确认回复",
                "全员群 · 行政${separator}提醒：周一关闭", "发布群 · 王五${separator}我晚点回你", "赵六${separator}收到，谢谢",
                if (english) "You replied" else "你回过了", displayTime("2026-01-01T11:21:00Z", language, resourcesFor(language, night), ZoneOffset.UTC, clock),
            )) {
                val found = rule.onAllNodes(hasText(words, substring = true), useUnmergedTree = true).fetchSemanticsNodes()
                assertTrue("$appearance dark=$night locale=$language: missing $words", found.isNotEmpty())
            }
            val prefix = if (english) "list-en" else "list"
            saveShot("$prefix-${appearance.name.lowercase()}-${if (night) "dark" else "light"}", "showcase-list")
        }
    }

    @Test fun drawsTheSharedDetailFoldedAndExpandedInEveryLanguageStyleAndAppearance() {
        val (records, liveSessions, clock) = showcase()
        val inbox = ShowcaseInbox(records, liveSessions)
        val detail = records.first { it.item.id == "sc-1" }
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var locale by mutableStateOf(Locale.SIMPLIFIED_CHINESE)
        var run by mutableIntStateOf(0)
        rule.setContent {
            val configuration = localizedConfiguration(LocalConfiguration.current, locale, dark)
            val localizedContext = LocalContext.current.createConfigurationContext(configuration)
            CompositionLocalProvider(
                LocalContext provides localizedContext,
                LocalConfiguration provides configuration,
                LocalInboxClock provides { clock }, LocalInboxZone provides ZoneOffset.UTC,
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    key(run) {
                        Box(Modifier.testTag("showcase-detail").width(400.dp).height(1800.dp).background(PersonaTheme.palette.background)) {
                            InboxEventScreen(inbox, InboxEventRoute(detail.item.id), navigator)
                        }
                    }
                }
            }
        }
        for (language in listOf(Locale.SIMPLIFIED_CHINESE, Locale.ENGLISH)) for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { locale = language; style = appearance; dark = night; run++ }
            rule.waitForIdle()
            val english = language == Locale.ENGLISH
            val separator = if (english) ": " else "："
            rule.onNodeWithText("订单群 · 张三${separator}好的，我看一下新的", useUnmergedTree = true).assertExists()
            // Only the current-state line carries follow.next; the timeline's waiting step ends after look again.
            rule.onNodeWithText("问更正后的报价行不行", substring = true, useUnmergedTree = true).assertExists()
            rule.onNodeWithText(if (english) "Conversation ›" else "处理对话 ›", useUnmergedTree = true).assertExists()
            rule.onNodeWithText("AI summary", useUnmergedTree = true).assertDoesNotExist()
            rule.onNodeWithText("发了邮件：报价单更正", useUnmergedTree = true).assertExists()
            rule.onNodeWithText(if (english) "Assistant says" else "助手说", useUnmergedTree = true).assertExists()
            rule.onNodeWithText("收到 2 条消息 · 张三", useUnmergedTree = true).assertDoesNotExist()
            rule.onNodeWithText("收到 1 条消息 · 张三", useUnmergedTree = true).assertExists()
            saveShot("detail-folded-${if (english) "en" else "zh-Hans"}-${appearance.name.lowercase()}-${if (night) "dark" else "light"}", "showcase-detail")

            rule.onNodeWithText(if (english) "Timeline" else "处理链条").performClick()
            rule.waitForIdle()
            rule.onNodeWithText("收到 2 条消息 · 张三", useUnmergedTree = true).assertExists()
            rule.onNodeWithText("开了 Agent：核对报价单", useUnmergedTree = true).assertExists()
            rule.onNodeWithText("Agent 停了：报价单第 3 行税率错了", useUnmergedTree = true).assertExists()
            if (liveSessions.isEmpty()) {
                for (word in listOf("完成未看", "已看", "未运行", "Unread", "Seen", "Not running")) {
                    rule.onAllNodesWithText(word, useUnmergedTree = true).assertCountEquals(0)
                }
            } else {
                val expectedState = requireNotNull(liveSessions.firstOrNull()?.stateWord(false)?.resolve(resourcesFor(language, night)))
                rule.onNodeWithText(expectedState, useUnmergedTree = true).assertExists()
            }
            saveShot("detail-expanded-${if (english) "en" else "zh-Hans"}-${appearance.name.lowercase()}-${if (night) "dark" else "light"}", "showcase-detail")
        }
    }

    @Test fun drawsTheSharedSc2DetailInChineseInEveryStyleAndAppearance() {
        val (records, liveSessions, clock) = showcase()
        val inbox = ShowcaseInbox(records, liveSessions)
        val detail = records.first { it.item.id == "sc-2" }
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        rule.setContent {
            val configuration = localizedConfiguration(LocalConfiguration.current, Locale.SIMPLIFIED_CHINESE, dark)
            val localizedContext = LocalContext.current.createConfigurationContext(configuration)
            CompositionLocalProvider(
                LocalContext provides localizedContext,
                LocalConfiguration provides configuration,
                LocalInboxClock provides { clock }, LocalInboxZone provides ZoneOffset.UTC,
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    key(run) {
                        Box(Modifier.testTag("showcase-detail-sc-2").width(400.dp).height(1800.dp).background(PersonaTheme.palette.background)) {
                            InboxEventScreen(inbox, InboxEventRoute(detail.item.id), navigator)
                        }
                    }
                }
            }
        }
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { style = appearance; dark = night; run++ }
            rule.waitForIdle()
            rule.onNodeWithText("等你确认回复", useUnmergedTree = true).assertExists()
            rule.onNodeWithText("处理对话 ›", useUnmergedTree = true).assertExists()
            rule.onNodeWithText("约，中午见。", useUnmergedTree = true).assertExists()
            val resources = resourcesFor(Locale.SIMPLIFIED_CHINESE, night)
            rule.onNodeWithText(resources.getString(R.string.inbox_line_say), useUnmergedTree = true).assertDoesNotExist()
            rule.onNodeWithText(resources.getString(R.string.inbox_line_retry), useUnmergedTree = true).assertDoesNotExist()
            val summary = rule.onNodeWithText("李四问周五的午饭还约不约", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val suggestion = rule.onNodeWithText("约，中午见。", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            assertTrue("$appearance dark=$night: summary must precede suggestion", summary.top < suggestion.top)
            saveShot("detail-sc-2-zh-Hans-${appearance.name.lowercase()}-${if (night) "dark" else "light"}", "showcase-detail-sc-2")
        }
    }
}
