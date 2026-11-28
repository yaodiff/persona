import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { useCounts } from "./sessions";
import { applyCompleteSessions, connect, getSession, getState, rememberSessionRow, reset, upsertSession } from "./store";
import { archiveAgent, appArchiveDeps } from "./agent-archives";
import { runtimeSessions } from "./runtime-list";
import { isWaiting } from "./agent-groups";
import { backgroundCount, workflowShortLine } from "./workflows";
import { t } from "../i18n";
import type { ListCounts } from "./session-list";
import type { Session } from "./types";

class Socket {
  static OPEN = 1;
  static last: Socket;
  readyState = 1;
  onopen?: () => void;
  onmessage?: (event: { data: string }) => void;
  constructor(public url: string) { Socket.last = this; }
  send() {}
  close() { this.readyState = 3; }
  push(value: unknown) { this.onmessage?.({ data: JSON.stringify(value) }); }
}
const session = (id: string, extra: Partial<Session> = {}): Session => ({ machine_id: "demo", session_id: id, title: id, cwd: "/demo", status: "idle", state: "idle", updated_at: "2026-01-01T00:00:00Z", waiting: false, ...extra });
let requests: URL[];
let changed: Session[];
let removed: string[];
let expired: boolean;
let scopedNeeds: number;
let scopedWaiting: number;
let scopedHeld: number;

beforeEach(() => {
  vi.useFakeTimers();
  requests = [];
  changed = [];
  removed = [];
  expired = false;
  scopedNeeds = 1;
  scopedWaiting = 0;
  scopedHeld = 0;
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("location", { protocol: "http:", host: "demo" });
  vi.stubGlobal("localStorage", { getItem: () => "demo-token" });
  vi.stubGlobal("window", { setTimeout, clearTimeout });
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    const url = new URL(path, "http://demo");
    requests.push(url);
    if (url.pathname === "/api/v1/sessions") {
      if (url.searchParams.has("since") && expired) {
        expired = false;
        return new Response(JSON.stringify({ code: "revision_expired", error: "expired" }), { status: 409 });
      }
      if (url.searchParams.has("counts")) {
        const scoped = url.searchParams.get("agents_only") === "1";
        // The all-sessions aggregate also includes Assistant and health conversations.
        return Response.json({ total: 8000, machine: { demo: 8000 }, project: { "/demo": 8000 }, group: {},
          state: scoped ? { needs_you: scopedNeeds, working: 3, unread: 2 } : { needs_you: 3, working: 5, unread: 4 },
          ...(scoped && scopedWaiting ? { background_waiting: scopedWaiting } : {}),
          ...(scoped && scopedHeld ? { held_needs: scopedHeld } : {}),
          revision: "r2" } satisfies Partial<ListCounts> as ListCounts);
      }
      if (!url.search) return Response.json({ sessions: [session("legacy")] });
      if (url.searchParams.has("since")) return Response.json({ sessions: changed, remove: removed, total: 8000, has_more: false, revision: "r2" });
      const full = url.searchParams.get("summary") === "0";
      return Response.json({ sessions: Array.from({ length: 50 }, (_, i) => session(`s${i}`, { waiting: full ? undefined : false, queued: full ? ["Queued demo"] : undefined })), remove: [], total: 8000, has_more: true, next_cursor: "frozen", revision: "r1" });
    }
    if (url.pathname === "/api/v1/agents/sessions") return Response.json({ sessions: [], remove: [], total: 0, has_more: false, revision: "p1", machine: {}, project: {}, group: {}, state: {} });
    return Response.json({});
  }));
});
afterEach(() => { reset(); vi.useRealTimers(); vi.unstubAllGlobals(); });
const settle = () => vi.advanceTimersByTimeAsync(0);
/** Past the invalidation window (session-list.ts INVALIDATE_THROTTLE_MS): a hint's trailing read has gone out. */
const settleThrottled = () => vi.advanceTimersByTimeAsync(2_100);
function hello(features: string[]) {
  connect();
  Socket.last.push({ type: "hello", features });
}

function countsShown() {
  let counts!: ReturnType<typeof useCounts>;
  function Probe() {
    counts = useCounts();
    return null;
  }
  renderToStaticMarkup(createElement(Probe));
  return counts;
}

