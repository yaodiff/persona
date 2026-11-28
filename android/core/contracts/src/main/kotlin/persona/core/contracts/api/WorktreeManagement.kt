package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Creating, renaming and deleting an engine's worktrees (Orca's, #17054): the hub's full-device node actions under
 * `POST /api/v1/machines/{id}/worktree/…` (docs/protocol/protocol.md "Orca worktree composer and removal"), the same
 * calls as the web's `lib/worktree.ts` and `lib/worktree-actions-state.ts`. The hub forwards them; the node checks the
 * repository and decides whether a removal is safe. Every call names the computer it is for, so a service over several
 * hubs or hub groups routes it by that ([routedWorktreeManagement]).
 */
interface WorktreeManagement {
    /**
     * Whether this device holds full-device access (`GET /api/v1/auth/tokens`, a full-device-only route): false for a
     * limited token and on any failure, so the actions stay read-only rather than failing later.
     */
    suspend fun fullAccess(machineId: String): Boolean

    /** The repository of [workspaceId] and, asked with [composer] on a computer whose Orca has it, the composer's options. */
    suspend fun prepareWorktree(machineId: String, workspaceId: String, kind: String, composer: Boolean): WorktreePrepare

    /**
     * Creates a worktree under the receipt [operationId] (`X-Persona-Op-Id`). The same id with the same request replays the
     * first outcome; an uncertain outcome is reconciled through [worktreeReceipt] or that replay, never a new id.
     */
    suspend fun createWorktree(machineId: String, request: WorktreeCreateRequest, operationId: String): WorktreeCreateOutcome

    /** The receipt's state (`GET /api/v1/ops/{id}`: pending, done, failed, unknown); a failure to read it throws. */
    suspend fun worktreeReceipt(machineId: String, operationId: String): String

    /** GitHub issues and pull requests to start a worktree from (`worktree/source`), with any warning the provider gave. */
    suspend fun worktreeSources(machineId: String, workspaceId: String, kind: String, query: String): WorktreeSources

    /** Saves a sparse checkout preset (`worktree/preset`): [directories] one repository-relative path per line. */
    suspend fun saveWorktreePreset(machineId: String, workspaceId: String, kind: String, name: String, directories: String): SparsePreset

    /** Renames a listed worktree's display name (`worktree/rename`); the answer's `state` ("" when it says none). */
    suspend fun renameWorktree(machineId: String, workspaceId: String, kind: String, name: String): String

    /** What deleting [workspaceId] would remove (`worktree/delete` with `preview`), with the token a confirmation spends. */
    suspend fun previewWorktreeDeletion(machineId: String, workspaceId: String, kind: String): WorktreeDeletionPreview

    /** Confirms a previewed deletion, or checks it with [WorktreeDeletion.reconcile] after an uncertain answer. */
    suspend fun deleteWorktree(machineId: String, deletion: WorktreeDeletion): WorktreeDeletionResult
}

/** A sparse checkout preset of the repository (`{id, name?, directories}`). */
@Serializable
data class SparsePreset(val id: String = "", val name: String? = null, val directories: List<String> = emptyList())

/**
 * What Orca's composer offers for the repository (`composer` of `worktree/prepare`): its [baseRef] and [refs], the sparse
 * [presets], and the adapter's [capabilities] (`create`, `source.github`, `sparse_presets`, `branch_override`,
 * `folder_parent`). Nothing advanced is sent to a computer that did not list it.
 */
@Serializable
data class WorktreeComposerOptions(
    val project: String = "",
    @SerialName("base_ref") val baseRef: String = "",
    val refs: List<String> = emptyList(),
    val presets: List<SparsePreset> = emptyList(),
    val capabilities: List<String> = emptyList(),
) {
    fun has(capability: String) = capability in capabilities
}

/**
 * `worktree/prepare`'s answer: the repository's top level [root] and [repoName], the source's [branch] ("" on a detached
 * HEAD), the folder the engine puts its checkouts in ([path]); [chooseParent] when the parent can be chosen apart from the
 * source, [automaticName] when the engine can name it, and [composer] only from a computer whose Orca has the composer.
 */
@Serializable
data class WorktreePrepare(
    val root: String = "",
    val repoName: String = "",
    val branch: String = "",
    val path: String = "",
    @SerialName("choose_parent") val chooseParent: Boolean = false,
    @SerialName("automatic_name") val automaticName: Boolean = false,
    val composer: WorktreeComposerOptions? = null,
)

/** A GitHub issue or pull request a worktree can start from (`worktree/source`'s items). */
@Serializable
data class WorktreeSource(
    val type: String = "",
    val number: Int = 0,
    val title: String = "",
    val url: String = "",
    val branchName: String? = null,
    val baseRefName: String? = null,
    val isCrossRepository: Boolean? = null,
) {
    /** What creation sends as `source_id`: `pr:12`, `issue:7`. */
    val sourceId: String get() = "$type:$number"
}

/** [items] found, and [message]: the provider's warning and errors in one line, null when it had none. */
data class WorktreeSources(val items: List<WorktreeSource>, val message: String? = null)

