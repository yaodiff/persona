// @vitest-environment jsdom
// Merge and "Add to to-dos" in the selecting bar (#6480).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const calls = vi.hoisted(() => ({ list: [] as { path: string; method: string; json?: unknown }[], items: [] as unknown[] }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: { items?: { id: string }[] } }) => {
    calls.list.push({ path, method: init?.method ?? "GET", json: init?.json });
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: calls.items });
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [] });
    if (path === "/inbox/merge") {
      const ids = (init?.json?.items ?? []).map((x) => x.id);
      const [first, ...rest] = (calls.items as InboxItem[]).filter((x) => ids.includes(x.id));
      return Promise.resolve({ item: { ...first, merged_from: rest.map((x) => ({ id: x.id, source: x.source })), count: 2 }, members: rest.map((x) => x.id), undo: { id: first.id } });
    }
    if (path === "/inbox/todo") {
      const ids = new Set((init?.json?.items ?? []).map((x) => x.id));
      const changed = (calls.items as { id: string }[]).filter((x) => ids.has(x.id)).map((x) => ({ ...x, done_at: "2026-11-09T05:00:00Z", done_by: "todo" }));
      return Promise.resolve({ changed, todos: changed.map((x) => ({ item: x, todo: { id: `t-${x.id}` } })), undo: { items: changed.map((x) => ({ id: x.id, todo_id: `t-${x.id}` })) } });
    }
    if (path === "/inbox/bulk") {
      const ids = new Set((init?.json?.items ?? []).map((x) => x.id));
      const changed = (calls.items as { id: string }[]).filter((x) => ids.has(x.id)).map((x) => ({ ...x, done_at: "2026-11-09T05:00:00Z", done_by: "user" }));
      return Promise.resolve({ changed, unchanged: [], skipped: [] });
    }
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});

import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { Inbox } from "./Inbox";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (id: string, patch: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "lark", kind: "group", account: "feishu", account_name: "Lark", title: `chat ${id}`, preview: "text", count: 1,
  updated_at: "2026-11-09T04:00:00Z", tier: "action", version: `v-${id}`, lark: { feed_id: id, is_group: true }, ...patch,
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const button = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === text);
// the rows in sight: a closed section keeps its rows, inert
const boxes = () => [...host.querySelectorAll<HTMLElement>('[role="checkbox"]')].filter((b) => !b.closest("[inert]"));
const bar = () => host.querySelector("[data-pick-bar]");
const click = async (el: Element | undefined) => {
  expect(el).toBeDefined();
  await act(async () => (el as HTMLElement).click());
  await flush();
};

beforeEach(async () => {
  calls.list = [];
  // two in Needs you, one in FYI, one in Collapsed (closed at first), one done
  calls.items = [item("a1", { agent: { state: "drafted", draft: "好的，周五前给你。" } as InboxItem["agent"] }), item("a2"), item("f1", { tier: "fyi" }), item("n1", { tier: "noise" }), item("d1", { done_at: "2026-11-09T03:00:00Z", done_by: "user" })];
  resetInboxForTests();
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  Element.prototype.scrollIntoView = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Inbox onToggleSidebar={() => {}} />));
  await flush();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});


describe("inbox merge and add to to-dos (#6480)", () => {
  const pickTwo = async () => {
    await click(button("选择"));
    await click(boxes()[0]);
    await click(boxes()[1]);
  };
  it("the phone's icon-only Merge and Add to to-dos are named for screen readers and hover", async () => {
    await click(button("选择"));
    for (const name of ["合并", "加入待办"]) {
      const b = host.querySelector(`[role="toolbar"] button[aria-label="${name}"]`);
      expect(b?.getAttribute("title")).toBe(name);
    }
  });
  it("Merge needs two rows; it merges them into one row that says so, and ends selecting", async () => {
    await click(button("选择"));
    await click(boxes()[0]);
    expect(button("合并")?.disabled).toBe(true);
    await click(boxes()[1]);
    const merge = button("合并");
    expect(merge?.disabled).toBe(false);
    await click(merge);
    const call = calls.list.find((c) => c.path === "/inbox/merge");
    expect(call?.method).toBe("POST");
    expect((call?.json as { items: unknown[] }).items).toHaveLength(2);
    expect(bar()).toBeNull();
    expect(host.querySelectorAll("[data-merged]")).toHaveLength(1);
    expect(host.querySelector("[data-merged]")?.textContent).toBe("由 2 行合并");
  });
  it("Add to to-dos asks the due time once and makes one to-do per row", async () => {
    await pickTwo();
    await click(button("加入待办"));
    expect(document.querySelector("[data-todo-due]")).not.toBeNull();
    const add = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "添加");
    await click(add);
    const call = calls.list.find((c) => c.path === "/inbox/todo");
    const json = call?.json as { items: unknown[]; due_date?: string; due_at?: string };
    expect(json.items).toHaveLength(2);
    expect(json.due_date).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(json.due_at).toBeUndefined();
    expect(bar()).toBeNull();
  });
  it("no due date sends no due", async () => {
    await pickTwo();
    await click(button("加入待办"));
    await click([...document.querySelectorAll('[role="radio"]')].find((b) => b.textContent === "不设截止"));
    await click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "添加"));
    const json = calls.list.find((c) => c.path === "/inbox/todo")?.json as Record<string, unknown>;
    expect(json.due_date).toBeUndefined();
    expect(json.due_at).toBeUndefined();
  });
});
