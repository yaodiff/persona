// The context menu of a node in a tree of machines (#2329): what src/lib/node-actions.ts builds, in a Menu that opens
// at the pointer for a right click and beside the row for its ⋯, the menu key and Shift+F10.
import { useCallback, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { defaultNodeDeps, nodeActions, type MenuNode, type NodeActionDeps, type NodeActionOptions } from "../lib/node-actions";
import { groupAgentMenu } from "./agent-menu-layout";
import { refresh } from "../lib/store";
import { Menu } from "../ui/atoms";
import type { MenuAt, MenuItem } from "../ui/menu-model";

export interface NodeMenuDeps {
  /** Optional scope partition (value or getter); switching it remounts the menu and fences stale actions/callbacks. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Controlled node action dependencies (bypasses `defaultNodeDeps` and `store.refresh`). */
  nodeDeps?: NodeActionDeps;
  /** Custom action builder (defaults to `nodeActions`). */
  buildActions?: (node: MenuNode, deps: NodeActionDeps, opts: NodeActionOptions) => MenuItem[];
  /** Pre-built node menu items (bypasses `defaultNodeDeps`, `store.refresh`, and `nodeActions`). */
  actions?: readonly MenuItem[];
  /** Default refresh fallback when `changed` is omitted and `nodeDeps` is not supplied (defaults to `store.refresh`). */
  refresh?: () => void | Promise<unknown>;
  /** Custom menu renderer for pure/controlled host shells (defaults to `<NodeMenuView>`). */
  renderMenu?: (props: NodeMenuViewProps) => ReactNode;
}

export interface NodeMenuProps {
  node: MenuNode;
  at: MenuAt;
  onClose: () => void;
  header?: string;
  /** Rows of the node's own menu that come first (an agent's "Pin", "Rename…"), the node's items after a hairline. */
  before?: MenuItem[];
  omit?: NodeActionOptions["omit"];
  /** The tree changed on the computer: read it again (the sessions are, by default). */
  changed?: () => void;
  deps?: NodeMenuDeps;
}

export interface NodeMenuViewProps {
  at: MenuAt;
  onClose: () => void;
  header?: string;
  items: MenuItem[];
  menuKey?: string | number;
}

const noopSubscribe = () => () => {};

/** Pure presentational view for a node context menu. */
export function NodeMenuView({ at, onClose, header, items, menuKey }: NodeMenuViewProps) {
  // the header is the node's name (a space's, a tab's, a pane's): as written, not in a label's capitals
  return <Menu key={menuKey} anchor={at.anchor ?? null} point={at.point} onClose={onClose} align="right" width={224} header={header} headerAsWritten items={items} />;
}

function useTrackedScope(subscribe: (listener: () => void) => () => void, read: () => string) {
  const stateRef = useRef<{ value: string; version: number } | null>(null);
  const sub = useCallback((listener: () => void) => subscribe(() => {
    const next = read();
    if (!stateRef.current || next !== stateRef.current.value) {
      stateRef.current = { value: next, version: (stateRef.current?.version ?? 0) + 1 };
    }
    listener();
  }), [subscribe, read]);
  const getSnapshot = useCallback(() => {
    const next = read();
    if (!stateRef.current) {
      stateRef.current = { value: next, version: 0 };
    } else if (next !== stateRef.current.value) {
      stateRef.current = { value: next, version: stateRef.current.version + 1 };
    }
    return stateRef.current;
  }, [read]);
  const snap = useSyncExternalStore(sub, getSnapshot, getSnapshot);
  const readToken = useCallback(() => {
    const cur = read();
    const s = stateRef.current;
    return s && s.value === cur ? `${s.version}:${cur}` : `drift:${cur}`;
  }, [read]);
  return { token: `${snap.version}:${snap.value}`, readToken };
}

function fenceAsync<T>(isCurrent: () => boolean, run: () => Promise<T>, fallback: T): Promise<T> {
  if (!isCurrent()) return Promise.resolve(fallback);
  let pending: Promise<T>;
  try {
    pending = Promise.resolve(run());
  } catch (error) {
    return isCurrent() ? Promise.reject(error) : Promise.resolve(fallback);
  }
  return pending.then(
    (value) => (isCurrent() ? value : fallback),
    (error) => (isCurrent() ? Promise.reject(error) : fallback),
  );
}

function fenceNodeDeps(raw: NodeActionDeps, isCurrent: () => boolean): NodeActionDeps {
  return {
    post: (mid, path, body) => fenceAsync(isCurrent, () => raw.post(mid, path, body), undefined),
    renameWorkspace: (n, name) => fenceAsync(isCurrent, () => raw.renameWorkspace(n, name), undefined),
    renameTab: (n, name) => fenceAsync(isCurrent, () => raw.renameTab(n, name), undefined),
    launchers: (mid) => fenceAsync(isCurrent, () => raw.launchers(mid), []),
    newAgent: (mid, cwd) => { if (isCurrent()) raw.newAgent(mid, cwd); },
    ...(raw.newWorktree ? { newWorktree: (n: MenuNode) => { if (isCurrent()) raw.newWorktree!(n); } } : {}),
    openSession: (mid, sid) => { if (isCurrent()) raw.openSession(mid, sid); },
    copy: (text) => { if (isCurrent()) raw.copy(text); },
    notify: (text, tone) => { if (isCurrent()) raw.notify(text, tone); },
    changed: () => { if (isCurrent()) raw.changed(); },
    ...(raw.closeAgent ? {
      closeAgent: (mid: string, sid: string) => {
        const action = raw.closeAgent!(mid, sid);
        return action ? { ...action, onSelect: () => { if (isCurrent()) return action.onSelect(); } } : undefined;
      },
    } : {}),
  };
}

function fenceMenuItem(item: MenuItem, isCurrent: () => boolean): MenuItem {
  const onSelect = () => {
    if (!isCurrent()) return;
    if (!item.inline) return item.onSelect();
    return fenceAsync(isCurrent, () => Promise.resolve(item.onSelect()), undefined);
  };
  const children = !item.children
    ? undefined
    : typeof item.children === "function"
      ? () => fenceAsync(isCurrent, () => (item.children as () => Promise<MenuItem[]>)(), []).then((sub) => (isCurrent() ? fenceMenuItems(sub, isCurrent) : []))
      : fenceMenuItems(item.children, isCurrent);
  const rename = item.rename
    ? {
        ...item.rename,
        commit: (name: string) => fenceAsync(isCurrent, () => Promise.resolve(item.rename!.commit(name)), undefined),
      }
    : undefined;
  return {
    ...item,
    onSelect,
    ...(children !== undefined ? { children } : {}),
    ...(rename !== undefined ? { rename } : {}),
  };
}

function fenceMenuItems(items: readonly MenuItem[], isCurrent: () => boolean): MenuItem[] {
  return items.map((item) => fenceMenuItem(item, isCurrent));
}

export function NodeMenu({ node, at, onClose, header, before, omit, changed, deps }: NodeMenuProps) {
  const scopeDep = deps?.scopeKey;
  const readScope = useCallback(() => (typeof scopeDep === "function" ? scopeDep() : (scopeDep ?? "")), [scopeDep]);
  const scopeSubscribe = deps?.subscribeScopeKey && typeof scopeDep === "function" ? deps.subscribeScopeKey : noopSubscribe;
  const trackedScope = useTrackedScope(scopeSubscribe, readScope);

  const nodeScope = JSON.stringify([trackedScope.token, node.kind, node.mid, node.workspaceId ?? "", node.tabId ?? "", node.paneId ?? "", node.sessionId ?? "", node.host ?? ""]);
  const [scope, setScope] = useState<{ key: string; epoch: number }>(() => ({ key: nodeScope, epoch: 1 }));
  if (scope.key !== nodeScope) {
    setScope({ key: nodeScope, epoch: scope.epoch + 1 });
  }

  const activeEpochRef = useRef(scope.epoch);
  const onCloseRef = useRef(onClose);
  const readScopeTokenRef = useRef(trackedScope.readToken);
  const refreshDep = deps?.refresh;
  useLayoutEffect(() => {
    onCloseRef.current = onClose;
    readScopeTokenRef.current = trackedScope.readToken;
  });
  useLayoutEffect(() => {
    activeEpochRef.current = scope.epoch;
    return () => {
      if (activeEpochRef.current === scope.epoch) activeEpochRef.current = 0;
    };
  }, [scope.epoch]);

  const capturedEpoch = scope.epoch;
  const capturedScope = trackedScope.token;
  const isCurrent = () => activeEpochRef.current === capturedEpoch && readScopeTokenRef.current() === capturedScope;
  const fencedOnClose = () => {
    if (activeEpochRef.current === capturedEpoch) onCloseRef.current();
  };

  const customNodeDeps = deps?.nodeDeps;
  const hasCustomActions = deps?.actions !== undefined;
  const baseDeps = useMemo(
    () => (hasCustomActions ? undefined : customNodeDeps ?? defaultNodeDeps(changed ?? (() => void Promise.resolve((refreshDep ?? refresh)()).catch(() => {})))),
    [hasCustomActions, customNodeDeps, changed, refreshDep],
  );

  let own: MenuItem[];
  if (deps?.actions !== undefined) {
    own = [...deps.actions];
  } else {
    const guardedNode: MenuNode = {
      ...node,
      ...(node.fold ? { fold: { ...node.fold, toggle: () => { if (isCurrent()) node.fold!.toggle(); } } } : {}),
      ...(node.onOpenShell ? { onOpenShell: () => { if (isCurrent()) node.onOpenShell!(); } } : {}),
      ...(node.onRefresh ? { onRefresh: () => { if (isCurrent()) node.onRefresh!(); } } : {}),
    };
    own = (deps?.buildActions ?? nodeActions)(guardedNode, fenceNodeDeps(baseDeps!, isCurrent), { omit });
  }

  const merged = node.sessionOnly && before
    ? groupAgentMenu(before, own)
    : before?.length && own.length ? [...before, { ...own[0], divider: true }, ...own.slice(1)] : [...(before ?? []), ...own];
  const items = fenceMenuItems(merged, isCurrent);
  const viewProps: NodeMenuViewProps = { at, onClose: fencedOnClose, header, items, menuKey: scope.epoch };
  if (deps?.renderMenu) return <>{deps.renderMenu(viewProps)}</>;
  return <NodeMenuView {...viewProps} />;
}
