// @vitest-environment jsdom
// A group title's "Mark all seen ⌄" (#10422, docs/assistant/inbox.md "Group titles"): on FYI's (and Collapsed's) group
// titles a pull-down files the group's items or the whole section's in one request with their versions, Undo on the
// toast; none on Needs you, none while selecting, none where a section has one group (its own button acts at once);
// under a filter or a search each choice says what "all" covers. The same rules as iOS's InboxGroupClearTests.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const calls = vi.hoisted(() => ({ done: [] as { done: boolean; items: { id: string; version?: string }[] }[], items: [] as unknown[], toast: [] as unknown[][] }));
vi.mock("../../lib/api", async (orig) => {
  const m = await orig<typeof import("../../lib/api")>();
  return {
    ...m,
    api: (path: string, init?: { method?: string; json?: { action?: string; items?: { id: string; version?: string }[] } }) => {
      if (path.startsWith("/inbox?")) return Promise.resolve({ items: calls.items });
      if (path.startsWith("/inbox/done/days?")) return Promise.reject(new m.ApiError(404, "not found", { error: "not found" }));
      if (path.startsWith("/inbox/done?")) return Promise.resolve({ items: [] });
      if (path === "/mail/accounts") return Promise.resolve({ accounts: [{ id: "acc1", email: "me@example.com" }] });
      // the section's Mark all seen and this menu: the bulk request's done, and its Undo's reopen
      if (path === "/inbox/bulk" && init?.method === "POST") {
        const body = { done: init.json?.action === "done", items: init.json?.items ?? [] };
        calls.done.push(body);
        const ids = new Set(body.items.map((x) => x.id));
        const at = body.done ? new Date().toISOString() : null;
        return Promise.resolve({ changed: (calls.items as { id: string }[]).filter((x) => ids.has(x.id)).map((x) => ({ ...x, done_at: at, done_by: body.done ? "user" : null })), unchanged: [], skipped: [] });
      }
      return Promise.resolve({});
    },
  };
});
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../ui/atoms", async (orig) => ({
  ...(await orig<typeof import("../../ui/atoms")>()),
  toast: (...a: unknown[]) => void calls.toast.push(a),
}));

