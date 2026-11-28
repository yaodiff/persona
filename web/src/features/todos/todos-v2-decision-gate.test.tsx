// @vitest-environment jsdom
// Integration coverage for Issue #15757:
// 1. Decision Queue entry + batch submission strictly gated on todos_v2 === true && >=2 needs_you decisions.
// 2. Weakest-link Target Repo gate (repo_gate) in list row (<= 2 lines) & detail workspace under todos_v2 === true.
// 3. Zero UI or behavioral change when todos_v2 === false.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../../lib/todos";
import type { TodoSummary } from "../../lib/todo-view-wire";

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
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: unknown) => apiMock(path, init),
}));

const { initI18n } = await import("../../i18n");
const { getState, reset } = await import("../../lib/store");
const { TodosView } = await import("../../views/Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const NOW = Date.parse("2026-11-26T00:00:00.000Z");
const HOUR = 3_600_000;
const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString();
const view = (section: string, reason: string, phase: string, rank = 0): TodoSummary =>
  ({ phase, section, reason, rank }) as TodoSummary;

const blockedTodo: Todo = {
  id: "todo_blocked",
  title: "Ship cross-repo auth",
  origin: "conv-main",
  supervision: { supervisor: "main", state: "needs_user" },
  ask: {
    outcome: "decide",
    verdict: "Choose rollout order",
    question: "Deploy API before web?",
    recommend: "API first",
    asked_at: iso(HOUR),
    options: [{ label: "API first" }, { label: "Together" }],
  },
  repo_gate: {
    state: "blocked_ci",
    target_count: 3,
    ready_count: 2,
    reference_count: 1,
    blocking_repo: "acme/api",
    blocking_pr: "https://github.com/acme/api/pull/88",
    reason: "ci_failed",
  },
  created_at: iso(3 * HOUR),
  updated_at: iso(HOUR),
  view: view("needs_you", "owner_decides", "needs_you", 0),
};

const readyDecisionTodo: Todo = {
  id: "todo_ready",
  title: "Approve release notes",
  origin: "conv-main",
  supervision: { supervisor: "main", state: "needs_user" },
  ask: {
    outcome: "decide",
    verdict: "Publish release notes?",
    question: "Publish now or wait?",
    recommend: "Publish now",
    asked_at: iso(HOUR),
    options: [{ label: "Publish now" }, { label: "Wait" }],
  },
  repo_gate: {
    state: "ready",
    target_count: 2,
    ready_count: 2,
    reference_count: 0,
    reason: "all_targets_ready",
  },
  created_at: iso(2 * HOUR),
  updated_at: iso(HOUR),
  view: view("needs_you", "owner_decides", "needs_you", 1),
};

const refOnlyTodo: Todo = {
  id: "todo_ref_only",
  title: "Review upstream RFC",
  agents: [{ machine_id: "m1", session_id: "s1", linked_at: iso(HOUR) }],
  repo_gate: {
    state: "reference_only",
    target_count: 0,
    ready_count: 0,
    reference_count: 2,
    reason: "reference_only",
  },
  created_at: iso(2 * HOUR),
  updated_at: iso(HOUR),
  view: view("working", "agent_running", "working", 0),
};

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

const seedAndRender = async ({
  todos,
  todosV2,
  selectedId,
}: {
  todos: Todo[];
  todosV2?: boolean;
  selectedId?: string;
}) => {
  Object.assign(getState() as unknown as Record<string, unknown>, {
    todos,
    todosLoaded: true,
    sessions: {},
    interactions: [],
    answered: {},
    features: ["todo_view"],
    assistant: {
      machine_id: "m1",
      main: "main",
      conversations: [],
      ...(todosV2 !== undefined ? { todos_v2: todosV2 } : {}),
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
    if (path.includes("/repositories")) {
      return { revision: 1, repositories: [] };
    }
    if (path === "/todos/decisions/batch") {
      return {
        results: [
          { todo_id: "todo_blocked", ok: true },
          { todo_id: "todo_ready", ok: true },
        ],
        succeeded: 2,
        failed: 0,
      };
    }
    if (path.includes("/todos")) {
      return { todos: [] };
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

describe("TodosView Decision Queue & repo_gate under todos_v2 (#15757)", () => {
  it("renders zero Decision Queue entry or repo_gate UI when todos_v2 is false", async () => {
    await seedAndRender({
      todos: [blockedTodo, readyDecisionTodo, refOnlyTodo],
      todosV2: false,
      selectedId: "todo_blocked",
    });

    expect(host.querySelector('[data-testid="todos-decision-queue-open"]')).toBeNull();
    expect(host.querySelector('[data-testid="todo-row-repo-gate"]')).toBeNull();
    expect(host.querySelector('[data-testid="todo-detail-repo-gate"]')).toBeNull();
  });

  it("does not show Decision Queue entry when fewer than 2 needs_you decisions exist", async () => {
    await seedAndRender({
      todos: [blockedTodo, refOnlyTodo],
      todosV2: true,
    });
    expect(host.querySelector('[data-testid="todos-decision-queue-open"]')).toBeNull();
  });

  it("shows Decision Queue entry when todos_v2=true and >=2 needs_you decisions exist, and submits batch", async () => {
    await seedAndRender({
      todos: [blockedTodo, readyDecisionTodo, refOnlyTodo],
      todosV2: true,
    });

    const entryBtn = host.querySelector<HTMLButtonElement>('[data-testid="todos-decision-queue-open"]');
    expect(entryBtn).toBeTruthy();
    expect(entryBtn?.textContent).toContain("2");

    await act(async () => entryBtn!.click());
    await flush();

    expect(document.querySelector('[data-testid="decision-queue-panel"]')).toBeTruthy();
    // Nothing pre-selected even though both items have `recommend`
    expect(document.querySelector('[data-testid="decision-queue-ready-count"]')?.textContent).toContain("0 / 2");

    const opt1 = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-option"][data-todo-id="todo_blocked"][data-option="Together"]')!;
    const opt2 = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-option"][data-todo-id="todo_ready"][data-option="Publish now"]')!;
    await act(async () => opt1.click());
    await act(async () => opt2.click());
    await flush();

    const submitBtn = document.querySelector<HTMLButtonElement>('[data-testid="decision-queue-submit"]')!;
    await act(async () => submitBtn.click());
    await flush();

    expect(apiMock).toHaveBeenCalledWith(
      "/todos/decisions/batch",
      expect.objectContaining({
        method: "POST",
        json: {
          items: [
            { todo_id: "todo_blocked", option: "Together" },
            { todo_id: "todo_ready", option: "Publish now" },
          ],
        },
      }),
    );
  });

  it("renders repo_gate in list row (<=2 lines, ignoring reference_only) and detail workspace under todos_v2=true", async () => {
    await seedAndRender({
      todos: [blockedTodo, readyDecisionTodo, refOnlyTodo],
      todosV2: true,
      selectedId: "todo_blocked",
    });

    // Blocked row: compact repo_gate badge sits inside line 2 (no 3rd line added).
    const blockedRow = host.querySelector<HTMLElement>('[data-todo="todo_blocked"]')!;
    const blockedLine2 = blockedRow.querySelector('[data-testid="todo-slim-line2"]')!;
    const blockedGate = blockedLine2.querySelector('[data-testid="todo-row-repo-gate"]')!;
    expect(blockedGate).toBeTruthy();
    expect(blockedGate.getAttribute("data-gate-state")).toBe("blocked_ci");
    expect(blockedGate.textContent).toContain("2/3");
    expect(blockedGate.textContent).toContain("api");
    expect(blockedGate.getAttribute("title")).toContain("acme/api");

    // Ready row: compact green ready badge inside line 2.
    const readyGate = host.querySelector('[data-todo="todo_ready"] [data-testid="todo-slim-line2"] [data-testid="todo-row-repo-gate"]')!;
    expect(readyGate.getAttribute("data-gate-state")).toBe("ready");
    expect(readyGate.textContent).toContain("2/2");

    // Reference-only row: omits row repo_gate badge.
    expect(host.querySelector('[data-todo="todo_ref_only"] [data-testid="todo-row-repo-gate"]')).toBeNull();

    // Detail workspace: shows repo_gate summary banner with blocking repo, reason, reference count, and clickable PR link.
    const detailGate = host.querySelector('[data-testid="todo-detail-repo-gate"]')!;
    expect(detailGate).toBeTruthy();
    expect(detailGate.getAttribute("data-gate-state")).toBe("blocked_ci");
    expect(detailGate.textContent).toContain("2/3");
    expect(detailGate.textContent).toContain("acme/api");
    expect(detailGate.querySelector('[data-testid="todo-detail-repo-gate-reason"]')?.textContent).toContain("CI checks failed");
    expect(detailGate.querySelector('[data-testid="todo-detail-repo-gate-refs"]')?.textContent).toContain("1");
    const prLink = detailGate.querySelector<HTMLAnchorElement>('[data-testid="todo-detail-repo-gate-pr"]')!;
    expect(prLink.href).toBe("https://github.com/acme/api/pull/88");
    expect(prLink.textContent).toContain("PR #88");
  });
});
