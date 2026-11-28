import type { ClientModel } from './ClientModel';
import { ResourceRow, Wire } from './Wire';
import { SessionSpaceTab } from './SessionSpace';
import { agentRepositoryPlace, AgentRepositoryPlace } from './AgentRepositoryPlace';
import { herdrTabName } from '../core/SessionNaming';

function leaf(path: string): string {
  const parts = path.replace(/\\/g, '/').split('/').filter((part: string) => !!part);
  return parts[parts.length - 1] || path;
}
function cleanPath(path: string): string { return path.replace(/\/+$/, '') || path; }

// Workspaces follow Finder's natural filename order: app2 appears before app10.
const titleCollator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
function titleOrder(a:string,b:string):number { return titleCollator.compare(a,b); }

function parsedTime(value: string): number {
  const time = Date.parse(value);
  return Number.isFinite(time) ? time : 0;
}

/** The information shown on the iPhone's repository Agent rows, prepared once for sorting and search. */
export class ProjectAgent {
  row: ResourceRow;
  state: string;
  stateLabel: string;
  rank: number;
  updated: number;
  activity: number;
  runtime: string;
  markRuntime: string;
  model: string;
  preview: string;
  tab: string;
  spaceId: string;
  spaceName:string;
  spaceKind: string;
  archived: boolean;
  pending: number;
  todoTitle: string;
  tabKey: string = '';
  tabTitle: string = '';
  readonly herdrTabName: string = '';
  pane?: SessionSpaceTab;
  private searchText: string;
  private searchFolded: boolean = false;

  constructor(row: ResourceRow, data: Record<string, Object>) {
    this.row = row;
    const state = row.state || Wire.text(data, 'status');
    this.state = state === 'blocked' ? 'needs_you' : state === 'done' ? 'unread' : state;
    const work = Wire.object(data, 'work'), children = Wire.object(data, 'children');
    if (this.state === 'needs_you') { this.rank = 0; this.stateLabel = 'needs_you'; }
    else if (this.state === 'unread') { this.rank = 1; this.stateLabel = 'agent_group_unread'; }
    else if (this.state === 'idle' && Wire.bool(data,'awaits_reply')) { this.rank = 2; this.stateLabel = 'agent_group_awaiting'; }
    else if (this.state === 'working') {
      const background = Wire.bool(data,'background_waiting') && !Wire.number(children,'working') && !Wire.number(children,'needs_you');
      this.rank = background ? 4 : 3; this.stateLabel = background ? 'agent_group_background' : 'working';
    } else if (this.state === 'idle' && Wire.bool(work,'just_finished')) { this.rank = 5; this.stateLabel = 'agent_group_just_finished'; }
    else if (['idle','unknown'].includes(this.state) && (Wire.has(data,'waiting') ? Wire.bool(data,'waiting') :
      !!Object.keys(Wire.object(data,'loop')).length || Wire.rows(data,'background').length > 0 || Wire.bool(work,'hub_waits') ||
      Wire.rows(data,'subagents').some((item:Record<string,Object>) => Wire.bool(item,'running')))) {
      this.rank = 6; this.stateLabel = 'agent_group_waiting';
    } else {
      this.rank = 8; this.stateLabel = this.state === 'offline' ? 'offline' : this.state === 'idle' ? 'agent_group_idle' : 'unknown';
    }
    this.updated = parsedTime(Wire.text(data, 'updated_at'));
    this.activity = this.state === 'working' ? parsedTime(Wire.text(data,'state_since')) || this.updated : this.updated;
    const runtime = Wire.text(data,'agent_label') || Wire.text(data,'agent') || Wire.text(data,'agent_type') || Wire.text(data,'launcher');
    this.runtime = runtime === 'claude' ? 'Claude' : runtime === 'codex' ? 'Codex' : runtime;
    const sourceKind = Wire.text(data,'source_kind');
    this.markRuntime = sourceKind === 'codex_app_server' || sourceKind === 'codex_history' ? 'codex' : runtime.toLowerCase() || 'claude';
    this.model = Wire.text(data,'model');
    this.preview = (Wire.text(data,'last_text') || row.detail).trim();
    this.tab = Wire.text(data,'slot_name') || Wire.text(data,'space_name');
    this.spaceName = Wire.text(data,'space_name');
    this.spaceId = Wire.text(data,'space_id'); this.spaceKind = Wire.text(data,'space_kind');
    this.archived = Wire.bool(data,'archived'); this.pending = Wire.number(data,'pending_interactions');
    this.herdrTabName = herdrTabName(data);
    const todo = Wire.object(data,'todo');
    this.todoTitle = Wire.text(todo,'id') ? Wire.text(todo,'title') : '';
    const space = this.spaceId, slot = Wire.text(data,'slot_id');
    if (space && slot) {
      this.pane = new SessionSpaceTab(row, data);
      this.tabKey = JSON.stringify([row.group, row.manager, row.machine, Wire.text(data,'space_kind'), space, slot]);
      this.tabTitle = (Wire.text(data,'slot_name') || typeof data['slot_index'] === 'number') ? this.pane.label : slot;
    }
    this.searchText = [row.title,this.model,this.runtime,this.tab,this.tabTitle,row.path].join(' ');
  }

