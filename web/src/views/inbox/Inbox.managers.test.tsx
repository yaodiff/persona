// @vitest-environment jsdom
// The inbox with several managers (docs/assistant/managers.md "Clients"): the list is the current assistant's (no manager
// headings), with its own sections open or closed, a route opens the copy of its manager, the Muted dialog lists the
// current assistant's, a rule from another manager's item is read and saved with that manager, a new mail is the current
// assistant's, and an offline current assistant's list says it is offline rather than empty.
import { installedMailbox, preloadMailbox } from "../../test/installed-mailbox";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const calls = vi.hoisted(() => ({
  list: [] as { path: string; method: string; header?: string; json?: unknown }[], items: [] as unknown[],
  roster: { default: "assistant", managers: [{ id: "assistant", name: "助手", online: true, builtin: true }, { id: "m-1", name: "工作", online: true }] },
}));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown; headers?: Record<string, string> }) => {
    const header = init?.headers?.["X-Persona-Manager"];
    calls.list.push({ path, method: init?.method ?? "GET", header, json: init?.json });
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: calls.items });
    if (path === "/mail/accounts") return Promise.resolve({ accounts: header === "m-1" ? [{ id: "work", email: "work@example.com" }] : [] });
    if (path === "/managers") return Promise.resolve(calls.roster);
    if (path === "/inbox/mutes") return Promise.resolve({ mutes: header === "m-1" ? [{ id: "u1", source: "lark", tag: "chat", value: "oc_w", name: "Work group" }] : [{ id: "u2", source: "lark", tag: "chat", value: "oc_h", name: "Home group" }] });
    if (path === "/inbox/policies" && (init?.method ?? "GET") === "GET") return Promise.resolve({ policies: header === "m-1" ? [{ id: "p1", source: "lark", tag: "chat", value: "oc_w", name: "Work group", tier: "noise" }] : [] });
    if (path.startsWith("/inbox/policies")) return Promise.resolve({ policy: { id: "p1", source: "lark", tag: "chat", value: "oc_w", name: "Work group", tier: "fyi" } });
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});

