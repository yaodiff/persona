package persona.feature.agents

import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*

/** New Agent's workspace choice (#7259): the rule shared with iOS and the web, and what a pick sends. */
@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class NewAgentWorkspacesTest {
    private fun ws(
        id: String, source: String = "paseo", machine: String = "laptop", title: String = id, cwd: String = "/code/$id",
        branch: String? = null, project: String? = null, name: String? = null, path: String? = null, main: Boolean? = null,
        kind: String? = "git", available: Boolean? = true, online: Boolean? = true,
    ) = AgentWorkspace("$machine/$source/$id", machine, source, id, title, cwd, branch, projectId = project, projectName = name,
        projectPath = path, isMain = main, workspaceKind = kind, online = online, available = available)

    @Test fun onlyTheComputersOrcaAndPaseoWorkspacesThatCanTakeAnAgent() {
        val rows = listOf(
            ws("paseo"), ws("orca", source = "orca"), ws("herdr", source = "herdr"), ws("botmux", source = "botmux"),
            ws("dir", source = "directory"), ws("elsewhere", machine = "studio"), ws("busy", available = false),
            ws("gone", online = false), ws("old", available = null, online = null), ws("nocwd", cwd = ""),
        )
        val ids = NewAgentWorkspaces.sections(rows, "laptop").flatMap { s -> s.rows.map { it.workspace.sourceId } }
        assertEquals(setOf("paseo", "orca", "old"), ids.toSet())
        assertTrue(NewAgentWorkspaces.sections(emptyList(), "laptop").isEmpty())
        assertTrue(NewAgentWorkspaces.sections(rows, "phone").isEmpty())
    }

    @Test fun groupedByProjectSortedByNameWithOtherWorkspacesLast() {
        val rows = listOf(
            ws("loose"),
            ws("z-main", project = "p1", name = "zeta", main = true),
            ws("a-tree", project = "p2", path = "/code/Alpha/", branch = "feature"),
            ws("b-main", project = "p2", path = "/code/Alpha", main = true),
            ws("nameless", project = "p3"),
            // the same project id from another engine is another project
            ws("orca-zeta", source = "orca", project = "p1", name = "beta"),
        )
        val sections = NewAgentWorkspaces.sections(rows, "laptop")
        assertEquals(listOf("Alpha", "beta", "zeta", null), sections.map { it.heading })
        assertEquals(listOf("b-main", "a-tree"), sections[0].rows.map { it.workspace.sourceId })
        assertEquals(setOf("loose", "nameless"), sections.last().rows.map { it.workspace.sourceId }.toSet())
    }

    @Test fun mainCheckoutThenWorktreesThenFoldersTiesByTitle() {
        val rows = listOf(
            ws("f", title = "docs", kind = "folder", project = "p", name = "persona", main = true),
            ws("w2", title = "beta", project = "p", name = "persona"),
            ws("w1", title = "Alpha", project = "p", name = "persona", main = false),
            ws("m", title = "persona", project = "p", name = "persona", main = true, branch = "main"),
        )
        val section = NewAgentWorkspaces.sections(rows, "laptop").single()
        assertEquals(listOf("m", "w1", "w2", "f"), section.rows.map { it.workspace.sourceId })
        assertEquals(listOf(NewAgentWorkspaceKind.Main, NewAgentWorkspaceKind.Worktree, NewAgentWorkspaceKind.Worktree, NewAgentWorkspaceKind.Folder),
            section.rows.map { it.kind })
    }

    @Test fun titleAndBranchWords() {
        assertEquals("feature/x", NewAgentWorkspaces.title(ws("a", title = " ", branch = "feature/x")))
        assertEquals("notes", NewAgentWorkspaces.title(ws("a", title = "", cwd = "/home/me/notes/")))
        // a branch only when it is not the title, and never on a folder
        assertEquals("feature", NewAgentWorkspaces.row(ws("a", title = "persona", branch = "feature")).branch)
        assertNull(NewAgentWorkspaces.row(ws("a", title = "main", branch = "main")).branch)
        assertNull(NewAgentWorkspaces.row(ws("a", title = "", branch = "main")).branch)
        assertNull(NewAgentWorkspaces.row(ws("a", title = "docs", branch = "main", kind = "folder")).branch)
    }

    @Test fun pickingAWorkspaceStartsInItAndAFolderOrAnotherComputerClearsIt() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val service = Service().apply {
            catalog = listOf(
                ws("main", project = "p", name = "persona", main = true, cwd = "/code/persona"),
                ws("tree", project = "p", name = "persona", cwd = "/code/persona-tree", branch = "feature"),
                ws("studio-only", machine = "studio", cwd = "/srv/app"),
            )
        }
        val store = Store()
        val machines = MutableStateFlow(listOf(Machine("laptop", online = true), Machine("studio", online = true)))
        val model = NewAgentViewModel(service, store, machines)
        try {
            advanceUntilIdle()
            val state = model.state.value
            assertEquals("laptop", state.machineId)
            // nothing picked: the remembered folder stays the default
            assertNull(state.workspace)
            assertEquals("/code/remembered", state.selection.cwd)
            assertEquals(listOf("main", "tree"), state.workspaces.single().rows.map { it.workspace.sourceId })

            val tree = state.workspaces.single().rows[1].workspace
            model.pickWorkspace(tree)
            assertEquals("/code/persona-tree", model.state.value.selection.cwd)
            assertFalse(model.state.value.worktreeOffered)
            model.chooseByFolder()
            assertNull(model.state.value.workspace)
            assertEquals("/code/remembered", model.state.value.selection.cwd)

            model.pickWorkspace(tree)
            model.useFolder("/code/persona-tree")
            assertNull(model.state.value.workspace)
            model.pickWorkspace(tree)
            model.selection(model.state.value.selection.copy(cwd = "/code/other"))
            assertNull(model.state.value.workspace)

            model.pickWorkspace(tree)
            model.selection(model.state.value.selection.copy(mode = "plan")) // not a folder: the workspace stays
            assertEquals(tree, model.state.value.workspace)
            model.submit()
            advanceUntilIdle()
            val request = requireNotNull(service.request)
            assertEquals("tree", request.workspaceId)
            assertEquals("paseo", request.host)
            assertEquals("/code/persona-tree", request.cwd)
            // a start in a workspace never replaces the computer's remembered folder
            assertTrue(store.saved.isEmpty())
        } finally {
            model.viewModelScope.cancel()
            Dispatchers.resetMain()
        }
    }

    @Test fun anotherComputerClearsThePickAndAStaleAnswerIsDropped() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val service = Service().apply {
            catalog = listOf(ws("lap", cwd = "/code/lap"), ws("stu", machine = "studio", source = "orca", cwd = "/srv/stu"))
        }
        val machines = MutableStateFlow(listOf(Machine("laptop", online = true), Machine("studio", online = true)))
        val model = NewAgentViewModel(service, Store(), machines)
        try {
            advanceUntilIdle()
            model.pickWorkspace(model.state.value.workspaces.single().rows.single().workspace)
            assertNotNull(model.state.value.workspace)
            model.selectMachine("studio")
            assertNull(model.state.value.workspace)
            assertTrue(model.state.value.workspaces.isEmpty())
            advanceUntilIdle()
            // the laptop's catalog is slow to answer and the studio is picked again: its rows never show for the studio
            val slow = CompletableDeferred<Unit>()
            service.gate = slow
            model.selectMachine("laptop")
            advanceUntilIdle()
            model.selectMachine("studio")
            slow.complete(Unit)
            advanceUntilIdle()
            assertEquals("studio", model.state.value.machineId)
            assertEquals(listOf("stu"), model.state.value.workspaces.flatMap { s -> s.rows.map { it.workspace.sourceId } })
            // a row of another computer can't be picked
            model.pickWorkspace(ws("lap", cwd = "/code/lap"))
            assertNull(model.state.value.workspace)
            model.submit()
            advanceUntilIdle()
            assertNull(service.request?.host)
            assertNull(service.request?.workspaceId)
        } finally {
            model.viewModelScope.cancel()
            Dispatchers.resetMain()
        }
    }

    @Test fun aFailedOrMissingCatalogShowsNoChoice() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val service = Service().apply { failure = IllegalStateException("404") }
        val model = NewAgentViewModel(service, Store(), MutableStateFlow(listOf(Machine("laptop", online = true))))
        try {
            advanceUntilIdle()
            assertTrue(model.state.value.workspaces.isEmpty())
            assertNull(model.state.value.error)
        } finally {
            model.viewModelScope.cancel()
            Dispatchers.resetMain()
        }
    }

    private class Store : AgentCreationStore {
        var operation: AgentStartOperation? = null
        val saved = mutableMapOf<String, AgentDefaults>()
        override suspend fun lastMachine(scope: String) = "laptop"
        override suspend fun defaults(scope: String, machine: String) = saved[machine] ?: AgentDefaults("/code/remembered", "claude")
        override suspend fun saveDefaults(scope: String, machine: String, defaults: AgentDefaults) { saved[machine] = defaults }
        override suspend fun pending(scope: String) = operation
        override suspend fun pending(scope: String, value: AgentStartOperation?) { operation = value }
    }

    private class Service : AgentCreationService {
        override val scope = "hub/pairing"
        var catalog = emptyList<AgentWorkspace>()
        var failure: Exception? = null
        var gate: CompletableDeferred<Unit>? = null
        var request: AgentStartRequest? = null
        override suspend fun launchers(machineId: String) = listOf(AgentLauncher("claude"))
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path.ifEmpty { "/code" })
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
            this.request = request
            return AgentStartResult.Created(Session(machineId = request.machineId, sessionId = "s1"))
        }
        override suspend fun operation(operationId: String): AgentStartResult = AgentStartResult.Unknown
        override suspend fun workspaces(machineId: String): List<AgentWorkspace> {
            failure?.let { throw it }
            gate?.let { gate = null; it.await() }
            return catalog.filter { it.machineId == machineId }
        }
    }
}
