package persona.feature.agents

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.WorktreeDeletion
import persona.core.contracts.api.WorktreeDeletionResult
import persona.core.designsystem.UiText
import persona.feature.agents.FakeWorktrees.Companion.machine
import persona.feature.agents.FakeWorktrees.Companion.previewOf
import persona.feature.agents.FakeWorktrees.Companion.rows
import persona.feature.agents.FakeWorktrees.Companion.target

/** A worktree's rename and previewed deletion (#17054, the web's `WorktreeMutation`): spent once sent, then only checked. */
class WorktreeMutationTest {
    private var access = WorktreeAccess.Full
    private val gate = { WorktreeGates.resolve(target, rows, listOf(machine), access) }

    @Test fun aRenameIsSentOnceAndAnUnconfirmedOneSaysSo() = runTest {
        val api = FakeWorktrees()
        val done = mutableListOf<WorktreeDone>()
        val mutation = WorktreeMutation(target, api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)), gate) { done += it }
        mutation.rename("  ")
        assertTrue(api.calls.isEmpty())
        mutation.rename(" Login form ")
        assertEquals(listOf("rename:Login form"), api.calls)
        assertEquals(listOf(WorktreeDone.Renamed), done)
        api.rename = { "pending" }
        mutation.rename("Again")
        assertEquals(R.string.agents_worktree_rename_unknown, (mutation.state.value.error as UiText.Res).id)
        assertEquals(1, done.size)
        // Without full-device access nothing is sent, and the reason is said.
        access = WorktreeAccess.ReadOnly
        mutation.rename("Third")
        assertEquals(2, api.calls.size)
        assertEquals(R.string.agents_worktree_read_only, (mutation.state.value.error as UiText.Res).id)
    }

    @Test fun thePreviewMustBeOfThisVeryWorktree() = runTest {
        val api = FakeWorktrees()
        val mutation = WorktreeMutation(target, api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)), gate)
        api.preview = { previewOf().copy(path = "/somewhere/else") }
        mutation.preview()
        assertEquals(WorktreePhase.Idle, mutation.state.value.phase)
        assertEquals(R.string.agents_worktree_preview_invalid, (mutation.state.value.error as UiText.Res).id)
        for (wrong in listOf(previewOf().copy(hostId = "remote"), previewOf().copy(instanceId = ""), previewOf().copy(previewToken = ""),
                previewOf().copy(dirty = null), previewOf().copy(branch = null), previewOf().copy(closeOnly = true), previewOf().copy(kind = "herdr"))) {
            api.preview = { wrong }
            mutation.preview()
            assertEquals(WorktreePhase.Idle, mutation.state.value.phase)
        }
        api.preview = { previewOf() }
        mutation.preview()
        assertEquals(WorktreePhase.Ready, mutation.state.value.phase)
    }

    @Test fun changedFilesAreDeletedOnlyOnPurposeAndTheBranchOnlyWhenAsked() = runTest {
        val api = FakeWorktrees()
        val done = mutableListOf<WorktreeDone>()
        val mutation = WorktreeMutation(target, api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)), gate) { done += it }
        mutation.preview()
        mutation.remove(discardChanges = false, deleteBranch = true)
        assertTrue(api.deletions.isEmpty())
        mutation.remove(discardChanges = true, deleteBranch = true)
        assertEquals(listOf(WorktreeDeletion(target.workspaceId, "orca", "token-1", force = true, deleteBranch = true)), api.deletions)
        assertEquals(WorktreePhase.Removed, mutation.state.value.phase)
        assertEquals(listOf(WorktreeDone.Removed), done)

        // A clean worktree needs no acknowledgement and keeps its branch unless asked.
        val clean = FakeWorktrees().apply { preview = { previewOf(dirty = 0) } }
        WorktreeMutation(target, clean, CoroutineScope(UnconfinedTestDispatcher(testScheduler)), gate).apply { preview(); remove(false, false) }
        assertEquals(listOf(WorktreeDeletion(target.workspaceId, "orca", "token-1")), clean.deletions)
    }

    @Test fun aSentDeletionIsNeverSentAgainOnlyChecked() = runTest {
        val api = FakeWorktrees()
        val mutation = WorktreeMutation(target, api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)), gate)
        mutation.preview()
        api.delete = { throw CodedFailure(null, message = "timeout") }
        mutation.remove(discardChanges = true, deleteBranch = false)
        assertEquals(WorktreePhase.Uncertain, mutation.state.value.phase)
        assertTrue(mutation.state.value.dispatched)
        // Neither another delete nor another preview goes out.
        mutation.remove(discardChanges = true, deleteBranch = false)
        mutation.preview()
        assertEquals(listOf("preview:${target.workspaceId}", "delete:token-1"), api.calls)
        // The node still has it: still uncertain, checked again with the same token.
        api.delete = { WorktreeDeletionResult(target.workspaceId, removed = false, state = "present") }
        mutation.reconcile()
        assertEquals(WorktreePhase.Uncertain, mutation.state.value.phase)
        api.delete = { WorktreeDeletionResult(target.workspaceId, removed = true, state = "removed") }
        mutation.reconcile()
        assertEquals(WorktreePhase.Removed, mutation.state.value.phase)
        assertEquals(listOf(false, true, true), api.deletions.map { it.reconcile })
        assertEquals(setOf("token-1"), api.deletions.map { it.previewToken }.toSet())
        assertFalse(api.deletions.drop(1).any { it.force || it.deleteBranch })
    }

    @Test fun aMainCheckoutOrAParentIsNeverPreviewedForDeletion() = runTest {
        val api = FakeWorktrees()
        val mainTarget = WorktreeTarget.of(FakeWorktrees.main)
        val mutation = WorktreeMutation(mainTarget, api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)),
            { WorktreeGates.resolve(mainTarget, rows, listOf(machine), WorktreeAccess.Full) })
        mutation.preview()
        assertTrue(api.calls.isEmpty())
        assertEquals(R.string.agents_worktree_has_children, (mutation.state.value.error as UiText.Res).id)
    }

    @Test fun theRegistryKeepsASentDeletionAndForgetsTheRest() = runTest {
        val api = FakeWorktrees()
        val registry = WorktreeActionsRegistry(CoroutineScope(UnconfinedTestDispatcher(testScheduler)))
        val first = registry.mutation(api, target, gate)
        registry.release(first)
        val second = registry.mutation(api, target, gate)
        assertFalse(first === second)
        second.preview()
        api.delete = { throw CodedFailure(503, message = "lost") }
        second.remove(true, false)
        registry.release(second)
        assertEquals(setOf(target.key), registry.dispatched.value)
        assertTrue(second === registry.mutation(api, target, gate))
        // Access is asked once per computer.
        registry.ensureAccess(api, "mac")
        registry.ensureAccess(api, "mac")
        assertEquals(1, api.calls.count { it == "access:mac" })
        assertEquals(WorktreeAccess.Full, registry.access.value["mac"])
    }
}
