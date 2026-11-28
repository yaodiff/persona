// Hub-separated meeting speakers UI (#17674, #17675, docs/assistant/meeting-minutes.md "Speakers (#17674)",
// docs/design/DESIGN_SYSTEM.md "Web: speakers in the transcript and the minutes (#17675)"):
//   - useRecordingSpeakers: local-first optimistic state reconciled with POST /api/v1/recordings/{id}/speakers,
//     live updates on `meeting_live` (`speakers`) and 5 s visible polling while `waiting`;
//   - SpeakerAvatar: neutral circular avatar (never coloured per speaker), hidden from screen readers;
//   - RecordingSpeakersBar: status / entry row above the transcript (waiting, ready ok/low, failed by reason);
//   - RecordingSpeakersDialog: one-screen naming page (inputs, one-tap candidates, samples, attributed positions,
//     folded "Rarely spoke", merged speakers with Unmerge, inline Undo, and menu -> Delete);
//   - DeleteSpeakersConfirmDialog: single confirmation before irreversible `clear`.
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, enc, errorText } from "../../lib/api";
import type { KBDetail } from "../../lib/kb";
import { messageManager } from "../../lib/manager-choice";
import { onHubEvent } from "../../lib/store";
import { documentPage, visiblePoll } from "../../lib/visible-poll";
import { composing } from "../../lib/send-history";
import { Button, Collapse, cx, Dialog, IconButton, Menu, Pill, Spinner, toast, useMenu, type MenuItem } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { inputClass } from "../../views/assistant/parts";
import { minutesDebates } from "./minutes-debates";
import "./minutes-overview.css";
import type { RecordingAuth } from "./RecordingMinutes";
import type { RecordingView } from "./recording-view";
import {
  applyOptimisticSpeakerEdit,
  canonicalSpeakerId,
  decodeSpeakers,
  formatSpeakerStats,
  hasVisibleSpeakers,
  MAX_SPEAKER_NAME_RUNES,
  runeLength,
  speakerAvatarGlyph,
  speakerCandidateNames,
  speakerLabel,
  speakerPositions,
  speakerSampleLines,
  waitingSpeakerMinutes,
  type DetailSpeakers,
  type SpeakerEditOp,
  type SpeakerFailReason,
  type SpeakerPerson,
} from "./speakers";

const WAITING_POLL_MS = 5000;

export interface SpeakerUndoState {
  label: string;
  ops: SpeakerEditOp[];
}

export interface SpeakersController {
  /** Decoded & reconciled speakers block (null when absent, malformed or unknown state). */
  speakers: DetailSpeakers | null;
  /** True while a write request to the hub is in flight. */
  busy: boolean;
  /** Latest reversible action's hub-computed undo ops, cleared after undo or clear. */
  lastUndo: SpeakerUndoState | null;
  /** Inline error message from the last failed write (reverted locally). */
  error: string;
  clearError: () => void;
  /** Apply edit ops optimistically and reconcile with POST /api/v1/recordings/{id}/speakers. */
  edit: (ops: SpeakerEditOp[], summaryLabel: string) => Promise<boolean>;
  /** Replay the server-computed undo ops from the last name/merge/unmerge. */
  undo: () => Promise<boolean>;
  /** Irreversibly clear this meeting's speakers (`op: "clear"`). */
  clear: () => Promise<boolean>;
}

