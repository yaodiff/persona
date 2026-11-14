// @vitest-environment jsdom
// U3 of the four-client compare, as ruled (DECISIONS D3 "四端对比后统一" and the lead's ruling on 2026-11-13): the reported
// step uses the shared table's per-platform glyph (`spec/icons/meanings.json` reported: sf arrow.up.message, phosphor
// chat-circle; the web draws chat-circle), and the two sides are told apart on the source's: a branded source keeps its
// brand mark, an unbranded one the web's generic source icon (`link`, mail its envelope), so no unbranded source borrows
// reported's bubble. In a real inbox a chat source is branded (Lark); only a fixture's bare `chat` takes the fallback.
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
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api: () => Promise.resolve({}) }));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});
vi.mock("../../lib/extension-slots", () => ({ useInboxItemPlace: () => undefined }));
vi.mock("../../lib/translate", () => ({ useTranslatePrefs: () => ({ enabled: false }), hubTranslation: () => undefined, translatesChat: () => false, translatesMail: () => false, languageName: () => "" }));

import type { InboxItem } from "../../lib/inbox";
import { resetInboxForTests } from "../../lib/inbox";
import type { ChainEntry } from "../../lib/inbox-events";
import { resetMailForTests } from "../../lib/mail";
import { settleRosterDefault } from "../../lib/manager-choice";
import { EventBody } from "./InboxEvent";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const chain: ChainEntry[] = [
  { id: "c1", at: "2026-01-01T11:02:00Z", kind: "received", text: "收到 2 条消息", via: "chat", count: 2 },
  { id: "c5", at: "2026-01-01T11:20:00Z", kind: "reported", text: "助手说：发了邮件", detail: "发了邮件", via: "agent" },
];
const item: InboxItem = {
  id: "chat:c1:1", source: "chat", kind: "group", account: "chat", account_name: "Team chat", title: "一件事",
  preview: "最新一句", count: 1, started_at: "2026-01-01T11:00:00Z", updated_at: "2026-01-01T11:30:00Z", chain,
};

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  resetInboxForTests();
  resetMailForTests();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<EventBody item={item} onSource={() => {}} />));
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const glyphOf = (id: string) => host.querySelector(`[data-chain-id="${id}"] [data-chain-glyph]`)?.getAttribute("data-chain-glyph");

describe("链条图标：reported 用共享表的气泡，来源那边分开", () => {
  // As ruled: an unbranded source draws the web's generic source icon (`link`); the reported step keeps the shared
  // table's chat-circle. A chat source is branded in a real inbox (Lark), so only this fixture's bare `chat` falls back.
  it("an unbranded source step draws the generic source icon, not the reported bubble", () => {
    expect(glyphOf("c1")).toBe("link");
  });

  it("a reported step keeps the shared chat-circle and never the source's glyph", () => {
    expect(glyphOf("c5")).toBe("chat");
    expect(glyphOf("c5")).not.toBe(glyphOf("c1"));
  });
});
