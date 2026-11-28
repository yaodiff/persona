// Read-only speaker timeline types, defensive decoding and transcript grouping (#17674, #17675,
// docs/assistant/meeting-minutes.md "Speakers (#17674)"). Kept out of lib/kb.ts so nothing here enters the first
// screen, and outside src/modules/recording so the module-off read-only detail (RecordingReadOnlyDetail.tsx) can share
// the exact same decoding and display rules without importing the recording module.
import { initial } from "./kb";

/** Closed set of known speaker lifecycle states (`docs/assistant/meeting-minutes.md` "Speakers (#17674)"). */
export type SpeakerState = "waiting" | "ready" | "failed" | "cleared";

/** Closed set of known speaker separation failure reasons; unrecognized wire values decode as `"unknown"`. */
export type SpeakerFailReason =
  | "engine_unavailable"
  | "no_audio"
  | "too_long"
  | "timeout"
  | "gave_up"
  | "audio_gone"
  | "error"
  | "unknown";

/** Closed set of known speaker separation quality verdicts. */
export type SpeakerQuality = "ok" | "low" | "unknown";

export interface SpeakerPerson {
  id: string;
  name?: string;
  me?: boolean;
  merged_into?: string;
  minor?: boolean;
  seconds?: number;
  lines?: number;
  samples?: number[];
}

export type SpeakerTurn = [number, number, string];
export type SpeakerSpan = [number, number];

export interface DetailSpeakers {
  rev: number;
  state: SpeakerState;
  reason: SpeakerFailReason;
  quality: SpeakerQuality;
  source?: string;
  done?: number;
  end?: number;
  people: SpeakerPerson[];
  candidates: string[];
  spans: SpeakerTurn[];
  covered: SpeakerSpan[];
  trimmed: boolean;
}

const SPEAKER_ID_RE = /^s[1-9][0-9]*$/;
const KNOWN_STATES = new Set<string>(["waiting", "ready", "failed", "cleared"]);
const KNOWN_FAIL_REASONS = new Set<string>([
  "engine_unavailable",
  "no_audio",
  "too_long",
  "timeout",
  "gave_up",
  "audio_gone",
  "error",
]);

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const nonNeg = (v: unknown): number | undefined =>
  typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : undefined;

/** True when `id` is a valid hub speaker identifier ("s1", "s2", ...). */
export function isValidSpeakerId(id: unknown): id is string {
  return typeof id === "string" && SPEAKER_ID_RE.test(id);
}

/** Numeric speaker number from a valid speaker id ("s3" -> 3). */
export function speakerNumber(id: string): number {
  return Number.parseInt(id.slice(1), 10);
}

/** Follows `merged_into` (bounded against malformed cycles) to the canonical unmerged speaker id. */
export function canonicalSpeakerId(
  people: readonly Pick<SpeakerPerson, "id" | "merged_into">[],
  id: string,
): string {
  if (!people.length) return id;
  const byId = new Map(people.map((p) => [p.id, p]));
  const seen = new Set<string>();
  let cur = id;
  while (!seen.has(cur)) {
    seen.add(cur);
    const p = byId.get(cur);
    if (!p?.merged_into) return cur;
    cur = p.merged_into;
  }
  return id;
}

/** Single glyph for a speaker's neutral avatar: first meaningful character of the name, else the speaker number. */
export function speakerAvatarGlyph(person: Pick<SpeakerPerson, "id" | "name">): string {
  const name = person.name?.trim();
  if (name) {
    const ch = initial(name);
    if (ch) return ch;
  }
  return isValidSpeakerId(person.id) ? String(speakerNumber(person.id)) : "?";
}

/** Display label for a speaker: owner-chosen name if set, else localized numbered label ("Speaker 3"). */
export function speakerLabel(
  person: Pick<SpeakerPerson, "id" | "name">,
  formatNumbered: (n: number) => string,
): string {
  const name = person.name?.trim();
  if (name) return name;
  return isValidSpeakerId(person.id) ? formatNumbered(speakerNumber(person.id)) : "";
}

function decodePerson(v: unknown): SpeakerPerson | null {
  if (!isObj(v) || !isValidSpeakerId(v.id)) return null;
  const out: SpeakerPerson = { id: v.id };
  if (typeof v.name === "string" && v.name.trim()) out.name = v.name.trim();
  if (v.me === true) out.me = true;
  if (isValidSpeakerId(v.merged_into) && v.merged_into !== v.id) out.merged_into = v.merged_into;
  if (v.minor === true && !out.merged_into) out.minor = true;
  const seconds = nonNeg(v.seconds);
  if (seconds !== undefined) out.seconds = seconds;
  if (typeof v.lines === "number" && Number.isInteger(v.lines) && v.lines >= 0) out.lines = v.lines;
  if (Array.isArray(v.samples)) {
    const samples = v.samples.filter((s): s is number => nonNeg(s) !== undefined);
    if (samples.length) out.samples = samples;
  }
  return out;
}

/** Defensively decodes `detail.speakers`; malformed blocks or unknown states return null so the rest of the detail
 *  stays intact and renders as if no speaker block were present. */