  get search(): string {
    if (!this.searchFolded) {
      this.searchText = this.searchText.toLocaleLowerCase();
      this.searchFolded = true;
    }
    return this.searchText;
  }
  get active(): boolean { return this.rank < 8; }
}

function sessionPlace(row: ResourceRow, data: Record<string, Object>): AgentRepositoryPlace {
  return agentRepositoryPlace(row.path, Wire.text(data, 'worktree'), Wire.text(data, 'repo_root'));
}

export class ProjectWorktree {
  key: string = '';
  group: string = '';
  manager: string = '';
  machine: string = '';
  machineTitle: string = '';
  source: string = '';
  sourceId: string = '';
  title: string = '';
  cwd: string = '';
  branch: string = '';
  reportedBranch?: string;
  parent: string = '';
  parentReported: boolean = false;
  parentKey: string = '';
  breadcrumb: string = '';
  children: ProjectWorktree[] = [];
  projectId: string = '';
  projectTitle: string = '';
  main: boolean = false;
  folder: boolean = false;
  workspaceKind: string = '';
  head: string = '';
  index: number = Number.MAX_SAFE_INTEGER;
  pinnedAt: number = 0;
  latestPin: number = 0;
  activeCount: number = 0;
  agents: ProjectAgent[] = [];
  online: boolean = true;
  startAllowed: boolean = false;
  sessions: ResourceRow[] = [];

  permits(action: string): boolean {
    if (!this.sourceId || !this.online || !this.startAllowed) return false;
    if (action === 'rename') return ['herdr', 'herdr-core', 'orca', 'paseo'].includes(this.source);
    if (action === 'create') return ['herdr', 'herdr-core', 'orca'].includes(this.source);
    return action === 'clean' && !this.main && ['herdr', 'orca', 'paseo'].includes(this.source);
  }
  // New Agent's general picker lists checkout engines. A terminal host is joined from its own workspace menu.
  get pickerWorkspace(): boolean { return this.source === 'paseo' || this.source === 'orca'; }
  get boundStart(): boolean { return !!this.sourceId && (this.pickerWorkspace || this.source === 'herdr' || this.source === 'herdr-core'); }
  receiptCurrent?: () => boolean;
  matchesCreated(child: ProjectWorktree): boolean {
    // Some terminal hosts list only the machine, workspace ID and checkout directory.
    // Their create receipt supplies the optional fields until the host actually reports them.
    if (this.key !== child.key || this.cwd !== child.cwd) return false;
    if (this.reportedBranch !== undefined && this.reportedBranch !== child.branch) return false;
    return (!this.branch || this.branch === child.branch) &&
      (this.parent === child.parent || (!this.parentReported && !this.parent));
  }
  get canStart(): boolean { return this.online && this.startAllowed && !!this.cwd && (this.boundStart || this.source === 'session-directory' || !this.source); }
}

export class PersonaProject {
  key: string = '';
  title: string = '';
  machine: string = '';
  roots: ProjectWorktree[] = [];
  trees: ProjectWorktree[] = [];
  sessionCount: number = 0;
  activeCount: number = 0;
  pinnedAt: number = 0;
  hostKey: string = '';
  hostOrder: number = Number.MAX_SAFE_INTEGER;

  get flat(): boolean {
    const root = this.roots[0];
    return this.roots.length === 1 && root.main && !root.children.length && !root.branch &&
      (!root.source || root.source === 'session-directory') && !!root.agents.length;
  }
}

function startAllowed(machine: ResourceRow | undefined): boolean {
  if (!machine || machine.state === 'offline') return false;
  const data = machine.data();
  return Wire.bool(data, 'online') && !Wire.bool(data, 'pending') && !Wire.bool(data, 'pending_confirm') && Wire.text(data, 'trust') !== 'pending';
}

class WorkspaceIndex {
  private byId: Map<string, ProjectWorktree[]> = new Map();
  private byPath: Map<string, ProjectWorktree[]> = new Map();

