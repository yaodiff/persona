import { bindingPermission } from '../core/PhoneSettingsPermissions';
import { PhoneSectionTitle, PhoneStyle } from '../design/PhoneStyle';
import { nativeEnvironment } from '../platform/NativeEnvironment';
import { Symbols } from '../design/Symbols';
import { IconActions } from '../design/IconActions';
import { ButtonStyles } from '../design/Controls';
import { SelectStyles } from '../design/Controls';
import { ComposeListItemV2, ContentItemV2, LengthMetrics } from '@kit.ArkUI';
import { ClientModel } from '../models/ClientModel';
import { ManagementModel, ManagementRow, ManagementInput } from '../models/ManagementModel';
import { ResourceRow, Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { Tokens } from '../design/Tokens';
import { tr, trError } from '../design/Strings';
import { nativeNotifications, NativeNotifications } from '../platform/NativeNotifications';
import { listScheduleTemplates } from '../core/ScheduleTemplates';
import type { ScheduleTemplate } from '../core/ScheduleTemplates';
import { DeviceLook, deviceLook } from '../models/DeviceLook';
import { AgentWorkspace } from '../models/AgentWorkspace';
import { Colors } from '../design/Colors';
import { PhoneAppearanceSettings } from './PhoneAppearanceSettings';
import { DeviceLookPicker } from './DeviceLookPicker';
import { LicenseNotices } from './LicenseNotices';
import { LiveViewSettingsCard } from './LiveViewSettingsCard';
import { Haptics } from '../platform/Haptics';
import { toastCenter } from '../models/ToastCenter';
import { common } from '@kit.AbilityKit';

@ComponentV2
export struct SettingsPage {
  @Require @Param model: ClientModel;
  @Param page: string = '';
  @Param active: boolean = true;
  @Param agentWorkspace: AgentWorkspace = new AgentWorkspace();
  @Local machineOwner: ClientModel | undefined;
  @Param agentLayoutOnly: boolean = false;
  @Param phoneAppearanceRoute: string = '';
  @Param initialMachine: ResourceRow | undefined = undefined;
  @Local machineDirectoryKey: string = '';
  private loadedMachineRoute: string = '';
  private machineChoice: ResourceRow | undefined = undefined;
  private machineRevision: number = 0;
  private pairingOwnerScope: string = '';
  private mounted: boolean = false;
  @Local appearanceRevision: number = 0;
  @Event onOpen: (page: string) => void = () => {};
  @Event onDetail: (page: string, row: ResourceRow) => void = () => {};
  @Event onExtension: (extensionId: string, uiId: string, title: string) => void = () => {};
  @Event onBack: () => void = () => {};
  @Event onComputer: (row: ResourceRow) => void = () => {};
  @Event onChat: (row: ResourceRow) => void = () => {};
  @Local management: ManagementModel = new ManagementModel();
  @Local extensionConfigFieldsBindings: BoundRow<ManagementInput>[] = [];
  @Local extensionSecretFieldsBindings: BoundRow<ManagementInput>[] = [];
  @Local authFieldsBindings: BoundRow<ManagementInput>[] = [];
  @Local connectorFieldsBindings: BoundRow<ManagementInput>[] = [];
  @Local channelFieldsBindings: BoundRow<ManagementInput>[] = [];
  private inputBindingScope: string = '';
  private inputBindingOwner: Object | undefined;
  @Local channelFeatureBindings: BoundRow<Record<string, Object>>[] = [];
  private channelFeatureScope: string = '';
  @Local managementRowBindings: BoundRow<ManagementRow>[] = [];
  @Local scheduleRunBindings: BoundRow<ManagementRow>[] = [];
  private managementBindingOwner: Object | undefined;
  private managementBindingScope: string = '';
  @Local catalogRowBindings: BoundRow<ManagementRow>[] = [];
  private catalogBindingOwner: Object | undefined;
  private catalogBindingScope: string = '';
  @Local notifications: NativeNotifications = nativeNotifications;
  @Local addComputerOpen: boolean = false;
  @Local computerName: string = '';
  @Local computerOs: string = 'mac';
  @Local purposeIndex: number = 0;
  @Local modelScopeExpanded:boolean=false;
  @Local permissionMode: string = '';
  @Local effortMode: string = '';
  @Local sessionModel: string = '';
  @Local scheduleEditOpen: boolean = false;
  @Local extensionPreviewOpen: boolean = false;
  @Local licenseNoticesOpen: boolean = false;
  @Local channelTargetDraft: string = '';
  @Local look: DeviceLook = deviceLook;

  private appearanceCurrent(owner: ClientModel, revision: number, scope: string, generation: number): boolean {
    return this.mounted && this.active && this.page === 'appearance' && this.appearanceRevision === revision &&
      this.model === owner && owner.owns(generation, scope);
  }

  private applyAgentGrouping(index: number, owner: ClientModel, workspace: AgentWorkspace, revision: number,
    scope: string, generation: number): void {
    if (!this.appearanceCurrent(owner, revision, scope, generation) || this.agentWorkspace !== workspace ||
      !workspace.isBoundTo(owner) || workspace.loading) return;
    workspace.setGroupingMode(['state', 'machine', 'space'][index] || 'state');
  }

  private applyDensity(index: number, owner: ClientModel, revision: number, scope: string, generation: number): void {
    if (!this.appearanceCurrent(owner, revision, scope, generation) || !this.look.ready) return;
    this.look.setDensity(index === 1 ? 'compact' : 'standard');
  }

  private applyLanguage(index: number, owner: ClientModel, revision: number, scope: string, generation: number): void {
    if (!this.appearanceCurrent(owner, revision, scope, generation)) return;
    this.model.setLanguage(['default', 'en-US', 'zh-CN', 'ja-JP'][index]).then(() => {
      if (this.appearanceCurrent(owner, revision, scope, generation) && index === 0 && this.model.language === 'default')
        toastCenter.info(tr('language_after_restart'));
    });
  }

  private applyColorMode(index: number, owner: ClientModel, revision: number, scope: string, generation: number): void {
    if (this.appearanceCurrent(owner, revision, scope, generation)) this.model.setAppearance(index);
  }

  private applyStyle(index: number, owner: ClientModel, revision: number, scope: string, generation: number): void {
    if (this.appearanceCurrent(owner, revision, scope, generation)) this.model.setStyle(['system', 'glass', 'brief', 'board'][index] || 'system');
  }

  private agentGroupingIndex(workspace: AgentWorkspace): number {
    const index = ['state', 'machine', 'space'].indexOf(workspace.groupingMode);
    return index < 0 ? 0 : index;
  }

  aboutToAppear(): void { this.mounted = true; this.appearanceRevision++; this.syncCatalogRows(); this.syncManagementRows(); this.syncInputRows(); this.loadSettingsPage(); this.loadLook(); }
  private async loadLook(): Promise<void> {
    if (!this.mounted || !this.active || this.page !== 'appearance') return;
    const owner = this.model, scope = owner.scope(), generation = owner.connectionGeneration(), pairing = owner.pairingRevision();
    const revision = this.appearanceRevision;
    const current = (): boolean => this.mounted && this.active && this.page === 'appearance' && this.appearanceRevision === revision &&
      this.model === owner && owner.owns(generation, scope) && pairing === owner.pairingRevision();
    const context = this.getUIContext().getHostContext(); if (!context) return;
    this.model.loadNextSuggestions().catch(() => undefined);
    try { await this.look.initialize(context as common.UIAbilityContext); }
    catch (_) { if (current()) this.look.error = 'device_look_storage_failed'; return; }
    if (!current()) return;
    await this.look.bind(owner);
  }
  private basePage(): string { return this.page==='models' ? 'models_catalog' : this.page === 'extension_detail' || this.page === 'extension_catalog' ? 'extensions' : this.page === 'connector_detail' ? 'connectors' : this.page === 'channel_detail' ? 'channels' : this.page; }
  private async loadSettingsPage(): Promise<void> {
    if (!this.mounted || !this.active) return;
    if (this.page === 'machines') { await this.loadMachineDirectory(); return; }
    const page = this.page, owner = this.model, generation = owner.connectionGeneration(), scope = owner.scope(), revision = ++this.machineRevision, target = owner.selected;
    const sessionTarget = owner.target(target);
    const current = (): boolean => this.mounted && this.active && this.page === page && this.model === owner && revision === this.machineRevision && owner.owns(generation, scope);
    this.management.setAdmission(current);
    const row = new ManagementRow(target.id, target.title, target.detail, target.data());
    if (page === 'channel_detail' && !this.channelAllowed(row.id) ||
        page === 'connector_detail' && !this.connectorAllowed(row.id)) {
      this.management.selectedId = row.id;
      this.management.channelDetail = {};
      this.management.error = '';
      this.management.loading = false;
      return;
    }
    await this.management.load(this.basePage(), owner, page==='model_purpose' ? target.id : ''); if (!current()) return;
    if (page === 'extension_detail') await this.management.openExtension(owner, row);
    else if (page === 'connector_detail') this.management.openConnector(row);
    else if (page === 'channel_detail') await this.management.openChannel(owner, row);
    else if (page === 'extension_catalog') await this.management.loadExtensionCatalog(owner);
    if (!current()) return;
    this.purposeIndex = Math.max(0, this.management.catalogPurposes.findIndex((item: Record<string, Object>) => Wire.text(item, 'id') === this.management.selectedId));
    if (['session_permissions', 'session_effort', 'session_model'].includes(page) && owner.target() !== sessionTarget) return;
    this.permissionMode = Wire.text(owner.selected.data(), 'permission_mode'); this.effortMode = Wire.text(owner.selected.data(), 'effort');
    this.sessionModel = Wire.text(owner.selected.data(), 'model');
  }
  private openDetail(page: string, item?: ManagementRow): void {
    if (!this.mounted || !this.active) return;
    const row = new ResourceRow(item?.id || 'catalog', item?.title || tr('extension_market'), 'settings_detail');
    row.group = this.model.profile.id; row.manager = this.model.profile.manager; row.detail = item?.detail || ''; row.raw = JSON.stringify(item?.raw || Wire.payload([], [])); this.onDetail(page, row);
  }

  aboutToDisappear(): void { this.mounted = false; this.appearanceRevision++; this.machineRevision++; if (this.page === 'appearance') this.look.bind(undefined); }
  @Monitor('management.extensionDetailMissing') extensionRemoved(): void { if (this.mounted && this.active && this.page === 'extension_detail' && this.management.extensionDetailMissing) this.onBack(); }
  @Monitor('active') machineVisibilityChanged(): void { this.appearanceRevision++; if (!this.active) { this.machineRevision++; if (this.page === 'appearance') this.look.bind(undefined); } else if (this.mounted) { this.loadSettingsPage(); this.loadLook(); } }
  @Monitor('model.connected') settingsConnectionChanged(): void { if (this.mounted && this.active) { if (this.model.connected) this.loadSettingsPage(); this.loadLook(); } }
  @Monitor('model.profile', 'model.forgottenEpoch', 'model.connected') scheduleOwnerChanged(): void { this.appearanceRevision++; this.syncCatalogRows(); this.syncManagementRows(); this.management.scheduleReceiptChat(this.model); this.loadLook(); }
  @Monitor('page') settingsPageChanged(): void { this.appearanceRevision++; if (this.mounted && this.active && ['session_model', 'session_effort', 'session_permissions'].includes(this.page)) this.loadSettingsPage(); }
  @Monitor('management.catalogRows') catalogRowsChanged(): void { this.syncCatalogRows(); }

  @Monitor('management.rows', 'management.scheduleRuns', 'management.channelDetail', 'management.selectedId', 'page', 'model.profile', 'machineOwner')
  managementRowsChanged(): void { this.syncManagementRows(); }

  @Monitor('management.extensionConfigFields', 'management.extensionSecretFields', 'management.authFields', 'management.connectorFields', 'management.channelFields', 'management.selectedId', 'page', 'model.profile')
  inputRowsChanged(): void { this.syncInputRows(); }

  private syncInputRows(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager, this.page, this.management.selectedId]);
    this.extensionConfigFieldsBindings = reconcileBoundRows(this.extensionConfigFieldsBindings, this.management.extensionConfigFields,
      (field: ManagementInput) => field.key, this.inputBindingOwner, this.model, this.inputBindingScope, scope);
    this.extensionSecretFieldsBindings = reconcileBoundRows(this.extensionSecretFieldsBindings, this.management.extensionSecretFields,
      (field: ManagementInput) => field.key, this.inputBindingOwner, this.model, this.inputBindingScope, scope);
    this.authFieldsBindings = reconcileBoundRows(this.authFieldsBindings, this.management.authFields,
      (field: ManagementInput) => field.key, this.inputBindingOwner, this.model, this.inputBindingScope, scope);
    this.connectorFieldsBindings = reconcileBoundRows(this.connectorFieldsBindings, this.management.connectorFields,
      (field: ManagementInput) => field.key, this.inputBindingOwner, this.model, this.inputBindingScope, scope);
    this.channelFieldsBindings = reconcileBoundRows(this.channelFieldsBindings, this.management.channelFields,
      (field: ManagementInput) => field.key, this.inputBindingOwner, this.model, this.inputBindingScope, scope);
    this.inputBindingOwner = this.model; this.inputBindingScope = scope;
  }

  private syncManagementRows(): void {
    const owner = this.page === 'machines' ? this.machineOwner || this.model : this.model;
    const scope = JSON.stringify([owner.scope(), owner.profile.id, owner.profile.manager, this.page]);
    this.managementRowBindings = reconcileBoundRows(this.managementRowBindings, this.management.rows,
      (row: ManagementRow) => row.id, this.managementBindingOwner, owner, this.managementBindingScope, scope);
    this.scheduleRunBindings = reconcileBoundRows(this.scheduleRunBindings, this.management.scheduleRuns,
      (row: ManagementRow) => row.id, this.managementBindingOwner, owner, this.managementBindingScope, scope);
    const featureScope = JSON.stringify([scope, this.management.selectedId]);
    this.channelFeatureBindings = reconcileBoundRows(this.channelFeatureBindings, Wire.rows(this.management.channelData(), 'features'),
      (feature: Record<string, Object>) => Wire.text(feature, 'id'), this.managementBindingOwner, owner,
      this.channelFeatureScope, featureScope);
    this.channelFeatureScope = featureScope;
    this.managementBindingOwner = owner; this.managementBindingScope = scope;
  }

  private syncCatalogRows(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager, 'extension_catalog']);
    this.catalogRowBindings = reconcileBoundRows(this.catalogRowBindings, this.management.catalogRows,
      (row: ManagementRow) => Wire.text(row.raw, 'market') + '.' + row.id, this.catalogBindingOwner, this.model,
      this.catalogBindingScope, scope);
    this.catalogBindingOwner = this.model; this.catalogBindingScope = scope;
  }
  @Monitor('initialMachine') routedMachineChanged():void {
    if (this.mounted && this.active && this.page==='machines' && (this.initialMachine?.key() || '')!==this.loadedMachineRoute)
      this.loadMachineDirectory();
  }

  private sameRoutedMachine(candidate:ResourceRow,target:ResourceRow):boolean {
    const owner=this.model.profile;
    const group=candidate.group || owner.id, targetGroup=target.group || owner.id;
    const manager=candidate.manager || (group===owner.id ? owner.manager : '');
    const targetManager=target.manager || (targetGroup===owner.id ? owner.manager : '');
    return candidate.id===target.id && group===targetGroup && manager===targetManager;
  }

  private clearMachineChoice():void {
    this.machineChoice=undefined; this.machineOwner=undefined; this.machineDirectoryKey='';
    this.management.rows=[]; this.management.selectedId=''; this.management.pairing={}; this.management.error='';
    this.management.setAdmission(():boolean => false);
    this.pairingOwnerScope=''; this.computerName=''; this.computerOs='mac'; this.addComputerOpen=false;
  }

  private machineOptions(): ResourceRow[] { return this.model.directoryReady ? this.model.directoryMachines : this.model.machines; }
  private machineLabel(row: ResourceRow): string { return [this.model.groupName(row), row.manager, row.title || row.id].filter((value: string) => !!value).join(' · '); }
  private async loadMachineDirectory(): Promise<void> {
    const owner=this.model, generation=owner.connectionGeneration(), scope=owner.scope(), revision=++this.machineRevision;
    const route=this.initialMachine, routeKey=route?.key() || '';
    if (this.loadedMachineRoute!==routeKey) {
      this.clearMachineChoice();
      this.loadedMachineRoute=routeKey;
    }
    const current=():boolean => this.mounted && this.active && revision===this.machineRevision &&
      owner===this.model && owner.owns(generation,scope) && routeKey===(this.initialMachine?.key() || '');
    let refreshFailed=false;
    try { await owner.refreshDirectory(); } catch (_) { refreshFailed=true; }
    if (!current()) return;
    const list=this.machineOptions();
    let selected=list.find((row:ResourceRow) => row.key()===this.machineDirectoryKey);
    if (!selected && route?.id) selected=list.find((row:ResourceRow) => this.sameRoutedMachine(row,route));
    if (!selected && !route?.id) selected=list.find((row:ResourceRow) => row.key()===this.model.selected.key()) || list[0];
    if (selected) { await this.chooseMachine(selected); return; }
    this.clearMachineChoice(); this.management.loading=false;
    if (route?.id || refreshFailed) { this.management.error=refreshFailed ? 'offline' : 'admin_machine_unavailable'; return; }
    this.machineOwner=owner;
    this.management.setAdmission(():boolean => current() && this.machineOwner===owner);
    await this.management.load('machines',owner);
  }
  private async chooseMachine(row: ResourceRow): Promise<void> {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), revision = ++this.machineRevision;
    if (this.machineDirectoryKey !== row.key()) { this.management.pairing = {}; this.pairingOwnerScope = ''; this.computerName = ''; this.computerOs = 'mac'; this.addComputerOpen = false; }
    this.machineChoice = row; this.machineDirectoryKey = row.key(); this.machineOwner = undefined; this.management.rows = []; this.management.selectedId = ''; this.management.error = ''; this.management.loading = true;
    this.management.setAdmission((): boolean => false);
    try {
      const owner = await this.model.viewFor(row);
      if (!this.mounted || !this.active || revision !== this.machineRevision || !this.model.owns(generation, scope)) return;
      this.machineOwner = owner;
      this.management.setAdmission((): boolean => this.mounted && this.active && revision === this.machineRevision && this.machineOwner === owner && this.machineDirectoryKey === row.key() && this.model.owns(generation, scope));
      await this.management.load('machines', owner);
      if (!this.mounted || !this.active || revision !== this.machineRevision || !this.model.owns(generation, scope)) return;
      this.management.selectedId = row.id;
    } catch (_) { if (this.mounted && this.active && revision === this.machineRevision && this.model.owns(generation, scope)) this.management.error = 'offline'; }
    finally { if (revision === this.machineRevision) this.management.loading = false; }
  }

  private purposeLabel(item: Record<string, Object>): string { return Wire.text(item, 'name', Wire.text(item, 'id')); }
  private selectOptions(values: string[]): SelectOption[] {
    return values.map((value: string): SelectOption => {
      const option: SelectOption = { value: value };
      return option;
    });
  }
  private providerOptions(): SelectOption[] {
    const values: SelectOption[] = [];
    const inherited: SelectOption = { value: tr('automatic') }; values.push(inherited);
    for (const provider of this.purposeProviders()) {
      const option: SelectOption = { value: Wire.text(provider, 'name', Wire.text(provider, 'id')) };
      values.push(option);
    }
    return values;
  }
  private modeOptionsFor(field: string): string[] {
    if (field === 'model') return this.management.providerModels.map((item: Record<string, Object>) => Wire.text(item, 'id'));
    return field === 'permission_mode' ? this.modeOptions() : Wire.strings(this.model.selected.data(), 'efforts');
  }
  private modeName(field: string, value: string): string {
    if (field === 'permission_mode') {
      return ['default', 'acceptEdits', 'plan', 'auto', 'bypassPermissions'].includes(value)
        ? tr('assistant_feed_permission_' + value) : value;
    }
    if (field === 'model') {
      const item = this.management.providerModels.find((option: Record<string, Object>) => Wire.text(option, 'id') === value);
      return item ? Wire.text(item, 'name', value) || value : value;
    }
    if (['none', 'minimal', 'ultra'].includes(value)) return tr('session_effort_' + value);
    return tr(value);
  }
  private modeLabel(field: string): string {
    return tr(field === 'model' ? 'model' : field === 'effort' ? 'reasoning_effort' : 'permission_mode');
  }
  private modeDraft(field: string): string {
    return field === 'model' ? this.sessionModel : field === 'effort' ? this.effortMode : this.permissionMode;
  }
  private currentModeName(field: string): string {
    const value = Wire.text(this.model.selected.data(), field);
    if (!value && field === 'model') return tr('unknown');
    return value ? this.modeName(field, value) : tr('automatic');
  }
  private purposeProviders(): Record<string, Object>[] {
    const purpose = this.management.purpose, kind = Wire.text(purpose, 'kind', 'launcher');
    const ids = Wire.strings(purpose, 'provider_ids');
    return this.management.providers.filter((item: Record<string, Object>) =>
      ids.length ? ids.includes(Wire.text(item, 'id')) : Wire.text(item, 'kind', 'launcher') === kind);
  }
  private providerIndex(): number {
    const id = this.management.purposeDraft.provider;
    if (!id) return 0;
    const index = this.purposeProviders().findIndex((item: Record<string, Object>) => Wire.text(item, 'id') === id);
    return index < 0 ? 0 : index + 1;
  }
  private modelIndex(): number {
    const index = this.management.providerModels.findIndex((item: Record<string, Object>) =>
      Wire.text(item, 'id') === this.management.purposeDraft.model);
    return Math.max(0, index);
  }
  private effortOptions(): string[] {
    const model = this.management.providerModels.find((item: Record<string, Object>) =>
      Wire.text(item, 'id') === this.management.purposeDraft.model);
    return Wire.strings(model || {}, 'efforts');
  }
  private modelProvider(): Record<string, Object> {
    const id = this.management.purposeDraft.provider || Wire.text(Wire.object(this.management.purpose, 'effective'), 'provider');
    return this.management.providers.find((item: Record<string, Object>) => Wire.text(item, 'id') === id) || {};
  }
  private fieldSupported(key: string): boolean {
    const fields = this.management.purpose['fields'];
    return Array.isArray(fields) ? (fields as Object[]).includes(key) : key === 'provider' || key === 'model';
  }
  private purposeChanged(): boolean {
    return this.management.purposeChanged();
  }
  private purposePermissionOptions():string[] {
    const options=this.management.permissionChoices(),value=this.management.purposeDraft.permissionMode;
    if (!options.length) return [];
    return [''].concat(options,value && !options.includes(value) ? [value] : []);
  }
  private purposePermissionName(value:string):string {
    return value ? this.modeName('permission_mode',value) : tr(this.management.selectedId==='inbox.agent' ? 'phone_default_permissions' : 'default');
  }
  private modeOptions(): string[] {
    const values = ['default', 'acceptEdits', 'plan', 'auto'];
    if (Wire.bool(this.model.selected.data(), 'bypass_available')) values.push('bypassPermissions');
    return values;
  }
  private machineRow(): ManagementRow | undefined {
    return this.management.rows.find((item: ManagementRow) => item.id === this.management.selectedId);
  }
  private screenMachine(): ResourceRow | undefined {
    const selected = this.machineRow();
    if (!selected || !this.machineOwner) return undefined;
    const local=this.machineOwner.machines.find((row:ResourceRow) => row.id===selected.id && row.key()===this.machineDirectoryKey);
    if (local?.group) return local;
    // The per-manager list omits its group. The aggregate directory always supplies it.
    const choice=this.machineChoice, profile=this.machineOwner.profile;
    if (!choice || !profile || choice.id!==selected.id || choice.group && choice.group!==profile.id ||
      choice.manager && choice.manager!==profile.manager) return undefined;
    const row=this.machineOwner.machines.find((r:ResourceRow) => r.id===selected.id && !r.group && (!r.manager || r.manager===profile.manager));
    if (!row) return undefined;
    const scoped=ResourceRow.from(row.data(),'machine');
    scoped.group=profile.id; scoped.manager=profile.manager; return scoped;
  }
  private canOpenScreen(): boolean {
    const row = this.screenMachine(); if (!this.mounted || !this.active || !row || !this.machineOwner?.connected || this.model.demo || this.management.loading) return false;
    const data = row.data(), os = Wire.text(data, 'os', Wire.text(Wire.object(data, 'system'), 'os'));
    return ['', 'mac', 'darwin'].includes(os) && Wire.bool(data, 'online') && !Wire.bool(data, 'pending') && !Wire.bool(data, 'pending_confirm') && ['', 'confirmed', 'personal', 'work'].includes(Wire.text(data, 'trust'));
  }
  private rowEnabled(row: ManagementRow): boolean { return Wire.bool(row.raw, 'enabled'); }
  private canPairComputer(): boolean { return this.mounted && this.active && !!this.machineOwner?.connected && !this.management.loading && (!this.machineDirectoryKey || !!this.screenMachine()); }
  private async createComputerPairing(): Promise<void> {
    const owner = this.machineOwner; if (!owner || !this.canPairComputer()) return;
    const generation = owner.connectionGeneration(), scope = owner.scope(), revision = this.machineRevision;
    const ok = await this.management.createPairing(owner, this.computerName, this.computerOs);
    if (ok && this.mounted && this.active && this.machineOwner === owner && revision === this.machineRevision && owner.owns(generation, scope)) { const selected = owner.machines.find((row: ResourceRow) => row.key() === this.machineDirectoryKey); if (selected) this.management.selectedId = selected.id; this.pairingOwnerScope = scope; this.computerName = ''; this.addComputerOpen = false; }
  }
  private canRenew(): boolean {
    return this.canPairComputer() && this.pairingOwnerScope === this.machineOwner?.scope() && this.management.canRenewPairing();
  }
  private async saveTriage(key: string, value: boolean): Promise<void> {
    const next: Record<string, Object> = Wire.payload([], []);
    next['enabled'] = Wire.bool(this.management.data, 'enabled');
    next['auto_continue'] = Wire.bool(this.management.data, 'auto_continue');
    next['wake'] = Wire.bool(this.management.data, 'wake');
    next['decide'] = Wire.bool(this.management.data, 'decide');
    next['reply_reminder'] = Wire.bool(this.management.data, 'reply_reminder', true);
    if (!Object.keys(this.management.data).includes(key)) return;
    next[key] = value;
    await this.management.setTriage(this.model, next);
  }
  private confirmMachine(row: ManagementRow, trust: boolean): void {
    if (!this.machineOwner || !this.screenMachine() || this.screenMachine()?.id !== row.id || this.management.loading) return;
    const owner = this.machineOwner, scope = owner.scope(), generation = owner.connectionGeneration(), selected = this.machineDirectoryKey;
    this.getUIContext().showAlertDialog({ title: tr(trust ? 'confirm_trust' : 'reject'),
      message: row.title + '\n' + row.id,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr(trust ? 'confirm_computer' : 'reject'), action: () => {
        if (this.mounted && this.active && selected === this.machineDirectoryKey && owner.owns(generation, scope)) this.management.trustMachine(owner, row, trust);
      } } });
  }
  private confirmForget(): void {
    const id = this.model.profile.id, scope = this.model.scope();
    this.getUIContext().showAlertDialog({ title: tr('forget_hub'), message: this.model.profile.name + '\n' + this.model.profile.baseUrl + '\n\n' + tr('forget_hub_notice'),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('forget_hub'), action: () => {
        if (this.model.scope() === scope) this.model.forgetProfile(id).catch(() => this.management.error = 'failed');
      } } });
  }
  private saveMode(field: string): void {
    const owner = this.model, row = owner.selected, scope = owner.scope(), generation = owner.connectionGeneration(),
      target = owner.target(row), revision = this.machineRevision, value = this.modeDraft(field);
    if (!this.management.canEditSessionMode(owner, field) || !this.modeOptionsFor(field).includes(value)) return;
    this.getUIContext().showAlertDialog({ title: tr('confirm'),
      message: row.title + '\n' + this.modeLabel(field) + ': ' + this.modeName(field, value),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('save'), action: () => {
        if (this.mounted && this.active && this.model === owner && this.machineRevision === revision &&
          owner.owns(generation, scope) && owner.target() === target) this.management.saveMode(owner, field, value);
      } } });
  }
  private showError(): boolean { return this.management.error.length > 0; }

  private selectedSchedule(): ManagementRow | undefined {
    return this.management.rows.find((row: ManagementRow) => row.id === this.management.scheduleDraft.id);
  }
  private selectedConnector(): ManagementRow | undefined {
    return this.management.rows.find((row: ManagementRow) => row.id === this.management.selectedId);
  }
  private selectedChannel(): ManagementRow | undefined {
    return this.management.rows.find((row: ManagementRow) => row.id === this.management.selectedId);
  }
  private catalogMarketName(): string {
    const market = this.management.catalogMarkets.find((item: Record<string, Object>) => Wire.text(item, 'id') === this.management.catalogMarket);
    return Wire.text(market || {}, 'name', this.management.catalogMarket || tr('all'));
  }
  private catalogMarketOptions(): string[] {
    return [tr('all')].concat(this.management.catalogMarkets.map((market: Record<string, Object>) => Wire.text(market, 'name', Wire.text(market, 'id'))));
  }
  private channelTargetIndex(): number {
    return ['main', 'new', 'conversation'].indexOf(this.channelTargetDraft || Wire.text(Wire.object(this.management.channelData(), 'target'), 'kind', 'main'));
  }
  private channelTargetLabel(kind: string): string {
    if (kind === 'main') return tr('main_chat');
    if (kind === 'new') return tr('new_chat');
    if (kind === 'agent') return tr('agent');
    return tr('conversation');
  }
  private channelBound(): boolean {
    const account = Wire.object(this.management.channelData(), 'account');
    return Object.keys(account).length > 0 || Wire.text(this.management.channelData(), 'state') === 'connected';
  }
  private channelWaitingText(): string {
    const waiting = Wire.object(this.management.channelDetail, 'waiting');
    if (!Object.keys(waiting).length) return '';
    return String(Wire.number(waiting, 'answers')) + ' · ' + String(Wire.number(waiting, 'asks'));
  }
  private channelConversationIndex(): number {
    const target = Wire.object(this.management.channelData(), 'target');
    const index = this.model.conversations.findIndex((row: ResourceRow) => row.machine === Wire.text(target, 'machine_id') &&
      (row.session || row.id) === Wire.text(target, 'session_id'));
    return Math.max(0, index);
  }
  private selectChannelTarget(index: number): void {
    const id = this.management.selectedId, kind = ['main', 'new', 'conversation'][index];
    this.channelTargetDraft = kind === 'conversation' ? kind : '';
    if (kind && kind !== 'conversation') this.management.setChannelTarget(this.model, id, kind);
  }
  private selectChannelConversation(index: number): void {
    const row = this.model.conversations[index];
    const id = this.management.selectedId;
    if (row) this.management.setChannelTarget(this.model, id, 'conversation', row.machine, row.session || row.id).then((ok: boolean) => {
      if (ok && this.management.selectedId === id) this.channelTargetDraft = '';
    });
  }
  private async openCatalog(): Promise<void> {
    this.openDetail('extension_catalog');
  }
  private previewCatalogRow(row: ManagementRow): void {
    this.management.previewExtension(this.model, row).then((ok: boolean) => {
      if (ok && Object.keys(this.management.preview).length > 0) this.extensionPreviewOpen = true;
    });
  }
  private refreshOperation(): void {
    this.management.refreshExtensionOperation(this.model).then((ok: boolean) => {
      if (ok && Object.keys(this.management.preview).length > 0) this.extensionPreviewOpen = true;
    });
  }
  private confirmScheduleSave(): void {
    const scope = this.model.scope(), owner = this.model.profile.manager, id = this.management.scheduleDraft.id;
    const target = JSON.stringify([scope, owner, id]);
    this.getUIContext().showAlertDialog({ title: tr('confirm'), message: this.management.scheduleDraft.title,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('save'), action: () => {
        if (this.model.scope() !== scope || this.model.profile.manager !== owner || JSON.stringify([this.model.scope(), this.model.profile.manager, this.management.scheduleDraft.id]) !== target) return;
        this.management.saveSchedule(this.model).then((ok: boolean) => { if (ok) this.scheduleEditOpen = false; });
      } } });
  }
  private confirmScheduleDelete(row: ManagementRow): void {
    const scope = this.model.scope(), owner = this.model.profile.manager, id = row.id;
    this.getUIContext().showAlertDialog({ title: tr('delete'), message: row.title,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('delete'), action: () => {
        if (this.model.scope() === scope && this.model.profile.manager === owner && this.management.rows.some((item: ManagementRow) => item.id === id))
          this.management.deleteSchedule(this.model, row);
      } } });
  }
  private confirmScheduleRun(row: ManagementRow): void {
    const scope = this.model.scope(), owner = this.model.profile.manager, id = row.id;
    this.getUIContext().showAlertDialog({ title: tr('run_now'), message: row.title,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('run_now'), action: () => {
        if (this.model.scope() === scope && this.model.profile.manager === owner && this.management.rows.some((item: ManagementRow) => item.id === id))
          this.management.runSchedule(this.model, row).then((): void => {
            if (!this.mounted || !this.active || this.model.scope() !== scope || this.model.profile.manager !== owner) return;
            const target = this.management.scheduleReceiptChat(this.model); if (target) this.onChat(target);
          });
      } } });
  }
  private confirmExtensionSave(): void {
    const revision = this.machineRevision, connection = this.model.connectionGeneration(), boundModel = this.model;
    const scope = this.model.scope(), owner = this.model.profile.manager, id = this.management.selectedId;
    this.getUIContext().showAlertDialog({ title: tr('save'), message: Wire.text(this.management.extensionDetail, 'name', id),
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('save'), action: () => {
        if (this.mounted && this.active && this.machineRevision === revision && this.model === boundModel && this.model.connectionGeneration() === connection && this.model.scope() === scope && this.model.profile.manager === owner && this.management.selectedId === id)
          this.management.saveExtensionConfig(this.model);
      } } });
  }
  private confirmExtensionRemove(): void {
    const revision = this.machineRevision, connection = this.model.connectionGeneration(), boundModel = this.model;
    const scope = this.model.scope(), owner = this.model.profile.manager, id = this.management.selectedId;
    this.getUIContext().showAlertDialog({ title: tr('remove'), message: Wire.text(this.management.extensionDetail, 'name', id),
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('remove'), action: () => {
        if (this.mounted && this.active && this.machineRevision === revision && this.model === boundModel && this.model.connectionGeneration() === connection && this.model.scope() === scope && this.model.profile.manager === owner && this.management.selectedId === id)
          this.management.removeExtension(this.model).then((done: boolean) => {
            if (done && this.mounted && this.active && this.machineRevision === revision && this.model === boundModel && this.model.connectionGeneration() === connection && this.model.scope() === scope && this.model.profile.manager === owner && !Object.keys(this.management.operation).length) this.onBack();
          });
      } } });
  }
  private confirmConnectorLogout(): void {
    const revision = this.machineRevision, connection = this.model.connectionGeneration(), boundModel = this.model;
    const row = this.selectedConnector(); if (!row) return;
    const scope = this.model.scope(), owner = this.model.profile.manager, id = row.id;
    this.getUIContext().showAlertDialog({ title: tr('sign_out'), message: row.title,
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('sign_out'), action: () => {
        if (this.mounted && this.active && this.machineRevision === revision && this.model === boundModel && this.model.connectionGeneration() === connection && this.model.scope() === scope && this.model.profile.manager === owner && this.management.selectedId === id)
          this.management.connectorLogout(this.model, row);
      } } });
  }
  private confirmChannelUnbind(): void {
    const revision = this.machineRevision, connection = this.model.connectionGeneration(), boundModel = this.model;
    const channel = this.management.channelData(), id = this.management.selectedId, scope = this.model.scope(), owner = this.model.profile.manager;
    this.getUIContext().showAlertDialog({ title: tr('unbind'), message: Wire.text(channel, 'name', id),
      primaryButton: { value: tr('cancel'), action: () => {} }, secondaryButton: { value: tr('unbind'), action: () => {
        if (this.mounted && this.active && this.machineRevision === revision && this.model === boundModel && this.model.connectionGeneration() === connection && this.model.scope() === scope && this.model.profile.manager === owner && this.management.selectedId === id)
          this.management.channelUnbind(this.model, id);
      } } });
  }

  @Builder status() {
    if (this.management.loading) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
    if (this.showError()) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
  }

  private purposeSummary(row:Record<string,Object>):string {
    const effective=Wire.object(row,'effective'),mode=Wire.text(row,'kind')==='launcher' ?
      (Wire.text(row,'id')==='inbox.agent' ? bindingPermission('inbox.agent',Wire.text(Wire.object(row,'override'),'permission_mode')) || 'default' :
        Wire.text(effective,'permission_mode')) : '';
    const effort=Wire.text(effective,'effort');
    return [Wire.text(effective,'model'),effort ? tr(effort) : '',mode ? this.purposePermissionName(mode) : '']
      .filter((value:string)=>!!value).join(' · ') || tr(Wire.text(effective,'provider') ? 'automatic' : 'phone_model_unset');
  }
  private purposeInheritance(row:Record<string,Object>):string {
    const parent=Wire.text(row,'parent');
    if (!Wire.strings(row,'fields').length || (parent!=='agent.new' && Wire.text(row,'id')!=='assistant.supervise')) return '';
    const binding=Wire.object(row,'override');
    return tr(Object.keys(binding).some((key:string)=>!!Wire.text(binding,key)) ? 'phone_model_configured' :
      parent==='agent.new' ? 'phone_follow_agents' : 'phone_model_follow_main');
  }
  private openPurpose(row:Record<string,Object>):void {
    const id=Wire.text(row,'id');
    if (!id || !this.management.modelCatalogRows().some((entry:Record<string,Object>)=>Wire.text(entry,'id')===id)) return;
    this.openDetail('model_purpose',new ManagementRow(id,this.purposeLabel(row),Wire.text(row,'description'),row));
  }
  @Builder modelCatalogPage() {
    List({space:8}) {
      if (this.management.loading) ListItem() { LoadingProgress().height(30).width(30) }
      if (this.management.error) ListItem() { Column({space:6}) {
        Text(trError(this.management.error)).fontColor(Colors.danger)
        Button(tr('retry'),{buttonStyle:ButtonStyleMode.TEXTUAL}).onClick(()=>{this.loadSettingsPage();})
      } }
      if (this.management.modelCatalogRows().length) {
        ListItem() { PhoneSectionTitle({text:tr('phone_model_choose_uses')}) }
        ForEach(this.management.modelCatalogRows(),(row:Record<string,Object>)=>{
          ListItem() {
            Button({type:ButtonType.Normal}) {
              Row({space:10}) {
                Column({space:4}) {
                  Text(this.purposeLabel(row)).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Medium).fontColor(Colors.ink).maxLines(1)
                  if (Wire.text(row,'description').trim()) Text(Wire.text(row,'description').trim()).fontSize(13).fontColor(Colors.secondary)
                  Text(this.purposeSummary(row)).fontSize(15).maxLines(1).fontColor(Colors.link)
                  if (this.purposeInheritance(row)) Text(this.purposeInheritance(row)).fontSize(13).fontColor(Colors.secondary)
                  if (Wire.text(row,'status')) Text(Wire.text(row,'status')).fontSize(13).fontColor(Colors.secondary)
                }.alignItems(HorizontalAlign.Start).layoutWeight(1)
                SymbolGlyph(Symbols.expand).fontSize('14vp').fontColor([Colors.secondary]).accessibilityLevel('no')
              }.width('100%')
            }.width('100%').padding({left:16,right:14,top:11,bottom:11}).backgroundColor(Colors.card(false))
              .borderRadius(PhoneStyle.cardRadius)
              .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.cardBorder(nativeEnvironment.dark),radius:PhoneStyle.cardRadius})
              .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
              .onClick(()=>{ Haptics.tap(); this.openPurpose(row); }).id('model-purpose-'+Wire.text(row,'id'))
          }
        },(row:Record<string,Object>)=>Wire.text(row,'id'))
        if (this.management.modelCategories().length) {
          ListItem() { Button(tr('phone_model_scope'),{buttonStyle:ButtonStyleMode.TEXTUAL}).onClick(()=>{this.modelScopeExpanded=!this.modelScopeExpanded;}).id('model-config-scope') }
          if (this.modelScopeExpanded) ForEach(this.management.modelCategories(),(row:Record<string,Object>)=>{
            ListItem() { Column({space:4}) {
              Text(this.purposeLabel(row)).fontSize(15).fontWeight(FontWeight.Medium).fontColor(Colors.ink)
              Text(Wire.rows(row,'uses').map((use:Record<string,Object>)=>Wire.text(use,'name')).filter((name:string)=>!!name).join('、')).fontSize(15).fontColor(Colors.secondary)
            }.alignItems(HorizontalAlign.Start).padding(12) }
          },(row:Record<string,Object>)=>Wire.text(row,'id'))
          ListItem() { Text(tr('phone_model_scope_hint')).fontSize(13).fontColor(Colors.secondary).padding(8) }
        }
      } else if (!this.management.loading && !this.management.error) { ListItem() { Text(tr('phone_model_none')).fontColor(Colors.secondary).padding(16) } }
      ListItem() { PhoneSectionTitle({text:tr('phone_settings_advanced')}) }
      ListItem() { Button(tr('phone_model_provider_keys'),{buttonStyle:ButtonStyleMode.TEXTUAL}).onClick(()=>this.onOpen('launchers')).id('settings-model-providers') }
    }.width('100%').height('100%').padding({left:16,right:16,top:12,bottom:16}).backgroundColor(Colors.grouped)
  }

  @Builder modelsPage() {
    Column() {
      if (this.page!=='model_purpose' && this.management.catalogPurposes.length > 0) {
        Select(this.selectOptions(this.management.catalogPurposes.map((item: Record<string, Object>) => this.purposeLabel(item)))).attributeModifier(SelectStyles.inline)
          .selected(this.purposeIndex).value(this.purposeLabel(this.management.catalogPurposes[this.purposeIndex] || {}))
          .onSelect((index: number) => {
            this.purposeIndex = index;
            const item = this.management.catalogPurposes[index];
            this.management.selectPurpose(this.model, Wire.text(item, 'id'));
          }).margin(Tokens.inset)
      }
      List() {
        if (Object.keys(this.management.purpose).length > 0) {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('purpose'),
            secondaryText: Wire.text(this.management.purpose, 'name', Wire.text(this.management.purpose, 'id')) }) }) }
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('model_provider'),
            secondaryText: Wire.text(this.modelProvider(), 'name', Wire.text(this.modelProvider(), 'id', tr('automatic'))) }) }) }
            .enabled(this.fieldSupported('provider'))
          if (this.fieldSupported('provider')) ListItem() {
            Select(this.providerOptions()).attributeModifier(SelectStyles.inline)
              .selected(this.providerIndex()).value(this.providerIndex() === 0 ? tr('automatic') : Wire.text(this.purposeProviders()[this.providerIndex() - 1], 'name', Wire.text(this.purposeProviders()[this.providerIndex() - 1], 'id')))
              .onSelect((index: number) => {
                const provider = index === 0 ? '' : Wire.text(this.purposeProviders()[index - 1], 'id');
                this.management.changePurposeDraft('provider', provider);
                this.management.loadPurposeModels(this.model);
              })
          }
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('model_id'),
            secondaryText: this.management.purposeDraft.model || Wire.text(Wire.object(this.management.purpose, 'effective'), 'model', tr('automatic')) }) }) }
          if (this.fieldSupported('model') && (Wire.text(this.modelProvider(), 'kind', 'launcher') !== 'launcher' || this.management.providerModels.length === 0)) {
            ListItem() { TextInput({ text: this.management.purposeDraft.model, placeholder: tr('model_id') })
              .onChange((value: string) => this.management.changePurposeDraft('model', value)) }
          } else if (this.fieldSupported('model') && this.management.providerModels.length > 0) {
            ListItem() {
              Select(this.selectOptions(this.management.providerModels.map((item: Record<string, Object>) => Wire.text(item, 'name', Wire.text(item, 'id'))))).attributeModifier(SelectStyles.inline)
                .selected(this.modelIndex()).value(Wire.text(this.management.providerModels[this.modelIndex()] || {}, 'name', this.management.purposeDraft.model))
                .onSelect((index: number) => this.management.changePurposeDraft('model', Wire.text(this.management.providerModels[index], 'id')))
            }
          }
          if (this.fieldSupported('effort') && this.effortOptions().length > 0) ListItem() {
            Select(this.selectOptions(this.effortOptions().map((item: string) => tr(item)))).attributeModifier(SelectStyles.inline)
              .value(this.management.purposeDraft.effort || tr('automatic'))
              .onSelect((index: number) => this.management.changePurposeDraft('effort', this.effortOptions()[index]))
          }
          if (Wire.text(this.management.purpose, 'status')) ListItem() {
            ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('status'), secondaryText: Wire.text(this.management.purpose, 'status') }) })
          }
          if (this.management.purposePermissionInEffect()) ListItem() {
            ComposeListItemV2({contentItemV2:new ContentItemV2({primaryText:tr('permissions'),
              secondaryText:this.purposePermissionName(this.management.purposePermissionInEffect())})})
          }.id('settings.model.permission_effective')
          if (this.purposePermissionOptions().length) ListItem() {
            Column({space:6}) {
              Text(tr(this.management.selectedId==='inbox.agent' ? 'phone_inbox_permissions':'permissions')).fontSize(16).fontColor(Colors.ink)
              Select(this.selectOptions(this.purposePermissionOptions().map((v:string)=>this.purposePermissionName(v)))).attributeModifier(SelectStyles.inline)
                .value(this.purposePermissionName(this.management.purposeDraft.permissionMode)).enabled(!this.management.saving && !this.management.purposeConflict)
                .onSelect((index:number)=>{const values=this.purposePermissionOptions();if(index>=0 && index<values.length)this.management.changePurposeDraft('permission_mode',values[index]);})
                .id('settings.model.permission')
              if (this.management.selectedId==='inbox.agent') {
                Text(tr('phone_inbox_permission_hint')).fontSize(13).fontColor(this.management.purposeDraft.permissionMode==='bypassPermissions' ? Colors.danger:Colors.secondary)
                Button(tr('phone_follow_agents'),{buttonStyle:ButtonStyleMode.TEXTUAL}).enabled(!this.management.saving && !this.management.purposeConflict)
                  .onClick(()=>{this.management.restoreInboxInheritance();this.management.loadPurposeModels(this.model);}).id('settings.model.follow_agents')
              }
            }.width('100%').alignItems(HorizontalAlign.Start).padding(Tokens.inset)
          }
          if (this.management.purposeConflict) ListItem() {
            Button(tr('retry'),{buttonStyle:ButtonStyleMode.TEXTUAL}).onClick(()=>{this.management.selectPurpose(this.model,this.management.selectedId);}).id('settings.model.reload')
          }
          ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('save')).bindTips(tr('save'))
            .enabled(this.purposeChanged() && !this.management.saving && !this.management.purposeConflict).onClick(() => this.management.savePurpose(this.model)).id('settings.model.save') }
        }
        if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      }.width('100%').layoutWeight(1)
    }.width('100%').height('100%').enabled(!this.management.saving)
  }

  @Builder sessionModePage(field: string) {
    Column() {
      if (!this.management.canEditSessionMode(this.model, field)) {
        Text(tr('no_capability')).padding(Tokens.page)
      } else {
        List() {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: this.modeLabel(field),
            secondaryText: this.currentModeName(field) }) }) }
          if (field === 'model' && this.modeOptionsFor(field).length === 0 && !this.management.loading && !this.management.error) {
            ListItem() { Text(tr('session_models_empty')).padding(Tokens.inset) }
          }
          if (this.modeOptionsFor(field).length > 0) { ListItem() {
            Select(this.selectOptions(this.modeOptionsFor(field).map((item: string) => this.modeName(field, item)))).attributeModifier(SelectStyles.inline)
              .selected(this.modeOptionsFor(field).indexOf(this.modeDraft(field)))
              .value(this.modeDraft(field) ? this.modeName(field, this.modeDraft(field)) : this.currentModeName(field))
              .onSelect((index: number) => {
                const value = this.modeOptionsFor(field)[index];
                if (!value) return;
                if (field === 'permission_mode') this.permissionMode = value;
                else if (field === 'effort') this.effortMode = value;
                else this.sessionModel = value;
              })
          } }
          ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('save')).bindTips(tr('save')).enabled(!this.management.saving &&
            !this.management.loading && this.modeOptionsFor(field).includes(this.modeDraft(field)) && this.modeDraft(field) !== Wire.text(this.model.selected.data(), field))
            .onClick(() => this.saveMode(field)) }
        }.width('100%')
      }
      if (this.management.error) Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
      if (field === 'model' && this.management.error && !this.management.loading && this.model.connected) {
        Button(tr('retry')).onClick(() => this.loadSettingsPage())
      }
    }.width('100%').height('100%')
  }

  @Builder computerForm() {
    Column({ space: Tokens.page }) {
      TextInput({ text: this.computerName, placeholder: tr('name') }).onChange((value: string) => this.computerName = value)
      Select(this.selectOptions(['Mac', 'Linux', 'Windows'])).attributeModifier(SelectStyles.inline).selected(this.computerOs === 'mac' ? 0 : this.computerOs === 'linux' ? 1 : 2).value(this.computerOs === 'mac' ? 'Mac' : this.computerOs === 'linux' ? 'Linux' : 'Windows')
        .onSelect((index: number) => this.computerOs = ['mac', 'linux', 'windows'][index])
      Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('generate_code')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('generate_code', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('generate_code')).bindTips(tr('generate_code')).enabled(this.canPairComputer() && this.computerName.trim().length > 0 && !this.management.saving)
        .onClick(() => this.createComputerPairing())
      if (this.management.error) Text(trError(this.management.error)).fontColor($r('app.color.persona_danger'))
    }.padding(Tokens.page).width('100%')
  }

  @Builder machinesPage() {
    Column() {
      List() {
        if ((this.machineOwner || this.model).features.includes('machines_add')) ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('add_computer')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('add_computer', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('add_computer')).bindTips(tr('add_computer')).enabled(this.canPairComputer()).onClick(() => this.addComputerOpen = true) }.padding({ left: Tokens.page, right: Tokens.page })
        if (this.machineOptions().length > 0) ListItem() {
          Select(this.selectOptions(this.machineOptions().map((row: ResourceRow) => this.machineLabel(row)))).attributeModifier(SelectStyles.inline)
            .selected(Math.max(0, this.machineOptions().findIndex((row: ResourceRow) => row.key() === this.machineDirectoryKey)))
            .value(this.machineOptions().find((row: ResourceRow) => row.key() === this.machineDirectoryKey) ? this.machineLabel(this.machineOptions().find((row: ResourceRow) => row.key() === this.machineDirectoryKey) as ResourceRow) : tr('machine'))
            .onSelect((index: number) => { const row = this.machineOptions()[index]; if (row) this.chooseMachine(row); }).enabled(!this.management.loading && !this.addComputerOpen).width('100%')
        }.padding({ left: Tokens.page, right: Tokens.page })
        if (this.machineRow()) {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('platform'),
            secondaryText: [Wire.text(this.machineRow()?.raw || {}, 'platform'), Wire.text(this.machineRow()?.raw || {}, 'engine'), Wire.text(this.machineRow()?.raw || {}, 'version')].filter((v: string) => !!v).join(' · ') }) }) }
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('status'),
            secondaryText: Wire.text(this.machineRow()?.raw || {}, 'error', Wire.text(this.machineRow()?.raw || {}, 'state', tr(Wire.bool(this.machineRow()?.raw || {}, 'online') ? 'online' : 'offline'))) }) }) }
          ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('computer_screen_open')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('computer_screen_open', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('computer_screen_open')).bindTips(tr('computer_screen_open')).enabled(this.canOpenScreen()).onClick(() => { const row = this.screenMachine(); if (row && this.canOpenScreen()) this.onComputer(row); }).id('machines.screen') }.padding({ left: Tokens.page, right: Tokens.page })
          if (Wire.text(this.machineRow()?.raw || {}, 'trust') === 'pending') {
            ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('confirm_computer')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('confirm_computer', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('confirm_computer')).bindTips(tr('confirm_computer')).enabled(!!this.screenMachine() && !this.management.loading).onClick(() => this.confirmMachine(this.machineRow() as ManagementRow, true)) }
            ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('reject')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('reject', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('reject')).bindTips(tr('reject')).enabled(!!this.screenMachine() && !this.management.loading).onClick(() => this.confirmMachine(this.machineRow() as ManagementRow, false)) }
          }
        }
        if (this.machineOwner && this.pairingOwnerScope === this.machineOwner.scope() && this.management.pairing['pair_url']) {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(this.management.pairing, 'name'),
            secondaryText: Wire.text(this.management.pairing, 'machine') }) }) }
          ListItem() { Text(Wire.text(this.management.pairing, 'pair_url')).copyOption(CopyOptions.LocalDevice).padding(Tokens.page) }
          ListItem() { Text(tr('expires_at') + ': ' + Wire.text(this.management.pairing, 'expires_at')).padding(Tokens.page) }
          if (this.canRenew()) ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('new_pairing_code')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('new_pairing_code', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('new_pairing_code')).bindTips(tr('new_pairing_code')).onClick(() => this.management.renewPairing(this.machineOwner || this.model)) }
          if (Wire.text(this.management.pairing, 'command')) ListItem() { Text(Wire.text(this.management.pairing, 'command')).copyOption(CopyOptions.LocalDevice).padding(Tokens.page) }
        }
        if (!this.management.rows.length && !this.management.loading) ListItem() { Text(tr('no_computer')).padding(Tokens.page) }
        if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      }.width('100%').layoutWeight(1)
    }.width('100%').height('100%')
      .bindSheet($$this.addComputerOpen, this.computerForm(), { title: { title: tr('add_computer') }, height: SheetSize.MEDIUM, showClose: true })
  }

  @Builder scheduleForm() {
    Column({ space: Tokens.page }) {
      TextInput({ text: this.management.scheduleDraft.title, placeholder: tr('title') })
        .onChange((value: string) => this.management.changeSchedule('title', value))
      TextArea({ text: this.management.scheduleDraft.prompt, placeholder: tr('task') })
        .onChange((value: string) => this.management.changeSchedule('prompt', value)).height(Tokens.inputExpanded)
      Select(this.selectOptions(['once', 'daily', 'interval'].map((value: string) => tr(value)))).attributeModifier(SelectStyles.inline)
        .selected(Math.max(0, ['once', 'daily', 'interval'].indexOf(this.management.scheduleDraft.repeat)))
        .value(tr(this.management.scheduleDraft.repeat))
        .onSelect((index: number) => this.management.changeSchedule('repeat', ['once', 'daily', 'interval'][index]))
      if (this.management.scheduleDraft.repeat === 'once') TextInput({ text: this.management.scheduleDraft.at, placeholder: tr('run_at') })
        .onChange((value: string) => this.management.changeSchedule('at', value))
      if (this.management.scheduleDraft.repeat === 'daily') {
        TextInput({ text: this.management.scheduleDraft.time, placeholder: tr('time') })
          .onChange((value: string) => this.management.changeSchedule('time', value))
        TextInput({ text: this.management.scheduleDraft.weekdays, placeholder: tr('weekdays') })
          .onChange((value: string) => this.management.changeSchedule('weekdays', value))
      }
      if (this.management.scheduleDraft.repeat === 'interval') TextInput({ text: this.management.scheduleDraft.everyMinutes, placeholder: tr('every_minutes') })
        .type(InputType.Number).onChange((value: string) => this.management.changeSchedule('every_minutes', value))
      Row() {
        ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('enabled') }) }).layoutWeight(1)
        Toggle({ type: ToggleType.Switch, isOn: this.management.scheduleDraft.enabled })
          .onChange((value: boolean) => this.management.changeSchedule('enabled', value)).accessibilityText(tr('enabled'))
      }
      Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('save')).bindTips(tr('save')).enabled(this.management.scheduleValid() && !this.management.saving).onClick(() => this.confirmScheduleSave())
      Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('cancel')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('cancel', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('cancel')).bindTips(tr('cancel')).onClick(() => { if (this.management.cancelSchedule()) this.scheduleEditOpen = false; })
      if (this.management.error) Text(trError(this.management.error)).fontColor($r('app.color.persona_danger'))
    }.padding(Tokens.page).width('100%')
  }

  @Builder scheduleCreateMenu() {
    Menu() {
      MenuItem({ content: tr('create') }).onClick(() => { this.management.beginSchedule(); this.scheduleEditOpen = true; })
      ForEach(listScheduleTemplates(), (template: ScheduleTemplate) => {
        MenuItem({ content: tr(template.titleKey) }).onClick(() => {
          if (this.management.applyScheduleTemplate(template.id, tr(template.titleKey), tr(template.promptKey))) this.scheduleEditOpen = true;
        })
      }, (template: ScheduleTemplate) => template.id)
    }
  }

  private scheduleTime(value:string): string {
    if (!value) return '';
    const date = new Date(value);
    if (!Number.isFinite(date.getTime())) return value;
    return date.toLocaleDateString() + ' · ' + String(date.getHours()).padStart(2,'0') + ':' + String(date.getMinutes()).padStart(2,'0');
  }
  @Builder scheduleMenu(row:ManagementRow) {
    Menu() {
      MenuItem({content:tr('edit')}).onClick(() => { this.management.beginSchedule(row); this.scheduleEditOpen = true; })
      MenuItem({content:tr('run_now')}).onClick(() => this.confirmScheduleRun(row))
      MenuItem({content:tr('delete')}).contentFontColor(Colors.danger).onClick(() => this.confirmScheduleDelete(row))
    }
  }
  @Builder schedulesPage() {
    List({space:6}) {
      ListItem() {
        Row({space:10}) {
          Button({type:ButtonType.Normal}) {
            Row({space:7}) { SymbolGlyph(Symbols.plus).fontSize(16).fontColor([Colors.link]); Text(tr('create')).fontSize(15).fontWeight(FontWeight.Medium).fontColor(Colors.link) }
          }.height(38).padding({left:13,right:13}).borderRadius(Tokens.radiusCapsule).backgroundColor(Colors.surface).bindMenu(this.scheduleCreateMenu).id('schedules.create')
          Blank().layoutWeight(1)
          Button({type:ButtonType.Circle}) { SymbolGlyph(Symbols.refresh).fontSize(16).fontColor([Colors.ink]) }
            .width(38).height(38).padding(0).backgroundColor(Colors.surface).accessibilityText(tr('refresh'))
            .onClick(() => this.management.load('schedules',this.model)).id('schedules.refresh')
        }.width('100%')
      }
      ListItem() { PhoneSectionTitle({text:tr('phone_scheduled'),trailing:String(this.management.rows.length)}) }
      ForEach(this.managementRowBindings, (binding: BoundRow<ManagementRow>) => {
        ListItem() {
          Column() {
            Row({space:10}) {
              Stack() { SymbolGlyph(Symbols.calendar).fontSize(18).fontColor([Colors.teal]) }.width(32).height(32).backgroundColor(Colors.control).borderRadius(Tokens.radiusControl)
              Column({space:4}) {
                Text(binding.value.title).fontSize(17).fontWeight(FontWeight.Medium).fontColor(Colors.ink).maxLines(2)
                if (binding.value.detail || Wire.text(binding.value.raw,'last_status'))
                  Text(this.scheduleTime(binding.value.detail) || Wire.text(binding.value.raw,'last_status')).fontSize(13).fontColor(Colors.secondary).maxLines(2)
              }.layoutWeight(1).alignItems(HorizontalAlign.Start)
              Toggle({type:ToggleType.Switch,isOn:Wire.bool(binding.value.raw,'enabled')}).selectedColor(Colors.positive)
                .onChange((enabled:boolean) => { Haptics.tap(); this.management.setScheduleEnabled(this.model,binding.value,enabled); }).accessibilityText(binding.value.title)
              Button({type:ButtonType.Circle}) { Image(Symbols.more).width(18).height(18).fillColor(Colors.secondary) }
                .width(31).height(31).padding(0).backgroundColor(Colors.control).accessibilityText(tr('more')).bindMenu(this.scheduleMenu(binding.value))
                .id('schedule.menu.' + binding.value.id)
            }.width('100%')
          }.width('100%').padding(12).backgroundColor(Colors.surface).borderRadius(PhoneStyle.cardRadius)
            .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(nativeEnvironment.dark), radius: PhoneStyle.cardRadius })
            .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
        }.id('schedule.' + binding.value.id)
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (this.management.lastRunReceipt['session_id']) ListItem() {
        ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('run_started') }) })
      }.onClick(() => { const target = this.management.scheduleReceiptChat(this.model); if (target) this.onChat(target); }).id('schedule.receipt')
      if (!this.management.rows.length && !this.management.loading && !this.management.error)
        ListItem() { Text(tr('phone_no_schedules')).fontSize(15).fontColor(Colors.secondary).padding(16).width('100%').backgroundColor(Colors.surface).borderRadius(Tokens.radiusCard) }
      if (this.scheduleRunBindings.length) ListItem() { PhoneSectionTitle({text:tr('phone_history')}) }
      ForEach(this.scheduleRunBindings, (binding: BoundRow<ManagementRow>) => {
        ListItem() {
          Row({space:10}) {
            Column({space:4}) {
              Text(binding.value.title).fontSize(16).fontColor(Colors.ink).maxLines(2)
              Text([Wire.text(binding.value.raw,'status'),this.scheduleTime(Wire.text(binding.value.raw,'at') || Wire.text(binding.value.raw,'due'))].filter((v:string) => !!v).join(' · ')).fontSize(13).fontColor(Colors.secondary)
            }.alignItems(HorizontalAlign.Start).layoutWeight(1)
            SymbolGlyph(Symbols.expand).fontSize(13).fontColor([Colors.secondary])
          }.padding(13).width('100%').backgroundColor(Colors.surface).borderRadius(Tokens.radiusCard)
        }.id('schedule.run.' + binding.value.id)
          .onClick(() => { const target = this.management.scheduleRunChat(this.model, binding.value); if (target) this.onChat(target); })
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (this.management.loading) ListItem() { LoadingProgress().width(28).height(28) }
      if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor(Colors.danger).padding(Tokens.inset) }
    }.width('100%').height('100%').padding({left:16,right:16,top:6,bottom:12}).backgroundColor(Colors.grouped)
      .bindSheet($$this.scheduleEditOpen, this.scheduleForm(), { title: { title: tr(this.management.scheduleDraft.id ? 'edit' : 'create') }, height: SheetSize.LARGE, showClose: true,
        onDisappear: (): void => { this.management.cancelSchedule(); } })
  }

  @Builder triagePage() {
    List({ space: 12 }) {
      ListItem() {
        Column() {
          ForEach(['enabled', 'auto_continue', 'wake', 'decide', 'reply_reminder'], (key: string, idx: number) => {
            if (key !== 'reply_reminder' || Object.keys(this.management.data).includes(key)) {
              Column() {
                Row({ space: Tokens.inset }) {
                  Text(tr(key)).fontSize(Tokens.body).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.ink).layoutWeight(1)
                  Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.management.data, key, key === 'reply_reminder') })
                    .selectedColor(Colors.positive)
                    .enabled(!this.management.saving && (key === 'enabled' || Wire.bool(this.management.data, 'enabled')))
                    .onChange((value: boolean) => { Haptics.tap(); this.saveTriage(key, value); }).accessibilityText(tr(key))
                }.width('100%').constraintSize({ minHeight: 54 }).padding({ left: Tokens.page, right: Tokens.page, top: 6, bottom: 6 })
                if (idx < 4 && (idx < 3 || Object.keys(this.management.data).includes('reply_reminder'))) {
                  Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
                }
              }.id('triage.' + key)
            }
          }, (key: string) => key)
        }
        .width('100%')
        .backgroundColor(Colors.surface)
        .borderRadius(PhoneStyle.cardRadius)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(nativeEnvironment.dark), radius: PhoneStyle.cardRadius })
        .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
      }
      if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%').padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: 24 }).backgroundColor(Colors.grouped)
  }

  private secretSet(name: string): boolean {
    return Wire.rows(this.management.extensionDetail, 'secrets').some((item: Record<string, Object>) =>
      Wire.text(item, 'name') === name && Wire.bool(item, 'set'));
  }
  private qrPayload(prompt: Record<string, Object>): string {
    const value = Wire.text(prompt, 'qr_text', Wire.text(prompt, 'url', Wire.text(prompt, 'verification_uri_complete')));
    return value.length <= 4096 && /^https:\/\//.test(value) ? value : '';
  }

  @Builder extensionOperationPanel() {
    if (Object.keys(this.management.operation).length > 0) ListItem() {
      Column({ space: Tokens.inset }) {
        ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('operation'),
          secondaryText: tr(Wire.text(this.management.operation, 'state')) + ' · ' + Wire.text(this.management.operation, 'step') }) })
        if (Wire.text(this.management.operation, 'error')) Text(Wire.text(this.management.operation, 'error')).fontColor($r('app.color.persona_danger'))
        ForEach(Wire.rows(this.management.operation, 'steps'), (step: Record<string, Object>) => {
          Text(Wire.text(step, 'id') + ' · ' + Wire.text(step, 'state') + (Wire.bool(step, 'cached') ? ' · ' + tr('cached') : ''))
            .fontSize(Tokens.meta).padding(Tokens.inset)
        }, (step: Record<string, Object>, index: number) => JSON.stringify([index, step]))
        Row() {
          if (['running', 'needs_consent'].includes(Wire.text(this.management.operation, 'state')))
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('refresh')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('refresh', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('refresh')).bindTips(tr('refresh')).onClick(() => this.refreshOperation())
          if (['running', 'needs_consent'].includes(Wire.text(this.management.operation, 'state')))
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('cancel')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('cancel', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('cancel')).bindTips(tr('cancel')).onClick(() => this.management.cancelExtensionOperation(this.model))
          if (Wire.text(this.management.operation, 'state') === 'failed' && Wire.bool(this.management.operation, 'retryable'))
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry')).bindTips(tr('retry')).onClick(() => this.management.retryExtensionOperation(this.model))
          if (Wire.text(this.management.operation, 'state') === 'needs_consent' && Object.keys(this.management.preview).length > 0)
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('review')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('review', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('review')).bindTips(tr('review')).onClick(() => this.extensionPreviewOpen = true)
        }
      }.padding(Tokens.inset).width('100%').alignItems(HorizontalAlign.Start)
    }.id('extension.operation')
  }

  @Builder bundleHeader(member: Record<string, Object>) {
    Text(Wire.text(Wire.object(member, 'manifest'), 'name', Wire.text(Wire.object(member, 'manifest'), 'id')))
      .fontSize(Tokens.meta).fontWeight(FontWeight.Medium).padding(Tokens.inset)
  }
  @Builder extensionPreviewForm() {
    Column() {
      List() {
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(Wire.object(this.management.preview, 'manifest'), 'name', this.management.catalogSelectedId),
          secondaryText: Wire.text(Wire.object(this.management.preview, 'manifest'), 'version') }) }) }
        ForEach(Wire.rows(this.management.preview, 'permissions'), (permission: Record<string, Object>) => {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(permission, 'title', Wire.text(permission, 'key')),
            secondaryText: Wire.text(permission, 'detail') }) }) }.id('permission.' + Wire.text(permission, 'key'))
        }, (permission: Record<string, Object>) => Wire.text(permission, 'key'))
        ForEach(Wire.rows(this.management.preview, 'added_permissions'), (permission: Record<string, Object>) => {
          ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(permission, 'title', Wire.text(permission, 'key')),
            secondaryText: Wire.text(permission, 'detail') }) }) }.id('added.permission.' + Wire.text(permission, 'key'))
        }, (permission: Record<string, Object>) => Wire.text(permission, 'key'))
        ForEach(Wire.strings(this.management.preview, 'warnings'), (warning: string) => {
          ListItem() { Text(warning).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
        }, (warning: string) => warning)
        ForEach(Wire.rows(this.management.preview, 'bundle'), (member: Record<string, Object>) => {
          ListItemGroup({ header: () => this.bundleHeader(member) }) {
            ForEach(Wire.rows(member, 'permissions'), (permission: Record<string, Object>) => {
              ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(permission, 'title', Wire.text(permission, 'key')),
                secondaryText: Wire.text(permission, 'detail') }) }) }
            }, (permission: Record<string, Object>) => Wire.text(permission, 'key'))
            ForEach(Wire.strings(member, 'warnings'), (warning: string) => {
              ListItem() { Text(warning).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
            }, (warning: string) => warning)
          }
        }, (member: Record<string, Object>) => Wire.text(member, 'preview_id'))
        if (Wire.strings(this.management.preview, 'unresolved').length > 0 || !this.management.previewReady())
          ListItem() { Text(tr('dependencies_unresolved')).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      }.width('100%').layoutWeight(1)
      Row() {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('cancel')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('cancel', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('cancel')).bindTips(tr('cancel')).onClick(() => {
          this.extensionPreviewOpen = false;
          if (Wire.text(this.management.operation, 'state') === 'needs_consent') this.management.cancelExtensionOperation(this.model);
          else this.management.preview = {};
        })
        Button({ type: ButtonType.Circle }) { if (IconActions.busy(Wire.bool(this.management.preview, 'upgrade') ? 'upgrade' : 'install')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(Wire.bool(this.management.preview, 'upgrade') ? 'upgrade' : 'install')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(Wire.bool(this.management.preview, 'upgrade') ? 'upgrade' : 'install', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(Wire.bool(this.management.preview, 'upgrade') ? 'upgrade' : 'install')).bindTips(tr(Wire.bool(this.management.preview, 'upgrade') ? 'upgrade' : 'install'))
          .enabled(this.management.previewReady() && !this.management.saving)
          .onClick(() => this.management.confirmExtensionPreview(this.model).then((ok: boolean) => { if (ok) this.extensionPreviewOpen = false; }))
      }.justifyContent(FlexAlign.SpaceBetween).width('100%').padding(Tokens.inset)
      if (this.management.error) Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
    }.width('100%').height('100%')
  }

  @Builder extensionCatalogPage() {
    List() {
      ListItem() {
        Row() {
          TextInput({ text: this.management.catalogQuery, placeholder: tr('search') })
            .onChange((value: string) => this.management.catalogQuery = value).layoutWeight(1)
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('search')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('search', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('search')).bindTips(tr('search')).onClick(() => this.management.loadExtensionCatalog(this.model))
        }
      }
      if (this.management.catalogMarkets.length > 0) ListItem() {
        Select(this.selectOptions(this.catalogMarketOptions())).attributeModifier(SelectStyles.inline)
          .selected(this.management.catalogMarket ? Math.max(0, this.management.catalogMarkets.findIndex((market: Record<string, Object>) => Wire.text(market, 'id') === this.management.catalogMarket) + 1) : 0)
          .value(this.catalogMarketName())
          .onSelect((index: number) => this.management.loadExtensionCatalog(this.model, this.management.catalogQuery,
            index === 0 ? '' : Wire.text(this.management.catalogMarkets[index - 1], 'id')))
      }
      this.extensionOperationPanel()
      ForEach(this.catalogRowBindings, (binding: BoundRow<ManagementRow>) => {
        ListItem() {
          Column() {
            ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title,
              secondaryText: Wire.text(binding.value.raw, 'problem', Wire.text(binding.value.raw, 'description', Wire.text(binding.value.raw, 'version'))) }) })
            Button({ type: ButtonType.Circle }) { if (IconActions.busy(Wire.bool(binding.value.raw, 'upgradable') ? 'upgrade' : 'preview')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(Wire.bool(binding.value.raw, 'upgradable') ? 'upgrade' : 'preview')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(Wire.bool(binding.value.raw, 'upgradable') ? 'upgrade' : 'preview', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(Wire.bool(binding.value.raw, 'upgradable') ? 'upgrade' : 'preview')).bindTips(tr(Wire.bool(binding.value.raw, 'upgradable') ? 'upgrade' : 'preview'))
              .enabled(Wire.bool(this.management.data, 'can_install') && !Wire.text(binding.value.raw, 'problem') && !this.management.saving)
              .onClick(() => this.previewCatalogRow(binding.value))
          }.padding(Tokens.inset).width('100%').alignItems(HorizontalAlign.Start)
        }.id('catalog.' + Wire.text(binding.value.raw, 'market') + '.' + binding.value.id)
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (!this.management.catalogRows.length && !this.management.loading) ListItem() { Text(tr('empty')).padding(Tokens.page) }
      if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder extensionDetailPage() {
    List() {
      if (Wire.text(this.management.extensionDetail, 'version', Wire.text(this.management.extensionDetail, 'source'))) ListItem() { Text(Wire.text(this.management.extensionDetail, 'version', Wire.text(this.management.extensionDetail, 'source'))).fontColor($r('sys.color.font_secondary')).padding(Tokens.inset) }
      if (Wire.bool(this.management.extensionDetail, 'read_only')) ListItem() { Text(tr('read_only')).padding(Tokens.inset) }
      this.extensionOperationPanel()
      if (this.management.extensionConfigFields.length > 0 && this.management.extensionSchemaSupported) {
        ForEach(this.extensionConfigFieldsBindings, (binding: BoundRow<ManagementInput>) => {
          ListItem() {
            Column() {
              Text(binding.value.label).fontSize(Tokens.body)
              if (binding.value.kind === 'boolean') Toggle({ type: ToggleType.Switch, isOn: binding.value.value === 'true' })
                .onChange((value: boolean) => this.management.changeInput(this.management.extensionConfigFields, binding.value.key, String(value)))
              else if (binding.value.kind === 'enum') Select(this.selectOptions(binding.value.options)).attributeModifier(SelectStyles.inline)
                .selected(Math.max(0, binding.value.options.indexOf(binding.value.value))).value(binding.value.value)
                .onSelect((index: number) => this.management.changeInput(this.management.extensionConfigFields, binding.value.key, binding.value.options[index]))
              else TextInput({ text: binding.value.value, placeholder: binding.value.label })
                .type(binding.value.kind === 'integer' || binding.value.kind === 'number' ? InputType.Number : InputType.Normal)
                .onChange((value: string) => this.management.changeInput(this.management.extensionConfigFields, binding.value.key, value))
            }.padding(Tokens.inset).width('100%').alignItems(HorizontalAlign.Start)
          }.id('extension.config.' + binding.value.key)
        }, (binding: BoundRow<ManagementInput>) => binding.key)
        ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('save')).bindTips(tr('save')).enabled(this.management.extensionConfigChanged() && !this.management.saving && !Wire.bool(this.management.extensionDetail, 'read_only'))
          .onClick(() => this.confirmExtensionSave()) }
      } else if (!this.management.extensionSchemaSupported) ListItem() { Text(tr('unknown_field')).padding(Tokens.inset) }
      ForEach(this.extensionSecretFieldsBindings, (binding: BoundRow<ManagementInput>) => {
        ListItem() {
          Column() {
            TextInput({ text: binding.value.value, placeholder: binding.value.label }).type(InputType.Password).showPasswordIcon(false)
              .onChange((value: string) => this.management.changeInput(this.management.extensionSecretFields, binding.value.key, value))
            Row() {
              Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('save')).bindTips(tr('save')).enabled(!!binding.value.value && !this.management.saving).onClick(() => this.management.saveExtensionSecret(this.model, binding.value))
              if (this.secretSet(binding.value.key)) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('clear')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('clear', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('clear')).bindTips(tr('clear')).onClick(() => this.management.clearExtensionSecret(this.model, binding.value))
            }
          }.padding(Tokens.inset).width('100%').alignItems(HorizontalAlign.Start)
        }.id('extension.secret.' + binding.value.key)
      }, (binding: BoundRow<ManagementInput>) => binding.key)
      if (this.model.features.includes('extension_auth') && Object.keys(this.management.auth).length > 0) {
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('sign_in'),
          secondaryText: Wire.text(this.management.auth, 'account', Wire.text(this.management.auth, 'state')) }) }) }
        if (['signed_out', 'expired'].includes(Wire.text(this.management.auth, 'state')))
          ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('sign_in')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('sign_in', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('sign_in')).bindTips(tr('sign_in')).enabled(!this.management.saving).onClick(() => this.management.extensionAuthStep(this.model, this.management.selectedId, true)) }
        if (Wire.text(this.management.auth, 'state') === 'signed_in')
          ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('sign_out')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('sign_out', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('sign_out')).bindTips(tr('sign_out')).onClick(() => this.management.extensionLogout(this.model, this.management.selectedId)) }
        if (Object.keys(this.management.authPrompt).length > 0) {
          if (Wire.text(this.management.authPrompt, 'url')) ListItem() { Text(Wire.text(this.management.authPrompt, 'url')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
          if (Wire.text(this.management.authPrompt, 'user_code')) ListItem() { Text(Wire.text(this.management.authPrompt, 'user_code')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
          if (Wire.text(this.management.authPrompt, 'hint')) ListItem() { Text(Wire.text(this.management.authPrompt, 'hint')).padding(Tokens.inset) }
          if (this.qrPayload(this.management.authPrompt)) ListItem() { QRCode(this.qrPayload(this.management.authPrompt)).width(Tokens.qr).height(Tokens.qr).margin(Tokens.page) }
          ForEach(this.authFieldsBindings, (binding: BoundRow<ManagementInput>) => ListItem() {
            TextInput({ text: binding.value.value, placeholder: binding.value.label }).type(binding.value.secret ? InputType.Password : InputType.Normal)
              .onChange((value: string) => this.management.changeInput(this.management.authFields, binding.value.key, value))
          }.id('extension.auth.' + binding.value.key), (binding: BoundRow<ManagementInput>) => binding.key)
          if (Wire.text(this.management.authPrompt, 'error')) ListItem() { Text(Wire.text(this.management.authPrompt, 'error')).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
          ListItem() { Button({ type: ButtonType.Circle }) { if (IconActions.busy(Wire.text(this.management.authPrompt, 'kind') ? 'check_status' : 'continue')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(Wire.text(this.management.authPrompt, 'kind') ? 'check_status' : 'continue')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(Wire.text(this.management.authPrompt, 'kind') ? 'check_status' : 'continue', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(Wire.text(this.management.authPrompt, 'kind') ? 'check_status' : 'continue')).bindTips(tr(Wire.text(this.management.authPrompt, 'kind') ? 'check_status' : 'continue'))
            .onClick(() => this.management.extensionAuthStep(this.model, this.management.selectedId, false)) }
        }
      }
      ForEach(Wire.rows(this.management.extensionDetail, 'ui'), (page: Record<string, Object>) => {
        if (Wire.text(page, 'id')) ListItem() { Button({ type: ButtonType.Normal }) { Row({ space: Tokens.inset }) { Text(Wire.text(page, 'title', Wire.text(page, 'id'))).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1); SymbolGlyph($r('sys.symbol.chevron_right')).fontSize(Tokens.meta).fontColor([Colors.secondary]) }.width('100%').constraintSize({ minHeight: Tokens.touch }) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).padding({ left: Tokens.page, right: Tokens.page }).onClick(() => this.onExtension(this.management.selectedId, Wire.text(page, 'id'), Wire.text(page, 'title', Wire.text(page, 'id')))) }
      }, (page: Record<string, Object>) => Wire.text(page, 'id'))
      if (Wire.text(this.management.extensionDetail, 'origin').startsWith('market:') && !Wire.bool(this.management.extensionDetail, 'read_only'))
        ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('remove')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('remove', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('remove')).bindTips(tr('remove')).onClick(() => this.confirmExtensionRemove()) }
      if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  private connectorAllowed(id: string): boolean {
    const normalized = id.trim().toLowerCase();
    if (!this.model.mailModuleAllowed() && (normalized === 'mail' || normalized.startsWith('mail:'))) return false;
    if (!this.model.larkModuleAllowed() && ['larksync', 'feishu_bot', 'feishu-bot', 'lark_bot'].includes(normalized)) return false;
    return true;
  }
  private channelAllowed(id: string): boolean {
    const normalized = id.trim().toLowerCase();
    if (!this.model.larkModuleAllowed() && ['feishu', 'feishu_bot', 'feishu-bot', 'lark_bot'].includes(normalized)) return false;
    return true;
  }
  @Builder connectorDetailPage() {
    List() {
      if (!this.connectorAllowed(this.management.selectedId)) {
        ListItem() { Text(tr('read_only')).fontColor($r('sys.color.font_secondary')).padding(Tokens.inset) }
      } else {
      if (Wire.text(this.selectedConnector()?.raw || {}, 'status')) ListItem() { Text(Wire.text(this.selectedConnector()?.raw || {}, 'status')).padding(Tokens.inset) }
      if (this.selectedConnector() && Wire.text(this.selectedConnector()?.raw || {}, 'account'))
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('account'), secondaryText: Wire.text(this.selectedConnector()?.raw || {}, 'account') }) }) }
      if (this.selectedConnector() && Wire.text(this.selectedConnector()?.raw || {}, 'via'))
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('via'), secondaryText: Wire.text(this.selectedConnector()?.raw || {}, 'via') }) }) }
      if (this.selectedConnector() && Wire.text(this.selectedConnector()?.raw || {}, 'error'))
        ListItem() { Text(Wire.text(this.selectedConnector()?.raw || {}, 'error')).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      if (this.selectedConnector() && Wire.has(this.selectedConnector()?.raw || {}, 'enabled')) ListItem() {
        Row() {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('enabled') }) }).layoutWeight(1)
          Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.selectedConnector()?.raw || {}, 'enabled') })
            .onChange((value: boolean) => this.management.setConnectorSetting(this.model, this.selectedConnector() as ManagementRow, 'enabled', value))
        }
      }
      if (this.selectedConnector() && Wire.has(this.selectedConnector()?.raw || {}, 'inbox')) ListItem() {
        Row() {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('inbox') }) }).layoutWeight(1)
          Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.selectedConnector()?.raw || {}, 'inbox') })
            .onChange((value: boolean) => this.management.setConnectorSetting(this.model, this.selectedConnector() as ManagementRow, 'inbox', value))
        }
      }
      if (this.selectedConnector() && Wire.has(this.selectedConnector()?.raw || {}, 'archive')) ListItem() {
        Row() {
          ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('archive') }) }).layoutWeight(1)
          Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.selectedConnector()?.raw || {}, 'archive') })
            .onChange((value: boolean) => this.management.setConnectorSetting(this.model, this.selectedConnector() as ManagementRow, 'archive', value))
        }
      }
      if (this.selectedConnector() && Wire.strings(this.selectedConnector()?.raw || {}, 'guide').length > 0) {
        ForEach(Wire.strings(this.selectedConnector()?.raw || {}, 'guide'), (line: string) => {
          ListItem() { Text(line).padding(Tokens.inset) }
        }, (line: string) => line)
      }
      if (this.selectedConnector() && Wire.strings(this.selectedConnector()?.raw || {}, 'capabilities').includes('login')) {
        if (Wire.text(this.management.connectorPrompt, 'url')) ListItem() { Text(Wire.text(this.management.connectorPrompt, 'url')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
        if (Wire.text(this.management.connectorPrompt, 'user_code')) ListItem() { Text(Wire.text(this.management.connectorPrompt, 'user_code')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
        if (Wire.text(this.management.connectorPrompt, 'hint')) ListItem() { Text(Wire.text(this.management.connectorPrompt, 'hint')).padding(Tokens.inset) }
        if (this.qrPayload(this.management.connectorPrompt)) ListItem() { QRCode(this.qrPayload(this.management.connectorPrompt)).width(Tokens.qr).height(Tokens.qr).margin(Tokens.page) }
        ForEach(this.connectorFieldsBindings, (binding: BoundRow<ManagementInput>) => {
          ListItem() { TextInput({ text: binding.value.value, placeholder: binding.value.label }).type(binding.value.secret ? InputType.Password : InputType.Normal)
            .onChange((value: string) => this.management.changeInput(this.management.connectorFields, binding.value.key, value)) }
            .id('connector.login.' + binding.value.key)
        }, (binding: BoundRow<ManagementInput>) => binding.key)
        if (Wire.text(this.management.connectorPrompt, 'error')) ListItem() { Text(Wire.text(this.management.connectorPrompt, 'error')).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
        if (!Wire.text(this.selectedConnector()?.raw || {}, 'account')) ListItem() {
          Button({ type: ButtonType.Circle }) { if (IconActions.busy(Object.keys(this.management.connectorPrompt).length === 0 ? 'sign_in' : Wire.text(this.management.connectorPrompt, 'kind') ? 'check_status' : 'continue')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(Object.keys(this.management.connectorPrompt).length === 0 ? 'sign_in' : Wire.text(this.management.connectorPrompt, 'kind') ? 'check_status' : 'continue')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(Object.keys(this.management.connectorPrompt).length === 0 ? 'sign_in' : Wire.text(this.management.connectorPrompt, 'kind') ? 'check_status' : 'continue', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(Object.keys(this.management.connectorPrompt).length === 0 ? 'sign_in' : Wire.text(this.management.connectorPrompt, 'kind') ? 'check_status' : 'continue')).bindTips(tr(Object.keys(this.management.connectorPrompt).length === 0 ? 'sign_in' : Wire.text(this.management.connectorPrompt, 'kind') ? 'check_status' : 'continue'))
            .enabled(!this.management.saving)
            .onClick(() => this.management.connectorLogin(this.model, this.selectedConnector() as ManagementRow, Object.keys(this.management.connectorPrompt).length === 0))
        }
        if (Wire.text(this.selectedConnector()?.raw || {}, 'account'))
          ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('sign_out')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('sign_out', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('sign_out')).bindTips(tr('sign_out')).onClick(() => this.confirmConnectorLogout()) }
      }
      if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      }
    }.width('100%').height('100%')
  }

  @Builder extensionConnectorPage(page: string) {
    List() {
      if (page === 'extensions' && Wire.bool(this.management.data, 'markets')) ListItem() {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('extension_market')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('extension_market', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('extension_market')).bindTips(tr('extension_market')).onClick(() => this.openCatalog())
      }
      this.extensionOperationPanel()
      ForEach(this.managementRowBindings, (binding: BoundRow<ManagementRow>) => {
        if (page !== 'connectors' || this.connectorAllowed(binding.value.id)) ListItem() {
          Column() {
            Row() {
              Button({ type: ButtonType.Normal }) {
                Row({ space: Tokens.gap }) {
                  ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title,
                    secondaryText: Wire.text(binding.value.raw, 'error', binding.value.detail || Wire.text(binding.value.raw, 'via', Wire.text(binding.value.raw, 'status'))) }) }).layoutWeight(1)
                  SymbolGlyph(Symbols.expand).fontSize(Tokens.symbolFont).fontColor([Colors.secondary])
                }.width('100%').constraintSize({ minHeight: Tokens.touch })
              }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).layoutWeight(1)
                .onClick(() => this.openDetail(page === 'extensions' ? 'extension_detail' : 'connector_detail', binding.value))
              Toggle({ type: ToggleType.Switch, isOn: this.rowEnabled(binding.value) })
                .onChange((value: boolean) => page === 'extensions' ? this.management.setExtensionEnabled(this.model, binding.value, value) : this.management.setConnectorSetting(this.model, binding.value, 'enabled', value))
                .accessibilityText(binding.value.title)
            }

          }
        }.id(page + '.' + binding.value.id)
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (!this.management.rows.length && !this.management.loading) ListItem() { Text(tr('empty')).padding(Tokens.page) }
      if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder channelDetailPage() {
    List() {
      if (!this.channelAllowed(this.management.selectedId)) {
        ListItem() { Text(tr('read_only')).fontColor($r('sys.color.font_secondary')).padding(Tokens.inset) }
      } else if (Object.keys(this.management.channelDetail).length > 0) {
        ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(this.management.channelData(), 'name', this.management.selectedId),
          secondaryText: Wire.text(this.management.channelData(), 'state') }) }) }
        if (Wire.bool(this.management.channelData(), 'read_only')) ListItem() { Text(tr('read_only')).padding(Tokens.inset) }
        if (!Wire.bool(this.management.channelData(), 'read_only')) {
          ListItem() {
            Row() {
              ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('enabled') }) }).layoutWeight(1)
              Toggle({ type: ToggleType.Switch, isOn: Wire.bool(this.management.channelData(), 'enabled') })
                .onChange((value: boolean) => this.management.setChannelEnabled(this.model, this.selectedChannel() as ManagementRow, value))
            }
          }
          if (Wire.text(Wire.object(this.management.channelData(), 'target'), 'kind')) ListItem() {
            Select(this.selectOptions(['main', 'new', 'conversation'].map((kind: string) => this.channelTargetLabel(kind)))).attributeModifier(SelectStyles.inline)
              .selected(this.channelTargetIndex()).value(this.channelTargetLabel(this.channelTargetDraft || Wire.text(Wire.object(this.management.channelData(), 'target'), 'kind', 'main')))
              .enabled(!this.management.saving)
              .onSelect((index: number) => this.selectChannelTarget(index))
          }
          if ((this.channelTargetDraft === 'conversation' || Wire.text(Wire.object(this.management.channelData(), 'target'), 'kind') === 'conversation') && this.model.conversations.length > 0) ListItem() {
            Select(this.selectOptions(this.model.conversations.map((row: ResourceRow) => row.title))).attributeModifier(SelectStyles.inline)
              .selected(this.channelTargetDraft ? -1 : this.channelConversationIndex()).value(this.channelTargetDraft ? tr('conversation') : this.model.conversations[this.channelConversationIndex()]?.title || '')
              .enabled(!this.management.saving)
              .onSelect((index: number) => this.selectChannelConversation(index))
          }
          ForEach(this.channelFeatureBindings, (binding: BoundRow<Record<string, Object>>) => {
            ListItem() {
              Row() {
                ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: Wire.text(binding.value, 'title', Wire.text(binding.value, 'id')) }) }).layoutWeight(1)
                Toggle({ type: ToggleType.Switch, isOn: Wire.bool(binding.value, 'on') })
                  .onChange((value: boolean) => this.management.setChannelFeature(this.model, this.management.selectedId, Wire.text(binding.value, 'id'), value))
                  .accessibilityText(Wire.text(binding.value, 'title', Wire.text(binding.value, 'id')))
              }
            }.id('channel.feature.' + Wire.text(binding.value, 'id'))
          }, (binding: BoundRow<Record<string, Object>>) => binding.key)
        }
        if (Wire.text(this.management.channelData(), 'bind', 'none') !== 'none' && !Wire.bool(this.management.channelData(), 'read_only')) {
          if (this.channelBound())
            ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('unbind')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('unbind', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('unbind')).bindTips(tr('unbind')).onClick(() => this.confirmChannelUnbind()) }
          else if (Object.keys(this.management.channelPrompt).length === 0)
            ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('sign_in')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('sign_in', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('sign_in')).bindTips(tr('sign_in')).onClick(() => this.management.channelBind(this.model, this.management.selectedId, true)) }
          if (Wire.text(this.management.channelPrompt, 'url')) ListItem() { Text(Wire.text(this.management.channelPrompt, 'url')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
          if (Wire.text(this.management.channelPrompt, 'user_code')) ListItem() { Text(Wire.text(this.management.channelPrompt, 'user_code')).copyOption(CopyOptions.LocalDevice).padding(Tokens.inset) }
          if (Wire.text(this.management.channelPrompt, 'hint')) ListItem() { Text(Wire.text(this.management.channelPrompt, 'hint')).padding(Tokens.inset) }
          if (this.qrPayload(this.management.channelPrompt)) ListItem() { QRCode(this.qrPayload(this.management.channelPrompt)).width(Tokens.qr).height(Tokens.qr).margin(Tokens.page) }
          ForEach(this.channelFieldsBindings, (binding: BoundRow<ManagementInput>) => {
            ListItem() { TextInput({ text: binding.value.value, placeholder: binding.value.label }).type(binding.value.secret ? InputType.Password : InputType.Normal)
              .onChange((value: string) => this.management.changeInput(this.management.channelFields, binding.value.key, value)) }
              .id('channel.bind.' + binding.value.key)
          }, (binding: BoundRow<ManagementInput>) => binding.key)
          if (Wire.text(this.management.channelPrompt, 'error')) ListItem() { Text(Wire.text(this.management.channelPrompt, 'error')).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
          if (Object.keys(this.management.channelPrompt).length > 0) ListItem() {
            Button({ type: ButtonType.Circle }) { if (IconActions.busy(Wire.text(this.management.channelPrompt, 'kind') ? 'check_status' : 'continue')) LoadingProgress().width(Tokens.symbol).height(Tokens.symbol).color(Colors.link); else SymbolGlyph(IconActions.symbol(Wire.text(this.management.channelPrompt, 'kind') ? 'check_status' : 'continue')).fontSize(Tokens.symbolFont).fontColor([IconActions.color(Wire.text(this.management.channelPrompt, 'kind') ? 'check_status' : 'continue', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr(Wire.text(this.management.channelPrompt, 'kind') ? 'check_status' : 'continue')).bindTips(tr(Wire.text(this.management.channelPrompt, 'kind') ? 'check_status' : 'continue'))
              .enabled(!this.management.saving).onClick(() => this.management.channelBind(this.model, this.management.selectedId, false))
          }
        }
        if (Wire.text(this.management.channelData(), 'state') === 'connected' && !Wire.bool(this.management.channelData(), 'read_only'))
          ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('test')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('test', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('test')).bindTips(tr('test')).onClick(() => this.management.channelTest(this.model, this.management.selectedId)) }
        if (this.channelWaitingText()) ListItem() { ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: tr('waiting'),
          secondaryText: this.channelWaitingText() }) }) }
      }
      if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder channelsPage() {
    List() {
      ForEach(this.managementRowBindings, (binding: BoundRow<ManagementRow>) => {
        if (this.channelAllowed(binding.value.id)) ListItem() {
          Column() {
            Row() {
              Button({ type: ButtonType.Normal }) {
                Row({ space: Tokens.gap }) {
                  ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title,
                    secondaryText: Wire.text(binding.value.raw, 'status', binding.value.detail) }) }).layoutWeight(1)
                  SymbolGlyph(Symbols.expand).fontSize(Tokens.symbolFont).fontColor([Colors.secondary])
                }.width('100%').constraintSize({ minHeight: Tokens.touch })
              }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).layoutWeight(1)
                .enabled(Wire.bool(binding.value.raw, 'installed', true)).onClick(() => this.openDetail('channel_detail', binding.value))
              Toggle({ type: ToggleType.Switch, isOn: this.rowEnabled(binding.value) })
                .enabled(Wire.bool(binding.value.raw, 'installed', true) && !Wire.bool(binding.value.raw, 'read_only'))
                .onChange((value: boolean) => this.management.setChannelEnabled(this.model, binding.value, value)).accessibilityText(binding.value.title)
            }

          }
        }.id('channel.' + binding.value.id)
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (!this.management.rows.length && !this.management.loading) ListItem() { Text(tr('empty')).padding(Tokens.page) }
      if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  @Builder ledgerPage() {
    List() {
      ForEach(this.managementRowBindings, (binding: BoundRow<ManagementRow>) => {
        ListItem() {
          Column() {
            ComposeListItemV2({ contentItemV2: new ContentItemV2({ primaryText: binding.value.title,
              secondaryText: binding.value.detail || Wire.text(binding.value.raw, 'action') }) })
            Text(Wire.text(binding.value.raw, 'text')).fontSize(Tokens.body).copyOption(CopyOptions.LocalDevice).width('100%')
            Text(Wire.text(binding.value.raw, 'at')).fontSize(Tokens.meta).width('100%')
            Text(Wire.text(Wire.object(binding.value.raw, 'origin'), 'session_id')).fontSize(Tokens.meta).width('100%')
          }.padding(Tokens.inset).width('100%').alignItems(HorizontalAlign.Start)
        }.id('sent.' + binding.value.id)
      }, (binding: BoundRow<ManagementRow>) => binding.key)
      if (!this.management.rows.length && !this.management.loading) ListItem() { Text(tr('empty')).padding(Tokens.page) }
      if (Wire.identifier(this.management.data, 'next')) ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('load_more')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('load_more', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('load_more')).bindTips(tr('load_more')).enabled(!this.management.loading).onClick(() => this.management.loadMoreLedger(this.model)) }
      if (this.management.error) ListItem() { Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
    }.width('100%').height('100%')
  }

  private appearanceSelector(kind: string, revision: number): (index: number) => void {
    const owner = this.model, workspace = this.agentWorkspace, scope = owner.scope(), generation = owner.connectionGeneration();
    return (index: number): void => {
      if (kind === 'language') this.applyLanguage(index, owner, revision, scope, generation);
      else if (kind === 'color') this.applyColorMode(index, owner, revision, scope, generation);
      else if (kind === 'style') this.applyStyle(index, owner, revision, scope, generation);
      else if (kind === 'phoneNavigation' && this.appearanceCurrent(owner,revision,scope,generation)) this.look.setPhoneNavigation(index === 1 ? 'merged' : 'separate');
      else if (kind === 'density') this.applyDensity(index, owner, revision, scope, generation);
      else if (kind === 'group') this.applyAgentGrouping(index, owner, workspace, revision, scope, generation);
    };
  }
  private appearanceAvatarAction(revision: number, accent: boolean): (key: string) => void {
    const owner = this.model, scope = owner.scope(), generation = owner.connectionGeneration();
    return (key: string): void => { if (this.appearanceCurrent(owner, revision, scope, generation)) { if (accent) this.look.setAccent(key); else this.look.setAvatar(key); } };
  }
  private appearanceRefreshAction(revision: number): () => void {
    const owner = this.model, scope = owner.scope(), generation = owner.connectionGeneration();
    return (): void => { if (this.appearanceCurrent(owner, revision, scope, generation)) this.loadLook(); };
  }
  @Builder phoneLookPage() {
    if (this.phoneAppearanceRoute === 'phone_look_avatar') {
      List() {
        DeviceLookPicker({model:this.look,showAccent:false,active:this.appearanceCurrent(this.model,this.appearanceRevision,this.model.scope(),this.model.connectionGeneration()),
          onAvatarSelected:this.appearanceAvatarAction(this.appearanceRevision,false),onRefresh:this.appearanceRefreshAction(this.appearanceRevision)})
      }.width('100%').height('100%').backgroundColor(Colors.sheetBackground)
    } else {
      ForEach([this.appearanceRevision], (revision: number) => {
        PhoneAppearanceSettings({model:this.model,look:this.look,route:this.phoneAppearanceRoute,
          active:this.appearanceCurrent(this.model,revision,this.model.scope(),this.model.connectionGeneration()),
          onOpen:(key:string) => this.onOpen(key)})
      }, (revision:number) => String(revision))
    }
  }
  @Builder appearanceGeneral() {
    ForEach([this.appearanceRevision], (revision: number) => {
      DeviceLookPicker({ model: this.look, active: this.appearanceCurrent(this.model, revision, this.model.scope(), this.model.connectionGeneration()),
        onAvatarSelected: this.appearanceAvatarAction(revision, false),
        onAccentSelected: this.appearanceAvatarAction(revision, true),
        onRefresh: this.appearanceRefreshAction(revision) })
    }, (revision: number) => String(revision))
    ListItem() { PhoneSectionTitle({ text: tr('phone_look_section') }) }
    ListItem() {
      Column() {
        Flex({ wrap: FlexWrap.Wrap, justifyContent: FlexAlign.SpaceBetween, alignItems: ItemAlign.Center, space: { main: LengthMetrics.vp(Tokens.gap), cross: LengthMetrics.vp(Tokens.gap) } }) {
          Text(tr('language')).fontSize(Tokens.body).fontColor(Colors.ink).flexShrink(0).constraintSize({ maxWidth: '100%' })
          Select([{ value: tr('system') }, { value: 'English' }, { value: '简体中文' }, { value: '日本語' }]).attributeModifier(SelectStyles.inline)
            .selected(['default', 'en-US', 'zh-CN', 'ja-JP'].indexOf(this.model.language))
            .value(this.model.language === 'default' ? tr('system') : this.model.language === 'en-US' ? 'English' : this.model.language === 'zh-CN' ? '简体中文' : '日本語')
            .flexShrink(0).constraintSize({ minHeight: Tokens.touch, maxWidth: '100%' }).enabled(!this.model.languageChanging && this.appearanceCurrent(this.model, this.appearanceRevision, this.model.scope(), this.model.connectionGeneration()))
            .onSelect(this.appearanceSelector('language', this.appearanceRevision)).id('appearance.language')
        }.constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.fine, bottom: Tokens.fine }).width('100%')
        Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
        Flex({ wrap: FlexWrap.Wrap, justifyContent: FlexAlign.SpaceBetween, alignItems: ItemAlign.Center, space: { main: LengthMetrics.vp(Tokens.gap), cross: LengthMetrics.vp(Tokens.gap) } }) {
          Text(tr('color_mode')).fontSize(Tokens.body).fontColor(Colors.ink).flexShrink(0).constraintSize({ maxWidth: '100%' })
          Select([{ value: tr('system') }, { value: tr('light') }, { value: tr('dark') }]).attributeModifier(SelectStyles.inline).selected(this.model.appearance)
            .value(tr(['system', 'light', 'dark'][this.model.appearance] || 'system'))
            .flexShrink(0).constraintSize({ minHeight: Tokens.touch, maxWidth: '100%' }).enabled(this.appearanceCurrent(this.model, this.appearanceRevision, this.model.scope(), this.model.connectionGeneration()))
            .onSelect(this.appearanceSelector('color', this.appearanceRevision)).id('appearance.color-mode')
        }.constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.fine, bottom: Tokens.fine }).width('100%')
        Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
        Flex({ wrap: FlexWrap.Wrap, justifyContent: FlexAlign.SpaceBetween, alignItems: ItemAlign.Center, space: { main: LengthMetrics.vp(Tokens.gap), cross: LengthMetrics.vp(Tokens.gap) } }) {
          Text(tr('style')).fontSize(Tokens.body).fontColor(Colors.ink).flexShrink(0).constraintSize({ maxWidth: '100%' })
          Select([{ value: tr('app_style_system') }, { value: tr('app_style_glass') }, { value: tr('app_style_brief') }, { value: tr('app_style_board') }]).attributeModifier(SelectStyles.inline)
            .selected(['system', 'glass', 'brief', 'board'].indexOf(this.model.style) < 0 ? 0 : ['system', 'glass', 'brief', 'board'].indexOf(this.model.style))
            .value(tr(['system', 'glass', 'brief', 'board'].includes(this.model.style) ? 'app_style_' + this.model.style : 'app_style_system'))
            .flexShrink(0).constraintSize({ minHeight: Tokens.touch, maxWidth: '100%' }).enabled(this.appearanceCurrent(this.model, this.appearanceRevision, this.model.scope(), this.model.connectionGeneration()))
            .onSelect(this.appearanceSelector('style', this.appearanceRevision)).id('appearance.style')
        }.constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.fine, bottom: Tokens.fine }).width('100%')
        Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
        Row({space:12}) {
          Text(tr('flow_navigation')).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1)
          Select([{value:tr('flow_separate')},{value:tr('flow_merged')}]).attributeModifier(SelectStyles.inline)
            .selected(this.look.phoneNavigation === 'merged' ? 1 : 0).value(tr(this.look.phoneNavigation === 'merged' ? 'flow_merged' : 'flow_separate'))
            .enabled(this.look.ready && this.appearanceCurrent(this.model,this.appearanceRevision,this.model.scope(),this.model.connectionGeneration()))
            .onSelect(this.appearanceSelector('phoneNavigation',this.appearanceRevision)).id('appearance.phone-navigation')
        }.width('100%').constraintSize({minHeight:Tokens.touch}).padding({left:Tokens.page,right:Tokens.page})
      }
      .width('100%')
      .clip(true)
      .backgroundColor(Colors.surface)
      .borderRadius(PhoneStyle.cardRadius)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(nativeEnvironment.dark), radius: PhoneStyle.cardRadius })
      .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
    }
  }
  @Builder appearancePage() {
    List() {
      if (!this.agentLayoutOnly) this.appearanceGeneral()
      ListItem() { PhoneSectionTitle({ text: tr('agents') }) }
      ListItem() {
        Column() {
          Flex({ wrap: FlexWrap.Wrap, justifyContent: FlexAlign.SpaceBetween, alignItems: ItemAlign.Center, space: { main: LengthMetrics.vp(Tokens.gap), cross: LengthMetrics.vp(Tokens.gap) } }) {
            Text(tr('agent_density')).fontSize(Tokens.body).fontColor(Colors.ink).flexShrink(0).constraintSize({ maxWidth: '100%' })
            Select([{ value: tr('agent_density_standard') }, { value: tr('agent_density_compact') }]).attributeModifier(SelectStyles.inline)
              .selected(this.look.density === 'compact' ? 1 : 0)
              .value(tr(this.look.density === 'compact' ? 'agent_density_compact' : 'agent_density_standard'))
              .flexShrink(0).constraintSize({ minHeight: Tokens.touch, maxWidth: '100%' }).enabled(this.look.ready && this.appearanceCurrent(this.model, this.appearanceRevision, this.model.scope(), this.model.connectionGeneration()))
              .onSelect(this.appearanceSelector('density', this.appearanceRevision)).id('appearance.agent-density')
          }.constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.fine, bottom: Tokens.fine }).width('100%')
          Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
          Flex({ wrap: FlexWrap.Wrap, justifyContent: FlexAlign.SpaceBetween, alignItems: ItemAlign.Center, space: { main: LengthMetrics.vp(Tokens.gap), cross: LengthMetrics.vp(Tokens.gap) } }) {
            Text(tr('grouping')).fontSize(Tokens.body).fontColor(Colors.ink).flexShrink(0).constraintSize({ maxWidth: '100%' })
            if (this.agentWorkspace.isBoundTo(this.model) && !this.agentWorkspace.loading) {
              Select([{ value: tr('agent_group_state') }, { value: tr('agent_filter_machine') }, { value: tr('agent_group_space') }]).attributeModifier(SelectStyles.inline)
                .selected(this.agentGroupingIndex(this.agentWorkspace))
                .value(tr(['agent_group_state', 'agent_filter_machine', 'agent_group_space'][this.agentGroupingIndex(this.agentWorkspace)]))
                .flexShrink(0).constraintSize({ minHeight: Tokens.touch, maxWidth: '100%' }).enabled(this.appearanceCurrent(this.model, this.appearanceRevision, this.model.scope(), this.model.connectionGeneration()))
                .onSelect(this.appearanceSelector('group', this.appearanceRevision)).id('appearance.agent-grouping')
            } else LoadingProgress().width(Tokens.touch).height(Tokens.touch).accessibilityText(tr('loading'))
          }.constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.fine, bottom: Tokens.fine }).width('100%')
          if (this.model.nextSuggestionsValue !== null) {
            Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
            Row({ space: Tokens.inset }) {
              Column({ space: Tokens.fine }) {
                Text(tr('next_suggestions_title'))
                  .fontSize(Tokens.body)
                  .maxFontScale(Tokens.interfaceFontMaxScale)
                  .fontColor(Colors.ink)
                  .width('100%')
                Text(tr('next_suggestions_note'))
                  .fontSize(Tokens.caption)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
                  .fontColor(Colors.secondary)
                  .width('100%')
              }.layoutWeight(1).alignItems(HorizontalAlign.Start)
              Toggle({ type: ToggleType.Switch, isOn: this.model.nextSuggestionsValue === true })
                .selectedColor(Colors.action)
                .enabled(!this.model.nextSuggestionsSaving && (this.model.connected || this.model.demo))
                .onChange((value: boolean) => {
                  if (this.model.nextSuggestionsValue !== null && value !== this.model.nextSuggestionsValue) {
                    Haptics.selection();
                    this.model.setNextSuggestions(value).catch(() => undefined);
                  }
                })
                .accessibilityText(tr('next_suggestions_title'))
                .id('appearance.next-suggestions')
            }.width('100%').constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.fine, bottom: Tokens.fine })
          }
          if (this.model.agentsDisplaySupported) {
            Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
            Row({ space: Tokens.inset }) {
              Column({ space: Tokens.fine }) {
                Text(tr('agents_use_herdr_tab_name'))
                  .fontSize(Tokens.body)
                  .maxFontScale(Tokens.interfaceFontMaxScale)
                  .fontColor(Colors.ink)
                  .width('100%')
                Text(tr('agents_use_herdr_tab_name_hint'))
                  .fontSize(Tokens.caption)
                  .maxFontScale(Tokens.interfaceScaleFor(Tokens.caption))
                  .fontColor(Colors.secondary)
                  .width('100%')
              }.layoutWeight(1).alignItems(HorizontalAlign.Start)
              Toggle({ type: ToggleType.Switch, isOn: this.model.useHerdrTabName })
                .selectedColor(Colors.action)
                .enabled(!this.model.agentsDisplaySaving && this.model.connected && !this.model.demo)
                .onChange((value: boolean) => {
                  if (value !== this.model.useHerdrTabName) {
                    Haptics.selection();
                    this.model.setUseHerdrTabName(value);
                  }
                })
                .accessibilityText(tr('agents_use_herdr_tab_name'))
                .id('appearance.use-herdr-tab-name')
            }.width('100%').constraintSize({ minHeight: Tokens.touch }).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.fine, bottom: Tokens.fine })
          }
        }
        .width('100%')
        .clip(true)
        .backgroundColor(Colors.surface)
        .borderRadius(PhoneStyle.cardRadius)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(nativeEnvironment.dark), radius: PhoneStyle.cardRadius })
        .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
      }
    }.width('100%').height('100%').padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap, bottom: 28 }).backgroundColor(Colors.grouped)
  }

  @Builder notificationsPage() {
    List() {
      ListItem() {
        Column() {
          Row({ space: Tokens.inset }) {
            Text(tr('message_alerts'))
              .fontSize(Tokens.body)
              .maxFontScale(Tokens.interfaceFontMaxScale)
              .fontColor(Colors.ink)
              .layoutWeight(1)
            Toggle({ type: ToggleType.Switch, isOn: this.model.alerts && this.notifications.granted })
              .selectedColor(Colors.positive)
              .enabled(!this.notifications.changing && !this.model.demo)
              .onChange((value: boolean) => { Haptics.tap(); this.notifications.setEnabled(this.model, value); })
              .accessibilityText(tr('message_alerts'))
              .id('settings.notifications.alerts')
          }
          .width('100%')
          .constraintSize({ minHeight: 54 })
          .padding({ left: Tokens.page, right: Tokens.page, top: 6, bottom: 6 })
        }
        .width('100%')
        .backgroundColor(Colors.surface)
        .borderRadius(PhoneStyle.cardRadius)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(nativeEnvironment.dark), radius: PhoneStyle.cardRadius })
        .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
      }.padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: Tokens.gap })
      if (this.notifications.error) ListItem() { Text(trError(this.notifications.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.page) }
      ListItem() { LiveViewSettingsCard() }
    }.width('100%').height('100%').backgroundColor(Colors.grouped)
  }

  @Builder privacyPage() {
    List({ space: 12 }) {
      ListItem() {
        Column() {
          Row({ space: Tokens.inset }) {
            Text(tr('hub')).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1)
            Text(this.model.profile.name).fontSize(15).fontColor(Colors.secondary).maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis })
          }.width('100%').constraintSize({ minHeight: 52 }).padding({ left: Tokens.page, right: Tokens.page })
          Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
          Row({ space: Tokens.inset }) {
            Text(tr('manager')).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1)
            Text(this.model.profile.manager).fontSize(15).fontColor(Colors.secondary).maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis })
          }.width('100%').constraintSize({ minHeight: 52 }).padding({ left: Tokens.page, right: Tokens.page })
          Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
          Button({ type: ButtonType.Normal }) {
            Row({ space: Tokens.inset }) {
              Text(tr('open_source_licenses')).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1)
              SymbolGlyph($r('sys.symbol.chevron_right')).fontSize(Tokens.meta).fontColor([Colors.secondary])
            }.width('100%').constraintSize({ minHeight: 52 })
          }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).width('100%').padding({ left: Tokens.page, right: Tokens.page }).onClick(() => { Haptics.tap(); this.licenseNoticesOpen = true; })
        }
        .width('100%')
        .clip(true)
        .backgroundColor(Colors.surface)
        .borderRadius(PhoneStyle.cardRadius)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(nativeEnvironment.dark), radius: PhoneStyle.cardRadius })
        .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
      }
      ListItem() {
        Row({ space: 12 }) {
          Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('forget_hub')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('forget_hub', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('forget_hub')).bindTips(tr('forget_hub')).onClick(() => { Haptics.attention(); this.confirmForget(); })
          if (this.model.pendingCleanupCount > 0) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry_cleanup')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry_cleanup', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry_cleanup')).bindTips(tr('retry_cleanup')).onClick(() => { Haptics.tap(); this.model.retryCleanup(); })
        }.width('100%').padding({ left: 4, top: 4 })
      }
    }.width('100%').height('100%').padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: 24 }).backgroundColor(Colors.grouped)
  }

  @Builder licenseNoticesForm() { LicenseNotices() }

  @Builder candidateAddressesHeader() { PhoneSectionTitle({ text: tr('candidate_addresses') }) }
  @Builder diagnosticsPage() {
    List({ space: 12 }) {
      ListItem() {
        Column() {
          Row({ space: Tokens.inset }) {
            Text(tr('status')).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1)
            Row({ space: 6 }) {
              Circle().width(8).height(8).fill(this.model.connected ? Colors.positive : Colors.secondary)
              Text(tr(this.model.connected ? 'online' : 'offline')).fontSize(15).fontWeight(FontWeight.Medium).fontColor(this.model.connected ? Colors.positive : Colors.secondary)
            }
          }.width('100%').constraintSize({ minHeight: 52 }).padding({ left: Tokens.page, right: Tokens.page })
          Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
          Row({ space: Tokens.inset }) {
            Text(tr('hub')).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1)
            Text(this.model.profile.hubId).fontSize(14).fontColor(Colors.secondary).maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis })
          }.width('100%').constraintSize({ minHeight: 52 }).padding({ left: Tokens.page, right: Tokens.page })
          Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
          Column({ space: 4 }) {
            Text(this.model.profile.baseUrl).fontSize(14).fontColor(Colors.link).copyOption(CopyOptions.LocalDevice).width('100%')
            if (this.model.features.length > 0) {
              Text(tr('features') + ': ' + this.model.features.join(', ')).fontSize(13).fontColor(Colors.secondary).width('100%')
            }
          }.width('100%').padding({ left: Tokens.page, right: Tokens.page, top: 12, bottom: 12 }).alignItems(HorizontalAlign.Start)
        }
        .width('100%')
        .clip(true)
        .backgroundColor(Colors.surface)
        .borderRadius(PhoneStyle.cardRadius)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(nativeEnvironment.dark), radius: PhoneStyle.cardRadius })
        .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
      }
      if (this.model.profile.addresses.length > 0) ListItemGroup({ header: this.candidateAddressesHeader }) {
        ListItem() {
          Column() {
            ForEach(this.model.profile.addresses, (address: string, idx: number) => {
              Column() {
                Text(address).fontSize(14).fontColor(Colors.ink).copyOption(CopyOptions.LocalDevice).width('100%').padding({ left: Tokens.page, right: Tokens.page, top: 12, bottom: 12 })
                if (idx < this.model.profile.addresses.length - 1) Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({ left: Tokens.page })
              }
            }, (address: string) => address)
          }
          .width('100%')
          .clip(true)
          .backgroundColor(Colors.surface)
          .borderRadius(PhoneStyle.cardRadius)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(nativeEnvironment.dark), radius: PhoneStyle.cardRadius })
          .shadow(PhoneStyle.cardElevation(nativeEnvironment.dark))
        }
      }
      if (this.model.error) ListItem() { Text(trError(this.model.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset) }
      ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('refresh')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('refresh', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('refresh')).bindTips(tr('refresh')).onClick(() => { Haptics.tap(); if (this.model.connected) this.model.refresh(); else this.model.foregrounded(); this.management.load(this.page, this.model); }) }
    }.width('100%').height('100%').padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: 24 }).backgroundColor(Colors.grouped)
  }

  @Builder delegatedPage() {
    List() {
      ListItem() { Button({ type: ButtonType.Normal }) { Row({ space: Tokens.inset }) { Text(tr(this.page)).fontSize(Tokens.body).fontColor(Colors.ink).layoutWeight(1); SymbolGlyph($r('sys.symbol.chevron_right')).fontSize(Tokens.meta).fontColor([Colors.secondary]) }.width('100%').constraintSize({ minHeight: Tokens.touch }) }.buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).padding({ left: Tokens.page, right: Tokens.page }).onClick(() => this.onOpen(this.page)) }
    }.width('100%').height('100%')
  }

  build() {
    Column() {
      if (this.management.loading) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
      if (this.page === 'models') this.modelCatalogPage()
      else if (this.page === 'model_purpose' || this.page === 'translation') this.modelsPage()
      else if (this.page === 'session_permissions') this.sessionModePage('permission_mode')
      else if (this.page === 'session_effort') this.sessionModePage('effort')
      else if (this.page === 'session_model') this.sessionModePage('model')
      else if (this.page === 'machines') this.machinesPage()
      else if (this.page === 'schedules') this.schedulesPage()
      else if (this.page === 'triage') this.triagePage()
      else if (this.page === 'extension_detail') this.extensionDetailPage()
      else if (this.page === 'connector_detail') this.connectorDetailPage()
      else if (this.page === 'channel_detail') this.channelDetailPage()
      else if (this.page === 'extension_catalog') this.extensionCatalogPage()
      else if (this.page === 'extensions' || this.page === 'connectors') this.extensionConnectorPage(this.page)
      else if (this.page === 'channels') this.channelsPage()
      else if (this.page === 'ledger') this.ledgerPage()
      else if (this.page === 'appearance' && this.phoneAppearanceRoute) this.phoneLookPage()
      else if (this.page === 'appearance') this.appearancePage()
      else if (this.page === 'notifications') this.notificationsPage()
      else if (this.page === 'privacy') this.privacyPage()
      else if (this.page === 'diagnostics') this.diagnosticsPage()
      else this.delegatedPage()
      if (this.management.error && !['models', 'session_permissions', 'session_effort', 'session_model', 'machines', 'schedules', 'triage', 'extensions', 'connectors', 'channels', 'ledger', 'extension_detail', 'connector_detail', 'channel_detail', 'extension_catalog'].includes(this.page))
        Text(trError(this.management.error)).fontColor($r('app.color.persona_danger')).padding(Tokens.inset)
    }.width('100%').height('100%')
      .bindSheet($$this.extensionPreviewOpen, this.extensionPreviewForm(), { title: { title: tr(Wire.bool(this.management.preview, 'upgrade') ? 'upgrade' : 'install') + ' · ' + tr('permissions') }, height: SheetSize.LARGE, showClose: true })
      .bindSheet($$this.licenseNoticesOpen, this.licenseNoticesForm(), { title: { title: tr('open_source_licenses') }, height: SheetSize.LARGE, showClose: true })
  }
}
