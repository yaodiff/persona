// @vitest-environment jsdom
// Machine detail, live screen (#3247): one "View screen" link in the top bar while Screen Sharing answers, whatever Cua Driver's
// state; "Computer control" keeps no link of its own, says how to turn Screen Sharing on while it is off, and points to the
// top bar while macOS asks for access. Both read Screen Sharing from the machine list. The screen is a module's page of
// its own (core/module-host.ts `standalonePages`, the screen-share module): with no module drawing it, no link.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { NO_SCREEN_SHARING, SCREEN_PAGE } from "../../lib/computer";
import { registerModule, resetModulesForTest } from "../../core/module-host";
import { forgetModuleSwitches, setModuleSwitchedOff } from "../../core/module-switch";
import type { Machine } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const fake = vi.hoisted(() => ({ machines: [] as Machine[] }));
vi.mock("../../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: true, machines: fake.machines }) };
});

const { MachineDetail } = await import("./MachineDetail");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const screenLinks = () => [...host.querySelectorAll<HTMLAnchorElement>('a[href="/screen/mac"]')];
const section = () => host.querySelector('[data-testid="computer-section"]')!;
const ALLOW_HERE = t("machines-computersection.allow_from_here");
const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });

// What the node reports while Cua Driver's daemon is not running (computer.status: permissions unknown, the hub's note).
const NOTE = "Cua Driver 没在运行，助手要用时会自动打开它"; // i18n-ignore: the hub's note, shown as it comes
const daemonDown = { enabled: true, installed: true, daemon: false, ready: false, accessibility: "unknown", screen_recording: "unknown", note: NOTE };
const mac = (computer: Machine["computer"], extra: Partial<Machine> = {}) => ({ id: "mac", name: "Mac", online: true, os: "darwin", computer, ...extra }) as Machine;

async function open(m: Machine) {
  fake.machines = [m];
  act(() => root.render(<MachineDetail subview="management" mid="mac" onToggleSidebar={() => {}} />));
  await flush();
}

beforeEach(() => {
  // a synthetic module draws the screen page (the core-only lane has none of the real ones)
  resetModulesForTest();
  registerModule({ id: "live-screen", title: () => "Live screen", description: () => "", standalonePages: [SCREEN_PAGE], load: async () => ({ Standalone: () => null }) });
  // every read fails: the page keeps the machine list's copy
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response(JSON.stringify({ error: "not found" }), { status: 404 }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  resetModulesForTest();
  localStorage.removeItem("persona.modules.off");
  forgetModuleSwitches();
});

describe("MachineDetail live screen", () => {
  it("offers the screen in the top bar while Cua Driver is not running", async () => {
    await open(mac({ ...daemonDown, screen_sharing: true }));
    expect(section().textContent).toContain(NOTE);
    const links = screenLinks();
    expect(links).toHaveLength(1);
    expect(links[0]!.closest("header")).not.toBeNull();
    expect(links[0]!.textContent).toBe("查看屏幕");
    expect(links[0]!.target).toBe("_blank");
    expect(section().textContent).not.toContain(NO_SCREEN_SHARING);
  });

  it("keeps the one entry when Cua Driver is ready: the section has no screen link or button", async () => {
    await open(mac({ enabled: true, installed: true, daemon: true, ready: true, accessibility: "granted", screen_recording: "granted", screen_sharing: true }));
    expect(screenLinks()).toHaveLength(1);
    expect(section().querySelector('a[href*="/screen/"]')).toBeNull();
    expect([...section().querySelectorAll("a, button")].map((e) => e.textContent ?? "").join(" ")).not.toMatch(/VNC|查看屏幕/);
    expect(section().textContent).not.toContain("VNC");
    expect(section().textContent).not.toContain(ALLOW_HERE);
  });

  it("points to the top bar to click Allow while macOS's permission dialogs are up", async () => {
    const granted = { enabled: true, installed: true, daemon: true, ready: false, accessibility: "missing", screen_recording: "granted", job: { kind: "grant", state: "done" } };
    await open(mac({ ...granted, screen_sharing: true }));
    expect(screenLinks()).toHaveLength(1);
    expect(section().textContent).toContain(ALLOW_HERE);
    await open(mac({ ...granted, screen_sharing: false }));
    expect(section().textContent).not.toContain(ALLOW_HERE);
    expect(section().textContent).toContain(NO_SCREEN_SHARING);
  });

  it("reads Screen Sharing from the machine list for both the link and the hint", async () => {
    // the section's own read is older than the list's answer: the link and the hint still agree
    vi.stubGlobal("fetch", vi.fn((url: string) =>
      Promise.resolve(String(url).endsWith("/machines/mac/computer") ? json({ ...daemonDown, screen_sharing: false }) : new Response(JSON.stringify({ error: "not found" }), { status: 404 }))));
    await open(mac({ ...daemonDown, screen_sharing: true }));
    expect(screenLinks()).toHaveLength(1);
    expect(section().textContent).not.toContain(NO_SCREEN_SHARING);
  });

  it("says how to turn on Screen Sharing while it is off, with no link", async () => {
    await open(mac({ ...daemonDown, screen_sharing: false }));
    expect(screenLinks()).toHaveLength(0);
    expect(section().textContent).toContain(NO_SCREEN_SHARING);
  });

  it("offers the screen through ego while Computer control is off", async () => {
    await open(mac({ ...daemonDown, enabled: false, screen_sharing: true }, { browser: { enabled: true, installed: true, set_up: true, ready: true, screen_sharing: true } }));
    expect(screenLinks()).toHaveLength(1);
  });

  it("offers no link while no module draws the screen, or its module is switched off", async () => {
    resetModulesForTest();
    await open(mac({ ...daemonDown, screen_sharing: true }));
    expect(screenLinks()).toHaveLength(0);
    registerModule({ id: "live-screen", title: () => "Live screen", description: () => "", standalonePages: [SCREEN_PAGE], load: async () => ({ Standalone: () => null }) });
    await open(mac({ ...daemonDown, screen_sharing: true }));
    expect(screenLinks()).toHaveLength(1);
    act(() => void setModuleSwitchedOff("live-screen", true));
    await flush();
    expect(screenLinks()).toHaveLength(0);
    act(() => void setModuleSwitchedOff("live-screen", false));
    await flush();
    expect(screenLinks()).toHaveLength(1);
  });

  it("offers nothing while the Mac is offline", async () => {
    await open(mac({ ...daemonDown, screen_sharing: true }, { online: false }));
    expect(screenLinks()).toHaveLength(0);
    await open(mac({ ...daemonDown, screen_sharing: false }, { online: false }));
    expect(section().textContent).not.toContain(NO_SCREEN_SHARING);
  });
});
