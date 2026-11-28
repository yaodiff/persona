/**
 * Pure TypeScript helpers for the unified composer-top queue & status strip (#16247).
 * Aligned with `server/internal/proto/session_wire.go`, `apple/Shared/Models.swift`,
 * `server/internal/node/runtimes.go`, and `docs/protocol/protocol.md`.
 * Zero ArkUI dependencies so Node test runners can import directly.
 */

export interface QueuedComposerItem {
  readonly id: string;
  readonly text: string;
  readonly relayed: boolean;
  readonly sender: string;
  readonly noWake: boolean;
}

export interface ComposerQueueState {
  readonly items: QueuedComposerItem[];
  readonly ownCount: number;
  readonly relayedCount: number;
  readonly preview: string;
  readonly steersQueue: boolean;
}

interface ParsedQueueEntry {
  readonly id: string;
  readonly text: string;
  readonly explicitRelayed: boolean | undefined;
  readonly sender: string;
  readonly noWake: boolean;
}

const RELAY_PREFIX_PATTERN = /^来自「([^\n]*?)」(（不唤醒）)?：([\s\S]*)$/;

function parseRelayMetadata(
  rawText: string,
  relayed: boolean,
  explicitSender: string,
  explicitNoWake: boolean
): { text: string; sender: string; noWake: boolean } {
  if (!relayed) {
    return { text: rawText, sender: explicitSender, noWake: false };
  }
  const match = RELAY_PREFIX_PATTERN.exec(rawText);
  if (match) {
    const parsedSender = match[1].trim();
    const parsedNoWake = !!match[2];
    const body = match[3].trim();
    if (parsedSender) {
      return {
        text: body || rawText,
        sender: explicitSender || parsedSender,
        noWake: explicitNoWake || parsedNoWake
      };
    }
  }
  return { text: rawText, sender: explicitSender, noWake: explicitNoWake };
}

/**
 * Returns true only when `index` refers to a mutable user-owned queued item (`0 <= index < ownCount`).
 * Relayed messages (`index >= ownCount`) are strictly read-only and must never hit `/queue`.
 */
export function canMutateQueueIndex(ownCount: number, index: number): boolean {
  return Number.isInteger(ownCount) && Number.isInteger(index) && ownCount > 0 && index >= 0 && index < ownCount;
}

/**
 * Builds the `keep` index array for `POST /api/v1/sessions/{m}/{s}/queue` that removes `removeIndex`.
 * Matches iOS `ComposerTopSlot.swift`: excludes relayed indices (`>= ownCount`) and `removeIndex`.
 */
export function queueKeepWithout(ownCount: number, removeIndex: number): number[] {
  if (!Number.isInteger(ownCount) || ownCount <= 0) return [];
  const keep: number[] = [];
  for (let i = 0; i < ownCount; i++) {
    if (i !== removeIndex) keep.push(i);
  }
  return keep;
}

/**
 * Builds the `keep` index array for moving own queued item `index` by `delta` (`-1` up, `1` down).
 * Returns `[]` when the move is out of bounds or targets a relayed index, matching `QueueSteerTests.swift`.
 */
export function queueKeepSwapped(ownCount: number, index: number, delta: -1 | 1): number[] {
  const to = index + delta;
  if (
    !Number.isInteger(ownCount) ||
    !Number.isInteger(index) ||
    (delta !== -1 && delta !== 1) ||
    ownCount < 2 ||
    index < 0 ||
    index >= ownCount ||
    to < 0 ||
    to >= ownCount
  ) {
    return [];
  }
  const order: number[] = [];
  for (let i = 0; i < ownCount; i++) {
    order.push(i);
  }
  order.splice(index, 1);
  order.splice(to, 0, index);
  return order;
}

/**
 * Appends a taken-back queued message below any existing composer draft without overwriting it.
 */
export function mergeQueuedTakeBackDraft(existingDraft: string, pulledText: string): string {
  const pulled = (pulledText || '').trim();
  if (!pulled) return existingDraft;
  if (!existingDraft || !existingDraft.trim()) return pulled;
  return existingDraft + '\n' + pulled;
}

function asRecord(value: Object | undefined | null): Record<string, Object> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
  return value as Record<string, Object>;
}

function normalizeSingleLine(text: string, maxLength: number = 140): string {
  const collapsed = text.replace(/\s+/g, ' ').trim();
  if (!collapsed) return '';
  if (collapsed.length <= maxLength) return collapsed;
  return collapsed.slice(0, maxLength - 1) + '…';
}

/**
 * Extracts queued follow-up notes and queue-steering state from a raw Hub session/conversation record.
 * Matches iOS `Session.ownQueuedCount` (`queued_relayed`) and `Session.steersQueue`
 * (`capabilities.queue_send_now`, Claude Code in a terminal pane, or Assistant with `canSend`).
 */
