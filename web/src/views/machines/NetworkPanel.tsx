// On-demand read-only network and proxy connectivity diagnostics panel (#12351).
// Lazy-loaded via lazyPage from Machines.tsx; never runs background polling.
import { useCallback, useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, enc, errorText, isAbort, pastHub } from "../../lib/api";
import { ago } from "../../lib/format";
import type { Machine } from "../../lib/types";
import { MachineTool, type MachineToolProps } from "./MachinePicker";
import { Button, cx, EASE, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

export interface MachineNetInterface {
  name: string;
  up: boolean;
  addresses: string[];
}

export interface MachineProxyConfig {
  http_proxy?: string;
  https_proxy?: string;
  all_proxy?: string;
  no_proxy?: string;
  active: boolean;
}

export interface MachineEndpointProbe {
  id: string;
  name: string;
  target: string;
  protocol: string;
  via_proxy: boolean;
  reachable: boolean;
  status_code?: number;
  latency_ms: number;
  error?: string;
}

export interface MachineNetworkReport {
  checked_at: number;
  interfaces: MachineNetInterface[];
  proxy: MachineProxyConfig;
  probes: MachineEndpointProbe[];
}

type UnavailableReason = "old-node" | "old-hub" | "forbidden" | "offline";

type LoadState =
  | { kind: "loading" }
  | { kind: "report"; report: MachineNetworkReport }
  | { kind: "unavailable"; why: UnavailableReason }
  | { kind: "error"; text: string };

const HUB_NETWORK_TIMEOUT_MS = 15_000;

function classifyUnavailable(err: unknown): UnavailableReason | null {
  if (!(err instanceof ApiError)) return null;
  if (err.status === 403) return "forbidden";
  if (err.status === 404 && !err.isJson) return "old-hub";
  if (err.status === 409 && /node_outdated/i.test(err.message)) return "old-node";
  if (/unknown method/i.test(err.message)) return "old-node";
  if (err.status === 503 || /offline|not connected/i.test(err.message)) return "offline";
  return null;
}

export function NetworkPanel(props: MachineToolProps) {
  return (
    <MachineTool {...props}>
      {(machine) => <Network machine={machine} />}
    </MachineTool>
  );
}

function Network({ machine }: { machine?: Machine }) {
  const mid = machine?.id ?? "";
  const online = !!machine?.online;

  const [load, setLoad] = useState<LoadState>({ kind: "loading" });
  const [busy, setBusy] = useState(false);
  const seq = useRef(0);

  const check = useCallback(
    async (targetId: string, signal?: AbortSignal) => {
      if (!targetId) return;
      const mine = ++seq.current;
      setBusy(true);
      try {
        const raw = await api<MachineNetworkReport>(`/machines/${enc(targetId)}/network`, {
          signal,
          timeout: pastHub(HUB_NETWORK_TIMEOUT_MS),
        });
        if (mine !== seq.current) return;
        setLoad({
          kind: "report",
          report: {
            checked_at: raw.checked_at,
            interfaces: raw.interfaces ?? [],
            proxy: raw.proxy ?? { active: false },
            probes: raw.probes ?? [],
          },
        });
      } catch (e) {
        if (isAbort(e) || mine !== seq.current) return;
        const why = classifyUnavailable(e);
        setLoad(why ? { kind: "unavailable", why } : { kind: "error", text: errorText(e) });
      } finally {
        if (mine === seq.current) setBusy(false);
      }
    },
    [],
  );

  // On-demand single check when opening the panel or switching computers; zero background polling.
  useEffect(() => {
    if (!mid || !online) {
      seq.current++;
      setLoad({ kind: "loading" });
      setBusy(false);
      return;
    }
    setLoad({ kind: "loading" });
    const ctrl = new AbortController();
    void check(mid, ctrl.signal);
    return () => ctrl.abort();
  }, [mid, online, check]);

  if (!machine) return null;

  const report = load.kind === "report" ? load.report : null;
  const okCount = report ? report.probes.filter((p) => p.reachable).length : 0;
  const totalCount = report ? report.probes.length : 0;
  const allReachable = report !== null && totalCount > 0 && okCount === totalCount;
  const checkedAgo = report?.checked_at ? ago(new Date(report.checked_at * 1000).toISOString()) : "";

  const proxyRows: readonly [string, string | undefined][] = report
    ? [
        ["HTTP_PROXY", report.proxy.http_proxy],
        ["HTTPS_PROXY", report.proxy.https_proxy],
        ["ALL_PROXY", report.proxy.all_proxy],
        ["NO_PROXY", report.proxy.no_proxy],
      ]
    : [];

  return (
    <div className="flex flex-col gap-4" data-testid="network-panel" style={{ animation: `fade-up 320ms ${EASE} both` }}>
      {/* Diagnostic report */}
      <section className="overflow-hidden rounded-card bg-surface shadow-card">
        <div className="flex flex-wrap items-start gap-3 px-4 py-3.5">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-field text-ink-2">
            <Icon name="web" size={16} />
          </span>
          <div className="min-w-0 flex-1">
            <h2 className="text-card-title font-semibold text-ink">{t("machines-network.title")}</h2>
            <p className="mt-0.5 text-label text-ink-3">{t("machines-network.subtitle")}</p>
          </div>
          {online && (
            <Button tone="secondary" icon="retry" busy={busy} onClick={() => void check(mid)}>
              {t("machines-network.recheck")}
            </Button>
          )}
        </div>

        {/* Body status or summary strip */}
        {!online ? (
          <p className="border-t border-line px-4 py-3 text-meta text-ink-3" role="status">
            {t("machines-network.offline")}
          </p>
        ) : load.kind === "loading" ? (
          <p className="flex items-center gap-2 border-t border-line px-4 py-3 text-meta text-ink-2" role="status">
            <Spinner />
            {t("machines-network.checking")}
          </p>
        ) : load.kind === "unavailable" ? (
          <p className="border-t border-line px-4 py-3 text-meta text-ink-3" role="status">
            {load.why === "old-node"
              ? t("machines-network.old_node")
              : load.why === "old-hub"
                ? t("machines-network.old_hub")
                : load.why === "forbidden"
                  ? t("machines-network.forbidden")
                  : t("machines-network.went_offline")}
          </p>
        ) : load.kind === "error" ? (
          <div className="flex flex-wrap items-center gap-2 border-t border-line px-4 py-3">
            <p className="min-w-0 flex-1 break-words text-meta text-red" role="alert">
              {t("machines-network.error", { error: load.text })}
            </p>
            <Button tone="secondary" icon="retry" busy={busy} onClick={() => void check(mid)}>
              {t("machines-network.recheck")}
            </Button>
          </div>
        ) : (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-t border-line px-4 py-2.5" role="status">
            <span
              className={cx(
                "flex size-5 shrink-0 items-center justify-center rounded-full",
                allReachable ? "bg-green-tint text-green" : "bg-orange-tint text-orange",
              )}
            >
              <Icon name={allReachable ? "check" : "warning"} size={12} stroke={2.4} />
            </span>
            <span className="text-meta font-medium text-ink tabular-nums">
              {t("machines-network.summary_reachable", { ok: okCount, total: totalCount })}
            </span>
            <span
              className={cx(
                "inline-flex h-5 items-center rounded-full px-2 text-submeta font-medium whitespace-nowrap",
                report!.proxy.active ? "bg-orange-tint text-orange" : "bg-field text-ink-2",
              )}
            >
              {report!.proxy.active ? t("machines-network.proxy_active") : t("machines-network.proxy_direct")}
            </span>
            {checkedAgo && (
              <span className="text-submeta text-ink-3">{t("machines-network.checked_at", { when: checkedAgo })}</span>
            )}
          </div>
        )}
      </section>

      {report && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {/* Endpoint probes */}
          <section className="col-span-full overflow-hidden rounded-card bg-surface shadow-card" data-testid="network-probes">
            <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
              <h3 className="text-meta font-medium text-ink">{t("machines-network.probes_heading")}</h3>
              <span className="text-submeta text-ink-3 tabular-nums">
                {t("machines-network.summary_reachable", { ok: okCount, total: totalCount })}
              </span>
            </div>
            {report.probes.length === 0 ? (
              <p className="px-4 py-3 text-meta text-ink-3">{t("machines-network.probes_empty")}</p>
            ) : (
              <ul className="divide-y divide-line">
                {report.probes.map((p) => (
                  <li key={p.id} data-probe={p.id} data-reachable={String(p.reachable)} className="flex flex-wrap items-start gap-3 px-4 py-2.5">
                    <span
                      className={cx("mt-1.5 size-2 shrink-0 rounded-full", p.reachable ? "bg-green" : "bg-red")}
                      aria-hidden
                    />
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="text-meta font-medium text-ink">{p.name}</span>
                        <span className="inline-flex h-5 items-center rounded-full bg-field px-1.5 font-mono text-submeta uppercase text-ink-2">
                          {p.protocol}
                        </span>
                        <span className="inline-flex h-5 items-center rounded-full bg-inset px-1.5 text-submeta text-ink-3 shadow-hairline">
                          {p.via_proxy ? t("machines-network.via_proxy") : t("machines-network.via_direct")}
                        </span>
                      </div>
                      <p className="mt-0.5 truncate font-mono text-submeta text-ink-3" title={p.target}>
                        {p.target}
                      </p>
                      {!p.reachable && p.error && (
                        <p className="mt-1 break-words font-mono text-submeta text-red">{p.error}</p>
                      )}
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      {p.status_code ? (
                        <span className="font-mono text-submeta text-ink-3 tabular-nums">
                          {t("machines-network.http_status", { code: p.status_code })}
                        </span>
                      ) : null}
                      <span className="font-mono text-submeta text-ink-2 tabular-nums">
                        {t("machines-network.latency_ms", { ms: p.latency_ms })}
                      </span>
                      <span
                        className={cx(
                          "inline-flex h-5 items-center whitespace-nowrap rounded-full px-2 text-submeta font-medium",
                          p.reachable ? "bg-green-tint text-green" : "bg-red-tint text-red",
                        )}
                      >
                        {p.reachable ? t("machines-network.status_reachable") : t("machines-network.status_unreachable")}
                      </span>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </section>

          {/* Effective proxy configuration */}
          <section className="overflow-hidden rounded-card bg-surface shadow-card" data-testid="network-proxy">
            <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
              <h3 className="text-meta font-medium text-ink">{t("machines-network.proxy_heading")}</h3>
              <span
                className={cx(
                  "inline-flex h-5 items-center rounded-full px-2 text-submeta font-medium whitespace-nowrap",
                  report.proxy.active ? "bg-orange-tint text-orange" : "bg-field text-ink-3",
                )}
              >
                {report.proxy.active ? t("machines-network.proxy_active") : t("machines-network.proxy_direct")}
              </span>
            </div>
            <dl className="divide-y divide-line">
              {proxyRows.map(([envName, value]) => (
                <div key={envName} className="flex items-baseline gap-3 px-4 py-2">
                  <dt className="w-28 shrink-0 font-mono text-submeta text-ink-3">{envName}</dt>
                  <dd className={cx("min-w-0 flex-1 break-all text-label select-text", value ? "font-mono text-ink" : "text-ink-3")}>
                    {value || t("machines-network.proxy_not_set")}
                  </dd>
                </div>
              ))}
            </dl>
            <p className="flex items-center gap-1.5 border-t border-line px-4 py-2 text-submeta text-ink-3">
              <Icon name="shield" size={12} className="shrink-0" />
              <span>{t("machines-network.proxy_redacted_note")}</span>
            </p>
          </section>

          {/* Non-loopback network interfaces */}
          <section className="overflow-hidden rounded-card bg-surface shadow-card" data-testid="network-interfaces">
            <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
              <h3 className="text-meta font-medium text-ink">{t("machines-network.interfaces_heading")}</h3>
              <span className="text-submeta text-ink-3 tabular-nums">{report.interfaces.length}</span>
            </div>
            {report.interfaces.length === 0 ? (
              <p className="px-4 py-3 text-meta text-ink-3">{t("machines-network.interfaces_empty")}</p>
            ) : (
              <ul className="divide-y divide-line">
                {report.interfaces.map((iface) => (
                  <li key={iface.name} className="flex items-start gap-3 px-4 py-2.5">
                    <span className="w-24 shrink-0 truncate font-mono text-label font-medium text-ink" title={iface.name}>
                      {iface.name}
                    </span>
                    <div className="min-w-0 flex-1 space-y-0.5">
                      {(iface.addresses ?? []).map((addr) => (
                        <div key={addr} className="break-all font-mono text-submeta text-ink-2 select-text">
                          {addr}
                        </div>
                      ))}
                    </div>
                    <span
                      className={cx(
                        "inline-flex h-5 shrink-0 items-center whitespace-nowrap rounded-full px-2 text-submeta font-medium",
                        iface.up ? "bg-green-tint text-green" : "bg-field text-ink-3",
                      )}
                    >
                      {iface.up ? t("machines-network.interface_up") : t("machines-network.interface_down")}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </div>
      )}
    </div>
  );
}
