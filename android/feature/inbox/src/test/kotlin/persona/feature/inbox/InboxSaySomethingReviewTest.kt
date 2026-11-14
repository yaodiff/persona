package persona.feature.inbox

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
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
 * The instruction entry where the decisions put it: on the real event page, above the stale notice and the suggestion
 * card (review additions for batch ③). It measures what only the page shows — the entry and the card never both offer
 * an instruction, a stale answer keeps the page's "Suggestion updated" over the new card, and instructing speaks for the
 * item's manager. The entry's own states are covered by `InboxSaySomethingTest`.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class InboxSaySomethingReviewTest {
    @get:Rule val rule = createComposeRule()

    @Test fun aStaleAnswerFromThePageKeepsItsSuggestionUpdatedNoticeOverTheNewCard() {
        val repository = FakeRepository(record("current", "Current", handling = InboxHandling(state = "failed", error = "timeout")))
        repository.answer = {
            val current = repository.snapshot.value.records.single()
            repository.snapshot.value = repository.snapshot.value.copy(records = listOf(
                current.copy(event = current.event.copy(
                    suggestion = InboxSuggestion(3, "reply", "先确认时间")))))
            SuggestionOutcome.Stale
        }
        show(repository)
        rule.onNodeWithText("再试一次").performClick()
        rule.waitForIdle()
        // The page's own notice (event-stale) is over the card the hub answered with.
        rule.onNodeWithText("建议已更新").assertExists()
        rule.onNodeWithText("先确认时间", substring = true).assertExists()
        // With a suggestion back, the line's instruction entry is gone; the card's own buttons are the way in.
        rule.onNodeWithText("再试一次").assertDoesNotExist()
        rule.onNodeWithText("说一句").assertDoesNotExist()
        assertEquals(FeedbackStyle.Info, feedback.notices.single().style)
    }

    @Test fun theSheetOpensFromThePageAndCancellingSendsNothing() {
        val repository = FakeRepository(record("current", "Current"))
        show(repository)
        rule.onNodeWithText("说一句").performClick()
        rule.onNode(hasSetTextAction()).assertExists()
        rule.onNodeWithText("告诉 AI").assertExists()
        rule.onNodeWithText("取消").performClick()
        rule.waitForIdle()
        rule.onNode(hasSetTextAction()).assertDoesNotExist()
        assertTrue(repository.requests.isEmpty())
    }

    private fun show(repository: FakeRepository) {
        rule.setContent { Content(repository) }
        rule.waitForIdle()
    }

    @Composable
    private fun Content(repository: FakeRepository) {
        PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
            CompositionLocalProvider(LocalAppFeedback provides feedback) {
                InboxEventScreen(repository, InboxEventRoute("current"), navigator)
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
        var answer: (SuggestionRequest) -> SuggestionOutcome = { SuggestionOutcome.Done }
        override suspend fun actOnSuggestion(request: SuggestionRequest): SuggestionOutcome {
            requests += request
            return answer(request)
        }
    }

    companion object {
        private fun record(id: String, title: String, handling: InboxHandling? = null) = InboxRecord(
            InboxItem(id, title = title, status = "action", updatedAt = "2026-11-13T00:00:00Z"),
            event = InboxEventFields(status = "action", handling = handling))
    }
}
