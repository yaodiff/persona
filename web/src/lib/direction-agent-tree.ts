// The direction tree (#4352, lib/direction-tree.ts) as the sidebar's 常驻 rows unfold it: task › agent › sub-agent in
// lib/todo-tree.ts's nodes, with live session rows. (The Agents list's "By pinned" grouping drew it too, until #6662.)
import { agentRef, type DirectionAgent, type DirectionTask, type DirectionTaskState } from "./direction-tree";
import { agentTreeState, type TreeAgent, type TreeState, type TreeTodo } from "./todo-tree";
import type { AgentState, Session } from "./types";
import { splitSubagentLink } from "./subagent-link";

/** The Agents tree's one-word state for a task's state: needs you, working, and waiting for stuck and review. */
const TREE_STATE: Record<DirectionTaskState, TreeState> = { needs_you: "needs_you", working: "working", stuck: "waiting", review: "waiting", planned: "waiting" };

/** What a session row needs of an agent the session list does not have (another assistant's, an in-session sub-agent):
 *  its title and the state the hub saw. */
function standIn(a: DirectionAgent): Session {
  const known: readonly string[] = ["needs_you", "working", "unread", "idle", "offline"];
  return { machine_id: a.machine_id, session_id: a.session_id, title: a.title, cwd: "", status: "", state: (known.includes(a.state) ? a.state : "offline") as AgentState, ...(a.agent ? { agent: a.agent } : {}) };
}

/**
 * A task's agents as tree nodes: each found in `sessions` (by "machine/session") once, nested as the hub nested them.
 * `standIns`: an agent the list does not have is drawn from what the tree says of it (the sidebar); otherwise it is
 * left out and its children move up (the Agents page, whose list is filtered). `placed` collects the agents drawn.
 * An in-session subagent ("<parent>@<agent>") is never a row, nor are the ones it launched (#5030, cindy): it is a card
 * in its parent's chat.
 */
export function directionAgentNodes(list: readonly DirectionAgent[] | undefined, sessions: ReadonlyMap<string, Session>, placed: Set<string>, standIns: boolean): TreeAgent[] {
  const out: TreeAgent[] = [];
  for (const a of list ?? []) {
    if (splitSubagentLink(a.session_id)) continue;
    const ref = agentRef(a);
    const children = directionAgentNodes(a.children, sessions, placed, standIns);
    const s = placed.has(ref) ? undefined : sessions.get(ref) ?? (standIns ? standIn(a) : undefined);
    if (!s) {
      out.push(...children);
      continue;
    }
    placed.add(ref);
    out.push({ key: `s:${ref}`, session: s, state: agentTreeState(s), children, folded: false });
  }
  return out;
}

/** A task as the Agents tree's to-do node, under `keyPrefix` (its fold is kept per key). */
export function directionTaskNode(td: DirectionTask, sessions: ReadonlyMap<string, Session>, placed: Set<string>, { keyPrefix, folded, standIns }: { keyPrefix: string; folded: boolean; standIns: boolean }): TreeTodo {
  const agents = directionAgentNodes(td.agents, sessions, placed, standIns);
  const flat = (n: TreeAgent): Session[] => [n.session, ...n.children.flatMap(flat)];
  return { key: `${keyPrefix}${td.id}`, todoId: td.id, name: td.title, state: TREE_STATE[td.state], taskState: td.state, agents, sessions: agents.flatMap(flat), folded };
}
