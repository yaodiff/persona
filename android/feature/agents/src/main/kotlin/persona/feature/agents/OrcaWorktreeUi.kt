package persona.feature.agents

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.Machine
import persona.core.contracts.api.AgentCreationService
import persona.core.contracts.api.AgentWorkspace
import persona.core.contracts.api.WorktreeComposerOptions
import persona.core.contracts.api.WorktreeManagement
import persona.core.contracts.api.WorktreePrepare
import persona.core.designsystem.JAction
import persona.core.designsystem.JActionSheet
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JCheckbox
import persona.core.designsystem.JDialog
import persona.core.designsystem.JIcon
import persona.core.designsystem.JRow
import persona.core.designsystem.JSegment
import persona.core.designsystem.JSegmented
import persona.core.designsystem.JSheet
import persona.core.designsystem.JSpinner
import persona.core.designsystem.JTextButton
import persona.core.designsystem.JTextField
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.UiText
import persona.core.designsystem.rememberCopy
import persona.core.designsystem.text
import persona.core.domain.engines.Engines

/** Which worktree dialog is open, and for which worktree. */
internal sealed interface WorktreeDialog {
    val target: WorktreeTarget
    data class Create(override val target: WorktreeTarget) : WorktreeDialog
    data class Rename(override val target: WorktreeTarget) : WorktreeDialog
    data class Delete(override val target: WorktreeTarget) : WorktreeDialog
}

/**
 * Orca's worktree actions on one screen (#17054): the menus' items, gated like the web's `resolveWorktreeAction` against
 * the catalog's Orca rows ([rows]) and the computers ([machines]), and the dialog they open ([dialog], drawn by
 * [OrcaWorktreeDialogs]). Null where the pairing has no worktree calls.
 */
@Stable
internal class OrcaWorktreeActions(val api: WorktreeManagement, val registry: WorktreeActionsRegistry) {
    var machines: List<Machine> by mutableStateOf(emptyList())
    var rows: List<AgentWorkspace> by mutableStateOf(emptyList())
    var access: Map<String, WorktreeAccess> by mutableStateOf(emptyMap())
    var dispatched: Set<String> by mutableStateOf(emptySet())
    var dialog: WorktreeDialog? by mutableStateOf(null)
    /** Called when a worktree was created, renamed or removed: the catalog is read again. */
    var changed: () -> Unit = {}
    /** Says what finished ("Worktree renamed"). */
    var notify: (UiText) -> Unit = {}

    fun gate(target: WorktreeTarget): WorktreeGate =
        WorktreeGates.resolve(target, rows, machines, access[target.machineId] ?: WorktreeAccess.Checking)

    /** The worktree [sessions] all run in, when the catalog lists it. */
    fun targetOf(sessions: List<persona.core.contracts.Session>): WorktreeTarget? = WorktreeGates.targetOf(sessions, rows)

    fun mutation(target: WorktreeTarget): WorktreeMutation = registry.mutation(api, target) { gate(target) }.also { mutation ->
        mutation.done = { done ->
            changed()
            notify(UiText.Res(if (done == WorktreeDone.Renamed) R.string.agents_worktree_renamed else R.string.agents_worktree_removed))
            if (dialog?.target == target) dialog = null
        }
    }

    fun creation(target: WorktreeTarget): WorktreeCreation = registry.creation(api, target, WorktreeParent.Source).also {
        it.created = { changed() }
    }
}

/** The screen's [OrcaWorktreeActions], kept current with [machines] and [rows]; null without worktree calls. */
@Composable
internal fun rememberOrcaWorktreeActions(service: AgentCreationService?, machines: List<Machine>, rows: List<AgentWorkspace>,
                                         onChanged: () -> Unit): OrcaWorktreeActions? {
    if (service == null) return null
    val api = service.worktrees ?: return null
    val registry = remember(service.scope) { WorktreeActionsRegistry.of(service.scope) }
    val access by registry.access.collectAsState()
    val dispatched by registry.dispatched.collectAsState()
    val feedback = LocalAppFeedback.current
    val resources = agentResources()
    val changed by rememberUpdatedState(onChanged)
    fun OrcaWorktreeActions.follow() {
        this.machines = machines
        this.rows = rows
        this.access = access
        this.dispatched = dispatched
        this.changed = { changed() }
        this.notify = { text -> feedback?.notice(text.resolve(resources)) }
    }
    val actions = remember(api, registry) { OrcaWorktreeActions(api, registry).apply { follow() } }
    SideEffect { actions.follow() }
    return actions
}

