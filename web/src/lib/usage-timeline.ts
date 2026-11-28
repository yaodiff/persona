// Popover-only timeline, prefix-stripping, and pause helpers: kept out of usage-windows.ts so the
// cold first screen (UsageBars) never pays for popover logic (src/startup-preload.ts FIRST_SCREEN_BUDGET).
import { providerTitle, sortedWindows, timelinePosition, type PopoverAccount, type ProviderGroup } from "./usage-popover";
import {
  canonicalUsageProvider,
  isHistoricalCodexReading,
  protectReserve,
  remainingPercent,
  windowMinutes,
  type UsageAccount,
  type UsageWindow,
} from "./usage-windows";

const roundPct = (p: number) => Math.round(p * 1e6) / 1e6;

/** Distance `h = r - R` between a window's remaining quota and its protection reserve (0 when unprotected). */
export function windowHeadroom(w: Pick<UsageWindow, "used_percent" | "protect_percent">): number {
  return roundPct(remainingPercent(w.used_percent) - (protectReserve(w.protect_percent) ?? 0));
}

/** True when a window has a protection reserve and its remaining quota has reached or entered `[0, R]` (`r <= R`). */
export function isWindowPaused(w: Pick<UsageWindow, "used_percent" | "protect_percent">): boolean {
  const reserve = protectReserve(w.protect_percent);
  return reserve !== null && roundPct(remainingPercent(w.used_percent) - reserve) <= 0;
}

/** True when any plan window (or any window when an account has only per-model windows) is paused at its protection reserve. */
export function isAccountPaused(a: Pick<UsageAccount, "windows">): boolean {
  const plan = a.windows.filter((w) => !w.model);
  const scope = plan.length ? plan : a.windows;
  return scope.some(isWindowPaused);
}

const escapeRegExp = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** Strip a redundant leading provider-name prefix from an account name for compact timeline labels
 *  (e.g. "Codex #alpha" -> "#alpha", "Antigravity · Gemini" -> "Gemini"). Keeps the original name if nothing remains. */
export function stripProviderPrefix(name: string, provider: string): string {
  const trimmed = name.trim();
  if (!trimmed) return "";
  const seen = new Set<string>();
  const prefixes = [providerTitle(provider), canonicalUsageProvider(provider), provider.trim()]
    .filter((p) => {
      const k = p.toLowerCase();
      if (!k || seen.has(k)) return false;
      seen.add(k);
      return true;
    })
    .sort((a, b) => b.length - a.length);
  for (const prefix of prefixes) {
    const match = new RegExp(`^${escapeRegExp(prefix)}(?:(?:\\s*[·•:\\-–—/]\\s*|\\s+)|(?=#))(.+)$`, "i").exec(trimmed);
    const rest = match?.[1].trim();
    if (rest) return rest;
  }
  return trimmed;
}

export interface TimelineLaneModel {
  model: string;
  resetsAt: string;
  remainingMs: number;
}
export interface TimelineLane {
  key: string;
  label: string;
  /** A window longer than six hours (the weekly one): the blue capsule; a short one is the teal one. */
  long: boolean;
  /** Where the capsule ends along the axis, 0-1. */
  end: number;
  /** Time left, in ms (the end may be beyond the axis; `end` is then 1). */
  remainingMs: number;
  resetsAt: string;
  /** Plan-level reset time when a lane merges a plan window alongside per-model windows of the same length. */
  planResetsAt?: string;
  /** Per-model entries merged into this window-type lane (sorted soonest first). Empty for plan-only windows. */
  models: TimelineLaneModel[];
  /** A window in this lane has reached its protection reserve (`isWindowPaused`): its end dot is orange. Judged per
   *  lane, so a healthy 5h lane keeps its own colour while the 7d one is paused. */
  paused: boolean;
}
export interface TimelineRow {
  account: PopoverAccount;
  /** Window-type lanes with a reset time still ahead, 5h first. */
  lanes: TimelineLane[];
}
export interface UnifiedTimelineRow extends TimelineRow {
  provider: string;
  /** Account name with redundant leading provider prefix removed. */
  displayName: string;
  /** True when any active window on this account is paused at or inside its protection reserve. */
  paused: boolean;
}

