// Popover-only grouping, table-layout, overlap-hint, and browser-local display preferences for UsagePopover.
// Kept out of usage-windows.ts so the cold first screen (UsageBars) never pays for popover logic (FIRST_SCREEN_BUDGET).
import { useSyncExternalStore } from "react";
import { planNote, sortedWindows, type PopoverAccount, type ProviderGroup } from "./usage-popover";
import {
  canAliasUsageAccount,
  canonicalUsageProvider,
  isBalanceAccount,
  windowMinutes,
  type UsageAccount,
  type UsageWindow,
} from "./usage-windows";
import { stripProviderPrefix, windowHeadroom, type UnifiedTimelineRow } from "./usage-timeline";

export type UsageWindowSection =
  | { kind: "plain"; windows: UsageWindow[] }
  | { kind: "inline-model"; model: string; window: UsageWindow }
  | { kind: "group"; key: string; title: string; source: "group" | "model"; windows: UsageWindow[] };

const INLINE_MODEL_MAX_LEN = 18;

/** Partition an account's sorted windows into visual sections:
 *  - ungrouped plan windows render directly,
 *  - explicit extension `group` windows always render together under one heading,
 *  - multi-window per-model pools (e.g. Antigravity's 5h + 7d per model group) render under one heading per pool,
 *  - single-window per-model allowances alongside plan windows (e.g. Claude `7d · Fable`) render compactly inline. */
export function groupAccountWindows(a: Pick<UsageAccount, "windows">): UsageWindowSection[] {
  const ordered = sortedWindows(a);
  const plain: UsageWindow[] = [];
  const extGroups = new Map<string, { title: string; windows: UsageWindow[] }>();
  const modelGroups = new Map<string, { title: string; windows: UsageWindow[] }>();

  for (const w of ordered) {
    const g = w.group?.trim();
    if (g) {
      const k = g.toLowerCase();
      const bucket = extGroups.get(k);
      if (bucket) bucket.windows.push(w);
      else extGroups.set(k, { title: g, windows: [w] });
      continue;
    }
    const m = w.model?.trim();
    if (m) {
      const k = m.toLowerCase();
      const bucket = modelGroups.get(k);
      if (bucket) bucket.windows.push(w);
      else modelGroups.set(k, { title: m, windows: [w] });
      continue;
    }
    plain.push(w);
  }

  const sections: UsageWindowSection[] = [];
  if (plain.length) sections.push({ kind: "plain", windows: plain });
  for (const [k, b] of extGroups) {
    sections.push({ kind: "group", key: `group:${k}`, title: b.title, source: "group", windows: b.windows });
  }
  const models = [...modelGroups.entries()];
  const useModelHeaders = models.some(([, b]) => b.windows.length > 1) || (plain.length === 0 && models.length > 1);
  for (const [k, b] of models) {
    if (b.windows.length === 1 && !useModelHeaders && b.title.length <= INLINE_MODEL_MAX_LEN) {
      sections.push({ kind: "inline-model", model: b.title, window: b.windows[0] });
    } else {
      sections.push({ kind: "group", key: `model:${k}`, title: b.title, source: "model", windows: b.windows });
    }
  }
  return sections;
}

export interface WindowRowDisplay {
  /** Always the short window duration label ("5h", "7d") so column 1 stays aligned across every row in a card. */
  span: string;
  /** Optional compact caption aligned above the meter track (e.g. single-model name "Fable" or extension `title` "Burst"). */
  caption: string;
  /** Whether `caption` represents a per-model allowance (marks `data-usage-model`). */
  isModelCaption: boolean;
  /** Whether `caption` carries a distinct extension `title` (marks `data-usage-window-title`). */
  hasTitle: boolean;
  /** Extension-supplied quiet remark under the row, or "". */
  note: string;
}

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** Remove a redundant window-span prefix or suffix (e.g. "7d · Fable", "7 days · Fable", "Fable (7d)") from a title
 *  so callers can check whether `title` merely repeats `label` + `model` or `group`. */
