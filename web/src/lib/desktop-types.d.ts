export type { DesktopBridge, DesktopNotification, DesktopPresence } from "../../../spec/desktop-bridge.d.ts";

declare global {
  interface Window { readonly personaDesktop?: DesktopBridge }
}

export {};
