import type { ClientModel } from './ClientModel';
import { ResourceRow, Wire } from './Wire';
import type { HubRepository } from './HubRepository';

const KNOWLEDGE_KINDS: string[] = ['projects', 'topics', 'chats', 'people', 'docs', 'meetings', 'events', 'recordings'];

class KnowledgeOwner {
  root: ClientModel;
  rootGeneration: number;
  rootScope: string;
  rootManager: string;
  client: ClientModel;
  repository: HubRepository;
  generation: number;
  scope: string;
  manager: string;
  target: string;

  constructor(root: ClientModel, rootGeneration: number, rootScope: string, rootManager: string, client: ClientModel,
    repository: HubRepository, generation: number, scope: string, manager: string, target: string) {
    this.root = root; this.rootGeneration = rootGeneration; this.rootScope = rootScope; this.rootManager = rootManager;
    this.client = client; this.repository = repository; this.generation = generation; this.scope = scope;
    this.manager = manager; this.target = target;
  }
}

// The list encodes project duration and status as “short · active”; detail responses put those fields under meta.
export class KnowledgeListEntry {
  readonly priority: string;
  readonly relevance: number;
  readonly duration: string;
  readonly section: string;
  readonly tags: string[];
  readonly summary: string;
  readonly updated: string;

  constructor(row: ResourceRow) {
    const data = row.data(), meta = Wire.object(data, 'meta');
    const extra = Wire.text(data, 'extra').split('·').map((part: string) => part.trim());
    this.priority = Wire.text(data, 'importance') || Wire.text(meta, 'importance');
    this.relevance = Wire.number(data, 'relevance', Wire.number(meta, 'relevance'));
    this.tags = Wire.strings(data, 'tags').length ? Wire.strings(data, 'tags') : Wire.strings(meta, 'tags');
    this.summary = Wire.text(data, 'one_line') || row.detail;
    this.updated = Wire.text(data, 'updated') || Wire.text(data, 'started_at');
    this.duration = row.path === 'projects' ? extra[0] || Wire.text(meta, 'kind') : '';
    const status = extra[1] || Wire.text(meta, 'status') || row.state;
    this.section = row.path !== 'projects' ? 'all' : status === 'ignored' ? 'ignored' : status === 'done' ? 'done' :
      this.priority === 'P0' || this.priority === 'P1' ? 'focus' : 'active';
  }
}

export class KnowledgeSearchHit {
  entry: ResourceRow = new ResourceRow();
  count: number = 0;
  inName: boolean = false;
  hits: string[] = [];
}

export class KnowledgeDirectory {
  machine: string = '';
  path: string = '';
  label: string = '';
}

