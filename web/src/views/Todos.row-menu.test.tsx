// @vitest-environment jsdom
// A to-do row's menu (#4429): a right click or Shift+F10 on a row opens it; its choices act on that row's to-do, not
// the one open beside the list, through the hub's existing update and delete calls and the page's own dialogs. The
// hub is faked; the data made up.
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

const { dropTodo, getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const click = async (el: Element | undefined | null) => {
  expect(el, "element").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const listText = () => document.querySelector<HTMLElement>("[data-testid=todos-list-column]")?.textContent ?? "";
const row = (title: string) =>
  [...document.querySelectorAll<HTMLElement>("[data-testid=todos-list-column] [data-todo]")].find((r) => r.textContent?.includes(title));
const menuItems = () => [...document.querySelectorAll<HTMLElement>("[role=menuitem], [role=menuitemradio]")];
const menuItem = (label: string) => menuItems().find((m) => m.textContent?.trim().startsWith(label));
const button = (label: string, scope: ParentNode = document) =>
  [...scope.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent?.trim() === label);
const writes = (method: string) =>
  api.mock.calls.filter(([, init]) => (init as { method?: string } | undefined)?.method === method).map(([path, init]) => [path, (init as { json?: unknown }).json]);

const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = (id: string, title: string): Todo => ({ id, title, created_at: iso(7_200_000), updated_at: iso(3_600_000) });

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

// the hub's to-dos: a DELETE takes one away, a read of /todos answers what is left
let hub: Todo[] = [];
const fakeHub = async (path: string, init?: unknown): Promise<unknown> => {
  const method = (init as { method?: string } | undefined)?.method;
  if (method === "DELETE") hub = hub.filter((x) => path !== `/todos/${x.id}`);
  if (path === "/todos" && !method) return { todos: hub };
  return path.endsWith("/launchers") ? { launchers: [] } : {};
};

beforeEach(async () => {
  api.mockReset();
  api.mockImplementation(fakeHub);
  hub = [todo("t1", "Release checklist"), todo("t2", "deploy web")];
  seed({ todos: [...hub], todosLoaded: true, machines: [], sessions: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<TodosView selectedId="t1" />));
  await flush();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
  vi.restoreAllMocks();
});

/** Right-clicks the row of the to-do not open beside the list. */
const rightClick = async (title = "deploy web") => {
  const el = row(title);
  expect(el, `row ${title}`).toBeTruthy();
  await act(async () => void el!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 40, clientY: 120 })));
  await flush();
};

describe("a to-do row's menu", () => {
  it("opens on a right click with the page's actions, and keeps the browser's own menu away", async () => {
    const ev = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 40, clientY: 120 });
    await act(async () => void row("deploy web")!.dispatchEvent(ev));
    await flush();
    expect(ev.defaultPrevented).toBe(true);
    expect(menuItems().map((m) => m.textContent?.trim())).toEqual(["完成待办", "优先级", "截止日期", "提醒", "交给 Agent…", "删除待办…"]);
  });

  it("opens from the keyboard with Shift+F10 on a focused row", async () => {
    const el = row("deploy web")!.querySelector<HTMLButtonElement>("[data-testid=todo-row-open]")!;
    el.focus();
    await act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true, cancelable: true })));
    await flush();
    expect(menuItem("完成待办")).toBeTruthy();
    // and the row was not opened, as Enter would
    expect(document.querySelector<HTMLInputElement>("[data-testid=todos-detail-column] input[type=text]")?.value).toBe("Release checklist");
  });

  it("completes the right-clicked to-do, not the open one", async () => {
    await rightClick();
    await click(menuItem("完成待办"));
    expect(writes("PATCH")).toEqual([["/todos/t2", { done: true }]]);
    expect(menuItems()).toEqual([]);
  });

  it("sets the priority from its submenu", async () => {
    await rightClick();
    await click(menuItem("优先级"));
    const high = menuItems().find((m) => m.getAttribute("role") === "menuitemradio" && m.textContent?.trim() === "高");
    await click(high);
    expect(writes("PATCH")).toEqual([["/todos/t2", { priority: "high" }]]);
  });

  it("picks any due day in a dialog", async () => {
    await rightClick();
    await click(menuItem("截止日期"));
    await click(menuItem("选择日期…"));
    expect(dialog()?.textContent).toContain("截止日期");
    const input = document.querySelector<HTMLInputElement>("[data-testid=todo-due-pick]")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "2026-12-01");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(button("保存", dialog()!));
    expect(writes("PATCH")).toEqual([["/todos/t2", { due_date: "2026-12-01" }]]);
    expect(dialog()).toBeNull();
  });

  it("keeps the day picker open when the save fails", async () => {
    api.mockImplementation(async (path: string, init?: unknown) => {
      if ((init as { method?: string } | undefined)?.method === "PATCH") throw new Error("hub down");
      return fakeHub(path, init);
    });
    await rightClick();
    await click(menuItem("截止日期"));
    await click(menuItem("选择日期…"));
    await click(button("保存", dialog()!));
    expect(writes("PATCH")).toHaveLength(1);
    expect(dialog()).toBeTruthy();
  });

  it("closes when its to-do goes away elsewhere", async () => {
    await rightClick();
    expect(menuItems().length).toBeGreaterThan(0);
    await act(async () => dropTodo("t2"));
    await flush();
    expect(menuItems()).toEqual([]);
  });

  it("hands the right-clicked to-do over in the hand-over dialog", async () => {
    await rightClick();
    await click(menuItem("交给 Agent…"));
    expect(dialog()?.textContent).toContain("deploy web");
  });

  it("deletes after the confirmation dialog, and the open to-do stays open", async () => {
    const replace = vi.spyOn(history, "replaceState");
    await rightClick();
    await click(menuItem("删除待办…"));
    expect(writes("DELETE")).toEqual([]);
    expect(dialog()?.textContent).toContain("deploy web");
    await click(button("删除", dialog()!));
    expect(writes("DELETE")).toEqual([["/todos/t2", undefined]]);
    expect(dialog()).toBeNull();
    expect(listText()).not.toContain("deploy web");
    expect(document.querySelector<HTMLInputElement>("[data-testid=todos-detail-column] input[type=text]")?.value).toBe("Release checklist");
    expect(replace).not.toHaveBeenCalled();
  });

  it("deletes the open to-do from its row as its page's ⋯ does", async () => {
    await rightClick("Release checklist");
    await click(menuItem("删除待办…"));
    expect(dialog()?.textContent).toContain("Release checklist");
    await click(button("删除", dialog()!));
    expect(writes("DELETE")).toEqual([["/todos/t1", undefined]]);
    expect(getState().todos.map((t) => t.id)).toEqual(["t2"]);
  });
});
