import type { Notification as ElectronNotification } from "electron";

/** The Electron notification support check, not a renderer-controlled permission. */
export function systemNotifyPermission(supported: boolean): string {
  return supported ? "granted" : "denied";
}

export type NotificationConstructor = typeof ElectronNotification;
