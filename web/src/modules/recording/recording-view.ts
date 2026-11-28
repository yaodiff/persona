// A recording's view of its knowledge entry (docs/assistant/meeting-minutes.md, docs/assistant/day-recap.md): its
// transcript lines at their clock time, an all-day recording's parts of the day, how long its minutes are kept, and
// what its metadata says of its meeting (room, follow-up, private mark, photos), all read defensively. Moved from
// lib/kb.ts with the recording module (#13407), so none of it loads with the first screen.
import { formatDate, t } from "../../i18n";
import { knownZone, wallClock, type DayRecapEntry, type DayRecapGap, type KBSegment, type MeetingConversation, type MeetingRoom, type RecordingPhoto } from "../../lib/kb";
import { isValidSpeakerId } from "../../lib/recording-speakers";

function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

const pad = (n: number) => String(n).padStart(2, "0");
const yearMonthDay = (d: Date) => formatDate(d, { year: "numeric", month: "short", day: "numeric" });

/** The photo a minutes item cites (`"photo": n`), when it names one. */
export function citedPhoto(item: { photo?: unknown }): number | undefined {
  return Number.isInteger(item.photo) && (item.photo as number) > 0 ? (item.photo as number) : undefined;
}

/** `GET …/photos`'s list read defensively, oldest first: entries without a positive whole `n` or a string `id` are left
 *  out, blank texts read as absent. */
export function recordingPhotos(v: unknown): RecordingPhoto[] {
  const raw = v && typeof v === "object" && !Array.isArray(v) ? (v as { photos?: unknown }).photos : undefined;
  if (!Array.isArray(raw)) return [];
  const out: RecordingPhoto[] = [];
  for (const x of raw as Record<string, unknown>[]) {
    if (!x || typeof x !== "object" || !Number.isInteger(x.n) || (x.n as number) <= 0 || typeof x.id !== "string" || !x.id) continue;
    const words = (k: "text" | "caption" | "note") => (typeof x[k] === "string" && (x[k] as string).trim() ? { [k]: (x[k] as string).trim() } : {});
    out.push({
      n: x.n as number, id: x.id, at: typeof x.at === "number" && Number.isFinite(x.at) ? Math.max(0, x.at) : 0,
      mime: str(x.mime) ?? "", width: Number(x.width) || 0, height: Number(x.height) || 0,
      ...words("text"), ...words("caption"), ...words("note"),
      cited: x.cited === true, forwarded: x.forwarded === true, added_at: str(x.added_at) ?? "",
    });
  }
  return out.sort((a, b) => a.n - b.n);
}

/** What `detail.meta` (or a `GET …/live`) says of a recording's meeting conversation (#3862): well-formed values only. */
export interface MeetingMeta {
  room?: MeetingRoom;
  followup?: MeetingConversation;
  private?: boolean;
}

/** The room, follow-up and private mark a recording's metadata carries, read defensively. */
export function meetingMeta(meta?: Record<string, unknown> | null): MeetingMeta {
  const out: MeetingMeta = {};
  const room = meetingRoom(meta?.room);
  if (room) out.room = room;
  const followup = meetingConversation(meta?.followup);
  if (followup) out.followup = followup;
  if (typeof meta?.private === "boolean") out.private = meta.private;
  return out;
}

/** A room as the apps read it: an object with a string state; its ids only both together, its problem only when given. */
export function meetingRoom(v: unknown): MeetingRoom | undefined {
  if (!v || typeof v !== "object" || Array.isArray(v)) return undefined;
  const r = v as Record<string, unknown>;
  if (typeof r.state !== "string") return undefined;
  return { state: r.state, ...meetingConversation(r), ...(typeof r.problem === "string" && r.problem ? { problem: r.problem } : {}) };
}

/** A conversation's ids, both non-empty strings, or nothing. */
export function meetingConversation(v: unknown): MeetingConversation | undefined {
  if (!v || typeof v !== "object" || Array.isArray(v)) return undefined;
  const r = v as Record<string, unknown>;
  return typeof r.machine_id === "string" && r.machine_id && typeof r.session_id === "string" && r.session_id
    ? { machine_id: r.machine_id, session_id: r.session_id } : undefined;
}

/** Whether a recording is a meeting as #2981 counts them (`kind: meeting` or minutes asked for): only those take the
 *  private mark (docs/assistant/meeting-minutes.md "The private mark"). */
export function countsAsMeeting(meta?: Record<string, unknown> | null): boolean {
  return meetingKind(meta) || meta?.minutes_asked === true;
}

/** Whether a recording's `kind` is `meeting`: only those take photos (#3862 phase 3, `409 not_meeting` otherwise). */
export function meetingKind(meta?: Record<string, unknown> | null): boolean {
  return str(meta?.kind)?.trim().toLowerCase() === "meeting";
}

