import { useId, type ReactNode } from "react";
import { t } from "../../i18n";
import { level, load, memPercent, percent, rate, runs, usedOfTotal, type Level } from "../../lib/machine-detail";
import { formatTokens, totalTokens } from "../../lib/tokens";
import type { Machine, MachineMetrics, MetricPoint } from "../../lib/types";
import { cx } from "../../ui/atoms";

const known = (value: number | undefined): number | undefined => typeof value === "number" && Number.isFinite(value) ? value : undefined;
const positive = (value: number | undefined): number | undefined => value !== undefined && value > 0 && Number.isFinite(value) ? value : undefined;

type SparkSeries = { color: string; read: (point: MetricPoint) => number | undefined };
const CPU: SparkSeries[] = [{ color: "var(--accent)", read: (p) => p.cpu }];
const NETWORK: SparkSeries[] = [{ color: "var(--orange)", read: (p) => p.rx }, { color: "var(--ai)", read: (p) => p.tx }];

/** The same three summaries as the phone. No extra requests: all history comes from /metrics. */
export function MachineMetricsSummary({ machine, metrics }: { machine: Machine; metrics?: MachineMetrics }) {
  // Either source can already know about a disconnect while the other still has its previous sample.
  if (!machine.online || metrics?.online === false) return null;
  const latest = metrics?.latest;
  const points = metrics?.points ?? [];
  const total = positive(latest?.mem_total) ?? positive(metrics?.system?.memory) ?? positive(machine.system?.memory);
  const cpu = known(latest?.cpu);
  const memory = memPercent(known(latest?.mem_used), total);
  const now = Date.now() / 1000;
  const usage = metrics?.usage;
  const minute = Math.floor(now / 60) * 60;
  const tokenPoints = usage?.recent.map((cpu, i) => ({ t: minute - (usage.recent.length - 1 - i) * 60, cpu })) ?? [];
  // The metrics are one row when each column holds the widest value it can show ("↓ 1023 MB/s", 97.7 px at the standard
  // text size: a rate keeps four digits up to 1023 before the next unit, as the app's MetricFormat), so 98 px a column
  // with its 12 px gaps: 26.75rem for four, 19.875rem for three. Narrower, a card at 1024 px cut the network's unit
  // ("↓ 2.58 M…") and read as another number. Below that two columns, never three with one left alone on a second row
  // (a card at 1280 px, 428 px inside, keeps its four in a row).
  return (
    <div className="@container px-4 py-3">
      <div className={cx("grid grid-cols-2 gap-3", usage ? "@min-[26.75rem]:grid-cols-4" : "@min-[19.875rem]:grid-cols-3")} data-testid="machine-metrics-summary">
        <MetricCell label="CPU" value={percent(cpu)} detail={t("machines.load_value", { value: load(known(latest?.load)) })} severity={level(cpu, 75, 90)}>
          <Spark points={points} end={now} />
        </MetricCell>
        <MetricCell label={t("machines.memory")} value={percent(memory)} detail={usedOfTotal(known(latest?.mem_used), total)} severity={level(memory, 80, 90)}>
          <Spark points={points} end={now} series={[{ color: "var(--green)", read: (p) => memPercent(known(p.mem), total) }]} label={t("machines.memory_last_hour")} />
        </MetricCell>
        <MetricCell label={t("machines.network")} value={`↓ ${rate(known(latest?.net_rx))}`} detail={`↑ ${rate(known(latest?.net_tx))}`} valueColor="var(--orange)" detailColor="var(--ai)">
          <Spark points={points} end={now} series={NETWORK} minimumMax={1} label={t("machines.network_last_hour")} />
        </MetricCell>
        {usage && (
          <MetricCell label={t("machines-usage.tokens")} value={t("machines-usage.per_minute_value", { n: formatTokens(usage.per_minute) })} detail={t("machines-usage.today_value", { n: formatTokens(totalTokens(usage.today)) })}>
            <Spark points={tokenPoints} end={now} minimumMax={1} label={t("machines-usage.tokens_last_hour")} />
          </MetricCell>
        )}
      </div>
    </div>
  );
}

function MetricCell({ label, value, detail, severity = "normal", valueColor, detailColor, children }: {
  label: string; value: string; detail: string; severity?: Level; valueColor?: string; detailColor?: string; children: ReactNode;
}) {
  return (
    <div className="min-w-0">
      <div className="text-xs text-ink-3">{label}</div>
      <div className={cx("mt-0.5 truncate text-base leading-6 font-medium tabular-nums", severity === "critical" ? "text-red" : severity === "warning" ? "text-orange" : "text-ink")} title={value}>
        {valueColor ? <><span style={{ color: valueColor }}>{value.slice(0, 1)}</span>{value.slice(1)}</> : value}
        {severity !== "normal" && <span className="sr-only"> {t(severity === "critical" ? "machines-machinedetail.critical" : "machines-machinedetail.high")}</span>}
      </div>
      <div className="min-h-4 truncate text-xs text-ink-3 tabular-nums" title={detail}>
        {detailColor ? <><span style={{ color: detailColor }}>{detail.slice(0, 1)}</span>{detail.slice(1)}</> : detail}
      </div>
      <div className="mt-2">{children}</div>
    </div>
  );
}

/** One lightweight SVG per summary, with no axes or hover layer. Runs never bridge missing samples. */
export function Spark({ points, series = CPU, minimumMax = 100, label = t("machines.cpu_last_hour"), end = Date.now() / 1000 }: {
  points: MetricPoint[]; series?: SparkSeries[]; minimumMax?: number; label?: string; end?: number;
}) {
  const fill = `metric-fill-${useId().replace(/:/g, "")}`;
  const W = 160, H = 40;
  const lines = series.map((s) => ({ ...s, runs: runs(points, s.read) }));
  if (!lines.some((s) => s.runs.some((r) => r.length > 1))) {
    return <div className="flex h-10 items-center text-xs text-ink-3" aria-label={`${label}: ${t("machines.no_history")}`}>{t("machines.no_history")}</div>;
  }
  const top = Math.max(minimumMax, ...lines.flatMap((s) => s.runs.flatMap((r) => r.map((p) => p.v))));
  const x = (time: number) => ((time - (end - 3600)) / 3600) * W;
  const y = (value: number) => H - 2 - (Math.max(0, value) / top) * (H - 4);
  const wash = series.length === 1;
  return (
    <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-10 w-full overflow-hidden" role="img" aria-label={label}>
      {wash && <defs><linearGradient id={fill} x1="0" x2="0" y1="0" y2="1"><stop offset="0" stopColor={series[0].color} stopOpacity="0.12" /><stop offset="1" stopColor={series[0].color} stopOpacity="0" /></linearGradient></defs>}
      {lines.map((s, i) => <g key={i} data-series={i}>{s.runs.filter((r) => r.length > 1).map((r, j) => {
        const line = r.map((p) => `${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join(" ");
        return <g key={j}>
          {wash && <polygon points={`${x(r[0].t)},${H} ${line} ${x(r[r.length - 1].t)},${H}`} fill={`url(#${fill})`} />}
          <polyline points={line} fill="none" stroke={s.color} strokeWidth={1.5} vectorEffect="non-scaling-stroke" strokeLinejoin="round" strokeLinecap="round" />
        </g>;
      })}</g>)}
    </svg>
  );
}
