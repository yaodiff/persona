/**
 * Pure TypeScript cross-link helpers for Knowledge Chat <-> Inbox items,
 * aligned with iOS `KBChatInbox.swift` & `InboxKBChat.swift` (#15356 / #16672).
 */

export interface KBInboxLine {
  readonly day: string;
  readonly line: string;
}

export interface KBInboxItem {
  readonly id: string;
  readonly title: string;
  readonly tier: string;
  readonly status: string;
  readonly summary: string;
  readonly preview: string;
  readonly updatedAt: string;
  readonly doneAt: string;
  readonly messages: KBInboxLine[];
}

export interface KBChatInboxPiece {
  readonly id: number;
  readonly text: string;
  readonly item: KBInboxItem | null;
}

export interface KBChatInboxPage {
  readonly open: KBInboxItem[];
  readonly done: KBInboxItem[];
  readonly pieces: KBChatInboxPiece[] | null;
}

export interface InboxKBChatLink {
  readonly id: string;
  readonly path: string;
}

export interface KBMessageLine {
  readonly time: string;
  readonly sender: string;
  readonly text: string;
}

export type KBInboxTier = 'action' | 'working' | 'fyi' | 'noise';

function asObj(raw: Object | null | undefined): Record<string, Object> | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  return raw as Record<string, Object>;
}

function asStr(raw: Object | undefined): string {
  return typeof raw === 'string' ? raw : '';
}

export function parseKBInboxItems(rawInbox: Object | null | undefined): KBInboxItem[] {
  if (!Array.isArray(rawInbox)) return [];
  const items: KBInboxItem[] = [];
  for (const entry of rawInbox as Object[]) {
    const obj = asObj(entry);
    if (!obj) continue;
    const id = asStr(obj['id']).trim();
    if (!id) continue;
    const messages: KBInboxLine[] = [];
    if (Array.isArray(obj['messages'])) {
      for (const m of obj['messages'] as Object[]) {
        const mo = asObj(m);
        if (!mo) continue;
        const day = asStr(mo['day']).trim();
        const line = asStr(mo['line']);
        if (day && line) messages.push({ day, line });
      }
    }
    items.push({
      id,
      title: asStr(obj['title']),
      tier: asStr(obj['tier']),
      status: asStr(obj['status']),
      summary: asStr(obj['summary']),
      preview: asStr(obj['preview']),
      updatedAt: asStr(obj['updated_at']),
      doneAt: asStr(obj['done_at']),
      messages
    });
  }
  return items;
}

export function kbInboxTier(item: KBInboxItem): KBInboxTier | null {
  const status = !item.doneAt ? item.status.trim() : '';
  const key = status || item.tier.trim();
  if (!key) return null;
  if (key === 'action' || key === 'working' || key === 'fyi' || key === 'noise') return key;
  return 'action';
}

export function kbInboxLine(item: KBInboxItem): string {
  const summary = item.summary.trim();
  if (summary) return summary;
  return item.preview.trim();
}

export function parseKBMessageLine(raw: string): KBMessageLine {
  const t = raw.startsWith('- ') ? raw.slice(2) : raw;
  const spaceIdx = t.indexOf(' ');
  if (spaceIdx === 5 && t[2] === ':' && t[0] >= '0' && t[0] <= '9') {
    const rest = t.slice(6);
    const colonIdx = rest.indexOf(': ');
    if (colonIdx >= 0) {
      return {
        time: t.slice(0, 5),
        sender: rest.slice(0, colonIdx),
        text: rest.slice(colonIdx + 2)
      };
    }
  }
  return { time: '', sender: '', text: t };
}

export function kbMessageRowKey(day: string, line: string): string {
  const m = parseKBMessageLine(line);
  const text = m.text.replace(/\n[ \t]+/g, '\n').trim();
  return [day, m.time, m.sender, text].join('\u0000');
}

function rowItemsMap(items: readonly KBInboxItem[]): Map<string, KBInboxItem> {
  const out = new Map<string, KBInboxItem>();
  for (const item of items) {
    for (const m of item.messages) {
      const key = kbMessageRowKey(m.day, m.line);
      if (!out.has(key)) {
        out.set(key, item);
      }
    }
  }
  return out;
}

function headingDay(line: string): string | null {
  if (!line.startsWith('## ')) return null;
  const d = line.slice(3).trim();
  return /^\d{4}-\d{2}-\d{2}$/.test(d) ? d : null;
}

/**
 * Splits a chat's `recent` 7-day transcript text into plain runs and Inbox-linked message rows,
 * matching `KBChatInbox.pieces` (`KBChatInbox.swift`). Returns `null` when no item holds a row.
 */
export function parseChatInboxPieces(
  recent: string,
  items: readonly KBInboxItem[]
): KBChatInboxPiece[] | null {
  const byRow = rowItemsMap(items);
  if (byRow.size === 0 || !recent) return null;

  const lines = recent.split('\n');
  const linked: Array<{ first: number; last: number; item: KBInboxItem }> = [];
  let day = '';
  let currentRow: { first: number; last: number; lines: string[]; day: string } | null = null;

  const closeRow = (): void => {
    if (currentRow && currentRow.day) {
      const item = byRow.get(kbMessageRowKey(currentRow.day, currentRow.lines.join('\n')));
      if (item) {
        linked.push({ first: currentRow.first, last: currentRow.last, item });
      }
    }
    currentRow = null;
  };

  for (let i = 0; i < lines.length; i++) {
    const l = lines[i];
    const h = headingDay(l);
    if (h !== null) {
      closeRow();
      day = h;
    } else if (l.startsWith('- ')) {
      closeRow();
      currentRow = { first: i, last: i, lines: [l], day };
    } else if (currentRow && l.length > 0 && /^\s/.test(l[0])) {
      currentRow.last = i;
      currentRow.lines.push(l);
    } else {
      const t = l.trim();
      if (t && t !== '…') {
        closeRow();
        currentRow = { first: i, last: i, lines: [l], day };
      }
    }
  }
  closeRow();

  if (linked.length === 0) return null;

  const out: KBChatInboxPiece[] = [];
  let next = 0;
  const pushPlain = (stop: number): void => {
    if (next < stop) {
      const text = lines.slice(next, stop).join('\n');
      if (text) out.push({ id: next, text, item: null });
    }
  };
  for (const r of linked) {
    pushPlain(r.first);
    out.push({ id: r.first, text: lines.slice(r.first, r.last + 1).join('\n'), item: r.item });
    next = r.last + 1;
  }
  pushPlain(lines.length);
  return out;
}

export function parseChatInboxPage(
  kind: string,
  recent: string,
  rawInbox: Object | null | undefined
): KBChatInboxPage | null {
  if (kind !== 'chats') return null;
  const items = parseKBInboxItems(rawInbox);
  if (!items.length) return null;
  return {
    open: items.filter((item: KBInboxItem) => !item.doneAt),
    done: items.filter((item: KBInboxItem) => !!item.doneAt),
    pieces: parseChatInboxPieces(recent, items)
  };
}

export function parseInboxKBChat(rawKbChat: Object | null | undefined): InboxKBChatLink | null {
  const obj = asObj(rawKbChat);
  if (!obj) return null;
  const id = asStr(obj['id']).trim();
  if (!id) return null;
  const path = asStr(obj['path']).trim() || 'chats';
  return { id, path };
}
