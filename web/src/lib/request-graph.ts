// The requests graph of the To-dos page's "By request" grouping (views/TodoRequestGraph.tsx), pure parts: every request
// the owner made as a small DAG (the owner's words → the pieces of work, left to right by what each piece waits for),
// who is on each piece, and the pieces still queued. Nothing here reads the hub or the clock.
//
// A request in pieces is two fields of a to-do, the hub's (docs/assistant/todos.md "Requests in pieces"): `part_of`, the
// to-do whose request this one is a piece of (the request's root), and `after`, the pieces it waits for. The hub checks
// them when they are written (the to-dos exist, one level, no cycle); layout leaves out edges to missing to-dos.
// `prerequisite_blockers`, when sent, gives the actual blockers even if a to-do is missing from the graph.
// A queued piece is started on its own to-do (task_plan's todo_id), so its id never changes.
//
// Requests come from (1) a to-do others name as their parent; (2) the to-dos of one task plan (plan_id), the root being
// the plan's words; an open plan not started yet shows its tasks as pieces waiting for Start; (3) a to-do with neither,
// a request of one piece. A to-do belongs to exactly one request: `part_of` wins over its plan. A worker a
// leader's Agent handed out is drawn on the leader's piece, among its Agents, not as a request of its own.
//
// A request's heading is the sentence its work was filed under (the request to-do's title, the one task's title), with
// the owner's own words quoted under it when they say something else ("Go ahead" is the owner's words, not a heading):
// the brief keeps them, else a request to-do's notes do, on a line like `用户原话（…）：…` / `Your words: …`.
import { foldedState } from "./child-sessions";
import type { DirectionAgent, DirectionTask, DirectionTree } from "./direction-tree";
import type { PlanTask, TaskPlan } from "./feed";
import { reviewState } from "./supervision";
import { acceptedAtRest, interruption, isAutoDone, isCancelled, openAsk, taskNow, todoStatus, type SessionLookup, type Todo, type TodoStatus } from "./todos";
import type { AgentState, Session, Subagent } from "./types";

// ── what a piece is and where it stands ─────────────────────

/**
 *   queued    it waits for pieces that are not done yet (`waitsFor`), or, in an open plan, for Start (`waitsStart`);
 *   ready     nothing it waits for is pending and nobody took it yet;
 *   working   an Agent on it works or waits to be brought back;
 *   needs_you it waits on the owner: a decision, an Agent that asks, a supervisor that passed it up, an interrupted one;
 *   review    it says it is done and waits to be accepted;
 *   stopped   its Agents ended their turn and nothing brings them back;
 *   done      closed;
 *   cancelled closed by cancellation, with its own outcome rather than Done;
 *   failed    its Agent did not start (start_error).
 */
export type NodeState = "queued" | "ready" | "working" | "needs_you" | "review" | "stopped" | "done" | "cancelled" | "failed";

/** Where a state sorts: what waits on the owner first, then the work going on, then what waits, the closed last. */
const STATE_RANK: Record<NodeState, number> = { needs_you: 0, failed: 1, review: 2, working: 3, ready: 4, queued: 5, stopped: 6, done: 7, cancelled: 8 };

export type AgentRole = "lead" | "worker" | "spawned" | "subagent" | "before";

/** One Agent on a piece: the one it was given to, the workers a leader handed out, what they spawned, the earlier ones. */
export interface NodeAgent {
  key: string;
  mid: string;
  agent?: string;
  session?: Session;
  subagent?: Subagent;
  title: string;
  state: AgentState | "";
  role: AgentRole;
  /** How far down the piece's Agent tree: 0 for the piece's own Agents (and the earlier ones), 1 for what they handed
   *  out, spawned or launched, and so on. */
  depth: number;
  /** The row it hangs under (a key of this list); none at depth 0. */
  parent?: string;
  /** It finished while the piece goes on: a worker whose to-do closed, a subagent that returned, an earlier Agent. */
  ended: boolean;
  /** What it does now, or the last it said (one line, "" when nothing is known). */
  doing: string;
  /** The session that opens for it: its own; for an in-session subagent, its link "<launching session>@<id>", which opens
   *  the launching chat at the subagent's card (lib/subagent-link.ts). */
  open: { mid: string; sid: string };
}

/** A line of a piece's Agent tree: an Agent, or the ended subagents folded under one parent ("3 more ended"). */
export type TreeRow =
  | { kind: "agent"; key: string; depth: number; agent: NodeAgent }
  | { kind: "more"; key: string; depth: number; count: number; parent?: string };

interface GraphWait {
  key: string;
  title: string;
}

export interface GraphNode {
  key: string;
  title: string;
  todo?: Todo;
  /** A task of an open plan, not started yet. */
  task?: PlanTask;
  /** 1 + the deepest layer among what it waits for; 1 for a piece that waits for nothing. */
  layer: number;
  /** Its place in its layer, from the top. */
  row: number;
  /** The pieces it waits for (node keys), after the edges that would close a cycle were dropped. */
  after: string[];
  /** Actual blockers, with their title if known, otherwise their to-do ID. */
  waitsFor: GraphWait[];
  /** An open plan's task: it waits for the owner's Start. */
  waitsStart: boolean;
  state: NodeState;
  status?: TodoStatus;
  agents: NodeAgent[];
  /** Its Agents as a tree under the card, when more than one is on it (the earlier ones aside); [] keeps the card as it
   *  was, its one Agent on its bottom line. */
  tree: TreeRow[];
  /** What it asks the owner (needs_you), or why it did not start (failed). */
  ask: string;
  /** Since when it is in its state (ms), when known. */
  since?: number;
}

export type RequestKind = "parent" | "plan" | "single";

export interface GraphEdge {
  /** "root" or a node key. */
  from: string;
  to: string;
  /** Into a piece that is still queued. */
  dashed: boolean;
}

