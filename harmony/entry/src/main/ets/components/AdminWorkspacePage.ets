import { IconActions } from '../design/IconActions';
import { Colors } from '../design/Colors';
import { ButtonStyles } from '../design/Controls';
import { pasteboard } from '@kit.BasicServicesKit';
import { ClientModel } from '../models/ClientModel';
import { AdminManager, AdminMemory, AdminSnapshot, AdminWorkspace } from '../models/AdminWorkspace';
import { ResourceRow, Wire } from '../models/Wire';
import { Tokens } from '../design/Tokens';
import { tr } from '../design/Strings';
import { Haptics } from '../platform/Haptics';

const LAUNCHER_KINDS: string[] = ['api', 'launcher'];
const LAUNCHER_APIS: string[] = ['messages', 'responses', 'chat'];
const LAUNCHER_AUTHS: string[] = ['api_key', 'bearer'];

@ComponentV2
export struct AdminWorkspacePage {
  @Require @Param model: ClientModel;
  @Param page: string = '';
  @Param createManagerOnly: boolean = false;
  @Param active: boolean = false;
  @Param refreshRevision: number = 0;
  @Param row: ResourceRow | undefined = undefined;
  @Param installingManager: string = '';
  private focusedInstallation: string = '';
  @Event onOpenResource: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Event onOpenMachine: (row: ResourceRow) => void = (_row: ResourceRow) => {};
  @Local workspace: AdminWorkspace = new AdminWorkspace();
  @Local newManagerName: string = '';
  @Local activePage: boolean = false;
  private routeRevision: number = 0;

  aboutToAppear(): void { this.syncRoute(); }
  aboutToDisappear(): void { this.activePage = false; this.routeRevision++; this.workspace.hide(); }

  @Monitor('refreshRevision') refreshChanged(): void { if (this.activePage) this.workspace.refresh().catch(() => undefined); }
  @Monitor('active') activeChanged(): void { this.syncRoute(); }
  @Monitor('page') pageChanged(): void { if (this.activePage) this.syncRoute(); }
  @Monitor('row') rowChanged(): void { if (this.activePage) this.syncRoute(); }
  @Monitor('model.profile') profileChanged(): void { if (this.activePage) this.syncRoute(); }
  @Monitor('model.connected') connectionChanged(): void {
    if (this.activePage && this.model.connected) this.syncRoute();
  }

  private syncRoute(): void {
    const revision = ++this.routeRevision;
    this.activePage = this.active;
    if (!this.activePage) { this.workspace.hide(); return; }
    this.workspace.open(this.model, this.page, true, this.row).then(() => {
      if (revision !== this.routeRevision || !this.activePage) return;
      if (this.page === 'managers' && this.installingManager && this.focusedInstallation !== this.installingManager &&
          this.workspace.managers.some((manager: AdminManager) => manager.id === this.installingManager && !!manager.installing)) {
        this.workspace.selectManager(this.installingManager);
        this.focusedInstallation = this.installingManager;
      }
    }).catch(() => undefined);
  }

  private options(values: string[]): SelectOption[] {
    return values.map((value: string): SelectOption => ({ value: value }));
  }

  private selectedOptionValue(values: string[], index: number): string {
    return index >= 0 && index < values.length ? values[index] : '';
  }

  private machineChoices(): ResourceRow[] { return this.workspace.machineChoices(); }
  private machineLabel(row: ResourceRow): string { return row.title || row.id; }
  private machineIndex(): number {
    return this.machineChoices().findIndex((row: ResourceRow) => row.id === this.workspace.selectedMachine);
  }
  private chooseMachine(index: number): void {
    const row = this.machineChoices()[index];
    if (row) { this.workspace.selectMachine(row.id); this.workspace.refresh().catch(() => undefined); }
  }
  private machineSelectOptions(): SelectOption[] {
    return this.machineChoices().map((row: ResourceRow): SelectOption => ({ value: this.machineLabel(row) + ' · ' + (row.manager || this.model.profile.manager) }));
  }
  private machineSelectValue(): string {
    const row = this.machineChoices().find((item: ResourceRow) => item.id === this.workspace.selectedMachine);
    return row ? this.machineLabel(row) + ' · ' + (row.manager || this.model.profile.manager) : '';
  }
  private managerLabels(): string[] {
    return this.workspace.managers.map((row: AdminManager) => row.name + ' · ' + row.id);
  }
  private moveMachineLabels(): string[] {
    return this.workspace.moveChoices().map((row: ResourceRow) => (row.title || row.id) + ' · ' + row.manager);
  }
  private launcherAgent(): string { return this.workspace.launcherNew ? 'claude' : Wire.text(this.workspace.launcherDetail, 'agent', 'claude') || 'claude'; }
  private launcherAPIs(): string[] { return this.workspace.formText('kind', 'launcher') === 'launcher' ? (this.launcherAgent() === 'claude' ? ['messages'] : ['messages', 'responses']) : LAUNCHER_APIS; }
  private launcherSaveAllowed(): boolean {
    const kind = this.workspace.formText('kind', 'launcher'), agent = this.launcherAgent();
    return this.writeAllowed() && !!this.workspace.formText('name').trim() && !!this.workspace.formText('model').trim() &&
      (!(kind === 'api' || !agent || agent === 'claude') || !!this.workspace.formText('base_url').trim());
  }
  private memoryNames(): string[] { return this.workspace.memoryRows.map((row: AdminMemory) => row.name); }
  private engineDefaultLabels(): string[] { return this.workspace.selectedEngineHosts.map((id: string) => this.engineLabel(id)); }
  private legacyEngineLabels(): string[] { return this.workspace.engineOptions().map((id: string) => this.engineLabel(id)); }
  private launcherLabels(): string[] {
    return this.workspace.launchers.map((row: Record<string, Object>) =>
      Wire.text(row, 'display_name', Wire.text(row, 'name')) + ' · ' + Wire.text(row, 'name'));
  }
  private launcherIndex(): number {
    return this.workspace.launchers.findIndex((row: Record<string, Object>) => Wire.text(row, 'name') === this.workspace.selectedLauncher);
  }
  private managerIndex(): number {
    return this.workspace.managers.findIndex((row: AdminManager) => row.id === this.workspace.selectedManager);
  }
  private memoryIndex(): number {
    return this.workspace.memoryRows.findIndex((row: AdminMemory) => row.name === this.workspace.selectedMemory);
  }
  private currentManager(): AdminManager | undefined {
    return this.workspace.managers.find((row: AdminManager) => row.id === this.workspace.selectedManager);
  }
  private currentLauncher(): Record<string, Object> {
    return this.workspace.launchers.find((row: Record<string, Object>) => Wire.text(row, 'name') === this.workspace.selectedLauncher) || {};
  }
  private currentMemory(): AdminMemory | undefined {
    return this.workspace.memoryRows.find((row: AdminMemory) => row.name === this.workspace.selectedMemory);
  }
  private managerName(id: string): string {
    return this.workspace.managers.find((row: AdminManager) => row.id === id)?.name || id;
  }
  private managerForMoveMachine(id: string): string {
    return this.workspace.moveChoices().find((row: ResourceRow) => row.id === id)?.manager || '';
  }
  private moveMachineIndex(): number {
    return this.workspace.moveChoices().findIndex((row: ResourceRow) => row.id === this.workspace.selectedMoveMachine);
  }
  private moveDestinationOptions(): string[] {
    const owner = this.managerForMoveMachine(this.workspace.selectedMoveMachine);
    return this.workspace.managers.filter((row: AdminManager) => !row.installing && row.id !== owner).map((row: AdminManager) => row.name + ' · ' + row.id);
  }
  private moveDestinationIds(): string[] {
    const owner = this.managerForMoveMachine(this.workspace.selectedMoveMachine);
    return this.workspace.managers.filter((row: AdminManager) => !row.installing && row.id !== owner).map((row: AdminManager) => row.id);
  }
  private moveDestinationIndex(): number { return this.moveDestinationIds().indexOf(this.workspace.selectedMoveDestination); }
  private engineDefaultIndex(): number { return this.workspace.selectedEngineHosts.indexOf(this.workspace.selectedEngineDefault); }
  private legacyEngineIndex(): number {
    const id = this.workspace.selectedEngineHosts[0] || this.workspace.selectedEngine;
    return this.workspace.engineOptions().indexOf(id);
  }
  private writeAllowed(): boolean { return !this.workspace.saving && !['pending', 'result_unknown'].includes(this.workspace.writeState); }

