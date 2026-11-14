package persona.feature.inbox

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.IOException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.InboxEventFields
import persona.core.contracts.InboxHandling
import persona.core.contracts.InboxSuggestion
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.AppStyle
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.Navigator

/**
 * The state line of the event page after the display package's `now` words joined it (review additions): one row carrying
 * the state, then "Conversation ›", then "Say something" / "Try again". A suggestion keeps the state and conversation
 * but not either instruction action. The pieces themselves (the sheet, the request, the picker) are covered by
 * `InboxSaySomethingTest`.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class InboxStateLineReviewTest {
    @get:Rule val rule = createComposeRule()

    @Test fun theStateProcessingChatAndSaySomethingShareOneOrderedRow() {
        val record = record("current", "Current",
            handling = InboxHandling(state = "looking", machineId = "m1", sessionId = "s1"))
        show(record)
        val state = rule.onNodeWithText("需要你").fetchSemanticsNode().boundsInRoot
        val chat = rule.onNodeWithText("处理对话 ›").fetchSemanticsNode().boundsInRoot
        val say = rule.onNodeWithText("说一句").fetchSemanticsNode().boundsInRoot
        // One row: the three share a band, and the order is state → processing chat → Say something.
        assertTrue("state and chat on one row: $state / $chat", state.top < chat.bottom && chat.top < state.bottom)
        assertTrue("chat and say on one row: $chat / $say", chat.top < say.bottom && say.top < chat.bottom)
        assertTrue("$state / $chat / $say", state.left < chat.left && chat.left < say.left)
    }

    @Test fun theSharedNowWordWinsOverTheLegacyMark() {
        // The hub's shared word wins over the legacy mark: this handling record reads "queued", not "AI is looking".
        val record = record("current", "Current",
            handling = InboxHandling(state = "looking", machineId = "m1", sessionId = "s1"), now = "queued")
        show(record)
        rule.onNodeWithText("排队中，等 AI 看").assertExists()
        rule.onNodeWithText("AI 正在看").assertDoesNotExist()
        rule.onNodeWithText("说一句").assertExists()
    }

    @Test fun aFailureKnownOnlyFromTheSharedWordOffersTheOneTapRetry() {
        // No legacy failed mark at all: now == failed alone must turn the entry into the retry.
        val failed = record("current", "Current", now = "failed")
        val repository = FakeRepository(failed)
        show(failed, repository)
        rule.onNodeWithText("AI 处理失败").assertExists()
        rule.onNodeWithText("再试一次").performClick()
        rule.waitForIdle()
        assertEquals("请再试一次。", repository.requests.single().text)
        assertEquals(SuggestionAction.Instruct, repository.requests.single().action)
    }

    @Test fun aSuggestionKeepsTheSharedStateAndConversationWithoutSayOrRetry() {
        val record = record("current", "Current",
            handling = InboxHandling(state = "looking", machineId = "m1", sessionId = "s1"),
            suggestion = InboxSuggestion(2, "reply", "先确认时间"), now = "confirm_reply")
        show(record)
        rule.onNodeWithText("先确认时间", substring = true).assertExists()
        rule.onNodeWithText("等你确认回复").assertExists()
        rule.onNodeWithText("处理对话 ›").assertExists()
        rule.onNodeWithText("说一句").assertDoesNotExist()
        rule.onNodeWithText("再试一次").assertDoesNotExist()
    }

    @Test fun aDoneItemHasNoStateRowEntry() {
        show(record("current", "Current", done = true))
        rule.onNodeWithText("说一句").assertDoesNotExist()
        rule.onNodeWithText("再试一次").assertDoesNotExist()
    }

    private fun show(record: InboxRecord, repository: FakeRepository = FakeRepository(record)) {
        rule.setContent { Content(repository, record) }
        rule.waitForIdle()
    }

    @Composable
    private fun Content(repository: FakeRepository, record: InboxRecord) {
        PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
            CompositionLocalProvider(LocalAppFeedback provides feedback) {
                InboxEventScreen(repository, InboxEventRoute(record.item.id), navigator)
            }
        }
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: persona.core.navigation.AppTab) = Unit
    }

    private val feedback = object : AppFeedback {
        val notices = mutableListOf<FeedbackNotice>()
        var failures = 0
        override fun failure(error: Throwable, operation: Boolean): Boolean { failures++; return true }
        override fun notice(message: String): Boolean = notice(message, FeedbackStyle.Info)
        override fun notice(message: String, style: FeedbackStyle): Boolean { notices += FeedbackNotice(message, style = style); return true }
    }

    private class FakeRepository(record: InboxRecord) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(record)))
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("unused")
        val requests = mutableListOf<SuggestionRequest>()
        override suspend fun actOnSuggestion(request: SuggestionRequest): SuggestionOutcome {
            requests += request
            return SuggestionOutcome.Done
        }
    }

    companion object {
        private fun record(
            id: String, title: String, handling: InboxHandling? = null, suggestion: InboxSuggestion? = null,
            done: Boolean = false, now: String? = null,
        ) = InboxRecord(
            InboxItem(id, title = title, status = if (done) null else "action", doneAt = if (done) "2026-11-13T09:00:00Z" else null),
            event = InboxEventFields(status = if (done) null else "action", handling = handling, suggestion = suggestion, now = now))
    }
}
