// @vitest-environment jsdom
// A chat or mail event shows its own body first. Its original is unmounted until Original or a source-chain entry opens
// it, so merely opening the event cannot read the conversation or report messages as seen.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };
});

const calls = vi.hoisted(() => ({
  list: [] as { path: string; method?: string; json?: unknown }[],
}));
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown }) => {
    calls.list.push({ path, method: init?.method, json: init?.json });
    if (path.includes("/lark/conversations/f1/messages?")) {
      const position = new URL(path, "http://hub").searchParams.get("around") ?? "100";
      return Promise.resolve({
        messages: [{ id: `m${position}`, position, sender_name: "Colleague", created_at: "2026-11-05T10:00:00Z", text: `Message ${position}` }],
        read_cursor: "0",
      });
    }
    if (path.includes("/read-states")) return Promise.resolve({ states: [] });
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({
  useTranslatePrefs: () => ({ enabled: false }),
  hubTranslation: () => undefined,
  translatesChat: () => false,
  translatesMail: () => false,
  languageName: () => "",
}));

import { onOverlayEntry } from "../../lib/back-close";
import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { resetInboxViewMemoryForTests } from "../../lib/inbox-view-memory";
import { settleRosterDefault } from "../../lib/manager-choice";
import { initI18n, t } from "../../i18n";
import { InboxDetail } from "./InboxDetail";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const received = () => ({ id: "c1", at: "2026-11-05T10:00:00Z", kind: "received", text: "1 new message" });
const chatItem = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: "lark:f1:1",
  source: "lark",
  kind: "dm",
  account: "lark",
  account_name: "Chat",
  title: "Example chat",
  preview: "hi",
  count: 1,
  tier: "action",
  updated_at: "2026-11-05T10:00:00Z",
  version: "v1",
  lark: { feed_id: "f1", is_group: false, start_position: "102" },
  chain: [received()],
  ...over,
});

/** Every watched message is visible, so the real chat read tracker can report it after expansion. */
class SeenObserver {
  constructor(private cb: IntersectionObserverCallback) {}
  observe(el: Element) {
    const entry = { target: el, isIntersecting: true, intersectionRatio: 1, rootBounds: null, intersectionRect: { height: 0 } };
    this.cb([entry as unknown as IntersectionObserverEntry], this as unknown as IntersectionObserver);
  }
  unobserve() {}
  disconnect() {}
  takeRecords() { return []; }
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const draw = async (item: InboxItem) => {
  await act(async () => root.render(<InboxDetail item={item} loaded />));
  await flush();
};
const originalToggle = (testId: string) => host.querySelector<HTMLButtonElement>(`[data-testid="${testId}"]`)!;
const pressOriginal = async (testId: string) => {
  await act(async () => originalToggle(testId).click());
  await flush();
};
const expectToggles = (open: boolean) => {
  const header = originalToggle("inbox-original-toggle");
  expect(header.textContent).toBe(t(open ? "inbox-inboxdetail.hide_source" : "inbox-inboxdetail.view_source"));
  expect(header.getAttribute("aria-expanded")).toBe(String(open));
  const row = host.querySelector<HTMLButtonElement>('[data-testid="inbox-original-row-toggle"]');
  if (open) {
    expect(row).toBeNull();
    return;
  }
  expect(row?.textContent).toBe(t("inbox-inboxdetail.view_source"));
  expect(row?.getAttribute("aria-expanded")).toBe("false");
};
const embedded = () => host.querySelector('[data-event-body="embedded"]');
const pin = () => host.querySelector(`button[aria-label="${t("inbox-inboxdetail.pin_messages")}"]`);
const conversationReads = () => calls.list.filter((c) => c.path.includes("/lark/conversations/f1/messages?"));
const readReports = () => calls.list.filter((c) => c.method === "POST" && /\/lark\/conversations\/f1\/read(?:\?|$)/.test(c.path));

beforeEach(() => {
  calls.list = [];
  resetInboxForTests();
  resetInboxViewMemoryForTests();
  vi.useFakeTimers();
  vi.stubGlobal("IntersectionObserver", SeenObserver);
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("CSS", { escape: (value: string) => value });
  Element.prototype.scrollIntoView = vi.fn();
  Element.prototype.scrollTo = vi.fn();
  localStorage.clear();
  history.replaceState(null, "", "/inbox");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
  resetInboxViewMemoryForTests();
  history.replaceState(null, "", "/");
  await initI18n("zh-Hans");
}, 30_000);

describe("an embedded original starts collapsed", () => {
  it("does not read or report a chat until expanded; the row expands it and the header hides it again", async () => {
    await draw(chatItem());
    expect(embedded()).toBeNull();
    expect(host.querySelector("[data-mid]")).toBeNull();
    expect(conversationReads()).toEqual([]);
    expect(readReports()).toEqual([]);
    expectToggles(false);
    expect(pin()).toBeNull();

    await pressOriginal("inbox-original-row-toggle");
    expect(embedded()).not.toBeNull();
    expect(host.querySelector('[data-mid="m102"]')).not.toBeNull();
    expect(conversationReads()).toHaveLength(1);
    expect(new URL(conversationReads()[0].path, "http://hub").searchParams.get("around")).toBe("102");
    expect(readReports()).toEqual([]);
    expectToggles(true);
    expect(pin()).not.toBeNull();

    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
      vi.advanceTimersByTime(450);
    });
    await flush();
    expect(readReports()).toHaveLength(1);

    await pressOriginal("inbox-original-toggle");
    expect(embedded()).toBeNull();
    expect(host.querySelector("[data-mid]")).toBeNull();
    expectToggles(false);
    expect(pin()).toBeNull();
  });

