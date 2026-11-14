// @vitest-environment jsdom
// Link to to-do: Escape in "New to-do…" cancels only the new to-do (the dialog stays for the next Escape), and every
// row says where its to-do stands and when it last changed, so two with one title can be told apart; a long list can
// be searched.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getState } from "../lib/store";
import type { Todo } from "../lib/todos";
import type { Session } from "../lib/types";
import { LINK_SEARCH_FROM, LinkTodoDialog, matchTodos } from "./LinkTodoDialog";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Fixed local noon keeps the 26-hour-old fixture on yesterday, even when this suite runs after midnight.
const NOW = new Date(2026, 0, 15, 12).getTime();
const minutesAgo = (m: number) => new Date(NOW - m * 60_000).toISOString();
const todo = (id: string, title: string, patch: Partial<Todo> = {}): Todo => ({ id, title, created_at: minutesAgo(600), updated_at: minutesAgo(5), ...patch });
const session: Session = { machine_id: "mac", session_id: "s1", title: "菜谱网站加搜索", cwd: "/Users/me/code/recipes", status: "running" };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

let host: HTMLDivElement;
let root: Root;
let closed: number;
const before = { todos: getState().todos, sessions: getState().sessions };

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  closed = 0;
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(json({ items: [{ id: "blog", name: "个人博客" }] }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  Object.assign(getState(), before);
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const open = (todos: Todo[]) => {
  Object.assign(getState(), { todos });
  return act(async () => root.render(<LinkTodoDialog open session={session} onClose={() => closed++} />));
};
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const rows = () => [...document.querySelectorAll<HTMLButtonElement>("[data-link-todos] button")];
const escape = (el: Element) => act(() => {
  el.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
});
const type = (el: HTMLInputElement, value: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});

describe("Link to to-do", () => {
  it("Escape in the new to-do's title cancels only that; the next Escape closes the dialog", async () => {
    await open([todo("a", "整理发布说明")]);
    const create = [...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "新建待办…")!;
    act(() => create.click());
    const input = dialog()!.querySelector<HTMLInputElement>('input[placeholder="输入新待办标题…"]')!;
    type(input, "abc");

    escape(input);
    expect(closed).toBe(0);
    expect(dialog()).not.toBeNull();
    expect(dialog()!.querySelector('input[placeholder="输入新待办标题…"]')).toBeNull();
    // the keyboard is back on "New to-do…", not dropped on the page under the dialog
    expect(document.activeElement?.textContent).toBe("新建待办…");

    escape(document.activeElement!);
    expect(closed).toBe(1);
  });

  it("tells two to-dos with one title apart by where each stands, its project and when it last changed", async () => {
    const waiting = todo("a", "博客迁移到 Astro", { project: "blog", updated_at: minutesAgo(3) });
    const done = todo("b", "博客迁移到 Astro", { updated_at: minutesAgo(26 * 60), agents: [] });
    await open([waiting, done]);
    await act(async () => { await new Promise((r) => setTimeout(r, 0)); }); // the projects' names
    const [first, second] = rows().map((r) => r.querySelectorAll("span span")[1]?.textContent ?? "");
    expect(rows().map((r) => r.querySelector("span span")?.textContent)).toEqual(["博客迁移到 Astro", "博客迁移到 Astro"]);
    expect(first).toContain("个人博客");
    expect(first).toMatch(/3 ?分钟前/);
    expect(second).not.toBe(first);
    expect(second).toContain("昨天");
    // the To-dos page's word for where it stands
    expect(first.split(" · ")[0]).toBe("未开始");
  });

  it("has no search for a list its box shows whole, and one that narrows a longer list by title", async () => {
    // the box shows five two-line rows: a list that needs no scrolling needs no search
    expect(LINK_SEARCH_FROM).toBe(6);
    await open(Array.from({ length: LINK_SEARCH_FROM - 1 }, (_, i) => todo(`s${i}`, `待办 ${i}`)));
    expect(dialog()!.querySelector('input[placeholder="搜索未完成的待办"]')).toBeNull();
    act(() => root.unmount());
    root = createRoot(host);

    const many = Array.from({ length: LINK_SEARCH_FROM }, (_, i) => todo(`t${i}`, `待办 ${i}`));
    await open([...many, todo("blog", "博客迁移到 Astro")]);
    const search = dialog()!.querySelector<HTMLInputElement>('input[placeholder="搜索未完成的待办"]')!;
    expect(search).not.toBeNull();
    type(search, "astro");
    expect(rows().map((r) => r.querySelector("span span")?.textContent)).toEqual(["博客迁移到 Astro"]);
    type(search, "没有这个");
    expect(rows()).toHaveLength(0);
    expect(dialog()!.textContent).toContain("没有符合的待办");
  });

  it("Escape in the search empties it and keeps the dialog; with nothing typed, Escape closes the dialog", async () => {
    await open([...Array.from({ length: LINK_SEARCH_FROM }, (_, i) => todo(`t${i}`, `待办 ${i}`)), todo("blog", "博客迁移到 Astro")]);
    const search = dialog()!.querySelector<HTMLInputElement>('input[placeholder="搜索未完成的待办"]')!;
    type(search, "astro");
    expect(rows()).toHaveLength(1);

    escape(search);
    expect(closed).toBe(0);
    expect(dialog()).not.toBeNull();
    expect(search.value).toBe("");
    expect(rows()).toHaveLength(LINK_SEARCH_FROM + 1);

    escape(search);
    expect(closed).toBe(1);
  });
});

describe("matchTodos", () => {
  it("matches title, notes and the project's name, case-insensitively", () => {
    const list = [todo("a", "Blog to Astro"), todo("b", "整理", { notes: "发布说明" }), todo("c", "备份", { project: "nas" })];
    expect(matchTodos(list, "  astro ").map((t) => t.id)).toEqual(["a"]);
    expect(matchTodos(list, "发布").map((t) => t.id)).toEqual(["b"]);
    expect(matchTodos(list, "家里", (slug) => (slug === "nas" ? "家里的 NAS" : slug)).map((t) => t.id)).toEqual(["c"]);
    expect(matchTodos(list, "")).toHaveLength(3);
  });
});
