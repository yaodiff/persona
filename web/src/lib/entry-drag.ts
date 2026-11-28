// Dragging an entry of the Files panel into a terminal or the chat composer (docs/web/web.md "Side panel"): the row carries
// its machine, absolute path and kind under a custom type; the target types `@path ` (the agents read that as the
// file). The pure parts — what a row carries, the path as the target sees it, whether the drop goes through. The
// terminal's side is in views/session/terminal/feed.ts, the composer's in views/chat/Composer.tsx.
import { dragHasFiles, escapePastePath } from "./paste-path";
import { below } from "./files";
import { t } from "../i18n";

/** The drag type of a Files panel entry (Moshi's is `application/x-moshi-file`). */
export const ENTRY_DRAG_TYPE = "application/x-persona-file";

/** What a dragged row carries. */
export interface DraggedEntry {
  mid: string;
  /** Absolute, on that machine. */
  path: string;
  dir: boolean;
}

/** The payload a row puts on the drag. */
export function entryPayload(entry: DraggedEntry): string {
  return JSON.stringify({ mid: entry.mid, path: entry.path, dir: entry.dir });
}

/** The entry a payload describes, null for anything else (another page's drag, a mangled value). */
export function parseEntry(raw: string): DraggedEntry | null {
  try {
    const v: unknown = JSON.parse(raw);
    if (!v || typeof v !== "object") return null;
    const { mid, path, dir } = v as Record<string, unknown>;
    if (typeof mid !== "string" || !mid || typeof path !== "string" || !path) return null;
    return { mid, path, dir: dir === true };
  } catch {
    return null;
  }
}

/** Whether a drag carries an entry (decided while dragging, when only the types are readable). */
export function dragHasEntry(data: Pick<DataTransfer, "types"> | null | undefined): boolean {
  return Boolean(data && Array.from(data.types ?? []).includes(ENTRY_DRAG_TYPE));
}

/** Whether a terminal takes the drag: files from the desktop (uploaded) or an entry of the Files panel (mentioned). */
export function dragIsDroppable(data: Pick<DataTransfer, "types"> | null | undefined): boolean {
  return dragHasFiles(data) || dragHasEntry(data);
}

/** The entry a drop carries, null when it carries none. */
export function droppedEntry(data: Pick<DataTransfer, "getData"> | null | undefined): DraggedEntry | null {
  return data ? parseEntry(data.getData(ENTRY_DRAG_TYPE)) : null;
}

/**
 * The entry's path as the target sees it: relative to `cwd` when it lies inside (the folder itself is "."), else the
 * absolute path. A folder ends in a slash.
 */
export function entryPath(entry: DraggedEntry, cwd: string): string {
  const abs = entry.path.replace(/\/+$/, "") || "/";
  const inside = cwd ? below(cwd, abs) : null;
  const path = inside === null ? abs : inside.length ? inside.join("/") : ".";
  return entry.dir && !path.endsWith("/") ? path + "/" : path;
}

/** What a drop on a terminal does: types the mention, or says why not. */
export type EntryDrop = { action: "paste"; text: string } | { action: "refuse"; toast: string };

/**
 * A drop on a terminal. Only while this browser drives the pane; a file on another computer cannot be named there.
 * The path is escaped as a shell word, the way pasted files are.
 */
export function terminalEntryDrop(entry: DraggedEntry, target: { mid: string; cwd: string; driving: boolean }): EntryDrop {
  if (!target.driving) return { action: "refuse", toast: t("lib-entry-drag.drive_first") };
  if (entry.mid !== target.mid) return { action: "refuse", toast: t("lib-entry-drag.other_computer_terminal") };
  return { action: "paste", text: `@${escapePastePath(entryPath(entry, target.cwd))} ` };
}

/** A drop on the chat composer: the same `@path ` the composer's @ menu types (relative to the session's folder, as is). */
export function composerEntryDrop(entry: DraggedEntry, target: { mid?: string; cwd?: string }): EntryDrop {
  if (entry.mid !== target.mid) return { action: "refuse", toast: t("lib-entry-drag.other_computer_session") };
  return { action: "paste", text: `@${entryPath(entry, target.cwd ?? "")} ` };
}
