package persona.feature.agents

import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.AgentWorkspace
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JRadio
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader

/**
 * New Agent's "Workspace" section (#7259): the selected computer's Orca and Paseo workspaces by project, drawn as the
 * recent folders are (a radio, the title, then the kind and branch over the folder). Nothing when there are none.
 */
internal fun LazyListScope.newAgentWorkspaceItems(state: NewAgentState, editable: Boolean, onPick: (AgentWorkspace) -> Unit) {
    if (state.workspaces.isEmpty()) return
    item("workspaces-title") { JSectionHeader(stringResource(R.string.agents_workspace_title)) }
    for (section in state.workspaces) {
        item("workspaces-group:${section.key}") {
            JSectionHeader(section.heading ?: stringResource(R.string.agents_workspace_other), sub = true)
        }
        itemsIndexed(section.rows, key = { _, row -> "workspace:${row.workspace.source}:${row.workspace.sourceId}" }) { index, row ->
            val picked = state.workspace
            val selected = picked != null && picked.source == row.workspace.source && picked.sourceId == row.workspace.sourceId
            JGroupItem(GroupPosition.of(index, section.rows.size)) {
                JRow(row.title, subtitle = workspaceLine(row) + "\n" + row.workspace.cwd, subtitleLines = 3,
                    leading = { JRadio(selected, null, enabled = editable) }, enabled = editable,
                    onClick = { onPick(row.workspace) }, chevron = false)
            }
        }
    }
}

/** "Main checkout", "Worktree" or "Folder", then " · branch" when a Git row's branch is not its title. */
@Composable
internal fun workspaceLine(row: NewAgentWorkspaceRow): String {
    val kind = stringResource(when (row.kind) {
        NewAgentWorkspaceKind.Main -> R.string.agents_workspace_main
        NewAgentWorkspaceKind.Worktree -> R.string.agents_repo_worktree
        NewAgentWorkspaceKind.Folder -> R.string.agents_workspace_folder
    })
    return row.branch?.let { stringResource(R.string.agents_workspace_kind_branch, kind, it) } ?: kind
}
