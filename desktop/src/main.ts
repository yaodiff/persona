import { join } from "node:path";
import { BrowserWindow, app, dialog, screen, shell, powerMonitor, Notification } from "electron";
import { parseDeepLink, parseHubAddress, externalBrowserURL, inHub } from "./urls.js";
import type { HubAddress } from "./urls.js";
import { appPairingURL } from "./pairing.js";
import { installDesktopIPC, DesktopNotifications } from "./ipc.js";
import { installSetupIPC } from "./setup-ipc.js";
import { JSONStore } from "./store.js";
import { restoreWindow, DEFAULT_WINDOW } from "./window-state.js";
import { closeToTray, installTray } from "./tray.js";
import type { DesktopTray } from "./tray.js";
import { desktopLocale, loadStrings } from "./strings.js";
import { updateMenu } from "./update-state.js";
import type { UpdateMenuItem } from "./update-state.js";
import { hubPartition } from "./sessions.js";
import { installSmokeCapture } from "./smoke.js";
import { installAutoUpdate } from "./updater.js";
import { desktopPresence } from "./presence.js";

installSmokeCapture();

let mainWindow: BrowserWindow | undefined;
let hub: HubAddress | undefined;
let quitting = false;
let pendingDeepLink: string | undefined;
let setupWindow: BrowserWindow | undefined;
let screenLocked = false;
let presenceTimer: ReturnType<typeof setInterval> | undefined;
const store = () => JSONStore.inUserData(app.getPath("userData"));

function navigateFromDeepLink(raw: string): void {
  const link = parseDeepLink(raw);
  if (!link) return;
  if (!hub) { pendingDeepLink = raw; void showSetup(); return; }
  if (link.kind === "open" && link.route) mainWindow?.webContents.send("desktop:notification-open", link.route);
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

function secureHubWindow(window: BrowserWindow): void {
  const contents = window.webContents;
  contents.setWindowOpenHandler(({ url }) => {
    const external = externalBrowserURL(url);
    if (external && (!hub || !inHub(hub, url))) void shell.openExternal(external);
    else if (hub && inHub(hub, url)) void window.loadURL(url);
    return { action: "deny" };
  });
  contents.on("will-navigate", (event, url) => {
    if (hub && inHub(hub, url)) return;
    event.preventDefault();
    const external = externalBrowserURL(url);
    if (external) void shell.openExternal(external);
  });
  contents.on("will-redirect", (event, url) => {
    if (hub && inHub(hub, url)) return;
    event.preventDefault();
    const external = externalBrowserURL(url);
    if (external) void shell.openExternal(external);
  });
  contents.on("will-attach-webview", event => event.preventDefault());
  contents.session.setPermissionRequestHandler((_contents, permission, callback) => {
    // The web notification preference still resolves; the OS notification itself comes from Electron.
    callback(permission === "notifications");
  });
  contents.session.setPermissionCheckHandler((_contents, permission) => permission === "notifications");
}

// A different hub needs a new window and its own persistent session, not navigation of the old window.
async function replaceMainWindow(next: HubAddress, close: (window: BrowserWindow) => void): Promise<void> {
  const previous = mainWindow;
  if (!previous || previous.isDestroyed()) return;
  // Sub-path containment is not equality: /old/ is inside a new / hub, but still needs its own origin-isolated session.
  if (inHub(next, previous.webContents.getURL()) && (() => { try { return parseHubAddress(previous.webContents.getURL()).url.href === next.url.href; } catch { return false; } })()) return;
  // Setting hub first lets closeToTray destroy this window without putting the old hub back on screen.
  hub = next;
  const closed = new Promise<void>(done => previous.once("closed", done));
  close(previous);
  await closed;
  mainWindow = undefined;
}

async function showMain(): Promise<void> {
  if (mainWindow && !mainWindow.isDestroyed()) { mainWindow.show(); return; }
  const state = restoreWindow((await store().read<{ window?: unknown }>())?.window, screen.getAllDisplays().map(d => d.workArea));
  const current = new BrowserWindow({
    ...DEFAULT_WINDOW,
    ...state,
    show: false,
    title: "Persona",
    autoHideMenuBar: true,
    backgroundColor: "#111111",
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
      ...(hub ? { partition: hubPartition(hub) } : {}),
    },
  });
  mainWindow = current;
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
  secureHubWindow(current);
  closeToTray(current, () => quitting);
  const removeIPC = installDesktopIPC(current, {
    // The partition is immutable, so derive authorization from this window's actual origin, never a later global hub.
    hub: () => {
      const url = current.webContents.getURL();
      if (hub && inHub(hub, url)) return hub;
      try { return parseHubAddress(url); } catch { return undefined; }
    },
    presence: () => desktopPresence(powerMonitor.getSystemIdleTime(), screenLocked, Notification.isSupported()),
    openLocalSettings: () => { void showSetup(); },
    notifications: new DesktopNotifications(route => {
      current.show();
      current.focus();
      current.webContents.send("desktop:notification-open", route);
    }),
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
    if (state.maximized) current.maximize();
    current.show();
    if (pendingDeepLink) navigateFromDeepLink(pendingDeepLink);
    pendingDeepLink = undefined;
  });
  current.once("closed", () => { removeIPC(); if (mainWindow === current) mainWindow = undefined; });
  if (hub) await current.loadURL(hub.url.href);
}

