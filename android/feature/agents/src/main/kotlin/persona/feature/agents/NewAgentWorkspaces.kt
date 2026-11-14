package persona.feature.agents

import persona.core.contracts.api.AgentWorkspace

/** What a catalog workspace is to the person picking it (#7259): its main checkout, another worktree, or a plain folder. */
internal enum class NewAgentWorkspaceKind { Main, Worktree, Folder }

/**
 * One workspace New Agent offers: [title] its first line, [kind] and [branch] (only a Git row's branch that is not its
 * title) the second. Starting in it sends the [workspace]'s `source_id`, `source` and `cwd`.
 */
internal data class NewAgentWorkspaceRow(
    val workspace: AgentWorkspace,
    val title: String,
    val kind: NewAgentWorkspaceKind,
    val branch: String?,
)

/** A project's workspaces under its [heading]; null: the last group, "Other workspaces". */
internal data class NewAgentWorkspaceSection(val key: String, val heading: String?, val rows: List<NewAgentWorkspaceRow>)

/**
 * New Agent's workspace choice (#7259), the same rule as iOS and the web: the selected computer's Orca and Paseo
 * workspaces that can take an agent now, grouped by project (sorted by name, "Other workspaces" last), each project's
 * main checkout first, then its worktrees, then its folders.
 */
internal object NewAgentWorkspaces {
    /** The engines whose workspaces a new agent can start in from here; herdr spaces and folders are picked otherwise. */
    val SOURCES = setOf("orca", "paseo")

    fun sections(rows: List<AgentWorkspace>, machineId: String): List<NewAgentWorkspaceSection> {
        val offered = rows.filter {
            it.machineId == machineId && it.source in SOURCES && it.available != false && it.online != false &&
                it.sourceId.isNotBlank() && it.cwd.isNotBlank()
        }.distinctBy { it.source to it.sourceId }
        if (offered.isEmpty()) return emptyList()
        val projects = offered.groupBy { row -> row.projectId?.takeIf(String::isNotBlank)?.let { "${row.source}\u0000$it" } }
        val named = mutableListOf<NewAgentWorkspaceSection>()
        val other = mutableListOf<AgentWorkspace>()
        for ((key, members) in projects) {
            val heading = key?.let { heading(members) }
            if (key == null || heading == null) other += members
            else named += NewAgentWorkspaceSection(key, heading, ordered(members))
        }
        val sorted = named.sortedWith(compareBy<NewAgentWorkspaceSection, String>(String.CASE_INSENSITIVE_ORDER) { it.heading.orEmpty() }
            .thenBy { it.key })
        return if (other.isEmpty()) sorted else sorted + NewAgentWorkspaceSection("other", null, ordered(other))
    }

    fun kind(row: AgentWorkspace): NewAgentWorkspaceKind = when {
        row.workspaceKind == "folder" -> NewAgentWorkspaceKind.Folder
        row.isMain == true -> NewAgentWorkspaceKind.Main
        else -> NewAgentWorkspaceKind.Worktree
    }

    /** Its title, else its branch, else its folder's name. */
    fun title(row: AgentWorkspace): String = row.title.trim().ifEmpty { row.branch?.trim().orEmpty() }.ifEmpty { lastElement(row.cwd) }

    fun row(workspace: AgentWorkspace): NewAgentWorkspaceRow {
        val title = title(workspace)
        val kind = kind(workspace)
        val branch = workspace.branch?.trim()?.takeIf { kind != NewAgentWorkspaceKind.Folder && it.isNotEmpty() && it != title }
        return NewAgentWorkspaceRow(workspace, title, kind, branch)
    }

    /** The project's name, else its folder's name; null when it has neither (its rows go to "Other workspaces"). */
    private fun heading(members: List<AgentWorkspace>): String? =
        members.firstNotNullOfOrNull { it.projectName?.trim()?.takeIf(String::isNotEmpty) }
            ?: members.firstNotNullOfOrNull { row -> row.projectPath?.let(::lastElement)?.takeIf(String::isNotEmpty) }

    private fun ordered(members: List<AgentWorkspace>): List<NewAgentWorkspaceRow> = members.map(::row)
        .sortedWith(compareBy<NewAgentWorkspaceRow> { it.kind.ordinal }.thenBy(String.CASE_INSENSITIVE_ORDER) { it.title })

    private fun lastElement(path: String): String = path.trim().trimEnd('/', '\\').substringAfterLast('/').substringAfterLast('\\')
}
