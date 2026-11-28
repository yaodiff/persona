// Shared canvas rendering, pointer interaction, encoding, and toolbar primitives for ImageEditor and
// ComposerImageStage (#17173). Stays strictly inside lazy editor / overlay chunks (never imported on the first screen).
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type MouseEventHandler, type ReactNode, type RefObject } from "react";
import { t } from "../../i18n";
import { imagePayload, MAX_IMAGE } from "../../lib/attachments";
import type { DraftImage } from "../../lib/drafts";
import {
  boxRect,
  CROP_SHAPES,
  dragCrop,
  handleAt,
  handleCursor,
  IMAGE_EDIT,
  INKS,
  isEmpty,
  keepMark,
  layout,
  markWidth,
  noEdits,
  noHistory,
  record,
  redo,
  renderPlan,
  sameEdits,
  screenToPixel,
  shapeRatio,
  turnMatrix,
  turnedSize,
  undo,
  unturnPoint,
  type CropHandle,
  type CropShape,
  type EditState,
  type ImageEdits,
  type Ink,
  type Layout,
  type Mark,
  type Point,
  type Rect,
  type Size,
} from "../../lib/image-edit";
import { cx, Menu, type MenuItem } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";

export type EditorTool = "box" | "pen" | "crop";
export type EditorMenuKind = "width" | "shape";
export type EditorMenuState = { kind: EditorMenuKind; anchor: HTMLElement } | null;
export type RenderEditedResult = { ok: true; image: DraftImage } | { ok: false; reason: "too_large" | "failed" };

export function inkName(ink: Ink): string {
  switch (ink) {
    case "red":
      return t("chat-imageeditor.color_red");
    case "yellow":
      return t("chat-imageeditor.color_yellow");
    case "green":
      return t("chat-imageeditor.color_green");
    case "blue":
      return t("chat-imageeditor.color_blue");
    case "white":
      return t("chat-imageeditor.color_white");
    case "black":
      return t("chat-imageeditor.color_black");
  }
}

export function widthName(width: number): string {
  if (width === 0) return t("chat-imageeditor.width_thin");
  if (width === 2) return t("chat-imageeditor.width_thick");
  return t("chat-imageeditor.width_medium");
}

export function shapeName(shape: CropShape): string {
  switch (shape) {
    case "free":
      return t("chat-imageeditor.shape_free");
    case "original":
      return t("chat-imageeditor.shape_original");
    case "square":
      return t("chat-imageeditor.shape_square");
    default:
      return shape;
  }
}

/** A control's name with the value it shows, in the language's own form ("Line width: Medium", "粗细：中"). */
export function named(label: string, value: string): string {
  return t("chat-imageeditor.named_value", { label, value });
}

/** The inks' colors (`--mark-*`, the same in light and dark), read once for a drawing rather than once a mark. */
export function inkColors(): Record<Ink, string> {
  const css = getComputedStyle(document.documentElement);
  return Object.fromEntries(INKS.map((ink) => [ink, css.getPropertyValue(`--mark-${ink}`).trim() || ink])) as Record<Ink, string>;
}

/** A mark's line, in whatever coordinates the context is set to (the source's pixels). */
export function strokeMark(ctx: CanvasRenderingContext2D, m: Mark, colors: Record<Ink, string>) {
  const pts = m.points;
  if (pts.length < 2) return;
  ctx.strokeStyle = colors[m.ink];
  ctx.lineWidth = m.width;
  ctx.beginPath();
  if (m.tool === "box") {
    const r = boxRect(pts[0], pts[pts.length - 1]);
    ctx.rect(r.x, r.y, r.width, r.height);
  } else {
    ctx.moveTo(pts[0].x, pts[0].y);
    for (const p of pts.slice(1)) ctx.lineTo(p.x, p.y);
  }
  ctx.stroke();
}

/** The crop's frame while cropping: the cropped-out parts of the picture dimmed, a line round the crop, its corners
 *  marked, and the rule-of-thirds grid while it is dragged. */
