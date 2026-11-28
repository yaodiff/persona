import type { AgentDecisionQueue } from './AgentDecisionQueue';
import type { ClientModel } from './ClientModel';
import { ResourceRow, Wire } from './Wire';
import type { AgentListAdapter } from '../core/AgentListAdapter';
import type { AgentListInput, AgentListRow, AgentListPreferences, CoreAgentListGroup, CoreAgentListProjection } from '../core/AgentListProjection';
import { CoreAgentListAdapter } from '../core/AgentListAdapter';
import { phoneAgentGroups } from './AgentActivityGroups';

const AGENT_GROUPINGS: string[] = ['repo','space','todo','state','machine','app','recent','direction'];
export const agentGroupingLabels: Record<string,string> = {
  'repo':'agent_group_repo', 'space':'agent_group_space', 'todo':'agent_group_todo', 'state':'agent_group_state',
  'machine':'agent_group_machine', 'app':'agent_group_app', 'recent':'agent_group_recent', 'direction':'agent_group_direction'
};

interface AgentRepository {
  call(path: string, method?: string, body?: Record<string, Object>, operationId?: string, admit?: () => boolean): Promise<Record<string, Object>>;
}

interface AgentOwnerSnapshot {
  root: ClientModel;
  rootScope: string;
  rootGeneration: number;
  revision: number;
  client: ClientModel;
  repo: AgentRepository;
  generation: number;
  scope: string;
  viewScope: string;
  manager: string;
  rowKey: string;
  rowTarget: string;
  actionEpoch: number;
}

@ObservedV2
export class AgentWorkspaceGroup {
  id: string = '';
  machineId: string = '';
  machineName: string = '';
  repositoryPath: string = '';
  repositoryName: string = '';
  worktreePath: string = '';
  worktreeName: string = '';
  branch: string = '';
  showMachine: boolean = false;
  showRepository: boolean = false;
  showWorktree: boolean = false;
  folded: boolean = false;
  parent: string = '';
  stateCounts: Record<string, Object> = {};
  spoken: string = '';
  totalRowCount: number = 0;
  memberIds: string[] = [];
  rows: ResourceRow[] = [];
  presentation: string = '';
  titleKey: string = '';
  subtitle: string = '';
  firstProject: boolean = false;
  lastProject: boolean = false;
  capped: boolean = false;

  constructor(id: string, machineId: string, machineName: string, repositoryPath: string, repositoryName: string,
    worktreePath: string, worktreeName: string, branch: string, rows: ResourceRow[] = []) {
    this.id = id; this.machineId = machineId; this.machineName = machineName; this.repositoryPath = repositoryPath;
    this.repositoryName = repositoryName; this.worktreePath = worktreePath; this.worktreeName = worktreeName;
    this.branch = branch; this.rows = rows; this.totalRowCount = rows.length;
  }
}

function segment(value: string): string { return encodeURIComponent(value); }
function rowSession(row: ResourceRow): string { return row.session || Wire.text(row.data(), 'session_id') || row.id; }
function rowMachine(row: ResourceRow): string { return row.machine || Wire.text(row.data(), 'machine_id'); }
function rowKey(row: ResourceRow): string { return JSON.stringify(row.group ? [row.group, row.kind, row.manager, rowMachine(row), rowSession(row)] : [row.kind, row.manager, rowMachine(row), rowSession(row)]); }
function rowSpaceKey(row: ResourceRow, space?: string): string {
  const name = space ?? Wire.text(row.data(), 'space_name');
  return name ? rowMachine(row) + '/' + name : '';
}
export function coreRowId(row: ResourceRow): string {
  return (row.group ? row.group + '|' : '') + (row.manager ? row.manager + '|' : '') + rowMachine(row) + '|' + rowSession(row);
}
function cloneRow(row: ResourceRow): ResourceRow {
  const copy = new ResourceRow(row.id, row.title, row.kind);
  copy.group = row.group; copy.detail = row.detail; copy.state = row.state; copy.manager = row.manager; copy.machine = row.machine;
  copy.session = row.session; copy.path = row.path; copy.raw = row.raw;
  return copy;
}
function stringsFrom(value: Record<string, Object>, key: string, optional: boolean = false): string[] {
  if (!Wire.has(value, key) && optional) return [];
  const item = value[key];
  if (!Array.isArray(item)) throw new Error('invalid_preferences');
  const values = item as Object[];
  if (values.some((part: Object) => typeof part !== 'string')) throw new Error('invalid_preferences');
  return values.map((part: Object) => part as string);
}
function removeKey(values: string[], key: string): string[] { return values.filter((value: string) => value !== key); }
function toggleKey(values: string[], key: string): string[] {
  return values.includes(key) ? removeKey(values, key) : values.concat([key]);
}

class AgentPreferences {
  pinsSpaces: string[] = []; pinsSessions: string[] = [];
  mutesSpaces: string[] = []; mutesSessions: string[] = []; mutesAllowed: string[] = [];
}

/** Native Agents list state and the hub's pins, mutes, and read receipts. */
@ObservedV2
export class AgentWorkspace {
  @Trace groups: AgentWorkspaceGroup[] = [];
  @Trace unpinnedRows: ResourceRow[] = [];
  @Trace unreadShownRows: ResourceRow[] = [];
  @Trace quickFilter: string = 'all';
  @Trace filterCounts:Record<string,Object> = {};
  @Trace groupingMode: string = 'state';
  @Trace machineFilter: string = '';
  @Trace currentHubOnly: boolean = false;
  @Trace search: string = '';
  @Trace history: boolean = false;
  @Trace selectedKey: string = '';
  @Trace loading: boolean = false;
  @Trace busy: boolean = false;
  @Trace error: string = '';
  @Trace pinsSpaces: string[] = [];
  @Trace pinsSessions: string[] = [];
  @Trace mutesSpaces: string[] = [];
  @Trace mutesSessions: string[] = [];
  @Trace mutesAllowed: string[] = [];
  /**
   * Mirrors iOS `ChatsView.kept`: under a quick filter (e.g. Unread), sessions that matched when
   * entering the filter stay in place after being opened/read so the reader can step through sequentially.
   */
  private filterKept: string[] = [];

