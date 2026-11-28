// The inbox's folder filter (docs/assistant/inbox.md "Folders"), apart from inbox-folders.ts's tree and settings: the
// first screen's lib/inbox.ts reads only this, and the rest is the inbox page's.

/** The folder filter's value for items in no folder (a real path never starts with "/"). */
export const UNFILED = "/";

/** The parts of an item the folder code reads. */
export type Filed = { folder?: string; done_at?: string | null };

/** Whether an item is in the folder filter: under the folder (or in it), or in none for UNFILED; "" is every item. */
export function inFolderFilter(it: Filed, folder: string): boolean {
  if (!folder) return true;
  const path = it.folder ?? "";
  if (folder === UNFILED) return !path;
  return path === folder || path.startsWith(`${folder}/`);
}