export function drawCropFrame(ctx: CanvasRenderingContext2D, picture: Rect, f: Rect, dragging: boolean) {
  ctx.fillStyle = `rgb(0 0 0 / ${IMAGE_EDIT.cropDim})`;
  ctx.beginPath();
  ctx.rect(picture.x, picture.y, picture.width, picture.height);
  ctx.rect(f.x, f.y, f.width, f.height);
  ctx.fill("evenodd");
  ctx.strokeStyle = "#fff";
  ctx.lineWidth = IMAGE_EDIT.cropLine;
  ctx.strokeRect(f.x, f.y, f.width, f.height);
  if (dragging) {
    ctx.strokeStyle = `rgb(255 255 255 / ${IMAGE_EDIT.cropThirdsOpacity})`;
    ctx.beginPath();
    for (const i of [1, 2]) {
      const x = f.x + (f.width * i) / 3,
        y = f.y + (f.height * i) / 3;
      ctx.moveTo(x, f.y);
      ctx.lineTo(x, f.y + f.height);
      ctx.moveTo(f.x, y);
      ctx.lineTo(f.x + f.width, y);
    }
    ctx.stroke();
  }
  const c = Math.min(IMAGE_EDIT.cropCorner, f.width / 2, f.height / 2);
  ctx.strokeStyle = "#fff";
  ctx.lineWidth = IMAGE_EDIT.cropCornerWidth;
  ctx.lineCap = "square";
  ctx.beginPath();
  for (const [x, y, dx, dy] of [
    [f.x, f.y, c, c],
    [f.x + f.width, f.y, -c, c],
    [f.x, f.y + f.height, c, -c],
    [f.x + f.width, f.y + f.height, -c, -c],
  ]) {
    ctx.moveTo(x + dx, y);
    ctx.lineTo(x, y);
    ctx.lineTo(x, y + dy);
  }
  ctx.stroke();
}

/** Paints the source picture, turns, crop clip, marks, and optional crop frame onto a DPR-scaled canvas. */
export function paintEditorCanvas(
  c: HTMLCanvasElement,
  img: HTMLImageElement,
  edits: ImageEdits,
  natural: Size,
  size: Size,
  l: Layout,
  opts: { current: Mark | null; cropMode: boolean; cropping: boolean },
) {
  if (!size.width || !size.height) return;
  const dpr = devicePixelRatio || 1;
  c.width = Math.round(size.width * dpr);
  c.height = Math.round(size.height * dpr);
  const ctx = c.getContext("2d");
  if (!ctx) return; // no 2D canvas (lost, or none at all): nothing to draw on
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, size.width, size.height);
  ctx.save();
  ctx.beginPath();
  ctx.rect(l.visible.x, l.visible.y, l.visible.width, l.visible.height);
  ctx.clip();
  ctx.translate(l.picture.x, l.picture.y);
  ctx.scale(l.scale, l.scale);
  ctx.transform(...turnMatrix(edits, natural));
  ctx.drawImage(img, 0, 0, natural.width, natural.height);
  ctx.lineCap = "round";
  ctx.lineJoin = "round";
  const colors = inkColors();
  for (const m of opts.current ? [...edits.marks, opts.current] : edits.marks) strokeMark(ctx, m, colors);
  ctx.restore();
  if (opts.cropMode) drawCropFrame(ctx, l.picture, l.crop, opts.cropping);
}

export function encodeCanvas(c: HTMLCanvasElement, type: string, quality?: number): Promise<Blob | null> {
  return new Promise((ok) => c.toBlob(ok, type, quality));
}

/** Keeps an attachment's own name when edited inline, switching only the extension when PNG falls back to JPEG. */
export function editedFileName(originalName: string | undefined, mime: string): string {
  if (!originalName) return mime === "image/jpeg" ? "image.jpg" : "image.png";
  if (mime === "image/jpeg" && /\.(png|webp|gif|bmp)$/i.test(originalName)) {
    return originalName.replace(/\.(png|webp|gif|bmp)$/i, ".jpg");
  }
  return originalName;
}