describe("negotiated bounded runtime lists", () => {
  it("counts Agents across all pages and updates the badge from the scoped delta counts", async () => {
    hello(["sessions_list", "sessions_lookup", "sessions_hierarchy", "sessions_agents_only"]);
    await settle();

    expect(countsShown()).toMatchObject({ needs: 1, working: 3, unread: 2, badge: 1, quietCounted: true });
    const scoped = requests.filter((url) => url.searchParams.get("agents_only") === "1");
    expect(scoped).toHaveLength(2);
    expect(scoped.every((url) => url.searchParams.get("roots") === "1")).toBe(true);
    expect(scoped.some((url) => url.searchParams.has("cursor"))).toBe(false);

    scopedNeeds = 0;
    Socket.last.push({ type: "sessions_list", revision: "r2" });
    await settleThrottled();
    expect(countsShown()).toMatchObject({ needs: 0, badge: 0 });
  });

  it("takes the Agents that only wait in the background out of the hub's working count (#10938)", async () => {
    scopedWaiting = 2;
    hello(["sessions_list", "sessions_lookup", "sessions_hierarchy", "sessions_agents_only"]);
    await settle();

    expect(countsShown()).toMatchObject({ working: 1, waiting: 2, unread: 2 });
  });

  it("retains the legacy aggregate on a paged hub without the scoped query", async () => {
    hello(["sessions_list", "sessions_lookup"]);
    await settle();

    expect(countsShown()).toMatchObject({ needs: 3, working: 5, unread: 4, badge: 3, quietCounted: true });
    expect(requests.some((url) => url.searchParams.has("agents_only"))).toBe(false);
  });

  it("requests a single 50-row summary page and separate counts, never the complete list", async () => {
    hello(["sessions_list", "sessions_delta", "sessions_lookup"]);
    await settle();
    const pages = requests.filter((r) => r.pathname === "/api/v1/sessions" && r.searchParams.has("limit"));
    expect(pages).toHaveLength(1);
    expect(pages[0].searchParams.get("limit")).toBe("50");
    expect(pages[0].searchParams.get("summary")).toBe("1");
    expect(requests.some((r) => r.pathname === "/api/v1/sessions" && !r.search)).toBe(false);
    expect(getState().sessionSummaries).toHaveLength(50);
    expect(getState().sessionCounts?.total).toBe(8000);
    expect(getState().sessions).toEqual({});
    expect(Socket.last.url).toContain("features=sessions_list,sessions_list_changes,sessions_delta,session_live");
  });

  it("keeps chat details separate and consumes title-only invalidations via since", async () => {
    hello(["sessions_list", "sessions_lookup"]);
    await settle();
    const complete = session("s0", {
      title: "Complete detail title",
      background: [{ id: "bg", kind: "shell", description: "Long detail" }],
      background_ended: [{ id: "done", kind: "shell", status: "completed", ended_at: "2026-01-01T00:01:00Z" }],
    });
    upsertSession(complete);
    changed = [session("s0", { title: "New summary", waiting: true })];
    Socket.last.push({ type: "sessions_list", revision: "not-the-baseline" });
    await settle();
    expect(requests.filter((r) => r.searchParams.has("since")).at(-1)?.searchParams.get("since")).toBe("r1");
    expect(getState().sessionSummaries.find((s) => s.session_id === "s0")?.title).toBe("New summary");
    expect(getSession("demo", "s0")?.background).toEqual(complete.background);
    expect(getSession("demo", "s0")?.background_ended).toEqual(complete.background_ended);
    expect(getSession("demo", "s0")?.title).toBe("Complete detail title");
  });

  it("normalizes runtime tuple removals before applying a loaded-row tombstone", async () => {
    hello(["sessions_list", "sessions_lookup"]);
    await settle();
    const sid = "row/<&>/with/slashes";
    changed = [session(sid, { updated_at: "2027-01-01T00:00:00Z" })];
    Socket.last.push({ type: "sessions_list", revision: "r2" });
    await settleThrottled();
    expect(runtimeSessions.getSnapshot().rows.some((s) => s.session_id === sid)).toBe(true);
    changed = [];
    removed = [JSON.stringify(["demo", sid])];
    Socket.last.push({ type: "sessions_list", revision: "r3" });
    await settleThrottled();
    expect(runtimeSessions.getSnapshot().rows.some((s) => s.session_id === sid)).toBe(false);
  });

  it("clears optimistic summary markers when complete chat info arrives", async () => {
    hello(["sessions_list", "sessions_lookup"]);
    await settle();
    upsertSession({ ...getState().sessionSummaries[0], title: "Optimistic title" });
    upsertSession(session("s0", { waiting: undefined, background: [{ id: "bg", kind: "shell" }] }));
    expect(getSession("demo", "s0")?.waiting).toBeUndefined();
    expect(backgroundCount(getSession("demo", "s0"))).toBe(1);
  });

  it("recovers a resumed socket's expired revision with a bounded baseline", async () => {
    hello(["sessions_list", "sessions_lookup"]);
    await settle();
    expired = true;
    Socket.last.push({ type: "hello", features: ["sessions_list", "sessions_lookup"], resumed: true });
    Socket.last.push({ type: "sessions_list", revision: "new-epoch" });
    await settle();
    expect(requests.filter((r) => r.pathname === "/api/v1/sessions" && r.searchParams.has("limit") && !r.searchParams.has("since"))).toHaveLength(2);
    expect(runtimeSessions.getSnapshot().rows).toHaveLength(50);
    expect(requests.some((r) => r.pathname === "/api/v1/sessions" && !r.search)).toBe(false);
  });

  it("replaces complete snapshots and clears omitted task arrays", async () => {
    hello(["sessions_list", "sessions_lookup"]);
    await settle();
    applyCompleteSessions([session("s0", {
      waiting: undefined,
      background: [{ id: "bg", kind: "shell" }],
      background_ended: [{ id: "done", kind: "shell", status: "completed" }],
      subagents: [], workflows: [], queued: ["Queued task"], queued_relayed: 1, permission_mode: "plan",
    })]);
    applyCompleteSessions([session("s0", { waiting: undefined, title: "Complete latest", permission_mode: "default" })]);
    expect(getSession("demo", "s0")?.title).toBe("Complete latest");
    expect(getSession("demo", "s0")?.background).toBeUndefined();
    expect(getSession("demo", "s0")?.background_ended).toBeUndefined();
    expect(getSession("demo", "s0")?.queued).toBeUndefined();
    expect(getSession("demo", "s0")?.queued_relayed).toBeUndefined();
    expect(getSession("demo", "s0")?.permission_mode).toBe("default");
  });
  it("clears omitted queued fields on complete session upsert while preserving them on summary patches", async () => {
    hello(["sessions_list", "sessions_lookup"]);
    await settle();
    upsertSession(session("s0", { waiting: undefined, queued: ["Queued item"], queued_relayed: 1 }));
    expect(getSession("demo", "s0")?.queued).toEqual(["Queued item"]);
    expect(getSession("demo", "s0")?.queued_relayed).toBe(1);
    upsertSession(session("s0", { waiting: true, title: "Summary patch" }));
    expect(getSession("demo", "s0")?.queued).toEqual(["Queued item"]);
    expect(getSession("demo", "s0")?.queued_relayed).toBe(1);
    upsertSession(session("s0", { waiting: undefined, title: "Complete pushed row" }));
    expect(getSession("demo", "s0")?.queued).toBeUndefined();
    expect(getSession("demo", "s0")?.queued_relayed).toBeUndefined();
  });
  it("retains a clicked filtered-row hint without treating it as complete detail", async () => {
    hello(["sessions_list", "sessions_lookup"]);
    await settle();
    rememberSessionRow(session("outside-page", { title: "Filtered row", waiting: true }));
    expect(getSession("demo", "outside-page")?.title).toBe("Filtered row");
    expect(getState().sessions.demo).toBeUndefined();
  });
  it("uses bounded complete pages on a paged hub without exact lookup support", async () => {
    hello(["sessions_list"]);
    await settle();
    const pages = requests.filter((r) => r.pathname === "/api/v1/sessions" && r.searchParams.has("limit"));
    expect(pages).toHaveLength(1);
    expect(pages[0].searchParams.get("summary")).toBe("0");
    expect(getSession("demo", "s0")?.queued).toEqual(["Queued demo"]);
    expect(requests.some((r) => r.pathname === "/api/v1/sessions" && !r.search)).toBe(false);
  });

  it("retains full-list behavior on an older hub", async () => {
    hello(["sessions_delta"]);
    await settle();
    expect(getState().sessions.demo.map((s) => s.session_id)).toEqual(["legacy"]);
    expect(getState().sessionSummaries).toEqual([]);
    expect(requests.some((r) => r.pathname === "/api/v1/sessions" && !r.search)).toBe(true);
  });
});

