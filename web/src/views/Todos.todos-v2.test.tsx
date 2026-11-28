// @vitest-environment jsdom
// Unified todos_v2 Labs toggle (#14642): lightweight All / To-dos / Tasks filter and hiding empty Uncategorized
// placeholders strictly gated behind the selected assistant's `todos_v2 === true`, with instant zero-migration rollback.
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

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

const { getState, patchAssistant, reset } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { t } = await import("../i18n");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const NOW = Date.parse("2026-11-24T12:00:00.000Z");
const HOUR = 3_600_000;
const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString();

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);

// The view as the wire carries it: raw strings, which may be a newer hub's values the closed types do not know.
const view = (section: string, reason: string, phase: string, rank = 0): TodoSummary => ({ phase, section, reason, rank }) as TodoSummary;

const sampleTodos: Todo[] = [
  // Personal to-do without project
  {
    id: "personal_plain",
    title: "Buy coffee beans",
    created_at: iso(5 * HOUR),
    updated_at: iso(4 * HOUR),
    view: view("todo", "rest", "todo", 0),
  },
  // Personal to-do with explicit audience="human" and assigned project
  {
    id: "personal_proj",
    title: "Plan Q4 offsite",
    project: "ops",
    audience: "human",
    created_at: iso(4 * HOUR),
    updated_at: iso(3 * HOUR),
    view: view("todo", "rest", "todo", 1),
  },
  // Agent-backed task with assigned project
  {
    id: "task_agent",
    title: "Fix login timeout",
    project: "persona",
    agents: [{ machine_id: "m1", session_id: "s1", linked_at: iso(2 * HOUR) }],
    created_at: iso(3 * HOUR),
    updated_at: iso(2 * HOUR),
    view: view("working", "agent_running", "working", 0),
  },
  // Supervised task without project
  {
    id: "task_supervised",
    title: "Audit release checklist",
    origin: "conv-main",
    supervision: { supervisor: "main", state: "needs_user" },
    created_at: iso(2 * HOUR),
    updated_at: iso(1 * HOUR),
    view: view("needs_you", "owner_decides", "needs_you", 0),
  },
  // Explicit autonomous task, done
  {
    id: "task_done",
    title: "Run nightly benchmark",
    audience: "autonomous",
    created_at: iso(6 * HOUR),
    updated_at: iso(1 * HOUR),
    done_at: iso(1 * HOUR),
    view: view("done", "closed", "done", 0),
  },
];

const kbProjects = [
  { id: "persona", slug: "persona", name: "Persona Core" },
  { id: "ops", slug: "ops", name: "Operations" },
];

beforeEach(() => {
  reset();
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  api.mockReset();
  api.mockImplementation(async (path: string) => {
    if (path.includes("/kb")) return { items: kbProjects };
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

const render = async ({
  todos = sampleTodos,
  sharedBlockers = [],
  todoBoard = null,
  sessions = {},
  todosV2,
  assistantManager,
  leaders,
  conversations = [],
  grouping = "status",
  selectedId,
}: {
  todos?: Todo[];
  sharedBlockers?: unknown[];
  todoBoard?: TodoBoardProjection | null;
  sessions?: Record<string, unknown[]>;
  todosV2?: boolean;
  assistantManager?: string;
  leaders?: string[];
  conversations?: unknown[];
  grouping?: "status" | "folder" | "direction";
  selectedId?: string;
} = {}) => {
  localStorage.setItem("persona.todos.grouping", grouping);
  seed({
    todos,
    sharedBlockers,
    todoBoard,
    todosLoaded: true,
    sessions,
    interactions: [],
    answered: {},
    features: ["todo_view"],
    assistant:
      todosV2 === undefined && assistantManager === undefined && leaders === undefined && conversations.length === 0
        ? { machine_id: "m1", main: "main", conversations: [] }
        : {
            machine_id: "m1",
            main: "main",
            conversations,
            ...(leaders !== undefined ? { leaders } : {}),
            ...(assistantManager !== undefined ? { manager: assistantManager } : {}),
            ...(todosV2 !== undefined ? { todos_v2: todosV2 } : {}),
          },
  });
  await act(async () => root.render(<TodosView selectedId={selectedId} />));
  await flush();
};

const summary = () => host.querySelector("[data-testid=todos-summary]")?.textContent ?? "";
const kindFilterEl = () => host.querySelector<HTMLElement>("[data-testid=todos-kind-filter]");
const visibleTodoIds = () => [...host.querySelectorAll<HTMLElement>("[data-todo]")].map((el) => el.dataset.todo);
const clickKind = async (label: string) => {
  const btn = [...(kindFilterEl()?.querySelectorAll<HTMLButtonElement>("button[role=radio]") ?? [])].find((b) => b.textContent === label);
  expect(btn, `expected kind filter option "${label}"`).toBeTruthy();
  await act(async () => btn!.click());
  await flush();
};

describe("TodosView under todos_v2 Labs toggle (#14642)", () => {
  it("renders identically to legacy when todos_v2 is false or omitted by an older Hub", async () => {
    await render({ todosV2: undefined, selectedId: "personal_plain" });
    expect(kindFilterEl()).toBeNull();
    expect(host.querySelector("[data-testid=todo-project-tag]")).toBeNull();
    const legacyHtml = host.querySelector("[data-testid=todos-list-column]")?.innerHTML;
    const legacySummary = summary();
    const legacySelectOption = host.querySelector<HTMLOptionElement>("[data-testid=todo-plan-project] option[value='']")?.textContent;
    expect(legacySelectOption).toBe(t("todos.project_none"));

    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todosV2: false, selectedId: "personal_plain" });
    expect(kindFilterEl()).toBeNull();
    expect(host.querySelector("[data-testid=todo-project-tag]")).toBeNull();
    expect(host.querySelector("[data-testid=todos-list-column]")?.innerHTML).toBe(legacyHtml);
    expect(summary()).toBe(legacySummary);
  });

  it("shows All / To-dos / Tasks filter under todos_v2=true with consistent counts before view placement", async () => {
    await render({ todosV2: true });
    expect(kindFilterEl()).toBeTruthy();
    // Under todos_v2 (#14830), redundant 5-part summary is replaced by concise needs-you pill; Done starts folded.
    expect(host.querySelector("[data-testid=todos-summary]")).toBeNull();
    expect(host.querySelector("[data-testid=todos-header-attention]")?.textContent).toContain(t("todos.v2_header_needs_you", { count: 1 }));
    expect(visibleTodoIds()).toEqual(["task_supervised", "task_agent", "personal_plain", "personal_proj"]);
    expect(host.querySelector('[data-testid="todo-section"][data-section="done"]')?.getAttribute("data-todos")).toBe("1");

    // Filter to personal To-dos only (no needs_you -> attention pill hides)
    await clickKind(t("todos.kind_filter_todos"));
    expect(visibleTodoIds()).toEqual(["personal_plain", "personal_proj"]);
    expect(host.querySelector("[data-testid=todos-header-attention]")).toBeNull();

    // Filter to Agent / supervised Tasks only (1 needs_you -> attention pill returns; Done stays folded with count 1)
    await clickKind(t("todos.kind_filter_tasks"));
    expect(visibleTodoIds()).toEqual(["task_supervised", "task_agent"]);
    expect(host.querySelector("[data-testid=todos-header-attention]")?.textContent).toContain(t("todos.v2_header_needs_you", { count: 1 }));
    expect(host.querySelector('[data-testid="todo-section"][data-section="done"]')?.getAttribute("data-todos")).toBe("1");

    // Back to All
    await clickKind(t("todos.kind_filter_all"));
    expect(visibleTodoIds()).toEqual(["task_supervised", "task_agent", "personal_plain", "personal_proj"]);
    expect(host.querySelector("[data-testid=todos-header-attention]")?.textContent).toContain(t("todos.v2_header_needs_you", { count: 1 }));
  });

  it("shows assigned project tags while omitting empty Uncategorized placeholders under todos_v2=true", async () => {
    await render({ todosV2: true, selectedId: "personal_plain" });
    // Assigned projects render their tag
    expect(host.querySelector('[data-todo="personal_proj"] [data-testid="todo-project-tag"]')?.textContent).toBe("Operations");
    expect(host.querySelector('[data-todo="task_agent"] [data-testid="todo-project-tag"]')?.textContent).toBe("Persona Core");
    // Unassigned rows have no project tag and no Uncategorized / None placeholder
    expect(host.querySelector('[data-todo="personal_plain"] [data-testid="todo-project-tag"]')).toBeNull();
    expect(host.querySelector('[data-todo="task_supervised"] [data-testid="todo-project-tag"]')).toBeNull();
    expect(host.querySelector('[data-todo="personal_plain"]')?.textContent).not.toContain(t("todos.project_none"));
    expect(host.querySelector('[data-todo="personal_plain"]')?.textContent).not.toContain(t("lib-todo-tree.unsorted"));
    // Detail project select keeps a visible localized None option label (#17696)
    const emptyOption = host.querySelector<HTMLOptionElement>("[data-testid=todo-plan-project] option[value='']");
    expect(emptyOption?.textContent).toBe(t("todos.project_none"));

    // Folder grouping: named project headers remain, while Unsorted header is omitted
    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todosV2: true, grouping: "folder" });
    const groups = [...host.querySelectorAll<HTMLElement>('[data-testid="todo-group"]')];
    const unsortedGroup = groups.find((el) => el.dataset.group === "todos-project:\u0000");
    expect(unsortedGroup).toBeTruthy();
    expect(unsortedGroup?.querySelector("button[aria-expanded]")).toBeNull();
    expect(unsortedGroup?.textContent).not.toContain(t("lib-todo-tree.unsorted"));
    const namedGroup = groups.find((el) => el.dataset.group === "todos-kb:persona");
    expect(namedGroup?.querySelector("button[aria-expanded]")?.textContent).toContain("Persona Core");
  });

  it("immediately reverts to standard view when todos_v2 is switched off", async () => {
    await render({ todosV2: true });
    await clickKind(t("todos.kind_filter_todos"));
    expect(visibleTodoIds()).toEqual(["personal_plain", "personal_proj"]);
    expect(host.querySelector('[data-todo="personal_proj"] [data-testid="todo-project-tag"]')).toBeTruthy();

    // Disable todos_v2 in store -> immediate rollback without reload or migration
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(kindFilterEl()).toBeNull();
    expect(host.querySelector("[data-testid=todo-project-tag]")).toBeNull();
    expect(summary()).toBe(t("todos.summary_triage", { open: 4, parked: 0, shelved: 0, done: 1, cancelled: 0 }));
    expect(visibleTodoIds()).toEqual(["task_supervised", "task_agent", "personal_plain", "personal_proj", "task_done"]);
  });

  it("stays off when assistant payload belongs to a different manager", async () => {
    await render({ todosV2: true, assistantManager: "other-manager" });
    expect(kindFilterEl()).toBeNull();
    expect(host.querySelector("[data-testid=todo-project-tag]")).toBeNull();
  });
});

