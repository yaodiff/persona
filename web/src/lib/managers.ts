// Settings › Managers (docs/assistant/managers.md, docs/protocol/protocol.md "Managers"): the hub's roster of managers (each a whole assistant),
// making one (a one-time install code, and the install commands when the hub gives them), renaming, the default,
// installing one elsewhere and removing one. The hub pushes {"type":"managers"} when the roster changes to a page
// that declared the feature `managers` (CLIENT_FEATURES).
import { useSyncExternalStore } from "react";
import { t } from "../i18n";
import { api, ApiError, enc, errorText } from "./api";
import { ago } from "./format";
import { BUILTIN_MANAGER, chooseManager, chosenManager, clearConfirmedDefault, notePickedName, noteShownIdentity, onManagerChoice, setRosterDefault, settleRosterDefault, shownManager } from "./manager-choice";
import { onHubEvent } from "./store";

export interface Manager {
  id: string;
  name: string;
  online: boolean;
  default?: boolean;
  /** The assistant the hub runs itself. */
  builtin?: boolean;
  /** The built-in one only: the user asked to keep it out of the switcher while it is not used (#9055). */
  hide?: boolean;
  /** That is in effect (not the default, no computer): the switcher leaves it out. An older hub says neither. */
  hidden?: boolean;
  host?: string;
  /** Host machine's stable UID, when reported by newer managers/hubs (#14963). */
  machine_uid?: string;
  os?: string;
  arch?: string;
  /** How it runs, as it reported: "child" (the hub's own), "service", "docker", "external"… */
  deploy?: string;
  version?: string;
  /** Connected since (online only). */
  since?: string;
  /** Last connected (offline: "Last synced at..."). */
  last_seen?: string;
  /** Not installed yet: when its install code runs out. */
  installing?: string;
  /** Online on a build that reads no settings of its own (no `config.own`): it runs on built-in defaults until updated. */
  needs_update?: boolean;
  /** On-disk personad binary was replaced while this manager was busy; it will restart automatically once idle (#14963). */
  pending_restart?: boolean;
  /** Its open inbox items that need the user (the inbox page's badge). Absent: not known (offline, installing, an
   *  older hub), counted as none. The hub pushes `managers` when it changes. */
  inbox_needs_you?: number;
  /** The hub and this connected assistant support their own extension information-access settings. */
  extension_info_access_scoped?: boolean;
  /** The connected assistant advertises the read-only Integrations settings projection. */
  integrations_projection_v1?: boolean;
}

export interface ManagerList {
  managers: Manager[];
  default: string;
  /** Matching bot identities across managers, without exposing account identifiers or secrets. */
  bot_conflicts?: BotConflict[];
}

export interface BotConflict {
  platform: "feishu" | "wechat";
  managers: string[];
}

export interface InstallCode {
  code: string;
  expires_at: string;
  /** The install commands, from a hub that gives them; otherwise only the code. */
  commands?: { service?: string; docker?: string };
}

export const listManagers = () => api<ManagerList>("/managers");
export const createManager = (name: string) => api<InstallCode & { manager: Manager }>("/managers", { method: "POST", json: { name } });
export const newInstallCode = (id: string) => api<InstallCode>(`/managers/${enc(id)}/code`, { method: "POST" });
export const renameManager = (id: string, name: string) => api<{ manager: Manager }>(`/managers/${enc(id)}`, { method: "PATCH", json: { name } });
export const makeDefault = (id: string) => api<{ manager: Manager }>(`/managers/${enc(id)}`, { method: "PATCH", json: { default: true } });
/** Keeps the built-in assistant out of the switcher while it is not used, or shows it again (#9055). */
export const hideManager = (id: string, hide: boolean) => api<{ manager: Manager }>(`/managers/${enc(id)}`, { method: "PATCH", json: { hide } });
export const deleteManager = (id: string) => api<{ ok: boolean }>(`/managers/${enc(id)}`, { method: "DELETE" });

