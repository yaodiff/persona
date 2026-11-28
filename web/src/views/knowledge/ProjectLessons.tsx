// What a project's accepted tasks taught (#12295, docs/assistant/todos.md "Lessons from accepted tasks"): the lessons
// Persona hands to the project's new Agents, on the project's page for the owner to read, correct and delete. The hub
// keeps them (GET /kb/projects/{id}/lessons, PUT and DELETE …/lessons/{lesson}); every answer is the list as it is
// after the change. A lesson changed on another device meanwhile answers 409 with the list as it is now, which takes
// this one's place. Nothing is drawn while the project has no lessons (or the hub has no such route).
import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { t } from "../../i18n";
import { api, ApiError, enc, errorText } from "../../lib/api";
import { ago } from "../../lib/format";
import { composing } from "../../lib/send-history";
import { sameValue } from "../../lib/stable-lines";
import { Button, Dialog, toast } from "../../ui/atoms";
import { Card, MoreRow, SectionTitle } from "./KBParts";

export interface ProjectLesson {
  id: string;
  kind: string;
  text: string;
  seen: number;
  first_seen_at?: string;
  last_seen_at?: string;
  /** Not taught again for 90 days: no longer handed to new Agents. */
  stale: boolean;
  /** The owner corrected its text. */
  edited: boolean;
  /** The to-dos that taught it, the newest first. */
  sources: { todo_id: string; title?: string }[];
}

export interface ProjectLessonsList {
  /** Whether Persona learns new lessons (Settings › Persona). */
  enabled: boolean;
  lessons: ProjectLesson[];
}

export type ProjectLessonNoticeTone = "info" | "ok" | "error";

export type ProjectLessonsAuth = {
  token: string;
  tokenOwner: string;
  headers: Record<string, string>;
};

export interface EditLessonDialogViewProps {
  lesson: ProjectLesson;
  draft: string;
  busy?: boolean;
  error?: string;
  onDraftChange: (value: string) => void;
  onSave: () => void;
  onClose: () => void;
}

export interface DeleteLessonDialogViewProps {
  lesson: ProjectLesson | null;
  /** Last opened lesson retained during dialog close transitions (defaults to `lesson`). */
  shownLesson?: ProjectLesson | null;
  busy?: boolean;
  error?: string;
  onConfirm: () => void;
  onClose: () => void;
}

export interface ProjectLessonsViewProps {
  list: ProjectLessonsList | null;
  showAll: boolean;
  editing: ProjectLesson | null;
  editDraft: string;
  editBusy?: boolean;
  editError?: string;
  deleting: ProjectLesson | null;
  deleteShown?: ProjectLesson | null;
  deleteBusy?: boolean;
  deleteError?: string;
  /** Fixed timestamp or clock reader (ms) for relative `last_seen_at` formatting. */
  now?: number | (() => number);
  /** Custom relative time formatter for `last_seen_at` (bypasses `ago()`). */
  formatAgo?: (iso: string) => string;
  onToggleAll: () => void;
  onOpenEdit: (lesson: ProjectLesson) => void;
  onCloseEdit: () => void;
  onEditDraftChange: (value: string) => void;
  onSaveEdit: () => void;
  onOpenDelete: (lesson: ProjectLesson) => void;
  onCloseDelete: () => void;
  onConfirmDelete: () => void;
}

/** The hub calls and optional controlled seams; tests and host shells swap them. */
export interface ProjectLessonsDeps {
  list: (signal?: AbortSignal) => Promise<ProjectLessonsList>;
  edit: (lesson: string, text: string, signal?: AbortSignal) => Promise<ProjectLessonsList>;
  remove: (lesson: string, signal?: AbortSignal) => Promise<ProjectLessonsList>;
  /** Optional scope partition (value or getter); switching it resets dialogs, errors, expansion, and fences stale async work. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Controlled lessons payload or getter (bypasses initial `list()` fetch when supplied). */
  data?: ProjectLessonsList | null | (() => ProjectLessonsList | null);
  /** Subscribe to controlled data updates when `data` is a getter. */
  subscribeData?: (listener: () => void) => () => void;
  /** Fixed timestamp or clock reader (ms) for relative `last_seen_at` formatting (bypasses wall-clock `Date.now()`). */
  now?: number | (() => number);
  /** Custom relative time formatter for `last_seen_at` (bypasses `ago()`). */
  formatAgo?: (iso: string) => string;
  /** Show toast notifications (defaults to `atoms.toast`). */
  notify?: (message: string, tone: ProjectLessonNoticeTone) => void;
  /** Format thrown errors (defaults to `api.errorText`). */
  formatError?: (error: unknown) => string;
  /** Error observer invoked when edit or delete fails in the active scope. */
  onError?: (message: string) => void;
  /** Optional custom view renderer (defaults to `<ProjectLessonsView>`). */
  renderView?: (props: ProjectLessonsViewProps) => ReactNode;
}

