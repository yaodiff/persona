// Files a message carries as <persona-file> tags (docs/protocol/protocol.md "Sending videos"; iOS draws the same cards), and files an
// answer names by path that exist on the machine: videos and audio play in place, documents are cards with their first
// page (iOS DocumentCards), other files open in the file viewer.
import { useEffect, useMemo, useRef, useState } from "react";
import { fileThumbURL } from "../../lib/api";
import { documentKind, isDarkPage, isDocument, MAX_DOCUMENT_CARDS } from "../../lib/documents";
import { usePathLinks, type PathStat } from "../../lib/path-presence";
import { bytes } from "../../lib/format";
import { openPanel, useReadOnlyFile } from "../../lib/panel";
import { formatDuration, mediaKindOf, replyPaths, type AttachedFile } from "../../lib/turns";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { MediaPlayer } from "../../ui/Media";
import { FileGlyph } from "../session/files/parts";
import { edgeMask, useEdgeFade } from "./edge-fade";
import { t } from "../../i18n";

/** A message of yours with more files than this folds to `FOLDED` and a row that opens the rest (iOS AttachedFileCards). */
const FOLD_AT = 4;
const FOLDED = 3;

function openFilePanel(mid: string, path: string, cwd?: string, readOnly?: boolean) {
  openPanel({ kind: "file", mid, path, ...(cwd ? { cwd } : {}), ...(readOnly ? { readOnly: true } : {}) });
}

export interface FileCardsProps {
  files: AttachedFile[];
  mid: string;
  align?: "start" | "end";
  cwd?: string;
  /** A panel with its own file viewer replaces the default side-panel singleton. */
  onOpenFile?: (file: AttachedFile) => void;
  /** A panel with its own media resources replaces the default fileDownloadURL builder. */
  resolveDownloadURL?: (path: string, options: { inline?: boolean }) => string;
}

export function FileCards({ files, mid, align = "end", cwd, onOpenFile, resolveDownloadURL }: FileCardsProps) {
  const [all, setAll] = useState(false);
  if (!files.length) return null;
  // the files sent with your message (#8361): past four, three and "N more files", opened and folded in place
  const folds = align === "end" && files.length > FOLD_AT;
  const shown = folds && !all ? files.slice(0, FOLDED) : files;
  return (
    <div className={align === "end" ? "flex max-w-full flex-col items-end gap-1.5" : "flex max-w-full flex-col items-start gap-1.5"}>
      {shown.map((f) => (
        <FileCard key={`${mid}\0${cwd ?? ""}\0${f.path}`} f={f} mid={mid} cwd={cwd} onOpenFile={onOpenFile} resolveDownloadURL={resolveDownloadURL} />
      ))}
      {folds && (
        <button
          type="button"
          aria-expanded={all}
          onClick={() => setAll((v) => !v)}
          className="flex max-w-[360px] min-w-0 items-center gap-1.5 rounded-[12px] bg-inset px-3 py-2 text-meta text-accent shadow-hairline hover:bg-hover"
          data-testid="file-cards-more"
        >
          {all ? t("chat-attachedphotos.files_less") : t("chat-attachedphotos.files_more", { count: files.length - FOLDED })}
          <Icon name="chevron" size={12} className={cx(all && "rotate-180")} />
        </button>
      )}
    </div>
  );
}

