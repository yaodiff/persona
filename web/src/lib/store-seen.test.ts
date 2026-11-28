// @vitest-environment jsdom
// Viewing an agent's chat on the web marks it seen, here and on the hub, and keeps it seen (#6493: the green unread dot
// came back after the chat was read). The old page only sent a WebSocket `seen` and changed nothing itself: a send while
// the socket was still connecting (opening a chat link, a reload) or over a socket that was gone went nowhere, and the
// next list from the hub put the dot back.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { connect, getSession, getState, markSeen, reset, upsertSession, viewSession as openView } from "./store";
import { runtimeIdentity, runtimeSessions } from "./runtime-list";
import { heldCounts, heldSeen, SEEN_CONFIRM_MS, SEEN_SETTLE_MS } from "./seen-outbox";
import { activityOf } from "./format";
import type { Session } from "./types";

vi.mock("./api", async (original) => ({ ...await original<typeof import("./api")>(), api: vi.fn() }));

class Socket {
  static OPEN = 1;
  static all: Socket[] = [];
  readyState = 0;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  constructor() { Socket.all.push(this); }
  send(s: string) { this.sent.push(s); }
  close() { this.readyState = 3; }
  open() { this.readyState = 1; this.onopen?.(); }
  push(msg: Record<string, unknown>) { this.onmessage?.({ data: JSON.stringify(msg) }); }
  seen() { return this.sent.map((s) => JSON.parse(s)).filter((m) => m.type === "seen"); }
}
const sock = () => Socket.all[Socket.all.length - 1];

