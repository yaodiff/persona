/** Pure headless XiaoYi business logic shared by Intents Kit, On-Device A2A, and AgentUI cards. No platform imports. */

export interface XiaoYiMachine {
  id: string;
  name: string;
  online: boolean;
  trusted: boolean;
  manager: string;
}

export interface XiaoYiSession {
  id: string;
  machineId: string;
  title: string;
  status: string;
  state: string;
  updatedAt: string;
  assistant: boolean;
  quiet: boolean;
  muted: boolean;
  spaceName: string;
  unreadCount: number;
  canSend: boolean;
}

export interface XiaoYiInteraction {
  id: string;
  machineId: string;
  sessionId: string;
  kind: string;
  status: string;
  summary: string;
  toolName: string;
  createdAt: string;
  delegating: boolean;
  muted: boolean;
  quiet: boolean;
  planThen: string;
  hasRequiredFields: boolean;
}

export interface XiaoYiMuteRules {
  spaces: string[];
  sessions: string[];
  allowed: string[];
}

export interface XiaoYiMachineSelection {
  ok: boolean;
  machine?: XiaoYiMachine;
  errorCode: string;
  target: string;
}

export interface XiaoYiSessionSelection {
  ok: boolean;
  session?: XiaoYiSession;
  errorCode: string;
  target: string;
}

export interface XiaoYiApprovalPayload {
  kind: 'resolve' | 'needs_screen';
  reason: string;
  body?: Record<string, Object>;
}

export interface XiaoYiStatusSummary {
  workingCount: number;
  pendingCount: number;
  unreadPersonalCount: number;
  topPending?: XiaoYiInteraction;
  headline: string;
  detail: string;
  spokenText: string;
  profileId?: string;
  managerId?: string;
}

export interface XiaoYiPushItem {
  key: string;
  title: string;
  body: string;
  app: string;
  link?: string;
  time: string;
  createdAtMs: number;
}

export interface XiaoYiTranscriptEntry {
  seq: number;
  kind: string;
  role: string;
  text: string;
}

export interface XiaoYiAssistantAnswer {
  text: string;
  maxSeq: number;
  hasReply: boolean;
  segments: string[];
}

export interface XiaoYiErrorDescriptor {
  key: string;
  host: string;
  target: string;
}

export const XIAOYI_OUTBOX_MAX_ITEMS: number = 200;
export const XIAOYI_OUTBOX_TTL_MS: number = 7 * 24 * 3600 * 1000;
export const XIAOYI_OUTBOX_BATCH_SIZE: number = 20;
export const XIAOYI_PUSH_BODY_MAX: number = 8000;

import {
  HUB_MODULES_MARKER,
  HUB_MODULE_PREFIX,
  RECORDING_MODULE_FEATURE,
  extractPublishedModules,
  isHubModuleAllowed
} from './HubModules.ts';

export { HUB_MODULES_MARKER, HUB_MODULE_PREFIX, RECORDING_MODULE_FEATURE, extractPublishedModules };

/** Serialize remembered Hub module flags for LocalStore persistence (`xiaoyi:<scope>:hub-modules`). */
export function serializeStoredHubModules(published: readonly string[] | null): string {
  const payload: Record<string, Object> = {};
  payload['published'] = published === null ? '' : published.slice();
  payload['hasMarker'] = published !== null;
  return JSON.stringify(payload);
}

/** Decode remembered Hub module flags from LocalStore. Returns `null` when never heard or older hub. */
export function parseStoredHubModules(raw: string): string[] | null {
  if (!raw || !raw.trim()) return null;
  try {
    const parsed = JSON.parse(raw) as Record<string, Object>;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null;
    if (parsed['hasMarker'] !== true) return null;
    const list = parsed['published'];
    if (!Array.isArray(list)) return [];
    const out: string[] = [];
    for (let i = 0; i < list.length; i++) {
      const item = list[i];
      if (typeof item === 'string' && item.startsWith(HUB_MODULE_PREFIX)) out.push(item);
    }
    return out;
  } catch (_) {
    return null;
  }
}

/**
 * Fail-closed recording module check matching upstream iOS `RecordingModule.allowance` (#15978 / #15985):
 * off ONLY when the Hub announces `modules` and lacks `module.recording` (live `features` takes precedence over
 * remembered `storedPublished`; an older Hub without `modules` or a Hub never heard from remains on).
 */
export function isRecordingModuleAllowed(
  features?: readonly string[],
  storedPublished?: readonly string[] | null
): boolean {
  return isHubModuleAllowed(RECORDING_MODULE_FEATURE, features ?? [], storedPublished);
}

