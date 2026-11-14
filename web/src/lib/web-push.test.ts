// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";

const api = vi.fn();
vi.mock("./api", async (orig) => ({ ...(await orig<typeof import("./api")>()), api: (...a: unknown[]) => api(...a) }));

const {
  browserLabel,
  keyBytes,
  loadWebPush,
  madeWith,
  pushedOver,
  sendTestPush,
  settlePushed,
  syncWebPush,
  turnOffWebPush,
  turnOnWebPush,
  WebPushError,
  webPushSupport,
} = await import("./web-push");
const { deviceId } = await import("./device-id");

const KEY = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8";
const OTHER = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4";

const env = {
  desktop: false,
  prod: true,
  base: "",
  secure: true,
  worker: true,
  push: true,
  notification: true,
  kept: true,
  ua: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
  touchPoints: 0,
  standalone: false,
};

describe("webPushSupport (#7459)", () => {
  it("says why a browser can't", () => {
    expect(webPushSupport(env)).toBe("ok");
    expect(webPushSupport({ ...env, desktop: true })).toBe("desktop-app");
    expect(webPushSupport({ ...env, prod: false })).toBe("dev");
    expect(webPushSupport({ ...env, base: "/persona" })).toBe("subpath");
    expect(webPushSupport({ ...env, secure: false })).toBe("insecure");
    expect(webPushSupport({ ...env, push: false })).toBe("unsupported");
    expect(webPushSupport({ ...env, kept: false })).toBe("unsupported");
    const iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1";
    expect(webPushSupport({ ...env, ua: iphone })).toBe("home-screen");
    expect(webPushSupport({ ...env, ua: iphone, standalone: true })).toBe("ok");
    // iPadOS asks for the desktop site
    expect(webPushSupport({ ...env, ua: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15", touchPoints: 5 })).toBe("home-screen");
  });

  it("names the browser and its system for the hub's list", () => {
    expect(browserLabel(env.ua)).toBe("Chrome · macOS");
    expect(browserLabel("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36 Edg/142.0.0.0")).toBe("Edge · Windows");
    expect(browserLabel("Mozilla/5.0 (X11; Linux x86_64; rv:145.0) Gecko/20100101 Firefox/145.0")).toBe("Firefox · Linux");
    expect(browserLabel("Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Mobile Safari/537.36")).toBe("Chrome · Android");
    expect(browserLabel("Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1")).toBe("Safari · iPhone");
    expect(browserLabel("")).toBe("");
  });

  it("reads the hub's key as bytes and tells a subscription made with another", () => {
    expect(keyBytes(KEY).length).toBe(65);
    expect(keyBytes(KEY)[0]).toBe(4);
    expect(madeWith({ options: { applicationServerKey: keyBytes(KEY).buffer, userVisibleOnly: true } }, KEY)).toBe(true);
    expect(madeWith({ options: { applicationServerKey: keyBytes(OTHER).buffer, userVisibleOnly: true } }, KEY)).toBe(false);
    expect(madeWith({ options: { applicationServerKey: null, userVisibleOnly: true } }, KEY)).toBe(true);
  });
});

describe("pushedOver", () => {
  const states = new Map([
    ["m/waiting", "needs_you"],
    ["m/seen", "idle"],
    ["m/unseen", "unread"],
  ] as const);
  it("is a pushed notification whose event is over, as the page's own are taken back", () => {
    const pending = new Set(["ix-open"]);
    expect(pushedOver("m/waiting#needs", states, pending)).toBe(false);
    expect(pushedOver("m/seen#needs", states, pending)).toBe(true);
    expect(pushedOver("m/gone#needs", states, pending)).toBe(true);
    expect(pushedOver("m/unseen#finished", states, pending)).toBe(false);
    expect(pushedOver("m/seen#finished", states, pending)).toBe(true);
    expect(pushedOver("card:ix-open#needs", states, pending)).toBe(false);
    expect(pushedOver("card:ix-done#needs", states, pending)).toBe(true);
    for (const tag of ["todo:1", "alert:a", "webpush-test"]) expect(pushedOver(tag, states, pending)).toBe(false);
  });
});

// ── the browser's side, faked ────────────────────────────

class FakeSubscription {
  unsubscribed = false;
  constructor(
    readonly endpoint: string,
    readonly key: string,
  ) {}
  get options() {
    return { applicationServerKey: keyBytes(this.key).buffer, userVisibleOnly: true };
  }
  toJSON() {
    return { endpoint: this.endpoint, keys: { p256dh: "pk", auth: "au" } };
  }
  unsubscribe() {
    this.unsubscribed = true;
    return Promise.resolve(true);
  }
}

let current: FakeSubscription | null;
let subscribed: string[];
let shown: { tag: string; data: unknown; close: () => void; closed?: boolean }[];
const pushManager = {
  getSubscription: () => Promise.resolve(current),
  subscribe: (o: { applicationServerKey: Uint8Array }) => {
    const key = btoa(String.fromCharCode(...o.applicationServerKey)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    subscribed.push(key);
    current = new FakeSubscription(`https://fcm.googleapis.com/fcm/send/${subscribed.length}`, key);
    return Promise.resolve(current);
  },
};
const registration = { pushManager, getNotifications: () => Promise.resolve(shown) };

function notification(tag: string, pushed = true) {
  const n = { tag, data: pushed ? { pushed: true, url: "/" } : { url: "/" }, closed: false, close: () => (n.closed = true) };
  return n;
}

beforeEach(() => {
  current = null;
  subscribed = [];
  shown = [];
  api.mockReset();
  localStorage.clear();
  Object.defineProperty(navigator, "serviceWorker", { configurable: true, value: { ready: Promise.resolve(registration), getRegistration: () => Promise.resolve(registration) } });
  vi.stubGlobal("Notification", { permission: "granted", requestPermission: vi.fn(() => Promise.resolve("granted")) });
});
afterEach(() => vi.unstubAllGlobals());

const device = () => deviceId().id;

describe("turning Web Push on and off", () => {
  it("subscribes with the hub's key and hands the hub the subscription, its language and what it can show", async () => {
    api.mockImplementation((path: string, init?: { method?: string; json?: unknown }) => {
      if (path === "/push/web") return Promise.resolve({ key: KEY });
      if (init?.method === "PUT") return Promise.resolve({ subscribed: true, service: "FCM", current: true });
      return Promise.reject(new Error(`unexpected ${path}`));
    });
    const status = await turnOnWebPush();
    expect(status).toMatchObject({ subscribed: true, service: "FCM" });
    expect(subscribed).toEqual([KEY]);
    const put = api.mock.calls.find((c) => c[1]?.method === "PUT")!;
    expect(put[0]).toBe(`/push/web/${encodeURIComponent(device())}`);
    expect(put[1].json).toMatchObject({ endpoint: "https://fcm.googleapis.com/fcm/send/1", keys: { p256dh: "pk", auth: "au" }, key: KEY, locale: "zh-Hans" });
    expect(put[1].json.features).toContain("elicitation");
    expect(localStorage.getItem("persona.webPush")).toBe("1");
  });

  it("asks for notifications first, and stops when the browser says no", async () => {
    vi.stubGlobal("Notification", { permission: "default", requestPermission: vi.fn(() => Promise.resolve("denied")) });
    await expect(turnOnWebPush()).rejects.toBeInstanceOf(WebPushError);
    expect(api).not.toHaveBeenCalled();
    expect(localStorage.getItem("persona.webPush")).toBeNull();
  });

  it("replaces a subscription made with another key, and subscribes again when the hub's key changed", async () => {
    current = new FakeSubscription("https://fcm.googleapis.com/fcm/send/old", OTHER);
    const old = current;
    let puts = 0;
    api.mockImplementation((path: string, init?: { method?: string; json?: { key: string } }) => {
      if (path === "/push/web") return Promise.resolve({ key: KEY });
      if (init?.method === "PUT") {
        puts++;
        if (puts === 1) return Promise.reject(new ApiError(409, "changed", { code: "key_changed", key: OTHER }));
        return Promise.resolve({ subscribed: true, current: true });
      }
      return Promise.reject(new Error(path));
    });
    await turnOnWebPush();
    expect(old.unsubscribed).toBe(true);
    expect(subscribed).toEqual([KEY, OTHER]);
    expect(api.mock.calls.filter((c) => c[1]?.method === "PUT").map((c) => c[1].json.key)).toEqual([KEY, OTHER]);
  });

  it("turns off: the hub forgets it, told the endpoint, and the browser drops it", async () => {
    current = new FakeSubscription("https://fcm.googleapis.com/fcm/send/x", KEY);
    const sub = current;
    localStorage.setItem("persona.webPush", "1");
    api.mockResolvedValue({ ok: true });
    await turnOffWebPush();
    expect(api).toHaveBeenCalledWith(`/push/web/${encodeURIComponent(device())}?endpoint=${encodeURIComponent(sub.endpoint)}`, { method: "DELETE" });
    expect(sub.unsubscribed).toBe(true);
    expect(localStorage.getItem("persona.webPush")).toBeNull();
  });

  it("says an older hub has none", async () => {
    api.mockRejectedValue(new ApiError(404, "not found", {}));
    expect(await loadWebPush()).toMatchObject({ loaded: true, hubMissing: true });
  });

  it("sends a test and reads the status again", async () => {
    api.mockResolvedValue({ ok: true, subscribed: true });
    await sendTestPush();
    expect(api).toHaveBeenCalledWith(`/push/web/${encodeURIComponent(device())}/test`, { method: "POST" });
  });
});

describe("syncWebPush", () => {
  it("renews the subscription once a load, only where it was turned on and notifications are allowed", async () => {
    api.mockImplementation((path: string, init?: { method?: string }) => Promise.resolve(path === "/push/web" ? { key: KEY } : init?.method === "PUT" ? { subscribed: true } : {}));
    // jsdom runs as a dev build: the support check says "dev"; the rest of the conditions are what this test covers
    vi.stubEnv("PROD", true);
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    vi.stubGlobal("PushManager", function PushManager() {});
    await syncWebPush("first");
    expect(api).not.toHaveBeenCalled(); // not turned on here
    localStorage.setItem("persona.webPush", "1");
    await syncWebPush("second");
    expect(api.mock.calls.filter((c) => c[1]?.method === "PUT")).toHaveLength(1);
    await syncWebPush("second");
    expect(api.mock.calls.filter((c) => c[1]?.method === "PUT")).toHaveLength(1);
    vi.stubGlobal("Notification", { permission: "denied", requestPermission: vi.fn() });
    await syncWebPush("third");
    expect(api.mock.calls.filter((c) => c[1]?.method === "PUT")).toHaveLength(1);
    vi.unstubAllEnvs();
  });
});

describe("settlePushed", () => {
  it("closes only what was pushed and is over", async () => {
    const over = notification("m/seen#needs");
    const waiting = notification("m/waiting#needs");
    const own = notification("m/seen#needs", false); // the page's own: its code takes it back
    const todo = notification("todo:1");
    shown = [over, waiting, own, todo];
    const closed = await settlePushed(new Map([["m/seen", "idle"], ["m/waiting", "needs_you"]]), new Set());
    expect(closed).toBe(1);
    expect([over.closed, waiting.closed, own.closed, todo.closed]).toEqual([true, false, false, false]);
  });
});
