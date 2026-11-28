/**
 * Pure TypeScript Starred Messages helpers aligned with `apple/Shared/StarredMessages.swift`,
 * `apple/Persona/Views/Assistant/StarredMessagesPage.swift`, and `server/internal/assistant/saved/starred.go`.
 * Zero ArkUI dependencies so host Node test suites can import directly.
 */

export const STARRED_MESSAGES_FEATURE = 'starred_messages';
export const STARRED_TEXT_MAX_SCALARS = 4000;

export type StarredConversationKind = 'assistant' | 'agent';

export interface StarredSource {
  readonly machine_id: string;
  readonly session_id: string;
  readonly item_id: string;
  readonly conversation: StarredConversationKind;
  readonly source_fingerprint?: string;
}

export interface StarredReference extends StarredSource {
  readonly id: string;
}

export interface StarredSnapshot extends StarredSource {
  readonly id: string;
  readonly role: string;
  readonly title: string;
  readonly speaker: string;
  readonly source_manager?: string;
  readonly written_at?: string;
  readonly starred_at?: string;
  readonly text: string;
  readonly omitted_chars?: number;
}

export interface StarredIndexResult {
  readonly refs: StarredReference[];
  readonly canWrite: boolean;
}

export interface StarredListResult {
  readonly stars: StarredSnapshot[];
  readonly canWrite: boolean;
}

const POSITIONAL_ITEM_PATTERN = /^line\d+#\d+$/;
const FINGERPRINT_HEX_PATTERN = /^[0-9a-fA-F]{32}$/;

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

function normalizeConversationKind(value: string): StarredConversationKind | undefined {
  if (value === 'assistant' || value === 'agent') return value;
  return undefined;
}

/**
 * Positional IDs (`line<n>#<part>`) can be reused by a rewritten transcript and require a 32-hex fingerprint.
 */
export function isPositionalItemId(itemId: string): boolean {
  return POSITIONAL_ITEM_PATTERN.test(itemId);
}

export function isVerifiedStarSource(source: StarredSource): boolean {
  if (!source.machine_id || !source.session_id || !source.item_id) return false;
  if (!isPositionalItemId(source.item_id)) return true;
  const fp = (source.source_fingerprint || '').trim();
  return FINGERPRINT_HEX_PATTERN.test(fp);
}

export function starSourceKey(source: StarredSource): string {
  const fp = isPositionalItemId(source.item_id) ? (source.source_fingerprint || '').trim().toLowerCase() : '';
  return JSON.stringify([source.machine_id, source.session_id, source.item_id, source.conversation, fp]);
}

export function parseStarredReference(raw: Object | undefined | null): StarredReference | undefined {
  const rec = asRecord(raw);
  const id = cleanString(rec['id']);
  const machine = cleanString(rec['machine_id']);
  const session = cleanString(rec['session_id']);
  const item = cleanString(rec['item_id']);
  const conversation = normalizeConversationKind(cleanString(rec['conversation']));
  if (!id || !machine || !session || !item || !conversation) return undefined;
  const fpRaw = cleanString(rec['source_fingerprint']).toLowerCase();
  const ref: StarredReference = {
    id: id,
    machine_id: machine,
    session_id: session,
    item_id: item,
    conversation: conversation,
    source_fingerprint: fpRaw || undefined
  };
  return ref;
}

export function parseStarredSnapshot(raw: Object | undefined | null): StarredSnapshot | undefined {
  const rec = asRecord(raw);
  const id = cleanString(rec['id']);
  const machine = cleanString(rec['machine_id']);
  const session = cleanString(rec['session_id']);
  const item = cleanString(rec['item_id']);
  const conversation = normalizeConversationKind(cleanString(rec['conversation']));
  const text = rawString(rec['text']);
  if (!machine || !session || !item || !conversation || !text.trim()) return undefined;
  const roleRaw = cleanString(rec['role']);
  const role = roleRaw === 'user' || roleRaw === 'assistant' ? roleRaw : 'assistant';
  const fpRaw = cleanString(rec['source_fingerprint']).toLowerCase();
  const sourceManager = cleanString(rec['source_manager']);
  const writtenAt = cleanString(rec['written_at']);
  const starredAt = cleanString(rec['starred_at']);
  const omittedRaw = rec['omitted_chars'];
  const omitted = typeof omittedRaw === 'number' && Number.isFinite(omittedRaw) && omittedRaw > 0
    ? Math.floor(omittedRaw)
    : undefined;

  return {
    id: id,
    machine_id: machine,
    session_id: session,
    item_id: item,
    conversation: conversation,
    source_fingerprint: fpRaw || undefined,
    role: role,
    title: rawString(rec['title']).trim(),
    speaker: rawString(rec['speaker']).trim(),
    source_manager: sourceManager || undefined,
    written_at: writtenAt || undefined,
    starred_at: starredAt || undefined,
    text: text,
    omitted_chars: omitted
  };
}

