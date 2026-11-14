// @vitest-environment jsdom
// Two states on a done item's detail (#6611 E12, DECISIONS D3 "Agent 行的实时状态" and 11-13 15:20 "「处理对话 ›」在 done 条目上也显示"):
// - the processing-conversation link is there whenever the item has one, done or not, with the state words when known;
// - the open-item actions (说一句 / 再试一次) never are.
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
import { resetMailForTests } from "../../lib/mail";
import { settleRosterDefault } from "../../lib/manager-choice";
import { EventBody } from "./InboxEvent";

settleRosterDefault();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const handling = (state: string, extra: Record<string, unknown> = {}) => ({ owner: "manager", state, machine_id: "m1", session_id: "s-handling", since: "2026-01-01T09:00:00Z", ...extra });
const done = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "done-1", source: "mail", kind: "mail", account: "a1", account_name: "me", title: "A done matter",
  preview: "Latest line", count: 1, tier: "action", done_at: "2026-01-01T10:00:00Z", done_by: "user", ...patch,
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

const conversationLinks = () => [...host.querySelectorAll("button")].filter((b) => (b.textContent ?? "").startsWith("处理对话"));
const openActions = () => [...host.querySelectorAll("button")].map((b) => b.textContent?.trim() ?? "").filter((x) => x === "说一句" || x === "再试一次");

describe("已处理条目的详情：处理对话入口与开放动作", () => {
  it("a done item with a known now keeps the words and the conversation link on one line, with no open actions", () => {
    act(() => root.render(<EventBody item={done({ now: "done_user", handling: handling("done") })} onSource={() => {}} />));
    const status = host.querySelector("[data-event-now]")!;
    expect(status.textContent).toContain("你标了已处理");
    expect(conversationLinks()).toHaveLength(1);
    expect(status.contains(conversationLinks()[0])).toBe(true);
    expect(openActions()).toEqual([]);
  });

  it("every done key with a conversation keeps the link, and none of them offers 说一句", () => {
    for (const now of ["done_replied", "done_manager", "done_user", "done_read", "done_dismissed", "done_muted", "done_superseded", "fyi", "folded"]) {
      act(() => root.render(<EventBody item={done({ id: `d-${now}`, now, handling: handling("done") })} onSource={() => {}} />));
      expect(conversationLinks(), now).toHaveLength(1);
      expect(openActions(), now).toEqual([]);
    }
  });

  it("an older done item the manager filed keeps its legacy words and puts the link on the same line", () => {
    act(() => root.render(<EventBody item={done({ done_by: "manager", handling: handling("done") })} onSource={() => {}} />));
    const line = host.querySelector("[data-event-handling='done']")!;
    expect(line.textContent).toContain("助手处理了");
    expect(line.contains(conversationLinks()[0])).toBe(true);
    expect(host.querySelector("[data-event-manager-done]")).toBeNull();
  });

  it("an older done item with no now and no conversation keeps its old presentation", () => {
    act(() => root.render(<EventBody item={done({ done_by: "manager", handling: handling("done") })} onSource={() => {}} />));
    act(() => root.render(<EventBody item={done({ done_by: "manager", handling: undefined })} onSource={() => {}} />));
    expect(host.querySelector("[data-event-manager-done]")?.textContent).toBe("助手处理了");
    expect(conversationLinks()).toEqual([]);
    act(() => root.render(<EventBody item={done({ done_by: "user", handling: undefined })} onSource={() => {}} />));
    expect(host.querySelector("[data-event-manager-done]")).toBeNull();
    expect(host.querySelector("[data-event-handling]")).toBeNull();
    expect(conversationLinks()).toEqual([]);
    expect(openActions()).toEqual([]);
  });

  it("never shows a live open-item word on a done item (it is not waiting or working any more)", () => {
    // A done item whose handling still says waiting / looking / agent (the hub keeps the handling of a filed item):
    // the state sentence must not claim it is still in progress. With no `now`, D3 shows the conversation link alone.
    for (const [state, urgent] of [["waiting", false], ["looking", false], ["looking", true], ["agent", false]] as const) {
      act(() => root.render(<EventBody item={done({ id: `d-${state}-${urgent}`, urgent, handling: handling(state) })} onSource={() => {}} />));
      const text = host.textContent ?? "";
      for (const live of ["等对方回复", "AI 正在看", "Agent 在做", "AI 正在准备建议"]) expect(text, `${state}/${urgent}`).not.toContain(live);
      expect(conversationLinks(), `${state}/${urgent}`).toHaveLength(1);
    }
  });

  it("keeps the manager's words on a done item whose handling still says waiting", () => {
    act(() => root.render(<EventBody item={done({ done_by: "manager", handling: handling("waiting") })} onSource={() => {}} />));
    const text = host.textContent ?? "";
    expect(text).toContain("助手处理了");
    expect(text).not.toContain("等对方回复");
    expect(conversationLinks()).toHaveLength(1);
  });

  it("an open item still gets both the link and 说一句", () => {
    act(() => root.render(<EventBody item={done({ done_at: undefined, done_by: undefined, handling: handling("waiting") })} onSource={() => {}} />));
    expect(conversationLinks()).toHaveLength(1);
    expect(openActions()).toContain("说一句");
  });

  it("an open failed item gets 再试一次 and no 说一句", () => {
    act(() => root.render(<EventBody item={done({ done_at: undefined, done_by: undefined, handling: handling("failed", { error: "超时" }) })} onSource={() => {}} />));
    expect(conversationLinks()).toHaveLength(1);
    expect(openActions()).toContain("再试一次");
    expect(openActions()).not.toContain("说一句");
  });
});
