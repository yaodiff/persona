// Pure speaker display-name and roster edit rules (#17674, #17675, docs/assistant/meeting-minutes.md "Speakers
// (#17674)"), verified against the shared fixture spec/fixtures/speakers-names/cases.json. Used both for disagreement
// attribution and for immediate local-first edits before reconciling with POST /api/v1/recordings/{id}/speakers.
import { t } from "../../i18n";
import {
  canonicalSpeakerId,
  decodeSpeakers,
  groupLinesBySpeaker,
  hasVisibleSpeakers,
  isValidSpeakerId,
  speakerAvatarGlyph,
  speakerLabel,
  speakerNumber,
  waitingSpeakerMinutes,
  type DetailSpeakers,
  type SpeakerFailReason,
  type SpeakerPerson,
  type SpeakerQuality,
  type SpeakerState,
  type TranscriptSpeakerGroup,
} from "../../lib/recording-speakers";
import { transcriptLineId, type RecordingView } from "./recording-view";
import type { DebatePosition, MinutesDebate } from "./minutes-debates";

export {
  canonicalSpeakerId,
  decodeSpeakers,
  groupLinesBySpeaker,
  hasVisibleSpeakers,
  isValidSpeakerId,
  speakerAvatarGlyph,
  speakerLabel,
  speakerNumber,
  waitingSpeakerMinutes,
  type DetailSpeakers,
  type SpeakerFailReason,
  type SpeakerPerson,
  type SpeakerQuality,
  type SpeakerState,
  type TranscriptSpeakerGroup,
};

export const MINOR_THRESHOLD_SECONDS = 20.0;
export const MAX_SPEAKER_NAME_RUNES = 100;
const FLOAT_EPS = 1e-9;

export type PositionDisplay =
  | { kind: "name"; value: string }
  | { kind: "numbered"; number: number }
  | { kind: "who"; value: string };

export interface PositionAttributionInput {
  speaker?: string;
  speaker_unsure?: boolean;
  who?: string;
  also?: readonly { speaker?: string; speaker_unsure?: boolean }[];
}

export type SpeakerEditOp =
  | { op: "name"; id: string; name: string; me?: boolean }
  | { op: "merge"; from: string; into: string }
  | { op: "unmerge"; id: string }
  | { op: "clear" };

export interface SpeakerEdits {
  name?: number;
  merge?: number;
  unmerge?: number;
  clear?: number;
}

export interface RosterPerson {
  id: string;
  name?: string;
  me?: boolean;
  merged_into?: string;
  minor?: boolean;
}

export interface SpeakerRoster {
  next: number;
  people: RosterPerson[];
  edits?: SpeakerEdits;
}

export type SpeakerStatsMap = Record<string, readonly [number, number]>;

export type SpeakerEditResult =
  | {
      ok: true;
      roster: SpeakerRoster;
      undo: SpeakerEditOp[];
      cleared: boolean;
    }
  | {
      ok: false;
      error: "invalid" | "refused" | "missing";
      error_code: string;
    };

/** Resolves a disagreement position's display names by the shared contract table (spec/fixtures/speakers-names):
 *  1) sure named speaker -> owner name; 2) sure unnamed speaker -> Speaker N; 3) model-written `who`; 4) nothing.
 *  When `quality` is `"low"`, no position is attributed to a speaker (transcript `who` still falls back per row 3). */
