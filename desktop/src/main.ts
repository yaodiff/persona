import { join } from "node:path";
import { BrowserWindow, Menu, app, dialog, nativeTheme, protocol, screen, shell, powerMonitor, Notification } from "electron";
import { parseDeepLink, parseHubAddress, inHub } from "./urls.js";
import { AdminWindow, keepNavigation, openNewWindowLink } from "./hub-links.js";
import type { HubAddress } from "./urls.js";
import { appPairingURL } from "./pairing.js";
import { parseConnectInput } from "./connect-input.js";
import { classifyConnectError } from "./connect-errors.js";
import { allowPinnedSessionRequest, isValidPin, verifyPinnedCertificate } from "./tls-pin.js";
import type { HubPin } from "./tls-pin.js";
import { installDesktopIPC, DesktopNotifications } from "./ipc.js";
import { applyDesktopThemeSource, parseDesktopThemeSource } from "./theme-source.js";
import { applyMacSurfaceStyle, liquidGlassVibrancy, seedMacSurfaceState } from "./surface-style.js";
import { PageRoutes } from "./page-routes.js";
import { installSetupIPC, installSetupProtocol } from "./setup-ipc.js";
import type { ConnectResponse } from "./setup-ipc.js";
import { SETUP_SCHEME, SETUP_URL } from "./setup-page.js";
import { JSONStore } from "./store.js";
import { restoreWindow, DEFAULT_WINDOW } from "./window-state.js";
import { closeToTray, installTray } from "./tray.js";
import type { DesktopTray } from "./tray.js";
import { desktopLocale, loadStrings } from "./strings.js";
import { appMenuTemplate } from "./app-menu.js";
import { updateMenu } from "./update-state.js";
import type { UpdateMenuItem } from "./update-state.js";
import { hubPartition } from "./sessions.js";
import { installSmokeCapture } from "./smoke.js";
import { installAutoUpdate } from "./updater.js";
import { HubWindowLifecycle } from "./hub-window-lifecycle.js";
import { desktopPresence } from "./presence.js";
import { NotchIsland, installIslandProtocol } from "./island.js";
import { ISLAND_SCHEME } from "./island-page.js";

installSmokeCapture();
// Electron takes this registration once, before the app is ready: every scheme of the app's own pages belongs here.
protocol.registerSchemesAsPrivileged([
  { scheme: ISLAND_SCHEME, privileges: { standard: true, secure: true } },
  { scheme: SETUP_SCHEME, privileges: { standard: true, secure: true } },
]);

