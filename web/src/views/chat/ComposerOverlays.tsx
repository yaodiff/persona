import { useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { t } from "../../i18n";
import { base64File, releaseAttachments } from "../../lib/attachments";
import { useCloseOnBack } from "../../lib/back-close";
import { addFiles, MAX_FILES } from "../../lib/composer-files";
import type { Attachment } from "../../lib/composer-images";
import { setCommentImages, type CommentImagePicker, type DraftImage } from "../../lib/drafts";
import { composerEntryDrop, parseEntry } from "../../lib/entry-drag";
import { uuid } from "../../lib/format";
import { MAX_PICTURE_SOURCE, type ImageLimits } from "../../lib/image-limits";
import { enterSends, hasFinePointer, NARROW, useMedia } from "../../lib/viewport";
import { useComposition } from "../../lib/ime";
import { editorText, type InlineDocument, type InlineSelection } from "../../lib/inline-mentions";
import { Button, cx, Dialog, EASE, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { useModalFocus } from "../../ui/overlay";
import { ComposerField } from "./ComposerField";
import { ComposerImageStage, type ComposerImageStageProps } from "./ComposerImageStage";
import type { InlineMentionInputHandle } from "./InlineMentionInput";

/** A picked picture made ready for a message (scaled down as on the iPhone, lib/image-scale.ts), or null when
 *  the page cannot read it or bring it under the limits: it goes as a file. */
export async function fitDraftImage(file: File, limits: ImageLimits): Promise<DraftImage | null> {
  if (file.size > MAX_PICTURE_SOURCE) return null;
  const { fitPicture } = await import("../../lib/image-scale");
  const fit = await fitPicture(file, limits);
  return fit && { ...fit, name: file.name || "image.png" };
}

export async function readImage(file: File, limits: ImageLimits) {
  const image = await fitDraftImage(file, limits);
  return image && { ...image, id: uuid(), url: URL.createObjectURL(file), original: file };
}

type ImageOverflow = "count" | "bytes";
interface ImageTarget {
  draftKey: string;
  machineId?: string;
  uploadDest?: string;
  limits: () => ImageLimits;
}

export function showImageRoomError(why: ImageOverflow, limits: ImageLimits) {
  toast(why === "count" ? t("chat-composeroverlays.too_many_images", { max: limits.per_message }) : t("chat-composeroverlays.images_too_large"), "error");
}

// Pictures past the message limits (#12973) can ride as files on the session's computer.
function spillImageFiles({ draftKey, machineId, uploadDest, limits }: ImageTarget, files: File[], why: ImageOverflow) {
  if (!files.length) return;
  if (!machineId) { showImageRoomError(why, limits()); return; }
  const { tooMany } = addFiles(draftKey, machineId, files, uploadDest);
  const went = files.length - tooMany;
  if (went > 0) toast(why === "count" ? t("chat-composeroverlays.images_as_files", { max: limits().per_message, count: went }) : t("chat-composeroverlays.images_bytes_as_files", { count: went }));
  if (tooMany) toast(t("chat-composeroverlays.too_many_files", { max: MAX_FILES }), "error");
}

// A burst of forwarded pictures gets one notice per limit and conversation.
const forwarded = new WeakMap<ImageTarget, Record<ImageOverflow, File[]>>();
export function spillDraftImage(target: ImageTarget, { name, mime, data }: DraftImage, why: ImageOverflow, current: () => boolean) {
  let batch = forwarded.get(target);
  if (!batch) {
    const pending: Record<ImageOverflow, File[]> = { count: [], bytes: [] };
    forwarded.set(target, pending);
    queueMicrotask(() => {
      forwarded.delete(target);
      if (!current()) return;
      spillImageFiles(target, pending.count, "count");
      spillImageFiles(target, pending.bytes, "bytes");
    });
    batch = pending;
  }
  batch[why].push(base64File(name, mime, data));
}

export async function addPickedFiles(picked: File[], target: ImageTarget, {
  current, reading, roomForImage, attach,
}: {
  current: () => boolean;
  reading: { current: number };
  roomForImage: (size: number) => "ok" | ImageOverflow;
  attach: (image: Attachment) => void;
}) {
  const { draftKey, machineId, uploadDest, limits } = target;
  const spill = { count: [] as File[], bytes: [] as File[] };
  const others: File[] = [];
  for (const file of picked) {
    if (!current()) return;
    if (!file.type.startsWith("image/")) {
      if (machineId) others.push(file);
      else toast(t("chat-composeroverlays.images_only"), "error");
      continue;
    }
    if (roomForImage(0) === "count") {
      spill.count.push(file);
      continue;
    }
    reading.current++;
    let image: Attachment | null;
    try {
      image = await readImage(file, limits());
    } finally {
      reading.current--;
    }
    if (!current()) {
      if (image) releaseAttachments([image]);
      return;
    }
    if (!image) {
      if (machineId) others.push(file);
      else toast(t("chat-composeroverlays.image_unreadable", { name: file.name }), "error");
      continue;
    }
    const room = roomForImage(image.data.length);
    if (room === "ok") attach(image);
    else {
      releaseAttachments([image]);
      spill[room].push(file);
    }
  }
  spillImageFiles(target, spill.count, "count");
  spillImageFiles(target, spill.bytes, "bytes");
  if (machineId && others.length) {
    const result = addFiles(draftKey, machineId, others, uploadDest);
    if (result.tooMany) toast(t("chat-composeroverlays.too_many_files", { max: MAX_FILES }), "error");
    if (result.tooBig.length) toast(t("chat-composer.file_too_large"), "error");
  }
}

export function resolveEntryDrop(raw: string, target: { mid?: string; cwd?: string }, onPaste: (path: string) => void) {
  const entry = parseEntry(raw);
  if (!entry) return;
  const drop = composerEntryDrop(entry, target);
  if (drop.action === "refuse") toast(drop.toast);
  else onPaste(drop.text.slice(1, -1));
}

export async function runCommentImagePicker({
  target,
  files,
  signal,
  draftKey,
  limits,
  roomForImage,
  imageRoomError,
  commentReads,
  releaseLoader,
  current,
}: {
  target: Parameters<CommentImagePicker>[0];
  files: File[];
  signal: AbortSignal;
  draftKey: string;
  limits: () => ImageLimits;
  roomForImage: (size: number) => "ok" | "count" | "bytes";
  imageRoomError: (why: "count" | "bytes") => void;
  commentReads: Set<object>;
  releaseLoader: () => void;
  current: (id: string | undefined) => boolean;
}) {
  releaseLoader();
  let id = typeof target === "string" ? target : undefined;
  for (const file of files) {
    if (!current(id)) return;
    if (!file.type.startsWith("image/")) {
      toast(t("chat-composeroverlays.images_only"), "error");
      continue;
    }
    const room = roomForImage(0);
    if (room !== "ok") {
      imageRoomError(room);
      return;
    }
    const read = {};
    const release = () => commentReads.delete(read);
    commentReads.add(read);
    signal.addEventListener("abort", release, { once: true });
    let image: DraftImage | null = null;
    try {
      image = await fitDraftImage(file, limits());
    } catch {
      // A failed decode or read uses the same message as an unsupported image.
    } finally {
      release();
      signal.removeEventListener("abort", release);
    }
    if (!current(id)) return;
    if (!image) {
      toast(t("chat-composeroverlays.image_unreadable", { name: file.name }), "error");
      continue;
    }
    const fittedRoom = roomForImage(image.data.length);
    if (fittedRoom !== "ok") {
      imageRoomError(fittedRoom);
      continue;
    }
    if (!id && typeof target === "function") id = target();
    if (!id || !current(id)) return;
    const added = { ...image, id: uuid() };
    setCommentImages(draftKey, id, (images) => [...images, added]);
  }
}

type ExpandedProps = {
  editLabel?: string;
  value: InlineDocument;
  placeholder: string;
  selection: [number, number];
  onChange: (v: InlineDocument) => void;
  onCollapse: (sel: [number, number]) => void;
  canSend: boolean;
  blocked: boolean;
  onSend: (sel: [number, number], repeat?: boolean) => void;
};

type SuggestionProps = { text: string; onFill: () => void; onSend?: () => void };
type PasteProps = {
  pastedBlocks: { id: string; text: string }[];
  onRemove: (id: string) => void;
  onExpand: (id: string, text: string) => void;
};
type ImageStageOverlayProps = { imageStage: ComposerImageStageProps };
type ConfirmOverlayProps = {
  confirm: {
    word: string;
    message: string;
    onConfirm: () => void;
  };
  onClose: () => void;
};

/** Conditional composer extras share one chunk and one first-screen loader. */
export function ComposerOverlay(props: ExpandedProps | SuggestionProps | PasteProps | ImageStageOverlayProps | ConfirmOverlayProps | { dropHint: true }) {
  if ("dropHint" in props) return <DropHint />;
  if ("imageStage" in props) return <ComposerImageStage {...props.imageStage} />;
  if ("confirm" in props) return <ConfirmDialog {...props} />;
  if ("pastedBlocks" in props) return <PastedBlocks {...props} />;
  return "value" in props ? <ExpandedComposer {...props} /> : <SuggestionPill {...props} />;
}

function DropHint() {
  return (
    <div className="pointer-events-none absolute inset-0 z-20 flex items-center justify-center rounded-[14px] bg-surface/80 backdrop-blur-[2px]">
      <span className="inline-flex items-center gap-1.5 rounded-full border border-accent/30 bg-surface px-3 py-1 text-xs font-medium text-accent shadow-card">
        <Icon name="image" size={14} />
        {t("chat-composeroverlays.drop_here")}
      </span>
    </div>
  );
}

function ConfirmDialog({ confirm, onClose }: ConfirmOverlayProps) {
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("chat-composer.confirm_run_title", { word: confirm.word })}
      width={400}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("chat-composer.confirm_cancel")}
          </Button>
          <Button
            tone="danger"
            onClick={() => {
              const fn = confirm.onConfirm;
              onClose();
              fn();
            }}
          >
            {t("chat-composer.confirm_ok")}
          </Button>
        </>
      }
    >
      <p className="text-[length:var(--chat-secondary)] leading-relaxed text-ink-2">{confirm.message}</p>
    </Dialog>
  );
}

