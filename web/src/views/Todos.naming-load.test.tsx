// @vitest-environment jsdom
// Fail the module import while the real Todo detail has dirty fields. Neither
// lazyPage, PageBoundary, the Todo editors nor their REST client is replaced.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";

const chunk = vi.hoisted(() => {
  window.matchMedia = ((media: string) => ({
    matches: false, media, onchange: null, addEventListener() {}, removeEventListener() {},
    addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
  let reject!: (error: Error) => void;
  const pending = new Promise<void>((_, no) => { reject = no; });
  return { pending, reject };
});
vi.mock("../ui/WorkspaceNaming", async (original) => { await chunk.pending; return original(); });

import { getState } from "../lib/store";
import { getToken, setToken } from "../lib/api";
import { initI18n, t } from "../i18n";
import { PageBoundary } from "./PageBoundary";
import { TodosView } from "./Todos";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("keeps the same Todo title and notes mounted and saveable after its naming import fails", async () => {
  await initI18n("en");
  const previous = { ...getState() }, token = getToken();
  const todo: Todo = { id: "todo-example", title: "Saved title", notes: "Saved notes",
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" };
  const writes: unknown[] = [];
  const json = (data: unknown) => new Response(JSON.stringify(data));
  setToken("synthetic-naming-owner");
  Object.assign(getState(), { todos: [{ ...todo }], todosLoaded: true, todosError: null, todosSupported: true,
    machines: [], sessions: {}, interactions: [], answered: {} });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), "http://hub").pathname.replace("/api/v1", "");
    if (path === "/todos/todo-example" && init?.method === "PATCH") {
      const patch = JSON.parse(String(init.body)); writes.push(patch); Object.assign(todo, patch);
      return json({ todo: { ...todo } });
    }
    return json(path === "/todos" ? { todos: [{ ...todo }] } : {});
  }));
  vi.spyOn(console, "error").mockImplementation(() => {});
  // PageBoundary's persistent-failure state, following its existing one-time reload.
  sessionStorage.setItem("persona.chunkReload", String(Date.now()));
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
  const detail = () => host.querySelector('[data-testid="todos-detail-column"]');
  const field = (name: "title" | "notes") => detail()!.querySelector<HTMLInputElement | HTMLTextAreaElement>(name === "title" ? "input" : "textarea")!;
  const edit = async (name: "title" | "notes", value: string) => {
    const node = field(name), proto = name === "title" ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
    await act(async () => {
      Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(node, value);
      node.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  try {
    await act(async () => root.render(<PageBoundary page="/todos"><TodosView selectedId={todo.id} /></PageBoundary>));
    await flush();
    const mounted = detail(), title = field("title"), notes = field("notes");
    await edit("title", "Unsubmitted title");
    await edit("notes", "Unsubmitted notes");
    expect(writes).toEqual([]);
    await act(async () => chunk.reject(new TypeError("Failed to fetch dynamically imported module: WorkspaceNaming")));
    await flush();
    expect(detail()).toBe(mounted);
    expect(field("title")).toBe(title);
    expect(field("notes")).toBe(notes);
    expect(title.value).toBe("Unsubmitted title");
    expect(notes.value).toBe("Unsubmitted notes");
    expect(detail()!.querySelector('[role="alert"]')!.textContent).toContain(t("pageboundary.reload"));
    expect(writes).toEqual([]);
    await edit("title", "Edited after failure");
    await edit("notes", "Notes after failure");
    for (const name of ["title", "notes"] as const) {
      await act(async () => field(name).dispatchEvent(new FocusEvent("focusout", { bubbles: true })));
      await flush();
    }
    expect(writes).toEqual([{ title: "Edited after failure" }, { notes: "Notes after failure" }]);
    expect(todo).toMatchObject({ title: "Edited after failure", notes: "Notes after failure" });
    expect(detail()).toBe(mounted);
    expect(notes.value).toBe("Notes after failure");
  } finally {
    await act(async () => root.unmount()); host.remove();
    Object.assign(getState(), previous); setToken(token);
    sessionStorage.removeItem("persona.chunkReload");
  }
});