export interface GraphRequest {
  key: string;
  kind: RequestKind;
  /** Its heading: the sentence the work was filed under; the owner's words (`own`) only when nothing else names it. */
  title: string;
  own: boolean;
  /** The owner's own words when they say something else than the heading, quoted under it; "" when not. */
  quote: string;
  /** When it was said (ISO), when known. */
  at?: string;
  /** The parent to-do (kind parent), the plan (kind plan). */
  root?: Todo;
  plan?: TaskPlan;
  nodes: GraphNode[];
  edges: GraphEdge[];
  layers: number;
  rows: number;
  done: boolean;
  /** The latest change on it (ms): the most active request first. */
  touched: number;
  /** When its last piece closed (ms), for a done request. */
  finished: number;
  /** One piece whose title is the request's heading, closed exactly when the request's own to-do is (mergesWithRoot):
   * drawn as that piece's card alone (with when it was said). */
  merged: boolean;
}

export interface Graph {
  /** Not done or wholly cancelled, the most active first. */
  active: GraphRequest[];
  /** Done, the latest finished first. */
  done: GraphRequest[];
  /** Terminal requests containing a cancellation, kept apart from both active work and Done. */
  cancelled: GraphRequest[];
}

export interface GraphInput {
  todos: readonly Todo[];
  /** The task plans the feed has (lib/feed.ts), by id. */
  plans: Record<string, TaskPlan>;
  data: GraphData;
}

/** What the pieces' Agents are read from: the direction tree, every to-do (a leader's workers are among them) and the rows. */
export interface GraphData {
  tree: DirectionTree | null | undefined;
  todos: readonly Todo[];
  lookup: SessionLookup;
  /** Complete session rows, when the view has them: a list's summary row leaves out a session's subagents (running and
   *  ended), so they are read from here. */
  complete?: SessionLookup;
}

const ms = (iso: string | null | undefined) => {
  const v = iso ? Date.parse(iso) : NaN;
  return Number.isFinite(v) ? v : undefined;
};
const ref = (mid: string, sid: string) => `${mid}/${sid}`;

/** Closed by the hub as done: a piece waiting for it may go on. A piece that did not start is not done. */
export const isDoneTodo = (t: Pick<Todo, "done_at" | "start_error">) => Boolean(t.done_at) && !t.start_error;

/** How deep workers of workers go at most (a leader's workers are plain workers: one level is what the hub allows). */
const MAX_DEPTH = 4;

/**
 * Where each to-do of a list is drawn, one rule for the graph's cards and the pieces' Agent trees, so every to-do is drawn
 * exactly once:
 * - `pieceOf`: the request it is a piece of (a listed to-do other than itself that is no piece itself; the hub keeps one
 *   level). A piece has its own card, never a line in another piece's tree.
 * - `roots`: the to-dos some piece names: each is a request's root card.
 * - `workerOf`: the to-do whose Agent handed it out (its ledger's supervisor, else its origin), when that to-do is drawn
 *   with an Agent tree: the worker is a line in that tree. A piece, a root, and a to-do whose chain of dispatchers loops
 *   are never workers.
 * - `rootWorkerOf`: the root whose own Agent handed it out. A root's card has no Agent tree, so such a to-do is drawn
 *   as a piece of that request instead of being hidden.
 */
export interface Placement {
  pieceOf: (t: Todo) => string | undefined;
  roots: ReadonlySet<string>;
  workerOf: (t: Todo) => string | undefined;
  rootWorkerOf: (t: Todo) => string | undefined;
}

const placementCache = new WeakMap<readonly Todo[], Placement>();

export function placement(todos: readonly Todo[]): Placement {
  const cached = placementCache.get(todos);
  if (cached) return cached;
  const byId = new Map(todos.map((t) => [t.id, t]));
  const pieceOf = (t: Todo) => {
    const p = t.part_of;
    return p && p !== t.id && byId.has(p) && !byId.get(p)!.part_of ? p : undefined;
  };
  const roots = new Set<string>();
  for (const t of todos) {
    const p = pieceOf(t);
    if (p) roots.add(p);
  }
  const owners = agentRefs(todos);
  // the to-do whose Agent handed `t` out, none for a piece or a root (each has a card of its own)
  const dispatcher = (t: Todo) => {
    if (pieceOf(t) || roots.has(t.id)) return undefined;
    const owner = owners.get(t.supervision?.supervisor || t.origin || "");
    return owner && owner !== t.id ? owner : undefined;
  };
  // a chain of dispatchers that comes back to `t`: none of them is a worker, so each keeps a card
  const loops = (t: Todo) => {
    const seen = new Set([t.id]);
    for (let d = dispatcher(t); d; d = byId.get(d) && dispatcher(byId.get(d)!)) {
      if (seen.has(d)) return true;
      seen.add(d);
    }
    return false;
  };
  const p: Placement = {
    pieceOf,
    roots,
    workerOf: (t) => {
      const d = dispatcher(t);
      return d && !roots.has(d) && !loops(t) ? d : undefined;
    },
    rootWorkerOf: (t) => {
      const d = dispatcher(t);
      return d && roots.has(d) ? d : undefined;
    },
  };
  placementCache.set(todos, p);
  return p;
}

/** The direction tree's task for a to-do, wherever it is filed. */
function directionTask(tree: DirectionTree | null | undefined, todoId: string): DirectionTask | undefined {
  for (const d of tree?.directions ?? []) {
    const hit = d.tasks?.find((td) => td.id === todoId);
    if (hit) return hit;
  }
  return undefined;
}

/** The to-dos a leader's Agent handed out (its workers), open and closed: their ledger names it as supervisor, or their
 *  origin is it. A closed one stays on the leader's piece, ended, while the piece goes on. */
function workersOf(todos: readonly Todo[], mid: string, sid: string): Todo[] {
  const r = ref(mid, sid);
  return todos.filter((td) => td.supervision?.supervisor === r || td.origin === r);
}

type AgentLink = { machine_id: string; session_id: string; title?: string; agent?: string; state?: string; children?: DirectionAgent[] };

/** The first line of what was said, trimmed. */
const firstLine = (s: string | undefined | null) => (s ?? "").trim().split(/\r?\n/, 1)[0].trim();

/** The subagent id in a direction tree child's session id ("<session>@<agent id>", the hub's sessionKids), when it is
 *  one of session `sid`'s in-session subagents. */
const subagentId = (childSid: string, sid: string) => (childSid.startsWith(`${sid}@`) ? childSid.slice(sid.length + 1) : undefined);

