// The page of one space (`/space/<machine>/<space name>`, docs/web/web.md "Space page", iOS SpaceView): what is computed from the
// store, kept apart so it can be tested. A space is found by its name on the machine, like the pins, mutes and the
// todos it hangs on: herdr gives a workspace a new id when it restarts, the name stays.
import { byAttention, activityOf } from "./format";
import { findTodoForSpace, type Todo } from "./todos";
import type { Session } from "./types";
import { byTime } from "./times";

/** What a space's name is, as the sidebar and the pins call it. */
export function spaceNameOf(s: Session): string {
  return s.space_name || s.space_id || "";
}

export interface SpaceView {
  /** Running agents, the most urgent first (needs you, unread, working, seen), the tab order among equals kept by byAttention's dates. */
  live: Session[];
  /** The space's agents that are no longer running (most recent first, at most 20). */
  recent: Session[];
  counts: { needs: number; working: number; unread: number };
  /** The space's directory and its workspace, from one of its agents (a live one first). */
  cwd?: string;
  first?: Session;
}

const RECENT_LIMIT = 20;

export function spaceView(sessions: readonly Session[], mid: string, space?: string): SpaceView {
  const mine = sessions.filter((s) => s.machine_id === mid && !s.assistant && (space === undefined || (s.space_id && spaceNameOf(s) === space)));
  const live = mine.filter((s) => activityOf(s) !== "offline").sort(byAttention);
  const recent = mine
    .filter((s) => activityOf(s) === "offline")
    .sort((a, b) => byTime(b.updated_at, a.updated_at) || a.session_id.localeCompare(b.session_id))
    .slice(0, RECENT_LIMIT);
  const counts = { needs: 0, working: 0, unread: 0 };
  for (const s of live) {
    const st = activityOf(s);
    if (st === "needs_you") counts.needs++;
    else if (st === "working") counts.working++;
    else if (st === "unread") counts.unread++;
  }
  const first = live[0] ?? recent[0];
  return { live, recent, counts, cwd: first?.cwd || undefined, first };
}

export interface SpaceTodo {
  id: string;
  title: string;
  /** "space": the space itself hangs on it ("Link to to-do"); "agent": one of its agents does. */
  via: "space" | "agent";
  todo?: Todo;
}

/** The todos the space is tied to: the one the space hangs on first, then the ones its running agents hang on, each once. */
export function spaceTodos(todos: readonly Todo[], live: readonly Session[], mid: string, space: string): SpaceTodo[] {
  const out: SpaceTodo[] = [];
  const linked = findTodoForSpace(todos as Todo[], mid, space);
  if (linked) out.push({ id: linked.id, title: linked.title, via: "space", todo: linked });
  for (const s of live) {
    if (!s.todo || out.some((x) => x.id === s.todo!.id)) continue;
    const todo = todos.find((x) => x.id === s.todo!.id);
    out.push({ id: s.todo.id, title: todo?.title || s.todo.title, via: "agent", todo });
  }
  return out;
}