export interface ProjectLessonsProps {
  deps: ProjectLessonsDeps;
  current: () => boolean;
}

/** The hub keeps a lesson's text within this many characters (lessonTextMax). */
export const LESSON_TEXT_MAX = 200;
/** Lessons shown before "Show all". */
const LESSONS_SHOWN = 5;
const KINDS = new Set(["repeated_mistake", "missing_context", "wasted_step", "improvement"]);

const noopSubscribe = () => () => {};

function resolveNow(now?: number | (() => number)): number | undefined {
  return now === undefined ? undefined : typeof now === "function" ? now() : now;
}

function formatLastSeen(
  iso: string,
  formatAgo?: (iso: string) => string,
  now?: number | (() => number),
): string {
  if (formatAgo) return formatAgo(iso);
  if (now !== undefined) return ago(iso, resolveNow(now));
  return ago(iso);
}

function safeFormatError(fmt: ((error: unknown) => string) | undefined, error: unknown): string {
  if (!fmt) return errorText(error);
  try {
    return fmt(error);
  } catch {
    return errorText(error);
  }
}

function safeNotify(
  notify: ((message: string, tone: ProjectLessonNoticeTone) => void) | undefined,
  message: string,
  tone: ProjectLessonNoticeTone,
) {
  try {
    if (notify) notify(message, tone);
    else toast(message, tone);
  } catch {
    // Notification failures stay non-blocking so recovery never leaves busy stuck.
  }
}

function reportError(onError: ((message: string) => void) | undefined, message: string) {
  try {
    onError?.(message);
  } catch {
    // Error observer failures stay non-blocking so recovery never leaves busy stuck.
  }
}

function hasExtendedDeps(deps: ProjectLessonsDeps): boolean {
  return deps.scopeKey !== undefined
    || deps.subscribeScopeKey !== undefined
    || deps.data !== undefined
    || deps.subscribeData !== undefined
    || deps.now !== undefined
    || deps.formatAgo !== undefined
    || deps.notify !== undefined
    || deps.formatError !== undefined
    || deps.onError !== undefined
    || deps.renderView !== undefined;
}

function callList(deps: ProjectLessonsDeps, signal: AbortSignal): Promise<ProjectLessonsList> {
  return deps.list.length >= 1 || hasExtendedDeps(deps) ? deps.list(signal) : deps.list();
}

function callEdit(deps: ProjectLessonsDeps, lesson: string, text: string, signal: AbortSignal): Promise<ProjectLessonsList> {
  return deps.edit.length >= 3 || hasExtendedDeps(deps) ? deps.edit(lesson, text, signal) : deps.edit(lesson, text);
}

