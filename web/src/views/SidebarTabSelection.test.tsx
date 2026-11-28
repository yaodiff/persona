// @vitest-environment jsdom
// A herdr tab outside the first summary page still lights its row in the sidebar on a paging hub (#13708): clicking a
// row stores it in `sessions[mid]` and redirects `/s/` to `/t/`, and a direct `/t/<mid>/<space>/<slot>/<sid>` load
// keeps the named pane selected even before exact session hydration completes. The data is made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { loadPages } from "../lib/lazy-page";
import type { Route } from "../lib/router";
import { SESSIONS_AGENTS_ONLY_FEATURE } from "../lib/runtime-list";
import { SESSIONS_CATALOG_FEATURE } from "../lib/session-groups";
import { listPair } from "../lib/session-list";
import { connect, reset, upsertSession } from "../lib/store";
import type { Session } from "../lib/types";
import { Sidebar } from "./Sidebar";
import { CURRENT_ROW, useActiveKey } from "./SidebarParts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  constructor() { FakeSocket.all.push(this); }
  send() {}
  close() { this.readyState = 3; }
  open() { this.readyState = 1; this.onopen?.(); }
  push(msg: Record<string, unknown>) { this.onmessage?.({ data: JSON.stringify(msg) }); }
}

const CLAUDE_ROW: Session = {
  machine_id: "laptop",
  session_id: "claude-1",
  agent: "claude",
  title: "Claude task",
  cwd: "/code/app",
  status: "idle",
  state: "idle",
  updated_at: "2026-11-23T10:00:00Z",
  space_id: "w1",
  space_name: "app",
  space_kind: "herdr",
  slot_id: "w1:t1",
};

const AGY_ROW: Session = {
  machine_id: "laptop",
  session_id: "agy-w2-p1",
  agent: "agy",
  title: "Antigravity task",
  cwd: "/code/tools",
  status: "working",
  state: "working",
  space_id: "w2",
  space_name: "tools",
  space_kind: "herdr",
  slot_id: "w2:t1",
};

const ALL_ROWS: Session[] = [CLAUDE_ROW, AGY_ROW];
const MACHINES = [{ id: "laptop", name: "Work laptop", online: true }];

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
const flush = async () => { for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); }); };

let host: HTMLDivElement;
let root: Root;

/** Simulates a paging hub whose first summary page holds only CLAUDE_ROW, while space queries return AGY_ROW too. */
function answerSessions(url: URL) {
  const q = url.searchParams;
  const spaces = q.getAll("space");
  const scoped = ALL_ROWS.filter((s) => !spaces.length || spaces.includes(listPair(s.machine_id, s.space_id ?? "")));
  const tally = (key: (s: Session) => string) => scoped.reduce<Record<string, number>>((out, s) => ({ ...out, [key(s)]: (out[key(s)] ?? 0) + 1 }), {});
  if (q.has("catalog")) {
    const map = new Map<string, { machine_id: string; id: string; name: string; path?: string; total: number }>();
    for (const s of scoped) {
      const pair = listPair(s.machine_id, s.space_id ?? "");
      const entry = map.get(pair) ?? { machine_id: s.machine_id, id: s.space_id ?? "", name: s.space_name ?? "", path: s.cwd, total: 0 };
      entry.total++;
      map.set(pair, entry);
    }
    return json({ revision: "r1", total: scoped.length, projects: [], spaces: [...map.values()] });
  }
  if (q.has("counts")) {
    return json({ total: scoped.length, revision: "r1", state: tally((s) => s.state ?? ""), machine: tally((s) => s.machine_id), group: tally((s) => s.space_id ?? ""), project: tally((s) => s.cwd) });
  }
  // Unscoped first page returns only CLAUDE_ROW with has_more=true; section queries return the space's own rows.
  const rows = spaces.length ? scoped : [CLAUDE_ROW];
  return json({ sessions: rows, remove: [], has_more: !spaces.length, revision: "r1", total: scoped.length });
}

