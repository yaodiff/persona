// The finished agents this page marked seen that the hub has not recorded yet (the app's SeenOutbox, #1588;
// docs/protocol/agent-states.md "Seen"). A `seen` sent over a socket that is still connecting, or that looks open but is
// gone (a laptop waking up, a tab coming back after its socket went quiet), is lost without a word: the hub kept the
// agent Unread and the page showed it green again with the next list. A mark stays until a list from the hub shows it
// recorded (a later `seen_at`) or the agent moved on; until then the agent is shown seen and the mark goes again
// (over HTTP when the socket's is not taken in time, over the socket again after a reconnect).
//
// Once one of the hub's lists shows it recorded, the mark still holds the rows of the lists that have not read since
// (SEEN_SETTLE_MS): each list reads on its own hints, at most once in two seconds, so the sidebar's list could still
// have the row Unread when the chat's own read already had it seen, and the row went green again until it read. The
// hub's counts read with such rows still count the agent Unread too (heldCounts).
//
// Only the hub's own times are compared with each other (its `seen_at`, the machine's `updated_at`): the page's clock
// only says when to try again and when to give up.
import { activityOf } from "./format";
import { runtimeIdentity } from "./runtime-list";
import { countSides, foldedStateWith } from "./side-fold";
import type { ListCounts } from "./session-list";
import { timeMs } from "./times";
import type { Session } from "./types";

/** How long a mark sent over the socket gets before it goes again over HTTP, which answers. */
export const SEEN_CONFIRM_MS = 5_000;
/** Given up after this: the hub was out of reach all along. */
export const SEEN_KEEP_MS = 10 * 60_000;
/** The machine touched the finished agent's transcript without a new turn (the hub's own slack is 2 s). */
export const SEEN_SAME_TURN_MS = 2_000;
/** How long a mark the hub recorded still holds the rows of lists that have not read since (a list reads at most once
 *  in two seconds, a group's counts once in 1.5 s): well past both. */
export const SEEN_SETTLE_MS = 15_000;

export interface SeenMark {
  /** The hub's `seen_at` and the agent's `updated_at` when it was marked (ms). */
  seenBefore: number;
  updatedBefore: number;
  /** The `seen_at` this page wrote into its own copy ("Mark all as seen"): not the hub's record. */
  localSeenAt?: string;
  /** On the page's clock. */
  markedAt: number;
  sentAt: number;
  route: "socket" | "http";
  /** The hub answered the HTTP request: nothing more to send, only the list to wait for. */
  answered: boolean;
  /** When a list from the hub first showed it recorded (page clock): nothing more to send; it holds only the rows of
   *  lists that have not read since, for SEEN_SETTLE_MS. */
  recordedAt?: number;
}

/** By the agent's runtimeIdentity (lib/runtime-list.ts). */
export type SeenMarks = Readonly<Record<string, SeenMark>>;

const expired = (m: SeenMark, now: number) => now - m.markedAt > SEEN_KEEP_MS || (m.recordedAt !== undefined && now - m.recordedAt > SEEN_SETTLE_MS);

/** Finished and not seen yet: Unread, or Just finished (seen as far as its state goes, but nobody looked, #9751). */
export function unseen(s: Session): boolean {
  if (s.view) {
    const reason = s.view.reason;
    return !s.locallySeen && s.view.phase === "done" && (reason?.kind === "unseen" || reason?.kind === "just_finished" || (reason?.kind === "asks" && reason.unseen === true));
  }
  const st = activityOf(s);
  return st === "unread" || (st === "idle" && Boolean(s.work?.just_finished));
}

/** `s` as seen: no longer Unread, nor Just finished. */
export function seenCopy(s: Session): Session {
  return { ...s, state: "idle", ...(s.view ? { locallySeen: true } : {}), ...(s.work?.just_finished ? { work: { ...s.work, just_finished: false } } : {}) };
}

/** A mark for `s` as the page has it now; only an unseen one (Unread or Just finished) waits for the hub. */
export function newMark(s: Session, now: number, localSeenAt?: string): SeenMark | undefined {
  if (!unseen(s)) return undefined;
  return { seenBefore: timeMs(s.seen_at), updatedBefore: timeMs(s.updated_at), localSeenAt, markedAt: now, sentAt: now, route: "socket", answered: false };
}

/** The hub's row shows the mark taken: a `seen_at` later than the one it had (not the page's own). */
export function markRecorded(s: Session, m: SeenMark): boolean {
  return timeMs(s.seen_at) > m.seenBefore && s.seen_at !== m.localSeenAt;
}

/** The hub's row says the agent moved on since the look (a new turn ended, it works or needs you again), or the mark is
 *  too old: the mark is done with, whatever any list says. */
export function markEnded(s: Session, m: SeenMark, now: number): boolean {
  const newTurn = timeMs(s.updated_at) > m.updatedBefore + SEEN_SAME_TURN_MS;
  const st = activityOf(s);
  return newTurn || st === "working" || st === "needs_you" || expired(m, now);
}

