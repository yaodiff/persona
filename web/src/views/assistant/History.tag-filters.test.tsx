// @vitest-environment jsdom
// #12018: History supplies the controlled bar's entries and selection; the hub owns their exact saved order.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "../../lib/types";

const hub = vi.hoisted(() => ({
  fetched: [] as Session[],
  assistant: { conversation_tag_filters: null as string[] | null } as Record<string, unknown>,
  listeners: new Set<() => void>(),
  writes: [] as unknown[],
  fail: false,
}));
const emit = () => hub.listeners.forEach((listener) => listener());
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return { ...real, api: vi.fn(async (path: string, init?: { method?: string; json?: unknown }) => {
    if (path === "/assistant?archived=1") return { conversations: hub.fetched };
    if (path === "/assistant" && init?.method === "PUT") {
      hub.writes.push(init.json);
      if (hub.fail) throw new real.ApiError(0, "offline", {});
      hub.assistant = { ...hub.assistant, conversation_tag_filters: structuredClone((init.json as { conversation_tag_filters: string[] | null }).conversation_tag_filters) };
      emit();
      return hub.assistant;
    }
    throw new real.ApiError(404, "not found", {});
  }) };
});
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  const { useSyncExternalStore } = await import("react");
  return { ...real, refreshAssistant: async () => {}, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => useSyncExternalStore(
    (listener) => (hub.listeners.add(listener), () => void hub.listeners.delete(listener)),
    () => select({ ...real.getState(), assistant: hub.assistant, sessions: { server: hub.fetched } } as unknown as ReturnType<typeof real.getState>),
  ) };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { History } from "./History";
import { toast } from "../../ui/atoms";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});
const conv = (sid: string, tags?: string[]): Session => ({ machine_id: "server", session_id: sid, title: sid, cwd: "", status: "idle", assistant: true, updated_at: "2026-11-01T09:00:00Z", tags } as Session);
let host: HTMLDivElement, root: Root;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const chips = () => [...host.querySelectorAll('[aria-label="按标签筛选"] button[aria-pressed]')].map((b) => b.textContent?.trim());
const shownIds = () => [...host.querySelectorAll("a[href*='/persona/']")].map((a) => /\/persona\/([^?]+)/.exec(a.getAttribute("href") ?? "")?.[1]).filter(Boolean);
const customize = async () => {
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="显示选项"]')!.click());
  const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"], button')].find((b) => b.textContent?.trim() === "自定义标签过滤…")!;
  await act(async () => item.click());
  await flush();
};
const setInput = (input: HTMLInputElement, value: string) => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
};

beforeEach(async () => {
  hub.fetched = [conv("main"), conv("alpha", ["trip"]), conv("beta", ["trip", "planning"]), conv("gamma")];
  hub.assistant = { machine_id: "server", main: "main", conversations: hub.fetched, conversation_tag_filters: null };
  hub.writes = [];
  hub.fail = false;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<History onToggleSidebar={() => {}} />));
  await flush();
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

describe("History custom tag filters (#12018)", () => {
  it("adds an exact entry through the hub and keeps tag filtering exact", async () => {
    expect(chips()).toEqual(["planning", "trip"]);
    await customize();
    const input = host.querySelector<HTMLInputElement>('input[aria-label="输入精确标签"]')!;
    act(() => setInput(input, "trip-long"));
    act(() => input.closest("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    await flush();
    expect(hub.writes).toEqual([{ conversation_tag_filters: ["planning", "trip", "trip-long"] }]);
    expect(chips()).toEqual(["planning", "trip", "trip-long"]);
    const chip = [...host.querySelectorAll<HTMLButtonElement>('[aria-label="按标签筛选"] button[aria-pressed]')].find((b) => b.textContent === "trip-long")!;
    await act(async () => chip.click());
    await flush();
    expect(shownIds()).toEqual([]); // exact tag, not the chats carrying its prefix "trip"
    await act(async () => chip.click());
    await act(async () => [...host.querySelectorAll<HTMLButtonElement>('[aria-label="按标签筛选"] button[aria-pressed]')].find((b) => b.textContent === "trip")!.click());
    await flush();
    expect(shownIds()).toEqual(["alpha", "beta"]);
  });

  it("clears a removed active filter, restores automatic chips, and reports failed saves", async () => {
    const trip = [...host.querySelectorAll<HTMLButtonElement>('[aria-label="按标签筛选"] button[aria-pressed]')].find((b) => b.textContent === "trip")!;
    await act(async () => trip.click());
    await customize();
    await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="移除过滤「trip」"]')!.click());
    await flush();
    expect(hub.writes.at(-1)).toEqual({ conversation_tag_filters: ["planning"] });
    expect(shownIds()).toEqual(["main", "alpha", "beta", "gamma"]);
    await act(async () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "恢复全部在用标签")!.click());
    await flush();
    expect(hub.writes.at(-1)).toEqual({ conversation_tag_filters: null });
    expect(chips()).toEqual(["planning", "trip"]);

    hub.fail = true;
    const input = host.querySelector<HTMLInputElement>('input[aria-label="输入精确标签"]')!;
    act(() => setInput(input, "offline"));
    act(() => input.closest("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    await flush();
    expect(toast).toHaveBeenLastCalledWith(expect.stringContaining("无法保存标签过滤设置"), "error");
    expect(chips()).toEqual(["planning", "trip"]);
  });
});
