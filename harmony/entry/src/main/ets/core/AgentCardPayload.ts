/** Pure payload parser and inline confirmation state machine for AgentUIExtensionAbility cards. */

import { parseXiaoYiUri } from './XiaoYiRoutes';

export type AgentCardVariant = 'status' | 'approval' | 'answer' | 'receipt';

export interface AgentCardSessionRow {
  title: string;
  state: string;
  machine: string;
}

export interface AgentCardModel {
  variant: AgentCardVariant;
  title: string;
  body: string;
  deepLinkUri: string;
  profileId: string;
  managerId: string;
  workingCount: number;
  pendingCount: number;
  unreadCount: number;
  sessions: AgentCardSessionRow[];
  decisionId: string;
  decisionKind: string;
  canApproveInline: boolean;
}

export type InlineConfirmStep = 'idle' | 'confirming' | 'submitting' | 'approved' | 'failed';

const MAX_TITLE_CHARS: number = 140;
const MAX_BODY_CHARS: number = 1200;
const MAX_SESSIONS: number = 3;

function clip(raw: string, maxChars: number): string {
  const clean = (raw || '').replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, '').trim();
  const chars = Array.from(clean);
  return chars.length <= maxChars ? clean : chars.slice(0, maxChars - 1).join('') + '…';
}

function readStr(map: Record<string, Object>, key: string): string {
  const val = map[key];
  return typeof val === 'string' ? val : '';
}

function readNum(map: Record<string, Object>, key: string): number {
  const val = map[key];
  return typeof val === 'number' && Number.isFinite(val) && val >= 0 ? Math.floor(val) : 0;
}

function readBool(map: Record<string, Object>, key: string): boolean {
  const val = map[key];
  return typeof val === 'boolean' ? val : val === 'true' || val === '1';
}

function normalizeVariant(raw: string): AgentCardVariant {
  const lowered = raw.trim().toLowerCase();
  if (lowered === 'status' || lowered === 'approval' || lowered === 'answer' || lowered === 'receipt') {
    return lowered;
  }
  return 'answer';
}

function safeDeepLink(raw: string, variant: AgentCardVariant): string {
  const fallback = variant === 'status' || variant === 'approval'
    ? 'persona://open?dest=agents'
    : 'persona://open?dest=assistant';
  if (!raw) return fallback;
  return parseXiaoYiUri(raw) ? raw.trim() : fallback;
}

function parseSessions(raw: Object | undefined): AgentCardSessionRow[] {
  if (!Array.isArray(raw)) return [];
  const rows: AgentCardSessionRow[] = [];
  const items = raw as Object[];
  for (let i = 0; i < items.length && rows.length < MAX_SESSIONS; i++) {
    const item = items[i];
    if (!item || typeof item !== 'object' || Array.isArray(item)) continue;
    const rec = item as Record<string, Object>;
    const title = clip(readStr(rec, 'title'), 80);
    if (!title) continue;
    rows.push({
      title: title,
      state: clip(readStr(rec, 'state') || 'working', 24),
      machine: clip(readStr(rec, 'machine'), 48)
    });
  }
  return rows;
}

/** Decode untrusted Want/LocalStorage parameters into a bounded AgentCardModel. */
export function parseAgentCardPayload(params?: Record<string, Object>): AgentCardModel {
  const map: Record<string, Object> = params || {};
  const variant = normalizeVariant(readStr(map, 'cardType') || readStr(map, 'variant'));
  const decisionKind = clip(readStr(map, 'decisionKind'), 32);
  const canApproveFlag = readBool(map, 'canApproveInline');
  const decisionId = clip(readStr(map, 'decisionId'), 128);
  // Only permission and non-bypass plan decisions with an explicit ID may offer inline confirmation.
  const canApproveInline = variant === 'approval' &&
    !!decisionId &&
    canApproveFlag &&
    (decisionKind === 'permission' || decisionKind === 'plan');

  const deepLinkUri = safeDeepLink(readStr(map, 'deepLinkUri'), variant);
  const parsedRoute = parseXiaoYiUri(deepLinkUri);
  const profileId = clip(readStr(map, 'profileId') || readStr(map, 'profile') || (parsedRoute ? parsedRoute.profile : ''), 128);
  const managerId = clip(readStr(map, 'managerId') || readStr(map, 'manager') || (parsedRoute ? parsedRoute.manager : ''), 128);

  return {
    variant: variant,
    title: clip(readStr(map, 'title') || 'Persona', MAX_TITLE_CHARS),
    body: clip(readStr(map, 'summary') || readStr(map, 'body') || readStr(map, 'text'), MAX_BODY_CHARS),
    deepLinkUri: deepLinkUri,
    profileId: profileId,
    managerId: managerId,
    workingCount: readNum(map, 'workingCount'),
    pendingCount: readNum(map, 'pendingCount'),
    unreadCount: readNum(map, 'unreadCount'),
    sessions: parseSessions(map['sessions']),
    decisionId: decisionId,
    decisionKind: decisionKind,
    canApproveInline: canApproveInline
  };
}

/** Build the safe resolution payload for an inline card confirmation (NEVER bypassPermissions). */
export function buildInlineCardResolution(decisionKind: string): Record<string, Object> | undefined {
  if (decisionKind === 'permission') {
    const body: Record<string, Object> = {};
    body['allow'] = true;
    return body;
  }
  if (decisionKind === 'plan') {
    const body: Record<string, Object> = {};
    body['approve'] = true;
    body['mode'] = 'acceptEdits';
    return body;
  }
  return undefined;
}

/** Pure inline two-step confirmation reducer (avoids sub-windows forbidden in AgentUIExtensionAbility). */
export function nextInlineConfirmStep(current: InlineConfirmStep, event: 'request' | 'cancel' | 'confirm' | 'ok' | 'error'): InlineConfirmStep {
  if (event === 'cancel') return current === 'confirming' ? 'idle' : current;
  if (event === 'request') return current === 'idle' || current === 'failed' ? 'confirming' : current;
  if (event === 'confirm') return current === 'confirming' ? 'submitting' : current;
  if (event === 'ok') return current === 'submitting' ? 'approved' : current;
  if (event === 'error') return current === 'submitting' ? 'failed' : current;
  return current;
}
