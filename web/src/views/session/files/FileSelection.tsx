import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { t } from "../../../i18n";
import { commentKey } from "../../../lib/comment-target";
import { rangeLabel } from "../../../lib/comments";
import { addQuote } from "../../../lib/drafts";
import { closePanel, type Panel } from "../../../lib/panel";
import { composing } from "../../../lib/send-history";
import { focusKind } from "../../../lib/shortcuts";
import { mediaMatches, NARROW } from "../../../lib/viewport";
import type { CommentBarAnchor } from "../../chat/CommentBar";
import { CommentBarOverlay, preloadCommentBar } from "../../chat/CommentBarOverlay";

export type FileCommentTarget = Omit<Extract<Panel, { kind: "file" }>, "kind" | "line" | "to" | "navigationId">;

interface FileSelectionDraft {
  key: string;
  doc: Document;
  text: string;
  line?: number;
  to?: number;
  location: string;
  anchor: CommentBarAnchor;
  focused: boolean;
}

function isCommentShortcut(e: KeyboardEvent): boolean {
  return !e.defaultPrevented && e.key.toLowerCase() === "c" && !e.metaKey && !e.ctrlKey &&
    !e.altKey && !e.shiftKey && !e.repeat && !composing(e) && focusKind(e.target) === "page";
}

function codeEdge(node: Node, offset: number) {
  const element = node.nodeType === Node.ELEMENT_NODE ? node as Element : node.parentElement;
  const row = element?.closest("[data-line]");
  const span = row?.querySelector("[data-file-code-text]");
  const line = Number(row?.getAttribute("data-line"));
  if (!span || !line || (node !== row && !span.contains(node))) return null;
  const prefix = span.ownerDocument.createRange();
  prefix.selectNodeContents(span);
  const marker = span.querySelector("[data-file-truncation-marker]");
  if (marker) prefix.setEndBefore(marker);
  // Endpoints in or beyond the marker stop at the last visible source character.
  if (prefix.comparePoint(node, offset) <= 0) prefix.setEnd(node, offset);
  return { line, offset: prefix.toString().length };
}

/** The source rows are virtualized and contain a gutter. Rebuild the selection from its text endpoints and source lines. */
function selectedCode(range: Range, lines: readonly string[], maxWidth: number) {
  const start = codeEdge(range.startContainer, range.startOffset);
  const end = codeEdge(range.endContainer, range.endOffset);
  // Controls and virtual spacers have no code edge. Both edges must resolve to loaded source rows.
  if (!start || !end || start.line > end.line || end.line > lines.length) return null;
  const startLength = Math.min(lines[start.line - 1].length, maxWidth);
  const first = start.offset > 0 && start.offset >= startLength && start.line < end.line ? start.line + 1 : start.line;
  const last = end.offset === 0 && end.line > start.line ? end.line - 1 : end.line;
  const text = lines.slice(first - 1, last).map((raw, index) => {
    const line = first + index;
    const visibleLength = Math.min(raw.length, maxWidth);
    const from = line === start.line ? Math.min(start.offset, visibleLength) : 0;
    const to = line === end.line ? Math.min(end.offset, visibleLength) : visibleLength;
    return raw.slice(from, to);
  }).join("\n");
  return { text, line: first, to: last };
}

function sameDraft(a: FileSelectionDraft | null, b: FileSelectionDraft): boolean {
  return Boolean(
    a &&
    a.key === b.key &&
    a.text === b.text &&
    a.line === b.line &&
    a.to === b.to &&
    a.location === b.location &&
    a.anchor.x === b.anchor.x &&
    a.anchor.top === b.anchor.top &&
    a.anchor.bottom === b.anchor.bottom,
  );
}