let mainWindow: BrowserWindow | undefined;
let hub: HubAddress | undefined;
let hubPins: Record<string, string> = {};
const verifiedLinkPins = new Map<string, string>();
let pendingInitialURL: { readonly origin: string; readonly href: string } | undefined;
let connectionFailed = false;
let quitting = false;
let pendingDeepLink: string | undefined;
let setupWindow: BrowserWindow | undefined;
let screenLocked = false;
let presenceTimer: ReturnType<typeof setInterval> | undefined;
const hubWindow = new HubWindowLifecycle<BrowserWindow | undefined>();
const store = () => JSONStore.inUserData(app.getPath("userData"));
function windowBackground(surface: "hub" | "setup" = "hub"): string {
  if (surface === "setup") {
    // Match --page in setup.css until the local renderer paints.
    return nativeTheme.shouldUseDarkColors ? "#0b0b0c" : "#f1f1f4";
  }
  // Match the Web shell canvas and its prepaint background.
  return nativeTheme.shouldUseDarkColors ? "#0b0b0c" : "#f1f1f4";
}
function applySurfaceStyle(window: BrowserWindow, style: DesktopSurfaceStyle): void {
  if (process.platform !== "darwin") return;
  applyMacSurfaceStyle(window, style, nativeTheme.shouldUseDarkColors, windowBackground());
}
// Each hub window's routes wait until its page listens (page-routes.ts, #10282): no route is sent around them.
const pageRoutes = new WeakMap<BrowserWindow, PageRoutes>();
function openRoute(window: BrowserWindow | undefined, route: string): void {
  if (window && !window.isDestroyed()) pageRoutes.get(window)?.open(route);
}
function activePinFor(address: HubAddress | undefined): HubPin | undefined {
  if (!address) return undefined;
  const fp = hubPins[address.url.origin];
  if (!fp || !isValidPin(fp)) return undefined;
  return { origin: address.url.origin, fp };
}
const windowPins = new WeakMap<BrowserWindow, string | undefined>();
function sessionPinKey(pin: HubPin | undefined): string | undefined {
  return pin ? `${pin.origin}#${pin.fp}` : undefined;
}
async function applySessionPin(window: BrowserWindow, owner: HubAddress): Promise<void> {
  if (window.isDestroyed()) return;
  const pin = activePinFor(owner);
  const ses = window.webContents.session;
  ses.webRequest.onBeforeRequest(
    pin
      ? (details, callback) => {
          callback({ cancel: !allowPinnedSessionRequest(details.url, pin) });
        }
      : null,
  );
  ses.setCertificateVerifyProc((request, callback) => {
    callback(verifyPinnedCertificate({
      hostname: request.hostname,
      certificateData: request.certificate.data,
      verificationResult: request.verificationResult,
    }, pin));
  });
  windowPins.set(window, sessionPinKey(pin));
  await ses.closeAllConnections();
}
// The island at the MacBook notch (#7669): off until the person turns it on; a clicked row opens as a notification does.
const island = new NotchIsland(route => {
  if (mainWindow && !mainWindow.isDestroyed()) {
    mainWindow.show();
    mainWindow.focus();
    openRoute(mainWindow, route);
  } else void showMain();
});

function navigateFromDeepLink(raw: string): void {
  const link = parseDeepLink(raw);
  if (!link) return;
  if (!hub || !mainWindow || mainWindow.isDestroyed()) { pendingDeepLink = raw; void showSetup(); return; }
  if (link.kind === "open" && link.route) openRoute(mainWindow, link.route);
  if (link.kind === "pair-app") {
    const pairing = appPairingURL(hub, raw);
    if (pairing) {
      void mainWindow?.loadURL(pairing);
    }
  }
  if (link.kind === "pair-link") {
    // Link pairing belongs to the separate local app, not to the hub-loaded page. No automatic pairing here.
    void showSetup();
  }
  mainWindow?.show();
}

/** `openAdmin`: the Assistant window sends Hub Admin to its own window (hub-links.ts, #16992); the admin window has none. */
function secureHubWindow(window: BrowserWindow, openAdmin?: (url: string) => void): void {
  const contents = window.webContents;
  const targets = { admin: openAdmin, browser: (url: string) => { void shell.openExternal(url); } };
  contents.setWindowOpenHandler(({ url }) => {
    openNewWindowLink(hub, url, { ...targets, here: page => { void window.loadURL(page); } });
    return { action: "deny" };
  });
  contents.on("will-navigate", (event, url) => {
    if (!keepNavigation(hub, url, targets)) event.preventDefault();
  });
  contents.on("will-redirect", (event, url) => {
    if (!keepNavigation(hub, url, targets)) event.preventDefault();
  });
  contents.on("will-attach-webview", event => event.preventDefault());
  contents.session.setPermissionRequestHandler((_contents, permission, callback) => {
    // The web notification preference still resolves; the OS notification itself comes from Electron.
    callback(permission === "notifications");
  });
  contents.session.setPermissionCheckHandler((_contents, permission) => permission === "notifications");
}

/** Hub Admin's window (#16992): the Assistant window's hub session (its sign-in cookies, TLS pin and permission
 *  handlers) and the same link rules, but no preload, since the admin page uses no desktop capability, and the
 *  system's standard title bar, so no inset under the window controls is needed. */
