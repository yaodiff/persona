package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxEventFields
import persona.core.contracts.api.ExtensionUiPlace
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.*
import persona.feature.inbox.InboxEventRoute
import persona.feature.inbox.InboxItemPages
import persona.feature.inbox.InboxDocRef
import persona.feature.inbox.InboxMomentRef
import persona.feature.inbox.InboxRecord
import persona.feature.inbox.LarkMomentRoute
import persona.feature.inbox.LarkRoute
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ShellRoutesTest {
    private fun stacks() = mapOf<AppTab, MutableList<NavKey>>(
        AppTab.Persona to mutableListOf(AssistantRoute), AppTab.Agents to mutableListOf(AgentsRoute),
    )

    @Test fun notificationTabWinsBeforeMetadataAndLandsOnTheChat() {
        // Cold start: the session list has not loaded, so the router alone cannot tell the kind.
        val unknown = Router()
        val chat = ChatRoute("machine", "session")
        val assistant = unknown.open(NotificationRoute(AppTab.Persona, chat))!!
        val stacks = stacks()
        assertEquals(AppTab.Persona, assistant.restore(stacks))
        assertEquals(listOf(AssistantRoute, chat.copy(kind = ConversationKind.Assistant)), stacks.getValue(AppTab.Persona))

        // Metadata that disagrees does not move a conversation the notification placed explicitly.
        val agentMetadata = Router { _, _ -> ConversationKind.Agent }
        assertEquals(AppTab.Persona, agentMetadata.open(NotificationRoute(AppTab.Persona, chat))!!.tab)
        assertEquals(AppTab.Agents, unknown.open(NotificationRoute(AppTab.Agents, chat))!!.tab)
    }

    @Test fun notificationWithoutTabWaitsForMetadataAndTabOnlyLinksSelectTheTab() {
        val chat = ChatRoute("machine", "session")
        assertNull(Router().open(NotificationRoute(null, chat)))
        val known = Router { _, _ -> ConversationKind.Assistant }
        assertEquals(AppTab.Persona, known.open(NotificationRoute(null, chat))!!.tab)

        val stacks = stacks()
        stacks.getValue(AppTab.Agents).add(ChatRoute("machine", "other", ConversationKind.Agent))
        val tab = Router().open(NotificationRoute(AppTab.Agents))!!
        assertEquals(AppTab.Agents, tab.restore(stacks))
        // A tab-only link keeps that tab's stack as the user left it.
        assertEquals(2, stacks.getValue(AppTab.Agents).size)
        assertNull(Router().open(NotificationRoute(null)))
    }

    @Test fun inboxItemLinksOpenEventPagesLikeRows() {
        val event = InboxEventFields(chain = listOf(InboxChainEntry("c1", kind = "received", text = "Received")))
        val chat = InboxRecord(InboxItem("chat-event", kind = "dm", status = "action"), feedId = "room", event = event)
        val mail = InboxRecord(InboxItem("mail-event", kind = "mail", status = "action"), source = "mail", event = event)
        val records = listOf(chat, mail)

        assertEquals(InboxDestination.Open(InboxEventRoute("chat-event")), inboxDestination(InboxItemRoute("chat-event"), records, true))
        assertEquals(InboxDestination.Open(InboxEventRoute("mail-event")), inboxDestination(InboxItemRoute("mail-event"), records, true))
        assertEquals(InboxDestination.Open(LarkRoute("room", "chat-event", null)), inboxDestination(LarkChatRoute("room"), records, true))
    }

    @Test fun inboxItemLinksOpenTheirRowPageOnceTheItemIsKnown() {
        val lark = InboxRecord(InboxItem("lark-item", kind = "dm"), feedId = "chat", startPosition = 42)
        val mail = InboxRecord(InboxItem("mail-item", kind = "mail"))
        val moment = InboxRecord(InboxItem("moment-item", kind = "moment"), moment = InboxMomentRef(postId = "post-1"))
        val records = listOf(lark, mail, moment)

        assertEquals(InboxDestination.Open(InboxEventRoute("lark-item")), inboxDestination(InboxItemRoute("lark-item"), records, false))
        assertEquals(InboxDestination.Open(InboxEventRoute("mail-item")), inboxDestination(InboxItemRoute("mail-item"), records, false))
        assertEquals(InboxDestination.Open(LarkMomentRoute("post-1", "moment-item")), inboxDestination(InboxItemRoute("moment-item"), records, false))
        // Before the first refresh a missing item may still arrive; afterwards it is reported.
        assertNull(inboxDestination(InboxItemRoute("gone"), records, false))
        assertEquals(InboxDestination.Missing, inboxDestination(InboxItemRoute("gone"), records, true))

        assertEquals(InboxDestination.Open(LarkRoute("chat", "lark-item", 42)), inboxDestination(LarkChatRoute("chat"), records, false))
        assertNull(inboxDestination(LarkChatRoute("other"), records, false))
        assertEquals(InboxDestination.Open(LarkRoute("other")), inboxDestination(LarkChatRoute("other"), records, true))
        assertNull(inboxDestination(InboxRoute, records, true))
    }

    // Item links open chat and mail in the collapsed detail; non-chat items still use their connector's page.
    // Direct chat links keep the native conversation.
    @Test fun inboxItemLinksCollapseChatAndMailWithAnActiveConnectorSlot() {
        val lark = InboxRecord(InboxItem("lark-item", kind = "group", title = "评审"), feedId = "chat", startPosition = 42, source = "lark")
        val mail = InboxRecord(InboxItem("mail-item", kind = "mail"), source = "mail")
        val moment = InboxRecord(InboxItem("moment-item", kind = "moment", title = "动态"), source = "lark", moment = InboxMomentRef(postId = "post-1"))
        val doc = InboxRecord(InboxItem("doc-item", kind = "doc", title = "文档"), source = "lark", doc = InboxDocRef("https://example.larkoffice.com/docx/D1"))
        val pages = InboxItemPages(listOf(ExtensionUiPlace("larkext", "event", "事件", "inbox_item", connector = "lark")))
        assertEquals(InboxDestination.Open(InboxEventRoute("lark-item")),
            inboxDestination(InboxItemRoute("lark-item"), listOf(lark, mail), false, pages))
        assertEquals(InboxDestination.Open(InboxItemUiRoute("moment-item", "larkext", "event", "动态")),
            inboxDestination(InboxItemRoute("moment-item"), listOf(moment), false, pages))
        assertEquals(InboxDestination.Open(InboxItemUiRoute("doc-item", "larkext", "event", "文档")),
            inboxDestination(InboxItemRoute("doc-item"), listOf(doc), false, pages))
        assertEquals(InboxDestination.Open(InboxEventRoute("mail-item")), inboxDestination(InboxItemRoute("mail-item"), listOf(lark, mail), false, pages))
        assertEquals(InboxDestination.Open(LarkRoute("chat", "lark-item", 42)), inboxDestination(LarkChatRoute("chat"), listOf(lark), false, pages))
        val pluginPages = pages.copy(nativeChats = false)
        assertEquals(InboxDestination.Open(InboxEventRoute("lark-item")),
            inboxDestination(InboxItemRoute("lark-item"), listOf(lark), false, pluginPages))
        assertEquals(InboxDestination.Open(LarkRoute("chat", "lark-item", 42)),
            inboxDestination(LarkChatRoute("chat"), listOf(lark), false, pluginPages))
        // No pages (an older hub): the collapsed item page.
        assertEquals(InboxDestination.Open(InboxEventRoute("lark-item")), inboxDestination(InboxItemRoute("lark-item"), listOf(lark), false))
    }

    @Test fun personaTerminalLinksKeepTheirConversationStack() {
        val link = "persona://open?target=terminal&machine=mac%20mini&session=a%2Bb"
        val route = terminalLink(link)!!
        assertEquals(TerminalRoute("mac mini", "a+b"), route.key)
        assertEquals(AppTab.Agents, route.tab)
        val stacks = stacks()
        assertEquals(AppTab.Agents, route.open(stacks))
        assertEquals(listOf(AgentsRoute, ChatRoute("mac mini", "a+b", ConversationKind.Agent), TerminalRoute("mac mini", "a+b")),
            stacks.getValue(AppTab.Agents))
        assertNull(terminalLink("persona://open?target=terminal&machine=m&session=s&session=t"))
    }

    @Test fun terminalNotificationLinksOpenTheTerminalAboveItsConversation() {
        val route = terminalLink("persona://open?target=terminal&machine=mac%20mini&session=a%2Bb")!!
        assertEquals(TerminalRoute("mac mini", "a+b"), route.key)
        val stacks = stacks()
        assertEquals(AppTab.Agents, route.open(stacks))
        assertEquals(listOf(AgentsRoute, ChatRoute("mac mini", "a+b", ConversationKind.Agent), TerminalRoute("mac mini", "a+b")),
            stacks.getValue(AppTab.Agents))
        // Opening the same terminal again neither duplicates it nor the conversation.
        assertEquals(AppTab.Agents, route.open(stacks))
        assertEquals(3, stacks.getValue(AppTab.Agents).size)

        assertNull(terminalLink("persona://open?target=terminal&machine=m"))
        assertNull(terminalLink("persona://open?target=terminal&machine=m&session=s&session=t"))
        assertNull(terminalLink("persona://open?machine=m&session=s"))
        assertNull(terminalLink("https://example.com/open?target=terminal&machine=m&session=s"))
    }
}