export function resolvePositionDisplays(
  people: readonly Pick<SpeakerPerson, "id" | "name" | "merged_into" | "minor">[],
  position: PositionAttributionInput,
  quality: SpeakerQuality = "ok",
): PositionDisplay[] {
  const byId = new Map(people.map((p) => [p.id, p]));
  const lowQuality = quality === "low";
  const resolveSure = (
    speaker?: string,
    unsure?: boolean,
  ): { canon: string; display: PositionDisplay } | null => {
    if (lowQuality || !isValidSpeakerId(speaker) || unsure === true) return null;
    const canon = canonicalSpeakerId(people, speaker);
    const person = byId.get(canon);
    const name = person?.name?.trim();
    if (name) return { canon, display: { kind: "name", value: name } };
    return { canon, display: { kind: "numbered", number: speakerNumber(canon) } };
  };

  const out: PositionDisplay[] = [];
  const seenCanon = new Set<string>();
  const main = resolveSure(position.speaker, position.speaker_unsure);
  if (main) {
    out.push(main.display);
    seenCanon.add(main.canon);
  }

  const alsoDisplays: PositionDisplay[] = [];
  for (const entry of position.also ?? []) {
    const resolved = resolveSure(entry.speaker, entry.speaker_unsure);
    if (!resolved || seenCanon.has(resolved.canon)) continue;
    seenCanon.add(resolved.canon);
    alsoDisplays.push(resolved.display);
  }

  if (!main) {
    const who = position.who?.trim();
    if (who && !alsoDisplays.some((d) => d.kind === "name" && d.value === who)) {
      out.push({ kind: "who", value: who });
    }
  }
  out.push(...alsoDisplays);
  return out;
}

/** Formats resolved position displays into a localized attribution string (or empty string when none). */
export function formatPositionDisplays(displays: readonly PositionDisplay[]): string {
  if (!displays.length) return "";
  const parts = displays.map((d) =>
    d.kind === "numbered" ? t("recording.speaker_n", { n: d.number }) : d.value,
  );
  return parts.join(t("recording.speakers_sep"));
}

/** Counts Unicode code points (matching Go's rune count). */
export function runeLength(value: string): number {
  return Array.from(value).length;
}

function normRosterPerson(p: RosterPerson): RosterPerson {
  const out: RosterPerson = { id: p.id };
  if (p.name) out.name = p.name;
  if (p.me) out.me = true;
  if (p.merged_into) out.merged_into = p.merged_into;
  if (p.minor) out.minor = true;
  return out;
}

function nameUndoOp(id: string, name: string, me: boolean): SpeakerEditOp {
  return me ? { op: "name", id, name, me: true } : { op: "name", id, name };
}

function mergedSeconds(stats: SpeakerStatsMap, people: readonly RosterPerson[]): Map<string, number> {
  const secs = new Map<string, number>();
  for (const [sid, st] of Object.entries(stats)) {
    const canon = canonicalSpeakerId(people, sid);
    secs.set(canon, (secs.get(canon) ?? 0) + Number(st[0] || 0));
  }
  return secs;
}

function applyMinorMarks(stats: SpeakerStatsMap, people: RosterPerson[]): void {
  const secs = mergedSeconds(stats, people);
  for (const p of people) {
    if (p.merged_into) {
      delete p.minor;
      continue;
    }
    if ((secs.get(p.id) ?? 0) + FLOAT_EPS < MINOR_THRESHOLD_SECONDS) {
      p.minor = true;
    } else {
      delete p.minor;
    }
  }
}

function validateNameInput(
  raw: unknown,
): { ok: true; value: string } | { ok: false; error: "invalid"; error_code: string } {
  if (typeof raw !== "string") {
    return { ok: false, error: "invalid", error_code: "recordingspeakers.name.invalid" };
  }
  if (raw.includes("\n") || raw.includes("\r")) {
    return { ok: false, error: "invalid", error_code: "recordingspeakers.name.multiline" };
  }
  const trimmed = raw.trim();
  if (!trimmed) return { ok: true, value: "" };
  if (runeLength(trimmed) > MAX_SPEAKER_NAME_RUNES) {
    return { ok: false, error: "invalid", error_code: "recordingspeakers.name.too_long" };
  }
  return { ok: true, value: trimmed };
}

/** Applies speaker edit operations atomically, matching `server/internal/recordingspeakers/rules.go` and
 *  `spec/fixtures/speakers-names/cases.json`. */
