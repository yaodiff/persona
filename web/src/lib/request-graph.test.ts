// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  agentTree, buildGraph, CARD_H, CARD_W, ENDED_KEPT, layoutRequest, lineage, mergesWithRoot, notesWords, ownerWords, pieceStep, queuedRows, requestHeading, rootHeight,
  roundedPath, subagentLink, TREE_HEAD_H, TREE_ROW_H, treeHeight, type GraphData, type GraphNode, type GraphRequest,
} from "./request-graph";
import { caseKey, caseName, caseTodos, MERGE_CASES, PIECES, ROOTS, type PieceKind, type RootKind } from "./request-graph-merge-cases";
import type { PlanTask, TaskPlan } from "./feed";
import { makeSessionLookup, todoStatus, type Todo } from "./todos";
import type { DirectionAgent, DirectionTree } from "./direction-tree";
import type { Session, Subagent } from "./types";

const at = (minutes: number) => new Date(Date.UTC(2026, 10, 14, 10, 0) + minutes * 60_000).toISOString();
const todo = (id: string, patch: Partial<Todo> = {}): Todo => ({ id, title: id, created_at: at(0), updated_at: at(0), agents: [], ...patch });
const session = (sid: string, patch: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: sid, title: `agent ${sid}`, cwd: "/code/app", status: "working", state: "working", ...patch }) as Session;
const link = (sid: string, patch: Partial<NonNullable<Todo["agents"]>[number]> = {}) => ({ machine_id: "mac", session_id: sid, linked_at: at(1), ...patch });
const data = (todos: Todo[], sessions: Session[] = []): GraphData => ({ todos, tree: null, lookup: makeSessionLookup(sessions) });
const task = (title: string, patch: Partial<PlanTask> = {}): PlanTask => ({ title, prompt: title, machine: "mac", cwd: "/code/app", ...patch });
const plan = (id: string, state: TaskPlan["state"], tasks: PlanTask[], minutes = 0): TaskPlan => ({ id, conversation: "main", state, tasks, created_at: at(minutes) });
const graph = (todos: Todo[], sessions: Session[] = [], plans: Record<string, TaskPlan> = {}) => buildGraph({ todos, plans, data: data(todos, sessions) });
const node = (r: GraphRequest, id: string) => r.nodes.find((n) => n.key === `todo:${id}`)!;

