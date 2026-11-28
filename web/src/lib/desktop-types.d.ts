export type { DesktopBridge, DesktopIsland, DesktopIslandRow, DesktopIslandState, DesktopNotification, DesktopPresence, DesktopSurfaceStyle, DesktopThemeSource, DesktopWindowChrome } from "../../../spec/desktop-bridge.d.ts";

declare global {
  interface Window { readonly personaDesktop?: DesktopBridge }
}

export {};
