// "Assistant | Agents" in the middle of a phone's top bar (#1996), like the iOS app's new shell (docs/apple/ios.md "New shell"): the
// main conversation and the Agents page, side by side. Agents carries how many agents are waiting for you (quiet ones
// are told about in the main conversation, not counted here), the current assistant's with several. Narrow screens only;
// wide ones have the sidebar.
//
// The switch stays within its header slots; History is a destination under Agents (#10240).
import type { MouseEvent, ReactNode } from "react";
import { href, onLinkClick, type Route } from "../lib/router";
import { useScopedCounts } from "../lib/current-assistant";
import { heldOnlyNeeds } from "../lib/attention";
import { othersWaiting } from "../lib/sessions";
import { supervisorHeldOnly } from "../lib/supervisor-held";
import { useStore } from "../lib/store";
import { usePhoneNavMode } from "../lib/phone-nav";
import type { Interaction, Session } from "../lib/types";
import { cx, IconButton } from "../ui/atoms";
import { HEADER_SLOT, HEADER_SLOT_END, HEADER_SLOT_START } from "./chat/header";
import { t } from "../i18n";

export type ShellPage = "assistant" | "agents";

export interface ShellCounts {
  badge: number;
  quietCounted?: boolean;
}

export interface ShellAttentionState {
  interactions: readonly Interaction[];
  pushGrading?: boolean;
}

export interface ShellSwitchNavDeps {
  /** Compute the `href` for a shell destination route (defaults to `router.href`). */
  routeHref?: (to: Route) => string;
  /** Handle clicking an inactive shell link (defaults to `router.onLinkClick`). */
  onRouteClick?: (e: MouseEvent<HTMLAnchorElement>, to: Route, page: ShellPage) => void;
}

export interface ShellSwitchDeps extends ShellSwitchNavDeps {
  /** Direct waiting badge override (bypasses `useScopedCounts` when supplied). */
  waiting?: number;
  /** Controlled scoped counts snapshot or reader (bypasses `useScopedCounts` when supplied). */
  counts?: ShellCounts | (() => ShellCounts);
  /** Controlled replacement for `useScopedCounts`. */
  useScopedCounts?: () => ShellCounts;
}

export interface ChatSidebarButtonDeps {
  /** Direct waiting count override (bypasses `useScopedCounts` and `useStore` when supplied). */
  waiting?: number;
  /** Controlled scoped counts snapshot or reader (bypasses `useScopedCounts` when supplied). */
  counts?: ShellCounts | (() => ShellCounts);
  /** Controlled replacement for `useScopedCounts`. */
  useScopedCounts?: () => ShellCounts;
  /** Controlled held-session override or predicate (bypasses `useStore` when supplied). */
  isHeld?: boolean | ((session: Session) => boolean);
  /** Controlled interactions & pushGrading snapshot/reader to compute `held` without `useStore`. */
  attention?: ShellAttentionState | (() => ShellAttentionState);
}

const SHELL_PAGES: { page: ShellPage; label: string; short?: string; to: Route }[] = [
  { page: "assistant", label: t("shellswitch.assistant"), to: { name: "persona" } },
  { page: "agents", label: t("agentspage.title"), to: { name: "agent-list" } },
];

function isSessionHeld(session: Session | undefined, interactions: readonly Interaction[], pushGrading: boolean): boolean {
  return !!session && ((pushGrading && heldOnlyNeeds(session, interactions)) || supervisorHeldOnly(interactions).has(`${session.machine_id}/${session.session_id}`));
}

export function ShellSwitch({ page, deps }: { page: ShellPage; deps?: ShellSwitchDeps }) {
  // With the phone's tab bar (lib/phone-nav.ts) the bottom carries the places, so the top bar names the page instead.
  const phoneNav = usePhoneNavMode();
  if (phoneNav === "tabs" && !deps) return <ShellPageTitle page={page} />;
  if (deps?.waiting !== undefined) return <ShellSwitchView page={page} waiting={deps.waiting} deps={deps} />;
  if (deps?.counts !== undefined) {
    const counts = typeof deps.counts === "function" ? deps.counts() : deps.counts;
    return <ShellSwitchView page={page} waiting={counts.badge} deps={deps} />;
  }
  if (deps?.useScopedCounts) return <CustomScopedShellSwitch page={page} deps={deps} readCounts={deps.useScopedCounts} />;
  return <DefaultShellSwitch page={page} deps={deps} />;
}