export function parseStarredIndexResponse(raw: Record<string, Object> | undefined | null): StarredIndexResult {
  const rec = asRecord(raw);
  const canWrite = rec['can_write'] === true;
  const rawRefs = Array.isArray(rec['refs']) ? (rec['refs'] as Object[]) : [];
  const refs: StarredReference[] = [];
  for (const entry of rawRefs) {
    const parsed = parseStarredReference(entry);
    if (parsed) refs.push(parsed);
  }
  return { refs: refs, canWrite: canWrite };
}

export function parseStarredListResponse(raw: Record<string, Object> | undefined | null): StarredListResult {
  const rec = asRecord(raw);
  const canWrite = rec['can_write'] === true;
  const rawStars = Array.isArray(rec['stars']) ? (rec['stars'] as Object[]) : [];
  const stars: StarredSnapshot[] = [];
  for (const entry of rawStars) {
    const parsed = parseStarredSnapshot(entry);
    if (parsed && parsed.id) stars.push(parsed);
  }
  return { stars: stars, canWrite: canWrite };
}

export function buildStarredIndexMap(refs: readonly StarredReference[]): Map<string, string> {
  const map = new Map<string, string>();
  for (const ref of refs) {
    if (!isVerifiedStarSource(ref)) continue;
    map.set(starSourceKey(ref), ref.id);
  }
  return map;
}

export function isMessageStarred(indexMap: ReadonlyMap<string, string>, source: StarredSource): boolean {
  if (!isVerifiedStarSource(source)) return false;
  return indexMap.has(starSourceKey(source));
}

export function starredRefId(indexMap: ReadonlyMap<string, string>, source: StarredSource): string | undefined {
  if (!isVerifiedStarSource(source)) return undefined;
  return indexMap.get(starSourceKey(source));
}

/**
 * Bounds a snapshot payload before `POST /api/v1/assistant/starred`, matching iOS `StarredSnapshot.bounded()`.
 */
export function boundStarredInput(input: StarredSnapshot): Record<string, Object> {
  const trimmed = input.text.trim();
  const scalars = Array.from(trimmed);
  const kept = scalars.length > STARRED_TEXT_MAX_SCALARS
    ? scalars.slice(0, STARRED_TEXT_MAX_SCALARS).join('')
    : trimmed;
  const extraOmitted = Math.max(0, scalars.length - STARRED_TEXT_MAX_SCALARS);
  const omitted = (input.omitted_chars || 0) + extraOmitted;

  const payload: Record<string, Object> = {
    machine_id: input.machine_id,
    session_id: input.session_id,
    item_id: input.item_id,
    conversation: input.conversation,
    role: input.role,
    title: input.title,
    speaker: input.speaker,
    text: kept
  };
  if (input.source_fingerprint) payload['source_fingerprint'] = input.source_fingerprint.toLowerCase();
  if (input.source_manager) payload['source_manager'] = input.source_manager;
  if (input.written_at) payload['written_at'] = input.written_at;
  if (omitted > 0) payload['omitted_chars'] = omitted;
  return payload;
}

export function starSpeakerLabel(
  star: StarredSnapshot,
  defaultUserLabel: string = 'Me',
  defaultAssistantLabel: string = 'Assistant'
): string {
  if (star.speaker) return star.speaker;
  return star.role === 'user' ? defaultUserLabel : defaultAssistantLabel;
}

/**
 * Multi-term case-insensitive search across `text`, `title`, and displayed `speaker`,
 * matching `StarredMessagesPage.swift`.
 */
export function filterStarredSnapshots(
  stars: readonly StarredSnapshot[],
  query: string,
  defaultUserLabel: string = 'Me',
  defaultAssistantLabel: string = 'Assistant'
): StarredSnapshot[] {
  const clean = (query || '').trim().toLowerCase();
  if (!clean) return stars.slice();
  const terms = clean.split(/\s+/).filter((t: string) => !!t);
  if (!terms.length) return stars.slice();

  return stars.filter((star: StarredSnapshot) => {
    const fields = [
      star.text.toLowerCase(),
      star.title.toLowerCase(),
      starSpeakerLabel(star, defaultUserLabel, defaultAssistantLabel).toLowerCase()
    ];
    return terms.every((term: string) => fields.some((field: string) => field.includes(term)));
  });
}