export function applySpeakerEdit(
  roster: SpeakerRoster,
  stats: SpeakerStatsMap,
  ops: readonly SpeakerEditOp[],
  ownerName = "",
): SpeakerEditResult {
  if (!Array.isArray(ops) || ops.length === 0) {
    return { ok: false, error: "invalid", error_code: "recordingspeakers.ops.empty" };
  }
  let people: RosterPerson[] = roster.people.map(normRosterPerson);
  const edits: SpeakerEdits = { ...(roster.edits ?? {}) };
  const next = roster.next > 0 ? roster.next : 1;
  const undoGroups: SpeakerEditOp[][] = [];
  let cleared = false;

  for (const rawOp of ops) {
    if (!rawOp || typeof rawOp !== "object") {
      return { ok: false, error: "invalid", error_code: "recordingspeakers.op.unknown" };
    }
    if (rawOp.op === "name") {
      if (!isValidSpeakerId(rawOp.id)) {
        return { ok: false, error: "invalid", error_code: "recordingspeakers.speaker.format" };
      }
      const idx = people.findIndex((p) => p.id === rawOp.id);
      if (idx < 0) {
        return { ok: false, error: "missing", error_code: "recordingspeakers.speaker.not_found" };
      }
      if (people[idx]!.merged_into) {
        return { ok: false, error: "refused", error_code: "recordingspeakers.speaker.merged" };
      }
      const me = rawOp.me === true;
      let rawName: unknown = rawOp.name ?? "";
      if (
        me &&
        typeof rawName === "string" &&
        rawName.trim() === "" &&
        !rawName.includes("\n") &&
        !rawName.includes("\r") &&
        ownerName.trim()
      ) {
        rawName = ownerName.trim();
      }
      const checked = validateNameInput(rawName);
      if (!checked.ok) return checked;
      const targetName = checked.value;
      const oldName = people[idx]!.name ?? "";
      const oldMe = people[idx]!.me === true;

      let otherIdx = -1;
      if (targetName) {
        otherIdx = people.findIndex(
          (p, i) => i !== idx && !p.merged_into && p.name === targetName,
        );
      }
      let prevOwnerIdx = -1;
      if (me) {
        prevOwnerIdx = people.findIndex(
          (p, i) => i !== idx && i !== otherIdx && !p.merged_into && p.me === true,
        );
      }

      if (otherIdx < 0) {
        const ug: SpeakerEditOp[] = [];
        if (prevOwnerIdx >= 0) {
          const po = people[prevOwnerIdx]!;
          delete po.me;
          ug.push(nameUndoOp(po.id, po.name ?? "", true));
        }
        if (oldName !== targetName || oldMe !== me) {
          if (targetName) people[idx]!.name = targetName;
          else delete people[idx]!.name;
          if (me) people[idx]!.me = true;
          else delete people[idx]!.me;
          ug.unshift(nameUndoOp(rawOp.id, oldName, oldMe));
        }
        if (ug.length) undoGroups.push(ug);
      } else {
        const secs = mergedSeconds(stats, people);
        const pSec = secs.get(people[idx]!.id) ?? 0;
        const oSec = secs.get(people[otherIdx]!.id) ?? 0;
        const [fromIdx, intoIdx] = pSec > oSec + FLOAT_EPS ? [otherIdx, idx] : [idx, otherIdx];
        const fromId = people[fromIdx]!.id;
        const intoId = people[intoIdx]!.id;
        const otherOldMe = people[otherIdx]!.me === true;
        const children = people.filter((p) => p.merged_into === fromId).map((p) => p.id);
        const mergedMe = me || oldMe || otherOldMe;

        let prevOwnerRestore: SpeakerEditOp | null = null;
        if (prevOwnerIdx >= 0) {
          const po = people[prevOwnerIdx]!;
          delete po.me;
          prevOwnerRestore = nameUndoOp(po.id, po.name ?? "", true);
        }

        people[intoIdx]!.name = targetName;
        if (mergedMe) people[intoIdx]!.me = true;
        else delete people[intoIdx]!.me;
        delete people[intoIdx]!.merged_into;

        delete people[fromIdx]!.name;
        delete people[fromIdx]!.me;
        delete people[fromIdx]!.minor;
        people[fromIdx]!.merged_into = intoId;
        for (const p of people) {
          if (p.merged_into === fromId) p.merged_into = intoId;
        }

        const ug: SpeakerEditOp[] = [];
        if (fromIdx === idx) {
          ug.push({ op: "unmerge", id: fromId });
          for (const cid of children) {
            ug.push({ op: "unmerge", id: cid }, { op: "merge", from: cid, into: fromId });
          }
          if (oldName || oldMe) ug.push(nameUndoOp(fromId, oldName, oldMe));
          if (mergedMe !== otherOldMe) ug.push(nameUndoOp(intoId, targetName, otherOldMe));
        } else {
          ug.push(nameUndoOp(intoId, oldName, oldMe));
          ug.push({ op: "unmerge", id: fromId });
          for (const cid of children) {
            ug.push({ op: "unmerge", id: cid }, { op: "merge", from: cid, into: fromId });
          }
          ug.push(nameUndoOp(fromId, targetName, otherOldMe));
        }
        if (prevOwnerRestore) ug.push(prevOwnerRestore);
        undoGroups.push(ug);
      }
      edits.name = (edits.name ?? 0) + 1;
    } else if (rawOp.op === "merge") {
      if (!isValidSpeakerId(rawOp.from) || !isValidSpeakerId(rawOp.into)) {
        return { ok: false, error: "invalid", error_code: "recordingspeakers.speaker.format" };
      }
      const fromIdx = people.findIndex((p) => p.id === rawOp.from);
      const intoIdx = people.findIndex((p) => p.id === rawOp.into);
      if (fromIdx < 0 || intoIdx < 0) {
        return { ok: false, error: "missing", error_code: "recordingspeakers.speaker.not_found" };
      }
      if (people[fromIdx]!.merged_into) {
        return { ok: false, error: "refused", error_code: "recordingspeakers.speaker.already_merged" };
      }
      const targetId = canonicalSpeakerId(people, rawOp.into);
      if (targetId === rawOp.from) {
        return { ok: false, error: "invalid", error_code: "recordingspeakers.merge.same_speaker" };
      }
      const fromOldName = people[fromIdx]!.name ?? "";
      const fromOldMe = people[fromIdx]!.me === true;
      const children = people.filter((p) => p.merged_into === rawOp.from).map((p) => p.id);

      people[fromIdx]!.merged_into = targetId;
      delete people[fromIdx]!.name;
      delete people[fromIdx]!.me;
      delete people[fromIdx]!.minor;
      for (const p of people) {
        if (p.merged_into === rawOp.from) p.merged_into = targetId;
      }

      const ug: SpeakerEditOp[] = [{ op: "unmerge", id: rawOp.from }];
      for (const cid of children) {
        ug.push({ op: "unmerge", id: cid }, { op: "merge", from: cid, into: rawOp.from });
      }
      if (fromOldName || fromOldMe) ug.push(nameUndoOp(rawOp.from, fromOldName, fromOldMe));
      undoGroups.push(ug);
      edits.merge = (edits.merge ?? 0) + 1;
    } else if (rawOp.op === "unmerge") {
      if (!isValidSpeakerId(rawOp.id)) {
        return { ok: false, error: "invalid", error_code: "recordingspeakers.speaker.format" };
      }
      const idx = people.findIndex((p) => p.id === rawOp.id);
      if (idx < 0) {
        return { ok: false, error: "missing", error_code: "recordingspeakers.speaker.not_found" };
      }
      const prevTarget = people[idx]!.merged_into;
      if (!prevTarget) {
        return { ok: false, error: "refused", error_code: "recordingspeakers.speaker.not_merged" };
      }
      delete people[idx]!.merged_into;
      delete people[idx]!.name;
      delete people[idx]!.me;
      undoGroups.push([{ op: "merge", from: rawOp.id, into: prevTarget }]);
      edits.unmerge = (edits.unmerge ?? 0) + 1;
    } else if (rawOp.op === "clear") {
      people = [];
      undoGroups.length = 0;
      cleared = true;
      edits.clear = (edits.clear ?? 0) + 1;
    } else {
      return { ok: false, error: "invalid", error_code: "recordingspeakers.op.unknown" };
    }
  }

  applyMinorMarks(stats, people);
  const undo: SpeakerEditOp[] = [];
  for (let i = undoGroups.length - 1; i >= 0; i--) {
    undo.push(...undoGroups[i]!);
  }
  const cleanEdits: SpeakerEdits = {};
  if (edits.name) cleanEdits.name = edits.name;
  if (edits.merge) cleanEdits.merge = edits.merge;
  if (edits.unmerge) cleanEdits.unmerge = edits.unmerge;
  if (edits.clear) cleanEdits.clear = edits.clear;

  return {
    ok: true,
    roster: { next, people: people.map(normRosterPerson), edits: cleanEdits },
    undo,
    cleared,
  };
}

