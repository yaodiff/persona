package persona.feature.agents

import androidx.annotation.StringRes
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.AgentWorkspace
import persona.core.contracts.api.WorktreeCreateRequest
import persona.core.contracts.api.WorktreePrepare
import persona.core.contracts.api.WorktreeSource
import persona.core.designsystem.UiText
import persona.core.domain.sort.hostKind

/** The engine whose worktrees Android creates, renames and deletes (#17054). */
internal const val ORCA_KIND = "orca"

/**
 * One Orca worktree an action is for (#17054, the web's `WorkspaceTarget`): the catalog row's exact identity on its
 * computer, its folder and the name it shows. Folders and names are never identities; [workspaceId] is.
 */
internal data class WorktreeTarget(val machineId: String, val workspaceId: String, val cwd: String, val label: String) {
    val kind: String get() = ORCA_KIND
    /** What the registries key it by, within one pairing. */
    val key: String get() = listOf(machineId, kind, workspaceId, cwd).joinToString("\u0000")

    companion object {
        fun of(row: AgentWorkspace) = WorktreeTarget(row.machineId, row.sourceId, row.cwd, row.label)
    }
}

/** What a catalog row is called: its title, else its branch, else its folder's name. */
internal val AgentWorkspace.label: String
    get() = title.ifBlank { branch?.takeIf(String::isNotBlank) ?: cwd.trimEnd('/', '\\').substringAfterLast('/').substringAfterLast('\\') }

/** The Orca worktree a session runs in (its terminal's or its chat's), by id; null for any other engine's. */
internal val Session.orcaWorktreeId: String?
    get() = operationWorkspaceId?.takeIf { hostKind.ifEmpty { spaceKind.orEmpty() } == ORCA_KIND }

/** Whether this device may change worktrees: unknown until the hub answers, then full or read-only. */
internal enum class WorktreeAccess { Checking, Full, ReadOnly }

/**
 * What may be done to a [WorktreeTarget] now (the web's `resolveWorktreeAction`): [workspace] is its catalog row when the
 * catalog lists it exactly once at the same folder; [reason] says why nothing can change (null: it can).
 */
internal data class WorktreeGate(
    val workspace: AgentWorkspace?,
    val machine: Machine?,
    @StringRes val reason: Int?,
    val hasChildren: Boolean,
    val canRename: Boolean,
    val canCreate: Boolean,
    val canDelete: Boolean,
    val canReconcile: Boolean,
) {
    val writable: Boolean get() = reason == null
    /** Why Delete is off on a writable worktree: the main checkout, or one with worktrees under it. */
    @get:StringRes val protection: Int? get() = when {
        hasChildren -> R.string.agents_worktree_has_children
        workspace?.isMain == true -> R.string.agents_worktree_protected
        else -> null
    }
}

internal object WorktreeGates {
    /** The engines a computer enables: the hub's set, else the one its single engine stands for (web `currentHosts`). */
    fun hosts(machine: Machine): List<String> = machine.engineHosts ?: when (machine.engine) {
        "herdr-both" -> listOf("herdr", "herdr-core")
        "" -> listOf("herdr")
        else -> listOf(machine.engine)
    }

    /** Orca is enabled on [machine], and the hub has not reported it unavailable there. */
    fun orcaUsable(machine: Machine): Boolean =
        ORCA_KIND in hosts(machine) && machine.engines.none { it.id == ORCA_KIND && !it.available }

