/**
 * Fleet Live View state & HarmonyOS LiveViewKit projection matching iOS FleetState
 * (apple/Shared/FleetState.swift) and LiveActivityController (docs/apple/live-activity.md).
 * Pure TypeScript so every state transition, privacy masking rule, and payload mapping
 * is verified in host unit tests.
 */
export type FleetPhase = 'needsYou' | 'working' | 'done' | 'none';
export type FleetItemKind = 'permission' | 'plan' | 'question' | 'screen' | 'working' | 'unread';
export type FleetLifecycleStep = 'start' | 'update' | 'endSoon' | 'nothing';

export interface FleetItemProgress {
  done: number;
  started: number;
}

export interface FleetItem {
  id: string;
  title: string;
  place: string;
  kind: FleetItemKind;
  detail: string;
  interaction: string;
  since: number;
  background: boolean;
  progress: FleetItemProgress | undefined;
}

export interface FleetStats {
  tpm: number;
  tokens: number;
  requests: number;
}

export interface FleetLiveState {
  phase: FleetPhase;
  needsYou: number;
  working: number;
  unread: number;
  items: FleetItem[];
  workingSince: number;
  assistant: boolean;
  details: boolean;
  stats: FleetStats | undefined;
}

export interface FleetSessionSummary {
  machine: string;
  session: string;
  title: string;
  place: string;
  state: string;
  detail: string;
  updatedAtSec: number;
  startedAtSec: number;
  muted: boolean;
  backgroundWorking: boolean;
  backgroundDetail: string;
  progressDone: number;
  progressStarted: number;
  interactionKind: string;
  interactionId: string;
}

export interface FleetStrings {
  appTitle: string;
  needsYouCount: string;
  workingCount: string;
  unreadCount: string;
  allCaughtUp: string;
  assistantReplying: string;
  backgroundTag: string;
  anonPermission: string;
  anonPlan: string;
  anonQuestion: string;
  anonScreen: string;
  anonWorking: string;
  anonUnread: string;
  capsuleNeeds: string;
  capsuleWorking: string;
  capsuleUnread: string;
  capsuleCaughtUp: string;
  moreSuffix: string;
}

export interface LiveViewRichSegment {
  text: string;
  textColor?: string;
}

export interface FleetLiveViewSpec {
  id: number;
  event: string;
  title: string;
  content: LiveViewRichSegment[];
  capsuleKind: 'text' | 'timer' | 'progress';
  capsuleTitle: string;
  capsuleContent: string;
  capsuleBackgroundColor: string;
  timerElapsedMs: number;
  timerPaused: boolean;
  extensionText: string;
  layoutKind: 'default' | 'progress' | 'pickup';
  progressPercent: number;
  progressColor: string;
  pickupTitle: string;
  pickupContent: string;
  targetMachine: string;
  targetSession: string;
  keepTimeSec: number;
  isMute: boolean;
}

export const FLEET_LIVE_VIEW_ID: number = 7001;
export const FLEET_STALE_RENEW_MS: number = 10 * 60 * 1000;
export const FLEET_END_DELAY_MS: number = 60 * 1000;

const VALID_PHASES: string[] = ['needsYou', 'working', 'done', 'none'];
const VALID_KINDS: string[] = ['permission', 'plan', 'question', 'screen', 'working', 'unread'];

function readString(obj: Record<string, Object>, key: string, fallback: string = ''): string {
  const value = obj[key];
  return typeof value === 'string' ? value : fallback;
}

