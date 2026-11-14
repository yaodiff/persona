package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.navigation.AgentsRoute
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.InboxRoute
import persona.core.navigation.RouteSource
import persona.feature.inbox.InboxEventRoute

/** Round-four persistence coverage for explicit navigation away from an inbox origin. */
class InboxReturnRound4Test {
    @Test fun aDirectUnsourcedOpenDoesNotRegainItsOldInboxReturnAfterAnotherColdRestore() {
        val retained = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("old-item"))
        val stacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute),
            AppTab.Agents to mutableListOf(AgentsRoute, retained),
        )
        var selected = AppTab.Agents
        fun navigator(inboxReturns: List<ChatRoute>) = AppNavigator(
            stacks,
            { selected },
            { selected = it },
            chatTab = { AppTab.Agents },
            inboxReturns = inboxReturns,
        )

        val direct = navigator(listOf(retained))
        direct.navigate(retained.copy(source = null))
        // Reusing the existing page must not replace its NavKey or its saved UI state.
        assertEquals(retained, stacks.getValue(AppTab.Agents).last())
        assertTrue(direct.inboxReturns().isEmpty())

        // ShellPosition also retains that visible key. A later cold restore must remember the explicit unsourced open,
        // rather than reviving the key's old source now that the in-memory suppression is gone.
        val restored = navigator(direct.inboxReturns())
        restored.back()

        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf<NavKey>(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf<NavKey>(AssistantRoute), stacks.getValue(AppTab.Persona))
    }

    @Test fun aKindlessAgentReopenedFromASecondItemKeepsItsExistingNavKey() {
        val existing = ChatRoute("machine", "agent", source = RouteSource.InboxItem("first-item"))
        val reopened = ChatRoute("machine", "agent", source = RouteSource.InboxItem("second-item"))
        val detail = InboxEventRoute("second-item")
        val stacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute, InboxRoute, detail),
            AppTab.Agents to mutableListOf(AgentsRoute, existing),
        )
        var selected = AppTab.Persona
        val navigator = AppNavigator(
            stacks,
            { selected },
            { selected = it },
            // The session metadata now owns this kindless chain route, as it does after a late first open.
            chatTab = { AppTab.Agents },
        )

        navigator.navigate(reopened)

        assertEquals(listOf<NavKey>(AgentsRoute, existing), stacks.getValue(AppTab.Agents))
        assertEquals(reopened.source, navigator.inboxReturns().single().source)
        navigator.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(detail, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aDirectKindlessAgentsListOpenDoesNotStackOverItsRetainedInboxPage() {
        val retained = ChatRoute("machine", "agent", source = RouteSource.InboxItem("old-item"))
        val stacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute),
            AppTab.Agents to mutableListOf(AgentsRoute, retained),
        )
        var selected = AppTab.Agents
        val navigator = AppNavigator(
            stacks,
            { selected },
            { selected = it },
            chatTab = { AppTab.Agents },
            inboxReturns = listOf(retained),
        )

        // AgentsScreen and AgentsEntries open ordinary rows without a ConversationKind.
        navigator.navigate(retained.copy(source = null))

        assertEquals(listOf<NavKey>(AgentsRoute, retained), stacks.getValue(AppTab.Agents))
        navigator.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf<NavKey>(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf<NavKey>(AssistantRoute), stacks.getValue(AppTab.Persona))
    }
}
