// @vitest-environment jsdom
// New agent in a desktop app (#5480 Claude's desktop app, #5726 the Codex App), as iOS's DesktopStartUITests: with a
// launcher whose `source` says so, no folder picked is a temporary session (the start carries no folder, the computer
// picks it), browsing starts in the folder holding the app's projects, a new folder may be named, and the engine host
// choice is not offered. Every other launcher keeps New agent as it was.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine, Session } from "../lib/types";
import type { HostWorkspace } from "../lib/workspace-catalog";
import type { NewAgentPreset } from "../lib/new-agent";
import { t } from "../i18n";
import { loadRecentSetups, recentSetupsKey, type RecentSetup } from "../lib/recent-setups";

const state = vi.hoisted(() => ({ loaded: true, features: [] as string[], machines: [] as Machine[], sessions: {} as Record<string, Session[]>, workspaces: [] as HostWorkspace[], desktopSource: true, codexPromptOptional: false }));
vi.mock("../lib/current-assistant", async () => {
  const store = (await import("../lib/store")) as unknown as { useStore: <T>(select: (s: { machines: unknown[] }) => T) => T };
  return { useScopedMachines: () => store.useStore((s) => s.machines) };
});
vi.mock("../lib/store", () => ({
  getState: () => state,
  useStore: (selector: (value: typeof state) => unknown) => selector(state),
  hubHas: (features: string[], feature: string, unknown = false) => (features.length ? features.includes(feature) : unknown),
  onHubEvent: () => () => {},
}));
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));
const launched = vi.hoisted(() => [] as Record<string, unknown>[]);
vi.mock("../lib/new-agent", async (original) => ({ ...(await original<typeof import("../lib/new-agent")>()), launchAgent: (input: Record<string, unknown>) => { launched.push(input); return "local-1"; } }));

vi.mock("../lib/workspace-catalog", async (original) => {
  const real = await original<typeof import("../lib/workspace-catalog")>();
  return { ...real, useWorkspaceCatalog: () => ({ ...real.workspaceCatalogOwner(), workspaces: state.workspaces }) };
});

import { NewAgent } from "./NewAgent";
import { resetModelListsForTests } from "../lib/session-models";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const LAUNCHERS = [
  { name: "claude", agent: "claude" },
  { name: "claude-desktop", agent: "claude", source: "claude_desktop" },
  { name: "codex-app", agent: "codex", agent_type: "codex", source: "codex_app" },
];
const desktop = (cwd: string, agent = "claude"): Session => ({ machine_id: "mac", session_id: cwd, title: "", cwd, status: "idle", agent, agent_surface: "desktop" }) as Session;

let host: HTMLDivElement;
let root: Root;
let listed: string[];
// the desktop parts are a lazy chunk (NewAgentDesktop.tsx): a macrotask lets their import come in
const flush = () => act(async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); await new Promise((r) => setTimeout(r, 0)); });
const q = <T extends HTMLElement = HTMLElement>(sel: string) => document.body.querySelector<T>(sel);
const button = (text: string) => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === text);
const folderInput = () => q<HTMLInputElement>("input[placeholder]")!;
const temporary = () => q('[data-testid="new-agent-temporary"]');

