import type { TaskStartRecovery } from './TaskStartRecovery';
import { ClientModel } from './ClientModel';
import { HubRepository, segment } from './HubRepository';
import { HttpFailure } from '../platform/HubTransport';
import { todoDetailPatchAccepted } from './PhoneTodoDetailEditor';
import { ResourceRow, Wire } from './Wire';
import { TODO_HUB_CHOICES, TODO_HUB_SECTIONS, TodoHubCounts, TodoHubRow, todoHubChoiceIncludes, todoHubLabel } from './TodoHubView';
import { TODO_LIST_SECTIONS, type TodoListSection } from './TodoViewWire';
import { type TodoWait, type TodoWaitContact, type TodoWaitPRBlocker, parseTodoWaitKind, parseTodoWaitPRBlocker } from './TodoWaitWire';
import type { TodoPhonePlacement } from './TodoPhoneSections';
import { TODO_PHONE_SECTIONS, TodoPhoneRowContext, TodoPhoneSections, phoneTodoSeenCanFile, phoneTodoTitle } from './TodoPhoneSections';

const TODO_STATES: string[] = ['needs_you', 'checking', 'working', 'followup', 'not_started', 'done'];
const TODO_STATUS_ORDER: string[] = ['needs_you', 'checking', 'working', 'followup', 'not_started', 'done'];

interface TodoSeenChange { id:string; manager:string; stamp:string; otherOpen:boolean; }

interface TodoOwnerSnapshot {
  root: ClientModel;
  client: ClientModel;
  repo: HubRepository;
  rootGeneration: number;
  rootScope: string;
  generation: number;
  scope: string;
  revision: number;
  groupId: string;
  managerId: string;
  todoId: string;
  todoManagerId: string;
}

export class TodoWorkspaceAgent {
  machineId: string = '';
  sessionId: string = '';
  title: string = '';
  cwd: string = '';
  agent: string = '';
  startError: string = '';
  linkedAt: string = '';
  releasedAt: string = '';
  dismissedAt: string = '';
  source: string = '';
  via: string = '';
  raw: string = '{}';

  constructor(data: Record<string, Object> = {}) {
    this.machineId = Wire.text(data, 'machine_id');
    this.sessionId = Wire.text(data, 'session_id');
    this.title = Wire.text(data, 'title');
    this.cwd = Wire.text(data, 'cwd');
    this.agent = Wire.text(data, 'agent');
    this.startError = Wire.text(data, 'start_error');
    this.linkedAt = Wire.text(data, 'linked_at');
    this.releasedAt = Wire.text(data, 'released_at');
    this.dismissedAt = Wire.text(data, 'dismissed_at');
    this.source = Wire.text(data, 'source');
    this.via = Wire.text(data, 'via');
    this.raw = JSON.stringify(data);
  }

  isActive(): boolean { return !this.releasedAt && !this.dismissedAt; }
}

export class TodoWorkspaceSpace {
  machineId: string = '';
  space: string = '';
  linkedAt: string = '';

  constructor(data: Record<string, Object> = {}) {
    this.machineId = Wire.text(data, 'machine_id');
    this.space = Wire.text(data, 'space');
    this.linkedAt = Wire.text(data, 'linked_at');
  }
}

export function todoDayKey(date:Date):string {
  return String(date.getFullYear())+'-'+String(date.getMonth()+1).padStart(2,'0')+'-'+String(date.getDate()).padStart(2,'0');
}

function dayFromKey(key:string):number {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(key)) return Number.NaN;
  const d=new Date(key+'T00:00:00');
  return todoDayKey(d)===key ? d.getTime() : Number.NaN;
}

export interface TodoCalendarDue { at:string; day:string; }
export interface TodoCalendarDrop { day:number; minute:number; }
export interface TodoCalendarBand { left:number; top:number; width:number; height:number; days:number[]; hour:number; }

/** Timed rows snap to the nearest quarter. The all-day row clears the time. Month cells keep it. */
export function todoCalendarDrop(bands:TodoCalendarBand[],x:number,y:number):TodoCalendarDrop|undefined {
  const band=bands.find((b:TodoCalendarBand)=>x>=b.left && x<b.left+b.width && y>=b.top && y<b.top+b.height);
  if (!band || !band.days.length) return undefined;
  const col=Math.min(band.days.length-1,Math.floor((x-band.left)*band.days.length/band.width));
  const minute=band.hour<0 ? band.hour : Math.max(0,Math.min(1425,15*Math.round((band.hour*60+60*(y-band.top)/band.height)/15)));
  return {day:band.days[col],minute:minute};
}

export function todoDropDue(item:TodoWorkspaceItem,target:TodoCalendarDrop):TodoCalendarDue {
  const date=new Date(target.day);
  if (target.minute===-1 || target.minute===-2 && !item.dueAt) return {at:'',day:todoDayKey(date)};
  if (target.minute===-2) {
    const previous=new Date(item.due);
    date.setHours(previous.getHours(),previous.getMinutes(),previous.getSeconds(),0);
  } else date.setHours(Math.floor(target.minute/60),target.minute%60,0,0);
  return {at:date.toISOString(),day:''};
}

function sameTodoDue(a:TodoCalendarDue,b:TodoCalendarDue):boolean {
  return a.day===b.day && (a.at===b.at || !!a.at && !!b.at && Date.parse(a.at)===Date.parse(b.at));
}

export function todoCalendarDays(mode:string, anchor:number, weekStart:number):number[] {
  const first=new Date(anchor);
  first.setHours(0,0,0,0);
  if (mode==='day') return [first.getTime()];
  if (mode==='month') first.setDate(1);
  const month=first.getMonth();
  first.setDate(first.getDate()-(first.getDay()-weekStart+7)%7);
  const days:number[]=[];
  do {
    for (let i=0;i<7;i++) { days.push(first.getTime()); first.setDate(first.getDate()+1); }
  } while (mode==='month' && first.getMonth()===month);
  return days;
}

interface ParsedTodoWait {
  wait: TodoWait;
  unknownKind: string;
}

function parseTodoWait(raw: Record<string, Object>): ParsedTodoWait | undefined {
  const kindRaw = Wire.text(raw, 'kind').trim();
  if (!kindRaw) return undefined;
  const parsedKind = parseTodoWaitKind(kindRaw);
  const knownKind = typeof parsedKind === 'string' ? parsedKind : 'parked';
  const unknownKind = typeof parsedKind === 'string' ? '' : kindRaw;

  const blockersRaw = raw['pr_blockers'];
  const blockers: TodoWaitPRBlocker[] = [];
  if (Array.isArray(blockersRaw)) {
    for (const item of blockersRaw as Object[]) {
      if (typeof item !== 'string') continue;
      const parsedBlocker = parseTodoWaitPRBlocker(item as string);
      if (typeof parsedBlocker === 'string') blockers.push(parsedBlocker);
    }
  }

  let contact: TodoWaitContact | undefined = undefined;
  const contactObj = Wire.object(raw, 'contact');
  const channel = Wire.text(contactObj, 'channel').trim();
  if (channel) {
    contact = {
      channel: channel,
      sender: Wire.text(contactObj, 'sender') || undefined,
      sent_at: Wire.text(contactObj, 'sent_at') || undefined,
      url: Wire.text(contactObj, 'url') || undefined,
      request: Wire.text(contactObj, 'request') || undefined
    };
  }

  const wait: TodoWait = {
    kind: knownKind,
    waiting_on: Wire.text(raw, 'waiting_on') || undefined,
    reason: Wire.text(raw, 'reason') || undefined,
    wake_condition: Wire.text(raw, 'wake_condition') || undefined,
    wake_at: Wire.text(raw, 'wake_at') || undefined,
    until: Wire.text(raw, 'until') || undefined,
    since: Wire.text(raw, 'since') || undefined,
    url: Wire.text(raw, 'url') || undefined,
    handoff_todo_id: Wire.text(raw, 'handoff_todo_id') || undefined,
    contact: contact,
    pr_blockers: blockers.length ? blockers : undefined,
    step: Wire.text(raw, 'step') || undefined
  };
  return { wait: wait, unknownKind: unknownKind };
}

export class TodoWorkspaceItem {
  id: string = '';
  title: string = '';
  notes: string = '';
  manager: string = '';
  origin: string = '';
  planId: string = '';
  doneAt: string = '';
  dueAt: string = '';
  dueDate: string = '';
  due: number = Number.NaN;
  dueBy: number = Number.NaN;
  createdAt: string = '';
  updatedAt: string = '';
  reopenedAt: string = '';
  doneBy: string = '';
  doneReason: string = '';
  startError: string = '';
  project: string = '';
  priority: string = '';
  partOf: string = '';
  hubView: TodoHubRow | undefined;
  wait: TodoWait | undefined;
  waitUnknownKind: string = '';
  stage: string = '';
  stageKind: string = '';
  prUrl: string = '';
  kind: string = '';
  agents: TodoWorkspaceAgent[] = [];
  spaces: TodoWorkspaceSpace[] = [];
  ask: Record<string, Object> = {};
  supervision: Record<string, Object> = {};
  delivery: Record<string, Object> = {};
  check: Record<string, Object> = {};
  thread: Record<string, Object> = {};
  source: Record<string, Object> = {};
  brief: Record<string, Object> = {};
  lineage: Record<string, Object>[] = [];
  raw: string = '{}';

  constructor(data: Record<string, Object> = {}, manager: string = '') {
    this.id = Wire.text(data, 'id');
    this.title = Wire.text(data, 'title');
    this.notes = Wire.text(data, 'notes');
    this.manager = Wire.text(data, 'manager', manager);
    this.origin = Wire.text(data, 'origin');
    this.planId = Wire.text(data, 'plan_id');
    this.doneAt = Wire.text(data, 'done_at');
    this.dueAt=Wire.text(data,'due_at'); this.dueDate=Wire.text(data,'due_date');
    this.due=Date.parse(this.dueAt); this.dueBy=this.due;
    if (!Number.isFinite(this.due) && /^\d{4}-\d{2}-\d{2}$/.test(this.dueDate)) {
      const parts=this.dueDate.split('-').map((n:string) => Number(n));
      const day=new Date(0); day.setFullYear(parts[0],parts[1]-1,parts[2]); day.setHours(0,0,0,0);
      if (day.getFullYear() === parts[0] && day.getMonth() === parts[1]-1 && day.getDate() === parts[2]) {
        this.due=day.getTime(); day.setDate(day.getDate()+1); this.dueBy=day.getTime()-1000;
      }
    }
    this.createdAt = Wire.text(data, 'created_at');
    this.updatedAt = Wire.text(data, 'updated_at');
    this.reopenedAt = Wire.text(data, 'reopened_at');
    this.doneBy = Wire.text(data, 'done_by');
    this.doneReason = Wire.text(data, 'done_reason');
    this.startError = Wire.text(data, 'start_error');
    this.project = Wire.text(data, 'project');
    this.priority = Wire.text(data, 'priority');
    this.partOf = Wire.text(data, 'part_of');
    this.hubView = TodoHubRow.read(data);
    const parsedWait = parseTodoWait(Wire.object(data, 'wait'));
    this.wait = parsedWait?.wait;
    this.waitUnknownKind = parsedWait?.unknownKind || '';
    this.stage = Wire.text(data, 'stage');
    this.stageKind = Wire.text(data, 'stage_kind');
    this.prUrl = Wire.text(data, 'pr_url');
    this.kind = Wire.text(data, 'kind');
    this.agents = Wire.rows(data, 'agents').map((item: Record<string, Object>) => new TodoWorkspaceAgent(item));
    this.spaces = Wire.rows(data, 'spaces').map((item: Record<string, Object>) => new TodoWorkspaceSpace(item));
    this.ask = Wire.object(data, 'ask');
    this.supervision = Wire.object(data, 'supervision');
    this.delivery = Wire.object(data, 'delivery');
    this.check = Wire.object(data, 'check');
    this.thread = Wire.object(data, 'thread');
    this.source = Wire.object(data, 'source');
    this.brief = Wire.object(data, 'brief');
    this.lineage = Wire.rows(data, 'lineage');
    this.raw = JSON.stringify(data);
  }