function cleanTags(value: string): string[] {
  return value.split(/[\s,，]+/).map((tag: string) => tag.trim().replace(/^#/, '')).filter((tag: string) => !!tag);
}

function segment(value: string): string { return encodeURIComponent(value); }

function shortOperation(): string {
  return 'harmony-' + Date.now().toString(36) + '-' + Math.floor(Math.random() * 0x7fffffff).toString(36);
}

function objectPayload(keys: string[], values: Object[]): Record<string, Object> { return Wire.payload(keys, values); }

/** Native Knowledge list, search, detail and the existing user-owned actions. */
@ObservedV2
export class KnowledgeWorkspace {
  @Trace entries: ResourceRow[] = [];
  @Trace results: KnowledgeSearchHit[] = [];
  @Trace tags: string[] = [];
  @Trace kind: string = 'projects';
  @Trace tag: string = '';
  @Trace query: string = '';
  @Trace selected: ResourceRow = new ResourceRow();
  @Trace detail: Record<string, Object> = {};
  @Trace inboxItems: Record<string, Object>[] = [];
  @Trace matches: Record<string, Object>[] = [];
  @Trace supportsExpectedPinned:boolean = false;
  @Trace agents: ResourceRow[] = [];
  @Trace momentText: string = '';
  @Trace loading: boolean = false;
  @Trace busy: boolean = false;
  @Trace error: string = '';
  @Trace writeState: string = '';
  @Trace editOpen: boolean = false;
  @Trace editImportance: string = 'P2';
  @Trace editRelevance: string = '0';
  @Trace editTags: string = '';
  @Trace editProjectKind: string = 'short';
  @Trace editStatus: string = 'active';
  @Trace askDraft: string = '';
  @Trace agentDraft: string = '';
  @Trace agentLauncher: string = '';
  @Trace directoryTargets: KnowledgeDirectory[] = [];
  @Trace targetMachines: ResourceRow[] = [];
  @Trace selectedDirectory: number = 0;

  private client: ClientModel | undefined;
  private editorClient: ClientModel | undefined;
  private owner: string = '';
  private viewScope: string = '';
  private revision: number = 0;
  private readRevision: number = 0;
  private listRows: ResourceRow[] = [];
  private recordingPage: boolean = false;
  private transientMeetingList: boolean = false;
  private searchTimer: number = -1;
  private draftWrite: Promise<void> = Promise.resolve();
  private momentTarget: string = '';
  private editorDirty: boolean = false;
  private editorRevision: number = 0;
  private detailEditorBaseline: number = 0;
  private detailInitialized: boolean = false;

  async open(client: ClientModel, row?: ResourceRow, seed: ResourceRow[] = [], initialKind: string = ''): Promise<void> {
    const revision = ++this.revision, readRevision = ++this.readRevision;
    if (this.searchTimer !== -1) clearTimeout(this.searchTimer);
    this.searchTimer = -1;
    this.client = client; this.editorClient = undefined; this.owner = row?.manager || client.profile.manager;
    this.viewScope = JSON.stringify([client.scope(), client.profile.manager, this.owner]);
    this.loading = true; this.busy = false; this.error = ''; this.writeState = ''; this.editOpen = false; this.editorDirty = false; this.editorRevision++;
    this.detailEditorBaseline = this.editorRevision;
    this.detailInitialized = false;
    this.detail = {}; this.inboxItems = []; this.supportsExpectedPinned=false; this.matches = []; this.agents = []; this.momentText = ''; this.momentTarget = ''; this.results = []; this.entries = [];
    this.agentDraft = ''; this.agentLauncher = ''; this.directoryTargets = []; this.targetMachines = []; this.selectedDirectory = 0;
    this.tags = [];
    this.recordingPage = !row && initialKind === 'recordings';
    this.transientMeetingList = !row && initialKind === 'meetings';
    this.listRows = seed.slice();
    if (row) {
      this.selected = this.copyRow(row);
      this.kind = KNOWLEDGE_KINDS.includes(row.path) ? row.path : this.kind;
      const openedQuery = Wire.text(row.data(), 'opened_from_query');
      if (openedQuery) this.query = openedQuery;
    } else {
      this.selected = new ResourceRow();
    }
    let owner: KnowledgeOwner | undefined;
    try {
      owner = await this.capture(client, this.owner, row ? row.key() : 'list');
      if (!this.current(owner, revision) || !this.currentRead(revision, readRevision)) return;
      const saved = Wire.parse(await owner.client.loadView(this.recordingPage ? 'knowledge-recordings' : 'knowledge-workspace'));
      if (!this.current(owner, revision) || !this.currentRead(revision, readRevision)) return;
      if (!row) {
        if (KNOWLEDGE_KINDS.includes(initialKind)) { this.kind = initialKind; this.tag = ''; this.query = this.recordingPage ? Wire.text(saved, 'query') : ''; }
        else {
          this.kind = KNOWLEDGE_KINDS.includes(Wire.text(saved, 'kind')) ? Wire.text(saved, 'kind') : this.kind;
          if (this.kind === 'recordings' && !owner.client.demo) this.kind = 'meetings';
          this.tag = Wire.text(saved, 'tag'); this.query = Wire.text(saved, 'query');
        }
      }
      if (row) {
        await this.initializeDetail(owner, revision, row, readRevision);
        if (!this.current(owner, revision) || !this.currentRead(revision, readRevision)) return;
      } else {
        if (seed.length) this.entries = seed.map((item: ResourceRow) => this.copyRow(item));
        await this.load(owner, revision, readRevision);
      }
    } catch (failure) {
      if (this.currentRead(revision, readRevision) && (owner ? this.current(owner, revision) : this.currentRoot(client, revision))) this.error = this.errorCode(failure as Error);
    } finally {
      if (this.currentRead(revision, readRevision)) this.loading = false;
    }
  }

  hide(): void {
    this.revision++; this.readRevision++; this.client = undefined; this.editorClient = undefined; this.listRows = []; this.detail = {}; this.inboxItems = []; this.supportsExpectedPinned=false; this.matches = []; this.agents = [];
    this.detailInitialized = false;
    if (this.searchTimer !== -1) clearTimeout(this.searchTimer);
    this.searchTimer = -1;
  }

  async refresh(): Promise<void> {
    const client = this.client;
    if (!client) return;
    const revision = this.revision, readRevision = ++this.readRevision, row = this.selected.id ? this.selected : undefined;
    this.loading = true;
    let owner: KnowledgeOwner | undefined;
    try {
      owner = await this.capture(client, row?.manager || this.owner, row ? row.key() : 'list');
      if (!this.current(owner, revision) || !this.currentRead(revision, readRevision)) return;
      if (row) {
        if (this.detailInitialized) await this.loadDetail(owner, revision, row, readRevision);
        else await this.initializeDetail(owner, revision, row, readRevision);
      } else await this.load(owner, revision, readRevision);
    } catch (failure) {
      if (this.currentRead(revision, readRevision) && (owner ? this.current(owner, revision) : this.currentRoot(client, revision))) this.error = this.errorCode(failure as Error);
    } finally { if (this.currentRead(revision, readRevision)) this.loading = false; }
  }

  setKind(kind: string): void {
    if (!KNOWLEDGE_KINDS.includes(kind) || kind === this.kind) return;
    this.invalidateListRead(); this.kind = kind; this.tag = ''; this.saveFilters(); this.refresh().catch(() => undefined);
  }

  setTag(tag: string): void {
    if (this.tag === tag) return;
    this.tag = tag;
    this.saveFilters();
    // Search spans all kinds and tags. Keep its in-flight results while remembering the list filter.
    if (!this.query.trim()) {
      this.invalidateListRead();
      this.refresh().catch(() => undefined);
    }
  }

  setQuery(query: string): void {
    this.invalidateListRead(); this.query = query;
    if (this.searchTimer !== -1) clearTimeout(this.searchTimer);
    this.saveFilters();
    this.searchTimer = setTimeout(() => { this.searchTimer = -1; this.refresh().catch(() => undefined); }, 280);
  }

  async openEntry(row: ResourceRow): Promise<ResourceRow> {
    const next = this.copyRow(row);
    if (this.query.trim()) { const data = next.data(); data['opened_from_query'] = this.query.trim(); next.raw = JSON.stringify(data); }
    return next;
  }

  beginEdit(): void {
    this.editOpen = true;
    if (!this.editorDirty) {
    const meta = Wire.object(this.detail, 'meta');
    this.editImportance = Wire.text(meta, 'importance', 'P2');
    this.editRelevance = String(Math.max(0, Math.min(100, Math.round(Wire.number(meta, 'relevance')))));
    this.editTags = Wire.strings(meta, 'tags').join(' ');
    if (this.selected.path === 'projects') {
      this.editProjectKind = Wire.text(meta, 'kind', 'short'); this.editStatus = Wire.text(meta, 'status', 'active');
    }
    }
    this.saveEditorLater();
  }

  setEditImportance(value: string): void { if (['P0', 'P1', 'P2', 'P3'].includes(value)) { this.editImportance = value; this.editorDirty = true; this.saveEditorLater(); } }
  setEditRelevance(value: string): void { this.editRelevance = value; this.editorDirty = true; this.saveEditorLater(); }
  setEditTags(value: string): void { this.editTags = value; this.editorDirty = true; this.saveEditorLater(); }
  setEditProjectKind(value: string): void { if (['short', 'long'].includes(value)) { this.editProjectKind = value; this.editorDirty = true; this.saveEditorLater(); } }
  setEditStatus(value: string): void { if (['active', 'paused', 'done', 'ignored'].includes(value)) { this.editStatus = value; this.editorDirty = true; this.saveEditorLater(); } }
  setAgentDraft(value: string): void { this.agentDraft = value; this.saveEditorLater(); }
  setAgentLauncher(value: string): void { this.agentLauncher = value; this.saveEditorLater(); }

  canSaveEdit(): boolean {
    const value = Number(this.editRelevance);
    return Number.isInteger(value) && value >= 0 && value <= 100 && ['P0', 'P1', 'P2', 'P3'].includes(this.editImportance) &&
      !this.busy && !['pending', 'unknown'].includes(this.writeState);
  }

  async saveEdit(): Promise<boolean> {
    const row = this.selected, client = this.client;
    if (!row.id || !client || !this.canSaveEdit()) return false;
    const relevance = Number(this.editRelevance), project = row.path === 'projects';
    const body = objectPayload(['importance', 'relevance', 'tags'], [this.editImportance, relevance, cleanTags(this.editTags)]);
    if (project) { body['status'] = this.editStatus; body['project_kind'] = this.editProjectKind; }
    return this.updateEntry(body,true);
  }

  async saveFacts(value:string, expected:string):Promise<boolean> {
    if (!this.supportsExpectedPinned || this.selected.path!=='recordings' || !this.selected.id || !this.client || this.busy || ['pending','unknown'].includes(this.writeState)) return false;
    return this.updateEntry(objectPayload(['pinned','expected_pinned'],[value.trim(),expected]),false);
  }

  private async updateEntry(body:Record<string,Object>, metadata:boolean):Promise<boolean> {
    const row=this.selected, client=this.client;
    if (!client || !row.id) return false;
    const editorRevision=this.editorRevision, revision=this.revision, targetKey=row.key(), ownerName=row.manager || this.owner;
    const path = '/api/v1/kb/' + segment(row.path) + '/' + segment(row.id);
    this.busy = true; this.error = ''; this.writeState = 'sending';
    let owner: KnowledgeOwner | undefined;
    try {
      owner = await this.capture(client, ownerName, targetKey);
      if (!this.current(owner, revision) || this.selected.key() !== targetKey) return false;
      if (owner.client.operationBlocked(path)) { this.writeState = 'pending'; this.error = 'pending_action'; return false; }
      const admitted=owner;
      await owner.repository.call(path, 'PUT', body, shortOperation(), () => this.current(admitted,revision) && this.selected.key()===targetKey);
      if (!this.current(owner, revision) || this.selected.key() !== targetKey) return false;
      this.writeState = 'done';
      // An acknowledged save stays acknowledged if reading its new value back fails.
      await this.reloadDetail(owner, revision, row);
      if (metadata && this.current(owner, revision) && editorRevision === this.editorRevision) { this.editOpen = false; this.editorDirty = false; this.clearEditorLater(owner, row); }
      return true;
    } catch (failure) {
      if (!owner || !this.current(owner, revision) || this.selected.key() !== targetKey) {
        if (!owner && this.revision === revision && this.client === client) { this.writeState = 'failed'; this.error = this.errorCode(failure as Error); }
        return false;
      }
      if (!metadata && Wire.number(failure as Object as Record<string,Object>,'status')===409) {
        this.writeState='failed';
        await this.reloadDetail(owner,revision,row);
        if (this.current(owner,revision) && this.selected.key()===targetKey && !this.error) this.error='phone_facts_changed';
        return false;
      }
      this.writeState = this.unknownWrite(failure as Error);
      this.error = this.errorCode(failure as Error);
      return false;
    } finally { if (this.revision === revision && this.client === client) this.busy = false; }
  }

  canDeleteRecording(): boolean { return this.selected.path === 'recordings' && !this.busy && !['pending', 'unknown', 'deleted'].includes(this.writeState); }

  async deleteRecording(): Promise<boolean> {
    const row = this.selected, client = this.client;
    if (!client || !this.canDeleteRecording()) return false;
    const revision = this.revision, targetKey = row.key(), path = '/api/v1/recordings/' + segment(row.id);
    this.busy = true; this.writeState = 'sending'; this.error = '';
    let owner: KnowledgeOwner | undefined;
    try {
      owner = await this.capture(client, row.manager || this.owner, targetKey);
      if (!this.current(owner, revision) || this.selected.key() !== targetKey) return false;
      if (owner.client.operationBlocked(path)) { this.writeState = 'pending'; this.error = 'pending_action'; return false; }
      await owner.repository.call(path, 'DELETE', {}, shortOperation());
      if (!this.current(owner, revision) || this.selected.key() !== targetKey) return false;
      this.readRevision++; this.loading = false;
      this.writeState = 'deleted'; this.entries = this.entries.filter((item: ResourceRow) => item.key() !== targetKey);
      return true;
    } catch (failure) {
      if (!owner || !this.current(owner, revision) || this.selected.key() !== targetKey) {
        if (!owner && this.revision === revision && this.client === client) { this.writeState = 'failed'; this.error = this.errorCode(failure as Error); }
        return false;
      }
      this.writeState = this.unknownWrite(failure as Error); this.error = this.errorCode(failure as Error); return false;
    } finally { if (this.revision === revision && this.client === client) this.busy = false; }
  }

  async makeMinutes(): Promise<boolean> { return this.recordingWrite('minutes', objectPayload([], [])); }

  async addMinutesTodos(actionIds: string[]): Promise<boolean> {
    const minutes = Wire.object(this.detail, 'minutes'), version = Wire.number(minutes, 'version');
    if (version <= 0 || !actionIds.length) return false;
    return this.recordingWrite('minutes/todos', objectPayload(['version', 'actions'], [version, actionIds]));
  }

  askPersona(): Promise<ResourceRow | undefined> { return this.handoff('knowledge'); }

  async openMoment(id: string): Promise<string> {
    const client = this.client, row = this.selected;
    if (!client || !row.id || !id) throw new Error('request_updated');
    const revision = this.revision, owner = await this.capture(client, row.manager || this.owner, row.key());
    if (!this.current(owner, revision) || this.selected.key() !== row.key()) throw new Error('request_updated');
    this.momentTarget = id; this.momentText = '';
    const result = await owner.repository.call('/api/v1/kb/moments/' + segment(id));
    if (!this.current(owner, revision) || this.selected.key() !== row.key() || this.momentTarget !== id) throw new Error('request_updated');
    this.momentText = Wire.text(result, 'markdown');
    return this.momentText;
  }

  dispatchAgent(machine: ResourceRow, prompt: string, launcher: string): Promise<ResourceRow | undefined> {
    const row = this.selected, client = this.client, directory = this.directoryTargets[this.selectedDirectory];
    if (!client || row.path !== 'projects' || !directory || !this.canUseMachine(machine, row.manager || this.owner) || !prompt.trim()) return Promise.resolve(undefined);
    const revision = this.revision, targetKey = row.key(); this.busy = true; this.error = '';
    const context = this.structuredContext(row, 'knowledge');
    const message = context + '\n\n' + prompt.trim();
    const ownerName = row.manager || this.owner;
    return this.dispatchAgentOnOwner(client, ownerName, revision, targetKey, machine, directory.path, message, launcher);
  }

  prepareAgentTargets(machines: ResourceRow[]): void {
    const meta = Wire.object(this.detail, 'meta'), dirs = Wire.strings(meta, 'dirs'), choices: KnowledgeDirectory[] = [];
    dirs.forEach((raw: string) => {
      const colon = raw.indexOf(':');
      if (colon < 1 || colon === raw.length - 1) return;
      const machine = raw.slice(0, colon), path = raw.slice(colon + 1);
      if (!machines.some((row: ResourceRow) => row.id === machine && this.canUseMachine(row, this.owner))) return;
      const target = new KnowledgeDirectory(); target.machine = machine; target.path = path; target.label = machine + ' · ' + path;
      if (!choices.some((item: KnowledgeDirectory) => item.machine === machine && item.path === path)) choices.push(target);
    });
    this.directoryTargets = choices; this.selectedDirectory = 0;
  }

  machineForDirectory(machines: ResourceRow[]): ResourceRow | undefined {
    const selected = this.directoryTargets[this.selectedDirectory];
    return selected ? machines.find((row: ResourceRow) => row.id === selected.machine && this.canUseMachine(row, this.owner)) : undefined;
  }

  canUseMachine(machine: ResourceRow, manager: string = this.owner): boolean {
    const data = machine.data(), trust = Wire.text(data, 'trust');
    return machine.kind === 'machine' && machine.id.length > 0 && (!machine.manager || machine.manager === manager) &&
      Wire.bool(data, 'online') && ['personal', 'work'].includes(trust) && !Wire.bool(data, 'pending') && !Wire.bool(data, 'pending_confirm');
  }

  private async recordingWrite(action: string, body: Record<string, Object>): Promise<boolean> {
    const row = this.selected, client = this.client;
    if (!client || row.path !== 'recordings' || this.busy || ['pending', 'unknown', 'deleted'].includes(this.writeState)) return false;
    const revision = this.revision, key = row.key(), path = '/api/v1/recordings/' + segment(row.id) + '/' + action;
    this.busy = true; this.writeState = 'sending'; this.error = '';
    let owner: KnowledgeOwner | undefined;
    try {
      owner = await this.capture(client, row.manager || this.owner, key);
      if (!this.current(owner, revision) || this.selected.key() !== key) return false;
      if (owner.client.operationBlocked(path)) { this.writeState = 'pending'; this.error = 'pending_action'; return false; }
      await owner.repository.call(path, 'POST', body, shortOperation());
      if (!this.current(owner, revision) || this.selected.key() !== key) return false;
      this.writeState = 'done';
      await this.reloadDetail(owner, revision, row);
      return true;
    } catch (failure) {
      if (!owner || !this.current(owner, revision) || this.selected.key() !== key) {
        if (!owner && this.revision === revision && this.client === client) { this.writeState = 'failed'; this.error = this.errorCode(failure as Error); }
        return false;
      }
      this.writeState = this.unknownWrite(failure as Error); this.error = this.errorCode(failure as Error); return false;
    } finally { if (this.revision === revision && this.client === client) this.busy = false; }
  }

  private async handoff(kind: string): Promise<ResourceRow | undefined> {
    const client = this.client, row = this.selected;
    if (!client || !row.id || this.busy) return undefined;
    const revision = this.revision, targetKey = row.key();
    this.busy = true; this.error = '';
    let owner: KnowledgeOwner | undefined;
    try {
      owner = await this.capture(client, row.manager || this.owner, targetKey);
      if (!this.current(owner, revision) || this.selected.key() !== targetKey) return undefined;
      const target = await owner.client.handoff(row);
      if (!this.current(owner, revision) || this.selected.key() !== targetKey) return undefined;
      // `handoff` stores the structured entry reference on that manager's main-chat draft; no message is sent here.
      return target;
    } catch (failure) {
      if (owner && this.current(owner, revision)) this.error = this.errorCode(failure as Error);
      else if (!owner && this.revision === revision && this.client === client) this.error = this.errorCode(failure as Error);
      return undefined;
    } finally { if (this.revision === revision && this.client === client) this.busy = false; }
  }

  private async dispatchAgentOnOwner(client: ClientModel, manager: string, revision: number, targetKey: string, machine: ResourceRow,
    directory: string, prompt: string, launcher: string): Promise<ResourceRow | undefined> {
    let owner: KnowledgeOwner | undefined;
    try {
      owner = await this.capture(client, manager, targetKey);
      if (!this.current(owner, revision) || this.selected.key() !== targetKey || !this.canUseMachine(machine, manager)) return undefined;
      const created = await owner.client.createAgent(prompt, machine, directory, launcher.trim());
      if (!this.current(owner, revision) || this.selected.key() !== targetKey) return undefined;
      this.busy = false; return created;
    } catch (failure) {
      if (owner && this.current(owner, revision)) this.error = this.errorCode(failure as Error);
      if (this.revision === revision && this.client === client) this.busy = false;
      return undefined;
    }
  }

  private async load(owner: KnowledgeOwner, revision: number, readRevision: number): Promise<void> {
    const kind = this.kind, tag = this.tag, query = this.query.trim();
    if (query) {
      await this.loadSearch(owner, revision, readRevision, query); return;
    }
    const suffix = tag ? '?tag=' + segment(tag) : '';
    if (kind === 'meetings' || this.recordingPage) {
      const both = await Promise.all([owner.repository.call('/api/v1/kb/meetings' + suffix),
        owner.repository.call('/api/v1/kb/recordings' + suffix)]);
      if (!this.current(owner, revision) || !this.currentRead(revision, readRevision)) return;
      const imported = Wire.rows(both[0], 'items').map((item: Record<string,Object>) => this.entryRow(item,owner.manager,'meetings'));
      const recordings = Wire.rows(both[1], 'items').map((item: Record<string,Object>) => this.entryRow(item,owner.manager,'recordings'));
      const replaced = new Set<string>(), byMeeting = new Map<string,ResourceRow>();
      for (const row of imported) {
        const key = JSON.stringify([row.manager,row.id]);
        if (!byMeeting.has(key)) byMeeting.set(key,row);
      }
      for (const row of recordings) {
        const entry = row.data(), meetingId = Wire.text(entry,'meeting_id');
        const key = JSON.stringify([row.manager,meetingId]), original = meetingId ? byMeeting.get(key) : undefined;
        if (!original) continue;
        replaced.add(key);
        entry['imported_meeting'] = original.data();
        row.raw = JSON.stringify(entry);
      }
      const merged = imported.filter((row:ResourceRow) => !replaced.has(JSON.stringify([row.manager,row.id]))).concat(recordings);
      const when = new Map<ResourceRow,number>();
      for (const row of merged) {
        const stamp = Date.parse(Wire.text(row.data(),'updated'));
        when.set(row,Number.isFinite(stamp) ? stamp : 0);
      }
      this.entries = merged.sort((a:ResourceRow,b:ResourceRow) => (when.get(b) || 0) - (when.get(a) || 0));
      this.tags = Array.from(new Set<string>(Wire.strings(both[0],'tags').concat(Wire.strings(both[1],'tags'))));
    } else {
      const result = await owner.repository.call('/api/v1/kb/' + segment(kind) + suffix);
      if (!this.current(owner, revision) || !this.currentRead(revision, readRevision)) return;
      this.entries = Wire.rows(result,'items').map((data:Record<string,Object>) => this.entryRow(data,owner.manager,kind));
      this.tags = Wire.strings(result,'tags');
    }
    this.error = '';
  }

  private async loadSearch(owner: KnowledgeOwner, revision: number, readRevision: number, query: string): Promise<void> {
    const result = await owner.repository.call('/api/v1/kb-search?q=' + segment(query) + '&results=1');
    if (!this.current(owner, revision) || !this.currentRead(revision, readRevision)) return;
    const hits = Wire.rows(result, 'results').map((data: Record<string, Object>) => {
      const hit = new KnowledgeSearchHit(), entry = Wire.object(data, 'entry');
      hit.entry = this.entryRow(entry, owner.manager, Wire.text(entry, 'kind'));
      hit.count = Wire.number(data, 'count'); hit.inName = Wire.bool(data, 'in_name');
      hit.hits = Wire.rows(data, 'hits').map((item: Record<string, Object>) => Wire.text(item, 'line'));
      return hit;
    });
    this.results = this.recordingPage ? hits.filter((hit:KnowledgeSearchHit) => ['meetings','recordings'].includes(hit.entry.path)) : hits; this.error = '';
  }

  private async initializeDetail(owner: KnowledgeOwner, revision: number, row: ResourceRow, readRevision: number): Promise<void> {
    this.editorClient = owner.client;
    const draft = Wire.parse(await owner.client.loadView(this.draftKey(row)));
    if (!this.current(owner, revision) || !this.currentRead(revision, readRevision) || this.selected.key() !== row.key()) return;
    await this.loadDetail(owner, revision, row, readRevision);
    if (!this.current(owner, revision) || !this.currentRead(revision, readRevision) || this.selected.key() !== row.key()) return;
    if (this.editorRevision === this.detailEditorBaseline) this.loadEditor(draft);
    this.targetMachines = owner.client.machines.filter((machine: ResourceRow) => this.canUseMachine(machine, owner.manager));
    if (row.path === 'projects') this.prepareAgentTargets(this.targetMachines);
    this.detailInitialized = true;
  }

  private async loadDetail(owner: KnowledgeOwner, revision: number, row: ResourceRow, readRevision: number): Promise<void> {
    const q = Wire.text(row.data(), 'opened_from_query'), path = '/api/v1/kb/' + segment(row.path) + '/' + segment(row.id) +
      (q ? '?q=' + segment(q) : '');
    const result = await owner.repository.call(path);
    if (!this.current(owner, revision) || !this.currentRead(revision, readRevision) || this.selected.key() !== row.key()) return;
    const detail = Wire.object(result, 'detail');
    if (Wire.text(detail, 'id') !== row.id || Wire.text(detail, 'kind') !== row.path) throw new Error('request_updated');
    this.detail = detail;
    this.inboxItems = row.path === 'chats' ? Wire.rows(result, 'inbox') : [];
    this.supportsExpectedPinned=row.path==='recordings' && Wire.bool(result,'supports_expected_pinned');
    this.matches = Wire.rows(result, 'matches');
    this.agents = Wire.rows(result, 'agents').map((data: Record<string, Object>) => {
      const agent = ResourceRow.from(data, 'session'); agent.manager = owner.manager; return agent;
    });
    if (!this.editorDirty && !this.editOpen) {
      this.editImportance = Wire.text(Wire.object(detail, 'meta'), 'importance', 'P2');
      this.editRelevance = String(Wire.number(Wire.object(detail, 'meta'), 'relevance'));
      this.editTags = Wire.strings(Wire.object(detail, 'meta'), 'tags').join(' ');
    }
    this.error = '';
  }

  private async reloadDetail(owner: KnowledgeOwner, revision: number, row: ResourceRow): Promise<void> {
    const readRevision = ++this.readRevision;
    this.loading = true;
    try { await this.loadDetail(owner, revision, row, readRevision); }
    catch (failure) {
      if (this.current(owner, revision) && this.currentRead(revision, readRevision) && this.selected.key() === row.key()) this.error = this.errorCode(failure as Error);
    } finally { if (this.currentRead(revision, readRevision)) this.loading = false; }
  }

  private async capture(client: ClientModel, manager: string, target: string): Promise<KnowledgeOwner> {
    const rootGeneration = client.connectionGeneration(), rootScope = client.scope(), rootManager = client.profile.manager;
    if (!client.connected || !client.profile.id) throw new Error('offline');
    let view = client;
    if (manager && manager !== client.profile.manager) {
      const ref = new ResourceRow('owner', '', 'manager'); ref.manager = manager;
      view = await client.viewFor(ref);
      if (!client.owns(rootGeneration, rootScope) || view.profile.manager !== manager || !view.connected) throw new Error('request_updated');
    }
    const generation = view.connectionGeneration(), scope = view.scope(), repository = view.repo();
    return new KnowledgeOwner(client, rootGeneration, rootScope, rootManager, view, repository, generation, scope, view.profile.manager, target);
  }

  private current(owner: KnowledgeOwner, revision: number): boolean {
    if (this.revision !== revision || this.client !== owner.root || !owner.root.connected || !owner.root.owns(owner.rootGeneration, owner.rootScope) ||
      owner.root.profile.manager !== owner.rootManager || !owner.client.owns(owner.generation, owner.scope) ||
      owner.client.profile.manager !== owner.manager || !owner.client.connected) return false;
    try { return owner.client.repo() === owner.repository; } catch (_) { return false; }
  }

  private currentRoot(client: ClientModel, revision: number): boolean {
    return this.revision === revision && this.client === client &&
      this.viewScope === JSON.stringify([client.scope(), client.profile.manager, this.owner]);
  }

  private currentRead(revision: number, readRevision: number): boolean {
    return this.revision === revision && this.readRevision === readRevision;
  }

  private invalidateListRead(): void {
    this.readRevision++;
    if (!this.selected.id) { this.loading = false; this.error = ''; }
  }

  private async saveFilters(): Promise<void> {
    const client = this.client, revision = this.revision;
    if (!client || this.transientMeetingList) return;
    const scope = this.viewScope;
    try {
      await client.saveView(this.recordingPage ? 'knowledge-recordings' : 'knowledge-workspace', JSON.stringify(objectPayload(['kind', 'tag', 'query'], [this.kind, this.tag, this.query])));
      if (this.revision !== revision || this.viewScope !== scope || this.client !== client) return;
    } catch (_) { if (this.revision === revision) this.error = 'knowledge_storage_unavailable'; }
  }

  private draftKey(row: ResourceRow): string { return 'knowledge-draft:' + JSON.stringify([row.manager || this.owner, row.path, row.id]); }

  private loadEditor(draft: Record<string, Object>): void {
    this.editorDirty = Wire.bool(draft, 'metadata_dirty', Wire.has(draft, 'importance'));
    const meta = Wire.object(this.detail, 'meta');
    this.editImportance = Wire.text(draft, 'importance', Wire.text(meta, 'importance', 'P2'));
    this.editRelevance = Wire.text(draft, 'relevance', String(Wire.number(meta, 'relevance')));
    this.editTags = Wire.text(draft, 'tags', Wire.strings(meta, 'tags').join(' '));
    this.editProjectKind = Wire.text(draft, 'project_kind', Wire.text(meta, 'kind', 'short'));
    this.editStatus = Wire.text(draft, 'status', Wire.text(meta, 'status', 'active'));
    this.agentDraft = Wire.text(draft, 'agent_prompt'); this.agentLauncher = Wire.text(draft, 'agent_launcher');
  }

  private saveEditorLater(): void {
    this.editorRevision++;
    const owner = this.editorClient, revision = this.revision, row = this.selected;
    if (!owner || !row.id) return;
    const scope = owner.scope(), manager = owner.profile.manager, key = this.draftKey(row), epoch = owner.viewRevision(key);
    const data = objectPayload(['importance', 'relevance', 'tags', 'project_kind', 'status', 'agent_prompt', 'agent_launcher', 'metadata_dirty'],
      [this.editImportance, this.editRelevance, this.editTags, this.editProjectKind, this.editStatus, this.agentDraft, this.agentLauncher, this.editorDirty]);
    this.draftWrite = this.draftWrite.catch(() => {}).then(async (): Promise<void> => {
      if (owner.scope() !== scope || owner.profile.manager !== manager) return;
      await owner.saveView(key, JSON.stringify(data), epoch);
    }).catch((failure: Error) => {
      if (this.revision === revision && this.editorClient === owner && this.selected.key() === row.key()) this.error = this.errorCode(failure);
    });
  }

  private clearEditorLater(owner: KnowledgeOwner, row: ResourceRow): void {
    const version = this.editorRevision, key = this.draftKey(row), epoch = owner.client.viewRevision(key);
    this.draftWrite = this.draftWrite.catch(() => {}).then(async (): Promise<void> => {
      if (version !== this.editorRevision || owner.client.scope() !== owner.scope) return;
      await owner.client.saveView(key, '', epoch);
    }).catch(() => { if (version === this.editorRevision) this.error = 'knowledge_storage_unavailable'; });
  }

  private entryRow(data: Record<string, Object>, manager: string, kind: string): ResourceRow {
    const row = ResourceRow.from(data, 'knowledge');
    row.path = Wire.text(data, 'kind', kind); row.manager = Wire.text(data, 'manager', manager);
    return row;
  }

  private copyRow(row: ResourceRow): ResourceRow {
    const copy = new ResourceRow(row.id, row.title, row.kind);
    copy.detail = row.detail; copy.state = row.state; copy.group = row.group; copy.manager = row.manager; copy.machine = row.machine;
    copy.session = row.session; copy.path = row.path; copy.raw = row.raw;
    return copy;
  }

  private structuredContext(row: ResourceRow, kind: string): string {
    const name = Wire.text(Wire.object(this.detail, 'meta'), 'name', row.title || row.id);
    const summary = Wire.text(this.detail, 'summary', row.detail);
    const log = Wire.strings(this.detail, 'log').slice(-10).join('\n');
    const dirs = Wire.strings(Wire.object(this.detail, 'meta'), 'dirs').join('\n');
    const payload = objectPayload(['kind', 'owner', 'entry_kind', 'entry_id', 'title', 'summary', 'timeline', 'directories'],
      [kind, row.manager || this.owner, row.path, row.id, name, summary, log, dirs]);
    return '<persona-context>' + JSON.stringify(payload) + '</persona-context>';
  }

  private unknownWrite(error: Error): string {
    const data = error as Object as Record<string, Object>;
    return Wire.bool(data, 'resultPending') ? 'pending' : Wire.bool(data, 'resultUnknown') ? 'unknown' : 'failed';
  }

  private errorCode(error: Error): string {
    const known = ['offline', 'request_updated', 'permission_denied', 'result_unknown', 'pending_action', 'storage_unavailable'];
    const data = error as Object as Record<string, Object>;
    const status = Wire.number(data, 'status');
    return known.includes(error.message) ? error.message : status === 401 || status === 403 ? 'permission_denied' :
      Wire.bool(data, 'resultPending') ? 'pending' :
      Wire.bool(data, 'resultUnknown') ? 'result_unknown' : 'knowledge_request_failed';
  }
}

/** The open recording editor owns edits made while a previous fact save is waiting for the Hub. */
@ObservedV2
export class RecordingFactsDraft {
  @Trace open: boolean = false;
  @Trace text: string = '';
  @Trace conflict: boolean = false;
  @Trace baseline: string = '';
  private lease: number = 0;
  private revision: number = 0;

  begin(value: string): void {
    this.lease++; this.revision++; this.conflict=false; this.baseline=value; this.text=value; this.open=true;
  }
  edit(value: string): void { this.revision++; this.text=value; }
  close(): void { this.lease++; this.revision++; this.conflict=false; this.open=false; this.text=''; this.baseline=''; }

  async save(workspace: KnowledgeWorkspace): Promise<void> {
    if (!this.open || workspace.busy || !workspace.supportsExpectedPinned) return;
    const lease=this.lease, revision=this.revision, target=workspace.selected.key(), kind=workspace.selected.path, sent=this.text.trim();
    const saved=await workspace.saveFacts(sent,this.baseline);
    if (lease!==this.lease || workspace.selected.key()!==target || workspace.selected.path!==kind) return;
    if (saved) {
      this.baseline=sent; this.conflict=false;
      if (this.revision===revision) this.close();
    } else if (workspace.error==='phone_facts_changed') {
      this.baseline=Wire.text(Wire.object(workspace.detail,'meta'),'pinned'); this.conflict=true;
    }
  }
}
