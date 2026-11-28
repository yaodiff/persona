/**
 * Pure TypeScript Unified Assistant Search helpers aligned with
 * `apple/Persona/Views/Assistant/AssistantChatSearch.swift` and
 * `apple/Persona/Views/Assistant/AssistantFullSearch.swift`.
 * Zero ArkUI dependencies so host Node test suites can import directly.
 */

import type { SearchTextPart } from './PhoneSearchExcerpt.ts';
import { phoneSearchExcerpt, searchHitRanges } from './PhoneSearchExcerpt.ts';

export const ASSISTANT_SEARCH_FEATURE = 'assistant_search';
export const ASSISTANT_SEARCH_DEBOUNCE_MS = 200;
export const ASSISTANT_SEARCH_LIMIT = 30;
export const ASSISTANT_FULL_SEARCH_AGENT_CAP = 50;

export type AssistantSearchScope = 'chats' | 'agents';

export interface AssistantSearchHit {
  readonly item_id: string;
  readonly seq: number;
  readonly role: string;
  readonly snippet: string;
  readonly ranges: number[][];
}

export interface AssistantSearchResult {
  readonly session_id: string;
  readonly machine_id: string;
  readonly title: string;
  readonly archived: boolean;
  readonly title_match: boolean;
  readonly hits: AssistantSearchHit[];
}

export interface AssistantSearchAnswer {
  readonly results: AssistantSearchResult[];
  readonly partial: boolean;
}

export interface AssistantSearchSessionInput {
  readonly id: string;
  readonly session_id: string;
  readonly machine_id: string;
  readonly title: string;
  readonly last_text?: string;
  readonly updated_at?: string;
  readonly archived?: boolean;
  readonly assistant?: boolean;
  readonly is_subagent?: boolean;
  readonly cwd?: string;
  readonly branch?: string;
  readonly machine_name?: string;
  readonly main?: boolean;
  readonly herdr_tab_name?: string;
  readonly prefer_herdr_tab?: boolean;
  readonly state?: string;
}

export interface AssistantSearchMergedRow<T extends AssistantSearchSessionInput = AssistantSearchSessionInput> {
  readonly session: T;
  readonly hit?: AssistantSearchHit;
}

export interface AssistantSearchScheduleContext {
  readonly query: string;
  readonly scope: AssistantSearchScope;
  readonly connected: boolean;
  readonly supportsRemote: boolean;
  readonly hubKey: string;
  readonly fetcher: (query: string) => Promise<AssistantSearchAnswer>;
  readonly onResult: (query: string, answer: AssistantSearchAnswer | undefined, failed: boolean) => void;
}

function asRecord(value: Object | undefined | null): Record<string, Object> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
  return value as Record<string, Object>;
}

function cleanString(value: Object | undefined): string {
  return typeof value === 'string' ? value.trim() : '';
}

function rawString(value: Object | undefined): string {
  return typeof value === 'string' ? value : '';
}

function sessionTimestamp(item: AssistantSearchSessionInput): number {
  const ms = Date.parse(item.updated_at || '');
  return Number.isFinite(ms) ? ms : 0;
}

function sessionKeyOf(item: AssistantSearchSessionInput): string {
  return (item.session_id || item.id || '').trim();
}

export function assistantSearchPath(query: string, limit: number = ASSISTANT_SEARCH_LIMIT): string {
  const q = (query || '').trim();
  const safeLimit = Number.isFinite(limit) && limit > 0 ? Math.floor(limit) : ASSISTANT_SEARCH_LIMIT;
  return '/api/v1/assistant/search?q=' + encodeURIComponent(q) + '&limit=' + String(safeLimit);
}

export function parseAssistantSearchAnswer(raw: Record<string, Object>): AssistantSearchAnswer {
  const rawResults = Array.isArray(raw['results']) ? (raw['results'] as Object[]) : [];
  const results: AssistantSearchResult[] = [];
  for (const item of rawResults) {
    const rec = asRecord(item);
    const sessionId = cleanString(rec['session_id']);
    const machineId = cleanString(rec['machine_id']);
    if (!sessionId || !machineId) continue;
    const rawHits = Array.isArray(rec['hits']) ? (rec['hits'] as Object[]) : [];
    const hits: AssistantSearchHit[] = [];
    for (const h of rawHits) {
      const hitRec = asRecord(h);
      const itemId = cleanString(hitRec['item_id']);
      const snippet = rawString(hitRec['snippet']);
      if (!itemId && !snippet) continue;
      const seqRaw = hitRec['seq'];
      const seq = typeof seqRaw === 'number' && Number.isFinite(seqRaw) ? Math.floor(seqRaw) : 0;
      hits.push({
        item_id: itemId,
        seq,
        role: cleanString(hitRec['role']),
        snippet,
        ranges: searchHitRanges(hitRec['ranges'])
      });
    }
    results.push({
      session_id: sessionId,
      machine_id: machineId,
      title: rawString(rec['title']),
      archived: rec['archived'] === true,
      title_match: rec['title_match'] === true,
      hits
    });
  }
  return {
    results,
    partial: raw['partial'] === true
  };
}

export function localAssistantChatMatches<T extends AssistantSearchSessionInput>(
  conversations: readonly T[],
  query: string
): T[] {
  const words = (query || '').trim().toLowerCase().split(/\s+/).filter((w: string) => !!w);
  const filtered = conversations.filter((item: T) => {
    if (item.archived) return false;
    if (words.length === 0) return true;
    const haystack = [item.title || '', item.last_text || ''].join(' ').toLowerCase();
    return words.every((word: string) => haystack.includes(word));
  });
  return filtered.slice().sort((a: T, b: T) => sessionTimestamp(b) - sessionTimestamp(a));
}

