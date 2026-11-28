import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { runtimeAgents, runtimeIdentity } from "./runtime-list";
import { getSeenVisit } from "./seen-rows";
import { canInterruptAgentRow, connect, getState, markRowSeen, markSeen, reset, subscribe, upsertSession } from "./store";
import type { Session } from "./types";

vi.mock("./api", async (original) => ({ ...await original<typeof import("./api")>(), api: vi.fn() }));

class Socket {
  static OPEN = 1;
  static last: Socket;
  readyState = 1;
  send = vi.fn();
  constructor() { Socket.last = this; }
  close() { this.readyState = 3; }
}
const row = (session_id: string, patch: Partial<Session> = {}): Session => ({
  machine_id: "demo", session_id, title: session_id, cwd: "/demo", status: "idle", state: "unread", ...patch,
});

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("location", { protocol: "https:", host: "hub.example.com" });
  vi.stubGlobal("localStorage", { getItem: () => "mock-token" });
  vi.stubGlobal("window", { setTimeout, clearTimeout });
  reset();
  vi.mocked(api).mockReset().mockResolvedValue({});
});
afterEach(() => { reset(); vi.useRealTimers(); vi.unstubAllGlobals(); });

it.each([
  ["2025-12-31T00:00:00.123000001Z", false],
  ["2025-12-31T00:00:00.123000002Z", false],
  ["2025-12-31T00:00:00.123000000Z", true],
])("rechecks a retained interrupt grant against the paged Agent at %s", async (updatedAt, allowed) => {
  const shown = row("busy", { state: "working", status: "working", updated_at: "2025-12-31T00:00:00.123000001Z", capabilities: { interrupt: true } });
  upsertSession(shown);
  const revoked = { ...shown, updated_at: String(updatedAt), waiting: false, capabilities: { interrupt: false } };
  vi.mocked(api).mockResolvedValue({ sessions: [revoked], revision: "2", has_more: false, total: 1, remove: [] });
  await runtimeAgents.load();
  expect(runtimeAgents.getSnapshot().rows).toEqual([revoked]);
  expect(canInterruptAgentRow(shown)).toBe(allowed);
});

