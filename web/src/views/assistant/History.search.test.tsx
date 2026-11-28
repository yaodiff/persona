// @vitest-environment jsdom
// "All chats" finds a chat by what was said in it (#6283): with the hub's assistant_search feature the manager's results
// merge into the local matches (titles, last messages), a chat found by a message shows that sentence with the match
// marked and opens at it; without the feature, or on a 404, the page filters titles and last messages as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "../../lib/types";

const hub = vi.hoisted(() => ({
  fetched: [] as unknown[],
  state: { assistant: null as unknown, sessions: {} as Record<string, unknown[]>, features: [] as string[] },
  listeners: new Set<() => void>(),
  searched: [] as string[],
  answer: null as unknown,
  missing: false,
}));
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string) => {
      if (path === "/assistant?archived=1") return { conversations: hub.fetched };
      if (path.startsWith("/assistant/search?")) {
        hub.searched.push(decodeURIComponent(path.split("q=")[1].split("&")[0]));
        if (hub.missing) throw new real.ApiError(404, "not found", {});
        return hub.answer;
      }
      throw new real.ApiError(404, "not found", {});
    }),
  };
});
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  const { useSyncExternalStore } = await import("react");
  return {
    ...real,
    refreshAssistant: async () => {},
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T =>
      useSyncExternalStore(
        (l) => (hub.listeners.add(l), () => void hub.listeners.delete(l)),
        () => select({ ...real.getState(), ...hub.state } as ReturnType<typeof real.getState>),
      ),
  };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { History } from "./History";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

const conv = (sid: string, p: Partial<Session> = {}): Session => ({
  machine_id: "server", session_id: sid, title: `对话 ${sid}`, cwd: "/home/me/.persona/assistant", status: "idle", assistant: true,
  updated_at: "2026-11-01T09:00:00Z", ...p,
} as Session);

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const settle = async () => {
  await act(async () => { await new Promise((ok) => setTimeout(ok, 260)); });
  await flush();
};
const type = async (q: string) => {
  const input = host.querySelector("input") as HTMLInputElement;
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    set.call(input, q);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await settle();
};
const row = (sid: string) => [...host.querySelectorAll("a")].find((a) => a.textContent?.includes(`对话 ${sid}`));

beforeEach(async () => {
  hub.searched = [];
  hub.missing = false;
  hub.fetched = [conv("main"), conv("trip", { last_text: "路线已发你。" }), conv("keep"), conv("old", { archived: true } as Partial<Session>)];
  hub.state = {
    assistant: { machine_id: "server", main: "main", conversations: [conv("main"), conv("trip"), conv("keep")] },
    sessions: { server: [] },
    features: ["assistant_search"],
  };
  hub.answer = {
    results: [
      { session_id: "trip", machine_id: "server", title: "对话 trip", hits: [{ item_id: "i1#0", seq: 1, role: "user", snippet: "我想去青石巷 BlueLane 看看", ranges: [[7, 15]] }] },
      { session_id: "old", machine_id: "server", title: "对话 old", archived: true, hits: [{ item_id: "i3#0", seq: 3, role: "assistant", snippet: "旧的 BlueLane", ranges: [[3, 11]] }] },
    ],
    partial: false,
    indexed: { done: 4, total: 4 },
  };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<History onToggleSidebar={() => {}} />));
  await flush();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("All chats' search with the manager's (#6283)", () => {
  it("says it searches chats, finds them by what was said, marks the match and opens at that message", async () => {
    expect((host.querySelector("input") as HTMLInputElement).placeholder).toBe("搜索对话");
    await type("BlueLane");
    expect(hub.searched).toEqual(["BlueLane"]);
    const trip = row("trip")!;
    expect(trip.querySelector("[data-said]")?.textContent).toBe("我想去青石巷 BlueLane 看看");
    expect(trip.querySelector("mark")?.textContent).toBe("BlueLane");
    expect(trip.getAttribute("href")).toContain("at=i1%230");
    // an archived one too, under Archived (open while searching)
    expect(row("old")?.querySelector("mark")?.textContent).toBe("BlueLane");
    expect(row("keep")).toBeUndefined();
  });

  it("keeps the local matches the manager did not return, and says when it is still going through older chats", async () => {
    hub.answer = { ...(hub.answer as object), partial: true, results: [(hub.answer as { results: unknown[] }).results[0]] };
    await type("keep");
    expect(row("keep")).toBeDefined(); // its title, matched here
    expect(row("keep")?.getAttribute("href")).not.toContain("at=");
    expect(row("trip")?.querySelector("mark")).not.toBeNull();
    expect(host.textContent).toContain("正在索引较早的对话");
  });

  it("filters titles and last messages as before on a hub without it", async () => {
    act(() => root.unmount());
    hub.state = { ...hub.state, features: [] };
    root = createRoot(host);
    act(() => root.render(<History onToggleSidebar={() => {}} />));
    await flush();
    expect((host.querySelector("input") as HTMLInputElement).placeholder).toBe("搜索标题或最新消息");
    await type("路线");
    expect(hub.searched).toEqual([]);
    expect(row("trip")?.textContent).toContain("路线已发你。");
    expect(row("trip")?.getAttribute("href")).not.toContain("at=");
  });

  it("falls back to the local matches when the manager has no such route (404)", async () => {
    hub.missing = true;
    await type("路线");
    expect(row("trip")?.textContent).toContain("路线已发你。");
    expect(host.querySelector("mark")).toBeNull();
  });
});

it("finds the main conversation by its displayed Main chat label and keeps the home symbol", async () => {
  await type("主对话");
  const main = [...host.querySelectorAll("a")].find((a) => a.textContent?.includes("主对话"));
  expect(main).toBeDefined();
  expect(main?.querySelector('path[d^="M3 10.5 12 3"]')).not.toBeNull();
  expect(main?.textContent).not.toContain("对话 main");
});