describe("requests", () => {
  it("groups the pieces a to-do is named parent of under it, in layers by what each waits for (a diamond)", () => {
    const todos = [
      todo("req", { title: "Ship the search page", brief: { request: { text: "please ship search", at: at(-5) } } as Todo["brief"] }),
      todo("a", { part_of: "req", done_at: at(5) }),
      todo("b", { part_of: "req", after: ["a"] }),
      todo("c", { part_of: "req", after: ["a"] }),
      todo("d", { part_of: "req", after: ["b", "c"] }),
    ];
    const { active, done } = graph(todos);
    expect(done).toEqual([]);
    expect(active).toHaveLength(1);
    const r = active[0];
    // the request to-do's title heads it; the owner's words, which say something else, are quoted under it
    expect([r.kind, r.title, r.own, r.quote, r.at]).toEqual(["parent", "Ship the search page", false, "please ship search", at(-5)]);
    expect(r.nodes.map((n) => [n.key, n.layer])).toEqual([["todo:a", 1], ["todo:b", 2], ["todo:c", 2], ["todo:d", 3]]);
    expect([r.layers, r.rows]).toEqual([3, 2]);
    expect(r.edges.map((e) => `${e.from}>${e.to}`)).toEqual(["root>todo:a", "todo:a>todo:b", "todo:a>todo:c", "todo:b>todo:d", "todo:c>todo:d"]);
    // a is done: b and c are ready (nobody took them); d waits for both
    expect([node(r, "a").state, node(r, "b").state, node(r, "c").state, node(r, "d").state]).toEqual(["done", "ready", "ready", "queued"]);
    expect(node(r, "d").waitsFor).toEqual([{ key: "todo:b", title: "b" }, { key: "todo:c", title: "c" }]);
    expect(r.edges.filter((e) => e.dashed).map((e) => e.to)).toEqual(["todo:d", "todo:d"]);
  });

  it("takes a node's layer from its deepest prerequisite when it has several", () => {
    const todos = [todo("req"), todo("a", { part_of: "req" }), todo("b", { part_of: "req", after: ["a"] }), todo("c", { part_of: "req", after: ["a", "b"] })];
    const r = graph(todos).active[0];
    expect(r.nodes.map((n) => [n.key, n.layer, n.after])).toEqual([["todo:a", 1, []], ["todo:b", 2, ["todo:a"]], ["todo:c", 3, ["todo:a", "todo:b"]]]);
    // c skips a column: its edge from a runs along the gutter, never across b
    const lay = layoutRequest(r);
    const b = lay.nodes.get("todo:b")!;
    const skip = lay.edges.find((e) => e.from === "todo:a" && e.to === "todo:c")!;
    const pts = [...skip.d.matchAll(/(-?[\d.]+) (-?[\d.]+)/g)].map((m) => [Number(m[1]), Number(m[2])]);
    const inside = ([x, y]: number[]) => x > b.x && x < b.x + CARD_W && y > b.y && y < b.y + CARD_H;
    // no point of the path, nor the middle of any of its legs, is inside b
    expect(pts.some(inside) || pts.slice(1).some((p, i) => inside([(p[0] + pts[i][0]) / 2, (p[1] + pts[i][1]) / 2]))).toBe(false);
  });

  it("ignores unknown ids and a to-do naming itself", () => {
    const todos = [todo("req"), todo("a", { part_of: "req", after: ["ghost", "a"] }), todo("lone", { part_of: "nobody" })];
    const { active } = graph(todos);
    const r = active.find((x) => x.key === "todo:req")!;
    expect(r.nodes.map((n) => [n.key, n.after, n.state])).toEqual([["todo:a", [], "ready"]]);
    expect(active.find((x) => x.key === "todo:lone")!.kind).toBe("single");
  });

  it("drops the edge that closes a cycle instead of looping", () => {
    const todos = [todo("req"), todo("a", { part_of: "req", after: ["c"] }), todo("b", { part_of: "req", after: ["a"] }), todo("c", { part_of: "req", after: ["b"] })];
    const r = graph(todos).active[0];
    // walking a → c → b → a: the edge back into a is dropped, the rest stays a DAG
    expect(r.nodes.map((n) => [n.key, n.layer, n.after])).toEqual([["todo:a", 3, ["todo:c"]], ["todo:b", 1, []], ["todo:c", 2, ["todo:b"]]]);
  });

  it("makes a plan's to-dos one request under the plan's words, edges from after, part_of winning", () => {
    const p = plan("p1", "started", [task("Fix", { todo_id: "f", brief: { request: { text: "fix it and test it", at: at(-3) } } as PlanTask["brief"] }), task("Test", { todo_id: "t" })], -2);
    const todos = [
      todo("t", { plan_id: "p1", after: ["f"], created_at: at(-1) }),
      todo("f", { plan_id: "p1", created_at: at(0), agents: [link("s1")] }),
      todo("own", { title: "Mine", plan_id: "p1", part_of: "other" }),
      todo("other"),
    ];
    const { active } = graph(todos, [session("s1", { state_since: at(1) })], { p1: p });
    const r = active.find((x) => x.kind === "plan")!;
    // a plan of several tasks has no sentence of its own: the owner's words head it
    expect([r.key, r.title, r.own, r.quote, r.at]).toEqual(["plan:p1", "fix it and test it", true, "", at(-2)]);
    // in the plan's order, whatever their creation
    expect(r.nodes.map((n) => [n.key, n.title, n.layer, n.state])).toEqual([["todo:f", "Fix", 1, "working"], ["todo:t", "Test", 2, "queued"]]);
    expect(node(r, "f").agents.map((a) => [a.key, a.role])).toEqual([["mac/s1", "lead"]]);
    expect(node(r, "f").since).toBe(Date.parse(at(1)));
    expect(active.find((x) => x.key === "todo:other")!.nodes.map((n) => n.key)).toEqual(["todo:own"]);
  });

  it("shows an open plan's tasks as pieces waiting for Start", () => {
    const p = plan("p2", "open", [task("One", { brief: { request: { text: "do one and two", at: at(0) } } as PlanTask["brief"] }), task("Two")]);
    const r = graph([], [], { p2: p, gone: plan("gone", "dismissed", [task("x")]) }).active;
    expect(r).toHaveLength(1);
    expect([r[0].title, r[0].nodes.map((n) => [n.title, n.state, n.waitsStart, n.layer])]).toEqual(["do one and two", [["One", "queued", true, 1], ["Two", "queued", true, 1]]]);
    expect(r[0].edges.every((e) => e.from === "root" && e.dashed)).toBe(true);
  });

  it("makes a lone to-do a request of one piece, under its own words", () => {
    const r = graph([todo("solo", { title: "Rename the repo", agents: [link("s1")] })], [session("s1", { state: "needs_you", status: "idle", asks: "Which name?" })]).active[0];
    expect([r.kind, r.title, r.own, r.quote]).toEqual(["single", "Rename the repo", false, ""]);
    expect([r.nodes[0].state, r.nodes[0].ask]).toEqual(["needs_you", "Which name?"]);
  });

  it("files finished requests under done, the latest first, and keeps a request with an open piece active", () => {
    const todos = [
      todo("old", { done_at: at(10) }),
      todo("new", { done_at: at(20) }),
      todo("req", { done_at: at(30) }),
      todo("piece", { part_of: "req" }),
      todo("broke", { done_at: at(40), start_error: "rpc: closed" }),
    ];
    const { active, done } = graph(todos);
    expect(done.map((r) => r.key)).toEqual(["todo:new", "todo:old"]);
    expect(active.map((r) => r.key).sort()).toEqual(["todo:broke", "todo:req"]);
    expect(active.find((r) => r.key === "todo:broke")!.nodes[0].state).toBe("failed");
  });

  it("keeps cancelled requests and pieces out of Done, including a mixed terminal request", () => {
    const todos = [
      todo("cancelled-root", { done_at: at(30) }),
      todo("cancelled-a", { part_of: "cancelled-root", done_at: at(31), cancelled_at: at(31), cancelled_reason: "Owner stopped it" }),
      todo("cancelled-b", { part_of: "cancelled-root", done_at: at(32), cancelled_at: at(32) }),
      todo("mixed-root", { done_at: at(40) }),
      todo("mixed-done", { part_of: "mixed-root", done_at: at(41) }),
      todo("mixed-cancelled", { part_of: "mixed-root", done_at: at(42), cancelled_at: at(42) }),
      todo("complete", { done_at: at(50) }),
    ];
    const g = graph(todos);
    expect(g.active).toEqual([]);
    expect(g.done.map((r) => r.key)).toEqual(["todo:complete"]);
    expect(g.cancelled.map((r) => r.key)).toEqual(["todo:mixed-root", "todo:cancelled-root"]);
    const cancelled = g.cancelled.find((r) => r.key === "todo:cancelled-root")!;
    expect(cancelled.nodes.map((n) => n.state)).toEqual(["cancelled", "cancelled"]);
    const mixed = g.cancelled.find((r) => r.key === "todo:mixed-root")!;
    expect(node(mixed, "mixed-done").state).toBe("done");
    expect(node(mixed, "mixed-cancelled").state).toBe("cancelled");
  });

  it("keeps a cancelled parent with ordinary Done pieces out of Done and preserves its root card", () => {
    const g = graph([
      todo("parent", { done_at: at(30), cancelled_at: at(30), cancelled_reason: "Owner stopped the request" }),
      todo("piece", { part_of: "parent", done_at: at(31) }),
    ]);
    expect(g.active).toEqual([]);
    expect(g.done).toEqual([]);
    expect(g.cancelled.map((r) => r.key)).toEqual(["todo:parent"]);
    expect(g.cancelled[0].merged).toBe(false);
    expect(node(g.cancelled[0], "piece").state).toBe("done");
  });

  it("draws a leader's workers among its Agents (a closed one ended), not as requests of their own, and lists earlier Agents last", () => {
    const todos = [
      todo("lead", { agents: [link("L"), link("old", { released_at: at(2) })] }),
      todo("w1", { origin: "mac/L", supervision: { supervisor: "mac/L" } as Todo["supervision"], agents: [link("W1")] }),
      todo("w2", { origin: "mac/L", agents: [link("W2")], done_at: at(9) }),
    ];
    const { active, done } = graph(todos, [session("L"), session("W1"), session("old", { state: "idle", status: "idle" })]);
    expect(done).toEqual([]);
    expect(active.map((r) => r.key)).toEqual(["todo:lead"]);
    expect(active[0].nodes[0].agents.map((a) => [a.key, a.role, a.depth, a.parent, a.ended])).toEqual([
      ["mac/L", "lead", 0, undefined, false], ["mac/W1", "worker", 1, "mac/L", false], ["mac/W2", "worker", 1, "mac/L", true], ["mac/old", "before", 0, undefined, true],
    ]);
  });

  describe("where a worker is drawn: once, as a card or as a line of exactly one Agent tree", () => {
    // each to-do's Agent is its id in capitals; `from` names the Agent that handed it out (its origin)
    const t = (id: string, patch: Partial<Todo> & { from?: string } = {}) => {
      const { from, ...rest } = patch;
      return todo(id, { agents: [link(id.toUpperCase())], ...(from ? { origin: `mac/${from}` as Todo["origin"] } : {}), ...rest });
    };
    const dir = (todoId: string, agents: DirectionAgent[]): DirectionTree =>
      ({ directions: [{ id: "main", name: "", tasks: [{ id: todoId, title: todoId, state: "working", agents }] }] }) as unknown as DirectionTree;
    const cases: { name: string; todos: Todo[]; tree?: DirectionTree; want: Record<string, string> }[] = [
      { name: "a leader's worker, on a request of its own", todos: [t("lead"), t("w", { from: "LEAD" })],
        want: { lead: "card todo:lead", w: "tree lead" } },
      { name: "a worker of a piece's leader", todos: [todo("r"), t("p", { part_of: "r" }), t("w", { from: "P" })],
        want: { r: "root todo:r", p: "card todo:r", w: "tree p" } },
      { name: "a worker that is itself a piece of the same request", todos: [todo("r"), t("p", { part_of: "r" }), t("w", { part_of: "r", from: "P" })],
        want: { r: "root todo:r", p: "card todo:r", w: "card todo:r" } },
      { name: "a worker that is a piece of another request", todos: [todo("r"), t("p", { part_of: "r" }), todo("r2"), t("w", { part_of: "r2", from: "P" })],
        want: { r: "root todo:r", p: "card todo:r", r2: "root todo:r2", w: "card todo:r2" } },
      { name: "a worker the request root's own Agent handed out, and that worker's own worker",
        todos: [t("r"), t("p", { part_of: "r" }), t("w", { from: "R" }), t("w2", { from: "W" })],
        want: { r: "root todo:r", p: "card todo:r", w: "card todo:r", w2: "tree w" } },
      { name: "a worker whose ledger names another supervisor than its origin", todos: [t("a"), t("b"), t("w", { from: "A", supervision: { supervisor: "mac/B" } as Todo["supervision"] })],
        want: { a: "card todo:a", b: "card todo:b", w: "tree b" } },
      { name: "a worker of a worker", todos: [t("lead"), t("w1", { from: "LEAD" }), t("w2", { from: "W1" })],
        want: { lead: "card todo:lead", w1: "tree lead", w2: "tree lead" } },
      { name: "another piece's Agent the direction tree hangs under a leader", todos: [todo("r"), t("p", { part_of: "r" }), t("q", { part_of: "r" })],
        tree: dir("p", [{ machine_id: "mac", session_id: "P", title: "p", state: "working", children: [{ machine_id: "mac", session_id: "Q", title: "q", state: "working" }] }]),
        want: { r: "root todo:r", p: "card todo:r", q: "card todo:r" } },
      { name: "two to-dos naming each other's Agents", todos: [t("a", { from: "B" }), t("b", { from: "A" })],
        want: { a: "card todo:a", b: "card todo:b" } },
      { name: "a worker whose dispatcher is not listed", todos: [t("w", { from: "NOBODY" })],
        want: { w: "card todo:w" } },
    ];
    it.each(cases)("$name", ({ todos, tree, want }) => {
      const g = buildGraph({ todos, plans: {}, data: { todos, tree: tree ?? null, lookup: makeSessionLookup([]) } });
      const byAgent = new Map<string, string>(todos.flatMap((td) => (td.agents ?? []).map((a) => [`${a.machine_id}/${a.session_id}`, td.id] as [string, string])));
      const places = new Map<string, string[]>();
      const put = (id: string, where: string) => places.set(id, [...(places.get(id) ?? []), where]);
      for (const r of [...g.active, ...g.done]) {
        if (r.root && !r.merged) put(r.root.id, `root ${r.key}`);
        for (const n of r.nodes) {
          if (!n.todo) continue;
          put(n.todo.id, `card ${r.key}`);
          for (const a of n.agents) {
            const owner = byAgent.get(a.key);
            if (owner && owner !== n.todo.id && a.role !== "subagent") put(owner, `tree ${n.todo.id}`);
          }
        }
      }
      // every to-do once, where it belongs
      expect(Object.fromEntries([...places].map(([id, w]) => [id, w.join(" + ")]))).toEqual(want);
    });

    it("draws a root's worker as a piece hanging from the root, and nests a worker's worker under it", () => {
      const todos = [t("r"), t("p", { part_of: "r" }), t("w", { from: "R" })];
      const r = graph(todos).active[0];
      expect(r.edges.map((e) => `${e.from}>${e.to}`)).toEqual(["root>todo:p", "root>todo:w"]);
      const nested = graph([t("lead"), t("w1", { from: "LEAD" }), t("w2", { from: "W1" })]).active[0].nodes[0].agents;
      expect(nested.map((a) => [a.key, a.role, a.depth, a.parent ?? ""])).toEqual([
        ["mac/LEAD", "lead", 0, ""], ["mac/W1", "worker", 1, "mac/LEAD"], ["mac/W2", "worker", 2, "mac/W1"],
      ]);
    });
  });

  it("puts the most active request first: what needs the owner, then work going on, then what waits", () => {
    const todos = [
      todo("idle", { updated_at: at(50) }),
      todo("busy", { agents: [link("w")], updated_at: at(10) }),
      todo("asks", { agents: [link("n")], updated_at: at(1) }),
    ];
    const order = graph(todos, [session("w"), session("n", { state: "needs_you", status: "idle" })]).active.map((r) => r.key);
    expect(order).toEqual(["todo:asks", "todo:busy", "todo:idle"]);
  });
});