export function extractComposerQueue(sessionData: Record<string, Object> | undefined | null): ComposerQueueState {
  const data = asRecord(sessionData);
  const rawQueued = data['queued'];
  const parsed: ParsedQueueEntry[] = [];

  if (Array.isArray(rawQueued)) {
    const entries = rawQueued as Object[];
    for (let i = 0; i < entries.length; i++) {
      const entry = entries[i];
      if (typeof entry === 'string') {
        const text = entry.trim();
        if (!text) continue;
        parsed.push({
          id: '',
          text: text,
          explicitRelayed: undefined,
          sender: '',
          noWake: false
        });
      } else if (entry && typeof entry === 'object' && !Array.isArray(entry)) {
        const record = entry as Record<string, Object>;
        const rawText = typeof record['text'] === 'string'
          ? record['text'] as string
          : typeof record['body'] === 'string'
            ? record['body'] as string
            : '';
        const text = rawText.trim();
        if (!text) continue;
        const rawId = typeof record['id'] === 'string' ? (record['id'] as string).trim() : '';
        const sender = typeof record['sender'] === 'string' ? (record['sender'] as string).trim() : '';
        const explicitRelayed = typeof record['relayed'] === 'boolean' ? record['relayed'] as boolean : undefined;
        const noWake = record['no_wake'] === true || record['noWake'] === true;
        parsed.push({
          id: rawId,
          text: text,
          explicitRelayed: explicitRelayed,
          sender: sender,
          noWake: noWake
        });
      }
    }
  }

  let ownCount = 0;
  let relayedCount = 0;
  const items: QueuedComposerItem[] = [];
  const assignedIds = new Set<string>();
  for (const entry of parsed) {
    if (entry.id) assignedIds.add(entry.id);
  }
  const emittedIds = new Set<string>();
  const resolveId = (rawId: string, index: number): string => {
    if (rawId && !emittedIds.has(rawId)) {
      emittedIds.add(rawId);
      return rawId;
    }
    const base = rawId || ('idx-' + String(index));
    let candidate = base;
    let suffix = 1;
    while (assignedIds.has(candidate) || emittedIds.has(candidate)) {
      candidate = base + '-' + String(suffix++);
    }
    assignedIds.add(candidate);
    emittedIds.add(candidate);
    return candidate;
  };

  const hasQueuedRelayed = typeof data['queued_relayed'] === 'number' && Number.isFinite(data['queued_relayed']);
  const hasOwnQueuedCount = typeof data['own_queued_count'] === 'number' && Number.isFinite(data['own_queued_count']);

  if (hasQueuedRelayed) {
    const rawRelayed = data['queued_relayed'] as number;
    const clampedRelayed = Math.min(Math.max(Math.floor(rawRelayed), 0), parsed.length);
    ownCount = parsed.length - clampedRelayed;
    relayedCount = clampedRelayed;
    for (let i = 0; i < parsed.length; i++) {
      const entry = parsed[i];
      const relayed = i >= ownCount;
      const meta = parseRelayMetadata(entry.text, relayed, entry.sender, entry.noWake);
      items.push({
        id: resolveId(entry.id, i),
        text: meta.text,
        relayed: relayed,
        sender: meta.sender,
        noWake: meta.noWake
      });
    }
  } else if (hasOwnQueuedCount) {
    const rawOwn = data['own_queued_count'] as number;
    ownCount = Math.min(Math.max(Math.floor(rawOwn), 0), parsed.length);
    relayedCount = parsed.length - ownCount;
    for (let i = 0; i < parsed.length; i++) {
      const entry = parsed[i];
      const relayed = i >= ownCount;
      const meta = parseRelayMetadata(entry.text, relayed, entry.sender, entry.noWake);
      items.push({
        id: resolveId(entry.id, i),
        text: meta.text,
        relayed: relayed,
        sender: meta.sender,
        noWake: meta.noWake
      });
    }
  } else {
    for (let i = 0; i < parsed.length; i++) {
      const entry = parsed[i];
      const relayed = entry.explicitRelayed !== undefined ? entry.explicitRelayed : !!entry.sender;
      if (relayed) relayedCount++;
      else ownCount++;
      const meta = parseRelayMetadata(entry.text, relayed, entry.sender, entry.noWake);
      items.push({
        id: resolveId(entry.id, i),
        text: meta.text,
        relayed: relayed,
        sender: meta.sender,
        noWake: meta.noWake
      });
    }
  }

  const firstOwn = items.find((item) => !item.relayed);
  const previewSource = firstOwn ? firstOwn.text : (items.length > 0 ? items[0].text : '');
  const preview = normalizeSingleLine(previewSource);

  const sourceKind = typeof data['source_kind'] === 'string' ? (data['source_kind'] as string).trim() : '';
  const hasSource = sourceKind.length > 0;
  const rawCaps = data['capabilities'];
  const caps = rawCaps && typeof rawCaps === 'object' && !Array.isArray(rawCaps)
    ? rawCaps as Record<string, Object>
    : undefined;
  const canSend = caps && typeof caps['send'] === 'boolean'
    ? caps['send'] === true
    : !hasSource;
  const agent = typeof data['agent'] === 'string' ? (data['agent'] as string).trim().toLowerCase() : '';
  const isCodex = agent === 'codex' || sourceKind === 'codex_app_server' || sourceKind === 'codex_history';
  const isClaude = !isCodex && (agent === '' || agent === 'claude');
  const paneId = typeof data['pane_id'] === 'string' ? (data['pane_id'] as string).trim() : '';
  const assistant = data['assistant'] === true;

  const steersQueue = (caps?.['queue_send_now'] === true) ||
    (!assistant && !hasSource && isClaude && !!paneId && canSend) ||
    (assistant === true && canSend);

  return {
    items: items,
    ownCount: ownCount,
    relayedCount: relayedCount,
    preview: preview,
    steersQueue: steersQueue
  };
}
