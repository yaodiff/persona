// @vitest-environment jsdom
import { act, StrictMode, useCallback, useEffect, useRef, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PANEL_GAP, SIDEBAR_DEFAULT, setSidebarCollapsed, takeFocusHandoff, toggleSidebarColumn, useLayout } from "./layout";
import { SIDEBAR_COLLAPSE_MS, SIDEBAR_EXPAND_MS, SIDEBAR_RAIL_WIDTH, useSidebarTransition, type SidebarMode } from "./sidebar-transition";
import type { MacContourDriver } from "../views/MacChrome";
import { CollapseButton } from "../views/SidebarRail";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let reducedMotion = false;
let now = 1000;
let nextRafId = 1;
let frames = new Map<number, FrameRequestCallback>();

function rect(width: number, height = 720): DOMRect {
  return { x: 0, y: 0, left: 0, top: 0, right: width, bottom: height, width, height, toJSON: () => ({}) } as DOMRect;
}

function advanceFrame(ms: number, inspectBeforeCommit?: () => void) {
  now += ms;
  const pending = [...frames.entries()];
  frames.clear();
  act(() => {
    for (const [, cb] of pending) cb(now);
    inspectBeforeCommit?.();
  });
}

function Layer({ name, active = true, counts }: { name: string; active?: boolean; counts: { mounts: number; unmounts: number } }) {
  useEffect(() => {
    counts.mounts += 1;
    return () => {
      counts.unmounts += 1;
    };
  }, [counts]);
  return (
    <div data-layer-child={name} data-active={active ? "1" : "0"}>
      {active && <div data-layer-content={name} />}
    </div>
  );
}

function Harness({
  mode,
  narrow = false,
  macWindowed = false,
  colWidth = SIDEBAR_DEFAULT,
  mainWidth = 960,
  customSidebarW,
  contourDraw,
  fullCounts = { mounts: 0, unmounts: 0 },
  railCounts = { mounts: 0, unmounts: 0 },
  onState,
}: {
  mode: SidebarMode;
  narrow?: boolean;
  macWindowed?: boolean;
  colWidth?: number;
  mainWidth?: number;
  customSidebarW?: number;
  contourDraw?: (bx: number) => void;
  fullCounts?: { mounts: number; unmounts: number };
  railCounts?: { mounts: number; unmounts: number };
  onState?: (s: { animating: boolean; leaving: "expanded" | "rail" | null }) => void;
}) {
  const shellRef = useRef<HTMLDivElement>(null);
  const columnRef = useRef<HTMLDivElement>(null);
  const fullLayerRef = useRef<HTMLDivElement>(null);
  const railLayerRef = useRef<HTMLDivElement>(null);
  const contourDriverRef = useRef<MacContourDriver | null>(contourDraw ? { draw: contourDraw } : null);
  const [mainEl, setMainEl] = useState<HTMLElement | null>(null);

  const setColumnRef = useCallback((el: HTMLDivElement | null) => {
    columnRef.current = el;
    if (el) {
      el.getBoundingClientRect = () => rect(colWidth);
      if (customSidebarW !== undefined) el.style.setProperty("--sidebar-w", `${customSidebarW}px`);
    }
  }, [colWidth, customSidebarW]);

  const setMainRef = useCallback((el: HTMLElement | null) => {
    if (el) el.getBoundingClientRect = () => rect(mainWidth);
    setMainEl(el);
  }, [mainWidth]);

  const transition = useSidebarTransition({
    shellRef,
    columnRef,
    mainEl,
    fullLayerRef,
    railLayerRef,
    contourDriverRef,
    mode,
    narrow,
    macWindowed,
  });

  useEffect(() => {
    onState?.(transition);
  });

  return (
    <div ref={shellRef} data-app-shell="">
      <div ref={setColumnRef} data-sidebar-column="">
        {(narrow || mode === "expanded" || transition.leaving === "expanded") && (
          <div key="full" ref={fullLayerRef} data-full-layer="">
            <Layer name="full" active={narrow ? false : mode === "expanded" || transition.leaving === "expanded"} counts={fullCounts} />
          </div>
        )}
        {!narrow && (mode === "rail" || transition.leaving === "rail") && (
          <div key="rail" ref={railLayerRef} data-rail-layer="">
            <Layer name="rail" counts={railCounts} />
          </div>
        )}
      </div>
      <main ref={setMainRef} data-main="" />
    </div>
  );
}