describe("marking fallback-only sessions seen", () => {
  it("marks a paged-only row, keeps its place and receives the hub acknowledgement without the socket", async () => {
    const shown = row("off-page", { waiting: false });
    markRowSeen(shown);
    await Promise.resolve();
    expect(api).toHaveBeenCalledExactlyOnceWith("/sessions/demo/off-page/seen", { method: "POST" });
    expect(Object.values(getState().seen)).toMatchObject([{ route: "http", answered: true }]);
    expect(getSeenVisit().seen["demo/off-page"]).toBe("unread");
    expect(getState().sessions.demo).toBeUndefined(); // a paged summary is not a complete chat detail
  });

  it("does not mark a paged row already seen a nanosecond after its retained unread detail", () => {
    const cached = row("recent", { updated_at: "2025-12-31T00:00:00.123000001Z" });
    upsertSession(cached);
    markRowSeen({ ...cached, state: "idle", updated_at: "2025-12-31T00:00:00.123000002Z" });
    expect(api).not.toHaveBeenCalled();
  });

  it.each([undefined, "2025-12-28T00:00:00Z"])("uses the newer row when a paged menu and the retained detail disagree (previous receipt %s)", (previousSeenAt) => {
    const fresh = row("new-turn", { waiting: false, updated_at: "2025-12-31T00:00:00Z" });
    upsertSession(row("new-turn", { state: "working", updated_at: "2025-12-29T00:00:00Z", seen_at: previousSeenAt, agent_name: "Retained name" }));
    markRowSeen(fresh);
    expect(api).toHaveBeenCalledExactlyOnceWith("/sessions/demo/new-turn/seen", { method: "POST" });
    const detail = getState().sessions.demo[0];
    expect(detail.state).toBe("idle");
    expect(detail).toMatchObject({ agent_name: "Retained name", seen_at: "2026-01-01T00:00:00.000Z" });
    const mark = getState().seen[runtimeIdentity(fresh)];
    expect(mark.localSeenAt).toBe(detail.seen_at);
    expect(mark.recordedAt).toBeUndefined(); // only the hub can acknowledge the receipt
    upsertSession(row("new-turn", { state: "working", updated_at: "2026-01-02T00:00:00Z" }));
    markRowSeen(fresh);
    expect(api).toHaveBeenCalledTimes(1);
    expect(getState().sessions.demo[0].state).toBe("working");
  });

  it("records the local receipt when the menu uses the retained detail itself", () => {
    const given = row("retained", { seen_at: "2025-12-28T00:00:00Z" });
    upsertSession(given);
    markRowSeen(getState().sessions.demo[0]);
    const detail = getState().sessions.demo[0];
    expect(detail).toMatchObject({ state: "idle", seen_at: "2026-01-01T00:00:00.000Z" });
    markRowSeen(given);
    expect(api).toHaveBeenCalledExactlyOnceWith("/sessions/demo/retained/seen", { method: "POST" });
  });

  it("sends each supplied unread identity once and retains only acted-on rows in one update", () => {
    const full = Array.from({ length: 75 }, (_, i) => row(`agent-${i}`));
    const untouched = row("retained");
    upsertSession(untouched);
    const changed = vi.fn();
    const off = subscribe(changed);
    markSeen([full[64], full[70], full[64]]);
    off();

    expect(vi.mocked(api).mock.calls).toEqual([
      ["/sessions/demo/agent-64/seen", { method: "POST" }],
      ["/sessions/demo/agent-70/seen", { method: "POST" }],
    ]);
    expect(getState().sessions.demo).toEqual([
      untouched,
      { ...full[64], state: "idle", seen_at: "2026-01-01T00:00:00.000Z" },
      { ...full[70], state: "idle", seen_at: "2026-01-01T00:00:00.000Z" },
    ]);
    expect(getState().sessions.demo[0]).toBe(untouched);
    expect(changed).toHaveBeenCalledTimes(1);
  });

  it("uses retained current state and skips agents no longer unread", () => {
    const working = row("working", { state: "working", status: "working" });
    const current = row("changed", { title: "Current title" });
    upsertSession(working);
    upsertSession(current);
    markSeen([row("working"), row("changed"), row("already-seen", { state: "idle" })]);

    expect(vi.mocked(api).mock.calls).toEqual([["/sessions/demo/changed/seen", { method: "POST" }]]);
    expect(getState().sessions.demo).toEqual([
      working,
      { ...current, state: "idle", seen_at: "2026-01-01T00:00:00.000Z" },
    ]);
    markSeen([row("changed")]);
    expect(api).toHaveBeenCalledTimes(1);
  });

  it("sends fallback rows over the connected socket and distinguishes composite identities", () => {
    connect();
    const left = row("part/agent", { machine_id: "demo" });
    const right = row("agent", { machine_id: "demo/part" });
    markSeen([left, right, left]);

    expect(Socket.last.send.mock.calls.map(([message]) => JSON.parse(message))).toEqual([
      { type: "seen", machine_id: left.machine_id, session_id: left.session_id },
      { type: "seen", machine_id: right.machine_id, session_id: right.session_id },
    ]);
    expect(api).not.toHaveBeenCalled();
    expect(getState().sessions.demo[0].state).toBe("idle");
    expect(getState().sessions["demo/part"][0].state).toBe("idle");
  });

  it("does not let an older detail cache suppress a newly unread full snapshot", () => {
    upsertSession(row("updated", { state: "idle", updated_at: "2025-12-30T00:00:00Z", seen_at: "2025-12-30T01:00:00Z" }));
    const latest = row("updated", { title: "Latest result", updated_at: "2025-12-31T00:00:00Z", state_since: "2025-12-31T00:00:01Z" });
    markSeen([latest]);
    expect(vi.mocked(api).mock.calls).toEqual([["/sessions/demo/updated/seen", { method: "POST" }]]);
    expect(getState().sessions.demo[0]).toEqual({ ...latest, state: "idle", seen_at: "2026-01-01T00:00:00.000Z" });
  });
});
