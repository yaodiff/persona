// What is wrong with a computer, as the Machines page's chips and alert sheet say it (#8037, docs/web/web.md
// "Machines"): one alert per problem, worst first, from what the page already has (the machine list, its latest metric
// sample, the launcher health records). Thresholds are docs/server/machines.md "Alerts" (lib/machine-detail.ts
// `level`). A computer waiting to be confirmed, never connected, or whose assistant is offline keeps its own notice on
// the card and gets no alerts. Also the cards' order, the native MachineConcern's: pending, offline, critical,
// warning, needs you, version mismatch, then the rest in the hub's order. Pure: no requests, no React.
import { t } from "../i18n";
import { isPending, neverConnected } from "./add-computer";
import { stripAnsi } from "./ansi";
import { disconnectExpected } from "./machine-deploy";
import { level, memPercent, percent, type Level } from "./machine-detail";
import { classifyConnection } from "./node-health";
import type { Machine, MachineMetrics } from "./types";

export type AlertKind = "offline" | "deploy_failed" | "memory" | "disk" | "cpu" | "load" | "launcher" | "mismatch";
export type Severity = "critical" | "warning" | "info";

/** Why a computer is offline, in the words the sheet has a checklist for. */
export type OfflineReason = "auth" | "unreachable" | "timeout" | "version" | "node_service_down" | "link_offline" | "node_outdated" | "never_connected" | "unknown";

export interface MachineAlert {
  kind: AlertKind;
  severity: Severity;
  /** memory / disk / cpu: percent; load: load per core. */
  value?: number;
  /** offline: why. */
  reason?: OfflineReason;
  /** launcher: how many launchers fail on it. */
  count?: number;
  /** mismatch: a redeploy is under way. */
  busy?: boolean;
  /** mismatch: the new build is installed and waiting for active work to drain before restarting. */
  draining?: boolean;
}

/** A launcher health record (GET /api/v1/launchers/health, docs/server/launcher-health.md "API for the board"). */
export interface LauncherHealthRecord {
  machine: string;
  launcher: string;
  agent?: string;
  /** ok | failing | repairing | blocked */
  state: string;
  kind?: string;
  error?: string;
  checked_at?: string;
  fails?: number;
  resets_at?: string;
  blocked_reason?: string;
  source?: string;
}

/** A record that says the launcher does not work on its computer now. */
export const launcherFailing = (r: Pick<LauncherHealthRecord, "state">): boolean => r.state === "failing" || r.state === "blocked";

/** A record's error text without terminal escapes or shell startup noise around it (an older hub keeps them). */
export const launcherErrorText = (r: Pick<LauncherHealthRecord, "error">): string => stripAnsi(r.error ?? "").trim();

/** A record's blocked reason without terminal escapes (an older hub keeps them). */
export const launcherBlockedReason = (r: Pick<LauncherHealthRecord, "blocked_reason">): string => stripAnsi(r.blocked_reason ?? "").trim();

export interface AlertInput {
  machine: Machine;
  metrics?: MachineMetrics;
  /** Launchers failing or blocked on it (launcherFailing). */
  launchers?: number;
  /** Its assistant is offline (several assistants): the card says that instead. */
  managerOffline?: boolean;
}

const SEVERITY_RANK: Record<Severity, number> = { critical: 0, warning: 1, info: 2 };
const KIND_RANK: Record<AlertKind, number> = { offline: 0, deploy_failed: 1, memory: 2, disk: 3, cpu: 4, load: 5, launcher: 6, mismatch: 7 };
const CODES: OfflineReason[] = ["node_service_down", "link_offline", "never_connected", "node_outdated"];

const finite = (v: number | undefined): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
const positive = (v: number | undefined): number | undefined => (finite(v) !== undefined && v! > 0 ? v : undefined);
const severity = (l: Level): Severity | null => (l === "critical" ? "critical" : l === "warning" ? "warning" : null);

/** The hub's error_code when it has one of ours, else what the error text says (lib/node-health.ts). */
export function offlineReason(m: Machine): OfflineReason {
  const code = CODES.find((c) => c === m.error_code);
  if (code) return code;
  return classifyConnection({ ...m, online: false })?.kind ?? "unknown";
}

/** Load per core as every screen writes it, one decimal (the apps' MetricFormat.perCore): 1.43 is "1.4". */
export const perCoreText = (perCore: number): string => perCore.toFixed(1);