  private client: ClientModel | undefined;
  @Trace private preferences: Map<string, AgentPreferences> = new Map();
  private owner: string = '';
  private scope: string = '';
  private viewScope: string = '';
  private revision: number = 0;
  private actionEpoch: number = 0;
  revokeRowActions(): void { this.actionEpoch++; this.busy = false; }
  private supervisorQueue: AgentDecisionQueue | undefined;
  private hidSupervisorRows: boolean = false;
  private sourceRows: ResourceRow[] = [];
  private latestSourceRows: ResourceRow[] = [];
  private externalSelection: ResourceRow | undefined;
  private historyRows: ResourceRow[] = [];
  private historyLoaded: boolean = false;
  private frozenGroups: AgentWorkspaceGroup[] | undefined;
  private readingDetail: boolean = false;
  private openingView: boolean = false;
  private freezeSuppressed: boolean = false;
  private historyRequest: number = 0;
  private persistTimer: number = -1;
  private hubModeLoaded: boolean = false;
  private adapter: AgentListAdapter;
  private defaultGrouping: string;
  private preferredGrouping: string;
  @Trace private supportsTodos: boolean = false;
  @Trace private supportsDirections: boolean = false;
  private readonly standaloneView: boolean;
  private projectRows: ResourceRow[] | undefined;
  constructor(adapter?: AgentListAdapter, defaultGrouping: string = 'state', standaloneView: boolean = true) {
    this.standaloneView = standaloneView; this.hubModeLoaded = !standaloneView;
    this.groupingMode = defaultGrouping;
    this.adapter = adapter || new CoreAgentListAdapter();
    this.defaultGrouping = defaultGrouping;
    this.preferredGrouping = defaultGrouping;
  }
  @Trace private folded: string[] = [];
  private expandedIdle: string[] = [];
  private expandedQuiet: string[] = [];
  private heldSession: string = '';
  includesHub(group: string): boolean {
    const current = this.client?.profile.id;
    return !this.currentHubOnly || !current || !group || current === group;
  }
  private shownOwners(client: ClientModel): ClientModel[] {
    return client.directoryOwners().filter((owner: ClientModel) => this.includesHub(owner.profile.id));
  }
  historySupported(): boolean {
    return !!this.client && this.shownOwners(this.client).some((owner: ClientModel) => owner.features.includes('agent_session_archives'));
  }
  rowOnline(row: ResourceRow): boolean { return this.ownerFor(row)?.connected === true; }
  hasRows(): boolean { return this.groups.some((group: AgentWorkspaceGroup) => group.totalRowCount > 0); }
  visibleRows(): ResourceRow[] {
    const rows: ResourceRow[] = [];
    this.groups.forEach((group: AgentWorkspaceGroup) => group.rows.forEach((row: ResourceRow) => rows.push(row)));
    return rows;
  }
  isSelected(row: ResourceRow): boolean { return this.selectedKey === rowKey(row); }
  rowMetadata(row: ResourceRow): string[] {
    const data = row.data(), machineId = rowMachine(row);
    const path = row.path || Wire.text(data, 'worktree') || Wire.text(data, 'repo_root') || Wire.text(data, 'cwd');
    const owner = this.ownerFor(row);
    const machine = machineId ? owner?.machineName(machineId) || machineId : '';
    const identity = this.client ? [this.client.groupName(row), row.manager].filter((value: string) => !!value).join(' · ') : '';
    const shortPath = path && this.client ? this.client.shortPath(path) : path;
    const state = row.state || Wire.text(data, 'status');
    const content = Wire.text(data, 'last_text') || row.detail;
    return [identity, machine, shortPath, state, content];
  }

  listMetadata(row: ResourceRow): string[] {
    const metadata = this.rowMetadata(row);
    const machineId = rowMachine(row);
    return [this.client?.groupName(row) || '', metadata[1] === machineId ? '' : metadata[1], metadata[3], metadata[4]];
  }

  isBoundTo(client: ClientModel): boolean {
    return this.client === client && this.scope === client.scope() && this.owner === client.profile.manager;
  }

  async open(client: ClientModel, readingDetail: boolean = false): Promise<void> {
    const revision = ++this.revision;
    this.client = client; this.scope = client.scope(); this.owner = client.profile.manager;
    this.viewScope = JSON.stringify([this.scope, this.owner]);
    this.openingView = true; this.loading = true; this.busy = false; this.error = '';
    this.externalSelection = undefined; this.groups = []; this.sourceRows = []; this.latestSourceRows = []; this.projectRows = undefined; this.historyRows = []; this.historyLoaded = false; this.frozenGroups = undefined;
    this.supervisorQueue = undefined; this.hidSupervisorRows = false;
    this.quickFilter = 'all'; this.filterCounts = {}; this.unpinnedRows = []; this.unreadShownRows = []; this.filterKept = [];
    this.preferredGrouping = this.defaultGrouping; this.groupingMode = this.defaultGrouping;
    this.supportsTodos = false; this.supportsDirections = false;
    this.machineFilter = ''; this.search = ''; this.history = false; this.selectedKey = '';
    this.readingDetail = false; this.freezeSuppressed = false;
    this.preferences = new Map<string, AgentPreferences>(); this.pinsSpaces = []; this.pinsSessions = []; this.mutesSpaces = []; this.mutesSessions = []; this.mutesAllowed = [];
    try {
      const values = await Promise.all([this.standaloneView ? client.loadView('agent-workspace') : Promise.resolve(''), this.hubModeLoaded ? Promise.resolve('') : client.loadView('agent-hub-mode')]);
      if (!this.loadCurrent(client, revision)) return;
      const saved = Wire.parse(values[0]);
      if (!this.hubModeLoaded) { this.currentHubOnly = values[1] === 'current'; this.hubModeLoaded = true; }
      this.quickFilter = ['all', 'needsYou', 'unread', 'working', 'backgroundWaiting', 'feishu', 'terminal'].includes(Wire.text(saved, 'quick')) ? Wire.text(saved, 'quick') :
        (Wire.text(saved, 'quick') === 'needs_you' ? 'needsYou' : 'all');
      const storedGrouping = Wire.text(saved,'grouping');
      this.preferredGrouping = AGENT_GROUPINGS.includes(storedGrouping) ? storedGrouping : this.defaultGrouping;
      this.groupingMode = this.resolvedGrouping();
      const savedMachine = Wire.text(saved, 'machine');
      this.machineFilter = this.machineForShownHub(savedMachine); this.search = Wire.text(saved, 'search');
      this.history = Wire.bool(saved, 'history');
      if (this.history && client.connected && !this.historySupported()) this.history = false;
      this.selectedKey = Wire.text(saved, 'selected');
      this.folded = Wire.strings(saved, 'folded'); this.expandedIdle = Wire.strings(saved, 'expanded_idle');
      this.expandedQuiet = Wire.strings(saved, 'expanded_quiet');
      if (this.machineFilter !== savedMachine) this.saveViewLater();
      this.latestSourceRows = this.history ? this.historyRows.slice() : this.rowsForClient(client);
      this.syncSelected(client);
      this.rebuild();
      this.setReadingDetail(readingDetail);
      if (client.connected) {
        await client.refreshDirectory();
        if (!this.loadCurrent(client, revision)) return;
        this.latestSourceRows = this.history ? this.historyRows.slice() : this.rowsForClient(client); this.rebuild();
        await this.refreshDirectoryPreferences(client, revision);
        if (!this.loadCurrent(client, revision)) return;
        if (this.history) await this.loadHistory(client, revision);
      }
    } catch (failure) {
      if (this.loadCurrent(client, revision)) this.fail(failure as Error);
    } finally {
      if (this.loadCurrent(client, revision)) { this.openingView = false; this.loading = false; this.rebuild(); }
    }
  }

  hide(): void {
    this.supervisorQueue = undefined; this.hidSupervisorRows = false;
    this.projectRows = undefined;
    this.filterCounts = {}; this.unpinnedRows = []; this.unreadShownRows = []; this.filterKept = [];
    this.revision++; this.openingView = false; this.client = undefined; this.groups = []; this.sourceRows = []; this.latestSourceRows = []; this.historyRows = []; this.historyLoaded = false;
    this.frozenGroups = undefined; this.readingDetail = false;
    if (this.persistTimer !== -1) clearTimeout(this.persistTimer);
    this.persistTimer = -1;
  }

