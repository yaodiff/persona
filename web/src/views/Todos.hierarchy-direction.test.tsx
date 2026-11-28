// @vitest-environment jsdom
// Integration tests for #16939 (Sub-PR 2): effective direction resolution across list rows & TodoDetail,
// unified TodoDetail ownership strip & subtasks on all templates, parent/child hierarchy in Status/Direction/Project
// groupings, discoverable labeled grouping switcher, and strict todos_v2=false zero-regression guard.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo, TodoBoardProjection } from "../lib/todos";
import type { TodoSummary } from "../lib/todo-view-wire";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const apiMock = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => apiMock(path, init),
}));

const { initI18n, t } = await import("../i18n");
const { getState, patchAssistant, reset } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const NOW = Date.parse("2026-11-27T12:00:00.000Z");
const HOUR = 3_600_000;
const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString();
const view = (section: string, reason: string, phase: string, rank = 0): TodoSummary =>
  ({ phase, section, reason, rank }) as TodoSummary;

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

const seed = (patch: Record<string, unknown>) =>
  Object.assign(getState() as unknown as Record<string, unknown>, patch);

const render = async ({
  todos,
  todoBoard = null,
  todosV2 = true,
  leaders = ["dir-platform", "dir-growth"],
  conversations = [
    { machine_id: "m1", session_id: "dir-platform", title: "Platform Direction" },
    { machine_id: "m1", session_id: "dir-growth", title: "Growth Direction" },
  ],
  grouping = "status",
  foldChildren,
  selectedId,
}: {
  todos: Todo[];
  todoBoard?: TodoBoardProjection | null;
  todosV2?: boolean;
  leaders?: string[];
  conversations?: unknown[];
  grouping?: "status" | "folder" | "direction";
  foldChildren?: boolean;
  selectedId?: string;
}) => {
  localStorage.setItem("persona.todos.grouping", grouping);
  seed({
    todos,
    todoBoard,
    todosLoaded: true,
    todosSupported: true,
    todosError: null,
    sessions: {},
    machines: [],
    interactions: [],
    answered: {},
    features: ["todo_view"],
    assistant: {
      machine_id: "m1",
      main: "main",
      conversations,
      leaders,
      todos_v2: todosV2,
      todos_v2_fold_children: foldChildren,
    },
  });
  await act(async () => root.render(<TodosView selectedId={selectedId} />));
  await flush();
};

beforeEach(async () => {
  await initI18n("en");
  reset();
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  apiMock.mockReset();
  apiMock.mockImplementation(async (path: string) => {
    if (path.includes("/kb")) {
      return {
        items: [
          { id: "core", slug: "core", name: "Core Project" },
          { id: "web", slug: "web", name: "Web Project" },
        ],
      };
    }
    return {};
  });
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  localStorage.clear();
  reset();
  vi.useRealTimers();
});