/** The hub's row says the mark is over for this row: recorded, or the agent moved on (markEnded). */
export function markOver(s: Session, m: SeenMark, now: number): boolean {
  return markRecorded(s, m) || markEnded(s, m, now);
}

/** The row as the page shows it: an unseen one whose mark still holds it is seen. A row read before the hub recorded the
 *  mark is held too while the mark settles (SEEN_SETTLE_MS), however another list already showed it recorded. */
export function heldSeen(s: Session, m: SeenMark | undefined, now: number): Session {
  return m && unseen(s) && !markOver(s, m, now) ? seenCopy(s) : s;
}

/** `marks` after the hub's `rows`: a mark a row shows recorded is settling from now (recordedAt), one a row shows the
 *  agent moved on from goes. The same object when nothing changed. */
export function settleMarks(rows: Iterable<Session>, marks: SeenMarks, now: number): SeenMarks {
  let out: Record<string, SeenMark> | undefined;
  for (const s of rows) {
    const id = runtimeIdentity(s);
    const m = (out ?? marks)[id];
    if (!m) continue;
    if (markEnded(s, m, now)) {
      out ??= { ...marks };
      delete out[id];
    } else if (m.recordedAt === undefined && markRecorded(s, m)) {
      out ??= { ...marks };
      out[id] = { ...m, recordedAt: now };
    }
  }
  return out ?? marks;
}

/** `counts` as the page shows them, read with `rows` (as the list read them): an agent those rows still have Unread
 *  (or Just finished) that a mark holds seen is no longer counted so; the same object when none is. A parent is counted
 *  by its folded state (lib/side-fold.ts): its own mark leaves it Unread while one of its side sessions is, and
 *  `sidesOf` (the side rows an open chat or the sidebar holds, each seen by its own mark) takes it out of Unread once
 *  none of them is, before the hub's next read says so. */
export function heldCounts(counts: ListCounts, rows: readonly Session[], marks: SeenMarks, now: number,
  sidesOf?: (s: Session) => readonly Session[] | undefined): ListCounts {
  let unread = 0, finished = 0;
  for (const s of rows) {
    const m = marks[runtimeIdentity(s)];
    const own = m ? heldSeen(s, m, now) : s;
    const sides = s.children?.sides ? sidesOf?.(s) : undefined;
    if (own === s && !sides) continue;
    // what the hub counted it as, and what the page shows
    const counted = foldedStateWith(s);
    if (counted === "unread" && foldedStateWith(own, sides && countSides(sides)) !== "unread") unread++;
    else if (own !== s && counted !== "unread" && s.work?.just_finished) finished++;
  }
  if (!unread && !finished) return counts;
  const state = { ...counts.state };
  if (unread) {
    const was = state.unread ?? 0;
    const held = Math.min(unread, was);
    state.unread = was - held;
    state.idle = (state.idle ?? 0) + held;
  }
  return { ...counts, state, ...(finished && counts.just_finished !== undefined ? { just_finished: Math.max(0, counts.just_finished - finished) } : {}) };
}

/** `rows` as the page shows them; the same array when no mark holds any of them. */
export function heldRows(rows: Session[], marks: SeenMarks, now: number): Session[] {
  let out = rows;
  rows.forEach((s, i) => {
    const shown = heldSeen(s, marks[runtimeIdentity(s)], now);
    if (shown === s) return;
    if (out === rows) out = [...rows];
    out[i] = shown;
  });
  return out;
}

/** The marks still kept: the expired ones go (their agents were never listed again). */
export function liveMarks(marks: SeenMarks, now: number): SeenMarks {
  const gone = Object.keys(marks).filter((key) => expired(marks[key], now));
  if (!gone.length) return marks;
  const out = { ...marks };
  for (const key of gone) delete out[key];
  return out;
}

const waiting = (m: SeenMark, now: number) => m.route === "socket" && !m.answered && m.recordedAt === undefined && !expired(m, now);

/** Marks sent over the socket and not taken within SEEN_CONFIRM_MS: for HTTP now. */
export function dueMarks(marks: SeenMarks, now: number): string[] {
  return Object.keys(marks).filter((key) => waiting(marks[key], now) && now - marks[key].sentAt >= SEEN_CONFIRM_MS);
}

/** When the next mark sent over the socket is due (page clock, ms), if any. */
export function nextDue(marks: SeenMarks, now: number): number | undefined {
  const at = Object.values(marks).filter((m) => waiting(m, now)).map((m) => m.sentAt + SEEN_CONFIRM_MS);
  return at.length ? Math.min(...at) : undefined;
}

/** Not answered yet: sent again once the socket is back (an HTTP request that failed waits for that). */
export function unansweredMarks(marks: SeenMarks, now: number): string[] {
  return Object.keys(marks).filter((key) => !marks[key].answered && marks[key].recordedAt === undefined && !expired(marks[key], now));
}
