/**
 * Pure TypeScript Rule Suggestions decoder, path builders, and quiet/settled state logic,
 * aligned with iOS (`RuleSuggestion.swift`, `RuleSuggestions.swift`)
 * and Go Hub (`server/internal/assistant/rule_suggest.go`).
 */

export interface RuleScope {
  readonly machineId: string;
  readonly machine: string;
  readonly directory: string;
  readonly action: string;
  readonly conditions: string[];
  readonly quote: string;
}

export interface RuleSuggestion {
  readonly id: string;
  readonly kind: string;
  readonly text: string;
  readonly scope: RuleScope;
  readonly creates: string;
  readonly setting: string;
  readonly count: number;
}

export interface RuleSuggestionMade {
  readonly kind: string;
  readonly creates: string;
  readonly setting: string;
}

export type RuleSuggestionMadeKind = 'task_trust' | 'standing_authorization' | 'setting';

export const RULE_SUGGESTION_QUIET_MS: number = 3600 * 1000;

function asObj(raw: Object | null | undefined): Record<string, Object> | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  return raw as Record<string, Object>;
}

function asStr(v: Object | undefined): string {
  return typeof v === 'string' ? v : '';
}

function asStrList(v: Object | undefined): string[] {
  if (!Array.isArray(v)) return [];
  const out: string[] = [];
  for (const item of v as Object[]) {
    if (typeof item === 'string' && item.trim()) out.push(item.trim());
  }
  return out;
}

export function ruleScopePlace(scope: RuleScope): string {
  const name = scope.machine.trim();
  return name || scope.machineId.trim();
}

export function parseRuleScope(raw: Object | null | undefined): RuleScope {
  const obj = asObj(raw);
  if (!obj) {
    return { machineId: '', machine: '', directory: '', action: '', conditions: [], quote: '' };
  }
  return {
    machineId: asStr(obj['machine_id']),
    machine: asStr(obj['machine']),
    directory: asStr(obj['directory']),
    action: asStr(obj['action']),
    conditions: asStrList(obj['conditions']),
    quote: asStr(obj['quote'])
  };
}

export function parseRuleSuggestion(raw: Object | null | undefined): RuleSuggestion | null {
  const obj = asObj(raw);
  if (!obj) return null;
  const id = asStr(obj['id']).trim();
  const text = asStr(obj['text']).trim();
  if (!id || !text) return null;
  const countRaw = obj['count'];
  const count = typeof countRaw === 'number' && Number.isFinite(countRaw) ? Math.max(0, Math.trunc(countRaw)) : 0;
  return {
    id,
    kind: asStr(obj['kind']),
    text,
    scope: parseRuleScope(obj['scope']),
    creates: asStr(obj['creates']),
    setting: asStr(obj['setting']),
    count
  };
}

export function parseRuleSuggestionMade(raw: Object | null | undefined): RuleSuggestionMade {
  const obj = asObj(raw);
  if (!obj) return { kind: '', creates: '', setting: '' };
  return {
    kind: asStr(obj['kind']),
    creates: asStr(obj['creates']),
    setting: asStr(obj['setting'])
  };
}

export function ruleSuggestionMadeKind(suggestion: RuleSuggestion): RuleSuggestionMadeKind | null {
  if (suggestion.creates === 'tasks.trust') return 'task_trust';
  if (suggestion.creates === 'standing_authorization') return 'standing_authorization';
  if (!suggestion.creates && suggestion.setting.trim()) return 'setting';
  return null;
}

export function isRuleSuggestionOffered(suggestion: RuleSuggestion | null | undefined): boolean {
  if (!suggestion) return false;
  return !!suggestion.id.trim() && !!suggestion.text.trim() && ruleSuggestionMadeKind(suggestion) !== null;
}

export function ruleSuggestionAlwaysPath(id: string, manager?: string): string {
  const base = '/api/v1/assistant/rule-suggestions/' + encodeURIComponent(id) + '/always';
  return manager ? base + '?manager=' + encodeURIComponent(manager) : base;
}

export function ruleSuggestionNotNowPath(id: string, manager?: string): string {
  const base = '/api/v1/assistant/rule-suggestions/' + encodeURIComponent(id) + '/not-now';
  return manager ? base + '?manager=' + encodeURIComponent(manager) : base;
}

export type RuleSuggestionAnswer = 'always' | 'not_now';

export interface RuleSuggestionLineState {
  readonly status: 'offered' | 'sending' | 'made' | 'hidden';
  readonly suggestion?: RuleSuggestion;
  readonly answer?: RuleSuggestionAnswer;
  readonly madeText?: string;
}

export class RuleSuggestionTracker {
  private readonly sendingMap: Map<string, RuleSuggestionAnswer> = new Map();
  private readonly settledAtMap: Map<string, number> = new Map();
  private readonly madeMap: Map<string, { readonly suggestionId: string; readonly text: string }> = new Map();

  private suggestionKey(suggestionId: string, scopeKey: string): string {
    return scopeKey + '|' + suggestionId;
  }

  private cardScopeKey(cardKey: string, scopeKey: string): string {
    return scopeKey + '|' + cardKey;
  }

  isQuiet(suggestionId: string, scopeKey: string, nowMs: number = Date.now()): boolean {
    const key = this.suggestionKey(suggestionId, scopeKey);
    const at = this.settledAtMap.get(key);
    if (at === undefined) return false;
    return nowMs - at < RULE_SUGGESTION_QUIET_MS;
  }

  isSending(suggestionId: string, scopeKey: string): boolean {
    return this.sendingMap.has(this.suggestionKey(suggestionId, scopeKey));
  }

  markSending(suggestionId: string, scopeKey: string, answer: RuleSuggestionAnswer): void {
    this.sendingMap.set(this.suggestionKey(suggestionId, scopeKey), answer);
  }

  clearSending(suggestionId: string, scopeKey: string): void {
    this.sendingMap.delete(this.suggestionKey(suggestionId, scopeKey));
  }

  markMade(cardKey: string, scopeKey: string, suggestionId: string, text: string): void {
    this.madeMap.set(this.cardScopeKey(cardKey, scopeKey), { suggestionId, text });
  }

  markSettled(suggestionId: string, scopeKey: string, nowMs: number = Date.now()): void {
    const expired: string[] = [];
    this.settledAtMap.forEach((at: number, k: string) => {
      if (nowMs - at >= RULE_SUGGESTION_QUIET_MS) expired.push(k);
    });
    for (const k of expired) this.settledAtMap.delete(k);
    this.settledAtMap.set(this.suggestionKey(suggestionId, scopeKey), nowMs);
  }

  lineState(
    suggestion: RuleSuggestion | null | undefined,
    cardKey: string,
    scopeKey: string,
    nowMs: number = Date.now()
  ): RuleSuggestionLineState {
    const made = this.madeMap.get(this.cardScopeKey(cardKey, scopeKey));
    if (!suggestion || !isRuleSuggestionOffered(suggestion)) {
      return made ? { status: 'made', madeText: made.text } : { status: 'hidden' };
    }
    const sKey = this.suggestionKey(suggestion.id, scopeKey);
    const quiet = this.isQuiet(suggestion.id, scopeKey, nowMs);
    if (made && (made.suggestionId === suggestion.id || quiet)) {
      return { status: 'made', madeText: made.text };
    }
    if (quiet) return { status: 'hidden' };
    const sending = this.sendingMap.get(sKey);
    if (sending) return { status: 'sending', suggestion, answer: sending };
    return { status: 'offered', suggestion };
  }
}

export const sharedRuleSuggestionTracker: RuleSuggestionTracker = new RuleSuggestionTracker();
