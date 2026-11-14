import { describe, expect, it } from "vitest";
import { agentGroupLabel, agentTreeState, buildTodoTree, groupTodosByProject, nestAgents, projectOf, STALE_MS, todoTreeState, treeStateLabel, worstTreeState } from "./todo-tree";
import { makeSessionLookup, todoStatus, todoStatusLabel, type Todo } from "./todos";
import { matchesFilter } from "./agent-filter";
import { sessionSource } from "./agent-groups";
import type { Machine, Session } from "./types";

const NOW = Date.parse("2026-11-05T12:00:00Z");
const fresh = new Date(NOW - 60_000).toISOString();
const old = new Date(NOW - STALE_MS - 60_000).toISOString();
const machines: Machine[] = [{ id: "m1", name: "Mac", online: true }];
const s = (sid: string, state: Session["state"], cwd: string, patch: Partial<Session> = {}): Session => ({
  machine_id: "m1", session_id: sid, title: sid, cwd, status: state === "working" ? "working" : "idle", state, updated_at: fresh, ...patch,
});
const todo = (id: string, agents: Todo["agents"], patch: Partial<Todo> = {}): Todo => ({ id, title: id, created_at: fresh, updated_at: fresh, agents, ...patch });
const link = (sid: string, patch: object = {}) => ({ machine_id: "m1", session_id: sid, linked_at: fresh, ...patch });
const noMutes = { spaces: [], sessions: [] };

