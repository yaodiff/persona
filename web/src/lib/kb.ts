// The knowledge base (docs/assistant/knowledge.md "App API"): the hub's wire types and the pure logic of the "Knowledge" page —
// list grouping and ranking, search results merged by entry with the words marked, which tags a row has room for,
// the timeline, the profile without what the header shows, and the message "Ask Persona" sends. The iOS app is the
// reference (apple/Persona/Views/Knowledge/KnowledgeView.swift, apple/Shared/Knowledge.swift).
import type { TopicPoint } from "./kb-points";
import { formatDate, t } from "../i18n";
import { contextBody } from "./context-body";
import type { Session } from "./types";

export const KB_KINDS = ["projects", "topics", "chats", "people", "docs", "meetings", "events", "recordings"] as const;
export type KBKind = (typeof KB_KINDS)[number];

export const KIND_TITLE: Record<KBKind, string> = { projects: t("lib-kb.kind_projects"), topics: t("lib-kb.kind_topics"), chats: t("lib-kb.kind_chats"), people: t("lib-kb.kind_people"), docs: t("lib-kb.kind_docs"), meetings: t("lib-kb.kind_meetings"), events: t("lib-kb.kind_events"), recordings: t("lib-kb.kind_recordings") };

/** The kinds the list offers as chips: recordings are listed under meetings (#8871), with the meetings imported from
 *  connected apps, so the knowledge base has one place for meetings and notes. */
export const LIST_KINDS: readonly KBKind[] = KB_KINDS.filter((k) => k !== "recordings");

export function isKind(k: unknown): k is KBKind {
  return typeof k === "string" && (KB_KINDS as readonly string[]).includes(k);
}

/** The list a route names; anything else is the default list, projects. Recordings are listed under meetings (#8871),
 *  so an older link to the recordings list, or a recording's own page, shows that list. */
export function kindOf(k?: string): KBKind {
  if (k === "recordings") return "meetings";
  return isKind(k) ? k : "projects";
}

export function kindTitle(k: string): string {
  return isKind(k) ? KIND_TITLE[k] : k;
}

// ── wire types ──────────────────────────────────────────

export interface KBEntry {
  kind: string;
  id: string;
  name: string;
  one_line?: string;
  importance?: string;
  relevance: number;
  tags?: string[] | null;
  updated?: string;
  /** projects: "short · active" (kind · status); chats: dm / group…; topics: how many knowledge points, in decimal */
  extra?: string;
  /** recordings: meeting, note or day (#8870); absent from an older hub */
  recording_kind?: string;
  /** recordings: why in-flight work (minutes, recap or upload) is paused on the hub ("module_off"); absent otherwise (#15214) */
  paused?: string;
  /** chats: the source they came from; docs, meetings and events: the connector that supplied them (#8872) */
  source?: string;
  /** recordings: the id of the imported meeting it overlaps, as the meetings list has it (#8872) */
  meeting_id?: string;
  /** recordings in the meetings list (this client's, `mergeMeetingLists` in views/knowledge/meetings.ts): the imported meeting it stands for (#8872) */
  imported?: KBEntry;
}

/** Why a stored recording's in-flight work is paused on the hub (#15214): closed so an unknown reason from a newer hub
 *  neither crashes nor triggers module-off wording. */
export type RecordingPause = "module_off" | "unknown";

/** Decodes `paused` from a recording entry or detail into the closed set; empty, absent or non-string values are null. */
export function recordingPaused(value: unknown): RecordingPause | null {
  if (typeof value !== "string" || value.length === 0) return null;
  return value === "module_off" ? "module_off" : "unknown";
}

/** The tag a recording's row carries in the meetings list (#8871): notes and all-day recordings are the exceptions among
 *  meetings, so only they are marked; a meeting, an imported one, or a recording from a hub that does not say, none. */
export function recordingKindTag(e: KBEntry): string | null {
  if (e.kind !== "recordings") return null;
  if (e.recording_kind === "note") return t("lib-kb.recording_note");
  if (e.recording_kind === "day") return t("lib-kb.recording_day");
  return null;
}

export interface KBList {
  items: KBEntry[] | null;
  tags?: string[] | null;
}

export interface KBMomentRef {
  id: string;
  author: string;
  text: string;
  at?: string;
  comments: number;
  note?: string;
  /** The name of the feed the post came from (whoever archived it says); none: Moments. */
  feed?: string;
}

/** What to call a post's feed: its archived name, else Moments. */
export function momentFeed(m: Pick<KBMomentRef, "feed">): string {
  return m.feed?.trim() || t("lib-kb.moments");
}

export interface KBDetail {
  kind: string;
  id: string;
  path: string;
  meta?: Record<string, unknown> | null;
  summary?: string;
  log?: string[] | null;
  recent?: string;
  links?: KBEntry[] | null;
  /** The links the user removed by hand (meta.excluded), named as links are: Persona leaves them out until allowed again. */
  removed?: KBEntry[] | null;
  moments?: KBMomentRef[] | null;
  /** recordings: the transcript, seconds from the start (docs/protocol/protocol.md "Recording sync"). */
  segments?: KBSegment[] | null;
  /** recordings: hub-separated speakers and owner-chosen names (#17674, docs/assistant/meeting-minutes.md "Speakers
   *  (#17674)"). Read defensively on its own (lib/recording-speakers.ts `decodeSpeakers`), so a broken block never
   *  costs the rest of the detail. */
  speakers?: unknown;
  /** Recordings may retain older minutes while a newer revision is pending or failed. */
  minutes?: RecordingMinutes | null;
  /** An all-day recording's recap (#4741), kept while a newer one is pending or failed. */
  recap?: DayRecap | null;
  /** recordings: why in-flight work (minutes, recap or upload) is paused on the hub ("module_off"); absent otherwise (#15214) */
  paused?: string;
  /** topics: the knowledge points (docs/assistant/knowledge.md "Knowledge points"). */
  points?: TopicPoint[] | null;
}

