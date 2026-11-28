// A card this page ended stays ended (#1541): until the node ends it, the hub still lists it as pending, both in the
// snapshot after a hello that resumes the stream (as `interaction` frames) and in a full list read after the answer.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const op = vi.hoisted(() => vi.fn());
vi.mock("./ops", async (orig) => ({ ...(await orig<typeof import("./ops")>()), apiOp: (...args: unknown[]) => op(...args) }));
import { ApiError } from "./api";
import { answerCard } from "./card-answer";
import { connect, flushPushes, getState, mergeInteractions, reset } from "./store";
import type { Interaction } from "./types";

class FakeSocket {
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  constructor() {
    FakeSocket.all.push(this);
  }
  send() {}
  close() {
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  push(msg: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify(msg) });
    flushPushes();
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  FakeSocket.all = [];
  op.mockReset();
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("location", { protocol: "http:", host: "hub" });
  vi.stubGlobal("localStorage", { getItem: () => "tok", setItem: () => {}, removeItem: () => {} });
  vi.stubGlobal("window", { setTimeout: (f: () => void, ms: number) => setTimeout(f, ms), clearTimeout: (t: number) => clearTimeout(t) });
  vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response("{}", { status: 200 }))));
});

afterEach(() => {
  reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const card: Interaction = { id: "ix1", machine_id: "m1", session_id: "s1", kind: "permission", tool_name: "Bash", created_at: "2026-01-02T03:04:05.000000001Z" };
const other: Interaction = { ...card, id: "ix2" };

function online() {
  connect();
  const s = FakeSocket.all[FakeSocket.all.length - 1];
  s.open();
  s.push({ type: "hello", server_version: "v1", event_stream: "ev1", event_seq: 1 });
  s.push({ type: "sessions", machine_id: "m1", sessions: [{ machine_id: "m1", session_id: "s1" }] });
  s.push({ type: "interaction", interaction: card });
  return s;
}

describe("a card this page ended", () => {
  it("is not put back by a resumed stream's snapshot, while other cards are", async () => {
    let s = online();
    op.mockResolvedValue({ ok: true });
    expect((await answerCard(card, { allow: true })).ok).toBe(true);
    expect(getState().interactions).toEqual([]);

    s.close();
    connect();
    s = FakeSocket.all[FakeSocket.all.length - 1];
    s.open();
    s.push({ type: "hello", server_version: "v1", event_stream: "ev1", event_seq: 1, resumed: true });
    s.push({ type: "interaction", interaction: card });
    s.push({ type: "interaction", interaction: other });
    expect(getState().interactions.map((i) => i.id)).toEqual(["ix2"]);
    expect(getState().answered.ix1).toBeDefined();
  });

  it("is left out of a pending list read after the answer", async () => {
    online();
    op.mockResolvedValue({ ok: true });
    await answerCard(card, { allow: true });
    expect(mergeInteractions([card, other], Number.MAX_SAFE_INTEGER).map((i) => i.id)).toEqual(["ix2"]);
  });

  it("includes one found over or handed back", async () => {
    online();
    op.mockRejectedValueOnce(new ApiError(410, "over", { status: "expired" }));
    await answerCard(card, { allow: true });
    op.mockResolvedValueOnce({ ok: true });
    await answerCard(other, { dismiss: true });
    expect(mergeInteractions([card, other], Number.MAX_SAFE_INTEGER)).toEqual([]);
  });

  it("does not include a card whose answer failed: it stays answerable", async () => {
    const s = online();
    op.mockRejectedValueOnce(new ApiError(502, "unreachable", {}));
    expect((await answerCard(card, { allow: true })).ok).toBe(false);
    s.push({ type: "interaction", interaction: card });
    expect(getState().interactions.map((i) => i.id)).toEqual(["ix1"]);
    expect(mergeInteractions([card], Number.MAX_SAFE_INTEGER).map((i) => i.id)).toEqual(["ix1"]);
  });

  it("keeps a same-ID replacement that arrives before the earlier answer response", async () => {
    const s = online();
    let finish!: (value: Record<string, unknown>) => void;
    op.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    const answering = answerCard(card, { allow: true });
    const replacement: Interaction = { ...card, created_at: "2026-01-02T03:04:05.000000002Z", summary: "replacement" };
    s.push({ type: "interaction", interaction: replacement });
    expect(getState().interactions).toEqual([replacement]);
    finish({ ok: true });
    expect((await answering).ok).toBe(true);
    expect(getState().interactions).toEqual([replacement]);
    expect(mergeInteractions([replacement], Number.MAX_SAFE_INTEGER)).toEqual([replacement]);
  });

  it("keeps a newer answered receipt when the earlier occurrence's response arrives late", async () => {
    const s = online();
    let finish!: (value: Record<string, unknown>) => void;
    op.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    const answering = answerCard(card, { allow: true });
    const replacement: Interaction = { ...card, created_at: "2026-01-02T03:04:05.000000002Z", summary: "replacement" };
    s.push({ type: "interaction", interaction: replacement });
    op.mockResolvedValueOnce({ ok: true });
    expect((await answerCard(replacement, { allow: true })).ok).toBe(true);
    expect(getState().answered.ix1.ix).toEqual(replacement);
    finish({ ok: true });
    expect((await answering).ok).toBe(true);
    expect(getState().answered.ix1.ix).toEqual(replacement);
  });

  it("keeps a replacement when the earlier answer was already delegated", async () => {
    const s = online();
    const first: Interaction = { ...card, kind: "question" };
    s.push({ type: "interaction", interaction: first });
    let finish!: (value: Record<string, unknown>) => void;
    op.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    const answering = answerCard(first, { answers: { "Which?": "Mine" } });
    const replacement: Interaction = { ...first, created_at: "2026-01-02T03:04:05.000000002Z", summary: "replacement" };
    s.push({ type: "interaction", interaction: replacement });
    finish({ already: true, status: "resolved", resolution: { answered_by: "assistant", answers: { "Which?": "Assistant" } } });
    expect((await answering).ok).toBe(true);
    expect(getState().interactions).toEqual([replacement]);
  });

  it("keeps a replacement when the earlier occurrence resolves late", () => {
    const s = online();
    const replacement: Interaction = { ...card, created_at: "2026-01-02T03:04:05.000000002Z", summary: "replacement" };
    s.push({ type: "interaction", interaction: replacement });
    s.push({ type: "interaction_resolved", id: card.id, created_at: card.created_at });
    expect(getState().interactions).toEqual([replacement]);
  });

  it("does not let a delayed older live event replace a newer pending occurrence", () => {
    const s = online();
    const replacement: Interaction = { ...card, created_at: "2026-01-02T03:04:05.000000002Z", summary: "replacement" };
    s.push({ type: "interaction", interaction: replacement });
    s.push({ type: "interaction", interaction: card });
    expect(getState().interactions.filter((i) => i.id === card.id)).toEqual([replacement]);
    expect(mergeInteractions([card], Number.MAX_SAFE_INTEGER)).toEqual([replacement]);
  });

  it("does not resurrect an older occurrence after its replacement resolved elsewhere", () => {
    const s = online();
    const replacement: Interaction = { ...card, created_at: "2026-01-02T03:04:05.000000002Z", summary: "replacement" };
    s.push({ type: "interaction", interaction: replacement });
    s.push({ type: "interaction_resolved", id: replacement.id, created_at: replacement.created_at });
    s.push({ type: "interaction", interaction: card });
    expect(getState().interactions.filter((i) => i.id === card.id)).toEqual([]);
    expect(mergeInteractions([card], Number.MAX_SAFE_INTEGER)).toEqual([]);
  });

  it("is forgotten with a new login", async () => {
    online();
    op.mockResolvedValue({ ok: true });
    await answerCard(card, { allow: true });
    reset();
    expect(mergeInteractions([card], Number.MAX_SAFE_INTEGER).map((i) => i.id)).toEqual(["ix1"]);
  });
});