/** Renders `edits` over `img` at `natural` resolution into a DraftImage under MAX_IMAGE (PNG, falling back to JPEG). */
export async function renderEditedImage(
  img: HTMLImageElement,
  src: string,
  edits: ImageEdits,
  natural: Size,
  originalName?: string,
): Promise<RenderEditedResult> {
  try {
    if (isEmpty(edits)) {
      // nothing done (or all of it undone): the picture as it is
      const blob = await (await fetch(src)).blob();
      const name = originalName || "image.png";
      return { ok: true, image: { name, ...(await imagePayload(new File([blob], name, { type: blob.type || "image/png" }))) } };
    }
    const plan = renderPlan(edits, natural);
    if (!plan) return { ok: false, reason: "failed" };
    const out = document.createElement("canvas");
    out.width = plan.width;
    out.height = plan.height;
    const ctx = out.getContext("2d");
    if (!ctx) return { ok: false, reason: "failed" };
    // a transparent picture sent as JPEG would go black where it is clear: white under it, as iOS and Android do
    const colors = inkColors();
    ctx.fillStyle = colors.white;
    ctx.fillRect(0, 0, plan.width, plan.height);
    ctx.setTransform(...plan.matrix);
    ctx.imageSmoothingQuality = "high";
    ctx.drawImage(img, 0, 0, natural.width, natural.height);
    ctx.lineCap = "round";
    ctx.lineJoin = "round";
    for (const m of edits.marks) strokeMark(ctx, m, colors);
    const blob = (await encodeCanvas(out, "image/png")) ?? null;
    // a big screenshot as PNG can pass the 8 MB a message takes: JPEG then
    const fitting = blob && blob.size <= MAX_IMAGE ? blob : await encodeCanvas(out, "image/jpeg", 0.9);
    if (!fitting || fitting.size > MAX_IMAGE) return { ok: false, reason: "too_large" };
    const name = editedFileName(originalName, fitting.type);
    return { ok: true, image: { name, ...(await imagePayload(new File([fitting], name, { type: fitting.type }))) } };
  } catch {
    return { ok: false, reason: "failed" };
  }
}

/** Whether a row's buttons fit with their words (iOS ViewThatFits): measured with them, and with them again once the
 *  row is as wide as it needed. */
export function useFitsWords(ref: RefObject<HTMLElement | null>, key: unknown): boolean {
  const [words, setWords] = useState(true);
  const needed = useRef(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const check = () => {
      if (words) {
        if (el.scrollWidth > el.clientWidth + 0.5) {
          needed.current = el.scrollWidth;
          setWords(false);
        }
      } else if (el.clientWidth >= needed.current) setWords(true);
    };
    check();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(check);
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref, words, key]);
  return words;
}

export interface UseImageEditorCoreOptions {
  /** The unedited source picture URL (never changed). */
  src: string;
  /** Identity of the picture being edited; changing it resets active drags and syncs `doc` from `start`. */
  stateKey: string;
  /** Initial / saved edits and history for `stateKey`. */
  start?: EditState;
  /** Active tool, or null in preview-only mode. */
  tool: EditorTool | null;
  ink: Ink;
  lineWidth: number;
  disabled?: boolean;
  /** True when the canvas element is mounted and should be sized and painted. */
  canvasActive: boolean;
  /** Optional shared cache of decoded source images so toggling tools or switching images is instant. */
  decodedCache?: RefObject<Map<string, HTMLImageElement>>;
  /** Called synchronously after any discrete committed edit (mark finished, crop released, turn, reshape, reset, clear, undo, redo). */
  onCommit?: (state: EditState, img: HTMLImageElement, natural: Size) => void;
  /** Called if decoding `src` fails. */
  onDecodeError?: () => void;
  /** Called when a pointer drag begins on the canvas (stroke or crop handle). */
  onDragStart?: () => void;
}