  constructor(trees: ProjectWorktree[]) {
    for (const tree of trees) {
      this.add(this.byId,tree,tree.sourceId);
      if (tree.cwd) this.add(this.byPath,tree,cleanPath(tree.cwd));
    }
  }
  private add(index: Map<string, ProjectWorktree[]>, tree: ProjectWorktree, value: string): void {
    const key = JSON.stringify([tree.group,tree.manager,tree.machine,value]), bucket = index.get(key);
    if (bucket) bucket.push(tree); else index.set(key,[tree]);
  }
  match(row: ResourceRow,data: Record<string,Object>,place: AgentRepositoryPlace): ProjectWorktree | undefined {
    const kind = Wire.text(data,'space_kind'), bound = Wire.text(data,'workspace_id'), space = Wire.text(data,'space_id');
    const key = (id:string):string => JSON.stringify([row.group,row.manager,row.machine,id]);
    if (bound) {
      const candidates = this.byId.get(key(bound)) || [];
      return kind ? candidates.find((t:ProjectWorktree) => t.source === kind) : candidates.length === 1 ? candidates[0] : undefined;
    }
    if (kind && space) {
      const exact = this.byId.get(key(space))?.find((t:ProjectWorktree) => t.source === kind);
      if (exact) return exact;
    }
    const cwd = cleanPath(row.path);
    if (!cwd) return undefined;
    const matches = this.byPath.get(key(cwd)) || [];
    const directory = matches.find((t:ProjectWorktree) => t.source === 'session-directory');
    if (directory) return directory;
    if (['herdr','herdr-core','botmux','paseo','tern','cindy'].includes(kind)) return undefined;
    if (matches.length) return matches.length === 1 ? matches[0] : undefined;
    const checkout = this.byPath.get(key(place.checkout)) || [];
    return checkout.length === 1 ? checkout[0] : undefined;
  }
}