describe("TodosView goal folding and blocker surfacing under todos_v2 (#14661)", () => {
  const goalTodos: Todo[] = [
    // Top-level personal goal
    {
      id: "goal_ship",
      title: "Launch Q4 release",
      audience: "human",
      created_at: iso(8 * HOUR),
      updated_at: iso(2 * HOUR),
      view: view("working", "agent_running", "working", 0),
    },
    // Non-urgent child task: working
    {
      id: "child_work",
      title: "Build release artifacts",
      part_of: "goal_ship",
      audience: "autonomous",
      agents: [{ machine_id: "m1", session_id: "s-work", linked_at: iso(2 * HOUR) }],
      created_at: iso(6 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 1),
    },
    // Non-urgent child task: completed
    {
      id: "child_done",
      title: "Freeze release branch",
      part_of: "goal_ship",
      audience: "autonomous",
      created_at: iso(7 * HOUR),
      updated_at: iso(3 * HOUR),
      done_at: iso(3 * HOUR),
      view: view("done", "closed", "done", 0),
    },
    // Urgent child task: needs_you (blocker)
    {
      id: "child_blocker",
      title: "Approve production cutover",
      part_of: "goal_ship",
      audience: "autonomous",
      supervision: { supervisor: "main", state: "needs_user" },
      ask: { verdict: "Ready to cut over?", asked_at: iso(1 * HOUR), outcome: "decide", recommend: "Proceed" },
      created_at: iso(5 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("needs_you", "owner_decides", "needs_you", 0),
    },
    // Standalone top-level personal to-do
    {
      id: "solo_todo",
      title: "Send release announcement",
      audience: "human",
      created_at: iso(4 * HOUR),
      updated_at: iso(2 * HOUR),
      view: view("todo", "rest", "todo", 0),
    },
  ];

  it("keeps legacy flat DOM and counts pixel-identical when todos_v2 is false or omitted", async () => {
    await render({ todos: goalTodos, todosV2: undefined, selectedId: "goal_ship" });
    expect(host.querySelector("[data-testid=todo-goal-toggle]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-parent-goal]")).toBeNull();
    const legacyHtml = host.querySelector("[data-testid=todos-list-column]")?.innerHTML;
    const legacyIds = visibleTodoIds();
    expect(legacyIds).toEqual(["child_blocker", "goal_ship", "child_work", "solo_todo", "child_done"]);

    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todos: goalTodos, todosV2: false, selectedId: "goal_ship" });
    expect(host.querySelector("[data-testid=todo-goal-toggle]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-parent-goal]")).toBeNull();
    expect(host.querySelector("[data-testid=todos-list-column]")?.innerHTML).toBe(legacyHtml);
  });

  it("folds non-urgent children, shows done/total progress & inline expand without opening detail, and surfaces needs_you blocker at top", async () => {
    await render({ todos: goalTodos, todosV2: true, selectedId: "solo_todo" });

    // Non-urgent children (child_work, child_done) are folded; blocker surfaces in waiting (`needs_you`)
    expect(visibleTodoIds()).toEqual(["child_blocker", "goal_ship", "solo_todo"]);

    // Waiting section holds the surfaced blocker with parent goal hint
    const waitingSec = host.querySelector<HTMLElement>('[data-testid="todo-section"][data-section="waiting"]');
    expect(waitingSec?.querySelector('[data-todo="child_blocker"]')).toBeTruthy();
    expect(waitingSec?.querySelector('[data-todo="child_blocker"] [data-testid="todo-parent-goal"]')?.textContent).toContain("Launch Q4 release");

    // Parent goal row shows compact 1/3 progress, blocked indicator, and accessible toggle
    const parentRow = host.querySelector<HTMLElement>('[data-todo="goal_ship"]')!;
    const toggle = parentRow.querySelector<HTMLButtonElement>('[data-testid="todo-goal-toggle"]')!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.className).toContain("touch-hit");
    expect(parentRow.querySelector('[data-testid="todo-goal-progress"]')?.textContent).toBe("1/3");
    expect(parentRow.querySelector('[data-testid="todo-goal-blocked"]')?.textContent).toBe(t("todos.goal_blocked", { count: 1 }));
    expect(host.querySelector('[data-testid="todo-goal-children"]')).toBeNull();

    // Clicking toggle expands inline children WITHOUT changing the selected detail (solo_todo stays selected)
    await act(async () => toggle.click());
    await flush();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector('[data-todo="solo_todo"]')?.getAttribute("aria-current")).toBe("true");
    expect(parentRow.getAttribute("aria-current")).toBeNull();

    const childrenBox = host.querySelector<HTMLElement>('[data-testid="todo-goal-children"][data-parent="goal_ship"]')!;
    expect(childrenBox).toBeTruthy();
    const childIds = [...childrenBox.querySelectorAll<HTMLElement>("[data-todo]")].map((el) => el.dataset.todo);
    expect(childIds).toEqual(["child_blocker", "child_work", "child_done"]);

    // Clicking toggle again collapses children
    await act(async () => toggle.click());
    await flush();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector('[data-testid="todo-goal-children"]')).toBeNull();
  });

  it("interoperates with All / To-dos / Tasks filter and reverts immediately when todos_v2 is switched off", async () => {
    await render({ todos: goalTodos, todosV2: true });
    expect(visibleTodoIds()).toEqual(["child_blocker", "goal_ship", "solo_todo"]);

    // Under Tasks filter, personal parent goal_ship is excluded -> its task children stay visible as top-level rows (child_done is inside folded Done)
    // and retain their parent goal hint (#16939) so parent context is never lost when filtered.
    await clickKind(t("todos.kind_filter_tasks"));
    expect(visibleTodoIds()).toEqual(["child_blocker", "child_work"]);
    expect(host.querySelector('[data-testid="todo-section"][data-section="done"]')?.getAttribute("data-todos")).toBe("1");
    expect(host.querySelector('[data-todo="child_blocker"] [data-testid="todo-parent-goal"]')?.textContent).toContain("Launch Q4 release");
    expect(host.querySelector('[data-todo="child_work"] [data-testid="todo-parent-goal"]')?.textContent).toContain("Launch Q4 release");
    expect(host.querySelector('[data-testid="todo-goal-toggle"]')).toBeNull();

    // Under To-dos filter, task children are excluded -> personal parent renders without a fold toggle
    await clickKind(t("todos.kind_filter_todos"));
    expect(visibleTodoIds()).toEqual(["goal_ship", "solo_todo"]);
    expect(host.querySelector('[data-testid="todo-goal-toggle"]')).toBeNull();

    // Back to All -> goal folding resumes
    await clickKind(t("todos.kind_filter_all"));
    expect(visibleTodoIds()).toEqual(["child_blocker", "goal_ship", "solo_todo"]);

    // Switching todos_v2 off reverts immediately to flat list with zero migration
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(host.querySelector('[data-testid="todo-goal-toggle"]')).toBeNull();
    expect(host.querySelector('[data-testid="todo-parent-goal"]')).toBeNull();
    expect(visibleTodoIds()).toEqual(["child_blocker", "goal_ship", "child_work", "solo_todo", "child_done"]);
  });

  it("gates the detail view Target Repo / Reference Repo matrix on todos_v2 and reverts immediately when switched off", async () => {
    const repoSet = {
      schema_version: 1,
      manager: "assistant",
      todo_id: "task_agent",
      revision: 1,
      repositories: [
        {
          repo_id: "docs",
          requested_ref: { kind: "branch", value: "docs/v2" },
          source_snapshot: {
            identity: { provider: "github", instance_url: "https://github.com", path: "example/docs", canonical_url: "https://github.com/example/docs" },
            clone_url: "https://github.com/example/docs",
            transport_origin: "canonical_default",
            catalog_revision: 1,
          },
          links: [{ kind: "issue", number: "5", canonical_url: "https://github.com/example/docs/issues/5", role: "reference" }],
        },
        {
          repo_id: "web",
          requested_ref: { kind: "default" },
          source_snapshot: {
            identity: { provider: "github", instance_url: "https://github.com", path: "example/web", canonical_url: "https://github.com/example/web" },
            clone_url: "https://github.com/example/web",
            transport_origin: "canonical_default",
            catalog_revision: 1,
          },
          links: [{ kind: "pull_request", number: "12", canonical_url: "https://github.com/example/web/pull/12", role: "delivery" }],
        },
      ],
    };
    api.mockImplementation(async (path: string) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/task_agent/repositories") return repoSet;
      return {};
    });

    await render({ todos: sampleTodos, todosV2: true, selectedId: "task_agent" });
    const matrix = host.querySelector("[data-repo-matrix]");
    expect(matrix).toBeTruthy();
    const rows = [...matrix!.querySelectorAll<HTMLElement>("[data-repository-row]")];
    expect(rows.map((r) => r.dataset.repoRole)).toEqual(["target", "reference"]);
    expect(matrix!.querySelector('[data-repo-role-badge="target"]')?.textContent).toBe(t("task-repositories.role_target"));
    expect(matrix!.querySelector('[data-repo-role-badge="reference"]')?.textContent).toBe(t("task-repositories.role_reference"));

    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(host.querySelector("[data-repo-matrix]")).toBeNull();
    expect(host.querySelector("[data-repo-role-badge]")).toBeNull();
  });
});

describe("TodosView shared external blockers strip under todos_v2 (#14728)", () => {
  const sampleBlockers = [
    {
      key: "pull_request:https://github.com/example/core/pull/42",
      kind: "pull_request",
      label: "example/core#42",
      url: "https://github.com/example/core/pull/42",
      todo_ids: ["task_agent", "task_supervised"],
      count: 2,
    },
    {
      key: "target:staging deploy",
      kind: "external_target",
      label: "staging deploy",
      todo_ids: ["personal_proj", "task_agent"],
      count: 2,
    },
  ];

  const stripEl = () => host.querySelector<HTMLElement>("[data-testid=todos-shared-blockers]");
  const chips = () => [...host.querySelectorAll<HTMLButtonElement>("[data-testid=shared-blocker-chip]")];

  it("renders no shared blockers DOM when todos_v2 is false or omitted, even if shared_blockers exists", async () => {
    await render({ todosV2: undefined, sharedBlockers: sampleBlockers });
    expect(stripEl()).toBeNull();
    const legacyHtml = host.querySelector("[data-testid=todos-list-column]")?.innerHTML;

    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todosV2: false, sharedBlockers: sampleBlockers });
    expect(stripEl()).toBeNull();
    expect(host.querySelector("[data-testid=todos-list-column]")?.innerHTML).toBe(legacyHtml);
  });

  it("renders nothing under todos_v2=true when shared_blockers is empty or absent", async () => {
    await render({ todosV2: true, sharedBlockers: [] });
    expect(stripEl()).toBeNull();
  });

  it("renders blocker labels, counts, and safe external links, and filters tasks on click with toggle and clear", async () => {
    await render({ todosV2: true, sharedBlockers: sampleBlockers });
    expect(stripEl()).toBeTruthy();

    const list = chips();
    expect(list).toHaveLength(2);
    expect(list[0].textContent).toContain("example/core#42");
    expect(list[0].textContent).toContain(t("todos.shared_blockers_count", { count: 2 }));
    expect(list[0].getAttribute("aria-pressed")).toBe("false");

    // External link renders only for valid http(s) URL, never nested inside the filter button
    const links = [...host.querySelectorAll<HTMLAnchorElement>("[data-testid=shared-blocker-link]")];
    expect(links).toHaveLength(1);
    expect(links[0].getAttribute("href")).toBe("https://github.com/example/core/pull/42");
    expect(links[0].getAttribute("target")).toBe("_blank");
    expect(links[0].getAttribute("rel")).toBe("noopener noreferrer");
    expect(links[0].closest("button")).toBeNull();
    expect(host.querySelector("[data-testid=shared-blockers-clear]")).toBeNull();

    // Click first blocker -> filters list to its todo_ids
    await act(async () => list[0].click());
    await flush();
    expect(list[0].getAttribute("aria-pressed")).toBe("true");
    expect(visibleTodoIds()).toEqual(["task_supervised", "task_agent"]);
    expect(host.querySelector("[data-testid=shared-blockers-clear]")).toBeTruthy();

    // Click same blocker again -> toggles off and restores all rows (Done remains folded under todos_v2)
    await act(async () => list[0].click());
    await flush();
    expect(list[0].getAttribute("aria-pressed")).toBe("false");
    expect(visibleTodoIds()).toEqual(["task_supervised", "task_agent", "personal_plain", "personal_proj"]);

    // Click second blocker, then click Clear filter button -> restores all rows
    await act(async () => chips()[1].click());
    await flush();
    expect(visibleTodoIds()).toEqual(["task_agent", "personal_proj"]);
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=shared-blockers-clear]")!.click());
    await flush();
    expect(visibleTodoIds()).toEqual(["task_supervised", "task_agent", "personal_plain", "personal_proj"]);
  });

  it("auto-clears selection when the selected blocker disappears after refresh, and reverts when todos_v2 is switched off", async () => {
    await render({ todosV2: true, sharedBlockers: sampleBlockers });
    await act(async () => chips()[0].click());
    await flush();
    expect(visibleTodoIds()).toEqual(["task_supervised", "task_agent"]);

    // Refresh drops the selected blocker -> automatically exits filter instead of deadlocking empty
    await act(async () => {
      seed({ sharedBlockers: [sampleBlockers[1]] });
      root.render(<TodosView />);
    });
    await flush();
    expect(visibleTodoIds()).toEqual(["task_supervised", "task_agent", "personal_plain", "personal_proj"]);
    expect(host.querySelector("[data-testid=shared-blockers-clear]")).toBeNull();

    // Re-select remaining blocker, then turn todos_v2 off -> immediate rollback
    await act(async () => chips()[0].click());
    await flush();
    expect(visibleTodoIds()).toEqual(["task_agent", "personal_proj"]);

    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(stripEl()).toBeNull();
    expect(visibleTodoIds()).toEqual(["task_supervised", "task_agent", "personal_plain", "personal_proj", "task_done"]);
  });
});