/**
 * A new install code for manager id. A never-installed one (`fresh`) whose code ran out is gone from the hub's roster
 * (`404 no_manager`, #5564): it is made again with the same name, and the answer says the new id.
 */
export async function renewCode(id: string, name: string, fresh: boolean): Promise<{ id: string; code: InstallCode }> {
  try {
    return { id, code: await newInstallCode(id) };
  } catch (e) {
    if (!fresh || !(e instanceof ApiError) || e.status !== 404 || e.code !== "no_manager") throw e;
    const made = await createManager(name);
    return { id: made.manager.id, code: made };
  }
}

/** Names are 1–40 characters (hub/managers.go managerName). */
export const NAME_MAX = 40;

export function validName(name: string): boolean {
  const n = [...name.trim()].length;
  return n > 0 && n <= NAME_MAX;
}

/** Whether a hub without the roster (from before it) answered. */
export function unsupported(e: unknown): boolean {
  return e instanceof ApiError && e.status === 404 && !e.code;
}

const OS: Record<string, string> = { darwin: "macOS", linux: "Linux", windows: "Windows" };

/** How it runs, in plain words. */
export function deployText(m: Pick<Manager, "builtin" | "deploy">): string {
  if (m.builtin || m.deploy === "child") return t("settings-managers.deploy_hub");
  switch (m.deploy) {
    case undefined:
    case "":
      return "";
    case "service":
    case "systemd":
    case "launchd":
      return t("settings-managers.deploy_service");
    case "docker":
    case "container":
      return t("settings-managers.deploy_container");
    case "external":
      return t("settings-managers.deploy_external");
    default:
      return m.deploy;
  }
}

/** Where it runs: host · OS · how. Empty for one not installed yet. */
export function whereText(m: Manager): string {
  const os = m.os ? (OS[m.os] ?? m.os) : "";
  return [m.host, os, deployText(m)].filter(Boolean).join(" · ");
}

/** Minutes the install code has left (to the nearest, at least 1 while it lasts: a fresh code says 10), 0 once it ran out. */
export function minutesLeft(until: string, now = Date.now()): number {
  const ms = Date.parse(until) - now;
  return Number.isFinite(ms) && ms > 0 ? Math.max(1, Math.round(ms / 60_000)) : 0;
}

export type Tone = "green" | "grey" | "accent";

/** The status line under the name and its dot's color. */
export function statusLine(m: Manager, now = Date.now()): { text: string; tone: Tone } {
  if (m.installing) {
    const n = minutesLeft(m.installing, now);
    return { text: n > 0 ? t("settings-managers.installing", { count: n }) : t("settings-managers.installing_expired"), tone: "accent" };
  }
  if (m.online) return { text: t("settings-managers.online"), tone: "green" };
  const when = ago(m.last_seen, now);
  return { text: when ? t("settings-managers.offline_since", { time: when }) : t("settings-managers.offline"), tone: "grey" };
}

/** Why Delete is off for it, or "" when it can be removed. */
export function deleteBlocked(m: Manager): string {
  if (m.builtin) return t("settings-managers.delete_builtin");
  if (m.default) return t("settings-managers.delete_default");
  return "";
}

/** A failed change, in plain words. */
export function managerError(e: unknown): string {
  if (e instanceof ApiError) {
    switch (e.code) {
      case "default_manager":
        return t("settings-managers.err_default");
      case "builtin_manager":
        return t("settings-managers.err_builtin");
      case "no_manager":
        return t("settings-managers.err_gone");
      case "installing":
        return t("settings-managers.err_installing");
    }
    if (e.status === 400) return t("settings-managers.err_name");
    if (e.status === 403) return t("settings-managers.err_forbidden");
  }
  return errorText(e);
}

// ── the roster the other pages go by (step 4: the switcher, grouping, push switches) ──

