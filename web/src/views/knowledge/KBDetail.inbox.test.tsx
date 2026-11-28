// @vitest-environment jsdom
// A chat's knowledge page and its Inbox items (#15356): "In Inbox" lists the open ones with their tier and folds the
// done ones, each opening its item; a row of the chat's recent messages an item holds opens that item; a chat with no
// Inbox items shows nothing extra. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { setToken } from "../../lib/api";
import type { KBDetailResponse, KBInboxItem } from "../../lib/kb";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { EntryDetail } from "./KBDetail";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;

const recent = "## 2026-11-20\n- 10:15 Alice: Can you review the plan?\n  It is in the shared doc\n- 10:16 Bob: Thanks\n- 10:17 Alice: Can you review the plan?\n";
const review: KBInboxItem = {
  id: "larkx:c1:2", title: "Review the plan", tier: "action", status: "action", summary: "Alice asks for a review", updated_at: "2026-11-20T02:15:00Z",
  messages: [{ day: "2026-11-20", line: "- 10:15 Alice: Can you review the plan?\n  It is in the shared doc" }],
};
const slides: KBInboxItem = { id: "larkx:c1:1", title: "Shared the slides", tier: "fyi", updated_at: "2026-11-19T02:00:00Z", done_at: "2026-11-19T03:00:00Z" };
const chat = (inbox?: KBInboxItem[]): KBDetailResponse => ({
  detail: { kind: "chats", id: "larkx-c1", path: "", meta: { name: "Release group", connector: "larkx", source_id: "c1" }, summary: "# Release group", recent },
  ...(inbox ? { inbox } : {}),
});

const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
async function open(resp: KBDetailResponse) {
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(url.endsWith("/kb/chats/larkx-c1") ? resp : {}), { status: 200, headers: { "Content-Type": "application/json" } }))));
  act(() => root.render(<EntryDetail kind="chats" id="larkx-c1" reloadKey={0} onToggleSidebar={() => {}} onBack={() => {}} onOpen={() => {}} onSaved={() => {}} />));
  await flush();
}
const button = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(text));
const section = () => host.querySelector<HTMLElement>("[data-kb-inbox]");
const showMessages = () => act(() => button(t("knowledge-kbdetail.last_7_days"))!.click());

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  resetManagerChoice();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  history.replaceState(null, "", "/kb/chats/larkx-c1");
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

describe("a chat's Inbox items on its knowledge page (#15356)", () => {
  it("lists the open items with their tier and folds the done ones", async () => {
    await open(chat([review, slides]));
    const s = section()!;
    expect(s.textContent).toContain(t("knowledge-kbinbox.title"));
    const rows = () => [...s.querySelectorAll<HTMLAnchorElement>("a[data-inbox-item]")];
    expect(rows().map((a) => a.dataset.inboxItem)).toEqual([review.id]);
    expect(rows()[0].textContent).toContain("Review the plan");
    expect(rows()[0].textContent).toContain("Alice asks for a review");
    expect(rows()[0].textContent).toContain(t("lib-inbox.tier_action"));
    expect(rows()[0].getAttribute("href")).toBe("/inbox/larkx%3Ac1%3A2");
    act(() => button(t("knowledge-kbinbox.done", { count: 1 }))!.click());
    expect(rows().map((a) => a.dataset.inboxItem)).toEqual([review.id, slides.id]);
    expect(rows()[1].textContent).toContain(t("lib-inbox.tier_fyi"));
  });

  it("opens an item from its row", async () => {
    await open(chat([review]));
    act(() => section()!.querySelector<HTMLAnchorElement>("a[data-inbox-item]")!.click());
    expect(location.pathname).toBe("/inbox/larkx%3Ac1%3A2");
  });

  it("links the rows of the recent messages an item holds, and only those", async () => {
    await open(chat([review, slides]));
    showMessages();
    const links = [...host.querySelectorAll<HTMLAnchorElement>("a[data-inbox-item]")].filter((a) => !section()!.contains(a));
    expect(links).toHaveLength(1);
    expect(links[0].dataset.inboxItem).toBe(review.id);
    expect(links[0].textContent).toContain("It is in the shared doc");
    expect(links[0].getAttribute("href")).toBe("/inbox/larkx%3Ac1%3A2");
    // Bob's row and Alice's later row with the same words but at another minute stay plain
    expect(host.textContent).toContain("Thanks");
    act(() => links[0].click());
    expect(location.pathname).toBe("/inbox/larkx%3Ac1%3A2");
  });

  it("names the assistant whose Inbox it is when that is not the default", async () => {
    const { chooseManager } = await import("../../lib/manager-choice");
    chooseManager("second");
    await open(chat([review]));
    expect(section()!.querySelector("a[data-inbox-item]")!.getAttribute("href")).toBe("/inbox/larkx%3Ac1%3A2?manager=second");
  });

  it("shows nothing extra for a chat without Inbox items", async () => {
    await open(chat());
    expect(host.textContent).toContain("Release group");
    expect(section()).toBeNull();
    showMessages();
    expect(host.textContent).toContain("Can you review the plan?");
    expect(host.querySelector("a[data-inbox-item]")).toBeNull();
    await act(async () => root.unmount());
    root = createRoot(host);
    await open(chat([]));
    expect(section()).toBeNull();
  });
});
