// The Messages page's one list (mixed-feed.ts): Feishu chats and personal Agents by their latest message, without
// moving a Feishu row, with Agents older than the loaded pages waiting for the next one.
import { describe, expect, it } from "vitest";
import type { AgentSession } from "../../lib/agents";
import type { LarkConversation } from "./lark-chats";
import { agentKey, agentPreview, arrangeFeed, interleave, messagesTarget, pageBoundary, targetKey, type FeedInput } from "./mixed-feed";

const at = (h: number) => Date.UTC(2026, 10, 20, h);
const chat = (id: string, h: number, extra: Partial<LarkConversation> = {}): LarkConversation => ({
  id, name: `Chat ${id}`, is_group: false, unread: 0, at_me: false, muted: false, preview: "", is_mine: false,
  updated_at: new Date(at(h)).toISOString(), ...extra,
});
const agent = (id: string, ms: number, extra: Partial<AgentSession> = {}): AgentSession => ({
  id, source: "example", sessionKey: id, provider: "example_bot", title: `Agent ${id}`, canSend: true, unreadCount: 0, rankTimeMs: ms, ...extra,
});
const keys = (xs: { key: string }[]) => xs.map((x) => x.key);
const input = (x: Partial<FeedInput>): FeedInput => ({
  larkPinned: [], larkRows: [], larkMore: false, agents: [], agentPins: [], mix: true, filter: "all", query: "", ...x,
});

describe("interleave", () => {
  it("puts each Agent before the first Feishu chat older than it, newest first", () => {
    const list = interleave([chat("c9", 9), chat("c7", 7), chat("c3", 3)], [agent("a8", at(8)), agent("a1", at(1)), agent("a10", at(10))]);
    expect(keys(list)).toEqual(["agent:a10", "c9", "agent:a8", "c7", "c3", "agent:a1"]);
  });

  it("never reorders Feishu's own rows, even out of time order", () => {
    // the gateway's order wins over the times it reports
    const list = interleave([chat("c5", 5), chat("c8", 8), chat("c2", 2)], [agent("a6", at(6))]);
    expect(keys(list).filter((k) => !k.startsWith("agent:"))).toEqual(["c5", "c8", "c2"]);
    expect(keys(list)).toEqual(["agent:a6", "c5", "c8", "c2"]);
  });

  it("keeps a Feishu chat ahead of an Agent at the same time, and Agents of one time in id order", () => {
    expect(keys(interleave([chat("c", 5)], [agent("b", at(5)), agent("a", at(5))]))).toEqual(["c", "agent:a", "agent:b"]);
  });
});

describe("arrangeFeed", () => {
  it("while Feishu has a next page, shows only the Agents not older than its oldest chat loaded", () => {
    const feed = arrangeFeed(input({ larkRows: [chat("c9", 9), chat("c5", 5)], larkMore: true, agents: [agent("a7", at(7)), agent("a5", at(5)), agent("a2", at(2))] }));
    expect(keys(feed.rows)).toEqual(["c9", "agent:a7", "c5", "agent:a5"]);
    expect(pageBoundary([chat("c9", 9), chat("c5", 5)])).toBe(at(5));
    expect(feed.pagesLarkMore).toBe(true);
  });

  it("shows every Agent once Feishu has no next page, those without a time last", () => {
    const feed = arrangeFeed(input({ larkRows: [chat("c9", 9), chat("c5", 5)], agents: [agent("a0", 0), agent("a2", at(2)), agent("neg", -1)] }));
    expect(keys(feed.rows)).toEqual(["c9", "c5", "agent:a2", "agent:a0", "agent:neg"]);
  });

  it("never puts an Agent without a time on top, nor any Agent while no Feishu page has come", () => {
    const paged = arrangeFeed(input({ larkRows: [chat("c1", 1)], larkMore: true, agents: [agent("a0", 0)] }));
    expect(keys(paged.rows)).toEqual(["c1"]);
    const waiting = arrangeFeed(input({ larkRows: [], larkMore: true, agents: [agent("a9", at(9))] }));
    expect(keys(waiting.rows)).toEqual([]);
  });

  it("puts the pinned Agents on the shelf with the pinned chats, and out of the list", () => {
    const feed = arrangeFeed(input({
      larkPinned: [chat("p8", 8)], larkRows: [chat("c3", 3)], agents: [agent("a9", at(9)), agent("a1", at(1)), agent("a4", at(4))], agentPins: ["a1", "a9", "gone"],
    }));
    expect(keys(feed.pinned)).toEqual(["agent:a9", "p8", "agent:a1"]);
    expect(keys(feed.rows)).toEqual(["agent:a4", "c3"]);
  });

  it("filters to Feishu or to the Agents, and lists no Agent with the mix switch off", () => {
    const base = input({ larkPinned: [chat("p", 8)], larkRows: [chat("c9", 9), chat("c5", 5)], larkMore: true, agents: [agent("a7", at(7)), agent("a1", at(1))], agentPins: ["a1"] });
    const lark = arrangeFeed({ ...base, filter: "lark" });
    expect(keys(lark.pinned)).toEqual(["p"]);
    expect(keys(lark.rows)).toEqual(["c9", "c5"]);
    const agents = arrangeFeed({ ...base, filter: "agents" });
    expect(keys(agents.pinned)).toEqual(["agent:a1"]);
    expect(keys(agents.rows)).toEqual(["agent:a7"]); // no page boundary: Feishu is not listed
    expect(agents.pagesLarkMore).toBe(false);
    const off = arrangeFeed({ ...base, mix: false, filter: "agents" });
    expect(keys(off.pinned)).toEqual(["p"]);
    expect(keys(off.rows)).toEqual(["c9", "c5"]);
  });

  it("searches both, pinned ones too, past the page boundary", () => {
    const feed = arrangeFeed(input({
      larkPinned: [chat("p", 8, { name: "Weekly sync" })], larkRows: [chat("c9", 9, { name: "Lunch" }), chat("c5", 5, { preview: "sync notes" })], larkMore: true,
      agents: [agent("a1", at(1), { title: "Helper", previewText: "Sync done" }), agent("a2", at(2), { title: "Other" })], agentPins: [], query: "SYNC",
    }));
    expect(feed.pinned).toEqual([]);
    expect(keys(feed.rows)).toEqual(["p", "c5", "agent:a1"]);
    expect(feed.pagesLarkMore).toBe(false);
  });
});

describe("the page's address", () => {
  it("reads an Agent from two segments and a Feishu chat from one", () => {
    expect(messagesTarget(["agent", "example:room/1"])).toEqual({ kind: "agent", id: "example:room/1" });
    expect(messagesTarget(["agent"])).toEqual({ kind: "lark", id: "agent" }); // a Feishu id that happens to read "agent"
    expect(messagesTarget(["oc_1"])).toEqual({ kind: "lark", id: "oc_1" });
    expect(messagesTarget([])).toBeUndefined();
    expect(messagesTarget(["a", "b", "c"])).toBeUndefined();
    expect(targetKey({ kind: "agent", id: "x" })).toBe(agentKey("x"));
    expect(targetKey({ kind: "lark", id: "oc_1" })).toBe("oc_1");
  });
});

describe("agentPreview", () => {
  it("is the latest message on one line, marked when it is the user's", () => {
    const mine = (text: string) => `Me: ${text}`;
    expect(agentPreview({ previewText: " hello\n world " }, mine)).toBe("hello world");
    expect(agentPreview({ previewText: "done", previewIsMine: true }, mine)).toBe("Me: done");
    expect(agentPreview({}, mine)).toBe("");
  });
});
