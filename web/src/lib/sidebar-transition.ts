// Three-track wide-screen sidebar transition: width + cross-morph layers + synced Apple G2 bay contour.
import { useLayoutEffect, useRef, useState, type RefObject } from "react";
import { bezier } from "../ui/motion";
import { prefersReducedMotion } from "../ui/stream/motion";
import { PANEL_GAP, SIDEBAR_DEFAULT } from "./layout";
import { macHeaderInset, sidebarBayCutout } from "./mac-chrome";
import type { MacContourDriver } from "../views/MacChrome";

export type SidebarMode = "expanded" | "rail" | "hidden";
export const SIDEBAR_RAIL_WIDTH = 56;
export const SIDEBAR_EXPAND_MS = 280;
export const SIDEBAR_COLLAPSE_MS = 240;
const SIDEBAR_EASE = bezier("cubic-bezier(0.22, 1, 0.36, 1)");
const clamp01 = (v: number) => (v <= 0 ? 0 : v >= 1 ? 1 : v);

function expandedWidth(column: HTMLElement): number {
  const v = parseFloat(column.style.getPropertyValue("--sidebar-w"));
  return Number.isFinite(v) && v > 0 ? v : SIDEBAR_DEFAULT;
}

const targetWidth = (mode: SidebarMode, column: HTMLElement) =>
  mode === "rail" ? SIDEBAR_RAIL_WIDTH : mode === "hidden" ? 0 : expandedWidth(column);

function clearStyles(
  shell: HTMLElement | null,
  column: HTMLElement | null,
  mainEl: HTMLElement | null,
  fullLayer: HTMLElement | null,
  railLayer: HTMLElement | null,
) {
  if (shell) {
    delete shell.dataset.sidebarAnimating;
    shell.style.paddingLeft = "";
  }
  if (column) {
    column.style.width = "";
    column.style.overflow = "";
  }
  if (mainEl) mainEl.style.removeProperty("--mac-header-inset");
  for (const el of [fullLayer, railLayer]) {
    if (!el) continue;
    el.style.opacity = "";
    el.style.transform = "";
  }
}

function paintFrame(
  p: number,
  fromMode: SidebarMode,
  toMode: SidebarMode,
  fromW: number,
  toW: number,
  macWindowed: boolean,
  shell: HTMLElement,
  column: HTMLElement,
  mainEl: HTMLElement | null,
  fullLayer: HTMLElement | null,
  railLayer: HTMLElement | null,
  contourDriver: MacContourDriver | null,
): number {
  const e = SIDEBAR_EASE(p);
  const w = fromW + (toW - fromW) * e;
  column.style.width = `${w.toFixed(2)}px`;
  column.style.overflow = "hidden";
  if (fromMode === "hidden" || toMode === "hidden") {
    const gap = PANEL_GAP * (1 - clamp01(w / SIDEBAR_RAIL_WIDTH));
    shell.style.paddingLeft = `${gap.toFixed(2)}px`;
  } else {
    shell.style.removeProperty("padding-left");
  }

  const fx = (el: HTMLElement | null, o: number, x = 0, s = 1) => {
    if (!el) return;
    el.style.opacity = String(o);
    el.style.transform = s === 1 ? `translateX(${x.toFixed(2)}px)` : `translateX(${x.toFixed(2)}px) scale(${s.toFixed(4)})`;
  };

  // Track 2: cross-morph outgoing & incoming layers without text reflow.
  if (fromMode === "expanded" && toMode === "rail") {
    const k = clamp01(p / 0.45);
    const rk = clamp01((p - 0.25) / 0.7);
    fx(fullLayer, 1 - k, -10 * k);
    fx(railLayer, rk, -6 * (1 - rk), 0.96 + 0.04 * rk);
  } else if (fromMode === "rail" && toMode === "expanded") {
    const k = clamp01(p / 0.35);
    const fk = SIDEBAR_EASE(clamp01((p - 0.18) / 0.82));
    fx(railLayer, 1 - k, 0, 1 - 0.04 * k);
    fx(fullLayer, fk, -12 * (1 - fk));
  } else if (toMode === "hidden") {
    const k = clamp01(p / 0.65);
    fx(fromMode === "expanded" ? fullLayer : railLayer, 1 - k, -10 * e);
  } else if (fromMode === "hidden") {
    const k = SIDEBAR_EASE(clamp01((p - 0.12) / 0.88));
    fx(toMode === "expanded" ? fullLayer : railLayer, k, -12 * (1 - k));
  }

  // Track 3: dynamic macOS Apple G2 bay cutout & header clearance.
  if (macWindowed) {
    const bx = sidebarBayCutout(w);
    contourDriver?.draw(bx);
    mainEl?.style.setProperty("--mac-header-inset", `${macHeaderInset(bx)}px`);
  }
  return w;
}