/** Load per core in words: "1.4 per core" (the chip, the machine page's Load tile, the alert sheet). */
export const perCoreLabel = (perCore: number): string => t("lib-machine-alerts.per_core", { value: perCoreText(perCore) });

/** What load counts, in one sentence wherever load is shown (#12519; the apps' MachineCard.loadDefinition,
 *  docs/server/machines.md "What load counts"): the raw number alone read as a wrong one. */
export const loadDefinition = (): string => t("lib-machine-alerts.load_definition");

/** The latest memory, disk and CPU percents and the load per core; undefined for what was not measured. */
export function readings(machine: Machine, metrics: MachineMetrics | undefined): { memory?: number; disk?: number; cpu?: number; load?: number; cores?: number } {
  const latest = metrics?.latest;
  const memTotal = positive(latest?.mem_total) ?? positive(metrics?.system?.memory) ?? positive(machine.system?.memory);
  const cores = positive(metrics?.system?.cores) ?? positive(machine.system?.cores);
  const disk = positive(latest?.disk_total) && finite(latest?.disk_used) !== undefined ? (100 * latest!.disk_used!) / latest!.disk_total! : undefined;
  const load = cores && finite(latest?.load) !== undefined ? latest!.load! / cores : undefined;
  return { memory: memPercent(finite(latest?.mem_used), memTotal), disk, cpu: finite(latest?.cpu), load, cores };
}

/** Everything wrong with the computer, worst first (critical, warning, info; then offline, a failed deploy, memory,
 *  disk, CPU, load, launchers, the version). */
export function machineAlerts({ machine: m, metrics, launchers = 0, managerOffline }: AlertInput): MachineAlert[] {
  if (isPending(m) || neverConnected(m) || managerOffline) return [];
  // lib/machine-deploy.ts's busy and expectedDisconnect, read here without machineDeployment: the page orders its cards by
  // these alerts on every push, and a card counts its deployment reads (MachinesCardMemo.test.tsx)
  const busy = !!(m.deploying || m.deploy_phase);
  const expectedDisconnect = disconnectExpected(m);
  const out: MachineAlert[] = [];
  const online = m.online && metrics?.online !== false;
  if (!m.online && !expectedDisconnect) out.push({ kind: "offline", severity: "critical", reason: offlineReason(m) });
  if (m.deploy_error && !busy) out.push({ kind: "deploy_failed", severity: "warning" });
  if (online) {
    const r = readings(m, metrics);
    const push = (kind: AlertKind, value: number | undefined, warn: number, crit: number) => {
      const s = severity(level(value, warn, crit));
      if (s) out.push({ kind, severity: s, value });
    };
    push("memory", r.memory, 80, 90);
    push("disk", r.disk, 80, 90);
    push("cpu", r.cpu, 75, 90);
    push("load", r.load, 1, 2);
  }
  if (launchers > 0) out.push({ kind: "launcher", severity: "warning", count: launchers });
  if ((m.node_mismatch || busy) && !m.deploy_error) out.push({ kind: "mismatch", severity: "info", ...(busy ? { busy: true } : {}), ...(m.deploy_phase === "draining" ? { draining: true } : {}) });
  return out.sort((a, b) => SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity] || KIND_RANK[a.kind] - KIND_RANK[b.kind]);
}

/** The chip's words: "Memory used · 95%", "High load · 1.4 per core", "Offline", "Launcher failing · 2". On a card
 *  (`card`), whose own notice already says why it is offline, the offline chip says what it opens: "Offline · What to
 *  check". */
export function alertLabel(a: MachineAlert, card = false): string {
  if (card && a.kind === "offline") return t("lib-machine-alerts.offline_check");
  const name = alertTitle(a);
  if (a.kind === "load" && a.value !== undefined) return t("lib-machine-alerts.with_value", { name, value: perCoreLabel(a.value) });
  if (a.value !== undefined) return t("lib-machine-alerts.with_value", { name, value: percent(a.value) });
  if (a.kind === "launcher" && (a.count ?? 0) > 1) return t("lib-machine-alerts.with_value", { name, value: String(a.count) });
  return name;
}