  update(client: ClientModel, readingDetail: boolean = this.readingDetail): void {
    const nextScope = JSON.stringify([client.scope(), client.profile.manager]);
    if (this.client !== client || this.viewScope !== nextScope) {
      this.open(client, readingDetail).catch(() => undefined);
      return;
    }
    if (this.history && client.connected && !this.historySupported()) {
      this.history = false; this.latestSourceRows = this.rowsForClient(client); this.saveViewLater();
    }
    this.syncSelected(client);
    this.setReadingDetail(readingDetail);
    this.latestSourceRows = this.history ? this.historyRows.slice() : this.rowsForClient(client);
    this.rebuild();
  }

  async refresh(client: ClientModel): Promise<void> {
    const nextScope = JSON.stringify([client.scope(), client.profile.manager]);
    if (this.client !== client || this.viewScope !== nextScope) { await this.open(client, this.readingDetail); return; }
    this.update(client, this.readingDetail);
    const snapshot = this.capture(client);
    if (!snapshot) return;
    try {
      await client.refreshDirectory();
      if (!this.ownerActive(snapshot)) return;
      this.latestSourceRows = this.history ? this.historyRows.slice() : this.rowsForClient(client); this.rebuild();
      await this.refreshDirectoryPreferences(client, this.revision);
      if (this.history) await this.loadHistory(client, this.revision);
    } catch (failure) { if (this.ownerActive(snapshot)) this.fail(failure as Error); }
  }

  setReadingDetail(value: boolean): void {
    if (value === this.readingDetail) return;
    if (value) {
      if (this.readingDetail) return;
      this.heldSession = ''; this.rebuild();
      this.readingDetail = true;
      this.freezeSuppressed = false;
      if (this.sourceReady() || this.groups.length > 0) {
        this.frozenGroups = this.copyGroups(this.groups);
      }
    } else {
      this.readingDetail = false;
      this.heldSession = '';
      this.freezeSuppressed = false;
      this.frozenGroups = undefined;
      this.rebuild();
    }
  }

  setSearch(value: string): void {
    this.releaseFreeze(); this.filterKept = []; this.search = value; this.rebuild(); this.refreezeAfterFilter(); this.saveViewLater();
  }

  private compactDisplay:boolean=false;
  setCompactDisplay(value:boolean):void {
    if(this.compactDisplay===value)return;
    this.compactDisplay=value;this.rebuild();
  }

  /** Resets quick-filter kept rows (e.g. pull-to-refresh or reselecting active chip). */
  clearFilterKept(): void {
    if (!this.filterKept.length) return;
    this.filterKept = [];
    this.rebuild();
  }

  setQuickFilter(value: string): void {
    if (!['all', 'needsYou', 'working', 'unread', 'backgroundWaiting', 'feishu', 'terminal'].includes(value)) return;
    this.releaseFreeze(); this.filterKept = []; this.quickFilter = value; this.rebuild(); this.refreezeAfterFilter(); this.saveViewLater();
  }

  groupingOptions(): string[] {
    return AGENT_GROUPINGS.filter((key:string)=>(key !== 'todo' || this.supportsTodos) && (key !== 'direction' || this.supportsDirections));
  }
  private resolvedGrouping(): string {
    if (this.preferredGrouping === 'direction' && !this.supportsDirections) return this.supportsTodos ? 'todo' : 'space';
    return this.preferredGrouping === 'todo' && !this.supportsTodos ? 'space' : this.preferredGrouping;
  }
  updateGroupingSupport(todos: boolean, directions: boolean): void {
    this.supportsTodos=todos; this.supportsDirections=directions;
    const resolved = this.resolvedGrouping();
    if (resolved === this.groupingMode) return;
    this.releaseFreeze(); this.groupingMode = resolved; this.rebuild(); this.refreezeAfterFilter();
  }
  setGroupingMode(value: string): void {
    if (!this.groupingOptions().includes(value) || value === this.preferredGrouping) return;
    this.releaseFreeze(); this.preferredGrouping = value; this.groupingMode = value; this.rebuild(); this.refreezeAfterFilter(); this.saveViewLater();
  }

  groupingFolded(id: string, fallback: boolean): boolean {
    return this.folded.includes('!'+id) ? false : this.folded.includes(id) || fallback;
  }
  toggleGroupingFold(id: string, folded: boolean): void {
    this.folded=this.folded.filter((entry:string)=>entry !== id && entry !== '!'+id).concat([folded ? '!'+id : id]);
    this.saveViewLater();
  }

  toggleFold(id: string): void {
    if (this.groups.some((group) => group.parent === 'quiet' && group.id === id)) {
      this.expandedQuiet = this.expandedQuiet.includes(id) ? removeKey(this.expandedQuiet, id) : this.expandedQuiet.concat([id]);
    } else if (id === 'state/idle') {
      this.folded = this.folded.includes('!state/idle') ? removeKey(this.folded, '!state/idle') : this.folded.concat(['!state/idle']);
      this.toggleIdleRows(id);
      return;
    } else {
      this.folded = this.folded.includes(id) ? removeKey(this.folded, id) : this.folded.concat([id]);
    }
    this.releaseFreeze();
    this.rebuild(); this.refreezeAfterFilter(); this.saveViewLater();
  }

  toggleIdleRows(id: string): void {
    this.releaseFreeze();
    this.expandedIdle = this.expandedIdle.includes(id) ? removeKey(this.expandedIdle, id) : this.expandedIdle.concat([id]);
    this.rebuild(); this.refreezeAfterFilter(); this.saveViewLater();
  }

  async setMachineFilter(value: string): Promise<void> {
    this.releaseFreeze(); this.filterKept = []; this.machineFilter = this.machineForShownHub(value); this.rebuild(); this.saveViewLater();
    if (this.history) {
      const client = this.client;
      if (client) await this.loadHistory(client, this.revision, true);
      else this.refreezeAfterFilter();
    } else {
      this.refreezeAfterFilter();
    }
  }

  private machineForShownHub(value: string): string {
    if (!value || !this.currentHubOnly || !this.client?.profile.id) return value;
    let group = '';
    if (value.startsWith('[')) {
      try {
        const parts = JSON.parse(value) as string[];
        if (parts.length === 3 && typeof parts[0] === 'string') group = parts[0];
      } catch (_) {}
    } else {
      const parts = value.split('|');
      if (parts.length >= 3 || parts.length === 2 && this.client.directoryOwners().some((owner: ClientModel) => owner.profile.id === parts[0])) group = parts[0];
    }
    return this.includesHub(group) ? value : '';
  }

  async setCurrentHubOnly(value: boolean): Promise<void> {
    const client = this.client;
    if (!client || this.currentHubOnly === value) return;
    this.hubModeLoaded = true; this.currentHubOnly = value; this.releaseFreeze(); this.filterKept = [];
    this.historyRequest++; this.historyRows = []; this.historyLoaded = false; this.loading = false;
    this.machineFilter = this.machineForShownHub(this.machineFilter);
    if (this.history && !this.historySupported()) this.history = false;
    this.latestSourceRows = this.history ? [] : this.rowsForClient(client);
    this.rebuild(); this.saveViewLater();
    const saved = client.saveView('agent-hub-mode', value ? 'current' : 'all').catch(() => undefined);
    if (this.history) await this.loadHistory(client, this.revision, true);
    else this.refreezeAfterFilter();
    await saved;
  }

  async setHistory(value: boolean): Promise<void> {
    if (value && !this.historySupported()) return;
    this.releaseFreeze();
    this.historyRequest++; this.loading = false;
    this.history = value;
    this.error = '';
    this.latestSourceRows = value ? [] : this.rowsForClient(this.client);
    this.rebuild(); this.saveViewLater();
    if (value && this.client) await this.loadHistory(this.client, this.revision, true);
    else this.refreezeAfterFilter();
  }