    fun resolve(target: WorktreeTarget, rows: List<AgentWorkspace>, machines: List<Machine>, access: WorktreeAccess): WorktreeGate {
        val local = rows.filter { it.machineId == target.machineId && it.source == ORCA_KIND }
        val matches = local.filter { it.sourceId == target.workspaceId }
        val workspace = matches.singleOrNull()?.takeIf { it.cwd == target.cwd }
        val machine = machines.singleOrNull { it.id == target.machineId }
        val engine = machine != null && machine.online && orcaUsable(machine)
        val available = workspace != null && engine && workspace.online != false && workspace.available != false
        val writable = access == WorktreeAccess.Full
        val reason = when {
            !available -> R.string.agents_worktree_unavailable
            access == WorktreeAccess.Checking -> R.string.agents_worktree_checking_access
            !writable -> R.string.agents_worktree_read_only
            else -> null
        }
        val hasChildren = workspace != null && local.any { it.parentSourceId == workspace.sourceId && it.sourceId != workspace.sourceId }
        return WorktreeGate(
            workspace, machine, reason, hasChildren,
            canRename = reason == null,
            canCreate = reason == null,
            canDelete = reason == null && workspace?.isMain != true && !hasChildren,
            // A dispatched removal can still be checked once its row is gone, while the computer and Orca answer.
            canReconcile = writable && engine && (matches.isEmpty() || available),
        )
    }

    /** The Orca worktree [sessions] all run in, as the catalog lists it; null when they are in none or in several. */
    fun targetOf(sessions: List<Session>, rows: List<AgentWorkspace>): WorktreeTarget? {
        val ids = sessions.mapNotNullTo(mutableSetOf()) { s -> s.orcaWorktreeId?.let { s.machineId to it } }
        val (machine, id) = ids.singleOrNull() ?: return null
        val row = rows.filter { it.machineId == machine && it.source == ORCA_KIND && it.sourceId == id }.singleOrNull() ?: return null
        return WorktreeTarget.of(row)
    }
}

/** Where a new worktree sits in Orca's sidebar: under its source (the default), at the top, or under another worktree. */
internal sealed interface WorktreeParent {
    data object Source : WorktreeParent
    data object None : WorktreeParent
    data class Workspace(val id: String, val label: String) : WorktreeParent
}

/**
 * The New worktree form (the web's `CreationDraft` and `RepositoryDraft` for one source): the composer's [name], [baseRef],
 * [preset], [provider], [note] and [branch] override, or the basic form's [branch] and [path]; [basic] uses the basic form
 * on a computer whose composer is missing or broken.
 */
internal data class WorktreeDraft(
    val name: String = "",
    val branch: String = "",
    val path: String = "",
    val pathEdited: Boolean = false,
    val automaticName: Boolean = false,
    val note: String = "",
    val baseRef: String = "",
    val preset: String = "",
    val provider: WorktreeSource? = null,
    val parent: WorktreeParent = WorktreeParent.Source,
    val basic: Boolean = false,
) {
    /** Choices only the composer takes: the basic form refuses them rather than silently making something else. */
    val hasAdvanced: Boolean get() = name.isNotBlank() || note.isNotBlank() || baseRef.isNotEmpty() || preset.isNotEmpty() || provider != null
}

/** The draft as a request, or why it cannot be sent. */
internal sealed interface WorktreeBody {
    data class Ready(val request: WorktreeCreateRequest) : WorktreeBody
    data class Problem(val text: UiText) : WorktreeBody
}

internal object WorktreeForms {
    /** The directory a branch's checkout gets (the node's WorktreeSlug, `AgentWorktree.slug`). */
    fun suggestedPath(base: String, branch: String): String {
        val slug = AgentWorktree.slug(branch.trim())
        if (base.isEmpty() || slug.isEmpty()) return ""
        val sep = if ('\\' in base && '/' !in base) "\\" else "/"
        return base.trimEnd('/', '\\') + sep + slug
    }

    /** The branch was typed: the path follows it, unless it was edited by hand. */
    fun typeBranch(draft: WorktreeDraft, base: String, branch: String) =
        draft.copy(branch = branch, path = if (draft.pathEdited) draft.path else suggestedPath(base, branch))

    /** The path was typed. Emptied, it follows the branch again. */
    fun typePath(draft: WorktreeDraft, base: String, path: String) =
        if (path.isEmpty()) draft.copy(path = suggestedPath(base, draft.branch), pathEdited = false)
        else draft.copy(path = path, pathEdited = true)