/**
 * A worktree's menu items (the web's `WorktreeActionsMenu`): New child worktree…, Rename worktree…, its name and folder to
 * copy, and Delete worktree… (Check status once a deletion was sent and not confirmed). Items that cannot run now stay,
 * disabled; [note] says why. [section] titles them when they follow other actions.
 */
@Composable
internal fun OrcaWorktreeActions.menuActions(target: WorktreeTarget, section: Boolean = false): List<JAction> {
    LaunchedEffect(api, target.machineId) { registry.ensureAccess(api, target.machineId) }
    val gate = gate(target)
    val dispatched = target.key in this.dispatched
    val copy = rememberCopy()
    val name = gate.workspace?.label ?: target.label
    return buildList {
        add(JAction(stringResource(R.string.agents_worktree_new_child), PersonaIcons.gitBranch, enabled = gate.canCreate,
            section = if (section) stringResource(R.string.agents_worktree_section) else null) { dialog = WorktreeDialog.Create(target) })
        if (!dispatched) add(JAction(stringResource(R.string.agents_worktree_rename), PersonaIcons.compose, enabled = gate.canRename) {
            dialog = WorktreeDialog.Rename(target)
        })
        add(JAction(stringResource(R.string.agents_worktree_copy_name), PersonaIcons.copy, enabled = gate.workspace != null) { copy(name, null) })
        add(JAction(stringResource(R.string.agents_worktree_copy_path), PersonaIcons.copy, enabled = gate.workspace != null && target.cwd.isNotEmpty()) {
            copy(target.cwd, null)
        })
        add(JAction(stringResource(if (dispatched) R.string.agents_worktree_check_status else R.string.agents_worktree_delete), PersonaIcons.delete,
            destructive = true, enabled = if (dispatched) gate.canReconcile else gate.canDelete, separatorBefore = true) {
            dialog = WorktreeDialog.Delete(target)
        })
    }
}

/**
 * Why [menuActions]' disabled items are off, for the sheet's note: the device's access, the worktree or Orca unavailable,
 * or (Delete) the main checkout or a parent. Null when everything can run.
 */
@Composable
internal fun OrcaWorktreeActions.note(target: WorktreeTarget): String? {
    val gate = gate(target)
    val id = gate.reason ?: gate.protection.takeIf { target.key !in dispatched }
    return id?.let { stringResource(it) }
}

/** A space's New worktree… (the web's space menu): from the Orca worktree its sessions run in, when it can make one now. */
@Composable
internal fun OrcaWorktreeActions.newWorktreeAction(target: WorktreeTarget): JAction? {
    LaunchedEffect(api, target.machineId) { registry.ensureAccess(api, target.machineId) }
    if (!gate(target).canCreate) return null
    return JAction(stringResource(R.string.agents_worktree_new), PersonaIcons.gitBranch, separatorBefore = true) {
        dialog = WorktreeDialog.Create(target)
    }
}

/** The open worktree dialog, if any. */
@Composable
internal fun OrcaWorktreeDialogs(actions: OrcaWorktreeActions?) {
    val dialog = actions?.dialog ?: return
    val close = { actions.dialog = null }
    when (dialog) {
        is WorktreeDialog.Create -> NewWorktreeSheet(actions.creation(dialog.target), actions.gate(dialog.target), actions.rows, close)
        is WorktreeDialog.Rename -> {
            val mutation = remember(dialog) { actions.mutation(dialog.target) }
            DisposableEffect(mutation) { onDispose { actions.registry.release(mutation) } }
            RenameWorktreeDialog(mutation, actions.gate(dialog.target).workspace?.label ?: dialog.target.label, close)
        }
        is WorktreeDialog.Delete -> {
            val mutation = remember(dialog) { actions.mutation(dialog.target) }
            DisposableEffect(mutation) { onDispose { actions.registry.release(mutation) } }
            val gate = actions.gate(dialog.target)
            DeleteWorktreeDialog(mutation, gate.workspace?.label ?: dialog.target.label, gate.machine?.displayName, close)
        }
    }
}