describe("TodosView batch reorganize mode under todos_v2 (#14758)", () => {
  const toggleBtn = () => host.querySelector<HTMLButtonElement>("[data-testid=todos-reorganize-toggle]");
  const barEl = () => host.querySelector<HTMLElement>("[data-testid=batch-reorganize-bar]");
  const checkboxes = () => [...host.querySelectorAll<HTMLInputElement>("[data-testid=todo-reorganize-checkbox]")];
  const checkboxFor = (id: string) =>
    host.querySelector<HTMLInputElement>(`[data-testid=todo-reorganize-checkbox][data-todo-id="${id}"]`);

  it("renders no batch reorganize controls when todos_v2 is false or omitted by an older Hub", async () => {
    await render({ todosV2: undefined, selectedId: "personal_plain" });
    expect(toggleBtn()).toBeNull();
    expect(barEl()).toBeNull();
    expect(checkboxes()).toHaveLength(0);
    const legacyHeader = host.querySelector("header")?.innerHTML;
    const legacyList = host.querySelector("[data-testid=todos-list-column]")?.innerHTML;

    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todosV2: false, selectedId: "personal_plain" });
    expect(toggleBtn()).toBeNull();
    expect(barEl()).toBeNull();
    expect(checkboxes()).toHaveLength(0);
    expect(host.querySelector("header")?.innerHTML).toBe(legacyHeader);
    expect(host.querySelector("[data-testid=todos-list-column]")?.innerHTML).toBe(legacyList);
  });

  it("supports multi-select across states without opening detail, excludes cycle parent targets, and submits exact payload", async () => {
    const hierarchyTodos: Todo[] = [
      ...sampleTodos,
      {
        id: "child_of_plain",
        title: "Grind beans",
        part_of: "personal_plain",
        created_at: iso(3 * HOUR),
        updated_at: iso(2 * HOUR),
        view: view("todo", "rest", "todo", 2),
      },
    ];
    api.mockImplementation(async (path: string) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/reorganize") return { updated: [hierarchyTodos[0], hierarchyTodos[2]] };
      if (path === "/todos") return { todos: hierarchyTodos };
      return {};
    });

    await render({
      todos: hierarchyTodos,
      todosV2: true,
      selectedId: "task_supervised",
      leaders: ["dir-platform"],
      conversations: [{ machine_id: "m1", session_id: "dir-platform", title: "Platform Direction" }],
    });

    expect(toggleBtn()?.getAttribute("aria-pressed")).toBe("false");
    expect(barEl()).toBeNull();

    // Enter organize mode
    await act(async () => toggleBtn()!.click());
    await flush();
    expect(toggleBtn()?.getAttribute("aria-pressed")).toBe("true");
    expect(barEl()).toBeTruthy();

    // Select a personal to-do and a working agent task; detail selection stays on task_supervised
    await act(async () => checkboxFor("personal_plain")!.click());
    await act(async () => checkboxFor("task_agent")!.click());
    await flush();
    expect(host.querySelector('[data-todo="task_supervised"]')?.getAttribute("aria-current")).toBe("true");
    expect(host.querySelector("[data-testid=reorganize-selected-count]")?.textContent).toBe(
      t("todos.reorganize_selected_count", { count: 2 }),
    );

    // Parent dropdown excludes selected items AND their descendants (child_of_plain) to prevent cycles
    const parentSelect = host.querySelector<HTMLSelectElement>("[data-testid=reorganize-part-of]")!;
    const parentValues = [...parentSelect.options].map((o) => o.value);
    expect(parentValues).not.toContain("personal_plain");
    expect(parentValues).not.toContain("task_agent");
    expect(parentValues).not.toContain("child_of_plain");
    expect(parentValues).toContain("personal_proj");

    // transfer_supervision is disabled and unchecked by default until an active direction is chosen
    const transferBox = host.querySelector<HTMLInputElement>("[data-testid=reorganize-transfer-supervision]")!;
    expect(transferBox.checked).toBe(false);
    expect(transferBox.disabled).toBe(true);

    // Choose target parent and active direction
    await act(async () => {
      parentSelect.value = "personal_proj";
      parentSelect.dispatchEvent(new Event("change", { bubbles: true }));
    });
    const dirSelect = host.querySelector<HTMLSelectElement>("[data-testid=reorganize-direction]")!;
    await act(async () => {
      dirSelect.value = "dir-platform";
      dirSelect.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    expect(transferBox.disabled).toBe(false);
    expect(transferBox.checked).toBe(false);

    // Submit without checking transfer_supervision -> defaults to false
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=reorganize-apply]")!.click());
    await flush();

    expect(api).toHaveBeenCalledWith(
      "/todos/reorganize",
      expect.objectContaining({
        method: "POST",
        json: {
          ids: ["personal_plain", "task_agent"],
          part_of: "personal_proj",
          direction_id: "dir-platform",
          include_children: false,
          transfer_supervision: false,
        },
      }),
    );
    // Refreshes list, clears selection, and shows feedback
    expect(api).toHaveBeenCalledWith("/todos", expect.anything());
    expect(host.querySelector("[data-testid=reorganize-selected-count]")?.textContent).toBe(
      t("todos.reorganize_selected_count", { count: 0 }),
    );
    expect(host.querySelector("[data-testid=reorganize-feedback]")?.textContent).toBe(
      t("todos.reorganize_success", { count: 2 }),
    );
  });

  it("shows inline error without losing selection on API failure, and resets immediately when todos_v2 turns off", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/reorganize") throw new Error("read-only role cannot modify todos");
      return {};
    });

    await render({ todosV2: true });
    await act(async () => toggleBtn()!.click());
    await flush();

    await act(async () => checkboxFor("personal_plain")!.click());
    await flush();

    const parentSelect = host.querySelector<HTMLSelectElement>("[data-testid=reorganize-part-of]")!;
    await act(async () => {
      parentSelect.value = "__clear__";
      parentSelect.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=reorganize-apply]")!.click());
    await flush();

    expect(host.querySelector("[data-testid=reorganize-error]")?.textContent).toContain("read-only role cannot modify todos");
    // Selection preserved on error
    expect(checkboxFor("personal_plain")?.checked).toBe(true);
    expect(host.querySelector("[data-testid=reorganize-selected-count]")?.textContent).toBe(
      t("todos.reorganize_selected_count", { count: 1 }),
    );

    // Turning todos_v2 off exits organize mode and removes all controls immediately
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(toggleBtn()).toBeNull();
    expect(barEl()).toBeNull();
    expect(checkboxes()).toHaveLength(0);
  });

  it("Select all only selects currently expanded visible rows and excludes folded sections & collapsed subtasks (#17696)", async () => {
    const foldTodos: Todo[] = [
      {
        id: "open_goal",
        title: "Open parent goal",
        created_at: iso(5 * HOUR),
        updated_at: iso(1 * HOUR),
        view: view("working", "agent_running", "working", 0),
      },
      {
        id: "folded_child",
        title: "Collapsed subtask under open goal",
        part_of: "open_goal",
        created_at: iso(4 * HOUR),
        updated_at: iso(1 * HOUR),
        view: view("working", "agent_running", "working", 1),
      },
      {
        id: "open_personal",
        title: "Visible personal item",
        created_at: iso(3 * HOUR),
        updated_at: iso(1 * HOUR),
        view: view("todo", "rest", "todo", 0),
      },
      {
        id: "folded_done",
        title: "Hidden in folded Done section",
        done_at: iso(1 * HOUR),
        created_at: iso(6 * HOUR),
        updated_at: iso(1 * HOUR),
        view: view("done", "closed", "done", 0),
      },
    ];

    await render({ todos: foldTodos, todosV2: true });
    await act(async () => toggleBtn()!.click());
    await flush();

    // Only open_goal and open_personal are mounted; folded_child and folded_done are collapsed
    const selectAllBtn = host.querySelector<HTMLButtonElement>("[data-testid=reorganize-select-all]")!;
    expect(selectAllBtn.disabled).toBe(false);
    await act(async () => selectAllBtn.click());
    await flush();

    expect(host.querySelector("[data-testid=reorganize-selected-count]")?.textContent).toBe(
      t("todos.reorganize_selected_count", { count: 2 }),
    );
    expect(checkboxFor("open_goal")?.checked).toBe(true);
    expect(checkboxFor("open_personal")?.checked).toBe(true);
    expect(checkboxFor("folded_child")).toBeNull();
    expect(checkboxFor("folded_done")).toBeNull();

    // Expanding open_goal makes folded_child visible -> Select all re-enables and selects it too (still excluding folded_done)
    await act(async () => host.querySelector<HTMLButtonElement>('[data-todo="open_goal"] [data-testid="todo-goal-toggle"]')!.click());
    await flush();
    expect(selectAllBtn.disabled).toBe(false);
    await act(async () => selectAllBtn.click());
    await flush();
    expect(host.querySelector("[data-testid=reorganize-selected-count]")?.textContent).toBe(
      t("todos.reorganize_selected_count", { count: 3 }),
    );
    expect(checkboxFor("folded_child")?.checked).toBe(true);
  });
});

describe("TodosView TodoPlanEditor, ARIA/keyboard a11y, and ja localization polish (#17696)", () => {
  it("renders a non-empty None project option under todos_v2=true with zero React key warnings", async () => {
    const errSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      await render({ todos: sampleTodos, todosV2: true, selectedId: "personal_plain" });
      const select = host.querySelector<HTMLSelectElement>("[data-testid=todo-plan-project]")!;
      expect(select).toBeTruthy();
      expect(select.options[0]?.value).toBe("");
      expect(select.options[0]?.textContent).toBe(t("todos.project_none"));
      expect(select.options[0]?.textContent?.trim().length).toBeGreaterThan(0);
      const keyWarnings = errSpy.mock.calls.filter((args) =>
        args.some((arg) => typeof arg === "string" && arg.includes('unique "key" prop')),
      );
      expect(keyWarnings).toEqual([]);
    } finally {
      errSpy.mockRestore();
    }
  });

  it("avoids nested interactive controls in list rows and exposes aria-expanded on fold toggles and keyboard-reachable agent cards", async () => {
    const a11yTodos: Todo[] = [
      {
        id: "open_item",
        title: "Active work item",
        agents: [{ machine_id: "m1", session_id: "s-live", linked_at: iso(1 * HOUR), title: "Live worker" }],
        created_at: iso(3 * HOUR),
        updated_at: iso(1 * HOUR),
        view: view("working", "agent_running", "working", 0),
      },
      {
        id: "done_item",
        title: "Finished work item",
        done_at: iso(1 * HOUR),
        created_at: iso(4 * HOUR),
        updated_at: iso(1 * HOUR),
        view: view("done", "closed", "done", 0),
      },
      {
        id: "cancelled_item",
        title: "Cancelled work item",
        cancelled_at: iso(1 * HOUR),
        created_at: iso(5 * HOUR),
        updated_at: iso(1 * HOUR),
        view: view("cancelled", "cancelled", "cancelled", 0),
      },
    ];
    await render({
      todos: a11yTodos,
      sessions: {
        m1: [{ id: "s-live", machine_id: "m1", session_id: "s-live", agent: "claude", title: "Live worker", cwd: "/repo", state: "working", updated_at: iso(5 * 60_000) }],
      },
      todosV2: true,
      grouping: "folder",
      selectedId: "open_item",
    });

    // Zero interactive controls nested inside role="button" or <button> across the entire view
    expect(host.querySelectorAll('[role="button"] button, [role="button"] input, [role="button"] a')).toHaveLength(0);
    expect(host.querySelectorAll("button button, button input, button a")).toHaveLength(0);

    // Done & Cancelled fold toggles carry accurate aria-expanded
    const doneFold = host.querySelector<HTMLButtonElement>("[data-testid=todos-done-fold]")!;
    expect(doneFold.getAttribute("aria-expanded")).toBe("false");
    await act(async () => doneFold.click());
    await flush();
    expect(doneFold.getAttribute("aria-expanded")).toBe("true");

    const cancelledFold = host.querySelector<HTMLButtonElement>("[data-testid=todos-cancelled-fold]")!;
    expect(cancelledFold.getAttribute("aria-expanded")).toBe("false");
    await act(async () => cancelledFold.click());
    await flush();
    expect(cancelledFold.getAttribute("aria-expanded")).toBe("true");

    // Active agent card uses a native keyboard-focusable <button> sibling to Unlink
    const agentOpen = host.querySelector<HTMLButtonElement>("[data-testid=todo-agent-open]")!;
    expect(agentOpen).toBeTruthy();
    expect(agentOpen.tagName).toBe("BUTTON");
    expect(agentOpen.type).toBe("button");
    expect(agentOpen.querySelector("button")).toBeNull();
  });

  it("localizes ja v2_labs_badge and mergetrain_position_badge and keeps en/zh-Hans/ja parity", async () => {
    const [enTodos, zhTodos, jaTodos] = await Promise.all([
      import("../i18n/locales/en/todos.json"),
      import("../i18n/locales/zh-Hans/todos.json"),
      import("../i18n/locales/ja/todos.json"),
    ]);
    expect(jaTodos.default.v2_labs_badge).not.toBe(enTodos.default.v2_labs_badge);
    expect(jaTodos.default.v2_labs_badge).toContain("ラボ");
    expect(jaTodos.default.mergetrain_position_badge).not.toBe(enTodos.default.mergetrain_position_badge);
    expect(jaTodos.default.mergetrain_position_badge).toContain("{position}");
    expect(jaTodos.default.mergetrain_position_badge).toContain("{state}");
    for (const key of ["detail_focus_region", "detail_focus_less", "detail_audit_group"] as const) {
      expect(enTodos.default[key]).toBeTruthy();
      expect(zhTodos.default[key]).toBeTruthy();
      expect(jaTodos.default[key]).toBeTruthy();
    }
  });
});

