// Lazy By-to-do tree for todos_v2 (#15800), with goal folding for hubs without a board projection.

import { t } from "../../i18n";
import { splitAgentSessions, type AgentMutes } from "../../lib/agent-groups";
import { activityOf, stateOf } from "../../lib/format";
import {
  agentGroupLabel,
  agentTreeState,
  buildTodoTree,
  latestTreeActivity,
  nestAgents,
  STALE_MS,
  staleTodoTreeBranch,
  worstTreeState,
  type LooseGroup,
  type TodoTree,
  type TreeState,
  type TreeTodo,
} from "../../lib/todo-tree";
import { todoLook, waitOf } from "../../lib/todo-wait-reason";
import {
  makeSessionLookup,
  openAsk,
  todoStatus,
  type Todo,
  type TodoBoardProjection,
  type TodoBoardSectionKey,
  type TodoStatus,
} from "../../lib/todos";
import type { Machine, Session } from "../../lib/types";
import { repoGatePRLook } from "./repo-gate";

function defaultSessionCompare(mutes: AgentMutes) {
  const rank = (s: Session): number => {
    const st = activityOf(s);
    const waiting =
      (st === "idle" || st === "unknown") &&
      Boolean(s.loop || s.background?.length || s.subagents?.some((a) => a.running));
    const isMuted =
      mutes.sessions.includes(`${s.machine_id}/${s.session_id}`) ||
      Boolean(s.space_name && mutes.spaces.includes(`${s.machine_id}/${s.space_name}`));
    if (st === "needs_you") return 0;
    if (isMuted && (st === "working" || st === "unread" || waiting)) return 4;
    if (st === "unread") return 1;
    if (st === "working") return 2;
    if (waiting) return 3;
    return 5;
  };
  const ts = (s: Session): number => {
    const raw = (stateOf(s) === "working" ? s.state_since || s.updated_at : s.updated_at) ?? "";
    return raw ? Date.parse(raw) || 0 : 0;
  };
  return (a: Session, b: Session): number => {
    const rA = rank(a);
    const rB = rank(b);
    if (rA !== rB) return rA - rB;
    const diff = ts(b) - ts(a);
    if (diff !== 0) return diff;
    return a.session_id.localeCompare(b.session_id) || a.machine_id.localeCompare(b.machine_id);
  };
}

function sidebarNodeState(
  section: TodoBoardSectionKey,
  needsYouCount: number,
  sessions: readonly Session[],
): TreeState {
  if (section === "needs_you" || needsYouCount > 0) return "needs_you";
  if (section === "can_wait") return "bg_waiting";
  if (section === "in_progress") {
    const states = sessions.map(agentTreeState);
    if (states.includes("needs_you")) return "needs_you";
    return states.includes("bg_waiting") && !states.includes("working") ? "bg_waiting" : "working";
  }
  if (section === "waiting") {
    const states = sessions.map(agentTreeState);
    if (states.includes("needs_you")) return "needs_you";
    return "bg_waiting";
  }
  return worstTreeState(sessions.map(agentTreeState));
}

/**
 * Thinly adapts server `board.sidebar_tree` for `Sidebar.tsx` By-to-do view under `todos_v2 === true`.
 * Preserves server top-level ordering, folded child aggregation (`child_ids`, `folded_count`), and
 * surfaced blockers (`surfaced_child_ids`) without re-running client sorting or goal-folding derivation.
 */