/** Optimistically applies `ops` to a `DetailSpeakers` snapshot so the transcript, minutes and naming page update on the
 *  same frame before the hub responds. The hub's response remains authoritative and replaces this snapshot on return. */
export function applyOptimisticSpeakerEdit(
  current: DetailSpeakers,
  ops: readonly SpeakerEditOp[],
  ownerName = "",
): { speakers: DetailSpeakers; undo: SpeakerEditOp[] } | null {
  const stats: SpeakerStatsMap = {};
  let maxNum = 0;
  for (const p of current.people) {
    if (isValidSpeakerId(p.id)) maxNum = Math.max(maxNum, speakerNumber(p.id));
    if (!p.merged_into) {
      stats[p.id] = [p.seconds ?? 0, p.lines ?? 0];
    } else {
      // Preserve a non-minor default for an unmerged speaker whose original duration isn't in DetailSpeakers until hub answers.
      stats[p.id] = [MINOR_THRESHOLD_SECONDS, 0];
    }
  }
  const result = applySpeakerEdit(
    { next: maxNum + 1, people: current.people },
    stats,
    ops,
    ownerName,
  );
  if (!result.ok) return null;

  if (result.cleared) {
    return {
      speakers: {
        ...current,
        state: "cleared",
        reason: "unknown",
        quality: "ok",
        people: [],
        spans: [],
        covered: [],
      },
      undo: [],
    };
  }

  const prevById = new Map(current.people.map((p) => [p.id, p]));
  const nextPeople: SpeakerPerson[] = result.roster.people.map((rp) => {
    const prev = prevById.get(rp.id);
    const out: SpeakerPerson = { ...rp };
    if (!rp.merged_into) {
      // Sum stats from this speaker and any newly absorbed unmerged speaker.
      let seconds = prev && !prev.merged_into ? (prev.seconds ?? 0) : 0;
      let lines = prev && !prev.merged_into ? (prev.lines ?? 0) : 0;
      const samples = new Set<number>(prev?.samples ?? []);
      for (const other of result.roster.people) {
        if (other.merged_into !== rp.id) continue;
        const prevOther = prevById.get(other.id);
        if (prevOther && !prevOther.merged_into) {
          seconds += prevOther.seconds ?? 0;
          lines += prevOther.lines ?? 0;
          for (const s of prevOther.samples ?? []) samples.add(s);
        }
      }
      if (seconds > 0 || prev?.seconds !== undefined) out.seconds = seconds;
      if (lines > 0 || prev?.lines !== undefined) out.lines = lines;
      const sortedSamples = [...samples].sort((a, b) => a - b).slice(0, 3);
      if (sortedSamples.length) out.samples = sortedSamples;
    }
    return out;
  });

  return {
    speakers: {
      ...current,
      people: nextPeople,
    },
    undo: result.undo,
  };
}

