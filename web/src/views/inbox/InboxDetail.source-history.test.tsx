// @vitest-environment jsdom
// "Original" pages over the event body (a document, a connector thread, a Moments post — and the extension's own "View in
// chat") get one same-address history entry, so Back returns to the event and "Handling" takes the entry back off (E1).
// The entry must never outlive the page it was opened from: switching items, ‹, Done and Back all land where they should,
// and a chat or mail item keeps its original collapsed until Original or a chain entry opens it, even when an extension
// offers a page for it.
import { StrictMode, act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  Object.assign(globalThis, {
    ResizeObserver: globalThis.ResizeObserver ?? class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: globalThis.IntersectionObserver ?? class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } },
  });
  globalThis.CSS ??= { escape: (s: string) => s } as unknown as typeof CSS;
});

// An extension offers a page for each connector: a chat or mail item's event must still sit above its source, and an item
// that is not an event keeps the extension's page.
const places: Record<string, unknown> = {
  lark: { extension: "chat-ext", extension_name: "Chat extension", id: "event", title: "Event", placement: "inbox_item", size: "full", connector: "lark" },
  docs: { extension: "doc-ext", extension_name: "Doc extension", id: "event", title: "Event", placement: "inbox_item", size: "full", connector: "docs" },
  mail: { extension: "mail-ext", extension_name: "Mail extension", id: "event", title: "Event", placement: "inbox_item", size: "full", connector: "mail" },
};
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: (source?: string) => (source ? places[source] : undefined) }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined, translatesChat: () => false, translatesMail: () => false, languageName: () => "" }));
vi.mock("./LarkThread", () => ({
  LarkThread: (p: { chat: { focus?: string } }) => <div data-chat-body data-focus={p.chat.focus ?? ""} />,
  chatOfItem: () => ({}),
}));
vi.mock("./InboxLine", () => ({ InboxLine: () => null }));
vi.mock("./ConnectorThread", async (original) => ({ ...(await original<typeof import("./ConnectorThread")>()), ConnectorBody: () => <div data-connector-body /> }));
// What the extension draws is its own business here: a page that says it is there, with its "View in chat" press.
vi.mock("../settings/ExtensionView", () => ({
  ExtensionUI: ({ onOpenSource }: { onOpenSource?: () => void }) => (
    <div data-extension-page><button type="button" onClick={onOpenSource}>View in chat</button></div>
  ),
}));

import type { InboxItem } from "../../lib/inbox";
import { resetInboxForTests } from "../../lib/inbox";
import { onOverlayEntry } from "../../lib/back-close";
import { backToInboxList, stepsToInbox } from "../../lib/inbox-nav";
import { settleRosterDefault } from "../../lib/manager-choice";
import { navigate } from "../../lib/router";
import { t } from "../../i18n";
import { InboxDetail } from "./InboxDetail";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const received = () => ({ id: "c1", at: "2026-11-05T10:00:00Z", kind: "received", text: "1 new message" });
const chatItem = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: "lark:f1:1", source: "lark", kind: "dm", account: "lark", account_name: "Chat", title: "Example chat", preview: "hi",
  count: 1, tier: "action", updated_at: "2026-11-05T10:00:00Z", version: "v1",
  lark: { feed_id: "f1", is_group: false, start_position: "10" }, chain: [received()], ...over,
});
/** A document item: no chat, no mail, so its "Original" replaces the event body. */
const docItem = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: "docs:d1", source: "docs", kind: "doc", account: "docs", account_name: "Docs", title: "Release plan", preview: "mentioned you",
  count: 1, tier: "action", updated_at: "2026-11-05T10:00:00Z", version: "v1",
  doc: { url: "https://example.com/d/1", type: "doc", notice: "mention", rows: [{ ref: "r1", title: "A row" }] },
  chain: [received()],
  ...over,
});
/** A connector's thread: its body is the connector's page, and it is never embedded under the event. */
const connectorItem = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: "github:o/r#1", source: "github", kind: "mention", account: "github", account_name: "GitHub", title: "A pull request",
  preview: "please look", count: 1, tier: "action", updated_at: "2026-11-05T10:00:00Z", version: "v1",
  connector: { thread: "github:o/r#1", can_reply: true }, chain: [received()], ...over,
});
/** A chat item from an older hub (no event fields): the extension's page, as before. */
const legacyChat = (over: Partial<InboxItem> = {}): InboxItem => chatItem({ chain: undefined, ...over });

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const draw = async (it: InboxItem, strict = false) => {
  await act(async () => root.render(strict ? <StrictMode><InboxDetail item={it} loaded /></StrictMode> : <InboxDetail item={it} loaded />));
  await flush();
};
const press = async (label: string) => {
  const b = [...host.querySelectorAll("button")].find((x) => (x.textContent ?? "").replace(/\s+/g, " ").includes(label));
  expect(b, `button ${label}`).toBeDefined();
  await act(async () => b!.click());
  await flush();
};
const eventBody = () => host.querySelector("[data-event-body]");
const extensionPage = () => host.querySelector("[data-extension-page]");
/** A close from the page takes its entry off a tick later (back-close's 0 ms timer, then the Back it causes). */
const settleTimers = async () => {
  await act(async () => { await new Promise((r) => setTimeout(r, 30)); });
  await flush();
};
/** Runs `go`, then waits for the Back it causes (history.go() / history.back() are asynchronous). */
const popped = (go: () => void) =>
  new Promise<void>((done, fail) => {
    const timer = setTimeout(() => fail(new Error("no Back happened")), 1_000);
    addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
    go();
  });

