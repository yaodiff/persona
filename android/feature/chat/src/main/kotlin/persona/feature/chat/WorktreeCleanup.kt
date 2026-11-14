package persona.feature.chat

import android.content.res.Resources
import androidx.compose.foundation.background
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import persona.core.contracts.api.AgentCreationService
import persona.core.contracts.api.WorktreeRemoved
import persona.core.contracts.api.WorktreeStatus
import persona.core.contracts.api.hubCodedFailure
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons

// What to do with the worktree a closed agent or deleted chat worked in, when Persona made it (#4532, iOS
// `AgentWorktree.Cleanup`, `WorktreeCleanupSection`, web lib/worktree.ts): keep it (the default), or remove it, its branch
// too if asked; what removing loses (changed files, and with the branch its commits main lacks) must be confirmed first,
// and only then is `force` sent.

/** The choice in Close Agent / Delete Chat; [confirmed]: "I understand these changes will be lost" is on. */
data class WorktreeCleanup(val remove: Boolean = false, val deleteBranch: Boolean = false, val confirmed: Boolean = false)

object WorktreeCleanups {
    /** `worktree/remove` tries: the node may still list the agent for a moment after it closed (`worktree_in_use`). */
    const val REMOVE_TRIES = 6

    /** Changed files lost, and with the branch, its commits the main branch lacks. */
    fun loss(st: WorktreeStatus, deleteBranch: Boolean): Pair<Int, Int> = st.dirty to (if (deleteBranch) st.unmerged else 0)

    /** Removing it would lose something: the confirmation shows and the button waits for it. */
    fun losing(st: WorktreeStatus, c: WorktreeCleanup): Boolean = c.remove && loss(st, c.deleteBranch).let { (d, u) -> d > 0 || u > 0 }

    /** Nothing unconfirmed stands in the way of closing or deleting. */
    fun ready(st: WorktreeStatus?, c: WorktreeCleanup): Boolean = st == null || !losing(st, c) || c.confirmed

    /** `force` only for losses the user confirmed. */
    fun force(st: WorktreeStatus, c: WorktreeCleanup): Boolean = losing(st, c) && c.confirmed

    /** The status a close or delete offers the cleanup for: one Persona made; null otherwise. */
    fun offered(st: WorktreeStatus?): WorktreeStatus? = st?.takeIf { it.ours && it.path.isNotEmpty() }

    /**
     * Removes it once its agent is gone, a few tries [wait] ms apart while the node still lists the agent; by the engine
     * the agent ran in ([kind], `Session.worktreeKind`).
     */
    suspend fun remove(service: AgentCreationService, machineId: String, st: WorktreeStatus, c: WorktreeCleanup,
                       kind: String? = null, tries: Int = REMOVE_TRIES, wait: Long = 1000): WorktreeRemoved {
        var attempt = 1
        while (true) {
            try { return service.removeWorktree(machineId, st.path, c.deleteBranch, force(st, c), kind) }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                if (attempt >= tries || hubCodedFailure(e)?.code != "worktree_in_use") throw e
                attempt++
                delay(wait)
            }
        }
    }

    // ponytail: a process-wide scope, so the removal outlives the chat page that closes with its agent; a few seconds of
    // network at most. Move it to an app-level owner if more such follow-ups appear.
    private val scope = MainScope()

    /** After its agent was closed or its chat deleted: removes it when asked ([kind]: as [remove]), and says how it went app-wide. */
    fun cleanUp(service: AgentCreationService, machineId: String, st: WorktreeStatus, c: WorktreeCleanup,
                feedback: AppFeedback?, resources: Resources, kind: String? = null) {
        if (!c.remove || st.path.isEmpty()) return
        scope.launch {
            val message = try {
                val r = remove(service, machineId, st, c, kind)
                resources.getString(if (r.branchDeleted) R.string.chat_worktree_removed_branch else R.string.chat_worktree_removed)
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                val words = e.message?.takeIf { hubCodedFailure(e)?.status == 409 && it.isNotBlank() }
                resources.getString(R.string.chat_worktree_remove_failed, words ?: operationFeedback(e).text.resolve(resources))
            }
            feedback?.notice(message)
        }
    }
}

