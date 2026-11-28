// Settings › hub (docs/operations/hub-update.md "In the app", the app's HubUpdateModel / HubUpdateStatus): the hub's version and channel,
// checking for and installing a release, what an update waits for, auto-update, the history; and the assistant's
// skills (GET /api/v1/skills). Pure helpers here; the page is views/settings/HubSettings.tsx.
import { t } from "../i18n";
import { MANAGER_HEADER } from "./manager-choice";
import { api } from "./api";

export interface BusyItem {
  machine_id: string;
  machine: string;
  session_id?: string;
  title: string;
}

export interface HubDevelopmentProgress {
  phase: "building" | "backing_up" | "applying_backup";
  step: string;
  step_index: number;
  step_total: number;
  steps?: string[];
  elapsed_seconds: number;
  item_label?: string;
  item_done?: number;
  item_total?: number;
  bytes_done?: number;
  bytes_total?: number;
  bytes_per_second?: number;
  percent?: number;
}

export interface HubUpdateStatus {
  can_cancel?: boolean;
  cancelling?: boolean;
  supports_preview?: boolean;
  source?: "release" | "beta" | "web-dev";
  development?: {
    configured: boolean;
    /** Why it cannot be selected (localized by the hub). */
    why?: string;
    branch: string;
    current_commit?: string;
    current_updated_at?: string;
    /** The snapshot folder in effect, the recommended one, and how many are kept. */
    backup_directory?: string;
    backup_default_directory?: string;
    backup_directory_is_default?: boolean;
    backup_retain?: number;
    /** The hub pauses its own writers instead of running hub.json hooks. */
    builtin_quiesce?: boolean;
  };
  backup?: { id: string; retained: number; pruned: string[] | null };
  version: string;
  build: number;
  channel: string; // release | beta | docker | dev
  can_update: boolean;
  why_not?: string;
  latest?: {
    version: string;
    build: number;
    released?: string;
    notes?: string;
    newer: boolean;
    /** Went back before: not installed automatically. */
    bad: boolean;
    /** Why auto-update does not install this newer one. */
    held?: string;
    commit?: string;
    updated_at?: string;
    branch?: string;
    backup_id?: string;
  };
  state: string;
  progress?: string;
  development_progress?: HubDevelopmentProgress;
  busy: BusyItem[];
  error?: string;
  auto: boolean;
  auto_update_nodes?: boolean;
  auto_notice: boolean;
  last_check?: string;
  confirmed: boolean;
  image?: { name: string; tag: string; pinned: boolean };
  /** Where the hub reads its releases now, and whether hub.json's update.url or the build says so. */
  update_url?: string;
  update_url_from?: string; // hub.json | build
  /** The source's newest release (`latest`) is older than this hub and the release its code contains: the source
   *  looks stopped (#8108). */
  source_behind?: boolean;
}

export interface HubUpdateRecord {
  source?: string;
  commit?: string;
  backup?: HubUpdateStatus["backup"];
  from_version: string;
  to_version: string;
  to_build: number;
  started?: string;
  ended?: string;
  result: string; // updated | rolled_back | failed | cancelled | held
  reason?: string;
  auto: boolean;
}

export interface Skill {
  name: string;
  description?: string;
  source: string; // user | builtin
}

export const hubUpdate = () => api<HubUpdateStatus>("/hub/update");
/** `now`: don't wait for the assistant and the agents to be idle. */
export const startHubUpdate = (now: boolean) => api<HubUpdateStatus>("/hub/update", { method: "POST", json: now ? { now: true } : {} });
export const setHubUpdateSettings = (s: {
  auto?: boolean;
  auto_notice_seen?: boolean;
  auto_update_nodes?: boolean;
  source?: "release" | "beta" | "web-dev";
  /** Declarative snapshot settings; "" restores the recommended folder. */
  development_backup_directory?: string;
  development_backup_retain?: number;
}) => api<HubUpdateStatus>("/hub/update/settings", { method: "PUT", json: s });
export interface HubUpdateBackup {
  id: string;
  version: string;
  build: number;
  commit: string;
  updated_at: string;
  created_at: string;
}
export const skills = (manager?: string) => api<{ machine_id: string; machine?: string; skills: Skill[] | null }>("/skills", { headers: manager ? { [MANAGER_HEADER]: manager } : undefined });