export function consumeBoardSidebarTree(
  all: Session[],
  machines: Machine[],
  todos: readonly Todo[],
  board: TodoBoardProjection,
  mutes: AgentMutes = { spaces: [], sessions: [] },
  now = Date.now(),
  { keepEmpty = false, compareSessions }: { keepEmpty?: boolean; compareSessions?: (a: Session, b: Session) => number } = {},
): TodoTree {
  const { live } = splitAgentSessions(all, machines);
  const lookup = makeSessionLookup(live);
  const byId = new Map<string, Todo>();
  for (const td of todos) byId.set(td.id, td);

  const liveByKey = new Map<string, Session>();
  for (const s of live) liveByKey.set(`${s.machine_id}/${s.session_id}`, s);

  // Map each live session to its home to-do ID.
  const homeOf = new Map<string, string>();
  for (const [key, s] of liveByKey) {
    const own = s.todo?.id;
    if (own && byId.has(own)) {
      homeOf.set(key, own);
      continue;
    }
    const owner = todos.find(
      (td) => !td.done_at && (td.agents ?? []).some((a) => !a.released_at && `${a.machine_id}/${a.session_id}` === key),
    );
    if (owner) homeOf.set(key, owner.id);
  }

  // Group live sessions by their owning to-do ID.
  const sessionsByTodo = new Map<string, Session[]>();
  for (const [key, todoId] of homeOf) {
    const s = liveByKey.get(key);
    if (!s) continue;
    const list = sessionsByTodo.get(todoId);
    if (list) list.push(s);
    else sessionsByTodo.set(todoId, [s]);
  }

  const byRank = compareSessions ?? defaultSessionCompare(mutes);
  const assignedSessionKeys = new Set<string>();
  const treeTodos: TreeTodo[] = [];

  for (const node of board.sidebar_tree) {
    const todo = byId.get(node.todo_id);
    if (!todo) continue;

    // Aggregate sessions from this node and its folded children (surfaced children are their own top-level nodes).
    const surfacedSet = new Set(node.surfaced_child_ids ?? []);
    const memberIds = [node.todo_id, ...(node.child_ids ?? []).filter((cid) => !surfacedSet.has(cid))];
    const seenKeys = new Set<string>();
    const nodeSessions: Session[] = [];

    for (const id of memberIds) {
      for (const s of sessionsByTodo.get(id) ?? []) {
        const key = `${s.machine_id}/${s.session_id}`;
        if (seenKeys.has(key) || assignedSessionKeys.has(key)) continue;
        seenKeys.add(key);
        nodeSessions.push(s);
      }
    }

    if (nodeSessions.length === 0) continue;
    for (const key of seenKeys) assignedSessionKeys.add(key);
    nodeSessions.sort(byRank);

    const status: TodoStatus = todoStatus(todo, lookup);
    const wait = waitOf(todo, status, lookup);
    const state = sidebarNodeState(node.section, node.needs_you_count, nodeSessions);
    const statusLabel = todoLook(todo, status, wait, undefined, now, repoGatePRLook).label;

    const allChildIds = [...(node.child_ids ?? []), ...(node.surfaced_child_ids ?? [])];
    const totalChildren = Math.max(allChildIds.length, node.folded_count + (node.surfaced_child_ids?.length ?? 0));
    let goalProgress: { done: number; total: number } | undefined;
    if (totalChildren > 0) {
      const doneChildren = allChildIds.reduce((n, cid) => {
        const child = byId.get(cid);
        return n + (child?.done_at || child?.view?.section === "done" ? 1 : 0);
      }, 0);
      goalProgress = { done: doneChildren, total: totalChildren };
    }

    const folded =
      node.section !== "needs_you" &&
      node.needs_you_count === 0 &&
      (status === "done" || (state === "done" && now - latestTreeActivity(nodeSessions) > STALE_MS));

    treeTodos.push({
      key: `todo-tree:todo:${todo.id}`,
      todoId: todo.id,
      name: todo.title,
      status,
      statusLabel,
      ...(wait ? { wait } : {}),
      state,
      ...(goalProgress ? { goalProgress } : {}),
      agents: nestAgents(nodeSessions, todo, now),
      sessions: nodeSessions,
      folded,
    });
  }

  const unassigned = live.filter((s) => !assignedSessionKeys.has(`${s.machine_id}/${s.session_id}`));
  unassigned.sort(byRank);

  const loose: LooseGroup[] = [];
  if (unassigned.length > 0 || keepEmpty) {
    const agents = nestAgents(unassigned, undefined, now);
    const state = worstTreeState(agents.map((a) => a.state));
    const label = agentGroupLabel(unassigned, state);
    loose.push({
      key: "todo:none",
      name: t("lib-todos.unassigned"),
      hint: t("lib-todos.unassigned_hint"),
      state,
      ...(label ? { label } : {}),
      agents,
      sessions: unassigned,
      folded: treeTodos.length > 0 && state !== "needs_you",
    });
  }

  return { projects: [], todos: treeTodos, loose };
}

/** Resolves the highest ancestor goal id present in `byId` via `part_of`.
 *  Returns undefined for top-level items, orphan children whose parent is missing, self-references, or cycles. */
function resolveRootGoalId(id: string, byId: ReadonlyMap<string, Todo>): string | undefined {
  const start = byId.get(id);
  if (!start?.part_of || start.part_of === id || !byId.has(start.part_of)) return undefined;
  const visited = new Set<string>([id]);
  let current = start.part_of;
  while (current) {
    if (visited.has(current)) return undefined;
    visited.add(current);
    const node = byId.get(current);
    if (!node) return undefined;
    const next = node.part_of;
    if (!next || next === current || !byId.has(next)) return current;
    current = next;
  }
  return undefined;
}

/** Whether a `part_of` child branch is urgent (`needs_you` or active owner ask) and must stay surfaced at the top level. */
function isUrgentTreeChild(node: TreeTodo, todo: Todo | undefined): boolean {
  if (node.state === "needs_you" || node.wait === "owner") return true;
  if (todo?.view?.section === "needs_you" || todo?.view?.phase === "needs_you") return true;
  if (todo && !todo.done_at && Boolean(openAsk(todo))) return true;
  return false;
}

