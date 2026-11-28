// Side sessions live in their parent's chat (#7944; the owner's request of 2026-11-15, reversing the 11-13 decision that
// drew them under their parent in the list). A Claude desktop app's side chat, /btw question or spawned session names
// the session it was started from in `parent_agent_id` (#5935). When hello lists `sessions_side_fold`, the hub's manager
// folds it under its topmost listed parent (`folded_under`, `folded_as: "side"`) and gives that parent a side-only
// rollup (`children.sides*`, docs/protocol/protocol.md "Session hierarchy"): no list or count shows it on its own, and
// the parent's chat reaches it from one button (views/chat/TeamPanel.tsx SidesPanel). A row folded through a side that
// is not itself one (the side's own Codex children or workers) names that side in `folded_side`: it belongs to the
// side's chat. Without the feature nothing here folds a side: the lists keep nesting it by `parent_agent_id`
// (lib/side-sessions.ts). This module is the rule, with no imports of the store, so every list and count reads it.
import { activityOf } from "./format";
import type { AgentState, Session } from "./types";
import { byTime } from "./times";

/** The hub folds side sessions into their parent (#7944): fold them only while hello lists this. */
export const SESSIONS_SIDE_FOLD_FEATURE = "sessions_side_fold";

let sideFold = false;
/** Set from hello (lib/store.ts): whether this hub folds side sessions. */
export function setSideFold(on: boolean) {
  sideFold = on;
}
export const sideFoldOn = () => sideFold;

type Folded = { folded_under?: string; folded_as?: string; folded_side?: string };

/** A side session folded into its parent's chat: only with the feature on (otherwise it stays a row). */
export const isSide = (s: Folded): boolean => sideFold && s.folded_as === "side" && Boolean(s.folded_under);

/** The listed row `s` folds under in lists and counts, if any. A side folds under its topmost parent only with the
 *  feature; without it a row the hub folded through a side folds under that side, as before side folding. */
export function foldsUnder(s: Folded): string | undefined {
  if (s.folded_as === "side") return sideFold ? s.folded_under : undefined;
  if (s.folded_side && !sideFold) return s.folded_side;
  return s.folded_under;
}

/** The chat a folded row is drawn in (a child card, a worker): its side's when it belongs to one, else the row it folds
 *  under. A side's own home is its parent's chat, where its button and panel are. */
export function foldHome(s: Folded): string | undefined {
  return s.folded_side || foldsUnder(s);
}

/** A side session's rollup on its topmost parent: how many, and how many of them work, need the user or are unread. */
export interface SideRollup {
  sides: number;
  working: number;
  needs_you: number;
  unread: number;
}

/** The rollup the hub sent on a parent's row (`children.sides*`); undefined without the feature or any side. */
export function hubSides(s: Pick<Session, "children">): SideRollup | undefined {
  const c = s.children;
  if (!sideFold || !c?.sides) return undefined;
  return { sides: c.sides, working: c.sides_working ?? 0, needs_you: c.sides_needs_you ?? 0, unread: c.sides_unread ?? 0 };
}

/** The rollup of the side rows a client holds, by each one's own state as the page shows it (seen here at once). */
export function countSides(sides: readonly Session[]): SideRollup {
  const out: SideRollup = { sides: 0, working: 0, needs_you: 0, unread: 0 };
  for (const s of sides) {
    out.sides++;
    const st = activityOf(s);
    if (st === "working") out.working++;
    else if (st === "needs_you") out.needs_you++;
    else if (st === "unread") out.unread++;
  }
  return out;
}

/** The state a parent shows and is counted under (the hub's `Session.FoldedState`): it needs you if it or any folded row
 *  does; else it works if any folded row works, unless its own reply is unread or awaits the user's; else it is unread if
 *  it is or one of its sides is and it does not work itself (sides only: Codex children and workers stay as before);
 *  else its own. `sides`: the
 *  rollup of the side rows the client holds, which stands in for the hub's side part of `children`. */
export function foldedStateWith(s: Session, sides?: SideRollup): AgentState {
  const own = activityOf(s);
  if (s.view) return own;
  const c = s.children;
  const hub = hubSides(s);
  const local = sideFold ? sides : undefined;
  // the hub counts sides among `needs_you` and `working`: with the rows held here, their own part replaces the hub's
  const needs = (c?.needs_you ?? 0) - (local ? hub?.needs_you ?? 0 : 0) + (local?.needs_you ?? 0);
  const working = (c?.working ?? 0) - (local ? hub?.working ?? 0 : 0) + (local?.working ?? 0);
  if (own === "needs_you" || needs > 0) return "needs_you";
  if (working > 0 && own !== "unread" && !(own === "idle" && s.awaits_reply)) return "working";
  // a row at work itself stays working beside an unread side
  if (own === "unread" || (own !== "working" && ((local ?? hub)?.unread ?? 0) > 0)) return "unread";
  return own;
}

// needs you first, then working, then the rest, stopped ones last
const RANK: Partial<Record<AgentState, number>> = { needs_you: 0, working: 1, offline: 3 };
/** The panel's order (#7944): needs you first, then working, then by last activity, newest first, stopped ones last. */
export function sideOrder(a: Session, b: Session): number {
  return (RANK[activityOf(a)] ?? 2) - (RANK[activityOf(b)] ?? 2) || byTime(b.updated_at, a.updated_at) || (a.session_id < b.session_id ? -1 : a.session_id > b.session_id ? 1 : 0);
}

/** The side sessions among a parent's folded rows (sides of sides too), in the panel's order. */
export function sidesAmong(rows: readonly Session[]): Session[] {
  return rows.filter(isSide).sort(sideOrder);
}

/** The side sessions among a parent's folded rows that keep it Unread (#13299 showed why a row must say so): the unread
 *  ones, the most recent first. The sidebar names the first on the parent's row and opens it in one click. */
export function unreadSides(rows: readonly Session[]): Session[] {
  return rows.filter((s) => isSide(s) && activityOf(s) === "unread").sort((a, b) => byTime(b.updated_at, a.updated_at) || (a.session_id < b.session_id ? -1 : a.session_id > b.session_id ? 1 : 0));
}

/** The chat a card is answered in: the side session it belongs to (`folded_side`, or its own session when that is a
 *  side), else the row it shows under (spec/fixtures/side-fold, "chat"). In the parent's page a side's card is also in
 *  the parent's dock while another side is shown (DESIGN_SYSTEM 4.8). */
export function cardChat(ix: Folded & { session_id: string }): string {
  if (sideFold && ix.folded_side) return ix.folded_side;
  if (isSide(ix)) return ix.session_id;
  return foldsUnder(ix) || ix.session_id;
}

/** The side session a search hit is in (#7944: with `roots=1` the hub returns the side's topmost parent's row and names
 *  the side in that row's `search_hits[<id>].hit_session_id`, lib/runtime-search.ts `side`): opened as the side's link,
 *  its parent's page with the side shown. Undefined for a hit in the row's own conversation. */
export function searchHitSide(hit: { session: Pick<Session, "session_id">; side?: string }): string | undefined {
  return sideFold && hit.side && hit.side !== hit.session.session_id ? hit.side : undefined;
}
