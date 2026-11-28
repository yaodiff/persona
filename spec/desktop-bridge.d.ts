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

/** The island at the MacBook notch (#7669): whether this shell can draw it here (macOS) and whether it is on. */
export interface DesktopIsland {
  readonly supported: boolean;
  readonly enabled: boolean;
}

/** One row of the island's list, its words already in the user's language. */
export interface DesktopIslandRow {
  readonly title: string;
  /** `needs_you`, `working` or `unread`: the color and symbol of its mark. */
  readonly state: string;
  /** The state's name, as the Agents list says it. */
  readonly label: string;
  /** The line the Agents list shows under it, "" when none. */
  readonly line: string;
  /** Existing BASE-prefixed SPA path, as a notification's. */
  readonly route: string;
}

/** What the island shows: the Agents by state as the Agents list counts them, and the first rows, most urgent first. */
export interface DesktopIslandState {
  readonly needsYou: number;
  readonly working: number;
  readonly unread: number;
  readonly rows: readonly DesktopIslandRow[];
  /** What the pill says to VoiceOver: "2 need you, 3 working". */
  readonly summary: string;
}

/** Window frame state for the macOS unified titlebar (#16607). */
export interface DesktopWindowChrome {
  readonly unifiedTitlebar: boolean;
  readonly fullScreen: boolean;
  readonly focused: boolean;
}

/** Optional macOS window surface style synced with Settings › Advanced › Appearance. */
export type DesktopSurfaceStyle = "default" | "liquid-glass";

/** Web appearance choice synced with Electron nativeTheme.themeSource so macOS vibrancy matches the page (#16776). */
export type DesktopThemeSource = "system" | "light" | "dark";

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
  /** The notch island (#7669); absent in shells before it. */
  getIsland?(): Promise<DesktopIsland>;
  setIslandEnabled?(enabled: boolean): Promise<DesktopIsland>;
  /** The island's state; the shell keeps the latest and ignores it while the island is off. */
  setIslandState?(state: DesktopIslandState): void;
  /** Developing Persona (#9675): the shell records its main process while the page's switch is on; off deletes what it
   *  kept. Absent in shells before it. */
  setDevDiagnostics?(on: boolean): Promise<{ on: boolean }>;
  /** What the shell kept, as JSON lines of code-chosen words and numbers, and how many crash dumps it has kept on this
   *  computer since (counted, never read). */
  readDevDiagnostics?(): Promise<{ lines: string[]; crashDumps: number }>;
  /** Unified macOS titlebar window state (#16607); absent in older shells. */
  windowChrome?(): DesktopWindowChrome;
  onWindowChromeChange?(callback: (chrome: DesktopWindowChrome) => void): () => void;
  /** Syncs the optional macOS Liquid Glass vibrancy with Settings › Advanced › Appearance (#16607). */
  setSurfaceStyle?(style: DesktopSurfaceStyle): void;
  /** Syncs Electron nativeTheme.themeSource with the web theme so macOS vibrancy and window backing match (#16776). */
  setThemeSource?(theme: DesktopThemeSource): void;
}
