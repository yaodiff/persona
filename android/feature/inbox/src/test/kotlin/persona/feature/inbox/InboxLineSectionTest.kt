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
import persona.core.contracts.InboxHandling
import persona.core.contracts.Session
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.InboxParentRef
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.AppStyle
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentSession
import persona.core.navigation.ChatRoute
import persona.core.navigation.Navigator
import persona.core.navigation.RouteSource
import persona.core.navigation.TodoDetailRoute

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class InboxLineSectionTest {
    @get:Rule val rule = createComposeRule()

    @Test fun rowsPutFarthestAncestorsFirstCapTheHubLineAndUnlinkTheLowerItem() {
        val current = record("current", "Current")
        val ancestors = List(12) { index ->
            InboxLineAncestor(InboxParentRef("inbox", "a$index"), record("a$index", "A$index"))
        }
        val child = record("child", "Child")
        val rows = inboxLineRows(InboxLineage(InboxParentRef("inbox", "current"), current, ancestors, listOf(child)))
        assertEquals(10, rows.size) // nine ancestors and one child, even if malformed hub data loops back
        assertEquals("a8", rows.first().parent.id)
        assertEquals("a7", rows.first().unlink?.hubItemId)
        assertEquals("a0", rows[8].parent.id)
        assertEquals("current", rows[8].unlink?.hubItemId)
        assertEquals("child", rows.last().unlink?.hubItemId)
        assertTrue(rows.last().up.not())
    }

    @Test fun aMissingAncestorUsesWordsNotItsRawIdAndAnItemOpensItsEventPage() {
        val current = record("current", "Current")
        val parent = record("parent", "Parent")
        val repository = FakeRepository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("inbox", "parent"), parent), InboxLineAncestor(InboxParentRef("inbox", "raw-gone")))))
        show(repository, current)
        rule.onNodeWithText("已不在收件箱里", substring = true).assertExists()
        rule.onNodeWithText("raw-gone").assertDoesNotExist()
        rule.onNodeWithText("Parent", substring = true).performClick()
        assertEquals(InboxEventRoute("parent"), opened.last())
    }

    @Test fun unlinkTargetsThePairSLowerItemRefreshesAndShowsTheExactSuccessWords() {
        val current = record("current", "Current")
        val parent = record("parent", "Parent")
        val child = record("child", "Child")
        val repository = FakeRepository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("inbox", "parent"), parent)), listOf(child)))
        show(repository, current)
        repository.line = null
        rule.onAllNodesWithText("不是同一件事")[0].performClick()
        rule.waitForIdle()
        assertEquals("current", repository.parentWrites.single().first.hubItemId)
        assertEquals(null, repository.parentWrites.single().second)
        assertEquals(2, repository.lineCalls)
        assertEquals("已标为不是同一件事", feedback.notices.single().message)
        assertEquals(FeedbackStyle.Success, feedback.notices.single().style)
        rule.onNodeWithText("线索").assertDoesNotExist()
    }

    @Test fun aFailedUnlinkKeepsTheRowsDoesNotRefreshAndUsesTheExistingFailureFeedback() {
        val current = record("current", "Current")
        val parent = record("parent", "Parent")
        val repository = FakeRepository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("inbox", "parent"), parent)))).apply { failWrite = true }
        show(repository, current)
        rule.onNodeWithText("不是同一件事").performClick()
        rule.waitForIdle()
        assertEquals(1, repository.lineCalls)
        assertEquals(1, feedback.failures)
        assertTrue(feedback.notices.isEmpty())
        rule.onNodeWithText("Parent", substring = true).assertExists()
    }

    @Test fun aFailedFirstReadAndAnEmptyLineHaveNoSection() {
        val current = record("current", "Current")
        show(FakeRepository(null).apply { failRead = true }, current)
        rule.onNodeWithText("线索").assertDoesNotExist()
        rule.onNodeWithText("不是同一件事").assertDoesNotExist()
    }

    @Test fun aLineItemPrefersTheSharedNowWordsOverItsLegacyMark() {
        val current = record("current", "Current")
        val parent = record("parent", "Parent").let { old ->
            old.copy(item = old.item.copy(status = "working"),
                event = old.event.copy(now = "queued", handling = InboxHandling(state = "looking")))
        }
        val repository = FakeRepository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("inbox", "parent"), parent))))
        show(repository, current)
        rule.onNodeWithText("排队中，等 AI 看").assertExists()
        rule.onNodeWithText("AI 正在看").assertDoesNotExist()
    }

    @Test fun aTodoRowUsesItsExistingRoute() {
        val current = record("current", "Current")
        val repository = FakeRepository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("todo", "todo-1")))))
        show(repository, current)
        rule.onNodeWithText("待办 待办").assertDoesNotExist()
        rule.onNodeWithText("待办", substring = true).performClick()
        assertEquals(TodoDetailRoute("todo-1"), opened.last())
    }

    @Test fun anUntitledSessionSaysItsKindOnlyOnce() {
        val current = record("current", "Current")
        val repository = FakeRepository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("session", "m1/missing")))))
        show(repository, current)
        rule.onNodeWithText("Agent 会话").assertExists()
        rule.onNodeWithText("Agent 会话 Agent 会话").assertDoesNotExist()
    }

    @Test fun aSessionRowUsesItsLiveTitleAndTheItemSource() {
        val current = record("current", "Current")
        val repository = FakeRepository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("session", "m1/s1")))))
        repository.snapshot.value = repository.snapshot.value.copy(sessions = listOf(AgentSession(Session(machineId = "m1", sessionId = "s1", title = "Review"))))
        show(repository, current)
        rule.onNodeWithText("Review", substring = true).performClick()
        assertEquals(ChatRoute("m1", "s1", source = RouteSource.InboxItem("current")), opened.last())
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

    private val opened = mutableListOf<NavKey>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { opened += key }
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
        var failRead = false
        var failWrite = false
        val parentWrites = mutableListOf<Pair<InboxRecord, InboxParentRef?>>()
        override suspend fun inboxLine(item: InboxRecord): InboxLineage? {
            lineCalls++
            if (failRead) throw IOException("read failed")
            return line
        }
        override suspend fun setInboxParent(item: InboxRecord, parent: InboxParentRef?) {
            parentWrites += item to parent
            if (failWrite) throw IOException("write failed")
        }
    }

    companion object {
        private fun record(id: String, title: String, manager: String? = null) = InboxRecord(
            InboxItem(InboxItemKey.appId(id, manager), title = title, status = "action", updatedAt = "2026-11-13T00:00:00Z"),
            event = InboxEventFields(status = "action"), manager = manager)
    }
}