/** Local-first state and hub sync for a recording's `detail.speakers` block. */
export function useRecordingSpeakers(
  detail: KBDetail | null | undefined,
  auth: RecordingAuth,
  current: () => boolean,
  manager: string,
  reload: () => Promise<boolean>,
): SpeakersController {
  const id = detail?.id ?? "";
  const fromDetail = useMemo(() => decodeSpeakers(detail?.speakers), [detail?.speakers]);
  // Optimistic / hub-reconciled override keyed by recording id, kept when its `rev` is at least `fromDetail.rev` so a
  // stale background reload never reverts a newer edit.
  const [override, setOverride] = useState<{ id: string; speakers: DetailSpeakers } | null>(null);
  const [busy, setBusy] = useState(false);
  const [lastUndo, setLastUndo] = useState<{ id: string; item: SpeakerUndoState } | null>(null);
  const [errorState, setErrorState] = useState<{ id: string; text: string } | null>(null);

  const speakers: DetailSpeakers | null = useMemo(() => {
    if (override && override.id === id) {
      if (!fromDetail || override.speakers.rev >= fromDetail.rev) return override.speakers;
    }
    return fromDetail;
  }, [override, id, fromDetail]);

  // Keep refs to the latest acknowledged hub state and pending queue so rapid sequential edits never race `rev`.
  const ackRef = useRef<{ id: string; speakers: DetailSpeakers | null }>({ id, speakers: fromDetail });
  if (ackRef.current.id !== id) {
    ackRef.current = { id, speakers: fromDetail };
  } else if (fromDetail && (!ackRef.current.speakers || fromDetail.rev >= ackRef.current.speakers.rev)) {
    ackRef.current = { id, speakers: fromDetail };
  }

  const shownId = useRef(id);
  shownId.current = id;
  const authRef = useRef(auth);
  authRef.current = auth;
  const currentRef = useRef(current);
  currentRef.current = current;
  const reloadRef = useRef(reload);
  reloadRef.current = reload;
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  const valid = useCallback(
    (forId: string) => alive.current && currentRef.current() && shownId.current === forId,
    [],
  );

  // Serialize writes in order so each mutation uses the previous hub response's updated `rev`.
  const chainRef = useRef<Promise<void>>(Promise.resolve());
  const pendingCount = useRef(0);
  const optimisticRef = useRef<{ id: string; speakers: DetailSpeakers | null }>({ id, speakers });
  optimisticRef.current = { id, speakers };

  // Poll while `waiting` (fallback) and reload immediately on `meeting_live` (`changed` includes `"speakers"`).
  const waiting = speakers?.state === "waiting";
  useEffect(() => {
    if (!id || !waiting) return;
    let skip = documentPage.visible();
    return visiblePoll(() => {
      if (skip) {
        skip = false;
        return;
      }
      if (pendingCount.current === 0 && valid(id)) void reloadRef.current().catch(() => {});
    }, WAITING_POLL_MS, documentPage);
  }, [id, waiting, valid]);

  useEffect(() => {
    if (!id) return;
    return onHubEvent("meeting_live", (msg) => {
      if (String(msg.recording_id ?? "").toLowerCase() !== id.toLowerCase() || messageManager(msg) !== manager) return;
      const changed = Array.isArray(msg.changed) ? msg.changed : [];
      if (!changed.includes("speakers")) return;
      const revs = msg.revs && typeof msg.revs === "object" ? (msg.revs as Record<string, unknown>) : undefined;
      const nextRev = typeof revs?.speakers === "number" ? revs.speakers : undefined;
      const knownRev = ackRef.current.id === id ? ackRef.current.speakers?.rev : undefined;
      if (nextRev !== undefined && knownRev !== undefined && nextRev <= knownRev) return;
      if (pendingCount.current === 0 && valid(id)) void reloadRef.current().catch(() => {});
    });
  }, [id, manager, valid]);

  const sendEdit = useCallback(
    (ops: SpeakerEditOp[], summaryLabel: string, isUndo: boolean): Promise<boolean> => {
      const targetId = shownId.current;
      const baseOptimistic = optimisticRef.current.id === targetId ? optimisticRef.current.speakers : null;
      if (!targetId || !baseOptimistic || !ops.length) return Promise.resolve(false);

      // Apply local-first optimistic update immediately before any network work.
      const existingMe = baseOptimistic.people.find((p) => p.me && !p.merged_into)?.name ?? "";
      const optimistic = applyOptimisticSpeakerEdit(baseOptimistic, ops, existingMe);
      if (optimistic) {
        setOverride({ id: targetId, speakers: optimistic.speakers });
        optimisticRef.current = { id: targetId, speakers: optimistic.speakers };
      }
      setErrorState(null);
      if (isUndo) setLastUndo(null);
      else if (ops.some((o) => o.op === "clear")) setLastUndo(null);

      pendingCount.current++;
      setBusy(true);

      let resolveOuter!: (ok: boolean) => void;
      const outer = new Promise<boolean>((r) => { resolveOuter = r; });

      chainRef.current = chainRef.current.then(async () => {
        if (!valid(targetId)) {
          resolveOuter(false);
          return;
        }
        const authoritative = ackRef.current.id === targetId ? ackRef.current.speakers : baseOptimistic;
        const rev = authoritative?.rev ?? baseOptimistic.rev;
        try {
          const resp = await api<{ speakers?: unknown; undo?: unknown }>(`/recordings/${enc(targetId)}/speakers`, {
            method: "POST",
            json: { rev, ops },
            ...authRef.current,
          });
          if (!valid(targetId)) {
            resolveOuter(false);
            return;
          }
          const decoded = decodeSpeakers(resp.speakers);
          if (decoded) {
            ackRef.current = { id: targetId, speakers: decoded };
            setOverride({ id: targetId, speakers: decoded });
            optimisticRef.current = { id: targetId, speakers: decoded };
          }
          const serverUndo = Array.isArray(resp.undo) ? (resp.undo as SpeakerEditOp[]) : [];
          if (ops.some((o) => o.op === "clear")) {
            setLastUndo(null);
            toast(t("recording.speakers_deleted"), "ok");
          } else if (isUndo) {
            setLastUndo(null);
            toast(t("recording.speakers_undone"), "ok");
          } else if (serverUndo.length > 0) {
            setLastUndo({ id: targetId, item: { label: summaryLabel, ops: serverUndo } });
          } else {
            setLastUndo(null);
          }
          // Refresh the full detail in the background so minutes Markdown & server-aligned segments stay current.
          void reloadRef.current().catch(() => {});
          resolveOuter(true);
        } catch (err) {
          if (!valid(targetId)) {
            resolveOuter(false);
            return;
          }
          // Revert optimistic state back to the last hub-acknowledged state and explain why.
          const reverted = ackRef.current.id === targetId ? ackRef.current.speakers : null;
          if (reverted) {
            setOverride({ id: targetId, speakers: reverted });
            optimisticRef.current = { id: targetId, speakers: reverted };
          } else {
            setOverride(null);
          }
          let message: string;
          if (err instanceof ApiError && err.status === 409 && err.code === "speakers_stale") {
            message = t("recording.speakers_stale");
            void reloadRef.current().catch(() => {});
          } else if (err instanceof ApiError && err.status === 409 && err.code === "speakers_cleared") {
            message = t("recording.speakers_cleared_err");
            void reloadRef.current().catch(() => {});
          } else {
            message = t("recording.speakers_edit_failed", { error: errorText(err) });
          }
          setErrorState({ id: targetId, text: message });
          toast(message, "error");
          resolveOuter(false);
        } finally {
          pendingCount.current = Math.max(0, pendingCount.current - 1);
          if (alive.current && pendingCount.current === 0) setBusy(false);
        }
      });

      return outer;
    },
    [valid],
  );

  const edit = useCallback(
    (ops: SpeakerEditOp[], summaryLabel: string) => sendEdit(ops, summaryLabel, false),
    [sendEdit],
  );

  const undo = useCallback(() => {
    if (!lastUndo || lastUndo.id !== id || !lastUndo.item.ops.length) return Promise.resolve(false);
    return sendEdit(lastUndo.item.ops, t("recording.speakers_undone"), true);
  }, [id, lastUndo, sendEdit]);

  const clear = useCallback(
    () => sendEdit([{ op: "clear" }], t("recording.speakers_deleted"), false),
    [sendEdit],
  );

  const clearError = useCallback(() => setErrorState(null), []);

  return {
    speakers,
    busy,
    lastUndo: lastUndo && lastUndo.id === id ? lastUndo.item : null,
    error: errorState && errorState.id === id ? errorState.text : "",
    clearError,
    edit,
    undo,
    clear,
  };
}

