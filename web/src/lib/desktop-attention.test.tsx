// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));
vi.mock("./router", () => ({ navigate: navigateMock }));

import { useAttentionSignals } from "./attention-signals";
import type { DesktopBridge, DesktopPresence } from "./desktop-types";
import type { Interaction } from "./types";

const { sendMock, connectMock, disconnectMock } = vi.hoisted(() => ({ sendMock: vi.fn(), connectMock: vi.fn(), disconnectMock: vi.fn() }));
let features: string[] = ["desktop_unregister"];
let notifyOn = true;
vi.mock("./store", () => ({
  useStore: (select: (s: { loaded: boolean; interactions: Interaction[] }) => unknown) => select({ loaded: true, interactions: [] }),
  getState: () => ({ mutes: { spaces: [], sessions: [], allowed: [] }, interactions: [], features }),
  onHello: (fn: () => void) => { hello = fn; return () => { hello = undefined; }; },
  send: sendMock, appSocketReady: () => true,
  connect: connectMock, disconnect: disconnectMock,
  hubHas: (list: string[], feature: string) => list.includes(feature),
  DESKTOP_UNREGISTER_FEATURE: "desktop_unregister",
}));
vi.mock("./sessions", () => ({ useCounts: () => ({ badge: 0 }), useAgentSessions: () => [] }));
vi.mock("./notify", async (original) => ({
  ...(await original<typeof import("./notify")>()),
  systemNotifyActive: () => notifyOn,
  onSystemNotifyChange: (callback: () => void) => { changeNotifications = callback; return () => { changeNotifications = undefined; }; },
}));

let hello: (() => void) | undefined;
let active: boolean | undefined;
let changePresence: ((value: boolean) => void) | undefined;
let changeNotifications: (() => void) | undefined;

function desktopMock(overrides: Partial<DesktopBridge> = {}): DesktopBridge {
  return {
    version: 1, platform: "linux", deviceName: "Desktop",
    notificationPermission: () => "granted",
    setBadge: vi.fn(), notify: () => true, closeNotification: vi.fn(), clearNotifications: vi.fn(),
    desktopPresence: () => ({ active: active ?? false, notifies: active ?? false }),
    onOpenNotification: () => () => {},
    onPresenceChange: (callback: (presence: DesktopPresence) => void) => {
      changePresence = (value) => { active = value; callback({ active: value, notifies: value }); };
      return () => { changePresence = undefined; };
    },
    setLaunchAtLogin: async () => ({ enabled: false }),
    getLaunchAtLogin: async () => ({ enabled: false }),
    ...overrides,
  };
}

function Probe() {
  useAttentionSignals("Persona", undefined);
  return null;
}

describe("desktop attention lifecycle", () => {
  let root: Root;
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    features = ["desktop_unregister"];
    notifyOn = true;
    active = true;
    vi.stubGlobal("window", Object.assign(window, { personaDesktop: desktopMock() }));
    hello = undefined;
    changePresence = undefined;
    changeNotifications = undefined;
    root = createRoot(document.createElement("div"));
  });
  afterEach(() => {
    act(() => root.unmount());
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });
  it("registers on authenticated hello, withdraws and restores on unlock, and follows notification settings", async () => {
    await act(async () => root.render(<Probe />));
    await act(async () => hello?.());
    expect(sendMock).toHaveBeenNthCalledWith(1, { type: "register_desktop", device_name: "Desktop" });
    await act(async () => changePresence?.(false));
    expect(sendMock).toHaveBeenNthCalledWith(2, { type: "unregister_desktop" });
    await act(async () => changePresence?.(true));
    expect(connectMock).toHaveBeenCalledOnce();
    expect(sendMock).toHaveBeenNthCalledWith(3, { type: "register_desktop", device_name: "Desktop" });
    await act(async () => { notifyOn = false; changeNotifications?.(); });
    expect(sendMock).toHaveBeenNthCalledWith(4, { type: "unregister_desktop" });
    await act(async () => { notifyOn = true; changeNotifications?.(); });
    expect(sendMock).toHaveBeenNthCalledWith(5, { type: "register_desktop", device_name: "Desktop" });
    await act(async () => root.unmount());
    expect(hello).toBeUndefined();
    expect(changePresence).toBeUndefined();
    expect(changeNotifications).toBeUndefined();
  });
  it("reconnects an old hub when notifications are restored and routes native clicks through the real listener", async () => {
    let click: ((route: string) => void) | undefined;
    let unsubscribe = 0;
    features = [];
    vi.stubGlobal("window", Object.assign(window, { personaDesktop: Object.assign({}, window.personaDesktop!, {
      onOpenNotification: (callback: (route: string) => void) => {
        click = callback;
        return () => { unsubscribe++; click = undefined; };
      },
    }) }));
    await act(async () => root.render(<Probe />));
    await act(async () => hello?.());
    await act(async () => { notifyOn = false; changeNotifications?.(); });
    expect(disconnectMock).toHaveBeenCalledOnce();
    await act(async () => { notifyOn = true; changeNotifications?.(); });
    expect(connectMock).toHaveBeenCalledOnce();
    expect(sendMock).toHaveBeenLastCalledWith({ type: "register_desktop", device_name: "Desktop" });
    click?.("/persona/s/m/a?view=chat");
    click?.("https://example.invalid/not-the-hub");
    click?.("//example.invalid/not-the-hub");
    click?.("/persona\\example.invalid");
    expect(navigateMock).toHaveBeenCalledExactlyOnceWith("/persona/s/m/a?view=chat");
    await act(async () => root.unmount());
    expect(unsubscribe).toBe(1);
    expect(click).toBeUndefined();
  });
  it("keeps the browser lifecycle when an older desktop lacks additive capabilities", async () => {
    const legacy: Partial<DesktopBridge> = desktopMock();
    delete legacy.desktopPresence;
    delete legacy.onPresenceChange;
    delete legacy.onOpenNotification;
    vi.stubGlobal("window", Object.assign(window, { personaDesktop: legacy }));
    await act(async () => root.render(<Probe />));
    await act(async () => hello?.());
    expect(sendMock).toHaveBeenCalledWith({ type: "register_desktop", device_name: "Desktop" });
    await act(async () => root.unmount());
    expect(changePresence).toBeUndefined();
  });
});
