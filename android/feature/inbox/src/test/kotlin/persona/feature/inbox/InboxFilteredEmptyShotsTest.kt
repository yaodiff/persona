package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
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

/**
 * A source or kind filter that leaves nothing open says so, with the filter mark and Clear filters, instead of the
 * check and "All caught up" (#7550). The bay with nothing, and an inbox that has never had an item, stay as they were.
 * Shots: Glass, Brief and Board, light and dark, in `build/inbox-filtered-empty-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class InboxFilteredEmptyShotsTest {
    @get:Rule val rule = createComposeRule()

    private val open = InboxRecord(InboxItem(
        "mail:1", account = "mail-1", kind = "mail", title = "Weekly report", sender = "Example",
        preview = "The plan is attached.", tier = "action", updatedAt = "2026-11-04T09:30:00Z",
    ), sourceLabel = "Mail", source = "mail")

    private val fyi = InboxRecord(InboxItem(
        "mail:2", account = "mail-1", kind = "mail", title = "Office note", sender = "Example",
        preview = "The lobby closes early.", tier = "fyi", updatedAt = "2026-11-04T08:00:00Z",
    ), sourceLabel = "Mail", source = "mail")

    private class OneInbox(records: List<InboxRecord>, prefs: InboxPreferences = InboxPreferences()) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
        override val connectionState = flowOf(ConnectionState.Online)
        override val preferences = flowOf(prefs)
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

    private fun show(records: List<InboxRecord>, prefs: InboxPreferences = InboxPreferences()) {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxScreen(OneInbox(records, prefs), navigator)
                }
            }
        }
        rule.waitForIdle()
    }

    private fun see(vararg texts: String) = texts.forEach { assertTrue(it, rule.onAllNodesWithText(it).fetchSemanticsNodes().isNotEmpty()) }
    private fun gone(vararg texts: String) = texts.forEach { assertTrue(it, rule.onAllNodesWithText(it).fetchSemanticsNodes().isEmpty()) }

    @Test fun aFilterThatHidesOpenItemsSaysSoAndClearsBack() {
        show(listOf(open), InboxPreferences(source = "mail-1", kind = "group"))
        see("Nothing open matches these filters", "Clear the filters to see the whole Inbox.", "Clear filters", "1 to answer · Persona is on 0")
        gone("All caught up", "Weekly report")
        rule.onNodeWithText("Clear filters").performClick()
        rule.waitForIdle()
        see("Weekly report")
        gone("Nothing open matches these filters", "All caught up")
    }

    @Test fun aBayWithNothingStillSaysAllCaughtUp() {
        show(listOf(fyi), InboxPreferences(section = "action"))
        see("All caught up", "Nothing under this filter.")
        gone("Nothing open matches these filters", "Clear filters")
    }

    @Test fun anInboxWithNoItemsStillSaysItIsEmpty() {
        show(emptyList())
        see("Your inbox is empty")
        gone("Nothing open matches these filters", "All caught up")
    }

    @Test fun drawsTheFilteredEmptyStateInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxScreen(OneInbox(listOf(open), InboxPreferences(source = "mail-1", kind = "group")), navigator)
                }
            }
        }
        val dir = File(System.getProperty("persona.shots") ?: "build/inbox-filtered-empty-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s
            dark = d
            rule.waitForIdle()
            rule.onNodeWithText("Nothing open matches these filters").assertExists()
            gone("All caught up")
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val background = bitmap.getPixel(1, 1)
            var other = 0
            for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
            assertTrue("filtered-empty-${s.key} drew nothing", other > 100)
            File(dir, "filtered-empty-${s.key}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
    }
}