  actionTicket(row: ResourceRow): () => boolean { const snapshot = this.captureRow(row, this.sessionPath(row)); return (): boolean => !!snapshot && this.active(snapshot); }
  forgetHub(group: string): void {
    this.historyRequest++; this.loading = false;
    const keep = (row: ResourceRow): boolean => row.group !== group;
    this.latestSourceRows = this.sourceRows.filter(keep); this.historyRows = this.historyRows.filter(keep);
    if (this.externalSelection?.group === group) { this.externalSelection = undefined; this.selectedKey = ''; }
    if (this.frozenGroups) this.frozenGroups = this.frozenGroups.map((source: AgentWorkspaceGroup) => { const copy = this.copyGroup(source); copy.rows = copy.rows.filter(keep); return copy; }).filter((source: AgentWorkspaceGroup) => source.rows.length > 0);
    this.filterKept = [];
    this.preferences = new Map<string, AgentPreferences>(); this.rebuild();
  }
  selectExternal(row: ResourceRow): void { this.externalSelection = row; if (this.client) this.syncSelected(this.client); }

  selectRow(row: ResourceRow): void {
    if (row.kind !== 'session' || !this.groups.some((group: AgentWorkspaceGroup) =>
      group.rows.some((item: ResourceRow) => rowKey(item) === rowKey(row)))) return;
    this.externalSelection = row; this.selectedKey = rowKey(row);
    this.heldSession = row.state === 'unread' ? (row.group ? row.group + '|' : '') + (row.manager ? row.manager + '|' : '') +
      rowMachine(row) + '|' + rowSession(row) : '';
    this.saveViewLater();
  }

  isPinned(row: ResourceRow): boolean { return this.preferencesFor(row).pinsSessions.includes(this.sessionKey(row)); }
  isSpacePinned(row: ResourceRow): boolean { const key = rowSpaceKey(row); return !!key && this.preferencesFor(row).pinsSpaces.includes(key); }
  isSpaceMuted(row: ResourceRow): boolean { const key = rowSpaceKey(row); return !!key && this.preferencesFor(row).mutesSpaces.includes(key); }
  isMuted(row: ResourceRow, space?: string): boolean {
    const key = this.sessionKey(row);
    const preferences = this.preferencesFor(row);
    if (preferences.mutesSessions.includes(key)) return true;
    if (!preferences.mutesSpaces.length || preferences.mutesAllowed.includes(key)) return false;
    const target = rowSpaceKey(row, space);
    return !!target && preferences.mutesSpaces.includes(target);
  }

  canRename(row: ResourceRow): boolean {
    if (!this.canMutateSession(row)) return false;
    const data = row.data(), source = Wire.text(data, 'source_kind');
    if (!source) return true;
    return Wire.bool(Wire.object(data, 'capabilities'), 'terminal');
  }

  canClose(row: ResourceRow): boolean {
    if (!this.canMutateSession(row)) return false;
    const data = row.data(), source = Wire.text(data, 'source_kind'), capabilities = Wire.object(data, 'capabilities');
    if (Wire.has(capabilities, 'close')) return Wire.bool(capabilities, 'close');
    return !source || Wire.bool(capabilities, 'terminal');
  }

  canDelete(row: ResourceRow): boolean {
    if (!this.canMutateSession(row) || this.history) return false;
    const data = row.data();
    return !Wire.bool(data, 'assistant') && !Wire.bool(data, 'starting') &&
      ['offline', 'done', 'idle', 'unknown', 'interrupted'].includes(Wire.text(data, 'status'));
  }

  canMarkSeen(row: ResourceRow): boolean {
    return this.rowBelongs(row) && (row.state === 'unread' ||
      (row.state === 'idle' && Wire.bool(Wire.object(row.data(), 'work'), 'just_finished')));
  }
  readOnly(row: ResourceRow): boolean {
    const data = row.data();
    return !!Wire.text(data, 'read_only_reason') || Wire.bool(data, 'read_only') || Wire.text(data, 'source_kind') === 'codex_history';
  }

  async togglePin(row: ResourceRow, space: boolean = false): Promise<boolean> {
    const key = space ? rowSpaceKey(row) : this.sessionKey(row);
    return key ? this.changePreference('pins', key, space, row) : false;
  }

