// Decode untrusted JSON at the transport boundary. Unknown keys never become executable actions.
export class Wire {
  static readonly legacyStartedBy: string = 'started_by_jarvis';
  static payload(keys: string[], values: Object[]): Record<string, Object> {
    const result: Record<string, Object> = {}; keys.forEach((key: string, index: number) => result[key] = values[index]); return result;
  }
  static readonly maxBytes: number = 4 * 1024 * 1024;
  static parse(raw: string): Record<string, Object> {
    if (raw.length > Wire.maxBytes) throw new Error('response_too_large');
    if (!raw.trim()) return {};
    const value: Object = JSON.parse(raw) as Object;
    return Wire.record(value);
  }
  static record(value: Object | undefined | null): Record<string, Object> {
    if (value === undefined || value === null || typeof value !== 'object' || Array.isArray(value)) return {};
    return value as Record<string, Object>;
  }
  static object(value: Record<string, Object>, key: string): Record<string, Object> { return Wire.record(value[key]); }
  static has(value: Record<string, Object>, key: string): boolean { return Object.keys(value).includes(key); }
  static text(value: Record<string, Object>, key: string, fallback: string = ''): string {
    const result = value[key];
    return typeof result === 'string' ? result as string : fallback;
  }
  static number(value: Record<string, Object>, key: string, fallback: number = 0): number {
    const result = value[key];
    return typeof result === 'number' && Number.isFinite(result as number) ? result as number : fallback;
  }
  static identifier(value: Record<string, Object>, key: string): string {
    const result = value[key];
    if (typeof result === 'string') return result as string;
    return typeof result === 'number' && Number.isSafeInteger(result as number) && (result as number) > 0 ? String(result) : '';
  }
  static bool(value: Record<string, Object>, key: string, fallback: boolean = false): boolean {
    const result = value[key]; return typeof result === 'boolean' ? result as boolean : fallback;
  }
  static rows(value: Record<string, Object>, key: string): Record<string, Object>[] {
    const result = value[key];
    if (!Array.isArray(result)) return [];
    return (result as Object[]).map((item: Object) => Wire.record(item));
  }
  static strings(value: Record<string, Object>, key: string): string[] {
    const result = value[key];
    if (!Array.isArray(result)) return [];
    return (result as Object[]).filter((item: Object) => typeof item === 'string').map((item: Object) => item as string);
  }
}

// A per-agent entry is a complete set. Missing or malformed fields in that set use the protocol defaults.
export class ImageLimits {
  requestBytes: number = 32 * 1024 * 1024;
  imageBytes: number = 3750000;
  maxSide: number = 1600;
  perMessage: number = 20;

  private static field(values: Record<string, Object>, key: string, fallback: number): number {
    const n = Wire.number(values, key);
    return n >= 1 && n < 2147483647 ? Math.floor(n) : fallback;
  }

  static forAgent(published: Record<string, Object>, agent: string): ImageLimits {
    const agents = Wire.object(published, 'agents'), name = agent || 'claude';
    const entry = agents[name];
    const values = Wire.has(agents, name) && entry !== null && typeof entry === 'object' && !Array.isArray(entry)
      ? Wire.record(entry) : published;
    const limits = new ImageLimits();
    limits.requestBytes = ImageLimits.field(values, 'request_bytes', limits.requestBytes);
    limits.imageBytes = ImageLimits.field(values, 'image_bytes', limits.imageBytes);
    limits.maxSide = ImageLimits.field(values, 'max_side', limits.maxSide);
    limits.perMessage = ImageLimits.field(values, 'per_message', limits.perMessage);
    return limits;
  }
}

/** Keep the small directory fields needed by the home badge out of serialized navigation rows. */
export class SessionBadgeFields {
  readonly snapshot: string;
  readonly hidden: boolean;
  readonly quietTodo: string;
  readonly childrenWaiting: boolean;
  readonly pending: boolean;
  readonly parent: string;
  readonly space: string;

  constructor(snapshot: string, data: Record<string, Object>) {
    this.snapshot = snapshot;
    this.hidden = Wire.bool(data, 'assistant') || Wire.bool(data, 'hidden_by_directory') ||
      Wire.bool(data, 'muted') || Wire.bool(data, 'quiet');
    this.quietTodo = Wire.text(Wire.object(data, 'todo'), 'id');
    this.childrenWaiting = Wire.number(Wire.object(data, 'children'), 'needs_you') > 0;
    this.pending = Wire.number(data, 'pending_interactions') > 0;
    this.parent = Wire.text(data, 'folded_under');
    this.space = Wire.text(data, 'space_name');
  }
}

const sessionBadgeFields: WeakMap<ResourceRow, SessionBadgeFields> = new WeakMap();

