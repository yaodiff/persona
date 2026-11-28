package persona.feature.agents

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import persona.core.contracts.api.WorktreeDeletion
import persona.core.contracts.api.WorktreeDeletionPreview
import persona.core.contracts.api.WorktreeDeletionResult
import persona.core.contracts.api.WorktreeManagement
import persona.core.designsystem.UiText
import persona.core.designsystem.failureReasonText

/** Where a rename or a deletion stands (the web's `WorktreeMutationState`). */
internal enum class WorktreePhase { Idle, Renaming, Previewing, Ready, Deleting, Checking, Uncertain, Removed, Invalid }

internal data class WorktreeMutationState(
    val phase: WorktreePhase = WorktreePhase.Idle,
    val preview: WorktreeDeletionPreview? = null,
    val result: WorktreeDeletionResult? = null,
    val error: UiText? = null,
    /** A deletion was sent: from here it can only be checked ([WorktreeMutation.reconcile]), never sent again. */
    val dispatched: Boolean = false,
) {
    val busy: Boolean get() = phase in setOf(WorktreePhase.Renaming, WorktreePhase.Previewing, WorktreePhase.Deleting, WorktreePhase.Checking)
}

/** What a mutation finished, for the notice and the catalog's refresh. */
internal enum class WorktreeDone { Renamed, Removed }

/**
 * One worktree's rename and previewed deletion (the web's `WorktreeMutation`, #17054): one exact destination and one
 * deletion token. A deletion spends the confirmation as soon as it is sent, even when its answer is lost: what is left is
 * [reconcile], which asks the node with the same token whether it went, and never sends the deletion again. Each step
 * checks [gate] again first. Calls run in [scope], which outlives the dialog, so closing it loses no answer.
 */