/** The page's name where the switch would be, under the phone's tab bar. */
export function ShellPageTitle({ page }: { page: ShellPage }) {
  const label = SHELL_PAGES.find((p) => p.page === page)?.label ?? "";
  return <h1 data-shell-title className="min-w-0 truncate text-card-title font-semibold text-ink">{label}</h1>;
}

function DefaultShellSwitch({ page, deps }: { page: ShellPage; deps?: ShellSwitchDeps }) {
  return <ShellSwitchView page={page} waiting={useScopedCounts().badge} deps={deps} />;
}

function CustomScopedShellSwitch({ page, deps, readCounts }: { page: ShellPage; deps: ShellSwitchDeps; readCounts: () => ShellCounts }) {
  return <ShellSwitchView page={page} waiting={readCounts().badge} deps={deps} />;
}

export function WaitingBadge({ count, className }: { count: number; className?: string }) {
  return <span aria-hidden className={cx("flex h-4 min-w-4 items-center justify-center rounded-full bg-field px-1 text-[10px] font-semibold text-attention tabular-nums", className)}>{count}</span>;
}

/** Pure presentational view for the narrow-screen chat sidebar toggle button with optional waiting badge. */
export function ChatSidebarButtonView({ waiting, onClick }: { waiting: number; onClick: () => void }) {
  const shown = waiting > 0 ? waiting : 0;
  const label = t("chat-chatview.sidebar");
  return (
    <span className="relative flex shrink-0 lg:hidden">
      <IconButton icon="sidebar" label={shown ? t("shellswitch.waiting_label", { label, count: shown }) : label} onClick={onClick} />
      {shown > 0 && <WaitingBadge count={shown} className="pointer-events-none absolute -top-1 -right-1" />}
    </span>
  );
}

/** A chat header's way to the sidebar on a narrow screen, carrying how many other agents are waiting for you. `counted` is
 *  off where the header's switch already shows that number. */
export function ChatSidebarButton({ session, counted, onClick, deps }: { session?: Session; counted: boolean; onClick: () => void; deps?: ChatSidebarButtonDeps }) {
  if (deps?.waiting !== undefined) return <ChatSidebarButtonView waiting={counted ? deps.waiting : 0} onClick={onClick} />;
  if (deps?.counts !== undefined) {
    const counts = typeof deps.counts === "function" ? deps.counts() : deps.counts;
    return <ChatSidebarButtonHeld session={session} counted={counted} onClick={onClick} counts={counts} deps={deps} />;
  }
  if (deps?.useScopedCounts) return <CustomCountsSidebarButton session={session} counted={counted} onClick={onClick} deps={deps} readCounts={deps.useScopedCounts} />;
  return <DefaultCountsSidebarButton session={session} counted={counted} onClick={onClick} deps={deps} />;
}

function DefaultCountsSidebarButton({ session, counted, onClick, deps }: { session?: Session; counted: boolean; onClick: () => void; deps?: ChatSidebarButtonDeps }) {
  return <ChatSidebarButtonHeld session={session} counted={counted} onClick={onClick} counts={useScopedCounts()} deps={deps} />;
}

function CustomCountsSidebarButton({ session, counted, onClick, deps, readCounts }: { session?: Session; counted: boolean; onClick: () => void; deps: ChatSidebarButtonDeps; readCounts: () => ShellCounts }) {
  return <ChatSidebarButtonHeld session={session} counted={counted} onClick={onClick} counts={readCounts()} deps={deps} />;
}

function ChatSidebarButtonHeld({ session, counted, onClick, counts, deps }: { session?: Session; counted: boolean; onClick: () => void; counts: ShellCounts; deps?: ChatSidebarButtonDeps }) {
  if (deps?.isHeld !== undefined) {
    const held = !!session && (typeof deps.isHeld === "function" ? deps.isHeld(session) : deps.isHeld);
    return <ChatSidebarButtonView waiting={counted ? othersWaiting(counts.badge, session, counts.quietCounted ?? false, held) : 0} onClick={onClick} />;
  }
  if (deps?.attention !== undefined) {
    const att = typeof deps.attention === "function" ? deps.attention() : deps.attention;
    const held = isSessionHeld(session, att.interactions, Boolean(att.pushGrading));
    return <ChatSidebarButtonView waiting={counted ? othersWaiting(counts.badge, session, counts.quietCounted ?? false, held) : 0} onClick={onClick} />;
  }
  if ((!session || !counted) && deps !== undefined) {
    return <ChatSidebarButtonView waiting={counted ? othersWaiting(counts.badge, undefined, counts.quietCounted ?? false, false) : 0} onClick={onClick} />;
  }
  return <DefaultHeldSidebarButton session={session} counted={counted} onClick={onClick} counts={counts} />;
}

