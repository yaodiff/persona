// @vitest-environment jsdom
// A sessions push re-renders the sidebar, but an agent's row renders again only when its own session changed: the rest
// keep their session object (lib/sessions-delta.ts) and the row is memoized (SessionRow).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// every render of a SessionRow reads its preview once: counted per session
const previews = vi.hoisted(() => new Map<string, number>());
vi.mock("../lib/format", async (load) => {
  const real = await load<typeof import("../lib/format")>();
  return {
    ...real,
    sessionPreview: (s: Parameters<typeof real.sessionPreview>[0]) => {
      previews.set(s.session_id, (previews.get(s.session_id) ?? 0) + 1);
      return real.sessionPreview(s);
    },
  };
});
import { getState, refresh, reset, upsertSession } from "../lib/store";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const machines: Machine[] = [{ id: "mac", name: "MacBook", online: true }];
const sessions: Session[] = ["s1", "s2", "s3", "s4"].map((id, i) => ({ machine_id: "mac", session_id: id, title: `任务 ${i + 1}`, cwd: "/code", status: "idle", state: "idle" }));
const answers: Record<string, unknown> = { "/api/v1/sessions": { sessions }, "/api/v1/machines": { machines } };

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  localStorage.clear();
  previews.clear();
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(answers[new URL(url, "http://x").pathname] ?? {}), { headers: { "Content-Type": "application/json" } }))));
  history.replaceState(null, "", "/");
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

describe("a sessions push", () => {
  it("re-renders only the row of the session that changed", async () => {
    await act(() => refresh());
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(host.textContent).toContain("任务 4");
    expect([...previews.keys()].sort()).toEqual(["s1", "s2", "s3", "s4"]);
    previews.clear();

    act(() => upsertSession({ ...getState().sessions.mac[1], title: "任务 2（改）" }));
    await flush();
    expect(host.textContent).toContain("任务 2（改）");
    expect([...previews.keys()]).toEqual(["s2"]);
  });

  it("moving to another conversation re-renders only the two rows whose highlight moves", async () => {
    await act(() => refresh());
    act(() => root.render(<Sidebar route={{ name: "session", mid: "mac", sid: "s1" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    previews.clear();
    act(() => root.render(<Sidebar route={{ name: "session", mid: "mac", sid: "s3" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect([...previews.keys()].sort()).toEqual(["s1", "s3"]);
    expect(host.querySelector('a[aria-current="page"]')?.textContent).toContain("任务 3");
  });

  it("an unchanged offline row still moves its age on with the clock", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    try {
      vi.setSystemTime(new Date("2026-05-01T10:00:00Z"));
      answers["/api/v1/sessions"] = { sessions: sessions.map((s) => (s.session_id === "s4" ? { ...s, status: "offline", state: "offline", updated_at: new Date().toISOString() } : s)) };
      await act(() => refresh());
      // the offline row is under Recent (not running), open here: a folded group mounts no rows
      localStorage.setItem("fold:recent", "0");
      act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
      await flush();
      const row = () => [...host.querySelectorAll("a")].find((a) => a.textContent?.includes("任务 4"))?.textContent ?? "";
      expect(row()).toContain("刚刚");

      // ten minutes on, a push for another session: s4's row keeps its props (memo) but not a stale age
      act(() => vi.advanceTimersByTime(10 * 60_000));
      act(() => upsertSession({ ...getState().sessions.mac[0], title: "任务 1（改）" }));
      await flush();
      expect(row()).not.toContain("刚刚");
      expect(row()).toMatch(/10/);
    } finally {
      answers["/api/v1/sessions"] = { sessions };
      vi.useRealTimers();
    }
  });
});
