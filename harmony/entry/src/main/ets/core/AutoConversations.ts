/**
 * Pure TypeScript Auto Conversations & All Chats helpers aligned with
 * `apple/Persona/Views/Assistant/AutoConversations.swift` and `AutoConversationsView.swift`.
 * Zero ArkUI dependencies so host Node test suites can import directly.
 */

export type AutoConversationSourceId =
  | 'inbox'
  | 'task'
  | 'lark_agent'
  | 'spawned'
  | 'extension'
  | 'other';

export type AutoConversationFilter = 'all' | 'mine' | 'automatic' | AutoConversationSourceId;

export const AUTO_CONVERSATION_FEATURE = 'conversation_origin';

export const AUTO_CONVERSATION_SOURCES: readonly AutoConversationSourceId[] = [
  'inbox',
  'task',
  'lark_agent',
  'spawned',
  'extension',
  'other'
];

export const AUTO_CONVERSATION_USERS_OWN: readonly string[] = ['', 'user', 'channel'];

export interface AutoConversationInput {
  readonly id: string;
  readonly session_id?: string;
  readonly machine_id?: string;
  readonly group?: string;
  readonly manager?: string;
  readonly title?: string;
  readonly origin?: string;
  readonly kind?: string;
  readonly automatic?: boolean;
  readonly main?: boolean;
  readonly archived?: boolean;
  readonly state?: string;
  readonly status?: string;
  readonly updated_at?: string;
  readonly created_at?: string;
  readonly last_text?: string;
}

export interface AutoConversationGroup<T extends AutoConversationInput = AutoConversationInput> {
  readonly source: AutoConversationSourceId;
  readonly sessions: T[];
  readonly needsYou: number;
  readonly working: boolean;
  readonly unread: number;
}

export interface AutoConversationsPartition<T extends AutoConversationInput = AutoConversationInput> {
  readonly supported: boolean;
  readonly all: T[];
  readonly mine: T[];
  readonly auto: T[];
  readonly groups: AutoConversationGroup<T>[];
  readonly needsYou: number;
  readonly working: boolean;
  readonly unread: number;
}

export interface PartitionAutoConversationsOptions {
  readonly mainId?: string;
  readonly mainMachineId?: string;
  readonly group?: string;
  readonly manager?: string;
  readonly hubSays?: boolean;
  readonly knownTaskIds?: readonly string[];
  readonly currentKey?: string;
}

function convId(item: AutoConversationInput): string {
  return (item.session_id || item.id || '').trim();
}

function convTimestamp(item: AutoConversationInput): number {
  const raw = item.updated_at || item.created_at || '';
  const stamp = Date.parse(raw);
  if (!Number.isFinite(stamp)) return Number.NEGATIVE_INFINITY;
  const fraction = raw.match(/:\d{2}\.\d{3}(\d+)/);
  return stamp + (fraction ? Number('0.' + fraction[1]) : 0);
}

export function sortAutoConversationsNewestFirst<T extends AutoConversationInput>(rows: readonly T[]): T[] {
  return rows.slice().sort((a: T, b: T) => {
    const diff = convTimestamp(b) - convTimestamp(a);
    if (diff !== 0) return diff;
    const left = convId(a);
    const right = convId(b);
    return left < right ? -1 : left > right ? 1 : 0;
  });
}

export function normalizeAutoConversationState(item: AutoConversationInput): string {
  const raw = (item.state || item.status || '').trim().toLowerCase();
  if (raw === 'blocked' || raw === 'needs_you') return 'needs_you';
  if (raw === 'working' || raw === 'running') return 'working';
  if (raw === 'done' || raw === 'unread') return 'unread';
  return raw || 'idle';
}

export function resolveAutoConversationSource(origin: string | undefined): AutoConversationSourceId {
  const trimmed = (origin || '').trim();
  if (
    trimmed === 'inbox' ||
    trimmed === 'task' ||
    trimmed === 'lark_agent' ||
    trimmed === 'spawned' ||
    trimmed === 'extension'
  ) {
    return trimmed;
  }
  return 'other';
}

