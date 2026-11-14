// @vitest-environment jsdom
// Audit item 34: a long run of rows mounts in steps. 300 FYI mails built every row (some 7,000 elements) before the
// Inbox showed anything; now the first rows come at once and the rest follow while the page is idle, until all are
// there. The opened item is there from the start (the list keeps it in sight) and stays, and a returned view, which puts
// its scroll position back, gets every row at once.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const hub = vi.hoisted(() => ({ items: [] as unknown[] }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => {
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [{ id: "a1", email: "me@example.com" }] });
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: hub.items });
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});

import { InboxList } from "./InboxList";
import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { resetMailForTests } from "../../lib/mail";
import { InboxViewMemoryContext, type InboxViewMemory } from "../../lib/inbox-view-memory";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// 120 mails to read, newest first: m000 is on top
const mail = (i: number): InboxItem => ({
  id: `m${String(i).padStart(3, "0")}`, source: "mail", kind: "mail", account: "a1", account_name: "me@example.com", title: `notice ${i}`, preview: "text", count: 1,
  updated_at: new Date(Date.UTC(2026, 10, 9, 4, 0, 0) - i * 60_000).toISOString(), tier: "fyi", version: `v${i}`, mail: { account_id: "a1", uids: [i] } as InboxItem["mail"],
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
// the idle steps: each one mounts the next rows
const idle = async (steps: number) => {
  for (let i = 0; i < steps; i++) await act(async () => { await new Promise((done) => setTimeout(done, 60)); });
};
const rows = () => host.querySelectorAll("[data-item]").length;

beforeEach(() => {
  hub.items = Array.from({ length: 120 }, (_, i) => mail(i));
  localStorage.clear();
  resetInboxForTests();
  resetMailForTests();
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  Element.prototype.scrollIntoView = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("a long list mounts in steps (audit item 34)", () => {
  it("shows the first rows at once and the rest while the page is idle, in the list's order", async () => {
    act(() => root.render(<InboxList />));
    await flush();
    expect(rows()).toBe(20);
    expect(host.querySelector("[data-item]")?.getAttribute("data-item")).toBe("m000");
    await idle(1);
    expect(rows()).toBe(40);
    await idle(6);
    expect(rows()).toBe(120);
    expect([...host.querySelectorAll("[data-item]")].map((r) => r.getAttribute("data-item"))).toEqual(hub.items.map((it) => (it as InboxItem).id));
  });

  it("has the opened item from the start, so the list can show it", async () => {
    act(() => root.render(<InboxList selected={{ id: "m090" }} />));
    await flush();
    expect(host.querySelector('[data-item="m090"]')).not.toBeNull();
    expect(rows()).toBe(91);
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
  });

  // the keyboard comes back to the opened item's row when it closes (audit item 74): closed before the next step, the
  // row is still there to take it
  it("keeps the opened item's row once it is there, when the item closes before the next step", async () => {
    act(() => root.render(<InboxList selected={{ id: "m090" }} />));
    await flush();
    act(() => root.render(<InboxList />));
    await flush();
    expect(host.querySelector('[data-item="m090"]')).not.toBeNull();
    expect(rows()).toBe(91);
  });

  it("draws every row at once for a returned view, which puts its scroll position back", async () => {
    const view = { restored: true, revision: 0, entryId: null, listQuery: () => "", setListQuery() {}, listToggled: () => new Set<string>(), setListToggled() {}, listScroll: () => 0, setListScroll() {}, chainOpen: () => false, setChainOpen() {}, originalOpen: () => false, setOriginalOpen() {}, attach() {}, detach() {} } satisfies InboxViewMemory;
    act(() => root.render(<InboxViewMemoryContext.Provider value={view}><InboxList /></InboxViewMemoryContext.Provider>));
    await flush();
    expect(rows()).toBe(120);
  });
});
