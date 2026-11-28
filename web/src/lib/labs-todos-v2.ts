// Adapt the selected Assistant's todos_v2 Labs setting and attention preferences to the Labs form (#14538, #14584, #16111).
import { t } from "../i18n";
import { api, ApiError, onCredentialChange } from "./api";
import { MANAGER_HEADER, sameManager, shownManager } from "./manager-choice";
import { patchAssistant, refreshTodos } from "./store";
import { checkUpdate, CONFIGURATION_VERSION, ConfigurationError, type ConfigurationDocument, type ConfigurationSource } from "./configuration/contract";
import type {
  TodosV2AttentionPrefs,
  TodosV2AutoSeen,
  TodosV2CanWaitSLAHours,
  TodosV2DigestWindowMinutes,
  TodosV2PendingReviewSLAHours,
} from "./types";

const field = "todos_v2";
type Settings = {
  manager?: string;
  todos_v2?: boolean;
} & TodosV2AttentionPrefs;

export const TODOS_V2_AUTO_SEEN_OPTIONS: readonly TodosV2AutoSeen[] = ["off", "subtasks", "all_autonomous"];
export const TODOS_V2_PENDING_REVIEW_SLA_OPTIONS: readonly TodosV2PendingReviewSLAHours[] = [0, 12, 24, 48];
export const TODOS_V2_CAN_WAIT_SLA_OPTIONS: readonly TodosV2CanWaitSLAHours[] = [0, 24, 48, 72];
export const TODOS_V2_DIGEST_WINDOW_OPTIONS: readonly TodosV2DigestWindowMinutes[] = [0, 30, 60, 120];

const QUIET_HOURS_RE = /^([01]\d|2[0-3]):([0-5]\d)-([01]\d|2[0-3]):([0-5]\d)$/;

/** Validate strict 24-hour quiet-hours range ("HH:MM-HH:MM" with distinct start and end) or empty string (off). */
export function isValidTodosV2QuietHours(raw: string): boolean {
  if (raw === "") return true;
  const m = QUIET_HOURS_RE.exec(raw);
  if (!m) return false;
  return `${m[1]}:${m[2]}` !== `${m[3]}:${m[4]}`;
}

/** Parse a non-empty valid quiet-hours string into its start and end HH:MM values; returns null when off/invalid. */
export function parseTodosV2QuietHours(raw: string): { start: string; end: string } | null {
  if (!raw || !isValidTodosV2QuietHours(raw)) return null;
  const m = QUIET_HOURS_RE.exec(raw);
  if (!m) return null;
  return { start: `${m[1]}:${m[2]}`, end: `${m[3]}:${m[4]}` };
}

export interface ResolvedTodosV2AttentionPrefs {
  todos_v2_auto_seen: TodosV2AutoSeen;
  todos_v2_pending_review_sla_hours: TodosV2PendingReviewSLAHours;
  todos_v2_can_wait_sla_hours: TodosV2CanWaitSLAHours;
  todos_v2_fold_children: boolean;
  todos_v2_quiet_hours: string;
  todos_v2_digest_window_minutes: TodosV2DigestWindowMinutes;
}

export const DEFAULT_TODOS_V2_ATTENTION_PREFS: ResolvedTodosV2AttentionPrefs = {
  todos_v2_auto_seen: "all_autonomous",
  todos_v2_pending_review_sla_hours: 24,
  todos_v2_can_wait_sla_hours: 48,
  todos_v2_fold_children: true,
  todos_v2_quiet_hours: "",
  todos_v2_digest_window_minutes: 0,
};

