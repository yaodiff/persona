import { agentId, sessionAgent } from "./agent-kind";
import { foldRows } from "./folded-rows";
import { activityOf } from "./format";
import { runtimeOrder } from "./runtime-list";
import { isSidebarAgent } from "./sidebar-agent";
import type { Session } from "./types";

/** History uses the sidebar's folded state: an offline parent with live/pending children stays with live work. */
export function historySessions(rows: Session[], filters: { machine?: string; agent?: string } = {}): Session[] {
  return foldRows(rows).filter(s => !s.deleted && isSidebarAgent(s) && activityOf(s) === "offline" &&
    (!filters.machine || s.machine_id === filters.machine) && (!filters.agent || agentId(sessionAgent(s).agent) === filters.agent)).sort(runtimeOrder);
}
export interface HistoryGroup { key: string; date?: Date; sessions: Session[] }
/** Calendar days in the reader's timezone, never UTC date substrings. Counts are exactly the rows in each group. */
export function historyGroups(rows: Session[]): HistoryGroup[] {
  const groups = new Map<string, HistoryGroup>();
  for (const s of [...rows].sort(runtimeOrder)) {
    const date = new Date(s.updated_at || "");
    const valid = Number.isFinite(date.getTime());
    const key = valid ? `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}` : "unknown";
    let group = groups.get(key);
    if (!group) { group = { key, ...(valid ? { date } : {}), sessions: [] }; groups.set(key, group); }
    group.sessions.push(s);
  }
  return [...groups.values()];
}