function stripSpanFromTitle(title: string, label: string): string {
  let out = title.trim();
  const spanTokens = [label.trim()];
  const m = /^(\d+(?:\.\d+)?)\s*([mhdw])$/i.exec(label.trim());
  if (m) {
    const n = m[1];
    const unit = { m: "minutes?", h: "hours?", d: "days?", w: "weeks?" }[m[2].toLowerCase() as "m" | "h" | "d" | "w"];
    spanTokens.push(`${n}\\s*${unit}`);
  }
  const sep = "(?:\\s*[·•:\\-–—/|]\\s*|\\s+)";
  for (const tok of spanTokens) {
    if (!tok) continue;
    const pat = tok === label.trim() ? escapeRe(tok) : tok;
    out = out
      .replace(new RegExp(`^${pat}${sep}`, "i"), "")
      .replace(new RegExp(`${sep}${pat}$`, "i"), "")
      .replace(new RegExp(`^\\(${pat}\\)\\s*`, "i"), "")
      .replace(new RegExp(`\\s*\\(${pat}\\)$`, "i"), "")
      .trim();
  }
  return out;
}

/** Resolve display labels for one window row: always keeps `span` ("5h", "7d") in column 1 and deduplicates extension
 *  `title` against `label`, `inlineModel`, and `groupContext` so e.g. `{ label: "7d", model: "Fable", title: "Fable" }`
 *  never renders "Fable · Fable" or drops "7d". */
export function windowRowDisplay(w: UsageWindow, inlineModel?: string, groupContext?: string): WindowRowDisplay {
  const span = w.label.trim();
  const rawTitle = w.title?.trim() ?? "";
  const note = w.note?.trim() ?? "";
  const model = inlineModel?.trim() ?? "";
  const group = groupContext?.trim() ?? "";

  const strippedTitle = rawTitle ? stripSpanFromTitle(rawTitle, span) : "";
  const titleEqualsSpan = Boolean(rawTitle) && rawTitle.toLowerCase() === span.toLowerCase();
  const titleEqualsGroup = Boolean(group) && (
    rawTitle.toLowerCase() === group.toLowerCase() ||
    (Boolean(strippedTitle) && strippedTitle.toLowerCase() === group.toLowerCase())
  );

  if (model) {
    const modelLower = model.toLowerCase();
    const titleDuplicatesModel = !rawTitle || titleEqualsSpan || titleEqualsGroup
      || rawTitle.toLowerCase() === modelLower
      || strippedTitle.toLowerCase() === modelLower;
    if (titleDuplicatesModel) {
      return { span, caption: model, isModelCaption: true, hasTitle: false, note };
    }
    const extra = strippedTitle || rawTitle;
    return { span, caption: `${model} · ${extra}`, isModelCaption: true, hasTitle: true, note };
  }

  if (rawTitle && !titleEqualsSpan && !titleEqualsGroup) {
    return { span, caption: rawTitle, isModelCaption: false, hasTitle: true, note };
  }
  return { span, caption: "", isModelCaption: false, hasTitle: false, note };
}

/** Whether a single-account provider card has an account name identical to its provider title (case-insensitive),
 *  allowing the card header to merge account badges and pin controls without repeating the title row. */
export function shouldMergeSingleAccountHeader(group: Pick<ProviderGroup, "accounts">, providerTitleText: string): boolean {
  if (group.accounts.length !== 1 || group.accounts[0].alias) return false;
  return group.accounts[0].name.trim().toLowerCase() === providerTitleText.trim().toLowerCase();
}

const OVERLAP_TOLERANCE_MS = 60_000;

function canonicalScopeName(raw: string | undefined): string {
  const s = (raw ?? "").trim().toLowerCase();
  if (s === "gemini models") return "gemini";
  if (s === "claude and gpt models" || s === "claude gpt" || s === "claude_gpt" || s === "claude/gpt") return "claude/gpt";
  return s;
}

function explicitAccountEmail(name: string | undefined): string {
  const head = (name ?? "").split("·")[0]?.trim().toLowerCase() ?? "";
  return head.includes("@") ? head : "";
}

/** Non-destructive detection of local-only rows whose active window reset times match an external/extension row (or,
 *  when the local row is stale, a live sibling row) under the same provider within 60s, returning a map of local
 *  account id -> matched peer account name. */
