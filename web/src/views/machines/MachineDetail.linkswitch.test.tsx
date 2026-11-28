// @vitest-environment jsdom
// Machine detail and a switch to Persona Link (#10662): the switch shows what the machine list says, posts through the
// app's own request, and while it runs the computer's disconnect is expected (no offline box, no Reconnect), though it
// is of kind link and offline meanwhile.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetLinkSwitchAttempts } from "../../lib/link-switch";
import type { Machine } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const fake = vi.hoisted(() => ({ machines: [] as Machine[] }));
vi.mock("../../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: true, machines: fake.machines }) };
});

const { MachineDetail } = await import("./MachineDetail");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const button = (label: string) => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const posts = () => fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === "POST");

beforeEach(() => {
  resetLinkSwitchAttempts();
  fetchMock = vi.fn((url: string, init?: RequestInit) =>
    Promise.resolve(
      init?.method === "POST" && String(url).endsWith("/link-switch")
        ? new Response(JSON.stringify({ link_switch: { available: false, reason: "in_progress", phase: "checking", at: "2026-11-19T08:00:00Z" } }), { status: 202 })
        : new Response(JSON.stringify({ error: "not found" }), { status: 404 }),
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

describe("MachineDetail and a switch to Persona Link", () => {
  it("hides the expected disconnect while a switch runs, and shows its steps", async () => {
    fake.machines = [{ id: "box", name: "盒子", kind: "link", online: false, error: "Persona Link 没有连上来", error_code: "link_offline", link_switch: { available: false, reason: "in_progress", phase: "connecting" } } as Machine];
    act(() => root.render(<MachineDetail subview="management" mid="box" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("[data-offline-box]")).toBeNull();
    expect(button("重新连接")).toBeUndefined();
    expect(host.querySelector("[data-step=connecting]")?.getAttribute("data-state")).toBe("current");
  });

  it("still shows the offline box when no switch runs", async () => {
    fake.machines = [{ id: "box", name: "盒子", kind: "ssh", online: false, error: "something odd", link_switch: { available: false, reason: "offline" } } as Machine];
    act(() => root.render(<MachineDetail subview="management" mid="box" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("[data-offline-box]")).not.toBeNull();
    expect(host.querySelector("[data-link-switch]")).toBeNull(); // the offline box says it; nothing of a switch too
  });

  it("shows nothing about switching for an older hub", async () => {
    fake.machines = [{ id: "box", name: "盒子", kind: "ssh", online: true } as Machine];
    act(() => root.render(<MachineDetail subview="management" mid="box" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("[data-link-switch]")).toBeNull();
  });

  it("posts once through the app's request to the machine's link-switch route", async () => {
    fake.machines = [{ id: "box", name: "盒子", kind: "ssh", online: true, home: "/home/alice", link_switch: { available: true } } as Machine];
    act(() => root.render(<MachineDetail subview="management" mid="box" onToggleSidebar={() => {}} />));
    await flush();
    await act(async () => button("切换到 Persona Link…")!.click());
    await act(async () => button("切换")!.click());
    await flush();
    expect(posts()).toHaveLength(1);
    const [url, init] = posts()[0]! as [string, RequestInit];
    expect(url).toMatch(/\/api\/v1\/machines\/box\/link-switch$/);
    expect(new Headers(init.headers).get("Authorization")).toMatch(/^Bearer\b/);
    expect(host.querySelector("[data-step=checking]")?.getAttribute("data-state")).toBe("current");
  });
});
