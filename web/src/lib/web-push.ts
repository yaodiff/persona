// Web Push for this browser (#7459, docs/web/web.md "Web Push"): Settings › Notifications › "Notify this browser when
// Persona is closed". The browser subscribes with the hub's VAPID key (PushManager), the hub keeps the subscription
// (PUT /api/v1/push/web/{device}) and pushes what a phone would get while no page of this browser is open; the
// service worker shows it (lib/sw-push.ts). Each load checks the subscription is still the one the hub has, and takes
// back what was pushed for an event that is over.
import { useSyncExternalStore } from "react";
import { currentLanguage } from "../i18n";
import { api, ApiError, CLIENT_FEATURES, enc } from "./api";
import { BASE } from "./base";
import { deviceId } from "./device-id";
import type { AgentState } from "./types";
import { WEB_PUSH_KEY, webPushFlag } from "./web-push-flag";

/** Why this browser can't, or "ok". */
export type WebPushSupport =
  | "ok"
  | "desktop-app" // the desktop app notifies by itself
  | "dev" // Vite's dev server registers no service worker
  | "subpath" // the worker's scope is the root's (lib/pwa.ts)
  | "insecure" // push needs https (or this computer's own address)
  | "home-screen" // iPhone and iPad: only the Home Screen app gets pushes
  | "unsupported"; // no service worker, PushManager or Notification, or no storage for the device id

export interface WebPushEnv {
  desktop: boolean;
  prod: boolean;
  base: string;
  secure: boolean;
  worker: boolean;
  push: boolean;
  notification: boolean;
  kept: boolean;
  ua: string;
  touchPoints: number;
  standalone: boolean;
}

function currentEnv(): WebPushEnv {
  const nav = typeof navigator === "undefined" ? undefined : navigator;
  return {
    desktop: typeof window !== "undefined" && !!window.personaDesktop,
    prod: import.meta.env.PROD,
    base: BASE,
    secure: typeof isSecureContext !== "undefined" && isSecureContext,
    worker: !!nav && "serviceWorker" in nav,
    push: typeof window !== "undefined" && "PushManager" in window,
    notification: typeof Notification !== "undefined",
    kept: deviceId().kept,
    ua: nav?.userAgent ?? "",
    touchPoints: nav?.maxTouchPoints ?? 0,
    standalone:
      (typeof matchMedia !== "undefined" && matchMedia("(display-mode: standalone)").matches) || (nav as { standalone?: boolean } | undefined)?.standalone === true,
  };
}

export function webPushSupport(env: WebPushEnv = currentEnv()): WebPushSupport {
  if (env.desktop) return "desktop-app";
  if (!env.prod) return "dev";
  if (env.base) return "subpath";
  if (!env.secure) return "insecure";
  // iPadOS asks for the desktop site: a "Macintosh" with a touch screen (lib/pwa.ts installState).
  const ios = /iPhone|iPad|iPod/.test(env.ua) || (/Macintosh/.test(env.ua) && env.touchPoints > 1);
  if (ios && !env.standalone) return "home-screen";
  if (!env.worker || !env.push || !env.notification || !env.kept) return "unsupported";
  return "ok";
}

/** This browser's subscription as the hub has it (GET /api/v1/push/web/{device}). */
export interface WebPushStatus {
  subscribed: boolean;
  /** FCM, Mozilla, Apple or Windows. */
  service?: string;
  host?: string;
  /** Made with the hub's key now; false: renewed on the next load. */
  current?: boolean;
  endpoint?: string;
  last_ok_at?: string;
  failed_at?: string;
  status?: number;
  error?: string;
}

/** The browser's name and system as a hub lists it (`hub doctor`): "Chrome · macOS". */
export function browserLabel(ua: string, touchPoints = 0): string {
  const browser = /Edg(?:e|A|iOS)?\//.test(ua)
    ? "Edge"
    : /OPR\/|Opera/.test(ua)
      ? "Opera"
      : /SamsungBrowser\//.test(ua)
        ? "Samsung Internet"
        : /Firefox\/|FxiOS\//.test(ua)
          ? "Firefox"
          : /Chrome\/|CriOS\//.test(ua)
            ? "Chrome"
            : /Safari\//.test(ua)
              ? "Safari"
              : "";
  const system = /iPhone|iPod/.test(ua)
    ? "iPhone"
    : /iPad/.test(ua) || (/Macintosh/.test(ua) && touchPoints > 1)
      ? "iPad"
      : /Android/.test(ua)
        ? "Android"
        : /CrOS/.test(ua)
          ? "ChromeOS"
          : /Macintosh|Mac OS X/.test(ua)
            ? "macOS"
            : /Windows/.test(ua)
              ? "Windows"
              : /Linux/.test(ua)
                ? "Linux"
                : "";
  return [browser, system].filter(Boolean).join(" · ");
}