export function detectLocalExternalOverlaps(accounts: readonly UsageAccount[], now: number, aliases?: ReadonlyMap<string, string>): Map<string, string> {
  const out = new Map<string, string>();
  const byProvider = new Map<string, { locals: UsageAccount[]; externals: UsageAccount[]; reconciled: UsageAccount[] }>();
  for (const a of accounts) {
    const p = canonicalUsageProvider(a.provider);
    let bucket = byProvider.get(p);
    if (!bucket) {
      bucket = { locals: [], externals: [], reconciled: [] };
      byProvider.set(p, bucket);
    }
    const hasSources = (a.sources?.length ?? 0) > 0;
    if (Boolean(a.local) && !hasSources) bucket.locals.push(a);
    else if (!a.local && hasSources) bucket.externals.push(a);
    else if (Boolean(a.local) && hasSources) bucket.reconciled.push(a);
  }
  const activeWinsOf = (a: UsageAccount) => {
    const nameScope = canonicalScopeName(a.name.includes("·") ? a.name.split("·").slice(1).join("·") : "");
    return a.windows
      .map((w) => ({
        mins: windowMinutes(w),
        at: w.resets_at ? Date.parse(w.resets_at) : NaN,
        scope: canonicalScopeName(w.model || w.group) || nameScope,
      }))
      .filter((w) => w.mins > 0 && Number.isFinite(w.at) && w.at > now);
  };
  const sharesMachine = (a: UsageAccount, b: UsageAccount) =>
    Boolean(a.machines?.length && b.machines?.some((m) => a.machines!.includes(m)));

  for (const { locals, externals, reconciled } of byProvider.values()) {
    if (!locals.length) continue;
    for (const local of locals) {
      const localWins = local.windows
        .map((w) => ({ mins: windowMinutes(w), at: w.resets_at ? Date.parse(w.resets_at) : NaN, scope: canonicalScopeName(w.model || w.group) }))
        .filter((w) => w.mins > 0 && Number.isFinite(w.at) && w.at > now);
      if (!localWins.length) continue;

      const candidates = [
        ...externals,
        ...reconciled.filter((r) => !sharesMachine(local, r)),
        ...(local.stale ? locals.filter((peer) => !peer.stale && peer.id !== local.id) : []),
      ];
      const localEmail = explicitAccountEmail(local.name);
      for (const peer of candidates) {
        const peerEmail = explicitAccountEmail(peer.name);
        if (localEmail && peerEmail && localEmail !== peerEmail) continue;
        const peerWins = activeWinsOf(peer);
        const matched = peerWins.some((pw) =>
          localWins.some(
            (lw) => lw.mins === pw.mins && Math.abs(lw.at - pw.at) <= OVERLAP_TOLERANCE_MS && (!lw.scope || !pw.scope || lw.scope === pw.scope),
          ),
        );
        if (matched) {
          out.set(local.id, (canAliasUsageAccount(peer) && aliases?.get(peer.id)) || peer.name);
          break;
        }
      }
    }
  }
  return out;
}

export interface NextTimelineReset {
  accountId: string;
  accountName: string;
  provider: string;
  label: string;
  resetsAt: string;
  remainingMs: number;
  paused: boolean;
}

/** Earliest upcoming reset across timeline rows, used for the collapsed reset-timeline summary bar.
 *  Prefers live, non-excluded rows so a stale/collapsed duplicate never hides the live account's next reset,
 *  falling back to all rows only when every row is stale or excluded. */
export function nextTimelineReset(
  rows: readonly UnifiedTimelineRow[],
  excludeIds?: ReadonlySet<string>,
): NextTimelineReset | null {
  const pickFrom = (list: readonly UnifiedTimelineRow[]): NextTimelineReset | null => {
    let best: NextTimelineReset | null = null;
    for (const row of list) {
      for (const lane of row.lanes) {
        if (!best || lane.remainingMs < best.remainingMs) {
          best = {
            accountId: row.account.id,
            accountName: row.displayName,
            provider: row.provider,
            label: lane.label,
            resetsAt: lane.resetsAt,
            remainingMs: lane.remainingMs,
            paused: lane.paused,
          };
        }
      }
    }
    return best;
  };
  const preferred = rows.filter((r) => !r.account.stale && !excludeIds?.has(r.account.id));
  return pickFrom(preferred) ?? pickFrom(rows);
}