let roster: ManagerList | null = null;
let rosterLoad: Promise<void> | null = null;
// a read asked for while one was out (a `managers` push, a change made here): once more after it, shared by every ask
// meanwhile, as the hub may have answered the read out before the change it tells about
let rosterAgain: Promise<void> | null = null;
let rosterReset = 0; // resetRoster's count: a read asked for before a reset is not made after it
let rosterWatched = false;
const rosterListeners = new Set<() => void>();

/** How often, and how long apart, a roster read that failed is tried again (besides the next hello). */
export const ROSTER_RETRIES = 3;
export const ROSTER_RETRY_MS = 5_000;
let rosterRetries = 0;
let rosterRetry: ReturnType<typeof setTimeout> | null = null;

/** Reads the roster again (a `managers` push, the socket's hello); a hub without one stays at none. Asked while a read
 *  is out, it reads once more when that one is done, so no push is lost to a read that was already on its way. */
export function loadRoster(): Promise<void> {
  if (rosterLoad) {
    const since = rosterReset;
    rosterAgain ??= rosterLoad.then(() => {
      if (since !== rosterReset) return;
      rosterAgain = null;
      return loadRoster();
    });
    return rosterAgain;
  }
  rosterLoad = listManagers()
    .then((r) => {
      rosterRetries = 0;
      setRoster({ managers: r.managers ?? [], default: r.default || BUILTIN_MANAGER });
    })
    .catch(rosterFailed)
    .finally(() => void (rosterLoad = null));
  return rosterLoad;
}

/** A roster read that failed: the default is taken as the hub's own assistant for now (settleRosterDefault), so pages
 *  waiting for it don't wait for good. A hub without a roster (404) has only that one. Any other failure (a 503, the
 *  network) is read again a few seconds later, a few times, so a blip on a hub whose default is another manager is
 *  put right without waiting for the next hello (what was routed or cached under the hub's own is evaluated again
 *  when the default moves: onDefaultManager). */
function rosterFailed(e: unknown) {
  // a hub without a roster has one assistant: a pick kept from a newer hub at this address names nobody
  if (unsupported(e)) {
    chooseManager(null);
    noteShownIdentity(null);
  }
  settleRosterDefault();
  const status = (e as { status?: unknown } | null)?.status;
  if (status === 404 || rosterRetries >= ROSTER_RETRIES) {
    rosterRetries = 0;
    return;
  }
  rosterRetries++;
  rosterRetry ??= setTimeout(() => {
    rosterRetry = null;
    void loadRoster();
  }, ROSTER_RETRY_MS);
}

/** The roster as last read, or null until (and unless) the hub has one; for code outside React (useRoster in a view). */
export function currentRoster(): ManagerList | null {
  return roster;
}

/** Forgets the roster and any read waiting to be tried again (tests). */
export function resetRoster() {
  if (rosterRetry) clearTimeout(rosterRetry);
  rosterRetry = null;
  rosterRetries = 0;
  rosterLoad = null;
  rosterAgain = null;
  rosterReset++;
  roster = null;
  clearConfirmedDefault();
}

/** Puts a roster in place (tests set one directly); the current assistant follows a default that moved, and goes back
 *  to the default when the one picked left the roster, is not installed, or is hidden from the switcher (#9055). */
export function setRoster(next: ManagerList | null) {
  roster = next;
  if (next) {
    setRosterDefault(next.default);
    const chosen = chosenManager();
    if (chosen && !switcherManagers(next).some((m) => m.id === chosen)) chooseManager(null);
    noteCurrentAssistant();
  } else {
    clearConfirmedDefault();
  }
  rosterListeners.forEach((l) => l());
}

/** Keeps the pick's name and the head's display-only identity as the roster gives them. The latter includes the
 *  default, and only an installed current manager: an empty or installing-only roster must not show a stale one. */