beforeEach(() => {
  resetModelListsForTests();
  state.desktopSource = true;
  state.codexPromptOptional = false;
  state.workspaces = [];
  state.machines = [{ id: "mac", name: "MacBook", online: true, engine_hosts: ["herdr", "orca"], engine_default: "herdr" } as Machine];
  // the app's projects are in /Users/me/code (two), one in /Users/me/notes; a terminal claude's folder doesn't count
  state.sessions = { mac: [desktop("/Users/me/code/app"), desktop("/Users/me/code/web"), desktop("/Users/me/notes/plan"), { ...desktop("/Users/me/other/x"), agent_surface: "cli" }] };
  launched.length = 0;
  listed = [];
  localStorage.clear();
  localStorage.setItem("new.machine", "mac");
  localStorage.setItem("new.cwd.mac", "/code/app"); // the last start's folder: not picked on this sheet
  localStorage.setItem("new.launcher.mac", "claude-desktop");
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/model-config/purposes/")) return json({ error: "not found" }, 404);
    if (/\/launchers\/[^/]+\/models/.test(url)) return json({ models: [] });
    if (url.includes("/launchers")) return json({ launchers: LAUNCHERS.map((launcher) => ({ ...launcher,
      source: state.desktopSource ? launcher.source : undefined,
      ...(launcher.name === "codex-app" ? { prompt_optional: state.codexPromptOptional } : {}),
    })) });
    const dirs = /\/dirs\?path=([^&]*)/.exec(url);
    if (dirs) {
      const path = decodeURIComponent(dirs[1]) || "/Users/me";
      listed.push(path);
      return json({ path, parent: "/Users", entries: [{ name: "api", path: `${path}/api` }] });
    }
    return json({});
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

async function open(key = "a", preset?: NewAgentPreset) {
  await act(async () => root.render(<NewAgent key={key} open preset={preset} onClose={() => {}} />));
  await act(async () => vi.dynamicImportSettled());
  await flush();
}

describe("New agent in Claude's desktop app (#5480)", () => {
  it("with no folder picked is a temporary session: Start is ready and the start carries no folder", async () => {
    await open();
    expect(temporary()?.textContent).toContain("临时会话");
    expect(temporary()?.textContent).toContain("在 Claude 桌面版侧边栏中归为一组");
    expect(folderInput().value).toBe(""); // the last start's folder is not this one's
    expect(q('[data-testid="new-agent-desktop-hint"]')?.textContent).toContain("将在 Claude 桌面版中启动会话");
    // browsing starts where the app's projects are; no engine host to pick
    expect(listed).toContain("/Users/me/code");
    expect(q('[data-testid="start-host"]')).toBeNull();
    expect(button("启动")!.disabled).toBe(false);
    act(() => button("启动")!.click());
    expect(launched).toEqual([expect.objectContaining({ launcher: "claude-desktop", cwd: "", host: undefined })]);
    // the last real folder stays for the next start; the recent setup is a temporary session
    expect(localStorage.getItem("new.cwd.mac")).toBe("/code/app");
    expect(loadRecentSetups()).toEqual([expect.objectContaining({ launcher: "claude-desktop", cwd: "" })]);
  });

  it("a folder picked is used, and Use a temporary session goes back", async () => {
    await open();
    act(() => [...document.body.querySelectorAll<HTMLButtonElement>("button[title]")].find((b) => b.title === "/Users/me/code/api")!.click());
    expect(temporary()).toBeNull();
    expect(folderInput().value).toBe("/Users/me/code/api");
    act(() => button("改用临时会话")!.click());
    await flush();
    expect(temporary()).not.toBeNull();
    act(() => button("启动")!.click());
    expect(launched).toEqual([expect.objectContaining({ cwd: "" })]);
  });

  it("names a new folder in the one browsed, which the computer makes when the session starts", async () => {
    await open();
    await act(async () => q<HTMLButtonElement>('[data-testid="new-agent-new-folder"]')!.click());
    await act(async () => vi.dynamicImportSettled());
    await flush();
    const name = q<HTMLInputElement>('[data-testid="new-agent-naming"] input')!;
    expect(q('[data-testid="new-agent-naming"]')?.textContent).toContain("会话启动时将在「code」中创建。");
    // not one folder's name: nothing to use
    act(() => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(name, "a/b"); name.dispatchEvent(new Event("input", { bubbles: true })); });
    expect(button("使用")!.disabled).toBe(true);
    act(() => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(name, "site"); name.dispatchEvent(new Event("input", { bubbles: true })); });
    act(() => button("使用")!.click());
    expect(q('[data-testid="new-agent-naming"]')).toBeNull();
    expect(folderInput().value).toBe("/Users/me/code/site");
    act(() => button("启动")!.click());
    expect(launched).toEqual([expect.objectContaining({ launcher: "claude-desktop", cwd: "/Users/me/code/site" })]);
  });

  it("a recent card of a temporary session says so and fills one in", async () => {
    const card: RecentSetup = { machine: "mac", cwd: "", launcher: "claude-desktop", model: "", agent_type: "claude", mode: "", effort: "", at: "2026-11-13T03:00:00Z" };
    localStorage.setItem(recentSetupsKey(), JSON.stringify([card]));
    localStorage.setItem("new.launcher.mac", "claude");
    await open();
    expect(temporary()).toBeNull(); // plain Claude Code: New agent as before
    const c = q<HTMLButtonElement>("[data-recent-setup]")!;
    expect(c.textContent).toContain("临时会话");
    act(() => c.click());
    await flush();
    expect(temporary()).not.toBeNull();
    act(() => button("启动")!.click());
    expect(launched).toEqual([expect.objectContaining({ launcher: "claude-desktop", cwd: "" })]);
  });
});

