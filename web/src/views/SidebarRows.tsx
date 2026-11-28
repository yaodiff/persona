// The sidebar's rows: Persona's main conversation, the pinned heading, a page's row, an agent's row and the personal
// agents. Imports only SidebarParts among the sidebar's files.
import { memo, useCallback, useMemo, useState, useSyncExternalStore, type ReactNode } from "react";
import { formatList, t } from "../i18n";
import { awaitsReply, justFinished, sessionSource, sourceName } from "../lib/agent-groups";
import { addSection, arrangeSessions, assignSection, GROUP_LABEL, isDefaultView, providerLabel, refreshAgentSessions, removeSection, resetView, setView, SHOW_LABEL, SORT_LABEL, toggleProvider, togglePin, totalUnread, updateAgentPrefs, useAgentList, useAgentPrefs, viewOf, type AgentPrefs, type AgentSession, type GroupBy, type ShowMode, type SortBy } from "../lib/agents";
import { activityOf, backgroundWaiting, externalLink, sessionDisplayTab, sessionTitle, sessionTitleDetail, stateOf, tabLabel } from "../lib/format";
import { useHerdrTabName } from "../lib/agents-display";
import { firstAndCurrent } from "../lib/persona-nav";
import { pickMainChat } from "../lib/handoff";
import { href, navigate, routePath, type Route } from "../lib/router";
import { rememberSessionRow, useStore } from "../lib/store";
import { draftLine, rowLine } from "../lib/row-line";
import { usageLimitLabel } from "../lib/usage-limit";
import { useLeftDraft } from "../lib/drafts";
import { planInRow, planLabel, planLine } from "../lib/plan";
import { answeredSession, answerPending, lastAnsweredAt } from "./assistant/answered-lookup";
import type { AgentState, Session } from "../lib/types";
import { cx, EASE, Kbd, Menu, sidebarFoldMotion, useMenu, useNow, type MenuItem } from "../ui/atoms";
import { StatusMark } from "../ui/StatusMark";
import { NoticeDot } from "../ui/NoticeDot";
import { runningWorkflows, workflowLine, workflowShortLine } from "../lib/workflows";
import { ultracodeMarkShown } from "../lib/ultracode";
import { Rolling } from "../ui/Rolling";
import { BrandMark } from "../ui/BrandMark";
import { OriginMark } from "../ui/ChannelMark";
import { autoOrigin } from "../lib/conversation-origins";
import { InfoTip } from "../ui/InfoTip";
import { Icon, type IconName } from "../ui/icons";
import { UltraMark } from "./chat/Ultracode";
import { contextMenuProps } from "../ui/context-menu";
import type { MenuAt } from "../ui/menu-model";
import { rowDetail, useAgentDetails } from "../lib/context-usage";
import { ContextUsageBar } from "../ui/ContextUsageBar";
import { AgentMark, SessionMark } from "../ui/AgentMark";
import { AgentAvatar } from "./agents/AgentAvatar";
import { useFolded } from "../lib/use-folded";
import { ariaKeyshortcuts, formatChord } from "../lib/shortcuts";
import { PagedSessionList } from "./agents/PagedSessionList";
import { useAccountGroups } from "./agents/account-groups";
import { runtimeIdentity } from "../lib/runtime-list";
import { heldSeen } from "../lib/seen-outbox";
import { heldLaunchWorking, launchForSession } from "../lib/launch-handoff";
import { childRollup } from "../lib/child-sessions";
import { foldedRow, ownRow } from "../lib/folded-rows";
import { rowCards } from "../lib/supervisor-held";
import { countSides, foldedStateWith, hubSides, unreadSides } from "../lib/side-fold";
import { useChildSessions, useHeldSides } from "../lib/use-child-sessions";
import { askSide } from "../lib/side-panel";
import { askOverviewSubagents } from "../lib/subagent-link";
import { backgroundStall } from "../lib/background-work";
import { SESSIONS_LIST_FEATURE } from "../lib/session-list";
import { personalHideDeps, personalOrder, personalSessions, removeAgentSession, useGoingIds } from "../lib/agents";
import { BADGE_CLASS, CURRENT_ROW, type GoFn, GroupCount, pageIconStyle, type RegFn, ROW_TITLE_MIN, type RowMenuFn, Section } from "./SidebarParts";
import { sidebarRowHover } from "./SidebarRowHover";
import { requestDirectWaiting } from "../lib/direct-waiting";
import { needsYouAutoOpen } from "../lib/needs-you-auto-open";
import type { UIColor } from "../lib/extension-ui-v6";
import { deliverySummary } from "../lib/undelivered";

// The main conversation uses the home symbol (Back to main chat's) and one Main conversation line.
// The assistant's identity belongs to the sidebar head; a left draft keeps its existing second line.
// Its menu (Copy session ID, End…: iOS's long press on the main row) opens on a right click, the menu key or its ⋯.
// Memoized like SessionRow: a push that leaves the main conversation as it was does not render it again.
export const PersonaRow = memo(function PersonaRow({ reg, hover, active, go, main, chord, waiting, onMenu: menuOf }: { reg: RegFn; hover: (k: string | null) => void; active: boolean; go: GoFn; main?: Session; /** The "Open Persona" shortcut as bound now ("": none). */ chord?: string; /** The main conversation's exact direct-user waiting count. */ waiting?: number; /** The conversation rows' menu, opened on `main`: none without it. */ onMenu?: RowMenuFn }) {
  const to: Route = { name: "persona" };
  const st = main ? activityOf(main) : undefined;
  const pending = waiting ?? main?.pending_interactions ?? 0;
  const onMenu = main && menuOf ? (anchor: HTMLElement, at?: MenuAt) => menuOf(main, anchor, at) : undefined;
  // one ref for the row's life, so a re-render does not let go of it and register it again (useGlide's reg)
  const ref = useMemo(() => reg("persona"), [reg]);
  const aside = onMenu && !active ? "group-hover/row:invisible" : undefined;
  // words left unsent in the main conversation's box, once you are not in it (#5568)
  const draft = draftLine(useLeftDraft(main ? `${main.machine_id}/${main.session_id}` : null));
  const open = (e: React.MouseEvent) => {
    // set to Full page, picked from another place it opens the page the conversation kept over it (#13164)
    go(to, () => pickMainChat(active))(e);
    if (e.defaultPrevented && main && pending > 0 && needsYouAutoOpen()) requestDirectWaiting(main.session_id);
  };
  const link = (
    <a
      ref={ref}
      href={href(to)}
      onClick={open}
      {...(onMenu ? contextMenuProps((at, el) => onMenu(el, at)) : {})}
      {...(main && sidebarRowHover(() => ({ title: sessionTitle(main), session: main, draft })))}
      onMouseEnter={() => hover("persona")}
      onFocus={() => hover("persona")}
      onBlur={() => hover(null)}
      aria-current={active ? "page" : undefined}
      aria-keyshortcuts={chord ? ariaKeyshortcuts(chord) : undefined}
      aria-description={t("sidebar.persona_main")}
      // pinned over the list (#5581), its own hover fill: the gliding highlight passes under the pinned box
      className={cx("relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row transition-[background-color,transform] duration-150 active:scale-[0.98] [@media(pointer:coarse)]:min-h-11", active ? CURRENT_ROW : "hover:bg-hover focus-visible:bg-hover", onMenu && (active ? "pr-8" : "group-hover/row:pr-8"), onMenu && "[@media(hover:none)]:pr-11")}
    >
      <Icon name="home" size={14} stroke={1.8} className="shrink-0 text-ink" />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-[13.5px] font-semibold text-ink">{t("sidebar.main_chat")}</span>
        {draft && <SecondLine line={draft} draft />}
      </span>
      {chord && <span aria-hidden data-persona-chord className={cx("hidden sm:inline-flex [@media(hover:none)]:hidden", aside)}><Kbd>{formatChord(chord)}</Kbd></span>}
      {pending > 0 ? (
        <span data-main-waiting={pending} className={cx("flex h-4.5 min-w-4.5 items-center justify-center rounded-full bg-field px-1 text-[10.5px] font-semibold text-attention tabular-nums", aside)}>{pending}</span>
      ) : st === "working" || st === "needs_you" || st === "unread" ? (
        <span className={cx("flex", aside)}><StatusMark state={st} /></span>
      ) : null}
    </a>
  );
  if (!onMenu) return link;
  return (
    <div className="group/row relative">
      {link}
      <button
        type="button"
        aria-label={t("conversation-menu.actions")}
        aria-haspopup="menu"
        title={t("sidebar.more")}
        tabIndex={-1}
        onClick={(e) => onMenu(e.currentTarget.parentElement ?? e.currentTarget)}
        className={cx("absolute top-1/2 right-1 z-20 size-6 -translate-y-1/2 items-center justify-center rounded-[6px] text-ink-3 hover:text-ink group-hover/row:flex [@media(hover:none)]:right-0 [@media(hover:none)]:flex [@media(hover:none)]:size-11 [@media(hover:none)]:bg-transparent", active ? "flex hover:bg-hover" : "hidden hover:bg-hover-2")}
      >
        <Icon name="more" size={14} />
      </button>
    </div>
  );
});