/** Renames the worktree's display name in Orca (`worktree/rename`); its branch, folder and Agents keep theirs. */
@Composable
internal fun RenameWorktreeDialog(mutation: WorktreeMutation, name: String, onDismiss: () -> Unit) {
    val state by mutation.state.collectAsState()
    var typed by rememberSaveable(mutation.target.key) { mutableStateOf(name) }
    val busy = state.phase == WorktreePhase.Renaming
    val palette = PersonaTheme.palette
    JDialog(onDismiss = { if (!busy) onDismiss() }, title = stringResource(R.string.agents_worktree_rename_title),
        confirm = stringResource(R.string.agents_dialog_save) to { mutation.rename(typed) },
        confirmEnabled = typed.isNotBlank() && state.phase != WorktreePhase.Invalid, confirmLoading = busy) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(mutation.target.cwd, style = PersonaTheme.type.codeSmall, color = palette.ink2)
            JTextField(typed, { typed = it }, Modifier.fillMaxWidth().testTag("worktree-name"), singleLine = true,
                label = stringResource(R.string.agents_worktree_name), enabled = !busy && state.phase != WorktreePhase.Invalid,
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done), keyboardActions = KeyboardActions(onDone = { mutation.rename(typed) }))
            Text(stringResource(R.string.agents_worktree_rename_hint), style = PersonaTheme.type.caption, color = palette.ink2)
            state.error?.let { Text(it.text(), style = PersonaTheme.type.caption, color = palette.danger.text) }
        }
    }
}

/**
 * Deletes a worktree after its preview (the web's `DeleteWorktreeDialog`): what it is and where, that its Agents and shells
 * stop and its files go, the uncommitted changes to discard on purpose, and whether its branch goes too. Once sent, the
 * only way on is Check status: the deletion is never sent again from here.
 */
@Composable
internal fun DeleteWorktreeDialog(mutation: WorktreeMutation, name: String, machineName: String?, onDismiss: () -> Unit) {
    val state by mutation.state.collectAsState()
    var discard by rememberSaveable(mutation.target.key) { mutableStateOf(false) }
    var deleteBranch by rememberSaveable(mutation.target.key) { mutableStateOf(false) }
    LaunchedEffect(mutation) { if (!state.dispatched && state.phase == WorktreePhase.Idle && state.preview == null) mutation.preview() }
    val info = state.preview
    val folder = info?.folder == true
    val dirty = info?.dirty ?: 0
    val invalid = state.phase == WorktreePhase.Invalid
    val canRemove = state.phase == WorktreePhase.Ready && !state.dispatched && info != null && (folder || dirty == 0 || discard)
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    JDialog(
        onDismiss = { if (!state.busy) onDismiss() },
        title = stringResource(if (folder) R.string.agents_worktree_unregister else R.string.agents_worktree_delete_title),
        confirm = if (state.dispatched) stringResource(R.string.agents_worktree_check_status) to { mutation.reconcile() }
            else stringResource(if (folder) R.string.agents_worktree_unregister_confirm else R.string.agents_worktree_delete_confirm) to {
                mutation.remove(discard, deleteBranch)
            },
        dismiss = stringResource(if (state.dispatched) R.string.agents_close else persona.core.designsystem.R.string.designsystem_cancel) to onDismiss,
        destructive = !state.dispatched,
        confirmEnabled = if (state.dispatched) !invalid && state.phase == WorktreePhase.Uncertain else canRemove,
        confirmLoading = state.busy,
    ) {
        Column(Modifier.testTag("worktree-delete"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Column(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                Text(name, style = type.bodyStrong, color = palette.ink)
                Text(listOf(machineName ?: stringResource(R.string.agents_worktree_computer), Engines.displayName(ORCA_KIND))
                    .joinToString(" · "), style = type.caption, color = palette.ink2)
                Text(info?.path ?: mutation.target.cwd, style = type.codeSmall, color = palette.ink2)
            }
            if (state.busy) Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                JSpinner()
                Text(stringResource(when (state.phase) {
                    WorktreePhase.Checking -> R.string.agents_worktree_checking
                    WorktreePhase.Deleting -> R.string.agents_worktree_removing
                    else -> R.string.agents_worktree_previewing
                }), style = type.callout, color = palette.ink2)
            }
            if (info != null) {
                Text(stringResource(R.string.agents_worktree_sessions_end), style = type.callout, color = palette.ink)
                Text(stringResource(if (folder) R.string.agents_worktree_folder_kept else R.string.agents_worktree_files_deleted),
                    style = type.callout, color = palette.ink)
                if (!folder) {
                    if (dirty > 0) {
                        Text(pluralStringResource(R.plurals.agents_worktree_dirty, dirty, dirty), style = type.callout, color = palette.danger.text)
                        WorktreeChoice(stringResource(R.string.agents_worktree_discard), discard, enabled = !state.busy && !state.dispatched && !invalid,
                            tag = "worktree-discard") { discard = it }
                    }
                    info.branch?.takeIf(String::isNotEmpty)?.let { branch ->
                        Text(branch, style = type.codeSmall, color = palette.ink2)
                        WorktreeChoice(stringResource(R.string.agents_worktree_delete_branch), deleteBranch,
                            enabled = !state.busy && !state.dispatched && !invalid) { deleteBranch = it }
                    }
                    Text(stringResource(R.string.agents_worktree_keep_branch), style = type.caption, color = palette.ink2)
                }
            }
            state.error?.let { Text(it.text(), style = type.caption, color = palette.danger.text) }
            if (info == null && !state.busy && !invalid && !state.dispatched) {
                JTextButton(stringResource(R.string.agents_worktree_retry_preview), { mutation.preview() }, icon = PersonaIcons.retry)
            }
        }
    }
}