describe("TodosView effective direction, unified detail hierarchy, and grouping hierarchy (#16939)", () => {
  const todos: Todo[] = [
    // Parent goal with server-resolved effective direction (raw direction_id intentionally empty!)
    {
      id: "goal_parent",
      title: "Unify hierarchy UX",
      project: "core",
      effective_direction_id: "dir-platform",
      effective_direction_name: "Platform Stream",
      created_at: iso(8 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 0),
    },
    // Personal child under goal_parent (NO agents, NO supervision, NO origin -> personal template!)
    {
      id: "personal_child",
      title: "Draft rollout checklist",
      audience: "human",
      part_of: "goal_parent",
      project: "core",
      created_at: iso(6 * HOUR),
      updated_at: iso(2 * HOUR),
      view: view("todo", "rest", "todo", 1),
    },
    // Task child in a DIFFERENT project & direction (mismatch hint + cross-group parent hint)
    {
      id: "cross_child",
      title: "Audit growth metrics",
      part_of: "goal_parent",
      project: "web",
      direction_id: "dir-growth",
      agents: [{ machine_id: "m1", session_id: "s-growth", linked_at: iso(2 * HOUR) }],
      created_at: iso(5 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 2),
    },
    // Legacy leader task with raw direction_id empty, resolved from board.direction_groups / supervisor
    {
      id: "leader_legacy",
      title: "Stabilize CI queue",
      supervision: { supervisor: "dir-platform" },
      created_at: iso(4 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 3),
    },
    // Personal standalone with board-projected direction
    {
      id: "personal_dir",
      title: "Review platform notes",
      audience: "human",
      created_at: iso(3 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("todo", "rest", "todo", 2),
    },
  ];

  const board: TodoBoardProjection = {
    status_groups: [
      {
        key: "in_progress",
        total_count: 3,
        visible_count: 3,
        entries: [
          {
            todo_id: "goal_parent",
            folded_child_ids: ["personal_child"],
            folded_count: 1,
          },
          // Intentionally unnested child in status view (simulates fold_children=false or surfaced item)
          { todo_id: "cross_child" },
          { todo_id: "leader_legacy" },
        ],
      },
      {
        key: "other",
        total_count: 1,
        visible_count: 1,
        entries: [{ todo_id: "personal_dir" }],
      },
    ],
    direction_groups: [
      {
        direction_id: "dir-platform",
        label: "Platform Stream",
        counts: { needs_you: 0, in_progress: 3, pending_review: 0, waiting: 0, done: 0, total: 4 },
        todo_ids: ["personal_child", "goal_parent", "leader_legacy", "personal_dir"],
      },
      {
        direction_id: "dir-growth",
        label: "Growth Stream",
        counts: { needs_you: 0, in_progress: 1, pending_review: 0, waiting: 0, done: 0, total: 1 },
        todo_ids: ["cross_child"],
      },
    ],
    sidebar_tree: [],
    counts: {
      needs_you: 0,
      in_progress: 4,
      pending_review: 0,
      waiting: 0,
      someday: 0,
      done: 0,
      open_total: 5,
    },
  };

  it("shows server-resolved effective direction on list rows & TodoDetail even when raw direction_id is empty, and shows honest inherited picker", async () => {
    await render({ todos, todoBoard: board, todosV2: true, selectedId: "leader_legacy" });

    // List rows: goal_parent (effective_direction_id), leader_legacy (board/supervisor), personal_dir (board)
    expect(host.querySelector('[data-todo="goal_parent"] [data-testid="todo-direction-tag"]')?.textContent).toBe("Platform Stream");
    expect(host.querySelector('[data-todo="leader_legacy"] [data-testid="todo-direction-tag"]')?.textContent).toBe("Platform Stream");
    expect(host.querySelector('[data-todo="personal_dir"] [data-testid="todo-direction-tag"]')?.textContent).toBe("Platform Stream");
    // Unnested child in Status view retains parent goal hint
    expect(host.querySelector('[data-todo="cross_child"] [data-testid="todo-parent-goal"]')?.textContent).toContain("Unify hierarchy UX");

    // TodoDetail on leader_legacy shows resolved effective direction with inherited marker
    const detailDir = host.querySelector<HTMLElement>("[data-testid=todo-detail-direction]")!;
    expect(detailDir.textContent).toContain("Platform Stream");
    expect(detailDir.dataset.directionInherited).toBe("true");

    // Opening TodoDirectionPicker shows inherited hint and pre-selects dir-platform
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-change-direction]")!.click());
    await flush();
    expect(host.querySelector("[data-testid=todo-detail-direction-inherited]")?.textContent).toContain("Platform Stream");
    expect(host.querySelector<HTMLSelectElement>("[data-testid=todo-detail-direction-select]")?.value).toBe("dir-platform");
  });

  it("renders unified ownership strip on personal template without duplicate buttons, and renders clickable subtasks with status & direction mismatch", async () => {
    await render({ todos, todoBoard: board, todosV2: true, selectedId: "personal_child" });

    // Personal template: ownership strip renders WITHOUT work_goal template badge or status summary
    const ownership = host.querySelector("[data-testid=todo-detail-ownership]")!;
    expect(ownership).toBeTruthy();
    expect(host.querySelector("[data-testid=todo-detail-template-badge]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-status-summary]")).toBeNull();

    // Direction inherited from parent/board + clickable parent link visible on personal template
    expect(ownership.querySelector("[data-testid=todo-detail-direction]")?.textContent).toContain("Platform Stream");
    const parentLink = ownership.querySelector<HTMLButtonElement>("[data-testid=todo-detail-parent-link]")!;
    expect(parentLink.textContent).toContain("Unify hierarchy UX");

    // Action buttons rendered exactly once inside the strip (no duplicate title-row buttons)
    expect(host.querySelectorAll("[data-testid=todo-detail-change-parent]")).toHaveLength(1);
    expect(host.querySelectorAll("[data-testid=todo-detail-change-direction]")).toHaveLength(1);

    // Clicking parent link opens goal_parent, which shows clickable subtasks with status & direction mismatch hint
    await act(async () => parentLink.click());
    await flush();

    const subtasks = host.querySelector("[data-testid=todo-detail-subtasks]")!;
    expect(subtasks).toBeTruthy();
    const items = [...subtasks.querySelectorAll<HTMLElement>("[data-testid=todo-detail-subtask-item]")];
    expect(items.map((el) => el.dataset.subtaskId)).toEqual(["personal_child", "cross_child"]);

    // Same-direction child omits mismatch badge; cross-direction child shows Growth badge; both show status label
    expect(items[0].querySelector("[data-testid=todo-detail-subtask-direction]")).toBeNull();
    expect(items[0].querySelector("[data-testid=todo-detail-subtask-status]")?.textContent?.trim()).toBeTruthy();
    expect(items[1].querySelector("[data-testid=todo-detail-subtask-direction]")?.textContent).toContain("Growth");
    expect(items[1].querySelector("[data-testid=todo-detail-subtask-status]")?.textContent?.trim()).toBeTruthy();

    // Clicking a subtask navigates to that child
    await act(async () => items[1].click());
    await flush();
    expect(host.querySelector<HTMLInputElement>("[data-testid=todos-detail-column] input[type=text]")?.value).toBe("Audit growth metrics");
  });

  it("shows labeled grouping switcher and nests parent/child rows (expanded by default with collapse toggle + cross-section parent hints) in Direction and Project views", async () => {
    await render({ todos, todoBoard: board, todosV2: true, grouping: "status", selectedId: "goal_parent" });

    // Grouping switcher shows visible active label ("By status")
    const trigger = host.querySelector<HTMLButtonElement>("[data-testid=todos-grouping-trigger]")!;
    expect(trigger).toBeTruthy();
    expect(host.querySelector("[data-testid=todos-grouping-label]")?.textContent).toBe(t("todos.grouping_v2_status"));

    // Open grouping menu and switch to "By direction"
    await act(async () => trigger.click());
    await flush();
    const menuItems = [...document.querySelectorAll<HTMLElement>("[role=menuitemradio]")];
    const byDirItem = menuItems.find((el) => el.textContent?.includes(t("todos.grouping_v2_direction")));
    expect(byDirItem).toBeTruthy();
    await act(async () => byDirItem!.click());
    await flush();

    expect(host.querySelector("[data-testid=todos-grouping-label]")?.textContent).toBe(t("todos.grouping_v2_direction"));

    // Direction grouping: Platform nests personal_child under goal_parent (expanded by default)
    const platformGroup = host.querySelector<HTMLElement>('[data-testid="todo-group"][data-group="direction:dir-platform"]')!;
    const platformChildren = platformGroup.querySelector<HTMLElement>('[data-testid="todo-goal-children"][data-parent="goal_parent"]')!;
    expect(platformChildren).toBeTruthy();
    expect([...platformChildren.querySelectorAll<HTMLElement>("[data-todo]")].map((el) => el.dataset.todo)).toEqual([
      "personal_child",
    ]);

    // Collapse toggle collapses and re-expands children inside Direction view
    const dirToggle = platformGroup.querySelector<HTMLButtonElement>('[data-todo="goal_parent"] [data-testid="todo-goal-toggle"]')!;
    expect(dirToggle.getAttribute("aria-expanded")).toBe("true");
    await act(async () => dirToggle.click());
    await flush();
    expect(dirToggle.getAttribute("aria-expanded")).toBe("false");
    expect(platformGroup.querySelector('[data-testid="todo-goal-children"][data-parent="goal_parent"]')).toBeNull();
    await act(async () => dirToggle.click());
    await flush();
    expect(platformGroup.querySelector('[data-testid="todo-goal-children"][data-parent="goal_parent"]')).toBeTruthy();

    // Growth direction holds cross_child whose parent is in Platform -> shows parentGoalTitle hint!
    const growthGroup = host.querySelector<HTMLElement>('[data-testid="todo-group"][data-group="direction:dir-growth"]')!;
    expect(growthGroup.querySelector('[data-todo="cross_child"] [data-testid="todo-parent-goal"]')?.textContent).toContain("Unify hierarchy UX");

    // Switch to "By project" via grouping menu
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=todos-grouping-trigger]")!.click());
    await flush();
    const byProjItem = [...document.querySelectorAll<HTMLElement>("[role=menuitemradio]")].find((el) =>
      el.textContent?.includes(t("todos.grouping_v2_folder")),
    );
    expect(byProjItem).toBeTruthy();
    await act(async () => byProjItem!.click());
    await flush();

    expect(host.querySelector("[data-testid=todos-grouping-label]")?.textContent).toBe(t("todos.grouping_v2_folder"));

    // Core project nests personal_child under goal_parent (expanded by default)
    const coreGroup = host.querySelector<HTMLElement>('[data-testid="todo-group"][data-group="todos-kb:core"]')!;
    const coreChildren = coreGroup.querySelector<HTMLElement>('[data-testid="todo-goal-children"][data-parent="goal_parent"]')!;
    expect(coreChildren).toBeTruthy();
    expect([...coreChildren.querySelectorAll<HTMLElement>("[data-todo]")].map((el) => el.dataset.todo)).toEqual([
      "personal_child",
    ]);

    // Web project holds cross_child whose parent is in Core -> shows parentGoalTitle hint!
    const webGroup = host.querySelector<HTMLElement>('[data-testid="todo-group"][data-group="todos-kb:web"]')!;
    expect(webGroup.querySelector('[data-todo="cross_child"] [data-testid="todo-parent-goal"]')?.textContent).toContain("Unify hierarchy UX");
  });

  it("renders zero new UI and keeps legacy behavior 100% unchanged when todos_v2 is false", async () => {
    await render({ todos, todoBoard: board, todosV2: false, grouping: "folder", selectedId: "personal_child" });

    expect(host.querySelector("[data-testid=todos-grouping-trigger]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-direction-tag]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-parent-goal]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-goal-toggle]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-goal-children]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-ownership]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-subtasks]")).toBeNull();

    // Toggling todos_v2 on then off reverts cleanly
    await act(async () => patchAssistant({ todos_v2: true }));
    await flush();
    expect(host.querySelector("[data-testid=todo-detail-ownership]")).toBeTruthy();
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(host.querySelector("[data-testid=todo-detail-ownership]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-goal-children]")).toBeNull();
  });

  it.each(["status", "folder", "direction"] as const)("keeps children as separate rows in %s when folding is off", async (grouping) => {
    await render({ todos, todoBoard: grouping === "direction" ? board : null, grouping, foldChildren: false });
    const children = host.querySelector('[data-todo="personal_child"]');
    expect(children).not.toBeNull();
    expect(children?.closest("[data-testid=todo-goal-children]")).toBeNull();
    expect(host.querySelector('[data-todo="goal_parent"] [data-testid=todo-goal-toggle]')).toBeNull();
    expect(children?.querySelector("[data-testid=todo-parent-goal]")?.textContent).toContain("Unify hierarchy UX");
  });
});
