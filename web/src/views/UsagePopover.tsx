// The account windows and error details behind a click on the sidebar's usage bars (#10082): these are not needed
// for the cold first screen, so the overlay, its CSS transitions, and its long strings load on intent.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { formatDate, t as tr } from "../i18n";
import { sendDraft } from "../lib/drafts";
import { navigate } from "../lib/router";
import { here } from "../lib/hubs";
import { useShownManager } from "../lib/manager-choice";
import { getState } from "../lib/store";
import { balanceEmpty, balanceText } from "../lib/usage-balance";
import { useCloseOnBack } from "../lib/back-close";
import { accountPace } from "../lib/usage-pace";
import { groupByProvider, planNote, providerTitle, remainingText, TIMELINE_CAP_MS, TIMELINE_TICKS_HOURS, timelinePosition, type PopoverAccount, type ProviderGroup } from "../lib/usage-popover";
import {
  buildUsageTable,
  createUsageAliasStore,
  USAGE_ALIAS_MAX_LENGTH,
  detectLocalExternalOverlaps,
  groupAccountWindows,
  nextTimelineReset,
  setUsageTimelineOpen,
  setUsageViewMode,
  shouldMergeSingleAccountHeader,
  tableWindowSubLabel,
  useUsageTimelineOpen,
  useUsageViewMode,
  windowRowDisplay,
  type UsageTableSort,
  type UsageViewMode,
} from "../lib/usage-popover-model";
import { isWindowPaused, unifiedTimeline, type TimelineLane, type UnifiedTimelineRow } from "../lib/usage-timeline";
import { canAliasUsageAccount, canonicalUsageProvider, isBalanceAccount, isPinnableUsageAccount, protectReserve, remainingPercent, remainingTone, resetLabel, resetPinnedUsageAccounts, sampleAge, selectSidebarUsageItems, sourceNames, togglePinnedUsageAccount, usageWindowPinId, usePinnedUsageAccounts, windowKey, type UnconnectedProvider, type UsageAccount, type UsageWindow } from "../lib/usage-windows";
import { Collapse, cx, IconButton, toast } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { reducedMotion } from "../ui/motion";
import { NARROW_QUERY, returnFocus, useMedia } from "../ui/overlay";
import { TONE_BG, TONE_TEXT } from "./UsageBars";
import "./usage-popover.css";

const percent = (p: number) => Math.min(100, Math.max(0, p));
const shown = (p: number) => `${Math.round(p)}%`;

/** Remaining-quota bar for the popover: fills left-to-right to `remaining%`, optional left-side `[0, reserve]`
 *  hatched exclusion zone + vertical line at `x = reserve`, and optional 1px tick at `expectedRemaining` (`100 - expectedUsed`). */
export function Meter({
  remaining,
  expectedRemaining,
  reserve,
  label,
  tall,
  barClass,
  reserveTop = true,
}: {
  remaining: number;
  expectedRemaining?: number;
  reserve?: number;
  label: string;
  tall?: boolean;
  barClass?: string;
  reserveTop?: boolean;
}) {
  const hasReserve = typeof reserve === "number" && Number.isFinite(reserve) && reserve >= 0 && reserve <= 100;
  const r = percent(remaining);
  const R = hasReserve ? percent(reserve) : 0;
  const flagAlign = R <= 18 ? "left-0" : R >= 82 ? "right-0" : "-translate-x-1/2";
  const flagStyle = R <= 18 || R >= 82 ? undefined : { left: `${R}%` };
  return (
    <div
      role="progressbar"
      aria-label={hasReserve ? `${label}, ${tr("usage.protect_label", { percent: Math.round(R) })}` : label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(r)}
      className={cx("relative w-full rounded-full bg-inset", tall ? "h-1.5" : "h-1", hasReserve && reserveTop && "mt-3.5")}
    >
      <div className={cx("h-full rounded-full transition-[width] duration-300", barClass ?? TONE_BG[remainingTone(r, R)])} style={{ width: `${r}%` }} />
      {hasReserve && (
        <>
          <span
            aria-hidden
            data-protect-zone
            className="pointer-events-none absolute inset-y-0 left-0"
            style={{ width: `${R}%`, borderTopLeftRadius: "9999px", borderBottomLeftRadius: "9999px", backgroundImage: "repeating-linear-gradient(135deg, transparent 0 2px, color-mix(in srgb, var(--orange) 45%, transparent) 2px 3px)" }}
          />
          <span aria-hidden data-protect-line className="pointer-events-none absolute -top-1 -bottom-1 w-0.5 -translate-x-1/2 rounded-full bg-orange" style={{ left: `${R}%` }} />
          <span aria-hidden data-protect-flag className={cx("pointer-events-none absolute -top-4 whitespace-nowrap rounded-sm bg-surface px-0.5 text-submeta font-bold leading-none text-orange", flagAlign)} style={flagStyle}>
            {tr("usage.protect_flag", { percent: Math.round(R) })}
          </span>
        </>
      )}
      {expectedRemaining !== undefined && <span aria-hidden data-pace-tick className="pointer-events-none absolute -top-0.5 -bottom-0.5 w-px -translate-x-1/2 rounded-full bg-ink-3" style={{ left: `${percent(expectedRemaining)}%` }} />}
    </div>
  );
}

/** Adaptive multi-column popover geometry constants. */
export const POPOVER_COL_WIDTH = 340;
export const POPOVER_SINGLE_COL_WIDTH = 360;
export const POPOVER_COL_GAP = 12;
export const POPOVER_COL_STEP = POPOVER_COL_WIDTH + POPOVER_COL_GAP; // 352px
export const POPOVER_MAX_COLS = 3;
export const POPOVER_PAD_X = 24;
export const POPOVER_PAD_Y = 24;
export const POPOVER_GAP_PX = 8;
export const POPOVER_EDGE_PX = 16;
export const POPOVER_HYSTERESIS_PX = 24;
export const POPOVER_SINGLE_COL_COMFORT_H = 440;
export const POPOVER_TABLE_MIN_WIDTH = 660;
export const POPOVER_TABLE_MAX_WIDTH = 880;
const CLOSE_MS = 160;
const REDUCED_CLOSE_MS = 120;

/** The least height a run of cards takes in `cols` CSS columns: cards keep their order (`break-inside: avoid`), so this
 *  is the smallest possible tallest column over every split of the sequence into at most `cols` contiguous runs. */
export function packedColumnHeight(cards: readonly number[], cols: number, gap = POPOVER_COL_GAP): number {
  const hs = cards.filter((h) => h > 0);
  const n = hs.length;
  if (!n) return 0;
  const c = Math.max(1, Math.min(cols, n));
  const prefix = [0];
  for (const h of hs) prefix.push(prefix[prefix.length - 1] + h);
  const run = (i: number, j: number) => prefix[j] - prefix[i] + (j - i - 1) * gap;
  let best = hs.map((_, j) => run(0, j + 1));
  for (let k = 2; k <= c; k++) {
    const next = best.slice();
    for (let j = 0; j < n; j++) {
      for (let i = 0; i < j; i++) next[j] = Math.min(next[j], Math.max(best[i], run(i + 1, j + 1)));
    }
    best = next;
  }
  return best[n - 1];
}

export interface UsagePopoverLayoutInput {
  /** Natural height when laid out in a single column (including vertical padding); used when `cardHeights` is absent. */
  naturalHeight?: number;
  /** Measured height of each provider card (unsplittable, flowed in order into the columns). */
  cardHeights?: readonly number[];
  /** Total height of the full-width blocks (`column-span: all`: header, pinned bar, unified timeline, unconnected rows) with their gaps. */
  spanHeight?: number;
  viewportWidth: number;
  viewportHeight: number;
  anchorRect?: { left: number; top: number; right?: number; bottom?: number } | null;
  /** Previous settled column count for 24px hysteresis; omit or pass null on initial layout. */
  prevCols?: number | null;
  /** Number of provider cards; caps columns so multi-column never creates empty columns. */
  providerCount?: number;
  /** Display mode: cards (adaptive 1..3 columns) or table (single full-width span sized to table columns). */
  viewMode?: UsageViewMode;
  /** Number of data columns in table view (window columns + optional Other). */
  tableColCount?: number;
}

export interface UsagePopoverLayout {
  cols: number;
  colWidth: number;
  width: number;
  left: number;
  bottom: number;
  availH: number;
  availW: number;
}

/** Pure adaptive multi-column layout calculator: stays single-column (356px inner / 380px outer, clamped to availW)
 *  when compact or when only one provider card exists; on wide viewports with >=2 provider cards, expands to 2+ columns
 *  once single-column height exceeds `min(availH, POPOVER_SINGLE_COL_COMFORT_H)` (440px) so multi-provider desktop
 *  popovers never look like a skinny vertical tower, and expands to 3 columns when 2 columns would overflow `availH`.
 *  24px hysteresis on later resizes keeps the column count from flickering.
 *  In table mode, uses a single wide column tailored to the number of aligned window columns (clamped to availW). */