export interface RecordingView {
  startedAt?: string;
  /** "1 h 2 min", "3 min 20 s" (in Chinese) */
  duration?: string;
  device: string;
  language?: string;
  /** Transcription still going (more will come). */
  partial: boolean;
  /** A line per segment and bookmark, at its clock time, in order. */
  lines: { seconds: number; clock: string; text: string; bookmark?: boolean; speaker?: string; speakerUnsure?: boolean }[];
  /** Persona attends the meeting in this role (#2981); absent when off. */
  attendance?: "listener" | "participant";
  /** A room meeting whose room only takes notes (#8851): Persona is not in it and answers nothing. */
  notesOnly?: boolean;
  /** The session that attends; absent: the main chat. */
  attendingSession?: { machineId: string; sessionId: string };
  /** When its transcript expired with `recording_days` and only the minutes were kept (`meta.trimmed_at`, #3780); absent
   *  while it is whole. */
  trimmedAt?: string;
  /** An all-day recording (`kind: day`, #4741): the date it is of and its part (1 unless the day outgrew one). */
  allDay?: { day: string; part: number };
  /** The zone its clock is read in: an all-day recording's `tz` when this browser knows the zone; absent: local time. */
  tz?: string;
}

/** Wall-clock time shared by minutes, recap and transcript, in `tz` when given (else local); offsets when the start is
 *  unknown. */
export function recordingClock(startedAt: string | undefined, seconds: number, tz?: string): string {
  const start = Date.parse(startedAt ?? "");
  if (!Number.isFinite(start)) return formatSeconds(seconds);
  const c = wallClock(new Date(start + seconds * 1000), tz);
  return `${pad(c.h)}:${pad(c.m)}:${pad(c.s)}`;
}

/** Keep fractional seconds in anchors; bookmarks at the same time get their own identity. */
export function transcriptLineId(seconds: number, bookmark = false): string {
  return `recording-${bookmark ? "bookmark" : "line"}-${seconds}`;
}

/** The transcript line containing an offset, or its preceding line in a gap. Never jump to a bookmark. */
export function transcriptTarget(lines: RecordingView["lines"], seconds: number): string | undefined {
  const line = lines.findLast((line) => !line.bookmark && line.seconds <= seconds) ?? lines.find((line) => !line.bookmark);
  return line ? transcriptLineId(line.seconds) : undefined;
}

/** A recording's facts and transcript (meta.json and the detail's segments); null for the other kinds. */
export function recordingView(kind: string, meta?: Record<string, unknown> | null, segments?: KBSegment[] | null): RecordingView | null {
  if (kind !== "recordings") return null;
  const started = str(meta?.started_at);
  const t0 = started ? Date.parse(started) : NaN;
  const day = str(meta?.kind) === "day";
  const tz = day && knownZone(meta?.tz) ? (meta.tz as string) : undefined;
  const clock = (sec: number) => recordingClock(started, sec, tz);
  const at: { sec: number; line: RecordingView["lines"][number] }[] = [];
  const trimmed = str(meta?.trimmed_at);
  const trimmedAt = trimmed && Number.isFinite(Date.parse(trimmed)) ? trimmed : undefined;
  // Detail refreshes can briefly pair new retention metadata with an older segment list.
  for (const s of trimmedAt ? [] : segments ?? []) {
    if (s?.echo === true) continue; // Persona's echo is not the meeting's: hidden, as the phone hides it
    const sec = Number(s?.start) || 0;
    if (typeof s?.text === "string" && s.text.trim()) {
      const speaker = isValidSpeakerId(s.speaker) ? s.speaker : undefined;
      const speakerUnsure = speaker && s.speaker_unsure === true ? true : undefined;
      at.push({
        sec,
        line: {
          seconds: sec,
          clock: clock(sec),
          text: s.text.trim(),
          ...(speaker ? { speaker } : {}),
          ...(speakerUnsure ? { speakerUnsure: true } : {}),
        },
      });
    }
  }
  const marks = Array.isArray(meta?.bookmarks) ? (meta.bookmarks as { at?: unknown; label?: unknown }[]) : [];
  for (const b of marks) {
    const sec = Number(b?.at) || 0;
    at.push({ sec, line: { seconds: sec, clock: clock(sec), text: typeof b?.label === "string" ? b.label.trim() : "", bookmark: true } });
  }
  at.sort((x, y) => x.sec - y.sec); // stable: a bookmark after the segment that starts with it
  const lines = at.map((x) => x.line);
  const secs = Number(meta?.duration);
  const device = str(meta?.device) === "watch" ? t("recording.device_watch") : t("recording.device_phone");
  const role = str(meta?.attendance);
  const attending = meta?.attending_session as { machine_id?: unknown; session_id?: unknown } | null | undefined;
  const machineId = str(attending?.machine_id), sessionId = str(attending?.session_id);
  const part = meta?.part;
  return {
    startedAt: Number.isFinite(t0) ? started : undefined,
    duration: Number.isFinite(secs) && secs > 0 ? durationLabel(secs) : undefined,
    device,
    language: str(meta?.language) || undefined,
    partial: meta?.complete !== true,
    lines,
    ...(role === "listener" || role === "participant" ? { attendance: role } : {}),
    ...(role === "notes" ? { notesOnly: true } : {}),
    // Both ids or none: anything else names no session, and the main chat attends.
    ...(machineId && sessionId ? { attendingSession: { machineId, sessionId } } : {}),
    ...(trimmedAt ? { trimmedAt } : {}),
    ...(day ? { allDay: { day: str(meta?.day) ?? "", part: typeof part === "number" && Number.isInteger(part) && part >= 1 && part <= 99 ? part : 1 } } : {}),
    ...(tz ? { tz } : {}),
  };
}

