package persona.feature.agents

import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*

/** A new agent's own worktree (#4532): the node's naming rules, as iOS `AgentWorktreeTests`, and New Agent's flow. */
@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class AgentWorktreeTest {
    @Test fun branchesAreNamedAsTheNodeNamesThem() {
        assertEquals("feat-a-b-c", AgentWorktree.slug("Feat/A_b.c"))
        assertEquals("persona/fix-the-login-bug", AgentWorktree.branch("Fix the login bug!"))
        assertEquals("persona/agent", AgentWorktree.branch("修一下登录"))
        val long = AgentWorktree.branch("a".repeat(39) + " b and more")
        assertEquals("persona/" + "a".repeat(39), long) // cut at 40, the trailing dash dropped
        assertEquals("修一下登录".let { AgentWorktree.nameHint(it, "/code/weather/") }, "weather")
        assertEquals("Fix it", AgentWorktree.nameHint("  Fix it ", "/code/weather"))
        val repo = WorktreeStatus(path = "/code/weather", checkouts = "/home/me/.persona/worktrees/weather/")
        assertEquals("/home/me/.persona/worktrees/weather/persona-fix-it", AgentWorktree.checkout(repo, "persona/fix-it"))
        assertEquals("C:\\wt\\persona-x", AgentWorktree.checkout(WorktreeStatus(checkouts = "C:\\wt\\"), "persona/x"))
        assertEquals("", AgentWorktree.checkout(WorktreeStatus(), "persona/x"))
    }

    @Test fun aStartInANewWorktreeMakesItFirstAndKeepsTheFolderPicked() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = Store()
        val service = Service()
        val model = NewAgentViewModel(service, store, MutableStateFlow(listOf(Machine("first", online = true))))
        try {
            advanceUntilIdle()
            model.selection(model.state.value.selection.copy(cwd = "/code/weather", launcher = "claude"))
            assertNull(model.state.value.repo) // asked a moment after the folder stops changing
            advanceUntilIdle()
            assertEquals("first:/code/weather", service.asked.last())
            assertTrue(model.state.value.worktreeOffered)
            model.inWorktree(true)
            model.worktreeBranch(" feat/x ")
            model.prompt("Fix the login bug")
            model.submit()
            advanceUntilIdle()
            assertEquals(listOf("first:/code/weather:Fix the login bug: feat/x "), service.added)
            assertEquals("/wt/feat-x", service.request?.cwd)
            assertEquals("/code/weather", store.operation?.folder)
            assertEquals("/code/weather", store.saved["first"]?.cwd)
        } finally {
            model.viewModelScope.cancel()
            Dispatchers.resetMain()
        }
    }

    @Test fun aFailedWorktreeStartsNothingAndAFolderOutsideARepositoryOffersNone() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val service = Service().apply { failAdd = true }
        val model = NewAgentViewModel(service, Store(), MutableStateFlow(listOf(Machine("first", online = true))))
        try {
            advanceUntilIdle()
            model.selection(model.state.value.selection.copy(cwd = "/code/weather", launcher = "claude"))
            advanceUntilIdle()
            model.inWorktree(true)
            model.submit()
            advanceUntilIdle()
            assertEquals(0, service.starts)
            assertEquals(R.string.agents_worktree_add_failed, model.state.value.error?.id)
            model.selection(model.state.value.selection.copy(cwd = "/tmp/notes"))
            advanceUntilIdle()
            assertFalse(model.state.value.worktreeOffered)
        } finally {
            model.viewModelScope.cancel()
            Dispatchers.resetMain()
        }
    }

    private class Store : AgentCreationStore {
        var operation: AgentStartOperation? = null
        val saved = mutableMapOf<String, AgentDefaults>()
        override suspend fun lastMachine(scope: String) = "first"
        override suspend fun defaults(scope: String, machine: String) = saved[machine] ?: AgentDefaults()
        override suspend fun saveDefaults(scope: String, machine: String, defaults: AgentDefaults) { saved[machine] = defaults }
        override suspend fun pending(scope: String) = operation
        override suspend fun pending(scope: String, value: AgentStartOperation?) { operation = value }
    }

    private class Service : AgentCreationService {
        override val scope = "hub/pairing"
        val asked = mutableListOf<String>()
        val added = mutableListOf<String>()
        var failAdd = false
        var starts = 0
        var request: AgentStartRequest? = null
        override suspend fun launchers(machineId: String) = listOf(AgentLauncher("claude"))
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path.ifEmpty { "/code" })
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
            starts++
            this.request = request
            return AgentStartResult.Created(Session(machineId = request.machineId, sessionId = "s1", cwd = request.cwd))
        }
        override suspend fun operation(operationId: String) = AgentStartResult.Unknown
        override suspend fun worktreeStatus(machineId: String, cwd: String, kind: String?): WorktreeStatus? {
            asked += "$machineId:$cwd" + (kind?.let { ":$it" } ?: "")
            return if (cwd.startsWith("/code/")) WorktreeStatus(path = cwd, checkouts = "/wt") else WorktreeStatus()
        }
        override suspend fun addWorktree(machineId: String, cwd: String, nameHint: String, branch: String, kind: String?): WorktreeAdded {
            if (failAdd) throw java.io.IOException("worktree_failed")
            added += "$machineId:$cwd:$nameHint:$branch" + (kind?.let { ":$it" } ?: "")
            return WorktreeAdded("/wt/" + AgentWorktree.slug(branch), branch.trim())
        }
    }
}
