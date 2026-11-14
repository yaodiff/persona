// @vitest-environment jsdom
// "Workflow board" is off unless turned on (#2475): no sidebar entry and no discovery polling while off, the entry back once
// Settings › General › Agent turns it on, and a hub that says the board is on (one used before the switch) shows it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// jsdom has no matchMedia, which the sidebar's modules read when they load, and no ResizeObserver, which the
// sidebar's highlight uses (#2490)
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { resetManagerChoice, settleRosterDefault } from "../lib/manager-choice";
import { getWorkspaceBoardSetting, resetWorkspaceBoardSettingForTest } from "../lib/workspace-board-setting";
import { AgentGroup } from "./settings/AgentGroup";
import { Sidebar } from "./Sidebar";
import { WorkspaceBoard } from "./WorkspaceBoard";
import { CATALOG_POLL_MS } from "../lib/workspace-catalog";

// the hub's pushes, by type, as the pages listen for them (each still reaches the real listeners too)
const pushes = vi.hoisted(() => new Map<string, (msg: Record<string, unknown>) => void>());
vi.mock("../lib/store", async (orig) => {
  const store = await orig<typeof import("../lib/store")>();
  return { ...store, onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => { pushes.set(type, l); return store.onHubEvent(type, l); } };
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const board = { revision: 2, columns: [{ id: "todo", title: "待办" }], cards: [], can_write: true };
let enabled = false;
let calls: { method: string; path: string; body?: unknown }[] = [];

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const path = String(input).split("?")[0]!;
  const method = init?.method ?? "GET";
  calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined });
  if (path === "/api/v1/workspace-board/settings") {
    if (method === "PUT") enabled = (JSON.parse(String(init!.body)) as { enabled: boolean }).enabled;
    return Promise.resolve(json({ enabled, can_write: true }));
  }
  if (path === "/api/v1/workspace-board") return Promise.resolve(json(board));
  if (path === "/api/v1/workspaces") return Promise.resolve(json({ workspaces: [] }));
  return Promise.resolve(json({}));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const discoveryCalls = () => calls.filter((c) => c.path === "/api/v1/workspaces").length;
const sidebarEntry = () => host.querySelector('a[href="/workspace-board"]');

beforeEach(() => {
  enabled = false;
  calls = [];
  resetWorkspaceBoardSettingForTest();
  vi.stubGlobal("fetch", vi.fn(hub));
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  resetManagerChoice();
});

describe("the workflow board switch", () => {
  it("is off by default: no sidebar entry, an 未开启 page with the way to settings, and no discovery polling", async () => {
    act(() => root.render(<><Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} /><WorkspaceBoard /></>));
    await flush();
    expect(getWorkspaceBoardSetting().enabled).toBe(false);
    expect(sidebarEntry()).toBeNull();
    expect(host.textContent).toContain("工作流看板未开启");
    await act(async () => { vi.advanceTimersByTime(60_000); });
    await flush();
    expect(discoveryCalls()).toBe(0);
    expect(calls.some((c) => c.path === "/api/v1/workspace-board")).toBe(false);

    const open = [...host.querySelectorAll("button")].find((b) => b.textContent === "去设置打开")!;
    act(() => open.click());
    expect(location.pathname + location.search).toBe("/settings?tab=hub");
  });

  it("shows the sidebar entry once 设置 › 通用 › Agent turns it on, and keeps the switch on the hub", async () => {
    act(() => root.render(<><AgentGroup /><Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} /></>));
    await flush();
    const sw = host.querySelector<HTMLButtonElement>('button[role="switch"][aria-label="工作流看板"]')!;
    expect(sw.getAttribute("aria-checked")).toBe("false");
    expect(sidebarEntry()).toBeNull();
    act(() => sw.click());
    await flush();
    expect(calls).toContainEqual({ method: "PUT", path: "/api/v1/workspace-board/settings", body: { enabled: true } });
    expect(sw.getAttribute("aria-checked")).toBe("true");
    expect(sidebarEntry()).not.toBeNull();
  });

  it("opens the saved board when the hub says it is on (a board in use before the switch counts as on)", async () => {
    enabled = true;
    settleRosterDefault();
    act(() => root.render(<><Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} /><WorkspaceBoard /></>));
    await flush();
    expect(sidebarEntry()).not.toBeNull();
    expect(host.textContent).not.toContain("工作流看板未开启");
    expect(calls.some((c) => c.path === "/api/v1/workspace-board")).toBe(true);
    const before = discoveryCalls();
    expect(before).toBeGreaterThan(0);
    await act(async () => { vi.advanceTimersByTime(CATALOG_POLL_MS); });
    await flush();
    expect(discoveryCalls()).toBeGreaterThan(before);
  });
});

