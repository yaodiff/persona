package persona.feature.agents

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.domain.sort.*

internal open class PaseoCatalogTestService : AgentCreationService {
    override val scope = "test-pairing"
    var rows: List<AgentWorkspace> = emptyList()
    var failure: Exception? = null
    var complete = true
    var failedSources: Set<String> = emptySet()
    val reads = mutableListOf<String>()
    override suspend fun workspaceCatalog(machineId: String): AgentWorkspaceCatalog {
        reads += machineId
        failure?.let { throw it }
        return AgentWorkspaceCatalog(rows.filter { it.machineId == machineId }, complete, failedSources)
    }
    override suspend fun launchers(machineId: String) = emptyList<AgentLauncher>()
    override suspend fun directories(machineId: String, path: String) = error("Not used")
    override suspend fun start(request: AgentStartRequest, operationId: String) = error("Not used")
    override suspend fun operation(operationId: String) = error("Not used")
}

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class AgentsWorkspaceCatalogTest {
    private fun workspace(pin: String? = "2026-11-16T10:00:00Z", machine: String = "computer") =
        AgentWorkspace(machineId = machine, source = "paseo", sourceId = "workspace", title = "Pinned workspace",
            cwd = "/code/project", pinnedAt = pin, online = true, available = true)

    @Test fun successfulRefreshClearsPinsAndRemovesWorkspacesWhileFailureRetainsThem() = runTest {
        val service = PaseoCatalogTestService().apply { rows = listOf(workspace()) }
        val catalog = AgentsWorkspaceCatalog()
        val machines = listOf("computer" to true)
        catalog.refresh(service, machines)
        assertNotNull(catalog.rows.value.single().pinnedAt)
        service.failure = IllegalStateException("offline")
        catalog.refresh(service, machines)
        assertNotNull(catalog.rows.value.single().pinnedAt)
        service.failure = null
        service.rows = listOf(workspace(pin = null))
        catalog.refresh(service, machines)
        assertNull(catalog.rows.value.single().pinnedAt)
        service.rows = emptyList()
        catalog.refresh(service, machines)
        assertTrue(catalog.rows.value.isEmpty())
    }

    /** Orca's rows feed only the worktree actions (#17054): never the repository rows, kept through a failed read. */
    @Test fun orcaRowsAreKeptApartFromTheRepositoryRows() = runTest {
        val orca = AgentWorkspace(machineId = "computer", source = "orca", sourceId = "repo::/code/app", title = "app", cwd = "/code/app")
        val orcaOnly = orca.copy(machineId = "laptop", sourceId = "repo::/code/web", cwd = "/code/web")
        val service = PaseoCatalogTestService().apply { rows = listOf(workspace(), orca, orcaOnly, orcaOnly.copy(source = "herdr", sourceId = "w1")) }
        val catalog = AgentsWorkspaceCatalog()
        catalog.refresh(service, listOf("computer" to true), orcaOnly = listOf("laptop" to false))
        assertEquals(listOf("paseo"), catalog.rows.value.map { it.source })
        assertEquals(listOf(orca.sourceId, orcaOnly.sourceId), catalog.orcaRows.value.map { it.sourceId })
        // An offline computer's rows are kept but can take nothing.
        assertEquals(false, catalog.orcaRows.value.single { it.machineId == "laptop" }.available)
        service.failedSources = setOf("orca")
        service.complete = false
        service.rows = listOf(workspace())
        catalog.refresh(service, listOf("computer" to true), orcaOnly = listOf("laptop" to true))
        assertEquals(listOf(orca.sourceId), catalog.orcaRows.value.filter { it.machineId == "computer" }.map { it.sourceId })
        service.failedSources = emptySet()
        service.complete = true
        catalog.refresh(service, listOf("computer" to true), orcaOnly = listOf("laptop" to true))
        assertTrue(catalog.orcaRows.value.isEmpty())
    }

    @Test fun partialReadsRetainOnlyTheFailingSourcesRows() = runTest {
        val paseo = workspace()
        val herdr = paseo.copy(source = "herdr")
        val machines = listOf("computer" to true)
        val service = PaseoCatalogTestService().apply { rows = listOf(paseo, herdr) }
        val catalog = AgentsWorkspaceCatalog()
        catalog.refresh(service, machines)
        assertEquals(setOf("herdr", "paseo"), catalog.rows.value.map { it.source }.toSet())
        service.rows = emptyList()
        service.failedSources = setOf("herdr")
        catalog.refresh(service, machines)
        assertEquals(listOf(herdr), catalog.rows.value)
        service.failedSources = emptySet()
        catalog.refresh(service, machines)
        assertTrue(catalog.rows.value.isEmpty())
        service.rows = listOf(paseo, herdr)
        catalog.refresh(service, machines)
        service.rows = emptyList()
        service.failedSources = setOf("paseo")
        service.complete = false
        catalog.refresh(service, machines)
        assertEquals(listOf(paseo), catalog.rows.value)
    }

    @Test fun offlineAndReconnectReadTheHubAndKeepSourceIdentity() = runTest {
        val service = PaseoCatalogTestService().apply { rows = listOf(workspace(), workspace(machine = "other")) }
        val catalog = AgentsWorkspaceCatalog()
        catalog.refresh(service, listOf("computer" to true, "other" to true))
        assertEquals(2, catalog.rows.value.size)
        service.reads.clear()
        catalog.refresh(service, listOf("computer" to false, "other" to true))
        assertEquals(setOf("computer", "other"), service.reads.toSet())
        assertFalse(catalog.rows.value.single { it.machineId == "computer" }.online!!)
        assertNotNull(catalog.rows.value.single { it.machineId == "computer" }.pinnedAt)
        service.rows = listOf(workspace(pin = null))
        catalog.refresh(service, listOf("computer" to true))
        assertEquals(listOf("computer"), catalog.rows.value.map { it.machineId })
        assertTrue(catalog.rows.value.single().online!!)
        assertNull(catalog.rows.value.single().pinnedAt)
    }

    @Test fun anOlderReadCannotRestoreAPinAfterANewerReadClearedIt() = runTest {
        val pending = CompletableDeferred<AgentWorkspaceCatalog>()
        val slow = object : PaseoCatalogTestService() {
            override suspend fun workspaceCatalog(machineId: String) = pending.await()
        }
        val fresh = PaseoCatalogTestService().apply { rows = listOf(workspace(pin = null)) }
        val catalog = AgentsWorkspaceCatalog()
        val stale = launch { catalog.refresh(slow, listOf("computer" to true)) }
        runCurrent()
        catalog.refresh(fresh, listOf("computer" to true))
        pending.complete(AgentWorkspaceCatalog(listOf(workspace())))
        stale.join()
        assertNull(catalog.rows.value.single().pinnedAt)
    }

    @Test fun aColdOfflineListRestoresAndDrawsAnEmptyPinnedWorkspaceFromTheHub() = runTest {
        val service = PaseoCatalogTestService().apply { rows = listOf(workspace()) }
        val catalog = AgentsWorkspaceCatalog()
        catalog.refresh(service, listOf("computer" to false))
        assertEquals(listOf("computer"), service.reads)
        val row = catalog.rows.value.single()
        assertNotNull(row.pinnedAt)
        assertFalse(row.online!!)
        assertFalse(row.available!!)
        val list = buildAgentList(emptyList(), listOf(Machine("computer", online = false)),
            settings = AgentListSettings(workspaces = catalog.rows.value))
        val pin = list.groups.single { it.worktree?.pinned == true }
        assertFalse(pin.folded)
        assertTrue(pin.worktree!!.offline)
        assertTrue(pin.rows.isEmpty())
    }

    @Test fun aCompleteOfflineRefreshCanClearAPinAndRemoveItsWorkspace() = runTest {
        val service = PaseoCatalogTestService().apply { rows = listOf(workspace()) }
        val catalog = AgentsWorkspaceCatalog()
        val machines = listOf("computer" to false)
        catalog.refresh(service, machines)
        service.rows = listOf(workspace(pin = null))
        catalog.refresh(service, machines)
        assertNull(catalog.rows.value.single().pinnedAt)
        assertFalse(catalog.rows.value.single().available!!)
        service.rows = emptyList()
        catalog.refresh(service, machines)
        assertTrue(catalog.rows.value.isEmpty())
    }

    @Test fun partialRowsRestoreAColdCacheAndPreserveOnlyMissingRowsOfTheSameSource() = runTest {
        val service = object : PaseoCatalogTestService() {
            override suspend fun workspaceCatalog(machineId: String) = AgentWorkspaceCatalog(rows, complete)
        }.apply { rows = listOf(workspace()); complete = false }
        val catalog = AgentsWorkspaceCatalog()
        val machines = listOf("computer" to true)
        catalog.refresh(service, machines)
        assertNotNull(catalog.rows.value.single().pinnedAt)
        val missing = workspace().copy(sourceId = "missing")
        service.complete = true
        service.rows = listOf(workspace(), missing)
        catalog.refresh(service, machines)
        service.complete = false
        service.rows = listOf(workspace(pin = null), workspace(machine = "other"), workspace().copy(source = "orca"))
        catalog.refresh(service, machines)
        assertEquals(setOf("workspace", "missing"), catalog.rows.value.map { it.sourceId }.toSet())
        assertNull(catalog.rows.value.single { it.sourceId == "workspace" }.pinnedAt)
        assertEquals(missing, catalog.rows.value.single { it.sourceId == "missing" })
        assertTrue(catalog.rows.value.all { it.machineId == "computer" && it.source == "paseo" })
        service.complete = true
        service.rows = listOf(workspace(pin = null))
        catalog.refresh(service, machines)
        assertEquals(listOf("workspace"), catalog.rows.value.map { it.sourceId })
    }

    @Test fun cancellationKeepsTheCacheAndNewPairingNeverInheritsItsPartialRows() = runTest {
        val service = PaseoCatalogTestService().apply { rows = listOf(workspace()) }
        val catalog = AgentsWorkspaceCatalog()
        val machines = listOf("computer" to true)
        catalog.refresh(service, machines)
        val pending = CompletableDeferred<AgentWorkspaceCatalog>()
        val slow = object : PaseoCatalogTestService() {
            override suspend fun workspaceCatalog(machineId: String) = pending.await()
        }
        val request = launch { catalog.refresh(slow, machines) }
        runCurrent()
        request.cancelAndJoin()
        assertNotNull(catalog.rows.value.single().pinnedAt)
        val anotherPairing = object : PaseoCatalogTestService() { override val scope = "another-pairing" }
            .apply { complete = false }
        catalog.refresh(anotherPairing, machines)
        assertTrue(catalog.rows.value.isEmpty())
    }

    @Test fun allRangePlacesAStoppedPinnedChatInItsWorkspaceOnlyOnce() {
        val workspace = workspace()
        val stopped = AgentSession(Session(machineId = workspace.machineId, sessionId = "stopped", cwd = workspace.cwd,
            status = "offline", state = "offline", workspaceId = workspace.sourceId, sourceKind = "runtime", sourceInstanceId = "paseo"))
        val result = scopedAgentList(listOf(stopped), listOf(Machine("computer", online = true)),
            AgentsPreferencesSnapshot(range = AgentRange.All), AgentListSettings(workspaces = listOf(workspace)))
        assertEquals(1, result.groups.flatMap { it.rows }.count { it.id == stopped.id })
        assertTrue(result.groups.none { it.id.startsWith("history/") })
        assertEquals(listOf(stopped), result.groups.single { it.worktree?.pinned == true }.visibleRows)
    }
}