function createAdminWindow(owner: HubAddress | undefined): BrowserWindow {
  const admin = new BrowserWindow({
    width: 1180,
    height: 800,
    minWidth: 760,
    minHeight: 560,
    show: false,
    title: "Persona",
    autoHideMenuBar: true,
    backgroundColor: windowBackground(),
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
      allowRunningInsecureContent: false,
      devTools: !app.isPackaged,
      nodeIntegrationInWorker: false,
      nodeIntegrationInSubFrames: false,
      webviewTag: false,
      spellcheck: true,
      ...(owner ? { partition: hubPartition(owner) } : {}),
    },
  });
  secureHubWindow(admin);
  admin.once("ready-to-show", () => {
    if (!quitting && !admin.isDestroyed()) admin.show();
  });
  return admin;
}

// A different hub needs a new window and its own persistent session, not navigation of the old window.
async function replaceMainWindow(next: HubAddress, close: (window: BrowserWindow) => void): Promise<void> {
  const previous = mainWindow;
  if (!previous || previous.isDestroyed()) { hubWindow.replace(); return; }
  // Sub-path containment is not equality: /old/ is inside a new / hub, but still needs its own origin-isolated session.
  if (inHub(next, previous.webContents.getURL()) && (() => { try { return parseHubAddress(previous.webContents.getURL()).url.href === next.url.href; } catch { return false; } })()) return;
  // Setting hub first lets closeToTray destroy this window without putting the old hub back on screen.
  hub = next;
  hubWindow.replace();
  const closed = new Promise<void>(done => previous.once("closed", done));
  close(previous);
  await closed;
  mainWindow = undefined;
}

