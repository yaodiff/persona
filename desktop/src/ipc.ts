import { app, ipcMain, Notification } from "electron";
import { setBadge } from "./badge.js";
import type { BrowserWindow } from "electron";
import { inHub } from "./urls.js";
import { notificationRoute } from "./contract.cjs";
import { DesktopIPCOwner } from "./ipc-owner.js";
import type { HubAddress } from "./urls.js";

function text(value: unknown, maximum: number): string {
  return typeof value === "string" ? value.trim().slice(0, maximum) : "";
}

export class DesktopNotifications {
  private readonly notifications = new Map<string, Notification>();
  constructor(private readonly open: (route: string) => void) {}

  show(value: unknown, hub: HubAddress | undefined): boolean {
    if (!value || typeof value !== "object") return false;
    const v = value as Record<string, unknown>;
    const title = text(v.title, 200), body = text(v.body, 500), tag = text(v.tag, 300);
    const route = typeof v.route === "string" ? notificationRoute(v.route) : undefined;
    if (!title || !tag || !route || !hub) return false;
    const target = new URL(route, hub.url);
    if (!inHub(hub, target)) return false;
    this.close(tag);
    const notification = new Notification({ title, body, silent: v.silent === true });
    this.notifications.set(tag, notification);
    notification.on("click", () => this.open(route));
    notification.on("close", () => {
      if (this.notifications.get(tag) === notification) this.notifications.delete(tag);
    });
    try {
      notification.show();
      return true;
    } catch {
      this.close(tag);
      return false;
    }
  }

  close(tag: string): void {
    this.notifications.get(tag)?.close();
    this.notifications.delete(tag);
  }

  closeAll(): void {
    for (const tag of [...this.notifications.keys()]) this.close(tag);
  }
}

const ipcOwner = new DesktopIPCOwner();

export function installDesktopIPC(window: BrowserWindow, deps: {
  readonly hub: () => HubAddress | undefined;
  readonly notifications: DesktopNotifications;
  readonly presence: () => { active: boolean; notifies: boolean };
  readonly openLocalSettings: () => void;
}): () => void {
  const authorized = (event: Electron.IpcMainEvent | Electron.IpcMainInvokeEvent): boolean => {
    const hub = deps.hub();
    return event.sender === window.webContents && !window.isDestroyed() && event.senderFrame === event.sender.mainFrame && !!hub && inHub(hub, event.senderFrame.url);
  };
  const names = ["desktop:notification-permission", "desktop:badge", "desktop:notify", "desktop:notifications-clear", "desktop:notification-close", "desktop:presence", "desktop:open-local-settings"];
  const releaseOwnership = ipcOwner.install();
  for (const name of names) ipcMain.removeAllListeners(name);
  for (const name of ["desktop:login-get", "desktop:login-set"]) ipcMain.removeHandler(name);
  const listen = (name: string, handler: Parameters<typeof ipcMain.on>[1]) => {
    ipcMain.on(name, handler);
  };
  listen("desktop:notification-permission", (event: Electron.IpcMainEvent) => {
    event.returnValue = authorized(event) && Notification.isSupported() ? "granted" : "denied";
  });
  listen("desktop:badge", (event: Electron.IpcMainEvent, value: unknown) => {
    if (!authorized(event)) return;
    if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0 || value > 1_000_000) return;
    setBadge(window, value);
  });
  listen("desktop:notify", (event: Electron.IpcMainEvent, value: unknown) => {
    event.returnValue = authorized(event) ? deps.notifications.show(value, deps.hub()) : false;
  });
  listen("desktop:notifications-clear", (event: Electron.IpcMainEvent) => {
    if (authorized(event)) deps.notifications.closeAll();
  });
  listen("desktop:notification-close", (event: Electron.IpcMainEvent, value: unknown) => {
    if (!authorized(event) || typeof value !== "string") return;
    deps.notifications.close(value);
  });
  listen("desktop:presence", (event: Electron.IpcMainEvent) => {
    event.returnValue = authorized(event) ? deps.presence() : { active: false, notifies: false };
  });
  listen("desktop:open-local-settings", (event: Electron.IpcMainEvent) => {
    // A request to show the separate local window only. No Link command, hub credential or local data crosses here.
    if (authorized(event)) deps.openLocalSettings();
  });
  ipcMain.handle("desktop:login-get", function loginGet(event) {
    if (!authorized(event)) return { enabled: false };
    const settings = app.getLoginItemSettings();
    return { enabled: settings.openAtLogin, requiresApproval: process.platform === "darwin" && settings.status === "requires-approval" };
  });
  ipcMain.handle("desktop:login-set", function loginSet(event, enabled: unknown) {
    if (!authorized(event)) return { enabled: false };
    if (typeof enabled !== "boolean") throw new TypeError("Launch at login expects a boolean");
    app.setLoginItemSettings({ openAtLogin: enabled });
    const settings = app.getLoginItemSettings();
    return { enabled: settings.openAtLogin, requiresApproval: process.platform === "darwin" && settings.status === "requires-approval" };
  });
  return () => {
    releaseOwnership();
    if (ipcOwner.active) return;
    for (const name of names) ipcMain.removeAllListeners(name);
    for (const name of ["desktop:login-get", "desktop:login-set"]) ipcMain.removeHandler(name);
    deps.notifications.closeAll();
  };
}