describe("a request of one piece that says the same", () => {
  it("is drawn as the piece's card alone when its heading is the piece's title, spaces aside, the owner's other words quoted on it", () => {
    const g = graph([
      todo("same", { title: "Rename  the repo " , brief: { request: { text: " Rename the\nrepo", at: at(0) } } as Todo["brief"] }),
      // the owner said "Go ahead" to the assistant: the task's title is the heading, the words are quoted
      todo("goahead", { title: "Rename the repo", brief: { request: { text: "Go ahead", at: at(0) } } as Todo["brief"] }),
      todo("req", { title: "Search" }),
      todo("one", { title: "Search", part_of: "req" }),
      todo("ship", { title: "Ship it" }),
      todo("build", { title: "Build it", part_of: "ship" }),
    ]);
    const by = (k: string) => g.active.find((r) => r.key === k)!;
    expect([by("todo:same").merged, by("todo:same").edges, by("todo:same").quote]).toEqual([true, [], ""]);
    expect([by("todo:goahead").merged, by("todo:goahead").title, by("todo:goahead").quote]).toEqual([true, "Rename the repo", "Go ahead"]);
    expect(by("todo:ship").merged).toBe(false);
    expect(by("todo:ship").edges.map((e) => e.from)).toEqual(["root"]);
    // a parent with one piece of the same name merges too
    expect(by("todo:req").merged).toBe(true);
    const lay = layoutRequest(by("todo:same"));
    expect([lay.edges, lay.nodes.get("todo:same")!.x, lay.width]).toEqual([[], lay.root.x, lay.root.x * 2 + CARD_W]);
    expect(layoutRequest(by("todo:ship")).nodes.get("todo:build")!.x).toBeGreaterThan(CARD_W);
  });

  it("heads a one-task plan with its task's title, the owner's words quoted", () => {
    const p = plan("p", "started", [task("Tidy the By request headings", { todo_id: "x", brief: { request: { text: "Go ahead", at: at(0) } } as PlanTask["brief"] })]);
    const r = graph([todo("x", { plan_id: "p", title: "Tidy the By request headings" })], [], { p }).active[0];
    expect([r.kind, r.title, r.own, r.quote, r.merged]).toEqual(["plan", "Tidy the By request headings", false, "Go ahead", true]);
  });

  it("keeps the root's card when the root and its piece disagree on done", () => {
    const g = graph([
      todo("open", { title: "Search" }),
      todo("finished", { title: "Search", part_of: "open", done_at: at(5) }),
      todo("closed", { title: "Export", done_at: at(5) }),
      todo("going", { title: "Export", part_of: "closed" }),
      todo("both", { title: "Import", done_at: at(6) }),
      todo("both-piece", { title: "Import", part_of: "both", done_at: at(5) }),
    ]);
    const find = (k: string) => [...g.active, ...g.done].find((r) => r.key === k)!;
    // the piece is done, the request's to-do still open: the request stays open, its root card drawn
    const open = find("todo:open");
    expect([g.active.includes(open), open.done, open.merged, node(open, "finished").state]).toEqual([true, false, false, "done"]);
    expect(open.edges.map((e) => `${e.from}>${e.to}`)).toEqual(["root>todo:finished"]);
    const lay = layoutRequest(open);
    expect(lay.nodes.get("todo:finished")!.x).toBeGreaterThan(lay.root.x + CARD_W);
    // and the other way round: the root done, its piece not
    expect([find("todo:closed").done, find("todo:closed").merged]).toEqual([false, false]);
    // both done: one card again
    expect([find("todo:both").done, find("todo:both").merged]).toEqual([true, true]);
  });

  it("keeps the root's card for an open root whose same-name piece the hub closed and nobody looked at yet", () => {
    // stored closed (done_at, done_by auto) but drawn `review`: an open root and an unseen auto-done piece still split
    const g = graph([todo("req", { title: "Search" }), todo("piece", { title: "Search", part_of: "req", done_at: at(5), done_by: "auto", done_reason: "finished" })]);
    const req = g.active.find((r) => r.key === "todo:req")!;
    expect([Boolean(req), req.done, node(req, "piece").state, req.merged]).toEqual([true, false, "review", false]);
    expect(req.edges.map((e) => `${e.from}>${e.to}`)).toEqual(["root>todo:piece"]);
  });
});

