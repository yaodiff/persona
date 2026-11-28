// @vitest-environment jsdom
// The todo page shows the chat thread a todo is bound to and lets the user unbind it (#5279): a line "Lark topic · <title>",
// an Unbind button that asks first, a mark on the list row, and a note on how to bind when there is none. A hub from before
// threads (no `todo_threads` feature in hello, or a route-level 404 on the unbind) shows nothing of it. The hub is faked;
// the data made up.
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

const toastSpy = vi.fn();
vi.mock("../ui/atoms", async (orig) => ({
  ...(await orig<typeof import("../ui/atoms")>()),
  toast: (...args: unknown[]) => toastSpy(...args),
}));

const { ApiError } = await import("../lib/api");
const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const button = (label: string, scope: ParentNode = document) =>
  [...scope.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | undefined | null) => {
  expect(el, "element").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const section = () => document.querySelector<HTMLElement>("[data-testid=todo-thread]");
const marks = () => document.querySelectorAll("[data-testid=todo-thread-mark]").length;
const threadDeletes = () => api.mock.calls.filter(([path, init]) => path.endsWith("/thread") && (init as { method?: string } | undefined)?.method === "DELETE");

const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = (id: string, title: string, patch: Partial<Todo> = {}): Todo => ({ id, title, created_at: iso(7_200_000), updated_at: iso(3_600_000), ...patch });
const bound = { channel: "feishu", chat_id: "oc_test", thread_id: "omt_test", title: "测试话题" };

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

const open = async (id: string) => {
  await act(async () => root.render(<TodosView selectedId={id} />));
  await flush();
};

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => ((resolve = res), (reject = rej)));
  return { promise, resolve, reject };
}

beforeEach(() => {
  api.mockReset();
  toastSpy.mockReset();
  api.mockImplementation(async (path: string) => (path.endsWith("/launchers") ? { launchers: [] } : path.endsWith("/decisions?all=1") ? { decisions: [] } : {}));
  seed({ todos: [todo("t1", "Release checklist", { thread: bound }), todo("t2", "deploy web")], todosLoaded: true, machines: [], sessions: {}, features: ["todo_threads"] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
  vi.restoreAllMocks();
});

describe("a todo's chat thread", () => {
  it("shows the thread a todo is bound to, and marks only that todo in the list", async () => {
    await open("t1");
    expect(section()?.textContent).toContain("飞书话题 · 测试话题");
    expect(button("解绑", section()!)).toBeTruthy();
    expect(marks()).toBe(1);
  });

  it("says how to bind when the todo has no thread, with nothing to unbind", async () => {
    await open("t2");
    expect(section()?.textContent).toContain("绑定到对应话题");
    expect(section()?.textContent).not.toContain("todo_bind_thread");
    expect(section()?.textContent).toContain("/topics");
    expect(button("解绑", section()!)).toBeUndefined();
  });

  it("names the thread by its short id when it has no title", async () => {
    seed({ todos: [todo("t1", "Release checklist", { thread: { ...bound, title: "", thread_id: "omt_1a2b3c4d5e6f" } })] });
    await open("t1");
    expect(section()?.textContent).toContain("飞书话题 · omt_1a…5e6f");
  });

  it("asks before unbinding, and sends nothing when the user cancels", async () => {
    await open("t1");
    await click(button("解绑", section()!));
    expect(dialog()?.textContent).toContain("测试话题");
    await click(button("取消", dialog()!));
    expect(dialog()).toBeNull();
    expect(threadDeletes()).toHaveLength(0);
  });

  it("unbinds on confirmation, closes the dialog and refreshes the todos", async () => {
    await open("t1");
    await click(button("解绑", section()!));
    api.mockImplementation(async (path: string, init?: unknown) => {
      if ((init as { method?: string } | undefined)?.method === "DELETE") return { todo: todo("t1", "Release checklist") };
      if (path === "/todos") return { todos: [todo("t1", "Release checklist"), todo("t2", "deploy web")] };
      return path.endsWith("/launchers") ? { launchers: [] } : { decisions: [] };
    });
    await click(button("解绑", dialog()!));
    expect(threadDeletes().map(([p]) => p)).toEqual(["/todos/t1/thread"]);
    expect(dialog()).toBeNull();
    expect(section()?.textContent).not.toContain("飞书话题");
    expect(marks()).toBe(0);
  });

  it("keeps the dialog and the thread when the unbind fails for a reason of its own", async () => {
    await open("t1");
    await click(button("解绑", section()!));
    api.mockImplementation(async (_path: string, init?: unknown) => {
      if ((init as { method?: string } | undefined)?.method === "DELETE") throw new ApiError(500, "boom", { error: "boom" });
      return {};
    });
    await click(button("解绑", dialog()!));
    expect(dialog()).toBeTruthy();
    expect(section()?.textContent).toContain("飞书话题 · 测试话题");
  });

  it("hides the section without a word on a hub with no thread route", async () => {
    await open("t1");
    await click(button("解绑", section()!));
    api.mockImplementation(async (_path: string, init?: unknown) => {
      if ((init as { method?: string } | undefined)?.method === "DELETE") throw new ApiError(404, "Not Found", { code: "not_found", error: "Not Found" });
      return {};
    });
    await click(button("解绑", dialog()!));
    expect(section()).toBeNull();
    expect(dialog()).toBeNull();
  });

  it("does not hide the section when the 404 is the todo's own (it is gone)", async () => {
    await open("t1");
    await click(button("解绑", section()!));
    api.mockImplementation(async (_path: string, init?: unknown) => {
      if ((init as { method?: string } | undefined)?.method === "DELETE") throw new ApiError(404, "todo not found", { error: "todo not found" });
      return {};
    });
    await click(button("解绑", dialog()!));
    expect(section()).toBeTruthy();
  });

  it("shows nothing of threads when the hub does not say it has them", async () => {
    seed({ features: ["locale"] });
    await open("t1");
    expect(section()).toBeNull();
    expect(marks()).toBe(0);
    await act(async () => root.render(<TodosView selectedId="t2" />));
    await flush();
    expect(section()).toBeNull();
  });

  it("lets the user try again after a failed unbind, and says it failed", async () => {
    await open("t1");
    await click(button("解绑", section()!));
    api.mockImplementation(async (_path: string, init?: unknown) => {
      if ((init as { method?: string } | undefined)?.method === "DELETE") throw new ApiError(500, "boom", { error: "boom" });
      return {};
    });
    await click(button("解绑", dialog()!));
    expect(toastSpy.mock.calls.some(([text, tone]) => String(text).includes("解绑失败") && tone === "error")).toBe(true);
    expect(button("解绑", dialog()!)?.hasAttribute("disabled")).toBe(false);
    expect(button("取消", dialog()!)?.hasAttribute("disabled")).toBe(false);
    await click(button("解绑", dialog()!));
    expect(threadDeletes()).toHaveLength(2);
  });

  it("sends one DELETE for a double click, and cannot be cancelled while it is out", async () => {
    await open("t1");
    await click(button("解绑", section()!));
    const del = deferred<unknown>();
    api.mockImplementation(async (_path: string, init?: unknown) => {
      if ((init as { method?: string } | undefined)?.method === "DELETE") return del.promise;
      return {};
    });
    const confirm = button("解绑", dialog()!)!;
    await click(confirm);
    await click(confirm);
    expect(threadDeletes()).toHaveLength(1);
    expect(button("取消", dialog()!)?.hasAttribute("disabled")).toBe(true);
    await act(async () => del.resolve({ todo: todo("t1", "Release checklist") }));
    await flush();
    expect(dialog()).toBeNull();
  });
});
