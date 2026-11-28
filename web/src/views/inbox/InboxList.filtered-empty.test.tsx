// @vitest-environment jsdom
// Audit items 15 and 84: a list emptied by its filters or a search is not "All caught up" (no green check, a way back
// to the whole Inbox, Clean up all names the filters instead of "Nothing to do"); while the list loads or after a failed
// read Clean up all claims nothing, and the failure line over waiting agents has its own Retry.
import { registerTestMailbox } from "../../test/mailbox-fixture";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

// the hub: "ok" answers the list, "fail" refuses it, "slow" never answers
const hub = vi.hoisted(() => ({ mode: "ok" as "ok" | "fail" | "slow", items: [] as unknown[], calls: [] as string[] }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => {
    hub.calls.push(path);
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [{ id: "a1", email: "me@example.com" }] });
    if (path.startsWith("/inbox?")) {
      if (hub.mode === "slow") return new Promise(() => {});
      if (hub.mode === "fail") return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve({ items: hub.items });
    }
    return Promise.resolve({});
  },
}));
// an agent waiting on you (the store's), so a failed read still has a list under its error line
const fakeStore = vi.hoisted(() => ({ waiting: false, list: [{ id: "ix1", machine_id: "m", session_id: "s1", session_title: "改登录页", kind: "question" }] }));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true, ...(fakeStore.waiting ? { interactions: fakeStore.list as never } : {}) })) };
});

import { InboxList } from "./InboxList";
import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";

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
// the list's buttons, not the section bar's choices (its All is a choice of sections, not the chips' All)
const button = (text: string) => [...host.querySelectorAll<HTMLButtonElement>("button:not([data-bar-choice])")].find((b) => b.textContent?.trim() === text);
// the section bar opens on Needs you (#9053): these cases are about the whole list, All
const showAll = () => act(() => host.querySelector<HTMLButtonElement>('[data-bar-choice="all"]')!.click());
const cleanAll = () => host.querySelector<HTMLButtonElement>("[data-clean-all]")!;
const rows = () => [...host.querySelectorAll("[data-item]")].map((r) => r.getAttribute("data-item"));
const typeSearch = async (text: string) => {
  const input = host.querySelector<HTMLInputElement>('input[aria-label="搜索收件箱"]')!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await flush();
};

beforeEach(() => {
  hub.mode = "ok";
  hub.calls = [];
  // a Lark chat that needs you and a mail to read
  hub.items = [item("l1"), item("m1", { source: "mail", kind: "mail", account: "a1", account_name: "me@example.com", tier: "fyi", lark: undefined, mail: { account_id: "a1", uids: [1] } as InboxItem["mail"] })];
  localStorage.clear();
  resetInboxForTests();
  resetMailForTests();
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  localStorage.clear();
});

registerTestMailbox();