describe("TodoDetail dual template, default-expanded sections & status summary under todos_v2 (#14777, #14851)", () => {
  const longNotes =
    "Line 1: Context about the migration plan.\nLine 2: Ensure backward compatibility.\nLine 3: Keep first screen bundle within budget.\nLine 4: Verify rollback path.\nLine 5: Validate i18n keys.\nLine 6: Ship clean PR.";

  const dualTodos: Todo[] = [
    {
      id: "personal_note",
      title: "Pick up groceries",
      audience: "human",
      notes: "Milk and eggs",
      created_at: iso(5 * HOUR),
      updated_at: iso(4 * HOUR),
      view: view("todo", "rest", "todo", 0),
    },
    {
      id: "goal_parent",
      title: "Ship Todos V2",
      direction_id: "dir-platform",
      created_at: iso(8 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 0),
    },
    {
      id: "work_goal_active",
      title: "Implement dual-template detail page",
      part_of: "goal_parent",
      direction_id: "dir-platform",
      priority: "high",
      notes: longNotes,
      pr_url: "https://github.com/nwverse/persona/pull/14777",
      agents: [
        { machine_id: "m1", session_id: "s-active", linked_at: iso(2 * HOUR), title: "Frontend worker" },
        { machine_id: "m1", session_id: "s-past", linked_at: iso(4 * HOUR), released_at: iso(3 * HOUR), title: "Earlier planner", cwd: "/repo/web" },
      ],
      created_at: iso(4 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 1),
    },
    {
      id: "work_goal_with_due",
      title: "Prepare launch notes",
      part_of: "goal_parent",
      due_date: "2026-12-01",
      supervision: { supervisor: "main", state: "needs_user" },
      created_at: iso(3 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 2),
    },
  ];

  const activeSessions = {
    m1: [
      {
        id: "s-active",
        machine_id: "m1",
        session_id: "s-active",
        agent: "claude",
        title: "Frontend worker",
        cwd: "/repo/web",
        state: "working",
        updated_at: iso(10 * 60_000),
      },
    ],
  };

  it("keeps TodoDetail DOM 100% identical when todos_v2 is false or omitted by an older Hub", async () => {
    await render({
      todos: dualTodos,
      sessions: activeSessions,
      todosV2: undefined,
      selectedId: "work_goal_active",
      leaders: ["dir-platform"],
      conversations: [{ machine_id: "m1", session_id: "dir-platform", title: "Platform Direction" }],
    });
    expect(host.querySelector("[data-testid=todo-detail-ownership]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-status-summary]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-priority-subtle]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-summary-open-active-agent]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-personal-fields-toggle]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-notes-collapsed]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-open-active-agent]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-open-pr]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-add-agent]")).toBeNull();
    // Released agent history remains collapsed by default in legacy view
    expect(host.querySelector("[data-testid=todo-detail]")?.textContent).not.toContain("Earlier planner");
    const legacyDetailHtml = host.querySelector("[data-testid=todo-detail]")?.innerHTML;

    await act(async () => root.unmount());
    root = createRoot(host);
    await render({
      todos: dualTodos,
      sessions: activeSessions,
      todosV2: false,
      selectedId: "work_goal_active",
      leaders: ["dir-platform"],
      conversations: [{ machine_id: "m1", session_id: "dir-platform", title: "Platform Direction" }],
    });
    expect(host.querySelector("[data-testid=todo-detail]")?.innerHTML).toBe(legacyDetailHtml);
  });

  it("keeps personal to-dos on the lightweight template under todos_v2=true", async () => {
    await render({ todos: dualTodos, todosV2: true, selectedId: "personal_note" });
    // Personal template keeps no work_goal badge, status summary, subtasks, or agent CTAs;
    // when eligible parent targets exist, the single unified ownership strip houses the Parent goal button without duplicating it on the title row (#16939).
    expect(host.querySelector("[data-testid=todo-detail-template-badge]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-ownership]")).toBeTruthy();
    expect(host.querySelectorAll("[data-testid=todo-detail-change-parent]")).toHaveLength(1);
    expect(host.querySelector("[data-testid=todo-detail-status-summary]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-personal-fields-toggle]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-subtasks]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-plan]")).toBeTruthy();
    expect(host.querySelector("[data-testid=todo-plan-priority]")?.className).toContain("text-ink-3");
    expect(host.querySelector("[data-testid=todo-open-active-agent]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-add-agent]")).toBeNull();

    // A lone personal to-do with no direction, parent, children, or eligible reorganization targets omits the ownership strip completely
    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todos: [dualTodos[0]], todosV2: true, selectedId: "personal_note" });
    expect(host.querySelector("[data-testid=todo-detail-ownership]")).toBeNull();
  });

  it("converges top overview strip, deduplicates meta row, keeps primary workspace expanded, and default-collapses secondary audit sections under todos_v2=true", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path.startsWith("/todos/work_goal_active/decisions")) {
        return {
          decisions: [
            {
              id: "d1",
              todo_id: "work_goal_active",
              question: "Keep sections expanded by default?",
              choice: "Yes, expand primary workspace",
              reason: "Keep primary execution open and fold audit sections",
              source: "manual",
              source_machine: "",
              source_session: "",
              source_message: "",
              supersedes_id: "",
              superseded_by: "",
              created_at: iso(1 * HOUR),
            },
          ],
        };
      }
      return {};
    });

    await render({
      todos: dualTodos,
      sessions: activeSessions,
      todosV2: true,
      selectedId: "work_goal_active",
      leaders: ["dir-platform"],
      conversations: [{ machine_id: "m1", session_id: "dir-platform", title: "Platform Direction" }],
    });

    // Single consolidated top overview strip holds status, work-goal badge, agent count, direction, parent link, subtle priority, and primary CTA
    const ownership = host.querySelector<HTMLElement>("[data-testid=todo-detail-ownership]")!;
    expect(ownership).toBeTruthy();
    expect(host.querySelector("[data-testid=todo-detail-status-summary]")).toBeNull();
    expect(ownership.className).toContain("bg-inset");
    expect(ownership.className).toContain("border-line");
    expect(ownership.className).toContain("rounded-control");
    expect(ownership.querySelector("[data-testid=todo-detail-summary-status]")?.textContent).toBeTruthy();
    expect(ownership.querySelector("[data-testid=todo-detail-template-badge]")?.textContent).toBe(t("todos.detail_template_work_goal"));
    expect(ownership.querySelector("[data-testid=todo-detail-direction]")?.textContent).toContain("Platform Direction");
    const parentLink = ownership.querySelector<HTMLButtonElement>("[data-testid=todo-detail-parent-link]")!;
    expect(parentLink.textContent).toContain("Ship Todos V2");
    const subtlePriority = ownership.querySelector<HTMLElement>("[data-testid=todo-detail-priority-subtle]")!;
    expect(subtlePriority).toBeTruthy();
    expect(subtlePriority.textContent).toContain(t("todos.priority_high"));
    expect(subtlePriority.className).toContain("text-ink-3");
    const summaryOpenAgentBtn = ownership.querySelector<HTMLButtonElement>("[data-testid=todo-summary-open-active-agent]")!;
    expect(summaryOpenAgentBtn.textContent).toBe(t("todos.detail_open_active_agent"));

    // Meta row beneath title does not repeat status badge or agent count for work goals
    const metaRow = host.querySelector<HTMLElement>("[data-testid=todo-detail-meta]")!;
    expect(metaRow).toBeTruthy();
    expect(metaRow.querySelector("[data-testid=todo-detail-status]")).toBeNull();
    expect(metaRow.textContent).not.toContain(t("todos.agent_count", { count: 1 }));

    // Primary workspace (plan fields with muted priority, notes, active agents) stays expanded by default
    expect(host.querySelector("[data-testid=todo-detail-personal-fields-toggle]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-plan]")).toBeTruthy();
    expect(host.querySelector("[data-testid=todo-plan-priority]")?.className).toContain("text-ink-3");
    expect(host.querySelector("[data-testid=todo-detail-notes-collapsed]")).toBeNull();
    expect(host.querySelector<HTMLTextAreaElement>("textarea")?.value).toBe(longNotes);

    // Secondary audit sections (decisions & participated history) start collapsed with counts and expand on click
    const decisionsToggle = host.querySelector<HTMLButtonElement>("[data-testid=todo-decisions-toggle]")!;
    expect(decisionsToggle).toBeTruthy();
    expect(decisionsToggle.getAttribute("aria-expanded")).toBe("false");
    expect(decisionsToggle.textContent).toContain(t("todos.decisions_section", { count: 1 }));
    expect(host.querySelector("[data-testid=todo-decisions]")?.textContent).not.toContain("Keep sections expanded by default?");
    await act(async () => decisionsToggle.click());
    await flush();
    expect(decisionsToggle.getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector("[data-testid=todo-decisions]")?.textContent).toContain("Keep sections expanded by default?");

    expect(host.querySelector("[data-testid=todo-detail]")?.textContent).not.toContain("Earlier planner");
    const releasedToggle = [...host.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")].find((b) =>
      b.textContent?.includes(t("todos.released_section", { count: 1 })),
    )!;
    expect(releasedToggle).toBeTruthy();
    expect(releasedToggle.getAttribute("aria-expanded")).toBe("false");
    await act(async () => releasedToggle.click());
    await flush();
    expect(releasedToggle.getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector("[data-testid=todo-detail]")?.textContent).toContain("Earlier planner");

    // Dynamic footer action bar: Open active Agent primary CTA + safe Open PR link + demoted Add Agent button
    const openAgentBtn = host.querySelector<HTMLButtonElement>("[data-testid=todo-open-active-agent]")!;
    expect(openAgentBtn.textContent).toBe(t("todos.detail_open_active_agent"));
    const openPrLink = host.querySelector<HTMLAnchorElement>("[data-testid=todo-open-pr]")!;
    expect(openPrLink.getAttribute("href")).toBe("https://github.com/nwverse/persona/pull/14777");
    expect(openPrLink.getAttribute("target")).toBe("_blank");
    expect(openPrLink.getAttribute("rel")).toBe("noopener noreferrer");
    const addAgentBtn = host.querySelector<HTMLButtonElement>("[data-testid=todo-add-agent]")!;
    expect(addAgentBtn.textContent).toBe(t("todos.detail_add_agent"));

    // Clicking parent link navigates to parent goal, which displays its subtasks summary & checklist expanded by default
    await act(async () => parentLink.click());
    await flush();
    expect(host.querySelector("[data-testid=todo-detail-summary-subtasks]")?.textContent).toContain("0/2");
    const subtasks = host.querySelector("[data-testid=todo-detail-subtasks]");
    expect(subtasks).toBeTruthy();
    const subtaskIds = [...subtasks!.querySelectorAll<HTMLElement>("[data-testid=todo-detail-subtask-item]")].map(
      (el) => el.dataset.subtaskId,
    );
    expect(subtaskIds).toEqual(["work_goal_active", "work_goal_with_due"]);

    // Clicking a subtask with due_date keeps schedule/reminder expanded
    await act(async () =>
      subtasks!.querySelector<HTMLButtonElement>('[data-subtask-id="work_goal_with_due"]')!.click(),
    );
    await flush();
    expect(host.querySelector("[data-testid=todo-detail-personal-fields-toggle]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-plan]")).toBeTruthy();

    // Turning todos_v2 off immediately reverts TodoDetail to legacy layout
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(host.querySelector("[data-testid=todo-detail-ownership]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-status-summary]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-subtasks]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-open-active-agent]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-add-agent]")).toBeNull();
  });

  it("unifies wait, active blockers, repo gate, and merge-train into one progressive focus banner and default-collapses responsible & thread under todos_v2=true", async () => {
    const complexTodo: Todo = {
      id: "complex_focus",
      title: "Roll out multi-repo release",
      origin: "conv-main",
      pr_url: "https://github.com/nwverse/persona/pull/17696",
      responsible: { kind: "person", name: "Alex Reviewer" },
      thread: { channel: "feishu", chat_id: "oc_1", thread_id: "omt_1", title: "Release coordination" },
      wait: { kind: "person_reply", waiting_on: "Alex Reviewer", wake_condition: "When sign-off arrives" },
      active_blockers: [
        { id: "blk_1", kind: "external_pr", target_ref: "https://github.com/nwverse/persona/pull/17000", state: "active" },
      ],
      repo_gate: {
        state: "blocked_ci",
        target_count: 2,
        ready_count: 1,
        reference_count: 0,
        blocking_repo: "nwverse/persona",
        blocking_pr: "https://github.com/nwverse/persona/pull/17696",
        reason: "ci_failed",
      },
      created_at: iso(4 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("parked", "external_wait", "parked", 0),
    };
    api.mockImplementation(async (path: string) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/mergetrain") {
        return {
          repos: [],
          by_todo: {
            complex_focus: {
              repo_key: "nwverse/persona",
              position: 2,
              ahead_pr_url: "https://github.com/nwverse/persona/pull/17690",
              state: "queued",
            },
          },
        };
      }
      if (path.includes("/repositories")) return { revision: 1, repositories: [] };
      return {};
    });

    seed({ features: ["todo_view", "todo_threads"] });
    await render({ todos: [complexTodo], todosV2: true, selectedId: "complex_focus" });
    // Ensure todo_threads feature is set after render helper
    seed({ features: ["todo_view", "todo_threads"] });
    await act(async () => root.render(<TodosView selectedId="complex_focus" />));
    await flush();

    // Only ONE focus banner container exists; highest-priority blocked repo_gate renders immediately, others progressively fold
    expect(host.querySelectorAll("[data-testid=todo-detail-focus-banner]")).toHaveLength(1);
    const focus = host.querySelector<HTMLElement>("[data-testid=todo-detail-focus-banner]")!;
    expect(focus.getAttribute("data-focus-count")).toBe("4");
    expect(focus.querySelector("[data-testid=todo-detail-repo-gate]")).toBeTruthy();
    // No duplicate repo gate inside repositories section
    expect(host.querySelectorAll("[data-testid=todo-detail-repo-gate]")).toHaveLength(1);
    // Secondary signals start collapsed behind one aria-expanded toggle
    const moreBtn = focus.querySelector<HTMLButtonElement>("[data-testid=todo-detail-focus-more]")!;
    expect(moreBtn.getAttribute("aria-expanded")).toBe("false");
    expect(focus.querySelector("[data-testid=todo-detail-focus-extra]")).toBeNull();

    // Expanding reveals blockers, wait info, and merge train inside the same unified focus banner
    await act(async () => moreBtn.click());
    await flush();
    expect(moreBtn.getAttribute("aria-expanded")).toBe("true");
    expect(focus.querySelector("[data-testid=todo-active-blockers]")).toBeTruthy();
    expect(focus.querySelector("[data-testid=todo-detail-mergetrain]")).toBeTruthy();
    expect(focus.querySelector("[data-task-wait-info]")).toBeTruthy();

    // Responsible and Thread sections start collapsed with counts/summaries and expand on click
    const respToggle = host.querySelector<HTMLButtonElement>("[data-testid=todo-responsible-toggle]")!;
    expect(respToggle.getAttribute("aria-expanded")).toBe("false");
    expect(respToggle.textContent).toContain("Alex Reviewer");
    expect(host.querySelector("[data-testid=todo-responsible-name]")).toBeNull();
    await act(async () => respToggle.click());
    await flush();
    expect(respToggle.getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector("[data-testid=todo-responsible-name]")?.textContent).toBe("Alex Reviewer");

    const threadToggle = host.querySelector<HTMLButtonElement>("[data-testid=todo-thread-toggle]")!;
    expect(threadToggle.getAttribute("aria-expanded")).toBe("false");
    expect(threadToggle.textContent).toContain("Release coordination");
    expect(host.querySelector("[data-testid=todo-thread-line]")).toBeNull();
    await act(async () => threadToggle.click());
    await flush();
    expect(threadToggle.getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector("[data-testid=todo-thread-line]")?.textContent).toContain("Release coordination");
  });
});