export function computeUsagePopoverLayout(input: UsagePopoverLayoutInput): UsagePopoverLayout {
  const vw = Math.max(240, input.viewportWidth);
  const vh = Math.max(160, input.viewportHeight);
  const anchor = input.anchorRect;
  const rawLeft = anchor ? Math.max(8, anchor.left) : POPOVER_EDGE_PX;
  const availW = Math.max(200, vw - POPOVER_EDGE_PX - rawLeft);
  const viewportAvailH = Math.max(120, vh - POPOVER_EDGE_PX * 2);

  let availH = viewportAvailH;
  let bottom = POPOVER_EDGE_PX;
  if (anchor) {
    const spaceAbove = anchor.top - POPOVER_GAP_PX - POPOVER_EDGE_PX;
    if (spaceAbove >= 120) {
      availH = Math.min(viewportAvailH, spaceAbove);
      bottom = Math.max(POPOVER_EDGE_PX, vh - anchor.top + POPOVER_GAP_PX);
    }
  }

  if (input.viewMode === "table") {
    const dataCols = Math.max(1, input.tableColCount ?? 2);
    const desired = Math.min(POPOVER_TABLE_MAX_WIDTH, Math.max(POPOVER_TABLE_MIN_WIDTH, 270 + dataCols * 190 + POPOVER_PAD_X));
    const width = Math.round(Math.min(availW, desired));
    const colWidth = Math.max(160, width - POPOVER_PAD_X);
    const left = Math.round(Math.max(8, Math.min(rawLeft, vw - POPOVER_EDGE_PX - width)));
    return { cols: 1, colWidth, width, left, bottom: Math.round(bottom), availH: Math.round(availH), availW: Math.round(availW) };
  }

  const cards = input.cardHeights?.filter((h) => h > 0);
  const cardCap = cards ? Math.max(1, cards.length) : input.providerCount;
  const maxPossibleCols = cardCap !== undefined ? Math.max(1, Math.min(POPOVER_MAX_COLS, cardCap)) : POPOVER_MAX_COLS;
  const rawMaxByWidth = Math.max(1, Math.min(maxPossibleCols, Math.floor(availW / POPOVER_COL_STEP)));
  const H = Math.max(0, input.naturalHeight ?? 0);
  const spans = Math.max(0, input.spanHeight ?? 0);
  const singleColLimit = Math.min(availH, POPOVER_SINGLE_COL_COMFORT_H);
  /** Whether `c` columns hold everything within the target height for `c`, less `slack`. */
  const fits = (c: number, slack = 0) => {
    const limit = c === 1 && maxPossibleCols > 1 && rawMaxByWidth > 1 ? singleColLimit : availH;
    return cards ? POPOVER_PAD_Y + spans + packedColumnHeight(cards, c) <= limit - slack : H <= c * limit - slack;
  };
  let rawNeeded = maxPossibleCols;
  for (let c = 1; c <= maxPossibleCols; c++) {
    if (fits(c)) {
      rawNeeded = c;
      break;
    }
  }
  const rawCols = Math.max(1, Math.min(rawNeeded, rawMaxByWidth));

  let cols = rawCols;
  if (input.prevCols != null) {
    const prev = Math.max(1, Math.min(maxPossibleCols, input.prevCols));
    if (rawCols === prev) {
      cols = prev;
    } else if (rawCols < prev) {
      if (prev > rawMaxByWidth) {
        // Viewport narrowed past the current column count: shrink immediately so we never clip horizontally.
        cols = rawMaxByWidth;
      } else {
        // Height decreased / availH grew: require 24px headroom before collapsing to fewer columns.
        cols = prev;
        for (let c = rawCols; c < prev; c++) {
          if (fits(c, POPOVER_HYSTERESIS_PX)) {
            cols = c;
            break;
          }
        }
      }
    } else {
      // Wants more columns: require 24px width hysteresis past the column step to avoid width-edge bounce.
      cols = prev;
      for (let c = rawCols; c > prev; c--) {
        if (!fits(c - 1) && availW >= c * POPOVER_COL_STEP + POPOVER_HYSTERESIS_PX) {
          cols = c;
          break;
        }
      }
    }
  }

  const idealWidth = cols === 1
    ? POPOVER_SINGLE_COL_WIDTH + POPOVER_PAD_X
    : cols * POPOVER_COL_WIDTH + (cols - 1) * POPOVER_COL_GAP + POPOVER_PAD_X;
  const width = Math.round(Math.min(availW, idealWidth));
  const innerW = Math.max(160, width - POPOVER_PAD_X);
  const colWidth = cols === 1
    ? Math.min(POPOVER_SINGLE_COL_WIDTH, innerW)
    : Math.max(160, Math.min(POPOVER_COL_WIDTH, Math.floor((innerW - (cols - 1) * POPOVER_COL_GAP) / cols)));
  const left = Math.round(Math.max(8, Math.min(rawLeft, vw - POPOVER_EDGE_PX - width)));

  return { cols, colWidth, width, left, bottom: Math.round(bottom), availH: Math.round(availH), availW: Math.round(availW) };
}

/** Provider brand dot color from the design system's --hue-* palette (no raw hex). */
const PROVIDER_HUE: Record<string, string> = {
  claude: "var(--hue-orange)",
  codex: "var(--hue-green)",
  openai: "var(--hue-green)",
  gemini: "var(--hue-blue)",
  antigravity: "var(--hue-blue)",
  deepseek: "var(--hue-indigo)",
  kimi: "var(--hue-purple)",
  cursor: "var(--hue-teal)",
  copilot: "var(--hue-indigo)",
  trae: "var(--hue-green)",
  grok: "var(--hue-gray)",
  xai: "var(--hue-gray)",
};

export function providerHue(provider: string): string {
  return PROVIDER_HUE[canonicalUsageProvider(provider)] ?? "var(--hue-gray)";
}

const HOUR_MS = 3600_000;
const DAY_HOURS = 24;
const CAP_HOURS = TIMELINE_CAP_MS / HOUR_MS;
/** Minimum center-to-center spacing for secondary ticks and clear pixel gap between adjacent label bounding boxes. */
export const TIMELINE_MIN_TICK_GAP_PX = 20;
const TIMELINE_MIN_LABEL_GAP_PX = 6;
const ALL_TIMELINE_TICKS: readonly number[] = [0, ...TIMELINE_TICKS_HOURS];
/** Interior ticks dropped in priority order when narrow width causes spacing or bounding-box collisions. */
const DROPPABLE_TIMELINE_TICKS = [6, 120, 12, 72, 24] as const;

function tickLabel(hours: number): string {
  if (hours === 0) return tr("usage.timeline_now");
  if (hours < DAY_HOURS) return tr("usage.tick_hours", { h: hours });
  return tr("usage.tick_days", { d: hours / DAY_HOURS });
}

/** Conservative rendered width estimate in px for a text-submeta (11.5px) tick label so collision checks hold across locales. */
function estimateTickWidth(hours: number): number {
  const text = tickLabel(hours);
  let width = 0;
  for (const ch of text) width += ch.charCodeAt(0) > 0x7f ? 12 : 7;
  if (hours === 0) width += 2; // Now is font-semibold
  return Math.max(14, width);
}

function tickBounds(hours: number, axisWidthPx: number, widthPx: number): [number, number] {
  if (hours === 0) return [0, widthPx];
  if (hours === CAP_HOURS) return [axisWidthPx - widthPx, axisWidthPx];
  const cxPos = timelinePosition(hours * HOUR_MS) * axisWidthPx;
  return [cxPos - widthPx / 2, cxPos + widthPx / 2];
}

/** Visible timeline tick hours for an axis width: enforces both >=20px tick spacing and non-overlapping label bounds,
 *  progressively dropping 6h, 5d, 12h, 3d, 1d so narrow widths never overlap or wrap. */
export function visibleTimelineTickHours(axisWidthPx: number | null, labelWidths?: Readonly<Record<number, number>>): number[] {
  const ticks = [...ALL_TIMELINE_TICKS];
  if (axisWidthPx === null || axisWidthPx <= 0) return ticks;
  const widthOf = (h: number) => labelWidths?.[h] ?? estimateTickWidth(h);
  const collides = (a: number, b: number) => {
    const centerGap = (timelinePosition(b * HOUR_MS) - timelinePosition(a * HOUR_MS)) * axisWidthPx;
    const [, aRight] = tickBounds(a, axisWidthPx, widthOf(a));
    const [bLeft] = tickBounds(b, axisWidthPx, widthOf(b));
    return centerGap < TIMELINE_MIN_TICK_GAP_PX || bLeft - aRight < TIMELINE_MIN_LABEL_GAP_PX;
  };
  for (const drop of DROPPABLE_TIMELINE_TICKS) {
    const idx = ticks.indexOf(drop);
    if (idx <= 0 || idx >= ticks.length - 1) continue;
    if (collides(ticks[idx - 1], ticks[idx]) || collides(ticks[idx], ticks[idx + 1])) {
      ticks.splice(idx, 1);
    }
  }
  return ticks;
}

/** Compact duration for narrow (<300px) cards and table cells: "45m", "1.5h", "2d 3h", "5d", or "Resets soon". */
export function compactResetText(resetsAt: string | undefined, now: number): string {
  const at = resetsAt ? Date.parse(resetsAt) : NaN;
  if (!Number.isFinite(at)) return "";
  const ms = at - now;
  if (ms <= 0) return tr("usage.due");
  const mins = Math.ceil(ms / 60_000);
  if (mins < 60) return tr("usage.tick_minutes", { m: Math.max(1, mins) });
  const hours = ms / HOUR_MS;
  if (hours < DAY_HOURS) return tr("usage.tick_hours", { h: Math.round(hours * 10) / 10 });
  const d = Math.floor(hours / DAY_HOURS);
  const h = Math.round(hours - d * DAY_HOURS);
  if (h >= DAY_HOURS) return tr("usage.tick_days", { d: d + 1 });
  return h === 0 ? tr("usage.tick_days", { d }) : tr("usage.tick_days_hours", { d, h });
}

function problemText(a: UsageAccount): string {
  switch (a.problem) {
    case "rejected":
      return tr("usage.problem_rejected");
    case "unreachable":
      return tr("usage.problem_unreachable");
    case "failed":
      return tr("usage.problem_failed");
    case "error":
      return tr("usage.problem_error");
    default:
      return "";
  }
}

/** Why a balance could not be read, or why the amount shown is the last one the manager had. */
function balanceProblemText(a: UsageAccount): string {
  if (!isBalanceAccount(a)) return "";
  switch (a.problem) {
    case "rejected":
      return a.balance ? tr("usage.balance_rejected_kept") : tr("usage.balance_rejected");
    case "unreachable":
      return a.balance ? tr("usage.balance_unreachable_kept") : tr("usage.balance_unreachable");
    case "failed":
      return a.balance ? tr("usage.balance_failed_kept") : tr("usage.balance_failed");
    default:
      return "";
  }
}

function connectHint(p: UnconnectedProvider): string {
  if (p.agent === "claude" && p.id === "claude" && !p.base_url) return tr("usage.connect_hint_claude");
  if (!p.base_url && (p.agent === "agy" || p.agent === "antigravity" || (p.agent === "codex" && p.id === "codex"))) return tr("usage.connect_hint_pending", { name: p.title });
  return tr("usage.connect_hint_other");
}

function connectPrompt(p: UnconnectedProvider): string {
  if (p.agent === "claude" && p.id === "claude" && !p.base_url) return tr("usage.connect_prompt_claude", { id: p.id });
  if (!p.base_url && (p.agent === "agy" || p.agent === "antigravity")) return tr("usage.connect_prompt_agy", { id: p.id });
  return tr("usage.connect_prompt_other", { name: p.title, id: p.id });
}

/** The body's blocks as the column choice needs them: each provider card's height (unsplittable) and the full-width
 *  blocks' total with the 12px gap each keeps. `natural`: the single-column height, the fallback without any card. */
function measureBody(bodyEl: HTMLElement | null, scrollEl: HTMLElement | null): { cards: number[]; spans: number; natural: number } {
  const cards: number[] = [];
  let spans = 0;
  if (bodyEl) {
    for (const child of Array.from(bodyEl.children) as HTMLElement[]) {
      const h = child.offsetHeight;
      if (h <= 0) continue;
      if (child.classList.contains("usage-popover-card")) cards.push(h);
      else spans += h + POPOVER_COL_GAP;
    }
  }
  const natural = cards.length || spans ? POPOVER_PAD_Y + spans + packedColumnHeight(cards, 1) : (scrollEl?.scrollHeight ?? 0);
  return { cards, spans, natural };
}

function sameLayout(a: UsagePopoverLayout, b: UsagePopoverLayout): boolean {
  return a.cols === b.cols && a.colWidth === b.colWidth && a.width === b.width && a.left === b.left && a.bottom === b.bottom && a.availH === b.availH && a.availW === b.availW;
}

function readViewport(): { width: number; height: number } {
  if (typeof window === "undefined") return { width: 1280, height: 800 };
  const vv = window.visualViewport;
  return {
    width: vv ? Math.min(window.innerWidth, vv.width) : window.innerWidth,
    height: vv ? Math.min(window.innerHeight, vv.height) : window.innerHeight,
  };
}

