// @vitest-environment jsdom
// Regressions for #12379:
// 1. Follow model settings resolves from a single source of truth across Follow row, bottom model pill,
//    dropdown Current/Default badges, starting placeholder metadata, and launch payload.
// 2. Sidebar Starting placeholder shows the matching engine icon (never a mismatched launcher icon).
// 3. Newly opened session composer displays the resolved follow model instead of "Default (recommended)"
//    before the first reply reports session.model.
// 4. Opening / reopening New Agent resets scrollTop to 0 and keeps the top Recent setups card unclipped.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentLaunch } from "../lib/new-agent";
import { recentSetupsKey, type RecentSetup } from "../lib/recent-setups";
import { MANAGER_HEADER, resetManagerChoice, setRosterDefault } from "../lib/manager-choice";
import { resetModelListsForTests } from "../lib/session-models";
import type { Launcher, Machine, Session } from "../lib/types";

const storeState = vi.hoisted(() => ({
  loaded: true,
  features: [] as string[],
  machines: [] as Machine[],
  sessions: {} as Record<string, Session[]>,
  launches: {} as Record<string, AgentLaunch>,
  interactions: [] as unknown[],
  answered: {} as Record<string, unknown>,
  mutes: {} as Record<string, unknown>,
  pins: {} as Record<string, unknown>,
  todos: [] as unknown[],
  todosSupported: false,
  todosLoaded: true,
  todosError: null as string | null,
  seen: {} as Record<string, unknown>,
  assistant: null,
}));

vi.mock("../lib/current-assistant", async () => {
  const store = (await import("../lib/store")) as unknown as { useStore: <T>(select: (s: typeof storeState) => T) => T };
  return {
    useScopedMachines: () => store.useStore((s) => s.machines),
    useScope: () => ({ several: false }),
    useScoped: <T,>(items: T[]) => items,
    useScopedInteractions: () => [],
    useScopedTodos: () => [],
    scoped: <T,>(items: T[]) => items,
  };
});

vi.mock("../lib/store", async (original) => ({
  ...(await original<typeof import("../lib/store")>()),
  getState: () => storeState,
  useStore: (selector: (value: typeof storeState) => unknown) => selector(storeState),
  hubHas: (features: string[], feature: string, unknown = false) => (features.length ? features.includes(feature) : unknown),
  getSession: (mid: string, sid: string) => (storeState.sessions[mid] ?? []).find((s) => s.session_id === sid),
  putLaunch: (launch: AgentLaunch) => { storeState.launches = { ...storeState.launches, [launch.id]: launch }; },
}));

vi.mock("react-dom", async (original) => ({
  ...(await original<typeof import("react-dom")>()),
  createPortal: (children: React.ReactNode) => children,
}));

const launched = vi.hoisted(() => [] as Record<string, unknown>[]);
vi.mock("../lib/new-agent", async (original) => ({
  ...(await original<typeof import("../lib/new-agent")>()),
  launchAgent: (input: Record<string, unknown>) => {
    launched.push(input);
    return "local-follow-1";
  },
}));

import { NewAgent } from "./NewAgent";
import { NavRow } from "./SidebarRows";
import { launchDisplayEngine } from "../lib/new-agent";
import { AgentModelPill } from "./chat/ModelPill";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const EFFORTS = ["low", "medium", "high", "xhigh", "max"];
const LAUNCHERS: Launcher[] = [
  { name: "claude", agent: "claude" },
  { name: "agy", agent: "agy" },
];
const CLAUDE_LIST = {
  agent: "claude",
  models: [
    { id: "default", name: "Default (recommended)", default: true, resolved: "deepseek-v4.1-flash", efforts: EFFORTS },
    { id: "deepseek-v4.1-flash", name: "deepseek-v4.1-flash", efforts: EFFORTS },
  ],
};
const AGY_LIST = {
  agent: "agy",
  models: [
    { id: "gemini-4-neon-high", name: "Gemini 4 Neon (High)", default: true, efforts: ["low", "high", "max"] },
  ],
};

let host: HTMLDivElement;
let root: Root;
let purposeHeaders: (string | null)[] = [];
let purposeEffective: Record<string, string> = { provider: "launcher.agy", model: "gemini-4-neon-high", effort: "high" };
let launchersDefault: Record<string, string> = { provider: "claude", model: "deepseek-v4.1-flash", effort: "max" };

