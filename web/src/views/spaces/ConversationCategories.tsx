// A conversation's information categories as chips (#13500, docs/assistant/health.md "Using information by scene"):
// the kinds of personal information it holds (health, money…), which decide how carefully it is used outside its
// space. The user may take one away (the space's own ones stay) and put a taken one back; only the user loosens.
import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type MouseEvent, type ReactNode } from "react";
import { errorText } from "../../lib/api";
import { sameValue } from "../../lib/stable-lines";
import { changeConversationCategories, readConversationCategories, type ConversationCategories as Categories } from "../../lib/tracks";
import { cx, Menu, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { categoryLabel } from "./labels";
import { t } from "../../i18n";

export type ConversationCategoryNoticeTone = "ok" | "error" | "info";
export type ConversationCategoryChange = { add?: string[]; remove?: string[] };

export interface ConversationCategoriesViewProps {
  categories: Categories | null;
  busyCategory?: string;
  menuAnchor: HTMLElement | null;
  onRemove: (category: string) => void;
  onAdd: (category: string) => void;
  onOpenMenu: (event: MouseEvent<HTMLElement>) => void;
  onCloseMenu: () => void;
}

export interface ConversationCategoriesDeps {
  /** Optional scope partition (value or getter); switching it resets chips/menu/busy state and fences stale callbacks. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Controlled categories snapshot or getter (bypasses `readConversationCategories` when supplied). */
  categories?: Categories | null | (() => Categories | null);
  /** Subscribe to controlled categories updates when `categories` is a getter. */
  subscribeCategories?: (listener: () => void) => () => void;
  /** Read a conversation's categories (sync or async; defaults to `lib/tracks.readConversationCategories`). */
  readConversationCategories?: (sid: string, signal: AbortSignal) => Categories | null | Promise<Categories | null>;
  /** Update a conversation's categories (sync or async; defaults to `lib/tracks.changeConversationCategories`). */
  changeConversationCategories?: (sid: string, change: ConversationCategoryChange, signal: AbortSignal) => Categories | null | void | Promise<Categories | null | void>;
  /** Show toast notifications (defaults to `atoms.toast`). */
  notify?: (message: string, tone?: ConversationCategoryNoticeTone) => void;
  /** Format thrown mutation errors (defaults to `api.errorText`). */
  formatError?: (error: unknown) => string;
  /** Error observer invoked when a category change throws or rejects in the active scope. */
  onError?: (message: string) => void;
  /** Optional custom view renderer (defaults to `<ConversationCategoriesView>`). */
  renderView?: (props: ConversationCategoriesViewProps) => ReactNode;
}

export interface ConversationCategoriesProps {
  sid: string;
  deps?: ConversationCategoriesDeps;
}

interface PendingOp {
  epoch: number;
  seq: number;
  controller: AbortController;
}

const noopSubscribe = () => () => {};

function isThenable(value: unknown): value is PromiseLike<unknown> {
  return value !== null && (typeof value === "object" || typeof value === "function") && typeof (value as PromiseLike<unknown>).then === "function";
}

function safeFormatError(fmt: ((error: unknown) => string) | undefined, error: unknown): string {
  if (!fmt) return errorText(error);
  try {
    return fmt(error);
  } catch {
    return errorText(error);
  }
}

function safeNotify(notify: ((message: string, tone?: ConversationCategoryNoticeTone) => void) | undefined, message: string, tone?: ConversationCategoryNoticeTone) {
  try {
    if (notify) notify(message, tone);
    else toast(message, tone);
  } catch {
    // Notification failures stay non-blocking so recovery never leaves busy stuck.
  }
}

function useTrackedScope(
  source: string | (() => string) | undefined,
  subscribe: ((listener: () => void) => () => void) | undefined,
) {
  const isGetter = typeof source === "function";
  const read = useCallback(() => (typeof source === "function" ? source() : (source ?? "")), [source]);
  const readRef = useRef(read);
  const committedRef = useRef<{ value: string; version: number } | null>(null);
  const snapRef = useRef<{ value: string; version: number } | null>(null);
  const sub = useCallback((listener: () => void) => {
    if (!subscribe || !isGetter) return noopSubscribe();
    return subscribe(() => {
      const next = readRef.current();
      const cur = committedRef.current;
      if (!cur || next !== cur.value) {
        committedRef.current = { value: next, version: (cur?.version ?? 0) + 1 };
      }
      listener();
    });
  }, [subscribe, isGetter]);
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
    readRef.current = read;
    committedRef.current = snap;
  }, [read, snap]);
  const readToken = useCallback(() => {
    const cur = readRef.current();
    const committed = committedRef.current;
    return committed && committed.value === cur ? `${committed.version}:${cur}` : `drift:${cur}`;
  }, []);
  return { value: snap.value, token: `${snap.version}:${snap.value}`, readToken };
}

