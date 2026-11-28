// @vitest-environment jsdom
// Pressing through the #2975 parity pieces: a routine row's right-click menu and the footer that names it, "All" on the
// task dock and the "Today" card opening the task board, the plan card's "Edit" dialog starting with its edits, and the
// todos page's "To-dos" back on a narrow screen popping the entry a row click pushed. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPages } from "../lib/lazy-page";
import type { Task } from "../lib/routines";
import type { Todo } from "../lib/todos";

const media = vi.hoisted(() => ({ noHover: false }));
vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    // read each time: lib/viewport caches one list per query
    get matches() {
      return query === "(hover: none)" ? media.noHover : false;
    },
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const { credentialOwner, getToken } = await import("../lib/api");
const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (...args: Parameters<typeof import("../lib/api").api>) => api(...args),
}));

const startPlan = vi.fn(async (..._args: unknown[]) => {});
vi.mock("../lib/feed", async (orig) => ({
  ...(await orig<typeof import("../lib/feed")>()),
  startPlan: (...args: unknown[]) => startPlan(...args),
}));

const { getState } = await import("../lib/store");
const { Routines } = await import("./assistant/Routines");
const { FeedLineView, PlanCard, TaskDock } = await import("./assistant/Tasks");
const { TodosView } = await import("./Todos");
beforeAll(() => loadPages(), 60_000);

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const render = async (el: React.ReactNode) => {
  await act(async () => root.render(el));
  await flush();
};
const byText = (label: string, sel = "button, [role=menuitem]") =>
  [...document.querySelectorAll<HTMLElement>(sel)].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | undefined | null) => {
  expect(el, "element").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};

const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = (o: Partial<Todo> = {}): Todo => ({ id: "t1", title: "整理发布清单", created_at: iso(7_200_000), updated_at: iso(3_600_000), ...o });
const routine = (o: Partial<Task> = {}): Task => ({ id: "r1", title: "回头看看 CI", prompt: "看 CI", repeat: "once", at: iso(-3_600_000), enabled: true, ...o });

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

beforeEach(() => {
  api.mockReset();
  api.mockImplementation(async () => ({}));
  startPlan.mockClear();
  media.noHover = false;
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

describe("a routine row's menu", () => {
  const withTasks = (tasks: Task[]) =>
    api.mockImplementation(async (path: string) => (path === "/assistant/tasks" ? { tasks } : path === "/assistant/runs" ? { runs: [] } : { session_id: "run-1" }));
  const mutation = (path: string, body: { method: string; json?: unknown }) => {
    const call = api.mock.calls.find(([called]) => called === path);
    expect(call?.[0]).toBe(path);
    const init = call?.[1] as Record<string, unknown> | undefined;
    expect(init && Object.keys(init).sort()).toEqual([...Object.keys(body), "headers", "isAuthContextCurrent", "token", "tokenOwner"].sort());
    for (const [key, value] of Object.entries(body)) expect(init?.[key]).toEqual(value);
    expect(init).toMatchObject({ headers: undefined, token: getToken(), tokenOwner: credentialOwner() });
    expect((init?.isAuthContextCurrent as () => boolean)()).toBe(true);
  };

  it("right click offers 立即运行 / 编辑 / 删除, and 删除 asks once more", async () => {
    withTasks([routine()]);
    await render(<Routines onToggleSidebar={() => {}} />);
    const row = [...document.querySelectorAll<HTMLElement>("[role=button]")].find((r) => r.textContent?.includes("回头看看 CI"));
    expect(row).toBeTruthy();
    await act(async () => void row!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 })));
    await flush();
    const labels = [...document.querySelectorAll("[role=menuitem]")].map((m) => m.textContent?.replace("⌫", "").trim());
    expect(labels).toEqual(["立即运行", "编辑", "删除"]);
    await click([...document.querySelectorAll("[role=menuitem]")].find((m) => m.textContent?.startsWith("删除")));
    expect(api.mock.calls.some(([, init]) => (init as { method?: string } | undefined)?.method === "DELETE")).toBe(false);
    const asking = [...document.querySelectorAll("[role=menuitem]")].find((m) => m.textContent?.startsWith("确认删除"));
    expect(asking).toBeTruthy();
    await click(asking);
    mutation("/assistant/tasks/r1", { method: "DELETE" });
  });

  it("立即运行 drops a one-off from the list as it runs", async () => {
    withTasks([routine()]);
    await render(<Routines onToggleSidebar={() => {}} />);
    const row = [...document.querySelectorAll<HTMLElement>("[role=button]")].find((r) => r.textContent?.includes("回头看看 CI"))!;
    await act(async () => void row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })));
    await flush();
    withTasks([]); // the hub has run it
    await click(byText("立即运行"));
    mutation("/assistant/tasks/r1/run", { method: "POST", json: {} });
    expect(document.body.textContent).not.toContain("回头看看 CI");
  });

  it("names the right click under 一次性跟进 only where there is one", async () => {
    withTasks([routine()]);
    await render(<Routines onToggleSidebar={() => {}} />);
    expect(document.body.textContent).toContain("右键点击即可立即运行或删除");
    await act(async () => root.unmount());
    media.noHover = true;
    root = createRoot(host);
    await render(<Routines onToggleSidebar={() => {}} />);
    expect(document.body.textContent).toContain("这些任务由 Persona 在对话中自行安排，到点后会自动唤醒并继续处理。");
    expect(document.body.textContent).not.toContain("右键");
  });
});