  isDone(): boolean { return !!this.doneAt; }
  activeAgents(): TodoWorkspaceAgent[] { return this.agents.filter((agent: TodoWorkspaceAgent) => agent.isActive()); }
  previousAgents(): TodoWorkspaceAgent[] { return this.agents.filter((agent: TodoWorkspaceAgent) => !agent.isActive()); }
  data(): Record<string, Object> { return Wire.parse(this.raw); }
}

export function todoAgentStateLabel(state: string): string {
  if (state === 'blocked') return 'agent_task_state_needs_you';
  if (state === 'done') return 'agent_task_state_unread';
  if (state === 'followup') return 'todo_state_followup';
  return 'agent_task_state_' + (['needs_you','unread','working','idle','offline'].includes(state) ? state : 'unknown');
}

export const TODO_DUE_FILTERS:string[] = ['all','no_due','today','week','month'];
export const TODO_LIST_ORDERS:string[] = ['recent','due','priority'];

export function todoMatchesDue(todo:TodoWorkspaceItem,filter:string,now:number,firstWeekday:number):boolean {
  if (filter === 'all') return true;
  if (filter === 'no_due') return !Number.isFinite(todo.due);
  if (!Number.isFinite(todo.due)) return false;
  if (!todo.isDone() && todo.dueBy < now) return true;
  const start=new Date(now); start.setHours(0,0,0,0);
  if (filter === 'month') start.setDate(1);
  if (filter === 'week') start.setDate(start.getDate() - (start.getDay() - firstWeekday + 7) % 7);
  const end=new Date(start.getTime());
  if (filter === 'month') end.setMonth(end.getMonth()+1);
  else end.setDate(end.getDate()+(filter === 'week' ? 7 : 1));
  return todo.due >= start.getTime() && todo.due < end.getTime();
}

export class TodoWorkspaceGroup {
  key: string = '';
  title: string = '';
  state: string = '';
  members: TodoWorkspaceItem[] = [];
  phone: TodoPhoneRowContext = new TodoPhoneRowContext();

  constructor(key: string, title: string, state: string, members: TodoWorkspaceItem[]) {
    this.key = key;
    this.title = title;
    this.state = state;
    this.members = members;
  }

  get lead(): TodoWorkspaceItem { return this.members[0] || new TodoWorkspaceItem(); }
  related(): TodoWorkspaceItem[] { return this.members.slice(1); }
}

export class TodoWorkspaceSection {
  key: string = '';
  title: string = '';
  rows: TodoWorkspaceGroup[] = [];

  constructor(key: string, title: string, rows: TodoWorkspaceGroup[]) {
    this.key = key;
    this.title = title;
    this.rows = rows;
  }
}

export class TodoWorkspaceSpaceChoice {
  machineId: string = '';
  space: string = '';
  label: string = '';
  constructor(machineId: string, space: string, label: string) {
    this.machineId = machineId;
    this.space = space;
    this.label = label;
  }
}

function todoAgentSession(agents: TodoWorkspaceAgent[], sessions: ResourceRow[], agent: TodoWorkspaceAgent): ResourceRow | undefined {
  return sessions.find((session: ResourceRow) => session.kind === 'session' &&
    session.machine === agent.machineId && (session.session || session.id) === agent.sessionId);
}

function askIsOpen(todo: TodoWorkspaceItem): boolean {
  return !todo.isDone() && !!Wire.text(todo.ask, 'verdict') && !!Wire.text(todo.ask, 'asked_at');
}

function acceptanceStands(todo: TodoWorkspaceItem): boolean {
  if (Wire.text(todo.supervision, 'state') !== 'accepted') return false;
  const reviews = Wire.rows(todo.supervision, 'reviews');
  return !!reviews.length && Wire.text(reviews[reviews.length - 1], 'verdict') === 'accepted';
}

/** The native six-state view, projected from the server's todo and live session fields. */
export function todoWorkspaceStatus(todo: TodoWorkspaceItem, sessions: ResourceRow[]): string {
  // A failed task start is stored as done_at + start_error. It still needs the owner's
  // attention until they retry or close it explicitly, so inspect this before done_at.
  if (todo.startError) return 'needs_you';
  if (todo.isDone()) return 'done';

  const askOutcome = Wire.text(todo.ask, 'outcome');
  const active = todo.activeAgents();
  const supervisionState = Wire.text(todo.supervision, 'state');
  const ownerDecision = supervisionState === 'user_check' || supervisionState === 'needs_user' || askIsOpen(todo) && (askOutcome === 'decide' || askOutcome === 'cannot');
  let checking: boolean = false;
  let working: boolean = false;
  let needsYou: boolean = false;
  for (const agent of active) {
    if (agent.startError) continue;
    const session = todoAgentSession(active, sessions, agent);
    if (!session) continue;
    const data = session.data();
    const state = session.state || Wire.text(data, 'state', Wire.text(data, 'status'));
    const turnError = Wire.text(data, 'turn_error');
    const stopped = state === 'unread' || state === 'idle';
    const interrupted = !!turnError && stopped && Wire.text(data, 'turn_error_kind') !== 'usage_limit';
    if (state === 'needs_you' || interrupted) needsYou = true;
    else if (!!turnError && stopped && Wire.text(data, 'turn_error_kind') === 'usage_limit') checking = true;
    else if (state === 'unread') checking = true;
    else {
      const work = Wire.object(data, 'work');
      const waitingOnWork = Wire.text(work, 'phase') === 'blocked' &&
        ['agent', 'external', 'transient'].includes(Wire.text(work, 'on'));
      if (state === 'working' || state === 'waiting' || Wire.bool(data, 'starting') || Wire.bool(data, 'background_working') || waitingOnWork) working = true;
    }
  }
  if (needsYou) return 'needs_you';
  if (ownerDecision) return 'needs_you';
  if (working && !checking) return 'working';
  if (acceptanceStands(todo) || Wire.text(todo.delivery, 'state') === 'live') return 'done';
  if (checking || supervisionState === 'pending_review' || askIsOpen(todo)) return 'checking';
  const deliveryState = Wire.text(todo.delivery, 'state');
  const hasOpenPr = deliveryState === 'pr' || !!todo.prUrl && ['none', ''].includes(deliveryState) &&
    !['merge', 'deploy', 'verify'].includes(todo.stage);
  if (hasOpenPr || ['merged', 'off'].includes(deliveryState) || todo.prUrl && ['merge', 'deploy', 'verify'].includes(todo.stage)) return 'followup';
  if (working) return 'working';
  return active.length ? 'followup' : 'not_started';
}

function normalizedTitle(value: string): string {
  return value.toLowerCase().replace(/[\s\p{P}\p{S}]+/gu, '');
}

