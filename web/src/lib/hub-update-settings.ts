// Hub settings loads its reads and presentation on demand. The first screen needs only the Hub notice.
import { t } from "../i18n";
import { api } from "./api";
import { workingUpdateTitle, type HubUpdateBackup, type HubUpdateRecord, type HubUpdateStatus } from "./hub-update";

export const checkHubUpdate = (preview = false) => api<HubUpdateStatus>("/hub/update/check", { method: "POST", json: preview ? { preview: true } : undefined });
export const startLatestHubUpdate = () => api<HubUpdateStatus>("/hub/update", { method: "POST", json: { target: "latest" } });
export const cancelHubUpdate = () => api<HubUpdateStatus>("/hub/update/cancel", { method: "POST" });
export const hubUpdateHistory = () => api<{ history: HubUpdateRecord[] | null }>("/hub/update/history").then(r => r.history ?? []);
export const hubUpdateBackups = () => api<{ backups: HubUpdateBackup[] }>("/hub/update/backups").then(r => r.backups);
export const applyHubUpdateBackup = (id: string) => api<HubUpdateStatus>(`/hub/update/backups/${encodeURIComponent(id)}/apply`, { method: "POST" });

/** An update is under way (the progress row, no buttons). */
export function updateWorking(s: Pick<HubUpdateStatus, "state">): boolean {
  return ["resolving", "building", "applying_backup", "backing_up", "downloading", "waiting_idle", "installing", "restarting", "confirming"].includes(s.state);
}

/** A state that ends an update: the history has a new line. */
export function updateFinished(prev: string | undefined, next: string): boolean {
  return prev !== next && (["updated", "rolled_back", "failed"].includes(next) ||
    (next === "idle" && prev !== undefined && updateWorking({ state: prev })));
}

export function updateStateTitle(s: HubUpdateStatus): string {
  const working = workingUpdateTitle(s);
  if (working) return working;
  switch (s.state) {
    case "checking":
      return t("lib-hub-update.state_checking");
    case "updated":
    case "rolled_back":
    case "failed":
      return recordTitle(s.state);
    case "available":
      return t("lib-hub-update.state_available");
    default:
      return t("lib-hub-update.state_current");
  }
}

export const DOCKER_COMMAND = "docker compose pull && docker compose up -d";

/** The channel of the currently running hub binary (`web-dev-*` builds report channel "release" on the wire). */
export function runningChannelOf(s: Pick<HubUpdateStatus, "version" | "channel">): string {
  return s.version.startsWith("web-dev-") ? "web-dev" : s.channel;
}

/** Whether checking the active update source can run on the hub (Docker checks registry tags even without self-update). */
export function canCheckHubUpdate(s: Pick<HubUpdateStatus, "can_update" | "channel">): boolean {
  return s.can_update || s.channel === "docker";
}

/** Whether switching Update source should immediately run a read-only check to populate the target source's status. */
export function shouldAutoCheckAfterSourceSwitch(s: Pick<HubUpdateStatus, "can_update" | "latest" | "state" | "source" | "supports_preview">): boolean {
  if (!s.can_update || s.latest || s.state === "checking" || updateWorking(s)) return false;
  if (s.source === "web-dev" && s.supports_preview === false) return false;
  return true;
}

export function channelTitle(channel: string): string {
  if (channel === "web-dev") return t("settings-hubsettings.web_dev");
  if (channel === "beta") return t("settings-hubsettings.beta_channel");
  return channel === "release" ? t("lib-hub-update.channel_release") : channel === "docker" ? "Docker" : t("lib-hub-update.channel_dev");
}

export function recordTitle(result: string): string {
  if (result === "cancelled") return t("settings-hubsettings.cancelled");
  return t(result === "updated" ? "lib-hub-update.result_updated" : result === "rolled_back" ? "lib-hub-update.result_rolled_back" : result === "held" ? "lib-hub-update.result_held" : "lib-hub-update.result_failed");
}

export function recordTone(result: string): "green" | "orange" | "red" | "neutral" {
  if (result === "cancelled") return "neutral";
  return result === "updated" ? "green" : result === "rolled_back" || result === "held" ? "orange" : "red";
}

/** What the "Update" group shows for the running build and the selected source. */
export type UpdateView =
  | { kind: "docker" }
  | { kind: "cannot"; why: string }
  | { kind: "newer"; working: boolean }
  | { kind: "working" }
  | { kind: "source_behind"; warning: string }
  | { kind: "ahead" }
  | { kind: "unchecked" }
  | { kind: "current" };