// ── Compact table model (inspired by Magpie's span-aligned quota table) ──────────────────────────────────────

const TABLE_MAX_WINDOW_COLS = 3;
const MONTH_MIN_MINUTES = 28 * 1440;
const MONTH_MAX_MINUTES = 31 * 1440;
const MONTH_CANONICAL_MINUTES = 30 * 1440;

export interface UsageTableColumn {
  key: string;
  head: string;
  /** Window duration in minutes (Infinity for windows without a parseable duration). */
  minutes: number;
}

export interface UsageTableSort {
  key: "account" | string;
  dir: "asc" | "desc";
}

export interface UsageTableRow {
  provider: string;
  account: PopoverAccount;
  displayName: string;
  plan: string;
  cells: Record<string, UsageWindow[]>;
  other: UsageWindow[];
  bareBalance: boolean;
  noticeOnly: boolean;
}

export interface UsageTableModel {
  columns: UsageTableColumn[];
  hasOther: boolean;
  rows: UsageTableRow[];
}

function tableColumnOf(w: UsageWindow): UsageTableColumn {
  const raw = windowMinutes(w);
  if (raw > 0) {
    const mins = raw >= MONTH_MIN_MINUTES && raw <= MONTH_MAX_MINUTES ? MONTH_CANONICAL_MINUTES : raw;
    return { key: `m:${mins}`, head: w.label.trim() || `${mins}m`, minutes: mins };
  }
  const label = w.label.trim();
  return { key: `l:${label.toLowerCase()}`, head: label, minutes: Infinity };
}

/** Compact pool / model label for tight table cells while preserving the full name for tooltips. */
export function shortPoolLabel(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  const lower = trimmed.toLowerCase();
  if (lower === "claude and gpt models") return "Claude & GPT";
  if (lower === "gemini models") return "Gemini";
  const stripped = trimmed.replace(/\s+models?$/i, "").trim();
  return stripped.length >= 3 ? stripped : trimmed;
}

/** Sub-label drawn beside a percentage inside a table cell (empty when the column heading already identifies the window).
 *  Deduplicates extension `title` against `colHead`, `model`, and `group` so e.g. `{ label: "7d", model: "Fable", title: "7 days · Fable" }`
 *  inside the `7d` column reads cleanly as `"Fable"`. */
export function tableWindowSubLabel(
  w: UsageWindow,
  colHead: string,
  cellSize: number,
  planWord: string,
): { text: string; full: string } {
  const rawTitle = w.title?.trim() ?? "";
  const group = w.group?.trim() ?? "";
  const model = w.model?.trim() ?? "";
  const headLower = colHead.trim().toLowerCase();
  const cleanTitle = rawTitle ? stripSpanFromTitle(rawTitle, colHead || w.label) : "";

  if (model) {
    const modelLower = model.toLowerCase();
    const titleDuplicatesModel = !rawTitle
      || rawTitle.toLowerCase() === headLower
      || rawTitle.toLowerCase() === modelLower
      || cleanTitle.toLowerCase() === modelLower;
    if (titleDuplicatesModel) return { text: shortPoolLabel(model), full: model };
    const extra = cleanTitle || rawTitle;
    return { text: shortPoolLabel(extra), full: `${model} · ${extra}` };
  }

  if (rawTitle && rawTitle.toLowerCase() !== headLower) {
    const effective = cleanTitle || rawTitle;
    const full = group && group.toLowerCase() !== effective.toLowerCase() ? `${group} · ${effective}` : effective;
    return { text: shortPoolLabel(effective), full };
  }
  if (group) return { text: shortPoolLabel(group), full: group };
  if (cellSize > 1) return { text: planWord, full: planWord };
  return { text: "", full: colHead };
}

/** Build a span-aligned comparison table across all provider groups (5h, 7d, and optional Other column),
 *  supporting 3-state column sorting with empty cells always anchored below populated rows. */