/** The session a subagent of session `sid` opens: its own link as the hub sends it ("<sid>@<id>", or a session of its
 *  own), else "<sid>@<id>"; never the launching session itself, which would open the Agent that launched it. */
export function subagentLink(sub: Pick<Subagent, "session_id"> | undefined, sid: string, id: string): string {
  const own = sub?.session_id?.trim();
  return own && own !== sid ? own : `${sid}@${id}`;
}

/** Which to-do had each Agent (refs "machine/session"), per to-do list: a direction tree child that is another to-do's
 *  Agent is a worker. */
const ownersCache = new WeakMap<readonly Todo[], Map<string, Todo>>();
function ownerOfAgent(todos: readonly Todo[], key: string): Todo | undefined {
  let owners = ownersCache.get(todos);
  if (!owners) {
    owners = new Map();
    for (const t of todos) for (const a of t.agents ?? []) if (!owners.has(ref(a.machine_id, a.session_id))) owners.set(ref(a.machine_id, a.session_id), t);
    ownersCache.set(todos, owners);
  }
  return owners.get(key);
}

/**
 * Every Agent on a to-do, as a tree in reading order (each row before what hangs under it): its own (the direction tree's,
 * else the to-do's own links), and under each, first the workers it handed out (open and closed) with their own trees,
 * then the Agents it spawned, then its in-session subagents (running and ended, nested by the subagent that launched
 * each, the earliest first); the ones it had before come last. A summary row has no subagents, so they are read from the
 * complete row when there is one, and the direction tree fills in subagents the session row does not have.
 */
export function nodeAgents(todo: Todo, data: GraphData): NodeAgent[] {
  const out: NodeAgent[] = [];
  const seen = new Set<string>();
  // the to-dos drawn in this tree: this one, then each worker as it is reached; a to-do is a worker line here only when
  // its dispatcher is one of them (placement.workerOf), so a piece, or a worker drawn in another tree, is never repeated
  const place = placement(data.todos);
  const inTree = new Set([todo.id]);
  const ours = (w: Todo) => {
    const d = place.workerOf(w);
    return d !== undefined && inTree.has(d);
  };
  const subagents = (a: AgentLink, s: Session | undefined, depth: number, parent: string) => {
    const mid = a.machine_id, sid = a.session_id;
    const list = data.complete?.(mid, sid)?.subagents ?? s?.subagents ?? [];
    type Sub = { id: string; parent?: string; title: string; running: boolean; at?: number; doing: string; sub?: Subagent; i: number };
    const subs: Sub[] = list.map((x, i) => ({
      id: x.id, parent: x.parent_id, title: x.description?.trim() || x.type || "", running: Boolean(x.running), at: ms(x.started_at),
      doing: firstLine(x.running ? x.last_tool?.summary || x.last_text : x.last_text), sub: x, i,
    }));
    const fromTree = (kids: DirectionAgent[] | undefined, under?: string) => {
      for (const c of kids ?? []) {
        const id = subagentId(c.session_id, sid);
        if (id === undefined) continue;
        if (!subs.some((x) => x.id === id)) subs.push({ id, parent: under, title: c.title, running: c.state === "working", doing: "", i: subs.length });
        fromTree(c.children, id);
      }
    };
    fromTree(a.children);
    if (!subs.length) return;
    const ids = new Set(subs.map((x) => x.id));
    const order = (x: Sub, y: Sub) => (x.at ?? Infinity) - (y.at ?? Infinity) || x.i - y.i;
    const placed = new Set<string>();
    const emit = (x: Sub, d: number, under: string) => {
      placed.add(x.id);
      const key = `sub:${mid}/${sid}/${x.id}`;
      out.push({
        key, mid, agent: s?.agent || a.agent, subagent: x.sub, title: x.title, state: x.running ? "working" : "", role: "subagent",
        depth: d, parent: under, ended: !x.running, doing: x.doing, open: { mid, sid: subagentLink(x.sub, sid, x.id) },
      });
      for (const y of subs.filter((y) => y.parent === x.id && y.id !== x.id && !placed.has(y.id)).sort(order)) emit(y, d + 1, key);
    };
    const top = subs.filter((x) => !x.parent || x.parent === x.id || !ids.has(x.parent)).sort(order);
    for (const x of top) if (!placed.has(x.id)) emit(x, depth, parent);
    // launched by one another in a loop: at the top, so none is lost
    for (const x of [...subs].sort(order)) if (!placed.has(x.id)) emit(x, depth, parent);
  };
  const visit = (a: AgentLink, role: AgentRole, depth: number, parent: string | undefined, ended: boolean) => {
    const s = data.lookup(a.machine_id, a.session_id);
    const key = ref(a.machine_id, a.session_id);
    seen.add(key);
    const state = (s ? foldedState(s) : (a.state as AgentState | undefined)) || "";
    out.push({
      key, mid: a.machine_id, agent: s?.agent || a.agent, session: s, title: s?.title || a.title || "", state, role, depth, parent, ended,
      doing: firstLine(taskNow(s, !ended && state === "working")?.text), open: { mid: a.machine_id, sid: a.session_id },
    });
    const kids = (a.children ?? []).filter((c) => subagentId(c.session_id, a.session_id) === undefined);
    // the workers it handed out: those the direction tree hangs under it, then those the to-dos name it for
    if (depth < MAX_DEPTH) {
      const workers: Todo[] = [];
      for (const c of kids) {
        const owner = ownerOfAgent(data.todos, ref(c.machine_id, c.session_id));
        if (owner && owner.id !== todo.id && ours(owner) && !workers.includes(owner)) workers.push(owner);
      }
      for (const w of workersOf(data.todos, a.machine_id, a.session_id)) if (w.id !== todo.id && ours(w) && !workers.includes(w)) workers.push(w);
      for (const w of workers) {
        inTree.add(w.id);
        on(w, "worker", depth + 1, key, Boolean(w.done_at));
      }
    }
    // what it spawned; an Agent of another to-do drawn elsewhere (a piece, another tree's worker) is not repeated here
    for (const c of kids) {
      if (seen.has(ref(c.machine_id, c.session_id))) continue;
      const owner = ownerOfAgent(data.todos, ref(c.machine_id, c.session_id));
      if (owner && !inTree.has(owner.id)) continue;
      visit(c, "spawned", depth + 1, key, false);
    }
    subagents(a, s, depth + 1, key);
  };
  const on = (td: Todo, role: AgentRole, depth: number, parent: string | undefined, ended: boolean) => {
    const tree = directionTask(data.tree, td.id)?.agents;
    let list: AgentLink[] = tree?.length ? tree : (td.agents ?? []).filter((a) => !a.released_at);
    // a closed worker's Agents may all be released: the one it had last stands for it
    if (!list.length && ended && td.agents?.length) list = [td.agents[td.agents.length - 1]];
    for (const a of list) if (!seen.has(ref(a.machine_id, a.session_id))) visit(a, role, depth, parent, ended);
  };
  on(todo, "lead", 0, undefined, false);
  for (const a of todo.agents ?? []) {
    if (!a.released_at || seen.has(ref(a.machine_id, a.session_id))) continue;
    const s = data.lookup(a.machine_id, a.session_id);
    seen.add(ref(a.machine_id, a.session_id));
    out.push({
      key: ref(a.machine_id, a.session_id), mid: a.machine_id, agent: s?.agent || a.agent, session: s, title: s?.title || a.title || "",
      state: s ? foldedState(s) : "", role: "before", depth: 0, ended: true, doing: firstLine(s?.last_text), open: { mid: a.machine_id, sid: a.session_id },
    });
  }
  return out;
}