describe("By to-do: to-dos at the top level, no repository rows (#2526, #6458)", () => {
  it("lists the to-dos in sortTodos's order with no repository over them, and the agents on no to-do directly under their group", () => {
    const all = [s("a", "working", "/code/persona"), s("b", "unread", "/code/persona/.worktrees/x"), s("c", "needs_you", "/code/botmux"), s("loose", "idle", "/code/persona"), s("loose2", "idle", "/code/other")];
    const todos = [todo("t1", [link("a")]), todo("t2", [link("b")]), todo("t3", [link("c")])];
    const tree = buildTodoTree(all, machines, todos, noMutes, NOW);
    expect(tree.projects).toEqual([]);
    // the task board's order: Needs you (t3), Working (t1), Finished (t2), #4896
    expect(tree.todos.map((td) => [td.todoId, td.state])).toEqual([["t3", "needs_you"], ["t1", "working"], ["t2", "done"]]);
    expect(tree.loose.map((g) => [g.key, g.agents.map((a) => a.session.session_id)])).toEqual([["todo:none", ["loose", "loose2"]]]);
  });

  it("派生的 Agent 挂在派它的 Agent 下，状态向上汇总", () => {
    const all = [s("parent", "idle", "/code/p"), s("child", "needs_you", "/code/p"), s("grand", "working", "/code/p")];
    const td = todo("t", [link("parent"), link("child", { source: "spawned", via: "m1/parent" }), link("grand", { source: "spawned", via: "m1/child" })]);
    const roots = nestAgents(all, td, NOW);
    expect(roots).toHaveLength(1);
    expect(roots[0]!.state).toBe("needs_you");
    expect(roots[0]!.children[0]!.session.session_id).toBe("child");
    expect(roots[0]!.children[0]!.children[0]!.session.session_id).toBe("grand");
  });

  it("父 Agent 不在列表里或成环时留在顶层", () => {
    const all = [s("x", "idle", "/p"), s("y", "idle", "/p")];
    const td = todo("t", [link("x", { source: "spawned", via: "m1/y" }), link("y", { source: "spawned", via: "m1/x" })]);
    expect(nestAgents(all, td, NOW)).toHaveLength(2);
    expect(nestAgents([s("z", "idle", "/p")], todo("t", [link("z", { source: "spawned", via: "m1/gone" })]), NOW)).toHaveLength(1);
  });

  it("做完且超过 1 天没动的待办默认折叠，在做的不折叠", () => {
    const all = [s("quiet", "idle", "/code/a", { updated_at: old }), s("busy", "working", "/code/b", { updated_at: old })];
    const tree = buildTodoTree(all, machines, [todo("t1", [link("quiet")]), todo("t2", [link("busy")])], noMutes, NOW);
    const byId = Object.fromEntries(tree.todos.map((td) => [td.todoId, td]));
    expect(byId.t1!.folded).toBe(true);
    expect(byId.t1!.state).toBe("done");
    expect(byId.t2!.folded).toBe(false);
  });

  it("在终端里开的和别处开的同在「没挂待办的」，终端的那行仍有来源标记 (#6591)", () => {
    const hand = s("hand", "idle", "/code/p", { space_kind: "herdr", pane_id: "w1:p1" });
    const all = [s("a", "working", "/code/p"), hand, s("bot", "idle", "/code/q", { pane_id: "botmux:x" })];
    const tree = buildTodoTree(all, machines, [todo("t1", [link("a")])], noMutes, NOW);
    expect(tree.loose.map((g) => [g.key, g.sessions.map((x) => x.session_id).sort()])).toEqual([["todo:none", ["bot", "hand"]]]);
    // what the group holds, for its ⓘ and its empty state (#3057, #6458); no engine name (DESIGN_SYSTEM 5.6)
    expect(tree.loose[0].hint).toContain("没挂在任何待办上");
    expect(tree.loose[0].hint).toContain("终端");
    expect(tree.loose[0].hint).not.toMatch(/herdr/i);
    // where it came from stays on the row: the Terminal source mark
    expect(sessionSource(hand)).toBe("terminal");
  });

  it("the group starts folded below the to-dos, open when one of its agents needs you or no to-do is above it", () => {
    const all = [s("a", "working", "/code/p"), s("hand", "working", "/code/p", { space_kind: "herdr" }), s("bot", "idle", "/code/q", { pane_id: "botmux:x" })];
    const below = buildTodoTree(all, machines, [todo("t1", [link("a")])], noMutes, NOW);
    expect(below.loose.map((g) => [g.key, g.state, g.folded])).toEqual([["todo:none", "working", true]]);
    const asks = buildTodoTree([...all, s("ask", "needs_you", "/code/q")], machines, [todo("t1", [link("a")])], noMutes, NOW);
    expect(asks.loose.map((g) => [g.state, g.folded])).toEqual([["needs_you", false]]);
    const alone = buildTodoTree(all, machines, [], noMutes, NOW);
    expect(alone.loose.map((g) => g.folded)).toEqual([false]);
  });

  it("keepEmpty lists the group while empty, saying what would be there", () => {
    const all = [s("a", "working", "/code/p")];
    expect(buildTodoTree(all, machines, [todo("t1", [link("a")])], noMutes, NOW).loose).toEqual([]);
    const tree = buildTodoTree(all, machines, [todo("t1", [link("a")])], noMutes, NOW, { keepEmpty: true });
    expect(tree.loose.map((g) => [g.key, g.name, g.agents.length, g.folded])).toEqual([["todo:none", "没挂待办的", 0, true]]);
    expect(tree.loose[0].hint.length).toBeGreaterThan(0);
  });

  it("a to-do says the To-dos page's word for its state, ranked as the tree ranks it (#6458)", () => {
    // its agents ended their turns and one waits on CI: the To-dos page's Waiting, never Done
    const all = [s("ci", "idle", "/code/p", { work: { phase: "blocked", on: "external", line: "CI" } }), s("seen", "idle", "/code/q"), s("new", "unread", "/code/r")];
    const tree = buildTodoTree(all, machines, [todo("wait", [link("ci")]), todo("follow", [link("seen")]), todo("fresh", [link("new")])], noMutes, NOW);
    const byId = Object.fromEntries(tree.todos.map((td) => [td.todoId, td]));
    const lookup = makeSessionLookup(all);
    for (const id of ["wait", "follow", "fresh"]) {
      const td = todo(id, [link({ wait: "ci", follow: "seen", fresh: "new" }[id]!)]);
      expect(byId[id]!.statusLabel).toBe(todoStatusLabel(td, todoStatus(td, lookup)));
    }
    expect([byId.wait!.statusLabel, byId.follow!.statusLabel, byId.fresh!.statusLabel]).toEqual(["等待中", "待跟进", "完成未看"]);
    expect(byId.wait!.state).toBe("bg_waiting");
    expect(todoTreeState("waiting")).toBe("bg_waiting");
  });

  it("未归属里有等你（含中断）时展开", () => {
    const all = [s("a", "working", "/code/p"), s("cut", "idle", "/code/p", { turn_error: "overloaded" })];
    const tree = buildTodoTree(all, machines, [todo("t1", [link("a")])], noMutes, NOW);
    expect(tree.loose.at(-1)?.state).toBe("needs_you");
    expect(tree.loose.at(-1)?.folded).toBe(false);
  });

  it("projectOf 取多数仓库", () => {
    expect(projectOf(["/r/a", "/r/b", "/r/b/.claude/worktrees/x"])).toBe("b");
    expect(projectOf([])).toBe("");
  });

  it("待办页按项目分段，没 Agent 的放最后", () => {
    const all = [s("a", "working", "/code/persona")];
    const groups = groupTodosByProject([todo("t1", [link("a")]), todo("t2", [])], makeSessionLookup(all), NOW);
    expect(groups.map((g) => [g.name, g.state, g.todos.map((t) => t.id)])).toEqual([["persona", "working", ["t1"]], ["未归类", "done", ["t2"]]]);
  });

  it("a folder holding a to-do the page has in Needs you says so, a failed start the hub closed too (audit item 13)", () => {
    const old = new Date(NOW - 3 * STALE_MS).toISOString();
    const failedStart = todo("f1", [], { done_at: old, updated_at: old, start_error: "machine offline", origin: "conv" });
    // by its agents alone it reads Done, and a quiet folder of it folds away
    expect(groupTodosByProject([failedStart], makeSessionLookup([]), NOW).map((g) => [g.state, g.folded])).toEqual([["done", true]]);
    const waiting = groupTodosByProject([failedStart, todo("t2", [])], makeSessionLookup([]), NOW, undefined, (td) => td.id === "f1");
    expect(waiting.map((g) => [g.state, g.folded, g.todos.map((t) => t.id)])).toEqual([["needs_you", false, ["f1", "t2"]]]);
  });

  it("待办页先按待办所属的知识库项目分段，再按仓库，都没有的最后 (#2581)", () => {
    const all = [s("a", "working", "/code/persona"), s("b", "needs_you", "/code/persona")];
    const inArea = (id: string, project: string, links: ReturnType<typeof link>[] = []) => ({ ...todo(id, links), project });
    const names = (slug: string) => (slug === "community" ? "社群运营" : slug);
    const groups = groupTodosByProject(
      [todo("t1", [link("a")]), inArea("t2", "community"), todo("t3", []), inArea("t4", "community", [link("b")]), inArea("t5", "gone")],
      makeSessionLookup(all),
      NOW,
      names,
    );
    // an area's to-dos stay together whatever their agents' repository; its order kept; a project not in the list shows its slug
    expect(groups.map((g) => [g.key, g.name, g.todos.map((t) => t.id)])).toEqual([
      ["todos-kb:community", "社群运营", ["t2", "t4"]],
      ["todos-kb:gone", "gone", ["t5"]],
      ["todos-project:persona", "persona", ["t1"]],
      ["todos-project:\u0000", "未归类", ["t3"]],
    ]);
    expect(groups[0].state).toBe("needs_you");
    // an area with nothing at work stays open however long it was quiet; a repository's folds as before
    const old = { updated_at: new Date(NOW - 3 * STALE_MS).toISOString() };
    const quiet = groupTodosByProject([todo("q1", [], { ...old, project: "community" }), todo("q2", [link("gone")], old)], makeSessionLookup([]), NOW, names);
    expect(quiet.map((g) => [g.name, g.state, g.folded, g.area ?? false])).toEqual([
      ["社群运营", "done", false, true],
      ["未归类", "done", true, false],
    ]);
  });
});

