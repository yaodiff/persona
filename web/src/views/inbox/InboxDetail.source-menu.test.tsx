// @vitest-environment jsdom
// Two entries stacked on one item (#E1): a document's original page (its own same-address entry) and, on a narrow
// screen, the ⋯ / mute menu opened over it (back-close's entry too). Each Back closes one: the menu first, then the
// original, then the list — never a Back that closes nothing and never one that skips the original.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  // narrow (the sidebar is a drawer, menus put an entry on the stack), not a phone width
  window.matchMedia ??= ((query: string) => ({ matches: query.includes("max-width: 1023px"), media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  Object.assign(globalThis, {
    ResizeObserver: globalThis.ResizeObserver ?? class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: globalThis.IntersectionObserver ?? class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } },
  });
  globalThis.CSS ??= { escape: (s: string) => s } as unknown as typeof CSS;
});

vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined }));
vi.mock("./InboxLine", () => ({ InboxLine: () => null }));

import type { InboxItem } from "../../lib/inbox";
import { resetInboxForTests } from "../../lib/inbox";
import { onOverlayEntry } from "../../lib/back-close";
import { stepsToInbox } from "../../lib/inbox-nav";
import { settleRosterDefault } from "../../lib/manager-choice";
import { t } from "../../i18n";
import { InboxDetail } from "./InboxDetail";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item: InboxItem = {
  id: "docs:d1", source: "docs", kind: "doc", account: "docs", account_name: "Docs", title: "Release plan", preview: "mentioned you",
  count: 1, tier: "action", updated_at: "2026-11-05T10:00:00Z", version: "v1",
  doc: { url: "https://example.com/d/1", type: "doc", notice: "mention", rows: [{ ref: "r1", title: "A row" }] },
  chain: [{ id: "c1", at: "2026-11-05T10:00:00Z", kind: "received", text: "1 new message" }],
  mutes: [{ tag: "sender", value: "u1", name: "Someone" }],
};

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const eventBody = () => host.querySelector("[data-event-body]");
const menuRows = () => document.querySelectorAll('[role="menuitem"]');
const popped = (go: () => void) =>
  new Promise<void>((done, fail) => {
    const timer = setTimeout(() => fail(new Error("no Back happened")), 1_000);
    addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
    go();
  });

beforeEach(async () => {
  resetInboxForTests();
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, text: async () => JSON.stringify({}) })));
  history.pushState(null, "", "/inbox");
  history.pushState({ personaInboxSteps: 1 }, "", "/inbox/docs:d1");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<InboxDetail item={item} loaded />));
  await flush();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
});

describe("a menu over an item's original page", () => {
  it("each Back closes one: the menu, then the original, then the list", async () => {
    expect(eventBody()).not.toBeNull();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("inbox-inboxdetail.mute")}"]`)!.click();
    });
    await flush();
    // the original is not open yet; the menu alone
    expect(menuRows().length).toBeGreaterThan(0);
    await popped(() => history.back());
    await flush();
    expect(menuRows().length).toBe(0);
    expect(eventBody()).not.toBeNull(); // the item is untouched

    await act(async () => {
      [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === t("inbox-inboxdetail.view_source"))!.click();
    });
    await flush();
    expect(eventBody()).toBeNull();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("inbox-inboxdetail.mute")}"]`)!.click();
    });
    await flush();
    expect(menuRows().length).toBeGreaterThan(0);

    await popped(() => history.back());
    await flush();
    expect(menuRows().length).toBe(0);
    expect(eventBody()).toBeNull(); // the original is still open under the menu

    await popped(() => history.back());
    await flush();
    expect(eventBody()).not.toBeNull();
    expect(onOverlayEntry(history.state)).toBe(false);
    expect(stepsToInbox(history.state)).toBe(1);

    await popped(() => history.back());
    expect(location.pathname).toBe("/inbox");
  });

  it("a menu opened over the original and closed from the page leaves the original's own entry alone", async () => {
    await act(async () => {
      [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === t("inbox-inboxdetail.view_source"))!.click();
    });
    await flush();
    expect(eventBody()).toBeNull();
    await act(async () => {
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("inbox-inboxdetail.mute")}"]`)!.click();
    });
    await flush();
    expect(menuRows().length).toBeGreaterThan(0);
    // Escape closes the menu from the page: its entry comes off, the original stays
    await act(async () => {
      await popped(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    });
    await flush();
    expect(menuRows().length).toBe(0);
    expect(eventBody()).toBeNull(); // still the original
    expect(onOverlayEntry(history.state)).toBe(true);
    await popped(() => history.back());
    await flush();
    expect(eventBody()).not.toBeNull();
  });
});
