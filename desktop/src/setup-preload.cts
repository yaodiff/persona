import { contextBridge, ipcRenderer } from "electron";
import type { LinkCommand } from "./link-contract.cjs";

const setup = Object.freeze({
  saveHub(address: string) { return ipcRenderer.invoke("desktop:hub-set", address); },
  getHub() { return ipcRenderer.invoke("desktop:hub-get"); },
  openExternal(url: string) { return ipcRenderer.invoke("desktop:open-external", url); },
  linkStatus() { return ipcRenderer.invoke("desktop:link-status"); },
  linkCommand(command: LinkCommand) { return ipcRenderer.invoke("desktop:link-command", command); },
  getLaunchAtLogin() { return ipcRenderer.invoke("desktop:setup-login-get"); },
  setLaunchAtLogin(enabled: boolean) { return ipcRenderer.invoke("desktop:setup-login-set", enabled); },
});
contextBridge.exposeInMainWorld("desktopSetup", setup);