const flush = () => act(async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); });
const q = <T extends HTMLElement = HTMLElement>(sel: string) => document.body.querySelector<T>(sel);
const button = (text: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === text)!;
const pill = () => q<HTMLButtonElement>("[data-testid=new-agent-model] [data-model-pill]")!;
const followRow = () => q("[data-follow-row]")!;
const launcherHeader = (id: string) => q<HTMLButtonElement>(`[data-panel-launcher="${id}"]`)!;

async function openNewAgent(open = true) {
  act(() => root.render(<NewAgent open={open} onClose={() => {}} />));
  await flush();
}

async function openModelPanel() {
  act(() => pill().click());
  await act(async () => vi.dynamicImportSettled());
  await flush();
  expect(q("[data-model-panel]")).not.toBeNull();
}

beforeEach(() => {
  resetModelListsForTests();
  resetManagerChoice();
  setRosterDefault("assistant");
  storeState.machines = [{ id: "mac", name: "Mac", online: true, manager: "work-mgr" }];
  storeState.sessions = {};
  storeState.launches = {};
  launched.length = 0;
  purposeHeaders = [];
  purposeEffective = { provider: "launcher.agy", model: "gemini-4-neon-high", effort: "high" };
  launchersDefault = { provider: "claude", model: "deepseek-v4.1-flash", effort: "max" };
  localStorage.clear();
  localStorage.setItem("new.machine", "mac");
  localStorage.setItem("new.cwd.mac", "/code/app");
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/model-config/purposes/")) {
      purposeHeaders.push(new Headers(init?.headers).get(MANAGER_HEADER));
      return json({ purpose: { id: "agent.new", effective: purposeEffective }, providers: [] });
    }
    if (url.includes("/launchers/claude/models")) return json(CLAUDE_LIST);
    if (url.includes("/launchers/agy/models")) return json(AGY_LIST);
    if (url.includes("/machines/mac/launchers")) {
      return json({ launchers: LAUNCHERS, default: launchersDefault });
    }
    return json({ path: "/code/app", entries: [] });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  resetManagerChoice();
});

