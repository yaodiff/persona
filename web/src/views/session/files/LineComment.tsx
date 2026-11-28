// Comments on lines of a file or a diff (#2437, #11585): the line number turns into a + when the pointer is over the
// row, a click (or a drag, or shift-click, over several) picks the lines, and the shared floating CommentBar opens
// anchored at the picked line (Enter or ✓ saves, Esc cancels).
import { useEffect, useRef, useState } from "react";
import { lineRange } from "../../../lib/comments";
import { Icon } from "../../../ui/icons";
import type { CommentBarAnchor } from "../../chat/CommentBar";
import { CommentBarOverlay, preloadCommentBar } from "../../chat/CommentBarOverlay";
import { NumberDot } from "../../chat/CommentCard";
import { t } from "../../../i18n";

export interface LinePick {
  /** The lines picked, once the pointer let go. */
  range: { from: number; to: number } | null;
  /** Viewport rect of the picked line's gutter button, so the floating bar anchors right at the line. */
  anchor: CommentBarAnchor | null;
  /** The lines being swept right now (the bar waits for the pointer). */
  sweeping: { from: number; to: number } | null;
  down: (n: number, e: React.PointerEvent) => void;
  enter: (n: number, e?: React.PointerEvent) => void;
  clear: () => void;
}

function rectOf(el: EventTarget | null): CommentBarAnchor | null {
  if (!(el instanceof Element)) return null;
  const r = el.getBoundingClientRect();
  return { x: r.right + 8, top: r.top, bottom: r.bottom };
}

function scopeOf(el: EventTarget | null): Element | null {
  return el instanceof Element ? el.closest("[data-testid='diff'], [data-testid='file-code']") : null;
}

function visibleRangeRect(scope: Element | null, from: number, to: number, startEl: Element | null, endEl: Element | null): CommentBarAnchor | null {
  const pickEl = (line: number, fallback: Element | null): Element | null => {
    if (fallback?.isConnected && fallback.getAttribute("data-gutter-at") === String(line)) return fallback;
    // Only re-query within our own connected scroll container so another mounted file/diff gutter is never matched.
    if (scope?.isConnected) return scope.querySelector(`[data-gutter-at="${line}"]`);
    return fallback?.isConnected ? fallback : null;
  };
  const loEl = pickEl(from, startEl);
  const hiEl = from === to ? loEl : pickEl(to, endEl);
  const primary = loEl ?? hiEl;
  if (!primary) return null;
  const loRect = (loEl ?? primary).getBoundingClientRect();
  const hiRect = (hiEl ?? primary).getBoundingClientRect();
  if (loRect.width === 0 && loRect.height === 0 && hiRect.width === 0 && hiRect.height === 0) return null;
  const top = Math.min(loRect.top, hiRect.top);
  const bottom = Math.max(loRect.bottom, hiRect.bottom);
  const scroller = scope?.isConnected ? scope : primary.closest("[data-testid='diff'], [data-testid='file-code']") ?? primary.parentElement?.closest("[class*='overflow-']");
  const bounds = scroller ? scroller.getBoundingClientRect() : new DOMRect(0, 0, window.innerWidth, window.innerHeight);
  if (bottom < bounds.top || top > bounds.bottom) return null;
  if (bottom < 0 || top > window.innerHeight) return null;
  return { x: loRect.right + 8, top, bottom };
}

function sameAnchor(a: CommentBarAnchor | null, b: CommentBarAnchor | null): boolean {
  if (a === b) return true;
  if (!a || !b) return false;
  return a.x === b.x && a.top === b.top && a.bottom === b.bottom;
}