describe("a group's word says what its rows say, never Done over agents that are not", () => {
  const label = (all: Session[]) => agentGroupLabel(all, worstTreeState(all.map(agentTreeState)));
  it("Not on a to-do holding a finished agent not looked at yet says 完成未看, as By status and the quick filter do", () => {
    const tree = buildTodoTree([s("seen", "idle", "/code/a"), s("new", "unread", "/code/b")], machines, [], noMutes, NOW);
    expect(tree.loose.map((g) => [g.state, g.label])).toEqual([["done", "完成未看"]]);
    expect(label([s("seen", "idle", "/code/a"), s("new", "unread", "/code/b")])).toBe("完成未看");
  });
  it("Recent (not running) of stopped agents says 未运行", () => {
    expect(label([s("gone", "offline", "/code/a"), s("gone2", "offline", "/code/b")])).toBe("未运行");
  });
  it("keeps the state's own word otherwise: Done for seen agents, Needs you / Working above it", () => {
    expect(label([s("seen", "idle", "/code/a"), s("gone", "offline", "/code/b")])).toBeUndefined();
    expect(label([s("asks", "needs_you", "/code/a"), s("new", "unread", "/code/b")])).toBeUndefined();
    expect(label([s("runs", "working", "/code/a"), s("gone", "offline", "/code/b")])).toBeUndefined();
    expect(label([])).toBeUndefined();
    const seen = buildTodoTree([s("seen", "idle", "/code/a")], machines, [], noMutes, NOW);
    expect(seen.loose[0]).not.toHaveProperty("label");
  });
});

