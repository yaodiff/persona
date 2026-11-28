import { util } from '@kit.ArkTS';
import { ClientModel } from './ClientModel';
import { HubRepository, segment } from './HubRepository';
import { MutationRecord } from './MutationStore';
import { ResourceRow, Wire } from './Wire';
import { HttpFailure } from '../platform/HubTransport';

const MAX_MESSAGE_PAGE: number = 50;
const MAX_UPLOAD_BYTES: number = 32 * 1024 * 1024;
const MAX_MAIL_ATTACHMENT_BYTES: number = 25 * 1024 * 1024;
const MAX_IMAGE_PIXELS: number = 40000000;
const MAX_IMAGE_EDGE: number = 8192;

interface OwnerSnapshot {
  client: ClientModel;
  repo: HubRepository;
  generation: number;
  scope: string;
  pageRevision: number;
  rowKey: string;
  managerId: string;
  groupId: string;
}

export class InboxMessage {
  id: string = '';
  position: string = '';
  sender: string = '';
  mine: boolean = false;
  text: string = '';
  state: string = '';
  privateMessage: boolean = false;
  raw: string = '{}';
  constructor(data: Record<string, Object> = {}) {
    this.id = Wire.text(data, 'id');
    const position = data['position'];
    this.position = typeof position === 'string' ? position as string : typeof position === 'number' ? String(position) : '';
    const sender = Wire.object(data, 'sender');
    this.sender = Wire.text(sender, 'name', Wire.text(data, 'sender_name'));
    this.mine = Wire.bool(data, 'mine', Wire.bool(data, 'is_mine'));
    this.text = Wire.text(data, 'text');
    this.state = Wire.text(data, 'state');
    this.privateMessage = Wire.bool(data, 'private');
    if (this.privateMessage && Wire.text(data, 'private_text')) this.text = Wire.text(data, 'private_text');
    if (!this.text) this.text = Wire.rows(data, 'parts').map((part: Record<string, Object>) => {
      const kind = Wire.text(part, 'kind');
      return Wire.text(part, 'text', kind === 'emoji' ? Wire.text(part, 'key') : '');
    }).join('');
    this.raw = JSON.stringify(data);
  }
  data(): Record<string, Object> { return Wire.parse(this.raw); }
}

export class InboxMailMessage {
  accountId: string = '';
  mailbox: string = 'INBOX';
  uid: number = 0;
  messageId: string = '';
  subject: string = '';
  from: string = '';
  to: string = '';
  date: string = '';
  text: string = '';
  html: string = '';
  attachments: Record<string, Object>[] = [];
  raw: string = '{}';
  constructor(data: Record<string, Object> = {}) {
    this.accountId = Wire.text(data, 'account_id'); this.mailbox = Wire.text(data, 'mailbox', 'INBOX');
    this.uid = Wire.number(data, 'uid'); this.messageId = Wire.text(data, 'message_id'); this.subject = Wire.text(data, 'subject');
    this.from = addressLabel(Wire.object(data, 'from')); this.to = Wire.rows(data, 'to').map((item: Record<string, Object>) => addressLabel(item)).join(', ');
    this.date = Wire.text(data, 'date'); this.text = Wire.text(data, 'text'); this.html = Wire.text(data, 'html');
    this.attachments = Wire.rows(data, 'attachments'); this.raw = JSON.stringify(data);
  }
  data(): Record<string, Object> { return Wire.parse(this.raw); }
  bodyText(): string { return this.text || htmlToText(this.html); }
}

export class InboxAttachmentDraft {
  name: string = '';
  mime: string = 'application/octet-stream';
  kind: string = 'file';
  size: number = 0;
  data: ArrayBuffer = new ArrayBuffer(0);
  constructor(name: string, mime: string, kind: string, data: ArrayBuffer) {
    this.name = name; this.mime = mime; this.kind = kind; this.size = data.byteLength; this.data = data;
  }
}

export interface InboxAttachmentContent {
  data: ArrayBuffer;
  name: string;
  mime: string;
}

export class InboxMailComposeSnapshot {
  accountId: string = '';
  to: string = '';
  cc: string = '';
  bcc: string = '';
  subject: string = '';
  text: string = '';
  operationId: string = '';
  constructor(accountId: string, to: string, cc: string, bcc: string, subject: string, text: string, operationId: string) {
    this.accountId = accountId; this.to = to; this.cc = cc; this.bcc = bcc; this.subject = subject; this.text = text; this.operationId = operationId;
  }
}

export function addressLabel(address: Record<string, Object>): string {
  const name = Wire.text(address, 'name'), email = Wire.text(address, 'email');
  return name && name !== email ? name + ' <' + email + '>' : email || name;
}

