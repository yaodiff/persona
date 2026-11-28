// The narrow screen's sidebar drawer (#1996), after iOS's DrawerPan (docs/apple/ios.md "Gestures"): a drag to the right anywhere on
// the page pulls it out, a drag to the left while it is open puts it back. The drag is decided by where the finger has
// got to from where it went down: within 45° of horizontal it is the drawer's, steeper is the page scrolling. The drawer
// follows the finger; letting go fast goes that way, otherwise past half opens it. Something under the finger that can
// scroll that way (a code block, a table, the pane tabs, a chart) scrolls first.
import { useEffect, useRef } from "react";

/** How far the finger moves before the drag is decided (px). */
export const SLOP = 8;
/** A fling (px/ms, 300 pt/s like iOS): faster than this, letting go follows the direction. */
export const FLING = 0.3;

/** Whether a move of (dx, dy) from the finger's start is sideways (within 45°), steeper, or too short to say yet. */
export function dragAxis(dx: number, dy: number, slop = SLOP): "x" | "y" | undefined {
  if (Math.hypot(dx, dy) < slop) return undefined;
  return Math.abs(dx) >= Math.abs(dy) ? "x" : "y";
}

/** Whether a sideways drag is the drawer's: closed it only opens (to the right), open it only closes (to the left). */
export function drawerTakes(open: boolean, dx: number): boolean {
  return open ? dx < 0 : dx > 0;
}

/** How far out the drawer is (0 closed … 1 open) with the finger `dx` from where it went down. */
export function dragProgress(open: boolean, dx: number, width: number): number {
  if (width <= 0) return open ? 1 : 0;
  return Math.min(1, Math.max(0, (open ? width + dx : dx) / width));
}

/** Whether letting go at `progress` with the finger moving at `velocity` (px/ms, + is to the right) leaves it open. */
export function settlesOpen(progress: number, velocity: number): boolean {
  if (Math.abs(velocity) > FLING) return velocity > 0;
  return progress > 0.5;
}

/** What a sideways scroller tells about itself (an element's own fields and computed overflow-x). */
export interface ScrollBox {
  scrollLeft: number;
  scrollWidth: number;
  clientWidth: number;
  overflowX: string;
}

/** Whether `b` scrolls with a finger moving `dx`: to the right shows what is to its left, so it needs to be off its
 *  left edge; to the left, off its right edge. At its edge the drag goes to the drawer, like iOS. */
export function scrollsWith(b: ScrollBox, dx: number): boolean {
  if (b.overflowX !== "auto" && b.overflowX !== "scroll") return false;
  const max = b.scrollWidth - b.clientWidth;
  if (max < 1) return false;
  return dx > 0 ? b.scrollLeft > 0.5 : b.scrollLeft < max - 0.5;
}

/** The finger's speed (px/ms) over the last ~100 ms of its samples (oldest first). */
export function velocityOf(samples: readonly { x: number; t: number }[]): number {
  const last = samples[samples.length - 1];
  if (!last) return 0;
  const first = samples.find((s) => last.t - s.t <= 100) ?? last;
  const dt = last.t - first.t;
  return dt > 0 ? (last.x - first.x) / dt : 0;
}

/** The drawer's accessibility on a narrow screen: shut, it is inert (Tab doesn't land in it); open, it is a modal
 *  dialog (and the page under it is inert). Wide, it is the page's column and none of this applies. */
export function drawerA11y(narrow: boolean, open: boolean): { inert: boolean; modal: boolean } {
  return { inert: narrow && !open, modal: narrow && open };
}

/** Where focus goes back when the drawer shuts: the control that opened it, if focus was left in the drawer (or
 *  dropped to the body when it went inert) and that control is still on the page. */
export function focusBack<T extends { isConnected: boolean }>(opener: T | null, active: unknown, inDrawer: boolean, body: unknown): T | null {
  if (!opener || !opener.isConnected || opener === body) return null;
  return inDrawer || active === body || active === null ? opener : null;
}

const SKIP = "input, textarea, select, [contenteditable=''], [contenteditable='true'], [data-decision-swipe]";