export function autoConversationSourceLabelKey(source: AutoConversationSourceId): string {
  switch (source) {
    case 'inbox': return 'inbox';
    case 'task': return 'phone_drawer_schedules';
    case 'lark_agent': return 'phone_all_chats_lark';
    case 'spawned': return 'phone_sidebar_source_spawned';
    case 'extension': return 'extensions';
    case 'other': return 'phone_sidebar_source_other';
  }
}

export function isExternalAutoSource(source: AutoConversationSourceId): boolean {
  return source === 'lark_agent' || source === 'extension';
}

export function sourceOfAutoConversation(
  item: AutoConversationInput,
  knownTaskIds: readonly string[] = []
): AutoConversationSourceId | undefined {
  const origin = (item.origin || '').trim();
  if (!AUTO_CONVERSATION_USERS_OWN.includes(origin)) {
    return resolveAutoConversationSource(origin);
  }
  const id = convId(item);
  if (id && knownTaskIds.includes(id)) {
    return 'task';
  }
  if (item.automatic === true || item.kind === 'automatic') {
    return 'other';
  }
  return undefined;
}

export function partitionAutoConversations<T extends AutoConversationInput>(
  conversations: readonly T[],
  options: PartitionAutoConversationsOptions = {}
): AutoConversationsPartition<T> {
  const mainId = (options.mainId || '').trim();
  const mainMachine = (options.mainMachineId || '').trim();
  const group = (options.group || '').trim();
  const manager = (options.manager || '').trim();
  const knownTasks = options.knownTaskIds || [];

  const eligible = sortAutoConversationsNewestFirst(conversations.filter((item: T) => {
    const id = convId(item);
    if (!id || item.archived) return false;
    if (group && item.group && item.group !== group) return false;
    if (manager && item.manager && item.manager !== manager) return false;
    if (item.main) return false;
    if (mainId && id === mainId && (!mainMachine || !item.machine_id || item.machine_id === mainMachine)) {
      return false;
    }
    return true;
  }));

  const supported = options.hubSays === true ||
    eligible.some((item: T) => typeof item.origin === 'string' || item.automatic === true || item.kind === 'automatic');

  const mine: T[] = [];
  const auto: T[] = [];
  const bySource = new Map<AutoConversationSourceId, T[]>();

  for (const item of eligible) {
    const src = sourceOfAutoConversation(item, knownTasks);
    if (!supported) {
      if (src === 'task' || src === 'other') {
        auto.push(item);
      } else {
        mine.push(item);
      }
      continue;
    }
    if (!src) {
      mine.push(item);
    } else {
      auto.push(item);
      const key = (item.machine_id || '') + '|' + convId(item);
      if (options.currentKey && key === options.currentKey) {
        mine.push(item);
      }
      const list = bySource.get(src) || [];
      list.push(item);
      bySource.set(src, list);
    }
  }

  const groups: AutoConversationGroup<T>[] = [];
  for (const src of AUTO_CONVERSATION_SOURCES) {
    const sessions = bySource.get(src);
    if (!sessions || sessions.length === 0) continue;
    let needsYou = 0;
    let working = false;
    let unread = 0;
    for (const s of sessions) {
      const state = normalizeAutoConversationState(s);
      if (state === 'needs_you') needsYou++;
      if (state === 'working') working = true;
      if (state === 'unread') unread++;
    }
    groups.push({ source: src, sessions, needsYou, working, unread });
  }

  let totalNeeds = 0;
  let anyWorking = false;
  let totalUnread = 0;
  for (const s of auto) {
    const state = normalizeAutoConversationState(s);
    if (state === 'needs_you') totalNeeds++;
    if (state === 'working') anyWorking = true;
    if (state === 'unread') totalUnread++;
  }

  return {
    supported,
    all: eligible,
    mine,
    auto,
    groups,
    needsYou: totalNeeds,
    working: anyWorking,
    unread: totalUnread
  };
}

export function filterAutoConversations<T extends AutoConversationInput>(
  partition: AutoConversationsPartition<T>,
  filter: AutoConversationFilter
): T[] {
  if (filter === 'all') return partition.all;
  if (filter === 'mine') return partition.mine;
  if (filter === 'automatic') return partition.auto;
  const group = partition.groups.find((g: AutoConversationGroup<T>) => g.source === filter);
  return group ? group.sessions : [];
}
