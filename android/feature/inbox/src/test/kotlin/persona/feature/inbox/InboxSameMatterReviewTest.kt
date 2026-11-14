package persona.feature.inbox

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.IOException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
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
 * The two picker entries on the real event page (review additions for batch ②): a link from the More menu must make the
 * Thread section beside it show the new line, and a refusal must leave that section as it was. The picker's own rules
 * and the section's bottom entry are covered by `InboxSameMatterSheetTest`.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class InboxSameMatterReviewTest {
    @get:Rule val rule = createComposeRule()

    @Test fun aLinkFromTheMoreMenuMakesTheThreadSectionShowTheNewLine() {
        val current = record("current", "Current")
        val before = InboxLineage(InboxParentRef("inbox", "current"), current)
        val after = InboxLineage(InboxParentRef("inbox", "current"), current,
            children = listOf(record("alpha", "Alpha")))
        val repository = FakeRepository(listOf(current, record("alpha", "Alpha")), before).apply { nextLine = after }
        show(repository, current)
        rule.onNodeWithText("线索").assertDoesNotExist()
        openPicker()
        rule.onNode(hasText("Alpha", substring = true) and hasClickAction() and !hasSetTextAction()).performClick()
        rule.waitForIdle()
        assertEquals(InboxParentRef("inbox", "alpha"), repository.writes.single().second)
        assertEquals("已连到「Alpha」", feedback.notices.single().message)
        // The section beside the sheet reloaded from the tree the link answered with.
        rule.onNodeWithText("线索").assertExists()
        rule.onAllNodesWithText("Alpha", substring = true)[0].assertExists()
    }

    @Test fun aRefusedLinkLeavesTheThreadSectionUnchangedAndClaimsNothing() {
        val current = record("current", "Current")
        val before = InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("inbox", "parent"), record("parent", "Parent"))))
        val repository = FakeRepository(listOf(current, record("alpha", "Alpha")), before)
            .apply { writeError = CycleFailure() }
        show(repository, current)
        rule.onNodeWithText("Parent", substring = true).assertExists()
        openPicker()
        val reads = repository.lineCalls
        rule.onNode(hasText("Alpha", substring = true) and hasClickAction() and !hasSetTextAction()).performClick()
        rule.waitForIdle()
        // Every 409 from the parent route gets one plain explanation, never the hub's own sentence and never a success.
        assertEquals("这个关联不成立，请选择其他内容。", feedback.notices.single().message)
        assertEquals(FeedbackStyle.Error, feedback.notices.single().style)
        assertEquals(0, feedback.failures)
        // The picker stays open with the choice still offered (another one may work), and the section behind it is
        // still the line it had: no reload was asked for and nothing claims the link happened.
        rule.onNode(hasSetTextAction()).assertExists()
        assertEquals(reads, repository.lineCalls)
        rule.onNodeWithText("Parent", substring = true).assertExists()
        rule.onAllNodesWithText("是同一件事…")[0].assertExists()
    }

    /**
     * The event page's More menu, then "Same matter as…" (the picker reads the tree itself from here). The page may
     * already show the Thread section's own bottom entry with the same words; the menu's is the one drawn lowest,
     * over the page (an action sheet), so the bottom-most node is clicked.
     */
    private fun openPicker() {
        rule.onNodeWithContentDescription("更多").performClick()
        rule.waitForIdle()
        val entries = rule.onAllNodesWithText("是同一件事…")
        val count = entries.fetchSemanticsNodes().size
        val lowest = (0 until count).maxBy { entries[it].fetchSemanticsNode().boundsInRoot.top }
        entries[lowest].performClick()
        rule.waitForIdle()
    }

    private fun show(repository: FakeRepository, current: InboxRecord) {
        rule.setContent { Content(repository, current) }
        rule.waitForIdle()
    }

    @Composable
    private fun Content(repository: FakeRepository, current: InboxRecord) {
        PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
            CompositionLocalProvider(LocalAppFeedback provides feedback) {
                InboxEventScreen(repository, InboxEventRoute(current.item.id), navigator)
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

    private class CycleFailure : IOException("parent_cycle"), HubCodedFailure {
        override val status: Int? = 409
        override val code: String? = "parent_cycle"
    }

    private class FakeRepository(records: List<InboxRecord>, private var line: InboxLineage) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
        /** What the tree answers after a successful link, as the hub would. */
        var nextLine: InboxLineage? = null
        var writeError: Exception? = null
        val writes = mutableListOf<Pair<InboxRecord, InboxParentRef?>>()
        /** How many times a tree was read: the picker's lazy read and the section's own (the refused write adds none). */
        var lineCalls = 0
        override suspend fun refresh() = Unit
        override suspend fun inboxLine(item: InboxRecord): InboxLineage { lineCalls++; return line }
        override suspend fun setInboxParent(item: InboxRecord, parent: InboxParentRef?) {
            writes += item to parent
            writeError?.let { throw it }
            nextLine?.let { line = it }
        }
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("unused")
    }

    companion object {
        private fun record(id: String, title: String) = InboxRecord(
            InboxItem(id, title = title, status = "action", updatedAt = "2026-11-13T00:00:00Z"),
            event = InboxEventFields(status = "action"))
    }
}