describe("root × piece: when one card stands for both", () => {
  const cases = MERGE_CASES.map((c) => ({ ...c, name: caseName(c) }));

  it("covers every root and every piece", () => {
    const seen = new Set(MERGE_CASES.map(caseKey));
    for (const r of Object.keys(ROOTS)) for (const p of Object.keys(PIECES)) expect(seen.has(caseKey({ root: r as RootKind, piece: p as PieceKind })), `${r} × ${p}`).toBe(true);
    expect(seen.size).toBe(MERGE_CASES.length);
  });

  it.each(cases)("$name", (c) => {
    const { todos, sessions } = caseTodos(c, at, todo);
    const g = graph(todos, sessions);
    const req = [...g.active, ...g.done, ...g.cancelled].find((r) => r.key === `todo:r-${caseKey(c)}`)!;
    expect(req.kind).toBe("parent");
    const filed = g.done.includes(req) ? "done" : g.cancelled.includes(req) ? "cancelled" : "active";
    expect({ state: node(req, `p-${caseKey(c)}`).state, merged: req.merged, done: req.done, filed })
      .toEqual({ state: c.state, merged: c.merged, done: c.done, filed: c.cancelled ? "cancelled" : c.done ? "done" : "active" });
    // a merged block has no connectors; a split one has the root's
    expect(req.edges.map((e) => e.from)).toEqual(c.merged ? [] : ["root"]);
    // never a done-looking card standing for an open request
    if (req.merged && !req.done) expect(req.nodes[0].state).not.toBe("done");
  });

  it("the same, all in one graph: each request's answer is its own", () => {
    const all = MERGE_CASES.map((c) => caseTodos(c, at, todo));
    const g = graph(all.flatMap((x) => x.todos), all.flatMap((x) => x.sessions));
    for (const c of MERGE_CASES) {
      const req = [...g.active, ...g.done, ...g.cancelled].find((r) => r.key === `todo:r-${caseKey(c)}`)!;
      expect([req.merged, req.done], caseName(c)).toEqual([c.merged, c.done]);
    }
  });

  it("merges a request with no root to-do by its words alone: a lone to-do in any state, an open plan's one task", () => {
    for (const kind of Object.keys(PIECES) as PieceKind[]) {
      const lone = todo(`solo-${kind}`, { title: "Rename", ...PIECES[kind].patch(at) });
      const lg = graph([lone], (PIECES[kind] as { sessions?: (a: typeof at) => Session[] }).sessions?.(at) ?? []);
      const r = [...lg.active, ...lg.done, ...lg.cancelled][0];
      expect([r.kind, r.merged], kind).toEqual(["single", true]);
    }
    const r = graph([], [], { p: plan("p", "open", [task("Rename", { brief: { request: { text: "Rename", at: at(0) } } as PlanTask["brief"] })]) }).active[0];
    expect([r.kind, r.nodes[0].todo, r.nodes[0].state, r.merged]).toEqual(["plan", undefined, "queued", true]);
  });

  it("answers mergesWithRoot from the stored state, and the drawn one only for a plan's task with no to-do", () => {
    const n = (patch: Partial<GraphNode>): GraphNode => ({ key: "k", title: "t", layer: 1, row: 0, after: [], waitsFor: [], waitsStart: false, state: "ready", agents: [], tree: [], ask: "", ...patch });
    // a task with no to-do: its drawn state is all there is
    expect([mergesWithRoot(todo("r"), n({ state: "queued" })), mergesWithRoot(todo("r", { done_at: at(1) }), n({ state: "done" }))]).toEqual([true, true]);
    expect(mergesWithRoot(todo("r"), n({ state: "done" }))).toBe(false);
    // a to-do's stored state wins over what it is drawn as
    expect(mergesWithRoot(todo("r"), n({ state: "review", todo: todo("p", { done_at: at(1), done_by: "auto" }) }))).toBe(false);
    expect(mergesWithRoot(todo("r", { done_at: at(1) }), n({ state: "review", todo: todo("p", { done_at: at(1), done_by: "auto" }) }))).toBe(true);
  });
});