describe("summary-aware background state", () => {
  it("uses explicit waiting false rather than stale arrays", () => {
    expect(isWaiting(session("s", { waiting: false, background: [{ id: "old", kind: "shell" }] }))).toBe(false);
    expect(isWaiting(session("s", { waiting: true }))).toBe(true);
    expect(isWaiting(session("s", { waiting: true, state: "working" }))).toBe(false);
  });
  it("uses compact background/workflow/subagent counts without needing arrays", () => {
    const s = session("s", { waiting: true, background_count: 2, running_workflows: 1, running_subagents: 1 });
    expect(backgroundCount(s)).toBe(2);
    expect(workflowShortLine(s)).toBe(t("lib-workflows.running_in_background"));
  });
});

describe("archive failure rollback with negotiated lists", () => {
 it.each([
  ["delta", ["sessions_delta"], "legacy"],
  ["paged", ["sessions_list", "sessions_delta", "sessions_lookup"], "s0"],
 ] as const)("restores the removed detail immediately with a %s hub and real refresh", async (_, features, id) => {
  hello([...features]);
  await settle();
  const snapshot = session(id, { title:"Waiting for confirmation", status:"blocked", state:"needs_you", waiting:undefined });
  upsertSession(snapshot);
  const summaries = getState().sessionSummaries;
  let reject!: (error: Error) => void;
  const operation = archiveAgent(snapshot,true,{
   ...appArchiveDeps,
   archive:() => new Promise((_,fail) => { reject=fail; }),
   notify:() => {},
  });
  expect(getState().sessions.demo).toEqual([]);
  reject(new Error("close refused"));
  await settle();
  expect(await operation).toEqual({ok:false});
  expect(getState().sessions.demo).toEqual([snapshot]);
  expect(getSession("demo",id)?.title).toBe("Waiting for confirmation");
  if (features.some(feature => feature === "sessions_list")) {
   expect(getState().sessionSummaries).toEqual(summaries);
   expect(requests.some(r => r.pathname === "/api/v1/sessions" && !r.search)).toBe(false);
  }
 });

 it("does not replace a newer pushed detail when a failed archive rolls back", async () => {
  hello(["sessions_list", "sessions_lookup"]);
  await settle();
  const snapshot = session("s0",{title:"Before close",waiting:undefined});
  upsertSession(snapshot);
  let reject!: (error:Error) => void;
  const operation = archiveAgent(snapshot,true,{...appArchiveDeps,archive:() => new Promise((_,fail) => {reject=fail;}),notify:() => {}});
  const newer: Session = {...snapshot,title:"New work",status:"working",state:"working"};
  upsertSession(newer);
  reject(new Error("close refused"));
  await settle();
  expect(await operation).toEqual({ok:false});
  expect(getState().sessions.demo).toEqual([newer]);
 });

  it("tracks hello.push_grading and push_grading events and subtracts held_needs from badge only while grading is on (#11771)", async () => {
    scopedNeeds = 3;
    scopedHeld = 2;
    connect();
    Socket.last.push({ type: "hello", features: ["sessions_list", "sessions_lookup", "sessions_hierarchy", "sessions_agents_only"], push_grading: false });
    await settle();
    expect(getState().pushGrading).toBe(false);
    expect(countsShown()).toMatchObject({ needs: 3, badge: 3 });

    Socket.last.push({ type: "push_grading", enabled: true });
    expect(getState().pushGrading).toBe(true);
    expect(countsShown()).toMatchObject({ needs: 3, badge: 1 });

    // A P1 interaction update refreshes bounded counts so held_needs updates without a session row change.
    const countsBefore = requests.filter((r) => r.searchParams.has("counts")).length;
    scopedHeld = 3;
    Socket.last.push({ type: "interaction", interaction: { id: "ix_p1", machine_id: "demo", session_id: "s1", kind: "permission", push_level: "P1" } });
    await settleThrottled();
    expect(requests.filter((r) => r.searchParams.has("counts")).length).toBeGreaterThan(countsBefore);
    expect(countsShown()).toMatchObject({ needs: 3, badge: 0 });

    Socket.last.push({ type: "push_grading", enabled: false });
    expect(countsShown()).toMatchObject({ needs: 3, badge: 3 });
  });
});