  it("a source-chain entry expands and focuses the chat without writing history", async () => {
    history.pushState(null, "", "/inbox");
    history.pushState({ personaInboxSteps: 1 }, "", "/inbox/lark:f1:1");
    await draw(chatItem({ chain: [{ ...received(), link: { type: "source", position: "42" } }] }));
    expect(embedded()).toBeNull();
    expect(conversationReads()).toEqual([]);
    const push = vi.spyOn(history, "pushState");
    const replace = vi.spyOn(history, "replaceState");

    await act(async () => host.querySelector<HTMLButtonElement>('[data-chain-entry="received"] button')!.click());
    await flush();
    expect(embedded()).not.toBeNull();
    expect(host.querySelector('[data-mid="m42"]')).not.toBeNull();
    expect(new URL(conversationReads().at(-1)!.path, "http://hub").searchParams.get("around")).toBe("42");
    expectToggles(true);
    expect(push).not.toHaveBeenCalled();
    expect(replace).not.toHaveBeenCalled();
    expect(onOverlayEntry(history.state)).toBe(false);
    expect(location.pathname).toBe("/inbox/lark:f1:1");
  });
});

describe("the original-source controls show their literal English labels", { timeout: 30_000 }, () => {
  beforeEach(async () => {
    await initI18n("en");
  }, 30_000);

  it("labels both collapsed chat controls View original, then the expanded header Hide original", async () => {
    await draw(chatItem());
    const header = originalToggle("inbox-original-toggle");
    const row = originalToggle("inbox-original-row-toggle");
    expect(header.textContent).toBe("View original");
    expect(row.textContent).toBe("View original");
    expect(header.textContent).not.toBe("Original");
    expect(row.textContent).not.toBe("Original");
    expect(embedded()).toBeNull();

    await pressOriginal("inbox-original-row-toggle");
    expect(originalToggle("inbox-original-toggle").textContent).toBe("Hide original");
    expect(host.querySelector('[data-testid="inbox-original-row-toggle"]')).toBeNull();
    expect(embedded()).not.toBeNull();
    expect(host.querySelector('[data-mid="m102"]')).not.toBeNull();
  });

  it("alternates a non-embedded event between View original and Handling", async () => {
    await draw(chatItem({ lark: undefined }));
    expect(host.querySelector("[data-event-body]")).not.toBeNull();
    expect(originalToggle("inbox-original-toggle").textContent).toBe("View original");

    await pressOriginal("inbox-original-toggle");
    expect(originalToggle("inbox-original-toggle").textContent).toBe("Handling");
    expect(host.querySelector("[data-event-body]")).toBeNull();

    await pressOriginal("inbox-original-toggle");
    expect(originalToggle("inbox-original-toggle").textContent).toBe("View original");
    expect(host.querySelector("[data-event-body]")).not.toBeNull();
  });

  it("uses the same literal labels for an embedded mail original", async () => {
    await draw(chatItem({
      id: "mail:a1:1",
      source: "mail",
      kind: "mail",
      account: "a1",
      account_name: "Mail",
      lark: undefined,
      mail: { account_id: "a1", mailbox: "INBOX", uids: [1], from: { name: "A", email: "a@example.com" } },
    }));
    expect(originalToggle("inbox-original-toggle").textContent).toBe("View original");
    expect(originalToggle("inbox-original-row-toggle").textContent).toBe("View original");
    expect(embedded()).toBeNull();

    await pressOriginal("inbox-original-row-toggle");
    expect(originalToggle("inbox-original-toggle").textContent).toBe("Hide original");
    expect(host.querySelector('[data-testid="inbox-original-row-toggle"]')).toBeNull();
    expect(embedded()).not.toBeNull();
  });
});