/** Plain-language recording refusal matching iOS `MeetingRecorder.startRefusal` (#15985) across en/zh-Hans/ja. */
export function recordingModuleRefusalText(locale: string = 'en'): string {
  const lang = normalizeLocale(locale);
  if (lang === 'zh-Hans') return '这台 hub 关闭了会议录音。';
  if (lang === 'ja') return 'このhubでは会議の録音がオフです。';
  return 'Meeting recording is off on this hub.';
}

function parseTimestampMs(raw: string): number {
  if (!raw) return Number.POSITIVE_INFINITY;
  const ms = Date.parse(raw);
  if (!Number.isFinite(ms)) return Number.POSITIVE_INFINITY;
  const frac = /:\d{2}\.\d{3}(\d+)/.exec(raw);
  return frac ? ms + Number('0.' + frac[1]) : ms;
}

function isInteractionClosed(status: string): boolean {
  const s = (status || '').toLowerCase();
  return s !== '' && s !== 'open' && s !== 'pending';
}

function isSessionMutedByRules(session: XiaoYiSession | undefined, machineId: string, sessionId: string, mutes: XiaoYiMuteRules): boolean {
  const sessionKey = machineId && sessionId ? machineId + '/' + sessionId : '';
  if (sessionKey && mutes.allowed.includes(sessionKey)) return false;
  if (sessionKey && mutes.sessions.includes(sessionKey)) return true;
  if (session && session.spaceName && machineId) {
    const spaceKey = machineId + '/' + session.spaceName;
    if (mutes.spaces.includes(spaceKey)) return true;
  }
  return session ? session.muted : false;
}

/**
 * Filter and order pending approvals oldest-first, matching the badge and iOS IntentPicker rule
 * so CheckPersonaStatus and ApproveFirstPending ALWAYS agree on the exact same top item.
 */
export function selectPendingApprovals(
  interactions: readonly XiaoYiInteraction[],
  sessions: readonly XiaoYiSession[],
  mutes: XiaoYiMuteRules = { spaces: [], sessions: [], allowed: [] }
): XiaoYiInteraction[] {
  const sessionMap = new Map<string, XiaoYiSession>();
  for (let i = 0; i < sessions.length; i++) {
    const s = sessions[i];
    sessionMap.set(s.machineId + '/' + s.id, s);
  }

  const eligible: XiaoYiInteraction[] = [];
  for (let i = 0; i < interactions.length; i++) {
    const item = interactions[i];
    if (!item.id || isInteractionClosed(item.status)) continue;
    // Delegated non-question approvals belong to the task supervisor unless escalated.
    if (item.delegating && item.kind !== 'question') continue;
    if (item.quiet || item.muted) continue;

    const session = item.machineId && item.sessionId ? sessionMap.get(item.machineId + '/' + item.sessionId) : undefined;
    if (session && session.quiet) continue;
    if (isSessionMutedByRules(session, item.machineId, item.sessionId, mutes)) continue;
    eligible.push(item);
  }

  return eligible.sort((a: XiaoYiInteraction, b: XiaoYiInteraction): number => {
    const ta = parseTimestampMs(a.createdAt);
    const tb = parseTimestampMs(b.createdAt);
    if (ta !== tb) return ta < tb ? -1 : 1;
    if (a.id === b.id) return 0;
    return a.id < b.id ? -1 : 1;
  });
}

/**
 * Build the safe Hub resolution payload for a background/card approval.
 * Plans ONLY approve with `acceptEdits` (NEVER `bypassPermissions`); questions/screens defer to the app.
 */
export function buildApprovalPayload(decision: XiaoYiInteraction): XiaoYiApprovalPayload {
  if (decision.kind === 'permission') {
    if (decision.hasRequiredFields || decision.toolName === 'secret_run' || decision.toolName === 'secret_handoff') {
      return { kind: 'needs_screen', reason: 'secret_fields' };
    }
    const body: Record<string, Object> = {};
    body['allow'] = true;
    return { kind: 'resolve', reason: 'permission_allow', body: body };
  }
  if (decision.kind === 'plan') {
    if (decision.planThen === 'bypassPermissions') {
      return { kind: 'needs_screen', reason: 'bypass_requires_app' };
    }
    const body: Record<string, Object> = {};
    body['approve'] = true;
    body['mode'] = decision.planThen && ['default', 'acceptEdits', 'auto'].includes(decision.planThen)
      ? decision.planThen
      : 'acceptEdits';
    return { kind: 'resolve', reason: 'plan_accept_edits', body: body };
  }
  return { kind: 'needs_screen', reason: decision.kind || 'unsupported' };
}