function DefaultHeldSidebarButton({ session, counted, onClick, counts }: { session?: Session; counted: boolean; onClick: () => void; counts: ShellCounts }) {
  // left out of the badge already: graded to wait, or waiting only on cards its task's supervisor holds
  const held = useStore((s) => isSessionHeld(session, s.interactions, Boolean(s.pushGrading)));
  const waiting = counted ? othersWaiting(counts.badge, session, counts.quietCounted ?? false, held) : 0;
  return <ChatSidebarButtonView waiting={waiting} onClick={onClick} />;
}

/** Pure presentational view for the phone top bar header slots around the switch. */
export function ShellHeaderSlotsView({ page, waiting, onToggleSidebar, end, endWide, deps }: {
  page: ShellPage; waiting: number; onToggleSidebar: () => void; end: ReactNode; endWide?: boolean; deps?: ShellSwitchNavDeps;
}) {
  return (
    <>
      <div className={cx(HEADER_SLOT, HEADER_SLOT_START, "lg:hidden")}>
        <IconButton icon="sidebar" label={t("shellswitch.sidebar")} onClick={onToggleSidebar} />
      </div>
      <div className="flex min-w-0 flex-1 justify-center lg:hidden">
        <ShellSwitchView page={page} waiting={waiting} deps={deps} />
      </div>
      <div className={cx(HEADER_SLOT, HEADER_SLOT_END, !endWide && "lg:hidden")}>{end}</div>
    </>
  );
}

/** A phone's top bar around the switch, in ChatView's header slots (chat/header.ts): sidebar on the left, `end` on the
 *  right, so both pages have their buttons in the same places. Narrow only, unless `endWide` keeps `end` on a wide screen. */
export function ShellHeaderSlots({ page, onToggleSidebar, end, endWide, deps }: { page: ShellPage; onToggleSidebar: () => void; end: ReactNode; endWide?: boolean; deps?: ShellSwitchDeps }) {
  return (
    <>
      <div className={cx(HEADER_SLOT, HEADER_SLOT_START, "lg:hidden")}>
        <IconButton icon="sidebar" label={t("shellswitch.sidebar")} onClick={onToggleSidebar} />
      </div>
      <div className="flex min-w-0 flex-1 justify-center lg:hidden">
        <ShellSwitch page={page} deps={deps} />
      </div>
      <div className={cx(HEADER_SLOT, HEADER_SLOT_END, !endWide && "lg:hidden")}>{end}</div>
    </>
  );
}

/** The switch itself, with the number of agents waiting for you on its Agents side (0: no badge). */
export function ShellSwitchView({ page, waiting, deps }: { page: ShellPage; waiting: number; deps?: ShellSwitchNavDeps }) {
  const resolveHref = deps?.routeHref ?? href;
  const handleClick = deps?.onRouteClick;
  return (
    <nav aria-label={t("shellswitch.switch_page")} className="flex h-8 max-w-full min-w-0 items-center gap-0.5 rounded-full bg-inset p-0.5 shadow-hairline">
      {SHELL_PAGES.map((p) => {
        const on = p.page === page;
        const badge = p.page === "agents" && waiting > 0 ? waiting : 0;
        return (
          <a
            key={p.page}
            href={resolveHref(p.to)}
            onClick={(e) => (on ? e.preventDefault() : handleClick ? handleClick(e, p.to, p.page) : onLinkClick(e, p.to))}
            aria-current={on ? "page" : undefined}
            aria-label={badge ? t("shellswitch.waiting_label", { label: p.label, count: badge }) : p.label}
            title={p.short ? p.label : undefined}
            className={cx(
              // a phone: tighter, and may give way to the bar (its label truncating); wider: as wide as its label
              "touch-hit flex h-7 min-w-0 items-center gap-1.5 whitespace-nowrap rounded-full px-2 text-[13px] transition-[background-color,color,box-shadow] duration-150 sm:shrink-0 sm:px-3.5",
              on ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-2 hover:text-ink",
            )}
          >
            <span className="min-w-0 truncate" data-switch-label>
              {p.short ? (
                <>
                  <span className="sm:hidden">{p.short}</span>
                  <span className="max-sm:hidden">{p.label}</span>
                </>
              ) : (
                p.label
              )}
            </span>
            {/* a phone: the count sits on the segment's corner, as on the sidebar button, taking no room from the label */}
            {badge > 0 && <WaitingBadge count={badge} className="shrink-0 max-sm:pointer-events-none max-sm:absolute max-sm:-top-1 max-sm:-right-1" />}
          </a>
        );
      })}
    </nav>
  );
}
