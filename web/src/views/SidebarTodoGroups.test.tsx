// @vitest-environment jsdom
// "By to-do" (#6458): the to-dos and the group of agents on no to-do are one kind of row at the top level, each a sticky
// group heading with its chevron, mark, title, count and state; the group's explanation is behind its ⓘ and is what it
// says when open and empty; the rows that fold groups select no text. Agents opened by hand in a terminal are in the
// same group, their rows ending with the Terminal source mark (#6591).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { buildTodoTree } from "../lib/todo-tree";
import { buildV2SidebarTree, consumeBoardSidebarTree } from "../features/todos/board-sidebar-tree";
import type { Todo, TodoBoardProjection } from "../lib/todos";
import type { Machine, Session } from "../lib/types";
import { GroupBranch, TodoTreeView } from "./SidebarTree";
import { Section } from "./SidebarParts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const at = new Date().toISOString();
const machines: Machine[] = [{ id: "m1", name: "Mac", online: true }];
const s = (sid: string, state: Session["state"], cwd: string, patch: Partial<Session> = {}): Session => ({ machine_id: "m1", session_id: sid, title: `agent ${sid}`, cwd, status: state === "working" ? "working" : "idle", state, updated_at: at, ...patch });
const link = (sid: string) => ({ machine_id: "m1", session_id: sid, linked_at: at });
const todos: Todo[] = [
  { id: "ship", title: "Ship 1.3", created_at: at, updated_at: at, agents: [link("a"), link("b")] },
  { id: "ci", title: "Submit for review", created_at: at, updated_at: at, agents: [link("c")] },
];
const sessions = [
  s("a", "working", "/code/weather"),
  s("b", "idle", "/code/weather"),
  s("c", "idle", "/code/weather", { work: { phase: "blocked", on: "external", line: "CI", wait: "CI" } }),
  // on no to-do, started elsewhere and waiting on the user
  s("loose", "needs_you", "/code/report", { pane_id: "botmux:x" }),
  // on no to-do, opened by hand in a terminal
  s("hand", "working", "/code/sandbox", { space_kind: "herdr", pane_id: "w1:p1", started_by_persona: false }),
];
/** Only the agents on to-dos: the group is empty. */
const onTodos = sessions.filter((x) => x.session_id !== "loose" && x.session_id !== "hand");
const stable = { reg: () => () => {}, hover: () => {}, go: () => () => {}, onRowMenu: () => {} };

let host: HTMLDivElement;
let root: Root;
function show(node: React.ReactNode) {
  act(() => root.render(node));
}
const branch = (key: string) => host.querySelector<HTMLElement>(`[data-tree-branch="${key}"]`)!;
const row = (key: string) => branch(key).querySelector<HTMLElement>("[data-tree-branch-row]")!;
const chevron = (key: string) => row(key).querySelector<HTMLButtonElement>("button[aria-expanded]")!;

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.querySelectorAll('[role="tooltip"]').forEach((el) => el.remove());
});

