/**
 * The versioned boundary between the hub-hosted web renderer and its optional Electron shell.
 *
 * This file is types only. Both sides import it with `import type`; it emits no runtime module and owns no IPC.
 * The desktop may be older than the web it loads: version 1 is additive only. The web must feature-detect a method
 * added after an older shell before calling it. An incompatible change increments `version` and needs a migration path.
 */
export interface DesktopNotification {
  readonly title: string;
  readonly body: string;
  readonly tag: string;
  /** Existing BASE-prefixed SPA path, never a complete remote URL. */
  readonly route: string;
  readonly silent?: boolean;
  readonly ix?: string;
}

export interface DesktopPresence {
  readonly active: boolean;
  readonly notifies: boolean;
}

export interface DesktopLaunchAtLogin {
  readonly enabled: boolean;
  readonly requiresApproval?: boolean;
}

export interface DesktopBridge {
  readonly version: 1;
  readonly platform: string;
  readonly deviceName: string;
  notificationPermission(): string;
  setBadge(count: number): void;
  /** false keeps the existing browser notification fallback. */
  notify(notification: DesktopNotification): boolean;
  closeNotification(tag: string): void;
  clearNotifications(): void;
  onOpenNotification(callback: (route: string) => void): () => void;
  desktopPresence(): DesktopPresence;
  onPresenceChange(callback: (presence: DesktopPresence) => void): () => void;
  setLaunchAtLogin(enabled: boolean): Promise<DesktopLaunchAtLogin>;
  getLaunchAtLogin(): Promise<DesktopLaunchAtLogin>;
  /** Opens the shell-owned local options window. Older shells omit this additive capability. */
  openLocalSettings?(): void;
}
