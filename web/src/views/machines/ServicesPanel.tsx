// Lazy-loaded Services & Containers panel (#12353) for the Machines view:
// inspects user systemd/launchd services and Docker containers, shows bounded
// recent logs in a dialog, and gates start/stop/restart behind explicit UI
// confirmation with Persona self-protection guardrails.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, enc, errorText, isAbort, pastHub } from "../../lib/api";
import type { Machine } from "../../lib/types";
import { MachineTool, type MachineToolProps } from "./MachinePicker";
import { Button, copyText, cx, Dialog, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Segmented } from "../assistant/parts";

export type ServiceKind = "systemd" | "launchd" | "docker";
export type ServiceState = "running" | "stopped" | "failed" | "activating" | "unknown";
export type ServiceAction = "start" | "stop" | "restart";

export interface ServiceManagerStatus {
  kind: ServiceKind;
  available: boolean;
  unavailable?: string;
}

export interface MachineServiceItem {
  kind: ServiceKind;
  id: string;
  name: string;
  state: ServiceState;
  sub_state?: string;
  status?: string;
  description?: string;
  pid?: number;
  protected?: boolean;
  protect_note?: string;
}

export interface MachineServicesReport {
  at: number;
  os: string;
  managers: ServiceManagerStatus[];
  services: MachineServiceItem[];
  containers: MachineServiceItem[];
}

export interface MachineServiceLogsResult {
  kind: ServiceKind;
  id: string;
  lines: number;
  truncated?: boolean;
  content: string;
  source?: string;
  note?: string;
}

type CategoryFilter = "all" | "services" | "containers";
type LoadState =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "report"; report: MachineServicesReport }
  | { kind: "unavailable"; reason: "old-node" | "old-hub" | "forbidden" | "offline" }
  | { kind: "error"; text: string };

const LOG_LINE_CHOICES = [50, 100, 200, 500] as const;

const STATE_TONE: Record<ServiceState, { pill: string; dot: string }> = {
  running: { pill: "bg-green-tint text-green", dot: "bg-green" },
  failed: { pill: "bg-red-tint text-red", dot: "bg-red" },
  activating: { pill: "bg-orange-tint text-orange", dot: "bg-orange" },
  stopped: { pill: "bg-field text-ink-3", dot: "bg-line-strong" },
  unknown: { pill: "bg-field text-ink-3", dot: "bg-line-strong" },
};

function kindLabel(kind: ServiceKind): string {
  if (kind === "systemd") return t("machines-services.kind_systemd");
  if (kind === "launchd") return t("machines-services.kind_launchd");
  return t("machines-services.kind_docker");
}

function stateLabel(state: ServiceState): string {
  switch (state) {
    case "running":
      return t("machines-services.state_running");
    case "stopped":
      return t("machines-services.state_stopped");
    case "failed":
      return t("machines-services.state_failed");
    case "activating":
      return t("machines-services.state_activating");
    default:
      return t("machines-services.state_unknown");
  }
}

function actionLabel(action: ServiceAction): string {
  if (action === "start") return t("machines-services.action_start");
  if (action === "stop") return t("machines-services.action_stop");
  return t("machines-services.action_restart");
}

function classifyUnavailable(err: unknown): "old-node" | "old-hub" | "forbidden" | "offline" | null {
  if (!(err instanceof ApiError)) return null;
  if (err.status === 403) return "forbidden";
  if (err.status === 404) return "old-hub";
  if (err.status === 503) return "offline";
  if (err.status === 502 && /unknown method/i.test(err.message)) return "old-node";
  return null;
}

export function ServicesPanel(props: MachineToolProps) {
  return (
    <MachineTool {...props}>
      {(machine) => <Services activeMachine={machine} />}
    </MachineTool>
  );
}

