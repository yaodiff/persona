// An agent's page switches its middle in place between its conversation and its terminal (docs/web/web.md "Terminal ⇄ chat"),
// as Moshi's session page does: a "Terminal | Chat" switch in the header and ⌘J. Which one shows is the address's
// `?view=chat|term`; an address without it shows what that agent's page showed last (kept per browser), else the
// conversation. Pure helpers here, so they are tested without a DOM; the hook is the page's side of it.
import { useCallback, useEffect, useRef, useState } from "react";
import { isStoppedAgent, sessionCan, activityOf } from "./format";
import { paneRoute } from "./herdr-tree";
import { navigate, useRoute, type Route, type SessionView } from "./router";
import { isPaneAddress } from "./terminals";
import type { OutboxEntry } from "./outbox";
import type { Interaction, Session } from "./types";

const KEY = "persona.sessionView";
/** How many agents' last view are kept; the oldest go first. */
const KEEP = 300;

type Store = Pick<Storage, "getItem" | "setItem">;

export const otherView = (v: SessionView): SessionView => (v === "term" ? "chat" : "term");

/** The agent has a terminal to show in place: its source lets a terminal open, and its pane is live or it is a stopped
 *  Agent (#4754), whose terminal is the shell it left, or Resume when that is gone. */
export function canShowTerminal(s: Session | undefined): boolean {
  return Boolean(s && sessionCan(s, "terminal") && (activityOf(s) !== "offline" || isStoppedAgent(s)));
}

/** The agent's terminal waits on a dialog (#7343): herdr calls the agent blocked, a screen card stands for what its
 *  terminal shows, or the newest message sent here was refused because a dialog had the terminal (dialog_open). What
 *  lights the terminal button while the conversation is shown. */
export function terminalWaits(
  s: Session | undefined,
  interactions: readonly Pick<Interaction, "kind">[],
  outbox: readonly Pick<OutboxEntry, "state" | "code" | "at">[] = [],
): boolean {
  if (!s || !canShowTerminal(s)) return false;
  const newest = outbox.reduce<Pick<OutboxEntry, "state" | "code" | "at"> | undefined>((a, e) => (!a || e.at >= a.at ? e : a), undefined);
  return s.status === "blocked" || interactions.some((ix) => ix.kind === "screen") || (newest?.state === "failed" && newest.code === "dialog_open");
}

/** A chat that cannot show its terminal in place but has one (the side panel, #7343): its terminal button opens the
 *  agent's page in the terminal view. Not the assistant's chats, not an embedded one, not a page (it switches in place). */
export function terminalJump(s: Session | undefined, o: { inPanel?: boolean; assistant?: boolean }): boolean {
  return Boolean(o.inPanel && !o.assistant && canShowTerminal(s));
}

/** What sits where an agent chat's composer goes: the composer, or a stopped Agent's read-only line (#4754) with
 *  Terminal and Resume. Not for the assistant's chats, or while the computer is offline (the composer's offline line
 *  says that, and Resume could not reach it). */
export function composerSlot(s: Session | undefined, o: { assistant?: boolean; offline?: boolean } = {}): "composer" | "stopped" {
  return !o.assistant && !o.offline && isStoppedAgent(s) ? "stopped" : "composer";
}

/** The composer says "view-only": a row whose source or capabilities refuse sending. A native row from an older node
 *  (no capabilities) is not one: a message still resumes it there. */
export function viewOnlyRow(s: Session | undefined): boolean {
  return Boolean(s && !sessionCan(s, "send") && (s.source_kind || s.capabilities));
}

/** The view to draw: the address's, else the one remembered, else the conversation; always the conversation when there is no terminal. */
export function resolveView(url: SessionView | undefined, remembered: SessionView | undefined, canTerminal: boolean): SessionView {
  return canTerminal ? (url ?? remembered ?? "chat") : "chat";
}

const keyOf = (mid: string, sid: string) => `${mid}/${sid}`;

function readAll(store: Store): Record<string, SessionView> {
  try {
    const v: unknown = JSON.parse(store.getItem(KEY) ?? "null");
    if (!v || typeof v !== "object" || Array.isArray(v)) return {};
    return Object.fromEntries(Object.entries(v).filter(([, x]) => x === "chat" || x === "term")) as Record<string, SessionView>;
  } catch {
    return {};
  }
}

/** What an agent's page showed last in this browser. */
export function readView(mid: string, sid: string, store: Store = localStorage): SessionView | undefined {
  return readAll(store)[keyOf(mid, sid)];
}

export function rememberView(mid: string, sid: string, view: SessionView, store: Store = localStorage) {
  const all = readAll(store);
  if (all[keyOf(mid, sid)] === view) return;
  delete all[keyOf(mid, sid)]; // set again below, so the newest sits last
  all[keyOf(mid, sid)] = view;
  const keys = Object.keys(all);
  for (const k of keys.slice(0, Math.max(0, keys.length - KEEP))) delete all[k];
  try {
    store.setItem(KEY, JSON.stringify(all));
  } catch {
    // a full or blocked store: the view is just not remembered
  }
}

/**
 * Where a terminal opened from a list of panes (the "Terminal" page's tree, the sidebar's, ⌘K) goes: an agent's pane to its
 * page in the terminal view, a plain shell (it has no conversation) to the "Terminal" page.
 */
export function terminalTarget(mid: string, id: string, sessions: readonly Session[]): Route {
  if (!isPaneAddress(id)) {
    const s = sessions.find((x) => x.session_id === id);
    if (s && canShowTerminal(s)) return { ...paneRoute(s), view: "term" } as Route;
  }
  return { name: "terminal", mid, id };
}

/** The agent page's own view: what shows, and how to switch it (the address changes, in place: Back leaves the page). */
export function useSessionView(mid: string, sid: string, canTerminal: boolean): [SessionView, (v: SessionView) => void] {
  const route = useRoute();
  const [remembered, setRemembered] = useState(() => readView(mid, sid));
  const onPage = route.name === "session" || route.name === "tab";
  const work = onPage && route.work === true;
  const view = resolveView(onPage ? route.view : undefined, remembered, canTerminal);
  useEffect(() => {
    if (!canTerminal || work) return;
    rememberView(mid, sid, view);
    setRemembered(view);
  }, [mid, sid, view, canTerminal, work]);
  // one function for the page's life, reading the address when called: the chat's header is memoized on it
  const at = useRef(route);
  at.current = route;
  const setView = useCallback((v: SessionView) => {
    const now = at.current;
    if (now.name !== "session" && now.name !== "tab") return;
    // In the browser only a user's mode change is a preference. A pending-card link and a temporary pane loss are not.
    if (now.work) {
      rememberView(mid, sid, v);
      setRemembered(v);
    }
    navigate({ ...now, view: v }, true);
  }, [mid, sid]);
  return [view, setView];
}
