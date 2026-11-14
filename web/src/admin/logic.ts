import { canonicalAppURL } from "../lib/compatibility";
import { nodeStatusNote } from "../lib/extensions";
// Pure helpers behind the admin pages, kept apart so they can be tested without a browser.
import { formatDate, formatLocale, t } from "../i18n";
import type { UIItem } from "../lib/extension-ui";
import { clock } from "../lib/format";
import type { Access, AdminMachine, AdminExtension, Device, DoctorItem, ExtensionMetrics, LogLine, MeshHub, MeshView, MetricsWindow, Peer, PeerProbe, Restart } from "./types";

// ── access scope (docs/operations/admin.md "Access scope") ──────────────

export const ACCESS_INFO: Record<Access, { label: string; detail: string; risk: string }> = {
  loopback: { label: t("admin-logic.access_loopback"), detail: t("admin-logic.access_loopback_detail"), risk: t("admin-logic.access_loopback_risk") },
  lan: { label: t("admin-logic.access_lan"), detail: t("admin-logic.access_lan_detail"), risk: t("admin-logic.access_lan_risk") },
  cidrs: { label: t("admin-logic.access_cidrs"), detail: t("admin-logic.access_cidrs_detail"), risk: t("admin-logic.access_cidrs_risk") },
  any: { label: t("admin-logic.access_any"), detail: t("admin-logic.access_any_detail"), risk: t("admin-logic.access_any_risk") },
};

export const ACCESS_ORDER: Access[] = ["loopback", "lan", "cidrs", "any"];

// narrowerChoices: what the web page may switch to from the current scope (widening is the hub machine's call).
export function narrowerChoices(current: Access): Access[] {
  switch (current) {
    case "any":
      return ["loopback", "lan", "cidrs", "any"];
    case "lan":
      return ["loopback", "lan"];
    case "cidrs":
      return ["loopback", "cidrs"];
    default:
      return ["loopback"];
  }
}

export function canChoose(current: Access, next: Access): boolean {
  return narrowerChoices(current).includes(next);
}

// A light syntax check; the hub validates for real (and that the new list is no wider than the old one).
export function validCidr(s: string): boolean {
  const m = /^([^/\s]+)\/(\d{1,3})$/.exec(s.trim());
  if (!m) return false;
  const bits = Number(m[2]);
  if (m[1].includes(":")) return bits <= 128 && /^[0-9a-fA-F:.]+$/.test(m[1]) && m[1].split("::").length <= 2;
  const parts = m[1].split(".");
  return bits <= 32 && parts.length === 4 && parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) <= 255);
}

