// @vitest-environment jsdom
// Hovering a row moves the gliding highlight without re-rendering the sidebar: the row under the pointer is a ref and
// the box is GlideHighlight's own (Sidebar.tsx useGlide), so a sweep over the rows renders only that span.
import { act, Profiler } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia = ((query: string) => ({ matches: query === "(hover: hover) and (pointer: fine)", media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// Count calls made directly in each body, including a re-render that only changes local state.
const calls = vi.hoisted(() => ({ sidebar: 0, agents: 0 }));
vi.mock("../lib/conversation-origins", async (load) => {
  const real = await load<typeof import("../lib/conversation-origins")>();
  return { ...real, recentConversations: (...a: Parameters<typeof real.recentConversations>) => (calls.sidebar++, real.recentConversations(...a)) };
});
vi.mock("../lib/agent-groups", async (load) => {
  const real = await load<typeof import("../lib/agent-groups")>();
  return { ...real, agentGrouping: (...a: Parameters<typeof real.agentGrouping>) => (calls.agents++, real.agentGrouping(...a)) };
});
import { refresh, reset } from "../lib/store";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";
import { SidebarRowHoverHost } from "./SidebarRowHover";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const machines: Machine[] = [{ id: "mac", name: "MacBook", online: true }];
const sessions: Session[] = Array.from({ length: 12 }, (_, i) => ({ machine_id: "mac", session_id: `s${i + 1}`, title: `task ${i + 1}`, cwd: "/code", status: "idle", state: "idle" }));
const chat: Session = { machine_id: "hub", session_id: "trip", title: "1125 | Plan | Trip itinerary", cwd: "/plans", status: "idle", assistant: true, last_text: "Three day trips are ready to compare." };
const answers: Record<string, unknown> = {
  "/api/v1/sessions": { sessions },
  "/api/v1/machines": { machines },
  "/api/v1/assistant": { machine_id: "hub", online: true, main: "main", conversations: [{ ...chat, session_id: "main", title: "Persona" }, chat] },
};

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  localStorage.clear();
  calls.sidebar = calls.agents = 0;
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(answers[new URL(url, "http://x").pathname] ?? {}), { headers: { "Content-Type": "application/json" } }))));
  // jsdom lays nothing out: each link is a 28px row at its place among the links, everything else a tall box
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    const i = this.tagName === "A" ? [...document.querySelectorAll("a")].indexOf(this as HTMLAnchorElement) : -1;
    const top = i < 0 ? 0 : i * 30;
    const height = i < 0 ? 10_000 : 28;
    return { top, bottom: top + height, height, left: 0, right: 200, width: 200, x: 0, y: top, toJSON() {} } as DOMRect;
  });
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("hovering sidebar rows", () => {
  it("moves the highlight without re-rendering the sidebar", async () => {
    let commits = 0;
    await act(() => refresh());
    act(() =>
      root.render(
        <Profiler id="sidebar" onRender={() => commits++}>
          <Sidebar route={{ name: "session", mid: "mac", sid: "gone" }} onSearch={() => {}} onNewAgent={() => {}} />
        </Profiler>,
      ),
    );
    await flush();
    const list = host.querySelector<HTMLElement>("[data-sidebar-rows]")!;
    const glide = list.querySelector<HTMLElement>("[data-glide]")!;
    const links = [...list.querySelectorAll<HTMLAnchorElement>("a")].filter((a) => a.textContent?.includes("task ")).slice(0, 10);
    expect(links).toHaveLength(10);
    expect(host.textContent).toContain("task 12");
    calls.sidebar = calls.agents = 0;
    commits = 0;

    const places = new Set<string>();
    for (const a of links) {
      act(() => void a.dispatchEvent(new MouseEvent("mouseover", { bubbles: true, relatedTarget: null })));
      expect(glide.style.opacity).toBe("1");
      places.add(glide.style.transform);
    }
    // each row put the box at its own place
    expect(places.size).toBe(10);
    console.info(`10 hovers: Sidebar renders ${calls.sidebar}, AgentsBlock renders ${calls.agents}, commits ${commits}`);
    expect(calls.sidebar).toBe(0);
    expect(calls.agents).toBe(0);

    // the pointer leaves the list: no open-row overlap; the box fades out instead
    act(() => void links[9].dispatchEvent(new MouseEvent("mouseout", { bubbles: true, relatedTarget: document.body })));
    expect(glide.style.opacity).toBe("0");
    expect(calls.sidebar).toBe(0);
  });

  it("mounts glide inside [data-sidebar-scroller], matches row horizontal bounds, snaps from hidden, and hides over active row (#17136)", async () => {
    vi.restoreAllMocks();
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      if (this.hasAttribute("data-sidebar-scroller")) {
        return { top: 100, bottom: 700, height: 600, left: 0, right: 260, width: 260, x: 0, y: 100, toJSON() {} } as DOMRect;
      }
      if (this.tagName === "A") {
        const hrefAttr = this.getAttribute("href") ?? "";
        const i = [...document.querySelectorAll("a")].indexOf(this as HTMLAnchorElement);
        const top = 120 + i * 36;
        // Simulate narrower indented/keepTrailing row bounds for agent links vs full-width links
        const left = hrefAttr.startsWith("/s/mac/") ? 22 : 10;
        const width = hrefAttr.startsWith("/s/mac/") ? 210 : 236;
        return { top, bottom: top + 32, height: 32, left, right: left + width, width, x: left, y: top, toJSON() {} } as DOMRect;
      }
      return { top: 0, bottom: 10_000, height: 10_000, left: 0, right: 260, width: 260, x: 0, y: 0, toJSON() {} } as DOMRect;
    });

    await act(() => refresh());
    act(() =>
      root.render(
        <Sidebar route={{ name: "session", mid: "mac", sid: "s1" }} onSearch={() => {}} onNewAgent={() => {}} />,
      ),
    );
    await flush();

    const list = host.querySelector<HTMLElement>("[data-sidebar-rows]")!;
    const scroller = list.querySelector<HTMLElement>("[data-sidebar-scroller]")!;
    const glide = list.querySelector<HTMLElement>("[data-glide]")!;
    expect(scroller.contains(glide)).toBe(true);
    // Initial state without hover: hidden so it never compounds on top of active row's CURRENT_ROW
    expect(glide.style.opacity).toBe("0");

    const activeRow = host.querySelector<HTMLAnchorElement>('a[href="/s/mac/s1"]')!;
    const otherAgent = host.querySelector<HTMLAnchorElement>('a[href="/s/mac/s2"]')!;
    const inboxRow = host.querySelector<HTMLAnchorElement>('a[href="/inbox"]')!;

    // Hovering non-active agent: appears via opacity snap (no transform slide from stale row) and matches row width
    act(() => void otherAgent.dispatchEvent(new MouseEvent("mouseover", { bubbles: true, relatedTarget: null })));
    expect(glide.style.opacity).toBe("1");
    expect(glide.style.left).toBe("22px");
    expect(glide.style.width).toBe("210px");
    expect(glide.style.transition).toBe("opacity 150ms ease");

    // Moving directly to another non-active row: glides via transform and adopts that row's width
    act(() => void inboxRow.dispatchEvent(new MouseEvent("mouseover", { bubbles: true, relatedTarget: null })));
    expect(glide.style.opacity).toBe("1");
    expect(glide.style.left).toBe("10px");
    expect(glide.style.width).toBe("236px");
    expect(glide.style.transition).toContain("transform");

    // Moving onto the active row: fades out so CURRENT_ROW never double-composites with [data-glide]
    act(() => void activeRow.dispatchEvent(new MouseEvent("mouseover", { bubbles: true, relatedTarget: null })));
    expect(glide.style.opacity).toBe("0");
  });

  it("opens and dismisses Persona and Agent cards without rendering either list", async () => {
    await act(() => refresh());
    act(() => root.render(<>
      <Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />
      <SidebarRowHoverHost />
    </>));
    await flush();
    const persona = host.querySelector<HTMLAnchorElement>('a[href="/persona/trip"]')!;
    const agents = [...host.querySelectorAll<HTMLAnchorElement>('a[href^="/s/mac/"]')].slice(0, 5);
    const card = () => document.querySelector('[data-sidebar-row-hover-card]');
    const pointer = (row: HTMLElement, type: "pointerover" | "pointerout") => {
      act(() => row.dispatchEvent(new PointerEvent(type, { pointerType: "mouse", bubbles: true, relatedTarget: document.body })));
    };

    expect(persona).toBeTruthy();
    expect(agents).toHaveLength(5);
    expect(calls.sidebar).toBeGreaterThan(0);
    expect(calls.agents).toBeGreaterThan(0);
    calls.sidebar = calls.agents = 0;
    vi.useFakeTimers();
    try {
      for (const row of agents) {
        pointer(row, "pointerover");
        act(() => vi.advanceTimersByTime(35));
        pointer(row, "pointerout");
      }
      act(() => vi.advanceTimersByTime(250));
      expect(card()).toBeNull();

      for (const row of [persona, agents[0]]) {
        pointer(row, "pointerover");
        act(() => vi.advanceTimersByTime(249));
        expect(card()).toBeNull();
        await act(async () => { vi.advanceTimersByTime(1); });
        await flush();
        expect(card()).not.toBeNull();
        expect(card()?.textContent).toContain(row.textContent);
        expect(host.contains(card())).toBe(false);
        pointer(row, "pointerout");
        expect(card()).toBeNull();
      }
      console.info(`Persona and Agent cards: Sidebar renders ${calls.sidebar}, AgentsBlock renders ${calls.agents}`);
      expect(calls.sidebar).toBe(0);
      expect(calls.agents).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });
});
