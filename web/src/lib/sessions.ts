// Derived lists the sidebar, the home page and the palette share.
import { useEffect, useMemo, useSyncExternalStore } from "react";
import { credentialOwner, errorText } from "./api";
import { CompleteSessions } from "./complete-sessions";
import { heldOnlyNeeds } from "./attention";
import { supervisorHeldOnly, userCards } from "./supervisor-held";
import { activityOf, backgroundWaiting, stateOf } from "./format";
import { legacyAgentViews } from "./legacy-agent-view";
import type { AgentView } from "./agent-view-wire";
import { isMuted } from "./session-actions";
import { sameValue } from "./stable-lines";
import type { AgentMutes } from "./agent-groups";
import { defaultManager } from "./manager-choice";
import { agentKey } from "./pins";
import { foldedState } from "./child-sessions";
import { foldRows } from "./folded-rows";
import { heldLaunchRows, observeLaunchRows } from "./launch-handoff";
import { cardSessionRow, getState, onHubEvent, subscribe, useStore, type State } from "./store";
import { SESSIONS_LIST_FEATURE, type ListCounts } from "./session-list";
import { runtimeAgents, runtimeSessions, supportsPagedAgents } from "./runtime-list";
import { heldCounts, heldRows } from "./seen-outbox";
import { holdNotedSeen } from "./seen-rows";
import { heldSidesOf, useHeldVersion } from "./use-child-sessions";
import type { Interaction, Session } from "./types";

/** #8578: a directory-hidden session's pending cards leave the card lists and counts (Needs you, the sidebar, the Inbox,
 *  the assistants' badges) as its row leaves the lists; they stay answerable on that session's own page. The row comes
 *  from the lists or from the card's exact lookup (store.ts cardSessionRow), which returns hidden rows too. Display copies
 *  also stay out of these aggregate lists and counts; conversation pages read the store directly and retain both cards. */
export function directoryVisibleCards(cards: Interaction[]): Interaction[] {
  const visible = cards.filter((ix) => !ix.mirror_of && !cardSessionRow(ix.machine_id, ix.session_id)?.hidden_by_directory);
  return visible.length === cards.length ? cards : visible;
}

// Filter before folding: a hidden parent must not swallow a visible child, or a hidden child raise its parent’s state.
const directoryVisible = (rows: Session[]) => rows.some((s) => s.hidden_by_directory) ? rows.filter((s) => !s.hidden_by_directory) : rows;

// An unrelated fleet update must preserve memoized list rows. For an unchanged
// session only its external cards, delegation and mute can change the view.
const legacyRows = new WeakMap<Session, Session>();
function withLegacyView(session: Session, view: AgentView): Session {
  const previous = legacyRows.get(session);
  if (previous && sameValue(previous.view, view)) return previous;
  const row = { ...session, view };
  legacyRows.set(session, row);
  return row;
}

/** Apply the old-manager fallback before excluding hidden rows or folding children. */
export function listedAgents(rows: Session[], cards: readonly Interaction[] = [], mutes?: AgentMutes): Session[] {
  let sessions = rows;
  if (rows.some((s) => !s.view)) {
    const fallback = defaultManager();
    const managerOf = (row: { manager?: string }) => row.manager || fallback;
    const managers = new Set(rows.filter((s) => !s.view).map(managerOf));
    const resolved = new Map<Session, Session>();
    for (const manager of managers) {
      const own = rows.filter((s) => managerOf(s) === manager);
      const pending = cards.filter((ix) => managerOf(ix) === manager);
      const views = legacyAgentViews(own, pending, (s) => Boolean(mutes && isMuted(s, mutes)));
      for (const s of own) {
        const view = !s.view && views.get(agentKey(s));
        if (view) resolved.set(s, withLegacyView(s, view));
      }
    }
    sessions = rows.map((s) => resolved.get(s) ?? s);
  }
  return foldRows(directoryVisible(sessions)).filter((s) => !s.assistant);
}

/** The agents the lists show and count: without the child sessions folded under one of them, each parent in its folded
 *  state (lib/folded-rows.ts, #5120). A child's own row is in the store (`sessions`, `sessionSummaries`) for its card. */
export function useAgentSessions(): Session[] {
  const by = useStore((s) => s.sessions);
  const summaries = useStore((s) => s.sessionSummaries);
  const bounded = useStore((s) => s.features.includes(SESSIONS_LIST_FEATURE));
  const launches = useStore((s) => s.launches);
  const cards = useStore((s) => s.interactions);
  const mutes = useStore((s) => s.mutes);
  // folded before the assistant's conversations go: a child folded under one of them is no Agent of its own either
  return useMemo(() => listedAgents(heldLaunchRows(bounded ? summaries : Object.values(by).flat(), launches, Date.now()), cards, mutes), [by, summaries, bounded, launches, cards, mutes]);
}