export function lines(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

// ── first setup ──────────────────────────────────────────

// setupRequest is POST setup's body: the setup code only when the hub asked for one, as typed (the hub ignores case
// and dashes).
export function setupRequest(password: string, code: string, codeRequired: boolean): { password: string; setup_code?: string } {
  return codeRequired ? { password, setup_code: code.trim() } : { password };
}

// setupCodeFromHash reads the first-run setup code `personad hub setup` puts in the admin URL's fragment
// (/admin#setup=ABCD-EFGH, docs/assistant/onboarding.md 4.1): a fragment never reaches the server or its logs.
export function setupCodeFromHash(hash: string): string {
  const code = new URLSearchParams(hash.replace(/^#/, "")).get("setup") ?? "";
  return /^[A-Za-z0-9 -]{1,32}$/.test(code.trim()) ? code.trim().toUpperCase() : "";
}

// withoutSetupCode is the fragment with the setup code taken out (the code is spent once used).
export function withoutSetupCode(hash: string): string {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  params.delete("setup");
  const rest = params.toString();
  return rest ? "#" + rest : "";
}

// ── QR code ──────────────────────────────────────────────

// qrPath turns the hub's rows of "0" / "1" into one SVG path (runs of dark modules as rectangles), offset by margin.
export function qrPath(rows: string[], margin = 0): { d: string; size: number } {
  const n = Math.max(rows.length, ...rows.map((r) => r.length));
  let d = "";
  rows.forEach((row, y) => {
    let x = 0;
    while (x < row.length) {
      if (row[x] !== "1") {
        x++;
        continue;
      }
      let run = 1;
      while (row[x + run] === "1") run++;
      d += `M${x + margin} ${y + margin}h${run}v1h-${run}z`;
      x += run;
    }
  });
  return { d, size: n + 2 * margin };
}

// ── time ─────────────────────────────────────────────────

export function uptime(startedAt: string | undefined, now = Date.now()): string {
  const at = startedAt ? Date.parse(startedAt) : NaN;
  if (!Number.isFinite(at)) return "—";
  const s = Math.max(0, Math.floor((now - at) / 1000));
  const d = Math.floor(s / 86400),
    h = Math.floor((s % 86400) / 3600),
    m = Math.floor((s % 3600) / 60);
  if (d > 0) return h ? t("admin-logic.uptime_days_hours", { d, h }) : t("admin-logic.uptime_days", { count: d });
  if (h > 0) return m ? t("admin-logic.uptime_hours_minutes", { h, m }) : t("admin-logic.uptime_hours", { count: h });
  if (m > 0) return t("admin-logic.uptime_minutes", { count: m });
  return t("admin-logic.uptime_under_minute");
}

// countdown is m:ss until expiresAt, or "" once it has passed.
export function countdown(expiresAt: string | undefined, now = Date.now()): string {
  const t = expiresAt ? Date.parse(expiresAt) : NaN;
  if (!Number.isFinite(t) || t <= now) return "";
  const s = Math.ceil((t - now) / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

export function dateTime(iso?: string): string {
  const t = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(t)) return "";
  return formatDate(t, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

// ── labels ───────────────────────────────────────────────

export function restartText(r?: Restart | null): string {
  if (!r?.required) return "";
  const n = r.reasons.length;
  return n > 1 ? t("admin-logic.restart_changes", { count: n }) : t("admin-logic.restart_change");
}

export function groupDoctor(items: DoctorItem[]): Record<DoctorItem["level"], DoctorItem[]> {
  const out: Record<DoctorItem["level"], DoctorItem[]> = { problem: [], ok: [], info: [] };
  for (const it of items) (out[it.level] ?? out.info).push(it);
  return out;
}

// How a hub reaches a computer, in the words the admin page uses (kind of a hub.json entry, or a path's via).
export const KIND_LABEL: Record<string, string> = {
  local: t("admin-logic.kind_local"),
  service: t("admin-logic.kind_service"),
  link: t("admin-logic.kind_link"),
  ssh: "ssh",
  tailcat: "tailcat",
  hub: t("admin-logic.kind_hub"),
};

export function viaLabel(via?: string): string {
  return via ? (KIND_LABEL[via] ?? via) : "";
}

export function machineStatus(m: AdminMachine): { tone: "green" | "red" | "orange" | "gray"; label: string } {
  if (m.state === "pending_add") return { tone: "orange", label: t("admin-logic.machine_pending_add") };
  if (m.state === "pending_remove") return { tone: "gray", label: t("admin-logic.machine_pending_remove") };
  if (m.online) return { tone: "green", label: t("admin-logic.online") };
  if (m.error) return { tone: "red", label: t("admin-logic.unreachable") };
  return { tone: "gray", label: t("admin-logic.offline") };
}

export function deviceKindLabel(kind: string): string {
  return ({ phone: t("admin-logic.device_phone"), watch: t("admin-logic.device_watch"), link: "Persona Link", web: t("admin-logic.device_web"), other: t("admin-logic.device_web") } as Record<string, string>)[kind] ?? kind;
}

export function roleLabel(role: string): string {
  return ({ full: t("admin-logic.role_full"), answer: t("admin-logic.role_answer"), link: t("admin-logic.role_link") } as Record<string, string>)[role] ?? role;
}

export function activeDevices(list: Device[]): Device[] {
  return list.filter((d) => !d.revoked_at);
}

export const REFUSED_TEXT = t("admin-logic.peer_refused");

// peerStatus: how a peer's link reads. A refused link shows as offline with why (its own error, else the usual cause).
export function peerStatus(p: Peer): { tone: "green" | "orange" | "gray"; label: string; error?: string } {
  switch (p.state) {
    case "connected":
      return { tone: "green", label: t("admin-logic.peer_connected") };
    case "connecting":
      return { tone: "orange", label: t("admin-logic.peer_connecting"), error: p.error };
    case "refused":
      return { tone: "gray", label: t("admin-logic.offline"), error: p.error || REFUSED_TEXT };
    default:
      return { tone: "gray", label: t("admin-logic.offline"), error: p.error };
  }
}

// A probed hub can only be linked over TLS: without a TLS port and a key fingerprint there is nothing to pin.
export function probeLinkable(p: PeerProbe): boolean {
  return p.tls_port > 0 && p.fp !== "";
}

export function endpointKindLabel(kind: string): string {
  return ({ ipv6: "IPv6", name: t("admin-logic.endpoint_name"), lan: t("admin-logic.endpoint_lan"), ipv4: "IPv4" } as Record<string, string>)[kind] ?? kind;
}

// Machine ids end up in URLs and hub.json: the same rule the hub enforces.
export function validMachineId(id: string): boolean {
  return /^[a-z0-9][a-z0-9._-]{0,62}$/.test(id);
}

// ── computers: paths, adding, repairing (docs/archive/operations/plan-hub-center.md 4, 5) ──

// hubLabel names the hub a path goes through: this hub, a hub of the group by name, else the start of its id.
export function hubLabel(mesh: MeshView | null | undefined, hubId: string | undefined, selfId?: string): string {
  if (!hubId) return "";
  const h = mesh?.hubs.find((x) => x.hub_id === hubId);
  if (h?.self || hubId === selfId) return t("admin-logic.this_hub");
  return h?.name || hubId.slice(0, 8);
}

export interface PathRow {
  key: string;
  via: string;
  label: string; // "This machine" / "Hub connects" / "Computer connects" / ssh / tailcat / "Via member hub"
  hub: string; // which hub it goes through, by name
  self: boolean; // through this hub itself
  ok: boolean;
  active: boolean;
  rtt?: number;
  error?: string;
}

// pathRows: a computer's paths for the list, the one in use first, the rest in the hub's order (its preference).
export function pathRows(m: AdminMachine, mesh: MeshView | null | undefined, selfId?: string): PathRow[] {
  const rows = (m.paths ?? []).map((p, i) => ({
    key: `${p.via}:${p.hub_id}:${p.id ?? ""}:${i}`,
    via: p.via,
    label: viaLabel(p.via),
    hub: hubLabel(mesh, p.hub_id, selfId),
    self: p.hub_id === selfId || Boolean(mesh?.hubs.find((h) => h.hub_id === p.hub_id)?.self),
    ok: p.ok,
    active: Boolean(p.active),
    rtt: p.rtt_ms && p.rtt_ms > 0 ? p.rtt_ms : undefined,
    error: p.ok ? undefined : p.error || undefined,
  }));
  return [...rows.filter((r) => r.active), ...rows.filter((r) => !r.active)];
}

// A computer configured on a member only (kind hub) is not in this hub's hub.json: it cannot be removed here.
export function canRemove(m: AdminMachine): boolean {
  return m.kind !== "hub" && m.state !== "pending_remove";
}

// ssh and tailcat entries keep working, but new computers come through the node service instead.
export function isLegacy(m: AdminMachine): boolean {
  return m.kind === "ssh" || m.kind === "tailcat";
}

// needsRepair: a service computer whose connector no longer takes this hub's credential (paired with another hub,
// reinstalled). The hub's error then asks to pair it again.
export function needsRepair(m: AdminMachine): boolean {
  // i18n-ignore: matches the hub's error text
  return m.kind === "service" && /重新配对/.test(m.error ?? "");
}

// needsMachines: nothing to drive yet but the hub's own machine; the overview then points at adding computers.
export function needsMachines(list: AdminMachine[]): boolean {
  return list.filter((m) => m.state !== "pending_remove" && m.kind !== "local").length === 0;
}

export interface ConnectorLink {
  addrs: string[];
  fp: string;
  code: string;
}

// parseConnectorLink reads what `persona-link pair --listen` prints: persona://connector?addr=<host:port>&…&fp=…&code=…
// (the hub checks it again). null when it is not one or misses the key fingerprint or the code.
export function parseConnectorLink(raw: string): ConnectorLink | null {
  const m = /^persona:\/\/connector\/?\?(.*)$/is.exec(canonicalAppURL(raw.trim()));
  if (!m) return null;
  const q = new URLSearchParams(m[1]);
  const fp = q.get("fp")?.trim() ?? "";
  const code = q.get("code")?.trim() ?? "";
  if (!fp || !code) return null;
  return { addrs: q.getAll("addr").map((a) => a.trim()).filter(validHostPort), fp, code };
}

// validHostPort: host:port, an IPv6 host in brackets — what the hub takes for a connector's address.
export function validHostPort(s: string): boolean {
  const m = /^(\[[0-9a-fA-F:.%a-zA-Z]+\]|[^\s:/[\]@]+):(\d{1,5})$/.exec(s.trim());
  if (!m) return false;
  const port = Number(m[2]);
  return port > 0 && port < 65536;
}

// validProxy: socks5://[user[:password]@]host:port (or socks5h), the only proxy the hub dials connectors through.
export function validProxy(s: string): boolean {
  const m = /^socks5h?:\/\/(?:[^\s@/]+@)?([^\s@/]+)\/?$/i.exec(s.trim());
  return Boolean(m && validHostPort(m[1]));
}

// shellQuote wraps s in single quotes for a POSIX shell.
export function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

// linuxInstallCommand pairs, installs and starts persona-link on a Linux computer in one go (docs/operations/link.md "Linux").
export function linuxInstallCommand(pairUrl: string): string {
  return `persona-link install --pair - <<< ${shellQuote(pairUrl)}`;
}

// ── the group of hubs (docs/archive/operations/plan-hub-center.md 2) ──────

// sortMeshHubs: the center first, then this hub, then by name.
export function sortMeshHubs(hubs: MeshHub[]): MeshHub[] {
  const rank = (h: MeshHub) => (h.role === "center" ? 0 : h.self ? 1 : 2);
  return [...hubs].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name, formatLocale()));
}

export function centerName(mesh: MeshView): string {
  return mesh.center.name || mesh.hubs.find((h) => h.hub_id === mesh.center.hub_id)?.name || mesh.center.hub_id.slice(0, 8);
}

// memberNotice: what every admin page says on a member hub (null on the center): the group is the center's to run.
export function memberNotice(mesh: MeshView | null | undefined): { title: string; detail: string; offline: boolean } | null {
  if (!mesh || mesh.role !== "member") return null;
  const name = centerName(mesh);
  const offline = !mesh.center.online;
  return {
    title: t("admin-logic.member_title", { name }),
    detail: offline ? t("admin-logic.member_detail_offline") : t("admin-logic.member_detail"),
    offline,
  };
}

// ── extensions ──

// extensionStatus: the state's words (as the app's Settings › Extensions says them) and tone.
export function extensionStatus(p: Pick<AdminExtension, "status" | "node_status">): { tone: "green" | "red" | "orange" | "gray"; label: string } {
  // A node-only extension's line is the aggregate across its machines ("Running on 2 machines"), not the idle hub process.
  const node = nodeStatusNote(p);
  if (node) {
    const tone = p.status === "error" ? "red" : p.status === "starting" ? "orange" : "green";
    return { tone, label: node };
  }
  switch (p.status) {
    // Running or idle until something needs it (stopped): both are on, as the switch says.
    case "running":
    case "stopped":
      return { tone: "green", label: t("admin-logic.ext_on") };
    case "starting":
      return { tone: "orange", label: t("admin-logic.ext_starting") };
    case "needs_setup":
      return { tone: "orange", label: t("admin-logic.ext_needs_setup") };
    case "disabled":
      return { tone: "gray", label: t("admin-logic.ext_disabled") };
    case "incompatible":
      return { tone: "red", label: t("admin-logic.ext_incompatible") };
    case "error":
      return { tone: "red", label: t("admin-logic.ext_error") };
  }
  return { tone: "gray", label: t("admin-logic.ext_unknown") };
}

// extensionRunNote: whether an enabled extension's program runs right now (the details' small print); "" otherwise.
export function extensionRunNote(p: Pick<AdminExtension, "status" | "attached" | "node_status">): string {
  if (p.node_status) return ""; // the node status line already says where it runs
  if (p.attached) return attachedRunNote(p.status, p.attached);
  return p.status === "running" ? t("admin-logic.run_running") : p.status === "stopped" ? t("admin-logic.run_stopped") : "";
}

// attachedRunNote: online or offline since when, and why while it is still reconnecting (its status says no error yet).
function attachedRunNote(status: string, a: NonNullable<AdminExtension["attached"]>): string {
  if (status === "disabled") return "";
  const time = clock(a.since);
  if (!time) return status === "stopped" ? t("admin-logic.run_attached_idle") : "";
  if (a.online) return t("admin-logic.run_attached_online", { time });
  if (status === "starting") return a.error ? t("admin-logic.run_attached_reconnecting_why", { time, why: a.error }) : t("admin-logic.run_attached_reconnecting", { time });
  return t("admin-logic.run_attached_offline", { time });
}

export function extensionSourceLabel(source?: string): string {
  return ({ builtin: t("admin-logic.source_builtin"), "hub.json": "manager.json", installed: t("admin-logic.source_installed") } as Record<string, string>)[source ?? ""] ?? source ?? "";
}

export function pointLabel(point: string): string {
  return ({ connectors: t("admin-logic.point_connectors"), voice: t("admin-logic.point_voice") } as Record<string, string>)[point] ?? point;
}

// canUpgrade: only what was installed (built-in ones come with personad, hub.json's are changed there).
export function canUpgrade(p: AdminExtension): boolean {
  return p.source === "installed";
}

// canRollBack: an installed package with the version before its last upgrade still kept (a remote extension has none).
export function canRollBack(p: AdminExtension): boolean {
  return p.source === "installed" && Boolean(p.previous_version) && !(p.origin ?? "").startsWith("remote:");
}

// sourceProblem says what is wrong with a typed source before asking the hub, "" when it may be right (the hub checks
// the rest: whether the path exists, the download's hash).
export function sourceProblem(raw: string): string {
  const s = raw.trim();
  if (!s) return "";
  if (s.startsWith("http://")) return t("admin-logic.source_https_only");
  if (s.startsWith("https://")) return /#sha256=[0-9a-fA-F]{64}$/.test(s) ? "" : t("admin-logic.source_needs_sha");
  if (s.startsWith("remote:")) return /^remote:(?:(?:rpc|http)=[^,]+)(?:,(?:rpc|http)=[^,]+)?$/.test(s) ? "" : t("admin-logic.source_remote_form");
  if (/^(?:dir|file):/.test(s)) return /^(?:dir|file):(?:\/|~\/)/.test(s) ? "" : t("admin-logic.source_absolute");
  if (s.startsWith("/") || s.startsWith("~/")) return "";
  return t("admin-logic.source_unknown");
}

// uploadProblem: a file the hub would refuse, before sending it ("" when fine).
export function uploadProblem(file: { name: string; size: number }, most: number): string {
  if (!/\.(?:tar\.gz|tgz|zip)$/i.test(file.name)) return t("admin-logic.upload_type");
  if (most > 0 && file.size > most) return t("admin-logic.upload_too_large", { mb: Math.floor(most / (1 << 20)) });
  return "";
}

// ── how the extensions run (docs/extensions/extensions.md "Runtime status") ──

/** An error rate as people read it: 0%, 0.4%, 12%; "—" without calls. */
export function rateText(rate: number, calls: number): string {
  if (!calls) return "—";
  const pct = rate * 100;
  if (pct === 0) return "0%";
  if (pct < 0.1) return "<0.1%";
  return `${pct < 10 ? Number(pct.toFixed(1)) : Math.round(pct)}%`;
}

/** A latency: 0.8 ms, 140 ms, 1.2 s; "—" without one. */
export function msText(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return "—";
  if (ms < 10) return `${Number(ms.toFixed(1))} ms`;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${Number((ms / 1000).toFixed(ms < 10_000 ? 1 : 0))} s`;
}

/** The rate is worth a color: orange from 1%, red from 10% (with enough calls to mean it). */
export function rateTone(rate: number, calls: number): "red" | "orange" | "gray" {
  if (calls < 5 || rate < 0.01) return "gray";
  return rate >= 0.1 ? "red" : "orange";
}

/** A step's label on the charts: the hour and minute, with the day for the 7 days. */
export function stepLabel(iso: string, window: MetricsWindow): string {
  const d = new Date(iso);
  const two = (n: number) => String(n).padStart(2, "0");
  const hm = `${two(d.getHours())}:${two(d.getMinutes())}`;
  return window === "7d" ? t("admin-logic.step_day_hour", { month: two(d.getMonth() + 1), day: two(d.getDate()), hour: two(d.getHours()) }) : hm;
}

/** The two charts of an extension's "Activity": calls (answered and failed, stacked) and the p95 latency. */
export function metricsCharts(m: Pick<ExtensionMetrics, "series" | "window">): { calls: UIItem; latency: UIItem | null } {
  const label = (at: string) => stepLabel(at, m.window);
  const calls: UIItem = {
    type: "chart",
    kind: "bar",
    stacked: true,
    unit: t("admin-logic.unit_calls"),
    series: [
      { name: t("admin-logic.series_ok"), points: m.series.map((p) => ({ label: label(p.at), value: p.calls - p.errors })) },
      { name: t("admin-logic.series_errors"), tone: "danger", points: m.series.map((p) => ({ label: label(p.at), value: p.errors })) },
    ],
  };
  if (!m.series.some((p) => p.p95_ms !== null)) return { calls, latency: null };
  const latency: UIItem = {
    type: "chart",
    kind: "line",
    unit: "ms",
    series: [{ name: "p95", points: m.series.map((p) => ({ label: label(p.at), value: p.p95_ms ?? 0, display: msText(p.p95_ms) })) }],
  };
  return { calls, latency };
}

/** Log lines as text to copy: time, where from, the line. */
export function logText(lines: LogLine[]): string {
  return lines.map((l) => `${l.at} ${l.source === "hub" ? "[hub] " : ""}${l.text}`).join("\n");
}

/** Lines kept on the page: the latest most of them, without repeats (a stream that came back resends some). */
export function mergeLines(have: LogLine[], more: LogLine[], most = 2000): LogLine[] {
  const last = have.length ? have[have.length - 1].seq : -1;
  const out = have.concat(more.filter((l) => l.seq > last));
  return out.length > most ? out.slice(out.length - most) : out;
}