/** One-tap candidate names for naming `person`: combines hub `candidates` and other unmerged speakers' names (for
 *  one-tap merge), deduplicated and omitting `person`'s current name. */
export function speakerCandidateNames(
  person: Pick<SpeakerPerson, "id" | "name">,
  speakers: Pick<DetailSpeakers, "people" | "candidates">,
): string[] {
  const current = person.name?.trim() ?? "";
  const seen = new Set<string>(current ? [current] : []);
  const out: string[] = [];
  for (const raw of speakers.candidates) {
    const name = raw.trim();
    if (!name || seen.has(name)) continue;
    seen.add(name);
    out.push(name);
  }
  for (const other of speakers.people) {
    if (other.id === person.id || other.merged_into) continue;
    const name = other.name?.trim();
    if (!name || seen.has(name)) continue;
    seen.add(name);
    out.push(name);
  }
  return out;
}

export interface SpeakerSampleLine {
  id: string;
  at: number;
  clock: string;
  text: string;
}

/** Resolves `person.samples` start timestamps against the recording's transcript lines. */
export function speakerSampleLines(
  person: Pick<SpeakerPerson, "samples">,
  lines: RecordingView["lines"],
): SpeakerSampleLine[] {
  if (!person.samples?.length || !lines.length) return [];
  const spoken = lines.filter((l) => !l.bookmark);
  if (!spoken.length) return [];
  const seen = new Set<string>();
  const out: SpeakerSampleLine[] = [];
  for (const at of person.samples) {
    const exact = spoken.find((l) => Math.abs(l.seconds - at) < 0.01)
      ?? spoken.findLast((l) => l.seconds <= at)
      ?? spoken[0];
    if (!exact) continue;
    const id = transcriptLineId(exact.seconds);
    if (seen.has(id)) continue;
    seen.add(id);
    out.push({ id, at: exact.seconds, clock: exact.clock, text: exact.text });
  }
  return out;
}

