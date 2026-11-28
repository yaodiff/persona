// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { DEFAULT_DIMENSION_FILTERS } from "../lib/agent-chatlist-filter";
import { filterAgentList, resetAgentListFilters, setAgentListFilters } from "../lib/agent-list-filters";
import { seedAgentsDisplay } from "../lib/agents-display";
import { SESSIONS_AGENTS_ONLY_FEATURE } from "../lib/runtime-list";
import { connect, getState, reset } from "../lib/store";
import type { Session } from "../lib/types";
import { AgentsList, Sidebar } from "./Sidebar";

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

const agent = (id: string): Session => ({
  machine_id: "computer",
  session_id: id,
  title: id,
  cwd: "/code/demo",
  status: "working",
  state: "working",
  updated_at: "2026-01-01T00:00:00Z",
  space_id: "project",
  space_name: "Demo project",
});

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};

const setupFetch = (herdrRunning = true, countsOverride?: Record<string, unknown>) => {
  const rows = [agent("First page agent")];
  const fetchMock = vi.fn((input: string) => {
    const url = new URL(input, "http://hub.example.com");
    if (url.pathname.endsWith("/sessions") && !url.pathname.includes("/agents/")) {
      const q = url.searchParams;
      if (q.has("counts")) {
        return json(countsOverride ?? { total: rows.length, state: { working: rows.length }, machine: { computer: rows.length }, group: {}, project: {}, revision: "r1" });
      }
      return json({ sessions: rows, remove: [], has_more: false, revision: "r1", total: rows.length });
    }
    if (url.pathname.endsWith("/pins")) return json({ sessions: [], spaces: [] });
    if (url.pathname.endsWith("/machines")) {
      return json({ machines: [{ id: "computer", name: "Demo computer", online: true, herdr_running: herdrRunning }] });
    }
    if (url.pathname.endsWith("/todos")) return json({ todos: [] });
    return json({});
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
};

const connectPagedHub = async () => {
  act(() => {
    connect();
    const socket = FakeSocket.all.at(-1)!;
    socket.open();
    socket.push({ type: "hello", features: ["sessions_list", "sessions_lookup", "sessions_todo", SESSIONS_AGENTS_ONLY_FEATURE] });
  });
  await flush();
};

const agentsBlock = () => host.querySelector<HTMLElement>("[data-sidebar-agents]")!;
const agentsFoldButton = () => [...agentsBlock().querySelectorAll<HTMLButtonElement>(":scope > div > button[aria-expanded]")].find((b) => b.textContent?.trim() === "Agents") ?? null;
const pagedList = () => agentsBlock().querySelector("[data-paged-sessions]");
const foldAllButton = () => agentsBlock().querySelector<HTMLButtonElement>("[data-testid=agents-fold-all]");

beforeEach(() => {
  localStorage.clear();
  resetAgentListFilters();
  reset();
  seedAgentsDisplay({ show_botmux: true, configured: true, show_filter: true });
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
  resetAgentListFilters();
  vi.unstubAllGlobals();
});

describe("BoundedAgentsBlock folding", () => {
  it("folds and unfolds the whole Agents block when foldable, hiding filter bar, list, and herdr notices", async () => {
    setupFetch(false);
    localStorage.setItem("persona.agentGrouping", "machine");
    await connectPagedHub();
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    const btn = agentsFoldButton()!;
    expect(btn).not.toBeNull();
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(btn.textContent?.trim()).toBe("Agents");
    expect(pagedList()).not.toBeNull();
    expect(agentsBlock().querySelector('[role="radiogroup"][aria-label="Agent 分组"]')).not.toBeNull();
    expect(agentsBlock().textContent).toContain("Demo computer");
    expect(agentsBlock().querySelector('[aria-label="新建 Agent"]')).not.toBeNull();

    // Fold
    act(() => btn.click());
    await flush();
    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("false");
    expect(localStorage.getItem("fold:agents")).toBe("1");
    expect(pagedList()).toBeNull();
    expect(agentsBlock().querySelector('[role="radiogroup"][aria-label="Agent 分组"]')).toBeNull();
    expect(agentsBlock().querySelector('[aria-label="新建 Agent"]')).not.toBeNull();

    // Unfold
    act(() => agentsFoldButton()!.click());
    await flush();
    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("true");
    expect(localStorage.getItem("fold:agents")).toBe("0");
    expect(pagedList()).not.toBeNull();
    expect(agentsBlock().querySelector('[role="radiogroup"][aria-label="Agent 分组"]')).not.toBeNull();
  });

  it("remembers folded state across remount and skips paged session fetches while folded", async () => {
    localStorage.setItem("fold:agents", "1");
    localStorage.setItem("persona.agentGrouping", "machine");
    const fetchMock = setupFetch(true);
    await connectPagedHub();
    fetchMock.mockClear();
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("false");
    expect(pagedList()).toBeNull();
    const sessionCallsWhileFolded = fetchMock.mock.calls.filter(([u]) => new URL(u as string, "http://hub.example.com").pathname.endsWith("/sessions"));
    expect(sessionCallsWhileFolded).toHaveLength(0);

    // Clicking the fold-all button while folded opens the block and loads sessions
    const expandAll = foldAllButton()!;
    expect(expandAll.getAttribute("aria-label")).toBe("全部展开");
    act(() => expandAll.click());
    await flush();

    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("true");
    expect(pagedList()).not.toBeNull();
    const sessionCallsAfterOpen = fetchMock.mock.calls.filter(([u]) => new URL(u as string, "http://hub.example.com").pathname.endsWith("/sessions"));
    expect(sessionCallsAfterOpen.length).toBeGreaterThan(0);
  });

  it("temporarily unfolds when dimFilters.query is non-empty and restores folded state when cleared", async () => {
    localStorage.setItem("fold:agents", "1");
    localStorage.setItem("persona.agentGrouping", "machine");
    setupFetch(true);
    await connectPagedHub();
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("false");
    expect(pagedList()).toBeNull();

    act(() => filterAgentList("First"));
    await flush();
    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("true");
    expect(pagedList()).not.toBeNull();

    expect(localStorage.getItem("fold:agents")).toBe("1");
    act(() => filterAgentList(""));
    await flush();
    // Clearing the query preserves the stored folded state across remount
    act(() => root.unmount());
    root = createRoot(host);
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("false");
    expect(pagedList()).toBeNull();
  });

  it("renders a non-foldable plain title and always shows the body when foldable is false (AgentsList)", async () => {
    localStorage.setItem("fold:agents", "1");
    localStorage.setItem("persona.agentGrouping", "machine");
    setupFetch(true);
    await connectPagedHub();
    act(() => root.render(<AgentsList route={{ name: "agent-list" }} onNewAgent={() => {}} />));
    await flush();

    expect(agentsFoldButton()).toBeNull();
    expect(agentsBlock().textContent).toContain("Agents");
    expect(pagedList()).not.toBeNull();
  });

  it("renders folded StateTally with needs_you, working (including in-flight launches), and unread counts without fetching paged sessions", async () => {
    localStorage.setItem("fold:agents", "1");
    localStorage.setItem("persona.agentGrouping", "machine");
    const fetchMock = setupFetch(true, {
      total: 6,
      state: { needs_you: 2, working: 3, unread: 1 },
      background_waiting: 1,
      machine: { computer: 6 },
      group: {},
      project: {},
      revision: "r1",
    });
    await connectPagedHub();
    Object.assign(getState(), {
      launches: {
        l1: { id: "l1", machineId: "computer", cwd: "/code/demo", launcher: "claude", text: "Starting", phase: "creating", attachments: [], createdAt: Date.now() },
      },
    });
    fetchMock.mockClear();
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("false");
    expect(pagedList()).toBeNull();
    const sessionCallsWhileFolded = fetchMock.mock.calls.filter(([u]) => new URL(u as string, "http://hub.example.com").pathname.endsWith("/sessions"));
    expect(sessionCallsWhileFolded).toHaveLength(0);

    const tally = agentsBlock().querySelector<HTMLElement>("[data-testid=state-tally]");
    expect(tally).not.toBeNull();
    expect(tally!.querySelector("[data-state=needs_you]")?.textContent).toBe("2");
    expect(tally!.querySelector("[data-state=working]")?.textContent).toBe("3");
    expect(tally!.querySelector("[data-state=bg_waiting]")?.textContent).toBe("1");
    expect(tally!.querySelector("[data-state=unread]")?.textContent).toBe("1");

    // Unfolding removes the title-row folded tally
    act(() => agentsFoldButton()!.click());
    await flush();
    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("true");
    expect(agentsBlock().querySelector(":scope > div > [data-testid=state-tally]")).toBeNull();
  });

  it("renders no folded tally when there are no hidden active sessions", async () => {
    localStorage.setItem("fold:agents", "1");
    localStorage.setItem("persona.agentGrouping", "machine");
    setupFetch(true, {
      total: 2,
      state: { idle: 2 },
      machine: { computer: 2 },
      group: {},
      project: {},
      revision: "r1",
    });
    await connectPagedHub();
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("false");
    expect(agentsBlock().querySelector("[data-testid=state-tally]")).toBeNull();
  });

  it("scopes folded StateTally and in-flight launches to the selected machine filter without fetching paged sessions", async () => {
    localStorage.setItem("fold:agents", "1");
    localStorage.setItem("persona.agentGrouping", "machine");
    const fetchMock = vi.fn((input: string) => {
      const url = new URL(input, "http://hub.example.com");
      if (url.pathname.endsWith("/sessions") && !url.pathname.includes("/agents/")) {
        const q = url.searchParams;
        if (q.has("counts")) {
          const mid = q.get("machine_id");
          if (mid === "computer-b") {
            return json({ total: 1, state: { idle: 1 }, machine: { "computer-b": 1 }, group: {}, project: {}, revision: "r1" });
          }
          if (mid === "computer") {
            return json({ total: 2, state: { needs_you: 1, working: 1 }, machine: { computer: 2 }, group: {}, project: {}, revision: "r1" });
          }
          return json({ total: 3, state: { needs_you: 1, working: 1, idle: 1 }, machine: { computer: 2, "computer-b": 1 }, group: {}, project: {}, revision: "r1" });
        }
        return json({ sessions: [agent("First page agent")], remove: [], has_more: false, revision: "r1", total: 3 });
      }
      if (url.pathname.endsWith("/pins")) return json({ sessions: [], spaces: [] });
      if (url.pathname.endsWith("/machines")) {
        return json({
          machines: [
            { id: "computer", name: "Demo computer", online: true, herdr_running: true },
            { id: "computer-b", name: "Quiet computer", online: true, herdr_running: true },
          ],
        });
      }
      if (url.pathname.endsWith("/todos")) return json({ todos: [] });
      return json({});
    });
    vi.stubGlobal("fetch", fetchMock);

    await connectPagedHub();
    Object.assign(getState(), {
      launches: {
        l1: { id: "l1", machineId: "computer", cwd: "/code/demo", launcher: "claude", text: "Starting", phase: "creating", attachments: [], createdAt: Date.now() },
      },
    });

    // Select quiet computer-b: must not leak computer's needs_you/working/launch
    act(() => setAgentListFilters({ ...DEFAULT_DIMENSION_FILTERS, machines: ["computer-b"] }));
    fetchMock.mockClear();
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("false");
    expect(pagedList()).toBeNull();
    expect(agentsBlock().querySelector("[data-testid=state-tally]")).toBeNull();
    const pagedCallsQuiet = fetchMock.mock.calls.filter(([u]) => {
      const url = new URL(u as string, "http://hub.example.com");
      return url.pathname.endsWith("/sessions") && !url.searchParams.has("counts");
    });
    expect(pagedCallsQuiet).toHaveLength(0);

    // Switch filter to computer: shows only computer's counts + matching in-flight launch
    act(() => setAgentListFilters({ ...DEFAULT_DIMENSION_FILTERS, machines: ["computer"] }));
    await flush();

    const tally = agentsBlock().querySelector<HTMLElement>("[data-testid=state-tally]");
    expect(tally).not.toBeNull();
    expect(tally!.querySelector("[data-state=needs_you]")?.textContent).toBe("1");
    expect(tally!.querySelector("[data-state=working]")?.textContent).toBe("2");
    const pagedCallsActive = fetchMock.mock.calls.filter(([u]) => {
      const url = new URL(u as string, "http://hub.example.com");
      return url.pathname.endsWith("/sessions") && !url.searchParams.has("counts");
    });
    expect(pagedCallsActive).toHaveLength(0);
  });

  it("scopes folded StateTally and in-flight launches to active search query q when manually folded during search", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    const fetchMock = vi.fn((input: string) => {
      const url = new URL(input, "http://hub.example.com");
      if (url.pathname.endsWith("/sessions") && !url.pathname.includes("/agents/")) {
        const q = url.searchParams;
        if (q.has("counts")) {
          const query = q.get("q");
          if (query === "alpha") {
            return json({ total: 2, state: { needs_you: 1, working: 1 }, machine: { computer: 2 }, group: {}, project: {}, revision: "r1" });
          }
          if (query === "unrelated") {
            return json({ total: 0, state: {}, machine: {}, group: {}, project: {}, revision: "r1" });
          }
          return json({ total: 5, state: { needs_you: 3, working: 2 }, machine: { computer: 5 }, group: {}, project: {}, revision: "r1" });
        }
        return json({ sessions: [agent("Alpha agent")], remove: [], has_more: false, revision: "r1", total: 1 });
      }
      if (url.pathname.endsWith("/pins")) return json({ sessions: [], spaces: [] });
      if (url.pathname.endsWith("/machines")) {
        return json({ machines: [{ id: "computer", name: "Demo computer", online: true, herdr_running: true }] });
      }
      if (url.pathname.endsWith("/todos")) return json({ todos: [] });
      return json({});
    });
    vi.stubGlobal("fetch", fetchMock);

    await connectPagedHub();
    Object.assign(getState(), {
      launches: {
        lMatch: { id: "lMatch", machineId: "computer", cwd: "/code/alpha", launcher: "claude", text: "alpha launch", phase: "creating", attachments: [], createdAt: Date.now() },
        lOther: { id: "lOther", machineId: "computer", cwd: "/code/beta", launcher: "codex", text: "beta launch", phase: "creating", attachments: [], createdAt: Date.now() },
      },
    });

    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    // Activate search query with surrounding whitespace, then manually fold BoundedAgentsBlock
    act(() => filterAgentList("  alpha  "));
    await flush();
    fetchMock.mockClear();

    act(() => agentsFoldButton()!.click());
    await flush();

    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("false");
    expect(pagedList()).toBeNull();

    const countQueries = fetchMock.mock.calls
      .map(([u]) => new URL(u as string, "http://hub.example.com"))
      .filter((url) => url.pathname.endsWith("/sessions") && url.searchParams.has("counts"))
      .map((url) => url.searchParams.get("q"));
    expect(countQueries).toContain("alpha");

    const pagedCallsWhileFolded = fetchMock.mock.calls.filter(([u]) => {
      const url = new URL(u as string, "http://hub.example.com");
      return url.pathname.endsWith("/sessions") && !url.searchParams.has("counts");
    });
    expect(pagedCallsWhileFolded).toHaveLength(0);

    const tally = agentsBlock().querySelector<HTMLElement>("[data-testid=state-tally]");
    expect(tally).not.toBeNull();
    expect(tally!.querySelector("[data-state=needs_you]")?.textContent).toBe("1");
    // 1 working from counts(q=alpha) + 1 matching in-flight launch (lMatch); lOther is excluded
    expect(tally!.querySelector("[data-state=working]")?.textContent).toBe("2");

    // Updating query to non-matching term while folded must not leak non-matching agents or launches
    fetchMock.mockClear();
    act(() => filterAgentList("unrelated"));
    await flush();

    expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("false");
    expect(agentsBlock().querySelector("[data-testid=state-tally]")).toBeNull();
    const pagedCallsUnrelated = fetchMock.mock.calls.filter(([u]) => {
      const url = new URL(u as string, "http://hub.example.com");
      return url.pathname.endsWith("/sessions") && !url.searchParams.has("counts");
    });
    expect(pagedCallsUnrelated).toHaveLength(0);
  });

  it("scopes folded StateTally and in-flight launches to active quick.select filter without paged session fetches", async () => {
    const runQuickCase = async (
      filterKey: "unread" | "working" | "feishu",
      expectedParams: Record<string, string>,
      countsBody: Record<string, unknown>,
      expectedTally: { needs_you?: string; working?: string; unread?: string },
    ) => {
      act(() => root.unmount());
      reset();
      seedAgentsDisplay({ show_botmux: true, configured: true, show_filter: true });
      localStorage.setItem("fold:agents", "1");
      localStorage.setItem("persona.agentGrouping", "machine");
      localStorage.setItem("persona.agentFilter", filterKey);

      const fetchMock = vi.fn((input: string) => {
        const url = new URL(input, "http://hub.example.com");
        if (url.pathname.endsWith("/sessions") && !url.pathname.includes("/agents/")) {
          if (url.searchParams.has("counts")) return json(countsBody);
          return json({ sessions: [], remove: [], has_more: false, revision: "r1", total: 0 });
        }
        if (url.pathname.endsWith("/pins")) return json({ sessions: [], spaces: [] });
        if (url.pathname.endsWith("/machines")) {
          return json({ machines: [{ id: "computer", name: "Demo computer", online: true, herdr_running: true }] });
        }
        if (url.pathname.endsWith("/todos")) return json({ todos: [] });
        return json({});
      });
      vi.stubGlobal("fetch", fetchMock);

      act(() => {
        connect();
        const socket = FakeSocket.all.at(-1)!;
        socket.open();
        socket.push({
          type: "hello",
          features: ["sessions_list", "sessions_lookup", "sessions_todo", SESSIONS_AGENTS_ONLY_FEATURE, "sessions_quick_filters"],
        });
      });
      await flush();

      Object.assign(getState(), {
        launches: {
          l1: { id: "l1", machineId: "computer", cwd: "/code/demo", launcher: "claude", text: "Starting", phase: "creating", attachments: [], createdAt: Date.now() },
        },
      });
      fetchMock.mockClear();

      root = createRoot(host);
      act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
      await flush();

      expect(agentsFoldButton()!.getAttribute("aria-expanded")).toBe("false");
      expect(pagedList()).toBeNull();

      const countUrls = fetchMock.mock.calls
        .map(([u]) => new URL(u as string, "http://hub.example.com"))
        .filter((url) => url.pathname.endsWith("/sessions") && url.searchParams.has("counts"));
      expect(countUrls.length).toBeGreaterThan(0);
      const lastCountUrl = countUrls.at(-1)!;
      for (const [k, v] of Object.entries(expectedParams)) {
        expect(lastCountUrl.searchParams.get(k)).toBe(v);
      }

      const pagedCalls = fetchMock.mock.calls.filter(([u]) => {
        const url = new URL(u as string, "http://hub.example.com");
        return url.pathname.endsWith("/sessions") && !url.searchParams.has("counts");
      });
      expect(pagedCalls).toHaveLength(0);

      const tally = agentsBlock().querySelector<HTMLElement>("[data-testid=state-tally]")!;
      expect(tally).not.toBeNull();
      expect(tally.querySelector("[data-state=needs_you]")?.textContent).toBe(expectedTally.needs_you);
      expect(tally.querySelector("[data-state=working]")?.textContent).toBe(expectedTally.working);
      expect(tally.querySelector("[data-state=unread]")?.textContent).toBe(expectedTally.unread);
    };

    await runQuickCase(
      "unread",
      { state: "unread" },
      { total: 2, state: { unread: 2 }, machine: { computer: 2 }, group: {}, project: {}, revision: "r1" },
      { unread: "2" },
    );
    await runQuickCase(
      "working",
      { state: "working", waiting: "0" },
      { total: 1, state: { working: 1 }, machine: { computer: 1 }, group: {}, project: {}, revision: "r1" },
      { working: "2" },
    );
    await runQuickCase(
      "feishu",
      { pane_source: "feishu" },
      { total: 1, state: { needs_you: 1 }, machine: { computer: 1 }, group: {}, project: {}, revision: "r1" },
      { needs_you: "1" },
    );
  });
});
