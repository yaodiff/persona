/**
 * Pure TypeScript Personal Agents parsing, entry gating (`0eba2caef`, #16793),
 * and list presentation aligned with iOS `PersonalAgentModels.swift` & `PersonalAgentListModel.swift`.
 */

export interface PersonalAgentProvider {
  readonly provider: string;
  readonly connection: string;
  readonly available: boolean;
}

export interface PersonalAgentAccount {
  readonly accountId: string;
  readonly provider: string;
  readonly displayName: string;
  readonly status: string;
  readonly lastError: string;
  readonly sessionCount: number;
}

export interface PersonalAgentSourceStatus {
  readonly source: string;
  readonly enabled: boolean;
  readonly error: string;
  readonly setup: string;
  readonly providers: PersonalAgentProvider[];
  readonly accounts: PersonalAgentAccount[];
}

export interface PersonalAgentSession {
  readonly id: string;
  readonly source: string;
  readonly sessionKey: string;
  readonly accountId: string;
  readonly provider: string;
  readonly title: string;
  readonly kind: string;
  readonly canSend: boolean;
  readonly unreadCount: number;
  readonly rankTimeMs: number;
  readonly parentSessionKey: string;
  readonly previewText: string;
  readonly previewIsMine: boolean;
  readonly onTopRankTimeMs: number;
}

export interface PersonalAgentSessionsPage {
  readonly sessions: PersonalAgentSession[];
  readonly total?: number;
  readonly hasMore: boolean;
}

export interface PersonalAgentEntrySummary {
  readonly available: boolean;
  readonly count: number;
}

export interface PersonalAgentMessageBlock {
  readonly type: string;
  readonly text: string;
  readonly name: string;
  readonly status: string;
  readonly summary: string;
}

export interface PersonalAgentMessage {
  readonly externalId: string;
  readonly role: string;
  readonly plainText: string;
  readonly createTimeMs: number;
}

function asObj(raw: Object | null | undefined): Record<string, Object> | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  return raw as Record<string, Object>;
}

function textVal(obj: Record<string, Object> | null, key: string, altKey?: string): string {
  if (!obj) return '';
  const direct = obj[key];
  if (typeof direct === 'string') return direct;
  if (altKey) {
    const alt = obj[altKey];
    if (typeof alt === 'string') return alt;
  }
  return '';
}

function numVal(obj: Record<string, Object> | null, key: string, altKey?: string): number {
  if (!obj) return 0;
  const direct = obj[key];
  if (typeof direct === 'number' && Number.isFinite(direct)) return direct;
  if (altKey) {
    const alt = obj[altKey];
    if (typeof alt === 'number' && Number.isFinite(alt)) return alt;
  }
  return 0;
}

function boolVal(obj: Record<string, Object> | null, key: string, altKey?: string): boolean {
  if (!obj) return false;
  const direct = obj[key];
  if (typeof direct === 'boolean') return direct;
  if (altKey) {
    const alt = obj[altKey];
    if (typeof alt === 'boolean') return alt;
  }
  return false;
}

export function parsePersonalAgentSources(raw: Object | null | undefined): PersonalAgentSourceStatus[] {
  const root = asObj(raw);
  const rawSources = Array.isArray(root?.['sources']) ? (root?.['sources'] as Object[]) : [];
  const out: PersonalAgentSourceStatus[] = [];
  for (const entry of rawSources) {
    const obj = asObj(entry);
    if (!obj) continue;
    const source = textVal(obj, 'source');
    if (!source) continue;
    const providers: PersonalAgentProvider[] = [];
    if (Array.isArray(obj['providers'])) {
      for (const p of obj['providers'] as Object[]) {
        const po = asObj(p);
        if (!po) continue;
        providers.push({
          provider: textVal(po, 'provider'),
          connection: textVal(po, 'connection'),
          available: po['available'] === true
        });
      }
    }
    const accounts: PersonalAgentAccount[] = [];
    if (Array.isArray(obj['accounts'])) {
      for (const a of obj['accounts'] as Object[]) {
        const ao = asObj(a);
        if (!ao) continue;
        accounts.push({
          accountId: textVal(ao, 'accountId', 'account_id'),
          provider: textVal(ao, 'provider'),
          displayName: textVal(ao, 'displayName', 'display_name'),
          status: textVal(ao, 'status'),
          lastError: textVal(ao, 'lastError', 'last_error'),
          sessionCount: numVal(ao, 'sessionCount', 'session_count')
        });
      }
    }
    out.push({
      source,
      enabled: obj['enabled'] === true,
      error: textVal(obj, 'error'),
      setup: textVal(obj, 'setup'),
      providers,
      accounts
    });
  }
  return out;
}