export function buildUsageTable(groups: readonly ProviderGroup[], sort: UsageTableSort | null): UsageTableModel {
  const flat: Array<{ order: number; provider: string; account: PopoverAccount; windows: UsageWindow[] }> = [];
  for (const g of groups) {
    for (const account of g.accounts) {
      flat.push({ order: flat.length, provider: g.provider, account, windows: sortedWindows(account) });
    }
  }

  const colAccountCount = new Map<string, number>();
  const colMinutes = new Map<string, number>();
  const colHeads = new Map<string, Map<string, number>>();
  const colOrder = new Map<string, number>();

  for (const entry of flat) {
    const seenCols = new Set<string>();
    for (const w of entry.windows) {
      const c = tableColumnOf(w);
      if (!colOrder.has(c.key)) colOrder.set(c.key, colOrder.size);
      colMinutes.set(c.key, c.minutes);
      seenCols.add(c.key);
      let heads = colHeads.get(c.key);
      if (!heads) {
        heads = new Map();
        colHeads.set(c.key, heads);
      }
      heads.set(c.head, (heads.get(c.head) ?? 0) + 1);
    }
    for (const key of seenCols) colAccountCount.set(key, (colAccountCount.get(key) ?? 0) + 1);
  }

  const headOf = (key: string): string => {
    const entries = [...(colHeads.get(key)?.entries() ?? [])];
    entries.sort(([a, ca], [b, cb]) => cb - ca || Number(/^\d/.test(b)) - Number(/^\d/.test(a)) || a.localeCompare(b));
    return entries[0]?.[0] ?? key;
  };

  const columns: UsageTableColumn[] = [...colAccountCount.keys()]
    .sort((a, b) => {
      const ma = colMinutes.get(a) ?? Infinity;
      const mb = colMinutes.get(b) ?? Infinity;
      return Number(ma === Infinity) - Number(mb === Infinity)
        || (colAccountCount.get(b) ?? 0) - (colAccountCount.get(a) ?? 0)
        || ma - mb
        || (colOrder.get(a) ?? 0) - (colOrder.get(b) ?? 0);
    })
    .slice(0, TABLE_MAX_WINDOW_COLS)
    .sort((a, b) => (colMinutes.get(a) ?? Infinity) - (colMinutes.get(b) ?? Infinity) || (colOrder.get(a) ?? 0) - (colOrder.get(b) ?? 0))
    .map((key) => ({ key, head: headOf(key), minutes: colMinutes.get(key) ?? Infinity }));

  const colKeySet = new Set(columns.map((c) => c.key));
  const rawRows = flat.map((entry) => {
    const cells: Record<string, UsageWindow[]> = {};
    for (const c of columns) cells[c.key] = [];
    const other: UsageWindow[] = [];
    for (const w of entry.windows) {
      const k = tableColumnOf(w).key;
      if (colKeySet.has(k)) cells[k].push(w);
      else other.push(w);
    }
    const bareBalance = isBalanceAccount(entry.account) && entry.windows.length === 0;
    const noticeOnly = !bareBalance && entry.windows.length === 0;
    return {
      order: entry.order,
      provider: entry.provider,
      account: entry.account,
      displayName: entry.account.alias ?? stripProviderPrefix(entry.account.name, entry.provider),
      plan: planNote(entry.account),
      cells,
      other,
      bareBalance,
      noticeOnly,
    };
  });

  const hasOther =
    rawRows.some((r) => r.other.length > 0 || (r.account.balance !== undefined && r.account.windows.length > 0)) ||
    (columns.length === 0 && rawRows.some((r) => r.bareBalance));

  const validSort = sort && (sort.key === "account" || colKeySet.has(sort.key)) ? sort : null;
  if (validSort) {
    const dir = validSort.dir === "desc" ? -1 : 1;
    rawRows.sort((a, b) => {
      if (validSort.key === "account") {
        const cmp = (a.account.alias ?? a.account.name).localeCompare(b.account.alias ?? b.account.name, undefined, { sensitivity: "base" });
        return cmp * dir || a.order - b.order;
      }
      const wa = a.cells[validSort.key] ?? [];
      const wb = b.cells[validSort.key] ?? [];
      const ha = wa.length ? Math.min(...wa.map(windowHeadroom)) : null;
      const hb = wb.length ? Math.min(...wb.map(windowHeadroom)) : null;
      if ((ha === null) !== (hb === null)) return ha === null ? 1 : -1;
      if (ha !== null && hb !== null && ha !== hb) return (ha - hb) * dir;
      return a.order - b.order;
    });
  }

  return {
    columns,
    hasOther,
    rows: rawRows.map(({ order: _order, ...row }) => row),
  };
}

