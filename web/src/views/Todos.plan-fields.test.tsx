// @vitest-environment jsdom
// Audit item 2: the to-do page's due date and reminder are saved once they are entered, not on every typed digit. A day
// typed segment by segment is a valid date after its first digit; saving it then stored a day the user was still typing
// ("1" of "15" saved the 1st) and disabled the field while it saved, which took the focus away before the "5". Typed
// changes wait for Enter or for the field to be left; a pick from the browser's picker (a change with no key before it)
// still saves at once. Leaving the field while that save is on its way sends it no second time. The hub is faked; the
// data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = { id: "plan", title: "Renew the passport", created_at: iso(7_200_000), updated_at: iso(3_600_000), due_date: "2026-12-05", agents: [] } as unknown as Todo;
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

beforeEach(async () => {
  api.mockReset();
  api.mockImplementation(async (path) => (path === "/todos" ? { todos: [todo] } : { todo }));
  seed({ todos: [todo], todosLoaded: true, sessions: {}, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<TodosView selectedId="plan" />));
  await flush();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
});

const field = (id: string) => host.querySelector<HTMLInputElement>(`[data-testid=${id}]`)!;
const patches = () => api.mock.calls.filter(([, init]) => (init as { method?: string })?.method === "PATCH").map(([, init]) => (init as { json?: unknown }).json);
// what the browser does for a typed digit: the key, then the field's new value
const type = async (el: HTMLInputElement, key: string, value: string) => {
  await act(async () => {
    el.focus();
    el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

describe("the to-do page's date fields", () => {
  it("keep typed digits unsaved and the field usable until it is left, then save what was typed once", async () => {
    const due = field("todo-plan-due");
    await type(due, "1", "2026-12-01");
    expect(patches()).toEqual([]);
    expect(due.disabled).toBe(false);
    expect(document.activeElement).toBe(due);
    await type(due, "5", "2026-12-15");
    expect(patches()).toEqual([]);
    expect(due.value).toBe("2026-12-15");
    await act(async () => due.blur());
    await flush();
    expect(patches()).toEqual([{ due_date: "2026-12-15" }]);
  });

  it("save a typed reminder on Enter", async () => {
    const remind = field("todo-plan-remind");
    await type(remind, "2", "2026-12-05T02:30");
    await type(remind, "1", "2026-12-05T21:30");
    expect(patches()).toEqual([]);
    await act(async () => remind.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await flush();
    expect(patches()).toEqual([{ remind_at: new Date("2026-12-05T21:30").toISOString() }]);
    expect(remind.disabled).toBe(false);
  });

  it("save once when the field is left before the save from Enter is through", async () => {
    // the hub answers the PATCH only when the test says so
    let answer = () => {};
    api.mockImplementation(async (path, init) => {
      if ((init as { method?: string })?.method === "PATCH") await new Promise<void>((r) => (answer = r));
      return path === "/todos" ? { todos: [todo] } : { todo };
    });
    const due = field("todo-plan-due");
    await type(due, "2", "2026-12-12");
    await act(async () => due.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await act(async () => due.blur());
    await act(async () => due.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(patches()).toEqual([{ due_date: "2026-12-12" }]);
    expect(due.value).toBe("2026-12-12");
    await act(async () => answer());
    await flush();
    expect(patches()).toEqual([{ due_date: "2026-12-12" }]);
  });

  it("save a day picked without typing at once", async () => {
    const due = field("todo-plan-due");
    await act(async () => {
      due.dispatchEvent(new Event("pointerdown", { bubbles: true }));
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(due, "2026-12-24");
      due.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await flush();
    expect(patches()).toEqual([{ due_date: "2026-12-24" }]);
  });

  it("leave the saved day alone when what was typed is the same", async () => {
    const due = field("todo-plan-due");
    await type(due, "5", "2026-12-05");
    await act(async () => due.blur());
    await flush();
    expect(patches()).toEqual([]);
  });
});
