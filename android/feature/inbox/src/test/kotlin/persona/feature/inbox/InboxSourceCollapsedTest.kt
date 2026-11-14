package persona.feature.inbox

import android.content.Context
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.snapshots.SnapshotStateList
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.onStart
import kotlinx.serialization.json.JsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxChainLink
import persona.core.contracts.InboxEventFields
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.MailInboxItem
import persona.core.contracts.api.MailMessage
import persona.core.contracts.api.MailReference
import persona.core.contracts.api.MailTransport
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** An inbox detail keeps its source unmounted until the user explicitly asks for it. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class InboxSourceCollapsedTest {
    @get:Rule val rule = createComposeRule()

    private val context: Context
        get() = ApplicationProvider.getApplicationContext()
    private val viewOriginal: String
        get() = context.getString(R.string.inbox_event_view_source)
    private val back: String
        get() = context.getString(persona.core.designsystem.R.string.designsystem_back)

    private enum class Source { Chat, Mail }

    private class StackNavigator(private val stack: SnapshotStateList<NavKey>) : Navigator {
        override fun navigate(key: NavKey) { stack += key }
        override fun back() { if (stack.size > 1) stack.removeAt(stack.lastIndex) }
        override fun selectTab(tab: AppTab) = Unit
    }

    /** A visible, verified window whose reads are recorded instead of reaching a transport. */
    private class RecordingRepository(
        record: InboxRecord,
        private val base: FakeInboxRepository = FakeInboxRepository(),
    ) : InboxRepository by base {
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(record)))
        override val connectionState = flowOf(ConnectionState.Online)
        private val window = MutableStateFlow(
            LarkConversation(
                title = "Source chat",
                messages = listOf(
                    LarkMessage(
                        id = CHAT_MESSAGE_ID,
                        senderId = "sender",
                        senderName = "Sender",
                        position = CHAT_POSITION,
                        time = "2026-11-04T09:00:00Z",
                        parts = listOf(LarkPart.Text("Unread source chat body")),
                    ),
                ),
                verified = true,
            ),
        )
        val visibleCalls = mutableListOf<List<String>>()
        val conversationCalls = mutableListOf<Pair<String, Long?>>()
        val conversationSubscriptions = mutableListOf<Pair<String, Long?>>()

        override fun conversation(feedId: String, startPosition: Long?) = window.onStart {
            conversationSubscriptions += feedId to startPosition
        }.also { conversationCalls += feedId to startPosition }

        override suspend fun markVisible(feedId: String, messages: List<LarkMessage>): Boolean {
            visibleCalls += messages.map { it.id }
            return true
        }
    }

    /** The latest message starts unseen; every read and seen write is recorded. */
    private class RecordingMailTransport : MailTransport {
        override val scope = "test"
        val itemCalls = mutableListOf<String>()
        val messageCalls = mutableListOf<Long>()
        val seenCalls = mutableListOf<Pair<Long, Boolean>>()

        override suspend fun item(itemId: String): MailInboxItem {
            itemCalls += itemId
            return MailInboxItem(itemId, MailReference(ACCOUNT_ID, uids = listOf(40L, LATEST_MAIL_UID)))
        }

        override suspend fun message(ref: MailReference, uid: Long): MailMessage {
            messageCalls += uid
            return MailMessage(
                accountId = ref.accountId,
                mailbox = ref.mailbox,
                uid = uid,
                subject = "Source mail subject",
                text = "Unread source mail body",
                seen = false,
            )
        }

        override suspend fun setSeen(ref: MailReference, uid: Long, seen: Boolean) {
            seenCalls += uid to seen
        }

        override suspend fun refreshInbox() = Unit
    }

    private fun record(source: Source, hasEvent: Boolean): InboxRecord {
        val event = if (hasEvent) InboxEventFields(status = "fyi", recap = "Collapsed recap") else InboxEventFields.None
        return when (source) {
            Source.Chat -> InboxRecord(
                InboxItem("chat-item", kind = "group", title = "Chat item", preview = "Chat preview", status = event.status),
                feedId = FEED_ID,
                startPosition = CHAT_POSITION,
                source = "lark",
                event = event,
            )
            Source.Mail -> InboxRecord(
                InboxItem("mail-item", kind = "mail", title = "Mail item", preview = "Mail preview", status = event.status),
                source = "mail",
                mail = InboxMailRef(ACCOUNT_ID, uids = listOf(40L, LATEST_MAIL_UID)),
                event = event,
            )
        }
    }

    private fun advancePastReadWindow() {
        rule.mainClock.advanceTimeBy(1_000)
        rule.waitForIdle()
    }

    private fun assertNoSourceReads(repository: RecordingRepository, transport: RecordingMailTransport) {
        assertTrue("The collapsed detail opened a chat body: ${repository.conversationCalls}", repository.conversationCalls.isEmpty())
        assertTrue("The collapsed detail subscribed to a chat body: ${repository.conversationSubscriptions}", repository.conversationSubscriptions.isEmpty())
        assertTrue("The collapsed detail acknowledged chat messages: ${repository.visibleCalls}", repository.visibleCalls.isEmpty())
        assertTrue("The collapsed detail loaded a mail item: ${transport.itemCalls}", transport.itemCalls.isEmpty())
        assertTrue("The collapsed detail loaded a mail body: ${transport.messageCalls}", transport.messageCalls.isEmpty())
        assertTrue("The collapsed detail marked mail seen: ${transport.seenCalls}", transport.seenCalls.isEmpty())
    }

    private fun sourceCallCounts(repository: RecordingRepository, transport: RecordingMailTransport) = listOf(
        repository.conversationCalls.size, repository.conversationSubscriptions.size, repository.visibleCalls.size,
        transport.itemCalls.size, transport.messageCalls.size, transport.seenCalls.size,
    )

    private fun verifyCollapsedUntilViewOriginal(source: Source, hasEvent: Boolean) {
        val record = record(source, hasEvent)
        val pages = InboxItemPages()
        val repository = RecordingRepository(record)
        val transport = RecordingMailTransport()
        val stack = mutableStateListOf<NavKey>(inboxRowPage(record, pages))
        val navigator = StackNavigator(stack)

        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalInboxItemPages provides pages) {
                    when (val route = stack.last()) {
                        is InboxEventRoute -> InboxEventScreen(repository, route, navigator)
                        is LarkRoute -> LarkScreen(repository, route, navigator)
                        is MailRoute -> MailScreen(repository, transport, route, navigator)
                        else -> error("Unexpected route in collapsed-source test: $route")
                    }
                }
            }
        }

        // This also covers the chat's 450 ms visible-read delay and mail's immediate body load.
        advancePastReadWindow()
        assertNoSourceReads(repository, transport)
        rule.onNodeWithText(viewOriginal).assertExists().performClick()
        advancePastReadWindow()

        assertEquals(pages.open(record), stack.last())
        when (source) {
            Source.Chat -> {
                assertEquals("View original must open the chat body exactly once", 1, repository.conversationCalls.size)
                assertEquals("View original must subscribe to the chat body exactly once", 1, repository.conversationSubscriptions.size)
                assertEquals(FEED_ID, repository.conversationCalls.single().first)
                assertTrue("View original did not expose the visible chat to its read gate", repository.visibleCalls.isNotEmpty())
                assertEquals(listOf(CHAT_MESSAGE_ID), repository.visibleCalls.last())
                assertTrue(transport.itemCalls.isEmpty())
                assertTrue(transport.messageCalls.isEmpty())
                assertTrue(transport.seenCalls.isEmpty())
            }
            Source.Mail -> {
                assertEquals(listOf(record.item.id), transport.itemCalls)
                assertEquals(listOf(LATEST_MAIL_UID), transport.messageCalls)
                assertEquals(listOf(LATEST_MAIL_UID to true), transport.seenCalls)
                assertTrue(repository.visibleCalls.isEmpty())
            }
        }

        val callsBeforeBack = sourceCallCounts(repository, transport)
        rule.onNodeWithContentDescription(back).performClick()
        advancePastReadWindow()

        assertEquals(listOf(InboxEventRoute(record.item.id)), stack.toList())
        assertEquals("Returning to the collapsed detail mounted or read the source again", callsBeforeBack,
            sourceCallCounts(repository, transport))
    }

    private fun verifyCollapsedUntilSourceChainClick(source: Source) {
        val base = record(source, hasEvent = true)
        val chainText = when (source) {
            Source.Chat -> "Open the positioned source chat"
            Source.Mail -> "Open the source mail by UID"
        }
        val link = when (source) {
            Source.Chat -> InboxChainLink(type = "source", position = JsonPrimitive(CHAT_POSITION.toString()))
            Source.Mail -> InboxChainLink(type = "source", uid = LATEST_MAIL_UID)
        }
        val record = base.copy(event = base.event.copy(chain = listOf(
            InboxChainEntry("source-chain", kind = "received", text = chainText, link = link),
        )))
        val pages = InboxItemPages()
        val repository = RecordingRepository(record)
        val transport = RecordingMailTransport()
        val stack = mutableStateListOf<NavKey>(inboxRowPage(record, pages))
        val navigator = StackNavigator(stack)

        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalInboxItemPages provides pages) {
                    when (val route = stack.last()) {
                        is InboxEventRoute -> InboxEventScreen(repository, route, navigator)
                        is LarkRoute -> LarkScreen(repository, route, navigator)
                        is MailRoute -> MailScreen(repository, transport, route, navigator)
                        else -> error("Unexpected route in source-chain test: $route")
                    }
                }
            }
        }

        advancePastReadWindow()
        assertNoSourceReads(repository, transport)
        rule.onNodeWithText(chainText).assertExists().performClick()
        // Compose the destination before advancing its visible-read window.
        rule.waitForIdle()
        advancePastReadWindow()

        when (source) {
            Source.Chat -> {
                assertEquals(LarkRoute(FEED_ID, record.item.id, CHAT_POSITION), stack.last())
                assertEquals(listOf(FEED_ID to CHAT_POSITION), repository.conversationCalls)
                assertEquals(listOf(FEED_ID to CHAT_POSITION), repository.conversationSubscriptions)
                assertTrue("The source chain did not expose the visible chat to its read gate", repository.visibleCalls.isNotEmpty())
                assertEquals(listOf(CHAT_MESSAGE_ID), repository.visibleCalls.last())
                assertTrue(transport.itemCalls.isEmpty())
                assertTrue(transport.messageCalls.isEmpty())
                assertTrue(transport.seenCalls.isEmpty())
            }
            Source.Mail -> {
                assertEquals(MailRoute(record.item.id), stack.last())
                assertEquals(listOf(record.item.id), transport.itemCalls)
                assertEquals(listOf(LATEST_MAIL_UID), transport.messageCalls)
                assertEquals(listOf(LATEST_MAIL_UID to true), transport.seenCalls)
                assertTrue(repository.conversationCalls.isEmpty())
                assertTrue(repository.conversationSubscriptions.isEmpty())
                assertTrue(repository.visibleCalls.isEmpty())
            }
        }

        val callsBeforeBack = sourceCallCounts(repository, transport)
        rule.onNodeWithContentDescription(back).performClick()
        advancePastReadWindow()

        assertEquals(listOf(InboxEventRoute(record.item.id)), stack.toList())
        assertEquals("Returning from a source-chain page mounted or read the source again", callsBeforeBack,
            sourceCallCounts(repository, transport))
    }

    @Test fun eventChatStaysCollapsedUntilViewOriginal() {
        verifyCollapsedUntilViewOriginal(Source.Chat, hasEvent = true)
    }

    @Test fun eventMailStaysCollapsedUntilViewOriginal() {
        verifyCollapsedUntilViewOriginal(Source.Mail, hasEvent = true)
    }

    @Test fun nonEventChatStaysCollapsedUntilViewOriginal() {
        verifyCollapsedUntilViewOriginal(Source.Chat, hasEvent = false)
    }

    @Test fun nonEventMailStaysCollapsedUntilViewOriginal() {
        verifyCollapsedUntilViewOriginal(Source.Mail, hasEvent = false)
    }

    @Test fun eventChatSourceChainOpensPositionedBodyOnlyOnClick() {
        verifyCollapsedUntilSourceChainClick(Source.Chat)
    }

    @Test fun eventMailSourceChainOpensBodyOnlyOnClick() {
        verifyCollapsedUntilSourceChainClick(Source.Mail)
    }

    @Test fun legacyItemsAndDirectSourceRoutesKeepTheirExistingPages() {
        val pages = InboxItemPages()
        val chat = record(Source.Chat, hasEvent = false)
        val mail = record(Source.Mail, hasEvent = false)
        val chatSource = LarkRoute(FEED_ID, chat.item.id, CHAT_POSITION)
        val mailSource = MailRoute(mail.item.id)

        // View original and other direct source entry points still use the native routes themselves.
        assertEquals(chatSource, pages.open(chat))
        assertEquals(chatSource, inboxItemPage(chat))
        assertEquals(chatSource, inboxOpenPage(chat, pages))
        assertEquals(mailSource, pages.open(mail))
        assertEquals(mailSource, inboxItemPage(mail))
        assertEquals(mailSource, inboxOpenPage(mail, pages))

        val legacy = InboxRecord(InboxItem("legacy-item", kind = "unknown", title = "Legacy item"), source = "other")
        assertEquals(InboxItemDetailRoute(legacy.item.id), inboxItemPage(legacy))
        assertEquals(InboxItemDetailRoute(legacy.item.id), inboxOpenPage(legacy, pages))
        assertEquals(InboxItemDetailRoute(legacy.item.id), inboxRowPage(legacy, pages))
    }

    private companion object {
        const val FEED_ID = "feed-1"
        const val CHAT_MESSAGE_ID = "chat-message"
        const val CHAT_POSITION = 41L
        const val ACCOUNT_ID = "account"
        const val LATEST_MAIL_UID = 41L
    }
}
