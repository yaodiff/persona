package persona.feature.agents

import persona.core.designsystem.dense
import persona.core.designsystem.text
import persona.core.designsystem.*
import androidx.annotation.StringRes
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import persona.core.designsystem.icons.PersonaIcons
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import persona.core.contracts.Session
import persona.core.contracts.api.AgentWorkspace
import persona.core.domain.engines.Engines
import persona.core.domain.sort.SessionRename
import persona.core.domain.sort.canTerminal
import persona.core.domain.sort.closeConfirmation
import persona.core.domain.sort.deleteConfirmation
import persona.core.domain.sort.isRex
import persona.core.domain.sort.isTern
import persona.core.domain.sort.usesUnifiedHerdrRename
import persona.core.domain.sort.worktreeKind

internal sealed interface AgentOperationDialog {
    val session: Session
    data class Rename(override val session: Session) : AgentOperationDialog
    data class RenameSpace(override val session: Session) : AgentOperationDialog
    data class Close(override val session: Session) : AgentOperationDialog
    data class Delete(override val session: Session) : AgentOperationDialog
}

@Composable
internal fun workspaceCopyPathAction(workspace: AgentWorkspace?): JAction? {
    val path = workspace?.cwd?.takeIf(String::isNotBlank) ?: return null
    val copy = rememberCopy()
    return JAction(stringResource(R.string.agents_copy_workspace_path), PersonaIcons.copy, onClick = { copy(path, null) })
}

@Composable
internal fun AgentOperationDialog(
    dialog: AgentOperationDialog, model: AgentsViewModel, machineName: String? = null,
    /** Worktree status and removal (#4532); null: no keep/remove choice. */
    worktrees: persona.core.contracts.api.AgentCreationService? = null, machine: persona.core.contracts.Machine? = null,
    preferHerdrTabName: Boolean = false,
    dismiss: () -> Unit,
) {
    val session = dialog.session
    // Keep or remove the worktree Persona made for it, when it worked in one (#4532); removed once it is gone.
    val ending = dialog is AgentOperationDialog.Close || dialog is AgentOperationDialog.Delete
    val worktree = persona.feature.chat.rememberOurWorktree(worktrees.takeIf { ending }, session.machineId, session.cwd, machine?.online == true,
        session.worktreeKind)
    var cleanup by remember(session.key) { mutableStateOf(persona.feature.chat.WorktreeCleanup()) }
    val feedback = persona.core.designsystem.LocalAppFeedback.current
    val res = androidx.compose.ui.platform.LocalResources.current
    fun cleanUp() { if (worktrees != null && worktree != null)
        persona.feature.chat.WorktreeCleanups.cleanUp(worktrees, session.machineId, worktree, cleanup, feedback, res, session.worktreeKind) }
    val cleanupSection: (@Composable androidx.compose.foundation.layout.ColumnScope.() -> Unit)? = worktree?.let { st ->
        { persona.feature.chat.WorktreeCleanupSection(st, cleanup, { cleanup = it }, machine?.home) } }
    when (dialog) {
        // The same words as the chat's menu (core:domain closeConfirmation); long press and swipe both land here.
        is AgentOperationDialog.Close -> {
            val confirmation = session.closeConfirmation(machineName)
            JDialog(
                onDismiss = dismiss, title = confirmation.title.text(), destructive = true, text = confirmation.message.text(),
                confirm = stringResource(R.string.agents_action_close_agent) to {
                    // It may have been closed elsewhere since the dialog opened: then there is nothing left to do.
                    if (model.closable(session)) model.close(session, onDone = ::cleanUp)
                    dismiss()
                }, confirmEnabled = persona.feature.chat.WorktreeCleanups.ready(worktree, cleanup), content = cleanupSection,
            )
        }
        // The same words as the chat's menu (core:domain deleteConfirmation).
        is AgentOperationDialog.Delete -> {
            val confirmation = session.deleteConfirmation(machineName)
            JDialog(
                onDismiss = dismiss, title = confirmation.title.text(), destructive = true, text = confirmation.message.text(),
                confirm = stringResource(R.string.agents_dialog_delete) to {
                    // It may have started again since the dialog opened: then the hub would refuse it.
                    if (model.deletable(session)) model.delete(session, onDone = ::cleanUp)
                    dismiss()
                }, confirmEnabled = persona.feature.chat.WorktreeCleanups.ready(worktree, cleanup), content = cleanupSection,
            )
        }
        else -> {
            val space = dialog is AgentOperationDialog.RenameSpace
            val unified = !space && session.usesUnifiedHerdrRename(preferHerdrTabName)
            var name by rememberSaveable(session.key.path, space, unified) { mutableStateOf(if (space) session.spaceName.orEmpty() else session.title) }
            var tab by rememberSaveable(session.key.path, unified) { mutableStateOf(session.slotName.orEmpty()) }
            val hasTab = !space && !session.paneId.isNullOrBlank()
            val newName = name.trim().takeIf { it.isNotBlank() && it != (if (space) session.spaceName else session.title) }
            val newTab = SessionRename.changes(session, name, tab).tabName
            val unifiedEdit = if (unified) SessionRename.unifiedChanges(session, tab) else SessionRename()
            JDialog(
                onDismiss = dismiss, title = stringResource(if (space) R.string.agents_dialog_rename_space else R.string.agents_action_rename),
                // the engine the tab or workspace is in, by name (#17054): herdr, Orca, Herdr App
                text = renameNote(session, space, unified)?.let { stringResource(it, Engines.sessionEngineName(session)) },
                confirmEnabled = if (unified) unifiedEdit.changed else newName != null || newTab != null,
                confirm = stringResource(R.string.agents_dialog_save) to {
                    when {
                        space -> model.renameSpace(session, requireNotNull(newName))
                        unified -> model.renameUnified(session, tab)
                        else -> model.rename(session, newName, newTab)
                    }
                    dismiss()
                }, content = {
                    Column(verticalArrangement = Arrangement.spacedBy(12.dp.dense)) {
                        if (unified) {
                            JTextField(tab, { tab = it }, singleLine = true, label = stringResource(R.string.agents_dialog_name))
                        } else {
                            if (hasTab) JTextField(tab, { tab = it }, singleLine = true, label = stringResource(tabNameLabel(session), Engines.sessionEngineName(session)))
                            JTextField(name, { name = it }, singleLine = true, label = stringResource(if (space) R.string.agents_dialog_space_name else R.string.agents_dialog_session_name))
                        }
                    }
                },
            )
        }
    }
}

