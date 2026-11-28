// Popover-only pure helpers for subscription usage cards and the reset timeline (#13173, #13870, #13964).
// Kept in a separate module so UsagePopover's lazy chunk owns them instead of the cold first screen.
import { t as tr } from "../i18n";
import { canAliasUsageAccount, canonicalUsageProvider, isHistoricalCodexReading, windowMinutes, type UsageAccount, type UsageWindow } from "./usage-windows";

const PLAN_ACRONYMS = new Set(["api", "ai", "vip", "gpt", "llm", "mcp", "sso", "cli", "ide"]);

/** Normalize a subscription plan label for display (e.g. "max" -> "Max", "api" -> "API", "claude_max_20x" -> "Claude Max 20x")
 *  while preserving intentional mixed-case tokens such as "Pro+" or "xAI". */
export function formatPlanLabel(raw: string | undefined): string {
  const cleaned = (raw ?? "").trim().replace(/[_-]+/g, " ").replace(/\s+/g, " ");
  if (!cleaned) return "";
  return cleaned
    .split(" ")
    .map((word) => {
      const lower = word.toLowerCase();
      if (PLAN_ACRONYMS.has(lower)) return lower.toUpperCase();
      if (/^\d+x$/i.test(word)) return lower;
      const hasUpper = word !== lower;
      const isAllUpper = word === word.toUpperCase() && hasUpper;
      if (hasUpper && !isAllUpper) return word;
      return lower.charAt(0).toUpperCase() + lower.slice(1);
    })
    .join(" ");
}

/** Browser-only presentation of a sampled account. */
export type PopoverAccount = UsageAccount & { alias?: string };

/** The normalized plan to write beside the name, unless the name already says it ("Codex Pro" and plan "pro"). */
export function planNote(a: PopoverAccount): string {
  const plan = formatPlanLabel(a.plan);
  if (!plan || a.alias) return plan;
  const raw = (a.plan ?? "").trim().toLowerCase();
  const nameLower = a.name.toLowerCase();
  return !nameLower.includes(plan.toLowerCase()) && (!raw || !nameLower.includes(raw)) ? plan : "";
}

/** The longest stretch the reset timeline shows: windows resetting later are drawn to its end. */
export const TIMELINE_CAP_MS = 7 * 24 * 3600_000;

const windowSpanRank = (w: UsageWindow) => {
  const m = windowMinutes(w);
  return m === 300 ? 0 : m === 10080 ? 1 : 2 + m;
};

/** The windows of one account, grouped so related windows stay together: ungrouped plan windows first (5h, then 7d,
 *  then other lengths ascending), then explicit extension `group` blocks (preserving extension group and window order),
 *  then per-model allowances aggregated by normalized model name (5h, 7d, then other lengths within each model). */
export function sortedWindows(a: Pick<UsageAccount, "windows">): UsageWindow[] {
  const plain: UsageWindow[] = [];
  const byGroup = new Map<string, UsageWindow[]>();
  const byModel = new Map<string, UsageWindow[]>();
  for (const w of a.windows) {
    const group = w.group?.trim();
    if (group) {
      const k = group.toLowerCase();
      const list = byGroup.get(k);
      if (list) list.push(w);
      else byGroup.set(k, [w]);
      continue;
    }
    const model = w.model?.trim();
    if (model) {
      const k = model.toLowerCase();
      const list = byModel.get(k);
      if (list) list.push(w);
      else byModel.set(k, [w]);
      continue;
    }
    plain.push(w);
  }
  plain.sort((x, y) => windowSpanRank(x) - windowSpanRank(y));
  const out: UsageWindow[] = [...plain];
  for (const groupWins of byGroup.values()) out.push(...groupWins);
  for (const modelWins of byModel.values()) {
    modelWins.sort((x, y) => windowSpanRank(x) - windowSpanRank(y));
    out.push(...modelWins);
  }
  return out;
}

