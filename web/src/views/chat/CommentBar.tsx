// Floating one-line comment bar (#11585): anchored at selected transcript words, a clicked pin, or a picked code line.
// On narrow/touch viewports (~390px or coarse pointer) it docks cleanly above the bottom viewport / soft keyboard while
// keeping the comment's number and context visible. Enter or ✓ saves (empty note allowed), Esc cancels, and editing an
// existing pin offers an in-place delete button.
import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type RefObject } from "react";
import { createPortal } from "react-dom";
import { base64File } from "../../lib/attachments";
import { MAX_COMMENT_CHARS } from "../../lib/comments";
import type { CommentImage } from "../../lib/drafts";
import { useComposition } from "../../lib/ime";
import { enterSends, hasFinePointer, NO_HOVER, PHONE, useMedia } from "../../lib/viewport";
import { cx, EASE, IconButton } from "../../ui/atoms";
import { AttachmentTray } from "../../ui/AttachmentTray";
import { Icon } from "../../ui/icons";
import { NumberDot } from "./CommentCard";
import { t } from "../../i18n";

export interface CommentBarAnchor {
  /** Preferred left/right x coordinate in viewport pixels. */
  x: number;
  /** Top of the anchored selection/pin/line in viewport pixels. */
  top: number;
  /** Bottom of the anchored selection/pin/line in viewport pixels. */
  bottom: number;
}

export interface CommentBarProps {
  /** Viewport anchor for desktop floating placement; null/omitted docks cleanly at the bottom. */
  anchor?: CommentBarAnchor | null;
  /** Comment number badge (shown when editing a pin, or on narrow dock to keep context clear). */
  number?: number;
  /** Short context preview (selected words or `file:line` location). Shown on narrow dock and for code lines. */
  context?: string;
  /** Always show the context label even on desktop (e.g. code line `src/app.ts:12-14`). */
  showContextOnDesktop?: boolean;
  /** Initial note value (empty for new comment, existing note when editing a pin). */
  initialValue?: string;
  /** Auto-focus the input on mount (true for pin edit, code lines, or after C shortcut). */
  autoFocus?: boolean;
  /** Optional ref to the underlying text field so callers (e.g. C shortcut) can focus it. */
  inputRef?: RefObject<HTMLTextAreaElement | null>;
  /** Called when the user focuses the input (e.g. so selectionchange does not dismiss a new selection bar). */
  onInputFocus?: () => void;
  /** Called on every edit so callers can flush dirty notes before switching pins or toggling popovers. */
  onDraftChange?: (body: string) => void;
  /** Called on Enter or ✓ click with the trimmed note (empty string allowed). */
  onSave: (body: string) => void;
  /** Called on Esc or explicit cancel button (reverts live-persisted edits when applicable). */
  onCancel: () => void;
  /** Called on outside pointerdown when clean/unchanged; defaults to onCancel. Live-persisted editors pass close-without-revert. */
  onDismiss?: () => void;
  /** Called when another CommentBar takes active ownership; closes cleanly without reverting live-persisted edits. */
  onYield?: () => void;
  /** When editing an existing comment, shows an in-place delete button. */
  onDelete?: () => void;
  /** Images belong to selection comments, including when they are reopened from a pin or the chip. */
  images?: readonly CommentImage[];
  onAddImages?: (files: File[], signal: AbortSignal) => Promise<void>;
  onRemoveImage?: (id: string) => void;
  /** Accessible label for the input field. */
  ariaLabel?: string;
  /** Elements matching this predicate do not trigger outside-click dismissal (e.g. code line gutters for shift-click range extension). */
  ignoreOutside?: (target: Element) => boolean;
  /** Intercept window-level Escape even if focus is outside the bar (true for explicit modals like pin edit / line box; false for unfocused text selection). */
  captureEscape?: boolean;
  /** Optional single-key shortcut badge (e.g. "C") shown while the bar is dormant/unfocused so users know how to focus it. */
  shortcutHint?: string;
  /** Optional portal target; defaults to `document.body`. */
  portal?: Element | null;
}

const BAR_MAX_W = 340;
const EDGE = 8;
const GAP = 8;

type ActiveBar = { id: symbol; yieldControl: () => void };
let activeBar: ActiveBar | null = null;

function subscribeResize(onStoreChange: () => void) {
  if (typeof window === "undefined") return () => {};
  window.addEventListener("resize", onStoreChange);
  return () => window.removeEventListener("resize", onStoreChange);
}