/**
 * The note of a rename dialog: what else the new name renames (iOS `SpaceRenameForm.note`, `SessionRenameForm.tabNote`). A
 * space is its engine's workspace (herdr's, an Orca worktree), or in Tern or Rex one of its sessions (their spaces are their
 * sessions); a pane's tab is its engine's. The generic notes take the engine's name (`Engines.sessionEngineName`, #17054).
 * None for a session without a terminal (a runtime's, #6294): it has no tab, and no Claude's /rename.
 */
@StringRes
internal fun renameNote(session: Session, space: Boolean, unified: Boolean = false): Int? = when {
    space -> when (session.spaceKind) {
        "tern" -> R.string.agents_dialog_rename_space_text_tern
        "rex" -> R.string.agents_dialog_rename_space_text_rex
        else -> R.string.agents_dialog_rename_space_text
    }
    !session.canTerminal -> null
    unified -> R.string.agents_dialog_rename_text_unified
    session.isTern -> R.string.agents_dialog_rename_text_tern
    session.isRex -> R.string.agents_dialog_rename_text_rex
    else -> R.string.agents_dialog_rename_text
}

/** The tab field of a session's rename: the tab is renamed in its engine, named by the generic label's argument. */
@StringRes
internal fun tabNameLabel(session: Session): Int = when {
    session.isTern -> R.string.agents_dialog_tab_name_tern
    session.isRex -> R.string.agents_dialog_tab_name_rex
    else -> R.string.agents_dialog_tab_name
}

/** "Link to to-do…" / "Move to another to-do…" / "Unlink from to-do" of a project (iOS SpaceTodoMenu); [linked] when it is on a to-do. */
internal class SpaceTodoActions(val linked: Boolean, val onLink: () -> Unit, val onUnlink: (() -> Unit)?)

