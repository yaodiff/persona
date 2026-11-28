/** Pure deep-link route parsing and building for XiaoYi, Intents Kit, A2A cards, and Share handoff. */

export type XiaoYiDestination =
  | 'assistant'
  | 'agents'
  | 'session'
  | 'todos'
  | 'knowledge'
  | 'health'
  | 'inbox'
  | 'recording'
  | 'dictate'
  | 'settings_xiaoyi'
  | 'share';

export interface XiaoYiRoute {
  dest: XiaoYiDestination;
  profile: string;
  manager: string;
  machine: string;
  session: string;
  autoStart: boolean;
  text: string;
  title: string;
  url: string;
}

const MAX_URI_LENGTH: number = 16384;
const MAX_ID_LENGTH: number = 256;
const MAX_TEXT_LENGTH: number = 8000;
const MAX_TITLE_LENGTH: number = 256;
const MAX_URL_LENGTH: number = 2048;

const ALLOWED_DESTINATIONS: readonly XiaoYiDestination[] = [
  'assistant',
  'agents',
  'session',
  'todos',
  'knowledge',
  'health',
  'inbox',
  'recording',
  'dictate',
  'settings_xiaoyi',
  'share'
];

function hasControlChars(value: string): boolean {
  return /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(value);
}

function hasAnyControlChars(value: string): boolean {
  return /[\u0000-\u001f\u007f]/.test(value);
}

function normalizeDestination(raw: string): XiaoYiDestination | undefined {
  const lowered = raw.trim().toLowerCase();
  if (lowered === 'todo') return 'todos';
  if (lowered === 'recordings' || lowered === 'meeting') return 'recording';
  if (lowered === 'xiaoyi' || lowered === 'phone_xiaoyi') return 'settings_xiaoyi';
  for (let i = 0; i < ALLOWED_DESTINATIONS.length; i++) {
    if (ALLOWED_DESTINATIONS[i] === lowered) return ALLOWED_DESTINATIONS[i];
  }
  return undefined;
}

function sanitizeId(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed || trimmed.length > MAX_ID_LENGTH || hasAnyControlChars(trimmed)) return '';
  return trimmed;
}

function sanitizeText(raw: string, maxLen: number): string {
  if (!raw) return '';
  const cleaned = raw.replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, '').trim();
  if (!cleaned) return '';
  const chars = Array.from(cleaned);
  return chars.length > maxLen ? chars.slice(0, maxLen).join('') : cleaned;
}