    /** Why git would not take [raw] as a branch name (`git check-ref-format --branch`), null when it would. */
    @StringRes
    fun branchProblem(raw: String): Int? {
        val b = raw.trim()
        return when {
            b.isEmpty() -> R.string.agents_worktree_branch_required
            b.startsWith("-") -> R.string.agents_worktree_branch_dash
            b == "@" || b.any { it.isWhitespace() || it in "~^:?*[\\" || it.code < 0x20 || it.code == 0x7f } -> R.string.agents_worktree_branch_chars
            ".." in b || "@{" in b || "//" in b -> R.string.agents_worktree_branch_pattern
            b.startsWith("/") || b.endsWith("/") || b.endsWith(".") || b.endsWith(".lock") -> R.string.agents_worktree_branch_edges
            b.split('/').any { it.startsWith(".") || it.endsWith(".lock") } -> R.string.agents_worktree_branch_parts
            else -> null
        }
    }

    /**
     * The request [draft] makes for [target] (the web's `creationBody`). The composer's choices go only to a computer that
     * advertised them; the basic form never takes one of them, so nothing is downgraded into a different checkout.
     */
    fun body(target: WorktreeTarget, draft: WorktreeDraft, info: WorktreePrepare): WorktreeBody {
        val unsupported = WorktreeBody.Problem(UiText.Res(R.string.agents_worktree_unsupported))
        val composer = info.composer?.takeIf { !draft.basic }
        val parentId = (draft.parent as? WorktreeParent.Workspace)?.id
        val noParent = draft.parent == WorktreeParent.None
        if (composer != null) {
            val branch = draft.branch.trim()
            if (!composer.has("create")) return unsupported
            if (branch.isNotEmpty() && !composer.has("branch_override")) return unsupported
            if (draft.provider != null && !composer.has("source.github")) return unsupported
            if (draft.preset.isNotEmpty() && (!composer.has("sparse_presets") || composer.presets.none { it.id == draft.preset })) return unsupported
            if (draft.baseRef.isNotEmpty() && draft.baseRef != composer.baseRef && draft.baseRef !in composer.refs)
                return WorktreeBody.Problem(UiText.Res(R.string.agents_worktree_base_missing))
            if (branch.isNotEmpty()) branchProblem(branch)?.let { return WorktreeBody.Problem(UiText.Res(it)) }
            return WorktreeBody.Ready(WorktreeCreateRequest(
                workspaceId = target.workspaceId, kind = target.kind, composer = true,
                name = draft.name.trim().ifEmpty { null }, branchOverride = branch.ifEmpty { null },
                baseRef = draft.baseRef.ifEmpty { composer.baseRef }.ifEmpty { null },
                parentWorkspaceId = parentId, noParent = noParent, note = draft.note.trim().ifEmpty { null },
                sparsePresetId = draft.preset.ifEmpty { null },
                sourceId = draft.provider?.sourceId, sourceUrl = draft.provider?.url,
            ))
        }
        if (draft.hasAdvanced) return unsupported
        if (draft.parent != WorktreeParent.Source && !info.chooseParent) return unsupported
        if (draft.automaticName && !info.automaticName) return unsupported
        val path = (if (draft.pathEdited) draft.path else suggestedPath(info.path, draft.branch)).trim()
        if (!draft.automaticName) {
            branchProblem(draft.branch)?.let { return WorktreeBody.Problem(UiText.Res(it)) }
            if (path.isEmpty()) return WorktreeBody.Problem(UiText.Res(R.string.agents_worktree_path_required))
        }
        return WorktreeBody.Ready(WorktreeCreateRequest(
            workspaceId = target.workspaceId, kind = target.kind,
            parentWorkspaceId = parentId.takeIf { info.chooseParent }, noParent = noParent && info.chooseParent,
            autoName = draft.automaticName,
            branch = draft.branch.trim().takeUnless { draft.automaticName }, path = path.takeUnless { draft.automaticName },
        ))
    }
}
