// The files section of ⌘K (docs/web/web.md "Command palette"): the files under the conversation's (or the open terminal pane's)
// directory, found as you type. Palette.tsx lists the rows this hook returns after its own and draws them with
// FileResultRow; the matching and the loading are in lib/file-find.ts.
import { forwardRef, useEffect, useMemo, useState } from "react";
import { t } from "../../i18n";
import { cachedIndex, createFileSearch, fileContext, highlightRuns, hitPath, indexFresh, loadFileIndex, mentionOf, type FileContext, type FileHit, type FileIndex } from "../../lib/file-find";
import { openPanel } from "../../lib/panel";
import type { MachinePanes } from "../../lib/palette";
import type { Route } from "../../lib/router";
import { getSession } from "../../lib/store";
import { copyText } from "../../ui/atoms";
import { FileGlyph } from "../session/files/file-glyph";

/** One selectable row of the files section. */
export interface FileRow {
  kind: "file";
  key: string;
  hit: FileHit;
  ctx: FileContext;
}

export type FilesStatus = "off" | "loading" | "ready" | "error";

export interface PaletteFiles {
  /** Where the files are searched; null where there is nothing to search in (the section is not there at all). */
  ctx: FileContext | null;
  status: FilesStatus;
  rows: FileRow[];
}

/**
 * The files the typed text finds. The list is fetched when the palette opens (so it is there by the time something is
 * typed) and held for a while; each keystroke narrows the last one's matches.
 */
export function usePaletteFiles(o: { open: boolean; jump: boolean; text: string; route: Route; pane?: { mid: string; sid: string }; machinePanes: MachinePanes[] }): PaletteFiles {
  const { open, jump, text, route, pane, machinePanes } = o;
  const mid = route.name === "terminal" ? route.mid : undefined;
  const id = route.name === "terminal" ? route.id : undefined;
  const spaces = machinePanes.find((m) => m.mid === mid)?.spaces;
  // eslint-disable-next-line react-hooks/exhaustive-deps -- the parts of `route` that matter are mid / id
  const ctx = useMemo(() => fileContext(route, pane, (m, s) => getSession(m, s)?.cwd, () => spaces), [route.name, mid, id, pane?.mid, pane?.sid, spaces]);
  const key = ctx ? `${ctx.mid}\n${ctx.cwd}` : "";

  const [loaded, setLoaded] = useState<{ key: string; index: FileIndex } | null>(null);
  const [failed, setFailed] = useState("");
  useEffect(() => {
    if (!open || !jump || !ctx) return;
    let live = true;
    const held = cachedIndex(ctx);
    if (held) setLoaded({ key, index: held });
    if (indexFresh(ctx)) return;
    setFailed("");
    loadFileIndex(ctx).then(
      (index) => live && setLoaded({ key, index }),
      () => live && setFailed(key),
    );
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- ctx is its key
  }, [open, jump, key]);

  const index = loaded?.key === key ? loaded.index : undefined;
  const search = useMemo(() => (index ? createFileSearch(index.files) : null), [index]);
  const rows = useMemo<FileRow[]>(() => {
    if (!open || !jump || !ctx || !search || !text) return [];
    return search.search(text).map((hit) => ({ kind: "file" as const, key: `f:${hit.path}`, hit, ctx }));
  }, [open, jump, ctx, search, text]);

  const status: FilesStatus = !ctx || !jump ? "off" : index ? "ready" : failed === key ? "error" : "loading";
  return { ctx, status, rows };
}

/** Opens the hit in the page's file viewer (the side panel). */
export function openFileHit(row: FileRow) {
  openPanel({ kind: "file", mid: row.ctx.mid, path: hitPath(row.ctx, row.hit), cwd: row.ctx.cwd });
}

/** Puts `@path` on the clipboard, the way a file is named in a message. */
export async function copyFileMention(row: FileRow) {
  await copyText(mentionOf(row.hit)); // says "Copied" itself
}

function Marked({ text, offset, hit, className }: { text: string; offset: number; hit: FileHit; className: string }) {
  return (
    <>
      {highlightRuns(text, offset, hit.matched).map((r, i) =>
        r.hit ? (
          <b key={i} className={`font-semibold ${className}`}>
            {r.text}
          </b>
        ) : (
          <span key={i}>{r.text}</span>
        ),
      )}
    </>
  );
}

/** The heading the first file row stands under (the command list's headings are drawn the same way). */
export function FilesHeading() {
  return <div className="px-2.5 pt-2 pb-1 text-[10.5px] font-medium tracking-[0.06em] text-ink-3 uppercase">{t("files-find.heading")}</div>;
}

/** A file in the list: its name with the matched letters marked, and the folders it is in beneath. */
export const FileResultRow = forwardRef<HTMLButtonElement, { row: FileRow; className: string; onMouseMove: () => void; onClick: () => void; option?: { id: string; role: "option"; "aria-selected": boolean } }>(function FileResultRow({ row, className, onMouseMove, onClick, option }, ref) {
  const { hit } = row;
  const dirLen = hit.path.lastIndexOf("/") + 1;
  const dir = hit.path.slice(0, dirLen);
  const name = hit.path.slice(dirLen);
  return (
    <button ref={ref} type="button" {...option} onMouseMove={onMouseMove} onClick={onClick} className={className}>
      <span className="flex size-5 shrink-0 items-center justify-center">
        <FileGlyph name={name} size={13} />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] font-medium text-ink">
          <Marked text={name} offset={dirLen} hit={hit} className="text-accent-ink" />
        </span>
        {dir && (
          <span className="block truncate text-[11.5px] text-ink-3">
            <Marked text={dir.replace(/\/$/, "")} offset={0} hit={hit} className="text-accent-ink" />
          </span>
        )}
      </span>
    </button>
  );
});

/** Under the rows: that the files are being listed, or that they cannot be. */
export function FilesNotice({ status, text, count }: { status: FilesStatus; text: string; count: number }) {
  if (!text) return null;
  if (status === "loading") {
    return (
      <div role="status" className="px-2.5 py-2 text-[12px] text-ink-3">
        {t("files-find.indexing")}
      </div>
    );
  }
  if (status === "error" && count === 0) {
    return (
      <div role="status" className="px-2.5 py-2 text-[12px] text-ink-3">
        {t("files-find.unreadable")}
      </div>
    );
  }
  return null;
}
