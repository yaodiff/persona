// A lead's Team side panel (#5180, after cindy's Team tab, `OrcaWorkerPanel` / `WorkerListToolbar`; docs/web/web.md "Team"):
// a 32 px strip of the lead's workers (the other Agents of its herdr tab, lib/team.ts) as round pills, each with its status
// mark and title, the one shown filled; under it the shown worker's own conversation, the chat page's without its header,
// where it is read, answered, written to and stopped. The lead stays the page's chat.
// A session's Side sessions panel (#7944, lib/side-panel.ts) is the same layout over its side sessions: their strip in the
// panel's order, and the chosen side's own conversation, landed at a search hit's message when a link asked for one.
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { sessionTitle, activityOf, stateOf } from "../../lib/format";
import { agentLabel } from "../../lib/mentions";
import { STATE_LABEL, stateName } from "../../lib/state-labels";
import { getSession, useStore } from "../../lib/store";
import { awaitsReply, justFinished } from "../../lib/agent-groups";
import { shownWorker, showWorker, teamWorkers, workerCount } from "../../lib/team";
import { sidesAmong } from "../../lib/side-fold";
import { shownSide, showSide } from "../../lib/side-panel";
import { useChildSessions } from "../../lib/use-child-sessions";
import type { Session } from "../../lib/types";
import { cx, Loading } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { PanelHeader } from "../../ui/PanelHeader";
import { StatusMark } from "../../ui/StatusMark";
import { t } from "../../i18n";
import { scrollBehavior } from "../../ui/stream/motion";
import { ChatView } from "./ChatView";

/** The pill an arrow key (or Home / End) moves to from `i` among `n`; undefined for any other key. */
export function nextTab(i: number, key: string, n: number): number | undefined {
  if (key === "ArrowRight") return (i + 1) % n;
  if (key === "ArrowLeft") return (i - 1 + n) % n;
  if (key === "Home") return 0;
  if (key === "End") return n - 1;
  return undefined;
}

/** How far one press of a strip's arrow scrolls it (cindy's `workerTabsScrollStep`): most of what is in view. */
export function stripStep(width: number): number {
  return Math.max(80, Math.floor(width * 0.7));
}

/** A pill's hover text: which Agent, its title and its state, as the pane tabs said it. */
export function workerHint(w: Session, awaiting: boolean, finished = false): string {
  return [agentLabel(w), sessionTitle(w), pillState(w, awaiting, finished)].filter(Boolean).join(" · ");
}

/** A pill's state word: Awaiting your reply (#5096) and Just finished (#9751) over the seen state they stand on. */
function pillState(w: Session, awaiting: boolean, finished: boolean): string {
  const st = w.view ? stateOf(w) : activityOf(w);
  if (st === "idle" && awaiting) return stateName("awaits_reply");
  if (st === "idle" && finished) return stateName("just_finished");
  return STATE_LABEL[st];
}

export function TeamPanel({ mid, sid, worker, at, onClose }: { mid: string; sid: string; worker?: string; at?: string; onClose: () => void }) {
  const lead = useStore(() => getSession(mid, sid));
  // the lead's folded rows (lib/use-child-sessions.ts): the store's, or on a paging hub one bounded read of them
  const kids = useChildSessions(mid, sid, workerCount(lead) > 0 || Boolean(worker));
  const workers = useMemo(() => teamWorkers(kids.rows), [kids.rows]);
  const shown = shownWorker(workers, mid, sid, worker);
  const running = workers.filter((w) => stateOf(w) === "working").length;
  const body = useId();
  return (
    <StripPanel
      icon="users"
      title={t("sidepanel.team")}
      subtitle={workers.length ? t("chat-teampanel.summary", { count: workers.length, running }) : undefined}
      onClose={onClose}
      strip={workers.length > 0 && <SessionStrip members={workers} shown={shown?.session_id} controls={body} onPick={(w) => showWorker(mid, sid, w.session_id)} words={WORKER_WORDS} hint={workerHint} />}
      body={body}
      shown={shown}
    >
      {shown ? (
        // the worker's conversation: its own chat, keyed by it, so switching pills starts it at its latest message
        <ChatView key={`${mid}/${shown.session_id}`} mid={mid} sid={shown.session_id} embedded landAt={shown.session_id === worker ? at : undefined} />
      ) : !kids.loaded ? (
        <div className="flex flex-1 items-center justify-center"><Loading /></div>
      ) : (
        <div className="flex flex-1 items-center justify-center px-6 text-center text-[13px] text-ink-3">{t("chat-teampanel.waiting")}</div>
      )}
    </StripPanel>
  );
}

