// A personal Agent's allowance as a charge (lib/agents.ts quotaGauge), as the account pages show "70% left": a ring
// around its avatar, filled clockwise from the top, and a small battery with the percentage beside its name. Green while
// there is plenty, amber at 20% or less, red when it is used up, muted grey for an old reading. Nothing is drawn without
// a figure, so a Lark chat or an Agent whose source reports no quota looks as before.
import type { ReactNode } from "react";
import { formatDate, t } from "../../i18n";
import { quotaGauge, type AgentQuota, type QuotaGauge, type QuotaTone } from "../../lib/agents";
import { cx } from "../../ui/atoms";

const TONE: Record<QuotaTone, string> = { ok: "var(--green)", low: "var(--orange)", exhausted: "var(--red)", stale: "var(--ink-3)" };

/** "70% left", with when it resets when the source says. */
export function quotaText(g: QuotaGauge): string {
  const left = g.tone === "exhausted" ? t("lib-agents.quota_exhausted") : t("lib-agents.quota_left", { percent: g.percent });
  const stale = g.tone === "stale" ? ` · ${t("lib-agents.quota_stale")}` : "";
  return g.resetsAt ? `${left}${stale} · ${t("lib-agents.quota_resets", { time: formatDate(g.resetsAt, { dateStyle: "medium", timeStyle: "short" }) })}` : `${left}${stale}`;
}

/** The avatar `children` (of `size` px) with the charge drawn around it, outside its box: the layout does not move. */
export function QuotaRing({ quota, size, children, className }: { quota?: AgentQuota; size: number; children: ReactNode; className?: string }) {
  const g = quotaGauge(quota);
  if (!g) return <>{children}</>;
  const stroke = size >= 32 ? 2.5 : 2;
  const gap = 1.5;
  const box = size + 2 * (stroke + gap);
  const r = (box - stroke) / 2;
  const c = 2 * Math.PI * r;
  return (
    <span className={cx("relative flex shrink-0", className)} style={{ width: size, height: size }} data-quota={g.tone} title={quotaText(g)}>
      {children}
      <svg aria-hidden width={box} height={box} viewBox={`0 0 ${box} ${box}`} className="pointer-events-none absolute" style={{ left: -(stroke + gap), top: -(stroke + gap), opacity: g.tone === "stale" ? 0.6 : 1 }}>
        {/* used up: the whole track goes red, faint, so the empty ring still says so */}
        <circle cx={box / 2} cy={box / 2} r={r} fill="none" stroke={g.tone === "exhausted" ? TONE.exhausted : "var(--line)"} strokeOpacity={g.tone === "exhausted" ? 0.45 : 1} strokeWidth={stroke} />
        {g.level > 0 && (
          <circle cx={box / 2} cy={box / 2} r={r} fill="none" stroke={TONE[g.tone]} strokeWidth={stroke} strokeLinecap="round"
            strokeDasharray={`${c * g.level} ${c}`} transform={`rotate(-90 ${box / 2} ${box / 2})`} />
        )}
      </svg>
    </span>
  );
}

/** A small battery filled to the charge, and the percentage. */
export function QuotaBattery({ quota, className }: { quota?: AgentQuota; className?: string }) {
  const g = quotaGauge(quota);
  if (!g) return null;
  const text = quotaText(g);
  return (
    <span role="img" aria-label={text} title={text} data-quota={g.tone} className={cx("inline-flex shrink-0 items-center gap-1 tabular-nums text-submeta", g.tone === "stale" && "opacity-60", className)} style={{ color: g.tone === "ok" ? "var(--ink-3)" : TONE[g.tone] }}>
      <svg aria-hidden width="17" height="9" viewBox="0 0 17 9">
        <rect x="0.5" y="0.5" width="14" height="8" rx="2" fill="none" stroke="currentColor" strokeOpacity="0.55" />
        <rect x="15" y="3" width="1.6" height="3" rx="0.6" fill="currentColor" fillOpacity="0.55" />
        {g.level > 0 && <rect x="2" y="2" width={Math.max(1, 11 * g.level)} height="5" rx="1" fill={TONE[g.tone]} />}
      </svg>
      {g.percent}%
    </span>
  );
}
