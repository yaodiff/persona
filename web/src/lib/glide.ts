// Where the sidebar's gliding highlight (Beautiful UI's Sidebar Nav) goes: the part of its row that is on show (#2476).
// Rows sit inside folding groups (Collapse clips them with overflow hidden), so a row in a closed or closing group is
// clipped away while its own box still has a height; the highlight follows the clip and goes away with the row.

import type { CSSProperties } from "react";

/** A box in viewport coordinates (`top`/`bottom`, with optional horizontal bounds for row-width matching). */
export type Span = { top: number; bottom: number; left?: number; right?: number };

/** The highlight's place in the list's own coordinates (it scrolls with the rows). */
export type GlideBox = { top: number; height: number; left?: number; width?: number };

/** Less than this much of the row on show and the highlight goes away (a sliver at the end of a fold, rounding). */
export const MIN_VISIBLE = 2;

/** The highlight for a row at `row`, clipped by the boxes it sits in (`clips`, the overflow-hidden ancestors between
 *  it and the list), in the coordinates of the list at `list` scrolled by `scrollTop`. null: no row, or
 *  none of it is on show (folded away, zero height, not laid out). */
export function glideBox(row: Span | null | undefined, clips: readonly Span[], list: Span, scrollTop: number): GlideBox | null {
  if (!row) return null;
  let top = row.top,
    bottom = row.bottom;
  for (const c of clips) {
    top = Math.max(top, c.top);
    bottom = Math.min(bottom, c.bottom);
  }
  if (bottom - top < MIN_VISIBLE) return null;
  const box: GlideBox = { top: top - list.top + scrollTop, height: bottom - top };
  if (row.left != null && row.right != null && list.left != null) {
    box.width = row.right - row.left;
    if (box.width < MIN_VISIBLE) return null;
    box.left = row.left - list.left;
  }
  return box;
}

/** Whether two placements are the same (a re-measure that moved nothing doesn't re-render). */
export function sameBox(a: GlideBox | null, b: GlideBox | null): boolean {
  return a === b || (!!a && !!b && a.top === b.top && a.height === b.height && a.left === b.left && a.width === b.width);
}

/** Whether an element's overflow clips its children (a Collapse's inner box, a truncating wrapper). */
export function clipsOverflow(overflowY: string): boolean {
  return overflowY === "hidden" || overflowY === "clip";
}

/** How long the highlight takes to glide to its next row. */
export const GLIDE_MS = 220;

/** The highlight's inline style over `at` (shown when `on`), for the sidebar and the admin page's nav. It moves by
 *  transform, which the compositor animates without laying the list out each frame (animating `top` / `height` did:
 *  5x the layouts on a sweep over the rows). When `instant` is true (appearing from hidden), it snaps into place and
 *  only fades opacity instead of sliding in from a stale row. */
export function glideStyle(at: GlideBox | null, on: boolean, ease: string, instant = false): CSSProperties {
  return {
    top: 0,
    left: at?.left,
    width: at?.width,
    height: at?.height ?? 0,
    transform: `translateY(${at?.top ?? 0}px)`,
    opacity: on ? 1 : 0,
    transition: `${instant ? "" : `transform ${GLIDE_MS}ms ${ease}, `}opacity 150ms ease`,
  };
}