function getViewportWidth() {
  return typeof window === "undefined" ? 1024 : window.innerWidth;
}

function getViewportHeight() {
  return typeof window === "undefined" ? 800 : window.innerHeight;
}

/** Computes clamped viewport coordinates for the floating bar, flipping above the anchor if near the bottom. */
export function placeCommentBar(anchor: CommentBarAnchor, width: number, height: number, vw: number, vh: number): { left: number; top: number } {
  const w = Math.min(width, Math.max(160, vw - EDGE * 2));
  const left = Math.min(Math.max(anchor.x - w / 2, EDGE), Math.max(EDGE, vw - w - EDGE));
  const below = anchor.bottom + GAP;
  const above = anchor.top - height - GAP;
  const top = below + height <= vh - EDGE ? below : above >= EDGE ? above : Math.max(EDGE, vh - height - EDGE);
  return { left: Math.round(left), top: Math.round(top) };
}

export function CommentBar({
  anchor,
  number,
  context,
  showContextOnDesktop = false,
  initialValue = "",
  autoFocus = false,
  inputRef,
  onInputFocus,
  onDraftChange,
  onSave,
  onCancel,
  onDismiss,
  onYield,
  onDelete,
  images,
  onAddImages,
  onRemoveImage,
  ariaLabel,
  ignoreOutside,
  captureEscape = false,
  shortcutHint,
  portal,
}: CommentBarProps) {
  const [body, setBody] = useState(initialValue);
  const bodyRef = useRef(body);
  bodyRef.current = body;
  const initialRef = useRef(initialValue);
  initialRef.current = initialValue;
  const onSaveRef = useRef(onSave);
  onSaveRef.current = onSave;
  const onCancelRef = useRef(onCancel);
  onCancelRef.current = onCancel;
  const onDismissRef = useRef(onDismiss);
  onDismissRef.current = onDismiss;
  const onYieldRef = useRef(onYield);
  onYieldRef.current = onYield;
  const ime = useComposition();
  const fileInput = useRef<HTMLInputElement>(null);
  const reads = useRef<AbortController | null>(null);
  const [preparing, setPreparing] = useState(0);
  const [previews, setPreviews] = useState<{ id: string; name: string; url: string }[]>([]);
  const previewUrls = useRef(new Map<CommentImage, string>());
  useLayoutEffect(() => {
    const urls = previewUrls.current;
    return () => {
      urls.forEach((url) => URL.revokeObjectURL(url));
      urls.clear();
    };
  }, []);
  useLayoutEffect(() => {
    const urls = previewUrls.current;
    if (!urls.size && !images?.length) return;
    for (const [image, url] of urls) {
      if (images?.includes(image)) continue;
      URL.revokeObjectURL(url);
      urls.delete(image);
    }
    setPreviews((images ?? []).map((image) => {
      const { id, name, mime, data } = image;
      const url = urls.get(image) ?? URL.createObjectURL(base64File(name, mime, data));
      urls.set(image, url);
      return { id, name, url };
    }));
  }, [images]);
  const addImages = async (files: File[]) => {
    if (!onAddImages || !files.length) return;
    const { signal } = reads.current ??= new AbortController();
    setPreparing((n) => n + 1);
    try {
      await onAddImages(files, signal);
    } finally {
      if (!signal.aborted) setPreparing((n) => n - 1);
    }
  };
  const cancel = () => {
    reads.current?.abort();
    onCancel();
  };
  const [focused, setFocused] = useState(autoFocus);
  const active = focused || captureEscape;
  // Keep a mounted dirty bar registered across blur until it is saved, cancelled, or yielded to another bar.
  const ownsBar = active || body !== initialValue || preparing > 0;
  const barRef = useRef<HTMLDivElement>(null);
  const localInput = useRef<HTMLTextAreaElement>(null);
  const field = inputRef ?? localInput;
  const isPhone = useMedia(PHONE);
  const isTouch = useMedia(NO_HOVER);
  const viewportWidth = useSyncExternalStore(subscribeResize, getViewportWidth, () => 1024);
  const viewportHeight = useSyncExternalStore(subscribeResize, getViewportHeight, () => 800);
  const narrow = isPhone || isTouch || viewportWidth <= 639;
  const docked = narrow || !anchor;

  const [pos, setPos] = useState<{ left: number; top: number }>(() =>
    anchor ? placeCommentBar(anchor, BAR_MAX_W, 44, viewportWidth, viewportHeight) : { left: EDGE, top: EDGE },
  );

  useEffect(() => {
    setBody(initialValue);
  }, [initialValue]);

  useLayoutEffect(() => {
    if (!ownsBar) return;
    const id = Symbol();
    const prev = activeBar;
    const mine: ActiveBar = {
      id,
      yieldControl: () => {
        if (onYieldRef.current) onYieldRef.current();
        else if (bodyRef.current !== initialRef.current) onSaveRef.current(bodyRef.current.trim());
        else onCancelRef.current();
      },
    };
    activeBar = mine;
    if (prev && prev.id !== id) prev.yieldControl();
    return () => {
      if (activeBar?.id === id) activeBar = null;
    };
  }, [ownsBar]);

  useLayoutEffect(() => {
    if (!autoFocus) return;
    const el = field.current;
    if (!el) return;
    el.focus({ preventScroll: true });
    el.setSelectionRange(el.value.length, el.value.length);
  }, [autoFocus, field]);

  useLayoutEffect(() => {
    const el = field.current;
    if (!el) return;
    el.style.height = "auto";
    if (el.scrollHeight > 0) el.style.height = `${Math.min(el.scrollHeight, 96)}px`;
  }, [body, field]);

  useLayoutEffect(() => {
    if (docked || !anchor || !barRef.current || typeof window === "undefined") return;
    // Use unscaled layout dimensions so CSS pop-in scale transforms do not under-measure width/height.
    const w = barRef.current.offsetWidth || BAR_MAX_W;
    const h = barRef.current.offsetHeight || 44;
    setPos(placeCommentBar(anchor, w, h, viewportWidth, viewportHeight));
  }, [anchor, docked, showContextOnDesktop, context, viewportWidth, viewportHeight, body, preparing, previews.length]);

  // Dismiss on outside click only when the note is unchanged from initialValue (raw comparison so whitespace edits are kept).
  useEffect(() => {
    const onPointerDown = (e: PointerEvent) => {
      if (!(e.target instanceof Node)) return;
      if (barRef.current?.contains(e.target)) return;
      if (e.target instanceof Element && ignoreOutside?.(e.target)) return;
      if (bodyRef.current !== initialRef.current || preparing > 0) return;
      (onDismissRef.current ?? onCancelRef.current)();
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || ime.busy(e)) return;
      const inside = Boolean(barRef.current && document.activeElement && barRef.current.contains(document.activeElement));
      if (!inside && !captureEscape) return;
      e.preventDefault();
      e.stopPropagation();
      cancel();
    };
    document.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown, true);
      window.removeEventListener("keydown", onKeyDown, true);
    };
  }, [cancel, ignoreOutside, captureEscape, ime, preparing]);

  const submit = () => onSave(body.trim());
  const showContext = Boolean(context) && (docked || showContextOnDesktop);
  const cleanContext = context ? context.replace(/\s+/g, " ").trim() : "";

  const node = (
    <div
      ref={barRef}
      role={focused || captureEscape ? "dialog" : undefined}
      aria-label={ariaLabel ?? t("chat-comments.placeholder")}
      data-comment-bar
      data-focused={focused ? "true" : "false"}
      data-docked={docked ? "true" : undefined}
      onPointerDown={(e) => e.stopPropagation()}
      onClick={(e) => {
        if (e.target instanceof Element && e.target.closest("button, textarea, input")) return;
        field.current?.focus({ preventScroll: true });
      }}
      className={cx(
        "fixed z-40 flex flex-col gap-1 rounded-[12px] border p-1.5 text-ink transition-[border-color,box-shadow,background-color] duration-150",
        focused
          ? "border-accent bg-surface shadow-[0_0_0_1.5px_var(--accent),var(--shadow-overlay)]"
          : "border-line bg-surface/95 shadow-raised hover:border-line-strong",
        docked ? "inset-x-2 max-w-[calc(100vw-16px)]" : "w-[min(340px,calc(100vw-16px))]",
      )}
      style={
        docked
          ? {
              // Ride above the visual viewport bottom (soft keyboard on iOS/Android) with safe-area padding.
              bottom: "calc(100dvh - var(--kb-top, 0px) - var(--kb-h, 100dvh) + env(safe-area-inset-bottom, 0px) + 8px)",
              animation: `pop-in 140ms ${EASE} both`,
            }
          : {
              left: pos.left,
              top: pos.top,
              animation: `pop-in 140ms ${EASE} both`,
            }
      }
    >
      {showContext && (
        <div className="flex min-w-0 items-center gap-1.5 px-1 text-submeta text-ink-3" data-comment-bar-context>
          {number !== undefined && <NumberDot n={number} />}
          <span className={cx("min-w-0 flex-1 truncate", showContextOnDesktop && !docked && "font-mono")} title={cleanContext}>
            {cleanContext}
          </span>
          {docked && (
            <button
              type="button"
              aria-label={t("chat-comments.cancel")}
              title={t("chat-comments.cancel")}
              onPointerDown={(e) => e.preventDefault()}
              onMouseDown={(e) => e.preventDefault()}
              onClick={cancel}
              className="flex size-5 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink"
            >
              <Icon name="x" size={11} stroke={2.4} />
            </button>
          )}
        </div>
      )}
      <div className="flex min-w-0 items-center gap-1.5">
        {!showContext && number !== undefined && <NumberDot n={number} className="ml-1" />}
        <textarea
          ref={field}
          rows={1}
          value={body}
          maxLength={MAX_COMMENT_CHARS}
          aria-label={ariaLabel ?? t("chat-comments.placeholder")}
          placeholder={t("chat-comments.placeholder")}
          {...ime.handlers}
          onFocus={() => {
            setFocused(true);
            onInputFocus?.();
          }}
          onBlur={() => {
            ime.onBlur();
            setFocused(false);
          }}
          onChange={(e) => {
            setBody(e.target.value);
            onDraftChange?.(e.target.value);
          }}
          onPaste={onAddImages && ((e) => {
            const files = Array.from(e.clipboardData.files ?? []).filter((file) => file.type.startsWith("image/"));
            if (!files.length) {
              for (const item of Array.from(e.clipboardData.items ?? [])) {
                if (item.kind !== "file") continue;
                const file = item.getAsFile();
                if (file?.type.startsWith("image/")) files.push(file);
              }
            }
            if (!files.length) return;
            e.preventDefault();
            void addImages(files);
          })}
          onKeyDown={(e) => {
            if (ime.busy(e.nativeEvent)) return;
            if (enterSends(e, hasFinePointer())) {
              e.preventDefault();
              submit();
            } else if (e.key === "Escape") {
              e.preventDefault();
              e.stopPropagation();
              cancel();
            }
          }}
          className="max-h-24 min-h-7 min-w-0 flex-1 resize-none overflow-y-auto bg-transparent px-1.5 py-[5px] text-meta leading-[1.4] text-ink outline-none placeholder:text-ink-3"
        />
        {!focused && shortcutHint && !docked && (
          <kbd
            data-comment-shortcut
            aria-hidden="true"
            className="shrink-0 rounded-[5px] border border-line-strong bg-inset px-1.5 py-0.5 font-sans text-submeta leading-none font-medium text-ink-3"
          >
            {shortcutHint}
          </kbd>
        )}
        {onAddImages && (
          <>
            <IconButton icon="image" label={t("chat-comments.add_images")} className="shrink-0" onClick={() => fileInput.current?.click()} />
            <input ref={fileInput} type="file" accept="image/*" multiple hidden onChange={(e) => {
              void addImages(Array.from(e.currentTarget.files ?? []));
              e.currentTarget.value = "";
            }} />
          </>
        )}
        {onDelete && (
          <button
            type="button"
            data-comment-delete
            aria-label={t("chat-comments.remove")}
            title={t("chat-comments.remove")}
            onPointerDown={(e) => e.preventDefault()}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => {
              reads.current?.abort();
              onDelete();
            }}
            className="flex size-7 shrink-0 items-center justify-center rounded-[8px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-red"
          >
            <Icon name="trash" size={13} />
          </button>
        )}
        <button
          type="button"
          data-comment-save
          aria-label={t("chat-comments.save")}
          title={t("chat-comments.save")}
          onPointerDown={(e) => e.preventDefault()}
          onMouseDown={(e) => e.preventDefault()}
          onClick={submit}
          className="flex size-7 shrink-0 items-center justify-center rounded-[8px] bg-accent-ink text-surface transition-[filter,transform] duration-150 hover:brightness-105 active:scale-[0.95]"
        >
          <Icon name="check" size={14} stroke={2.4} />
        </button>
      </div>
      {preparing > 0 && <span role="status" className="px-1 text-submeta text-ink-3">{t("chat-comments.preparing_images")}</span>}
      <AttachmentTray attachments={previews} onRemove={onRemoveImage} />
    </div>
  );

  const target = portal ?? (typeof document !== "undefined" ? document.body : null);
  return target ? createPortal(node, target) : node;
}