/** A side pill's hover text and spoken name: its title and its state (no engine or model name, DESIGN_SYSTEM 5.6). */
export function sideHint(w: Session, awaiting: boolean, finished = false): string {
  return [sessionTitle(w), pillState(w, awaiting, finished)].filter(Boolean).join(" · ");
}

export function SidesPanel({ mid, sid, side, at, onClose }: { mid: string; sid: string; side?: string; at?: string; onClose: () => void }) {
  const parent = useStore(() => getSession(mid, sid));
  // the parent's folded rows (lib/use-child-sessions.ts), its side sessions among them, sides of sides too
  const kids = useChildSessions(mid, sid, Boolean(parent?.children?.total) || Boolean(side));
  const sides = useMemo(() => sidesAmong(kids.rows), [kids.rows]);
  const shown = shownSide(sides, mid, sid, side);
  const body = useId();
  return (
    <StripPanel
      icon="sideSessions"
      title={t("chat-teampanel.side_sessions")}
      subtitle={shown ? sessionTitle(shown) : undefined}
      onClose={onClose}
      strip={sides.length > 0 && <SessionStrip members={sides} shown={shown?.session_id} controls={body} onPick={(w) => showSide(mid, sid, w.session_id)} words={SIDE_WORDS} hint={sideHint} named />}
      body={body}
      shown={shown}
    >
      {shown ? (
        // the side's own conversation (its composer, its cards, Stop); a link's landing only on the side it asked for
        <ChatView key={`${mid}/${shown.session_id}`} mid={mid} sid={shown.session_id} embedded landAt={shown.session_id === side ? at : undefined} />
      ) : !kids.loaded ? (
        <div className="flex flex-1 items-center justify-center"><Loading /></div>
      ) : (
        <div className="flex flex-1 items-center justify-center px-6 text-center text-meta text-ink-3">{t("chat-teampanel.no_sides")}</div>
      )}
    </StripPanel>
  );
}

/** The two panels' frame: the side panel's header, the strip, and under it the shown session's conversation. */
function StripPanel({ icon, title, subtitle, onClose, strip, body, shown, children }: { icon: IconName; title: string; subtitle?: string; onClose: () => void; strip: React.ReactNode; body: string; shown?: Session; children: React.ReactNode }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader icon={icon} title={title} subtitle={subtitle} onClose={onClose} />
      {strip}
      <div id={body} role={shown ? "tabpanel" : undefined} aria-label={shown ? sessionTitle(shown) : undefined} className="flex min-h-0 flex-1 flex-col">
        {children}
      </div>
    </div>
  );
}

/** What a strip says of itself: its name and its scroll arrows'. */
interface StripWords {
  label: string;
  left: string;
  right: string;
}
const WORKER_WORDS: StripWords = { label: "chat-teampanel.strip_label", left: "chat-teampanel.scroll_left", right: "chat-teampanel.scroll_right" };
const SIDE_WORDS: StripWords = { label: "chat-teampanel.side_sessions", left: "chat-teampanel.sides_scroll_left", right: "chat-teampanel.sides_scroll_right" };

/** cindy's tab layout of the worker list: one row of pills that scrolls sideways, with ‹ › at its ends while there is more
 *  that way and the edges fading; a mouse wheel scrolls it too. The shown pill is kept in view. Arrow keys, Home and End
 *  move between the pills as in any tab list. The Side sessions panel's strip is the same; its pills are named by their
 *  hover text (`named`: title and state), as their state is said nowhere else in words. */