function PastedBlocks({ pastedBlocks, onRemove, onExpand }: PasteProps) {
  const [previewId, setPreviewId] = useState<string | null>(null);
  const preview = pastedBlocks.find((block) => block.id === previewId);
  return <>
    <div className="flex flex-wrap items-center gap-1.5">
      {pastedBlocks.map((block) => <span key={block.id} className={cx(
        "inline-flex h-6.5 max-w-full items-center gap-1 rounded-[8px] border px-2 text-label transition-colors",
        preview === block ? "border-accent/40 bg-accent-tint text-accent-ink" : "border-line bg-inset text-ink-2 hover:bg-hover",
      )}>
        <button type="button" onClick={() => setPreviewId((id) => id === block.id ? null : block.id)}
          className="inline-flex min-w-0 items-center gap-1.5 truncate" title={t("chat-composeroverlays.pasted_preview_hint")}>
          <Icon name="paste" size={12} className="shrink-0 opacity-75" />
          <span className="truncate">{t("chat-composeroverlays.pasted_chip", { count: block.text.length })}</span>
        </button>
        <button type="button" aria-label={t("chat-composeroverlays.pasted_remove")} onClick={() => onRemove(block.id)}
          className="ml-0.5 flex size-4 shrink-0 items-center justify-center rounded-[4px] text-ink-3 transition-colors hover:bg-hover hover:text-ink">
          <Icon name="x" size={10} stroke={2.4} />
        </button>
      </span>)}
    </div>
    {preview && <PastedPreview pastedText={preview.text} onExpand={() => onExpand(preview.id, preview.text)} />}
  </>;
}

