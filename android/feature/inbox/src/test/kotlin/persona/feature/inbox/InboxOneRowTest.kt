package persona.feature.inbox

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeLeft
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
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
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxEventFields
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxBulkAction
import persona.core.contracts.api.InboxDoneTarget
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * One item, one row (the Inbox's first step of "展示"): a chat's items are listed as their own rows like mail's, the
 * hub's sections stand as they are (Needs you · In progress · FYI · Collapsed · Done), and a row's gestures, menu and
 * selection act on that one item only — never on the other open items of the same source conversation.
 *
 * These run the production [InboxScreen] against a real [InboxConversations] controller, so they hold whatever the list
 * is built from, as long as the behaviour a person sees stays the same.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi")
class InboxOneRowTest {
    @get:Rule val rule = createComposeRule()
    private val owner = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val opened = mutableListOf<NavKey>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { opened += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
    private val res get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources

    @After fun stop() { owner.cancel() }

    /** Two open items of one chat: one row each, with its own title and preview. */
    private fun item(id: String, title: String, preview: String, tier: String = "action", status: String? = null,
        feedId: String? = "shared-room", event: InboxEventFields = InboxEventFields.None, doneAt: String? = null,
        agent: InboxAgent? = null) = InboxRecord(
        InboxItem(id, account = "feishu", kind = "dm", title = title, sender = "Example Person", preview = preview,
            summary = null, tier = tier, updatedAt = "2026-11-12T10:00:00Z", doneAt = doneAt, version = "v-$id",
            status = status),
        sourceLabel = "Lark", source = "lark", feedId = feedId, event = event, agent = agent)

    private class FakeInbox(records: List<InboxRecord>, expanded: List<String> = listOf("action", "fyi")) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
        override val connectionState = MutableStateFlow(ConnectionState.Online)
        override val preferences = MutableStateFlow(InboxPreferences(expanded = expanded))
        override suspend fun savePreferences(value: InboxPreferences) { preferences.value = value }
        override val conversationInbox = MutableStateFlow<InboxConversations?>(null)
        val calls = mutableListOf<Pair<List<InboxDoneTarget>, InboxBulkAction>>()
        /** One row's own Done / Move back, as the row's swipe and menu make it: the item alone, never its chat. */
        val singles = mutableListOf<Pair<String, Boolean>>()
        val reads = mutableListOf<String>()
        var history: List<InboxRecord> = emptyList()
        val itemReads = mutableListOf<String>()
        override suspend fun item(itemId: String): InboxRecord? {
            itemReads += itemId
            return snapshot.value.records.inboxRecord(itemId) ?: history.inboxRecord(itemId)
        }
        override suspend fun markLatestRead(feedId: String): Boolean {
            reads += feedId
            snapshot.value = snapshot.value.copy(records = snapshot.value.records.map {
                if (it.feedId == feedId) it.copy(read = true) else it
            })
            return true
        }
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): InboxBulkResult {
            calls += items to action
            val ids = items.map { it.id }.toSet()
            snapshot.value = snapshot.value.copy(records = snapshot.value.records.map { record ->
                if (record.item.id in ids) record.copy(item = record.item.copy(
                    doneAt = if (action == InboxBulkAction.Reopen) null else "2026-11-12T11:00:00Z")) else record
            })
            return InboxBulkResult(changed = items.map { it.id })
        }
        override suspend fun doneHistory(manager: String?, cursor: InboxDoneCursor?) =
            if (history.isEmpty()) InboxDonePage() else InboxDonePage(history, next = null)
        override suspend fun refresh() = Unit
        override suspend fun refreshNow() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) {
            singles += itemId to done
            snapshot.value = snapshot.value.copy(records = snapshot.value.records.map {
                if (it.item.id == itemId) it.copy(item = it.item.copy(
                    doneAt = if (done) "2026-11-12T11:00:00Z" else null)) else it
            })
        }
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = error("A gesture must never send a reply")
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private fun screen(inbox: FakeInbox) {
        inbox.conversationInbox.value = InboxConversations(inbox, "synthetic-pairing", owner, ReplyComposers(inbox, owner))
        rule.setContent { PersonaTheme(reduceMotion = true) {
            Box(Modifier.testTag("inbox-one-row").background(PersonaTheme.palette.background)) { InboxScreen(inbox, navigator) }
        } }
        rule.waitForIdle()
    }

    private fun row(title: String) = rule.onNode(hasText(title) and hasClickAction())

    @Test fun bothOpenItemsOfOneChatAreRowsAndEachOpensItsOwnPage() {
        val inbox = FakeInbox(listOf(
            item("a", "First event", "First preview", event = InboxEventFields(chain = listOf(InboxChainEntry("e1", kind = "received", text = "Hello")))),
            item("b", "Second event", "Second preview", event = InboxEventFields(chain = listOf(InboxChainEntry("e2", kind = "received", text = "Again"))))))
        screen(inbox)
        row("First event").assertExists()
        row("Second event").assertExists()
        row("Second event").performClick()
        rule.waitForIdle()
        assertEquals("The row opens its own item, not the chat's newest one", listOf<NavKey>(InboxEventRoute("b")), opened)
    }

    @Test fun filingOneItemLeavesTheOtherOpenItemsOfTheSameChatAlone() {
        val inbox = FakeInbox(listOf(item("a", "First event", "First preview"), item("b", "Second event", "Second preview")))
        screen(inbox)
        // Rows swipe one way only since #6633: the trailing swipe is Done, and Hand to AI lives in the long-press menu.
        row("First event").performTouchInput { swipeLeft() }
        rule.waitForIdle()
        rule.runOnIdle {
            assertEquals("A gesture files this item only", listOf("a" to true), inbox.singles)
            assertTrue("A row's own Done is one item's, not its chat's bulk", inbox.calls.isEmpty())
            assertTrue(inbox.snapshot.value.records.single { it.item.id == "a" }.item.isDone)
            assertFalse("The other item of the chat stays open", inbox.snapshot.value.records.single { it.item.id == "b" }.item.isDone)
        }
        row("Second event").assertExists()
    }

    @Test fun selectingOneItemOfAChatThenMarkingDoneFilesJustThatOne() {
        val inbox = FakeInbox(listOf(item("a", "First event", "First preview"), item("b", "Second event", "Second preview")))
        screen(inbox)
        // A long-press starts a selection with that item; picking nothing else and marking done files only it.
        row("First event").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onNodeWithText(res.getString(R.string.inbox_mark_done)).performClick()
        rule.waitForIdle()
        rule.runOnIdle {
            assertEquals("A selection files the picked item only", listOf(listOf("a")), inbox.calls.map { c -> c.first.map { it.id } })
            assertEquals(InboxBulkAction.Done, inbox.calls.single().second)
            assertFalse("The other item of the chat stays open", inbox.snapshot.value.records.single { it.item.id == "b" }.item.isDone)
        }
        rule.onNodeWithText("Second event").assertExists()
    }

    @Test fun theMenuOfAWorkingItemsRowFilesThatItemOnly() {
        // A row Persona is working on cannot be swiped, so its menu is the only way in; it speaks for this item alone.
        val inbox = FakeInbox(listOf(
            item("busy", "Busy event", "Busy preview", agent = InboxAgent(state = "running")),
            item("open", "Open event", "Open preview")))
        screen(inbox)
        row("Busy event").performTouchInput { longClick() }
        rule.waitForIdle()
        // The row's menu files with the row's own word ("Done"), as its swipe does; the Done chip's own count shares the
        // word, so the action in the sheet is the one to tap. The top bar's "Mark as done" is for a selection.
        // The Done chip's label shares the word, so the sheet's action (drawn last, over the page) is the one tapped.
        rule.onAllNodes(hasText(res.getString(R.string.inbox_done)) and hasClickAction()).onLast().performClick()
        rule.waitForIdle()
        rule.runOnIdle {
            assertEquals(listOf("busy" to true), inbox.singles)
            assertTrue(inbox.calls.isEmpty())
            assertFalse(inbox.snapshot.value.records.single { it.item.id == "open" }.item.isDone)
        }
    }

    @Test fun aSectionsMarkAllAsReadAcknowledgesTheChatAndReportsTheMailItCannotRead() {
        val inbox = FakeInbox(listOf(
            item("a", "First event", "First preview"),
            item("b", "Second event", "Second preview"),
            InboxRecord(InboxItem("mail-1", account = "me@example.com", kind = "mail", title = "A mail", tier = "action",
                updatedAt = "2026-11-12T09:00:00Z", version = "v-mail"), source = "mail")))
        screen(inbox)
        rule.onNodeWithText(res.getString(R.string.inbox_conversations_read_all)).performClick()
        rule.waitForIdle()
        rule.runOnIdle {
            assertEquals("The chat is acknowledged once, whatever its items", listOf("shared-room"), inbox.reads)
            assertTrue("A read receipt never files an item", inbox.calls.isEmpty())
            assertTrue(inbox.snapshot.value.records.none { it.item.isDone })
        }
        rule.onNodeWithText(res.getString(R.string.inbox_conversations_read_skipped)).assertExists()
    }

    @Test fun theSectionOrderIsTheHubsOwnAndMixedWorkingNeverShowsTwice() {
        val inbox = FakeInbox(listOf(
            item("hub-working", "Hub is on it", "Working preview", tier = "action", status = "working"),
            item("legacy-working", "Agent is on it", "Legacy preview", tier = "action",
                agent = InboxAgent(state = "running")),
            item("fyi", "Just so you know", "Fyi preview", tier = "fyi"),
            item("noise", "Folded away", "Noise preview", tier = "noise")))
        screen(inbox)
        // Both In progress rows show, exactly once each: the hub's working section and the older hub's running agent
        // join the same one section, so there is one In progress title and neither row is swallowed.
        rule.onAllNodesWithText(res.getString(R.string.inbox_section_working)).assertCountEquals(1)
        rule.onAllNodesWithText("Hub is on it").assertCountEquals(1)
        rule.onAllNodesWithText("Agent is on it").assertCountEquals(1)
        rule.onNodeWithText("Just so you know").assertExists()
        rule.onNodeWithText(res.getString(R.string.inbox_bay_needs_you)).assertExists()
        rule.onNodeWithText(res.getString(R.string.inbox_group_fyi)).assertExists()
    }

    private fun historyInbox() = FakeInbox(listOf(InboxRecord(InboxItem("recent", kind = "dm", title = "Handled yesterday",
        doneAt = "2026-11-12T09:00:00Z", updatedAt = "2026-11-12T09:00:00Z", version = "v-recent"))),
        expanded = listOf("action", "fyi", "done")).also { inbox ->
        // Done "just now", so it sits in the group Done opens by default; it comes from the history page, not the list.
        inbox.history = listOf(item("old", "Handled long ago", "Old preview",
            event = InboxEventFields(chain = listOf(InboxChainEntry("e-old", kind = "received", text = "Hi"))),
            doneAt = java.time.Instant.now().toString(), feedId = null))
    }

    @Test fun aDoneHistoryItemOpensItsEventPageAsBefore() {
        // The done history is merged into the list only; its event route still opens, and the single-item lookup fills
        // the page from that history when the live snapshot no longer carries the item.
        screen(historyInbox())
        row("Handled long ago").assertExists()
        row("Handled long ago").performClick()
        rule.waitForIdle()
        assertEquals(listOf<NavKey>(InboxEventRoute("old")), opened)
    }

    @Test fun theEventPageOfAnItemPastTheSnapshotShowsItsContents() {
        // The snapshot no longer carries the item, but the repository's single-item lookup returns its full history record.
        val inbox = historyInbox()
        rule.setContent { PersonaTheme(reduceMotion = true) {
            InboxEventScreen(inbox, InboxEventRoute("old"), navigator)
        } }
        rule.waitForIdle()
        rule.onNodeWithText("Handled long ago").assertExists()
        rule.onNodeWithText("Hi").assertExists()
        rule.onNodeWithText(res.getString(R.string.inbox_item_missing)).assertDoesNotExist()
        assertEquals(listOf("old"), inbox.itemReads)
    }

    @Test fun anOlderHubChatRowOpensTheCollapsedItemPage() {
        val inbox = FakeInbox(listOf(item("plain", "No event fields", "Plain preview")))
        screen(inbox)
        row("No event fields").performClick()
        rule.waitForIdle()
        assertEquals(listOf<NavKey>(InboxEventRoute("plain")), opened)
    }
}