/** The complete list as its readers get it: `error` says why it has not loaded (its read failed; it is read again in a
 *  few seconds, or now with retryFullAgentSessions), "" while it loads or once it has. */
export interface FullAgentSessions { sessions: Session[]; loaded: boolean; error: string }
const EMPTY_FULL_LIST = { sessions: [] as Session[], loaded: false, error: "", base: {} as Record<string, Session[]> };
const FULL_LIST_RETRY_MS = 5_000;
// Working agents emit list hints for every display update. Space reads from their completion so hints that keep
// arriving share one read. The complete list is read once; later reads ask only for what changed (complete-sessions.ts).
const FULL_LIST_REFRESH_MS = 3_000;

// A short handoff window covers Sidebar → AgentsPage and StrictMode remounts, not a permanent
// full-list cache. Only the active credential partition can own this one shared transport.
const FULL_LIST_GRACE_MS = 1_000;
let fullSnapshot = { ...EMPTY_FULL_LIST, epoch: 0 };
const fullListeners = new Set<() => void>();
const subscribeFull = (listener: () => void) => {
  fullListeners.add(listener);
  return () => { fullListeners.delete(listener); };
};
const fullChanged = () => fullListeners.forEach((listener) => listener());
const getFullSnapshot = () => fullSnapshot;
let fullCache: ReturnType<typeof createFullCache> | undefined;

function createFullCache(owner: string) {
  let stopped = false, consumers = 0, pending = false, dirty = false, urgent = false;
  let nextReadAt = 0;
  let controller: AbortController | undefined;
  const complete = new CompleteSessions();
  let retry: ReturnType<typeof setTimeout> | undefined;
  let trailing: ReturnType<typeof setTimeout> | undefined;
  let expiry: ReturnType<typeof setTimeout> | undefined;
  const valid = () => !stopped && credentialOwner() === owner && getState().features.includes(SESSIONS_LIST_FEATURE);
  const read = async (immediate = false) => {
    if (!valid()) return;
    if (!consumers || pending) { dirty = true; urgent ||= immediate; return; }
    const delay = nextReadAt - Date.now();
    if (!immediate && delay > 0) {
      // Hints share one fixed trailing deadline, even across consumer handoffs.
      trailing ??= setTimeout(() => { trailing = undefined; void read(); }, delay);
      return;
    }
    clearTimeout(trailing);
    trailing = undefined;
    clearTimeout(retry);
    retry = undefined;
    dirty = urgent = false;
    pending = true;
    controller = new AbortController();
    const base = getState().sessions;
    try {
      const result = await complete.read(controller.signal);
      if (valid()) observeLaunchRows(result.sessions);
      // An unchanged delta leaves the published snapshot (and its older base, which only widens the overlay) as it is.
      if (valid() && (result.changed || !fullSnapshot.loaded)) {
        const sessions = result.sessions;
        fullSnapshot = { sessions, loaded: true, error: "", base, epoch: fullSnapshot.epoch };
        holdNotedSeen(sessions);
        fullChanged();
      }
    } catch (e) {
      // An initial failure or quiet disconnected hub still needs a retry. During grace, retain
      // the retry intent but do not download data without a consumer.
      if (valid()) {
        // with nothing loaded yet the list says why (audit item 4: it stayed blank); a loaded list keeps its rows
        const error = errorText(e);
        if (!fullSnapshot.loaded && fullSnapshot.error !== error) {
          fullSnapshot = { ...fullSnapshot, error };
          fullChanged();
        }
        dirty = true;
        if (consumers) retry = setTimeout(() => { retry = undefined; void read(true); }, FULL_LIST_RETRY_MS);
      }
    } finally {
      pending = false;
      nextReadAt = Date.now() + FULL_LIST_REFRESH_MS;
      if (valid() && consumers && dirty && retry === undefined) void read(urgent);
    }
  };
  const off = ["sessions_list", "session", "sessions", "sessions_delta", "hello"].map((event) => onHubEvent(event, (msg) => {
    // A hint for the revision this copy already holds (the one on connect, a burst's repeat) needs no read.
    if (event === "sessions_list" && complete.holds(msg?.revision)) return;
    void read(event === "hello");
  }));
  const dispose = () => {
    if (stopped) return;
    stopped = true;
    controller?.abort();
    clearTimeout(retry);
    clearTimeout(trailing);
    clearTimeout(expiry);
    off.forEach((unsubscribe) => unsubscribe());
    fullCache = undefined;
    fullSnapshot = { ...EMPTY_FULL_LIST, epoch: fullSnapshot.epoch + 1 };
    fullChanged();
  };
  // Keep this fence subscribed through grace: a reset followed by the same login must not
  // revive an old snapshot, even when React batches both store publications into one commit.
  off.push(subscribe(() => { if (!valid()) dispose(); }));
  return {
    owner,
    retain() {
      consumers++;
      clearTimeout(expiry);
      expiry = undefined;
      if (!pending && (dirty || !fullSnapshot.loaded) && retry === undefined) void read(urgent || !fullSnapshot.loaded);
      return () => {
        if (stopped || --consumers) return;
        clearTimeout(retry);
        retry = undefined;
        if (trailing !== undefined) dirty = true;
        clearTimeout(trailing);
        trailing = undefined;
        expiry = setTimeout(dispose, FULL_LIST_GRACE_MS);
      };
    },
    dispose,
    /** Retry: read now, the list loading again (its error gone) until the answer. */
    again() {
      if (stopped) return;
      clearTimeout(retry);
      retry = undefined;
      if (fullSnapshot.error) {
        fullSnapshot = { ...fullSnapshot, error: "" };
        fullChanged();
      }
      void read(true);
    },
  };
}

