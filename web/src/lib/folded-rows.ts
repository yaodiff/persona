// The lists' side of child sessions folded into their parent (#5120, lib/child-sessions.ts): every list and count the
// web draws takes its rows through foldRows, so a child is never a row, a dock entry or a count of its own, and each
// parent is drawn and counted once in its folded state. A child's card belongs to the parent too (homeKey). Side
// sessions fold the same way while the hub folds them (#7944, lib/side-fold.ts); a list that holds them works out its
// parents' side part from those rows, so a side seen here shows at once.
import { foldedState, foldsUnderMachine, isFoldedIn, listedKeys, requestHome, requestHomeMachine, rowKey, type RequestHomeRef } from "./child-sessions";
import { activityOf } from "./format";
import { countSides, foldedStateWith, isSide, sideFoldOn, type SideRollup } from "./side-fold";
import type { Interaction, Session } from "./types";

// One folded copy per row object: selectors and memoized rows compare by identity, so the same row gives back the same
// copy until the hub sends a new one.
const copies = new WeakMap<Session, Session>();
// The rows foldRows gave out, in their folded state already (their side part from the side rows the list held): drawn
// again, they keep it, and are not folded a second time from the hub's rollup alone.
const settled = new WeakSet<Session>();
// each folded copy's row as the hub sent it, in its own state (ownRow)
const owners = new WeakMap<Session, Session>();

/** The row a folded copy was made from, in its own state; `s` itself when it is not one. */
export function ownRow(s: Session): Session {
  return owners.get(s) ?? s;
}

/** A row as the lists draw and count it: in its folded state, `s` itself when that is its own state. `sides`: the
 *  rollup of its side rows the caller holds (else the hub's, `children.sides*`). */
export function foldedRow(s: Session, sides?: SideRollup): Session {
  if (s.view || !s.children || (!sides && settled.has(s))) return s;
  const state = sides ? foldedStateWith(s, sides) : foldedState(s);
  if (state === activityOf(s)) return s;
  let copy = copies.get(s);
  if (copy?.state !== state) {
    copy = { ...s, state };
    copies.set(s, copy);
    owners.set(copy, ownRow(s));
  }
  return copy;
}

// One folded list per list object: a store selector folding a machine's rows (`sessions[mid]`, replaced only when that
// machine's rows change) gets the same array back on every other push.
// (and the same side rule: hello can turn side folding on or off for the same rows)
const folds = new WeakMap<Session[], { sides: boolean; out: Session[] }>();

/** A list as the web shows and counts it: without any `is_subagent` rows or children folded under a row the list holds,
 *  each parent in its folded state (foldedRow, its side part from the side rows in `rows`); `rows` itself when nothing changes. */
export function foldRows(rows: Session[]): Session[] {
  const known = folds.get(rows);
  if (known?.sides === sideFoldOn()) return known.out;
  if (!rows.some((s) => s.is_subagent || s.folded_under || s.children)) return rows;
  const listed = listedKeys(rows);
  // the side rows this list holds, by their parent
  const sides = new Map<string, Session[]>();
  for (const s of rows) {
    if (!isSide(s) || !isFoldedIn(s, listed)) continue;
    const k = rowKey(foldsUnderMachine(s), s.folded_under!);
    const held = sides.get(k);
    if (held) held.push(s);
    else sides.set(k, [s]);
  }
  let changed = false;
  const out: Session[] = [];
  for (const s of rows) {
    // Views are published per computer. A child on another computer can own
    // a counted request even when the client also has its parent's row.
    if (!s.view?.counted && (s.is_subagent || isFoldedIn(s, listed))) {
      changed = true;
      continue;
    }
    const mine = s.children?.sides ? sides.get(rowKey(s.machine_id, s.session_id)) : undefined;
    const shown = foldedRow(s, mine && countSides(mine));
    if (shown !== s) changed = true;
    if (shown.children) settled.add(shown);
    out.push(shown);
  }
  if (!changed) return rows;
  folds.set(rows, { sides: sideFoldOn(), out });
  return out;
}

/** The agentKey (`<machine>/<session>`) of the session a card belongs to in lists and counts: the parent a child's
 *  card folds under (including across machines via `folded_under_machine_id`), else its own. */
export function homeKey(ix: RequestHomeRef): string {
  return `${requestHomeMachine(ix)}/${requestHome(ix)}`;
}

/** The cards waiting for the user from the sessions folded under mid/sid (its same-machine and cross-machine children's
 *  questions and approvals). */
export function childCards(interactions: readonly Interaction[], mid: string, sid: string): Interaction[] {
  return interactions.filter(
    (ix) => requestHomeMachine(ix) === mid && requestHome(ix) === sid && !(ix.machine_id === mid && ix.session_id === sid),
  );
}

/** The card a session's row or task speaks for: its own, else one its child sessions wait on (their home is its page). */
export function sessionCard(interactions: readonly Interaction[], mid: string, sid: string): Interaction | undefined {
  return interactions.find((ix) => ix.machine_id === mid && ix.session_id === sid) ?? childCards(interactions, mid, sid)[0];
}
