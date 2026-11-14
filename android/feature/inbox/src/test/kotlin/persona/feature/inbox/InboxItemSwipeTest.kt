package persona.feature.inbox

import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeLeft
import androidx.compose.ui.test.swipeRight
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxBulkAction
import persona.core.contracts.api.InboxDoneTarget
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** Item-row gesture regressions that remain relevant after conversation rows leave the main list. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class InboxItemSwipeTest {
    @get:Rule val rule = createComposeRule()
    private val owner = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val opened = mutableListOf<NavKey>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { opened += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @After fun stop() { owner.cancel() }

    private fun record(id: String, title: String, preview: String = title) = InboxRecord(
        InboxItem(id, account = "demo", title = title, preview = preview, kind = "dm", tier = "action", version = "v-$id"),
        source = "lark", feedId = "shared-room",
    )

    private class FakeInbox(records: List<InboxRecord>, query: String = "") : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
        override val connectionState = MutableStateFlow(ConnectionState.Online)
        override val preferences = MutableStateFlow(InboxPreferences(query = query, expanded = listOf("action", "fyi", "done")))
        override suspend fun savePreferences(value: InboxPreferences) { preferences.value = value }
        override val conversationInbox = MutableStateFlow<InboxConversations?>(null)
        val doneCalls = mutableListOf<Pair<String, Boolean>>()
        val bulkCalls = mutableListOf<Pair<List<InboxDoneTarget>, InboxBulkAction>>()
        val handoffs = mutableListOf<InboxHandOffRequest>()
        val reads = mutableListOf<String>()
        var failDone = false
        var failRead = false
        var doneGate: CompletableDeferred<Unit>? = null

        private fun apply(ids: Set<String>, done: Boolean) {
            snapshot.value = snapshot.value.copy(records = snapshot.value.records.map { record ->
                if (record.item.id in ids) record.copy(item = record.item.copy(
                    doneAt = if (done) "2026-11-13T00:00:00Z" else null)) else record
            })
        }

        override suspend fun setDone(itemId: String, done: Boolean) {
            doneCalls += itemId to done
            doneGate?.await()
            if (failDone) throw IllegalStateException("Synthetic completion failure")
            apply(setOf(itemId), done)
        }

        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): InboxBulkResult {
            bulkCalls += items to action
            val ids = items.map { it.id }.toSet()
            apply(ids, action != InboxBulkAction.Reopen)
            return InboxBulkResult(changed = items.map { it.id })
        }

        override suspend fun handOff(request: InboxHandOffRequest): SessionKey? {
            handoffs += request
            throw IllegalStateException("Synthetic handoff failure")
        }

        override suspend fun markLatestRead(feedId: String): Boolean {
            reads += feedId
            if (failRead) throw IllegalStateException("Synthetic read failure")
            snapshot.value = snapshot.value.copy(records = snapshot.value.records.map {
                if (it.feedId == feedId) it.copy(read = true) else it
            })
            return true
        }

        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = error("A gesture must never send a reply")
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private fun screen(inbox: FakeInbox) {
        inbox.conversationInbox.value = InboxConversations(inbox, "synthetic-pairing", owner, ReplyComposers(inbox, owner))
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxScreen(inbox, navigator) } }
        rule.waitForIdle()
    }

    private fun row(title: String) = rule.onNode(hasText(title) and hasClickAction())

    private fun handOffFromLongPress(title: String) {
        row(title).performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onNodeWithContentDescription("More").performClick()
        rule.onNodeWithText("Hand to AI").performClick()
        rule.waitForIdle()
    }

    @Test fun aFilteredEndSwipeFilesOnlyTheVisibleItem() {
        val inbox = FakeInbox(listOf(
            record("visible", "Visible matter", "Visible match"),
            record("hidden", "Hidden matter", "Different text"),
        ), query = "Visible")
        screen(inbox)

        row("Visible matter").performTouchInput { swipeLeft() }
        rule.waitForIdle()

        assertEquals(listOf("visible" to true), inbox.doneCalls)
        assertTrue(inbox.snapshot.value.records.single { it.item.id == "visible" }.item.isDone)
        assertFalse(inbox.snapshot.value.records.single { it.item.id == "hidden" }.item.isDone)
        assertTrue(inbox.bulkCalls.isEmpty())
    }

    @Test fun aFailedCompletionRestoresAndRetryUsesOnlyThatItem() {
        val inbox = FakeInbox(listOf(record("one", "One matter"))).apply { failDone = true }
        screen(inbox)
        row("One matter").performTouchInput { swipeLeft() }
        rule.waitForIdle()
        rule.onNodeWithText("previous state is back", substring = true).assertExists()
        assertFalse(inbox.snapshot.value.records.single().item.isDone)

        inbox.failDone = false
        rule.onNodeWithText("Retry").performClick()
        rule.waitForIdle()

        assertEquals(listOf("one"), inbox.bulkCalls.single().first.map { it.id })
        assertEquals(InboxBulkAction.Done, inbox.bulkCalls.single().second)
        assertTrue(inbox.snapshot.value.records.single().item.isDone)
    }

    @Test fun pendingCompletionBlocksRepeatedDoneGesturesAndTheMenuAction() {
        val inbox = FakeInbox(listOf(record("one", "One matter"))).apply { doneGate = CompletableDeferred() }
        screen(inbox)
        row("One matter").performTouchInput { swipeLeft() }
        row("One matter").performTouchInput { swipeLeft() }
        row("One matter").performTouchInput { swipeRight() }

        assertEquals(listOf("one" to true), inbox.doneCalls)
        assertTrue(inbox.handoffs.isEmpty())
        assertTrue(opened.isEmpty())

        row("One matter").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onNodeWithText("Move Back").assertDoesNotExist()
        rule.onNodeWithText("Hand to AI").assertDoesNotExist()

        inbox.doneGate?.complete(Unit)
        rule.waitForIdle()
        assertTrue(inbox.snapshot.value.records.single().item.isDone)
    }

    @Test fun aFilteredLongPressHandsOffOnlyTheVisibleItem() {
        val inbox = FakeInbox(listOf(
            record("visible", "Visible matter", "Visible match"),
            record("hidden", "Hidden matter", "Different text"),
        ), query = "Visible")
        screen(inbox)
        row("Visible matter").performTouchInput { swipeRight() }
        rule.onNodeWithText("What should it do").assertDoesNotExist()
        handOffFromLongPress("Visible matter")
        rule.onNodeWithText("What should it do").assertExists()
        rule.onNodeWithText("Send").performClick()
        rule.waitForIdle()

        assertEquals(listOf("visible"), inbox.handoffs.map { it.itemId })
        assertTrue(inbox.doneCalls.isEmpty())
        assertTrue(inbox.bulkCalls.isEmpty())
        assertTrue(opened.isEmpty())
    }

    @Test fun readAllRetriesThenDisappearsWithoutMovingTheItem() {
        val inbox = FakeInbox(listOf(record("one", "One matter"))).apply { failRead = true }
        screen(inbox)
        rule.onNodeWithText("Mark all as read").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Some messages could not be marked as read. Try again.").assertExists()

        inbox.failRead = false
        rule.onNodeWithText("Retry").performClick()
        rule.waitForIdle()

        assertEquals(listOf("shared-room", "shared-room"), inbox.reads)
        rule.onNodeWithText("Mark all as read").assertDoesNotExist()
        row("One matter").assertExists()
        assertTrue(inbox.snapshot.value.records.single().read)
        assertFalse(inbox.snapshot.value.records.single().item.isDone)
        assertTrue(inbox.doneCalls.isEmpty())
    }
}