describe("TodosView slim header & left pane under todos_v2 (#14830)", () => {
  const slimTodos: Todo[] = [
    {
      id: "task_decision",
      title: "Approve database cutover",
      priority: "high",
      direction_id: "dir-platform",
      supervision: { supervisor: "main", state: "needs_user" },
      ask: { verdict: "Proceed with blue-green swap?", asked_at: iso(1 * HOUR), outcome: "decide", recommend: "Proceed" },
      created_at: iso(4 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("needs_you", "owner_decides", "needs_you", 0),
    },
    {
      id: "task_pr_working",
      title: "Ship slim left pane",
      priority: "high",
      direction_id: "dir-platform",
      pr_url: "https://github.com/nwverse/persona/pull/14830",
      agents: [{ machine_id: "m1", session_id: "s-run", linked_at: iso(2 * HOUR) }],
      created_at: iso(5 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 0),
    },
    {
      id: "personal_due",
      title: "Renew passport",
      audience: "human",
      priority: "high",
      due_date: "2026-12-10",
      created_at: iso(6 * HOUR),
      updated_at: iso(2 * HOUR),
      view: view("todo", "rest", "todo", 0),
    },
    {
      id: "task_parked",
      title: "Wait for upstream review",
      supervision: {
        supervisor: "main",
        state: "parked",
        parked: { reason: "Waiting on CI mirror", until: new Date(NOW + 24 * HOUR).toISOString(), waiting_on: "external_event", count: 1 },
      },
      created_at: iso(8 * HOUR),
      updated_at: iso(3 * HOUR),
      view: view("parked", "parked", "parked", 0),
    },
    {
      id: "task_recent_done",
      title: "Publish release notes",
      audience: "autonomous",
      created_at: iso(10 * HOUR),
      updated_at: iso(1 * HOUR),
      done_at: iso(1 * HOUR),
      view: view("done", "closed", "done", 0),
    },
  ];

  it("keeps header, list width, toolbar, and rows 100% pixel-identical when todos_v2 is false or omitted", async () => {
    await render({ todos: slimTodos, todosV2: undefined, selectedId: "personal_due" });
    expect(host.querySelector("[data-testid=todos-header-attention]")).toBeNull();
    expect(host.querySelector("[data-testid=todos-v2-labs-link]")).toBeNull();
    expect(host.querySelector("[data-testid=todos-v2-toolbar]")).toBeNull();
    expect(host.querySelector("[data-todo-row-layout]")).toBeNull();
    expect(host.querySelector("[data-testid=todos-list-column]")?.className).toContain("md:w-[380px]");
    const normalizeReactIds = (html: string | undefined) => html?.replace(/_r_[0-9a-z]+_/g, "_r_ID_");
    const legacyHeader = host.querySelector("header")?.innerHTML;
    const legacyList = normalizeReactIds(host.querySelector("[data-testid=todos-list-column]")?.innerHTML);

    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todos: slimTodos, todosV2: false, selectedId: "personal_due" });
    expect(host.querySelector("header")?.innerHTML).toBe(legacyHeader);
    expect(normalizeReactIds(host.querySelector("[data-testid=todos-list-column]")?.innerHTML)).toBe(legacyList);
  });

  it("renders slim header with needs-you pill & Labs link, narrow single-row toolbar, 1–2 line rows preserving subtle priority & decisions, and folded parked/done sections", async () => {
    await render({
      todos: slimTodos,
      todosV2: true,
      leaders: ["dir-platform"],
      conversations: [{ machine_id: "m1", session_id: "dir-platform", title: "Platform" }],
    });

    // 1. De-duplicated header: no 5-part summary, concise needs-you pill, Labs · v2 link to Settings › Labs
    expect(host.querySelector("[data-testid=todos-summary]")).toBeNull();
    const attention = host.querySelector("[data-testid=todos-header-attention]");
    expect(attention?.getAttribute("data-count")).toBe("1");
    expect(attention?.textContent).toContain(t("todos.v2_header_needs_you", { count: 1 }));
    const labsLink = host.querySelector<HTMLAnchorElement>("[data-testid=todos-v2-labs-link]")!;
    expect(labsLink).toBeTruthy();
    expect(labsLink.getAttribute("href")).toBe("/settings?tab=labs");
    expect(labsLink.textContent).toContain(t("todos.v2_labs_badge"));

    // 2. Narrowed left column & single-row compact toolbar
    const listCol = host.querySelector<HTMLElement>("[data-testid=todos-list-column]")!;
    expect(listCol.className).toContain("md:w-[356px]");
    expect(listCol.className).toContain("lg:w-[368px]");
    const toolbar = host.querySelector("[data-testid=todos-v2-toolbar]")!;
    expect(toolbar).toBeTruthy();
    expect(toolbar.querySelector("[data-testid=todos-kind-filter]")).toBeTruthy();
    expect(toolbar.querySelector("[data-testid=todos-grouping-menu]")).toBeTruthy();
    expect(toolbar.querySelector("[data-testid=todos-filter]")).toBeTruthy();
    expect(toolbar.querySelector("[data-testid=todos-reorganize-toggle]")).toBeTruthy();

    // 3. Task rows: max 2 lines, no redundant status line, PR anchor, direction meta, subtle priority & pending decision preserved
    const workingRow = host.querySelector<HTMLElement>('[data-todo="task_pr_working"]')!;
    expect(workingRow.dataset.todoRowLayout).toBe("task-slim");
    expect(workingRow.querySelector("[data-testid=todo-status-line]")).toBeNull();
    expect(workingRow.querySelector("[data-testid=todo-slim-pr]")?.textContent).toBe("PR #14830");
    expect(workingRow.querySelector("[data-testid=todo-direction-tag]")?.textContent).toBe("Platform");
    expect(workingRow.querySelector("[data-testid=todo-priority-subtle]")?.textContent).toContain(t("todos.priority_option_high"));

    const decisionRow = host.querySelector<HTMLElement>('[data-todo="task_decision"]')!;
    expect(decisionRow.dataset.todoRowLayout).toBe("task-slim");
    expect(decisionRow.querySelector("[data-testid=todo-ask]")?.textContent).toContain("Proceed with blue-green swap?");
    expect(decisionRow.querySelector("[data-testid=todo-priority-subtle]")?.textContent).toContain(t("todos.priority_option_high"));

    // 4. Personal to-do: minimal single-line layout with due date & subtle priority
    const personalRow = host.querySelector<HTMLElement>('[data-todo="personal_due"]')!;
    expect(personalRow.dataset.todoRowLayout).toBe("personal-slim");
    expect(personalRow.querySelector("[data-testid=todo-slim-line2]")).toBeNull();
    expect(personalRow.querySelector("[data-testid=todo-due-compact]")?.textContent).toBeTruthy();
    expect(personalRow.querySelector("[data-testid=todo-priority-subtle]")?.textContent).toContain(t("todos.priority_option_high"));

    // 5. Parked & Done sections start compactly folded with counts and external wait hint
    const parkedSec = host.querySelector<HTMLElement>('[data-testid="todo-section"][data-section="parked"]')!;
    expect(parkedSec.querySelector("button[aria-expanded]")?.getAttribute("aria-expanded")).toBe("false");
    expect(parkedSec.getAttribute("data-todos")).toBe("1");
    expect(parkedSec.querySelector("[data-testid=todo-section-folded-hint]")?.textContent).toContain(t("todos.parked_folded_hint"));
    expect(parkedSec.querySelector('[data-todo="task_parked"]')).toBeNull();

    const doneSec = host.querySelector<HTMLElement>('[data-testid="todo-section"][data-section="done"]')!;
    expect(doneSec.querySelector("button[aria-expanded]")?.getAttribute("aria-expanded")).toBe("false");
    expect(doneSec.getAttribute("data-todos")).toBe("1");
    expect(doneSec.querySelector('[data-todo="task_recent_done"]')).toBeNull();

    // Expanding Parked reveals its compact row
    await act(async () => parkedSec.querySelector<HTMLButtonElement>("button[aria-expanded]")!.click());
    await flush();
    expect(parkedSec.querySelector('[data-todo="task_parked"]')).toBeTruthy();

    // Turning todos_v2 off immediately restores legacy header, width, and rows
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(host.querySelector("[data-testid=todos-v2-labs-link]")).toBeNull();
    expect(host.querySelector("[data-testid=todos-v2-toolbar]")).toBeNull();
    expect(host.querySelector("[data-testid=todos-summary]")).toBeTruthy();
    expect(host.querySelector("[data-testid=todos-list-column]")?.className).toContain("md:w-[380px]");
  });

  it("enforces a strict slim-row line-2 badge quota so the primary decision/wait cue is never starved (#17696)", async () => {
    const overloaded: Todo[] = [
      {
        id: "overloaded_goal",
        title: "Ship cross-repo release with many badges",
        direction_id: "dir-platform",
        project: "persona",
        pr_url: "https://github.com/nwverse/persona/pull/17696",
        spaces: [
          { machine_id: "m1", space: "core", linked_at: iso(2 * HOUR) },
          { machine_id: "m1", space: "web", linked_at: iso(2 * HOUR) },
        ],
        child_summary: { total: 4, open: 3, needs_you: 2, working: 1, done: 1 },
        repo_gate: {
          state: "blocked_ci",
          target_count: 3,
          ready_count: 2,
          reference_count: 0,
          blocking_repo: "nwverse/persona",
          reason: "ci_failed",
        },
        ask: { verdict: "Approve production rollout window?", asked_at: iso(1 * HOUR), outcome: "decide" },
        created_at: iso(3 * HOUR),
        updated_at: iso(1 * HOUR),
        view: view("needs_you", "owner_decides", "needs_you", 0),
      },
    ];
    api.mockImplementation(async (path: string) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/mergetrain") {
        return {
          repos: [],
          by_todo: {
            overloaded_goal: { repo_key: "nwverse/persona", position: 1, state: "escalated" },
          },
        };
      }
      return {};
    });

    await render({
      todos: overloaded,
      todosV2: true,
      leaders: ["dir-platform"],
      conversations: [{ machine_id: "m1", session_id: "dir-platform", title: "Platform" }],
    });

    const line2 = host.querySelector<HTMLElement>('[data-todo="overloaded_goal"] [data-testid="todo-slim-line2"]')!;
    expect(line2).toBeTruthy();
    // With a primary decision cue present, at most 1 highest-signal badge renders and lower-priority badges demote to detail
    expect(line2.getAttribute("data-badge-count")).toBe("1");
    expect(Number(line2.getAttribute("data-omitted-badges"))).toBeGreaterThanOrEqual(4);
    expect(line2.querySelector("[data-testid=todo-child-summary-needs-you]")).toBeTruthy();
    expect(line2.querySelector("[data-testid=todo-direction-tag]")).toBeNull();
    expect(line2.querySelector("[data-testid=todo-project-tag]")).toBeNull();
    expect(line2.querySelector("[data-testid=todo-row-repo-gate]")).toBeNull();
    expect(line2.querySelector("[data-testid=todo-ask]")?.textContent).toBe("Approve production rollout window?");
  });
});

