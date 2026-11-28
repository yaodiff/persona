// @vitest-environment jsdom
// Settings › Agents: "Sort Agents by recent activity" and "Show the Filter on the Agents list". The list keeps the stable
// order of #11801 until the sort is on; on, it orders by activity but holds still while the pointer is over the sidebar.
// The Filter is drawn on both the complete and the paged list only while its switch is on; a choice the hub's paged read
// cannot express is answered by the complete list. The ⋯ menu carries both choices directly, writing the same hub setting
// Settings › Agents shows (#14398).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../i18n";
import { seedAgentsDisplay } from "../lib/agents-display";
import { resetAgentListFilters, filterAgentList, getAgentListFilters } from "../lib/agent-list-filters";
import { connect, refresh, reset, upsertSession } from "../lib/store";
import type { Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

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
const flush = async () => { for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); }); };
let host: HTMLDivElement, root: Root;
const sidebar = () => <Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />;
const agentsText = () => host.querySelector("[data-sidebar-agents]")?.textContent ?? "";
const filterButton = () => host.querySelector<HTMLButtonElement>('[data-testid="agent-filter-button"]');
const menuRow = (label: string) => [...document.querySelectorAll<HTMLElement>('[role^="menuitem"]')].find((el) => el.textContent?.includes(label));
const display = (patch: { sort_by_activity?: boolean; show_filter?: boolean }) => seedAgentsDisplay({ show_botmux: true, configured: true, ...patch });

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  reset();
  resetAgentListFilters();
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

describe("the complete list's order", () => {
  const agent = (machine: string, id: string, title: string, at: string): Session => ({ machine_id: machine, session_id: id, title, cwd: `/code/${id}`, status: "idle", state: "idle", updated_at: at });
  const hub = async (sessions: Session[]) => {
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      const path = new URL(url, "http://x").pathname;
      return json(path === "/api/v1/sessions" ? { sessions } : path === "/api/v1/machines" ? { machines: [{ id: "mac", name: "工作电脑", online: true }, { id: "box", name: "家里电脑", online: true }] } : {});
    }));
    await act(async () => { await refresh(); });
  };
  const before = [agent("mac", "a", "旧会话", "2026-01-01T01:00:00Z"), agent("box", "b", "新会话", "2026-01-01T02:00:00Z")];
  const touched = [agent("mac", "a", "旧会话", "2026-01-01T03:00:00Z"), before[1]!];
  const order = () => { const text = agentsText(); return text.indexOf("工作电脑") < text.indexOf("家里电脑") ? "mac first" : "box first"; };
  const hover = (on: boolean) => act(() => { host.querySelector("[data-sidebar-root]")!.dispatchEvent(new PointerEvent(on ? "pointerenter" : "pointerleave", { pointerType: "mouse" })); });

  it("is the hub's computer order until the sort is on, however recent the activity", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    localStorage.setItem("persona.agentSort", "updated"); // the old per-browser key means nothing now
    display({ sort_by_activity: false });
    await hub(before);
    act(() => root.render(sidebar()));
    await flush();
    expect(order()).toBe("mac first");
  });

  it("orders computers by their latest agent when on, and holds still while the pointer is over the sidebar", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    display({ sort_by_activity: true });
    await hub(before);
    act(() => root.render(sidebar()));
    await flush();
    expect(order()).toBe("box first");
    hover(true);
    act(() => upsertSession(touched[0]!)); // the first computer's agent is now the most recent
    await flush();
    expect(order()).toBe("box first");
    expect(agentsText()).toContain("旧会话"); // rows still update in place
    hover(false);
    await flush();
    expect(order()).toBe("mac first");
  });

  it("holds still while keyboard focus is in the sidebar, and moves once it leaves", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    display({ sort_by_activity: true });
    await hub(before);
    act(() => root.render(sidebar()));
    await flush();
    const button = host.querySelector<HTMLButtonElement>("[data-sidebar-agents] button")!;
    act(() => { button.focus(); });
    act(() => upsertSession(touched[0]!));
    await flush();
    expect(order()).toBe("box first");
    act(() => { button.blur(); });
    await flush();
    expect(order()).toBe("mac first");
  });
});

