// @vitest-environment jsdom
// The inbox that could not load the first time (#2970, after Android's failure empty state): its own empty state with
// "Retry", never "All caught up" under a red error; "Retry" loads again and the ordinary empty state comes back.
import { registerTestMailbox } from "../../test/mailbox-fixture";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

let fail = true;
let inboxItems: unknown[] = [];
// the hub's Done day counts (#2967): null for none, as a read that fails
let doneDays: { day: string; count: number }[] | null = null;
// the hub's mail accounts: one by default (a source is connected, an empty inbox is "All caught up"); [] for none
let mailAccounts: { id: string; email: string }[] = [{ id: "a1", email: "me@example.com" }];
const calls: string[] = [];
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => {
    calls.push(path);
    if (path === "/mail/accounts") return Promise.resolve({ accounts: mailAccounts });
    if (path.startsWith("/inbox/done/days") && doneDays) return Promise.resolve({ days: doneDays });
    return path.startsWith("/inbox") && fail ? Promise.reject(new TypeError("Failed to fetch")) : Promise.resolve({ items: inboxItems });
  },
}));

// agents waiting on you come from the store; `waiting` puts one there (a fixed array: the selector's result is stable),
// `storeLoaded` says whether the store's first snapshot is in
const fakeStore = vi.hoisted(() => ({ waiting: false, storeLoaded: true, list: [{ id: "ix1", machine_id: "m", session_id: "s1", session_title: "改登录页", kind: "question" }] }));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: fakeStore.storeLoaded, ...(fakeStore.waiting ? { interactions: fakeStore.list as never } : {}) })) };
});

import { InboxList } from "./InboxList";
import { resetInboxForTests } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  resetInboxForTests();
  resetMailForTests();
  inboxItems = [];
  mailAccounts = [{ id: "a1", email: "me@example.com" }];
  doneDays = null;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

registerTestMailbox();

describe("收件箱：列表不会横向滚动", () => {
  // overflow-y alone made the list scroll sideways whenever something inside was wider than the 380px column, cutting
  // off every card (#3262): the scroller clips x, and a source's long name truncates in its chip instead of widening it.
  it("clips the x axis on its scroller, and a chip's long name truncates", async () => {
    fail = false;
    mailAccounts = [{ id: "a1", email: "a-very-long-mailbox-name-for-the-office@example.com" }];
    act(() => root.render(<InboxList />));
    await flush();
    const scroller = host.querySelector(".overflow-x-clip");
    expect(scroller).not.toBeNull();
    expect(scroller?.className).toContain("overflow-y-auto");
    const chip = host.querySelector("button[aria-pressed]");
    expect(chip?.className).toContain("text-ellipsis");
    expect(chip?.className).toContain("min-w-0");
    expect(chip?.className).toContain("max-w-full");
  });
});

describe("收件箱：还没接入消息来源", () => {
  it("an empty inbox with nothing connected says so and offers 邮箱, not 都处理完了", async () => {
    fail = false;
    mailAccounts = [];
    act(() => root.render(<InboxList />));
    await flush();
    const none = host.querySelector("[data-inbox-no-sources]");
    expect(none?.textContent).toContain("尚未接入消息来源");
    expect([...none!.querySelectorAll("button")].map((b) => b.textContent?.trim())).toContain("接入邮箱");
    expect(host.textContent).not.toContain("已全部处理完毕");
  });
});

describe("收件箱：首次加载失败", () => {
  it("shows its own empty state with 重试; 重试 loads again and 都处理完了 comes back", async () => {
    fail = true;
    act(() => root.render(<InboxList />));
    await flush();
    const failed = host.querySelector("[data-inbox-failed]");
    expect(failed?.textContent).toContain("无法加载收件箱");
    expect(failed?.textContent).toContain("无法连接 hub");
    expect(host.textContent).not.toContain("已全部处理完毕");
    expect(host.querySelector("p.text-red")).toBeNull(); // the reason is in the empty state, not a red line over it

    fail = false;
    const before = calls.filter((c) => c.startsWith("/inbox")).length;
    const retry = [...failed!.querySelectorAll("button")].find((b) => b.textContent?.trim() === "重试");
    await act(async () => retry!.click());
    await flush();
    expect(calls.filter((c) => c.startsWith("/inbox")).length).toBe(before + 1);
    expect(host.querySelector("[data-inbox-failed]")).toBeNull();
    expect(host.textContent).toContain("已全部处理完毕");
  });

  // the section bar (#9053): Done chosen, with its day counts from the hub, still says the list couldn't be read, as the
  // stacked page said it over Done
  it("says so over Done when Done is chosen and has its days", async () => {
    fail = true;
    const today = new Date();
    doneDays = [{ day: `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, "0")}-${String(today.getDate()).padStart(2, "0")}`, count: 3 }];
    act(() => root.render(<InboxList />));
    await flush();
    const done = host.querySelector<HTMLButtonElement>('[data-bar-choice="done"]');
    expect(done).not.toBeNull();
    act(() => done!.click());
    await flush();
    expect(host.querySelector('[data-section="done"]')).not.toBeNull();
    expect(host.querySelector("[data-inbox-failed]")?.textContent).toContain("无法加载收件箱");
  });

  it("with agents waiting the list is not empty: the error is one red line over it, no failed empty state", async () => {
    fail = true;
    fakeStore.waiting = true;
    try {
      act(() => root.render(<InboxList />));
      await flush();
      expect(host.querySelector("p.text-red")?.textContent).toContain("无法连接 hub");
      expect(host.querySelector("[data-inbox-failed]")).toBeNull();
      expect(host.textContent).toContain("改登录页");
      expect(host.textContent).not.toContain("已全部处理完毕");
    } finally {
      fakeStore.waiting = false;
    }
  });
});

describe("收件箱：首屏一次画好", () => {
  it("waits for the store as well as the inbox, so the agents waiting do not push painted rows down", async () => {
    fail = false;
    fakeStore.waiting = true;
    fakeStore.storeLoaded = false;
    try {
      act(() => root.render(<InboxList />));
      await flush();
      // the inbox answered, the store has not: only "Loading", no rows and no empty state yet
      expect(calls.some((c) => c.startsWith("/inbox"))).toBe(true);
      expect(host.textContent).toContain("加载中");
      expect(host.textContent).not.toContain("改登录页");
      expect(host.textContent).not.toContain("已全部处理完毕");
      expect(host.querySelector("section")).toBeNull();

      fakeStore.storeLoaded = true;
      act(() => root.render(<InboxList />));
      await flush();
      expect(host.textContent).not.toContain("加载中");
      expect(host.textContent).toContain("改登录页");
    } finally {
      fakeStore.waiting = false;
      fakeStore.storeLoaded = true;
    }
  });

  it("renders sticky group headers with bg-page/95 rather than translucent bg-canvas/95 under Frosted Glass", async () => {
    fail = false;
    inboxItems = [
      { id: "l1", source: "lark", kind: "dm", account: "feishu", account_name: "Lark", title: "Ping", preview: "", count: 1, updated_at: new Date().toISOString(), tier: "action", version: "v1" },
      { id: "m1", source: "mail", kind: "mail", account: "a1", account_name: "me@example.com", title: "Weekly status", preview: "", count: 1, updated_at: new Date().toISOString(), tier: "action", version: "v2" },
    ];
    act(() => root.render(<InboxList />));
    await flush();
    const stickyHead = host.querySelector<HTMLElement>("[data-group] > .sticky");
    expect(stickyHead).not.toBeNull();
    expect(stickyHead!.className).toContain("bg-page/95");
    expect(stickyHead!.className).not.toContain("bg-canvas/95");
  });
});
