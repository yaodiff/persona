package persona.feature.assistant

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Direction
import persona.core.contracts.DirectionAgent
import persona.core.contracts.DirectionCounts
import persona.core.contracts.DirectionTask
import persona.core.contracts.DirectionTaskState
import persona.core.contracts.DirectionTree
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantSearchHit
import persona.core.contracts.api.LeaderCounts
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import java.io.File
import java.time.Instant

/**
 * The Persona tab's history drawer drawn on the JVM with Robolectric's native graphics (no emulator), in every style,
 * light and dark: the main conversation first, as "Persona" with Persona's face, and "More" without "Profile".
 * Assistant details remain available from the Persona toolbar, not settings. Each is written as a PNG to `build/drawer-shots/` (or
 * `-Dpersona.shots=<dir>`) to look at.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2100dp-xhdpi")
class AssistantDrawerShotsTest {
    @get:Rule val rule = createComposeRule()

    private val minutesAgo = { m: Long -> Instant.now().minusSeconds(m * 60).toString() }
    private val main = Session(machineId = "hub", sessionId = "main", title = "主对话", status = "idle",
        updatedAt = minutesAgo(2), lastText = "今天的日程已经整理好了。")
    private val pinned = Session(machineId = "hub", sessionId = "p1", title = "周报草稿", status = "done",
        updatedAt = minutesAgo(30), lastText = "第二版写好了。")
    private val recent = listOf(
        Session(machineId = "hub", sessionId = "r1", title = "整理收件箱", status = "working", updatedAt = minutesAgo(5)),
        Session(machineId = "hub", sessionId = "r2", title = "周末去哪儿", status = "idle", updatedAt = minutesAgo(90)),
    )

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/drawer-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun drawsTheDrawerInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var groups by mutableStateOf(ConversationGroups(main, listOf(pinned), recent))
        var opened: SessionKey? = null
        var mainTapped = false
        var info: AssistantInfo? = null
        var settingsTapped = false
        var inboxTapped = false
        var openedPage = ""
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                    AssistantDrawer("", {}, groups, active = null, hasMain = groups.main != null,
                        onEnd = null, onMain = { mainTapped = true }, onOpen = { opened = it }, onInfo = { info = it },
                        onNew = {}, onSettings = { settingsTapped = true }, onTodos = {}, todoCount = 2,
                        onInbox = { inboxTapped = true }, inboxCount = 5, settingsDot = true,
                        pages = listOf(
                            NavItem("machines", "电脑", PhIcons.Desktop, badge = 2),
                            NavItem("extension", "扩展", PhIcons.PuzzlePiece, dot = true),
                        ), onPage = { openedPage = it })
                }
            }
        }
        // The main conversation reads "Persona" (the session keeps its own title), with a small 主对话 under it so it
        // reads as a chat (#6327, as the web, #6293), and "Profile" is gone.
        rule.onAllNodesWithText("主对话").assertCountEquals(1)
        rule.onAllNodesWithText("资料页").assertCountEquals(0)
        rule.onAllNodesWithText("认识 Persona").assertCountEquals(0)
        // #2004 on every platform: the first row is the main conversation as "Persona" with the app's mark, then a
        // line; Persona is not listed again (no header of its own, no second main row).
        rule.onAllNodesWithText("Persona").assertCountEquals(1)
        val persona = rule.onNodeWithText("Persona").fetchSemanticsNode().boundsInRoot
        // The texts themselves, not the row they merge into.
        val name = rule.onNodeWithText("Persona", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val mark = rule.onNodeWithText("主对话", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertTrue("主对话 is under Persona", name.bottom <= mark.top)
        val search = rule.onNodeWithText("搜索对话").fetchSemanticsNode().boundsInRoot
        assertTrue("Persona is above the search", persona.bottom <= search.top && mark.bottom <= search.top)
        rule.onNodeWithText("Persona").performClick()
        assertTrue(mainTapped)
        assertEquals(null, opened)
        rule.onNodeWithText("启动器").performScrollTo().performClick()
        assertEquals(AssistantInfo.Launcher, info)
        rule.onNodeWithText("会话信息").performClick()
        assertEquals(AssistantInfo.Session, info)
        rule.onNodeWithContentDescription("设置").performClick()
        assertTrue(settingsTapped)
        rule.onNodeWithText("收件箱").performScrollTo().performClick()
        assertTrue(inboxTapped)
        // The leader is the 常驻 group; every other chat waits under one row, which opens the list in place (#4331).
        rule.onNodeWithText("常驻").assertExists()
        rule.onNodeWithText("周报草稿").assertExists()
        rule.onAllNodesWithText("整理收件箱").assertCountEquals(0)
        rule.onNodeWithText("全部对话（2）").performScrollTo().performClick()
        rule.onNodeWithText("整理收件箱").assertExists()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            capture("drawer-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        // No main conversation yet: the same row starts one.
        style = AppStyle.Glass; dark = false
        mainTapped = false
        groups = ConversationGroups(null, emptyList(), recent)
        rule.onAllNodesWithText("Persona").assertCountEquals(1)
        rule.onNodeWithText("开始主对话").performClick()
        assertTrue(mainTapped)
        capture("drawer-no-main-glass-light")
    }

    @Test fun navigationPagesAddTrailingChevronsAndKeepBadges() {
        var opened = ""
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalHomeDrawer provides true) {
                    Box(Modifier.width(320.dp).height(800.dp).background(PersonaTheme.palette.background)) {
                        AssistantDrawer("", {}, ConversationGroups(null, emptyList(), emptyList()), active = null,
                            hasMain = false, onEnd = null, onMain = {}, onOpen = {}, onInfo = {}, onNew = {},
                            onSettings = {}, onInbox = {}, inboxCount = 2, showTodos = false, pages = listOf(
                                NavItem("machines", "电脑", PhIcons.Desktop, badge = 2),
                                NavItem("extension", "扩展", PhIcons.PuzzlePiece, badge = 2),
                            ), onPage = { opened = it })
                    }
                }
            }
        }
        rule.onAllNodesWithText("2").assertCountEquals(3)
        // Inbox has the same badge but no navigation chevron. The final 22 dp are its chevron slot on each page.
        val withoutChevron = rule.onNodeWithText("收件箱").captureToImage().asAndroidBitmap()
        for (label in listOf("电脑", "扩展")) {
            val page = rule.onNodeWithText(label).captureToImage().asAndroidBitmap()
            assertEquals(withoutChevron.width, page.width)
            assertEquals(withoutChevron.height, page.height)
            val trailingStart = page.width - with(rule.density) { 22.dp.toPx() }.toInt()
            var differentPixels = 0
            for (x in trailingStart until page.width) for (y in 0 until page.height) {
                if (page.getPixel(x, y) != withoutChevron.getPixel(x, y)) differentPixels++
            }
            assertTrue("$label draws a chevron after its badge", differentPixels > 20)
        }
        rule.onNodeWithText("电脑").performClick()
        assertEquals("machines", opened)
        rule.onNodeWithText("扩展").performClick()
        assertEquals("extension", opened)
    }

    @Test fun aConversationsMenuArchivesItButTheMainOnesDoesNot() {
        var archived: Session? = null
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                AssistantDrawer("", {}, ConversationGroups(main, emptyList(), recent.take(1)), active = null,
                    hasMain = true, onEnd = null, onMain = {}, onOpen = {}, onInfo = {}, onNew = {}, onSettings = {},
                    onArchive = { archived = it })
            }
        }
        rule.onNodeWithText("全部对话（1）").performClick()
        rule.onAllNodesWithContentDescription("对话操作")[0].performClick()
        rule.onNodeWithText("拷贝会话 ID").assertExists()
        rule.onAllNodesWithText("归档").assertCountEquals(0)
        rule.onNodeWithText("拷贝会话 ID").performClick()
        rule.onAllNodesWithContentDescription("对话操作")[1].performClick()
        rule.onNodeWithText("归档").performClick()
        assertEquals(recent.first(), archived)
    }

    // The Persona row says what the main conversation waits on: its pending count first, else working / unread.
    @Test fun thePersonaRowShowsTheMainConversationsState() {
        var groups by mutableStateOf(ConversationGroups(main.copy(pendingInteractions = 3, state = "working"), emptyList(), recent))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(700.dp).background(PersonaTheme.palette.background)) {
                    AssistantDrawer("", {}, groups, active = null, hasMain = true, onEnd = null, onMain = {}, onOpen = {},
                        onInfo = {}, onNew = {}, onSettings = {})
                }
            }
        }
        rule.onNodeWithText("3").assertExists()
        capture("drawer-persona-pending-glass-light")
        groups = ConversationGroups(main.copy(state = "working"), emptyList(), recent.drop(1))
        rule.waitForIdle()
        capture("drawer-persona-working-glass-light")
        rule.onAllNodesWithText("3").assertCountEquals(0)
        rule.onAllNodesWithText("工作中").assertCountEquals(1)
    }

    // #6230, #6626: one spinning activity mark. A lead whose own conversation is idle while something under it runs (the
    // tree's `running`) spins in every style, with no 「在做 N」 beside it; a lead with nothing running keeps its quiet row.
    @Test fun anIdleLeadWithAgentsWorkingSpins() {
        var style by mutableStateOf(AppStyle.Glass)
        val idleLead = Session(machineId = "hub", sessionId = "l9", title = "1110 | 探索 | 基建", status = "idle",
            updatedAt = minutesAgo(8), lastText = "等待下一轮。")
        val groups = ConversationGroups(main, listOf(idleLead), recent,
            names = mapOf("l9" to "基建"),
            counts = mapOf("l9" to LeaderCounts(running = true, needsYou = 0)))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    AssistantDrawer("", {}, groups, active = null, hasMain = true, onEnd = null, onMain = {}, onOpen = {},
                        onInfo = {}, onNew = {}, onSettings = {})
                }
            }
        }
        rule.onNodeWithText("在做", substring = true).assertDoesNotExist()
        for (s in AppStyle.entries) {
            style = s
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            // the spinner is the only element carrying the loading content description (JSpinner), one for this lead
            rule.onNodeWithContentDescription("加载中").assertExists()
            capture("drawer-lead-working-spin-${s.name.lowercase()}-light")
        }
    }

    // #4331: the leaders, "常驻", under their names, each saying what it has open, in every style, light and dark; every
    // other chat waits under one All chats row.
    @Test fun drawsTheLeadersWithTheirCounts() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val leadA = Session(machineId = "hub", sessionId = "l1", title = "1110 | 探索 | 增长", status = "working",
            updatedAt = minutesAgo(4), lastText = "正在整理增长实验的数据。")
        val leadB = Session(machineId = "hub", sessionId = "l2", title = "1110 | 探索 | 支付", status = "idle", updatedAt = minutesAgo(20))
        val groups = ConversationGroups(main, listOf(leadA, leadB), recent,
            names = mapOf("l1" to "增长", "l2" to "支付"),
            counts = mapOf("l1" to LeaderCounts(running = true, needsYou = 1), "l2" to LeaderCounts(needsYou = 3)))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    AssistantDrawer("", {}, groups, active = null, hasMain = true, onEnd = null, onMain = {}, onOpen = {},
                        onInfo = {}, onNew = {}, onSettings = {})
                }
            }
        }
        rule.onNodeWithText("常驻").assertExists()
        rule.onNodeWithText("增长").assertExists()
        // what waits on you in words, never 「在做 N」 (#6626: what runs is the spinner)
        rule.onNodeWithText("等你 1").assertExists()
        rule.onNodeWithText("等你 3").assertExists()
        rule.onNodeWithText("在做", substring = true).assertDoesNotExist()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            capture("drawer-leaders-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        style = AppStyle.Glass; dark = false
        rule.onNodeWithText("全部对话（2）").performClick()
        rule.onNodeWithText("整理收件箱").assertExists()
        capture("drawer-all-chats-glass-light")
    }

    // #4352 + #5860: an open 常驻 lead unfolds the hub's one tree — to-dos with their state words (Not started is the quiet
    // grey for a piece no Agent has started), then a to-do's Agents and a sub-Agent. Drawn on the JVM with Robolectric, so
    // the tree is checked here even with no emulator.
    @Test fun drawsAnOpenDirectionTreeWithANotStartedPiece() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val lead = Session(machineId = "hub", sessionId = "l1", title = "1110 | 探索 | 增长", status = "working",
            updatedAt = minutesAgo(4), lastText = "正在整理增长实验的数据。")
        val tree = DirectionTree(listOf(Direction("l1", "增长",
            counts = DirectionCounts(working = 1, needsYou = 1, planned = 1),
            tasks = listOf(
                DirectionTask("t1", "上线支付", DirectionTaskState.Working, listOf(
                    DirectionAgent("hub", "a1", title = "Agent 甲", state = "working",
                        children = listOf(DirectionAgent("hub", "a1@sub", title = "子 Agent", state = "working"))))),
                DirectionTask("t2", "核对账单", DirectionTaskState.NeedsYou),
                DirectionTask("t3", "平板两栏布局", DirectionTaskState.Planned),
            ))))
        val groups = ConversationGroups(main, listOf(lead), emptyList(),
            names = mapOf("l1" to "增长"), tree = tree)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    AssistantDrawer("", {}, groups, active = null, hasMain = true, onEnd = null, onMain = {}, onOpen = {},
                        onInfo = {}, onNew = {}, onSettings = {})
                }
            }
        }
        // Fold the lead open, then its working to-do, through the real carets (the fold state is SharedPreferences-backed).
        rule.onNodeWithContentDescription("增长 的待办").performClick()
        rule.onNodeWithText("上线支付").assertExists()
        rule.onNodeWithText("核对账单").assertExists()
        rule.onNodeWithText("平板两栏布局").assertExists()
        // Every open task says its state; the piece nobody started reads Not started, in the quiet tone.
        rule.onNodeWithText("在做").assertExists()
        rule.onNodeWithText("等你").assertExists()
        rule.onNodeWithText("未开始").assertExists()
        rule.onNodeWithContentDescription("上线支付 的 Agent").performClick()
        rule.onNodeWithText("Agent 甲").assertExists()
        // A sub-Agent is one more fold down: open the Agent's own caret, then the child shows.
        rule.onNodeWithContentDescription("Agent 甲 的 Agent").performClick()
        rule.onNodeWithText("子 Agent").assertExists()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            capture("drawer-direction-open-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }

    // #4922 / #5196: every newest chat, including automatic ones with their source mark, between 常驻 and All chats
    // (which still lists everything), in every style, light and dark; no Recent heading when the group is empty.
    @Test fun drawsRecentBetweenTheLeadersAndAllChats() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val lead = Session(machineId = "hub", sessionId = "l1", title = "1110 | 探索 | 增长", status = "working", updatedAt = minutesAgo(4))
        val auto = Session(machineId = "hub", sessionId = "a1", title = "收件箱分身", origin = "inbox", status = "idle", updatedAt = minutesAgo(1))
        val snapshot = persona.core.contracts.api.AssistantSnapshot(main.key, listOf(main, lead, auto) + recent, leaders = listOf("l1"),
            directionNames = mapOf("l1" to "增长"))
        var groups by mutableStateOf(conversationGroups(snapshot, "", ConversationWords("新对话", "接力", "接力：%s")))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1300.dp).background(PersonaTheme.palette.background)) {
                    AssistantDrawer("", {}, groups, active = null, hasMain = true, onEnd = null, onMain = {}, onOpen = {},
                        onInfo = {}, onNew = {}, onSettings = {})
                }
            }
        }
        val top = { text: String -> rule.onNodeWithText(text).fetchSemanticsNode().boundsInRoot.top }
        rule.onNodeWithText("最近").assertExists()
        assertTrue("常驻 › 最近 › 全部对话", top("常驻") < top("最近") && top("最近") < top("整理收件箱") && top("周末去哪儿") < top("全部对话（3）"))
        rule.onNodeWithText("收件箱分身").assertExists()
        rule.onNodeWithContentDescription("收件箱", useUnmergedTree = true).assertExists()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            capture("drawer-recent-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        // #6228: the Recent heading folds its rows and opens them again.
        rule.onNodeWithText("最近").performClick()
        rule.onNodeWithText("整理收件箱").assertDoesNotExist()
        rule.onNodeWithText("周末去哪儿").assertDoesNotExist()
        rule.onNodeWithText("最近").performClick()
        rule.onNodeWithText("整理收件箱").assertExists()
        groups = groups.copy(recent = emptyList())
        rule.waitForIdle()
        rule.onNodeWithText("最近").assertDoesNotExist()
    }

    // #5196: automatic conversations carry their source's mark everywhere the same row is drawn. The user's own chat
    // keeps its latest-message channel mark; an automatic chat's source takes precedence over that channel.
    @Test fun drawsSourceMarksInRecentLeadersSearchAndAllChats() {
        val origins = listOf("inbox", "task", "lark_agent", "spawned", "extension", "something_new")
        val titles = listOf("Inbox chat", "Scheduled chat", "Lark chat", "Spawned chat", "Extension chat", "Other chat")
        val sources = origins.mapIndexed { index, origin ->
            Session(machineId = "hub", sessionId = "auto-$index", title = titles[index], origin = origin,
                channels = if (origin == "lark_agent") listOf("feishu") else emptyList())
        }
        val own = Session(machineId = "hub", sessionId = "own", title = "Own chat", origin = "user", channels = listOf("feishu"))
        val lead = sources[1].copy(sessionId = "lead", title = "Leader chat")
        var query by mutableStateOf("")
        var groups by mutableStateOf(ConversationGroups(main = main, directions = listOf(lead), others = sources + own,
            recent = sources + own, names = mapOf("lead" to "Leader chat")))
        rule.setContent {
            CompositionLocalProvider(LocalChannels provides listOf(persona.core.contracts.api.HubChannel(id = "feishu", name = "Feishu channel"))) {
                PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1700.dp).background(PersonaTheme.palette.background)) {
                        AssistantDrawer(query, { query = it }, groups, active = null, hasMain = true, onEnd = null,
                            onMain = {}, onOpen = {}, onInfo = {}, onNew = {}, onSettings = {})
                    }
                }
            }
        }
        val labels = listOf("收件箱", "定时任务", "来自 飞书 /agent", "对话派生", "来自扩展", "其他")
        fun assertRowMark(title: String, label: String, below: Float? = null) {
            val rows = rule.onAllNodesWithText(title).fetchSemanticsNodes()
                .filter { below == null || it.boundsInRoot.top >= below }
            assertTrue("no $title row below $below", rows.isNotEmpty())
            val marks = rule.onAllNodesWithContentDescription(label, useUnmergedTree = true).fetchSemanticsNodes()
            assertTrue("$title lacks $label in its own row", rows.any { row ->
                marks.any { mark -> mark.boundsInRoot.top >= row.boundsInRoot.top && mark.boundsInRoot.bottom <= row.boundsInRoot.bottom }
            })
        }
        fun assertMarks(below: Float? = null) {
            titles.forEachIndexed { index, title -> assertRowMark(title, labels[index], below) }
            assertRowMark("Own chat", "Feishu channel", below)
        }
        assertMarks() // Every Recent row, not merely one mark somewhere in the drawer.
        assertRowMark("Leader chat", "定时任务")
        groups = groups.copy(recent = emptyList(), directions = emptyList())
        rule.waitForIdle()
        rule.onNodeWithText("全部对话（7）").performScrollTo().performClick()
        val allChatsBottom = rule.onNodeWithText("全部对话（7）").fetchSemanticsNode().boundsInRoot.bottom
        assertMarks(below = allChatsBottom) // Every expanded All chats row.
        query = "chat"
        groups = groups.copy(directions = listOf(lead))
        rule.waitForIdle()
        assertMarks() // Every search-result row.
        assertRowMark("Leader chat", "定时任务")
    }

    // #5084: the built-in background supervisor waits under All chats with the chats Persona opened itself, under the
    // app's name for it, in every style, light and dark; its menu only copies its session id.
    @Test fun drawsTheBackgroundSupervisorUnderAllChatsWithoutPinArchiveOrEnd() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val scheduled = Session(machineId = "hub", sessionId = "s1", title = "每日早报", origin = "task", originRef = "t1",
            status = "idle", updatedAt = minutesAgo(50))
        val background = Session(machineId = "hub", sessionId = "bg", title = "Background supervisor", origin = "task",
            status = "working", updatedAt = minutesAgo(3), lastText = "登录页修复：三条验收标准都有证据，已验收。")
        val snapshot = persona.core.contracts.api.AssistantSnapshot(main.key, listOf(main, scheduled, background) + recent,
            backgroundSupervisor = "bg")
        val groups = conversationGroups(snapshot, "", ConversationWords("新对话", "接力", "接力：%s", "后台督办"))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1700.dp).background(PersonaTheme.palette.background)) {
                    AssistantDrawer("", {}, groups, active = null, hasMain = true, onEnd = {}, onMain = {}, onOpen = {},
                        onInfo = {}, onNew = {}, onSettings = {}, onArchive = {}, onLeader = {})
                }
            }
        }
        // not among the user's recent chats; under All chats, which lists it with the scheduled task's conversation
        rule.onNodeWithText("后台督办").assertDoesNotExist()
        rule.onNodeWithText("每日早报").assertDoesNotExist() // the scheduled task's chat left Recent too (#6228)
        rule.onNodeWithText("全部对话（4）").performClick()
        rule.onNodeWithText("后台督办").assertExists()
        rule.onAllNodesWithText("每日早报").assertCountEquals(1) // All chats only now, with the same Scheduled mark.
        rule.onAllNodesWithText("Background supervisor").assertCountEquals(0)
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            capture("drawer-background-supervisor-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        style = AppStyle.Glass; dark = false
        rule.waitForIdle()
        // The rows with a menu, top to bottom: Persona, the two recent chats, then All chats newest first, where the
        // background supervisor (3 minutes ago) is first.
        rule.onAllNodesWithContentDescription("对话操作")[3].performClick()
        rule.onNodeWithText("拷贝会话 ID").assertExists()
        rule.onAllNodesWithText("设为常驻").assertCountEquals(0)
        rule.onAllNodesWithText("归档").assertCountEquals(0)
        rule.onAllNodesWithText("结束…").assertCountEquals(0)
        rule.onNodeWithText("拷贝会话 ID").performClick()
        // any other chat there keeps its whole menu
        rule.onAllNodesWithContentDescription("对话操作")[4].performClick()
        rule.onNodeWithText("设为常驻").assertExists()
        rule.onNodeWithText("归档").assertExists()
        rule.onNodeWithText("结束…").assertExists()
    }

    @Test fun drawsSearchResultsInEveryStyle() {
        val said = AssistantSearchHit(itemId = "m1", snippet = "前面的话青石巷后面还有一句", ranges = listOf(listOf(4, 7)))
        val route = Session(machineId = "hub", sessionId = "route", title = "路线", status = "idle",
            updatedAt = minutesAgo(10), lastText = "最后一句不该盖住命中。")
        val titled = Session(machineId = "hub", sessionId = "titled", title = "周末去哪儿", status = "idle", updatedAt = minutesAgo(20))
        val archived = Session(machineId = "hub", sessionId = "old", title = "旧的旅行", status = "idle",
            archived = true, updatedAt = minutesAgo(40))
        val found = listOf(FoundChat(main, said), FoundChat(titled, null), FoundChat(route, said), FoundChat(archived, null))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    AssistantDrawer("青石巷", {}, ConversationGroups(main, emptyList(), emptyList()),
                        active = null, hasMain = true, onEnd = null, onMain = {}, onOpen = {}, onInfo = {},
                        onNew = {}, onSettings = {}, found = found, searchPartial = true)
                }
            }
        }
        rule.onAllNodesWithText("青石巷", substring = true).assertCountEquals(3)
        rule.onNodeWithText("周末去哪儿").assertExists()
        rule.onNodeWithText("已归档").assertExists()
        rule.onNodeWithText("较早的对话还在整理，可能有些暂时搜不到。").assertExists()
        rule.onAllNodesWithText("Persona").assertCountEquals(1)
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            capture("drawer-search-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }
}
