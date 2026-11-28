// @vitest-environment jsdom
// The main chat after its node connection stalled (#16214): the manager held everything the node sent for 15 s at the
// end of a turn, then let it through at once. The reply's words had streamed before the stall; its end marker, its item
// and the answer to a catch-up read sent during the stall then arrive together, in any order. Every order ends with
// the reply in the chat once and no preview left, so the turn never stays without its answer.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemsEvent, listenSession } from "../../lib/store";
import type { Item, ItemsPage } from "../../lib/types";
import type { Transcript } from "./useTranscript";

type Listener = Parameters<typeof listenSession>[2];
let listener: Listener | undefined;

const at = "2026-11-26T06:15:49Z";
const prompt: Item = { id: "u1#1", seq: 1, role: "user", kind: "text", text: "OK", ts: at } as Item;
const send: Item[] = [
  { id: "t4#0", seq: 4, role: "assistant", kind: "tool_use", tool_use_id: "toolu_send", tool_name: "mcp__persona__conversation_send", ts: "2026-11-26T06:15:58Z" } as Item,
  { id: "r6#0", seq: 6, role: "user", kind: "tool_result", tool_use_id: "toolu_send", text: "sent", ts: "2026-11-26T06:15:59Z" } as Item,
];
const words = "Handed it to the direction; it will open a to-do and report back.";
const reply: Item = { id: "a9#0", seq: 9, role: "assistant", kind: "text", text: words, ts: "2026-11-26T06:16:04Z" } as Item;

// a catch-up read (items?after_seq=) the page sent while the node's answers were held, answered when they were let go
let heldRead: ((page: ItemsPage) => void) | undefined;
const reads: string[] = [];
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: vi.fn(async (path: string): Promise<ItemsPage> => {
    reads.push(path);
    if (path.includes("after_seq=")) return new Promise<ItemsPage>((resolve) => { heldRead = resolve; });
    return { items: [prompt], to_seq: 1, has_more: true } as ItemsPage;
  }),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  send: vi.fn(),
  listenSession: (_mid: string, _sid: string, l: Listener) => {
    listener = l;
    return () => (listener = undefined);
  },
}));

const { useTranscript } = await import("./useTranscript");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let tr: Transcript;
let session = 0; // a session of its own per test: the pull gate is shared by a tab's views of one session
function Probe() {
  tr = useTranscript("hub", `c0ffee00-0000-4000-8000-${String(session).padStart(12, "0")}`, { assistant: true });
  return null;
}

let root: Root;
beforeEach(async () => {
  vi.useFakeTimers();
  session++;
  heldRead = undefined;
  reads.length = 0;
  root = createRoot(document.createElement("div"));
  act(() => root.render(<Probe />));
  await act(async () => {}); // the first page
});
afterEach(() => {
  act(() => root.unmount());
  vi.useRealTimers();
});

const settle = () => act(async () => void vi.advanceTimersByTime(500)); // past the delta throttle and item coalescing
const delta = async (text: string) => {
  act(() => listener!.delta!(text));
  await settle();
};
const push = async (list: Item[], from: number, to: number) => {
  act(() => listener!.items!({ items: list, from_seq: from, to_seq: to } as ItemsEvent));
  await settle();
};
const answerHeldRead = async (page: ItemsPage = { items: [reply], to_seq: 11 } as ItemsPage) => {
  expect(heldRead, "a catch-up read was sent during the stall").toBeDefined();
  await act(async () => heldRead!(page));
  await settle();
};
// the node answered the read before it had read the reply's line: nothing new, up to the line before it
const staleRead = { items: [], to_seq: 8 } as unknown as ItemsPage;

// Before the stall: the turn's work and the reply's words as they streamed; then a push past what the page holds makes
// it ask for what came after (the gap fill), and that read is held with everything else.
async function beforeTheStall() {
  await push(send, 1, 7);
  await delta(words);
  expect(tr.streaming).toBe(words);
  await push([], 8, 8); // a batch that does not continue from what the page holds: a gap
  await act(async () => void vi.advanceTimersByTime(2_000)); // the gap fill goes out, and is held
  expect(reads.some((p) => p.includes("after_seq="))).toBe(true);
}

function expectReplyOnce() {
  expect(tr.items.filter((i) => i.id === reply.id)).toHaveLength(1);
  expect(tr.items.at(-1)?.text).toBe(words);
  expect(tr.streaming).toBe("");
}

describe("the main chat when a stalled node connection lets everything through at once (#16214)", () => {
  it("end marker, then the reply's item, then the held read", async () => {
    await beforeTheStall();
    await delta("");
    await push([reply], 8, 11);
    await answerHeldRead();
    expectReplyOnce();
  });

  it("the held read first, then the end marker and the reply's item", async () => {
    await beforeTheStall();
    await answerHeldRead();
    await delta("");
    await push([reply], 8, 11);
    expectReplyOnce();
  });

  it("the reply's item, then a late end marker and the held read", async () => {
    await beforeTheStall();
    await push([reply], 8, 11);
    await delta("");
    await answerHeldRead();
    expectReplyOnce();
  });

  it("a held read answered before the reply's line was read, landing after the reply's item", async () => {
    await beforeTheStall();
    await delta("");
    await push([reply], 8, 11);
    await answerHeldRead(staleRead);
    expectReplyOnce();
  });

  it("a held read answered before the reply's line was read, landing before the reply's item", async () => {
    await beforeTheStall();
    await answerHeldRead(staleRead);
    await delta("");
    await push([reply], 8, 11);
    expectReplyOnce();
  });

  it("only the held read brings the reply: the push was lost", async () => {
    await beforeTheStall();
    await delta("");
    await answerHeldRead();
    expectReplyOnce();
  });
});