import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";
import { Inbox } from "./Inbox";
import { groupClear } from "./group-clear";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ago = (hours: number) => new Date(Date.now() - hours * 3_600_000).toISOString();
const lark = (id: string, patch: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "lark", kind: "dm", account: "feishu", account_name: "Lark", title: `chat ${id}`, preview: "", count: 1, updated_at: ago(1), tier: "fyi", version: `v-${id}`, ...patch,
});
const mail = (id: string, patch: Partial<InboxItem> = {}) => lark(id, { source: "mail", kind: "mail", account: "acc1", account_name: "me@example.com", ...patch });

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};
const button = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === text);
const head = (section: string, key: string) => host.querySelector<HTMLElement>(`[data-group="|${section}|${key}"]`);
const pullDown = (section: string, key: string) => head(section, key)?.querySelector<HTMLButtonElement>("[data-group-clear]") ?? null;
const choices = () => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')];
const rows = () => [...host.querySelectorAll<HTMLElement>("[data-item]")].map((el) => el.dataset.item);
const click = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};
const choose = (bar: string) => click(host.querySelector(`[data-bar-choice="${bar}"]`));
function edit(el: HTMLInputElement, value: string) {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(async () => {
  calls.done = [];
  calls.toast = [];
  // Needs you: Lark and mail; FYI: two Lark, one mail; Collapsed: one Lark
  calls.items = [lark("a1", { tier: "action" }), mail("a2", { tier: "action" }), lark("f1"), lark("f2"), mail("f3"), lark("n1", { tier: "noise" })];
  localStorage.clear();
  resetInboxForTests();
  resetMailForTests();
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

describe("a group title's Mark all seen (#10422)", () => {
  it("FYI's group titles carry it; Needs you's don't", async () => {
    await choose("all");
    expect(pullDown("fyi", "lark")?.textContent).toBe("全部设为已看");
    expect(pullDown("fyi", "mail:acc1")?.textContent).toBe("全部设为已看");
    expect(pullDown("action", "lark")).toBeNull();
    expect(pullDown("action", "mail:acc1")).toBeNull();
    expect(head("action", "lark")).toBeTruthy();
  });

  it("the group's items alone, one request with their versions, then Undo brings them back", async () => {
    await choose("fyi");
    await click(pullDown("fyi", "lark"));
    expect(choices().map((c) => c.textContent)).toEqual(["将「飞书」中的全部 2 条标为已看", "将「知悉即可」中的全部 3 条标为已看"]);
    await click(choices()[0]);
    expect(calls.done).toEqual([{ done: true, items: [{ id: "f1", version: "v-f1" }, { id: "f2", version: "v-f2" }] }]);
    expect(rows()).not.toContain("f1");
    expect(rows()).toContain("f3");
    const [, , undo] = calls.toast.at(-1) as [string, string, { label: string; run: () => void }];
    expect(undo.label).toBe("撤销");
    await act(async () => undo.run());
    await flush();
    expect(calls.done.at(-1)).toMatchObject({ done: false });
    expect(calls.done.at(-1)!.items.map((x) => x.id).sort()).toEqual(["f1", "f2"]);
  });

  it("the whole section from a group's title", async () => {
    await choose("fyi");
    await click(pullDown("fyi", "mail:acc1"));
    expect(choices()[0].textContent).toBe("将「邮件 · me@example.com」中的全部 1 条标为已看");
    await click(choices()[1]);
    expect(calls.done.map((r) => r.items.map((x) => x.id).sort())).toEqual([["f1", "f2", "f3"]]);
  });

  it("under a search each choice says what all covers, and counts what the search shows", async () => {
    calls.items = [...calls.items, lark("f4", { title: "weekly report" }), mail("f5", { title: "weekly digest" })];
    resetInboxForTests();
    act(() => root.render(<Inbox onToggleSidebar={() => {}} key="again" />));
    await flush();
    edit(host.querySelector<HTMLInputElement>("input[aria-label]")!, "weekly");
    await flush();
    await click(pullDown("fyi", "lark"));
    expect(choices().map((c) => c.textContent)).toEqual(["将「飞书」中的全部 1 条标为已看搜索「weekly」", "将「知悉即可」中的全部 2 条标为已看搜索「weekly」"]);
  });

  it("none while selecting, and none where the section has one group", async () => {
    await choose("all");
    await click(button("选择"));
    expect(pullDown("fyi", "lark")).toBeNull();
    // Collapsed has one group: no group titles, its own Clear all
    expect(host.querySelectorAll('[data-section="noise"] [data-group]').length).toBe(0);
  });
});

describe("groupClear", () => {
  const section = [lark("f1"), lark("f2"), mail("f3")];
  it("Collapsed clears instead; Needs you and In progress have none", () => {
    const g = { title: "飞书", items: section.slice(0, 2) };
    expect(groupClear("noise", "已折叠", g, section)?.choices.map((c) => c.label)).toEqual(["清除「飞书」中的全部 2 条", "清除「已折叠」中的全部 3 条"]);
    expect(groupClear("noise", "已折叠", g, section)?.label).toBe("全部清除");
    expect(groupClear("action", "需要你", g, section)).toBeUndefined();
    expect(groupClear("working", "进行中", g, section)).toBeUndefined();
  });

  it("a group that is the whole section, or empty, has none", () => {
    expect(groupClear("fyi", "知悉即可", { title: "飞书", items: section }, section)).toBeUndefined();
    expect(groupClear("fyi", "知悉即可", { title: "飞书", items: [] }, section)).toBeUndefined();
  });
});