describe("全部 opens the task board", () => {
  it("from the dock, as a dialog, not the todos page", async () => {
    seed({ todos: [todo({ origin: "c1" })], sessions: {} });
    const push = vi.spyOn(history, "pushState");
    await render(<TaskDock conversation="c1" />);
    const all = document.querySelector('[data-testid="task-dock-board"]');
    expect(all?.getAttribute("aria-haspopup")).toBe("dialog");
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    await click(all);
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("整理发布清单");
    expect(push.mock.calls.some(([, , url]) => String(url).includes("/todos"))).toBe(false);
  });

  it("from the 今天 card's working row", async () => {
    seed({ todos: [todo({ origin: "c1" })], sessions: {} });
    await render(<FeedLineView item={{ id: "f1", conversation: "c1", at: iso(0), kind: "today", body: { date: "2026-11-05", working: 1 } }} />);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    await click(byText("全部"));
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("整理发布清单");
  });
});

describe("the plan card's 编辑", () => {
  const plan = { id: "p1", conversation: "c1", state: "open" as const, tasks: [{ title: "补测试", prompt: "补", machine: "m1", cwd: "/w", model: "opus" }] };

  it("starts with the edits, leaving out a permission the plan never set", async () => {
    seed({ machines: [{ id: "m1", name: "工作机" }] });
    await render(<PlanCard plan={plan} />);
    await click(byText("编辑"));
    const title = document.querySelector<HTMLInputElement>("#plan-p1-0-title")!;
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      set.call(title, "补单元测试");
      title.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(document.querySelector<HTMLInputElement>("#plan-p1-0-model")?.placeholder).toBe("留空则保留 Persona 选择的 opus");
    const start = [...document.querySelectorAll<HTMLElement>('[role="dialog"] button')].filter((b) => b.textContent?.trim() === "开始").at(-1);
    await click(start);
    expect(startPlan).toHaveBeenCalledTimes(1);
    const [, remember, edits] = startPlan.mock.calls[0] as [unknown, boolean, Record<string, unknown>[]];
    expect(remember).toBe(false);
    expect(edits).toHaveLength(1);
    expect(edits[0]).toMatchObject({ title: "补单元测试", machine: "m1", cwd: "/w", model: "opus", prompt: "补" });
    expect(edits[0]).not.toHaveProperty("permission");
  });

  it("asks before throwing changes away", async () => {
    seed({ machines: [{ id: "m1", name: "工作机" }] });
    const ask = vi.spyOn(window, "confirm").mockReturnValue(false);
    await render(<PlanCard plan={plan} />);
    await click(byText("编辑"));
    await click(byText("取消")); // nothing changed: closes without asking
    expect(ask).not.toHaveBeenCalled();
    expect(document.querySelector('[data-testid="plan-edit"]')).toBeNull();
    await click(byText("编辑"));
    const prompt = document.querySelector<HTMLTextAreaElement>("#plan-p1-0-prompt")!;
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    await act(async () => {
      set.call(prompt, "补完再跑一遍");
      prompt.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(byText("取消"));
    expect(ask).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[data-testid="plan-edit"]')).toBeTruthy();
    expect(startPlan).not.toHaveBeenCalled();
  });
});

describe("a todo's sections", () => {
  it("names its agents in the page's language, like 决策 and 项目 (#4039)", async () => {
    seed({ todos: [todo()], todosLoaded: true, sessions: {} });
    await render(<TodosView selectedId="t1" />);
    const heads = [...document.querySelectorAll("div, span")].map((e) => e.textContent?.trim()).filter((s) => s && /^[^·]+ · \d+$/.test(s));
    expect(heads).toContain("Agent · 0");
    expect(document.body.textContent).not.toContain("AGENTS");
  });
});

describe("待办 back on a narrow screen", () => {
  it("pops the entry a row click pushed", async () => {
    seed({ todos: [todo()], todosLoaded: true, sessions: {} });
    const back = vi.spyOn(history, "back").mockImplementation(() => {});
    await render(<TodosView />);
    await click(document.querySelector('[data-todo="t1"] [data-testid="todo-row-open"]') ?? document.querySelector('[data-todo="t1"]'));
    await render(<TodosView selectedId="t1" />); // the route now says ?id=t1
    await click(document.querySelector('[data-testid="todos-back"] button'));
    expect(back).toHaveBeenCalledTimes(1);
  });

  it("replaces a deep link's entry instead of stacking another", async () => {
    seed({ todos: [todo()], todosLoaded: true, sessions: {} });
    const back = vi.spyOn(history, "back").mockImplementation(() => {});
    const replace = vi.spyOn(history, "replaceState");
    history.replaceState(null, "", "/todos?id=t1");
    replace.mockClear();
    await render(<TodosView selectedId="t1" />);
    await click(document.querySelector('[data-testid="todos-back"] button'));
    expect(back).not.toHaveBeenCalled();
    expect(replace).toHaveBeenCalled();
    expect(String(replace.mock.calls.at(-1)?.[2])).toMatch(/\/todos$/);
  });
});