function useCachedSnapshot<T>(read: () => T, subscribe: ((listener: () => void) => () => void) | undefined): T {
  const cache = useRef<{ has: boolean; value: T | undefined }>({ has: false, value: undefined });
  const getSnapshot = useCallback((): T => {
    const next = read();
    if (cache.current.has && sameValue(cache.current.value, next)) {
      return cache.current.value as T;
    }
    cache.current = { has: true, value: next };
    return next;
  }, [read]);
  return useSyncExternalStore(subscribe ?? noopSubscribe, getSnapshot, getSnapshot);
}

/** Pure presentational view for a conversation's information category chips and restore menu. */
export function ConversationCategoriesView({
  categories: cats,
  busyCategory = "",
  menuAnchor,
  onRemove,
  onAdd,
  onOpenMenu,
  onCloseMenu,
}: ConversationCategoriesViewProps) {
  if (!cats || (!cats.categories.length && !cats.removed.length)) return null;
  return (
    <span className="flex min-w-0 items-center gap-1" data-testid="conversation-categories">
      {cats.categories.map((c) => {
        const kept = cats.floor.includes(c);
        return (
          <span key={c} className="inline-flex h-6 shrink-0 items-center gap-0.5 whitespace-nowrap rounded-full bg-field pr-1 pl-2 text-label font-medium text-ink-2" title={kept ? t("spaces.category_kept") : undefined}>
            {categoryLabel(c)}
            {kept ? (
              <span className="px-0.5 text-ink-3"><Icon name="lock" size={10} /><span className="sr-only">{t("spaces.category_kept")}</span></span>
            ) : (
              <button
                type="button"
                disabled={busyCategory === c}
                onClick={() => onRemove(c)}
                aria-label={t("spaces.category_remove", { name: categoryLabel(c) })}
                title={t("spaces.category_remove", { name: categoryLabel(c) })}
                className={cx("touch-hit flex size-4 items-center justify-center rounded-full text-ink-3 hover:bg-hover hover:text-ink disabled:opacity-40")}
              >
                <Icon name="x" size={10} stroke={2.4} />
              </button>
            )}
          </span>
        );
      })}
      {cats.removed.length > 0 && (
        <button type="button" onClick={onOpenMenu} aria-label={t("spaces.category_restore")} title={t("spaces.category_restore")} className="touch-hit flex size-6 shrink-0 items-center justify-center rounded-full text-ink-3 hover:bg-hover hover:text-ink">
          <Icon name="plus" size={12} stroke={2.2} />
        </button>
      )}
      {menuAnchor && cats.removed.length > 0 && (
        <Menu
          anchor={menuAnchor}
          onClose={onCloseMenu}
          align="right"
          header={t("spaces.category_restore")}
          items={cats.removed.map((c) => ({ key: c, label: categoryLabel(c), onSelect: () => onAdd(c) }))}
        />
      )}
    </span>
  );
}

