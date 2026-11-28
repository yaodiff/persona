// @vitest-environment jsdom
// A conversation the Lark connector can no longer open (a disbanded group, access removed) gets an explicit empty
// state instead of a blank pane or a raw gateway error; transient gateway failures keep the retryable error text.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Listener = (msg: Record<string, unknown>) => void;
const { realListeners } = vi.hoisted(() => ({ realListeners: new Map<string, Set<(msg: Record<string, unknown>) => void>>() }));
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return {
    ...real,
    onHubEvent: (type: string, l: Listener) => {
      const set = realListeners.get(type) ?? new Set<Listener>();
      set.add(l);
      realListeners.set(type, set);
      return () => void set.delete(l);
    },
  };
});

import { ApiError } from "../../lib/api";
import { conversationUnavailable, LarkThread } from "./LarkThread";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let failure: { status: number; body: Record<string, unknown> } | null;

class FakeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return [];
  }
}

beforeEach(() => {
  failure = null;
  vi.stubGlobal("IntersectionObserver", FakeObserver);
  Element.prototype.scrollIntoView = vi.fn();
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const problem = failure;
    if (url.includes("/conversations/f1/messages?")) {
      if (problem) return { ok: false, status: problem.status, text: async () => JSON.stringify(problem.body), headers: new Headers() };
      return { ok: true, status: 200, text: async () => JSON.stringify({ messages: [] }), headers: new Headers() };
    }
    return { ok: false, status: 404, text: async () => JSON.stringify({ error: "not found" }), headers: new Headers() };
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

const flush = async (n = 8) => {
  for (let i = 0; i < n; i++) await act(async () => { await Promise.resolve(); });
};

const open = async () => {
  await act(async () => root.render(<LarkThread chat={{ feed: "f1", title: "Old group", group: true }} />));
  await flush();
};

describe("a conversation that can no longer be opened", () => {
  it.each([
    [404, { code: "CONVERSATION_NOT_FOUND", error: "Conversation not found" }],
    // the hub may wrap an extension refusal in 502 while keeping its business code
    [502, { code: "CONVERSATION_NOT_FOUND", error: "Conversation not found" }],
    [403, { code: "CONVERSATION_NOT_MEMBER", error: "Not a member" }],
    [404, { error: "Gone" }],
    [410, {}],
  ])("shows the unavailable state for status %s with body %j", async (status, body) => {
    failure = { status, body };
    await open();
    expect(host.textContent).toContain("该会话暂不可用");
    expect(host.textContent).not.toContain("正在加载飞书消息");
  });

  it("keeps the ordinary error text for a transient gateway failure", async () => {
    failure = { status: 502, body: { code: "UPSTREAM_UNAVAILABLE", error: "upstream unavailable" } };
    await open();
    expect(host.textContent).not.toContain("该会话暂不可用");
    expect(host.querySelector(".text-red")).toBeTruthy();
  });

  it("does not treat the hub's generic route 404 as a gone conversation", async () => {
    failure = { status: 404, body: { code: "not_found", error: "Not Found" } };
    await open();
    expect(host.textContent).not.toContain("该会话暂不可用");
    expect(host.querySelector(".text-red")).toBeTruthy();
  });
});

describe("conversationUnavailable", () => {
  it("only classifies terminal conversation refusals", () => {
    expect(conversationUnavailable(new ApiError(404, "x", { code: "conversation_not_found" }))).toBe(true);
    expect(conversationUnavailable(new ApiError(403, "x", { code: "CONVERSATION_NOT_MEMBER" }))).toBe(true);
    expect(conversationUnavailable(new ApiError(410, "x", {}))).toBe(true);
    expect(conversationUnavailable(new ApiError(503, "x", { code: "CONVERSATION_ACCESS_UNVERIFIED" }))).toBe(false);
    expect(conversationUnavailable(new ApiError(502, "x", {}))).toBe(false);
    expect(conversationUnavailable(new TypeError("fetch failed"))).toBe(false);
  });
});
