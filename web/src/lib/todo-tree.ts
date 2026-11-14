import { t } from "../i18n";
import type { AgentGroup, AgentMutes } from "./agent-groups";
import { repoOf } from "./agent-repos";
import { backgroundWaiting, stateOf } from "./format";
import { buildTodoAgentGroups, interruptedAgent, todoStatus, todoStatusLabel, type SessionLookup, type Todo, type TodoStatus } from "./todos";
import { usageLimitAgent } from "./usage-limit";
import type { Machine, Session } from "./types";
import { stateName } from "./state-labels";
import { sameValue } from "./stable-lines";
import type { DirectionCounts, DirectionTaskState } from "./direction-tree";

/** The one-word state every node of the tree shows (#2526): "Needs you" before "Working" before "Waiting in the
 *  background" (#6045) before "Done". */
export type TreeState = "needs_you" | "working" | "bg_waiting" | "waiting" | "done";

const TREE_RANK: Record<TreeState, number> = { needs_you: 0, working: 1, bg_waiting: 2, waiting: 3, done: 4 };

export function treeStateLabel(s: TreeState): string {
  return stateName(s);
}

/** A branch with nothing going on whose newest activity is older than this folds by default. */
export const STALE_MS = 24 * 60 * 60 * 1000;

export interface TreeAgent {
  key: string;
  session: Session;
  state: TreeState;
  /** The agents it spawned for the same todo (todo_agents.source "spawned", via this one). */
  children: TreeAgent[];
  folded: boolean;
}

export interface TreeTodo {
  key: string;
  todoId?: string;
  name: string;
  status?: TodoStatus;
  /** A to-do's state in the To-dos page's word (todoStatusLabel), said in place of `state`'s, so the two pages agree
   *  (#6458: a to-do waiting on CI read "Done" here and "Waiting" there). `state` still ranks, folds and colors it. */
  statusLabel?: string;
  state: TreeState;
  /** A task of the direction tree (#4352): its state as the hub decided it, shown in place of `state`'s word. */
  taskState?: DirectionTaskState;
  agents: TreeAgent[];
  sessions: Session[];
  folded: boolean;
}

export interface TreeProject {
  key: string;
  name: string;
  state: TreeState;
  todos: TreeTodo[];
  sessions: Session[];
  folded: boolean;
  /** A pinned direction of the direction tree (#4352) in place of a project: what it has going on the title line. */
  direction?: { counts: DirectionCounts; running: boolean; secretary: boolean };
}

/** The group of agents on no to-do, "Not on a to-do": its agents directly under it, no repository rows (#6458). */
export interface LooseGroup {
  key: string;
  name: string;
  /** What its agents are and how to file one, behind the row's ⓘ; the group's empty state when it is open and empty. */
  hint: string;
  /** The most urgent of its agents' states; "Done" for none, which the row does not say. */
  state: TreeState;
  /** What the row says in place of `state`'s word, when "Done" would hide what its rows say (agentGroupLabel). */
  label?: string;
  agents: TreeAgent[];
  sessions: Session[];
  folded: boolean;
}

export interface TodoTree {
  /** "By pinned"'s directions (#4352), each over its tasks; "By to-do" has none (#6458: no repository layer). */
  projects: TreeProject[];
  /** "By to-do"'s open to-dos with agents, at the top level, in sortTodos's order. */
  todos: TreeTodo[];
  /** Agents on no to-do, after them: "Not on a to-do", the ones opened by hand in a terminal too (#6591; a group of their
   *  own before, #2623). It starts folded unless one of its agents needs the user, or no to-do is above it (it is the whole
   *  list). */
  loose: LooseGroup[];
}

export function agentTreeState(s: Session): TreeState {
  const st = stateOf(s);
  if (usageLimitAgent(s)) return "waiting"; // a limit resetting is a wait, not work (#1758)
  if (st === "needs_you" || interruptedAgent(s)) return "needs_you";
  if (s.starting) return "working";
  if (backgroundWaiting(s)) return "bg_waiting";
  if (st === "working") return "working";
  return "done";
}

/** A todo's state in the tree from its status; a working one whose agents only wait in the background (none works, one
 *  waits) is "Waiting in the background" (#6045), and so is one the To-dos page calls Waiting (its agents ended their
 *  turns and something brings one back, #3274): under way, never "Done" (#6458). */
export function todoTreeState(status: TodoStatus, agents: readonly Session[] = []): TreeState {
  if (status === "needs_you" || status === "interrupted") return "needs_you";
  if (status === "usage_limit") return "waiting";
  if (status === "waiting") return "bg_waiting";
  if (status === "working") {
    const states = agents.map(agentTreeState);
    return states.includes("bg_waiting") && !states.includes("working") ? "bg_waiting" : "working";
  }
  return "done";
}