/** Neutral circular speaker avatar: shows the name's first character or the speaker number; never coloured per speaker,
 *  and always hidden from screen readers so the adjacent text label is read cleanly once. */
export function SpeakerAvatar({
  person,
  size = "sm",
}: {
  person: Pick<SpeakerPerson, "id" | "name">;
  size?: "sm" | "lg";
}) {
  return (
    <span
      aria-hidden="true"
      data-speaker-avatar
      className={cx(
        "inline-flex shrink-0 items-center justify-center rounded-full bg-field font-semibold text-ink-2 select-none",
        size === "lg" ? "speaker-avatar-lg text-label" : "speaker-avatar text-submeta",
      )}
    >
      {speakerAvatarGlyph(person)}
    </span>
  );
}

/** Localized wording for a known failure reason; returns empty string for unknown reasons. */
export function speakerFailureText(reason: SpeakerFailReason): string {
  switch (reason) {
    case "too_long":
      return t("recording.speakers_failed_too_long");
    case "timeout":
      return t("recording.speakers_failed_timeout");
    case "engine_unavailable":
      return t("recording.speakers_failed_engine_unavailable");
    case "no_audio":
      return t("recording.speakers_failed_no_audio");
    case "gave_up":
    case "audio_gone":
    case "error":
      return t("recording.speakers_failed_other");
    case "unknown":
      return "";
  }
}