function callRemove(deps: ProjectLessonsDeps, lesson: string, signal: AbortSignal): Promise<ProjectLessonsList> {
  return deps.remove.length >= 2 || hasExtendedDeps(deps) ? deps.remove(lesson, signal) : deps.remove(lesson);
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

export function projectLessonsDeps(project: string, auth: ProjectLessonsAuth): ProjectLessonsDeps {
  const base = `/kb/projects/${enc(project)}/lessons`;
  return {
    list: (signal?: AbortSignal) => api<ProjectLessonsList>(base, signal ? { ...auth, signal } : auth),
    edit: (lesson, text, signal?: AbortSignal) => api<ProjectLessonsList>(`${base}/${enc(lesson)}`, { method: "PUT", json: { text }, ...auth, ...(signal ? { signal } : {}) }),
    remove: (lesson, signal?: AbortSignal) => api<ProjectLessonsList>(`${base}/${enc(lesson)}`, { method: "DELETE", ...auth, ...(signal ? { signal } : {}) }),
  };
}

/** The list a 409 lesson_changed carries: the lessons as they are now. */
function changedList(err: unknown): ProjectLessonsList | null {
  if (!(err instanceof ApiError) || err.status !== 409 || err.code !== "lesson_changed" || !Array.isArray(err.body.lessons)) return null;
  return { enabled: err.body.enabled === true, lessons: err.body.lessons as ProjectLesson[] };
}

function LessonRow({
  lesson,
  now,
  formatAgo,
  onEdit,
  onDelete,
}: {
  lesson: ProjectLesson;
  now?: number | (() => number);
  formatAgo?: (iso: string) => string;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const kind = KINDS.has(lesson.kind) ? lesson.kind : "improvement";
  const source = lesson.sources[0];
  const meta = [
    lesson.seen > 1 ? t("knowledge-projectlessons.seen", { count: lesson.seen }) : "",
    lesson.last_seen_at ? t("knowledge-projectlessons.last_seen", { when: formatLastSeen(lesson.last_seen_at, formatAgo, now) }) : "",
    source ? t("knowledge-projectlessons.from", { task: source.title || t("knowledge-projectlessons.untitled_task") }) : "",
    lesson.edited ? t("knowledge-projectlessons.edited") : "",
  ].filter(Boolean);
  return (
    <div className="flex min-w-0 items-start gap-3">
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 text-submeta">
          <span className="shrink-0 font-semibold text-ink-2">{t(`knowledge-projectlessons.kind_${kind}`)}</span>
          {lesson.stale && <span className="shrink-0 text-ink-3">{t("knowledge-projectlessons.stale")}</span>}
        </div>
        <p className="mt-0.5 break-words text-meta leading-relaxed text-ink select-text">{lesson.text}</p>
        {meta.length > 0 && <p className="mt-1 break-words text-submeta leading-relaxed text-ink-3">{meta.join(" · ")}</p>}
      </div>
      <div className="flex shrink-0 items-center gap-1">
        <Button tone="quiet" onClick={onEdit}>{t("knowledge-projectlessons.edit")}</Button>
        <Button tone="quiet" onClick={onDelete}>{t("knowledge-projectlessons.delete")}</Button>
      </div>
    </div>
  );
}

/** Pure presentational dialog for editing a project lesson's text. */
export function EditLessonDialogView({
  lesson,
  draft,
  busy = false,
  error = "",
  onDraftChange,
  onSave,
  onClose,
}: EditLessonDialogViewProps) {
  const label = useId();
  const text = draft.trim();
  const over = [...draft].length > LESSON_TEXT_MAX;
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={t("knowledge-projectlessons.edit_title")}
      width={480}
      footer={
        <>
          <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="primary" busy={busy} disabled={!text || over || text === lesson.text} onClick={onSave}>{t("common.save")}</Button>
        </>
      }
    >
      <label htmlFor={label} className="mb-2 block text-label text-ink-2">{t(`knowledge-projectlessons.kind_${KINDS.has(lesson.kind) ? lesson.kind : "improvement"}`)}</label>
      <textarea
        id={label}
        value={draft}
        onChange={(event) => onDraftChange(event.target.value)}
        // ⌘↩ / Ctrl+↩ saves, as the other multi-line fields do; Enter alone is a new line
        onKeyDown={(event) => {
          if (event.key !== "Enter" || !(event.metaKey || event.ctrlKey) || composing(event.nativeEvent)) return;
          event.preventDefault();
          if (!busy && text && !over && text !== lesson.text) onSave();
        }}
        disabled={busy}
        rows={3}
        autoFocus
        className="w-full resize-y rounded-control bg-field p-3 text-sm leading-relaxed text-ink shadow-btn outline-none focus:ring-1 focus:ring-accent disabled:opacity-70"
      />
      <p className="mt-2 text-submeta leading-relaxed text-ink-3">
        {over ? t("knowledge-projectlessons.too_long", { max: LESSON_TEXT_MAX }) : t("knowledge-projectlessons.edit_hint")}
      </p>
      {error && <p role="alert" className="mt-2 break-words text-label text-red">{t("knowledge-projectlessons.save_failed", { error })}</p>}
    </Dialog>
  );
}

/** Pure presentational confirmation dialog for deleting a project lesson. */
export function DeleteLessonDialogView({
  lesson,
  shownLesson,
  busy = false,
  error = "",
  onConfirm,
  onClose,
}: DeleteLessonDialogViewProps) {
  const shown = lesson ?? shownLesson ?? null;
  return (
    <Dialog
      open={!!lesson}
      onClose={() => !busy && onClose()}
      title={t("knowledge-projectlessons.delete_title")}
      width={400}
      footer={
        <>
          <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="danger" icon="trash" busy={busy} onClick={onConfirm}>{t("knowledge-projectlessons.delete")}</Button>
        </>
      }
    >
      {shown && <p className="break-words text-meta leading-relaxed text-ink">{shown.text}</p>}
      <p className="mt-2 text-label leading-relaxed text-ink-2">{t("knowledge-projectlessons.delete_body")}</p>
      {error && <p role="alert" className="mt-2 break-words text-label text-red">{t("knowledge-projectlessons.delete_failed", { error })}</p>}
    </Dialog>
  );
}

/** Pure presentational section for a project's lessons and its edit/delete dialogs. */
export function ProjectLessonsView({
  list,
  showAll,
  editing,
  editDraft,
  editBusy = false,
  editError = "",
  deleting,
  deleteShown,
  deleteBusy = false,
  deleteError = "",
  now,
  formatAgo,
  onToggleAll,
  onOpenEdit,
  onCloseEdit,
  onEditDraftChange,
  onSaveEdit,
  onOpenDelete,
  onCloseDelete,
  onConfirmDelete,
}: ProjectLessonsViewProps) {
  const titleId = useId();
  const lessons = list?.lessons ?? [];
  if (lessons.length === 0) return null;
  const shown = showAll ? lessons : lessons.slice(0, LESSONS_SHOWN);
  return (
    <section aria-labelledby={titleId}>
      <SectionTitle title={<span id={titleId}>{t("knowledge-projectlessons.title")}</span>} count={lessons.length} />
      <Card>
        <ul className="divide-y divide-line">
          {shown.map((l) => (
            <li key={l.id} className="px-4 py-3">
              <LessonRow lesson={l} now={now} formatAgo={formatAgo} onEdit={() => onOpenEdit(l)} onDelete={() => onOpenDelete(l)} />
            </li>
          ))}
        </ul>
        {lessons.length > LESSONS_SHOWN && <MoreRow open={showAll} label={t("knowledge-projectlessons.show_all", { count: lessons.length })} onClick={onToggleAll} />}
      </Card>
      <p className="mt-2 px-1 text-submeta leading-relaxed text-ink-3">
        {t(list?.enabled ? "knowledge-projectlessons.footer_on" : "knowledge-projectlessons.footer_off")}
      </p>
      {editing && (
        <EditLessonDialogView
          lesson={editing}
          draft={editDraft}
          busy={editBusy}
          error={editError}
          onDraftChange={onEditDraftChange}
          onSave={onSaveEdit}
          onClose={onCloseEdit}
        />
      )}
      <DeleteLessonDialogView
        lesson={deleting}
        shownLesson={deleteShown}
        busy={deleteBusy}
        error={deleteError}
        onConfirm={onConfirmDelete}
        onClose={onCloseDelete}
      />
    </section>
  );
}

interface PendingOp {
  epoch: number;
  dialogSeq: number;
  seq: number;
  controller: AbortController;
}

interface PendingList {
  epoch: number;
  seq: number;
  controller: AbortController;
}

export function ProjectLessons({ deps, current }: ProjectLessonsProps) {
  const hasExplicitScope = deps.scopeKey !== undefined;
  const trackedScope = useTrackedScope(deps.scopeKey, deps.subscribeScopeKey);

  const dataDep = deps.data;
  const hasControlledData = dataDep !== undefined;
  const isDataGetter = typeof dataDep === "function";
  const subscribeDataDep = deps.subscribeData;
  const subscribeData = useCallback((listener: () => void) => {
    if (!isDataGetter || !subscribeDataDep) return noopSubscribe();
    return subscribeDataDep(listener);
  }, [isDataGetter, subscribeDataDep]);
  const dataCache = useRef<{ has: boolean; value: ProjectLessonsList | null }>({ has: false, value: null });
  const getDataSnapshot = useCallback((): ProjectLessonsList | null => {
    if (dataDep === undefined) return null;
    const next = typeof dataDep === "function" ? dataDep() : dataDep;
    if (dataCache.current.has && sameValue(dataCache.current.value, next)) {
      return dataCache.current.value;
    }
    dataCache.current = { has: true, value: next };
    return next;
  }, [dataDep]);
  const controlledList = useSyncExternalStore(subscribeData, getDataSnapshot, getDataSnapshot);

  const [scope, setScope] = useState<{
    explicit: boolean;
    token: string;
    depsRef: ProjectLessonsDeps | null;
    epoch: number;
  }>(() => ({
    explicit: hasExplicitScope,
    token: hasExplicitScope ? trackedScope.token : "",
    depsRef: hasExplicitScope ? null : deps,
    epoch: 1,
  }));

  const scopeChanged = hasExplicitScope
    ? (!scope.explicit || scope.token !== trackedScope.token)
    : (scope.explicit || scope.depsRef !== deps);
  const effectiveEpoch = scopeChanged ? scope.epoch + 1 : scope.epoch;
  if (scopeChanged) {
    setScope({
      explicit: hasExplicitScope,
      token: hasExplicitScope ? trackedScope.token : "",
      depsRef: hasExplicitScope ? null : deps,
      epoch: effectiveEpoch,
    });
  }

  const [listState, setListState] = useState<{ epoch: number; list: ProjectLessonsList | null }>({ epoch: 0, list: null });
  const [allState, setAllState] = useState<{ epoch: number; open: boolean }>({ epoch: 0, open: false });
  const [editState, setEditState] = useState<{
    epoch: number;
    seq: number;
    lesson: ProjectLesson | null;
    draft: string;
    busy: boolean;
    error: string;
  }>({ epoch: 0, seq: 0, lesson: null, draft: "", busy: false, error: "" });
  const [deleteState, setDeleteState] = useState<{
    epoch: number;
    seq: number;
    lesson: ProjectLesson | null;
    shown: ProjectLesson | null;
    busy: boolean;
    error: string;
  }>({ epoch: 0, seq: 0, lesson: null, shown: null, busy: false, error: "" });

  const activeList = hasControlledData
    ? controlledList
    : (listState.epoch === effectiveEpoch ? listState.list : null);
  const activeLessons = activeList?.lessons ?? [];
  const showAll = allState.epoch === effectiveEpoch && allState.open;
  const liveEditing = editState.epoch === effectiveEpoch && editState.lesson
    ? (activeLessons.some((l) => l.id === editState.lesson!.id) ? editState.lesson : null)
    : null;
  if (editState.epoch === effectiveEpoch && editState.lesson && !liveEditing) {
    setEditState({ epoch: effectiveEpoch, seq: editState.seq + 1, lesson: null, draft: "", busy: false, error: "" });
  }
  const liveDeleting = deleteState.epoch === effectiveEpoch && deleteState.lesson
    ? (activeLessons.some((l) => l.id === deleteState.lesson!.id) ? deleteState.lesson : null)
    : null;
  if (deleteState.epoch === effectiveEpoch && deleteState.lesson && !liveDeleting) {
    setDeleteState({ epoch: effectiveEpoch, seq: deleteState.seq + 1, lesson: null, shown: deleteState.shown, busy: false, error: "" });
  }

  const editing = liveEditing;
  const editSeq = editing ? editState.seq : 0;
  const editDraft = editing ? editState.draft : "";
  const editBusy = Boolean(editing) && editState.busy;
  const editError = editing ? editState.error : "";
  const deleting = liveDeleting;
  const deleteSeq = deleting ? deleteState.seq : 0;
  const deleteShown = deleteState.epoch === effectiveEpoch ? deleteState.shown : null;
  const deleteBusy = Boolean(deleting) && deleteState.busy;
  const deleteError = deleting ? deleteState.error : "";

  const activeEpochRef = useRef(0);
  const activeEditSeqRef = useRef(0);
  const activeDeleteSeqRef = useRef(0);
  const dialogSeqRef = useRef(0);
  const listSeqRef = useRef(0);
  const opSeqRef = useRef(0);
  const pendingListRef = useRef<PendingList | null>(null);
  const pendingEditRef = useRef<PendingOp | null>(null);
  const pendingDeleteRef = useRef<PendingOp | null>(null);
  const depsRef = useRef(deps);
  const currentRef = useRef(current);
  const hasExplicitScopeRef = useRef(hasExplicitScope);
  const hasControlledDataRef = useRef(hasControlledData);
  const readScopeTokenRef = useRef(trackedScope.readToken);

  useLayoutEffect(() => {
    depsRef.current = deps;
    currentRef.current = current;
    hasExplicitScopeRef.current = hasExplicitScope;
    hasControlledDataRef.current = hasControlledData;
    readScopeTokenRef.current = trackedScope.readToken;
  });

  useLayoutEffect(() => {
    activeEpochRef.current = effectiveEpoch;
    return () => {
      if (activeEpochRef.current === effectiveEpoch) activeEpochRef.current = 0;
      pendingListRef.current?.controller.abort();
      pendingListRef.current = null;
      pendingEditRef.current?.controller.abort();
      pendingEditRef.current = null;
      pendingDeleteRef.current?.controller.abort();
      pendingDeleteRef.current = null;
      listSeqRef.current += 1;
      opSeqRef.current += 1;
    };
  }, [effectiveEpoch]);

  useLayoutEffect(() => {
    activeEditSeqRef.current = editSeq;
    return () => {
      if (activeEditSeqRef.current === editSeq) activeEditSeqRef.current = 0;
      if (pendingEditRef.current && pendingEditRef.current.epoch === effectiveEpoch && pendingEditRef.current.dialogSeq === editSeq) {
        pendingEditRef.current.controller.abort();
        pendingEditRef.current = null;
        opSeqRef.current += 1;
      }
    };
  }, [effectiveEpoch, editSeq]);

  useLayoutEffect(() => {
    activeDeleteSeqRef.current = deleteSeq;
    return () => {
      if (activeDeleteSeqRef.current === deleteSeq) activeDeleteSeqRef.current = 0;
      if (pendingDeleteRef.current && pendingDeleteRef.current.epoch === effectiveEpoch && pendingDeleteRef.current.dialogSeq === deleteSeq) {
        pendingDeleteRef.current.controller.abort();
        pendingDeleteRef.current = null;
        opSeqRef.current += 1;
      }
    };
  }, [effectiveEpoch, deleteSeq]);

  const capturedEpoch = effectiveEpoch;
  const capturedScopeToken = trackedScope.token;

  useEffect(() => {
    if (hasControlledData) {
      pendingListRef.current?.controller.abort();
      pendingListRef.current = null;
      listSeqRef.current += 1;
      return;
    }
    const controller = new AbortController();
    const runSeq = ++listSeqRef.current;
    pendingListRef.current?.controller.abort();
    pendingListRef.current = { epoch: capturedEpoch, seq: runSeq, controller };
    const isListCurrent = () =>
      !controller.signal.aborted
      && activeEpochRef.current === capturedEpoch
      && listSeqRef.current === runSeq
      && (!hasExplicitScopeRef.current || readScopeTokenRef.current() === capturedScopeToken)
      && currentRef.current();

    void (async () => {
      try {
        const loaded = await callList(depsRef.current, controller.signal);
        if (isListCurrent()) setListState({ epoch: capturedEpoch, list: loaded });
      } catch {
        if (isListCurrent()) setListState({ epoch: capturedEpoch, list: null });
      } finally {
        if (pendingListRef.current?.seq === runSeq) pendingListRef.current = null;
      }
    })();

    return () => {
      controller.abort();
      if (pendingListRef.current?.seq === runSeq) pendingListRef.current = null;
      if (listSeqRef.current === runSeq) listSeqRef.current += 1;
    };
  }, [capturedEpoch, capturedScopeToken, hasControlledData]);

  const isScopeLive = () =>
    activeEpochRef.current === capturedEpoch
    && (!hasExplicitScopeRef.current || readScopeTokenRef.current() === capturedScopeToken);

  const toggleAll = () => {
    if (!isScopeLive()) return;
    setAllState((prev) => ({ epoch: capturedEpoch, open: prev.epoch === capturedEpoch ? !prev.open : true }));
  };

  const openEdit = (lesson: ProjectLesson) => {
    if (!isScopeLive()) return;
    pendingEditRef.current?.controller.abort();
    pendingEditRef.current = null;
    opSeqRef.current += 1;
    const seq = ++dialogSeqRef.current;
    setEditState({ epoch: capturedEpoch, seq, lesson, draft: lesson.text, busy: false, error: "" });
  };

  const closeEdit = () => {
    if (!isScopeLive()) return;
    pendingEditRef.current?.controller.abort();
    pendingEditRef.current = null;
    opSeqRef.current += 1;
    const seq = ++dialogSeqRef.current;
    setEditState({ epoch: capturedEpoch, seq, lesson: null, draft: "", busy: false, error: "" });
  };

  const handleEditDraftChange = (value: string) => {
    if (!isScopeLive() || !editing || activeEditSeqRef.current !== editSeq) return;
    setEditState((prev) =>
      prev.epoch === capturedEpoch && prev.seq === editSeq && !prev.busy
        ? { ...prev, draft: value }
        : prev,
    );
  };

  const saveEdit = () => {
    const target = editing;
    const runDialogSeq = editSeq;
    const text = editDraft.trim();
    const over = [...editDraft].length > LESSON_TEXT_MAX;
    if (
      !target
      || pendingEditRef.current !== null
      || !isScopeLive()
      || activeEditSeqRef.current !== runDialogSeq
      || !currentRef.current()
      || !text
      || over
      || text === target.text
    ) {
      return;
    }
    const controller = new AbortController();
    const runSeq = ++opSeqRef.current;
    pendingEditRef.current = { epoch: capturedEpoch, dialogSeq: runDialogSeq, seq: runSeq, controller };
    const isRunCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && activeEditSeqRef.current === runDialogSeq
      && pendingEditRef.current?.seq === runSeq
      && currentRef.current();

    setEditState((prev) =>
      prev.epoch === capturedEpoch && prev.seq === runDialogSeq
        ? { ...prev, busy: true, error: "" }
        : prev,
    );

    void (async () => {
      try {
        const nextList = await callEdit(depsRef.current, target.id, text, controller.signal);
        if (!isRunCurrent()) return;
        pendingListRef.current?.controller.abort();
        pendingListRef.current = null;
        listSeqRef.current += 1;
        if (!hasControlledDataRef.current) setListState({ epoch: capturedEpoch, list: nextList });
        const nextDialogSeq = ++dialogSeqRef.current;
        setEditState({ epoch: capturedEpoch, seq: nextDialogSeq, lesson: null, draft: "", busy: false, error: "" });
      } catch (err) {
        if (!isRunCurrent()) return;
        const now = changedList(err);
        if (now) {
          pendingListRef.current?.controller.abort();
          pendingListRef.current = null;
          listSeqRef.current += 1;
          if (!hasControlledDataRef.current) setListState({ epoch: capturedEpoch, list: now });
          safeNotify(depsRef.current.notify, t("knowledge-projectlessons.changed_elsewhere"), "info");
          const nextDialogSeq = ++dialogSeqRef.current;
          setEditState({ epoch: capturedEpoch, seq: nextDialogSeq, lesson: null, draft: "", busy: false, error: "" });
          return;
        }
        const msg = safeFormatError(depsRef.current.formatError, err);
        setEditState((prev) =>
          prev.epoch === capturedEpoch && prev.seq === runDialogSeq
            ? { ...prev, busy: false, error: msg }
            : prev,
        );
        reportError(depsRef.current.onError, msg);
      } finally {
        if (pendingEditRef.current?.seq === runSeq) pendingEditRef.current = null;
        if (activeEpochRef.current === capturedEpoch && activeEditSeqRef.current === runDialogSeq) {
          setEditState((prev) =>
            prev.epoch === capturedEpoch && prev.seq === runDialogSeq && prev.busy
              ? { ...prev, busy: false }
              : prev,
          );
        }
      }
    })();
  };

  const openDelete = (lesson: ProjectLesson) => {
    if (!isScopeLive()) return;
    pendingDeleteRef.current?.controller.abort();
    pendingDeleteRef.current = null;
    opSeqRef.current += 1;
    const seq = ++dialogSeqRef.current;
    setDeleteState({ epoch: capturedEpoch, seq, lesson, shown: lesson, busy: false, error: "" });
  };

  const closeDelete = () => {
    if (!isScopeLive()) return;
    pendingDeleteRef.current?.controller.abort();
    pendingDeleteRef.current = null;
    opSeqRef.current += 1;
    const seq = ++dialogSeqRef.current;
    setDeleteState((prev) => ({
      epoch: capturedEpoch,
      seq,
      lesson: null,
      shown: prev.epoch === capturedEpoch ? prev.shown : null,
      busy: false,
      error: "",
    }));
  };

  const confirmDelete = () => {
    const target = deleting;
    const runDialogSeq = deleteSeq;
    if (
      !target
      || pendingDeleteRef.current !== null
      || !isScopeLive()
      || activeDeleteSeqRef.current !== runDialogSeq
      || !currentRef.current()
    ) {
      return;
    }
    const controller = new AbortController();
    const runSeq = ++opSeqRef.current;
    pendingDeleteRef.current = { epoch: capturedEpoch, dialogSeq: runDialogSeq, seq: runSeq, controller };
    const isRunCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && activeDeleteSeqRef.current === runDialogSeq
      && pendingDeleteRef.current?.seq === runSeq
      && currentRef.current();

    setDeleteState((prev) =>
      prev.epoch === capturedEpoch && prev.seq === runDialogSeq
        ? { ...prev, busy: true, error: "" }
        : prev,
    );

    void (async () => {
      try {
        const nextList = await callRemove(depsRef.current, target.id, controller.signal);
        if (!isRunCurrent()) return;
        pendingListRef.current?.controller.abort();
        pendingListRef.current = null;
        listSeqRef.current += 1;
        if (!hasControlledDataRef.current) setListState({ epoch: capturedEpoch, list: nextList });
        const nextDialogSeq = ++dialogSeqRef.current;
        setDeleteState((prev) => ({
          epoch: capturedEpoch,
          seq: nextDialogSeq,
          lesson: null,
          shown: prev.epoch === capturedEpoch ? prev.shown : target,
          busy: false,
          error: "",
        }));
      } catch (err) {
        if (!isRunCurrent()) return;
        const now = changedList(err);
        if (now) {
          pendingListRef.current?.controller.abort();
          pendingListRef.current = null;
          listSeqRef.current += 1;
          if (!hasControlledDataRef.current) setListState({ epoch: capturedEpoch, list: now });
          safeNotify(depsRef.current.notify, t("knowledge-projectlessons.changed_elsewhere"), "info");
          const nextDialogSeq = ++dialogSeqRef.current;
          setDeleteState((prev) => ({
            epoch: capturedEpoch,
            seq: nextDialogSeq,
            lesson: null,
            shown: prev.epoch === capturedEpoch ? prev.shown : target,
            busy: false,
            error: "",
          }));
          return;
        }
        const msg = safeFormatError(depsRef.current.formatError, err);
        setDeleteState((prev) =>
          prev.epoch === capturedEpoch && prev.seq === runDialogSeq
            ? { ...prev, busy: false, error: msg }
            : prev,
        );
        reportError(depsRef.current.onError, msg);
      } finally {
        if (pendingDeleteRef.current?.seq === runSeq) pendingDeleteRef.current = null;
        if (activeEpochRef.current === capturedEpoch && activeDeleteSeqRef.current === runDialogSeq) {
          setDeleteState((prev) =>
            prev.epoch === capturedEpoch && prev.seq === runDialogSeq && prev.busy
              ? { ...prev, busy: false }
              : prev,
          );
        }
      }
    })();
  };

  const viewProps: ProjectLessonsViewProps = {
    list: activeList,
    showAll,
    editing,
    editDraft,
    editBusy,
    editError,
    deleting,
    deleteShown,
    deleteBusy,
    deleteError,
    now: deps.now,
    formatAgo: deps.formatAgo,
    onToggleAll: toggleAll,
    onOpenEdit: openEdit,
    onCloseEdit: closeEdit,
    onEditDraftChange: handleEditDraftChange,
    onSaveEdit: saveEdit,
    onOpenDelete: openDelete,
    onCloseDelete: closeDelete,
    onConfirmDelete: confirmDelete,
  };
  if (deps.renderView) return <>{deps.renderView(viewProps)}</>;
  return <ProjectLessonsView {...viewProps} />;
}