type TransitionState = {
  active: boolean;
  from: SidebarMode;
  to: SidebarMode;
  leaving: "expanded" | "rail" | null;
  run: number;
};

export function useSidebarTransition(opts: {
  shellRef: RefObject<HTMLDivElement | null>;
  columnRef: RefObject<HTMLDivElement | null>;
  mainEl: HTMLElement | null;
  fullLayerRef: RefObject<HTMLDivElement | null>;
  railLayerRef: RefObject<HTMLDivElement | null>;
  contourDriverRef: RefObject<MacContourDriver | null>;
  mode: SidebarMode;
  narrow: boolean;
  macWindowed: boolean;
}): { animating: boolean; leaving: "expanded" | "rail" | null } {
  const { shellRef, columnRef, mainEl, fullLayerRef, railLayerRef, contourDriverRef, mode, narrow, macWindowed } = opts;
  const [state, setState] = useState<TransitionState>({
    active: false,
    from: mode,
    to: mode,
    leaving: null,
    run: 0,
  });
  // Keep latest chrome/main references without restarting an active transition on non-mode updates.
  const envRef = useRef({ mainEl, macWindowed });
  const stylesDirty = useRef(false);
  const activeRun = useRef(false);
  const liveW = useRef(0);
  const raf = useRef(0);

  // Derive transition state during render so the outgoing layer stays mounted without a 1-frame unmount/remount.
  const canAnimate = !narrow && typeof requestAnimationFrame === "function" && !prefersReducedMotion();
  if (!canAnimate) {
    if (state.active || state.to !== mode || state.from !== mode || state.leaving !== null) {
      setState({ active: false, from: mode, to: mode, leaving: null, run: state.run });
    }
  } else if (state.to !== mode) {
    const from = state.to;
    setState({
      active: true,
      from,
      to: mode,
      leaving: from === "hidden" ? null : from,
      run: state.run + 1,
    });
  }

  useLayoutEffect(() => {
    envRef.current = { mainEl, macWindowed };
  });

  useLayoutEffect(() => {
    const shell = shellRef.current;
    const column = columnRef.current;
    const { mainEl: curMain } = envRef.current;
    const clearIfDirty = () => {
      if (!stylesDirty.current) return;
      clearStyles(shell, column, curMain, fullLayerRef.current, railLayerRef.current);
      stylesDirty.current = false;
    };

    if (!state.active) {
      cancelAnimationFrame(raf.current);
      activeRun.current = false;
      clearIfDirty();
      return;
    }

    const layoutReady = Boolean(shell && column) && (
      activeRun.current
        ? (curMain?.getBoundingClientRect().width ?? 0) > 0 || column!.getBoundingClientRect().width > 0
        : state.from === "hidden"
          ? (curMain?.getBoundingClientRect().width ?? 0) > 0
          : column!.getBoundingClientRect().width > 0
    );
    if (!shell || !column || !layoutReady) {
      cancelAnimationFrame(raf.current);
      activeRun.current = false;
      clearIfDirty();
      setState((cur) => (cur.active ? { active: false, from: cur.to, to: cur.to, leaving: null, run: cur.run } : cur));
      return;
    }

    cancelAnimationFrame(raf.current);
    const fromW = activeRun.current ? liveW.current : targetWidth(state.from, column);
    const toW = targetWidth(state.to, column);
    liveW.current = fromW;
    activeRun.current = true;
    stylesDirty.current = true;
    shell.dataset.sidebarAnimating = "1";

    const dur = state.to === "expanded" || (state.from === "hidden" && state.to === "rail")
      ? SIDEBAR_EXPAND_MS
      : SIDEBAR_COLLAPSE_MS;
    const drawAt = (p: number) => {
      const { mainEl: liveMain, macWindowed: liveMacWindowed } = envRef.current;
      liveW.current = paintFrame(
        p,
        state.from,
        state.to,
        fromW,
        toW,
        liveMacWindowed,
        shell,
        column,
        liveMain,
        fullLayerRef.current,
        railLayerRef.current,
        contourDriverRef.current,
      );
    };
    drawAt(0);
    const start = performance.now();
    const tick = (now: number) => {
      const p = clamp01((now - start) / dur);
      drawAt(p);
      if (p < 1) {
        raf.current = requestAnimationFrame(tick);
      } else {
        // Defer clearStyles until React commits active=false and unmounts the leaving layer.
        activeRun.current = false;
        setState((cur) => (cur.active && cur.run === state.run
          ? { active: false, from: cur.to, to: cur.to, leaving: null, run: cur.run }
          : cur));
      }
    };
    raf.current = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf.current);
  }, [state]);

  return { animating: state.active, leaving: state.leaving };
}