export function decodeSpeakers(raw: unknown): DetailSpeakers | null {
  if (!isObj(raw)) return null;
  if (typeof raw.state !== "string" || !KNOWN_STATES.has(raw.state)) return null;
  const state = raw.state as SpeakerState;
  const rev = typeof raw.rev === "number" && Number.isInteger(raw.rev) && raw.rev >= 0 ? raw.rev : 0;

  let reason: SpeakerFailReason = "unknown";
  if (typeof raw.reason === "string" && KNOWN_FAIL_REASONS.has(raw.reason)) {
    reason = raw.reason as SpeakerFailReason;
  }

  let quality: SpeakerQuality = "ok";
  if (raw.quality === "low") quality = "low";
  else if (raw.quality === "ok" || raw.quality === "" || raw.quality === undefined) quality = "ok";
  else quality = "unknown";

  const seenIds = new Set<string>();
  const people: SpeakerPerson[] = [];
  if (Array.isArray(raw.people)) {
    for (const item of raw.people) {
      const p = decodePerson(item);
      if (!p || seenIds.has(p.id)) continue;
      seenIds.add(p.id);
      people.push(p);
    }
  }

  const seenCand = new Set<string>();
  const candidates: string[] = [];
  if (Array.isArray(raw.candidates)) {
    for (const c of raw.candidates) {
      if (typeof c !== "string") continue;
      const trimmed = c.trim();
      if (!trimmed || seenCand.has(trimmed)) continue;
      seenCand.add(trimmed);
      candidates.push(trimmed);
    }
  }

  const spans: SpeakerTurn[] = [];
  if (Array.isArray(raw.spans)) {
    for (const item of raw.spans) {
      if (!Array.isArray(item) || item.length !== 3) continue;
      const start = nonNeg(item[0]);
      const end = nonNeg(item[1]);
      if (start !== undefined && end !== undefined && end > start && isValidSpeakerId(item[2])) {
        spans.push([start, end, item[2]]);
      }
    }
  }

  const covered: SpeakerSpan[] = [];
  if (Array.isArray(raw.covered)) {
    for (const item of raw.covered) {
      if (!Array.isArray(item) || item.length !== 2) continue;
      const start = nonNeg(item[0]);
      const end = nonNeg(item[1]);
      if (start !== undefined && end !== undefined && end >= start) {
        covered.push([start, end]);
      }
    }
  }

  const done = nonNeg(raw.done);
  const end = nonNeg(raw.end);
  const source = typeof raw.source === "string" && raw.source.trim() ? raw.source.trim() : undefined;

  return {
    rev,
    state,
    reason,
    quality,
    ...(source ? { source } : {}),
    ...(state === "waiting" && done !== undefined ? { done } : {}),
    ...(state === "waiting" && end !== undefined && end > 0 ? { end } : {}),
    people,
    candidates,
    spans,
    covered,
    trimmed: raw.trimmed === true,
  };
}

/** Whether `speakers` has visible UI content (an unrecognized state, `cleared`, or `failed` with an unrecognized
 *  reason renders identically to having no speakers block). */
export function hasVisibleSpeakers(speakers: DetailSpeakers | null): speakers is DetailSpeakers {
  if (!speakers) return false;
  if (speakers.state === "cleared") return false;
  if (speakers.state === "failed" && speakers.reason === "unknown") return false;
  if (speakers.state === "ready" && speakers.quality === "unknown") return false;
  return true;
}

/** Progress minutes for a `waiting` speakers block, matching minutes-wait rounding. */
export function waitingSpeakerMinutes(
  done?: number,
  end?: number,
): { done: number; total?: number } | null {
  if (done === undefined || !Number.isFinite(done) || done < 0) return null;
  if (end !== undefined && Number.isFinite(end) && end > 0) {
    const total = Math.max(1, Math.round(end / 60));
    return { done: Math.min(Math.floor(done / 60), total), total };
  }
  return { done: Math.floor(done / 60) };
}

export type TranscriptSpeakerGroup<T> =
  | { kind: "speaker"; speakerId: string; person: SpeakerPerson | undefined; unsure: boolean; rows: T[] }
  | { kind: "plain"; rows: T[] };

/** Groups consecutive transcript lines that share the same canonical speaker and certainty state; bookmarks and
 *  unlabelled lines stay in plain groups without a speaker header. */
export function groupLinesBySpeaker<
  T extends { speaker?: string; speakerUnsure?: boolean; bookmark?: boolean },
>(rows: readonly T[], speakers: DetailSpeakers | null): TranscriptSpeakerGroup<T>[] {
  if (!speakers || speakers.state !== "ready" || speakers.quality === "unknown") {
    return rows.length ? [{ kind: "plain", rows: [...rows] }] : [];
  }
  const byId = new Map(speakers.people.map((p) => [p.id, p]));
  const lowQuality = speakers.quality === "low";
  const groups: TranscriptSpeakerGroup<T>[] = [];

  for (const row of rows) {
    if (row.bookmark || !isValidSpeakerId(row.speaker)) {
      const last = groups.at(-1);
      if (last && last.kind === "plain") last.rows.push(row);
      else groups.push({ kind: "plain", rows: [row] });
      continue;
    }
    const canon = canonicalSpeakerId(speakers.people, row.speaker);
    const person = byId.get(canon);
    const unsure = lowQuality || row.speakerUnsure === true || person?.minor === true;
    const last = groups.at(-1);
    if (last && last.kind === "speaker" && last.speakerId === canon && last.unsure === unsure) {
      last.rows.push(row);
    } else {
      groups.push({ kind: "speaker", speakerId: canon, person, unsure, rows: [row] });
    }
  }
  return groups;
}
