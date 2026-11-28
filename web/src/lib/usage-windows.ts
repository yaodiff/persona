// Subscription rate-limit windows (#2432, docs/server/usage-windows.md): GET /api/v1/usage/windows lists the accounts the
// machines' agents are signed in to with how much of each window (5h, 7d …) is used. Pure helpers here cover
// countdowns, sample age and what the sidebar in views/UsageBars.tsx shows.
import { useSyncExternalStore } from "react";
import { formatDate, formatRelative, t as tr } from "../i18n";
import { api, ApiError } from "./api";

export interface UsageWindow {
  label: string;
  window_minutes: number;
  used_percent: number;
  resets_at?: string;
  /** An allowance a provider keeps for one model next to the plan's own (#2595); absent for the plan's window. */
  model?: string;
  /** Optional group heading from a usage extension; windows sharing a group render together under one heading. */
  group?: string;
  /** Optional row display name from a usage extension. */
  title?: string;
  /** Optional short remark under the window row from a usage extension. */
  note?: string;
  /** Where scheduling stops using this window's account, 0-100; it can differ per account and per window. Absent
   *  when the hub (older, or no such rule) does not say: nothing is drawn then (#13173). */
  protect_percent?: number;
}
/** A prepaid balance (#11825): the provider's own decimal amount and currency, never turned into a percentage. */
export interface UsageBalance {
  amount: string;
  currency: string;
}
export interface UsageAccount {
  id: string;
  provider: string;
  name: string;
  plan?: string;
  windows: UsageWindow[];
  /** A remaining amount, for a provider with no rate-limit window (the official DeepSeek launcher). Such an account
   *  may have no windows; a genuine zero is `amount: "0.00"`, not a missing balance. */
  balance?: UsageBalance;
  sampled_at: string;
  stale?: boolean;
  /** When it turns stale if nothing newer comes (#1905): kept numbers are judged again against it (`liveAccounts`). */
  stale_at?: string;
  /** Where the numbers came from (#2595): an agent's files on one of the computers, and/or account-pool gateways by name. */
  local?: boolean;
  /** Present only for values read from a saved Codex session. Older hubs omit the field. */
  reading_source?: "rollout";
  sources?: string[];
  /** The computers (by name) that read it (#1907); an older hub only says `local`. */
  machines?: string[];
  /** Why a gateway's numbers are old: "error" (it could not read this account just now) or, when its own last read
   *  failed, "rejected" (the key was turned away), "unreachable" or "failed" (docs/server/usage-windows.md). */
  problem?: string;
  /** Optional summary line from a usage extension (plain text in the user's language). */
  headline?: string;
  /** Optional status or explanation from a usage extension (e.g. cooldown or why no windows are drawn). */
  notice?: string;
}

/** Samples older than this are dropped (a source that went quiet for days says nothing about now). */
export const SAMPLE_MAX_AGE_MS = 3 * 24 * 3600_000;
/** From this used percentage a bar turns to the caution colour (kept for ultracode quota warnings). */
export const WARN_PERCENT = 80;
/** Remaining headroom thresholds (percentage points above protection reserve R): green >= 20, orange >= 5, red < 5. */
export const REMAINING_OK_HEADROOM = 20;
export const REMAINING_WARN_HEADROOM = 5;
const POLL_MS = 60_000;

export type RemainingTone = "green" | "orange" | "red";

const clampPercent = (p: number) => (Number.isFinite(p) ? Math.min(100, Math.max(0, p)) : 0);
const roundPct = (p: number) => Math.round(p * 1e6) / 1e6;

/** Remaining quota share `r = 100 - used`, clamped to [0, 100]. */
export function remainingPercent(usedPercent: number): number {
  return roundPct(100 - clampPercent(usedPercent));
}

/** Protection reserve `R = 100 - protect_percent` on the remaining-quota scale, or null when unprotected. */
export function protectReserve(protectPercent: number | undefined): number | null {
  if (typeof protectPercent !== "number" || !Number.isFinite(protectPercent) || protectPercent < 0 || protectPercent > 100) return null;
  return roundPct(100 - protectPercent);
}

/** Semantic tone from remaining percentage `r` and protection reserve `R` (0 when unprotected),
 *  classified by headroom `h = r - R`: green (--green) when h >= 20, orange (--orange) when 5 <= h < 20, red (--red) when h < 5. */
