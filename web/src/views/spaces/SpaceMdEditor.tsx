// Editing a space's SPACE.md (#13500, docs/web/web.md "Domain spaces"): what the user tells the assistant about the
// space. The parts the user wrote are fields; the blocks Persona keeps up to date are shown read-only and go back
// unchanged (lib/space-md.ts). Saving names the version the edit started from: changed meanwhile (409) or a managed
// block touched (400), the dialog says so plainly and keeps what was typed.
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { errorText } from "../../lib/api";
import { assembleSpaceMd, editableParts, sameManagedBlocks, splitSpaceMd, type SpaceMdPart } from "../../lib/space-md";
import { sameValue } from "../../lib/stable-lines";
import { readSpaceMd, spaceMdError, writeSpaceMd, type SpaceMd, type SpaceMdError } from "../../lib/tracks";
import { Button, Dialog, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";

export type SpaceMdNoticeTone = "ok" | "error" | "info";

export interface SpaceMdSaveError {
  kind: SpaceMdError;
  text: string;
}

export interface SpaceMdEditorViewProps {
  open: boolean;
  shape: { parts: SpaceMdPart[]; editable: number[] } | null;
  edits: Readonly<Record<number, string>>;
  readError: string;
  saveError: SpaceMdSaveError | null;
  changed: boolean;
  saving: boolean;
  onEdit: (index: number, value: string) => void;
  onSave: () => void;
  onReload: () => void;
  onClose: () => void;
}

export interface SpaceMdEditorDeps {
  /** Optional scope partition (value or getter); switching it resets drafts, errors, and busy state and fences stale callbacks. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Controlled SPACE.md snapshot or getter (bypasses initial `readSpaceMd` when supplied). */
  md?: SpaceMd | null | (() => SpaceMd | null);
  /** Subscribe to controlled SPACE.md updates when `md` is a getter. */
  subscribeMd?: (listener: () => void) => () => void;
  /** Read a space's SPACE.md (sync or async; defaults to `lib/tracks.readSpaceMd`). */
  readSpaceMd?: (slug: string, signal: AbortSignal) => SpaceMd | Promise<SpaceMd>;
  /** Write a space's SPACE.md (sync or async; defaults to `lib/tracks.writeSpaceMd`). */
  writeSpaceMd?: (slug: string, content: string, baseDigest: string, signal: AbortSignal) => SpaceMd | Promise<SpaceMd>;
  /** Classify a SPACE.md read or write error (defaults to `lib/tracks.spaceMdError`). */
  spaceMdError?: (error: unknown) => SpaceMdError;
  /** Show toast notifications (defaults to `atoms.toast`). */
  notify?: (message: string, tone?: SpaceMdNoticeTone) => void;
  /** Format thrown read or save errors (defaults to `api.errorText`). */
  formatError?: (error: unknown) => string;
  /** Error observer invoked when reading or saving fails in the active scope. */
  onError?: (message: string, kind: SpaceMdError) => void;
  /** Optional custom view renderer (defaults to `<SpaceMdEditorView>`). */
  renderView?: (props: SpaceMdEditorViewProps) => ReactNode;
}

export interface SpaceMdEditorProps {
  open: boolean;
  slug: string;
  onClose: () => void;
  onSaved?: (md: SpaceMd) => void;
  deps?: SpaceMdEditorDeps;
}

interface PendingOp {
  epoch: number;
  seq: number;
  controller: AbortController;
}

const noop = () => {};
const noopSubscribe = () => () => {};
const EMPTY_EDITS: Readonly<Record<number, string>> = {};

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

function safeClassifyError(classify: ((error: unknown) => SpaceMdError) | undefined, error: unknown): SpaceMdError {
  if (!classify) return spaceMdError(error);
  try {
    return classify(error);
  } catch {
    return spaceMdError(error);
  }
}

function safeNotify(notify: ((message: string, tone?: SpaceMdNoticeTone) => void) | undefined, message: string, tone?: SpaceMdNoticeTone) {
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

/** The words for a failed read or save. */
export function spaceMdErrorText(kind: SpaceMdError, e: unknown, formatError?: (error: unknown) => string): string {
  switch (kind) {
    case "stale":
      return t("spaces.md_stale");
    case "managed":
      return t("spaces.md_managed");
    case "missing":
      return t("spaces.md_missing");
    case "offline":
      return t("spaces.md_offline");
    default:
      return t("spaces.md_failed", { error: safeFormatError(formatError, e) });
  }
}

/** Pure presentational dialog view for editing a space's SPACE.md. */
export function SpaceMdEditorView({
  open,
  shape,
  edits,
  readError,
  saveError,
  changed,
  saving,
  onEdit,
  onSave,
  onReload,
  onClose,
}: SpaceMdEditorViewProps) {
  const fieldId = useId();
  const handleClose = saving ? noop : onClose;

  const part = (p: SpaceMdPart, i: number) => {
    if (p.kind === "managed") {
      return (
        <div key={i} className="rounded-control bg-inset px-3 py-2.5 shadow-hairline" data-managed={p.name}>
          <p className="mb-1 flex items-center gap-1.5 text-label font-medium text-ink-3"><Icon name="lock" size={11} />{t("spaces.md_managed_block")}</p>
          <p className="whitespace-pre-wrap break-words text-meta leading-relaxed text-ink-2">{p.body.trim() || t("spaces.md_managed_empty")}</p>
        </div>
      );
    }
    if (!shape?.editable.includes(i)) return null;
    const first = shape.editable[0] === i;
    return (
      <textarea
        key={i}
        id={first ? fieldId : undefined}
        value={edits[i] ?? p.text}
        onChange={(e) => onEdit(i, e.target.value)}
        rows={first ? 10 : 5}
        spellCheck
        placeholder={t("spaces.md_placeholder")}
        className="w-full resize-y rounded-control bg-surface px-3 py-2.5 font-sans text-meta leading-relaxed text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-btn"
      />
    );
  };

  return (
    <Dialog
      open={open}
      onClose={handleClose}
      width={640}
      title={t("spaces.md_title")}
      footer={
        <>
          {saveError?.kind === "stale" && <Button tone="quiet" icon="retry" disabled={saving} onClick={onReload}>{t("spaces.md_reload")}</Button>}
          <Button tone="quiet" disabled={saving} onClick={handleClose}>{t("spaces.cancel")}</Button>
          <Button tone="primary" busy={saving} disabled={!changed} onClick={onSave}>{t("spaces.save")}</Button>
        </>
      }
    >
      <label htmlFor={fieldId} className="mb-3 block text-meta leading-relaxed text-ink-2">{t("spaces.md_intro")}</label>
      {readError ? (
        <div role="alert" className="flex flex-col items-start gap-2 text-meta text-ink-2">
          <p>{readError}</p>
          <Button icon="retry" disabled={saving} onClick={onReload}>{t("spaces.retry")}</Button>
        </div>
      ) : !shape ? (
        <div className="flex justify-center py-8"><Loading /></div>
      ) : (
        <div className="flex flex-col gap-2">{shape.parts.map(part)}</div>
      )}
      {saveError && <p role="alert" className="mt-3 rounded-control bg-orange-tint px-3 py-2 text-meta text-ink">{saveError.text}</p>}
    </Dialog>
  );
}

export function SpaceMdEditor({ open, slug, onClose, onSaved, deps }: SpaceMdEditorProps) {
  const trackedScope = useTrackedScope(deps?.scopeKey, deps?.subscribeScopeKey);

  const mdDep = deps?.md;
  const hasControlledMd = mdDep !== undefined;
  const readControlledMd = useCallback((): SpaceMd | null => {
    if (mdDep === undefined) return null;
    return typeof mdDep === "function" ? mdDep() : mdDep;
  }, [mdDep]);
  const controlledMd = useCachedSnapshot(
    readControlledMd,
    typeof mdDep === "function" ? deps?.subscribeMd : undefined,
  );

  const editorScope = JSON.stringify([trackedScope.token, slug, open ? 1 : 0]);
  const [scope, setScope] = useState<{ key: string; epoch: number }>(() => ({ key: editorScope, epoch: 1 }));
  const effectiveEpoch = scope.key === editorScope ? scope.epoch : scope.epoch + 1;
  if (scope.key !== editorScope) {
    setScope({ key: editorScope, epoch: effectiveEpoch });
  }

  const [mdState, setMdState] = useState<{ epoch: number; value: SpaceMd | null }>({ epoch: 0, value: null });
  const [readErrorState, setReadErrorState] = useState<{ epoch: number; message: string }>({ epoch: 0, message: "" });
  const [saveErrorState, setSaveErrorState] = useState<{ epoch: number; error: SpaceMdSaveError | null }>({ epoch: 0, error: null });
  const [editsState, setEditsState] = useState<{ epoch: number; values: Record<number, string> }>({ epoch: 0, values: {} });
  const [savingState, setSavingState] = useState<{ epoch: number; seq: number; saving: boolean }>({ epoch: 0, seq: 0, saving: false });

  const md = hasControlledMd
    ? controlledMd
    : (mdState.epoch === effectiveEpoch ? mdState.value : null);
  const readError = readErrorState.epoch === effectiveEpoch ? readErrorState.message : "";
  const saveError = saveErrorState.epoch === effectiveEpoch ? saveErrorState.error : null;
  const edits = editsState.epoch === effectiveEpoch ? editsState.values : EMPTY_EDITS;
  const saving = savingState.epoch === effectiveEpoch && savingState.saving;

  const activeEpochRef = useRef(0);
  const loadSeqRef = useRef(0);
  const saveSeqRef = useRef(0);
  const pendingLoadRef = useRef<PendingOp | null>(null);
  const pendingSaveRef = useRef<PendingOp | null>(null);
  const depsRef = useRef(deps);
  const slugRef = useRef(slug);
  const openRef = useRef(open);
  const onCloseRef = useRef(onClose);
  const onSavedRef = useRef(onSaved);
  const hasControlledMdRef = useRef(hasControlledMd);
  const readScopeTokenRef = useRef(trackedScope.readToken);

  useLayoutEffect(() => {
    depsRef.current = deps;
    slugRef.current = slug;
    openRef.current = open;
    onCloseRef.current = onClose;
    onSavedRef.current = onSaved;
    hasControlledMdRef.current = hasControlledMd;
    readScopeTokenRef.current = trackedScope.readToken;
  });

  useLayoutEffect(() => {
    activeEpochRef.current = effectiveEpoch;
    return () => {
      if (activeEpochRef.current === effectiveEpoch) activeEpochRef.current = 0;
      pendingLoadRef.current?.controller.abort();
      pendingLoadRef.current = null;
      pendingSaveRef.current?.controller.abort();
      pendingSaveRef.current = null;
      loadSeqRef.current += 1;
      saveSeqRef.current += 1;
    };
  }, [effectiveEpoch]);

  const capturedEpoch = effectiveEpoch;
  const capturedScopeToken = trackedScope.token;

  const isScopeLive = () =>
    activeEpochRef.current === capturedEpoch
    && readScopeTokenRef.current() === capturedScopeToken
    && slugRef.current === slug
    && openRef.current === open;

  const reportError = (msg: string, kind: SpaceMdError) => {
    try {
      depsRef.current?.onError?.(msg, kind);
    } catch {
      // Error observer failures stay non-blocking.
    }
  };

  const runLoad = useCallback((epoch: number, scopeToken: string, targetSlug: string) => {
    if (pendingSaveRef.current !== null) return;
    if (hasControlledMdRef.current && !depsRef.current?.readSpaceMd) return;

    pendingLoadRef.current?.controller.abort();
    const controller = new AbortController();
    const runSeq = ++loadSeqRef.current;
    pendingLoadRef.current = { epoch, seq: runSeq, controller };

    const isLoadCurrent = () =>
      !controller.signal.aborted
      && activeEpochRef.current === epoch
      && loadSeqRef.current === runSeq
      && readScopeTokenRef.current() === scopeToken
      && slugRef.current === targetSlug
      && openRef.current;

    const finishSuccess = (r: SpaceMd) => {
      if (!isLoadCurrent()) return;
      if (!hasControlledMdRef.current) setMdState({ epoch, value: r });
      setEditsState({ epoch, values: {} });
      setSaveErrorState({ epoch, error: null });
      setReadErrorState({ epoch, message: "" });
    };

    const finishError = (err: unknown) => {
      if (!isLoadCurrent()) return;
      const kind = safeClassifyError(depsRef.current?.spaceMdError, err);
      const msg = spaceMdErrorText(kind, err, depsRef.current?.formatError);
      setReadErrorState({ epoch, message: msg });
      reportError(msg, kind);
    };

    setReadErrorState({ epoch, message: "" });
    let handedOff = false;
    try {
      const res = depsRef.current?.readSpaceMd
        ? depsRef.current.readSpaceMd(targetSlug, controller.signal)
        : readSpaceMd(targetSlug);

      if (isThenable(res)) {
        handedOff = true;
        void (async () => {
          try {
            const awaited = await res;
            finishSuccess(awaited);
          } catch (err) {
            finishError(err);
          } finally {
            if (pendingLoadRef.current?.seq === runSeq) pendingLoadRef.current = null;
          }
        })();
        return;
      }

      finishSuccess(res);
    } catch (err) {
      finishError(err);
    } finally {
      if (!handedOff && pendingLoadRef.current?.seq === runSeq) pendingLoadRef.current = null;
    }
  }, []);

  useEffect(() => {
    if (!open || hasControlledMd) {
      pendingLoadRef.current?.controller.abort();
      pendingLoadRef.current = null;
      loadSeqRef.current += 1;
      return;
    }
    runLoad(capturedEpoch, capturedScopeToken, slug);
    return () => {
      pendingLoadRef.current?.controller.abort();
      pendingLoadRef.current = null;
      loadSeqRef.current += 1;
    };
  }, [open, hasControlledMd, capturedEpoch, capturedScopeToken, slug, runLoad]);

  const shape = useMemo(() => (md ? editableParts(splitSpaceMd(md.content)) : null), [md]);
  const changed = Object.keys(edits).length > 0 && !!shape && assembleSpaceMd(shape.parts, edits) !== md?.content;

  const handleEdit = (index: number, value: string) => {
    if (!isScopeLive()) return;
    setEditsState((prev) => ({
      epoch: capturedEpoch,
      values: prev.epoch === capturedEpoch ? { ...prev.values, [index]: value } : { [index]: value },
    }));
  };

  const handleReload = () => {
    if (!isScopeLive() || pendingSaveRef.current !== null) return;
    runLoad(capturedEpoch, capturedScopeToken, slug);
  };

  const handleClose = () => {
    if (!isScopeLive() || pendingSaveRef.current !== null) return;
    pendingLoadRef.current?.controller.abort();
    pendingLoadRef.current = null;
    loadSeqRef.current += 1;
    onCloseRef.current();
  };

  const handleSave = () => {
    if (!isScopeLive() || !md || !shape || pendingSaveRef.current !== null) return;
    const content = assembleSpaceMd(shape.parts, edits);
    // A field that now holds a block's marker would change what Persona keeps: refused here as the hub would.
    if (!sameManagedBlocks(md.content, content)) {
      const text = t("spaces.md_managed");
      setSaveErrorState({ epoch: capturedEpoch, error: { kind: "managed", text } });
      return;
    }

    const controller = new AbortController();
    const runSeq = ++saveSeqRef.current;
    const baseDigest = md.digest;

    const isRunCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && saveSeqRef.current === runSeq;

    const releaseLock = () => {
      if (pendingSaveRef.current?.seq === runSeq) pendingSaveRef.current = null;
      if (activeEpochRef.current === capturedEpoch && saveSeqRef.current === runSeq) {
        setSavingState((prev) => (prev.epoch === capturedEpoch && prev.seq === runSeq ? { epoch: capturedEpoch, seq: runSeq, saving: false } : prev));
      }
    };

    const finishSuccess = (r: SpaceMd) => {
      if (!isRunCurrent()) return;
      // Supersede any older in-flight read so it cannot overwrite the saved content.
      pendingLoadRef.current?.controller.abort();
      pendingLoadRef.current = null;
      loadSeqRef.current += 1;
      if (!hasControlledMdRef.current) setMdState({ epoch: capturedEpoch, value: r });
      setEditsState({ epoch: capturedEpoch, values: {} });
      setSaveErrorState({ epoch: capturedEpoch, error: null });
      try {
        onSavedRef.current?.(r);
      } catch (err) {
        const kind = safeClassifyError(depsRef.current?.spaceMdError, err);
        reportError(spaceMdErrorText(kind, err, depsRef.current?.formatError), kind);
      }
      safeNotify(depsRef.current?.notify, t("spaces.md_saved"), "ok");
      // Release lock before invoking onClose so dismissal is permitted immediately.
      if (pendingSaveRef.current?.seq === runSeq) pendingSaveRef.current = null;
      try {
        onCloseRef.current();
      } catch {
        // Close callback errors stay non-blocking so busy still clears in finally.
      }
    };

    const finishError = (err: unknown) => {
      if (!isRunCurrent()) return;
      const kind = safeClassifyError(depsRef.current?.spaceMdError, err);
      const text = spaceMdErrorText(kind, err, depsRef.current?.formatError);
      setSaveErrorState({ epoch: capturedEpoch, error: { kind, text } });
      reportError(text, kind);
    };

    pendingSaveRef.current = { epoch: capturedEpoch, seq: runSeq, controller };
    setSavingState({ epoch: capturedEpoch, seq: runSeq, saving: true });
    setSaveErrorState({ epoch: capturedEpoch, error: null });

    let handedOff = false;
    try {
      const res = depsRef.current?.writeSpaceMd
        ? depsRef.current.writeSpaceMd(slug, content, baseDigest, controller.signal)
        : writeSpaceMd(slug, content, baseDigest);

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

  const viewProps: SpaceMdEditorViewProps = {
    open,
    shape,
    edits,
    readError,
    saveError,
    changed,
    saving,
    onEdit: handleEdit,
    onSave: handleSave,
    onReload: handleReload,
    onClose: handleClose,
  };
  if (deps?.renderView) return <>{deps.renderView(viewProps)}</>;
  return <SpaceMdEditorView {...viewProps} />;
}