function Services({ activeMachine }: { activeMachine?: Machine }) {
  const mid = activeMachine?.id ?? "";
  const online = !!activeMachine?.online;

  const [load, setLoad] = useState<LoadState>({ kind: "idle" });
  const [refreshing, setRefreshing] = useState(false);
  const [category, setCategory] = useState<CategoryFilter>("all");
  const [query, setQuery] = useState("");
  const seq = useRef(0);

  // Bounded log viewer dialog state.
  const [logTarget, setLogTarget] = useState<MachineServiceItem | null>(null);
  const [logLines, setLogLines] = useState<number>(100);
  const [logData, setLogData] = useState<MachineServiceLogsResult | null>(null);
  const [logError, setLogError] = useState("");
  const [logLoading, setLogLoading] = useState(false);
  const logSeq = useRef(0);

  // Explicit confirmation dialog state for start/stop/restart.
  const [confirming, setConfirming] = useState<{ item: MachineServiceItem; action: ServiceAction } | null>(null);
  const [acting, setActing] = useState(false);

  const fetchServices = useCallback(
    async (signal?: AbortSignal) => {
      if (!mid || !online) return;
      const mine = ++seq.current;
      setRefreshing(true);
      try {
        const report = await api<MachineServicesReport>(`/machines/${enc(mid)}/services`, {
          signal,
          timeout: pastHub(25_000),
        });
        if (mine !== seq.current) return;
        setLoad({
          kind: "report",
          report: {
            ...report,
            managers: report.managers ?? [],
            services: report.services ?? [],
            containers: report.containers ?? [],
          },
        });
      } catch (e) {
        if (isAbort(e) || mine !== seq.current) return;
        const why = classifyUnavailable(e);
        setLoad(why ? { kind: "unavailable", reason: why } : { kind: "error", text: errorText(e) });
      } finally {
        if (mine === seq.current) setRefreshing(false);
      }
    },
    [mid, online],
  );

  useEffect(() => {
    setLogTarget(null);
    setConfirming(null);
    if (!mid || !online) {
      seq.current++;
      setLoad({ kind: "idle" });
      setRefreshing(false);
      return;
    }
    setLoad({ kind: "loading" });
    const ctrl = new AbortController();
    void fetchServices(ctrl.signal);
    return () => ctrl.abort();
  }, [mid, online, fetchServices]);

  const fetchLogs = useCallback(
    async (item: MachineServiceItem, lines: number, signal?: AbortSignal) => {
      if (!mid) return;
      const mine = ++logSeq.current;
      setLogLoading(true);
      setLogError("");
      try {
        const qs = new URLSearchParams({ kind: item.kind, id: item.id, lines: String(lines) }).toString();
        const res = await api<MachineServiceLogsResult>(`/machines/${enc(mid)}/services/logs?${qs}`, {
          signal,
          timeout: pastHub(20_000),
        });
        if (mine !== logSeq.current) return;
        setLogData(res);
      } catch (e) {
        if (isAbort(e) || mine !== logSeq.current) return;
        setLogError(errorText(e));
      } finally {
        if (mine === logSeq.current) setLogLoading(false);
      }
    },
    [mid],
  );

  useEffect(() => {
    if (!logTarget) return;
    setLogData(null);
    const ctrl = new AbortController();
    void fetchLogs(logTarget, logLines, ctrl.signal);
    return () => ctrl.abort();
  }, [logTarget, logLines, fetchLogs]);

  const runAction = async () => {
    if (!confirming || !mid) return;
    const { item, action } = confirming;
    setActing(true);
    try {
      await api(`/machines/${enc(mid)}/services/action`, {
        method: "POST",
        json: { kind: item.kind, id: item.id, action, confirmed: true },
        timeout: pastHub(30_000),
      });
      toast(t("machines-services.action_succeeded", { action: actionLabel(action), name: item.name }), "ok");
      setConfirming(null);
      void fetchServices();
    } catch (e) {
      toast(t("machines-services.action_failed", { error: errorText(e) }), "error");
    } finally {
      setActing(false);
    }
  };

  const report = load.kind === "report" ? load.report : null;
  const allItems = useMemo(() => {
    if (!report) return [];
    const list: MachineServiceItem[] = [];
    if (category === "all" || category === "services") list.push(...report.services);
    if (category === "all" || category === "containers") list.push(...report.containers);
    const q = query.trim().toLowerCase();
    if (!q) return list;
    return list.filter(
      (it) =>
        it.name.toLowerCase().includes(q) ||
        it.id.toLowerCase().includes(q) ||
        (it.description ?? "").toLowerCase().includes(q) ||
        (it.status ?? "").toLowerCase().includes(q),
    );
  }, [report, category, query]);

  const totalCount = report ? report.services.length + report.containers.length : 0;

  return (
    <section data-testid="services-panel" aria-label={t("machines-services.title")} className="flex flex-col gap-3">
      {/* Filter, search and refresh the selected computer’s services */}
      <div className="flex flex-wrap items-center gap-2 rounded-card bg-surface px-4 py-3 shadow-card">

        <Segmented
          compact
          value={category}
          onChange={setCategory}
          label={t("machines-services.filter_label")}
          options={[
            { v: "all", label: t("machines-services.filter_all") },
            { v: "services", label: t("machines-services.filter_services") },
            { v: "containers", label: t("machines-services.filter_containers") },
          ]}
        />

        <div className="relative min-w-40 flex-1">
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label={t("machines-services.search_label")}
            placeholder={t("machines-services.search_placeholder")}
            className="h-8 w-full rounded-control bg-inset px-2.5 text-label text-ink shadow-hairline outline-none placeholder:text-ink-3"
          />
        </div>

        <Button
          tone="secondary"
          icon="retry"
          busy={refreshing}
          disabled={!online}
          testId="services-refresh"
          onClick={() => void fetchServices()}
        >
          {t("machines-services.refresh")}
        </Button>
      </div>

      {/* Manager availability notices */}
      {report && report.managers.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 px-1" data-testid="services-managers">
          {report.managers.map((mgr) => (
            <span
              key={mgr.kind}
              data-manager={mgr.kind}
              data-available={mgr.available ? "true" : "false"}
              className={cx(
                "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-submeta font-medium",
                mgr.available ? "bg-green-tint text-green" : "bg-field text-ink-3",
              )}
            >
              <span className={cx("size-1.5 rounded-full", mgr.available ? "bg-green" : "bg-ink-3")} aria-hidden />
              {mgr.available
                ? t("machines-services.manager_available", { kind: kindLabel(mgr.kind) })
                : t("machines-services.manager_unavailable", { kind: kindLabel(mgr.kind), reason: mgr.unavailable || "—" })}
            </span>
          ))}
        </div>
      )}

      {/* Main list / status card */}
      <div className="overflow-hidden rounded-card bg-surface shadow-card">
        {!online ? (
          <p className="px-4 py-4 text-meta text-ink-3" role="status">
            {t("machines-services.offline")}
          </p>
        ) : load.kind === "loading" ? (
          <div className="px-4 py-4">
            <Loading />
          </div>
        ) : load.kind === "unavailable" ? (
          <p className="px-4 py-4 text-meta text-ink-2" role="status">
            {load.reason === "old-node"
              ? t("machines-services.old_node")
              : load.reason === "old-hub"
                ? t("machines-services.old_hub")
                : load.reason === "forbidden"
                  ? t("machines-services.forbidden")
                  : t("machines-services.offline")}
          </p>
        ) : load.kind === "error" ? (
          <div className="flex flex-wrap items-center gap-2 px-4 py-3">
            <p className="min-w-0 flex-1 break-words text-meta text-red" role="alert">
              {t("machines-services.load_failed", { error: load.text })}
            </p>
            <Button tone="secondary" size="sm" icon="retry" busy={refreshing} onClick={() => void fetchServices()}>
              {t("machines-services.refresh")}
            </Button>
          </div>
        ) : allItems.length === 0 ? (
          <p className="px-4 py-4 text-meta text-ink-3" role="status">
            {totalCount === 0 ? t("machines-services.empty_all") : t("machines-services.empty_filtered")}
          </p>
        ) : (
          <ul className="divide-y divide-line" data-testid="services-list">
            {allItems.map((item) => {
              const tone = STATE_TONE[item.state] ?? STATE_TONE.unknown;
              const isRunning = item.state === "running" || item.state === "activating";
              const protectTitle = item.protect_note || t("machines-services.protected_hint");
              return (
                <li
                  key={`${item.kind}:${item.id}`}
                  data-service-row={`${item.kind}:${item.id}`}
                  className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3"
                >
                  <div className="min-w-48 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="truncate font-mono text-meta font-medium text-ink">{item.name}</span>
                      <span className="inline-flex h-5 shrink-0 items-center rounded-full bg-inset px-2 text-submeta font-medium text-ink-2 shadow-hairline">
                        {kindLabel(item.kind)}
                      </span>
                      <span
                        className={cx(
                          "inline-flex h-5 shrink-0 items-center gap-1 whitespace-nowrap rounded-full px-2 text-submeta font-medium",
                          tone.pill,
                        )}
                      >
                        <span className={cx("size-1.5 rounded-full", tone.dot)} aria-hidden />
                        {stateLabel(item.state)}
                      </span>
                      {item.protected && (
                        <span
                          title={protectTitle}
                          className="inline-flex h-5 shrink-0 items-center gap-1 whitespace-nowrap rounded-full bg-orange-tint px-2 text-submeta font-medium text-orange"
                        >
                          <Icon name="shield" size={11} />
                          {t("machines-services.protected_badge")}
                        </span>
                      )}
                    </div>
                    <p className="mt-0.5 truncate text-submeta text-ink-3">
                      {[item.status || item.sub_state, item.description].filter(Boolean).join(" · ") || item.id}
                    </p>
                  </div>

                  <div className="flex shrink-0 flex-wrap items-center gap-1.5">
                    <Button
                      tone="secondary"
                      size="sm"
                      icon="read"
                      onClick={() => {
                        setLogLines(100);
                        setLogTarget(item);
                      }}
                    >
                      {t("machines-services.action_logs")}
                    </Button>
                    {!isRunning && (
                      <Button
                        tone="secondary"
                        size="sm"
                        icon="play"
                        disabled={item.protected}
                        title={item.protected ? protectTitle : undefined}
                        onClick={() => setConfirming({ item, action: "start" })}
                      >
                        {t("machines-services.action_start")}
                      </Button>
                    )}
                    <Button
                      tone="secondary"
                      size="sm"
                      icon="retry"
                      disabled={item.protected}
                      title={item.protected ? protectTitle : undefined}
                      onClick={() => setConfirming({ item, action: "restart" })}
                    >
                      {t("machines-services.action_restart")}
                    </Button>
                    {isRunning && (
                      <Button
                        tone="quiet"
                        size="sm"
                        icon="stop"
                        disabled={item.protected}
                        title={item.protected ? protectTitle : undefined}
                        onClick={() => setConfirming({ item, action: "stop" })}
                      >
                        {t("machines-services.action_stop")}
                      </Button>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      {/* Bounded log viewer dialog */}
      <Dialog
        open={!!logTarget}
        onClose={() => setLogTarget(null)}
        width={720}
        title={logTarget ? t("machines-services.logs_title", { name: logTarget.name }) : ""}
        footer={
          <div className="flex w-full flex-wrap items-center gap-2">
            <select
              aria-label={t("machines-services.logs_lines_label")}
              value={logLines}
              onChange={(e) => setLogLines(Number(e.target.value))}
              className="h-8 rounded-control bg-surface px-2.5 pr-7 text-label text-ink shadow-hairline outline-none"
            >
              {LOG_LINE_CHOICES.map((n) => (
                <option key={n} value={n}>
                  {t("machines-services.logs_lines_option", { count: n })}
                </option>
              ))}
            </select>
            <Button
              tone="secondary"
              size="sm"
              icon="retry"
              busy={logLoading}
              onClick={() => logTarget && void fetchLogs(logTarget, logLines)}
            >
              {t("machines-services.refresh")}
            </Button>
            <Button
              tone="secondary"
              size="sm"
              icon="copy"
              disabled={!logData?.content}
              onClick={() => logData?.content && void copyText(logData.content)}
            >
              {t("machines-services.logs_copy")}
            </Button>
            <span className="flex-1" />
            <Button tone="quiet" onClick={() => setLogTarget(null)}>
              {t("ui-atoms.close")}
            </Button>
          </div>
        }
      >
        <div className="flex flex-col gap-2" data-testid="service-logs-dialog">
          {logData?.truncated && (
            <p className="rounded-[8px] bg-orange-tint px-2.5 py-1.5 text-submeta text-orange">
              {t("machines-services.logs_truncated")}
            </p>
          )}
          {logData?.note && <p className="text-submeta text-ink-3">{logData.note}</p>}
          {logLoading && !logData ? (
            <div className="flex items-center gap-2 py-4 text-meta text-ink-2" role="status">
              <Spinner />
              <span>{t("common.loading")}</span>
            </div>
          ) : logError ? (
            <p className="rounded-[8px] bg-red-tint px-3 py-2 text-meta text-red" role="alert">
              {t("machines-services.logs_failed", { error: logError })}
            </p>
          ) : logData?.content ? (
            <pre className="max-h-80 overflow-auto rounded-[8px] bg-inset p-3 font-mono text-submeta leading-relaxed whitespace-pre-wrap break-all text-ink select-text">
              {logData.content}
            </pre>
          ) : (
            <p className="py-4 text-meta text-ink-3">{t("machines-services.logs_empty")}</p>
          )}
        </div>
      </Dialog>

      {/* Confirmation dialog for lifecycle actions */}
      <Dialog
        open={!!confirming}
        onClose={() => !acting && setConfirming(null)}
        title={
          confirming
            ? t(`machines-services.confirm_title_${confirming.action}`, { name: confirming.item.name })
            : ""
        }
        footer={
          <>
            <Button tone="quiet" disabled={acting} onClick={() => setConfirming(null)}>
              {t("common.cancel")}
            </Button>
            <Button
              tone={confirming?.action === "stop" ? "danger" : "primary"}
              busy={acting}
              testId="service-action-confirm"
              onClick={() => void runAction()}
            >
              {confirming ? actionLabel(confirming.action) : ""}
            </Button>
          </>
        }
      >
        {confirming && (
          <p className="text-meta leading-relaxed text-ink-2">
            {t(`machines-services.confirm_body_${confirming.action}`, {
              name: confirming.item.name,
              kind: kindLabel(confirming.item.kind),
              machine: activeMachine?.name ?? mid,
            })}
          </p>
        )}
      </Dialog>
    </section>
  );
}
