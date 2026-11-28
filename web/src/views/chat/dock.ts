// What sits under the chat on a phone (#1996): the composer, and above it the cards waiting on you, the running status,
// the queue. Without a limit, two or three cards (or a keyboard) pushed the conversation away and the composer off the
// screen. The heights follow the visible viewport (`--kb-h`, lib/viewport.ts), so they shrink with the keyboard.
import { interactionAsk, interactionTitle } from "../../lib/inbox";
import type { AgentState, Interaction } from "../../lib/types";
import type { Segment } from "../../lib/turns";

/** A share of the visible height, capped at `cap` px, as a CSS length. */
export function visibleShare(share: number, cap?: number): string {
  const part = `calc(var(--kb-h) * ${share})`;
  return cap === undefined ? part : `min(${cap}px, ${part})`;
}

/** Everything under the chat on a narrow screen: at most about half of what is visible. */
export const DOCK_MAX = visibleShare(0.55);
/** Answer can give the existing command field room above its sticky footer. The composer keeps its place below it. */
export const COMMAND_DOCK_MAX = visibleShare(0.72);
/** The composer's text box: 240px, less when little is visible, so the send button stays on screen. */
export const INPUT_MAX = visibleShare(0.35, 240);
/** A plan card's plan (it scrolls inside). */
export const PLAN_MAX = visibleShare(0.4);

/** The answered line's list, opened (card-stack.ts): it scrolls inside past a third of what is visible. */
export const ANSWERED_LIST_MAX = visibleShare(0.3, 220);

export interface CardFold {
  /** Shown in full: the first `full` cards. */
  full: number;
  /** The one line in their place, or nothing: "N pending" for all of them while typing (`typing`: tapping it puts the
   *  keyboard away) or on a phone on its side (`short`: tapping it opens them). */
  summary: { kind: "typing" | "short"; count: number } | null;
}

/** Whether the answered cards (their receipts) show above the composer: not while the cards are folded to one line
 *  (typing, or a short screen not opened), which is about the room the keyboard leaves. */
export function showsAnswered({ narrow, keyboard, open, short = false }: { narrow: boolean; keyboard: boolean; open: boolean; short?: boolean }): boolean {
  return !narrow || (!keyboard && (open || !short));
}

/**
 * Which of the cards waiting on you show in full: the first, at every width, the others behind it until its "2 / 6"
 * is tapped (`open`); on a narrow screen while the keyboard is up, or on a phone on its side (`short`) until tapped,
 * none: one line in their place.
 */
export function cardFold(count: number, { narrow, keyboard, open, short = false }: { narrow: boolean; keyboard: boolean; open: boolean; short?: boolean }): CardFold {
  if (count === 0) return { full: 0, summary: null };
  if (narrow && keyboard) return { full: 0, summary: { kind: "typing", count } };
  if (open) return { full: count, summary: null };
  if (narrow && short) return { full: 0, summary: { kind: "short", count } };
  return { full: 1, summary: null };
}

/** A card in one line (the folded cards' line): what it asks, as the inbox says it, else what it waits for. */
export function cardLine(ix: Interaction): string {
  return interactionAsk(ix) || interactionTitle(ix);
}

export interface NextWaiting {
  /** The first of them in "Overview"'s "Needs you" order (the store's). */
  ix: Interaction;
  /** How many cards wait on you outside this chat. */
  count: number;
}

/** Authoritative cards other chats wait on you with (#4091): the same list "Overview"'s "Needs you" shows, this chat's taken out. */
export function nextWaiting(all: Interaction[], mid: string, sid: string): NextWaiting | null {
  const others = all.filter((ix) => !ix.mirror_of && (ix.machine_id !== mid || ix.session_id !== sid));
  return others.length ? { ix: others[0], count: others.length } : null;
}

/** When the turn going on started: its message, else its first item (SegmentView's "Working…" counts from the same). */
export function turnStart(seg: Pick<Segment, "user" | "start"> | undefined): string | undefined {
  return seg?.user?.ts ?? seg?.start;
}

/** How long a message of yours, the newest thing in the chat, counts as the start of a turn the hub has not reported yet. */
export const AWAIT_MS = 60_000;
/** Give the status stream time to catch up before reporting an empty echoed turn. */
export const RETRY_IDLE_GRACE_MS = 2_000;

/**
 * A message of yours is the newest thing and nothing has answered it, while the session's state still says it is not
 * at work (#3654): the transcript has the message before the node's next sessions push says working, and in between the
 * chat showed nothing. True for AWAIT_MS after the message; never while offline or waiting on you.
 */
export function awaitingTurn(
  last: Pick<Segment, "user" | "steps" | "answer"> | undefined,
  state: AgentState,
  now: number,
  /** The session's failure, if it has one: when its state last changed (the turn the host stopped, botmux). */
  failure?: { since?: string },
  settled = false,
): boolean {
  if (!last?.user || last.answer || settled) return false;
  // Opening a completed exchange must not revive it. An unanswered tool/thought can precede the hub's working state.
  if (last.steps.length && !last.steps.some(s => s.kind === "thinking" || (s.kind === "tool" && !s.result))) return false;
  if (state !== "idle" && state !== "unread" && state !== "unknown") return false;
  const at = last.user.ts ? Date.parse(last.user.ts) : NaN;
  if (Number.isNaN(at) || now - at >= AWAIT_MS) return false;
  // the turn failed before writing anything: its FailureNote says so, nothing is worked on (#3935). A failure
  // without a time, or from before the message (the hub clears it once a message goes in), is taken as this one's
  // only when it is not older.
  if (failure) {
    const failedAt = failure.since ? Date.parse(failure.since) : NaN;
    if (Number.isNaN(failedAt) || failedAt >= at) return false;
  }
  return true;
}

/**
 * State used for the pre-working pickup wait. An existing background command can keep the session `working` while
 * the next foreground turn has not been picked up yet, in which case that working flag is background-only and the
 * echo still needs the idle-based wait. Once this turn already streams, answers, thinks or runs an unresolved tool,
 * the foreground is demonstrably active: never reinterpret that `working` as idle, otherwise a live turn falls into
 * the empty-response/Retry path and loses the real Interrupt guard (review P1 on #14941).
 */
export function pickupStateForWait(
  state: AgentState,
  backgroundWorking: boolean | undefined,
  foregroundActive: boolean,
): AgentState {
  return state === "working" && backgroundWorking === true && !foregroundActive ? "idle" : state;
}
