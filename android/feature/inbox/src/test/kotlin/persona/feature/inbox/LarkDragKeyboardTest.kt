package persona.feature.inbox

import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.click
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeDown
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * Dragging a Lark chat puts the keyboard away (#4210, iOS `ChatView` does it for its conversations): the chat draws the
 * list the Agent chat and Persona's conversation draw (`ChatDragKeyboardTest`), and its reply box loses the keyboard.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class LarkDragKeyboardTest {
    @get:Rule val rule = createComposeRule()
    private val probe = KeyboardProbe()

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private val messages = (1..40).map {
        LarkMessage("m$it", "u${it % 2}", "Alex Example", position = it.toLong(), time = "2026-11-04T09:%02d:00Z".format(it),
            parts = listOf(LarkPart.Text("Message number $it")))
    }

    private class Chat(private val messages: List<LarkMessage>) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(
            InboxRecord(InboxItem("lark:f1:40", kind = "group", title = "Launch team"), source = "lark", feedId = "f1"))))
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) =
            flowOf(LarkConversation(title = "Launch team", messages = messages, verified = true))
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    @Test fun draggingALarkChatPutsTheKeyboardAway() {
        val chat = Chat(messages)
        rule.setContent {
            PersonaTheme(reduceMotion = true) { probe.Around { LarkScreen(chat, LarkRoute("f1", "lark:f1:40", 40), navigator) } }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithText("Message number 38").fetchSemanticsNodes().isNotEmpty() }
        rule.waitForIdle()
        rule.onNode(hasSetTextAction()).performTouchInput { click() }
        rule.waitForIdle()
        probe.reset()
        rule.onNodeWithText("Message number 38").performTouchInput { swipeDown() }
        rule.waitForIdle()
        assertTrue("a drag asks the focus to clear: ${probe.focusClears}", probe.focusClears.isNotEmpty() && probe.focusClears.none { it })
        assertTrue("and the keyboard to hide", probe.hides > 0)
    }
}