/** A choice whose words wrap (a dialog is narrow), ticked on purpose. */
@Composable
private fun WorktreeChoice(text: String, checked: Boolean, enabled: Boolean = true, tag: String? = null, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().toggleable(checked, enabled = enabled, role = Role.Checkbox, onValueChange = onChange)
        .then(if (tag != null) Modifier.testTag(tag) else Modifier),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
        JCheckbox(checked, null, enabled = enabled)
        Text(text, Modifier.weight(1f), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
    }
}

/** A row that opens or folds a group of fields under it. */
@Composable
private fun Disclosure(title: String, open: Boolean, onToggle: () -> Unit) {
    JRow(title, onClick = onToggle, trailing = {
        JIcon(PersonaIcons.disclosure, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink3,
            modifier = Modifier.rotate(if (open) 90f else 0f), decorative = true)
    })
}

/** What a picker row opens: the options with the chosen one ticked. */
private data class WorktreePick(val title: String, val actions: List<JAction>)

/**
 * New worktree (the web's `NewWorktree`), from [creation]'s source worktree: Orca's composer where the computer has it
 * (name, base branch, a GitHub issue or pull request, and under Advanced the branch, parent, note and sparse checkout), the
 * basic branch-and-path form otherwise. While a create is pending or uncertain the form is locked and the button checks
 * that very create; closing the sheet keeps it.
 */
