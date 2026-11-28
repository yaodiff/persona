// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo, TodoBoardProjection } from "../lib/todos";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia = ((query: string) => ({
    matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {},
    addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const api = vi.hoisted(() => vi.fn(async (_path: string, _init?: { headers?: HeadersInit }): Promise<unknown> => ({})));
vi.mock("../lib/api", async (original) => ({ ...(await original<typeof import("../lib/api")>()), api }));

const { initI18n, t } = await import("../i18n");
const { setRoster } = await import("../lib/managers");
const { navigate, useRoute } = await import("../lib/router");
const { getState, patchAssistant, refreshTodos, reset } = await import("../lib/store");
const { useNavPages } = await import("./SidebarParts");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const at = "2026-11-28T08:00:00Z";
const ask = { outcome: "decide", asked_at: at, verdict: "Choose how to proceed" };
const todo = (id: string, patch: Partial<Todo> = {}): Todo => ({
  id, title: id, created_at: at, updated_at: at, project: "sample", item_type: "task", ...patch,
});
const todos = [
  todo("parent", { ask }),
  todo("child", { part_of: "parent", ask: { ...ask, outcome: "done" } }),
  todo("todo_od_independent", { part_of: "parent", ask }),
  todo("quiet", { ask, quiet: true }),
  todo("deferred", { ask, view: { section: "can_wait", phase: "needs_you", reason: "can_wait", rank: 0 } }),
  todo("ordinary", { item_type: "personal" }),
  todo("finished", { done_at: at }),
  todo("abandoned", { done_at: at, cancelled_at: at }),
];
const group = (key: string, ...ids: string[]) => ({
  key, total_count: ids.length, visible_count: ids.length, entries: ids.map((todo_id) => ({ todo_id })),
});
const board: TodoBoardProjection = {
  status_groups: [
    group("needs_you", "parent", "child", "todo_od_independent", "quiet"),
    group("can_wait", "deferred"),
    group("todo", "ordinary"),
    group("done", "finished"),
    group("cancelled", "abandoned"),
  ],
  direction_groups: [{
    direction_id: "product",
    label: "Product",
    counts: { needs_you: 2, in_progress: 0, pending_review: 0, waiting: 0, done: 1, total: todos.length },
    todo_ids: todos.map(({ id }) => id),
  }],
  sidebar_tree: [],
  counts: { needs_you: 2, can_wait: 1, in_progress: 0, pending_review: 0, waiting: 0, todo: 1, someday: 0, done: 1, cancelled: 1, open_total: 5 },
};
const blockers = [{ key: "shared", kind: "external_target", label: "Sample blocker", count: 2, todo_ids: ["parent", "ordinary"] }];
let host: HTMLDivElement;
let root: Root;

function Screen() {
  const route = useRoute();
  const { pages } = useNavPages();
  return <>
    <output data-testid="sidebar-todos-count">{pages.find((page) => page.k === "todos")?.badge}</output>
    {route.name === "todos" && <TodosView view={route.view} mode={route.mode} date={route.date} selectedId={route.id} />}
  </>;
}

const find = (id: string) => host.querySelector<HTMLElement>(`[data-testid="${id}"]`);
const section = (key: string) => host.querySelector<HTMLElement>(`[data-testid="todo-section"][data-section="${key}"]`);
const ids = (el: Element | null) => [...(el?.querySelectorAll("[data-todo]") ?? [])].map((row) => row.getAttribute("data-todo"));
const control = (role: string, label: string) => [...document.querySelectorAll<HTMLElement>(`[role="${role}"]`)]
  .find((el) => el.textContent?.trim() === label);
const click = async (el: HTMLElement | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => el!.click());
};
const view = async (name: "list" | "board" | "calendar") => click(control("radio", t(`todos.view_${name}`)));
const grouping = async (name: "status" | "folder" | "direction") => {
  await click(find("todos-grouping-trigger"));
  await click(control("menuitemradio", t(`todos.grouping_v2_${name}`)));
};

async function mount() {
  await act(async () => root.render(<Screen />));
}

async function remount() {
  await act(async () => root.unmount());
  root = createRoot(host);
  await mount();
}

