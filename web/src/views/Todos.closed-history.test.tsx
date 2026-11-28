// @vitest-environment jsdom
// Opening Done reads the existing cursor for closed rows older than the bounded default list (#15683).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import type { Todo } from "../lib/todos";

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

const api = vi.hoisted(() => vi.fn(async (_path: string, _init?: RequestInit): Promise<unknown> => ({})));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api,
}));

const { getState, refreshTodos, reset } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const done = (id: string, title: string, created_at: string): Todo => ({
  id,
  title,
  created_at,
  updated_at: created_at,
  done_at: created_at,
  seen_at: created_at,
  spaces: [],
});

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(async () => {
  reset();
  localStorage.clear();
  localStorage.setItem("persona.todos.grouping", "status");
  await initI18n("en");
  api.mockReset();
  api.mockImplementation(async (path, init) => {
    if (path === "/todos") {
      return {
        todos: [done("done-recent", "Recent done", "2026-11-24T11:00:00Z")],
        counts: { open: 0, needs_you: 0, in_progress: 0, sections: { done: 55, cancelled: 0 } },
        board: {
          status_groups: [{ key: "done", total_count: 55, visible_count: 1, entries: [{ todo_id: "done-recent" }] }],
          direction_groups: [],
          sidebar_tree: [],
          counts: { needs_you: 0, in_progress: 0, pending_review: 0, waiting: 0, someday: 0, done: 55, open_total: 0 },
        },
        closed_cursors: { assistant: { next_cursor: "closed-1", has_more: true } },
      };
    }
    if (path === "/todos?limit=50&cursor=closed-1") {
      expect(new Headers(init?.headers).get("X-Persona-Manager")).toBe("assistant");
      return {
        todos: [
          done("done-recent", "Recent done", "2026-11-24T11:00:00Z"),
            { ...done("done-older", "Older done", "2026-10-01T11:00:00Z"), due_date: "2026-10-01" },
        ],
        has_more: false,
      };
    }
    return {};
  });
  await refreshTodos();
  Object.assign(getState(), {
    assistant: { machine_id: "m1", main: "main", conversations: [], todos_v2: true },
    features: ["todo_view"],
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  localStorage.clear();
  reset();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("older closed history", () => {
  it("loads the next closed cursor when Done opens and keeps the row in the list", async () => {
    await act(async () => root.render(<TodosView />));
    await flush();
    const section = host.querySelector<HTMLElement>('[data-testid=todo-section][data-section="done"]')!;
    const fold = section.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
    expect(fold.textContent).toContain(t("todos.section_done"));
    expect(host.querySelector('[data-testid=todo-section][data-section="cancelled"]'), "older closed history keeps the Cancelled entry reachable")
      .not.toBeNull();
    expect(section.dataset.todos, "the server's whole-list count is not a visible row count").toBe("1");
    const board = getState().todoBoard;
    expect(board).not.toBeNull();
    expect(host.textContent).not.toContain("Older done");

    await act(async () => fold.click());
    await flush();

    expect(api).toHaveBeenCalledWith("/todos?limit=50&cursor=closed-1", expect.anything());
    expect(host.textContent).toContain("Older done");
    expect(section.dataset.todos, "older rows are drawn from the cursor page").toBe("2");
    expect(getState().closedCursors.assistant).toEqual({ next_cursor: "", has_more: false });
    expect(getState().todoBoard).toBe(board);
    expect(host.querySelector('[data-testid="todos-sections"]')?.getAttribute("data-todo-source")).toBe("board");

    await act(async () => refreshTodos());
    expect(getState().todoBoard).toEqual(board);
    expect(host.textContent).toContain("Older done");
  });

  it("keeps older history reachable when the bounded window has no matching row", async () => {
    api.mockImplementation(async (path, init) => {
      if (path === "/todos") {
        return {
          todos: [],
          counts: { open: 0, needs_you: 0, in_progress: 0, sections: { done: 1, cancelled: 0 } },
          closed_cursors: { assistant: { next_cursor: "closed-1", has_more: true } },
        };
      }
      if (path === "/todos?limit=50&cursor=closed-1") {
        expect(new Headers(init?.headers).get("X-Persona-Manager")).toBe("assistant");
        return { todos: [done("done-older", "Older done", "2026-10-01T11:00:00Z")], has_more: false };
      }
      return {};
    });
    reset();
    localStorage.setItem("persona.todos.grouping", "status");
    await refreshTodos();
    Object.assign(getState(), {
      assistant: { machine_id: "m1", main: "main", conversations: [], todos_v2: true },
      features: ["todo_view"],
    });

    await act(async () => root.render(<TodosView />));
    await flush();
    const section = host.querySelector<HTMLElement>('[data-testid=todo-section][data-section="done"]')!;
    expect(section, "Done remains reachable although the bounded rows do not show it").toBeTruthy();
    expect(host.textContent).not.toContain("No matching");

    await act(async () => section.querySelector<HTMLButtonElement>("button[aria-expanded]")!.click());
    await flush();
    expect(host.textContent).toContain("Older done");
  });

  it("offers a Cancelled entry when only older cancelled history exists", async () => {
    api.mockImplementation(async (path, init) => {
      if (path === "/todos") {
        return { todos: [], closed_cursors: { assistant: { next_cursor: "closed-cancelled", has_more: true } } };
      }
      if (path === "/todos?limit=50&cursor=closed-cancelled") {
        expect(new Headers(init?.headers).get("X-Persona-Manager")).toBe("assistant");
        return {
          todos: [{ ...done("cancelled-older", "Older cancelled", "2026-10-01T11:00:00Z"), done_by: "cancelled", cancelled_at: "2026-10-01T11:00:00Z" }],
          has_more: false,
        };
      }
      return {};
    });
    reset();
    localStorage.setItem("persona.todos.grouping", "status");
    await refreshTodos();
    Object.assign(getState(), {
      assistant: { machine_id: "m1", main: "main", conversations: [], todos_v2: true },
      features: ["todo_view"],
    });

    await act(async () => root.render(<TodosView />));
    await flush();
    const section = host.querySelector<HTMLElement>('[data-testid=todo-section][data-section="cancelled"]')!;
    expect(section).toBeTruthy();
    await act(async () => section.querySelector<HTMLButtonElement>("button[aria-expanded]")!.click());
    await flush();
    expect(host.textContent).toContain("Older cancelled");
  });

  it("loads the cursor when an already-open Done section reaches the end", async () => {
    localStorage.setItem("fold:todos-section:done", "0");
    class Visible {
      constructor(private cb: IntersectionObserverCallback) {}
      observe(target: Element) {
        queueMicrotask(() => this.cb([{ isIntersecting: true, target } as IntersectionObserverEntry], this as unknown as IntersectionObserver));
      }
      unobserve() {}
      disconnect() {}
      takeRecords() { return []; }
    }
    vi.stubGlobal("IntersectionObserver", Visible);

    await act(async () => root.render(<TodosView />));
    await flush();

    expect(api).toHaveBeenCalledWith("/todos?limit=50&cursor=closed-1", expect.anything());
    expect(host.textContent).toContain("Older done");
  });

  it("keeps Calendar history bounded with Show done enabled", async () => {
    localStorage.setItem("persona.todos.calendar.showDone", "1");
    const board = getState().todoBoard;
    await act(async () => root.render(<TodosView view="calendar" mode="day" date="2026-10-01" />));
    await flush();
    expect(api).not.toHaveBeenCalledWith("/todos?limit=50&cursor=closed-1", expect.anything());
    expect(getState().todoBoard).toBe(board);
  });
});
