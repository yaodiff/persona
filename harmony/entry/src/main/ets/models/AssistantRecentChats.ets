import type { ClientModel } from './ClientModel';
import { ResourceRow, Wire } from './Wire';
import { relativeActivityDate } from '../core/PhoneDate';

// Older Hubs identify a scheduled conversation through its task and run history.
// A task that continues the conversation it came from leaves that conversation in Recent.
export class ScheduledChatOrigins {
  private ids: Set<string> = new Set();
  private topics: string[] = [];
  constructor(tasks: Record<string,Object>[], runs: Record<string,Object>[]) {
    const original = new Set(tasks.map((r: Record<string,Object>) => Wire.text(r,'session_id')));
    const add = (id: string): void => { if (id && !original.has(id)) this.ids.add(id); };
    for (const task of tasks) {
      add(Wire.text(task,'run_session_id')); add(Wire.text(task,'last_session_id'));
      const title = Wire.text(task,'title').trim();
      if (title) this.topics.push(title);
    }
    for (const run of runs) add(Wire.text(run,'session_id'));
  }
  contains(row: ResourceRow): boolean {
    if (this.ids.has(row.session || row.id)) return true;
    // The Hub gives older task runs this title and shortens the topic to fit it.
    const match = /^\s*[0-9]{4}\s*\|\s*定时\s*\|\s*(.+?)\s*$/.exec(row.title);
    return !!match && this.topics.some((topic:string) => topic.startsWith(match[1]) || match[1].startsWith(topic));
  }
  taskSessionIds(rows: readonly ResourceRow[]): string[] {
    return rows.filter((row: ResourceRow) => this.contains(row)).map((row: ResourceRow) => row.session || row.id);
  }
}

export class SidebarChats {
  recent: ResourceRow[] = [];
  directions: ResourceRow[] = [];
}

function sidebarOrigin(row: ResourceRow, data: Record<string,Object>, history: ScheduledChatOrigins): string {
  const origin = Wire.text(data,'origin').trim();
  if (origin && origin !== 'user' && origin !== 'channel') return origin;
  return history.contains(row) ? 'task' : '';
}

export function sidebarConversationTitle(row: ResourceRow, translate: (key:string) => string): string {
  return row.title && row.title !== 'Claude Code' ? row.title : translate('phone_new_chat_title');
}

const sourceKeys: Record<string,string> = {
  'inbox': 'inbox', 'task': 'phone_drawer_schedules', 'spawned': 'phone_sidebar_source_spawned',
  'extension': 'phone_sidebar_source_extension', 'lark_agent': 'phone_sidebar_source_lark'
};
const stateKeys: Record<string,string> = {
  'needs_you': 'phone_todo_section_needs', 'working': 'phone_todo_section_working', 'unread': 'phone_sidebar_unread'
};

const allChatSources: string[] = ['inbox', 'task', 'lark_agent', 'spawned', 'extension', 'other'];

export function assistantChatSource(row: ResourceRow, history: ScheduledChatOrigins): string {
  const source = sidebarOrigin(row, row.data(), history);
  return !source || allChatSources.includes(source) ? source : 'other';
}

export function assistantChatSourceKey(source: string, spoken: boolean = false): string {
  if (!spoken && source === 'lark_agent') return 'phone_all_chats_lark';
  if (!spoken && source === 'extension') return 'extensions';
  return Wire.text(sourceKeys, source, 'phone_sidebar_source_other');
}

export function assistantChatState(row: ResourceRow): string {
  const state = Wire.text(row.data(), 'state');
  return state || (row.state === 'blocked' ? 'needs_you' : row.state === 'done' ? 'unread' : row.state);
}

function chatActivity(row: ResourceRow, locale: string, now: number, translate: (key: string) => string): string {
  const key = Wire.text(stateKeys, assistantChatState(row));
  return key ? translate(key) : relativeActivityDate(Date.parse(Wire.text(row.data(), 'updated_at')), now, locale,
    translate('agent_time_just_now'), translate('kb_minutes_ago'), translate('kb_yesterday'));
}

export function allChatDetail(row: ResourceRow, source: string, filter: string, locale: string, now: number,
  translate: (key: string) => string): string {
  return [filter === 'all' && source ? translate(assistantChatSourceKey(source)) : '',
    chatActivity(row, locale, now, translate)].filter((value: string) => !!value).join(' · ');
}

export function assistantChatTitleParts(title: string): string[] {
  const parts = title.split(' | ');
  if (parts.length < 3 || !/^[0-9]{4}$/.test(parts[0]) || parts[1].length < 1 || parts[1].length > 4) return ['', title];
  const topic = parts.slice(2).join(' | ').trim();
  return topic ? [parts[0] + ' | ' + parts[1] + ' | ', topic] : ['', title];
}

function updatedAt(row: ResourceRow): number {
  const value = Wire.text(row.data(), 'updated_at'), stamp = Date.parse(value);
  const fraction = value.match(/:\d{2}\.\d{3}(\d+)/);
  return Number.isFinite(stamp) ? stamp + (fraction ? Number('0.' + fraction[1]) : 0) : Number.NEGATIVE_INFINITY;
}

