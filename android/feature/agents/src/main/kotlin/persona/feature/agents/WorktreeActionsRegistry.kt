package persona.feature.agents

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import persona.core.contracts.api.WorktreeManagement

/**
 * One pairing's worktree work that must outlive a dialog (#17054, the web's module-level sessions and mutation registry):
 * each source's New worktree draft and create, each worktree's rename or deletion (a sent deletion stays until it is
 * checked), and whether this device may change worktrees on each computer. Kept for the process; a new pairing has a new
 * [scope] and so a new registry.
 */
internal class WorktreeActionsRegistry(private val calls: CoroutineScope) {
    private val creations = mutableMapOf<String, WorktreeCreation>()
    private val mutations = mutableMapOf<String, WorktreeMutation>()
    private val _access = MutableStateFlow<Map<String, WorktreeAccess>>(emptyMap())
    val access: StateFlow<Map<String, WorktreeAccess>> = _access.asStateFlow()

    /** [target]'s New worktree session, its draft and create kept until another create replaces them. */
    fun creation(api: WorktreeManagement, target: WorktreeTarget, parent: WorktreeParent): WorktreeCreation =
        creations.getOrPut(target.key + "\u0000" + parent.key) {
            WorktreeCreation(target, api, calls).also { session -> session.update { it.copy(parent = parent) } }
        }

    /** [target]'s rename and deletion; a new one once the last was removed or went stale. */
    fun mutation(api: WorktreeManagement, target: WorktreeTarget, gate: () -> WorktreeGate): WorktreeMutation {
        val kept = mutations[target.key]?.takeUnless { it.state.value.phase in setOf(WorktreePhase.Removed, WorktreePhase.Invalid) }
        return (kept ?: WorktreeMutation(target, api, calls, gate).also { mutations[target.key] = it }).also { it.gate = gate }
    }

    private val _dispatched = MutableStateFlow<Set<String>>(emptySet())
    /**
     * The worktrees (by [WorktreeTarget.key]) with a deletion sent and not confirmed, as of the last dialog that closed:
     * their menus offer Check status instead of Delete and Rename.
     */
    val dispatched: StateFlow<Set<String>> = _dispatched.asStateFlow()

    /** The dialog closed: forget a mutation that sent nothing, keep one whose deletion may have run. */
    fun release(mutation: WorktreeMutation) {
        val state = mutation.state.value
        if (!state.dispatched && !state.busy && mutations[mutation.target.key] === mutation) mutations.remove(mutation.target.key)
        _dispatched.value = mutations.filterValues { it.state.value.dispatched && it.state.value.phase != WorktreePhase.Removed }.keys.toSet()
    }

    /** Asks once per computer whether this device holds full-device access; a failure reads as read-only. */
    fun ensureAccess(api: WorktreeManagement, machineId: String) {
        if (machineId in _access.value || !asking.add(machineId)) return
        calls.launch {
            try {
                val full = api.fullAccess(machineId)
                _access.update { it + (machineId to if (full) WorktreeAccess.Full else WorktreeAccess.ReadOnly) }
            } finally {
                asking -= machineId
            }
        }
    }
    private val asking = mutableSetOf<String>()

    companion object {
        private val registries = mutableMapOf<String, WorktreeActionsRegistry>()
        private val processCalls by lazy { CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate) }

        /** The registry of one pairing ([scope]), shared by the Agents list and the space page. */
        fun of(scope: String): WorktreeActionsRegistry = synchronized(registries) {
            registries.getOrPut(scope) { WorktreeActionsRegistry(processCalls) }
        }
    }
}

private val WorktreeParent.key: String
    get() = when (this) {
        WorktreeParent.Source -> "source"
        WorktreeParent.None -> "none"
        is WorktreeParent.Workspace -> "parent:$id"
    }
