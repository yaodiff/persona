package persona.android.navigation

import android.content.Context
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.core.navigation.AgentsRoute
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.InboxItemRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.RouteSource
import persona.feature.inbox.InboxEventRoute
import java.io.File

/** Persistence regressions for inbox return provenance. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class InboxReturnRound5Test {
    private val context = ApplicationProvider.getApplicationContext<Context>()

    @Test fun aLegacyShellWithoutInboxReturnsKeepsItsVisibleAgentsProvenance() {
        val scope = "inbox-return-round5-legacy"
        val file = File(context.noBackupFilesDir, "hub-data/$scope/shell-position.json")
        file.parentFile!!.mkdirs()
        // ShellPosition before inboxReturns was added. Its visible Agent already carries RouteSource.InboxItem.
        file.writeText(
            """{"tab":"Agents","chats":{"Agents":{"machineId":"machine","sessionId":"agent","kind":"Agent",""" +
                """"source":{"type":"persona.core.navigation.RouteSource.InboxItem","itemId":"item"}}}}""",
        )
        val saved = ShellPositionStore(context, scope).load()
        assertNotNull(saved.chats[AppTab.Agents])
        val agent = saved.chats.getValue(AppTab.Agents)
        val stacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute),
            AppTab.Agents to mutableListOf(AgentsRoute, agent),
        )
        var selected = AppTab.Agents
        // This is the production Root construction with the decoded legacy position.
        val navigator = AppNavigator(
            stacks,
            { selected },
            { selected = it },
            chatTab = { AppTab.Agents },
            inboxReturns = saved.inboxReturns,
        )

        navigator.back()

        assertEquals(AppTab.Persona, selected)
        assertEquals(
            listOf<NavKey>(AssistantRoute, InboxRoute, InboxItemRoute("item")),
            stacks.getValue(AppTab.Persona),
        )
    }

    @Test fun legacyProvenanceSurvivesTheNextAutomaticShellSaveAndColdRestore() {
        val scope = "inbox-return-round5-second-restore"
        val file = File(context.noBackupFilesDir, "hub-data/$scope/shell-position.json")
        file.parentFile!!.mkdirs()
        file.writeText(
            """{"tab":"Agents","chats":{"Agents":{"machineId":"machine","sessionId":"agent","kind":"Agent",""" +
                """"source":{"type":"persona.core.navigation.RouteSource.InboxItem","itemId":"item"}}}}""",
        )
        val store = ShellPositionStore(context, scope)
        val legacy = store.load()
        val agent = legacy.chats.getValue(AppTab.Agents)
        var selected = AppTab.Agents
        val firstStacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute),
            AppTab.Agents to mutableListOf(AgentsRoute, agent),
        )
        val first = AppNavigator(
            firstStacks, { selected }, { selected = it }, chatTab = { AppTab.Agents },
            inboxReturns = legacy.inboxReturns,
        )

        // Root's initial snapshotFlow collection saves this value without requiring another navigation action.
        store.save(legacy.copy(inboxReturns = first.inboxReturns()))
        val restored = store.load()
        val secondStacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute),
            AppTab.Agents to mutableListOf(AgentsRoute, agent),
        )
        val second = AppNavigator(
            secondStacks, { selected }, { selected = it }, chatTab = { AppTab.Agents },
            inboxReturns = restored.inboxReturns,
        )
        second.back()

        assertEquals(AppTab.Persona, selected)
        assertEquals(
            listOf<NavKey>(AssistantRoute, InboxRoute, InboxItemRoute("item")),
            secondStacks.getValue(AppTab.Persona),
        )
    }

    @Test fun aDirectKindlessReopenBeforeMetadataKeepsTheRestoredAgentKey() {
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
            // A restored chain route is kindless and the session list has not loaded yet.
            chatTab = { null },
            inboxReturns = listOf(retained),
        )

        navigator.navigate(retained.copy(source = null))

        assertEquals(listOf<NavKey>(AgentsRoute, retained), stacks.getValue(AppTab.Agents))
        assertEquals(emptyList<ChatRoute>(), navigator.inboxReturns())
        navigator.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf<NavKey>(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf<NavKey>(AssistantRoute), stacks.getValue(AppTab.Persona))
    }

    @Test fun aSecondItemKindlessReopenBeforeMetadataKeepsTheRestoredAgentKey() {
        val retained = ChatRoute("machine", "agent", source = RouteSource.InboxItem("first-item"))
        val second = RouteSource.InboxItem("second-item")
        val detail = InboxEventRoute("second-item")
        val stacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute, InboxRoute, detail),
            AppTab.Agents to mutableListOf(AgentsRoute, retained),
        )
        var selected = AppTab.Persona
        val navigator = AppNavigator(
            stacks,
            { selected },
            { selected = it },
            chatTab = { null },
            inboxReturns = listOf(retained),
        )

        navigator.navigate(retained.copy(source = second))

        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf<NavKey>(AgentsRoute, retained), stacks.getValue(AppTab.Agents))
        assertEquals(listOf<NavKey>(AssistantRoute, InboxRoute, detail), stacks.getValue(AppTab.Persona))
        assertEquals(second, navigator.inboxReturns().single().source)
        navigator.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(detail, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aSecondItemKindlessProcessingOpenDoesNotPopItsDetail() {
        val firstDetail = InboxEventRoute("first-item")
        val secondDetail = InboxEventRoute("second-item")
        val retained = ChatRoute("machine", "handling", source = RouteSource.InboxItem("first-item"))
        val reopened = retained.copy(source = RouteSource.InboxItem("second-item"))
        val stacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute, InboxRoute, firstDetail, retained, secondDetail),
            AppTab.Agents to mutableListOf(AgentsRoute),
        )
        var selected = AppTab.Persona
        val navigator = AppNavigator(
            stacks,
            { selected },
            { selected = it },
            chatTab = { null },
            inboxReturns = listOf(retained),
        )

        navigator.navigate(reopened)

        assertEquals(reopened, stacks.getValue(AppTab.Persona).last())
        navigator.back()
        assertEquals(secondDetail, stacks.getValue(AppTab.Persona).last())
    }
}
