// The drag handle on a column's edge (role="separator"): the side panel's left edge and the sidebar's right edge
// (docs/web/web.md "Side panels", "The sidebar's width"). A mouse, a finger or a pen drags it (the pointer is captured,
// so the drag goes on over an iframe or outside the window), the arrow keys / Home / End move it from the keyboard,
// and a double-click or Enter puts the default width back. The column's owner keeps the width; the handle reports it.
import { useEffect, useRef, useState } from "react";
import { cx } from "./atoms";

/** How far one arrow key press moves a handle. */
export const RESIZE_STEP = 16;

export function ResizeHandle(props: {
  /** The column's edge the handle is on: dragging away from the column widens it. */
  edge: "left" | "right";
  value: number;
  min: number;
  max: number;
  /** Each step of a drag, to draw; the width to keep comes to onCommit when the drag ends. */
  onResize?: (width: number) => void;
  /** The width to keep: the end of a drag, or a key. */
  onCommit: (width: number) => void;
  /** Double-click or Enter: back to the default width. */
  onReset: () => void;
  label: string;
  title?: string;
  controls?: string;
  className?: string;
}) {
  const { edge, value, min, max, onCommit, onReset } = props;
  // the drag's width, for aria-valuenow while the owner may not render (it can draw the drag without React)
  const [live, setLive] = useState<number | null>(null);
  // the window's listeners read the bounds and callbacks of the latest render, not the one the drag started in
  const latest = useRef(props);
  latest.current = props;
  const release = useRef<(() => void) | null>(null);
  // taken away mid-drag (the sidebar folded, the panel closed): let go of the window and the cursor, keep nothing
  useEffect(() => () => release.current?.(), []);
  const clamp = (w: number) => Math.round(Math.max(latest.current.min, Math.min(latest.current.max, w)));
  const wider = edge === "right" ? 1 : -1;

  const start = (e: React.PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0 || release.current) return;
    // Resizing can put a sidebar link under the mouse before its compatibility mousedown arrives.
    // Suppress native link/text dragging, which otherwise cancels the resize pointer in Chrome.
    e.preventDefault();
    e.currentTarget.focus({ preventScroll: true });
    const id = e.pointerId;
    const x = e.clientX;
    // from the width drawn, so a drag starts where the edge is
    const from = value;
    let w = from;
    try {
      e.currentTarget.setPointerCapture(id);
    } catch {
      // a pointer the browser no longer tracks: the window still hears its moves
    }
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
    const move = (ev: PointerEvent) => {
      if (ev.pointerId !== id) return;
      const next = clamp(from + wider * (ev.clientX - x));
      if (next === w) return;
      w = next;
      setLive(w);
      latest.current.onResize?.(w);
    };
    const stop = (ev?: PointerEvent) => {
      if (ev && ev.pointerId !== id) return;
      removeEventListener("pointermove", move);
      removeEventListener("pointerup", stop);
      removeEventListener("pointercancel", stop);
      release.current = null;
      document.body.style.removeProperty("cursor");
      document.body.style.removeProperty("user-select");
      setLive(null);
      if (ev && w !== from) latest.current.onCommit(w);
    };
    release.current = () => stop();
    addEventListener("pointermove", move);
    addEventListener("pointerup", stop);
    addEventListener("pointercancel", stop);
  };

  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={props.label}
      title={props.title}
      aria-controls={props.controls}
      aria-valuemin={min}
      aria-valuemax={max}
      aria-valuenow={live ?? value}
      tabIndex={0}
      onKeyDown={(e) => {
        const to = { [edge === "right" ? "ArrowRight" : "ArrowLeft"]: value + RESIZE_STEP, [edge === "right" ? "ArrowLeft" : "ArrowRight"]: value - RESIZE_STEP, Home: min, End: max }[e.key];
        if (to !== undefined) {
          e.preventDefault();
          onCommit(clamp(to));
        } else if (e.key === "Enter") {
          e.preventDefault();
          onReset();
        }
      }}
      onPointerDown={start}
      onDoubleClick={onReset}
      className={cx("group absolute inset-y-0 z-10 flex w-2 cursor-col-resize items-center justify-center touch-none", edge === "right" ? "-right-1" : "-left-1", props.className)}
    >
      <span aria-hidden className="pointer-events-none h-8 w-0.5 rounded-full bg-line-strong opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-focus-visible:opacity-100" />
    </div>
  );
}
