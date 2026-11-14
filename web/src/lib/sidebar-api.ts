import { storedValue, storeValue } from "./compatibility";
// What the sidebar offers the keyboard shortcuts (docs/web/web.md "Keyboard shortcuts"): fold or open every group, switch how Agents are
// grouped, walk the rows. The sidebar's groups subscribe through useFolded (`onFoldAll`), Sidebar.tsx subscribes to
// `onGrouping` and marks its list `data-sidebar-rows`, and the shortcuts call the rest. Kept out of the sidebar's views
// so they only gain the few lines that listen.
import { AGENT_GROUPINGS, agentGrouping, type AgentGrouping } from "./agent-groups";
import { noteFoldAll } from "./fold-all";

export const GROUPING_KEY = "persona.agentGrouping";

/** Whether the Agents block draws the hub's paged list (`BoundedAgentsBlock`) rather than the complete one (#6662): every
 *  grouping but By repository, whose worktree level needs every session, and By to-do only on a hub that lists sessions by
 *  to-do; pins and several assistants' scopes need the complete list. ⌘K's "Filter Agents list" asks it too (#6589). */
export function pagedAgentList(o: { bounded: boolean; byTodo: boolean; todosSupported: boolean; pins: { sessions: string[]; spaces: string[] }; stored: string | null; several: boolean }): boolean {
  const grouping = agentGrouping(o.stored);
  // a hub without todos shows by machine, a by-todo choice kept for when it has them
  const shown = !o.todosSupported && grouping === "todo" ? "machine" : grouping;
  return o.bounded && !o.several && o.pins.sessions.length === 0 && o.pins.spaces.length === 0 && shown !== "repo" && (shown !== "todo" || o.byTodo);
}

// ── fold all / open all ──────────────────────────────────

const foldListeners = new Map<(folded: boolean) => void, string | undefined>();

/** The Agents block's groups (its list menu folds or opens only them, #3027). */
export const AGENTS_FOLD = "agents";

/** Every foldable group of the sidebar follows (the sidebar's useFolded subscribes here); `scope`: the block it is in. */
export function onFoldAll(listener: (folded: boolean) => void, scope?: string): () => void {
  foldListeners.set(listener, scope);
  return () => void foldListeners.delete(listener);
}

/** Folds or opens every group, or only those of `scope`: the ones mounted now, and those not drawn now (under a folded
 *  group, in a long group's parts out of view) when they are next drawn (lib/fold-all.ts). */
export function setAllFolded(folded: boolean, scope?: string) {
  noteFoldAll(folded, scope);
  foldListeners.forEach((s, f) => (scope === undefined || s === scope) && f(folded));
}

/** The Agents list menu's "Collapse all" / "Expand all" (#3027, the app's foldAll): every group of the Agents block. Opening also
 *  opens the spaces and tab groups folded by hand that are not drawn now (another grouping, inside a folded group), so
 *  switching to them later finds them open too (Android #2792). */
export function setAgentsFolded(folded: boolean, store: Pick<Storage, "length" | "key" | "getItem" | "setItem"> = localStorage) {
  if (!folded) {
    const keys: string[] = [];
    for (let i = 0; i < store.length; i++) {
      const k = store.key(i);
      if (k && /^fold:(space|tree):/.test(k) && store.getItem(k) === "1") keys.push(k);
    }
    keys.forEach((k) => store.setItem(k, "0"));
  }
  setAllFolded(folded, AGENTS_FOLD);
}

/** What the Agents title row's fold-all button does now (#6447): a click always changes what is on screen. */
export type AgentsFoldAction = "open-block" | "fold-groups" | "open-groups";

/** `blockFolded`: the list is folded under the Agents title (its own click, #2524); `flat`: the list is drawn without
 *  groups (the chat-list view; its Pinned and Recent sections at the ends are not the list); `groups` / `open`: the group
 *  headings on screen and whether any is open. The button acts on the list under the controls only: Collapse all folds
 *  the groups to their headings while any is open, Expand all opens them when none is, and the Agents title, grouping
 *  tabs, quick filters, search and Filter stay. Folded under its title, the list opens with every group in it. A flat
 *  list, or one with no groups (a grouping still loading, nothing listed), has nothing to fold to, so there is no button:
 *  folding its rows would leave a blank under the search that reads as an empty list, and the design system invents no
 *  heading over a flat list (docs/design/DESIGN_SYSTEM.md 3.4). Before #6447 it acted on the groups alone, so there a
 *  click only flipped its label or opened a section out of sight; a first fix folded the whole block, controls too. */
export function agentsFoldAction({ blockFolded, flat = false, groups, open }: { blockFolded: boolean; flat?: boolean; groups: number; open: boolean }): AgentsFoldAction | null {
  if (blockFolded) return "open-block";
  if (flat || groups === 0) return null;
  return open ? "fold-groups" : "open-groups";
}

// ── how Agents are grouped ───────────────────────────────

const groupingListeners = new Set<(g: AgentGrouping) => void>();

export function onGrouping(listener: (g: AgentGrouping) => void): () => void {
  groupingListeners.add(listener);
  return () => void groupingListeners.delete(listener);
}

/** The grouping after `current`, among those offered (a hub without todos has no "By to-do"). */
export function nextGrouping(current: AgentGrouping, offered: readonly AgentGrouping[] = AGENT_GROUPINGS.map((g) => g.key)): AgentGrouping {
  // a choice the hub cannot offer is shown as the first one (the sidebar does the same)
  const i = Math.max(0, offered.indexOf(current));
  return offered[(i + 1) % offered.length] ?? current;
}

/** Choose a grouping from another navigation surface, keeping mounted sidebar and phone views in sync. */
export function setAgentGrouping(grouping: AgentGrouping): void {
  storeValue(GROUPING_KEY, grouping);
  groupingListeners.forEach((listener) => listener(grouping));
}

/** Switches the sidebar to its next grouping (kept in this browser, as the menu's choice is); returns it. */
export function cycleGrouping(offered?: readonly AgentGrouping[]): AgentGrouping {
  const next = nextGrouping(agentGrouping(storedValue(GROUPING_KEY)), offered);
  setAgentGrouping(next);
  return next;
}

export const groupingLabel = (g: AgentGrouping) => AGENT_GROUPINGS.find((x) => x.key === g)?.label ?? "";

// ── walking the rows ─────────────────────────────────────

/** The row after (`dir` 1) or before (-1) `current` among `count`, wrapping round; none is current → the first / last. */
export function stepIndex(count: number, current: number, dir: 1 | -1): number {
  if (count <= 0) return -1;
  if (current < 0) return dir === 1 ? 0 : count - 1;
  return (current + dir + count) % count;
}

/** Opens the previous / next row of the sidebar's list: the one you can see (not inside a folded group) and that is a page. */
export function stepSidebarRow(dir: 1 | -1, root: ParentNode = document): boolean {
  const rows = [...root.querySelectorAll<HTMLAnchorElement>("[data-sidebar-rows] a[href]:not([aria-label])")].filter((a) => !a.closest("[inert]") && a.getClientRects().length > 0);
  if (!rows.length) return false;
  const to = rows[stepIndex(rows.length, rows.findIndex((a) => a.getAttribute("aria-current") === "page"), dir)];
  if (!to) return false;
  // the row's own click opens it (and closes a drawer), as a tap would
  to.click();
  to.scrollIntoView({ block: "nearest" });
  return true;
}
