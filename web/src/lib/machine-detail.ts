// Machine details (docs/server/machines.md "App"): the pure part of the page — ranges, the metric charts' scales, gaps, ticks and
// the hover read-out, the number formats, the agents by space and the system facts. MachineDetail.tsx only draws.
import { formatDate, t } from "../i18n";
import { normalizeTraeCliName } from "./agent-kind";
import type { HostWorkspace } from "./workspace-catalog";
import { activityOf, tabLabel } from "./format";
import type { AgentState, Machine, Session } from "./types";
import type { MachineComputer } from "./computer";
import { stateCount } from "./state-labels";

// ── wire types (GET /api/v1/machines/{id}/metrics, docs/protocol/protocol.md) ──

/** One step of history (the step's average). mem / swap / disk are used bytes, rx / tx bytes a second. */
export interface DetailPoint {
  t: number;
  cpu?: number;
  load?: number;
  mem?: number;
  swap?: number;
  rx?: number;
  tx?: number;
  disk?: number;
  battery_percent?: number;
  temp_c?: number;
  gap?: boolean;
}

/** The latest reading (every 10 s); a field the node could not measure is missing, not 0. */
export interface DetailSample {
  t: number;
  cpu?: number;
  cores?: number[];
  load?: number;
  mem_used?: number;
  mem_total?: number;
  swap_used?: number;
  swap_total?: number;
  net_rx?: number;
  net_tx?: number;
  disk_used?: number;
  disk_total?: number;
  battery_percent?: number;
  battery_charging?: boolean;
  temp_c?: number;
}

/** Machine.system (docs/server/machines.md "System information"). */
export interface SystemInfo {
  os?: string;
  distro?: string;
  name?: string;
  version?: string;
  build?: string;
  kernel?: string;
  arch?: string;
  model?: string;
  cpu?: string;
  cores?: number;
  memory?: number;
  boot_time?: number;
}

export interface DetailMetrics {
  machine_id: string;
  online: boolean;
  system?: SystemInfo;
  latest?: DetailSample | null;
  points: DetailPoint[];
}

/** What the machine list carries beyond types.ts's Machine (the detail page reads these too). */
/** computer: only while the hub's extension cua is on, and only for a Mac (docs/extensions/computer-use.md). */
export type DetailMachine = Machine & { ssh_host?: string; node_version?: string; system?: SystemInfo; computer?: MachineComputer };

// ── ranges ──

export type MetricRange = "1h" | "6h" | "24h";

/** The hub's steps per range (10 / 60 / 240 s, about 360 points), and how often the page refetches it: the hour
 * every 10 s, the longer ranges once a minute (a new point there comes only every 1 or 4 minutes), as on iOS. */
export const RANGES: Record<MetricRange, { label: string; secs: number; step: number; poll: number }> = {
  "1h": { label: t("lib-machine-detail.range_hours", { count: 1 }), secs: 3600, step: 10, poll: 10_000 },
  "6h": { label: t("lib-machine-detail.range_hours", { count: 6 }), secs: 6 * 3600, step: 60, poll: 60_000 },
  "24h": { label: t("lib-machine-detail.range_hours", { count: 24 }), secs: 86400, step: 240, poll: 60_000 },
};

export const RANGE_KEYS = Object.keys(RANGES) as MetricRange[];

// ── series ──

export type Reader = (p: DetailPoint) => number | undefined;

/** The values of one measure, each run between gaps (or missing values) its own line: no line crosses a time the
 * machine sent nothing. A run of one point is kept (drawn as a dot). */
export function runs(points: DetailPoint[], read: Reader): { t: number; v: number }[][] {
  const out: { t: number; v: number }[][] = [];
  let cur: { t: number; v: number }[] = [];
  for (const p of points) {
    const v = p.gap ? undefined : read(p);
    if (v === undefined || !Number.isFinite(v)) {
      if (cur.length) out.push(cur);
      cur = [];
      continue;
    }
    cur.push({ t: p.t, v });
  }
  if (cur.length) out.push(cur);
  return out;
}

