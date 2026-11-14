// Which row of a settings list is the item open beside it (Settings' two columns, audit item 68).
import { otherManager, parseOwnedSub, sameManager } from "../../lib/manager-choice";

/**
 * Whether `open` (the route of the item open beside the list, `<id>` or `<id>@<manager>`) is the row's item: `id`, of
 * `manager` (none: the default's). A route that names no manager opens the current assistant's copy (`picked`), as the
 * item's own page does (Connectors, ChannelPage, Extensions' OwnedExtension), so that copy's row is the one marked.
 */
export function isOpenBeside(open: string | null | undefined, id: string, manager: string | undefined, picked: string | null | undefined): boolean {
  if (!open) return false;
  const at = parseOwnedSub(open);
  return at.id === id && sameManager(at.manager ?? otherManager(picked ?? undefined), manager);
}