export function projectDirectory(catalog: ResourceRow[], sessions: ResourceRow[], machines: ResourceRow[]): PersonaProject[] {
  const trees = new Map<string, ProjectWorktree>();
  const identifiers = new Intl.Collator();
  const keyOrder = (a: string, b: string): number => a === b ? 0 : identifiers.compare(a, b);
  const computers = new Map<string,ResourceRow>(), hostOrder = new Map<string,number>();
  for (const row of machines) {
    const key = JSON.stringify([row.group,row.manager,row.id]);
    if (!computers.has(key)) { computers.set(key,row); hostOrder.set(key,hostOrder.size); }
  }
  const computer = (group:string,manager:string,id:string):ResourceRow | undefined => computers.get(JSON.stringify([group,manager,id]));
  for (const row of catalog) {
    const data = row.data(), tree = new ProjectWorktree();
    tree.machine = row.machine; tree.group = row.group; tree.manager = row.manager;
    tree.source = Wire.text(data, 'source'); tree.sourceId = Wire.text(data, 'source_id');
    if (!tree.machine || !tree.source || !tree.sourceId) continue;
    tree.key = JSON.stringify([tree.group, tree.manager, tree.machine, tree.source, tree.sourceId]);
    const machine = computer(tree.group, tree.manager, tree.machine);
    tree.machineTitle = machine?.title || tree.machine; tree.startAllowed = startAllowed(machine); tree.cwd = row.path;
    tree.branch = Wire.text(data, 'branch');
    if (Wire.has(data, 'branch')) tree.reportedBranch = tree.branch;
    tree.title = tree.source === 'session-directory' && cleanPath(row.title) === cleanPath(tree.cwd) ? leaf(tree.cwd) :
      row.title || tree.branch || leaf(tree.cwd);
    tree.parent = Wire.text(data, 'parent_source_id'); tree.parentReported = Wire.has(data, 'parent_source_id');
    const catalogWorkspace = tree.source === 'orca' || tree.source === 'paseo';
    const address = tree.sourceId.split('::');
    const hostProject = Wire.text(data, 'project_id') || (catalogWorkspace && address.length > 1 ? address[0] : '');
    tree.projectId = hostProject ? JSON.stringify([tree.source, hostProject]) : Wire.text(data, 'project_path');
    tree.projectTitle = Wire.text(data, 'project_name') ||
      (catalogWorkspace && !hostProject ? tree.title : leaf(Wire.text(data, 'project_path')));
    tree.main = Wire.bool(data, 'is_main'); tree.workspaceKind = Wire.text(data, 'workspace_kind');
    tree.head = Wire.text(data,'head'); tree.folder = ['folder','folder-workspace'].includes(tree.workspaceKind);
    tree.index = Wire.number(data,'index',Number.MAX_SAFE_INTEGER); tree.pinnedAt = parsedTime(Wire.text(data,'pinned_at'));
    tree.online = row.state !== 'offline' && Wire.bool(data, 'online', true) && Wire.bool(data, 'available', true) &&
      (!machine || machine.state !== 'offline' && Wire.bool(machine.data(), 'online', true));
    trees.set(tree.key, tree);
  }
  const index = new WorkspaceIndex(Array.from(trees.values()));
  // A checkout path can be claimed by different repository roots on the same computer.
  // Resolve all claims before assigning route keys so a poll arriving in a different order cannot redirect a row.
  const loose = new Map<string,Map<string,ProjectWorktree>>();
  const seen = new Set<string>();
  for (const row of sessions) {
    const data = row.data(), identity = row.key();
    if (!row.machine || Wire.bool(data,'assistant') || seen.has(identity)) continue;
    seen.add(identity);
    const place = sessionPlace(row,data);
    let tree = index.match(row,data,place);
    if (!tree) {
      const key = JSON.stringify([row.group,row.manager,row.machine,'session',place.checkout || row.id]);
      let claims = loose.get(key);
      if (!claims) { claims = new Map<string,ProjectWorktree>(); loose.set(key,claims); }
      tree = claims.get(place.repo);
      if (!tree) {
        tree = new ProjectWorktree(); tree.group = row.group; tree.manager = row.manager; tree.machine = row.machine;
        const machine = computer(row.group,row.manager,row.machine);
        tree.machineTitle = machine?.title || row.machine; tree.startAllowed = startAllowed(machine);
        tree.cwd = place.checkout; tree.projectId = place.repo; tree.projectTitle = leaf(place.repo); tree.main = place.main;
        tree.title = leaf(place.checkout) || row.title;
        tree.online = !!machine && machine.state !== 'offline' && Wire.bool(machine.data(),'online',true); claims.set(place.repo,tree);
      }
    }
    const agent = new ProjectAgent(row,data);
    tree.agents.push(agent);
    if (!tree.branch) tree.branch = Wire.text(data, 'branch');
    if (!tree.projectId && place.repo) { tree.projectId = place.repo; if (!tree.projectTitle) tree.projectTitle = leaf(place.repo); }
    if (tree.source === 'session-directory' && place.main && tree.cwd === place.checkout) tree.main = true;
  }
  for (const key of loose.keys()) {
    const claims = loose.get(key)!;
    const roots = Array.from(claims.keys()).sort();
    const primary = roots.find((repo:string) => claims.get(repo)!.main) || roots[0];
    for (const repo of roots) {
      const tree = claims.get(repo)!;
      tree.key = repo === primary ? key : JSON.stringify([tree.group,tree.manager,tree.machine,'session',tree.cwd,repo]);
      let hostTitle = '', conflict = false;
      if (!tree.main) for (const agent of tree.agents) {
        const data = agent.row.data(), name = Wire.text(data,'slot_name').trim();
        if (!name || Wire.text(data,'slot_id')) continue;
        if (hostTitle && name !== hostTitle) { conflict = true; break; }
        hostTitle = name;
      }
      tree.title = !conflict && hostTitle ? hostTitle : tree.branch || tree.title;
      trees.set(tree.key,tree);
    }
  }
  const projects = new Map<string, PersonaProject>();
  for (const tree of trees.values()) {
    if (tree.parent) {
      const key = JSON.stringify([tree.group, tree.manager, tree.machine, tree.source, tree.parent]);
      if (key !== tree.key && trees.has(key)) tree.parentKey = key;
    }
  }
  // A malformed source can return a cycle. Break its smallest key so every valid row remains reachable.
  for (const tree of trees.values()) {
    const chain: string[] = [];
    let cursor: ProjectWorktree | undefined = tree;
    while (cursor && cursor.parentKey) {
      const seen = chain.indexOf(cursor.key);
      if (seen >= 0) { const cut = trees.get(chain.slice(seen).sort()[0]); if (cut) cut.parentKey = ''; break; }
      chain.push(cursor.key); cursor = trees.get(cursor.parentKey);
    }
  }
  for (const tree of trees.values()) {
    let root = tree;
    const names: string[] = [tree.title];
    const parent = trees.get(tree.parentKey);
    if (parent) parent.children.push(tree);
    while (root.parentKey) {
      const next = trees.get(root.parentKey); if (!next) break;
      root = next; names.unshift(root.title);
    }
    tree.breadcrumb = names.join(' › ');
    const projectKey = JSON.stringify([root.group, root.manager, root.machine, root.projectId || cleanPath(root.cwd) || root.key]);
    let project = projects.get(projectKey);
    if (!project) {
      project = new PersonaProject(); project.key = projectKey; project.title = root.projectTitle || leaf(root.cwd) || root.title;
      project.machine = root.machineTitle; project.hostKey = JSON.stringify([root.group,root.manager,root.machine]);
      project.hostOrder = hostOrder.get(project.hostKey) ?? Number.MAX_SAFE_INTEGER;
      projects.set(projectKey, project);
    }
    if (!parent) project.roots.push(tree);
    tree.agents.sort((a:ProjectAgent,b:ProjectAgent) => a.rank - b.rank || b.activity - a.activity || keyOrder(a.row.id,b.row.id));
    tree.sessions = tree.agents.map((a:ProjectAgent) => a.row);
    tree.activeCount = tree.agents.filter((a:ProjectAgent) => a.active).length; tree.latestPin = tree.pinnedAt;
    project.trees.push(tree); project.sessionCount += tree.agents.length;
    project.activeCount += tree.activeCount;
    project.pinnedAt = Math.max(project.pinnedAt,tree.pinnedAt);
  }
  const order = (a:ProjectWorktree,b:ProjectWorktree):number => b.latestPin - a.latestPin || a.index - b.index ||
    Number(b.main) - Number(a.main) || titleOrder(a.title,b.title) || keyOrder(a.key,b.key);
  for (const project of projects.values()) {
    // Fold an idle parent only when all its descendants are idle too.
    for (const tree of project.trees) {
      const direct = tree.agents.filter((a:ProjectAgent) => a.active).length;
      let parent = trees.get(tree.parentKey);
      while (parent && (direct || tree.pinnedAt)) {
        parent.activeCount += direct; parent.latestPin = Math.max(parent.latestPin,tree.pinnedAt); parent = trees.get(parent.parentKey);
      }
    }
    project.roots.sort(order);
    for (const tree of project.trees) tree.children.sort(order);
  }
  return Array.from(projects.values()).sort((a:PersonaProject,b:PersonaProject) => a.hostOrder - b.hostOrder || keyOrder(a.hostKey,b.hostKey) ||
    b.pinnedAt - a.pinnedAt || titleOrder(a.title,b.title) || keyOrder(a.key,b.key));
}