describe("a piece of a piece", () => {
  it("keeps one level: a to-do whose part_of names another piece, or a loop, is a request of its own", () => {
    const g = graph([todo("req"), todo("a", { part_of: "req" }), todo("deep", { part_of: "a" }), todo("x", { part_of: "y" }), todo("y", { part_of: "x" })]);
    expect(g.active.map((q) => [q.key, q.kind, q.nodes.map((n) => n.key)]).sort()).toEqual([
      ["todo:deep", "single", ["todo:deep"]], ["todo:req", "parent", ["todo:a"]], ["todo:x", "single", ["todo:x"]], ["todo:y", "single", ["todo:y"]],
    ]);
  });
});

describe("lineage", () => {
  it("is a node, its ancestors and its descendants, with the words", () => {
    const todos = [todo("req"), todo("a", { part_of: "req" }), todo("b", { part_of: "req", after: ["a"] }), todo("c", { part_of: "req", after: ["a"] }), todo("d", { part_of: "req", after: ["b"] })];
    const r = graph(todos).active[0];
    expect([...lineage(r, "todo:b")].sort()).toEqual(["root", "todo:a", "todo:b", "todo:d"]);
    expect([...lineage(r, "todo:c")].sort()).toEqual(["root", "todo:a", "todo:c"]);
    expect(lineage(r, "nope").size).toBe(0);
  });
});