/** Row above the transcript showing speaker separation status or opening the naming page. Returns null when there is
 *  no `speakers` block, when `cleared`, or when `failed` carries an unknown reason. */
export function RecordingSpeakersBar({
  speakers,
  busy,
  onOpenNaming,
  onDelete,
}: {
  speakers: DetailSpeakers | null;
  busy?: boolean;
  onOpenNaming: () => void;
  onDelete: () => void;
}) {
  if (!hasVisibleSpeakers(speakers)) return null;

  if (speakers.state === "waiting") {
    const progress = waitingSpeakerMinutes(speakers.done, speakers.end);
    const label = !progress
      ? t("recording.speakers_waiting")
      : progress.total !== undefined
        ? t("recording.speakers_waiting_progress", { done: progress.done, total: progress.total })
        : t("recording.speakers_waiting_done", { done: progress.done });
    return (
      <div
        role="status"
        data-speakers-bar="waiting"
        className="flex items-center gap-2.5 px-4 py-2.5 text-label leading-relaxed text-ink-2"
      >
        <Spinner size={12} />
        <span className="min-w-0 break-words">{label}</span>
      </div>
    );
  }

  if (speakers.state === "failed") {
    const reasonText = speakerFailureText(speakers.reason);
    if (!reasonText) return null;
    return (
      <div
        role="status"
        data-speakers-bar="failed"
        data-speakers-reason={speakers.reason}
        className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5"
      >
        <div className="flex min-w-0 flex-1 items-start gap-2 text-label leading-relaxed text-ink-2">
          <Icon name="info" size={14} className="mt-0.5 shrink-0 text-ink-3" />
          <span className="min-w-0 break-words">{reasonText}</span>
        </div>
        <Button
          tone="quiet"
          size="sm"
          disabled={busy}
          aria-label={t("recording.speakers_delete")}
          onClick={onDelete}
        >
          {t("recording.speakers_delete_short")}
        </Button>
      </div>
    );
  }

  // state === "ready" (ok or low, whole or trimmed)
  const unmerged = speakers.people.filter((p) => !p.merged_into);
  const mainSpeakers = unmerged.filter((p) => !p.minor);
  const listed = mainSpeakers.length ? mainSpeakers : unmerged;
  const formatNum = (n: number) => t("recording.speaker_n", { n });
  const namedCount = listed.filter((p) => !!p.name).length;
  const summary = listed.length === 0
    ? t("recording.speakers_empty")
    : namedCount > 0
      ? listed.map((p) => speakerLabel(p, formatNum)).join(" · ")
      : t("recording.speakers_count", { count: listed.length });

  return (
    <div data-speakers-bar="ready" data-speakers-quality={speakers.quality}>
      <button
        type="button"
        data-speakers-open
        onClick={onOpenNaming}
        className="flex min-h-11 w-full items-center gap-2.5 px-4 py-2.5 text-left transition-colors hover:bg-hover"
      >
        <Icon name="users" size={14} className="shrink-0 text-ink-3" />
        <span className="shrink-0 whitespace-nowrap text-meta font-medium text-ink">
          {t("recording.speakers_title")}
        </span>
        <span className="min-w-0 flex-1 truncate text-label text-ink-3">{summary}</span>
        <Icon name="chevronRight" size={13} stroke={2.2} className="shrink-0 text-ink-3" />
      </button>
      {speakers.quality === "low" && (
        <p
          role="note"
          data-speakers-low-quality
          className="flex items-start gap-2 border-t border-line bg-inset px-4 py-2 text-label leading-relaxed text-ink-2"
        >
          <Icon name="info" size={13} className="mt-0.5 shrink-0 text-ink-3" />
          <span className="min-w-0 break-words">{t("recording.speakers_low_quality")}</span>
        </p>
      )}
    </div>
  );
}