export function useImageEditorCore({
  src,
  stateKey,
  start,
  tool,
  ink,
  lineWidth,
  disabled = false,
  canvasActive,
  decodedCache,
  onCommit,
  onDecodeError,
  onDragStart,
}: UseImageEditorCoreOptions) {
  const [loaded, setLoaded] = useState<{ src: string; img: HTMLImageElement } | null>(null);
  const img = loaded && loaded.src === src ? loaded.img : null;
  const [doc, setDoc] = useState<EditState>(() => start ?? { edits: noEdits(), history: noHistory() });
  const [shownKey, setShownKey] = useState(stateKey);
  const [current, setCurrent] = useState<Mark | null>(null);
  const [cropping, setCropping] = useState<{ handle: CropHandle; from: Rect; at: Point } | null>(null);
  const [cursor, setCursor] = useState("default");
  const [size, setSize] = useState<Size>({ width: 0, height: 0 });
  const areaRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);

  if (shownKey !== stateKey) {
    setShownKey(stateKey);
    setDoc(start ?? { edits: noEdits(), history: noHistory() });
    setCurrent(null);
    setCropping(null);
    setCursor("default");
  }

  const docRef = useRef(doc);
  const currentRef = useRef(current);
  const croppingRef = useRef(cropping);
  const onCommitRef = useRef(onCommit);
  const onDecodeErrorRef = useRef(onDecodeError);
  const onDragStartRef = useRef(onDragStart);

  useLayoutEffect(() => {
    docRef.current = doc;
    currentRef.current = current;
    croppingRef.current = cropping;
    onCommitRef.current = onCommit;
    onDecodeErrorRef.current = onDecodeError;
    onDragStartRef.current = onDragStart;
  });

  useEffect(() => {
    if (!src) {
      setLoaded(null);
      return;
    }
    const cached = decodedCache?.current.get(src);
    if (cached) {
      setLoaded({ src, img: cached });
      return;
    }
    let live = true;
    const el = new Image();
    el.src = src;
    el.decode().then(
      () => {
        decodedCache?.current.set(src, el);
        if (live) setLoaded({ src, img: el });
      },
      () => {
        if (!live) return;
        onDecodeErrorRef.current?.();
      },
    );
    return () => {
      live = false;
    };
  }, [src, decodedCache]);

  useLayoutEffect(() => {
    const el = areaRef.current;
    if (!el || !canvasActive) return;
    const sync = () => setSize((prev) => (prev.width === el.clientWidth && prev.height === el.clientHeight ? prev : { width: el.clientWidth, height: el.clientHeight }));
    sync();
    const ro = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(sync);
    ro?.observe(el);
    window.addEventListener("resize", sync);
    return () => {
      ro?.disconnect();
      window.removeEventListener("resize", sync);
    };
  }, [canvasActive, stateKey]);

  const natural: Size = img ? { width: img.naturalWidth, height: img.naturalHeight } : { width: 0, height: 0 };
  const cropMode = tool === "crop";
  const l: Layout = layout(doc.edits, natural, size, cropMode);

  // Paint the picture, turns, crop clip, marks, and optional crop frame at the screen's DPR.
  useEffect(() => {
    const c = canvasRef.current;
    if (!canvasActive || !c || !img || !size.width || !size.height) return;
    paintEditorCanvas(c, img, doc.edits, natural, size, l, {
      current,
      cropMode,
      cropping: cropping !== null,
    });
  });

  const emitCommit = useCallback(
    (next: EditState) => {
      if (img) onCommitRef.current?.(next, img, { width: img.naturalWidth, height: img.naturalHeight });
    },
    [img],
  );

  /** One step of the history: the edits changed by `make`, the ones before kept for Undo. */
  const change = useCallback(
    (make: (e: ImageEdits) => ImageEdits) => {
      const cur = docRef.current;
      const nextEdits = make(cur.edits);
      if (sameEdits(nextEdits, cur.edits)) return;
      const next: EditState = { edits: nextEdits, history: record(cur.history, cur.edits) };
      docRef.current = next;
      setDoc(next);
      emitCommit(next);
    },
    [emitCommit],
  );

  const stepBack = useCallback(() => {
    const cur = docRef.current;
    const s = undo(cur.history, cur.edits);
    if (!s) return;
    const next: EditState = { edits: s.edits, history: s.history };
    docRef.current = next;
    setDoc(next);
    emitCommit(next);
  }, [emitCommit]);

  const stepForward = useCallback(() => {
    const cur = docRef.current;
    const s = redo(cur.history, cur.edits);
    if (!s) return;
    const next: EditState = { edits: s.edits, history: s.history };
    docRef.current = next;
    setDoc(next);
    emitCommit(next);
  }, [emitCommit]);

  const cancelDrag = useCallback(() => {
    const activeCrop = croppingRef.current;
    if (activeCrop) {
      const restored: EditState = { ...docRef.current, edits: { ...docRef.current.edits, crop: activeCrop.from } };
      docRef.current = restored;
      setDoc(restored);
      croppingRef.current = null;
      setCropping(null);
    }
    if (currentRef.current) {
      currentRef.current = null;
      setCurrent(null);
    }
  }, []);

  const local = (e: React.PointerEvent<HTMLCanvasElement>): Point => {
    const r = canvasRef.current!.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };

  const onPointerDown = (e: React.PointerEvent<HTMLCanvasElement>) => {
    if (!tool || !img || e.button !== 0 || disabled) return;
    const p = local(e);
    if (tool === "crop") {
      const handle = handleAt(p, l.crop);
      if (!handle) return;
      e.currentTarget.setPointerCapture?.(e.pointerId);
      const nextCrop = { handle, from: docRef.current.edits.crop, at: p };
      croppingRef.current = nextCrop;
      setCropping(nextCrop);
      onDragStartRef.current?.();
      return;
    }
    e.currentTarget.setPointerCapture?.(e.pointerId);
    const width = markWidth(IMAGE_EDIT.lineWidths[lineWidth], l.scale);
    const mark: Mark = { tool, ink, width, points: [unturnPoint(docRef.current.edits, screenToPixel(l, p), natural)] };
    currentRef.current = mark;
    setCurrent(mark);
    onDragStartRef.current?.();
  };

  const onPointerMove = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const p = local(e);
    const activeCrop = croppingRef.current;
    if (activeCrop) {
      if (l.picture.width <= 0 || l.picture.height <= 0) return;
      const delta = { x: (p.x - activeCrop.at.x) / l.picture.width, y: (p.y - activeCrop.at.y) / l.picture.height };
      const minimum = { width: IMAGE_EDIT.cropMinimum / l.picture.width, height: IMAGE_EDIT.cropMinimum / l.picture.height };
      const r = shapeRatio(docRef.current.edits.shape, turnedSize(docRef.current.edits, natural));
      const crop = dragCrop(activeCrop.from, activeCrop.handle, delta, minimum, r === null ? null : (r * l.picture.height) / l.picture.width);
      const next: EditState = { ...docRef.current, edits: { ...docRef.current.edits, crop } };
      docRef.current = next;
      setDoc(next);
      return;
    }
    const activeMark = currentRef.current;
    if (activeMark) {
      const q = unturnPoint(docRef.current.edits, screenToPixel(l, p), natural);
      const nextMark: Mark = { ...activeMark, points: activeMark.tool === "box" ? [activeMark.points[0], q] : [...activeMark.points, q] };
      currentRef.current = nextMark;
      setCurrent(nextMark);
      return;
    }
    if (tool === "crop" && e.pointerType === "mouse") setCursor(handleCursor(handleAt(p, l.crop)));
  };

  const onPointerUp = () => {
    const activeCrop = croppingRef.current;
    if (activeCrop) {
      const { from } = activeCrop;
      croppingRef.current = null;
      setCropping(null);
      // the crop let go is one step: back to where it was before this drag
      const cur = docRef.current;
      if (!sameEdits({ ...cur.edits, crop: from }, cur.edits)) {
        const next: EditState = { edits: cur.edits, history: record(cur.history, { ...cur.edits, crop: from }) };
        docRef.current = next;
        setDoc(next);
        emitCommit(next);
      }
      return;
    }
    const activeMark = currentRef.current;
    currentRef.current = null;
    setCurrent(null);
    if (activeMark && keepMark(activeMark)) {
      change((e) => ({ ...e, marks: [...e.marks, activeMark] }));
    }
  };

  const onPointerCancel = () => {
    cancelDrag();
  };

  return {
    img,
    natural,
    doc,
    edits: doc.edits,
    history: doc.history,
    dragging: current !== null || cropping !== null,
    cropping,
    cursor,
    areaRef,
    canvasRef,
    change,
    stepBack,
    stepForward,
    cancelDrag,
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel,
  };
}

