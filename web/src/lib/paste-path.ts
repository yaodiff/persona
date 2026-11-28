// The two pieces of the terminal's file drop that the first screen needs too, for dropping a file or a Files entry on a
// chat's composer (lib/entry-drag.ts): whether a drag carries files, and a path as a shell word. The rest of the
// terminal's clipboard, with its words, is lib/terminal-clipboard.ts, which only the terminal's own pages load.

/** A path as a shell word: everything but letters, digits and `@%+=:,./-` (and non-ASCII) gets a backslash. */
export function escapePastePath(path: string): string {
  return path.replace(/[^\w@%+=:,./\-\u{80}-\u{10FFFF}]/gu, (m) => "\\" + m);
}

/** Whether a drag has files in it (decided while dragging, when the files themselves are not readable yet). */
export function dragHasFiles(data: Pick<DataTransfer, "types"> | null | undefined): boolean {
  return Boolean(data && Array.from(data.types ?? []).includes("Files"));
}
