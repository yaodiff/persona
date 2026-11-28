// The sidebar's bottom (#2432, docs/server/usage-windows.md): the two accounts with lowest remaining headroom as thin bars;
// a click opens a popover with every window of every account, its reset countdown, protection reserve, unified reset timeline,
// and adaptive multi-column layout. Hidden entirely while the hub has no data.
import { useSidebarUsageShown } from "../lib/sidebar-usage";
import { type ComponentType, useEffect, useRef, useState } from "react";
import { t as tr } from "../i18n";
import { isBalanceAccount, isHistoricalCodexReading, liveAccounts, protectReserve, remainingPercent, remainingTone, selectSidebarUsageItems, sidebarWindowTitle, sourceNames, tightestRemaining, usePinnedUsageAccounts, useUsageWindows, type RemainingTone, type UnconnectedProvider, type UsageAccount } from "../lib/usage-windows";
import { cx } from "../ui/atoms";
import { lazyDialog } from "./LazyOverlay";

const UsagePopover = lazyDialog<{ open: boolean; accounts: UsageAccount[]; unconnected?: readonly UnconnectedProvider[]; anchor?: HTMLElement | null; onClose: () => void }>(() => import("./UsagePopover").then((m) => m.UsagePopover));
let LoadedUnconnected: ComponentType<{ accounts: UsageAccount[]; onChange: (next: readonly UnconnectedProvider[]) => void }> | undefined;

const percent = (p: number) => Math.min(100, Math.max(0, p));
const shown = (p: number) => `${Math.round(p)}%`;
export const TONE_BG: Record<RemainingTone, string> = { green: "bg-green", orange: "bg-orange", red: "bg-red" };
export const TONE_TEXT: Record<RemainingTone, string> = { green: "text-green", orange: "text-orange", red: "text-red" };
// The strip's row text: one place for the named secondary-meta size.
const ROW_NAME = "min-w-0 flex-1 truncate text-submeta text-ink-3";
const ROW_VALUE = "shrink-0 text-submeta tabular-nums";

/** Compact remaining-quota progress bar for the collapsed sidebar strip. Popover-only overlays (protection zone/line/flag
 *  and expected-pace tick) live inside the lazy UsagePopover chunk so they add zero bytes to FIRST_SCREEN_BUDGET. */
function StripMeter({ remaining, barClass, label }: { remaining: number; barClass: string; label: string }) {
  const r = percent(remaining);
  return (
    <div role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(r)} className="relative h-1 w-full rounded-full bg-inset">
      <div className={cx("h-full rounded-full transition-[width] duration-300", barClass)} style={{ width: `${r}%` }} />
    </div>
  );
}

const EMPTY_UNCONNECTED: readonly UnconnectedProvider[] = [];

