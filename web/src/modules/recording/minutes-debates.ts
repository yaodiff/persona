// Real disagreements in meeting minutes (#16728, docs/assistant/meeting-minutes.md "Disagreements (#16728)"):
// `minutes.debates` read on its own, apart from the rest of the minutes, and cleaned as the contract's "Clients" says.
// When nothing survives, no Disagreements section is shown and the minutes look identical to minutes without `debates`.
// The hub, mock hub, iPhone and this page check the same cases (spec/fixtures/minutes-debates/cases.json).
import type { MinutesPoint } from "../../lib/kb";
import { isValidSpeakerId } from "../../lib/recording-speakers";
import type { MinutesOverview } from "./minutes-overview";

/** Closed status set: an unknown, missing or non-string wire value decodes to `"unknown"` (never to a real status) and
 *  is shown with no status word (`docs/architecture/evolution-guide.md`). */
export type DebateStatus = "open" | "settled" | "unknown";

export interface DebateSupporter {
  at?: number;
  speaker?: string;
  speaker_unsure?: boolean;
}

export interface DebatePosition {
  stance: string;
  /** Start of the cited transcript line (seconds into the recording), omitted when absent or invalid. */
  at?: number;
  who?: string;
  /** Hub-computed speaker attribution from the cited line (#17674); absent without a timeline. */
  speaker?: string;
  speaker_unsure?: boolean;
  /** Additional lines where other speakers backed the same position (#17674). */
  also?: DebateSupporter[];
}

export interface DebateOutcome {
  text: string;
  /** Start of the cited transcript line where it was decided, omitted when absent or invalid. */
  at?: number;
  /** 0-based index in `minutes.decisions` computed by the hub; kept only when an integer within `decisions` bounds. */
  decision?: number;
}

export interface MinutesDebate {
  question: string;
  topic?: string;
  status: DebateStatus;
  at?: number;
  positions: DebatePosition[];
  outcome?: DebateOutcome;
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const nonBlank = (v: unknown): string | undefined => (typeof v === "string" && v.trim() ? v : undefined);
const nonNeg = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : undefined);

function cleanStatus(v: unknown): DebateStatus {
  if (v === "open" || v === "settled") return v;
  return "unknown";
}

function cleanSupporter(o: Obj): DebateSupporter | null {
  const at = nonNeg(o.at);
  const speaker = isValidSpeakerId(o.speaker) ? o.speaker : undefined;
  const unsure = speaker && o.speaker_unsure === true ? true : undefined;
  if (at === undefined && !speaker) return null;
  return {
    ...(at !== undefined ? { at } : {}),
    ...(speaker ? { speaker } : {}),
    ...(unsure ? { speaker_unsure: true } : {}),
  };
}

function cleanPosition(o: Obj): DebatePosition | null {
  const stance = nonBlank(o.stance);
  if (!stance) return null;
  const at = nonNeg(o.at);
  const who = nonBlank(o.who);
  const speaker = isValidSpeakerId(o.speaker) ? o.speaker : undefined;
  const unsure = speaker && o.speaker_unsure === true ? true : undefined;
  const also = Array.isArray(o.also)
    ? o.also.filter(isObj).map(cleanSupporter).filter((x): x is DebateSupporter => !!x)
    : [];
  return {
    stance,
    ...(at !== undefined ? { at } : {}),
    ...(who ? { who } : {}),
    ...(speaker ? { speaker } : {}),
    ...(unsure ? { speaker_unsure: true } : {}),
    ...(also.length ? { also } : {}),
  };
}

function cleanOutcome(v: unknown, decisionsCount: number): DebateOutcome | undefined {
  if (!isObj(v)) return undefined;
  const text = nonBlank(v.text);
  if (!text) return undefined;
  const at = nonNeg(v.at);
  const decision =
    typeof v.decision === "number" && Number.isInteger(v.decision) && v.decision >= 0 && v.decision < decisionsCount
      ? v.decision
      : undefined;
  return { text, ...(at !== undefined ? { at } : {}), ...(decision !== undefined ? { decision } : {}) };
}

/** Disagreements as the page shows them, or `[]` when missing, malformed or nothing survives cleaning. */
export function minutesDebates(v: unknown, decisionsCount = 0): MinutesDebate[] {
  if (!Array.isArray(v)) return [];
  const limit = Number.isInteger(decisionsCount) && decisionsCount > 0 ? decisionsCount : 0;
  const out: MinutesDebate[] = [];
  for (const entry of v) {
    if (!isObj(entry)) continue;
    const question = nonBlank(entry.question);
    if (!question || !Array.isArray(entry.positions)) continue;
    const positions = entry.positions.filter(isObj).map(cleanPosition).filter((p): p is DebatePosition => !!p);
    if (positions.length < 2) continue;
    const status = cleanStatus(entry.status);
    const topic = nonBlank(entry.topic);
    const at = nonNeg(entry.at);
    const outcome = status === "settled" ? cleanOutcome(entry.outcome, limit) : undefined;
    out.push({
      question,
      ...(topic ? { topic } : {}),
      status,
      ...(at !== undefined ? { at } : {}),
      positions,
      ...(outcome ? { outcome } : {}),
    });
  }
  return out;
}

/** The element id a settled disagreement's hub-linked decision jumps to. */
export function decisionAnchor(index: number): string {
  return `recording-decision-${index}`;
}

export interface OverviewDecisionTarget {
  anchor: string;
  groupIndex: number;
  topicIndex: number;
  bulletIndex: number;
}

/** Locates each kept decision (`minutes.decisions[i]`) inside the summary card's topic bullets (`bullet.decision === true`)
 *  so a settled disagreement's hub-computed `outcome.decision` index can jump to that rendered bullet (opening its topic
 *  if folded). Never derives a decision link from `outcome.at`: only maps `minutes.decisions` to the overview bullets
 *  that mirror it 1-to-1 (`docs/assistant/meeting-minutes.md` "Summary card (#6563)"). */
export function overviewDecisionTargets(
  overview: Pick<MinutesOverview, "groups">,
  decisions?: readonly MinutesPoint[] | null,
): Map<number, OverviewDecisionTarget> {
  const out = new Map<number, OverviewDecisionTarget>();
  if (!decisions?.length) return out;
  const bullets: { g: number; t: number; b: number; text: string; at?: number; used: boolean }[] = [];
  overview.groups.forEach((group, g) => {
    group.topics.forEach((topic, t) => {
      topic.bullets.forEach((bullet, b) => {
        if (bullet.decision) bullets.push({ g, t, b, text: bullet.text, at: bullet.at, used: false });
      });
    });
  });
  decisions.forEach((decision, idx) => {
    const match =
      bullets.find((x) => !x.used && x.text === decision.text && x.at === decision.at) ??
      bullets.find((x) => !x.used && x.text === decision.text);
    if (!match) return;
    match.used = true;
    out.set(idx, { anchor: decisionAnchor(idx), groupIndex: match.g, topicIndex: match.t, bulletIndex: match.b });
  });
  return out;
}
