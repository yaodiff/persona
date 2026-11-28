// @vitest-environment jsdom
// Pure Web thin-consumer UI tests for #16433: parent child_summary badge, merge-train queue position badge,
// lazy causal audit trace disclosure, and strict todos_v2=false zero-fetch / zero-render guard.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";
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
const { getState, patchAssistant, putTodo, reset } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const NOW = "2026-11-26T12:00:00.000Z";
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

const sampleTodos: Todo[] = [
  {
    id: "goal_parent",
    title: "Ship v2 workflow",
    item_type: "task",
    created_at: NOW,
    updated_at: NOW,
    view: view("working", "agent_running", "working", 0),
    child_summary: {
      total: 4,
      open: 2,
      needs_you: 1,
      working: 1,
      done: 2,
    },
  },
  {
    id: "pr_task",
    title: "Wire merge train badge",
    item_type: "task",
    pr_url: "https://github.com/org/repo/pull/202",
    created_at: NOW,
    updated_at: NOW,
    view: view("working", "agent_running", "working", 1),
  },
];

beforeEach(async () => {
  await initI18n("en");
  apiMock.mockReset();
  reset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  reset();
});

describe("TodosView todos_v2 thin-consumer wiring (#16433)", () => {
  it("hides the PR status from a cached target repo gate when the Lab is off", async () => {
    const gatedTodo = {
      id: "gated", title: "Release work", created_at: NOW, updated_at: NOW,
      repo_gate: { state: "blocked_conflict", target_count: 1, ready_count: 0, reference_count: 0 },
    };
    seed({ assistant: { machine_id: "hub", conversations: [], todos_v2: false }, todos: [gatedTodo] });
    await act(async () => root.render(<TodosView selectedId={gatedTodo.id} />));
    await flush();
    const conflict = t("todos.status_pr_conflicts");
    expect(host.textContent).not.toContain(conflict);
    await act(async () => patchAssistant({ todos_v2: true }));
    expect(host.textContent).toContain(conflict);
    await act(async () => patchAssistant({ todos_v2: false }));
    expect(host.textContent).not.toContain(conflict);
  });

  it("skips merge-train reads without an open PR to-do", async () => {
    seed({
      assistant: { machine_id: "hub", todos_v2: true },
      todos: [sampleTodos[0], { ...sampleTodos[1], done_at: NOW }],
    });
    await act(async () => root.render(<TodosView />));
    await flush();
    expect(apiMock.mock.calls.filter(([path]) => path === "/todos/mergetrain")).toHaveLength(0);
  });

  it("refreshes merge-train badges only when PR inputs change and keeps the last response on error", async () => {
    const [goal, todo] = sampleTodos;
    const pr = {
      ...todo,
      pr_status: {
        url: todo.pr_url!, state: "open" as const, draft: false, review: "approved" as const,
        checks: "pending" as const, conflicts: false, checked_at: NOW,
      },
    };
    const badge = () => host.querySelector('[data-todo="pr_task"] [data-testid="todo-mergetrain-badge"]');
    const reads = () => apiMock.mock.calls.filter(([path]) => path === "/todos/mergetrain");
    let fail = false;
    apiMock.mockImplementation(async (path) => {
      if (path !== "/todos/mergetrain") return {};
      if (fail) throw new Error("Temporary read failure");
      return { by_todo: { pr_task: { repo_key: "org/repo", position: 2, state: "queued" } } };
    });
    seed({ assistant: { machine_id: "hub", todos_v2: true }, todos: [goal, pr] });
    await act(async () => root.render(<TodosView />));
    await flush();
    expect(reads()).toHaveLength(1);
    expect(badge()?.textContent).toContain("#2");

    await act(async () => {
      putTodo({ ...goal, title: "Edited goal", updated_at: "2026-11-26T13:00:00Z" });
      putTodo({
        ...pr,
        notes: "Updated description",
        updated_at: "2026-11-26T13:00:00Z",
        pr_status: { ...pr.pr_status, checked_at: "2026-11-26T13:00:00Z" },
      });
    });
    expect(reads()).toHaveLength(1);

    fail = true;
    await act(async () => putTodo({ ...pr, pr_status: { ...pr.pr_status, checks: "success" } }));
    await flush();
    expect(reads()).toHaveLength(2);
    expect(badge()?.textContent).toContain("#2");
  });

  it("renders nothing new and fires ZERO requests to v2 endpoints when todos_v2 is false", async () => {
    seed({
      connected: true,
      loaded: true,
      assistant: { machine_id: "hub", todos_v2: false },
      todos: sampleTodos,
      machines: [],
      sessions: [],
      interactions: [],
    });

    await act(async () => {
      root.render(<TodosView selectedId="goal_parent" />);
    });
    await flush();

    const calledPaths = apiMock.mock.calls.map(([p]) => String(p));
    expect(calledPaths.some((p) => p.includes("/todos/mergetrain"))).toBe(false);
    expect(calledPaths.some((p) => p.includes("/todos/decisions/groups"))).toBe(false);
    expect(calledPaths.some((p) => p.includes("/trace"))).toBe(false);

    expect(host.querySelector('[data-testid="todo-child-summary-badge"]')).toBeNull();
    expect(host.querySelector('[data-testid="todo-detail-child-summary"]')).toBeNull();
    expect(host.querySelector('[data-testid="todo-mergetrain-badge"]')).toBeNull();
    expect(host.querySelector('[data-testid="todo-detail-mergetrain"]')).toBeNull();
    expect(host.querySelector('[data-testid="todo-causal-trace"]')).toBeNull();
  });

  it("displays child_summary, merge-train queue position badges, and lazy causal trace under todos_v2=true", async () => {
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/todos/mergetrain") {
        return {
          repos: [],
          by_todo: {
            pr_task: {
              repo_key: "org/repo",
              position: 2,
              ahead_pr_url: "https://github.com/org/repo/pull/201",
              state: "queued",
            },
          },
        };
      }
      if (path === "/todos/pr_task/trace") {
        return {
          todo_id: "pr_task",
          title: "Wire merge train badge",
          count: 1,
          events: [
            {
              id: "queue:enqueued",
              at: NOW,
              category: "queue",
              kind: "enqueued",
              summary: "Queued at #2 in org/repo",
            },
          ],
        };
      }
      return {};
    });

    seed({
      connected: true,
      loaded: true,
      assistant: { machine_id: "hub", todos_v2: true },
      todos: sampleTodos,
      machines: [],
      sessions: [],
      interactions: [],
    });

    // Open parent goal first to verify child_summary on row & detail.
    await act(async () => {
      root.render(<TodosView selectedId="goal_parent" />);
    });
    await flush();

    expect(host.querySelector('[data-testid="todo-child-summary-badge"]')?.textContent).toContain("2/4");
    expect(host.querySelector('[data-testid="todo-child-summary-working"]')?.textContent).toContain("1 working");
    expect(host.querySelector('[data-testid="todo-child-summary-needs-you"]')?.textContent).toContain("1 needs you");
    expect(host.querySelector('[data-testid="todo-detail-child-summary"]')?.textContent).toContain("2/4");

    // Merge-train badge appears on pr_task row.
    const rowBadge = host.querySelector('[data-todo="pr_task"] [data-testid="todo-mergetrain-badge"]');
    expect(rowBadge?.textContent).toContain("#2");
    expect(rowBadge?.textContent).toContain("Queued");

    // Switch to pr_task detail view.
    await act(async () => {
      root.render(<TodosView selectedId="pr_task" />);
    });
    await flush();

    const detailMt = host.querySelector('[data-testid="todo-detail-mergetrain"]');
    expect(detailMt?.textContent).toContain("#2");
    expect(detailMt?.textContent).toContain("org/repo");
    const aheadLink = host.querySelector<HTMLAnchorElement>('[data-testid="todo-detail-mergetrain-ahead"]');
    expect(aheadLink?.href).toBe("https://github.com/org/repo/pull/201");

    // Causal trace starts collapsed with zero /trace calls.
    expect(apiMock.mock.calls.some(([p]) => String(p).includes("/trace"))).toBe(false);
    const traceToggle = host.querySelector<HTMLButtonElement>('[data-testid="todo-causal-trace-toggle"]')!;
    expect(traceToggle.getAttribute("aria-expanded")).toBe("false");

    // Expanding fetches GET /todos/pr_task/trace lazily.
    await act(async () => traceToggle.click());
    await flush();

    expect(apiMock).toHaveBeenCalledWith("/todos/pr_task/trace", expect.anything());
    expect(host.querySelector('[data-testid="todo-causal-trace-event"]')?.textContent).toContain("Queued at #2 in org/repo");
  });
});