/** The point nearest to `t`, within two steps; nothing over a gap (the hover finds no value there). */
export function nearest(points: DetailPoint[], t: number, step: number): DetailPoint | undefined {
  let best: DetailPoint | undefined;
  for (const p of points) {
    if (p.gap) continue;
    if (!best || Math.abs(p.t - t) < Math.abs(best.t - t)) best = p;
  }
  return best && Math.abs(best.t - t) <= step * 2 ? best : undefined;
}

/** Whether a range has no line to draw at all (just started, or offline all along). */
export function isEmpty(points: DetailPoint[]): boolean {
  return !points.some((p) => !p.gap);
}

/** A percent of the memory total; undefined without either. */
export function memPercent(used: number | undefined, total: number | undefined): number | undefined {
  return used !== undefined && total ? (100 * used) / total : undefined;
}

export function temperature(v: number | undefined): string {
  return v !== undefined && Number.isFinite(v) ? `${v.toFixed(1)}°C` : "—";
}

// ── scales and ticks ──

/** A linear map from [d0, d1] to [r0, r1]. */
export function scale(d0: number, d1: number, r0: number, r1: number) {
  const span = d1 - d0 || 1;
  return (v: number) => r0 + ((v - d0) / span) * (r1 - r0);
}

/** The time under a pointer at `x` of `width`, clamped to the range. */
export function timeAt(x: number, width: number, t0: number, t1: number): number {
  const f = Math.min(1, Math.max(0, width > 0 ? x / width : 0));
  return t0 + f * (t1 - t0);
}

/** A round step for about `count` intervals over [0, max]: 1, 2, 2.5 or 5 times a power of ten (or of 1024 for
 * bytes, so the axis reads 512 KB, 1 MB, 1.5 MB…). */
export function niceStep(max: number, count = 2, binary = false): number {
  if (!(max > 0)) return 1;
  const raw = max / count;
  let base = 1;
  if (binary) {
    while (raw / base >= 1024) base *= 1024;
  }
  const scaled = raw / base;
  const pow = 10 ** Math.floor(Math.log10(scaled));
  const n = scaled / pow;
  const m = n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10;
  // 1000 of a binary unit is the next unit (1000 KB → 1 MB).
  return (binary && m * pow >= 1000 ? 1024 : m * pow) * base;
}

/** The y axis: the top (a round number at or over the largest value, so the line never touches the top) and at most
 * three ticks from 0. Percent charts are fixed 0…100. */
export function yAxis(max: number, opts: { percent?: boolean; binary?: boolean } = {}): { top: number; ticks: number[] } {
  if (opts.percent) return { top: 100, ticks: [0, 50, 100] };
  const step = niceStep(max * 1.08, 2, opts.binary);
  const top = Math.max(step, Math.ceil((max * 1.08) / step) * step);
  // Bytes: a top of 1000 of a unit is the next unit (1000 KB → 1 MB, ticks 512 KB and 1 MB).
  if (opts.binary && top >= 1024) {
    let base = 1;
    while (top / base >= 1024) base *= 1024;
    if (top / base >= 1000) return { top: 1024 * base, ticks: [0, 512 * base, 1024 * base] };
  }
  const ticks: number[] = [];
  for (let v = 0; v <= top + step / 1e6; v += step) ticks.push(v);
  // At most three labels: 0, the top, and the middle when it is one of the round steps.
  return { top, ticks: ticks.length > 3 ? (ticks.some((v) => Math.abs(v - top / 2) < step / 1e6) ? [0, top / 2, top] : [0, top]) : ticks };
}

const MIN = 60;
const TICK_STEPS = [5 * MIN, 10 * MIN, 15 * MIN, 30 * MIN, 3600, 2 * 3600, 3 * 3600, 6 * 3600, 12 * 3600];