function PastedPreview({ pastedText, onExpand }: { pastedText: string; onExpand: () => void }) {
  const lines = pastedText.split(/\r?\n/);
  const snippet = lines.slice(0, 6).join("\n") + (lines.length > 6 ? "\n…" : "");
  return (
    <div className="rounded-[8px] border border-line bg-inset p-2 text-label">
      <div className="mb-1 flex items-center justify-between gap-2 text-ink-3">
        <span>{t("chat-composeroverlays.pasted_stats", { lines: lines.length, chars: pastedText.length })}</span>
        <button type="button" onClick={onExpand} className="font-medium text-accent-ink hover:underline">
          {t("chat-composeroverlays.pasted_expand")}
        </button>
      </div>
      <pre className="max-h-28 overflow-hidden font-mono text-submeta leading-[1.45] whitespace-pre-wrap text-ink-2 [overflow-wrap:anywhere]">{snippet}</pre>
    </div>
  );
}

/** The draft full screen on a phone (#5688): collapse at the top right, the text filling the visible height, send at the
 *  bottom right. It edits the box's own draft; closing hands back the caret so the box picks up where this left off. */
function ExpandedComposer({ editLabel, value, placeholder, selection, onChange, onCollapse, canSend, blocked, onSend }: ExpandedProps) {
  const panel = useRef<HTMLDivElement>(null);
  const field = useRef<InlineMentionInputHandle>(null);
  const start = useRef(selection);
  const ime = useComposition();
  useModalFocus(panel);
  useLayoutEffect(() => {
    const el = field.current;
    if (!el) return;
    el.focus({ preventScroll: true });
    el.setSelectionRange(start.current[0], start.current[1]);
  }, []);
  const sel = (): InlineSelection => {
    const end = editorText(value).length;
    return field.current?.selection() ?? [end, end];
  };
  // Back collapses it, as the collapse button does, and stays in the chat
  useCloseOnBack(true, () => onCollapse(sel()), useMedia(NARROW));
  return createPortal(
    <div
      ref={panel}
      role="dialog"
      aria-modal="true"
      aria-label={editLabel ?? t("chat-composer.message")}
      tabIndex={-1}
      data-testid="composer-expanded"
      className="fixed inset-x-0 z-50 flex flex-col bg-surface outline-none"
      style={{ top: "var(--kb-top)", height: "var(--kb-h)", animation: `fade-in 160ms ease-out both` }}
      onKeyDown={(e) => {
        if (e.key === "Escape" && !ime.busy(e.nativeEvent)) {
          e.preventDefault();
          e.stopPropagation();
          onCollapse(sel());
        }
      }}
    >
      <div className="flex justify-end px-2 pt-[max(env(safe-area-inset-top),8px)]">
        <button
          type="button"
          data-testid="composer-collapse"
          aria-label={t("chat-composeroverlays.collapse")}
          title={t("chat-composeroverlays.collapse")}
          onClick={() => onCollapse(sel())}
          className="touch-hit flex size-9 items-center justify-center rounded-[10px] text-ink-2 transition-colors hover:bg-hover hover:text-ink"
        >
          <Icon name="collapse" size={18} stroke={2} />
        </button>
      </div>
      <ComposerField
        inputRef={field}
        value={value}
        onChange={onChange}
        {...ime.handlers}
        onBlur={ime.onBlur}
        onKeyDown={(e) => {
          if (ime.busy(e.nativeEvent)) return;
          // The full editor always keeps plain Enter as a newline; an explicit Mod+Enter sends on any pointer.
          if (editLabel || !enterSends(e, false)) return;
          e.preventDefault();
          if (canSend || blocked) onSend(sel(), e.repeat);
        }}
        placeholder={placeholder}
        label={editLabel ?? t("chat-composer.message")}
        className="relative min-h-0 w-full flex-1 resize-none overflow-y-auto bg-transparent px-5 py-1 text-[16px] leading-[24px] text-ink outline-none [overflow-wrap:anywhere] placeholder:text-ink-3"
      />
      {!editLabel && <div className="flex justify-end px-3 pt-2 pb-[max(env(safe-area-inset-bottom),12px)]">
        <button
          type="button"
          data-testid="composer-expanded-send"
          aria-label={t("chat-composer.send")}
          disabled={!canSend}
          onClick={() => onSend(sel())}
          className="touch-hit flex size-9 shrink-0 items-center justify-center rounded-full transition-[background-color,color,transform] duration-200 enabled:active:scale-[0.94]"
          style={{ background: canSend ? "var(--ink)" : "var(--line-strong)", color: canSend ? "var(--surface)" : "var(--ink-2)" }}
        >
          <Icon name="up" size={18} stroke={2.4} />
        </button>
      </div>}
    </div>,
    document.body,
  );
}