const prevent: MouseEventHandler<HTMLButtonElement> = (e) => e.preventDefault();

/** A tool, or an option of Crop: an icon and its word on one line (the icon alone when the row has no room for the
 *  words; `label` is then what a screen reader and the tooltip say). `text` stands in the word's place (the shape
 *  chosen). Pressed for the tool shown; a menu's button when `menu` is set. */
export function ToolButton({
  icon,
  label,
  text,
  words,
  active,
  disabled,
  menu,
  testId,
  keepFocus,
  compact,
  onClick,
}: {
  icon?: IconName;
  label: string;
  text?: string;
  words: boolean;
  active?: boolean;
  disabled?: boolean;
  menu?: boolean;
  testId?: string;
  keepFocus?: boolean;
  compact?: boolean;
  onClick: (el: HTMLElement) => void;
}) {
  const shown = text ?? label;
  // icon-only, or showing its value: named in full ("Aspect ratio: Freeform"), aloud and in the tooltip
  const said = text ? named(label, text) : label;
  const showWords = words || !icon;
  return (
    <button
      type="button"
      data-testid={testId}
      aria-label={text || !words ? said : undefined}
      title={text || !words ? said : undefined}
      aria-pressed={active}
      aria-haspopup={menu === undefined ? undefined : "menu"}
      aria-expanded={menu === undefined ? undefined : menu}
      disabled={disabled}
      onMouseDown={keepFocus && menu === undefined ? prevent : undefined}
      onClick={(e) => onClick(e.currentTarget)}
      className={cx(
        "touch-hit flex shrink-0 items-center gap-1.5 rounded-full text-meta font-medium whitespace-nowrap transition-[background-color,color,opacity] disabled:opacity-40",
        compact ? (showWords ? "h-8 px-3" : "size-8 justify-center") : showWords ? "h-9 px-4" : "h-9 w-9 justify-center",
        active ? "bg-white text-black" : "bg-white/15 text-white hover:bg-white/25",
      )}
    >
      {icon && <Icon name={icon} size={14} />}
      {showWords && <span>{shown}</span>}
    </button>
  );
}