@Composable
internal fun NewWorktreeSheet(creation: WorktreeCreation, gate: WorktreeGate, rows: List<AgentWorkspace>, onDismiss: () -> Unit) {
    val state by creation.state.collectAsState()
    LaunchedEffect(creation) { creation.load() }
    var pick by remember { mutableStateOf<WorktreePick?>(null) }
    val intent = state.intent
    val created = intent?.created
    val offline = gate.machine?.online == false
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    JSheet(onDismiss = { if (!state.sending) onDismiss() }, title = stringResource(
        if (created != null) R.string.agents_worktree_created else R.string.agents_worktree_new_title), scrollable = true) {
        Column(Modifier.fillMaxWidth().testTag("new-worktree"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                Text(gate.workspace?.label ?: creation.target.label, style = type.bodyStrong, color = palette.ink, maxLines = 1,
                    overflow = TextOverflow.Ellipsis)
                Text(creation.target.cwd, style = type.codeSmall, color = palette.ink2)
            }
            if (created != null) {
                Text(created.path, style = type.codeSmall, color = palette.ink)
                if (created.branch.isNotEmpty()) Text(created.branch, style = type.codeSmall, color = palette.ink2)
                Text(stringResource(R.string.agents_worktree_created_help), style = type.callout, color = palette.ink2)
                JTextButton(stringResource(R.string.agents_worktree_create_another), creation::fresh)
            } else when (val read = state.read) {
                WorktreeRead.Reading -> Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                    JSpinner()
                    Text(stringResource(R.string.agents_worktree_reading), style = type.callout, color = palette.ink2)
                }
                is WorktreeRead.Failed -> {
                    Text(read.text.text(), style = type.callout, color = palette.danger.text)
                    if (!state.locked) Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        JTextButton(stringResource(R.string.agents_worktree_retry), { creation.load(force = true) }, icon = PersonaIcons.retry)
                        if (read.basicAvailable) JTextButton(stringResource(R.string.agents_worktree_use_basic), creation::useBasic)
                    }
                }
                is WorktreeRead.Ready -> {
                    val composer = read.info.composer?.takeIf { !state.draft.basic }
                    if (composer != null) ComposerFields(creation, state, composer, rows) { pick = it }
                    else BasicFields(creation, state, read.info, rows) { pick = it }
                }
            }
            if (offline && created == null) Text(stringResource(R.string.agents_worktree_offline), style = type.caption, color = palette.danger.text)
            if (state.locked && created == null) Text(stringResource(
                if (intent?.phase == CreationPhase.Pending || state.sending) R.string.agents_worktree_pending else R.string.agents_worktree_create_unknown),
                style = type.caption, color = palette.ink2)
            (state.error ?: intent?.error)?.let { Text(it.text(), style = type.caption, color = palette.danger.text) }
            Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.s), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JButton(stringResource(if (created != null) R.string.agents_worktree_done else persona.core.designsystem.R.string.designsystem_cancel),
                    onDismiss, Modifier.weight(1f), kind = JButtonKind.Secondary, size = JButtonSize.Medium, enabled = !state.sending)
                if (created == null) JButton(stringResource(if (state.locked) R.string.agents_worktree_check_result else R.string.agents_worktree_create),
                    creation::submit, Modifier.weight(1f).testTag("worktree-create"), size = JButtonSize.Medium, loading = state.sending,
                    enabled = !offline && (state.info != null || intent != null) && !(state.read is WorktreeRead.Failed && intent == null))
            }
        }
    }
    pick?.let { shown -> JActionSheet(shown.actions.map { action -> action.copy(onClick = { action.onClick(); pick = null }) }, { pick = null }, title = shown.title) }
}