function sanitizeHttpUrl(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed || trimmed.length > MAX_URL_LENGTH || hasAnyControlChars(trimmed)) return '';
  if (!/^https?:\/\/[^\s/?#]+/i.test(trimmed)) return '';
  return trimmed;
}

function decodeQueryValue(raw: string): string | undefined {
  try {
    return decodeURIComponent(raw.replace(/\+/g, '%20'));
  } catch (_) {
    return undefined;
  }
}

function parseQueryParams(query: string): Map<string, string> | undefined {
  const params = new Map<string, string>();
  if (!query) return params;
  const pairs = query.split('&');
  for (let i = 0; i < pairs.length; i++) {
    const pair = pairs[i];
    if (!pair) continue;
    const eq = pair.indexOf('=');
    const rawKey = eq >= 0 ? pair.slice(0, eq) : pair;
    const rawValue = eq >= 0 ? pair.slice(eq + 1) : '';
    const key = decodeQueryValue(rawKey);
    const value = decodeQueryValue(rawValue);
    if (key === undefined || value === undefined) return undefined;
    if (!params.has(key)) params.set(key, value);
  }
  return params;
}

function boolParam(value: string): boolean {
  const lowered = value.trim().toLowerCase();
  return lowered === '1' || lowered === 'true' || lowered === 'yes';
}

export function createXiaoYiRoute(dest: XiaoYiDestination): XiaoYiRoute {
  return {
    dest: dest,
    profile: '',
    manager: '',
    machine: '',
    session: '',
    autoStart: false,
    text: '',
    title: '',
    url: ''
  };
}

/** Parse a `persona://open?...` URI. Pairing and password-login URIs return undefined. */
export function parseXiaoYiUri(uri: string): XiaoYiRoute | undefined {
  if (!uri || uri.length > MAX_URI_LENGTH || hasAnyControlChars(uri)) return undefined;
  const match = /^persona:\/\/([^/?#]+)(\/[^?#]*)?(?:\?([^#]*))?(?:#.*)?$/i.exec(uri.trim());
  if (!match) return undefined;
  const host = match[1].toLowerCase();
  if (host !== 'open') return undefined;

  const params = parseQueryParams(match[3] || '');
  if (!params) return undefined;

  const pathDest = (match[2] || '').replace(/^\/+|\/+$/g, '');
  const rawDest = params.get('dest') || pathDest || 'assistant';
  const dest = normalizeDestination(rawDest);
  if (!dest) return undefined;

  const rawProfile = params.get('profile') || '';
  const rawManager = params.get('manager') || '';
  const rawMachine = params.get('machine') || '';
  const rawSession = params.get('session') || '';
  if ((rawProfile && !sanitizeId(rawProfile)) ||
    (rawManager && !sanitizeId(rawManager)) ||
    (rawMachine && !sanitizeId(rawMachine)) ||
    (rawSession && !sanitizeId(rawSession))) {
    return undefined;
  }

  const route = createXiaoYiRoute(dest);
  route.profile = sanitizeId(rawProfile);
  route.manager = sanitizeId(rawManager);
  route.machine = sanitizeId(rawMachine);
  route.session = sanitizeId(rawSession);
  route.autoStart = boolParam(params.get('autoStart') || '');
  route.text = sanitizeText(params.get('text') || '', MAX_TEXT_LENGTH);
  route.title = sanitizeText(params.get('title') || '', MAX_TITLE_LENGTH);
  route.url = sanitizeHttpUrl(params.get('url') || '');

  if (route.dest === 'session' && (!route.machine || !route.session)) return undefined;
  return route;
}

function readParamString(parameters: Record<string, Object> | undefined, key: string): string {
  if (!parameters) return '';
  const value = parameters[key];
  return typeof value === 'string' ? value : '';
}

function readParamBool(parameters: Record<string, Object> | undefined, key: string): boolean {
  if (!parameters) return false;
  const value = parameters[key];
  if (typeof value === 'boolean') return value;
  if (typeof value === 'string') return boolParam(value);
  return false;
}

/** Parse a XiaoYi route from Want URI or explicit Want parameters. */
export function parseXiaoYiWant(uri: string, parameters?: Record<string, Object>): XiaoYiRoute | undefined {
  if (uri) {
    const parsed = parseXiaoYiUri(uri);
    if (parsed) return parsed;
  }
  const paramUri = readParamString(parameters, 'personaXiaoYiUri');
  if (paramUri) {
    const parsed = parseXiaoYiUri(paramUri);
    if (parsed) return parsed;
  }
  const rawDest = readParamString(parameters, 'personaDest');
  if (!rawDest) return undefined;
  const dest = normalizeDestination(rawDest);
  if (!dest) return undefined;

  const route = createXiaoYiRoute(dest);
  route.profile = sanitizeId(readParamString(parameters, 'profile'));
  route.manager = sanitizeId(readParamString(parameters, 'manager'));
  route.machine = sanitizeId(readParamString(parameters, 'machine'));
  route.session = sanitizeId(readParamString(parameters, 'session'));
  route.autoStart = readParamBool(parameters, 'autoStart');
  route.text = sanitizeText(readParamString(parameters, 'text'), MAX_TEXT_LENGTH);
  route.title = sanitizeText(readParamString(parameters, 'title'), MAX_TITLE_LENGTH);
  route.url = sanitizeHttpUrl(readParamString(parameters, 'url'));
  if (route.dest === 'session' && (!route.machine || !route.session)) return undefined;
  return route;
}

/** Build a validated `persona://open?...` URI suitable for Intents, A2A artifacts, and AgentUI cards. */
export function buildXiaoYiRouteUri(route: XiaoYiRoute): string {
  const parts: string[] = ['dest=' + encodeURIComponent(route.dest)];
  if (route.profile) parts.push('profile=' + encodeURIComponent(sanitizeId(route.profile)));
  if (route.manager) parts.push('manager=' + encodeURIComponent(sanitizeId(route.manager)));
  if (route.machine) parts.push('machine=' + encodeURIComponent(sanitizeId(route.machine)));
  if (route.session) parts.push('session=' + encodeURIComponent(sanitizeId(route.session)));
  if (route.autoStart) parts.push('autoStart=1');
  if (route.title) parts.push('title=' + encodeURIComponent(sanitizeText(route.title, MAX_TITLE_LENGTH)));
  if (route.url) {
    const safeUrl = sanitizeHttpUrl(route.url);
    if (safeUrl) parts.push('url=' + encodeURIComponent(safeUrl));
  }
  if (route.text) {
    const safeText = sanitizeText(route.text, MAX_TEXT_LENGTH);
    if (safeText && !hasControlChars(safeText)) parts.push('text=' + encodeURIComponent(safeText));
  }
  const built = 'persona://open?' + parts.join('&');
  return built.length <= MAX_URI_LENGTH ? built : 'persona://open?dest=' + encodeURIComponent(route.dest);
}

/** Format incoming shared content into a readable unsent draft without auto-sending. */
export function formatSharedDraftText(title: string, text: string, url: string): string {
  const safeTitle = sanitizeText(title, MAX_TITLE_LENGTH);
  const safeText = sanitizeText(text, MAX_TEXT_LENGTH);
  const safeUrl = sanitizeHttpUrl(url);
  const segments: string[] = [];
  if (safeTitle && safeTitle !== safeText && safeTitle !== safeUrl) segments.push(safeTitle);
  if (safeText) segments.push(safeText);
  if (safeUrl && !safeText.includes(safeUrl)) segments.push(safeUrl);
  return sanitizeText(segments.join('\n'), MAX_TEXT_LENGTH);
}