/** A topic's point count from its list entry (extra) or its metadata (points). */
export function topicPointCount(e: Pick<KBEntry, "kind" | "extra">, meta?: Record<string, unknown> | null): number | null {
  if (e.kind !== "topics") return null;
  if (typeof meta?.points === "number") return meta.points;
  const n = Number.parseInt(e.extra ?? "", 10);
  return Number.isFinite(n) ? n : null;
}

export interface MinutesPoint {
  text: string;
  at: number;
  who?: string;
  /** The meeting photo it cites instead of a transcript line (#3862 phase 3): its `n`; `at` is then the photo's. */
  photo?: number;
}

export interface MinutesAction {
  id: string;
  who?: string;
  what: string;
  by?: string;
  due_date?: string;
  at: number;
  todo_id?: string;
  /** The meeting photo it cites instead of a transcript line (#3862 phase 3): its `n`; `at` is then the photo's. */
  photo?: number;
}

export interface RecordingMinutes {
  version: number;
  revision: number;
  generated_at: string;
  title: string;
  /** The hub's fixed-format span ("2026-11-10 14:00–15:10"), shown only without `start` / `end` (an older hub). */
  when: string;
  /** The recording's span (RFC 3339, #4713), formatted here like the app's other dates (`formatSpan`). */
  start?: string;
  end?: string;
  where?: string;
  attendees?: string[];
  points?: MinutesPoint[];
  decisions?: MinutesPoint[];
  actions?: MinutesAction[];
  open?: MinutesPoint[];
  /** The summary card (#6563): the headline, the core conclusions and the points and decisions grouped by topic. Read
   *  defensively on its own (modules/recording/minutes-overview.ts `minutesOverview`), so a broken one never costs the fields above. */
  overview?: unknown;
  /** Real disagreements and each side's stance (#16728). Read defensively on its own
   *  (modules/recording/minutes-debates.ts `minutesDebates`), so a broken block never costs the fields above. */
  debates?: unknown;
  partial?: boolean;
  /** The same minutes as Markdown, what Copy sends; it follows the summary card when there is one. */
  markdown: string;
}

/** A recap point: what was said, at the start of the transcript line it cites (seconds since `started_at`). */
export type DayRecapPoint = MinutesPoint;

/** The meeting a recap entry lies in, when the hub knows it: one of the user's meeting recordings (`recordings`) or a
 *  `meetings` / `events` item of the knowledge base. */
export interface DayRecapMeeting {
  kind: string;
  id: string;
  name: string;
}

/** One stretch of the day: inside a meeting or outside one, with what was said (docs/assistant/day-recap.md). */
export interface DayRecapEntry {
  from: number;
  to: number;
  /** "meeting" or "outside"; anything else is shown as outside. */
  kind: string;
  title: string;
  meeting?: DayRecapMeeting | null;
  points?: DayRecapPoint[] | null;
}

/** Time of the day with no text and why: `paused`, `interrupted`, `stopped`, `pending`, `lost`, `erased`, or a reason
 *  this client doesn't know. */
export interface DayRecapGap {
  from: number;
  to: number;
  reason: string;
}

/** `detail.recap` of an all-day recording (docs/assistant/day-recap.md "What the hub stores and returns"): every time is
 *  seconds since the recording's `started_at`, the clock of its segments. */
export interface DayRecap {
  version: number;
  revision: number;
  generated_at: string;
  day: string;
  /** What the recap read: up to the end of the last chunk. */
  covers: { from: number; to: number };
  /** Made after the day closed: `complete` (everything arrived) or `cutoff` (what arrived by 6 hours after the day). */
  final: boolean;
  final_reason?: string;
  summary: string;
  timeline?: DayRecapEntry[] | null;
  actions?: MinutesAction[] | null;
  gaps?: DayRecapGap[] | null;
  /** The day was too long and some of it was not read. */
  partial?: boolean;
  markdown: string;
}

export interface KBSegment {
  start: number;
  end: number;
  text: string;
  /** Persona's own answer heard by the meeting's microphone (#5905, docs/assistant/meeting-minutes.md "Persona's echo"):
   *  kept on the hub, not shown as the meeting's. */
  echo?: boolean;
  /** Hub-assigned speaker id ("s1", "s2", ...) for a hub-transcribed meeting with a timeline (#17674). */
  speaker?: string;
  /** True when the line's speaker assignment is unsure or the meeting's separation quality is low (#17674). */
  speaker_unsure?: boolean;
}

/** One question asked in an attended meeting (docs/assistant/meeting-minutes.md "Attending a meeting", `Ask`). `state`,
 *  `question_source` and `error` are open sets: an unknown value is shown generically. */
export interface RecordingAsk {
  id: string;
  question: string;
  question_source: string;
  at?: number;
  state: string;
  answer: string;
  lang?: string;
  speak?: boolean;
  /** An earlier meeting's `when` / `start` / `end` are its minutes' (#4713). */
  sources?: { kind: string; id: string; name: string; when?: string; start?: string; end?: string }[] | null;
  error?: string;
  asked_at: string;
  answered_at?: string;
  /** A room meeting's (#3862, "Asking the room"): how it is answered once its first attempt went out (`room` or
   *  `material`), and the lines a heard question was made of. */
  via?: string;
  heard?: KBSegment[] | null;
}