/** A line as wide on screen as the pen draws at width `width` (an index of IMAGE_EDIT.lineWidths): what Line width shows
 *  for the one chosen, and each of its rows. */
export function LineSample({ width }: { width: number }) {
  return <span aria-hidden className="block w-4 rounded-full bg-current" style={{ height: IMAGE_EDIT.lineWidths[width] }} />;
}

export function IconAction({
  icon,
  label,
  value,
  menu,
  disabled,
  testId,
  keepFocus,
  compact,
  onClick,
}: {
  icon: IconName | ReactNode;
  label: string;
  value?: string;
  menu?: boolean;
  disabled: boolean;
  testId?: string;
  keepFocus?: boolean;
  compact?: boolean;
  onClick: (el: HTMLElement) => void;
}) {
  const said = value ? named(label, value) : label;
  return (
    <button
      type="button"
      data-testid={testId}
      aria-label={said}
      title={said}
      aria-haspopup={menu === undefined ? undefined : "menu"}
      aria-expanded={menu === undefined ? undefined : menu}
      disabled={disabled}
      onMouseDown={keepFocus && menu === undefined ? prevent : undefined}
      onClick={(e) => onClick(e.currentTarget)}
      className={cx(
        "touch-hit flex shrink-0 items-center justify-center rounded-full bg-white/15 text-white transition-colors hover:bg-white/25 disabled:opacity-40",
        compact ? "size-8" : "size-9",
      )}
    >
      {typeof icon === "string" ? <Icon name={icon} size={15} /> : icon}
    </button>
  );
}

