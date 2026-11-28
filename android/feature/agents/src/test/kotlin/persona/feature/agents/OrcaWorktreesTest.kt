package persona.feature.agents

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.DetectedEngine
import persona.core.contracts.Session
import persona.core.contracts.api.WorktreeCreateRequest
import persona.core.contracts.api.WorktreeSource
import persona.core.designsystem.UiText
import persona.feature.agents.FakeWorktrees.Companion.basicPrepare
import persona.feature.agents.FakeWorktrees.Companion.composerPrepare
import persona.feature.agents.FakeWorktrees.Companion.machine
import persona.feature.agents.FakeWorktrees.Companion.main
import persona.feature.agents.FakeWorktrees.Companion.rows
import persona.feature.agents.FakeWorktrees.Companion.target
import persona.feature.agents.FakeWorktrees.Companion.tree

/** The rules of Orca's worktree actions on Android (#17054): the web's gates, branch checks and request bodies. */
class OrcaWorktreesTest {
    private fun ready(draft: WorktreeDraft, info: persona.core.contracts.api.WorktreePrepare = composerPrepare) =
        (WorktreeForms.body(target, draft, info) as WorktreeBody.Ready).request
    private fun problem(draft: WorktreeDraft, info: persona.core.contracts.api.WorktreePrepare = composerPrepare) =
        ((WorktreeForms.body(target, draft, info) as WorktreeBody.Problem).text as UiText.Res).id

    @Test fun branchNamesAreCheckedAsGitWould() {
        assertEquals(R.string.agents_worktree_branch_required, WorktreeForms.branchProblem("  "))
        assertEquals(R.string.agents_worktree_branch_dash, WorktreeForms.branchProblem("-x"))
        assertEquals(R.string.agents_worktree_branch_chars, WorktreeForms.branchProblem("a b"))
        assertEquals(R.string.agents_worktree_branch_chars, WorktreeForms.branchProblem("a:b"))
        assertEquals(R.string.agents_worktree_branch_pattern, WorktreeForms.branchProblem("a..b"))
        assertEquals(R.string.agents_worktree_branch_edges, WorktreeForms.branchProblem("a/"))
        assertEquals(R.string.agents_worktree_branch_edges, WorktreeForms.branchProblem("a.lock"))
        assertEquals(R.string.agents_worktree_branch_parts, WorktreeForms.branchProblem("a/.b"))
        assertNull(WorktreeForms.branchProblem("feat/login-form"))
    }

    @Test fun thePathFollowsTheBranchUntilItIsEdited() {
        var draft = WorktreeForms.typeBranch(WorktreeDraft(), "/code/.orca/", "Feat/A_b.c")
        assertEquals("/code/.orca/feat-a-b-c", draft.path)
        draft = WorktreeForms.typePath(draft, "/code/.orca", "/elsewhere")
        draft = WorktreeForms.typeBranch(draft, "/code/.orca", "other")
        assertEquals("/elsewhere", draft.path)
        draft = WorktreeForms.typePath(draft, "/code/.orca", "")
        assertEquals("/code/.orca/other", draft.path)
        assertFalse(draft.pathEdited)
        assertEquals("C:\\w\\x", WorktreeForms.suggestedPath("C:\\w", "x"))
    }

    @Test fun theComposerSendsOnlyWhatTheComputerAdvertised() {
        // Nothing chosen: the project's base and the source as parent.
        assertEquals(WorktreeCreateRequest(target.workspaceId, "orca", composer = true, baseRef = "origin/main"), ready(WorktreeDraft()))
        val pr = WorktreeSource("pr", 12, "Fix", "https://example.com/pr/12")
        assertEquals(WorktreeCreateRequest(target.workspaceId, "orca", composer = true, name = "login", branchOverride = "feat/x",
            baseRef = "origin/release", noParent = true, note = "why", sparsePresetId = "p1", sourceId = "pr:12", sourceUrl = pr.url),
            ready(WorktreeDraft(name = " login ", branch = "feat/x", baseRef = "origin/release", parent = WorktreeParent.None,
                note = "why", preset = "p1", provider = pr)))
        assertEquals("other", ready(WorktreeDraft(parent = WorktreeParent.Workspace("other", "Other"))).parentWorkspaceId)
        val composer = composerPrepare.composer!!
        val noOverride = composerPrepare.copy(composer = composer.copy(capabilities = listOf("create")))
        assertEquals(R.string.agents_worktree_unsupported, problem(WorktreeDraft(branch = "x"), noOverride))
        assertEquals(R.string.agents_worktree_unsupported, problem(WorktreeDraft(provider = pr), noOverride))
        assertEquals(R.string.agents_worktree_unsupported, problem(WorktreeDraft(preset = "p1"), noOverride))
        assertEquals(R.string.agents_worktree_unsupported, problem(WorktreeDraft(preset = "gone")))
        assertEquals(R.string.agents_worktree_base_missing, problem(WorktreeDraft(baseRef = "origin/gone")))
        assertEquals(R.string.agents_worktree_branch_pattern, problem(WorktreeDraft(branch = "a..b")))
        assertEquals(R.string.agents_worktree_unsupported, problem(WorktreeDraft(), composerPrepare.copy(composer = composer.copy(capabilities = emptyList()))))
    }

