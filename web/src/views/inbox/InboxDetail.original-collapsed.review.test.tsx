// @vitest-environment jsdom
// Review of E13 (a chat / mail event's original starts collapsed): the expansion belongs to the item on the history
// entry, so two items never share it; the item's manager is part of the identity; and either control opens the body
// while only the header's one closes it again.
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };
});

const calls = vi.hoisted(() => ({ items: [] as unknown[], list: [] as { path: string; method?: string }[] }));
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string }) => {
    calls.list.push({ path, method: init?.method });
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: calls.items });
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [] });
    if (path.includes("/lark/conversations/")) return Promise.resolve({ messages: [], read_cursor: "0", states: [] });
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/store", async (original) => {
  const m = await original<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined, translatesMail: () => false }));
vi.mock("./LarkThread", () => ({ LarkThread: () => <div data-chat-body />, chatOfItem: () => ({}) }));

import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { resetInboxViewMemoryForTests, useInboxView } from "../../lib/inbox-view-memory";
import { settleRosterDefault } from "../../lib/manager-choice";
import { Inbox } from "./Inbox";
import { InboxDetail } from "./InboxDetail";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const chatItem = (id: string, manager?: string): InboxItem => ({
  id, source: "lark", kind: "group", account: "lark", account_name: "Lark", title: `chat ${id}`, preview: "hi", count: 1,
  updated_at: "2026-11-09T04:00:00Z", tier: "action", version: `v-${id}`, lark: { feed_id: id, is_group: true },
  ...(manager ? { manager } : {}),
  chain: [{ id: `${id}-received`, kind: "received", text: "received" }],
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const show = async (id?: string) => {
  act(() => root.render(<StrictMode><Inbox id={id} onToggleSidebar={() => {}} /></StrictMode>));
  await flush();
};
const rowLink = (id: string) => host.querySelector<HTMLAnchorElement>(`[data-item="${id}"] a`)!;
const openItem = async (id: string) => {
  act(() => rowLink(id).click());
  expect(location.pathname).toBe(`/inbox/${id}`);
  await show(id);
};
const headerToggle = () => host.querySelector<HTMLButtonElement>('[data-testid="inbox-original-toggle"]');
const rowToggle = () => host.querySelector<HTMLButtonElement>('[data-testid="inbox-original-row-toggle"]');
const originalMounted = () => host.querySelector("[data-chat-body]") !== null;
const popped = (go: () => void) => new Promise<void>((done, fail) => {
  const timer = setTimeout(() => fail(new Error("no Back happened")), 1_000);
  addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
  go();
});

beforeEach(async () => {
  calls.items = [chatItem("a1"), chatItem("a2")];
  calls.list = [];
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

describe("the original's expansion belongs to one item", () => {
  it("expands from the header too, then only the header closes it, and the other item stays collapsed", async () => {
    await openItem("a1");
    expect(originalMounted()).toBe(false);
    expect(headerToggle()!.textContent).toBe("看原文");
    expect(rowToggle()!.textContent).toBe("看原文");

    await act(async () => headerToggle()!.click());
    await flush();
    expect(originalMounted()).toBe(true);
    expect(headerToggle()!.textContent).toBe("收起原文");
    expect(rowToggle()).toBeNull(); // one entry once expanded, not two that say the same

    await openItem("a2");
    expect(originalMounted()).toBe(false); // the first item's expansion did not come with it
    expect(headerToggle()!.getAttribute("aria-expanded")).toBe("false");

    await act(async () => popped(() => history.back()));
    await show("a1");
    expect(originalMounted()).toBe(true); // still this item's own
    expect(headerToggle()!.getAttribute("aria-expanded")).toBe("true");
  });

  it("carries an expanded original to the same row opened again from the list, as the chain's expansion does", async () => {
    await openItem("a1");
    await act(async () => headerToggle()!.click());
    await flush();
    expect(originalMounted()).toBe(true);

    await act(async () => popped(() => history.back()));
    await show();
    expect(location.pathname).toBe("/inbox");
    await openItem("a1");
    // E3's rule: a push carries the view on screen (list search, chain expansion), so it does not start collapsed here;
    // a fresh navigation or a reload does (Inbox.view-memory.test.tsx).
    expect(headerToggle()!.getAttribute("aria-expanded")).toBe("true");
    expect(originalMounted()).toBe(true);
  });

  it("a mail chain entry opens its uid from the collapsed state, reading only that message", async () => {
    const mail: InboxItem = {
      id: "mail:a1:1", source: "mail", kind: "mail", account: "a1", account_name: "Mail", title: "A thread", preview: "p", count: 1,
      updated_at: "2026-11-09T04:00:00Z", tier: "action", version: "v", mail: { account_id: "a1", mailbox: "INBOX", uids: [88, 89] },
      chain: [{ id: "m88", kind: "received", text: "older mail", link: { type: "source", uid: 88 } }],
    };
    calls.items = [mail];
    await act(async () => root.render(<InboxDetail item={mail} loaded />));
    await flush();
    expect(host.querySelector('[data-event-body="embedded"]')).toBeNull();
    expect(calls.list.filter((c) => c.path.startsWith("/mail/messages/"))).toEqual([]);

    await act(async () => host.querySelector<HTMLButtonElement>('[data-chain-entry="received"] button')!.click());
    await flush();
    expect(host.querySelector('[data-event-body="embedded"]')).not.toBeNull();
    expect(calls.list.filter((c) => c.path === "/mail/messages/a1/88?mailbox=INBOX")).toHaveLength(1);
    expect(calls.list.some((c) => c.path === "/mail/messages/a1/89?mailbox=INBOX")).toBe(false); // not the latest too
  });

  it("keeps two managers' items of the same id apart", async () => {
    const view = { current: null as ReturnType<typeof useInboxView> | null };
    const Probe = () => {
      view.current = useInboxView();
      return null;
    };
    act(() => root.render(<Probe />));
    await flush();
    const memory = view.current!;
    await act(async () => memory.setOriginalOpen("m-1\u0000x", true));
    expect(memory.originalOpen("m-1\u0000x")).toBe(true);
    expect(memory.originalOpen("m-2\u0000x")).toBe(false); // the other manager's item of the same id
    expect(memory.originalOpen("m-1\u0000y")).toBe(false); // and another id of the same manager
  });
});
