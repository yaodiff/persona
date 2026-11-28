// @vitest-environment jsdom
// "All chats"' ⋯ (#8324): the sidebar's Recent from here, the same settings as Settings has. "Show scheduled task chats"
// (the hub's, only when it has it) saves at once; the count is this browser's.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ state: { assistant: null as unknown, sessions: {} as Record<string, unknown[]> }, saved: [] as boolean[] }));
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return { ...real, api: vi.fn(async () => ({ conversations: [] })) };
});
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return {
    ...real,
    refreshAssistant: async () => {},
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), ...hub.state } as ReturnType<typeof real.getState>),
  };
});
vi.mock("../../lib/recent-task-chats", () => ({ saveRecentTaskChats: async (on: boolean) => void hub.saved.push(on) }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { History } from "./History";
import { recentCount, setRecentCount } from "../../lib/sidebar-recent";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const mount = async (recent?: boolean) => {
  hub.state = { assistant: { machine_id: "server", main: "main", conversations: [], ...(recent === undefined ? {} : { recent_task_chats: recent }) }, sessions: {} };
  root = createRoot(host);
  act(() => root.render(<History onToggleSidebar={() => {}} />));
  await flush();
};
const openOptions = async () => {
  await act(async () => (host.querySelector('button[aria-label="显示选项"]') as HTMLButtonElement).click());
  return [...document.querySelectorAll('[role="menuitem"], [role="menuitemradio"]')] as HTMLElement[];
};

beforeEach(() => {
  hub.saved = [];
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  setRecentCount(5);
});

describe("全部对话's ⋯ (#8324)", () => {
  it("has the switch with its state and saves the other value at once", async () => {
    await mount(false);
    const rows = await openOptions();
    expect(rows.map((r) => r.textContent?.trim().replace(/\s+/g, " "))).toEqual(["显示定时任务的对话", "侧栏最近对话5"]);
    await act(async () => rows[0].click());
    await flush();
    expect(hub.saved).toEqual([true]);
  });

  it("has only the count from a hub without the setting, and the count is the sidebar's own setting", async () => {
    await mount();
    const rows = await openOptions();
    expect(rows.map((r) => r.textContent?.trim().replace(/\s+/g, " "))).toEqual(["侧栏最近对话5"]);
    await act(async () => rows[0].click());
    const ten = [...document.querySelectorAll('[role="menuitemradio"]')].find((r) => r.textContent?.trim() === "10") as HTMLElement;
    await act(async () => ten.click());
    expect(recentCount()).toBe(10);
    expect(hub.saved).toEqual([]);
  });
});