async function bootPagedHub() {
  localStorage.setItem("persona.agentGrouping", "space");
  vi.stubGlobal("fetch", vi.fn((input: string) => {
    const url = new URL(input, "http://hub.example.com");
    if (url.pathname.endsWith("/sessions")) return answerSessions(url);
    if (url.pathname.endsWith("/pins")) return json({ sessions: [], spaces: [] });
    if (url.pathname.endsWith("/machines")) return json({ machines: MACHINES });
    if (url.pathname.endsWith("/todos")) return json({ todos: [] });
    return json({});
  }));
  act(() => {
    connect();
    const socket = FakeSocket.all.at(-1)!;
    socket.open();
    socket.push({ type: "hello", features: ["sessions_list", "sessions_lookup", SESSIONS_AGENTS_ONLY_FEATURE, SESSIONS_CATALOG_FEATURE] });
  });
  await flush();
}

beforeAll(() => loadPages(), 60_000);

beforeEach(() => {
  localStorage.clear();
  reset();
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
});

describe("sidebar selection for /t/ routes outside sessionSummaries (#13708)", () => {
  const rowLink = (sid: string) => host.querySelector<HTMLAnchorElement>(`[data-sidebar-rows] a[href$="/s/laptop/${sid}"]`);
  const agyTabRoute: Route = { name: "tab", mid: "laptop", space: "w2", slot: "w2:t1", sid: "agy-w2-p1" };

  it("keeps the selected state when a clicked herdr pane is in store.sessions but not in sessionSummaries", async () => {
    await bootPagedHub();
    act(() => upsertSession(AGY_ROW));
    act(() => root.render(<Sidebar route={agyTabRoute} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    const agy = rowLink("agy-w2-p1");
    const claude = rowLink("claude-1");
    expect(agy).not.toBeNull();
    expect(agy!.getAttribute("aria-current")).toBe("page");
    expect(agy!.className).toContain(CURRENT_ROW);
    expect(claude?.getAttribute("aria-current")).toBeNull();
  });

  it("lights the named pane on a direct /t/ load even before store.sessions is hydrated", async () => {
    await bootPagedHub();
    act(() => root.render(<Sidebar route={agyTabRoute} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    const agy = rowLink("agy-w2-p1");
    expect(agy).not.toBeNull();
    expect(agy!.getAttribute("aria-current")).toBe("page");
    expect(agy!.className).toContain(CURRENT_ROW);
  });

  it("resolves bare /t/ routes from cached machine sessions, falls back for stale pane IDs, and keeps drill-down/work routes intact", async () => {
    await bootPagedHub();
    let seen = "";
    let renders = 0;
    function Probe({ route }: { route: Route }) {
      renders++;
      seen = useActiveKey(route);
      return null;
    }

    // Bare /t/ without sid: empty until cached, then resolves from store.sessions[mid].
    const bareAgy: Route = { name: "tab", mid: "laptop", space: "w2", slot: "w2:t1" };
    act(() => root.render(<Probe route={bareAgy} />));
    expect(seen).toBe("");
    act(() => upsertSession(AGY_ROW));
    expect(seen).toBe("s:laptop/agy-w2-p1");

    // Unrelated session update leaves the active key unchanged and skips re-rendering the consumer.
    const before = renders;
    act(() => upsertSession({ ...CLAUDE_ROW, title: "Updated title" }));
    expect(renders).toBe(before);

    // Stale pane ID in a live tab resolves to that tab's live pane (matching resolveTab).
    act(() => root.render(<Probe route={{ name: "tab", mid: "laptop", space: "w1", slot: "w1:t1", sid: "gone-pane" }} />));
    expect(seen).toBe("s:laptop/claude-1");

    // Work view keeps lighting "terminal".
    act(() => root.render(<Probe route={{ ...agyTabRoute, work: true }} />));
    expect(seen).toBe("terminal");

    // Drill-down from another page keeps lighting the origin page instead of the tab.
    history.replaceState({ handoff: { path: "/todos?id=t1" } }, "", "/t/laptop/w2/w2%3At1/agy-w2-p1");
    act(() => root.render(<Probe route={agyTabRoute} />));
    expect(seen).toBe("todos");
  });
});
