/**
 * Pure TypeScript Standing Authorizations & Session Always-Allow Grants helpers
 * aligned with `apple/Persona/Views/Assistant/StandingAuthorizations.swift`,
 * `apple/Persona/Views/Chat/AlwaysGrantsSection.swift`, and
 * `server/internal/assistant/{standing_authorizations,always}.go`.
 * Zero ArkUI dependencies so host Node test suites can import directly.
 */

export interface StandingAuthorization {
  readonly id: string;
  readonly machine_id: string;
  readonly directory: string;
  readonly actions: string[];
  readonly conditions: string[];
  readonly quote: string;
  readonly source: string;
  readonly granted_at: string;
  readonly reversible: boolean;
}

export interface StandingAuthorizationList {
  readonly revision: number;
  readonly authorizations: StandingAuthorization[];
}

export interface AlwaysGrant {
  readonly key_hash: string;
  readonly tool_name: string;
  readonly created_at: string;
}

export interface AlwaysRevokedResult {
  readonly ok: boolean;
  readonly restart_required: boolean;
}

export interface StandingSourceSummary {
  readonly kind: 'suggested_rule' | 'main_chat' | 'a_chat' | 'settings' | 'raw';
  readonly count: number;
  readonly raw: string;
}

function asRecord(value: Object | undefined | null): Record<string, Object> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
  return value as Record<string, Object>;
}

function cleanString(value: Object | undefined): string {
  return typeof value === 'string' ? value.trim() : '';
}

function stringList(value: Object | undefined): string[] {
  if (!Array.isArray(value)) return [];
  const out: string[] = [];
  for (const item of value as Object[]) {
    if (typeof item === 'string') {
      const clean = item.trim();
      if (clean) out.push(clean);
    }
  }
  return out;
}

export function standingAuthorizationsPath(): string {
  return '/api/v1/assistant/standing-authorizations';
}

export function revokeStandingAuthorizationPath(id: string): string {
  return '/api/v1/assistant/standing-authorizations/' + encodeURIComponent((id || '').trim());
}

export function alwaysGrantsPath(conversationId: string): string {
  return '/api/v1/assistant/conversations/' + encodeURIComponent((conversationId || '').trim()) + '/always';
}

export function revokeAlwaysGrantPath(conversationId: string, keyHash: string): string {
  return alwaysGrantsPath(conversationId) + '/' + encodeURIComponent((keyHash || '').trim());
}

export function parseStandingAuthorizationsResponse(raw: Record<string, Object>): StandingAuthorizationList {
  const revisionRaw = raw['revision'];
  const revision = typeof revisionRaw === 'number' && Number.isFinite(revisionRaw)
    ? Math.max(0, Math.floor(revisionRaw))
    : 0;
  const rows = Array.isArray(raw['authorizations']) ? (raw['authorizations'] as Object[]) : [];
  const authorizations: StandingAuthorization[] = [];
  const seen = new Set<string>();
  for (const item of rows) {
    const rec = asRecord(item);
    const id = cleanString(rec['id']);
    if (!id || seen.has(id)) continue;
    seen.add(id);
    authorizations.push({
      id,
      machine_id: cleanString(rec['machine_id']),
      directory: cleanString(rec['directory']),
      actions: stringList(rec['actions']),
      conditions: stringList(rec['conditions']),
      quote: typeof rec['quote'] === 'string' ? (rec['quote'] as string).trim() : '',
      source: cleanString(rec['source']),
      granted_at: cleanString(rec['granted_at']),
      reversible: rec['reversible'] === true
    });
  }
  return { revision, authorizations };
}

export function parseAlwaysGrantsResponse(raw: Record<string, Object>): AlwaysGrant[] {
  const rows = Array.isArray(raw['grants']) ? (raw['grants'] as Object[]) : [];
  const grants: AlwaysGrant[] = [];
  const seen = new Set<string>();
  for (const item of rows) {
    const rec = asRecord(item);
    const keyHash = cleanString(rec['key_hash']);
    if (!keyHash || seen.has(keyHash)) continue;
    seen.add(keyHash);
    grants.push({
      key_hash: keyHash,
      tool_name: cleanString(rec['tool_name']),
      created_at: cleanString(rec['created_at'])
    });
  }
  return grants;
}

export function parseAlwaysRevokeResponse(raw: Record<string, Object>): AlwaysRevokedResult {
  return {
    ok: raw['ok'] !== false,
    restart_required: raw['restart_required'] === true
  };
}

export function filterOutRevokedStandingGrants(
  grants: readonly StandingAuthorization[],
  revokingIds: readonly string[],
  revokedIds: readonly string[]
): StandingAuthorization[] {
  return grants.filter((grant: StandingAuthorization) =>
    !revokingIds.includes(grant.id) && !revokedIds.includes(grant.id)
  );
}

export function standingFolderName(directory: string): string {
  const clean = (directory || '').trim().replace(/[\\/]+$/, '');
  if (!clean) return directory || '';
  const parts = clean.split(/[\\/]/).filter((part: string) => !!part);
  return parts.length > 0 ? parts[parts.length - 1] : clean;
}

export function parseSuggestedRuleCount(source: string): number | undefined {
  const text = (source || '').trim();
  if (!text) return undefined;
  const patterns: RegExp[] = [
    /^Suggested rule: the same answer (\d+) times in 14 days, confirmed with Always\.$/,
    /^建议的规则：14 天内 (\d+) 次相同的回答，点「总是」确认。$/,
    /^提案されたルール：14 日間に同じ回答が (\d+) 回あり、「常に」で確認されました。$/
  ];
  for (const pattern of patterns) {
    const match = pattern.exec(text);
    if (match && match[1]) {
      const n = Number(match[1]);
      if (Number.isInteger(n) && n > 0) return n;
    }
  }
  return undefined;
}

export function normalizeStandingSource(rawSource: string): StandingSourceSummary {
  const raw = (rawSource || '').trim();
  const count = parseSuggestedRuleCount(raw);
  if (count !== undefined) {
    return { kind: 'suggested_rule', count, raw };
  }
  const key = raw.toLowerCase().replace(/^[.。\s]+|[.。\s]+$/g, '');
  if (['main conversation', 'the main conversation', 'main chat', 'the main chat',
    '主对话', '主会话', '主聊天', 'メインチャット', 'メインの会話', 'メイン会話'].includes(key)) {
    return { kind: 'main_chat', count: 0, raw };
  }
  if (['conversation', 'a conversation', 'chat', 'a chat', '对话', '会话', '会話', 'チャット'].includes(key)) {
    return { kind: 'a_chat', count: 0, raw };
  }
  if (['settings', '设置', '設定'].includes(key)) {
    return { kind: 'settings', count: 0, raw };
  }
  return { kind: 'raw', count: 0, raw };
}

export function standingActionLabelKey(action: string): string {
  switch (action) {
    case 'merge': return 'standing_action_merge';
    case 'push': return 'standing_action_push';
    case 'deploy-test': return 'standing_action_deploy_test';
    case 'deploy-production': return 'standing_action_deploy_production';
    case 'send': return 'standing_action_send';
    case 'delete': return 'standing_action_delete';
    case 'spend': return 'standing_action_spend';
    default: return '';
  }
}

export function standingConditionLabelKey(condition: string): string {
  switch (condition) {
    case 'ci_green': return 'standing_condition_ci_green';
    case 'no_conflicts': return 'standing_condition_no_conflicts';
    default: return '';
  }
}
