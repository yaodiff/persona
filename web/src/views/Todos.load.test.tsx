// @vitest-environment jsdom
// A failed read must not look like an empty todo list. Retry uses the same store path and preserves existing rows.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((media: string) => ({
    matches: false, media, onchange: null,
    addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const api = vi.fn(async (_path: string): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string) => api(path),
}));

const { ApiError, errorText } = await import("../lib/api");
const { getState, refresh, refreshTodos } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const todo: Todo = { id: "todo-example", title: "Release checklist", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" };
const unavailable = () => new ApiError(503, "Service temporarily unavailable", {});
const seed = (patch: Partial<ReturnType<typeof getState>>) => Object.assign(getState(), patch);
const before = { ...getState() };
let host: HTMLDivElement, root: Root;
const list = () => host.querySelector<HTMLElement>("[data-testid=todos-list-column]")!;
const retry = () => [...list().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t("todos.retry"));
const render = async () => { await act(async () => root.render(<TodosView />)); };
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function failReads(reason = unavailable()) {
  api.mockImplementation(async (path) => {
    if (path === "/todos") throw reason;
    return {};
  });
}

async function search(text: string) {
  const input = list().querySelector<HTMLInputElement>("input")!;
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    set.call(input, text);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(async () => {
  await initI18n("zh-Hans");
  api.mockReset();
  failReads();
  seed({ todos: [], todosLoaded: false, todosError: null, todosSupported: true, machines: [], sessions: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  seed(before);
  await initI18n("zh-Hans");
  vi.restoreAllMocks();
});

describe("todo list load failures", () => {
  it("keeps the loading state until the read fails, then explains the failure even with a search", async () => {
    const pending = deferred<{ todos: Todo[] }>();
    api.mockImplementation(async (path) => path === "/todos" ? pending.promise : {});
    await render();
    let loaded!: Promise<void>;
    await act(async () => { loaded = refresh(); });
    expect(list().querySelector("[data-testid=todos-loading]")).toBeTruthy();
    expect(list().textContent).not.toContain(t("todos.empty_title"));
    expect(retry()).toBeUndefined();
    await search("release");

    await act(async () => { pending.reject(unavailable()); await loaded; });
    expect(list().querySelector("[data-testid=todos-loading]")).toBeNull();
    expect(list().textContent).toContain(t("todos.load_failed"));
    expect(list().textContent).toContain(errorText(unavailable()));
    expect(list().textContent).not.toContain(t("todos.empty_title"));
    expect(list().textContent).not.toContain(t("todos.no_match_title"));
    expect(retry()?.disabled).toBe(false);
  });

  it.each([false, true])("retries only todos once while pending, then renders the successful list (populated: %s)", async (populated) => {
    await act(async () => { await refreshTodos(); });
    await render();
    const pending = deferred<{ todos: Todo[] }>();
    api.mockClear();
    api.mockImplementation(async (path) => path === "/todos" ? pending.promise : {});
    const button = retry()!;
    expect(button).toBeTruthy();
    await act(async () => button.click());
    expect(button.disabled).toBe(true);
    expect(button.getAttribute("aria-busy")).toBe("true");
    expect(list().textContent).toContain(errorText(unavailable()));
    await act(async () => { button.click(); button.click(); });
    expect(api.mock.calls.map(([path]) => path)).toEqual(["/todos"]);

    await act(async () => pending.resolve({ todos: populated ? [todo] : [] }));
    await flush();
    expect(list().textContent).not.toContain(t("todos.load_failed"));
    expect(list().textContent).not.toContain(errorText(unavailable()));
    expect(retry()).toBeUndefined();
    expect(list().textContent).toContain(populated ? todo.title : t("todos.empty_title"));
  });

  it("keeps the reason and enables retry again when retry fails", async () => {
    await act(async () => { await refreshTodos(); });
    await render();
    api.mockClear();
    await act(async () => retry()!.click());
    await flush();
    expect(api.mock.calls.map(([path]) => path)).toEqual(["/todos"]);
    expect(list().textContent).toContain(errorText(unavailable()));
    expect(retry()?.disabled).toBe(false);
    expect(list().textContent).not.toContain(t("todos.empty_title"));
  });

  it("hides header counts after an empty failed read and restores them on recovery", async () => {
    await act(async () => { await refreshTodos(); });
    await render();
    const header = host.querySelector("header")!;
    const emptySummary = t("todos.summary_triage", { open: 0, parked: 0, shelved: 0, done: 0, cancelled: 0 });
    expect(header.textContent).not.toContain(emptySummary);

    api.mockImplementation(async (path) => path === "/todos" ? { todos: [] } : {});
    await act(async () => retry()!.click());
    await flush();
    expect(header.textContent).toContain(emptySummary);

    api.mockImplementation(async (path) => path === "/todos" ? { todos: [todo] } : {});
    await act(async () => { await refreshTodos(); });
    const populatedSummary = t("todos.summary_triage", { open: 1, parked: 0, shelved: 0, done: 0, cancelled: 0 });
    expect(header.textContent).toContain(populatedSummary);
    failReads();
    await act(async () => { await refreshTodos(); });
    expect(header.textContent).toContain(populatedSummary);
  });

  it("keeps existing rows and their no-match search state when a refresh fails", async () => {
    seed({ todos: [todo], todosLoaded: true });
    await render();
    await act(async () => { await refreshTodos(); });
    expect(list().textContent).toContain(todo.title);
    expect(list().textContent).not.toContain(errorText(unavailable()));
    expect(retry()).toBeUndefined();
    await search("unrelated query");
    expect(list().textContent).toContain(t("todos.no_match_title"));
    expect(list().textContent).not.toContain(t("todos.load_failed"));
    expect(retry()).toBeUndefined();
  });

  it("keeps unsupported hubs out of the failure state after a 404", async () => {
    await act(async () => { await refreshTodos(); });
    await render();
    expect(retry()).toBeTruthy();
    failReads(new ApiError(404, "not found", {}));
    await act(async () => retry()!.click());
    await flush();
    expect(getState().todosSupported).toBe(false);
    expect(list().textContent).not.toContain(t("todos.load_failed"));
    expect(retry()).toBeUndefined();
  });

  it.each([
    ["en", "Retry"],
    ["zh-Hans", "重试"],
    ["ja", "再試行"],
  ])("shows the failure and retry in %s", async (language, retryLabel) => {
    await initI18n(language);
    await act(async () => { await refreshTodos(); });
    await render();
    expect(t("todos.load_failed")).not.toBe("todos.load_failed");
    expect(list().textContent).toContain(t("todos.load_failed"));
    expect(list().textContent).toContain(errorText(unavailable()));
    expect(retry()?.textContent).toBe(retryLabel);
    expect(list().textContent).not.toContain(t("todos.empty_title"));
  });
});