/** Every live account, grouped by provider with Cards/Table view toggle, collapsible unified reset timeline,
 *  and active providers without usage data yet. */
export function UsagePopover({
  open = true,
  accounts,
  unconnected = [],
  anchor,
  now: fixed,
  defaultViewMode,
  defaultTimelineOpen,
  onClose,
}: {
  open?: boolean;
  accounts: UsageAccount[];
  unconnected?: readonly UnconnectedProvider[];
  anchor?: HTMLElement | null;
  now?: number;
  /** Optional initial view mode override (for isolated previews / tests); when omitted, reads browser-local preference (default "cards"). */
  defaultViewMode?: UsageViewMode;
  /** Optional initial timeline fold override (for isolated previews / tests); when omitted, reads browser-local preference (default collapsed). */
  defaultTimelineOpen?: boolean;
  onClose?: () => void;
}) {
  const [phase, setPhase] = useState<"open" | "closing" | "closed">(open ? "open" : "closed");
  const [prevOpen, setPrevOpen] = useState(open);
  const [tick, setTick] = useState(() => Date.now());
  if (open !== prevOpen) {
    setPrevOpen(open);
    if (open) {
      setPhase("open");
      // Reopened: draw the countdowns from now, not from when it last closed.
      setTick(Date.now());
    } else if (phase === "open") setPhase("closing");
  }
  const dialogRef = useRef<HTMLDivElement>(null);
  const narrow = useMedia(NARROW_QUERY);
  const close = useCallback(() => onClose?.(), [onClose]);
  // On a narrow screen Back closes the popover before the drawer under it (lib/back-close.ts).
  useCloseOnBack(open, close, narrow);
  // Opened: the keyboard focus moves into the popover. Closed (Escape, Back, its trigger): it goes back to the trigger,
  // unless the close moved it elsewhere on purpose (a click on another control).
  useLayoutEffect(() => {
    const el = dialogRef.current;
    if (!el) return;
    if (open) {
      if (!el.contains(document.activeElement)) el.focus({ preventScroll: true });
    } else {
      returnFocus(el, anchor ?? null);
    }
  }, [open, anchor]);

  useEffect(() => {
    if (phase !== "closing") return;
    const ms = reducedMotion() ? REDUCED_CLOSE_MS : CLOSE_MS;
    const timer = setTimeout(() => setPhase("closed"), ms);
    return () => clearTimeout(timer);
  }, [phase]);

  const persistedViewMode = useUsageViewMode();
  const [localViewMode, setLocalViewMode] = useState<UsageViewMode | undefined>(defaultViewMode);
  const viewMode: UsageViewMode = defaultViewMode !== undefined ? (localViewMode ?? defaultViewMode) : persistedViewMode;
  const pickViewMode = useCallback(
    (next: UsageViewMode) => {
      if (defaultViewMode !== undefined) setLocalViewMode(next);
      else setUsageViewMode(next);
    },
    [defaultViewMode],
  );

  const persistedTimelineOpen = useUsageTimelineOpen();
  const [localTimelineOpen, setLocalTimelineOpen] = useState<boolean | undefined>(defaultTimelineOpen);
  const timelineOpen = defaultTimelineOpen !== undefined ? (localTimelineOpen ?? defaultTimelineOpen) : persistedTimelineOpen;
  const toggleTimeline = useCallback(() => {
    const next = !timelineOpen;
    if (defaultTimelineOpen !== undefined) setLocalTimelineOpen(next);
    else setUsageTimelineOpen(next);
  }, [defaultTimelineOpen, timelineOpen]);

  const [tableSort, setTableSort] = useState<UsageTableSort | null>(null);

  const hub = here();
  const manager = useShownManager();
  const aliasStore = useMemo(() => createUsageAliasStore(hub, manager), [hub, manager]);
  const aliases = aliasStore.useValue();
  const editAlias = (a: PopoverAccount) => {
    const value = window.prompt(tr("usage.alias_prompt", { name: a.name, limit: USAGE_ALIAS_MAX_LENGTH }), a.alias ?? "");
    if (value === null) return;
    if (Array.from(value.trim()).length > USAGE_ALIAS_MAX_LENGTH) {
      toast(tr("usage.alias_too_long", { limit: USAGE_ALIAS_MAX_LENGTH }), "error");
      return;
    }
    aliasStore.setAlias(a.id, value);
  };
  const groups = useMemo(() => groupByProvider(accounts, aliases), [accounts, aliases]);
  const hasAnyWindows = groups.some((g) => g.accounts.some((a) => a.windows.length > 0));
  const pinnedIds = usePinnedUsageAccounts();
  const pinnedSet = new Set(pinnedIds);
  const { items: selectedPins, custom: hasLivePins } = selectSidebarUsageItems(accounts, pinnedIds);
  const livePinnedCount = hasLivePins ? selectedPins.length : 0;
  useEffect(() => {
    if (fixed !== undefined || phase !== "open") return;
    const t = setInterval(() => setTick(Date.now()), 1000);
    return () => clearInterval(t);
  }, [fixed, phase]);
  const now = fixed ?? tick;

  const overlaps = useMemo(() => detectLocalExternalOverlaps(accounts, now, aliases), [accounts, now, aliases]);
  const tableModel = useMemo(
    () => (viewMode === "table" && accounts.length > 0 ? buildUsageTable(groups, tableSort) : null),
    [viewMode, accounts.length, groups, tableSort],
  );
  const tableColCount = tableModel ? tableModel.columns.length + (tableModel.hasOther ? 1 : 0) : 2;
  const timelineRows = useMemo(() => unifiedTimeline(groups, now), [groups, now]);

  const scrollRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const prevColsRef = useRef<number | null>(null);
  const [ready, setReady] = useState(false);
  const [reflow, setReflow] = useState<"none" | "a" | "b">("none");
  const [scrollState, setScrollState] = useState({ scrollable: false, fadeTop: false, fadeBottom: false });
  const [layout, setLayout] = useState<UsagePopoverLayout>(() => {
    const vp = readViewport();
    const rect = anchor?.getBoundingClientRect();
    return computeUsagePopoverLayout({
      naturalHeight: 0,
      viewportWidth: vp.width,
      viewportHeight: vp.height,
      anchorRect: rect,
      prevCols: null,
      providerCount: groups.length || 1,
      viewMode,
      tableColCount,
    });
  });

  const updateScrollEdges = useCallback((availH: number) => {
    const el = scrollRef.current;
    if (!el) return;
    const scrollable = el.scrollHeight > availH + 1;
    const fadeTop = scrollable && el.scrollTop > 1;
    const fadeBottom = scrollable && el.scrollTop + el.clientHeight < el.scrollHeight - 1;
    setScrollState((s) => (s.scrollable === scrollable && s.fadeTop === fadeTop && s.fadeBottom === fadeBottom ? s : { scrollable, fadeTop, fadeBottom }));
  }, []);

  const recompute = useCallback(() => {
    const vp = readViewport();
    const rect = anchor?.getBoundingClientRect();
    const measured = measureBody(bodyRef.current, scrollRef.current);
    const next = computeUsagePopoverLayout({
      naturalHeight: measured.natural,
      ...(measured.cards.length ? { cardHeights: measured.cards, spanHeight: measured.spans } : {}),
      viewportWidth: vp.width,
      viewportHeight: vp.height,
      anchorRect: rect,
      prevCols: viewMode === "table" ? null : prevColsRef.current,
      providerCount: groups.length || 1,
      viewMode,
      tableColCount,
    });
    if (prevColsRef.current !== null && next.cols !== prevColsRef.current) {
      setReflow((r) => (r === "a" ? "b" : "a"));
    }
    prevColsRef.current = next.cols;
    setLayout((cur) => (sameLayout(cur, next) ? cur : next));
    updateScrollEdges(next.availH);
  }, [anchor, groups.length, viewMode, tableColCount, updateScrollEdges]);

  useLayoutEffect(() => {
    if (phase !== "open") {
      prevColsRef.current = null;
      setReady(false);
      return;
    }
    recompute();
    const frame = typeof requestAnimationFrame === "function" ? requestAnimationFrame(() => setReady(true)) : 0;
    const onResize = () => recompute();
    window.addEventListener("resize", onResize);
    const vv = window.visualViewport;
    vv?.addEventListener("resize", onResize);
    let ro: ResizeObserver | undefined;
    if (typeof ResizeObserver !== "undefined" && bodyRef.current) {
      ro = new ResizeObserver(() => recompute());
      ro.observe(bodyRef.current);
    }
    return () => {
      if (frame && typeof cancelAnimationFrame === "function") cancelAnimationFrame(frame);
      window.removeEventListener("resize", onResize);
      vv?.removeEventListener("resize", onResize);
      ro?.disconnect();
    };
  }, [phase, recompute, accounts.length, unconnected.length, pinnedIds.length, viewMode, timelineOpen]);

  useLayoutEffect(() => {
    if (phase === "open") updateScrollEdges(layout.availH);
  }, [phase, layout, updateScrollEdges]);

  if (phase === "closed") return null;

  const askAssistant = (p: UnconnectedProvider) => {
    sendDraft("persona-new", { type: "replace", text: connectPrompt(p) });
    onClose?.();
    const onStarter = typeof location !== "undefined" && (location.pathname === "/persona/new" || (location.pathname === "/persona" && !getState().assistant?.main));
    if (!onStarter) navigate("/persona/new");
  };
  const bodyStyle = {
    "--usage-cols": String(layout.cols),
    "--usage-col-w": `${layout.colWidth}px`,
  } as CSSProperties;

  const dialog = (
    <div
      ref={dialogRef}
      role="dialog"
      aria-label={tr("usage.title")}
      tabIndex={-1}
      aria-hidden={phase === "closing" || undefined}
      inert={phase === "closing" || undefined}
      data-usage-popover={phase}
      data-state={phase}
      data-ready={ready ? "true" : undefined}
      data-cols={layout.cols}
      data-view={viewMode}
      className="usage-popover fixed z-50 rounded-card bg-surface shadow-overlay outline-none"
      style={{ left: `${layout.left}px`, bottom: `${layout.bottom}px`, width: `${layout.width}px` }}
      onAnimationEnd={(e) => {
        if (e.target === e.currentTarget && phase === "closing") setPhase("closed");
      }}
    >
      <div
        ref={scrollRef}
        data-usage-scroll
        data-scrollable={scrollState.scrollable ? "true" : "false"}
        data-fade-top={scrollState.fadeTop ? "true" : "false"}
        data-fade-bottom={scrollState.fadeBottom ? "true" : "false"}
        className="usage-popover-scroll p-3"
        style={{ maxHeight: `${layout.availH}px` }}
        onScroll={() => updateScrollEdges(layout.availH)}
      >
        <div
          className="usage-popover-content"
          data-reflow={reflow !== "none" ? reflow : undefined}
          onAnimationEnd={(e) => {
            if (e.target === e.currentTarget && reflow !== "none") setReflow("none");
          }}
        >
          <div ref={bodyRef} className="usage-popover-body" data-cols={layout.cols} style={bodyStyle}>
            {accounts.length > 0 && (
              <header data-usage-header className="usage-popover-span usage-popover-header flex items-start justify-between gap-2 border-b border-line pb-2">
                <div className="min-w-0 flex-1">
                  <h2 className="truncate text-meta font-semibold text-ink">{tr("usage.title")}</h2>
                  {hasAnyWindows && (
                    <p data-usage-sort-subtitle className="truncate text-submeta text-ink-3" title={tr("usage.card_sort_subtitle")}>
                      {tr("usage.card_sort_subtitle")}
                    </p>
                  )}
                </div>
                <div
                  role="group"
                  aria-label={tr("usage.view_label")}
                  data-usage-view-toggle
                  className="inline-flex shrink-0 items-center rounded-control border border-line bg-inset p-0.5 text-submeta"
                >
                  {(["cards", "table"] as const).map((mode) => {
                    const active = viewMode === mode;
                    return (
                      <button
                        key={mode}
                        type="button"
                        data-usage-view={mode}
                        aria-pressed={active}
                        onClick={() => pickViewMode(mode)}
                        className={cx(
                          "whitespace-nowrap rounded-chip px-2 py-0.5 transition-colors",
                          active ? "bg-surface font-medium text-ink shadow-hairline" : "text-ink-3 hover:text-ink",
                        )}
                      >
                        {mode === "cards" ? tr("usage.view_cards") : tr("usage.view_table")}
                      </button>
                    );
                  })}
                </div>
              </header>
            )}
            {pinnedIds.length > 0 && (accounts.length > 0 || unconnected.length > 0) && (
              <div data-usage-pinned-bar className="usage-popover-span usage-popover-pinned flex items-center justify-between gap-2 border-b border-line pb-2 text-submeta">
                <span className="min-w-0 truncate text-ink-3">
                  {livePinnedCount > 0 ? tr("usage.pinned_summary", { count: livePinnedCount }) : tr("usage.pinned_fallback")}
                </span>
                <button
                  type="button"
                  data-usage-reset-pins
                  onClick={() => resetPinnedUsageAccounts()}
                  className="shrink-0 font-medium whitespace-nowrap text-accent-ink hover:underline"
                >
                  {tr("usage.reset_pinned")}
                </button>
              </div>
            )}
            {viewMode === "table" && tableModel ? (
              <>
                {tableModel.rows.length > 0 && (
                  <UsageTable onEditAlias={editAlias} model={tableModel} sort={tableSort} onSort={setTableSort} pinnedSet={pinnedSet} overlaps={overlaps} now={now} />
                )}
                {groups.flatMap((g) => g.historicalReadings.map((a) => <HistoricalReading key={a.id} a={a} now={now} />))}
              </>
            ) : (
              groups.map((g) => {
                const title = providerTitle(g.provider);
                const headerAge = g.sampledAt ? sampleAge(g.sampledAt, now) : "";
                const mergedSingle = shouldMergeSingleAccountHeader(g, title);
                const singleAcct = mergedSingle ? g.accounts[0] : undefined;
                const singlePlan = singleAcct ? planNote(singleAcct) : "";
                const singlePinned = singleAcct ? pinnedSet.has(singleAcct.id) : false;
                const singleShowPin = singleAcct ? (isPinnableUsageAccount(singleAcct) || singlePinned) : false;
                const singleHeaderAge = singleAcct?.stale
                  ? tr("usage.stale_age", { age: sampleAge(singleAcct.sampled_at, now) })
                  : headerAge;
                const cardAlign: CardWindowAlign = {
                  hasReset: g.accounts.some((acct) =>
                    acct.windows.some((w) => Boolean(resetLabel(w.resets_at, now) || isWindowPaused(w))),
                  ),
                  hasDetail: g.accounts.some((acct) =>
                    acct.windows.some((w) => Boolean(
                      resetLabel(w.resets_at, now)
                        || isWindowPaused(w)
                        || accountPace({ stale: Boolean(acct.stale) }, w, now)?.state === "over",
                    )),
                  ),
                  hasPinnedWindow: g.accounts.some((acct) =>
                    acct.windows.some((w) => pinnedSet.has(usageWindowPinId(acct.id, w))),
                  ),
                };
                return (
                  <section
                    key={g.provider}
                    data-usage-provider={g.provider}
                    aria-label={title}
                    className="usage-popover-card @container flex flex-col gap-2 rounded-control border border-line p-2.5"
                  >
                    <header className="flex items-center gap-1.5">
                      <span className="flex min-w-0 flex-1 items-center gap-1.5">
                        <span aria-hidden data-provider-dot={g.provider} className="size-2 shrink-0 rounded-full" style={{ background: providerHue(g.provider) }} />
                        <h3 className="truncate text-meta font-semibold text-ink">{title}</h3>
                        {mergedSingle && singlePlan && (
                          <span data-usage-plan className="max-w-36 shrink-0 truncate whitespace-nowrap rounded-chip bg-inset px-1.5 py-px text-submeta font-medium text-ink-2" title={singlePlan}>
                            {singlePlan}
                          </span>
                        )}
                        {(g.accounts.length > 1 || g.historicalReadings.length > 0) && (
                          <span className="shrink-0 whitespace-nowrap text-submeta text-ink-3">{tr("usage.provider_accounts", { count: g.accounts.length })}</span>
                        )}
                      </span>
                      {singleHeaderAge && (
                        <span data-usage-card-age className="shrink-0 whitespace-nowrap text-submeta text-ink-3">
                          {singleHeaderAge}
                        </span>
                      )}
                      {singleAcct && <AliasButton account={singleAcct} onEdit={editAlias} />}
                      {mergedSingle && singleAcct && singleShowPin && (
                        <PinToggle
                          pinned={singlePinned}
                          pinAttr={singleAcct.id}
                          label={singlePinned ? tr("usage.unpin_account", { name: singleAcct.name }) : tr("usage.pin_account", { name: singleAcct.name })}
                          onClick={() => togglePinnedUsageAccount(singleAcct.id)}
                        />
                      )}
                    </header>
                    {g.accounts.map((a, i) => (
                      <div key={a.id} className={cx("flex flex-col", i > 0 && "border-t border-line pt-2")}>
                        <AccountSection
                          a={a}
                          now={now}
                          pinnedSet={pinnedSet}
                          headerAge={headerAge}
                          onEditAlias={editAlias}
                          mergedHeader={mergedSingle}
                          overlapWith={overlaps.get(a.id)}
                          align={cardAlign}
                        />
                      </div>
                    ))}
                    {g.historicalReadings.map((a) => <HistoricalReading key={a.id} a={a} now={now} />)}
                  </section>
                );
              })
            )}
            <UnifiedTimelineSection groups={groups} rows={timelineRows} open={timelineOpen} onToggle={toggleTimeline} now={now} />
            {unconnected.map((p, i) => (
              <section key={`provider:${p.id}`} data-usage-unconnected-card={p.id} aria-label={p.title} className={cx("usage-popover-span flex flex-col gap-1 text-xs", (accounts.length > 0 || i > 0) && "usage-popover-unconnected border-t border-line pt-2.5")}>
                <div className="flex items-baseline gap-2">
                  <h3 className="min-w-0 flex-1 truncate font-medium text-ink">{p.title}</h3>
                  <button
                    type="button"
                    data-usage-connect={p.id}
                    onClick={() => askAssistant(p)}
                    className="shrink-0 font-medium whitespace-nowrap text-accent-ink hover:underline"
                  >
                    {tr("usage.connect_action")}
                  </button>
                </div>
                <div className="leading-snug text-ink-3">{connectHint(p)}</div>
              </section>
            ))}
          </div>
        </div>
      </div>
    </div>
  );

  return anchor && typeof document !== "undefined" ? createPortal(dialog, document.body) : dialog;
}