const T0 = "2026-01-01T00:00:00Z";
const unread = (patch: Partial<Session> = {}): Session => ({
  machine_id: "mac", session_id: "s1", title: "Fix the tests", cwd: "/code", status: "done", state: "unread",
  updated_at: "2025-12-31T23:59:00Z", seen_at: "0001-01-01T00:00:00Z", ...patch,
});
const seenPosts = () => vi.mocked(api).mock.calls.filter(([path]) => String(path).endsWith("/seen"));
let visibility: DocumentVisibilityState = "visible";
Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
const toFront = () => { visibility = "visible"; document.dispatchEvent(new Event("visibilitychange")); };
// every view a test opens is closed after it: the registry of chats on screen lives as long as the module
const opened: Array<() => void> = [];
const viewSession = (mid: string, sid: string) => {
  const release = openView(mid, sid);
  let done = false;
  const once = () => { if (!done) { done = true; release(); } };
  opened.push(once);
  return once;
};

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(T0));
  Socket.all = [];
  visibility = "visible";
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("location", { protocol: "https:", host: "hub.example.com" });
  vi.stubGlobal("localStorage", { getItem: () => "tok", setItem: () => {}, removeItem: () => {} });
  reset();
  vi.mocked(api).mockReset().mockResolvedValue({});
});
afterEach(() => {
  opened.splice(0).forEach((release) => release());
  reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

/** A connected page with the hub's hello, the machine's list pushed (a hub with sessions_delta). */
function online(rows: Session[] = [unread()]) {
  connect();
  sock().open();
  sock().push({ type: "hello", features: ["sessions_delta"], event_stream: "e", event_seq: 1 });
  sock().push({ type: "sessions", machine_id: "mac", sessions: rows });
  return sock();
}
const shown = () => activityOf(getSession("mac", "s1")!);
const ID = runtimeIdentity(unread());

describe("viewing a chat marks it seen", () => {
  it("shows it seen at once and tells the hub", () => {
    const s = online();
    const release = viewSession("mac", "s1");
    expect(shown()).toBe("idle");
    expect(s.seen()).toEqual([{ type: "seen", machine_id: "mac", session_id: "s1" }]);
    release();
  });

  it("stays seen when the hub's next list still says unread (the bug), and sends it again over HTTP", () => {
    const s = online();
    viewSession("mac", "s1");
    // another machine's push, or a list the hub made before it read the seen: the agent is still unread there
    s.push({ type: "sessions", machine_id: "mac", sessions: [unread({ title: "Fix the tests (renamed)" })] });
    expect(shown()).toBe("idle");
    // nothing from the hub says it took the socket's seen: it goes again over HTTP, which answers
    vi.advanceTimersByTime(SEEN_CONFIRM_MS + 10);
    expect(seenPosts().map(([path]) => path)).toEqual(["/sessions/mac/s1/seen"]);
  });

  it("is sent once the socket is back when the chat opened while it was still connecting", async () => {
    connect(); // a reload or a chat link: the socket is not open yet
    upsertSession(unread());
    viewSession("mac", "s1");
    expect(shown()).toBe("idle");
    // the socket could not take it: HTTP did
    expect(seenPosts().map(([path]) => path)).toEqual(["/sessions/mac/s1/seen"]);
    vi.mocked(api).mockClear();
    sock().open();
    sock().push({ type: "hello", features: ["sessions_delta"], event_stream: "e", event_seq: 1 });
    // the HTTP answer had not come when the socket came back: sent again there too
    await Promise.resolve();
    expect(sock().seen().length + seenPosts().length).toBeGreaterThan(0);
  });

  it("lets go once the hub records it, and a new turn after that is unread again", () => {
    const s = online();
    viewSession("mac", "s1")();
    s.push({ type: "sessions", machine_id: "mac", sessions: [unread({ state: "idle", seen_at: "2026-01-01T00:00:01Z" })] });
    // recorded: nothing goes again, over the socket or HTTP
    expect(getState().seen[ID].recordedAt).toBeDefined();
    s.sent = [];
    vi.advanceTimersByTime(SEEN_CONFIRM_MS + 10);
    expect(seenPosts()).toEqual([]);
    expect(s.seen()).toEqual([]);
    s.push({ type: "sessions", machine_id: "mac", sessions: [unread({ updated_at: "2026-01-01T00:05:00Z", seen_at: "2026-01-01T00:00:01Z" })] });
    expect(getState().seen).toEqual({});
    expect(shown()).toBe("unread");
  });

  it("keeps the sidebar's own list seen until it reads again, once another list showed it recorded", () => {
    // A paging hub: the sidebar draws its list's own rows, read on that list's hints (at most once in two seconds),
    // while the chat's own read and the store's list read on theirs. The first of them to show the seen recorded let
    // the mark go, and the sidebar's row, still the one read before the look, went green again until its list read.
    const s = online();
    viewSession("mac", "s1");
    const before = { ...unread(), waiting: false };
    s.push({ type: "sessions", machine_id: "mac", sessions: [unread({ state: "idle", seen_at: "2026-01-01T00:00:01Z" })] });
    vi.advanceTimersByTime(1_500);
    expect(activityOf(heldSeen(before, getState().seen[ID], Date.now()))).toBe("idle");
    // the hub's counts read with that row still count it Unread: shown without it
    const counts = { total: 3, revision: "r", state: { unread: 2, idle: 1 }, machine: {}, project: {}, group: {} };
    expect(heldCounts(counts, [before], getState().seen, Date.now()).state).toEqual({ unread: 1, idle: 2 });
    // long after every list has read again, the mark is gone
    vi.advanceTimersByTime(SEEN_SETTLE_MS);
    s.push({ type: "sessions", machine_id: "mac", sessions: [unread({ state: "idle", seen_at: "2026-01-01T00:00:01Z" })] });
    expect(getState().seen).toEqual({});
  });

  it("does not mark from a background tab, and does when it comes to the front", () => {
    const s = online();
    visibility = "hidden";
    viewSession("mac", "s1");
    expect(shown()).toBe("unread");
    expect(s.seen()).toEqual([]);
    toFront();
    expect(shown()).toBe("idle");
    expect(s.seen().length).toBe(1);
  });

  it("keeps a turn that ends while the chat is on screen from lighting up", () => {
    const s = online([unread({ state: "working", status: "working" })]);
    const release = viewSession("mac", "s1");
    s.sent = [];
    // the turn ends: the push that says unread is shown seen in the same update
    s.push({ type: "sessions_delta", machine_id: "mac", upsert: [unread({ updated_at: "2026-01-01T00:01:00Z" })], remove: [] });
    expect(shown()).toBe("idle");
    expect(s.seen()).toEqual([{ type: "seen", machine_id: "mac", session_id: "s1" }]);
    // once the chat is closed, the next finished turn is unread as before
    release();
    s.push({ type: "sessions_delta", machine_id: "mac", upsert: [unread({ state: "working", status: "working", updated_at: "2026-01-01T00:02:00Z" })], remove: [] });
    s.push({ type: "sessions_delta", machine_id: "mac", upsert: [unread({ updated_at: "2026-01-01T00:03:00Z" })], remove: [] });
    expect(shown()).toBe("unread");
  });

  it("does not mark a background tab's chat whose turn ends", () => {
    const s = online([unread({ state: "working", status: "working" })]);
    viewSession("mac", "s1");
    visibility = "hidden";
    s.sent = [];
    s.push({ type: "sessions_delta", machine_id: "mac", upsert: [unread({ updated_at: "2026-01-01T00:01:00Z" })], remove: [] });
    expect(shown()).toBe("unread");
    expect(s.seen()).toEqual([]);
  });

  it("holds the paged list's summary rows seen too", () => {
    const s = online();
    // a paging hub: the sidebar draws the list's own rows (state.sessionSummaries), which the list refetches
    const listed = { ...unread(), waiting: false };
    // the list's first page lands with the row still unread after the look
    viewSession("mac", "s1");
    (runtimeSessions as unknown as { publish: (p: object) => void }).publish({ rows: [listed], loaded: true });
    expect(activityOf(getState().sessionSummaries[0])).toBe("idle");
    expect(s.seen().length).toBe(1);
  });
});

describe("mark all as seen", () => {
  it("holds them seen against a stale list from the hub", () => {
    const s = online([unread(), unread({ session_id: "s2" })]);
    markSeen(getState().sessions.mac);
    s.push({ type: "sessions", machine_id: "mac", sessions: [unread(), unread({ session_id: "s2" })] });
    expect(getState().sessions.mac.map(activityOf)).toEqual(["idle", "idle"]);
  });
});
