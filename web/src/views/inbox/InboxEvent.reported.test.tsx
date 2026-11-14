// @vitest-environment jsdom
// Reported steps in the handling chain (docs/web/web.md "Inbox"; the shared contract in
// spec/fixtures/inbox/timeline.json): the localized "Assistant says" tag plus the entry's own words (`detail`, single line in the
// row, whole when opened), the server's `text` ("Assistant says: …", written for older apps) only when there are no words.
import { act } from "react";
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

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: () => Promise.resolve({}),
}));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined, translatesChat: () => false, translatesMail: () => false, languageName: () => "" }));

import type { InboxItem } from "../../lib/inbox";
import { resetInboxForTests } from "../../lib/inbox";
import { canExpand, type ChainEntry } from "../../lib/inbox-events";
import { resetMailForTests } from "../../lib/mail";
import { settleRosterDefault } from "../../lib/manager-choice";
import { EventBody } from "./InboxEvent";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (chain: ChainEntry[]): InboxItem => ({
  id: "lark:oc_demo1:100",
  source: "lark",
  kind: "dm",
  account: "me",
  account_name: "Me",
  title: "报价",
  preview: "客户催报价了",
  count: 1,
  started_at: "2026-01-01T10:00:00Z",
  updated_at: "2026-01-01T11:30:00Z",
  chain,
});

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  resetInboxForTests();
  resetMailForTests();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const row = (id = "r1") => host.querySelector(`[data-chain-id="${id}"]`) as HTMLElement;
const rowText = (id = "r1") => row(id).querySelector("button span.truncate")?.textContent;

describe("收件箱事件：助手说的步骤", () => {
  it("a new hub's reported step draws the tag and its own words, not the text written for older apps", () => {
    const text = "助手说：发了邮件：报价单更正";
    act(() => root.render(<EventBody item={item([{ id: "r1", at: "2026-01-01T11:20:00Z", kind: "reported", text, detail: "发了邮件：报价单更正", via: "agent" }])} onSource={() => {}} />));
    const r = row();
    expect(r.getAttribute("data-chain-reported")).toBe("");
    expect(r.textContent).toContain("助手说");
    expect(rowText()).toBe("发了邮件：报价单更正"); // the words once, without the server's prefix
    expect(r.textContent).not.toContain(text);
    expect(rowText()).not.toContain("助手说：");
  });

  it("an older hub's reported step (text only) keeps drawing its text beside the tag", () => {
    act(() => root.render(<EventBody item={item([{ id: "r1", at: "2026-01-01T11:20:00Z", kind: "reported", text: "发了邮件：报价单更正", via: "agent" }])} onSource={() => {}} />));
    expect(row().textContent).toContain("助手说");
    expect(rowText()).toBe("发了邮件：报价单更正");
  });

  it("a blank detail falls back to the text", () => {
    act(() => root.render(<EventBody item={item([{ id: "r1", at: "2026-01-01T11:20:00Z", kind: "reported", text: "发了邮件：报价单更正", detail: "   ", via: "agent" }])} onSource={() => {}} />));
    expect(rowText()).toBe("发了邮件：报价单更正");
  });

  it("opens a reported step on its detail: the row is one line, the whole words are inside", async () => {
    const words = "发了邮件：报价单更正\n附件是新的报价单，请查收";
    act(() => root.render(<EventBody item={item([{ id: "r1", at: "2026-01-01T11:20:00Z", kind: "reported", text: `助手说：${words}`, detail: words, via: "agent" }])} onSource={() => {}} />));
    expect(row().querySelector("[data-chain-detail]")).toBeNull();
    expect(row().querySelector("button")!.disabled).toBe(false);
    await act(async () => row().querySelector<HTMLButtonElement>("button")!.click());
    const detail = row().querySelector("[data-chain-detail]")?.textContent;
    expect(detail).toContain("附件是新的报价单，请查收");
    expect(detail).not.toContain("助手说：");
  });

  it("detail and messages together open to both; messages alone still open", async () => {
    const both: ChainEntry = { id: "r1", at: "2026-01-01T11:20:00Z", kind: "reported", text: "助手说：发了", detail: "发了", messages: [{ sender: "Agent", text: "已发送" }] };
    expect(canExpand(both)).toBe(true);
    act(() => root.render(<EventBody item={item([both])} onSource={() => {}} />));
    await act(async () => row().querySelector<HTMLButtonElement>("button")!.click());
    const detail = row().querySelector("[data-chain-detail]");
    expect(detail?.textContent).toContain("发了");
    expect(detail?.textContent).toContain("Agent");
    expect(detail?.textContent).toContain("已发送");
  });

  it("other kinds keep expanding on their detail and drawing their text", async () => {
    const triaged: ChainEntry = { id: "r1", at: "2026-01-01T11:20:00Z", kind: "triaged", text: "需要你 · 私聊直接提问", detail: "需要你 · 私聊直接提问" };
    expect(canExpand(triaged)).toBe(true);
    act(() => root.render(<EventBody item={item([triaged])} onSource={() => {}} />));
    expect(rowText()).toBe("需要你 · 私聊直接提问");
    expect(row().getAttribute("data-chain-reported")).toBeNull();
    await act(async () => row().querySelector<HTMLButtonElement>("button")!.click());
    expect(row().querySelector("[data-chain-detail]")?.textContent).toBe("需要你 · 私聊直接提问");
  });

  it("canExpand is unchanged for every kind", () => {
    expect(canExpand({ id: "x", kind: "done", text: "已处理", detail: "按已读归档" })).toBe(true);
    expect(canExpand({ id: "x", kind: "reported", text: "a", detail: "按已读归档" })).toBe(true);
    expect(canExpand({ id: "x", kind: "reported", text: "a" })).toBe(false);
    expect(canExpand({ id: "x", kind: "reported", text: "a", detail: "" })).toBe(false);
    expect(canExpand({ id: "x", kind: "reported", text: "a", messages: [] })).toBe(false);
  });
});