describe("the Filter switch", () => {
  const sessions: Session[] = [{ machine_id: "mac", session_id: "a", title: "旧会话", cwd: "/code/a", status: "idle", state: "idle", updated_at: "2026-01-01T01:00:00Z" }];
  const setup = async () => {
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      const path = new URL(url, "http://x").pathname;
      return json(path === "/api/v1/sessions" ? { sessions } : path === "/api/v1/machines" ? { machines: [{ id: "mac", name: "工作电脑", online: true }] } : {});
    }));
    await act(async () => { await refresh(); });
    act(() => root.render(sidebar()));
    await flush();
  };

  it("draws no Filter and ignores what it held while the switch is off, and draws it on", async () => {
    display({ show_filter: false });
    filterAgentList("旧会话");
    await setup();
    expect(filterButton()).toBeNull();
    expect(host.querySelector('[data-testid="agent-filter-chips"]')).toBeNull();
    expect(getAgentListFilters().query).toBe(""); // not left behind as a hidden filter
    expect(agentsText()).toContain("全部"); // the quick filters are unaffected
    display({ show_filter: true });
    await flush();
    expect(filterButton()).not.toBeNull();
  });

  it("is hidden too on a hub that has not got the switch", async () => {
    seedAgentsDisplay({ show_botmux: true, configured: true });
    await setup();
    expect(filterButton()).toBeNull();
  });
});

describe("the paged list's Filter", () => {
  const row = (id: string, machine: string): Session => ({ machine_id: machine, session_id: id, title: id, cwd: "/code/demo", status: "idle", state: "idle", updated_at: id === "beta" ? "2026-01-02T00:00:00Z" : "2026-01-01T00:00:00Z" });
  let urls: string[];
  const setup = async (newestFirst = false) => {
    urls = [];
    vi.stubGlobal("fetch", vi.fn((input: string) => {
      const url = new URL(input, "http://hub.example.com");
      if (url.pathname.endsWith("/sessions") && !url.pathname.includes("/agents/")) {
        urls.push(url.search);
        const only = url.searchParams.get("machine_id");
        const all = newestFirst ? [row("beta", "two"), row("alpha", "one")] : [row("alpha", "one"), row("beta", "two")];
        const rows = only ? all.filter((r) => r.machine_id === only) : all;
        if (url.searchParams.has("counts") && !url.searchParams.has("since")) return json({ total: rows.length, state: { working: rows.length }, machine: Object.fromEntries(rows.map((r) => [r.machine_id, 1])), group: {}, project: {}, revision: "r1" });
        return json({ sessions: rows, remove: [], added: [], has_more: false, revision: "r1", total: rows.length });
      }
      if (url.pathname.endsWith("/machines")) return json({ machines: [{ id: "one", name: "第一台", online: true }, { id: "two", name: "第二台", online: true }] });
      if (url.pathname.endsWith("/pins")) return json({ sessions: [], spaces: [] });
      if (url.pathname.endsWith("/todos")) return json({ todos: [] });
      return json({});
    }));
    act(() => {
      connect();
      const socket = FakeSocket.all.at(-1)!;
      socket.open();
      socket.push({ type: "hello", features: ["sessions_list", "sessions_lookup", "sessions_todo", "sessions_agents_only"] });
    });
    await flush();
    act(() => root.render(sidebar()));
    await flush();
  };
  const click = async (el: Element | null | undefined) => { await act(async () => { (el as HTMLElement).click(); }); await flush(); };

  it("is on the title row and narrows the paged read by the computer it names, with its chip", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    display({ show_filter: true });
    await setup();
    expect(host.querySelector("[data-paged-sessions]")).not.toBeNull();
    expect(filterButton()).not.toBeNull();
    await click(filterButton());
    await click(menuRow(t("lib-agent-filter.filter_machine")));
    expect(menuRow("第二台")).toBeDefined();
    await click(menuRow("第二台"));
    expect(urls.some((u) => u.includes("machine_id=two"))).toBe(true);
    expect(host.querySelector('[data-chip="machines:two"]')?.textContent).toContain("第二台");
    expect(host.querySelector("[data-paged-sessions]")).not.toBeNull(); // still the paged list
    await click(host.querySelector('[data-testid="scoped-filter-reset"]'));
    expect(host.querySelector('[data-chip="machines:two"]')).toBeNull();
  });

  it("falls back to the complete list for a choice the hub cannot express, and back again when it is cleared", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    display({ show_filter: true });
    await setup();
    await click(filterButton());
    await click(menuRow(t("lib-agent-filter.filter_pinned")));
    expect(host.querySelector("[data-paged-sessions]")).toBeNull();
    expect(host.querySelector('[data-chip="pinned"]')).not.toBeNull();
    await click(host.querySelector('[data-testid="scoped-filter-reset"]'));
    expect(host.querySelector("[data-paged-sessions]")).not.toBeNull();
  });

  it("has no Filter on the paged list while the switch is off", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    display({ show_filter: false });
    await setup();
    expect(host.querySelector("[data-paged-sessions]")).not.toBeNull();
    expect(filterButton()).toBeNull();
  });

  const names = () => { const text = agentsText(); return text.indexOf("第一台") < text.indexOf("第二台") ? "first computer first" : "second computer first"; };
  // both names drawn before their order means anything: with neither drawn, names() would report "second computer first"
  const drawn = () => vi.waitFor(async () => {
    await flush();
    expect(agentsText()).toContain("第一台");
    expect(agentsText()).toContain("第二台");
  });

  it("keeps the hub's computer order on the paged list, though the newest session is on the second computer", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    await setup(true);
    display({ sort_by_activity: false });
    await flush();
    expect(host.querySelector("[data-paged-sessions]")).not.toBeNull();
    await drawn();
    expect(names()).toBe("first computer first");
  });

  it("orders the paged list's computers by their newest session when the sort is on", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    await setup(true);
    display({ sort_by_activity: true });
    await flush();
    await drawn();
    expect(names()).toBe("second computer first");
  });
});

