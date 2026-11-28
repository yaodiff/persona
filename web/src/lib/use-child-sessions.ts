// The child sessions folded under one chat's session (#5120, docs/protocol/protocol.md "Session hierarchy"): what its
// cards are drawn from. A hub that pushes every row has them in the store already; a paging hub (`sessions_list`) is asked
// for exactly those rows (`parent_session_id`, with `sessions_hierarchy`; list summaries) while the chat is open and the
// session's row says it has some (its `children` rollup). An older paging hub folds nothing: no cards. The rows an open
// chat holds are kept here too (`useChildRow`), where its cards and its children's request cards find them: a summary is
// never put among the store's complete rows.
import { useEffect, useLayoutEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { foldedChildren, rowKey, SESSIONS_CROSS_MACHINE_HIERARCHY_FEATURE, SESSIONS_HIERARCHY_FEATURE } from "./child-sessions";
import { runtimeIdentity, runtimeOrder, runtimeRemoval, runtimeSessions } from "./runtime-list";
import { heldSeen } from "./seen-outbox";
import { SESSIONS_LIST_FEATURE, SessionList, type ListSnapshot } from "./session-list";
import { isSide } from "./side-fold";
import { onHubEvent, useStore } from "./store";
import type { Session } from "./types";

const NONE: Session[] = [];
const EMPTY: ListSnapshot<Session> = { rows: [], loaded: false, loading: false, loadingMore: false, error: null, total: 0, counts: null, hasMore: false, supported: null };
const noSubscribe = () => () => {};
const noSnapshot = () => EMPTY;

export interface ChildSessions {
  /** The children, oldest first (by when each started); the same array while none of them changed. */
  rows: Session[];
  /** All of them are known: a chat waiting for one (a link to it) can tell it is not there. */
  loaded: boolean;
}

// ── the rows the open chats hold, by each chat (a page and a side panel may show the same one) ──
const held = new Map<object, { parent: string; complete: boolean; rows: Map<string, Session> }>();
let heldVersion = 0;
const heldListeners = new Set<() => void>();
const tellHeld = () => {
  heldVersion++;
  heldListeners.forEach((l) => l());
};
const subscribeHeld = (l: () => void) => {
  heldListeners.add(l);
  return () => void heldListeners.delete(l);
};

/** A child session's row as an open chat of its parent holds it; undefined when none does. */
export function heldChild(mid: string, sid: string): Session | undefined {
  for (const { rows } of held.values()) {
    const row = rows.get(rowKey(mid, sid));
    if (row) return row;
  }
  return undefined;
}

// how each paging chat's list of children reads again, by the same owner as `held`
const rereads = new Map<object, () => Promise<void>>();

/** Reads again the lists of children that hold mid/sid, after something changed it that no hub hint may follow yet (a
 *  Live link, #7497): a child's card reads its parent's held row before the store's. Done when they answered. */
export async function rereadHeldChild(mid: string, sid: string): Promise<void> {
  const key = rowKey(mid, sid);
  await Promise.all([...held].filter(([, h]) => h.rows.has(key)).map(([owner]) => rereads.get(owner)?.()));
}

/** `heldChild`, kept up to date: a child's card and its request card read their row here. */
export function useChildRow(mid: string, sid: string): Session | undefined {
  const read = () => heldChild(mid, sid);
  return useSyncExternalStore(subscribeHeld, read, read);
}

// the held side rows by their parent, worked out again once the held rows change
let sidesByParent = new Map<string, Session[]>();
let sidesAt = -1;

/** The side sessions of mid/sid (#7944) when an open chat holds its folded rows (its page, its Side sessions panel);
 *  undefined when none does. A paged list's row reads its side part here, so a side seen here shows at once. */
export function heldSides(mid: string, sid: string): Session[] | undefined {
  if (sidesAt !== heldVersion) {
    const next = new Map<string, Session[]>();
    for (const h of held.values()) {
      if (!h.complete || next.has(h.parent)) continue;
      next.set(h.parent, sameRows(sidesByParent.get(h.parent), [...h.rows.values()].filter(isSide)));
    }
    sidesByParent = next;
    sidesAt = heldVersion;
  }
  return sidesByParent.get(rowKey(mid, sid));
}

/** The side rows of a listed row (`heldSides` by its own key), for what counts many rows at once (heldCounts). */
export const heldSidesOf = (s: Pick<Session, "machine_id" | "session_id">): Session[] | undefined => heldSides(s.machine_id, s.session_id);

/** Moves on whenever the rows the open chats hold change: what reads `heldSides` outside a row's hook (a count) reads
 *  again with it. */
export function useHeldVersion(): number {
  const read = () => heldVersion;
  return useSyncExternalStore(subscribeHeld, read, read);
}

/** `heldSides`, kept up to date; undefined while `enabled` is off (a row with no sides reads nothing). */
export function useHeldSides(mid: string, sid: string, enabled = true): Session[] | undefined {
  const read = () => (enabled ? heldSides(mid, sid) : undefined);
  return useSyncExternalStore(subscribeHeld, read, read);
}

/** `prev` while it holds the same rows as `next`, so what is built from it holds still between pushes. */
export function sameRows(prev: Session[] | undefined, next: Session[]): Session[] {
  return prev && prev.length === next.length && prev.every((s, i) => s === next[i]) ? prev : next;
}

/** The sessions folded into mid/sid's chat (lib/child-sessions.ts foldedChildren: its side sessions among them); none
 *  while `enabled` is off. `hasChildren`: the session's row counts some (a paging hub is not asked otherwise). `within`:
 *  for a side session's chat (#7944), the parent it folds under, whose folded rows a paging hub is asked for: the side's
 *  own children fold under that parent too, naming the side in `folded_side`. */
export function useChildSessions(mid: string, sid: string, hasChildren: boolean, enabled = true, within?: string): ChildSessions {
  const paged = useStore((s) => s.features.includes(SESSIONS_LIST_FEATURE));
  const hierarchy = useStore((s) => s.features.includes(SESSIONS_HIERARCHY_FEATURE));
  const crossMachine = useStore((s) => s.features.includes(SESSIONS_CROSS_MACHINE_HIERARCHY_FEATURE));
  const storeLoaded = useStore((s) => s.loaded);
  // a hub that pushes every row: search all machines so cross-machine A2A subagents fold in too (useStore's shallow
  // compare keeps the array identity across pushes for unrelated sessions)
  const pushed = useStore((s) => (enabled && !paged ? foldedChildren(Object.values(s.sessions).flat(), mid, sid) : NONE));
  // a paging hub: one bounded list of this session's children, as list summaries (what a card shows is in one)
  const list = useMemo(
    () => (enabled && paged && hierarchy && hasChildren ? new SessionList<Session>("/sessions", runtimeIdentity, runtimeOrder, undefined, runtimeRemoval, { summary: true, pageCounts: () => runtimeSessions.usesPageCounts(), pauseWhenHidden: true }) : null),
    [enabled, paged, hierarchy, hasChildren],
  );
  const page = useSyncExternalStore(list?.subscribe ?? noSubscribe, list?.getSnapshot ?? noSnapshot, list?.getSnapshot ?? noSnapshot);
  const parent = within || sid;
  useEffect(() => {
    if (!list) return;
    const filters = crossMachine
      ? { parent_machine_id: mid, parent_session_id: parent }
      : { machine_id: mid, parent_session_id: parent };
    void list.setFilters(filters).catch(() => {});
    // the hub's hint for any change of the list: a read of what changed since (`since`), only for these rows
    const off = onHubEvent("sessions_list", (msg) => list.invalidate(msg));
    return () => {
      off();
      list.reset();
    };
  }, [list, mid, parent, crossMachine]);
  // a session with more children than a page: the rest too (there are seldom more than a few)
  useEffect(() => {
    if (list && page.loaded && page.hasMore && !page.loading && !page.error) void list.more().catch(() => {});
  }, [list, page]);
  // a paging hub's rows are the list's, not the store's: the ones seen here and not yet recorded by the hub show seen
  // here too (lib/seen-outbox.ts), as the store's rows do
  const marks = useStore((s) => (list ? s.seen : undefined));
  const last = useRef<Session[] | undefined>(undefined);
  const listed = list ? page.rows : undefined;
  const rows = useMemo(() => {
    const now = Date.now();
    const shown = listed ? foldedChildren(listed, mid, sid).map((s) => heldSeen(s, marks?.[runtimeIdentity(s)], now)) : pushed;
    return (last.current = sameRows(last.current, shown));
  }, [listed, pushed, mid, sid, marks]);
  const loaded = !enabled || (list ? (page.loaded && !page.hasMore) || page.supported === false : paged || storeLoaded);
  // all of them read (a paging hub's list, or the store's rows): what the parent's row may take its side part from
  const complete = enabled && (list ? page.loaded && !page.hasMore : !paged && storeLoaded);
  // held while the chat is open, before it paints: its cards find their rows in the same frame
  const owner = useMemo(() => ({}), []);
  useEffect(() => {
    if (!list) return;
    // a read after the one out now (an invalidation's trailing read), and its answer awaited
    rereads.set(owner, async () => {
      list.invalidate();
      await Promise.resolve();
      await list.load().catch(() => {});
    });
    return () => void rereads.delete(owner);
  }, [list, owner]);
  useLayoutEffect(() => {
    held.set(owner, { parent: rowKey(mid, sid), complete, rows: new Map(rows.map((s) => [rowKey(s.machine_id, s.session_id), s])) });
    tellHeld();
  }, [owner, rows, mid, sid, complete]);
  useLayoutEffect(
    () => () => {
      held.delete(owner);
      tellHeld();
    },
    [owner],
  );
  return useMemo(() => ({ rows, loaded }), [rows, loaded]);
}