/** A list entry, then an item opened from it: the history every case here starts on. */const openFromList = (id = "lark:f1:1") => {
  history.pushState(null, "", "/inbox");
  history.pushState({ personaInboxSteps: 1 }, "", `/inbox/${id}`);
};

beforeEach(() => {
  history.replaceState(null, "", "/inbox");
  resetInboxForTests();
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, text: async () => JSON.stringify({}) })));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
});

describe("a chat item's event keeps its chat collapsed, extension page or not", () => {
  it("mounts the chat and Pin only after Original expands it", async () => {
    await draw(chatItem());
    expect(host.querySelector('[data-event-body="embedded"]')).toBeNull();
    expect(host.querySelector("[data-chat-body]")).toBeNull();
    expect(host.querySelector("[data-extension-inbox]")).toBeNull();
    expect(host.querySelector('button[aria-label="Pin 消息"]')).toBeNull();
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="inbox-original-row-toggle"]')!.click());
    await flush();
    expect(host.querySelector('[data-event-body="embedded"]')).not.toBeNull();
    expect(host.querySelector("[data-chat-body]")).not.toBeNull();
    expect(host.querySelector('button[aria-label="Pin 消息"]')).not.toBeNull();
  });

  it("a chain entry expands and focuses the chat in place, adding no history entry", async () => {
    openFromList();
    await draw(chatItem({ chain: [{ ...received(), link: { type: "source", position: "42" } }] }));
    expect(host.querySelector("[data-chat-body]")).toBeNull();
    await press("1 new message");
    expect(host.querySelector('[data-event-body="embedded"]')).not.toBeNull();
    expect(host.querySelector("[data-chat-body]")?.getAttribute("data-focus")).toBe("42");
    expect(onOverlayEntry(history.state)).toBe(false);
    expect(eventBody()).not.toBeNull(); // the event is still there above it
  });

  it("an older hub's item (no event fields) keeps the extension's page, as before", async () => {
    openFromList();
    await draw(legacyChat());
    expect(eventBody()).toBeNull();
    expect(extensionPage()).not.toBeNull();
    expect(host.querySelector("[data-chat-body]")).toBeNull();
    expect(onOverlayEntry(history.state)).toBe(false);
  });

  it("a mail item's event starts with its message collapsed, extension page or not", async () => {
    await draw(docItem({ id: "mail:a1:1:1", source: "mail", doc: undefined, mail: { account_id: "a1", mailbox: "INBOX", uids: [1], from: { name: "A", email: "a@example.com" } } }));
    expect(host.querySelector('[data-event-body="embedded"]')).toBeNull();
    expect(host.querySelector("[data-extension-inbox]")).toBeNull();
    expect(host.querySelector<HTMLButtonElement>('[data-testid="inbox-original-row-toggle"]')?.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("the original page of an event over its own body", () => {
  it("adds one entry; Back returns to the event body", async () => {
    openFromList();
    await draw(docItem());
    expect(eventBody()).not.toBeNull();
    await press(t("inbox-inboxdetail.view_source"));
    expect(eventBody()).toBeNull(); // the document's rows took its place
    expect(onOverlayEntry(history.state)).toBe(true);
    await popped(() => history.back());
    await flush();
    expect(eventBody()).not.toBeNull();
    expect(location.pathname).toBe("/inbox/lark:f1:1");
    expect(onOverlayEntry(history.state)).toBe(false);
    expect(stepsToInbox(history.state)).toBe(1); // the item's way back to the list is still there
  });

  it("an event with an extension page for its source opens its own body, not the extension page", async () => {
    openFromList("docs:d1");
    await draw(docItem());
    await press(t("inbox-inboxdetail.view_source"));
    expect(extensionPage()).toBeNull();
    expect(eventBody()).toBeNull();
    expect(host.textContent).toContain("A row"); // the document's own rows
    await popped(() => history.back());
    await flush();
    expect(eventBody()).not.toBeNull();
  });

  it("Handling takes its entry back off: one Back then leaves for the list", async () => {
    openFromList();
    await draw(docItem());
    await press(t("inbox-inboxdetail.view_source"));
    // closing it from the page takes its entry off in a Back of its own
    await popped(() => {
      const b = [...host.querySelectorAll("button")].find((x) => x.textContent?.trim() === t("inbox-inboxdetail.view_event"))!;
      b.click();
    });
    await flush();
    expect(eventBody()).not.toBeNull();
    expect(onOverlayEntry(history.state)).toBe(false);
    expect(stepsToInbox(history.state)).toBe(1);
    await popped(() => history.back());
    expect(location.pathname).toBe("/inbox");
    expect(stepsToInbox(history.state)).toBe(0);
  });

  it("‹ on the item from its original page goes back to the list in one step", async () => {
    openFromList();
    await draw(docItem());
    await press(t("inbox-inboxdetail.view_source"));
    await popped(backToInboxList);
    expect(location.pathname).toBe("/inbox");
    expect(stepsToInbox(history.state)).toBe(0);
    expect(onOverlayEntry(history.state)).toBe(false);
  });

  it("Done on the item from its original page goes back to the list too", async () => {
    openFromList();
    await draw(docItem());
    await press(t("inbox-inboxdetail.view_source"));
    await act(() => popped(() => {
      const done = [...host.querySelectorAll("button")].filter((b) => b.textContent?.trim() === t("inbox-inboxdetail.done")).at(-1)!;
      done.click();
    }));
    await flush();
    expect(location.pathname).toBe("/inbox");
  });

  it("switching to another item while the original is open leaves no dead entry", async () => {
    openFromList();
    await draw(docItem());
    await press(t("inbox-inboxdetail.view_source"));
    // another row on a wide screen: the router takes the original's entry over
    await act(async () => navigate({ name: "inbox", id: "docs:other" }));
    await draw(docItem({ id: "docs:other" }));
    await flush();
    expect(onOverlayEntry(history.state)).toBe(false);
    expect(eventBody()).not.toBeNull(); // the new item, not a copy of the old original
    await popped(() => history.back());
    expect(location.pathname).toBe("/inbox/lark:f1:1");
    await popped(() => history.back());
    expect(location.pathname).toBe("/inbox");
  });

  it("StrictMode's double effect still leaves one entry, and one Back closes it", async () => {
    openFromList();
    await draw(docItem(), true);
    await press(t("inbox-inboxdetail.view_source"));
    await flush();
    expect(eventBody()).toBeNull();
    await popped(() => history.back());
    await flush();
    expect(eventBody()).not.toBeNull();
    expect(onOverlayEntry(history.state)).toBe(false);
    await popped(() => history.back());
    expect(location.pathname).toBe("/inbox"); // and the list, not another copy of the item
  });

  it("a connector thread's Original has an entry too", async () => {
    history.replaceState(null, "", "/inbox/github:o%2Fr#1");
    history.pushState({ personaInboxSteps: 1 }, "", "/inbox/github:o%2Fr#1");
    await draw(connectorItem());
    await press(t("inbox-inboxdetail.view_source"));
    expect(host.querySelector("[data-connector-body]")).not.toBeNull();
    expect(onOverlayEntry(history.state)).toBe(true);
    await popped(() => history.back());
    await flush();
    expect(eventBody()).not.toBeNull();
  });

  it("a plain item with no event over its own body adds no entry", async () => {
    openFromList();
    await draw(chatItem({ chain: undefined }), true);
    expect(onOverlayEntry(history.state)).toBe(false);
  });

  it("an extension page's \"View in chat\" adds one entry, and the page's back button takes it off", async () => {
    openFromList();
    await draw(legacyChat());
    expect(extensionPage()).not.toBeNull();
    await press("View in chat");
    expect(host.querySelector("[data-inbox-source]")).not.toBeNull();
    expect(onOverlayEntry(history.state)).toBe(true);
    // the page's own back button: one Back, which closes the source page rather than the item
    await popped(() => press(host.querySelector("[data-inbox-source] button")!.textContent!));
    await flush();
    expect(host.querySelector("[data-inbox-source]")).toBeNull();
    expect(extensionPage()?.className).not.toContain("hidden");
    expect(onOverlayEntry(history.state)).toBe(false);
    expect(location.pathname).toBe("/inbox/lark:f1:1");
  });

  it("browser Back from an extension page's source returns to the extension page, keeping the item's way back", async () => {
    openFromList();
    await draw(legacyChat());
    await press("View in chat");
    await popped(() => history.back());
    await flush();
    expect(host.querySelector("[data-inbox-source]")).toBeNull();
    expect(extensionPage()?.className).not.toContain("hidden");
    expect(stepsToInbox(history.state)).toBe(1);
  });

  it("‹ with an extension page's source open goes back to the list in one step", async () => {
    openFromList();
    await draw(legacyChat());
    await press("View in chat");
    await popped(backToInboxList);
    expect(location.pathname).toBe("/inbox");
    expect(stepsToInbox(history.state)).toBe(0);
    expect(onOverlayEntry(history.state)).toBe(false);
  });

  it("switching items with an extension page's source open takes its entry, leaving no dead page", async () => {
    openFromList();
    await draw(legacyChat());
    await press("View in chat");
    await act(async () => navigate({ name: "inbox", id: "lark:f2:2" }));
    await draw(legacyChat({ id: "lark:f2:2", lark: { feed_id: "f2", is_group: false } }));
    await settleTimers();
    expect(onOverlayEntry(history.state)).toBe(false);
    expect(host.querySelector("[data-inbox-source]")).toBeNull();
    await popped(() => history.back());
    expect(location.pathname).toBe("/inbox/lark:f1:1");
    await popped(() => history.back());
    expect(location.pathname).toBe("/inbox");
  });

  it("two items, each with its original open in turn: one entry each, no pile-up", async () => {
    openFromList();
    await draw(docItem());
    await press(t("inbox-inboxdetail.view_source"));
    await act(async () => navigate({ name: "inbox", id: "docs:d2" }));
    await draw(docItem({ id: "docs:d2" }));
    await press(t("inbox-inboxdetail.view_source"));
    expect(onOverlayEntry(history.state)).toBe(true);
    await popped(() => history.back());
    await flush();
    expect(location.pathname).toBe("/inbox/docs%3Ad2");
    expect(eventBody()).not.toBeNull();
    await popped(() => history.back());
    expect(location.pathname).toBe("/inbox/lark:f1:1");
    await popped(() => history.back());
    expect(location.pathname).toBe("/inbox");
  });

  it("Handling followed at once by ‹ still goes back to the list in one step", async () => {
    openFromList();
    await draw(docItem());
    await press(t("inbox-inboxdetail.view_source"));
    await act(() => popped(() => {
      // both in the same tick: Handling's entry is taken back before its own Back had a chance to run
      const b = [...host.querySelectorAll("button")].find((x) => x.textContent?.trim() === t("inbox-inboxdetail.view_event"))!;
      b.click();
      backToInboxList();
    }));
    await settleTimers();
    expect(location.pathname).toBe("/inbox");
    expect(stepsToInbox(history.state)).toBe(0);
    expect(onOverlayEntry(history.state)).toBe(false);
  });

  it("the item going away under an open original leaves no dead entry", async () => {    openFromList();
    await draw(docItem());
    await press(t("inbox-inboxdetail.view_source"));
    // the hub files it (a push): the detail now has no item
    await draw(undefined as unknown as InboxItem);
    await settleTimers();
    expect(onOverlayEntry(history.state)).toBe(false);
    await popped(() => history.back());
    expect(location.pathname).toBe("/inbox");
  });
});
