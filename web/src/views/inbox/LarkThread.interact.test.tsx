// @vitest-environment jsdom
// The Feishu chat page's interactions against a fake hub (fetch): a reaction the hub turns down goes back, a card
// button's failure says why and a success reads the window around the pressed message again, the draft and its quote
// come back with the chat and are gone once sent, a group's @ list offers "Everyone" first, and 「让 Persona 总结」
// starts one conversation however often it is pressed while that one is starting.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LarkMessage } from "../../lib/inbox";
import { draftKey } from "../../lib/lark-chat";

vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { toast } from "../../ui/atoms";
import { LarkThread, type LarkChatRef } from "./LarkThread";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Reply = { status?: number; body: unknown };
// A route may hold its answer back (a promise) to stand for a hub that takes a while.
type Route = (url: string, init: RequestInit) => Reply | undefined | Promise<Reply | undefined>;
let route: Route;
const calls: { url: string; method: string; body?: unknown }[] = [];

const ok = (body: unknown) => ({ status: 200, body });
const msg = (id: string, extra: Partial<LarkMessage> = {}): LarkMessage => ({
  id, position: id.replace(/\D/g, "") || "1", sender_name: "示例同事", created_at: "2026-11-01T09:00:00Z", text: `消息 ${id}`, ...extra,
});

// The production debounces the waits below run out (not LarkThread's 450 ms mark-read flush, which no test here reads).
const DRAFT_SAVE_MS = 300;
const MEMBER_LOOKUP_MS = 140;

let host: HTMLDivElement;
let root: Root;
const flush = async (n = 8) => {
  for (let i = 0; i < n; i++) await act(async () => { await Promise.resolve(); });
};
const buttons = () => [...host.querySelectorAll<HTMLButtonElement>("button")];
const byText = (text: string) => buttons().find((b) => b.textContent?.includes(text));
const render = async (chat: LarkChatRef) => {
  await act(async () => root.render(<LarkThread chat={chat} />));
  await flush();
};
const typeInto = (el: HTMLTextAreaElement, value: string) => {
  const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  set.call(el, value);
  el.setSelectionRange(value.length, value.length);
  el.dispatchEvent(new Event("input", { bubbles: true }));
};

// jsdom has no IntersectionObserver: every row is on screen as soon as it is watched.
class FakeObserver {
  constructor(private cb: IntersectionObserverCallback) {}
  observe(el: Element) {
    const entry = { target: el, isIntersecting: true, intersectionRatio: 1, rootBounds: null, intersectionRect: el.getBoundingClientRect() } as unknown as IntersectionObserverEntry;
    queueMicrotask(() => this.cb([entry], this as unknown as IntersectionObserver));
  }
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return [];
  }
}

