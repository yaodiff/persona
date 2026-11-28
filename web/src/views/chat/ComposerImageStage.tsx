// Enlarged image stage shown above the elevated single live Composer (#17095, #17173): FLIP-animates both the
// clicked thumbnail and the live composer container, dynamically tracks the growing composer's height, supports
// multi-image navigation and drag-and-drop, and integrates inline Box, Pen, and Crop editing with non-destructive
// auto-commit and latest-wins async encoding directly above the live Composer.
import { useCallback, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState, type HTMLAttributes, type RefObject } from "react";
import { createPortal } from "react-dom";
import { t } from "../../i18n";
import { useCloseOnBack } from "../../lib/back-close";
import type { DraftImage } from "../../lib/drafts";
import {
  handleCursor,
  IMAGE_EDIT,
  isFramedAsIs,
  resetFrame,
  reshape,
  turnLeft,
  type EditedImage,
  type EditState,
  type Ink,
  type Size,
} from "../../lib/image-edit";
import { NARROW, PHONE, useMedia } from "../../lib/viewport";
import { cx, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import {
  BarIcon,
  EditorMenu,
  IconAction,
  InkSwatches,
  LineSample,
  renderEditedImage,
  shapeName,
  ToolButton,
  useFitsWords,
  useImageEditorCore,
  widthName,
  type EditorMenuState,
  type EditorTool,
} from "./image-editor-shared";

export interface ComposerImageStageItem {
  id: string;
  name: string;
  url: string;
  edited?: EditedImage;
}

export interface ComposerImageStageHandle {
  /** Returns a Promise awaiting all in-flight image encodes (resolving false on duplicate concurrent send flush), or null when idle. */
  flush: () => Promise<boolean> | null;
  /** Flushes any in-flight encode before switching to `id`. */
  select: (id: string) => void;
}

export interface ComposerImageStageProps {
  attachments: readonly ComposerImageStageItem[];
  activeId: string;
  closing: boolean;
  compact: boolean;
  composerRef: RefObject<HTMLDivElement | null>;
  slotRef: RefObject<HTMLDivElement | null>;
  handleRef?: RefObject<ComposerImageStageHandle | null>;
  innerOverlayOpen: boolean;
  dragging: boolean;
  drag: Pick<HTMLAttributes<HTMLDivElement>, "onDragOver" | "onDragLeave" | "onDrop">;
  onSelect: (id: string) => void;
  onUpdateAttachment: (id: string, img: DraftImage, state: EditState) => void;
  onClose: () => void;
  onCloseComplete: () => void;
}

const OPEN_MS = 280;
const CLOSE_MS = 210;
const SWITCH_MS = 200;
const STAGE_EASE = "cubic-bezier(0.22, 1, 0.36, 1)";
const CLOSE_EASE = "cubic-bezier(0.4, 0, 0.2, 1)";

function reducedMotion(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function canAnimate(el: Element | null): el is HTMLElement {
  return Boolean(el && typeof (el as HTMLElement).animate === "function" && !reducedMotion());
}

function clamp(v: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, v));
}

function hasExternalOverlay(): boolean {
  return Boolean(document.querySelector("[role=dialog],[role=menu]"));
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return Boolean(target.closest("input, textarea, select, [contenteditable='true'], [contenteditable='']") || target.isContentEditable);
}

function findTileRect(composer: HTMLElement | null, id: string): DOMRect | null {
  if (!composer) return null;
  for (const el of composer.querySelectorAll<HTMLElement>("[data-attachment-id]")) {
    if (el.dataset.attachmentId === id) {
      const rect = el.getBoundingClientRect();
      return rect.width > 0 && rect.height > 0 ? rect : null;
    }
  }
  return null;
}

let openStageCount = 0;

/** Marks the document root while ComposerImageStage is mounted so fixed stage/composer geometry escapes shell filters and clip-path. */
function retainComposerStage(): () => void {
  const root = document.documentElement;
  if (++openStageCount === 1) root.dataset.composerStage = "1";
  let released = false;
  return () => {
    if (released) return;
    released = true;
    if (--openStageCount === 0) delete root.dataset.composerStage;
  };
}

