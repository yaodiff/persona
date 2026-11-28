// A comment on picked words or on lines of code (#2437, #11585): a single compact chip above the composer
// (`CommentsChip`, opening a popover list to jump to any pin or remove one) and a card in a sent bubble (`CommentBlock`).
// The number is the one the spot carries in the transcript (`CommentMarks.tsx`) and the one the agent reads.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { jumpToComment } from "../../lib/comment-marks";
import { codePreview, diffLabel, openSpot, rangeLabel, type CodeSpot, type ElementSpot } from "../../lib/comments";
import { openPanel, type Panel } from "../../lib/panel";
import { getSession } from "../../lib/store";
import { uuid } from "../../lib/format";
import { addressOf } from "../../lib/preview";
import { cancelNote, setCommentImages, setNote, type CommentImage, type CommentImagePicker, type DraftQuote } from "../../lib/drafts";
import { cx, EASE, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import type { CommentBarAnchor } from "./CommentBar";
import { CommentBarOverlay, preloadCommentBar } from "./CommentBarOverlay";
import { t } from "../../i18n";

/** `staged`, `unstaged`, `commit 1a2b3c4d`, worded for the reader. */
export function diffName(d: NonNullable<CodeSpot["diff"]>): string {
  if (d.kind === "review") {
    if (d.scope === "uncommitted") return t("chat-comments.review_uncommitted");
    return t(d.scope === "branch" ? "chat-comments.review_branch" : d.scope === "turn" ? "chat-comments.review_turn" : "chat-comments.review_since", { sha: d.base });
  }
  return d.kind === "commit" ? t("chat-comments.commit", { sha: diffLabel(d).slice(7) }) : t(d.kind === "staged" ? "chat-comments.staged" : "chat-comments.unstaged");
}

/** `src/app.ts:12-14 · staged`, worded for the reader. */
export function spotWhere(s: CodeSpot): string {
  return [`${s.file}:${rangeLabel(s.from, s.to)}`, s.old ? t("chat-comments.side_old") : "", s.diff ? diffName(s.diff) : ""].filter(Boolean).join(" · ");
}

/** `button.save · /settings`: an element comment's element and the page it is on. */
export function elementWhere(e: ElementSpot): string {
  return [e.selector, e.port ? addressOf(e.url, e.port) : e.url].join(" · ");
}

/** Where a comment points, worded for the reader: a file's lines, or an element of a preview. */
function whereOf(c: NonNullable<DraftQuote["comment"]>): string {
  if (c.file) return c.file.location;
  return c.element ? elementWhere(c.element) : c.spot ? spotWhere(c.spot) : "";
}

/** Lines of code (or of an element's HTML) a comment carries beyond the ones sent. */
function omittedOf(c: NonNullable<DraftQuote["comment"]>): number {
  return c.element?.omitted ?? c.spot?.omitted ?? 0;
}

/** The round number a comment carries, in the popover and next to its words in the transcript. */
export function NumberDot({ n, className }: { n: number; className?: string }) {
  return (
    <span aria-hidden className={cx("flex size-4 shrink-0 items-center justify-center rounded-full bg-accent text-[10px] leading-none font-semibold text-white tabular-nums", className)}>
      {n}
    </span>
  );
}

/** The words or the lines a comment is about: code in mono, one line of it each. */
function Excerpt({ q, lines }: { q: DraftQuote; lines: number }) {
  const spot = q.comment?.spot ?? q.comment?.element;
  const text = q.text.trim();
  if (!text) return null;
  if (spot) {
    return (
      <span className="flex min-w-0 flex-col font-mono text-[12px] leading-[1.5] text-ink-2" data-comment-excerpt>
        {codePreview(text, lines).map((l, i) => (
          <span key={i} className="truncate whitespace-pre">
            {l || " "}
          </span>
        ))}
      </span>
    );
  }
  return (
    <span className={cx(lines === 3 ? "line-clamp-3" : "line-clamp-2", "text-[12.5px] leading-[1.5] text-ink-2 [overflow-wrap:anywhere]")} data-comment-excerpt>
      {text}
    </span>
  );
}

/** Recover the viewer target from a sent comment in its Agent session. Drafts already carry the original target. */
export function fileCommentPanel(file: NonNullable<DraftQuote["comment"]>["file"], mid?: string, sid?: string): Extract<Panel, { kind: "file" }> | undefined {
  if (!file || file.panel) return file?.panel;
  if (!mid || !sid) return;
  const cwd = getSession(mid, sid)?.cwd;
  const range = /^(.+):([1-9]\d*)(?:-([1-9]\d*))?$/.exec(file.location);
  if (range) {
    const line = Number(range[2]);
    const to = Number(range[3] ?? range[2]);
    if (Number.isSafeInteger(line) && Number.isSafeInteger(to) && to >= line) {
      return { kind: "file", mid, sid, cwd, path: range[1], line, to };
    }
  }
  return { kind: "file", mid, sid, cwd, path: file.location };
}

export interface CommentPreviewTarget {
  mid: string;
  sid: string;
  cwd: string;
  port: number;
  path: string;
  element: ElementSpot;
}

export interface CommentNavigationHandlers {
  /** Machine and session context for resolving sent file comments when no panel target is stored. */
  mid?: string;
  sid?: string;
  /** Opens a file viewer panel for a file comment instead of the default side-panel singleton. */
  openFile?: (target: Extract<Panel, { kind: "file" }>) => boolean | void;
  /** Opens a preview panel for an element comment instead of the default side-panel singleton. */
  openPreview?: (target: CommentPreviewTarget) => boolean | void;
  /** Opens a code or diff spot instead of the default `openSpot` singleton. */
  openSpot?: (spot: CodeSpot) => boolean;
  /** Scrolls to a text comment in the transcript instead of the default `jumpToComment` singleton. */
  jumpToText?: (id: string) => boolean;
  /** Reports a jump-unavailable notice instead of the default `toast` singleton. */
  onNotice?: (message: string) => void;
}

/** Goes back to where the comment was made: the words in the transcript, or the file / diff at its lines. */
export function goToComment(q: DraftQuote, handlers?: CommentNavigationHandlers): boolean {
  const c = q.comment;
  if (!c) return false;
  const notice = handlers?.onNotice ?? toast;
  const file = fileCommentPanel(c.file, handlers?.mid, handlers?.sid);
  if (file) {
    // Jump again even if the same file is open and the viewer has switched modes or scrolled away.
    const target = { ...file, navigationId: uuid() };
    if (handlers?.openFile) {
      const ok = handlers.openFile(target) !== false;
      if (!ok) notice(t("chat-comments.jump_code_gone"));
      return ok;
    }
    openPanel(target);
    return true;
  }
  if (c.element) {
    const e = c.element;
    // the preview of that session, at the page the element was on
    if (!e.mid || !e.sid || !e.cwd || !e.port) {
      notice(t("chat-comments.jump_code_gone"));
      return false;
    }
    const rawPath = addressOf(e.url, e.port);
    const path = rawPath.startsWith("/") ? rawPath : "/";
    if (handlers?.openPreview) {
      const ok = handlers.openPreview({ mid: e.mid, sid: e.sid, cwd: e.cwd, port: e.port, path, element: e }) !== false;
      if (!ok) notice(t("chat-comments.jump_code_gone"));
      return ok;
    }
    openPanel({ kind: "preview", mid: e.mid, sid: e.sid, cwd: e.cwd, port: e.port, path });
    return true;
  }
  if (c.spot) {
    const ok = handlers?.openSpot ? handlers.openSpot(c.spot) : openSpot(c.spot);
    if (!ok) notice(t("chat-comments.jump_code_gone"));
    return ok;
  }
  if (q.id && (handlers?.jumpToText ?? jumpToComment)(q.id)) return true;
  notice(t("chat-comments.jump_text_gone"));
  return false;
}

const POPOVER_MAX_W = 340;
const EDGE = 8;
const GAP = 6;

/** Single compact `"N comments"` chip above the composer; clicking opens a popover list to jump to a pin, edit a note, or remove one. */
export function CommentsChip({ draftKey, comments, onRemove, onAddImages }: {
  draftKey?: string; comments: readonly DraftQuote[]; onRemove: (id: string) => void; onAddImages?: CommentImagePicker;
}) {
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<{
    id: string; n: number; context: string; originalBody: string; originalImages?: readonly CommentImage[]; anchor: CommentBarAnchor | null;
  } | null>(null);
  const editedQuote = editing && comments.find((q) => q.id === editing.id);
  const chipRef = useRef<HTMLButtonElement>(null);
  const popRef = useRef<HTMLDivElement>(null);
  const [place, setPlace] = useState<{ left: number; bottom: number; maxHeight: number } | null>(null);

  useEffect(() => {
    if (editing && !editedQuote) setEditing(null);
  }, [editing, editedQuote]);

  useLayoutEffect(() => {
    if (!open || !chipRef.current || typeof window === "undefined") return;
    const update = () => {
      const btn = chipRef.current;
      if (!btn) return;
      const r = btn.getBoundingClientRect();
      const w = Math.min(POPOVER_MAX_W, Math.max(180, window.innerWidth - EDGE * 2));
      const left = Math.min(Math.max(r.left, EDGE), Math.max(EDGE, window.innerWidth - w - EDGE));
      const bottom = Math.max(EDGE, window.innerHeight - r.top + GAP);
      const maxHeight = Math.max(120, Math.min(280, r.top - GAP - EDGE));
      setPlace({ left: Math.round(left), bottom: Math.round(bottom), maxHeight: Math.round(maxHeight) });
    };
    update();
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [open, comments.length]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
      if (!(e.target instanceof Node)) return;
      if (chipRef.current?.contains(e.target) || popRef.current?.contains(e.target)) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
      chipRef.current?.focus({ preventScroll: true });
    };
    document.addEventListener("pointerdown", onDown, true);
    window.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("pointerdown", onDown, true);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [open]);

  if (!comments.length) return null;

  const label = t("chat-comments.count", { count: comments.length });
  const popover =
    open && typeof document !== "undefined"
      ? createPortal(
          <div
            ref={popRef}
            role="dialog"
            aria-label={t("chat-comments.list_label")}
            data-comment-popover
            className="fixed z-40 flex w-[min(340px,calc(100vw-16px))] flex-col gap-0.5 overflow-y-auto overscroll-contain rounded-[12px] border border-line-strong bg-surface p-1.5 text-ink shadow-overlay"
            style={{
              left: place?.left ?? EDGE,
              bottom: place?.bottom ?? EDGE,
              maxHeight: place?.maxHeight ?? 280,
              animation: `pop-in 140ms ${EASE} both`,
            }}
          >
            {comments.map((q, idx) => {
              const n = idx + 1;
              const c = q.comment!;
              const where = whereOf(c);
              const firstWords = where || q.text.replace(/\s+/g, " ").trim();
              const note = c.body.trim();
              return (
                <div
                  key={q.id ?? idx}
                  data-comment-item={n}
                  className="flex min-w-0 items-start gap-1.5 rounded-[8px] p-1 transition-colors hover:bg-hover"
                >
                  <button
                    type="button"
                    data-comment-jump={n}
                    title={t("chat-comments.jump")}
                    onClick={() => {
                      if (draftKey) {
                        const [mid, ...sid] = draftKey.split("/");
                        goToComment(q, { mid, sid: sid.join("/") });
                      } else {
                        goToComment(q);
                      }
                      setOpen(false);
                    }}
                    className="flex min-w-0 flex-1 items-start gap-2 rounded-[6px] px-1 py-0.5 text-left focus-visible:outline-2 focus-visible:outline-accent"
                  >
                    <NumberDot n={n} className="mt-0.5" />
                    <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                      <span className={cx("truncate text-[12px] leading-[1.35] text-ink-2", where && "font-mono text-[11.5px]")}>{firstWords}</span>
                      {note && <span className="truncate text-[12.5px] leading-[1.35] text-ink">{note}</span>}
                    </span>
                  </button>
                  {draftKey && q.id && (
                    <button
                      type="button"
                      data-comment-edit={n}
                      aria-label={t("chat-comments.edit")}
                      title={t("chat-comments.edit")}
                      onClick={() => {
                        const r = chipRef.current?.getBoundingClientRect();
                        setOpen(false);
                        setEditing({
                          id: q.id!,
                          n,
                          context: firstWords,
                          originalBody: c.body,
                          originalImages: q.images,
                          anchor: r ? { x: (r.left + r.right) / 2, top: r.top, bottom: r.bottom } : null,
                        });
                      }}
                      className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink"
                    >
                      <Icon name="pencil" size={12} />
                    </button>
                  )}
                  <button
                    type="button"
                    data-comment-remove={n}
                    aria-label={t("chat-comments.remove")}
                    title={t("chat-comments.remove")}
                    onClick={() => onRemove(q.id!)}
                    className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink"
                  >
                    <Icon name="x" size={12} stroke={2.4} />
                  </button>
                </div>
              );
            })}
          </div>,
          document.body,
        )
      : null;

  return (
    <div className="flex items-center px-0.5 pt-0.5" data-comment-chip-row>
      <button
        ref={chipRef}
        type="button"
        data-comment-chip
        aria-expanded={open}
        aria-haspopup="dialog"
        onClick={() => {
          if (!open && draftKey) void preloadCommentBar().catch(() => {});
          // Edits are live-persisted to the quote store via onDraftChange, so closing the editor keeps any typed note.
          setEditing(null);
          setOpen((o) => !o);
        }}
        className="inline-flex h-7 max-w-full items-center gap-1.5 rounded-full bg-accent-tint px-2.5 text-[12px] font-medium whitespace-nowrap text-accent-ink transition-[filter,transform] duration-150 hover:brightness-95 active:scale-[0.98] dark:hover:brightness-110"
        style={{ animation: `pop-in 160ms ${EASE} both` }}
      >
        <Icon name="chat" size={12} className="shrink-0" />
        <span className="tabular-nums">{label}</span>
        <Icon name="chevron" size={11} className={cx("shrink-0 transition-transform duration-150", open && "rotate-180")} />
      </button>
      {popover}
      {editing && editedQuote && draftKey && (
        <CommentBarOverlay
          key={editing.id}
          anchor={editing.anchor}
          number={editing.n}
          context={editing.context}
          showContextOnDesktop
          initialValue={editing.originalBody}
          images={editedQuote.images}
          onAddImages={onAddImages && !editedQuote.comment?.spot && !editedQuote.comment?.element
            ? (files, signal) => onAddImages(editing.id, files, signal) : undefined}
          onRemoveImage={(id) => setCommentImages(draftKey, editing.id, (images) => images.filter((image) => image.id !== id))}
          autoFocus
          captureEscape
          onDraftChange={(next) => {
            setNote(draftKey, editing.id, next, true);
          }}
          onSave={(body) => {
            setNote(draftKey, editing.id, body, true);
            setEditing(null);
          }}
          onCancel={() => {
            cancelNote(draftKey, editing.id, editing.originalBody, editing.originalImages);
            setEditing(null);
          }}
          onDismiss={() => setEditing(null)}
          onYield={() => setEditing(null)}
          onDelete={() => {
            onRemove(editing.id);
            setEditing(null);
          }}
          ariaLabel={t("chat-comments.note_label", { n: editing.n })}
        />
      )}
    </div>
  );
}

export interface CommentBlockProps {
  quote: DraftQuote;
  n: number;
  /** Explicit click handler for jumping back to the commented spot. */
  open?: () => void;
  /** Controlled navigation handlers used when `open` is omitted. */
  navigation?: CommentNavigationHandlers;
}

/** A comment in a sent bubble: its number and where, the words or the code, the note. The code one opens its file or
 *  diff when the session's folder is known (`cwd`). */
export function CommentBlock({ quote, n, open, navigation }: CommentBlockProps) {
  const c = quote.comment!;
  const where = whereOf(c);
  const onOpen = open ?? (navigation ? () => { goToComment(quote, navigation); } : undefined);
  const head = (
    <>
      <NumberDot n={n} />
      <span className="shrink-0">{t("chat-comments.comment_n", { n })}</span>
    </>
  );
  const headClass = "flex max-w-full min-w-0 items-center gap-1.5 text-[12px] font-medium whitespace-nowrap text-ink-3";
  return (
    <div className="flex min-w-0 gap-2 rounded-[8px] bg-ink/[0.05] py-1.5 pr-2.5 pl-2 whitespace-normal" data-quote data-comment-block={n}>
      <span aria-hidden className="w-[3px] shrink-0 self-stretch rounded-full bg-accent/60" />
      <div className="flex min-w-0 flex-1 flex-col items-start gap-1">
        {onOpen ? (
          <button type="button" onClick={onOpen} title={t("chat-comments.jump")} className={cx(headClass, "rounded-[6px] transition-colors hover:bg-hover-2")}>
            {head}
          </button>
        ) : (
          <span className={headClass}>{head}</span>
        )}
        {where && <span className="max-w-full truncate font-mono text-[11px] text-ink-3">{where}</span>}
        <Excerpt q={quote} lines={3} />
        {omittedOf(c) > 0 && <span className="text-[11px] text-ink-3">{t("chat-comments.omitted", { count: omittedOf(c) })}</span>}
        {c.body && <span className="text-[13px] leading-[1.55] whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">{c.body}</span>}
      </div>
    </div>
  );
}