/** The complete list's Retry: read it again now (lib/sessions.ts reads a failed one again by itself every few seconds). */
export function retryFullAgentSessions() {
  fullCache?.again();
}

/** Pins, grouping and the per-assistant lists need complete rows, including agents outside the bounded first page.
 * Consumers share a scoped snapshot and request, released shortly after the last one leaves. Folded like
 * useAgentSessions: no child session folded under a listed one, each parent in its folded state. */
export function useFullAgentSessions(on = true): FullAgentSessions {
  const full = useCompleteAgentSessions(on);
  const cards = useStore((s) => s.interactions);
  const mutes = useStore((s) => s.mutes);
  return useMemo(() => {
    const sessions = listedAgents(full.sessions, cards, mutes);
    return sessions === full.sessions ? full : { sessions, loaded: full.loaded, error: full.error };
  }, [full, cards, mutes]);
}

function useCompleteAgentSessions(on: boolean): FullAgentSessions {
  const full = useHubCompleteAgentSessions(on);
  // the agents seen on this page that the complete list's last read still has Unread: seen in every reader of it (the
  // row, the quick filters' counts, the groups' unread counts) at once, not when it is read again (lib/seen-outbox.ts)
  const marks = useStore((s) => s.seen);
  return useMemo(() => {
    const sessions = heldRows(full.sessions, marks, Date.now());
    return sessions === full.sessions ? full : { ...full, sessions };
  }, [full, marks]);
}

function useHubCompleteAgentSessions(on: boolean): FullAgentSessions {
  const by = useStore((s) => s.sessions);
  const launches = useStore((s) => s.launches);
  const bounded = useStore((s) => s.features.includes(SESSIONS_LIST_FEATURE));
  const loaded = useStore((s) => s.loaded);
  const snapshot = useSyncExternalStore(subscribeFull, getFullSnapshot, getFullSnapshot);
  const owner = on ? credentialOwner() : "";
  const full = fullCache?.owner === owner ? snapshot : EMPTY_FULL_LIST;
  useEffect(() => {
    // Hello confirms paging support. Fetch the complete list while the rest of the first refresh is in flight.
    if (!on || !bounded) return;
    if (fullCache && fullCache.owner !== owner) fullCache.dispose();
    fullCache ??= createFullCache(owner);
    return fullCache.retain();
  }, [on, bounded, owner, snapshot.epoch]);
  return useMemo(() => {
    // `on` false: the caller has no use for the list now and asks the hub for nothing
    if (!on) return EMPTY_FULL_LIST;
    if (!bounded) return { sessions: heldLaunchRows(Object.values(by).flat(), launches, Date.now()), loaded, error: "" };
    // Wait for the first refresh's pins and grouping inputs before showing the complete snapshot.
    if (!loaded) return EMPTY_FULL_LIST;
    if (by === full.base) {
      const held = heldLaunchRows(full.sessions, launches, Date.now());
      return held === full.sessions ? full : { sessions: held, loaded: full.loaded, error: full.error };
    }
    // Reflect actions on retained details (rename, mark seen) without copying the complete list into the store.
    // Unchanged cached details may predate this HTTP snapshot, so only apply rows changed since its request.
    const key = (s: Session) => JSON.stringify([s.machine_id, s.session_id]);
    const before = new Map(Object.values(full.base).flat().map((s) => [key(s), s]));
    const current = new Map(Object.values(by).flat().map((s) => [key(s), s]));
    const sessions = heldLaunchRows(full.sessions.filter((s) => !before.has(key(s)) || current.has(key(s))).map((s) => {
      const next = current.get(key(s));
      return next && next !== before.get(key(s)) ? next : s;
    }), launches, Date.now());
    return { sessions, loaded: full.loaded, error: full.error };
  }, [on, bounded, loaded, full, by, launches]);
}