/** Build merged reset-timeline lanes for one account: windows of the same window kind (e.g. multiple 5h models)
 *  collapse into a single lane taking the earliest reset time while preserving every model in `models`. */
export function accountTimelineLanes(account: UsageAccount, now: number): TimelineLane[] {
  interface Bucket {
    key: string;
    label: string;
    hasPlanLabel: boolean;
    long: boolean;
    order: number;
    earliestAt: number;
    resetsAt: string;
    planAt?: number;
    planResetsAt?: string;
    models: TimelineLaneModel[];
    paused: boolean;
  }
  if (isHistoricalCodexReading(account)) return [];
  const byKind = new Map<string, Bucket>();
  for (const w of sortedWindows(account)) {
    const at = w.resets_at ? Date.parse(w.resets_at) : NaN;
    if (!Number.isFinite(at) || at <= now) continue;
    const mins = windowMinutes(w);
    const kindKey = mins > 0 ? String(mins) : w.label.trim().toLowerCase();
    const order = mins === 300 ? 0 : mins === 10080 ? 1 : 2 + mins;
    let bucket = byKind.get(kindKey);
    if (!bucket) {
      bucket = {
        key: `${kindKey}:${w.label}`,
        label: w.label,
        hasPlanLabel: !w.model,
        long: mins > 360,
        order,
        earliestAt: at,
        resetsAt: w.resets_at as string,
        models: [],
        paused: false,
      };
      byKind.set(kindKey, bucket);
    } else {
      if (at < bucket.earliestAt) {
        bucket.earliestAt = at;
        bucket.resetsAt = w.resets_at as string;
      }
      if (!w.model && !bucket.hasPlanLabel) {
        bucket.label = w.label;
        bucket.hasPlanLabel = true;
      }
    }
    if (isWindowPaused(w)) bucket.paused = true;
    const model = w.model?.trim();
    const group = w.group?.trim();
    const title = w.title?.trim();
    if (model) {
      bucket.models.push({ model, resetsAt: w.resets_at as string, remainingMs: at - now });
    } else if (group || title) {
      const label = group && title && group.toLowerCase() !== title.toLowerCase() ? `${group} · ${title}` : (title || group)!;
      bucket.models.push({ model: label, resetsAt: w.resets_at as string, remainingMs: at - now });
    } else if (bucket.planAt === undefined || at < bucket.planAt) {
      bucket.planAt = at;
      bucket.planResetsAt = w.resets_at as string;
    }
  }
  return [...byKind.values()]
    .sort((a, b) => a.order - b.order || a.label.localeCompare(b.label))
    .map((b) => {
      const remainingMs = b.earliestAt - now;
      const models = b.models.sort((x, y) => x.remainingMs - y.remainingMs || x.model.localeCompare(y.model));
      return {
        key: b.key,
        label: b.label,
        long: b.long,
        end: timelinePosition(remainingMs),
        remainingMs,
        resetsAt: b.resetsAt,
        ...(b.planResetsAt && models.length ? { planResetsAt: b.planResetsAt } : {}),
        models,
        paused: b.paused,
      };
    });
}

/** The rows of one provider's reset timeline: one per account in the order given, merging multi-model entries of the
 *  same window type into one lane taking the earliest reset time. Accounts with no lane have no row. */
export function providerTimeline(accounts: UsageAccount[], now = Date.now()): TimelineRow[] {
  const rows: TimelineRow[] = [];
  for (const account of accounts) {
    const lanes = accountTimelineLanes(account, now);
    if (lanes.length) rows.push({ account, lanes });
  }
  return rows;
}

/** Unified reset timeline across all provider groups in card order + account order within each provider. */
export function unifiedTimeline(groups: readonly ProviderGroup[], now = Date.now()): UnifiedTimelineRow[] {
  const rows: UnifiedTimelineRow[] = [];
  for (const g of groups) {
    for (const account of g.accounts) {
      const lanes = accountTimelineLanes(account, now);
      if (!lanes.length) continue;
      rows.push({
        provider: g.provider,
        account,
        displayName: account.alias ?? stripProviderPrefix(account.name, g.provider),
        paused: isAccountPaused(account),
        lanes,
      });
    }
  }
  return rows;
}