describe("the ⋯ menu's direct switches (#14398)", () => {
  const sessions: Session[] = [{ machine_id: "mac", session_id: "a", title: "旧会话", cwd: "/code/a", status: "idle", state: "idle", updated_at: "2026-01-01T01:00:00Z" }];
  const hub = (settings: { sort_by_activity?: boolean; show_filter?: boolean }) => vi.fn((url: string, init?: RequestInit) => {
    const path = new URL(url, "http://x").pathname;
    if (path === "/api/v1/agents/display-settings") return json({ show_botmux: true, configured: true, ...settings, ...(init?.body ? JSON.parse(String(init.body)) : {}) });
    return json(path === "/api/v1/sessions" ? { sessions } : { machines: [{ id: "mac", name: "工作电脑", online: true }] });
  });
  const setup = async (fetched: ReturnType<typeof vi.fn>) => {
    vi.stubGlobal("fetch", fetched);
    await act(async () => { await refresh(); });
    act(() => root.render(sidebar()));
    await flush();
  };
  const openMenu = async () => {
    const button = host.querySelector<HTMLButtonElement>('button[aria-label="Agent 列表选项"]')!;
    await act(async () => button.click());
    await flush();
  };
  const puts = (fetched: ReturnType<typeof vi.fn>) => fetched.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === "PUT");
  // a toggle row (not one of a radio set) draws its check invisibly when off
  const checked = (label: string) => !!menuRow(label)?.querySelector('svg.invisible') === false;

  it("offers both choices, checked as they are, and writes the hub setting on a click", async () => {
    const fetched = hub({ sort_by_activity: false, show_filter: false });
    await setup(fetched);
    await openMenu();
    expect(checked(t("sidebar.sort_activity"))).toBe(false);
    expect(checked(t("sidebar.show_list_filter"))).toBe(false);

    await act(async () => menuRow(t("sidebar.sort_activity"))!.click());
    await flush();
    expect(JSON.parse(String((puts(fetched).at(-1)?.[1] as RequestInit).body))).toEqual({ sort_by_activity: true });
    expect(checked(t("sidebar.sort_activity"))).toBe(true); // the menu stays open

    await act(async () => menuRow(t("sidebar.show_list_filter"))!.click());
    await flush();
    expect(JSON.parse(String((puts(fetched).at(-1)?.[1] as RequestInit).body))).toEqual({ show_filter: true });
    expect(filterButton()).not.toBeNull();
  });

  it("shows neither on a hub that does not have the fields", async () => {
    const fetched = vi.fn((url: string) => {
      const path = new URL(url, "http://x").pathname;
      if (path === "/api/v1/agents/display-settings") return json({ show_botmux: true, configured: true });
      return json(path === "/api/v1/sessions" ? { sessions } : { machines: [{ id: "mac", name: "工作电脑", online: true }] });
    });
    await setup(fetched);
    const menu = host.querySelector<HTMLButtonElement>('button[aria-label="Agent 列表选项"]');
    if (menu) {
      await openMenu();
      expect(menuRow(t("sidebar.sort_activity"))).toBeUndefined();
      expect(menuRow(t("sidebar.show_list_filter"))).toBeUndefined();
    }
  });
});
