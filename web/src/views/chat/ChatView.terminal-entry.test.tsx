// @vitest-environment jsdom
// The way to an agent's terminal from its chat (#7343): on its page the terminal button switches in place; in the side
// panel (a task's or an Agent's), which shows no terminal in place, the same button opens the agent's page in its
// terminal view. While the conversation is shown, the button carries the attention dot when the terminal waits on a
// dialog: herdr calls the agent blocked, or a send here was refused with dialog_open.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
const media = vi.hoisted(() => {
  const state = { phone: false };
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver ??= class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } } as unknown as typeof IntersectionObserver;
  window.matchMedia = ((query: string) => ({
    get matches() { return state.phone && /max-width: (639|767|1023)px/.test(query); },
    media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
  return state;
});
vi.mock("../../lib/store", async (load) => ({
  ...(await load<typeof import("../../lib/store")>()),
  listenSession: () => () => {},
}));
vi.mock("../../lib/panel", async (load) => ({ ...(await load<typeof import("../../lib/panel")>()), usePanel: () => null }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));

import { applyCompleteSessions, reset, setOutbox } from "../../lib/store";
import type { Session } from "../../lib/types";
import { initI18n, t } from "../../i18n";
import { ChatView } from "./ChatView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const row = (x: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: "s1", title: "Checks", cwd: "/code", agent: "codex", status: "idle", state: "idle", ...x }) as Session;
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

let host: HTMLDivElement;
let root: Root;
beforeEach(async () => {
  media.phone = false;
  await initI18n("en");
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(String(url).includes("/items") ? { items: [], to_seq: 0 } : {}), { status: 200, headers: { "Content-Type": "application/json" } }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

async function open(view: React.ReactElement, session = row()) {
  act(() => applyCompleteSessions([session]));
  act(() => root.render(view));
  for (let i = 0; i < 6; i++) await settle();
}
const terminalButton = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("lib-terminals.terminal")}"]`);
const dot = () => terminalButton()?.parentElement?.querySelector("[data-terminal-waits]") ?? null;
async function menuItems(): Promise<string[]> {
  act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!.click());
  await settle();
  const items = [...document.querySelectorAll<HTMLElement>('[role="menu"] [role="menuitem"]')].map((x) => x.textContent ?? "");
  act(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
  return items;
}

it.each(["en", "zh-Hans", "ja"])("on the agent's page: localized terminal entries, no dot while nothing waits (%s)", async (lang) => {
  await initI18n(lang);
  expect(t("lib-terminals.terminal")).not.toBe("lib-terminals.terminal");
  await open(<ChatView mid="mac" sid="s1" inPage />);
  expect(terminalButton()).not.toBeNull();
  expect(dot()).toBeNull();
  // The desktop's existing toolbar button owns Terminal; the phone puts it in ⋯.
  expect((await menuItems()).some((x) => x.includes(t("lib-terminals.terminal")))).toBe(false);

  media.phone = true;
  await open(<ChatView key={`phone-${lang}`} mid="mac" sid="s1" inPage />);
  expect((await menuItems()).some((x) => x.includes(t("lib-terminals.terminal")))).toBe(true);
});

it("in the side panel: the terminal button opens the agent's page in its terminal view and closes the panel", async () => {
  const router = await import("../../lib/router");
  const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
  const onClose = vi.fn();
  try {
    await open(<ChatView mid="mac" sid="s1" panel={{ onClose, items: [] }} />);
    expect(terminalButton()).not.toBeNull();
    expect((await menuItems()).some((x) => x.includes(t("lib-terminals.terminal")))).toBe(false);
    act(() => terminalButton()!.click());
    expect(navigate).toHaveBeenCalledWith({ name: "session", mid: "mac", sid: "s1", view: "term" });
    expect(onClose).toHaveBeenCalled();
  } finally {
    navigate.mockRestore();
  }
});

it("the side panel's localized terminal menu item opens the terminal page and closes the panel", async () => {
  await initI18n("zh-Hans");
  media.phone = true;
  const router = await import("../../lib/router");
  const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
  const onClose = vi.fn();
  try {
    await open(<ChatView mid="mac" sid="s1" panel={{ onClose, items: [] }} />);
    act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!.click());
    await settle();
    const item = [...document.querySelectorAll<HTMLElement>('[role="menu"] [role="menuitem"]')]
      .find((x) => x.textContent?.includes(t("lib-terminals.terminal")));
    expect(item).toBeDefined();
    act(() => item!.click());
    expect(navigate).toHaveBeenCalledWith({ name: "session", mid: "mac", sid: "s1", view: "term" });
    expect(onClose).toHaveBeenCalledOnce();
  } finally {
    navigate.mockRestore();
  }
});

it("an agent herdr calls blocked lights the terminal button, on its page and in the panel", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />, row({ status: "blocked", state: "needs_you" }));
  expect(dot()).not.toBeNull();
  act(() => root.render(<ChatView key="panel" mid="mac" sid="s1" panel={{ onClose: () => {}, items: [] }} />));
  for (let i = 0; i < 6; i++) await settle();
  expect(dot()).not.toBeNull();
});

it("a send refused because a dialog has the terminal lights the terminal button", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />);
  expect(dot()).toBeNull();
  act(() => setOutbox("mac/s1", [{ id: "m1", text: "go on", images: [], at: Date.now(), state: "failed", error: "x", code: "dialog_open" }]));
  await settle();
  expect(dot()).not.toBeNull();
  const openTerminal = [...host.querySelectorAll<HTMLButtonElement>('[role="alert"] button')].find((b) => b.textContent === "Open terminal");
  expect(openTerminal).toBeDefined();
});

it("a dialog refusal offers no current-session shortcut when the chat has no terminal", async () => {
  await open(<ChatView mid="mac" sid="s1" inPage />, row({ capabilities: { read: true, terminal: false } as Session["capabilities"] }));
  act(() => setOutbox("mac/s1", [{ id: "m1", text: "go on", images: [], at: Date.now(), state: "failed", code: "dialog_open" }]));
  await settle();
  expect(host.querySelector('[role="alert"]')?.textContent).not.toContain("Open terminal");
});

it("an offline agent, or a chat that cannot show a terminal, offers none in the panel", async () => {
  await open(<ChatView mid="mac" sid="s1" panel={{ onClose: () => {}, items: [] }} />, row({ status: "offline", state: "offline", capabilities: { read: true, terminal: false } as Session["capabilities"] }));
  expect(terminalButton()).toBeNull();
});