/** One-screen naming page (shared `Dialog`): every speaker's stats, name field, one-tap candidates, sample lines and
 *  attributed disagreement positions, folded "Rarely spoke", merged speakers with Unmerge, inline Undo and Delete menu. */
export function RecordingSpeakersDialog({
  open,
  onClose,
  speakers,
  detail,
  rec,
  busy,
  lastUndo,
  error,
  onEdit,
  onUndo,
  onAskDelete,
  onJumpSample,
}: {
  open: boolean;
  onClose: () => void;
  speakers: DetailSpeakers;
  detail: KBDetail;
  rec: RecordingView;
  busy: boolean;
  lastUndo: SpeakerUndoState | null;
  error: string;
  onEdit: (ops: SpeakerEditOp[], summaryLabel: string) => Promise<boolean>;
  onUndo: () => Promise<boolean>;
  onAskDelete: () => void;
  onJumpSample: (lineId: string) => void;
}) {
  const [showMinor, setShowMinor] = useState(false);
  const menu = useMenu();
  const minorId = useId();
  const formatNum = useCallback((n: number) => t("recording.speaker_n", { n }), []);

  const debates = useMemo(
    () =>
      minutesDebates(
        detail.minutes?.debates,
        Array.isArray(detail.minutes?.decisions) ? detail.minutes.decisions.length : 0,
      ),
    [detail.minutes?.debates, detail.minutes?.decisions],
  );

  const unmerged = useMemo(() => speakers.people.filter((p) => !p.merged_into), [speakers.people]);
  const mainPeople = useMemo(() => unmerged.filter((p) => !p.minor), [unmerged]);
  const minorPeople = useMemo(() => unmerged.filter((p) => p.minor), [unmerged]);
  const mergedPeople = useMemo(() => speakers.people.filter((p) => !!p.merged_into), [speakers.people]);
  const byId = useMemo(() => new Map(speakers.people.map((p) => [p.id, p])), [speakers.people]);

  const menuItems = useMemo<MenuItem[]>(
    () => [
      {
        key: "delete-speakers",
        label: t("recording.speakers_delete"),
        icon: "trash",
        danger: true,
        onSelect: onAskDelete,
      },
    ],
    [onAskDelete],
  );

  const handleName = useCallback(
    (person: SpeakerPerson, nextName: string, me = false) => {
      const trimmed = nextName.trim();
      const existingHolder = trimmed
        ? speakers.people.find((p) => p.id !== person.id && !p.merged_into && p.name === trimmed)
        : undefined;
      const summary = me && !trimmed
        ? t("recording.speakers_named", { name: t("recording.speakers_me") })
        : !trimmed
          ? t("recording.speakers_name_cleared")
          : existingHolder
            ? t("recording.speakers_merged_notice", { name: trimmed })
            : t("recording.speakers_named", { name: trimmed });
      const op: SpeakerEditOp = me
        ? { op: "name", id: person.id, name: trimmed, me: true }
        : { op: "name", id: person.id, name: trimmed };
      void onEdit([op], summary);
    },
    [speakers.people, onEdit],
  );

  const handleUnmerge = useCallback(
    (person: SpeakerPerson) => {
      const label = speakerLabel({ id: person.id }, formatNum);
      void onEdit([{ op: "unmerge", id: person.id }], t("recording.speakers_unmerged_notice", { speaker: label }));
    },
    [formatNum, onEdit],
  );

  const renderRow = (person: SpeakerPerson) => (
    <SpeakerNamingRow
      key={person.id}
      person={person}
      speakers={speakers}
      rec={rec}
      debates={debates}
      busy={busy}
      onName={handleName}
      onJumpSample={onJumpSample}
    />
  );

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("recording.speakers_title")}
      width={560}
      footer={
        <div className="flex w-full items-center justify-between gap-2">
          <div className="flex items-center gap-1.5">
            <IconButton
              icon="more"
              label={t("recording.speakers_menu")}
              disabled={busy}
              onClick={menu.open}
            />
            {menu.anchor && (
              <Menu
                items={menuItems}
                anchor={menu.anchor}
                onClose={menu.close}
                side="top"
                align="left"
              />
            )}
          </div>
          <Button tone="primary" onClick={onClose}>
            {t("recording.speakers_done")}
          </Button>
        </div>
      }
    >
      <div data-speakers-dialog className="space-y-3">
        {speakers.quality === "low" && (
          <p
            role="note"
            data-speakers-low-quality
            className="flex items-start gap-2 rounded-control bg-inset px-3 py-2 text-label leading-relaxed text-ink-2 shadow-hairline"
          >
            <Icon name="info" size={13} className="mt-0.5 shrink-0 text-ink-3" />
            <span className="min-w-0 break-words">{t("recording.speakers_low_quality")}</span>
          </p>
        )}

        {error && (
          <div
            role="alert"
            data-speakers-error
            className="flex items-start gap-2 rounded-control bg-inset px-3 py-2 text-label leading-relaxed text-red shadow-hairline"
          >
            <Icon name="warning" size={13} className="mt-0.5 shrink-0 text-red" />
            <span className="min-w-0 break-words">{error}</span>
          </div>
        )}

        {lastUndo && (
          <div
            role="status"
            data-speakers-undo-bar
            className="flex flex-wrap items-center justify-between gap-2 rounded-control bg-inset px-3 py-2 shadow-hairline"
          >
            <span className="min-w-0 break-words text-label text-ink-2">{lastUndo.label}</span>
            <Button tone="secondary" size="sm" icon="undo" disabled={busy} onClick={() => void onUndo()}>
              {t("recording.speakers_undo")}
            </Button>
          </div>
        )}

        {mainPeople.length === 0 && minorPeople.length === 0 && (
          <p className="py-4 text-center text-meta text-ink-3">{t("recording.speakers_empty")}</p>
        )}

        {mainPeople.length > 0 && (
          <div data-speakers-list className="space-y-3">
            {mainPeople.map(renderRow)}
          </div>
        )}

        {minorPeople.length > 0 && (
          <section data-speakers-minor className="rounded-card bg-inset shadow-hairline">
            <button
              type="button"
              data-speakers-minor-toggle
              aria-expanded={showMinor}
              aria-controls={minorId}
              onClick={() => setShowMinor((x) => !x)}
              className="flex min-h-11 w-full items-center justify-between gap-2 px-3 py-2 text-left transition-colors hover:bg-hover"
            >
              <span className="min-w-0 truncate text-label font-medium text-ink-2">
                {t("recording.speakers_rarely_spoke", { count: minorPeople.length })}
              </span>
              <Icon
                name="chevron"
                size={13}
                stroke={2.2}
                className={cx("shrink-0 text-ink-3 transition-transform duration-200", showMinor && "rotate-180")}
              />
            </button>
            <Collapse open={showMinor} lazy>
              <div id={minorId} className="space-y-3 border-t border-line p-3">
                {minorPeople.map(renderRow)}
              </div>
            </Collapse>
          </section>
        )}

        {mergedPeople.length > 0 && (
          <section data-speakers-merged className="border-t border-line pt-3">
            <h3 className="mb-2 text-label font-medium text-ink-3">
              {t("recording.speakers_merged_title")}
            </h3>
            <ul className="space-y-2">
              {mergedPeople.map((person) => {
                const targetId = canonicalSpeakerId(speakers.people, person.merged_into ?? person.id);
                const target = byId.get(targetId);
                const targetName = speakerLabel(target ?? { id: targetId }, formatNum);
                const ownLabel = speakerLabel({ id: person.id }, formatNum);
                return (
                  <li
                    key={person.id}
                    data-merged-speaker={person.id}
                    className="flex flex-wrap items-center justify-between gap-2 rounded-control bg-inset px-3 py-2 shadow-hairline"
                  >
                    <div className="flex min-w-0 flex-1 items-center gap-2">
                      <SpeakerAvatar person={{ id: person.id }} />
                      <div className="min-w-0">
                        <p className="break-words text-meta font-medium text-ink">{ownLabel}</p>
                        <p className="break-words text-submeta text-ink-3">
                          {t("recording.speakers_merged_into", { name: targetName })}
                        </p>
                      </div>
                    </div>
                    <Button
                      tone="secondary"
                      size="sm"
                      disabled={busy}
                      aria-label={t("recording.speakers_unmerge_aria", { speaker: ownLabel })}
                      onClick={() => handleUnmerge(person)}
                    >
                      {t("recording.speakers_unmerge")}
                    </Button>
                  </li>
                );
              })}
            </ul>
          </section>
        )}
      </div>
    </Dialog>
  );
}

