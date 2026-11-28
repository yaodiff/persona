// @vitest-environment jsdom
// The sidebar's Persona row (#5581): pinned at the top of the list while the rest scrolls, with a fog under it once rows
// go beneath, and its shortcut (Mod+Alt+P by default, "Open Persona") shown on the row and following a rebinding.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// jsdom has no matchMedia (the sidebar's modules read it when they load) and no ResizeObserver (its highlight)
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { setSidebarCollapsed } from "../lib/layout";
import { reset } from "../lib/store";
import { clearBinding, resetAllBindings, setBinding, setShortcutEnabled } from "../lib/shortcuts";
import { useShortcuts } from "../lib/use-shortcuts";
import { Sidebar, SidebarRail } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const row = () => host.querySelector<HTMLAnchorElement>('a[aria-description="Persona 主对话"]')!;
const hint = () => row().querySelector("[data-persona-chord]")?.textContent ?? null;

beforeEach(() => {
  localStorage.clear();
  setSidebarCollapsed(false);
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("{}", { headers: { "Content-Type": "application/json" } }))));
  history.replaceState(null, "", "/overview");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetAllBindings();
  vi.unstubAllGlobals();
});

describe("the pinned Persona row", () => {
  it("sits in a sticky box at the top of the scrolling list, the line under it, above the pages", async () => {
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    const list = host.querySelector<HTMLElement>("[data-sidebar-rows]")!;
    const pinned = list.querySelector<HTMLElement>(":scope > [data-sidebar-sticky]")!;
    expect(pinned.className).toMatch(/\bsticky\b/);
    expect(pinned.className).toMatch(/\btop-0\b/);
    // opaque, over the rows (z-10) and their ⋯ buttons (z-20) that scroll beneath it
    expect(pinned.className).toMatch(/\bbg-canvas\b/);
    expect(pinned.className).toMatch(/\bz-30\b/);
    expect(pinned.contains(row())).toBe(true);
    expect(pinned.querySelector("[data-persona-divider]")).not.toBeNull();
    // it is the list's first block (after the highlight), so the pages and everything else scroll under it
    expect([...list.children].filter((c) => c.getAttribute("aria-hidden") !== "true")[0]).toBe(pinned);
    expect(pinned.textContent).not.toContain("页面");
    // Tab stops a row below it: the pinned box is about 62px with the 主对话 line (4 + 48 + 1 + 1 + 8), 58px on a
    // coarse pointer (44px touch row): 64px covers both
    expect(list.className).toMatch(/(^| )scroll-pt-16( |$)/);
    expect(list.className).not.toContain("scroll-pt-12");
  });

  it("keeps its selected state and its own hover fill (the gliding highlight passes under the pinned box)", async () => {
    act(() => root.render(<Sidebar route={{ name: "persona" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(row().getAttribute("aria-current")).toBe("page");
    expect(row().className).toContain("bg-hover-2");
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    expect(row().getAttribute("aria-current")).toBeNull();
    expect(row().className).toContain("hover:bg-hover");
  });

  it("scrolls sidebar rows inside a clipped sibling scroller below the pinned Persona row and keeps Liquid Glass headers static/transparent (#16663)", async () => {
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    const list = host.querySelector<HTMLElement>("[data-sidebar-rows]")!;
    const pinned = list.querySelector<HTMLElement>(":scope > [data-sidebar-sticky]")!;
    const scroller = list.querySelector<HTMLElement>(":scope > [data-sidebar-scroller][data-row-scroller]")!;
    expect(scroller).not.toBeNull();
    expect(scroller.contains(pinned)).toBe(false);
    expect(scroller.className).toMatch(/\boverflow-y-auto\b/);
    expect([...list.children].filter((c) => c.getAttribute("aria-hidden") !== "true")).toEqual([pinned, scroller]);

    const css = readFileSync(join(__dirname, "../styles.css"), "utf8");
    expect(css).not.toMatch(/:root\[data-surface-style="liquid-glass"\]\s+\[data-sidebar-rows\]\s*\{[^}]*mask-image/);
    expect(css).toMatch(/:root\[data-surface-style="liquid-glass"\]\s+\[data-app-shell\]\s+:is\(\[data-sidebar-root\],\s*\[data-sidebar-rail\],\s*\[data-sidebar-sticky\]\)\s*\{[^}]*background:\s*transparent/);
    expect(css).toMatch(/:root\[data-surface-style="liquid-glass"\]\s+\.sidebar-group-heading\s*\{[^}]*position:\s*static;\s*[^}]*background:\s*transparent/);
    expect(css).toMatch(/:root\[data-surface-style="liquid-glass"\]\s+\.sidebar-group-heading\[data-edge\]::after\s*\{[^}]*display:\s*none/);
    expect(css).toMatch(/\[data-sidebar-rows\]\s+\.group\\\/row:has\(>\s*button\[aria-haspopup="menu"\]\):hover\b/);
  });
});

describe("the Persona shortcut on the row", () => {
  it("shows the default chord, and follows a rebinding, clearing and turning it off", async () => {
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    // jsdom is not a Mac: Ctrl
    expect(hint()).toBe("Ctrl+Alt+P");
    expect(row().getAttribute("aria-keyshortcuts")).toBe("Control+Alt+P");
    act(() => setBinding("persona", "Mod+Shift+Y"));
    expect(hint()).toBe("Ctrl+Shift+Y");
    expect(row().getAttribute("aria-keyshortcuts")).toBe("Control+Shift+Y");
    act(() => setShortcutEnabled("persona", false));
    expect(hint()).toBeNull();
    expect(row().hasAttribute("aria-keyshortcuts")).toBe(false);
    act(() => setShortcutEnabled("persona", true));
    expect(hint()).toBe("Ctrl+Shift+Y");
    act(() => clearBinding("persona"));
    expect(hint()).toBeNull();
    act(() => resetAllBindings());
    expect(hint()).toBe("Ctrl+Alt+P");
  });

  it("is in the folded rail's tooltip too", async () => {
    act(() => root.render(<SidebarRail route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();
    const persona = host.querySelector<HTMLAnchorElement>('nav a[aria-label="Persona"]')!;
    expect(persona.title).toBe("Persona 主对话 (Ctrl+Alt+P)");
    act(() => setBinding("persona", "Mod+Shift+Y"));
    expect(persona.title).toBe("Persona 主对话 (Ctrl+Shift+Y)");
  });

  it("Ctrl+Alt+P opens the main conversation, as a click on the row does", async () => {
    function Shell() {
      useShortcuts({ route: { name: "home" }, narrow: false, toggleDrawer: () => {} });
      return null;
    }
    act(() => root.render(<Shell />));
    expect(location.pathname).toBe("/overview");
    act(() => void dispatchEvent(new KeyboardEvent("keydown", { key: "p", code: "KeyP", ctrlKey: true, altKey: true, bubbles: true, cancelable: true })));
    expect(location.pathname).toBe("/");
  });

  it("in the narrow drawer it closes the drawer too, also when Persona is already open", async () => {
    const close = vi.fn();
    function Shell() {
      useShortcuts({ route: { name: "persona" }, narrow: true, toggleDrawer: () => {}, closeDrawer: close });
      return null;
    }
    act(() => root.render(<Shell />));
    act(() => void dispatchEvent(new KeyboardEvent("keydown", { key: "p", code: "KeyP", ctrlKey: true, altKey: true, bubbles: true, cancelable: true })));
    expect(location.pathname).toBe("/");
    expect(close).toHaveBeenCalledTimes(1);
  });
});
