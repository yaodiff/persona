// @vitest-environment jsdom
// Sidebar alignment and visual polish (#14892):
// 1. Desktop More row hides the visible numeric count while keeping aria-label / title and a subtle right chevron.
// 2. The paged session tally row ("N sessions") shares the px-2 horizontal rail with group headers and session rows.
// 3. Group header counts use a 16px right-aligned tabular slot (min-w-4 text-right tabular-nums), matching SourceTag.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../i18n";
import { buildMachineTree } from "../lib/herdr-tree";
import { setSidebarCollapsed } from "../lib/layout";
import { resetSidebarPrefs, setPageShown } from "../lib/sidebar-pages";
import { connect, reset } from "../lib/store";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";
import { GroupCount } from "./SidebarSection";
import { MachineSection, SessionTally } from "./SidebarTree";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
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

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
const classes = (el: Element | null | undefined) => el?.className.split(/\s+/) ?? [];
const session = (id: string, extra: Partial<Session> = {}): Session => ({
  machine_id: "m1",
  session_id: id,
  title: `Session ${id}`,
  cwd: "/code/demo",
  status: "idle",
  state: "idle",
  updated_at: "2026-01-01T00:00:00Z",
  ...extra,
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  localStorage.clear();
  reset();
  setSidebarCollapsed(false);
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("fetch", vi.fn(() => json({})));
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetSidebarPrefs();
  vi.unstubAllGlobals();
});

describe("sidebar alignment (#14892)", () => {
  it("keeps the desktop More row free of a visible count while preserving aria-label, title, and chevron", async () => {
    setPageShown("kb", false);
    setPageShown("routines", false);
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    const more = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("sidebar.more_pages")))!;
    expect(more).toBeDefined();
    expect(more.textContent?.trim()).toBe(t("sidebar.more_pages"));
    expect(more.textContent).not.toMatch(/\d/);
    const expectedLabel = t("sidebar.more_pages_count", { count: 2 });
    expect(more.getAttribute("aria-label")).toBe(expectedLabel);
    expect(more.getAttribute("title")).toBe(expectedLabel);
    expect(more.querySelectorAll("svg").length).toBe(2);
  });

  it("aligns the paged session tally row with group headers and session rows via px-2", async () => {
    const agents = [session("a1"), session("a2"), session("a3")];
    vi.stubGlobal("fetch", vi.fn((input: string) => {
      const url = new URL(input, "http://hub.example.com");
      const q = url.searchParams;
      if (url.pathname === "/api/v1/sessions") {
        if (q.has("counts")) return json({ total: 3, state: { idle: 3 }, machine: { m1: 3 }, group: {}, project: {}, revision: "r1" });
        if (q.has("since")) return json({ sessions: [], remove: [], has_more: false, revision: "r1", total: 3 });
        return json({ sessions: agents, remove: [], has_more: false, revision: "r1", total: 3 });
      }
      if (url.pathname === "/api/v1/machines") return json({ machines: [{ id: "m1", name: "Mac", online: true }] });
      if (url.pathname === "/api/v1/pins") return json({ sessions: [], spaces: [] });
      if (url.pathname === "/api/v1/todos") return json({ todos: [] });
      return json({});
    }));

    // By status reads the complete list for AgentView (#16330). By computer still uses the paged list.
    localStorage.setItem("persona.agentGrouping", "machine");
    act(() => {
      connect();
      const socket = FakeSocket.all.at(-1)!;
      socket.open();
      socket.push({ type: "hello", features: ["sessions_list", "sessions_lookup", "sessions_todo", "sessions_agents_only"] });
    });
    await flush();

    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    const totalEl = host.querySelector<HTMLElement>("[data-session-total]")!;
    expect(totalEl).not.toBeNull();
    expect(totalEl.textContent).toBe(t("paged-agents.count", { count: 3 }));
    expect(classes(totalEl.parentElement)).toEqual(expect.arrayContaining(["-mx-1", "px-2"]));
  });

  it("gives group header counts a 16px right-aligned tabular slot matching trailing row icons", () => {
    const machines: Machine[] = [{ id: "m1", name: "Mac", online: true }];
    const sessions = [
      session("s1", { space_id: "sp1", space_name: "Workspace", slot_id: "tab1", slot_name: "#1", source_kind: "terminal" }),
      session("s2", { space_id: "sp1", space_name: "Workspace", slot_id: "tab2", slot_name: "#2", source_kind: "terminal" }),
    ];
    const [tree] = buildMachineTree(machines, sessions);
    const noop = () => {};

    act(() => root.render(
      <>
        <GroupCount n={2} label={t("sidebar.agent_count", { count: 2 })} />
        <SessionTally sessions={sessions} folded={false} />
        <MachineSection m={tree!} machines={machines} reg={() => noop} hover={noop} activeKey="" go={() => noop} mutes={{ spaces: [], sessions: [] }} onSpaceMenu={noop} onRowMenu={noop} onNodeMenu={noop} />
      </>,
    ));

    const counts = [...host.querySelectorAll<HTMLElement>("span.tabular-nums")].filter((el) => el.getAttribute("title")?.includes("2"));
    expect(counts.length).toBeGreaterThanOrEqual(3);
    for (const el of counts) {
      expect(classes(el)).toEqual(expect.arrayContaining(["inline-block", "min-w-4", "text-right", "tabular-nums"]));
    }
  });
});