/** Picking lines by their numbers: pointer down starts (shift extends), moving over other rows while it is down sweeps. */
export function useLinePick(enabled: boolean): LinePick {
  const [range, setRange] = useState<{ from: number; to: number } | null>(null);
  const [anchor, setAnchor] = useState<CommentBarAnchor | null>(null);
  const [sweeping, setSweeping] = useState<{ from: number; to: number } | null>(null);
  const startLine = useRef<number | null>(null);
  const anchorRef = useRef<CommentBarAnchor | null>(null);
  const anchorElRef = useRef<Element | null>(null);
  const endElRef = useRef<Element | null>(null);
  const scopeRef = useRef<Element | null>(null);
  const drag = useRef(false);
  useEffect(() => {
    const up = () => {
      if (!drag.current) return;
      drag.current = false;
      setSweeping((s) => {
        if (s) {
          setRange(s);
          setAnchor(anchorRef.current);
        }
        return null;
      });
    };
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", up);
    return () => {
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", up);
    };
  }, []);
  useEffect(() => {
    if (!range) return;
    let frame = 0;
    const measure = () => {
      const next = visibleRangeRect(scopeRef.current, range.from, range.to, anchorElRef.current, endElRef.current);
      anchorRef.current = next;
      setAnchor((prev) => (sameAnchor(prev, next) ? prev : next));
    };
    const soon = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(measure);
    };
    window.addEventListener("scroll", soon, true);
    window.addEventListener("resize", soon);
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener("scroll", soon, true);
      window.removeEventListener("resize", soon);
    };
  }, [range]);
  return {
    range,
    anchor,
    sweeping,
    down: (n, e) => {
      if (!enabled || e.button !== 0) return;
      e.preventDefault(); // no text selection while sweeping the numbers
      const el = e.currentTarget;
      const scope = scopeOf(el);
      if (e.shiftKey && startLine.current !== null) {
        const picked = lineRange(startLine.current, n);
        endElRef.current = el;
        if (!anchorElRef.current?.isConnected) {
          anchorElRef.current = el;
          scopeRef.current = scope;
        }
        anchorRef.current = visibleRangeRect(scopeRef.current ?? scope, picked.from, picked.to, anchorElRef.current, endElRef.current) ?? rectOf(el);
        setSweeping(picked);
      } else {
        startLine.current = n;
        anchorElRef.current = el;
        endElRef.current = el;
        scopeRef.current = scope;
        anchorRef.current = rectOf(el);
        setSweeping({ from: n, to: n });
      }
      setRange(null);
      setAnchor(null);
      drag.current = true;
    },
    enter: (n, e) => {
      if (drag.current && startLine.current !== null) {
        const picked = lineRange(startLine.current, n);
        if (e) {
          endElRef.current = e.currentTarget;
          if (!anchorElRef.current) {
            anchorElRef.current = e.currentTarget;
            scopeRef.current = scopeOf(e.currentTarget);
          }
        }
        anchorRef.current = visibleRangeRect(scopeRef.current, picked.from, picked.to, anchorElRef.current, endElRef.current) ?? anchorRef.current;
        setSweeping(picked);
      }
    },
    clear: () => {
      startLine.current = null;
      anchorRef.current = null;
      anchorElRef.current = null;
      endElRef.current = null;
      scopeRef.current = null;
      setRange(null);
      setAnchor(null);
      setSweeping(null);
    },
  };
}

/** A row's number: the digits, a + under the pointer, the comment's number where one waits. */
export function GutterNumber({ n, at = n, pick, marked, enabled, className, style }: { n: number | undefined; /** What the pick counts by when it is not the number itself (a diff counts rows). */ at?: number; pick: LinePick; marked?: number; enabled: boolean; className?: string; style?: React.CSSProperties }) {
  if (n === undefined || at === undefined || !enabled) return <span className={className} style={style}>{n ?? ""}</span>;
  return (
    <button
      type="button"
      tabIndex={-1}
      aria-label={marked ? t("chat-comments.gutter_marked", { n: marked }) : t("chat-comments.gutter_add", { line: n })}
      title={t("chat-comments.gutter_add", { line: n })}
      onPointerDown={(e) => {
        void preloadCommentBar().catch(() => {});
        pick.down(at, e);
      }}
      onPointerEnter={(e) => pick.enter(at, e)}
      className={`${className ?? ""} relative flex cursor-pointer items-center justify-end`}
      style={style}
      data-gutter={n}
      data-gutter-at={at}
    >
      <span className="[@media(hover:hover)]:group-hover/row:invisible">{marked ? <NumberDot n={marked} /> : n}</span>
      <span aria-hidden className="absolute inset-y-0 right-1.5 hidden items-center text-accent-ink [@media(hover:hover)]:group-hover/row:flex">
        <Icon name="plus" size={13} stroke={2.4} />
      </span>
    </button>
  );
}

/** Floating one-line comment bar anchored at the picked code line (or docked on narrow screens / preview panel). */
const isLineGutter = (el: Element) => Boolean(el.closest("[data-gutter]"));

export function LineBox({ where, anchor, onAdd, onCancel }: { where: string; anchor?: CommentBarAnchor | null; onAdd: (body: string) => void; onCancel: () => void }) {
  return (
    <CommentBarOverlay
      anchor={anchor ?? null}
      context={where}
      showContextOnDesktop
      autoFocus
      ignoreOutside={isLineGutter}
      onSave={onAdd}
      onCancel={onCancel}
      ariaLabel={t("chat-comments.box_label")}
    />
  );
}