// ── Browser-local display preferences (view mode & timeline fold) ────────────────────────────────────────────

export type UsageViewMode = "cards" | "table";
export const USAGE_VIEW_MODE_KEY = "persona.usage.viewMode";
export const USAGE_TIMELINE_OPEN_KEY = "persona.usage.timelineOpen";

function createLocalPrefStore<T>(key: string, fallback: T, parse: (raw: string | null) => T, serialize: (v: T) => string | null) {
  let lastRaw: string | null | undefined;
  let cached: T = fallback;
  const listeners = new Set<() => void>();
  let stopSync: (() => void) | undefined;

  const readRaw = (): string | null => {
    try {
      return typeof localStorage === "undefined" ? null : localStorage.getItem(key);
    } catch {
      return null;
    }
  };

  const getSnapshot = (): T => {
    const raw = readRaw();
    if (raw === lastRaw) return cached;
    lastRaw = raw;
    cached = parse(raw);
    return cached;
  };

  const notify = () => {
    for (const l of listeners) l();
  };

  const set = (next: T): T => {
    const raw = serialize(next);
    let persisted = false;
    try {
      if (typeof localStorage !== "undefined") {
        if (raw === null) localStorage.removeItem(key);
        else localStorage.setItem(key, raw);
        persisted = true;
      }
    } catch {
      // storage unavailable: keep in-memory state for this tab
    }
    lastRaw = persisted ? raw : readRaw();
    cached = next;
    notify();
    return cached;
  };

  const subscribe = (listener: () => void) => {
    listeners.add(listener);
    if (!stopSync && typeof addEventListener === "function") {
      const onStorage = (e: StorageEvent) => {
        if (e.key === null || e.key === key) notify();
      };
      addEventListener("storage", onStorage);
      stopSync = () => removeEventListener("storage", onStorage);
    }
    return () => {
      listeners.delete(listener);
      if (listeners.size === 0) {
        stopSync?.();
        stopSync = undefined;
      }
    };
  };

  return {
    getSnapshot,
    useValue: () => useSyncExternalStore(subscribe, getSnapshot, getSnapshot),
    set,
    reset: () => set(fallback),
  };
}

const viewModeStore = createLocalPrefStore<UsageViewMode>(
  USAGE_VIEW_MODE_KEY,
  "cards",
  (raw) => (raw === "table" ? "table" : "cards"),
  (v) => (v === "cards" ? null : v),
);

const timelineOpenStore = createLocalPrefStore<boolean>(
  USAGE_TIMELINE_OPEN_KEY,
  false,
  (raw) => raw === "1" || raw === "true",
  (v) => (v ? "1" : null),
);

export const useUsageViewMode = viewModeStore.useValue;
export const setUsageViewMode = viewModeStore.set;
export const resetUsageViewMode = viewModeStore.reset;

export const useUsageTimelineOpen = timelineOpenStore.useValue;
export const setUsageTimelineOpen = timelineOpenStore.set;
export const resetUsageTimelineOpen = timelineOpenStore.reset;

export const USAGE_ALIAS_MAX_LENGTH = 32;

/** Use the same hub URL as the hub switcher (including its proxy path) and the selected manager. */
export function createUsageAliasStore(hub: string, manager: string) {
  const store = createLocalPrefStore<ReadonlyMap<string, string>>(
    `persona.usage.codexAliases:${JSON.stringify([hub, manager])}`,
    new Map(),
    (raw) => {
      if (!raw) return new Map();
      try {
        return new Map(Object.entries(JSON.parse(raw)).filter((entry): entry is [string, string] => typeof entry[1] === "string"));
      } catch {
        return new Map();
      }
    },
    (aliases) => aliases.size ? JSON.stringify(Object.fromEntries(aliases)) : null,
  );
  return {
    useValue: store.useValue,
    setAlias(id: string, value: string) {
      const next = new Map(store.getSnapshot());
      const alias = value.trim();
      if (alias) next.set(id, alias);
      else next.delete(id);
      store.set(next);
    },
  };
}