export function remainingTone(remaining: number, reserve = 0): RemainingTone {
  const r = Number.isFinite(remaining) ? remaining : 0;
  const R = Number.isFinite(reserve) && reserve > 0 ? reserve : 0;
  const h = roundPct(r - R);
  if (h < REMAINING_WARN_HEADROOM) return "red";
  if (h < REMAINING_OK_HEADROOM) return "orange";
  return "green";
}

/** Distance `h = r - R` between remaining quota and protection reserve, plus pause/timeline helpers live in
 *  `lib/usage-timeline.ts` so popover-only code stays off the first screen (FIRST_SCREEN_BUDGET). */

export interface TightestWindow {
  remaining: number;
  reserve: number;
  headroom: number;
  paused: boolean;
}

function windowTightness(w: UsageWindow): TightestWindow {
  const remaining = remainingPercent(w.used_percent);
  const rawReserve = protectReserve(w.protect_percent);
  const reserve = rawReserve ?? 0;
  const headroom = roundPct(remaining - reserve);
  const paused = rawReserve !== null && headroom <= 0;
  return { remaining, reserve, headroom, paused };
}

function tightestOf(windows: readonly UsageWindow[]): TightestWindow {
  let best = windowTightness(windows[0]);
  for (let i = 1; i < windows.length; i++) {
    const cur = windowTightness(windows[i]);
    if (
      cur.headroom < best.headroom ||
      (cur.headroom === best.headroom && (cur.remaining < best.remaining || (cur.remaining === best.remaining && cur.paused && !best.paused)))
    ) {
      best = cur;
    }
  }
  return best;
}

/** Most constrained window of an account by headroom `h = r - R` (smallest first, then smallest `r`).
 *  Per-model windows do not override plan windows while plan windows exist. When an account has only
 *  per-model pools (no plan windows), each pool is constrained by its own tightest window and the account
 *  takes the pool with the most headroom so one exhausted model pool does not hide healthy sibling pools. */
export function tightestRemaining(a: Pick<UsageAccount, "windows">): TightestWindow {
  if (!a.windows.length) return { remaining: 100, reserve: 0, headroom: 100, paused: false };
  const plan = a.windows.filter((w) => !w.model?.trim());
  if (plan.length) return tightestOf(plan);
  const byModel = new Map<string, UsageWindow[]>();
  for (const w of a.windows) {
    const k = w.model!.trim().toLowerCase();
    const list = byModel.get(k);
    if (list) list.push(w);
    else byModel.set(k, [w]);
  }
  let best: TightestWindow | null = null;
  for (const pool of byModel.values()) {
    const cur = tightestOf(pool);
    if (
      !best ||
      cur.headroom > best.headroom ||
      (cur.headroom === best.headroom && (cur.remaining > best.remaining || (cur.remaining === best.remaining && !cur.paused && best.paused)))
    ) {
      best = cur;
    }
  }
  return best!;
}

/** The window's length in minutes from its label ("5h", "7d", "45m"); the given minutes when the label is not one. */
export function windowMinutes(w: Pick<UsageWindow, "label" | "window_minutes">): number {
  const m = /^(\d+(?:\.\d+)?)\s*([mhdw])$/i.exec(w.label.trim());
  if (m) return Math.round(Number(m[1]) * { m: 1, h: 60, d: 1440, w: 10080 }[m[2].toLowerCase() as "m" | "h" | "d" | "w"]);
  return w.window_minutes > 0 ? w.window_minutes : 0;
}

type Countdown = { kind: "due" | "clock" | "span"; text: string };

/** When a window starts over: the span ("45m", "2d 3h", "5d" with no hours left over; each language's own units, "2 天 3 小时"),
 *  the local clock time when that is today, or "due". null without a time. */
function countdownOf(resetsAt: string | undefined, now: number): Countdown | null {
  const at = resetsAt ? Date.parse(resetsAt) : NaN;
  if (!Number.isFinite(at)) return null;
  const ms = at - now;
  if (ms <= 0) return { kind: "due", text: tr("usage.due") };
  const mins = Math.ceil(ms / 60_000);
  if (mins < 60) return { kind: "span", text: tr("usage.minutes", { m: mins }) };
  if (new Date(at).toDateString() === new Date(now).toDateString()) return { kind: "clock", text: formatDate(at, { timeStyle: "short" }) };
  const hours = Math.floor(mins / 60);
  const d = Math.floor(hours / 24);
  const h = hours % 24;
  return { kind: "span", text: d === 0 ? tr("usage.hours", { h: hours }) : h === 0 ? tr("usage.days", { d }) : tr("usage.days_hours", { d, h }) };
}

