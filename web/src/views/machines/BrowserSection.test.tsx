// @vitest-environment jsdom
// "Browser (ego lite)" on a Mac's page (G107): the switch PUTs /browser and says so when that fails, the install / cancel /
// open buttons POST their action, the hub is asked again every 2 s while the setup is under way, and the sign-ins
// (BrowserLogins) only follow once the first-run setup is done.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// api() reads the token from localStorage, which this jsdom has none of
vi.hoisted(() => {
  const kept = new Map<string, string>();
  const g = globalThis as unknown as { localStorage: Pick<Storage, "getItem" | "setItem" | "removeItem"> };
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const toasts = vi.hoisted(() => [] as { text: string; tone?: string }[]);
vi.mock("../../ui/atoms", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ui/atoms")>()),
  toast: (text: string, tone?: string) => void toasts.push({ text, tone }),
}));
// the sign-ins are their own component (and their own requests); only whether they are there is this test's
vi.mock("./BrowserLogins", () => ({ BrowserLogins: () => <div data-testid="browser-logins" /> }));

import type { MachineBrowser } from "../../lib/browser";
import { BrowserSection } from "./BrowserSection";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let state: MachineBrowser = { enabled: false };
let failPut = false;
let calls: { method: string; path: string; body?: unknown }[] = [];

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const path = String(input).split("?")[0]!;
  const method = init?.method ?? "GET";
  calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined });
  if (path === "/api/v1/machines/mac/browser" && method === "PUT") {
    if (failPut) return Promise.resolve(json({ error: "boom" }, 500));
    state = { ...state, enabled: (JSON.parse(String(init!.body)) as { enabled: boolean }).enabled };
    return Promise.resolve(json(state));
  }
  if (path === "/api/v1/machines/mac/browser") return Promise.resolve(json(state));
  const action = /^\/api\/v1\/machines\/mac\/browser\/(install|open|cancel)$/.exec(path);
  if (action && method === "POST") {
    const what = action[1]!;
    state = what === "cancel" ? { ...state, job: undefined } : { ...state, job: { kind: what, state: "running" } };
    return Promise.resolve(json(state));
  }
  return Promise.resolve(json({ error: "not found" }, 404));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const sw = () => host.querySelector<HTMLButtonElement>('button[role="switch"]');
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const gets = () => calls.filter((c) => c.method === "GET" && c.path === "/api/v1/machines/mac/browser").length;
const render = (b: MachineBrowser, online = true) => act(() => root.render(<BrowserSection mid="mac" browser={b} online={online} />));

beforeEach(() => {
  failPut = false;
  calls = [];
  toasts.length = 0;
  vi.stubGlobal("fetch", vi.fn(hub));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("BrowserSection", () => {
  it("turns it on with a PUT and shows the install step", async () => {
    state = { enabled: false };
    render(state);
    await flush();
    expect(sw()?.getAttribute("aria-checked")).toBe("false");
    act(() => sw()!.click());
    await flush();
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ enabled: true });
    expect(sw()?.getAttribute("aria-checked")).toBe("true");
    expect(button("安装 ego lite")).toBeDefined();
  });

  it("says so in a toast when the switch cannot be saved, and stays as it was", async () => {
    state = { enabled: false };
    failPut = true;
    render(state);
    await flush();
    act(() => sw()!.click());
    await flush();
    expect(toasts).toHaveLength(1);
    expect(toasts[0]!.tone).toBe("error");
    expect(toasts[0]!.text).toContain("开启失败");
    expect(sw()?.getAttribute("aria-checked")).toBe("false");
  });

  it("installs, then cancels the running install", async () => {
    state = { enabled: true, installed: false };
    render(state);
    await flush();
    act(() => button("安装 ego lite")!.click());
    await flush();
    expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/machines/mac/browser/install")).toBe(true);
    expect(host.textContent).toContain("正在安装");
    act(() => button("取消")!.click());
    await flush();
    expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/machines/mac/browser/cancel")).toBe(true);
    expect(button("安装 ego lite")).toBeDefined();
  });

  it("opens it for the first-run setup and asks again every 2 s while that is under way", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    state = { enabled: true, installed: true, set_up: false };
    render(state);
    await flush();
    expect(host.querySelectorAll("[data-step]")).toHaveLength(4);
    const before = gets();
    // not watching yet: nothing was opened on the Mac
    await act(async () => { vi.advanceTimersByTime(4000); });
    expect(gets()).toBe(before);

    act(() => button("打开 ego lite")!.click());
    await flush();
    expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/machines/mac/browser/open")).toBe(true);
    const opened = gets();
    await act(async () => { vi.advanceTimersByTime(2000); });
    await flush();
    expect(gets()).toBe(opened + 1);
    await act(async () => { vi.advanceTimersByTime(2000); });
    await flush();
    expect(gets()).toBe(opened + 2);

    // the setup is done: it stops asking and the sign-ins follow
    state = { enabled: true, installed: true, set_up: true, ready: true, job: undefined };
    await act(async () => { vi.advanceTimersByTime(2000); });
    await flush();
    const done = gets();
    await act(async () => { vi.advanceTimersByTime(6000); });
    expect(gets()).toBe(done);
    expect(host.querySelector('[data-testid="browser-logins"]')).not.toBeNull();
  });

  it("shows the sign-ins only once the first-run setup is done", async () => {
    state = { enabled: true, installed: true, set_up: false };
    render(state);
    await flush();
    expect(host.querySelector('[data-testid="browser-logins"]')).toBeNull();
    act(() => root.unmount());
    root = createRoot(host);
    state = { enabled: true, installed: true, set_up: true, ready: true, version: "1.0" };
    render(state);
    await flush();
    expect(host.querySelector('[data-testid="browser-logins"]')).not.toBeNull();
    expect(host.textContent).toContain("ego lite 1.0");
  });
});