export type DayPart = "morning" | "afternoon" | "evening";

/** The part of the day an offset falls in on the recording's clock: morning before 12:00, afternoon before 18:00, then
 *  evening; undefined when the start is unknown. */
export function dayPart(startedAt: string | undefined, seconds: number, tz?: string): DayPart | undefined {
  const start = Date.parse(startedAt ?? "");
  if (!Number.isFinite(start)) return undefined;
  const { h } = wallClock(new Date(start + seconds * 1000), tz);
  return h < 12 ? "morning" : h < 18 ? "afternoon" : "evening";
}

/** "09:41", the clock a recap's ranges are in; offsets when the start is unknown. */
export function dayClock(startedAt: string | undefined, seconds: number, tz?: string): string {
  const clock = recordingClock(startedAt, seconds, tz);
  return Number.isFinite(Date.parse(startedAt ?? "")) ? clock.slice(0, 5) : clock;
}

export interface RecapGroup<E extends DayRecapEntry = DayRecapEntry> {
  /** Undefined when the start is unknown: the whole timeline is one group. */
  part?: DayPart;
  entries: E[];
}

/** A recap's timeline in time order (entries at the same time in the order given), cut by part of the day at each
 *  entry's start. */
export function recapGroups<E extends DayRecapEntry>(timeline: E[] | null | undefined, startedAt: string | undefined, tz?: string): RecapGroup<E>[] {
  const groups: RecapGroup<E>[] = [];
  const sorted = [...(timeline ?? [])].filter((e) => e && Number.isFinite(e.from)).sort((a, b) => a.from - b.from);
  for (const entry of sorted) {
    const part = dayPart(startedAt, entry.from, tz);
    const last = groups.at(-1);
    if (last && last.part === part) last.entries.push(entry);
    else groups.push({ part, entries: [entry] });
  }
  return groups;
}

/** A recap's gaps worth naming (a minute or longer), in time order, as clock ranges with their reason. */
export function recapGaps(gaps: DayRecapGap[] | null | undefined, startedAt: string | undefined, tz?: string): { from: string; to: string; reason: string }[] {
  return (gaps ?? [])
    .filter((g) => g && Number.isFinite(g.from) && Number.isFinite(g.to) && g.to - g.from >= 60)
    .sort((a, b) => a.from - b.from)
    .map((g) => ({ from: dayClock(startedAt, g.from, tz), to: dayClock(startedAt, g.to, tz), reason: typeof g.reason === "string" ? g.reason : "" }));
}

/** The day a trimmed recording's minutes go (#3780): `days` (the assistant's `minutes_days` in effect) after it
 *  started, as a date; undefined when either is unknown or days is 0 (forever). */
export function minutesKeptUntil(startedAt: string | undefined, days: unknown): string | undefined {
  const start = Date.parse(startedAt ?? "");
  if (!Number.isFinite(start) || typeof days !== "number" || !Number.isInteger(days) || days <= 0) return undefined;
  return yearMonthDay(new Date(start + days * 86_400_000));
}

/** 3733 → "1 h 2 min"; 200 → "3 min 20 s"; 42 → "42 s" (in Chinese). */
export function durationLabel(secs: number): string {
  const s = Math.round(secs);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return t("recording.duration_hours", { h, m });
  if (m > 0) return t("recording.duration_minutes", { m, s: s % 60 });
  return t("recording.duration_seconds", { s });
}

/** Seconds from the start as "1:02:03" / "2:03", when the start time is unknown. */
function formatSeconds(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  return h > 0 ? `${h}:${pad(Math.floor((s % 3600) / 60))}:${pad(s % 60)}` : `${Math.floor(s / 60)}:${pad(s % 60)}`;
}