function readNumber(obj: Record<string, Object>, key: string, fallback: number = 0): number {
  const value = obj[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback;
}

function readBool(obj: Record<string, Object>, key: string, fallback: boolean = false): boolean {
  const value = obj[key];
  return typeof value === 'boolean' ? value : fallback;
}

function readObject(obj: Record<string, Object>, key: string): Record<string, Object> | undefined {
  const value = obj[key];
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, Object> : undefined;
}

export function emptyFleetState(details: boolean = false): FleetLiveState {
  return {
    phase: 'none',
    needsYou: 0,
    working: 0,
    unread: 0,
    items: [],
    workingSince: 0,
    assistant: false,
    details: details,
    stats: undefined
  };
}

export function isFleetItemNeedsYou(item: FleetItem): boolean {
  return item.kind !== 'working' && item.kind !== 'unread';
}

export function fleetItemProgressPercent(item: FleetItem): number {
  if (!item.progress || item.progress.started <= 0) return -1;
  const clamped = Math.max(0, Math.min(item.progress.done, item.progress.started));
  return Math.round(clamped * 100 / item.progress.started);
}

/** Lenient decoder matching Swift `FleetState.init(from:)` and Go `hub.FleetState`. */
export function decodeFleetState(raw: Record<string, Object>, lockDetails: boolean): FleetLiveState {
  const source = readObject(raw, 'fleet') || raw;
  const rawPhase = readString(source, 'phase', '');
  const needsYou = Math.max(0, Math.floor(readNumber(source, 'needsYou', 0)));
  const working = Math.max(0, Math.floor(readNumber(source, 'working', 0)));
  const unread = Math.max(0, Math.floor(readNumber(source, 'unread', 0)));
  let phase: FleetPhase = VALID_PHASES.includes(rawPhase) ? rawPhase as FleetPhase : 'none';
  if (phase === 'none') {
    if (needsYou > 0) phase = 'needsYou';
    else if (working > 0) phase = 'working';
    else if (unread > 0) phase = 'done';
  }
  const rawItems = Array.isArray(source['items']) ? source['items'] as Object[] : [];
  const items: FleetItem[] = [];
  for (const entry of rawItems) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) continue;
    const item = entry as Record<string, Object>;
    const id = readString(item, 'id');
    if (!id) continue;
    const rawKind = readString(item, 'kind', 'screen');
    const kind: FleetItemKind = VALID_KINDS.includes(rawKind) ? rawKind as FleetItemKind : 'screen';
    const progressRaw = readObject(item, 'progress');
    const progress: FleetItemProgress | undefined = progressRaw && readNumber(progressRaw, 'started', 0) > 0
      ? { done: Math.max(0, Math.floor(readNumber(progressRaw, 'done', 0))), started: Math.floor(readNumber(progressRaw, 'started', 0)) }
      : undefined;
    items.push({
      id: id,
      title: readString(item, 'title'),
      place: readString(item, 'place'),
      kind: kind,
      detail: readString(item, 'detail'),
      interaction: lockDetails ? readString(item, 'interaction') : '',
      since: Math.max(0, readNumber(item, 'since', 0)),
      background: readBool(item, 'background', false),
      progress: progress
    });
  }
  const statsRaw = readObject(source, 'stats');
  const stats: FleetStats | undefined = statsRaw ? {
    tpm: Math.max(0, Math.floor(readNumber(statsRaw, 'tpm', 0))),
    tokens: Math.max(0, Math.floor(readNumber(statsRaw, 'tokens', 0))),
    requests: Math.max(0, Math.floor(readNumber(statsRaw, 'requests', 0)))
  } : undefined;
  return {
    phase: phase,
    needsYou: needsYou,
    working: working,
    unread: unread,
    items: items.slice(0, 3),
    workingSince: Math.max(0, readNumber(source, 'workingSince', 0)),
    assistant: readBool(source, 'assistant', false),
    details: lockDetails,
    stats: stats
  };
}

/**
 * Local fallback derivation matching `hub/liveactivity.go buildFleet`:
 * excludes muted / idle / offline sessions, prioritizes Needs You (oldest first),
 * then Working (longest-running first), then Unread (newest first).
 */
export function deriveFleetFromSessions(sessions: FleetSessionSummary[], lockDetails: boolean, assistantReplying: boolean = false): FleetLiveState {
  const needs: FleetItem[] = [];
  const working: FleetItem[] = [];
  const unread: FleetItem[] = [];
  let earliestWorkingSince = 0;

  for (const s of sessions) {
    if (!s.machine || !s.session) continue;
    const isNeeds = s.state === 'needs_you';
    if (s.muted && !isNeeds) continue;
    const id = s.machine + '/' + s.session;
    if (isNeeds) {
      const kind: FleetItemKind = s.interactionKind === 'permission' || s.interactionKind === 'plan' || s.interactionKind === 'question'
        ? s.interactionKind as FleetItemKind : 'screen';
      needs.push({
        id: id,
        title: s.title,
        place: s.place,
        kind: kind,
        detail: s.detail,
        interaction: lockDetails ? s.interactionId : '',
        since: s.updatedAtSec,
        background: false,
        progress: undefined
      });
    } else if (s.state === 'working') {
      const since = s.startedAtSec > 0 ? s.startedAtSec : s.updatedAtSec;
      if (since > 0 && (earliestWorkingSince === 0 || since < earliestWorkingSince)) earliestWorkingSince = since;
      const progress: FleetItemProgress | undefined = s.backgroundWorking && s.progressStarted > 0
        ? { done: Math.min(Math.max(0, s.progressDone), s.progressStarted), started: s.progressStarted }
        : undefined;
      working.push({
        id: id,
        title: s.title,
        place: s.place,
        kind: 'working',
        detail: s.backgroundWorking ? s.backgroundDetail : s.detail,
        interaction: '',
        since: since,
        background: s.backgroundWorking,
        progress: progress
      });
    } else if (s.state === 'unread') {
      unread.push({
        id: id,
        title: s.title,
        place: s.place,
        kind: 'unread',
        detail: s.detail,
        interaction: '',
        since: s.updatedAtSec,
        background: false,
        progress: undefined
      });
    }
  }

  needs.sort((a, b) => (a.since || 0) - (b.since || 0));
  working.sort((a, b) => (a.since || 0) - (b.since || 0));
  unread.sort((a, b) => (b.since || 0) - (a.since || 0));

  const items = needs.concat(working, unread).slice(0, 3);
  const phase: FleetPhase = needs.length > 0 ? 'needsYou' : working.length > 0 ? 'working' : unread.length > 0 ? 'done' : 'none';
  return {
    phase: phase,
    needsYou: needs.length,
    working: working.length,
    unread: unread.length,
    items: items,
    workingSince: working.length > 0 ? earliestWorkingSince : 0,
    assistant: assistantReplying,
    details: lockDetails,
    stats: undefined
  };
}