/** How many ended subagents of one parent stay on their own line; the earlier ones fold into one "more ended" line. */
export const ENDED_KEPT = 3;

/**
 * A piece's Agent tree under its card: every Agent in `agents` (nodeAgents' order), when more than one is on it, the
 * earlier ones aside ([] otherwise: the card keeps its one Agent on its bottom line). Under one parent, ended subagents
 * that launched nothing beyond the latest ENDED_KEPT fold into one line, where the first of them was.
 */
export function agentTree(agents: readonly NodeAgent[]): TreeRow[] {
  if (agents.filter((a) => a.role !== "before").length <= 1) return [];
  const parents = new Set(agents.map((a) => a.parent).filter(Boolean));
  const leaf = (a: NodeAgent) => a.role === "subagent" && a.ended && !parents.has(a.key);
  const folded = new Set<string>();
  const groups = new Map<string, NodeAgent[]>();
  for (const a of agents) if (leaf(a)) groups.set(a.parent ?? "", [...(groups.get(a.parent ?? "") ?? []), a]);
  for (const list of groups.values()) for (const a of list.slice(0, Math.max(0, list.length - ENDED_KEPT))) folded.add(a.key);
  const rows: TreeRow[] = [];
  const told = new Set<string>();
  for (const a of agents) {
    if (!folded.has(a.key)) rows.push({ kind: "agent", key: a.key, depth: a.depth, agent: a });
    else if (!told.has(a.parent ?? "")) {
      told.add(a.parent ?? "");
      rows.push({ kind: "more", key: `more:${a.key}`, depth: a.depth, count: groups.get(a.parent ?? "")!.length - ENDED_KEPT, parent: a.parent });
    }
  }
  return rows;
}

// ── where a to-do stands ────────────────────────────────────

/** The four steps of the user's view of a to-do, from the states lib/todos.ts already works out (todoStatus, reviewState,
 *  openAsk); no state of its own is made up here:
 *    needs_me   something on it waits on the user: a decision it asks (openAsk with outcome decide / cannot), an Agent
 *               that needs them, a supervisor that passed it up (needs_user), an Agent cut off by a model error, an
 *               Agent that did not start;
 *    review     it says the work is done and waits to be accepted: its supervisor's review is pending, its Agent
 *               reported done (report_to_supervisor), its ask is "please verify" / a conclusion, or the hub closed it
 *               as done (done_by auto) and the user has not looked at it yet;
 *    working    everything else still open: working, waiting, stopped, not started;
 *    accepted   closed: accepted by its supervisor, marked done by the user, closed after the user saw it;
 *    cancelled  closed by cancellation, never counted as accepted or done. */
export type PieceStep = "needs_me" | "review" | "working" | "accepted" | "cancelled";

/** Where a to-do stands; `status` is its todoStatus. */
export function pieceStep(todo: Todo, status: TodoStatus): PieceStep {
  if (isCancelled(todo)) return "cancelled";
  if (status === "failed") return "needs_me";
  if (todo.done_at) return isAutoDone(todo) && !todo.seen_at ? "review" : "accepted";
  const ask = openAsk(todo);
  if (ask && (ask.outcome === "decide" || ask.outcome === "cannot")) return "needs_me";
  const review = reviewState(todo);
  if (status === "needs_you" || status === "interrupted" || review === "needs_user") return "needs_me";
  if (status === "working" || status === "waiting") return "working";
  if (acceptedAtRest(todo, status)) return "accepted";
  if (ask || review === "pending_review" || reportedDone(todo)) return "review";
  return "working";
}

/** Its Agent's latest report says done and no review has answered it yet. */
function reportedDone(todo: Todo): boolean {
  const sup = todo.supervision;
  if (sup?.progress?.kind !== "done") return false;
  const last = sup.reviews?.[sup.reviews.length - 1];
  return !last || !sup.progress.at || Date.parse(last.at) < Date.parse(sup.progress.at);
}

/** A line of a request to-do's notes that quotes the owner, as the main assistant writes it: `用户原话（<when>）：…`,
 *  `原话：…`, `ユーザーの言葉：…`, `Your words: …`, `Owner's words (…): …`. Its text is the rest of the line (else the next
 *  line that has any), outer quote marks taken off. */