function parseSession(entry: Object | null | undefined): PersonalAgentSession | null {
  const obj = asObj(entry);
  if (!obj) return null;
  const id = textVal(obj, 'id');
  const sessionKey = textVal(obj, 'sessionKey', 'session_key');
  if (!id && !sessionKey) return null;
  return {
    id: id || `${textVal(obj, 'source')}:${sessionKey}`,
    source: textVal(obj, 'source'),
    sessionKey,
    accountId: textVal(obj, 'accountId', 'account_id'),
    provider: textVal(obj, 'provider'),
    title: textVal(obj, 'title'),
    kind: textVal(obj, 'kind'),
    canSend: boolVal(obj, 'canSend', 'can_send'),
    unreadCount: numVal(obj, 'unreadCount', 'unread_count'),
    rankTimeMs: numVal(obj, 'rankTimeMs', 'rank_time_ms'),
    parentSessionKey: textVal(obj, 'parentSessionKey', 'parent_session_key'),
    previewText: textVal(obj, 'previewText', 'preview_text'),
    previewIsMine: boolVal(obj, 'previewIsMine', 'preview_is_mine'),
    onTopRankTimeMs: numVal(obj, 'onTopRankTimeMs', 'on_top_rank_time_ms')
  };
}

export function parsePersonalAgentSessionsPage(raw: Object | null | undefined): PersonalAgentSessionsPage {
  const root = asObj(raw);
  const rawSessions = Array.isArray(root?.['sessions']) ? (root?.['sessions'] as Object[]) : [];
  const sessions: PersonalAgentSession[] = [];
  for (const entry of rawSessions) {
    const parsed = parseSession(entry);
    if (parsed) sessions.push(parsed);
  }
  const hasMore = root?.['hasMore'] === true || root?.['has_more'] === true;
  const totalRaw = root?.['total'];
  const total = typeof totalRaw === 'number' && Number.isFinite(totalRaw) ? Math.max(0, Math.trunc(totalRaw)) : undefined;
  return { sessions, total, hasMore };
}

export function parsePersonalAgentMessages(raw: Object | null | undefined): PersonalAgentMessage[] {
  const root = asObj(raw);
  const rawMessages = Array.isArray(root?.['messages']) ? (root?.['messages'] as Object[]) : [];
  const out: PersonalAgentMessage[] = [];
  for (const entry of rawMessages) {
    const obj = asObj(entry);
    if (!obj) continue;
    const blocks = Array.isArray(obj['blocks']) ? (obj['blocks'] as Object[]) : [];
    const lines: string[] = [];
    for (const block of blocks) {
      const bo = asObj(block);
      if (!bo) continue;
      const text = textVal(bo, 'text') || textVal(bo, 'summary') || textVal(bo, 'name');
      if (text) lines.push(text);
    }
    out.push({
      externalId: textVal(obj, 'externalId', 'external_id'),
      role: textVal(obj, 'role'),
      plainText: lines.join('\n'),
      createTimeMs: numVal(obj, 'createTimeMs', 'create_time_ms')
    });
  }
  return out;
}

/**
 * Computes whether the Personal Agents group heading should be offered and its count,
 * matching `PersonalAgentEntryModel.swift` (`#16793` / `0eba2caef`).
 */
export function computePersonalAgentEntry(
  sources: readonly PersonalAgentSourceStatus[],
  page: PersonalAgentSessionsPage
): PersonalAgentEntrySummary {
  const configured = new Set<string>();
  for (const s of sources) {
    if (
      s.enabled &&
      !['missing', 'disabled', 'no_accounts'].includes(s.setup) &&
      s.accounts.length > 0
    ) {
      configured.add(s.source);
    }
  }
  if (configured.size === 0) {
    return { available: false, count: 0 };
  }
  const shown = page.sessions.filter((session: PersonalAgentSession) => configured.has(session.source)).length;
  if (shown <= 0) {
    return { available: false, count: 0 };
  }
  const count = page.hasMore && page.total !== undefined ? Math.max(page.total, shown) : shown;
  return { available: true, count };
}

/**
 * Group heading visibility gating (`0eba2caef`): ONLY when active filter is `all`,
 * search query is empty, AND configured personal agents count > 0.
 */
export function shouldShowPersonalAgentsHeading(
  quickFilter: string,
  searchQuery: string,
  personalAgentCount: number
): boolean {
  return quickFilter === 'all' && !searchQuery.trim() && personalAgentCount > 0;
}

export function personalAgentSessionPath(id: string): string {
  return '/api/v1/agents/sessions/' + encodeURIComponent(id);
}

export function filterPersonalAgentSessions(
  sessions: readonly PersonalAgentSession[],
  query: string
): PersonalAgentSession[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return sessions.slice();
  return sessions.filter((s: PersonalAgentSession) => {
    const title = (s.title || s.sessionKey).toLowerCase();
    const provider = s.provider.toLowerCase();
    const preview = s.previewText.toLowerCase();
    return title.includes(needle) || provider.includes(needle) || preview.includes(needle);
  });
}
