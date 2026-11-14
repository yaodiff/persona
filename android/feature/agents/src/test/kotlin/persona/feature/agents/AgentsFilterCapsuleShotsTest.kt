package persona.feature.agents

import android.graphics.Bitmap
import android.graphics.Canvas
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.Density
import persona.core.designsystem.icons.PhIcons
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.unit.dp
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.domain.sort.AgentGrouping
import persona.core.domain.sort.AgentQuickFilter
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/** Production Agents page, not an HTML reconstruction; synthetic data only. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h860dp-xhdpi")
class AgentsFilterCapsuleShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val sessions = listOf(
        Session("computer", "s1", title = "检查发布说明", state = "unread", lastText = "发布说明已整理好。", updatedAt = "2026-11-13T12:00:00Z"),
        Session("computer", "s2", title = "整理旅行资料", state = "working", lastText = "正在整理路线与住宿。", updatedAt = "2026-11-13T12:00:00Z"),
        Session("computer", "s3", title = "等待后台任务", state = "working", lastText = "等待任务完成。", backgroundWaiting = true, updatedAt = "2026-11-13T12:00:00Z"),
        Session("computer", "s4", title = "选择下一步", state = "needs_you", lastText = "请确认下一步。", updatedAt = "2026-11-13T12:00:00Z"),
    )
    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override val lastHello get() = Hello(serverVersion = "1.0.0", protocol = 1)
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private class Requests : InteractionCenter {
        override val pending = MutableStateFlow<List<Interaction>>(emptyList())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private class Sender : MessageSender {
        override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private class Repository(rows: List<Session>) : SessionsRepository {
        override val sessions = MutableStateFlow(rows)
        override val machines = MutableStateFlow(listOf(Machine("computer", "我的电脑", online = true)))
        override suspend fun refresh() = Unit
    }
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
    private fun preferences(): AgentsPreferences {
        val store = object : DataStore<Preferences> {
            override val data = MutableStateFlow<Preferences>(emptyPreferences())
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences) = transform(data.value).also { data.value = it }
        }
        val hub = object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot())
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        }
        return AgentsPreferences(store, hub)
    }
    private var shotSuffix = ""
    private fun save(scene: String, style: AppStyle, dark: Boolean, popup: Boolean = false) {
        val root = rule.onNodeWithTag("shot")
        val image = root.captureToImage().asAndroidBitmap().copy(Bitmap.Config.ARGB_8888, true)
        if (popup) {
            val menu = rule.onNode(isPopup())
            val offset = menu.fetchSemanticsNode().positionOnScreen - root.fetchSemanticsNode().positionOnScreen
            Canvas(image).drawBitmap(menu.captureToImage().asAndroidBitmap(), offset.x, offset.y, null)
        }
        val directory = File(System.getProperty("persona.shots") ?: "build/filter-capsule-shots").also { it.mkdirs() }
        File(directory, "agents-$scene-${style.name.lowercase()}-${if (dark) "dark" else "light"}$shotSuffix.png").outputStream()
            .use { image.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
    private fun scene(style: AppStyle, dark: Boolean, fontScale: Float = 1f) {
        val model = AgentsViewModel(Repository(sessions), Requests(), Connection(), Sender(), preferences())
        model.grouping(AgentGrouping.State)
        shotSuffix = if (fontScale > 1f) "-large" else ""
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                CompositionLocalProvider(LocalHomeChrome provides HomeChrome(listOf(
                    NavItem("assistant", "助手", PhIcons.ChatCircle), NavItem("agents", "Agents", PhIcons.Terminal)), "agents", {}, {})) {
                    Box(Modifier.width(400.dp).height(860.dp).background(PersonaTheme.palette.background).testTag("shot")) {
                        AgentsScreen(model, navigator, AgentsMetadata())
                    }
                }
            }
            }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithTag("agents-status-0").fetchSemanticsNodes().isNotEmpty() }
        rule.waitForIdle()
        rule.onNodeWithTag("shot").performTouchInput { swipeDown(durationMillis = 300) }
        rule.waitForIdle()
        val grouping = rule.onNodeWithTag("agents-grouping")
        val fixed = grouping.fetchSemanticsNode().boundsInRoot
        rule.onNodeWithTag("agents-status-0").assertIsSelected()
        save("initial", style, dark)
        rule.onNodeWithTag("agents-status-scroll").performTouchInput { swipeLeft(durationMillis = 500) }
        rule.waitForIdle()
        // Focus updates after the same bounded settle interval used by the approved prototype.
        rule.waitUntil(5_000) {
            rule.onAllNodes(SemanticsMatcher.expectValue(AgentFilterExpanded, true) and
                SemanticsMatcher.expectValue(SemanticsProperties.Selected, false)).fetchSemanticsNodes().isNotEmpty()
        }
        assertEquals(fixed, grouping.fetchSemanticsNode().boundsInRoot)
        save("middle", style, dark)
        rule.onNodeWithTag("agents-status-scroll").performScrollToIndex(4)
        rule.onNodeWithTag("agents-status-4").performClick()
        rule.waitUntil(5_000) { model.page.value.settings.quick == AgentQuickFilter.BackgroundWaiting }
        rule.waitForIdle()
        rule.onNodeWithTag("agents-status-4").assertIsSelected()
        rule.onNodeWithTag("agents-status-4").assert(SemanticsMatcher.expectValue(AgentFilterExpanded, true))
        save("selected", style, dark)
        grouping.performClick()
        rule.waitForIdle()
        val menu = rule.onNode(isPopup())
        menu.assertExists()
        listOf("按状态", "按项目", "按仓库").forEach { rule.onNode(hasText(it) and hasAnyAncestor(isPopup())).assertExists() }
        rule.onNode(hasText("工作中") and hasAnyAncestor(isPopup())).assertDoesNotExist()
        rule.onNode(hasText("完成未看") and hasAnyAncestor(isPopup())).assertDoesNotExist()
        save("menu", style, dark, popup = true)
        rule.onNode(hasText("按项目") and hasAnyAncestor(isPopup())).performClick()
        rule.waitUntil(5_000) { model.page.value.preferences.grouping == AgentGrouping.MachineSpace }
        rule.onNode(isPopup()).assertDoesNotExist()
        assertEquals(AgentQuickFilter.BackgroundWaiting, model.page.value.settings.quick)
    }
    @Test fun glassLight() = scene(AppStyle.Glass, false)
    @Test fun glassDark() = scene(AppStyle.Glass, true)
    @Test fun briefLight() = scene(AppStyle.Brief, false)
    @Test fun briefDark() = scene(AppStyle.Brief, true)
    @Test fun boardLight() = scene(AppStyle.Board, false)
    @Test fun boardDark() = scene(AppStyle.Board, true)
    @Test fun glassLargeLight() = scene(AppStyle.Glass, false, 2f)
    @Test fun glassLargeDark() = scene(AppStyle.Glass, true, 2f)
}
