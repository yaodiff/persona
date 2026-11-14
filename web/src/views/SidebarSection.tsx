// A sidebar group: its heading stays in view while its rows scroll, a section folds under it, and its count says what
// it counts. Kept apart from the sidebar's own parts so that a list outside the sidebar can draw the same sections.
import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { t } from "../i18n";
import { stateCount } from "../lib/state-labels";
import { Collapse, cx, sidebarFoldMotion } from "../ui/atoms";
import { contextMenuProps } from "../ui/context-menu";
import { Icon } from "../ui/icons";
import type { MenuAt } from "../ui/menu-model";

/** `pinned`: a nested group tells its parent that its heading is pinned (1) or no longer (-1). */
const GroupHeadingOffset = createContext<{ offset: number; depth: number; pinned?: (by: 1 | -1) => void }>({ offset: 0, depth: 0 });

// Every heading's height comes from one observer, after the browser's own layout: a heading's layout effect that read
// it (getBoundingClientRect) made the browser lay out the whole list there and then, on every list drawn and every
// group opened. flushSync applies the heights before that frame is painted, as the layout effect did.
const headingReports = new Map<Element, (height: number) => void>();
let headingSizes: ResizeObserver | null = null;
function watchHeading(el: Element, report: (height: number) => void): () => void {
  headingSizes ??= new ResizeObserver((entries) => flushSync(() => {
    for (const e of entries) headingReports.get(e.target)?.(e.borderBoxSize?.[0]?.blockSize ?? e.target.getBoundingClientRect().height);
  }));
  headingReports.set(el, report);
  headingSizes.observe(el);
  return () => { headingReports.delete(el); headingSizes?.unobserve(el); };
}

// A heading pinned over its group's rows draws an edge under it (`data-edge`, styles.css), so a row half under it reads
// as scrolled beneath, not as text run into the heading (the web usability audit of 2026-11-14, audit item 51). Two
// marks per group, raised by where its heading sticks, say whether it is pinned: the one at the group's top has gone
// above the list's top edge and the one at its end, raised by the heading's height too, has not (the group's end has
// not started pushing the heading out). One IntersectionObserver per scroller watches them all: no layout read and no
// React state on scroll. Its view reaches far below the list, so a mark is only ever in it or above it: a jump from
// below the list to above it (a long scroll in one frame) is still reported.
const PIN_VIEW = "0px 0px 1000000px 0px";
const pinReports = new Map<Element, (above: boolean) => void>();
const pinWatchers = new WeakMap<Element, IntersectionObserver>();
function watchPin(mark: Element, scroller: Element, report: (above: boolean) => void): () => void {
  let watcher = pinWatchers.get(scroller);
  if (!watcher) {
    watcher = new IntersectionObserver((entries) => {
      for (const e of entries) pinReports.get(e.target)?.(!e.isIntersecting && !!e.rootBounds && e.boundingClientRect.top < e.rootBounds.top);
    }, { root: scroller, rootMargin: PIN_VIEW });
    pinWatchers.set(scroller, watcher);
  }
  pinReports.set(mark, report);
  watcher.observe(mark);
  const w = watcher;
  return () => { pinReports.delete(mark); w.unobserve(mark); };
}

/** A 1 px mark `above` px plus the pinned Persona row above its place: at the group's top it leaves the list's view as
 *  the heading starts to stick; at the group's end (`above` with the heading's own height) as the end starts pushing
 *  the heading out under the ones above. */
function PinMark({ mark, above }: { mark: React.RefObject<HTMLSpanElement | null>; above: number }) {
  return <span aria-hidden className="relative block h-0"><span ref={mark} className="pointer-events-none absolute left-0 h-px w-px" style={{ top: `calc(-1 * (var(--sidebar-sticky-top, 0px) + ${above}px))` }} /></span>;
}

