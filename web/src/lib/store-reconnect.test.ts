// The socket's side of #1574: a drop is invisible inside the grace, a restart retries at once, and a resumed hello
// refetches nothing while an ordinary one refreshes everything as before.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { appSocketReady, applyMachine, checkSocket, connect, disconnect, getState, onAppSocket, onPrivateUIStream, onHubEvent, reconnectNow, refresh, reset } from "./store";
import { offlineNow } from "./feedback";
import { liveEntry, resetLive } from "./live";
import { messageFeedback } from "./message-send";

class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  url: string;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  sent: string[] = [];
  constructor(url: string) {
    this.url = url;
    FakeSocket.all.push(this);
  }
  send(s: string) {
    this.sent.push(s);
  }
  close() {
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  push(msg: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify(msg) });
  }
  drop(code = 1006) {
    this.readyState = 3;
    this.onclose?.({ code });
  }
}

const last = () => FakeSocket.all[FakeSocket.all.length - 1];
const fetched = (fetcher: ReturnType<typeof vi.fn>) => fetcher.mock.calls.map((c) => String(c[0]));

let fetcher: ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.useFakeTimers();
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("location", { protocol: "http:", host: "hub" });
  vi.stubGlobal("localStorage", { getItem: () => "tok", setItem: () => {}, removeItem: () => {} });
  vi.stubGlobal("window", { setTimeout: (f: () => void, ms: number) => setTimeout(f, ms), clearTimeout: (t: number) => clearTimeout(t) });
  fetcher = vi.fn().mockImplementation(() => Promise.resolve(new Response("{}", { status: 200 })));
  vi.stubGlobal("fetch", fetcher);
});