/** At most `max` round times inside [t0, t1] (every 15 minutes, every 2 hours…), on the local clock. */
export function timeTicks(t0: number, t1: number, max = 4): number[] {
  return clockTicks(t0, t1, TICK_STEPS.find((s) => (t1 - t0) / s <= max) ?? 24 * 3600);
}

/** The times inside [t0, t1] that are a whole number of `step` seconds (a divisor of a day) after a local midnight.
 * Each day is laid out on its own clock, so the ticks before and after a daylight-saving change stay on round local
 * times (#9863). */
export function clockTicks(t0: number, t1: number, step: number): number[] {
  const out = new Set<number>();
  const first = new Date(t0 * 1000);
  for (let d = new Date(first.getFullYear(), first.getMonth(), first.getDate()); d.getTime() <= t1 * 1000; d = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1)) {
    for (let s = 0; s < 86400; s += step) {
      // A time the clocks skip lands on the next one, hence the set.
      const at = new Date(d.getFullYear(), d.getMonth(), d.getDate(), 0, 0, s).getTime() / 1000;
      if (at >= t0 && at <= t1) out.add(at);
    }
  }
  return [...out];
}

// ── formats (the same as the app's MetricFormat) ──

/** "41.2 GB", "512 MB" (binary units, like Activity Monitor and free -h). */
export function bytesBin(v: number | undefined): string {
  if (v === undefined || !Number.isFinite(v)) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let x = Math.max(0, v),
    i = 0;
  // The unit and the decimals follow the number as it is shown, after rounding (apple MachineMetrics.bytes): 1023.6 KB is
  // "1.00 MB", not "1024 KB"; 99.97 MB is "100 MB", not "100.0 MB".
  while (x >= 1023.5 && i < units.length - 1) {
    x /= 1024;
    i++;
  }
  if (i === 0) return `${Math.round(x)} B`;
  return `${x >= 99.95 ? x.toFixed(0) : x >= 9.995 ? x.toFixed(1) : x.toFixed(2)} ${units[i]}`;
}

/** Used of total for a tile's narrow sub line: one unit when both share it ("56.5 / 64.0 GB"), else both
 * ("812 MB / 8.00 GB"). Three tiles a row on a 390-wide phone leave about 94 px, and "56.5 GB / 64.0 GB" needs 92 (#3637). */
export function usedOfTotal(used: number | undefined, total: number | undefined): string {
  const a = bytesBin(used),
    b = bytesBin(total);
  const ua = a.split(" ")[1],
    ub = b.split(" ")[1];
  return ua !== undefined && ua === ub ? `${a.split(" ")[0]} / ${b}` : `${a} / ${b}`;
}

/** An axis label: the same units without trailing zeros ("1.5 MB", "512 KB"). */
export function bytesAxis(v: number): string {
  if (v <= 0) return "0";
  return bytesBin(v).replace(/\.0+ /, " ").replace(/(\.\d*?)0+ /, "$1 ");
}

/** A rate: "1.20 MB/s". */
export function rate(v: number | undefined): string {
  return v === undefined ? "—" : `${bytesBin(v)}/s`;
}

/** A percent: "37%", "4.5%" under 10. */
export function percent(v: number | undefined): string {
  if (v === undefined || !Number.isFinite(v)) return "—";
  return v < 10 && v > 0 ? `${v.toFixed(1)}%` : `${Math.round(v)}%`;
}

export function load(v: number | undefined): string {
  return v === undefined ? "—" : v >= 10 ? v.toFixed(1) : v.toFixed(2);
}

/** A load axis label: "0", "0.5", "16" (no trailing zeros). */
export function loadAxis(v: number): string {
  return v <= 0 ? "0" : String(Number(v.toFixed(2)));
}

/** Each core's busy percent now (Linux nodes send it with the latest sample, iOS CoreBars): only for a computer that
 * is online and has more than one, each clamped to 0…100. */
export function coresNow(latest: DetailSample | null | undefined, online: boolean): number[] | undefined {
  const cores = latest?.cores;
  if (!online || !cores || cores.length < 2) return undefined;
  return cores.map((c) => (Number.isFinite(c) ? Math.min(100, Math.max(0, c)) : 0));
}

