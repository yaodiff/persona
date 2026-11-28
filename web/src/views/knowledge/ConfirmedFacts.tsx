import { useCallback, useId, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { t } from "../../i18n";
import { api, enc, errorText } from "../../lib/api";
import { composing } from "../../lib/send-history";
import type { KBUpdate } from "../../lib/kb";
import { Button } from "../../ui/atoms";
import { Card, SectionTitle } from "./KBParts";

export type ConfirmedFactsAuth = {
  token: string;
  tokenOwner: string;
  headers: Record<string, string>;
};

export type ConfirmedFactsPhase = "edit" | "saving" | "refreshing" | "refresh-error";

export interface FactsEditorViewProps {
  draft: string;
  initial: string;
  phase: ConfirmedFactsPhase;
  error: string;
  onDraftChange: (value: string) => void;
  onSave: () => void;
  onRetryRefresh: () => void;
  onClose: () => void;
}

export interface ConfirmedFactsViewProps {
  value: string;
  editing: boolean;
  draft: string;
  initial: string;
  phase: ConfirmedFactsPhase;
  error: string;
  onStartEdit: () => void;
  onDraftChange: (value: string) => void;
  onSave: () => void;
  onRetryRefresh: () => void;
  onClose: () => void;
}

export interface ConfirmedFactsDeps {
  /** Optional scope partition (value or getter); switching it resets editor state and aborts in-flight work. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Persist pinned confirmed facts (defaults to `PUT /kb/{kind}/{id}`). */
  savePinned?: (kind: string, id: string, pinned: string, auth: ConfirmedFactsAuth, signal: AbortSignal) => Promise<unknown> | void;
  /** Format thrown save/refresh errors (defaults to `api.errorText`). */
  formatError?: (error: unknown) => string;
  /** Error observer invoked when save or readback fails in the active scope. */
  onError?: (message: string) => void;
  /** Optional custom view renderer (defaults to `<ConfirmedFactsView>`). */
  renderView?: (props: ConfirmedFactsViewProps) => ReactNode;
}

export type ConfirmedFactsProps = {
  kind: string;
  id: string;
  value: string;
  auth: ConfirmedFactsAuth;
  current: () => boolean;
  refresh: (signal?: AbortSignal) => Promise<boolean>;
  onSaved: () => void;
  deps?: ConfirmedFactsDeps;
};

const noopSubscribe = () => () => {};

function safeFormatError(fmt: ((error: unknown) => string) | undefined, error: unknown): string {
  if (!fmt) return errorText(error);
  try {
    return fmt(error);
  } catch {
    return errorText(error);
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

/** Pure presentational form for editing confirmed facts and handling save/refresh states. */
export function FactsEditorView({
  draft,
  initial,
  phase,
  error,
  onDraftChange,
  onSave,
  onRetryRefresh,
  onClose,
}: FactsEditorViewProps) {
  const label = useId();
  const hint = useId();

  return (
    <form
      onSubmit={(event) => { event.preventDefault(); onSave(); }}
      // ⌘↩ / Ctrl+↩ saves, as the dialogs' multi-line fields do (audit item 70); Enter alone is a new line
      onKeyDown={(event) => {
        if (event.key !== "Enter" || !(event.metaKey || event.ctrlKey) || composing(event.nativeEvent)) return;
        event.preventDefault();
        event.currentTarget.requestSubmit();
      }}
    >
      <label id={label} htmlFor={`${label}-input`} className="mb-2 block text-sm font-medium text-ink">{t("knowledge-confirmedfacts.title")}</label>
      <textarea
        id={`${label}-input`}
        aria-labelledby={label}
        aria-describedby={hint}
        value={draft}
        onChange={(event) => onDraftChange(event.target.value)}
        disabled={phase !== "edit"}
        rows={6}
        autoFocus
        className="w-full resize-y rounded-control bg-field p-3 text-sm leading-relaxed text-ink shadow-btn outline-none placeholder:text-ink-3 focus:ring-1 focus:ring-accent disabled:opacity-70"
      />
      <p id={hint} className="mt-2 text-xs leading-relaxed text-ink-3">{t("knowledge-confirmedfacts.edit_hint")}</p>
      {phase === "refresh-error" ? (
        <div role="alert" className="mt-3 break-words text-sm leading-relaxed text-ink-2">
          <p>{t("knowledge-confirmedfacts.refresh_failed", { error })}</p>
          <Button tone="secondary" className="mt-3" onClick={onRetryRefresh}>{t("knowledge-confirmedfacts.retry_refresh")}</Button>
        </div>
      ) : (
        <>
          {error && <p role="alert" className="mt-3 break-words text-sm text-red">{t("knowledge-confirmedfacts.save_failed", { error })}</p>}
          {phase === "refreshing" && <p role="status" className="mt-3 text-sm text-ink-2">{t("knowledge-confirmedfacts.refreshing")}</p>}
          <div className="mt-4 flex flex-wrap justify-end gap-2">
            <Button tone="quiet" disabled={phase !== "edit"} onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" tone="primary" busy={phase === "saving" || phase === "refreshing"} disabled={phase !== "edit" || draft === initial}>
              {t(error ? "knowledge-confirmedfacts.retry_save" : "common.save")}
            </Button>
          </div>
        </>
      )}
    </form>
  );
}

/** Pure presentational section for viewing or editing confirmed facts. */
export function ConfirmedFactsView({
  value,
  editing,
  draft,
  initial,
  phase,
  error,
  onStartEdit,
  onDraftChange,
  onSave,
  onRetryRefresh,
  onClose,
}: ConfirmedFactsViewProps) {
  return (
    <section>
      <SectionTitle title={t("knowledge-confirmedfacts.title")} right={!editing && (
        <Button tone="quiet" onClick={onStartEdit}>
          {t(value ? "knowledge-confirmedfacts.edit" : "knowledge-confirmedfacts.add")}
        </Button>
      )} />
      <Card>
        <div className="p-4">
          {editing ? (
            <FactsEditorView
              draft={draft}
              initial={initial}
              phase={phase}
              error={error}
              onDraftChange={onDraftChange}
              onSave={onSave}
              onRetryRefresh={onRetryRefresh}
              onClose={onClose}
            />
          ) : (
            <>
              <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-ink select-text">{value || t("knowledge-confirmedfacts.empty")}</p>
              <p className="mt-2 text-xs leading-relaxed text-ink-3">{t("knowledge-confirmedfacts.hint")}</p>
            </>
          )}
        </div>
      </Card>
    </section>
  );
}

interface EditorState {
  epoch: number;
  session: number;
  editing: boolean;
  initial: string;
  draft: string;
  phase: ConfirmedFactsPhase;
  error: string;
}

interface PendingOp {
  epoch: number;
  session: number;
  seq: number;
  controller: AbortController;
}

/** Explicit corrections stay separate from the inferred profile. Each opening starts from authoritative detail. */
export function ConfirmedFacts({ kind, id, value, auth, current, refresh, onSaved, deps }: ConfirmedFactsProps) {
  const trackedScope = useTrackedScope(deps?.scopeKey, deps?.subscribeScopeKey);
  const targetScope = JSON.stringify([trackedScope.token, kind, id]);

  const [scope, setScope] = useState<{ key: string; epoch: number }>(() => ({ key: targetScope, epoch: 1 }));
  const effectiveEpoch = scope.key === targetScope ? scope.epoch : scope.epoch + 1;
  if (scope.key !== targetScope) {
    setScope({ key: targetScope, epoch: effectiveEpoch });
  }

  const [editor, setEditor] = useState<EditorState>({
    epoch: 0,
    session: 0,
    editing: false,
    initial: "",
    draft: "",
    phase: "edit",
    error: "",
  });

  const editing = editor.epoch === effectiveEpoch && editor.editing;
  const editorSession = editing ? editor.session : 0;
  const initial = editing ? editor.initial : value;
  const draft = editing ? editor.draft : value;
  const phase: ConfirmedFactsPhase = editing ? editor.phase : "edit";
  const error = editing ? editor.error : "";

  const activeEpochRef = useRef(0);
  const activeSessionRef = useRef(0);
  const sessionSeqRef = useRef(0);
  const opSeqRef = useRef(0);
  const busyRef = useRef(false);
  const pendingRef = useRef<PendingOp | null>(null);
  const depsRef = useRef(deps);
  const propsRef = useRef({ kind, id, value, auth, current, refresh, onSaved });
  const readScopeTokenRef = useRef(trackedScope.readToken);

  useLayoutEffect(() => {
    depsRef.current = deps;
    propsRef.current = { kind, id, value, auth, current, refresh, onSaved };
    readScopeTokenRef.current = trackedScope.readToken;
  });

  useLayoutEffect(() => {
    activeEpochRef.current = effectiveEpoch;
    return () => {
      if (activeEpochRef.current === effectiveEpoch) activeEpochRef.current = 0;
      pendingRef.current?.controller.abort();
      pendingRef.current = null;
      busyRef.current = false;
      opSeqRef.current += 1;
    };
  }, [effectiveEpoch]);

  useLayoutEffect(() => {
    activeSessionRef.current = editorSession;
    return () => {
      if (activeSessionRef.current === editorSession) activeSessionRef.current = 0;
      if (pendingRef.current && pendingRef.current.epoch === effectiveEpoch && pendingRef.current.session === editorSession) {
        pendingRef.current.controller.abort();
        pendingRef.current = null;
        busyRef.current = false;
        opSeqRef.current += 1;
      }
    };
  }, [effectiveEpoch, editorSession]);

  const capturedEpoch = effectiveEpoch;
  const capturedScopeToken = trackedScope.token;

  const isScopeLive = () =>
    activeEpochRef.current === capturedEpoch
    && readScopeTokenRef.current() === capturedScopeToken;

  const reportError = (msg: string) => {
    try {
      depsRef.current?.onError?.(msg);
    } catch {
      // Error observer failures stay non-blocking so recovery never leaves busy stuck.
    }
  };

  const startEdit = () => {
    if (!isScopeLive() || editing || !propsRef.current.current()) return;
    pendingRef.current?.controller.abort();
    pendingRef.current = null;
    busyRef.current = false;
    opSeqRef.current += 1;
    const nextSession = ++sessionSeqRef.current;
    const currentVal = propsRef.current.value;
    setEditor({
      epoch: capturedEpoch,
      session: nextSession,
      editing: true,
      initial: currentVal,
      draft: currentVal,
      phase: "edit",
      error: "",
    });
  };

  const closeEditor = () => {
    if (!isScopeLive()) return;
    pendingRef.current?.controller.abort();
    pendingRef.current = null;
    busyRef.current = false;
    opSeqRef.current += 1;
    const nextSession = ++sessionSeqRef.current;
    setEditor({
      epoch: capturedEpoch,
      session: nextSession,
      editing: false,
      initial: "",
      draft: "",
      phase: "edit",
      error: "",
    });
  };

  const handleDraftChange = (nextDraft: string) => {
    if (!isScopeLive() || !editing || activeSessionRef.current !== editorSession) return;
    setEditor((prev) =>
      prev.epoch === capturedEpoch && prev.session === editorSession && prev.phase === "edit"
        ? { ...prev, draft: nextDraft }
        : prev,
    );
  };

  const runReadback = async (runSession: number, existingController?: AbortController, existingSeq?: number) => {
    const controller = existingController ?? new AbortController();
    const runSeq = existingSeq ?? ++opSeqRef.current;
    pendingRef.current = { epoch: capturedEpoch, session: runSession, seq: runSeq, controller };
    const stillCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && activeSessionRef.current === runSession
      && opSeqRef.current === runSeq
      && propsRef.current.current();

    setEditor((prev) =>
      prev.epoch === capturedEpoch && prev.session === runSession
        ? { ...prev, phase: "refreshing", error: "" }
        : prev,
    );
    try {
      const loaded = await propsRef.current.refresh(controller.signal);
      if (!stillCurrent()) return;
      if (!loaded) {
        setEditor((prev) =>
          prev.epoch === capturedEpoch && prev.session === runSession
            ? { ...prev, phase: "refresh-error" }
            : prev,
        );
        return;
      }
      const nextSession = ++sessionSeqRef.current;
      setEditor({
        epoch: capturedEpoch,
        session: nextSession,
        editing: false,
        initial: "",
        draft: "",
        phase: "edit",
        error: "",
      });
      try {
        propsRef.current.onSaved();
      } catch {
        // Save observer failures stay non-blocking so recovery never leaves busy stuck.
      }
    } catch (err) {
      if (!stillCurrent()) return;
      const msg = safeFormatError(depsRef.current?.formatError, err);
      setEditor((prev) =>
        prev.epoch === capturedEpoch && prev.session === runSession
          ? { ...prev, error: msg, phase: "refresh-error" }
          : prev,
      );
      reportError(msg);
    } finally {
      if (pendingRef.current?.seq === runSeq) {
        pendingRef.current = null;
        busyRef.current = false;
      }
    }
  };

  const save = async () => {
    const runSession = editorSession;
    // Admit synchronously: two events can arrive before React paints the busy button.
    if (
      busyRef.current
      || !editing
      || !isScopeLive()
      || activeSessionRef.current !== runSession
      || !propsRef.current.current()
      || phase !== "edit"
      || draft === initial
    ) {
      return;
    }
    busyRef.current = true;
    const controller = new AbortController();
    const runSeq = ++opSeqRef.current;
    pendingRef.current = { epoch: capturedEpoch, session: runSession, seq: runSeq, controller };
    const stillCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && activeSessionRef.current === runSession
      && opSeqRef.current === runSeq
      && propsRef.current.current();

    const curKind = propsRef.current.kind;
    const curId = propsRef.current.id;
    const curAuth = propsRef.current.auth;
    const nextPinned = draft;

    setEditor((prev) =>
      prev.epoch === capturedEpoch && prev.session === runSession
        ? { ...prev, phase: "saving", error: "" }
        : prev,
    );

    let handedToReadback = false;
    try {
      const savePinned = depsRef.current?.savePinned;
      if (savePinned) {
        await savePinned(curKind, curId, nextPinned, curAuth, controller.signal);
      } else {
        const update: KBUpdate = { pinned: nextPinned };
        await api(`/kb/${enc(curKind)}/${enc(curId)}`, { method: "PUT", json: update, ...curAuth, signal: controller.signal });
      }
      if (!stillCurrent()) return;
      handedToReadback = true;
    } catch (err) {
      if (!stillCurrent()) return;
      const msg = safeFormatError(depsRef.current?.formatError, err);
      setEditor((prev) =>
        prev.epoch === capturedEpoch && prev.session === runSession
          ? { ...prev, error: msg, phase: "edit" }
          : prev,
      );
      reportError(msg);
      return;
    } finally {
      if (!handedToReadback && pendingRef.current?.seq === runSeq) {
        pendingRef.current = null;
        busyRef.current = false;
      }
    }
    await runReadback(runSession, controller, runSeq);
  };

  const retryRefresh = () => {
    const runSession = editorSession;
    if (
      busyRef.current
      || !editing
      || phase !== "refresh-error"
      || !isScopeLive()
      || activeSessionRef.current !== runSession
      || !propsRef.current.current()
    ) {
      return;
    }
    busyRef.current = true;
    void runReadback(runSession);
  };

  const viewProps: ConfirmedFactsViewProps = {
    value,
    editing,
    draft,
    initial,
    phase,
    error,
    onStartEdit: startEdit,
    onDraftChange: handleDraftChange,
    onSave: () => { void save(); },
    onRetryRefresh: retryRefresh,
    onClose: closeEditor,
  };
  if (deps?.renderView) return <>{deps.renderView(viewProps)}</>;
  return <ConfirmedFactsView {...viewProps} />;
}