/** Offer a floating comment bar for text selected in the file viewer. */
export function FileSelection({ view, file, lines, source, maxWidth = Infinity }: {
  view: RefObject<HTMLElement | null>;
  file: FileCommentTarget;
  lines?: readonly string[];
  source?: boolean;
  maxWidth?: number;
}) {
  const { mid, sid, cwd, path } = file;
  const [draft, setDraft] = useState<FileSelectionDraft | null>(null);
  const draftRef = useRef<FileSelectionDraft | null>(null);
  draftRef.current = draft;
  const draftNoteRef = useRef("");
  const barInputRef = useRef<HTMLTextAreaElement>(null);

  const hide = useCallback(() => {
    draftNoteRef.current = "";
    draftRef.current = null;
    setDraft(null);
  }, []);

  const save = useCallback((note: string) => {
    const selection = draftRef.current;
    hide();
    if (!selection || commentKey(mid, sid) !== selection.key) return;
    const { key, doc, text, line, to, location } = selection;
    const panel = { kind: "file" as const, path, line, to, mid, sid, cwd };
    doc.getSelection()?.removeAllRanges();
    addQuote(key, { text, comment: { body: note, file: { location, panel } } }, false);
    if (mediaMatches(NARROW)) closePanel();
  }, [mid, sid, cwd, path, hide]);

  const keepOrHide = useCallback(() => {
    const note = draftNoteRef.current.trim();
    if (note) save(note);
    else hide();
  }, [save, hide]);

  useEffect(() => {
    const root = view.current;
    if (!root) return;
    hide();
    const doc = root.ownerDocument;
    let raf = 0;
    let dragging = false;
    const read = () => {
      if (draftRef.current?.focused || (draftRef.current && draftNoteRef.current !== "")) return;
      const key = commentKey(mid, sid);
      const sel = doc.getSelection();
      if (!key || dragging || !sel || sel.isCollapsed || !sel.rangeCount) return hide();
      const range = sel.getRangeAt(0);
      if (!root.contains(range.commonAncestorContainer)) return hide();
      const code = lines && selectedCode(range, lines, maxWidth);
      const text = lines ? code?.text : sel.toString().trim();
      if (!text?.trim()) return hide();
      const rects = range.getClientRects();
      const first = rects[0];
      const last = rects[rects.length - 1];
      if (!first || !last) return hide();
      const bounds = root.getBoundingClientRect();
      const x = last.right;
      const top = first.top;
      const bottom = last.top + last.height;
      if (bottom <= bounds.top || bottom > bounds.bottom || x < bounds.left || x > bounds.right) return hide();
      const label = path.replaceAll("\r", "\\r").replaceAll("\n", "\\n");
      const line = source ? code?.line : undefined;
      const to = source ? code?.to : undefined;
      const location = line === undefined ? label : `${label}:${rangeLabel(line, to ?? line)}`;
      void preloadCommentBar().catch(() => {});
      const next: FileSelectionDraft = {
        key,
        doc,
        text,
        line,
        to,
        location,
        anchor: { x: (last.left + last.right) / 2, top, bottom },
        focused: false,
      };
      setDraft((prev) => (sameDraft(prev, next) ? prev : next));
    };
    const soon = () => {
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(read);
    };
    const down = (e: PointerEvent) => {
      const target = e.target as Element | null;
      if (target?.closest?.("[data-comment-bar]")) return;
      dragging = Boolean(target && root.contains(target));
      if (!draftRef.current?.focused && draftNoteRef.current === "") hide();
    };
    const up = () => {
      if (!dragging) return;
      dragging = false;
      soon();
    };
    const press = (e: KeyboardEvent) => {
      if (!draftRef.current || !isCommentShortcut(e)) return;
      e.preventDefault();
      setDraft((d) => (d && !d.focused ? { ...d, focused: true } : d));
      barInputRef.current?.focus({ preventScroll: true });
    };
    doc.addEventListener("selectionchange", soon);
    doc.addEventListener("pointerdown", down, true);
    doc.addEventListener("pointerup", up, true);
    doc.addEventListener("pointercancel", up, true);
    doc.addEventListener("keydown", press);
    doc.addEventListener("scroll", soon, { capture: true, passive: true });
    window.addEventListener("resize", soon);
    return () => {
      cancelAnimationFrame(raf);
      doc.removeEventListener("selectionchange", soon);
      doc.removeEventListener("pointerdown", down, true);
      doc.removeEventListener("pointerup", up, true);
      doc.removeEventListener("pointercancel", up, true);
      doc.removeEventListener("keydown", press);
      doc.removeEventListener("scroll", soon, true);
      window.removeEventListener("resize", soon);
    };
  }, [view, lines, source, maxWidth, mid, sid, path, hide]);

  return draft && (
    <CommentBarOverlay
      anchor={draft.anchor}
      context={draft.location}
      showContextOnDesktop
      inputRef={barInputRef}
      autoFocus={draft.focused}
      shortcutHint="C"
      onInputFocus={() => setDraft((d) => (d && !d.focused ? { ...d, focused: true } : d))}
      onDraftChange={(next) => {
        draftNoteRef.current = next;
      }}
      onSave={save}
      onCancel={hide}
      onDismiss={keepOrHide}
      onYield={keepOrHide}
      ariaLabel={t("chat-comments.box_label")}
    />
  );
}
