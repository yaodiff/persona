// What an Agents row read from Unread or Just finished does once it is seen (#12585; docs/design/DESIGN_SYSTEM.md 3.4,
// "Rows read during a visit"; iOS SeenRows / AppModel.seenThisVisit). Kept by this browser: Settings › General › Agent
// preferences › Seen Agents. The visit's held set lives in memory only, so a reload starts fresh.
import { useMemo, useSyncExternalStore } from "react";
import type { HeldOrderOptions, HeldPlace, HeldSeenMap } from "./agent-groups";
import { activityOf } from "./format";
import { agentKey } from "./pins";
import { seenCopy, unseen } from "./seen-outbox";
import { sameValue } from "./stable-lines";
import type { Session } from "./types";

export type SeenRowsMode = "stay" | "leave";

export const SEEN_ROWS_KEY = "agents.seenRows";
export const SEEN_ROWS_NOTICED_KEY = "agents.seenRows.noticed";

const prefListeners = new Set<() => void>();
const fallback: { mode?: SeenRowsMode; noticed?: boolean } = {};
const readStorage = (k: string) => { try { return localStorage.getItem(k); } catch { return undefined; } };
const writeStorage = (k: string, v: string | null) => {
  try { if (v === null) localStorage.removeItem(k); else localStorage.setItem(k, v); return true; } catch { return false; }
};

export function seenRowsMode(): SeenRowsMode {
  const s = readStorage(SEEN_ROWS_KEY);
  return s === "leave" || s === "stay" ? s : fallback.mode ?? "stay";
}

export function seenRowsNoticed(): boolean {
  const s = readStorage(SEEN_ROWS_NOTICED_KEY);
  return s === "1" ? true : s === null ? (fallback.noticed ?? false) : fallback.noticed ?? false;
}

export function setSeenRowsNoticed(done: boolean): void {
  fallback.noticed = writeStorage(SEEN_ROWS_NOTICED_KEY, done ? "1" : null) ? undefined : done;
  prefListeners.forEach((f) => f());
}

export function setSeenRowsMode(mode: SeenRowsMode): void {
  fallback.mode = writeStorage(SEEN_ROWS_KEY, mode === "leave" ? "leave" : null) ? undefined : mode;
  fallback.noticed = writeStorage(SEEN_ROWS_NOTICED_KEY, "1") ? undefined : true;
  if (mode === "leave") releaseAllSeen();
  prefListeners.forEach((f) => f());
}

function subscribePrefs(listener: () => void): () => void {
  prefListeners.add(listener);
  const onStorage = (e: StorageEvent) => {
    if (!e.key || e.key === SEEN_ROWS_KEY || e.key === SEEN_ROWS_NOTICED_KEY) listener();
  };
  if (typeof window !== "undefined") window.addEventListener("storage", onStorage);
  return () => {
    prefListeners.delete(listener);
    if (typeof window !== "undefined") window.removeEventListener("storage", onStorage);
  };
}

export const useSeenRowsMode = (): SeenRowsMode => useSyncExternalStore(subscribePrefs, seenRowsMode, () => "stay");
export const useSeenRowsNoticed = (): boolean => useSyncExternalStore(subscribePrefs, seenRowsNoticed, () => false);

// ── in-memory visit state (cleared on reload / reset) ───────────────────────

export interface SeenVisitSnapshot {
  seen: HeldSeenMap;
  open: Readonly<Record<string, HeldPlace>>;
  snapshots: Readonly<Record<string, Session>>;
}

let shownThisVisit = new Map<string, HeldPlace>();
let visit: SeenVisitSnapshot = { seen: {}, open: {}, snapshots: {} };
const visitListeners = new Set<() => void>();
const EMPTY_HELD: HeldOrderOptions = {};

function emitVisit(next: SeenVisitSnapshot) {
  visit = next;
  visitListeners.forEach((f) => f());
}

const subscribeVisit = (l: () => void) => { visitListeners.add(l); return () => { visitListeners.delete(l); }; };
export const getSeenVisit = (): SeenVisitSnapshot => visit;
export const getShownThisVisitForTest = (): ReadonlyMap<string, HeldPlace> => shownThisVisit;
export const useSeenVisit = (): SeenVisitSnapshot => useSyncExternalStore(subscribeVisit, getSeenVisit, getSeenVisit);

export function heldOrderFor(mode: SeenRowsMode, state: SeenVisitSnapshot): HeldOrderOptions {
  const hasOpen = Object.keys(state.open).length > 0;
  if (mode === "leave") return hasOpen ? { heldSeen: state.open } : EMPTY_HELD;
  const hasSeen = Object.keys(state.seen).length > 0;
  if (!hasSeen && !hasOpen) return EMPTY_HELD;
  return { heldSeen: hasOpen ? (hasSeen ? { ...state.seen, ...state.open } : state.open) : state.seen };
}

