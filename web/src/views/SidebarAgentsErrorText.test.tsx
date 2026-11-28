// @vitest-environment jsdom
// The complete Agents list says "Can't reach the hub" only when its read never got an answer. A read that answered but
// whose rows the page could not process (a TypeError in its own code: a row with a field it did not expect) says what
// broke, so the person and an error report see the real fault, not a network gap Retry can never close.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { seedAgentsDisplay } from "../lib/agents-display";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// A row the page's own code trips over while it takes in the complete list's answer (lib/sessions.ts): the read's first
// step after the answer hands the rows to the launch handoff.
const PROGRAM_ERROR = "Cannot read properties of undefined (reading 'trim')";
vi.mock("../lib/launch-handoff", async (original) => {
  const actual = await original<typeof import("../lib/launch-handoff")>();
  return { ...actual, observeLaunchRows: (rows: readonly { session_id: string }[]) => {
    if (rows.some((s) => s.session_id === "malformed")) throw new TypeError(PROGRAM_ERROR);
    actual.observeLaunchRows(rows as Parameters<typeof actual.observeLaunchRows>[0]);
  } };
});
import { t } from "../i18n";
import { connect, getState, reset } from "../lib/store";
import type { Todo } from "../lib/todos";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
  static OPEN = 1;
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
  }
  drop() {
    this.readyState = 3;
    this.onclose?.({ code: 1006 });
  }
}
const last = () => FakeSocket.all[FakeSocket.all.length - 1];

const mac: Machine = { id: "mac", name: "工作电脑", online: true };
const agent: Session = { machine_id: "mac", session_id: "s1", title: "改发布脚本", cwd: "/code/weather", status: "working", state: "working", updated_at: "2026-11-05T00:00:00Z" };
const todo: Todo = { id: "td1", title: "天气 App 发 1.3 版", created_at: "2026-11-05T00:00:00Z", updated_at: "2026-11-05T00:00:00Z", agents: [{ machine_id: "mac", session_id: "s1", linked_at: "2026-11-05T00:00:00Z" }] };

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
const counts = { total: 1, revision: "r1", state: { working: 1 }, machine: { mac: 1 }, project: {}, group: {}, todo: { td1: 1 } };
const agentsResponse = (query: URLSearchParams) => query.get("counts") === "1"
  ? json(counts)
  : json({ sessions: [agent], total: 1, has_more: false, revision: "r1",
      ...(query.get("with_counts") === "1" ? { counts } : {}) });

/** The complete and paged lists share the route. A counts-only read uses its own response shape. */
const hub = (o: { todos?: () => Promise<Response>; sessions?: (query: URLSearchParams) => Promise<Response>; pinned?: boolean }) =>
  vi.fn((url: string) => {
    const { pathname: path, searchParams } = new URL(url, "http://hub");
    if (path === "/api/v1/todos") return (o.todos ?? (() => json({ todos: [todo] })))();
    if (path === "/api/v1/sessions") return (o.sessions ?? agentsResponse)(searchParams);
    if (path === "/api/v1/machines") return json({ machines: [mac] });
    // a pin keeps the complete list on a paging hub (lib/sidebar-api.ts pagedAgentList)
    if (path === "/api/v1/pins" && o.pinned) return json({ sessions: ["mac/s1"], spaces: [] });
    return json({});
  });

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); await new Promise((r) => setTimeout(r, 0)); });
};
const grouping = () => host.querySelector<HTMLElement>('[role="radiogroup"][aria-label="Agent 分组"]')!;
const agentsBlock = () => grouping().closest<HTMLElement>("[data-sidebar-agents]")!;
const alerts = () => [...agentsBlock().querySelectorAll('[role="alert"]')].map((el) => el.textContent);
const retryButton = () => [...agentsBlock().querySelectorAll("button")].find((b) => b.textContent === t("sidebar.retry"));
const render = async () => {
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
};
/** Connects, says hello with `features` and pushes the computer and its agent. */
const online = (features: string[] = []) => {
  connect();
  const sock = last();
  sock.open();
  sock.push({ type: "hello", server_version: "v1", features });
  sock.push({ type: "machines", machines: [mac] });
  sock.push({ type: "sessions", machine_id: "mac", sessions: [agent] });
};

beforeEach(() => {
  localStorage.clear();
  reset();
  // Settings › Agents: the Filter is on for these tests (it is off until the hub says so)
  seedAgentsDisplay({ show_botmux: true, configured: true, show_filter: true });
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  // the reads this test started answer before the next one starts
  await flush();
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("the complete Agents list's failure text", () => {
  const unreachable = t("lib-api.cant_reach_hub");
  const malformed: Session = { ...agent, session_id: "malformed", title: "坏的一行" };

  it("says Can't reach the hub when the read never got an answer", async () => {
    let sessions: (q: URLSearchParams) => Promise<Response> = () => Promise.reject(new TypeError("Failed to fetch"));
    vi.stubGlobal("fetch", hub({ sessions: (q) => q.get("counts") === "1" ? json(counts) : sessions(q), pinned: true }));
    online(["sessions_list"]);
    await render();
    expect(getState().pins.sessions).toEqual(["mac/s1"]);
    const [said] = alerts();
    expect(said).toContain(t("sidebar.agents_read_failed", { error: unreachable }));
    sessions = agentsResponse;
    await act(async () => retryButton()!.click());
    await flush();
    expect(alerts()).toEqual([]);
  });

  it("shows the page's own TypeError, not Can't reach the hub, when the hub answered", async () => {
    vi.stubGlobal("fetch", hub({ pinned: true, sessions: (q) => q.get("counts") === "1" ? json({ ...counts, total: 2 })
      : json({ sessions: [agent, malformed], total: 2, has_more: false, revision: "r1" }) }));
    online(["sessions_list"]);
    await render();
    const [said] = alerts();
    expect(said).toContain(t("sidebar.agents_read_failed", { error: t("lib-api.page_error", { detail: PROGRAM_ERROR }) }));
    expect(said).not.toContain(unreachable);
  });
});