/** The agents by state. `badge` is the sidebar's "Needs you" count: without quiet tasks' agents, which the main conversation
 * tells about (#1731). */
/** `waiting`: working, but only waiting on background commands (#6045, backgroundWaiting): not in `working`, as the
 *  sidebar's quick filters count them (#10938). */
export function stateCounts(all: Session[], interactions: readonly Interaction[] = [], grading = false) {
  const c = { needs: 0, working: 0, waiting: 0, unread: 0, badge: 0 };
  // "Needs you" only for cards a task's supervisor holds (lib/supervisor-held.ts): the supervisor is at work on them, so
  // it is counted Working, as the overview lists it, in neither the number nor the badge (the hub's `supervisor_held`)
  const held = supervisorHeldOnly(interactions);
  for (const s of all) {
    if (s.hidden_by_directory) continue;
    const st = stateOf(s);
    if (s.view?.counted) { c.needs++; c.badge++; }
    if (s.view && st === "needs_you") continue;
    if (st === "needs_you" && held.has(`${s.machine_id}/${s.session_id}`)) c.working++;
    else if (st === "needs_you") {
      c.needs++;
      if (!s.quiet && (!grading || !heldOnlyNeeds(s, interactions))) c.badge++;
    } else if (st === "working") {
      if (backgroundWaiting(s)) c.waiting++;
      else c.working++;
    } else if (activityOf(s) === "unread") c.unread++;
  }
  return c;
}

/** A paging hub's counts as the lists read them (useCounts). Its State needs_you has in it the agents that wait only on
 *  cards a task's supervisor holds (`supervisor_held`, apart from `held_needs`): Working here, as stateCounts counts
 *  them, in neither the number nor the badge, graded or not. */
export function boundedCounts(counts: Pick<ListCounts, "state" | "background_waiting" | "held_needs" | "supervisor_held">, grading: boolean) {
  const supervised = Math.max(0, Math.min(counts.supervisor_held ?? 0, counts.state.needs_you ?? 0));
  const needs = Math.max(0, (counts.state.needs_you ?? 0) - supervised);
  // the hub leaves the ones that only wait in the background in state.working for older apps (protocol.md "Counts")
  const waiting = Math.min(counts.background_waiting ?? 0, counts.state.working ?? 0);
  const badge = grading ? Math.max(0, needs - (counts.held_needs ?? 0)) : needs;
  return { needs, working: (counts.state.working ?? 0) - waiting + supervised, waiting, unread: counts.state.unread ?? 0, badge };
}

/** `badge` without `open`, the agent on screen: it says it is waiting itself, so a count of only it would tell nothing.
 *  `quietCounted`: `badge` is the hub's own count, which has quiet agents in it. `held`: open agent was excluded for grading. */
export function othersWaiting(badge: number, open: Session | undefined, quietCounted = false, held = false): number {
  return Math.max(0, badge - (open && !open.assistant && !open.hidden_by_directory && (open.view ? open.view.counted : (quietCounted || !open.quiet) && !held && foldedState(open) === "needs_you") ? 1 : 0));
}

const scopedAgentCounts = () => runtimeAgents.getSnapshot().counts;
const scopedAgentRows = () => runtimeAgents.getSnapshot().rows;
const listedRows = () => runtimeSessions.getSnapshot().rows;

/** A newer manager can supply views through a paging hub with an older hello. */
export function hasAgentViews(s: Pick<State, "features" | "sessionSummaries">): boolean {
  return s.features.includes("agent_view") || (s.features.includes(SESSIONS_LIST_FEATURE) && s.sessionSummaries.some(row => !!row.view));
}

