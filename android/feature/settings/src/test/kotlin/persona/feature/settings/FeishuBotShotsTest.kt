package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File
import java.time.Instant

/**
 * Settings › Lark bot (#2806) drawn with Robolectric's native graphics, in every style, light and dark: a bot that is
 * connected and paired (with the agents chatted with in Feishu), and one with no credentials yet, where Verify and save
 * waits for the secret. PNGs in `build/feishu-bot-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1600dp-xhdpi")
class FeishuBotShotsTest {
    @get:Rule val rule = createComposeRule()

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/feishu-bot-shots").also { it.mkdirs() }

    private class Bot(val status: FeishuBotStatus, override val sync: LarkSyncSettings? = null, override val groupAccess: FeishuGroupAccessSettings? = null) : FeishuBotSettings {
        override suspend fun status() = status
        override suspend fun update(update: FeishuBotUpdate) = status
    }

    /**
     * The daily sync (#15392) with example data: the chat bot waiting for the admin's approval, its account up to date;
     * a second company's sync bot with one account to authorize again, one whose access ends soon, and one waiting too;
     * and a bot whose setup was left half way.
     */
    private val syncView: LarkSyncView get() {
        val now = Instant.now()
        val ago = now.minusSeconds(3 * 3600).toString()
        val next = now.plusSeconds(20 * 3600).toString()
        return LarkSyncView(available = true, scheduleHour = 3,
            accounts = listOf(LarkSyncAccount("a1", label = "示例用户"), LarkSyncAccount("a2", name = "Example Co."),
                LarkSyncAccount("a3", label = "Lark 工作区"), LarkSyncAccount("a4")),
            bots = listOf(
                LarkSyncBot("b1", label = "示例公司", tier = LarkSync.TierDocs, hasSecret = true),
                LarkSyncBot("primary", appId = "cli_a1b2c3", domain = "https://open.feishu.cn", tier = LarkSync.TierFull,
                    approval = "awaiting_approval", primary = true, hasSecret = true),
                LarkSyncBot("b2", label = "未完成的机器人", hasSecret = false, registering = true),
            ),
            bindings = listOf(
                LarkSyncBinding("primary:a1", "primary", "a1", tier = LarkSync.TierFull, enabled = true, health = "ok", reauthDueDays = 300,
                    lastRunAt = ago, nextRunAt = next, lastMessages = 128, lastDocs = 6),
                LarkSyncBinding("b1:a2", "b1", "a2", enabled = true, health = "needs_reauth", lastRunAt = ago, lastMessages = 0, lastDocs = 3),
                LarkSyncBinding("b1:a3", "b1", "a3", enabled = true, health = "ok", reauthDueDays = 9, lastRunAt = ago, nextRunAt = next,
                    lastMessages = 0, lastDocs = 1),
                LarkSyncBinding("b1:a4", "b1", "a4", enabled = true, health = "awaiting_approval"),
            ))
    }

    private val paired = FeishuBotStatus(enabled = true, appId = "cli_a1b2c3", domain = "https://open.feishu.cn", hasSecret = true, connected = true,
        lastEventAt = Instant.now().minusSeconds(300).epochSecond, botName = "Persona 助手",
        owner = FeishuBotOwner("示例用户", 1), target = "主对话",
        agents = listOf(FeishuBotAgent("修登录页的跳转", "laptop", "s1"), FeishuBotAgent("整理发布说明", "macmini", "s2"),
            FeishuBotAgent("同一会话的另一个话题", "laptop", "s1")))

    private fun shots(name: String) {
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "feishu-bot-$name-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    private fun show(api: FeishuBotSettings) = rule.setContent {
        PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
            key(style, dark) {
                Box(Modifier.testTag("shot").fillMaxSize().background(PersonaTheme.palette.background)) { FeishuBotPage(api, onBack = {}) }
            }
        }
    }

    @Test fun aPairedBot() {
        show(Bot(paired))
        rule.waitUntil(5_000) { rule.onAllNodes(androidx.compose.ui.test.hasText("已连接")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("Persona 助手").assertIsDisplayed()
        rule.onNodeWithText("修登录页的跳转").assertIsDisplayed()
        rule.onNodeWithText("验证并保存").assertIsNotEnabled()
        rule.onAllNodes(androidx.compose.ui.test.hasText("https://open.feishu.cn")).assertCountEquals(0) // Feishu, not the URL
        shots("paired")
    }

    /** Every style, light and dark, scrolled to [target] once the page is drawn again in that style and [prepare]d. */
    private fun syncShots(name: String, target: String, prepare: () -> Unit) {
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            prepare()
            bringIn(target)
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "feishu-bot-$name-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    /** Scrolls the page until [text] is on screen, once the page has it. A test tag is a node the list can scroll to too. */
    private fun bringIn(text: String) = rule.waitUntil(5_000) {
        runCatching {
            rule.onAllNodes(hasScrollToNodeAction())[0].performScrollToNode(hasText(text) or androidx.compose.ui.test.hasTestTag(text))
        }.isSuccess
    }

    @Test fun theDailySync() {
        show(Bot(paired.copy(agents = null), FakeLarkSync(syncView)))
        // The chat bot first, its app waiting for the admin; then the second company's accounts, each in its state.
        for (text in listOf("每天同步到知识库", "聊天机器人", "等待管理员审批", "示例用户", "示例公司", "需要重新授权", "重新授权",
            "授权还有 9 天到期，重新授权后可继续同步。", "账号 4", "继续设置", "同步时间", "添加账号或机器人")) {
            bringIn(text)
            rule.onAllNodes(hasText(text, substring = true), useUnmergedTree = true)[0].assertIsDisplayed()
        }
        syncShots("sync", "添加账号或机器人") {}
    }

    @Test fun addingAnAccount() {
        val sync = FakeLarkSync(syncView)
        show(Bot(paired.copy(agents = null), sync))
        syncShots("sync-add", "扫码新建") {
            bringIn("添加账号或机器人")
            rule.onNodeWithText("添加账号或机器人").performClick()
            rule.waitForIdle()
        }
        bringIn("用聊天机器人")
        bringIn("扫码新建")
        rule.onNodeWithText("扫码新建").performClick()
        rule.waitUntil(5_000) { sync.calls.any { it.startsWith("create") } }
        bringIn("第 1 步，共 2 步：创建应用")
    }

    @Test fun anOlderHubShowsNoSync() {
        show(Bot(paired.copy(agents = null), FakeLarkSync(null)))
        bringIn("怎么建机器人")
        rule.onAllNodes(hasText("每天同步到知识库")).assertCountEquals(0)
    }

    /** Group access (#16848): an allowlist, one recent request and one tool, in every style, light and dark. */
    private val groupAccess = FeishuGroupAccess(
        revision = 3, paired = true,
        policy = FeishuGroupAccessPolicy(mode = FeishuGroupAccessMode.Allowlist,
            allowedUsers = listOf(FeishuGroupUser("ou_1", "示例用户")),
            allowedChats = listOf(FeishuGroupChat("oc_1", "项目群")),
            blockedUsers = listOf(FeishuGroupUser("ou_9", "已屏蔽的人")),
            guestTools = listOf("wiki_search"), perChatHourly = 20),
        availableTools = listOf(FeishuGuestToolOption("wiki_search", "知识库")),
        recentRequests = listOf(FeishuGroupRequest("ou_2", "同事乙", "oc_1", "项目群", 1795000000)),
    )

    private class GroupAccessApi(private val access: FeishuGroupAccess?) : FeishuGroupAccessSettings {
        override suspend fun read() = access
        override suspend fun update(revision: Long, policy: FeishuGroupAccessPolicy) = access ?: error("nothing to update")
    }

    @Test fun groupAccess() {
        show(Bot(paired.copy(agents = null), groupAccess = GroupAccessApi(groupAccess)))
        for (text in listOf("群聊访问", "我允许的人", "只有你添加的人可以 @ 机器人，并且只能在你允许的群里。", "每个群每小时的提问数",
            "允许的群", "项目群", "允许的人", "示例用户", "最近的请求", "同事乙", "已屏蔽", "屏蔽在任何模式下都优先生效。",
            "访客可用的工具", "知识库 · wiki_search")) {
            bringIn(text)
            rule.onAllNodes(hasText(text, substring = true), useUnmergedTree = true)[0].assertIsDisplayed()
        }
        bringIn("group-access-caution")
        rule.onNodeWithTag("group-access-caution").assertIsDisplayed()
        syncShots("group-access", "访客可用的工具") {}
    }

    @Test fun anOlderHubShowsNoGroupAccess() {
        show(Bot(paired.copy(agents = null), groupAccess = GroupAccessApi(null)))
        bringIn("怎么建机器人")
        rule.onAllNodes(hasText("群聊访问")).assertCountEquals(0)
    }

    @Test fun anUnpairedBotShowsNoGroupAccess() {
        show(Bot(FeishuBotStatus(enabled = true, hasSecret = true, connected = true), groupAccess = GroupAccessApi(groupAccess)))
        bringIn("怎么建机器人")
        rule.onAllNodes(hasText("群聊访问")).assertCountEquals(0)
    }

    @Test fun noCredentialsYet() {
        show(Bot(FeishuBotStatus()))
        rule.waitUntil(5_000) { rule.onAllNodes(androidx.compose.ui.test.hasText("未打开")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("怎么建机器人").assertIsDisplayed()
        shots("empty")
        rule.onAllNodes(hasSetTextAction())[0].performTextInput("cli_new")
        rule.onNodeWithText("验证并保存").assertIsNotEnabled() // the secret too, the first time
        rule.onAllNodes(hasSetTextAction())[1].performTextInput("s3cret")
        rule.onNodeWithText("验证并保存").assertIsEnabled()
    }
}