/** "3 d 4 h", "5 h 12 min", "8 min" (in Chinese). */
export function uptime(bootSec: number, nowSec: number): string {
  const s = Math.max(0, Math.floor(nowSec - bootSec));
  const d = Math.floor(s / 86400),
    h = Math.floor((s % 86400) / 3600),
    m = Math.floor((s % 3600) / 60);
  if (d > 0) return h > 0 ? t("lib-machine-detail.uptime_days_hours", { d, h }) : t("lib-machine-detail.uptime_days", { d });
  if (h > 0) return t("lib-machine-detail.uptime_hours_minutes", { h, m });
  return t("lib-machine-detail.uptime_minutes", { m });
}

/** "14:05" on the local clock, with the offset of that moment (a time before a daylight-saving change reads as the
 * clock did then, #9863); `tz` fixes the offset east of UTC in seconds instead. */
export function hm(tSec: number, tz = localOffset(tSec * 1000)): string {
  const local = (((Math.floor(tSec + tz) % 86400) + 86400) % 86400) / 60;
  return `${String(Math.floor(local / 60)).padStart(2, "0")}:${String(Math.floor(local % 60)).padStart(2, "0")}`;
}

/** The local offset east of UTC, in seconds. */
export function localOffset(at = Date.now()): number {
  return -new Date(at).getTimezoneOffset() * 60;
}

export type Level = "normal" | "warning" | "critical";

/** docs/server/machines.md "Alerts": CPU ≥ 75 / 90 %, memory and disk ≥ 80 / 90 %, load ≥ 1 / 2 × cores. */
export function level(v: number | undefined, warn: number, crit: number): Level {
  if (v === undefined) return "normal";
  return v >= crit ? "critical" : v >= warn ? "warning" : "normal";
}

// ── agents by space ──

export interface SpaceGroup {
  workspace?: HostWorkspace;
  key: string;
  name: string;
  sessions: Session[];
  counts: { needs: number; working: number; unread: number };
}

/** A machine's live agents (not the assistant's conversations), by herdr space in herdr's order (space, then tab),
 * agents outside any space last. */
export function agentsBySpace(sessions: Session[], workspaceOf?: (s: Session) => HostWorkspace | undefined): SpaceGroup[] {
  const live = sessions.filter((s) => !s.assistant && activityOf(s) !== "offline");
  const map = new Map<string, SpaceGroup & { index: number }>();
  for (const s of live) {
    const workspace = workspaceOf?.(s);
    const key = workspace ? JSON.stringify(["workspace", workspace.id]) : s.space_id || "";
    if (!map.has(key)) {
      const name = workspace?.title || s.space_name || s.space_id || workspace?.cwd || t("lib-machine-detail.no_space");
      map.set(key, { key, name, workspace, sessions: [], counts: { needs: 0, working: 0, unread: 0 },
        index: workspace?.index ?? (s.space_id ? s.space_index ?? 0 : Number.MAX_SAFE_INTEGER) });
    }
    const g = map.get(key)!;
    g.sessions.push(s);
    const st = activityOf(s);
    if (s.view ? s.view.counted : st === "needs_you") g.counts.needs++;
    else if (st === "working") g.counts.working++;
    else if (st === "unread") g.counts.unread++;
  }
  const bySlot = (a: Session, b: Session) => (a.slot_index ?? 0) - (b.slot_index ?? 0) || a.session_id.localeCompare(b.session_id);
  return [...map.values()]
    .sort((a, b) => a.index - b.index || a.name.localeCompare(b.name))
    .map(({ index: _index, ...g }) => ({ ...g, sessions: g.sessions.sort(bySlot) }));
}

/** "1 needs you · 2 working" for a space's header; "" when all are quiet. */
export function countsText(c: SpaceGroup["counts"]): string {
  return [c.needs && stateCount("needs_you", c.needs), c.working && stateCount("working", c.working), c.unread && stateCount("unread", c.unread)].filter(Boolean).join(" · ");
}

