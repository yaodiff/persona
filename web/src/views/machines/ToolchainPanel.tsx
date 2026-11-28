// On-demand read-only development toolchain and local environment panel for the Machines view (#12350).
// Loaded lazily via lazyPage in Machines.tsx so it adds zero bytes to the cold first screen or idle tier.
// Never polls in the background: fetches once per selected machine on demand and only re-probes on Refresh.
import { useCallback, useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, enc, errorText, isAbort, pastHub } from "../../lib/api";
import { ago } from "../../lib/format";
import type { Machine } from "../../lib/types";
import { MachineTool, type MachineToolProps } from "./MachinePicker";
import { Button, cx, EASE, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

export interface ToolchainEntry {
  id: string;
  category: "core" | "agent" | string;
  found: boolean;
  version?: string;
  path?: string;
  resolved_path?: string;
  capabilities?: string[];
  status?: string;
  note?: string;
}

export interface ToolchainDockerInfo {
  installed: boolean;
  client_version?: string;
  server_version?: string;
  socket_path?: string;
  socket_accessible: boolean;
  status: string;
  note?: string;
}

export interface ToolchainGHAuth {
  installed: boolean;
  version?: string;
  authenticated: boolean;
  hosts?: string[];
  status: string;
  note?: string;
}

export interface ToolchainGPU {
  name: string;
  vendor?: string;
  vram_mb?: number;
  driver?: string;
  unified?: boolean;
}

export interface ToolchainPower {
  has_battery: boolean;
  battery_percent?: number;
  charging?: boolean;
  source?: string;
  sleep_state?: string;
}

export interface ToolchainHardware {
  chip?: string;
  gpus?: ToolchainGPU[];
  power?: ToolchainPower;
}

export interface ToolchainEnvSummary {
  shell?: string;
  path_entries?: string[];
  version_managers?: string[];
  has_custom_go_path?: boolean;
  proxy_configured?: boolean;
}

export interface MachineToolchainReport {
  collected_at: string;
  cached: boolean;
  cache_ttl_sec: number;
  duration_ms: number;
  os: string;
  arch: string;
  tools: ToolchainEntry[];
  agent_clis: ToolchainEntry[];
  docker?: ToolchainDockerInfo;
  github_cli?: ToolchainGHAuth;
  hardware?: ToolchainHardware;
  environment: ToolchainEnvSummary;
}

type UnavailableReason = "old_node" | "old_hub" | "forbidden";

type LoadState =
  | { kind: "idle" }
  | { kind: "loading"; mid: string }
  | { kind: "ready"; mid: string; report: MachineToolchainReport }
  | { kind: "unavailable"; mid: string; reason: UnavailableReason }
  | { kind: "error"; mid: string; message: string };

const TOOLCHAIN_TIMEOUT_MS = 20_000;
const UNKNOWN_METHOD_RE = /\bunknown method(?::\s*|\s+)machine\.toolchain\b/i;

function statusTone(status: string | undefined): { pill: string; dot: string } {
  switch (status) {
    case "ready":
    case "authenticated":
      return { pill: "bg-green-tint text-green", dot: "bg-green" };
    case "unauthenticated":
    case "daemon_unreachable":
    case "permission_denied":
      return { pill: "bg-orange-tint text-orange", dot: "bg-orange" };
    case "error":
      return { pill: "bg-red-tint text-red", dot: "bg-red" };
    default:
      return { pill: "bg-field text-ink-3", dot: "bg-line-strong" };
  }
}

function statusLabel(status: string | undefined): string {
  switch (status) {
    case "ready":
      return t("machines-toolchain.status_ready");
    case "authenticated":
      return t("machines-toolchain.status_authenticated");
    case "unauthenticated":
      return t("machines-toolchain.status_unauthenticated");
    case "daemon_unreachable":
      return t("machines-toolchain.status_daemon_unreachable");
    case "permission_denied":
      return t("machines-toolchain.status_permission_denied");
    case "error":
      return t("machines-toolchain.status_error");
    default:
      return t("machines-toolchain.status_missing");
  }
}

function capabilityLabel(cap: string): string {
  switch (cap) {
    case "authenticated":
      return t("machines-toolchain.cap_authenticated");
    case "daemon_accessible":
      return t("machines-toolchain.cap_daemon_accessible");
    case "symlink":
      return t("machines-toolchain.cap_symlink");
    default:
      return cap;
  }
}

function formatVram(vramMb: number): string {
  if (vramMb >= 1024) {
    const gb = Math.round((vramMb / 1024) * 10) / 10;
    return t("machines-toolchain.hw_vram_gb", { gb: String(gb) });
  }
  return t("machines-toolchain.hw_vram_mb", { mb: String(vramMb) });
}

function classifyUnavailable(err: unknown): UnavailableReason | null {
  if (!(err instanceof ApiError)) return null;
  if (err.status === 403) return "forbidden";
  if (err.status === 404 && !err.isJson) return "old_hub";
  if (err.code === "node_outdated") return "old_node";
  if (UNKNOWN_METHOD_RE.test(err.message)) return "old_node";
  return null;
}

export function ToolchainPanel(props: MachineToolProps) {
  return (
    <MachineTool {...props}>
      {(machine) => <Toolchain activeMachine={machine} />}
    </MachineTool>
  );
}

function Toolchain({ activeMachine }: { activeMachine?: Machine }) {
  const activeMid = activeMachine?.id ?? "";

  const [state, setState] = useState<LoadState>({ kind: "idle" });
  const [refreshing, setRefreshing] = useState<{ seq: number; mid: string } | null>(null);
  const [showPath, setShowPath] = useState(false);
  const seq = useRef(0);
  const activeMidRef = useRef(activeMid);
  activeMidRef.current = activeMid;
  const inFlight = useRef<{ seq: number; mid: string; ctrl: AbortController } | null>(null);

  const abortInFlight = useCallback(() => {
    inFlight.current?.ctrl.abort();
    inFlight.current = null;
  }, []);

  const fetchReport = useCallback(
    async (mid: string, refresh: boolean) => {
      abortInFlight();
      const ctrl = new AbortController();
      const mine = ++seq.current;
      inFlight.current = { seq: mine, mid, ctrl };
      if (refresh) {
        setRefreshing({ seq: mine, mid });
      } else {
        setRefreshing(null);
        setState({ kind: "loading", mid });
      }
      try {
        const query = refresh ? "?refresh=1" : "";
        const report = await api<MachineToolchainReport>(`/machines/${enc(mid)}/toolchain${query}`, {
          signal: ctrl.signal,
          timeout: pastHub(TOOLCHAIN_TIMEOUT_MS),
        });
        if (ctrl.signal.aborted || mine !== seq.current || activeMidRef.current !== mid) return;
        setState({
          kind: "ready",
          mid,
          report: {
            ...report,
            tools: report.tools ?? [],
            agent_clis: report.agent_clis ?? [],
            environment: report.environment ?? {},
          },
        });
      } catch (err) {
        if (ctrl.signal.aborted || isAbort(err) || mine !== seq.current || activeMidRef.current !== mid) return;
        const unavailable = classifyUnavailable(err);
        if (unavailable) {
          setState({ kind: "unavailable", mid, reason: unavailable });
        } else {
          setState({ kind: "error", mid, message: errorText(err) });
        }
      } finally {
        if (inFlight.current?.seq === mine) {
          inFlight.current = null;
        }
        setRefreshing((prev) => (prev?.seq === mine ? null : prev));
      }
    },
    [abortInFlight],
  );

  useEffect(() => {
    setShowPath(false);
    if (!activeMachine || !activeMachine.online) {
      seq.current++;
      abortInFlight();
      setState({ kind: "idle" });
      setRefreshing(null);
      return;
    }
    void fetchReport(activeMachine.id, false);
    return () => {
      seq.current++;
      abortInFlight();
      setRefreshing(null);
    };
  }, [activeMachine?.id, activeMachine?.online, fetchReport, abortInFlight]);

  if (!activeMachine) {
    return (
      <section data-testid="toolchain-panel" className="rounded-card bg-surface px-4 py-5 shadow-card">
        <p className="text-meta text-ink-3">{t("machines-toolchain.no_machines")}</p>
      </section>
    );
  }

  const activeState: LoadState = state.kind === "idle" || state.mid === activeMid ? state : { kind: "idle" };
  const isRefreshing = refreshing !== null && refreshing.mid === activeMid;
  const report = activeState.kind === "ready" ? activeState.report : null;
  const readyCore = report ? report.tools.filter((x) => x.found).length : 0;
  const readyAgents = report ? report.agent_clis.filter((x) => x.found).length : 0;

  return (
    <div data-testid="toolchain-panel" className="flex flex-col gap-3" style={{ animation: `fade-up 320ms ${EASE} both` }}>
      {/* Report header */}
      <section className="rounded-card bg-surface px-4 py-3.5 shadow-card">
        <div className="flex flex-wrap items-start gap-3">
          <div className="min-w-0 flex-1">
            <h2 className="text-card-title font-semibold text-ink">{t("machines-toolchain.title")}</h2>
            <p className="mt-0.5 text-submeta text-ink-3">{t("machines-toolchain.subtitle")}</p>
          </div>
          {activeMachine?.online && (
            <Button
              tone="secondary"
              icon="retry"
              busy={isRefreshing || activeState.kind === "loading"}
              onClick={() => activeMachine && void fetchReport(activeMachine.id, true)}
            >
              {isRefreshing ? t("machines-toolchain.refreshing") : t("machines-toolchain.refresh")}
            </Button>
          )}
        </div>

        {report && (
          <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-line pt-2.5 text-submeta text-ink-3">
            <span className="font-mono text-ink-2">
              {report.os}/{report.arch}
            </span>
            <span>{report.cached ? t("machines-toolchain.meta_cached", { seconds: report.cache_ttl_sec }) : t("machines-toolchain.meta_live")}</span>
            <span className="tabular-nums">{t("machines-toolchain.meta_duration", { ms: report.duration_ms })}</span>
            {report.collected_at && (
              <span>{t("machines-toolchain.meta_collected", { when: ago(report.collected_at) || report.collected_at })}</span>
            )}
          </div>
        )}
      </section>

      {!activeMachine?.online ? (
        <section className="rounded-card bg-surface px-4 py-4 shadow-card">
          <p className="text-meta text-ink-3">{t("machines-toolchain.offline")}</p>
        </section>
      ) : activeState.kind === "loading" ? (
        <section className="rounded-card bg-surface px-4 py-5 shadow-card">
          <p className="flex items-center gap-2 text-meta text-ink-2" role="status">
            <Spinner />
            {t("machines-toolchain.loading")}
          </p>
        </section>
      ) : activeState.kind === "unavailable" ? (
        <section className="rounded-card bg-surface px-4 py-4 shadow-card">
          <p className="text-meta text-ink-2">
            {activeState.reason === "forbidden"
              ? t("machines-toolchain.unavailable_forbidden")
              : activeState.reason === "old_hub"
                ? t("machines-toolchain.unavailable_old_hub")
                : t("machines-toolchain.unavailable_old_node")}
          </p>
        </section>
      ) : activeState.kind === "error" ? (
        <section className="flex flex-wrap items-center gap-3 rounded-card bg-surface px-4 py-3.5 shadow-card">
          <p role="alert" className="min-w-0 flex-1 break-words text-meta text-red">
            {t("machines-toolchain.error_load", { error: activeState.message })}
          </p>
          <Button tone="secondary" icon="retry" busy={isRefreshing} onClick={() => void fetchReport(activeMachine.id, true)}>
            {t("machines-toolchain.refresh")}
          </Button>
        </section>
      ) : report ? (
        <>
          {/* Summary KPI strip */}
          <div className="grid grid-cols-2 gap-2.5 lg:grid-cols-4">
            <SummaryTile
              label={t("machines-toolchain.summary_core")}
              value={t("machines-toolchain.ratio", { ready: readyCore, total: report.tools.length })}
            />
            <SummaryTile
              label={t("machines-toolchain.summary_agents")}
              value={t("machines-toolchain.ratio", { ready: readyAgents, total: report.agent_clis.length })}
            />
            <SummaryTile
              label={t("machines-toolchain.summary_docker")}
              value={statusLabel(report.docker?.status ?? "missing")}
              status={report.docker?.status}
            />
            <SummaryTile
              label={t("machines-toolchain.summary_gh")}
              value={statusLabel(report.github_cli?.status ?? "missing")}
              status={report.github_cli?.status}
            />
          </div>

          {/* Core development tools & Agent CLIs */}
          <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
            <ToolListCard title={t("machines-toolchain.section_core")} entries={report.tools} testId="toolchain-core-list" />
            <ToolListCard title={t("machines-toolchain.section_agents")} entries={report.agent_clis} testId="toolchain-agent-list" />
          </div>

          {/* Runtime integrations & Hardware */}
          <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
            <RuntimeIntegrationsCard docker={report.docker} gh={report.github_cli} />
            {report.hardware && <HardwareCard hardware={report.hardware} />}
          </div>

          {/* Environment summary */}
          <EnvironmentCard env={report.environment} showPath={showPath} onTogglePath={() => setShowPath((v) => !v)} />
        </>
      ) : null}
    </div>
  );
}

function SummaryTile({ label, value, status }: { label: string; value: string; status?: string }) {
  const tone = status ? statusTone(status) : null;
  return (
    <div className="rounded-card bg-surface px-3.5 py-3 shadow-card">
      <div className="text-submeta text-ink-3">{label}</div>
      <div className="mt-1 flex items-center gap-1.5">
        {tone && <span className={cx("size-2 shrink-0 rounded-full", tone.dot)} aria-hidden />}
        <span className="truncate text-meta font-semibold text-ink tabular-nums">{value}</span>
      </div>
    </div>
  );
}

function ToolListCard({ title, entries, testId }: { title: string; entries: ToolchainEntry[]; testId: string }) {
  return (
    <section data-testid={testId} className="overflow-hidden rounded-card bg-surface shadow-card">
      <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
        <h3 className="text-meta font-medium text-ink">{title}</h3>
        <span className="text-submeta text-ink-3 tabular-nums">
          {t("machines-toolchain.ratio", { ready: entries.filter((e) => e.found).length, total: entries.length })}
        </span>
      </div>
      <ul className="divide-y divide-line">
        {entries.map((entry) => {
          const tone = statusTone(entry.status);
          const displayPath = entry.resolved_path || entry.path;
          const symlinked = Boolean(entry.path && entry.resolved_path && entry.path !== entry.resolved_path);
          return (
            <li key={entry.id} data-tool={entry.id} className="flex items-start gap-3 px-4 py-2.5">
              <span className={cx("mt-2 size-1.5 shrink-0 rounded-full", tone.dot)} aria-hidden />
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                  <span className="font-mono text-meta font-medium text-ink">{entry.id}</span>
                  {entry.version && <span className="font-mono text-submeta text-ink-2 tabular-nums">v{entry.version}</span>}
                </div>
                {displayPath && (
                  <p className="mt-0.5 truncate font-mono text-submeta text-ink-3" title={displayPath}>
                    {displayPath}
                  </p>
                )}
                {symlinked && entry.path && (
                  <p className="truncate font-mono text-submeta text-ink-3" title={entry.path}>
                    {t("machines-toolchain.resolved_from", { path: entry.path })}
                  </p>
                )}
                {entry.note && <p className="mt-0.5 break-words text-submeta text-ink-3">{entry.note}</p>}
                {entry.capabilities && entry.capabilities.length > 0 && (
                  <div className="mt-1 flex flex-wrap gap-1">
                    {entry.capabilities.map((cap) => (
                      <span key={cap} className="inline-flex h-5 items-center rounded-full bg-field px-2 text-submeta text-ink-2">
                        {capabilityLabel(cap)}
                      </span>
                    ))}
                  </div>
                )}
              </div>
              <span className={cx("inline-flex h-5 shrink-0 items-center whitespace-nowrap rounded-full px-2 text-submeta font-medium", tone.pill)}>
                {statusLabel(entry.status)}
              </span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

function RuntimeIntegrationsCard({ docker, gh }: { docker?: ToolchainDockerInfo; gh?: ToolchainGHAuth }) {
  const dockerTone = statusTone(docker?.status ?? "missing");
  const ghTone = statusTone(gh?.status ?? "missing");
  return (
    <section data-testid="toolchain-runtime-card" className="overflow-hidden rounded-card bg-surface shadow-card">
      <div className="border-b border-line px-4 py-2.5">
        <h3 className="text-meta font-medium text-ink">{t("machines-toolchain.section_runtime")}</h3>
      </div>
      <div className="divide-y divide-line">
        <div className="px-4 py-3">
          <div className="flex items-center justify-between gap-2">
            <span className="text-meta font-medium text-ink">{t("machines-toolchain.summary_docker")}</span>
            <span className={cx("inline-flex h-5 items-center whitespace-nowrap rounded-full px-2 text-submeta font-medium", dockerTone.pill)}>
              {statusLabel(docker?.status ?? "missing")}
            </span>
          </div>
          <dl className="mt-2 grid grid-cols-1 gap-1 text-submeta">
            {docker?.client_version && (
              <div className="flex items-baseline justify-between gap-2">
                <dt className="text-ink-3">{t("machines-toolchain.docker_client")}</dt>
                <dd className="font-mono text-ink-2">{docker.client_version}</dd>
              </div>
            )}
            {docker?.server_version && (
              <div className="flex items-baseline justify-between gap-2">
                <dt className="text-ink-3">{t("machines-toolchain.docker_server")}</dt>
                <dd className="font-mono text-ink-2">{docker.server_version}</dd>
              </div>
            )}
            {docker?.socket_path && (
              <div className="flex items-baseline justify-between gap-2">
                <dt className="shrink-0 text-ink-3">{t("machines-toolchain.docker_socket")}</dt>
                <dd className="min-w-0 truncate font-mono text-ink-2" title={docker.socket_path}>
                  {docker.socket_path}
                </dd>
              </div>
            )}
          </dl>
          {docker?.note && <p className="mt-1 text-submeta text-ink-3">{docker.note}</p>}
        </div>

        <div className="px-4 py-3">
          <div className="flex items-center justify-between gap-2">
            <span className="text-meta font-medium text-ink">{t("machines-toolchain.summary_gh")}</span>
            <span className={cx("inline-flex h-5 items-center whitespace-nowrap rounded-full px-2 text-submeta font-medium", ghTone.pill)}>
              {statusLabel(gh?.status ?? "missing")}
            </span>
          </div>
          <div className="mt-2 flex items-baseline justify-between gap-2 text-submeta">
            <span className="text-ink-3">{t("machines-toolchain.gh_hosts")}</span>
            <span className="font-mono text-ink-2">
              {gh?.hosts && gh.hosts.length > 0 ? gh.hosts.join(", ") : t("machines-toolchain.gh_no_hosts")}
            </span>
          </div>
          {gh?.note && <p className="mt-1 text-submeta text-ink-3">{gh.note}</p>}
        </div>
      </div>
    </section>
  );
}

function HardwareCard({ hardware }: { hardware: ToolchainHardware }) {
  const power = hardware.power;
  return (
    <section data-testid="toolchain-hardware-card" className="overflow-hidden rounded-card bg-surface shadow-card">
      <div className="border-b border-line px-4 py-2.5">
        <h3 className="text-meta font-medium text-ink">{t("machines-toolchain.section_hardware")}</h3>
      </div>
      <div className="divide-y divide-line">
        {hardware.chip && (
          <div className="flex items-baseline justify-between gap-3 px-4 py-2.5 text-submeta">
            <span className="text-ink-3">{t("machines-toolchain.hw_chip")}</span>
            <span className="text-right text-meta font-medium text-ink">{hardware.chip}</span>
          </div>
        )}
        {hardware.gpus && hardware.gpus.length > 0 && (
          <div className="px-4 py-2.5">
            <div className="text-submeta text-ink-3">{t("machines-toolchain.hw_gpus")}</div>
            <ul className="mt-1.5 space-y-1.5">
              {hardware.gpus.map((gpu, idx) => (
                <li key={`${gpu.name}-${idx}`} className="flex flex-wrap items-center justify-between gap-2 text-meta">
                  <span className="font-medium text-ink">{gpu.name}</span>
                  <span className="flex flex-wrap items-center gap-1.5 text-submeta text-ink-2">
                    {gpu.unified ? (
                      <span className="rounded-full bg-field px-2 py-0.5">{t("machines-toolchain.hw_unified")}</span>
                    ) : gpu.vram_mb ? (
                      <span className="font-mono tabular-nums">{formatVram(gpu.vram_mb)}</span>
                    ) : null}
                    {gpu.driver && <span className="font-mono text-ink-3">{t("machines-toolchain.hw_driver", { driver: gpu.driver })}</span>}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {power && power.has_battery && (
          <div className="px-4 py-2.5">
            <div className="text-submeta text-ink-3">{t("machines-toolchain.hw_power")}</div>
            <div className="mt-1 flex flex-wrap items-center gap-2 text-meta text-ink">
              {power.battery_percent !== undefined && (
                <span className="font-mono font-semibold tabular-nums">
                  {t("machines-toolchain.hw_battery_pct", { pct: power.battery_percent })}
                </span>
              )}
              {power.charging !== undefined && (
                <span className="rounded-full bg-field px-2 py-0.5 text-submeta text-ink-2">
                  {power.charging ? t("machines-toolchain.hw_charging") : t("machines-toolchain.hw_discharging")}
                </span>
              )}
              {power.source && power.source !== "unknown" && (
                <span className="text-submeta text-ink-3">
                  {power.source === "ac" ? t("machines-toolchain.hw_source_ac") : t("machines-toolchain.hw_source_battery")}
                </span>
              )}
              {power.sleep_state && (
                <span className="font-mono text-submeta text-ink-3">
                  {t("machines-toolchain.hw_sleep", { state: power.sleep_state })}
                </span>
              )}
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

function EnvironmentCard({
  env,
  showPath,
  onTogglePath,
}: {
  env: ToolchainEnvSummary;
  showPath: boolean;
  onTogglePath: () => void;
}) {
  const pathEntries = env.path_entries ?? [];
  const managers = env.version_managers ?? [];
  return (
    <section data-testid="toolchain-env-card" className="overflow-hidden rounded-card bg-surface shadow-card">
      <div className="border-b border-line px-4 py-2.5">
        <h3 className="text-meta font-medium text-ink">{t("machines-toolchain.section_env")}</h3>
      </div>
      <dl className="grid grid-cols-1 divide-y divide-line md:grid-cols-2 md:divide-y-0">
        <div className="flex items-baseline justify-between gap-3 border-b border-line px-4 py-2.5 md:border-r">
          <dt className="text-submeta text-ink-3">{t("machines-toolchain.env_shell")}</dt>
          <dd className="font-mono text-meta text-ink">{env.shell || "—"}</dd>
        </div>
        <div className="flex items-baseline justify-between gap-3 border-b border-line px-4 py-2.5">
          <dt className="text-submeta text-ink-3">{t("machines-toolchain.env_managers")}</dt>
          <dd className="text-right font-mono text-submeta text-ink-2">
            {managers.length > 0 ? managers.join(", ") : t("machines-toolchain.env_none")}
          </dd>
        </div>
        <div className="flex items-baseline justify-between gap-3 border-b border-line px-4 py-2.5 md:border-r md:border-b-0">
          <dt className="text-submeta text-ink-3">{t("machines-toolchain.env_proxy")}</dt>
          <dd className="text-submeta text-ink-2">
            {env.proxy_configured ? t("machines-toolchain.env_set") : t("machines-toolchain.env_unset")}
          </dd>
        </div>
        <div className="flex items-baseline justify-between gap-3 px-4 py-2.5">
          <dt className="text-submeta text-ink-3">{t("machines-toolchain.env_custom_go")}</dt>
          <dd className="text-submeta text-ink-2">
            {env.has_custom_go_path ? t("machines-toolchain.env_yes") : t("machines-toolchain.env_no")}
          </dd>
        </div>
      </dl>
      {pathEntries.length > 0 && (
        <div className="border-t border-line">
          <button
            type="button"
            aria-expanded={showPath}
            onClick={onTogglePath}
            className="flex w-full items-center gap-1.5 px-4 py-2 text-left text-label text-ink-2 transition-colors hover:bg-hover hover:text-ink"
          >
            <Icon name={showPath ? "chevron" : "chevronRight"} size={13} />
            <span>
              {showPath
                ? t("machines-toolchain.env_path_hide")
                : t("machines-toolchain.env_path_show", { count: pathEntries.length })}
            </span>
          </button>
          {showPath && (
            <ul className="space-y-1 border-t border-line bg-inset/50 px-4 py-2.5 font-mono text-submeta text-ink-2">
              {pathEntries.map((dir) => (
                <li key={dir} className="truncate" title={dir}>
                  {dir}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </section>
  );
}
