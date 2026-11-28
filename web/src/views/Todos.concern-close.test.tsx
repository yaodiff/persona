// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import { getState } from "../lib/store";
import type { Todo } from "../lib/todos";
import { TodosView } from "./Todos";

vi.hoisted(() => {
  window.matchMedia = ((media: string) => ({
    matches: false, media, onchange: null, addEventListener() {}, removeEventListener() {},
    addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const toast = vi.hoisted(() => vi.fn());
vi.mock("../ui/atoms", async (original) => ({ ...(await original<typeof import("../ui/atoms")>()), toast }));

const previous = { ...getState() };
const concern: Todo = { id: "todo_concern", title: "Check whether the delivery arrived", kind: "concern",
  created_at: "2026-11-10T08:00:00Z", updated_at: "2026-11-10T08:00:00Z" };
const ordinary: Todo = { ...concern, id: "todo_ordinary", title: "Water the plants", kind: undefined };
const concernPath = `/api/v1/todos/${concern.id}`;
const decisionsPath = `${concernPath}/decisions`;
const reportText = "The delivery arrived on Tuesday.";
const reportError = "The report could not be saved";
let host: HTMLDivElement;
let root: Root;
let hubTodo: Todo;
let rejectReport: boolean;

const assistant = (todosV2: boolean) => ({ machine_id: "computer", main: "main", conversations: [], todos_v2: todosV2 });
let hubAssistant = assistant(false);

const fetchHub = vi.fn(async (input: string, init?: RequestInit) => {
  const { pathname } = new URL(input, window.location.href);
  const method = init?.method ?? "GET";
  if (method === "GET") {
    if (pathname === "/api/v1/todos") return Response.json({ todos: [hubTodo] });
    if (pathname === "/api/v1/assistant") return Response.json(hubAssistant);
    if (pathname.endsWith("/decisions")) return Response.json({ decisions: [] });
    return Response.json({});
  }
  if (pathname === decisionsPath && method === "POST") {
    if (rejectReport) return Response.json({ error: reportError }, { status: 500 });
    const now = new Date().toISOString();
    hubTodo = { ...hubTodo, done_at: now, done_by: "reported", updated_at: now };
    return Response.json({ todo: hubTodo, closed: true });
  }
  if (pathname === `/api/v1/todos/${hubTodo.id}` && method === "PATCH") {
    const { done } = JSON.parse(String(init?.body)) as { done: boolean };
    if (done && hubTodo.kind === "concern") {
      return Response.json({ error: "a concern closes only once its outcome was reported to the owner" }, { status: 409 });
    }
    hubTodo = { ...hubTodo, done_at: done ? new Date().toISOString() : undefined };
    return Response.json({ todo: hubTodo });
  }
  return Response.json({ error: "Unexpected request" }, { status: 400 });
});

const writes = () => fetchHub.mock.calls.flatMap(([url, init]) =>
  init?.method === "PATCH" || init?.method === "POST"
    ? [{ path: new URL(url, window.location.href).pathname, method: init.method, body: JSON.parse(String(init.body)) as unknown }]
    : [],
);

beforeAll(async () => { await initI18n("en"); });

beforeEach(() => {
  fetchHub.mockClear();
  toast.mockClear();
  rejectReport = false;
  vi.stubGlobal("fetch", fetchHub);
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  localStorage.setItem("persona.todos.grouping", "status");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  localStorage.clear();
  Object.assign(getState(), previous);
  vi.unstubAllGlobals();
});

const renderTodo = async (todo: Todo, { slim = false, detail = false } = {}) => {
  hubTodo = todo;
  hubAssistant = assistant(slim);
  Object.assign(getState(), { assistant: hubAssistant, todos: [todo], todosLoaded: true });
  await act(async () => root.render(<TodosView selectedId={detail ? todo.id : undefined} />));
  fetchHub.mockClear();
};

const row = (id = concern.id) => host.querySelector<HTMLElement>(`[data-todo="${id}"]`)!;
const checkbox = (id = concern.id) => row(id).querySelector<HTMLButtonElement>("button[aria-label]")!;
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const detailButton = (...labels: string[]) =>
  [...host.querySelectorAll<HTMLButtonElement>("[data-testid=todos-detail-column] button")]
    .find((button) => labels.includes(button.textContent?.trim() ?? ""))!;
const submitButton = () => dialog()?.querySelector<HTMLButtonElement>("button[type=submit]");
const click = async (button: HTMLButtonElement) => {
  expect(button).toBeTruthy();
  await act(async () => button.click());
};
const enterReport = async (text: string) => {
  const textarea = dialog()?.querySelector("textarea");
  expect(textarea).toBeTruthy();
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(textarea, text);
    textarea!.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

describe("closing a concern in To-dos", () => {
  it.each([
    { entry: "standard row", slim: false, layout: undefined, todo: concern },
    { entry: "compact row", slim: true, layout: "personal-slim", todo: concern },
    { entry: "slim task row", slim: true, layout: "task-slim", todo: { ...concern, item_type: "task" as const } },
  ])("opens the report dialog from the $entry before writing", async ({ slim, layout, todo }) => {
    await renderTodo(todo, { slim });
    expect(row().dataset.todoRowLayout).toBe(layout);
    await click(checkbox());
    expect(writes()).toEqual([]);
    expect(checkbox().getAttribute("aria-label")).toBe(t("todos.report_concern_action"));
    expect(dialog()?.querySelector("h2")?.textContent).toBe(t("todos.report_concern_title"));
  });

  it("opens the report dialog from the detail action before writing", async () => {
    await renderTodo(concern, { detail: true });
    const button = detailButton(t("todos.report_concern_action"), t("todos.complete"));
    await click(button);
    expect(writes()).toEqual([]);
    expect(button.textContent).toBe(t("todos.report_concern_action"));
    expect(dialog()?.querySelector("h2")?.textContent).toBe(t("todos.report_concern_title"));
  });

  it("opens the same report dialog from the row menu", async () => {
    await renderTodo(concern);
    await act(async () => {
      row().dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 40, clientY: 120 }));
    });
    const item = document.querySelector<HTMLButtonElement>("[role=menuitem]")!;
    await click(item);
    expect(writes()).toEqual([]);
    expect(item.textContent?.trim()).toBe(t("todos.report_concern_action"));
    expect(dialog()?.querySelector("h2")?.textContent).toBe(t("todos.report_concern_title"));
  });

  it("blocks empty and whitespace-only reports even when submitting the form directly", async () => {
    await renderTodo(concern);
    await click(checkbox());
    for (const text of ["", " \n\t "]) {
      await enterReport(text);
      expect(submitButton()?.disabled).toBe(true);
      await act(async () => dialog()!.querySelector("form")!.requestSubmit());
      expect(writes()).toEqual([]);
    }
  });

  it("posts the trimmed outcome, refreshes the list and app, and confirms success", async () => {
    await renderTodo(concern, { detail: true });
    await click(checkbox());
    await enterReport(`  ${reportText}\n `);
    await click(submitButton()!);
    expect(writes()).toEqual([{ path: decisionsPath, method: "POST", body: { source: "reported", choice: reportText } }]);
    expect(fetchHub.mock.calls.map(([url]) => new URL(url, window.location.href).pathname))
      .toEqual(expect.arrayContaining(["/api/v1/todos", "/api/v1/interactions"]));
    expect(detailButton(t("todos.reopen"))).toBeTruthy();
    expect(dialog()).toBeNull();
    expect(toast).toHaveBeenCalledWith(t("todos.report_concern_success"), "ok");
  });

  it("keeps the draft in the open dialog if the report fails", async () => {
    rejectReport = true;
    await renderTodo(concern);
    await click(checkbox());
    await enterReport(reportText);
    await click(submitButton()!);
    expect(writes()).toEqual([{ path: decisionsPath, method: "POST", body: { source: "reported", choice: reportText } }]);
    expect(dialog()?.querySelector("textarea")?.value).toBe(reportText);
    expect(submitButton()?.disabled).toBe(false);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining(reportError), "error");
  });

  it("reopens a closed concern with a PATCH", async () => {
    await renderTodo({ ...concern, done_at: concern.updated_at, done_by: "reported" }, { detail: true });
    await click(detailButton(t("todos.reopen")));
    expect(writes()).toEqual([{ path: concernPath, method: "PATCH", body: { done: false } }]);
    expect(dialog()).toBeNull();
  });

  it("completes an ordinary open to-do immediately with a PATCH", async () => {
    await renderTodo(ordinary);
    const button = checkbox(ordinary.id);
    expect(button.getAttribute("aria-label")).toBe(t("todos.complete_todo"));
    await click(button);
    expect(writes()).toEqual([{ path: `/api/v1/todos/${ordinary.id}`, method: "PATCH", body: { done: true } }]);
    expect(dialog()).toBeNull();
  });
});