const QUOTE_LINE = /^\s*(?:(?:用户|用戶)?的?原[话話文]|(?:ユーザーの|ご本人の)?(?:原文|言葉)|(?:the\s+)?(?:owner'?s?|user'?s?|your|original|own)\s+words)\s*(?:[（(][^）)]*[）)])?\s*[:：]\s*(.*)$/i;
const QUOTED = /^\s*[「“"『‘'](.*)[」”"』’']\s*$/s;

/** The owner's words a to-do's notes quote (the first such line: the request as first said); "" when none does. */
export function notesWords(notes: string | undefined | null): string {
  const lines = (notes ?? "").split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const m = QUOTE_LINE.exec(lines[i]);
    if (!m) continue;
    const text = m[1].trim() || (lines.slice(i + 1).find((l) => l.trim()) ?? "").trim();
    return (QUOTED.exec(text)?.[1] ?? text).trim();
  }
  return "";
}

/** The owner's own words for a to-do: as its brief keeps them (the hub's record), else as its notes quote them. */
export function ownerWords(todo: Pick<Todo, "brief" | "notes">): string {
  return todo.brief?.request?.text?.trim() || notesWords(todo.notes);
}

/** A request's heading from the sentence its work was filed under and the owner's words: the filed sentence, the words
 *  quoted under it when they say something else; the words themselves when nothing was filed. */
export function requestHeading(filed: string | undefined, said: string): { title: string; own: boolean; quote: string } {
  const title = filed?.trim() ?? "";
  if (!title) return { title: said, own: Boolean(said), quote: "" };
  return { title, own: false, quote: said && !sameWords(said, title) ? said : "" };
}

/** When it was asked: the owner's message when the brief has it, else when the to-do was made. */
export function requestedAt(todo: Pick<Todo, "created_at" | "brief">): string {
  return todo.brief?.request?.at || todo.created_at;
}

/** A to-do's piece: its state, what it asks, since when (prerequisites are filled in later). */
function todoNode(todo: Todo, data: GraphData, title?: string): GraphNode {
  const lookup = data.lookup;
  const status = todoStatus(todo, lookup);
  const step = pieceStep(todo, status);
  const active = (todo.agents ?? []).filter((a) => !a.released_at);
  const sessions = active.map((a) => lookup(a.machine_id, a.session_id)).filter((s): s is Session => Boolean(s));
  let state: NodeState;
  if (step === "cancelled") state = "cancelled";
  else if (todo.start_error) state = "failed";
  else if (step === "accepted") state = "done";
  else if (step === "review") state = "review";
  else if (step === "needs_me") state = "needs_you";
  else if (!active.length) state = "ready";
  else if (status === "working" || status === "waiting") state = "working";
  else if (status === "unread") state = "review";
  else state = "stopped";
  const ask = openAsk(todo);
  const asking = sessions.find((s) => foldedState(s) === "needs_you");
  const cut = sessions.map((s) => interruption(s)).find(Boolean);
  const text = state === "failed" ? todo.start_error ?? ""
    : state === "cancelled" ? todo.cancelled_reason || todo.done_reason || ""
    : state === "needs_you" ? ask?.question?.trim() || ask?.verdict?.trim() || asking?.asks?.trim() || cut?.reason || ""
    : state === "review" ? ask?.verdict?.trim() || todo.supervision?.progress?.line || "" : "";
  const working = sessions.filter((s) => foldedState(s) === "working").map((s) => ms(s.state_since)).filter((v): v is number => v !== undefined);
  const since = state === "working" ? (working.length ? Math.min(...working) : undefined)
    : state === "needs_you" ? ms(ask?.asked_at) ?? ms(asking?.state_since)
    : state === "done" ? ms(todo.done_at)
    : state === "cancelled" ? ms(todo.cancelled_at) ?? ms(todo.done_at)
    : ms(todo.created_at);
  const agents = nodeAgents(todo, data);
  return {
    key: `todo:${todo.id}`, title: title || todo.title, todo, layer: 1, row: 0, after: [], waitsFor: [], waitsStart: false,
    state, status, agents, tree: agentTree(agents), ask: text, since,
  };
}

function planTaskNode(plan: TaskPlan, task: PlanTask, i: number): GraphNode {
  return { key: `plan:${plan.id}/${i}`, title: task.title, task, layer: 1, row: 0, after: [], waitsFor: [], waitsStart: true, state: "queued", agents: [], tree: [], ask: "", since: ms(plan.created_at) };
}

// ── layers ──────────────────────────────────────────────────

/**
 * Fills in each node's prerequisites (`wants`: node key → the keys it names), drops an edge that would close a cycle
 * (walking the nodes in their order, an edge back into the walk in progress), then lays the nodes out: layer = 1 + the
 * deepest layer among its prerequisites, rows in a layer by the average row of their prerequisites (fewer crossings),
 * ties in the given order. Returns the edges: root → every layer-1 node, prerequisite → dependent.
 */
export function layOut(nodes: GraphNode[], wants: Map<string, string[]>): { edges: GraphEdge[]; layers: number; rows: number } {
  const byKey = new Map(nodes.map((n) => [n.key, n]));
  const kept = new Map<string, string[]>();
  const mark = new Map<string, 1 | 2>(); // 1: on the walk, 2: finished
  const walk = (key: string) => {
    mark.set(key, 1);
    const keep: string[] = [];
    for (const w of wants.get(key) ?? []) {
      if (w === key || !byKey.has(w) || keep.includes(w)) continue;
      if (mark.get(w) === 1) continue; // closes a cycle: dropped
      if (!mark.has(w)) walk(w);
      keep.push(w);
    }
    kept.set(key, keep);
    mark.set(key, 2);
  };
  for (const n of nodes) if (!mark.has(n.key)) walk(n.key);
  const layerOf = new Map<string, number>();
  const layer = (key: string): number => {
    const have = layerOf.get(key);
    if (have) return have;
    const v = 1 + Math.max(0, ...(kept.get(key) ?? []).map(layer));
    layerOf.set(key, v);
    return v;
  };
  for (const n of nodes) {
    n.after = kept.get(n.key) ?? [];
    n.layer = layer(n.key);
  }
  const layers = Math.max(0, ...nodes.map((n) => n.layer));
  let rows = 0;
  for (let l = 1; l <= layers; l++) {
    const here = nodes.map((n, i) => ({ n, i })).filter((x) => x.n.layer === l);
    const center = (n: GraphNode) => (n.after.length ? n.after.reduce((s, k) => s + byKey.get(k)!.row, 0) / n.after.length : Infinity);
    if (l > 1) here.sort((a, b) => center(a.n) - center(b.n) || a.i - b.i);
    here.forEach((x, r) => (x.n.row = r));
    rows = Math.max(rows, here.length);
  }
  const edges: GraphEdge[] = [];
  for (const n of nodes) {
    const dashed = n.state === "queued";
    if (n.layer === 1) edges.push({ from: "root", to: n.key, dashed });
    for (const a of n.after) edges.push({ from: a, to: n.key, dashed });
  }
  return { edges, layers, rows };
}