/** A room meeting's room conversation as `GET …/live` and `detail.meta` give it (docs/assistant/meeting-minutes.md
 *  "Fields on `GET …/live` and `detail.meta`", #3862). `state` and `problem` are open sets: an unknown state reads as
 *  `starting`; the ids come once it has started. */
export interface MeetingRoom {
  state: string;
  machine_id?: string;
  session_id?: string;
  problem?: string;
}

/** One of a meeting's conversations (its follow-up), by ids. */
export interface MeetingConversation {
  machine_id: string;
  session_id: string;
}

/** `GET /api/v1/recordings/{id}/live`: how far the attending session has been fed, and the meeting's questions. */
export interface RecordingLive {
  next: number;
  fed: number;
  attendance: string;
  attending_session?: { machine_id: string; session_id: string; main?: boolean };
  /** Open set: attendee_unavailable, attendee_not_allowed, live_full today. */
  problem?: string;
  asks?: RecordingAsk[] | null;
  /** The meeting conversation (#3862): the private mark (absent until chosen), a room meeting's room, the follow-up once
   *  made, and each part's revision (`room`, `asks`, `private`, `followup`; numbers that only go up). `photos` has a
   *  revision here and no field: its list is `GET …/photos` (phase 3). */
  private?: boolean;
  room?: MeetingRoom;
  followup?: MeetingConversation;
  revs?: Record<string, number>;
  /** Phase 2 (`meeting_live_view`, "Phase 2: live view"): the live log from `?from=` (no `lines` without it), at most
   *  500, `more` when there are more; the room's topics (always present) and when they last changed. Read through
   *  `liveLines` / `meetingTopics` (modules/recording/meeting-thread.ts, off the first screen). */
  lines?: unknown;
  more?: boolean;
  topics?: unknown;
  topics_at?: string;
}

/** One line of a meeting's live log (`GET …/live?from=`): `n` its position (from 0, append-only), `start` / `end` seconds
 *  from the start; `source` (phase 5) is an open set. */
export interface LiveLine {
  n: number;
  start: number;
  end: number;
  text: string;
  source?: string;
}

/** One of a meeting's topics as the room set them: `state` `done`, `current` (at most one) or `open`; `at` the seconds
 *  the topic began. */
export interface MeetingTopic {
  title: string;
  state: "done" | "current" | "open";
  at?: number;
  note?: string;
}

/** One of a meeting's photos as `GET /api/v1/recordings/{id}/photos` lists them (docs/assistant/meeting-minutes.md
 *  "Phase 3: photos", #3862): `n` counts from 1 per recording and is never reused, `at` is seconds from the start,
 *  `text` what the phone recognized in it, `caption` the owner's words, `note` the room's description, `cited` whether
 *  the current minutes cite it, `forwarded` whether it went into the room. Its image is `GET …/photos/{n}`. */
export interface RecordingPhoto {
  n: number;
  id: string;
  at: number;
  mime: string;
  width: number;
  height: number;
  text?: string;
  caption?: string;
  note?: string;
  cited: boolean;
  forwarded: boolean;
  added_at: string;
}

/** A line a search found: `path` relative to the data dir, `n` its line number in that file. */
export interface KBFound {
  path: string;
  line: string;
  n?: number;
}

/** One place in an entry that matched the search it was opened from; a message comes with the ones around it. */
export interface KBMatch {
  path: string;
  n: number;
  line: string;
  day?: string;
  before?: string[] | null;
  after?: string[] | null;
}

export interface KBDetailResponse {
  detail: KBDetail;
  agents?: Session[] | null;
  matches?: KBMatch[] | null;
  /** chats: the chat's Inbox items (#15356, docs/assistant/knowledge.md "A chat's Inbox items"); absent: none. */
  inbox?: KBInboxItem[] | null;
}

/** An Inbox item a chat's page lists: what its row draws, and `messages`, the rows of the page's `recent` it holds,
 *  each by its day and its lines as written there (the hub matched them on time, sender and text). */
export interface KBInboxItem {
  id: string;
  title: string;
  tier?: string;
  /** an open item's section (action, working, fyi, noise) as the Inbox has it */
  status?: string;
  summary?: string;
  preview?: string;
  updated_at: string;
  done_at?: string | null;
  messages?: { day: string; line: string }[] | null;
}

/** One entry a search found (`/kb-search?results=1`): how many lines matched and the first few. */
export interface KBResult {
  entry: KBEntry;
  count: number;
  hits?: KBFound[] | null;
  in_name?: boolean;
}

export interface KBDay {
  day: string;
  messages: { n: number; text: string }[] | null;
  prev?: string;
  next?: string;
}

/** `PUT /kb/{kind}/{id}` (the hub's kb.Update); tagging set here is locked against the automatic one. */
export interface KBUpdate {
  remove_people?: string[];
  remove_chats?: string[];
  remove_projects?: string[];
  /** With a removal: also lock the whole lists on both entries; false keeps out only the removed link. */
  lock_lists?: boolean;
  /** Links the user removed that Persona may make again on its own; nothing is linked. */
  allow_people?: string[];
  allow_chats?: string[];
  allow_projects?: string[];
  /** Omitted preserves confirmed facts; an explicit empty string clears them. */
  pinned?: string;
  importance?: string;
  relevance?: number;
  tags?: string[];
  status?: string;
  project_kind?: string;
}

// ── tagging ─────────────────────────────────────────────

export type Tone = "red" | "orange" | "accent" | "neutral";

/** P0 red, P1 orange, P2 blue, P3 and unranked grey. */
export function importanceTone(p?: string): Tone {
  return p === "P0" ? "red" : p === "P1" ? "orange" : p === "P2" ? "accent" : "neutral";
}