/** Orca's composer fields. */
@Composable
private fun ComposerFields(creation: WorktreeCreation, state: CreationState, composer: WorktreeComposerOptions, rows: List<AgentWorkspace>,
                           onPick: (WorktreePick) -> Unit) {
    val draft = state.draft
    val enabled = !state.locked
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    var github by rememberSaveable { mutableStateOf(false) }
    var advanced by rememberSaveable { mutableStateOf(false) }
    var newPreset by rememberSaveable { mutableStateOf(false) }
    var presetName by rememberSaveable { mutableStateOf("") }
    var directories by rememberSaveable { mutableStateOf("") }
    val base = draft.baseRef.ifEmpty { composer.baseRef }
    val baseTitle = stringResource(R.string.agents_worktree_base)
    JRow(baseTitle, value = base, enabled = enabled, onClick = {
        onPick(WorktreePick(baseTitle, (listOf(composer.baseRef) + composer.refs + listOfNotNull(draft.baseRef.ifEmpty { null }))
            .filter(String::isNotEmpty).distinct().map { ref -> JAction(ref, checked = ref == base) { creation.update { it.copy(baseRef = ref) } } }))
    })
    JTextField(draft.name, { name -> creation.update { it.copy(name = name) } }, Modifier.fillMaxWidth(), singleLine = true, enabled = enabled,
        label = stringResource(R.string.agents_worktree_name_optional), placeholder = stringResource(R.string.agents_worktree_generated),
        supportingText = stringResource(R.string.agents_worktree_location_help))
    if (composer.has("source.github")) {
        Disclosure(stringResource(R.string.agents_worktree_github), github) { github = !github }
        if (github) {
            JTextField(state.search.query, creation::query, Modifier.fillMaxWidth(), singleLine = true, enabled = enabled,
                label = stringResource(R.string.agents_worktree_search_source),
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search), keyboardActions = KeyboardActions(onSearch = { creation.search() }))
            JButton(stringResource(R.string.agents_worktree_search), creation::search, kind = JButtonKind.Secondary, size = JButtonSize.Small,
                loading = state.search.searching, enabled = enabled, icon = PersonaIcons.search)
            state.search.message?.let { Text(it.text(), style = type.caption, color = palette.danger.text) }
            state.search.items.forEach { item ->
                JRow("#${item.number} · ${item.title}", enabled = enabled, subtitle = item.url, subtitleLines = 1, onClick = { creation.pickSource(item) })
            }
        }
    }
    draft.provider?.let { provider ->
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text("#${provider.number} · ${provider.title}", Modifier.weight(1f), style = type.callout, color = palette.ink, maxLines = 2,
                overflow = TextOverflow.Ellipsis)
            JTextButton(stringResource(R.string.agents_worktree_clear), { creation.pickSource(null) }, enabled = enabled)
        }
    }
    Disclosure(stringResource(R.string.agents_worktree_advanced), advanced) { advanced = !advanced }
    if (advanced) {
        if (composer.has("branch_override")) JTextField(draft.branch, { branch -> creation.update { it.copy(branch = branch) } }, Modifier.fillMaxWidth(),
            singleLine = true, enabled = enabled, label = stringResource(R.string.agents_worktree_branch_optional),
            supportingText = stringResource(R.string.agents_worktree_branch_help))
        else if (draft.branch.isNotEmpty()) {
            Text(stringResource(R.string.agents_worktree_unsupported), style = type.caption, color = palette.danger.text)
            JTextButton(stringResource(R.string.agents_worktree_clear_branch), { creation.update { it.copy(branch = "") } }, enabled = enabled)
        }
        ParentRow(creation, state, rows, folders = composer.has("folder_parent"), onPick)
        JTextField(draft.note, { note -> creation.update { it.copy(note = note) } }, Modifier.fillMaxWidth(), minLines = 2, enabled = enabled,
            label = stringResource(R.string.agents_worktree_note))
        if (composer.has("sparse_presets") || draft.preset.isNotEmpty()) {
            val sparseTitle = stringResource(R.string.agents_worktree_sparse)
            val full = stringResource(R.string.agents_worktree_full_checkout)
            val chosen = composer.presets.firstOrNull { it.id == draft.preset }
            JRow(sparseTitle, value = chosen?.let(WorktreeCreation::presetLabel) ?: full, enabled = enabled, onClick = {
                onPick(WorktreePick(sparseTitle, listOf(JAction(full, checked = draft.preset.isEmpty()) { creation.update { it.copy(preset = "") } }) +
                    composer.presets.map { preset ->
                        JAction(WorktreeCreation.presetLabel(preset), checked = preset.id == draft.preset) { creation.update { it.copy(preset = preset.id) } }
                    }))
            })
            if (composer.has("sparse_presets")) {
                Disclosure(stringResource(R.string.agents_worktree_new_preset), newPreset) { newPreset = !newPreset }
                if (newPreset) {
                    JTextField(presetName, { presetName = it }, Modifier.fillMaxWidth(), singleLine = true, enabled = enabled,
                        label = stringResource(R.string.agents_worktree_preset_name))
                    JTextField(directories, { directories = it }, Modifier.fillMaxWidth(), minLines = 3, enabled = enabled,
                        label = stringResource(R.string.agents_worktree_directories), supportingText = stringResource(R.string.agents_worktree_directories_help))
                    JButton(stringResource(R.string.agents_worktree_save_preset), {
                        creation.savePreset(presetName, directories) { presetName = ""; directories = ""; newPreset = false }
                    }, kind = JButtonKind.Secondary, size = JButtonSize.Small, loading = state.savingPreset,
                        enabled = enabled && presetName.isNotBlank() && directories.isNotBlank())
                }
            }
        }
    }
    Text(stringResource(R.string.agents_worktree_agent_help), style = type.caption, color = palette.ink2)
}