/** The sidebar's strip. `accounts` as the hub gave them; `unconnected` lists active providers without usage data yet. */
export function UsageBars({ accounts, unconnected = EMPTY_UNCONNECTED, now = Date.now() }: { accounts: UsageAccount[]; unconnected?: readonly UnconnectedProvider[]; now?: number }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const pinnedIds = usePinnedUsageAccounts();
  const live = liveAccounts(accounts, now);
  const { items } = selectSidebarUsageItems(live, pinnedIds);
  const notices = !items.length ? live.filter((a) => !isBalanceAccount(a) && a.windows.length === 0 && Boolean(a.notice?.trim() || a.headline?.trim())).slice(0, 2) : [];

  useEffect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => {
      const target = e.target;
      if ((target instanceof Node && ref.current?.contains(target)) || (target instanceof Element && target.closest("[data-usage-popover]"))) return;
      setOpen(false);
    };
    const key = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    addEventListener("pointerdown", down, true);
    addEventListener("keydown", key);
    return () => {
      removeEventListener("pointerdown", down, true);
      removeEventListener("keydown", key);
    };
  }, [open]);

  const showHistorical = !items.length && live.some(isHistoricalCodexReading);
  if (!items.length && !notices.length && !unconnected.length && !showHistorical) return null;
  const barCount = items.reduce((n, item) => n + (item.kind === "balance" ? 0 : 1), 0);
  return (
    <div ref={ref} className="relative shrink-0 px-2.5 pb-2.5 pt-1" data-usage-bars>
      <UsagePopover open={open} accounts={live} unconnected={unconnected} anchor={triggerRef.current} onClose={() => setOpen(false)} />
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="dialog"
        aria-expanded={open}
        title={tr("usage.title")}
        className="touch-hit flex w-full flex-col gap-1.5 rounded-control px-2.5 py-2 text-left shadow-hairline transition-colors hover:bg-hover"
      >
        {items.map((item) => {
          if (item.kind === "balance") return <BalanceRow key={item.key} a={item.account} />;
          const a = item.account;
          const w = item.kind === "window" ? item.window : undefined;
          const { remaining, reserve } = w
            ? { remaining: remainingPercent(w.used_percent), reserve: protectReserve(w.protect_percent) ?? 0 }
            : tightestRemaining(a);
          const tone = remainingTone(remaining, reserve);
          const name = w ? sidebarWindowTitle(a, w) : a.name;
          const fullName = w ? `${name} ${w.label}` : a.name;
          const from = sourceNames(a);
          const title = [w ? fullName : "", from.length ? tr("usage.sources_title", { sources: from.join(" · ") }) : "", a.stale ? tr("usage.stale_title") : ""].filter(Boolean).join(" · ");
          return (
            <span key={item.key} data-usage-window={w ? item.key : undefined} className={cx("flex flex-col gap-1", a.stale && "opacity-60")} title={title || undefined}>
              <span className="flex items-baseline gap-2">
                <span className={ROW_NAME}>{name}</span>
                {w && <span className="shrink-0 whitespace-nowrap text-submeta text-ink-3">{w.label}</span>}
                <span className={cx(ROW_VALUE, a.stale ? "text-ink-2" : TONE_TEXT[tone])}>{tr("usage.left_short", { percent: shown(remaining) })}</span>
              </span>
              <StripMeter remaining={remaining} barClass={TONE_BG[tone]} label={tr("usage.remaining_label", { name: fullName, percent: shown(remaining) })} />
            </span>
          );
        })}
        {showHistorical && <span className="text-left text-xs text-ink-2">{tr("usage.codex_local_reading")}</span>}
        {notices.map((a) => (
          <span key={a.id} data-usage-notice-strip={a.id} className={cx("flex items-baseline gap-2", a.stale && "opacity-60")}>
            <span className={ROW_NAME}>{a.name}</span>
            <span className="min-w-0 max-w-[55%] truncate text-submeta text-ink-3">{a.notice?.trim() || a.headline?.trim()}</span>
          </span>
        ))}
        {unconnected.slice(0, Math.max(0, 4 - barCount)).map((p) => (
          <span key={`_${p.id}`} data-usage-unconnected={p.id} className="flex items-baseline gap-2 text-xs text-ink-3">
            <span className="min-w-0 flex-1 truncate">{p.title}</span>
            <span className="shrink-0">{tr("usage.not_connected")}</span>
          </span>
        ))}
      </button>
    </div>
  );
}

/** A prepaid account's row: the amount and currency, no bar. A genuine zero and an unreadable balance are both
 *  caution-coloured, but only the first shows an amount. */
function BalanceRow({ a }: { a: UsageAccount }) {
  // Why a balance is missing or old is the popover's detail (progressive disclosure); the strip only fades a stale one.
  return (
    <span className={cx("flex items-baseline gap-2", a.stale && "opacity-60")}>
      <span className={ROW_NAME}>{a.name}</span>
      <span className={cx(ROW_VALUE, !a.balance || Number(a.balance.amount) === 0 ? "text-orange" : "text-ink-2")}>
        {a.balance ? `${a.balance.currency} ${a.balance.amount}`.trim() : "—"}
      </span>
    </span>
  );
}

/** The sidebar mounts. */
export function SidebarUsage() {
  // Off in Settings (lib/sidebar-usage.ts): nothing mounts, so the usage windows are not even read.
  return useSidebarUsageShown() ? <SidebarUsageShown /> : null;
}

function SidebarUsageShown() {
  return <SidebarUsageInner accounts={useUsageWindows()} />;
}

function SidebarUsageInner({ accounts }: { accounts: UsageAccount[] }) {
  const [, setLoaded] = useState(() => Boolean(LoadedUnconnected));
  const [unconnected, setUnconnected] = useState(EMPTY_UNCONNECTED);
  useEffect(() => {
    if (LoadedUnconnected) return;
    let active = true;
    void import("./UsageUnconnected").then((m) => {
      LoadedUnconnected = m.UsageUnconnected;
      if (active) setLoaded(true);
    });
    return () => { active = false; };
  }, []);
  return (
    <>
      {LoadedUnconnected && <LoadedUnconnected accounts={accounts} onChange={setUnconnected} />}
      <UsageBars accounts={accounts} unconnected={unconnected} />
    </>
  );
}