/** The suggested next message over the box (iOS SuggestionPill): the row fills it in, the arrow sends it as it is; a
 *  risky one (no onSend) has no arrow (#4644). */
function SuggestionPill({ text, onFill, onSend }: SuggestionProps) {
  return (
    <div data-testid="composer-suggestion" className="mb-2 flex items-center gap-1 rounded-full bg-surface p-1 pl-1 shadow-card" style={{ animation: `fade-up 200ms ${EASE} both` }}>
      <button
        type="button"
        onMouseDown={(e) => e.preventDefault()}
        onClick={onFill}
        aria-label={t("chat-composeroverlays.suggestion_label", { suggestion: text })}
        title={hasFinePointer() ? t("chat-composeroverlays.suggestion_use_hint") : undefined}
        className="touch-hit flex min-w-0 flex-1 items-center gap-2 rounded-full py-1 pr-1 pl-2.5 text-left transition-colors hover:bg-hover"
      >
        <Icon name="sparkles" size={13} stroke={2} className="shrink-0 text-accent" />
        <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-2">{text}</span>
        <span className="shrink-0 whitespace-nowrap text-[12px] font-medium text-accent">{t("chat-composeroverlays.suggestion_use")}</span>
      </button>
      {onSend && (
        <button
          type="button"
          onMouseDown={(e) => e.preventDefault()}
          onClick={onSend}
          aria-label={t("chat-composeroverlays.suggestion_send")}
          title={t("chat-composeroverlays.suggestion_send")}
          className="touch-hit flex size-7 shrink-0 items-center justify-center rounded-full text-ink-3 transition-[background-color,color,transform] duration-150 hover:bg-hover hover:text-ink active:scale-[0.94]"
        >
          <Icon name="up" size={15} stroke={2.4} />
        </button>
      )}
    </div>
  );
}
