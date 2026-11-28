// New space (`/tracks/new`, #13500, docs/web/web.md "Domain spaces"): a name, a short line about it, an icon, which kinds
// of personal information it holds, and (folded under More options) its kind and its address. Created, it opens.
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { errorText } from "../../lib/api";
import { navigate, type Route } from "../../lib/router";
import { sameValue } from "../../lib/stable-lines";
import { createTrack, DEFAULT_KIND, formSlug, INFO_CATEGORIES, listTrackKinds, noteTrack, SPACE_ICONS, suggestSlug, useTracks, type Track, type TrackCreate, type TrackKind } from "../../lib/tracks";
import { Button, cx, EASE, IconButton } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { categoryLabel } from "./labels";
import { t } from "../../i18n";

export type NewSpaceNoticeTone = "ok" | "error" | "info";
export type SpaceSlugEntry = Pick<Track, "slug">;

export interface NewSpaceViewProps {
  title: string;
  description: string;
  icon: string;
  categories: readonly string[];
  kinds: readonly TrackKind[];
  kind: string;
  shownSlug: string;
  slugOk: boolean;
  slugTaken: boolean;
  more: boolean;
  busy: boolean;
  ready: boolean;
  error: string;
  onToggleSidebar: () => void;
  onTitleChange: (value: string) => void;
  onDescriptionChange: (value: string) => void;
  onIconChange: (value: string) => void;
  onToggleCategory: (category: string) => void;
  onToggleMore: () => void;
  onKindChange: (value: string) => void;
  onSlugChange: (value: string) => void;
  onSubmit: () => void;
  onCancel: () => void;
}

export interface NewSpaceDeps {
  /** Optional scope partition (value or getter); switching it resets drafts, errors, and busy state and fences stale callbacks. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Controlled track kinds snapshot or getter (bypasses `listTrackKinds` when supplied). */
  kinds?: readonly TrackKind[] | (() => readonly TrackKind[]);
  /** Subscribe to controlled track kinds updates when `kinds` is a getter. */
  subscribeKinds?: (listener: () => void) => () => void;
  /** Load available track kinds (sync or async; defaults to `lib/tracks.listTrackKinds`). */
  listTrackKinds?: (signal: AbortSignal) => readonly TrackKind[] | Promise<readonly TrackKind[]>;
  /** Controlled existing spaces snapshot or getter (bypasses `useTracks` and `listSpaces` when supplied). */
  spaces?: readonly SpaceSlugEntry[] | (() => readonly SpaceSlugEntry[]);
  /** Subscribe to controlled spaces updates when `spaces` is a getter. */
  subscribeSpaces?: (listener: () => void) => () => void;
  /** Load existing spaces for slug collision checks (sync or async; bypasses `useTracks` when supplied). */
  listSpaces?: (signal: AbortSignal) => readonly SpaceSlugEntry[] | Promise<readonly SpaceSlugEntry[]>;
  /** Create a space (sync or async; defaults to `lib/tracks.createTrack` + `noteTrack`). */
  createSpace?: (body: TrackCreate, signal: AbortSignal) => Track | Promise<Track>;
  /** Navigate on creation or cancel (defaults to `lib/router.navigate`). */
  navigate?: (route: Route, replace?: boolean) => void | Promise<unknown>;
  /** Optional error notifications (when omitted, errors stay inline without extra toast). */
  notify?: (message: string, tone?: NewSpaceNoticeTone) => void;
  /** Format thrown create/navigation errors (defaults to `api.errorText`). */
  formatError?: (error: unknown) => string;
  /** Error observer invoked when creating or navigating fails in the active scope. */
  onError?: (message: string) => void;
  /** Optional custom view renderer (defaults to `<NewSpaceView>`). */
  renderView?: (props: NewSpaceViewProps) => ReactNode;
}

export interface NewSpaceProps {
  onToggleSidebar: () => void;
  deps?: NewSpaceDeps;
}

interface PendingOp {
  epoch: number;
  seq: number;
  controller: AbortController;
}

interface NewSpaceDraft {
  epoch: number;
  title: string;
  description: string;
  icon: string;
  categories: string[];
  kind: string;
  slug: string;
  slugEdited: boolean;
  more: boolean;
}

const noopSubscribe = () => () => {};
const EMPTY_SPACES: readonly SpaceSlugEntry[] = [];
const EMPTY_KINDS: readonly TrackKind[] = [];

