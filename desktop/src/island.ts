import { join } from "node:path";
import { BrowserWindow, ipcMain, net, protocol, screen } from "electron";
import { notificationRoute } from "./contract.cjs";
import { ISLAND_SCHEME, ISLAND_URL, islandFile } from "./island-page.js";
import { EMPTY_ISLAND, ISLAND_WINDOW, islandDisplay, islandRect, islandSides, islandState, islandView } from "./island-model.js";
import type { IslandState, IslandStatus } from "./island-model.js";

/** How long the pointer may be away before the open list closes: crossing the pill's edge does not close it. */
const CLOSE_AFTER_MS = 250;

/** Serves the island's page from its own scheme (island-page.ts); main.ts registers the scheme before the app is
 *  ready and calls this once it is. */
export function installIslandProtocol(): void {
  protocol.handle(ISLAND_SCHEME, request => {
    const file = islandFile(request.url, request.method);
    return file ? net.fetch(file) : new Response("Not found", { status: 404 });
  });
}

/**
 * The island at the MacBook notch (#7669): a frameless, always-on-top window that takes no focus, over the menu bar
 * around the notch, its top corners square against the top of the screen (`ISLAND_WINDOW`). It draws the latest state
 * the web sent (`setState`) from its local page (island-page.ts), opens downward into a list while the pointer is over
 * it, and hands a clicked row's route to `openRoute`, the notification-click path. macOS only; off until the person
 * turns it on in Settings, and hidden while there is nothing to show.
 */
export class NotchIsland {
  private window: BrowserWindow | undefined;
  private state: IslandState = EMPTY_ISLAND;
  /** The webContents id of the page that sent `state`. */
  private source: number | undefined;
  private open = false;
  private closing: ReturnType<typeof setTimeout> | undefined;
  private readonly placeNow = () => this.render();

  constructor(private readonly openRoute: (route: string) => void) {}

  get supported(): boolean { return process.platform === "darwin"; }

  status(): IslandStatus { return { supported: this.supported, enabled: this.window !== undefined }; }

  setEnabled(on: boolean): IslandStatus {
    if (on && this.supported) this.create();
    else this.destroy();
    return this.status();
  }

  /** The web's latest state, from page `source`; kept while the island is off, so turning it on shows the current
   *  counts at once. */
  setState(value: unknown, source?: number): void {
    const next = islandState(value);
    if (!next) return;
    this.state = next;
    this.source = source;
    this.render();
  }

  /** Forgets page `source`'s state once that page is gone (its window closed for another hub, its renderer crashed),
   *  so nothing of that hub stays at the notch; hidden until a signed-in page sends its own. A state another page sent
   *  since stays: a retired window can be destroyed after its replacement has signed in. */
  forget(source: number): void {
    if (this.source !== source) return;
    if (this.closing) clearTimeout(this.closing);
    this.closing = undefined;
    this.state = EMPTY_ISLAND;
    this.source = undefined;
    this.open = false;
    this.render();
  }

  private create(): void {
    if (this.window && !this.window.isDestroyed()) return;
    const w = new BrowserWindow({
      ...ISLAND_WINDOW,
      ...this.rect(),
      webPreferences: {
        preload: join(import.meta.dirname, "island-preload.cjs"),
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
        webSecurity: true,
        devTools: false,
        webviewTag: false,
        spellcheck: false,
      },
    });
    w.setAlwaysOnTop(true, "screen-saver");
    // skipTransformProcessType: Electron's default makes the whole app a background one (no Dock icon or badge, no menu
    // bar) for as long as it runs. The panel type is what lets this window over full-screen apps.
    w.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true, skipTransformProcessType: true });
    w.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
    w.webContents.on("will-navigate", event => event.preventDefault());
    w.webContents.on("did-finish-load", this.placeNow);
    w.once("closed", () => { if (this.window === w) this.window = undefined; });
    this.window = w;
    this.listen(w);
    screen.on("display-metrics-changed", this.placeNow);
    screen.on("display-added", this.placeNow);
    screen.on("display-removed", this.placeNow);
    void w.loadURL(ISLAND_URL);
  }

  private destroy(): void {
    screen.removeListener("display-metrics-changed", this.placeNow);
    screen.removeListener("display-added", this.placeNow);
    screen.removeListener("display-removed", this.placeNow);
    for (const name of ["island:hover", "island:open"]) ipcMain.removeAllListeners(name);
    if (this.closing) clearTimeout(this.closing);
    this.closing = undefined;
    this.open = false;
    const w = this.window;
    this.window = undefined;
    if (w && !w.isDestroyed()) w.destroy();
  }

  /** The page's two operations: the pointer over it or away, and a row clicked. Only this window's page is heard. */
  private listen(w: BrowserWindow): void {
    for (const name of ["island:hover", "island:open"]) ipcMain.removeAllListeners(name);
    const own = (event: Electron.IpcMainEvent) => !w.isDestroyed() && event.sender === w.webContents && event.senderFrame === event.sender.mainFrame;
    ipcMain.on("island:hover", (event, inside: unknown) => {
      if (!own(event)) return;
      if (this.closing) clearTimeout(this.closing);
      this.closing = undefined;
      if (inside === true) {
        if (!this.open && this.state.rows.length) { this.open = true; this.render(); }
        return;
      }
      this.closing = setTimeout(() => { this.closing = undefined; if (this.open) { this.open = false; this.render(); } }, CLOSE_AFTER_MS);
    });
    ipcMain.on("island:open", (event, value: unknown) => {
      if (!own(event) || typeof value !== "string") return;
      const route = notificationRoute(value);
      if (!route || !this.state.rows.some(r => r.route === route)) return;
      this.open = false;
      this.render();
      this.openRoute(route);
    });
  }

  private display() {
    return islandDisplay(screen.getAllDisplays(), screen.getPrimaryDisplay());
  }

  private rect() {
    return islandRect(this.display(), this.open, this.state.rows.length);
  }

  /** Places the window for the current state, sends the page what to draw, and hides it while there is nothing. */
  private render(): void {
    const w = this.window;
    if (!w || w.isDestroyed()) return;
    const [left] = islandSides(this.state);
    if (!left) {
      this.open = false;
      w.hide();
      return;
    }
    if (this.open && !this.state.rows.length) this.open = false;
    w.setBounds(this.rect());
    w.webContents.send("island:view", islandView(this.state, this.display(), this.open));
    if (!w.isVisible()) w.showInactive();
  }
}