  async toggleMute(row: ResourceRow, space: boolean = false): Promise<boolean> {
    const key = space ? rowSpaceKey(row) : this.sessionKey(row);
    if (!key) return false;
    const snapshot = this.captureRow(row, '/api/v1/mutes');
    if (!snapshot || this.blocked(snapshot, '/api/v1/mutes')) return false;
    if (space) return this.changePreference('mutes', key, true, row);
    if (this.busy) return false;
    this.busy = true; this.error = '';
    try {
      const current = await snapshot.repo.call('/api/v1/mutes');
      if (!this.active(snapshot) || !this.rowBelongs(row)) return false;
      const spaces = stringsFrom(current, 'spaces');
      const sessions = stringsFrom(current, 'sessions');
      const allowed = stringsFrom(current, 'allowed', true);
      const spaceKey = rowSpaceKey(row), inherited = !!spaceKey && spaces.includes(spaceKey);
      const muted = sessions.includes(key) || (!allowed.includes(key) && inherited);
      const nextSessions = muted ? removeKey(sessions, key) : toggleKey(sessions, key);
      const nextAllowed = muted && inherited ? togglePresent(allowed, key) : removeKey(allowed, key);
      const body = Wire.payload(['spaces', 'sessions', 'allowed'], [spaces, nextSessions, nextAllowed]);
      if (!this.active(snapshot) || this.blocked(snapshot, '/api/v1/mutes')) return false;
      await snapshot.repo.call('/api/v1/mutes', 'PUT', body, '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      this.applyMutes(spaces, nextSessions, nextAllowed, snapshot.client); return true;
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  async rename(row: ResourceRow, title: string, tabName: string): Promise<boolean> {
    if (!this.canRename(row)) return false;
    const data = row.data(), priorTitle = Wire.text(data, 'title'), priorTab = Wire.text(data, 'slot_name');
    const nextTitle = title.trim(), nextTab = tabName.trim();
    const keys: string[] = [], values: Object[] = [];
    if (nextTitle !== priorTitle && nextTitle) { keys.push('title'); values.push(nextTitle); }
    if (Wire.text(data, 'pane_id') && nextTab !== priorTab && nextTab) { keys.push('tab_name'); values.push(nextTab); }
    if (!keys.length) return false;
    const path = this.sessionPath(row) + '/rename', snapshot = this.captureRow(row, path);
    if (!snapshot || this.blocked(snapshot, path) || this.busy) return false;
    this.busy = true; this.error = '';
    try {
      await snapshot.repo.call(path, 'POST', Wire.payload(keys, values), '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      await this.refreshAfterMutation(snapshot);
      return this.ownerActive(snapshot);
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  async close(row: ResourceRow): Promise<boolean> {
    if (!this.canClose(row)) return false;
    const path = this.sessionPath(row) + '/close', snapshot = this.captureRow(row, path);
    if (!snapshot || this.blocked(snapshot, path) || this.busy) return false;
    this.busy = true; this.error = '';
    try {
      await snapshot.repo.call(path, 'POST', Wire.payload([], []), '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      await this.refreshAfterMutation(snapshot);
      return this.ownerActive(snapshot);
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  async delete(row: ResourceRow): Promise<boolean> {
    if (!this.canDelete(row)) return false;
    const path = this.sessionPath(row), snapshot = this.captureRow(row, path);
    if (!snapshot || this.blocked(snapshot, path) || this.busy) return false;
    this.busy = true; this.error = '';
    try {
      await snapshot.repo.call(path, 'DELETE', Wire.payload([], []), '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      await this.refreshAfterMutation(snapshot);
      return this.ownerActive(snapshot);
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  async markSeen(row: ResourceRow): Promise<boolean> {
    if (!this.canMarkSeen(row) || this.busy) return false;
    const path = this.sessionPath(row) + '/seen', snapshot = this.captureRow(row, path);
    if (!snapshot || this.blocked(snapshot, path)) return false;
    this.busy = true; this.error = '';
    try {
      await snapshot.repo.call(path, 'POST', Wire.payload([], []), '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      this.filterKept = removeKey(this.filterKept, coreRowId(row));
      this.patchState(row, 'idle');
      return true;
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  /** Matches iOS Unread filter "Mark all as seen": marks every visible unread row seen and clears kept rows. */
  async markAllSeen(): Promise<number> {
    if (this.busy || !this.unreadShownRows.length) return 0;
    const targets = this.unreadShownRows.filter((row: ResourceRow) => this.canMarkSeen(row));
    if (!targets.length) return 0;
    this.busy = true; this.error = '';
    let marked = 0;
    try {
      const doneRows: ResourceRow[] = [];
      for (const row of targets) {
        const path = this.sessionPath(row) + '/seen', snapshot = this.captureRow(row, path);
        if (!snapshot || this.blocked(snapshot, path)) continue;
        await snapshot.repo.call(path, 'POST', Wire.payload([], []), '', (): boolean => this.active(snapshot));
        if (!this.ownerActive(snapshot)) break;
        doneRows.push(row);
        marked++;
      }
      if (doneRows.length) {
        this.filterKept = [];
        this.patchStates(doneRows, 'idle');
      }
      return marked;
    } catch (failure) {
      this.fail(failure as Error);
      return marked;
    } finally {
      this.busy = false;
    }
  }

  machineIdentity(row: ResourceRow): string { return JSON.stringify([row.group || this.client?.profile.id || '', row.manager, row.kind === 'machine' ? row.id : rowMachine(row)]); }

  private sessionKey(row: ResourceRow): string {
    const machine = rowMachine(row), session = rowSession(row);
    return machine && session ? machine + '/' + session : '';
  }

  private sessionPath(row: ResourceRow): string { return '/api/v1/sessions/' + segment(rowMachine(row)) + '/' + segment(rowSession(row)); }

  private rowBelongs(row: ResourceRow): boolean {
    if (row.kind !== 'session' || !rowMachine(row) || !rowSession(row)) return false;
    return this.sourceRows.some((item: ResourceRow) => rowKey(item) === rowKey(row));
  }

  private canMutateSession(row: ResourceRow): boolean {
    if (!this.client || this.history || !this.rowBelongs(row) || this.readOnly(row)) return false;
    const owner = this.ownerFor(row); if (!owner || !owner.connected) return false;
    const machine = owner.machines.find((item: ResourceRow) => item.id === rowMachine(row) && item.manager === row.manager);
    if (!machine) return false;
    const trust = Wire.text(machine.data(), 'trust');
    return ['', 'confirmed', 'personal', 'work'].includes(trust);
  }

  private ownerFor(row: ResourceRow): ClientModel | undefined { return this.client?.directoryOwnerFor(row); }
  private capture(client: ClientModel): AgentOwnerSnapshot | undefined {
    const root = this.client;
    if (!root || !client.connected || root.scope() !== this.scope) return undefined;
    try {
      const repo = client.repo() as AgentRepository;
      return { root: root, rootScope: root.scope(), rootGeneration: root.connectionGeneration(), revision: this.revision,
        client: client, repo: repo, generation: client.connectionGeneration(), scope: client.scope(),
        viewScope: this.viewScope, manager: client.profile.manager, rowKey: '', rowTarget: '', actionEpoch: this.actionEpoch };
    } catch (_) { return undefined; }
  }
  private captureRow(row: ResourceRow, path: string): AgentOwnerSnapshot | undefined {
    const client = this.ownerFor(row);
    if (!client || !this.rowBelongs(row)) return undefined;
    const snapshot = this.capture(client); if (!snapshot) return undefined;
    if (client.operationBlocked(path)) { this.error = 'pending_action'; return undefined; }
    snapshot.rowKey = rowKey(row); snapshot.rowTarget = client.target(row); return snapshot;
  }
  private active(snapshot: AgentOwnerSnapshot): boolean {
    if (!this.ownerActive(snapshot)) return false;
    if (snapshot.rowKey) {
      const row = this.findSourceRow(snapshot.rowKey);
      if (!row || snapshot.client.target(row) !== snapshot.rowTarget || this.ownerFor(row) !== snapshot.client) return false;
    }
    return true;
  }
  private ownerActive(snapshot: AgentOwnerSnapshot): boolean {
    if (snapshot.rowKey && snapshot.actionEpoch !== this.actionEpoch) return false;
    if (this.client !== snapshot.root || this.revision !== snapshot.revision || this.viewScope !== snapshot.viewScope ||
      !snapshot.root.owns(snapshot.rootGeneration, snapshot.rootScope) || !snapshot.client.owns(snapshot.generation, snapshot.scope) ||
      !snapshot.client.connected || snapshot.client.profile.manager !== snapshot.manager) return false;
    try { return snapshot.client.repo() === snapshot.repo; } catch (_) { return false; }
  }
  private preferencesFor(row: ResourceRow): AgentPreferences {
    const owner = this.ownerFor(row), saved = owner ? this.preferences.get(owner.scope()) : undefined;
    if (saved) return saved;
    const preferences = new AgentPreferences();
    if (owner === this.client) { preferences.pinsSpaces = this.pinsSpaces; preferences.pinsSessions = this.pinsSessions; preferences.mutesSpaces = this.mutesSpaces; preferences.mutesSessions = this.mutesSessions; preferences.mutesAllowed = this.mutesAllowed; }
    return preferences;
  }
  private async refreshDirectoryPreferences(client: ClientModel, revision: number): Promise<void> {
    const owners = new Set<ClientModel>(client.directoryOwners());
    for (const owner of owners) {
      if (!this.loadCurrent(client, revision)) return;
      const snapshot = this.capture(owner); if (snapshot) { try { await this.refreshPreferences(snapshot); } catch (failure) { if (this.ownerActive(snapshot)) this.fail(failure as Error); } }
    }
  }
  private findSourceRow(key: string): ResourceRow | undefined { return this.sourceRows.find((row: ResourceRow) => rowKey(row) === key); }

  private blocked(snapshot: AgentOwnerSnapshot, path: string): boolean {
    if (!this.active(snapshot)) return true;
    if (snapshot.client.operationBlocked(path)) { this.error = 'pending_action'; return true; }
    return false;
  }

  private loadCurrent(client: ClientModel, revision: number): boolean {
    return this.client === client && this.revision === revision && this.scope === client.scope() && this.owner === client.profile.manager;
  }

  private async refreshPreferences(snapshot: AgentOwnerSnapshot): Promise<void> {
    const pins = await snapshot.repo.call('/api/v1/pins');
    if (!this.ownerActive(snapshot)) return;
    const mutes = await snapshot.repo.call('/api/v1/mutes');
    if (!this.ownerActive(snapshot)) return;
    this.applyPins(stringsFrom(pins, 'spaces'), stringsFrom(pins, 'sessions'), snapshot.client);
    this.applyMutes(stringsFrom(mutes, 'spaces'), stringsFrom(mutes, 'sessions'), stringsFrom(mutes, 'allowed', true), snapshot.client);
    this.rebuild();
  }

  private async changePreference(kind: string, key: string, space: boolean, row: ResourceRow): Promise<boolean> {
    if (!this.client || this.busy) return false;
    const path = '/api/v1/' + kind, snapshot = this.captureRow(row, path);
    if (!snapshot || this.blocked(snapshot, path)) return false;
    this.busy = true; this.error = '';
    try {
      const current = await snapshot.repo.call(path);
      if (!this.active(snapshot)) return false;
      const spaces = stringsFrom(current, 'spaces'), sessions = stringsFrom(current, 'sessions');
      if (kind === 'pins') {
        const nextSpaces = space ? toggleKey(spaces, key) : spaces;
        const nextSessions = space ? sessions : toggleKey(sessions, key);
        const body = Wire.payload(['spaces', 'sessions'], [nextSpaces, nextSessions]);
        if (!this.active(snapshot) || this.blocked(snapshot, path)) return false;
        await snapshot.repo.call(path, 'PUT', body, '', (): boolean => this.active(snapshot));
        if (!this.ownerActive(snapshot)) return false;
        this.applyPins(nextSpaces, nextSessions, snapshot.client); return true;
      }
      const allowed = stringsFrom(current, 'allowed', true);
      const nextSpaces = space ? toggleKey(spaces, key) : spaces;
      const nextSessions = space ? sessions : toggleKey(sessions, key);
      const nextAllowed = space ? allowed : removeKey(allowed, key);
      const body = Wire.payload(['spaces', 'sessions', 'allowed'], [nextSpaces, nextSessions, nextAllowed]);
      if (!this.active(snapshot) || this.blocked(snapshot, path)) return false;
      await snapshot.repo.call(path, 'PUT', body, '', (): boolean => this.active(snapshot));
      if (!this.ownerActive(snapshot)) return false;
      this.applyMutes(nextSpaces, nextSessions, nextAllowed, snapshot.client); return true;
    } catch (failure) { if (this.active(snapshot)) this.fail(failure as Error); return false; }
    finally { if (this.ownerActive(snapshot)) this.busy = false; }
  }

  private applyPins(spaces: string[], sessions: string[], owner: ClientModel): void {
    const preferences = this.preferences.get(owner.scope()) || new AgentPreferences(); preferences.pinsSpaces = spaces.slice(); preferences.pinsSessions = sessions.slice();
    this.preferences = new Map(this.preferences).set(owner.scope(), preferences);
    if (owner === this.client) { this.pinsSpaces = spaces.slice(); this.pinsSessions = sessions.slice(); } this.rebuild();
  }
  private applyMutes(spaces: string[], sessions: string[], allowed: string[], owner: ClientModel): void {
    const preferences = this.preferences.get(owner.scope()) || new AgentPreferences(); preferences.mutesSpaces = spaces.slice(); preferences.mutesSessions = sessions.slice(); preferences.mutesAllowed = allowed.slice();
    this.preferences = new Map(this.preferences).set(owner.scope(), preferences);
    if (owner === this.client) { this.mutesSpaces = spaces.slice(); this.mutesSessions = sessions.slice(); this.mutesAllowed = allowed.slice(); } this.rebuild();
  }

  private async refreshAfterMutation(snapshot: AgentOwnerSnapshot): Promise<void> {
    try { await snapshot.client.refresh(); } catch (_) { /* the acknowledged mutation stays authoritative; a later list event refreshes rows */ }
  }

  private patchState(row: ResourceRow, state: string): void {
    this.patchStates([row], state);
  }

  private patchStates(rows: ResourceRow[], state: string): void {
    const keys = new Set<string>(rows.map((row: ResourceRow) => rowKey(row)));
    const patch = (items: ResourceRow[]): ResourceRow[] => items.map((item: ResourceRow) => {
      if (!keys.has(rowKey(item))) return item;
      const next = cloneRow(item), data = next.data();
      if (state === 'idle') {
        const work = Wire.object(data, 'work');
        if (Wire.bool(work, 'just_finished')) { work['just_finished'] = false; data['work'] = work; }
      }
      data['state'] = state; next.raw = JSON.stringify(data); next.state = state; return next;
    });
    this.latestSourceRows = patch(this.sourceRows); this.historyRows = patch(this.historyRows); this.rebuild();
  }

  private async loadHistory(client: ClientModel, revision: number, refreezeAfterLoad: boolean = false): Promise<void> {
    if (!this.history || !this.historySupported() || client.demo) { if (refreezeAfterLoad) this.refreezeAfterFilter(); return; }
    const request = ++this.historyRequest, machine = this.machineFilter;
    const owners = new Set<ClientModel>(this.shownOwners(client));
    this.loading = true; this.error = ''; const rows: ResourceRow[] = [];
    try {
      for (const owner of owners) {
        if (!this.loadCurrent(client, revision) || request !== this.historyRequest || machine !== this.machineFilter) return;
        if (!owner.features.includes('agent_session_archives')) continue;
        const snapshot = this.capture(owner); if (!snapshot) continue;
        const filters = owner.profile.manager ? ['manager=' + segment(owner.profile.manager)] : [];
        const machineId = this.archiveMachineFor(machine, owner, client);
        if (machineId === undefined) continue;
        if (machineId) filters.unshift('machine_id=' + segment(machineId));
        const path = '/api/v1/sessions/archived' + (filters.length ? '?' + filters.join('&') : '');
        try {
          const data = await snapshot.repo.call(path);
          if (!this.loadCurrent(client, revision) || !this.ownerActive(snapshot) || request !== this.historyRequest || machine !== this.machineFilter) return;
          for (const item of Wire.rows(data, 'sessions')) {
            const row = ResourceRow.from(item, 'session'); if (!row.manager) row.manager = owner.profile.manager;
            row.group = owner.profile.id || ''; if (row.manager === owner.profile.manager && !Wire.bool(row.data(), 'assistant')) rows.push(row);
          }
        } catch (failure) {
          if (this.ownerActive(snapshot) && request === this.historyRequest && machine === this.machineFilter) this.fail(failure as Error);
        }
      }
      if (!this.loadCurrent(client, revision) || request !== this.historyRequest || machine !== this.machineFilter) return;
      if ((refreezeAfterLoad || !this.historyLoaded) && this.readingDetail) { this.freezeSuppressed = false; this.frozenGroups = undefined; }
      this.historyRows = rows; this.historyLoaded = true; this.latestSourceRows = rows.slice(); this.rebuild();
    } finally { if (this.loadCurrent(client, revision) && request === this.historyRequest) { this.loading = false; if (refreezeAfterLoad && this.freezeSuppressed) this.refreezeAfterFilter(); } }
  }

  private archiveMachineFor(filter: string, owner: ClientModel, root: ClientModel): string | undefined {
    if (!filter) return '';
    if (!filter.startsWith('[') && !filter.includes('|')) return filter;
    let parts: string[] = [];
    if (filter.startsWith('[')) {
      try { parts = JSON.parse(filter) as string[]; } catch (_) { return undefined; }
      if (!Array.isArray(parts) || parts.length !== 3 || !parts.every((part: string) => typeof part === 'string')) return undefined;
    } else {
      parts = filter.split('|');
      if (parts.length === 2) {
        const group = root.directoryOwners().some((candidate: ClientModel) => candidate.profile.id === parts[0]);
        parts = group ? [parts[0], '', parts[1]] : ['', parts[0], parts[1]];
      }
    }
    if (parts.length !== 3 || !parts[2] || parts[0] && parts[0] !== owner.profile.id || parts[1] && parts[1] !== owner.profile.manager) return undefined;
    return parts[2];
  }

  private rowsForClient(client: ClientModel | undefined): ResourceRow[] {
    if (!client) return [];
    return (this.projectRows || (client.directoryReady ? client.directorySessions : client.sessions)).filter((row: ResourceRow) => row.kind === 'session' && this.includesHub(row.group) &&
      !Wire.bool(row.data(), 'archived') && !Wire.bool(row.data(), 'assistant'));
  }

  private syncSelected(client: ClientModel): void {
    const selected = this.externalSelection || client.selected;
    if (!selected.kind) { if (this.externalSelection) this.selectedKey = ''; return; }
    const next = selected.kind === 'session' && this.sourceRows.some((row: ResourceRow) => rowKey(row) === rowKey(selected)) ? rowKey(selected) : '';
    if (next === this.selectedKey) return;
    this.selectedKey = next; this.saveViewLater();
  }

  private releaseFreeze(): void {
    if (!this.readingDetail) return;
    this.freezeSuppressed = true; this.frozenGroups = undefined;
  }

  quickCountsKnown(): boolean { return this.sourceReady() && !this.loading && Object.keys(this.filterCounts).length > 0; }

  private sourceReady(): boolean { return !this.openingView && (this.history ? this.historyLoaded : !!this.client && (this.client.directoryReady || this.client.sessionsReady) && !this.client.directoryLoading); }

  private refreezeAfterFilter(): void {
    if (!this.readingDetail) return;
    this.freezeSuppressed = false;
    if (this.sourceReady() || this.groups.length > 0) this.frozenGroups = this.copyGroups(this.groups);
  }

  updateSupervisorCards(client: ClientModel, queue?: AgentDecisionQueue): void {
    if (!this.isBoundTo(client)) return;
    this.supervisorQueue = queue;
    if (this.hidSupervisorRows || queue?.hasSupervisorCards) this.rebuild();
  }

  private applySupervisorHolds(matches:string[]):void {
    this.hidSupervisorRows = false;
    if (this.history || !this.client || !this.supervisorQueue?.hasSupervisorCards) return;
    const held = this.supervisorQueue.heldOnlyRows(this.client,this.sourceRows);
    if (!held.size) return;
    this.hidSupervisorRows = true;
    const ids = new Set(Array.from(held).map((row:ResourceRow) => coreRowId(row)));
    const visible = (id:string):boolean => !ids.has(id);
    const quick = this.quickFilter === 'needsYou';
    const affected = matches.filter((id:string) => ids.has(id)).length;
    this.filterCounts['needsYou'] = Math.max(0,Wire.number(this.filterCounts,'needsYou') - affected);
    if (quick) this.unpinnedRows = this.unpinnedRows.filter((row:ResourceRow) => visible(coreRowId(row)));
    this.groups = this.groups.filter((group:AgentWorkspaceGroup):boolean => {
      if (!quick && group.id !== 'state/needs') return true;
      group.memberIds = group.memberIds.filter(visible);
      group.totalRowCount = group.memberIds.length;
      group.rows = group.rows.filter((row:ResourceRow) => visible(coreRowId(row)));
      return group.totalRowCount > 0;
    });
  }

  private effectiveKept(): string[] {
    const merged: string[] = [];
    const seen = new Set<string>();
    const add = (id: string): void => {
      if (id && !seen.has(id)) { seen.add(id); merged.push(id); }
    };
    if (this.frozenGroups) {
      this.frozenGroups.forEach((group) => group.memberIds.forEach(add));
    }
    if (this.quickFilter !== 'all') {
      this.filterKept.forEach(add);
    }
    return merged;
  }

  private rebuild(): void {
    let groups: CoreAgentListGroup[];
    let unpinned: string[];
    let matches: string[];
    let unreadIds: string[];
    try {
      const input = this.makeListInput();
      const projection = this.adapter.project(input);
      matches = projection.queryMatches || [];
      groups = phoneAgentGroups(input,projection);
      unpinned = projection.unpinned || [];
      unreadIds = projection.unread_shown || [];
      this.filterCounts = projection.filter_counts || {};
    } catch (_) {
      // Preserve both rows and action authority from the same successful snapshot.
      this.error = 'failed';
      return;
    }
    this.sourceRows = this.latestSourceRows;
    if (this.client) this.syncSelected(this.client);
    const byId = new Map<string, ResourceRow>();
    this.sourceRows.forEach((row) => byId.set(coreRowId(row), row));
    this.unreadShownRows = unreadIds.flatMap((id: string): ResourceRow[] => {
      const row = byId.get(id);
      return row ? [row] : [];
    });
    if (this.quickFilter !== 'all' && !this.readingDetail) {
      const nextKept = new Set<string>(this.filterKept.filter((id: string) => byId.has(id)));
      groups.forEach((group) => group.rows.forEach((id: string) => { if (byId.has(id)) nextKept.add(id); }));
      this.filterKept = Array.from(nextKept);
    } else if (this.quickFilter === 'all' && this.filterKept.length) {
      this.filterKept = [];
    }
    if (this.readingDetail && !this.freezeSuppressed && this.frozenGroups) {
      const frozenIds = new Set<string>(this.frozenGroups.flatMap((group) => group.memberIds));
      const rowsById = new Map<string, ResourceRow>();
      groups.forEach((group) => group.visible_rows.forEach((id) => {
        const row = byId.get(id);
        if (frozenIds.has(id) && row) rowsById.set(id, row);
      }));
      this.groups = this.frozenGroups.map((group) => {
        const copy = this.copyGroup(group);
        const projected = groups.find((entry) => entry.id === group.id);
        if (projected) copy.folded = projected.folded;
        copy.rows = [];
        if (group.presentation !== 'app-parent' && !copy.folded) {
          const previous = group.presentation ? group.rows.map((row:ResourceRow)=>coreRowId(row)) : group.memberIds;
          previous.forEach((id: string) => { const row = rowsById.get(id); if (row) copy.rows.push(row); });
        }
        return copy;
      }).filter((group) => group.rows.length > 0 || group.totalRowCount > 0);
      this.applySupervisorHolds(matches);
      return;
    }
    this.unpinnedRows = unpinned.flatMap((id:string):ResourceRow[]=>{ const row=byId.get(id); return row ? [row] : []; });
    this.groups = groups.map((group) => {
      const native = new AgentWorkspaceGroup(group.id, '', group.title, '', '', '', '', '');
      native.folded = group.folded; native.parent = group.parent || ''; native.stateCounts = group.state_counts || {};      native.spoken = group.spoken || ''; native.totalRowCount = group.rows.length;
      native.memberIds = group.rows.slice();
      native.presentation = group.presentation || ''; native.titleKey = group.titleKey || ''; native.capped = !!group.capped; native.subtitle=group.subtitle || '';
      native.firstProject=!!group.firstProject; native.lastProject=!!group.lastProject;
      native.showMachine = group.id.startsWith('machine/');
      native.rows = [];
      group.visible_rows.forEach((id: string) => { const row = byId.get(id); if (row) native.rows.push(row); });
      return native;
    });
    if (this.readingDetail && this.sourceReady() && !this.freezeSuppressed && !this.frozenGroups) this.frozenGroups = this.copyGroups(this.groups);
    this.applySupervisorHolds(matches);
  }

  // Project search renders the same Agent groups, with its own query, machine and quick filter.
  // The standalone Agents view persists its own filters independently.
  setProjectSearch(root:ClientModel, rows:ResourceRow[], query:string, quick:string, machine:string, grouping:string,
    decisions:AgentDecisionQueue): void {
    if (this.standaloneView || !this.isBoundTo(root)) return;
    this.projectRows=rows; this.search=query; this.quickFilter=quick; this.supervisorQueue=decisions;
    this.machineFilter=this.machineForShownHub(machine);
    this.preferredGrouping=AGENT_GROUPINGS.includes(grouping) ? grouping : this.defaultGrouping;
    this.groupingMode=this.resolvedGrouping();
    this.latestSourceRows=this.rowsForClient(root);
    this.rebuild();
  }

  // Projects shares the Agents search and pane-source projection, with its own machine, query and reading position.
  repositoryQuery(root:ClientModel, sessions:ResourceRow[], query:string, quick:string, machine:string):CoreAgentListProjection {
    const bound = this.isBoundTo(root);
    const machines = root.directoryMachines.map((row:ResourceRow):Record<string,Object> =>
      ({ 'id':row.id, 'name':row.title, 'group':row.group, 'manager':row.manager }));
    return this.adapter.project({
      rows:this.listRows(sessions), includeSourceFilters:true, grouping:'repo', quick, query, machineFilter:machine,
      kept:[], history:false, reading:false, machines, coreSettings:{ 'sort':'state', 'include_filter_counts':true },
      preferencesFor:(row:AgentListRow):AgentListPreferences => bound ? this.preferencesFor(row.source as ResourceRow) : new AgentPreferences(),
      machineIdentity:(row:AgentListRow):string => rowMachine(row.source as ResourceRow),
      machineName:(row:AgentListRow):string => root.machineName(rowMachine(row.source as ResourceRow))
    });
  }

  private listRows(source:ResourceRow[]):AgentListRow[] {
    return source.map((row:ResourceRow):AgentListRow => {
      const raw = row.data();
      raw['machine_id'] = rowMachine(row); raw['session_id'] = rowSession(row); raw['title'] = row.title;
      raw['cwd'] = row.path; raw['state'] = row.state; raw['manager'] = row.manager; raw['app_hub_group'] = row.group;
      return {id:coreRowId(row), raw, source:row};
    });
  }

  private makeListInput(): AgentListInput {
    const rows = this.listRows(this.latestSourceRows);
    return {rows, includeSourceFilters:true, history: this.history, grouping: this.groupingMode, quick: this.quickFilter,
      machineFilter: this.machineFilter, query: this.search, reading: this.readingDetail,
      kept: this.effectiveKept(),
      machines: (this.client ? this.shownOwners(this.client) : []).flatMap((owner: ClientModel): Record<string, Object>[] => {
        const current: Record<string, Object>[] = owner.machines.map((row: ResourceRow): Record<string, Object> =>
          ({ 'id': row.id, 'name': row.title, 'group': owner.profile.id, 'manager': row.manager || owner.profile.manager }));
        this.historyRows.filter((session) => (session.group || owner.profile.id) === owner.profile.id && session.manager === owner.profile.manager)
          .forEach((session) => {
            if (!current.some((machine) => machine['id'] === rowMachine(session))) {
              current.push({ 'id': rowMachine(session), 'name': this.ownerFor(session)?.machineName(rowMachine(session)) || rowMachine(session),
                'group': owner.profile.id, 'manager': session.manager });
            }
          });
        return current;
      }),
      coreSettings: { 'sort': 'state', 'folded': this.quickFilter !== 'all' && this.filterKept.length > 0 && !this.folded.includes('state/idle') && !this.folded.includes('!state/idle') ? this.folded.concat(['!state/idle']) : this.folded, 'expanded_idle': this.expandedIdle, 'expanded_quiet': this.expandedQuiet,
        'compact': this.compactDisplay, 'history': this.history, 'include_filter_counts': true, 'now': new Date().toISOString(), 'held': this.heldSession },
      preferencesFor: (row: AgentListRow): AgentListPreferences => this.preferencesFor(row.source as ResourceRow),
      machineIdentity: (row: AgentListRow): string => this.machineIdentity(row.source as ResourceRow),
      machineName: (row: AgentListRow): string => {
        const source = row.source as ResourceRow;
        const machine = this.ownerFor(source)?.machines.find((item: ResourceRow) => item.id === rowMachine(source) && item.manager === source.manager);
        return machine?.title || rowMachine(source);
      }};
  }

  private copyGroup(group: AgentWorkspaceGroup): AgentWorkspaceGroup {
    const copy = new AgentWorkspaceGroup(group.id, group.machineId, group.machineName, group.repositoryPath, group.repositoryName,
      group.worktreePath, group.worktreeName, group.branch, group.rows.slice());
    copy.showMachine = group.showMachine; copy.showRepository = group.showRepository; copy.showWorktree = group.showWorktree;
    copy.presentation = group.presentation; copy.titleKey = group.titleKey; copy.capped = group.capped; copy.subtitle=group.subtitle;
    copy.firstProject=group.firstProject; copy.lastProject=group.lastProject;
    copy.folded = group.folded; copy.parent = group.parent; copy.stateCounts = group.stateCounts;
    copy.spoken = group.spoken; copy.totalRowCount = group.totalRowCount; copy.memberIds = group.memberIds.slice(); return copy;
  }

  private copyGroups(groups: AgentWorkspaceGroup[]): AgentWorkspaceGroup[] { return groups.map((group: AgentWorkspaceGroup) => this.copyGroup(group)); }

  private fail(failure: Error): void {
    const detail = failure as Error as Object as Record<string, Object>;
    this.error = Wire.bool(detail, 'resultPending') ? 'pending' : Wire.bool(detail, 'resultUnknown') ? 'result_unknown' :
      Wire.text(detail, 'message') === 'pending_action' ? 'pending_action' : Wire.text(detail, 'message') === 'invalid_preferences' ? 'failed' : 'failed';
  }

  private saveViewLater(): void {
    if (!this.standaloneView) return;
    if (this.persistTimer !== -1) clearTimeout(this.persistTimer);
    const client = this.client, revision = this.revision, viewScope = this.viewScope;
    this.persistTimer = setTimeout(() => {
      this.persistTimer = -1;
      if (!client || !this.loadCurrent(client, revision) || this.viewScope !== viewScope) return;
      const values: Object[] = [this.quickFilter, this.preferredGrouping, this.machineFilter, this.search, this.history, this.selectedKey,
        this.folded, this.expandedIdle, this.expandedQuiet];
      client.saveView('agent-workspace', JSON.stringify(Wire.payload(['quick', 'grouping', 'machine', 'search', 'history', 'selected',
        'folded', 'expanded_idle', 'expanded_quiet'], values)))
        .catch(() => undefined);
    }, 180);
  }
}

function togglePresent(values: string[], key: string): string[] { return values.includes(key) ? values : values.concat([key]); }