/** The most urgent of some states ("Done" for none). */
export function worstTreeState(states: TreeState[]): TreeState {
  return worst(states);
}

/** What a group of agents says beside its count when its most urgent state is "Done" (Not on a to-do, Recent (not
 *  running)): the word its rows and the other groupings use, never "Done" over agents that are not. A finished agent
 *  not looked at yet is Unread, as By status and the quick filter call it, and a group whose agents all stopped is Not
 *  running; undefined keeps "Done" (agents seen, or a mix of seen and stopped). */
export function agentGroupLabel(sessions: readonly Session[], state: TreeState): string | undefined {
  if (state !== "done" || !sessions.length) return undefined;
  const states = sessions.map(stateOf);
  if (states.includes("unread")) return stateName("unread");
  if (states.every((st) => st === "offline")) return stateName("offline");
  return undefined;
}

function worst(states: TreeState[]): TreeState {
  return states.reduce<TreeState>((a, b) => (TREE_RANK[b] < TREE_RANK[a] ? b : a), "done");
}

function latest(sessions: Session[]): number {
  let at = 0;
  for (const s of sessions) at = Math.max(at, Date.parse(s.updated_at ?? "") || 0, Date.parse(s.state_since ?? "") || 0);
  return at;
}

/** Done, or nothing working or waiting for you and quiet for a day: such a branch starts folded. */
function stale(state: TreeState, sessions: Session[], now: number): boolean {
  return state === "done" && now - latest(sessions) > STALE_MS;
}

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
export function projectOfTodo(todo: Todo, lookup?: SessionLookup): string {
  const cwds = (todo.agents ?? [])
    .filter((a) => !a.released_at)
    .map((a) => lookup?.(a.machine_id, a.session_id)?.cwd || a.cwd || "");
  return projectOf(cwds);
}

const keyOf = (s: Session) => `${s.machine_id}/${s.session_id}`;

/** A todo's agents with each spawned one under the agent that spawned it (when that agent is in the list too). */
export function nestAgents(sessions: Session[], todo: Todo | undefined, now: number): TreeAgent[] {
  const parentOf = new Map<string, string>();
  for (const a of todo?.agents ?? []) if (!a.released_at && a.source === "spawned" && a.via) parentOf.set(`${a.machine_id}/${a.session_id}`, a.via);
  const listed = new Set(sessions.map(keyOf));
  const nodes = new Map<string, TreeAgent>();
  for (const s of sessions) nodes.set(keyOf(s), { key: `s:${keyOf(s)}`, session: s, state: agentTreeState(s), children: [], folded: false });
  const roots: TreeAgent[] = [];
  for (const s of sessions) {
    const key = keyOf(s);
    let p = parentOf.get(key);
    // a parent outside the list, or a loop (a → b → a), leaves it at the top
    if (p && (!listed.has(p) || cycles(key, parentOf))) p = undefined;
    if (p) nodes.get(p)!.children.push(nodes.get(key)!);
    else roots.push(nodes.get(key)!);
  }
  const settle = (n: TreeAgent): Session[] => {
    const all = [n.session, ...n.children.flatMap(settle)];
    n.state = worst([n.state, ...n.children.map((c) => c.state)]);
    n.folded = n.children.length > 0 && stale(n.state, all.slice(1), now);
    return all;
  };
  roots.forEach(settle);
  return roots;
}

function cycles(start: string, parentOf: Map<string, string>): boolean {
  const seen = new Set<string>();
  for (let q: string | undefined = start; q; q = parentOf.get(q)) {
    if (seen.has(q)) return true;
    seen.add(q);
  }
  return false;
}

/** The group of agents on no to-do, "Not on a to-do": opened by hand in a terminal or not, each row's source mark says
 *  where it runs (#6591). */
const LOOSE_GROUPS = [
  { key: "todo:none", name: () => t("lib-todos.unassigned"), hint: () => t("lib-todos.unassigned_hint") },
] as const;

/**
 * Agents "By to-do" (#2526, #6458): the open to-dos at the top level, each over its agents (a spawned agent under the one
 * that spawned it), then the group of agents on no to-do, its agents directly under it. Grouping by repository or
 * folder belongs to "By repository" and "By project" only. A to-do says its state in the To-dos page's word; a group the
 * most urgent of its agents' ("Needs you" / "Working" / "Waiting in the background" / "Done"). A done or day-quiet to-do
 * starts folded; the group starts folded unless one of its agents needs the user or no to-do is above it. The to-dos and
 * their order (sortTodos's, the task board's) come from buildTodoAgentGroups. `keepEmpty`: the group is listed while
 * empty (the sidebar's "By to-do" with nothing narrowing the list), its explanation inside it.
 */