beforeEach(() => {
  reducedMotion = false;
  now = 1000;
  nextRafId = 1;
  frames = new Map();
  vi.spyOn(performance, "now").mockImplementation(() => now);
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query === "(prefers-reduced-motion: reduce)" && reducedMotion,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    const id = nextRafId++;
    frames.set(id, cb);
    return id;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    frames.delete(id);
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function px(value: string): number {
  expect(value).toMatch(/px$/);
  return parseFloat(value);
}

describe("useSidebarTransition", () => {
  const shell = () => host.querySelector<HTMLElement>("[data-app-shell]")!;
  const col = () => host.querySelector<HTMLElement>("[data-sidebar-column]")!;
  const fullLayer = () => host.querySelector<HTMLElement>("[data-full-layer]");
  const railLayer = () => host.querySelector<HTMLElement>("[data-rail-layer]");
  const mainEl = () => host.querySelector<HTMLElement>("[data-main]")!;

  it("keeps final-frame inline styles intact through rAF completion until React unmounts leaving layer (expanded -> rail -> expanded -> hidden -> rail)", () => {
    const observed: { animating: boolean; leaving: "expanded" | "rail" | null }[] = [];
    const renderMode = (mode: SidebarMode) =>
      act(() => root.render(<Harness mode={mode} onState={(s) => observed.push(s)} />));

    renderMode("expanded");
    expect(observed.at(-1)).toEqual({ animating: false, leaving: null });
    expect(shell().dataset.sidebarAnimating).toBeUndefined();
    expect(col().style.width).toBe("");

    // Collapse: expanded -> rail
    renderMode("rail");
    expect(observed.at(-1)).toEqual({ animating: true, leaving: "expanded" });
    expect(shell().dataset.sidebarAnimating).toBe("1");
    expect(px(col().style.width)).toBe(SIDEBAR_DEFAULT);
    expect(col().style.overflow).toBe("hidden");

    advanceFrame(SIDEBAR_COLLAPSE_MS / 2);
    const midWidth = px(col().style.width);
    expect(midWidth).toBeGreaterThan(SIDEBAR_RAIL_WIDTH);
    expect(midWidth).toBeLessThan(SIDEBAR_DEFAULT);

    // Final rAF tick: styles MUST remain intact before React commits leaving=null.
    advanceFrame(SIDEBAR_COLLAPSE_MS, () => {
      expect(fullLayer()).not.toBeNull();
      expect(fullLayer()!.style.opacity).toBe("0");
      expect(px(col().style.width)).toBe(SIDEBAR_RAIL_WIDTH);
      expect(col().style.overflow).toBe("hidden");
      expect(shell().dataset.sidebarAnimating).toBe("1");
    });
    expect(observed.at(-1)).toEqual({ animating: false, leaving: null });
    expect(fullLayer()).toBeNull();
    expect(col().style.width).toBe("");
    expect(col().style.overflow).toBe("");
    expect(shell().dataset.sidebarAnimating).toBeUndefined();
    expect(railLayer()!.style.opacity).toBe("");
    expect(railLayer()!.style.transform).toBe("");

    // Expand: rail -> expanded
    renderMode("expanded");
    expect(observed.at(-1)).toEqual({ animating: true, leaving: "rail" });
    advanceFrame(SIDEBAR_EXPAND_MS, () => {
      expect(railLayer()).not.toBeNull();
      expect(railLayer()!.style.opacity).toBe("0");
      expect(px(col().style.width)).toBe(SIDEBAR_DEFAULT);
      expect(col().style.overflow).toBe("hidden");
      expect(shell().dataset.sidebarAnimating).toBe("1");
    });
    expect(observed.at(-1)).toEqual({ animating: false, leaving: null });
    expect(railLayer()).toBeNull();
    expect(col().style.width).toBe("");
    expect(shell().dataset.sidebarAnimating).toBeUndefined();
    expect(fullLayer()!.style.opacity).toBe("");
    expect(fullLayer()!.style.transform).toBe("");

    // Maximize/hide: expanded -> hidden
    renderMode("hidden");
    expect(observed.at(-1)).toEqual({ animating: true, leaving: "expanded" });
    advanceFrame(SIDEBAR_COLLAPSE_MS, () => {
      expect(fullLayer()).not.toBeNull();
      expect(fullLayer()!.style.opacity).toBe("0");
      expect(px(col().style.width)).toBe(0);
      expect(col().style.overflow).toBe("hidden");
      expect(px(shell().style.paddingLeft)).toBe(PANEL_GAP);
      expect(shell().dataset.sidebarAnimating).toBe("1");
    });
    expect(observed.at(-1)).toEqual({ animating: false, leaving: null });
    expect(fullLayer()).toBeNull();
    expect(col().style.width).toBe("");
    expect(col().style.overflow).toBe("");
    expect(shell().style.paddingLeft).toBe("");
    expect(shell().dataset.sidebarAnimating).toBeUndefined();

    // Unmaximize to rail: hidden -> rail
    renderMode("rail");
    expect(observed.at(-1)).toEqual({ animating: true, leaving: null });
    advanceFrame(SIDEBAR_EXPAND_MS, () => {
      expect(railLayer()).not.toBeNull();
      expect(railLayer()!.style.opacity).toBe("1");
      expect(px(col().style.width)).toBe(SIDEBAR_RAIL_WIDTH);
      expect(px(shell().style.paddingLeft)).toBe(0);
    });
    expect(observed.at(-1)).toEqual({ animating: false, leaving: null });
    expect(col().style.width).toBe("");
    expect(shell().style.paddingLeft).toBe("");
    expect(railLayer()!.style.opacity).toBe("");
  });

  it("never unmounts or remounts the outgoing layer on Commit 1 and preserves active content and scroll position", () => {
    const fullCounts = { mounts: 0, unmounts: 0 };
    const railCounts = { mounts: 0, unmounts: 0 };
    act(() => root.render(<Harness mode="expanded" fullCounts={fullCounts} railCounts={railCounts} />));
    expect(fullCounts).toEqual({ mounts: 1, unmounts: 0 });
    fullLayer()!.scrollTop = 120;

    act(() => root.render(<Harness mode="rail" fullCounts={fullCounts} railCounts={railCounts} />));
    expect(fullCounts).toEqual({ mounts: 1, unmounts: 0 });
    expect(fullLayer()!.scrollTop).toBe(120);
    expect(host.querySelector('[data-layer-child="full"]')?.getAttribute("data-active")).toBe("1");
    expect(host.querySelector('[data-layer-content="full"]')).not.toBeNull();
    expect(railCounts).toEqual({ mounts: 1, unmounts: 0 });

    advanceFrame(SIDEBAR_COLLAPSE_MS);
    expect(fullCounts).toEqual({ mounts: 1, unmounts: 1 });
    expect(railCounts).toEqual({ mounts: 1, unmounts: 0 });

    // Expanding back keeps rail mounted without remounting until animation finishes.
    act(() => root.render(<Harness mode="expanded" fullCounts={fullCounts} railCounts={railCounts} />));
    expect(railCounts).toEqual({ mounts: 1, unmounts: 0 });
    expect(fullCounts).toEqual({ mounts: 2, unmounts: 1 });
    advanceFrame(SIDEBAR_EXPAND_MS);
    expect(railCounts).toEqual({ mounts: 1, unmounts: 1 });
  });

  it("reverses smoothly from current mid-flight width without unmounting layers", () => {
    const fullCounts = { mounts: 0, unmounts: 0 };
    const railCounts = { mounts: 0, unmounts: 0 };
    act(() => root.render(<Harness mode="expanded" customSidebarW={320} fullCounts={fullCounts} railCounts={railCounts} />));
    act(() => root.render(<Harness mode="rail" customSidebarW={320} fullCounts={fullCounts} railCounts={railCounts} />));

    advanceFrame(SIDEBAR_COLLAPSE_MS / 2);
    const liveW = px(col().style.width);
    expect(liveW).toBeGreaterThan(SIDEBAR_RAIL_WIDTH);
    expect(liveW).toBeLessThan(320);

    // Reverse mid-flight back to expanded.
    act(() => root.render(<Harness mode="expanded" customSidebarW={320} fullCounts={fullCounts} railCounts={railCounts} />));
    expect(px(col().style.width)).toBeCloseTo(liveW, 2);
    expect(fullCounts).toEqual({ mounts: 1, unmounts: 0 });
    expect(railCounts).toEqual({ mounts: 1, unmounts: 0 });

    advanceFrame(SIDEBAR_EXPAND_MS, () => {
      expect(px(col().style.width)).toBe(320);
      expect(railLayer()!.style.opacity).toBe("0");
    });
    expect(railCounts).toEqual({ mounts: 1, unmounts: 1 });
    expect(fullCounts).toEqual({ mounts: 1, unmounts: 0 });
    expect(col().style.width).toBe("");
  });

  it("cancels animation and clears inline styles when switching to narrow mid-flight", () => {
    const observed: { animating: boolean; leaving: "expanded" | "rail" | null }[] = [];
    act(() => root.render(<Harness mode="expanded" onState={(s) => observed.push(s)} />));
    act(() => root.render(<Harness mode="rail" onState={(s) => observed.push(s)} />));
    expect(frames.size).toBe(1);
    expect(shell().dataset.sidebarAnimating).toBe("1");

    act(() => root.render(<Harness mode="rail" narrow onState={(s) => observed.push(s)} />));
    expect(observed.at(-1)).toEqual({ animating: false, leaving: null });
    expect(frames.size).toBe(0);
    expect(shell().dataset.sidebarAnimating).toBeUndefined();
    expect(col().style.width).toBe("");
    expect(col().style.overflow).toBe("");
  });

  it("skips animation cleanly under reduced motion and zero-width headless layout", () => {
    const observed: { animating: boolean; leaving: "expanded" | "rail" | null }[] = [];

    reducedMotion = true;
    act(() => root.render(<Harness mode="expanded" onState={(s) => observed.push(s)} />));
    act(() => root.render(<Harness mode="rail" onState={(s) => observed.push(s)} />));
    expect(observed.at(-1)).toEqual({ animating: false, leaving: null });
    expect(frames.size).toBe(0);
    expect(shell().dataset.sidebarAnimating).toBeUndefined();

    reducedMotion = false;
    act(() => root.render(<Harness mode="expanded" colWidth={0} onState={(s) => observed.push(s)} />));
    act(() => root.render(<Harness mode="rail" colWidth={0} onState={(s) => observed.push(s)} />));
    expect(observed.at(-1)).toEqual({ animating: false, leaving: null });
    expect(frames.size).toBe(0);
    expect(fullLayer()).toBeNull();
    expect(shell().dataset.sidebarAnimating).toBeUndefined();
  });

  it("drives macWindowed contour and preserves --mac-header-inset until commit", () => {
    const contourDraw = vi.fn();
    act(() => root.render(<Harness mode="expanded" macWindowed contourDraw={contourDraw} />));
    act(() => root.render(<Harness mode="rail" macWindowed contourDraw={contourDraw} />));
    expect(contourDraw).toHaveBeenCalledWith(0);
    expect(mainEl().style.getPropertyValue("--mac-header-inset")).toBe("16px");

    advanceFrame(SIDEBAR_COLLAPSE_MS, () => {
      expect(contourDraw).toHaveBeenLastCalledWith(112);
      expect(mainEl().style.getPropertyValue("--mac-header-inset")).toBe("146px");
    });
    expect(mainEl().style.getPropertyValue("--mac-header-inset")).toBe("");

    act(() => root.render(<Harness mode="expanded" macWindowed contourDraw={contourDraw} />));
    expect(contourDraw).toHaveBeenLastCalledWith(112);
    advanceFrame(SIDEBAR_EXPAND_MS, () => {
      expect(contourDraw).toHaveBeenLastCalledWith(0);
      expect(mainEl().style.getPropertyValue("--mac-header-inset")).toBe("16px");
    });
    expect(mainEl().style.getPropertyValue("--mac-header-inset")).toBe("");
  });

  it("preserves single-transition animation cleanly under React StrictMode", () => {
    const fullCounts = { mounts: 0, unmounts: 0 };
    const railCounts = { mounts: 0, unmounts: 0 };
    act(() => root.render(<StrictMode><Harness mode="expanded" fullCounts={fullCounts} railCounts={railCounts} /></StrictMode>));
    expect(fullCounts).toEqual({ mounts: 2, unmounts: 1 });

    act(() => root.render(<StrictMode><Harness mode="rail" fullCounts={fullCounts} railCounts={railCounts} /></StrictMode>));
    expect(frames.size).toBe(1);
    expect(shell().dataset.sidebarAnimating).toBe("1");
    expect(fullLayer()).not.toBeNull();
    expect(railLayer()).not.toBeNull();
    expect(fullCounts).toEqual({ mounts: 2, unmounts: 1 });
    expect(railCounts).toEqual({ mounts: 2, unmounts: 1 });

    advanceFrame(SIDEBAR_COLLAPSE_MS, () => {
      expect(fullLayer()!.style.opacity).toBe("0");
      expect(px(col().style.width)).toBe(SIDEBAR_RAIL_WIDTH);
    });
    expect(fullLayer()).toBeNull();
    expect(fullCounts).toEqual({ mounts: 2, unmounts: 2 });
    expect(railCounts).toEqual({ mounts: 2, unmounts: 1 });
    expect(shell().dataset.sidebarAnimating).toBeUndefined();
    expect(col().style.width).toBe("");
  });

  it("handles mid-flight transition from expanded -> rail -> hidden without width snap or style leaks", () => {
    act(() => root.render(<Harness mode="expanded" />));
    act(() => root.render(<Harness mode="rail" />));
    advanceFrame(SIDEBAR_COLLAPSE_MS / 2);
    const midW = px(col().style.width);
    expect(midW).toBeGreaterThan(SIDEBAR_RAIL_WIDTH);
    expect(midW).toBeLessThan(SIDEBAR_DEFAULT);

    // Interrupt collapse-to-rail with maximize (hidden) while rail is already leaving=rail.
    act(() => root.render(<Harness mode="hidden" />));
    expect(px(col().style.width)).toBeCloseTo(midW, 2);
    expect(fullLayer()).toBeNull();
    expect(railLayer()).not.toBeNull();

    advanceFrame(SIDEBAR_COLLAPSE_MS, () => {
      expect(railLayer()).not.toBeNull();
      expect(railLayer()!.style.opacity).toBe("0");
      expect(px(col().style.width)).toBe(0);
      expect(px(shell().style.paddingLeft)).toBe(PANEL_GAP);
    });
    expect(railLayer()).toBeNull();
    expect(col().style.width).toBe("");
    expect(shell().style.paddingLeft).toBe("");
    expect(shell().dataset.sidebarAnimating).toBeUndefined();
  });

  it("preserves CollapseButton keyboard focus handoff on mid-flight reversals without leaking focusHandoff", () => {
    setSidebarCollapsed(false);
    function FocusShell() {
      const { sidebarCollapsed } = useLayout();
      const mode: SidebarMode = sidebarCollapsed ? "rail" : "expanded";
      const shellRef = useRef<HTMLDivElement>(null);
      const columnRef = useRef<HTMLDivElement>(null);
      const fullLayerRef = useRef<HTMLDivElement>(null);
      const railLayerRef = useRef<HTMLDivElement>(null);
      const contourDriverRef = useRef<MacContourDriver | null>(null);
      const [mainEl, setMainEl] = useState<HTMLElement | null>(null);
      const setCol = useCallback((el: HTMLDivElement | null) => {
        columnRef.current = el;
        if (el) el.getBoundingClientRect = () => rect(SIDEBAR_DEFAULT);
      }, []);
      const setMain = useCallback((el: HTMLElement | null) => {
        if (el) el.getBoundingClientRect = () => rect(960);
        setMainEl(el);
      }, []);
      const { leaving } = useSidebarTransition({
        shellRef,
        columnRef,
        mainEl,
        fullLayerRef,
        railLayerRef,
        contourDriverRef,
        mode,
        narrow: false,
        macWindowed: false,
      });
      return (
        <div ref={shellRef} data-app-shell="">
          <div ref={setCol} data-sidebar-column="">
            {(mode === "expanded" || leaving === "expanded") && (
              <div key="full" ref={fullLayerRef} inert={leaving === "expanded" || undefined} data-full-layer="">
                <CollapseButton collapsed={false} />
              </div>
            )}
            {(mode === "rail" || leaving === "rail") && (
              <div key="rail" ref={railLayerRef} inert={leaving === "rail" || undefined} data-rail-layer="">
                <CollapseButton collapsed={true} />
              </div>
            )}
          </div>
          <main ref={setMain} data-main="" />
        </div>
      );
    }

    act(() => root.render(<FocusShell />));
    const fullBtn = () => fullLayer()!.querySelector<HTMLButtonElement>("button")!;
    const railBtn = () => railLayer()!.querySelector<HTMLButtonElement>("button")!;
    fullBtn().focus();
    expect(document.activeElement).toBe(fullBtn());

    // Start collapse via focused button: focus moves to rail button.
    act(() => fullBtn().click());
    expect(document.activeElement).toBe(railBtn());
    expect(takeFocusHandoff()).toBe(false);

    // Reverse mid-flight while fullLayer remained mounted: focus moves back to full button without leak.
    advanceFrame(SIDEBAR_COLLAPSE_MS / 2);
    act(() => railBtn().click());
    expect(document.activeElement).toBe(fullBtn());
    expect(takeFocusHandoff()).toBe(false);

    // Reverse mid-flight again via shortcut while focus is inside full column: moves to rail button cleanly.
    act(() => toggleSidebarColumn(true));
    expect(document.activeElement).toBe(railBtn());
    expect(takeFocusHandoff()).toBe(false);

    advanceFrame(SIDEBAR_COLLAPSE_MS);
    expect(fullLayer()).toBeNull();
    act(() => setSidebarCollapsed(false));
  });
});
