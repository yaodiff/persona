// @vitest-environment jsdom
// The personal Agents on the Messages page (mixed-feed.ts): listed with the Feishu chats by their latest message, each
// with its own face, its service's name and its last line; the pinned ones on the shelf with the pinned chats; the
// filter and the mix switch; one opens on the page's right-hand side in the Agents page's conversation view.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentSession } from "../../lib/agents";
import type { LarkConversation } from "./lark-chats";

vi.mock("../../lib/store", async (original) => ({ ...(await original<typeof import("../../lib/store")>()), useLarkOn: () => true }));
await import("./Messages");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const at = (h: number) => Date.UTC(2026, 10, 20, h);
const chat = (id: string, h: number, more: Partial<LarkConversation> = {}): LarkConversation => ({
  id, name: `Chat ${id}`, is_group: false, unread: 0, at_me: false, muted: false, preview: `about ${id}`, is_mine: false,
  updated_at: new Date(at(h)).toISOString(), ...more,
});
const agent = (key: string, h: number, more: Partial<AgentSession> = {}): AgentSession => ({
  id: `example:${key}`, source: "example", sessionKey: key, provider: "grok_bot", title: `Helper ${key}`, canSend: true, unreadCount: 0,
  rankTimeMs: at(h), ...more,
});

let host: HTMLDivElement;
let root: Root;
let conversations: { conversations: LarkConversation[]; next_cursor?: string };
let pinned: LarkConversation[];
let sessions: AgentSession[];
// the hub's copy of the prefs, for a GET (a PUT answers what it saved)
let prefsOnHub: unknown;
const reads: string[] = [];

/** A fresh module graph per test: the chat list and the Agents' list are the tab's own stores. */
const load = async () => {
  vi.resetModules();
  (await import("./home")).setLarkHome(true);
  return { ...(await import("./Messages")), ...(await import("./mix")), ...(await import("../../lib/agents")), i18n: await import("../../i18n") };
};
const flush = async (n = 30) => {
  for (let i = 0; i < n; i++) await act(async () => { await Promise.resolve(); });
};
const rowKeys = () => [...host.querySelectorAll<HTMLElement>("section[aria-label] > div > div > div.mt-3 > a[data-chat]")].map((a) => a.dataset.chat);
const shelfKeys = () => [...host.querySelectorAll<HTMLElement>("section > div.flex.gap-1 > a[data-chat]")].map((a) => a.dataset.chat);