/** Relevance to the user (0–100): its pill is red from 70, orange from 40, neutral below. */
export function relevanceTone(n: number): Tone {
  return n >= 70 ? "red" : n >= 40 ? "orange" : "neutral";
}

export interface ProjectInfo {
  kind: "short" | "long";
  status: string;
}

/** A project's kind and status from the list's `extra` ("long · done"); null for other kinds. */
export function projectInfo(e: Pick<KBEntry, "kind" | "extra">): ProjectInfo | null {
  if (e.kind !== "projects") return null;
  const [k, st] = (e.extra ?? "").split(" · ");
  return { kind: k?.trim() === "long" ? "long" : "short", status: st?.trim() || "active" };
}

/** "Short-term" / "Long-term", only when the hub said which. */
export function projectKindLabel(e: Pick<KBEntry, "kind" | "extra">): string | null {
  if (e.kind !== "projects" || !e.extra) return null;
  return e.extra.startsWith("long") ? t("lib-kb.project_long") : t("lib-kb.project_short");
}

const STATUS_LABEL: Record<string, string> = { paused: t("lib-kb.status_paused"), done: t("lib-kb.status_done"), ignored: t("lib-kb.status_ignored"), merged: t("lib-kb.status_merged") };

/** A project's status when it is not the usual "In progress". */
export function statusLabel(status?: string): string | null {
  return (status && STATUS_LABEL[status]) || null;
}

// ── lists ───────────────────────────────────────────────

function importanceRank(p?: string): number {
  const m = /^P([0-3])$/.exec(p ?? "");
  return m ? Number(m[1]) : 4;
}

/** The hub's kb.Rank: importance, then relevance, then the most recently updated. Stable. */
export function rankEntries<T extends Pick<KBEntry, "importance" | "relevance" | "updated">>(list: T[]): T[] {
  return list
    .map((e, i) => ({ e, i }))
    .sort((a, b) => {
      const d = importanceRank(a.e.importance) - importanceRank(b.e.importance);
      if (d) return d;
      if (a.e.relevance !== b.e.relevance) return b.e.relevance - a.e.relevance;
      const t = (Date.parse(b.e.updated ?? "") || 0) - (Date.parse(a.e.updated ?? "") || 0);
      return t || a.i - b.i;
    })
    .map((x) => x.e);
}

export interface EntrySection {
  key: string;
  title: string;
  items: KBEntry[];
  /** Folded until opened ("Not a project", the people nobody tagged yet). */
  fold?: boolean;
  footer?: string;
}

/**
 * A kind's list as the app shows it. Projects: "Focus" (P0 / P1), "Other active", "Done", then "Not a project" folded. Other kinds:
 * the ranked ones, then the rest (only a name in a group, no tagging yet) folded under "Not sorted yet"; with a tag, one
 * section of everything carrying it.
 */
/** Entries newest first (no time last), the order otherwise kept. */
function newestFirst(list: KBEntry[]): KBEntry[] {
  const at = (e: KBEntry) => (e.updated ? Date.parse(e.updated) : NaN);
  return [...list].sort((a, b) => (Number.isNaN(at(b)) ? -Infinity : at(b)) - (Number.isNaN(at(a)) ? -Infinity : at(a)) || 0);
}

export function groupEntries(kind: KBKind, items: KBEntry[], tag?: string): EntrySection[] {
  const list = rankEntries(items);
  let out: EntrySection[];
  if (kind === "projects") {
    const st = (e: KBEntry) => projectInfo(e)?.status;
    const active = list.filter((e) => st(e) !== "done" && st(e) !== "ignored");
    const focus = active.filter((e) => e.importance === "P0" || e.importance === "P1");
    const rest = active.filter((e) => e.importance !== "P0" && e.importance !== "P1");
    out = [
      { key: "focus", title: t("lib-kb.section_focus"), items: focus },
      { key: "active", title: focus.length ? t("lib-kb.section_other_active") : t("lib-kb.status_active"), items: rest },
      { key: "done", title: t("lib-kb.status_done"), items: list.filter((e) => st(e) === "done") },
      {
        key: "ignored",
        title: t("lib-kb.status_ignored"),
        items: list.filter((e) => st(e) === "ignored"),
        fold: true,
        footer: t("lib-kb.ignored_footer"),
      },
    ];
  } else if (kind === "topics") {
    // Topics are curated as a whole: one list, the ones with the most knowledge first after any importance set by hand.
    const n = (e: KBEntry) => topicPointCount(e) ?? 0;
    const sorted = [...items].sort((a, b) => importanceRank(a.importance) - importanceRank(b.importance) || n(b) - n(a) || a.name.localeCompare(b.name));
    out = [{ key: "all", title: tag ? `#${tag}` : t("lib-kb.section_all"), items: sorted }];
  } else if (tag) {
    out = [{ key: "tag", title: `#${tag}`, items: kind === "meetings" ? newestFirst(list) : list }];
  } else if (kind === "meetings") {
    // A meeting is looked for by when it was (#8871): the sorted ones newest first with the owner's own recordings, never
    // folded (one just ended has no importance until its summary is written), the rest folded as elsewhere.
    const open = (e: KBEntry) => !!e.importance || e.kind === "recordings";
    const ranked = newestFirst(list.filter(open));
    const rest = newestFirst(list.filter((e) => !open(e)));
    out = [
      { key: "ranked", title: t("lib-kb.section_all"), items: ranked },
      {
        key: "rest",
        title: ranked.length ? t("lib-kb.section_unsorted") : t("lib-kb.section_all"),
        items: rest,
        fold: ranked.length > 0,
        footer: ranked.length ? t("lib-kb.unsorted_footer_meetings") : undefined,
      },
    ];
  } else {
    const ranked = list.filter((e) => e.importance);
    const rest = list.filter((e) => !e.importance);
    out = [
      { key: "ranked", title: t("lib-kb.section_ranked"), items: ranked },
      {
        key: "rest",
        title: ranked.length ? t("lib-kb.section_unsorted") : t("lib-kb.section_all"),
        items: rest,
        fold: ranked.length > 0,
        footer: ranked.length ? t(`lib-kb.unsorted_footer_${kind}`) : undefined,
      },
    ];
  }
  return out.filter((s) => s.items.length);
}