/** Prune a fresh projection, preserving every ancestor of an admitted session or a matching registered workspace. */
export function trimAgentProjects(projects: PersonaProject[], includeEmpty: boolean, search: string, createdKey: string = ''): PersonaProject[] {
  const query=search.trim().toLocaleLowerCase();
  return projects.filter((project:PersonaProject) => {
    const trees = new Map<string,ProjectWorktree>(), keep = new Set<string>();
    for (const tree of project.trees) trees.set(tree.key,tree);
    for (const tree of project.trees) {
      const matches = !query || (tree.title + ' ' + tree.cwd).toLocaleLowerCase().includes(query);
      if (!tree.agents.length && !(includeEmpty && (tree.pickerWorkspace || tree.key === createdKey) && matches)) continue;
      let current: ProjectWorktree | undefined = tree;
      while (current && !keep.has(current.key)) { keep.add(current.key); current=trees.get(current.parentKey); }
    }
    project.trees=project.trees.filter((tree:ProjectWorktree) => keep.has(tree.key));
    project.roots=project.roots.filter((tree:ProjectWorktree) => keep.has(tree.key));
    for (const tree of project.trees) tree.children=tree.children.filter((child:ProjectWorktree) => keep.has(child.key));
    return project.trees.length>0;
  });
}

@ObservedV2
export class ProjectBrowserState {
  @Trace query: string = '';
  @Trace searchQuick: string = '';
  @Trace searchActive: boolean = false;
  @Trace searchGrouping: string = '';
  @Trace private searchClosed: string[] = [];
  private searchScope: string = '';
  @Trace machine: string = '';
  @Trace preferences: string[] = [];
  @Trace unlimited: string[] = [];
  @Trace selectedSession: string = '';
  @Trace selectedTree: string = '';
  private anchors: Map<string,string> = new Map();
  edits: number = 0;

  clear(): void {
    this.query = ''; this.searchQuick = ''; this.searchActive = false; this.searchGrouping = ''; this.machine = ''; this.preferences = []; this.unlimited = []; this.selectedTree = ''; this.selectedSession = ''; this.anchors.clear(); this.searchClosed = []; this.searchScope = ''; this.edits++;
  }
  searchOpen(id:string): boolean { return !this.searchClosed.includes(id); }
  toggleSearch(id:string): void {
    this.searchClosed = this.searchOpen(id) ? this.searchClosed.concat([id]) : this.searchClosed.filter((key:string) => key !== id);
  }
  resetSearchFolds(): void { this.searchClosed = []; }
  setSearch(query: string, scope: string): void {
    if (this.query !== query || this.searchScope !== scope) this.resetSearchFolds();
    this.query = query; this.searchScope = scope;
  }
  open(id: string, fallback: boolean): boolean {
    if (this.preferences.includes('+' + id)) return true;
    return this.preferences.includes('-' + id) ? false : fallback;
  }
  toggle(id: string, open: boolean): void {
    this.edits++;
    this.preferences = this.preferences.filter((value:string) => value !== '+' + id && value !== '-' + id)
      .concat([(open ? '-' : '+') + id]);
  }
  remember(route:string, key:string): void { this.anchors.set(route,key); }
  anchor(route:string): string { return this.anchors.get(route) || ''; }
  showAll(key:string): void { if (!this.unlimited.includes(key)) this.unlimited = this.unlimited.concat([key]); }
}