export function ConversationCategories({ sid, deps }: ConversationCategoriesProps) {
  const trackedScope = useTrackedScope(deps?.scopeKey, deps?.subscribeScopeKey);

  const catsDep = deps?.categories;
  const hasControlledCategories = catsDep !== undefined;
  const readControlledCategories = useCallback((): Categories | null => {
    if (catsDep === undefined) return null;
    return typeof catsDep === "function" ? catsDep() : catsDep;
  }, [catsDep]);
  const controlledCats = useCachedSnapshot(
    readControlledCategories,
    typeof catsDep === "function" ? deps?.subscribeCategories : undefined,
  );

  const targetScope = JSON.stringify([trackedScope.token, sid]);
  const [scope, setScope] = useState<{ key: string; epoch: number }>(() => ({ key: targetScope, epoch: 1 }));
  const effectiveEpoch = scope.key === targetScope ? scope.epoch : scope.epoch + 1;
  if (scope.key !== targetScope) {
    setScope({ key: targetScope, epoch: effectiveEpoch });
  }

  const [catsState, setCatsState] = useState<{ epoch: number; value: Categories | null }>({ epoch: 0, value: null });
  const [busyState, setBusyState] = useState<{ epoch: number; seq: number; category: string }>({ epoch: 0, seq: 0, category: "" });
  const [menuState, setMenuState] = useState<{ epoch: number; anchor: HTMLElement | null }>({ epoch: 0, anchor: null });

  const cats = hasControlledCategories
    ? controlledCats
    : (catsState.epoch === effectiveEpoch ? catsState.value : null);
  const busyCategory = busyState.epoch === effectiveEpoch ? busyState.category : "";
  const menuAnchor = menuState.epoch === effectiveEpoch && (cats?.removed.length ?? 0) > 0 ? menuState.anchor : null;

  const activeEpochRef = useRef(0);
  const loadSeqRef = useRef(0);
  const changeSeqRef = useRef(0);
  const pendingLoadRef = useRef<PendingOp | null>(null);
  const pendingChangeRef = useRef<PendingOp | null>(null);
  const depsRef = useRef(deps);
  const sidRef = useRef(sid);
  const hasControlledRef = useRef(hasControlledCategories);
  const readScopeTokenRef = useRef(trackedScope.readToken);

  useLayoutEffect(() => {
    depsRef.current = deps;
    sidRef.current = sid;
    hasControlledRef.current = hasControlledCategories;
    readScopeTokenRef.current = trackedScope.readToken;
  });

  useLayoutEffect(() => {
    activeEpochRef.current = effectiveEpoch;
    return () => {
      if (activeEpochRef.current === effectiveEpoch) activeEpochRef.current = 0;
      pendingLoadRef.current?.controller.abort();
      pendingLoadRef.current = null;
      pendingChangeRef.current?.controller.abort();
      pendingChangeRef.current = null;
      loadSeqRef.current += 1;
      changeSeqRef.current += 1;
    };
  }, [effectiveEpoch]);

  const capturedEpoch = effectiveEpoch;
  const capturedScopeToken = trackedScope.token;

  useEffect(() => {
    if (hasControlledCategories) {
      pendingLoadRef.current?.controller.abort();
      pendingLoadRef.current = null;
      loadSeqRef.current += 1;
      return;
    }

    pendingLoadRef.current?.controller.abort();
    const controller = new AbortController();
    const runSeq = ++loadSeqRef.current;
    pendingLoadRef.current = { epoch: capturedEpoch, seq: runSeq, controller };

    const isLoadCurrent = () =>
      !controller.signal.aborted
      && activeEpochRef.current === capturedEpoch
      && loadSeqRef.current === runSeq
      && readScopeTokenRef.current() === capturedScopeToken
      && sidRef.current === sid
      && !hasControlledRef.current;

    const finishSuccess = (result: Categories | null) => {
      if (!isLoadCurrent()) return;
      setCatsState({ epoch: capturedEpoch, value: result });
    };

    const finishError = () => {
      if (!isLoadCurrent()) return;
      // An assistant without categories, or one that could not be read: no chips.
      setCatsState({ epoch: capturedEpoch, value: null });
    };

    let res: Categories | null | Promise<Categories | null>;
    try {
      res = depsRef.current?.readConversationCategories
        ? depsRef.current.readConversationCategories(sid, controller.signal)
        : readConversationCategories(sid);
    } catch {
      if (pendingLoadRef.current?.seq === runSeq) pendingLoadRef.current = null;
      finishError();
      return () => {
        controller.abort();
        if (pendingLoadRef.current?.seq === runSeq) pendingLoadRef.current = null;
      };
    }

    if (!isThenable(res)) {
      if (pendingLoadRef.current?.seq === runSeq) pendingLoadRef.current = null;
      finishSuccess(res);
      return () => {
        controller.abort();
        if (pendingLoadRef.current?.seq === runSeq) pendingLoadRef.current = null;
      };
    }

    void (async () => {
      try {
        const awaited = await res;
        finishSuccess(awaited);
      } catch {
        finishError();
      } finally {
        if (pendingLoadRef.current?.seq === runSeq) pendingLoadRef.current = null;
      }
    })();

    return () => {
      controller.abort();
      if (pendingLoadRef.current?.seq === runSeq) pendingLoadRef.current = null;
      if (loadSeqRef.current === runSeq) loadSeqRef.current += 1;
    };
  }, [capturedEpoch, capturedScopeToken, sid, hasControlledCategories]);

  const isScopeLive = () =>
    activeEpochRef.current === capturedEpoch
    && readScopeTokenRef.current() === capturedScopeToken
    && sidRef.current === sid;

  const reportError = (msg: string) => {
    try {
      depsRef.current?.onError?.(msg);
    } catch {
      // Error observer failures stay non-blocking.
    }
  };

  const handleOpenMenu = (event: MouseEvent<HTMLElement>) => {
    if (!isScopeLive()) return;
    const target = event.currentTarget;
    setMenuState((prev) => ({
      epoch: capturedEpoch,
      anchor: prev.epoch === capturedEpoch && prev.anchor ? null : target,
    }));
  };

  const handleCloseMenu = () => {
    if (!isScopeLive()) return;
    setMenuState({ epoch: capturedEpoch, anchor: null });
  };

  const handleChange = (category: string, op: "add" | "remove") => {
    if (!isScopeLive() || pendingChangeRef.current !== null) return;
    const controller = new AbortController();
    const runSeq = ++changeSeqRef.current;

    const isRunCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && changeSeqRef.current === runSeq;

    const releaseLock = () => {
      if (pendingChangeRef.current?.seq === runSeq) pendingChangeRef.current = null;
      if (activeEpochRef.current === capturedEpoch && changeSeqRef.current === runSeq) {
        setBusyState((prev) => (prev.epoch === capturedEpoch && prev.seq === runSeq ? { epoch: capturedEpoch, seq: runSeq, category: "" } : prev));
      }
    };

    const finishSuccess = (result: Categories | null | void) => {
      if (!isRunCurrent()) return;
      if (!hasControlledRef.current && result !== undefined) {
        // Supersede any older in-flight read so it cannot overwrite the freshly mutated state.
        pendingLoadRef.current?.controller.abort();
        pendingLoadRef.current = null;
        loadSeqRef.current += 1;
        setCatsState({ epoch: capturedEpoch, value: result });
      }
      safeNotify(depsRef.current?.notify, t(op === "add" ? "spaces.category_added" : "spaces.category_removed", { name: categoryLabel(category) }), "ok");
    };

    const finishError = (err: unknown) => {
      if (!isRunCurrent()) return;
      const msg = t("spaces.category_failed", { error: safeFormatError(depsRef.current?.formatError, err) });
      safeNotify(depsRef.current?.notify, msg, "error");
      reportError(msg);
    };

    pendingChangeRef.current = { epoch: capturedEpoch, seq: runSeq, controller };
    setBusyState({ epoch: capturedEpoch, seq: runSeq, category });

    const payload: ConversationCategoryChange = op === "add" ? { add: [category] } : { remove: [category] };
    let handedOff = false;
    try {
      const res = depsRef.current?.changeConversationCategories
        ? depsRef.current.changeConversationCategories(sid, payload, controller.signal)
        : changeConversationCategories(sid, payload);

      if (isThenable(res)) {
        handedOff = true;
        void (async () => {
          try {
            const awaited = await res;
            finishSuccess(awaited);
          } catch (err) {
            finishError(err);
          } finally {
            releaseLock();
          }
        })();
        return;
      }

      finishSuccess(res);
    } catch (err) {
      finishError(err);
    } finally {
      if (!handedOff) releaseLock();
    }
  };

  const viewProps: ConversationCategoriesViewProps = {
    categories: cats,
    busyCategory,
    menuAnchor,
    onRemove: (category) => handleChange(category, "remove"),
    onAdd: (category) => handleChange(category, "add"),
    onOpenMenu: handleOpenMenu,
    onCloseMenu: handleCloseMenu,
  };
  if (deps?.renderView) return <>{deps.renderView(viewProps)}</>;
  return <ConversationCategoriesView {...viewProps} />;
}