export function fleetShowsNothing(state: FleetLiveState): boolean {
  return state.phase === 'none' && !state.assistant;
}

/** Matches Swift `LiveActivityController.step(_:running:active:)`. */
export function decideFleetStep(state: FleetLiveState, running: boolean, foreground: boolean): FleetLifecycleStep {
  if (fleetShowsNothing(state)) return running ? 'endSoon' : 'nothing';
  if (running) return 'update';
  return foreground && state.phase !== 'none' && state.phase !== 'done' ? 'start' : 'nothing';
}

/** Matches Swift `LiveActivityController.clearsNow(_:to:)` (#8636): last Needs You clears immediately. */
export function shouldClearFleetNow(shown: FleetLiveState | undefined, next: FleetLiveState): boolean {
  if (!shown || !fleetShowsNothing(next)) return false;
  return shown.needsYou > 0 || shown.items.some(isFleetItemNeedsYou);
}

export function fleetFingerprint(state: FleetLiveState): string {
  return JSON.stringify({
    phase: state.phase,
    needsYou: state.needsYou,
    working: state.working,
    unread: state.unread,
    workingSince: Math.floor(state.workingSince),
    assistant: state.assistant,
    details: state.details,
    items: state.items.map((item) => ({
      id: item.id,
      title: state.details ? item.title : '',
      place: state.details ? item.place : '',
      kind: item.kind,
      detail: state.details ? item.detail : '',
      bg: item.background,
      p: item.progress ? `${item.progress.done}/${item.progress.started}` : ''
    }))
  });
}

/** Matches Swift `LiveActivityController.worthUpdating`: skips redundant redraws until stale renew window. */
export function isFleetWorthUpdating(
  shown: FleetLiveState | undefined,
  lastUpdatedMs: number,
  next: FleetLiveState,
  nowMs: number
): boolean {
  if (!shown) return true;
  if (fleetFingerprint(shown) !== fleetFingerprint(next)) return true;
  return nowMs - lastUpdatedMs >= FLEET_STALE_RENEW_MS;
}

export function fleetHiddenCount(state: FleetLiveState, showing: number = 2): number {
  const total = state.needsYou + state.working + state.unread;
  return Math.max(0, total - Math.min(showing, state.items.length));
}

export function parseFleetTarget(item: FleetItem | undefined): { machine: string; session: string } {
  if (!item || !item.id) return { machine: '', session: '' };
  const slash = item.id.indexOf('/');
  if (slash <= 0 || slash >= item.id.length - 1) return { machine: '', session: '' };
  return { machine: item.id.slice(0, slash), session: item.id.slice(slash + 1) };
}

function formatCount(template: string, count: number): string {
  return template.replace('{count}', String(count));
}

function anonymousLine(item: FleetItem, strings: FleetStrings): string {
  if (item.kind === 'permission') return strings.anonPermission;
  if (item.kind === 'plan') return strings.anonPlan;
  if (item.kind === 'question') return strings.anonQuestion;
  if (item.kind === 'screen') return strings.anonScreen;
  if (item.kind === 'working') return strings.anonWorking;
  return strings.anonUnread;
}

