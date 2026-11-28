// Loaded when a sidebar conversation menu opens; the first screen does not carry its delete and arrange controls (#12017).
import { lazy, Suspense, useCallback, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { COMPACT_FEATURE, NEW_MAIN_FEATURE } from "../lib/compaction";
import { useCanDeleteConversation } from "../lib/conversation-delete";
import { CLEAR_MAIN_FEATURE, clearMainContext, useMainClearing } from "../lib/main-clear";
import { navigate, routeAt, type Route } from "../lib/router";
import { sameValue } from "../lib/stable-lines";
import { getState, hubHas, useStore } from "../lib/store";
import type { Session } from "../lib/types";
import { Dialog, Loading, Menu, type MenuItem, type ToastAction } from "../ui/atoms";
import type { MenuAt } from "../ui/menu-model";
import { t } from "../i18n";
import { ConversationDeleteConfirmation, conversationMenuDeps, conversationMenuItems, type ConversationMenuDeps, type ConversationMenuOptions } from "./ConversationMenu";
import type { DeleteConversationDialogDeps } from "./DeleteConversationDialog";
import { lazyDialog } from "./LazyOverlay";
import type { NewMainDialogDeps } from "./assistant/NewMainDialog";

const LazyDeleteConversationDialog = lazy(() => import("./DeleteConversationDialog").then((m) => ({ default: m.DeleteConversationDialog })));
const LazyNewMainDialog = lazyDialog(() => import("./assistant/NewMainDialog").then((m) => m.NewMainDialog));

export interface MainCapabilities {
  canCompact: boolean;
  canClearMain: boolean;
  canNewMain: boolean;
  clearingMain: boolean;
}

const NO_MAIN_CAPABILITIES: MainCapabilities = {
  canCompact: false,
  canClearMain: false,
  canNewMain: false,
  clearingMain: false,
};

export interface ConversationRowMenuViewProps {
  s: Session;
  at: MenuAt;
  items: MenuItem[];
  deleting: boolean;
  startingNewMain?: boolean;
  onClose: () => void;
  onNewMainClose?: () => void;
  onNavigate?: () => void;
  menuKey?: string | number;
  dialogKey?: string | number;
  newMainDialogKey?: string | number;
  newMainScopeKey?: string;
  deleteDialogDeps?: DeleteConversationDialogDeps;
  newMainDialogDeps?: NewMainDialogDeps;
  renderMenu?: (props: {
    anchor: HTMLElement | null;
    point?: { x: number; y: number };
    onClose: () => void;
    items: MenuItem[];
    menuKey?: string | number;
  }) => ReactNode;
  renderDeleteConfirmation?: (props: {
    s: Session;
    onClose: () => void;
    dialogKey?: string | number;
  }) => ReactNode;
  renderNewMainDialog?: (props: {
    s: Session;
    onClose: () => void;
    dialogKey?: string | number;
  }) => ReactNode;
}

export interface ConversationRowMenuDeps {
  /** Optional scope partition (value or getter); switching it resets dialogs and fences stale callbacks. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Controlled delete eligibility or reader (bypasses `useCanDeleteConversation` when supplied). */
  canDelete?: boolean | ((s: Session, options: { main: boolean; builtIn?: boolean }) => boolean);
  /** Subscribe to delete eligibility updates when `canDelete` is a reader. */
  subscribeCanDelete?: (listener: () => void) => () => void;
  /** Controlled main-chat capabilities or reader (bypasses `useStore` when supplied). */
  mainCapabilities?: Partial<MainCapabilities> | ((s: Session, options: { main: boolean; builtIn?: boolean }) => Partial<MainCapabilities>);
  /** Subscribe to main-chat capability updates when `mainCapabilities` is a reader. */
  subscribeMainCapabilities?: (listener: () => void) => () => void;
  /** Controlled delete-confirmation mode. */
  deleting?: boolean;
  /** Invoked when delete-confirmation mode toggles or resets on scope change. */
  onDeletingChange?: (deleting: boolean) => void;
  /** Controlled new-main confirmation mode. */
  startingNewMain?: boolean;
  /** Invoked when new-main confirmation mode toggles or resets on scope change. */
  onStartingNewMainChange?: (starting: boolean) => void;
  /** Partial or full overrides for `ConversationMenuDeps` passed to `conversationMenuItems`. */
  menuDeps?: Partial<ConversationMenuDeps>;
  /** Controlled pre-built base menu items or reader (bypasses `conversationMenuItems` when supplied). */
  items?: readonly MenuItem[] | (() => readonly MenuItem[]);
  /** Subscribe to controlled items updates when `items` is a getter. */
  subscribeItems?: (listener: () => void) => () => void;
  /** Custom menu item builder (replaces `conversationMenuItems` when `items` is omitted). */
  buildItems?: (
    s: Session,
    options: ConversationMenuOptions & { canDelete: boolean; onDelete: () => void },
    deps: ConversationMenuDeps,
  ) => MenuItem[];
  /** Navigate to another route (defaults to `router.navigate`). */
  navigate?: (to: Route) => void;
  /** Controlled dependencies forwarded to `DeleteConversationDialog` when confirming deletion. */
  deleteDialogDeps?: DeleteConversationDialogDeps;
  /** Controlled dependencies forwarded to `NewMainDialog` when confirming a new main chat. */
  newMainDialogDeps?: NewMainDialogDeps;
  /** Custom outer view renderer (defaults to `<ConversationRowMenuView>`). */
  renderView?: (props: ConversationRowMenuViewProps) => ReactNode;
  renderMenu?: ConversationRowMenuViewProps["renderMenu"];
  renderDeleteConfirmation?: ConversationRowMenuViewProps["renderDeleteConfirmation"];
  renderNewMainDialog?: ConversationRowMenuViewProps["renderNewMainDialog"];
}

export interface ConversationRowMenuProps {
  s: Session;
  main: boolean;
  leader?: boolean;
  builtIn?: boolean;
  anchor: HTMLElement;
  at?: MenuAt;
  onClose: () => void;
  onArrange?: () => void;
  onNavigate?: () => void;
  deps?: ConversationRowMenuDeps;
}

const noopSubscribe = () => () => {};

function isThenable(value: unknown): value is PromiseLike<unknown> {
  return value !== null && (typeof value === "object" || typeof value === "function") && typeof (value as PromiseLike<unknown>).then === "function";
}

function useTrackedScope(
  source: string | (() => string) | undefined,
  subscribe: ((listener: () => void) => () => void) | undefined,
) {
  const isGetter = typeof source === "function";
  const read = useCallback(() => (typeof source === "function" ? source() : (source ?? "")), [source]);
  const committedRef = useRef<{ value: string; version: number } | null>(null);
  const snapRef = useRef<{ value: string; version: number } | null>(null);
  const sub = useCallback((listener: () => void) => {
    if (!subscribe || !isGetter) return noopSubscribe();
    return subscribe(() => {
      const next = read();
      const cur = committedRef.current;
      if (!cur || next !== cur.value) {
        committedRef.current = { value: next, version: (cur?.version ?? 0) + 1 };
      }
      listener();
    });
  }, [subscribe, isGetter, read]);
  const getSnapshot = useCallback(() => {
    const next = read();
    const cur = committedRef.current;
    if (cur && next === cur.value) return cur;
    const nextVersion = cur ? cur.version + 1 : 0;
    if (snapRef.current && snapRef.current.value === next && snapRef.current.version === nextVersion) {
      return snapRef.current;
    }
    snapRef.current = { value: next, version: nextVersion };
    return snapRef.current;
  }, [read]);
  const snap = useSyncExternalStore(sub, getSnapshot, getSnapshot);
  useLayoutEffect(() => {
    committedRef.current = snap;
  }, [snap]);
  const readToken = useCallback(() => {
    const cur = read();
    const committed = committedRef.current;
    return committed && committed.value === cur ? `${committed.version}:${cur}` : `drift:${cur}`;
  }, [read]);
  return { value: snap.value, token: `${snap.version}:${snap.value}`, readToken };
}

function useCachedExternalValue<T>(
  source: T | (() => T) | undefined,
  subscribe: ((listener: () => void) => () => void) | undefined,
): T | undefined {
  const cache = useRef<{ has: boolean; value: T | undefined }>({ has: false, value: undefined });
  const getSnapshot = useCallback((): T | undefined => {
    const next = typeof source === "function" ? (source as () => T)() : source;
    if (cache.current.has && sameValue(cache.current.value, next)) {
      return cache.current.value;
    }
    cache.current = { has: true, value: next };
    return next;
  }, [source]);
  const sub = subscribe && typeof source === "function" ? subscribe : noopSubscribe;
  return useSyncExternalStore(sub, getSnapshot, getSnapshot);
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

function runSingleFlight<T>(
  pending: Set<string>,
  key: string,
  isCurrent: () => boolean,
  isOpCurrent: () => boolean,
  run: () => T,
): T | Promise<unknown> | undefined {
  if (!isCurrent() || pending.has(key)) return undefined;
  pending.add(key);
  let result: T;
  try {
    result = run();
  } catch (error) {
    pending.delete(key);
    if (isOpCurrent()) throw error;
    return undefined;
  }
  if (!isThenable(result)) {
    pending.delete(key);
    return result;
  }
  return Promise.resolve(result).then(
    (value) => {
      pending.delete(key);
      return isOpCurrent() ? value : undefined;
    },
    (error) => {
      pending.delete(key);
      if (isOpCurrent()) return Promise.reject(error);
      return undefined;
    },
  );
}

function fenceMenuItem(
  item: MenuItem,
  epoch: number,
  pending: Set<string>,
  isMenuCurrent: () => boolean,
  isOpCurrent: () => boolean,
): MenuItem {
  const selectKey = `${epoch}:select:${item.key}`;
  const renameKey = `${epoch}:rename:${item.key}`;
  const onSelect = () => runSingleFlight(pending, selectKey, isMenuCurrent, isOpCurrent, () => item.onSelect());
  const children = !item.children
    ? undefined
    : typeof item.children === "function"
      ? () => fenceAsync(isMenuCurrent, () => (item.children as () => Promise<MenuItem[]>)(), [])
          .then((sub) => (isMenuCurrent() ? fenceMenuItems(sub, epoch, pending, isMenuCurrent, isOpCurrent) : []))
      : fenceMenuItems(item.children, epoch, pending, isMenuCurrent, isOpCurrent);
  const rename = item.rename
    ? {
        ...item.rename,
        commit: (name: string) =>
          Promise.resolve(runSingleFlight(pending, renameKey, isMenuCurrent, isOpCurrent, () => item.rename!.commit(name))) as Promise<void>,
      }
    : undefined;
  return {
    ...item,
    onSelect,
    ...(children !== undefined ? { children } : {}),
    ...(rename !== undefined ? { rename } : {}),
  };
}

function fenceMenuItems(
  items: readonly MenuItem[],
  epoch: number,
  pending: Set<string>,
  isMenuCurrent: () => boolean,
  isOpCurrent: () => boolean,
): MenuItem[] {
  return items.map((item) => fenceMenuItem(item, epoch, pending, isMenuCurrent, isOpCurrent));
}

/** Pure presentational view for a sidebar conversation row menu and its confirmation dialogs. */
export function ConversationRowMenuView({
  s,
  at,
  items,
  deleting,
  startingNewMain,
  onClose,
  onNewMainClose,
  onNavigate,
  menuKey,
  dialogKey,
  newMainDialogKey,
  newMainScopeKey,
  deleteDialogDeps,
  newMainDialogDeps,
  renderMenu,
  renderDeleteConfirmation,
  renderNewMainDialog,
}: ConversationRowMenuViewProps) {
  if (deleting) {
    if (renderDeleteConfirmation) {
      return <>{renderDeleteConfirmation({ s, onClose, dialogKey })}</>;
    }
    if (deleteDialogDeps) {
      return (
        <Suspense fallback={<Dialog open title={t("conversation-menu.delete")} onClose={onClose}><Loading /></Dialog>}>
          <LazyDeleteConversationDialog key={dialogKey} s={s} onClose={onClose} deps={deleteDialogDeps} />
        </Suspense>
      );
    }
    return <ConversationDeleteConfirmation key={dialogKey} s={s} onClose={onClose} />;
  }
  if (startingNewMain) {
    const closeNewMain = onNewMainClose ?? onClose;
    if (renderNewMainDialog) {
      return <>{renderNewMainDialog({ s, onClose: closeNewMain, dialogKey: newMainDialogKey })}</>;
    }
    return (
      <LazyNewMainDialog
        key={newMainDialogKey}
        open
        onClose={closeNewMain}
        onNavigate={onNavigate}
        session={s}
        scopeKey={newMainScopeKey}
        deps={newMainDialogDeps}
      />
    );
  }
  if (renderMenu) {
    return <>{renderMenu({ anchor: at.anchor ?? null, point: at.point, onClose, items, menuKey })}</>;
  }
  return <Menu key={menuKey} anchor={at.anchor ?? null} point={at.point} onClose={onClose} align="right" width={200} items={items} />;
}

function DefaultCanDeleteBridge({
  s,
  main,
  builtIn,
  render,
}: {
  s: Session;
  main: boolean;
  builtIn?: boolean;
  render: (canDelete: boolean) => ReactNode;
}) {
  const canDelete = useCanDeleteConversation(s, main, builtIn);
  return <>{render(canDelete)}</>;
}

function DefaultMainCapabilitiesBridge({
  s,
  render,
}: {
  s: Session;
  render: (caps: MainCapabilities) => ReactNode;
}) {
  const canCompact = useStore((st) => hubHas(st.features, COMPACT_FEATURE));
  const canClearMain = useStore((st) => hubHas(st.features, CLEAR_MAIN_FEATURE));
  const canNewMain = useStore((st) => hubHas(st.features, NEW_MAIN_FEATURE));
  const clearingMain = useMainClearing(s.session_id);
  return <>{render({ canCompact, canClearMain, canNewMain, clearingMain })}</>;
}

interface ConversationRowMenuBodyProps {
  s: Session;
  main: boolean;
  leader?: boolean;
  builtIn?: boolean;
  at: MenuAt;
  canDelete: boolean;
  mainCaps: MainCapabilities;
  deleting: boolean;
  startingNewMain: boolean;
  hasCustomItems: boolean;
  controlledItems?: readonly MenuItem[];
  hasArrange: boolean;
  effectiveEpoch: number;
  deletingGen: number;
  newMainGen: number;
  deps?: ConversationRowMenuDeps;
  depsRef: { current?: ConversationRowMenuDeps };
  onCloseRef: { current: () => void };
  onArrangeRef: { current?: () => void };
  onNavigateRef: { current?: () => void };
  canDeleteRef: { current: boolean };
  mainCapsRef: { current: MainCapabilities };
  pendingActionsRef: { current: Set<string> };
  isMenuCurrent: () => boolean;
  isOpCurrent: () => boolean;
  isScopeCurrent: () => boolean;
  scopeToken: string;
  openDelete: () => void;
  openNewMain: () => void;
  canSubmitNewMain: () => boolean;
}

function ConversationRowMenuBody({
  s,
  main,
  leader,
  builtIn,
  at,
  canDelete,
  mainCaps,
  deleting,
  startingNewMain,
  hasCustomItems,
  controlledItems,
  hasArrange,
  effectiveEpoch,
  deletingGen,
  newMainGen,
  deps,
  depsRef,
  onCloseRef,
  onArrangeRef,
  onNavigateRef,
  canDeleteRef,
  mainCapsRef,
  pendingActionsRef,
  isMenuCurrent,
  isOpCurrent,
  isScopeCurrent,
  scopeToken,
  openDelete,
  openNewMain,
  canSubmitNewMain,
}: ConversationRowMenuBodyProps) {
  useLayoutEffect(() => {
    canDeleteRef.current = canDelete;
    mainCapsRef.current = mainCaps;
    if (startingNewMain && !mainCaps.canNewMain) onCloseRef.current();
  }, [canDelete, canDeleteRef, mainCaps, mainCapsRef, onCloseRef, startingNewMain]);

  const menuDeps: ConversationMenuDeps = {
    rename: (session, title) =>
      fenceAsync(isOpCurrent, () => Promise.resolve((depsRef.current?.menuDeps?.rename ?? conversationMenuDeps.rename)(session, title)), undefined),
    archive: (session) =>
      fenceAsync(isOpCurrent, () => Promise.resolve((depsRef.current?.menuDeps?.archive ?? conversationMenuDeps.archive)(session)), undefined),
    unarchive: (session) => {
      const unarchiveKey = `${effectiveEpoch}:unarchive`;
      if (!isOpCurrent() || pendingActionsRef.current.has(unarchiveKey)) return Promise.resolve(undefined);
      pendingActionsRef.current.add(unarchiveKey);
      return fenceAsync(
        isOpCurrent,
        () => Promise.resolve((depsRef.current?.menuDeps?.unarchive ?? conversationMenuDeps.unarchive)(session)),
        undefined,
      ).finally(() => {
        pendingActionsRef.current.delete(unarchiveKey);
      });
    },
    end: (session) =>
      fenceAsync(isOpCurrent, () => Promise.resolve((depsRef.current?.menuDeps?.end ?? conversationMenuDeps.end)(session)), undefined),
    refresh: () =>
      fenceAsync(isOpCurrent, () => Promise.resolve((depsRef.current?.menuDeps?.refresh ?? conversationMenuDeps.refresh)()), undefined),
    lead: (session, on) =>
      fenceAsync(isOpCurrent, () => Promise.resolve((depsRef.current?.menuDeps?.lead ?? conversationMenuDeps.lead)(session, on)), undefined),
    compact: (session) =>
      fenceAsync(isOpCurrent, () => Promise.resolve((depsRef.current?.menuDeps?.compact ?? conversationMenuDeps.compact!)(session)), undefined),
    clearMain: (session) =>
      fenceAsync(
        isOpCurrent,
        () => {
          const override = depsRef.current?.menuDeps?.clearMain;
          if (override) return Promise.resolve(override(session));
          const customNav = depsRef.current?.navigate;
          const route = routeAt(location.href);
          const viewingMain = route?.name === "persona" && (!route.sid || route.sid === session.session_id);
          return clearMainContext(session.session_id, {
            home: (changedElsewhere) => {
              if (viewingMain || changedElsewhere) {
                (customNav ?? navigate)({ name: "persona" });
              }
            },
            afterHome: () => onNavigateRef.current?.(),
            isScopeCurrent,
          });
        },
        undefined,
      ),
    copy: (text) => {
      if (!isMenuCurrent()) return undefined;
      return (depsRef.current?.menuDeps?.copy ?? conversationMenuDeps.copy)(text);
    },
    notify: (text, tone, action?: ToastAction) => {
      if (!isOpCurrent()) return;
      const unarchiveKey = `${effectiveEpoch}:unarchive`;
      const fencedAction: ToastAction | undefined = action
        ? {
            ...action,
            run: () => {
              if (!isOpCurrent() || pendingActionsRef.current.has(unarchiveKey)) return;
              void runSingleFlight(pendingActionsRef.current, `${effectiveEpoch}:toast:${action.label}`, isOpCurrent, isOpCurrent, () => action.run());
            },
          }
        : undefined;
      (depsRef.current?.menuDeps?.notify ?? conversationMenuDeps.notify)(text, tone, fencedAction);
    },
  };

  const baseItems: MenuItem[] = hasCustomItems
    ? [...(controlledItems ?? [])]
    : (deps?.buildItems ?? conversationMenuItems)(
        s,
        {
          main,
          leader,
          builtIn,
          canDelete,
          onDelete: openDelete,
          canCompact: mainCaps.canCompact,
          canClearMain: mainCaps.canClearMain,
          canNewMain: mainCaps.canNewMain,
          clearingMain: mainCaps.clearingMain,
          onNewMain: openNewMain,
        },
        menuDeps,
      );

  if (leader && !main && !builtIn) {
    baseItems.unshift({
      key: "direction-detail",
      label: t("external-sources.direction_details"),
      icon: "target",
      onSelect: () => {
        if (!isMenuCurrent()) return;
        (depsRef.current?.navigate ?? navigate)({ name: "todos", direction: s.session_id, manager: s.manager });
      },
    });
  }
  if (hasArrange && !builtIn) {
    baseItems.unshift({
      key: "arrange",
      label: t("conversation-menu.arrange_chats"),
      icon: "menu",
      onSelect: () => {
        if (!isMenuCurrent()) return;
        onArrangeRef.current?.();
      },
    });
  }

  const items = fenceMenuItems(baseItems, effectiveEpoch, pendingActionsRef.current, isMenuCurrent, isOpCurrent);
  const viewProps: ConversationRowMenuViewProps = {
    s,
    at,
    items,
    deleting,
    startingNewMain: startingNewMain && mainCaps.canNewMain,
    onClose: () => {
      if (isMenuCurrent()) onCloseRef.current();
    },
    onNewMainClose: () => {
      if (isScopeCurrent()) onCloseRef.current();
    },
    onNavigate: () => {
      if (isScopeCurrent()) onNavigateRef.current?.();
    },
    menuKey: effectiveEpoch,
    dialogKey: `${effectiveEpoch}:${deletingGen}`,
    newMainDialogKey: `${effectiveEpoch}:${newMainGen}`,
    newMainScopeKey: scopeToken,
    deleteDialogDeps: deps?.deleteDialogDeps,
    newMainDialogDeps: { ...deps?.newMainDialogDeps, canStart: canSubmitNewMain },
    renderMenu: deps?.renderMenu,
    renderDeleteConfirmation: deps?.renderDeleteConfirmation,
    renderNewMainDialog: deps?.renderNewMainDialog,
  };
  if (deps?.renderView) return <>{deps.renderView(viewProps)}</>;
  return <ConversationRowMenuView {...viewProps} />;
}

/** The menu of a row (`at`: where a right click opened it; without it, beside `anchor`). */
export function ConversationRowMenu({ s, main, leader, builtIn, anchor, at, onClose, onArrange, onNavigate, deps }: ConversationRowMenuProps) {
  const trackedScope = useTrackedScope(deps?.scopeKey, deps?.subscribeScopeKey);
  const rowScope = JSON.stringify([trackedScope.token, s.machine_id, s.session_id, s.manager ?? "", main, Boolean(builtIn)]);
  const [scope, setScope] = useState<{ key: string; epoch: number }>(() => ({ key: rowScope, epoch: 1 }));
  const effectiveEpoch = scope.key === rowScope ? scope.epoch : scope.epoch + 1;
  if (scope.key !== rowScope) {
    setScope({ key: rowScope, epoch: effectiveEpoch });
  }

  const controlledDeletingMode = deps?.deleting !== undefined;
  const [deletingSource, setDeletingSource] = useState<{ epoch: number; controlled: boolean; gen: number }>(() => ({
    epoch: effectiveEpoch,
    controlled: controlledDeletingMode,
    gen: 1,
  }));
  const deletingSourceChanged = deletingSource.epoch !== effectiveEpoch || deletingSource.controlled !== controlledDeletingMode;
  const deletingGen = deletingSourceChanged ? deletingSource.gen + 1 : deletingSource.gen;
  if (deletingSourceChanged) {
    setDeletingSource({ epoch: effectiveEpoch, controlled: controlledDeletingMode, gen: deletingGen });
  }

  const [localDeletingState, setLocalDeletingState] = useState<{ epoch: number; gen: number; deleting: boolean }>(() => ({
    epoch: effectiveEpoch,
    gen: deletingGen,
    deleting: false,
  }));
  const deleting = controlledDeletingMode
    ? Boolean(deps?.deleting)
    : (localDeletingState.epoch === effectiveEpoch && localDeletingState.gen === deletingGen && localDeletingState.deleting);

  const controlledNewMainMode = deps?.startingNewMain !== undefined;
  const [newMainSource, setNewMainSource] = useState<{ epoch: number; controlled: boolean; gen: number }>(() => ({
    epoch: effectiveEpoch,
    controlled: controlledNewMainMode,
    gen: 1,
  }));
  const newMainSourceChanged = newMainSource.epoch !== effectiveEpoch || newMainSource.controlled !== controlledNewMainMode;
  const newMainGen = newMainSourceChanged ? newMainSource.gen + 1 : newMainSource.gen;
  if (newMainSourceChanged) {
    setNewMainSource({ epoch: effectiveEpoch, controlled: controlledNewMainMode, gen: newMainGen });
  }

  const [localNewMainState, setLocalNewMainState] = useState<{ epoch: number; gen: number; open: boolean }>(() => ({
    epoch: effectiveEpoch,
    gen: newMainGen,
    open: false,
  }));
  const startingNewMain = controlledNewMainMode
    ? Boolean(deps?.startingNewMain)
    : (localNewMainState.epoch === effectiveEpoch && localNewMainState.gen === newMainGen && localNewMainState.open);

  const canDeleteDep = deps?.canDelete;
  const readControlledCanDelete = useCallback((): boolean | undefined => {
    if (canDeleteDep === undefined) return undefined;
    if (typeof canDeleteDep === "function") return Boolean(canDeleteDep(s, { main, builtIn }));
    return Boolean(canDeleteDep);
  }, [canDeleteDep, s, main, builtIn]);
  const canDeleteSub = typeof canDeleteDep === "function" ? (deps?.subscribeCanDelete ?? noopSubscribe) : noopSubscribe;
  const controlledCanDelete = useSyncExternalStore(canDeleteSub, readControlledCanDelete, readControlledCanDelete);

  const mainCapsDep = deps?.mainCapabilities;
  const readControlledMainCaps = useCallback((): MainCapabilities | undefined => {
    if (mainCapsDep === undefined) return undefined;
    const raw = typeof mainCapsDep === "function" ? mainCapsDep(s, { main, builtIn }) : mainCapsDep;
    return {
      canCompact: Boolean(raw.canCompact),
      canClearMain: Boolean(raw.canClearMain),
      canNewMain: Boolean(raw.canNewMain),
      clearingMain: Boolean(raw.clearingMain),
    };
  }, [mainCapsDep, s, main, builtIn]);
  const controlledMainCaps = useCachedExternalValue(
    mainCapsDep !== undefined ? readControlledMainCaps : undefined,
    typeof mainCapsDep === "function" ? deps?.subscribeMainCapabilities : undefined,
  );

  const controlledItems = useCachedExternalValue(deps?.items, deps?.subscribeItems);

  const activeEpochRef = useRef(effectiveEpoch);
  const minValidEpochRef = useRef(effectiveEpoch);
  const prevCommittedEpochRef = useRef(0);
  const activeDeletingGenRef = useRef(deletingGen);
  const activeNewMainGenRef = useRef(newMainGen);
  const depsRef = useRef(deps);
  const onCloseRef = useRef(onClose);
  const onArrangeRef = useRef(onArrange);
  const onNavigateRef = useRef(onNavigate);
  const canDeleteRef = useRef(controlledCanDelete ?? false);
  const mainCapsRef = useRef<MainCapabilities>(controlledMainCaps ?? NO_MAIN_CAPABILITIES);
  const readScopeTokenRef = useRef(trackedScope.readToken);
  const pendingActionsRef = useRef(new Set<string>());

  const sRef = useRef(s);
  const mainRef = useRef(main);
  const builtInRef = useRef(builtIn);

  useLayoutEffect(() => {
    depsRef.current = deps;
    onCloseRef.current = onClose;
    onArrangeRef.current = onArrange;
    onNavigateRef.current = onNavigate;
    sRef.current = s;
    mainRef.current = main;
    builtInRef.current = builtIn;
    activeDeletingGenRef.current = deletingGen;
    activeNewMainGenRef.current = newMainGen;
    readScopeTokenRef.current = trackedScope.readToken;
  });

  useLayoutEffect(() => {
    const previous = prevCommittedEpochRef.current;
    prevCommittedEpochRef.current = effectiveEpoch;
    activeEpochRef.current = effectiveEpoch;
    if (effectiveEpoch > minValidEpochRef.current) {
      minValidEpochRef.current = effectiveEpoch;
    }
    if (previous !== 0 && previous !== effectiveEpoch) {
      depsRef.current?.onDeletingChange?.(false);
      depsRef.current?.onStartingNewMainChange?.(false);
    }
    const pending = pendingActionsRef.current;
    const prefix = `${effectiveEpoch}:`;
    return () => {
      if (activeEpochRef.current === effectiveEpoch) activeEpochRef.current = 0;
      for (const k of pending) {
        if (k.startsWith(prefix)) pending.delete(k);
      }
    };
  }, [effectiveEpoch]);

  const capturedEpoch = effectiveEpoch;
  const capturedGen = deletingGen;
  const capturedNewMainGen = newMainGen;
  const capturedScopeToken = trackedScope.token;

  const isScopeCurrent = () => readScopeTokenRef.current() === capturedScopeToken;
  const isMenuCurrent = () =>
    activeEpochRef.current === capturedEpoch && isScopeCurrent();
  const isOpCurrent = () =>
    capturedEpoch >= minValidEpochRef.current
    && (activeEpochRef.current === 0 || activeEpochRef.current === capturedEpoch)
    && isScopeCurrent();

  const liveCanDelete = () => {
    const dep = depsRef.current?.canDelete;
    if (dep !== undefined) {
      return typeof dep === "function" ? Boolean(dep(sRef.current, { main: mainRef.current, builtIn: builtInRef.current })) : Boolean(dep);
    }
    return canDeleteRef.current;
  };

  const liveCanNewMain = () => {
    const dep = depsRef.current?.mainCapabilities;
    if (dep !== undefined) {
      const raw = typeof dep === "function" ? dep(sRef.current, { main: mainRef.current, builtIn: builtInRef.current }) : dep;
      return Boolean(raw.canNewMain);
    }
    return hubHas(getState().features, NEW_MAIN_FEATURE) && getState().assistant?.main === sRef.current.session_id;
  };

  const openDelete = () => {
    if (!isMenuCurrent() || !liveCanDelete() || mainRef.current || builtInRef.current) return;
    if (depsRef.current?.deleting === undefined) {
      setLocalDeletingState({ epoch: capturedEpoch, gen: activeDeletingGenRef.current, deleting: true });
    }
    depsRef.current?.onDeletingChange?.(true);
  };

  const openNewMain = () => {
    if (!isMenuCurrent() || !liveCanNewMain() || !mainRef.current || builtInRef.current) return;
    if (depsRef.current?.startingNewMain === undefined) {
      setLocalNewMainState({ epoch: capturedEpoch, gen: activeNewMainGenRef.current, open: true });
    }
    depsRef.current?.onStartingNewMainChange?.(true);
  };

  const p = at ?? { anchor };
  const hasCustomItems = deps?.items !== undefined;
  const hasArrange = Boolean(onArrange);
  const renderBody = (canDelete: boolean, mainCaps: MainCapabilities) => (
    <ConversationRowMenuBody
      s={s}
      main={main}
      leader={leader}
      builtIn={builtIn}
      at={p}
      canDelete={canDelete}
      mainCaps={mainCaps}
      deleting={deleting}
      startingNewMain={startingNewMain}
      hasCustomItems={hasCustomItems}
      controlledItems={controlledItems}
      hasArrange={hasArrange}
      effectiveEpoch={effectiveEpoch}
      deletingGen={capturedGen}
      newMainGen={capturedNewMainGen}
      deps={deps}
      depsRef={depsRef}
      onCloseRef={onCloseRef}
      onArrangeRef={onArrangeRef}
      onNavigateRef={onNavigateRef}
      canDeleteRef={canDeleteRef}
      mainCapsRef={mainCapsRef}
      pendingActionsRef={pendingActionsRef}
      isMenuCurrent={isMenuCurrent}
      isOpCurrent={isOpCurrent}
      isScopeCurrent={isScopeCurrent}
      scopeToken={trackedScope.token}
      openDelete={openDelete}
      openNewMain={openNewMain}
      canSubmitNewMain={() => isMenuCurrent() && mainRef.current && liveCanNewMain()}
    />
  );

  const withMainCaps = (canDelete: boolean) => {
    if (main && !builtIn && !hasCustomItems && mainCapsDep === undefined) {
      return <DefaultMainCapabilitiesBridge s={s} render={(caps) => renderBody(canDelete, caps)} />;
    }
    return renderBody(canDelete, controlledMainCaps ?? NO_MAIN_CAPABILITIES);
  };

  if (canDeleteDep === undefined && !hasCustomItems) {
    return <DefaultCanDeleteBridge s={s} main={main} builtIn={builtIn} render={withMainCaps} />;
  }
  return withMainCaps(Boolean(controlledCanDelete));
}

export default ConversationRowMenu;