/** base64url (the hub's key) as bytes, for applicationServerKey. */
export function keyBytes(key: string): Uint8Array<ArrayBuffer> {
  const b64 = key.replace(/-/g, "+").replace(/_/g, "/");
  const bin = atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4));
  const out = new Uint8Array(new ArrayBuffer(bin.length));
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** The subscription was made with `key`; true when the browser doesn't say which key it was made with. */
export function madeWith(sub: Pick<PushSubscription, "options">, key: string): boolean {
  const have = sub.options?.applicationServerKey;
  if (!have) return true;
  const a = new Uint8Array(have);
  const b = keyBytes(key);
  return a.length === b.length && a.every((x, i) => x === b[i]);
}

// ── the state Settings shows ─────────────────────────────

export interface WebPushState {
  /** The hub's answer came (or failed). */
  loaded: boolean;
  status?: WebPushStatus;
  /** The hub has no Web Push (an older hub). */
  hubMissing?: boolean;
  /** Turning on, off or sending a test. */
  working: boolean;
  /** Why the last attempt failed, in words. */
  error?: string;
}

let state: WebPushState = { loaded: false, working: false };
const listeners = new Set<() => void>();
function setState(next: Partial<WebPushState>) {
  state = { ...state, ...next };
  listeners.forEach((l) => l());
}

export function useWebPush(): WebPushState {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => state,
    () => state,
  );
}

function setFlag(on: boolean) {
  try {
    if (on) localStorage.setItem(WEB_PUSH_KEY, "1");
    else localStorage.removeItem(WEB_PUSH_KEY);
  } catch {
    // storage unavailable: support() is "unsupported" then
  }
}

const statusPath = () => `/push/web/${enc(deviceId().id)}`;

/** Reads this browser's subscription from the hub. */
export async function loadWebPush(): Promise<WebPushState> {
  try {
    const status = await api<WebPushStatus>(statusPath());
    setState({ loaded: true, status, hubMissing: false });
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) setState({ loaded: true, hubMissing: true, status: undefined });
    else setState({ loaded: true, error: e instanceof Error ? e.message : String(e) });
  }
  return state;
}

/** The registration of the app's service worker, or an error once it doesn't come within `ms`. */
async function worker(ms = 10_000): Promise<ServiceWorkerRegistration> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      navigator.serviceWorker.ready,
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new WebPushError("no_worker")), ms);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

/** A failure with a reason Settings words: the browser refused notifications, or no service worker runs. */
export class WebPushError extends Error {
  constructor(readonly reason: "denied" | "no_worker") {
    super(reason);
  }
}

async function put(sub: PushSubscription, key: string): Promise<WebPushStatus> {
  const json = sub.toJSON();
  return api<WebPushStatus>(statusPath(), {
    method: "PUT",
    json: { endpoint: json.endpoint, keys: json.keys, key, locale: currentLanguage(), features: CLIENT_FEATURES, browser: browserLabel(navigator.userAgent, navigator.maxTouchPoints) },
  });
}

/**
 * Subscribes this browser with the hub's key and hands the subscription to the hub: a subscription made with another
 * key is replaced, and so is one the hub says it no longer has the key of.
 */
async function ensureSubscribed(): Promise<WebPushStatus> {
  const reg = await worker();
  const { key } = await api<{ key: string }>("/push/web");
  let sub = await reg.pushManager.getSubscription();
  if (sub && !madeWith(sub, key)) {
    await sub.unsubscribe().catch(() => false);
    sub = null;
  }
  sub ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(key) });
  try {
    return await put(sub, key);
  } catch (e) {
    const fresh = e instanceof ApiError && e.code === "key_changed" && typeof e.body.key === "string" ? e.body.key : undefined;
    if (!fresh) throw e;
    await sub.unsubscribe().catch(() => false);
    const again = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(fresh) });
    return put(again, fresh);
  }
}