/** When a window starts over, as it reads in the popover: "45m" within the hour, the clock time when it is today,
 *  otherwise "2d 3h" (days and hours, "5d" without a zero hour). "" without a time; "Resets soon" when it is due. */
export function countdown(resetsAt: string | undefined, now = Date.now()): string {
  return countdownOf(resetsAt, now)?.text ?? "";
}

/** The countdown as a phrase: "Resets in 55m", "Resets in 2d 3h", but a clock time reads "Resets at 1:05 PM". */
export function resetLabel(resetsAt: string | undefined, now = Date.now()): string {
  const c = countdownOf(resetsAt, now);
  if (!c) return "";
  if (c.kind === "due") return c.text;
  return c.kind === "clock" ? tr("usage.reset_at", { time: c.text }) : tr("usage.reset_in", { countdown: c.text });
}

/** How long ago a sample was taken ("12 seconds ago"); coarser past a minute. "" for a time that is not one. */
export function sampleAge(iso: string, now = Date.now()): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const s = Math.max(0, Math.round((now - t) / 1000));
  const ago = (n: number, unit: Intl.RelativeTimeFormatUnit) => formatRelative(-n, unit, { numeric: "always" });
  if (s < 60) return ago(s, "second");
  if (s < 3600) return ago(Math.floor(s / 60), "minute");
  if (s < 86400) return ago(Math.floor(s / 3600), "hour");
  return ago(Math.floor(s / 86400), "day");
}

/** The accounts worth showing: samples within three days, windows that have not reset, accounts that still have one;
 *  each stale once its `stale_at` has passed, however long ago the hub said it (#1905: the last answer is kept while
 *  refreshes fail, and its own stale mark was the hub's at that moment). */
export function liveAccounts(accounts: UsageAccount[], now = Date.now()): UsageAccount[] {
  const out: UsageAccount[] = [];
  for (const a of accounts) {
    const sampled = Date.parse(a.sampled_at);
    if (Number.isFinite(sampled) && now - sampled > SAMPLE_MAX_AGE_MS) continue;
    const windows = a.windows.filter((w) => !w.resets_at || Date.parse(w.resets_at) > now);
    const staleAt = a.stale_at ? Date.parse(a.stale_at) : NaN;
    const stale = Boolean(a.stale) || (Number.isFinite(staleAt) && now >= staleAt);
    if (windows.length || isBalanceAccount(a) || Boolean(a.notice?.trim() || a.headline?.trim())) out.push({ ...a, windows, ...(stale ? { stale } : {}) });
  }
  return out;
}

/** A prepaid-balance account (#11825): it has a balance, or is a DeepSeek account with no window whose first read
 *  failed. A DeepSeek account that does report windows stays an ordinary bar account. */
export function isBalanceAccount(a: UsageAccount): boolean {
  return a.balance !== undefined || (a.provider === "deepseek" && a.windows.length === 0);
}

/** The used percentage of an account's fullest window. A per-model allowance does not count while the plan's own windows
 *  are there: one model running out is not the account running out. With only per-model pools, each pool uses its own
 *  fullest window and the account takes the least-used pool. */
export function fullest(a: UsageAccount): number {
  const plan = a.windows.filter((w) => !w.model?.trim());
  if (plan.length) return plan.reduce((m, w) => Math.max(m, w.used_percent), 0);
  if (!a.windows.length) return 0;
  const byModel = new Map<string, number>();
  for (const w of a.windows) {
    const k = w.model!.trim().toLowerCase();
    byModel.set(k, Math.max(byModel.get(k) ?? 0, w.used_percent));
  }
  return Math.min(...byModel.values());
}

/** Where an account was seen, for people: the computers that read it by name (an older hub only says "a computer"),
 *  then the gateways'. Never "this computer": the numbers were read on a node, not on the device in hand (#1907). */
export function sourceNames(a: UsageAccount): string[] {
  const machines = a.machines?.length ? a.machines : a.local ? [tr("usage.source_local")] : [];
  return [...machines, ...(a.sources ?? [])];
}