/**
 * Machine selection rule matching iOS IntentPicker:
 * explicit request (fuzzy match; fail if offline or missing, never silently switch) -> remembered -> first online trusted.
 */
export function selectMachine(
  machines: readonly XiaoYiMachine[],
  requestedName: string = '',
  rememberedMachineId: string = ''
): XiaoYiMachineSelection {
  const trusted = machines.filter((m: XiaoYiMachine): boolean => m.trusted && !!m.id);
  const query = requestedName.trim();
  if (query) {
    const lowered = query.toLowerCase();
    let matched = trusted.find((m: XiaoYiMachine): boolean => m.id === query);
    if (!matched) matched = trusted.find((m: XiaoYiMachine): boolean => m.name.toLowerCase() === lowered);
    if (!matched) {
      matched = trusted.find((m: XiaoYiMachine): boolean =>
        m.name.toLowerCase().includes(lowered) || m.id.toLowerCase().includes(lowered)
      );
    }
    if (!matched) {
      return { ok: false, errorCode: 'xiaoyi_machine_not_found', target: query };
    }
    if (!matched.online) {
      return { ok: false, errorCode: 'xiaoyi_machine_offline', target: matched.name || matched.id };
    }
    return { ok: true, machine: matched, errorCode: '', target: matched.name || matched.id };
  }

  const online = trusted.filter((m: XiaoYiMachine): boolean => m.online);
  if (rememberedMachineId) {
    const remembered = online.find((m: XiaoYiMachine): boolean => m.id === rememberedMachineId);
    if (remembered) {
      return { ok: true, machine: remembered, errorCode: '', target: remembered.name || remembered.id };
    }
  }
  if (online.length > 0) {
    const first = online[0];
    return { ok: true, machine: first, errorCode: '', target: first.name || first.id };
  }
  return { ok: false, errorCode: 'xiaoyi_no_online_machine', target: '' };
}

/** Select an active non-offline sendable agent session on the chosen machine. */
export function selectLiveSession(
  sessions: readonly XiaoYiSession[],
  machineId: string,
  requestedName: string = ''
): XiaoYiSessionSelection {
  const live = sessions.filter((s: XiaoYiSession): boolean =>
    s.machineId === machineId &&
    !s.assistant &&
    s.canSend !== false &&
    s.status.toLowerCase() !== 'offline' &&
    s.state.toLowerCase() !== 'offline'
  );
  const ordered = live.slice().sort((a: XiaoYiSession, b: XiaoYiSession): number => {
    const ta = Date.parse(a.updatedAt);
    const tb = Date.parse(b.updatedAt);
    const va = Number.isFinite(ta) ? ta : 0;
    const vb = Number.isFinite(tb) ? tb : 0;
    if (va !== vb) return vb - va;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });

  const query = requestedName.trim();
  if (query) {
    const lowered = query.toLowerCase();
    let matched = ordered.find((s: XiaoYiSession): boolean => s.id === query);
    if (!matched) matched = ordered.find((s: XiaoYiSession): boolean => s.title.toLowerCase() === lowered);
    if (!matched) {
      matched = ordered.find((s: XiaoYiSession): boolean =>
        s.title.toLowerCase().includes(lowered) || s.id.toLowerCase().includes(lowered)
      );
    }
    if (!matched) {
      return { ok: false, errorCode: 'xiaoyi_no_live_session', target: query };
    }
    return { ok: true, session: matched, errorCode: '', target: matched.title || matched.id };
  }

  if (ordered.length > 0) {
    const latest = ordered[0];
    return { ok: true, session: latest, errorCode: '', target: latest.title || latest.id };
  }
  return { ok: false, errorCode: 'xiaoyi_no_live_session', target: machineId };
}

function normalizeLocale(locale: string): 'zh-Hans' | 'ja' | 'en' {
  const lowered = (locale || '').toLowerCase();
  if (lowered.startsWith('zh')) return 'zh-Hans';
  if (lowered.startsWith('ja')) return 'ja';
  return 'en';
}

function truncateClip(text: string, maxChars: number): string {
  const clean = text.replace(/\s+/g, ' ').trim();
  const chars = Array.from(clean);
  return chars.length <= maxChars ? clean : chars.slice(0, maxChars - 1).join('') + '…';
}