@ObservedV2
export class ProjectCatalog {
  @Trace projects: PersonaProject[] = [];
  // The create reply can arrive before the first directory snapshot containing this particular child.
  @Trace createdSelection: ProjectWorktree | undefined = undefined;
  private createdListed: boolean = false;
  @Trace loading: boolean = false;
  @Trace partial: boolean = false;
  @Trace startError: boolean = false;
  @Trace notice: string = '';
  readonly browser: ProjectBrowserState = new ProjectBrowserState();
  readonly agentBrowser: ProjectBrowserState = new ProjectBrowserState();
  private foldsRestored: boolean = false;
  private foldWrite: Promise<void> = Promise.resolve();
  private createdParents: Map<string, Record<string, Object>[]> = new Map();
  private ownerScope: string = '';
  private sequence: number = 0;
  private projectedFields: string[] = [];
  private snapshots: Map<string, ResourceRow[]> = new Map();
  private savedSessions: Map<string, ResourceRow[]> = new Map();
  private savedComputers: Map<string, ResourceRow[]> = new Map();
  clear(): void { this.sequence++; this.createdSelection = undefined; this.ownerScope = ''; this.projectedFields = []; this.notice = ''; this.browser.clear(); this.agentBrowser.clear(); this.foldsRestored = false; this.createdParents.clear(); this.snapshots.clear(); this.savedSessions.clear(); this.savedComputers.clear(); this.projects = []; this.partial = false; this.startError = false; this.loading = false; }

  /** Rebuild from the Agents projection's actual unpinned rows; empty workspaces come only from the owning catalog. */
  agentDirectory(model: ClientModel, sessions: ResourceRow[], includeEmpty: boolean, query: string,
    admits: (group:string,manager:string,machine:string) => boolean): PersonaProject[] {
    const rows: ResourceRow[] = [], pinned = new Set<string>();
    const known = new Set<string>(model.directoryMachines.map((row:ResourceRow) => JSON.stringify([row.group,row.manager,row.id])));
    if (model.scope() === this.ownerScope && this.projects.length) {
      for (const bucket of this.snapshots.values()) for (const row of bucket) {
        if (!known.has(JSON.stringify([row.group,row.manager,row.machine])) || !admits(row.group,row.manager,row.machine)) continue;
        rows.push(row);
        const data = row.data();
        if (Wire.text(data,'source') === 'paseo' && parsedTime(Wire.text(data,'pinned_at'))) {
          pinned.add(JSON.stringify([row.group,row.manager,row.machine,Wire.text(data,'source_id')]));
        }
      }
    }
    const visible = sessions.filter((row: ResourceRow) => {
      if (row.kind !== 'session' || !admits(row.group,row.manager,row.machine)) return false;
      const data = row.data();
      if (Wire.bool(data,'archived')) return false;
      if (row.state !== 'offline') return true;
      return Wire.text(data,'space_kind') === 'paseo' && pinned.has(JSON.stringify([row.group,row.manager,row.machine,Wire.text(data,'workspace_id')]));
    });
    const projected = projectDirectory(rows,visible,model.directoryMachines);
    const created = this.createdSelection, key = this.agentBrowser.selectedTree;
    return trimAgentProjects(projected,includeEmpty,query,created?.key === key && this.selectedWorktree(key) ? key : '');
  }

  syncRows(model: ClientModel): void {
    if (model.scope() !== this.ownerScope) return;
    const rows: ResourceRow[] = [];
    for (const bucket of this.snapshots.values()) rows.push(...bucket);
    const sessions = model.directorySessions.slice(), machines = model.directoryMachines.slice();
    const live = new Set<string>(model.directoryOwners().filter((owner: ClientModel) => owner.connected && owner.sessionsReady).map((owner: ClientModel) => owner.scope()));
    const known = new Set<string>(sessions.map((row: ResourceRow) => row.key()));
    const computers = new Set<string>(machines.map((row: ResourceRow) => row.key()));
    for (const key of this.savedSessions.keys()) {
      if (live.has(key)) continue;
      for (const row of this.savedSessions.get(key) || []) if (!known.has(row.key())) { sessions.push(row); known.add(row.key()); }
      for (const row of this.savedComputers.get(key) || []) if (!computers.has(row.key())) { machines.push(row); computers.add(row.key()); }
    }
    // Refresh, cache restore, and directory notifications can present the same rows with new object identities.
    // Keep the published tree when the full ordered input still matches, including in-place row and connectivity changes.
    const fields: string[] = [String(rows.length), String(sessions.length), String(machines.length)];
    const lanes: ResourceRow[][] = [rows, sessions, machines];
    for (const lane of lanes) for (const row of lane) {
      fields.push(row.id, row.kind, row.group, row.manager, row.machine, row.session, row.title, row.detail, row.path, row.state, row.raw);
    }
    if (fields.length === this.projectedFields.length && fields.every((field: string, index: number) => field === this.projectedFields[index])) return;
    this.projects = projectDirectory(rows, sessions, machines);
    if (this.createdSelection && this.tree(this.createdSelection.key)) this.createdListed = true;
    this.projectedFields = fields;
  }
  toggleBrowserFold(model:ClientModel,id:string,open:boolean): void {
    if (model.scope() !== this.ownerScope) return;
    this.browser.toggle(id,open);
    const scope = this.ownerScope, epoch = model.connectionGeneration(), revision = this.browser.edits;
    // Coalesce rapid taps and serialize writes so an older preference cannot win after a tab switch.
    this.foldWrite = this.foldWrite.then(async ():Promise<void> => {
      if (model.owns(epoch,scope) && this.browser.edits === revision) {
        await model.saveView('project-tree-folds',JSON.stringify(Wire.payload(['folds'],[this.browser.preferences])));
      }
    }).catch(() => {});
  }