/**
 * A `worktree/create` body. Composer mode ([composer]) takes [name], [branchOverride], [baseRef], the parent, [note],
 * [sparsePresetId] and the source; the older form takes [branch] and [path], or [autoName]. Equal requests are the same
 * intent: an unchanged one keeps its operation id.
 */
data class WorktreeCreateRequest(
    val workspaceId: String,
    val kind: String? = null,
    val composer: Boolean = false,
    val branch: String? = null,
    val path: String? = null,
    val autoName: Boolean = false,
    val name: String? = null,
    val branchOverride: String? = null,
    val baseRef: String? = null,
    val parentWorkspaceId: String? = null,
    val noParent: Boolean = false,
    val note: String? = null,
    val sparsePresetId: String? = null,
    val sourceId: String? = null,
    val sourceUrl: String? = null,
)

/** What `worktree/create` opened: the actual workspace, folder and branch (never rebuilt from what was asked). */
@Serializable
data class WorktreeCreated(
    @SerialName("workspace_id") val workspaceId: String = "",
    @SerialName("parent_workspace_id") val parentWorkspaceId: String? = null,
    @SerialName("tab_id") val tabId: String? = null,
    val pane: String? = null,
    val terminal: String? = null,
    val path: String = "",
    val branch: String = "",
)

/** How a create ended: made, still running on the node, or not known (the receipt or a replay says later). */
sealed interface WorktreeCreateOutcome {
    data class Created(val worktree: WorktreeCreated) : WorktreeCreateOutcome
    data object Pending : WorktreeCreateOutcome
    data object Unknown : WorktreeCreateOutcome
}

/**
 * `worktree/delete`'s preview: the exact workspace on this computer's local runtime ([hostId] `local`, Orca's
 * [instanceId]), its [path] and [branch], how many files have uncommitted changes ([dirty]), whether it is only a folder's
 * entry ([folder]), and the [previewToken] a confirmation spends. Null fields were missing from the answer.
 */
@Serializable
data class WorktreeDeletionPreview(
    @SerialName("workspace_id") val workspaceId: String = "",
    val kind: String = "",
    @SerialName("host_id") val hostId: String = "",
    @SerialName("instance_id") val instanceId: String = "",
    val path: String = "",
    val branch: String? = null,
    val dirty: Int? = null,
    val folder: Boolean? = null,
    @SerialName("preview_token") val previewToken: String = "",
    @SerialName("close_only") val closeOnly: Boolean = false,
)

/** A confirmation (or, with [reconcile], a check) of a previewed deletion: [force] acknowledges the previewed changes. */
data class WorktreeDeletion(
    val workspaceId: String,
    val kind: String,
    val previewToken: String,
    val force: Boolean = false,
    val deleteBranch: Boolean = false,
    val reconcile: Boolean = false,
)

/** What a deletion did: only [removed] with [state] `removed` is a confirmed removal; anything else is still uncertain. */
@Serializable
data class WorktreeDeletionResult(
    @SerialName("workspace_id") val workspaceId: String = "",
    val removed: Boolean = false,
    val state: String = "",
    @SerialName("branch_deleted") val branchDeleted: Boolean? = null,
)

/**
 * A [WorktreeManagement] that hands every call to the one [route] names for its computer, with the machine id that one
 * knows it by (a hub group's own id, a merged list's owner). [route] throws when no hub has the computer.
 */
fun routedWorktreeManagement(route: (machineId: String) -> Pair<WorktreeManagement, String>): WorktreeManagement =
    object : WorktreeManagement {
        override suspend fun fullAccess(machineId: String) = route(machineId).let { (to, id) -> to.fullAccess(id) }
        override suspend fun prepareWorktree(machineId: String, workspaceId: String, kind: String, composer: Boolean) =
            route(machineId).let { (to, id) -> to.prepareWorktree(id, workspaceId, kind, composer) }
        override suspend fun createWorktree(machineId: String, request: WorktreeCreateRequest, operationId: String) =
            route(machineId).let { (to, id) -> to.createWorktree(id, request, operationId) }
        override suspend fun worktreeReceipt(machineId: String, operationId: String) =
            route(machineId).let { (to, id) -> to.worktreeReceipt(id, operationId) }
        override suspend fun worktreeSources(machineId: String, workspaceId: String, kind: String, query: String) =
            route(machineId).let { (to, id) -> to.worktreeSources(id, workspaceId, kind, query) }
        override suspend fun saveWorktreePreset(machineId: String, workspaceId: String, kind: String, name: String, directories: String) =
            route(machineId).let { (to, id) -> to.saveWorktreePreset(id, workspaceId, kind, name, directories) }
        override suspend fun renameWorktree(machineId: String, workspaceId: String, kind: String, name: String) =
            route(machineId).let { (to, id) -> to.renameWorktree(id, workspaceId, kind, name) }
        override suspend fun previewWorktreeDeletion(machineId: String, workspaceId: String, kind: String) =
            route(machineId).let { (to, id) -> to.previewWorktreeDeletion(id, workspaceId, kind) }
        override suspend fun deleteWorktree(machineId: String, deletion: WorktreeDeletion) =
            route(machineId).let { (to, id) -> to.deleteWorktree(id, deletion) }
    }