describe("when the hub can't give the switch or the board", () => {
  // every request goes to `answer` first; what it leaves is the hub above
  const stubHub = (answer: (path: string, method: string) => Response | undefined) =>
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input).split("?")[0]!;
      const method = init?.method ?? "GET";
      const own = answer(path, method);
      if (!own) return hub(input, init);
      calls.push({ method, path });
      return Promise.resolve(own);
    }));
  const buttons = (text: string) => [...host.querySelectorAll("button")].filter((b) => b.textContent === text);
  const notFound = () => json({ error: "Not Found", code: "not_found" }, 404);
  const settingReads = () => calls.filter((c) => c.path === "/api/v1/workspace-board/settings" && c.method === "GET").length;

  it("a switch read that fails says why with Retry, on the page and in Settings, instead of loading forever", async () => {
    let failing = true;
    stubHub((path) => (path === "/api/v1/workspace-board/settings" && failing ? json({ error: "database is down", code: "internal" }, 500) : undefined));
    act(() => root.render(<><AgentGroup /><WorkspaceBoard /></>));
    await flush();
    expect(host.textContent).not.toContain("正在读取工作流看板的开关");
    expect(host.textContent).toContain("读不到工作流看板的开关");
    expect(host.textContent).toContain("HTTP 500");
    expect(host.querySelector('[role="alert"]')).not.toBeNull();
    expect(buttons("重试")).toHaveLength(2); // the page's and Settings'

    const before = settingReads();
    failing = false;
    act(() => buttons("重试")[0].click());
    await flush();
    expect(settingReads()).toBe(before + 1);
    expect(host.textContent).not.toContain("读不到工作流看板的开关");
    expect(host.textContent).toContain("工作流看板未开启"); // the hub answered: off
    expect(host.querySelector('button[role="switch"][aria-label="工作流看板"]')).not.toBeNull();
  });

  it("an older hub (404): one message to update the hub, none of the board's controls, and Settings says the hub lacks the switch", async () => {
    stubHub((path) => (path.startsWith("/api/v1/workspace-board") ? notFound() : undefined));
    settleRosterDefault();
    act(() => root.render(<><AgentGroup /><WorkspaceBoard /></>));
    await flush();
    // what to do once, in the title; the body only says why
    const title = [...host.querySelectorAll("h2")].find((h) => h.textContent === "更新 hub 后才能用工作流看板");
    expect(title).toBeDefined();
    expect(title!.nextElementSibling?.textContent).toBe("这个 hub 的版本太旧，或者没开看板存储。");
    expect(host.textContent).not.toContain("还用不了工作流看板");
    expect(host.textContent).not.toContain("先更新 hub");
    expect(host.textContent).not.toContain("看板暂不可用");
    expect(buttons("重试")).toHaveLength(0); // retrying can't help
    // nothing that would change nothing: search, machine filter, archived, manual / auto, the auto view's promo
    expect(host.querySelector('input[aria-label="搜索工作空间"]')).toBeNull();
    expect(host.querySelector("select")).toBeNull();
    expect(buttons("已归档")).toHaveLength(0);
    expect(host.querySelector('[role="group"]')).toBeNull();
    expect(host.textContent).not.toContain("点我切换到自动模式");
    const open = buttons("打开 hub 设置")[0]!;
    // Settings: no switch that can't move, and not "read-only device"
    expect(host.querySelector('button[role="switch"]')).toBeNull();
    expect(host.textContent).not.toContain("只读设备");
    expect(host.textContent).toContain("这个 hub 的版本还没有这个开关");
    act(() => open.click());
    expect(location.pathname + location.search).toBe("/settings?tab=hub");
  });

  it("a manager that is away (503 assistant_offline) keeps the board's controls and says it once, with Retry", async () => {
    let away = true;
    stubHub((path) => {
      if (path === "/api/v1/workspace-board/settings") return json({ enabled: true, can_write: true });
      if (path === "/api/v1/workspace-board" && away) return json({ error: "assistant offline", code: "assistant_offline" }, 503);
      return undefined;
    });
    settleRosterDefault();
    act(() => root.render(<WorkspaceBoard />));
    await flush();
    expect(host.textContent).not.toContain("更新 hub");
    expect(host.querySelectorAll('[role="alert"]')).toHaveLength(1);
    expect(host.textContent).toContain("看板暂不可用");
    expect(host.querySelector('input[aria-label="搜索工作空间"]')).not.toBeNull();
    away = false;
    act(() => buttons("重试")[0].click());
    await flush();
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(host.querySelector('section[aria-label="待办"]')).not.toBeNull();
  });

  it("a push after a switch read that failed: Settings offers the switch, and doesn't call the device read-only", async () => {
    stubHub((path) => (path === "/api/v1/workspace-board/settings" ? json({ error: "database is down", code: "internal" }, 500) : undefined));
    act(() => root.render(<AgentGroup />));
    await flush();
    act(() => settleRosterDefault()); // the roster names the default manager, whose pushes count
    await flush();
    expect(host.textContent).toContain("读不到工作流看板的开关");
    const reads = settingReads();
    act(() => pushes.get("workspace_board_settings")!({ type: "workspace_board_settings", enabled: true })); // another device turned it on
    await flush();
    expect(settingReads()).toBe(reads + 1); // who may change it: asked again (and still failing here)
    const sw = host.querySelector<HTMLButtonElement>('button[role="switch"][aria-label="工作流看板"]')!;
    expect(sw.getAttribute("aria-checked")).toBe("true");
    expect(sw.disabled).toBe(false);
    expect(host.textContent).not.toContain("只读设备");
  });

  it("a read-only device is still told it is read-only", async () => {
    stubHub((path) => (path === "/api/v1/workspace-board/settings" ? json({ enabled: true, can_write: false }) : undefined));
    act(() => root.render(<AgentGroup />));
    await flush();
    expect(host.textContent).toContain("只读设备不能改这个开关");
    expect(host.querySelector<HTMLButtonElement>('button[role="switch"]')!.disabled).toBe(true);
  });
});
