import { refreshAgentsDisplay } from "./agents-display";
// The list transport stays separate from complete, subscribed chat details.
import { SESSIONS_LIST_FEATURE, SessionList, compareListIds } from "./session-list";
import { foldedState } from "./child-sessions";
import { t } from "../i18n";
import type { Session } from "./types";
import type { SessionPlace } from "./session-groups";
import { byTime } from "./times";

// Match Go's JSON tuple spelling, including HTML and line-separator escaping.
const GO_JSON_ESCAPE = new RegExp("[<>&" + String.fromCharCode(8232, 8233) + "]", "g");
const tupleKey = (tuple: string[]) => JSON.stringify(tuple).replace(GO_JSON_ESCAPE, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`);
const identities = new WeakMap<Session, string>();
export const runtimeIdentity = (s: Session) => {
  // Row identities are immutable. Serialize once while the bounded cache retains the row, not on every sort.
  let key = identities.get(s);
  if (key === undefined) { key = tupleKey([s.machine_id, s.session_id]); identities.set(s, key); }
  return key;
};
/** The identity of the row a session is drawn under (`parent_agent_id`, #5935, lib/side-sessions.ts), as runtimeIdentity
 *  keys rows: on the session's own machine. */
export const runtimeParentIdentity = (s: Session) => s.parent_agent_id ? tupleKey([s.machine_id, s.parent_agent_id]) : undefined;
export function runtimeRemoval(id: string): string {
  try {
    const tuple: unknown = JSON.parse(id);
    if (Array.isArray(tuple) && tuple.length === 2 && tuple.every((part) => typeof part === "string")) return tupleKey(tuple);
  } catch { /* A malformed removal must not silently leave a stale row. */ }
  throw new Error(t("paged-agents.invalid_page"));
}
export const runtimeOrder = (a: Session, b: Session) => {
  const time = byTime(b.updated_at, a.updated_at);
  if (time) return time;
  // JavaScript timestamps drop sub-millisecond precision; Go's transport order does not.
  const fraction = (s: Session) => (/\.(\d+)/.exec(s.updated_at ?? "")?.[1] ?? "").padEnd(9, "0");
  const aFraction = fraction(a), bFraction = fraction(b);
  if (aFraction !== bFraction) return aFraction < bFraction ? 1 : -1;
  return compareListIds(runtimeIdentity(a), runtimeIdentity(b));
};
/** A row's last activity, the time `runtimeOrder` sorts by (NaN without one: it sorts last and counts as earlier). */
export const runtimeActiveAt = (s: Session) => Date.parse(s.updated_at ?? "");
/** Needs the user by the hub's rule for `state.needs_you` counts, not the client's answered-here state: by the folded
 *  state, which a child that waits gives its parent (`roots=1` counts it so, #5120). */
export const runtimeNeedsYou = (s: Session) => s.view ? s.view.counted === true : foldedState(s) === "needs_you";
/** Where a row is, by the attributes the hub groups and filters it by (docs/protocol/protocol.md "Bounded Agents
 *  lists"): its computer, its project directory and its space. */
// A Just finished row's state section is Just finished's on a hub that lists it apart (lib/session-groups.ts, #9751).
export const runtimePlace = (s: Session): SessionPlace => ({ machine: s.machine_id, project: s.repo_root || s.workspace_cwd || s.cwd || "", group: s.space_id || s.workspace_id || "", todo: s.todo?.id ?? "",
  state: s.state === "idle" && s.work?.just_finished ? "just_finished" : s.state });
export const SESSIONS_LOOKUP_FEATURE = "sessions_lookup";
export const SESSIONS_PAGE_COUNTS_FEATURE = "sessions_page_counts";
export const SESSIONS_AGENTS_ONLY_FEATURE = "sessions_agents_only";
export const supportsPagedAgents = (features: readonly string[]) => features.includes(SESSIONS_LIST_FEATURE) && features.includes(SESSIONS_AGENTS_ONLY_FEATURE);
export const RUNTIME_AGENTS_PATH = "/sessions?agents_only=1";
let pageCounts = false;
/** Set from hello: whether this hub reads a page's counts with it (`with_counts`, #4858). */
export function setRuntimePageCounts(on: boolean) { pageCounts = on; }
const changeListeners = new Set<(upsert: Session[], remove: string[]) => void>();
export function onRuntimeChanges(listener: (upsert: Session[], remove: string[]) => void) {
  changeListeners.add(listener);
  return () => { changeListeners.delete(listener); };
}
export const runtimeSessions = new SessionList<Session>("/sessions", runtimeIdentity, runtimeOrder, undefined, runtimeRemoval, {
  onDelta: (upsert, remove) => changeListeners.forEach((listener) => listener(upsert, remove)),
  pageCounts: () => pageCounts,
});
// A scoped removal can mean that a row is still present in the all-sessions list.
export const runtimeAgents = new SessionList<Session>(RUNTIME_AGENTS_PATH, runtimeIdentity, runtimeOrder, undefined, runtimeRemoval, {
  onRefresh: () => { void refreshAgentsDisplay(); },
  pageCounts: () => pageCounts,
});