  private confirmWrite(titleKey: string, targetLabel: string, path: string, snapshot: AdminSnapshot,
    action: () => void, detail: string = '', impact: string = ''): void {
    const message = tr('admin_confirmation_target') + ': ' + targetLabel +
      (detail ? '\n' + tr(detail) : '') + (impact ? '\n' + impact : '');
    this.getUIContext().showAlertDialog({ title: tr(titleKey), message: message,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('confirm'), action: () => {
        if (!this.activePage || !this.workspace.admits(snapshot, path)) return;
        action();
      } } });
  }

  private saveAssistant(): void {
    const snapshot = this.workspace.capture('/api/v1/assistant');
    this.workspace.saveAssistant(snapshot).catch(() => undefined);
  }

  private confirmDeleteMemory(file: AdminMemory): void {
    const path = '/api/v1/assistant/memory/' + encodeURIComponent(file.name), snapshot = this.workspace.capture(path);
    this.confirmWrite('admin_memory_delete', file.name, path, snapshot,
      () => { this.workspace.deleteMemory(snapshot, file.name).catch(() => undefined); }, 'admin_delete_memory_note');
  }

  private createMemory(): void {
    const path = '/api/v1/assistant/memory/' + encodeURIComponent(this.workspace.newMemoryName.trim());
    this.workspace.createMemory(this.workspace.capture(path)).catch(() => undefined);
  }

  private confirmCreateManager(): void {
    const name = this.newManagerName.trim(); if (!name) return;
    const path = '/api/v1/managers', snapshot = this.workspace.capture(path);
    this.confirmWrite('admin_manager_create', name, path, snapshot, () => {
      this.workspace.createManager(snapshot, name).then((ok: boolean) => { if (ok && this.workspace.admits(snapshot)) this.newManagerName = ''; }).catch(() => undefined);
    }, 'admin_manager_create_note');
  }

  private confirmRenameManager(manager: AdminManager): void {
    const name = this.workspace.managerNameDraft.trim(); if (!name || name === manager.name) return;
    const path = '/api/v1/managers/' + encodeURIComponent(manager.id);
    const snapshot = this.workspace.capture(path);
    this.confirmWrite('admin_manager_rename', manager.name + ' · ' + manager.id + ' → ' + name, path, snapshot,
      () => { this.workspace.renameManager(snapshot, manager.id, name).catch(() => undefined); }, 'admin_manager_rename_note');
  }

  private confirmDefaultManager(manager: AdminManager): void {
    const path = '/api/v1/managers/' + encodeURIComponent(manager.id);
    const snapshot = this.workspace.capture(path);
    this.confirmWrite('admin_manager_default', manager.name + ' · ' + manager.id, path, snapshot,
      () => { this.workspace.makeDefaultManager(snapshot, manager.id).catch(() => undefined); }, 'admin_manager_default_note');
  }

  private confirmDeleteManager(manager: AdminManager): void {
    const path = '/api/v1/managers/' + encodeURIComponent(manager.id), snapshot = this.workspace.capture(path);
    this.confirmWrite('admin_manager_delete', manager.name + ' · ' + manager.id, path, snapshot,
      () => { this.workspace.deleteManager(snapshot, manager.id).catch(() => undefined); }, 'admin_delete_manager_note');
  }

  private confirmManagerCode(manager: AdminManager): void {
    const path = '/api/v1/managers/' + encodeURIComponent(manager.id) + '/code', snapshot = this.workspace.capture(path);
    this.confirmWrite('admin_manager_code', manager.name + ' · ' + manager.id, path, snapshot,
      () => { this.workspace.renewManagerCode(snapshot, manager.id).catch(() => undefined); }, 'admin_manager_code_note');
  }

  private selectMoveMachine(index: number): void {
    const row = this.workspace.moveChoices()[index]; if (!row) return;
    this.workspace.selectMoveMachine(row.id);
    const ids = this.moveDestinationIds();
    this.workspace.selectMoveDestination(ids.includes(this.workspace.selectedMoveDestination) ? this.workspace.selectedMoveDestination : (ids[0] || ''));
  }

  private confirmMoveMachine(): void {
    const machineID = this.workspace.selectedMoveMachine, destination = this.workspace.selectedMoveDestination;
    const row = this.workspace.moveChoices().find((item: ResourceRow) => item.id === machineID);
    if (!row || !destination || destination === row.manager) return;
    const path = '/api/v1/managers/' + encodeURIComponent(destination) + '/machines';
    const target = (row.title || row.id) + ' · ' + row.id + ' → ' + this.managerName(destination);
    const snapshot = this.workspace.capture(path);
    this.confirmWrite('admin_manager_move', target, path, snapshot,
      () => { this.workspace.moveMachine(snapshot, destination, machineID).catch(() => undefined); }, 'admin_manager_move_note');
  }

  private selectLauncher(index: number): void {
    const row = this.workspace.launchers[index]; if (!row) return;
    this.workspace.selectLauncher(Wire.text(row, 'name'));
    this.workspace.refresh().catch(() => undefined);
  }
  private beginLauncher(isNew: boolean): void { this.workspace.beginLauncher(isNew); }
  private saveLauncher(): void {
    if (!this.workspace.formText('name').trim() || !this.writeAllowed()) return;
    const path = this.workspace.launcherNew ? '/api/v1/launchers' : '/api/v1/launchers/' + encodeURIComponent(this.workspace.selectedLauncher);
    const snapshot = this.workspace.capture(path);
    this.workspace.saveLauncher(snapshot).catch(() => undefined);
  }

  private async copyInstallCommand(name: string): Promise<void> {
    const command = this.workspace.managerInstallCommand(name);
    if (!command) return;
    try {
      await pasteboard.getSystemPasteboard().setData(pasteboard.createData(pasteboard.MIMETYPE_TEXT_PLAIN, command));
      this.workspace.notice = 'admin_command_copied';
    } catch (_) { this.workspace.error = 'admin_command_copy_failed'; }
  }

  private confirmDeleteLauncher(row: Record<string, Object>): void {
    const name = Wire.text(row, 'name'), path = '/api/v1/launchers/' + encodeURIComponent(name), snapshot = this.workspace.capture(path);
    this.confirmWrite('admin_launcher_delete', Wire.text(row, 'display_name', name) + ' · ' + name, path, snapshot,
      () => { this.workspace.deleteLauncher(snapshot, name).catch(() => undefined); }, 'admin_delete_launcher_note');
  }

  private confirmAddPreset(row: Record<string, Object>): void {
    const id = Wire.text(row, 'id'), path = '/api/v1/launchers', snapshot = this.workspace.capture(path);
    this.confirmWrite('admin_preset_add', Wire.text(row, 'name', id), path, snapshot,
      () => { this.workspace.addPreset(snapshot, id).catch(() => undefined); }, 'admin_preset_add_note');
  }

  private confirmImportLaunchers(): void {
    const machine = this.workspace.selectedMachine, providers = this.workspace.importerSelection.slice();
    const path = '/api/v1/launchers/import';
    const snapshot = this.workspace.capture(path), target = this.workspace.machineName(machine) + ' · ' + machine;
    const impact = tr('admin_import_providers') + ': ' + providers.join(', ');
    this.confirmWrite('admin_launcher_import', target, path, snapshot,
      () => { this.workspace.importLaunchers(snapshot, machine, providers).catch(() => undefined); }, 'admin_launcher_import_note', impact);
  }

  private loadImporter(): void {
    const machine = this.workspace.selectedMachine; if (!machine) return;
    const path = '/api/v1/launchers/import?machine=' + encodeURIComponent(machine);
    this.workspace.loadImporter(this.workspace.capture(path), machine).catch(() => undefined);
  }

  private confirmLauncherCheck(): void {
    const row = this.currentLauncher(), name = Wire.text(row, 'name'); if (!name) return;
    const machine = this.workspace.selectedMachine, path = '/api/v1/launchers/' + encodeURIComponent(name) + '/check' +
      (machine ? '?machine=' + encodeURIComponent(machine) : ''), snapshot = this.workspace.capture(path);
    this.confirmWrite('admin_launcher_check', Wire.text(row, 'display_name', name) + ' · ' + (machine ? this.workspace.machineName(machine) : tr('admin_default_machine')),
      path, snapshot, () => {
        this.workspace.checkLauncher(snapshot, name).then((ok: boolean) => { if (ok && this.workspace.admits(snapshot)) this.workspace.refresh().catch(() => undefined); }).catch(() => undefined);
      }, 'admin_launcher_check_note');
  }

  private confirmHealthCheck(): void {
    const machine = this.workspace.selectedMachine;
    const path = '/api/v1/launchers/health/check' + (machine ? '?machine=' + encodeURIComponent(machine) : '');
    const snapshot = this.workspace.capture(path), target = machine ? this.workspace.machineName(machine) + ' · ' + machine : tr('admin_all_online_machines');
    this.confirmWrite('admin_health_check', target, path, snapshot,
      () => { this.workspace.checkLauncherHealth(snapshot).then((ok: boolean) => { if (ok && this.workspace.admits(snapshot)) this.workspace.refresh().catch(() => undefined); }).catch(() => undefined); },
      'admin_health_check_note');
  }

  private confirmEngineSave(): void {
    const machineID = this.workspace.selectedMachine, row = this.machineChoices().find((item: ResourceRow) => item.id === machineID);
    if (!row) return;
    const path = '/api/v1/machines/' + encodeURIComponent(machineID) + '/engine';
    const snapshot = this.workspace.capture(path), target = (row.title || row.id) + ' · ' + (row.manager || this.model.profile.manager) + ' · ' + row.id;
    const hosts = this.workspace.selectedEngineHosts.map((id: string) => this.engineLabel(id));
    const defaultHost = this.workspace.selectedEngineHosts.includes(this.workspace.selectedEngineDefault) ? this.engineLabel(this.workspace.selectedEngineDefault) : '';
    const impact = tr('admin_engine_confirm_selection') + ': ' + (hosts.length ? hosts.join(', ') : tr('admin_engine_no_default')) +
      (defaultHost ? ' · ' + tr('admin_engine_default') + ': ' + defaultHost : '');
    this.confirmWrite('admin_engine_save', target, path, snapshot,
      () => { this.workspace.saveEngineSelection(snapshot).catch(() => undefined); }, 'admin_engine_save_note', impact);
  }

  private confirmNodeDeploy(): void {
    const machineID = this.workspace.selectedMachine, row = this.machineChoices().find((item: ResourceRow) => item.id === machineID);
    if (!row) return;
    const path = '/api/v1/machines/' + encodeURIComponent(machineID) + '/deploy', snapshot = this.workspace.capture(path);
    const target = (row.title || row.id) + ' · ' + (row.manager || this.model.profile.manager) + ' · ' + machineID + ' · ' +
      Wire.text(this.workspace.machineData, 'node_version', tr('unknown'));
    this.confirmWrite('admin_node_update', target, path, snapshot,
      () => { this.workspace.deployNode(snapshot).then((ok: boolean) => { if (ok && this.workspace.admits(snapshot)) this.workspace.refresh().catch(() => undefined); }).catch(() => undefined); },
      'admin_node_update_note');
  }

  private confirmHubCheck(): void {
    const path = '/api/v1/hub/update/check', snapshot = this.workspace.capture(path);
    const detail = Wire.bool(this.workspace.hubUpdate, 'auto') ? 'admin_hub_check_auto_note' : 'admin_hub_check_note';
    this.confirmWrite('admin_hub_check', this.model.profile.name, path, snapshot,
      () => { this.workspace.checkHubUpdate(snapshot).then((ok: boolean) => { if (ok && this.workspace.admits(snapshot)) this.workspace.refresh().catch(() => undefined); }).catch(() => undefined); }, detail);
  }

  private confirmHubApply(now: boolean): void {
    const path = '/api/v1/hub/update', snapshot = this.workspace.capture(path);
    const detail = now ? 'admin_hub_update_now_note' : 'admin_hub_update_note';
    this.confirmWrite(now ? 'admin_hub_update_now' : 'admin_hub_update', this.model.profile.name + ' · ' + Wire.text(this.workspace.hubUpdate, 'version'),
      path, snapshot, () => { this.workspace.applyHubUpdate(snapshot, now).then((ok: boolean) => { if (ok && this.workspace.admits(snapshot)) this.workspace.refresh().catch(() => undefined); }).catch(() => undefined); }, detail);
  }

  private confirmHubSetting(key: 'auto' | 'auto_update_nodes', enabled: boolean): void {
    const path = '/api/v1/hub/update/settings', snapshot = this.workspace.capture(path);
    const detail = key === 'auto_update_nodes' ? 'admin_hub_node_policy_note' : 'admin_hub_auto_policy_note';
    const setting = tr(key === 'auto' ? 'admin_auto_update' : 'admin_auto_update_nodes');
    const impact = tr(enabled ? 'admin_setting_enable' : 'admin_setting_disable') + ': ' + setting;
    this.confirmWrite('admin_hub_setting', this.model.profile.name + ' · ' + setting, path, snapshot,
      () => { this.workspace.setHubUpdateSetting(snapshot, key, enabled).then((ok: boolean) => { if (ok && this.workspace.admits(snapshot)) this.workspace.refresh().catch(() => undefined); }).catch(() => undefined); }, detail, impact);
  }

  private engineLabel(id: string): string { return this.workspace.engineName(id); }

  @Builder hubUpdateMenu() {
    Menu() {
      MenuItem({ content: tr('admin_hub_update') }).enabled(this.writeAllowed())
        .onClick(() => this.confirmHubApply(false))
      MenuItem({ content: tr('admin_hub_update_now') }).enabled(this.writeAllowed())
        .onClick(() => this.confirmHubApply(true))
    }
  }

  @Builder feedback() {
    if (this.workspace.loading && !this.workspace.saving) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
    if (this.workspace.notice) Text(tr(this.workspace.notice)).fontSize(Tokens.meta).fontColor($r('sys.color.font_emphasize')).width('100%')
    if (this.workspace.writeState) Text(tr('admin_state_' + this.workspace.writeState)).fontSize(Tokens.meta)
      .fontColor(this.workspace.writeState === 'failed' || this.workspace.writeState === 'pending' || this.workspace.writeState === 'result_unknown' ? $r('app.color.persona_danger') : $r('sys.color.font_secondary')).width('100%')
    if (this.workspace.refreshFailed) Text(tr('admin_refresh_failed')).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%')
    if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%')
    if (this.workspace.unsupported) Text(tr('admin_unsupported')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
  }

  @Builder private title(key: string) {
    Text(tr(key)).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
  }

  @Builder private assistantContent() {
    Column({ space: Tokens.section }) {
      Column({ space: Tokens.gap }) {
        TextInput({ text: this.workspace.formText('timezone'), placeholder: tr('admin_timezone') })
          .onChange((value: string) => this.workspace.setForm('timezone', value)).id('admin.assistant.timezone')
        TextInput({ text: this.workspace.formText('machine_id'), placeholder: tr('admin_machine_id') })
          .onChange((value: string) => this.workspace.setForm('machine_id', value)).id('admin.assistant.machine')
        TextInput({ text: this.workspace.formText('launcher'), placeholder: tr('admin_launcher_id') })
          .onChange((value: string) => this.workspace.setForm('launcher', value)).id('admin.assistant.launcher')
        TextArea({ text: this.workspace.formText('notes'), placeholder: tr('admin_about_me') }).height(Tokens.inputExpanded)
          .onChange((value: string) => this.workspace.setForm('notes', value)).id('admin.assistant.notes')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
      Column({ space: Tokens.gap }) {
        this.title('admin_preferences')
        Row({ space: Tokens.gap }) {
          Text(tr('admin_main_tasks_quiet')).layoutWeight(1).fontSize(Tokens.body)
          Toggle({ type: ToggleType.Switch, isOn: this.workspace.formBool('main_tasks_quiet', true) })
            .enabled(!this.workspace.saving).onChange((value: boolean) => this.workspace.setForm('main_tasks_quiet', value)).id('admin.assistant.main_tasks_quiet')
        }.width('100%')
        Row({ space: Tokens.gap }) {
          Text(tr('admin_agent_tasks')).layoutWeight(1).fontSize(Tokens.body)
          Toggle({ type: ToggleType.Switch, isOn: this.workspace.formBool('agent_tasks') })
            .enabled(!this.workspace.saving).onChange((value: boolean) => this.workspace.setForm('agent_tasks', value)).id('admin.assistant.agent_tasks')
        }.width('100%')
        Row({ space: Tokens.gap }) {
          Text(tr('admin_inbox_signals')).layoutWeight(1).fontSize(Tokens.body)
          Toggle({ type: ToggleType.Switch, isOn: this.workspace.formBool('inbox_signals') })
            .enabled(!this.workspace.saving).onChange((value: boolean) => this.workspace.setForm('inbox_signals', value)).id('admin.assistant.inbox_signals')
        }.width('100%')
        Row({ space: Tokens.gap }) {
          Text(tr('admin_interject_background')).layoutWeight(1).fontSize(Tokens.body)
          Toggle({ type: ToggleType.Switch, isOn: this.workspace.formBool('interject_background') })
            .enabled(!this.workspace.saving).onChange((value: boolean) => this.workspace.setForm('interject_background', value)).id('admin.assistant.interject_background')
        }.width('100%')
        Row({ space: Tokens.gap }) {
          Text(tr('admin_knowledge_topics')).layoutWeight(1).fontSize(Tokens.body)
          Toggle({ type: ToggleType.Switch, isOn: this.workspace.formBool('kb_topics', true) })
            .enabled(!this.workspace.saving).onChange((value: boolean) => this.workspace.setForm('kb_topics', value)).id('admin.assistant.kb_topics')
        }.width('100%')
        TextInput({ text: this.workspace.formText('idle_close_hours', '12'), placeholder: tr('admin_idle_hours') }).type(InputType.Number)
          .onChange((value: string) => this.workspace.setForm('idle_close_hours', value)).id('admin.assistant.idle_close_hours')
        TextInput({ text: this.workspace.formText('archive_closed_days', '3'), placeholder: tr('admin_archive_days') }).type(InputType.Number)
          .onChange((value: string) => this.workspace.setForm('archive_closed_days', value)).id('admin.assistant.archive_closed_days')
        TextInput({ text: this.workspace.formText('recording_days', '14'), placeholder: tr('admin_recording_days') }).type(InputType.Number)
          .onChange((value: string) => this.workspace.setForm('recording_days', value)).id('admin.assistant.recording_days')
        TextInput({ text: this.workspace.formText('minutes_days', '0'), placeholder: tr('admin_minutes_days') }).type(InputType.Number)
          .onChange((value: string) => this.workspace.setForm('minutes_days', value)).id('admin.assistant.minutes_days')
        Row({ space: Tokens.gap }) {
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('admin_save')).bindTips(tr('admin_save')).enabled(this.workspace.draftDirty && this.writeAllowed())
            .onClick(() => this.saveAssistant()).id('admin.assistant.save')
          if (this.workspace.saving) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
        }.width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
      if (typeof this.workspace.assistantData['next_suggestions'] === 'boolean') Column({ space: 6 }) {
        Text(tr('next_suggestions_title')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor(Colors.secondary)
        Row({ space: Tokens.inset }) {
          SymbolGlyph($r('sys.symbol.wand_and_stars')).fontSize('22vp').fontColor([Colors.ai])
          Text(tr('next_suggestions_title')).layoutWeight(1).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body))
            .fontColor(Colors.ink).maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis })
          Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.workspace.assistantData, 'next_suggestions') })
            .enabled(!this.workspace.saving).accessibilityText(tr('next_suggestions_title')).onChange((on: boolean) => {
              if (on !== Wire.bool(this.workspace.assistantData, 'next_suggestions')) {
                Haptics.selection();
                this.workspace.setNextSuggestions(this.workspace.capture('/api/v1/assistant'), on).catch(() => undefined);
              }
            }).id('admin.assistant.next_suggestions')
        }.width('100%').constraintSize({ minHeight: 48 }).padding({ left: Tokens.inset, right: Tokens.inset }).backgroundColor(Colors.surface).borderRadius(Tokens.inset)
        Text(tr('next_suggestions_note')).fontSize(Tokens.caption).maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
          .fontColor(Colors.secondary).width('100%').id('admin.assistant.next_suggestions.help')
      }.width('100%').alignItems(HorizontalAlign.Start)
      if (typeof this.workspace.assistantData['recent_task_chats'] === 'boolean') Column({space:6}) {
        Text(tr('phone_sidebar_recent_heading')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor(Colors.secondary)
        Row({space:Tokens.inset}) {
          SymbolGlyph($r('sys.symbol.clock')).fontSize('22vp').fontColor([Colors.link])
          Text(tr('phone_sidebar_show_scheduled')).layoutWeight(1).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body))
            .fontColor(Colors.ink).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
          Toggle({type:ToggleType.Switch,isOn:Wire.bool(this.workspace.assistantData,'recent_task_chats')})
            .enabled(!this.workspace.saving).accessibilityText(tr('phone_sidebar_show_scheduled')).onChange((on:boolean) => {
              this.workspace.setRecentTaskChats(this.workspace.capture('/api/v1/assistant'),on).catch(() => undefined);
            }).id('admin.assistant.recent_task_chats')
        }.width('100%').constraintSize({minHeight:48}).padding({left:Tokens.inset,right:Tokens.inset}).backgroundColor(Colors.surface).borderRadius(Tokens.inset)
        Text(tr('phone_sidebar_recent_help')).fontSize(Tokens.caption).maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
          .fontColor(Colors.secondary).width('100%').id('admin.assistant.recent_task_chats.help')
      }.width('100%').alignItems(HorizontalAlign.Start)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder private memoryContent() {
    Column({ space: Tokens.section }) {
      Column({ space: Tokens.gap }) {
        if (this.workspace.memoryRows.length) {

          Select(this.options(this.memoryNames())).selected(this.memoryIndex()).value(this.selectedOptionValue(this.memoryNames(), this.memoryIndex()))
            .onSelect((index: number) => { const row = this.workspace.memoryRows[index]; if (row) this.workspace.selectMemory(row.name); }).id('admin.memory.file')
          if (this.currentMemory()) {
            Text(this.currentMemory()?.updatedAt || tr('admin_updated_unknown')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
            TextArea({ text: this.workspace.memoryDraft, placeholder: tr('admin_memory_content') }).height(Tokens.inputExpanded)
              .onChange((value: string) => this.workspace.setMemoryDraft(value)).id('admin.memory.content')
            Row({ space: Tokens.gap }) {
              Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('admin_save')).bindTips(tr('admin_save')).enabled(this.workspace.memoryDirty && this.writeAllowed())
                .onClick(() => this.workspace.saveMemory(this.workspace.capture('/api/v1/assistant/memory/' + encodeURIComponent(this.workspace.selectedMemory))).catch(() => false)).id('admin.memory.save')
              if (this.workspace.selectedMemory !== 'MEMORY.md') Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('delete')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('delete', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('delete')).bindTips(tr('delete'))
                .enabled(this.writeAllowed()).onClick(() => this.confirmDeleteMemory(this.currentMemory() as AdminMemory)).id('admin.memory.delete')
            }.width('100%')
          }
        } else if (!this.workspace.loading && !this.workspace.error) Text(tr('admin_memory_empty')).fontSize(Tokens.meta).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)

      Column({ space: Tokens.gap }) {
        this.title('admin_new_memory')
        TextInput({ text: this.workspace.newMemoryName, placeholder: tr('admin_memory_filename') })
          .onChange((value: string) => this.workspace.setNewMemoryName(value)).id('admin.memory.new_name')
        if (this.workspace.memoryCreateError()) Text(tr(this.workspace.memoryCreateError())).fontSize(Tokens.meta)
          .fontColor($r('app.color.persona_danger')).width('100%')
        TextArea({ text: this.workspace.newMemoryContent, placeholder: tr('admin_memory_content') }).height(Tokens.inputExpanded)
          .onChange((value: string) => this.workspace.setNewMemoryContent(value)).id('admin.memory.new_content')
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_create_memory')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_create_memory', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_create_memory')).bindTips(tr('admin_create_memory')).enabled(this.workspace.canCreateMemory() && this.writeAllowed())
          .onClick(() => this.createMemory()).id('admin.memory.create')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder private createManagerForm() {
    Column({ space: Tokens.gap }) {
      this.title(this.createManagerOnly ? 'phone_new_assistant' : 'admin_new_manager')
      TextInput({ text: this.newManagerName, placeholder: tr('admin_manager_name') })
        .onChange((value: string) => this.newManagerName = value).id('admin.manager.new_name')
      Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_create_manager')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_create_manager', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_create_manager')).bindTips(tr('admin_create_manager'))
        .enabled(this.writeAllowed() && !!this.newManagerName.trim()).onClick(() => this.confirmCreateManager()).id('admin.manager.create')
    }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
      .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
  }

  @Builder private managersContent() {
    Column({ space: Tokens.section }) {
      Column({ space: Tokens.gap }) {
        if (this.workspace.managers.length) {

          Select(this.options(this.managerLabels())).selected(this.managerIndex())
            .value(this.selectedOptionValue(this.managerLabels(), this.managerIndex()))
            .onSelect((index: number) => { const row = this.workspace.managers[index]; if (row) this.workspace.selectManager(row.id); }).id('admin.manager.select')
          if (this.currentManager()) {
            Text(tr(this.currentManager()?.online ? 'admin_manager_online' : 'admin_manager_offline') + ' · ' +
              (this.currentManager()?.isDefault ? tr('admin_default') : '') + (this.currentManager()?.builtin ? ' · ' + tr('admin_builtin') : ''))
              .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
            TextInput({ text: this.workspace.managerNameDraft, placeholder: tr('admin_manager_name') })
              .onChange((value: string) => this.workspace.setManagerNameDraft(value)).id('admin.manager.name')
            Row({ space: Tokens.gap }) {
              Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_rename')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_rename', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_rename')).bindTips(tr('admin_rename')).enabled(this.writeAllowed() && !!this.workspace.managerNameDraft.trim() && this.workspace.managerNameDraft.trim() !== this.currentManager()?.name)
                .onClick(() => this.confirmRenameManager(this.currentManager() as AdminManager)).id('admin.manager.rename')
              if (!this.currentManager()?.isDefault) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_make_default')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_make_default', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_make_default')).bindTips(tr('admin_make_default')).enabled(this.writeAllowed() && !this.currentManager()?.installing)
                .onClick(() => this.confirmDefaultManager(this.currentManager() as AdminManager)).id('admin.manager.default')
            }.width('100%')
            Row({ space: Tokens.gap }) {
              Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_new_install_code')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_new_install_code', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_new_install_code')).bindTips(tr('admin_new_install_code')).enabled(this.writeAllowed() && !this.currentManager()?.builtin)
                .onClick(() => this.confirmManagerCode(this.currentManager() as AdminManager)).id('admin.manager.code')
              Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('delete')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('delete', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('delete')).bindTips(tr('delete'))
                .enabled(this.writeAllowed() && this.workspace.managerCanDelete(this.currentManager()))
                .onClick(() => this.confirmDeleteManager(this.currentManager() as AdminManager)).id('admin.manager.delete')
            }.width('100%')
            if (this.currentManager()?.installing) Text(tr('admin_manager_installing')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
          }
          if (Object.keys(this.workspace.installCode).length) {
            Text(tr('admin_install_code')).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).width('100%')
            Text(Wire.text(this.workspace.installCode, 'code')).fontSize(Tokens.title).fontWeight(FontWeight.Bold).width('100%')
            Text(tr('admin_code_expires') + ': ' + Wire.text(this.workspace.installCode, 'expires_at')).fontSize(Tokens.meta).width('100%')
            ForEach(this.workspace.managerInstallCommandKinds(), (commandName: string) => {
              Text(tr('admin_command_' + commandName)).fontSize(Tokens.meta).fontWeight(FontWeight.Medium).width('100%')
              Text(this.workspace.managerInstallCommand(commandName)).fontSize(Tokens.meta).width('100%')
              Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('copy')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('copy', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('copy')).bindTips(tr('copy')).onClick(() => this.copyInstallCommand(commandName)).id('admin.manager.copy.' + commandName)
            }, (commandName: string) => commandName)
          }
        } else if (!this.workspace.loading && !this.workspace.error) Text(tr('admin_managers_empty')).fontSize(Tokens.meta).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)

      this.createManagerForm()

      Column({ space: Tokens.gap }) {
        this.title('admin_move_machine')
        if (this.workspace.moveChoices().length && this.workspace.managers.length > 1) {

          Select(this.options(this.moveMachineLabels())).selected(this.moveMachineIndex())
            .value(this.selectedOptionValue(this.moveMachineLabels(), this.moveMachineIndex()))
            .onSelect((index: number) => this.selectMoveMachine(index)).id('admin.manager.move_machine')
          if (this.moveDestinationIds().length) Select(this.options(this.moveDestinationOptions())).selected(this.moveDestinationIndex())
            .value(this.selectedOptionValue(this.moveDestinationOptions(), this.moveDestinationIndex()))
            .onSelect((index: number) => this.workspace.selectMoveDestination(this.moveDestinationIds()[index] || '')).id('admin.manager.move_destination')
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_move')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_move', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_move')).bindTips(tr('admin_move')).enabled(this.writeAllowed() && !!this.workspace.selectedMoveMachine && !!this.workspace.selectedMoveDestination)
            .onClick(() => this.confirmMoveMachine()).id('admin.manager.move')
        } else Text(tr('admin_no_machine_move')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder private launcherEditorContent() {
    Column({ space: Tokens.gap }) {
      TextInput({ text: this.workspace.formText('name'), placeholder: tr('admin_launcher_name') }).enabled(this.workspace.launcherNew)
        .onChange((value: string) => this.workspace.setForm('name', value)).id('admin.launcher.edit.name')
      TextInput({ text: this.workspace.formText('display_name'), placeholder: tr('admin_launcher_display_name') })
        .onChange((value: string) => this.workspace.setForm('display_name', value)).id('admin.launcher.edit.display_name')
      Select(this.options(LAUNCHER_KINDS)).selected(LAUNCHER_KINDS.indexOf(this.workspace.formText('kind', 'api')))
        .value(this.selectedOptionValue(LAUNCHER_KINDS, LAUNCHER_KINDS.indexOf(this.workspace.formText('kind', 'api'))))
        .onSelect((index: number) => this.workspace.setForm('kind', LAUNCHER_KINDS[index] || 'api')).id('admin.launcher.edit.kind')
      Column({ space: Tokens.gap }) {
        Select(this.options(this.launcherAPIs())).selected(this.launcherAPIs().indexOf(this.workspace.formText('api', 'messages')))
          .value(this.selectedOptionValue(this.launcherAPIs(), this.launcherAPIs().indexOf(this.workspace.formText('api', 'messages'))))
          .onSelect((index: number) => this.workspace.setForm('api', this.launcherAPIs()[index] || 'messages')).id('admin.launcher.edit.api')
        TextInput({ text: this.workspace.formText('base_url'), placeholder: tr('admin_launcher_base_url') })
          .onChange((value: string) => this.workspace.setForm('base_url', value)).id('admin.launcher.edit.base_url')
        TextInput({ text: this.workspace.formText('model'), placeholder: tr('admin_launcher_model') })
          .onChange((value: string) => this.workspace.setForm('model', value)).id('admin.launcher.edit.model')
        Select(this.options(LAUNCHER_AUTHS)).selected(LAUNCHER_AUTHS.indexOf(this.workspace.formText('auth', 'api_key')))
          .value(this.selectedOptionValue(LAUNCHER_AUTHS, LAUNCHER_AUTHS.indexOf(this.workspace.formText('auth', 'api_key'))))
          .onSelect((index: number) => this.workspace.setForm('auth', LAUNCHER_AUTHS[index] || 'api_key')).id('admin.launcher.edit.auth')
        TextInput({ text: this.workspace.launcherSecretDraft, placeholder: tr(this.workspace.launcherNew ? 'admin_launcher_key' : 'admin_launcher_replace_key') })
          .type(InputType.Password).showPasswordIcon(false).onChange((value: string) => this.workspace.setLauncherSecretDraft(value)).id('admin.launcher.edit.key')
      }.width('100%')
      Row({ space: Tokens.gap }) {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('cancel')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('cancel', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('cancel')).bindTips(tr('cancel')).enabled(!this.workspace.saving)
          .onClick(() => this.workspace.cancelLauncherEdit()).id('admin.launcher.edit.cancel')
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('admin_save')).bindTips(tr('admin_save')).enabled(this.launcherSaveAllowed())
          .onClick(() => this.saveLauncher()).id('admin.launcher.edit.save')
      }.width('100%')
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder private launcherSummary() {
    if (this.workspace.selectedLauncher) {
      Text(Wire.text(this.currentLauncher(), 'display_name', Wire.text(this.currentLauncher(), 'name'))).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
      Text(Wire.text(this.currentLauncher(), 'source') + ' · ' + Wire.text(this.currentLauncher(), 'kind', Wire.text(this.currentLauncher(), 'agent'))).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      if (Wire.text(this.currentLauncher(), 'base_url')) Text(Wire.text(this.currentLauncher(), 'base_url')).fontSize(Tokens.meta).width('100%')
      if (Wire.text(this.currentLauncher(), 'model')) Text(tr('admin_launcher_model') + ': ' + Wire.text(this.currentLauncher(), 'model')).fontSize(Tokens.meta).width('100%')
      Text(tr(Wire.bool(this.currentLauncher(), 'has_key') ? 'admin_launcher_key_present' : 'admin_launcher_key_missing')).fontSize(Tokens.meta).width('100%')
      if (Wire.strings(this.currentLauncher(), 'machines').length) Text(tr('admin_machines') + ': ' + Wire.strings(this.currentLauncher(), 'machines').join(', ')).fontSize(Tokens.meta).width('100%')
      if (Wire.strings(this.currentLauncher(), 'stale').length) Text(tr('admin_launcher_stale') + ': ' + Wire.strings(this.currentLauncher(), 'stale').join(', ')).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%')
      if (Object.keys(this.workspace.launcherDetail).length) {
        if (Wire.bool(this.workspace.launcherDetail, 'has_headers')) Text(tr('admin_launcher_headers_present')).fontSize(Tokens.meta).width('100%')
        if (Wire.bool(this.workspace.launcherDetail, 'has_env')) Text(tr('admin_launcher_env_present')).fontSize(Tokens.meta).width('100%')
        if (Wire.rows(this.workspace.launcherDetail, 'models').length) Text(tr('admin_launcher_models') + ': ' + Wire.rows(this.workspace.launcherDetail, 'models').map((item: Record<string, Object>) => Wire.text(item, 'id', Wire.text(item, 'name'))).join(', ')).fontSize(Tokens.meta).width('100%')
      }
      Row({ space: Tokens.gap }) {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_launcher_check')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_launcher_check', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_launcher_check')).bindTips(tr('admin_launcher_check')).enabled(this.writeAllowed())
          .onClick(() => this.confirmLauncherCheck()).id('admin.launcher.check')
        if (Wire.text(this.currentLauncher(), 'source') === 'persona') Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_edit')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_edit', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_edit')).bindTips(tr('admin_edit')).enabled(this.writeAllowed())
          .onClick(() => this.beginLauncher(false)).id('admin.launcher.edit')
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('delete')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('delete', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('delete')).bindTips(tr('delete')).enabled(this.writeAllowed())
          .onClick(() => this.confirmDeleteLauncher(this.currentLauncher())).id('admin.launcher.delete')
      }.width('100%')
      if (Object.keys(this.workspace.healthResult).length) {
        Text(tr(Wire.bool(this.workspace.healthResult, 'ok') ? 'admin_check_ok' : 'admin_check_failed') + ' · ' + Wire.text(this.workspace.healthResult, 'model') + ' · ' + Wire.text(this.workspace.healthResult, 'error'))
          .fontSize(Tokens.meta).width('100%')
      }
    } else Text(tr('admin_launcher_select')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
  }

  @Builder private launcherContent() {
    Column({ space: Tokens.section }) {
      Column({ space: Tokens.gap }) {
        if (this.workspace.launchers.length) Select(this.options(this.launcherLabels())).selected(this.launcherIndex())
          .value(this.selectedOptionValue(this.launcherLabels(), this.launcherIndex()))
          .onSelect((index: number) => this.selectLauncher(index)).id('admin.launcher.select')
        Row({ space: Tokens.gap }) {
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_new_launcher')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_new_launcher', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_new_launcher')).bindTips(tr('admin_new_launcher')).onClick(() => this.beginLauncher(true)).id('admin.launcher.new')
        }.width('100%')
        if (this.workspace.launcherEditor) this.launcherEditorContent()
        else this.launcherSummary()
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)

      Column({ space: Tokens.gap }) {
        this.title('admin_launcher_import')
        Select(this.machineSelectOptions()).selected(this.machineIndex()).value(this.machineSelectValue()).enabled(this.machineChoices().length > 0)
          .onSelect((index: number) => this.chooseMachine(index)).id('admin.launcher.import_machine')
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_load_import')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_load_import', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_load_import')).bindTips(tr('admin_load_import')).enabled(!!this.workspace.selectedMachine && !this.workspace.loading)
          .onClick(() => this.loadImporter()).id('admin.launcher.import_load')
        ForEach(Wire.rows(this.workspace.importer, 'providers'), (item: Record<string, Object>) => {
          Row({ space: Tokens.gap }) {
            Toggle({ type: ToggleType.Checkbox, isOn: this.workspace.importerSelection.includes(Wire.text(item, 'name')) })
              .enabled(this.writeAllowed()).onChange((checked: boolean) => this.workspace.selectImport(Wire.text(item, 'name'), checked))
            Text(Wire.text(item, 'name') + (Wire.bool(item, 'has_key') ? ' · ' + tr('admin_launcher_key_present') : '')).layoutWeight(1).fontSize(Tokens.body)
          }.width('100%')
        }, (item: Record<string, Object>) => Wire.text(item, 'name'))
        if (this.workspace.importerSelection.length) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_import_selected')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_import_selected', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_import_selected')).bindTips(tr('admin_import_selected'))
          .enabled(this.writeAllowed() && this.workspace.importerMachine === this.workspace.selectedMachine)
          .onClick(() => this.confirmImportLaunchers()).id('admin.launcher.import_commit')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)

      Column({ space: Tokens.gap }) {
        this.title('admin_launcher_presets')
        if (this.workspace.presets.length) ForEach(this.workspace.presets, (preset: Record<string, Object>) => {
          Button({ type: ButtonType.Normal }) { Row({ space: Tokens.inset }) { Text(Wire.text(preset, 'name', Wire.text(preset, 'id'))).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1); SymbolGlyph($r('sys.symbol.chevron_right')).fontSize(Tokens.meta).fontColor([Colors.secondary]) }.width('100%').constraintSize({ minHeight: Tokens.touch }) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).padding({ left: Tokens.page, right: Tokens.page }).width('100%').height(Tokens.touch).enabled(this.writeAllowed())
            .onClick(() => this.confirmAddPreset(preset)).id('admin.launcher.preset.' + Wire.text(preset, 'id'))
        }, (preset: Record<string, Object>) => Wire.text(preset, 'id'))
        else Text(tr('admin_launcher_presets_empty')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)

      Column({ space: Tokens.gap }) {
        this.title('admin_launcher_health')
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_health_check')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_health_check', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_health_check')).bindTips(tr('admin_health_check')).enabled(this.writeAllowed())
          .onClick(() => this.confirmHealthCheck()).id('admin.launcher.health_check')
        ForEach(this.workspace.healthRecords, (record: Record<string, Object>) => {
          Column({ space: Tokens.fine }) {
            Text(Wire.text(record, 'launcher') + ' · ' + Wire.text(record, 'machine')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
            Text(Wire.text(record, 'state') + ' · ' + Wire.text(record, 'error', Wire.text(record, 'checked_at'))).fontSize(Tokens.meta)
              .fontColor(Wire.text(record, 'state') === 'healthy' ? $r('sys.color.font_secondary') : $r('app.color.persona_danger')).width('100%')
          }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
            .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
        }, (record: Record<string, Object>) => Wire.text(record, 'machine') + ':' + Wire.text(record, 'launcher'))
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder private engineContent() {
    Column({ space: Tokens.section }) {
      Column({ space: Tokens.gap }) {
        Select(this.machineSelectOptions()).selected(this.machineIndex()).value(this.machineSelectValue()).enabled(this.machineChoices().length > 0)
          .onSelect((index: number) => this.chooseMachine(index)).id('admin.engine.machine')
        if (this.workspace.selectedMachine && Object.keys(this.workspace.machineData).length) {
          Text((this.workspace.machineName(this.workspace.selectedMachine) || this.workspace.selectedMachine) + ' · ' + Wire.text(this.workspace.machineData, 'manager', this.model.profile.manager))
            .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
          if (this.workspace.engineSetsSupported()) {
            ForEach(this.workspace.engineOptions().concat(['none']), (id: string) => {
              Row({ space: Tokens.gap }) {
                Toggle({ type: ToggleType.Checkbox, isOn: id === 'none' ? this.workspace.selectedEngineHosts.length === 0 : this.workspace.selectedEngineHosts.includes(id) })
                  .onChange((checked: boolean) => this.workspace.selectEngineHost(id, checked)).id('admin.engine.option.' + id)
                Text(this.engineLabel(id)).layoutWeight(1).fontSize(Tokens.body)
              }.width('100%')
            }, (id: string) => id)
            if (this.workspace.selectedEngineHosts.length) {
              Text(tr('admin_engine_default')).fontSize(Tokens.meta).width('100%')

              Select(this.options(this.engineDefaultLabels())).selected(this.engineDefaultIndex())
                .value(this.selectedOptionValue(this.engineDefaultLabels(), this.engineDefaultIndex()))
                .onSelect((index: number) => this.workspace.selectEngineDefault(this.workspace.selectedEngineHosts[index] || '')).id('admin.engine.default')
            } else Text(tr('admin_engine_no_default')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
          } else {

            Select(this.options(this.legacyEngineLabels())).selected(this.legacyEngineIndex())
              .value(this.selectedOptionValue(this.legacyEngineLabels(), this.legacyEngineIndex()))
              .onSelect((index: number) => this.workspace.chooseLegacyEngine(this.workspace.engineOptions()[index] || 'herdr')).id('admin.engine.legacy')
          }
          if (Wire.bool(this.workspace.machineData, 'engine_sets_unsupported')) Text(tr('admin_engine_sets_unsupported')).fontSize(Tokens.meta)
            .fontColor($r('app.color.persona_danger')).width('100%')
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('admin_save')).bindTips(tr('admin_save')).enabled(this.writeAllowed() && this.machineChoices().length > 0)
            .onClick(() => this.confirmEngineSave()).id('admin.engine.save')
        } else if (!this.workspace.loading) Text(tr('admin_machine_unavailable')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder private nodeUpdateContent() {
    Column({ space: Tokens.section }) {
      Column({ space: Tokens.gap }) {
        Select(this.machineSelectOptions()).selected(this.machineIndex()).value(this.machineSelectValue()).enabled(this.machineChoices().length > 0)
          .onSelect((index: number) => this.chooseMachine(index)).id('admin.node_update.machine')
        if (this.workspace.selectedMachine && Object.keys(this.workspace.machineData).length) {
          Text((this.workspace.machineName(this.workspace.selectedMachine) || this.workspace.selectedMachine) + ' · ' + this.workspace.selectedMachine)
            .fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
          Text(tr('admin_node_version') + ': ' + Wire.text(this.workspace.machineData, 'node_version', tr('unknown'))).fontSize(Tokens.meta).width('100%')
          if (Wire.bool(this.workspace.machineData, 'node_mismatch')) Text(tr('admin_node_mismatch')).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%')
          if (Wire.bool(this.workspace.machineData, 'deploying')) Text(tr('admin_node_deploying') + ' · ' + Wire.text(this.workspace.machineData, 'deploy_phase'))
            .fontSize(Tokens.meta).width('100%')
          if (Wire.text(this.workspace.machineData, 'deploy_error')) Text(Wire.text(this.workspace.machineData, 'deploy_error')).fontSize(Tokens.meta)
            .fontColor($r('app.color.persona_danger')).width('100%')
          if (!Wire.bool(this.workspace.machineData, 'can_deploy')) Text(Wire.text(this.workspace.machineData, 'deploy_reason', tr('admin_node_cannot_deploy')))
            .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
          Button({ type: ButtonType.Circle }) { if (IconActions.busy(Wire.bool(this.workspace.machineData, 'deploying') || this.workspace.writeState === 'running' ? 'admin_node_deploying' : 'admin_redeploy')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(Wire.bool(this.workspace.machineData, 'deploying') || this.workspace.writeState === 'running' ? 'admin_node_deploying' : 'admin_redeploy')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(Wire.bool(this.workspace.machineData, 'deploying') || this.workspace.writeState === 'running' ? 'admin_node_deploying' : 'admin_redeploy', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(Wire.bool(this.workspace.machineData, 'deploying') || this.workspace.writeState === 'running' ? 'admin_node_deploying' : 'admin_redeploy')).bindTips(tr(Wire.bool(this.workspace.machineData, 'deploying') || this.workspace.writeState === 'running' ? 'admin_node_deploying' : 'admin_redeploy')).enabled(this.writeAllowed() && Wire.bool(this.workspace.machineData, 'can_deploy') && !Wire.bool(this.workspace.machineData, 'deploying'))
            .onClick(() => this.confirmNodeDeploy()).id('admin.node_update.deploy')
        } else if (!this.workspace.loading) Text(tr('admin_machine_unavailable')).fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  @Builder private hubUpdateContent() {
    Column({ space: Tokens.section }) {
      Column({ space: Tokens.gap }) {
        if (Object.keys(this.workspace.hubUpdate).length) {
          Text(tr('admin_hub_version') + ': ' + Wire.text(this.workspace.hubUpdate, 'version') + ' · ' + Wire.text(this.workspace.hubUpdate, 'channel')).fontSize(Tokens.body).width('100%')
          Text(tr('admin_hub_state') + ': ' + tr('admin_hub_state_' + Wire.text(this.workspace.hubUpdate, 'state', 'idle')) + ' · ' + Wire.text(this.workspace.hubUpdate, 'progress')).fontSize(Tokens.meta).width('100%')
          if (Wire.text(this.workspace.hubUpdate, 'last_check')) Text(tr('admin_last_check') + ': ' + Wire.text(this.workspace.hubUpdate, 'last_check')).fontSize(Tokens.meta).width('100%')
          if (Object.keys(Wire.object(this.workspace.hubUpdate, 'latest')).length) {
            Text(tr('admin_latest_version') + ': ' + Wire.text(Wire.object(this.workspace.hubUpdate, 'latest'), 'version') + (Wire.bool(Wire.object(this.workspace.hubUpdate, 'latest'), 'newer') ? ' · ' + tr('admin_update_available') : ''))
              .fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
            if (Wire.text(Wire.object(this.workspace.hubUpdate, 'latest'), 'notes')) Text(Wire.text(Wire.object(this.workspace.hubUpdate, 'latest'), 'notes')).fontSize(Tokens.meta).width('100%')
            if (Wire.text(Wire.object(this.workspace.hubUpdate, 'latest'), 'held')) Text(tr('admin_update_held') + ': ' + Wire.text(Wire.object(this.workspace.hubUpdate, 'latest'), 'held')).fontSize(Tokens.meta)
              .fontColor($r('app.color.persona_danger')).width('100%')
            if (Wire.bool(Wire.object(this.workspace.hubUpdate, 'latest'), 'bad')) Text(tr('admin_update_rolled_back')).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%')
          }
          if (Wire.text(this.workspace.hubUpdate, 'error')) Text(Wire.text(this.workspace.hubUpdate, 'error')).fontSize(Tokens.meta).fontColor($r('app.color.persona_danger')).width('100%')
          if (this.workspace.hubBusyLabels().length) Text(tr('admin_hub_busy') + ': ' + this.workspace.hubBusyLabels().join(', ')).fontSize(Tokens.meta).width('100%')
          Row({ space: Tokens.gap }) {
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('admin_hub_check')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('admin_hub_check', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_hub_check')).bindTips(tr('admin_hub_check')).enabled(this.writeAllowed() && !this.workspace.hubUpdateWorking())
              .onClick(() => this.confirmHubCheck()).id('admin.hub_update.check')
          }.width('100%')
          if (Wire.bool(this.workspace.hubUpdate, 'can_update') && Wire.bool(Wire.object(this.workspace.hubUpdate, 'latest'), 'newer') && !this.workspace.hubUpdateWorking()) {
            Row({ space: Tokens.gap }) {
              Button({ type: ButtonType.Circle }) { Image($r('sys.media.ohos_ic_public_more')).width(Tokens.symbol).height(Tokens.symbol).fillColor(Colors.link) }
                .attributeModifier(ButtonStyles.icon).accessibilityText(tr('admin_hub_update')).enabled(this.writeAllowed())
                .bindMenu(this.hubUpdateMenu).id('admin.hub_update.options')
            }.width('100%')
          } else if (!Wire.bool(this.workspace.hubUpdate, 'can_update')) Text(Wire.text(this.workspace.hubUpdate, 'why_not', tr('admin_hub_cannot_update')))
            .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
          Row({ space: Tokens.gap }) {
            Text(tr('admin_auto_update')).layoutWeight(1).fontSize(Tokens.body)
            Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.workspace.hubUpdate, 'auto') }).enabled(this.writeAllowed() && !this.workspace.hubUpdateWorking())
              .onChange((value: boolean) => this.confirmHubSetting('auto', value)).id('admin.hub_update.auto')
          }.width('100%')
          Row({ space: Tokens.gap }) {
            Text(tr('admin_auto_update_nodes')).layoutWeight(1).fontSize(Tokens.body)
            Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.workspace.hubUpdate, 'auto_update_nodes') }).enabled(this.writeAllowed() && !this.workspace.hubUpdateWorking())
              .onChange((value: boolean) => this.confirmHubSetting('auto_update_nodes', value)).id('admin.hub_update.auto_nodes')
          }.width('100%')
        } else if (!this.workspace.loading) Text(this.workspace.unsupported ? tr('admin_unsupported') : tr('admin_hub_no_status'))
          .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)

      Column({ space: Tokens.gap }) {
        this.title('admin_hub_update_history')
        if (this.workspace.updateHistoryUnsupported) Text(tr('admin_history_unsupported')).fontSize(Tokens.meta).width('100%')
        else if (this.workspace.updateHistory.length) ForEach(this.workspace.updateHistory, (record: Record<string, Object>) => {
          Column({ space: Tokens.fine }) {
            Text(Wire.text(record, 'result', Wire.text(record, 'state')) + (Wire.bool(record, 'auto') ? ' · ' + tr('admin_auto_update') : ''))
              .fontSize(Tokens.body).fontWeight(FontWeight.Medium).width('100%')
            Text(Wire.text(record, 'from_version') + ' → ' + Wire.text(record, 'to_version') + ' · ' + Wire.text(record, 'ended', Wire.text(record, 'started')))
              .fontSize(Tokens.meta).fontColor($r('sys.color.font_secondary')).width('100%')
            if (Wire.text(record, 'reason')) Text(Wire.text(record, 'reason')).fontSize(Tokens.meta).width('100%')
          }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
            .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
        }, (record: Record<string, Object>, index: number) => Wire.text(record, 'id', Wire.text(record, 'started') + ':' + String(index)))
        else if (!this.workspace.loading) Text(tr('admin_history_empty')).fontSize(Tokens.meta).width('100%')
      }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
        .backgroundColor($r('sys.color.comp_background_tertiary')).borderRadius(Tokens.inset)
    }.width('100%').alignItems(HorizontalAlign.Start)
  }

  build(): void {
    Scroll() {
      Column({ space: Tokens.section }) {
        if (this.workspace.saving || this.workspace.loading) LoadingProgress().width(Tokens.touch).height(Tokens.touch)
        this.feedback()
        if (this.page === 'assistant') this.assistantContent()
        else if (this.page === 'memory') this.memoryContent()
        else if (this.page === 'managers' && this.createManagerOnly) this.createManagerForm()
        else if (this.page === 'managers') this.managersContent()
        else if (this.page === 'launchers') this.launcherContent()
        else if (this.page === 'engine') this.engineContent()
        else if (this.page === 'node_update') this.nodeUpdateContent()
        else if (this.page === 'hub_update') this.hubUpdateContent()
        else Text(tr('admin_unsupported')).fontSize(Tokens.body).width('100%')
      }.padding(Tokens.page).width('100%').alignItems(HorizontalAlign.Start)
    }.width('100%').height('100%')
  }
}