function noteCurrentAssistant() {
  const picked = chosenManager();
  if (picked && roster) notePickedName(roster.managers.find((m) => m.id === picked)?.name ?? null);
  if (!roster) return;
  const current = installedManagers(roster).find((m) => m.id === shownManager());
  noteShownIdentity(current ? {
    id: current.id,
    name: current.name,
    isDefault: current.id === roster.default,
    line: switcherLine(current),
    tone: statusLine(current).tone,
  } : null);
}
onManagerChoice(noteCurrentAssistant);

/** Reads the roster now and again on every hello and `managers` push. The app calls it once signed in, so the default
 *  is known before the pages that go by it ask anything (an inbox item opened from a link, the sidebar's badges). */
export function watchRoster() {
  if (rosterWatched) return;
  rosterWatched = true;
  onHubEvent("managers", () => void loadRoster());
  onHubEvent("hello", () => void loadRoster());
  void loadRoster();
}

export function subscribeRoster(l: () => void) {
  watchRoster();
  rosterListeners.add(l);
  return () => void rosterListeners.delete(l);
}

/** The roster, or null until (and unless) the hub has one. */
export function useRoster(): ManagerList | null {
  return useSyncExternalStore(subscribeRoster, currentRoster, currentRoster);
}

/** The managers that run (or ran): the ones still being installed have nothing to show yet. */
export function installedManagers(list: ManagerList): Manager[] {
  return list.managers.filter((m) => !m.installing);
}

/** The installed managers the switcher lists, in its order: a hidden one (the built-in, kept out while it is not used,
 *  #9055) is left out. It still counts as installed: scoping, badges and the Default tag go by `managerOrder`. */
export function switcherManagers(list: ManagerList): Manager[] {
  return managerOrder(list).filter((m) => !m.hidden);
}

/** Whether there is more than one manager: only then do the pages switch and group by manager. */
export function multiManager(list: ManagerList | null): list is ManagerList {
  return !!list && installedManagers(list).length > 1;
}

/** The installed managers in the order the pages show them: the default first, then the roster's order. */
export function managerOrder(list: ManagerList): Manager[] {
  const ms = installedManagers(list);
  const d = ms.find((m) => m.id === list.default);
  return d ? [d, ...ms.filter((m) => m !== d)] : ms;
}

/** The line beside an offline manager's name: "Last synced 3 hours ago"; "" for one online (or never seen). */
export function lastSyncedText(m: Manager, now = Date.now()): string {
  if (m.online || m.installing) return "";
  const when = ago(m.last_seen, now);
  return when ? t("settings-managers.last_synced", { time: when }) : t("settings-managers.offline");
}

/**
 * The manager an offline computer is offline with (several managers only): its own, when that one isn't connected. The
 * hub then shows it offline with its words for an unreachable assistant; the pages name the manager instead, and offer
 * no Reconnect, which goes through that manager.
 */
export function offlineManagerOf(list: ManagerList | null, m: { online: boolean; manager?: string }): Manager | null {
  if (m.online || !multiManager(list)) return null;
  const owner = list.managers.find((x) => x.id === ownerIn(list, m.manager));
  return owner && !owner.online && !owner.installing ? owner : null;
}

/** Which of the shown groups a thing with manager `id` goes in: its own, else (unknown, or none) the default's. */
export function ownerIn(list: ManagerList, id: string | undefined): string {
  return id && installedManagers(list).some((m) => m.id === id) ? id : list.default;
}

// ── the switcher (the sidebar's head): each assistant's avatar, line and count ──

/** An assistant's avatar color (the same rule on every client): the default one is the AI purple, any other the hue
 *  its id's UTF-8 bytes add up to, of five. A CSS color: a token of the stylesheet, light and dark. */
export const AVATAR_HUES = ["blue", "green", "orange", "pink", "indigo"] as const;

export function avatarColor(id: string, isDefault: boolean): string {
  if (isDefault) return "var(--ai)";
  let sum = 0;
  for (const b of new TextEncoder().encode(id)) sum += b;
  return `var(--hue-${AVATAR_HUES[sum % AVATAR_HUES.length]})`;
}

