// The context menu of a node in a tree of machines (#2329): what src/lib/node-actions.ts builds, in a Menu that opens
// at the pointer for a right click and beside the row for its ⋯, the menu key and Shift+F10.
import { useMemo } from "react";
import { defaultNodeDeps, nodeActions, type MenuNode, type NodeActionOptions } from "../lib/node-actions";
import { refresh } from "../lib/store";
import { Menu } from "../ui/atoms";
import type { MenuAt, MenuItem } from "../ui/menu-model";

export function NodeMenu({
  node,
  at,
  onClose,
  header,
  before,
  omit,
  changed,
}: {
  node: MenuNode;
  at: MenuAt;
  onClose: () => void;
  header?: string;
  /** Rows of the node's own menu that come first (an agent's "Pin", "Rename…"), the node's items after a hairline. */
  before?: MenuItem[];
  omit?: NodeActionOptions["omit"];
  /** The tree changed on the computer: read it again (the sessions are, by default). */
  changed?: () => void;
}) {
  const deps = useMemo(() => defaultNodeDeps(changed ?? (() => void refresh().catch(() => {}))), [changed]);
  const own = nodeActions(node, deps, { omit });
  const items = before?.length && own.length ? [...before, { ...own[0], divider: true }, ...own.slice(1)] : [...(before ?? []), ...own];
  // the header is the node's name (a space's, a tab's, a pane's): as written, not in a label's capitals
  return <Menu anchor={at.anchor ?? null} point={at.point} onClose={onClose} align="right" width={224} header={header} headerAsWritten items={items} />;
}