export const windowKey = (w: UsageWindow) =>
  w.group || w.title
    ? `${w.label}:${w.window_minutes}:${w.model ?? ""}:${w.group ?? ""}:${w.title ?? ""}`
    : `${w.label}:${w.window_minutes}:${w.model ?? ""}`;

/** The usage provider ID shared by older node samples, gateways and visibility settings. */
export function canonicalUsageProvider(provider: string): string {
  const id = provider.trim().toLowerCase();
  return id === "agy" ? "antigravity" : id;
}

export function isHistoricalCodexReading(a: UsageAccount): boolean {
  return a.reading_source === "rollout" && a.stale === true && canonicalUsageProvider(a.provider) === "codex";
}

/** The hub's public Codex row ID survives quota resets. Saved session readings can describe an unidentified source. */
export function canAliasUsageAccount(a: UsageAccount): boolean {
  return Boolean(a.id) && canonicalUsageProvider(a.provider) === "codex" && a.reading_source !== "rollout";
}

/** The sidebar takes the two accounts with lowest headroom `h = r - R`, with stale accounts last. Rank whole
 *  percentages as displayed so sub-percent jitter keeps the same order. Balance accounts have their own rows. */
export function topAccounts(accounts: UsageAccount[], n = 2): UsageAccount[] {
  return accounts
    .filter((a) => !isBalanceAccount(a) && a.windows.length > 0)
    .sort((a, b) => {
      const ta = tightestRemaining(a);
      const tb = tightestRemaining(b);
      return Number(!!a.stale) - Number(!!b.stale)
        || Math.round(ta.headroom) - Math.round(tb.headroom)
        || Math.round(ta.remaining) - Math.round(tb.remaining)
        || canonicalUsageProvider(a.provider).localeCompare(canonicalUsageProvider(b.provider))
        || a.id.localeCompare(b.id);
    })
    .slice(0, n);
}

/** Browser-local storage key for user-pinned sidebar usage account/window IDs (#12418, #13870, display-only). */
export const PINNED_USAGE_ACCOUNTS_KEY = "persona.usage.pinnedAccounts";

const WINDOW_PIN_PREFIX = "window:";

/** Stable pin ID for one specific usage window of an account in the sidebar strip (#13870). */
export function usageWindowPinId(accountId: string, w: UsageWindow): string {
  return `${WINDOW_PIN_PREFIX}${encodeURIComponent(accountId)}:${windowKey(w)}`;
}

/** Decode a window pin ID into its account ID and `windowKey`, or null for an account-level pin or malformed value. */
export function parseUsageWindowPinId(id: string): { accountId: string; windowKey: string } | null {
  if (!id.startsWith(WINDOW_PIN_PREFIX)) return null;
  const rest = id.slice(WINDOW_PIN_PREFIX.length);
  const sep = rest.indexOf(":");
  if (sep <= 0) return null;
  const key = rest.slice(sep + 1);
  if (!key) return null;
  try {
    const accountId = decodeURIComponent(rest.slice(0, sep));
    return accountId ? { accountId, windowKey: key } : null;
  } catch {
    return null;
  }
}

/** Concise account + optional model title for a pinned window row in the collapsed sidebar strip. */
export function sidebarWindowTitle(account: Pick<UsageAccount, "name">, w: Pick<UsageWindow, "model">): string {
  const model = (w.model ?? "").trim();
  return model && account.name.toLowerCase() !== model.toLowerCase() ? `${account.name} · ${model}` : account.name;
}

const emptyPinned: readonly string[] = [];
let pinnedRaw: string | null | undefined;
let pinnedCache: readonly string[] = emptyPinned;
let pinnedVolatile = false;
const pinnedListeners = new Set<() => void>();
let stopPinnedSync: (() => void) | undefined;

function normalizePinnedIds(ids: readonly unknown[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const v of ids) {
    if (typeof v !== "string") continue;
    const id = v.trim();
    if (id && !seen.has(id)) {
      seen.add(id);
      out.push(id);
    }
  }
  return out;
}

/** Parse a stored pinned-account/window ID list, returning [] on missing or malformed values. */
export function parsePinnedUsageAccounts(raw: string | null): string[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? normalizePinnedIds(parsed) : [];
  } catch {
    return [];
  }
}