/**
 * The tags a row shows: the list's filter tag left out (every row has it), duplicates and blanks dropped, at most
 * `max`. The row itself then shows as many of these as fit whole (a cut "#Manage…" says nothing).
 */
export function rowTags(e: Pick<KBEntry, "tags">, filter?: string, max = 3): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of e.tags ?? []) {
    const t = raw.trim().replace(/^#+/, "");
    const key = t.toLowerCase();
    if (!t || seen.has(key) || (filter && key === filter.toLowerCase())) continue;
    seen.add(key);
    out.push(t);
    if (out.length >= max) break;
  }
  return out;
}

/** What the tag field holds: words split on spaces or commas, a leading # dropped, each once. */
export function parseTags(input: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of input.split(/[\s,，、;；]+/)) { // i18n-ignore: full-width separators people type
    const t = raw.replace(/^#+/, "").trim();
    if (!t || seen.has(t.toLowerCase())) continue;
    seen.add(t.toLowerCase());
    out.push(t);
  }
  return out;
}

const SKIP_INITIAL = new Set([..."#@（(【[「『《〈\"'“‘"]); // i18n-ignore: punctuation skipped in names

/** A person's or chat's tile: the first character that says something, uppercased. */
export function initial(name: string): string {
  for (const c of Array.from(name)) {
    if (/\s/.test(c) || SKIP_INITIAL.has(c)) continue;
    return c.toUpperCase();
  }
  return "";
}

// ── search ──────────────────────────────────────────────

export function queryWords(q: string): string[] {
  return q.trim().toLowerCase().split(/\s+/).filter(Boolean);
}

export interface Part {
  text: string;
  hit: boolean;
}

/** The text cut into runs, every word searched for marked (case-insensitive; the hub matches lines with all of them). */
export function markParts(text: string, query: string): Part[] {
  const words = queryWords(query);
  if (!text || !words.length) return text ? [{ text, hit: false }] : [];
  // Lowercasing can change the length of a few characters; then the words are looked for as typed.
  const lower = text.toLowerCase();
  const hay = lower.length === text.length ? lower : text;
  const marked = new Array<boolean>(text.length).fill(false);
  for (const w of words) {
    for (let i = hay.indexOf(w); i >= 0; i = hay.indexOf(w, i + w.length)) {
      for (let j = i; j < i + w.length; j++) marked[j] = true;
    }
  }
  const out: Part[] = [];
  for (let i = 0; i < text.length; i++) {
    const last = out[out.length - 1];
    if (last && last.hit === marked[i]) last.text += text[i];
    else out.push({ text: text[i], hit: marked[i] });
  }
  return out;
}

export interface ResultGroup {
  kind: string;
  title: string;
  results: KBResult[];
}

/** Results by kind, the kinds in the order of their best result (the hub ranks across kinds). */
export function groupResults(results: KBResult[]): ResultGroup[] {
  const by = new Map<string, KBResult[]>();
  for (const r of results) {
    const k = r.entry.kind;
    if (!by.has(k)) by.set(k, []);
    by.get(k)!.push(r);
  }
  return [...by].map(([kind, rs]) => ({ kind, title: kindTitle(kind), results: rs }));
}

/** The day of a message line ("2026-10-30"), null for the other files. */
export function hitDay(path: string): string | null {
  return /\/messages\/(\d{4}-\d{2}-\d{2})\.md$/.exec(path)?.[1] ?? null;
}

/** The line without its Markdown list marker, JSON key and quotes. */
export function hitText(line: string): string {
  let t = line.trim();
  for (const p of ["- ", "* ", "## ", "# "]) if (t.startsWith(p)) t = t.slice(p.length);
  if (t.startsWith('"')) {
    const i = t.indexOf('": ');
    if (i > 0) t = t.slice(i + 3);
  }
  return t.replace(/^[\s,"]+|[\s,"]+$/g, "");
}

export interface MessageLine {
  time: string;
  sender: string;
  text: string;
}

/** An archived message, `- 14:03 <name>: text` (continuation lines after a newline): when, who, what. */
export function parseMessageLine(raw: string): MessageLine {
  const t = raw.startsWith("- ") ? raw.slice(2) : raw;
  const m = /^(\d\d:\d\d) ([\s\S]*)$/.exec(t);
  if (m) {
    const i = m[2].indexOf(": ");
    if (i >= 0) return { time: m[1], sender: m[2].slice(0, i), text: tidy(m[2].slice(i + 2)) };
  }
  return { time: "", sender: "", text: tidy(t) };
}

function tidy(s: string) {
  return s.replace(/\n[ \t]+/g, "\n").trim();
}