describe("a list emptied by its filters (audit item 15)", () => {
  it("chips that leave nothing say so, offer Clear filters, and Clean up all names them instead of 没有未处理的", async () => {
    // the chips are kept on this device: Lark and the Mail kind, which no item is both
    localStorage.setItem("inbox.source", "feishu");
    localStorage.setItem("inbox.kind", "mail");
    act(() => root.render(<InboxList />));
    await flush();
    showAll();
    const empty = host.querySelector("[data-inbox-filtered-empty]");
    expect(empty?.textContent).toContain("没有符合筛选条件的未处理消息");
    expect(host.textContent).not.toContain("已全部处理完毕");
    expect(host.querySelector(".bg-green-tint")).toBeNull();
    expect(cleanAll().textContent).not.toContain("暂无待处理事项");
    expect(cleanAll().textContent).toContain("飞书 · 邮件");
    expect(cleanAll().disabled).toBe(true);

    // a finger's 44 px around it, and the keyboard is not dropped when the button goes with the empty state: it lands on
    // the first filter chip in the toolbar (no duplicate "全部" chip beside the section bar's "全部")
    const clear = button("清除筛选")!;
    expect(clear.className).toContain("touch-hit");
    act(() => clear.focus());
    await act(async () => clear.click());
    await flush();
    expect(clear.isConnected).toBe(false);
    expect(button("全部")).toBeUndefined();
    expect(document.activeElement).toBe(button("飞书"));
    expect(document.activeElement?.getAttribute("aria-pressed")).toBe("false");
    expect(localStorage.getItem("inbox.source")).toBeNull();
    expect(localStorage.getItem("inbox.kind")).toBeNull();
    expect(host.querySelector("[data-inbox-filtered-empty]")).toBeNull();
    expect(rows()).toEqual(["l1", "m1"]);
    expect(cleanAll().textContent).toContain("全部 · 2 项");
  });

  it("keeps only one 全部 button across the Inbox header and renders filters on a single non-wrapping row", async () => {
    act(() => root.render(<InboxList />));
    await flush();
    const allButtons = [...host.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.trim() === "全部");
    expect(allButtons).toHaveLength(1);
    expect(allButtons[0]?.dataset.barChoice).toBe("all");

    const toolbar = host.querySelector<HTMLElement>('[role="toolbar"][aria-label="筛选"]')!;
    expect(toolbar.className).toContain("flex-nowrap");
    expect(toolbar.className).toContain("overflow-x-auto");
    expect(toolbar.className).not.toContain("flex-wrap");
    expect(toolbar.contains(cleanAll())).toBe(false);
    expect(host.querySelector("[data-clear-filters]")).toBeNull();

    // Selecting a filter shows a contextual Clear filters button instead of a duplicate "全部" chip.
    await act(async () => button("飞书")!.click());
    await flush();
    const clearChip = host.querySelector<HTMLButtonElement>("[data-clear-filters]")!;
    expect(clearChip).not.toBeNull();
    await act(async () => clearChip.click());
    await flush();
    expect(host.querySelector("[data-clear-filters]")).toBeNull();
    expect(button("飞书")?.getAttribute("aria-pressed")).toBe("false");
  });

  it("a search with no hit keeps 没有匹配的, without the green check, and Clear search empties the field", async () => {
    act(() => root.render(<InboxList />));
    await flush();
    showAll();
    await typeSearch("zzzz");
    const empty = host.querySelector("[data-inbox-filtered-empty]");
    expect(empty?.textContent).toContain("无匹配结果");
    expect(host.querySelector(".bg-green-tint")).toBeNull();
    expect(cleanAll().textContent).not.toContain("暂无待处理事项");

    const clear = button("清除搜索")!;
    act(() => clear.focus());
    await act(async () => clear.click());
    await flush();
    expect(host.querySelector<HTMLInputElement>('input[aria-label="搜索收件箱"]')!.value).toBe("");
    expect(document.activeElement).toBe(button("飞书"));
    expect(rows()).toEqual(["l1", "m1"]);
  });

  it("an Inbox with nothing open and no filters is still 都处理完了, and Clean up all still says 没有未处理的", async () => {
    hub.items = [];
    act(() => root.render(<InboxList />));
    await flush();
    expect(host.textContent).toContain("已全部处理完毕");
    expect(host.querySelector("[data-inbox-filtered-empty]")).toBeNull();
    expect(cleanAll().textContent).toContain("暂无待处理事项");
  });
});

describe("Clean up all before the list is known (audit item 84)", () => {
  it("says nothing about what is left while the list loads", async () => {
    hub.mode = "slow";
    act(() => root.render(<InboxList />));
    await flush();
    expect(host.textContent).toContain("加载中");
    expect(cleanAll().textContent?.trim()).toBe("全部清理");
    expect(cleanAll().disabled).toBe(true);
  });

  it("after a failed read: no 没有未处理的, and the error line over the agents has its own Retry", async () => {
    hub.mode = "fail";
    fakeStore.waiting = true;
    try {
      act(() => root.render(<InboxList />));
      await flush();
      expect(cleanAll().textContent?.trim()).toBe("全部清理");
      const line = host.querySelector("[data-inbox-read-failed]");
      expect(line?.textContent).toContain("无法连接 hub");
      expect(host.textContent).toContain("改登录页");

      hub.mode = "ok";
      const before = hub.calls.filter((c) => c.startsWith("/inbox?")).length;
      const retry = [...line!.querySelectorAll("button")].find((b) => b.textContent?.trim() === "重试");
      await act(async () => retry!.click());
      await flush();
      expect(hub.calls.filter((c) => c.startsWith("/inbox?")).length).toBe(before + 1);
      expect(host.querySelector("[data-inbox-read-failed]")).toBeNull();
      expect(cleanAll().textContent).toContain("全部 · 2 项");
    } finally {
      fakeStore.waiting = false;
    }
  });
});