  async saveParent(owner: ClientModel, parent: ProjectWorktree, id: string, path: string, parentId: string): Promise<void> {
    const scope = owner.scope(), epoch = owner.connectionGeneration();
    const list = this.createdParents.get(scope) || Wire.rows(Wire.parse(await owner.loadView('worktree-parents')), 'links');
    if (!owner.owns(epoch, scope)) return;
    const next = list.filter((r: Record<string, Object>) => Wire.text(r, 'id') !== id || Wire.text(r, 'source') !== parent.source || Wire.text(r, 'machine') !== parent.machine);
    if (path && parentId && parentId !== id) next.push(Wire.payload(['machine','source','id','path','parent'], [parent.machine,parent.source,id,path,parentId]));
    this.createdParents.set(scope, next); await owner.saveView('worktree-parents', JSON.stringify(Wire.payload(['links'],[next])));
  }
  private addKnownParent(owner: ClientModel, data: Record<string, Object>): Record<string, Object> {
    if (Wire.has(data, 'parent_source_id')) return data;
    const link = this.createdParents.get(owner.scope())?.find((r: Record<string, Object>) => Wire.text(r, 'id') === Wire.text(data, 'source_id') &&
      Wire.text(r, 'path') === Wire.text(data, 'cwd') && Wire.text(r, 'source') === Wire.text(data, 'source') && Wire.text(r, 'machine') === Wire.text(data, 'machine_id'));
    if (!link) return data;
    const copy = Wire.parse(JSON.stringify(data)); copy['parent_source_id'] = Wire.text(link, 'parent'); return copy;
  }
  private fromHost(owner: ClientModel, data: Record<string, Object>): ResourceRow {
    const row = ResourceRow.from(this.addKnownParent(owner, data), 'worktree');
    row.group = owner.profile.id; row.manager = owner.profile.manager; return row;
  }
  private async restoreCatalog(model: ClientModel, valid: () => boolean): Promise<void> {
    if (!this.foldsRestored) {
      const edits = this.browser.edits;
      try {
        const saved = Wire.parse(await model.loadView('project-tree-folds'));
        if (!valid()) return;
        if (this.browser.edits === edits) this.browser.preferences = Wire.strings(saved,'folds')
          .filter((entry:string) => entry.startsWith('+') || entry.startsWith('-'));
      } catch (_) { /* First launch and legacy caches use the platform defaults. */ }
      if (valid()) this.foldsRestored = true;
    }
    for (const owner of model.directoryOwners()) {
      const key = owner.scope(), epoch = owner.connectionGeneration();
      if (this.snapshots.has(key)) continue;
      try {
        if (!this.createdParents.has(key)) {
          const registered = Wire.parse(await owner.loadView('worktree-parents'));
          if (!valid() || !owner.owns(epoch, key)) return;
          this.createdParents.set(key, Wire.rows(registered, 'links'));
        }
        const saved = Wire.parse(await owner.loadView('projects-workspaces'));
        if (!valid()) return;
        if (!owner.owns(epoch, key)) continue;
        const rows = Wire.rows(saved, 'workspaces').map((data: Record<string, Object>) => {
          const row = this.fromHost(owner, data); row.state = 'offline'; return row;
        });
        if (rows.length) this.snapshots.set(key, rows);
        const restore = (data: Record<string, Object>, kind: string): ResourceRow => {
          const row = ResourceRow.from(data, kind); row.group = owner.profile.id; row.manager = owner.profile.manager; row.state = 'offline'; return row;
        };
        this.savedSessions.set(key, Wire.rows(saved, 'sessions').map((data: Record<string, Object>) => restore(data, 'session')));
        this.savedComputers.set(key, Wire.rows(saved, 'machines').map((data: Record<string, Object>) => restore(data, 'machine')));
      } catch (_) { /* A cold installation starts from the live directory. */ }
    }
    if (valid()) this.syncRows(model);
  }