internal class WorktreeMutation(
    val target: WorktreeTarget,
    private val api: WorktreeManagement,
    private val scope: CoroutineScope,
    var gate: () -> WorktreeGate,
    var done: (WorktreeDone) -> Unit = {},
) {
    private val _state = MutableStateFlow(WorktreeMutationState())
    val state: StateFlow<WorktreeMutationState> = _state.asStateFlow()
    private fun set(transform: (WorktreeMutationState) -> WorktreeMutationState) = _state.update(transform)

    private enum class Step { Rename, Delete, Reconcile }

    private fun admit(step: Step): Boolean {
        if (_state.value.phase == WorktreePhase.Invalid) return false
        val gate = gate()
        val allowed = when (step) {
            Step.Rename -> gate.canRename
            Step.Delete -> gate.canDelete
            Step.Reconcile -> gate.canReconcile
        }
        if (!allowed) set { it.copy(error = UiText.Res(gate.reason ?: gate.protection ?: R.string.agents_worktree_unavailable)) }
        return allowed
    }

    /** Destination or availability changed under an open dialog: nothing more is sent from it. */
    fun invalidate() = set { it.copy(phase = WorktreePhase.Invalid, error = UiText.Res(R.string.agents_worktree_invalidated)) }

    fun rename(name: String) {
        val trimmed = name.trim()
        val now = _state.value
        if (trimmed.isEmpty() || now.dispatched || now.phase == WorktreePhase.Renaming || now.phase == WorktreePhase.Removed || !admit(Step.Rename)) return
        set { it.copy(phase = WorktreePhase.Renaming, error = null) }
        scope.launch {
            try {
                val answer = api.renameWorktree(target.machineId, target.workspaceId, target.kind, trimmed)
                if (answer == "pending" || answer == "unknown") {
                    set { it.copy(phase = WorktreePhase.Idle, error = UiText.Res(R.string.agents_worktree_rename_unknown)) }
                    return@launch
                }
                set { it.copy(phase = WorktreePhase.Idle) }
                done(WorktreeDone.Renamed)
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { set { it.copy(phase = WorktreePhase.Idle, error = failureReasonText(error)) } }
        }
    }

    /** Asks what deleting would remove; the dialog shows it before anything can be deleted. */
    fun preview() {
        val now = _state.value
        if (now.dispatched || now.phase == WorktreePhase.Previewing || now.phase == WorktreePhase.Removed || !admit(Step.Delete)) return
        set { it.copy(phase = WorktreePhase.Previewing, error = null, preview = null) }
        scope.launch {
            try {
                val info = api.previewWorktreeDeletion(target.machineId, target.workspaceId, target.kind)
                if (!matches(info)) {
                    set { it.copy(phase = WorktreePhase.Idle, error = UiText.Res(R.string.agents_worktree_preview_invalid)) }
                    return@launch
                }
                set { it.copy(phase = WorktreePhase.Ready, preview = info) }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { set { it.copy(phase = WorktreePhase.Idle, error = failureReasonText(error)) } }
        }
    }

    /** The preview is of this very worktree on this computer's own Orca, and complete. */
    private fun matches(info: WorktreeDeletionPreview): Boolean =
        info.workspaceId == target.workspaceId && info.kind == target.kind && info.hostId == "local" && info.instanceId.isNotBlank() &&
            !info.closeOnly && info.previewToken.isNotBlank() && info.path == target.cwd && info.branch != null &&
            info.folder != null && (info.dirty ?: -1) >= 0

    /**
     * Deletes what the preview showed. Uncommitted changes are deleted only with [discardChanges]; the branch only with
     * [deleteBranch] (the node keeps an unmerged one anyway). A folder's entry is only unregistered: neither applies.
     */
    fun remove(discardChanges: Boolean, deleteBranch: Boolean) {
        val now = _state.value
        val info = now.preview
        if (now.phase != WorktreePhase.Ready || info == null || now.dispatched || !admit(Step.Delete)) return
        val folder = info.folder == true
        val dirty = info.dirty ?: 0
        if (!folder && dirty > 0 && !discardChanges) return
        set { it.copy(phase = WorktreePhase.Deleting, error = null, dispatched = true) }
        finish(WorktreeDeletion(target.workspaceId, target.kind, info.previewToken,
            force = !folder && dirty > 0 && discardChanges, deleteBranch = !folder && deleteBranch))
    }

    /** After an uncertain answer: asks with the same token whether the worktree went. */
    fun reconcile() {
        val now = _state.value
        val info = now.preview
        if (info == null || !now.dispatched || now.phase != WorktreePhase.Uncertain || !admit(Step.Reconcile)) return
        set { it.copy(phase = WorktreePhase.Checking, error = null) }
        finish(WorktreeDeletion(target.workspaceId, target.kind, info.previewToken, reconcile = true))
    }

    private fun finish(deletion: WorktreeDeletion) {
        scope.launch {
            try {
                val result = api.deleteWorktree(target.machineId, deletion)
                if (result.workspaceId != target.workspaceId) {
                    set { it.copy(phase = WorktreePhase.Uncertain, error = UiText.Res(R.string.agents_worktree_preview_invalid)) }
                } else if (result.removed && result.state == "removed") {
                    set { it.copy(phase = WorktreePhase.Removed, result = result) }
                    done(WorktreeDone.Removed)
                } else {
                    set { it.copy(phase = WorktreePhase.Uncertain, result = result, error = UiText.Res(R.string.agents_worktree_unknown)) }
                }
            } catch (cancelled: CancellationException) {
                set { it.copy(phase = WorktreePhase.Uncertain, error = UiText.Res(R.string.agents_worktree_unknown)) }
                throw cancelled
            } catch (error: Exception) {
                // Whatever went wrong, the deletion may have run: only a check says.
                set { it.copy(phase = WorktreePhase.Uncertain,
                    error = UiText.Res(R.string.agents_worktree_failed_unknown, listOf(failureReasonText(error)))) }
            }
        }
    }
}