function readPinnedRaw(): { ok: true; value: string | null } | { ok: false } {
  try {
    if (typeof localStorage === "undefined") return { ok: false };
    return { ok: true, value: localStorage.getItem(PINNED_USAGE_ACCOUNTS_KEY) };
  } catch {
    return { ok: false };
  }
}

function getPinnedSnapshot(): readonly string[] {
  if (pinnedVolatile) return pinnedCache;
  const res = readPinnedRaw();
  if (!res.ok) return pinnedCache;
  if (res.value === pinnedRaw) return pinnedCache;
  pinnedRaw = res.value;
  const parsed = parsePinnedUsageAccounts(res.value);
  pinnedCache = parsed.length ? parsed : emptyPinned;
  return pinnedCache;
}

function notifyPinned(): void {
  for (const l of pinnedListeners) l();
}

/** Persist the user's chosen sidebar usage account/window pin IDs in this browser (display-only). */
export function setPinnedUsageAccounts(ids: readonly string[]): readonly string[] {
  const next = normalizePinnedIds(ids);
  const raw = next.length ? JSON.stringify(next) : null;
  let persisted = false;
  let storageFailed = false;
  try {
    if (typeof localStorage !== "undefined") {
      if (raw) localStorage.setItem(PINNED_USAGE_ACCOUNTS_KEY, raw);
      else localStorage.removeItem(PINNED_USAGE_ACCOUNTS_KEY);
      persisted = true;
    }
  } catch {
    // storage unavailable: keep in-memory state for this tab without overwriting from stale storage
    storageFailed = true;
  }
  pinnedVolatile = !persisted && (next.length > 0 || storageFailed);
  pinnedRaw = persisted ? raw : undefined;
  pinnedCache = next.length ? next : emptyPinned;
  notifyPinned();
  return pinnedCache;
}

export function togglePinnedUsageAccount(id: string): readonly string[] {
  const cur = getPinnedSnapshot();
  return setPinnedUsageAccounts(cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]);
}

export function resetPinnedUsageAccounts(): readonly string[] {
  return setPinnedUsageAccounts([]);
}

function subscribePinned(listener: () => void) {
  pinnedListeners.add(listener);
  if (!stopPinnedSync && typeof addEventListener === "function") {
    const onStorage = (e: StorageEvent) => {
      if ((e.key === null || e.key === PINNED_USAGE_ACCOUNTS_KEY) && !pinnedVolatile) {
        const res = readPinnedRaw();
        if (res.ok && res.value !== pinnedRaw) notifyPinned();
      }
    };
    addEventListener("storage", onStorage);
    stopPinnedSync = () => removeEventListener("storage", onStorage);
  }
  return () => {
    pinnedListeners.delete(listener);
    if (pinnedListeners.size === 0) {
      stopPinnedSync?.();
      stopPinnedSync = undefined;
    }
  };
}

/** Browser-local pinned account/window IDs for the collapsed sidebar usage strip. */
export function usePinnedUsageAccounts(): readonly string[] {
  return useSyncExternalStore(subscribePinned, getPinnedSnapshot, getPinnedSnapshot);
}

/** Whether an account can be pinned to the collapsed sidebar strip (has usage windows or a prepaid balance). */
export function isPinnableUsageAccount(a: UsageAccount): boolean {
  return isBalanceAccount(a) || a.windows.length > 0;
}

export type SidebarUsageItem =
  | { kind: "account"; key: string; account: UsageAccount }
  | { kind: "window"; key: string; account: UsageAccount; window: UsageWindow }
  | { kind: "balance"; key: string; account: UsageAccount };

/** Choose which live accounts or specific usage windows appear in the collapsed sidebar strip:
 *  when pinned IDs match at least one live pinnable account or window, show those matched items in pinned order;
 *  otherwise fall back to the default automatic ranking (`topAccounts(live)` followed by live balance accounts). */