/**
 * Under `todos_v2` (#14831): folds non-urgent `part_of` child branches under their top-level open parent goal
 * with compact `done/total` progress, while surfacing `needs_you` / active-ask child blockers at the top.
 */
export function foldTreeGoals(tree: TodoTree, todos: readonly Todo[], now: number): TodoTree {
  const byId = new Map(todos.map((td) => [td.id, td]));
  const progressByParent = new Map<string, { done: number; total: number }>();
  for (const td of todos) {
    const rootId = resolveRootGoalId(td.id, byId);
    if (!rootId) continue;
    const root = byId.get(rootId);
    if (!root || root.done_at) continue;
    const p = progressByParent.get(rootId) ?? { done: 0, total: 0 };
    p.total += 1;
    if (td.done_at || td.view?.section === "done") p.done += 1;
    progressByParent.set(rootId, p);
  }
  if (progressByParent.size === 0) return tree;

  const existingParentIds = new Set(tree.todos.map((n) => n.todoId).filter((id): id is string => Boolean(id)));
  const foldedByParent = new Map<string, TreeTodo[]>();
  const surfaced: TreeTodo[] = [];
  const slots: Array<{ kind: "node"; node: TreeTodo } | { kind: "synth"; parentId: string }> = [];
  const synthAdded = new Set<string>();

  for (const node of tree.todos) {
    const id = node.todoId;
    const rootId = id ? resolveRootGoalId(id, byId) : undefined;
    const root = rootId ? byId.get(rootId) : undefined;
    if (!rootId || !root || root.done_at) {
      slots.push({ kind: "node", node });
      continue;
    }
    if (isUrgentTreeChild(node, byId.get(id!))) {
      surfaced.push(node.folded ? { ...node, folded: false } : node);
      continue;
    }
    const list = foldedByParent.get(rootId);
    if (list) list.push(node);
    else foldedByParent.set(rootId, [node]);
    if (!existingParentIds.has(rootId) && !synthAdded.has(rootId)) {
      synthAdded.add(rootId);
      slots.push({ kind: "synth", parentId: rootId });
    }
  }

  const out: TreeTodo[] = [...surfaced];
  for (const slot of slots) {
    if (slot.kind === "synth") {
      const parent = byId.get(slot.parentId)!;
      const children = foldedByParent.get(slot.parentId)!;
      const sessions = children.flatMap((c) => c.sessions);
      const agents = children.flatMap((c) => c.agents);
      const state = worstTreeState(children.map((c) => c.state));
      const progress = progressByParent.get(slot.parentId);
      out.push({
        key: `todo-tree:todo:${parent.id}`,
        todoId: parent.id,
        name: parent.title,
        state,
        ...(progress && { goalProgress: progress }),
        agents,
        sessions,
        folded: staleTodoTreeBranch(state, sessions, now),
      });
      continue;
    }
    const node = slot.node;
    const children = node.todoId ? foldedByParent.get(node.todoId) : undefined;
    if (!children?.length) {
      out.push(node);
      continue;
    }
    const sessions = [...node.sessions, ...children.flatMap((c) => c.sessions)];
    const agents = [...node.agents, ...children.flatMap((c) => c.agents)];
    const state = worstTreeState([node.state, ...children.map((c) => c.state)]);
    const progress = progressByParent.get(node.todoId!);
    const keepStatusLabel = state === node.state;
    out.push({
      key: node.key,
      todoId: node.todoId,
      name: node.name,
      status: node.status,
      ...(keepStatusLabel && node.statusLabel ? { statusLabel: node.statusLabel } : {}),
      ...(keepStatusLabel && node.wait ? { wait: node.wait } : {}),
      state,
      ...(node.taskState ? { taskState: node.taskState } : {}),
      ...(progress ? { goalProgress: progress } : {}),
      agents,
      sessions,
      folded: (node.status === "done" && children.every((c) => c.status === "done")) || staleTodoTreeBranch(state, sessions, now),
    });
  }
  return { ...tree, todos: out };
}

export function buildV2SidebarTree(
  all: Session[],
  machines: Machine[],
  todos: Todo[],
  mutes: AgentMutes,
  now: number,
  options: {
    board: TodoBoardProjection | null;
    foldChildren: boolean;
    olderClosed?: readonly Todo[];
    keepEmpty?: boolean;
    compareSessions?: (a: Session, b: Session) => number;
  },
): TodoTree {
  const { board, foldChildren, olderClosed, ...treeOptions } = options;
  const historyIds = new Set(olderClosed?.map((td) => td.id));
  const window = historyIds.size ? todos.filter((td) => !historyIds.has(td.id)) : todos;
  if (board) return consumeBoardSidebarTree(all, machines, window, board, mutes, now, treeOptions);
  const tree = buildTodoTree(all, machines, window, mutes, now, { ...treeOptions, repoGateLook: repoGatePRLook });
  return foldChildren ? foldTreeGoals(tree, window, now) : tree;
}