/** The alert's name alone ("Memory used"). */
export function alertTitle(a: Pick<MachineAlert, "kind" | "busy" | "draining">): string {
  switch (a.kind) {
    case "offline":
      return t("lib-machine-alerts.offline");
    case "deploy_failed":
      return t("lib-machine-alerts.deploy_failed");
    case "memory":
      return t("lib-machine-alerts.memory");
    case "disk":
      return t("lib-machine-alerts.disk");
    case "cpu":
      return t("lib-machine-alerts.cpu");
    case "load":
      return t("lib-machine-alerts.load");
    case "launcher":
      return t("lib-machine-alerts.launcher");
    case "mismatch":
      return a.draining ? t("lib-machine-alerts.waiting_restart") : a.busy ? t("lib-machine-alerts.updating") : t("lib-machine-alerts.mismatch");
  }
}

/** What a screen reader says before the chip's words. */
export function severityText(s: Severity): string {
  return s === "critical" ? t("lib-machine-alerts.severity_critical") : s === "warning" ? t("lib-machine-alerts.severity_warning") : t("lib-machine-alerts.severity_info");
}

/** The chip's words with the severity first, for a screen reader and the tooltip: "Critical: Memory used · 95%". */
export function alertSpoken(a: MachineAlert, card = false): string {
  return t("lib-machine-alerts.spoken", { severity: severityText(a.severity), label: alertLabel(a, card) });
}

/** The worst severity among the alerts, null for none. */
export function worst(alerts: readonly MachineAlert[]): Severity | null {
  return alerts.reduce<Severity | null>((w, a) => (w === null || SEVERITY_RANK[a.severity] < SEVERITY_RANK[w] ? a.severity : w), null);
}

// ── the cards' order ──

export type Concern = "pending" | "offline" | "critical" | "warning" | "needs_you" | "mismatch";
const CONCERN_RANK: Record<Concern, number> = { pending: 0, offline: 1, critical: 2, warning: 3, needs_you: 4, mismatch: 5 };

/** The native MachineConcern of a computer: what puts its card first; null for none. */
export function concernOf(m: Machine, alerts: readonly MachineAlert[], needsYou: number): Concern | null {
  if (isPending(m)) return "pending";
  if (!m.online) return "offline";
  const metric = worst(alerts.filter((a) => a.kind !== "offline" && a.kind !== "mismatch"));
  if (metric === "critical") return "critical";
  if (metric === "warning") return "warning";
  if (needsYou > 0) return "needs_you";
  if (m.node_mismatch || m.deploying || m.deploy_phase) return "mismatch";
  return null;
}

/** The computers with a concern first, the most pressing first; the same concern, and the rest, in the order given
 *  (the hub's), so a card moves only when what it shows changes. Returns `machines` itself when nothing moves. */
export function orderMachines<M extends Machine>(machines: readonly M[], concern: (m: M) => Concern | null): readonly M[] {
  const ranked = machines.map((m, i) => ({ m, i, r: (() => { const c = concern(m); return c ? CONCERN_RANK[c] : 99; })() }));
  ranked.sort((a, b) => a.r - b.r || a.i - b.i);
  return ranked.every((x, i) => x.i === i) ? machines : ranked.map((x) => x.m);
}

// ── builds ──

/** When a build was made, from its name's trailing `-YYYYMMDDHHMM` (UTC, scripts/deploy/deploy.sh); undefined for a
 *  name without one ("dev", "mock"). */
export function buildTime(version: string | undefined): number | undefined {
  const m = /-(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})$/.exec(version ?? "");
  if (!m) return undefined;
  const at = Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]);
  return Number.isFinite(at) ? at : undefined;
}

/** How much older (negative: newer) the node's build is than the hub's, in ms; undefined when either time is unknown. */
export function buildGap(hub: string | undefined, node: string | undefined): number | undefined {
  const h = buildTime(hub), n = buildTime(node);
  return h === undefined || n === undefined ? undefined : h - n;
}

/** "3 days", "5 hours", "12 minutes": a gap between builds, in its largest whole unit. */
export function gapText(ms: number): string {
  const min = Math.round(Math.abs(ms) / 60_000);
  if (min >= 1440) return t("lib-machine-alerts.gap_days", { count: Math.floor(min / 1440) });
  if (min >= 60) return t("lib-machine-alerts.gap_hours", { count: Math.floor(min / 60) });
  return t("lib-machine-alerts.gap_minutes", { count: Math.max(1, min) });
}
