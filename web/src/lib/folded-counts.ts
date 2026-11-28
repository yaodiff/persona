import { backgroundWaiting, activityOf } from "./format";
import type { AgentState, Session } from "./types";

/** The states a folded sidebar row still counts, in the order it shows them: what needs you comes first (#3559). */
export const FOLDED_ORDER = [
  { key: "needs_you", state: "needs_you" },
  { key: "working", state: "working" },
  // working, but only waiting on background commands (#6045): counted apart from Working
  { key: "bg_waiting", state: "working", waiting: true },
  { key: "unread", state: "unread" },
] as const satisfies readonly { key: string; state: AgentState; waiting?: boolean }[];

export type FoldedCounts = Record<(typeof FOLDED_ORDER)[number]["key"], number>;

/** How many of these sessions need you, are working, only wait in the background and are done (and not yet seen). Idle
 *  and offline ones are not counted. */
export function foldedCounts(sessions: readonly Session[]): FoldedCounts {
  const counts: FoldedCounts = { needs_you: 0, working: 0, bg_waiting: 0, unread: 0 };
  for (const s of sessions) {
    const st = activityOf(s);
    if (st === "needs_you" && s.view && !s.view.counted) continue;
    if (backgroundWaiting(s)) counts.bg_waiting++;
    else if (st === "needs_you" || st === "working" || st === "unread") counts[st]++;
  }
  return counts;
}

/** Whether a folded row has anything to tally besides its plain count. */
export function hasFoldedCounts(c: FoldedCounts): boolean {
  return c.needs_you + c.working + c.bg_waiting + c.unread > 0;
}