async function showMain(): Promise<BrowserWindow | undefined> {
  if (quitting) return undefined;
  return hubWindow.show(async (): Promise<BrowserWindow | undefined> => {
  const generation = hubWindow.generation;
  const owner = hub;
  if (!owner) {
    pendingInitialURL = undefined;
    if (!setupWindow || setupWindow.isDestroyed()) await showSetup();
    return undefined;
  }
  if (mainWindow && !mainWindow.isDestroyed()) {
    connectionFailed = false;
    const initial = pendingInitialURL && pendingInitialURL.origin === owner.url.origin && inHub(owner, pendingInitialURL.href)
      ? pendingInitialURL.href
      : undefined;
    pendingInitialURL = undefined;
    if (initial || windowPins.get(mainWindow) !== sessionPinKey(activePinFor(owner))) {
      await applySessionPin(mainWindow, owner);
      if (quitting || owner !== hub || generation !== hubWindow.generation || mainWindow.isDestroyed()) return undefined;
    }
    if (initial) await mainWindow.loadURL(initial);
    if (quitting || owner !== hub || generation !== hubWindow.generation || mainWindow.isDestroyed()) return undefined;
    mainWindow.show();
    return mainWindow;
  }
  const saved = await store().read<{ window?: unknown; surfaceStyle?: unknown; themeSource?: unknown }>();
  const state = restoreWindow(saved?.window, screen.getAllDisplays().map(d => d.workArea));
  if (owner !== hub || generation !== hubWindow.generation) return undefined;
  const savedTheme = parseDesktopThemeSource(saved?.themeSource);
  if (savedTheme && nativeTheme.themeSource !== savedTheme) nativeTheme.themeSource = savedTheme;
  const initialStyle: DesktopSurfaceStyle = saved?.surfaceStyle === "liquid-glass" ? "liquid-glass" : "default";
  const initialVibrancy = liquidGlassVibrancy(nativeTheme.shouldUseDarkColors);
  const initialBackground = initialStyle === "liquid-glass" ? "#00000000" : windowBackground();
  const current = new BrowserWindow({
    ...DEFAULT_WINDOW,
    ...state,
    show: false,
    title: "Persona",
    autoHideMenuBar: true,
    backgroundColor: initialBackground,
    ...(process.platform === "darwin" ? {
      titleBarStyle: "hiddenInset" as const,
      trafficLightPosition: { x: 14, y: 19 },
      ...(initialStyle === "liquid-glass" ? { vibrancy: initialVibrancy } : {}),
      visualEffectState: "active" as const,
    } : {}),
    webPreferences: {
      preload: join(import.meta.dirname, "preload.cjs"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
      allowRunningInsecureContent: false,
      devTools: !app.isPackaged,
      nodeIntegrationInWorker: false,
      nodeIntegrationInSubFrames: false,
      webviewTag: false,
      backgroundThrottling: false,
      spellcheck: true,
      ...(owner ? { partition: hubPartition(owner) } : {}),
    },
  });
  mainWindow = current;
  let surfaceStyle: DesktopSurfaceStyle = initialStyle;
  if (process.platform === "darwin") {
    seedMacSurfaceState(current, {
      vibrancy: initialStyle === "liquid-glass" ? initialVibrancy : null,
      background: initialBackground,
    });
  }
  const readWindowChrome = (): DesktopWindowChrome => ({
    unifiedTitlebar: process.platform === "darwin",
    fullScreen: !current.isDestroyed() && current.isFullScreen(),
    focused: !current.isDestroyed() && current.isFocused(),
  });
  const publishChrome = () => {
    if (!current.isDestroyed()) current.webContents.send("desktop:window-chrome-changed", readWindowChrome());
  };
  current.on("enter-full-screen", publishChrome);
  current.on("leave-full-screen", publishChrome);
  const onThemeUpdated = () => {
    if (current.isDestroyed()) return;
    if (process.platform === "darwin") applySurfaceStyle(current, surfaceStyle);
    else current.setBackgroundColor(windowBackground());
  };
  nativeTheme.on("updated", onThemeUpdated);
  const routes = new PageRoutes(route => {
    if (!current.isDestroyed()) current.webContents.send("desktop:notification-open", route);
  });
  pageRoutes.set(current, routes);
  const page = current.webContents.id; // whose island state is whose (NotchIsland.forget)
  if (!app.isPackaged && process.env.PERSONA_DESKTOP_DEBUG === "1") {
    const log = (value: unknown) => {
      const line = `${typeof value === "string" ? value : JSON.stringify(value)}\n`;
      void import("node:fs/promises").then(({ appendFile }) => appendFile(process.env.PERSONA_DESKTOP_DEBUG_LOG ?? "/tmp/persona-desktop-debug.log", line, "utf8"));
    };
    current.webContents.openDevTools({ mode: "detach" });
    current.webContents.on("console-message", event => log(event.message));
    current.webContents.on("preload-error", (_event, _path, error) => log(error));
    current.webContents.on("render-process-gone", (_event, details) => log(details));
    current.webContents.on("did-finish-load", () => {
      void current.webContents.executeJavaScript("({title: document.title, text: document.body.innerText.slice(0, 500), html: document.body.innerHTML.slice(0, 500)})").then(value => log(value));
    });
  }
  // Hub Admin opens beside this window, never in it; it closes with this window, whose hub it belongs to.
  const admin = new AdminWindow(() => createAdminWindow(owner));
  secureHubWindow(current, url => { admin.open(url); });
  closeToTray(current, () => quitting);
  const removeIPC = installDesktopIPC(current, {
    // The partition is immutable, so derive authorization from this window's actual origin, never a later global hub.
    hub: () => {
      const url = current.webContents.getURL();
      if (owner && inHub(owner, url)) return owner;
      try { return parseHubAddress(url); } catch { return undefined; }
    },
    presence: () => desktopPresence(powerMonitor.getSystemIdleTime(), screenLocked, Notification.isSupported()),
    openLocalSettings: () => { void showSetup(); },
    routes,
    island: {
      status: () => island.status(),
      set: async enabled => {
        // Saved first: a choice that can't be kept changes nothing, so the island and the switch saying why agree.
        await store().update({ island: enabled && island.supported });
        return island.setEnabled(enabled);
      },
      state: value => island.setState(value, page),
    },
    notifications: new DesktopNotifications(route => {
      current.show();
      current.focus();
      routes.open(route);
    }),
    windowChrome: readWindowChrome,
    setSurfaceStyle: style => {
      surfaceStyle = style;
      applySurfaceStyle(current, style);
      void store().update({ surfaceStyle: style });
    },
    setThemeSource: theme => {
      applyDesktopThemeSource(nativeTheme, theme, () => applySurfaceStyle(current, surfaceStyle));
      void store().update({ themeSource: theme });
    },
  });
  const save = () => {
    if (current.isDestroyed() || current.isMinimized()) return;
    const maximized = current.isMaximized();
    const bounds = maximized ? current.getNormalBounds() : current.getBounds();
    void store().saveWindow({ ...bounds, maximized });
  };
  current.on("resize", save);
  current.on("move", save);
  current.on("maximize", save);
  current.on("unmaximize", save);
  current.on("close", save);
  current.once("ready-to-show", () => {
    if (quitting || owner !== hub || generation !== hubWindow.generation || current.isDestroyed()) return;
    if (state.maximized) current.maximize();
    current.show();
    if (pendingDeepLink) navigateFromDeepLink(pendingDeepLink);
    pendingDeepLink = undefined;
  });
  // The island's state is this page's (#7669): once the page is gone (a hub change destroys this window, its renderer
  // crashed), the island forgets it, so nothing of this hub stays at the notch; a newer page's state stays.
  current.webContents.on("render-process-gone", () => island.forget(page));
  current.once("closed", () => { nativeTheme.removeListener("updated", onThemeUpdated); removeIPC(); island.forget(page); admin.close(); if (mainWindow === current) mainWindow = undefined; });
  if (owner) {
    await applySessionPin(current, owner);
    const targetURL = pendingInitialURL && pendingInitialURL.origin === owner.url.origin && inHub(owner, pendingInitialURL.href)
      ? pendingInitialURL.href
      : owner.url.href;
    pendingInitialURL = undefined;
    try {
      await current.loadURL(targetURL);
    } catch (error) {
      removeIPC();
      if (mainWindow === current) mainWindow = undefined;
      if (!current.isDestroyed()) current.destroy();
      if (owner === hub && generation === hubWindow.generation) connectionFailed = true;
      if (owner === hub && generation === hubWindow.generation) throw error;
      return undefined;
    }
    if (quitting || owner !== hub || generation !== hubWindow.generation || current.isDestroyed()) {
      removeIPC();
      if (mainWindow === current) mainWindow = undefined;
      if (!current.isDestroyed()) current.destroy();
      return undefined;
    }
    connectionFailed = false;
    return current;
  }
  return undefined;
  });
}

async function showSetup(): Promise<void> {
  if (quitting) return;
  if (setupWindow && !setupWindow.isDestroyed()) { setupWindow.show(); setupWindow.focus(); return; }
  const isSettings = Boolean(mainWindow && !mainWindow.isDestroyed());
  const isRecovery = !isSettings && connectionFailed && Boolean(hub);
  const bounds = isSettings
    ? { width: 760, height: 560, minWidth: 640, minHeight: 480 }
    : isRecovery
      ? { width: 460, height: 440, minWidth: 400, minHeight: 380 }
      : { width: 480, height: 620, minWidth: 420, minHeight: 480 };
  const setup = new BrowserWindow({
    ...bounds,
    show: false,
    title: isSettings ? "Persona Settings" : "Persona",
    autoHideMenuBar: true,
    backgroundColor: windowBackground("setup"),
    ...(process.platform === "darwin" ? { titleBarStyle: "hiddenInset", trafficLightPosition: { x: 16, y: 16 } } : {}),
    webPreferences: {
      preload: join(import.meta.dirname, "setup-preload.cjs"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
      devTools: !app.isPackaged,
      webviewTag: false,
    },
  });
  setupWindow = setup;
  setup.once("ready-to-show", () => {
    if (!quitting && !setup.isDestroyed()) setup.show();
  });
  type ConnectionAttempt = {
    generation?: number;
    next: HubAddress;
    previousHub: HubAddress | undefined;
    previousPins: Record<string, string>;
    previousConnectionFailed: boolean;
    persisting?: boolean;
  };
  let activeConnection: ConnectionAttempt | undefined;
  let connectQueue: Promise<unknown> = Promise.resolve();
  setup.setMenu(null);
  setup.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
  setup.webContents.on("will-navigate", event => event.preventDefault());
  setup.once("closed", () => {
    removeIPC();
    if (setupWindow === setup) setupWindow = undefined;
    // Quitting closes setup too; closing onboarding or recovery exits cleanly instead of respawning (#9458).
    if (quitting) return;
    const attempt = activeConnection;
    if (attempt && !attempt.persisting) {
      activeConnection = undefined;
      const currentGeneration = attempt.generation === undefined || attempt.generation === hubWindow.generation;
      const currentOwner = hub === attempt.next || hub === attempt.previousHub;
      if (currentGeneration && currentOwner) {
        hub = attempt.previousHub;
        hubPins = attempt.previousPins;
        connectionFailed = attempt.previousConnectionFailed;
        pendingInitialURL = undefined;
        hubWindow.replace();
        if (mainWindow && !mainWindow.isDestroyed()) mainWindow.destroy();
      }
      if ((!mainWindow || mainWindow.isDestroyed()) && (!attempt.previousHub || attempt.previousConnectionFailed)) {
        quitting = true;
        app.quit();
      }
      return;
    }
    // Retire a pending connection when its setup closes. A late result must not reopen the last window.
    if (hubWindow.pending) {
      pendingInitialURL = undefined;
      hubWindow.replace();
      if (mainWindow && !mainWindow.isDestroyed()) mainWindow.destroy();
      if (!mainWindow || mainWindow.isDestroyed()) {
        quitting = true;
        app.quit();
      }
      return;
    }
    if (!mainWindow || mainWindow.isDestroyed()) {
      quitting = true;
      app.quit();
    }
  });
  const removeIPC = installSetupIPC(setup, {
    hub: () => hub,
    hasMainWindow: () => Boolean(mainWindow && !mainWindow.isDestroyed()),
    connectionFailed: () => connectionFailed,
    onVerifiedLinkPin: pin => {
      if (isValidPin(pin.fp)) verifiedLinkPins.set(pin.origin, pin.fp);
    },
    connect: async (raw: string): Promise<ConnectResponse> => {
      const parsed = parseConnectInput(raw);
      if (parsed.kind === "link-pair") {
        return {
          ok: false,
          errorCode: "link_pair_input",
          linkURL: parsed.linkURL,
          ...(parsed.suggestedHub ? { suggestedHub: parsed.suggestedHub.url.href } : {}),
          error: "This is a Persona Link pairing code for running Agents on this computer.",
        };
      }
      const next = parsed.hub;
      const verifiedPin = parsed.pin ?? (() => {
        const fp = verifiedLinkPins.get(next.url.origin);
        return fp && isValidPin(fp) ? { origin: next.url.origin, fp } : undefined;
      })();
      const previous = connectQueue;
      let generation: number | undefined;
      const run = async (): Promise<ConnectResponse> => {
      if (quitting || setup.isDestroyed()) return { ok: false, replaced: true, error: "The hub connection was replaced before it finished." };
      const previousHub = hub;
      const previousPins = { ...hubPins };
      const attempt: ConnectionAttempt = { next, previousHub, previousPins, previousConnectionFailed: connectionFailed };
      activeConnection = attempt;
      try {
        await replaceMainWindow(next, window => window.destroy());
        if (quitting || setup.isDestroyed()) return { ok: false, replaced: true, error: "The hub connection was replaced before it finished." };
        const generationValue = hubWindow.generation;
        generation = generationValue;
        attempt.generation = generationValue;
        hub = next;
        if (verifiedPin) {
          hubPins = { ...hubPins, [verifiedPin.origin]: verifiedPin.fp };
        }
        pendingInitialURL = parsed.autoSignIn ? { origin: next.url.origin, href: parsed.initialURL } : undefined;
        const loadedWindow = await showMain();
        // An obsolete or destroyed load cannot persist or close setup.
        if (generation !== hubWindow.generation || hub?.url.href !== next.url.href ||
            !loadedWindow || loadedWindow !== mainWindow || loadedWindow.isDestroyed()) {
          return { ok: false, replaced: true, error: "The hub connection was replaced before it finished." };
        }
        try {
          attempt.persisting = true;
          await store().update({ hub: next.url.href });
          if (verifiedPin) {
            await store().update({ pins: hubPins });
          }
        } catch (error) {
          // Do not leave a live window using a hub that the setup state no longer owns.
          if (generation === hubWindow.generation && hub?.url.href === next.url.href) {
            hub = undefined;
            hubPins = previousPins;
            hubWindow.replace();
            const failedWindow = mainWindow;
            mainWindow = undefined;
            if (failedWindow && !failedWindow.isDestroyed()) failedWindow.destroy();
          }
          if (!quitting && !setup.isDestroyed() && (!setupWindow || setupWindow.isDestroyed())) void showSetup().catch(() => {});
          return { ok: false, error: error instanceof Error ? error.message : "Could not save the hub connection." };
        }
        if (generation !== hubWindow.generation || hub?.url.href !== next.url.href ||
            loadedWindow !== mainWindow || loadedWindow.isDestroyed()) {
          return { ok: false, replaced: true, error: "The hub connection was replaced before it finished." };
        }
        if (activeConnection === attempt) activeConnection = undefined;
        if (!setup.isDestroyed()) setup.close();
        return { ok: true };
      } catch (error) {
        if (generation !== hubWindow.generation || hub?.url.href !== next.url.href) {
          return { ok: false, replaced: true, error: "The hub connection was replaced before it finished." };
        }
        const failedWindow = mainWindow;
        if (failedWindow && !failedWindow.isDestroyed()) {
          // A failed connection must not leave a live window after its generation is retired.
          mainWindow = undefined;
          hubWindow.replace();
          failedWindow.destroy();
        }
        const info = classifyConnectError(error, Boolean(activePinFor(next)));
        hub = previousHub;
        hubPins = previousPins;
        pendingInitialURL = undefined;
        connectionFailed = attempt.previousConnectionFailed;
        hubWindow.replace();
        return {
          ok: false,
          errorCode: info.code,
          ...(info.technical ? { technical: info.technical } : {}),
          error: error instanceof Error ? error.message : "Could not connect to the hub.",
        };
      } finally {
        if (activeConnection === attempt) activeConnection = undefined;
      }
      };
      const result = previous.catch(() => {}).then(run);
      connectQueue = result.catch(() => {});
      return result;
    },
  });
  await setup.loadURL(SETUP_URL);
}

const locked = app.requestSingleInstanceLock();
if (!locked) app.quit();
else {
  // Windows and Linux start the app with the link as an argument when it is not running (macOS: open-url below); it
  // waits like any link that comes before the window, and its route then waits for the page (#10282).
  pendingDeepLink = process.argv.find(value => /^(?:persona|jarvis):\/\//i.test(value));
  app.on("second-instance", (_event, argv) => {
    const raw = argv.find(value => /^(?:persona|jarvis):\/\//i.test(value));
    if (raw) navigateFromDeepLink(raw);
    mainWindow?.show();
    mainWindow?.focus();
  });
  app.on("open-url", (event, raw) => {
    event.preventDefault();
    if (mainWindow) navigateFromDeepLink(raw);
    else pendingDeepLink = raw;
  });
  app.whenReady().then(async () => {
    installIslandProtocol();
    installSetupProtocol();
    const publishPresence = () => {
      if (!mainWindow || mainWindow.isDestroyed()) return;
      const presence = desktopPresence(powerMonitor.getSystemIdleTime(), screenLocked, Notification.isSupported());
      mainWindow.webContents.send("desktop:presence-changed", presence);
    };
    powerMonitor.on("lock-screen", () => { screenLocked = true; publishPresence(); });
    powerMonitor.on("unlock-screen", () => { screenLocked = false; publishPresence(); });
    powerMonitor.on("suspend", () => { screenLocked = true; publishPresence(); });
    // Resume by itself is not proof that the person unlocked the computer; only unlock-screen restores presence.
    powerMonitor.on("resume", publishPresence);
    presenceTimer = setInterval(publishPresence, 30_000);
    const strings = await loadStrings(join(import.meta.dirname, "locales"), desktopLocale(app.getLocale()));
    const applicationMenu = appMenuTemplate(process.platform, app.name, strings);
    if (applicationMenu) {
      Menu.setApplicationMenu(Menu.buildFromTemplate(applicationMenu));
      app.setAboutPanelOptions({ applicationName: "Persona" });
    }
    // The first check starts here, before any window loads; its state is kept until the tray exists to show it.
    let tray: DesktopTray | undefined;
    let updateItems: readonly UpdateMenuItem[] = [];
    const updates = installAutoUpdate({
      changed: state => { updateItems = updateMenu(state, strings); tray?.setUpdate(updateItems); },
      prepareQuit: () => { quitting = true; },
    });
    const saved = await store().read<{ hub?: string; island?: boolean; pins?: Record<string, string> }>();
    if (saved?.pins && typeof saved.pins === "object" && !Array.isArray(saved.pins)) {
      for (const [origin, fp] of Object.entries(saved.pins)) {
        if (typeof fp === "string" && isValidPin(fp)) {
          try {
            hubPins[new URL(origin).origin] = fp;
          } catch { /* ignore invalid saved pin origin */ }
        }
      }
    }
    try { hub = parseHubAddress(process.env.PERSONA_HUB_URL ?? saved?.hub ?? ""); } catch { hub = undefined; }
    if (saved?.island === true) island.setEnabled(true);
    for (const scheme of ["persona", "jarvis"]) {
      if (!app.isDefaultProtocolClient(scheme)) app.setAsDefaultProtocolClient(scheme);
    }
    if (hub) await showMain().catch(() => {});
    if (quitting) return;
    if (!mainWindow) await showSetup();
    tray = installTray({ open: strings.tray_open ?? "Open Persona", quit: strings.tray_quit ?? "Quit Persona" }, () => {
      if (mainWindow && !mainWindow.isDestroyed()) { mainWindow.show(); mainWindow.focus(); }
      else if (hub) void showMain().catch(() => { if (!quitting) void showSetup().catch(() => {}); });
      else void showSetup().catch(() => {});
    }, () => { quitting = true; app.quit(); }, action => {
      if (action === "restart") updates?.restart();
      else updates?.check();
    });
    tray.setUpdate(updateItems);
  }).catch(error => {
    if (quitting) return;
    dialog.showErrorBox("Persona could not start", error instanceof Error ? error.message : String(error));
    app.quit();
  });
}
app.on("before-quit", () => { quitting = true; if (presenceTimer) clearInterval(presenceTimer); });
app.on("window-all-closed", () => {
  // Closing to the tray deliberately keeps the app alive; an explicit Quit exits on every platform.
});

// Developing Persona (#9675): the shell's own record while the page's switch is on (dev-diagnostics.ts).
import { installDevDiagnostics } from "./dev-diagnostics.js";
import type { DesktopSurfaceStyle, DesktopThemeSource, DesktopWindowChrome } from "../../spec/desktop-bridge.js";
installDevDiagnostics();