export function resolveTodosV2AttentionPrefs(raw?: TodosV2AttentionPrefs | null): ResolvedTodosV2AttentionPrefs {
  const autoSeen = raw?.todos_v2_auto_seen && (TODOS_V2_AUTO_SEEN_OPTIONS as readonly string[]).includes(raw.todos_v2_auto_seen)
    ? raw.todos_v2_auto_seen
    : DEFAULT_TODOS_V2_ATTENTION_PREFS.todos_v2_auto_seen;
  const pendingReview = raw?.todos_v2_pending_review_sla_hours !== undefined
    && (TODOS_V2_PENDING_REVIEW_SLA_OPTIONS as readonly number[]).includes(raw.todos_v2_pending_review_sla_hours)
    ? raw.todos_v2_pending_review_sla_hours
    : DEFAULT_TODOS_V2_ATTENTION_PREFS.todos_v2_pending_review_sla_hours;
  const canWait = raw?.todos_v2_can_wait_sla_hours !== undefined
    && (TODOS_V2_CAN_WAIT_SLA_OPTIONS as readonly number[]).includes(raw.todos_v2_can_wait_sla_hours)
    ? raw.todos_v2_can_wait_sla_hours
    : DEFAULT_TODOS_V2_ATTENTION_PREFS.todos_v2_can_wait_sla_hours;
  const foldChildren = typeof raw?.todos_v2_fold_children === "boolean"
    ? raw.todos_v2_fold_children
    : DEFAULT_TODOS_V2_ATTENTION_PREFS.todos_v2_fold_children;
  const quietHours = typeof raw?.todos_v2_quiet_hours === "string" && isValidTodosV2QuietHours(raw.todos_v2_quiet_hours.trim())
    ? raw.todos_v2_quiet_hours.trim()
    : DEFAULT_TODOS_V2_ATTENTION_PREFS.todos_v2_quiet_hours;
  const digestWindow = raw?.todos_v2_digest_window_minutes !== undefined
    && (TODOS_V2_DIGEST_WINDOW_OPTIONS as readonly number[]).includes(raw.todos_v2_digest_window_minutes)
    ? raw.todos_v2_digest_window_minutes
    : DEFAULT_TODOS_V2_ATTENTION_PREFS.todos_v2_digest_window_minutes;
  return {
    todos_v2_auto_seen: autoSeen,
    todos_v2_pending_review_sla_hours: pendingReview,
    todos_v2_can_wait_sla_hours: canWait,
    todos_v2_fold_children: foldChildren,
    todos_v2_quiet_hours: quietHours,
    todos_v2_digest_window_minutes: digestWindow,
  };
}

export interface TodosV2ManagerSnapshot {
  enabled: boolean;
  prefs: ResolvedTodosV2AttentionPrefs;
}

const stateByManager = new Map<string, TodosV2ManagerSnapshot>();
const listeners = new Set<() => void>();
const notify = () => listeners.forEach((l) => l());

onCredentialChange(() => {
  stateByManager.clear();
  notify();
});

function sameSnapshot(a: TodosV2ManagerSnapshot | undefined, b: TodosV2ManagerSnapshot): boolean {
  return !!a
    && a.enabled === b.enabled
    && a.prefs.todos_v2_auto_seen === b.prefs.todos_v2_auto_seen
    && a.prefs.todos_v2_pending_review_sla_hours === b.prefs.todos_v2_pending_review_sla_hours
    && a.prefs.todos_v2_can_wait_sla_hours === b.prefs.todos_v2_can_wait_sla_hours
    && a.prefs.todos_v2_fold_children === b.prefs.todos_v2_fold_children
    && a.prefs.todos_v2_quiet_hours === b.prefs.todos_v2_quiet_hours
    && a.prefs.todos_v2_digest_window_minutes === b.prefs.todos_v2_digest_window_minutes;
}

function recordState(manager: string, state: Settings, mergePrev = false) {
  const prev = stateByManager.get(manager);
  const next: TodosV2ManagerSnapshot = {
    enabled: typeof state.todos_v2 === "boolean" ? state.todos_v2 : prev?.enabled ?? false,
    prefs: resolveTodosV2AttentionPrefs(mergePrev && prev ? { ...prev.prefs, ...state } : state),
  };
  if (sameSnapshot(prev, next)) return;
  stateByManager.set(manager, next);
  notify();
}

