// "Collapse all" / "Expand all" for the groups that are not drawn when it happens (#7440). A folded sidebar group drops
// its rows (a lazy Collapse), and a long group draws only the parts near the view (views/WindowedRows.tsx), so the groups
// in them (a worktree under its repository, a to-do's spawned Agents, a tab's panes) have no `useFolded` listening when a
// fold-all goes out (lib/sidebar-api.ts). Each fold-all is noted here, and each such place tells the groups that mount
// in it from when they may have missed one (`FoldMissed`): mounting, a group takes the last fold-all after that and
// after its own last choice, as it would have taken it mounted. A group drawn for the first time, with nothing missed
// around it (a new Agent, another grouping), keeps its own fold, as before. Kept for this visit, not across a reload:
// a group not drawn again before one shows the fold it had stored before the fold-all (docs/web/web.md).
import { createContext } from "react";

let seq = 0;
// the last fold-all of each scope (undefined: every group's, the keyboard shortcut's)
const last = new Map<string | undefined, { seq: number; folded: boolean }>();
// when each group's fold was last set (by hand, or by a fold-all that reached it): an earlier fold-all is not its
const decided = new Map<string, number>();

/** Notes a fold-all, before it goes out to the groups listening. */
export function noteFoldAll(folded: boolean, scope?: string): void {
  last.set(scope, { seq: ++seq, folded });
}

/** The fold-alls so far: a place that stops drawing its groups now missed the ones after it. */
export const foldAllSeq = () => seq;

/** A group's fold was set now (by hand, or a fold-all reached it): fold-alls before now are not for it any more. */
export function foldDecided(key: string): void {
  decided.set(key, seq);
}

/** The fold a group mounting now missed (true: folded), if any: the last fold-all after `since` (its place's, below)
 *  and after the group's own last choice that reaches its `scope`. Taking it counts as the group's choice. */
export function missedFoldAll(key: string, scope: string | undefined, since: number | null): boolean | undefined {
  if (since === null) return undefined;
  const after = Math.max(since, decided.get(key) ?? since);
  const every = last.get(undefined);
  const own = scope === undefined ? undefined : last.get(scope);
  const latest = every && own ? (every.seq > own.seq ? every : own) : (every ?? own);
  if (!latest || latest.seq <= after) return undefined;
  decided.set(key, latest.seq);
  return latest.folded;
}

/** A place that does not draw its groups all the time (a lazy fold, a long group's parts): `since`, the fold-alls after
 *  it may have missed the groups mounting in it now; null, none did. Mutable, read when a group mounts: the place
 *  updates it after each commit without drawing anything again. */
export type FoldRegion = { since: number | null };
export const FoldMissed = createContext<FoldRegion>(Object.freeze({ since: null }));

/** For tests: forget every fold-all. */
export function resetFoldAll(): void {
  seq = 0;
  last.clear();
  decided.clear();
}
