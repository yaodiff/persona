// @vitest-environment jsdom
// E3 review: a history entry keeps the view that was on it. Two things this pins:
//  - a Back that lands on an entry already carrying a snapshot may not overwrite that snapshot with the view on screen
//    (it must still be there on the next return from outside the Inbox, which is what this package restores);
//  - a mount that is not a return (a plain remount, as a test or StrictMode's replay does) must not pick up a snapshot:
//    "a fresh Inbox navigation starts clean".
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
vi.mock("./LarkThread", () => ({ LarkThread: () => null, chatOfItem: () => ({}) }));

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
const chainToggle = () => host.querySelector<HTMLButtonElement>("[data-chain-toggle]")!;
const chainRows = () => host.querySelectorAll("[data-chain-entry]");
const search = () => host.querySelector<HTMLInputElement>('input[aria-label]')!;
const setSearch = async (value: string) => {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(search(), value);
    search().dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const listScroll = () => host.querySelector<HTMLElement>("[data-inbox-list-scroll]")!;
const openItem = async (id: string) => {
  act(() => rowLink(id).click());
  expect(location.pathname).toBe(`/inbox/${id}`);
  await show(id);
};
const popped = (go: () => void) => new Promise<void>((done, fail) => {
  const timer = setTimeout(() => fail(new Error("no Back happened")), 1_000);
  addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
  go();
});
const back = async (id?: string) => {
  await act(async () => popped(() => history.back()));
  await show(id);
};
const leaveForChat = async () => {
  await act(async () => {
    history.pushState(null, "", "/persona/c1");
    root.render(<div>chat</div>);
  });
  await flush();
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
  resetInboxViewMemoryForTests();
  history.replaceState(null, "", "/");
});

describe("E3 review: an entry keeps its own snapshot", () => {
  it("a Back that lands on an already-visited item does not throw that item's snapshot away", async () => {
    // A opened with its chain expanded, a row beside it opened, then Back to A, then out to a chat and Back again:
    // the last return is the one this package restores, and it must still show A's chain expanded.
    await openItem("a1");
    await act(async () => chainToggle().click());
    expect(chainRows()).toHaveLength(4);
    await openItem("a2");
    expect(chainRows()).toHaveLength(3);
    await back("a1");
    await leaveForChat();
    await back("a1");
    expect(chainRows()).toHaveLength(4);
  });

  it("leaving and returning by Back shows the view the entry was left with", async () => {
    await setSearch("chat");
    await openItem("a1");
    await act(async () => chainToggle().click());
    await leaveForChat();
    await back("a1");
    expect(search().value).toBe("chat");
    expect(chainRows()).toHaveLength(4);
  });

  it("Back to the list entry from an item keeps the list's scroll", async () => {
    await openItem("a1");
    await act(async () => {
      listScroll().scrollTop = 240;
      listScroll().dispatchEvent(new Event("scroll"));
    });
    await back();
    expect(listScroll().scrollTop).toBe(240);
  });

  it("leaving the Inbox from the list itself and coming back by Back restores the list", async () => {
    const fyi = () => host.querySelector<HTMLButtonElement>('[data-section="fyi"] > div > button[aria-expanded]')!;
    await setSearch("chat");
    await act(async () => fyi().click());
    expect(fyi().getAttribute("aria-expanded")).toBe("false");
    await leaveForChat();
    await back();
    expect(search().value).toBe("chat");
    expect(fyi().getAttribute("aria-expanded")).toBe("false");
  });

  it("Forward to an item after Back from it shows the item's own snapshot again", async () => {
    await openItem("a1");
    await act(async () => chainToggle().click());
    await leaveForChat();
    await back("a1");
    expect(chainRows()).toHaveLength(4);
    await act(async () => popped(() => history.forward()));
    await show();
    expect(location.pathname).toBe("/persona/c1");
    await act(async () => popped(() => history.back()));
    await show("a1");
    expect(chainRows()).toHaveLength(4);
  });

  it("a remount on the same entry that no Back or Forward reached starts clean", async () => {
    // Not a return: the page mounts again on the entry it was already on (a test case, a remount). The snapshot the
    // previous mount left must not come back by itself, or a fresh visit would show an old search or fold.
    await setSearch("chat");
    act(() => root.unmount());
    root = createRoot(host);
    await show();
    expect(search().value).toBe("");
  });
});