function detailedLine(item: FleetItem, strings: FleetStrings): string {
  const prefix = item.title || item.place || strings.appTitle;
  const tail = item.background && item.detail ? strings.backgroundTag + ' · ' + item.detail : item.detail;
  return tail ? prefix + ' · ' + tail : prefix;
}

/**
 * Projects `FleetLiveState` into a HarmonyOS LiveViewKit card + status bar capsule specification.
 * Colors follow Persona semantic tokens (`StatusPalette` / `Colors`):
 * - needsYou: #FFD93A3A (urgent red, high contrast white ink)
 * - working:  #FFB35400 (working amber)
 * - done:     #FF1D7A4C (unread green)
 * - none:     #FF0071E3 (Persona blue)
 */
export function buildFleetLiveViewSpec(state: FleetLiveState, strings: FleetStrings, nowMs: number = Date.now()): FleetLiveViewSpec {
  const totalActive = state.needsYou + state.working + state.unread;
  const first = state.items[0];
  const second = state.items[1];
  const hidden = fleetHiddenCount(state, 2);

  let title = strings.allCaughtUp;
  let capsuleBg = '#FF0071E3';
  let capsuleTitle = strings.capsuleCaughtUp;
  let capsuleContent = strings.appTitle;
  let extensionText = '';

  if (state.phase === 'needsYou') {
    title = formatCount(strings.needsYouCount, state.needsYou);
    capsuleBg = '#FFD93A3A';
    // Compact left capsule fits cleanly beside HarmonyOS pill cutouts (Mate 90 Pro / Mate X7).
    capsuleTitle = state.working > 0 ? '!' + String(state.needsYou) : String(state.needsYou);
    capsuleContent = state.working > 0 ? formatCount(strings.capsuleWorking, state.working) : formatCount(strings.capsuleNeeds, state.needsYou);
    extensionText = String(state.needsYou);
  } else if (state.phase === 'working') {
    title = formatCount(strings.workingCount, state.working);
    capsuleBg = '#FFB35400';
    capsuleTitle = String(state.working);
    capsuleContent = state.unread > 0 ? formatCount(strings.capsuleUnread, state.unread) : formatCount(strings.capsuleWorking, state.working);
    extensionText = String(state.working);
  } else if (state.phase === 'done') {
    title = formatCount(strings.unreadCount, state.unread);
    capsuleBg = '#FF1D7A4C';
    capsuleTitle = String(state.unread);
    capsuleContent = formatCount(strings.capsuleUnread, state.unread);
    extensionText = String(state.unread);
  } else if (state.assistant) {
    title = strings.assistantReplying;
    capsuleBg = '#FF5B4FD6';
    capsuleTitle = strings.appTitle;
    capsuleContent = strings.assistantReplying;
  }

  const summaryParts: string[] = [];
  if (state.needsYou > 0) summaryParts.push(formatCount(strings.needsYouCount, state.needsYou));
  if (state.working > 0) summaryParts.push(formatCount(strings.workingCount, state.working));
  if (state.unread > 0) summaryParts.push(formatCount(strings.unreadCount, state.unread));

  const content: LiveViewRichSegment[] = [];
  if (first) {
    const primaryLine = state.details ? detailedLine(first, strings) : anonymousLine(first, strings);
    const lines: string[] = [primaryLine.slice(0, 120)];
    if (state.details && second) {
      const extra = hidden > 0 ? ' ' + strings.moreSuffix.replace('{count}', String(hidden)) : '';
      lines.push((detailedLine(second, strings) + extra).slice(0, 120));
    } else if (hidden > 0) {
      lines[0] = (lines[0] + ' ' + strings.moreSuffix.replace('{count}', String(hidden))).slice(0, 120);
    }
    content.push({ text: lines.join('\n') });
  } else if (summaryParts.length > 0) {
    content.push({ text: summaryParts.join(' · ').slice(0, 120) });
  } else {
    content.push({ text: (state.assistant ? strings.assistantReplying : strings.allCaughtUp).slice(0, 120) });
  }

  const progressPct = first ? fleetItemProgressPercent(first) : -1;
  const elapsedMs = state.workingSince > 0 ? Math.max(0, Math.floor(nowMs - state.workingSince * 1000)) : 0;

  let capsuleKind: 'text' | 'timer' | 'progress' = 'text';
  let layoutKind: 'default' | 'progress' | 'pickup' = 'default';
  let pickupTitle = '';
  let pickupContent = '';

  if (progressPct >= 0 && state.phase === 'working') {
    capsuleKind = 'progress';
    layoutKind = 'progress';
  } else if (state.phase === 'working' && state.workingSince > 0) {
    capsuleKind = 'timer';
    layoutKind = 'pickup';
    pickupTitle = state.details && first?.title ? first.title.slice(0, 64) : strings.workingCount.replace('{count}', String(state.working));
    pickupContent = '${placeholder.timer}';
  } else if (state.phase === 'needsYou' && first) {
    layoutKind = 'pickup';
    pickupTitle = state.details && first.title ? first.title.slice(0, 64) : title;
    pickupContent = (state.details && first.detail ? first.detail : anonymousLine(first, strings)).slice(0, 96);
  }

  // Deep-link directly when there is a single active agent OR a single urgent Needs-You agent
  // waiting for approval; when multiple agents need attention, return to the full triage list.
  const canDirectTarget = totalActive === 1 || (state.phase === 'needsYou' && state.needsYou === 1);
  const singleTarget = canDirectTarget && first ? parseFleetTarget(first) : { machine: '', session: '' };

  return {
    id: FLEET_LIVE_VIEW_ID,
    event: layoutKind === 'progress' ? 'PROGRESS' : 'TIMER',
    title: title.slice(0, 120),
    content: content,
    capsuleKind: capsuleKind,
    capsuleTitle: capsuleTitle.slice(0, 32),
    capsuleContent: capsuleContent.slice(0, 48),
    capsuleBackgroundColor: capsuleBg,
    timerElapsedMs: elapsedMs,
    timerPaused: state.phase !== 'working',
    extensionText: extensionText,
    layoutKind: layoutKind,
    progressPercent: progressPct >= 0 ? progressPct : 0,
    progressColor: capsuleBg,
    pickupTitle: pickupTitle,
    pickupContent: pickupContent,
    targetMachine: singleTarget.machine,
    targetSession: singleTarget.session,
    keepTimeSec: state.phase === 'none' ? 0 : 15,
    isMute: state.phase !== 'needsYou'
  };
}