describe("TodoDetail single-item Change direction control under todos_v2 (#15653)", () => {
  const dirTodos: Todo[] = [
    {
      id: "goal_main",
      title: "Ship direction wiring",
      direction_id: "dir-platform",
      created_at: iso(6 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 0),
    },
    {
      id: "sub_task",
      title: "Update direction picker tests",
      part_of: "goal_main",
      direction_id: "dir-platform",
      agents: [{ machine_id: "m1", session_id: "s-run", linked_at: iso(2 * HOUR) }],
      created_at: iso(4 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 1),
    },
  ];

  const leaders = ["dir-platform", "dir-growth"];
  const conversations = [
    { machine_id: "m1", session_id: "dir-platform", title: "Platform" },
    { machine_id: "m1", session_id: "dir-growth", title: "Growth" },
  ];

  it("renders no Change direction button when todos_v2 is false or omitted", async () => {
    await render({ todos: dirTodos, todosV2: undefined, selectedId: "goal_main", leaders, conversations });
    expect(host.querySelector("[data-testid=todo-detail-change-direction]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-direction-panel]")).toBeNull();
    const legacyDetail = host.querySelector("[data-testid=todo-detail]")?.innerHTML;

    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todos: dirTodos, todosV2: false, selectedId: "goal_main", leaders, conversations });
    expect(host.querySelector("[data-testid=todo-detail-change-direction]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail]")?.innerHTML).toBe(legacyDetail);
  });

  it("submits POST /api/v1/todos/reorganize with transfer_supervision=false by default, optional include_children, and refreshes immediately", async () => {
    let currentTodos = dirTodos;
    api.mockImplementation(async (path: string, init?: unknown) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/reorganize") {
        const body = (init as { json?: { direction_id?: string; include_children?: boolean } })?.json;
        currentTodos = currentTodos.map((td) =>
          td.id === "goal_main" || (body?.include_children && td.part_of === "goal_main")
            ? { ...td, direction_id: body?.direction_id || undefined }
            : td,
        );
        return { updated: currentTodos };
      }
      if (path === "/todos") return { todos: currentTodos };
      return {};
    });

    await render({ todos: dirTodos, todosV2: true, selectedId: "goal_main", leaders, conversations });

    const trigger = host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-change-direction]")!;
    expect(trigger).toBeTruthy();
    expect(trigger.textContent).toContain(t("todos.detail_change_direction"));
    expect(host.querySelector("[data-testid=todo-detail-direction-panel]")).toBeNull();

    // Open picker panel
    await act(async () => trigger.click());
    await flush();
    expect(host.querySelector("[data-testid=todo-detail-direction-panel]")).toBeTruthy();

    // Parent goal has 1 child -> include_children checkbox is shown and defaults to true (#16138)
    const includeBox = host.querySelector<HTMLInputElement>("[data-testid=todo-direction-include-children]")!;
    expect(includeBox).toBeTruthy();
    expect(includeBox.checked).toBe(true);
    expect(includeBox.closest("label")?.textContent).toContain(t("todos.direction_include_children", { count: 1 }));

    // Apply disabled while selection equals current direction
    const applyBtn = host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-direction-apply]")!;
    expect(applyBtn.disabled).toBe(true);

    // Select another active direction -> transfer_supervision appears and defaults to false
    const select = host.querySelector<HTMLSelectElement>("[data-testid=todo-detail-direction-select]")!;
    await act(async () => {
      select.value = "dir-growth";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();

    const transferBox = host.querySelector<HTMLInputElement>("[data-testid=todo-detail-direction-transfer-supervision]")!;
    expect(transferBox).toBeTruthy();
    expect(transferBox.checked).toBe(false);
    expect(applyBtn.disabled).toBe(false);

    // Submit with default include_children=true and transfer_supervision=false
    await act(async () => applyBtn.click());
    await flush();

    expect(api).toHaveBeenCalledWith(
      "/todos/reorganize",
      expect.objectContaining({
        method: "POST",
        json: {
          ids: ["goal_main"],
          direction_id: "dir-growth",
          include_children: true,
          transfer_supervision: false,
        },
      }),
    );
    expect(api).toHaveBeenCalledWith("/todos", expect.anything());
    // Panel closes and updated direction name renders immediately
    expect(host.querySelector("[data-testid=todo-detail-direction-panel]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-direction]")?.textContent).toContain("Growth");

    // Re-open and uncheck include_children -> sends include_children=false to move only the parent
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-change-direction]")!.click());
    await flush();
    const includeBox2 = host.querySelector<HTMLInputElement>("[data-testid=todo-direction-include-children]")!;
    expect(includeBox2.checked).toBe(true);
    await act(async () => includeBox2.click());
    await flush();
    expect(includeBox2.checked).toBe(false);

    const select2 = host.querySelector<HTMLSelectElement>("[data-testid=todo-detail-direction-select]")!;
    await act(async () => {
      select2.value = "dir-platform";
      select2.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();

    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-direction-apply]")!.click());
    await flush();

    expect(api).toHaveBeenCalledWith(
      "/todos/reorganize",
      expect.objectContaining({
        method: "POST",
        json: {
          ids: ["goal_main"],
          direction_id: "dir-platform",
          include_children: false,
          transfer_supervision: false,
        },
      }),
    );
  });

  it("hides include_children on leaf tasks, shows inline error on failure, and unmounts immediately when todos_v2 turns off", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/reorganize") throw new Error("reorganize denied");
      return {};
    });

    await render({ todos: dirTodos, todosV2: true, selectedId: "sub_task", leaders, conversations });

    const trigger = host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-change-direction]")!;
    await act(async () => trigger.click());
    await flush();

    // Leaf task has no children -> include_children is omitted
    expect(host.querySelector("[data-testid=todo-direction-include-children]")).toBeNull();

    // Choose "Remove from direction" -> transfer_supervision hides
    const select = host.querySelector<HTMLSelectElement>("[data-testid=todo-detail-direction-select]")!;
    await act(async () => {
      select.value = "";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    expect(host.querySelector("[data-testid=todo-detail-direction-transfer-supervision]")).toBeNull();

    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-direction-apply]")!.click());
    await flush();

    // Inline error shown, panel stays open
    expect(host.querySelector("[data-testid=todo-detail-direction-error]")?.textContent).toContain("reorganize denied");
    expect(host.querySelector("[data-testid=todo-detail-direction-panel]")).toBeTruthy();

    // Switching todos_v2 off removes both trigger and panel immediately
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(host.querySelector("[data-testid=todo-detail-change-direction]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-direction-panel]")).toBeNull();
  });
});