/** Titles shared by the sidebar notice and the Settings progress row. */
export function workingUpdateTitle(s: HubUpdateStatus): string | undefined {
  switch (s.state) {
    case "resolving":
      return t("lib-hub-update.state_checking");
    case "applying_backup":
    case "building":
    case "backing_up":
    case "downloading":
    case "confirming":
      return t(`lib-hub-update.state_${s.state}`);
    case "waiting_idle":
      return s.busy.length ? t("lib-hub-update.state_waiting_busy", { count: s.busy.length }) : t("lib-hub-update.state_waiting_idle");
    case "installing":
    case "restarting":
      return t("lib-hub-update.state_restarting");
  }
}

/** The one quiet line over the Agents list about the hub (docs/operations/hub-update.md "In the app", iOS / Android `HubNotice`),
 *  the most pressing first. The app's two compatibility lines (app / hub too old) don't apply: the hub serves this
 *  page itself, so the two are always the same version. */
export type HubNotice =
  | { kind: "updating"; what: string }
  | { kind: "rolled_back"; why: string; build: number }
  | { kind: "auto_on" }
  | { kind: "available"; version: string; build: number }
  | { kind: "new_image"; version: string };

/** What closing the line remembers, so this one does not come back (null: it cannot be closed). Same keys as the app's. */
export function hubNoticeKey(n: HubNotice): string | null {
  switch (n.kind) {
    case "rolled_back":
      return `rolledBack:${n.build}`;
    case "available":
      return `build:${n.build}`;
    case "new_image":
      return `image:${n.version}`;
    default:
      return null;
  }
}

/** Every line that applies, the most pressing first. */
export function hubNoticeCandidates(s: HubUpdateStatus | null | undefined): HubNotice[] {
  if (!s) return [];
  const out: HubNotice[] = [];
  const workingTitle = workingUpdateTitle(s);
  if (workingTitle) out.push({ kind: "updating", what: workingTitle });
  if (s.state === "rolled_back" && s.error && s.latest?.build != null) out.push({ kind: "rolled_back", why: s.error, build: s.latest.build });
  if (s.auto_notice) out.push({ kind: "auto_on" });
  const l = s.latest;
  if (l?.newer) {
    if (s.channel === "docker") out.push({ kind: "new_image", version: l.version });
    // held: auto-update will not install it by itself either
    else if (s.can_update && (!s.auto || l.held) && !l.bad) out.push({ kind: "available", version: l.version, build: l.build });
  }
  return out;
}

/** The line to show, past the ones the user closed (`dismissed`: their `hubNoticeKey`s). */
export function hubNotice(s: HubUpdateStatus | null | undefined, dismissed: readonly string[] = []): HubNotice | null {
  return hubNoticeCandidates(s).find((n) => {
    const key = hubNoticeKey(n);
    return key === null || !dismissed.includes(key);
  }) ?? null;
}

export { hubNoticeText } from "./hub-notice-text";

const DISMISSED_KEY = "persona.hubNotice.dismissed";

/** The lines the user closed, the last few (like the app's `hubNotice.dismissed`). */
export function dismissedHubNotices(store: Pick<Storage, "getItem"> = localStorage): string[] {
  try {
    const v: unknown = JSON.parse(store.getItem(DISMISSED_KEY) ?? "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

/** Remembers `key` as closed and returns the new list (the last 8). */
export function dismissHubNotice(key: string, store: Pick<Storage, "getItem" | "setItem"> = localStorage): string[] {
  const next = [...dismissedHubNotices(store).filter((k) => k !== key), key].slice(-8);
  store.setItem(DISMISSED_KEY, JSON.stringify(next));
  return next;
}