export function selectSidebarUsageItems(
  readings: UsageAccount[],
  pinnedIds: readonly string[],
): { items: SidebarUsageItem[]; custom: boolean } {
  const live = readings.filter((a) => !isHistoricalCodexReading(a));
  if (pinnedIds.length > 0) {
    const byId = new Map(live.map((a) => [a.id, a]));
    const items: SidebarUsageItem[] = [];
    for (const id of pinnedIds) {
      const a = byId.get(id);
      if (a) {
        if (isPinnableUsageAccount(a)) {
          items.push({ kind: isBalanceAccount(a) ? "balance" : "account", key: a.id, account: a });
        }
        continue;
      }
      const parsed = parseUsageWindowPinId(id);
      if (!parsed) continue;
      const acct = byId.get(parsed.accountId);
      const win = acct?.windows.find((w) => windowKey(w) === parsed.windowKey);
      if (acct && win) items.push({ kind: "window", key: id, account: acct, window: win });
    }
    if (items.length > 0) return { items, custom: true };
  }
  return {
    items: [
      ...topAccounts(live).map((account): SidebarUsageItem => ({ kind: "account", key: account.id, account })),
      ...live.filter(isBalanceAccount).map((account): SidebarUsageItem => ({ kind: "balance", key: account.id, account })),
    ],
    custom: false,
  };
}

/** Choose which live accounts appear in the collapsed sidebar strip (account-level projection of `selectSidebarUsageItems`). */
export function selectSidebarUsageAccounts(
  live: UsageAccount[],
  pinnedIds: readonly string[],
): { bars: UsageAccount[]; balances: UsageAccount[]; custom: boolean } {
  const { items, custom } = selectSidebarUsageItems(live, pinnedIds);
  const bars: UsageAccount[] = [];
  const balances: UsageAccount[] = [];
  for (const item of items) {
    if (item.kind === "balance") balances.push(item.account);
    else bars.push(item.account);
  }
  return { bars, balances, custom };
}

export const isWarn = (percent: number) => percent >= WARN_PERCENT;

export interface UnconnectedProvider {
  id: string;
  title: string;
  agent: string;
  base_url?: string;
}

const emptyAccounts: UsageAccount[] = [];
const noProviders: readonly string[] = [];
let usageAccounts = emptyAccounts;
/** Providers the owner hid from the bars (`usage.hidden_providers`, #13179), as the hub last said; none from an old hub. */
let usageHidden = noProviders;
let usageReady = false;
let stopPolling: (() => void) | undefined;
const listeners = new Set<() => void>();

function publishUsage(accounts: UsageAccount[], ready = usageReady, hidden = usageHidden) {
  usageAccounts = accounts;
  usageHidden = hidden;
  usageReady = ready;
  for (const listener of listeners) listener();
}

function pollUsage() {
  let dead = false;
  // The tab coming back to the front can overlap the poll. Only the newest reply counts.
  let seq = 0;
  const load = () => {
    if (document.visibilityState !== "visible") return;
    const mine = ++seq;
    const current = () => !dead && mine === seq;
    api<{ accounts?: UsageAccount[]; hidden_providers?: string[] }>("/usage/windows")
      .then((r) => current() && publishUsage(r.accounts ?? emptyAccounts, true, r.hidden_providers?.map(canonicalUsageProvider) ?? noProviders))
      .catch((e: unknown) => {
        if (!current()) return;
        const missing = e instanceof ApiError && e.status === 404;
        publishUsage(missing ? emptyAccounts : usageAccounts.slice(), missing ? false : usageReady, missing ? noProviders : usageHidden);
      });
  };
  load();
  const timer = setInterval(load, POLL_MS);
  document.addEventListener("visibilitychange", load);
  return () => {
    dead = true;
    clearInterval(timer);
    document.removeEventListener("visibilitychange", load);
  };
}

function subscribeUsage(listener: () => void) {
  listeners.add(listener);
  stopPolling ??= pollUsage();
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) {
      stopPolling?.();
      stopPolling = undefined;
      usageAccounts = emptyAccounts;
      usageHidden = noProviders;
      usageReady = false;
    }
  };
}

/** The sidebar and Agent status lines share one poll, including while switching chats. */
export function useUsageWindows(): UsageAccount[] {
  return useSyncExternalStore(subscribeUsage, () => usageAccounts, () => emptyAccounts);
}

/** The canonical providers the owner hid from the usage bars, so the strip leaves them out everywhere (#13179). */
export function useHiddenUsageProviders(): readonly string[] {
  return useSyncExternalStore(subscribeUsage, () => usageHidden, () => noProviders);
}

/** True once GET /api/v1/usage/windows has completed a supported (non-404) response. */
export function useUsageWindowsReady(): boolean {
  return useSyncExternalStore(subscribeUsage, () => usageReady, () => false);
}
