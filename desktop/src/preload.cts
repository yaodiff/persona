import { contextBridge, ipcRenderer } from "electron";
import type { DesktopBridge, DesktopNotification, DesktopPresence } from "../../spec/desktop-bridge";

// Fixed, self-contained CommonJS capabilities; no arbitrary IPC, tokens, socket messages or Node access.
// The annotation makes every version-1 capability, including its signature, a compile-time requirement.
const api = Object.freeze({
  version: 1,
  platform: process.platform,
  deviceName: "Persona desktop",
  setBadge(count: number) { ipcRenderer.send("desktop:badge", count); },
  notificationPermission(): string { return ipcRenderer.sendSync("desktop:notification-permission"); },
  notify(notification: DesktopNotification): boolean {
    return ipcRenderer.sendSync("desktop:notify", notification) === true;
  },
  closeNotification(tag: string) { ipcRenderer.send("desktop:notification-close", tag); },
  clearNotifications() { ipcRenderer.send("desktop:notifications-clear"); },
  onOpenNotification(callback: (route: string) => void): () => void {
    const listener = (_event: Electron.IpcRendererEvent, route: string) => callback(route);
    ipcRenderer.on("desktop:notification-open", listener);
    return () => { ipcRenderer.removeListener("desktop:notification-open", listener); };
  },
  desktopPresence(): DesktopPresence { return ipcRenderer.sendSync("desktop:presence"); },
  onPresenceChange(callback: (presence: DesktopPresence) => void): () => void {
    const listener = (_event: Electron.IpcRendererEvent, presence: DesktopPresence) => callback(presence);
    ipcRenderer.on("desktop:presence-changed", listener);
    return () => { ipcRenderer.removeListener("desktop:presence-changed", listener); };
  },
  setLaunchAtLogin(enabled: boolean) { return ipcRenderer.invoke("desktop:login-set", enabled); },
  getLaunchAtLogin() { return ipcRenderer.invoke("desktop:login-get"); },
  openLocalSettings() { ipcRenderer.send("desktop:open-local-settings"); },
} satisfies DesktopBridge);

contextBridge.exposeInMainWorld("personaDesktop", api);