/** A node and everything above and below it (its ancestors, its descendants), with "root" for the request's words. */
export function lineage(req: Pick<GraphRequest, "nodes">, key: string): Set<string> {
  const byKey = new Map(req.nodes.map((n) => [n.key, n]));
  if (!byKey.has(key)) return new Set(key === "root" ? ["root", ...byKey.keys()] : []);
  const out = new Set<string>(["root", key]);
  const up = (k: string) => byKey.get(k)?.after.forEach((a) => !out.has(a) && (out.add(a), up(a)));
  const down = (k: string) => req.nodes.forEach((n) => n.after.includes(k) && !out.has(n.key) && (out.add(n.key), down(n.key)));
  up(key);
  down(key);
  return out;
}

// ── requests ────────────────────────────────────────────────

/** The refs ("machine/session") of every Agent a to-do had, so its workers can be told from requests of their own. */
function agentRefs(todos: readonly Todo[]): Map<string, string> {
  const owner = new Map<string, string>();
  for (const t of todos) for (const a of t.agents ?? []) if (!owner.has(ref(a.machine_id, a.session_id))) owner.set(ref(a.machine_id, a.session_id), t.id);
  return owner;
}

/** The same words, spaces aside (trimmed, runs of white space as one). */
export const sameWords = (a: string, b: string) => a.trim().replace(/\s+/g, " ") === b.trim().replace(/\s+/g, " ");

function finish(req: Omit<GraphRequest, "edges" | "layers" | "rows" | "done" | "touched" | "finished" | "merged">, wants: Map<string, string[]>, todosById: ReadonlyMap<string, Todo>): GraphRequest {
  const byKey = new Map(req.nodes.map((n) => [n.key, n]));
  // a piece is queued while something it waits for is not done, and nobody took it yet
  const { edges, layers, rows } = layOut(req.nodes, wants);
  for (const node of req.nodes) {
    const blockers = node.todo?.prerequisite_blockers;
    node.waitsFor = blockers !== undefined
      ? [...new Set(blockers)].map((id) => {
          const key = `todo:${id}`;
          return { key, title: byKey.get(key)?.title || todosById.get(id)?.title || id };
        })
      : node.after.flatMap((key) => {
          const prior = byKey.get(key)!;
          return prior.todo && isDoneTodo(prior.todo) ? [] : [{ key, title: prior.title }];
        });
    if (node.todo && node.waitsFor.length && node.state === "ready") node.state = "queued";
  }
  // edges again: a piece that just became queued is drawn dashed
  for (const e of edges) e.dashed = byKey.get(e.to)?.state === "queued";
  const todos = [...(req.root ? [req.root] : []), ...req.nodes.flatMap((n) => (n.todo ? [n.todo] : []))];
  const done = req.nodes.length > 0 && req.nodes.every((n) => n.state === "done") && (!req.root || isDoneTodo(req.root) && !isCancelled(req.root));
  const touched = Math.max(0, ms(req.plan?.created_at) ?? 0, ...todos.map((t) => ms(t.updated_at) ?? ms(t.created_at) ?? 0));
  const finished = Math.max(0, ...todos.map((t) => ms(t.done_at) ?? 0));
  const merged = req.nodes.length === 1 && sameWords(req.title, req.nodes[0].title) && (!req.root || mergesWithRoot(req.root, req.nodes[0]));
  return { ...req, edges: merged ? [] : edges, layers, rows, done, touched, finished, merged };
}

/**
 * Whether a request's one same-name piece may stand for its root to-do alone. The merged card opens the piece, so the
 * root's own card may go only when the root has nothing to say apart from it:
 *   1. both are closed or both open as stored (isDoneTodo), never as drawn: a piece the hub closed and the owner has
 *      not looked at is drawn `review` but is closed, a piece accepted and then reopened with nothing running is drawn
 *      `done` but is open, a failed start is closed with `start_error` but not done; a plan's task has no to-do yet, so
 *      its drawn state is all there is;
 *   2. a card drawn `done` never stands for a root still open: that request is among the open ones, and its root card
 *      is where it is completed.
 * Either one split keeps both cards (request-graph.test.ts "root × piece").
 */
export function mergesWithRoot(root: Todo, piece: GraphNode): boolean {
  if (isCancelled(root)) return false // the root's own cancellation and reason must stay visible beside the piece
  const rootDone = isDoneTodo(root);
  const pieceDone = piece.todo ? isDoneTodo(piece.todo) : piece.state === "done";
  return rootDone === pieceDone && (rootDone || piece.state !== "done");
}

const rank = (r: GraphRequest) => Math.min(STATE_RANK.done + 1, ...r.nodes.map((n) => STATE_RANK[n.state]));

/** A terminal request with a cancelled root or piece has its own outcome. An open root or piece keeps the request active,
 *  where its cancelled pieces remain visible without being counted as Done. */
const isCancelledRequest = (r: GraphRequest) => !r.done && (r.nodes.some((n) => n.state === "cancelled") || Boolean(r.root && isCancelled(r.root)))
  && r.nodes.every((n) => n.state === "done" || n.state === "cancelled") && (!r.root || isDoneTodo(r.root));

