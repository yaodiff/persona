// @vitest-environment jsdom
// New agent when the chosen computer's launchers can't be read (#10952): the model pill leaves its spinner for its failed
// state (a warning, "Model loading failed"), the line under the first message box says why with Retry, a click on the
// pill or Retry reads them again (a success brings the pill back), Start still runs the computer's default, and a read
// still on its way for a computer picked before never lands on the one picked since.
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
import { resetModelListsForTests } from "../lib/session-models";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const EFFORTS = ["low", "medium", "high", "xhigh", "max"];
const LAUNCHERS: Launcher[] = [{ name: "claude", agent: "claude" }, { name: "relay", agent: "claude" }];
const LIST = { agent: "claude", models: [{ id: "default", name: "Default", default: true, efforts: EFFORTS }, { id: "opus", name: "Opus 5.5", efforts: EFFORTS }] };
// Model settings › New agent: Claude Code's Opus at High
const PURPOSE = { purpose: { id: "agent.new", effective: { provider: "launcher.claude", model: "opus", effort: "high" } }, providers: [] };
// what the hub answers when the computer doesn't answer within its time (models_launchers_routes.go, apierr "timeout")
const TIMED_OUT = { code: "timeout", detail: "context deadline exceeded" };

// computer → how its launcher read goes: answered, failed, or held until the test answers it
let mode: Record<string, "ok" | "fail" | "hold">;
let held: { mid: string; answer: (failed: boolean) => void }[];
let launcherReads: string[];

let host: HTMLDivElement;
let root: Root;
const flush = () => act(async () => { for (let i = 0; i < 10; i++) await Promise.resolve(); });
const q = <T extends HTMLElement = HTMLElement>(sel: string) => document.body.querySelector<T>(sel);
const button = (text: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === text)!;
const machineChip = (name: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === name && b.className.includes("h-8"))!;
const pill = () => q<HTMLButtonElement>("[data-testid=new-agent-model] [data-model-pill]")!;
// the pill's Spinner (ui/atoms.tsx), before the name or after it
const spinning = () => Boolean(pill().querySelector("span.border-t-ink-2"));
// read, and nothing on its way: no spinner, no warning, it opens
const ready = () => pill().dataset.modelPill === "ready" && !pill().disabled && !spinning();
const failedLine = () => q("[data-testid=new-agent-launchers-failed]");
const start = () => act(() => button("启动").click());

beforeEach(() => {
  resetModelListsForTests();
  state.machines = [{ id: "mac", name: "Mac", online: true }, { id: "studio", name: "Studio", online: true }];
  launched.length = 0;
  mode = { mac: "fail", studio: "ok" };
  held = [];
  launcherReads = [];
  localStorage.clear();
  localStorage.setItem("new.machine", "mac");
  localStorage.setItem("new.cwd.mac", "/code/app");
  localStorage.setItem("new.cwd.studio", "/code/studio-app");
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/model-config/purposes/")) return json(PURPOSE);
    if (/\/launchers\/[^/?]+\/models/.test(url)) return json(LIST);
    const ls = /\/machines\/([^/]+)\/launchers(?:\?|$)/.exec(url);
    if (ls) {
      const mid = decodeURIComponent(ls[1]);
      launcherReads.push(mid);
      const ok = () => json({ launchers: LAUNCHERS, default: { provider: "claude", model: "opus", effort: "high" } });
      if (mode[mid] === "hold") return new Promise<Response>((resolve) => held.push({ mid, answer: (failed) => resolve(failed ? json(TIMED_OUT, 502) : ok()) }));
      return mode[mid] === "fail" ? json(TIMED_OUT, 502) : ok();
    }
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

async function open() {
  act(() => root.render(<NewAgent open onClose={() => {}} />));
  await flush();
}

describe("New agent when a computer's launchers can't be read (#10952)", () => {
  it("leaves the spinner for the pill's failed state, and says why under the box", async () => {
    await open();
    expect(launcherReads).toEqual(["mac"]);
    expect(pill().dataset.modelPill).toBe("failed");
    expect(pill().disabled).toBe(false);
    expect(spinning()).toBe(false);
    expect(pill().textContent).toContain("模型加载失败");
    expect(pill().querySelector("svg.text-red")).not.toBeNull(); // the warning
    // the reason is on the page, not only in a toast that goes away
    expect(failedLine()!.textContent).toContain("无法读取这台电脑的模型列表，仍可使用其默认模型启动。");
    expect(failedLine()!.textContent).toContain("请求超时");
    expect(failedLine()!.querySelector("button")!.textContent).toBe("重试");
  });

  it("still starts with the computer's default after the failure", async () => {
    await open();
    expect(button("启动").disabled).toBe(false);
    start();
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ machineId: "mac", cwd: "/code/app", launcher: "" });
    expect(launched[0].model).toBeUndefined();
    expect(launched[0].effort).toBeUndefined();
  });

  it("Retry under the box reads the launchers again, and a success brings the pill back", async () => {
    await open();
    mode.mac = "hold";
    act(() => failedLine()!.querySelector("button")!.click());
    await flush();
    expect(launcherReads).toEqual(["mac", "mac"]);
    // reading again: the spinner, no failure line, Start waits as on the first read
    expect(spinning()).toBe(true);
    expect(pill().disabled).toBe(true);
    expect(failedLine()).toBeNull();
    expect(button("启动").disabled).toBe(true);
    await act(async () => held[0].answer(false));
    await flush();
    expect(ready()).toBe(true);
    expect(pill().title).toBe("Claude Code · Opus 5.5 · High");
    expect(failedLine()).toBeNull();
    expect(button("启动").disabled).toBe(false);
  });

  it("a click on the failed pill reads them again; failing again shows the failure again", async () => {
    await open();
    act(() => pill().click());
    await flush();
    expect(launcherReads).toEqual(["mac", "mac"]);
    expect(pill().dataset.modelPill).toBe("failed");
    expect(failedLine()).not.toBeNull();
    // no panel opens on a failed pill: there is nothing to list
    expect(q("[data-model-panel]")).toBeNull();
    mode.mac = "ok";
    act(() => pill().click());
    await flush();
    expect(ready()).toBe(true);
    expect(pill().title).toBe("Claude Code · Opus 5.5 · High");
    expect(failedLine()).toBeNull();
  });

  it("a read still on its way for the computer picked before doesn't land on the one picked since", async () => {
    mode.mac = "hold";
    await open();
    expect(spinning()).toBe(true);
    act(() => machineChip("Studio").click());
    await flush();
    expect(ready()).toBe(true);
    // the Mac's read fails now: the Studio's pill and lines stay as they are
    await act(async () => held[0].answer(true));
    await flush();
    expect(ready()).toBe(true);
    expect(pill().title).toBe("Claude Code · Opus 5.5 · High");
    expect(failedLine()).toBeNull();
    start();
    expect(launched[0]).toMatchObject({ machineId: "studio", cwd: "/code/studio-app" });
  });

  it("a computer's failure stays with it: another one picked reads its own, and going back reads again", async () => {
    await open();
    expect(failedLine()).not.toBeNull();
    act(() => machineChip("Studio").click());
    await flush();
    expect(ready()).toBe(true);
    expect(failedLine()).toBeNull();
    mode.mac = "ok";
    act(() => machineChip("Mac").click());
    await flush();
    expect(launcherReads).toEqual(["mac", "studio", "mac"]);
    expect(ready()).toBe(true);
    expect(failedLine()).toBeNull();
  });
});