describe("By to-do's rows (#6458)", () => {
  it("draws the to-dos and the groups as one kind of top-level row, with no repository rows", () => {
    const tree = buildTodoTree(sessions, machines, todos, { spaces: [], sessions: [] }, Date.now(), { keepEmpty: true });
    show(<TodoTreeView tree={tree} activeKey="" {...stable} />);
    const keys = [...host.querySelectorAll("[data-tree-branch]")].map((el) => el.getAttribute("data-tree-branch"));
    // Stable title order, not the agents' activity rank: Submit for review, Ship 1.3, then no to-do.
    expect(keys).toEqual(["todo:ci", "todo:ship", "todo:none"]);
    for (const key of keys) {
      // a sticky group heading of the list's top level, never inside another branch
      expect(row(key!).closest("[data-sidebar-group-heading]")).not.toBeNull();
      expect(branch(key!).parentElement?.closest("[data-tree-branch]")).toBeNull();
      expect(row(key!).className).toContain("select-none");
      expect(chevron(key!)).not.toBeNull();
      expect(row(key!).querySelector("svg")).not.toBeNull();
    }
    // the only rows above the agents are these: no folder row names a repository (an agent's own line still may)
    expect([...host.querySelectorAll("[data-tree-branch-row]")].map((el) => el.textContent).join(" ")).not.toMatch(/weather|report/);
    // count, then state: the to-do's in the To-dos page's word, a group's the most urgent of its agents'
    expect(row("todo:ship").textContent).toMatch(/2 个 Agent工作中$/);
    // a waiting one says what it waits on, in the neutral tone: nothing is the owner's (#11090)
    expect(row("todo:ci").textContent).toMatch(/1 个 Agent等外部$/);
    expect(row("todo:ci").querySelector("[data-tree-state]")!.className).toContain("text-ink-2");
    expect(row("todo:none").textContent).toMatch(/^未关联待办.*2 个 Agent需要你$/);
  });

  it("an empty group says 0 and no state", () => {
    show(<TodoTreeView tree={buildTodoTree(onTodos, machines, todos, { spaces: [], sessions: [] }, Date.now(), { keepEmpty: true })} activeKey="" {...stable} />);
    expect(row("todo:none").textContent).toMatch(/^未关联待办.*0 个 Agent$/);
  });

  it("says 完成未看 over a finished agent not looked at yet, as its row and By status do, not 已完成", () => {
    const unseen = [...onTodos, s("done", "unread", "/code/notes"), s("seen", "idle", "/code/notes")];
    show(<TodoTreeView tree={buildTodoTree(unseen, machines, todos, { spaces: [], sessions: [] }, Date.now())} activeKey="" {...stable} />);
    expect(row("todo:none").textContent).toMatch(/^未关联待办.*2 个 Agent完成未看$/);
    // Recent (not running) as the sidebar draws it under By to-do: its agents stopped
    act(() => root.unmount());
    root = createRoot(host);
    show(<GroupBranch branch="recent" name="最近（未运行）" icon="history" n={1} state="done" label="未运行" folded onToggle={() => {}}>{null}</GroupBranch>);
    expect(row("recent").textContent).toMatch(/1 个 Agent未运行$/);
  });

  it("puts the agents opened in a terminal in the same group, each row ending with the Terminal source mark (#6591)", () => {
    show(<TodoTreeView tree={buildTodoTree(sessions, machines, todos, { spaces: [], sessions: [] }, Date.now())} activeKey="" {...stable} />);
    const marks = [...branch("todo:none").querySelectorAll("[data-tree-branch-list] [data-source]")].map((el) => [el.getAttribute("data-source"), el.getAttribute("aria-label")]);
    expect(marks).toEqual([["feishu", "飞书群"], ["terminal", "在标签页中"]]);
  });

  it("folds the group by default, except when it holds an agent that needs you", () => {
    show(<TodoTreeView tree={buildTodoTree(sessions.filter((x) => x.session_id !== "loose"), machines, todos, { spaces: [], sessions: [] }, Date.now())} activeKey="" {...stable} />);
    expect(chevron("todo:none").getAttribute("aria-expanded")).toBe("false");
    act(() => root.unmount());
    root = createRoot(host);
    localStorage.clear();
    const tree = buildTodoTree(sessions, machines, todos, { spaces: [], sessions: [] }, Date.now(), { keepEmpty: true });
    show(<TodoTreeView tree={tree} activeKey="" {...stable} />);
    expect(chevron("todo:none").getAttribute("aria-expanded")).toBe("true");
    // the shared fold hook keeps the choice in this browser
    act(() => chevron("todo:none").click());
    expect(chevron("todo:none").getAttribute("aria-expanded")).toBe("false");
    expect(localStorage.getItem("fold:todo:none")).toBe("1");
  });

  it("keeps the explanation behind the ⓘ, and says it when an empty group is opened", async () => {
    const tree = buildTodoTree(onTodos, machines, todos, { spaces: [], sessions: [] }, Date.now(), { keepEmpty: true });
    const hint = tree.loose[0]!.hint;
    show(<TodoTreeView tree={tree} activeKey="" {...stable} />);
    // folded: nothing under it at all, so nothing for a reader to land on (a folded group mounts no rows)
    expect(branch("todo:none").querySelector("[data-tree-branch-list]")).toBeNull();
    const info = row("todo:none").querySelector<HTMLButtonElement>('button[aria-label="关于「未关联待办」"]')!;
    expect(info.hasAttribute("aria-expanded")).toBe(false);
    // the tip's words load the first time an ⓘ is asked (InfoTipPanel)
    await act(async () => info.click());
    for (let i = 0; i < 50 && !document.querySelector('[role="tooltip"]'); i++) await act(() => new Promise((r) => setTimeout(r, 10)));
    const tip = document.querySelector('[role="tooltip"]')!;
    expect(tip.textContent).toBe(hint);
    expect(info.getAttribute("aria-describedby")).toBe(tip.id);
    act(() => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    expect(document.querySelector('[role="tooltip"]')).toBeNull();
    act(() => chevron("todo:none").click());
    expect(branch("todo:none").querySelector("[data-tree-branch-list]")!.textContent).toBe(hint);
  });

  it("draws the groups as sections beside the sections of By pinned", () => {
    show(
      <GroupBranch branch="todo:none" name="没挂待办的" hint="why" icon="layers" n={0} state="done" folded={false} onToggle={() => {}} section>
        {null}
      </GroupBranch>,
    );
    const heading = host.querySelector("[data-sidebar-group-heading]")!;
    expect(heading.textContent).toMatch(/^没挂待办的.*0 个 Agent$/);
    expect(heading.querySelector('button[aria-label="关于「没挂待办的」"]')).not.toBeNull();
    expect(host.textContent).toContain("why");
  });

  it("renders By-to-do branches directly from server board.sidebar_tree order and folded progress (#15800)", () => {
    const goalTodos: Todo[] = [
      { id: "goal_a", title: "Ship release", created_at: at, updated_at: at, agents: [link("a")] },
      { id: "sub_b", title: "Subtask B", part_of: "goal_a", created_at: at, updated_at: at, agents: [link("b")] },
      { id: "sub_urgent", title: "Blocked child", part_of: "goal_a", created_at: at, updated_at: at, agents: [link("c")] },
    ];
    const goalSessions: Session[] = [
      s("a", "working", "/code/persona"),
      s("b", "working", "/code/persona"),
      s("c", "needs_you", "/code/persona"),
    ];
    const board: TodoBoardProjection = {
      status_groups: [],
      direction_groups: [],
      sidebar_tree: [
        { todo_id: "sub_urgent", section: "needs_you", item_type: "task", folded_count: 0, needs_you_count: 1, active_count: 0 },
        { todo_id: "goal_a", section: "in_progress", item_type: "task", child_ids: ["sub_b"], surfaced_child_ids: ["sub_urgent"], folded_count: 1, needs_you_count: 0, active_count: 2 },
      ],
      counts: { needs_you: 1, in_progress: 2, pending_review: 0, waiting: 0, someday: 0, done: 0, open_total: 3 },
    };
    const tree = consumeBoardSidebarTree(goalSessions, machines, goalTodos, board, { spaces: [], sessions: [] }, Date.now(), { keepEmpty: true });
    show(<TodoTreeView tree={tree} activeKey="" {...stable} />);
    const keys = [...host.querySelectorAll("[data-tree-branch]")].map((el) => el.getAttribute("data-tree-branch"));
    expect(keys).toEqual(["todo:sub_urgent", "todo:goal_a", "todo:none"]);
    expect(row("todo:goal_a").querySelector("[data-goal-progress]")?.getAttribute("data-goal-progress")).toBe("0/2");
  });
});

describe("Section", () => {
  it("selects no text in its heading and shows no explanation under it", () => {
    show(<Section title="Recent" folded={false} onToggle={() => {}}><span>row</span></Section>);
    const heading = host.querySelector("[data-sidebar-group-heading] > div")!;
    expect(heading.className).toContain("select-none");
    expect(host.querySelector("[data-sidebar-group-heading] button")?.getAttribute("title")).toBeNull();
    expect(host.querySelectorAll("p")).toHaveLength(0);
  });
});

describe("By to-do under todos_v2 (#14831)", () => {
  const goalTodos: Todo[] = [
    { id: "goal", title: "Ship v2", created_at: at, updated_at: at },
    { id: "done-sub", title: "Done sub", part_of: "goal", done_at: at, created_at: at, updated_at: at },
    { id: "sub-work", title: "Build feature", part_of: "goal", created_at: at, updated_at: at, agents: [link("a")] },
    { id: "sub-block", title: "Approve migration", part_of: "goal", created_at: at, updated_at: at, agents: [link("b")] },
  ];
  const goalSessions = [
    s("a", "working", "/code/weather"),
    s("b", "needs_you", "/code/weather"),
  ];

  it("folds non-urgent subtasks under their parent goal with compact progress and surfaces needs_you blockers at the top, reverting losslessly when off", () => {
    // todos_v2 = false (default): flat branches, no goal progress badge
    const flatTree = buildTodoTree(goalSessions, machines, goalTodos, { spaces: [], sessions: [] }, Date.now());
    show(<TodoTreeView tree={flatTree} activeKey="" {...stable} />);
    const flatKeys = [...host.querySelectorAll("[data-tree-branch]")].map((el) => el.getAttribute("data-tree-branch"));
    expect(flatKeys).toEqual(["todo:sub-block", "todo:sub-work"]);
    expect(host.querySelector("[data-goal-progress]")).toBeNull();

    // todos_v2 = true: needs_you child surfaces first, non-urgent child folds under parent goal with 1/3 progress
    const v2Tree = buildV2SidebarTree(goalSessions, machines, goalTodos, { spaces: [], sessions: [] }, Date.now(), { board: null, foldChildren: true });
    show(<TodoTreeView tree={v2Tree} activeKey="" {...stable} />);
    const v2Keys = [...host.querySelectorAll("[data-tree-branch]")].map((el) => el.getAttribute("data-tree-branch"));
    expect(v2Keys).toEqual(["todo:sub-block", "todo:goal"]);
    const progress = row("todo:goal").querySelector<HTMLElement>("[data-goal-progress]")!;
    expect(progress).not.toBeNull();
    expect(progress.getAttribute("data-goal-progress")).toBe("1/3");
    expect(progress.className).toContain("text-ink-3");
    expect(progress.className).toContain("tabular-nums");
    expect(chevron("todo:goal").getAttribute("aria-expanded")).toBe("true");
    // parent goal remains interactive (can fold/unfold its folded subtasks' agents)
    act(() => chevron("todo:goal").click());
    expect(chevron("todo:goal").getAttribute("aria-expanded")).toBe("false");

    // turning todos_v2 back off immediately restores the exact flat DOM
    show(<TodoTreeView tree={flatTree} activeKey="" {...stable} />);
    const revertedKeys = [...host.querySelectorAll("[data-tree-branch]")].map((el) => el.getAttribute("data-tree-branch"));
    expect(revertedKeys).toEqual(flatKeys);
    expect(host.querySelector("[data-goal-progress]")).toBeNull();
  });
});