describe("the queue", () => {
  it("lists the pieces nobody took first, the longest waiting first, then the pieces that wait for others in the graph's order", () => {
    const todos = [
      todo("req"),
      todo("a", { part_of: "req", agents: [link("asker")] }),
      todo("b", { part_of: "req", after: ["a"] }),
      todo("c", { part_of: "req", created_at: at(3) }),
      todo("d", { part_of: "req", done_at: at(4), start_error: "no machine" }),
      todo("e", { part_of: "req", ask: { verdict: "Done, please check", asked_at: at(6) } }),
      todo("f", { part_of: "req", created_at: at(2) }),
      todo("solo", { agents: [link("w")] }),
    ];
    const p = plan("p", "open", [task("X"), task("Y")], 2);
    const g = graph(todos, [session("asker", { state: "needs_you", status: "idle", asks: "Which DB?", state_since: at(5) }), session("w")], { p });
    const rows = queuedRows(g);
    // what needs the owner, what works, what failed and what waits to be accepted are not queued; an open plan waits for Start
    expect(rows.map((q) => [q.kind, q.title])).toEqual([["ready", "f"], ["ready", "c"], ["after", "b"]]);
    expect(rows[2].waits.map((n) => n.key)).toEqual(["todo:a"]);
    expect(rows[2].request.key).toBe("todo:req");
  });

  it("uses the hub's blockers for strict pieces, including an empty list, without changing layout or legacy readiness", () => {
    const todos = [
      todo("req"),
      todo("cancelled", { title: "Cancelled task", part_of: "req", done_at: at(2), done_by: "review", supervision: { supervisor: "main", state: "cancelled" } }),
      todo("reopened", { title: "Reopened task", part_of: "req", reopened_at: at(3) }),
      todo("known-elsewhere", { title: "Another request" }),
      todo("legacy", { part_of: "req", after: ["cancelled", "missing"] }),
      todo("strict-ready", { part_of: "req", after: ["reopened"], prerequisite_blockers: [] }),
      todo("strict-blocked", { part_of: "req", after: ["cancelled", "reopened", "missing"], prerequisite_blockers: ["cancelled", "missing", "reopened", "known-elsewhere"] }),
    ];
    const result = graph(todos);
    const request = result.active.find((entry) => entry.key === "todo:req")!;
    expect(node(request, "legacy").state).toBe("ready");
    expect(node(request, "strict-ready")).toMatchObject({ state: "ready", layer: 2, waitsFor: [] });
    expect(node(request, "strict-blocked")).toMatchObject({ state: "queued", layer: 2, after: ["todo:cancelled", "todo:reopened"] });
    expect(request.edges.filter((edge) => edge.to === "todo:strict-blocked")).toEqual([
      { from: "todo:cancelled", to: "todo:strict-blocked", dashed: true },
      { from: "todo:reopened", to: "todo:strict-blocked", dashed: true },
    ]);
    expect(request.edges.find((edge) => edge.to === "todo:strict-ready")).toEqual({ from: "todo:reopened", to: "todo:strict-ready", dashed: false });
    expect(queuedRows(result).find((row) => row.key === "todo:strict-blocked")?.waits.map((wait) => wait.title)).toEqual([
      "Cancelled task", "missing", "Reopened task", "Another request",
    ]);
  });

  it("does not replace working, stopped, finished, or open plan states with prerequisite waiting", () => {
    const strictPiece = { part_of: "req", after: ["missing"], prerequisite_blockers: ["missing"] };
    const todos = [
      todo("req"),
      todo("working", { ...strictPiece, agents: [link("running")] }),
      todo("stopped", { ...strictPiece, agents: [link("idle")] }),
      todo("finished", { ...strictPiece, done_at: at(2) }),
    ];
    const open = plan("open", "open", [task("Unstarted plan task")]);
    const result = graph(todos, [session("running"), session("idle", { state: "idle", status: "idle" })], { open });
    const request = result.active.find((entry) => entry.key === "todo:req")!;
    expect([node(request, "working").state, node(request, "stopped").state, node(request, "finished").state]).toEqual(["working", "stopped", "done"]);
    expect(result.active.find((entry) => entry.plan === open)?.nodes[0]).toMatchObject({ state: "queued", waitsStart: true, waitsFor: [] });
    expect(queuedRows(result)).toEqual([]);
  });
});

describe("where a to-do stands", () => {
  const step = (t: Todo, sessions: Session[] = []) => pieceStep(t, todoStatus(t, makeSessionLookup(sessions)));
  const idle = (sid: string) => session(sid, { state: "idle", status: "idle", updated_at: at(0) });
  const agentOn = (sid: string) => ({ machine_id: "mac", session_id: sid, linked_at: at(0), released_at: null });

  it("puts what waits on the user first: a decision it asks, an Agent that needs them, a supervisor that passed it up, a failed start", () => {
    expect(step(todo("a", { agents: [agentOn("s")] }), [session("s", { state: "needs_you", status: "blocked" })])).toBe("needs_me");
    expect(step(todo("b", { ask: { outcome: "decide", verdict: "Pick one", asked_at: at(2) } }))).toBe("needs_me");
    expect(step(todo("c", { agents: [agentOn("s")], supervision: { supervisor: "conv", state: "needs_user" } }), [idle("s")])).toBe("needs_me");
    expect(step(todo("d", { start_error: "rpc closed", done_at: at(3) }))).toBe("needs_me");
  });

  it("reads 'it says it is done' as waiting to be accepted: a pending review, a reported done, a result to verify, a closed one nobody looked at", () => {
    expect(step(todo("a", { agents: [agentOn("s")], supervision: { supervisor: "conv", state: "pending_review" } }), [idle("s")])).toBe("review");
    expect(step(todo("b", { agents: [agentOn("s")], supervision: { supervisor: "conv", progress: { kind: "done", text: "Done", at: at(4) } } }), [idle("s")])).toBe("review");
    expect(step(todo("c", { ask: { outcome: "done", verdict: "Please verify", asked_at: at(2) } }))).toBe("review");
    expect(step(todo("d", { done_at: at(3), done_by: "auto" }))).toBe("review");
  });

  it("keeps the work going on in the middle, and the closed ones last", () => {
    expect(step(todo("a", { agents: [agentOn("s")] }), [session("s", { state: "working", status: "working" })])).toBe("working");
    expect(step(todo("b"))).toBe("working"); // nobody took it yet
    expect(step(todo("c", { done_at: at(3) }))).toBe("accepted");
    expect(step(todo("d", { done_at: at(3), done_by: "review" }))).toBe("accepted");
    expect(step(todo("e", { done_at: at(3), done_by: "auto", seen_at: at(4) }))).toBe("accepted");
    expect(step(todo("f", { agents: [agentOn("s")], supervision: { supervisor: "conv", state: "accepted", reviews: [{ at: at(2), verdict: "accepted" }] } }), [idle("s")])).toBe("accepted");
  });

  it("takes the owner's words from the brief, else from a request to-do's notes", () => {
    const brief = { id: "b", created_at: at(0), started_by: "owner", goal: "g", request: { text: " It crashes, fix it ", at: at(0) } } as Todo["brief"];
    expect(ownerWords(todo("t", { title: "Fix it", brief, notes: "原话：别的" }))).toBe("It crashes, fix it");
    expect(ownerWords(todo("t", { title: "Fix it" }))).toBe("");
    // the main assistant's lines: the first is the request as first said
    const notes = "用户原话（2026-11-15 14:23）：我今天固态硬盘到了，指导我操作\n用户原话（2026-11-15 14:43）：还有什么都能放过去\n\n硬盘：1TB";
    expect(ownerWords(todo("t", { notes }))).toBe("我今天固态硬盘到了，指导我操作");
    expect(notesWords("parent: x\n原话：「把旧盘清掉」")).toBe("把旧盘清掉");
    expect(notesWords("Your words:\n\n\"Clean up the old disk\"\nmore")).toBe("Clean up the old disk");
    expect(notesWords("Owner's words (Nov 15): ship it")).toBe("ship it");
    expect(notesWords("ユーザーの言葉：進めて")).toBe("進めて");
    // not at a line's start, or another label: no words
    expect(notesWords("see 原话：x\nKeywords: y\nnotes")).toBe("");
  });

  it("heads a request with the sentence it was filed under, the owner's words quoted only when they differ", () => {
    expect(requestHeading("Fix the login", "Go ahead")).toEqual({ title: "Fix the login", own: false, quote: "Go ahead" });
    expect(requestHeading("Fix the login", " Fix  the login")).toEqual({ title: "Fix the login", own: false, quote: "" });
    expect(requestHeading("Fix the login", "")).toEqual({ title: "Fix the login", own: false, quote: "" });
    expect(requestHeading("  ", "Go ahead")).toEqual({ title: "Go ahead", own: true, quote: "" });
  });
});

