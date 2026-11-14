package persona.feature.chat

import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.*

/** Keep or remove a worktree Persona made on Close Agent / Delete Chat (#4532), as iOS `AgentWorktreeTests`. */
class WorktreeCleanupTest {
    private val busy = WorktreeStatus(worktree = true, ours = true, path = "/wt/x", branch = "persona/x", mainBranch = "main", dirty = 2, unmerged = 3)
    private val clean = busy.copy(dirty = 0, unmerged = 0)

    @Test fun lossesMustBeConfirmedAndOnlyThenForced() {
        assertEquals(2 to 0, WorktreeCleanups.loss(busy, deleteBranch = false))
        assertEquals(2 to 3, WorktreeCleanups.loss(busy, deleteBranch = true))
        assertFalse(WorktreeCleanups.losing(busy, WorktreeCleanup()))                       // kept: nothing lost
        assertTrue(WorktreeCleanups.losing(busy, WorktreeCleanup(remove = true)))
        assertFalse(WorktreeCleanups.losing(busy.copy(dirty = 0), WorktreeCleanup(remove = true)))  // commits stay on the branch
        assertTrue(WorktreeCleanups.losing(busy.copy(dirty = 0), WorktreeCleanup(remove = true, deleteBranch = true)))
        assertFalse(WorktreeCleanups.ready(busy, WorktreeCleanup(remove = true)))
        assertTrue(WorktreeCleanups.ready(busy, WorktreeCleanup(remove = true, confirmed = true)))
        assertTrue(WorktreeCleanups.ready(null, WorktreeCleanup(remove = true)))
        assertTrue(WorktreeCleanups.force(busy, WorktreeCleanup(remove = true, confirmed = true)))
        assertFalse(WorktreeCleanups.force(clean, WorktreeCleanup(remove = true, confirmed = true)))
        assertNull(WorktreeCleanups.offered(busy.copy(ours = false)))
        assertNull(WorktreeCleanups.offered(busy.copy(path = "")))
        assertEquals(busy, WorktreeCleanups.offered(busy))
    }

    @Test fun removalIsTriedAgainWhileTheAgentIsStillListed() = runBlocking {
        class InUse : Exception("in use"), HubCodedFailure { override val status = 409; override val code = "worktree_in_use" }
        val calls = mutableListOf<String>()
        var refusals = 2
        val service = object : AgentCreationService {
            override val scope = "hub"
            override suspend fun launchers(machineId: String) = emptyList<AgentLauncher>()
            override suspend fun directories(machineId: String, path: String) = AgentDirectories(path)
            override suspend fun start(request: AgentStartRequest, operationId: String) = AgentStartResult.Unknown
            override suspend fun operation(operationId: String) = AgentStartResult.Unknown
            override suspend fun removeWorktree(machineId: String, path: String, deleteBranch: Boolean, force: Boolean, kind: String?): WorktreeRemoved {
                calls += "$machineId:$path:$deleteBranch:$force:$kind"
                if (refusals-- > 0) throw InUse()
                return WorktreeRemoved(true, deleteBranch)
            }
        }
        val c = WorktreeCleanup(remove = true, deleteBranch = true, confirmed = true)
        assertEquals(WorktreeRemoved(true, true), WorktreeCleanups.remove(service, "mac", busy, c, wait = 0))
        assertEquals(List(3) { "mac:/wt/x:true:true:null" }, calls)
        refusals = 10
        assertTrue(runCatching { WorktreeCleanups.remove(service, "mac", busy, c, tries = 2, wait = 0) }.exceptionOrNull() is InUse)
        // by the engine the agent ran in, which made it (a Paseo agent's is Paseo's)
        calls.clear()
        refusals = 0
        WorktreeCleanups.remove(service, "mac", busy, c, kind = "paseo", wait = 0)
        assertEquals(listOf("mac:/wt/x:true:true:paseo"), calls)
    }
}