function initialDraft(epoch: number): NewSpaceDraft {
  return {
    epoch,
    title: "",
    description: "",
    icon: SPACE_ICONS[0]!,
    categories: [],
    kind: DEFAULT_KIND,
    slug: "",
    slugEdited: false,
    more: false,
  };
}

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

function safeNotify(notify: ((message: string, tone?: NewSpaceNoticeTone) => void) | undefined, message: string, tone?: NewSpaceNoticeTone) {
  if (!notify) return;
  try {
    notify(message, tone);
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

/** The kinds a new space may be of: enabled ones, the main chat's own way of working left out (it is not a place). */
export function spaceKinds(kinds: readonly TrackKind[]): TrackKind[] {
  return kinds.filter((k) => !k.disabled && k.slug !== "orchestration");
}

const FIELD = "w-full rounded-control bg-surface px-3 py-2 text-meta text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-btn";

/** Pure presentational view for creating a new space. */
export function NewSpaceView({
  title,
  description,
  icon,
  categories,
  kinds,
  kind,
  shownSlug,
  slugOk,
  slugTaken,
  more,
  busy,
  ready,
  error,
  onToggleSidebar,
  onTitleChange,
  onDescriptionChange,
  onIconChange,
  onToggleCategory,
  onToggleMore,
  onKindChange,
  onSlugChange,
  onSubmit,
  onCancel,
}: NewSpaceViewProps) {
  const ids = { title: useId(), description: useId(), slug: useId(), kind: useId() };

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("spaces.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <h1 className="min-w-0 flex-1 truncate text-card-title font-semibold text-ink">{t("spaces.new_title")}</h1>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <form
          className="mx-auto flex max-w-[600px] flex-col gap-5 px-4 py-6 sm:px-8"
          style={{ animation: `fade-up 320ms ${EASE} both` }}
          onSubmit={(e) => {
            e.preventDefault();
            onSubmit();
          }}
        >
          <p className="text-meta leading-relaxed text-ink-2">{t("spaces.new_intro")}</p>

          <div className="flex flex-col gap-1.5">
            <label htmlFor={ids.title} className="text-label font-medium text-ink-2">{t("spaces.field_title")}</label>
            <input id={ids.title} value={title} onChange={(e) => onTitleChange(e.target.value)} maxLength={80} autoFocus placeholder={t("spaces.field_title_placeholder")} className={FIELD} />
          </div>

          <div className="flex flex-col gap-1.5">
            <label htmlFor={ids.description} className="text-label font-medium text-ink-2">{t("spaces.field_description")}</label>
            <input id={ids.description} value={description} onChange={(e) => onDescriptionChange(e.target.value)} maxLength={200} placeholder={t("spaces.field_description_placeholder")} className={FIELD} />
          </div>

          <fieldset className="flex flex-col gap-1.5">
            <legend className="mb-1.5 text-label font-medium text-ink-2">{t("spaces.field_icon")}</legend>
            <div className="flex flex-wrap gap-1.5" role="radiogroup" aria-label={t("spaces.field_icon")}>
              {SPACE_ICONS.map((name) => (
                <button
                  key={name}
                  type="button"
                  role="radio"
                  aria-checked={icon === name}
                  aria-label={t(`spaces.icon_${name}`)}
                  title={t(`spaces.icon_${name}`)}
                  onClick={() => onIconChange(name)}
                  className={cx("touch-hit flex size-9 items-center justify-center rounded-control transition-colors", icon === name ? "bg-accent-tint text-accent-ink shadow-btn" : "bg-surface text-ink-2 shadow-hairline hover:bg-hover")}
                >
                  <Icon name={name} size={16} />
                </button>
              ))}
            </div>
          </fieldset>

          <fieldset className="flex flex-col gap-1.5">
            <legend className="mb-1.5 text-label font-medium text-ink-2">{t("spaces.field_categories")}</legend>
            <div className="flex flex-wrap gap-1.5">
              {INFO_CATEGORIES.map((c) => (
                <button
                  key={c}
                  type="button"
                  aria-pressed={categories.includes(c)}
                  onClick={() => onToggleCategory(c)}
                  className={cx("inline-flex h-7 items-center gap-1 whitespace-nowrap rounded-full px-2.5 text-label font-medium transition-colors", categories.includes(c) ? "bg-accent-tint text-accent-ink" : "bg-field text-ink-2 hover:bg-hover")}
                >
                  {categories.includes(c) && <Icon name="check" size={11} stroke={2.4} />}
                  {categoryLabel(c)}
                </button>
              ))}
            </div>
            <p className="text-label leading-relaxed text-ink-3">{t("spaces.field_categories_hint")}</p>
          </fieldset>

          <div>
            <Button tone="quiet" icon={more ? "chevron" : "chevronRight"} expanded={more} onClick={onToggleMore}>{t("spaces.more_options")}</Button>
            {more && (
              <div className="mt-3 flex flex-col gap-4 rounded-card bg-surface p-4 shadow-card">
                <div className="flex flex-col gap-1.5">
                  <label htmlFor={ids.kind} className="text-label font-medium text-ink-2">{t("spaces.field_kind")}</label>
                  <select id={ids.kind} value={kind} onChange={(e) => onKindChange(e.target.value)} className={FIELD}>
                    {(kinds.length ? kinds : [{ slug: DEFAULT_KIND, title: "" } as TrackKind]).map((k) => (
                      <option key={k.slug} value={k.slug}>{k.slug === DEFAULT_KIND ? t("spaces.kind_space") : k.title || k.slug}</option>
                    ))}
                  </select>
                </div>
                <div className="flex flex-col gap-1.5">
                  <label htmlFor={ids.slug} className="text-label font-medium text-ink-2">{t("spaces.field_slug")}</label>
                  <input
                    id={ids.slug}
                    value={shownSlug}
                    onChange={(e) => onSlugChange(e.target.value)}
                    maxLength={32}
                    spellCheck={false}
                    className={cx(FIELD, "font-mono")}
                  />
                  <p className={cx("text-label leading-relaxed", shownSlug && !slugOk ? "text-red" : "text-ink-3")}>
                    {shownSlug && slugTaken ? t("spaces.field_slug_taken") : t("spaces.field_slug_hint")}
                  </p>
                </div>
              </div>
            )}
          </div>

          {error && <p role="alert" className="rounded-control bg-orange-tint px-3 py-2 text-meta text-ink">{error}</p>}

          <div className="flex justify-end gap-2">
            <Button tone="quiet" onClick={onCancel}>{t("spaces.cancel")}</Button>
            <Button tone="primary" type="submit" icon="plus" busy={busy} disabled={!ready}>{t("spaces.create")}</Button>
          </div>
        </form>
      </div>
    </div>
  );
}

export function NewSpace(props: NewSpaceProps) {
  const controlledSpaces = props.deps?.spaces !== undefined || props.deps?.subscribeSpaces !== undefined || props.deps?.listSpaces !== undefined;
  return controlledSpaces
    ? <NewSpaceInner {...props} defaultTracks={null} />
    : <NewSpaceWithDefaultTracks {...props} />;
}

function NewSpaceWithDefaultTracks(props: NewSpaceProps) {
  const { tracks } = useTracks();
  return <NewSpaceInner {...props} defaultTracks={tracks} />;
}

function NewSpaceInner({ onToggleSidebar, deps, defaultTracks }: NewSpaceProps & { defaultTracks: Track[] | null }) {
  const trackedScope = useTrackedScope(deps?.scopeKey, deps?.subscribeScopeKey);

  const spacesDep = deps?.spaces;
  const hasSnapshotSpaces = spacesDep !== undefined || deps?.subscribeSpaces !== undefined;
  const readControlledSpaces = useCallback((): readonly SpaceSlugEntry[] => {
    if (spacesDep === undefined) return EMPTY_SPACES;
    return typeof spacesDep === "function" ? spacesDep() : spacesDep;
  }, [spacesDep]);
  const controlledSpaces = useCachedSnapshot(
    readControlledSpaces,
    typeof spacesDep === "function" ? deps?.subscribeSpaces : undefined,
  );

  const kindsDep = deps?.kinds;
  const hasSnapshotKinds = kindsDep !== undefined || deps?.subscribeKinds !== undefined;
  const readControlledKinds = useCallback((): readonly TrackKind[] => {
    if (kindsDep === undefined) return EMPTY_KINDS;
    return typeof kindsDep === "function" ? kindsDep() : kindsDep;
  }, [kindsDep]);
  const controlledKindsRaw = useCachedSnapshot(
    readControlledKinds,
    typeof kindsDep === "function" ? deps?.subscribeKinds : undefined,
  );

  const useListSpacesLoader = !hasSnapshotSpaces && deps?.listSpaces !== undefined;

  const formScope = trackedScope.token;
  const [scope, setScope] = useState<{ key: string; epoch: number }>(() => ({ key: formScope, epoch: 1 }));
  const effectiveEpoch = scope.key === formScope ? scope.epoch : scope.epoch + 1;
  if (scope.key !== formScope) {
    setScope({ key: formScope, epoch: effectiveEpoch });
  }

  const [draftState, setDraftState] = useState<NewSpaceDraft>(() => initialDraft(1));
  const [kindsState, setKindsState] = useState<{ epoch: number; list: TrackKind[] }>({ epoch: 0, list: [] });
  const [loadedSpacesState, setLoadedSpacesState] = useState<{ epoch: number; list: readonly SpaceSlugEntry[] }>({ epoch: 0, list: EMPTY_SPACES });
  const [busyState, setBusyState] = useState<{ epoch: number; seq: number; busy: boolean }>({ epoch: 0, seq: 0, busy: false });
  const [errorState, setErrorState] = useState<{ epoch: number; message: string }>({ epoch: 0, message: "" });

  const draft = draftState.epoch === effectiveEpoch ? draftState : initialDraft(effectiveEpoch);
  const kinds = useMemo(
    () => (hasSnapshotKinds ? spaceKinds(controlledKindsRaw) : (kindsState.epoch === effectiveEpoch ? kindsState.list : [])),
    [hasSnapshotKinds, controlledKindsRaw, kindsState, effectiveEpoch],
  );
  const resolvedSpaces = hasSnapshotSpaces
    ? controlledSpaces
    : (useListSpacesLoader
      ? (loadedSpacesState.epoch === effectiveEpoch ? loadedSpacesState.list : EMPTY_SPACES)
      : (defaultTracks ?? EMPTY_SPACES));
  const taken = useMemo(() => resolvedSpaces.map((x) => x.slug), [resolvedSpaces]);

  // If the default kind is off or missing on this assistant, start on one it offers.
  const effectiveKind = kinds.length && !kinds.some((x) => x.slug === draft.kind) ? kinds[0]!.slug : draft.kind;
  const busy = busyState.epoch === effectiveEpoch && busyState.busy;
  const error = errorState.epoch === effectiveEpoch ? errorState.message : "";

  const activeEpochRef = useRef(0);
  const kindsSeqRef = useRef(0);
  const spacesSeqRef = useRef(0);
  const createSeqRef = useRef(0);
  const pendingKindsRef = useRef<PendingOp | null>(null);
  const pendingSpacesRef = useRef<PendingOp | null>(null);
  const pendingCreateRef = useRef<PendingOp | null>(null);
  const depsRef = useRef(deps);
  const readScopeTokenRef = useRef(trackedScope.readToken);

  useLayoutEffect(() => {
    depsRef.current = deps;
    readScopeTokenRef.current = trackedScope.readToken;
  });

  useLayoutEffect(() => {
    activeEpochRef.current = effectiveEpoch;
    return () => {
      if (activeEpochRef.current === effectiveEpoch) activeEpochRef.current = 0;
      pendingKindsRef.current?.controller.abort();
      pendingKindsRef.current = null;
      pendingSpacesRef.current?.controller.abort();
      pendingSpacesRef.current = null;
      pendingCreateRef.current?.controller.abort();
      pendingCreateRef.current = null;
      kindsSeqRef.current += 1;
      spacesSeqRef.current += 1;
      createSeqRef.current += 1;
    };
  }, [effectiveEpoch]);

  const capturedEpoch = effectiveEpoch;
  const capturedScopeToken = trackedScope.token;

  useEffect(() => {
    if (hasSnapshotKinds) {
      pendingKindsRef.current?.controller.abort();
      pendingKindsRef.current = null;
      kindsSeqRef.current += 1;
      return;
    }

    pendingKindsRef.current?.controller.abort();
    const controller = new AbortController();
    const runSeq = ++kindsSeqRef.current;
    pendingKindsRef.current = { epoch: capturedEpoch, seq: runSeq, controller };

    const isLoadCurrent = () =>
      !controller.signal.aborted
      && activeEpochRef.current === capturedEpoch
      && kindsSeqRef.current === runSeq
      && readScopeTokenRef.current() === capturedScopeToken;

    const applyKinds = (raw: readonly TrackKind[]) => {
      if (!isLoadCurrent()) return;
      const loaded = spaceKinds(raw);
      setKindsState({ epoch: capturedEpoch, list: loaded });
      setDraftState((prev) => {
        const cur = prev.epoch === capturedEpoch ? prev : initialDraft(capturedEpoch);
        const nextKind = loaded.length && !loaded.some((x) => x.slug === cur.kind) ? loaded[0]!.slug : cur.kind;
        return nextKind === cur.kind && prev.epoch === capturedEpoch ? prev : { ...cur, kind: nextKind };
      });
    };

    let handedOff = false;
    try {
      const res = depsRef.current?.listTrackKinds
        ? depsRef.current.listTrackKinds(controller.signal)
        : listTrackKinds();

      if (isThenable(res)) {
        handedOff = true;
        void (async () => {
          try {
            const awaited = await res;
            applyKinds(awaited);
          } catch {
            // Kind listing failures keep the default kind option.
          } finally {
            if (pendingKindsRef.current?.seq === runSeq) pendingKindsRef.current = null;
          }
        })();
      } else {
        applyKinds(res);
      }
    } catch {
      // Kind listing failures keep the default kind option.
    } finally {
      if (!handedOff && pendingKindsRef.current?.seq === runSeq) pendingKindsRef.current = null;
    }

    return () => {
      controller.abort();
      if (pendingKindsRef.current?.seq === runSeq) pendingKindsRef.current = null;
      if (kindsSeqRef.current === runSeq) kindsSeqRef.current += 1;
    };
  }, [capturedEpoch, capturedScopeToken, hasSnapshotKinds]);

  useEffect(() => {
    if (!useListSpacesLoader) {
      pendingSpacesRef.current?.controller.abort();
      pendingSpacesRef.current = null;
      spacesSeqRef.current += 1;
      return;
    }

    pendingSpacesRef.current?.controller.abort();
    const controller = new AbortController();
    const runSeq = ++spacesSeqRef.current;
    pendingSpacesRef.current = { epoch: capturedEpoch, seq: runSeq, controller };

    const isLoadCurrent = () =>
      !controller.signal.aborted
      && activeEpochRef.current === capturedEpoch
      && spacesSeqRef.current === runSeq
      && readScopeTokenRef.current() === capturedScopeToken;

    const applySpaces = (raw: readonly SpaceSlugEntry[]) => {
      if (!isLoadCurrent()) return;
      setLoadedSpacesState({ epoch: capturedEpoch, list: raw });
    };

    let handedOff = false;
    try {
      const loader = depsRef.current?.listSpaces;
      if (!loader) return;
      const res = loader(controller.signal);

      if (isThenable(res)) {
        handedOff = true;
        void (async () => {
          try {
            const awaited = await res;
            applySpaces(awaited);
          } catch {
            // Space listing failures keep slug suggestion working without blocking creation.
          } finally {
            if (pendingSpacesRef.current?.seq === runSeq) pendingSpacesRef.current = null;
          }
        })();
      } else {
        applySpaces(res);
      }
    } catch {
      // Space listing failures keep slug suggestion working without blocking creation.
    } finally {
      if (!handedOff && pendingSpacesRef.current?.seq === runSeq) pendingSpacesRef.current = null;
    }

    return () => {
      controller.abort();
      if (pendingSpacesRef.current?.seq === runSeq) pendingSpacesRef.current = null;
      if (spacesSeqRef.current === runSeq) spacesSeqRef.current += 1;
    };
  }, [capturedEpoch, capturedScopeToken, useListSpacesLoader]);

  const isScopeLive = () =>
    activeEpochRef.current === capturedEpoch
    && readScopeTokenRef.current() === capturedScopeToken;

  const updateDraft = (fn: (cur: NewSpaceDraft) => NewSpaceDraft) => {
    if (!isScopeLive()) return;
    setDraftState((prev) => fn(prev.epoch === capturedEpoch ? prev : initialDraft(capturedEpoch)));
  };

  const reportError = (msg: string) => {
    try {
      depsRef.current?.onError?.(msg);
    } catch {
      // Error observer failures stay non-blocking.
    }
  };

  // The address follows the name until the user writes one of their own.
  const shownSlug = draft.slugEdited ? draft.slug : draft.title.trim() ? suggestSlug(draft.title, taken) : "";
  const slugTaken = Boolean(shownSlug) && taken.includes(shownSlug);
  const slugOk = formSlug(shownSlug) && !slugTaken;
  const ready = draft.title.trim() !== "" && slugOk && !busy;

  const handleCreate = () => {
    if (!isScopeLive() || pendingCreateRef.current !== null) return;
    const trimmedTitle = draft.title.trim();
    if (!trimmedTitle || !slugOk) return;

    const controller = new AbortController();
    const runSeq = ++createSeqRef.current;
    const body: TrackCreate = {
      slug: shownSlug,
      kind: effectiveKind,
      title: trimmedTitle,
      icon: draft.icon,
      description: draft.description.trim(),
      default_categories: draft.categories,
    };

    const isRunCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && createSeqRef.current === runSeq;

    const releaseLock = () => {
      if (pendingCreateRef.current?.seq === runSeq) pendingCreateRef.current = null;
      if (activeEpochRef.current === capturedEpoch && createSeqRef.current === runSeq) {
        setBusyState((prev) => (prev.epoch === capturedEpoch && prev.seq === runSeq ? { epoch: capturedEpoch, seq: runSeq, busy: false } : prev));
      }
    };

    const handleError = (err: unknown) => {
      if (!isRunCurrent()) return;
      const msg = t("spaces.create_failed", { error: safeFormatError(depsRef.current?.formatError, err) });
      setErrorState({ epoch: capturedEpoch, message: msg });
      safeNotify(depsRef.current?.notify, msg, "error");
      reportError(msg);
    };

    const completeCreated = (track: Track): unknown => {
      if (!isRunCurrent()) return;
      if (!depsRef.current?.createSpace) noteTrack(track);
      const nav = depsRef.current?.navigate;
      return nav
        ? nav({ name: "track", slug: track.slug, fresh: true }, true)
        : navigate({ name: "track", slug: track.slug, fresh: true }, true);
    };

    pendingCreateRef.current = { epoch: capturedEpoch, seq: runSeq, controller };
    setBusyState({ epoch: capturedEpoch, seq: runSeq, busy: true });
    setErrorState({ epoch: capturedEpoch, message: "" });

    let handedOff = false;
    try {
      const res = depsRef.current?.createSpace
        ? depsRef.current.createSpace(body, controller.signal)
        : createTrack(body);

      if (isThenable(res)) {
        handedOff = true;
        void (async () => {
          try {
            const track = await res;
            const navRes = completeCreated(track);
            if (isThenable(navRes)) await navRes;
          } catch (err) {
            handleError(err);
          } finally {
            releaseLock();
          }
        })();
        return;
      }

      const navRes = completeCreated(res);
      if (isThenable(navRes)) {
        handedOff = true;
        void (async () => {
          try {
            await navRes;
          } catch (err) {
            handleError(err);
          } finally {
            releaseLock();
          }
        })();
        return;
      }
    } catch (err) {
      handleError(err);
    } finally {
      if (!handedOff) releaseLock();
    }
  };

  const handleCancel = () => {
    if (!isScopeLive()) return;
    const handleNavError = (err: unknown) => {
      if (!isScopeLive()) return;
      const msg = safeFormatError(depsRef.current?.formatError, err);
      setErrorState({ epoch: capturedEpoch, message: msg });
      safeNotify(depsRef.current?.notify, msg, "error");
      reportError(msg);
    };
    try {
      const res = depsRef.current?.navigate
        ? depsRef.current.navigate({ name: "persona" })
        : navigate({ name: "persona" });
      if (isThenable(res)) {
        void Promise.resolve(res).catch(handleNavError);
      }
    } catch (err) {
      handleNavError(err);
    }
  };

  const viewProps: NewSpaceViewProps = {
    title: draft.title,
    description: draft.description,
    icon: draft.icon,
    categories: draft.categories,
    kinds,
    kind: effectiveKind,
    shownSlug,
    slugOk,
    slugTaken,
    more: draft.more,
    busy,
    ready,
    error,
    onToggleSidebar,
    onTitleChange: (value) => updateDraft((cur) => ({ ...cur, title: value })),
    onDescriptionChange: (value) => updateDraft((cur) => ({ ...cur, description: value })),
    onIconChange: (value) => updateDraft((cur) => ({ ...cur, icon: value })),
    onToggleCategory: (c) => updateDraft((cur) => ({
      ...cur,
      categories: cur.categories.includes(c) ? cur.categories.filter((y) => y !== c) : [...cur.categories, c],
    })),
    onToggleMore: () => updateDraft((cur) => ({ ...cur, more: !cur.more })),
    onKindChange: (value) => updateDraft((cur) => ({ ...cur, kind: value })),
    onSlugChange: (value) => updateDraft((cur) => ({ ...cur, slugEdited: true, slug: value.toLowerCase() })),
    onSubmit: handleCreate,
    onCancel: handleCancel,
  };
  if (deps?.renderView) return <>{deps.renderView(viewProps)}</>;
  return <NewSpaceView {...viewProps} />;
}