/** A subgroup title. Its disclosure and help are separate controls; `tally` is what a folded group still says of the rows
 *  it hides (Recent's unread chats, #13425), at the end before the help. */
const SUB_HEADING_TYPE = "text-[11px] font-semibold text-ink-3";
export function SubHeading({ label, folded, onToggle, help, tally }: { label: string; folded?: boolean; onToggle?: () => void; help?: string; tally?: ReactNode }) {
  return (
    <div className="flex items-center gap-1 px-2 py-0.5">
      <div role="heading" aria-level={3} className={cx("min-w-0 flex-1", !onToggle && "truncate", SUB_HEADING_TYPE)}>
        {onToggle ? (
          <button type="button" aria-expanded={!folded} onClick={onToggle} className="touch-hit relative flex w-full min-w-0 items-center gap-1 whitespace-nowrap rounded-[5px] text-left hover:text-ink-2 [@media(pointer:coarse)]:min-h-11">
            <span className="min-w-0 truncate">{label}</span>
            <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 transition-transform" style={{ ...sidebarFoldMotion(!folded), transform: folded ? "rotate(-90deg)" : "none" }} />
          </button>
        ) : label}
      </div>
      {tally}
      {help && <InfoTip name={label} text={help} className="[@media(pointer:coarse)]:size-11" />}
    </div>
  );
}

type NavRowProps = { onMenu?: (at: MenuAt, el: HTMLElement, button: boolean) => void; menuLabel?: string; k: string; reg: RegFn; hover: (k: string | null) => void; active: boolean; to: Route; go: GoFn; icon: IconName; /** Engine mark ID shown in place of `icon` (e.g. a starting agent placeholder's resolved engine). */ agent?: string; label: string; badge?: number | string; badgeTone?: keyof typeof BADGE_CLASS; /** Spoken for the badge in place of its digits (NavPage). */ badgeLabel?: string; notice?: string; hint?: string; hintCount?: { n: number; label: string }; title?: string; /** A state shown as a dot before the hint ("Automatic"'s rows: some run). */ dot?: AgentState; /** A page's icon color (NavPage.color, #10822). */ color?: UIColor };

// The pages take nothing from the sessions, yet every sessions push re-renders the sidebar: memoized like SessionRow
// (sameProps), a page row renders again only when its own props change (a badge, the highlight).
export const NavRow = memo(function NavRow({ k, reg, hover, active, to, go, icon, agent, label, badge, badgeTone, badgeLabel, notice, hint, hintCount, title, onMenu, menuLabel, dot, color }: NavRowProps) {
  // one ref for the row's life, so a re-render does not let go of it and register it again (useGlide's reg)
  const ref = useMemo(() => reg(k), [reg, k]);
  const link = (
    <a
      ref={ref}
      {...(onMenu ? contextMenuProps((at, el) => onMenu(at, el, false)) : {})}
      href={href(to)}
      title={title}
      onClick={go(to)}
      onMouseEnter={() => hover(k)}
      onFocus={() => hover(k)}
      onBlur={() => hover(null)}
      aria-label={notice ? `${label}, ${notice}` : undefined}
      aria-current={active ? "page" : undefined}
      className={cx("relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row transition-transform duration-150 active:scale-[0.98] [@media(pointer:coarse)]:min-h-11", active && CURRENT_ROW, onMenu && (active ? "pr-8" : "group-hover/row:pr-8"), onMenu && "[@media(hover:none)]:pr-11")}
    >
      <span className={active ? "text-ink" : "text-ink-3"} style={pageIconStyle(color, active)}>
        {agent ? <AgentMark agent={agent} size={14} decorative /> : <Icon name={icon} size={14} stroke={1.8} />}
      </span>
      <span className={cx("min-w-0 flex-1 truncate text-[13px]", active ? "font-medium text-ink" : "text-ink-2")}>{label}</span>
      {notice && <NoticeDot />}
      {dot && <StatusMark state={dot} size="sm" />}
      {hintCount ? <GroupCount {...hintCount} className="shrink-0" />
        : hint && <span className="text-[11px] text-ink-3 tabular-nums">{hint}</span>}
      {badge !== undefined && (
        <span className={cx("flex h-4.5 min-w-4.5 shrink-0 items-center justify-center whitespace-nowrap rounded-full px-1 text-[10.5px] font-semibold tabular-nums", BADGE_CLASS[badgeTone ?? "accent"])} style={{ animation: `pop-in 250ms ${EASE} both` }} key={badge}>
          {badgeLabel ? <><span aria-hidden>{badge}</span><span className="sr-only">{badgeLabel}</span></> : badge}
        </span>
      )}
    </a>
  );
  if (!onMenu) return link;
  // the ⋯ shows over the row's end on hover (a pointer); on a touch screen it stays, in the room the row leaves for it
  return (
    <div className="group/row relative">
      {link}
      <button
        type="button"
        aria-label={menuLabel}
        aria-haspopup="menu"
        title={t("sidebar.more")}
        tabIndex={-1}
        onClick={(e) => {
          const box = e.currentTarget.parentElement ?? e.currentTarget;
          onMenu({ anchor: box }, box, true);
        }}
        className="absolute top-1/2 right-1 z-20 hidden size-6 -translate-y-1/2 items-center justify-center rounded-[6px] text-ink-3 hover:bg-hover-2 hover:text-ink group-hover/row:flex [@media(hover:none)]:right-0 [@media(hover:none)]:flex [@media(hover:none)]:size-11 [@media(hover:none)]:bg-transparent"
      >
        <Icon name="more" size={14} />
      </button>
    </div>
  );
}, sameProps);

type ShellRowProps = {
  k: string;
  reg: RegFn;
  hover: (k: string | null) => void;
  active: boolean;
  to: Route;
  go: GoFn;
  label: string;
  hint?: string;
  title?: string;
  onMenu?: (at: MenuAt, el: HTMLElement, button: boolean) => void;
  menuLabel?: string;
};

/** An active non-Agent shell tab row inside a workspace group. Reserves the exact same leading column width as
 *  SessionRow (`14px` framed terminal mark + `gap-2` + `14px` status-dot slot + `gap-2`) so shell titles and
 *  Agent titles start on the exact same vertical X pixel. */
