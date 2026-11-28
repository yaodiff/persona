import { useImperativeHandle, useLayoutEffect, useRef, useState, type Ref } from "react";
import { t } from "../i18n";
import { setSidebarCollapsed, toggleMaximized } from "../lib/layout";
import { appleG2Contour, appleG2ContourAt } from "../lib/mac-chrome";
import { jumpHint, useBindings } from "../lib/shortcuts";
import { cx } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { CollapseButton, SIDEBAR_CONTROL } from "./SidebarRail";

/** Controls parked inside the 44 px Apple G2 bay beside the macOS traffic lights across expanded, rail, and hidden sidebar states. */
export function MacBayTools({ bay, onSearch }: { bay: "expanded" | "rail" | "hidden"; onSearch: () => void }) {
  const bindings = useBindings();
  const collapsed = bay !== "expanded";
  return (
    <div
      data-mac-bay-tools
      className="pointer-events-auto absolute z-50 flex h-7 items-center gap-1 max-lg:hidden"
      style={{ left: 86, top: 11.5 }}
    >
      <span aria-hidden className="mr-0.5 h-4.5 w-px rounded-full bg-line-strong" />
      <CollapseButton
        collapsed={collapsed}
        chord={bindings["left-sidebar"]}
        size="sm"
        onToggle={bay === "hidden" ? () => {
          setSidebarCollapsed(false);
          toggleMaximized();
        } : undefined}
      />
      <button
        type="button"
        onClick={onSearch}
        aria-label={t("sidebar.search_sessions")}
        title={`${t("sidebar.search_sessions")} (${jumpHint(bindings)})`}
        className={cx(SIDEBAR_CONTROL, "size-7")}
      >
        <Icon name="search" size={15} stroke={1.8} />
      </button>
    </div>
  );
}

function applyMainClip(el: HTMLElement, value: string): void {
  el.style.clipPath = value;
  el.style.setProperty("-webkit-clip-path", value);
}

export interface MacContourDriver {
  draw: (bx: number) => void;
}

/** Clips `<main>` with the 44 px Apple G2 continuous-curvature path and strokes its crisp 1 px border + top specular rim. */
export function MacMainContour({ mainEl, bay, animating = false, contourRef }: { mainEl: HTMLElement | null; bay: "expanded" | "rail" | "hidden" | null | undefined; animating?: boolean; contourRef?: Ref<MacContourDriver> }) {
  const [size, setSize] = useState<{ w: number; h: number }>({ w: 0, h: 0 });
  const svg = useRef<SVGSVGElement>(null);
  const strokePath = useRef<SVGPathElement>(null);
  const rimPath = useRef<SVGPathElement>(null);
  const liveBx = useRef<number | null>(null);
  const cutout = bay && bay !== "expanded" ? bay : null;

  useImperativeHandle(contourRef, () => ({
    draw(bx: number) {
      liveBx.current = bx;
      if (!mainEl) return;
      const w = Math.round(mainEl.clientWidth);
      const h = Math.round(mainEl.clientHeight);
      if (w < 100 || h < 100) return;
      const { clip, stroke, rim } = appleG2ContourAt(w, h, bx);
      applyMainClip(mainEl, `path("${clip}")`);
      if (svg.current) svg.current.setAttribute("viewBox", `0 0 ${w} ${h}`);
      if (strokePath.current) strokePath.current.setAttribute("d", stroke);
      if (rimPath.current) rimPath.current.setAttribute("d", rim);
    },
  }), [mainEl]);

  useLayoutEffect(() => {
    if (!mainEl) return;
    if (animating) {
      const w = Math.round(mainEl.clientWidth);
      const h = Math.round(mainEl.clientHeight);
      if (w >= 100 && h >= 100) setSize((prev) => (prev.w === w && prev.h === h ? prev : { w, h }));
      return;
    }
    liveBx.current = null;
    if (!cutout) {
      applyMainClip(mainEl, "");
      return;
    }
    const measure = () => {
      const w = Math.round(mainEl.clientWidth);
      const h = Math.round(mainEl.clientHeight);
      if (w >= 100 && h >= 100) {
        const { clip } = appleG2Contour(w, h, cutout);
        applyMainClip(mainEl, `path("${clip}")`);
        setSize((prev) => (prev.w === w && prev.h === h ? prev : { w, h }));
      }
    };
    measure();
    if (typeof ResizeObserver === "undefined") return () => applyMainClip(mainEl, "");
    const ro = new ResizeObserver(measure);
    ro.observe(mainEl);
    return () => {
      ro.disconnect();
      applyMainClip(mainEl, "");
    };
  }, [mainEl, cutout, animating]);

  const w = mainEl && animating ? Math.round(mainEl.clientWidth) : size.w;
  const h = mainEl && animating ? Math.round(mainEl.clientHeight) : size.h;
  if ((!cutout && !animating) || w < 100 || h < 100) return null;
  const { stroke, rim } = animating && liveBx.current !== null
    ? appleG2ContourAt(w, h, liveBx.current)
    : appleG2Contour(w, h, cutout);
  return (
    <svg
      ref={svg}
      aria-hidden
      data-mac-contour
      viewBox={`0 0 ${w} ${h}`}
      className="pointer-events-none absolute inset-0 z-30 h-full w-full max-lg:hidden"
    >
      <path ref={strokePath} d={stroke} fill="none" stroke="var(--line)" strokeWidth={1} />
      <path ref={rimPath} d={rim} fill="none" stroke="var(--mac-rim-top, transparent)" strokeWidth={1} />
    </svg>
  );
}