/** Real headings stay interactive while pinned; each nested group reserves its parent's measured height. */
export function SidebarGroup({ heading, children, className }: { heading: React.ReactNode; children: React.ReactNode; className?: string }) {
  const { offset: above, depth, pinned: toParent } = useContext(GroupHeadingOffset);
  const ref = useRef<HTMLDivElement>(null);
  const [height, setHeight] = useState(0);
  const start = useRef<HTMLSpanElement>(null);
  const end = useRef<HTMLSpanElement>(null);
  // the edge goes under the innermost pinned heading only: a nested heading pinned below this one takes it
  const pin = useRef({ start: false, end: false, pinned: false, inner: 0 });
  const edge = useCallback(() => { ref.current?.toggleAttribute("data-edge", pin.current.pinned && pin.current.inner === 0); }, []);
  const innerPinned = useCallback((by: 1 | -1) => { pin.current.inner += by; edge(); }, [edge]);
  useEffect(() => {
    const scroller = start.current?.closest("[data-row-scroller]");
    if (!start.current || !end.current || !scroller || typeof IntersectionObserver === "undefined") return;
    const p = pin.current;
    const update = () => {
      const pinned = p.start && !p.end;
      if (pinned === p.pinned) return;
      p.pinned = pinned;
      toParent?.(pinned ? 1 : -1);
      edge();
    };
    const stops = [watchPin(start.current, scroller, (a) => { p.start = a; update(); }), watchPin(end.current, scroller, (a) => { p.end = a; update(); })];
    return () => { for (const stop of stops) stop(); p.start = p.end = false; update(); };
  }, [toParent, edge]);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const scroller = el.closest<HTMLElement>("[data-sidebar-rows]");
    const controls = el.querySelector("[data-sidebar-agents-controls]");
    const stop = watchHeading(el, (height) => {
      setHeight(height);
      if (controls) scroller?.style.setProperty("--sidebar-controls-height", `${height}px`);
    });
    return () => { stop(); if (controls) scroller?.style.removeProperty("--sidebar-controls-height"); };
  }, []);
  const nested = useMemo(() => ({ offset: above + height, depth: depth + 1, pinned: innerPinned }), [above, height, depth, innerPinned]);
  return <div className={className} data-sidebar-group>
    <PinMark mark={start} above={above} />
    <div ref={ref} data-sidebar-group-heading className="sidebar-group-heading" style={{ top: `calc(var(--sidebar-sticky-top, 0px) + ${above}px)`, zIndex: 24 - depth }}>{heading}</div>
    <GroupHeadingOffset.Provider value={nested}>{children}</GroupHeadingOffset.Provider>
    <PinMark mark={end} above={above + height} />
  </div>;
}

/** A sidebar group: its heading folds it. The heading selects no text, so a press-and-drag over the list paints no
 *  selection behind it (#6458); what a group holds, when its name does not say, is an ⓘ in `right` (InfoTip). */
export function Section({ title, children, folded, onToggle, right, onMenu, first }: { title: React.ReactNode; children: React.ReactNode; folded: boolean; onToggle: () => void; right?: React.ReactNode; onMenu?: (at: MenuAt, el: HTMLElement) => void; first?: boolean }) {
  return (
    <SidebarGroup className={first ? "mt-1" : "mt-3"} heading={
      <div className="group/sec relative flex select-none items-center gap-1 px-2 pb-1" {...(onMenu ? contextMenuProps(onMenu) : {})}>
        <button type="button" aria-expanded={!folded} onClick={onToggle} className="touch-hit relative z-10 flex min-w-0 flex-1 items-center gap-1 whitespace-nowrap text-left text-[11px] font-semibold text-ink-3 hover:text-ink-2">
          <span className="min-w-0 truncate">{title}</span>
          <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 transition-transform" style={{ ...sidebarFoldMotion(!folded), transform: folded ? "rotate(-90deg)" : "none" }} />
        </button>
        {right}
      </div>
    }>
      <Collapse open={!folded} sidebarFold unclipped>
        <div className="flex flex-col gap-px" inert={folded || undefined}>{children}</div>
      </Collapse>
    </SidebarGroup>
  );
}

/** A group title's count with what it counts: a tooltip, and the same words for a screen reader. */
export function GroupCount({ n, label, className }: { n: number; label: string; className?: string }) {
  return (
    <span title={label} className={cx("text-[10.5px] text-ink-3 tabular-nums", className)}>
      <span aria-hidden>{n}</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}

/** On a folded group's heading, how many of its sessions need the user (DESIGN_SYSTEM 3.4): the number in the
 *  attention tone, with the words for its tooltip and for a screen reader. Nothing when none do. */
export function NeedsCount({ n }: { n: number }) {
  if (n <= 0) return null;
  return (
    <span title={stateCount("needs_you", n)} className="flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-field px-1 text-[10.5px] font-semibold text-attention tabular-nums">
      {n}<span className="sr-only"> {t("sidebar.needs_you_suffix", { count: n })}</span>
    </span>
  );
}

/** "Load more" (or "Show N more") under a list's rows: a quiet line in the look of the sidebar's own (Recent, a group's
 *  rows), never a button's pill: the paged list's "Load more" looked like no other row (#6662, the owner). */
export function ShowMoreRow({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="relative z-10 w-full whitespace-nowrap rounded-[7px] px-2 py-1 text-left text-label text-ink-3 hover:text-ink-2 [@media(pointer:coarse)]:min-h-11">
      {label}
    </button>
  );
}
