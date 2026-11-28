// @vitest-environment jsdom
// "New agent" for a launcher that lists its own permission modes (#8261, Paseo's Grok, OpenCode, Codex): its own
// ("Launcher default", sent as no mode) or Full access, picked on the sheet and never remembered, so a mode left from a
// Claude Code start never reaches it; a provider with its own alone shows no field; Claude Code keeps its five.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Launcher, Machine } from "../lib/types";

const state = vi.hoisted(() => ({ loaded: true, features: [] as string[], machines: [] as Machine[] }));
// one assistant: its computers are the store's (lib/current-assistant.ts reads the roster, which this store stand-in has none of)
vi.mock("../lib/current-assistant", async () => {
  const store = (await import("../lib/store")) as unknown as { useStore: <T>(select: (s: { machines: unknown[] }) => T) => T };
  return { useScopedMachines: () => store.useStore((s) => s.machines) };
});
vi.mock("../lib/store", () => ({
  getState: () => state,
  useStore: (selector: (value: typeof state) => unknown) => selector(state),
  hubHas: (features: string[], feature: string, unknown = false) => (features.length ? features.includes(feature) : unknown),
}));
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));
const launched = vi.hoisted(() => [] as Record<string, unknown>[]);
vi.mock("../lib/new-agent", async (original) => ({ ...(await original<typeof import("../lib/new-agent")>()), launchAgent: (input: Record<string, unknown>) => { launched.push(input); return "local-1"; } }));

import { NewAgent } from "./NewAgent";
import { t } from "../i18n";
import { resetModelListsForTests } from "../lib/session-models";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const both = ["default", "bypassPermissions"];
const LAUNCHERS: Launcher[] = [
  { name: "claude", agent: "claude" },
  { name: "agy", agent: "agy", source: "agy", display_name: "Antigravity", permission_modes: ["default", "acceptEdits", "plan", "bypassPermissions"] },
  { name: "grok", agent: "paseo", source: "paseo", display_name: "Grok", permission_modes: both },
  { name: "kimi", agent: "paseo", source: "paseo", display_name: "Kimi", permission_modes: ["default"] },
];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
let host: HTMLDivElement;
let root: Root;
const flush = () => act(async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); });
const button = (text: string) => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;
const providerField = () => document.body.querySelector<HTMLElement>("[data-testid=new-agent-provider-modes]");
const choices = (el: HTMLElement | null) => [...(el?.querySelectorAll<HTMLButtonElement>("[role=radio]") ?? [])].map((b) => [b.textContent?.trim(), b.getAttribute("aria-checked")]);

async function open(launcher: string, key: string) {
  localStorage.setItem("new.launcher.mac", launcher);
  act(() => root.render(<NewAgent key={key} open onClose={() => {}} />));
  await flush();
}

beforeEach(() => {
  resetModelListsForTests();
  state.machines = [{ id: "mac", name: "Mac", online: true }];
  launched.length = 0;
  localStorage.clear();
  localStorage.setItem("new.cwd.mac", "/code/app");
  // a mode remembered from this computer's last Claude Code start
  localStorage.setItem("new.mode.mac", "plan");
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/model-config/purposes/")) return json({ error: "not found" }, 404);
    if (url.includes("/launchers/") && url.includes("/models")) return json({ agent: url.includes("/claude/") ? "claude" : url.includes("/agy/") ? "agy" : "paseo", models: [] });
    if (url.includes("/launchers")) return json({ launchers: LAUNCHERS });
    return json({ path: "/code", entries: [] });
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

describe("New agent on a launcher that lists its permission modes", () => {
  it("offers its own and Full access, starts on its own by default, and never sends the mode remembered for Claude Code", async () => {
    await open("grok", "a");
    expect(choices(providerField())).toEqual([["跟随启动器", "true"], ["完全访问", "false"]]);
    act(() => button("启动").click());
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ launcher: "grok" });
    expect(launched[0].permissionMode).toBeUndefined();
    expect(localStorage.getItem("new.mode.mac")).toBe("plan");
  });

  it("Full access picked goes with the start, and is not remembered", async () => {
    await open("grok", "b");
    act(() => button("完全访问").click());
    expect(choices(providerField())).toEqual([["跟随启动器", "false"], ["完全访问", "true"]]);
    expect(providerField()!.parentElement!.textContent).toContain("完全访问：编辑文件和执行命令均不会向你确认");
    act(() => button("启动").click());
    expect(launched[0]).toMatchObject({ launcher: "grok", permissionMode: "bypassPermissions" });
    expect(localStorage.getItem("new.mode.mac")).toBe("plan");
  });

  it("a provider with nothing but its own shows no field and sends no mode", async () => {
    await open("kimi", "c");
    expect(providerField()).toBeNull();
    expect(button("计划模式")).toBeUndefined();
    act(() => button("启动").click());
    expect(launched[0]).toMatchObject({ launcher: "kimi" });
    expect(launched[0].permissionMode).toBeUndefined();
  });

  it("Claude Code keeps its five modes and the one remembered", async () => {
    await open("claude", "d");
    expect(providerField()).toBeNull();
    expect(button("计划模式")).toBeDefined();
    act(() => button("启动").click());
    expect(launched[0]).toMatchObject({ launcher: "claude", permissionMode: "plan" });
  });

  it("Antigravity with restricted permission_modes hides Auto, keeps Ask first, and ignores a stale remembered auto (#17239)", async () => {
    localStorage.setItem("new.mode.mac", "auto");
    await open("agy", "e");
    expect(providerField()).toBeNull();
    expect(button(t("lib-session-actions.mode_default"))).toBeDefined();
    expect(button(t("lib-session-actions.mode_accept_edits"))).toBeDefined();
    expect(button(t("lib-session-actions.mode_plan"))).toBeDefined();
    expect(button(t("lib-session-actions.mode_bypass_permissions"))).toBeDefined();
    expect(button(t("lib-session-actions.mode_auto"))).toBeUndefined();
    act(() => button("启动").click());
    expect(launched[0]).toMatchObject({ launcher: "agy" });
    expect(launched[0].permissionMode).toBeUndefined();
    expect(localStorage.getItem("new.mode.mac")).toBe("auto");
  });
});
