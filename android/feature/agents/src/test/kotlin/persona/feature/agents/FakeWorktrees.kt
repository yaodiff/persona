package persona.feature.agents

import persona.core.contracts.Machine
import persona.core.contracts.api.*

/** A [WorktreeManagement] that records every call and answers what each test sets (#17054). */
internal class FakeWorktrees : WorktreeManagement {
    val calls = mutableListOf<String>()
    val creates = mutableListOf<Pair<WorktreeCreateRequest, String>>()
    val deletions = mutableListOf<WorktreeDeletion>()
    var full = true
    var prepare: (Boolean) -> WorktreePrepare = { composer -> if (composer) composerPrepare else basicPrepare }
    var create: (WorktreeCreateRequest, String) -> WorktreeCreateOutcome = { request, _ ->
        WorktreeCreateOutcome.Created(WorktreeCreated("repo::/code/app-new", path = "/code/.orca/app-new", branch = request.branchOverride ?: "app-new"))
    }
    var receipt: (String) -> String = { "" }
    var rename: (String) -> String = { "" }
    var preview: () -> WorktreeDeletionPreview = { previewOf() }
    var delete: (WorktreeDeletion) -> WorktreeDeletionResult = { WorktreeDeletionResult(target.workspaceId, removed = true, state = "removed") }
    var sources: (String) -> WorktreeSources = { WorktreeSources(listOf(WorktreeSource("pr", 12, "Fix the login form", "https://example.com/pr/12"))) }

    override suspend fun fullAccess(machineId: String): Boolean { calls += "access:$machineId"; return full }
    override suspend fun prepareWorktree(machineId: String, workspaceId: String, kind: String, composer: Boolean): WorktreePrepare {
        calls += "prepare:$workspaceId:$kind:$composer"
        return prepare(composer)
    }
    override suspend fun createWorktree(machineId: String, request: WorktreeCreateRequest, operationId: String): WorktreeCreateOutcome {
        calls += "create:$operationId"
        creates += request to operationId
        return create(request, operationId)
    }
    override suspend fun worktreeReceipt(machineId: String, operationId: String): String {
        calls += "receipt:$operationId"
        return receipt(operationId)
    }
    override suspend fun worktreeSources(machineId: String, workspaceId: String, kind: String, query: String): WorktreeSources {
        calls += "source:$query"
        return sources(query)
    }
    override suspend fun saveWorktreePreset(machineId: String, workspaceId: String, kind: String, name: String, directories: String): SparsePreset {
        calls += "preset:$name"
        return SparsePreset("p-new", name, directories.lines())
    }
    override suspend fun renameWorktree(machineId: String, workspaceId: String, kind: String, name: String): String {
        calls += "rename:$name"
        return rename(name)
    }
    override suspend fun previewWorktreeDeletion(machineId: String, workspaceId: String, kind: String): WorktreeDeletionPreview {
        calls += "preview:$workspaceId"
        return preview()
    }
    override suspend fun deleteWorktree(machineId: String, deletion: WorktreeDeletion): WorktreeDeletionResult {
        calls += if (deletion.reconcile) "reconcile:${deletion.previewToken}" else "delete:${deletion.previewToken}"
        deletions += deletion
        return delete(deletion)
    }

    companion object {
        val target = WorktreeTarget("mac", "repo::/code/app-login", "/code/.orca/app-login", "login-form")
        val main = AgentWorkspace("mac/orca/repo::/code/app", "mac", "orca", "repo::/code/app", "app", "/code/app", "main",
            projectId = "repo", projectName = "app", isMain = true, workspaceKind = "git")
        val tree = AgentWorkspace("mac/orca/${target.workspaceId}", "mac", "orca", target.workspaceId, "login-form", target.cwd, "feat/login",
            parentSourceId = main.sourceId, projectId = "repo", projectName = "app", isMain = false, workspaceKind = "git")
        val rows = listOf(main, tree)
        val machine = Machine("mac", "MacBook Pro", online = true, engine = "orca")
        val composerPrepare = WorktreePrepare("/code/app", "app", "feat/login", "/code/.orca", chooseParent = true, automaticName = true,
            composer = WorktreeComposerOptions("app", "origin/main", listOf("origin/main", "origin/release"),
                listOf(SparsePreset("p1", "Docs only", listOf("docs"))),
                listOf("create", "source.github", "sparse_presets", "branch_override", "folder_parent")))
        val basicPrepare = WorktreePrepare("/code/app", "app", "feat/login", "/code/.orca", chooseParent = false, automaticName = false)
        fun previewOf(dirty: Int = 2, branch: String = "feat/login") = WorktreeDeletionPreview(target.workspaceId, "orca", "local", "orca-1",
            target.cwd, branch, dirty, folder = false, previewToken = "token-1")
    }
}

/** A hub's refusal with a status, a code and what it says was delivered. */
internal class CodedFailure(override val status: Int?, override val code: String? = null, override val delivered: String? = null,
                            message: String = "refused") : Exception(message), HubCodedFailure
