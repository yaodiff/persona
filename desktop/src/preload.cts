import { contextBridge, ipcRenderer } from "electron";
import type { DesktopBridge, DesktopIslandState, DesktopNotification, DesktopPresence, DesktopSurfaceStyle, DesktopThemeSource, DesktopWindowChrome } from "../../spec/desktop-bridge";

// Fixed, self-contained CommonJS capabilities; no arbitrary IPC, tokens, socket messages or Node access.
// The annotation makes every version-1 capability, including its signature, a compile-time requirement.
// A route for this page waits in the main process until the page's app listens (page-routes.ts, #10282): a new
// document does not listen yet, and onOpenNotification says when it does, after its listener is in place.
ipcRenderer.send("desktop:page-started");
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
    ipcRenderer.send("desktop:notification-listening");
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
  getIsland() { return ipcRenderer.invoke("desktop:island-get"); },
  setIslandEnabled(enabled: boolean) { return ipcRenderer.invoke("desktop:island-set", enabled); },
  setIslandState(state: DesktopIslandState) { ipcRenderer.send("desktop:island-state", state); },
  setDevDiagnostics(on: boolean) { return ipcRenderer.invoke("desktop:dev-diagnostics-set", on); },
  readDevDiagnostics() { return ipcRenderer.invoke("desktop:dev-diagnostics-read"); },
  windowChrome(): DesktopWindowChrome { return ipcRenderer.sendSync("desktop:window-chrome"); },
  onWindowChromeChange(callback: (chrome: DesktopWindowChrome) => void): () => void {
    const listener = (_event: Electron.IpcRendererEvent, chrome: DesktopWindowChrome) => callback(chrome);
    ipcRenderer.on("desktop:window-chrome-changed", listener);
    return () => { ipcRenderer.removeListener("desktop:window-chrome-changed", listener); };
  },
  setSurfaceStyle(style: DesktopSurfaceStyle) { ipcRenderer.send("desktop:surface-style", style); },
  setThemeSource(theme: DesktopThemeSource) { ipcRenderer.send("desktop:theme-source", theme); },
} satisfies DesktopBridge);

contextBridge.exposeInMainWorld("personaDesktop", api);
