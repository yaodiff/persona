package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.SessionKey
import persona.core.navigation.AgentsRoute
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.InboxItemRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.RouteSource
import persona.core.navigation.tabRoot
import persona.feature.inbox.InboxEventRoute

/** Adversarial coverage for provenance left behind by an earlier inbox navigation. */
class InboxReturnAdversarialTest {
    private val stacks = mapOf<AppTab, MutableList<NavKey>>(
        AppTab.Persona to mutableListOf(AssistantRoute),
        AppTab.Agents to mutableListOf(AgentsRoute),
    )
    private var selected = AppTab.Persona

    private val navigator = newNavigator()

    private fun newNavigator(inboxReturns: List<ChatRoute> = emptyList()) = AppNavigator(
        stacks,
        { selected },
        { selected = it },
        chatTab = { route ->
            when (route.kind) {
                ConversationKind.Agent -> AppTab.Agents
                ConversationKind.Assistant -> AppTab.Persona
                null -> null
            }
        },
        inboxReturns = inboxReturns,
    )

    @Test fun anAgentLaterOpenedFromTheAgentsListDoesNotReuseItsOldInboxReturn() {
        val detail = InboxEventRoute("item")
        val handling = ChatRoute(
            "machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("item"),
        )
        val agentKey = SessionKey("machine", "agent")
        val sourcedAgent = ChatRoute(
            agentKey.machineId, agentKey.sessionId, ConversationKind.Agent, RouteSource.InboxItem("item"),
        )
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, detail, handling))

        navigator.navigate(sourcedAgent)
        navigator.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())

        // A later ordinary Agents-list open has no inbox source and must retain its legacy Back behavior.
        navigator.selectTab(AppTab.Agents)
        navigator.navigate(ChatRoute(agentKey.machineId, agentKey.sessionId, ConversationKind.Agent))
        navigator.back()

        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf<NavKey>(tabRoot(AppTab.Agents)), stacks.getValue(AppTab.Agents))
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aColdReturnUsesTheLatestItemWhenTheExistingAgentKeyHasOlderSource() {
        val existing = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("first"))
        val secondDetail = InboxEventRoute("second")
        val secondHandling = ChatRoute(
            "machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("second"),
        )
        stacks.getValue(AppTab.Agents).add(existing)
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, secondDetail, secondHandling))
        selected = AppTab.Persona

        // Reopening preserves the existing Agent key and its UI state, but records the new item separately.
        navigator.navigate(existing.copy(source = RouteSource.InboxItem("second")))
        assertEquals(existing, stacks.getValue(AppTab.Agents).last())
        val persisted = navigator.inboxReturns()
        assertEquals(RouteSource.InboxItem("second"), persisted.single().source)

        // The production cold shell restores only the visible Agent, not the embedded-assistant detail stack.
        stacks.getValue(AppTab.Persona).apply { clear(); add(AssistantRoute) }
        stacks.getValue(AppTab.Agents).apply { clear(); add(AgentsRoute); add(existing) }
        selected = AppTab.Agents
        val restored = newNavigator(persisted)
        restored.back()

        assertEquals(AppTab.Persona, selected)
        assertEquals(
            listOf<NavKey>(AssistantRoute, InboxRoute, InboxItemRoute("second")),
            stacks.getValue(AppTab.Persona),
        )
    }
}
