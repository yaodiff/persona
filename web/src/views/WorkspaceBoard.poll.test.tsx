// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// The hub events the board listens to, so a test can push one.
const hub = vi.hoisted(() => ({ listeners: new Map<string, Set<(msg: Record<string, unknown>) => void>>() }));
vi.mock("../lib/store", async (load) => ({
  ...(await load<typeof import("../lib/store")>()),
  onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => { hub.listeners.get(type)?.delete(l); };
  },
}));
vi.mock("../lib/workspace-board-setting", () => ({ useWorkspaceBoardSetting: () => ({ enabled: true }) }));
import { reset } from "../lib/store";
import { resetManagerChoice, setRosterDefault } from "../lib/manager-choice";
import { CATALOG_POLL_MS } from "../lib/workspace-catalog";
import { WorkspaceBoard } from "./WorkspaceBoard";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let revision = 1;
const board = () => ({ revision, can_write: true, columns: [{ id: "todo", title: "To do" }], cards: [] });
let reads: string[] = [];
let host: HTMLDivElement;
let root: Root;
const flush = async () => { for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); }); };
const count = (path: string) => reads.filter((p) => p === path).length;
const push = async (type: string, msg: Record<string, unknown> = {}) => { act(() => hub.listeners.get(type)?.forEach((l) => l({ type, ...msg }))); await flush(); };
const wait = async (ms: number) => { await act(async () => { vi.advanceTimersByTime(ms); }); await flush(); };

beforeEach(() => {
  revision = 1; reads = []; hub.listeners.clear();
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    const path = new URL(url, "http://x").pathname;
    reads.push(path);
    const body = path === "/api/v1/workspace-board" ? board() : path === "/api/v1/workspaces" ? { workspaces: [] } : path === "/api/v1/todos" ? { todos: [] } : {};
    return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
  }));
  setRosterDefault("assistant"); // A real app resolves the roster before manager-owned board reads.
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetManagerChoice();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("how often an open workflow board reads the hub", () => {
  it("waits for the default owner before any board or discovery read, including timer and reconnect", async () => {
    resetManagerChoice();
    act(() => root.render(<WorkspaceBoard />));
    await flush();
    await wait(CATALOG_POLL_MS);
    await push("hello");
    expect(count("/api/v1/workspace-board")).toBe(0);
    expect(count("/api/v1/workspaces")).toBe(0);

    act(() => setRosterDefault("assistant"));
    await flush();
    expect(count("/api/v1/workspace-board")).toBe(1);
    expect(count("/api/v1/workspaces")).toBe(1);
  });

  it("reads the board and discovery once a minute, not every 10 s: a write arrives as a push", async () => {
    act(() => root.render(<WorkspaceBoard />));
    await flush();
    expect(count("/api/v1/workspace-board")).toBe(1);
    expect(count("/api/v1/workspaces")).toBe(1);

    for (let s = 0; s < 5; s++) await wait(10_000); // 50 s: was 5 more of each (one per 10 s)
    await wait(CATALOG_POLL_MS - 51_000);
    expect(count("/api/v1/workspace-board")).toBe(1);
    expect(count("/api/v1/workspaces")).toBe(1);

    await wait(1_000);
    expect(count("/api/v1/workspace-board")).toBe(2);
    expect(count("/api/v1/workspaces")).toBe(2);

    // a write elsewhere: the push reads it at once, without waiting for the timer
    revision = 2;
    await push("workspace_board", { revision: 2 });
    expect(count("/api/v1/workspace-board")).toBe(3);
    // an echo of what the page already has reads nothing
    await push("workspace_board", { revision: 2 });
    expect(count("/api/v1/workspace-board")).toBe(3);
  });

  it("reads again after a reconnect that may have missed pushes, not after one that replayed them", async () => {
    act(() => root.render(<WorkspaceBoard />));
    await flush();
    expect(count("/api/v1/workspace-board")).toBe(1);

    await push("hello", { resumed: true });
    expect(count("/api/v1/workspace-board")).toBe(1);

    await push("hello");
    expect(count("/api/v1/workspace-board")).toBe(2);
    expect(count("/api/v1/workspaces")).toBe(2);
  });

  it("stops listening when the board closes", async () => {
    act(() => root.render(<WorkspaceBoard />));
    await flush();
    act(() => root.unmount());
    root = createRoot(host);
    const before = reads.length;
    await push("hello");
    await wait(CATALOG_POLL_MS);
    expect(reads.length).toBe(before);
  });
});