/** The basic branch-and-path form (a computer without Orca's composer, or after choosing it). */
@Composable
private fun BasicFields(creation: WorktreeCreation, state: CreationState, info: WorktreePrepare, rows: List<AgentWorkspace>,
                        onPick: (WorktreePick) -> Unit) {
    val draft = state.draft
    val enabled = !state.locked
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    if (!draft.basic) Text(stringResource(R.string.agents_worktree_legacy), style = type.caption, color = palette.ink2)
    Text(if (info.branch.isNotEmpty()) stringResource(R.string.agents_worktree_repo_on_branch, info.repoName, info.branch)
        else stringResource(R.string.agents_worktree_repo_detached, info.repoName), style = type.caption, color = palette.ink2, maxLines = 1,
        overflow = TextOverflow.Ellipsis)
    if (draft.basic) Text(stringResource(R.string.agents_worktree_basic_help), style = type.caption, color = palette.ink2)
    if (draft.hasAdvanced || (draft.parent != WorktreeParent.Source && !info.chooseParent)) {
        JTextButton(stringResource(R.string.agents_worktree_clear_advanced), {
            creation.update { it.copy(name = "", note = "", baseRef = "", preset = "", provider = null,
                parent = if (info.chooseParent) it.parent else WorktreeParent.Source) }
        }, enabled = enabled)
    }
    if (info.automaticName) JSegmented(listOf(JSegment(stringResource(R.string.agents_worktree_manual)), JSegment(stringResource(R.string.agents_worktree_generated))),
        if (draft.automaticName) 1 else 0, { index -> if (enabled) creation.update { it.copy(automaticName = index == 1) } })
    if (!draft.automaticName) {
        JTextField(draft.branch, { branch -> creation.update { WorktreeForms.typeBranch(it, info.path, branch) } }, Modifier.fillMaxWidth(),
            singleLine = true, enabled = enabled, label = stringResource(R.string.agents_worktree_branch),
            placeholder = stringResource(R.string.agents_worktree_branch_placeholder))
        JTextField(if (draft.pathEdited) draft.path else WorktreeForms.suggestedPath(info.path, draft.branch),
            { path -> creation.update { WorktreeForms.typePath(it, info.path, path) } }, Modifier.fillMaxWidth(), singleLine = true, enabled = enabled,
            label = stringResource(R.string.agents_worktree_path), supportingText = stringResource(R.string.agents_worktree_path_hint))
    }
    if (info.chooseParent) ParentRow(creation, state, rows, folders = false, onPick)
    Text(stringResource(R.string.agents_worktree_form_note), style = type.caption, color = palette.ink2)
}

/** The parent a new worktree sits under in Orca's sidebar: the source (the default), none, or another of this computer's. */
@Composable
private fun ParentRow(creation: WorktreeCreation, state: CreationState, rows: List<AgentWorkspace>, folders: Boolean, onPick: (WorktreePick) -> Unit) {
    val target = creation.target
    val title = stringResource(R.string.agents_worktree_parent)
    val sourceLabel = stringResource(R.string.agents_worktree_source_parent)
    val noneLabel = stringResource(R.string.agents_worktree_no_parent)
    val parent = state.draft.parent
    val choices = rows.filter { it.machineId == target.machineId && it.source == ORCA_KIND && it.sourceId != target.workspaceId &&
        (folders || it.workspaceKind.let { kind -> kind != "folder" && kind != "folder-workspace" }) }
    JRow(title, value = when (parent) {
        WorktreeParent.Source -> sourceLabel
        WorktreeParent.None -> noneLabel
        is WorktreeParent.Workspace -> parent.label
    }, subtitle = stringResource(R.string.agents_worktree_parent_help), subtitleLines = 3, enabled = !state.locked, onClick = {
        onPick(WorktreePick(title, listOf(
            JAction(sourceLabel, checked = parent == WorktreeParent.Source) { creation.update { it.copy(parent = WorktreeParent.Source) } },
            JAction(noneLabel, checked = parent == WorktreeParent.None) { creation.update { it.copy(parent = WorktreeParent.None) } },
        ) + choices.map { row ->
            JAction(row.label, subtitle = row.cwd, checked = (parent as? WorktreeParent.Workspace)?.id == row.sourceId) {
                creation.update { it.copy(parent = WorktreeParent.Workspace(row.sourceId, row.label)) }
            }
        }))
    })
}