export const ShellRow = memo(function ShellRow({ k, reg, hover, active, to, go, label, hint, title, onMenu, menuLabel }: ShellRowProps) {
  const ref = useMemo(() => reg(k), [reg, k]);
  const link = (
    <a
      ref={ref}
      {...(onMenu ? contextMenuProps((at, el) => onMenu(at, el, false)) : {})}
      href={href(to)}
      title={title}
      onClick={go(to)}
      onMouseEnter={() => hover(k)}
      onFocus={() => hover(k)}
      onBlur={() => hover(null)}
      aria-current={active ? "page" : undefined}
      data-shell-row={k}
      className={cx(
        "relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row transition-transform duration-150 active:scale-[0.98] [@media(pointer:coarse)]:min-h-11",
        active && CURRENT_ROW,
        onMenu && (active ? "pr-8" : "group-hover/row:pr-8"),
        onMenu && "[@media(hover:none)]:pr-11",
      )}
    >
      <span
        aria-hidden
        data-shell-mark
        className={cx(
          "inline-flex size-3.5 shrink-0 items-center justify-center rounded-[3px] border border-line bg-inset",
          active ? "text-ink" : "text-ink-3",
        )}
      >
        <Icon name="terminal" size={10} stroke={2} />
      </span>
      <span aria-hidden data-shell-status className="flex size-3.5 shrink-0 items-center justify-center">
        <span className="size-1.5 rounded-full bg-ink-3/50" />
      </span>
      <span className={cx("min-w-0 flex-1 truncate text-meta", active ? "font-medium text-ink" : "text-ink-2")}>{label}</span>
      {hint && <span className={cx("max-w-[72px] min-w-6 shrink-[20] truncate text-[11px] text-ink-3", !active && "group-hover/row:invisible")}>{hint}</span>}
    </a>
  );
  if (!onMenu) return link;
  return (
    <div className="group/row relative">
      {link}
      <button
        type="button"
        aria-label={menuLabel}
        aria-haspopup="menu"
        title={t("sidebar.more")}
        tabIndex={-1}
        onClick={(e) => {
          const box = e.currentTarget.parentElement ?? e.currentTarget;
          onMenu({ anchor: box }, box, true);
        }}
        className={cx(
          "absolute top-1/2 right-1 z-20 size-6 -translate-y-1/2 items-center justify-center rounded-[6px] text-ink-3 hover:text-ink group-hover/row:flex [@media(hover:none)]:right-0 [@media(hover:none)]:flex [@media(hover:none)]:size-11 [@media(hover:none)]:bg-transparent",
          active ? "flex hover:bg-hover" : "hidden hover:bg-hover-2",
        )}
      >
        <Icon name="more" size={14} />
      </button>
    </div>
  );
}, sameProps);

// The list's one-line notes (loading, no match), as each writes it.
const NOTE = "px-2 py-1 text-label text-ink-3";

// Two shapes as well as two tones, so they tell apart without color: a framed terminal prompt and a window with a title bar.
const SOURCE_ICON = { terminal: { name: "terminal", tone: "text-ink-3" }, desktop: { name: "appWindow", tone: "text-source-desktop" } } as const;

/** Where the agent was opened (#2526), as a mark: a terminal or a desktop app's window in its own color, Lark's mark for a
 *  Lark group (a click opens its Feishu topic). Nothing for other sources. Its name is the tooltip and what a screen reader
 *  reads. At the row's end, so the title starts where an unmarked one does (#3043). */
function SourceTag({ s, className }: { s: Session; className?: string }) {
  const source = sessionSource(s);
  if (!source) return null;
  const label = sourceName(source);
  const mark = source === "feishu" ? <BrandMark brand="lark" size={16} fit="badge" /> : <Icon name={SOURCE_ICON[source].name} size={14} className={SOURCE_ICON[source].tone} />;
  const cls = cx("flex size-4 shrink-0 items-center justify-center", className);
  const ext = source === "feishu" ? externalLink(s) : undefined;
  if (ext) {
    // a link inside the row's link: opened by hand, so the row does not navigate too
    const open = (e: React.SyntheticEvent) => {
      e.preventDefault();
      e.stopPropagation();
      window.open(ext, "_blank", "noopener,noreferrer");
    };
    const title = t("lib-agent-groups.open_feishu_topic");
    return (
      <span data-source={source} role="link" tabIndex={0} title={`${label} · ${title}`} aria-label={`${label} · ${title}`} onClick={open} onKeyDown={(e) => e.key === "Enter" && open(e)} className={cx(cls, "cursor-pointer rounded-[4px] hover:bg-hover-2")}>
        {mark}
      </span>
    );
  }
  return <span data-source={source} role="img" title={label} aria-label={label} className={cls}>{mark}</span>;
}

/** A row's line under its title (rowLine, or draftLine): the red lead, then the words. */
function SecondLine({ line, draft }: { line: { lead?: string; text: string; tone?: "needs" | "reply" }; draft: boolean }) {
  return (
    <span data-row-line data-draft={draft ? "" : undefined} className="truncate text-[11.5px] leading-4 text-ink-3">
      {line.lead && <span className={cx("font-medium", line.tone === "reply" ? "text-green" : "text-attention")}>{line.lead}</span>}
      {line.text}
    </span>
  );
}

/** Unread as the rows draw it: a turn a usage limit stopped is drawn seen (it goes on by itself once the limit resets).
 *  A folded Persona chats or Recent heading counts the chats it hides that are (#13425). Just finished is not counted
 *  there, as in the Agents list's folded tally (docs/protocol/agent-states.md "Just finished"). */
export function unreadShown(s: Session): boolean {
  return activityOf(s) === "unread" && !usageLimitLabel(s);
}

/** Reads a parent's folded rows while its row names an unread side session (useChildSessions): they are held for the
 *  row (useHeldSides), each seen by this page's own mark, so the line can name them and a side seen here clears the row
 *  at once. Draws nothing. */
function HoldSides({ mid, sid }: { mid: string; sid: string }) {
  useChildSessions(mid, sid, true);
  return null;
}

/** The line under a parent's title while a side session folded into it has a reply nobody has seen (#13299: the row's
 *  green dot was the side's, and opening the parent never cleared it). It names the most recent such side, "+N" for the
 *  others, and opens it in the parent's Side sessions panel in one click (the side's own link, lib/side-panel.ts), where
 *  it is seen as any chat is. Before the side rows are read (`sides` absent) it only says how many there are. */
function SideUnreadLine({ sides, count, open }: { sides?: readonly Session[]; count: number; open: (side: Session, e: React.MouseEvent) => void }) {
  const first = sides?.[0];
  // in the unread tint, as the dot it explains: told apart from the parent's own words under its title
  const cls = "flex h-4 min-w-0 items-center gap-1 text-submeta leading-4 text-green";
  const mark = <Icon name="sideSessions" size={10} stroke={2.2} className="shrink-0" />;
  if (!first) {
    const words = t("sidebar.side_unread_count", { count });
    return <span data-side-unread="" title={words} className={cls}>{mark}<span className="truncate">{words}</span></span>;
  }
  const name = sessionTitle(first);
  const words = t("sidebar.side_unread", { title: name });
  const rest = sides!.slice(1);
  const hint = [t("sidebar.side_unread_open", { title: name }), rest.length ? t("sidebar.side_unread_also", { titles: formatList(rest.map((w) => sessionTitle(w))) }) : ""].filter(Boolean);
  const go = (e: React.MouseEvent) => {
    // a link inside the row's link: the row does not open the parent as well
    e.stopPropagation();
    open(first, e);
  };
  // the line's own width decides whether the lead fits beside the title (a container, as the row's column can be narrow)
  return (
    <span className="@container/side-line block min-w-0">
      <span
        data-side-unread={first.session_id}
        role="link"
        tabIndex={0}
        title={hint.join("\n")}
        aria-label={[words, ...hint].join(". ")}
        onClick={go}
        onKeyDown={(e) => {
          if (e.key !== "Enter") return;
          e.preventDefault();
          e.stopPropagation();
          e.currentTarget.click();
        }}
        className={cx(cls, "w-fit max-w-full cursor-pointer rounded-[4px] hover:text-ink focus-visible:text-ink")}
      >
        {mark}
        {/* the side's title keeps the room: the lead shows only where both fit (the sentence is in the tooltip and the
            spoken name either way) */}
        <span data-side-lead className="hidden shrink-0 font-medium @[10rem]/side-line:inline">{t("todos.kicker_line", { kicker: t("sidebar.side_unread_kicker") }).trimEnd()}</span>
        <span className="min-w-0 truncate">{name}</span>
        {rest.length > 0 && <span className="shrink-0 tabular-nums">{t("sidebar.side_unread_more", { count: rest.length })}</span>}
      </span>
    </span>
  );
}