async function showSetup(): Promise<void> {
  if (setupWindow && !setupWindow.isDestroyed()) { setupWindow.show(); setupWindow.focus(); return; }
  const setup = new BrowserWindow({
    width: 720,
    height: 640,
    title: "Persona setup",
    autoHideMenuBar: true,
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
  setup.setMenu(null);
  setup.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
  setup.webContents.on("will-navigate", event => event.preventDefault());
  setup.once("closed", () => { removeIPC(); if (setupWindow === setup) setupWindow = undefined; if (!mainWindow) void showMain(); });
  const removeIPC = installSetupIPC(setup, {
    hub: () => hub,
    connect: async (raw: string) => {
      try {
        const next = parseHubAddress(raw);
        await store().update({ hub: next.url.href });
        await replaceMainWindow(next, window => window.destroy());
        hub = next;
        setup.close();
        await showMain();
        return { ok: true };
      } catch { return { ok: false, error: "Enter a valid HTTP or HTTPS hub address." }; }
    },
  });
  await setup.loadFile(join(import.meta.dirname, "setup.html"));
}

const locked = app.requestSingleInstanceLock();
if (!locked) app.quit();
else {
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
    // The first check starts here, before any window loads; its state is kept until the tray exists to show it.
    let tray: DesktopTray | undefined;
    let updateItems: readonly UpdateMenuItem[] = [];
    const updates = installAutoUpdate({
      changed: state => { updateItems = updateMenu(state, strings); tray?.setUpdate(updateItems); },
      prepareQuit: () => { quitting = true; },
    });
    const saved = await store().read<{ hub?: string }>();
    try { hub = parseHubAddress(process.env.PERSONA_HUB_URL ?? saved?.hub ?? ""); } catch { hub = undefined; }
    for (const scheme of ["persona", "jarvis"]) {
      if (!app.isDefaultProtocolClient(scheme)) app.setAsDefaultProtocolClient(scheme);
    }
    if (hub) await showMain();
    else await showSetup();
    tray = installTray({ open: strings.tray_open ?? "Open Persona", quit: strings.tray_quit ?? "Quit Persona" }, () => {
      if (mainWindow && !mainWindow.isDestroyed()) { mainWindow.show(); mainWindow.focus(); }
      else void showMain();
    }, () => { quitting = true; app.quit(); }, action => {
      if (action === "restart") updates?.restart();
      else updates?.check();
    });
    tray.setUpdate(updateItems);
  }).catch(error => {
    dialog.showErrorBox("Persona could not start", error instanceof Error ? error.message : String(error));
    app.quit();
  });
}
app.on("before-quit", () => { quitting = true; if (presenceTimer) clearInterval(presenceTimer); });
app.on("window-all-closed", () => {
  // Closing to the tray deliberately keeps the app alive; an explicit Quit exits on every platform.
});
