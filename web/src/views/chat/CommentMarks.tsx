// Comments in the transcript (#2437, #11585): selecting words opens a floating one-line comment bar anchored right at
// the selection (pressing C focuses it immediately); Enter or ✓ saves (empty note allowed), Esc cancels. Saved comments
// stay lit in the transcript with a numbered pin beside them; clicking a pin reopens the same floating bar anchored at
// that pin to edit or delete in place without jumping focus down to the bottom composer.
import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { anchorOf, onJumpToComment, rangeFor } from "../../lib/comment-marks";
import { textQuote, type TextAnchor } from "../../lib/comments";
import { addQuote, cancelNote, removeQuote, setCommentImages, setNote, useQuotes, type CommentImage, type CommentImagePicker } from "../../lib/drafts";
import { focusKind } from "../../lib/shortcuts";
import { cx } from "../../ui/atoms";
import { scrollBehavior } from "../../ui/stream/motion";
import type { CommentBarAnchor } from "./CommentBar";
import { CommentBarOverlay, preloadCommentBar } from "./CommentBarOverlay";
import { NumberDot } from "./CommentCard";
import { useChatActions } from "./chat-actions";
import { t } from "../../i18n";
import { composing } from "../../lib/send-history";

interface Box {
  id: string;
  n: number;
  rects: { x: number; y: number; w: number; h: number }[];
}

interface SelectionDraft {
  text: string;
  anchor: TextAnchor | null;
  range: Range;
  rect: CommentBarAnchor | null;
  focused: boolean;
  quote: { id?: string; cancelled?: boolean };
}

interface PinEdit {
  id: string;
  rect: CommentBarAnchor | null;
  originalBody: string;
  originalImages?: readonly CommentImage[];
}

interface HighlightBucket {
  base: Range[];
  pulsed: Range[];
}

const DOT = 16;
const FLASH_MS = 1400;
const HIGHLIGHT_NAME = "persona-comment";
const HIGHLIGHT_FLASH_NAME = "persona-comment-flash";
const isCommentPin = (el: Element) => Boolean(el.closest("[data-comment-pin]"));

// CSS.highlights is document-global while CommentMarks mounts per chat scroller (e.g. main chat + SidePanel).
// Track each mounted instance's ranges separately and publish their union so panes never wipe each other.
const highlightBuckets = new Map<symbol, HighlightBucket>();

function supportsCustomHighlight(): boolean {
  return typeof CSS !== "undefined" && Boolean(CSS.highlights) && typeof Highlight !== "undefined";
}

function publishCustomHighlights(): void {
  if (!supportsCustomHighlight()) return;
  const base: Range[] = [];
  const pulsed: Range[] = [];
  for (const bucket of highlightBuckets.values()) {
    for (const r of bucket.base) base.push(r);
    for (const r of bucket.pulsed) pulsed.push(r);
  }
  if (base.length) CSS.highlights.set(HIGHLIGHT_NAME, new Highlight(...base));
  else CSS.highlights.delete(HIGHLIGHT_NAME);
  if (pulsed.length) CSS.highlights.set(HIGHLIGHT_FLASH_NAME, new Highlight(...pulsed));
  else CSS.highlights.delete(HIGHLIGHT_FLASH_NAME);
}

function setInstanceHighlights(owner: symbol, base: Range[], pulsed: Range[]): void {
  if (base.length || pulsed.length) {
    highlightBuckets.set(owner, { base, pulsed });
    publishCustomHighlights();
  } else if (highlightBuckets.delete(owner)) {
    publishCustomHighlights();
  }
}

function clearInstanceHighlights(owner: symbol): void {
  if (highlightBuckets.delete(owner)) publishCustomHighlights();
}

function sameAnchor(a: CommentBarAnchor | null, b: CommentBarAnchor | null): boolean {
  if (a === b) return true;
  if (!a || !b) return false;
  return a.x === b.x && a.top === b.top && a.bottom === b.bottom;
}

function sameRange(a: Range, b: Range): boolean {
  return (
    a.startContainer === b.startContainer &&
    a.startOffset === b.startOffset &&
    a.endContainer === b.endContainer &&
    a.endOffset === b.endOffset
  );
}

function rectInBounds(range: Range, bounds: DOMRect): CommentBarAnchor | null {
  const rects = range.getClientRects();
  const first = rects.length ? rects[0] : range.getBoundingClientRect();
  const last = rects.length ? rects[rects.length - 1] : range.getBoundingClientRect();
  if (last.width === 0 && last.height === 0) return null;
  if (last.bottom < bounds.top || first.top > bounds.bottom - 4) return null;
  return { x: (last.left + last.right) / 2, top: first.top, bottom: last.bottom };
}

