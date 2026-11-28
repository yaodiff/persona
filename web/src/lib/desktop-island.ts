// The island at the MacBook notch (#7669, docs/desktop/desktop.md "Island at the MacBook notch"): what the desktop app
// draws there, worked out from the Agents list: its counts and its rows from the same list, so the pill never counts an
// agent its list cannot show. Sent to a desktop that has the island; the desktop keeps the latest and ignores it while
// the island is off.
import { useEffect, useMemo, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { useStore } from "./store";
import { sessionTitle, activityOf, stateOf } from "./format";
import { sessionCard } from "./folded-rows";
import { paneRoute } from "./herdr-tree";
import { rowLine } from "./row-line";
import { href } from "./router";
import { useAgentSessions, useFullAgentSessions } from "./sessions";
import { stateCount, stateName } from "./state-labels";
import type { Interaction, Session } from "./types";
import type { DesktopIsland, DesktopIslandRow, DesktopIslandState } from "./desktop-types";
import { byTime } from "./times";

/** The most rows the island lists, as the desktop draws them. */
export const ISLAND_ROWS = 5;

const RANK: Record<string, number> = { needs_you: 0, working: 1, unread: 2 };

/** A row line's colored lead and its words on one line, as the Agents list sets them side by side: no space after a
 *  full-width colon or a lead that ends in one. */
function joinLead(lead: string, text: string): string {
  if (!lead || !text) return lead || text;
  return /[：\s]$/.test(lead) ? lead + text : `${lead} ${text}`;
}

/** The island's state: the agents by state, and the ones that need you, then the working ones, then the ones that
 *  finished unseen, each with the line its Agents-list row shows (what it asks, is doing or did). The counts are of the
 *  same agents as the rows, the Agents list's: not the Persona assistant's conversations, which the Agents list leaves
 *  out. Within a state, the one that entered it last comes first (`state_since`): a working agent's updates do not move
 *  its row while the list is open. */
export function islandState(sessions: readonly Session[], interactions: readonly Interaction[]): DesktopIslandState {
  // the card a row speaks for, as the Agents list finds it: its own, else one a child session folded under it waits on
  const card = (s: Session) => sessionCard(interactions, s.machine_id, s.session_id);
  const since = (s: Session) => s.state_since ?? s.updated_at;
  // filter makes the copy that is sorted: the hooks' shared lists are never reordered
  const agents = sessions.filter((s) => !s.assistant && activityOf(s) in RANK && (stateOf(s) !== "needs_you" || !s.view || s.view.counted));
  const rows = agents
    .sort((a, b) => RANK[activityOf(a)] - RANK[activityOf(b)] || byTime(since(b), since(a)))
    .slice(0, ISLAND_ROWS)
    .map((s): DesktopIslandRow => {
      const state = activityOf(s);
      const words = state === "working" ? undefined : rowLine(s, card(s));
      const line = words ? joinLead(words.lead ?? "", words.text) : (s.activity?.trim() ?? "");
      return { title: sessionTitle(s), state, label: stateName(state), line, route: href(paneRoute(s)) };
    });
  const count = (state: string) => agents.filter((s) => activityOf(s) === state).length;
  const counts = { needs: count("needs_you"), working: count("working"), unread: count("unread") };
  const summary = [
    counts.needs > 0 && stateCount("needs_you", counts.needs),
    counts.working > 0 && stateCount("working", counts.working),
    counts.unread > 0 && stateCount("unread", counts.unread),
  ].filter(Boolean).join(t("home.summary_separator"));
  return { needsYou: counts.needs, working: counts.working, unread: counts.unread, rows, summary };
}

// Whether the desktop draws the island: its answer, asked when the web starts and given again by the Settings switch,
// the one place that turns it on or off.
let islandOn = false;
const islandListeners = new Set<() => void>();
const subscribeIsland = (listener: () => void) => {
  islandListeners.add(listener);
  return () => { islandListeners.delete(listener); };
};
const getIslandOn = () => islandOn;

/** Keeps the desktop's answer (DesktopIsland.enabled) for the island's rows: Settings gives it after the switch. */
export function setIslandOn(on: boolean): void {
  if (on === islandOn) return;
  islandOn = on;
  islandListeners.forEach((listener) => listener());
}

/** What the island shows of nobody: once the shell is gone (signed out, or the credential stopped working). */
const NOTHING: DesktopIslandState = { needsYou: 0, working: 0, unread: 0, rows: [], summary: "" };

/** Sends the island nothing of this account: the credential stopped working, or the shell is gone. */
export function clearDesktopIsland(): void {
  window.personaDesktop?.setIslandState?.(NOTHING);
}

/** Sends the island's state when it changes. A leaf, as AttentionSignals: a sessions push re-renders only it. */
export function DesktopIslandSignals(): null {
  const on = useSyncExternalStore(subscribeIsland, getIslandOn, getIslandOn);
  useEffect(() => {
    let live = true;
    window.personaDesktop?.getIsland?.().then((v: DesktopIsland) => { if (live) setIslandOn(v.enabled); }, () => {});
    return () => { live = false; };
  }, []);
  // The counts and the rows are the whole hub's: a hub with the paged Agents list pushes only its first page, so while the
  // island is on (it is off by default) they come from the complete list, the pushed page until it has loaded.
  const pushed = useAgentSessions();
  const complete = useFullAgentSessions(on);
  const sessions = complete.loaded ? complete.sessions : pushed;
  const interactions = useStore((s) => s.interactions);
  // No language among the inputs: a page shows one, and choosing another reloads it (i18n setLanguagePreference).
  const state = useMemo(() => islandState(sessions, interactions), [sessions, interactions]);
  const sent = JSON.stringify(state);
  useEffect(() => {
    window.personaDesktop?.setIslandState?.(JSON.parse(sent) as DesktopIslandState);
  }, [sent]);
  useEffect(() => () => clearDesktopIsland(), []);
  return null;
}
