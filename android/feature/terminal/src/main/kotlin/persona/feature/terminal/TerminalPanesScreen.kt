package persona.feature.terminal

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import persona.core.contracts.AgentState
import persona.core.contracts.api.isPaneAddress
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.designsystem.icons.PersonaIcons
import persona.core.navigation.Navigator
import persona.core.navigation.TerminalRoute

/**
 * "Terminal" (docs/integrations/terminal.md "Standalone terminal"): machine -> workspace -> tab -> pane, agents and plain shells, each opened in the
 * terminal page; "New tab" opens a shell tab in a workspace. A pane with an agent opens by its session (the stream its
 * chat's terminal uses), the others by their address.
 */
@Composable
fun TerminalPanesScreen(model: TerminalPanesViewModel, navigator: Navigator) {
    val state by model.state.collectAsStateWithLifecycle()
    val confirmation by model.confirmation.collectAsStateWithLifecycle()
    val closing by model.closing.collectAsStateWithLifecycle()
    val closingMachine by model.closingMachine.collectAsStateWithLifecycle()
    var closePane by remember { mutableStateOf<TermPane?>(null) }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(model, lifecycle) { lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) { model.poll() } }
    val feedback = LocalAppFeedback.current
    val resources = LocalResources.current
    LaunchedEffect(model, feedback, resources, navigator) {
        model.events.collect { event ->
            when (event) {
                is TerminalPanesEvent.Open -> navigator.navigate(TerminalRoute(event.machineId, event.terminal,
                    event.title.ifEmpty { resources.getString(R.string.terminal_panes_untitled) }))
                is TerminalPanesEvent.NewTabFailed -> feedback?.notice(resources.getString(R.string.terminal_panes_new_tab_failed, event.error))
                is TerminalPanesEvent.CloseFailed -> feedback?.notice(resources.getString(R.string.terminal_panes_close_failed, event.error))
                is TerminalPanesEvent.Closed -> feedback?.notice(resources.getQuantityString(R.plurals.terminal_panes_closed, event.count, event.count) +
                    if (event.kept == 0) "" else " " + resources.getQuantityString(R.plurals.terminal_panes_kept, event.kept, event.kept))
                TerminalPanesEvent.NoFinished -> feedback?.notice(resources.getString(R.string.terminal_panes_no_finished))
            }
        }
    }
    val untitled = stringResource(R.string.terminal_panes_untitled)
    JListPage(
        title = stringResource(R.string.terminal_panes_title), summary = stringResource(R.string.terminal_panes_summary),
        onBack = navigator::back, refreshing = state.busy, onRefresh = model::refresh,
        actions = { JIconButton(PhIcons.ArrowClockwise, stringResource(R.string.terminal_panes_refresh), model::refresh, enabled = !state.busy) },
    ) {
        if (state.machines.isEmpty()) item("empty") {
            JEmptyState(stringResource(R.string.terminal_panes_no_computers), stringResource(R.string.terminal_panes_no_computers_detail),
                icon = PhIcons.TerminalWindow)
        }
        for (entry in state.machines) {
            val machine = entry.machine
            item("machine-${machine.id}", contentType = "machine") {
                Column {
                    JSectionHeader(machine.displayName, trailing = if (machine.online) null else stringResource(R.string.terminal_panes_offline))
                    if (machine.online && hasFinished(entry.listing?.response)) Row(verticalAlignment = Alignment.CenterVertically) {
                        JTextButton(stringResource(R.string.terminal_panes_close_finished),
                            { model.previewFinished(machine.id) }, enabled = !closing)
                        if (closingMachine == machine.id) JSpinner(Modifier.size(Tokens.Space.Standard.l))
                    }
                }
            }
            if (!machine.online) continue
            val listing = entry.listing
            when {
                listing == null -> item("loading-${machine.id}", contentType = "note") { Note(stringResource(R.string.terminal_panes_loading)) }
                listing.error != null && listing.response == null -> item("error-${machine.id}", contentType = "error") {
                    JNotice(listing.error, status = Status.NeedsYou, actionLabel = stringResource(R.string.terminal_retry), onAction = model::refresh)
                }
                entry.spaces.isEmpty() -> item("none-${machine.id}", contentType = "note") {
                    val kind = if (model.opensNewSpaces(machine.id)) homeShellHost(listing.response) else null
                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                        Note(stringResource(if (kind == null) R.string.terminal_panes_no_panes_agent else R.string.terminal_panes_no_panes),
                            Modifier.weight(1f))
                        if (kind != null) OpenButton(stringResource(R.string.terminal_panes_new_terminal), opening = state.opening != null,
                            thisOne = state.opening == homeOpeningKey(machine.id, kind)) { model.newTerminal(machine.id, kind) }
                    }
                }
            }
            for (space in entry.spaces) {
                item(spaceItemKey(machine.id, space), contentType = "space") {
                    SpaceHeader(space, opening = state.opening != null, thisOne = state.opening == openingKey(machine.id, space)) {
                        model.newTab(machine.id, space)
                    }
                }
                for (tab in space.tabs) {
                    // A tab named like its workspace, the only one there, needs no heading of its own.
                    if (space.tabs.size > 1 || tab.label != space.label) item(tabItemKey(machine.id, space, tab), contentType = "tab") {
                        Note(stringResource(R.string.terminal_panes_tab, tab.label))
                    }
                    tab.panes.forEachIndexed { index, pane ->
                        item(paneItemKey(machine.id, space, pane), contentType = "pane") {
                            JGroupItem(GroupPosition.of(index, tab.panes.size)) {
                                PaneRow(pane, untitled, enabled = !closing, onClose = if (pane.canClose) ({ closePane = pane }) else null) {
                                    // A session's terminal is titled by the session; a pane by its place.
                                    navigator.navigate(TerminalRoute(pane.key.machineId, pane.key.sessionId,
                                        if (pane.key.isPaneAddress) paneTrail(space, tab, pane, untitled) else ""))
                                }
                            }
                        }
                    }
                }
            }
        }
    }
    closePane?.let { pane ->
        JDialog(onDismiss = { closePane = null }, title = stringResource(R.string.terminal_panes_close_title, paneLabel(pane, untitled)),
            text = stringResource(R.string.terminal_panes_close_detail), destructive = true,
            confirm = stringResource(R.string.terminal_close) to { closePane = null; model.close(pane.key.machineId, pane.pane) })
    }
    confirmation?.let { confirmed ->
        JDialog(onDismiss = model::dismissFinished,
            title = pluralStringResource(R.plurals.terminal_panes_finish_title, confirmed.preview.closed.size, confirmed.preview.closed.size), destructive = true,
            confirmEnabled = !closing,
            confirm = stringResource(R.string.terminal_panes_confirm_close) to model::confirmFinished,
            content = { FinishedPreview(confirmed, untitled) })
    }
}