export function useCounts() {
  const all = useAgentSessions();
  const typed = useStore(hasAgentViews);
  const complete = useFullAgentSessions(typed);
  const allInteractions = useStore((s) => s.interactions);
  const grading = useStore((s) => s.pushGrading);
  // the cards waiting on the user: not those a task's supervisor holds (lib/supervisor-held.ts)
  const interactions = useMemo(() => userCards(directoryVisibleCards(allInteractions)).length, [allInteractions]);
  const { bounded, pagedAgents, aggregate } = useStore((s) => ({
    bounded: s.features.includes(SESSIONS_LIST_FEATURE),
    pagedAgents: supportsPagedAgents(s.features),
    aggregate: s.sessionCounts,
  }));
  const scoped = useSyncExternalStore(runtimeAgents.subscribe, scopedAgentCounts, scopedAgentCounts);
  // the rows read with those counts: an agent seen on this page that they still have Unread is not counted so
  // (lib/seen-outbox.ts heldCounts), however long the list takes to read again
  const agentRows = useSyncExternalStore(runtimeAgents.subscribe, scopedAgentRows, scopedAgentRows);
  const rows = useSyncExternalStore(runtimeSessions.subscribe, listedRows, listedRows);
  const marks = useStore((s) => s.seen);
  // a parent whose unread side sessions were all seen here is not counted Unread either (heldCounts' sidesOf)
  const heldAt = useHeldVersion();
  const read = pagedAgents ? scoped : aggregate;
  // only where a page's counts come with its rows (sessions_page_counts): two reads' rows and counts could disagree
  const coupled = (pagedAgents ? runtimeAgents : runtimeSessions).usesPageCounts();
  // eslint-disable-next-line react-hooks/exhaustive-deps -- heldAt: heldSidesOf reads the held rows, which it versions
  const counts = useMemo(() => read && coupled ? heldCounts(read, pagedAgents ? agentRows : rows, marks, Date.now(), heldSidesOf) : read, [read, coupled, pagedAgents, agentRows, rows, marks, heldAt]);
  return useMemo(() => {
    if (typed) return { ...stateCounts(complete.loaded ? complete.sessions : all, allInteractions, grading), interactions, quietCounted: false };
    if (!bounded || !counts) return { ...stateCounts(all, allInteractions, grading), interactions, quietCounted: false };
    return { ...boundedCounts(counts, grading), interactions, quietCounted: true };
  }, [all, allInteractions, grading, interactions, counts, bounded, typed, complete]);
}

// A read that follows a failed one counts as failed until one comes back: each attempt clears the list's error while it
// runs, and an agent's change of the list starts one, so the failure line and its Retry would otherwise turn into
// "Loading…" under the pointer and back. Retry itself loads again until the answer (retryAgentList), as the complete
// list's Retry does.
let listFailed = false;
runtimeSessions.subscribe(() => {
  const list = runtimeSessions.getSnapshot();
  if (list.loaded || (!list.loading && !list.error)) listFailed = false; // read, or reset to nothing read
  else if (list.error) listFailed = true;
});
const listStatus = (): AgentListStatus => {
  const list = runtimeSessions.getSnapshot();
  return list.loaded ? "loaded" : list.error || listFailed ? "failed" : "loading";
};
const listHasMore = () => runtimeSessions.getSnapshot().hasMore;
export type AgentListStatus = "loaded" | "loading" | "failed";
/** `whole`: the rows read are every agent (no page left to read), so they can be counted, e.g. by computer. */
export type AgentList = { status: AgentListStatus; whole: boolean };
const PUSHED_LIST: AgentList = { status: "loaded", whole: true };

/** Whether the agents' counts can be told. A hub that pages its Agents list pushes no sessions: the list is read over
 *  HTTP (`runtimeSessions`), and until that read has come back its counts are unknown, not zero: "loading", or "failed"
 *  when it failed (read again with the hub's next change of the list, or retryAgentList). Its first pages are not every
 *  agent (`whole` false) while it has more. A hub that pushes them: "loaded", whole. */
export function useAgentListStatus(): AgentList {
  const bounded = useStore((s) => s.features.includes(SESSIONS_LIST_FEATURE));
  const status = useSyncExternalStore(runtimeSessions.subscribe, listStatus, listStatus);
  const more = useSyncExternalStore(runtimeSessions.subscribe, listHasMore, listHasMore);
  return useMemo(() => (bounded ? { status, whole: status === "loaded" && !more } : PUSHED_LIST), [bounded, status, more]);
}

/** Retry after the agents' read failed: reads at once, loading until the answer, the Agents list's first page
 *  (useAgentListStatus "failed") and the complete list (useFullAgentSessions `error`, which is otherwise read again
 *  every few seconds), whichever has not loaded. */
export function retryAgentList() {
  if (!runtimeSessions.getSnapshot().loaded) {
    listFailed = false;
    void runtimeSessions.load().catch(() => {});
  }
  if (!fullSnapshot.loaded) retryFullAgentSessions();
}