function FileCard({
  f,
  mid,
  cwd,
  onOpenFile,
  resolveDownloadURL,
}: {
  f: AttachedFile;
  mid: string;
  cwd?: string;
  onOpenFile?: FileCardsProps["onOpenFile"];
  resolveDownloadURL?: FileCardsProps["resolveDownloadURL"];
}) {
  const readOnly = useReadOnlyFile();
  // the length the file really has wins over the one the message says (it may be rounded, or wrong)
  const [real, setReal] = useState<number | undefined>(undefined);
  useEffect(() => setReal(undefined), [mid, cwd, f.path, f.kind, resolveDownloadURL]);
  const duration = real ?? f.duration;
  const meta = [f.size ? bytes(f.size) : "", duration ? formatDuration(duration) : ""].filter(Boolean).join(" · ");
  if (f.kind === "video" || f.kind === "audio")
    return (
      <figure className="m-0 flex w-[min(360px,100%)] flex-col gap-1">
        <MediaPlayer
          key={`${mid}\0${cwd ?? ""}\0${f.path}\0${f.kind}`}
          mid={mid}
          path={f.path}
          cwd={cwd}
          kind={f.kind}
          className={f.kind === "video" ? "max-h-[420px] w-full" : undefined}
          compact
          onDuration={setReal}
          resolveDownloadURL={resolveDownloadURL}
        />
        <figcaption className="flex min-w-0 items-center gap-1.5 px-1 text-[11.5px] text-ink-3">
          <span className="min-w-0 truncate">{f.name}</span>
          {meta && <span className="shrink-0 tabular-nums">{meta}</span>}
        </figcaption>
      </figure>
    );
  return (
    <button
      type="button"
      onClick={() => (onOpenFile ? onOpenFile(f) : openFilePanel(mid, f.path, cwd, readOnly))}
      className="flex max-w-[360px] min-w-0 items-center gap-2 rounded-[12px] bg-inset px-3 py-2 text-left shadow-hairline hover:bg-hover"
      title={f.path}
    >
      <Icon name={f.kind === "image" ? "image" : "read"} size={16} className="shrink-0 text-ink-3" />
      <span className="min-w-0 truncate text-[13px] text-ink">{f.name}</span>
      {meta && <span className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">{meta}</span>}
    </button>
  );
}

export interface ReplyFilesProps {
  text: string;
  mid: string;
  cwd?: string;
  /** Controlled path-stat resolver skips the default path-presence store and /stat fetches. */
  resolvePathStat?: (path: string) => PathStat | undefined;
  /** Explicit resolved files skip both reply path parsing and path-presence lookups. */
  files?: AttachedFile[];
  /** Controlled file opener replacing the default side-panel singleton. */
  onOpenFile?: (file: AttachedFile) => void;
  /** Controlled thumbnail URL resolver for document cards. */
  resolveThumbURL?: (path: string, size: number) => string;
  /** Controlled download URL resolver for video and audio cards. */
  resolveDownloadURL?: (path: string, options: { inline?: boolean }) => string;
}

function statFiles(paths: string[], stat: (path: string) => PathStat | undefined): AttachedFile[] {
  return paths.flatMap((p) => {
    const s = stat(p);
    return s?.kind === "file" ? [{ kind: mediaKindOf(p), path: p, name: p.split("/").pop() || p, size: s.size }] : [];
  });
}

function renderReplyFiles(files: AttachedFile[], { mid, cwd, onOpenFile, resolveThumbURL, resolveDownloadURL }: ReplyFilesProps) {
  const docs = files.filter((f) => isDocument(f.path)).slice(0, MAX_DOCUMENT_CARDS);
  const rest = files.filter((f) => !isDocument(f.path));
  return files.length ? (
    <div className="mt-2 flex flex-col gap-1.5">
      {rest.length > 0 && <FileCards files={rest} mid={mid} align="start" cwd={cwd} onOpenFile={onOpenFile} resolveDownloadURL={resolveDownloadURL} />}
      {docs.length > 0 && <DocumentCards files={docs} mid={mid} cwd={cwd} onOpenFile={onOpenFile} resolveThumbURL={resolveThumbURL} />}
    </div>
  ) : null;
}

function DefaultReplyFiles(props: ReplyFilesProps) {
  const paths = useMemo(() => replyPaths(props.text), [props.text]);
  const { stat, key } = usePathLinks(props.mid, props.cwd, paths);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const files = useMemo(() => statFiles(paths, stat), [paths, key]);
  return renderReplyFiles(files, props);
}

/** Files an answer names by absolute path that are there on the machine, as cards under it (iOS links the same). */
export function ReplyFiles(props: ReplyFilesProps) {
  if (props.files !== undefined) return renderReplyFiles(props.files, props);
  if (props.resolvePathStat) return renderReplyFiles(statFiles(replyPaths(props.text), props.resolvePathStat), props);
  return <DefaultReplyFiles {...props} />;
}

export interface DocumentCardsProps {
  files: AttachedFile[];
  mid: string;
  cwd?: string;
  /** Controlled file opener replacing the default side-panel singleton. */
  onOpenFile?: (file: AttachedFile) => void;
  /** Controlled thumbnail URL resolver replacing the default fileThumbURL builder. */
  resolveThumbURL?: (path: string, size: number) => string;
}

/** The documents an answer names, as cards with their first page, in a row that scrolls sideways (iOS DocumentCards). */
export function DocumentCards({ files, mid, cwd, onOpenFile, resolveThumbURL }: DocumentCardsProps) {
  const row = useRef<HTMLDivElement>(null);
  const { edges, measure } = useEdgeFade(row, [files.length]);
  const mask = edgeMask(edges);
  return (
    <div
      ref={row}
      onScroll={measure}
      className="flex max-w-full gap-2 overflow-x-auto overscroll-x-contain py-px [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      style={{ maskImage: mask, WebkitMaskImage: mask }}
    >
      {files.map((f) => (
        <DocumentCard key={`${mid}\0${cwd ?? ""}\0${f.path}`} f={f} mid={mid} cwd={cwd} onOpenFile={onOpenFile} resolveThumbURL={resolveThumbURL} />
      ))}
    </div>
  );
}

function DocumentCard({
  f,
  mid,
  cwd,
  onOpenFile,
  resolveThumbURL,
}: {
  f: AttachedFile;
  mid: string;
  cwd?: string;
  onOpenFile?: DocumentCardsProps["onOpenFile"];
  resolveThumbURL?: DocumentCardsProps["resolveThumbURL"];
}) {
  const readOnly = useReadOnlyFile();
  // no thumbnail (an old hub, a machine that cannot draw it, the file gone) or a dark page (Quick Look in dark mode):
  // the file's glyph, as iOS falls back to its symbol
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [mid, cwd, f.path, resolveThumbURL]);
  return (
    <button
      type="button"
      onClick={() => (onOpenFile ? onOpenFile(f) : openFilePanel(mid, f.path, cwd, readOnly))}
      title={f.path}
      className="flex h-16 w-60 max-w-full shrink-0 items-center gap-3 rounded-[14px] bg-inset px-2 text-left shadow-hairline outline-none hover:bg-hover focus-visible:ring-2 focus-visible:ring-accent"
      style={{ animation: "fade-in 220ms ease-out both" }}
      data-testid="document-card"
    >
      <span className="flex h-12 w-9 shrink-0 items-center justify-center">
        {failed ? (
          <FileGlyph name={f.name} size={18} />
        ) : (
          <img
            src={resolveThumbURL ? resolveThumbURL(f.path, 128) : fileThumbURL(mid, f.path, cwd, 128)}
            alt=""
            loading="lazy"
            decoding="async"
            onLoad={(e) => isDarkPage(e.currentTarget) && setFailed(true)}
            onError={() => setFailed(true)}
            className="max-h-full max-w-full rounded-[4px] bg-white object-contain shadow-hairline"
          />
        )}
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="truncate text-[13px] font-medium text-ink">{f.name}</span>
        <span className="truncate text-[11.5px] text-ink-3">{documentKind(f.path)}</span>
      </span>
    </button>
  );
}