// `prefix`: a word before the title (a tab's name, a pane's agent), in `prefixClass`
type SessionRowProps = { menuLabel?: string; /** The mark at the start of a row that does not show its state (a Persona conversation's chat icon), drawn while it neither works nor holds a reply nobody has opened (#5862, #13425). */ icon?: IconName; s: Session; k: string; reg: RegFn; hover: (k: string | null) => void; active: boolean; /** null: a targetless external direction, drawn as the same non-link row. */ to: Route | null; go: GoFn; tab?: boolean; /** Plain space and tab label for older nodes without a tab association. */ tabFallback?: string; label?: string; prefix?: string; prefixClass?: string; hint?: string; /** A lead's hint stays whole when there is room; the title keeps its minimum and the hint truncates first. */ hintFull?: boolean; host?: string; showState?: boolean; showTodoTag?: boolean; onMenu?: RowMenuFn; endRoom?: boolean; /** A lead row whose own conversation is idle while something under it runs (the direction tree's `running`, #6626): draw the working arc anyway (#6230). An own needs-you / unread state still takes precedence. */ workingMark?: boolean;
  /** 常驻 rows (#6214): the "在做 N · 等你 N" counts never fade on hover and the ⋯ sits in a reserved end slot instead
   * of covering them, so hovering removes no information. */
  keepTrailing?: boolean };

// Every sessions push re-renders the sidebar and the Agents list, while lib/sessions-delta.ts keeps the object of each
// session that did not change: a row renders again only when one of its props did. The props compare by identity
// (`go` and the menus are stable), except the route, which the call sites build anew and which is its path (routePath).
function sameProps<P extends { to: Route | null }>(a: P, b: P): boolean {
  const keys = Object.keys(a) as (keyof P)[];
  if (keys.length !== Object.keys(b).length) return false;
  return keys.every((key) => (key === "to" ? (a.to && b.to ? routePath(a.to) === routePath(b.to) : a.to === b.to) : Object.is(a[key], b[key])));
}