/** The name a project goes by on a to-do: its herdr name, else its id; null outside a herdr space (iOS SpaceGroup.todoSpace). */
internal val Session.todoSpaceName: String?
    get() = spaceId?.takeIf { it.isNotBlank() }?.let { id -> spaceName?.takeIf { it.isNotBlank() } ?: id }

/**
 * The actions of a space. [onOpen] adds "Open space" (the list offers it here instead of a separate button);
 * [compact] is the smaller trigger of list headings. A space without a herdr name can only be opened and read.
 * [extra] goes last (an Orca space's New worktree…, #17054).
 */
@Composable
internal fun AgentSpaceMenu(session: Session, pinned: Boolean, muted: Boolean, busy: Boolean,
    onPin: () -> Unit, onMute: () -> Unit, onRename: () -> Unit, onMarkRead: (() -> Unit)? = null,
    onOpen: (() -> Unit)? = null, compact: Boolean = false, todo: SpaceTodoActions? = null, extra: List<JAction> = emptyList()) {
    val named = !session.spaceName.isNullOrBlank()
    if (!named && onOpen == null && onMarkRead == null && todo == null && extra.isEmpty()) return
    var menu by remember { mutableStateOf(false) }
    val actions = stringResource(R.string.agents_space_actions)
    if (compact) HeadingAction(PersonaIcons.more, actions, onClick = { menu = true })
    else JIconButton(PersonaIcons.more, actions, { menu = true })
    if (menu) JActionSheet(agentSpaceActions(session, pinned, muted, busy, onPin, onMute, onRename, onMarkRead, onOpen, todo) + extra,
        { menu = false }, title = session.spaceName?.takeIf { it.isNotBlank() } ?: actions)
}

/**
 * A space's actions, in its ⋯ sheet ([AgentSpaceMenu]) and in the long press of a repository's header on Glass (iOS's
 * `repoMenu`): open it, mark its agents read, pin, mute and rename it, put it on a to-do.
 */
@Composable
internal fun agentSpaceActions(session: Session, pinned: Boolean, muted: Boolean, busy: Boolean,
    onPin: () -> Unit, onMute: () -> Unit, onRename: () -> Unit, onMarkRead: (() -> Unit)? = null,
    onOpen: (() -> Unit)? = null, todo: SpaceTodoActions? = null): List<JAction> = buildList {
    if (onOpen != null) add(JAction(stringResource(R.string.agents_space_open), PersonaIcons.allMachines, onClick = onOpen))
    if (onMarkRead != null) add(JAction(stringResource(R.string.agents_mark_all_read), PersonaIcons.done, onClick = onMarkRead))
    if (!session.spaceName.isNullOrBlank()) {
        add(JAction(stringResource(if (pinned) R.string.agents_space_unpin else R.string.agents_space_pin), PersonaIcons.highPriority, enabled = !busy, onClick = onPin))
        add(JAction(stringResource(if (muted) R.string.agents_space_unmute else R.string.agents_space_mute), PersonaIcons.interrupt, enabled = !busy, onClick = onMute))
        add(JAction(stringResource(R.string.agents_dialog_rename_space), PersonaIcons.compose, enabled = !busy && session.operationWorkspaceId != null, onClick = onRename))
    }
    if (todo != null) {
        add(JAction(stringResource(if (todo.linked) R.string.agents_space_todo_move else R.string.agents_space_todo_link), PersonaIcons.backgroundTasks,
            separatorBefore = isNotEmpty(), onClick = todo.onLink))
        todo.onUnlink?.let { add(JAction(stringResource(R.string.agents_space_todo_unlink), PersonaIcons.link, destructive = true, onClick = it)) }
    }
}

/** Asks before "Mark all as read" and names how many finished sessions it marks (iOS confirmReadAll). */
@Composable
internal fun MarkAllReadDialog(count: Int, onDismiss: () -> Unit, onConfirm: () -> Unit) {
    JDialog(onDismiss = onDismiss, title = pluralStringResource(R.plurals.agents_mark_all_read_confirm, count, count),
        confirm = stringResource(R.string.agents_mark_all_read_action, count) to { onDismiss(); onConfirm() })
}