export function htmlToText(html: string): string {
  if (!html) return '';
  return html.replace(/<\s*br\s*\/?\s*>/gi, '\n').replace(/<\s*\/p\s*>/gi, '\n\n')
    .replace(/<\s*\/div\s*>/gi, '\n').replace(/<[^>]*>/g, ' ')
    .replace(/&nbsp;/gi, ' ').replace(/&amp;/gi, '&').replace(/&lt;/gi, '<').replace(/&gt;/gi, '>')
    .replace(/&quot;/gi, '"').replace(/&#39;/gi, "'").replace(/[ \t]+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim();
}

function numberArray(record: Record<string, Object>, key: string): number[] {
  const value = record[key];
  if (!Array.isArray(value)) return [];
  return (value as Object[]).map((item: Object) => typeof item === 'number' ? item as number : typeof item === 'string' && /^\d+$/.test(item as string) ? Number(item as string) : 0)
    .filter((item: number) => Number.isSafeInteger(item) && item > 0);
}

function query(values: Record<string, string>): string {
  const out: string[] = [];
  Object.keys(values).forEach((key: string) => { if (values[key] !== '') out.push(encodeURIComponent(key) + '=' + encodeURIComponent(values[key])); });
  return out.length ? '?' + out.join('&') : '';
}

function managerQuery(managerId: string, values: Record<string, Object> = Wire.payload([], [])): string {
  const all: Record<string, string> = {};
  if (managerId) all['manager'] = managerId;
  Object.keys(values).forEach((key: string) => { const value = Wire.text(values, key); if (value) all[key] = value; });
  return query(all);
}

function safeMessagingMedia(value: string): string {
  if (!value || value.length > 2000 || /[\\\r\n#\u0000-\u001f]/.test(value) || value.startsWith('/') || /^https?:/i.test(value)) return '';
  const queryAt = value.indexOf('?'), pathname = queryAt >= 0 ? value.slice(0, queryAt) : value;
  const tail = queryAt >= 0 ? value.slice(queryAt) : '';
  if (tail.length > 1000 || tail.includes('#') || /[\r\n\u0000-\u001f]/.test(tail)) return '';
  if (/(?:^|[?&])(account|manager)=/i.test(tail)) return '';
  const parts = pathname.split('/');
  if (parts[0] === 'media' && parts.length >= 2) { /* a source media resource */ }
  else if (parts[0] === 'messages' && parts.length >= 4 && parts[2] === 'media') { /* a message media resource */ }
  else return '';
  for (const part of parts) {
    if (!part) return '';
    let decoded = '';
    try { decoded = decodeURIComponent(part); } catch (_) { return ''; }
    if (!decoded || decoded === '.' || decoded === '..' || decoded.includes('/') || decoded.includes('\\')) return '';
  }
  return parts.map((part: string) => encodeURIComponent(decodeURIComponent(part))).join('/') + tail;
}

function done(row: ResourceRow): boolean { return !!Wire.text(row.data(), 'done_at'); }
function mailTarget(row: ResourceRow): Record<string, Object> { return Wire.object(Wire.parse(row.raw), 'reply_target'); }
function isTodoReference(row: ResourceRow): boolean { return row.kind === 'todo_source' || row.kind === 'todo_thread'; }
function messagingSource(channel: string): string {
  const value = channel.trim().toLowerCase();
  return value === 'feishu' ? 'lark' : value;
}
function targetRevision(row: ResourceRow, detail: Record<string, Object>): string {
  const mail = Wire.object(detail, 'mail'), target = Wire.object(detail, 'reply_target');
  const conversation = Wire.object(detail, 'conversation'), lark = Wire.object(detail, 'lark');
  const connector = Wire.object(detail, 'connector');
  return JSON.stringify([row.manager, Wire.text(detail, 'source', Wire.text(row.data(), 'source')), Wire.text(detail, 'account', Wire.text(row.data(), 'account')),
    Wire.text(mail, 'account_id'), Wire.text(mail, 'mailbox'), Wire.identifier(target, 'uid'), Wire.text(target, 'version'),
    Wire.text(conversation, 'source'), Wire.text(conversation, 'id'), Wire.identifier(lark, 'feed_id'), Wire.text(lark, 'thread_kind'),
    Wire.identifier(lark, 'reply_to'), Wire.text(connector, 'reply_to'), Wire.bool(connector, 'can_reply'), Wire.text(row.data(), 'version'),
    row.kind, row.session, Wire.text(row.data(), 'channel'), Wire.text(row.data(), 'chat_id'), Wire.text(row.data(), 'message_id'),
    Wire.text(row.data(), 'anchor'), Wire.text(row.data(), 'thread_id'), Wire.text(row.data(), 'root_message_id')]);
}

function genericConversation(detail: Record<string, Object>, row: ResourceRow): Record<string, Object> {
  const conversation = Wire.object(detail, 'conversation');
  return Object.keys(conversation).length ? conversation : Wire.object(row.data(), 'conversation');
}

function itemSource(row: ResourceRow, detail: Record<string, Object> = row.data()): string {
  const source = Wire.text(detail, 'source', Wire.text(row.data(), 'source'));
  if (source) return source==='feishu' ? 'lark' : source;
  if (Object.keys(Wire.object(detail, 'mail')).length) return 'mail';
  if (Object.keys(Wire.object(detail, 'lark')).length) return 'lark';
  return Object.keys(Wire.object(detail, 'connector')).length ? 'connector' : '';
}

/** Account and source folder as shown on both the inbox row and its detail header. */
export function inboxRowSourceLabel(data: Record<string, Object>, source: string): string {
  const account = Wire.text(data, 'account_name', Wire.text(data, 'account'));
  const mailbox = Wire.text(Wire.object(data, 'mail'), 'mailbox');
  const label = source + ' · ' + account;
  return mailbox && mailbox.toLowerCase() !== 'inbox' ? label + ' · ' + mailbox : label;
}

function updateResourceList(rows: ResourceRow[], updated: ResourceRow): ResourceRow[] {
  const index = rows.findIndex((item: ResourceRow) => item.key() === updated.key());
  if (index < 0) return rows;
  const next = rows.slice(); next[index] = updated; return next;
}

function handoffConversation(session: Record<string, Object>, managerId: string): ResourceRow | undefined {
  const machine = Wire.text(session, 'machine_id'), id = Wire.text(session, 'session_id');
  if (!machine.trim() || !id.trim()) return undefined;
  const row = new ResourceRow(id, '', 'conversation');
  row.manager = managerId; row.machine = machine; row.session = id;
  row.raw = JSON.stringify(Wire.payload(['id', 'machine_id', 'session_id', 'manager'], [id, machine, id, managerId]));
  return row;
}

const INBOX_OPEN_TIERS:string[] = ['action','working','fyi','noise'];
const INBOX_TIERS: string[] = INBOX_OPEN_TIERS.concat(['done']);

export function inboxTier(data:Record<string,Object>):string {
  if (Wire.text(data,'done_at')) return 'done';
  const status = Wire.text(data,'status'), tier = Wire.text(data,'tier');
  if (INBOX_OPEN_TIERS.includes(status)) return status;
  return ['action','fyi','noise'].includes(tier) ? tier : 'action';
}

export function inboxTierLabel(key:string):string {
  if (key === 'action') return 'needs_you';
  if (key === 'working') return 'flow_inbox_progress';
  if (key === 'fyi') return 'flow_inbox_fyi';
  if (key === 'done') return 'phone_inbox_done';
  return key === 'noise' ? 'flow_inbox_collapsed' : key;
}

export function inboxLineKey(row:ResourceRow):string {
  return row.kind === 'session' ? JSON.stringify([row.key(),Wire.text(row.data(),'inbox_interaction')]) : row.key();
}

function inboxQuestionPreview(request:Record<string,Object>):string {
  const summary=Wire.text(request,'summary').trim(), tool=Wire.text(request,'tool_name');
  switch (Wire.text(request,'kind')) {
    case 'question': return Wire.text(Wire.rows(request,'questions')[0] || {},'question') || summary;
    case 'plan': return Wire.text(request,'plan').split('\n').map((line:string) => line.trim().replace(/^[#* ]+|[#* ]+$/g,''))
      .find((line:string) => !!line) || summary;
    case 'permission': {
      if (summary || tool==='secret_run' || tool==='secret_handoff') return summary;
      const input=Wire.object(request,'tool_input');
      for (const key of ['command','file_path','notebook_path','description','path','url','query','prompt']) {
        const target=Wire.text(input,key).trim(); if (target) return target;
      }
      return tool;
    }
    case 'screen': return Wire.text(Wire.object(request,'dialog'),'question') || summary;
    case 'elicitation': return Wire.text(Wire.object(request,'elicitation'),'message') || summary;
    default: return summary;
  }
}

function inboxSourceGroup(row:ResourceRow):string {
  if (row.kind==='session') return 'agents';
  const data=row.data(), source=itemSource(row,data), account=Wire.text(data,'account');
  if (Object.keys(Wire.object(data,'connector')).length) return 'c:'+account;
  return source==='mail' ? 'mail:'+account : source || 'inbox';
}

// Calendar days keep local midnight boundaries across daylight-saving changes.
function inboxTimeGroup(row: ResourceRow, now: Date): string {
  if (row.kind === 'session') return 'agents';
  const data = row.data();
  const date = new Date(Wire.text(data, 'updated_at') || Wire.text(data, 'at') || Wire.text(data, 'created_at'));
  if (!Number.isFinite(date.getTime())) return 'time:month';
  const localDay = (value: Date): number => Date.UTC(value.getFullYear(), value.getMonth(), value.getDate()) / 86400000;
  const days = localDay(now) - localDay(date);
  if (days <= 0) return 'time:today';
  if (days === 1) return 'time:yesterday';
  if (days < 7) return 'time:d:' + date.getFullYear() + '-' + String(date.getMonth()+1).padStart(2,'0') + '-' + String(date.getDate()).padStart(2,'0');
  return days < 30 ? 'time:week' : 'time:month';
}

function inboxTimeOrder(key: string): number {
  if (key === 'agents') return 0;
  if (key === 'time:today') return 1;
  if (key === 'time:yesterday') return 2;
  if (key.startsWith('time:d:')) return 3;
  return key === 'time:week' ? 4 : 5;
}

export class InboxListSection {
  key:string;
  rows:ResourceRow[] = [];
  groups:InboxListSection[] = [];
  constructor(key:string) { this.key=key; }
}

export class InboxQuickFilter {
  key:string;
  count:number;
  constructor(key:string,count:number) { this.key=key; this.count=count; }
}

export class InboxListView {
  choices:InboxQuickFilter[] = [];
  sections:InboxListSection[] = [];
}

@ObservedV2
export class InboxWorkspace {
  @Trace items: ResourceRow[] = [];
  @Trace selected: ResourceRow = new ResourceRow();
  @Trace detail: Record<string, Object> = {};
  @Trace kbChat: Record<string, Object> = {};
  @Trace sourceFilter: string = 'all';
  @Trace accountFilter: string = 'all';
  @Trace kindFilter:string = '';
  @Trace filterAddresses:Record<string,string> = {};
  @Trace statusFilter: string = 'action';
  private beforeSearch: string = '';
  @Trace doneItems: ResourceRow[] = [];
  @Trace waitingAgents:ResourceRow[] = [];
  @Trace haveDoneDays: boolean = false;
  @Trace historyBusy: boolean = false;
  @Trace historyEnded: boolean = false;
  private historyBefore: string = '';
  private historyBeforeID: string = '';
  private historyRevision: number = 0;
  @Trace search: string = '';
  @Trace loading: boolean = false;
  @Trace detailLoading: boolean = false;
  @Trace feedLoading: boolean = false;
  @Trace error: string = '';
  @Trace writeState: string = '';
  private writeOutbound: boolean = false;
  @Trace replyDraft: string = '';
  @Trace replyTargetChanged: boolean = false;
  @Trace draftLoading: boolean = false;
  @Trace draftReady: boolean = false;
  @Trace threadReplyTo: string = '';
  @Trace threadReply: boolean = false;
  @Trace feedMessages: InboxMessage[] = [];
  @Trace isTopicList: boolean = false;
  @Trace feedHasOlder: boolean = false;
  @Trace feedBefore: string = '';
  @Trace threadRoot: string = '';
  @Trace mailMessages: InboxMailMessage[] = [];
  @Trace mailReadUids: number[] = [];
  @Trace mailAccounts: Record<string, Object>[] = [];
  @Trace mailComposeOpen: boolean = false;
  @Trace mailComposeBusy: boolean = false;
  @Trace mailComposeReady: boolean = false;
  @Trace mailComposeBlocked: boolean = false;
  @Trace mailComposeState: string = 'draft';
  @Trace mailComposeAccount: string = '';
  @Trace mailComposeTo: string = '';
  @Trace mailComposeCc: string = '';
  @Trace mailComposeBcc: string = '';
  @Trace mailComposeSubject: string = '';
  @Trace mailComposeText: string = '';
  @Trace mailComposeError: string = '';
  @Trace mailTranslation: Record<string, Object> = {};
  @Trace translatedMailUid: number = 0;
  @Trace translationError: string = '';
  @Trace translationBusy: boolean = false;
  @Trace translationTarget: string = 'zh-Hans';
  @Trace showOriginal: boolean = true;
  @Trace quickReactions: string[] = [];
  @Trace attachment: InboxAttachmentDraft | undefined;
  @Trace attachmentBusy: boolean = false;
  @Trace handoffDraft: string = '';
  @Trace handoffBusy: boolean = false;
  @Trace referenceMode: boolean = false;
  @Trace referenceThreadId: string = '';
  @Trace referenceThreadTitle: string = '';

  private client: ClientModel | undefined;
  private boundRepo: HubRepository | undefined;
  private active: boolean = false;
  private revision: number = 0;
  private selectedKey: string = '';
  private pendingRow: ResourceRow | undefined;
  private detailTargetRevision: string = '';
  private sourceDefinitions: Record<string, Object>[] = [];
  private sourceCapabilities: string[] = [];
  private sourceStatus: Record<string, Object> = {};
  private sourceMe: Record<string, Object> = {};
  private visibleReadTarget: string = '';
  private visibleReadOwner: OwnerSnapshot | undefined;
  private visibleReadIds: string[] = [];
  private visibleReadPositions: Record<string, string> = {};
  private visibleReadBadges: Record<string, number> = {};
  private readTimer: number = -1;
  private listRequest: number = 0;
  private draftWrite: Promise<void> = Promise.resolve();
  private composeWrite: Promise<void> = Promise.resolve();
  private currentSource: string = '';
  private currentConversation: string = '';
  private currentAccount: string = '';
  private currentAccountName: string = '';
  private currentIsGeneric: boolean = false;
  private currentThreadRootMessage: string = '';
  private currentThreadCanReply: boolean = false;
  private referenceThreadLoaded: boolean = false;

  setSearch(value:string):void {
    const previous = !!this.search.trim(), next = !!value.trim();
    if (!previous && next) { this.beforeSearch = this.statusFilter; this.statusFilter = 'all'; }
    if (previous && !next) { this.statusFilter = this.beforeSearch || 'action'; this.beforeSearch = ''; }
    this.search = value;
  }

  private kindMatches(data:Record<string,Object>,mentionLabel:string):boolean {
    if (!this.kindFilter) return true;
    const kind=Wire.text(data,'kind');
    if (this.kindFilter==='agent') return false;
    if (kind===this.kindFilter) return true;
    if (this.kindFilter==='group') return kind==='bot';
    return this.kindFilter==='mention' && (kind==='doc' || kind==='moment' &&
      (Wire.bool(data,'mentioned') || !!mentionLabel && Wire.text(data,'kind_label')===mentionLabel));
  }

  listView(mentionLabel:string='', requestLabel:(row:ResourceRow)=>string=() => '', byTime:boolean=false, now:Date=new Date()):InboxListView {
    const buckets = new Map<string,InboxListSection>(), seen = new Set<string>(), query = this.search.trim().toLocaleLowerCase();
    for (const key of INBOX_TIERS) buckets.set(key,new InboxListSection(key));
    if (this.sourceFilter === 'all' && this.accountFilter === 'all' && (!this.kindFilter || this.kindFilter==='agent')) {
      for (const session of this.waitingAgents) {
        if (!query || [session.title,session.detail,requestLabel(session)].join(' ').toLocaleLowerCase().includes(query)) buckets.get('action')!.rows.push(session);
      }
    }
    for (const row of this.items.concat(this.doneItems)) {
      const identity = row.key();
      if (seen.has(identity)) continue;
      seen.add(identity);
      const data = row.data(), source = itemSource(row,data), account = Wire.text(data,'account');
      if (!this.kindMatches(data,mentionLabel)) continue;
      if (this.sourceFilter !== 'all' && source !== this.sourceFilter &&
        !(this.sourceFilter === 'connector' && Object.keys(Wire.object(data,'connector')).length > 0)) continue;
      if (this.accountFilter !== 'all' && account !== this.accountFilter) continue;
      if (query && ![row.title,row.detail,Wire.text(data,'sender'),Wire.text(data,'where'),Wire.text(data,'preview'),Wire.text(data,'account_name')].join(' ').toLocaleLowerCase().includes(query)) continue;
      buckets.get(inboxTier(data))!.rows.push(row);
    }
    const result = new InboxListView();
    const chosen = this.statusFilter;
    for (const key of INBOX_TIERS) {
      const section = buckets.get(key)!;
      if (key === 'action' || key !== 'noise' && (section.rows.length || chosen === key ||
        key === 'done' && this.haveDoneDays && !query && !this.kindFilter && this.sourceFilter === 'all' && this.accountFilter === 'all')) {
        result.choices.push(new InboxQuickFilter(key,section.rows.length));
      }
      if (chosen === 'all' || chosen === key || chosen === 'open' && key !== 'done') {
        if (key === 'done') section.rows.sort((a:ResourceRow,b:ResourceRow) => Wire.text(b.data(),'done_at').localeCompare(Wire.text(a.data(),'done_at')));
        if (section.rows.length) {
          if (key!=='working' && key!=='done') {
            const groups = new Map<string,InboxListSection>();
            for (const row of section.rows) {
              const groupKey = byTime ? inboxTimeGroup(row, now) : inboxSourceGroup(row);
              let group = groups.get(groupKey);
              if (!group) { group = new InboxListSection(groupKey); groups.set(groupKey, group); }
              group.rows.push(row);
            }
            if (groups.size > 1) section.groups = Array.from(groups.values()).sort((a:InboxListSection,b:InboxListSection) =>
              byTime ? inboxTimeOrder(a.key) - inboxTimeOrder(b.key) || b.key.localeCompare(a.key) :
                this.sourceGroupOrder(a.key) - this.sourceGroupOrder(b.key));
          }
          result.sections.push(section);
        }
      }
    }
    result.choices.push(new InboxQuickFilter('all',0));
    return result;
  }
  private sourceGroupOrder(key:string):number {
    if (key==='agents') return 0;
    if (key==='lark' || key==='feishu') return 1;
    if (key.startsWith('mail:')) return 2;
    return 3;
  }
  get filteredItems():ResourceRow[] {
    // Legacy callers may explicitly request every open row in the Hub's list order.
    if (this.statusFilter === 'open') {
      const keys = new Set<string>(this.listView().sections.flatMap((s:InboxListSection) => s.rows.map((r:ResourceRow) => r.key())));
      return this.items.filter((r:ResourceRow) => keys.has(r.key()));
    }
    return this.listView().sections.flatMap((section:InboxListSection) => section.rows);
  }

  private historyRow(data:Record<string,Object>,owner:OwnerSnapshot):ResourceRow | undefined {
    const id = Wire.text(data,'id');
    if (!id || !Wire.text(data,'done_at')) return undefined;
    const row=ResourceRow.from(data,'inbox'); if (!row.manager) row.manager = owner.managerId; return row;
  }
  async loadOlderDone():Promise<void> {
    if (this.historyBusy || this.historyEnded || this.referenceMode) return;
    let owner:OwnerSnapshot;
    try { owner = this.capture(undefined); } catch (_) { return; }
    const rev = this.historyRevision;
    const admit = ():boolean => this.owns(owner,false) && this.historyRevision === rev;
    const query = Wire.payload(['sources','limit'],['all','100']);
    if (this.historyBefore && this.historyBeforeID) { query['before']=this.historyBefore; query['before_id']=this.historyBeforeID; }
    this.historyBusy = true;
    try {
      const result = await owner.repo.call('/api/v1/inbox/done' + managerQuery(owner.managerId,query));
      if (!admit()) return;
      const next = this.doneItems.slice(), known = new Set<string>(next.map((r:ResourceRow) => r.key()));
      for (const item of Wire.rows(result,'items')) {
        const row = this.historyRow(item,owner);
        if (row && !known.has(row.key())) { known.add(row.key()); next.push(row); }
      }
      const cursor = Wire.object(result,'next'), before = Wire.text(cursor,'before'), id=Wire.text(cursor,'before_id');
      this.historyEnded = !before || !id || before === this.historyBefore && id === this.historyBeforeID;
      this.historyBefore=before; this.historyBeforeID=id; this.doneItems=next;
    } catch (error) {
      if (admit()) {
        if (error instanceof HttpFailure && [404,501].includes(error.status)) this.historyEnded=true;
        else this.error=this.errorCode(error as Error);
      }
    } finally { if (admit()) this.historyBusy=false; }
  }
  private async readWaitingAgents(owner:OwnerSnapshot,current:() => boolean):Promise<void> {
    if (!owner.client.sessions?.length) { if (current()) this.waitingAgents=[]; return; }
    try {
      const requests=await owner.repo.decisions();
      if (!current()) return;
      const found:ResourceRow[] = [], ids=new Set<string>();
      for (const item of requests) {
        const nested=Wire.object(item,'interaction'), id=Wire.text(item,'id') || Wire.text(nested,'id');
        const machine=Wire.text(item,'machine_id') || Wire.text(nested,'machine_id'), session=Wire.text(item,'session_id') || Wire.text(nested,'session_id');
        const manager=Wire.text(item,'manager') || Wire.text(item,'manager_id') || Wire.text(nested,'manager') || owner.managerId;
        const identity=JSON.stringify([manager,machine,session,id]);
        if (!id || !session || !machine || ids.has(identity) || manager !== owner.managerId || Wire.bool(item,'muted') || Wire.bool(nested,'muted')) continue;
        const origin=owner.client.sessions.find((row:ResourceRow) => row.machine === machine && (row.session || row.id) === session &&
          (!row.group || row.group === owner.groupId) && (!row.manager || row.manager === manager));
        if (!origin || Wire.bool(origin.data(),'muted')) continue;
        ids.add(identity);
        const request=Object.keys(nested).length ? nested : item, body=origin.data();
        body['inbox_interaction']=id;
        body['inbox_request_kind']=Wire.text(request,'kind');
        body['inbox_request_tool']=Wire.text(request,'tool_name');
        body['inbox_asked_at']=Wire.text(request,'created_at') || Wire.text(item,'created_at');
        const target=ResourceRow.from(body,'session'); target.group=owner.groupId; target.manager=manager;
        target.title=Wire.text(request,'session_title') || Wire.text(item,'session_title') || origin.title;
        target.detail=inboxQuestionPreview(request);
        found.push(target);
      }
      this.waitingAgents=found.sort((a:ResourceRow,b:ResourceRow) =>
        (Date.parse(Wire.text(a.data(),'inbox_asked_at')) || 0)-(Date.parse(Wire.text(b.data(),'inbox_asked_at')) || 0));
    } catch (_) { if (current()) this.waitingAgents=[]; }
  }
  private async readDoneDays(owner:OwnerSnapshot,current:() => boolean):Promise<void> {
    try {
      const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
      const result = await owner.repo.call('/api/v1/inbox/done/days' + managerQuery(owner.managerId,Wire.payload(['sources','tz'],['all',zone])));
      if (current()) this.haveDoneDays=Wire.rows(result,'days').some((day:Record<string,Object>) =>
        /^\d{4}-\d{2}-\d{2}$/.test(Wire.text(day,'day')) && Wire.number(day,'count')>0);
    } catch (_) { /* Older Hubs still list recent completed items in the main response. */ }
  }

  get sourceOptions(): string[] {
    const values = this.items.map((row: ResourceRow) => itemSource(row)).filter((value: string, index: number, all: string[]) => value && all.indexOf(value) === index);
    return ['all'].concat(values);
  }
  get accountOptions(): string[] {
    const values = this.items.map((row: ResourceRow) => Wire.text(row.data(), 'account')).filter((value: string, index: number, all: string[]) => value && all.indexOf(value) === index);
    return ['all'].concat(values);
  }
  get sourceAccounts():string[] {
    return this.accountOptions.filter((key:string) => key!=='all' && this.items.some((row:ResourceRow) => {
      const data=row.data();
      return Wire.text(data,'account')===key && (itemSource(row,data)==='mail' || Object.keys(Wire.object(data,'connector')).length>0);
    }));
  }
  get channelOptions():string[] {
    return this.sourceOptions.filter((key:string) => key==='all' || key==='lark' || this.items.some((row:ResourceRow) => {
      const data=row.data();
      return itemSource(row,data)===key && (!Wire.text(data,'account') || key!=='mail' && !Object.keys(Wire.object(data,'connector')).length);
    }));
  }
  filterAccountName(key:string):string { return this.filterAddresses[key] || this.accountName(key); }
  private mailAllowed(client: ClientModel | undefined = this.client): boolean {
    return !client || typeof client.mailModuleAllowed !== 'function' || client.mailModuleAllowed();
  }
  private async readFilterAccounts(owner:OwnerSnapshot,current:() => boolean):Promise<void> {
    if (!this.mailAllowed(owner.client)) {
      if (current()) this.filterAddresses = {};
      return;
    }
    try {
      const result=await owner.repo.call('/api/v1/mail/accounts'+managerQuery(owner.managerId));
      if (!current()) return;
      const names:Record<string,string>={};
      for (const value of Wire.rows(result,'accounts')) {
        const key=Wire.text(value,'id'), address=Wire.text(value,'email');
        if (key) names[key]=address || Wire.text(value,'name') || key;
      }
      this.filterAddresses=names;
    } catch (_) { if (current()) this.filterAddresses={}; }
  }
  canComposeMail():boolean { return this.mailAllowed() && this.active && !!this.client?.connected && Object.keys(this.filterAddresses).length>0 && this.canWrite(); }
  sourceName(value: string): string {
    if (value === 'all') return 'all';
    if (value === 'mail') return 'mail';
    if (value === 'lark') return 'inbox_source_lark';
    if (value === 'connector') return 'inbox_source_generic';
    const source = this.sourceDefinitions.find((item: Record<string, Object>) => Wire.text(item, 'id') === value);
    return Wire.text(source || {}, 'name', value);
  }
  accountName(value: string): string {
    if (value === 'all') return 'all';
    const row = this.items.find((item: ResourceRow) => Wire.text(item.data(), 'account') === value);
    return row ? Wire.text(row.data(), 'account_name', value) : value;
  }
  isHandled(row: ResourceRow = this.selected): boolean { return done(row); }
  isMail(row: ResourceRow = this.selected): boolean { return itemSource(row, row.key() === this.selectedKey ? this.detail : row.data()) === 'mail'; }
  isLark(row: ResourceRow = this.selected): boolean { return itemSource(row, row.key() === this.selectedKey ? this.detail : row.data()) === 'lark'; }
  isReference(): boolean { return this.referenceMode; }
  isThreadReference(): boolean { return this.referenceMode && this.selected.kind === 'todo_thread'; }
  workspaceThreadTitle(): string { return this.referenceThreadTitle; }
  canReplyReferenceThread(): boolean { return !this.isThreadReference() || this.referenceThreadLoaded && this.currentThreadCanReply; }
  referenceSourceLabel(): string { return this.sourceName(this.currentSource || messagingSource(Wire.text(this.selected.data(), 'channel'))); }
  isGeneric(): boolean { return this.currentIsGeneric; }
  capability(name: string): boolean { return this.sourceCapabilities.includes(name); }
  hasConversation(): boolean { return !!this.currentConversation && !!this.currentSource && (!this.isThreadReference() || this.referenceThreadLoaded); }
  canWrite(): boolean {
    return !this.handoffBusy && !this.attachmentBusy && !['sending', 'saving', 'pending', 'unknown'].includes(this.writeState);
  }
  canSendGeneric(): boolean {
    const threadTargetReady = !this.isThreadReference() || this.referenceThreadLoaded && !!this.currentThreadRootMessage &&
      this.currentThreadCanReply && this.threadReply && this.threadReplyTo === this.currentThreadRootMessage;
    return this.canWrite() && !!this.client?.connected && !!this.boundRepo && threadTargetReady && (!this.referenceMode || !this.replyTargetChanged) &&
      this.currentIsGeneric && !!Wire.text(this.sourceMe, 'user_id') && Wire.bool(this.sourceStatus, 'configured') &&
      Wire.bool(this.sourceStatus, 'connected') && Wire.bool(this.sourceStatus, 'signed_in');
  }
  canReplyToMessage(message: InboxMessage): boolean {
    return this.currentIsGeneric && this.canSendGeneric() && Wire.bool(message.data(), 'can_reply', true);
  }
  canReact(message: InboxMessage): boolean { return this.currentIsGeneric ? this.capability('reactions') && Wire.bool(message.data(), 'can_react', true) : this.isLark() && Wire.bool(message.data(), 'can_react', true); }
  mailRecipient(): string {
    const mail = Wire.object(this.detail, 'mail'), target = Wire.object(this.detail, 'reply_target');
    const targetUid = Wire.number(target, 'uid');
    const message = this.mailMessages.find((item: InboxMailMessage) => item.uid === targetUid);
    const from = message ? Wire.object(message.data(), 'from') : Wire.object(mail, 'from');
    const value = addressLabel(from);
    return value || Wire.text(this.detail, 'sender', this.selected.title);
  }
  replyAccountLabel(): string {
    const data = this.selected.data(), mail = Wire.object(this.detail, 'mail'), conversation = genericConversation(this.detail, this.selected);
    if (this.currentIsGeneric && this.currentAccountName) return this.currentAccountName;
    return Wire.text(this.detail, 'account_name', Wire.text(data, 'account_name', Wire.text(mail, 'account_id', Wire.text(conversation, 'account', Wire.text(data, 'account')))));
  }
  replyRecipient(): string {
    if (this.isThreadReference()) return this.referenceThreadTitle || this.referenceThreadId;
    const source = itemSource(this.selected, this.detail), lark = Wire.object(this.detail, 'lark'), connector = Wire.object(this.detail, 'connector');
    if (source === 'mail') return this.mailRecipient();
    if (Object.keys(connector).length) return Wire.text(connector, 'reply_to', Wire.text(connector, 'label', this.selected.title));
    const conversation = genericConversation(this.detail, this.selected);
    const destination = Wire.text(conversation, 'name', this.selected.title);
    const replyTo = Wire.identifier(lark, 'reply_to');
    return replyTo ? destination + ' · ' + replyTo : destination;
  }
  replyConfirmationSnapshot(text: string): string {
    const client = this.client;
    return JSON.stringify([client ? client.scope() : '', this.selected.manager, itemSource(this.selected, this.detail), this.replyAccountLabel(),
      this.replyRecipient(), this.replyTargetSnapshot(), this.threadReplyTo, this.threadReply, text]);
  }
  replyTargetSnapshot(): string { return targetRevision(this.selected, this.detail); }
  canReplyToItem(): boolean {
    if (this.referenceMode) return false;
    const source = itemSource(this.selected, this.detail), connector = Wire.object(this.detail, 'connector');
    if (source === 'mail') {
      if (!this.mailAllowed()) return false;
      const target = Wire.object(this.detail, 'reply_target');
      return !!this.selected.id && !done(this.selected) && Wire.number(target, 'uid') > 0 && !!Wire.text(target, 'version');
    }
    if (source === 'lark') return !!this.selected.id && !!this.replyRecipient() && !done(this.selected);
    if (Object.keys(connector).length) return Wire.bool(connector, 'can_reply') && !!Wire.text(connector, 'reply_to') && !done(this.selected);
    return false;
  }
  private targetPath(row: ResourceRow): string { return '/api/v1/inbox/' + segment(row.id) + managerQuery(row.manager); }
  private targetActionPath(row: ResourceRow, action: string): string { return '/api/v1/inbox/' + segment(row.id) + '/' + action + managerQuery(row.manager); }
  private detailRow(data: Record<string, Object>, row: ResourceRow): ResourceRow {
    const next = ResourceRow.from(data, 'inbox');
    if (next.id && next.id !== row.id) throw new Error('identity_changed');
    next.id = row.id; next.group = row.group; next.manager = row.manager;
    next.machine = row.machine; next.session = row.session; return next;
  }
  private cloneRow(row: ResourceRow): ResourceRow {
    const next = ResourceRow.from(row.data(), row.kind || 'inbox');
    next.id = row.id || next.id; next.title = row.title || next.title; next.detail = row.detail || next.detail;
    next.group = row.group; next.manager = row.manager || next.manager; next.machine = row.machine || next.machine;
    next.session = row.session || next.session; next.path = row.path || next.path;
    return next;
  }

  async open(client: ClientModel, row?: ResourceRow): Promise<void> {
    this.revision++; this.active = true; this.client = client; this.boundRepo = undefined;
    this.pendingRow = row ? this.cloneRow(row) : undefined;
    this.selected = new ResourceRow(); this.selectedKey = ''; this.detail = {}; this.items = [];
    this.error = ''; this.writeState = ''; this.sourceFilter = 'all'; this.accountFilter = 'all'; this.kindFilter=''; this.filterAddresses={}; this.statusFilter = 'action'; this.search = ''; this.beforeSearch = '';
    this.historyRevision++; this.historyBusy=false; this.historyEnded=false; this.historyBefore=''; this.historyBeforeID=''; this.doneItems=[]; this.waitingAgents=[]; this.haveDoneDays=false;
    this.clearDetailState();
    try { this.boundRepo = client.repo(); } catch (_) { this.error = client.demo ? 'demo_unavailable' : 'offline'; return; }
    if (row) await this.openRow(row); else await this.refresh();
  }

  async close(): Promise<void> {
    try { await this.draftWrite; } catch (_) { }
    try { await this.composeWrite; } catch (_) { }
    if (!this.active) return;
    this.active = false; this.revision++;
    if (this.readTimer !== -1) clearTimeout(this.readTimer); this.readTimer = -1;
    this.visibleReadTarget = ''; this.visibleReadOwner = undefined; this.visibleReadIds = []; this.visibleReadPositions = {}; this.visibleReadBadges = {};
    this.client = undefined; this.boundRepo = undefined; this.pendingRow = undefined; this.selected = new ResourceRow(); this.selectedKey = '';
    this.detail = {}; this.feedMessages = []; this.mailMessages = []; this.attachment = undefined;
    this.clearDetailState();
  }

  private admitWrite(owner: OwnerSnapshot): boolean {
    if (!this.owns(owner)) return false;
    this.writeOutbound = true;
    return true;
  }
  private writeFailureState(error: Error): string {
    if (error instanceof HttpFailure) return error.resultPending ? 'pending' : error.resultUnknown ? 'unknown' : 'failed';
    return this.writeOutbound ? 'unknown' : 'failed';
  }
  private interruptWrite(): void {
    if (this.writeState === 'sending' || this.writeState === 'saving') {
      this.writeState = this.writeOutbound ? 'unknown' : 'failed';
      if (this.writeOutbound) this.error = 'result_unknown';
    }
    this.writeOutbound = false;
  }

  onDisconnected(): void {
    this.interruptWrite();
    this.revision++; this.boundRepo = undefined; this.error = 'offline'; this.loading = false; this.detailLoading = false; this.feedLoading = false; this.draftLoading = true; this.attachmentBusy = false; this.handoffBusy = false;
    if (this.readTimer !== -1) clearTimeout(this.readTimer); this.readTimer = -1;
    this.visibleReadTarget = ''; this.visibleReadOwner = undefined; this.visibleReadIds = []; this.visibleReadPositions = {}; this.visibleReadBadges = {};
  }

  async reconnect(row?: ResourceRow): Promise<void> {
    if (!this.active || !this.client) return;
    if (row && !this.selectedKey && !this.pendingRow) this.pendingRow = this.cloneRow(row);
    let repo: HubRepository;
    try { repo = this.client.repo(); }
    catch (error) { this.error = error instanceof Error ? error.message : 'offline'; return; }
    this.interruptWrite();
    this.boundRepo = repo;
    this.revision++;
    if (this.readTimer !== -1) clearTimeout(this.readTimer); this.readTimer = -1;
    this.visibleReadTarget = ''; this.visibleReadOwner = undefined; this.visibleReadIds = []; this.visibleReadPositions = {}; this.visibleReadBadges = {};
    this.loading = false; this.detailLoading = false; this.feedLoading = false; this.attachmentBusy = false; this.handoffBusy = false;
    this.error = '';
    if (!this.selectedKey && this.pendingRow) {
      const next = this.pendingRow; this.pendingRow = undefined;
      await this.openRow(next); return;
    }
    if (!this.selectedKey) { await this.refresh(); return; }
    const rowSnapshot = this.selected, previousTarget = targetRevision(rowSnapshot, this.detail);
    const previousDraftTarget = this.detailTargetRevision, draft = this.replyDraft, draftWasReady = this.draftReady;
    let owner: OwnerSnapshot;
    try { owner = this.capture(rowSnapshot); }
    catch (error) { this.error = error instanceof Error ? error.message : 'offline'; return; }
    this.detailLoading = true; this.draftLoading = true;
    if (this.referenceMode) {
      const previousThreadRoot = this.isThreadReference() ? '' : this.threadRoot;
      const previousThreadReplyTo = this.threadReplyTo, previousThreadReply = this.threadReply;
      if (!this.isThreadReference()) { this.threadRoot = ''; this.threadReplyTo = ''; this.threadReply = false; }
      await this.loadReference(owner, rowSnapshot);
      if (!this.owns(owner)) return;
      let replyTargetChangedDuringRestore = false;
      if (draftWasReady && previousThreadRoot && !this.isThreadReference() && this.hasConversation()) {
        const restoredThread = await this.openReferenceThread(owner, previousThreadRoot);
        if (!this.owns(owner)) return;
        if (restoredThread) {
          if (previousThreadReplyTo === previousThreadRoot) {
            this.threadReplyTo = previousThreadRoot; this.threadReply = previousThreadReply;
          } else if (previousThreadReplyTo) {
            const target = this.feedMessages.find((item: InboxMessage) => item.id === previousThreadReplyTo);
            if (target && this.currentIsGeneric && Wire.bool(target.data(), 'can_reply', true)) {
              this.threadReplyTo = previousThreadReplyTo; this.threadReply = previousThreadReply;
            } else { this.threadReplyTo = ''; this.threadReply = false; replyTargetChangedDuringRestore = true; }
          } else { this.threadReplyTo = ''; this.threadReply = false; }
        } else { this.threadRoot = ''; this.threadReplyTo = ''; this.threadReply = false; replyTargetChangedDuringRestore = true; }
      }
      if (!draftWasReady) await this.restoreDraft(owner, rowSnapshot);
      if (!this.owns(owner)) return;
      const currentTarget = targetRevision(this.selected, this.detail);
      if (draftWasReady) this.replyDraft = draft;
      const expectedTarget = this.detailTargetRevision || previousDraftTarget || (draftWasReady ? previousTarget : '');
      this.detailTargetRevision = expectedTarget || currentTarget;
      this.replyTargetChanged = this.replyTargetChanged || replyTargetChangedDuringRestore || (!!expectedTarget && expectedTarget !== currentTarget);
      this.draftLoading = false;
      if (this.draftReady && this.replyDraft && !this.replyTargetChanged) await this.persistDraft(owner, rowSnapshot, this.replyDraft);
    } else {
      await this.loadDetail(owner, rowSnapshot);
      if (!this.owns(owner)) return;
      if (!draftWasReady) await this.restoreDraft(owner, rowSnapshot);
      if (this.owns(owner)) this.draftLoading = false;
    }
  }

  private clearDetailState(): void {
    if (this.readTimer !== -1) clearTimeout(this.readTimer); this.readTimer = -1;
    this.detailTargetRevision = ''; this.replyDraft = ''; this.replyTargetChanged = false; this.draftLoading = false; this.draftReady = false;
    this.feedMessages = []; this.mailMessages = []; this.mailReadUids = []; this.visibleReadTarget = ''; this.visibleReadOwner = undefined;
    this.visibleReadIds = []; this.visibleReadPositions = {}; this.visibleReadBadges = {}; this.feedHasOlder = false;
    this.feedBefore = ''; this.threadRoot = ''; this.isTopicList = false; this.currentSource = ''; this.currentConversation = '';
    this.currentAccount = ''; this.currentAccountName = ''; this.currentIsGeneric = false; this.sourceCapabilities = [];
    this.sourceStatus = {}; this.sourceMe = {}; this.quickReactions = []; this.mailTranslation = {}; this.translationError = '';
    this.translatedMailUid = 0; this.translationBusy = false; this.showOriginal = true; this.writeState = ''; this.writeOutbound = false; this.attachment = undefined; this.attachmentBusy = false;
    this.threadReplyTo = ''; this.threadReply = false; this.handoffDraft = ''; this.handoffBusy = false;
    this.referenceMode = false; this.referenceThreadId = ''; this.referenceThreadTitle = '';
    this.currentThreadRootMessage = ''; this.currentThreadCanReply = false; this.referenceThreadLoaded = false;
    this.kbChat = {};
  }

  private capture(row: ResourceRow | undefined = this.selected): OwnerSnapshot {
    const client = this.client, repo = this.boundRepo;
    if (!this.active || !client || !repo) throw new Error('offline');
    if (!client.connected) throw new Error('offline');
    if (client.connectionGeneration() < 1 || client.scope() === '') throw new Error('scope_changed');
    return { client: client, repo: repo, generation: client.connectionGeneration(), scope: client.scope(), pageRevision: this.revision,
      rowKey: row?.key() || '', managerId: row?.manager || client.profile.manager, groupId: client.profile.id };
  }

  private ownsScope(owner: OwnerSnapshot): boolean {
    const client = this.client;
    if (!this.active || !client || client !== owner.client) return false;
    if (client.profile.id !== owner.groupId || client.connectionGeneration() !== owner.generation || client.scope() !== owner.scope) return false;
    try { return client.repo() === owner.repo && this.boundRepo === owner.repo; } catch (_) { return false; }
  }

  private owns(owner: OwnerSnapshot, rowRequired: boolean = true): boolean {
    if (!this.ownsScope(owner) || this.revision !== owner.pageRevision) return false;
    const client = this.client;
    if (!client) return false;
    if (rowRequired && owner.rowKey && this.selected.key() !== owner.rowKey) return false;
    return true;
  }

  async refresh(): Promise<void> {
    if (this.referenceMode) { await this.refreshConversation(); return; }
    let owner: OwnerSnapshot;
    try { owner = this.capture(undefined); } catch (error) { this.error = error instanceof Error ? error.message : 'offline'; return; }
    if (!this.owns(owner, false)) return;
    const request = ++this.listRequest;
    const current = (): boolean => request === this.listRequest && this.owns(owner, false);
    this.loading = true; this.error = '';
    try {
      const result = await owner.repo.call('/api/v1/inbox' + managerQuery(owner.managerId, Wire.payload(['sources'], ['all'])));
      if (!current()) return;
      const rows = Wire.rows(result, 'items').map((item: Record<string, Object>) => {
        const row = ResourceRow.from(item, 'inbox');
        if (!row.manager) row.manager = owner.managerId;
        return row;
      });
      // List reads rebuild membership; detail acknowledgements patch their existing row.
      this.items = rows;
      const recentIds=new Set<string>(rows.map((row:ResourceRow) => row.key()));
      this.doneItems=this.doneItems.filter((row:ResourceRow) => !recentIds.has(row.key()));
      if (!this.selectedKey) {
        this.readDoneDays(owner,current); this.readWaitingAgents(owner,current);
        this.readFilterAccounts(owner,current);
      }
      try {
        const sources = await owner.repo.call('/api/v1/messaging' + managerQuery(owner.managerId));
        if (!current()) return;
        this.sourceDefinitions = Wire.rows(sources, 'sources');
      } catch (_) { if (current()) this.sourceDefinitions = []; }
    } catch (error) { if (current()) this.error = this.errorCode(error as Error); }
    finally { if (current()) this.loading = false; }
  }

  async openRow(row: ResourceRow): Promise<void> {
    if (!this.active || !this.client || !this.boundRepo) return;
    this.pendingRow = undefined;
    const next = this.cloneRow(row);
    next.manager = row.manager || this.client.profile.manager;
    this.revision++; this.selected = next; this.selectedKey = next.key(); this.detail = next.data();
    this.clearDetailState(); this.detailLoading = true;
    this.referenceMode = isTodoReference(next);
    if (this.referenceMode) {
      this.referenceThreadId = next.kind === 'todo_thread' ? Wire.text(next.data(), 'thread_id', next.session) : '';
      this.referenceThreadTitle = next.kind === 'todo_thread' ? Wire.text(next.data(), 'title', next.title) : '';
      this.detail = this.referenceDetail(next);
    }
    this.error = '';
    let owner: OwnerSnapshot;
    try { owner = this.capture(next); } catch (error) { this.error = error instanceof Error ? error.message : 'offline'; this.detailLoading = false; return; }
    this.draftLoading = true; this.draftReady = false;
    // Todo references don't carry an account in their stored contract. Resolve the
    // source's selected/default account before restoring a draft so the storage key
    // is scoped to the account that will actually receive the reply.
    if (this.referenceMode) {
      await this.loadReference(owner, next);
      if (!this.owns(owner)) return;
    }
    await this.restoreDraft(owner, next);
    if (!this.owns(owner)) return;
    if (!this.referenceMode) await this.loadDetail(owner, next);
  }

  private draftStorageKey(owner: OwnerSnapshot, row: ResourceRow): string {
    const data = row.data(), source = itemSource(row), account = Wire.text(data, 'account') || (row.key() === this.selectedKey ? this.currentAccount : '');
    if (isTodoReference(row)) {
      return 'inbox:reply:todo-reference:' + JSON.stringify([owner.groupId, row.manager, row.kind,
        messagingSource(Wire.text(data, 'channel')), account, Wire.text(data, 'chat_id', row.id),
        Wire.text(data, 'message_id', row.kind === 'todo_source' ? row.session : ''),
        Wire.text(data, 'anchor'), Wire.text(data, 'thread_id', row.kind === 'todo_thread' ? row.session : '')]);
    }
    return 'inbox:reply:' + JSON.stringify([owner.groupId, row.manager, source, account, row.id]);
  }

  private async restoreDraft(owner: OwnerSnapshot, row: ResourceRow): Promise<void> {
    this.draftLoading = true; this.draftReady = false;
    const key = this.draftStorageKey(owner, row);
    try {
      const saved = await owner.client.loadView(key);
      if (!this.owns(owner)) return;
      if (saved) {
        const record = Wire.parse(saved);
        this.replyDraft = Wire.text(record, 'text');
        this.detailTargetRevision = Wire.text(record, 'target_revision');
        this.replyTargetChanged = !!this.detailTargetRevision && this.detailTargetRevision !== targetRevision(this.selected, this.detail);
        const savedReplyTo = Wire.text(record, 'reply_to'), savedThread = Wire.bool(record, 'thread');
        if (this.isThreadReference()) {
          if (this.referenceThreadLoaded && this.currentThreadRootMessage && this.currentThreadCanReply) {
            this.threadReplyTo = this.currentThreadRootMessage; this.threadReply = true;
          } else { this.threadReplyTo = ''; this.threadReply = false; }
        } else if (this.referenceMode) {
          const savedThreadRoot = Wire.text(record, 'thread_root');
          const threadRestored = savedThreadRoot ? await this.openReferenceThread(owner, savedThreadRoot) : false;
          if (!this.owns(owner)) return;
          if (savedThreadRoot && !threadRestored) this.replyTargetChanged = true;
          if (!savedReplyTo) { this.threadReplyTo = ''; this.threadReply = false; }
          else if (savedThreadRoot && savedReplyTo === savedThreadRoot && threadRestored && this.currentThreadCanReply) {
            this.threadReplyTo = savedReplyTo; this.threadReply = savedThread;
          } else {
            const target = this.feedMessages.find((item: InboxMessage) => item.id === savedReplyTo);
            if (target && this.currentIsGeneric && Wire.bool(target.data(), 'can_reply', true) && !this.replyTargetChanged) {
              this.threadReplyTo = savedReplyTo; this.threadReply = savedThread;
            } else { this.threadReplyTo = ''; this.threadReply = false; this.replyTargetChanged = true; }
          }
        } else {
          this.threadReplyTo = savedReplyTo; this.threadReply = savedThread;
        }
      }
      this.draftReady = true;
    } catch (_) { if (this.owns(owner)) { this.replyDraft = ''; this.draftReady = true; this.error = 'draft_restore_failed'; } }
    finally { if (this.owns(owner)) this.draftLoading = false; }
  }

  private async openReferenceThread(owner: OwnerSnapshot, rootMessageId: string): Promise<boolean> {
    if (!rootMessageId || !this.currentConversation) return false;
    try {
      if (this.currentIsGeneric) {
        if (!this.capability('threads') && !this.capability('topics')) return false;
        const result = await owner.repo.call('/api/v1/messaging/' + segment(this.currentSource) + '/messages/' + segment(rootMessageId) +
          managerQuery(owner.managerId, Wire.payload(['account'], [this.currentAccount])));
        if (!this.owns(owner)) return false;
        if (!this.messageAccountMatches(result, false)) return false;
        const message = Wire.object(result, 'message');
        if (Wire.text(message, 'id') !== rootMessageId) { this.error = 'inbox_reference_changed'; return false; }
        this.currentThreadCanReply = Wire.bool(message, 'can_reply', true);
        await this.openThread(new InboxMessage(message));
        return this.owns(owner) && this.threadRoot === rootMessageId && !this.error;
      }
      if (this.currentSource !== 'lark') return false;
      this.currentThreadCanReply = true;
      await this.openThread(new InboxMessage(Wire.payload(['id'], [rootMessageId])));
      return this.owns(owner) && this.threadRoot === rootMessageId && !this.error;
    } catch (error) {
      if (this.owns(owner)) this.error = this.errorCode(error as Error);
      return false;
    }
  }

  private referenceDetail(row: ResourceRow): Record<string, Object> {
    const data = row.data(), source = messagingSource(Wire.text(data, 'channel'));
    const conversationId = Wire.text(data, 'chat_id', row.id), account = Wire.text(data, 'account');
    const conversation = Wire.payload(['source', 'id', 'account', 'name', 'start_position'],
      [source, conversationId, account, conversationId, '']);
    const detail = Wire.payload(['id', 'kind', 'source', 'account', 'account_name', 'title', 'manager', 'conversation', 'channel',
      'chat_id', 'message_id', 'anchor', 'thread_id', 'root_message_id'],
    [conversationId, 'message', source, account, Wire.text(data, 'account_name'), row.title, row.manager, conversation,
      Wire.text(data, 'channel'), conversationId, Wire.text(data, 'message_id', row.kind === 'todo_source' ? row.session : ''),
      Wire.text(data, 'anchor'), Wire.text(data, 'thread_id', row.kind === 'todo_thread' ? row.session : ''), Wire.text(data, 'root_message_id')]);
    return detail;
  }

  private async loadReference(owner: OwnerSnapshot, row: ResourceRow): Promise<void> {
    const detail = this.detail, data = row.data();
    const conversation = Wire.object(detail, 'conversation'), source = Wire.text(conversation, 'source');
    const conversationId = Wire.text(conversation, 'id');
    if (!source || !conversationId) { if (this.owns(owner)) { this.error = 'inbox_reference_unavailable'; this.detailLoading = false; } return; }
    const isThread = row.kind === 'todo_thread';
    const rootMessageId = Wire.text(data, 'root_message_id', Wire.text(data, 'message_id'));
    this.referenceThreadLoaded = false;
    this.currentThreadRootMessage = isThread ? rootMessageId : '';
    this.currentThreadCanReply = false;
    try {
      await this.loadConversation(owner, row, detail, false);
      if (!this.owns(owner)) return;
      if (this.error) { this.currentSource = ''; this.currentConversation = ''; this.currentIsGeneric = false; return; }
      if (!this.currentSource || !this.currentConversation || !this.currentIsGeneric && this.currentSource !== 'lark') return;
      if (isThread) {
        const supportsThreadRead = this.currentIsGeneric ? this.capability('threads') || this.capability('topics') : source === 'lark';
        if (!rootMessageId || !supportsThreadRead) return;
        if (this.currentIsGeneric) {
          const result = await owner.repo.call('/api/v1/messaging/' + segment(source) + '/messages/' + segment(rootMessageId) +
            managerQuery(owner.managerId, Wire.payload(['account'], [this.currentAccount])));
          if (!this.owns(owner)) return;
          if (!this.messageAccountMatches(result, false)) return;
          const message = Wire.object(result, 'message');
          if (Wire.text(message, 'id') !== rootMessageId) { this.error = 'inbox_reference_changed'; return; }
          this.currentThreadCanReply = Wire.bool(message, 'can_reply', true);
          await this.openThread(new InboxMessage(message));
        } else {
          await this.openThread(new InboxMessage(Wire.payload(['id'], [rootMessageId])));
        }
        return;
      }

      let start = '';
      if (this.currentIsGeneric) start = await this.resolveReferencePosition(owner, source, this.currentAccount, row);
      if (!this.owns(owner)) return;
      if (this.error === 'account_changed') { this.currentSource = ''; this.currentConversation = ''; this.currentIsGeneric = false; return; }
      if (start) {
        const next = Wire.parse(JSON.stringify(this.detail));
        const nextConversation = Wire.object(next, 'conversation'); nextConversation['start_position'] = start;
        next['conversation'] = nextConversation; this.detail = next;
      }
      if (this.currentIsGeneric) await this.loadGenericMessages(owner, conversationId, this.currentAccount, start, false);
      else await this.loadLegacyLarkMessages(owner, conversationId, this.currentAccount, start, false);
    } catch (error) { if (this.owns(owner)) this.error = this.errorCode(error as Error); }
    finally { if (this.owns(owner)) this.detailLoading = false; }
  }

  private async resolveReferencePosition(owner: OwnerSnapshot, source: string, account: string, row: ResourceRow): Promise<string> {
    const data = row.data(), first = Wire.text(data, 'message_id', row.session), second = Wire.text(data, 'anchor');
    const ids = [first, second].filter((value: string, index: number, values: string[]) => !!value && values.indexOf(value) === index);
    for (const id of ids) {
      if (!this.owns(owner)) return '';
      try {
        const result = await owner.repo.call('/api/v1/messaging/' + segment(source) + '/messages/' + segment(id) +
          managerQuery(owner.managerId, Wire.payload(['account'], [account])));
        if (!this.owns(owner)) return '';
        if (!this.messageAccountMatches(result, false)) return '';
        const message = Wire.object(result, 'message');
        if (Wire.text(message, 'id') !== id) continue;
        const position = Wire.text(message, 'position');
        if (/^\d{1,40}$/.test(position)) return position;
      } catch (_) { if (!this.owns(owner)) return ''; }
    }
    return '';
  }

  setReplyDraft(value: string): void {
    if (!this.draftReady || !this.selectedKey) return;
    this.replyDraft = value;
    this.replyTargetChanged = false;
    let owner: OwnerSnapshot;
    try { owner = this.capture(); } catch (_) { return; }
    const text = value, target = targetRevision(this.selected, this.detail), replyTo = this.threadReplyTo, thread = this.threadReply;
    const threadRoot = this.referenceMode ? this.threadRoot : '', key = this.draftStorageKey(owner, this.selected);
    this.draftWrite = this.draftWrite.catch(() => {}).then(async () => {
      if (!this.ownsScope(owner)) return;
      await owner.client.saveView(key, JSON.stringify(Wire.payload(['text', 'target_revision', 'reply_to', 'thread', 'thread_root'], [text, target, replyTo, thread, threadRoot])));
      if (this.owns(owner)) this.detailTargetRevision = target;
    }).catch(() => { if (this.owns(owner)) this.error = 'draft_save_failed'; });
  }

  private async persistDraft(owner: OwnerSnapshot, row: ResourceRow, text: string): Promise<void> {
    if (!this.owns(owner)) return;
    const key = this.draftStorageKey(owner, row), target = targetRevision(row, this.detail), threadRoot = this.referenceMode ? this.threadRoot : '';
    await owner.client.saveView(key, JSON.stringify(Wire.payload(['text', 'target_revision', 'reply_to', 'thread', 'thread_root'], [text, target, this.threadReplyTo, this.threadReply, threadRoot])));
    if (!this.owns(owner)) return;
    this.detailTargetRevision = target;
  }

  private async loadDetail(owner: OwnerSnapshot, row: ResourceRow): Promise<void> {
    if (isTodoReference(row)) { await this.loadReference(owner, row); return; }
    try {
      const result = await owner.repo.call(this.targetPath(row));
      if (!this.owns(owner)) return;
      const item = Wire.object(result, 'item'); const data = Object.keys(item).length ? item : result;
      const updated = this.detailRow(data, row);
      if (updated.id && updated.id !== row.id) { this.error = 'identity_changed'; return; }
      updated.id = row.id; this.selected = updated; this.selectedKey = updated.key(); this.detail = data;
      const topKbChat = Wire.object(result, 'kb_chat');
      this.kbChat = Object.keys(topKbChat).length ? topKbChat : Wire.object(data, 'kb_chat');
      const nextTargetRevision = targetRevision(updated, data);
      if (this.detailTargetRevision) this.replyTargetChanged = this.detailTargetRevision !== nextTargetRevision;
      this.detailTargetRevision = nextTargetRevision;
      this.items = updateResourceList(this.items, updated);
      this.error = '';
      const source = itemSource(updated, data);
      if (source === 'mail') {
        if (this.mailAllowed(owner.client)) await this.loadMail(owner, updated, data);
        else this.mailMessages = [];
      }
      else if (Object.keys(genericConversation(data, updated)).length || Object.keys(Wire.object(data, 'lark')).length) await this.loadConversation(owner, updated, data);
    } catch (error) { if (this.owns(owner)) this.error = this.errorCode(error as Error); }
    finally { if (this.owns(owner)) this.detailLoading = false; }
  }

  async refreshDetail(): Promise<void> {
    if (!this.selectedKey) return;
    if (this.referenceMode) { await this.refreshConversation(); return; }
    let owner: OwnerSnapshot;
    try { owner = this.capture(); } catch (error) { this.error = error instanceof Error ? error.message : 'offline'; return; }
    this.detailLoading = true; await this.loadDetail(owner, this.selected);
  }

  private async loadMail(owner: OwnerSnapshot, row: ResourceRow, detail: Record<string, Object>): Promise<void> {
    const mail = Wire.object(detail, 'mail'), account = Wire.text(mail, 'account_id'), mailbox = Wire.text(mail, 'mailbox', 'INBOX');
    const uids = numberArray(mail, 'uids');
    if (!account || !uids.length) { if (this.owns(owner)) this.error = 'mail_detail_unavailable'; return; }
    const messages: InboxMailMessage[] = [];
    for (const uid of uids) {
      if (!this.owns(owner)) return;
      const path = '/api/v1/mail/messages/' + segment(account) + '/' + String(uid) + managerQuery(owner.managerId, Wire.payload(['mailbox'], [mailbox]));
      const result = await owner.repo.call(path);
      if (!this.owns(owner)) return;
      const message = Wire.object(result, 'message');
      if (Object.keys(message).length) messages.push(new InboxMailMessage(message));
    }
    if (!this.owns(owner)) return;
    this.mailMessages = messages; this.showOriginal = true;
    this.mailReadUids = messages.filter((item: InboxMailMessage) => Wire.bool(item.data(), 'seen')).map((item: InboxMailMessage) => item.uid);
  }

  async markMailVisible(uid: number): Promise<void> {
    if (!this.isMail() || !this.mailAllowed() || this.mailReadUids.includes(uid) || !Number.isSafeInteger(uid) || uid <= 0) return;
    let owner: OwnerSnapshot;
    try { owner = this.capture(); } catch (_) { return; }
    const mail = Wire.object(this.detail, 'mail'), mailbox = Wire.text(mail, 'mailbox', 'INBOX'), account = Wire.text(mail, 'account_id');
    if (!account || !numberArray(mail, 'uids').includes(uid)) return;
    try {
      await owner.repo.call('/api/v1/mail/messages/' + segment(account) + '/' + String(uid) + '/flags' + managerQuery(owner.managerId), 'POST', Wire.payload(['mailbox', 'seen'], [mailbox, true]));
      if (!this.owns(owner)) return;
      this.mailReadUids = this.mailReadUids.concat([uid]);
    } catch (error) { if (this.owns(owner)) this.error = this.errorCode(error as Error); }
  }

  async downloadMailAttachment(message: InboxMailMessage, attachment: Record<string, Object>): Promise<InboxAttachmentContent> {
    const owner = this.capture();
    const id = Wire.text(attachment, 'id'), name = Wire.text(attachment, 'filename', 'attachment'), declaredMime = Wire.text(attachment, 'mime');
    const mime = declaredMime || 'application/octet-stream';
    const size = Wire.number(attachment, 'size');
    if (!id || !Number.isSafeInteger(size) || size < 0 || size > MAX_MAIL_ATTACHMENT_BYTES || !message.accountId || !Number.isSafeInteger(message.uid) || message.uid <= 0) throw new Error('attachment_limit');
    const path = '/api/v1/mail/messages/' + segment(message.accountId) + '/' + String(message.uid) + '/attachments/' + segment(id) + managerQuery(owner.managerId, Wire.payload(['mailbox'], [message.mailbox]));
    const response = await owner.repo.transport.requestBytes(path, 'GET');
    if (!this.owns(owner) || response.data.byteLength > MAX_MAIL_ATTACHMENT_BYTES || size > 0 && response.data.byteLength !== size) throw new Error('attachment_changed');
    const actualMime = response.contentType.split(';')[0].trim().toLowerCase();
    if (declaredMime && actualMime && declaredMime.toLowerCase() !== actualMime) throw new Error('attachment_mime_changed');
    return { data: response.data, name: name, mime: actualMime || mime };
  }

  async downloadMessageMedia(media: string, name: string = 'attachment', expectedMime: string = ''): Promise<InboxAttachmentContent> {
    const owner = this.capture();
    let path = '';
    if (this.currentIsGeneric) {
      const relative = safeMessagingMedia(media);
      if (!relative || /(?:^|[?&])account=/i.test(relative)) throw new Error('media_unsupported');
      const queryAt = relative.indexOf('?'), pathname = queryAt >= 0 ? relative.slice(0, queryAt) : relative;
      const tail = queryAt >= 0 ? relative.slice(queryAt) : '';
      const accountQuery = managerQuery(owner.managerId, Wire.payload(['account'], [this.currentAccount]));
      const combined = tail ? tail + (accountQuery ? '&' + accountQuery.slice(1) : '') : accountQuery;
      path = '/api/v1/messaging/' + segment(this.currentSource) + '/' + pathname + combined;
    } else {
      const key = media.replace(/^media\//, '');
      if (!key || key.length > 2000 || /[\/\\\r\n#\u0000-\u001f]/.test(key) || key.split('/').some((part: string) => part === '.' || part === '..'))
        throw new Error('media_unsupported');
      path = '/api/v1/lark/media/' + segment(key) + managerQuery(owner.managerId, Wire.payload(['ol_account'], [this.currentAccount]));
    }
    const response = await owner.repo.transport.requestBytes(path, 'GET');
    if (!this.owns(owner)) throw new Error('scope_changed');
    if (!response.data.byteLength || response.data.byteLength > MAX_MAIL_ATTACHMENT_BYTES) throw new Error('attachment_limit');
    const actualMime = response.contentType.split(';')[0].trim().toLowerCase();
    if (!actualMime || expectedMime && expectedMime.toLowerCase() !== actualMime) throw new Error('attachment_mime_changed');
    if (actualMime.startsWith('image/') && response.data.byteLength > 7.5 * 1024 * 1024) throw new Error('attachment_limit');
    return { data: response.data, name: name, mime: actualMime };
  }

  private async loadConversation(owner: OwnerSnapshot, row: ResourceRow, detail: Record<string, Object>, loadMessages: boolean = true): Promise<void> {
    const conversation = genericConversation(detail, row), lark = Wire.object(detail, 'lark');
    let source = Wire.text(conversation, 'source'), conversationId = Wire.text(conversation, 'id');
    let account = Wire.text(conversation, 'account');
    const generic = !!source && !!conversationId && !!this.client && this.client.features.includes('messaging');
    const legacyLark = source === 'lark' || !conversationId && !!Wire.text(lark, 'feed_id');
    if (!generic && !conversationId && legacyLark) { source = 'lark'; conversationId = Wire.text(lark, 'feed_id'); account = Wire.text(lark, 'ol_account'); }
    if (!source || !conversationId) return;
    if (!generic && !legacyLark) { this.error = 'messaging_source_unavailable'; return; }
    this.currentSource = source; this.currentConversation = conversationId; this.currentAccount = account;
    this.currentIsGeneric = generic;
    if (generic) {
      if (!this.owns(owner)) return;
      const sourceList = await owner.repo.call('/api/v1/messaging' + managerQuery(owner.managerId));
      if (!this.owns(owner)) return;
      this.sourceDefinitions = Wire.rows(sourceList, 'sources');
      const definition = this.sourceDefinitions.find((item: Record<string, Object>) => Wire.text(item, 'id') === source);
      if (!definition) { this.error = 'messaging_source_unavailable'; return; }
      this.sourceCapabilities = Wire.strings(definition, 'capabilities');
      if (this.sourceCapabilities.includes('accounts')) {
        const accounts = await owner.repo.call('/api/v1/messaging/' + segment(source) + '/accounts' + managerQuery(owner.managerId));
        if (!this.owns(owner)) return;
        const values = Wire.rows(accounts, 'accounts'), defaultId = Wire.text(accounts, 'default');
        const accountId = account || defaultId;
        const accountRecord = values.find((item: Record<string, Object>) => Wire.text(item, 'id') === accountId);
        if (!accountId || !accountRecord) { this.error = 'account_changed'; return; }
        this.currentAccount = accountId;
        this.currentAccountName = Wire.text(accountRecord, 'name', Wire.text(accountRecord, 'email', accountId));
        if (isTodoReference(row)) {
          const resolved = Wire.parse(JSON.stringify(this.detail));
          resolved['account'] = accountId;
          const resolvedConversation = Wire.object(resolved, 'conversation'); resolvedConversation['account'] = accountId;
          resolved['conversation'] = resolvedConversation; this.detail = resolved;
        }
      }
      this.sourceStatus = await owner.repo.call('/api/v1/messaging/' + segment(source) + '/status' + managerQuery(owner.managerId, Wire.payload(['account'], [this.currentAccount])));
      if (!this.owns(owner)) return;
      this.sourceMe = await owner.repo.call('/api/v1/messaging/' + segment(source) + '/me' + managerQuery(owner.managerId, Wire.payload(['account'], [this.currentAccount])));
      if (!this.owns(owner)) return;
      if (!this.messageAccountMatches(this.sourceMe, false)) return;
      const meAccount = Wire.text(this.sourceMe, 'account', this.currentAccount);
      if (this.currentAccount && meAccount !== this.currentAccount) { this.error = 'account_changed'; return; }
      if (!this.currentAccountName) this.currentAccountName = Wire.text(this.sourceStatus, 'name', Wire.text(this.sourceMe, 'name', Wire.text(row.data(), 'account_name')));
      if (this.sourceCapabilities.includes('reactions')) {
        const quick = await owner.repo.call('/api/v1/messaging/' + segment(source) + '/reactions/quick' + managerQuery(owner.managerId, Wire.payload(['account'], [this.currentAccount])));
        if (!this.owns(owner)) return;
        this.quickReactions = Wire.strings(quick, 'keys').slice(0, 8);
      }
      if (loadMessages) await this.loadGenericMessages(owner, conversationId, this.currentAccount, Wire.text(conversation, 'start_position'), Wire.bool(conversation, 'topic'));
    } else {
      this.sourceCapabilities = Wire.strings(Wire.object(detail, 'capabilities'), 'capabilities');
      const start = Wire.text(lark, 'start_position');
      const topic = Wire.bool(lark, 'is_topic');
      if (loadMessages) await this.loadLegacyLarkMessages(owner, conversationId, account, start, topic);
    }
  }

  private async loadGenericMessages(owner: OwnerSnapshot, conversationId: string, account: string, start: string, topic: boolean): Promise<void> {
    if (!this.owns(owner)) return;
    this.feedLoading = true;
    const path = '/api/v1/messaging/' + segment(this.currentSource) + '/conversations/' + segment(conversationId) + '/messages' + managerQuery(owner.managerId,
      Wire.payload(['limit', 'around', 'topic', 'account'], [String(MAX_MESSAGE_PAGE), start, topic ? '1' : '', account]));
    try {
      const result = await owner.repo.call(path);
      if (!this.owns(owner)) return;
      if (!this.messageAccountMatches(result, false)) return;
      this.applyMessagePage(result, false);
    } catch (error) { if (this.owns(owner)) this.error = this.errorCode(error as Error); }
    finally { if (this.owns(owner)) this.feedLoading = false; }
  }

  private async loadLegacyLarkMessages(owner: OwnerSnapshot, feedId: string, account: string, start: string, topic: boolean): Promise<void> {
    if (!this.owns(owner)) return;
    this.feedLoading = true;
    const path = '/api/v1/lark/conversations/' + segment(feedId) + '/messages' + managerQuery(owner.managerId,
      Wire.payload(['limit', 'around', 'topic', 'ol_account'], [String(MAX_MESSAGE_PAGE), start, topic ? '1' : '', account]));
    try {
      const result = await owner.repo.call(path);
      if (!this.owns(owner)) return;
      this.applyMessagePage(result, true);
    } catch (error) { if (this.owns(owner)) this.error = this.errorCode(error as Error); }
    finally { if (this.owns(owner)) this.feedLoading = false; }
  }

  private applyMessagePage(result: Record<string, Object>, legacy: boolean): void {
    this.feedMessages = Wire.rows(result, 'messages').map((item: Record<string, Object>) => new InboxMessage(item));
    this.isTopicList = Wire.bool(result, 'topic') || this.feedMessages.some((message: InboxMessage) => Wire.text(message.data(), 'thread_kind') === 'topic');
    this.feedHasOlder = Wire.bool(result, 'has_older');
    this.feedBefore = Wire.text(result, 'older_cursor', Wire.text(result, 'older_before', ''));
    const root = Wire.object(result, 'root'); this.threadRoot = Wire.text(root, 'id', this.threadRoot);
  }

  private messageAccountMatches(result: Record<string, Object>, legacy: boolean): boolean {
    if (!legacy && this.currentIsGeneric && this.capability('accounts') && Wire.text(result, 'account') !== this.currentAccount) {
      this.error = 'account_changed'; return false;
    }
    return true;
  }

  async loadOlder(): Promise<void> {
    if (!this.feedHasOlder || this.feedLoading || !this.selectedKey) return;
    const owner = this.capture();
    const source = this.currentSource, conversationId = this.currentConversation, account = this.currentAccount, before = this.feedBefore;
    if (!before || !conversationId) return;
    this.feedLoading = true;
    try {
      let path = '';
      if (this.currentIsGeneric) path = '/api/v1/messaging/' + segment(source) + '/conversations/' + segment(conversationId) + '/messages' + managerQuery(owner.managerId,
        Wire.payload(['limit', 'before', 'topic', 'account'], [String(MAX_MESSAGE_PAGE), before, this.isTopicList ? '1' : '', account]));
      else path = '/api/v1/lark/conversations/' + segment(conversationId) + '/messages' + managerQuery(owner.managerId,
        Wire.payload(['limit', 'before', 'topic', 'ol_account'], [String(MAX_MESSAGE_PAGE), before, this.isTopicList ? '1' : '', account]));
      const result = await owner.repo.call(path);
      if (!this.owns(owner)) return;
      if (!this.messageAccountMatches(result, !this.currentIsGeneric)) return;
      const older = Wire.rows(result, 'messages').map((item: Record<string, Object>) => new InboxMessage(item));
      const seen = new Set<string>(this.feedMessages.map((item: InboxMessage) => item.id));
      this.feedMessages = older.filter((item: InboxMessage) => item.id && !seen.has(item.id)).concat(this.feedMessages);
      this.feedHasOlder = Wire.bool(result, 'has_older'); this.feedBefore = Wire.text(result, 'older_cursor', before);
    } catch (error) { if (this.owns(owner)) this.error = this.errorCode(error as Error); }
    finally { if (this.owns(owner)) this.feedLoading = false; }
  }

  async openThread(message: InboxMessage): Promise<void> {
    const openingReferenceThread = this.isThreadReference() && this.currentThreadRootMessage === message.id && !!this.currentConversation;
    if (!this.selectedKey || !message.id || !this.hasConversation() && !openingReferenceThread) return;
    if (this.currentIsGeneric && !(this.capability('threads') || this.capability('topics'))) return;
    const owner = this.capture(), source = this.currentSource, account = this.currentAccount;
    if (this.isThreadReference()) this.referenceThreadLoaded = false;
    const root = message.id; this.threadRoot = root;
    this.threadReplyTo = this.currentIsGeneric && !Wire.bool(message.data(), 'can_reply', true) ? '' : root;
    this.threadReply = !!this.threadReplyTo; this.feedLoading = true;
    try {
      const path = this.currentIsGeneric ? '/api/v1/messaging/' + segment(source) + '/messages/' + segment(message.id) + '/thread' + managerQuery(owner.managerId,
        Wire.payload(['limit', 'account'], [String(MAX_MESSAGE_PAGE), account]))
        : '/api/v1/lark/messages/' + segment(message.id) + '/thread' + managerQuery(owner.managerId, Wire.payload(['limit', 'ol_account'], [String(MAX_MESSAGE_PAGE), account]));
      const result = await owner.repo.call(path);
      if (!this.owns(owner)) return;
      if (!this.messageAccountMatches(result, !this.currentIsGeneric)) return;
      const returnedRoot = Wire.text(Wire.object(result, 'root'), 'id');
      if (this.isThreadReference() && returnedRoot !== this.currentThreadRootMessage) {
        this.error = 'inbox_reference_changed'; this.threadRoot = ''; this.threadReplyTo = ''; this.threadReply = false; return;
      }
      this.applyMessagePage(result, !this.currentIsGeneric);
      this.threadRoot = Wire.text(Wire.object(result, 'root'), 'id', root);
      const canReply = this.isThreadReference() ? this.currentThreadCanReply : !this.currentIsGeneric || Wire.bool(message.data(), 'can_reply', true);
      this.threadReplyTo = canReply ? this.threadRoot : '';
      this.threadReply = !!this.threadReplyTo;
      this.feedHasOlder = Wire.bool(result, 'has_older'); this.feedBefore = Wire.text(result, 'older_cursor');
      if (this.isThreadReference()) this.referenceThreadLoaded = true;
    } catch (error) {
      if (this.owns(owner)) {
        this.error = this.errorCode(error as Error);
        if (this.isThreadReference()) { this.threadRoot = ''; this.threadReplyTo = ''; this.threadReply = false; this.referenceThreadLoaded = false; }
      }
    }
    finally { if (this.owns(owner)) this.feedLoading = false; }
  }

  async closeThread(): Promise<void> {
    if (!this.threadRoot || !this.selectedKey || !this.currentConversation) return;
    const owner = this.capture(), source = this.currentSource, conversation = this.currentConversation, account = this.currentAccount;
    const start = Wire.text(Wire.object(this.detail, 'conversation'), 'start_position', Wire.text(Wire.object(this.detail, 'lark'), 'start_position'));
    this.threadRoot = ''; this.threadReplyTo = ''; this.threadReply = false; this.feedBefore = ''; this.feedHasOlder = false;
    if (this.currentIsGeneric) await this.loadGenericMessages(owner, conversation, account, start, false);
    else await this.loadLegacyLarkMessages(owner, conversation, account, start, false);
    if (!this.owns(owner)) return;
  }

  async markMessageVisible(message: InboxMessage): Promise<void> {
    if (!this.hasConversation() || !message.id || !message.position || message.mine || ['pending', 'failed'].includes(message.state)) return;
    let owner: OwnerSnapshot; try { owner = this.capture(); } catch (_) { return; }
    const target = JSON.stringify([owner.scope, owner.generation, owner.pageRevision, owner.rowKey, this.currentSource, this.currentConversation, this.currentAccount]);
    if (this.visibleReadTarget !== target) {
      this.visibleReadTarget = target; this.visibleReadOwner = owner; this.visibleReadIds = []; this.visibleReadPositions = {}; this.visibleReadBadges = {};
    }
    if (!this.visibleReadIds.includes(message.id)) this.visibleReadIds = this.visibleReadIds.concat([message.id]);
    const positions: Record<string, string> = {};
    Object.keys(this.visibleReadPositions).forEach((id: string) => positions[id] = this.visibleReadPositions[id]);
    positions[message.id] = message.position; this.visibleReadPositions = positions;
    const badges: Record<string, number> = {};
    Object.keys(this.visibleReadBadges).forEach((id: string) => badges[id] = this.visibleReadBadges[id]);
    badges[message.id] = Wire.number(message.data(), 'badge_count'); this.visibleReadBadges = badges;
    if (this.readTimer !== -1) clearTimeout(this.readTimer);
    this.readTimer = setTimeout(() => {
      this.readTimer = -1;
      this.flushVisibleRead(target).catch((error: Error) => { if (this.active && this.visibleReadTarget === target) this.error = this.errorCode(error); });
    }, 300);
  }

  private async flushVisibleRead(target: string): Promise<void> {
    if (!this.hasConversation() || this.visibleReadTarget !== target || !this.visibleReadIds.length) return;
    const owner = this.visibleReadOwner;
    if (!owner || !this.owns(owner) || owner.rowKey !== this.selected.key()) return;
    const source = this.currentSource, conversation = this.currentConversation, account = this.currentAccount, generic = this.currentIsGeneric;
    const ids = this.visibleReadIds.slice(0, 100), sentPositions: Record<string, string> = {};
    let position = '', positionId = '';
    ids.forEach((id: string) => {
      const value = this.visibleReadPositions[id] || '';
      sentPositions[id] = value;
      if (value && (!position || comparePosition(value, position) > 0)) { position = value; positionId = id; }
    });
    if (!position) return;
    const path = generic ? '/api/v1/messaging/' + segment(source) + '/conversations/' + segment(conversation) + '/read' + managerQuery(owner.managerId, Wire.payload(['account'], [account]))
      : '/api/v1/lark/conversations/' + segment(conversation) + '/read' + managerQuery(owner.managerId, Wire.payload(['ol_account'], [account]));
    const numeric = Number(position);
    if (!generic && !Number.isSafeInteger(numeric)) throw new Error('read_position_unsafe');
    const body: Record<string, Object> = Wire.payload(['position', 'message_ids'], [generic ? position : numeric, ids]);
    if (!generic) body['badge_count'] = this.visibleReadBadges[positionId] || 0;
    await owner.repo.call(path, 'POST', body);
    if (!this.owns(owner)) return;
    const remaining: string[] = [], positions: Record<string, string> = {}, badges: Record<string, number> = {};
    this.visibleReadIds.forEach((id: string) => {
      if (ids.includes(id) && this.visibleReadPositions[id] === sentPositions[id]) return;
      remaining.push(id); positions[id] = this.visibleReadPositions[id] || ''; badges[id] = this.visibleReadBadges[id] || 0;
    });
    this.visibleReadIds = remaining; this.visibleReadPositions = positions; this.visibleReadBadges = badges;
    if (!remaining.length) { this.visibleReadTarget = ''; this.visibleReadOwner = undefined; }
    else {
      this.readTimer = setTimeout(() => {
        this.readTimer = -1; this.flushVisibleRead(target).catch((error: Error) => { if (this.owns(owner)) this.error = this.errorCode(error); });
      }, 0);
    }
  }

  private async readCurrentMe(owner: OwnerSnapshot): Promise<Record<string, Object>> {
    const result = await owner.repo.call('/api/v1/messaging/' + segment(this.currentSource) + '/me' + managerQuery(owner.managerId, Wire.payload(['account'], [this.currentAccount])));
    if (!this.owns(owner)) throw new Error('scope_changed');
    const account = Wire.text(result, 'account', this.currentAccount);
    if (this.currentAccount && account !== this.currentAccount) throw new HttpFailure(409, 'account_changed');
    if (!Wire.text(result, 'user_id')) throw new HttpFailure(403, 'unknown_sender');
    return result;
  }

  async setThreadReply(value: string, replyTo: string = this.threadReplyTo, thread: boolean = this.threadReply): Promise<void> {
    if (this.isThreadReference()) { replyTo = this.currentThreadRootMessage; thread = true; }
    this.replyDraft = value; this.threadReplyTo = replyTo; this.threadReply = thread;
    if (this.draftReady && this.selectedKey) this.setReplyDraft(value);
  }

  setThreadTarget(replyTo: string, thread: boolean = false): void {
    if (!this.currentIsGeneric) return;
    if (this.isThreadReference()) {
      if (!this.referenceThreadLoaded || !this.currentThreadRootMessage) return;
      replyTo = this.currentThreadRootMessage; thread = true;
    }
    if (replyTo) {
      const target = this.feedMessages.find((item: InboxMessage) => item.id === replyTo);
      if (!target || !this.canReplyToMessage(target)) return;
    }
    this.threadReplyTo = replyTo; this.threadReply = !!replyTo && thread;
    if (this.draftReady) this.setReplyDraft(this.replyDraft);
  }

  async sendConversation(text: string, replyTo: string = this.threadReplyTo, thread: boolean = this.threadReply): Promise<boolean> {
    const value = text.trim();
    if (this.isThreadReference() && (replyTo !== this.currentThreadRootMessage || !thread)) { this.error = 'write_unsupported'; return false; }
    if (!value || !this.canSendGeneric() || !this.currentConversation) { this.error = 'write_unsupported'; return false; }
    const owner = this.capture(), source = this.currentSource, conversation = this.currentConversation, account = this.currentAccount;
    const clientId = util.generateRandomUUID();
    this.writeOutbound = false; this.writeState = 'sending'; this.error = '';
    let me: Record<string, Object>;
    try { me = await this.readCurrentMe(owner); }
    catch (error) { if (this.owns(owner)) { this.error = this.errorCode(error as Error); this.writeState = 'failed'; } return false; }
    if (!this.owns(owner)) return false;
    const body: Record<string, Object> = Wire.payload(['client_id', 'text', 'expect_user'], [clientId, value, Wire.text(me, 'user_id')]);
    if (replyTo) body['reply_to'] = replyTo;
    if (thread) body['thread'] = true;
    const path = '/api/v1/messaging/' + segment(source) + '/conversations/' + segment(conversation) + '/messages' + managerQuery(owner.managerId, Wire.payload(['account'], [account]));
    this.writeState = 'sending'; this.error = '';
    let acknowledged = false;
    try {
      await owner.repo.call(path, 'POST', body, clientId, () => this.admitWrite(owner));
      if (!this.owns(owner)) return false;
      acknowledged = true;
      this.replyDraft = ''; this.threadReplyTo = ''; this.threadReply = false; this.writeState = 'sent';
      if (this.draftReady) await this.persistDraft(owner, this.selected, '');
      if (!this.owns(owner)) return false;
      await this.refreshConversation(); return this.owns(owner);
    } catch (error) {
      if (acknowledged) { if (this.owns(owner)) this.error = this.errorCode(error as Error); return this.owns(owner); }
      if (this.owns(owner)) { this.writeState = this.writeFailureState(error as Error); this.error = this.errorCode(error as Error); }
      return false;
    }
  }

  async react(message: InboxMessage, key: string): Promise<boolean> {
    if (!this.canReact(message) || !message.id || !key) return false;
    const owner = this.capture(), old = Wire.rows(message.data(), 'reactions').find((item: Record<string, Object>) => Wire.text(item, 'key') === key);
    const remove = old && Wire.bool(old, 'mine');
    const path = this.currentIsGeneric ? '/api/v1/messaging/' + segment(this.currentSource) + '/messages/' + segment(message.id) + '/reactions' + managerQuery(owner.managerId, Wire.payload(['account'], [this.currentAccount]))
      : '/api/v1/lark/messages/' + segment(message.id) + '/reactions' + managerQuery(owner.managerId, Wire.payload(['ol_account'], [this.currentAccount]));
    try { await owner.repo.call(path, 'POST', Wire.payload(['key', 'remove'], [key, !!remove])); if (!this.owns(owner)) return false; await this.refreshConversation(); return this.owns(owner); }
    catch (error) { if (this.owns(owner)) this.error = this.errorCode(error as Error); return false; }
  }

  async refreshConversation(): Promise<void> {
    if (!this.currentConversation || !this.selectedKey) return;
    const owner = this.capture(), start = Wire.text(Wire.object(this.detail, 'conversation'), 'start_position', Wire.text(Wire.object(this.detail, 'lark'), 'start_position'));
    if (this.isThreadReference()) {
      if (!this.referenceThreadLoaded || !this.currentThreadRootMessage) return;
      const message = new InboxMessage(Wire.payload(['id', 'can_reply'], [this.currentThreadRootMessage, this.currentThreadCanReply]));
      await this.openThread(message); return;
    }
    if (this.referenceMode && this.threadRoot) { await this.openReferenceThread(owner, this.threadRoot); return; }
    if (this.threadRoot) {
      const message = this.feedMessages.find((item: InboxMessage) => item.id === this.threadRoot);
      if (message) await this.openThread(message);
    } else if (this.currentIsGeneric) await this.loadGenericMessages(owner, this.currentConversation, this.currentAccount, start, this.isTopicList);
    else await this.loadLegacyLarkMessages(owner, this.currentConversation, this.currentAccount, start, this.isTopicList);
  }

  private receiptEntry(receipt: Record<string, Object>): Record<string, Object> {
    return Wire.has(receipt, 'item') ? Wire.object(receipt, 'item') : receipt;
  }

  private acknowledgeEntry(row: ResourceRow, requireItem: boolean = false): (receipt: Record<string, Object>) => boolean {
    const expectedId = row.id;
    return (receipt: Record<string, Object>): boolean => {
      if ((requireItem && !Wire.has(receipt, 'item')) || !expectedId || Wire.text(this.receiptEntry(receipt), 'id') !== expectedId) {
        throw new HttpFailure(200, 'result_unknown', true);
      }
      return true;
    };
  }

  async replyInbox(text: string, confirmation: string): Promise<boolean> {
    if (!this.canWrite()) return false;
    if (this.referenceMode) return false;
    const value = text.trim(); if (!value || !this.canReplyToItem()) { this.error = 'reply_unsupported'; return false; }
    if (!confirmation || confirmation !== this.replyConfirmationSnapshot(value)) { this.error = 'confirmation_required'; return false; }
    const row = this.selected, detail = this.detail, owner = this.capture(row), clientId = util.generateRandomUUID();
    const body: Record<string, Object> = Wire.payload(['text', 'client_id'], [value, clientId]);
    const source = itemSource(row, detail), target = Wire.object(detail, 'reply_target');
    if (source === 'mail') {
      const uid = Wire.number(target, 'uid'), version = Wire.text(target, 'version');
      if (!Number.isSafeInteger(uid) || uid <= 0 || uid > 4294967295 || !version) { this.error = 'request_updated'; return false; }
      body['expected_uid'] = uid; body['expected_version'] = version;
    }
    const path = this.targetActionPath(row, 'reply');
    this.writeOutbound = false; this.writeState = 'sending'; this.error = '';
    let acknowledged = false;
    try {
      const result = await owner.repo.call(path, 'POST', body, clientId, () => this.admitWrite(owner), false,
        this.acknowledgeEntry(row));
      if (!this.owns(owner)) return false;
      const data = this.receiptEntry(result);
      const updated = this.detailRow(data, row); this.selected = updated; this.selectedKey = updated.key(); this.detail = data;
      this.items = updateResourceList(this.items, updated);
      this.replyDraft = ''; this.writeState = 'sent'; acknowledged = true;
      if (this.draftReady) await this.persistDraft(owner, row, '');
      if (!this.owns(owner)) return false;
      return true;
    } catch (error) {
      if (acknowledged) { if (this.owns(owner)) this.error = this.errorCode(error as Error); return this.owns(owner); }
      if (!this.owns(owner)) return false;
      this.writeState = this.writeFailureState(error as Error);
      this.error = this.errorCode(error as Error);
      if (error instanceof HttpFailure && error.code === 'mail_reply_target_changed') {
        // A stale target is reread, but the retained text never gets submitted again without a new confirmation.
        await this.refreshDetail();
        if (this.owns(owner)) { this.replyDraft = value; this.replyTargetChanged = true; this.writeState = 'target_changed'; }
      }
      return false;
    }
  }

  async processInbox(): Promise<boolean> {
    if (!this.canWrite()) return false;
    if (this.referenceMode) return false;
    if (!this.selectedKey) return false;
    const row = this.selected, owner = this.capture(row), nextDone = !done(row), path = this.targetActionPath(row, 'done');
    this.writeOutbound = false; this.writeState = 'saving'; this.error = '';
    try {
      const result = await owner.repo.call(path, 'POST', Wire.payload(['done'], [nextDone]), '', () => this.admitWrite(owner), false, this.acknowledgeEntry(row));
      if (!this.owns(owner)) return false;
      const data = this.receiptEntry(result), updated = this.detailRow(data, row);
      this.selected = updated; this.selectedKey = updated.key(); this.detail = data; this.items = updateResourceList(this.items, updated);
      this.doneItems = done(updated) ? updateResourceList(this.doneItems,updated) : this.doneItems.filter((row:ResourceRow) => row.key() !== updated.key());
      this.writeState = ''; return true;
    } catch (error) { if (this.owns(owner)) { this.writeState = this.writeFailureState(error as Error); this.error = this.errorCode(error as Error); } return false; }
  }

  async handoff(text: string): Promise<ResourceRow | undefined> {
    if (this.referenceMode) return undefined;
    if (!this.canWrite()) return undefined;
    const value = text.trim(); if (!value || !this.selectedKey) { this.error = 'text_required'; return undefined; }
    const row = this.selected, owner = this.capture(row), clientId = util.generateRandomUUID();
    const path = this.targetActionPath(row, 'handoff'); this.handoffBusy = true; this.writeOutbound = false; this.writeState = 'sending'; this.handoffDraft = value; this.error = '';
    try {
      const response = await owner.repo.call(path, 'POST', Wire.payload(['text', 'client_id'], [value, clientId]), clientId, () => this.admitWrite(owner), false, this.acknowledgeEntry(row, true));
      if (!this.owns(owner)) return undefined;
      this.writeState = 'handed_off'; this.handoffDraft = '';

      const acknowledgedItem = Wire.object(response, 'item');
      const acknowledgedId = Wire.text(acknowledgedItem, 'id');
      if (acknowledgedId === row.id) {
        const updated = this.detailRow(acknowledgedItem, row);
        updated.id = row.id; this.selected = updated; this.selectedKey = updated.key(); this.detail = acknowledgedItem;
        this.items = updateResourceList(this.items, updated);
      }

      let target = handoffConversation(Wire.object(response, 'session'), row.manager);
      if (target) return target;

      // The hand-off is acknowledged. Only recover the navigation target; a read failure must never make the
      // successful write look retryable.
      try {
        const refreshed = await owner.repo.call(this.targetPath(row));
        if (!this.owns(owner)) return undefined;
        const envelopeItem = Wire.object(refreshed, 'item');
        const detail = Object.keys(envelopeItem).length ? envelopeItem : refreshed;
        if (Wire.text(detail, 'id') === row.id) {
          const updated = this.detailRow(detail, row);
          updated.id = row.id; this.selected = updated; this.selectedKey = updated.key(); this.detail = detail;
          this.items = updateResourceList(this.items, updated);
          const handoffs = Wire.rows(detail, 'handoffs');
          target = handoffConversation(handoffs.length ? handoffs[handoffs.length - 1] : {}, row.manager);
        }
      } catch (_) {
        if (this.owns(owner)) this.error = 'inbox_handoff_target_unavailable';
        return undefined;
      }
      if (!target && this.owns(owner)) this.error = 'inbox_handoff_target_unavailable';
      return target;
    } catch (error) { if (this.owns(owner)) { this.writeState = this.writeFailureState(error as Error); this.error = this.errorCode(error as Error); } return undefined; }
    finally { if (this.owns(owner)) this.handoffBusy = false; }
  }

  async openMailCompose(): Promise<void> {
    if (!this.mailAllowed()) return;
    let owner: OwnerSnapshot; try { owner = this.capture(undefined); } catch (error) { this.error = this.errorCode(error as Error); return; }
    this.mailComposeError = ''; this.mailComposeOpen = true; this.mailComposeBusy = true; this.mailComposeReady = false;
    this.mailComposeBlocked = false; this.mailComposeState = 'draft'; this.mailComposeAccount = '';
    this.mailComposeTo = ''; this.mailComposeCc = ''; this.mailComposeBcc = ''; this.mailComposeSubject = ''; this.mailComposeText = '';
    try {
      const result = await owner.repo.call('/api/v1/mail/accounts' + managerQuery(owner.managerId));
      if (!this.owns(owner, false) || !this.mailComposeOpen) return;
      this.mailAccounts = Wire.rows(result, 'accounts');
      if (!this.mailAccounts.length) { this.mailComposeError = 'mail_account_unknown'; return; }
      const firstId = Wire.text(this.mailAccounts[0], 'id');
      this.mailComposeAccount = firstId;
      await this.selectMailComposeAccount(firstId);
    } catch (error) { if (this.owns(owner, false)) this.mailComposeError = this.errorCode(error as Error); }
    finally { if (this.owns(owner, false) && this.mailComposeOpen) this.mailComposeBusy = false; }
  }

  private composeDraftKey(owner: OwnerSnapshot, accountId: string): string {
    return 'inbox:mail-compose:' + JSON.stringify([owner.groupId, owner.managerId, accountId]);
  }
  private composeDraftRecord(accountId: string, state: string, operationId: string, to: string, cc: string, bcc: string, subject: string, text: string): string {
    const value: Record<string, Object> = Wire.payload(['account_id', 'state', 'operation_id', 'to', 'cc', 'bcc', 'subject', 'text'],
      [accountId, state, operationId, to, cc, bcc, subject, text]);
    return JSON.stringify(value);
  }
  private clearMailComposeFields(): void {
    this.mailComposeTo = ''; this.mailComposeCc = ''; this.mailComposeBcc = ''; this.mailComposeSubject = ''; this.mailComposeText = '';
  }
  async selectMailComposeAccount(accountId: string): Promise<void> {
    if (!this.mailComposeOpen || !accountId || !this.mailAccounts.some((item: Record<string, Object>) => Wire.text(item, 'id') === accountId)) {
      this.mailComposeError = 'account_changed'; return;
    }
    let owner: OwnerSnapshot; try { owner = this.capture(undefined); } catch (error) { this.mailComposeError = this.errorCode(error as Error); return; }
    if (this.mailComposeAccount === accountId && this.mailComposeReady) return;
    this.mailComposeAccount = accountId; this.mailComposeReady = false; this.mailComposeBlocked = false;
    this.mailComposeState = 'draft'; this.clearMailComposeFields(); this.mailComposeError = '';
    const key = this.composeDraftKey(owner, accountId);
    try {
      const saved = await owner.client.loadView(key);
      if (!this.ownsScope(owner) || !this.mailComposeOpen || this.mailComposeAccount !== accountId) return;
      if (saved) {
        const record = Wire.parse(saved), state = Wire.text(record, 'state', 'draft');
        this.mailComposeTo = Wire.text(record, 'to'); this.mailComposeCc = Wire.text(record, 'cc'); this.mailComposeBcc = Wire.text(record, 'bcc');
        this.mailComposeSubject = Wire.text(record, 'subject'); this.mailComposeText = Wire.text(record, 'text');
        this.mailComposeState = state;
        this.mailComposeBlocked = ['sending', 'pending', 'unknown'].includes(state) || state === 'sent' && !!this.mailComposeText;
        if (state === 'sent' && !this.mailComposeText) this.mailComposeState = 'draft';
      }
    } catch (_) { if (this.ownsScope(owner) && this.mailComposeOpen && this.mailComposeAccount === accountId) this.mailComposeError = 'draft_restore_failed'; }
    finally { if (this.ownsScope(owner) && this.mailComposeOpen && this.mailComposeAccount === accountId) this.mailComposeReady = true; }
  }
  setMailComposeField(field: string, value: string): void {
    if (!this.mailComposeReady || this.mailComposeBlocked || !this.mailComposeOpen) return;
    if (field === 'to') this.mailComposeTo = value;
    else if (field === 'cc') this.mailComposeCc = value;
    else if (field === 'bcc') this.mailComposeBcc = value;
    else if (field === 'subject') this.mailComposeSubject = value;
    else if (field === 'text') this.mailComposeText = value;
    else return;
    this.mailComposeState = 'draft';
    let owner: OwnerSnapshot; try { owner = this.capture(undefined); } catch (_) { return; }
    const accountId = this.mailComposeAccount, to = this.mailComposeTo, cc = this.mailComposeCc, bcc = this.mailComposeBcc;
    const subject = this.mailComposeSubject, text = this.mailComposeText, key = this.composeDraftKey(owner, accountId);
    this.composeWrite = this.composeWrite.catch(() => {}).then(async () => {
      if (!this.ownsScope(owner) || !this.mailComposeOpen || this.mailComposeAccount !== accountId) return;
      await owner.client.saveView(key, this.composeDraftRecord(accountId, 'draft', '', to, cc, bcc, subject, text));
    }).catch(() => { if (this.owns(owner, false) && this.mailComposeOpen && this.mailComposeAccount === accountId) this.mailComposeError = 'draft_save_failed'; });
  }
  closeMailCompose(): void { this.mailComposeOpen = false; this.mailComposeReady = false; this.mailComposeError = ''; }
  mailAccountName(id: string): string { const item = this.mailAccounts.find((value: Record<string, Object>) => Wire.text(value, 'id') === id); return Wire.text(item || {}, 'email', Wire.text(item || {}, 'name', id)); }
  mailComposeSnapshot(): InboxMailComposeSnapshot {
    return new InboxMailComposeSnapshot(this.mailComposeAccount, this.mailComposeTo, this.mailComposeCc, this.mailComposeBcc,
      this.mailComposeSubject, this.mailComposeText, util.generateRandomUUID());
  }
  mailComposeRecipients(snapshot: InboxMailComposeSnapshot): string { return parseAddressField(snapshot.to).map((item: Record<string, Object>) => addressLabel(item)).join(', '); }
  mailComposeCcRecipients(snapshot: InboxMailComposeSnapshot): string { return parseAddressField(snapshot.cc).map((item: Record<string, Object>) => addressLabel(item)).join(', '); }
  mailComposeBccRecipients(snapshot: InboxMailComposeSnapshot): string { return parseAddressField(snapshot.bcc).map((item: Record<string, Object>) => addressLabel(item)).join(', '); }
  mailComposeConfirmation(snapshot: InboxMailComposeSnapshot): string {
    const client = this.client;
    return JSON.stringify([client ? client.scope() : '', snapshot.accountId, this.mailComposeRecipients(snapshot),
      this.mailComposeCcRecipients(snapshot), this.mailComposeBccRecipients(snapshot), snapshot.subject, snapshot.text]);
  }
  mailComposeValid(snapshot?: InboxMailComposeSnapshot): boolean {
    const value = snapshot || new InboxMailComposeSnapshot(this.mailComposeAccount, this.mailComposeTo, this.mailComposeCc, this.mailComposeBcc, this.mailComposeSubject, this.mailComposeText, '');
    return this.mailComposeReady && !this.mailComposeBlocked && !!value.accountId && parseAddressField(value.to).length > 0 && parseAddressField(value.to).every((item: Record<string, Object>) => validEmail(Wire.text(item, 'email')))
      && parseAddressField(value.cc).every((item: Record<string, Object>) => validEmail(Wire.text(item, 'email')))
      && parseAddressField(value.bcc).every((item: Record<string, Object>) => validEmail(Wire.text(item, 'email')))
      && !!value.subject.trim() && !!value.text.trim();
  }
  async sendMailCompose(snapshot: InboxMailComposeSnapshot, confirmation: string): Promise<boolean> {
    if (!this.mailComposeValid(snapshot) || this.mailComposeBusy || !this.mailComposeOpen || this.mailComposeBlocked) { this.mailComposeError = 'mail_compose_invalid'; return false; }
    let owner: OwnerSnapshot; try { owner = this.capture(undefined); } catch (error) { this.mailComposeError = this.errorCode(error as Error); return false; }
    const current = new InboxMailComposeSnapshot(this.mailComposeAccount, this.mailComposeTo, this.mailComposeCc, this.mailComposeBcc,
      this.mailComposeSubject, this.mailComposeText, snapshot.operationId);
    const sameDraft = JSON.stringify([current.accountId, current.to, current.cc, current.bcc, current.subject, current.text]) ===
      JSON.stringify([snapshot.accountId, snapshot.to, snapshot.cc, snapshot.bcc, snapshot.subject, snapshot.text]);
    if (confirmation !== this.mailComposeConfirmation(snapshot) || !sameDraft) { this.mailComposeError = 'confirmation_required'; return false; }
    if (!this.mailAccounts.some((item: Record<string, Object>) => Wire.text(item, 'id') === snapshot.accountId)) { this.mailComposeError = 'account_changed'; return false; }
    const body: Record<string, Object> = Wire.payload(['account_id', 'to', 'subject', 'text'], [snapshot.accountId, parseAddressField(snapshot.to), snapshot.subject, snapshot.text]);
    const cc = parseAddressField(snapshot.cc), bcc = parseAddressField(snapshot.bcc);
    if (cc.length) body['cc'] = cc; if (bcc.length) body['bcc'] = bcc;
    this.mailComposeBusy = true; this.mailComposeError = '';
    const key = this.composeDraftKey(owner, snapshot.accountId);
    try {
      await this.composeWrite;
      if (!this.ownsScope(owner) || !this.mailComposeOpen || this.mailComposeAccount !== snapshot.accountId) return false;
      await owner.client.saveView(key, this.composeDraftRecord(snapshot.accountId, 'sending', snapshot.operationId,
        snapshot.to, snapshot.cc, snapshot.bcc, snapshot.subject, snapshot.text));
      if (!this.ownsScope(owner) || !this.mailComposeOpen || this.mailComposeAccount !== snapshot.accountId) return false;
      this.mailComposeState = 'sending';
      await owner.repo.call('/api/v1/mail/send' + managerQuery(owner.managerId), 'POST', body, snapshot.operationId);
      if (!this.ownsScope(owner) || !this.mailComposeOpen || this.mailComposeAccount !== snapshot.accountId) return false;
      this.clearMailComposeFields(); this.mailComposeState = 'sent'; this.mailComposeBlocked = false; this.mailComposeOpen = false; this.mailComposeReady = false;
      try { await owner.client.saveView(key, this.composeDraftRecord(snapshot.accountId, 'sent', snapshot.operationId, '', '', '', '', '')); }
      catch (_) { if (this.ownsScope(owner)) { this.mailComposeBlocked = true; this.mailComposeState = 'unknown'; this.mailComposeError = 'draft_save_failed'; } }
      return true;
    } catch (error) {
      if (!this.ownsScope(owner)) return false;
      const failure = error as Error, unknown = failure instanceof HttpFailure && (failure.resultUnknown || failure.resultPending);
      this.mailComposeBlocked = unknown; this.mailComposeState = unknown ? failure instanceof HttpFailure && failure.resultPending ? 'pending' : 'unknown' : 'draft';
      this.mailComposeError = this.errorCode(failure);
      try { await owner.client.saveView(key, this.composeDraftRecord(snapshot.accountId, this.mailComposeState,
        unknown ? snapshot.operationId : '', snapshot.to, snapshot.cc, snapshot.bcc, snapshot.subject, snapshot.text)); }
      catch (_) { this.mailComposeBlocked = true; this.mailComposeState = 'unknown'; }
      return false;
    } finally { if (this.ownsScope(owner) && this.mailComposeOpen) this.mailComposeBusy = false; }
  }

  async translateMail(message: InboxMailMessage): Promise<void> {
    const owner = this.capture(); this.translationBusy = true; this.translationError = ''; this.showOriginal = true; this.translatedMailUid = 0;
    try {
      const result = await owner.repo.call('/api/v1/translate/mail' + managerQuery(owner.managerId), 'POST', Wire.payload(['account_id', 'uid', 'mailbox', 'target'], [message.accountId, message.uid, message.mailbox, this.translationTarget]));
      if (!this.owns(owner)) return;
      this.mailTranslation = result; this.translatedMailUid = message.uid; this.showOriginal = false;
    } catch (error) { if (this.owns(owner)) { this.mailTranslation = {}; this.showOriginal = true; this.translationError = this.errorCode(error as Error); } }
    finally { if (this.owns(owner)) this.translationBusy = false; }
  }
  toggleOriginal(): void { if (Object.keys(this.mailTranslation).length) this.showOriginal = !this.showOriginal; }

  setAttachment(value: InboxAttachmentDraft): void {
    if (!this.canWrite()) return;
    if (!value.data.byteLength || value.data.byteLength > MAX_UPLOAD_BYTES || !value.name || !value.mime) { this.error = 'attachment_limit'; return; }
    if (value.kind === 'image' && (value.data.byteLength > 7.5 * 1024 * 1024 || !/^image\/(jpeg|png|webp|gif)$/.test(value.mime))) { this.error = 'attachment_limit'; return; }
    this.attachment = value; this.error = '';
  }
  removeAttachment(): void { if (this.canWrite()) this.attachment = undefined; }
  canSendAttachment(): boolean { return !!this.attachment && this.canSendGeneric() && this.capability('attachments') && this.hasConversation(); }

  async sendAttachment(): Promise<boolean> {
    const attachment = this.attachment, row = this.selected;
    if (!attachment || !this.canSendAttachment()) { this.error = 'write_unsupported'; return false; }
    const owner = this.capture(row), source = this.currentSource, conversation = this.currentConversation, account = this.currentAccount;
    this.writeOutbound = false; this.attachmentBusy = true; this.writeState = 'sending'; this.error = '';
    const replyTo = this.threadReplyTo, thread = this.threadReply;
    let me: Record<string, Object>;
    try { me = await this.readCurrentMe(owner); } catch (error) { if (this.owns(owner)) { this.error = this.errorCode(error as Error); this.attachmentBusy = false; this.writeState = 'failed'; } return false; }
    if (!this.owns(owner)) return false;
    const clientId = util.generateRandomUUID();
    const queryValues: Record<string, Object> = Wire.payload(['client_id', 'name', 'mime', 'kind', 'account'], [clientId, attachment.name, attachment.mime, attachment.kind, account]);
    if (replyTo) {
      if (!this.capability('attachment_replies')) { this.error = 'attachment_reply_unsupported'; this.attachmentBusy = false; this.writeState = 'failed'; return false; }
      queryValues['reply_to'] = replyTo; queryValues['thread'] = thread ? '1' : '';
    }
    const path = '/api/v1/messaging/' + segment(source) + '/conversations/' + segment(conversation) + '/attachments' + managerQuery(owner.managerId, queryValues);
    this.attachmentBusy = true; this.error = ''; this.writeState = 'sending';
    const mutations = owner.repo.mutations;
    if (!mutations) { this.attachmentBusy = false; this.writeState = 'failed'; this.error = 'storage_unavailable'; return false; }
    let record: MutationRecord | undefined;
    let acknowledged = false;
    try {
      const mutationPath = '/api/v1/messaging/' + segment(source) + '/conversations/' + segment(conversation) + '/attachments' + managerQuery(owner.managerId, Wire.payload(['account'], [account]));
      const admitted: MutationRecord = await mutations.begin(mutationPath, 'POST', '', clientId);
      record = admitted;
      if (!this.owns(owner)) { await mutations.finish(admitted, 'failed'); return false; }
      this.writeOutbound = true;
      const response = await owner.repo.transport.requestBytes(path, 'POST', attachment.data, attachment.mime, admitted.id);
      if (!this.owns(owner)) return false;
      if (response.data.byteLength > Wire.maxBytes) throw new HttpFailure(200, 'result_unknown', true);
      const receipt: Object = JSON.parse(util.TextDecoder.create('utf-8', { fatal: true }).decodeToString(new Uint8Array(response.data))) as Object;
      if (!receipt || typeof receipt !== 'object' || Array.isArray(receipt)) throw new HttpFailure(200, 'result_unknown', true);
      const result = receipt as Record<string, Object>;
      const messageId = result['message_id'];
      if (messageId !== undefined && (typeof messageId !== 'string' || Array.from(messageId as string).length > 200)) throw new HttpFailure(200, 'result_unknown', true);
      // A generic attachment receipt may be accepted without a message_id; the protocol forbids resending it automatically.
      if (Wire.text(result, 'state') === 'pending' || Wire.text(result, 'state') === 'unknown') throw new HttpFailure(202, 'result_unknown', true, Wire.text(result, 'state') === 'pending');
      acknowledged = true; this.attachment = undefined; this.writeState = 'sent';
      await mutations.finish(admitted, 'done');
      if (!this.owns(owner)) return false;
      await this.refreshConversation(); return this.owns(owner);
    } catch (error) {
      if (acknowledged) { if (this.owns(owner)) this.error = this.errorCode(error as Error); return this.owns(owner); }
      if (record && record.state === 'pending') {
        try { await mutations.finish(record, error instanceof HttpFailure && error.resultPending ? 'pending' : error instanceof HttpFailure && !error.resultUnknown ? 'failed' : 'unknown'); } catch (_) { }
      }
      if (this.owns(owner)) { this.writeState = this.writeFailureState(error as Error); this.error = this.errorCode(error as Error); }
      return false;
    } finally { if (this.owns(owner)) this.attachmentBusy = false; }
  }

  private errorCode(error: Error): string {
    if (error instanceof HttpFailure) {
      if (error.resultPending) return 'pending';
      if (error.resultUnknown) return 'result_unknown';
      if (error.code === 'mail_reply_target_changed') return 'mail_reply_target_changed';
      if (error.code === 'account_changed') return 'account_changed';
      if (error.status === 404 || error.status === 405) return 'unsupported';
      if (error.status === 401 || error.status === 403) return 'permission_denied';
    }
    return error.message || 'failed';
  }
}

function comparePosition(left: string, right: string): number {
  const a = left.replace(/^0+(?=\d)/, ''), b = right.replace(/^0+(?=\d)/, '');
  if (a.length !== b.length) return a.length > b.length ? 1 : -1;
  return a === b ? 0 : a > b ? 1 : -1;
}

function parseAddressField(value: string): Record<string, Object>[] {
  const parts: string[] = []; let start = 0, quoted = false, angle = false;
  for (let index = 0; index < value.length; index++) {
    const char = value[index];
    if (quoted && char === '\\') { index++; continue; }
    if (char === '"' && !angle) { quoted = !quoted; continue; }
    if (quoted) continue;
    if (char === '<') angle = true; else if (char === '>') angle = false;
    else if (!angle && (char === ',' || char === ';' || char === '，' || char === '；')) { parts.push(value.slice(start, index)); start = index + 1; }
  }
  parts.push(value.slice(start));
  return parts.map((part: string) => part.trim()).filter((part: string) => !!part).map((part: string) => {
    const left = part.lastIndexOf('<'), right = part.lastIndexOf('>');
    if (left >= 0 && right > left) return Wire.payload(['name', 'email'], [part.slice(0, left).trim().replace(/^"|"$/g, ''), part.slice(left + 1, right).trim()]);
    return Wire.payload(['name', 'email'], ['', part.trim()]);
  });
}

function validEmail(value: string): boolean {
  const email = value.trim(), at = email.indexOf('@');
  if (at <= 0 || at !== email.lastIndexOf('@') || /\s/.test(email)) return false;
  const domain = email.slice(at + 1);
  return domain.includes('.') && !domain.startsWith('.') && !domain.endsWith('.');
}
