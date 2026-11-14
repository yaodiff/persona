package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.contracts.SessionKey
import persona.core.navigation.*
import persona.feature.inbox.*
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

class AppNavigatorTest {
    private val stacks = AppTab.entries.associateWith { mutableListOf<NavKey>(tabRoot(it)) }
    private var selected = AppTab.Agents
    private val navigator = AppNavigator(stacks, { selected }, { selected = it })

    @Test fun secondarySettingsPagesReturnToTheOriginalTabAndPage() {
        val origin = ChatRoute("machine", "session")
        val settingsPages = listOf(SettingsRoute, SettingsPageRoute(SettingsPage.About), PairingRoute, ConnectionSettingsRoute)
        for (tab in AppTab.entries) {
            for (page in settingsPages) {
                stacks.values.forEach { it.subList(1, it.size).clear() }
                selected = tab
                stacks.getValue(tab).add(origin)
                navigator.navigate(page)
                assertEquals(tab, selected)
                val destination = if (page == ConnectionSettingsRoute) SettingsPageRoute(SettingsPage.Diagnostics) else page
                assertEquals(listOf(tabRoot(tab), origin, SettingsRoute) + if (destination == SettingsRoute) emptyList() else listOf(destination),
                    stacks.getValue(tab))
                navigator.back()
                if (page != SettingsRoute) {
                    assertEquals(SettingsRoute, stacks.getValue(tab).last())
                    navigator.back()
                }
                assertEquals(tab, selected)
                assertEquals(listOf(tabRoot(tab), origin), stacks.getValue(tab))
                assertEquals(listOf(tabRoot(AppTab.entries.first { it != tab })), stacks.getValue(AppTab.entries.first { it != tab }))
            }
        }
    }

    @Test fun contextualModelSettingsReturnDirectlyToTheAssistantTask() {
        selected = AppTab.Persona
        val origin = TodoDetailRoute("example-task")
        navigator.navigate(origin)
        navigator.navigate(ModelPurposeRoute("example-purpose", "example-manager"))
        assertEquals(listOf(AssistantRoute, origin, ModelPurposeRoute("example-purpose", "example-manager")), stacks.getValue(selected))
        navigator.back()
        assertEquals(origin, stacks.getValue(selected).last())
    }