/** A found line said plainly: where it is ("Profile", "Timeline", "Chat history" · day and time) and what it says. */
export function hitView(f: Pick<KBFound, "path" | "line">, now = new Date()): { source: string; text: string; day: string | null } {
  const day = hitDay(f.path);
  if (day) {
    const m = parseMessageLine(hitText(f.line));
    return { source: t("lib-kb.hit_chat", { day: `${dayTitle(day, now)}${m.time ? ` ${m.time}` : ""}` }), text: m.sender ? t("lib-kb.hit_sender", { sender: m.sender, text: m.text }) : m.text, day };
  }
  const file = f.path.split("/").pop() ?? f.path;
  const source = file === "summary.md" ? t("lib-kb.hit_profile") : file === "log.md" ? t("lib-kb.hit_timeline") : file === "transcript.md" ? t("lib-kb.hit_transcript") : file === "minutes.md" ? t("lib-kb.hit_minutes") : file === "recap.md" ? t("lib-kb.hit_recap") : file.endsWith(".json") ? t("lib-kb.hit_data") : file;
  return { source, text: hitText(f.line), day: null };
}

// ── detail ──────────────────────────────────────────────

export interface LogLine {
  when: string;
  source: string;
  text: string;
}

/** A timeline line, `- 2026-10-30 23:16 · Agent · <title> · <machine> · <what was said>`: when, from where, what. */
export function parseLogLine(raw: string): LogLine {
  let t = raw.trim();
  if (t.startsWith("- ")) t = t.slice(2);
  const parts = t.split(" · ");
  if (parts.length >= 2 && /^\d/.test(parts[0])) {
    return { when: parts[0], source: parts.slice(1, -1).join(" · "), text: parts[parts.length - 1] };
  }
  return { when: "", source: "", text: t };
}

/** The timeline newest first (the hub keeps it oldest first). */
export function timeline(log?: string[] | null): LogLine[] {
  return (log ?? []).filter((l) => l.trim()).map(parseLogLine).reverse();
}

/** "2026-10-30 23:16" → "10-30 23:16"; the year stays when it is not this one. */
export function shortWhen(when: string, now = new Date()): string {
  const y = `${now.getFullYear()}-`;
  return when.startsWith(y) ? when.slice(y.length) : when;
}

export function dropTitle(md: string): string {
  if (!md.startsWith("# ")) return md;
  const i = md.indexOf("\n");
  return i < 0 ? "" : md.slice(i + 1);
}

/**
 * The profile without what the header already shows: its title, a heading that is just the name, the one line, a
 * person's generated tagging line ("importance P0 · relevance to me 95 · …"). Empty means there is nothing more to show.
 */
