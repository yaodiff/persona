// ── the rail: the sidebar folded to its icons on a wide screen (#2524) ──
// Imports only SidebarParts among the sidebar's files.
import { useLayoutEffect, useRef, useState } from "react";
import { t } from "../i18n";
import { activityOf } from "../lib/format";
import { href, type Route } from "../lib/router";
import { pickMainChat } from "../lib/handoff";
import { useStore } from "../lib/store";
import { BREATHING, connStatus, phaseTone } from "../lib/connection";
import { cx, Menu } from "../ui/atoms";
import { StatusMark } from "../ui/StatusMark";
import { NoticeDot } from "../ui/NoticeDot";
import { AppMark } from "../ui/AppMark";
import { Icon } from "../ui/icons";
import { formatChord, jumpHint, useBindings } from "../lib/shortcuts";
import { takeFocusHandoff, toggleSidebarColumn } from "../lib/layout";
import { CURRENT_ROW, DOT_CLASS, morePagesMenu, pageLabel, type NavPage, pageIconStyle, useActiveKey, useGo, useNavPages } from "./SidebarParts";
import { requestDirectWaiting } from "../lib/direct-waiting";
import { needsYouAutoOpen } from "../lib/needs-you-auto-open";
import { useMainWaiting } from "./assistant/use-main-waiting";
import type { UIColor } from "../lib/extension-ui-v6";

export const SIDEBAR_CONTROL = "touch-hit relative z-10 flex shrink-0 items-center justify-center rounded-control text-ink-3 transition-[background-color,color,transform] duration-150 hover:bg-hover hover:text-ink active:scale-[0.94]";

/** The button that folds the sidebar to the rail and opens it again (⌘⌥B by default; the hint follows a rebinding). */
export function CollapseButton({ collapsed, chord, size = "md", onToggle }: { collapsed: boolean; chord?: string; size?: "sm" | "md"; onToggle?: (keepFocus: boolean) => void }) {
  const label = collapsed ? t("sidebar.expand") : t("sidebar.collapse");
  // pressed with the focus on it, this one takes the focus when it is in the active (non-inert) layer,
  // including mid-flight animation reversals where an already-mounted CollapseButton becomes active again.
  const self = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => {
    const el = self.current;
    if (el && !el.closest("[inert]") && takeFocusHandoff()) el.focus({ preventScroll: true });
  });
  return (
    <button
      ref={self}
      type="button"
      onClick={(e) => (onToggle ?? toggleSidebarColumn)(document.activeElement === e.currentTarget)}
      aria-label={label}
      aria-expanded={!collapsed}
      title={chord ? `${label} (${formatChord(chord)})` : label}
      className={cx(SIDEBAR_CONTROL, size === "sm" ? "size-7" : "size-8")}
    >
      <Icon name="sidebar" size={15} stroke={1.8} />
    </button>
  );
}

/** The folded "Pages" title's right side: a dot when a page in it has a badge, so folding hides nothing to look at. */
export function PagesTally({ pages }: { pages: NavPage[] }) {
  const counted = pages.filter((p) => p.badge !== undefined);
  const tones = [...new Set(counted.map((p) => p.badgeTone ?? "accent"))];
  if (!tones.length) return null;
  const label = counted.map(pageLabel).join(t("sidebar.list_separator"));
  return (
    <span title={label} className="flex items-center gap-1">
      <span className="sr-only">{label}</span>
      {tones.map((tone) => <span key={tone} aria-hidden className={cx("size-1.5 rounded-full", DOT_CLASS[tone])} />)}
    </span>
  );
}

const RAIL_ITEM = "touch-hit relative flex size-9 shrink-0 items-center justify-center rounded-[9px] transition-[background-color,color,transform] duration-150 active:scale-[0.94]";

/**
 * The sidebar folded on a wide screen: one column of icons, the open page filled, a page's count turned into a dot on
 * its icon (the count is in its label and tooltip). Why a rail rather than nothing: the pages stay one click away, and
 * the way back is on screen, so the fold can be kept across reloads (lib/layout.ts). The Agents' list and the
 * conversations are what fold away; ⌘K finds any of them.
 */