  async refresh(model: ClientModel): Promise<void> {
    const scope = model.scope(), generation = model.connectionGeneration();
    if (scope !== this.ownerScope) { this.clear(); this.ownerScope = scope; }
    const revision = ++this.sequence;
    const current = (): boolean => this.sequence === revision && model.owns(generation, scope);
    this.loading = true; this.startError = false;
    if (!this.projects.length && model.directorySessions.length) this.syncRows(model);
    try {
      await this.restoreCatalog(model, current);
      if (!current()) return;
      if (!model.connected) { this.partial = true; return; }
      await model.refreshDirectory();
      if (!current()) return;
      const owners = model.directoryOwners(), keep = new Set<string>();
      let partial = model.directoryUnavailable > 0;
      for (const owner of owners) {
        const key = owner.scope(), epoch = owner.connectionGeneration();
        keep.add(key);
        if (!owner.connected) { partial = true; continue; }
        try {
          const result = await owner.repo().call('/api/v1/workspaces');
          if (!current()) return;
          if (!owner.owns(epoch, key)) { partial = true; continue; }
          if (!Array.isArray(result['workspaces'])) { partial = true; continue; }
          const failures = Wire.rows(result, 'errors');
          if (failures.length) partial = true;
          const records = Wire.rows(result, 'workspaces');
          const rows = records.map((data: Record<string, Object>) => this.fromHost(owner, data));
          if (failures.length) {
            const missing = new Set<string>(failures.map((failure: Record<string, Object>) => Wire.text(failure, 'source')));
            const present = new Set<string>(rows.map((row: ResourceRow) => JSON.stringify([row.machine, Wire.text(row.data(), 'source'), Wire.text(row.data(), 'source_id')])));
            for (const old of this.snapshots.get(key) || []) {
              const data = old.data(), source = Wire.text(data, 'source'), id = JSON.stringify([old.machine, source, Wire.text(data, 'source_id')]);
              if (missing.has(source) && !present.has(id)) { const retained = this.fromHost(owner, data); retained.state = 'offline'; rows.push(retained); }
            }
          }
          this.snapshots.set(key, rows);
          if (!failures.length) {
            // Store only list metadata for this authenticated manager. Conversation bodies and credentials stay outside this view.
            const metadata = (row: ResourceRow, kind: string): Record<string, Object> => {
              const value = row.data(), fields = kind === 'machine' ? ['id', 'name', 'title'] :
                ['id', 'session_id', 'machine_id', 'title', 'cwd', 'branch', 'repo_root', 'worktree', 'workspace_id', 'space_kind', 'space_id', 'slot_name', 'slot_id', 'slot_index', 'space_name', 'pane_id', 'pane_index', 'updated_at', 'assistant', 'agent', 'agent_label', 'agent_type', 'launcher', 'model'];
              const kept: Record<string, Object> = {};
              for (const field of fields) if (Wire.has(value, field)) kept[field] = value[field];
              if (kind === 'machine') kept['online'] = false;
              return kept;
            };
            const belongs = (row: ResourceRow): boolean => row.group === owner.profile.id && row.manager === owner.profile.manager;
            const sessions = model.directorySessions.filter(belongs).map((row: ResourceRow) => metadata(row, 'session'));
            const machines = model.directoryMachines.filter(belongs).map((row: ResourceRow) => metadata(row, 'machine'));
            const encoded = JSON.stringify(Wire.payload(['workspaces', 'sessions', 'machines'], [records, sessions, machines]));
            if (encoded.length <= 512 * 1024) {
              try { await owner.saveView('projects-workspaces', encoded); } catch (_) { /* Keep the confirmed in-memory catalog. */ }
            }
          }
        } catch (_) { if (!current()) return; partial = true; }
      }
      if (!current()) return;
      for (const key of this.snapshots.keys()) if (!keep.has(key)) { this.snapshots.delete(key); this.createdParents.delete(key); this.savedSessions.delete(key); this.savedComputers.delete(key); }
      this.syncRows(model); this.partial = partial;
    } catch (_) { if (current()) this.partial = true; }
    finally { if (this.sequence === revision) this.loading = false; }
  }

  selectCreated(tree: ProjectWorktree): void {
    this.createdListed = !!this.tree(tree.key);
    this.createdSelection = tree;
  }
  selectedWorktree(key: string): ProjectWorktree | undefined {
    const known = this.tree(key), created = this.createdSelection;
    if (!created || created.key !== key) return known;
    if (!created.receiptCurrent?.()) return undefined;
    if (known) return known.matchesCreated(created) ? known : undefined;
    return this.createdListed ? undefined : created;
  }

  tree(key: string): ProjectWorktree | undefined {
    for (const project of this.projects) {
      const match = project.trees.find((tree: ProjectWorktree) => tree.key === key);
      if (match) return match;
    }
    return undefined;
  }
}
