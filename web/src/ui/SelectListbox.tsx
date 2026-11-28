// A positioned listbox overlay built on the design system's Menu surface, gliding highlight, fade edges, and tokens
// (docs/design/DESIGN_SYSTEM.md, "Preview native <select> overlay"). Used by PreviewPanel for bridged native <select>
// popups so it stays in the lazy preview chunk without growing the first screen.
import { Fragment, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import {
  edgeSelectOption, initialSelectActive, placeSelectPopup, SELECT_POPUP_MAX_HEIGHT, stepSelectOption,
  type PreviewSelectState, type SelectPopupPlacement,
} from "../lib/preview";
import { composing } from "../lib/send-history";
import { EASE, cx } from "./atoms";
import { Icon } from "./icons";
import { prefersReducedMotion } from "./stream/motion";

const FADE_HEIGHT = 24;
const ROW_ESTIMATE = 32;
const GROUP_ESTIMATE = 26;

function revealOption(box: HTMLElement, row: HTMLElement) {
  if (box.scrollHeight <= box.clientHeight) return;
  const top = row.offsetTop;
  const bottom = top + row.offsetHeight;
  if (top < box.scrollTop + FADE_HEIGHT) box.scrollTop = Math.max(0, top - FADE_HEIGHT);
  else if (bottom > box.scrollTop + box.clientHeight - FADE_HEIGHT) {
    box.scrollTop = Math.min(box.scrollHeight - box.clientHeight, bottom - box.clientHeight + FADE_HEIGHT);
  }
}

function estimateHeight(select: PreviewSelectState): number {
  let groups = 0;
  for (let i = 0; i < select.options.length; i++) {
    const g = select.options[i].group;
    if (g && (i === 0 || select.options[i - 1].group !== g)) groups++;
  }
  return Math.min(SELECT_POPUP_MAX_HEIGHT, Math.max(ROW_ESTIMATE, select.options.length * ROW_ESTIMATE + groups * GROUP_ESTIMATE) + 8);
}

export function SelectListbox({
  select,
  pageSize,
  fit,
  area,
  boxRef,
  canvasRef,
  keysRef,
  popupRef,
  label,
  emptyLabel,
  onChoose,
  onDismiss,
  onSwallowKeyUp,
}: {
  select: PreviewSelectState;
  pageSize: { w: number; h: number };
  fit: { w: number; h: number } | null;
  area: { w: number; h: number } | null;
  boxRef: RefObject<HTMLDivElement | null>;
  canvasRef: RefObject<HTMLCanvasElement | null>;
  keysRef: RefObject<HTMLTextAreaElement | null>;
  popupRef: RefObject<HTMLDivElement | null>;
  label: string;
  emptyLabel: string;
  onChoose: (index: number) => void;
  onDismiss: () => void;
  onSwallowKeyUp: (key: string) => void;
}) {
  const baseId = useId();
  const rows = useRef<(HTMLButtonElement | null)[]>([]);
  const keyed = useRef(false);
  const reduced = prefersReducedMotion();
  const [active, setActive] = useState(() => initialSelectActive(select.options, select.selected));
  const [highlight, setHighlight] = useState<{ top: number; height: number } | null>(null);
  const [more, setMore] = useState({ up: false, down: false });

  // Reset active highlight when a new select_open replaces the current one.
  const [openId, setOpenId] = useState(select.id);
  if (openId !== select.id) {
    setOpenId(select.id);
    setActive(initialSelectActive(select.options, select.selected));
  }

  const fallbackPlacement = useMemo<SelectPopupPlacement>(() => {
    const container = { w: area?.w ?? pageSize.w, h: area?.h ?? pageSize.h };
    const canvasBox = { left: 0, top: 0, w: fit?.w ?? container.w, h: fit?.h ?? container.h };
    return placeSelectPopup(select.rect, pageSize, canvasBox, container, estimateHeight(select));
  }, [select, pageSize, fit, area]);

  const [place, setPlace] = useState<SelectPopupPlacement>(fallbackPlacement);

  const measureMore = () => {
    const el = popupRef.current;
    if (!el) return;
    const up = el.scrollTop > 0;
    const down = el.scrollTop + el.clientHeight < el.scrollHeight - 1;
    setMore((m) => (m.up === up && m.down === down ? m : { up, down }));
  };

  // Place relative to the canvas inside the preview container, then scroll to the currently selected option on open.
  useLayoutEffect(() => {
    const el = popupRef.current;
    const containerEl = boxRef.current;
    const canvasEl = canvasRef.current;
    if (!el) return;
    const cr = containerEl?.getBoundingClientRect();
    const vr = canvasEl?.getBoundingClientRect();
    const containerW = cr && cr.width > 0 ? cr.width : (area?.w ?? pageSize.w);
    const containerH = cr && cr.height > 0 ? cr.height : (area?.h ?? pageSize.h);
    const canvasW = vr && vr.width > 0 ? vr.width : (fit?.w ?? containerW);
    const canvasH = vr && vr.height > 0 ? vr.height : (fit?.h ?? containerH);
    const canvasLeft = cr && vr && cr.width > 0 && vr.width > 0 ? vr.left - cr.left : 0;
    const canvasTop = cr && vr && cr.height > 0 && vr.height > 0 ? vr.top - cr.top : 0;

    const prevScroll = el.scrollTop;
    const prevMaxH = el.style.maxHeight;
    el.style.maxHeight = "none";
    const naturalH = el.scrollHeight > 0 ? el.scrollHeight : estimateHeight(select);
    const measuredW = el.offsetWidth > 0 ? el.offsetWidth : undefined;
    el.style.maxHeight = prevMaxH;
    el.scrollTop = prevScroll;

    const next = placeSelectPopup(
      select.rect,
      pageSize,
      { left: canvasLeft, top: canvasTop, w: canvasW, h: canvasH },
      { w: containerW, h: containerH },
      naturalH,
      measuredW,
    );
    setPlace(next);
  }, [select, pageSize, fit, area, boxRef, canvasRef, popupRef]);

  // Focus the listbox and scroll the currently selected option into view when opened or replaced.
  useLayoutEffect(() => {
    const el = popupRef.current;
    if (!el) return;
    el.focus({ preventScroll: true });
    const selectedPos = select.selected >= 0 ? select.options.findIndex((o) => o.index === select.selected) : -1;
    const target = selectedPos >= 0 ? rows.current[selectedPos] : null;
    if (target && el.scrollHeight > el.clientHeight) {
      el.scrollTop = Math.max(0, target.offsetTop - Math.max(0, (el.clientHeight - target.offsetHeight) / 2));
    }
    measureMore();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only on a newly opened select
  }, [select.id, place.maxHeight]);

  // Move the gliding highlight and reveal the keyboard-active row.
  useLayoutEffect(() => {
    const row = active >= 0 ? rows.current[active] : null;
    setHighlight(row ? { top: row.offsetTop, height: row.offsetHeight } : null);
    if (row && popupRef.current && keyed.current) {
      revealOption(popupRef.current, row);
      measureMore();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `keyed` and `popupRef` are refs
  }, [active, select.id]);

  // Keyboard handling while open: capture before PreviewPanel's hidden textarea or SidePanel Esc can see keys.
  const live = useRef({ select, active, onChoose, onDismiss, onSwallowKeyUp });
  live.current = { select, active, onChoose, onDismiss, onSwallowKeyUp };
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (composing(e)) return;
      const target = e.target;
      const popup = popupRef.current;
      const owns =
        !target ||
        target === window ||
        target === document ||
        target === document.body ||
        target === document.documentElement ||
        (target instanceof Node && popup !== null && popup.contains(target)) ||
        target === keysRef.current ||
        target === canvasRef.current;
      if (!owns) return;
      const { select: cur, active: curActive, onChoose: choose, onDismiss: dismiss, onSwallowKeyUp: swallow } = live.current;
      switch (e.key) {
        case "ArrowDown": {
          e.preventDefault();
          e.stopPropagation();
          keyed.current = true;
          const next = stepSelectOption(cur.options, curActive, 1);
          if (next >= 0) setActive(next);
          return;
        }
        case "ArrowUp": {
          e.preventDefault();
          e.stopPropagation();
          keyed.current = true;
          const next = stepSelectOption(cur.options, curActive, -1);
          if (next >= 0) setActive(next);
          return;
        }
        case "Home": {
          e.preventDefault();
          e.stopPropagation();
          keyed.current = true;
          const next = edgeSelectOption(cur.options, false);
          if (next >= 0) setActive(next);
          return;
        }
        case "End": {
          e.preventDefault();
          e.stopPropagation();
          keyed.current = true;
          const next = edgeSelectOption(cur.options, true);
          if (next >= 0) setActive(next);
          return;
        }
        case "Enter":
        case " ": {
          e.preventDefault();
          e.stopPropagation();
          swallow(e.key);
          const opt = curActive >= 0 ? cur.options[curActive] : undefined;
          if (opt && !opt.disabled) choose(opt.index);
          return;
        }
        case "Escape": {
          e.preventDefault();
          e.stopPropagation();
          swallow("Escape");
          dismiss();
          return;
        }
        case "Tab": {
          e.preventDefault();
          e.stopPropagation();
          dismiss();
          return;
        }
        default: {
          // Never forward typing or shortcuts to the remote page while the select list is open.
          if (!e.metaKey && !e.ctrlKey && !e.altKey) {
            e.preventDefault();
            e.stopPropagation();
          }
        }
      }
    };
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, [popupRef, keysRef, canvasRef]);

  const activeId = active >= 0 && select.options[active] ? `${baseId}-opt-${active}` : undefined;

  return (
    <div
      ref={popupRef}
      role="listbox"
      tabIndex={-1}
      aria-label={label}
      aria-activedescendant={activeId}
      data-preview-select=""
      data-placement={place.placement}
      onScroll={measureMore}
      className="absolute z-30 overflow-y-auto overscroll-contain rounded-card bg-surface p-1 shadow-overlay outline-none"
      style={{
        left: `${place.left}px`,
        top: `${place.top}px`,
        minWidth: `${place.minWidth}px`,
        maxWidth: `${place.maxWidth}px`,
        maxHeight: `${place.maxHeight}px`,
        animation: reduced ? undefined : `pop-in 160ms ${EASE} both`,
        transformOrigin: place.placement === "top" ? "bottom left" : "top left",
      }}
    >
      {more.up && <div aria-hidden data-menu-more="up" className="pointer-events-none sticky top-0 z-20 -mb-6 h-6 bg-linear-to-b from-surface to-transparent" />}
      <div className="relative">
        <span
          aria-hidden
          className="pointer-events-none absolute inset-x-0 rounded-chip bg-hover"
          style={{
            top: highlight?.top ?? 0,
            height: highlight?.height ?? 0,
            opacity: highlight ? 1 : 0,
            transition: reduced ? undefined : `top 160ms ${EASE}, height 160ms ${EASE}, opacity 120ms ease`,
          }}
        />
        {select.options.length === 0 ? (
          <div className="relative z-10 flex h-8 items-center px-2 text-label text-ink-3 pointer-coarse:min-h-11">{emptyLabel}</div>
        ) : (
          select.options.map((opt, i) => {
            const prevGroup = i > 0 ? select.options[i - 1].group : undefined;
            const showGroup = Boolean(opt.group) && opt.group !== prevGroup;
            const showUnGroupSep = !opt.group && Boolean(prevGroup);
            const isSelected = opt.index === select.selected;
            const isActive = i === active;
            return (
              <Fragment key={`${opt.index}-${i}`}>
                {showUnGroupSep && <div role="separator" className="mx-1 my-1 h-px bg-line" />}
                {showGroup && (
                  <div
                    role="presentation"
                    data-preview-select-group=""
                    className={cx("relative z-10 truncate px-2 pt-1.5 pb-1 text-submeta font-medium text-ink-3", i > 0 && "mt-1 border-t border-line pt-2")}
                  >
                    {opt.group}
                  </div>
                )}
                <button
                  ref={(el) => {
                    rows.current[i] = el;
                  }}
                  id={`${baseId}-opt-${i}`}
                  type="button"
                  role="option"
                  tabIndex={-1}
                  disabled={opt.disabled}
                  aria-selected={isSelected}
                  aria-disabled={opt.disabled || undefined}
                  data-active={isActive || undefined}
                  onMouseDown={(e) => e.preventDefault()}
                  onPointerEnter={(e) => {
                    if (e.pointerType !== "mouse" || opt.disabled) return;
                    keyed.current = false;
                    setActive(i);
                  }}
                  onClick={() => {
                    if (!opt.disabled) onChoose(opt.index);
                  }}
                  className={cx(
                    "relative z-10 flex min-h-8 w-full items-center gap-2 rounded-chip py-1 text-left text-meta text-ink pointer-coarse:min-h-11 disabled:cursor-not-allowed disabled:opacity-40",
                    opt.group ? "pr-2 pl-4" : "px-2",
                    isSelected && "font-medium",
                  )}
                >
                  <span className="min-w-0 flex-1 truncate">{opt.label}</span>
                  <Icon name="check" size={13} stroke={2.5} className={cx("shrink-0", isSelected ? "text-accent-ink" : "invisible")} />
                </button>
              </Fragment>
            );
          })
        )}
      </div>
      {more.down && <div aria-hidden data-menu-more="down" className="pointer-events-none sticky bottom-0 z-20 -mt-6 h-6 bg-linear-to-t from-surface to-transparent" />}
    </div>
  );
}