/** Turns Web Push on for this browser: asks for notifications first when it has not been asked. */
export async function turnOnWebPush(): Promise<WebPushStatus> {
  setState({ working: true, error: undefined });
  try {
    let p = Notification.permission;
    if (p === "default") p = await Notification.requestPermission();
    if (p !== "granted") throw new WebPushError("denied");
    const status = await ensureSubscribed();
    setFlag(true);
    setState({ status, loaded: true });
    return status;
  } catch (e) {
    setState({ error: e instanceof Error ? e.message : String(e) });
    throw e;
  } finally {
    setState({ working: false });
  }
}

/** Turns it off: the hub forgets the subscription, and the browser drops it. */
export async function turnOffWebPush(): Promise<void> {
  setState({ working: true, error: undefined });
  try {
    const reg = await navigator.serviceWorker?.getRegistration();
    const sub = await reg?.pushManager.getSubscription();
    const query = sub ? `?endpoint=${enc(sub.endpoint)}` : "";
    await api(`${statusPath()}${query}`, { method: "DELETE" });
    await sub?.unsubscribe().catch(() => false);
    setFlag(false);
    setState({ status: { subscribed: false } });
  } catch (e) {
    setState({ error: e instanceof Error ? e.message : String(e) });
    throw e;
  } finally {
    setState({ working: false });
  }
}

/** Has the hub push a test now; the notification shows even with this page open. Says what the push service answered. */
export async function sendTestPush(): Promise<void> {
  setState({ working: true });
  try {
    await api(`${statusPath()}/test`, { method: "POST" });
  } finally {
    setState({ working: false });
    void loadWebPush();
  }
}

let synced: string | undefined;

/**
 * Once a load (per credential): when this browser turned Web Push on and may still show notifications, makes sure its
 * subscription is the one the hub has, made with the hub's key, in this page's language. A failure is kept for
 * Settings to show; nothing is asked of the user.
 */
export async function syncWebPush(owner = deviceId().id): Promise<void> {
  if (synced === owner || !webPushFlag() || webPushSupport() !== "ok" || Notification.permission !== "granted") return;
  synced = owner;
  try {
    setState({ status: await ensureSubscribed(), loaded: true, error: undefined });
  } catch (e) {
    setState({ error: e instanceof Error ? e.message : String(e) });
  }
}

// ── taking back what was pushed ──────────────────────────

/**
 * A pushed notification whose event is over: a "Needs you" one of a session that no longer waits on the user (or a card
 * no longer pending), a finished one of a session that is seen. The rest (a to-do, an alert, the test) stay until
 * dismissed.
 */
export function pushedOver(tag: string, states: ReadonlyMap<string, AgentState>, pendingCards: ReadonlySet<string>): boolean {
  if (tag.startsWith("card:") && tag.endsWith("#needs")) return !pendingCards.has(tag.slice("card:".length, -"#needs".length));
  if (tag.endsWith("#needs")) return states.get(tag.slice(0, -"#needs".length)) !== "needs_you";
  if (tag.endsWith("#finished")) return states.get(tag.slice(0, -"#finished".length)) !== "unread";
  return false;
}

let settling = false;
let pendingSettle: [ReadonlyMap<string, AgentState>, ReadonlySet<string>] | undefined;

/**
 * Closes the notifications the worker showed for a push whose event is over (pushedOver): what came while every page
 * was closed, answered or seen since, here or elsewhere. One pass at a time; a call meanwhile runs after it, with its
 * own states. Returns how many it closed.
 */
export async function settlePushed(states: ReadonlyMap<string, AgentState>, pendingCards: ReadonlySet<string>): Promise<number> {
  if (settling) {
    pendingSettle = [states, pendingCards];
    return 0;
  }
  settling = true;
  let closed = 0;
  try {
    const reg = await navigator.serviceWorker?.getRegistration();
    for (const n of (await reg?.getNotifications()) ?? []) {
      const data = n.data as { pushed?: boolean } | null;
      if (data?.pushed && n.tag && pushedOver(n.tag, states, pendingCards)) {
        n.close();
        closed++;
      }
    }
  } catch {
    // no worker, or the browser keeps its notifications to itself: nothing to take back
  } finally {
    settling = false;
  }
  const next = pendingSettle;
  pendingSettle = undefined;
  if (next) closed += await settlePushed(...next);
  return closed;
}
