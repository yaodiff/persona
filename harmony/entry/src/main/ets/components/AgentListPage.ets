import { LengthMetrics } from '@kit.ArkUI';
import { PhoneGroupingGlyph } from './PhoneGroupingGlyph';
import { IconActions } from '../design/IconActions';
import { util } from '@kit.ArkTS';
import { ClientModel } from '../models/ClientModel';
import { AgentDecisionCard, AgentDecisionQueue } from '../models/AgentDecisionQueue';
import { AgentWorkspace, AgentWorkspaceGroup, agentGroupingLabels } from '../models/AgentWorkspace';
import { ProjectAgent, ProjectCatalog, ProjectWorktree } from '../models/ProjectCatalog';
import { AgentDirectoryRowContent } from './AgentDirectoryRowContent';
import { AgentNeedsYouMark } from './AgentNeedsYouMark';
import { NeedsYouPulseDot, WorkingArcSpinner } from './StatusMotionMarks';
import { recommendedOptionTitle, interactionToolName } from '../models/InteractionDecision';
import { agentPaneSource } from '../core/AgentListAdapter';
import { herdrTabName } from '../core/SessionNaming';
import { WorktreeChange } from '../models/WorktreeActions';
import { AgentRepositoryRows } from './AgentRepositoryRows';
import { AgentTaskGrouping, agentTaskSectionsFor } from '../models/AgentTaskGrouping';
import { AgentTaskSection } from '../models/AgentTaskSections';
import { AgentTaskGroupRows } from './AgentTaskGroupRows';
import { ResourceRow, Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { Tokens } from '../design/Tokens';
import { PhoneStyle } from '../design/PhoneStyle';
import { Colors } from '../design/Colors';
import { ButtonStyles, SelectStyles } from '../design/Controls';
import { Symbols } from '../design/Symbols';
import { tr, trError } from '../design/Strings';
import { nativeEnvironment, NativeEnvironment } from '../platform/NativeEnvironment';
import { nativeSheetInset } from '../platform/NativeInsets';
import { DeviceLook, deviceLook } from '../models/DeviceLook';
import { Motion } from '../design/Motion';
import { MotionTokens } from '../core/MotionTokens';
import { Haptics } from '../platform/Haptics';
import { nativeLiveView } from '../platform/NativeLiveView';
import { toastCenter } from '../models/ToastCenter';
import { PersonaStatusPill } from '../design/NormalizedPrimitives';
import { computePersonalAgentEntry, parsePersonalAgentSessionsPage, parsePersonalAgentSources, shouldShowPersonalAgentsHeading } from '../core/PersonalAgents';
import { PersonalAgentsSheet } from './PersonalAgentsSheet';

const QUICK_FILTERS: string[] = ['all', 'needsYou', 'unread', 'working', 'backgroundWaiting', 'feishu', 'terminal'];
const QUICK_LABELS: Record<string,string> = { 'all':'all', 'needsYou':'needs_you', 'unread':'agent_group_unread', 'working':'working', 'backgroundWaiting':'agent_group_background', 'feishu':'agent_source_lark_group', 'terminal':'terminal' };
const STATE_LABELS: Record<string, string> = {'pinned': 'agent_group_pinned', 'state/needs': 'needs_you',
  'state/unread': 'agent_group_unread', 'state/awaiting': 'agent_group_awaiting', 'state/working': 'working',
  'state/background': 'agent_group_background', 'state/just_finished': 'agent_group_just_finished',
  'state/waiting': 'agent_group_waiting', 'state/muted': 'agent_group_muted',
  'state/idle': 'agent_group_idle'};

@ObservedV2
class AgentRowPresentation {
  @Trace raw: string = '';
  @Trace lastText: string = '';
  @Trace rawStatus: string = '';
  @Trace updatedAt: number = Number.NaN;
  @Trace worktree: string = '';
  @Trace repositoryRoot: string = '';
  @Trace cwd: string = '';
  @Trace machineId: string = '';
  @Trace sessionId: string = '';
  @Trace spaceName: string = '';
  @Trace herdrTabName: string = '';
  @Trace recentContent: string = '';
  @Trace provider: string = '';
  @Trace brand: string = '';
  @Trace status: string = '';
  @Trace metadata: string[] = [];
  @Trace selectionKey: string = '';
  @Trace activity: ProjectAgent | undefined = undefined;
  @Trace activitySource: string = '';
  private activityRaw: string = '';
  prepareActivity(row:ResourceRow): void {
    if (this.activity && this.activityRaw === row.raw && this.activity.row === row) return;
    const data = row.data(), source=agentPaneSource(Wire.text(data,'pane_id'));
    this.activity = new ProjectAgent(row,data);
    this.activitySource = source === 'feishu' ? 'agent_source_lark_group' : source === 'terminal' ? 'terminal' : '';
    this.activityRaw = row.raw;
  }
}

@ObservedV2
class BoundAgentRow {
  readonly identity: string;
  readonly key: string;
  @Trace bound: BoundRow<ResourceRow>;
  @Trace first: boolean = false;
  @Trace last: boolean = false;
  @Trace presentation: AgentRowPresentation;

  constructor(bound: BoundRow<ResourceRow>) {
    this.identity = bound.identity; this.key = bound.key; this.bound = bound; this.presentation = new AgentRowPresentation();
  }
}

@ObservedV2
class BoundAgentGroup {
  readonly identity: string;
  readonly key: string;
  @Trace value: AgentWorkspaceGroup;
  @Trace rows: BoundAgentRow[] = [];

  constructor(identity: string, key: string, value: AgentWorkspaceGroup) { this.identity = identity; this.key = key; this.value = value; }
}

@ComponentV2
export struct AgentListPage {
  @Require @Param model: ClientModel;
  @Param rail: boolean = false;
  @Param compactRoot: boolean = false;
  @Param readingDetail: boolean = false;
  @Param active: boolean = true;
  @Param retainWorkspace: boolean = false;
  private editingAdmission: (() => boolean) | undefined;
  @Param selectedRow: ResourceRow = new ResourceRow();
  @Param selectionVisible: boolean = true;
  @Local refreshing: boolean = false;
  @Local environment: NativeEnvironment = nativeEnvironment;
  @Event onOpen: (row: ResourceRow) => void = () => {};
  @Event onOpenTodo: (row: ResourceRow) => void = () => {};
  @Event onRepositoryOpen: (kind:string,key:string) => void = () => {};
  @Event onRepositoryChanged: (tree:ProjectWorktree,change:WorktreeChange) => void = () => {};
  @Event onSearchMode: (editing: boolean) => void = () => {};
  @Event onVisible: (row: ResourceRow, ratio: number) => void = () => {};
  @Param workspace: AgentWorkspace = new AgentWorkspace();
  @Param projectCatalog: ProjectCatalog = new ProjectCatalog();
  @Param taskGrouping: AgentTaskGrouping = new AgentTaskGrouping();
  @Param externalSearchControls:boolean = false;
  @Local taskRowsByKey: Map<string,BoundAgentRow> = new Map();
  @Local look: DeviceLook = deviceLook;
  @Computed get displayedAgentStyle():string {
    if(this.look.agentDisplay==='list')return 'list';
    return this.model.style==='beautiful'?'beautiful':this.look.agentDisplay;
  }
  @Monitor('displayedAgentStyle') displayChanged():void {
    this.workspace.setCompactDisplay(this.displayedAgentStyle==='list');
  }
  @Local groupBindings: BoundAgentGroup[] = [];
  @Local railBindings: BoundAgentRow[] = [];
  @Local mixedMachines: boolean = false;
  @Local listWidth:number=0;
  private narrowRow():boolean { return this.listWidth>0 && this.listWidth<252*Math.min(this.environment.fontScale,Tokens.interfaceScaleFor(15)); }
  @Param decisions: AgentDecisionQueue = new AgentDecisionQueue();
  private stopDecisionEvents: (()=>void) | undefined;
  private decisionQueued: number = -1;
  private reloadDecisions(): void {
    clearTimeout(this.decisionQueued);
    if (this.externalSearchControls || !this.active || this.rail || !this.compactRoot) return;
    const owner = this.model;
    this.decisionQueued=setTimeout(() => this.decisions.reload(owner).then(() => {
      if (this.active && this.model === owner) this.workspace.updateSupervisorCards(owner,this.decisions);
    }).catch(() => {}),50);
  }
  private retireDecisions(): void {
    clearTimeout(this.decisionQueued);
    if (this.externalSearchControls) return;
    this.decisions.clear(); this.workspace.updateSupervisorCards(this.model);
  }

  @Local searchOpen: boolean = false;
  @Local searchEditing: boolean = false;
  @Local pageOriginY: number = -1;
  @Local private pressedChoiceKey: string = '';
  private searchField: SearchController = new SearchController();
  private quickScroller: Scroller = new Scroller();
  private agentScroller: Scroller = new Scroller();
  private pullY: number = -1;
  private pullX: number = 0;
  private revealedDuringPull: boolean = false;
  private focusSearch: boolean = false;
  private focusTimer: number = -1;
  private rowBindingOwner: Object | undefined;
  private rowBindingScope: string = '';
  private groupBindingSequence: number = 0;
  @Local editorOpen: boolean = false;
  @Local editingRow: ResourceRow | undefined;
  @Local renameTitle: string = '';
  @Local renameTab: string = '';
  @Local filterSheetOpen: boolean = false;
  @Local filterInset: number = 0;
  @Local personalAgentCount: number = 0;
  @Local personalAgentsSheetOpen: boolean = false;
  private personalAgentSerial: number = 0;
  private filterReady: boolean = false;
  private filterNodeId: string = 'agents.filters.' + util.generateRandomUUID();

  @Monitor('environment.safeBottomPx') filterBoundaryChanged(): void { this.measureFilters(); }
  private measureFilters(): void {
    if (!this.filterReady) return;
    const value = nativeSheetInset(this.getUIContext(), this.filterNodeId, this.environment.safeBottomPx);
    if (Math.abs(value - this.filterInset) > 0.1) this.filterInset = value;
  }

  aboutToAppear(): void {
    this.workspace.setCompactDisplay(this.displayedAgentStyle==='list');
    this.stopDecisionEvents=this.model.observeEvents((_payload:string, type:string) => {
      if (['interaction','interactions','interaction_resolved'].includes(type)) this.reloadDecisions();
      if (['todo','assistant_feed'].includes(type) && ['todo','direction'].includes(this.workspace.groupingMode)) this.refreshGroupingSupport();
    });
    this.reloadDecisions(); this.syncRowBindings(); if (this.rail) this.syncRailBindings(); if (this.active) this.activateWorkspace();
    this.refreshPersonalAgents().catch(() => undefined); }
  aboutToDisappear(): void { if (!this.externalSearchControls) this.taskGrouping.clear(); this.stopDecisionEvents?.(); this.retireDecisions(); this.resetSearchReveal(); this.editorOpen = false; this.filterSheetOpen = false; this.personalAgentsSheetOpen = false; this.editingAdmission = undefined; this.workspace.revokeRowActions(); if (!this.retainWorkspace) this.workspace.hide(); }

  @Monitor('active') visibleChanged(): void { if (this.active) { this.reloadDecisions(); this.refreshPersonalAgents().catch(() => undefined); } else this.retireDecisions(); if (!this.active) { this.resetSearchReveal(); this.editorOpen = false; this.filterSheetOpen = false; this.personalAgentsSheetOpen = false; this.editingRow = undefined; this.editingAdmission = undefined; this.workspace.revokeRowActions(); if (!this.retainWorkspace) this.workspace.hide(); } else this.activateWorkspace(); }
  @Monitor('model.forgottenEpoch') forgottenChanged(): void { this.retireDecisions(); this.reloadDecisions(); this.workspace.forgetHub(this.model.forgottenProfile); if (this.editingRow?.group === this.model.forgottenProfile) { this.editorOpen = false; this.editingRow = undefined; this.editingAdmission = undefined; } }
  @Monitor('model.profile') profileChanged(): void { this.taskGrouping.clear(); this.retireDecisions(); this.reloadDecisions(); this.resetSearchReveal(); this.editorOpen = false; this.personalAgentsSheetOpen = false; this.personalAgentCount = 0; this.editingAdmission = undefined; this.syncRowBindings(); if (!this.active) return; this.activateWorkspace(); this.refreshPersonalAgents().catch(() => undefined); }
  @Monitor('selectedRow') externalSelectionChanged(): void { this.workspace.selectExternal(this.selectedRow); }
  @Monitor('model.directorySessions', 'model.directoryReady') directoryChanged(): void { if (!this.active) return; this.reloadDecisions(); this.workspace.update(this.model, this.readingDetail); this.workspace.selectExternal(this.selectedRow); }
  @Monitor('model.sessionsReady') catalogChanged(): void { if (!this.active) return; this.workspace.update(this.model, this.readingDetail); }
  @Monitor('model.sessions') sessionsChanged(): void { if (!this.active) return; this.workspace.update(this.model, this.readingDetail); }
  @Monitor('model.selected') selectionChanged(): void { if (!this.active) return; this.workspace.update(this.model, this.readingDetail); }
  @Monitor('model.machines') machinesChanged(): void { if (!this.active) return; this.workspace.update(this.model, this.readingDetail); }
  @Monitor('model.features') featuresChanged(): void {
    if (!this.active) return; this.workspace.update(this.model, this.readingDetail);
    this.refreshGroupingSupport();
    this.workspace.refresh(this.model).catch(() => undefined);
  }
  @Monitor('model.connected') connectionChanged(): void {
    this.reloadDecisions(); if (!this.active) return; this.workspace.update(this.model, this.readingDetail);
    if (this.model.connected) { this.refreshGroupingSupport(); this.workspace.refresh(this.model).catch(() => undefined); this.refreshPersonalAgents().catch(() => undefined); }
  }
  @Monitor('workspace.groups', 'workspace.groupingMode') rowsChanged(): void { this.syncRowBindings(); }
  @Monitor('rail', 'groupBindings', 'workspace.unpinnedRows') railBindingsChanged(): void { this.syncRailBindings(); }
  @Monitor('readingDetail') detailChanged(): void { this.workspace.setReadingDetail(this.readingDetail); }

  private syncRailBindings(): void {
    if (!this.rail) {
      if (this.railBindings.length) this.railBindings = [];
      return;
    }
    const result: BoundAgentRow[] = [];
    const seen = new Set<string>();
    const groupSet = new Set<BoundAgentRow>();
    for (const group of this.groupBindings) {
      for (const rowBinding of group.rows) {
        groupSet.add(rowBinding);
        const key = rowBinding.bound.value.key();
        if (!seen.has(key)) {
          seen.add(key);
          result.push(rowBinding);
        }
      }
    }
    const extraRows: ResourceRow[] = [];
    const unpinned = this.workspace.unpinnedRows || [];
    for (const row of unpinned) {
      if (row.kind !== 'session') continue;
      const key = row.key();
      if (!seen.has(key)) {
        seen.add(key);
        extraRows.push(row);
      }
    }
    if (extraRows.length) {
      const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager]);
      const prior = this.railBindings.filter((item: BoundAgentRow) => !groupSet.has(item));
      const bound = reconcileBoundRows(prior.map((item: BoundAgentRow) => item.bound), extraRows,
        (row: ResourceRow) => row.key(), this.model, this.model, scope, scope);
      for (const item of this.bindAgentRows(prior, bound, true)) result.push(item);
    }
    this.railBindings = result;
  }

  private refreshGroupingSupport(): void {
    if (!this.active) return;
    const client=this.model, scope=client.scope(), generation=client.connectionGeneration();
    this.taskGrouping.refresh(client).then(() => {
      if (this.active && this.model === client && client.owns(generation,scope) && this.workspace.isBoundTo(client))
        this.workspace.updateGroupingSupport(this.taskGrouping.supportsTodos,this.taskGrouping.supportsDirections);
    }).catch(() => {});
  }
  private activateWorkspace(): void {
    if (this.externalSearchControls) { this.syncRowBindings(); return; }
    this.refreshGroupingSupport();
    if (this.workspace.isBoundTo(this.model)) {
      this.workspace.update(this.model, this.readingDetail);
      this.workspace.selectExternal(this.selectedRow);
      this.syncRowBindings();
      return;
    }
    this.workspace.open(this.model, this.readingDetail).then(() => {
      if (!this.active || !this.workspace.isBoundTo(this.model)) return;
      this.workspace.updateGroupingSupport(this.taskGrouping.supportsTodos,this.taskGrouping.supportsDirections);
      this.workspace.selectExternal(this.selectedRow); this.syncRowBindings();
    }).catch(() => undefined);
  }

  private syncRowBindings(): void {
    this.mixedMachines = new Set(this.workspace.visibleRows().map((row: ResourceRow) => JSON.stringify([row.group, row.manager, row.machine]))).size > 1;
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager]);
    const sameOwnerScope = this.rowBindingOwner === this.model && this.rowBindingScope === scope;
    const previous = sameOwnerScope ? this.groupBindings : [];
    const available = new Map<string, BoundAgentGroup[]>();
    previous.forEach((binding: BoundAgentGroup) => {
      const bindings = available.get(binding.identity) || [];
      bindings.push(binding);
      available.set(binding.identity, bindings);
    });
    const occurrences = new Map<string, number>(), next: BoundAgentGroup[] = [];
    this.workspace.groups.forEach((group: AgentWorkspaceGroup) => {
      const occurrence = occurrences.get(group.id) || 0;
      occurrences.set(group.id, occurrence + 1);
      const identity = JSON.stringify([group.id, occurrence]);
      const matches = available.get(identity);
      const existing = matches && matches.length ? matches.shift() : undefined;
      let binding: BoundAgentGroup;
      if (existing) {
        binding = existing; binding.value = group;
        const groupRows = reconcileBoundRows(binding.rows.map((rowBinding: BoundAgentRow) => rowBinding.bound), group.rows,
          (row: ResourceRow) => row.key(), this.model, this.model, scope, scope);
        binding.rows = this.bindAgentRows(binding.rows, groupRows, true);
      } else {
        this.groupBindingSequence++;
        binding = new BoundAgentGroup(identity, 'agent-group-binding.' + String(this.groupBindingSequence), group);
        const groupRows = reconcileBoundRows([], group.rows, (row: ResourceRow) => row.key(), undefined, this.model, '', scope);
        binding.rows = this.bindAgentRows([], groupRows, true);
      }
      next.push(binding);
    });
    this.groupBindings = next;
    if (this.workspace.groupingMode === 'todo' || this.workspace.groupingMode === 'direction') {
      const prior = sameOwnerScope ? Array.from(this.taskRowsByKey.values()) : [];
      const taskRows = reconcileBoundRows(prior.map((binding:BoundAgentRow)=>binding.bound),this.workspace.unpinnedRows,
        (row:ResourceRow)=>row.key(),sameOwnerScope ? this.model : undefined,this.model,scope,scope);
      const mapped = new Map<string,BoundAgentRow>();
      for (const binding of this.bindAgentRows(prior,taskRows,true)) mapped.set(binding.bound.value.key(),binding);
      this.taskRowsByKey = mapped;
    } else if (this.taskRowsByKey?.size) this.taskRowsByKey = new Map();
    this.rowBindingOwner = this.model; this.rowBindingScope = scope;
  }

  private bindAgentRows(current: BoundAgentRow[], rows: BoundRow<ResourceRow>[], project: boolean): BoundAgentRow[] {
    const available = new Map<string, BoundAgentRow>();
    current.forEach((binding: BoundAgentRow) => available.set(binding.identity, binding));
    const result: BoundAgentRow[] = [];
    rows.forEach((bound: BoundRow<ResourceRow>, index: number) => {
      let binding = available.get(bound.identity);
      if (binding) { available.delete(bound.identity); binding.bound = bound; }
      else binding = new BoundAgentRow(bound);
      binding.first = index === 0; binding.last = index === rows.length - 1;
      if (project) this.updateAgentRowPresentation(binding.presentation, bound.value);
      result.push(binding);
    });
    return result;
  }

  private updateAgentRowPresentation(presentation: AgentRowPresentation, row: ResourceRow): void {
    if (presentation.raw !== row.raw) {
      const data = Wire.parse(row.raw);
      presentation.raw = row.raw;
      presentation.lastText = Wire.text(data, 'last_text');
      const agent = (Wire.text(data, 'agent') || Wire.text(data, 'agent_type')).toLowerCase();
      const label = Wire.text(data, 'agent_label') || Wire.text(data, 'launcher');
      const sourceKind = Wire.text(data, 'source_kind').toLowerCase();
      const known = agent || label.toLowerCase();
      presentation.brand = sourceKind === 'codex_app_server' || sourceKind === 'codex_history' ||
        known.includes('codex') || known.includes('openai') ? 'codex' :
        (known.includes('claude') ? 'claude' : '');
      presentation.provider = label || (presentation.brand === 'codex' ? 'Codex' : presentation.brand === 'claude' ? 'Claude' : agent);
      presentation.rawStatus = Wire.text(data, 'status');
      presentation.updatedAt = Date.parse(Wire.text(data, 'updated_at'));
      presentation.worktree = Wire.text(data, 'worktree');
      presentation.repositoryRoot = Wire.text(data, 'repo_root');
      presentation.cwd = Wire.text(data, 'cwd');
      presentation.machineId = Wire.text(data, 'machine_id');
      presentation.sessionId = Wire.text(data, 'session_id');
      presentation.spaceName = Wire.text(data, 'space_name');
      presentation.herdrTabName = herdrTabName(data);
    }
    if (this.compactRoot && !this.rail || this.workspace?.groupingMode === 'app' || this.workspace?.groupingMode === 'recent') presentation.prepareActivity(row);
    const state = row.state || presentation.rawStatus;
    const content = presentation.lastText || row.detail;
    const machineId = row.machine || presentation.machineId;
    const sessionId = row.session || presentation.sessionId || row.id;
    const path = row.path || presentation.worktree || presentation.repositoryRoot || presentation.cwd;
    const owner = this.model.directoryOwnerFor(row);
    const machine = machineId ? owner?.machineName(machineId) || machineId : '';
    const identity = [this.model.groupName(row), row.manager].filter((value: string) => !!value).join(' · ');
    const shortPath = path ? this.model.shortPath(path) : '';
    presentation.status = state;
    presentation.recentContent = content;
    presentation.metadata = [identity, machine, shortPath, state, content];
    presentation.selectionKey = JSON.stringify(row.group ? [row.group, row.kind, row.manager, machineId, sessionId] : [row.kind, row.manager, machineId, sessionId]);
  }

  private machineOptions(): ResourceRow[] {
    return (this.model.directoryReady ? this.model.directoryMachines : this.model.machines)
      .filter((row: ResourceRow) => this.workspace.includesHub(row.group));
  }

  private machineLabel(): string {
    if (!this.workspace.machineFilter) return tr('agent_filter_all_machines');
    const row = this.machineOptions().find((item: ResourceRow) => this.workspace.machineIdentity(item) === this.workspace.machineFilter || item.id === this.workspace.machineFilter);
    return row?.title || this.workspace.machineFilter;
  }

  private machineIndex(): number {
    if (!this.workspace.machineFilter) return 0;
    const index = this.machineOptions().findIndex((item: ResourceRow) =>
      this.workspace.machineIdentity(item) === this.workspace.machineFilter || item.id === this.workspace.machineFilter);
    return index < 0 ? 0 : index + 1;
  }

  private machineSelectOptions(): SelectOption[] {
    const options: SelectOption[] = [{ value: tr('agent_filter_all_machines') }];
    this.machineOptions().forEach((row: ResourceRow) => options.push({ value: [this.model.groupName(row), row.manager, row.title || row.id].filter((value: string) => !!value).join(' · ') }));
    return options;
  }

  private selectMachine(index: number): void {
    const machine = index > 0 ? this.machineOptions()[index - 1] : undefined;
    this.workspace.setMachineFilter(machine ? this.workspace.machineIdentity(machine) : '').catch(() => undefined);
  }

  @Computed get availableGroupings(): string[] {
    return this.workspace.groupingOptions();
  }
  @Computed get taskSections(): AgentTaskSection[] {
    return agentTaskSectionsFor(this.model,this.workspace,this.taskGrouping,this.workspace.groupingMode);
  }
  private groupingIndex(): number {
    return this.availableGroupings.indexOf(this.workspace.groupingMode);
  }

  private selectGrouping(index: number): void {
    const grouping=this.availableGroupings[index];
    if (grouping) this.workspace.setGroupingMode(grouping);
  }

  private openRow(row: ResourceRow): void {
    if (this.compactRoot) { this.searchField.stopEditing(); this.resetSearchReveal(); }
    this.workspace.selectRow(row); this.onOpen(row);
  }

  private openRename(row: ResourceRow): void {
    if (!this.workspace.canRename(row) || this.workspace.busy) return;
    this.editingAdmission = this.workspace.actionTicket(row); this.editingRow = row; this.renameTitle = row.title || Wire.text(row.data(), 'title');
    this.renameTab = Wire.text(row.data(), 'slot_name'); this.editorOpen = true;
  }

  private async saveRename(): Promise<void> {
    const row = this.editingRow;
    if (!row || this.workspace.busy) return;
    if (!this.active || !this.editingAdmission || !this.editingAdmission()) { this.editorOpen = false; this.editingRow = undefined; return; }
    const saved = await this.workspace.rename(row, this.renameTitle, this.renameTab);
    if (saved) { this.editorOpen = false; this.editingRow = undefined; }
  }

  private confirmClose(row: ResourceRow): void {
    if (!this.workspace.canClose(row) || this.workspace.busy) return;
    const scope = this.model.scope(), generation = this.model.connectionGeneration(), key = row.key(), admitted = this.workspace.actionTicket(row);
    this.getUIContext().showAlertDialog({ title: tr('close'), message: row.title || row.id,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('close'), action: () => {
        if (!this.active || !admitted() || !this.model.owns(generation, scope) || row.key() !== key) return;
        this.workspace.close(row).catch(() => undefined);
      } } });
  }

  private confirmDelete(row: ResourceRow): void {
    if (!this.workspace.canDelete(row) || this.workspace.busy) return;
    const scope = this.model.scope(), generation = this.model.connectionGeneration(), key = row.key(), admitted = this.workspace.actionTicket(row);
    this.getUIContext().showAlertDialog({ title: tr('delete'), message: row.title || row.id,
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: { value: tr('delete'), action: () => {
        if (!this.active || !admitted() || !this.model.owns(generation, scope) || row.key() !== key) return;
        this.workspace.delete(row).catch(() => undefined);
      } } });
  }

  private async triggerAutoRename(row: ResourceRow): Promise<void> {
    const label = row.title || tr('agent_untitled');
    const err = await this.model.autoRenameAgent(row);
    if (err && this.active) {
      Haptics.error();
      toastCenter.error(trError(err) + ' · ' + label);
    }
  }

  @Builder rowMenu(binding:BoundAgentRow) { this.sessionMenu(binding.bound.value,binding.presentation.spaceName) }
  @Builder sessionMenu(row:ResourceRow,spaceName:string) {
    Menu() {
      MenuItem({ content: tr(this.workspace.isPinned(row) ? 'agent_unpin' : 'agent_pin') })
        .enabled(this.workspace.rowOnline(row) && !this.workspace.busy)
        .onClick(() => this.workspace.togglePin(row).catch(() => undefined))
      MenuItem({ content: tr(this.workspace.isMuted(row) ? 'unmute' : 'mute') })
        .enabled(this.workspace.rowOnline(row) && !this.workspace.busy)
        .onClick(() => this.workspace.toggleMute(row).catch(() => undefined))
      if (this.workspace.isSpacePinned(row) || spaceName) {
        MenuItem({ content: tr(this.workspace.isSpacePinned(row) ? 'agent_unpin_space' : 'agent_pin_space') })
          .enabled(this.workspace.rowOnline(row) && !this.workspace.busy)
          .onClick(() => this.workspace.togglePin(row, true).catch(() => undefined))
        MenuItem({ content: tr(this.workspace.isSpaceMuted(row) ? 'agent_unmute_space' : 'agent_mute_space') })
          .enabled(this.workspace.rowOnline(row) && !this.workspace.busy)
          .onClick(() => this.workspace.toggleMute(row, true).catch(() => undefined))
      }
      if (this.workspace.canMarkSeen(row)) MenuItem({ content: tr('agent_mark_seen') })
        .enabled(!this.workspace.busy).onClick(() => this.workspace.markSeen(row).catch(() => undefined))
      if (this.workspace.canRename(row)) MenuItem({ content: tr('rename') }).enabled(!this.workspace.busy)
        .onClick(() => this.openRename(row))
      if (this.model.canAutoRenameAgent(row)) {
        MenuItem({ content: tr('agent_auto_rename') })
          .enabled(!this.workspace.busy && !this.model.isAutoRenamingRow(row))
          .onClick(() => { void this.triggerAutoRename(row); })
          .id('agents.menu.autoname')
      }
      if (this.workspace.canClose(row)) MenuItem({ content: tr('close') }).enabled(!this.workspace.busy)
        .onClick(() => this.confirmClose(row))
      if (this.workspace.canDelete(row)) MenuItem({ content: tr('delete') }).enabled(!this.workspace.busy)
        .onClick(() => this.confirmDelete(row))
    }
  }

  private groupTitle(group:AgentWorkspaceGroup):string {
    if (group.titleKey) return tr(group.titleKey).replace('{agent}',group.machineName);
    return tr(STATE_LABELS[group.id] || '') || group.machineName;
  }
  private appCountKeys(group:AgentWorkspaceGroup):string[] {
    return ['needs_you','working','unread','backgroundWaiting','idle'].filter((key:string)=>Wire.number(group.stateCounts,key)>0);
  }
  private appCountColor(key:string):ResourceColor {
    return key === 'needs_you' ? Colors.needs : key === 'working' || key === 'backgroundWaiting' ? Colors.working : key === 'unread' ? Colors.unread : Colors.tertiary;
  }
  private appCountLabel(group:AgentWorkspaceGroup,key:string):string {
    const name = key === 'unread' ? 'agent_group_unread' : key === 'backgroundWaiting' ? 'agent_group_background' : key;
    return String(Wire.number(group.stateCounts,key)) + ' ' + tr(name);
  }
  @Builder activityMark(group:AgentWorkspaceGroup) {
    if (group.id === 'app/desktop/claude code') Image($r('app.media.project_agent_claude')).width(18).height(18).objectFit(ImageFit.Contain)
    else if (group.id === 'app/desktop/codex') Image($r('app.media.project_agent_openai')).width(18).height(18).objectFit(ImageFit.Contain).fillColor(Colors.ink)
    else if (group.id.startsWith('app/desktop/') && group.presentation!=='app-project') Text(group.machineName.slice(0,1).toUpperCase()).fontSize(15).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
    else if (group.presentation === 'app-project') SymbolGlyph(Symbols.folder).fontSize('16vp').fontColor([Colors.secondary])
    else if (group.id === 'app/other') SymbolGlyph(Symbols.agents).fontSize('16vp').fontColor([Colors.secondary])
    else Text('›_').fontSize('11vp').fontFamily('monospace').fontWeight(FontWeight.Medium).fontColor(Colors.secondary)
      .width(18).height(14).textAlign(TextAlign.Center).border({width:1,color:Colors.secondary,radius:3}).accessibilityLevel('no')
  }
  @Builder needsYouHeader(binding:BoundAgentGroup) {
    Button({type:ButtonType.Normal}) {
      Row({space:6}) {
        Text(this.groupTitle(binding.value)).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(600)
          .fontColor(Colors.systemControls && Colors.styleID==='glass' ? $r('app.color.phone_agent_section_ink') : Colors.secondary)
          .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        if (binding.value.totalRowCount>0) {
          Text(binding.value.totalRowCount>99?'99+':String(binding.value.totalRowCount)).fontSize(13)
            .maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Bold).textAlign(TextAlign.Center)
            .fontColor(Colors.systemControls && Colors.styleID==='glass' ? $r('app.color.phone_agent_count_ink') : Colors.needs)
            .padding({left:6,right:6}).height(PhoneStyle.formSize(18.33,25.67,this.environment.fontScale))
            .constraintSize({minWidth:PhoneStyle.formSize(20.33,23.67,this.environment.fontScale)})
            .backgroundColor(Colors.systemControls && Colors.styleID==='glass' ? $r('app.color.phone_agent_count_fill') : Colors.control)
            .borderRadius(Tokens.radiusCapsule)
        }
        Blank().layoutWeight(1)
        if (binding.value.folded) SymbolGlyph(Symbols.expand).fontSize(Tokens.interfaceFontVp(12,this.environment.fontScale)).fontColor([Colors.tertiary])
      }.width('100%').padding({left:20,right:20})
    }.width('100%').height(PhoneStyle.formSize(40.33,47.67,this.environment.fontScale)).padding(0).backgroundColor(Color.Transparent)
      .accessibilityText(this.groupTitle(binding.value)+' · '+String(binding.value.totalRowCount)+' · '+tr(binding.value.folded?'agent_expand':'agent_collapse'))
      .responseRegion([{x:0,y:-2,width:'100%',height:44},{x:0,y:0,width:'100%',height:'100%'}])
      .onClick(()=>this.workspace.toggleFold(binding.value.id)).id('agents.group.toggle.'+binding.value.id)
  }

  @Builder groupHeader(binding: BoundAgentGroup) {
    if (binding.value.presentation) Column() {
      if (binding.value.presentation === 'recent') Row({space:6}) {
        Text(this.groupTitle(binding.value)).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(600).fontColor(Colors.secondary)
        this.activityTotal(binding.value.totalRowCount)
      }.width('100%').constraintSize({minHeight:PhoneStyle.formSize(40.3,48,this.environment.fontScale)})
        .padding({left:20,right:20}).accessibilityGroup(true).id('agents.recent.heading')
      else Button({type:ButtonType.Normal}) {
        Row({space:6}) {
          this.activityMark(binding.value)
          Text(this.groupTitle(binding.value)).fontSize(binding.value.presentation==='app-project' ? 15 : 13)
            .maxFontScale(Tokens.interfaceScaleFor(binding.value.presentation==='app-project' ? 15 : 13))
            .fontWeight(600).fontColor(Colors.secondary).flexShrink(1).maxLines(2)
          if (binding.value.subtitle) Text(binding.value.subtitle).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.tertiary).maxLines(1)
          Blank().layoutWeight(1)
          ForEach(this.appCountKeys(binding.value),(key:string)=>{
            Row({space:4}) {
              Circle().width(6).height(6).fill(this.appCountColor(key))
              Text(String(Wire.number(binding.value.stateCounts,key))).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(600).fontColor(Colors.secondary)
            }.accessibilityGroup(true).accessibilityText(this.appCountLabel(binding.value,key)).id('agents.app.count.'+key)
          },(key:string)=>key)
          this.activityTotal(binding.value.totalRowCount)
          SymbolGlyph(Symbols.down).fontSize('12vp').fontColor([Colors.secondary])
            .rotate({angle:binding.value.folded ? -90 : 0}).animation(Motion.state(this.environment.reduceMotion))
        }.width('100%')
      }.width('100%').constraintSize({minHeight:binding.value.presentation==='app-project' ?
        PhoneStyle.formSize(56.3,64,this.environment.fontScale) : PhoneStyle.formSize(64,78,this.environment.fontScale)})
       .padding({left:20,right:20,top:binding.value.presentation==='app-project' ? 0 : 18})
       .backgroundColor(binding.value.presentation==='app-project' ? Colors.surface : Color.Transparent)
       .borderRadius({topLeft:binding.value.firstProject ? 26:0,topRight:binding.value.firstProject ? 26:0,
         bottomLeft:binding.value.lastProject && binding.value.folded ? 26:0,bottomRight:binding.value.lastProject && binding.value.folded ? 26:0})
       .accessibilityText([this.groupTitle(binding.value),binding.value.subtitle,tr(binding.value.folded ? 'agent_expand' : 'agent_collapse'),
         ...this.appCountKeys(binding.value).map((key:string)=>this.appCountLabel(binding.value,key)),String(binding.value.totalRowCount)]
         .filter((part:string)=>!!part).join(' · '))
       .onClick(()=>this.workspace.toggleFold(binding.value.id)).id('agents.group.toggle.'+binding.value.id)
    }.width('100%').padding({left:4,right:4})
    else if (this.compactRoot && !this.rail && binding.value.id==='state/needs') Column() {
      this.needsYouHeader(binding)
    }.width('100%').padding({left:this.floatingStatusCards?16:4,right:this.floatingStatusCards?16:4,top:20})
    else Column({ space: 4 }) {
      if (binding.value.parent === 'quiet') {
        Text(tr('agent_group_quiet_spaces')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium).fontColor(Colors.ink).width('100%')
      }
      Button({ type: ButtonType.Normal }) {
        Row({ space: 6 }) {
          Text(this.groupTitle(binding.value))
            .fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(FontWeight.Medium).fontColor(Colors.ink).flexShrink(1).maxLines(this.narrowRow() ? 4 : 2)
          Text(String(binding.value.totalRowCount)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
          Blank().layoutWeight(1)
          SymbolGlyph(Symbols.down).fontSize('12vp').fontColor([Colors.secondary])
            .rotate({ angle: binding.value.folded ? -90 : 0 }).animation(Motion.state(this.environment.reduceMotion))
        }.width('100%').constraintSize({ minHeight: 44 })
      }.width('100%').padding({ left: 4, right: 5 }).backgroundColor(Color.Transparent)
        .accessibilityText(this.groupTitle(binding.value) + ' · ' +
          String(binding.value.totalRowCount) + ' · ' + tr(binding.value.folded ? 'agent_expand' : 'agent_collapse'))
        .onClick(() => this.workspace.toggleFold(binding.value.id)).id('agents.group.toggle.' + binding.value.id)
    }.width('100%').padding({ top: 6 }).alignItems(HorizontalAlign.Start)
  }
  @Builder activityTotal(value:number) {
    Text(String(value)).fontSize(Tokens.typeCaption).maxFontScale(Tokens.interfaceScaleFor(Tokens.typeCaption)).fontWeight(600).fontColor(Colors.secondary)
      .padding({left:6,right:6,top:1,bottom:1}).backgroundColor(Colors.control).borderRadius(Tokens.radiusBadge)
  }

  private rowStatus(presentation: AgentRowPresentation): string {
    return presentation.status ? tr(presentation.status) : '';
  }
  private needsStatusLabel(groupId: string): boolean {
    return groupId !== 'pinned' && (!groupId.startsWith('state/') || groupId === 'state/muted');
  }

  private rowStatusColor(presentation: AgentRowPresentation): ResourceColor {
    const state = presentation.status;
    if (state === 'needs_you') return Colors.needs;
    if (state === 'working') return Colors.working;
    if (state === 'unread') return Colors.unread;
    return Colors.secondary;
  }

  private rowTime(presentation: AgentRowPresentation): string {
    if (!Number.isFinite(presentation.updatedAt)) return '';
    const elapsed = Math.floor((Date.now() - presentation.updatedAt) / 1000);
    if (elapsed < 0) return '';
    if (elapsed < 60) return tr('agent_time_just_now');
    if (elapsed < 3600) return String(Math.floor(elapsed / 60)) + tr('agent_time_minute_suffix');
    if (elapsed < 86400) return String(Math.floor(elapsed / 3600)) + tr('agent_time_hour_suffix');
    return String(Math.floor(elapsed / 86400)) + tr('agent_time_day_suffix');
  }

  @Builder filterSheet() {
    Column() {
      if (this.model.profiles.length > 1 || this.workspace.currentHubOnly) {
        Row({ space: Tokens.page }) {
          Text(tr('phone_agents_current_hub')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontColor(Colors.ink).layoutWeight(1)
          Toggle({ type: ToggleType.Switch, isOn: this.workspace.currentHubOnly })
            .accessibilityText(tr('phone_agents_current_hub')).id('agents.current_hub')
            .onChange((value: boolean) => this.workspace.setCurrentHubOnly(value))
        }.width('100%').constraintSize({ minHeight: Tokens.header }).alignItems(VerticalAlign.Center)
        Divider().strokeWidth(Tokens.hairline).color(Colors.divider)
      }
      Row({ space: Tokens.page }) {
        Text(tr('agent_filter_machine')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontColor(Colors.ink).layoutWeight(1)
        Select(this.machineSelectOptions()).selected(this.machineIndex()).value(this.machineLabel())
          .attributeModifier(SelectStyles.inline).constraintSize({ minHeight: Tokens.touch, maxWidth: '68%' })
          .onSelect((index: number) => this.selectMachine(index)).id('agents.machine_filter')
      }.width('100%').constraintSize({ minHeight: Tokens.header }).alignItems(VerticalAlign.Center)
      Divider().strokeWidth(Tokens.hairline).color(Colors.divider)
      if (this.workspace.historySupported()) {
        Row({ space: Tokens.page }) {
          Text(tr('agent_history')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontColor(Colors.ink).layoutWeight(1)
          Toggle({ type: ToggleType.Switch, isOn: this.workspace.history })
            .onChange((value: boolean) => this.workspace.setHistory(value))
            .accessibilityText(tr('agent_history')).id('agents.history')
        }.width('100%').alignItems(VerticalAlign.Center).constraintSize({ minHeight: Tokens.header })
        Divider().strokeWidth(Tokens.hairline).color(Colors.divider)
      }
      Row({ space: Tokens.page }) {
        Text(tr('grouping')).fontSize(Tokens.body).maxFontScale(Tokens.interfaceScaleFor(Tokens.body)).fontColor(Colors.ink).layoutWeight(1)
        Select(this.availableGroupings.map((key:string) => ({ value: tr(agentGroupingLabels[key]) } as SelectOption)))
          .selected(this.groupingIndex()).value(tr(agentGroupingLabels[this.workspace.groupingMode]))
          .attributeModifier(SelectStyles.inline).constraintSize({ minHeight: Tokens.touch, maxWidth: '68%' })
          .onSelect((index: number) => this.selectGrouping(index)).id('agents.grouping')
      }.width('100%').constraintSize({ minHeight: Tokens.header }).alignItems(VerticalAlign.Center)
    }.alignItems(HorizontalAlign.Start).padding({ left: Tokens.page, right: Tokens.page, top: Tokens.gap, bottom: Tokens.page + this.filterInset }).width('100%')
      .id(this.filterNodeId).onAreaChange(() => this.measureFilters())
  }

  private isRowSelected(binding: BoundAgentRow): boolean {
    return this.selectionVisible && this.workspace.selectedKey === binding.presentation.selectionKey;
  }
  private cardTitle(card:AgentDecisionCard,binding:BoundAgentRow):string {
    if (card.request.kind==='plan') return tr('phone_agent_plan_approval');
    if (card.request.kind==='permission') return tr('phone_agent_needs_approval');
    if (card.request.kind==='question') return tr('phone_agent_asks').replace('{agent}',card.questionAgent(binding.bound.value) || tr('agent'));
    return tr('needs_you');
  }

  private decisionPreview(card:AgentDecisionCard):string {
    if (card.request.kind==='permission') {
      const tool=interactionToolName(card.request);
      if (tool && card.preview.startsWith(tool+': ')) {
        return tr('phone_agent_permission_preview').replace('{tool}',() => tool).replace('{target}',() => card.preview.slice(tool.length+2));
      }
    }
    return card.preview;
  }

  @Computed get decisionButtonHeight():number { return PhoneStyle.formSize(28,35.33,this.environment.fontScale); }

  private choiceLabel(card:AgentDecisionCard, choice:string):string {
    if (card.request.kind==='permission') return tr(choice==='deny' ? 'phone_agent_deny_choice' : card.namedPermissionAgent ? 'approve' : 'allow');
    return recommendedOptionTitle(choice) ?? choice;
  }
  private choiceRecommended(card:AgentDecisionCard, choice:string):boolean {
    return card.request.kind==='question' && recommendedOptionTitle(choice)!==undefined;
  }
  private choiceHeight(card:AgentDecisionCard,choice:string):number {
    return this.choiceRecommended(card,choice) ? PhoneStyle.formSize(29.33,36.67,this.environment.fontScale) : this.decisionButtonHeight;
  }
  private choiceMaterial(prominent:boolean):LinearGradientOptions | undefined {
    if (!Colors.systemControls || Colors.styleID!=='glass') return undefined;
    const fill=prominent ? $r('app.color.phone_agent_allow_fill') : $r('app.color.phone_agent_quick_fill');
    const light=prominent ? $r('app.color.phone_agent_allow_highlight') : $r('app.color.phone_agent_quick_highlight');
    const edge=2/this.decisionButtonHeight;
    return {direction:GradientDirection.Bottom,colors:[[light,0],[fill,edge],[fill,1-edge],[light,1]]};
  }

  @Builder decisionChoices(card:AgentDecisionCard,binding:BoundAgentRow) {
    Scroll() {
      Row({space:8}) {
        ForEach(card.quickOptions(),(choice:string) => {
          Button({type:ButtonType.Capsule}) {
            Row({space:6}) {
              Text(this.choiceLabel(card,choice)).fontSize(String(PhoneStyle.formSize(15,20.33,this.environment.fontScale))+'vp')
                .maxLines(1).fontWeight(FontWeight.Medium).fontColor(card.request.kind==='permission' && choice==='allow' ? Colors.onAction :
                  Colors.systemControls && Colors.styleID==='glass' ? $r('app.color.phone_agent_quick_ink') : Colors.link)
              if (this.choiceRecommended(card,choice)) {
                Text(tr('phone_agent_recommended')).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12))
                  .lineHeight(PhoneStyle.settingsExampleLineHeight(14.33,21.67,this.environment.fontScale)).maxLines(1)
                  .fontWeight(FontWeight.Medium).fontColor($r('app.color.phone_agent_recommended_ink'))
                  .padding({left:8,right:8,top:2.5,bottom:2.5}).backgroundColor($r('app.color.phone_agent_recommended_fill')).borderRadius(Tokens.capsule)
              }
            }
          }.height(this.choiceHeight(card,choice)).padding({left:card.request.kind==='permission'?10:10.5,right:card.request.kind==='permission'?10:10.5}).flexShrink(0)
            .backgroundColor(card.request.kind==='permission' && choice==='allow' ? Colors.action :
              Colors.systemControls && Colors.styleID==='glass' ? $r('app.color.phone_agent_quick_fill') : Colors.surface)
            .linearGradient(this.choiceMaterial(card.request.kind==='permission' && choice==='allow'))
            .outline({width:Colors.systemControls && Colors.styleID==='glass' && !(card.request.kind==='permission' && choice==='allow') ? 0.5 : 0,
              color:$r('app.color.phone_agent_quick_edge'),radius:Tokens.capsule})
            .responseRegion({x:0,y:(this.choiceHeight(card,choice)-44)/2,width:'100%',height:44})
            .accessibilityText(this.choiceRecommended(card,choice) ?
              tr('phone_agent_recommended_accessible').replace('{value}',() => this.choiceLabel(card,choice)) : this.choiceLabel(card,choice))
            .scale({x:this.pressedChoiceKey === card.request.id+':'+choice ? MotionTokens.pressScale : 1,
              y:this.pressedChoiceKey === card.request.id+':'+choice ? MotionTokens.pressScale : 1})
            .animation(Motion.release(this.environment.reduceMotion))
            .onTouch((event:TouchEvent) => {
              const tag = card.request.id+':'+choice;
              if (event.type === TouchType.Down) this.pressedChoiceKey = tag;
              else if (event.type === TouchType.Up || event.type === TouchType.Cancel) {
                if (this.pressedChoiceKey === tag) this.pressedChoiceKey = '';
              }
            })
            .enabled(this.active && this.decisions.enabled(this.model,binding.bound.value,card))
            .onClick(() => {
              Haptics.tap();
              this.decisions.answer(this.model,binding.bound.value,card,choice,util.generateRandomUUID(),this.workspace.actionTicket(binding.bound.value))
                .then(() => { if (!this.decisions.error) Haptics.success(); else Haptics.error(); })
                .catch(() => { Haptics.error(); });
            })
            .id('agents.answer.'+card.request.id+'.'+choice)
        },(choice:string)=>choice)
      }
    }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).align(Alignment.Start).width('100%')
  }

  @Builder needsYouCards(binding:BoundAgentRow) {
    ListItem() {
      Column() {
        Column() {
          ForEach(this.decisions.forSession(this.model,binding.bound.value),(card:AgentDecisionCard,index:number) => {
            Column({space:12}) {
              Button({type:ButtonType.Normal}) {
                Row({space:8}) {
                  AgentNeedsYouMark({kind:card.request.kind,fontScale:this.environment.fontScale,surface:Colors.surface})
                  Column({space:2}) {
                    Text(this.cardTitle(card,binding)).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(600)
                      .lineHeight(PhoneStyle.settingsExampleLineHeight(20.33,27.67,this.environment.fontScale)).maxLines(2).fontColor(Colors.ink)
                    if (!index) Text([this.rowTitle(binding),this.model.machineName(card.machine)].filter((v:string)=>!!v).join(' · '))
                      .fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).lineHeight(PhoneStyle.settingsExampleLineHeight(14.33,21.67,this.environment.fontScale))
                      .fontColor(Colors.secondary).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
                  }.layoutWeight(1).alignItems(HorizontalAlign.Start)
                  SymbolGlyph(Symbols.expand).fontSize(Tokens.interfaceFontVp(12,this.environment.fontScale)).fontWeight(600).fontColor([Colors.tertiary])
                }.width('100%')
              }.width('100%').constraintSize({minHeight:0}).padding(0).backgroundColor(Color.Transparent)
                .responseRegion([{x:0,y:-12,width:'100%',height:44},{x:0,y:0,width:'100%',height:'100%'}])
                .onClick(() => this.openRow(binding.bound.value)).id('agents.request.'+card.request.id)
              if (card.preview) Text(this.decisionPreview(card)).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale).fontColor(Colors.ink)
                .lineSpacing(LengthMetrics.vp(PhoneStyle.sidebarMetric(17*(1.45-1.19),this.environment.fontScale)),{onlyBetweenLines:true})
                .lineHeight(PhoneStyle.settingsExampleLineHeight(20.33,27.67,this.environment.fontScale)).maxLines(3).textAlign(TextAlign.Start)
                .width('100%').onClick(() => this.openRow(binding.bound.value)).id('agents.preview.'+card.request.id)
              if (card.quickOptions().length) this.decisionChoices(card,binding)
            }.width('100%').alignItems(HorizontalAlign.Start).padding({left:20,right:20,top:index===0?19:20})
          },(card:AgentDecisionCard) => card.key+card.request.raw)
        }.width('100%').padding({bottom:19})
        if (!this.detachedDecisionRows && !this.endsSurface(binding,'state/needs')) {
          Divider().strokeWidth(1).color(Colors.systemControls && Colors.styleID==='glass' ?
            $r('app.color.phone_agent_session_separator') : Colors.divider).margin({left:20,right:20})
        }
      }.width('100%').alignItems(HorizontalAlign.Start).backgroundColor(Colors.surface)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: this.detachedDecisionRows || this.startsSurface(binding,'state/needs') ? 26 : 0 })
        .shadow(this.detachedDecisionRows || this.startsSurface(binding,'state/needs') ? PhoneStyle.cardElevation(this.environment.dark) : { radius: 0, color: Color.Transparent })
        .borderRadius({topLeft:this.detachedDecisionRows || this.startsSurface(binding,'state/needs') ? 26:0,
          topRight:this.detachedDecisionRows || this.startsSurface(binding,'state/needs') ? 26:0,
          bottomLeft:this.detachedDecisionRows || this.endsSurface(binding,'state/needs') ? 26:0,
          bottomRight:this.detachedDecisionRows || this.endsSurface(binding,'state/needs') ? 26:0})
    }.margin({left:this.floatingStatusCards?16:4,right:this.floatingStatusCards?16:4,
      top:this.detachedDecisionRows?4:0,bottom:this.detachedDecisionRows?4:0})
      .onVisibleAreaChange([0,1],(visible:boolean,ratio:number) => this.onVisible(binding.bound.value,visible ? ratio : 0))
      .onDisAppear(() => this.onVisible(binding.bound.value,0)).bindContextMenu(this.rowMenu(binding),ResponseType.LongPress)
      .id('agent.'+binding.bound.value.group+'.'+binding.bound.value.manager+'.'+binding.bound.value.machine+'.'+binding.bound.value.id)
  }

  @Computed get orderedNeedsRows():BoundAgentRow[] {
    const group=this.groupBindings.find((item:BoundAgentGroup):boolean=>item.value.id==='state/needs');
    if (!group) return [];
    return group.rows.slice().sort((a:BoundAgentRow,b:BoundAgentRow) =>
      this.decisions.waitingSince(this.model,a.bound.value)-this.decisions.waitingSince(this.model,b.bound.value));
  }
  @Computed get detachedDecisionRows():boolean { return this.floatingStatusCards || this.displayedAgentStyle==='beautiful'; }
  private displayRows(group:BoundAgentGroup):BoundAgentRow[] {
    return this.compactRoot && !this.rail && group.value.id==='state/needs' ? this.orderedNeedsRows : group.rows;
  }
  private startsSurface(row:BoundAgentRow,id:string):boolean {
    return this.compactRoot && !this.rail && id==='state/needs' ? this.orderedNeedsRows[0]===row : row.first;
  }
  private endsSurface(row:BoundAgentRow,id:string):boolean {
    return this.compactRoot && !this.rail && id==='state/needs' ? this.orderedNeedsRows[this.orderedNeedsRows.length-1]===row : row.last;
  }
  @Builder groupRow(binding:BoundAgentRow,groupId:string,endOfSurface:boolean=true) {
    if (this.compactRoot && !this.rail && groupId==='state/needs' && this.decisions.forSession(this.model,binding.bound.value).length)
      this.needsYouCards(binding)
    else if (this.compactRoot && !this.rail && binding.presentation.activity)
      this.activityRow(binding,groupId,endOfSurface)
    else this.agentRow(binding,groupId)
  }
  @Computed get floatingStatusCards():boolean {
    return this.compactRoot && !this.rail && this.displayedAgentStyle==='cards';
  }
  private activityPlace(binding:BoundAgentRow,groupId:string):string {
    const value=binding.presentation, agent=value.activity;
    if (!agent) return '';
    if ((this.workspace.groupingMode==='space' && agent.spaceId && groupId.endsWith('/'+agent.spaceId)) ||
      (groupId.includes('/project/') && !groupId.endsWith('/other#'))) return agent.tab;
    const place=[value.spaceName,agent.tab !== value.spaceName ? agent.tab : ''].filter((part:string)=>!!part).join(' › ');
    return [place,this.mixedMachines ? value.metadata[1] : ''].filter((part:string)=>!!part).join(' · ');
  }
  @Builder activityRow(binding:BoundAgentRow,groupId:string,endOfSurface:boolean,depth:number=0) {
    ListItem() {
      Column() {
        AgentDirectoryRowContent({agent:binding.presentation.activity!,display:this.displayedAgentStyle,muted:this.workspace.isMuted(binding.bound.value),
          hideState:groupId.startsWith('state/') && groupId!=='state/muted',age:this.rowTime(binding.presentation),place:this.activityPlace(binding,groupId),
          showsSource:!!binding.presentation.activitySource,sourceTitle:binding.presentation.activitySource ? tr(binding.presentation.activitySource) : '',
          narrow:this.listWidth>0 && this.listWidth<296,leftInset:(groupId.includes('/project/') ? 36 : 20)+16*depth,
          controlScale:Math.max(1,Math.min(this.environment.fontScale,67/51)),fontScale:this.environment.fontScale,
          tint:this.rowStatusColor(binding.presentation),activityList:true,useHerdrTabName:this.model.directoryOwnerFor(binding.bound.value)?.useHerdrTabName ?? this.model.useHerdrTabName,
          renaming:!!(this.model.namingAgentKeys && this.model.namingAgentKeys.length > 0 && this.model.isAutoRenamingRow(binding.bound.value))})
      }.width('100%').backgroundColor(this.isRowSelected(binding) ? Colors.selection : this.displayedAgentStyle==='cards'?Colors.grouped:Colors.surface)
       .borderRadius({topLeft:this.startsSurface(binding,groupId) && !groupId.includes('/project/') ? 26:0,topRight:this.startsSurface(binding,groupId) && !groupId.includes('/project/') ? 26:0,
         bottomLeft:this.endsSurface(binding,groupId) && endOfSurface ? 26:0,bottomRight:this.endsSurface(binding,groupId) && endOfSurface ? 26:0})
    }.margin({left:this.floatingStatusCards?0:4,right:this.floatingStatusCards?0:4}).accessibilityGroup(true)
      .accessibilityText([binding.presentation.provider,this.rowStatus(binding.presentation),
        this.rowTitle(binding),
        this.rowTime(binding.presentation),binding.presentation.recentContent,binding.presentation.activitySource ? tr(binding.presentation.activitySource) : '',
        this.activityPlace(binding,groupId),binding.presentation.activity?.model || '',binding.presentation.activity?.todoTitle || ''].filter((part:string)=>!!part).join(' · '))
      .selectable(true).selected(this.isRowSelected(binding)).onClick(()=>this.openRow(binding.bound.value))
      .onVisibleAreaChange([0,1],(visible:boolean,ratio:number)=>this.onVisible(binding.bound.value,visible ? ratio : 0))
      .onDisAppear(()=>this.onVisible(binding.bound.value,0)).bindContextMenu(this.rowMenu(binding),ResponseType.LongPress)
      .id('agent.'+binding.bound.value.group+'.'+binding.bound.value.manager+'.'+binding.bound.value.machine+'.'+binding.bound.value.id)
  }

  @Builder taskRow(row:ResourceRow,depth:number) {
    if (this.taskRowsByKey.has(row.key())) {
      if(this.compactRoot && !this.rail && this.taskRowsByKey.get(row.key())!.presentation.activity)
        this.activityRow(this.taskRowsByKey.get(row.key())!,this.workspace.groupingMode,true,depth)
      else this.agentRow(this.taskRowsByKey.get(row.key())!,this.workspace.groupingMode,depth)
    }
  }
  @Builder agentRow(binding: BoundAgentRow, groupId: string, depth:number=0) {
    ListItem() {
      Column() {
        Row({ space: 12 }) {
          Stack({ alignContent: Alignment.BottomEnd }) {
            Stack() {
              if (binding.presentation.brand === 'codex') {
                Image($r('app.media.project_agent_openai')).width(18).height(18).objectFit(ImageFit.Contain).fillColor(Colors.ink)
              } else if (binding.presentation.brand === 'claude') {
                Image($r('app.media.project_agent_claude')).width(18).height(18).objectFit(ImageFit.Contain)
              } else if (binding.presentation.provider) {
                Text(binding.presentation.provider.slice(0, 1).toUpperCase()).fontSize('16vp').fontWeight(FontWeight.Medium).fontColor(Colors.ink)
              } else SymbolGlyph(Symbols.agent).fontSize('17vp').fontColor([this.rowStatusColor(binding.presentation)])
            }.width(36).height(36).backgroundColor(Colors.subtle).borderRadius(Tokens.radiusCapsule)
            if (binding.presentation.status === 'needs_you') {
              NeedsYouPulseDot({ dotSize: 10, tint: Colors.needs, surface: Colors.surface, bordered: true }).offset({ x: 3, y: 3 })
            } else if (binding.presentation.status === 'working') {
              WorkingArcSpinner({ diameter: 16, tint: Colors.working, surface: Colors.surface, showCoreDot: true }).offset({ x: 4, y: 4 })
            } else if (binding.presentation.status === 'unread' || binding.presentation.status === 'done') {
              Circle().width(11).height(11).fill(Colors.unread)
                .stroke(Colors.surface).strokeWidth(2)
            }
          }.width(36).height(36)
          Column({ space: 3 }) {
            Row({ space: 6 }) {
              Text(this.rowTitle(binding)).fontSize(17).maxFontScale(Tokens.interfaceFontMaxScale)
                .fontWeight(binding.presentation.status === 'unread' ? FontWeight.Bold : 600)
                .fontColor(Colors.ink).layoutWeight(1).maxLines(this.narrowRow() ? 4 : 1)
                .textOverflow({ overflow: TextOverflow.Ellipsis })
              if (!this.narrowRow() && this.rowTime(binding.presentation)) {
                Text(this.rowTime(binding.presentation)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
              }
            }.width('100%').alignItems(VerticalAlign.Center)
            if (binding.presentation.recentContent) {
              Row({ space: 6 }) {
                Text(binding.presentation.recentContent).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary).layoutWeight(1)
                  .maxLines(this.narrowRow() ? 4 : this.look.density === 'compact' ? 1 : 2).textOverflow({ overflow: TextOverflow.Ellipsis })
                if (!this.narrowRow() && binding.presentation.status === 'needs_you') {
                  Text(tr('needs_you')).fontSize(11).maxFontScale(Tokens.interfaceScaleFor(11)).fontWeight(FontWeight.Medium).fontColor(Colors.needs)
                    .padding({ left: 6, right: 6, top: 2, bottom: 2 }).backgroundColor(Colors.selection).borderRadius(Tokens.radiusBadge).maxLines(1)
                }
              }.width('100%').alignItems(VerticalAlign.Top)
            } else if (!this.narrowRow() && binding.presentation.status === 'needs_you') {
              Text(tr('needs_you')).fontSize(11).maxFontScale(Tokens.interfaceScaleFor(11)).fontWeight(FontWeight.Medium).fontColor(Colors.needs)
                .padding({ left: 6, right: 6, top: 2, bottom: 2 }).backgroundColor(Colors.selection).borderRadius(Tokens.radiusBadge).maxLines(1)
            }
            this.rowContext(this.rowPlace(binding.presentation), this.needsStatusLabel(groupId) ? this.rowStatus(binding.presentation) : '',
              this.rowStatusColor(binding.presentation))
            if (this.narrowRow() && this.rowTime(binding.presentation)) {
              Text(this.rowTime(binding.presentation)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
            }
          }.alignItems(HorizontalAlign.Start).layoutWeight(1)
          SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.tertiary])
        }.width('100%').alignItems(VerticalAlign.Center).justifyContent(FlexAlign.Start)
          .padding({ left: 14, right: 12,
            top: this.look.density === 'compact' ? 9 : 12, bottom: this.look.density === 'compact' ? 9 : 12 })
        if (!this.endsSurface(binding,groupId)) Divider().strokeWidth(0.5).color(Colors.divider).margin({ left: 62, right: 12 })
      }.width('100%')
    }.accessibilityGroup(true).accessibilityText([this.rowTitle(binding),
      binding.presentation.recentContent, ...binding.presentation.metadata.slice(0, 2), this.rowStatus(binding.presentation)].filter((s: string) => !!s).join(' · '))
      .selectable(true).selected(this.isRowSelected(binding)).padding({left:18*Math.min(3,Math.max(0,depth))})
      .backgroundColor(this.isRowSelected(binding) ? Colors.selection : Colors.surface)
      .border({
        width: {
          top: this.startsSurface(binding, groupId) ? PhoneStyle.surfaceEdgeWidth(0.5) : 0,
          bottom: this.endsSurface(binding, groupId) ? PhoneStyle.surfaceEdgeWidth(0.5) : 0,
          left: PhoneStyle.surfaceEdgeWidth(0.5),
          right: PhoneStyle.surfaceEdgeWidth(0.5)
        },
        color: PhoneStyle.cardBorder(this.environment.dark),
        radius: {
          topLeft: this.startsSurface(binding, groupId) ? PhoneStyle.cardRadius : 0,
          topRight: this.startsSurface(binding, groupId) ? PhoneStyle.cardRadius : 0,
          bottomLeft: this.endsSurface(binding, groupId) ? PhoneStyle.cardRadius : 0,
          bottomRight: this.endsSurface(binding, groupId) ? PhoneStyle.cardRadius : 0
        }
      })
      .borderRadius({ topLeft: this.startsSurface(binding,groupId) ? PhoneStyle.cardRadius : 0, topRight: this.startsSurface(binding,groupId) ? PhoneStyle.cardRadius : 0,
        bottomLeft: this.endsSurface(binding,groupId) ? PhoneStyle.cardRadius : 0, bottomRight: this.endsSurface(binding,groupId) ? PhoneStyle.cardRadius : 0 })
      .shadow(this.endsSurface(binding, groupId) ? PhoneStyle.cardElevation(this.environment.dark) : { radius: 0, color: Color.Transparent })
      .onClick(() => this.openRow(binding.bound.value))
      .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => this.onVisible(binding.bound.value, visible ? ratio : 0))
      .onDisAppear(() => this.onVisible(binding.bound.value, 0))
      .bindContextMenu(this.rowMenu(binding), ResponseType.LongPress)
      .id('agent.' + binding.bound.value.group + '.' + binding.bound.value.manager + '.' + binding.bound.value.machine + '.' + binding.bound.value.id)
  }

  @Builder rowContext(place: string, status: string, color: ResourceColor) {
    if ((place || status) && this.narrowRow()) {
      Column({ space: 3 }) {
        if (status) Text(status).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(FontWeight.Medium).fontColor(color)
        if (place) Text(place).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(3).textOverflow({overflow:TextOverflow.Ellipsis})
      }.width('100%').alignItems(HorizontalAlign.Start)
    } else if (place || status) {
      Row({ space: 5 }) {
        if (status) Text(status).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontWeight(FontWeight.Medium).fontColor(color).maxLines(1)
        if (place) Text(place).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).layoutWeight(1).maxLines(1)
          .textOverflow({ overflow: TextOverflow.Ellipsis })
      }.width('100%')
    }
  }

  private rowTitle(binding: BoundAgentRow): string {
    if (this.model.namingAgentKeys && this.model.namingAgentKeys.length > 0 && this.model.isAutoRenamingRow(binding.bound.value)) {
      return tr('agent_renaming');
    }
    const preferTab = this.model.directoryOwnerFor(binding.bound.value)?.useHerdrTabName ?? this.model.useHerdrTabName;
    if (preferTab && binding.presentation.herdrTabName) {
      return binding.presentation.herdrTabName;
    }
    return binding.bound.value.title || tr('agent_untitled');
  }

  private rowPlace(presentation: AgentRowPresentation): string {
    const parts: string[] = [];
    if (presentation.provider) parts.push(presentation.brand === 'claude' ? 'Claude' : presentation.brand === 'codex' ? 'Codex' : presentation.provider);
    if (this.mixedMachines && this.workspace.groupingMode !== 'machine' && presentation.metadata[1]) parts.push(presentation.metadata[1]);
    const folder = presentation.spaceName || (presentation.worktree || presentation.cwd || presentation.repositoryRoot).split(/[\\/]/).filter((part: string) => !!part).pop();
    if (folder && !parts.includes(folder)) parts.push(folder);
    return parts.join(' · ');
  }

  private searchVisible(): boolean { return !this.compactRoot || this.searchOpen || !!this.workspace.search; }
  private resetSearchReveal(): void {
    clearTimeout(this.focusTimer); this.searchOpen = false; this.setSearchEditing(false); this.focusSearch = false; this.pullY = -1; this.revealedDuringPull = false;
  }
  private trackPull(event: TouchEvent): void {
    if (event.type === TouchType.Down) this.revealedDuringPull = false;
    if (this.externalSearchControls || !this.compactRoot || !this.active || this.searchEditing || this.workspace.search) return;
    if (event.type === TouchType.Down) {
      let top = false;
      try { top = this.agentScroller.currentOffset().yOffset <= 1; } catch (_) { }
      if ((top || this.searchOpen) && event.touches.length === 1) {
        this.pullX = event.touches[0].windowX; this.pullY = event.touches[0].windowY;
      } else this.pullY = -1;
    } else if (event.type === TouchType.Move && event.touches.length === 1 && this.pullY >= 0) {
      const dy = event.touches[0].windowY - this.pullY, dx = Math.abs(event.touches[0].windowX - this.pullX);
      if (Math.abs(dy) >= 48 && Math.abs(dy) > 1.5 * dx) {
        if (dy > 0 && !this.searchOpen) { this.revealedDuringPull = true; this.searchOpen = true; }
        else if (dy < 0 && this.searchOpen) this.searchOpen = false;
        this.pullY = -1;
      }
    } else this.pullY = -1;
  }
  private refreshAgents(): void {
    if (this.revealedDuringPull) { this.revealedDuringPull = false; this.refreshing = false; return; }
    this.workspace.clearFilterKept?.();
    this.reloadDecisions(); this.workspace.setReadingDetail(false);
    const refreshes:Promise<void>[] = [this.workspace.refresh(this.model), this.refreshPersonalAgents()];
    if (this.workspace.groupingMode === 'repo') refreshes.push(this.projectCatalog.refresh(this.model));
    this.refreshGroupingSupport();
    Promise.all(refreshes).catch(() => {}).finally(() => {
      if (this.active && this.workspace.isBoundTo(this.model)) this.workspace.setReadingDetail(this.readingDetail);
      this.refreshing = false;
    });
  }

  private async refreshPersonalAgents(): Promise<void> {
    if (this.rail || this.externalSearchControls) return;
    const owner = this.model;
    const scope = owner.scope();
    const generation = owner.connectionGeneration();
    const serial = ++this.personalAgentSerial;
    if (!owner.profile.id || (!owner.connected && !owner.demo)) {
      this.personalAgentCount = 0;
      return;
    }
    try {
      const repo = owner.repo();
      const rawSources = await repo.call('/api/v1/agents');
      if (serial !== this.personalAgentSerial || !owner.owns(generation, scope)) return;
      const sources = parsePersonalAgentSources(rawSources);
      const hasConfigured = sources.some((s) =>
        s.enabled && !['missing', 'disabled', 'no_accounts'].includes(s.setup) && s.accounts.length > 0
      );
      if (!hasConfigured) {
        this.personalAgentCount = 0;
        return;
      }
      const rawSessions = await repo.call('/api/v1/agents/sessions');
      if (serial !== this.personalAgentSerial || !owner.owns(generation, scope)) return;
      const summary = computePersonalAgentEntry(sources, parsePersonalAgentSessionsPage(rawSessions));
      this.personalAgentCount = summary.available ? summary.count : 0;
    } catch (_) {
      if (serial === this.personalAgentSerial) {
        this.personalAgentCount = 0;
      }
    }
  }

  @Builder personalAgentsHeadingRow() {
    ListItem() {
      Button({ type: ButtonType.Normal }) {
        Row({ space: Tokens.spaceM }) {
          Stack({ alignContent: Alignment.Center }) {
            SymbolGlyph(Symbols.agents)
              .fontSize('18vp')
              .fontColor([Colors.ai])
          }
          .width(32)
          .height(32)
          .borderRadius(Tokens.radiusControl)
          .backgroundColor(Colors.aiWash)
          .flexShrink(0)

          Text(tr('personal_agents'))
            .fontSize(Tokens.typeSecondary)
            .maxFontScale(Tokens.interfaceScaleFor(Tokens.typeSecondary))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.ink)
            .maxLines(1)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
            .layoutWeight(1)

          PersonaStatusPill({ tone: 'neutral', label: String(this.personalAgentCount) })

          SymbolGlyph(Symbols.expand)
            .fontSize('12vp')
            .fontColor([Colors.tertiary])
        }
        .width('100%')
        .constraintSize({ minHeight: Tokens.controlBar })
        .padding({ left: Tokens.spaceM, right: Tokens.spaceM, top: Tokens.spaceS, bottom: Tokens.spaceS })
        .alignItems(VerticalAlign.Center)
      }
      .width('100%')
      .padding(0)
      .backgroundColor(Colors.surface)
      .borderRadius(Tokens.radiusCard)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: Tokens.radiusCard })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .accessibilityText(tr('personal_agents') + ' · ' + String(this.personalAgentCount))
      .onClick(() => {
        Haptics.tap();
        this.personalAgentsSheetOpen = true;
      })
      .id('agents.personal')
    }
    .margin({ left: this.floatingStatusCards ? 16 : 4, right: this.floatingStatusCards ? 16 : 4, top: Tokens.spaceS, bottom: Tokens.spaceXs })
  }

  @Builder personalAgentsSheetBuilder() {
    PersonalAgentsSheet({
      model: this.model,
      onClose: () => { this.personalAgentsSheetOpen = false; },
      onChanged: () => { this.refreshPersonalAgents().catch(() => undefined); }
    })
  }
  @Computed get shownQuickFilters(): string[] {
    const counts = this.workspace.filterCounts;
    const known = this.workspace.quickCountsKnown();
    return QUICK_FILTERS.filter((key: string) => !known || key === 'all' || key === this.workspace.quickFilter || Wire.number(counts,key)>0);
  }
  private quickHeight(): number { return PhoneStyle.formSize(44,57.7,this.environment.fontScale); }
  private setSearchEditing(editing:boolean): void {
    if (editing === this.searchEditing) return;
    this.searchEditing = editing; this.onSearchMode(editing);
  }
  private activateSearch(): void {
    this.focusSearch = true; this.searchOpen = true;
    this.focusMountedSearch();
  }
  private focusMountedSearch(attempt:number=0): void {
    clearTimeout(this.focusTimer);
    if (!this.active || !this.searchOpen || !this.focusSearch) return;
    // ArkUI's native menu can still own focus while the new search field enters the page.
    this.focusTimer = setTimeout(() => {
      if (!this.active || !this.searchOpen || !this.focusSearch) return;
      const ui=this.getUIContext(), field=ui.getFrameNodeById('agents.search');
      if (field && field.getMeasuredSize().width > 0) {
        try { ui.getFocusController().requestFocus('agents.search'); } catch (_) { /* The outgoing menu may cover this frame. */ }
      }
      if (this.focusSearch && attempt < 8) this.focusMountedSearch(attempt + 1);
      else this.focusSearch = false;
    },45);
  }
  private headerTopInset(): number {
    if (!this.compactRoot || this.pageOriginY < 0 || !this.environment.isFullScreen) return 6;
    const ui=this.getUIContext();
    const height=ui.px2vp(this.environment.windowHeightPx), width=ui.px2vp(this.environment.windowWidthPx);
    if (height < 600 || width >= height) return 6;
    const top=Math.max(Tokens.compactStatusInset,ui.px2vp(this.environment.safeTopPx));
    // The phone shell leaves 34 points of room under its 44-point navigation bar.
    // A folded search band is 54 points shorter than its revealed field and filters.
    const firstRow=this.searchEditing ? top : top+44+34+(this.searchVisible() ? 10 : 20);
    return Math.max(0,firstRow-this.pageOriginY);
  }

  @Builder searchControl() {
    Search({value:this.workspace.search,placeholder:tr('agents_search_prompt'),controller:this.searchField})
      .maxFontScale(Tokens.interfaceFontMaxScale).height(44).margin(0)
      .cancelButton({style:this.workspace.search ? CancelButtonStyle.INPUT : CancelButtonStyle.INVISIBLE})
      .textFont({size:Tokens.interfaceFontVp(17,this.environment.fontScale)}).placeholderFont({size:Tokens.interfaceFontVp(17,this.environment.fontScale)}).caretStyle({color:Colors.action})
      .onFocus(() => { if (this.compactRoot) { this.focusSearch = false; this.searchOpen = true; this.setSearchEditing(true); } })
      .onBlur(() => { if (!this.workspace.search) this.setSearchEditing(false); })
      .onChange((value: string) => { if (this.compactRoot) this.searchOpen = true; this.workspace.setSearch(value); })
      .onAppear(() => this.focusMountedSearch()).id('agents.search')
  }
  @Builder displayChoices() {
    Menu(){
      ForEach(['avatar','compact','cards','list'],(style:string)=>{
        MenuItem({content:tr('phone_agent_display_'+style)}).selected(this.look.agentDisplay===style).selectIcon(true)
          .onClick(()=>{this.look.setAgentDisplay(style);}).id('agents.display.'+style)
      },(style:string)=>style)
    }
  }
  @Builder groupingMenu() {
    Menu() {
      ForEach(this.availableGroupings,(key:string,index:number) => {
        MenuItem({content:tr(agentGroupingLabels[key])}).selected(key===this.workspace.groupingMode).selectIcon(true)
          .onClick(() => this.selectGrouping(index)).id('agents.grouping.'+key)
      },(key:string)=>key)
      MenuItem({content:tr('phone_agent_display_title'),builder:()=>{this.displayChoices();}}).id('agents.display')
      MenuItem({content:tr('search_sessions')}).onClick(()=>this.activateSearch()).id('agents.search_reveal')
      MenuItem({content:tr('agent_filters_title')}).onClick(()=>this.filterSheetOpen=true).id('agents.grouping.filters')
    }
  }
  @Builder filterControl() {
    if (this.compactRoot) {
      Button({type:ButtonType.Circle}) {
        PhoneGroupingGlyph({controlHeight:this.quickHeight()})
      }
        .width(this.quickHeight()).height(this.quickHeight()).padding(0).backgroundColor(Color.Transparent)
        .accessibilityText(tr('agent_grouping_current').replace('{group}',tr(agentGroupingLabels[this.workspace.groupingMode])))
        .accessibilityCustomActions([{name:tr('search_sessions'),onAction:()=>this.activateSearch()}])
        .bindMenu(this.groupingMenu).id('agents.grouping.quick')
    } else {
      Button({ type: ButtonType.Circle }) { SymbolGlyph(Symbols.filter).fontSize(Tokens.symbolFont).fontColor([Colors.link]) }.attributeModifier(ButtonStyles.chrome).accessibilityText(tr('agent_filter_button'))
        .onClick(() => this.filterSheetOpen = true).id('agents.filter')
    }
  }
  private revealQuickFilter(key: string): void {
    const index = this.shownQuickFilters.indexOf(key);
    if (index < 0) return;
    try {
      const targetX = Math.max(0, index * 72 - 48);
      this.quickScroller.scrollTo({
        xOffset: targetX,
        yOffset: 0,
        animation: this.environment.reduceMotion ? false : { duration: MotionTokens.stateMs, curve: Curve.EaseOut }
      });
    } catch (_) {}
  }

  private confirmMarkAllSeen(): void {
    const count = this.workspace.unreadShownRows.length;
    if (!count || this.workspace.busy) return;
    Haptics.tap();
    this.getUIContext().showAlertDialog({
      title: tr('agent_mark_all_seen'),
      message: tr('agent_mark_all_seen_confirm').replace('{count}', String(count)),
      primaryButton: { value: tr('cancel'), action: () => {} },
      secondaryButton: {
        value: tr('agent_mark_all_seen'),
        action: () => {
          this.workspace.markAllSeen().then((marked: number) => {
            if (marked > 0) {
              Haptics.success();
              toastCenter.success(tr('agents_marked_seen_toast').replace('{count}', String(marked)));
              nativeLiveView.syncFromModel(this.model).catch(() => {});
            }
          }).catch(() => { Haptics.error(); toastCenter.error(tr('failed')); });
        }
      }
    });
  }

  @Builder markAllSeenRow() {
    ListItem() {
      Button({ type: ButtonType.Normal }) {
        Row({ space: 10 }) {
          SymbolGlyph(Symbols.check)
            .fontSize(Tokens.interfaceFontVp(16, this.environment.fontScale))
            .fontColor([Colors.link])
          Text(tr('agent_mark_all_seen'))
            .fontSize(15)
            .maxFontScale(Tokens.interfaceScaleFor(15))
            .fontWeight(FontWeight.Medium)
            .fontColor(Colors.link)
            .maxLines(1)
            .textOverflow({ overflow: TextOverflow.Ellipsis })
            .layoutWeight(1)
          Text(String(this.workspace.unreadShownRows.length))
            .fontSize(13)
            .maxFontScale(Tokens.interfaceScaleFor(13))
            .fontColor(Colors.secondary)
            .maxLines(1)
        }.width('100%').padding({ left: 16, right: 16 }).constraintSize({ minHeight: 44 })
      }
      .width('100%')
      .padding(0)
      .backgroundColor(Colors.surface)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: this.compactRoot && !this.rail ? Tokens.radiusPanel : Tokens.radiusCard })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .borderRadius(this.compactRoot && !this.rail ? Tokens.radiusPanel : Tokens.radiusCard)
      .enabled(!this.workspace.busy)
      .accessibilityText(tr('agent_mark_all_seen') + ' · ' + String(this.workspace.unreadShownRows.length))
      .onClick(() => this.confirmMarkAllSeen())
      .id('agents.unread.mark_all_seen')
    }.margin({ left: this.floatingStatusCards ? 16 : 4, right: this.floatingStatusCards ? 16 : 4, top: 8, bottom: 4 })
      .transition(Motion.fadeScale(this.environment.reduceMotion, 0.96))
  }

  @Builder filters() {
    if (!this.rail) Column({ space: this.compactRoot && this.searchVisible() ? 20 : 6 }) {
      if (this.searchVisible()) {
        if (this.compactRoot) Row({space: 11}) {
          Column() { this.searchControl() }.layoutWeight(1)
          if (this.searchEditing) {
            Button({type:ButtonType.Circle}) { SymbolGlyph($r('sys.symbol.xmark')).fontSize('16vp').fontColor([Colors.ink]) }
              .width(44).height(44).padding(0).backgroundColor(Colors.control).accessibilityText(tr('cancel'))
              .onClick(() => { Haptics.tap(); this.searchField.stopEditing(); this.workspace.setSearch(''); this.resetSearchReveal(); }).id('agents.search_cancel')
          }
        }.width('100%').transition(Motion.slideFade(this.environment.reduceMotion, -8))
        else this.searchControl()
      }
      Row({ space: this.compactRoot ? 0 : 6 }) {
        Stack({ alignContent: Alignment.End }) {
          Scroll(this.quickScroller) {
            Row({ space: 4 }) {
              ForEach(this.shownQuickFilters, (key: string) => {
                Button({ type: ButtonType.Capsule }) {
                  Row({space:4}) {
                    if (key !== 'all' && key !== 'feishu' && key !== 'terminal') Circle().width(5).height(5)
                      .fill(key === 'needsYou' ? Colors.needs : key === 'unread' ? Colors.unread : key === 'working' ? Colors.working : Colors.secondary)
                    Text(tr(QUICK_LABELS[key])).fontSize(Tokens.interfaceFontVp(15,this.environment.fontScale))
                      .fontWeight(this.workspace.quickFilter === key ? FontWeight.Bold : FontWeight.Normal)
                      .fontColor(this.workspace.quickFilter === key ? Colors.link : Colors.ink).maxLines(1)
                    if (key !== 'all' && this.workspace.filterCounts[key] !== undefined) Text(String(Wire.number(this.workspace.filterCounts,key)))
                      .fontSize(Tokens.interfaceFontVp(15,this.environment.fontScale)).fontWeight(FontWeight.Medium).fontColor(Colors.secondary)
                  }.padding({left:8,right:8,top:4,bottom:4}).borderRadius(Tokens.radiusCapsule)
                    .backgroundColor(this.compactRoot && this.workspace.quickFilter === key ? Colors.selection : Color.Transparent)
                    .animation(Motion.state(this.environment.reduceMotion))
                }
                  .height(this.quickHeight()).constraintSize({minWidth:this.quickHeight()}).padding(0)
                  .backgroundColor(this.compactRoot ? Color.Transparent : this.workspace.quickFilter === key ? Colors.selection : Colors.control)
                  .animation(Motion.state(this.environment.reduceMotion))
                  .accessibilityText(tr(QUICK_LABELS[key]))
                  .accessibilityDescription(key !== 'all' && this.workspace.filterCounts[key] !== undefined ? String(Wire.number(this.workspace.filterCounts,key)) : '')
                  .accessibilitySelected(this.workspace.quickFilter === key)
                  .responseRegion({x:0,y:0,width:'100%',height:this.quickHeight()})
                  .onClick(() => {
                    Haptics.tap();
                    Motion.run(this.getUIContext(), Motion.state(this.environment.reduceMotion), () => {
                      this.workspace.setQuickFilter(key);
                    });
                    this.revealQuickFilter(key);
                  }).id('agents.quick.' + key)
              }, (key: string) => key)
            }.padding({left:4,right:18})
          }.width('100%').height(this.quickHeight()).align(Alignment.Start).scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off)
            .id('agents.quick')
          Row()
            .width(24)
            .height(this.quickHeight())
            .linearGradient({
              direction: GradientDirection.Right,
              colors: this.compactRoot ? [
                [Colors.surfaceTransparent, 0],
                [Colors.surface, 1]
              ] : [
                [Colors.groupedTransparent, 0],
                [Colors.grouped, 1]
              ]
            })
            .hitTestBehavior(HitTestMode.None)
            .accessibilityLevel('no')
        }.layoutWeight(1).height(this.quickHeight())
        if (this.compactRoot) Divider().vertical(true).strokeWidth(0.5).height(22*this.quickHeight()/44).color(Colors.divider)
        this.filterControl()
      }.width('100%').height(this.quickHeight()).borderRadius(this.compactRoot ? Tokens.radiusCapsule : 0).clip(this.compactRoot)
        .backgroundColor(this.compactRoot ? Colors.surface : Color.Transparent)
        .shadow(this.compactRoot ? PhoneStyle.cardElevation(this.environment.dark) : { radius: 0, color: Color.Transparent })
        .outlineWidth(this.compactRoot ? 0.5 : 0).outlineColor(Colors.divider).outlineRadius(Tokens.radiusCapsule)
      if (this.workspace.machineFilter) {
        Button({ type: ButtonType.Capsule }) {
          Row({ space: 5 }) {
            SymbolGlyph($r('sys.symbol.xmark')).fontSize('11vp').fontColor([Colors.link])
            Text(this.machineLabel()).fontSize(13).maxFontScale(Tokens.navigationTextMaxScale).fontColor(Colors.link).flexShrink(1).maxLines(2)
          }
        }.constraintSize({minHeight:44,maxWidth:'100%'}).padding({ left: 10, right: 10, top:3, bottom:3 }).backgroundColor(Colors.surface)
          .accessibilityText(this.machineLabel() + ' · ' + tr('clear')).onClick(() => this.selectMachine(0)).id('agents.machine.clear')
      }
    }.alignItems(HorizontalAlign.Start).padding({ left: Tokens.page, right: Tokens.page, top: this.headerTopInset(), bottom: 4 }).width('100%')
  }

  @Builder renameForm() {
    Column({ space: Tokens.gap }) {
      TextInput({ text: this.renameTitle, placeholder: tr('agent_title') }).onChange((value: string) => this.renameTitle = value)
        .id('agents.rename_title')
      TextInput({ text: this.renameTab, placeholder: tr('agent_tab_name') }).onChange((value: string) => this.renameTab = value)
        .id('agents.rename_tab')
      Row({ space: Tokens.gap }) {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('save')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('save', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('save')).bindTips(tr('save')).enabled(!this.workspace.busy)
          .onClick(() => this.saveRename()).id('agents.rename_save')
      }.width('100%').justifyContent(FlexAlign.End)
      if (this.workspace.error) Text(tr(this.workspace.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta))
        .fontColor($r('app.color.persona_danger')).width('100%')
    }.padding(Tokens.page).width('100%')
  }

  @Builder railAvatar(binding: BoundAgentRow) {
    ListItem() {
      Button({ type: ButtonType.Circle }) {
        Stack({ alignContent: Alignment.BottomEnd }) {
          Stack() {
            if (binding.presentation.brand === 'claude') {
              Image($r('app.media.project_agent_claude')).width(20).height(20).objectFit(ImageFit.Contain)
            } else if (binding.presentation.brand === 'codex') {
              Image($r('app.media.project_agent_openai')).width(20).height(20).objectFit(ImageFit.Contain).fillColor(Colors.ink)
            } else if (this.rowTitle(binding) || binding.presentation.provider) {
              Text((this.rowTitle(binding) || binding.presentation.provider).trim().slice(0, 1).toUpperCase())
                .fontSize('15vp').fontWeight(FontWeight.Bold).fontColor(Colors.ink).maxLines(1)
            } else {
              SymbolGlyph(Symbols.agent).fontSize('18vp').fontColor([Colors.secondary])
            }
          }
          .width(40)
          .height(40)
          .backgroundColor(this.isRowSelected(binding) ? Colors.selection : Colors.surface)
          .borderRadius(Tokens.radiusCapsule)
          .border({
            width: this.isRowSelected(binding) ? 2 : PhoneStyle.surfaceEdgeWidth(0.5),
            color: this.isRowSelected(binding) ? Colors.link : Colors.divider,
            radius: Tokens.radiusCapsule
          })
          .shadow(PhoneStyle.cardElevation(this.environment.dark))
          if (binding.presentation.status === 'needs_you') {
            NeedsYouPulseDot({ dotSize: 9, tint: Colors.needs, surface: Colors.grouped, bordered: true }).offset({ x: 3, y: 3 })
          } else if (binding.presentation.status === 'working') {
            WorkingArcSpinner({ diameter: 14, tint: Colors.working, surface: Colors.grouped, showCoreDot: true }).offset({ x: 3, y: 3 })
          } else if (binding.presentation.status === 'unread' || binding.presentation.status === 'done') {
            Circle().width(10).height(10).fill(Colors.unread)
              .stroke(Colors.grouped).strokeWidth(1.8)
          }
        }.width(40).height(40)
      }
      .width(48)
      .height(48)
      .padding(0)
      .backgroundColor(Color.Transparent)
      .accessibilityText([this.rowTitle(binding), this.rowStatus(binding.presentation)].filter((s: string) => !!s).join(' · '))
      .accessibilitySelected(this.isRowSelected(binding))
      .bindTips(this.rowTitle(binding))
      .onClick(() => {
        Haptics.selection();
        this.openRow(binding.bound.value);
      })
      .bindContextMenu(this.rowMenu(binding), ResponseType.LongPress)
      .id('agent.rail.' + binding.bound.value.id)
    }
    .width('100%')
    .padding({ top: 3, bottom: 3 })
    .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => this.onVisible(binding.bound.value, visible ? ratio : 0))
    .onDisAppear(() => this.onVisible(binding.bound.value, 0))
  }

  @Builder resultList() {
    if (this.rail) {
      List({ scroller: this.agentScroller }) {
        if (this.workspace.loading && !this.railBindings.length) {
          ListItem() {
            Row() { LoadingProgress().width(24).height(24) }
              .width('100%').justifyContent(FlexAlign.Center).padding({ top: 12, bottom: 12 })
          }
        }
        ForEach(this.railBindings, (rowBinding: BoundAgentRow) => this.railAvatar(rowBinding), (rowBinding: BoundAgentRow) => rowBinding.key)
      }
      .width('100%')
      .height('100%')
      .alignListItem(ListItemAlign.Center)
      .scrollBar(BarState.Off)
      .padding({ top: 6, bottom: 12 })
      .backgroundColor(Colors.grouped)
      .id('agents.rail.list')
    } else {
      List({scroller:this.agentScroller}) {
        if (this.workspace.loading && !this.workspace.hasRows()) ListItem() { LoadingProgress().height(Tokens.touch).width(Tokens.touch).padding(Tokens.page) }
        if (!this.externalSearchControls && shouldShowPersonalAgentsHeading(this.workspace.quickFilter, this.workspace.search, this.personalAgentCount)) this.personalAgentsHeadingRow()
        if (this.workspace.quickFilter === 'unread' && this.workspace.unreadShownRows.length > 0) this.markAllSeenRow()
        ForEach(this.groupBindings, (binding: BoundAgentGroup) => {
            ListItemGroup({ header: () => this.groupHeader(binding) }) {
              ForEach(this.displayRows(binding), (rowBinding: BoundAgentRow) => this.groupRow(rowBinding, binding.value.id,
                binding.value.presentation !== 'app-project' || binding.value.lastProject), (rowBinding: BoundAgentRow) =>
                rowBinding.key)
            }.id('agents.group.' + binding.value.id)
            if (binding.value.presentation !== 'app-parent' && !binding.value.folded &&
                (binding.value.capped || binding.value.totalRowCount > binding.value.rows.length)) ListItem() {
              Button() {
                Row({ space: Tokens.gap }) {
                  Text(binding.value.capped ? (binding.value.rows.length === binding.value.totalRowCount ? tr('agent_collapse') :
                    tr('agent_show_all_count').replace('{count}',String(binding.value.totalRowCount))) :
                    String(binding.value.totalRowCount-binding.value.rows.length)+' '+tr('agent_group_more_idle'))
                    .fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor(Colors.link).layoutWeight(1)
                  SymbolGlyph(Symbols.down).fontSize(Tokens.symbolFont).fontColor([Colors.link])
                }.width('100%').padding(Tokens.page).constraintSize({ minHeight: Tokens.touch })
              }.type(ButtonType.Normal).buttonStyle(ButtonStyleMode.TEXTUAL).backgroundColor(Color.Transparent).width('100%')
                .onClick(() => this.workspace.toggleIdleRows(binding.value.id)).id('agents.idle.expand.' + binding.value.id)
            }
          }, (binding: BoundAgentGroup) => binding.key)
        if (this.workspace.groupingMode === 'repo') {
          AgentRepositoryRows({model:this.model,workspace:this.workspace,catalog:this.projectCatalog,rows:this.workspace.unpinnedRows,
            rowActions:(row:ResourceRow,spaceName:string)=>{this.sessionMenu(row,spaceName);},
            onOpen:(row:ResourceRow)=>this.openRow(row),onRoute:(kind:string,key:string)=>this.onRepositoryOpen(kind,key),
            onChanged:(tree:ProjectWorktree,change:WorktreeChange)=>this.onRepositoryChanged(tree,change)})
        } else if (this.workspace.groupingMode === 'todo' || this.workspace.groupingMode === 'direction') {
          Repeat<AgentTaskSection>(this.taskSections).each((entry:RepeatItem<AgentTaskSection>) => {
            AgentTaskGroupRows({section:entry.item,workspace:this.workspace,phone:this.compactRoot && !this.rail,onOpen:(row:ResourceRow)=>this.openRow(row),
              onOpenTodo:(row:ResourceRow)=>this.onOpenTodo(row),
              rowContent:(row:ResourceRow,depth:number):void=>this.taskRow(row,depth)})
          }).key((section:AgentTaskSection)=>section.key)
        } else if (!this.workspace.hasRows() && !this.workspace.loading) ListItem() {
          Text(tr(this.workspace.search ? 'no_results' : 'empty')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).padding(Tokens.page).width('100%')
        }.id('agents.empty')
      }.width('100%').height('100%').onAreaChange((_:Area,area:Area)=>{this.listWidth=Number(area.width);}).padding({ left: this.floatingStatusCards ? 0 : 12, right: this.floatingStatusCards ? 0 : 12, bottom: Tokens.listTail }).backgroundColor(Colors.grouped)
        .onTouch((event: TouchEvent) => this.trackPull(event)).id('agents.list.'+this.workspace.groupingMode)
    }
  }

  build() {
    Column() {
      if (!this.externalSearchControls) this.filters()
      if (!this.rail && this.model.directoryUnavailable > 0) Text(tr('directory_partly_unavailable')).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor($r('sys.color.font_secondary')).padding(Tokens.inset).width('100%')
      if (!this.rail && this.decisions.error) Text(tr(this.decisions.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta)).fontColor(Colors.danger).padding(Tokens.inset).width('100%')
      if (!this.rail && this.workspace.error && !this.editorOpen) Text(tr(this.workspace.error)).fontSize(Tokens.meta).maxFontScale(Tokens.interfaceScaleFor(Tokens.meta))
        .fontColor($r('app.color.persona_danger')).padding(Tokens.inset).width('100%')
      if (this.externalSearchControls || this.rail) {
        Column() {
          this.resultList()
        }.width('100%').layoutWeight(1)
      } else Refresh({ refreshing: $$this.refreshing }) {
        this.resultList()
      }.width('100%').layoutWeight(1).onRefreshing(() => this.refreshAgents())
      .bindSheet($$this.filterSheetOpen, this.filterSheet(), {
        title: { title: tr('agent_filters_title') }, height: SheetSize.FIT_CONTENT, showClose: true,
        onAppear: () => { this.filterReady = true; this.measureFilters(); }, onDisappear: () => this.filterReady = false
      })
    }.width('100%').height('100%').backgroundColor(Colors.grouped)
      .onAreaChange((_previous:Area,area:Area) => {
        const y=Number(area.globalPosition.y);
        if (this.compactRoot && Math.abs(y-this.pageOriginY)>0.2) this.pageOriginY=y;
      })
      .bindSheet($$this.editorOpen, this.renameForm(), { title: { title: tr('rename') }, height: SheetSize.MEDIUM, showClose: true })
      .bindSheet($$this.personalAgentsSheetOpen, this.personalAgentsSheetBuilder(), { height: SheetSize.LARGE, showClose: false, dragBar: true, backgroundColor: Colors.sheetBackground })
  }
}
