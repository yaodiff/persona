// By-project grouping is used by the lazy To-dos page.
import { t } from "../../i18n";
import { repoOf } from "../../lib/agent-repos-core";
import { compareTreeStates, latestTreeActivity, STALE_MS, todoTreeState, worstTreeState, type TreeState } from "../../lib/todo-tree";
import { todoStatus, type SessionLookup, type Todo } from "../../lib/todos";
import { timeMs } from "../../lib/times";
import type { Session } from "../../lib/types";

/** The project a set of working directories belongs to: the repository most of them sit in ("By repository"'s rule, merged across computers). */
export function projectOf(cwds: string[]): string {
  const count = new Map<string, number>();
  for (const cwd of cwds) {
    const name = repoOf(cwd).name;
    if (name) count.set(name, (count.get(name) ?? 0) + 1);
  }
  let best = "";
  let n = 0;
  for (const [name, c] of count) if (c > n || (c === n && name.localeCompare(best) < 0)) [best, n] = [name, c];
  return best;
}

/** A todo's project from its active agents (the live session's cwd, else the cwd the link recorded); "" for none. */
function projectOfTodo(todo: Todo, lookup?: SessionLookup): string {
  const cwds = (todo.agents ?? [])
    .filter((a) => !a.released_at)
    .map((a) => lookup?.(a.machine_id, a.session_id)?.cwd || a.cwd || "");
  return projectOf(cwds);
}

/** The todos page's list by project (#2526): open todos under the project their agents work in, the ones with no agent
 *  in one group last. Each group says "Needs you" / "Working" / "Done" and starts folded when every todo in it is done-state and quiet
 *  for a day. Todos keep the order they come in (sortTodos). */
export interface TodoProjectGroup {
  key: string;
  name: string;
  state: TreeState;
  todos: Todo[];
  folded: boolean;
  /** A knowledge-base project (an area of work, #2581): open to-dos the user keeps there, so it never folds for being
   *  quiet, and "Done" (no agent at work) is not said of it. */
  area?: boolean;
}

/** A todo's agents that are still on it, as the lookup has them. */
function liveAgents(td: Todo, lookup: SessionLookup): Session[] {
  return (td.agents ?? []).filter((a) => !a.released_at).map((a) => lookup(a.machine_id, a.session_id)).filter((s): s is Session => Boolean(s));
}

/** Open to-dos by project (#2526, #2581): the knowledge-base project a to-do was put in first (named by projectName),
 *  else the repository its agents work in; those with neither last ("Unsorted"). Groups that need the user come
 *  first within each of the three; the to-dos keep their order. */
export function groupTodosByProject(
  todos: Todo[],
  lookup: SessionLookup,
  now = Date.now(),
  projectName: (slug: string) => string = (slug) => slug,
  /** A to-do the To-dos page has in Needs you (its status section): its folder says Needs you whatever its agents do,
   *  so a failed start waiting on the owner (which the hub closed) does not read Done or fold away. */
  waiting: (td: Todo) => boolean = () => false,
): TodoProjectGroup[] {
  const map = new Map<string, { name: string; tier: number; list: Todo[] }>();
  for (const td of todos) {
    const repo = td.project ? "" : projectOfTodo(td, lookup);
    const [key, name, tier] = td.project
      ? [`todos-kb:${td.project}`, projectName(td.project), 0]
      : repo
        ? [`todos-project:${repo}`, repo, 1]
        : ["todos-project:\u0000", t("lib-todo-tree.unsorted"), 2];
    const g = map.get(key) ?? { name, tier, list: [] };
    g.list.push(td);
    map.set(key, g);
  }
  const groups = [...map.entries()].map(([key, { name, tier, list }]) => {
    const states = list.map((td): TreeState => (waiting(td) ? "needs_you" : todoTreeState(todoStatus(td, lookup), liveAgents(td, lookup))));
    const state = worstTreeState(states);
    const sessions = list.flatMap((td) => liveAgents(td, lookup));
    const touched = Math.max(latestTreeActivity(sessions), ...list.map((td) => timeMs(td.updated_at)));
    const area = tier === 0;
    return { group: { key, name, state, todos: list, folded: !area && state === "done" && now - touched > STALE_MS, ...(area && { area }) }, tier };
  });
  return groups.sort((a, b) => a.tier - b.tier || compareTreeStates(a.group.state, b.group.state)).map((g) => g.group);
}