/**
 * The worktree Persona made that [session] works in, asked when the dialog opens of the engine it ran in ([kind],
 * `Session.worktreeKind`); null while asking or otherwise.
 */
@Composable
fun rememberOurWorktree(service: AgentCreationService?, machineId: String, cwd: String, online: Boolean, kind: String? = null): WorktreeStatus? {
    var status by remember(machineId, cwd, kind) { mutableStateOf<WorktreeStatus?>(null) }
    LaunchedEffect(service, machineId, cwd, online, kind) {
        if (service == null || !online || cwd.isBlank()) return@LaunchedEffect
        status = WorktreeCleanups.offered(service.worktreeStatus(machineId, cwd, kind))
    }
    return status
}

/**
 * In Close Agent and Delete Chat when the agent worked in a worktree Persona made (iOS `WorktreeCleanupSection`): keep
 * it (the default) or remove it, its branch too, and what removing it loses, said with the counts and to be confirmed.
 */
@Composable
fun WorktreeCleanupSection(status: WorktreeStatus, cleanup: WorktreeCleanup, onChange: (WorktreeCleanup) -> Unit, home: String? = null) {
    val palette = PersonaTheme.palette
    val (dirty, unmerged) = WorktreeCleanups.loss(status, cleanup.deleteBranch)
    Column(Modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape).background(palette.sunken).padding(Tokens.Space.m)
        .testTag("worktree-cleanup"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.Top) {
            JIcon(PersonaIcons.gitBranch, null, size = Tokens.Size.iconXS, tint = palette.ink2, modifier = Modifier.padding(top = Tokens.Space.xxs))
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                Text(stringResource(R.string.chat_worktree_made), style = PersonaTheme.type.bodyStrong, color = palette.ink)
                if (status.branch.isNotEmpty()) Text(status.branch, style = PersonaTheme.type.codeSmall, color = palette.ink2, maxLines = 1)
                Text(home?.takeIf { it.isNotEmpty() && status.path.startsWith("$it/") }?.let { "~" + status.path.removePrefix(it) } ?: status.path,
                    style = PersonaTheme.type.codeSmall, color = palette.ink2, maxLines = 2)
            }
        }
        JSegmented(listOf(JSegment(stringResource(R.string.chat_worktree_keep)), JSegment(stringResource(R.string.chat_worktree_remove))),
            if (cleanup.remove) 1 else 0, { onChange(WorktreeCleanup(remove = it == 1, deleteBranch = cleanup.deleteBranch)) })
        if (cleanup.remove) {
            if (status.branch.isNotEmpty()) Toggle(stringResource(R.string.chat_worktree_delete_branch, status.branch), cleanup.deleteBranch) {
                onChange(cleanup.copy(deleteBranch = it, confirmed = false))
            }
            if (dirty > 0) Text(pluralStringResource(R.plurals.chat_worktree_dirty, dirty, dirty), style = PersonaTheme.type.caption, color = palette.danger.text)
            if (unmerged > 0) Text(pluralStringResource(R.plurals.chat_worktree_unmerged, unmerged, unmerged, status.mainBranch.ifEmpty { "main" }),
                style = PersonaTheme.type.caption, color = palette.danger.text)
            if (WorktreeCleanups.losing(status, cleanup)) Toggle(stringResource(R.string.chat_worktree_confirm_loss), cleanup.confirmed) {
                onChange(cleanup.copy(confirmed = it))
            }
        }
    }
}

/** A switch whose words wrap (a dialog is narrow: the list row's single line cut them off). */
@Composable
private fun Toggle(text: String, checked: Boolean, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().toggleable(checked, role = androidx.compose.ui.semantics.Role.Switch, onValueChange = onChange),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
        Text(text, Modifier.weight(1f), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
        JSwitch(checked, null)
    }
}
