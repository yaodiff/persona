package persona.feature.inbox

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
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
import persona.core.contracts.api.HubCodedFailure
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.AppStyle
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import androidx.lifecycle.compose.collectAsStateWithLifecycle

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class InboxSaySomethingTest {
    @get:Rule val rule = createComposeRule()

    @Test fun saySomethingSendsVersionZeroTrimsWordsAndClosesAfterSuccess() {
        val repository = FakeRepository(record("current", "Current"))
        show(repository)
        rule.onNodeWithText("说一句").performClick()
        rule.onNode(hasSetTextAction()).performTextReplacement(" 先问时间 ")
        rule.onNodeWithText("告诉 AI").performClick()
        rule.waitForIdle()
        val request = repository.requests.single()
        assertEquals("current", request.itemId)
        assertEquals(0L, request.version)
        assertEquals(SuggestionAction.Instruct, request.action)
        assertEquals("先问时间", request.text)
        assertTrue(request.clientId.isNotBlank())
        assertEquals("已交给 AI，等它重新出建议", feedback.notices.single().message)
        assertEquals(FeedbackStyle.Success, feedback.notices.single().style)
        rule.onNodeWithText("告诉 AI").assertDoesNotExist()
    }

    @Test fun failedHandlingOffersRetryWithTheExactLocalizedDefaultInstruction() {
        val repository = FakeRepository(record("current", "Current", handling = InboxHandling(state = "failed", error = "timeout")))
        show(repository)
        rule.onNodeWithText("AI 没处理完：timeout", substring = true).assertExists()
        rule.onNodeWithText("再试一次").performClick()
        rule.waitForIdle()
        assertEquals("请再试一次。", repository.requests.single().text)
        assertEquals(0L, repository.requests.single().version)
        assertEquals(SuggestionAction.Instruct, repository.requests.single().action)
    }

    @Test fun aFailureKeepsTheWordsAndRetriesTheSameAnswerWithTheSameClientId() {
        val repository = FakeRepository(record("current", "Current"))
        var calls = 0
        repository.answer = {
            if (calls++ == 0) throw IOException("offline") else SuggestionOutcome.Done
        }
        show(repository)
        rule.onNodeWithText("说一句").performClick()
        rule.onNode(hasSetTextAction()).performTextReplacement("先问时间")
        rule.onNodeWithText("告诉 AI").performClick()
        rule.waitForIdle()
        assertEquals(1, feedback.failures)
        rule.onNode(hasSetTextAction()).assertExists()
        rule.onNodeWithText("告诉 AI").performClick()
        rule.waitForIdle()
        assertEquals(2, repository.requests.size)
        assertEquals(repository.requests[0].clientId, repository.requests[1].clientId)
        assertEquals("先问时间", repository.requests[1].text)
    }

    @Test fun aStaleAnswerWithAnItemUsesItWithoutAnotherListRead() {
        val repository = FakeRepository(record("current", "Current", handling = InboxHandling(state = "failed", error = "timeout")))
        repository.answer = {
            val current = repository.snapshot.value.records.single()
            val updated = current.copy(event = current.event.copy(suggestion = InboxSuggestion(3, "reply", "好")))
            repository.snapshot.value = repository.snapshot.value.copy(records = listOf(updated))
            SuggestionOutcome.Stale
        }
        show(repository)
        rule.onNodeWithText("再试一次").performClick()
        rule.waitForIdle()
        assertEquals(0, repository.refreshCalls)
        assertEquals(true, staleReports.last())
        assertEquals("建议已更新", feedback.notices.single().message)
        assertTrue(feedback.notices.none { it.style == FeedbackStyle.Success })
        rule.onNodeWithText("再试一次").assertDoesNotExist()
    }

    @Test fun aStaleAnswerWithoutAnItemRefreshes() {
        val repository = FakeRepository(record("current", "Current", handling = InboxHandling(state = "failed", error = "timeout")))
        repository.answer = { SuggestionOutcome.Stale }
        show(repository)
        rule.onNodeWithText("再试一次").performClick()
        rule.waitForIdle()
        assertEquals(1, repository.refreshCalls)
        assertEquals(true, staleReports.last())
    }

    @Test fun anAnswerOnlyFailureUsesExistingFeedbackAndHidesTheEntry() {
        val repository = FakeRepository(record("current", "Current"))
        repository.answer = { throw CodedFailure(403, "answer_only") }
        show(repository)
        rule.onNodeWithText("说一句").performClick()
        rule.onNode(hasSetTextAction()).performTextReplacement("先问时间")
        rule.onNodeWithText("告诉 AI").performClick()
        rule.waitForIdle()
        assertEquals(1, feedback.failures)
        assertTrue(feedback.notices.isEmpty())
        rule.onNodeWithText("说一句").assertDoesNotExist()
        rule.onNodeWithText("告诉 AI").assertDoesNotExist()
    }

    @Test fun aFailedNowKeyWithoutAHandlingRecordAlsoOffersRetry() {
        val repository = FakeRepository(record("current", "Current", now = "failed"))
        show(repository)
        rule.onNodeWithText("再试一次").performClick()
        rule.waitForIdle()
        assertEquals("请再试一次。", repository.requests.single().text)
        rule.onNodeWithText("说一句").assertDoesNotExist()
    }

    @Test fun aSuggestionCardHasNoSeparateSayOrRetryEntry() {
        show(FakeRepository(record("current", "Current", suggestion = InboxSuggestion(2, "reply", "好"))))
        rule.onNodeWithText("说一句").assertDoesNotExist()
        rule.onNodeWithText("再试一次").assertDoesNotExist()
    }

    @Test fun aDoneItemHasNoInstructionEntry() {
        show(FakeRepository(record("current", "Current", done = true)))
        rule.onNodeWithText("说一句").assertDoesNotExist()
        rule.onNodeWithText("再试一次").assertDoesNotExist()
    }

    private val staleReports = mutableListOf<Boolean>()

    private fun show(repository: FakeRepository) {
        rule.setContent {
            val snapshot by repository.snapshot.collectAsStateWithLifecycle()
            val record = snapshot.records.single()
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalAppFeedback provides feedback) {
                    InboxSaySomething(repository, record, eventMark(record)?.takeIf { it !is EventMark.Suggests }, onStale = { staleReports += it })
                }
            }
        }
        rule.waitForIdle()
    }

    private val feedback = object : AppFeedback {
        val notices = mutableListOf<FeedbackNotice>()
        var failures = 0
        override fun failure(error: Throwable, operation: Boolean): Boolean { failures++; return true }
        override fun notice(message: String): Boolean = notice(message, FeedbackStyle.Info)
        override fun notice(message: String, style: FeedbackStyle): Boolean { notices += FeedbackNotice(message, style = style); return true }
    }

    private class CodedFailure(override val status: Int, override val code: String) : IOException("refused"), HubCodedFailure

    private class FakeRepository(record: InboxRecord) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(record)))
        var refreshCalls = 0
        override suspend fun refresh() { refreshCalls++ }
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
        private fun record(id: String, title: String, handling: InboxHandling? = null, suggestion: InboxSuggestion? = null, done: Boolean = false, now: String? = null) =
            InboxRecord(InboxItem(id, title = title, status = if (done) null else "action", doneAt = if (done) "2026-11-13T09:00:00Z" else null),
                event = InboxEventFields(status = if (done) null else "action", handling = handling, suggestion = suggestion, now = now))
    }
}
