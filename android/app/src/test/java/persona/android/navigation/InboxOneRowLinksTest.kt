package persona.android.navigation

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxEventFields
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.InboxItemRoute
import persona.core.navigation.LarkChatRoute
import persona.feature.inbox.InboxEventRoute
import persona.feature.inbox.InboxRecord
import persona.feature.inbox.LarkRoute

/**
 * An item link — a notification, a hub deep link, the Persona home's Needs you, the capsule's "open in the app" — opens
 * what the row would: the collapsed item page for an event or a chat/mail item, and the source or extension page for
 * other items. A direct chat link still opens the chat.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxOneRowLinksTest {
    private fun item(id: String, event: InboxEventFields = InboxEventFields.None) = InboxRecord(
        InboxItem(id, kind = "dm", title = id, tier = "action", updatedAt = "2026-11-12T10:00:00Z"),
        feedId = "room", source = "lark", event = event)

    @Test fun anItemWithEventsOpensItsEventPageNotTheChat() {
        val withEvents = item("with-events", InboxEventFields(chain = listOf(InboxChainEntry("e1", kind = "received", text = "Hi"))))
        assertEquals(InboxDestination.Open(InboxEventRoute("with-events")),
            inboxDestination(InboxItemRoute("with-events"), listOf(withEvents), loaded = true))
    }

    @Test fun aChatItemFromAnOlderHubOpensItsCollapsedPage() {
        assertEquals(InboxDestination.Open(InboxEventRoute("plain")),
            inboxDestination(InboxItemRoute("plain"), listOf(item("plain")), loaded = true))
    }

    @Test fun aChatLinkStillOpensTheChat() {
        assertEquals(InboxDestination.Open(LarkRoute("room", "plain", null)),
            inboxDestination(LarkChatRoute("room"), listOf(item("plain")), loaded = true))
    }
}