export function profile(md: string | undefined, oneLine?: string, name?: string): string {
  if (!md) return "";
  const one = oneLine?.trim() ?? "";
  const title = name?.trim() ?? "";
  return dropTitle(md)
    .split("\n")
    .filter((line) => {
      const t = line.trim();
      if (title && /^#{1,6} /.test(t) && t.replace(/^#+ /, "").trim() === title) return false;
      // i18n-ignore: the importance line the hub writes into a profile
      return !(t.startsWith("重要级 ") && t.includes("相关性")) && (!one || t !== one);
    })
    .join("\n")
    .trim();
}

function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

/** The entry as last loaded: a linked row, a search hit or an address starts with only its id or name. */
export function entryFromDetail(base: KBEntry | undefined, d: KBDetail): KBEntry {
  const e: KBEntry = { ...(base ?? { kind: d.kind, id: d.id, name: d.id, relevance: 0 }) };
  const m = d.meta;
  if (!m) return e;
  const name = str(m.name) || str(m.title);
  if (name) e.name = name;
  if (str(m.importance) !== undefined) e.importance = str(m.importance);
  if (typeof m.relevance === "number") e.relevance = m.relevance;
  if (Array.isArray(m.tags)) e.tags = m.tags.filter((t): t is string => typeof t === "string");
  const line = str(m.one_line) || str(m.note);
  if (line) e.one_line = line;
  if (e.kind === "projects" && str(m.kind)) e.extra = `${str(m.kind)} · ${str(m.status) || "active"}`;
  if (e.kind === "topics" && typeof m.points === "number") e.extra = String(m.points);
  return e;
}

export interface LinkGroup {
  kind: KBKind;
  title: string;
  items: KBEntry[];
}

/** Related entries by kind, in the kinds' order. */
export function linkGroups(links?: KBEntry[] | null): LinkGroup[] {
  return KB_KINDS.map((kind) => ({ kind, title: t(`lib-kb.related_${kind}`), items: (links ?? []).filter((e) => e.kind === kind) })).filter((g) => g.items.length);
}

/** A project's code directories, `<machine>:<path>`. */
export function dirsOf(meta?: Record<string, unknown> | null): { machine: string; path: string }[] {
  const dirs = Array.isArray(meta?.dirs) ? meta.dirs : [];
  return dirs
    .filter((d): d is string => typeof d === "string" && d.trim() !== "")
    .map((d) => {
      const i = d.indexOf(":");
      return i > 0 && /^[/~]/.test(d.slice(i + 1)) ? { machine: d.slice(0, i), path: d.slice(i + 1) } : { machine: "", path: d };
    });
}

export interface Facts {
  at?: string;
  url?: string;
  whenLabel: string;
  openLabel: string;
}

/** A doc's, meeting's or event's when and link; null for the other kinds or when it has neither. */
export function factsOf(kind: string, meta?: Record<string, unknown> | null): Facts | null {
  if (kind !== "docs" && kind !== "meetings" && kind !== "events") return null;
  const at = str(meta?.at);
  const ms = at ? Date.parse(at) : NaN;
  const url = str(meta?.url)?.trim();
  const f: Facts = {
    at: Number.isFinite(ms) && ms > 0 ? at : undefined, // Go's zero time is none
    url: url || undefined,
    whenLabel: kind === "docs" ? t("lib-kb.when_doc") : kind === "meetings" ? t("lib-kb.when_meeting") : t("lib-kb.when_event"),
    openLabel: kind === "docs" ? t("lib-kb.open_doc") : kind === "meetings" ? t("lib-kb.open_meeting") : t("lib-kb.open_event"),
  };
  return f.at || f.url ? f : null;
}

// A formatter per zone, null for a zone this browser can't read (then the clock is local).
const zoneFormats = new Map<string, Intl.DateTimeFormat | null>();

function zoneFormat(tz: string): Intl.DateTimeFormat | null {
  let f = zoneFormats.get(tz);
  if (f === undefined) {
    try {
      f = new Intl.DateTimeFormat("en-US", { timeZone: tz, hourCycle: "h23", year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "numeric", second: "numeric" });
    } catch {
      f = null;
    }
    zoneFormats.set(tz, f);
  }
  return f;
}

/** Whether this browser can show times in `tz`, an IANA zone name. */
export function knownZone(tz: unknown): tz is string {
  return typeof tz === "string" && tz.trim() !== "" && zoneFormat(tz) !== null;
}

// A zone's offset changes only at quarter-hour instants, so one formatter call per zone and quarter hour serves every
// line in it: a day's transcript has up to 20000.
const QUARTER_HOUR = 900_000;
const zoneOffsets = new Map<string, number>();

/** An instant's hour, minute and second on the clock of `tz`, or of this browser's zone. */
export function wallClock(d: Date, tz?: string): { h: number; m: number; s: number } {
  const f = tz ? zoneFormat(tz) : null;
  if (!f) return { h: d.getHours(), m: d.getMinutes(), s: d.getSeconds() };
  const quarter = Math.floor(d.getTime() / QUARTER_HOUR);
  const key = `${tz}\u0000${quarter}`;
  let offset = zoneOffsets.get(key);
  if (offset === undefined) {
    const at = quarter * QUARTER_HOUR;
    const parts = f.formatToParts(at);
    const part = (type: string) => Number(parts.find((p) => p.type === type)?.value ?? 0);
    offset = Date.UTC(part("year"), part("month") - 1, part("day"), part("hour") % 24, part("minute"), part("second")) - at;
    if (zoneOffsets.size >= 4096) zoneOffsets.clear();
    zoneOffsets.set(key, offset);
  }
  const wall = new Date(d.getTime() + offset);
  return { h: wall.getUTCHours(), m: wall.getUTCMinutes(), s: wall.getUTCSeconds() };
}

const pad = (n: number) => String(n).padStart(2, "0");
// `zone`: the date in that zone (an all-day recording's), else in this browser's.
const inZone = (zone?: string) => (zone ? { timeZone: zone } : {});
const monthDay = (d: Date, zone?: string) => formatDate(d, { month: "short", day: "numeric", ...inZone(zone) });
const yearMonthDay = (d: Date, zone?: string) => formatDate(d, { year: "numeric", month: "short", day: "numeric", ...inZone(zone) });
const weekday = (d: Date, zone?: string) => formatDate(d, { weekday: "short", ...inZone(zone) });

/** "Fri, Oct 30 14:00" (24-hour), with the year instead of the weekday when it is not this one; in `tz` when given and
 *  known (an all-day recording's zone), else local. */
export function formatAt(iso: string, now = new Date(), tz?: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const zone = tz && knownZone(tz) ? tz : undefined;
  const c = wallClock(d, zone);
  const hm = `${pad(c.h)}:${pad(c.m)}`;
  return d.getFullYear() === now.getFullYear()
    ? t("lib-kb.at_this_year", { date: monthDay(d, zone), weekday: weekday(d, zone), time: hm })
    : t("lib-kb.at_other_year", { date: yearMonthDay(d, zone), time: hm });
}

/** A span the hub sends as `start` / `end` (RFC 3339) in this app's dates, as `formatAt` says a time (#4713): "Fri, Oct 30
 *  14:00–15:10", with the end's date too when it is another day. `fallback` (the hub's fixed-format `when`) when either
 *  is missing or unreadable: a hub from before them. */
export function formatSpan(start: string | undefined, end: string | undefined, fallback = "", now = new Date()): string {
  const s = new Date(start ?? "");
  const e = new Date(end ?? "");
  if (!start || !end || Number.isNaN(s.getTime()) || Number.isNaN(e.getTime())) return fallback;
  const sameDay = s.getFullYear() === e.getFullYear() && s.getMonth() === e.getMonth() && s.getDate() === e.getDate();
  return sameDay ? `${formatAt(start, now)}–${pad(e.getHours())}:${pad(e.getMinutes())}` : `${formatAt(start, now)} – ${formatAt(end, now)}`;
}

/** "2026-10-30" → "Fri, Oct 30", with the year when it is not this one. */
export function dayTitle(day: string, now = new Date()): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  if (!m) return day;
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  return t(d.getFullYear() === now.getFullYear() ? "lib-kb.day_this_year" : "lib-kb.day_other_year", { date: d.getFullYear() === now.getFullYear() ? monthDay(d) : yearMonthDay(d), weekday: weekday(d) });
}

export interface RecentDay {
  /** null when the hub sent text without day headings. */
  day: string | null;
  messages: MessageLine[];
}

