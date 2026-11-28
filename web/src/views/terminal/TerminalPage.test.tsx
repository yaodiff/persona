// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import type { PanesResponse } from "../../lib/terminals";
import type { Machine, Session } from "../../lib/types";
import { TerminalPage } from "./TerminalPage";

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

const env = vi.hoisted(() => ({
  machines: [{ id: "m1", name: "Mac Studio", online: true }] as Machine[],
  sessions: { m1: [] as Session[] } as Record<string, Session[]>,
  features: ["terminal_panes", "terminal_control", "herdr_client"] as string[],
  listing: {
    m1: {
      data: {
        hosts: [
          {
            kind: "herdr",
            stream: true,
            control: true,
            new_tab: true,
            workspaces: [{ id: "w1", label: "persona", number: 1, cwd: "/code/persona" }],
            tabs: [{ id: "w1:t1", workspace_id: "w1", label: "shell", number: 1 }],
            panes: [{ pane: "w1:p2", terminal: "pane:w1:p2", tab_id: "w1:t1", workspace_id: "w1", title: "zsh", cwd: "/code/persona" }],
          },
        ],
      } as PanesResponse,
    },
  },
}));

vi.mock("../../lib/current-assistant", () => ({
  useScopedMachines: () => env.machines,
  useScoped: (rows: Session[]) => rows,
  useScope: () => ({ several: false, id: "main", has: () => true }),
  useScopedCounts: () => ({ badge: 0, quietCounted: 0 }),
}));
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  useStore: (read: (s: unknown) => unknown) => read(env),
  getSession: () => undefined,
  onHubEvent: () => () => {},
  rememberSessionRow: vi.fn(),
  refresh: vi.fn(async () => {}),
}));
vi.mock("../../lib/sessions", () => ({
  useFullAgentSessions: () => ({ sessions: [], loaded: true, error: "" }),
  retryFullAgentSessions: vi.fn(),
}));
vi.mock("../../lib/terminal-panes", () => ({
  usePanes: () => ({ listing: env.listing, busy: false, refresh: vi.fn(), reload: vi.fn() }),
}));
const FEED_SNAPSHOT = {
  mode: "stream" as const,
  loaded: true,
  ready: true,
  offline: false,
  stale: false,
  paused: false,
  wants: true,
  control: "active" as const,
  wrap: false,
  older: [],
  hasOlder: false,
  loadingOlder: false,
  atLimit: false,
  cols: 120,
  rows: 40,
  fontFamily: "monospace",
  fontSize: 13,
  palette: {},
};

vi.mock("../session/terminal/feed", () => ({
  TerminalFeed: class {
    cwd = "";
    driving = false;
    subscribe = () => () => {};
    snapshot = () => FEED_SNAPSHOT;
    mount = vi.fn(async () => {});
    dispose = vi.fn();
    retry = vi.fn();
    setWants = vi.fn();
    setWrap = vi.fn();
    focus = vi.fn();
    hasSelection = () => false;
    selectedText = () => "";
    clearFind = vi.fn();
  },
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("TerminalPage (Route A)", () => {
  it("renders History with ended sessions only and removes the Terminals segmented switcher and secondary tree", async () => {
    await act(async () => {
      root.render(<TerminalPage route={{ name: "terminal" }} onToggleSidebar={() => {}} onNewAgent={() => {}} />);
    });

    expect(host.querySelector("[data-history-browser]")).toBeTruthy();
    expect(host.querySelector("[data-terminals-list]")).toBeNull();
    expect(host.querySelector('[role="radiogroup"]')).toBeNull();
    expect(host.querySelector("h1")?.textContent).toBe(t("work-entry.title"));
  });

  it("renders direct /terminal/<mid>/<id> inside a single workspace card with one compact 44px bar", async () => {
    localStorage.setItem("persona.terminal.defaultMode", "pane");
    await act(async () => {
      root.render(
        <TerminalPage
          route={{ name: "terminal", mid: "m1", id: "pane:w1:p2" }}
          onToggleSidebar={() => {}}
          onNewAgent={() => {}}
        />,
      );
    });

    // Single workspace card: no History sidebar, no secondary terminal tree, no redundant outer header or duplicate toolbar.
    expect(host.querySelector("[data-terminal-workspace]")).toBeTruthy();
    expect(host.querySelector("[data-history-browser]")).toBeNull();
    expect(host.querySelector("[data-terminals-list]")).toBeNull();
    expect(host.querySelector("[data-terminal-toolbar]")).toBeNull();

    const headers = host.querySelectorAll("header");
    expect(headers).toHaveLength(1);
    const bar = host.querySelector<HTMLElement>("[data-terminal-header]")!;
    expect(bar).toBeTruthy();
    expect(bar.className).toContain("h-11");

    // Left: framed terminal icon + concise <shell/tab title> · <workspace / machine> breadcrumb.
    expect(bar.querySelector("[data-terminal-icon]")).toBeTruthy();
    expect(bar.textContent).toContain("zsh");
    expect(bar.textContent).toContain("persona / Mac Studio");

    // Right: Single pane | Full toggle + Control | Watch toggle + overflow menu button (no decorative layers split buttons).
    expect(bar.querySelector(`[role="radiogroup"][aria-label="${t("terminal-full.mode_group")}"]`)).toBeTruthy();
    expect(bar.querySelector(`[role="radiogroup"][aria-label="${t("session-terminalpanel.mode_label")}"]`)).toBeTruthy();
    expect(bar.querySelector(`button[aria-label="${t("session-terminalpanel.pane_actions")}"]`)).toBeTruthy();
    expect(bar.querySelector(`button[aria-label="${t("lib-node-actions.split_down")}"]`)).toBeNull();
  });
});
