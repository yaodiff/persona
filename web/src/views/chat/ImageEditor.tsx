// The image editor (iOS ImageEditor.swift, #4963 / #4991), over the picture on black: Box (the tool it opens on, as it
// always has) and Pen in six inks and three widths, Crop (free or a fixed shape, Rotate a quarter left, Reset), and Undo
// / Redo over all of it. The source is never changed: Done hands back the picture drawn from the edits at the source's
// own resolution, and the edits with their history (lib/image-edit.ts), so one handed in again carries on where it
// stopped.
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { DraftImage } from "../../lib/drafts";
import {
  handleCursor,
  IMAGE_EDIT,
  isFramedAsIs,
  resetFrame,
  reshape,
  turnLeft,
  type EditState,
  type Ink,
} from "../../lib/image-edit";
import { cx, Spinner, toast } from "../../ui/atoms";
import { useModalFocus } from "../../ui/overlay";
import { t } from "../../i18n";
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

export function ImageEditor({
  src,
  start,
  doneTitle = t("chat-imageeditor.done"),
  onDone,
  onCancel,
}: {
  /** The picture as it came (never changed). */
  src: string;
  /** Where an earlier edit of `src` stopped: its edits and their history, carried on. */
  start?: EditState;
  doneTitle?: string;
  onDone: (img: DraftImage, state: EditState) => void;
  onCancel: () => void;
}) {
  const [tool, setTool] = useState<EditorTool>("box");
  const [ink, setInk] = useState<Ink>("red");
  const [lineWidth, setLineWidth] = useState<number>(IMAGE_EDIT.defaultLineWidth);
  const [menu, setMenu] = useState<EditorMenuState>(null);
  const [busy, setBusy] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const toolsRow = useRef<HTMLDivElement>(null);
  const cropRow = useRef<HTMLDivElement>(null);

  const editor = useImageEditorCore({
    src,
    stateKey: src,
    start,
    tool,
    ink,
    lineWidth,
    disabled: busy,
    canvasActive: true,
    onDecodeError: () => {
      toast(t("chat-imageeditor.unreadable"), "error");
      onCancel();
    },
  });

  const { img, natural, edits, history, dragging, cropping, cursor, areaRef, canvasRef, change, stepBack, stepForward } = editor;
  const toolWords = useFitsWords(toolsRow, tool);
  const cropWords = useFitsWords(cropRow, `${tool}${edits.shape}`);
  // focus in, Tab kept in, back to the button that opened it (the viewer's "Edit", the composer's) on close
  useModalFocus(root);

  // read by the key handler, registered once
  const keys = useRef({ menu: false, dragging: false, stepBack, stepForward, onCancel });
  keys.current = { menu: menu !== null, dragging, stepBack, stepForward, onCancel };
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      const k = keys.current;
      if (k.menu) return; // an open menu takes its own keys (Esc closes it, not the editor)
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        k.onCancel();
      } else if ((e.metaKey || e.ctrlKey) && (e.key.toLowerCase() === "z" || (e.ctrlKey && !e.metaKey && e.key.toLowerCase() === "y"))) {
        e.preventDefault();
        e.stopPropagation();
        if (k.dragging) return;
        if (e.key.toLowerCase() === "y" || e.shiftKey) k.stepForward();
        else k.stepBack();
      }
    };
    addEventListener("keydown", key, true);
    return () => removeEventListener("keydown", key, true);
  }, []);

  const finish = async () => {
    if (!img || busy) return;
    const state: EditState = { edits, history };
    setBusy(true);
    try {
      const res = await renderEditedImage(img, src, edits, natural);
      if (!res.ok) {
        toast(t(res.reason === "too_large" ? "chat-imageeditor.too_large" : "chat-imageeditor.save_failed"), "error");
        return;
      }
      onDone(res.image, state);
    } finally {
      setBusy(false);
    }
  };

  const off = !img || busy;

  return createPortal(
    // dark whatever the page's appearance, as the image viewer: its menus (put inside it) take the dark look too
    <div ref={root} role="dialog" aria-modal="true" aria-label={t("chat-imageeditor.title")} tabIndex={-1} className="dark fixed inset-0 z-[60] flex flex-col bg-black text-white outline-none" style={{ animation: "fade-in 150ms ease-out both" }}>
      <div className="grid h-14 shrink-0 grid-cols-[1fr_auto_1fr] items-center gap-2 px-3">
        <button type="button" onClick={onCancel} className="h-9 justify-self-start rounded-full px-3 text-[13.5px] whitespace-nowrap text-white/85 transition-colors hover:bg-white/15 hover:text-white">
          {t("common.cancel")}
        </button>
        {/* Undo and Redo up top, as iOS and Photos have them: the rows below stay one line on a phone */}
        <div className="flex items-center gap-1">
          <BarIcon icon="undo" label={t("chat-imageeditor.undo")} disabled={off || !history.past.length} onClick={stepBack} />
          <BarIcon icon="redo" label={t("chat-imageeditor.redo")} disabled={off || !history.future.length} onClick={stepForward} />
        </div>
        <button
          type="button"
          onClick={() => void finish()}
          disabled={off}
          className="flex h-9 min-w-0 items-center gap-1.5 justify-self-end rounded-full bg-white px-4 text-[13.5px] font-semibold whitespace-nowrap text-black transition-[opacity,transform] active:scale-[0.97] disabled:opacity-50"
        >
          {busy && <Spinner />}
          {doneTitle}
        </button>
      </div>
      <div ref={areaRef} className="relative min-h-0 flex-1">
        <canvas
          ref={canvasRef}
          className={cx("absolute inset-0 size-full", tool !== "crop" && "cursor-crosshair")}
          style={{ touchAction: "none", cursor: tool === "crop" ? (cropping ? handleCursor(cropping.handle) : cursor) : undefined }}
          onPointerDown={editor.onPointerDown}
          onPointerMove={editor.onPointerMove}
          onPointerUp={editor.onPointerUp}
          onPointerCancel={editor.onPointerCancel}
        />
        {!img && (
          <div className="absolute inset-0 flex items-center justify-center">
            <Spinner />
          </div>
        )}
      </div>
      <div className="@container flex shrink-0 flex-col gap-3 px-4 pt-3 pb-[max(12px,env(safe-area-inset-bottom))]">
        {tool === "crop" ? (
          <div ref={cropRow} className="flex min-w-0 items-center gap-2 overflow-hidden">
            <ToolButton icon="rotateLeft" label={t("chat-imageeditor.rotate")} words={cropWords} disabled={off} onClick={() => change(turnLeft)} />
            <ToolButton
              icon="ratio"
              label={t("chat-imageeditor.aspect_ratio")}
              text={shapeName(edits.shape)}
              words={cropWords}
              disabled={off}
              menu={menu?.kind === "shape"}
              onClick={(el) => setMenu((m) => (m ? null : { kind: "shape", anchor: el }))}
            />
            <div className="flex-1" />
            <ToolButton label={t("chat-imageeditor.reset")} words disabled={off || isFramedAsIs(edits)} onClick={() => change(resetFrame)} />
          </div>
        ) : (
          <div className="flex items-center justify-center gap-3">
            <InkSwatches ink={ink} onSelect={setInk} />
            <IconAction
              icon={<LineSample width={lineWidth} />}
              label={t("chat-imageeditor.line_width")}
              value={widthName(lineWidth)}
              menu={menu?.kind === "width"}
              disabled={off}
              onClick={(el) => setMenu((m) => (m ? null : { kind: "width", anchor: el }))}
            />
          </div>
        )}
        <div ref={toolsRow} className="flex min-w-0 items-center gap-2 overflow-hidden">
          <ToolButton icon="square" label={t("chat-imageeditor.box")} words={toolWords} active={tool === "box"} onClick={() => setTool("box")} />
          <ToolButton icon="write" label={t("chat-imageeditor.pen")} words={toolWords} active={tool === "pen"} onClick={() => setTool("pen")} />
          <ToolButton icon="crop" label={t("chat-imageeditor.crop")} words={toolWords} active={tool === "crop"} disabled={!img} onClick={() => setTool("crop")} />
          <div className="flex-1" />
          {tool !== "crop" && <IconAction icon="trash" label={t("chat-imageeditor.clear")} disabled={off || !edits.marks.length} onClick={() => change((e) => ({ ...e, marks: [] }))} />}
        </div>
      </div>
      <EditorMenu
        menu={menu}
        lineWidth={lineWidth}
        shape={edits.shape}
        container={root.current}
        side="top"
        onSelectWidth={setLineWidth}
        onSelectShape={(s) => change((e) => reshape(e, s, natural))}
        onClose={() => setMenu(null)}
      />
    </div>,
    document.body,
  );
}
