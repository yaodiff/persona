// @vitest-environment jsdom
// Hub todo_view projection in the To-dos status list (#10226, #14583, features/todos/model): preferred by default
// whenever the hub lists `todo_view` (independent of Developing Persona), with `persona.todos.viewDraw=0` as explicit
// browser rollback and row-level fallback when a row lacks a readable view.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";
import type { Interaction, Session } from "../lib/types";
import type { TodoSummary } from "../lib/todo-view-wire";
import { resetRestoredCache, restoreTodo } from "../lib/todo-restore";

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

const { getState, putTodo, reset } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { DEV_MODE_KEY } = await import("../lib/dev-probe");
const { VIEW_DRAW_KEY } = await import("../features/todos/model/view-gates");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const NOW = Date.parse("2026-11-16T12:00:00.000Z");
const HOUR = 3_600_000;
const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString();

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);

beforeEach(() => {
  reset();
  resetRestoredCache();
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  api.mockReset();
  api.mockImplementation(async () => ({}));
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
  resetRestoredCache();
  reset();
  vi.useRealTimers();
});

const render = async (todos: Todo[], features = ["todo_view"], state = {}) => {
  localStorage.setItem("persona.todos.grouping", "status");
  seed({ todos, todosLoaded: true, sessions: {}, interactions: [], answered: {}, features, ...state });
  await act(async () => root.render(<TodosView />));
  await flush();
};
const sections = () => host.querySelector<HTMLElement>('[data-testid="todos-sections"]');
// The sections as drawn, without the badge's marks: those follow the hub's needs_you whenever the hub sent a view
// (lib/todo-triage.ts hubNeedsYou), switches or not, so the badge, the tray and the hub keep one number.
const drawn = () => {
  const s = sections()?.cloneNode(true) as HTMLElement | undefined;
  s?.querySelectorAll('[data-testid="todo-owner-mark"], [data-testid="todo-owner-count"]').forEach((m) => {
    const holder = m.parentElement;
    m.remove();
    // a title the mark was set beside is drawn in a holder of its own: without the mark it is the bare title again
    const title = holder?.firstElementChild;
    if (holder && holder.childElementCount === 1 && title?.getAttribute("data-testid") === "todo-title") holder.replaceWith(title);
  });
  return s?.outerHTML;
};
const marks = () => Object.fromEntries([...host.querySelectorAll<HTMLElement>('[data-testid="todo-owner-mark"]')].map((m) => [m.closest<HTMLElement>('[data-testid="todo-triage-row"]')?.dataset.id, m.dataset.need]));
const placed = () =>
  Object.fromEntries(
    [...host.querySelectorAll<HTMLElement>('[data-testid="todo-section"]')].map((s) => [s.dataset.section, [...s.querySelectorAll<HTMLElement>("[data-todo]")].map((r) => r.dataset.todo)]),
  );
// The view as the wire carries it: raw strings, which may be a newer hub's values the closed types do not know.
const view = (section: string, reason: string, phase: string, more: Partial<TodoSummary> = {}): TodoSummary => ({ phase, section, reason, rank: 0, ...more }) as TodoSummary;
const task = (id: string, fields: Partial<Todo> = {}): Todo => ({ id, title: `Errand ${id}`, created_at: iso(5 * HOUR), updated_at: iso(4 * HOUR), ...fields });

// Today's triage files all three in Other open; the hub's view puts the first two elsewhere.
const plain = [task("todo_ask"), task("todo_run"), task("todo_old")];
const withViews = [
  { ...plain[0], view: view("needs_you", "owner_decides", "needs_you") },
  { ...plain[1], view: view("working", "agent_running", "working") },
  plain[2], // an older manager's row: no view
];