beforeEach(async () => {
  await initI18n("en");
  reset();
  localStorage.clear();
  history.replaceState(null, "", "/todos");
  api.mockReset();
  api.mockImplementation(async () => ({}));
  Object.assign(getState(), {
    todos, todoBoard: board, todosLoaded: true, todosSupported: true, sharedBlockers: blockers,
    assistant: { machine_id: "hub", conversations: [], todos_v2: true },
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  reset();
  setRoster(null);
  localStorage.clear();
  await initI18n("zh-Hans");
});

describe("the Web To-dos projection and saved views", () => {
  it("shares the hub's totals and keeps batch decisions and the rendered sections tied to the projection", async () => {
    await mount();
    const count = board.counts.needs_you;
    expect(find("sidebar-todos-count")?.textContent).toBe(String(count));
    expect(find("todos-header-attention")?.dataset.count).toBe(String(count));
    expect(ids(section("waiting"))).toEqual(["parent", "child", "todo_od_independent", "quiet"]);
    expect(ids(section("can_wait"))).toEqual(["deferred"]);
    expect(ids(section("todo"))).toEqual(["ordinary"]);
    await click(section("done")?.querySelector("button"));
    await click(section("cancelled")?.querySelector("button"));
    expect(ids(section("done"))).toEqual(["finished"]);
    expect(ids(section("cancelled"))).toEqual(["abandoned"]);

    // Filters narrow the list only. Batch Decide counts rows with a decision the user can answer.
    await click(control("radio", t("todos.kind_filter_todos")));
    expect(ids(section("todo"))).toEqual(["ordinary"]);
    expect(find("todos-header-attention")?.dataset.count).toBe(String(count));
    const queueButton = find("todos-decision-queue-open");
    expect(queueButton?.textContent).toContain(t("todos.decision_queue_open", { count }));
    await click(queueButton);
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.querySelectorAll('input[type="checkbox"][aria-label]')).toHaveLength(2);
    expect(dialog?.textContent).toContain("todo_od_independent");
    expect(dialog?.textContent).not.toContain("deferred");
    expect(dialog?.textContent).not.toContain("quiet");
    await act(async () => navigate({ name: "todos", view: "board" }));
    const progress = find("todos-board");
    expect(progress?.dataset.needsYouCount).toBe(String(count));
    expect(progress?.dataset.openCount).toBe(String(board.counts.open_total));
    expect(find("todo-board-group")?.dataset.waitingCount).toBe(String(count));
    expect(ids(progress)).toEqual(["parent", "child", "todo_od_independent", "quiet", "deferred", "ordinary"]);
    expect(progress?.querySelector('[data-todo="deferred"] [data-testid="board-owner"]')).toBeNull();
    await click(progress?.querySelector<HTMLElement>('[data-board-grouping="direction"]'));
    expect(find("todo-board-group")?.dataset.direction).toBe("product");
    expect(find("todo-board-group")?.dataset.openCount).toBe(String(board.counts.open_total));
    expect(find("todo-board-group")?.dataset.waitingCount).toBe(String(count));
  });

  it("restores List filters after a delayed read and keeps each grouping through remounts and URL changes", async () => {
    await mount();
    await click(control("radio", t("todos.kind_filter_tasks")));
    await click(find("shared-blocker-chip"));
    await grouping("folder");
    expect(find("todos-grouping-menu")?.dataset.grouping).toBe("folder");
    expect(find("shared-blocker-chip")?.getAttribute("aria-pressed")).toBe("true");

    Object.assign(getState(), { sharedBlockers: [], todosLoaded: false });
    await remount();
    expect(control("radio", t("todos.kind_filter_tasks"))?.getAttribute("aria-checked")).toBe("true");
    expect(find("todos-grouping-menu")?.dataset.grouping).toBe("folder");
    expect(find("shared-blocker-chip")).toBeNull();
    api.mockImplementation(async (path) => path === "/todos" ? { todos, board, shared_blockers: blockers } : {});
    await act(async () => refreshTodos());
    expect(find("shared-blocker-chip")?.getAttribute("aria-pressed")).toBe("true");

    await click(find("shared-blocker-chip"));
    await grouping("direction");
    await click(find("todo-direction-review-done"));
    const completed = () => host.querySelector('[data-testid="todos-done-direction-chip"][data-direction-id="product"]');
    expect(completed()?.getAttribute("aria-pressed")).toBe("true");
    await grouping("status");
    await grouping("direction");
    expect(completed()?.getAttribute("aria-pressed")).toBe("true");
    await remount();
    expect(completed()?.getAttribute("aria-pressed")).toBe("true");

    await view("board");
    await click(host.querySelector<HTMLElement>('[data-board-grouping="direction"]'));
    await remount();
    expect(host.querySelector('[data-board-grouping="direction"]')?.getAttribute("aria-checked")).toBe("true");
    await view("calendar");
    await click(find("todo-calendar")?.querySelector(`button[title="${t("todos.calendar_next")}"]`));
    const calendarDate = new URLSearchParams(location.search).get("date");
    expect(calendarDate).toBeTruthy();
    await view("list");
    expect(new URLSearchParams(location.search).get("view")).toBe("list");
    expect(completed()?.getAttribute("aria-pressed")).toBe("true");
    await view("calendar");
    expect(new URLSearchParams(location.search).get("date")).toBe(calendarDate);
    await act(async () => {
      const popped = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
      history.back();
      await popped;
    });
    expect(find("todo-calendar")).toBeNull();
    expect(completed()?.getAttribute("aria-pressed")).toBe("true");

    await act(async () => patchAssistant({ todos_v2: false }));
    expect(find("todos-summary")).toBeTruthy();
    expect(find("todos-kind-filter")).toBeNull();
    expect(find("shared-blocker-chip")).toBeNull();
    expect(find("todos-decision-queue-open")).toBeNull();
    await act(async () => patchAssistant({ todos_v2: true }));
    expect(completed()?.getAttribute("aria-pressed")).toBe("true");
  });

  it("uses the selected assistant's hub counts instead of the merged board's counts", async () => {
    getState().todoBoard = null;
    setRoster({ default: "assistant", managers: [
      { id: "assistant", name: "Personal", online: true },
      { id: "work", name: "Work", online: true },
    ] });
    const merged = { ...board, counts: { ...board.counts, needs_you: 20 } };
    api.mockImplementation(async (path, init) => path === "/todos" ? {
      todos,
      board: new Headers(init?.headers).get("X-Persona-Manager") === "assistant" ? board : merged,
    } : {});
    await act(async () => refreshTodos());
    await mount();
    await act(async () => { await vi.dynamicImportSettled(); });
    expect(find("sidebar-todos-count")?.textContent).toBe("2");
    expect(find("todos-header-attention")?.dataset.count).toBe("2");
    expect(api.mock.calls.filter(([path]) => path === "/todos")).toHaveLength(2);
  });
});