function SessionStrip({ members: workers, shown, controls, onPick, words, hint, named = false }: { members: Session[]; shown?: string; controls: string; onPick: (w: Session) => void; words: StripWords; hint: (w: Session, awaiting: boolean, finished: boolean) => string; named?: boolean }) {
  const scroller = useRef<HTMLDivElement>(null);
  const pills = useRef<(HTMLButtonElement | null)[]>([]);
  const [more, setMore] = useState({ left: false, right: false });
  const at = Math.max(0, workers.findIndex((w) => w.session_id === shown));
  const mutes = useStore((s) => s.mutes);
  const measure = useCallback(() => {
    const el = scroller.current;
    if (!el) return;
    const max = el.scrollWidth - el.clientWidth;
    setMore((m) => {
      const next = { left: el.scrollLeft > 1, right: max - el.scrollLeft > 1 };
      return m.left === next.left && m.right === next.right ? m : next;
    });
  }, []);
  useEffect(() => {
    measure();
    const el = scroller.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [measure, workers.length]);
  useEffect(() => {
    pills.current[at]?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
    measure();
  }, [at, measure, workers.length]);
  const nudge = (dir: 1 | -1) => {
    const el = scroller.current;
    if (!el) return;
    el.scrollBy({ left: dir * stripStep(el.clientWidth), behavior: scrollBehavior() });
  };
  const arrow = (dir: 1 | -1) => {
    const on = dir < 0 ? more.left : more.right;
    const label = t(dir < 0 ? words.left : words.right);
    // the arrows' places stay when they hide, so the strip does not shift as its ends are reached
    return (
      <span className="flex h-6 w-5 shrink-0 items-center justify-center">
        {on && (
          <button type="button" aria-label={label} title={label} tabIndex={-1} onClick={() => nudge(dir)} className="flex h-6 w-5 items-center justify-center rounded-full text-ink-3 hover:text-ink">
            <Icon name={dir < 0 ? "chevronLeft" : "chevronRight"} size={14} />
          </button>
        )}
      </span>
    );
  };
  return (
    <div className="relative flex h-8 shrink-0 items-center border-b border-line px-1.5">
      {arrow(-1)}
      <div
        ref={scroller}
        role="tablist"
        aria-label={t(words.label)}
        onScroll={measure}
        onWheel={(e) => {
          const el = e.currentTarget;
          if (Math.abs(e.deltaY) <= Math.abs(e.deltaX) || el.scrollWidth <= el.clientWidth) return;
          el.scrollLeft += e.deltaY;
          measure();
        }}
        className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto overscroll-x-contain [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      >
        {workers.map((w, i) => {
          const on = i === at;
          const st = w.view ? stateOf(w) : activityOf(w);
          const awaiting = awaitsReply(w, mutes);
          const finished = justFinished(w, mutes);
          return (
            <button
              key={w.session_id}
              ref={(el) => {
                pills.current[i] = el;
              }}
              type="button"
              role="tab"
              aria-selected={on}
              aria-controls={controls}
              tabIndex={on ? 0 : -1}
              title={hint(w, awaiting, finished)}
              aria-label={named ? hint(w, awaiting, finished) : undefined}
              onClick={() => !on && onPick(w)}
              onKeyDown={(e) => {
                const to = nextTab(i, e.key, workers.length);
                if (to === undefined) return;
                e.preventDefault();
                pills.current[to]?.focus();
                if (to !== i) onPick(workers[to]!);
              }}
              className={cx(
                "touch-hit inline-flex h-6 max-w-[220px] shrink-0 items-center gap-1.5 rounded-full border px-2 text-[11px] leading-none transition-colors duration-150",
                on ? "border-ink bg-ink text-surface" : "border-line bg-field text-ink hover:bg-hover",
              )}
            >
              <StatusMark state={st} awaiting={awaiting} finished={finished} size="sm" label={false} />
              <span className="min-w-0 truncate font-medium">{sessionTitle(w)}</span>
            </button>
          );
        })}
      </div>
      {/* the ends fade while there is more that way, as cindy's strip does */}
      {more.left && <span aria-hidden className="pointer-events-none absolute inset-y-0 left-6.5 w-4 bg-linear-to-r from-page to-transparent" />}
      {more.right && <span aria-hidden className="pointer-events-none absolute inset-y-0 right-6.5 w-4 bg-linear-to-l from-page to-transparent" />}
      {arrow(1)}
    </div>
  );
}
