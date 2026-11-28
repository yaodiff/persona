package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.Density
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import persona.core.domain.sort.AgentQuickFilter
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.time.Instant

/**
 * "Lark groups" / "Terminal" on an agent's row (#3132, iOS #2698), under its time (#3043), in every style and appearance, the detailed
 * and the compact list. Written as PNGs to `build/source-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1400dp-xhdpi")
class AgentsSourceShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/source-shots").also { it.mkdirs() }

    private val now = Instant.now()
    private fun ago(minutes: Long) = now.minusSeconds(minutes * 60).toString()
    private val machines = listOf(Machine("mac", "MacBook Pro", online = true, engine = "herdr"))
    private val sessions = listOf(
        Session("mac", "s1", title = "修登录页，顺便把注册页的校验也改一下", cwd = "/code/app", state = "needs_you", spaceId = "w1",
            spaceName = "app", slotName = "1", lastText = "要跑测试吗？", updatedAt = ago(50), paneId = "w1:p1"),
        Session("mac", "s2", title = "群里问的发布时间", cwd = "/code/app", state = "unread", spaceId = "bm", spaceName = "botmux",
            lastText = "已经回复到群里了", updatedAt = ago(2), paneId = "botmux:abc", externalUrl = "https://example.com/topic"),
        Session("mac", "s3", title = "重构接口", cwd = "/code/api", state = "working", spaceId = "w3", spaceName = "api",
            slotName = "1", lastText = "在改重试", updatedAt = ago(10), paneId = "orca:1"),
    )

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override val lastHello: Hello get() = Hello(serverVersion = "1.0.0", protocol = 1)
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private class Interactions : InteractionCenter {
        override val pending = MutableStateFlow<List<Interaction>>(emptyList())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private class Sender : MessageSender {
        override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private class Repository(sessions: List<Session>, machines: List<Machine>) : SessionsRepository {
        override val sessions = MutableStateFlow(sessions)
        override val machines = MutableStateFlow(machines)
        override suspend fun refresh() = Unit
    }
    private object Nowhere : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun preferences(display: AgentsDisplayHubs = AgentsDisplayHubs { flowOf(emptyList()) }): AgentsPreferences {
        val store = object : DataStore<Preferences> {
            override val data = MutableStateFlow<Preferences>(emptyPreferences())
            override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
                transform(data.value).also { data.value = it }
        }
        val hub = object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot())
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        }
        return AgentsPreferences(store, hub, display)
    }

    @Test fun hubVisibilityRemovesTheChipAndRestoresTheFilterOnRefresh() {
        var show = true
        val api = object : AgentDisplayApi {
            override suspend fun read() = AgentsDisplaySettings(show, configured = true)
            override suspend fun migrateHidden(): AgentsDisplaySettings = error("Fresh phone never writes")
        }
        val hello = MutableStateFlow<SocketHello?>(SocketHello(1, Connection().lastHello))
        val prefs = preferences(AgentsDisplayHubs { flowOf(listOf(AgentsDisplayHub("fixture", null, true, hello, api))) })
        val model = AgentsViewModel(Repository(sessions, machines), Interactions(), Connection(), Sender(), prefs)
        var dark by mutableStateOf(false)
        var large by mutableStateOf(false)
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, if (large) 2f else 1f)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                        AgentsScreen(model, Nowhere, AgentsMetadata())
                    }
                }
            }
        }
        fun shot(name: String) {
            for (d in listOf(false, true)) {
                rule.runOnIdle { dark = d }
                rule.waitForIdle()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                File(out(), "hub-${name}-${if (d) "dark" else "light"}.png").outputStream().use {
                    assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
                }
            }
        }
        rule.waitUntil(5_000) { model.page.value.list.counts[AgentQuickFilter.Feishu] == 1 }
        assertTrue(rule.onAllNodesWithContentDescription("隐藏 botmux 会话").fetchSemanticsNodes().isEmpty())
        shot("shown")
        rule.runOnIdle { model.filter(AgentQuickFilter.Feishu) }
        rule.waitUntil(5_000) { model.page.value.settings.quick == AgentQuickFilter.Feishu }
        rule.runOnIdle { show = false; model.refresh() }
        rule.waitUntil(5_000) { model.page.value.sessions.size == 2 && model.page.value.settings.quick == AgentQuickFilter.All }
        rule.waitForIdle()
        assertTrue(rule.onAllNodesWithText("飞书群", useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        assertTrue(rule.onAllNodesWithText("群里问的发布时间").fetchSemanticsNodes().isEmpty())
        assertTrue(rule.onAllNodesWithContentDescription("显示 botmux 会话").fetchSemanticsNodes().isEmpty())
        shot("hidden")
        rule.runOnIdle { large = true }
        shot("hidden-large")
        rule.runOnIdle { show = true; hello.value = SocketHello(2, Connection().lastHello) }
        rule.waitUntil(5_000) { model.page.value.sessions.size == 3 && model.page.value.list.counts[AgentQuickFilter.Feishu] == 1 }
        assertEquals(AgentQuickFilter.All, model.page.value.settings.quick)
    }

    @Test fun sourceTagsUnderTheTime() {
        val model = AgentsViewModel(Repository(sessions, machines), Interactions(), Connection(), Sender(), preferences())
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    AgentsScreen(model, Nowhere, AgentsMetadata())
                }
            }
        }
        rule.waitForIdle()
        for (compact in listOf(false, true)) {
            if (compact) { rule.runOnIdle { model.compact() }; rule.waitForIdle() }
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                rule.runOnIdle { style = s; dark = d }
                rule.waitForIdle()
                // one tag each on the botmux, the herdr and the Orca agent (an Orca terminal is a tab too, #17054)
                assertEquals(1, rule.onAllNodesWithText("飞书群", useUnmergedTree = true).fetchSemanticsNodes()
                    .count { node -> node.config.getOrElseNullable(androidx.compose.ui.semantics.SemanticsActions.OnClick) { null } != null })
                assertTrue(rule.onAllNodesWithText("在标签页中", useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
                val list = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val name = "source-${if (compact) "compact" else "list"}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
                File(out(), name).outputStream().use { assertTrue(list.compress(Bitmap.CompressFormat.PNG, 100, it)) }
            }
        }
    }
}