import { reloadInbox, resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { chooseManager, resetManagerChoice } from "../../lib/manager-choice";
import { setRoster } from "../../lib/managers";
import { Inbox, routedItem } from "./Inbox";
import { InboxList } from "./InboxList";
import { openPolicyDialog, PolicyDialogHost } from "./PolicyDialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ROSTER = calls.roster;
const chat = { tag: "chat", value: "oc_w", name: "Work group" };
const item = (id: string, manager: string, patch: Partial<InboxItem> = {}): InboxItem => ({
  id, manager, source: "lark", kind: "group", account: "feishu", account_name: "Lark", title: `${manager} ${id}`, preview: "text", count: 1,
  updated_at: "2026-11-01T09:00:00Z", tier: "action", lark: { feed_id: "f1", is_group: true }, mutes: [chat], ...patch,
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  calls.list = [];
  calls.items = [item("lark:f1:1", "assistant"), item("lark:f1:1", "m-1"), item("lark:f2:1", "m-1", { tier: "fyi" }), item("lark:f3:1", "assistant", { tier: "fyi" })];
  resetInboxForTests();
  resetManagerChoice();
  setRoster(ROSTER);
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  Element.prototype.scrollIntoView = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  setRoster(null);
  resetManagerChoice();
  vi.unstubAllGlobals();
});

const sections = () => [...host.querySelectorAll<HTMLElement>("[data-section]")].map((s) => [s.dataset.section, s.querySelector("button[aria-expanded]")?.getAttribute("aria-expanded") ?? "always"]);
// the section bar opens on Needs you (#9053): the sections' own folds are All's
const showAll = () => act(() => host.querySelector<HTMLButtonElement>('[data-bar-choice="all"]')!.click());
const clickText = async (text: string) => {
  const b = [...document.body.querySelectorAll<HTMLElement>("button, [role=menuitem]")].find((x) => x.getAttribute("aria-label") === text || x.textContent?.trim() === text || x.textContent?.includes(text))!;
  await act(async () => b.click());
  await flush();
};

describe("the inbox of several managers", () => {
  it("lists the current assistant's items only, and keeps each one's sections open or closed on their own", async () => {
    act(() => root.render(<InboxList />));
    await flush();
    showAll();
    const rows = () => [...host.querySelectorAll<HTMLElement>("[data-item]")].map((r) => `${r.dataset.manager}:${r.dataset.item}`);
    // the default's two items, in its two sections; nothing of the other manager, and no manager's name over them
    expect(sections()).toEqual([["action", "true"], ["fyi", "true"]]);
    expect(rows()).toEqual(["assistant:lark:f1:1", "assistant:lark:f3:1"]);
    expect(host.textContent).not.toContain("工作");
    // close the default's FYI, then switch: the other assistant's list, with its FYI open as at first
    act(() => host.querySelector<HTMLElement>('[data-section="fyi"] button[aria-expanded]')!.click());
    expect(sections()).toEqual([["action", "true"], ["fyi", "false"]]);
    act(() => chooseManager("m-1"));
    await flush();
    expect(sections()).toEqual([["action", "true"], ["fyi", "true"]]);
    expect(rows()).toEqual(["m-1:lark:f1:1", "m-1:lark:f2:1"]);
    // and back: the default's FYI is still closed
    act(() => chooseManager(null));
    await flush();
    expect(sections()).toEqual([["action", "true"], ["fyi", "false"]]);
  });

  it("opens the copy of the manager the route names; a route without one opens the default's", () => {
    const items = calls.items as InboxItem[];
    expect(routedItem(items, "lark:f1:1", "m-1")?.manager).toBe("m-1");
    expect(routedItem(items, "lark:f1:1", undefined)?.manager).toBe("assistant");
    expect(routedItem(items, "lark:f2:1", undefined)?.manager).toBe("m-1"); // only m-1 has it
    expect(routedItem(items, "lark:f3:1", "m-1")).toBeUndefined();
  });

  it("lists the current assistant's mutes, and unmutes with it", async () => {
    act(() => chooseManager("m-1"));
    act(() => root.render(<InboxList />));
    await flush();
    await clickText("更多操作");
    await clickText("已静音");
    const groups = [...document.body.querySelectorAll<HTMLElement>("[data-mutes-manager]")];
    expect(groups.map((g) => g.dataset.mutesManager)).toEqual(["m-1"]);
    expect(groups[0].textContent).toContain("Work group");
    expect(groups[0].textContent).not.toContain("工作"); // no heading: the list is one assistant's
    // the read names the assistant: what it answers is stamped with the one asked
    expect(calls.list.filter((c) => c.path === "/inbox/mutes").map((c) => c.header)).toEqual(["m-1"]);
    const unmute = groups[0].querySelector("button")!;
    await act(async () => unmute.click());
    await flush();
    expect(calls.list.find((c) => c.path === "/inbox/mutes/u1")).toMatchObject({ method: "DELETE", header: "m-1" });
  });

  it.skipIf(!installedMailbox)("writes a new mail from the current assistant's accounts, sent through it", async () => {
    await preloadMailbox("Compose");
    act(() => chooseManager("m-1"));
    act(() => root.render(<Inbox onToggleSidebar={() => {}} />));
    await flush();
    expect(calls.list.filter((c) => c.path === "/mail/accounts").map((c) => c.header)).toContain("m-1");
    await clickText("写邮件");
    const to = document.body.querySelector<HTMLInputElement>('input[aria-label="收件人"]')!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(to, "someone@example.com");
      to.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await clickText("发送");
    expect(calls.list.find((c) => c.path === "/mail/send")).toMatchObject({ method: "POST", header: "m-1", json: expect.objectContaining({ account_id: "work" }) });
  });

  it("says the current assistant is offline instead of an empty list, and only then", async () => {
    setRoster({ ...ROSTER, managers: [...ROSTER.managers, { id: "m-2", name: "家里", online: false, last_seen: new Date(Date.now() - 3 * 3_600_000).toISOString() }] });
    act(() => chooseManager("m-2"));
    act(() => root.render(<InboxList />));
    await flush();
    const state = host.querySelector<HTMLElement>("[data-assistant-offline]");
    expect(state?.dataset.assistantOffline).toBe("m-2");
    expect(state?.textContent).toContain("家里 已离线");
    expect(state?.textContent).toContain("最后同步于");
    expect(state?.textContent).toContain("助手恢复在线后页面将自动更新。");
    expect(host.textContent).not.toContain("已全部处理完毕");
    expect(host.querySelector("[data-inbox-no-sources]")).toBeNull();
    // an online current assistant with nothing: as before
    act(() => chooseManager("m-1"));
    calls.items = [];
    await act(async () => { await reloadInbox(); });
    await flush();
    expect(host.querySelector("[data-assistant-offline]")).toBeNull();
  });

  it("reads and saves a rule of another manager's item with that manager", async () => {
    act(() => root.render(<PolicyDialogHost />));
    act(() => openPolicyDialog({ item: item("lark:f1:1", "m-1"), option: chat, source: "lark" }));
    await flush();
    expect(calls.list.find((c) => c.path === "/inbox/policies")?.header).toBe("m-1");
    await clickText("保存");
    expect(calls.list.find((c) => c.path === "/inbox/policies/p1")).toMatchObject({ method: "PATCH", header: "m-1" });
  });
});
