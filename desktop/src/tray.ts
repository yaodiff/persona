import { app, Menu, Tray, nativeImage } from "electron";
import type { BrowserWindow } from "electron";
import { PERSONA_TRAY_ICON } from "./badge-art.js";
import { trayTemplate } from "./tray-menu.js";
import type { TrayLabels } from "./tray-menu.js";
import type { UpdateAction, UpdateMenuItem } from "./update-state.js";

/** Closing a window leaves the signed-in renderer alive, so notifications and presence do not depend on a visible tab. */
export function closeToTray(window: BrowserWindow, quitting: () => boolean): void {
  window.on("close", event => {
    if (quitting()) return;
    event.preventDefault();
    window.hide();
  });
  if (process.platform === "darwin") {
    const activate = () => window.show();
    app.on("activate", activate);
    window.once("closed", () => app.removeListener("activate", activate));
  }
}

export interface DesktopTray {
  readonly tray: Tray;
  /** Replaces the update section. An empty list removes it (development runs and systems without updates). */
  setUpdate(items: readonly UpdateMenuItem[]): void;
}

export function installTray(labels: TrayLabels, open: () => void, quit: () => void, update: (action: UpdateAction) => void): DesktopTray {
  // Resolve the current main window at click time: the local setup window may have closed before it existed.
  const icon = nativeImage.createFromDataURL(PERSONA_TRAY_ICON);
  const tray = new Tray(icon.resize({ width: 16, height: 16 }));
  tray.setToolTip("Persona");
  const draw = (items: readonly UpdateMenuItem[]) => tray.setContextMenu(Menu.buildFromTemplate(trayTemplate(labels, items, { open, quit, update })));
  draw([]);
  tray.on("click", open);
  return { tray, setUpdate: draw };
}