export const SessionRow = memo(function SessionRow({ s: row, k, reg, hover, active, to, go, tab, tabFallback, label, prefix, prefixClass, hint, hintFull, host, showState = true, showTodoTag, onMenu, menuLabel = t("sidebar.agent_actions"), endRoom, icon = "chat", workingMark = false, keepTrailing = false }: SessionRowProps) {
  // a parent of child sessions in its folded state (#5120): one of them waiting makes it need you, one working makes it
  // work; the rows of the paged list come as the hub sends them, the other lists' folded already (lib/folded-rows.ts)
  // its side sessions' part from their rows when an open chat holds them (#7944): a side seen here shows at once
  const sides = useHeldSides(row.machine_id, row.session_id, Boolean(row.children?.sides));
  const given = foldedRow(row, sides && countSides(sides));
  // what it asks you, or what it did, under its title (#3195): no hover or click needed to see why it is red or green;
  // its own card first, else what one of its child sessions asks (the card's home is this row's page). Cards a task's
  // supervisor holds (lib/supervisor-held.ts) are not the user's yet: left out of the line, the count and the dot.
  const { ix, kidCount, ownHeld, heldOnly } = useStore((st) => rowCards(st.interactions, given.machine_id, given.session_id));
  const pendingCount = Math.max(0, (given.pending_interactions ?? 0) - ownHeld) + kidCount;
  // a card answered here takes its red dot and "Has a question" line away at once, not at the hub's next push (#4286)
  const justAnswered = useStore((st) => lastAnsweredAt(st.answered, given.machine_id, given.session_id));
  const pending = useStore((st) => answerPending(st.answered, given.machine_id, given.session_id));
  // seen on this page and not yet recorded by the hub (lib/seen-outbox.ts): the paged list's rows come from the list
  // itself, not the store, so the row holds it seen too; newly launched first-turn sessions stay working across handoff (#12380)
  const seenMark = useStore((st) => st.seen[runtimeIdentity(given)]);
  const launch = useStore((st) => launchForSession(st.launches, given));
  const nowHandoff = Date.now();
  // the page's own mark on the parent clears its own part only: a side session's unread reply keeps it Unread, and its
  // line below says which (#13299). `own`: the row in its own state, as the hub sent it.
  const ownSeen = heldSeen(ownRow(row), seenMark, nowHandoff);
  const marked = heldSeen(answeredSession(given, Boolean(ix), justAnswered, pending), seenMark, nowHandoff);
  const keptBySide = marked.state !== given.state && activityOf(given) === "unread" && foldedStateWith(ownSeen, sides && countSides(sides)) === "unread";
  const base = heldLaunchWorking(keptBySide ? { ...marked, state: "unread" } : marked, launch, nowHandoff);
  const s = !base.view && heldOnly && stateOf(base) === "needs_you" ? { ...base, state: "working" as const, pending_interactions: 0 } : base;
  const preferTab = useHerdrTabName();
  const title = sessionTitle(s, preferTab);
  const displayTitle = label ?? title;
  const st = activityOf(s);
  // The mark draws the activity, which tells Unread, Just finished and Awaiting reply apart within `done`; an `error`
  // phase (a refused start, a usage limit, an interrupted session) has no activity of its own, so it draws the phase.
  const phase = stateOf(s);
  // Awaiting Reply (#5096): seen, but it asked you something you have not answered
  const awaiting = useStore((x) => awaitsReply(s, x.mutes));
  // Just finished (#9751): done before you looked, less than three hours ago
  const finished = useStore((x) => justFinished(s, x.mutes));
  // A Persona conversation's row (showState off) keeps its chat icon at rest; the same slot draws its mark while it works
  // (#5862) and while it holds a reply nobody has opened (#13425): Unread's glowing dot, with the Agent rows' unread
  // weight on its title, or Just finished's plain one. Opening the chat takes it away at once: viewSession marks it
  // seen, and the seen outbox (heldSeen above) keeps it so until the hub has it.
  const unread = !showState && unreadShown(s);
  const unseen = unread || (!showState && finished);
  // A lead row whose own conversation is idle while something under it runs (the tree's `running`: an Agent at work,
  // #6626) carries the working arc anyway (#6230). Its own needs-you, working or Unread state still wins, as iOS's
  // RowStateMark has it; Just finished, ranked after Working, gives way to the arc. Agent rows (showState) keep their
  // own state.
  const leadActive = workingMark && !showState && st !== "working" && st !== "needs_you" && !unread;
  // where it was opened, marked at the row's end: not on a tab with several panes (endRoom), where a herdr tab is a
  // terminal by definition, each pane's row under it says so, and the mark's room is the title's
  const source = showState && !s.assistant && !endRoom ? sessionSource(s) : undefined;
  // a Persona conversation the hub opened by itself (#3758, #5195): what opened it, in the source mark's place
  const origin = source ? undefined : autoOrigin(s);
  const hasTabAssociation = !!(s.slot_id?.trim() || s.tab_id?.trim());
  const primaryTab = sessionDisplayTab(s, preferTab);
  const associatedTab = hasTabAssociation && !primaryTab ? tabLabel(s) : "";
  const tabIdentity = associatedTab ? t("sidebar.tab_named", { label: associatedTab }) : "";
  // A structural tab row keeps its old plain-text fallback, but only a real association earns the localized identity.
  const structuralTabHint = !hasTabAssociation && tabFallback || (tab && !primaryTab ? tabLabel(s) : "");
  const tail = [hintFull ? hint : "", tabIdentity || structuralTabHint].filter(Boolean).join(" · ");
  // Settings › Appearance › show details on agent rows (#2430): a second line under an agent's title — folder · model · context · age
  const details = useAgentDetails();
  const withDetail = details && showState && !s.assistant;
  // The clock only updates rows with time-sensitive inline details or active work.
  const showsAge = withDetail && !!s.updated_at;
  const now = useNow(60_000, showsAge || runningWorkflows(s).length > 0 || s.turn_error_kind === "usage_limit" || Boolean(s.background_stalled_since));
  // background commands that look stuck (#13744): it is still Working, the row says they are quiet
  const stall = showState && !s.assistant ? backgroundStall(s, now) : null;
  // a logical conversation's input that has not gone in (#17431): said on its row, never an idle row that swallowed it
  const delivery = deliverySummary(s.undelivered);
  const undelivered = delivery.expired.length
    ? { expired: true, text: t("sidebar.undelivered_expired", { count: delivery.expired.length }) }
    : delivery.waiting ? { expired: false, text: t("sidebar.undelivered_waiting", { count: delivery.waiting }) } : null;
  const detail = withDetail ? rowDetail(s, now) : [];
  const workflow = showState && !s.assistant && st !== "offline" ? workflowShortLine(s) : "";
  const workflowTitle = workflow ? workflowLine(s, now) : "";
  // its own task list (#5302): 3/5 · the step in progress
  const plan = showState && !s.assistant && st !== "offline" && planInRow(s, st === "working") ? planLine(s) : "";
  // Unified parent-row subagent rollup (#27, Principle 4): waiting children > running subagents > quiet.
  // Clear an optimistic needs_you count once answered locally so the subtitle stays aligned with the status mark.
  const rollupSource = s !== given && st !== "needs_you" && s.children?.needs_you
    ? { ...s, children: { ...s.children, needs_you: 0 } }
    : s;
  const rollup = showState && !s.assistant ? childRollup(rollupSource) : null;
  const childLine = rollup
    ? t(rollup.kind === "needs_you" ? "sidebar.subagents_need_you" : "sidebar.subagents_running", { count: rollup.count })
    : "";
  const openSubagents = useCallback((e: React.MouseEvent) => {
    if (!to || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.stopPropagation();
    rememberSessionRow(row);
    askOverviewSubagents(row.machine_id, row.session_id);
    go(to)(e);
  }, [row, go, to]);
  // what sits at the row's end steps aside for the hover ⋯; the open row leaves room for its ⋯ instead
  const aside = active ? undefined : "group-hover/row:invisible";
  // A 常驻 row's counts stay on screen while the ⋯ fades in beside them (#6214).
  const hintAside = keepTrailing ? undefined : aside;
  // Behind a prefix or beside a lead's full hint, the title keeps room for a few characters: secondary text gives way first.
  const titleMin = prefix || (hintFull && tail) ? ROW_TITLE_MIN : "min-w-0";
  // The tab's name gives way before the session title does.
  const titleFirst = tail ? "basis-auto" : undefined;
  // one ref per key, so letting go takes out only this row's entry (keyedRefs)
  const regRow = useMemo(() => reg(k), [reg, k]);
  // words left unsent in its box, once you are not in it (#5568): in place of what it asks or did
  const draft = draftLine(useLeftDraft(`${given.machine_id}/${given.session_id}`));
  // a side session folded into it with a reply nobody has seen (#13299): named under the title while the row shows
  // Unread, read for the row while the hub's rollup or the held rows say there is one (HoldSides: kept while the hub still
  // counts it, so a side seen here does not drop the rows and bring the hub's count back for a moment)
  const sideWatch = showState && !s.assistant && to !== null;
  const hubUnread = sideWatch ? hubSides(given)?.unread ?? 0 : 0;
  const sidesUnread = useMemo(() => (sides ? unreadSides(sides) : undefined), [sides]);
  const holdSides = sideWatch && (hubUnread > 0 || (sidesUnread?.length ?? 0) > 0);
  const sideCount = sidesUnread ? sidesUnread.length : hubUnread;
  const sideLine = holdSides && st === "unread" && sideCount > 0;
  // only its sides are unread: what it did itself was seen, so its own last words do not stand under the green dot
  const sideOnly = sideLine && activityOf(ownSeen) !== "unread";
  const openSide = useCallback((side: Session, e: React.MouseEvent) => {
    if (!to || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    rememberSessionRow(row);
    rememberSessionRow(side);
    askSide(row.machine_id, row.session_id, side.session_id);
    go(to)(e);
  }, [row, go, to]);
  const line = draft ?? (showState && !s.assistant && !sideOnly ? rowLine(s, ix, awaiting, finished) : undefined);
  // ultracode on (#3428): the workflow mark in the AI colour right after the title; a row without it is drawn as before.
  // Not on an offline row: the node keeps the last value it saw after the process is gone, and a resume starts it off.
  const ultra = showState && ultracodeMarkShown(s);
  const RowTag = to ? "a" : "div";
  const destination = to ? {
    href: href(to),
    onClick: (event: React.MouseEvent) => { rememberSessionRow(row); go(to)(event); },
  } : {};
  const link = (
    <RowTag
      ref={regRow}
      {...destination}
      // right click (at the pointer), the menu key and Shift+F10 (beside the row): the row's menu, the same as its ⋯
      {...(onMenu ? contextMenuProps((at, el) => onMenu(s, el, at)) : {})}
      {...sidebarRowHover(() => ({
        title: displayTitle, session: s, titleDetail: displayTitle !== title ? title : sessionTitleDetail(s, preferTab),
        draft, working: leadActive, tab: associatedTab || structuralTabHint,
      }))}
      onMouseEnter={() => hover(k)}
      onFocus={() => hover(k)}
      onBlur={() => hover(null)}
      aria-current={active ? "page" : undefined}
      // a touch screen: rows a finger can hit (44), and room at the end for the ⋯ that is always there; the open row
      // keeps its ⋯ too, so it has room for it
      className={cx("relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row transition-transform duration-150 active:scale-[0.98] [@media(pointer:coarse)]:min-h-11", active && CURRENT_ROW, // room for "N agents" and the ⋯ beside it (instead of the ⋯'s own)
        endRoom ? "pr-[108px] [@media(hover:none)]:pr-[120px]" : keepTrailing
          // the ⋯ is an in-flow slot of its own, so the anchor itself leaves no room for an overlay
          ? "pr-1 [@media(hover:none)]:pr-1"
          : cx(onMenu && (active ? "pr-8" : "group-hover/row:pr-8"), onMenu && "[@media(hover:none)]:pr-11"))}
    >
      {/* the agent's mark before its state (#6390): what runs there, then how it is doing */}
      {showState && !s.assistant && <SessionMark s={s} size={14} />}
      <span className="flex size-3.5 shrink-0 items-center justify-center">{showState || st === "working" || unseen || leadActive ? <StatusMark state={leadActive ? "working" : phase === "error" ? "error" : usageLimitLabel(s) && st === "unread" ? "idle" : st} awaiting={awaiting} finished={finished} waiting={backgroundWaiting(s)} /> : <Icon name={icon} size={12} className="text-ink-3" />}</span>
      {prefix && <span className={prefixClass}>{prefix}</span>}
      {/* with the mark, the title sits in the column whether a second line shows or not: the same element through a
          workflow's start and end, so the mark does not fade in again */}
      {holdSides && <HoldSides mid={row.machine_id} sid={row.session_id} />}
      {ultra || detail.length > 0 || workflow || line || sideLine || childLine || plan || stall || undelivered ? (
        <span className={cx("flex flex-1 flex-col", titleMin, titleFirst)}>
          {ultra ? (
            <span className="flex min-w-0 items-center gap-1">
              <span className={cx("min-w-0 truncate text-[13px]", active ? "font-medium text-ink" : "text-ink-2", st === "unread" && !active && !usageLimitLabel(s) && "font-medium text-ink")}>
                {displayTitle}
              </span>
              <UltraMark size={10} />
            </span>
          ) : (
            <span className={cx("truncate text-[13px]", active ? "font-medium text-ink" : st === "offline" ? "text-ink-3" : "text-ink-2", st === "unread" && !active && !usageLimitLabel(s) && "font-medium text-ink")}>
              {displayTitle}
            </span>
          )}
          {line && <SecondLine line={line} draft={!!draft} />}
          {sideLine && <SideUnreadLine sides={sidesUnread} count={sideCount} open={openSide} />}
          {rollup && childLine && (
            <span
              data-children-line={rollup.kind}
              role={to ? "link" : undefined}
              tabIndex={to ? 0 : undefined}
              title={`${childLine} · ${t("chat-chatview.session_overview")}`}
              onClick={to ? openSubagents : undefined}
              onKeyDown={to ? (e) => {
                if (e.key !== "Enter") return;
                e.preventDefault();
                e.stopPropagation();
                e.currentTarget.click();
              } : undefined}
              className={cx(
                "flex h-4 w-fit max-w-full min-w-0 items-center gap-1 rounded-[4px] text-submeta leading-4 whitespace-nowrap",
                rollup.kind === "needs_you" ? "font-medium text-attention" : "text-ink-3",
                to && "cursor-pointer hover:text-ink focus-visible:text-ink",
              )}
            >
              {rollup.kind === "needs_you"
                ? <Icon name="agent" size={10} stroke={2.2} className="shrink-0" />
                : <StatusMark state="working" size="sm" label={false} />}
              <span className="truncate tabular-nums">{childLine}</span>
            </span>
          )}
          {stall && (
            <span data-bg-stalled title={stall.title} className="flex h-4 min-w-0 items-center gap-1 text-submeta leading-4 text-ink-3">
              <Icon name="clock" size={10} stroke={2.2} className="shrink-0" />
              <span className="truncate">{stall.text}</span>
            </span>
          )}
          {undelivered && (
            <span data-undelivered-line={undelivered.expired ? "expired" : "waiting"} role={undelivered.expired ? "alert" : "status"} title={undelivered.text}
              className={cx("flex h-4 min-w-0 items-center gap-1 text-submeta leading-4", undelivered.expired ? "font-medium text-red" : "text-orange")}>
              <Icon name={undelivered.expired ? "warning" : "clock"} size={10} stroke={2.2} className="shrink-0" />
              <span className="truncate tabular-nums">{undelivered.text}</span>
            </span>
          )}
          {plan && (
            <span data-plan-line title={planLabel(s)} aria-label={planLabel(s)} className="flex h-4 min-w-0 items-center gap-1 text-[11px] leading-4 text-ink-3">
              <Icon name="list" size={10} stroke={2.2} className="shrink-0" />
              <span className="truncate tabular-nums">{plan}</span>
            </span>
          )}
          {/* one line high: a piece that does not fit wraps out of sight instead of squeezing the others */}
          {workflow && (
            <span data-workflow-line title={workflowTitle} aria-label={workflowTitle} className="flex h-4 min-w-0 items-center gap-1 text-[11px] leading-4 text-ai">
              <Icon name="workflow" size={10} stroke={2.2} className="shrink-0" />
              {/* its count rolls to the new one (#3434); the name and phase fade in when they change */}
              <span className="truncate tabular-nums"><Rolling value={workflow} /></span>
            </span>
          )}
          {detail.length > 0 && <span className="flex h-4 min-w-0 flex-wrap gap-x-1 overflow-hidden whitespace-nowrap text-submeta leading-4 text-ink-3">
            {detail.map((d, i) => (
              <span key={i} title={d.title} className={cx("flex shrink-0 items-center gap-1 tabular-nums", d.danger ? "font-medium text-orange" : d.warn && "font-medium text-orange")}>
                {i > 0 && <span aria-hidden="true">·</span>}
                {d.usage && <ContextUsageBar usage={d.usage} className="h-1 w-5" />}
                {d.text}
              </span>
            ))}
          </span>}
        </span>
      ) : keepTrailing ? (
        // a lead's name reads as a row, even with nothing second to say: the ⋯'s reserved slot must not take the title's
        // place. Drawn here, in place of the plain title, so the name shows once (it was drawn twice, "Per… Per…"). An
        // unread reply takes the Agent rows' unread weight (#13425)
        <span className={cx("flex-1 truncate text-meta", titleMin, titleFirst, active || unread ? "font-medium text-ink" : "text-ink-2")}>
          {displayTitle}
        </span>
      ) : (
        <span className={cx("flex-1 truncate text-meta", titleMin, titleFirst, active ? "font-medium text-ink" : st === "offline" ? "text-ink-3" : "text-ink-2", st === "unread" && !active && "font-medium text-ink")}>
          {displayTitle}
        </span>
      )}
      {host && <span className={cx("shrink-0 rounded bg-hover px-1 text-[10px] leading-4 text-ink-3", aside)}>{host}</span>}
      {showTodoTag && s.todo && (
        <span
          className={cx("flex shrink-0 items-center justify-center rounded bg-hover px-1 py-0.5 text-ink-3", aside)}
          title={t("sidebar.todo_tag", { title: s.todo.title })}
          aria-label={t("sidebar.todo_tag", { title: s.todo.title })}
        >
          <Icon name="todo" size={10} stroke={2} />
        </span>
      )}
      {origin && <OriginMark origin={origin} size={16} className={aside} session={s} />}
      {/* the row's ⋯ takes the end on hover: what is there steps aside instead of showing half under it */}
      {tail && <span className={cx(hintFull ? "min-w-0 shrink-[20] truncate tabular-nums" : "max-w-[72px] min-w-6 shrink-[20] truncate", "text-[11px] text-ink-3", hintAside)}>{tail}</span>}
      {source && <SourceTag s={s} className={aside} />}
      {pendingCount > 0 && <span className={cx("flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-field px-1 text-[10px] font-semibold text-attention tabular-nums", aside)}>{pendingCount}</span>}
    </RowTag>
  );
  if (!onMenu) return link;
  // 常驻 rows keep their counts on hover (#6214): the ⋯ lives in a reserved end slot that fades in instead of an
  // absolute button covering the row's words. Other rows keep the overlay they always had.
  const menuButton = (inFlow: boolean) => (
    <button
      type="button"
      aria-label={menuLabel}
      aria-haspopup="menu"
      title={t("sidebar.more")}
      tabIndex={-1}
      onClick={(e) => onMenu(s, e.currentTarget.parentElement ?? e.currentTarget)}
      className={inFlow
        ? cx("relative z-20 my-1 flex size-6 shrink-0 items-center justify-center self-center rounded-[6px] text-ink-3 transition-opacity hover:bg-hover hover:text-ink [@media(hover:none)]:size-11",
          active ? "opacity-100" : "opacity-0 group-hover/row:opacity-100 [@media(hover:none)]:opacity-100")
        : cx("absolute top-1/2 right-1 z-20 size-6 -translate-y-1/2 items-center justify-center rounded-[6px] text-ink-3 hover:text-ink group-hover/row:flex [@media(hover:none)]:right-0 [@media(hover:none)]:flex [@media(hover:none)]:size-11 [@media(hover:none)]:bg-transparent", active ? "flex hover:bg-hover" : "hidden hover:bg-hover-2")}
    >
      <Icon name="more" size={14} />
    </button>
  );
  if (keepTrailing) {
    return (
      <div className="group/row relative flex items-center">
        <span className="min-w-0 flex-1">{link}</span>
        {menuButton(true)}
      </div>
    );
  }
  return (
    <div className="group/row relative">
      {link}
      {menuButton(false)}
    </div>
  );
}, sameProps);

// Personal agents: the Lark gateway's personal agents with a view menu (group / sort / show), a search field, pins and custom
// sections — the prefs live on the hub so iOS sees the same pins.
type MenuPage = "root" | "group" | "sort" | "show";
function agentViewItems(page: MenuPage, setPage: (p: MenuPage) => void, p: AgentPrefs, providers: string[]): MenuItem[] {
  const v = viewOf(p);
  const back: MenuItem = { key: "back", label: t("sidebar.back"), keepOpen: true, onSelect: () => setPage("root") };
  if (page === "group")
    return [back, ...(Object.keys(GROUP_LABEL) as GroupBy[]).map((g, i) => ({ key: g, label: GROUP_LABEL[g], divider: i === 0, checked: v.group === g, onSelect: () => updateAgentPrefs((x) => setView(x, { group: g })) }))];
  if (page === "sort")
    return [back, ...(Object.keys(SORT_LABEL) as SortBy[]).map((s, i) => ({ key: s, label: SORT_LABEL[s], divider: i === 0, checked: v.sort === s, onSelect: () => updateAgentPrefs((x) => setView(x, { sort: s })) }))];
  if (page === "show")
    return [
      back,
      ...(Object.keys(SHOW_LABEL) as ShowMode[]).map((s, i) => ({ key: s, label: SHOW_LABEL[s], divider: i === 0, checked: v.show === s, keepOpen: true, onSelect: () => updateAgentPrefs((x) => setView(x, { show: s })) })),
      ...providers.map((pr, i) => ({ key: `p:${pr}`, label: providerLabel(pr), hint: i === 0 ? t("sidebar.sources") : undefined, divider: i === 0, checked: !v.providers.length || v.providers.includes(pr), keepOpen: true, onSelect: () => updateAgentPrefs((x) => (viewOf(x).providers.length ? toggleProvider(x, pr) : setView(x, { providers: providers.filter((y) => y !== pr) }))) })),
    ];
  const showHint = v.show !== "all" ? SHOW_LABEL[v.show] : v.providers.length ? t("sidebar.source_count", { count: v.providers.length }) : SHOW_LABEL.all;
  return [
    { key: "group", label: t("sidebar.group_by"), hint: GROUP_LABEL[v.group], submenu: true, onSelect: () => setPage("group") },
    { key: "sort", label: t("sidebar.sort_by"), hint: SORT_LABEL[v.sort], submenu: true, onSelect: () => setPage("sort") },
    { key: "show", label: t("sidebar.show"), hint: showHint, submenu: true, onSelect: () => setPage("show") },
    {
      key: "section",
      label: t("sidebar.new_group"),
      icon: "plus",
      divider: true,
      onSelect: () => {
        const name = prompt(t("sidebar.group_name"))?.trim();
        if (name) updateAgentPrefs((x) => addSection(x, name));
      },
    },
    { key: "reset", label: t("sidebar.reset_view"), divider: true, disabled: isDefaultView(p), onSelect: () => updateAgentPrefs(resetView) },
    { key: "all", label: t("sidebar.view_all"), onSelect: () => navigate({ name: "agents" }) },
  ];
}


// Row actions (⋯ and right click): pin, move to a custom section, remove a section, remove from the list.
function agentRowItems(s: AgentSession, p: AgentPrefs): MenuItem[] {
  const pinned = p.pins.includes(s.id);
  const cur = p.assign[s.id];
  return [
    { key: "pin", label: pinned ? t("sidebar.unpin") : t("sidebar.pin"), icon: "pin", onSelect: () => updateAgentPrefs((x) => togglePin(x, s.id)) },
    ...p.sections.map((sec, i) => ({ key: `sec:${sec.id}`, label: t("sidebar.move_to_group", { name: sec.name }), divider: i === 0, checked: cur === sec.id, onSelect: () => updateAgentPrefs((x) => assignSection(x, s.id, cur === sec.id ? null : sec.id)) })),
    ...(cur ? [{ key: "unsec", label: t("sidebar.remove_from_group"), onSelect: () => updateAgentPrefs((x) => assignSection(x, s.id, null)) }] : []),
    { key: "hide", label: t("lib-agents.remove"), icon: "archive", divider: true, onSelect: () => void removeAgentSession(s, personalHideDeps) },
  ];
}

// memoized: its props are stable (reg, hover, go) or a string, and it reads the agents from their own store, so a
// sessions push does not reach it
const personalIdentity = (s: AgentSession) => s.id;
export const PersonalAgents = memo(function PersonalAgents(props: { reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const bounded = useStore((s) => s.features.includes(SESSIONS_LIST_FEATURE));
  const { loaded, sources } = useAgentList();
  // Keep the subscription mounted while hidden so setup changes can reveal the section without a reload.
  if (!loaded || !sources?.length || sources.some((s) => !s.enabled || ["missing", "disabled", "no_accounts"].includes(s.setup ?? "") || s.accounts?.length === 0)) return null;
  return bounded ? <BoundedPersonalAgents {...props} /> : <LegacyPersonalAgents {...props} />;
});

function BoundedPersonalAgents({ reg, hover, activeKey, go }: { reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const prefs = useAgentPrefs();
  const [rowMenu, setRowMenu] = useState<{ s: AgentSession; el: HTMLElement } | null>(null);
  const onMenu = useCallback((s: AgentSession, el: HTMLElement) => setRowMenu({ s, el }), []);
  const [folded, toggle] = useFolded("personal-agents", false, { holds: activeKey.startsWith("a:") });
  const page = useSyncExternalStore(personalSessions.subscribe, personalSessions.getSnapshot, personalSessions.getSnapshot);
  const going = useGoingIds();
  const accounts = useAccountGroups();
  if (!page.loaded || !page.rows.some((s) => !going.has(s.id))) return null;
  return <Section top title={t("sidebar.personal_agents")} folded={folded} onToggle={toggle} right={<GroupCount n={page.total} label={t("sidebar.agent_count", { count: page.total })} />}>
    {!folded && <PagedSessionList root={personalSessions} path="/agents/sessions" identity={personalIdentity} compare={personalOrder} event="agents" personal label={accounts.label} accountMark={accounts.accountMark}
      renderRow={(s) => (going.has(s.id) ? null : <AgentRow s={s} reg={reg} hover={hover} active={activeKey === `a:${s.id}`} go={go} pinned={prefs.pins.includes(s.id)} onMenu={onMenu} />)} />}
    {rowMenu && <Menu anchor={rowMenu.el} onClose={() => setRowMenu(null)} align="right" width={200} items={agentRowItems(rowMenu.s, prefs)} />}
  </Section>;
}

const LegacyPersonalAgents = memo(function LegacyPersonalAgents({ reg, hover, activeKey, go }: { reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const { sessions: listed, loaded, error } = useAgentList();
  // a row removed here leaves at once, before the hub's re-pull
  const going = useGoingIds();
  const sessions = useMemo(() => (going.size ? listed.filter((s) => !going.has(s.id)) : listed), [listed, going]);
  const [retrying, setRetrying] = useState(false);
  const retry = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      await refreshAgentSessions();
    } finally {
      setRetrying(false);
    }
  };
  const prefs = useAgentPrefs();
  const [folded, toggle] = useFolded("personal-agents", false, { holds: activeKey.startsWith("a:") });
  const [q, setQ] = useState("");
  const [searching, setSearching] = useState(false);
  const [all, setAll] = useState<Record<string, boolean>>({});
  const menu = useMenu();
  const [page, setPage] = useState<MenuPage>("root");
  const [rowMenu, setRowMenu] = useState<{ s: AgentSession; el: HTMLElement } | null>(null);
  // the same function on every render, so the memoized rows skip a re-render
  const onRowMenu = useCallback((s: AgentSession, el: HTMLElement) => setRowMenu({ s, el }), []);
  if (!loaded || !sessions.length) return null;
  const groups = arrangeSessions(sessions, prefs, q);
  const unread = totalUnread(sessions);
  const providers = [...new Set(sessions.map((s) => s.provider))];
  const filtered = !isDefaultView(prefs) || !!q;
  const sectionOf = (key: string) => (key.startsWith("sec:") ? key.slice(4) : "");
  return (
    <Section
      top
      title={t("sidebar.personal_agents")}
      folded={folded}
      onToggle={toggle}
      right={
        <>
          {unread ? <span title={t("sidebar.unread_messages_count", { count: unread })} className="flex h-4 min-w-4 items-center justify-center rounded-full bg-accent-tint px-1 text-[10px] font-semibold text-accent-ink tabular-nums"><span aria-hidden>{unread}</span><span className="sr-only">{t("sidebar.unread_messages_count", { count: unread })}</span></span> : sessions.length > 0 && <GroupCount n={sessions.length} label={t("sidebar.agent_count", { count: sessions.length })} />}
          <button type="button" title={t("sidebar.search_personal")} aria-label={t("sidebar.search_personal")} onClick={() => setSearching((x) => !x || !!q)} className={cx("touch-hit relative z-10 flex size-5 items-center justify-center rounded-[5px] text-ink-3 hover:bg-hover hover:text-ink-2", (searching || q) && "text-ink-2")}>
            <Icon name="search" size={12} />
          </button>
          <button
            type="button"
            title={t("sidebar.filter_sort")}
            aria-label={t("sidebar.filter_sort")}
            aria-haspopup="menu"
            onClick={(e) => {
              setPage("root");
              menu.open(e);
            }}
            className={cx("touch-hit relative z-10 flex size-5 items-center justify-center rounded-[5px] hover:bg-hover hover:text-ink-2", filtered ? "text-accent" : "text-ink-3", menu.anchor && "bg-hover")}
          >
            <Icon name="filter" size={12} />
          </button>
          {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} align="right" width={220} items={agentViewItems(page, setPage, prefs, providers)} />}
        </>
      }
    >
      {!loaded && <p className={NOTE}>{t("sidebar.loading")}</p>}
      {error && <div className="relative z-10 px-2 py-1">
        <p role="alert" className="text-[12px] break-words text-red">{t("sidebar.personal_failed", { reason: error })}</p>
        <button type="button" disabled={retrying} onClick={() => void retry()} className="touch-hit mt-1 rounded-control px-2 py-1 text-xs leading-normal whitespace-nowrap text-accent-ink hover:bg-hover disabled:cursor-wait disabled:opacity-50">
          {t(retrying ? "sidebar.loading" : "sidebar.retry")}
        </button>
      </div>}
      {(searching || q) && (
        <input
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              setQ("");
              setSearching(false);
            }
          }}
          placeholder={t("sidebar.search_personal")}
          className="relative z-10 mx-1 mb-1 h-7 rounded-control bg-inset px-2 text-[12px] text-ink shadow-hairline outline-none placeholder:text-ink-3"
        />
      )}
      {loaded && !error && !groups.length && <p className={NOTE}>{t("sidebar.no_match")}</p>}
      {groups.map((g) => {
        const more = !q && !all[g.key] && g.key !== "pinned" && g.sessions.length > 5;
        const isOpen = (s: AgentSession) => activeKey === `a:${s.id}`;
        const rows = more ? firstAndCurrent(g.sessions, 5, isOpen) : g.sessions;
        const sec = sectionOf(g.key);
        return (
          <div key={g.key} className="flex flex-col gap-px">
            {g.label && (groups.length > 1 || g.key === "pinned" || sec) && (
              <div className="group/g flex items-center gap-1.5 px-2 pt-1.5 pb-0.5 text-[11px] text-ink-3">
                {g.key === "pinned" && <Icon name="pin" size={10} />}
                <span className="min-w-0 flex-1 truncate">{g.label}</span>
                {g.unread > 0 && <span className="tabular-nums text-accent-ink">{g.unread}</span>}
                {sec && (
                  <button type="button" title={t("sidebar.delete_group")} aria-label={t("sidebar.delete_group_named", { name: g.label })} onClick={() => confirm(t("sidebar.delete_group_confirm", { name: g.label })) && updateAgentPrefs((x) => removeSection(x, sec))} className="relative z-10 opacity-0 group-hover/g:opacity-100 hover:text-ink-2">
                    <Icon name="x" size={10} />
                  </button>
                )}
              </div>
            )}
            {rows.map((s) => (
              <AgentRow key={s.id} s={s} reg={reg} hover={hover} active={isOpen(s)} go={go} pinned={prefs.pins.includes(s.id)} onMenu={onRowMenu} />
            ))}
            {g.sessions.length > 5 && !q && g.key !== "pinned" && (
              <button type="button" onClick={() => setAll((x) => ({ ...x, [g.key]: !x[g.key] }))} className="relative z-10 w-full rounded-[7px] px-2 py-1 text-left text-[12px] text-ink-3 hover:text-ink-2">
                {more ? t("sidebar.show_more", { count: g.sessions.length - rows.length }) : t("sidebar.show_less")}
              </button>
            )}
          </div>
        );
      })}
      {rowMenu && <Menu anchor={rowMenu.el} onClose={() => setRowMenu(null)} align="right" width={200} items={agentRowItems(rowMenu.s, prefs)} />}
    </Section>
  );
});