export function buildTodoTree(all: Session[], machines: Machine[], todos: Todo[], mutes?: AgentMutes, now = Date.now(), { keepEmpty = false }: { keepEmpty?: boolean } = {}): TodoTree {
  const groups = buildTodoAgentGroups(all, machines, todos, mutes);
  const byId = new Map(todos.map((td) => [td.id, td]));
  const tree: TreeTodo[] = [];
  const loose = new Map<string, AgentGroup>();
  for (const g of groups) {
    if (!g.todoId) {
      loose.set(g.key, g);
      continue;
    }
    const todo = byId.get(g.todoId);
    const status = g.todoStatus as TodoStatus | undefined;
    const state = status ? todoTreeState(status, g.sessions) : worst(g.sessions.map(agentTreeState));
    tree.push({
      key: `todo-tree:todo:${g.todoId}`,
      todoId: g.todoId,
      name: g.name,
      status,
      ...(todo && status && { statusLabel: todoStatusLabel(todo, status) }),
      state,
      agents: nestAgents(g.sessions, todo, now),
      sessions: g.sessions,
      folded: status === "done" || stale(state, g.sessions, now),
    });
  }
  const rest = LOOSE_GROUPS.flatMap((def): AgentGroup[] => {
    const g = loose.get(def.key);
    if (g) return [g];
    return keepEmpty ? [{ key: def.key, name: def.name(), hint: def.hint(), sessions: [] }] : [];
  });
  return { projects: [], todos: tree, loose: rest.map((g) => looseTree(g, tree.length > 0, now)) };
}

/** `next`, with every node (project, to-do, agent) and list that comes out the same as in `prev` (sameValue) replaced
 *  by `prev`'s object, matched by key; `prev` itself when nothing changed. The tree is built afresh on every push, and
 *  its rows are memoized: shared nodes keep the rows under them from rendering again (a push of one agent, or of the
 *  machines' load, re-rendered the whole tree). Sessions are leaves: kept when equal, never merged. */
export function shareTree(prev: TodoTree | undefined, next: TodoTree): TodoTree {
  return prev ? shareNode(prev, next) : next;
}

/** The fields that hold lists of tree nodes (objects with a `key`): shared node by node; any other field is a leaf. */
const NODE_LISTS = new Set(["projects", "loose", "todos", "agents", "children"]);

function shareNode<T extends object>(prev: T, next: T): T {
  const before = prev as Record<string, unknown>;
  const out: Record<string, unknown> = {};
  let same = Object.keys(before).length === Object.keys(next).length;
  for (const [k, v] of Object.entries(next)) {
    const old = before[k];
    const had = Object.prototype.hasOwnProperty.call(before, k);
    const kept = !had
      ? v
      : NODE_LISTS.has(k) && Array.isArray(old) && Array.isArray(v)
        ? shareNodes(old as { key: string }[], v as { key: string }[])
        : sameValue(old, v) ? old : v;
    if (!had || kept !== old) same = false;
    out[k] = kept;
  }
  return same ? prev : (out as T);
}

function shareNodes<T extends { key: string }>(prev: T[], next: T[]): T[] {
  const byKey = new Map(prev.map((n) => [n.key, n]));
  let same = prev.length === next.length;
  const out = next.map((n, i) => {
    const old = byKey.get(n.key);
    const kept = old ? shareNode(old, n) : n;
    if (kept !== prev[i]) same = false;
    return kept;
  });
  return same ? prev : out;
}

/** A group of agents on no to-do, its agents in buildTodoAgentGroups's order (pinned and needs you first, then by time).
 *  Folded below the to-dos unless it says "Needs you" (an interrupted agent counts too, DESIGN_SYSTEM 3.4); with no
 *  to-do above it, it is the whole list, so open. */
function looseTree(g: AgentGroup, below: boolean, now: number): LooseGroup {
  const agents = nestAgents(g.sessions, undefined, now);
  const state = worst(agents.map((a) => a.state));
  const label = agentGroupLabel(g.sessions, state);
  return { key: g.key, name: g.name, hint: g.hint ?? "", state, ...(label && { label }), agents, sessions: g.sessions, folded: below && state !== "needs_you" };
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
    const state = worst(states);
    const sessions = list.flatMap((td) => (td.agents ?? []).filter((a) => !a.released_at).map((a) => lookup(a.machine_id, a.session_id)).filter((s): s is Session => Boolean(s)));
    const touched = Math.max(latest(sessions), ...list.map((td) => Date.parse(td.updated_at) || 0));
    const area = tier === 0;
    return { group: { key, name, state, todos: list, folded: !area && state === "done" && now - touched > STALE_MS, ...(area && { area }) }, tier };
  });
  return groups.sort((a, b) => a.tier - b.tier || TREE_RANK[a.group.state] - TREE_RANK[b.group.state]).map((g) => g.group);
}