afterEach(() => {
  reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

/** Connects and says hello on stream s1 at event 5 (with the hub's features, if given); returns the socket. */
function online(features?: string[], receiptVersion?: number) {
  connect();
  const s = last();
  s.open();
  s.push({ type: "hello", server_version: "v1", event_stream: "s1", event_seq: 5, ...(features ? { features } : {}), ...(receiptVersion === undefined ? {} : { message_send_state: receiptVersion }) });
  expect(getState().conn).toBe("online");
  return s;
}

describe("reconnecting", () => {
  it("trusts send receipts only after this socket's authenticated capability hello", () => {
    const receipt = (id: string) => ({ type: "message_send_state", machine_id: "m", session_id: "chat", client_msg_id: id, state: "processing", at: "2026-11-12T10:00:00Z" });
    const old = online(["message_send_state"], 1);
    old.push(receipt("first")); expect(messageFeedback("m", "chat", "first")).toBe("processing");
    reconnectNow(); const next = last(); next.open();
    expect(getState().features).toContain("message_send_state"); expect(appSocketReady()).toBe(false);
    next.push(receipt("before-hello")); old.push(receipt("old-socket"));
    expect(messageFeedback("m", "chat", "before-hello")).toBeUndefined();
    expect(messageFeedback("m", "chat", "old-socket")).toBeUndefined();
    next.push({ type: "hello", features: ["message_send_state"], resumed: true }); next.push(receipt("legacy-forgery"));
    expect(messageFeedback("m", "chat", "legacy-forgery")).toBeUndefined();
    next.push({ type: "hello", message_send_state: 1, features: ["message_send_state"], resumed: true }); next.push(receipt("trusted"));
    expect(messageFeedback("m", "chat", "trusted")).toBe("processing");
    vi.stubGlobal("localStorage", { getItem: () => "replacement-token" }); next.push(receipt("retired-credential"));
    expect(messageFeedback("m", "chat", "retired-credential")).toBeUndefined();
  });
  it("retires send receipt trust when the same token gets a new credential epoch", () => {
    const socket = online(["message_send_state"], 1);
    vi.stubGlobal("localStorage", { getItem: (key: string) => key === "persona.credentialEpoch" ? "next-epoch" : "tok" });
    expect(appSocketReady()).toBe(true);
    socket.push({ type: "message_send_state", machine_id: "m", session_id: "chat", client_msg_id: "retired-epoch", state: "processing", at: "2026-11-12T10:00:00Z" });
    expect(messageFeedback("m", "chat", "retired-epoch")).toBeUndefined();
  });
  it("gates private native frames on the current authenticated hello, not cached features or socket open", () => {
    const connections: boolean[] = [], frames: string[] = [];
    const offConnection = onAppSocket((value) => connections.push(value));
    const offFrames = onPrivateUIStream((raw) => frames.push(raw));
    try {
      const old = online(["extension_ui_stream_v1"]);
      expect(appSocketReady()).toBe(true);
      reconnectNow();
      const next = last(); next.open();
      expect(appSocketReady()).toBe(false);
      expect(getState().features).toContain("extension_ui_stream_v1");
      next.push({ type: "extension_ui_stream", event_seq: 999, kind: "snapshot" });
      old.push({ type: "extension_ui_stream", kind: "snapshot" });
      expect(frames).toEqual([]);
      next.push({ type: "hello", event_stream: "s1", event_seq: 5, resumed: true, features: ["extension_ui_stream_v1"] });
      const native = { type: "extension_ui_stream", event_seq: 999, kind: "snapshot" };
      next.push(native);
      expect(frames).toEqual([JSON.stringify(native)]);
      expect(appSocketReady()).toBe(true);
      next.drop(); vi.advanceTimersByTime(600);
      expect(last().url).toContain("event_seq=5"); // private session traffic is never broadcast replay
      expect(connections).toContain(false);
      expect(appSocketReady()).toBe(false);
    } finally { offConnection(); offFrames(); }
  });
  it("refuses an existing connection after its credential changes", () => {
    online(["extension_ui_stream_v1"]);
    vi.stubGlobal("localStorage", { getItem: () => "changed-token" });
    expect(appSocketReady()).toBe(false);
  });
  it("first connect sends no resume point; reconnects resume from the last broadcast seen", () => {
    const s = online();
    expect(s.url).toBe("ws://hub/api/v1/ws?token=tok&features=sessions_list,sessions_list_changes,sessions_delta,session_live,extension_ui_stream_v1,persona_contracts_v1"); // the session list comes as deltas; status lines are pushed
    s.push({ type: "todos", event_seq: 6 });
    s.push({ type: "items", machine_id: "m", session_id: "x", items: [] });
    s.drop();
    vi.advanceTimersByTime(600);
    expect(last().url).toBe("ws://hub/api/v1/ws?token=tok&event_stream=s1&event_seq=6&features=sessions_list,sessions_list_changes,sessions_delta,session_live,extension_ui_stream_v1,persona_contracts_v1");
  });

  it("an ordinary drop stays invisible for 8 s, then shows as before", () => {
    const s = online();
    s.drop();
    expect(getState().conn).toBe("online");
    // the attempts keep failing
    for (let t = 0; t < 7_900; t += 100) {
      vi.advanceTimersByTime(100);
      const cur = last();
      if (cur !== s && cur.readyState === 0) cur.drop();
    }
    expect(getState().conn).toBe("online");
    vi.advanceTimersByTime(200);
    expect(getState().conn).not.toBe("online");
  });

  it("back within the grace: nothing changes and a resumed hello refetches nothing", () => {
    const s = online();
    const seen: string[] = [];
    const off = onHubEvent("hello", (m) => seen.push(String(m.resumed)));
    fetcher.mockClear();
    s.drop();
    vi.advanceTimersByTime(600); // the first retry is within 600 ms
    const n = last();
    expect(n).not.toBe(s);
    n.open();
    n.push({ type: "hello", server_version: "v1", event_stream: "s1", event_seq: 7, resumed: true });
    expect(getState().conn).toBe("online");
    expect(seen).toEqual(["true"]);
    expect(fetched(fetcher)).toEqual([]);
    off();
  });

  it("a hello that did not resume refreshes everything, as before", async () => {
    const s = online();
    await vi.advanceTimersByTimeAsync(0);
    fetcher.mockClear();
    s.drop();
    vi.advanceTimersByTime(600);
    last().open();
    last().push({ type: "hello", server_version: "v1", event_stream: "s1", event_seq: 9, resumed: false });
    await vi.advanceTimersByTimeAsync(0);
    expect(fetched(fetcher)).toEqual(expect.arrayContaining(["/api/v1/interactions", "/api/v1/machines", "/api/v1/todos"]));
    expect(fetched(fetcher)).not.toContain("/api/v1/sessions"); // cached legacy rows + socket recovery, never another full-list download
  });

  it("a restart retries at once and stays invisible for 30 s", () => {
    const s = online();
    s.push({ type: "hub_restarting", back_in_ms: 2000 });
    s.drop(1001);
    expect(getState().conn).toBe("online");
    const before = FakeSocket.all.length;
    vi.advanceTimersByTime(400);
    expect(FakeSocket.all.length).toBe(before + 1); // retried within 150–400 ms
    for (let t = 400; t < 29_500; t += 100) {
      const cur = last();
      if (cur.readyState === 0) cur.drop(1006);
      vi.advanceTimersByTime(100);
    }
    expect(getState().conn).toBe("online");
    // quick retries: about one a second
    expect(FakeSocket.all.length - before).toBeGreaterThan(20);
    last().drop(1006);
    vi.advanceTimersByTime(1_000);
    expect(getState().conn).not.toBe("online"); // as before: connecting while an attempt runs, offline between
  });

  it("disconnect cancels a reconnect already scheduled (signed out after a drop: no socket with an empty token)", () => {
    const s = online();
    s.drop();
    disconnect(); // e.g. a 401 signs out before the retry fires
    const before = FakeSocket.all.length;
    vi.advanceTimersByTime(20_000);
    expect(FakeSocket.all.length).toBe(before);
  });

  it("close code 1001 alone counts as a restart", () => {
    const s = online();
    s.drop(1001);
    vi.advanceTimersByTime(20_000);
    expect(getState().conn).toBe("online");
  });
});

describe("live pushes (#1963)", () => {
  it("asks for session_live and hands each live message to lib/live", () => {
    const s = online();
    expect(s.url).toContain("session_live");
    s.push({ type: "live", machine_id: "m", session_id: "x", live: { verb: "Frosting", tokens_down: 1200 } });
    expect(liveEntry("m", "x")?.live).toEqual({ verb: "Frosting", tokens_down: 1200 });
    s.push({ type: "live", machine_id: "m", session_id: "x", live: null });
    expect(liveEntry("m", "x")).toBeUndefined();
    resetLive();
  });

  it("a hello that did not resume forgets every line; a resumed one keeps them", () => {
    const s = online();
    s.push({ type: "live", machine_id: "m", session_id: "x", live: { verb: "Frosting" } });
    s.drop(1006);
    vi.advanceTimersByTime(1000);
    last().open();
    last().push({ type: "hello", server_version: "v1", event_stream: "s1", event_seq: 6, resumed: true });
    expect(liveEntry("m", "x")?.live).toEqual({ verb: "Frosting" });
    last().drop(1006);
    vi.advanceTimersByTime(1000);
    last().open();
    last().push({ type: "hello", server_version: "v1", event_stream: "s2", event_seq: 1, resumed: false });
    expect(liveEntry("m", "x")).toBeUndefined(); // nothing is replayed, so the lines from before the drop are gone
    resetLive();
  });
});

describe("the facts behind the pill (lib/connection.ts)", () => {
  it("a drop records since when and the next try; a restart is told; hello clears them", () => {
    const s = online();
    vi.setSystemTime(50_000);
    s.push({ type: "hub_restarting" });
    s.drop(1001);
    const link = getState().link;
    expect(link.lastOnline).toBe(50_000);
    expect(link.restarting).toBe(true);
    expect(link.nextTry).toBeGreaterThan(50_000);
    vi.advanceTimersByTime(400);
    last().open();
    last().push({ type: "hello", server_version: "v1", event_stream: "s1", event_seq: 5, resumed: true });
    expect(getState().link.restarting).toBe(false);
    expect(getState().link.nextTry).toBe(0);
  });

  it("立即重试 opens a socket now instead of waiting for the backoff", () => {
    const s = online();
    s.drop();
    for (let i = 0; i < 4; i++) {
      vi.advanceTimersByTime(20_000);
      if (last().readyState === 0) last().drop();
    }
    const before = FakeSocket.all.length;
    reconnectNow();
    expect(FakeSocket.all.length).toBe(before + 1);
  });

  it("the phase stays put while the hub is down: a new attempt does not move offline back to connecting", () => {
    const s = online();
    s.drop();
    vi.advanceTimersByTime(9_000); // past the grace
    for (let i = 0; i < 6 && getState().conn !== "offline"; i++) {
      if (last().readyState === 0) last().drop();
      vi.advanceTimersByTime(20_000);
    }
    expect(getState().conn).toBe("offline");
    // every later attempt, while its socket is opening and after it fails
    for (let i = 0; i < 4; i++) {
      vi.advanceTimersByTime(20_000); // the next attempt has started
      expect(last().readyState).toBe(0);
      expect(getState().conn).toBe("offline");
      last().drop();
      expect(getState().conn).toBe("offline");
    }
    reconnectNow();
    expect(getState().conn).toBe("offline");
  });

  it("a restart is told for 5 minutes at most, then it is just out of reach", () => {
    const s = online();
    vi.setSystemTime(100_000);
    s.push({ type: "hub_restarting" });
    s.drop(1001);
    vi.advanceTimersByTime(60_000);
    if (last().readyState === 0) last().drop();
    expect(getState().link.restarting).toBe(true);
    for (let t = 0; t < 300_000; t += 15_000) {
      vi.advanceTimersByTime(15_000);
      if (last().readyState === 0) last().drop();
    }
    expect(getState().link.restarting).toBe(false);
    expect(getState().conn).toBe("offline");
  });

  it("an error toast while out of reach is the calm line (the probe the store registers)", () => {
    const s = online();
    expect(offlineNow()).toBeNull();
    s.drop();
    expect(offlineNow()).toBe("hub"); // inside the grace too: the socket is gone
  });
});

describe("a slow refresh() does not put back an older snapshot", () => {
  type Held = Record<string, ((body: unknown) => void)[]>;
  /** GETs whose path is in `held` wait until their turn is resolved; the rest answer `answers[path]` (or {}) at once. */
  function holdFetch(held: string[], answers: Record<string, unknown> = {}): Held {
    const turns: Held = Object.fromEntries(held.map((p) => [p, []]));
    const reply = (body: unknown) => ({ ok: true, status: 200, headers: new Headers(), text: async () => JSON.stringify(body) });
    fetcher.mockImplementation(async (url: string) => {
      const path = String(url).replace(/^.*\/api\/v1/, "");
      if (turns[path]) return reply(await new Promise((r) => turns[path].push(r)));
      return reply(answers[path] ?? {});
    });
    return turns;
  }
  const settle = async () => {
    for (let i = 0; i < 20; i++) await Promise.resolve();
  };
  const session = (machine_id: string, session_id: string, state: string) => ({ machine_id, session_id, state, title: session_id });

  it("a machines push that comes while GET /machines is out wins over its answer", async () => {
    const turns = holdFetch(["/machines"]);
    const s = online(); // the hello refreshes
    await settle();
    expect(turns["/machines"]).toHaveLength(1);
    s.push({ type: "machines", machines: [{ id: "a", name: "A", online: false }] });
    turns["/machines"][0]({ machines: [{ id: "a", name: "A", online: true }] });
    await settle();
    expect(getState().loaded).toBe(true);
    expect(getState().machines).toEqual([{ id: "a", name: "A", online: false }]);
  });

  it("applyMachine() while GET /machines is out wins over its answer", async () => {
    const turns = holdFetch(["/machines"]);
    online();
    await settle();
    turns["/machines"][0]({ machines: [{ id: "a", name: "A", online: true, engine: "herdr" }] });
    await settle();
    const later = refresh();
    await settle();
    applyMachine({ id: "a", name: "A", online: true, engine: "orca" });
    turns["/machines"][1]({ machines: [{ id: "a", name: "A", online: true, engine: "herdr" }] });
    await later;
    expect(getState().machines[0].engine).toBe("orca");
  });

  it("a hub without deltas: a sessions push while GET /sessions is out keeps that machine's pushed list", async () => {
    const turns = holdFetch(["/sessions"]);
    const s = online(); // the hello says no sessions_delta
    await settle();
    s.push({ type: "sessions", machine_id: "m", sessions: [session("m", "x", "working")] });
    turns["/sessions"][0]({ sessions: [session("m", "x", "idle"), session("n", "y", "idle")] });
    await settle();
    expect(getState().sessions.m.map((x) => x.state)).toEqual(["working"]);
    expect(getState().sessions.n.map((x) => x.session_id)).toEqual(["y"]); // a machine not pushed takes the answer
  });

  it("reset() while a refresh() is out: its answers belong to the old login and are dropped", async () => {
    const turns = holdFetch(["/sessions"], { "/machines": { machines: [{ id: "a", name: "A", online: true }] } });
    online();
    await settle();
    reset();
    turns["/sessions"][0]({ sessions: [session("a", "x", "idle")] });
    await settle();
    const st = getState();
    expect(st.loaded).toBe(false);
    expect(st.machines).toEqual([]);
    expect(st.sessions).toEqual({});
  });

  it("two overlapping refreshes: the older one answering last does not undo the newer one", async () => {
    const turns = holdFetch(["/machines"]);
    online(); // the first refresh
    await settle();
    const newer = refresh();
    await settle();
    expect(turns["/machines"]).toHaveLength(2);
    turns["/machines"][1]({ machines: [{ id: "a", name: "A", online: false }] });
    await newer;
    turns["/machines"][0]({ machines: [{ id: "a", name: "A", online: true }] });
    await settle();
    expect(getState().machines).toEqual([{ id: "a", name: "A", online: false }]);
  });

  it("refreshes answered in order are all applied", async () => {
    const turns = holdFetch(["/machines"]);
    online();
    await settle();
    turns["/machines"][0]({ machines: [{ id: "a", name: "A", online: true }] });
    await settle();
    expect(getState().machines[0].online).toBe(true);
    const next = refresh();
    await settle();
    turns["/machines"][1]({ machines: [{ id: "a", name: "A", online: false }] });
    await next;
    expect(getState().machines[0].online).toBe(false);
  });
});

describe("client_hello heartbeat", () => {
  it("asks for a 45 s pong timeout and leaves the interval to the hub, on every connection", () => {
    connect();
    last().open();
    last().drop();
    vi.advanceTimersByTime(1_000);
    last().open();
    expect(FakeSocket.all.length).toBe(2);
    for (const s of FakeSocket.all) {
      const hello = s.sent.map((m) => JSON.parse(m)).find((m) => m.type === "client_hello");
      expect(hello.heartbeat).toEqual({ pong_timeout: 45 });
    }
  });

  it("names this browser's notification device, so the hub pushes nothing to it while a page is open (#7459)", () => {
    connect();
    last().open();
    const hello = last().sent.map((m) => JSON.parse(m)).find((m) => m.type === "client_hello");
    expect(hello.device_id).toBeTruthy();
    expect(hello.device_id).toBe(localStorage.getItem("persona.notifyDevice"));
  });
});

describe("keeping a quiet connection (app_ping)", () => {
  const pings = (s: FakeSocket) => s.sent.map((m) => JSON.parse(m)).filter((m) => m.type === "ping");
  /** Lets ms go by, looking at the socket every 5 s as the page does. */
  const idle = (ms: number, onTick?: () => void) => {
    for (let t = 0; t < ms; t += 5_000) {
      vi.advanceTimersByTime(5_000);
      checkSocket();
      onTick?.();
    }
  };

  it("an idle hub that answers its pings keeps the socket: no reconnect every 70 s", () => {
    const s = online(["app_ping"]);
    const sockets = FakeSocket.all.length;
    let answered = 0;
    // five quiet minutes: the hub pushes nothing, but answers every ping it is sent
    idle(300_000, () => {
      for (const p of pings(s).slice(answered)) s.push({ type: "pong", id: p.id });
      answered = pings(s).length;
    });
    expect(answered).toBeGreaterThan(5);
    expect(FakeSocket.all.length).toBe(sockets);
  });

  it("a hub that stops answering is reconnected", () => {
    const s = online(["app_ping"]);
    const sockets = FakeSocket.all.length;
    idle(80_000); // pings go out, no pong comes back
    expect(pings(s).length).toBeGreaterThan(0);
    expect(FakeSocket.all.length).toBeGreaterThan(sockets);
  });

  it("a hub without app_ping is sent no ping, and a quiet socket is still reconnected", () => {
    const s = online([]);
    const sockets = FakeSocket.all.length;
    idle(80_000);
    expect(pings(s)).toEqual([]);
    expect(FakeSocket.all.length).toBeGreaterThan(sockets);
  });
});
