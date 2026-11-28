// @vitest-environment jsdom
// An Inbox item opens its chat's knowledge page (#15356): the item's own read names the chat (kb_chat), and
// "Knowledge page" goes to /kb/chats/<id>; an item whose read names none, an item of another assistant than the
// knowledge pages read, and an item with no chat at all show no such button. The hub is faked; the data made up.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { setToken } from "../../lib/api";
import type { InboxItem } from "../../lib/inbox";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { InboxDetail } from "./InboxDetail";
import { resetKBChatsForTests } from "./InboxKBChat";

vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined }));
vi.mock("./ConnectorThread", async (original) => ({ ...(await original<typeof import("./ConnectorThread")>()), ConnectorBody: () => null }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: ReturnType<typeof createRoot>;
let reads: string[];

const item = (id: string, more: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "larkx", kind: "group", account: "larkx", account_name: "Lark", preview: "Can you review the plan?", title: "Release group", count: 1, tier: "action",
  connector: { thread: id.split(":")[1] ?? "" }, ...more,
});
const chats: Record<string, string> = { "larkx:c1:1": "larkx-c1" };

beforeEach(async () => {
  await initI18n("en");
  resetManagerChoice();
  resetKBChatsForTests();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  reads = [];
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    // an item's own read (GET /inbox/<id>); the page's other reads (the list, folders, its tree) answer nothing
    const m = /^\/api\/v1\/inbox\/([^/]+)$/.exec(new URL(url, "http://hub").pathname);
    const id = m ? decodeURIComponent(m[1]) : "";
    if (id.includes(":")) reads.push(id);
    const body = !id.includes(":") ? {} : chats[id] ? { ...item(id), kb_chat: { id: chats[id], path: `/kb/chats/${chats[id]}` } } : item(id);
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }));
  }));
  history.replaceState(null, "", "/inbox");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  resetManagerChoice();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
});

const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const kbButton = () => host.querySelector<HTMLButtonElement>('[data-testid="inbox-kb-chat"]');

it("opens the chat's knowledge page from an item its read names it for", async () => {
  await act(async () => root.render(<InboxDetail item={item("larkx:c1:1")} loaded />));
  await flush();
  expect(kbButton()?.textContent).toBe(t("inbox-inboxdetail.open_kb_chat"));
  act(() => kbButton()!.click());
  expect(location.pathname).toBe("/kb/chats/larkx-c1");
  // asked once per item
  await act(async () => root.render(<InboxDetail item={{ ...item("larkx:c1:1"), preview: "newer" }} loaded />));
  await flush();
  expect(reads).toEqual(["larkx:c1:1"]);
});

it("shows no knowledge page for an item without a chat, of another assistant, or with nothing to have one", async () => {
  await act(async () => root.render(<InboxDetail item={item("larkx:c2:1")} loaded />));
  await flush();
  expect(reads).toEqual(["larkx:c2:1"]);
  expect(kbButton()).toBeNull();
  await act(async () => root.render(<InboxDetail item={item("larkx:c1:1", { manager: "second" })} loaded />));
  await flush();
  expect(kbButton()).toBeNull();
  await act(async () => root.render(<InboxDetail item={item("assistant:1", { source: "assistant", connector: undefined })} loaded />));
  await flush();
  expect(reads).toEqual(["larkx:c2:1", "larkx:c1:1"]);
  expect(kbButton()).toBeNull();
});
