package persona.feature.inbox

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.IOException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.InboxEventFields
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.InboxParentRef
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.AppStyle
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.Navigator

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class InboxSameMatterSheetTest {
    @get:Rule val rule = createComposeRule()

    @Test fun candidatesKeepOnlyOpenSameManagerChoicesNewestFirstAndSearchTitleAndWhere() {
        val current = record("current", "Current", manager = "m2", at = "2026-11-13T00:00:00Z")
        val child = record("child", "Child", manager = "m2", at = "2026-11-13T08:00:00Z")
        val grandchild = record("grandchild", "Grandchild", manager = "m2", at = "2026-11-13T01:00:00Z")
        val done = record("done", "Done", manager = "m2", done = true)
        val other = record("other", "Other", manager = "m1")
        val newer = record("newer", "Alpha", manager = "m2", at = "2026-11-13T07:00:00Z")
        val older = record("older", "Beta", manager = "m2", at = "2026-11-12T07:00:00Z")
        val place = record("place", "Gamma", manager = "m2", at = "2026-11-13T06:00:00Z", where = "Warehouse")
        val belowDone = record("below-done", "Below done", manager = "m2", at = "2026-11-13T02:00:00Z")
        val records = listOf(current, child, grandchild, done, other, newer, older, place, belowDone)
        val descendants = listOf(child, grandchild, done, belowDone)
        val all = sameMatterCandidates(records, current, descendants, "")
        assertEquals(listOf("newer", "place", "older"), all.map { it.hubItemId })
        assertEquals(listOf("newer"), sameMatterCandidates(records, current, descendants, "alpha").map { it.hubItemId })
        assertEquals(listOf("place"), sameMatterCandidates(records, current, descendants, "WAREHOUSE").map { it.hubItemId })
        assertTrue("all descendants are excluded, including those below a done item", all.none { it.hubItemId in setOf("grandchild", "below-done") })
    }

    @Test fun descendantsIncludeDoneIntermediateItemsAndStopAtEightWithVisited() = runTest {
        val nodes = (0..10).map { index -> record("n$index", "N$index", done = index == 3) }
        val root = InboxLineage(InboxParentRef("inbox", "n0"), nodes[0], children = listOf(nodes[1]))
        val children = (1..9).associate { index -> "n$index" to listOf(nodes[index + 1]) }.toMutableMap()
        children["n2"] = listOf(nodes[3], nodes[0]) // A malformed cycle back to the root must not loop forever.
        children["n4"] = listOf(nodes[5], nodes[1]) // A repeated child is read once too.
        val repository = FakeRepository(nodes, root, children)
        val descendants = inboxSameMatterDescendants(repository, root)
        assertEquals((1..8).map { "n$it" }, descendants.map { it.hubItemId })
        assertTrue("the done intermediate stays in the chain", "n3" in descendants.map { it.hubItemId })
        assertFalse("depth nine is outside the server's cap", "n9" in descendants.map { it.hubItemId })
        assertEquals(7, repository.lineCalls)
    }

    @Test fun everyParentConflictUsesOneExplanationRegardlessOfItsMessage() {
        for (message in listOf("that parent would make a cycle", "that parent would be too deep", "这个关联会形成循环，请选择其他内容。")) {
            assertTrue(message, isInboxParentConflict(HubFailure(HubError.Server, message, status = 409, code = "conflict")))
        }
        assertFalse(isInboxParentConflict(HubFailure(HubError.Server, "that parent would make a cycle", status = 500, code = "conflict")))
    }

    @Test fun pickerFiltersSearchesAndLinksTheRawParentWithoutAConfirmation() {
        val current = record("current", "Current", manager = "m2")
        val child = record("child", "Child", manager = "m2")
        val alpha = record("alpha", "Alpha", manager = "m2", sender = "Alice", preview = "Latest", at = "2026-11-13T07:00:00Z", where = "Design")
        val beta = record("beta", "Beta", manager = "m2", at = "2026-11-12T07:00:00Z")
        val done = record("done", "Done", manager = "m2", done = true)
        val tree = InboxLineage(InboxParentRef("inbox", "current"), current, children = listOf(child))
        val repository = FakeRepository(listOf(current, child, alpha, beta, done), tree)
        var dismisses = 0
        var show by mutableStateOf(true)
        val linked = mutableListOf<InboxLineage?>()
        rule.setContent {
            if (show) Sheet(repository, current, tree, { dismisses++; show = false }, { linked += it })
        }
        rule.onNodeWithText("Current").assertDoesNotExist()
        rule.onNodeWithText("Child").assertDoesNotExist()
        rule.onNodeWithText("Done").assertDoesNotExist()
        rule.onNodeWithText("Design · Alice：Latest").assertExists()
        rule.onNode(hasSetTextAction()).performTextReplacement("Beta")
        rule.onNodeWithText("Alpha").assertDoesNotExist()
        rule.onNode(hasText("Beta", substring = true) and hasClickAction() and !hasSetTextAction()).assertExists()
        rule.onNode(hasSetTextAction()).performTextReplacement("Alpha")
        rule.onNode(hasText("Alpha", substring = true) and hasClickAction() and !hasSetTextAction()).performClick()
        rule.waitForIdle()
        assertEquals(current.item.id, repository.writes.single().first.item.id)
        assertEquals(InboxParentRef("inbox", "alpha"), repository.writes.single().second)
        assertEquals(1, dismisses)
        assertEquals(1, linked.size)
        assertEquals("已连到「Alpha」", feedback.notices.single().message)
        assertEquals(FeedbackStyle.Success, feedback.notices.single().style)
        rule.onNodeWithText("是同一件事…").assertDoesNotExist()
    }

    @Test fun aParentConflictKeepsThePickerItsQueryAndShowsTheLocalizedExplanation() {
        val current = record("current", "Current")
        val alpha = record("alpha", "Alpha")
        val tree = InboxLineage(InboxParentRef("inbox", "current"), current)
        val repository = FakeRepository(listOf(current, alpha), tree).apply {
            writeError = HubFailure(HubError.Server, "这个关联不成立，请选择其他内容。", status = 409, code = "conflict")
        }
        var dismisses = 0
        rule.setContent { Sheet(repository, current, tree, { dismisses++ }, {}) }
        rule.onNode(hasSetTextAction()).performTextReplacement("Alpha")
        rule.onNode(hasText("Alpha", substring = true) and hasClickAction() and !hasSetTextAction()).performClick()
        rule.waitForIdle()
        assertEquals(0, feedback.failures)
        assertEquals("这个关联不成立，请选择其他内容。", feedback.notices.single().message)
        assertEquals(FeedbackStyle.Error, feedback.notices.single().style)
        assertEquals(0, dismisses)
        rule.onNodeWithText("that parent would make a cycle").assertDoesNotExist()
        rule.onNodeWithText("是同一件事…").assertExists()
        rule.onNode(hasText("Alpha", substring = true) and hasClickAction() and !hasSetTextAction()).assertExists()
    }

    @Test fun aFailedTreeReadCanBeRetried() {
        val current = record("current", "Current")
        val alpha = record("alpha", "Alpha")
        val tree = InboxLineage(InboxParentRef("inbox", "current"), current)
        val repository = FakeRepository(listOf(current, alpha), tree).apply { failRead = true }
        // Use no already-loaded tree, as the More menu does.
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalAppFeedback provides feedback) {
                    InboxSameMatterSheet(repository, current, initialTree = null, onDismiss = {}, onLinked = {})
                }
            }
        }
        repository.failRead = false
        rule.onNodeWithText("重试").performClick()
        rule.onNode(hasText("Alpha", substring = true) and hasClickAction() and !hasSetTextAction()).assertExists()
        assertEquals(2, repository.lineCalls)
    }

    @Test fun anOldHubWithoutTheTreeRouteDegradesToNoKnownDescendantsWithoutRetry() {
        val current = record("current", "Current")
        val alpha = record("alpha", "Alpha")
        val tree = InboxLineage(InboxParentRef("inbox", "current"), current)
        val repository = FakeRepository(listOf(current, alpha), tree).apply { nullRead = true }
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalAppFeedback provides feedback) {
                    InboxSameMatterSheet(repository, current, initialTree = null, onDismiss = {}, onLinked = {})
                }
            }
        }
        rule.onNode(hasText("Alpha", substring = true) and hasClickAction() and !hasSetTextAction()).assertExists()
        rule.onNodeWithText("重试").assertDoesNotExist()
    }

    @Test fun moreMenuLoadsThePickerOnlyWhenItIsOpened() {
        val current = record("current", "Current")
        val repository = FakeRepository(listOf(current), InboxLineage(InboxParentRef("inbox", "current"), current))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalAppFeedback provides feedback) {
                    InboxSameMatterMenu(repository, current, onLinked = {})
                }
            }
        }
        assertEquals(0, repository.lineCalls)
        rule.onNodeWithContentDescription("更多").performClick()
        rule.onNodeWithText("是同一件事…").performClick()
        rule.waitForIdle()
        assertEquals(1, repository.lineCalls)
        rule.onNode(hasSetTextAction()).assertExists()
    }

    @Test fun theSectionsBottomEntryReusesTheRootAndReadsEachDescendantOnce() {
        val current = record("current", "Current")
        val parent = record("parent", "Parent")
        val repository = FakeRepository(listOf(current), InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("inbox", "parent"), parent)), children = listOf(parent)))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalAppFeedback provides feedback) {
                    InboxLineSection(repository, current, navigator)
                }
            }
        }
        rule.onNodeWithText("是同一件事…").performClick()
        rule.onNode(hasSetTextAction()).assertExists()
        assertEquals(2, repository.lineCalls) // the root, then its direct child's descendants, still only after opening
    }

    @Composable
    private fun Sheet(repository: FakeRepository, current: InboxRecord, tree: InboxLineage, dismiss: () -> Unit, linked: (InboxLineage?) -> Unit) {
        PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
            CompositionLocalProvider(LocalAppFeedback provides feedback) {
                InboxSameMatterSheet(repository, current, tree, dismiss, linked)
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

    private class FakeRepository(
        records: List<InboxRecord>,
        private val line: InboxLineage,
        private val childrenById: Map<String, List<InboxRecord>> = emptyMap(),
    ) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
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
        var nullRead = false
        var writeError: Exception? = null
        val writes = mutableListOf<Pair<InboxRecord, InboxParentRef?>>()
        override suspend fun inboxLine(item: InboxRecord): InboxLineage? {
            lineCalls++
            if (failRead) throw IOException("read failed")
            if (nullRead) return null
            if (item.hubItemId == line.node.id) return line
            return InboxLineage(InboxParentRef("inbox", item.hubItemId), item, children = childrenById[item.hubItemId].orEmpty())
        }
        override suspend fun setInboxParent(item: InboxRecord, parent: InboxParentRef?) {
            writes += item to parent
            writeError?.let { throw it }
        }
    }

    companion object {
        private fun record(id: String, title: String, manager: String? = null, sender: String? = null, preview: String = "", at: String? = null, done: Boolean = false, where: String? = null) =
            InboxRecord(InboxItem(InboxItemKey.appId(id, manager), title = title, sender = sender, preview = preview,
                updatedAt = at, doneAt = if (done) "2026-11-13T09:00:00Z" else null, status = if (done) null else "action", where = where),
                event = InboxEventFields(status = if (done) null else "action", where = where), manager = manager)
    }
}
