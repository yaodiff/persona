// @vitest-environment jsdom
// An Inbox history entry keeps the view that was on it when another route took over: list search, section folds, list
// scroll, the item's handling-chain expansion, and an embedded original's expansion. A new visit (including a reload)
// starts clean.
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };
});

const calls = vi.hoisted(() => ({ items: [] as unknown[] }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => {
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: calls.items });
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [] });
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined }));
vi.mock("./LarkThread", () => ({ LarkThread: () => <div data-chat-body />, chatOfItem: () => ({}) }));

import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { resetInboxViewMemoryForTests } from "../../lib/inbox-view-memory";
import { settleRosterDefault } from "../../lib/manager-choice";
import { Inbox } from "./Inbox";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (id: string, tier: InboxItem["tier"]): InboxItem => ({
  id, source: "lark", kind: "group", account: "feishu", account_name: "Lark", title: `chat ${id}`, preview: "text", count: 1,
  updated_at: "2026-11-09T04:00:00Z", tier, version: `v-${id}`, lark: { feed_id: id, is_group: true },
  chain: [
    { id: `${id}-received`, kind: "received", text: "received" },
    { id: `${id}-triaged`, kind: "triaged", text: "triaged" },
    { id: `${id}-handling`, kind: "handling", text: "handling" },
    { id: `${id}-waiting`, kind: "waiting", text: "waiting" },
  ],
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const page = (id?: string) => <StrictMode><Inbox id={id} onToggleSidebar={() => {}} /></StrictMode>;
const show = async (id?: string) => {
  act(() => root.render(page(id)));
  await flush();
};
const rowLink = (id: string) => host.querySelector<HTMLAnchorElement>(`[data-item="${id}"] a`)!;
const search = () => host.querySelector<HTMLInputElement>('input[aria-label]')!;
const fyiToggle = () => host.querySelector<HTMLButtonElement>('[data-section="fyi"] > div > button[aria-expanded]')!;
const chainToggle = () => host.querySelector<HTMLButtonElement>("[data-chain-toggle]")!;
const chainRows = () => host.querySelectorAll("[data-chain-entry]");
const originalToggle = () => host.querySelector<HTMLButtonElement>('[data-testid="inbox-original-row-toggle"]')!;
const originalOpen = () => host.querySelector('[data-event-body="embedded"]') !== null;
const listScroll = () => host.querySelector<HTMLElement>("[data-inbox-list-scroll]")!;
const setSearch = async (value: string) => {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(search(), value);
    search().dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const openItem = async (id: string) => {
  act(() => rowLink(id).click());
  expect(location.pathname).toBe(`/inbox/${id}`);
  await show(id);
};
const leaveForChat = async () => {
  await act(async () => {
    history.pushState(null, "", "/persona/c1");
    root.render(<div>chat</div>);
  });
  await flush();
};
const popped = (go: () => void) => new Promise<void>((done, fail) => {
  const timer = setTimeout(() => fail(new Error("no Back happened")), 1_000);
  addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
  go();
});
const backToItem = async (id: string) => {
  await act(async () => popped(() => history.back()));
  await show(id);
};

beforeEach(async () => {
  calls.items = [item("a1", "action"), item("a2", "action"), item("f1", "fyi")];
  resetInboxForTests();
  resetInboxViewMemoryForTests();
  vi.stubGlobal("CSS", { escape: (value: string) => value });
  Element.prototype.scrollIntoView = vi.fn();
  history.replaceState(null, "", "/inbox");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await show();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  resetInboxViewMemoryForTests();
  history.replaceState(null, "", "/");
});

describe("Inbox history-entry view memory", () => {
  it("restores search, section folds, list scroll, and chain expansion after leaving and Back", async () => {
    await setSearch("chat");
    await openItem("a1");
    await act(async () => fyiToggle().click());
    expect(fyiToggle().getAttribute("aria-expanded")).toBe("false");
    await act(async () => chainToggle().click());
    expect(chainRows()).toHaveLength(4);
    await act(async () => {
      listScroll().scrollTop = 180;
      listScroll().dispatchEvent(new Event("scroll"));
    });

    await leaveForChat();
    await backToItem("a1");

    expect(search().value).toBe("chat");
    expect(fyiToggle().getAttribute("aria-expanded")).toBe("false");
    expect(chainRows()).toHaveLength(4);
    expect(listScroll().scrollTop).toBe(180);
  });

  it("restores an expanded original after leaving and Back, but starts collapsed on fresh navigation", async () => {
    await openItem("a1");
    expect(originalOpen()).toBe(false);
    expect(host.querySelector("[data-chat-body]")).toBeNull();
    await act(async () => originalToggle().click());
    expect(originalOpen()).toBe(true);
    expect(host.querySelector("[data-chat-body]")).not.toBeNull();

    await leaveForChat();
    await backToItem("a1");
    expect(originalOpen()).toBe(true);
    expect(host.querySelector("[data-chat-body]")).not.toBeNull();

    await leaveForChat();
    await act(async () => history.pushState(null, "", "/inbox/a1"));
    await show("a1");
    expect(originalOpen()).toBe(false);
    expect(host.querySelector("[data-chat-body]")).toBeNull();
  });

  it("starts clean on a fresh navigation and when the page memory is gone after a reload", async () => {
    await setSearch("chat");
    await openItem("a1");
    await act(async () => fyiToggle().click());
    await act(async () => chainToggle().click());
    await leaveForChat();

    await act(async () => history.pushState(null, "", "/inbox/a1"));
    await show("a1");
    expect(search().value).toBe("");
    expect(fyiToggle().getAttribute("aria-expanded")).toBe("true");
    expect(chainRows()).toHaveLength(3);
    expect(listScroll().scrollTop).toBe(0);

    await act(async () => root.unmount());
    resetInboxViewMemoryForTests(); // a reload drops the module map but keeps this entry's history.state
    root = createRoot(host);
    await show("a1");
    expect(search().value).toBe("");
    expect(fyiToggle().getAttribute("aria-expanded")).toBe("true");
    expect(chainRows()).toHaveLength(3);
  });

  it("restores an already-visited item immediately on an in-Inbox Back", async () => {
    await setSearch("chat");
    await openItem("a1");
    await act(async () => chainToggle().click());
    await openItem("a2");
    await setSearch("chat a2");
    expect(chainRows()).toHaveLength(3);

    await act(async () => popped(() => history.back()));
    await show("a1");
    expect(search().value).toBe("chat");
    expect(chainRows()).toHaveLength(4);
  });

  it("does not let a post-reload entry reuse an old entry's identifier and snapshot", async () => {
    await setSearch("before reload");
    const oldEntry = (history.state as Record<string, unknown>).personaInboxView;
    await leaveForChat();

    resetInboxViewMemoryForTests(); // another page load: a new prefix, the entry counter starting again
    await act(async () => history.pushState(null, "", "/inbox"));
    await show();
    const newEntry = (history.state as Record<string, unknown>).personaInboxView;
    expect(newEntry).not.toBe(oldEntry);
    await setSearch("after reload");
    await act(async () => root.render(<div>outside</div>));
    await flush();

    await act(async () => popped(() => history.go(-2)));
    await show();
    expect(search().value).toBe("");
  });

  it("copies the current list view to a row's new entry without moving another item's chain expansion", async () => {
    await setSearch("chat");
    await openItem("a1");
    await act(async () => fyiToggle().click());
    await act(async () => chainToggle().click());
    const firstEntry = (history.state as Record<string, unknown>).personaInboxView;

    await openItem("a2");
    expect(chainRows()).toHaveLength(3);
    expect((history.state as Record<string, unknown>).personaInboxSteps).toBe(2);
    expect((history.state as Record<string, unknown>).personaInboxView).not.toBe(firstEntry);

    await leaveForChat();
    await backToItem("a2");
    expect(search().value).toBe("chat");
    expect(fyiToggle().getAttribute("aria-expanded")).toBe("false");
    expect(chainRows()).toHaveLength(3);
  });

  it("does not replace history for search, folds, scrolling, chain expansion, or original expansion", async () => {
    await openItem("a1");
    const replace = vi.spyOn(history, "replaceState");
    await setSearch("c");
    await setSearch("ch");
    await setSearch("chat");
    await act(async () => fyiToggle().click());
    await act(async () => chainToggle().click());
    await act(async () => originalToggle().click());
    expect(originalOpen()).toBe(true);
    await act(async () => {
      listScroll().scrollTop = 90;
      listScroll().dispatchEvent(new Event("scroll"));
    });
    expect(replace).not.toHaveBeenCalled();
  });
});