export function useHeldSeenOrder(mode: SeenRowsMode): HeldOrderOptions {
  const state = useSeenVisit();
  return useMemo(() => heldOrderFor(mode, state), [mode, state]);
}

export function noteShownUnseen(rows: Readonly<Record<string, HeldPlace>>, currentSessions?: readonly Session[]): void {
  if (seenRowsMode() !== "stay") return;
  const keys = Object.keys(rows);
  if (!keys.length) return;
  for (const key of keys) shownThisVisit.set(key, rows[key]!);
  if (currentSessions) holdNotedSeen(currentSessions, keys);
}

export function holdNotedSeen(sessions: readonly Session[], onlyKeys?: Iterable<string>): void {
  const stay = seenRowsMode() === "stay";
  const hasShown = stay && shownThisVisit.size > 0;
  if (!hasShown && !Object.keys(visit.snapshots).length) return;

  const byKey = new Map<string, Session>();
  for (const s of sessions) byKey.set(agentKey(s), s);

  const seen: Record<string, HeldPlace> = { ...visit.seen };
  const snapshots: Record<string, Session> = { ...visit.snapshots };
  let changed = false;

  if (hasShown) {
    for (const key of onlyKeys ?? shownThisVisit.keys()) {
      const place = shownThisVisit.get(key);
      if (!place) continue;
      const s = byKey.get(key);
      const st = s ? activityOf(s) : undefined;
      if (s && (st === "idle" || st === "unknown") && !unseen(s)) shownThisVisit.delete(key);
      else if (!(key in seen)) continue;
      if (seen[key] !== place) { seen[key] = place; changed = true; }
      if (s && !sameValue(snapshots[key], s)) { snapshots[key] = s; changed = true; }
    }
  }
  for (const key of Object.keys(snapshots)) {
    const s = byKey.get(key);
    if (s && !sameValue(snapshots[key], s)) { snapshots[key] = s; changed = true; }
  }
  if (changed) emitVisit({ seen, open: visit.open, snapshots });
}

export function recordMarkedSeen(s: Session, place: HeldPlace, isViewed: boolean): void {
  const key = agentKey(s);
  const copy = seenCopy(s);
  const open = isViewed && visit.open[key] !== place ? { ...visit.open, [key]: place } : visit.open;
  const keep = seenRowsMode() === "stay" && (!isViewed || shownThisVisit.has(key) || key in visit.seen);
  if (keep) shownThisVisit.delete(key);
  const seen = keep && visit.seen[key] !== place ? { ...visit.seen, [key]: place } : visit.seen;
  const snapshots = (key in open || key in seen) && !sameValue(visit.snapshots[key], copy) ? { ...visit.snapshots, [key]: copy } : visit.snapshots;
  if (open !== visit.open || seen !== visit.seen || snapshots !== visit.snapshots) emitVisit({ seen, open, snapshots });
}

export function releaseOpenSeen(key: string): void {
  if (!(key in visit.open)) return;
  const open = { ...visit.open };
  delete open[key];
  let snapshots = visit.snapshots;
  if (!(key in visit.seen) && key in snapshots) {
    const next = { ...snapshots };
    delete next[key];
    snapshots = next;
  }
  emitVisit({ seen: visit.seen, open, snapshots });
}

export function releaseSeen(keys: Iterable<string>): void {
  const seen: Record<string, HeldPlace> = { ...visit.seen };
  const open: Record<string, HeldPlace> = { ...visit.open };
  const snapshots: Record<string, Session> = { ...visit.snapshots };
  let changed = false;
  for (const key of keys) {
    shownThisVisit.delete(key);
    if (key in seen) { delete seen[key]; changed = true; }
    if (key in open) { delete open[key]; changed = true; }
    if (key in snapshots) { delete snapshots[key]; changed = true; }
  }
  if (changed) emitVisit({ seen, open, snapshots });
}

export function releaseAllSeen(keeping?: Readonly<Record<string, HeldPlace>>): void {
  shownThisVisit = new Map(seenRowsMode() === "stay" && keeping ? Object.entries(keeping) : []);
  if (Object.keys(visit.seen).length || Object.keys(visit.open).length || Object.keys(visit.snapshots).length) {
    emitVisit({ seen: {}, open: {}, snapshots: {} });
  }
}

export function resetSeenVisit(): void {
  releaseAllSeen();
}