export function updateView(s: HubUpdateStatus): UpdateView {
  if (s.channel === "docker") return { kind: "docker" };
  const working = updateWorking(s);
  if (s.can_update && s.latest?.newer) return { kind: "newer", working };
  if (working) return { kind: "working" };
  if (s.source !== "web-dev") {
    const warning = sourceBehindWarning(s);
    if (warning) return { kind: "source_behind", warning };
    if (s.latest && s.latest.build > 0 && s.build > s.latest.build) return { kind: "ahead" };
  }
  if (!s.can_update) return { kind: "cannot", why: s.why_not ? t("lib-hub-update.cannot_update_why", { why: s.why_not }) : t("lib-hub-update.cannot_update") };
  if (!s.last_check && !s.latest) return { kind: "unchecked" };
  return { kind: "current" };
}

/** Structured explanation shown in the Update card when an update cannot proceed now. */
export interface UpdateGuidance {
  tone: "neutral" | "caution";
  title: string;
  body: string;
  nextStep?: string;
}

export function updateGuidance(s: HubUpdateStatus, view: UpdateView = updateView(s)): UpdateGuidance | null {
  if (view.kind === "ahead") {
    const webDev = runningChannelOf(s) === "web-dev";
    return {
      tone: "neutral",
      title: t(webDev ? "settings-hubsettings.ahead_web_dev_title" : "settings-hubsettings.ahead_title"),
      body: t(webDev ? "settings-hubsettings.ahead_web_dev_body" : "settings-hubsettings.ahead_of_source"),
      nextStep: t("settings-hubsettings.ahead_next_step"),
    };
  }
  if (view.kind === "cannot") {
    return {
      tone: "caution",
      title: t("settings-hubsettings.cannot_update_title"),
      body: s.why_not || view.why,
      nextStep: cannotUpdateNextStep(s),
    };
  }
  if (view.kind === "source_behind") {
    return {
      tone: "caution",
      title: t("settings-hubsettings.source_behind_title"),
      body: view.warning,
    };
  }
  return null;
}

function cannotUpdateNextStep(s: Pick<HubUpdateStatus, "channel" | "source" | "development">): string {
  if (s.source === "web-dev") return t("settings-hubsettings.cannot_next_step_web_dev");
  const canSwitch = Boolean(s.development?.configured);
  if (s.channel === "dev") {
    return t(canSwitch ? "settings-hubsettings.cannot_next_step_dev_or_switch" : "settings-hubsettings.cannot_next_step_dev");
  }
  return t(canSwitch ? "settings-hubsettings.cannot_next_step_source_or_switch" : "settings-hubsettings.cannot_next_step_config");
}

/** The warning under a newer release: it went back before, or auto-update holds it back. */
export function latestWarning(s: HubUpdateStatus): string | null {
  const l = s.latest;
  if (!l?.newer) return null;
  if (l.bad) return t("lib-hub-update.warning_bad");
  if (l.held && s.auto) return t("lib-hub-update.warning_held", { why: l.held });
  return null;
}

/** The line in place of "Up to date" when the update source looks stopped (`source_behind`, docs/operations/hub-update.md
 *  "Update source"): it names the source and where it came from, and hub.json's update.url, which sets another. */
export function sourceBehindWarning(s: HubUpdateStatus): string | null {
  if (!s.source_behind || !s.latest || !s.update_url) return null;
  const from = t(s.update_url_from === "hub.json" ? "lib-hub-update.source_from_config" : "lib-hub-update.source_from_build");
  return t("lib-hub-update.warning_source_behind", { url: s.update_url, from, version: s.latest.version });
}

/** The lines for a Docker hub: what to say and what to copy. */
export function dockerSteps(s: HubUpdateStatus): { headline?: string; steps: { text: string; copy: string }[] } {
  const l = s.latest;
  if (l?.newer) {
    if (s.image?.pinned) {
      return {
        headline: t("lib-hub-update.new_image", { version: l.version }),
        steps: [
          { text: t("lib-hub-update.docker_set_image"), copy: `${s.image.name}:${l.version}` },
          { text: t("lib-hub-update.docker_then_run"), copy: DOCKER_COMMAND },
        ],
      };
    }
    return { headline: t("lib-hub-update.new_image", { version: l.version }), steps: [{ text: t("lib-hub-update.docker_run"), copy: DOCKER_COMMAND }] };
  }
  return { steps: [{ text: t("lib-hub-update.docker_how"), copy: DOCKER_COMMAND }] };
}
