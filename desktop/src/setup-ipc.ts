import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { ipcMain, shell, app } from "electron";
import type { BrowserWindow } from "electron";
import { parseHubAddress, externalBrowserURL } from "./urls.js";
import type { HubAddress } from "./urls.js";
import { linkStatus, runPersonaLink } from "./link.js";
import type { LinkCommand } from "./link-contract.cjs";
import { desktopLocale, loadStrings } from "./strings.js";

/** Setup is a local file window. Its sender is validated again on every privileged action. */
export function installSetupIPC(window: BrowserWindow, deps: {
  readonly hub: () => HubAddress | undefined;
  readonly connect: (raw: string) => Promise<{ ok: boolean; error?: string }>;
}): () => void {
  const setupURL = pathToFileURL(join(import.meta.dirname, "setup.html")).href;
  const fromSetup = (sender: Electron.WebContents, frame: Electron.WebFrameMain | null): boolean => sender === window.webContents && !window.isDestroyed() && frame === sender.mainFrame && sender.getURL() === setupURL;
  // Setup owns separate channels: closing it must not remove the hub window's login-item handlers.
  const names = ["desktop:hub-get", "desktop:hub-set", "desktop:open-external", "desktop:link-status", "desktop:link-command", "desktop:setup-login-get", "desktop:setup-login-set"];
  for (const name of names) ipcMain.removeHandler(name);
  ipcMain.handle("desktop:hub-get", async event => {
    if (!fromSetup(event.sender, event.senderFrame)) return { address: "" };
    const locale = desktopLocale(app.getLocale());
    const strings = await loadStrings(join(import.meta.dirname, "locales"), locale);
    return { address: deps.hub()?.url.href ?? "", locale, strings };
  });
  ipcMain.handle("desktop:hub-set", async (event, value: unknown) => {
    if (!fromSetup(event.sender, event.senderFrame) || typeof value !== "string") return { ok: false, error: "Enter the hub's web address." };
    try { parseHubAddress(value); } catch { return { ok: false, error: "Enter a valid HTTP or HTTPS hub address." }; }
    return deps.connect(value);
  });
  ipcMain.handle("desktop:open-external", async (event, value: unknown) => {
    if (!fromSetup(event.sender, event.senderFrame) || typeof value !== "string") return;
    const url = externalBrowserURL(value);
    if (url) await shell.openExternal(url);
  });
  ipcMain.handle("desktop:link-status", async event => fromSetup(event.sender, event.senderFrame) ? linkStatus() : { available: false });
  ipcMain.handle("desktop:setup-login-get", async event => {
    if (!fromSetup(event.sender, event.senderFrame)) return { enabled: false };
    const settings = app.getLoginItemSettings();
    return { enabled: settings.openAtLogin, requiresApproval: process.platform === "darwin" && settings.status === "requires-approval" };
  });
  ipcMain.handle("desktop:setup-login-set", async (event, enabled: unknown) => {
    if (!fromSetup(event.sender, event.senderFrame) || typeof enabled !== "boolean") return { enabled: false };
    app.setLoginItemSettings({ openAtLogin: enabled });
    const settings = app.getLoginItemSettings();
    return { enabled: settings.openAtLogin, requiresApproval: process.platform === "darwin" && settings.status === "requires-approval" };
  });
  ipcMain.handle("desktop:link-command", async (event, command: LinkCommand) => {
    if (!fromSetup(event.sender, event.senderFrame) || !command || typeof command !== "object") return { ok: false, stdout: "", stderr: "Unsupported command." };
    return runPersonaLink(command);
  });
  return () => { for (const name of names) ipcMain.removeHandler(name); };
}
