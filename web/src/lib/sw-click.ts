// What a click on a notification the service worker showed does (sw.ts notificationclick; lib/notify.ts
// postNotification has the worker show one where the page can't make its own). Kept apart from sw.ts so it is tested.

/** The few parts of the worker's Clients and WindowClient this uses. */
export interface ClickWindow {
  focused?: boolean;
  visibilityState?: string;
  url?: string;
  focus(): Promise<unknown>;
  navigate(url: string): Promise<unknown | null>;
  postMessage?(message: unknown, transfer: Transferable[]): void;
}
export interface ClickClients {
  matchAll(o: { type: "window"; includeUncontrolled: boolean }): Promise<ClickWindow[]>;
  openWindow(url: string): Promise<unknown | null>;
}

const rank = (w: ClickWindow) => (w.focused ? 2 : w.visibilityState === "visible" ? 1 : 0);
const frontmost = (wins: ClickWindow[]) => [...wins].sort((a, b) => rank(b) - rank(a))[0];

/**
 * Brings a window of the app forward on `url` (the agent's conversation). Only a window this worker controls can be
 * navigated, so those are the ones looked at, a focused or visible one first. When there is none, or it can't be
 * navigated, a new window opens on `url`. Without a url, an open window comes forward.
 */
export async function openFromNotification(clients: ClickClients, url: string | undefined): Promise<void> {
  const win = frontmost(await clients.matchAll({ type: "window", includeUncontrolled: false }));
  if (!url) {
    if (win) await win.focus().catch(() => null);
    else await clients.openWindow("/").catch(() => null);
    return;
  }
  if (win) {
    await win.focus().catch(() => null);
    const went = await win.navigate(url).catch(() => null);
    if (went) return;
  }
  await clients.openWindow(url).catch(() => null);
}

/** What the worker tells a page of the app about a click (lib/notification-answer.ts takes it; it has its own copy of
 *  the type, since a module the page and the worker both import becomes a chunk the classic worker can't load). */
export const NOTE_MESSAGE = "persona.notification";
/** How long the worker waits for the page to say whether it took the click: a browser lets a worker open or focus a
 *  window only for a moment after it (Firefox about a second), and the fallback needs that. */
export const ANSWER_WAIT_MS = 800;
export interface NoteMessage {
  type: typeof NOTE_MESSAGE;
  action: "allow" | "deny" | "open";
  /** The permission card the notification is about. */
  ix: string;
  /** The exact occurrence the notification drew; absent only on a legacy notification. */
  created_at?: string;
  url?: string;
  /** A page that gets the message after this (ms since the epoch) leaves it to the worker, which may have stopped
   *  waiting: half the wait, so its answer is back in time. */
  until: number;
}

/** Posts `m` to the page; true when it says on the port within `ms` that it took it. */
function handOver(win: ClickWindow, m: NoteMessage, ms: number): Promise<boolean> {
  const post = win.postMessage?.bind(win);
  if (!post) return Promise.resolve(false);
  return new Promise((done) => {
    const ch = new MessageChannel();
    const end = (took: boolean) => {
      clearTimeout(timer);
      ch.port1.close();
      done(took);
    };
    const timer = setTimeout(() => end(false), ms);
    ch.port1.onmessage = (e) => end(e.data === true);
    try {
      post(m, [ch.port2]);
    } catch {
      end(false);
    }
  });
}

/** At most this many windows are asked in turn, so each still has a useful part of the wait. */
export const ASK_AT_MOST = 3;

/** Windows of the app that can take a click, front first: not the admin page (in the worker's scope, it doesn't listen). */
export function answeringWindows(wins: ClickWindow[]): ClickWindow[] {
  const app = wins.filter((w) => {
    if (!w.postMessage) return false;
    let p: string;
    try {
      p = new URL(w.url ?? "/", "http://x").pathname;
    } catch {
      return false;
    }
    // the admin UI is at the root only (docs/web/web.md"Subpaths"), and the worker is registered only there: an app page
    // with "admin" further down its path (a session or machine called admin) is still asked
    return !/^\/admin(\/|$)/.test(p);
  });
  return [...app].sort((a, b) => rank(b) - rank(a)).slice(0, ASK_AT_MOST);
}

/**
 * A click on a notification the worker showed. One about a permission card (`data.ix`, with the Allow / Deny buttons):
 * a button goes to an open window of the app, which answers with its own token (the worker has none) and is neither
 * opened nor brought forward; a plain click brings that window forward and the page opens the conversation itself.
 * The windows are asked one at a time, front first, each for its share of the wait, so a frozen tab in front doesn't
 * keep another from answering, and only one ever answers. When none takes it in time (none open, an older page, all
 * frozen), the conversation opens as for any other notification.
 */
export async function clickNotification(clients: ClickClients, data: { url?: string; ix?: string; created_at?: string } | null | undefined, action: string, wait = ANSWER_WAIT_MS): Promise<void> {
  const url = data?.url;
  if (!data?.ix) return openFromNotification(clients, url);
  const answer = action === "allow" || action === "deny" ? action : undefined;
  const wins = answeringWindows(await clients.matchAll({ type: "window", includeUncontrolled: false }));
  const share = wins.length ? wait / wins.length : 0;
  for (const win of wins) {
    if (await handOver(win, { type: NOTE_MESSAGE, action: answer ?? "open", ix: data.ix, created_at: data.created_at, url, until: Date.now() + share / 2 }, share)) {
      if (!answer) await win.focus().catch(() => null);
      return;
    }
  }
  await openFromNotification(clients, url);
}