describe("TodosView server-owned board projection consumption (#15800)", () => {
  const boardTodos: Todo[] = [
    {
      id: "order_second",
      title: "Second by client timestamp",
      created_at: iso(20 * HOUR),
      updated_at: iso(20 * HOUR),
      view: view("needs_you", "owner_decides", "needs_you", 2),
    },
    {
      id: "order_first",
      title: "First by client timestamp",
      created_at: iso(1 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("needs_you", "owner_decides", "needs_you", 0),
    },
    {
      id: "goal_parent",
      title: "Launch Q4 Milestone",
      direction_id: "dir-platform",
      created_at: iso(5 * HOUR),
      updated_at: iso(2 * HOUR),
      view: view("working", "agent_running", "working", 0),
    },
    {
      id: "goal_child_folded",
      title: "Hidden folded subtask",
      part_of: "goal_parent",
      direction_id: "dir-platform",
      created_at: iso(4 * HOUR),
      updated_at: iso(2 * HOUR),
      view: view("working", "agent_running", "working", 1),
    },
    {
      id: "goal_child_surfaced",
      title: "Blocked child needing approval",
      part_of: "goal_parent",
      direction_id: "dir-platform",
      created_at: iso(3 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("needs_you", "owner_decides", "needs_you", 1),
    },
  ];

  // Deliberately order `order_second` before `order_first` and give `counts.needs_you = 9`
  // so the test proves TodosView renders directly from server `board` instead of re-deriving locally.
  const serverBoard: TodoBoardProjection = {
    status_groups: [
      {
        key: "needs_you",
        total_count: 3,
        visible_count: 3,
        entries: [
          { todo_id: "order_second" },
          { todo_id: "goal_child_surfaced" },
          { todo_id: "order_first" },
        ],
      },
      {
        key: "in_progress",
        total_count: 2,
        visible_count: 1,
        entries: [
          {
            todo_id: "goal_parent",
            folded_child_ids: ["goal_child_folded"],
            surfaced_child_ids: ["goal_child_surfaced"],
            folded_count: 1,
          },
        ],
      },
    ],
    direction_groups: [
      {
        direction_id: "dir-platform",
        label: "Platform Server Label",
        counts: { needs_you: 1, in_progress: 2, pending_review: 0, waiting: 0, done: 0, total: 3 },
        todo_ids: ["goal_child_surfaced", "goal_parent", "goal_child_folded"],
      },
      {
        direction_id: "",
        counts: { needs_you: 2, in_progress: 0, pending_review: 0, waiting: 0, done: 0, total: 2 },
        todo_ids: ["order_second", "order_first"],
      },
    ],
    sidebar_tree: [],
    counts: {
      needs_you: 9,
      in_progress: 2,
      pending_review: 0,
      waiting: 0,
      someday: 0,
      done: 0,
      open_total: 11,
    },
  };

  it("strictly renders status sections, goal folding, surfaced blockers, and header count from server board under todos_v2=true", async () => {
    await render({ todos: boardTodos, todoBoard: serverBoard, todosV2: true, grouping: "status" });

    const sections = host.querySelector("[data-testid=todos-sections]")!;
    expect(sections.getAttribute("data-todo-source")).toBe("board");

    // Header attention count reads directly from board.counts.needs_you (9)
    expect(host.querySelector("[data-testid=todos-header-attention]")?.getAttribute("data-count")).toBe("9");

    // Waiting (needs_you) section renders in exact server order: order_second, goal_child_surfaced, order_first
    const waitingSection = host.querySelector('[data-testid=todo-section][data-section="waiting"]')!;
    const waitingIds = [...waitingSection.querySelectorAll("[data-todo]")].map((el) => el.getAttribute("data-todo"));
    expect(waitingIds).toEqual(["order_second", "goal_child_surfaced", "order_first"]);

    // Working (in_progress) section keeps goal_parent visible and folds goal_child_folded
    const workingSection = host.querySelector('[data-testid=todo-section][data-section="working"]')!;
    const workingIds = [...workingSection.querySelectorAll("[data-todo]")].map((el) => el.getAttribute("data-todo"));
    expect(workingIds).toEqual(["goal_parent"]);
    expect(workingSection.textContent).not.toContain("Hidden folded subtask");
  });

  it("renders direction grouping directly from server board.direction_groups under todos_v2=true", async () => {
    await render({ todos: boardTodos, todoBoard: serverBoard, todosV2: true, grouping: "direction" });

    const groups = [...host.querySelectorAll("[data-testid=todo-group]")];
    expect(groups.map((el) => el.getAttribute("data-group"))).toEqual([
      "direction:dir-platform",
      "direction:secretary",
    ]);
    expect(groups[0]!.textContent).toContain("Platform Server Label");
    // Under #16939, same-direction children nest under their parent goal (expanded by default),
    // preserving the children's relative server order.
    const platformIds = [...groups[0]!.querySelectorAll("[data-todo]")].map((el) => el.getAttribute("data-todo"));
    expect(platformIds).toEqual(["goal_parent", "goal_child_surfaced", "goal_child_folded"]);
    const nestedChildren = groups[0]!.querySelector('[data-testid="todo-goal-children"][data-parent="goal_parent"]')!;
    expect(nestedChildren).toBeTruthy();
    expect([...nestedChildren.querySelectorAll("[data-todo]")].map((el) => el.getAttribute("data-todo"))).toEqual([
      "goal_child_surfaced",
      "goal_child_folded",
    ]);
  });

  it("ignores server board completely when todos_v2 is false (100% legacy behavior)", async () => {
    await render({ todos: boardTodos, todoBoard: serverBoard, todosV2: false, grouping: "status" });

    const sections = host.querySelector("[data-testid=todos-sections]")!;
    expect(sections.getAttribute("data-todo-source")).not.toBe("board");
    expect(host.querySelector("[data-testid=todos-header-attention]")).toBeNull();
    expect(host.querySelector("[data-testid=todos-summary]")).not.toBeNull();
    // Legacy path keeps folded child unfolded at top level in working
    const workingSection = host.querySelector('[data-testid=todo-section][data-section="working"]')!;
    expect(workingSection.textContent).toContain("Hidden folded subtask");
  });

  it("falls back cleanly to client goal folding when todos_v2=true and server omits board (older Hub)", async () => {
    await render({ todos: boardTodos, todoBoard: null, todosV2: true, grouping: "status" });

    const sections = host.querySelector("[data-testid=todos-sections]")!;
    expect(sections.getAttribute("data-todo-source")).toBe("view");
    // Client fallback still folds goal_child_folded under goal_parent
    const workingSection = host.querySelector('[data-testid=todo-section][data-section="working"]')!;
    expect(workingSection.textContent).not.toContain("Hidden folded subtask");
  });
});

describe("TodosView detail parent picker, item_type filter & toggle, and undo auto-seen (#16100)", () => {
  const uxTodos: Todo[] = [
    {
      id: "top_goal",
      title: "Launch v2 architecture",
      item_type: "task",
      origin: "conv-main",
      created_at: iso(6 * HOUR),
      updated_at: iso(2 * HOUR),
      view: view("working", "agent_running", "working", 0),
    },
    {
      id: "other_goal",
      title: "Q4 roadmap",
      item_type: "task",
      origin: "conv-main",
      created_at: iso(5 * HOUR),
      updated_at: iso(2 * HOUR),
      view: view("todo", "rest", "todo", 1),
    },
    {
      id: "nested_sub",
      title: "Wire parent picker",
      item_type: "task",
      part_of: "top_goal",
      origin: "conv-main",
      created_at: iso(4 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 1),
    },
    {
      id: "personal_note",
      title: "Renew passport",
      item_type: "personal",
      created_at: iso(3 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("todo", "rest", "todo", 2),
    },
    {
      id: "auto_seen_task",
      title: "Nightly docs sync",
      item_type: "task",
      audience: "autonomous",
      done_at: iso(2 * HOUR),
      done_by: "auto",
      seen_at: iso(2 * HOUR),
      created_at: iso(8 * HOUR),
      updated_at: iso(2 * HOUR),
      view: view("done", "closed", "done", 0),
    },
  ];

  it("renders none of the #16100 controls when todos_v2 is false or omitted", async () => {
    await render({ todos: uxTodos, todosV2: false, selectedId: "auto_seen_task" });
    expect(host.querySelector("[data-testid=todo-detail-change-parent]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-parent-panel]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-detail-item-type]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-unsee-btn]")).toBeNull();
    expect(kindFilterEl()).toBeNull();
  });

  it("re-parents and promotes a single todo via TodoParentPicker without interrupting running Agents", async () => {
    let currentTodos = uxTodos;
    api.mockImplementation(async (path: string, init?: unknown) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/reorganize") {
        const body = (init as { json?: { ids: string[]; part_of?: string } })?.json;
        currentTodos = currentTodos.map((td) =>
          body?.ids.includes(td.id) ? { ...td, part_of: body.part_of || undefined } : td,
        );
        return { updated: currentTodos.filter((td) => body?.ids.includes(td.id)) };
      }
      if (path === "/todos") return { todos: currentTodos };
      return {};
    });

    await render({ todos: uxTodos, todosV2: true, selectedId: "top_goal" });

    // Open parent picker on top_goal: excludes self (top_goal), descendant (nested_sub), and done (auto_seen_task)
    const trigger = host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-change-parent]")!;
    expect(trigger).toBeTruthy();
    await act(async () => trigger.click());
    await flush();

    const select = host.querySelector<HTMLSelectElement>("[data-testid=todo-detail-parent-select]")!;
    const optionValues = [...select.options].map((o) => o.value);
    expect(optionValues).toEqual(["", "other_goal", "personal_note"]);

    // Re-parent top_goal under other_goal
    await act(async () => {
      select.value = "other_goal";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();

    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-parent-apply]")!.click());
    await flush();

    expect(api).toHaveBeenCalledWith(
      "/todos/reorganize",
      expect.objectContaining({
        method: "POST",
        json: {
          ids: ["top_goal"],
          part_of: "other_goal",
          include_children: false,
          transfer_supervision: false,
        },
      }),
    );
    expect(api).toHaveBeenCalledWith("/todos", expect.anything());
    expect(host.querySelector("[data-testid=todo-detail-parent-panel]")).toBeNull();

    // Now open parent picker again and promote top_goal back to top-level in one click
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-change-parent]")!.click());
    await flush();
    const promoteBtn = host.querySelector<HTMLButtonElement>("[data-testid=todo-detail-parent-promote]")!;
    expect(promoteBtn).toBeTruthy();
    await act(async () => promoteBtn.click());
    await flush();

    expect(api).toHaveBeenCalledWith(
      "/todos/reorganize",
      expect.objectContaining({
        method: "POST",
        json: {
          ids: ["top_goal"],
          part_of: "",
          include_children: false,
          transfer_supervision: false,
        },
      }),
    );

    // Turning todos_v2 off removes the parent picker button immediately
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(host.querySelector("[data-testid=todo-detail-change-parent]")).toBeNull();
  });

  it("filters by server item_type under todos_v2 and toggles personal/task in detail via PATCH /todos/{id}", async () => {
    let currentTodos = uxTodos;
    api.mockImplementation(async (path: string, init?: unknown) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/personal_note" && (init as { method?: string })?.method === "PATCH") {
        const body = (init as { json?: { item_type?: "personal" | "task" } })?.json;
        currentTodos = currentTodos.map((td) =>
          td.id === "personal_note" ? { ...td, item_type: body?.item_type } : td,
        );
        return { todo: currentTodos.find((td) => td.id === "personal_note") };
      }
      if (path === "/todos") return { todos: currentTodos };
      return {};
    });

    await render({ todos: uxTodos, todosV2: true, selectedId: "personal_note" });

    // Filter to personal only -> shows only personal_note
    await clickKind(t("todos.kind_filter_todos"));
    expect(visibleTodoIds()).toEqual(["personal_note"]);

    // Filter to tasks only -> excludes personal_note
    await clickKind(t("todos.kind_filter_tasks"));
    expect(visibleTodoIds()).not.toContain("personal_note");
    expect(visibleTodoIds()).toContain("top_goal");

    // Back to All
    await clickKind(t("todos.kind_filter_all"));

    // Detail item_type switcher starts on "personal"
    const typeGroup = host.querySelector("[data-testid=todo-detail-item-type]")!;
    expect(typeGroup).toBeTruthy();
    const personalBtn = typeGroup.querySelector<HTMLButtonElement>('[data-item-type="personal"]')!;
    const taskBtn = typeGroup.querySelector<HTMLButtonElement>('[data-item-type="task"]')!;
    expect(personalBtn.getAttribute("aria-checked")).toBe("true");
    expect(taskBtn.getAttribute("aria-checked")).toBe("false");
    expect(host.querySelector("[data-testid=todo-detail-template-badge]")).toBeNull();

    // Toggle to "task" -> calls PATCH /todos/personal_note with { item_type: "task" } and refreshes
    await act(async () => taskBtn.click());
    await flush();

    expect(api).toHaveBeenCalledWith(
      "/todos/personal_note",
      expect.objectContaining({
        method: "PATCH",
        json: { item_type: "task" },
      }),
    );
    expect(api).toHaveBeenCalledWith("/todos", expect.anything());
    expect(host.querySelector('[data-testid=todo-detail-item-type] [data-item-type="task"]')?.getAttribute("aria-checked")).toBe("true");
    expect(host.querySelector("[data-testid=todo-detail-template-badge]")?.textContent).toBe(t("todos.detail_template_work_goal"));
  });

  it("exposes Mark unread button on seen/auto-seen todos under todos_v2 and calls DELETE /todos/{id}/seen", async () => {
    let currentTodos = uxTodos;
    api.mockImplementation(async (path: string, init?: unknown) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/auto_seen_task/seen" && (init as { method?: string })?.method === "DELETE") {
        currentTodos = currentTodos.map((td) =>
          td.id === "auto_seen_task" ? { ...td, seen_at: null } : td,
        );
        return { todo: currentTodos.find((td) => td.id === "auto_seen_task") };
      }
      if (path === "/todos") return { todos: currentTodos };
      return {};
    });

    // Unseen open todo does not show Mark unread button
    await render({ todos: uxTodos, todosV2: true, selectedId: "personal_note" });
    expect(host.querySelector("[data-testid=todo-unsee-btn]")).toBeNull();

    // Auto-seen completed autonomous task shows Mark unread button
    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todos: uxTodos, todosV2: true, selectedId: "auto_seen_task" });

    const unseeBtn = host.querySelector<HTMLButtonElement>("[data-testid=todo-unsee-btn]")!;
    expect(unseeBtn).toBeTruthy();
    expect(unseeBtn.textContent).toContain(t("todos.detail_mark_unread"));

    await act(async () => unseeBtn.click());
    await flush();

    expect(api).toHaveBeenCalledWith(
      "/todos/auto_seen_task/seen",
      expect.objectContaining({ method: "DELETE" }),
    );
    expect(api).toHaveBeenCalledWith("/todos", expect.anything());

    // Turning todos_v2 off hides the button immediately
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(host.querySelector("[data-testid=todo-unsee-btn]")).toBeNull();
  });
});