describe("a piece's Agent tree", () => {
  const dir = (todoId: string, agents: DirectionAgent[]): DirectionTree =>
    ({ directions: [{ id: "main", name: "", tasks: [{ id: todoId, title: todoId, state: "working", agents }] }] }) as unknown as DirectionTree;
  const sub = (id: string, patch: Partial<Subagent> = {}): Subagent => ({ id, session_id: `L@${id}`, description: `sub ${id}`, ...patch });

  it("reads the leader, then its workers (open and closed), what it spawned and its subagents, each under what it hangs from", () => {
    const todos = [
      todo("lead", { agents: [link("L")] }),
      todo("w1", { title: "Worker one", origin: "mac/L", agents: [link("W1")] }),
      todo("w2", { title: "Worker two", supervision: { supervisor: "mac/L" } as Todo["supervision"], agents: [link("W2", { released_at: at(8) })], done_at: at(9) }),
    ];
    const tree = dir("lead", [{ machine_id: "mac", session_id: "L", title: "lead", state: "working", children: [
      { machine_id: "mac", session_id: "S", title: "spawned", state: "working" },
      { machine_id: "mac", session_id: "W1", title: "Worker one", state: "working" },
      { machine_id: "mac", session_id: "L@r1", title: "sub r1", state: "working", children: [{ machine_id: "mac", session_id: "L@fresh", title: "just launched", state: "working" }] },
    ] }]);
    // the list's summary row has no subagents; the complete row has them, the ended one too
    const summary = [session("L"), session("W1", { activity: "Reading the index" }), session("W2", { state: "idle", status: "idle", last_text: "Done: wrote the API\nmore" }), session("S")];
    const full = [session("L", { subagents: [
      sub("e1", { running: false, started_at: at(1), last_text: "Found 3 call sites" }),
      sub("r1", { running: true, started_at: at(2), last_tool: { name: "Grep", summary: "grep foo" } }),
      sub("n1", { running: true, started_at: at(3), parent_id: "r1" }),
    ] })];
    const d: GraphData = { todos, tree, lookup: makeSessionLookup(summary), complete: makeSessionLookup(full) };
    const agents = buildGraph({ todos, plans: {}, data: d }).active[0].nodes[0].agents;
    expect(agents.map((a) => [a.key, a.role, a.depth, a.parent ?? "", a.ended])).toEqual([
      ["mac/L", "lead", 0, "", false],
      ["mac/W1", "worker", 1, "mac/L", false],
      ["mac/W2", "worker", 1, "mac/L", true],
      ["mac/S", "spawned", 1, "mac/L", false],
      ["sub:mac/L/e1", "subagent", 1, "mac/L", true],
      ["sub:mac/L/r1", "subagent", 1, "mac/L", false],
      ["sub:mac/L/n1", "subagent", 2, "sub:mac/L/r1", false],
      // the direction tree's running one the complete row does not have yet
      ["sub:mac/L/fresh", "subagent", 2, "sub:mac/L/r1", false],
    ]);
    const by = (k: string) => agents.find((a) => a.key === k)!;
    expect([by("mac/W1").doing, by("mac/W2").doing, by("sub:mac/L/e1").doing, by("sub:mac/L/r1").doing]).toEqual(["Reading the index", "Done: wrote the API", "Found 3 call sites", "grep foo"]);
    // a subagent opens its own link (the launching chat at its card), never the Agent that launched it; one only the
    // direction tree knows too
    expect([by("sub:mac/L/n1").open, by("sub:mac/L/fresh").open, by("mac/W2").open]).toEqual([
      { mid: "mac", sid: "L@n1" }, { mid: "mac", sid: "L@fresh" }, { mid: "mac", sid: "W2" },
    ]);
    expect(agents.filter((a) => a.role === "subagent").every((a) => a.open.sid !== "L")).toBe(true);
    expect([by("sub:mac/L/e1").state, by("sub:mac/L/r1").state]).toEqual(["", "working"]);
  });

  it("opens a subagent at its own link: the hub's, else <launching session>@<id>, never the launching session", () => {
    expect(subagentLink({ session_id: "L@a1" }, "L", "a1")).toBe("L@a1");
    // a subagent with a session of its own
    expect(subagentLink({ session_id: "child-9" }, "L", "a1")).toBe("child-9");
    // a row that names the launching session (or nothing) as the subagent's: its link is made, not the parent opened
    expect([subagentLink({ session_id: "L" }, "L", "a1"), subagentLink({ session_id: "" }, "L", "a1"), subagentLink(undefined, "L", "a1")]).toEqual(["L@a1", "L@a1", "L@a1"]);
  });

  it("keeps a stopped parent and its running child from the direction tree when no complete row is available", () => {
    const todos = [todo("lead", { agents: [link("L")] })];
    const tree = dir("lead", [{ machine_id: "mac", session_id: "L", title: "lead", state: "working", children: [
      { machine_id: "mac", session_id: "L@a", title: "Explore the API", state: "idle", children: [
        { machine_id: "mac", session_id: "L@b", title: "Check one endpoint", state: "working" },
      ] },
    ] }]);
    const agents = buildGraph({ todos, plans: {}, data: { todos, tree, lookup: makeSessionLookup([session("L")]) } }).active[0].nodes[0].agents;
    expect(agents.map((a) => [a.key, a.role, a.title, a.parent, a.state, a.ended])).toEqual([
      ["mac/L", "lead", "agent L", undefined, "working", false],
      ["sub:mac/L/a", "subagent", "Explore the API", "mac/L", "", true],
      ["sub:mac/L/b", "subagent", "Check one endpoint", "sub:mac/L/a", "working", false],
    ]);
  });

  it("is drawn only when more than one Agent is on the piece, the earlier ones aside", () => {
    const one = graph([todo("t", { agents: [link("A"), link("old", { released_at: at(1) })] })], [session("A")]).active[0].nodes[0];
    expect([one.agents.length, one.tree]).toEqual([2, []]);
    const two = graph([todo("t", { agents: [link("A"), link("B")] })], [session("A"), session("B")]).active[0].nodes[0];
    expect(two.tree.map((r) => [r.kind, r.key, r.depth])).toEqual([["agent", "mac/A", 0], ["agent", "mac/B", 0]]);
  });

  it("folds the earlier ended subagents of one parent into one line, never a running one", () => {
    const ended = Array.from({ length: ENDED_KEPT + 2 }, (_, i) => sub(`e${i}`, { running: false, started_at: at(i) }));
    const todos = [todo("lead", { agents: [link("L")] })];
    const full = [session("L", { subagents: [...ended, sub("run", { running: true, started_at: at(0.5) })] })];
    const n = buildGraph({ todos, plans: {}, data: { todos, tree: null, lookup: makeSessionLookup(full) } }).active[0].nodes[0];
    expect(n.agents).toHaveLength(ENDED_KEPT + 4);
    expect(n.tree.map((r) => (r.kind === "more" ? `more ${r.count}` : r.key))).toEqual([
      "mac/L", "more 2", "sub:mac/L/run", ...ended.slice(2).map((e) => `sub:mac/L/${e.id}`),
    ]);
    expect(agentTree(n.agents.slice(0, 1))).toEqual([]);
  });
});