/** The tab label and, for anything but Claude Code, which agent it is: "#2 · Codex". */
export function agentMeta(s: Session): string {
  const agent = s.agent && s.agent !== "claude" ? normalizeTraeCliName(s.agent_label || (s.agent === "codex" ? "Codex" : s.agent)) : "";
  return [tabLabel(s), agent].filter(Boolean).join(" · ");
}

/** States that say something next to the dot (idle stays quiet: the gray dot says it). */
export function loudState(st: AgentState): boolean {
  return st === "needs_you" || st === "working" || st === "unread";
}

// ── system ──

/** One line under the name: "macOS 26.1 · MacBook Pro · Apple M4 Pro · 14 cores · 48 GB". */
export function systemLine(system: SystemInfo | undefined, m?: DetailMachine): string {
  if (!system) return [m?.os, m?.arch].filter(Boolean).join(" · ") || m?.hostname || "";
  return [system.name || m?.os, system.model, system.cpu, system.cores ? t("lib-machine-detail.cores", { count: system.cores }) : "", system.memory ? bytesBin(system.memory).replace(/\.00 /, " ") : ""].filter(Boolean).join(" · ");
}

/** How the hub reaches the machine. */
export function connectionText(m: DetailMachine): string {
  if (m.kind === "local") return t("lib-machine-detail.conn_local");
  if (m.kind === "ssh" || m.ssh_host) return t("lib-machine-detail.conn_ssh", { host: m.ssh_host || m.id });
  if (m.kind === "link") return t("lib-machine-detail.conn_link");
  return m.kind ? t("lib-machine-detail.conn_kind", { kind: m.kind }) : "";
}

/** "System": what it is, what it runs on, since when, and the versions (label, value, mono?).
 * The boot offset is resolved at the boot instant, which may be in another daylight-saving season. */
export function systemFacts(system: SystemInfo | undefined, m: DetailMachine | undefined, nowSec: number, tz = localOffset((system?.boot_time ?? nowSec) * 1000)): [string, string, boolean?][] {
  const out: [string, string, boolean?][] = [];
  if (system?.name) out.push([t("lib-machine-detail.fact_system"), system.build ? t("lib-machine-detail.fact_system_build", { name: system.name, build: system.build }) : system.name]);
  if (system?.kernel) out.push([t("lib-machine-detail.fact_kernel"), system.kernel, true]);
  const arch = system?.arch || m?.arch;
  if (arch) out.push([t("lib-machine-detail.fact_arch"), arch, true]);
  if (system?.model) out.push([t("lib-machine-detail.fact_model"), system.model]);
  if (system?.cpu) out.push([t("lib-machine-detail.fact_cpu"), system.cpu]);
  if (system?.cores) out.push([t("lib-machine-detail.fact_cores"), t("lib-machine-detail.cores", { count: system.cores })]);
  if (system?.memory) out.push([t("lib-machine-detail.fact_memory"), bytesBin(system.memory).replace(/\.00 /, " ")]);
  if (m?.hostname) out.push([t("lib-machine-detail.fact_hostname"), m.hostname, true]);
  if (system?.boot_time) {
    const d = new Date((system.boot_time + tz) * 1000);
    const day = `${formatDate(d, { year: "numeric", month: "long", day: "numeric", timeZone: "UTC" })} ${hm(system.boot_time, tz)}`;
    out.push([t("lib-machine-detail.fact_boot"), m?.online ? t("lib-machine-detail.fact_boot_uptime", { day, uptime: uptime(system.boot_time, nowSec) }) : day]);
  }
  if (m?.claude_version) out.push(["Claude Code", m.claude_version, true]);
  if (m?.herdr_version) out.push(["herdr", m.herdr_version, true]);
  if (m?.node_version) out.push(["personad", m.node_version, true]);
  const conn = m ? connectionText(m) : "";
  if (conn) out.push([t("lib-machine-detail.fact_connection"), conn]);
  return out;
}