export function SidebarRail({ route, onSearch, onNewAgent, hideCollapse = false }: { route: Route; onSearch: () => void; onNewAgent: () => void; hideCollapse?: boolean }) {
  const { phase, online, total, main, pages, more } = useNavPages();
  const hasAssistant = useStore((s) => Boolean(s.assistant));
  const [moreMenu, setMoreMenu] = useState<HTMLElement | null>(null);
  const bindings = useBindings();
  const activeKey = useActiveKey(route);
  const go = useGo();
  const status = connStatus(phase, online, total);
  const st = main ? activityOf(main) : undefined;
  const mainWaiting = useMainWaiting(main);
  const pending = mainWaiting.count;
  // `color`: a page's icon color (NavPage.color, #10822), kept under the pointer as well
  const link = (k: string, to: Route, label: string, icon: React.ReactNode, dot?: React.ReactNode, title?: string, color?: UIColor, after?: () => void) => {
    const active = activeKey === k;
    return (
      <a
        key={k}
        href={href(to)}
        // Main Chat opens the page its conversation kept, as the sidebar's row does (#13164)
        onClick={(e) => { go(to, k === "persona" ? () => pickMainChat(active) : undefined)(e); if (e.defaultPrevented) after?.(); }}
        aria-label={label}
        title={title ?? label}
        aria-current={active ? "page" : undefined}
        className={cx(RAIL_ITEM, active ? cx(CURRENT_ROW, "text-ink") : "text-ink-3 hover:bg-hover hover:text-ink")}
        style={pageIconStyle(color, active)}
      >
        {icon}
        {dot}
      </a>
    );
  };
  return (
    <aside data-sidebar-rail className="flex h-full w-full flex-col items-center bg-canvas py-2.5">
      {!hideCollapse && (
        <div data-rail-top-controls className="flex flex-col items-center">
          <CollapseButton collapsed chord={bindings["left-sidebar"]} />
          <button type="button" onClick={onSearch} aria-label={t("sidebar.search_sessions")} title={`${t("sidebar.search_sessions")} (${jumpHint(bindings)})`} className={cx(RAIL_ITEM, "mt-1 text-ink-3 hover:bg-hover hover:text-ink")}>
            <Icon name="search" size={15} stroke={1.8} />
          </button>
          <span aria-hidden className="my-2 h-px w-6 shrink-0 bg-line" />
        </div>
      )}
      <nav aria-label={t("sidebar.pages")} className="flex min-h-0 w-full flex-1 flex-col items-center gap-0.5 overflow-y-auto overscroll-contain">
        {hasAssistant && link("j:new", { name: "persona-new" }, t("sidebar.new_chat"), <Icon name="compose" size={15} stroke={1.8} />)}
        {link(
          "persona",
          { name: "persona" },
          pending > 0 ? `Persona ${pending}` : "Persona",
          <AppMark size={20} radius={5} className="shadow-hairline" />,
          pending > 0 ? <span aria-hidden className="absolute top-1 right-1 size-2 rounded-full bg-attention ring-2 ring-canvas" />
            : st === "working" || st === "needs_you" || st === "unread" ? <span aria-hidden className="absolute top-0.5 right-0.5 flex rounded-full ring-2 ring-canvas"><StatusMark state={st} /></span> : null,
          bindings.persona ? `${t("sidebar.persona_main")} (${formatChord(bindings.persona)})` : t("sidebar.persona_main"),
          undefined,
          () => { if (main && pending > 0 && needsYouAutoOpen()) requestDirectWaiting(main.session_id); },
        )}
        {pages.map((p) => link(
          p.k,
          p.to,
          pageLabel(p),
          <Icon name={p.icon} size={16} stroke={1.8} />,
          p.notice ? <NoticeDot className="absolute top-1.5 right-1.5 ring-2 ring-canvas" />
            : p.badge !== undefined ? <span aria-hidden className={cx("absolute top-1.5 right-1.5 size-2 rounded-full ring-2 ring-canvas", DOT_CLASS[p.badgeTone ?? "accent"])} /> : null,
          [p.title ?? (p.notice ? pageLabel(p) : p.label), p.badge ?? p.hint].filter((x) => x !== undefined).join(" · "),
          p.color,
        ))}
        {more.length > 0 && (
          <button
            type="button"
            onClick={(e) => setMoreMenu(moreMenu ? null : e.currentTarget)}
            aria-haspopup="menu"
            aria-expanded={!!moreMenu}
            aria-label={t("sidebar.more_pages_count", { count: more.length })}
            title={t("sidebar.more_pages_count", { count: more.length })}
            className={cx(RAIL_ITEM, more.some((p) => p.k === activeKey) ? cx(CURRENT_ROW, "text-ink") : "text-ink-3 hover:bg-hover hover:text-ink")}
          >
            <Icon name="more" size={16} stroke={1.8} />
            {more.some((p) => p.badge !== undefined) && <span aria-hidden className={cx("absolute top-1.5 right-1.5 size-2 rounded-full ring-2 ring-canvas", DOT_CLASS[more.find((p) => p.badge !== undefined)!.badgeTone ?? "accent"])} />}
          </button>
        )}
        {moreMenu && <Menu anchor={moreMenu} onClose={() => setMoreMenu(null)} width={220} header={t("sidebar.more_pages")} items={morePagesMenu(more, activeKey)} />}
        <button type="button" onClick={onNewAgent} aria-label={t("sidebar.new_agent")} title={t("sidebar.new_agent")} className={cx(RAIL_ITEM, "mt-1 text-ink-3 hover:bg-hover hover:text-ink")}>
          <Icon name="plus" size={15} stroke={1.8} />
        </button>
      </nav>
      <span role="img" aria-label={status} title={status} className="relative mt-2 flex shrink-0">
        <AppMark size={26} radius={7} className="shadow-hairline" />
        <span className={cx("absolute -right-0.5 -bottom-0.5 size-2.5 rounded-full ring-2 ring-canvas", phaseTone(phase).color)} style={phaseTone(phase).breathing ? BREATHING : undefined} />
      </span>
    </aside>
  );
}
