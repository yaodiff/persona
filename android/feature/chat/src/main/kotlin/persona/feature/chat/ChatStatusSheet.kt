package persona.feature.chat

import androidx.compose.ui.platform.LocalResources
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import android.content.res.Resources
import androidx.compose.ui.res.stringResource
import persona.core.contracts.BackgroundTask
import persona.core.contracts.LoopInfo
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.resultUnknown
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.sort.resumeCommand
import persona.core.domain.sort.canSend

@Composable
internal fun ChatStatusSheet(
    state: StatusLineState, session: Session?, messages: List<OutboxMessage>, busyIds: Set<String>,
    onAction: ((OutboxMessage, DeliveryAction) -> Unit)?, onDismiss: () -> Unit,
    replacement: Session?, onOpenReplacement: (() -> Unit)?,
    onConnectionDetails: (() -> Unit)?, terminal: (() -> Unit)?,
    recordedDuration: Long?, notice: String?,
    recovery: ChatStatusRecovery, machine: Machine?, draftSaveError: String?, readOnly: Boolean,
    onOpenSession: ((SessionKey) -> Unit)?,
) {
    val copy = rememberCopy()
    val res = LocalResources.current
    val workflowFolds = remember { WorkflowFolds() }
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.chat_status_title)) {
        LazyColumn(contentPadding = PaddingValues(bottom = Tokens.Space.m),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            item {
                StatusLine(state)
                recordedDuration?.takeIf { it >= 0 }?.let {
                    Text(stringResource(R.string.chat_status_turn_duration, it / 60, it % 60), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                }
                if (notice != null) Text(notice, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                recovery.notice?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
            }
            if (draftSaveError != null) item {
                JGroupItem(GroupPosition.Single) {
                    Column(Modifier.fillMaxWidth().padding(Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        JRow(stringResource(R.string.chat_status_draft_failed), leadingIcon = PhIcons.WarningCircle)
                        Text(draftSaveError.ifBlank { stringResource(R.string.chat_status_draft_failed_default) }, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
                        Text(stringResource(R.string.chat_status_draft_failed_hint), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                    }
                }
            }
            if (state.ended || state.replaced || state.machineOffline) item {
                JGroupItem(GroupPosition.Single) {
                    Column(Modifier.fillMaxWidth().padding(Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        val updated = if (state.machineOffline) machine?.lastSeen else session?.updatedAt
                        Text(updated?.takeIf(String::isNotBlank)?.let { stringResource(R.string.chat_status_last_updated, it) } ?: stringResource(R.string.chat_status_no_last_updated),
                            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                        if (replacement != null && onOpenReplacement != null)
                            JButton(stringResource(R.string.chat_status_open_new), onOpenReplacement, enabled = !recovery.busy)
                        if ((state.ended || state.replaced) && session != null) {
                            if (!readOnly && !session.assistant) {
                                val restored = recovery.restored
                                if (restored != null && onOpenSession != null)
                                    JButton(stringResource(R.string.chat_status_open_restored), { onOpenSession(restored) })
                                else if (restored == null) JButton(
                                    stringResource(if (recovery.resumeUnknown) R.string.chat_status_check_resume else R.string.chat_status_resume),
                                    { recovery.resume(session) },
                                    enabled = recovery.available && machine?.online == true &&
                                        (recovery.resumeUnknown || (session.cwd.isNotBlank() && !session.launcher.isNullOrBlank())),
                                    loading = recovery.busy)
                                if (!recovery.available) Text(stringResource(R.string.chat_status_resume_unavailable), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                                else if (session.launcher.isNullOrBlank() || session.cwd.isBlank())
                                    Text(stringResource(R.string.chat_status_resume_missing), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                            }
                            // Only a session in a terminal resumes there (an agent runtime's in its own UI).
                            session.resumeCommand()?.let { command ->
                                StatusSecondaryButton(stringResource(R.string.chat_status_copy_resume), { copy(command, null) })
                            }
                        }
                        if (state.machineOffline) {
                            Text(stringResource(R.string.chat_status_offline_hint), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                            if (!readOnly) StatusSecondaryButton(
                                stringResource(if (recovery.reconnectRequested) R.string.chat_status_refresh_machine else R.string.chat_status_reconnect),
                                { recovery.reconnect(session?.machineId ?: machine!!.id) },
                                enabled = recovery.available && (session != null || machine != null), loading = recovery.busy)
                            if (!readOnly && !recovery.available) Text(stringResource(R.string.chat_status_reconnect_unavailable), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                            if (onConnectionDetails != null) StatusSecondaryButton(stringResource(R.string.chat_status_connection_details), onConnectionDetails)
                        }
                    }
                }
            }
            items(messages, key = { it.clientMessageId }) { message ->
                JGroupItem(GroupPosition.Single) {
                    Column(Modifier.fillMaxWidth().padding(Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        Text(composerMessagePreview(message.text), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
                        OutboxStatus(message, message.clientMessageId in busyIds,
                            onAction = onAction?.let { action -> { choice -> action(message, choice) } }, canSend = session?.canSend != false)
                        if (message.state.resultUnknown) {
                            Text(stringResource(when (message.state) {
                                DeliveryState.Verifying -> R.string.chat_status_verifying
                                DeliveryState.NeedsConfirmation -> R.string.chat_status_needs_confirmation
                                else -> R.string.chat_status_unconfirmed
                            }), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                            Text(stringResource(R.string.chat_status_dont_resend), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                            if (onAction != null) {
                                StatusSecondaryButton(stringResource(R.string.chat_status_confirm_delivered), { onAction(message, DeliveryAction.ConfirmDelivered) },
                                    Modifier.fillMaxWidth(), enabled = message.clientMessageId !in busyIds)
                                StatusSecondaryButton(stringResource(R.string.chat_status_confirm_not_delivered), { onAction(message, DeliveryAction.ConfirmNotDelivered) },
                                    Modifier.fillMaxWidth(), enabled = message.clientMessageId !in busyIds)
                            }
                        }
                    }
                }
            }
            val queued = session?.queued.orEmpty()
            if (queued.isNotEmpty()) item { JSectionHeader(stringResource(R.string.chat_status_queued), count = queued.size) }
            itemsIndexed(queued) { index, message ->
                JGroupItem(GroupPosition.of(index, queued.size)) {
                    JRow(message, leadingIcon = PhIcons.Clock, subtitle = stringResource(R.string.chat_status_waiting))
                }
            }
            session?.loop?.let { loop -> item {
                JGroupItem(GroupPosition.Single) {
                    JRow(stringResource(R.string.chat_status_loop_title), subtitle = loopSummary(res, loop, separator = stringResource(R.string.chat_status_clause_separator)), leadingIcon = PhIcons.ArrowsClockwise)
                    // In full: this is the only place that shows what the loop does, and the sheet scrolls (#2708).
                    loop.prompt?.takeIf { it.isNotBlank() }?.let {
                        Text(it, Modifier.padding(Tokens.Space.m), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
                    }
                }
            } }
            // Its Workflow runs (#2935, #3435), one timeline card each, newest first; a running one is not listed again under
            // background. Tapping an agent opens its transcript by its session id, as a subagent's.
            val workflows = session?.workflows.orEmpty()
            if (workflows.isNotEmpty()) item { JSectionHeader(stringResource(R.string.chat_status_workflows), count = workflows.size) }
            items(workflows, key = { "workflow:" + (it.runId ?: it.taskId) }) { workflow ->
                JWorkflowCard(workflow, folds = workflowFolds,
                    onOpenAgent = if (onOpenSession != null && session != null) { agent -> onOpenSession(SessionKey(session.machineId, agent.sessionId)) } else null)
            }
            val tasks = session?.backgroundTasks.orEmpty()
            if (tasks.isNotEmpty()) item { JSectionHeader(stringResource(R.string.chat_status_background), count = tasks.size) }
            itemsIndexed(tasks) { index, task ->
                JGroupItem(GroupPosition.of(index, tasks.size)) {
                    JRow(backgroundTitle(task, stringResource(R.string.chat_status_background_untitled)), leadingIcon = PhIcons.ListChecks)
                }
            }
            item {
                if (terminal != null) StatusSecondaryButton(stringResource(R.string.chat_status_open_terminal), terminal)
                StatusSecondaryButton(stringResource(R.string.chat_status_close), onDismiss)
            }
        }
    }
}

/**
 * "Loop · every 30 min · next 14:05": the cadence the hub reports, in the phone's time zone, its parts joined by
 * [separator] (the language's own " · " when not given).
 */
internal fun loopSummary(
    res: Resources, loop: LoopInfo, zone: java.time.ZoneId = java.time.ZoneId.systemDefault(),
    separator: String = res.getString(R.string.chat_status_loop_separator),
): String {
    val every = loop.intervalSeconds?.takeIf { it > 0 }?.let { seconds ->
        when {
            seconds % 3600 == 0 -> res.getQuantityString(R.plurals.chat_status_every_hours, seconds / 3600, seconds / 3600)
            seconds % 60 == 0 -> res.getQuantityString(R.plurals.chat_status_every_minutes, seconds / 60, seconds / 60)
            else -> res.getQuantityString(R.plurals.chat_status_every_seconds, seconds, seconds)
        }
    } ?: loop.cron?.takeIf { it.isNotBlank() }?.let { res.getString(R.string.chat_status_schedule, it) }
    val next = loop.nextAt?.let { runCatching { java.time.Instant.parse(it) }.getOrNull() }
        ?.let { res.getString(R.string.chat_status_next, LocalizedFormats.format(it, LocalizedFormats.TIME, res.configuration.locales[0], zone)) }
    return listOfNotNull(res.getString(R.string.chat_status_loop), every, next).joinToString(separator)
}

/** A background task by what it is for; the command's first line when it has no description. */
internal fun backgroundTitle(task: BackgroundTask, untitled: String): String =
    task.description?.takeIf { it.isNotBlank() }
        ?: task.command?.lineSequence()?.firstOrNull { it.isNotBlank() }?.trim()
        ?: task.id.ifBlank { untitled }

@Composable
private fun StatusSecondaryButton(
    text: String, onClick: () -> Unit, modifier: Modifier = Modifier,
    enabled: Boolean = true, loading: Boolean = false,
) = JButton(text, onClick, modifier, kind = JButtonKind.Secondary, enabled = enabled, loading = loading)
