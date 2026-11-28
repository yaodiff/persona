package persona.feature.agents

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.WorktreeCreateOutcome
import persona.core.contracts.api.WorktreeCreateRequest
import persona.core.designsystem.UiText
import persona.feature.agents.FakeWorktrees.Companion.target

/**
 * New worktree (#17054, the web's `WorktreeCreationSession`): one receipt id per unchanged intent; a pending or uncertain
 * create is checked or replayed with that id and body, never sent under a new one; only a refusal frees the form.
 */
class WorktreeCreationTest {
    private var ids = 0
    private fun session(api: FakeWorktrees, scope: CoroutineScope) =
        WorktreeCreation(target, api, scope, newId = { "00000000-0000-4000-8000-00000000000${++ids}" })

    @Test fun theComposerIsAskedForAndACreateIsSentWithItsReceipt() = runTest {
        val api = FakeWorktrees()
        val made = mutableListOf<String>()
        val creation = session(api, CoroutineScope(UnconfinedTestDispatcher(testScheduler))).apply { created = { made += it.workspaceId } }
        creation.load()
        assertEquals(listOf("prepare:${target.workspaceId}:orca:true"), api.calls)
        creation.update { it.copy(name = "login") }
        creation.submit()
        val (request, id) = api.creates.single()
        assertEquals(WorktreeCreateRequest(target.workspaceId, "orca", composer = true, name = "login", baseRef = "origin/main"), request)
        assertEquals("00000000-0000-4000-8000-000000000001", id)
        assertEquals(CreationPhase.Created, creation.state.value.intent?.phase)
        assertEquals(listOf("repo::/code/app-new"), made)
        // Done: the form is locked until "Create another".
        creation.update { it.copy(name = "changed") }
        assertEquals("login", creation.state.value.draft.name)
        creation.submit()
        assertEquals(1, api.creates.size)
        creation.fresh()
        assertNull(creation.state.value.intent)
    }

    @Test fun anUncertainCreateIsCheckedAndReplayedWithTheSameIdNeverANewOne() = runTest {
        val api = FakeWorktrees()
        val creation = session(api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)))
        creation.load()
        api.create = { _, _ -> throw CodedFailure(null, message = "timeout") }
        api.receipt = { "pending" }
        creation.submit()
        assertEquals(CreationPhase.Pending, creation.state.value.intent?.phase)
        assertTrue(creation.state.value.locked)
        // A pending create keeps its body: an edit is ignored.
        creation.update { it.copy(name = "other") }
        assertEquals("", creation.state.value.draft.name)
        // Check result: the receipt first, then the very same request under the same id.
        api.create = { _, _ -> WorktreeCreateOutcome.Created(persona.core.contracts.api.WorktreeCreated("w2", path = "/p", branch = "b")) }
        api.receipt = { "done" }
        creation.submit()
        assertEquals(2, api.creates.size)
        assertEquals(api.creates[0], api.creates[1])
        assertEquals(CreationPhase.Created, creation.state.value.intent?.phase)
        assertEquals(listOf("create:", "receipt:", "receipt:", "create:"), api.calls.drop(1).map { it.substringBefore(':') + ":" })
    }

    @Test fun aReceiptThatSaysUnknownSendsNothing() = runTest {
        val api = FakeWorktrees()
        val creation = session(api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)))
        creation.load()
        api.create = { _, _ -> WorktreeCreateOutcome.Unknown }
        creation.submit()
        assertEquals(CreationPhase.Unknown, creation.state.value.intent?.phase)
        api.receipt = { "unknown" }
        creation.submit()
        assertEquals(1, api.creates.size)
        assertEquals(CreationPhase.Unknown, creation.state.value.intent?.phase)
    }

    @Test fun onlyARefusalThatReachedNothingFreesTheFormForANewId() = runTest {
        val api = FakeWorktrees()
        val creation = session(api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)))
        creation.load()
        // No receipt, and the hub says nothing reached the computer.
        api.create = { _, _ -> throw CodedFailure(503, "machine_offline", delivered = "none") }
        creation.submit()
        assertEquals(CreationPhase.Refused, creation.state.value.intent?.phase)
        assertFalse(creation.state.value.locked)
        creation.update { it.copy(name = "edited") }
        api.create = { _, _ -> WorktreeCreateOutcome.Pending }
        creation.submit()
        assertNotEquals(api.creates[0].second, api.creates[1].second)
        assertEquals("edited", api.creates[1].first.name)
        assertEquals(CreationPhase.Pending, creation.state.value.intent?.phase)
        // An error with no proof (delivered unknown, no receipt) is uncertain, not refused.
        val other = session(FakeWorktrees().apply { create = { _, _ -> throw CodedFailure(502, delivered = "unknown") } },
            CoroutineScope(UnconfinedTestDispatcher(testScheduler)))
        other.load()
        other.submit()
        assertEquals(CreationPhase.Unknown, other.state.value.intent?.phase)
    }

    @Test fun aDraftTheComputerCannotTakeIsSaidAndNotSent() = runTest {
        val api = FakeWorktrees()
        val creation = session(api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)))
        creation.submit()
        assertEquals(R.string.agents_worktree_not_ready, (creation.state.value.error as UiText.Res).id)
        creation.load()
        creation.update { it.copy(branch = "a..b") }
        creation.submit()
        assertEquals(R.string.agents_worktree_branch_pattern, (creation.state.value.error as UiText.Res).id)
        assertTrue(api.creates.isEmpty())
    }

    @Test fun anOlderComputerOffersTheBasicForm() = runTest {
        val api = FakeWorktrees().apply {
            prepare = { composer -> if (composer) throw CodedFailure(501, "unknown_method") else FakeWorktrees.basicPrepare }
        }
        val creation = session(api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)))
        creation.load()
        val failed = creation.state.value.read as WorktreeRead.Failed
        assertEquals(R.string.agents_worktree_needs_update, (failed.text as UiText.Res).id)
        assertTrue(failed.basicAvailable)
        creation.useBasic()
        assertEquals(FakeWorktrees.basicPrepare, creation.state.value.info)
        creation.update { WorktreeForms.typeBranch(it, "/code/.orca", "feat/x") }
        creation.submit()
        assertEquals(WorktreeCreateRequest(target.workspaceId, "orca", branch = "feat/x", path = "/code/.orca/feat-x"), api.creates.single().first)
    }

    @Test fun sourcesAreSearchedAndAPresetIsSavedAndPicked() = runTest {
        val api = FakeWorktrees()
        val creation = session(api, CoroutineScope(UnconfinedTestDispatcher(testScheduler)))
        creation.load()
        creation.query("login")
        creation.search()
        val found = creation.state.value.search.items.single()
        creation.pickSource(found)
        assertEquals(found, creation.state.value.draft.provider)
        creation.update { it.copy(baseRef = "origin/release") }
        creation.savePreset("Docs", "docs\nsite")
        assertEquals("p-new", creation.state.value.draft.preset)
        assertEquals("origin/release", creation.state.value.draft.baseRef)
        assertTrue(creation.state.value.info!!.composer!!.presets.any { it.id == "p-new" })
        creation.submit()
        val request = api.creates.single().first
        assertEquals("pr:12", request.sourceId)
        assertEquals("p-new", request.sparsePresetId)
    }
}