function relationKeys(todo: TodoWorkspaceItem): string[] {
  const keys: string[] = [];
  if (todo.planId) keys.push('plan:' + todo.planId);
  const pr = todo.prUrl.trim().replace(/[#?].*$/, '').replace(/\/+$/, '').toLowerCase();
  if (pr) keys.push('pr:' + pr);
  const issue = todo.title.match(/#([0-9]{2,7})\b/);
  if (issue) keys.push('issue:' + issue[1]);
  const title = normalizedTitle(todo.title);
  if (title.length >= 6) keys.push('title:' + title);
  return keys;
}

function related(left: TodoWorkspaceItem, right: TodoWorkspaceItem): boolean {
  if (left.manager!==right.manager || left.id===right.id || left.isDone()!==right.isDone() ||
    !left.isDone() && (left.kind==='concern' || right.kind==='concern')) return false;
  const rightKeys = relationKeys(right);
  return relationKeys(left).some((key: string) => rightKeys.includes(key));
}

export function relatedTodoMembers(items: TodoWorkspaceItem[], todo: TodoWorkspaceItem): TodoWorkspaceItem[] {
  const matching: TodoWorkspaceItem[] = [];
  const seen: Set<string> = new Set([todo.id]);
  const queue: TodoWorkspaceItem[] = [todo];
  while (queue.length) {
    const current = queue.shift();
    if (!current) continue;
    for (const candidate of items) {
      if (seen.has(candidate.id) || !related(current, candidate)) continue;
      seen.add(candidate.id);
      queue.push(candidate);
      matching.push(candidate);
    }
  }
  matching.sort((left: TodoWorkspaceItem, right: TodoWorkspaceItem) => compareTodoRecent(left, right));
  return [todo].concat(matching.filter((item: TodoWorkspaceItem) => item.id !== todo.id));
}

function compareTodoRecent(left: TodoWorkspaceItem, right: TodoWorkspaceItem): number {
  const a = left.updatedAt || left.createdAt;
  const b = right.updatedAt || right.createdAt;
  if (a === b) return left.id.localeCompare(right.id);
  return a < b ? 1 : -1;
}

function statusRank(value: string): number {
  const index = TODO_STATUS_ORDER.indexOf(value);
  return index < 0 ? TODO_STATUS_ORDER.length : index;
}

function groupLead(items: TodoWorkspaceItem[], sessions: ResourceRow[]): TodoWorkspaceItem[] {
  return items.slice().sort((left: TodoWorkspaceItem, right: TodoWorkspaceItem) => {
    const byState = statusRank(todoWorkspaceStatus(left, sessions)) - statusRank(todoWorkspaceStatus(right, sessions));
    return byState || compareTodoRecent(left, right);
  });
}

function managerQuery(managerId: string): string { return managerId ? '?manager=' + segment(managerId) : ''; }

function pathPart(value: string): string { return segment(value); }

function updateRecord(todo: TodoWorkspaceItem, data: Record<string, Object>, manager: string): TodoWorkspaceItem {
  const merged = Wire.payload([], []);
  Object.keys(todo.data()).forEach((key: string) => { merged[key] = todo.data()[key]; });
  Object.keys(data).forEach((key: string) => { merged[key] = data[key]; });
  if (!Wire.text(merged, 'id')) merged['id'] = todo.id;
  if (!Wire.text(merged, 'manager')) merged['manager'] = manager;
  return new TodoWorkspaceItem(merged, manager);
}

@ObservedV2
export class TodoWorkspace {
  @Trace items: TodoWorkspaceItem[] = [];
  @Trace sessions: ResourceRow[] = [];
  @Trace machines: ResourceRow[] = [];
  @Trace conversations: ResourceRow[] = [];
  @Trace search: string = '';
  @Trace statusFilter: string = 'all';
  @Trace groupBy: string = 'status';
  @Trace hubCounts: TodoHubCounts | undefined = undefined;
  @Trace hubChoice: string = 'all';
  @Trace hubUnfolded: string[] = [];
  @Trace dueFilter:string = 'all';
  @Trace order:string = 'recent';
  @Trace calendarOpen:boolean = false;
  @Trace calendarMode:string = 'day';
  @Trace calendarAnchor:number = Date.now();
  @Trace movingItem:TodoWorkspaceItem|undefined = undefined;
  @Trace undoTodo:string = '';
  @Trace calendarReadOnly:boolean = false;
  @Trace calendarReady:boolean = false;
  private movedFrom:TodoCalendarDue = {at:'',day:''};
  private movedTo:TodoCalendarDue = {at:'',day:''};
  private movedManager:string = '';
  private moveRevision:number = 0;
  @Trace selected: TodoWorkspaceItem = new TodoWorkspaceItem();
  @Trace loading: boolean = false;
  @Trace saving: boolean = false;
  @Trace seenPreview:TodoSeenChange|undefined = undefined;
  @Trace seenUndo:TodoSeenChange|undefined = undefined;
  private seenRequest:Promise<boolean>|undefined = undefined;
  @Trace error: string = '';
  @Trace writeState: string = '';
  @Trace titleDraft: string = '';
  @Trace notesDraft: string = '';
  @Trace draftReady: boolean = false;
  @Trace targetGone: boolean = false;
  @Trace editorOpen: boolean = false;
  @Trace detailReadOnly: boolean = false;
  @Trace detailProjects: ResourceRow[] = [];
  @Trace selectedAgentKey: string = '';
  @Trace selectedSpaceKey: string = '';
  @Trace projects: ResourceRow[] = [];

  private rootClient: ClientModel | undefined;
  private ownerClient: ClientModel | undefined;
  private boundRepo: HubRepository | undefined;
  private active: boolean = false;
  private revision: number = 0;
  private listRequestRevision: number = 0;
  private latestListReadRevision: number = 0;
  private latestListRead: Promise<void> | undefined;
  private rootGeneration: number = 0;
  private rootScope: string = '';
  private ownerGeneration: number = 0;
  private ownerScope: string = '';
  private groupId: string = '';
  private managerId: string = '';
  private draftWrites: Promise<void> = Promise.resolve();
  private initialRow: ResourceRow | undefined;
  private listMode: boolean = true;

  async open(client: ClientModel, row?: ResourceRow, list: boolean = true): Promise<void> {
    const revision = ++this.revision, openListRevision = ++this.listRequestRevision;
    this.active = true; this.initialRow = row; this.listMode = list; this.detailReadOnly=false; this.detailProjects=[];
    this.rootClient = client;
    this.ownerClient = undefined;
    this.boundRepo = undefined;
    this.rootGeneration = client.connectionGeneration();
    this.rootScope = client.scope();
    this.groupId = client.profile.id;
    this.loading = true;
    this.saving = false;
    this.error = '';
    this.writeState = '';
    this.items = [];
    this.hubCounts = undefined;
    this.hubChoice = 'all';
    this.hubUnfolded = [];
    this.groupBy = 'status';
    this.statusFilter = 'all';
    this.search = '';
    this.dueFilter = 'all';
    this.order = 'recent';
    this.calendarOpen=false; this.calendarMode='day'; this.calendarAnchor=Date.now(); this.calendarReady=false;
    this.resetCalendarMove();
    this.clearSeenActions();
    this.sessions = [];
    this.machines = [];
    this.conversations = [];
    this.projects = [];
    this.selected = new TodoWorkspaceItem();
    this.titleDraft = '';
    this.notesDraft = '';
    this.draftReady = false;
    this.targetGone = false;
    try {
      let owner = client;
      if (row && row.manager && row.manager !== client.profile.manager) owner = await client.viewFor(row);
      if (!this.loadCurrent(client, revision)) return;
      this.ownerClient = owner;
      this.ownerGeneration = owner.connectionGeneration();
      this.ownerScope = owner.scope();
      this.managerId = owner.profile.manager;
      await this.loadView(owner, revision);
      if (!this.loadCurrent(client, revision)) return;
      this.boundRepo = owner.repo();
      this.syncSessions(owner);
      const refreshRevision = this.listRequestRevision + 1;
      await this.refresh();
      if (!this.loadCurrent(client, revision)) return;
      let awaitedRevision = refreshRevision;
      while (this.listRequestRevision > awaitedRevision) {
        const latestRead = this.latestListRead, latestRevision = this.latestListReadRevision;
        if (!latestRead || latestRevision !== this.listRequestRevision) break;
        awaitedRevision = latestRevision;
        await latestRead;
        if (!this.loadCurrent(client, revision)) return;
      }
      if (this.listRequestRevision === refreshRevision) this.loading = false;
    } catch (failure) {
      if (this.loadCurrent(client, revision) && this.listRequestRevision === openListRevision) {
        this.error = this.errorCode(failure as Error);
        this.loading = false;
      }
    }
  }

  close(): void {
    this.resetCalendarMove();
    this.clearSeenActions();
    this.calendarReady=false;
    this.active = false;
    this.revision++;
    this.listRequestRevision++;
    this.latestListReadRevision = 0;
    this.latestListRead = undefined;
    this.rootClient = undefined;
    this.ownerClient = undefined;
    this.boundRepo = undefined;
    this.saving = false;
    this.items = [];
    this.hubCounts = undefined;
    this.sessions = [];
    this.machines = [];
    this.conversations = [];
    this.projects = [];
    this.selected = new TodoWorkspaceItem();
    this.draftReady = false;
    this.targetGone = false;
  }

  update(client: ClientModel): void {
    if (!this.active || client !== this.rootClient) return;
    const owner = this.ownerClient || client;
    this.syncSessions(owner);
  }

  private syncSessions(client: ClientModel): void {
    this.sessions = client.sessions.filter((row: ResourceRow) => row.kind === 'session' && row.manager === client.profile.manager);
    this.machines = client.machines.filter((row: ResourceRow) => row.kind === 'machine' && row.manager === client.profile.manager);
    this.conversations = client.conversations.filter((row: ResourceRow) => row.manager === client.profile.manager);
    this.projects = client.knowledge.filter((row: ResourceRow) => row.kind === 'knowledge' && row.path === 'projects' && row.manager === client.profile.manager);
  }

  private async loadView(client: ClientModel, revision: number): Promise<void> {
    try {
      await this.draftWrites;
      const saved = Wire.parse(await client.loadView('todo-workspace'));
      if (this.ownerClient !== client || !this.loadCurrent(this.rootClient, revision)) return;
      this.search = Wire.text(saved, 'search');
      this.statusFilter = ['all'].concat(TODO_STATES).includes(Wire.text(saved, 'status')) ? Wire.text(saved, 'status', 'all') : 'all';
      this.groupBy = ['status', 'project', 'thread', 'hub'].includes(Wire.text(saved, 'group')) ? Wire.text(saved, 'group', 'status') : 'status';
      this.hubUnfolded = Wire.strings(saved, 'hubUnfolded').filter((key: string) => TODO_LIST_SECTIONS.includes(key as TodoListSection) && ['someday', TODO_LIST_SECTIONS[1], 'done'].includes(key));
      const due=Wire.text(saved,'due'),order=Wire.text(saved,'order');
      this.dueFilter=TODO_DUE_FILTERS.includes(due) ? due : 'all'; this.order=TODO_LIST_ORDERS.includes(order) ? order : 'recent';
      this.calendarOpen=Wire.bool(saved,'calendar');
      const mode=Wire.text(saved,'calendarMode'),date=dayFromKey(Wire.text(saved,'calendarDate'));
      this.calendarMode=['day','week','month'].includes(mode) ? mode : 'day';
      this.calendarAnchor=Number.isFinite(date) ? date : Date.now();
    } catch (_) { if (this.ownerClient === client && this.loadCurrent(this.rootClient, revision)) this.error = 'draft_restore_failed'; }
    finally { if (this.ownerClient===client && this.loadCurrent(this.rootClient,revision)) this.calendarReady=true; }
  }

  private saveViewLater(): void {
    const client=this.ownerClient,root=this.rootClient;
    if (!client || !root || !this.active) return;
    const scope=this.ownerScope,rootScope=this.rootScope,group=this.groupId,manager=this.managerId;
    const current=():boolean => client.scope()===scope && root.scope()===rootScope && root.profile.id===group && client.profile.manager===manager;
    if (!current()) return;
    const value = JSON.stringify(Wire.payload(['search','status','group','due','order','hubUnfolded','calendar','calendarMode','calendarDate'],
      [this.search,this.statusFilter,this.groupBy,this.dueFilter,this.order,this.hubUnfolded,this.calendarOpen,this.calendarMode,todoDayKey(new Date(this.calendarAnchor))]));
    const epoch=client.viewRevision('todo-workspace');
    this.draftWrites = this.draftWrites.catch(() => {}).then(async () => {
      if (current()) await client.saveView('todo-workspace',value,epoch);
    }).catch(() => { if (current() && this.active && this.ownerClient===client) this.error='draft_save_failed'; });
  }

  setCalendar(open:boolean,mode:string=this.calendarMode,anchor:number=this.calendarAnchor):void {
    if (!['day','week','month'].includes(mode) || !Number.isFinite(new Date(anchor).getTime())) return;
    this.calendarOpen=open; this.calendarMode=mode; this.calendarAnchor=anchor; this.saveViewLater();
  }

  private resetCalendarMove():void {
    this.moveRevision++; this.movingItem=undefined; this.undoTodo=''; this.movedManager=''; this.calendarReadOnly=false;
  }

  canMove(todo:TodoWorkspaceItem):boolean {
    const data=todo.data();
    return !this.calendarReadOnly && !this.saving && !this.movingItem && !!this.capture(todo) && this.items.includes(todo) &&
      !Wire.bool(data,'read_only',Wire.bool(data,'readonly')) && (!Wire.has(data,'can_write') || Wire.bool(data,'can_write'));
  }

  calendarRows():TodoWorkspaceItem[] {
    const pending=this.movingItem;
    return pending ? this.items.map((row:TodoWorkspaceItem) => row.id===pending.id && row.manager===pending.manager ? pending : row) : this.items;
  }

  async reschedule(todo:TodoWorkspaceItem,due:TodoCalendarDue,undo:boolean=false):Promise<boolean> {
    const owner=this.capture(todo);
    if (!owner || !this.canMove(todo) || (!due.at && !Number.isFinite(dayFromKey(due.day))) ||
      (!!due.at && (!!due.day || !Number.isFinite(Date.parse(due.at))))) return false;
    const old:TodoCalendarDue={at:todo.dueAt,day:todo.dueDate};
    if (sameTodoDue(old,due)) return false;
    const path='/api/v1/todos/'+pathPart(todo.id)+managerQuery(owner.managerId);
    if (owner.client.operationBlocked(path)) { this.error='todo_write_pending'; return false; }
    const run=++this.moveRevision,current=():boolean=>this.moveRevision===run && this.owns(owner,false);
    this.error=''; this.movingItem=updateRecord(todo,Wire.payload(['due_at','due_date'],[due.at,due.day]),owner.managerId);
    try {
      const list=await owner.repo.call('/api/v1/todos'+managerQuery(owner.managerId));
      if (!current()) return false;
      const live=Wire.rows(list,'todos').find((row:Record<string,Object>) => Wire.text(row,'id')===todo.id && Wire.text(row,'manager',owner.managerId)===owner.managerId);
      const row=this.items.find((item:TodoWorkspaceItem)=>item.id===todo.id && (item.manager || owner.managerId)===owner.managerId);
      if (!live || !row) { this.error='todo_gone'; return false; }
      if (!sameTodoDue(old,{at:Wire.text(live,'due_at'),day:Wire.text(live,'due_date')}) || !sameTodoDue(old,{at:row.dueAt,day:row.dueDate})) {
        this.upsert(live,owner.managerId); this.error='request_updated'; return false;
      }
      if (Wire.bool(live,'read_only',Wire.bool(live,'readonly')) || Wire.has(live,'can_write') && !Wire.bool(live,'can_write')) {
        this.upsert(live,owner.managerId); this.error='permission_denied'; return false;
      }
      const result=await this.write(path,'PATCH',due.at ? Wire.payload(['due_at'],[due.at]) : Wire.payload(['due_date'],[due.day]),row,true,
        ():boolean => current() && this.items.some((a:TodoWorkspaceItem)=>a.id===todo.id && a.manager===row.manager && sameTodoDue(old,{at:a.dueAt,day:a.dueDate})));
      if (!result || !current()) {
        if (current() && this.error==='permission_denied') this.calendarReadOnly=true;
        if (current() && (this.writeState==='unknown' || this.writeState==='pending')) await this.refresh();
        return false;
      }
      const updated=Wire.object(result,'todo');
      if (Wire.text(updated,'id')!==todo.id || Wire.text(updated,'manager',owner.managerId)!==owner.managerId) { this.error='request_updated'; return false; }
      const accepted=this.upsert(updated,owner.managerId);
      if (!sameTodoDue(due,{at:accepted.dueAt,day:accepted.dueDate})) { this.error='request_updated'; return false; }
      this.undoTodo=undo ? '' : todo.id; this.movedManager=owner.managerId; this.movedFrom=old; this.movedTo=due; return true;
    } catch (failure) { if (current()) this.error=this.errorCode(failure as Error); return false;
    } finally { if (this.moveRevision===run) this.movingItem=undefined; }
  }

  movedItem():TodoWorkspaceItem|undefined {
    return this.items.find((row:TodoWorkspaceItem)=>row.id===this.undoTodo && (row.manager || this.managerId)===this.movedManager);
  }

  async undoCalendarMove():Promise<boolean> {
    const item=this.movedItem();
    if (!item || !sameTodoDue(this.movedTo,{at:item.dueAt,day:item.dueDate})) { this.undoTodo=''; this.error='request_updated'; return false; }
    return this.reschedule(item,this.movedFrom,true);
  }

  setSearch(value: string): void { this.search = value; this.saveViewLater(); }
  setStatusFilter(value: string): void {
    if (!['all'].concat(TODO_STATES).includes(value)) return;
    this.statusFilter = value; this.saveViewLater();
  }
  setDueFilter(value:string):void {
    if (TODO_DUE_FILTERS.includes(value)) { this.dueFilter=value; this.saveViewLater(); }
  }
  setOrder(value:string):void {
    if (TODO_LIST_ORDERS.includes(value)) { this.order=value; this.saveViewLater(); }
  }
  setGroupBy(value: string): void {
    if (!['status', 'project', 'thread'].includes(value) && !(value === 'hub' && this.hubCounts)) return;
    this.groupBy = value; this.saveViewLater();
  }

  effectiveGroup(): string { return this.groupBy === 'hub' && !this.hubCounts ? 'status' : this.groupBy; }

  setHubChoice(choice: string): void {
    if (this.effectiveGroup() === 'hub' && TODO_HUB_CHOICES.includes(choice)) this.hubChoice = choice;
  }

  toggleHubFold(key: string): void {
    if (!['someday', TODO_LIST_SECTIONS[1], 'done'].includes(key)) return;
    this.hubUnfolded = this.hubUnfolded.includes(key) ? this.hubUnfolded.filter((value: string) => value !== key) : this.hubUnfolded.concat([key]);
    this.saveViewLater();
  }

  private capture(todo: TodoWorkspaceItem | undefined): TodoOwnerSnapshot | undefined {
    const root = this.rootClient, client = this.ownerClient, repo = this.boundRepo;
    if (!this.active || !root || !client || !repo || !root.connected || !client.connected) return undefined;
    if (root.connectionGeneration() !== this.rootGeneration || root.scope() !== this.rootScope ||
      client.connectionGeneration() !== this.ownerGeneration || client.scope() !== this.ownerScope) return undefined;
    try {
      if (root.repo() === undefined || client.repo() !== repo) return undefined;
    } catch (_) { return undefined; }
    if (todo && todo.manager && todo.manager !== this.managerId) return undefined;
    return { root: root, client: client, repo: repo, rootGeneration: this.rootGeneration, rootScope: this.rootScope,
      generation: this.ownerGeneration, scope: this.ownerScope, revision: this.revision, groupId: this.groupId,
      managerId: this.managerId, todoId: todo?.id || '', todoManagerId: todo?.manager || this.managerId };
  }

  private ownsScope(owner: TodoOwnerSnapshot): boolean {
    if (!this.active || this.rootClient !== owner.root || this.ownerClient !== owner.client ||
      owner.root.connectionGeneration() !== owner.rootGeneration || owner.root.scope() !== owner.rootScope ||
      owner.client.connectionGeneration() !== owner.generation || owner.client.scope() !== owner.scope ||
      owner.root.profile.id !== owner.groupId || !owner.root.connected || !owner.client.connected) return false;
    try { return owner.client.repo() === owner.repo && this.boundRepo === owner.repo; } catch (_) { return false; }
  }

  private ownsListRequest(owner: TodoOwnerSnapshot, revision: number): boolean {
    return this.listRequestRevision === revision && this.revision === owner.revision && this.ownsScope(owner);
  }

  private invalidateListReads(): void {
    this.listRequestRevision++;
    this.loading = false;
  }

  private rejectGoneTarget(todo: TodoWorkspaceItem): boolean {
    if (!todo.id || !this.targetGone || this.selected.id !== todo.id ||
      (this.selected.manager || this.managerId) !== (todo.manager || this.managerId)) return false;
    this.error = 'todo_gone';
    return true;
  }

  private owns(owner: TodoOwnerSnapshot, requireTodo: boolean = true): boolean {
    if (!this.ownsScope(owner) || this.revision !== owner.revision) return false;
    return !requireTodo || !owner.todoId || (this.selected.id === owner.todoId &&
      (this.selected.manager || owner.managerId) === owner.todoManagerId);
  }

  isSelected(todo: TodoWorkspaceItem): boolean {
    if (this.rejectGoneTarget(todo)) return false;
    const owner = this.capture(todo);
    return !!owner && this.selected.id === todo.id && (this.selected.manager || owner.managerId) === owner.todoManagerId;
  }

  private loadCurrent(client: ClientModel | undefined, revision: number): boolean {
    if (!client || client !== this.rootClient || !this.active || this.revision !== revision) return false;
    return client.connectionGeneration() === this.rootGeneration && client.scope() === this.rootScope;
  }

  private errorCode(error: Error): string {
    if (error instanceof HttpFailure) {
      if (error.resultPending) return 'todo_write_pending';
      if (error.resultUnknown) return 'todo_write_unknown';
      if (error.status === 401 || error.status === 403) return 'permission_denied';
      if (error.status === 404) return 'todo_gone';
      if (error.status === 409 || error.status === 410) return 'request_updated';
    }
    return error.message === 'offline' ? 'offline' : 'failed';
  }

  refresh(): Promise<void> {
    const revision = ++this.listRequestRevision, root = this.rootClient;
    if (!this.active || !root || !root.connected || root.scope() !== this.rootScope) {
      if (revision === this.listRequestRevision) { this.error = 'offline'; this.loading = false; }
      return Promise.resolve();
    }
    if (!this.boundRepo) return this.open(root, this.initialRow, this.listMode);
    const rootScope = root.scope(), rootGeneration = root.connectionGeneration();
    this.loading = true;
    const request = this.readList(revision, root, rootScope, rootGeneration);
    this.latestListReadRevision = revision;
    this.latestListRead = request;
    return request;
  }

  private async readList(revision: number, root: ClientModel, rootScope: string, rootGeneration: number): Promise<void> {
    let ownerSnapshot: TodoOwnerSnapshot | undefined;
    try {
      const previousOwner = this.ownerClient;
      let repositoryCurrent = false;
      if (previousOwner && previousOwner.connected && this.boundRepo) {
        try { repositoryCurrent = previousOwner.repo() === this.boundRepo; } catch (_) { repositoryCurrent = false; }
      }
      if (!previousOwner || !repositoryCurrent || rootGeneration !== this.rootGeneration ||
        previousOwner.connectionGeneration() !== this.ownerGeneration || previousOwner.scope() !== this.ownerScope) {
        const ref = new ResourceRow('owner', '', 'manager');
        ref.manager = this.managerId || this.initialRow?.manager || root.profile.manager;
        const next = await root.viewFor(ref);
        if (revision !== this.listRequestRevision || !this.active || this.rootClient !== root ||
          rootScope !== this.rootScope || !root.owns(rootGeneration, rootScope)) return;
        this.hubCounts = undefined;
        this.resetCalendarMove();
        this.clearSeenActions();
        this.ownerClient = next;
        this.boundRepo = next.repo();
        this.ownerGeneration = next.connectionGeneration();
        this.ownerScope = next.scope();
        this.rootGeneration = rootGeneration;
        this.managerId = next.profile.manager;
        this.syncSessions(next);
      }
      const owner = this.capture(undefined);
      if (!owner) {
        if (revision === this.listRequestRevision) this.error = 'offline';
        return;
      }
      ownerSnapshot = owner;
      const result = await owner.repo.call('/api/v1/todos' + managerQuery(owner.managerId));
      if (!this.ownsListRequest(owner, revision)) return;
      const items = Wire.rows(result, 'todos').map((item: Record<string, Object>) => new TodoWorkspaceItem(item, owner.managerId));
      this.hubCounts = owner.client.features.includes('todo_view') ? TodoHubCounts.read(result) : undefined;
      this.items = items;
      this.syncSessions(owner.client);
      if (this.selected.id) {
        const selectedId = this.selected.id, selectedManager = this.selected.manager || owner.managerId;
        const selected = items.find((item: TodoWorkspaceItem) => item.id === selectedId && (item.manager || owner.managerId) === selectedManager);
        if (selected) {
          this.selected = selected;
          this.targetGone = false;
        } else this.targetGone = true;
      }
      this.error = this.targetGone ? 'todo_gone' : '';
      await this.resumeInitialDetail(owner, revision, items);
    } catch (failure) {
      const currentOwner = ownerSnapshot ? this.ownsScope(ownerSnapshot) :
        this.active && this.rootClient === root && root.owns(rootGeneration, rootScope) && root.scope() === this.rootScope;
      if (revision === this.listRequestRevision && currentOwner)
        this.error = ownerSnapshot ? this.errorCode(failure as Error) : 'offline';
    } finally {
      if (revision === this.listRequestRevision && this.rootClient === root) this.loading = false;
    }
  }

  private async resumeInitialDetail(owner: TodoOwnerSnapshot, revision: number, items: TodoWorkspaceItem[]): Promise<void> {
    const initial = this.initialRow;
    if (this.listMode || !initial || this.editorOpen || !this.ownsListRequest(owner, revision)) return;
    const manager = initial.manager || owner.managerId;
    const target = items.find((item: TodoWorkspaceItem) => item.id === initial.id && (item.manager || owner.managerId) === manager);
    if (!target) {
      if (!this.selected.id || this.selected.id === initial.id) {
        this.targetGone = true;
        this.error = 'todo_gone';
      }
      return;
    }
    this.targetGone = false;
    if (this.selected.id) {
      const selectedManager = this.selected.manager || owner.managerId;
      if (this.selected.id !== target.id || selectedManager !== manager || this.draftReady) return;
    }
    await this.openRow(target);
  }

  async openRow(todo: TodoWorkspaceItem): Promise<void> {
    if (this.rejectGoneTarget(todo)) return;
    const owner = this.capture(undefined);
    if (!owner || !todo.id || todo.manager && todo.manager !== owner.managerId) { this.error = 'offline'; return; }
    this.revision++;
    if (this.selected.id!==todo.id || this.selected.manager!==todo.manager) this.detailReadOnly=false;
    this.selected = todo;
    this.targetGone = false;
    this.titleDraft = todo.title;
    this.notesDraft = todo.notes;
    this.draftReady = false;
    this.error = '';
    const snapshot = this.capture(todo);
    if (!snapshot) return;
    try {
      const saved = Wire.parse(await snapshot.client.loadView(this.draftStorageKey(snapshot, todo)));
      if (!this.owns(snapshot)) return;
      this.titleDraft = Wire.text(saved, 'title', todo.title);
      this.notesDraft = Wire.text(saved, 'notes', todo.notes);
      this.draftReady = true;
    } catch (_) { if (this.owns(snapshot)) { this.draftReady = true; this.error = 'draft_restore_failed'; } }
  }

  startCreate(dueDate:string = '', dueAt:string = ''): void {
    const current = this.capture(undefined);
    if (!current) { this.error = 'offline'; return; }
    this.revision++;
    const snapshot = this.capture(undefined);
    if (!snapshot) { this.error = 'offline'; return; }
    this.selected = new TodoWorkspaceItem(Wire.payload(['manager','due_date','due_at'], [snapshot.managerId,dueDate,dueAt]),snapshot.managerId);
    this.targetGone = false;
    this.titleDraft = '';
    this.notesDraft = '';
    this.draftReady = true;
    this.editorOpen = true;
    this.writeState = '';
    this.restoreDraft(snapshot, this.selected).catch(() => undefined);
  }

  beginEdit(todo: TodoWorkspaceItem): void {
    this.openRow(todo).then(() => { if (this.isSelected(todo)) this.editorOpen = true; });
  }

  closeEditor(): void { this.editorOpen = false; }

  setTitleDraft(value: string): void { this.titleDraft = value; this.persistSelectedDraft(); }
  setNotesDraft(value: string): void { this.notesDraft = value; this.persistSelectedDraft(); }

  private draftIdentity(group:string,manager:string,todo:TodoWorkspaceItem):string[] {
    const key=[group,manager,todo.id || 'new'];
    if (!todo.id && (todo.dueAt || todo.dueDate)) key.push(todo.dueAt || todo.dueDate);
    return key;
  }

  private draftStorageKey(owner: TodoOwnerSnapshot, todo: TodoWorkspaceItem): string {
    return 'todo:draft:' + JSON.stringify(this.draftIdentity(owner.groupId,owner.managerId,todo));
  }

  private async restoreDraft(owner: TodoOwnerSnapshot, todo: TodoWorkspaceItem): Promise<void> {
    try {
      const saved = Wire.parse(await owner.client.loadView(this.draftStorageKey(owner, todo)));
      if (!this.owns(owner, false)) return;
      this.titleDraft = Wire.text(saved, 'title', this.titleDraft);
      this.notesDraft = Wire.text(saved, 'notes', this.notesDraft);
      this.draftReady = true;
    } catch (_) { if (this.owns(owner, false)) { this.draftReady = true; this.error = 'draft_restore_failed'; } }
  }

  private persistSelectedDraft(): void {
    const root = this.rootClient, client = this.ownerClient;
    if (!this.draftReady || !this.active || !root || !client || root.scope() !== this.rootScope || client.scope() !== this.ownerScope) return;
    const rootScope = this.rootScope, scope = this.ownerScope, group = this.groupId, key = 'todo:draft:' + JSON.stringify(this.draftIdentity(this.groupId,this.managerId,this.selected));
    const value = JSON.stringify(Wire.payload(['title', 'notes'], [this.titleDraft, this.notesDraft])), epoch = client.viewRevision(key);
    const current = (): boolean => root.scope() === rootScope && client.scope() === scope && root.profile.id === group;
    this.draftWrites = this.draftWrites.catch(() => {}).then(async () => { if (current()) await client.saveView(key, value, epoch); })
      .catch(() => { if (current()) this.error = 'draft_save_failed'; });
  }

  private async clearDraft(owner: TodoOwnerSnapshot, todo: TodoWorkspaceItem, title: string, notes: string): Promise<void> {
    if (!this.ownsScope(owner)) return;
    const key = this.draftStorageKey(owner, todo);
    const saved = Wire.parse(await owner.client.loadView(key));
    if (!this.ownsScope(owner)) return;
    if (Wire.text(saved, 'title') === title && Wire.text(saved, 'notes') === notes) await owner.client.saveView(key, '');
  }

  private async write(path: string, method: string, body: Record<string, Object>, todo: TodoWorkspaceItem | undefined, listRow:boolean=false,
    extraCheck?:()=>boolean): Promise<Record<string, Object> | undefined> {
    if (todo && this.rejectGoneTarget(todo)) return undefined;
    const owner = this.capture(todo);
    if (!owner) { this.error = 'offline'; return undefined; }
    const admit=():boolean => (!extraCheck || extraCheck()) && this.owns(owner,!!todo && !listRow) && (!listRow || !!todo && this.items.some((item:TodoWorkspaceItem) =>
      item.id === todo.id && (item.manager || owner.managerId) === owner.todoManagerId && item.isDone() === todo.isDone()));
    if (listRow && (this.saving || !todo || !this.items.includes(todo) || !admit())) return undefined;
    if (owner.client.operationBlocked(path)) { this.error = 'todo_write_pending'; return undefined; }
    this.saving = true;
    this.writeState = 'sending';
    this.error = '';
    try {
      const result = listRow || extraCheck ? await owner.repo.call(path,method,body,'',admit) : await owner.repo.call(path,method,body);
      if (!this.owns(owner, !!todo && !listRow)) return undefined;
      if (listRow && todo && !this.items.some((item:TodoWorkspaceItem) => item.id === todo.id && (item.manager || owner.managerId) === owner.todoManagerId)) {
        this.writeState=''; return undefined;
      }
      this.invalidateListReads();
      this.writeState = 'done';
      return result;
    } catch (failure) {
      if (!this.owns(owner, !!todo && !listRow)) return undefined;
      this.writeState = failure instanceof HttpFailure && failure.resultPending ? 'pending' :
        failure instanceof HttpFailure && failure.resultUnknown ? 'unknown' : 'failed';
      const unsupported = !!extraCheck && !listRow && failure instanceof HttpFailure &&
        ([405,501].includes(failure.status) || ['unsupported_field','unknown_field','not_implemented'].includes(failure.code));
      this.error = unsupported ? 'todo_detail_unsupported' : this.errorCode(failure as Error);
      return undefined;
    } finally { if (this.owns(owner, !!todo && !listRow)) this.saving = false; }
  }

  private upsert(data: Record<string, Object>, manager: string): TodoWorkspaceItem {
    const updated = new TodoWorkspaceItem(data, manager);
    const index = this.items.findIndex((item: TodoWorkspaceItem) => item.id === updated.id && (item.manager || manager) === (updated.manager || manager));
    const next = this.items.slice();
    if (index >= 0) next[index] = updated; else next.unshift(updated);
    this.items = next;
    if (this.selected.id === updated.id && (this.selected.manager || manager) === (updated.manager || manager)) this.selected = updated;
    this.refreshHubCountsAfterMutation();
    return updated;
  }

  private replace(todo: TodoWorkspaceItem): void {
    const next = this.items.slice();
    const index = next.findIndex((item: TodoWorkspaceItem) => item.id === todo.id);
    if (index >= 0) next[index] = todo; else next.unshift(todo);
    this.items = next;
    if (this.selected.id === todo.id) this.selected = todo;
    this.refreshHubCountsAfterMutation();
  }

  private refreshHubCountsAfterMutation(): void {
    // Mutation acknowledgements contain a row or an OK, never a count snapshot.
    if (!this.hubCounts) return;
    this.hubCounts = undefined;
    this.refresh().catch(() => undefined);
  }

  async save(): Promise<boolean> {
    if (this.selected.id && this.rejectGoneTarget(this.selected)) return false;
    const title = this.titleDraft.trim();
    if (!title) { this.error = 'todo_title_required'; return false; }
    const todo = this.selected, manager = this.managerId;
    const creating = !todo.id;
    const owner = this.capture(creating ? undefined : todo);
    if (!owner) { this.error = 'offline'; return false; }
    const path = '/api/v1/todos' + (creating ? '' : '/' + pathPart(todo.id)) + managerQuery(manager);
    const body = Wire.payload(['title', 'notes'], [title, this.notesDraft]);
    if (creating && Number.isFinite(todo.due)) {
      if (todo.dueAt) body['due_at']=todo.dueAt;
      else if (todo.dueDate) body['due_date']=todo.dueDate;
    }
    const result = await this.write(path, creating ? 'POST' : 'PATCH', body, creating ? undefined : todo);
    if (!result || !this.owns(owner, !creating)) return false;
    const data = Wire.object(result, 'todo');
    if (!Wire.text(data, 'id')) { this.error = 'failed'; return false; }
    const updated = this.upsert(data, manager);
    this.selected = updated;
    this.editorOpen = false;
    this.titleDraft = updated.title;
    this.notesDraft = updated.notes;
    this.draftReady = true;
    try { await this.clearDraft(owner, todo, title, this.notesDraft); } catch (_) { this.error = 'draft_save_failed'; }
    return true;
  }

  openTaskStart(recovery:TaskStartRecovery, visible:()=>boolean):void {
    const todo=this.selected, owner=this.capture(todo);
    recovery.close();
    if (!owner || !todo.id || !todo.origin || (this.initialRow?.group && this.initialRow.group!==owner.client.profile.id) ||
      (!todo.startError && todo.agents.length)) return;
    const current=():boolean=>visible() && this.owns(owner) && !this.targetGone;
    recovery.openTodo(owner.client,()=>this.selected.data(),current,()=>current() && this.detailEditable(),async()=>{
      if (current()) await this.refresh();
    });
  }

  detailEditable(): boolean {
    const todo=this.selected, data=todo.data();
    return !!todo.id && !!this.capture(todo) && !this.targetGone && !this.detailReadOnly &&
      !Wire.bool(data,'read_only',Wire.bool(data,'readonly')) && (!Wire.has(data,'can_write') || Wire.bool(data,'can_write'));
  }

  async loadDetailProjects(): Promise<void> {
    const todo=this.selected, owner=this.capture(todo);
    if (!todo.id || !owner) return;
    try {
      const rows=await owner.repo.knowledge('projects');
      if (!this.owns(owner)) return;
      this.detailProjects=rows.filter((row: ResourceRow) => (!row.manager || row.manager===owner.managerId) &&
        !Wire.text(row.data(),'extra').endsWith('ignored'));
    } catch (_) { if (this.owns(owner)) this.detailProjects=this.projects; }
  }

  async updateInlineDetail(todo: TodoWorkspaceItem, body: Record<string,Object>): Promise<boolean> {
    const owner=this.capture(todo), keys=Object.keys(body);
    if (!owner || !this.owns(owner) || !this.detailEditable() || this.saving || !keys.length ||
      !keys.every((key:string) => ['title','due_at','due_date','remind_at','priority','project'].includes(key))) return false;
    const path='/api/v1/todos/'+pathPart(todo.id)+managerQuery(owner.managerId);
    const result=await this.write(path,'PATCH',body,todo,false,():boolean => this.detailEditable());
    if (!this.owns(owner)) return false;
    if (!result) { if (this.error==='permission_denied') this.detailReadOnly=true; return false; }
    const data=Wire.object(result,'todo');
    if (Wire.text(data,'id')!==todo.id || Wire.text(data,'manager',owner.managerId)!==owner.managerId) { this.error='request_updated'; return false; }
    const updated=this.upsert(data,owner.managerId);
    if (!this.editorOpen && this.titleDraft===todo.title) this.titleDraft=updated.title;
    if (!todoDetailPatchAccepted(body,data)) { this.error='todo_detail_unsupported'; return false; }
    return true;
  }

  private clearSeenActions():void {
    this.seenPreview=undefined; this.seenUndo=undefined; this.seenRequest=undefined;
  }

  supportsSeen():boolean { return !!this.ownerClient?.features.includes('todo_seen'); }

  showsSeenOnMember(todo:TodoWorkspaceItem):boolean {
    return this.supportsSeen() && phoneTodoSeenCanFile(todo) && this.seenPlacement(todo).section==='just_done';
  }

  private seenPlacement(todo:TodoWorkspaceItem):TodoPhonePlacement {
    const superseded=!!todo.startError && todo.isDone() && relatedTodoMembers(this.items,todo).some((row:TodoWorkspaceItem)=>row.isDone() && !row.startError);
    return new TodoPhoneSections(this.sessions,Date.now()).place(todo,superseded);
  }

  markSeen(todo:TodoWorkspaceItem, opened:boolean=false):Promise<boolean> {
    if (!this.supportsSeen() || this.saving || this.seenRequest || !this.items.includes(todo) || !this.capture(todo)) return Promise.resolve(false);
    const place=this.seenPlacement(todo);
    if (place.section!=='just_done' || !opened && !phoneTodoSeenCanFile(todo)) return Promise.resolve(false);
    const change:TodoSeenChange={id:todo.id,manager:todo.manager || this.managerId,
      stamp:new Date(Math.max(Date.now(),place.last)).toISOString(),otherOpen:todo.kind==='concern' && !todo.isDone()};
    this.seenPreview=change;
    this.seenUndo=opened ? undefined : change;
    const work:Promise<boolean>=this.sendSeen(todo,change,true,opened).finally(()=>{
      if (this.seenRequest===work) this.seenRequest=undefined;
    });
    this.seenRequest=work;
    return work;
  }

  async undoSeen():Promise<boolean> {
    const previous=this.seenUndo;
    if (!previous) return false;
    if (this.seenRequest) await this.seenRequest;
    if (this.seenUndo!==previous || this.saving || !this.supportsSeen()) return false;
    const todo=this.items.find((row:TodoWorkspaceItem)=>row.id===previous.id && (row.manager || this.managerId)===previous.manager);
    if (!todo || Wire.text(todo.data(),'seen_at')!==previous.stamp) { this.seenUndo=undefined; this.error='request_updated'; return false; }
    this.seenUndo=undefined;
    const change:TodoSeenChange={id:previous.id,manager:previous.manager,stamp:'',otherOpen:previous.otherOpen};
    this.seenPreview=change;
    return this.sendSeen(todo,change,false,false,previous.stamp);
  }

  private async sendSeen(todo:TodoWorkspaceItem,change:TodoSeenChange,mark:boolean,opened:boolean,previousStamp:string=''):Promise<boolean> {
    const owner=this.capture(todo);
    let accepted=false;
    try {
      if (!owner) return false;
      const current=():TodoWorkspaceItem|undefined=>this.items.find((row:TodoWorkspaceItem)=>row.id===change.id && (row.manager || owner.managerId)===change.manager);
      const admit=():boolean=>{
        const row=current();
        return !!row && this.supportsSeen() && (mark ? this.seenPlacement(row).section==='just_done' &&
          (opened || phoneTodoSeenCanFile(row)) : Wire.text(row.data(),'seen_at')===previousStamp);
      };
      const path='/api/v1/todos/'+pathPart(todo.id)+'/seen'+managerQuery(change.manager);
      const result=await this.write(path,mark?'PUT':'DELETE',Wire.payload([],[]),todo,!opened,admit);
      const row=current();
      if (!result || !row || !this.owns(owner,opened)) return false;
      const ack=Wire.object(result,'todo'),stamp=Wire.text(ack,'seen_at');
      if (Wire.text(ack,'id')!==todo.id || Wire.text(ack,'manager',change.manager)!==change.manager ||
        (mark ? !Number.isFinite(Date.parse(stamp)) : !!stamp)) { this.error='request_updated'; return false; }
      // The acknowledgement can omit list-only fields such as the open ask.
      const data=row.data(); data['seen_at']=stamp;
      change.stamp=stamp;
      this.invalidateListReads();
      this.upsert(data,change.manager);
      accepted=true;
      return true;
    } finally {
      if (this.seenPreview===change) {
        this.seenPreview=undefined;
        if (this.seenUndo===change && !accepted) this.seenUndo=undefined;
      }
    }
  }

  async setDone(todo: TodoWorkspaceItem, done: boolean, listRow:boolean=false): Promise<boolean> {
    if (!todo.id || this.rejectGoneTarget(todo)) return false;
    const owner = this.capture(todo);
    if (!owner) { this.error = 'offline'; return false; }
    const path = '/api/v1/todos/' + pathPart(todo.id) + managerQuery(todo.manager || this.managerId);
    const result = await this.write(path, 'PATCH', Wire.payload(['done'], [done]), todo,listRow);
    if (!result || !this.owns(owner,!listRow)) return false;
    const data = Wire.object(result, 'todo');
    if (Wire.text(data,'id') !== todo.id || Wire.text(data,'manager') && Wire.text(data,'manager') !== (todo.manager || owner.managerId)) { this.error = 'failed'; return false; }
    this.upsert(data, todo.manager || this.managerId);
    return true;
  }

  async remove(todo: TodoWorkspaceItem): Promise<boolean> {
    if (this.rejectGoneTarget(todo)) return false;
    const owner = this.capture(todo);
    if (!owner) { this.error = 'offline'; return false; }
    const path = '/api/v1/todos/' + pathPart(todo.id) + managerQuery(todo.manager || this.managerId);
    const result = await this.write(path, 'DELETE', Wire.payload([], []), todo);
    if (!result || !this.owns(owner)) return false;
    this.items = this.items.filter((item: TodoWorkspaceItem) => item.id !== todo.id);
    if (this.selected.id === todo.id) this.selected = new TodoWorkspaceItem();
    this.targetGone = false;
    if (this.initialRow?.id === todo.id && (!this.initialRow.manager || this.initialRow.manager === (todo.manager || this.managerId)))
      this.initialRow = undefined;
    this.refreshHubCountsAfterMutation();
    return true;
  }

  async linkAgent(todo: TodoWorkspaceItem, session: ResourceRow): Promise<boolean> {
    if (this.rejectGoneTarget(todo)) return false;
    const owner = this.capture(todo);
    if (!owner) { this.error = 'offline'; return false; }
    if (todo.isDone()) { this.error = 'request_updated'; return false; }
    const machine = session.machine || Wire.text(session.data(), 'machine_id');
    const sessionId = session.session || Wire.text(session.data(), 'session_id') || session.id;
    if (!this.eligibleMachine(machine) || !machine || !sessionId) { this.error = 'todo_agent_unavailable'; return false; }
    const path = '/api/v1/todos/' + pathPart(todo.id) + '/agents/' + pathPart(machine) + '/' + pathPart(sessionId) + managerQuery(todo.manager || this.managerId);
    const result = await this.write(path, 'PUT', Wire.payload([], []), todo);
    if (!result || !this.owns(owner)) return false;
    const data = session.data();
    const agent = new TodoWorkspaceAgent(Wire.payload(['machine_id', 'session_id', 'title', 'cwd', 'agent', 'source', 'linked_at'],
      [machine, sessionId, session.title, Wire.text(data, 'cwd', session.path), Wire.text(data, 'agent'), 'manual', new Date().toISOString()]));
    const next = updateRecord(todo, Wire.payload(['agents'], [todo.agents.filter((item: TodoWorkspaceAgent) =>
      !(item.machineId === machine && item.sessionId === sessionId)).concat([agent]).map((item: TodoWorkspaceAgent) => Wire.parse(item.raw))]), todo.manager || this.managerId);
    this.replace(next);
    return true;
  }

  async unlinkAgent(todo: TodoWorkspaceItem, agent: TodoWorkspaceAgent): Promise<boolean> {
    if (this.rejectGoneTarget(todo)) return false;
    const owner = this.capture(todo);
    if (!owner || !agent.isActive()) { this.error = 'request_updated'; return false; }
    const path = '/api/v1/todos/' + pathPart(todo.id) + '/agents/' + pathPart(agent.machineId) + '/' + pathPart(agent.sessionId) + managerQuery(todo.manager || this.managerId);
    const result = await this.write(path, 'DELETE', Wire.payload([], []), todo);
    if (!result || !this.owns(owner)) return false;
    const now = new Date().toISOString();
    const agents = todo.agents.map((item: TodoWorkspaceAgent) => {
      const data = Wire.parse(item.raw);
      if (item.machineId === agent.machineId && item.sessionId === agent.sessionId) {
        data['released_at'] = now; data['dismissed_at'] = now;
      }
      return data;
    });
    this.replace(updateRecord(todo, Wire.payload(['agents'], [agents]), todo.manager || this.managerId));
    return true;
  }

  async linkSpace(todo: TodoWorkspaceItem, choice: TodoWorkspaceSpaceChoice): Promise<boolean> {
    if (this.rejectGoneTarget(todo)) return false;
    const owner = this.capture(todo);
    if (!owner) { this.error = 'offline'; return false; }
    if (todo.isDone()) { this.error = 'request_updated'; return false; }
    if (!this.eligibleMachine(choice.machineId) || !choice.space) { this.error = 'todo_project_unavailable'; return false; }
    const path = '/api/v1/todos/' + pathPart(todo.id) + '/spaces/' + pathPart(choice.machineId) + '/' + pathPart(choice.space) + managerQuery(todo.manager || this.managerId);
    const result = await this.write(path, 'PUT', Wire.payload([], []), todo);
    if (!result || !this.owns(owner)) return false;
    const spaces = todo.spaces.filter((item: TodoWorkspaceSpace) => !(item.machineId === choice.machineId && item.space === choice.space))
      .map((item: TodoWorkspaceSpace): Record<string, Object> => Wire.payload(['machine_id', 'space', 'linked_at'], [item.machineId, item.space, item.linkedAt]));
    spaces.push(Wire.payload(['machine_id', 'space', 'linked_at'], [choice.machineId, choice.space, new Date().toISOString()]));
    this.replace(updateRecord(todo, Wire.payload(['spaces'], [spaces]), todo.manager || this.managerId));
    return true;
  }

  async unlinkSpace(todo: TodoWorkspaceItem, space: TodoWorkspaceSpace): Promise<boolean> {
    if (this.rejectGoneTarget(todo)) return false;
    const owner = this.capture(todo);
    if (!owner || !todo.spaces.some((item: TodoWorkspaceSpace) => item.machineId === space.machineId && item.space === space.space)) {
      this.error = 'request_updated'; return false;
    }
    const path = '/api/v1/todos/' + pathPart(todo.id) + '/spaces/' + pathPart(space.machineId) + '/' + pathPart(space.space) + managerQuery(todo.manager || this.managerId);
    const result = await this.write(path, 'DELETE', Wire.payload([], []), todo);
    if (!result || !this.owns(owner)) return false;
    const spaces = todo.spaces.filter((item: TodoWorkspaceSpace) => !(item.machineId === space.machineId && item.space === space.space))
      .map((item: TodoWorkspaceSpace): Record<string, Object> => Wire.payload(['machine_id', 'space', 'linked_at'], [item.machineId, item.space, item.linkedAt]));
    const agents = todo.agents.filter((item: TodoWorkspaceAgent) => !(item.source === 'space' && item.via === space.machineId + '/' + space.space))
      .map((item: TodoWorkspaceAgent): Record<string, Object> => Wire.parse(item.raw));
    this.replace(updateRecord(todo, Wire.payload(['spaces', 'agents'], [spaces, agents]), todo.manager || this.managerId));
    return true;
  }

  private eligibleMachine(machineId: string): boolean {
    const machine = this.machines.find((row: ResourceRow) => row.id === machineId && row.manager === this.managerId);
    if (!machine) return false;
    const data = machine.data();
    const trust = Wire.text(data, 'trust');
    return Wire.bool(data, 'online', false) && trust !== 'pending' && trust !== 'removed' && trust !== 'revoked' &&
      !Wire.bool(data, 'pending_confirm') && !Wire.bool(data, 'pending');
  }

  candidateSessions(todo: TodoWorkspaceItem = this.selected): ResourceRow[] {
    return this.sessions.filter((row: ResourceRow) => {
      const machine = row.machine || Wire.text(row.data(), 'machine_id');
      const session = row.session || Wire.text(row.data(), 'session_id') || row.id;
      return !Wire.bool(row.data(), 'assistant') && !!machine && !!session && this.eligibleMachine(machine) &&
        !todo.agents.some((agent: TodoWorkspaceAgent) => agent.machineId === machine && agent.sessionId === session && agent.isActive());
    });
  }

  spaceChoices(todo: TodoWorkspaceItem = this.selected): TodoWorkspaceSpaceChoice[] {
    const choices: TodoWorkspaceSpaceChoice[] = [];
    for (const session of this.sessions) {
      const machineId = session.machine || Wire.text(session.data(), 'machine_id');
      const machine = this.machines.find((row: ResourceRow) => row.id === machineId);
      const data = session.data();
      const space = Wire.text(data, 'space_name', Wire.text(data, 'space_id'));
      if (!machineId || !space || !this.eligibleMachine(machineId)) continue;
      if (todo.spaces.some((item: TodoWorkspaceSpace) => item.machineId === machineId && item.space === space)) continue;
      const label = (machine?.title || machineId) + ' · ' + space;
      if (!choices.some((item: TodoWorkspaceSpaceChoice) => item.machineId === machineId && item.space === space))
        choices.push(new TodoWorkspaceSpaceChoice(machineId, space, label));
    }
    return choices;
  }

  machineName(machineId: string, manager?: string): string {
    const named = this.machines.find((row: ResourceRow) => row.id === machineId && (manager === undefined || row.manager === manager)) ||
      this.machines.find((row: ResourceRow) => row.id === machineId && !row.manager);
    return named?.title || machineId;
  }

  agentSession(agent: TodoWorkspaceAgent): ResourceRow {
    const existing = todoAgentSession(this.selected.activeAgents(), this.sessions, agent);
    if (existing) return existing;
    const row = new ResourceRow(agent.sessionId, agent.title, 'session');
    row.manager = this.managerId; row.machine = agent.machineId; row.session = agent.sessionId; row.path = agent.cwd;
    row.raw = JSON.stringify(Wire.payload(['id', 'session_id', 'machine_id', 'manager', 'title', 'cwd', 'agent', 'state', 'status', 'start_error'],
      [agent.sessionId, agent.sessionId, agent.machineId, this.managerId, agent.title, agent.cwd, agent.agent, 'offline', 'offline', agent.startError]));
    return row;
  }

  agentState(agent: TodoWorkspaceAgent): string {
    const session = todoAgentSession(this.selected.activeAgents(), this.sessions, agent);
    if (!agent.isActive()) return 'followup';
    if (!session) return 'followup';
    return session.state || Wire.text(session.data(), 'state', Wire.text(session.data(), 'status')) || 'followup';
  }

  state(todo: TodoWorkspaceItem): string { return todoWorkspaceStatus(todo, this.sessions); }

  stateLabelKey(todo: TodoWorkspaceItem): string { return 'todo_state_' + this.state(todo); }

  projectTitle(todo: TodoWorkspaceItem): string {
    if (!todo.project) return '';
    const project = this.projects.find((row: ResourceRow) => row.id === todo.project);
    return project?.title || todo.project;
  }

  threadTitle(todo: TodoWorkspaceItem): string { return Wire.text(todo.thread, 'title') || Wire.text(todo.thread, 'thread_id'); }

  searchMatches(todo: TodoWorkspaceItem, query: string): boolean {
    const needle = query.trim().toLocaleLowerCase();
    if (!needle) return true;
    const values: string[] = [todo.title, todo.notes, todo.project, this.projectTitle(todo),
      Wire.text(todo.thread, 'title'), Wire.text(todo.source, 'channel'),
      todo.wait?.waiting_on || '', todo.wait?.reason || '', todo.wait?.step || ''];
    todo.agents.forEach((agent: TodoWorkspaceAgent) => values.push(agent.title, agent.cwd));
    todo.spaces.forEach((space: TodoWorkspaceSpace) => values.push(space.space, this.machineName(space.machineId)));
    return values.some((value: string) => value.toLocaleLowerCase().includes(needle));
  }

  private phonePlacements(now:number,triage:TodoPhoneSections=new TodoPhoneSections(this.sessions,now)):Map<TodoWorkspaceItem,TodoPhonePlacement> {
    const placed=new Map<TodoWorkspaceItem,TodoPhonePlacement>();
    this.items.forEach((item:TodoWorkspaceItem)=>{
      const superseded=!!item.startError && item.isDone() && relatedTodoMembers(this.items,item).some((other:TodoWorkspaceItem)=>other.isDone() && !other.startError);
      const preview=this.seenPreview;
      const stamp=preview && preview.id===item.id && preview.manager===(item.manager || this.managerId) ? preview.stamp : undefined;
      placed.set(item,triage.place(item,superseded,stamp));
    });
    return placed;
  }

  private groupsFor(visible: TodoWorkspaceItem[],directory:TodoPhoneSections,phone?:Map<TodoWorkspaceItem,TodoPhonePlacement>): TodoWorkspaceGroup[] {
    const remaining: Set<TodoWorkspaceItem> = new Set(visible);
    const groups: TodoWorkspaceGroup[] = [];
    for (const todo of visible) {
      if (!remaining.has(todo)) continue;
      const members = relatedTodoMembers(visible,todo).filter((item:TodoWorkspaceItem)=>remaining.has(item));
      const sorted = phone ? members.slice().sort((a:TodoWorkspaceItem,b:TodoWorkspaceItem)=>{
        const left=phone.get(a)!,right=phone.get(b)!;
        return TODO_PHONE_SECTIONS.indexOf(left.section)-TODO_PHONE_SECTIONS.indexOf(right.section) ||
          Number(left.superseded)-Number(right.superseded) || right.last-left.last || a.id.localeCompare(b.id);
      }) : groupLead(members,this.sessions);
      sorted.forEach((item:TodoWorkspaceItem)=>remaining.delete(item));
      const group = new TodoWorkspaceGroup(sorted.map((item: TodoWorkspaceItem) => item.id).sort().join('|'), '',
        this.state(sorted[0] || todo), sorted);
      group.phone = directory.rowContext(group.lead,phone?.get(group.lead));
      groups.push(group);
    }
    return groups;
  }

  hubSection(item: TodoWorkspaceItem, now: number): string {
    if (item.hubView) return item.hubView.section;
    const local = this.state(item);
    if (local === 'needs_you' || local === 'working') return local;
    if (local === 'checking') return 'just_finished';
    if (local === 'done') {
      const closed = Date.parse(item.doneAt), seen = Date.parse(Wire.text(item.data(), 'seen_at'));
      return Number.isFinite(closed) && now - closed < 86400000 && !(seen >= closed) ? 'just_finished' : 'done';
    }
    return local === 'followup' && (item.prUrl || Wire.text(item.delivery, 'state') === 'pr') ? 'waiting' : 'todo';
  }

  hubCalm(): boolean {
    const counts = this.hubCounts;
    if (!counts || !counts.sections.get('todo')) return false;
    // A plain list of reminders needs neither the section header nor the bar.
    // Keep Done reachable on a device that also holds older, closed tasks.
    return TODO_HUB_SECTIONS.every((key: string) => key === 'todo' || counts.sections.get(key) === 0);
  }

  private hubSections(visible: TodoWorkspaceItem[], now: number): TodoWorkspaceSection[] {
    const grouped = new Map<string, TodoWorkspaceGroup[]>();
    const open = new Set<string>();
    this.items.forEach((item: TodoWorkspaceItem) => {
      if (item.hubView ? item.hubView.phase !== 'done' : !item.isDone() || !!item.startError) open.add(JSON.stringify([item.manager, item.id]));
    });
    const searching = !!this.search.trim();
    const choice = searching || this.hubCalm() ? 'all' : this.hubChoice;
    const fallbackRanks = new Map<string, number>();
    const nextRank = new Map<string, number>();
    for (const item of visible.slice().sort(compareTodoRecent)) {
      const identity = JSON.stringify([item.manager, item.id]);
      if (!searching && item.partOf && item.partOf !== item.id && open.has(JSON.stringify([item.manager, item.partOf]))) continue;
      const key = this.hubSection(item, now);
      if (!todoHubChoiceIncludes(choice, key)) continue;
      if (!item.hubView) {
        const group = JSON.stringify([item.manager, key]);
        const rank = nextRank.get(group) || 0;
        fallbackRanks.set(identity, rank);
        nextRank.set(group, rank + 1);
      }
      if (!grouped.has(key)) grouped.set(key, []);
      grouped.get(key)?.push(new TodoWorkspaceGroup(identity, '', key, [item]));
    }
    const rank = (group: TodoWorkspaceGroup): number => group.lead.hubView?.rank ?? fallbackRanks.get(group.key) ?? 0;
    const order = (item: TodoWorkspaceItem): number => {
      if (this.order === 'due') return Number.isFinite(item.dueBy) ? item.dueBy : Number.MAX_SAFE_INTEGER;
      if (this.order === 'priority') return item.priority === 'high' ? 0 : item.priority === 'low' ? 2 : 1;
      return 0;
    };
    const sections: TodoWorkspaceSection[] = [];
    TODO_HUB_SECTIONS.forEach((key: string) => {
      const groups = grouped.get(key);
      if (!groups) return;
      groups.sort((a: TodoWorkspaceGroup, b: TodoWorkspaceGroup) => order(a.lead) - order(b.lead) || rank(a) - rank(b) ||
        a.lead.manager.localeCompare(b.lead.manager) || a.lead.id.localeCompare(b.lead.id));
      sections.push(new TodoWorkspaceSection(key, todoHubLabel(key), groups));
    });
    return sections;
  }

  hiddenWaiting(now:number, firstWeekday:number): number {
    if (this.dueFilter === 'all') return 0;
    const phone=this.effectiveGroup()==='hub' ? undefined : this.phonePlacements(now);
    const hidden = new Set<string>();
    this.items.forEach((item: TodoWorkspaceItem) => {
      if (this.searchMatches(item,this.search) && !todoMatchesDue(item,this.dueFilter,now,firstWeekday) &&
        (phone ? phone.get(item)!.section : this.hubSection(item,now))==='needs_you') hidden.add(JSON.stringify([item.manager,item.id]));
    });
    return hidden.size;
  }

  sections(now:number=Date.now(),firstWeekday:number=1): TodoWorkspaceSection[] {
    const searching = !!this.search.trim();
    let visible = this.items.filter((item: TodoWorkspaceItem) => this.searchMatches(item,this.search) &&
      (todoMatchesDue(item,this.dueFilter,now,firstWeekday) || (searching && item.isDone() && !item.startError)));
    if (this.effectiveGroup() === 'hub') return this.hubSections(visible, now);
    if (this.statusFilter !== 'all') visible = visible.filter((item: TodoWorkspaceItem) => this.state(item) === this.statusFilter);
    const directory = new TodoPhoneSections(this.sessions,now);
    const phone=this.effectiveGroup()==='status' ? this.phonePlacements(now,directory) : undefined;
    const groups = this.groupsFor(visible,directory,phone);
    const grouped: Map<string, TodoWorkspaceGroup[]> = new Map();
    const titles: Map<string, string> = new Map();
    for (const group of groups) {
      let key = this.state(group.lead), title = this.stateLabelKey(group.lead);
      if (phone) { key=phone.get(group.lead)!.section; title=phoneTodoTitle(key); }
      else if (searching && group.lead.isDone() && !group.lead.startError) {
        key = 'done'; title = phoneTodoTitle('done');
      } else if (this.groupBy === 'project') {
        key = group.lead.project || 'unsorted'; title = group.lead.project ? this.projectTitle(group.lead) : 'todo_unsorted';
      } else if (this.groupBy === 'thread') {
        const chatId = Wire.text(group.lead.thread, 'chat_id'), threadId = Wire.text(group.lead.thread, 'thread_id');
        key = chatId && threadId ? JSON.stringify([Wire.text(group.lead.thread, 'channel'), chatId, threadId]) : 'unthreaded';
        title = this.threadTitle(group.lead) || 'todo_no_thread';
      }
      if (!grouped.has(key)) grouped.set(key, []);
      grouped.get(key)?.push(group);
      titles.set(key, title);
    }
    const order: string[] = Array.from(grouped.keys());
    if (phone) order.sort((left:string,right:string)=>TODO_PHONE_SECTIONS.indexOf(left)-TODO_PHONE_SECTIONS.indexOf(right));
    else order.sort((left: string, right: string) => {
      if (left === 'done' && right !== 'done') return 1;
      if (right === 'done' && left !== 'done') return -1;
      return (titles.get(left) || left).localeCompare(titles.get(right) || right);
    });
    return order.map((key: string) => {
      const rows = grouped.get(key) || [];
      const priority=(g:TodoWorkspaceGroup):number => g.members.reduce((best:number,item:TodoWorkspaceItem) => Math.min(best,item.priority === 'high' ? 0 : item.priority === 'low' ? 2 : 1),2);
      const due=(g:TodoWorkspaceGroup):number => g.members.reduce((earliest:number,item:TodoWorkspaceItem) => Number.isFinite(item.dueBy) ? Math.min(earliest,item.dueBy) : earliest,Number.MAX_SAFE_INTEGER);
      const last=(g:TodoWorkspaceGroup):number=>g.members.reduce((max:number,item:TodoWorkspaceItem)=>Math.max(max,phone?.get(item)?.last ?? Number.NEGATIVE_INFINITY),Number.NEGATIVE_INFINITY);
      rows.sort((left:TodoWorkspaceGroup,right:TodoWorkspaceGroup) => (this.order === 'priority' ? priority(left)-priority(right) : 0) ||
        (this.order !== 'recent' ? due(left)-due(right) : 0) || (phone ? last(right)-last(left) || left.lead.id.localeCompare(right.lead.id) : compareTodoRecent(left.lead,right.lead)));
      const awaitingReview=phone && key==='just_done' && rows.every((group:TodoWorkspaceGroup) =>
        group.members.every((member:TodoWorkspaceItem) => phone.get(member)?.awaitingReview === true));
      return new TodoWorkspaceSection(key, awaitingReview ? 'phone_todo_section_delivered' : titles.get(key) || key, rows);
    });
  }

  relatedTodos(todo: TodoWorkspaceItem): TodoWorkspaceItem[] { return relatedTodoMembers(this.items, todo).slice(1); }

  todoResource(todo: TodoWorkspaceItem): ResourceRow {
    const row = ResourceRow.from(todo.data(), 'todo');
    row.manager = todo.manager || this.managerId;
    return row;
  }

  sourceResource(todo: TodoWorkspaceItem): ResourceRow | undefined {
    if (todo.origin) {
      const existing = this.conversations.find((row: ResourceRow) => row.id === todo.origin);
      if (existing) return existing;
      const row = new ResourceRow(todo.origin, '', 'conversation');
      row.manager = todo.manager || this.managerId; row.session = todo.origin;
      row.raw = JSON.stringify(Wire.payload(['id', 'manager', 'source_kind'], [todo.origin, row.manager, 'assistant']));
      return row;
    }
    const source = todo.source;
    const chatId = Wire.text(source, 'chat_id'), messageId = Wire.text(source, 'message_id'), channel = Wire.text(source, 'channel');
    if (!chatId || !channel) return undefined;
    const row = new ResourceRow(chatId, Wire.text(source, 'title'), 'todo_source');
    row.manager = todo.manager || this.managerId; row.session = messageId; row.detail = channel;
    row.raw = JSON.stringify(Wire.payload(['id', 'manager', 'channel', 'chat_id', 'message_id', 'anchor', 'title'],
      [chatId, row.manager, channel, chatId, messageId, Wire.text(source, 'anchor'), Wire.text(source, 'title')]));
    return row;
  }

  threadResource(todo: TodoWorkspaceItem): ResourceRow | undefined {
    const threadId = Wire.text(todo.thread, 'thread_id'), chatId = Wire.text(todo.thread, 'chat_id');
    if (!threadId || !chatId) return undefined;
    const row = new ResourceRow(chatId, this.threadTitle(todo), 'todo_thread');
    row.manager = todo.manager || this.managerId; row.session = threadId; row.detail = Wire.text(todo.thread, 'channel');
    row.raw = JSON.stringify(JSON.parse(JSON.stringify(todo.thread)));
    return row;
  }

  projectResource(todo: TodoWorkspaceItem): ResourceRow | undefined {
    if (!todo.project) return undefined;
    const row = new ResourceRow(todo.project, this.projectTitle(todo), 'knowledge');
    row.path = 'projects'; row.manager = todo.manager || this.managerId;
    row.raw = JSON.stringify(Wire.payload(['id', 'title', 'manager'], [todo.project, row.title, row.manager]));
    return row;
  }

  lineageResources(todo: TodoWorkspaceItem): ResourceRow[] {
    const rows: ResourceRow[] = [];
    for (const step of todo.lineage) {
      const id = Wire.text(step, 'todo_id');
      if (!id || id === todo.id) continue;
      const row = new ResourceRow(id, Wire.text(step, 'title'), 'todo');
      row.manager = todo.manager || this.managerId; row.detail = Wire.text(step, 'why');
      row.raw = JSON.stringify(Wire.payload(['id', 'title', 'manager', 'todo_id', 'session_id', 'machine', 'why', 'continues', 'superseded_by'],
        [id, row.title, row.manager, id, Wire.text(step, 'session_id'), Wire.text(step, 'machine'), row.detail,
          Wire.text(step, 'continues'), Wire.text(step, 'superseded_by')]));
      rows.push(row);
    }
    return rows;
  }
}
