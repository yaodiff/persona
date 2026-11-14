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
import persona.feature.inbox.InboxEventRoute

/** Final restoration coverage for provenance held separately from the visible NavKey. */
class InboxReturnFinalTest {
    @Test fun aColdRestoredLedgerOnlyAssistantKeepsItsPageAndReturnsToDetail() {
        // Reopening this kindless page from an item retained its unsourced key and saved UI state. Only inboxReturns
        // records the item. The cold shell restores it under Agents before session metadata identifies an assistant.
        val retained = ChatRoute("machine", "handling")
        val persisted = retained.copy(source = RouteSource.InboxItem("item"))
        val stacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute),
            AppTab.Agents to mutableListOf(AgentsRoute, retained),
        )
        var selected = AppTab.Agents
        var metadataKnown = false
        fun kind(key: SessionKey): ConversationKind? =
            if (metadataKnown && key == SessionKey("machine", "handling")) ConversationKind.Assistant else null
        val navigator = AppNavigator(
            stacks,
            { selected },
            { selected = it },
            chatTab = { route ->
                kind(SessionKey(route.machineId, route.sessionId))?.let {
                    if (it == ConversationKind.Assistant) AppTab.Persona else AppTab.Agents
                }
            },
            openAssistant = { chat, select ->
                assistantHomeTarget(chat, embeddedAssistant = true, kind = ::kind)?.let {
                    if (select) {
                        val persona = stacks.getValue(AppTab.Persona)
                        while (persona.size > 1) persona.removeAt(persona.lastIndex)
                        selected = AppTab.Persona
                    }
                    true
                } ?: false
            },
            inboxReturns = listOf(persisted),
        )

        metadataKnown = true
        navigator.restoreChatTabs()

        // Ledger-only provenance must keep the exact retained page out of the embedded home, just like key provenance.
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf<NavKey>(AssistantRoute, retained), stacks.getValue(AppTab.Persona))
        assertEquals(listOf<NavKey>(AgentsRoute), stacks.getValue(AppTab.Agents))

        navigator.back()
        assertEquals(
            listOf<NavKey>(AssistantRoute, InboxRoute, InboxItemRoute("item")),
            stacks.getValue(AppTab.Persona),
        )
    }

    @Test fun aChildOfARestoredReopenedAssistantUsesTheLatestOriginForBothBacks() {
        val first = RouteSource.InboxItem("first")
        val second = RouteSource.InboxItem("second")
        val retained = ChatRoute("machine", "handling", source = first)
        val detail = InboxEventRoute("second")
        val child = ChatRoute("machine", "child")
        val stacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute, InboxRoute, detail),
            AppTab.Agents to mutableListOf(AgentsRoute, retained),
        )
        var selected = AppTab.Agents
        fun kind(key: SessionKey): ConversationKind = when (key.sessionId) {
            "handling" -> ConversationKind.Assistant
            else -> ConversationKind.Agent
        }
        val navigator = AppNavigator(
            stacks,
            { selected },
            { selected = it },
            chatTab = { route ->
                if (kind(SessionKey(route.machineId, route.sessionId)) == ConversationKind.Assistant) {
                    AppTab.Persona
                } else {
                    AppTab.Agents
                }
            },
            openAssistant = { chat, select ->
                assistantHomeTarget(chat, embeddedAssistant = true, kind = ::kind)?.let {
                    if (select) selected = AppTab.Persona
                    true
                } ?: false
            },
            // The second item reopened the retained route without replacing its first-item NavKey.
            inboxReturns = listOf(retained.copy(source = second)),
        )
        navigator.restoreChatTabs()
        assertEquals(retained, stacks.getValue(AppTab.Persona).last())

        navigator.navigate(child)

        assertEquals(second, (stacks.getValue(AppTab.Agents).last() as ChatRoute).source)
        navigator.back()
        assertEquals("The first Back returns to the processing chat, not a duplicate detail", retained,
            stacks.getValue(AppTab.Persona).last())
        navigator.back()
        assertEquals(detail, stacks.getValue(AppTab.Persona).last())
    }
}