export class ResourceRow {
  id: string = '';
  title: string = '';
  detail: string = '';
  kind: string = '';
  state: string = '';
  manager: string = '';
  group: string = '';
  machine: string = '';
  session: string = '';
  path: string = '';
  raw: string = '{}';
  constructor(id: string = '', title: string = '', kind: string = '') { this.id = id; this.title = title; this.kind = kind; }
  data(): Record<string, Object> { return Wire.parse(this.raw); }
  badgeFields(): SessionBadgeFields {
    const known = sessionBadgeFields.get(this);
    if (known && known.snapshot === this.raw) return known;
    const fields = new SessionBadgeFields(this.raw, this.data());
    sessionBadgeFields.set(this, fields);
    return fields;
  }
  static from(data: Record<string, Object>, kind: string): ResourceRow {
    const row = new ResourceRow(Wire.text(data, 'id', Wire.text(data, 'session_id')),
      Wire.text(data, 'title', Wire.text(data, 'name', Wire.text(data, 'subject', Wire.text(data, 'label')))), kind);
    row.detail = Wire.text(data, 'summary', Wire.text(data, 'notes', Wire.text(data, 'preview', Wire.text(data, 'description', Wire.text(data, 'one_line', Wire.text(data, 'content', Wire.text(data, 'text', Wire.text(data, 'body'))))))));
    row.state = Wire.text(data, 'state', Wire.text(data, 'status'));
    row.manager = Wire.text(data, 'manager', Wire.text(data, 'manager_id'));
    row.machine = Wire.text(data, 'machine_id', Wire.text(data, 'machine'));
    row.session = Wire.text(data, 'session_id', kind === 'session' ? row.id : '');
    row.path = Wire.text(data, 'cwd', Wire.text(data, 'path'));
    row.raw = JSON.stringify(data);
    if (kind === 'session') sessionBadgeFields.set(row, new SessionBadgeFields(row.raw, data));
    return row;
  }
  isInstallingManager(): boolean {
    const data = this.data();
    return !!Wire.text(data, 'installing') || Wire.bool(data, 'installing');
  }
  isDone(): boolean { const data = this.data(); return !!Wire.text(data, 'done_at') || Wire.bool(data, 'done'); }
  key(): string { return JSON.stringify(this.group ? [this.group, this.kind, this.manager, this.machine, this.id] : [this.kind, this.manager, this.machine, this.id]); }
}

export class HeldAgentMessage {
  id: string = '';
  text: string = '';
  computer: string = '';
  line: string = '';
  state: string = '';
  images: number = 0;
  at: string = '';
  deleteOnly: boolean = false;

  static from(data: Record<string, Object>): HeldAgentMessage | undefined {
    const id = Wire.text(data, 'id'), computer = Wire.text(data, 'computer'), state = Wire.text(data, 'state');
    if (!id || !computer || (state !== 'waiting' && state !== 'failed')) return undefined;
    const held = new HeldAgentMessage();
    held.id = id; held.computer = computer; held.state = state;
    held.text = Wire.text(data, 'text'); held.line = Wire.text(data, 'error'); held.images = Wire.number(data, 'images');
    held.at = Wire.text(data, 'at'); held.deleteOnly = Wire.bool(data, 'delete_only');
    return held;
  }
}

export interface ConversationRecapItem {
  id: string;
  text: string;
  at: number;
}

export class ChatMessage {
  id: string = '';
  role: string = 'assistant';
  recaps: ConversationRecapItem[] = [];
  terminalRecap: boolean = false;
  at: number = 0;
  text: string = '';
  work: string = '';
  toolCount: number = 0;
  thoughtCount: number = 0;
  imageId: string = '';
  itemId: string = '';
  sourceFingerprint: string = '';
  ownerTarget: string = '';
  imagePaths: string[] = [];
  filePaths: string[] = [];
  running: boolean = false;
  failed: boolean = false;
  deliveryState: string = '';
  held: HeldAgentMessage | undefined;
  localOperation: string = '';
  localImageCount: number = 0;
  source: string = '';
  raw: string = '{}';
  constructor(id: string = '', role: string = 'assistant', text: string = '') { this.id = id; this.role = role; this.text = text; }
}

@ObservedV2
export class Decision {
  id: string = '';
  machine: string = '';
  session: string = '';
  kind: string = '';
  prompt: string = '';
  options: string[] = [];
  fields: DecisionField[] = [];
  @Trace status: string = 'open';
  revision: string = '';
  raw: string = '{}';
}
export class DecisionField {
  key: string = '';
  title: string = '';
  kind: string = 'text';
  required: boolean = false;
  choices: string[] = [];
}

export class HubProfile {
  id: string = '';
  name: string = '';
  baseUrl: string = '';
  addresses: string[] = [];
  hubId: string = '';
  pin: string = '';
  manager: string = '';
  constructor(id: string = '', name: string = '', baseUrl: string = '') { this.id = id; this.name = name; this.baseUrl = baseUrl; }
}
