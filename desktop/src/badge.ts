import { app, nativeImage } from "electron";
import type { BrowserWindow } from "electron";
import { NEEDS_YOU_OVERLAY } from "./badge-art.js";

/** One badge definition for every desktop surface: the web badge count, not an independent formatter. */
export function setBadge(window: BrowserWindow, count: number): void {
  app.setBadgeCount(count);
  if (process.platform === "win32") {
    window.setOverlayIcon(count ? nativeImage.createFromDataURL(NEEDS_YOU_OVERLAY) : null, count ? `Needs you: ${count}` : "");
  }
}