/** `jump`: brings words into view `topOffset` px from the top and keeps them there while the exchanges around them are
 *  drawn (ChatView landRow); without it, the scroller's own smooth scroll. */
export function CommentMarks({ scroller, jump, onAddImages }: {
  scroller: RefObject<HTMLDivElement | null>; jump?: (words: Range, topOffset: number) => void; onAddImages?: CommentImagePicker;
}) {
  const chat = useChatActions();
  const key = chat?.draftKey ?? "";
  const can = Boolean(chat?.canCompose);
  const { list } = useQuotes(key);

  // ── floating bar for new selection ──
  const [draft, setDraft] = useState<SelectionDraft | null>(null);
  const draftRef = useRef<SelectionDraft | null>(null);
  draftRef.current = draft;
  const draftNoteRef = useRef("");
  const barInputRef = useRef<HTMLTextAreaElement>(null);
  const dragging = useRef(false);

  // ── floating bar for editing an existing pin ──
  const [editingPin, setEditingPin] = useState<PinEdit | null>(null);
  const editingPinRef = useRef<PinEdit | null>(null);
  editingPinRef.current = editingPin;

  const reposition = useCallback(() => {
    const el = scroller.current;
    if (!el) return;
    const bounds = el.getBoundingClientRect();
    const curDraft = draftRef.current;
    if (curDraft && (curDraft.focused || draftNoteRef.current !== "")) {
      const r = (curDraft.anchor ? rangeFor(el, curDraft.anchor) : null) ?? curDraft.range;
      const nextRect = r ? rectInBounds(r, bounds) : null;
      setDraft((prev) => (prev && !sameAnchor(prev.rect, nextRect) ? { ...prev, rect: nextRect } : prev));
    }
    const curPin = editingPinRef.current;
    if (curPin) {
      const btn = [...el.querySelectorAll<HTMLElement>("[data-comment-pin-id]")].find((node) => node.dataset.commentPinId === curPin.id);
      let nextRect: CommentBarAnchor | null = null;
      if (btn) {
        const r = btn.getBoundingClientRect();
        if (r.bottom >= bounds.top && r.top <= bounds.bottom) nextRect = { x: r.right, top: r.top, bottom: r.bottom };
      }
      setEditingPin((prev) => (prev && prev.id === curPin.id && !sameAnchor(prev.rect, nextRect) ? { ...prev, rect: nextRect } : prev));
    }
  }, [scroller]);

  const read = useCallback(() => {
    const el = scroller.current;
    const sel = document.getSelection();
    // Keep the bar open while the user is typing in it, has an unsaved note, or is editing an existing pin; only re-measure on scroll/resize.
    if (draftRef.current?.focused || (draftRef.current && draftNoteRef.current !== "") || editingPinRef.current) {
      reposition();
      return;
    }
    const hide = () => setDraft((d) => (d ? null : d));
    if (!can || !el || !sel || sel.isCollapsed || sel.rangeCount === 0 || dragging.current) return hide();
    const range = sel.getRangeAt(0);
    const text = sel.toString();
    if (!text.trim() || !el.contains(range.commonAncestorContainer)) return hide();
    const rect = rectInBounds(range, el.getBoundingClientRect());
    // the end of the words is out of sight: nothing to hang the bar under
    if (!rect) return hide();
    void preloadCommentBar().catch(() => {});
    const next: SelectionDraft = {
      text,
      anchor: anchorOf(range),
      range: range.cloneRange(),
      rect,
      focused: false,
      quote: {},
    };
    setDraft((prev) =>
      prev && prev.text === next.text && sameAnchor(prev.rect, next.rect) && sameRange(prev.range, next.range) ? prev : next,
    );
  }, [can, scroller, reposition]);

  useEffect(() => {
    let frame = 0;
    const soon = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(read);
    };
    const down = (e: PointerEvent) => {
      if (e.target instanceof Node && scroller.current?.contains(e.target)) {
        dragging.current = true;
        if (!draftRef.current?.focused && draftNoteRef.current === "") setDraft(null);
      }
    };
    const up = () => {
      if (!dragging.current) return;
      dragging.current = false;
      soon();
    };
    const el = scroller.current;
    document.addEventListener("selectionchange", soon);
    document.addEventListener("pointerdown", down, true);
    document.addEventListener("pointerup", up, true);
    document.addEventListener("pointercancel", up, true);
    el?.addEventListener("scroll", soon, { passive: true });
    window.addEventListener("resize", soon);
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("selectionchange", soon);
      document.removeEventListener("pointerdown", down, true);
      document.removeEventListener("pointerup", up, true);
      document.removeEventListener("pointercancel", up, true);
      el?.removeEventListener("scroll", soon);
      window.removeEventListener("resize", soon);
    };
  }, [read, scroller]);

  // C, with words picked and the keyboard not in a field: focus (or refocus after blur) the floating bar at the selection
  useEffect(() => {
    if (!draft) return;
    const press = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== "c" || e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || e.repeat || composing(e)) return;
      if (focusKind(e.target) !== "page") return;
      e.preventDefault();
      setDraft((d) => (d && !d.focused ? { ...d, focused: true } : d));
      barInputRef.current?.focus({ preventScroll: true });
    };
    window.addEventListener("keydown", press);
    return () => window.removeEventListener("keydown", press);
  }, [draft]);

  const selectionQuoteId = useCallback(
    (cur: SelectionDraft, note: string) => {
      if (!key || cur.quote.cancelled) return;
      if (cur.quote.id) return cur.quote.id;
      const el = scroller.current;
      // Re-resolve the live range before saving so streaming/re-renders never persist stale offsets or text.
      const liveRange =
        (el && cur.anchor ? rangeFor(el, cur.anchor) : null) ??
        (cur.range.startContainer.isConnected && cur.range.endContainer.isConnected ? cur.range : null);
      const liveAnchor = liveRange ? (anchorOf(liveRange) ?? cur.anchor) : cur.anchor;
      const liveText = liveRange ? liveRange.toString() : cur.text;
      const q = textQuote(liveText.trim() ? liveText : cur.text, liveAnchor ?? undefined);
      if (note) q.comment!.body = note;
      // write = false: note was already written in the floating bar; focus never jumps to the bottom composer
      return cur.quote.id = addQuote(key, q, false);
    },
    [key, scroller],
  );

  const closeDraft = useCallback(() => {
    draftNoteRef.current = "";
    draftRef.current = null;
    setDraft(null);
  }, []);

  const saveDraft = (note: string) => {
    const cur = draftRef.current;
    if (!cur) return;
    const id = selectionQuoteId(cur, note);
    if (id) setNote(key, id, note, true);
    document.getSelection()?.removeAllRanges();
    closeDraft();
  };

  const keepDraft = () => {
    const note = draftNoteRef.current.trim();
    if (note || draftRef.current?.quote.id) saveDraft(note);
    else closeDraft();
  };

  const cancelDraft = () => {
    const cur = draftRef.current;
    if (cur) {
      cur.quote.cancelled = true;
      if (cur.quote.id) removeQuote(key, cur.quote.id);
    }
    closeDraft();
  };

  const selectionQuote = draft?.quote.id ? list.find((q) => q.id === draft.quote.id) : undefined;
  useEffect(() => {
    if (draft?.quote.id && !selectionQuote) closeDraft();
  }, [draft, selectionQuote, closeDraft]);

  // ── the lit words ──
  const marks = useMemo(() => {
    let n = 0;
    const out: { id: string; n: number; anchor: TextAnchor }[] = [];
    for (const q of list) {
      if (!q.comment) continue;
      n++;
      if (q.comment.anchor && q.id) out.push({ id: q.id, n, anchor: q.comment.anchor });
    }
    return out;
  }, [list]);
  const selectionNumber = useMemo(() => {
    const comments = list.filter((q) => q.comment);
    return selectionQuote ? comments.indexOf(selectionQuote) + 1 : comments.length + 1;
  }, [list, selectionQuote]);
  const [boxes, setBoxes] = useState<Box[]>([]);
  const boxesNow = useRef(boxes);
  boxesNow.current = boxes;
  const [flash, setFlash] = useState<string | null>(null);
  const flashNow = useRef(flash);
  flashNow.current = flash;
  const flashTimer = useRef<number | undefined>(undefined);
  const nativeHighlight = useMemo(() => supportsCustomHighlight(), []);
  const [highlightOwner] = useState(() => Symbol());
  const rangesNow = useRef(new Map<string, Range>());

  const syncCustomHighlights = useCallback(
    (activeFlash: string | null) => {
      if (!nativeHighlight) return;
      const base: Range[] = [];
      const pulsed: Range[] = [];
      for (const [id, range] of rangesNow.current) {
        if (id === activeFlash) pulsed.push(range);
        else base.push(range);
      }
      setInstanceHighlights(highlightOwner, base, pulsed);
    },
    [highlightOwner, nativeHighlight],
  );

  // Close the pin editor if its comment was removed from elsewhere (e.g. composer popover).
  const editingQuote = useMemo(() => {
    if (!editingPin) return null;
    let n = 0;
    for (const q of list) {
      if (!q.comment) continue;
      n++;
      if (q.id === editingPin.id) return { quote: q, n };
    }
    return null;
  }, [editingPin, list]);

  useEffect(() => {
    if (editingPin && !editingQuote) setEditingPin(null);
  }, [editingPin, editingQuote]);

  const measure = useCallback(() => {
    const el = scroller.current;
    if (!el) return;
    const base = el.getBoundingClientRect();
    const out: Box[] = [];
    const nextRanges = new Map<string, Range>();
    for (const m of marks) {
      const range = rangeFor(el, m.anchor);
      if (!range) continue;
      nextRanges.set(m.id, range);
      const rects = [...range.getClientRects()]
        .filter((r) => r.width > 0 && r.height > 0)
        .map((r) => ({ x: Math.round(r.left - base.left + el.scrollLeft), y: Math.round(r.top - base.top + el.scrollTop), w: Math.round(r.width), h: Math.round(r.height) }));
      if (rects.length) out.push({ id: m.id, n: m.n, rects });
    }
    rangesNow.current = nextRanges;
    syncCustomHighlights(flashNow.current);
    setBoxes((prev) => (JSON.stringify(prev) === JSON.stringify(out) ? prev : out));
  }, [marks, scroller, syncCustomHighlights]);

  useEffect(() => {
    syncCustomHighlights(flash);
  }, [flash, syncCustomHighlights]);

  useEffect(() => {
    return () => {
      window.clearTimeout(flashTimer.current);
      flashTimer.current = undefined;
      rangesNow.current = new Map();
      if (nativeHighlight) clearInstanceHighlights(highlightOwner);
    };
  }, [highlightOwner, nativeHighlight]);

  // Passive effect so ancestor refs (ChatView's ref={scrollRef} wrapping CommentMarks) are attached before measuring.
  useEffect(() => {
    if (!marks.length) {
      rangesNow.current = new Map();
      window.clearTimeout(flashTimer.current);
      flashTimer.current = undefined;
      setFlash((f) => (f ? null : f));
      syncCustomHighlights(null);
      setBoxes((prev) => (prev.length ? [] : prev));
      return;
    }
    measure();
    const el = scroller.current;
    const content = el?.firstElementChild;
    if (!el || !content) return;
    let frame = 0;
    const soon = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(measure);
    };
    const mutations = new MutationObserver(soon);
    mutations.observe(content, { childList: true, subtree: true, characterData: true });
    const sizes = new ResizeObserver(soon);
    sizes.observe(content);
    sizes.observe(el);
    return () => {
      cancelAnimationFrame(frame);
      mutations.disconnect();
      sizes.disconnect();
    };
  }, [marks, measure, scroller, syncCustomHighlights]);

  // the chip popover's click: scroll to the words and pulse them
  const marksNow = useRef(marks);
  marksNow.current = marks;
  const jumpNow = useRef(jump);
  jumpNow.current = jump;
  useEffect(
    () =>
      onJumpToComment((id) => {
        const box = boxesNow.current.find((b) => b.id === id);
        const el = scroller.current;
        if (!box || !el) return false;
        const anchor = marksNow.current.find((m) => m.id === id)?.anchor;
        const jumpFn = jumpNow.current;
        const words = jumpFn && anchor ? rangeFor(el, anchor) : null;
        if (words && jumpFn) jumpFn(words, -el.clientHeight / 3);
        else el.scrollTo({ top: Math.max(0, box.rects[0].y - el.clientHeight / 3), behavior: scrollBehavior() });
        window.clearTimeout(flashTimer.current);
        setFlash(id);
        flashTimer.current = window.setTimeout(() => {
          flashTimer.current = undefined;
          setFlash((f) => (f === id ? null : f));
        }, FLASH_MS);
        return true;
      }),
    [scroller],
  );

  const openPin = (id: string, e: React.MouseEvent<HTMLButtonElement>) => {
    if (editingPinRef.current?.id === id) return;
    keepDraft();
    // Previous pin edits are already live-persisted. Escape restores this pin's note and images.
    const nextQuote = list.find((q) => q.id === id);
    const r = e.currentTarget.getBoundingClientRect();
    setEditingPin({ id, rect: { x: r.right, top: r.top, bottom: r.bottom }, originalBody: nextQuote?.comment?.body ?? "", originalImages: nextQuote?.images });
  };

  return (
    <>
      {boxes.length > 0 && (
        <div className="pointer-events-none absolute top-0 left-0 h-0 w-full" data-comment-marks>
          {boxes.map((b) => {
            const last = b.rects[b.rects.length - 1];
            return (
              <div key={b.id} data-comment-mark={b.n}>
                {!nativeHighlight &&
                  b.rects.map((r, i) => (
                    <span
                      key={i}
                      className={cx("absolute rounded-[3px] bg-accent/20 transition-shadow duration-300", flash === b.id && "shadow-[0_0_0_2px_var(--accent)]")}
                      style={{ left: r.x - 1, top: r.y, width: r.w + 2, height: r.h }}
                    />
                  ))}
                <button
                  type="button"
                  tabIndex={-1}
                  data-comment-pin={b.n}
                  data-comment-pin-id={b.id}
                  aria-label={t("chat-comments.comment_n", { n: b.n })}
                  onClick={(e) => openPin(b.id, e)}
                  className="pointer-events-auto absolute flex cursor-pointer items-center justify-center rounded-full"
                  // like a footnote mark: after the words, a little above their line
                  style={{ left: last.x + last.w - 2, top: last.y - DOT / 2, width: DOT, height: DOT }}
                >
                  <NumberDot n={b.n} />
                </button>
              </div>
            );
          })}
        </div>
      )}
      {draft && !editingPin && (
        <CommentBarOverlay
          anchor={draft.rect}
          number={selectionNumber}
          context={draft.text}
          inputRef={barInputRef}
          autoFocus={draft.focused}
          shortcutHint="C"
          ignoreOutside={isCommentPin}
          onInputFocus={() => setDraft((d) => (d && !d.focused ? { ...d, focused: true } : d))}
          onDraftChange={(next) => {
            draftNoteRef.current = next;
            if (draft.quote.id) setNote(key, draft.quote.id, next, true);
          }}
          images={selectionQuote?.images}
          onAddImages={onAddImages && ((files, signal) => {
            setDraft((d) => d && !d.focused ? { ...d, focused: true } : d);
            return onAddImages(() => {
              const current = draftRef.current;
              return selectionQuoteId(draft, current?.quote === draft.quote ? draftNoteRef.current : "");
            }, files, signal);
          })}
          onRemoveImage={(id) => {
            if (draft.quote.id) setCommentImages(key, draft.quote.id, (images) => images.filter((image) => image.id !== id));
          }}
          onSave={saveDraft}
          onCancel={cancelDraft}
          onDismiss={keepDraft}
          onYield={keepDraft}
          ariaLabel={t("chat-comments.note_label", { n: selectionNumber })}
        />
      )}
      {editingPin && editingQuote && (
        <CommentBarOverlay
          key={editingPin.id}
          anchor={editingPin.rect}
          number={editingQuote.n}
          context={editingQuote.quote.text}
          initialValue={editingPin.originalBody}
          images={editingQuote.quote.images}
          onAddImages={onAddImages && ((files, signal) => onAddImages(editingPin.id, files, signal))}
          onRemoveImage={(id) => setCommentImages(key, editingPin.id, (images) => images.filter((image) => image.id !== id))}
          autoFocus
          captureEscape
          ignoreOutside={isCommentPin}
          onDraftChange={(next) => {
            if (key) setNote(key, editingPin.id, next, true);
          }}
          onSave={(body) => {
            if (key) setNote(key, editingPin.id, body, true);
            setEditingPin(null);
          }}
          onCancel={() => {
            if (key) cancelNote(key, editingPin.id, editingPin.originalBody, editingPin.originalImages);
            setEditingPin(null);
          }}
          onDismiss={() => setEditingPin(null)}
          onYield={() => setEditingPin(null)}
          onDelete={() => {
            if (key) removeQuote(key, editingPin.id);
            setEditingPin(null);
          }}
          ariaLabel={t("chat-comments.note_label", { n: editingQuote.n })}
        />
      )}
    </>
  );
}