/** Every request, from the to-dos and the plans (see the head of this file). */
export function buildGraph({ todos: all, plans, data }: GraphInput): Graph {
  const todos = all;
  const byId = new Map(todos.map((t) => [t.id, t]));
  // a piece's request, or the root whose own Agent handed it out; a worker is drawn in its dispatcher's Agent tree
  const place = placement(todos);
  const rootOf = (t: Todo) => place.pieceOf(t) ?? place.rootWorkerOf(t) ?? t.id;
  const isWorker = (t: Todo) => place.workerOf(t) !== undefined;

  const groups = new Map<string, { kind: RequestKind; root?: Todo; plan?: TaskPlan; members: Todo[] }>();
  const group = (key: string, make: () => { kind: RequestKind; root?: Todo; plan?: TaskPlan }) => {
    let g = groups.get(key);
    if (!g) groups.set(key, (g = { ...make(), members: [] }));
    return g;
  };
  const roots = place.roots;
  for (const t of todos) {
    if (isWorker(t)) continue;
    if (roots.has(t.id)) {
      group(`todo:${t.id}`, () => ({ kind: "parent", root: t }));
      continue;
    }
    const r = rootOf(t);
    if (r !== t.id) group(`todo:${r}`, () => ({ kind: "parent", root: byId.get(r) })).members.push(t);
    else if (t.plan_id) group(`plan:${t.plan_id}`, () => ({ kind: "plan", plan: plans[t.plan_id!] })).members.push(t);
    else group(`todo:${t.id}`, () => ({ kind: "single" })).members.push(t);
  }
  // an open plan nobody started: its tasks wait for Start
  for (const p of Object.values(plans)) if (p.state === "open" && p.tasks.length) group(`plan:${p.id}`, () => ({ kind: "plan", plan: p }));

  const out: GraphRequest[] = [];
  for (const [key, g] of groups) {
    const wants = new Map<string, string[]>();
    let nodes: GraphNode[];
    if (g.plan && g.plan.state === "open") nodes = g.plan.tasks.map((task, i) => planTaskNode(g.plan!, task, i));
    else {
      // a plan's to-dos in its tasks' order, any others after them
      const order = (t: Todo) => {
        const i = g.plan?.tasks.findIndex((task) => task.todo_id === t.id) ?? -1;
        return i < 0 ? Number.MAX_SAFE_INTEGER : i;
      };
      const members = [...g.members].sort((a, b) => order(a) - order(b) || (ms(a.created_at) ?? 0) - (ms(b.created_at) ?? 0) || a.id.localeCompare(b.id));
      nodes = members.map((t) => todoNode(t, data, g.plan?.tasks.find((task) => task.todo_id === t.id)?.title));
      for (const t of members) wants.set(`todo:${t.id}`, (t.after ?? []).map((id) => `todo:${id}`));
    }
    if (!nodes.length) continue;
    let heading: { title: string; own: boolean; quote: string };
    let at: string | undefined;
    if (g.kind === "parent" && g.root) {
      // the request to-do's title, the owner's words (its brief, else its notes) under it
      heading = requestHeading(g.root.title, ownerWords(g.root));
      at = requestedAt(g.root);
    } else if (g.kind === "plan") {
      // the owner's words a plan's first task kept; one task: its title is the heading, as for a lone to-do
      const said = g.plan?.tasks.find((task) => task.brief?.request?.text?.trim())?.brief?.request?.text?.trim() ?? "";
      const first = g.members[0];
      const words = said || (first ? ownerWords(first) : "");
      heading = nodes.length === 1 ? requestHeading(nodes[0].title, words)
        : words ? { title: words, own: true, quote: "" } : requestHeading(first?.title ?? g.plan?.tasks[0]?.title, "");
      at = g.plan?.created_at || (first && requestedAt(first));
    } else {
      heading = requestHeading(g.members[0].title, ownerWords(g.members[0]));
      at = requestedAt(g.members[0]);
    }
    out.push(finish({ key, kind: g.kind, ...heading, at, root: g.root, plan: g.plan, nodes }, wants, byId));
  }
  const active = out.filter((r) => !r.done && !isCancelledRequest(r)).sort((a, b) => rank(a) - rank(b) || b.touched - a.touched || a.key.localeCompare(b.key));
  const done = out.filter((r) => r.done).sort((a, b) => b.finished - a.finished || a.key.localeCompare(b.key));
  const cancelled = out.filter(isCancelledRequest).sort((a, b) => b.finished - a.finished || a.key.localeCompare(b.key));
  return { active, done, cancelled };
}

// ── what is still queued ────────────────────────────────────

/** `ready`: nobody took it yet and nothing it waits for is pending; `after`: it waits for other pieces. */
export type QueueKind = "ready" | "after";

export interface QueueRow {
  key: string;
  kind: QueueKind;
  title: string;
  request: GraphRequest;
  node: GraphNode;
  /** The blockers to name in the queue summary. */
  waits: GraphWait[];
  since?: number;
}

/**
 * The pieces still queued, in order: first those nobody took yet with nothing pending before them (the longest waiting
 * first), then those that wait for others, in the graph's order (request, layer, row) so the list reads like the picture.
 * An open plan's tasks wait for Start and are not listed.
 */
export function queuedRows(graph: Graph): QueueRow[] {
  const rows: QueueRow[] = [];
  for (const request of graph.active) {
    if (request.plan?.state === "open") continue;
    for (const node of request.nodes) {
      const base = { key: node.key, title: node.title, request, node, since: node.since };
      if (node.state === "ready") rows.push({ ...base, kind: "ready", waits: [] });
      else if (node.state === "queued") rows.push({ ...base, kind: "after", waits: node.waitsFor });
    }
  }
  const active = new Map(graph.active.map((r, i) => [r.key, i]));
  return rows.sort((a, b) => {
    if (a.kind !== b.kind) return a.kind === "ready" ? -1 : 1;
    if (a.kind === "after") return (active.get(a.request.key)! - active.get(b.request.key)!) || a.node.layer - b.node.layer || a.node.row - b.node.row;
    return (a.since ?? Infinity) - (b.since ?? Infinity) || a.key.localeCompare(b.key);
  });
}

// ── geometry ────────────────────────────────────────────────

/** The card and the gaps, in px (the view draws its cards at these sizes). */
export const CARD_W = 200;
export const CARD_H = 92;
export const GAP_X = 44;
export const GAP_Y = 14;
export const PAD = 10;
/** A merged request's card: a piece's card with one more line on top, the request's label and time. */
export const MERGED_H = CARD_H + 20;
/** The root card's line quoting the owner's words under its heading. */
export const ROOT_QUOTE_H = 24;
/** A piece's Agent tree under its card: the line that names it and folds it (its top border included), an Agent's two
 *  lines, a "more ended" line, and the room under the last line. */