function SpeakerNamingRow({
  person,
  speakers,
  rec,
  debates,
  busy,
  onName,
  onJumpSample,
}: {
  person: SpeakerPerson;
  speakers: DetailSpeakers;
  rec: RecordingView;
  debates: ReturnType<typeof minutesDebates>;
  busy: boolean;
  onName: (person: SpeakerPerson, nextName: string, me?: boolean) => void;
  onJumpSample: (lineId: string) => void;
}) {
  const formatNum = (n: number) => t("recording.speaker_n", { n });
  const displayName = speakerLabel(person, formatNum);
  const committed = person.name ?? "";
  const [draft, setDraft] = useState(committed);
  const [syncedName, setSyncedName] = useState(committed);
  if (syncedName !== committed) {
    setSyncedName(committed);
    setDraft(committed);
  }

  const trimmed = draft.trim();
  const tooLong = runeLength(trimmed) > MAX_SPEAKER_NAME_RUNES;
  const dirty = trimmed !== committed;
  const candidates = useMemo(() => speakerCandidateNames(person, speakers), [person, speakers]);
  const samples = useMemo(
    () => (speakers.trimmed || rec.trimmedAt ? [] : speakerSampleLines(person, rec.lines)),
    [speakers.trimmed, rec.trimmedAt, person, rec.lines],
  );
  const positions = useMemo(
    () => speakerPositions(debates, speakers.people, person.id, speakers.quality),
    [debates, speakers.people, person.id, speakers.quality],
  );
  const stats = formatSpeakerStats(person.seconds, person.lines);
  const errId = useId();

  const submit = () => {
    if (busy || tooLong || !dirty) return;
    onName(person, trimmed, trimmed ? !!person.me : false);
  };

  return (
    <article
      data-speaker-row={person.id}
      className="rounded-card bg-inset p-3 shadow-hairline"
    >
      <div className="flex items-start gap-2.5">
        <SpeakerAvatar person={person} size="lg" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <h3 className="min-w-0 break-words text-meta font-semibold text-ink">{displayName}</h3>
            {person.me && <Pill>{t("recording.speakers_me")}</Pill>}
          </div>
          {stats && <p className="mt-0.5 break-words text-submeta text-ink-3 tabular-nums">{stats}</p>}
        </div>
      </div>

      <div className="mt-2.5 flex items-center gap-1.5">
        <input
          type="text"
          data-speaker-input={person.id}
          className={cx(inputClass, "min-w-0 flex-1 bg-surface")}
          value={draft}
          disabled={busy}
          aria-label={t("recording.speakers_name_label", { speaker: displayName })}
          aria-invalid={tooLong || undefined}
          aria-describedby={tooLong ? errId : undefined}
          placeholder={t("recording.speakers_name_placeholder")}
          onChange={(e) => setDraft(e.target.value.replace(/[\r\n]+/g, " "))}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !composing(e.nativeEvent)) {
              e.preventDefault();
              submit();
            }
          }}
        />
        {dirty && (
          <Button tone="primary" size="sm" disabled={busy || tooLong} onClick={submit}>
            {t("common.save")}
          </Button>
        )}
        {!dirty && committed && (
          <IconButton
            icon="x"
            label={t("recording.speakers_name_clear")}
            disabled={busy}
            onClick={() => onName(person, "", false)}
          />
        )}
      </div>

      {tooLong && (
        <p id={errId} role="alert" className="mt-1 text-submeta text-red">
          {t("recording.speakers_name_too_long")}
        </p>
      )}

      {(!person.me || candidates.length > 0) && (
        <div data-speaker-candidates className="mt-2 flex flex-wrap gap-1.5">
          {!person.me && (
            <button
              type="button"
              data-speaker-candidate-me
              disabled={busy}
              onClick={() => onName(person, "", true)}
              className="inline-flex h-7 max-w-full items-center whitespace-nowrap rounded-full bg-surface px-2.5 text-label font-medium text-ink shadow-hairline transition-colors hover:bg-hover disabled:opacity-45"
            >
              {t("recording.speakers_me")}
            </button>
          )}
          {candidates.map((cand) => (
            <button
              key={cand}
              type="button"
              data-speaker-candidate={cand}
              disabled={busy}
              onClick={() => onName(person, cand, false)}
              className="inline-flex h-7 max-w-full items-center truncate whitespace-nowrap rounded-full bg-surface px-2.5 text-label font-medium text-ink shadow-hairline transition-colors hover:bg-hover disabled:opacity-45"
            >
              {cand}
            </button>
          ))}
        </div>
      )}

      {samples.length > 0 && (
        <div data-speaker-samples className="mt-2.5 border-t border-line pt-2">
          <p className="mb-1 text-submeta font-medium text-ink-3">{t("recording.speakers_samples")}</p>
          <ul className="space-y-1">
            {samples.map((sample) => (
              <li key={sample.id}>
                <button
                  type="button"
                  data-speaker-sample={sample.id}
                  data-multiline-row
                  onClick={() => onJumpSample(sample.id)}
                  className="flex min-h-10 w-full items-baseline gap-2 rounded-control px-2 py-1.5 text-left text-label leading-relaxed text-ink transition-colors hover:bg-hover"
                >
                  <span className="shrink-0 text-submeta text-accent-ink tabular-nums">{sample.clock}</span>
                  <span className="min-w-0 flex-1 break-words text-ink-2">{sample.text}</span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      {positions.length > 0 && (
        <div data-speaker-positions className="mt-2.5 border-t border-line pt-2">
          <p className="mb-1 text-submeta font-medium text-ink-3">{t("recording.speakers_positions")}</p>
          <ul className="space-y-1">
            {positions.map((pos, i) => (
              <li key={i} className="text-label leading-relaxed text-ink-2 break-words">
                {pos.stance}
              </li>
            ))}
          </ul>
        </div>
      )}
    </article>
  );
}

/** Single-step destructive confirmation before clearing a meeting's speaker timeline and names. */
export function DeleteSpeakersConfirmDialog({
  open,
  busy,
  onClose,
  onConfirm,
}: {
  open: boolean;
  busy: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  const descId = useId();
  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      title={t("recording.speakers_delete_title")}
      describedBy={descId}
      width={400}
      footer={
        <>
          <Button tone="quiet" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="danger" busy={busy} onClick={onConfirm}>
            {t("recording.speakers_delete_short")}
          </Button>
        </>
      }
    >
      <p id={descId} className="text-meta leading-relaxed text-ink-2">
        {t("recording.speakers_delete_body")}
      </p>
    </Dialog>
  );
}
