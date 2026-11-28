// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { seedAgentsDisplay } from "../lib/agents-display";
import { resetAgentListFilters } from "../lib/agent-list-filters";
import { stateName } from "../lib/state-labels";
import { connect, reset } from "../lib/store";
import type { PanesResponse } from "../lib/terminals";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

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
}

const mac: Machine = { id: "mac", name: "Workstation", online: true };
const agent: Session = {
  machine_id: "mac",
  session_id: "s1",
  title: "Fix release script",
  cwd: "/code/app",
  status: "working",
  state: "working",
  space_id: "w1",
  space_kind: "herdr",
  space_name: "app",
  slot_id: "w1:t1",
  slot_name: "agent",
  pane_id: "w1:p1",
};
const panes: PanesResponse = {
  hosts: [
    {
      kind: "herdr",
      stream: true,
      control: true,
      new_tab: true,
      workspaces: [
        { id: "w1", label: "app", number: 1, cwd: "/code/app" },
        { id: "w2", label: "ops", number: 2, cwd: "/code/ops" },
      ],
      tabs: [
        { id: "w1:t1", workspace_id: "w1", label: "agent", number: 1 },
        { id: "w1:t2", workspace_id: "w1", label: "shell", number: 2 },
        { id: "w2:t1", workspace_id: "w2", label: "logs", number: 1 },
      ],
      panes: [
        { pane: "w1:p1", terminal: "pane:w1:p1", tab_id: "w1:t1", workspace_id: "w1", agent: "claude", status: "working", session_id: "s1", cwd: "/code/app" },
        { pane: "w1:p2", terminal: "pane:w1:p2", tab_id: "w1:t2", workspace_id: "w1", title: "zsh", cwd: "/code/app" },
        { pane: "w2:p1", terminal: "pane:w2:p1", tab_id: "w2:t1", workspace_id: "w2", title: "htop", cwd: "/code/ops" },
      ],
    },
  ],
};

const json = (body: unknown, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  localStorage.setItem("persona.agentGrouping", "machine");
  reset();
  seedAgentsDisplay({ show_botmux: true, configured: true, show_filter: false });
  resetAgentListFilters();
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    if (url.endsWith("/api/v1/machines/mac/panes")) return json(panes);
    if (url.endsWith("/api/v1/sessions")) return json({ sessions: [agent] });
    if (url.endsWith("/api/v1/machines")) return json({ machines: [mac] });
    if (url.endsWith("/api/v1/todos")) return json({ todos: [] });
    return json({});
  }));
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

async function mountSidebar(route: Parameters<typeof Sidebar>[0]["route"] = { name: "home" }) {
  connect();
  const sock = FakeSocket.all[FakeSocket.all.length - 1]!;
  sock.open();
  sock.push({ type: "hello", server_version: "v1", features: ["terminal_panes"] });
  sock.push({ type: "machines", machines: [mac] });
  sock.push({ type: "sessions", machine_id: "mac", sessions: [agent] });
  act(() => root.render(<Sidebar route={route} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
}

describe("Sidebar unified active shell tabs", () => {
  it("surfaces active shell tabs inside their owning workspace group and aligns leading columns with Agent rows", async () => {
    await mountSidebar({ name: "terminal", mid: "mac", id: "pane:w1:p2" });

    const agentLink = host.querySelector<HTMLAnchorElement>('a[href*="/t/mac/w1/w1%3At1"]')!;
    const shellLink = host.querySelector<HTMLAnchorElement>('a[data-shell-row="term:mac/pane:w1:p2"]')!;
    const opsShellLink = host.querySelector<HTMLAnchorElement>('a[data-shell-row="term:mac/pane:w2:p1"]')!;

    expect(agentLink).toBeTruthy();
    expect(shellLink).toBeTruthy();
    expect(opsShellLink).toBeTruthy();
    expect(shellLink.getAttribute("href")).toBe("/terminal/mac/pane%3Aw1%3Ap2");
    expect(shellLink.getAttribute("aria-current")).toBe("page");
    expect(shellLink.textContent).toContain("zsh");
    expect(opsShellLink.textContent).toContain("htop");

    // Same workspace container holds both the Agent row and its sibling Shell row (no separate nested "Terminal" folder).
    const workspaceList = agentLink.closest(".sidebar-indent");
    expect(workspaceList?.contains(shellLink)).toBe(true);

    // Leading icon-column alignment: ShellRow reserves the exact same 14px mark slot + 14px status-dot slot + gap-2 as SessionRow.
    expect(agentLink.className).toContain("gap-2");
    expect(shellLink.className).toContain("gap-2");
    expect(agentLink.className).toContain("px-2");
    expect(shellLink.className).toContain("px-2");

    const agentMark = agentLink.querySelector<HTMLElement>("[data-agent-mark]")!;
    const shellMark = shellLink.querySelector<HTMLElement>("[data-shell-mark]")!;
    const shellStatus = shellLink.querySelector<HTMLElement>("[data-shell-status]")!;

    expect(agentMark.style.width).toBe("14px");
    expect(shellMark.className).toContain("size-3.5");
    expect(shellStatus.className).toContain("size-3.5");
    expect(agentLink.children[1]?.className).toContain("size-3.5");
  });

  it("shows plain shells in All and counts only Agents in the In a tab filter", async () => {
    await mountSidebar();

    const shell = () => host.querySelector('a[data-shell-row="term:mac/pane:w1:p2"]');
    const otherShell = () => host.querySelector('a[data-shell-row="term:mac/pane:w2:p1"]');
    const filterButton = (label: string) => [...host.querySelectorAll<HTMLButtonElement>('[role="radiogroup"] [role="radio"]')]
      .find((button) => button.getAttribute("aria-label") === label)!;
    expect(shell()).toBeTruthy();
    expect(otherShell()).toBeTruthy();

    await act(async () => filterButton(`${t("lib-agent-filter.terminal")} 1`).click());
    await flush();
    expect(host.querySelector('a[href*="/t/mac/w1/w1%3At1"]')).toBeTruthy();
    expect(shell()).toBeNull();
    expect(otherShell()).toBeNull();

    await act(async () => filterButton(t("lib-agent-filter.all")).click());
    await flush();
    expect(shell()).toBeTruthy();

    await act(async () => filterButton(`${stateName("working")} 1`).click());
    await flush();
    expect(shell()).toBeNull();
    expect(otherShell()).toBeNull();
  });
});