interface CardWindowAlign {
  hasReset: boolean;
  hasDetail: boolean;
  hasPinnedWindow: boolean;
}

function windowPinLabel(name: string, w: UsageWindow, pinned: boolean): string {
  const scope = w.model?.trim()
    || (w.title?.trim() && w.title.trim().toLowerCase() !== w.label.trim().toLowerCase() ? w.title.trim() : "")
    || w.group?.trim()
    || "";
  if (scope) {
    return pinned
      ? tr("usage.unpin_window_model", { name, window: w.label, model: scope })
      : tr("usage.pin_window_model", { name, window: w.label, model: scope });
  }
  return pinned ? tr("usage.unpin_window", { name, window: w.label }) : tr("usage.pin_window", { name, window: w.label });
}

function AliasButton({ account, onEdit }: { account: PopoverAccount; onEdit: (a: PopoverAccount) => void }) {
  if (!canAliasUsageAccount(account)) return null;
  const label = tr("usage.edit_alias", { name: account.alias ?? account.name });
  return (
    <span data-usage-alias={account.id} className="shrink-0">
      <IconButton icon="pencil" size={24} iconSize={13} label={label} onClick={() => onEdit(account)} />
    </span>
  );
}

function PinToggle({
  pinned,
  label,
  onClick,
  pinAttr,
  windowAttr,
  pinOverlay,
  iconSize = 12,
  className,
}: {
  pinned: boolean;
  label: string;
  onClick: () => void;
  pinAttr?: string;
  windowAttr?: string;
  pinOverlay?: boolean;
  iconSize?: number;
  className?: string;
}) {
  return (
    <button
      type="button"
      data-usage-pin={pinAttr}
      data-usage-pin-window={windowAttr}
      data-pin-pinned={windowAttr ? (pinned ? "true" : "false") : undefined}
      data-pin-overlay={pinOverlay ? "true" : undefined}
      aria-pressed={pinned}
      aria-label={label}
      title={pinned ? tr("usage.unpin_title") : tr("usage.pin_title")}
      onClick={onClick}
      className={cx(
        "inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-control transition-[color,background-color,opacity]",
        pinned ? "bg-hover-2 text-accent-ink" : "text-ink-3 hover:bg-hover hover:text-ink",
        className,
      )}
    >
      <Icon name="pin" size={iconSize} />
    </button>
  );
}

/** A file sample is inspectable even when the hub has no current subscription reading. */
function HistoricalReading({ a, now }: { a: UsageAccount; now: number }) {
  return (
    <details className="rounded-control bg-inset px-2.5 text-submeta text-ink-2">
      <summary className="min-h-11 cursor-pointer break-words py-3.5 font-medium">
        {[tr("usage.codex_local_reading"), sourceNames(a).join(" · "), sampleAge(a.sampled_at, now)].filter(Boolean).join(" · ")}
      </summary>
      <p className="mt-1 leading-snug">{tr("usage.codex_local_explanation")}</p>
      <time dateTime={a.sampled_at} className="text-ink-3">{formatDate(a.sampled_at, { dateStyle: "medium", timeStyle: "short" })}</time>
      <dl className="mt-1 flex flex-col gap-1 pb-2">
        {a.windows.map((w) => (
          <div key={windowKey(w)} className="flex flex-wrap items-baseline justify-between gap-x-2">
            <dt>{w.label}</dt>
            <dd className="flex flex-wrap justify-end gap-x-2 tabular-nums">
              <span>{tr("usage.left_short", { percent: shown(remainingPercent(w.used_percent)) })}</span>
              {w.resets_at && (
                <time dateTime={w.resets_at} className="text-ink-3">
                  {tr("usage.reset_at", { time: formatDate(w.resets_at, { dateStyle: "short", timeStyle: "short" }) })}
                </time>
              )}
            </dd>
          </div>
        ))}
      </dl>
    </details>
  );
}