export const TREE_HEAD_H = 26;
export const TREE_ROW_H = 34;
export const TREE_MORE_H = 22;
export const TREE_PAD = 6;
const R = 8;

/** How tall a piece's Agent tree is under its card, open or folded to its first line (0: no tree). */
export function treeHeight(rows: readonly TreeRow[], open: boolean): number {
  if (!rows.length) return 0;
  return TREE_HEAD_H + (open ? rows.reduce((h, r) => h + (r.kind === "more" ? TREE_MORE_H : TREE_ROW_H), 0) + TREE_PAD : 0);
}

/** The root card's height: one more line when it quotes the owner. */
export const rootHeight = (req: Pick<GraphRequest, "quote">) => CARD_H + (req.quote ? ROOT_QUOTE_H : 0);

export interface Placed {
  key: string;
  x: number;
  y: number;
  /** Its whole height, the Agent tree under it included. */
  h: number;
}

export interface DrawnEdge extends GraphEdge {
  d: string;
  /** Where it leaves its source (the hollow dot). */
  dot: [number, number];
}

export interface RequestLayout {
  root: Placed;
  nodes: Map<string, Placed>;
  edges: DrawnEdge[];
  width: number;
  height: number;
}

const colX = (c: number) => PAD + c * (CARD_W + GAP_X);

/** A polyline through `pts`, its corners rounded (never by more than half of either leg). */
export function roundedPath(pts: [number, number][]): string {
  const p = pts.filter((q, i) => i === 0 || q[0] !== pts[i - 1][0] || q[1] !== pts[i - 1][1]);
  if (p.length < 2) return "";
  let d = `M${p[0][0]} ${p[0][1]}`;
  for (let i = 1; i < p.length - 1; i++) {
    const [ax, ay] = p[i - 1], [bx, by] = p[i], [cx, cy] = p[i + 1];
    const l1 = Math.hypot(bx - ax, by - ay), l2 = Math.hypot(cx - bx, cy - by);
    const r = Math.min(R, l1 / 2, l2 / 2);
    const sx = bx - ((bx - ax) / l1) * r, sy = by - ((by - ay) / l1) * r;
    const ex = bx + ((cx - bx) / l2) * r, ey = by + ((cy - by) / l2) * r;
    d += ` L${sx} ${sy} Q${bx} ${by} ${ex} ${ey}`;
  }
  const last = p[p.length - 1];
  return `${d} L${last[0]} ${last[1]}`;
}

/**
 * Where everything of a request goes: the heading in column 0, row 0; a node of layer L in column L at its row. A row is
 * as tall as its tallest card (a card with its Agent tree under it, `tree` giving the tree's height; the root with its
 * quote). An edge leaves its source's right edge at the middle of the card's head (CARD_H), turns in the gap right of
 * it, and enters its target's left edge the same way; one that skips columns runs along the gutter between two rows
 * (above its source when its target is higher, else below), so it never crosses a card or a tree.
 */
export function layoutRequest(
  req: Pick<GraphRequest, "nodes" | "edges" | "layers" | "rows"> & { merged?: boolean; quote?: string },
  tree: (n: GraphNode) => number = () => 0,
): RequestLayout {
  if (req.merged) {
    // one card, in the heading's place: no root, no connector
    const only = req.nodes[0];
    const h = MERGED_H + tree(only);
    return { root: { key: "root", x: PAD, y: PAD, h: MERGED_H }, nodes: new Map([[only.key, { key: only.key, x: PAD, y: PAD, h }]]), edges: [], width: PAD * 2 + CARD_W, height: PAD * 2 + h };
  }
  const rows = Math.max(1, req.rows);
  const heights = new Map(req.nodes.map((n) => [n.key, CARD_H + tree(n)]));
  const rowH = Array.from({ length: rows }, (_, r) => Math.max(r === 0 ? rootHeight({ quote: req.quote ?? "" }) : CARD_H, ...req.nodes.filter((n) => n.row === r).map((n) => heights.get(n.key)!)));
  const tops: number[] = [];
  for (let r = 0, y = PAD; r < rows; r++) {
    tops.push(y);
    y += rowH[r] + GAP_Y;
  }
  const nodes = new Map<string, Placed>(req.nodes.map((n) => [n.key, { key: n.key, x: colX(n.layer), y: tops[n.row], h: heights.get(n.key)! }]));
  const root: Placed = { key: "root", x: colX(0), y: tops[0], h: rootHeight({ quote: req.quote ?? "" }) };
  const rowOf = new Map(req.nodes.map((n) => [n.key, n.row]));
  const colOf = new Map(req.nodes.map((n) => [n.key, n.layer]));
  const edges: DrawnEdge[] = req.edges.map((e) => {
    const s = e.from === "root" ? root : nodes.get(e.from)!;
    const t = nodes.get(e.to)!;
    const sc = e.from === "root" ? 0 : colOf.get(e.from)!, tc = colOf.get(e.to)!;
    const sr = e.from === "root" ? 0 : rowOf.get(e.from)!, tr = rowOf.get(e.to)!;
    const x1 = s.x + CARD_W, y1 = s.y + CARD_H / 2;
    const x2 = t.x, y2 = t.y + CARD_H / 2;
    const gapOut = x1 + GAP_X / 2, gapIn = x2 - GAP_X / 2;
    let pts: [number, number][];
    if (tc === sc + 1) pts = [[x1, y1], [gapOut, y1], [gapOut, y2], [x2, y2]];
    else {
      const gutter = tr < sr ? tops[sr] - GAP_Y / 2 : tops[sr] + rowH[sr] + GAP_Y / 2;
      pts = [[x1, y1], [gapOut, y1], [gapOut, gutter], [gapIn, gutter], [gapIn, y2], [x2, y2]];
    }
    return { ...e, d: roundedPath(pts), dot: [x1, y1] };
  });
  const cols = Math.max(1, req.layers + 1);
  return { root, nodes, edges, width: colX(cols - 1) + CARD_W + PAD, height: tops[rows - 1] + rowH[rows - 1] + PAD };
}