describe("To-dos sections from the hub's view", () => {
  it.each([false, true])("shows supervisor processing in the Working section (slim rows: %s)", async (slim) => {
    const at = iso(HOUR);
    const worker: Session = { machine_id: "box", session_id: "worker", title: "Worker", cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at };
    const held: Interaction = { id: "ix-held", machine_id: "box", session_id: "worker", kind: "permission", delegating: true };
    const todo = task("supervised", {
      agents: [{ machine_id: "box", session_id: "worker", linked_at: at }],
      view: view("working", "persona_working", "working"),
    });
    await render([todo], ["todo_view"], { sessions: { box: [worker] }, interactions: [held], assistant: { todos_v2: slim } });
    const working = host.querySelector('[data-testid="todo-section"][data-section="working"]');
    expect(working?.getAttribute("data-todos")).toBe("1");
    const row = working?.querySelector('[data-todo="supervised"]');
    expect(row?.querySelector(slim ? '[data-testid="todo-slim-cue"]' : '[data-testid="todo-status"]')?.textContent).toBe("督办处理中");
    expect(marks()).toEqual({});
  });

  it("draws from the hub's view by default when todo_view is advertised, without Developing Persona", async () => {
    const ranked = [
      task("todo_ask_second", { view: view("needs_you", "owner_decides", "needs_you", { rank: 2 }) }),
      task("todo_ask_first", { view: view("needs_you", "owner_decides", "needs_you", { rank: 1 }) }),
      task("todo_run", { view: view("working", "agent_running", "working", { rank: 0 }) }),
      task("todo_old"), // missing view -> local fallback
      task("todo_unknown_phase", { view: view("needs_you", "owner_decides", "future_phase") }),
      task("todo_unknown_section", { view: view("future_section", "owner_decides", "needs_you") }),
      task("todo_unknown_reason", { view: view("needs_you", "future_reason", "needs_you") }),
    ];
    await render(ranked);
    expect(sections()?.dataset.todoSource).toBe("view");
    expect(placed()).toEqual({
      waiting: ["todo_ask_first", "todo_ask_second"],
      working: ["todo_run"],
      other: ["todo_old", "todo_unknown_phase", "todo_unknown_reason", "todo_unknown_section"],
    });
  });

  for (const [name, setup, features] of [
    ["persona.todos.viewDraw=0 rollback", () => (localStorage.setItem(DEV_MODE_KEY, iso(HOUR)), localStorage.setItem(VIEW_DRAW_KEY, "0")), ["todo_view"]],
    ["hub without todo_view", () => localStorage.setItem(DEV_MODE_KEY, iso(HOUR)), ["todo_seen"]],
  ] as const) {
    it(`draws identically to local triage with ${name}`, async () => {
      localStorage.setItem(VIEW_DRAW_KEY, "0");
      await render(plain, [...features]);
      const before = drawn();
      expect(placed()).toEqual({ other: ["todo_ask", "todo_old", "todo_run"] });
      await act(async () => root.unmount());
      root = createRoot(host);
      localStorage.clear();
      setup();
      await render(withViews, [...features]);
      expect(sections()?.dataset.todoSource).toBeUndefined();
      expect(drawn()).toBe(before);
      // the marks are the hub's: Needs you counts, a working one does not, a row without a view keeps the web's rule
      expect(marks()).toEqual({ todo_ask: "owner_decides", todo_old: "unattended" });
    });
  }

  it("applies due filter before placement and narrows display on search without shrinking filtered header counts", async () => {
    const dueToday = "2026-11-16";
    const list = [
      task("todo_alpha", { title: "Alpha release", due_date: dueToday, view: view("needs_you", "owner_decides", "needs_you", { rank: 0 }) }),
      task("todo_beta", { title: "Beta audit", due_date: dueToday, view: view("working", "agent_running", "working", { rank: 0 }) }),
      task("todo_nodue", { title: "Backlog item", view: view("todo", "rest", "todo", { rank: 0 }) }),
    ];
    localStorage.setItem("persona.todos.filter", "today");
    await render(list);
    expect(sections()?.dataset.todoSource).toBe("view");
    expect(placed()).toEqual({ waiting: ["todo_alpha"], working: ["todo_beta"] });
    const headerCount = () => host.querySelector<HTMLElement>('[data-testid="todos-open-count"]')?.textContent ?? host.querySelector("header")?.textContent ?? "";
    const beforeSearch = headerCount();
    expect(beforeSearch).toContain("2");

    // Search narrows displayed rows to Alpha, while filtered header counts still reflect both filtered items.
    const input = host.querySelector<HTMLInputElement>('input[type="search"], input[placeholder]');
    if (input) {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      await act(async () => {
        setter.call(input, "Alpha");
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
      await flush();
      expect(placed()).toEqual({ waiting: ["todo_alpha"] });
      expect(headerCount()).toBe(beforeSearch);
    }
  });

  it("preserves local restore across hub snapshots without flashing back to Shelved", async () => {
    const shelved = task("todo_old_inactive", {
      created_at: iso(72 * HOUR),
      updated_at: iso(72 * HOUR),
      view: view("todo", "rest", "todo", { inactive: iso(72 * HOUR) }),
    });
    await render([shelved]);
    expect(placed()).toEqual({ shelved: [] }); // Shelved starts folded
    expect(host.querySelector<HTMLElement>('[data-testid="todo-section"][data-section="shelved"]')?.dataset.todos).toBe("1");

    await act(async () => restoreTodo("todo_old_inactive", NOW));
    await flush();
    expect(placed()).toEqual({ other: ["todo_old_inactive"] });

    // A fresh hub snapshot arrives still carrying `inactive`: row must remain in Other open and not flash back.
    await act(async () => {
      putTodo({ ...shelved, updated_at: iso(71 * HOUR), view: view("todo", "rest", "todo", { inactive: iso(72 * HOUR) }) });
    });
    await flush();
    expect(placed()).toEqual({ other: ["todo_old_inactive"] });
  });

  it("keeps Got it feedback on a fresh closed concern without flashing back when the new snapshot lands", async () => {
    const concern = task("todo_concern", {
      kind: "concern",
      done_at: iso(HOUR),
      done_by: "reported",
      view: view("just_finished", "closed_unseen", "done"),
    });
    let resolveSeen!: (value: unknown) => void;
    api.mockImplementation(async (path: string, init?: unknown) => {
      if (path === "/todos/todo_concern/seen" && (init as { method?: string })?.method === "PUT") {
        return new Promise((resolve) => { resolveSeen = resolve; });
      }
      return {};
    });
    await render([concern], ["todo_view", "todo_seen"]);
    expect(placed()).toEqual({ just_done: ["todo_concern"] });

    const gotIt = host.querySelector<HTMLButtonElement>('[data-testid="todo-seen"]')!;
    await act(async () => gotIt.click());
    // Optimistically moved to Done before the hub answers.
    expect(host.querySelector('[data-testid="todo-section"][data-section="just_done"]')).toBeNull();
    expect(host.querySelector<HTMLElement>('[data-testid="todo-section"][data-section="done"]')?.dataset.todos).toBe("1");

    // Hub answers PUT /seen and pushes updated view: stays in Done without flashing back.
    await act(async () => {
      resolveSeen({ todo: { ...concern, seen_at: iso(0) } });
    });
    await flush();
    expect(host.querySelector('[data-testid="todo-section"][data-section="just_done"]')).toBeNull();
    expect(host.querySelector<HTMLElement>('[data-testid="todo-section"][data-section="done"]')?.dataset.todos).toBe("1");

    await act(async () => {
      putTodo({ ...concern, seen_at: iso(0), view: view("done", "reported", "done", { seen: true }) });
    });
    await flush();
    expect(host.querySelector('[data-testid="todo-section"][data-section="just_done"]')).toBeNull();
    expect(host.querySelector<HTMLElement>('[data-testid="todo-section"][data-section="done"]')?.dataset.todos).toBe("1");
  });
});
