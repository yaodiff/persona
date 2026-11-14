// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DesktopBridge } from "./desktop-types";

function bridgeMock(overrides: Partial<DesktopBridge>): DesktopBridge {
  return {
    version: 1, platform: "darwin", deviceName: "Desktop",
    notificationPermission: () => "granted", setBadge: vi.fn(), notify: () => true,
    closeNotification: vi.fn(), clearNotifications: vi.fn(), onOpenNotification: vi.fn(),
    desktopPresence: () => ({ active: true, notifies: true }), onPresenceChange: () => () => {},
    setLaunchAtLogin: async () => ({ enabled: false }), getLaunchAtLogin: async () => ({ enabled: false }),
    ...overrides,
  };
}

describe("Electron desktop bridge", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("uses the existing notification text, tag and quiet flag without a browser duplicate", async () => {
    const notify = vi.fn(() => true);
    const closeNotification = vi.fn();
    vi.stubGlobal("window", Object.assign(window, { personaDesktop: bridgeMock({ notify, closeNotification }) }));
    vi.stubGlobal("localStorage", { getItem: (key: string) => key === "persona.systemNotify" ? "1" : null, setItem: vi.fn(), removeItem: vi.fn() });
    vi.stubGlobal("navigator", {});
    const { postNotification, closeNotification: close } = await import("./notify");
    postNotification({ title: "Fix login", body: "Allow?", tag: "m/a#needs", url: "/persona/s/m/a", silent: true }, () => {});
    expect(notify).toHaveBeenCalledExactlyOnceWith({ title: "Fix login", body: "Allow?", tag: "m/a#needs", route: "/persona/s/m/a", silent: true, ix: undefined });
    close("m/a#needs");
    expect(closeNotification).toHaveBeenCalledExactlyOnceWith("m/a#needs");
  });

  it("retains the browser fallback if native display is rejected", async () => {
    const note = { close: vi.fn() };
    const BrowserNotification = vi.fn(function () { return note; });
    vi.stubGlobal("window", Object.assign(window, { personaDesktop: bridgeMock({ notify: () => false }) }));
    vi.stubGlobal("localStorage", { getItem: (key: string) => key === "persona.systemNotify" ? "1" : null, setItem: vi.fn(), removeItem: vi.fn() });
    vi.stubGlobal("navigator", {});
    vi.stubGlobal("Notification", Object.assign(BrowserNotification, { permission: "granted" }));
    const { postNotification } = await import("./notify");
    postNotification({ title: "Done", body: "Finished", tag: "m/a#finished", url: "/persona/s/m/a" }, () => {});
    expect(BrowserNotification).toHaveBeenCalledExactlyOnceWith("Done", expect.objectContaining({ body: "Finished", tag: "m/a#finished" }));
  });
});