/** Undo and Redo icon buttons on the dark chrome. */
export function BarIcon({
  icon,
  label,
  disabled,
  testId,
  keepFocus,
  compact,
  onClick,
}: {
  icon: IconName;
  label: string;
  disabled: boolean;
  testId?: string;
  keepFocus?: boolean;
  compact?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      data-testid={testId}
      aria-label={label}
      title={label}
      disabled={disabled}
      onMouseDown={keepFocus ? prevent : undefined}
      onClick={onClick}
      className={cx(
        "touch-hit flex shrink-0 items-center justify-center rounded-full text-white transition-colors hover:bg-white/15 disabled:opacity-35",
        compact ? "size-8" : "size-9",
      )}
    >
      <Icon name={icon} size={compact ? 16 : 17} />
    </button>
  );
}

/** The six markup color swatches shared by ImageEditor and inline ComposerImageStage. */
export function InkSwatches({
  ink,
  onSelect,
  compact,
  keepFocus,
}: {
  ink: Ink;
  onSelect: (ink: Ink) => void;
  compact?: boolean;
  keepFocus?: boolean;
}) {
  return (
    <>
      {INKS.map((c) => {
        const label = inkName(c);
        return (
          <button
            key={c}
            type="button"
            data-testid={`image-ink-${c}`}
            aria-label={label}
            title={label}
            aria-pressed={ink === c}
            onMouseDown={keepFocus ? prevent : undefined}
            onClick={() => onSelect(c)}
            className={cx(
              "touch-hit shrink-0 rounded-full transition-[box-shadow,transform] active:scale-90",
              compact ? "size-6" : "size-7",
              ink === c ? "ring-3 ring-white" : "ring-1 ring-white/40",
            )}
            style={{ background: `var(--mark-${c})` }}
          />
        );
      })}
    </>
  );
}

/** Shared line-width / aspect-ratio popover menu for ImageEditor and inline ComposerImageStage. */
export function EditorMenu({
  menu,
  lineWidth,
  shape,
  container,
  side = "top",
  onSelectWidth,
  onSelectShape,
  onClose,
}: {
  menu: EditorMenuState;
  lineWidth: number;
  shape: CropShape;
  container: Element | null;
  side?: "top" | "bottom";
  onSelectWidth: (width: number) => void;
  onSelectShape: (shape: CropShape) => void;
  onClose: () => void;
}) {
  if (!menu) return null;
  const items: MenuItem[] =
    menu.kind === "width"
      ? IMAGE_EDIT.lineWidths.map((_, i) => ({
          key: `w${i}`,
          label: widthName(i),
          lead: <LineSample width={i} />,
          radio: true,
          checked: lineWidth === i,
          onSelect: () => onSelectWidth(i),
        }))
      : CROP_SHAPES.map((s) => ({
          key: s,
          label: shapeName(s),
          radio: true,
          checked: shape === s,
          onSelect: () => onSelectShape(s),
        }));
  return (
    <Menu
      items={items}
      anchor={menu.anchor}
      container={container}
      side={side}
      align={menu.kind === "width" ? "right" : "left"}
      width={180}
      header={menu.kind === "width" ? t("chat-imageeditor.line_width") : t("chat-imageeditor.aspect_ratio")}
      onClose={onClose}
    />
  );
}
