package persona.android.navigation

import android.content.Context
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
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
import java.io.File

/** Cold-restore regressions for inbox return provenance. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class InboxReturnRound6Test {
    private val context = ApplicationProvider.getApplicationContext<Context>()

    @Test fun aLegacyAgentReturnSurvivesRepeatedColdShellRestores() {
        val scope = "inbox-return-round6-legacy-twice"
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

        // Root saves this snapshot automatically, without another navigation action.
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

    @Test fun aColdRestoredSourcedChatMovedToPersonaReturnsToItsDetail() {
        val chat = ChatRoute("machine", "handling", source = RouteSource.InboxItem("item"))
        val stacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute),
            AppTab.Agents to mutableListOf(AgentsRoute, chat),
        )
        var selected = AppTab.Agents
        var owner: AppTab? = null
        val navigator = AppNavigator(
            stacks, { selected }, { selected = it }, chatTab = { owner },
            inboxReturns = listOf(chat),
        )

        // The cold shell restored the kindless chat under Agents. Late metadata owns it under Persona.
        owner = AppTab.Persona
        navigator.restoreChatTabs()
        assertEquals(AppTab.Persona, selected)
        navigator.back()

        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf<NavKey>(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(
            listOf<NavKey>(AssistantRoute, InboxRoute, InboxItemRoute("item")),
            stacks.getValue(AppTab.Persona),
        )
    }
}