function AccountSection({
  a,
  now,
  pinnedSet,
  headerAge = "",
  onEditAlias,
  mergedHeader = false,
  overlapWith,
  align,
}: {
  a: PopoverAccount;
  onEditAlias: (a: PopoverAccount) => void;
  now: number;
  pinnedSet: ReadonlySet<string>;
  headerAge?: string;
  mergedHeader?: boolean;
  overlapWith?: string;
  align: CardWindowAlign;
}) {
  const [staleOverlapOpen, setStaleOverlapOpen] = useState(false);
  const name = a.alias ?? a.name;
  const from = sourceNames(a);
  const plan = planNote(a);
  const pinned = pinnedSet.has(a.id);
  const showPin = isPinnableUsageAccount(a) || pinned;
  // A balance's failure is about the provider's balance route, not a usage gateway's stored windows (#11825).
  const problem = isBalanceAccount(a) ? balanceProblemText(a) : problemText(a);
  const acctAge = sampleAge(a.sampled_at, now);
  const showAge = !mergedHeader && Boolean(acctAge) && (Boolean(a.stale) || acctAge !== headerAge);
  const sections = groupAccountWindows(a);
  const collapseStaleOverlap = Boolean(a.stale && overlapWith);

  const renderBody = () => (
    <>
      {a.balance && (
        <div data-usage-balance className="flex items-baseline justify-between gap-2 rounded-control bg-inset px-2.5 py-2">
          <span className="text-xs text-ink-3">{tr("usage.balance_label")}</span>
          <span className={cx("text-sm font-medium tabular-nums", a.stale ? "text-ink-2" : balanceEmpty(a.balance) ? "text-orange" : "text-ink")}>{balanceText(a.balance)}</span>
        </div>
      )}
      {!a.balance && isBalanceAccount(a) && <div data-usage-balance-unavailable className="text-xs text-orange">{tr("usage.balance_unavailable")}</div>}
      {sections.map((section, idx) => {
        if (section.kind === "plain") {
          return (
            <div key={`plain:${idx}`} className="flex flex-col gap-1.5">
              {section.windows.map((w) => {
                const pinId = usageWindowPinId(a.id, w);
                return <WindowRow key={windowKey(w)} name={name} w={w} stale={Boolean(a.stale)} now={now} pinId={pinId} pinned={pinnedSet.has(pinId)} align={align} />;
              })}
            </div>
          );
        }
        if (section.kind === "inline-model") {
          const pinId = usageWindowPinId(a.id, section.window);
          return (
            <WindowRow
              key={windowKey(section.window)}
              name={name}
              w={section.window}
              inlineModel={section.model}
              stale={Boolean(a.stale)}
              now={now}
              pinId={pinId}
              pinned={pinnedSet.has(pinId)}
              align={align}
            />
          );
        }
        return (
          <div key={section.key} data-usage-window-group={section.title} className="flex flex-col gap-1 border-l-2 border-line pl-2">
            <div
              data-usage-group-title
              data-usage-model={section.source === "model" ? section.title : undefined}
              className="truncate text-submeta font-medium text-ink-2"
              title={section.title}
            >
              {section.title}
            </div>
            <div className="flex flex-col gap-1">
              {section.windows.map((w) => {
                const pinId = usageWindowPinId(a.id, w);
                return <WindowRow key={windowKey(w)} name={name} w={w} groupContext={section.title} stale={Boolean(a.stale)} now={now} pinId={pinId} pinned={pinnedSet.has(pinId)} align={align} />;
              })}
            </div>
          </div>
        );
      })}
    </>
  );

  if (collapseStaleOverlap && overlapWith) {
    const staleSummary = tr("usage.overlap_stale_summary", { name: overlapWith });
    const fullTip = [name, acctAge ? tr("usage.stale_age", { age: acctAge }) : "", tr("usage.overlap_hint_title", { name: overlapWith })].filter(Boolean).join(" · ");
    return (
      <section aria-label={name} data-usage-stale="true" data-usage-stale-overlap={staleOverlapOpen ? "expanded" : "collapsed"} className="flex flex-col gap-1">
        <div className="flex items-center gap-1">
          <button
            type="button"
            data-usage-stale-overlap-toggle
            aria-expanded={staleOverlapOpen}
            aria-label={staleOverlapOpen ? tr("usage.overlap_stale_hide", { name }) : tr("usage.overlap_stale_show", { name })}
            title={fullTip}
            onClick={() => setStaleOverlapOpen((v) => !v)}
            className="flex min-w-0 flex-1 items-center gap-1.5 rounded-control px-1 py-0.5 text-left text-submeta text-ink-3 transition-colors hover:bg-hover/60"
          >
            <Icon name="info" size={11} className="shrink-0" />
            <span className="max-w-[42%] shrink-0 truncate font-medium text-ink-2">{name}</span>
            <span data-usage-overlap-hint className="min-w-0 flex-1 truncate text-ink-3">
              {staleSummary}
            </span>
            <span aria-hidden className={cx("inline-flex shrink-0 items-center transition-transform", staleOverlapOpen ? "rotate-180" : "-rotate-90")}>
              <Icon name="chevron" size={10} />
            </span>
          </button>
          <AliasButton account={a} onEdit={onEditAlias} />
          {pinned && (
            <PinToggle
              pinned={pinned}
              pinAttr={a.id}
              label={tr("usage.unpin_account", { name })}
              onClick={() => togglePinnedUsageAccount(a.id)}
            />
          )}
        </div>
        <Collapse open={staleOverlapOpen} lazy>
          <div data-usage-stale-overlap-body className="flex flex-col gap-1.5 pt-1">
            <div className="flex min-w-0 items-center justify-between gap-2 text-submeta text-ink-3">
              <span className="flex min-w-0 items-center gap-1.5">
                {plan && (
                  <span data-usage-plan className="shrink-0 whitespace-nowrap rounded-chip bg-inset px-1.5 py-px font-medium text-ink-2">
                    {plan}
                  </span>
                )}
                {from.length > 0 && (
                  <span data-usage-source className="min-w-0 truncate" title={tr("usage.sources_title", { sources: from.join(" · ") })}>
                    {from.join(" · ")}
                  </span>
                )}
              </span>
              <span className="flex shrink-0 items-center gap-1">
                {acctAge && <span data-usage-account-age className="whitespace-nowrap">{tr("usage.stale_age", { age: acctAge })}</span>}
                {showPin && !pinned && (
                  <PinToggle
                    pinned={false}
                    pinAttr={a.id}
                    label={tr("usage.pin_account", { name })}
                    onClick={() => togglePinnedUsageAccount(a.id)}
                  />
                )}
              </span>
            </div>
            {problem && <div className="text-submeta leading-snug text-orange">{problem}</div>}
            {a.headline?.trim() && <div data-usage-headline className="text-submeta leading-snug text-ink-2">{a.headline.trim()}</div>}
            {a.notice?.trim() && <div data-usage-notice className="text-submeta leading-snug text-ink-3">{a.notice.trim()}</div>}
            {renderBody()}
          </div>
        </Collapse>
      </section>
    );
  }

  const hasSubMeta = from.length > 0 || showAge || Boolean(overlapWith || problem || a.headline?.trim() || a.notice?.trim());
  return (
    <section
      aria-label={name}
      data-usage-stale={a.stale ? "true" : undefined}
      data-usage-merged-header={mergedHeader ? "true" : undefined}
      className="flex flex-col gap-1.5"
    >
      {(!mergedHeader || hasSubMeta) && (
        <header className="flex flex-col gap-0.5">
          {!mergedHeader && (
            <div className="flex items-center gap-1.5">
              <h3 className={cx("min-w-0 flex-1 truncate text-label font-medium", a.stale ? "text-ink-2" : "text-ink")} title={name}>
                {name}
              </h3>
              {plan && (
                <span data-usage-plan className="max-w-36 shrink-0 truncate whitespace-nowrap rounded-chip bg-inset px-1.5 py-px text-submeta font-medium text-ink-2" title={plan}>
                  {plan}
                </span>
              )}
              <AliasButton account={a} onEdit={onEditAlias} />
              {showPin && (
                <PinToggle
                  pinned={pinned}
                  pinAttr={a.id}
                  label={pinned ? tr("usage.unpin_account", { name }) : tr("usage.pin_account", { name })}
                  onClick={() => togglePinnedUsageAccount(a.id)}
                />
              )}
            </div>
          )}
          {(from.length > 0 || showAge) && (
            <div className="flex min-w-0 items-baseline justify-between gap-2 text-submeta text-ink-3">
              {from.length > 0 ? (
                <span data-usage-source className="min-w-0 truncate" title={tr("usage.sources_title", { sources: from.join(" · ") })}>
                  {from.join(" · ")}
                </span>
              ) : (
                <span aria-hidden />
              )}
              {showAge && (
                <span data-usage-account-age className="shrink-0 whitespace-nowrap">
                  {a.stale ? tr("usage.stale_age", { age: acctAge }) : acctAge}
                </span>
              )}
            </div>
          )}
          {overlapWith && (
            <div
              data-usage-overlap-hint
              className="flex min-w-0 items-center gap-1 text-submeta text-ink-3"
              title={tr("usage.overlap_hint_title", { name: overlapWith })}
            >
              <Icon name="info" size={11} className="shrink-0" />
              <span className="min-w-0 truncate">{tr("usage.overlap_hint", { name: overlapWith })}</span>
            </div>
          )}
          {problem && <div className="text-submeta leading-snug text-orange">{problem}</div>}
          {a.headline?.trim() && <div data-usage-headline className="text-submeta leading-snug text-ink-2">{a.headline.trim()}</div>}
          {a.notice?.trim() && <div data-usage-notice className="text-submeta leading-snug text-ink-3">{a.notice.trim()}</div>}
        </header>
      )}
      {renderBody()}
    </section>
  );
}