describe("New agent in the Codex App (#5726)", () => {
  it("says where a chat in no project goes, and browses from the App's projects", async () => {
    localStorage.setItem("new.launcher.mac", "codex-app");
    state.sessions = { mac: [desktop("/Users/me/Documents/Codex/2026-11-12/a", "codex"), desktop("/Users/me/Documents/Codex/2026-11-13/b", "codex"), desktop("/Users/me/work/svc", "codex")] };
    await open();
    expect(temporary()?.textContent).toContain("在 Codex App 的「无项目」对话中");
    expect(q('[data-testid="new-agent-desktop-hint"]')?.textContent).toContain("将在 Codex App 中启动对话");
    expect(listed).toContain("/Users/me/work");
  });
});

describe("New agent with any other launcher", () => {
  it("needs a folder, offers no new folder and keeps the engine host choice", async () => {
    localStorage.setItem("new.launcher.mac", "claude");
    localStorage.removeItem("new.cwd.mac");
    await open();
    expect(temporary()).toBeNull();
    expect(q('[data-testid="new-agent-new-folder"]')).toBeNull();
    expect(q('[data-testid="new-agent-desktop-hint"]')).toBeNull();
    expect(q('[data-testid="start-host"]')).not.toBeNull();
    expect(listed).toEqual(["/Users/me"]);
    expect(button("启动")!.disabled).toBe(true);
  });
});

const openPreset = (launcher: string, cwd = "", desktopProjectId?: string) => open("desktop-preset", { machineId: "pc", cwd, launcher, desktopProjectId });
const type = (selector: string, value: string) => act(() => {
  const el = host.querySelector<HTMLInputElement | HTMLTextAreaElement>(selector)!;
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});

