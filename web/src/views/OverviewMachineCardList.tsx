import { memo, Suspense } from "react";
import { t } from "../i18n";
import { lazyPage } from "../lib/lazy-page";
import { alertLabel, machineAlerts } from "../lib/machine-alerts";
import { hubLabel } from "../lib/mesh";
import { href, onLinkClick } from "../lib/router";
import type { Machine, MachineMetrics } from "../lib/types";
import { cx } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { hasCurrentSample } from "./OverviewMachineSample";
import { OverviewSummaryBoundary } from "./OverviewSummaryBoundary";

const MachineMetricsSummary = lazyPage(() => import("./machines/MachineMetricsSummary").then((m) => m.MachineMetricsSummary));

/** The monitoring cards' list, loaded away from Overview's first screen. */
export function OverviewMachineCardList({ machines, agentCounts, metricsByMachine, error, offlineText }: {
  machines: Machine[];
  agentCounts?: ReadonlyMap<string, number>;
  metricsByMachine: ReadonlyMap<string, MachineMetrics>;
  error: string;
  offlineText: (machine: Machine) => string;
}) {
  const hasMonitorable = machines.some((m) => hasCurrentSample(m, metricsByMachine.get(m.id)));
  return (
    <>
      {error && (
        <p role="alert" className="col-span-full flex items-start gap-2 rounded-[8px] bg-red-tint px-3 py-2 text-meta text-red">
          <Icon name="warning" size={14} className="mt-px shrink-0" />
          <span className="min-w-0 break-words">{hasMonitorable ? t("machines.refresh_failed_stale", { error, seconds: 10 }) : t("machines.load_failed", { error })}</span>
        </p>
      )}
      {machines.map((m) => (
        <OverviewMachineCard
          key={m.id}
          m={m}
          count={agentCounts ? (agentCounts.get(m.id) ?? 0) : undefined}
          hub={hubLabel(m, machines)}
          metrics={metricsByMachine.get(m.id)}
          offlineText={offlineText}
        />
      ))}
    </>
  );
}

const OverviewMachineCard = memo(function OverviewMachineCard({ m, count, hub, metrics, offlineText }: {
  m: Machine;
  count?: number;
  hub: string;
  metrics?: MachineMetrics;
  offlineText: (machine: Machine) => string;
}) {
  const detail = { name: "machine" as const, mid: m.id };
  const line = m.online ? [count !== undefined && t("home.machine_agents", { count }), m.system?.name].filter(Boolean).join(" · ") || t("home.online") : offlineText(m);
  const monitored = hasCurrentSample(m, metrics);
  const disk = machineAlerts({ machine: m, metrics }).find(a => a.kind === "disk");
  const card = (
    <a href={href(detail)} onClick={(e) => onLinkClick(e, detail)} className={cx("block rounded-card bg-surface shadow-card transition-colors hover:bg-hover", monitored ? "h-full" : "self-start")}>
      <span className={cx("flex items-center gap-3 px-3", monitored ? "pt-2.5" : "py-2.5")}>
        <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-[8px]", m.online ? "bg-field text-ink-2" : "bg-inset text-ink-3")}>
          <Icon name="machine" size={15} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="truncate text-meta font-medium text-ink">{m.name}</span>
            {hub && <span className="shrink-0 truncate text-submeta text-ink-3">{hub}</span>}
          </span>
          <span className="block truncate text-submeta text-ink-3" title={!m.online && m.error && line !== m.error ? m.error : undefined} data-machine-line>{line}</span>
        </span>
        <span className={cx("size-2 shrink-0 rounded-full", m.online ? "bg-green" : "bg-line-strong")} title={m.online ? t("home.online") : t("home.offline")} />
      </span>
      {monitored && (
        <OverviewSummaryBoundary>
          <Suspense fallback={null}><MachineMetricsSummary machine={m} metrics={metrics} /></Suspense>
        </OverviewSummaryBoundary>
      )}
    </a>
  );
  if (!disk) return card;
  const reclaim = { ...detail, subview: "storage", mode: "reclaim" } as const;
  return <div className="flex flex-col gap-1">
    {card}
    <a href={href(reclaim)} onClick={e => onLinkClick(e, reclaim)} className="flex min-h-11 items-center gap-2 rounded-control bg-field px-3 text-label text-ink">
      <Icon name="archive" size={14} className="text-orange" />{alertLabel(disk)}
    </a>
  </div>;
});