describe("TodosView detail active_blockers strip under todos_v2 (#16138)", () => {
  const blockedTodos: Todo[] = [
    {
      id: "blocked_task",
      title: "Land v2 schema migration",
      origin: "conv-main",
      created_at: iso(4 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("parked", "external_wait", "parked", 0),
      active_blockers: [
        {
          id: "blk_pr",
          kind: "external_pr",
          target_ref: "https://github.com/nwverse/persona/pull/16135",
          state: "active",
        },
        {
          id: "blk_merge",
          kind: "merge_train",
          target_ref: "nwverse/persona",
          state: "active",
          attempts: 2,
        },
        {
          id: "blk_todo",
          kind: "todo_done",
          target_ref: "todo_prereq_1",
          state: "active",
        },
      ],
    },
    {
      id: "unblocked_task",
      title: "Clean task without blockers",
      origin: "conv-main",
      created_at: iso(3 * HOUR),
      updated_at: iso(1 * HOUR),
      view: view("working", "agent_running", "working", 0),
      active_blockers: [],
    },
  ];

  it("hides active_blockers when todos_v2 is false or omitted", async () => {
    await render({ todos: blockedTodos, todosV2: undefined, selectedId: "blocked_task" });
    expect(host.querySelector("[data-testid=todo-active-blockers]")).toBeNull();

    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todos: blockedTodos, todosV2: false, selectedId: "blocked_task" });
    expect(host.querySelector("[data-testid=todo-active-blockers]")).toBeNull();
  });

  it("hides active_blockers under todos_v2=true when the list is empty or absent", async () => {
    await render({ todos: blockedTodos, todosV2: true, selectedId: "unblocked_task" });
    expect(host.querySelector("[data-testid=todo-active-blockers]")).toBeNull();
  });

  it("renders external PR clickable link, localized kind labels, and retry count under todos_v2=true, and hides on rollback", async () => {
    await render({ todos: blockedTodos, todosV2: true, selectedId: "blocked_task" });

    const strip = host.querySelector("[data-testid=todo-active-blockers]")!;
    expect(strip).toBeTruthy();
    expect(strip.textContent).toContain(t("todos.active_blockers_title", { count: 3 }));

    const items = [...strip.querySelectorAll<HTMLElement>("[data-testid=todo-active-blocker-item]")];
    expect(items).toHaveLength(3);

    // 1. external_pr -> clickable https:// PR link with short label
    expect(items[0].querySelector("[data-testid=todo-active-blocker-kind]")?.textContent).toBe(
      t("todos.active_blocker_kind_external_pr"),
    );
    const prAnchor = items[0].querySelector<HTMLAnchorElement>("[data-testid=todo-active-blocker-link]")!;
    expect(prAnchor).toBeTruthy();
    expect(prAnchor.getAttribute("href")).toBe("https://github.com/nwverse/persona/pull/16135");
    expect(prAnchor.getAttribute("target")).toBe("_blank");
    expect(prAnchor.getAttribute("rel")).toBe("noreferrer noopener");
    expect(prAnchor.textContent).toContain("nwverse/persona#16135");
    expect(items[0].querySelector("[data-testid=todo-active-blocker-attempts]")).toBeNull();

    // 2. merge_train with attempts=2 -> plain target + retry badge
    expect(items[1].querySelector("[data-testid=todo-active-blocker-kind]")?.textContent).toBe(
      t("todos.active_blocker_kind_merge_train"),
    );
    expect(items[1].querySelector("[data-testid=todo-active-blocker-target]")?.textContent).toBe("nwverse/persona");
    expect(items[1].querySelector("[data-testid=todo-active-blocker-attempts]")?.textContent).toBe(
      t("todos.active_blocker_attempts", { count: 2 }),
    );

    // 3. todo_done -> plain target without retry badge
    expect(items[2].querySelector("[data-testid=todo-active-blocker-kind]")?.textContent).toBe(
      t("todos.active_blocker_kind_todo_done"),
    );
    expect(items[2].querySelector("[data-testid=todo-active-blocker-target]")?.textContent).toBe("todo_prereq_1");
    expect(items[2].querySelector("[data-testid=todo-active-blocker-attempts]")).toBeNull();

    // Turning todos_v2 off hides the strip immediately
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(host.querySelector("[data-testid=todo-active-blockers]")).toBeNull();
  });

  it("renders no manual blocker add/cancel controls when todos_v2 is false or omitted", async () => {
    await render({ todos: blockedTodos, todosV2: undefined, selectedId: "unblocked_task" });
    expect(host.querySelector("[data-testid=todo-add-pr-blocker-btn]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-add-pr-blocker-input]")).toBeNull();

    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todos: blockedTodos, todosV2: false, selectedId: "blocked_task" });
    expect(host.querySelector("[data-testid=todo-add-pr-blocker-btn]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-cancel-blocker-blk_pr]")).toBeNull();
  });

  it("validates https:// URL, submits POST /todos/{id}/blockers, handles failure, and hides add trigger on done to-dos under todos_v2 (#16199)", async () => {
    let currentTodos: Todo[] = [
      ...blockedTodos,
      {
        id: "closed_task",
        title: "Already finished",
        created_at: iso(5 * HOUR),
        updated_at: iso(2 * HOUR),
        done_at: iso(1 * HOUR),
        view: view("done", "closed", "done", 0),
      },
    ];
    let failNextPost = true;
    api.mockImplementation(async (path: string, init?: unknown) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/unblocked_task/blockers" && (init as { method?: string })?.method === "POST") {
        if (failNextPost) {
          failNextPost = false;
          throw new Error("duplicate blocker");
        }
        const body = (init as { json?: { kind: string; target_ref: string } })?.json;
        const created = { id: "blk_added_1", kind: body!.kind, target_ref: body!.target_ref, state: "active" };
        currentTodos = currentTodos.map((td) =>
          td.id === "unblocked_task" ? { ...td, active_blockers: [...(td.active_blockers ?? []), created] } : td,
        );
        return { blocker: created };
      }
      if (path === "/todos") return { todos: currentTodos };
      return {};
    });

    const setInputValue = async (input: HTMLInputElement, next: string) => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      await act(async () => {
        setter.call(input, next);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };

    await render({ todos: currentTodos, todosV2: true, selectedId: "unblocked_task" });
    // Empty blockers still hides the strip, while open to-do exposes the compact "+ Wait on PR" button
    expect(host.querySelector("[data-testid=todo-active-blockers]")).toBeNull();
    const addBtn = host.querySelector<HTMLButtonElement>("[data-testid=todo-add-pr-blocker-btn]")!;
    expect(addBtn).toBeTruthy();

    await act(async () => addBtn.click());
    await flush();
    const input = host.querySelector<HTMLInputElement>("[data-testid=todo-add-pr-blocker-input]")!;
    expect(input).toBeTruthy();

    // Non-https URL is rejected client-side without hitting API
    await setInputValue(input, "http://github.com/nwverse/persona/pull/16199");
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=todo-add-pr-blocker-submit]")!.click());
    await flush();
    expect(host.querySelector("[data-testid=todo-add-pr-blocker-error]")?.textContent).toBe(
      t("todos.active_blocker_pr_url_invalid"),
    );
    expect(api).not.toHaveBeenCalledWith("/todos/unblocked_task/blockers", expect.anything());

    // Valid https:// URL -> server error shows message and preserves input
    await setInputValue(input, "https://github.com/nwverse/persona/pull/16199");
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=todo-add-pr-blocker-submit]")!.click());
    await flush();
    expect(host.querySelector("[data-testid=todo-add-pr-blocker-error]")?.textContent).toContain("duplicate blocker");
    expect(host.querySelector<HTMLInputElement>("[data-testid=todo-add-pr-blocker-input]")?.value).toBe(
      "https://github.com/nwverse/persona/pull/16199",
    );

    // Retry succeeds -> POSTs exact payload, refreshes /todos, closes form, renders new blocker
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=todo-add-pr-blocker-submit]")!.click());
    await flush();
    expect(api).toHaveBeenCalledWith(
      "/todos/unblocked_task/blockers",
      expect.objectContaining({
        method: "POST",
        json: { kind: "external_pr", target_ref: "https://github.com/nwverse/persona/pull/16199" },
      }),
    );
    expect(api).toHaveBeenCalledWith("/todos", expect.anything());
    expect(host.querySelector("[data-testid=todo-add-pr-blocker-input]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-active-blockers]")).toBeTruthy();
    expect(host.querySelector("[data-testid=todo-cancel-blocker-blk_added_1]")).toBeTruthy();

    // Closed to-do does not show "+ Wait on PR"
    await act(async () => root.unmount());
    root = createRoot(host);
    await render({ todos: currentTodos, todosV2: true, selectedId: "closed_task" });
    expect(host.querySelector("[data-testid=todo-add-pr-blocker-btn]")).toBeNull();
  });

  it("cancels an active blocker in one click via DELETE /todos/{id}/blockers/{blockerId} and refreshes todos (#16199)", async () => {
    let currentTodos: Todo[] = blockedTodos.map((td) => ({
      ...td,
      active_blockers: td.active_blockers ? [...td.active_blockers] : undefined,
    }));
    api.mockImplementation(async (path: string, init?: unknown) => {
      if (path.includes("/kb")) return { items: kbProjects };
      if (path === "/todos/blocked_task/blockers/blk_pr" && (init as { method?: string })?.method === "DELETE") {
        currentTodos = currentTodos.map((td) =>
          td.id === "blocked_task"
            ? { ...td, active_blockers: (td.active_blockers ?? []).filter((b) => b.id !== "blk_pr") }
            : td,
        );
        return { ok: true };
      }
      if (path === "/todos") return { todos: currentTodos };
      return {};
    });

    await render({ todos: currentTodos, todosV2: true, selectedId: "blocked_task" });
    // Only one "+ Wait on PR" button (rendered inside the strip header)
    expect(host.querySelectorAll("[data-testid=todo-add-pr-blocker-btn]")).toHaveLength(1);
    const cancelBtn = host.querySelector<HTMLButtonElement>("[data-testid=todo-cancel-blocker-blk_pr]")!;
    expect(cancelBtn).toBeTruthy();

    await act(async () => cancelBtn.click());
    await flush();

    expect(api).toHaveBeenCalledWith(
      "/todos/blocked_task/blockers/blk_pr",
      expect.objectContaining({ method: "DELETE" }),
    );
    expect(api).toHaveBeenCalledWith("/todos", expect.anything());
    expect(host.querySelector("[data-testid=todo-cancel-blocker-blk_pr]")).toBeNull();
    expect(host.querySelectorAll("[data-testid=todo-active-blocker-item]")).toHaveLength(2);

    // Turning todos_v2 off hides all blocker controls immediately
    await act(async () => patchAssistant({ todos_v2: false }));
    await flush();
    expect(host.querySelector("[data-testid=todo-active-blockers]")).toBeNull();
    expect(host.querySelector("[data-testid=todo-add-pr-blocker-btn]")).toBeNull();
  });
});