    @Test fun theBasicFormNeverTakesAnAdvancedChoice() {
        assertEquals(WorktreeCreateRequest(target.workspaceId, "orca", branch = "feat/x", path = "/code/.orca/feat-x"),
            ready(WorktreeDraft(branch = "feat/x"), basicPrepare))
        // A composer computer, with the basic form chosen, reads the basic form too.
        assertEquals("/p", ready(WorktreeDraft(branch = "b", path = "/p", pathEdited = true, basic = true)).path)
        assertEquals(R.string.agents_worktree_unsupported, problem(WorktreeDraft(branch = "b", name = "n"), basicPrepare))
        assertEquals(R.string.agents_worktree_unsupported, problem(WorktreeDraft(branch = "b", parent = WorktreeParent.None), basicPrepare))
        assertEquals(R.string.agents_worktree_unsupported, problem(WorktreeDraft(automaticName = true), basicPrepare))
        assertEquals(R.string.agents_worktree_branch_required, problem(WorktreeDraft(), basicPrepare))
        assertEquals(R.string.agents_worktree_path_required, problem(WorktreeDraft(branch = "b", path = " ", pathEdited = true), basicPrepare))
        val generated = basicPrepare.copy(automaticName = true, chooseParent = true)
        assertEquals(WorktreeCreateRequest(target.workspaceId, "orca", autoName = true, noParent = true),
            ready(WorktreeDraft(automaticName = true, parent = WorktreeParent.None), generated))
    }

    @Test fun actionsAreGatedLikeTheWeb() {
        val full = WorktreeGates.resolve(target, rows, listOf(machine), WorktreeAccess.Full)
        assertTrue(full.canCreate && full.canRename && full.canDelete && full.canReconcile)
        assertNull(full.reason)
        assertEquals(R.string.agents_worktree_checking_access, WorktreeGates.resolve(target, rows, listOf(machine), WorktreeAccess.Checking).reason)
        val readOnly = WorktreeGates.resolve(target, rows, listOf(machine), WorktreeAccess.ReadOnly)
        assertEquals(R.string.agents_worktree_read_only, readOnly.reason)
        assertFalse(readOnly.canCreate || readOnly.canDelete || readOnly.canReconcile)
        // The computer offline, Orca not enabled or reported unavailable, the row gone or moved: unavailable.
        for (machines in listOf(listOf(machine.copy(online = false)), listOf(machine.copy(engine = "herdr")),
                listOf(machine.copy(engines = listOf(DetectedEngine("orca", "Orca", available = false)))), emptyList())) {
            assertEquals(R.string.agents_worktree_unavailable, WorktreeGates.resolve(target, rows, machines, WorktreeAccess.Full).reason)
        }
        assertEquals(R.string.agents_worktree_unavailable, WorktreeGates.resolve(target.copy(cwd = "/moved"), rows, listOf(machine), WorktreeAccess.Full).reason)
        assertTrue(WorktreeGates.resolve(target, rows, listOf(machine.copy(engine = "herdr", engineHosts = listOf("herdr", "orca"))), WorktreeAccess.Full).canDelete)
        // The main checkout and a parent with children are never deleted; a removed row can still be checked.
        val mainGate = WorktreeGates.resolve(WorktreeTarget.of(main), rows, listOf(machine), WorktreeAccess.Full)
        assertFalse(mainGate.canDelete)
        assertTrue(mainGate.canRename)
        assertEquals(R.string.agents_worktree_has_children, mainGate.protection)
        assertEquals(R.string.agents_worktree_protected, WorktreeGates.resolve(WorktreeTarget.of(main), listOf(main), listOf(machine), WorktreeAccess.Full).protection)
        val gone = WorktreeGates.resolve(target, listOf(main), listOf(machine), WorktreeAccess.Full)
        assertFalse(gone.canDelete)
        assertTrue(gone.canReconcile)
    }

    @Test fun aGroupIsAnOrcaWorktreeWhenAllItsOrcaSessionsAreInOne() {
        val terminal = Session("mac", "a", spaceKind = "orca", workspaceId = tree.sourceId, paneId = "orca:tab:leaf")
        val chat = Session("mac", "b", spaceKind = "orca", spaceId = tree.sourceId, sourceKind = "runtime", sourceInstanceId = "orca")
        assertEquals(target, WorktreeGates.targetOf(listOf(terminal, chat), rows))
        val herdr = Session("mac", "c", spaceKind = "herdr", spaceId = "w1", paneId = "w1:p1")
        assertEquals(target, WorktreeGates.targetOf(listOf(terminal, herdr), rows))
        assertNull(WorktreeGates.targetOf(listOf(herdr), rows))
        assertNull(WorktreeGates.targetOf(listOf(terminal, Session("mac", "d", spaceKind = "orca", spaceId = main.sourceId)), rows))
        // A worktree the catalog does not list has no actions.
        assertNull(WorktreeGates.targetOf(listOf(terminal), listOf(main)))
    }
}
