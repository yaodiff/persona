package persona.feature.agents

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import persona.core.contracts.api.AgentCreationService
import persona.core.contracts.api.AgentWorkspace
import persona.core.domain.sort.inRepositoryCatalog

/**
 * One pairing's read-only repository workspace catalog. An incomplete read keeps the last source state. Beside it, the
 * computers' Orca worktrees ([orcaRows], #17054): what the worktree actions resolve their exact target against. They never
 * join [rows], so the repository grouping is unchanged.
 */
internal class AgentsWorkspaceCatalog {
    private val _rows = MutableStateFlow<List<AgentWorkspace>>(emptyList())
    val rows = _rows.asStateFlow()
    private val _orcaRows = MutableStateFlow<List<AgentWorkspace>>(emptyList())
    val orcaRows = _orcaRows.asStateFlow()
    private var generation = 0
    private var scope: String? = null

    /**
     * Reads [machines]' catalogs for the repository rows and their Orca rows, and [orcaOnly]'s for their Orca rows only
     * (computers with Orca but no Paseo): one read per computer.
     */
    suspend fun refresh(service: AgentCreationService, machines: List<Pair<String, Boolean>>,
                        orcaOnly: List<Pair<String, Boolean>> = emptyList()) = coroutineScope {
        if (scope != service.scope) {
            scope = service.scope
            _rows.value = emptyList()
            _orcaRows.value = emptyList()
        }
        val read = ++generation
        val old = _rows.value.groupBy { it.machineId }
        val oldOrca = _orcaRows.value.groupBy { it.machineId }
        val repository = machines.map { it.first }.toSet()
        val next = (machines + orcaOnly.filter { it.first !in repository }).map { (machine, online) -> async {
            val (rows, orca) = try {
                // The hub retains the source inventory in its database, so an offline computer can still restore it.
                val result = service.workspaceCatalog(machine)
                val rows = if (machine !in repository) emptyList() else {
                    val incoming = result.workspaces
                        .filter { it.machineId == machine && it.inRepositoryCatalog && it.sourceId.isNotEmpty() }
                        .distinctBy { it.source to it.sourceId }
                    val allIncomplete = result.unscopedFailure || (!result.complete && result.failedSources.isEmpty())
                    val incomplete = old[machine].orEmpty().filter { allIncomplete || it.source in result.failedSources }
                    val merged = incomplete.associateByTo(linkedMapOf()) { Triple(it.machineId, it.source, it.sourceId) }
                    incoming.forEach { merged[Triple(it.machineId, it.source, it.sourceId)] = it }
                    merged.values.toList()
                }
                // Orca's rows as the catalog has them now; a read that could not reach Orca keeps the last ones.
                val orcaFailed = result.unscopedFailure || "orca" in result.failedSources
                val orca = if (orcaFailed) oldOrca[machine].orEmpty() else result.workspaces
                    .filter { it.machineId == machine && it.source == ORCA_KIND && it.sourceId.isNotEmpty() }
                    .distinctBy { it.sourceId }
                rows to orca
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { old[machine].orEmpty() to oldOrca[machine].orEmpty() }
            fun List<AgentWorkspace>.offline() = if (online) this else map { it.copy(online = false, available = false) }
            rows.offline() to orca.offline()
        } }.awaitAll()
        val order = compareBy<AgentWorkspace> { it.machineId }.thenBy { it.source }.thenBy { it.sourceId }
        if (read == generation) {
            _rows.value = next.flatMap { it.first }.sortedWith(order)
            _orcaRows.value = next.flatMap { it.second }.sortedWith(order)
        }
    }
}