    @Test fun inboxIsPushedOntoTheCurrentStackAndBackReturnsToTheOrigin() {
        val origin = SettingsPageRoute(SettingsPage.Hubs)
        navigator.navigate(origin)
        navigator.navigate(InboxRoute)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, SettingsRoute, origin, InboxRoute), stacks.getValue(selected))
        navigator.back()
        assertEquals(origin, stacks.getValue(selected).last())
    }

    @Test fun legacyLedgerNavigationAndRestoredStacksReturnToConnectors() {
        navigator.navigate(SettingsPageRoute(SettingsPage.Sent))
        assertEquals(listOf(AgentsRoute, SettingsRoute, SettingsPageRoute(SettingsPage.Connectors), SettingsPageRoute(SettingsPage.Sent)),
            stacks.getValue(AppTab.Agents))
        navigator.back()
        assertEquals(SettingsPageRoute(SettingsPage.Connectors), stacks.getValue(AppTab.Agents).last())
        navigator.back()
        assertEquals(listOf(AgentsRoute, SettingsRoute), stacks.getValue(AppTab.Agents))

        stacks.getValue(AppTab.Agents).add(SettingsPageRoute(SettingsPage.Sent))
        val restored = AppNavigator(stacks, { selected }, { selected = it })
        restored.back()
        assertEquals(SettingsPageRoute(SettingsPage.Connectors), stacks.getValue(AppTab.Agents).last())
    }

    @Test fun confirmedCloseRemovesAllMatchingDestinationsAcrossTabs() {
        val target = persona.core.contracts.SessionKey("machine", "closed")
        val chat = ChatRoute(target.machineId, target.sessionId)
        val other = ChatRoute("other-machine", target.sessionId)
        stacks.getValue(AppTab.Agents).addAll(listOf(chat, TerminalRoute(target.machineId, target.sessionId)))
        stacks.getValue(AppTab.Persona).addAll(listOf(chat.copy(kind = ConversationKind.Assistant), InteractionRoute(chat, "request"), other))
        navigator.sessionClosed(target)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute, other), stacks.getValue(AppTab.Persona))
        navigator.sessionClosed(target)
        navigator.back()
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
    }

    @Test fun aChatWhoseGroupWentAwayLeavesItsOwnTabNotTheOneOnScreen() {
        val chat = ChatRoute("machine~g0a1b2c3d", "s")
        val hubs = SettingsPageRoute(SettingsPage.Hubs)
        stacks.getValue(AppTab.Agents).add(chat)
        selected = AppTab.Persona
        navigator.navigate(hubs)
        navigator.groupGone(chat, null)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, SettingsRoute, hubs), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))

        val moved = ChatRoute("machine", "s")
        stacks.getValue(AppTab.Agents).add(InteractionRoute(chat, "request"))
        navigator.groupGone(chat, moved)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, SettingsRoute, hubs), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute, moved), stacks.getValue(AppTab.Agents))

        val router = agentsOwnAgentChats()
        stacks.getValue(AppTab.Agents).remove(moved)
        router.navigate(chat)
        router.groupGone(chat, moved)
        assertEquals(listOf(AgentsRoute, moved), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(hubs, stacks.getValue(selected).last())
    }

    @Test fun switchingTabsReturnsToTheAgentsListAndBackNeverRemovesTheRoot() {
        val chat = ChatRoute("machine", "session")
        navigator.navigate(chat)
        navigator.selectTab(AppTab.Persona)
        navigator.back()
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
        navigator.selectTab(AppTab.Agents)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        navigator.back()
        navigator.back()
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
    }

    @Test fun knownAgentChatFromSettingsReturnsToThatSettingsPage() {
        selected = AppTab.Persona
        val settings = SettingsPageRoute(SettingsPage.Notifications)
        navigator.navigate(settings)
        val chat = ChatRoute("machine", "session", ConversationKind.Agent)
        navigator.navigate(chat)
        navigator.navigate(chat)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        navigator.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(settings, stacks.getValue(selected).last())
    }

    @Test fun assistantChatsStayInPersonaAndLateMetadataRepairsRestoredChats() {
        var assistantKnown = false
        val assistantChat = ChatRoute("machine", "assistant")
        val router = AppNavigator(stacks, { selected }, { selected = it }, chatTab = {
            if (assistantKnown && it == assistantChat) AppTab.Persona else null
        })
        router.selectTab(AppTab.Persona)
        router.navigate(assistantChat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        router.back()

        stacks.getValue(AppTab.Agents).add(assistantChat)
        selected = AppTab.Agents // Restored position, not an explicit request to open the Agents list.
        assistantKnown = true
        router.restoreChatTabs()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, assistantChat), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        router.selectTab(AppTab.Agents)
        router.navigate(assistantChat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, assistantChat), stacks.getValue(AppTab.Persona))
    }

    @Test fun restoredStackKeepsChatOwnership() {
        val chat = ChatRoute("machine", "assistant")
        stacks.getValue(AppTab.Persona).add(chat)
        navigator.navigate(chat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, chat), stacks.getValue(AppTab.Persona))
    }

    @Test fun embeddedAssistantConversationsStayInThePersonaHome() {
        val assistantChat = ChatRoute("machine", "assistant")
        val opened = mutableListOf<Pair<ChatRoute, Boolean>>()
        val router = AppNavigator(stacks, { selected }, { selected = it }, chatTab = {
            if (it.sessionId == "assistant") AppTab.Persona else AppTab.Agents
        }, openAssistant = { chat, select ->
            (chat.sessionId == "assistant").also { if (it) { opened += chat to select; if (select) selected = AppTab.Persona } }
        })
        router.navigate(assistantChat)
        assertEquals(listOf(assistantChat to true), opened)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))

        stacks.getValue(AppTab.Agents).add(assistantChat)
        router.navigate(InboxRoute)
        router.restoreChatTabs()
        assertEquals(assistantChat to false, opened.last())
        assertEquals(AppTab.Persona, selected)
        assertEquals(InboxRoute, stacks.getValue(selected).last())
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))

        val agentChat = ChatRoute("machine", "agent")
        router.navigate(agentChat)
        assertEquals(listOf(AgentsRoute, agentChat), stacks.getValue(AppTab.Agents))
    }

    @Test fun anInboxAssistantChatStaysAboveItsEventAfterMetadataRepair() {
        val event = InboxEventRoute("item")
        val chat = ChatRoute("machine", "assistant", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))
        // Cross-tab identity is the session ids, not the route's kind or provenance.
        stacks.getValue(AppTab.Agents).add(chat.copy(kind = null, source = null))

        router.navigate(chat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, chat), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))

        router.restoreChatTabs()
        assertEquals(listOf(AssistantRoute, InboxRoute, event, chat), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, InboxRoute, event), stacks.getValue(AppTab.Persona))
    }

    @Test fun anInboxAgentChatReturnsToTheEventWithoutMovingItsPersonaStack() {
        val event = InboxEventRoute("item")
        val chat = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))
        val router = embeddedAssistantNavigator { ConversationKind.Agent }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))

        router.navigate(chat)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute, InboxRoute, event), stacks.getValue(AppTab.Persona))

        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute, InboxRoute, event), stacks.getValue(AppTab.Persona))
    }

    @Test fun aRestoredInboxAgentReturnsToThePersonaDetailFromItsPersistedSource() {
        val event = InboxEventRoute("item")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val agent = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event, handling))
        stacks.getValue(AppTab.Agents).add(agent)
        selected = AppTab.Agents

        // Navigation3 restores the route stacks, not the navigator's in-memory openedFrom map.
        val restored = AppNavigator(stacks, { selected }, { selected = it })
        restored.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, handling), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        restored.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun anInboxAssistantChatOpenedFromTheOriginalPageReturnsThere() {
        val original = LarkChatRoute("chat")
        val chat = ChatRoute("machine", "assistant", source = RouteSource.InboxItem("item"))
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, original))

        router.navigate(chat)
        assertEquals(listOf(AssistantRoute, InboxRoute, original, chat), stacks.getValue(AppTab.Persona))
        router.restoreChatTabs()
        assertEquals(listOf(AssistantRoute, InboxRoute, original, chat), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(original, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun anUnsourcedAssistantChatStillReplacesThePersonaHomeContents() {
        val chat = ChatRoute("machine", "assistant", ConversationKind.Assistant)
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, InboxEventRoute("item")))

        router.navigate(chat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
    }

    @Test fun movingAnInboxChatToAnotherHubGroupKeepsItsSourceAndPlace() {
        val event = InboxEventRoute("item")
        val chat = ChatRoute("machine~group", "assistant", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val moved = ChatRoute("machine", "assistant", ConversationKind.Assistant)
        val expected = moved.copy(source = chat.source)
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event, chat))

        router.groupGone(chat, moved)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, expected), stacks.getValue(AppTab.Persona))
        router.restoreChatTabs()
        assertEquals(listOf(AssistantRoute, InboxRoute, event, expected), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aChildAssistantChatInheritsTheSourceAndBackUnwindsToTheEvent() {
        val event = InboxEventRoute("item")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val child = ChatRoute("machine", "child", ConversationKind.Assistant)
        val sourcedChild = child.copy(source = handling.source)
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))

        router.navigate(handling)
        router.navigate(child)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, handling, sourcedChild), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())
        router.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun anAgentAboveAnInboxAssistantStaysOnAgentsAndReturnsToTheHandlingChat() {
        val event = InboxEventRoute("item")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val agent = ChatRoute("machine", "agent", ConversationKind.Agent)
        val sourcedAgent = agent.copy(source = handling.source)
        val router = embeddedAssistantNavigator {
            if (it.sessionId == "agent") ConversationKind.Agent else ConversationKind.Assistant
        }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))

        router.navigate(handling)
        router.navigate(agent)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, sourcedAgent), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute, InboxRoute, event, handling), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())
        router.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aRequestInAChildAssistantChatKeepsTheSameReturnChain() {
        val event = InboxEventRoute("item")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val request = InteractionRoute(ChatRoute("machine", "child", ConversationKind.Assistant), "request")
        val sourcedRequest = request.copy(chat = request.chat.copy(source = handling.source))
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))

        router.navigate(handling)
        router.navigate(request)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, handling, sourcedRequest), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())
        router.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun reopeningAnAgentFromAnotherItemReturnsToTheLatestHandlingChat() {
        val first = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("first"))
        val event = InboxEventRoute("second")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("second"))
        val router = embeddedAssistantNavigator {
            if (it.sessionId == "agent") ConversationKind.Agent else ConversationKind.Assistant
        }
        stacks.getValue(AppTab.Agents).add(first)
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event, handling))

        router.navigate(first.copy(source = null))
        assertEquals(AppTab.Agents, selected)
        // Router.restore retains the existing Agent page and its UI state; the new origin is still recorded.
        assertEquals(listOf(AgentsRoute, first), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun reopeningAnUnsourcedAgentKeepsItsReturnAcrossRestorationWithoutReplacingItsPage() {
        val existing = ChatRoute("machine", "agent", ConversationKind.Agent)
        val event = InboxEventRoute("second")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("second"))
        val kind: (SessionKey) -> ConversationKind = {
            if (it.sessionId == "agent") ConversationKind.Agent else ConversationKind.Assistant
        }
        val router = embeddedAssistantNavigator(kind)
        stacks.getValue(AppTab.Agents).add(existing)
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event, handling))

        router.navigate(existing)
        // Router.restore retains the existing key and its UI state; provenance is persisted separately.
        assertEquals(existing, stacks.getValue(AppTab.Agents).last())
        val persisted = router.inboxReturns()
        assertEquals("agent", persisted.single().sessionId)
        assertEquals(handling.source, persisted.single().source)

        val restored = assistantNavigator(kind, persisted)
        restored.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aColdRestoredInboxAgentRebuildsTheItemLinkBeforeReturning() {
        val agent = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))
        stacks.getValue(AppTab.Agents).add(agent)
        selected = AppTab.Agents
        val restored = assistantNavigator({ ConversationKind.Agent }, listOf(agent))

        restored.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, InboxRoute, InboxItemRoute("item")), stacks.getValue(AppTab.Persona))
        restored.back()
        assertEquals(InboxRoute, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aReopenedAgentUsesItsLatestItemAfterAColdRestore() {
        val existing = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("first"))
        val second = RouteSource.InboxItem("second")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, second)
        val kind: (SessionKey) -> ConversationKind = {
            if (it.sessionId == "agent") ConversationKind.Agent else ConversationKind.Assistant
        }
        val router = embeddedAssistantNavigator(kind)
        stacks.getValue(AppTab.Agents).add(existing)
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, InboxEventRoute("second"), handling))

        router.navigate(existing.copy(source = second))
        assertEquals(existing, stacks.getValue(AppTab.Agents).last())
        val persisted = router.inboxReturns()
        assertEquals(second, persisted.single().source)

        // A cold shell restores the visible Agent but only Persona's root.
        stacks.getValue(AppTab.Persona).apply { clear(); add(AssistantRoute) }
        selected = AppTab.Agents
        val restored = assistantNavigator(kind, persisted)
        assertEquals(existing, stacks.getValue(AppTab.Agents).last())
        restored.back()

        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute, InboxRoute, InboxItemRoute("second")), stacks.getValue(AppTab.Persona))
    }

    @Test fun aReopenedAgentUsesItsLatestItemAfterItsGroupMoves() {
        val existing = ChatRoute("machine~group", "agent", ConversationKind.Agent, RouteSource.InboxItem("first"))
        val moved = ChatRoute("machine", "agent", ConversationKind.Agent)
        val movedWithOriginalSource = moved.copy(source = existing.source)
        val second = RouteSource.InboxItem("second")
        val handling = ChatRoute("machine~group", "handling", ConversationKind.Assistant, second)
        val kind: (SessionKey) -> ConversationKind = {
            if (it.sessionId == "agent") ConversationKind.Agent else ConversationKind.Assistant
        }
        val router = embeddedAssistantNavigator(kind)
        stacks.getValue(AppTab.Agents).add(existing)
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, InboxEventRoute("second"), handling))

        router.navigate(existing.copy(source = second))
        assertEquals(existing, stacks.getValue(AppTab.Agents).last())
        router.groupGone(existing, moved)
        assertEquals(listOf(AgentsRoute, movedWithOriginalSource), stacks.getValue(AppTab.Agents))
        val persisted = router.inboxReturns()
        assertEquals("machine", persisted.single().machineId)
        assertEquals("agent", persisted.single().sessionId)
        assertEquals(second, persisted.single().source)

        stacks.getValue(AppTab.Persona).apply { clear(); add(AssistantRoute) }
        selected = AppTab.Agents
        val restored = assistantNavigator(kind, persisted)
        assertEquals(movedWithOriginalSource, stacks.getValue(AppTab.Agents).last())
        restored.back()

        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute, InboxRoute, InboxItemRoute("second")), stacks.getValue(AppTab.Persona))
    }

    @Test fun aDirectUnsourcedOpenAfterRestoreDoesNotReuseTheRetainedKeysInboxSource() {
        val existing = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))
        stacks.getValue(AppTab.Agents).add(existing)
        selected = AppTab.Agents
        val restored = assistantNavigator({ ConversationKind.Agent }, listOf(existing))

        restored.navigate(existing.copy(source = null))
        assertEquals(existing, stacks.getValue(AppTab.Agents).last())
        restored.back()

        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
    }

    @Test fun kindlessAgentReopensKeepOneNavKeyAndUseTheLatestInboxSource() {
        val existing = ChatRoute("machine", "agent", source = RouteSource.InboxItem("first"))
        val second = RouteSource.InboxItem("second")
        val detail = InboxEventRoute("second")
        val router = agentsOwnAgentChats()
        stacks.getValue(AppTab.Agents).add(existing)
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail))
        selected = AppTab.Persona

        router.navigate(existing.copy(source = second))

        assertEquals(listOf(AgentsRoute, existing), stacks.getValue(AppTab.Agents))
        assertEquals(second, router.inboxReturns().single().source)
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(detail, stacks.getValue(AppTab.Persona).last())

        // An ordinary Agents-list reopen is kindless too, and must not stack over the retained inbox page.
        stacks.getValue(AppTab.Agents).add(existing)
        selected = AppTab.Agents
        router.navigate(existing.copy(source = null))
        assertEquals(listOf(AgentsRoute, existing), stacks.getValue(AppTab.Agents))
        assertEquals(emptyList<ChatRoute>(), router.inboxReturns())
        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(detail, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aSameTabUnsourcedReopenClearsItsOldCrossTabOrigin() {
        val existing = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))
        val router = agentsOwnAgentChats()
        stacks.getValue(AppTab.Agents).add(existing)
        selected = AppTab.Persona
        router.navigate(existing)
        assertEquals(AppTab.Agents, selected)

        // This reopen records no new cross-tab origin; the origin from the first open must not survive either.
        router.navigate(existing.copy(source = null))

        assertEquals(listOf(AgentsRoute, existing), stacks.getValue(AppTab.Agents))
        assertEquals(emptyList<ChatRoute>(), router.inboxReturns())
        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
    }

    @Test fun notingADirectOpenClearsItsOldCrossTabOriginAndInboxSource() {
        val existing = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))
        val direct = existing.copy(source = null)
        val router = agentsOwnAgentChats()
        stacks.getValue(AppTab.Agents).add(existing)
        selected = AppTab.Persona
        router.navigate(existing)
        assertEquals(AppTab.Agents, selected)

        // Root's ResolvedRoute.open path records the open without replacing the visible key or its saved UI state.
        router.noteOpened(direct)
        assertEquals(AppTab.Agents, ResolvedRoute(AppTab.Agents, direct).open(stacks))

        assertEquals(listOf(AgentsRoute, existing), stacks.getValue(AppTab.Agents))
        assertEquals(emptyList<ChatRoute>(), router.inboxReturns())
        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
    }

    @Test fun legacyConstructionFallsBackToARetainedSourceButExplicitEmptyRestoreDoesNot() {
        val existing = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))
        stacks.getValue(AppTab.Agents).add(existing)
        selected = AppTab.Agents

        val legacy = assistantNavigator({ ConversationKind.Agent })
        legacy.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, InboxRoute, InboxItemRoute("item")), stacks.getValue(AppTab.Persona))

        stacks.getValue(AppTab.Persona).apply { clear(); add(AssistantRoute) }
        stacks.getValue(AppTab.Agents).apply { clear(); add(AgentsRoute); add(existing) }
        selected = AppTab.Agents
        val restored = assistantNavigator({ ConversationKind.Agent }, emptyList())
        restored.back()

        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
    }

    @Test fun aColdRestoredAgentRebuildsTheLatestDetailAboveAnOldDetailOrTheInboxList() {
        val existing = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("first"))
        val latest = RouteSource.InboxItem("second")
        val persisted = listOf(existing.copy(source = latest))
        val personaStarts = listOf(
            listOf<NavKey>(InboxRoute, InboxEventRoute("first")),
            listOf<NavKey>(InboxRoute),
        )

        for (start in personaStarts) {
            stacks.values.forEach { stack -> while (stack.size > 1) stack.removeAt(stack.lastIndex) }
            stacks.getValue(AppTab.Persona).addAll(start)
            stacks.getValue(AppTab.Agents).add(existing)
            selected = AppTab.Agents
            val restored = assistantNavigator({ ConversationKind.Agent }, persisted)

            restored.back()

            assertEquals(AppTab.Persona, selected)
            assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
            assertEquals(listOf(AssistantRoute) + start + InboxItemRoute("second"), stacks.getValue(AppTab.Persona))
        }
    }

    @Test fun aColdRestoredAgentDoesNotDuplicateAnyCurrentPageOfItsItem() {
        val existing = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("first"))
        val latest = RouteSource.InboxItem("second")
        val persisted = listOf(existing.copy(source = latest))
        val processing = ChatRoute("machine", "handling", ConversationKind.Assistant, latest)
        val currentPages = listOf<NavKey>(
            InboxEventRoute("second"),
            InboxItemRoute("second"),
            InboxItemUiRoute("second", "extension", "item"),
            MailRoute("second"),
            InboxItemDetailRoute("second"),
            ConnectorThreadRoute("second"),
            LarkRoute("feed", "second"),
            LarkMomentRoute("post", "second"),
            processing,
            InteractionRoute(processing, "request"),
        )

        for (current in currentPages) {
            stacks.values.forEach { stack -> while (stack.size > 1) stack.removeAt(stack.lastIndex) }
            stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, current))
            stacks.getValue(AppTab.Agents).add(existing)
            selected = AppTab.Agents
            val restored = assistantNavigator({ ConversationKind.Agent }, persisted)

            restored.back()

            assertEquals("Current page: $current", AppTab.Persona, selected)
            assertEquals(
                "Current page: $current",
                listOf<NavKey>(AssistantRoute, InboxRoute, current),
                stacks.getValue(AppTab.Persona),
            )
        }
    }

    @Test fun aKindlessSourcedPersonaChatReopenedFromAgentsKeepsItsExactNavKey() {
        val existing = ChatRoute("machine", "handling", source = RouteSource.InboxItem("first"))
        val second = RouteSource.InboxItem("second")
        val detail = InboxEventRoute("second")
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail, existing))
        selected = AppTab.Agents

        navigator.navigate(existing.copy(source = second))

        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(
            listOf<NavKey>(AssistantRoute, InboxRoute, detail, existing),
            stacks.getValue(AppTab.Persona),
        )
        assertSame(existing, stacks.getValue(AppTab.Persona).last())
        assertEquals(second, navigator.inboxReturns().single().source)
    }

    @Test fun aColdLedgerOnlyAssistantSurvivesGroupMigrationAndLateMetadataAsAPage() {
        val retained = ChatRoute("machine~group", "handling")
        val moved = ChatRoute("machine", "handling")
        val source = RouteSource.InboxItem("item")
        stacks.getValue(AppTab.Agents).add(retained)
        selected = AppTab.Agents
        var metadataKnown = false
        val kind: (SessionKey) -> ConversationKind? = { key ->
            if (metadataKnown && key.sessionId == "handling") ConversationKind.Assistant else null
        }
        val router = assistantNavigator(kind, listOf(retained.copy(source = source)))
        assertTrue(router.hasInboxOrigin(retained))

        // Group migration moves the ledger entry before late session metadata identifies the assistant.
        router.groupGone(retained, moved)
        assertTrue(router.hasInboxOrigin(moved))
        metadataKnown = true
        router.restoreChatTabs()

        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf<NavKey>(AssistantRoute, moved), stacks.getValue(AppTab.Persona))
        assertSame(moved, stacks.getValue(AppTab.Persona).last())
        assertEquals(listOf<NavKey>(AgentsRoute), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(
            listOf<NavKey>(AssistantRoute, InboxRoute, InboxItemRoute("item")),
            stacks.getValue(AppTab.Persona),
        )
    }

    @Test fun everyChildOfAColdRestoredAssistantUsesItsLedgerSource() {
        val second = RouteSource.InboxItem("second")
        val kind: (SessionKey) -> ConversationKind = {
            if (it.sessionId == "agent-child") ConversationKind.Agent else ConversationKind.Assistant
        }

        for ((child, tab) in inboxChildren()) {
            resetStacks()
            val handling = ChatRoute("machine", "handling", ConversationKind.Assistant)
            val detail = InboxEventRoute("second")
            stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail, handling))
            selected = AppTab.Persona
            // The cold visible key has no provenance; only the separately restored ledger names the second item.
            val router = assistantNavigator(kind, listOf(handling.copy(source = second)))

            router.navigate(child)

            assertEquals("Child: $child", tab, selected)
            assertEquals("Child: $child", withInboxSource(child, second), stacks.getValue(tab).last())
            assertEquals(second, router.inboxReturns().single { it.sessionId == handling.sessionId }.source)
            router.back()
            assertEquals("Child: $child", AppTab.Persona, selected)
            assertEquals("Child: $child", handling, stacks.getValue(AppTab.Persona).last())
            router.back()
            assertEquals("Child: $child", detail, stacks.getValue(AppTab.Persona).last())
        }
    }

    @Test fun everyChildOfAMigratedAssistantUsesTheLatestLedgerSource() {
        val first = RouteSource.InboxItem("first")
        val second = RouteSource.InboxItem("second")
        val kind: (SessionKey) -> ConversationKind = {
            if (it.sessionId == "agent-child") ConversationKind.Agent else ConversationKind.Assistant
        }

        for ((child, tab) in inboxChildren()) {
            resetStacks()
            val handling = ChatRoute("machine~group", "handling", ConversationKind.Assistant, first)
            val moved = ChatRoute("machine", "handling", ConversationKind.Assistant)
            val movedWithKeySource = moved.copy(source = first)
            val detail = InboxEventRoute("second")
            stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail, handling))
            selected = AppTab.Persona
            val router = assistantNavigator(kind, listOf(handling.copy(source = second)))
            router.groupGone(handling, moved)
            assertTrue(router.hasInboxOrigin(movedWithKeySource))

            // The migrated key still says first; the ledger's second item must win for every child type.
            router.navigate(child)

            assertEquals("Child: $child", tab, selected)
            assertEquals("Child: $child", withInboxSource(child, second), stacks.getValue(tab).last())
            assertEquals(second, router.inboxReturns().single { it.sessionId == handling.sessionId }.source)
            router.back()
            assertEquals("Child: $child", AppTab.Persona, selected)
            assertEquals("Child: $child", movedWithKeySource, stacks.getValue(AppTab.Persona).last())
            router.back()
            assertEquals("Child: $child", detail, stacks.getValue(AppTab.Persona).last())
        }
    }

    @Test fun explicitSourceSuppressionLeavesEveryChildUnsourced() {
        val first = RouteSource.InboxItem("first")
        val second = RouteSource.InboxItem("second")
        val kind: (SessionKey) -> ConversationKind = {
            if (it.sessionId == "agent-child") ConversationKind.Agent else ConversationKind.Assistant
        }

        for ((child, tab) in inboxChildren()) {
            resetStacks()
            val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, first)
            val detail = InboxEventRoute("second")
            stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail, handling))
            selected = AppTab.Persona
            val router = AppNavigator(
                stacks, { selected }, { selected = it },
                chatTab = { route ->
                    if (kind(SessionKey(route.machineId, route.sessionId)) == ConversationKind.Assistant) {
                        AppTab.Persona
                    } else {
                        AppTab.Agents
                    }
                },
                inboxReturns = listOf(handling.copy(source = second)),
            )
            router.noteOpened(handling.copy(source = null))
            assertFalse(router.hasInboxOrigin(handling))

            router.navigate(child)

            assertEquals("Child: $child", tab, selected)
            assertEquals("Child: $child", child, stacks.getValue(tab).last())
            assertEquals("Child: $child", emptyList<ChatRoute>(), router.inboxReturns())
            router.back()
            assertEquals("Child: $child", AppTab.Persona, selected)
            assertEquals("Child: $child", handling, stacks.getValue(AppTab.Persona).last())
            router.back()
            assertEquals("Child: $child", detail, stacks.getValue(AppTab.Persona).last())
        }
    }

    @Test fun aKindlessReopenKeepsAKnownKindChatKeyThroughGroupMigration() {
        val first = RouteSource.InboxItem("first")
        val second = RouteSource.InboxItem("second")
        val existing = ChatRoute("machine~group", "agent", ConversationKind.Agent, first)
        val moved = ChatRoute("machine", "agent", ConversationKind.Agent, first)
        val detail = InboxEventRoute("second")
        val router = agentsOwnAgentChats()
        stacks.getValue(AppTab.Agents).addAll(listOf(existing, TerminalRoute("machine~group", "agent")))
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail))
        selected = AppTab.Persona

        router.navigate(existing.copy(kind = null, source = second))

        // Router.restore pops the terminal, then the navigator restores the exact known-kind key it found.
        assertEquals(listOf<NavKey>(AgentsRoute, existing), stacks.getValue(AppTab.Agents))
        assertSame(existing, stacks.getValue(AppTab.Agents).last())
        assertEquals(second, router.inboxReturns().single().source)
        router.groupGone(existing, moved)
        assertEquals(listOf<NavKey>(AgentsRoute, moved), stacks.getValue(AppTab.Agents))
        assertSame(moved, stacks.getValue(AppTab.Agents).last())
        assertEquals(second, router.inboxReturns().single().source)
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(detail, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aKindlessSameRequestReopenKeepsTheOriginalInteractionKeyAndLatestSource() {
        val first = RouteSource.InboxItem("first")
        val second = RouteSource.InboxItem("second")
        val existing = InteractionRoute(
            ChatRoute("machine~group", "agent", ConversationKind.Agent, first), "request",
        )
        val movedChat = ChatRoute("machine", "agent", ConversationKind.Agent, first)
        val detail = InboxEventRoute("second")
        val router = agentsOwnAgentChats()
        stacks.getValue(AppTab.Agents).addAll(
            listOf(existing, TerminalRoute("machine~group", "agent")),
        )
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail))
        selected = AppTab.Persona
        val reopened = InteractionRoute(existing.chat.copy(kind = null, source = second), existing.requestId)

        router.navigate(reopened)

        assertEquals(listOf<NavKey>(AgentsRoute, existing), stacks.getValue(AppTab.Agents))
        assertSame(existing, stacks.getValue(AppTab.Agents).last())
        assertEquals(second, router.inboxReturns().single().source)

        // A known-kind request keeps its request wrapper and key provenance when its hub group changes.
        router.groupGone(existing.chat, movedChat)
        val moved = InteractionRoute(movedChat, existing.requestId)
        assertEquals(listOf<NavKey>(AgentsRoute, moved), stacks.getValue(AppTab.Agents))
        val migrated = stacks.getValue(AppTab.Agents).last()
        assertEquals(second, router.inboxReturns().single().source)

        // The same kindless reopen after migration still reuses the exact migrated InteractionRoute key.
        selected = AppTab.Persona
        router.navigate(InteractionRoute(movedChat.copy(kind = null, source = second), existing.requestId))
        assertEquals(listOf<NavKey>(AgentsRoute, moved), stacks.getValue(AppTab.Agents))
        assertSame(migrated, stacks.getValue(AppTab.Agents).last())
        assertEquals(second, router.inboxReturns().single().source)
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(detail, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aKindlessDifferentRequestReopenStillReplacesTheInteractionKey() {
        val first = RouteSource.InboxItem("first")
        val second = RouteSource.InboxItem("second")
        val existing = InteractionRoute(ChatRoute("machine", "agent", ConversationKind.Agent, first), "request")
        val reopened = InteractionRoute(existing.chat.copy(kind = null, source = second), "other-request")
        val router = agentsOwnAgentChats()
        stacks.getValue(AppTab.Agents).add(existing)
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, InboxEventRoute("second")))
        selected = AppTab.Persona

        router.navigate(reopened)

        assertEquals(listOf<NavKey>(AgentsRoute, reopened), stacks.getValue(AppTab.Agents))
        assertSame(reopened, stacks.getValue(AppTab.Agents).last())
        assertEquals(second, router.inboxReturns().single().source)
    }

    @Test fun aDirectTerminalDeepLinkClearsTheChatsInboxOriginBeforeTwoBacks() {
        val detail = InboxEventRoute("item")
        val chat = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))
        val router = agentsOwnAgentChats()
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail))
        router.navigate(chat)
        assertEquals(AppTab.Agents, selected)
        val deepLink = terminalLink("persona://open?target=terminal&machine=machine&session=agent")!!

        // Root records a direct ResolvedRoute before restoring its synthetic Agent chat and terminal.
        router.noteDirectOpen(deepLink.key)
        assertEquals(emptyList<ChatRoute>(), router.inboxReturns())
        assertEquals(AppTab.Agents, deepLink.open(stacks))
        assertEquals(
            listOf<NavKey>(AgentsRoute, chat, TerminalRoute("machine", "agent")),
            stacks.getValue(AppTab.Agents),
        )

        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf<NavKey>(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()

        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf<NavKey>(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf<NavKey>(AssistantRoute, InboxRoute, detail), stacks.getValue(AppTab.Persona))
    }

    @Test fun anInAppTerminalKeepsTheSourcedChatsReturn() {
        val detail = InboxEventRoute("item")
        val chat = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))
        val terminal = TerminalRoute("machine", "agent")
        val router = agentsOwnAgentChats()
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail))

        router.navigate(chat)
        router.navigate(terminal)
        assertEquals(listOf<NavKey>(AgentsRoute, chat, terminal), stacks.getValue(AppTab.Agents))
        assertEquals(chat.source, router.inboxReturns().single().source)
        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf<NavKey>(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()

        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf<NavKey>(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf<NavKey>(AssistantRoute, InboxRoute, detail), stacks.getValue(AppTab.Persona))
    }

    @Test fun aSourcedAssistantBackPrefersItsItemOverAnAgentsOrigin() {
        val detail = InboxEventRoute("item")
        val chat = ChatRoute("machine", "assistant", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail, chat))
        selected = AppTab.Agents

        // Reopening the visible sourced page from Agents would ordinarily record openedFrom = Agents.
        router.navigate(chat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf<NavKey>(AssistantRoute, InboxRoute, detail, chat), stacks.getValue(AppTab.Persona))
        router.back()

        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf<NavKey>(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf<NavKey>(AssistantRoute, InboxRoute, detail), stacks.getValue(AppTab.Persona))
    }

    private fun resetStacks() {
        stacks.values.forEach { stack ->
            while (stack.size > 1) stack.removeAt(stack.lastIndex)
        }
    }

    private fun inboxChildren(): List<Pair<NavKey, AppTab>> = listOf(
        ChatRoute("machine", "agent-child", ConversationKind.Agent) to AppTab.Agents,
        ChatRoute("machine", "assistant-child", ConversationKind.Assistant) to AppTab.Persona,
        InteractionRoute(
            ChatRoute("machine", "request-child", ConversationKind.Assistant), "request",
        ) to AppTab.Persona,
    )

    private fun withInboxSource(key: NavKey, source: RouteSource.InboxItem): NavKey = when (key) {
        is ChatRoute -> key.copy(source = source)
        is InteractionRoute -> key.copy(chat = key.chat.copy(source = source))
        else -> error("Not a conversation child: $key")
    }

    /** Uses Root's production decision; only its selection side effect is reproduced here. */
    private fun embeddedAssistantNavigator(kind: (SessionKey) -> ConversationKind?) = assistantNavigator(kind)

    private fun assistantNavigator(kind: (SessionKey) -> ConversationKind?, inboxReturns: List<ChatRoute>? = null) = AppNavigator(
        stacks, { selected }, { selected = it },
        chatTab = { route ->
            kind(SessionKey(route.machineId, route.sessionId))?.let {
                if (it == ConversationKind.Assistant) AppTab.Persona else AppTab.Agents
            }
        },
        openAssistant = { chat, select ->
            assistantHomeTarget(chat, embeddedAssistant = true, kind = kind)?.let {
                if (select) {
                    val stack = stacks.getValue(AppTab.Persona)
                    while (stack.size > 1) stack.removeAt(stack.lastIndex)
                    selected = AppTab.Persona
                }
                true
            } ?: false
        },
        inboxReturns = inboxReturns,
    )

    private fun agentsOwnAgentChats() = AppNavigator(stacks, { selected }, { selected = it }, chatTab = { AppTab.Agents })

    @Test fun backFromAnAgentChatOpenedInThePersonaHomeReturnsToTheHome() {
        val router = agentsOwnAgentChats()
        selected = AppTab.Persona
        val chat = ChatRoute("machine", "agent")
        router.navigate(chat)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))

        stacks.getValue(AppTab.Agents).add(chat)
        router.navigate(chat)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)

        val known = ChatRoute("machine", "known", ConversationKind.Agent)
        router.navigate(known)
        assertEquals(AppTab.Agents, selected)
        router.back()
        assertEquals(AppTab.Persona, selected)
    }

    @Test fun pagesPushedAboveACrossTabChatGoBackToItFirst() {
        val router = agentsOwnAgentChats()
        selected = AppTab.Persona
        router.navigate(InboxRoute)
        val chat = ChatRoute("machine", "agent")
        val terminal = TerminalRoute("machine", "agent")
        router.navigate(chat)
        router.navigate(terminal)
        assertEquals(listOf(AgentsRoute, chat, terminal), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(InboxRoute, stacks.getValue(selected).last())
    }

    @Test fun pickingATabForgetsWhereACrossTabPageCameFrom() {
        val router = agentsOwnAgentChats()
        selected = AppTab.Persona
        val chat = ChatRoute("machine", "agent")
        router.navigate(chat)
        router.selectTab(AppTab.Persona)
        router.selectTab(AppTab.Agents)
        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        router.navigate(chat)
        router.back()
        assertEquals(AppTab.Agents, selected)
    }
}
