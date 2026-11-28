// @vitest-environment jsdom
// Inbox policies (docs/assistant/inbox.md "Policies"): which rule applies to an item (sender before chat before repository), the
// rule dialog prefilled from the hub and saving with the chosen tier / twin, and the Rules list's Remove.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type InboxItem, type InboxPolicy } from "../../lib/inbox";
import { policyFor, policySummary } from "../../lib/inbox-page";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";

vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { PolicyDialog, RulesDialog } from "./PolicyDialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const sender = { tag: "sender", value: "u1", name: "示例甲" };
const chat = { tag: "chat", value: "c1", name: "示例群" };
const item = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: "lark:c1:1", source: "lark", kind: "group", account: "lark", account_name: "飞书", title: "示例群", preview: "", count: 1, tier: "fyi",
  mutes: [chat, sender], ...over,
});
const policy = (over: Partial<InboxPolicy>): InboxPolicy => ({ id: "p1", source: "lark", tag: "chat", value: "c1", name: "示例群", ...over });

let host: HTMLDivElement;
let root: Root;
let fetcher: ReturnType<typeof vi.fn>;
let listed: InboxPolicy[];
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const call = (method: string, url: string) => fetcher.mock.calls.find(([u, init]) => u === url && (init?.method ?? "GET") === method);
const button = (text: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === text);

beforeEach(() => {
  listed = [];
  fetcher = vi.fn().mockImplementation(async (url: string, init?: { method?: string; body?: string }) => {
    const body = url === "/api/v1/inbox/policies" && (init?.method ?? "GET") === "GET" ? { policies: listed } : { policy: policy(JSON.parse(init?.body ?? "{}")), ok: true };
    return { ok: true, status: 200, text: async () => JSON.stringify(body) };
  });
  vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  resetManagerChoice();
});

describe("收件箱规则", () => {
  it("picks the sender's rule over the chat's, in the item's source only", () => {
    const chatRule = policy({ tier: "fyi" });
    const senderRule = policy({ id: "p2", tag: "sender", value: "u1", name: "示例甲", tier: "action", twin: "first" });
    expect(policyFor(item(), [chatRule])).toBe(chatRule);
    expect(policyFor(item(), [chatRule, senderRule])).toBe(senderRule);
    expect(policyFor(item({ source: "mail" }), [chatRule, senderRule])).toBeUndefined();
    expect(policySummary(senderRule)).toBe("始终需要你 · 分身优先");
  });

  it("saves a new rule from an item with the chosen tier and twin", async () => {
    const onClose = vi.fn();
    act(() => root.render(<PolicyDialog target={{ item: item(), option: sender, source: "lark" }} onClose={onClose} />));
    await flush();
    expect(button("移除")).toBeUndefined(); // nothing to remove yet
    act(() => button("始终需要你")!.click());
    act(() => button("不交由分身")!.click());
    act(() => button("保存")!.click());
    await flush();
    const post = call("POST", "/api/v1/inbox/policies");
    expect(JSON.parse(post![1].body)).toEqual({ item_id: "lark:c1:1", tag: "sender", tier: "action", twin: "never" });
    expect(onClose).toHaveBeenCalled();
  });

  it("prefills the rule the hub has and changes it with PATCH", async () => {
    listed = [policy({ tier: "noise" })];
    act(() => root.render(<PolicyDialog target={{ item: item(), option: chat, source: "lark" }} onClose={() => {}} />));
    await flush();
    expect(button("收起")!.getAttribute("aria-checked")).toBe("true");
    act(() => button("分身优先")!.click());
    act(() => button("保存")!.click());
    await flush();
    const patch = call("PATCH", "/api/v1/inbox/policies/p1");
    expect(JSON.parse(patch![1].body)).toEqual({ tier: "noise", twin: "first" });
  });

  it("lists the rules and removes one", async () => {
    listed = [policy({ tier: "fyi", source_name: "飞书" })];
    act(() => root.render(<RulesDialog onClose={() => {}} />));
    await flush();
    expect(document.querySelector('[data-policy="p1"]')?.textContent).toContain("飞书 · 会话 · 知悉");
    act(() => button("移除")!.click());
    await flush();
    expect(call("DELETE", "/api/v1/inbox/policies/p1")).toBeTruthy();
    expect(document.querySelector('[data-policy="p1"]')).toBeNull();
  });

  it("removes a rule opened from the Rules list on the manager it was read from, even after it became the default", async () => {
    const managerOf = (method: string, url: string) => new Headers(call(method, url)?.[1]?.headers).get("X-Persona-Manager");
    setRosterDefault("assistant");
    act(() => root.render(<PolicyDialog target={{ option: chat, source: "lark", policy: policy({ tier: "fyi", manager: "m-1" }) }} onClose={() => {}} />));
    await flush();
    setRosterDefault("m-1");
    act(() => button("移除")!.click());
    await flush();
    expect(JSON.parse(call("PATCH", "/api/v1/inbox/policies/p1")![1].body)).toEqual({ tier: "", twin: "" });
    expect(managerOf("PATCH", "/api/v1/inbox/policies/p1")).toBe("m-1");
  });

  it("removes a rule opened from an item as before: the default's names none", async () => {
    const managerOf = (method: string, url: string) => new Headers(call(method, url)?.[1]?.headers).get("X-Persona-Manager");
    setRosterDefault("m-1");
    listed = [policy({ tier: "fyi" })];
    act(() => root.render(<PolicyDialog target={{ item: item({ manager: "m-1" }), option: chat, source: "lark" }} onClose={() => {}} />));
    await flush();
    expect(managerOf("GET", "/api/v1/inbox/policies")).toBeNull();
    act(() => button("移除")!.click());
    await flush();
    expect(managerOf("PATCH", "/api/v1/inbox/policies/p1")).toBeNull();
  });
});