function yields(target: Element, root: Element, dx: number): boolean {
  for (let el: Element | null = target; el && el !== root; el = el.parentElement) {
    if (el.matches(SKIP)) return true;
    const h = el as HTMLElement;
    if (h.scrollWidth > h.clientWidth && scrollsWith({ scrollLeft: h.scrollLeft, scrollWidth: h.scrollWidth, clientWidth: h.clientWidth, overflowX: getComputedStyle(h).overflowX }, dx)) return true;
  }
  return false;
}

/** Drives the drawer with touch drags on `root` while `enabled`. `drawer` and `scrim` are moved directly while the
 *  finger is down (no render per frame); letting go hands the result to `setOpen` and CSS animates the rest. Touches
 *  that start outside `scope` (e.g. a full-screen side panel over the page) are left alone. */
export function useDrawerDrag(opts: {
  root: React.RefObject<HTMLElement | null>;
  drawer: React.RefObject<HTMLElement | null>;
  scrim: React.RefObject<HTMLElement | null>;
  open: boolean;
  setOpen: (open: boolean) => void;
  enabled: boolean;
  scope: (target: Element) => boolean;
}) {
  const live = useRef(opts);
  live.current = opts;
  useEffect(() => {
    const root = opts.root.current;
    if (!root || !opts.enabled) return;
    type Track = { x0: number; y0: number; open: boolean; width: number; target: Element; on: boolean; progress: number; samples: { x: number; t: number }[] };
    let t: Track | null = null;
    const paint = (p: number | null) => {
      const d = live.current.drawer.current, s = live.current.scrim.current;
      if (d) {
        d.style.transition = p === null ? "" : "none";
        // Tailwind's translate-x-* set the `translate` property (not `transform`): the drag sets the same one
        d.style.translate = p === null ? "" : `${(p - 1) * 100}% 0`;
      }
      if (s) {
        s.style.transition = p === null ? "" : "none";
        s.style.opacity = p === null ? "" : String(p);
        // a closed drawer's scrim is out of the page (display: none, #2102): a drag brings it in for as long as it goes
        s.style.display = p === null ? "" : "block";
      }
    };
    const start = (e: TouchEvent) => {
      const f = e.touches[0];
      const target = e.target instanceof Element ? e.target : null;
      t = null;
      if (e.touches.length !== 1 || !f || !target || !live.current.scope(target)) return;
      t = { x0: f.clientX, y0: f.clientY, open: live.current.open, width: 0, target, on: false, progress: 0, samples: [{ x: f.clientX, t: e.timeStamp }] };
    };
    const move = (e: TouchEvent) => {
      const f = e.touches[0];
      if (!t || !f) return;
      const dx = f.clientX - t.x0, dy = f.clientY - t.y0;
      if (!t.on) {
        const axis = dragAxis(dx, dy);
        // still deciding: keep a sideways start from turning into a scroll the page can't take back. Not over a
        // sideways scroller or a field (yields): cancelling the first move there would lose it its own pan for the
        // whole touch on iOS, and it is the one that wins.
        if (!axis) {
          if (Math.abs(dx) > Math.abs(dy) && drawerTakes(t.open, dx) && e.cancelable && !yields(t.target, root, dx)) e.preventDefault();
          return;
        }
        if (axis === "y" || !drawerTakes(t.open, dx) || !e.cancelable || yields(t.target, root, dx)) {
          t = null;
          return;
        }
        t.on = true;
        // measured once the drag is the drawer's, not on every touch (a layout read)
        t.width = live.current.drawer.current?.offsetWidth ?? 0;
      }
      e.preventDefault();
      t.progress = dragProgress(t.open, dx, t.width);
      t.samples.push({ x: f.clientX, t: e.timeStamp });
      if (t.samples.length > 8) t.samples.shift();
      paint(t.progress);
    };
    const end = () => {
      const done = t;
      t = null;
      if (!done?.on) return;
      paint(null);
      live.current.setOpen(settlesOpen(done.progress, velocityOf(done.samples)));
    };
    root.addEventListener("touchstart", start, { passive: true });
    root.addEventListener("touchmove", move, { passive: false });
    root.addEventListener("touchend", end);
    root.addEventListener("touchcancel", end);
    return () => {
      root.removeEventListener("touchstart", start);
      root.removeEventListener("touchmove", move);
      root.removeEventListener("touchend", end);
      root.removeEventListener("touchcancel", end);
      if (t?.on) paint(null);
      t = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the refs are stable; the rest is read live
  }, [opts.enabled]);
}
