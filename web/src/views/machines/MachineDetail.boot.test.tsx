// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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
const flush = async () => { for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); }); };

beforeEach(() => {
  vi.stubEnv("TZ", "America/New_York");
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response(JSON.stringify({ error: "not found" }), { status: 404 }))));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.unstubAllEnvs(); });

describe("MachineDetail boot time", () => {
  it.each([
    ["2026-07-01T04:30:00Z", "2026-12-01T12:00:00Z", "2026年7月1日", "00:30"],
    ["2026-12-01T04:30:00Z", "2027-07-01T12:00:00Z", "2026年11月30日", "23:30"],
  ])("shows the local time and date at boot (%s), not the offset when viewed", async (boot, now, date, time) => {
    vi.spyOn(Date, "now").mockReturnValue(Date.parse(now));
    fake.machines = [{ id: "box", name: "Box", online: false, system: { boot_time: Date.parse(boot) / 1000 } } as Machine];
    act(() => root.render(<MachineDetail subview="diagnostics" mid="box" onToggleSidebar={() => {}} />));
    await flush();
    const row = [...host.querySelectorAll("dt")].find((dt) => dt.textContent === "开机时间")?.parentElement;
    expect(row?.textContent).toContain(date);
    expect(row?.textContent).toContain(time);
  });
});