export function getTodosV2State(manager: string): TodosV2ManagerSnapshot | null {
  return stateByManager.get(manager) ?? null;
}

export function subscribeTodosV2State(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function resetTodosV2StateForTest() {
  stateByManager.clear();
  notify();
}

export async function saveTodosV2AttentionPrefs(
  manager: string,
  patch: TodosV2AttentionPrefs,
): Promise<ResolvedTodosV2AttentionPrefs> {
  const headers = { [MANAGER_HEADER]: manager };
  const state = await api<Settings>("/assistant", {
    method: "PUT",
    headers,
    json: patch,
  });
  if (state.manager && state.manager !== manager) {
    throw new ConfigurationError("configuration_unavailable");
  }
  const prev = stateByManager.get(manager);
  const resolved = resolveTodosV2AttentionPrefs(prev ? { ...prev.prefs, ...state } : state);
  recordState(manager, state, true);
  if (sameManager(manager, shownManager())) {
    patchAssistant(resolved);
    void refreshTodos({ invalidate: patch.todos_v2_fold_children !== undefined });
  }
  return resolved;
}

function document(manager: string, state: Settings, saved = false): ConfigurationDocument {
  if (state.manager !== manager) throw new ConfigurationError("configuration_unavailable");
  const supported = typeof state.todos_v2 === "boolean";
  return {
    schema_version: CONFIGURATION_VERSION,
    id: "assistant-todos-v2",
    revision: String(state.todos_v2),
    title: t("settings-labs.work_area"),
    groups: [{ id: "todos-v2", title: t("settings-labs.trial"), fields: [{
      id: field, kind: "toggle", default: false,
      title: t("settings-labs.todos_v2_title"), description: t("settings-labs.todos_v2_hint"),
      ...(!supported && { read_only: true, disabled_reason: t("settings-labs.todos_v2_unsupported") }),
      details: [
        { title: t("settings-labs.scope"), description: t("settings-labs.todos_v2_scope") },
        { title: t("settings-labs.activation"), description: t("settings-labs.todos_v2_activation") },
        { title: t("settings-labs.rollback"), description: t("settings-labs.todos_v2_rollback") },
      ],
    }] }],
    values: { [field]: state.todos_v2 === true },
    ...(saved && { notices: { [field]: { kind: "info", message: t("settings-labs.todos_v2_saved") } } }),
  };
}

export function todosV2LabsSource(manager: string): ConfigurationSource {
  const headers = { [MANAGER_HEADER]: manager };
  return {
    key: `labs:assistant-todos-v2:${manager}`,
    read: async (signal) => {
      const state = await api<Settings>("/assistant", { headers, signal });
      const doc = document(manager, state);
      recordState(manager, state);
      return doc;
    },
    update: async (update, signal) => {
      const expected = update.revision === "true";
      const refused = checkUpdate(document(manager, { manager, todos_v2: expected }), update);
      if (refused) throw refused;
      const value = update.reset?.includes(field) ? false : update.changes?.[field];
      try {
        const state = await api<Settings>("/assistant", { method: "PUT", headers, signal,
          json: { todos_v2: value, expected_todos_v2: expected } });
        if (state.todos_v2 !== value) throw new ConfigurationError("configuration_unavailable");
        const saved = document(manager, state, true);
        recordState(manager, state);
        if (sameManager(manager, shownManager())) {
          patchAssistant({ todos_v2: state.todos_v2 === true, ...resolveTodosV2AttentionPrefs(state) });
        }
        if (state.todos_v2 !== expected) void refreshTodos({ invalidate: true });
        return saved;
      } catch (e) {
        if (e instanceof ApiError) throw ConfigurationError.fromBody(e.body, e.status);
        throw e;
      }
    },
  };
}