/** The navigation badge counts unread open items that need the user, one per item and never one per chat. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxOneRowBadgeTest {
    private fun record(id: String, read: Boolean = false, tier: String = "action", status: String? = null,
        friendId: String? = "shared-room") = InboxRecord(
        InboxItem(id, kind = "dm", tier = tier, status = status, updatedAt = "2026-11-12T10:00:00Z"),
        read = read, feedId = friendId, source = if (friendId != null) "lark" else "")

    @Test fun twoUnreadItemsOfOneChatCountTwice() {
        val records = listOf(record("a"), record("b"))
        val grouped = buildInboxConversations("paired", InboxSnapshot(records = records))
        assertEquals("Each unread item that needs the user counts once", 2, inboxUnreadCount(records, grouped))
        assertEquals("The two items are still one chat underneath", 1, grouped.rows.size)
    }

    @Test fun aReadItemOfTheChatIsNotCountedAndReceiptsStillClearTheRest() {
        val records = listOf(record("a", read = true), record("b"))
        val grouped = buildInboxConversations("paired", InboxSnapshot(records = records))
        assertEquals(1, inboxUnreadCount(records, grouped))
        // A read receipt arrives before the hub's list: the chat's items stop counting at once.
        val receipt = buildInboxConversations("paired", InboxSnapshot(records = records),
            setOf(InboxReadVersion("a", null), InboxReadVersion("b", null)))
        assertEquals("A receipt ahead of the list clears the badge", 0, inboxUnreadCount(records, receipt))
        assertTrue(receipt.rows.single().records.none { it.item.isDone })
    }

    @Test fun fyisWorkingAndDoneItemsNeverCount() {
        val records = listOf(record("fyi", tier = "fyi"), record("noise", tier = "noise"),
            record("working", status = "working"), record("hub-action", tier = "fyi", status = "action"))
        val grouped = buildInboxConversations("paired", InboxSnapshot(records = records))
        assertEquals(1, inboxUnreadCount(records, grouped))
    }
}