/** A chat's last 7 days (`recent`: "## 2026-10-30" then "- 10:15 <name>: …" lines, continuations indented) by day. */
export function parseRecent(text?: string): RecentDay[] {
  const out: RecentDay[] = [];
  let raw: string[] = [];
  let cur: RecentDay | null = null;
  const flush = () => {
    if (cur && raw.length) cur.messages.push(parseMessageLine(raw.join("\n")));
    raw = [];
  };
  for (const line of (text ?? "").split("\n")) {
    const h = /^## (\d{4}-\d{2}-\d{2})\s*$/.exec(line);
    if (h) {
      flush();
      cur = { day: h[1], messages: [] };
      out.push(cur);
    } else if (line.startsWith("- ")) {
      flush();
      if (!cur) out.push((cur = { day: null, messages: [] }));
      raw = [line];
    } else if (/^\s/.test(line) && raw.length) {
      raw.push(line);
    } else if (line.trim() && line.trim() !== "…") {
      flush();
      if (!cur) out.push((cur = { day: null, messages: [] }));
      raw = [line];
    }
  }
  flush();
  return out.filter((d) => d.messages.length);
}

/** What a row of a chat's recent messages goes by: its day and the row as parseMessageLine reads it. */
export function messageRowKey(day: string | null, m: MessageLine): string {
  return [day ?? "", m.time, m.sender, m.text].join("\u0000");
}

/** The Inbox item each row of a chat's recent messages belongs to, by messageRowKey: a row an item's `messages` names,
 *  read as parseRecent reads it (the first item naming it, the newest, when two do). */
export function inboxOfRows(items: readonly KBInboxItem[] | null | undefined): Map<string, KBInboxItem> {
  const out = new Map<string, KBInboxItem>();
  for (const it of items ?? []) {
    for (const m of it.messages ?? []) {
      const key = messageRowKey(m.day, parseMessageLine(m.line));
      if (!out.has(key)) out.set(key, it);
    }
  }
  return out;
}

// ── Ask Persona ────────────────────────────────────────

/**
 * `<persona-context kind="…" title="…" url="…">text</persona-context>`, attributes in this order: the app's parser
 * (ContextRef) expects all three, so the card shows in the conversation on the phone too.
 */
export function contextXml(c: { kind: string; title: string; url?: string; text: string }): string {
  const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return `<persona-context kind="${esc(c.kind)}" title="${esc(c.title)}" url="${esc(c.url ?? "")}">\n${contextBody(c.text)}\n</persona-context>`;
}

/** An entry as context: its profile, recent timeline and folder. */
function entryText(e: Pick<KBEntry, "one_line">, d?: Pick<KBDetail, "summary" | "log" | "path"> | null): string {
  let text = d?.summary || e.one_line || "";
  if (d?.log?.length) text += `\n\n${t("lib-kb.context_timeline")}\n${d.log.slice(-10).join("\n")}`;
  if (d?.path) text += `\n\n${t("lib-kb.context_folder", { path: d.path })}`;
  return text.trim();
}

/** What "Ask Persona" sends to the main conversation: the entry's profile, recent timeline and folder, then the ask. */
export function askPersonaText(e: Pick<KBEntry, "name" | "one_line">, d?: Pick<KBDetail, "summary" | "log" | "path"> | null): string {
  return `${contextXml({ kind: "context", title: t("lib-kb.context_title", { name: e.name }), url: "", text: entryText(e, d) })}\n\n${t("lib-kb.ask_persona", { name: e.name })}`;
}

/**
 * What "Start agent" starts a new agent with (KBEntryView.agentPrompt): the entry as context, the ask left to write; and the
 * entry's first code folder on a computer that is online, if it has one.
 */
export function agentPreset(
  kind: string,
  e: Pick<KBEntry, "name" | "one_line">,
  d: Pick<KBDetail, "summary" | "log" | "path" | "meta"> | null | undefined,
  online: string[],
): { prompt: string; machineId?: string; cwd?: string } {
  const prompt = `${contextXml({ kind: "context", title: `${kindTitle(kind)} · ${e.name}`, url: "", text: entryText(e, d) })}\n\n`;
  const dir = dirsOf(d?.meta).find((x) => x.machine && online.includes(x.machine));
  return dir ? { prompt, machineId: dir.machine, cwd: dir.path } : { prompt };
}

// ── Tagging ──────────────────────────────────────────────

export const IMPORTANCE = ["P0", "P1", "P2", "P3"] as const;
export const PROJECT_KINDS = [
  { value: "short", label: t("lib-kb.project_short_detail") },
  { value: "long", label: t("lib-kb.project_long_detail") },
] as const;
export const PROJECT_STATUSES = [
  { value: "active", label: t("lib-kb.status_active") },
  { value: "paused", label: t("lib-kb.status_paused") },
  { value: "done", label: t("lib-kb.status_done") },
  { value: "ignored", label: t("lib-kb.status_ignored") },
] as const;

export interface TagForm {
  importance: string;
  relevance: number;
  tags: string;
  projectKind: "short" | "long";
  status: string;
}

export function tagForm(e: KBEntry): TagForm {
  const p = projectInfo(e);
  return {
    importance: IMPORTANCE.includes(e.importance as (typeof IMPORTANCE)[number]) ? e.importance! : "P2",
    relevance: Math.max(0, Math.min(100, Math.round(e.relevance || 0))),
    tags: (e.tags ?? []).join(" "),
    projectKind: p?.kind ?? "short",
    status: p?.status === "merged" ? "active" : (p?.status ?? "active"),
  };
}

/** The PUT body; a project's kind and status only for projects. */
export function tagUpdate(kind: string, f: TagForm): KBUpdate {
  const u: KBUpdate = { importance: f.importance, relevance: Math.max(0, Math.min(100, Math.round(f.relevance))), tags: parseTags(f.tags) };
  if (kind === "projects") {
    u.status = f.status;
    u.project_kind = f.projectKind;
  }
  return u;
}
