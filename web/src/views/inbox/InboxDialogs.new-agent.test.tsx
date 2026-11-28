// @vitest-environment jsdom
// "Hand to AI" › "New agent…" (#5933; docs/assistant/inbox.md "Item actions"): New agent opens over the dialog with the
// item as the card over the first message (the <persona-context> an agent target gets) and the ask, or the first preset;
// nothing is sent before its Start, and Cancel leaves the dialog as it was. Once the agent has its first message, the
// item's source remembers that agent (never "New agent…") and the item is filed only when the user asked for that.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../lib/router", () => ({ navigate: vi.fn() }));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
vi.mock("../../lib/new-agent", async (orig) => ({ ...(await orig<typeof import("../../lib/new-agent")>()), openNewAgent: vi.fn() }));

import { reloadInbox, resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { HANDOFF_PRESETS, handOffContext, handOffMessage } from "../../lib/inbox-page";
import { handOffSourceKey, recallHandOff } from "../../lib/handoff-memory";
import { contextXML } from "../../lib/context-xml";
import { openNewAgent, type NewAgentPreset } from "../../lib/new-agent";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { HandOffDialog } from "./InboxDialogs";
import type { Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item: InboxItem = {
  id: "lark:f1:1", manager: "assistant", source: "lark", kind: "dm", account: "feishu", account_name: "Lark", title: "Example", preview: "text", count: 1,
  updated_at: "2026-11-01T09:00:00Z", tier: "action", lark: { feed_id: "f1", is_group: false },
  mutes: [{ tag: "chat", value: "oc_example", name: "Example" }],
};
// GET /inbox/{id} adds the conversation's text
const full: InboxItem = { ...item, context: "Example: is it merged?\nMe: not yet" };

let host: HTMLDivElement;
let root: Root;
type Call = { url: string; method: string };
const calls: Call[] = [];

beforeEach(() => {
  calls.length = 0;
  localStorage.clear();
  resetManagerChoice();
  resetInboxForTests();
  setRosterDefault("assistant");
  vi.mocked(openNewAgent).mockClear();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, method: init?.method ?? "GET" });
    const body = url.includes("/inbox?") ? { items: [item] } : url.includes("/inbox/") ? full : {};
    return { ok: true, status: 200, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetManagerChoice();
  resetInboxForTests();
  vi.unstubAllGlobals();
});

const row = () => document.body.querySelector('[data-testid="handoff-new-agent"]') as HTMLButtonElement;
const ask = (text: string) => {
  const box = document.body.querySelector("textarea") as HTMLTextAreaElement;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, text);
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const open = async (): Promise<NewAgentPreset> => {
  await act(async () => row().click());
  await vi.waitFor(() => expect(openNewAgent).toHaveBeenCalledTimes(1));
  return vi.mocked(openNewAgent).mock.calls[0][0]!;
};
const started = { machine_id: "laptop", session_id: "s-9" } as Session;
const filed = (c: Call) => c.method === "POST" && c.url === "/api/v1/inbox/lark:f1:1/done";
const sent = (c: Call) => c.method === "POST" && (c.url.endsWith("/handoff") || c.url.includes("/messages") || c.url.endsWith("/assistant/conversations"));

describe("Hand to AI: New agent…", () => {
  it("heads the agents, even with none running, and leaves the main conversation picked", () => {
    act(() => root.render(<HandOffDialog item={item} onClose={() => {}} />));
    const list = document.body.querySelector('[role="radiogroup"]')!;
    const rows = [...list.children];
    expect(rows[2]).toBe(row());
    expect(row().textContent).toContain("新建 Agent…");
    expect(rows[0].getAttribute("aria-checked")).toBe("true");
  });

  it("opens New agent with the item as an agent gets it and the ask, sending nothing, the dialog still open", async () => {
    const onClose = vi.fn();
    act(() => root.render(<HandOffDialog item={item} onClose={onClose} />));
    ask("Look into the background");
    const preset = await open();
    expect(preset.forwards).toEqual([handOffContext(full)]);
    expect(preset.prompt).toBe("Look into the background");
    // New agent sends the card's block, then the ask: the text an agent target gets from here.
    const ref = preset.forwards![0];
    expect(`${contextXML(ref)}\n\n${preset.prompt}`).toBe(handOffMessage(full, "Look into the background"));
    expect(calls.some(sent)).toBe(false);
    expect(calls.some(filed)).toBe(false);
    expect(onClose).not.toHaveBeenCalled();
    expect(row()).not.toBeNull();
  });

  it("starts from the first preset when nothing was typed", async () => {
    act(() => root.render(<HandOffDialog item={item} onClose={() => {}} />));
    expect((await open()).prompt).toBe(HANDOFF_PRESETS[0]);
  });

  it("remembers the started agent for the source and leaves the item open by default", async () => {
    await reloadInbox();
    const onClose = vi.fn();
    act(() => root.render(<HandOffDialog item={item} onClose={onClose} />));
    const done = document.body.querySelector('input[type="checkbox"]') as HTMLInputElement;
    expect(done.checked).toBe(false);
    const preset = await open();
    act(() => preset.onStarted!(started));
    expect(recallHandOff(handOffSourceKey(item.source, item.mutes)!)).toBe("agent:laptop/s-9");
    await new Promise((r) => setTimeout(r, 20));
    expect(calls.some(filed)).toBe(false);
    expect(onClose).toHaveBeenCalled();
  });

  it("files the item once the agent starts when the user asks", async () => {
    await reloadInbox();
    act(() => root.render(<HandOffDialog item={item} onClose={() => {}} />));
    const done = document.body.querySelector('input[type="checkbox"]') as HTMLInputElement;
    act(() => done.click());
    const preset = await open();
    act(() => preset.onStarted!(started));
    expect(recallHandOff(handOffSourceKey(item.source, item.mutes)!)).toBe("agent:laptop/s-9");
    await vi.waitFor(() => expect(calls.some(filed)).toBe(true));
  });
});