function UsageTable({
  model,
  onEditAlias,
  sort,
  onSort,
  pinnedSet,
  overlaps,
  now,
}: {
  onEditAlias: (a: PopoverAccount) => void;
  model: ReturnType<typeof buildUsageTable>;
  sort: UsageTableSort | null;
  onSort: (next: UsageTableSort | null) => void;
  pinnedSet: ReadonlySet<string>;
  overlaps: ReadonlyMap<string, string>;
  now: number;
}) {
  const [expandedStaleIds, setExpandedStaleIds] = useState<Set<string>>(() => new Set());
  const toggleStaleRow = (id: string) =>
    setExpandedStaleIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const dataColSpan = Math.max(1, model.columns.length + (model.hasOther ? 1 : 0));
  const accountColPct = dataColSpan <= 2 ? 38 : 34;
  const dataColPct = (100 - accountColPct) / dataColSpan;
  const cycleSort = (key: "account" | string) => {
    const first: "asc" | "desc" = "asc";
    if (!sort || sort.key !== key) onSort({ key, dir: first });
    else if (sort.dir === first) onSort({ key, dir: "desc" });
    else onSort(null);
  };
  const sortHead = (key: "account" | string, label: string, sticky = false) => {
    const active = sort?.key === key;
    const ariaSort = active ? (sort.dir === "desc" ? "descending" : "ascending") : "none";
    const arrow = active ? (sort.dir === "desc" ? "↓" : "↑") : "";
    return (
      <th
        key={key}
        scope="col"
        aria-sort={ariaSort}
        className={cx(
          "border-b border-line px-2.5 py-1.5 text-left text-submeta font-medium whitespace-nowrap text-ink-3",
          sticky ? "usage-table-head-sticky" : "bg-inset",
        )}
      >
        <button
          type="button"
          data-table-sort={key}
          aria-label={tr("usage.table_sort_by", { column: label })}
          onClick={() => cycleSort(key)}
          className={cx("inline-flex items-center gap-1 whitespace-nowrap transition-colors hover:text-ink", active && "text-ink")}
        >
          <span>{label}</span>
          {arrow && <span aria-hidden>{arrow}</span>}
        </button>
      </th>
    );
  };

  return (
    <div data-usage-table className="usage-popover-span usage-table-wrap rounded-control border border-line bg-surface">
      <table className="usage-table text-submeta">
        <colgroup>
          <col style={{ width: `${accountColPct}%` }} />
          {model.columns.map((c) => (
            <col key={c.key} style={{ width: `${dataColPct}%` }} />
          ))}
          {model.hasOther && <col style={{ width: `${dataColPct}%` }} />}
        </colgroup>
        <thead>
          <tr>
            {sortHead("account", tr("usage.table_account"), true)}
            {model.columns.map((c) => sortHead(c.key, c.head))}
            {model.hasOther && (
              <th scope="col" className="border-b border-line bg-inset px-2.5 py-1.5 text-left text-submeta font-medium whitespace-nowrap text-ink-3">
                {tr("usage.table_other")}
              </th>
            )}
          </tr>
        </thead>
        <tbody>
          {model.rows.map((row, idx) => {
            const { account: a, provider, plan } = row;
            const name = a.alias ?? a.name;
            const from = sourceNames(a);
            const showPin = isPinnableUsageAccount(a) || pinnedSet.has(a.id);
            const problem = isBalanceAccount(a) ? balanceProblemText(a) : problemText(a);
            const overlapWith = overlaps.get(a.id);
            const providerBreak = !sort && idx > 0 && model.rows[idx - 1].provider !== provider;
            const rowBorder = idx === 0 ? "" : providerBreak ? "border-t border-line-strong" : "border-t border-line";
            const age = a.stale ? tr("usage.stale_age", { age: sampleAge(a.sampled_at, now) }) : sampleAge(a.sampled_at, now);
            const metaLine = [from.join(" · "), age].filter(Boolean).join(" · ");
            const isStaleOverlapCollapsed = Boolean(a.stale && overlapWith && !expandedStaleIds.has(a.id));

            if (isStaleOverlapCollapsed && overlapWith) {
              return (
                <tr
                  key={a.id}
                  data-usage-table-row={a.id}
                  data-usage-provider={provider}
                  data-usage-stale="true"
                  data-usage-stale-overlap="collapsed"
                >
                  <td colSpan={1 + dataColSpan} className={cx("px-2.5 py-1.5", rowBorder)}>
                    <button
                      type="button"
                      data-usage-stale-overlap-toggle
                      aria-expanded={false}
                      aria-label={tr("usage.overlap_stale_show", { name })}
                      title={[name, age, tr("usage.overlap_hint_title", { name: overlapWith })].filter(Boolean).join(" · ")}
                      onClick={() => toggleStaleRow(a.id)}
                      className="flex w-full items-center gap-1.5 rounded-control text-left text-submeta text-ink-3 transition-colors hover:text-ink"
                    >
                      <span aria-hidden data-provider-dot={provider} className="size-1.5 shrink-0 rounded-full opacity-60" style={{ background: providerHue(provider) }} />
                      <Icon name="info" size={11} className="shrink-0" />
                      <span className="max-w-[42%] shrink-0 truncate font-medium text-ink-2">{name}</span>
                      <span data-usage-overlap-hint className="min-w-0 flex-1 truncate">
                        {tr("usage.overlap_stale_summary", { name: overlapWith })}
                      </span>
                      <span aria-hidden className="inline-flex shrink-0 -rotate-90 items-center">
                        <Icon name="chevron" size={10} />
                      </span>
                    </button>
                  </td>
                </tr>
              );
            }

            return (
              <tr
                key={a.id}
                data-usage-table-row={a.id}
                data-usage-provider={provider}
                data-usage-stale={a.stale ? "true" : undefined}
                data-usage-stale-overlap={a.stale && overlapWith ? "expanded" : undefined}
              >
                <td className={cx("usage-table-sticky px-2.5 py-2 align-top", rowBorder)}>
                  <div className="flex items-start gap-1.5">
                    <span aria-hidden data-provider-dot={provider} className="mt-1.5 size-2 shrink-0 rounded-full" style={{ background: providerHue(provider) }} />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1">
                        <span className={cx("min-w-0 flex-1 truncate text-label font-medium", a.stale ? "text-ink-2" : "text-ink")} title={name}>
                          {name}
                        </span>
                      </div>
                      {(plan || metaLine || canAliasUsageAccount(a)) && (
                        <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-submeta text-ink-3">
                          {plan && (
                            <span data-usage-plan className="shrink-0 whitespace-nowrap rounded-chip bg-inset px-1 py-px text-submeta font-medium text-ink-2">
                              {plan}
                            </span>
                          )}
                          <AliasButton account={a} onEdit={onEditAlias} />
                          {metaLine && (
                            <span data-usage-source className="min-w-0 truncate" title={metaLine}>
                              {metaLine}
                            </span>
                          )}
                        </div>
                      )}
                      {overlapWith && (
                        a.stale ? (
                          <button
                            type="button"
                            data-usage-stale-overlap-toggle
                            aria-expanded={true}
                            aria-label={tr("usage.overlap_stale_hide", { name })}
                            onClick={() => toggleStaleRow(a.id)}
                            className="flex max-w-full items-center gap-1 text-submeta text-ink-3 hover:text-ink"
                          >
                            <Icon name="info" size={10} className="shrink-0" />
                            <span data-usage-overlap-hint className="min-w-0 truncate">{tr("usage.overlap_hint", { name: overlapWith })}</span>
                          </button>
                        ) : (
                          <div
                            data-usage-overlap-hint
                            className="flex items-center gap-1 text-submeta text-ink-3"
                            title={tr("usage.overlap_hint_title", { name: overlapWith })}
                          >
                            <Icon name="info" size={10} className="shrink-0" />
                            <span className="min-w-0 truncate">{tr("usage.overlap_hint", { name: overlapWith })}</span>
                          </div>
                        )
                      )}
                      {problem && <div className="text-submeta leading-snug text-orange">{problem}</div>}
                      {a.headline?.trim() && <div data-usage-headline className="text-submeta leading-snug text-ink-2">{a.headline.trim()}</div>}
                      {!row.noticeOnly && a.notice?.trim() && <div data-usage-notice className="text-submeta leading-snug text-ink-3">{a.notice.trim()}</div>}
                    </div>
                    {showPin && (
                      <PinToggle
                        pinned={pinnedSet.has(a.id)}
                        pinAttr={a.id}
                        label={pinnedSet.has(a.id) ? tr("usage.unpin_account", { name }) : tr("usage.pin_account", { name })}
                        onClick={() => togglePinnedUsageAccount(a.id)}
                      />
                    )}
                  </div>
                </td>
                {row.bareBalance ? (
                  <td colSpan={dataColSpan} className={cx("px-2 py-2 align-middle", rowBorder)}>
                    {a.balance ? (
                      <div data-usage-balance className="flex items-baseline gap-2">
                        <span className="text-submeta text-ink-3">{tr("usage.balance_label")}</span>
                        <span className={cx("text-label font-medium tabular-nums", a.stale ? "text-ink-2" : balanceEmpty(a.balance) ? "text-orange" : "text-ink")}>
                          {balanceText(a.balance)}
                        </span>
                      </div>
                    ) : (
                      <div data-usage-balance-unavailable className="text-submeta text-orange">{tr("usage.balance_unavailable")}</div>
                    )}
                  </td>
                ) : row.noticeOnly ? (
                  <td colSpan={dataColSpan} className={cx("px-2 py-2 align-middle text-submeta text-ink-3", rowBorder)}>
                    {a.notice?.trim() && <div data-usage-notice>{a.notice.trim()}</div>}
                  </td>
                ) : (
                  <>
                    {model.columns.map((col) => {
                      const list = row.cells[col.key] ?? [];
                      return (
                        <td key={col.key} data-table-col={col.key} className={cx("px-2 py-2 align-top", rowBorder)}>
                          {list.length > 0 ? (
                            <div className="flex flex-col gap-1.5">
                              {list.map((w) => {
                                const pinId = usageWindowPinId(a.id, w);
                                return (
                                  <TableWindowCell
                                    key={windowKey(w)}
                                    name={name}
                                    w={w}
                                    subLabel={tableWindowSubLabel(w, col.head, list.length, tr("usage.table_plan_window"))}
                                    stale={Boolean(a.stale)}
                                    now={now}
                                    pinId={pinId}
                                    pinned={pinnedSet.has(pinId)}
                                  />
                                );
                              })}
                            </div>
                          ) : (
                            <span aria-hidden className="text-ink-3">—</span>
                          )}
                        </td>
                      );
                    })}
                    {model.hasOther && (
                      <td data-table-col="other" className={cx("px-2 py-2 align-top", rowBorder)}>
                        <div className="flex flex-col gap-1.5">
                          {row.other.map((w) => {
                            const pinId = usageWindowPinId(a.id, w);
                            const sub = tableWindowSubLabel(w, "", 1, tr("usage.table_plan_window"));
                            return (
                              <TableWindowCell
                                key={windowKey(w)}
                                name={name}
                                w={w}
                                subLabel={{
                                  text: sub.text ? `${w.label} · ${sub.text}` : w.label,
                                  full: [w.group?.trim(), w.model?.trim(), sub.full, w.label].filter((v, i, arr) => Boolean(v) && arr.findIndex((x) => x?.toLowerCase() === v?.toLowerCase()) === i).join(" · "),
                                }}
                                stale={Boolean(a.stale)}
                                now={now}
                                pinId={pinId}
                                pinned={pinnedSet.has(pinId)}
                              />
                            );
                          })}
                          {a.balance && (
                            <div data-usage-balance className="text-submeta font-medium tabular-nums text-ink">
                              {balanceText(a.balance)}
                            </div>
                          )}
                        </div>
                      </td>
                    )}
                  </>
                )}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function TableWindowCell({
  name,
  w,
  subLabel,
  stale,
  now,
  pinId,
  pinned,
}: {
  name: string;
  w: UsageWindow;
  subLabel: { text: string; full: string };
  stale: boolean;
  now: number;
  pinId: string;
  pinned: boolean;
}) {
  const p = accountPace({ stale }, w, now);
  const remaining = remainingPercent(w.used_percent);
  const reserve = protectReserve(w.protect_percent);
  const tone = remainingTone(remaining, reserve ?? 0);
  const paused = isWindowPaused(w);
  const expectedRemaining = p ? remainingPercent(p.expected) : undefined;
  const paceVerdict = p?.state === "over" ? tr("usage.pace_over") : "";
  const fullReset = resetLabel(w.resets_at, now);
  const shortReset = compactResetText(w.resets_at, now);
  const pausedLabel = paused ? tr("usage.paused") : "";
  const note = w.note?.trim() ?? "";
  const tip = [subLabel.full, shown(remaining), paceVerdict, pausedLabel, fullReset, note].filter(Boolean).join(" · ");
  const scope = w.model?.trim() || w.group?.trim() || w.title?.trim();
  const meterBase = scope
    ? tr("usage.window_model_remaining_label", { name, window: w.label, model: scope, percent: shown(remaining) })
    : tr("usage.window_remaining_label", { name, window: w.label, percent: shown(remaining) });
  const inlineReset = !subLabel.text;
  const pinLabel = windowPinLabel(name, w, pinned);
  return (
    <div data-window-row={w.label} data-window-paused={paused ? "true" : undefined} className="usage-table-cell flex min-w-0 flex-col gap-1" title={tip}>
      <div className="relative flex min-w-0 items-center justify-between gap-1">
        {subLabel.text ? (
          <>
            <span
              data-usage-model={w.model?.trim() ? w.model.trim() : undefined}
              className="min-w-0 flex-1 truncate text-submeta text-ink-2"
              title={subLabel.full}
            >
              {subLabel.text}
            </span>
            <span data-usage-percent className={cx("shrink-0 whitespace-nowrap font-medium tabular-nums", stale ? "text-ink-2" : TONE_TEXT[tone])}>
              {shown(remaining)}
            </span>
          </>
        ) : (
          <>
            <span data-usage-percent className={cx("shrink-0 whitespace-nowrap font-medium tabular-nums", stale ? "text-ink-2" : TONE_TEXT[tone])}>
              {shown(remaining)}
            </span>
            {(shortReset || paused) ? (
              <span data-usage-reset data-usage-paused={paused ? "true" : undefined} className="min-w-0 flex-1 truncate text-right text-submeta text-ink-3">
                {paused && <span className="font-medium text-red">{pausedLabel}</span>}
                {paused && shortReset && <span aria-hidden> · </span>}
                {shortReset}
              </span>
            ) : (
              <span className="flex-1" />
            )}
          </>
        )}
        <PinToggle
          pinned={pinned}
          windowAttr={pinId}
          pinOverlay={!pinned}
          label={pinLabel}
          onClick={() => togglePinnedUsageAccount(pinId)}
          iconSize={10}
          className="usage-win-pin h-4 w-4"
        />
      </div>
      <div className={cx("min-w-0", stale && "opacity-70", reserve !== null && "pt-0.5")}>
        <Meter
          remaining={remaining}
          expectedRemaining={expectedRemaining}
          reserve={reserve ?? undefined}
          barClass={TONE_BG[tone]}
          label={paceVerdict ? `${meterBase}, ${paceVerdict}` : meterBase}
        />
      </div>
      {!inlineReset && (shortReset || paused) && (
        <div data-usage-reset data-usage-paused={paused ? "true" : undefined} className="truncate text-submeta text-ink-3">
          {paused && <span className="font-medium text-red">{pausedLabel}</span>}
          {paused && shortReset && <span aria-hidden> · </span>}
          {shortReset}
        </div>
      )}
      {note && <div data-usage-window-note className="truncate text-submeta text-ink-3" title={note}>{note}</div>}
    </div>
  );
}

const LANE_COLOR = { short: "var(--hue-teal)", long: "var(--hue-blue)" } as const;

/** Callback-ref width observer so re-mounting the timeline axis while the popover stays open rebinds ResizeObserver. */
function useAxisWidth(): [(el: HTMLDivElement | null) => void, number | null] {
  const [node, setNode] = useState<HTMLDivElement | null>(null);
  const [width, setWidth] = useState<number | null>(null);
  useEffect(() => {
    if (!node) {
      setWidth(null);
      return;
    }
    const measure = (w: number) => setWidth(w > 0 ? Math.round(w) : null);
    measure(node.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([entry]) => {
      if (entry) measure(entry.contentRect.width);
    });
    ro.observe(node);
    return () => ro.disconnect();
  }, [node]);
  return [setNode, width];
}

function uniqueLaneLabels(rows: readonly UnifiedTimelineRow[], long: boolean): string[] {
  const out: string[] = [];
  for (const r of rows) {
    for (const l of r.lanes) {
      if (l.long === long && !out.includes(l.label)) out.push(l.label);
    }
  }
  return out;
}

function formatLaneSummary(l: TimelineLane, now: number): string {
  const paused = l.paused ? ` · ${tr("usage.paused")}` : "";
  if (l.models.length > 0) {
    const entries: string[] = [];
    if (l.planResetsAt) entries.push(`${l.label} · ${resetLabel(l.planResetsAt, now)}`);
    for (const m of l.models) entries.push(`${m.model} · ${resetLabel(m.resetsAt, now)}`);
    return `${l.label}${paused} (${entries.join(", ")})`;
  }
  return `${l.label}${paused} · ${resetLabel(l.resetsAt, now)}`;
}

/** Collapsible reset-timeline footer: defaults to a compact one-line next-reset summary and opens the full
 *  UnifiedTimeline on click so multi-account timelines never dominate the popover unprompted. */
function UnifiedTimelineSection({
  groups,
  rows,
  open,
  onToggle,
  now,
}: {
  groups: readonly ProviderGroup[];
  rows: readonly UnifiedTimelineRow[];
  open: boolean;
  onToggle: () => void;
  now: number;
}) {
  if (!rows.length) return null;
  const next = nextTimelineReset(rows);
  return (
    <div data-usage-timeline-section className="usage-popover-span border-t border-line pt-2">
      <button
        type="button"
        data-usage-timeline-toggle
        aria-expanded={open}
        aria-label={open ? tr("usage.timeline_toggle_hide") : tr("usage.timeline_toggle_show")}
        onClick={onToggle}
        className="flex w-full items-center gap-2 rounded-control px-1 py-1 text-left text-submeta transition-colors hover:bg-hover/60"
      >
        <span className="inline-flex shrink-0 items-center gap-1.5 font-medium text-ink-2">
          <Icon name="clock" size={12} className="shrink-0 text-ink-3" />
          <span className="whitespace-nowrap">{tr("usage.timeline_title")}</span>
        </span>
        {!open && next ? (
          <span
            data-usage-timeline-summary
            className="flex min-w-0 flex-1 items-baseline justify-end gap-1 text-ink-3"
            title={tr("usage.timeline_next", { name: next.accountName, window: next.label, remaining: remainingText(next.remainingMs) })}
          >
            <span className="shrink-0 whitespace-nowrap">{tr("usage.timeline_next_prefix")}</span>
            <span data-usage-timeline-next-name className="min-w-0 truncate">{next.accountName}</span>
            <span data-usage-timeline-next-time className="shrink-0 whitespace-nowrap tabular-nums text-ink-2">
              {`· ${next.label} · ${compactResetText(next.resetsAt, now)}`}
            </span>
          </span>
        ) : (
          <span className="flex-1" />
        )}
        <span aria-hidden className={cx("inline-flex shrink-0 items-center text-ink-3 transition-transform", open ? "rotate-180" : "-rotate-90")}>
          <Icon name="chevron" size={10} />
        </span>
      </button>
      <Collapse open={open} lazy>
        <UnifiedTimeline groups={groups} rows={rows} now={now} embedded />
      </Collapse>
    </div>
  );
}

/** Single aggregated reset timeline across all provider cards. Hidden completely when no account has any resettable window. */
export function UnifiedTimeline({
  groups,
  rows: precomputed,
  now,
  embedded = false,
}: {
  groups: readonly ProviderGroup[];
  rows?: readonly UnifiedTimelineRow[];
  now: number;
  embedded?: boolean;
}) {
  const rows = precomputed ?? unifiedTimeline(groups, now);
  const [axisRef, axisWidth] = useAxisWidth();
  if (!rows.length) return null;
  const ticks = visibleTimelineTickHours(axisWidth);
  const shortLabels = uniqueLaneLabels(rows, false);
  const longLabels = uniqueLaneLabels(rows, true);
  return (
    <div
      data-usage-timeline
      className={cx(!embedded && "usage-popover-span border-t border-line pt-2.5", embedded && "pt-1.5")}
      role="group"
      aria-label={tr("usage.timeline_title")}
    >
      {!embedded && <div className="mb-1.5 text-submeta text-ink-3">{tr("usage.timeline_title")}</div>}
      <div className="usage-timeline-grid items-center">
        <span aria-hidden />
        <div ref={axisRef} data-timeline-axis className="relative h-4 whitespace-nowrap text-submeta leading-none text-ink-3" aria-hidden>
          {ticks.map((h) => {
            const isNow = h === 0;
            const isEnd = h === CAP_HOURS;
            return (
              <span
                key={h}
                data-timeline-tick={h}
                className={cx("absolute top-0 whitespace-nowrap", isNow ? "left-0 font-semibold text-ink" : isEnd ? "right-0" : "-translate-x-1/2")}
                style={isNow || isEnd ? undefined : { left: `${timelinePosition(h * HOUR_MS) * 100}%` }}
              >
                {tickLabel(h)}
              </span>
            );
          })}
        </div>
      </div>
      <div className="relative">
        <div aria-hidden className="usage-timeline-grid pointer-events-none absolute inset-0">
          <span />
          <div className="relative">
            {TIMELINE_TICKS_HOURS.map((h) => (
              <span key={h} className={cx("absolute inset-y-0 z-[1] w-px bg-line-strong", h !== DAY_HOURS && "opacity-60")} style={{ left: `${timelinePosition(h * HOUR_MS) * 100}%` }} />
            ))}
            <span data-timeline-now className="absolute inset-y-0 z-[3] w-0.5 rounded-full bg-ink" />
          </div>
        </div>
        {rows.map((r, i) => {
          const providerBreak = i > 0 && rows[i - 1].provider !== r.provider;
          const parts = r.lanes.map((l) => formatLaneSummary(l, now));
          const plan = planNote(r.account);
          const name = r.account.alias ?? r.account.name;
          const shaded = i % 2 === 0;
          return (
            <div
              key={r.account.id}
              data-timeline-row={r.account.id}
              data-timeline-provider={r.provider}
              data-timeline-paused={r.paused ? "true" : undefined}
              role="img"
              aria-label={`${name}: ${parts.join("; ")}`}
              title={parts.join("\n")}
              className={cx("usage-timeline-grid relative min-h-7 items-center py-0.5", shaded && "rounded-md bg-inset")}
            >
              {providerBreak && <span aria-hidden data-timeline-provider-divider className="absolute inset-x-0 top-0 z-[2] border-t border-line" />}
              <span className="z-[1] flex min-w-0 flex-col justify-center pr-2 pl-1.5 text-submeta leading-tight text-ink-2">
                <span className="flex min-w-0 items-center gap-1.5" title={name}>
                  <span aria-hidden data-timeline-provider-dot={r.provider} className="size-1.5 shrink-0 rounded-full" style={{ background: providerHue(r.provider) }} />
                  <span data-timeline-account-name className="min-w-0 truncate">{r.displayName}</span>
                </span>
                {plan && <small className="truncate pl-3 text-ink-3">{plan}</small>}
              </span>
              <div className="relative z-[2] flex min-w-0 flex-col justify-center gap-0.5">
                {r.lanes.map((l) => {
                  const color = l.long ? LANE_COLOR.long : LANE_COLOR.short;
                  const end = `${l.end * 100}%`;
                  return (
                    <span key={l.key} data-timeline-lane={l.label} aria-hidden className="relative block">
                      <span className="mx-1 grid" style={{ gridTemplateColumns: `${l.end}fr auto ${1 - l.end}fr` }}>
                        <span data-timeline-remaining className={cx("col-start-2 min-w-0 text-submeta font-semibold leading-none text-ink", shaded ? "bg-inset" : "bg-surface")}>
                          {remainingText(l.remainingMs)}
                        </span>
                      </span>
                      <span className={cx("relative block h-2.5", r.account.stale && "opacity-60")}>
                        <span className={cx("absolute left-0 top-1/2 -translate-y-1/2 rounded", l.long ? "h-2" : "h-1.5")} style={{ width: end, backgroundImage: `linear-gradient(90deg, transparent, ${color})` }} />
                        <span
                          data-timeline-dot={l.paused ? "paused" : "active"}
                          className="absolute top-0 size-2.5 -translate-x-1/2 rounded-full border-2 bg-surface"
                          style={l.paused ? { left: end, borderColor: "var(--orange)", background: "var(--orange)" } : { left: end, borderColor: color }}
                        />
                      </span>
                    </span>
                  );
                })}
              </div>
            </div>
          );
        })}
      </div>
      <div data-timeline-legend aria-hidden className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-submeta text-ink-3">
        {shortLabels.map((windowLabel) => (
          <span key={`short:${windowLabel}`} data-legend-item="short" className={cx("inline-flex max-w-full items-center gap-1.5", windowLabel.length <= 12 && "whitespace-nowrap")}>
            <span className="inline-block h-1 w-3 shrink-0 rounded-full" style={{ background: LANE_COLOR.short }} />
            <span>{tr("usage.legend_window", { window: windowLabel })}</span>
          </span>
        ))}
        {longLabels.map((windowLabel) => (
          <span key={`long:${windowLabel}`} data-legend-item="long" className={cx("inline-flex max-w-full items-center gap-1.5", windowLabel.length <= 12 && "whitespace-nowrap")}>
            <span className="inline-block h-1 w-3 shrink-0 rounded-full" style={{ background: LANE_COLOR.long }} />
            <span>{tr("usage.legend_window", { window: windowLabel })}</span>
          </span>
        ))}
        <span data-legend-item="now" className="inline-flex items-center gap-1.5 whitespace-nowrap">
          <span className="inline-block h-1 w-3 shrink-0 rounded-full bg-ink" />
          <span>{tr("usage.timeline_now")}</span>
        </span>
        <span data-legend-item="protect" className="inline-flex items-center gap-1.5 whitespace-nowrap" title={tr("usage.legend_protect_title")}>
          <span className="inline-block h-1 w-3 shrink-0 rounded-full bg-orange" />
          <span>{tr("usage.legend_protect")}</span>
        </span>
      </div>
    </div>
  );
}

function WindowRow({
  name,
  w,
  inlineModel,
  groupContext,
  stale,
  now,
  pinId,
  pinned,
  align,
}: {
  name: string;
  w: UsageWindow;
  inlineModel?: string;
  groupContext?: string;
  stale: boolean;
  now: number;
  pinId: string;
  pinned: boolean;
  align: CardWindowAlign;
}) {
  const [expanded, setExpanded] = useState(false);
  const p = accountPace({ stale }, w, now);
  const remaining = remainingPercent(w.used_percent);
  const reserve = protectReserve(w.protect_percent);
  const hasProtect = reserve !== null;
  const tone = remainingTone(remaining, reserve ?? 0);
  const paused = isWindowPaused(w);
  const expectedRemaining = p ? remainingPercent(p.expected) : undefined;
  const paceVerdict = p?.state === "over" ? tr("usage.pace_over") : "";
  const fullReset = resetLabel(w.resets_at, now);
  const shortReset = compactResetText(w.resets_at, now);
  const pausedLabel = paused ? tr("usage.paused") : "";
  const fullResetWithPaused = paused ? (fullReset ? `${pausedLabel} · ${fullReset}` : pausedLabel) : fullReset;
  const hasDetail = Boolean(paceVerdict || fullResetWithPaused);
  const display = windowRowDisplay(w, inlineModel, groupContext);
  const scopeLabel = w.model?.trim() || groupContext || "";
  const distinctTitle = w.title?.trim()
    && w.title.trim().toLowerCase() !== w.label.trim().toLowerCase()
    && (!scopeLabel || w.title.trim().toLowerCase() !== scopeLabel.toLowerCase())
    ? w.title.trim()
    : "";
  const windowLabelForAria = distinctTitle ? `${distinctTitle} (${w.label})` : w.label;
  const meterBaseLabel = scopeLabel
    ? tr("usage.window_model_remaining_label", { name, window: windowLabelForAria, model: scopeLabel, percent: shown(remaining) })
    : tr("usage.window_remaining_label", { name, window: windowLabelForAria, percent: shown(remaining) });
  const meterLabel = paceVerdict ? `${meterBaseLabel}, ${paceVerdict}` : meterBaseLabel;
  const rowTitle = [display.span, display.caption, shown(remaining), paceVerdict, fullResetWithPaused, display.note].filter(Boolean).join(" · ");
  const pinLabel = windowPinLabel(name, w, pinned);
  const gridCols = align.hasReset
    ? "grid-cols-[1.75rem_minmax(2.5rem,1fr)_2.25rem_3.75rem_0.75rem] @[300px]:grid-cols-[1.75rem_minmax(2.5rem,1fr)_2.25rem_6.75rem_0.75rem]"
    : align.hasDetail
      ? "grid-cols-[1.75rem_minmax(2.5rem,1fr)_2.25rem_0.75rem]"
      : "grid-cols-[1.75rem_minmax(2.5rem,1fr)_2.25rem]";
  const pinOverlay = !pinned && !align.hasPinnedWindow;
  return (
    <div className="flex flex-col">
      <div className={cx("usage-win-row relative flex items-end", align.hasPinnedWindow && "gap-1")}>
        <button
          type="button"
          data-window-row={w.label}
          data-window-paused={paused ? "true" : undefined}
          data-window-inline-model={display.isModelCaption ? "true" : undefined}
          aria-expanded={hasDetail ? expanded : undefined}
          aria-label={rowTitle}
          title={rowTitle}
          disabled={!hasDetail}
          onClick={() => hasDetail && setExpanded((v) => !v)}
          className={cx(
            "grid min-w-0 flex-1 items-center gap-x-1.5 rounded-sm text-left text-submeta [@media(pointer:coarse)]:min-h-11",
            gridCols,
            hasProtect && !display.caption && "mt-3.5",
            hasDetail && "hover:bg-hover/60",
          )}
        >
          {display.caption && (
            <span
              data-usage-model={display.isModelCaption ? (inlineModel ?? w.model) : undefined}
              data-usage-window-title={display.hasTitle ? display.caption : undefined}
              className={cx(
                "col-start-2 col-end-[-1] min-w-0 truncate text-submeta leading-tight text-ink-3",
                hasProtect ? "pb-4" : "pb-0.5",
              )}
              title={display.caption}
            >
              {display.caption}
            </span>
          )}
          <span data-window-span className="col-start-1 w-7 truncate text-ink-2">{display.span}</span>
          <span className={cx("col-start-2 block min-w-0", stale && "opacity-70")}>
            <Meter
              remaining={remaining}
              expectedRemaining={expectedRemaining}
              reserve={reserve ?? undefined}
              barClass={TONE_BG[tone]}
              reserveTop={false}
              label={meterLabel}
              tall
            />
          </span>
          <span data-usage-percent className={cx("col-start-3 w-9 whitespace-nowrap text-right font-medium tabular-nums", stale ? "text-ink-2" : TONE_TEXT[tone])}>
            {shown(remaining)}
          </span>
          {align.hasReset && (
            fullResetWithPaused ? (
              <span data-usage-reset data-usage-paused={paused ? "true" : undefined} className="col-start-4 min-w-0 truncate text-right text-ink-3" title={fullResetWithPaused}>
                {paused && <span className="font-medium text-red">{pausedLabel}</span>}
                {paused && fullReset && <span aria-hidden> · </span>}
                {fullReset && (
                  <>
                    <span data-reset-full className="hidden @[300px]:inline">{fullReset}</span>
                    <span data-reset-short className="@[300px]:hidden">{shortReset}</span>
                  </>
                )}
              </span>
            ) : (
              <span aria-hidden data-window-reset-placeholder className="col-start-4" />
            )
          )}
          {(align.hasReset || align.hasDetail) && (
            hasDetail ? (
              <span aria-hidden data-window-chevron className={cx("inline-flex w-3 shrink-0 items-center justify-end text-ink-3 transition-transform", align.hasReset ? "col-start-5" : "col-start-4", expanded ? "rotate-180" : "-rotate-90")}>
                <Icon name="chevron" size={10} />
              </span>
            ) : (
              <span aria-hidden data-window-chevron-placeholder className={cx("w-3", align.hasReset ? "col-start-5" : "col-start-4")} />
            )
          )}
        </button>
        <PinToggle
          pinned={pinned}
          windowAttr={pinId}
          pinOverlay={pinOverlay}
          label={pinLabel}
          onClick={() => togglePinnedUsageAccount(pinId)}
          iconSize={11}
          className="usage-win-pin"
        />
      </div>
      {display.note && (
        <div data-usage-window-note className="pl-[2.125rem] pt-0.5 text-submeta leading-snug text-ink-3">
          {display.note}
        </div>
      )}
      {hasDetail && (
        <Collapse open={expanded} lazy>
          <div data-usage-pace-detail className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-0.5 pl-[2.125rem] pt-1 text-submeta">
            {paceVerdict && <span className="whitespace-nowrap text-orange">{paceVerdict}</span>}
            {fullResetWithPaused && <span className={cx(paused && !fullReset ? "font-medium text-red" : "text-ink-3")}>{fullResetWithPaused}</span>}
          </div>
        </Collapse>
      )}
    </div>
  );
}