/** Build a crisp spoken and display summary for XiaoYi voice & cards. */
export function formatStatusSummary(
  sessions: readonly XiaoYiSession[],
  pendingApprovals: readonly XiaoYiInteraction[],
  unreadPersonalAgents: number = 0,
  locale: string = 'en'
): XiaoYiStatusSummary {
  const lang = normalizeLocale(locale);
  const workingCount = sessions.filter((s: XiaoYiSession): boolean =>
    !s.assistant && (s.state === 'working' || s.status === 'working' || s.status === 'running')
  ).length;
  const pendingCount = pendingApprovals.length;
  const top = pendingCount > 0 ? pendingApprovals[0] : undefined;
  const topLabel = top ? truncateClip(top.summary || top.toolName || top.kind, 120) : '';

  let headline: string;
  let detail: string = '';

  if (lang === 'zh-Hans') {
    if (workingCount === 0 && pendingCount === 0 && unreadPersonalAgents === 0) {
      headline = '目前一切空闲，没有待处理事项。';
    } else {
      const parts: string[] = [];
      if (workingCount > 0) parts.push(`${workingCount} 个任务进行中`);
      if (pendingCount > 0) parts.push(`${pendingCount} 项需要你确认`);
      if (unreadPersonalAgents > 0) parts.push(`${unreadPersonalAgents} 条未读消息`);
      headline = parts.join('，') + '。';
    }
    if (topLabel) detail = `首项待确认：${topLabel}`;
  } else if (lang === 'ja') {
    if (workingCount === 0 && pendingCount === 0 && unreadPersonalAgents === 0) {
      headline = '現在実行中のタスクや確認待ちはありません。';
    } else {
      const parts: string[] = [];
      if (workingCount > 0) parts.push(`${workingCount} 件実行中`);
      if (pendingCount > 0) parts.push(`${pendingCount} 件の確認待ち`);
      if (unreadPersonalAgents > 0) parts.push(`${unreadPersonalAgents} 件の未読`);
      headline = parts.join('、') + '。';
    }
    if (topLabel) detail = `最初の確認待ち: ${topLabel}`;
  } else {
    if (workingCount === 0 && pendingCount === 0 && unreadPersonalAgents === 0) {
      headline = 'All clear. Nothing is running or needs your attention right now.';
    } else {
      const parts: string[] = [];
      if (workingCount > 0) parts.push(`${workingCount} working`);
      if (pendingCount > 0) parts.push(`${pendingCount} need you`);
      if (unreadPersonalAgents > 0) parts.push(`${unreadPersonalAgents} unread`);
      headline = parts.join(', ') + '.';
    }
    if (topLabel) detail = `First pending: ${topLabel}`;
  }

  const spokenText = detail ? headline + ' ' + detail : headline;
  return {
    workingCount: workingCount,
    pendingCount: pendingCount,
    unreadPersonalCount: unreadPersonalAgents,
    topPending: top,
    headline: headline,
    detail: detail,
    spokenText: spokenText
  };
}

// Compact pure-TS SHA-256 so dedup keys match iOS ShortcutItem.key (first 16 bytes hex) with zero platform deps.
const SHA256_K: readonly number[] = [
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
];

function utf8Encode(input: string): Uint8Array {
  const out: number[] = [];
  for (let i = 0; i < input.length; i++) {
    let code = input.charCodeAt(i);
    if (code >= 0xd800 && code <= 0xdbff && i + 1 < input.length) {
      const next = input.charCodeAt(i + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        code = 0x10000 + ((code - 0xd800) << 10) + (next - 0xdc00);
        i++;
      }
    }
    if (code < 0x80) {
      out.push(code);
    } else if (code < 0x800) {
      out.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
    } else if (code < 0x10000) {
      out.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
    } else {
      out.push(
        0xf0 | (code >> 18),
        0x80 | ((code >> 12) & 0x3f),
        0x80 | ((code >> 6) & 0x3f),
        0x80 | (code & 0x3f)
      );
    }
  }
  return new Uint8Array(out);
}

function rotr32(x: number, n: number): number {
  return (x >>> n) | (x << (32 - n));
}