/** The letter on an assistant's avatar: its name's first character (a whole emoji or accented letter), upper case. */
export function avatarInitial(name: string): string {
  const first = [...new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(name.trim())][0]?.segment ?? "";
  return first.toLocaleUpperCase();
}

/** The line under an assistant's name in the switcher: "Online · <computer>", "Offline · last synced …", or
 *  "Installing · code expires in n minutes". */
export function switcherLine(m: Manager, now = Date.now()): string {
  const status = statusLine(m, now).text;
  return m.online && !m.installing && m.host ? `${status} · ${m.host}` : status;
}

/** The current assistant's record: the one picked, else the default's; null without a roster. */
export function managerOf(list: ManagerList | null, id: string): Manager | null {
  return list?.managers.find((m) => m.id === id) ?? null;
}

const normHost = (s: string | undefined): string => s?.trim().toLowerCase() ?? "";

type HostMachine = { id: string; hostname?: string; host?: string; machine_uid?: string };

/** The kind of binding, so a caller looking up one host can prefer a UID match over a legacy name match. */
export function managerHostMatch(manager: Manager, machine: HostMachine): "uid" | "host" | undefined {
  const managerUid = normHost(manager.machine_uid);
  const machineUid = normHost(machine.machine_uid);
  if (managerUid && machineUid) return managerUid === machineUid ? "uid" : undefined;
  const managerHost = normHost(manager.host);
  const machineHost = normHost(machine.hostname) || normHost(machine.host) || normHost(machine.id);
  return managerHost && managerHost === machineHost ? "host" : undefined;
}

/** Installed managers hosted on `machine`, in display order (default first). Matches by `machine_uid` when both carry
 *  one (never falling back to `host` when two distinct UIDs disagree), and falls back safely to case-insensitive host
 *  matching for older managers or hubs (#14964). */
export function hostedManagersForMachine(
  roster: ManagerList | null,
  machine: HostMachine,
): Manager[] {
  return roster ? managerOrder(roster).filter((m) => managerHostMatch(m, machine)) : [];
}

const NON_COMPARABLE_VERSION = new Set(["", "dev", "unknown"]);
const BUILD_STAMP = /-(\d{12})$/;
const SEMVER = /^v?(\d+)\.(\d+)\.(\d+)/;

/** Whether an online hosted manager is running an older/different version behind the computer's node version.
 *  Comparable build timestamps (`-YYYYMMDDHHMM`) and semver triples flag lag only when the manager is older. */
export function managerVersionLag(manager: Pick<Manager, "online" | "version">, nodeVersion: string | undefined): boolean {
  if (!manager.online) return false;
  const mv = manager.version?.trim() ?? "";
  const nv = nodeVersion?.trim() ?? "";
  if (NON_COMPARABLE_VERSION.has(mv.toLowerCase()) || NON_COMPARABLE_VERSION.has(nv.toLowerCase())) return false;
  if (mv === nv) return false;
  const ms = BUILD_STAMP.exec(mv);
  const ns = BUILD_STAMP.exec(nv);
  if (ms && ns) return ms[1]! < ns[1]!;
  const mvSem = SEMVER.exec(mv);
  const nvSem = SEMVER.exec(nv);
  if (mvSem && nvSem) {
    for (let i = 1; i <= 3; i++) {
      const diff = Number(mvSem[i]) - Number(nvSem[i]);
      if (diff !== 0) return diff < 0;
    }
  }
  return true;
}

export type HostedManagerAttention = "pending_restart" | "version_lag" | null;

/** Attention state for a co-located manager: explicit `pending_restart` takes priority over version lag. */
export function hostedManagerAttention(
  manager: Pick<Manager, "online" | "version" | "pending_restart">,
  nodeVersion: string | undefined,
): HostedManagerAttention {
  if (manager.pending_restart) return "pending_restart";
  if (managerVersionLag(manager, nodeVersion)) return "version_lag";
  return null;
}