export function ComposerImageStage({
  attachments,
  activeId,
  closing,
  compact,
  composerRef,
  slotRef,
  handleRef,
  innerOverlayOpen,
  dragging,
  drag,
  onSelect,
  onUpdateAttachment,
  onClose,
  onCloseComplete,
}: ComposerImageStageProps) {
  const rootRef = useRef<HTMLDivElement>(null);
  const backdropRef = useRef<HTMLDivElement>(null);
  const headerRef = useRef<HTMLDivElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);
  const imgRef = useRef<HTMLImageElement>(null);
  const cropRowRef = useRef<HTMLDivElement>(null);

  const [tool, setTool] = useState<EditorTool | null>(null);
  const [ink, setInk] = useState<Ink>("red");
  const [lineWidth, setLineWidth] = useState<number>(IMAGE_EDIT.defaultLineWidth);
  const [menu, setMenu] = useState<EditorMenuState>(null);
  const [menuHost, setMenuHost] = useState<HTMLDivElement | null>(null);
  const [localEdits, setLocalEdits] = useState<Record<string, EditState>>({});
  const [pendingIds, setPendingIds] = useState<ReadonlySet<string>>(() => new Set());

  const narrow = useMedia(NARROW);
  const phone = useMedia(PHONE);
  const index = Math.max(0, attachments.findIndex((a) => a.id === activeId));
  const active = attachments[index] ?? null;
  const many = attachments.length > 1;
  const activeSource = active ? active.edited?.source ?? active.url : "";
  const activeStart = active ? localEdits[active.id] ?? active.edited : undefined;
  const pendingActive = Boolean(active && pendingIds.has(active.id));
  const editActive = Boolean(active && !closing && tool !== null);
  const showCanvas = Boolean(active && !closing && (tool !== null || pendingActive));

  const decodedCache = useRef(new Map<string, HTMLImageElement>());
  const reqSeq = useRef(0);
  const latestReqById = useRef(new Map<string, number>());
  const inFlightById = useRef(new Map<string, { reqId: number; promise: Promise<void> }>());
  const pendingSelectRef = useRef<string | null>(null);
  const flushingSendRef = useRef(false);
  const aliveRef = useRef(true);
  const suppressBackdropClickRef = useRef(false);

  // Close open tool menus whenever active image changes or stage begins closing.
  const [menuKey, setMenuKey] = useState(`${activeId}:${closing}`);
  const nextMenuKey = `${activeId}:${closing}`;
  if (menuKey !== nextMenuKey) {
    setMenuKey(nextMenuKey);
    if (menu) setMenu(null);
  }

  useEffect(() => {
    aliveRef.current = true;
    const cache = decodedCache.current;
    return () => {
      aliveRef.current = false;
      cache.clear();
    };
  }, []);

  // Drop cached per-attachment tracking entries when attachments are removed.
  useEffect(() => {
    const liveIds = new Set(attachments.map((a) => a.id));
    for (const id of latestReqById.current.keys()) {
      if (!liveIds.has(id)) latestReqById.current.delete(id);
    }
    setLocalEdits((prev) => {
      let changed = false;
      const next: Record<string, EditState> = {};
      for (const [id, state] of Object.entries(prev)) {
        if (liveIds.has(id)) next[id] = state;
        else changed = true;
      }
      return changed ? next : prev;
    });
  }, [attachments]);

  const handleCommit = useCallback(
    (nextState: EditState, imgEl: HTMLImageElement, natural: Size) => {
      const target = latest.current.active;
      if (!target) return;
      const targetId = target.id;
      const targetSrc = target.edited?.source ?? target.url;
      const targetName = target.name;
      setLocalEdits((prev) => ({ ...prev, [targetId]: nextState }));

      const reqId = ++reqSeq.current;
      latestReqById.current.set(targetId, reqId);

      const promise = (async () => {
        try {
          const res = await renderEditedImage(imgEl, targetSrc, nextState.edits, natural, targetName);
          if (!aliveRef.current || latestReqById.current.get(targetId) !== reqId) return;
          if (!res.ok) {
            toast(t(res.reason === "too_large" ? "chat-imageeditor.too_large" : "chat-imageeditor.save_failed"), "error");
            return;
          }
          latest.current.onUpdateAttachment(targetId, res.image, nextState);
        } catch {
          if (!aliveRef.current || latestReqById.current.get(targetId) !== reqId) return;
          toast(t("chat-imageeditor.save_failed"), "error");
        } finally {
          if (inFlightById.current.get(targetId)?.reqId === reqId) {
            inFlightById.current.delete(targetId);
            if (aliveRef.current) {
              setPendingIds((prev) => {
                if (!prev.has(targetId)) return prev;
                const next = new Set(prev);
                next.delete(targetId);
                return next;
              });
            }
          }
        }
      })();

      inFlightById.current.set(targetId, { reqId, promise });
      setPendingIds((prev) => {
        if (prev.has(targetId)) return prev;
        const next = new Set(prev);
        next.add(targetId);
        return next;
      });
    },
    [],
  );

  const editor = useImageEditorCore({
    src: activeSource,
    stateKey: active?.id ?? "",
    start: activeStart,
    tool: editActive ? tool : null,
    ink,
    lineWidth,
    disabled: closing,
    canvasActive: showCanvas,
    decodedCache,
    onCommit: handleCommit,
    onDecodeError: () => {
      toast(t("chat-imageeditor.unreadable"), "error");
      setTool(null);
    },
    onDragStart: () => {
      suppressBackdropClickRef.current = true;
    },
  });

  const cropWords = useFitsWords(cropRowRef, `${tool ?? ""}${editor.edits.shape}${phone}`);

  const flushAll = useCallback((): Promise<void> | null => {
    if (inFlightById.current.size === 0) return null;
    return (async () => {
      while (inFlightById.current.size > 0) {
        await Promise.allSettled([...inFlightById.current.values()].map((entry) => entry.promise));
      }
    })();
  }, []);

  const flushForSend = useCallback((): Promise<boolean> | null => {
    const pending = flushAll();
    if (!pending) return null;
    if (flushingSendRef.current) return Promise.resolve(false);
    flushingSendRef.current = true;
    return pending
      .then(() => true)
      .finally(() => {
        flushingSendRef.current = false;
      });
  }, [flushAll]);

  const selectWithFlush = useCallback(
    (id: string) => {
      const pending = flushAll();
      if (!pending) {
        pendingSelectRef.current = null;
        latest.current.onSelect(id);
        return;
      }
      pendingSelectRef.current = id;
      void pending.then(() => {
        if (!aliveRef.current || pendingSelectRef.current !== id) return;
        pendingSelectRef.current = null;
        if (latest.current.attachments.some((a) => a.id === id)) {
          latest.current.onSelect(id);
        }
      });
    },
    [flushAll],
  );

  const closeWithFlush = useCallback(() => {
    pendingSelectRef.current = null;
    const pending = flushAll();
    if (!pending) {
      latest.current.onClose();
      return;
    }
    void pending.then(() => {
      if (aliveRef.current) latest.current.onClose();
    });
  }, [flushAll]);

  const exitTool = useCallback(() => {
    setMenu(null);
    editor.cancelDrag();
    setTool(null);
  }, [editor.cancelDrag]);

  const toggleTool = (next: EditorTool) => {
    setMenu(null);
    editor.cancelDrag();
    setTool((cur) => (cur === next ? null : next));
  };

  useImperativeHandle(
    handleRef,
    () => ({
      flush: flushForSend,
      select: selectWithFlush,
    }),
    [flushForSend, selectWithFlush],
  );

  // Latest callbacks/state for event listeners without re-binding on every render.
  const latest = useRef({
    active,
    attachments,
    index,
    closing,
    modalOpen: innerOverlayOpen,
    menuOpen: menu !== null,
    tool,
    draggingEdit: editor.dragging,
    canUndo: editor.history.past.length > 0 && Boolean(editor.img),
    canRedo: editor.history.future.length > 0 && Boolean(editor.img),
    exitTool,
    stepBack: editor.stepBack,
    stepForward: editor.stepForward,
    selectWithFlush,
    closeWithFlush,
    onSelect,
    onUpdateAttachment,
    onClose,
    onCloseComplete,
  });
  latest.current = {
    active,
    attachments,
    index,
    closing,
    modalOpen: innerOverlayOpen,
    menuOpen: menu !== null,
    tool,
    draggingEdit: editor.dragging,
    canUndo: editor.history.past.length > 0 && Boolean(editor.img),
    canRedo: editor.history.future.length > 0 && Boolean(editor.img),
    exitTool,
    stepBack: editor.stepBack,
    stepForward: editor.stepForward,
    selectWithFlush,
    closeWithFlush,
    onSelect,
    onUpdateAttachment,
    onClose,
    onCloseComplete,
  };

  // Track running Web Animations and open origin thumbnail rect captured before elevating the composer.
  const openAnims = useRef<Animation[]>([]);
  const closeAnims = useRef<Animation[]>([]);
  const originThumbRef = useRef<DOMRect | null>(null);
  const playedOpenImg = useRef(false);
  const prevShown = useRef<{ id: string; url: string; index: number } | null>(null);
  const hadOverlayAtDown = useRef(false);
  const swipeStart = useRef<{ x: number; y: number } | null>(null);
  const didSwipe = useRef(false);

  const cancelAll = (list: RefObject<Animation[]>) => {
    for (const anim of list.current) {
      try {
        anim.cancel();
      } catch {
        // already finished or unsupported
      }
    }
    list.current = [];
  };

  // Back gesture on narrow screens collapses the stage instead of leaving the conversation.
  useCloseOnBack(!closing, closeWithFlush, narrow);

  // Sync the elevated composer's width when viewport crosses the compact breakpoint.
  useLayoutEffect(() => {
    const composer = composerRef.current;
    if (!composer || composer.style.position !== "fixed") return;
    composer.style.width = compact ? "calc(100vw - 20px)" : "min(920px, calc(100vw - 48px))";
  }, [compact, composerRef]);

  // Keep the stage's bottom clearance strictly above the live elevated composer as it grows or wraps.
  // Read untransformed offsetHeight so FLIP scale transforms never under-measure clearance.
  useLayoutEffect(() => {
    const stage = stageRef.current;
    const composer = composerRef.current;
    if (!stage || !composer) return;
    const sync = () => {
      const h = composer.offsetHeight || composer.getBoundingClientRect().height;
      if (h > 0) stage.style.setProperty("--composer-stage-h", `${Math.ceil(h)}px`);
    };
    sync();
    const ro = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(sync);
    ro?.observe(composer);
    window.addEventListener("resize", sync);
    window.visualViewport?.addEventListener("resize", sync);
    return () => {
      ro?.disconnect();
      window.removeEventListener("resize", sync);
      window.visualViewport?.removeEventListener("resize", sync);
    };
  }, [composerRef]);

  // Play the FLIP animation on the enlarged image once its rendered box is measurable.
  const playOpenImageFlip = useCallback(() => {
    if (playedOpenImg.current) return;
    const img = imgRef.current;
    if (!img || !canAnimate(img)) {
      playedOpenImg.current = true;
      return;
    }
    const to = img.getBoundingClientRect();
    if (to.width <= 0 || to.height <= 0) return;
    playedOpenImg.current = true;
    const from = originThumbRef.current;
    if (from && from.width > 0 && from.height > 0) {
      const dx = (from.left + from.width / 2) - (to.left + to.width / 2);
      const dy = (from.top + from.height / 2) - (to.top + to.height / 2);
      const scale = clamp(Math.min(from.width / to.width, from.height / to.height), 0.08, 1);
      openAnims.current.push(
        img.animate(
          [
            { transform: `translate3d(${dx}px, ${dy}px, 0) scale(${scale})`, opacity: 0.65 },
            { transform: "translate3d(0, 0, 0) scale(1)", opacity: 1 },
          ],
          { duration: OPEN_MS, easing: STAGE_EASE },
        ),
      );
    } else {
      openAnims.current.push(
        img.animate(
          [
            { transform: "translate3d(0, 10px, 0) scale(0.94)", opacity: 0 },
            { transform: "translate3d(0, 0, 0) scale(1)", opacity: 1 },
          ],
          { duration: OPEN_MS, easing: STAGE_EASE },
        ),
      );
    }
  }, []);

  // Clear shell filter/backdrop-filter/clip-path before measuring or elevating the live composer.
  useLayoutEffect(() => retainComposerStage(), []);

  // Open choreography: measure the docked live composer & clicked tile before elevating the same DOM node to its
  // fixed bottom dock, hold the normal-flow placeholder height so chat scroll never jumps, and run FLIP animations.
  useLayoutEffect(() => {
    cancelAll(openAnims);
    const composer = composerRef.current;
    const slot = slotRef.current;
    const fromBox = composer?.getBoundingClientRect() ?? null;
    originThumbRef.current = findTileRect(composer, activeId);
    if (slot && fromBox && fromBox.height > 0) {
      slot.style.height = `${fromBox.height}px`;
    }
    if (composer) {
      composer.style.position = "fixed";
      composer.style.left = "0";
      composer.style.right = "0";
      composer.style.marginInline = "auto";
      composer.style.zIndex = "36";
      composer.style.width = compact ? "calc(100vw - 20px)" : "min(920px, calc(100vw - 48px))";
      composer.style.bottom = "calc(max(0px, 100dvh - var(--kb-top) - var(--kb-h)) + max(12px, env(safe-area-inset-bottom)))";
      composer.style.transformOrigin = "center bottom";
      const h = composer.offsetHeight || composer.getBoundingClientRect().height;
      if (h > 0 && stageRef.current) stageRef.current.style.setProperty("--composer-stage-h", `${Math.ceil(h)}px`);
    }
    if (canAnimate(backdropRef.current)) {
      openAnims.current.push(
        backdropRef.current.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 240, easing: STAGE_EASE }),
      );
    }
    if (canAnimate(headerRef.current)) {
      openAnims.current.push(
        headerRef.current.animate(
          [
            { opacity: 0, transform: "translate3d(0, -8px, 0)" },
            { opacity: 1, transform: "translate3d(0, 0, 0)" },
          ],
          { duration: 240, easing: STAGE_EASE },
        ),
      );
    }
    if (fromBox && fromBox.width > 0 && canAnimate(composer)) {
      const to = composer.getBoundingClientRect();
      if (to.width > 0 && to.height > 0) {
        const dx = (fromBox.left + fromBox.width / 2) - (to.left + to.width / 2);
        const dy = fromBox.bottom - to.bottom;
        const scale = clamp(fromBox.width / to.width, 0.72, 1.08);
        openAnims.current.push(
          composer.animate(
            [
              { transform: `translate3d(${dx}px, ${dy}px, 0) scale(${scale})`, opacity: 0.94 },
              { transform: "translate3d(0, 0, 0) scale(1)", opacity: 1 },
            ],
            { duration: OPEN_MS, easing: STAGE_EASE },
          ),
        );
      }
    }
    playOpenImageFlip();
    return () => {
      cancelAll(openAnims);
      if (composer) {
        composer.style.position = "";
        composer.style.left = "";
        composer.style.right = "";
        composer.style.marginInline = "";
        composer.style.zIndex = "";
        composer.style.width = "";
        composer.style.bottom = "";
        composer.style.transformOrigin = "";
      }
      if (slot) slot.style.height = "";
    };
    // Run once on mount; captures the live docked geometry before elevating the composer.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Multi-image switch animation in preview mode.
  useLayoutEffect(() => {
    if (!active || closing) return;
    const prev = prevShown.current;
    prevShown.current = { id: active.id, url: active.url, index };
    if (!prev || tool !== null) return;
    const img = imgRef.current;
    if (!canAnimate(img)) return;
    if (prev.id !== active.id) {
      const dir = index > prev.index ? 1 : index < prev.index ? -1 : 0;
      img.animate(
        [
          { transform: `translate3d(${dir * 20}px, 0, 0) scale(0.985)`, opacity: 0.25 },
          { transform: "translate3d(0, 0, 0) scale(1)", opacity: 1 },
        ],
        { duration: SWITCH_MS, easing: STAGE_EASE },
      );
    }
  }, [active, index, closing, tool]);

  // Collapse choreography: smoothly animate the live composer back onto its docked slot and fold the image away.
  useLayoutEffect(() => {
    if (!closing) {
      cancelAll(closeAnims);
      return;
    }
    cancelAll(openAnims);
    cancelAll(closeAnims);

    const composer = composerRef.current;
    if (!canAnimate(composer)) {
      latest.current.onCloseComplete();
      return;
    }

    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      cancelAll(closeAnims);
      latest.current.onCloseComplete();
    };

    const fromBox = composer.getBoundingClientRect();
    const toSlot = slotRef.current?.getBoundingClientRect();
    let leadAnim: Animation | undefined;

    if (toSlot && toSlot.width > 0 && fromBox.width > 0) {
      const dx = (toSlot.left + toSlot.width / 2) - (fromBox.left + fromBox.width / 2);
      const dy = toSlot.bottom - fromBox.bottom;
      const scale = clamp(toSlot.width / fromBox.width, 0.72, 1.08);
      leadAnim = composer.animate(
        [
          { transform: "translate3d(0, 0, 0) scale(1)" },
          { transform: `translate3d(${dx}px, ${dy}px, 0) scale(${scale})` },
        ],
        { duration: CLOSE_MS, easing: CLOSE_EASE, fill: "forwards" },
      );
      closeAnims.current.push(leadAnim);
    }

    if (canAnimate(backdropRef.current)) {
      const anim = backdropRef.current.animate([{ opacity: 1 }, { opacity: 0 }], {
        duration: CLOSE_MS,
        easing: CLOSE_EASE,
        fill: "forwards",
      });
      closeAnims.current.push(anim);
      leadAnim ??= anim;
    }

    if (canAnimate(headerRef.current)) {
      closeAnims.current.push(
        headerRef.current.animate(
          [
            { opacity: 1, transform: "translate3d(0, 0, 0)" },
            { opacity: 0, transform: "translate3d(0, -6px, 0)" },
          ],
          { duration: 160, easing: CLOSE_EASE, fill: "forwards" },
        ),
      );
    }

    const img = imgRef.current;
    if (active && canAnimate(img)) {
      const fromImg = img.getBoundingClientRect();
      const tileRect = findTileRect(composer, active.id);
      if (fromImg.width > 0 && fromImg.height > 0 && tileRect && toSlot && toSlot.width > 0 && fromBox.width > 0) {
        const boxScale = clamp(toSlot.width / fromBox.width, 0.72, 1.08);
        const targetCx = toSlot.left + (tileRect.left - fromBox.left) * boxScale + (tileRect.width * boxScale) / 2;
        const targetCy = toSlot.bottom - (fromBox.bottom - tileRect.bottom) * boxScale - (tileRect.height * boxScale) / 2;
        const imgDx = targetCx - (fromImg.left + fromImg.width / 2);
        const imgDy = targetCy - (fromImg.top + fromImg.height / 2);
        const imgScale = clamp(Math.min(tileRect.width / fromImg.width, tileRect.height / fromImg.height), 0.08, 0.7);
        closeAnims.current.push(
          img.animate(
            [
              { transform: "translate3d(0, 0, 0) scale(1)", opacity: 1 },
              { transform: `translate3d(${imgDx}px, ${imgDy}px, 0) scale(${imgScale})`, opacity: 0 },
            ],
            { duration: CLOSE_MS, easing: CLOSE_EASE, fill: "forwards" },
          ),
        );
      } else {
        closeAnims.current.push(
          img.animate(
            [
              { transform: "scale(1)", opacity: 1 },
              { transform: "scale(0.92)", opacity: 0 },
            ],
            { duration: CLOSE_MS, easing: CLOSE_EASE, fill: "forwards" },
          ),
        );
      }
    }

    const fallback = window.setTimeout(finish, CLOSE_MS + 60);
    if (leadAnim) {
      leadAnim.finished.then(finish, () => {});
    } else {
      finish();
    }

    return () => {
      window.clearTimeout(fallback);
      if (!done) cancelAll(closeAnims);
    };
  }, [closing, active, composerRef, slotRef]);

  // Keyboard choreography:
  // - Esc: sub-menu -> composer inner overlays -> deactivate active inline tool -> collapse stage (never reaching ChatView)
  // - Cmd/Ctrl+Z / Shift+Z / Ctrl+Y and ArrowLeft/ArrowRight: strictly yield to editable inputs; outside inputs, undo/redo or switch images.
  const handleKeyDown = useCallback((e: KeyboardEvent | React.KeyboardEvent) => {
    const cur = latest.current;
    const native = "nativeEvent" in e ? e.nativeEvent : e;
    if (cur.closing || e.defaultPrevented || native.isComposing || native.keyCode === 229) return;
    if (cur.modalOpen || cur.menuOpen || hasExternalOverlay()) return;

    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      if (cur.tool !== null) cur.exitTool();
      else cur.closeWithFlush();
      return;
    }

    if (isEditableTarget(e.target)) return;

    const key = e.key.toLowerCase();
    if ((e.metaKey || e.ctrlKey) && !e.altKey && (key === "z" || (e.ctrlKey && !e.metaKey && key === "y"))) {
      if (cur.tool !== null || cur.canUndo || cur.canRedo) {
        e.preventDefault();
        e.stopPropagation();
        if (cur.draggingEdit) return;
        if (key === "y" || e.shiftKey) cur.stepForward();
        else cur.stepBack();
      }
      return;
    }

    if ((e.key === "ArrowLeft" || e.key === "ArrowRight") && cur.attachments.length > 1 && !cur.draggingEdit) {
      const delta = e.key === "ArrowLeft" ? -1 : 1;
      const next = cur.attachments[cur.index + delta];
      if (!next) return;
      e.preventDefault();
      cur.selectWithFlush(next.id);
    }
  }, []);

  useEffect(() => {
    // Listen on document (bubble) so it runs after inner React controls and yields to menus/dialogs,
    // while still firing before ChatView's window-level Escape interrupt listener.
    const onDocKey = (e: KeyboardEvent) => handleKeyDown(e);
    document.addEventListener("keydown", onDocKey);
    return () => document.removeEventListener("keydown", onDocKey);
  }, [handleKeyDown]);

  const step = (delta: number) => {
    const next = attachments[index + delta];
    if (next) selectWithFlush(next.id);
  };

  const handleBackdropPointerDown = (e: React.PointerEvent) => {
    if (e.target === e.currentTarget) {
      suppressBackdropClickRef.current = false;
    }
    hadOverlayAtDown.current = innerOverlayOpen || menu !== null || hasExternalOverlay();
    didSwipe.current = false;
    if (e.pointerType !== "mouse" && many && !editActive) {
      swipeStart.current = { x: e.clientX, y: e.clientY };
    } else {
      swipeStart.current = null;
    }
  };

  const handleBackdropPointerUp = (e: React.PointerEvent) => {
    const start = swipeStart.current;
    swipeStart.current = null;
    if (!start || !many || closing || editActive) return;
    const dx = e.clientX - start.x;
    const dy = e.clientY - start.y;
    if (Math.abs(dx) > 48 && Math.abs(dy) < 72) {
      didSwipe.current = true;
      step(dx < 0 ? 1 : -1);
    }
  };

  const handleBackdropClick = (e: React.MouseEvent) => {
    if (closing || e.target !== e.currentTarget) return;
    if (suppressBackdropClickRef.current || editor.dragging) {
      suppressBackdropClickRef.current = false;
      return;
    }
    if (didSwipe.current) {
      didSwipe.current = false;
      return;
    }
    if (hadOverlayAtDown.current) {
      hadOverlayAtDown.current = false;
      return;
    }
    closeWithFlush();
  };

  const editOff = !editor.img || closing;
  const showSideNav = many && !closing && !(editActive && narrow);

  const toolButtons = active && (
    <div
      role="toolbar"
      aria-label={t("chat-composeroverlays.image_tools_label")}
      className="flex shrink-0 items-center gap-1 rounded-full p-1"
      style={{ backgroundColor: "rgba(255,255,255,0.1)", boxShadow: "inset 0 0 0 1px rgba(255,255,255,0.12)" }}
    >
      <ToolButton
        testId="composer-image-tool-box"
        icon="square"
        label={t("chat-imageeditor.box")}
        words={!phone}
        active={tool === "box"}
        disabled={closing}
        compact
        keepFocus
        onClick={() => toggleTool("box")}
      />
      <ToolButton
        testId="composer-image-tool-pen"
        icon="write"
        label={t("chat-imageeditor.pen")}
        words={!phone}
        active={tool === "pen"}
        disabled={closing}
        compact
        keepFocus
        onClick={() => toggleTool("pen")}
      />
      <ToolButton
        testId="composer-image-tool-crop"
        icon="crop"
        label={t("chat-imageeditor.crop")}
        words={!phone}
        active={tool === "crop"}
        disabled={closing}
        compact
        keepFocus
        onClick={() => toggleTool("crop")}
      />
    </div>
  );

  const historyAndClose = (
    <div className="flex shrink-0 items-center gap-1 justify-self-end">
      {active && (
        <>
          <BarIcon
            testId="composer-image-undo"
            icon="undo"
            label={t("chat-imageeditor.undo")}
            disabled={editOff || !editor.history.past.length}
            keepFocus
            compact={phone}
            onClick={editor.stepBack}
          />
          <BarIcon
            testId="composer-image-redo"
            icon="redo"
            label={t("chat-imageeditor.redo")}
            disabled={editOff || !editor.history.future.length}
            keepFocus
            compact={phone}
            onClick={editor.stepForward}
          />
        </>
      )}
      <button
        type="button"
        data-testid="composer-image-close"
        aria-label={t("chat-composeroverlays.image_close")}
        title={t("chat-composeroverlays.image_close")}
        onMouseDown={(e) => e.preventDefault()}
        onClick={closeWithFlush}
        className={cx(
          "touch-hit flex shrink-0 items-center justify-center rounded-full bg-white/15 text-white transition-colors hover:bg-white/25",
          phone ? "size-8" : "size-9",
        )}
      >
        <Icon name="x" size={15} stroke={2.2} />
      </button>
    </div>
  );

  return (
    <div
      ref={rootRef}
      role="region"
      aria-label={t("chat-composeroverlays.image_stage_label")}
      data-composer-image-stage={closing ? "closing" : "open"}
      data-composer-image-tool={editActive ? tool : "preview"}
      className={cx("dark fixed inset-x-0 flex flex-col select-none text-white", closing && "pointer-events-none")}
      style={{ top: "var(--kb-top)", height: "var(--kb-h)", zIndex: 35 }}
      onKeyDown={handleKeyDown}
      {...drag}
    >
      {/* Dimmed frosted backdrop */}
      <div
        ref={backdropRef}
        data-testid="composer-image-backdrop"
        onPointerDown={handleBackdropPointerDown}
        onClick={handleBackdropClick}
        className={cx("absolute inset-0 backdrop-blur-md", dragging && "ring-2 ring-inset ring-accent")}
        style={{ backgroundColor: "rgba(0,0,0,0.8)" }}
      />

      {/* Top toolbar: image name + counter, inline edit tools (Box/Pen/Crop), Undo/Redo, and Close */}
      <div
        ref={headerRef}
        className={cx(
          "relative z-10 shrink-0 items-center gap-2 px-3 pb-2 sm:px-5",
          phone ? "flex justify-between" : "grid grid-cols-[1fr_auto_1fr]",
        )}
        style={{ paddingTop: "max(12px, env(safe-area-inset-top))" }}
      >
        <div className="flex min-w-0 items-center gap-2">
          {active && (
            <span data-testid="composer-image-name" title={active.name} className="min-w-0 truncate text-meta font-medium text-white">
              {active.name}
            </span>
          )}
          {many && (
            <span
              data-testid="composer-image-counter"
              aria-live="polite"
              className="shrink-0 rounded-full bg-white/15 px-2.5 py-0.5 text-label font-medium tabular-nums whitespace-nowrap text-white"
            >
              {index + 1} / {attachments.length}
            </span>
          )}
        </div>

        {phone ? (
          <div className="flex shrink-0 items-center gap-1">
            {toolButtons}
            {historyAndClose}
          </div>
        ) : (
          <>
            {toolButtons}
            {historyAndClose}
          </>
        )}
      </div>

      {/* Upper enlarged image stage, dynamically padded above the live composer */}
      <div
        ref={stageRef}
        data-testid="composer-image-stage-area"
        onPointerDown={handleBackdropPointerDown}
        onPointerUp={handleBackdropPointerUp}
        onPointerCancel={() => {
          swipeStart.current = null;
        }}
        onClick={handleBackdropClick}
        className="relative z-10 flex min-h-0 flex-1 flex-col items-center justify-center px-4 pt-1 sm:px-16"
        style={{ paddingBottom: "calc(var(--composer-stage-h, 152px) + max(12px, env(safe-area-inset-bottom)) + 16px)" }}
      >
        <div
          ref={editor.areaRef}
          onPointerDown={handleBackdropPointerDown}
          onClick={handleBackdropClick}
          className="relative flex size-full min-h-0 flex-1 items-center justify-center"
        >
          {active && !editActive && (
            <img
              ref={imgRef}
              data-testid="composer-image-preview"
              src={active.url}
              alt={active.name}
              draggable={false}
              onLoad={playOpenImageFlip}
              className="max-h-full max-w-full rounded-card object-contain shadow-overlay"
              style={{
                boxShadow: "0 0 0 1px rgba(255,255,255,0.12), var(--shadow-overlay)",
                ...(pendingActive && editor.img ? { opacity: 0 } : {}),
              }}
            />
          )}

          {showCanvas && (
            <canvas
              ref={editor.canvasRef}
              data-testid={editActive ? "composer-image-canvas" : undefined}
              className={cx("absolute inset-0 size-full", !editActive && "pointer-events-none", editActive && tool !== "crop" && "cursor-crosshair")}
              style={{
                touchAction: editActive ? "none" : undefined,
                cursor: editActive && tool === "crop" ? (editor.cropping ? handleCursor(editor.cropping.handle) : editor.cursor) : undefined,
              }}
              onPointerDown={editActive ? editor.onPointerDown : undefined}
              onPointerMove={editActive ? editor.onPointerMove : undefined}
              onPointerUp={editActive ? editor.onPointerUp : undefined}
              onPointerCancel={editActive ? editor.onPointerCancel : undefined}
            />
          )}

          {editActive && !editor.img && (
            <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
              <Spinner />
            </div>
          )}

          {showSideNav && (
            <>
              <button
                type="button"
                data-testid="composer-image-prev"
                aria-label={t("chat-composeroverlays.image_previous")}
                title={t("chat-composeroverlays.image_previous")}
                disabled={index <= 0}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => step(-1)}
                className="touch-hit absolute top-1/2 flex size-9 -translate-y-1/2 items-center justify-center rounded-full bg-white/15 text-white backdrop-blur-sm transition-colors hover:bg-white/25 disabled:pointer-events-none disabled:opacity-0"
                style={narrow ? { left: 6, backgroundColor: "rgba(0,0,0,0.5)", boxShadow: "inset 0 0 0 1px rgba(255,255,255,0.2)" } : { left: -44 }}
              >
                <Icon name="chevronLeft" size={18} stroke={2.2} />
              </button>
              <button
                type="button"
                data-testid="composer-image-next"
                aria-label={t("chat-composeroverlays.image_next")}
                title={t("chat-composeroverlays.image_next")}
                disabled={index >= attachments.length - 1}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => step(1)}
                className="touch-hit absolute top-1/2 flex size-9 -translate-y-1/2 items-center justify-center rounded-full bg-white/15 text-white backdrop-blur-sm transition-colors hover:bg-white/25 disabled:pointer-events-none disabled:opacity-0"
                style={narrow ? { right: 6, backgroundColor: "rgba(0,0,0,0.5)", boxShadow: "inset 0 0 0 1px rgba(255,255,255,0.2)" } : { right: -44 }}
              >
                <Icon name="chevronRight" size={18} stroke={2.2} />
              </button>
            </>
          )}
        </div>

        {/* Contextual sub-bar strictly between the image canvas and the elevated composer */}
        {editActive && (
          <div
            role="toolbar"
            aria-label={t("chat-composeroverlays.image_tool_options_label")}
            data-testid="composer-image-subbar"
            onClick={(e) => e.stopPropagation()}
            className="mt-2 flex max-w-full shrink-0 items-center justify-center gap-2 rounded-full px-3 py-1.5 shadow-overlay backdrop-blur-md"
            style={{
              backgroundColor: "rgba(24,24,27,0.84)",
              boxShadow: "0 0 0 1px rgba(255,255,255,0.14), var(--shadow-overlay)",
            }}
          >
            {tool === "crop" ? (
              <div ref={cropRowRef} className="flex min-w-0 items-center gap-2 overflow-hidden">
                <ToolButton
                  testId="composer-image-rotate"
                  icon="rotateLeft"
                  label={t("chat-imageeditor.rotate")}
                  words={cropWords}
                  disabled={editOff}
                  compact
                  keepFocus
                  onClick={() => editor.change(turnLeft)}
                />
                <ToolButton
                  testId="composer-image-ratio"
                  icon="ratio"
                  label={t("chat-imageeditor.aspect_ratio")}
                  text={shapeName(editor.edits.shape)}
                  words={cropWords}
                  disabled={editOff}
                  compact
                  menu={menu?.kind === "shape"}
                  onClick={(el) => setMenu((m) => (m?.kind === "shape" ? null : { kind: "shape", anchor: el }))}
                />
                <ToolButton
                  testId="composer-image-reset"
                  label={t("chat-imageeditor.reset")}
                  words
                  disabled={editOff || isFramedAsIs(editor.edits)}
                  compact
                  keepFocus
                  onClick={() => editor.change(resetFrame)}
                />
              </div>
            ) : (
              <>
                <div className={cx("flex items-center", phone ? "gap-2" : "gap-3")}>
                  <InkSwatches ink={ink} onSelect={setInk} compact={phone} keepFocus />
                </div>
                <span aria-hidden className="shrink-0" style={{ width: 1, height: 18, backgroundColor: "rgba(255,255,255,0.18)" }} />
                <IconAction
                  testId="composer-image-width"
                  icon={<LineSample width={lineWidth} />}
                  label={t("chat-imageeditor.line_width")}
                  value={widthName(lineWidth)}
                  menu={menu?.kind === "width"}
                  disabled={editOff}
                  compact
                  onClick={(el) => setMenu((m) => (m?.kind === "width" ? null : { kind: "width", anchor: el }))}
                />
                <IconAction
                  testId="composer-image-clear"
                  icon="trash"
                  label={t("chat-imageeditor.clear")}
                  disabled={editOff || !editor.edits.marks.length}
                  compact
                  keepFocus
                  onClick={() => editor.change((e) => ({ ...e, marks: [] }))}
                />
              </>
            )}
          </div>
        )}
      </div>

      {/* Dark portal container without stacking-context z-index so Menu (fixed z-50) always renders above stage (z-35) and elevated composer (z-36). */}
      {createPortal(<div ref={setMenuHost} className="dark" />, document.body)}
      <EditorMenu
        menu={menu}
        lineWidth={lineWidth}
        shape={editor.edits.shape}
        container={menuHost}
        side="top"
        onSelectWidth={setLineWidth}
        onSelectShape={(s) => editor.change((e) => reshape(e, s, editor.natural))}
        onClose={() => setMenu(null)}
      />
    </div>
  );
}
