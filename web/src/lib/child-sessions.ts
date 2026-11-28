// Sessions an Agent started (a Codex subagent, an Agent a Paseo Agent created) folded into the session that started them
// (#5120, after cindy: a child Agent is never a task of its own). The hub's manager says which listed session a child
// folds under (`folded_under`) and gives that session a rollup of their states (`children`, docs/protocol/protocol.md
// "Session hierarchy"). The web leaves the children out of every list and count, shows the parent in its folded state,
// and draws each child as a card in the parent's chat, where its requests are answered too.
import { activityOf, stateOf } from "./format";
import { foldedStateWith, foldHome, foldsUnder, hubSides } from "./side-fold";
import type { AgentState, Interaction, Session } from "./types";
import { timeMs } from "./times";

/** The hub folds child sessions and its paged list takes `roots=1` and `parent_session_id=` (docs/protocol/protocol.md
 *  "Session hierarchy"): send those keys only when hello lists this. */
export const SESSIONS_HIERARCHY_FEATURE = "sessions_hierarchy";

/** The hub folds cross-machine subagents, hides orphan `is_subagent` rows under `roots=1&cross_machine_hierarchy=1`,
 *  and accepts `parent_machine_id=` on `GET /api/v1/sessions` (docs/protocol/protocol.md "Session hierarchy"). */
export const SESSIONS_CROSS_MACHINE_HIERARCHY_FEATURE = "sessions_cross_machine_hierarchy";

/** The state a parent shows and is counted under with its folded children (the hub's `Session.FoldedState`): one of
 *  them waiting on the user makes it need you; one working makes it work, unless it has a reply the user has not read or
 *  awaits the user's own; one of its side sessions unread makes it unread (#7944, lib/side-fold.ts). */
export function foldedState(s: Session): AgentState {
  return foldedStateWith(s);
}

/** A session's key in the sets and maps of child rows (`listedKeys`, the rows open chats hold). */
export const rowKey = (mid: string, sid: string) => `${mid}\u0000${sid}`;

/** The keys of the sessions a list holds, for `isFoldedIn`. */
export function listedKeys(rows: readonly Session[]): Set<string> {
  return new Set(rows.map((s) => rowKey(s.machine_id, s.session_id)));
}

/** The machine of the session `s` folds under (its own machine unless cross-machine `folded_under_machine_id` applies). */
export function foldsUnderMachine(
  s: Pick<Session, "machine_id" | "folded_under" | "folded_under_machine_id" | "folded_as" | "folded_side">,
): string {
  const under = foldsUnder(s);
  if (under && under === s.folded_under && s.folded_under_machine_id) return s.folded_under_machine_id;
  return s.machine_id;
}

/** s folds under a session the list holds (same-machine or cross-machine via `folded_under_machine_id`): it is never a
 *  row or a count of its own there. A side session folds only while the hub folds sides (lib/side-fold.ts). */
export function isFoldedIn(s: Session, listed: ReadonlySet<string>): boolean {
  const under = foldsUnder(s);
  return Boolean(under) && listed.has(rowKey(foldsUnderMachine(s), under!));
}

/** The sessions folded into mid/sid's chat, oldest first (by when they started, else were last updated): what folds
 *  under it (same-machine or cross-machine), its side sessions among them, plus unfolded orphan subagents that directly
 *  name mid/sid as parent (e.g. when the parent was archived), excluding rows belonging to a side's own chat
 *  (`folded_side`, #7944); for a side's chat, those rows. */
export function foldedChildren(rows: readonly Session[], mid: string, sid: string): Session[] {
  const at = (s: Session) => timeMs(s.started_at ?? s.updated_at);
  return rows
    .filter((s) => {
      const home = foldHome(s);
      if (home) {
        if (home !== sid) return false;
        const homeMid = s.folded_side ? s.machine_id : (s.folded_under_machine_id || s.machine_id);
        return homeMid === mid;
      }
      if (!s.is_subagent) return false;
      const parentMid = s.parent_machine_id || s.machine_id;
      return parentMid === mid && (s.parent_session_id === sid || s.root_session_id === sid);
    })
    .sort((a, b) => at(a) - at(b));
}

export type RequestHomeRef = Pick<
  Interaction,
  "machine_id" | "session_id" | "folded_under" | "folded_under_machine_id" | "folded_as" | "folded_side"
>;

/** The machine of the session a request is counted under: its parent's machine for a cross-machine folded child, else its own. */
export function requestHomeMachine(ix: RequestHomeRef): string {
  const under = foldsUnder(ix);
  if (under && under === ix.folded_under && ix.folded_under_machine_id) return ix.folded_under_machine_id;
  return ix.machine_id;
}

/** The session a request is counted under, whose page is its home: the one its session folds under, else its own. */
export function requestHome(ix: Pick<Interaction, "session_id" | "folded_under" | "folded_as" | "folded_side">): string {
  return foldsUnder(ix) || ix.session_id;
}

/** How many of a session's folded children are working. */
export function runningChildren(s: Session): number {
  return s.children?.working ?? 0;
}

/** Unified parent-row subagent rollup (DESIGN_SYSTEM, Principle 4):
 *  waiting children (`needs_you`) take precedence over running subagents (`running`); quiet (`null`) when all ended. */
export type ChildRollup = { kind: "needs_you" | "running"; count: number };

export function childRollup(s: Session | undefined, childrenRows?: readonly Session[]): ChildRollup | null {
  if (!s) return null;
  const c = s.children;
  const side = hubSides(s);
  const hubNeeds = Math.max(0, (c?.needs_you ?? 0) - (side?.needs_you ?? 0));
  const rowNeeds = childrenRows
    ? childrenRows.filter((child) => child.folded_as !== "side" && stateOf(child) === "needs_you").length
    : 0;
  const childNeeds = Math.max(hubNeeds, rowNeeds);
  if (childNeeds > 0) return { kind: "needs_you", count: childNeeds };

  const childWorking = Math.max(0, (c?.working ?? 0) - (side?.working ?? 0));
  const embeddedIds = new Set<string>();
  if (s.subagents?.length) {
    for (const a of s.subagents) {
      if (a.running) embeddedIds.add(rowKey(s.machine_id, a.session_id || `${s.session_id}@${a.id}`));
    }
  }
  if (childrenRows) {
    const runningIds = new Set(embeddedIds);
    for (const child of childrenRows) {
      if (child.folded_as === "side") continue;
      const st = activityOf(child);
      if (st === "working" || Boolean(child.background_working)) {
        runningIds.add(rowKey(child.machine_id, child.session_id));
      }
    }
    const running = Math.max(runningIds.size, embeddedIds.size + childWorking);
    return running > 0 ? { kind: "running", count: running } : null;
  }
  if (s.subagents) {
    const running = embeddedIds.size + childWorking;
    return running > 0 ? { kind: "running", count: running } : null;
  }
  const summaryRunning = typeof s.waiting === "boolean" ? (s.running_subagents ?? 0) : 0;
  // Paseo runtime summaries mirror child sessions into background[kind=agent] -> running_subagents; avoid double-counting them.
  const overlaps = s.source_kind === "runtime" || s.agent === "paseo";
  const running = overlaps ? Math.max(summaryRunning, childWorking) : summaryRunning + childWorking;
  return running > 0 ? { kind: "running", count: running } : null;
}