beforeEach(() => {
  vi.stubGlobal("IntersectionObserver", FakeObserver);
  Element.prototype.scrollIntoView = vi.fn();
  calls.length = 0;
  localStorage.clear();
  vi.mocked(toast).mockClear();
  route = () => undefined;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method ?? "GET";
    const body = typeof init.body === "string" ? JSON.parse(init.body) : undefined;
    calls.push({ url, method, body });
    const r = (await route(url, init)) ?? (url.includes("/read-states") ? ok({ states: [] }) : url.endsWith("/read") ? ok({}) : { status: 404, body: { error: "not found" } });
    return { ok: (r.status ?? 200) < 400, status: r.status ?? 200, text: async () => JSON.stringify(r.body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("飞书会话页的交互", () => {
  it("表情回复被 hub 拒绝时退回原样并提示", async () => {
    route = (url, init) => {
      if (url.includes("/conversations/f1/messages")) return ok({ messages: [msg("m1", { reactions: [{ key: "OK", count: 1, users: ["示例同事"] }] })] });
      if (url.includes("/reactions") && init.method === "POST") return { status: 500, body: { error: "飞书拒绝了" } };
      return undefined;
    };
    await render({ feed: "f1", title: "示例" });
    const chip = () => host.querySelector<HTMLButtonElement>("button[aria-pressed]")!;
    expect(chip().getAttribute("aria-pressed")).toBe("false");
    expect(chip().textContent).toContain("1");
    await act(async () => chip().click());
    await flush();
    const posted = calls.find((c) => c.url.includes("/messages/m1/reactions"));
    expect(posted?.body).toEqual({ key: "OK", remove: false });
    expect(chip().getAttribute("aria-pressed")).toBe("false");
    expect(chip().textContent).toContain("1");
    expect(chip().textContent).not.toContain("2");
    expect(vi.mocked(toast).mock.calls.some(([text, tone]) => tone === "error" && String(text).includes("飞书拒绝了"))).toBe(true);
  });

  it("卡片按钮失败时说明原因；成功后转圈到重读完，读的是按下那条周围", async () => {
    const card = { kind: "card", metadata: { body: { children: [
      { tag: "button", actionId: "approve", actionMethod: 2, children: [{ tag: "plain_text", text: "批准" }] },
      { tag: "button", actionId: "like", actionMethod: 2, tooltip: "点赞", children: [{ tag: "standard_icon", token: "thumbsup_outlined" }] },
    ] } } };
    let fail = true;
    route = (url, init) => {
      if (url.includes("/conversations/f1/messages")) return ok({ messages: [msg("m5", { parts: [card] as LarkMessage["parts"] })], has_newer: true });
      if (url.includes("/card-actions/") && init.method === "POST") return fail ? { status: 500, body: { error: "回调超时" } } : ok({});
      return undefined;
    };
    await render({ feed: "f1", title: "示例" });
    // the icon-only button is drawn with its tooltip as its name
    expect(host.querySelector('button[aria-label="点赞"]')).not.toBeNull();
    await act(async () => byText("批准")!.click());
    await flush();
    expect(vi.mocked(toast).mock.calls.some(([text, tone]) => tone === "error" && String(text).includes("提交失败") && String(text).includes("回调超时"))).toBe(true);

    fail = false;
    vi.useFakeTimers();
    calls.length = 0;
    await act(async () => byText("批准")!.click());
    await flush();
    expect(vi.mocked(toast).mock.calls.some(([text, tone]) => tone === "ok" && String(text).includes("批准"))).toBe(true);
    expect(byText("批准")!.disabled).toBe(true);
    await act(async () => { vi.advanceTimersByTime(1300); });
    await flush();
    const reread = calls.find((c) => c.method === "GET" && c.url.includes("/conversations/f1/messages?"));
    expect(reread?.url).toContain("around=5");
    expect(byText("批准")!.disabled).toBe(false);
  });

  it("草稿和引用跟着会话回来，发出去后就清掉", async () => {
    route = (url, init) => {
      if (url.includes("/messages") && init.method === "POST") return ok({ message_id: "m99" });
      if (url.includes("/conversations/f1/messages")) return ok({ messages: [msg("m1", { text: "要引用的那条" })] });
      if (url.includes("/conversations/f2/messages")) return ok({ messages: [msg("m2")] });
      return undefined;
    };
    await render({ feed: "f1", title: "一" });
    await act(async () => buttons().find((b) => b.title === "回复")!.click());
    const box = () => host.querySelector<HTMLTextAreaElement>("textarea")!;
    const quoteBar = () => buttons().find((b) => b.title === "取消引用");
    expect(quoteBar()).toBeDefined();
    await act(async () => typeInto(box(), "还没写完"));
    await render({ feed: "f2", title: "二" });
    expect(box().value).toBe("");
    expect(quoteBar()).toBeUndefined();
    await render({ feed: "f1", title: "一" });
    expect(box().value).toBe("还没写完");
    expect(quoteBar()).toBeDefined();
    expect(localStorage.getItem(draftKey("f1", undefined))).not.toBeNull();

    // the emptied box's draft is saved after its debounce (LarkComposer: 300 ms): fake timers run it out at once
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await act(async () => byText("发送")!.click());
    await flush();
    const sent = calls.find((c) => c.method === "POST" && c.url.includes("/messages/m1/replies"));
    expect(sent?.body).toMatchObject({ text: "还没写完" });
    await act(async () => { await vi.advanceTimersByTimeAsync(DRAFT_SAVE_MS); });
    expect(localStorage.getItem(draftKey("f1", undefined))).toBeNull();
  });

  it("群里打 @ 时「所有人」排第一", async () => {
    route = (url, init) => {
      if (url.includes("/members")) return ok({ members: [{ user_id: "u1", name: "示例成员" }] });
      if (url.includes("/conversations/g1/messages") && init.method === "POST") return ok({ message_id: "m7" });
      if (url.includes("/conversations/g1/messages")) return ok({ messages: [msg("m1")] });
      return undefined;
    };
    await render({ feed: "g1", title: "群", group: true });
    const box = host.querySelector<HTMLTextAreaElement>("textarea")!;
    await act(async () => box.focus());
    // the members are looked up after the @ list's debounce (LarkPeoplePicker: 140 ms): fake timers run it out at once
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await act(async () => typeInto(box, "@"));
    await act(async () => { await vi.advanceTimersByTimeAsync(MEMBER_LOOKUP_MS); });
    await flush();
    const options = [...document.querySelectorAll<HTMLElement>('[role="option"]')].map((o) => o.textContent ?? "");
    expect(options.length).toBeGreaterThanOrEqual(2);
    expect(options[0]).toContain("所有人");
    expect(options[1]).toContain("示例成员");

    // the picked name goes with the message as a mention
    await act(async () => document.querySelectorAll<HTMLElement>('[role="option"]')[1].dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
    await act(async () => document.querySelectorAll<HTMLElement>('[role="option"]')[1]?.click());
    await flush();
    expect(box.value).toContain("@示例成员");
    await act(async () => byText("发送")!.click());
    await flush();
    const sent = calls.find((c) => c.method === "POST" && c.url.includes("/conversations/g1/messages"));
    expect(sent?.body).toMatchObject({ mentions: [{ user_id: "u1", name: "示例成员" }] });
  });

  it("「让 Persona 总结」在对话开好之前再点，不会再开一段（#5868）", async () => {
    let release: (r: Reply) => void = () => {};
    route = (url, init) => {
      if (url.includes("/conversations/f1/messages")) return ok({ messages: [msg("m1")] });
      if (url.includes("/assistant/conversations") && init.method === "POST") return new Promise<Reply>((done) => { release = done; });
      return undefined;
    };
    await render({ feed: "f1", title: "示例" });
    const ask = () => buttons().find((b) => b.title === "让 Persona 总结")!;
    await act(async () => ask().click());
    await flush();
    expect(ask().disabled).toBe(true);
    await act(async () => ask().click());
    await flush();
    release(ok({ session: { machine_id: "mac", session_id: "s1" } }));
    await flush();
    expect(calls.filter((c) => c.method === "POST" && c.url.includes("/assistant/conversations"))).toHaveLength(1);
    expect(ask().disabled).toBe(false);
  });
});