describe("New Agent Follow model settings single source of truth (#12379)", () => {
  it("aligns Follow row, bottom model pill, dropdown Current badge, and launch display metadata when launchers.default disagrees with purpose.effective", async () => {
    await openNewAgent();

    // Queries the machine's owning manager for model-config too.
    expect(purposeHeaders).toEqual(["work-mgr"]);

    // Bottom pill matches claude · deepseek-v4.1-flash · max (NOT agy / Gemini 4 Neon).
    expect(pill().querySelector("[data-pill-engine]")!.getAttribute("data-pill-engine")).toBe("claude");
    expect(pill().title).toBe("Claude Code · deepseek-v4.1-flash · Max");
    expect(pill().textContent).not.toContain("Gemini 4 Neon");

    // Dropdown: Follow row is checked and names Claude Code · deepseek-v4.1-flash · Max;
    // claude (NOT agy) carries the Current badge.
    await openModelPanel();
    expect(followRow().getAttribute("aria-pressed")).toBe("true");
    expect(followRow().textContent).toBe("跟随模型配置Claude Code · deepseek-v4.1-flash · Max");
    expect(launcherHeader("claude").textContent).toContain("当前");
    expect(launcherHeader("agy").textContent).not.toContain("当前");
    expect(launcherHeader("agy").textContent).not.toContain("默认");

    // Close panel and click Start: launch payload keeps Follow mode (launcher: "") while
    // recording unified display metadata for the Starting placeholder and session handoff.
    act(() => pill().click());
    act(() => button("启动").click());
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({
      machineId: "mac",
      cwd: "/code/app",
      launcher: "",
      displayLauncher: "claude",
      displayEngine: "claude",
      displayModel: "deepseek-v4.1-flash",
    });
    expect(launched[0].model).toBeUndefined();
    expect(launched[0].effort).toBeUndefined();
  });

  it("ignores stale recent setups with empty launcher so Follow model settings is not overridden by an old model", async () => {
    const staleFollow: RecentSetup = {
      machine: "mac",
      cwd: "/code/app",
      launcher: "",
      model: "stale-model-id",
      agent_type: "claude",
      mode: "",
      effort: "",
      at: "2026-11-20T00:00:00Z",
    };
    localStorage.setItem(recentSetupsKey(), JSON.stringify([staleFollow]));

    await openNewAgent();
    expect(pill().title).toBe("Claude Code · deepseek-v4.1-flash · Max");
    expect(pill().title).not.toContain("stale-model-id");
  });

  it("renders the matching engine mark on the sidebar Starting placeholder row", () => {
    const launch: AgentLaunch = {
      id: "local-follow-1",
      machineId: "mac",
      cwd: "/code/app",
      launcher: "",
      displayLauncher: "claude",
      displayEngine: "claude",
      displayModel: "deepseek-v4.1-flash",
      text: "Check follow mode",
      attachments: [],
      phase: "creating",
      uploaded: {},
      startOpId: "op-1",
      startPromptOpId: "op-2",
      messageId: "msg-1",
    };
    act(() =>
      root.render(
        <NavRow
          k="s:mac/local-follow-1"
          reg={() => () => {}}
          hover={() => {}}
          active
          to={{ name: "session", mid: "mac", sid: "local-follow-1" }}
          go={() => () => {}}
          icon="agent"
          agent={launchDisplayEngine(launch)}
          label={launch.text}
          hint="后台启动中"
        />,
      ),
    );
    const mark = host.querySelector("[data-agent-mark]");
    expect(mark?.getAttribute("data-agent-mark")).toBe("claude");
  });

  it("keeps the newly opened session composer pill aligned with the advertised follow model instead of Default (recommended)", async () => {
    const session: Session = {
      machine_id: "mac",
      session_id: "s-new-1",
      title: "New",
      cwd: "/code/app",
      status: "idle",
      state: "idle",
      launcher: "claude",
      agent: "claude",
      effort: "max",
      starting: true,
    };
    storeState.sessions = { mac: [session] };
    storeState.launches = {
      "local-follow-1": {
        id: "local-follow-1",
        machineId: "mac",
        cwd: "/code/app",
        launcher: "",
        displayLauncher: "claude",
        displayEngine: "claude",
        displayModel: "deepseek-v4.1-flash",
        text: "",
        attachments: [],
        phase: "ready",
        session,
        uploaded: {},
        startOpId: "op-1",
        startPromptOpId: "op-2",
        messageId: "msg-1",
      },
    };

    act(() => root.render(<AgentModelPill session={session} />));
    await flush();

    const sessionPill = host.querySelector<HTMLButtonElement>("[data-model-pill]")!;
    expect(sessionPill.textContent).toContain("deepseek-v4.1-flash");
    expect(sessionPill.textContent).not.toContain("Default (recommended)");
    expect(sessionPill.title).toBe("Claude Code · deepseek-v4.1-flash · Max");

    // Once the server reports an actual model, session.model takes over.
    act(() => root.render(<AgentModelPill session={{ ...session, model: "claude-sonnet-5-5" }} />));
    await flush();
    expect(host.querySelector<HTMLButtonElement>("[data-model-pill]")!.title).toContain("sonnet 5.5");
  });

  it("opens New Agent scrolled to the top so the Recent setups card is not clipped under the dialog header", async () => {
    const recent: RecentSetup = {
      machine: "mac",
      cwd: "/code/app",
      launcher: "",
      model: "",
      agent_type: "claude",
      mode: "",
      effort: "",
      at: "2026-11-20T00:00:00Z",
    };
    localStorage.setItem(recentSetupsKey(), JSON.stringify([recent]));

    await openNewAgent(true);
    const dialog = q<HTMLElement>('[role="dialog"]')!;
    const body = q<HTMLElement>("[data-dialog-body]")!;
    const recents = q<HTMLElement>('[data-testid="recent-setups"]')!;

    expect(recents).not.toBeNull();
    expect(body.scrollTop).toBe(0);
    expect(body.className).toContain("[overflow-anchor:none]");
    // Focus stays on the dialog container on open rather than scrolling down to the bottom textarea.
    expect(document.activeElement).toBe(dialog);
    expect(document.activeElement).not.toBe(q("textarea"));

    // Simulate the user scrolling down, closing, and reopening: scroll position resets to 0.
    body.scrollTop = 180;
    await openNewAgent(false);
    await openNewAgent(true);
    expect(q<HTMLElement>("[data-dialog-body]")!.scrollTop).toBe(0);
    expect(document.activeElement).toBe(q<HTMLElement>('[role="dialog"]'));
  });
});