describe("来源：飞书群 / 终端 / 桌面端 (#2526)", () => {
  it("botmux 是飞书群，终端引擎的窗格是终端，桌面 App 自己的对话是桌面端，其余没有", () => {
    expect(sessionSource(s("b", "idle", "/", { pane_id: "botmux:abc" }))).toBe("feishu");
    expect(sessionSource(s("h", "idle", "/", { pane_id: "w1:p1" }))).toBe("terminal");
    expect(sessionSource(s("c", "idle", "/", { pane_id: "herdr-core:7" }))).toBe("terminal");
    expect(sessionSource(s("o", "idle", "/", { pane_id: "orca:1" }))).toBe("terminal");
    expect(sessionSource(s("t", "idle", "/", { pane_id: "tern:3", host: "tern", space_kind: "tern" }))).toBe("terminal");
    expect(sessionSource(s("x", "idle", "/", { space_kind: "codex_app", source_kind: "codex_history" }))).toBe("desktop");
    expect(sessionSource(s("p", "idle", "/", { host: "paseo", source_kind: "runtime", source_instance_id: "paseo" }))).toBe("desktop");
    expect(sessionSource(s("cy", "idle", "/", { host: "cindy", source_kind: "runtime", source_instance_id: "cindy" }))).toBe("desktop");
    expect(sessionSource(s("q", "idle", "/", { source_kind: "runtime", source_instance_id: "orca", space_kind: "orca" }))).toBe("desktop");
    expect(sessionSource(s("d", "idle", "/", { source_kind: "runtime", source_instance_id: "dsh" }))).toBeUndefined();
    expect(sessionSource(s("n", "idle", "/"))).toBeUndefined();
    expect(matchesFilter(s("b", "idle", "/", { pane_id: "botmux:abc" }), "feishu", noMutes)).toBe(true);
    expect(matchesFilter(s("h", "idle", "/", { pane_id: "w1:p1" }), "feishu", noMutes)).toBe(false);
    expect(matchesFilter(s("x", "idle", "/", { space_kind: "codex_app" }), "terminal", noMutes)).toBe(false);
  });
});

describe("a usage limit rolls up as Waiting, not Working", () => {
  const reset = new Date(Date.now() + 3 * 3600_000).toISOString();
  const limited = s("lim", "idle", "/code/weather", { turn_error: "You've hit your usage limit", turn_error_kind: "usage_limit", turn_error_resets_at: reset, turn_error_resumes: true });

  it("is Waiting for the agent and the todo", () => {
    expect(agentTreeState(limited)).toBe("waiting");
    expect(todoTreeState("usage_limit", [limited])).toBe("waiting");
    expect(treeStateLabel("waiting")).toBe("待唤醒");
    const tree = buildTodoTree([limited], machines, [todo("t", [link("lim")])], noMutes, NOW);
    expect(tree.todos.map((td) => [td.todoId, td.state])).toEqual([["t", "waiting"]]);
  });

  it("does not hide a todo that is actually working", () => {
    const busy = s("busy", "working", "/code/weather");
    const tree = buildTodoTree([limited, busy], machines, [todo("t-lim", [link("lim")]), todo("t-busy", [link("busy")])], noMutes, NOW);
    expect(tree.todos.map((td) => td.state).sort()).toEqual(["waiting", "working"]);
  });
});

describe("waiting in the background in the tree (#6045)", () => {
  const sess = (id: string, patch: Partial<Session>): Session => ({ machine_id: "m", session_id: id, state: "working", status: "done", ...patch }) as Session;
  it("ranks between Working and Done, for an agent and for a todo whose agents only wait", () => {
    const waiting = sess("w", { background_waiting: true });
    const busy = sess("b", {});
    expect(agentTreeState(waiting)).toBe("bg_waiting");
    expect(agentTreeState(sess("s", { background_waiting: true, starting: true }))).toBe("working");
    expect(todoTreeState("working", [waiting])).toBe("bg_waiting");
    expect(todoTreeState("working", [waiting, busy])).toBe("working");
    expect(todoTreeState("working")).toBe("working");
    expect(todoTreeState("needs_you", [waiting])).toBe("needs_you");
  });
});