export interface AttributedPosition {
  question: string;
  stance: string;
  at?: number;
}

/** Disagreement positions sure-attributed to `speakerId` (main or `also`, resolving merges); empty on `quality === "low"`. */
export function speakerPositions(
  debates: readonly MinutesDebate[],
  people: readonly Pick<SpeakerPerson, "id" | "merged_into">[],
  speakerId: string,
  quality: SpeakerQuality = "ok",
): AttributedPosition[] {
  if (quality === "low" || !debates.length) return [];
  const target = canonicalSpeakerId(people, speakerId);
  const out: AttributedPosition[] = [];
  const matches = (pos: DebatePosition): boolean => {
    if (isValidSpeakerId(pos.speaker) && !pos.speaker_unsure && canonicalSpeakerId(people, pos.speaker) === target) {
      return true;
    }
    return (pos.also ?? []).some(
      (a) => isValidSpeakerId(a.speaker) && !a.speaker_unsure && canonicalSpeakerId(people, a.speaker) === target,
    );
  };
  for (const debate of debates) {
    for (const pos of debate.positions) {
      if (matches(pos)) out.push({ question: debate.question, stance: pos.stance, at: pos.at });
    }
  }
  return out;
}

/** Human-readable speaking statistics for a speaker row, e.g. "spoke 12 min · 34 lines". */
export function formatSpeakerStats(seconds?: number, lines?: number): string {
  const sec = typeof seconds === "number" && Number.isFinite(seconds) && seconds >= 0 ? seconds : undefined;
  const lineCount = typeof lines === "number" && Number.isInteger(lines) && lines >= 0 ? lines : undefined;
  if (sec === undefined && lineCount === undefined) return "";
  const timePart = sec === undefined
    ? ""
    : sec >= 60
      ? t("recording.speakers_spoke_min", { min: Math.max(1, Math.round(sec / 60)) })
      : t("recording.speakers_spoke_sec", { sec: Math.max(1, Math.round(sec)) });
  const linesPart = lineCount === undefined
    ? ""
    : t("recording.speakers_lines", { count: lineCount });
  return [timePart, linesPart].filter(Boolean).join(" · ");
}
