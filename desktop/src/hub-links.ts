import { externalBrowserURL, inHub } from "./urls.js";
import type { HubAddress } from "./urls.js";

/** Hub Admin: the hub's own `/admin` pages, with their own sign-in (docs/operations/admin.md). Letter case does not
 *  matter, as for the hub's `adminPath`. */
export function isHubAdmin(hub: HubAddress, candidate: string): boolean {
  let url: URL;
  try { url = new URL(candidate); } catch { return false; }
  if (!inHub(hub, url)) return false;
  const path = url.pathname.slice(hub.url.pathname.length).replace(/^\/+/, "").toLowerCase();
  return path === "admin" || path.startsWith("admin/");
}

/** Where a hub window sends a link of its page (#16992). */
export interface HubLinkTargets {
  /** Hub Admin's own window. Absent in that window itself, whose admin links stay in it. */
  readonly admin?: ((url: string) => void) | undefined;
  /** The system browser, for any other HTTP(S) address. */
  readonly browser: (url: string) => void;
}

/** A link the page asks to open in a new window (`target="_blank"`, `window.open`). A page of the hub gets no new
 *  Electron window of its own: it loads in this window (`here`), except Hub Admin, which goes to its own window so the
 *  Assistant window keeps its page. Another HTTP(S) address goes to the system browser, anything else nowhere. */
export function openNewWindowLink(hub: HubAddress | undefined, url: string, to: HubLinkTargets & { readonly here: (url: string) => void }): void {
  if (hub && inHub(hub, url)) {
    if (to.admin && isHubAdmin(hub, url)) to.admin(url);
    else to.here(url);
    return;
  }
  const external = externalBrowserURL(url);
  if (external) to.browser(external);
}

/** A navigation of the window's own page (`will-navigate`, `will-redirect`): true when it goes on. A page of the hub
 *  stays in the window, except Hub Admin when the window has an admin window to send it to. Anything else is refused,
 *  an HTTP(S) address handed to the system browser. */
export function keepNavigation(hub: HubAddress | undefined, url: string, to: HubLinkTargets): boolean {
  if (hub && inHub(hub, url)) {
    if (!to.admin || !isHubAdmin(hub, url)) return true;
    to.admin(url);
    return false;
  }
  const external = externalBrowserURL(url);
  if (external) to.browser(external);
  return false;
}

/** What the admin window needs of an Electron `BrowserWindow`. */
export interface AdminWindowLike {
  isDestroyed(): boolean;
  isVisible(): boolean;
  show(): void;
  focus(): void;
  destroy(): void;
  loadURL(url: string): Promise<void>;
  once(event: "closed", listener: () => void): unknown;
  readonly webContents: { getURL(): string };
}

/** A navigation that a later one replaced: no failure of the window. */
const aborted = (error: unknown): boolean => (error as { code?: unknown } | undefined)?.code === "ERR_ABORTED";

/** Hub Admin's one window beside an Assistant window (#16992). An admin link opens it, or shows the one already open
 *  (on the link's page when it is another one); the Assistant window is never navigated. `close` goes with the
 *  Assistant window, whose hub and session the admin window shares. */
export class AdminWindow<W extends AdminWindowLike> {
  #window: W | undefined;

  constructor(private readonly create: () => W) {}

  get window(): W | undefined {
    return this.#window && !this.#window.isDestroyed() ? this.#window : undefined;
  }

  open(url: string): W {
    const open = this.window;
    if (open) {
      if (open.webContents.getURL() !== url) open.loadURL(url).catch(() => {});
      open.show();
      open.focus();
      return open;
    }
    const created = this.create();
    this.#window = created;
    created.once("closed", () => { if (this.#window === created) this.#window = undefined; });
    // A first load that fails leaves no hidden window behind: the next admin link opens a fresh one.
    created.loadURL(url).catch(error => {
      if (!aborted(error) && !created.isDestroyed() && !created.isVisible()) created.destroy();
    });
    return created;
  }

  close(): void {
    const open = this.window;
    this.#window = undefined;
    open?.destroy();
  }
}
