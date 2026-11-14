package persona.feature.inbox

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
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/** Inbox headers in every style, light and dark, at ordinary and 200% text. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class InboxSectionHeaderShotsTest {
    @get:Rule val rule = createComposeRule()
    private val owner = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @After fun stop() { owner.cancel() }

    private class HeaderInbox(records: List<InboxRecord>) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
        override val connectionState = MutableStateFlow(ConnectionState.Online)
        override val preferences = MutableStateFlow(InboxPreferences(expanded = listOf("action", "fyi", "noise", "done")))
        override val conversationInbox = MutableStateFlow<InboxConversations?>(null)
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

    private fun item(id: String, title: String) = InboxRecord(
        InboxItem(id, account = "chat", kind = "group", title = title, sender = "Example Person",
            preview = "A preview", tier = "action", updatedAt = "2026-11-13T10:00:00Z", version = "v-$id"),
        source = "lark", sourceLabel = "Chat connector", feedId = "shared-room", isGroup = true, groupName = "Planning room",
    )

    @Test fun headersKeepTheirTitlesInEveryAppearance() {
        val inbox = HeaderInbox(listOf(item("first", "First event"), item("second", "Second event")))
        inbox.conversationInbox.value = InboxConversations(inbox, "synthetic-pairing", owner, ReplyComposers(inbox, owner))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1000.dp).background(PersonaTheme.palette.background)) {
                        InboxScreen(inbox, navigator)
                    }
                }
            }
        }
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) for (scale in listOf(1f, 2f)) {
            rule.runOnIdle { style = appearance; dark = night; fontScale = scale }
            rule.waitForIdle()
            rule.onAllNodes(hasText("Needs you") and SemanticsMatcher.keyIsDefined(SemanticsProperties.Heading)).assertCountEquals(1)
            if (scale == 1f) rule.onNodeWithText("Mark all as read").assertExists()
            else rule.onNodeWithTag("inbox.section.overflow").assertExists()
            capture("${appearance.name.lowercase()}-${if (night) "dark" else "light"}-${scale}x")
        }
        rule.runOnIdle { style = AppStyle.Brief; dark = false; fontScale = 2f }
        rule.onNodeWithTag("inbox.section.overflow").performClick()
        rule.onNodeWithText("Mark all as read").assertExists()
        rule.onNodeWithText("Collapse").assertExists()
    }

    private fun capture(name: String) {
        val dir = File("build/inbox-section-header-shots").apply { mkdirs() }
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(dir, "$name.png").outputStream().use { assertTrue(bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it)) }
    }
}