/** Built-in sample states matching iOS `LiveActivityController.showSample` for Settings preview & emulator verification. */
export function sampleFleetState(phase: string, lockDetails: boolean, nowMs: number = Date.now()): FleetLiveState {
  const nowSec = Math.floor(nowMs / 1000);
  const ask: FleetItem = {
    id: 'demo-mac/demo-agent-review',
    title: 'Review release checks',
    place: 'Mac Studio · ReleaseDesk',
    kind: 'permission',
    detail: 'Bash: hvigorw assembleHap',
    interaction: lockDetails ? 'demo-plan-review' : '',
    since: nowSec - 90,
    background: false,
    progress: undefined
  };
  const work: FleetItem = {
    id: 'demo-mac/demo-agent-changelog',
    title: 'Prepare the changelog',
    place: 'Mac Studio · ReleaseDesk',
    kind: 'working',
    detail: 'Generating release summary',
    interaction: '',
    since: nowSec - 420,
    background: false,
    progress: undefined
  };
  const bgAudit: FleetItem = {
    id: 'demo-mac/demo-agent-audit',
    title: 'Harmony UI parity sweep',
    place: 'Mac Studio · persona',
    kind: 'working',
    detail: 'parity-audit · Verify · 18/24',
    interaction: '',
    since: nowSec - 610,
    background: true,
    progress: { done: 18, started: 24 }
  };
  const unreadItem: FleetItem = {
    id: 'demo-mac/demo-agent-notes',
    title: 'Organize project notes',
    place: 'Mac Studio · Notes',
    kind: 'unread',
    detail: 'All unit tests passed',
    interaction: '',
    since: nowSec - 45,
    background: false,
    progress: undefined
  };
  if (phase === 'working') {
    return { phase: 'working', needsYou: 0, working: 2, unread: 1, items: [work, unreadItem], workingSince: nowSec - 420, assistant: false, details: lockDetails, stats: undefined };
  }
  if (phase === 'background') {
    return { phase: 'working', needsYou: 0, working: 2, unread: 1, items: [bgAudit, work], workingSince: nowSec - 610, assistant: false, details: lockDetails, stats: undefined };
  }
  if (phase === 'done') {
    return { phase: 'done', needsYou: 0, working: 0, unread: 1, items: [unreadItem], workingSince: 0, assistant: false, details: lockDetails, stats: undefined };
  }
  if (phase === 'none') {
    return emptyFleetState(lockDetails);
  }
  return { phase: 'needsYou', needsYou: 1, working: 2, unread: 1, items: [ask, work, unreadItem], workingSince: nowSec - 420, assistant: false, details: lockDetails, stats: undefined };
}