/** When an account's plan 7d window resets, in ms; Infinity without one (or without a reset time): those sort last. */
export function weeklyResetMs(a: Pick<UsageAccount, "windows">): number {
  const w = a.windows.find((x) => !x.model && windowMinutes(x) === 10080 && x.resets_at);
  const at = w?.resets_at ? Date.parse(w.resets_at) : NaN;
  return Number.isFinite(at) ? at : Infinity;
}

/** A card's accounts: the soonest 7d reset first; accounts without one last, then by name (case-insensitive) and id. */
export function sortForCard(accounts: UsageAccount[]): UsageAccount[] {
  return [...accounts].sort((a, b) => {
    const x = weeklyResetMs(a);
    const y = weeklyResetMs(b);
    if (x !== y) return x < y ? -1 : 1;
    return a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.name.localeCompare(b.name) || a.id.localeCompare(b.id);
  });
}

export interface ProviderGroup {
  provider: string;
  /** Sorted by `sortForCard`. */
  accounts: PopoverAccount[];
  historicalReadings: UsageAccount[];
  /** The newest `sampled_at` of the group, "" when none reads as a time. */
  sampledAt: string;
}

/** The accounts grouped by provider (account type), groups in the order the hub first listed them. */
export function groupByProvider(accounts: UsageAccount[], aliases?: ReadonlyMap<string, string>): ProviderGroup[] {
  const by = new Map<string, UsageAccount[]>();
  for (const a of accounts) {
    const provider = canonicalUsageProvider(a.provider);
    by.set(provider, [...(by.get(provider) ?? []), a]);
  }
  return [...by].map(([provider, list]) => {
    const historicalReadings: UsageAccount[] = [];
    const counted: UsageAccount[] = [];
    for (const a of list) {
      if (isHistoricalCodexReading(a)) historicalReadings.push(a);
      else counted.push(a);
    }
    const newest = counted.reduce((m, a) => Math.max(m, Date.parse(a.sampled_at) || -Infinity), -Infinity);
    const sorted = sortForCard(counted).map((a) => {
      const alias = canAliasUsageAccount(a) ? aliases?.get(a.id) : undefined;
      return alias ? { ...a, alias } : a;
    });
    return { provider, accounts: sorted, historicalReadings, sampledAt: Number.isFinite(newest) ? new Date(newest).toISOString() : "" };
  });
}

const PROVIDER_TITLES: Record<string, string> = { xai: "xAI", deepseek: "DeepSeek", kimi: "Kimi" };
/** A provider's name for a card title: its own spelling when known, else the id with a capital. */
export function providerTitle(provider: string): string {
  const id = canonicalUsageProvider(provider);
  return PROVIDER_TITLES[id] ?? id.charAt(0).toUpperCase() + id.slice(1);
}

const HOUR_MS = 3600_000;
/** The shared axis: now, then these hours from now. The first 24 hours take 45% of the width, 1 to 7 days the other 55%. */
export const TIMELINE_TICKS_HOURS = [6, 12, 24, 72, 120, 168] as const;
/** Where `ms` from now falls along the axis, 0-1 (clamped at 7 days). */
export function timelinePosition(ms: number): number {
  const h = Math.max(0, Math.min(ms, TIMELINE_CAP_MS)) / HOUR_MS;
  return h <= 24 ? (h / 24) * 0.45 : 0.45 + ((h - 24) / 144) * 0.55;
}

/** Time left as a short label: "45m", "1.5h", "2d 3h" (each language's units). */
export function remainingText(ms: number): string {
  const mins = Math.ceil(ms / 60_000);
  if (mins < 60) return tr("usage.minutes", { m: Math.max(1, mins) });
  const hours = ms / HOUR_MS;
  if (hours < 24) return tr("usage.hours", { h: Math.round(hours * 10) / 10 });
  const d = Math.floor(hours / 24);
  const h = Math.round(hours - d * 24);
  return h === 0 ? tr("usage.days", { d }) : tr("usage.days_hours", { d, h });
}
