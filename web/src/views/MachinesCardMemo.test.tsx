// @vitest-environment jsdom
// Machines: a sessions push re-renders only the card of the computer it is about. Every card gets its own computer's agent
// counts as numbers from the page and is memoized (MachineCard), so a push about another computer leaves it alone.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// every render of a card reads its deployment (the card and its MachineDeploy): counted per machine
const renders = vi.hoisted(() => new Map<string, number>());
vi.mock("../lib/machine-deploy", async (load) => {
  const real = await load<typeof import("../lib/machine-deploy")>();
  return {
    ...real,
    machineDeployment: (m: Parameters<typeof real.machineDeployment>[0]) => {
      renders.set(m.id, (renders.get(m.id) ?? 0) + 1);
      return real.machineDeployment(m);
    },
  };
});
import { getState, refresh, reset, upsertSession } from "../lib/store";
import type { Machine, Session } from "../lib/types";
import { Machines } from "./Machines";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const machines: Machine[] = [
  { id: "mac", name: "MacBook", online: true },
  { id: "nas", name: "NAS", online: true },
];
const sessions: Session[] = [
  { machine_id: "mac", session_id: "m1", title: "任务 1", cwd: "/code", status: "idle", state: "idle" },
  { machine_id: "mac", session_id: "m2", title: "任务 2", cwd: "/code", status: "idle", state: "idle" },
  { machine_id: "nas", session_id: "n1", title: "任务 3", cwd: "/data", status: "working", state: "working" },
];
const answers: Record<string, unknown> = { "/api/v1/sessions": { sessions }, "/api/v1/machines": { machines }, "/api/v1/metrics": { machines: [] } };

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const card = (name: string) => [...host.querySelectorAll("section")].find((s) => s.querySelector("h2")?.textContent === name)?.textContent ?? "";

beforeEach(() => {
  localStorage.clear();
  renders.clear();
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(answers[new URL(url, "http://x").pathname] ?? {}), { headers: { "Content-Type": "application/json" } }))));
  history.replaceState(null, "", "/machines");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("a sessions push on the machines page", () => {
  it("re-renders only the card of the computer it is about", async () => {
    await act(() => refresh());
    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await flush();
    expect(card("MacBook")).toContain("2 个 Agent");
    expect(card("NAS")).toContain("1 个 Agent，1 个在工作");
    expect([...renders.keys()].sort()).toEqual(["mac", "nas"]);
    renders.clear();

    // MacBook's agent starts working: its card says so, the NAS card does not render
    act(() => upsertSession({ ...getState().sessions.mac[0], status: "working", state: "working" }));
    await flush();
    expect(card("MacBook")).toContain("2 个 Agent，1 个在工作");
    expect(renders.has("mac")).toBe(true);
    expect(renders.has("nas")).toBe(false);
    renders.clear();

    // a push that changes nothing the cards show (a new title) renders neither card
    act(() => upsertSession({ ...getState().sessions.mac[1], title: "任务 2（改）" }));
    await flush();
    expect(getState().sessions.mac[1].title).toBe("任务 2（改）");
    expect([...renders.keys()]).toEqual([]);
  });

  it("a machines refresh that only moves last_seen on renders no card, a real change renders that card", async () => {
    answers["/api/v1/machines"] = { machines: machines.map((m) => ({ ...m, last_seen: "2026-05-01T10:00:00Z" })) };
    try {
      await act(() => refresh());
      act(() => root.render(<Machines onToggleSidebar={() => {}} />));
      await flush();
      renders.clear();

      answers["/api/v1/machines"] = { machines: machines.map((m) => ({ ...m, last_seen: "2026-05-01T10:00:30Z" })) };
      await act(() => refresh());
      await flush();
      expect(getState().machines[0].last_seen).toBe("2026-05-01T10:00:30Z");
      expect([...renders.keys()]).toEqual([]);

      answers["/api/v1/machines"] = { machines: machines.map((m) => ({ ...m, last_seen: "2026-05-01T10:01:00Z", ...(m.id === "nas" ? { claude_version: "2.0.1" } : {}) })) };
      await act(() => refresh());
      await flush();
      expect(card("NAS")).toContain("claude 2.0.1");
      expect([...renders.keys()]).toEqual(["nas"]);
    } finally {
      answers["/api/v1/machines"] = { machines };
    }
  });
});