describe("geometry", () => {
  it("places the words in the first column and every layer in the next, no card on another", () => {
    const todos = [todo("req"), todo("a", { part_of: "req" }), todo("b", { part_of: "req" }), todo("c", { part_of: "req", after: ["a", "b"] })];
    const r = graph(todos).active[0];
    const lay = layoutRequest(r);
    const boxes = [lay.root, ...lay.nodes.values()];
    for (const p of boxes) for (const q of boxes) {
      if (p === q) continue;
      const apart = p.x + CARD_W <= q.x || q.x + CARD_W <= p.x || p.y + CARD_H <= q.y || q.y + CARD_H <= p.y;
      expect(apart, `${p.key} / ${q.key}`).toBe(true);
    }
    expect(lay.width).toBeGreaterThanOrEqual(lay.nodes.get("todo:c")!.x + CARD_W);
    // every edge leaves its source's right edge (the hollow dot) and ends on its target's left edge
    for (const e of lay.edges) {
      const s = e.from === "root" ? lay.root : lay.nodes.get(e.from)!;
      const t = lay.nodes.get(e.to)!;
      expect(e.dot).toEqual([s.x + CARD_W, s.y + CARD_H / 2]);
      expect(e.d.endsWith(`L${t.x} ${t.y + CARD_H / 2}`)).toBe(true);
    }
  });

  it("rounds the corners of a path, never by more than half a leg", () => {
    expect(roundedPath([[0, 0], [10, 0], [10, 4], [20, 4]])).toBe("M0 0 L8 0 Q10 0 10 2 L10 2 Q10 4 12 4 L20 4");
    expect(roundedPath([[0, 0], [0, 0]])).toBe("");
  });
});

describe("geometry with Agent trees and quotes", () => {
  it("makes a row as tall as its tallest card, the tree under it, and keeps every connector on the cards' heads", () => {
    const todos = [
      todo("req", { title: "Ship", brief: { request: { text: "go", at: at(0) } } as Todo["brief"] }),
      todo("a", { part_of: "req", agents: [link("A1"), link("A2")] }),
      todo("b", { part_of: "req" }),
      todo("c", { part_of: "req", after: ["a"] }),
      todo("d", { part_of: "req", after: ["req"] }),
    ];
    const r = graph(todos, [session("A1"), session("A2")]).active[0];
    const a = r.nodes.find((n) => n.key === "todo:a")!;
    expect(a.tree).toHaveLength(2);
    const h = treeHeight(a.tree, true);
    expect(h).toBe(TREE_HEAD_H + 2 * TREE_ROW_H + 6);
    expect(treeHeight(a.tree, false)).toBe(TREE_HEAD_H);
    expect(treeHeight([], true)).toBe(0);
    const lay = layoutRequest(r, (n) => treeHeight(n.tree, true));
    const pa = lay.nodes.get("todo:a")!, pb = lay.nodes.get(r.nodes.find((n) => n.row === 1 && n.layer === 1)!.key)!;
    expect(pa.h).toBe(CARD_H + h);
    // the root quotes the owner: a line taller
    expect(lay.root.h).toBe(rootHeight(r));
    expect(rootHeight(r)).toBeGreaterThan(CARD_H);
    // the row under a's starts below a's tree
    expect(pb.y).toBeGreaterThanOrEqual(pa.y + pa.h);
    for (const e of lay.edges) {
      const s = e.from === "root" ? lay.root : lay.nodes.get(e.from)!;
      expect(e.dot).toEqual([s.x + CARD_W, s.y + CARD_H / 2]);
    }
    // nothing overlaps, trees included
    const boxes = [lay.root, ...lay.nodes.values()];
    for (const p of boxes) for (const q of boxes) {
      if (p === q) continue;
      expect(p.x + CARD_W <= q.x || q.x + CARD_W <= p.x || p.y + p.h <= q.y || q.y + q.h <= p.y, `${p.key} / ${q.key}`).toBe(true);
    }
    expect(lay.height).toBeGreaterThanOrEqual(Math.max(...boxes.map((b) => b.y + b.h)));
  });
});
