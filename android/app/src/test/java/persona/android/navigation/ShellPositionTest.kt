package persona.android.navigation

import android.content.Context
import androidx.compose.runtime.snapshotFlow
import androidx.compose.runtime.snapshots.Snapshot
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.core.navigation.*
import java.io.File

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
@OptIn(ExperimentalCoroutinesApi::class)
class ShellPositionTest {
    private val context = ApplicationProvider.getApplicationContext<Context>()
    private val scope = "navigation-test"
    private val file = File(context.noBackupFilesDir, "hub-data/$scope/shell-position.json")

    @Test fun defaultAndInvalidPositionsOpenPersona() {
        assertEquals(AppTab.Persona, ShellPosition().tab)
        file.parentFile!!.mkdirs()
        for (json in listOf("not json", """{"tab":"Inbox","machine":"example"}""",
            """{"tab":"Settings","machine":"example"}""",
            """{"tab":"Agents","chats":{"Inbox":{"machineId":"example","sessionId":"old"}}}""")) {
            file.writeText(json)
            assertEquals(json, ShellPosition(), ShellPositionStore(context, scope).load())
            assertEquals(json, json, file.readText())
        }
    }

    @Test fun explicitEmptyInboxReturnsRoundTrip() {
        file.parentFile!!.mkdirs()
        val store = ShellPositionStore(context, scope)
        val position = ShellPosition(inboxReturns = emptyList())
        store.save(position)
        val restored = store.load()
        assertEquals(emptyList<ChatRoute>(), restored.inboxReturns)
        assertEquals(position, restored)
    }

    @Test fun legacyProvenanceSurvivesTheSecondAutomaticSaveAndColdRestore() {
        val scope = "navigation-legacy-second-save"
        val legacyFile = File(context.noBackupFilesDir, "hub-data/$scope/shell-position.json")
        legacyFile.parentFile!!.mkdirs()
        legacyFile.writeText(
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

        // Root's first snapshotFlow collection saves provenance without any further navigation.
        store.save(legacy.copy(inboxReturns = first.inboxReturns()))
        val restored = store.load()
        assertEquals(RouteSource.InboxItem("item"), restored.inboxReturns?.single()?.source)
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

    @Test fun provenanceMutationEmitsWhenTheVisibleShellDoesNotChange() = runTest {
        val retained = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("first"))
        val second = RouteSource.InboxItem("second")
        var selected = AppTab.Agents
        val stacks = mapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to mutableListOf(AssistantRoute),
            AppTab.Agents to mutableListOf(AgentsRoute, retained),
        )
        val navigator = AppNavigator(
            stacks, { selected }, { selected = it }, chatTab = { AppTab.Agents },
            inboxReturns = listOf(retained),
        )
        val positions = mutableListOf<ShellPosition>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) {
            snapshotFlow {
                ShellPosition(selected, "machine", stacks.mapNotNull { (tab, stack) ->
                    stack.filterIsInstance<ChatRoute>().lastOrNull()?.let { tab to it }
                }.toMap(), navigator.inboxReturns())
            }.distinctUntilChanged().collect { positions += it }
        }
        runCurrent()
        val initial = positions.single()
        assertEquals(retained.source, initial.inboxReturns?.single()?.source)
        val visible = stacks.getValue(selected).last()

        navigator.noteOpened(retained.copy(source = second))
        Snapshot.sendApplyNotifications()
        runCurrent()

        assertEquals(2, positions.size)
        val updated = positions.last()
        assertEquals(second, updated.inboxReturns?.single()?.source)
        assertEquals(initial.copy(inboxReturns = updated.inboxReturns), updated)
        assertEquals(AppTab.Agents, selected)
        assertEquals(visible, stacks.getValue(selected).last())
    }

    @Test fun currentTwoTabPositionsRoundTrip() {
        file.parentFile!!.mkdirs()
        val store = ShellPositionStore(context, scope)
        for (tab in AppTab.entries) {
            val position = ShellPosition(tab, "example", mapOf(tab to ChatRoute("example", "session")),
                inboxReturns = listOf(ChatRoute("example", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))))
            store.save(position)
            assertEquals(position, store.load())
        }
    }
}