beforeEach(() => {
  reads.length = 0;
  prefsOnHub = { pins: [] };
  localStorage.clear();
  conversations = { conversations: [chat("c9", 9), chat("c5", 5)] };
  pinned = [chat("p1", 1)];
  sessions = [
    agent("a7", 7, { previewText: "Draft is ready", unreadCount: 3, hasAvatar: true, avatarVersion: "v1", quota: { state: "ok", percent: 70, show: false } }),
    agent("a3", 3, { previewText: "Thanks", previewIsMine: true, quota: { state: "exhausted" } }),
    agent("a8", 8, { provider: "muse" }),
  ];
  vi.stubGlobal("IntersectionObserver", class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } });
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  Element.prototype.scrollTo = vi.fn();
  Element.prototype.scrollIntoView = vi.fn();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    reads.push(url);
    let body: unknown = {};
    if (url.includes("/agents/prefs")) body = init?.body ? JSON.parse(String(init.body)) : prefsOnHub;
    else if (url.includes("/lark/conversations?")) body = { account_id: "acc", ...conversations };
    else if (url.includes("/lark/pinned")) body = { account_id: "acc", conversations: pinned };
    else if (url.includes("/lark/accounts")) body = { accounts: [] };
    else if (/\/agents\/sessions\/[^/?]+\/messages/.test(url)) {
      body = { messages: [{ externalId: "m1", sessionKey: "a7", role: "assistant", blocks: [{ type: "text", text: "Here is the draft" }], createTimeMs: at(7), position: 1 }], hasMore: false };
    } else if (/\/agents\/sessions\/[^/?]+\/outbox/.test(url)) body = { outbox: [] };
    else if (url.includes("/agents/sessions")) body = { sessions };
    else if (/\/agents(\?|$)/.test(url)) body = { sources: [] };
    return { ok: true, status: 200, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("personal Agents on the Messages page", () => {
  it("are mixed with the chats by their latest message, each with its service and last line", async () => {
    const m = await load();
    await act(async () => root.render(<m.Messages onToggleSidebar={() => {}} />));
    await flush();
    expect(rowKeys()).toEqual(["c9", "agent:example:a8", "agent:example:a7", "c5", "agent:example:a3"]);
    const a7 = host.querySelector<HTMLAnchorElement>('[data-chat="agent:example:a7"]')!;
    expect(a7.textContent).toContain("Grok Bot");
    expect(a7.textContent).toContain("Draft is ready");
    expect(a7.textContent).toContain("3");
    expect(a7.querySelector("img")?.getAttribute("src")).toContain("/agents/sessions/example%3Aa7/avatar?v=v1");
    expect(a7.getAttribute("href")).toBe("/messages/agent/example%3Aa7");
    expect(host.querySelector('[data-chat="agent:example:a3"]')!.textContent).toContain(m.i18n.t("lark-chats.preview_mine", { text: "Thanks" }));
    expect(host.querySelector('[data-chat="agent:example:a8"]')!.textContent).toContain("Muse");
  });

  it("show what is left of their allowance as a ring and a battery, and nothing without a figure", async () => {
    const m = await load();
    await act(async () => root.render(<m.Messages onToggleSidebar={() => {}} />));
    await flush();
    const row = (key: string) => host.querySelector<HTMLElement>(`[data-chat="${key}"]`)!;
    // show: false is an older account's default, not "don't draw"
    expect(row("agent:example:a7").querySelectorAll('[data-quota="ok"]')).toHaveLength(2);
    expect(row("agent:example:a7").textContent).toContain("70%");
    expect(row("agent:example:a3").querySelectorAll('[data-quota="exhausted"]')).toHaveLength(2);
    expect(row("agent:example:a3").textContent).toContain("0%");
    expect(row("agent:example:a8").querySelector("[data-quota]")).toBeNull();
    expect(row("c9").querySelector("[data-quota]")).toBeNull();
  });

  it("wait for the next Feishu page when they are older than every chat loaded", async () => {
    conversations = { conversations: [chat("c9", 9), chat("c5", 5)], next_cursor: "2" };
    const m = await load();
    await act(async () => root.render(<m.Messages onToggleSidebar={() => {}} />));
    await flush();
    expect(rowKeys()).toEqual(["c9", "agent:example:a8", "agent:example:a7", "c5"]);
  });

  it("sit on the shelf with the pinned chats once pinned, and leave the list", async () => {
    const m = await load();
    m.updateAgentPrefs((p) => m.togglePin(p, "example:a7"));
    await act(async () => root.render(<m.Messages onToggleSidebar={() => {}} />));
    await flush();
    expect(shelfKeys()).toEqual(["agent:example:a7", "p1"]);
    expect(rowKeys()).not.toContain("agent:example:a7");
    // its face keeps its charge ring there
    expect(host.querySelector('section > div.flex.gap-1 > a[data-chat="agent:example:a7"] [data-quota="ok"]')).not.toBeNull();
  });

  it("can be filtered to, or away, and searched with the chats", async () => {
    const m = await load();
    await act(async () => root.render(<m.Messages onToggleSidebar={() => {}} />));
    await flush();
    const chip = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent === label)!;
    await act(async () => chip(m.i18n.t("lark-messages.filter_agents")).click());
    expect(rowKeys()).toEqual(["agent:example:a8", "agent:example:a7", "agent:example:a3"]);
    expect(shelfKeys()).toEqual([]);
    await act(async () => chip(m.i18n.t("lark-messages.filter_lark")).click());
    expect(rowKeys()).toEqual(["c9", "c5"]);
    expect(shelfKeys()).toEqual(["p1"]);
    await act(async () => chip(m.i18n.t("lark-messages.filter_all")).click());
    const input = host.querySelector("input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "draft");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(rowKeys()).toEqual(["agent:example:a7"]);
  });

  it("are not listed with the mix switch off, and the page is Feishu's alone", async () => {
    const m = await load();
    m.setMessagesMix(false);
    await act(async () => root.render(<m.Messages onToggleSidebar={() => {}} />));
    await flush();
    expect(rowKeys()).toEqual(["c9", "c5"]);
    expect(host.textContent).not.toContain(m.i18n.t("lark-messages.filter_agents"));
  });

  it("open on the page's right-hand side, marked in the list, with a pin in the title row", async () => {
    const m = await load();
    await act(async () => root.render(<m.Messages agent="example:a7" onToggleSidebar={() => {}} />));
    await flush();
    await vi.waitFor(() => expect(host.textContent).toContain("Here is the draft"));
    // the open conversation's title row: its battery (wide layout) beside the name
    expect(host.querySelectorAll('[role="img"][data-quota="ok"]').length).toBeGreaterThanOrEqual(2);
    expect(reads.some((u) => u.includes("/agents/sessions/example%3Aa7/messages"))).toBe(true);
    expect(host.querySelector('[data-chat="agent:example:a7"]')!.getAttribute("aria-current")).toBe("page");
    // one title row: the page's own, none of the Agents page's
    expect(host.querySelectorAll("header")).toHaveLength(1);
    expect(host.querySelector("textarea")).not.toBeNull();
    const pin = host.querySelector<HTMLButtonElement>(`button[aria-label="${m.i18n.t("lark-messages.pin_agent")}"]`)!;
    await act(async () => pin.click());
    await flush();
    expect(reads.filter((u) => u.includes("/agents/prefs")).length).toBeGreaterThan(0); // saved on the hub, shared with the other apps
    expect(shelfKeys()).toEqual(["agent:example:a7", "p1"]);
  });
});