function sha256HexPrefix16(input: string): string {
  const data = utf8Encode(input);
  const bitLenHi = Math.floor((data.length * 8) / 0x100000000);
  const bitLenLo = (data.length * 8) >>> 0;
  const paddedLen = (((data.length + 9 + 63) >> 6) << 6);
  const padded = new Uint8Array(paddedLen);
  padded.set(data, 0);
  padded[data.length] = 0x80;
  padded[paddedLen - 8] = (bitLenHi >>> 24) & 0xff;
  padded[paddedLen - 7] = (bitLenHi >>> 16) & 0xff;
  padded[paddedLen - 6] = (bitLenHi >>> 8) & 0xff;
  padded[paddedLen - 5] = bitLenHi & 0xff;
  padded[paddedLen - 4] = (bitLenLo >>> 24) & 0xff;
  padded[paddedLen - 3] = (bitLenLo >>> 16) & 0xff;
  padded[paddedLen - 2] = (bitLenLo >>> 8) & 0xff;
  padded[paddedLen - 1] = bitLenLo & 0xff;

  let h0 = 0x6a09e667;
  let h1 = 0xbb67ae85;
  let h2 = 0x3c6ef372;
  let h3 = 0xa54ff53a;
  let h4 = 0x510e527f;
  let h5 = 0x9b05688c;
  let h6 = 0x1f83d9ab;
  let h7 = 0x5be0cd19;
  const w = new Uint32Array(64);

  for (let offset = 0; offset < paddedLen; offset += 64) {
    for (let i = 0; i < 16; i++) {
      const idx = offset + i * 4;
      w[i] = ((padded[idx] << 24) | (padded[idx + 1] << 16) | (padded[idx + 2] << 8) | padded[idx + 3]) >>> 0;
    }
    for (let i = 16; i < 64; i++) {
      const s0 = (rotr32(w[i - 15], 7) ^ rotr32(w[i - 15], 18) ^ (w[i - 15] >>> 3)) >>> 0;
      const s1 = (rotr32(w[i - 2], 17) ^ rotr32(w[i - 2], 19) ^ (w[i - 2] >>> 10)) >>> 0;
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
    }
    let a = h0;
    let b = h1;
    let c = h2;
    let d = h3;
    let e = h4;
    let f = h5;
    let g = h6;
    let h = h7;
    for (let i = 0; i < 64; i++) {
      const S1 = (rotr32(e, 6) ^ rotr32(e, 11) ^ rotr32(e, 25)) >>> 0;
      const ch = ((e & f) ^ ((~e) & g)) >>> 0;
      const temp1 = (h + S1 + ch + SHA256_K[i] + w[i]) >>> 0;
      const S0 = (rotr32(a, 2) ^ rotr32(a, 13) ^ rotr32(a, 22)) >>> 0;
      const maj = ((a & b) ^ (a & c) ^ (b & c)) >>> 0;
      const temp2 = (S0 + maj) >>> 0;
      h = g;
      g = f;
      f = e;
      e = (d + temp1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (temp1 + temp2) >>> 0;
    }
    h0 = (h0 + a) >>> 0;
    h1 = (h1 + b) >>> 0;
    h2 = (h2 + c) >>> 0;
    h3 = (h3 + d) >>> 0;
    h4 = (h4 + e) >>> 0;
    h5 = (h5 + f) >>> 0;
    h6 = (h6 + g) >>> 0;
    h7 = (h7 + h) >>> 0;
  }

  const words = [h0, h1, h2, h3];
  let hex = '';
  for (let i = 0; i < words.length; i++) {
    hex += words[i].toString(16).padStart(8, '0');
  }
  return hex;
}

/** Deterministic 1-minute bucket dedup key matching iOS ShortcutItem.key. */
export function buildPushDedupKey(app: string, title: string, body: string, timestampMs: number): string {
  const minute = Math.floor(timestampMs / 60000);
  return sha256HexPrefix16([app, title, body, String(minute)].join('\u0000'));
}

export function buildPushItem(
  title: string,
  body: string,
  app: string = '',
  link: string = '',
  timestampMs: number = Date.now()
): XiaoYiPushItem | undefined {
  const cleanBodyChars = Array.from((body || '').trim());
  const cleanBody = cleanBodyChars.length > XIAOYI_PUSH_BODY_MAX
    ? cleanBodyChars.slice(0, XIAOYI_PUSH_BODY_MAX).join('')
    : cleanBodyChars.join('');
  let cleanTitle = (title || '').trim();
  if (!cleanTitle && cleanBody) {
    cleanTitle = cleanBody.split('\n')[0].trim().slice(0, 120);
  }
  if (!cleanTitle && !cleanBody) return undefined;
  const cleanApp = (app || '').trim();
  const cleanLink = (link || '').trim();
  const safeLink = /^https?:\/\/[^\s]+$/i.test(cleanLink) ? cleanLink : undefined;
  const key = buildPushDedupKey(cleanApp, cleanTitle, cleanBody, timestampMs);
  return {
    key: key,
    title: cleanTitle,
    body: cleanBody,
    app: cleanApp,
    link: safeLink,
    time: new Date(timestampMs).toISOString(),
    createdAtMs: timestampMs
  };
}

/** Drop expired items (> 7 days) and enforce the 200-item FIFO cap. */
export function pruneOutboxItems(items: readonly XiaoYiPushItem[], nowMs: number = Date.now()): XiaoYiPushItem[] {
  const fresh = items.filter((item: XiaoYiPushItem): boolean =>
    !!item.key && (nowMs - item.createdAtMs) < XIAOYI_OUTBOX_TTL_MS && item.createdAtMs <= nowMs + 86400000
  );
  if (fresh.length <= XIAOYI_OUTBOX_MAX_ITEMS) return fresh;
  return fresh.slice(fresh.length - XIAOYI_OUTBOX_MAX_ITEMS);
}

/** Enqueue an item while deduplicating by `key`, pruning expired entries, and capping at 200. */
export function enqueueOutboxItem(
  existing: readonly XiaoYiPushItem[],
  item: XiaoYiPushItem,
  nowMs: number = Date.now()
): XiaoYiPushItem[] {
  const kept = pruneOutboxItems(existing, nowMs).filter((entry: XiaoYiPushItem): boolean => entry.key !== item.key);
  kept.push(item);
  if (kept.length <= XIAOYI_OUTBOX_MAX_ITEMS) return kept;
  return kept.slice(kept.length - XIAOYI_OUTBOX_MAX_ITEMS);
}

export function takeOutboxBatch(items: readonly XiaoYiPushItem[], limit: number = XIAOYI_OUTBOX_BATCH_SIZE): XiaoYiPushItem[] {
  const cap = Math.max(1, Math.min(limit, XIAOYI_OUTBOX_MAX_ITEMS));
  return items.slice(0, cap);
}

/**
 * Extract clean assistant answer text from `/api/v1/sessions/{m}/{s}/items` after `afterSeq`.
 * Skips thinking/tool/fold/user items and preserves ordered assistant text segments for streaming or final display.
 */
export function extractLatestAssistantAnswer(
  items: readonly XiaoYiTranscriptEntry[],
  afterSeq: number
): XiaoYiAssistantAnswer {
  let maxSeq = afterSeq;
  const segments: string[] = [];
  for (let i = 0; i < items.length; i++) {
    const entry = items[i];
    if (entry.seq > maxSeq) maxSeq = entry.seq;
    if (entry.seq <= afterSeq) continue;
    if (entry.kind === 'text' && entry.role === 'assistant') {
      const text = (entry.text || '').trim();
      if (text) segments.push(text);
    }
  }
  return {
    text: segments.join('\n\n'),
    maxSeq: maxSeq,
    hasReply: segments.length > 0,
    segments: segments
  };
}

/** Map internal failure codes or HTTP status categories into actionable localized string keys. */
export function formatXiaoYiError(code: string, host: string = '', target: string = ''): XiaoYiErrorDescriptor {
  const normalized = (code || '').trim();
  const known: readonly string[] = [
    'xiaoyi_not_paired',
    'xiaoyi_auth_expired',
    'xiaoyi_already_handled',
    'xiaoyi_ssh_open_app',
    'xiaoyi_unlock_required',
    'xiaoyi_offline',
    'xiaoyi_machine_offline',
    'xiaoyi_machine_not_found',
    'xiaoyi_no_online_machine',
    'xiaoyi_no_live_session',
    'xiaoyi_hub_update_required',
    'xiaoyi_need_app_for_new_session',
    'xiaoyi_nothing_pending',
    'xiaoyi_needs_screen',
    'xiaoyi_nothing_to_send',
    'xiaoyi_water_not_enabled',
    'xiaoyi_recording_module_off',
    'xiaoyi_timeout',
    'xiaoyi_rate_limited'
  ];
  for (let i = 0; i < known.length; i++) {
    if (known[i] === normalized) {
      return { key: normalized, host: host, target: target };
    }
  }
  if (normalized === 'offline' || normalized === 'network_error') {
    return { key: 'xiaoyi_offline', host: host, target: target };
  }
  if (normalized === 'timeout') {
    return { key: 'xiaoyi_timeout', host: host, target: target };
  }
  return { key: 'xiaoyi_failed', host: host, target: target };
}