export function localAgentSearchMatches<T extends AssistantSearchSessionInput>(
  sessions: readonly T[],
  query: string,
  limit: number = ASSISTANT_FULL_SEARCH_AGENT_CAP
): T[] {
  const words = (query || '').trim().toLowerCase().split(/\s+/).filter((w: string) => !!w);
  const filtered = sessions.filter((item: T) => {
    if (item.assistant || item.is_subagent || item.archived) return false;
    if (words.length === 0) return true;
    const haystack = [
      item.title || '',
      item.herdr_tab_name || '',
      item.last_text || '',
      item.cwd || '',
      item.branch || '',
      item.machine_name || '',
      item.machine_id || ''
    ].join(' ').toLowerCase();
    return words.every((word: string) => haystack.includes(word));
  });
  return filtered
    .slice()
    .sort((a: T, b: T) => sessionTimestamp(b) - sessionTimestamp(a))
    .slice(0, Math.max(1, limit));
}

/**
 * Ports `AssistantSearchMerge.rows(local:answer:known:)`:
 * manager results first (each with its newest hit unless `title_match`),
 * followed by local matches not already returned.
 */
export function mergeAssistantSearchRows<T extends AssistantSearchSessionInput>(
  local: readonly T[],
  answer: AssistantSearchAnswer | undefined,
  known: readonly T[]
): AssistantSearchMergedRow<T>[] {
  if (!answer) {
    return local.map((session: T): AssistantSearchMergedRow<T> => ({ session }));
  }
  const byMachineAndId = new Map<string, T>();
  const byId = new Map<string, T>();
  for (const item of known) {
    const sid = sessionKeyOf(item);
    if (!sid) continue;
    const machineKey = (item.machine_id || '') + '|' + sid;
    if (!byMachineAndId.has(machineKey)) byMachineAndId.set(machineKey, item);
    if (!byId.has(sid)) byId.set(sid, item);
  }
  const out: AssistantSearchMergedRow<T>[] = [];
  const seen = new Set<string>();
  for (const result of answer.results) {
    const sid = result.session_id;
    if (!sid || seen.has(sid)) continue;
    const match = byMachineAndId.get((result.machine_id || '') + '|' + sid) || byId.get(sid);
    if (!match) continue;
    seen.add(sid);
    out.push({
      session: match,
      hit: result.title_match ? undefined : result.hits[0]
    });
  }
  for (const item of local) {
    const sid = sessionKeyOf(item);
    if (!sid || seen.has(sid)) continue;
    seen.add(sid);
    out.push({ session: item });
  }
  return out;
}

export function assistantSearchLead(fontScale: number): number {
  if (fontScale <= 1) return 8;
  if (fontScale <= 21 / 17) return 5;
  return 2;
}

export function assistantSearchSnippetParts(
  snippet: string,
  ranges: readonly number[][],
  fontScale: number = 1
): SearchTextPart[] {
  return phoneSearchExcerpt(snippet, ranges as number[][], fontScale);
}

/**
 * Debounced, sequence-guarded coordinator for `GET /api/v1/assistant/search`.
 * Caches hubs returning 404 so older hubs stay on local matching without repeated errors.
 */
export class AssistantSearchDebouncer {
  private sequence: number = 0;
  private timer: number = -1;
  private answeredQuery: string = '';
  private currentAnswer: AssistantSearchAnswer | undefined = undefined;
  private unsupportedHubs: Set<string> = new Set<string>();
  private readonly delayMs: number;

  constructor(delayMs: number = ASSISTANT_SEARCH_DEBOUNCE_MS) {
    this.delayMs = delayMs;
  }

  answerFor(query: string): AssistantSearchAnswer | undefined {
    const q = (query || '').trim();
    if (!q || q !== this.answeredQuery) return undefined;
    return this.currentAnswer;
  }

  isUnsupported(hubKey: string): boolean {
    return !!hubKey && this.unsupportedHubs.has(hubKey);
  }

  cancel(): void {
    this.sequence++;
    if (this.timer !== -1) {
      clearTimeout(this.timer);
      this.timer = -1;
    }
    this.answeredQuery = '';
    this.currentAnswer = undefined;
  }

  schedule(ctx: AssistantSearchScheduleContext): boolean {
    if (this.timer !== -1) {
      clearTimeout(this.timer);
      this.timer = -1;
    }
    this.sequence++;
    const token = this.sequence;
    const q = (ctx.query || '').trim();
    if (!q || ctx.scope !== 'chats' || !ctx.connected || !ctx.supportsRemote || this.isUnsupported(ctx.hubKey)) {
      this.answeredQuery = '';
      this.currentAnswer = undefined;
      ctx.onResult(q, undefined, false);
      return false;
    }
    this.timer = setTimeout(() => {
      this.timer = -1;
      if (token !== this.sequence) return;
      ctx.fetcher(q).then((answer: AssistantSearchAnswer) => {
        if (token !== this.sequence) return;
        this.answeredQuery = q;
        this.currentAnswer = answer;
        ctx.onResult(q, answer, false);
      }).catch((error: Object) => {
        if (token !== this.sequence) return;
        const rec = asRecord(error);
        const status = typeof rec['status'] === 'number' ? Number(rec['status']) : 0;
        const message = typeof rec['message'] === 'string' ? String(rec['message']) : '';
        if (status === 404 || message === 'not_found' || message === 'unsupported_feature' || message === 'unsupported') {
          if (ctx.hubKey) this.unsupportedHubs.add(ctx.hubKey);
          this.answeredQuery = '';
          this.currentAnswer = undefined;
          ctx.onResult(q, undefined, false);
          return;
        }
        ctx.onResult(q, undefined, true);
      });
    }, this.delayMs) as Object as number;
    return true;
  }
}