// memoized (props by identity: `s` is kept while the agent is unchanged, the functions are stable)
export const AgentRow = memo(function AgentRow({ s, reg, hover, active, go, pinned, onMenu }: { s: AgentSession; reg: RegFn; hover: (k: string | null) => void; active: boolean; go: GoFn; pinned: boolean; onMenu: (s: AgentSession, el: HTMLElement) => void }) {
  const k = `a:${s.id}`;
  // one ref for the row's life, so a re-render does not let go of it and register it again (useGlide's reg)
  const regRow = useMemo(() => reg(k), [reg, k]);
  const to: Route = { name: "agents", id: s.id };
  const unread = s.unreadCount > 0;
  const title = s.title || t("lib-format.untitled_session");
  return (
    <div className="group/row relative" onContextMenu={(e) => {
      e.preventDefault();
      onMenu(s, e.currentTarget);
    }}>
      <a
        ref={regRow}
        href={href(to)}
        onClick={go(to)}
        {...sidebarRowHover(() => ({ title, agent: s }))}
        onMouseEnter={() => hover(k)}
        onFocus={() => hover(k)}
        onBlur={() => hover(null)}
        aria-current={active ? "page" : undefined}
        className={cx("relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row transition-transform duration-150 active:scale-[0.98] [@media(hover:none)]:pr-11 [@media(pointer:coarse)]:min-h-11", active && cx(CURRENT_ROW, "pr-7"))}
      >
        <AgentAvatar s={s} size={16} />
        <span className={cx("min-w-0 flex-1 truncate text-[13px]", active || unread ? "font-medium text-ink" : "text-ink-2")}>{title}</span>
        {unread && <span className={cx("flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-accent px-1 text-[10px] font-semibold text-white tabular-nums", !active && "group-hover/row:invisible")}>{s.unreadCount}</span>}
      </a>
      <button
        type="button"
        title={pinned ? t("sidebar.unpin") : t("sidebar.pin")}
        aria-label={pinned ? t("sidebar.unpin") : t("sidebar.pin")}
        onClick={() => updateAgentPrefs((x) => togglePin(x, s.id))}
        className={cx("absolute top-1/2 right-1 z-20 flex size-5 -translate-y-1/2 items-center justify-center rounded-[5px] hover:bg-hover group-hover/row:opacity-100 focus-visible:opacity-100 [@media(hover:none)]:hidden", active ? "opacity-100" : "opacity-0", pinned ? "text-accent" : "text-ink-3")}
      >
        <Icon name="pin" size={11} />
      </button>
      {/* a touch screen has no hover pin and no right click: the row's menu ("Pin" is in it) behind a ⋯ that stays */}
      <button
        type="button"
        aria-label={t("sidebar.personal_actions")}
        aria-haspopup="menu"
        title={t("sidebar.more")}
        onClick={(e) => onMenu(s, e.currentTarget.parentElement ?? e.currentTarget)}
        className="absolute top-1/2 right-0 z-20 hidden size-11 -translate-y-1/2 items-center justify-center text-ink-3 [@media(hover:none)]:flex"
      >
        <Icon name="more" size={14} />
      </button>
    </div>
  );
});