@Composable
internal fun FinishedPreview(confirmed: FinishedConfirmation, untitled: String) {
    Column(Modifier.verticalScroll(androidx.compose.foundation.rememberScrollState()),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Text(stringResource(R.string.terminal_panes_finish_detail), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
        confirmed.rows.forEach { row ->
            Column {
                Text(paneLabel(row, untitled), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
                Text(paneSubtitle(row), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
        }
        if (confirmed.preview.kept.isNotEmpty()) Text(pluralStringResource(R.plurals.terminal_panes_kept, confirmed.preview.kept.size, confirmed.preview.kept.size),
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
    }
}

@Composable
private fun SpaceHeader(space: TermSpace, opening: Boolean, thisOne: Boolean, onNewTab: () -> Unit) {
    Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.m, bottom = Tokens.Space.xs, start = Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(space.label, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (space.branch.isNotEmpty()) Text(space.branch, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1,
                overflow = TextOverflow.Ellipsis)
        }
        if (space.canOpen) OpenButton(stringResource(R.string.terminal_panes_new_tab), opening, thisOne, onNewTab)
    }
}

@Composable
private fun OpenButton(label: String, opening: Boolean, thisOne: Boolean, onClick: () -> Unit) {
    if (thisOne) JSpinner(Modifier.size(Tokens.Space.Standard.l))
    else JTextButton(label, onClick, enabled = !opening, icon = PhIcons.Plus)
}

@Composable
private fun PaneRow(pane: TermPane, untitled: String, enabled: Boolean, onClose: (() -> Unit)?, onOpen: () -> Unit) {
    val status = pane.state?.let(::statusOf)
    val subtitle = paneSubtitle(pane)
    JRow(paneLabel(pane, untitled), subtitle = subtitle.ifEmpty { null }, onClick = onOpen,
        trailing = onClose?.let { close -> { JIconButton(PersonaIcons.close, stringResource(R.string.terminal_close), close, enabled = enabled) } },
        leading = {
            if (status != null) JStatusDot(status)
            else JIcon(PhIcons.TerminalWindow, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink2, decorative = true)
        })
}

@Composable
private fun paneLabel(pane: TermPane, untitled: String): String =
    if (pane.finished) stringResource(R.string.terminal_panes_finished, pane.title.ifEmpty { untitled }) else pane.title.ifEmpty { untitled }

@Composable
private fun paneSubtitle(pane: TermPane): String = listOfNotNull(
    if (pane.finished) stringResource(R.string.terminal_panes_untitled) else pane.state?.let(::statusOf)?.localizedLabel,
    pane.detail.ifEmpty { null },
).joinToString(" · ")

@Composable
private fun Note(text: String, modifier: Modifier = Modifier) {
    Text(text, modifier.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.s), style = PersonaTheme.type.caption,
        color = PersonaTheme.palette.ink3)
}

private fun statusOf(state: AgentState) = when (state) {
    AgentState.NeedsYou -> Status.NeedsYou; AgentState.Working -> Status.Working
    AgentState.Unread -> Status.Unread; AgentState.Idle -> Status.Idle
    AgentState.Offline -> Status.Offline; AgentState.Unknown -> Status.Unknown
}