describe("New Agent desktop project presets", () => {
  beforeEach(() => {
    state.machines = [{ id: "pc", name: "Computer", online: true, engine_hosts: ["herdr", "orca"] }];
    state.sessions = { pc: [] };
    localStorage.setItem("new.machine", "pc");
    localStorage.setItem("new.mode.pc", "plan");
  });
  it("allows a Claude temporary session with the selected permission mode", async () => {
    await openPreset("claude-desktop");
    expect(host.textContent).toContain(t("newagent.temporary_session"));
    expect(host.textContent).toContain(t("newagent.permission_mode"));
    expect(button(t("newagent.start"))!.disabled).toBe(false);
    act(() => button(t("newagent.start"))!.click());
    expect(launched).toEqual([expect.objectContaining({ launcher: "claude-desktop", cwd: "", permissionMode: "plan", host: undefined, workspaceId: undefined })]);
    expect(localStorage.getItem("new.mode.pc")).toBe("plan");
  });

  it.each([false, true])("requires a first message in the Codex App even when prompt_optional is %s", async (optional) => {
    state.codexPromptOptional = optional;
    await openPreset("codex-app");
    expect(button(t("newagent.start"))!.disabled).toBe(true);
    const area = host.querySelector("textarea")!;
    act(() => area.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true })));
    expect(launched).toEqual([]);
    type("textarea", "   ");
    expect(button(t("newagent.start"))!.disabled).toBe(true);
    type("textarea", "Check the project");
    expect(button(t("newagent.start"))!.disabled).toBe(false);
    act(() => button(t("newagent.start"))!.click());
    expect(launched).toEqual([expect.objectContaining({ launcher: "codex-app", cwd: "", inlinePrompt: true, agentType: "codex", text: "Check the project" })]);
  });

  it("passes a chosen Windows project directory to its desktop launcher", async () => {
    await openPreset("claude-desktop", "C:\\Projects\\Sample");
    act(() => button(t("newagent.start"))!.click());
    expect(launched).toEqual([expect.objectContaining({ launcher: "claude-desktop", cwd: "C:\\Projects\\Sample", workspaceId: undefined, host: undefined })]);
  });

  it.each([["codex-app", "codex-project:home:second"], ["claude-desktop", "claude-environment:account:second"]])("keeps the exact native project for %s when projects share a directory", async (launcher, desktopProjectId) => {
    await openPreset(launcher, "C:\\Projects\\Sample", desktopProjectId);
    type("textarea", "Check this project");
    act(() => button(t("newagent.start"))!.click());
    expect(launched).toEqual([expect.objectContaining({ desktopProjectId, launcher, workspaceId: undefined })]);
  });

  it.each([["codex-app", "codex_app", "codex-project:home:second"], ["claude-desktop", "claude_desktop", "claude-environment:account:second"]])("keeps %s project identity when another catalog root is selected", async (launcher, source, desktopProjectId) => {
    state.workspaces = ["C:\\Projects\\Sample", "D:\\Docs"].map((cwd) => ({ id: cwd, machine_id: "pc", source, source_id: desktopProjectId, title: "Saved project", cwd }));
    await openPreset(launcher, "C:\\Projects\\Sample", desktopProjectId);
    type("input[placeholder]", "d:/docs/pages");
    type("textarea", "Check this project");
    expect(host.textContent).not.toContain(t("newagent.desktop_project_detached"));
    act(() => button(t("newagent.start"))!.click());
    expect(launched).toEqual([expect.objectContaining({ desktopProjectId, cwd: "d:/docs/pages" })]);
  });

  it.each([["codex-app", "codex-project:home:second"], ["claude-desktop", "claude-environment:account:second"]])("clears %s project identity when a temporary session is selected", async (launcher, desktopProjectId) => {
    await openPreset(launcher, "C:\\Projects\\Sample", desktopProjectId);
    act(() => button(t("newagent-desktop.use_temporary"))!.click());
    type("textarea", "Check a new task");
    expect(host.textContent).toContain(t("newagent.desktop_project_detached"));
    act(() => button(t("newagent.start"))!.click());
    expect(launched).toEqual([expect.objectContaining({ desktopProjectId: undefined, cwd: "" })]);
  });

  it.each([["codex-app", "codex-project:home:second"], ["claude-desktop", "claude-environment:account:second"]])("clears %s project identity when a directory outside its roots is selected", async (launcher, desktopProjectId) => {
    await openPreset(launcher, "C:\\Projects\\Sample", desktopProjectId);
    type("input[placeholder]", "D:\\Other");
    type("textarea", "Check a new task");
    expect(host.textContent).toContain(t("newagent.desktop_project_detached"));
    act(() => button(t("newagent.start"))!.click());
    expect(launched).toEqual([expect.objectContaining({ desktopProjectId: undefined, cwd: "D:\\Other" })]);
  });

  it("honors an explicitly temporary preset over the remembered computer and folder", async () => {
    state.machines.push({ id: "other", name: "Other", online: true });
    localStorage.setItem("new.machine", "other");
    localStorage.setItem("new.cwd.pc", "C:\\Projects\\Remembered");
    await openPreset("claude-desktop");
    act(() => button(t("newagent.start"))!.click());
    expect(launched).toEqual([expect.objectContaining({ machineId: "pc", cwd: "", launcher: "claude-desktop" })]);
  });

  it("refuses a terminal alias with the same name as the requested desktop app", async () => {
    state.desktopSource = false;
    await openPreset("claude-desktop", "C:\\Projects\\Sample");
    expect(host.querySelector('[role="alert"]')?.textContent).toContain(t("newagent.desktop_unavailable", { app: "Claude" }));
    expect(button(t("newagent.start"))!.disabled).toBe(true);
    act(() => button(t("newagent.start"))!.click());
    expect(launched).toEqual([]);
  });
});
