// Progressive-disclosure attention & display preferences sub-panel inside the todos_v2 Labs card (#16111).
// Renders only when todos_v2 === true; writes immediately via PUT /api/v1/assistant and refreshes to-dos.
import { useEffect, useId, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import {
  getTodosV2State,
  isValidTodosV2QuietHours,
  parseTodosV2QuietHours,
  resolveTodosV2AttentionPrefs,
  saveTodosV2AttentionPrefs,
  subscribeTodosV2State,
  TODOS_V2_AUTO_SEEN_OPTIONS,
  TODOS_V2_CAN_WAIT_SLA_OPTIONS,
  TODOS_V2_DIGEST_WINDOW_OPTIONS,
  TODOS_V2_PENDING_REVIEW_SLA_OPTIONS,
} from "../../lib/labs-todos-v2";
import { sameManager, shownManager } from "../../lib/manager-choice";
import { useStore } from "../../lib/store";
import type {
  TodosV2AttentionPrefs as TodosV2AttentionPrefsPatch,
  TodosV2AutoSeen,
  TodosV2CanWaitSLAHours,
  TodosV2DigestWindowMinutes,
  TodosV2PendingReviewSLAHours,
} from "../../lib/types";
import { Button, Spinner } from "../../ui/atoms";
import { Segmented } from "../assistant/parts";
import { Switch } from "./SettingsParts";

function autoSeenLabel(v: TodosV2AutoSeen): string {
  switch (v) {
    case "off":
      return t("settings-labs.todos_v2_auto_seen_off");
    case "subtasks":
      return t("settings-labs.todos_v2_auto_seen_subtasks");
    case "all_autonomous":
      return t("settings-labs.todos_v2_auto_seen_all_autonomous");
  }
}

function pendingReviewLabel(h: TodosV2PendingReviewSLAHours): string {
  return h === 0
    ? t("settings-labs.todos_v2_pending_review_sla_off")
    : t("settings-labs.todos_v2_hours", { hours: h });
}

function canWaitLabel(h: TodosV2CanWaitSLAHours): string {
  return h === 0
    ? t("settings-labs.todos_v2_can_wait_sla_off")
    : t("settings-labs.todos_v2_hours", { hours: h });
}

function digestWindowLabel(m: TodosV2DigestWindowMinutes): string {
  return m === 0
    ? t("settings-labs.todos_v2_digest_window_off")
    : t("settings-labs.todos_v2_minutes", { minutes: m });
}

export function TodosV2AttentionPrefs({ manager }: { manager: string }) {
  const snapshot = useSyncExternalStore(
    subscribeTodosV2State,
    () => getTodosV2State(manager),
    () => null,
  );
  const storeAssistant = useStore((s) => s.assistant);
  const fallbackMatches = storeAssistant !== null && sameManager(storeAssistant.manager ?? shownManager(), manager);
  const enabled = snapshot ? snapshot.enabled : fallbackMatches && storeAssistant.todos_v2 === true;
  const prefs = snapshot ? snapshot.prefs : resolveTodosV2AttentionPrefs(fallbackMatches ? storeAssistant : null);

  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const parsedQuiet = parseTodosV2QuietHours(prefs.todos_v2_quiet_hours);
  const [quietStart, setQuietStart] = useState(parsedQuiet?.start ?? "");
  const [quietEnd, setQuietEnd] = useState(parsedQuiet?.end ?? "");
  const [quietValidationError, setQuietValidationError] = useState<string | null>(null);
  const activeManager = useRef(manager);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    activeManager.current = manager;
    setSaving(false);
    setSaved(false);
    setError(null);
    setQuietValidationError(null);
  }, [manager]);

  useEffect(() => {
    const parsed = parseTodosV2QuietHours(prefs.todos_v2_quiet_hours);
    setQuietStart(parsed?.start ?? "");
    setQuietEnd(parsed?.end ?? "");
    setQuietValidationError(null);
  }, [prefs.todos_v2_quiet_hours]);

  const autoSeenHintId = useId();
  const pendingReviewHintId = useId();
  const canWaitHintId = useId();
  const quietHoursHintId = useId();
  const digestWindowHintId = useId();
  const foldHintId = useId();

  if (!enabled) return null;

  const commit = async (patch: TodosV2AttentionPrefsPatch) => {
    if (saving) return;
    const target = manager;
    setSaving(true);
    setSaved(false);
    setError(null);
    try {
      await saveTodosV2AttentionPrefs(target, patch);
      if (!mounted.current || activeManager.current !== target) return;
      setSaved(true);
    } catch (e) {
      if (!mounted.current || activeManager.current !== target) return;
      setError(errorText(e));
    } finally {
      if (mounted.current && activeManager.current === target) {
        setSaving(false);
      }
    }
  };

  return (
    <div data-todos-v2-attention-prefs="" className="bg-inset px-4 py-3">
      <div className="mb-1 flex items-center justify-between gap-2">
        <h3 className="text-label font-medium text-ink-2">
          {t("settings-labs.todos_v2_prefs_title")}
        </h3>
        <span aria-live="polite" className={saving || saved ? "flex items-center gap-1.5 text-submeta leading-snug" : "sr-only"}>
          {saving && (
            <>
              <Spinner />
              <span className="text-ink-3">{t("settings-configuration.saving")}</span>
            </>
          )}
          {saved && !saving && (
            <span className="text-green">{t("settings-configuration.saved")}</span>
          )}
        </span>
      </div>

      {error && (
        <p role="alert" className="mb-2 text-submeta leading-snug text-red">
          {error}
        </p>
      )}

      <div className="divide-y divide-line">
        <div
          data-todos-v2-pref="todos_v2_auto_seen"
          className="flex flex-col gap-x-3 gap-y-2 py-2.5 sm:flex-row sm:items-center"
        >
          <div className="min-w-0 flex-1">
            <div className="text-meta text-ink">{t("settings-labs.todos_v2_auto_seen_label")}</div>
            <p id={autoSeenHintId} className="mt-0.5 text-submeta leading-snug text-ink-3">
              {t("settings-labs.todos_v2_auto_seen_hint")}
            </p>
          </div>
          <fieldset disabled={saving} aria-describedby={autoSeenHintId} className="shrink-0">
            <Segmented<TodosV2AutoSeen>
              compact
              wrap
              label={t("settings-labs.todos_v2_auto_seen_label")}
              value={prefs.todos_v2_auto_seen}
              options={TODOS_V2_AUTO_SEEN_OPTIONS.map((v) => ({ v, label: autoSeenLabel(v) }))}
              onChange={(v) => {
                if (v !== prefs.todos_v2_auto_seen) void commit({ todos_v2_auto_seen: v });
              }}
            />
          </fieldset>
        </div>

        <div
          data-todos-v2-pref="todos_v2_pending_review_sla_hours"
          className="flex flex-col gap-x-3 gap-y-2 py-2.5 sm:flex-row sm:items-center"
        >
          <div className="min-w-0 flex-1">
            <div className="text-meta text-ink">{t("settings-labs.todos_v2_pending_review_sla_label")}</div>
            <p id={pendingReviewHintId} className="mt-0.5 text-submeta leading-snug text-ink-3">
              {t("settings-labs.todos_v2_pending_review_sla_hint")}
            </p>
          </div>
          <fieldset disabled={saving} aria-describedby={pendingReviewHintId} className="shrink-0">
            <Segmented<string>
              compact
              wrap
              label={t("settings-labs.todos_v2_pending_review_sla_label")}
              value={String(prefs.todos_v2_pending_review_sla_hours)}
              options={TODOS_V2_PENDING_REVIEW_SLA_OPTIONS.map((h) => ({ v: String(h), label: pendingReviewLabel(h) }))}
              onChange={(v) => {
                const next = Number(v) as TodosV2PendingReviewSLAHours;
                if (next !== prefs.todos_v2_pending_review_sla_hours) {
                  void commit({ todos_v2_pending_review_sla_hours: next });
                }
              }}
            />
          </fieldset>
        </div>

        <div
          data-todos-v2-pref="todos_v2_can_wait_sla_hours"
          className="flex flex-col gap-x-3 gap-y-2 py-2.5 sm:flex-row sm:items-center"
        >
          <div className="min-w-0 flex-1">
            <div className="text-meta text-ink">{t("settings-labs.todos_v2_can_wait_sla_label")}</div>
            <p id={canWaitHintId} className="mt-0.5 text-submeta leading-snug text-ink-3">
              {t("settings-labs.todos_v2_can_wait_sla_hint")}
            </p>
          </div>
          <fieldset disabled={saving} aria-describedby={canWaitHintId} className="shrink-0">
            <Segmented<string>
              compact
              wrap
              label={t("settings-labs.todos_v2_can_wait_sla_label")}
              value={String(prefs.todos_v2_can_wait_sla_hours)}
              options={TODOS_V2_CAN_WAIT_SLA_OPTIONS.map((h) => ({ v: String(h), label: canWaitLabel(h) }))}
              onChange={(v) => {
                const next = Number(v) as TodosV2CanWaitSLAHours;
                if (next !== prefs.todos_v2_can_wait_sla_hours) {
                  void commit({ todos_v2_can_wait_sla_hours: next });
                }
              }}
            />
          </fieldset>
        </div>

        <div
          data-todos-v2-pref="todos_v2_quiet_hours"
          className="flex flex-col gap-x-3 gap-y-2 py-2.5 sm:flex-row sm:items-center"
        >
          <div className="min-w-0 flex-1">
            <div className="text-meta text-ink">{t("settings-labs.todos_v2_quiet_hours_label")}</div>
            <p id={quietHoursHintId} className="mt-0.5 text-submeta leading-snug text-ink-3">
              {t("settings-labs.todos_v2_quiet_hours_hint")}
            </p>
            {quietValidationError && (
              <p role="alert" data-testid="todos-v2-quiet-hours-error" className="mt-1 text-submeta leading-snug text-red">
                {quietValidationError}
              </p>
            )}
          </div>
          <div className="flex shrink-0 flex-wrap items-center gap-1.5">
            <input
              type="time"
              data-testid="todos-v2-quiet-hours-start"
              aria-label={t("settings-labs.todos_v2_quiet_hours_start")}
              aria-describedby={quietHoursHintId}
              disabled={saving}
              value={quietStart}
              onChange={(e) => {
                setQuietStart(e.target.value);
                setQuietValidationError(null);
              }}
              className="h-7 rounded-control border border-line bg-surface px-2 text-submeta text-ink tabular-nums focus-visible:outline-2 focus-visible:outline-accent disabled:opacity-50"
            />
            <span className="text-submeta text-ink-3" aria-hidden>–</span>
            <input
              type="time"
              data-testid="todos-v2-quiet-hours-end"
              aria-label={t("settings-labs.todos_v2_quiet_hours_end")}
              aria-describedby={quietHoursHintId}
              disabled={saving}
              value={quietEnd}
              onChange={(e) => {
                setQuietEnd(e.target.value);
                setQuietValidationError(null);
              }}
              className="h-7 rounded-control border border-line bg-surface px-2 text-submeta text-ink tabular-nums focus-visible:outline-2 focus-visible:outline-accent disabled:opacity-50"
            />
            <Button
              tone="secondary"
              size="sm"
              testId="todos-v2-quiet-hours-save"
              disabled={
                saving ||
                (!quietStart && !quietEnd && prefs.todos_v2_quiet_hours === "") ||
                (Boolean(quietStart && quietEnd) && `${quietStart}-${quietEnd}` === prefs.todos_v2_quiet_hours)
              }
              onClick={() => {
                const start = quietStart.trim();
                const end = quietEnd.trim();
                if (!start && !end) {
                  setQuietValidationError(null);
                  if (prefs.todos_v2_quiet_hours !== "") void commit({ todos_v2_quiet_hours: "" });
                  return;
                }
                const next = `${start}-${end}`;
                if (!isValidTodosV2QuietHours(next)) {
                  setQuietValidationError(t("settings-labs.todos_v2_quiet_hours_invalid"));
                  return;
                }
                setQuietValidationError(null);
                if (next !== prefs.todos_v2_quiet_hours) void commit({ todos_v2_quiet_hours: next });
              }}
              className="h-7 px-2.5 text-submeta whitespace-nowrap"
            >
              {t("settings-labs.todos_v2_quiet_hours_save")}
            </Button>
            {(prefs.todos_v2_quiet_hours !== "" || quietStart !== "" || quietEnd !== "") && (
              <Button
                tone="quiet"
                size="sm"
                testId="todos-v2-quiet-hours-clear"
                disabled={saving}
                onClick={() => {
                  setQuietStart("");
                  setQuietEnd("");
                  setQuietValidationError(null);
                  if (prefs.todos_v2_quiet_hours !== "") void commit({ todos_v2_quiet_hours: "" });
                }}
                className="h-7 px-2 text-submeta whitespace-nowrap"
              >
                {t("settings-labs.todos_v2_quiet_hours_clear")}
              </Button>
            )}
          </div>
        </div>

        <div
          data-todos-v2-pref="todos_v2_digest_window_minutes"
          className="flex flex-col gap-x-3 gap-y-2 py-2.5 sm:flex-row sm:items-center"
        >
          <div className="min-w-0 flex-1">
            <div className="text-meta text-ink">{t("settings-labs.todos_v2_digest_window_label")}</div>
            <p id={digestWindowHintId} className="mt-0.5 text-submeta leading-snug text-ink-3">
              {t("settings-labs.todos_v2_digest_window_hint")}
            </p>
          </div>
          <fieldset disabled={saving} aria-describedby={digestWindowHintId} className="shrink-0">
            <Segmented<string>
              compact
              wrap
              label={t("settings-labs.todos_v2_digest_window_label")}
              value={String(prefs.todos_v2_digest_window_minutes)}
              options={TODOS_V2_DIGEST_WINDOW_OPTIONS.map((m) => ({ v: String(m), label: digestWindowLabel(m) }))}
              onChange={(v) => {
                const next = Number(v) as TodosV2DigestWindowMinutes;
                if (next !== prefs.todos_v2_digest_window_minutes) {
                  void commit({ todos_v2_digest_window_minutes: next });
                }
              }}
            />
          </fieldset>
        </div>

        <div
          data-todos-v2-pref="todos_v2_fold_children"
          className="flex items-center justify-between gap-3 pt-2.5"
        >
          <div className="min-w-0 flex-1">
            <div className="text-meta text-ink">{t("settings-labs.todos_v2_fold_children_label")}</div>
            <p id={foldHintId} className="mt-0.5 text-submeta leading-snug text-ink-3">
              {t("settings-labs.todos_v2_fold_children_hint")}
            </p>
          </div>
          <Switch
            on={prefs.todos_v2_fold_children}
            onChange={(on) => {
              if (on !== prefs.todos_v2_fold_children) void commit({ todos_v2_fold_children: on });
            }}
            disabled={saving}
            label={t("settings-labs.todos_v2_fold_children_label")}
            describedBy={foldHintId}
          />
        </div>
      </div>
    </div>
  );
}