function newestChats(rows: ResourceRow[]): ResourceRow[] {
  const activity = new Map<ResourceRow,number>();
  for (const row of rows) activity.set(row,updatedAt(row));
  return rows.sort((a: ResourceRow,b: ResourceRow) => {
    const order = activity.get(b)! - activity.get(a)!;
    const left = a.session || a.id, right = b.session || b.id;
    return order || (left < right ? -1 : left > right ? 1 : 0);
  });
}

export class AllChatGroup {
  id: string;
  rows: ResourceRow[];
  constructor(id: string, rows: ResourceRow[]) { this.id = id; this.rows = rows; }
}

export function allAssistantChatGroups(conversations: ResourceRow[], main: string, mainMachine: string, group: string,
  manager: string, supportsOrigin: boolean, history: ScheduledChatOrigins): AllChatGroup[] {
  const all = newestChats(conversations.filter((row: ResourceRow) => {
    const id = row.session || row.id;
    return id.length > 0 && (!row.group || row.group === group) && (!row.manager || row.manager === manager) &&
      !(id === main && (!mainMachine || row.machine === mainMachine)) && !Wire.bool(row.data(), 'archived');
  }));
  const supported = supportsOrigin || all.some((row: ResourceRow) => typeof row.data()['origin'] === 'string');
  const mine: ResourceRow[] = [], bySource = new Map<string,ResourceRow[]>();
  for (const row of all) {
    const source = assistantChatSource(row, history);
    if (!supported) {
      if (!history.contains(row)) mine.push(row);
    } else if (!source) {
      mine.push(row);
    } else {
      if (!bySource.has(source)) bySource.set(source, []);
      bySource.get(source)!.push(row);
    }
  }
  const groups = [new AllChatGroup('all',all), new AllChatGroup('mine',mine)];
  for (const source of allChatSources) {
    const rows = bySource.get(source);
    if (rows?.length) groups.push(new AllChatGroup(source,rows));
  }
  return groups;
}

export function sidebarConversationLabel(row: ResourceRow, history: ScheduledChatOrigins, locale: string, now: number,
  translate: (key:string) => string): string {
  const data = row.data(), words = [sidebarConversationTitle(row,translate)];
  const origin = sidebarOrigin(row,data,history);
  if (origin) words.push(translate(Wire.text(sourceKeys,origin,'phone_sidebar_source_other')));
  const activity = chatActivity(row, locale, now, translate);
  if (activity) words.push(activity);
  const preview = Wire.text(data,'last_text').replace(/\*\*|__|`|### |## |# |> /g,'').replace(/\r\n?|\n/g,' ');
  if (preview) words.push(preview);
  return words.join(translate('phone_sidebar_label_separator'));
}

export function assistantSidebarChats(conversations: ResourceRow[], main: string, mainMachine: string,
  options: Record<string,Object>, history: ScheduledChatOrigins, group: string, manager: string): SidebarChats {
  const result = new SidebarChats();
  const leaders = Wire.strings(options,'leaders'), background = Wire.text(options,'background_supervisor');
  const showScheduled = Wire.bool(options,'recent_task_chats');
  const leadRows = new Map<string,ResourceRow>();
  for (const row of conversations) {
    const id = row.session || row.id;
    if (!id || row.group && row.group !== group || row.manager && row.manager !== manager ||
        id === main && (!mainMachine || row.machine === mainMachine)) continue;
    const data = row.data();
    if (Wire.bool(data,'archived')) continue;
    if (leaders.includes(id) && id !== main) {
      if (!leadRows.has(id)) leadRows.set(id,row);
      continue;
    }
    if (!showScheduled && (id === background || sidebarOrigin(row,data,history) === 'task')) continue;
    result.recent.push(row);
  }
  for (const id of leaders) {
    const row = leadRows.get(id);
    if (row) { result.directions.push(row); leadRows.delete(id); }
  }
  result.recent = newestChats(result.recent).slice(0,5);
  return result;
}

@ObservedV2
export class SidebarTaskHistory {
  @Trace revision: number = 0;
  private cache: Map<string,ScheduledChatOrigins> = new Map();
  private empty: ScheduledChatOrigins = new ScheduledChatOrigins([],[]);
  private request: string = '';
  private sequence: number = 0;
  known(scope:string): ScheduledChatOrigins { return this.cache.get(scope) || this.empty; }
  async refresh(owner:ClientModel,visible:boolean): Promise<void> {
    if (!visible || !owner.connected || owner.demo) {
      if (this.request) { this.request=''; this.sequence++; }
      return;
    }
    const scope=owner.scope(), generation=owner.connectionGeneration();
    const key=JSON.stringify([scope,generation,owner.conversations.length]);
    if (key===this.request) return;
    this.request=key;
    const token=++this.sequence, repo=owner.repo();
    try {
      const pages=await Promise.all([repo.call('/api/v1/assistant/tasks'),repo.call('/api/v1/assistant/runs')]);
      if (token!==this.sequence || !owner.owns(generation,scope) || owner.repo()!==repo ||
          !Array.isArray(pages[0]['tasks']) || !Array.isArray(pages[1]['runs'])) return;
      this.cache.set(scope,new ScheduledChatOrigins(Wire.rows(pages[0],'tasks'),Wire.rows(pages[1],'runs')));
      this.revision++;
    } catch (_) { /* Keep the last successful read for this Hub and assistant. */ }
  }
}
