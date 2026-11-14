package persona.feature.inbox

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
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
import persona.core.contracts.api.HubCodedFailure
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.InboxParentRef
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.AppStyle
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.Navigator

/**
 * What batch ① of the Thread area must not get wrong (review additions): a child's own "Not the same matter" unlinks
 * the child, and a hub refusal (409 `parent_cycle`, `parent_fit` …) keeps the rows and reports the existing failure
 * instead of a success toast. The hub bodies themselves are pinned in `InboxLineageTransportTest` (core:hub).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class InboxLineageReviewTest {
    @get:Rule val rule = createComposeRule()

    @Test fun aChildRowsNotTheSameMatterUnlinksTheChildItself() {
        val current = record("current", "Current")
        val child = record("child", "Child", manager = "m2")
        val repository = FakeRepository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("inbox", "parent"), record("parent", "Parent"))), listOf(child)))
        show(repository, current)
        // The up row comes first (it unlinks this item); the child's row below it unlinks the child.
        rule.onAllNodesWithText("不是同一件事")[1].performClick()
        rule.waitForIdle()
        val write = repository.parentWrites.single()
        assertEquals("child", write.first.hubItemId)
        assertTrue("the child's own app id, not the parent's", write.first.item.id != current.item.id)
        assertEquals(null, write.second)
        assertEquals("已标为不是同一件事", feedback.notices.single().message)
    }

    @Test fun aRefusedUnlinkKeepsTheRowsAndReportsTheExistingFailure() {
        val current = record("current", "Current")
        val child = record("child", "Child")
        val repository = FakeRepository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("inbox", "parent"), record("parent", "Parent"))), listOf(child)))
            .apply { writeFailure = CycleFailure() }
        show(repository, current)
        rule.onAllNodesWithText("不是同一件事")[0].performClick()
        rule.waitForIdle()
        assertEquals(1, repository.lineCalls)
        assertEquals(1, feedback.failures)
        assertTrue("a refused write must not claim success", feedback.notices.isEmpty())
        // The rows are still there and the buttons work again (the next attempt is possible).
        rule.onAllNodesWithText("不是同一件事")[0].performClick()
        rule.waitForIdle()
        assertEquals(2, repository.parentWrites.size)
    }

    private fun show(repository: FakeRepository, current: InboxRecord) {
        rule.setContent { Content(repository, current) }
        rule.waitForIdle()
    }

    @Composable
    private fun Content(repository: FakeRepository, current: InboxRecord) {
        PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
            CompositionLocalProvider(LocalAppFeedback provides feedback) {
                InboxLineSection(repository, current, navigator)
            }
        }
    }

    /** What a hub answers with when it refuses a parent: the transport's failure carries the status and the code. */
    private class CycleFailure : IOException("parent_cycle"), HubCodedFailure {
        override val status: Int? = 409
        override val code: String? = "parent_cycle"
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

    private class FakeRepository(var line: InboxLineage?) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot())
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("unused")
        var lineCalls = 0
        var writeFailure: Exception? = null
        val parentWrites = mutableListOf<Pair<InboxRecord, InboxParentRef?>>()
        override suspend fun inboxLine(item: InboxRecord): InboxLineage? {
            lineCalls++
            return line
        }
        override suspend fun setInboxParent(item: InboxRecord, parent: InboxParentRef?) {
            parentWrites += item to parent
            writeFailure?.let { throw it }
        }
    }

    companion object {
        private fun record(id: String, title: String, manager: String? = null) = InboxRecord(
            InboxItem(InboxItemKey.appId(id, manager), title = title, status = "action", updatedAt = "2026-11-13T00:00:00Z"),
            event = InboxEventFields(status = "action"), manager = manager)
    }
}
