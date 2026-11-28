import { pendingPlanCounts } from "../lib/pending-plan-counts";
import { loadFeed, useFeed } from "../lib/feed";
import { saveAgentsDisplay, useAgentsDisplay, useAgentsDisplayEditable, useBotmuxHidden, useBotmuxShown, useShowFilter, useSortByActivity, type AgentsDisplay, type DisplayChange } from "../lib/agents-display";
import { useBotmuxMode } from "../lib/botmux-mode";
import { activityOrder, useActivityDates } from "../lib/agent-sort";
import { useSidebarEngaged } from "../lib/sidebar-engaged";
import { setChatList, useChatList } from "../lib/agent-chat-list";
import { SectionAdd } from "./SectionAdd";
import { AgentsAdd } from "./agents/AgentsAdd";
import { isSidebarAgent } from "../lib/sidebar-agent";
import { launchDisplayEngine } from "../lib/new-agent";
import { storedValue, storeValue } from "../lib/compatibility";
// Beautiful UI's Sidebar Nav: workspace row, quick search, sections, one gliding highlight. Persona's main conversation
// is the first entry (the app opens on the assistant); a new conversation and a new agent are the "+" of their sections (#1239).
import { Fragment, lazy, memo, Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { NARROW, useMedia } from "../lib/viewport";
import { AGENT_FILTERS, agentFilter, applyFilter, arrangeSpaces, extendKept, filterCounts, isBotmux, isSpaceMuted, matchesFilter, matchingKeys, removeKept, shownFilters, unreadOf, withoutBotmux, type AgentFilter, type FilterCounts } from "../lib/agent-filter";
import { applyDimensionFilters, buildAgentFacets, DEFAULT_DIMENSION_FILTERS, hasActiveDimensionFilters, type AgentDimensionFilters, type AgentFacetCounts } from "../lib/agent-chatlist-filter";
import { pagedFilterOf, resetAgentListFilters, setAgentListFilters, useAgentListFilters } from "../lib/agent-list-filters";
import { AgentChatListRow, AgentFilterButton, AgentFilterChips } from "./agents/AgentChatListControls";
import { agentGrouping, AGENT_GROUPINGS, type AgentGrouping, buildAgentGroups, buildSpaces, heldPlace, hostsOf, isHeldSeen, isQuiet, needsSeenOf, sessionHost, splitAgentSessions, type AgentGroup, type Space } from "../lib/agent-groups";
import { isMuted } from "../lib/session-actions";
import { engineName } from "../lib/engine-names";
import { heldOrderFor, holdNotedSeen, noteShownUnseen, releaseAllSeen, releaseSeen, useHeldSeenOrder, useSeenRowsMode, useSeenRowsNoticed, useSeenVisit } from "../lib/seen-rows";
import { reportBoundary } from "../lib/error-reports";
import { isChunkError, reloadForStaleChunk, reloadPage } from "./PageBoundary";
import { useWorkspaceCatalog } from "../lib/workspace-catalog";
import { keyedRefs } from "../lib/keyed-refs";
import { buildMachineTree, machineSessionOrder, machineSessionPresentation } from "../lib/herdr-tree";
import { selectSidebarMachines } from "../lib/sidebar-machines";
import { machineWithHub } from "../lib/mesh";
import { backgroundWaiting, baseName, sessionTitle, stateOf, tabLabel } from "../lib/format";
import { cardState } from "../lib/card-focus";
import { agentKey, isSpacePinned, pinnedSessions } from "../lib/pins";
import { firstAndCurrent } from "../lib/persona-nav";
import { recentConversations } from "../lib/conversation-origins";
import { isBackgroundSupervisor } from "../lib/background-supervisor";
import { setRecentMore, useRecentCount, useRecentMore } from "../lib/sidebar-recent";
import { countsLabel, directionExternal, directionHint, directionLabel, useDirectionNames, useLeaderCounts, userCheckLabel, visibleSectionCounts } from "../lib/direction-names";
import { resolveSidebarDirections, sidebarConversations, SidebarDirectionsSection, type SidebarDirectionEntry } from "./SidebarDirections";
import { conversationPage } from "../lib/conversation-page";
import { href, navigate, onLinkClick, showsSession, type Route } from "../lib/router";
import { useScrollMemory } from "../lib/scroll-memory";
import { boundedCounts, hasAgentViews, retryFullAgentSessions, useAgentSessions, useFullAgentSessions } from "../lib/sessions";
import type { FoldedCounts } from "../lib/folded-counts";
import { heldAsWorking, rowCards } from "../lib/supervisor-held";
import { isInFlightLaunch } from "../lib/launch-handoff";
import { getState, refresh, refreshTodos, useHubUnreachable, useStore } from "../lib/store";
import { SESSIONS_SIDE_FOLD_FEATURE } from "../lib/side-fold";
import { conversationTitle } from "./assistant/conversations";
import { sameManager, useShownManager } from "../lib/manager-choice";
import { agentGroupLabel, agentTreeState, buildTodoTree, shareTree, worstTreeState, type TodoTree } from "../lib/todo-tree";
import { openTodoFromTag, sortTodos, TODO_STATUS_INFO, type TodoStatus } from "../lib/todos";
import type { Machine, Session } from "../lib/types";
import { Collapse, cx, EASE, Kbd, Menu, sidebarFoldMotion, useMenu, useNow, type MenuItem } from "../ui/atoms";
import { StateTally, StatusMark } from "../ui/StatusMark";
import { NoticeDot } from "../ui/NoticeDot";
import { Icon } from "../ui/icons";
import { HubNoticeLine as HubNoticeLineContent } from "./agents/HubNoticeRow";
import { DeleteSessionDialog, RenameDialog, RenameSpaceDialog } from "./chat/SessionDialogs";
import { AGENT_ARCHIVES_FEATURE } from "../lib/agent-archives";
import { AgentRowMenu, MarkSeenDialog as MarkSeenDialogContent, NodeMenu, SpaceMenu } from "./AgentMenus";
import { NodePrompt as NodePromptContent } from "./NodePrompt";
import { SidebarUsage as SidebarUsageContent } from "./UsageBars";
import type { MenuNode } from "../lib/node-actions";
import type { MenuAt } from "../ui/menu-model";
import { useSidebarTerminals } from "../lib/sidebar-terminals";
import { usePanes } from "../lib/terminal-panes";
import { buildTerminalTree, filterTerminalTree, shellsOnly, type TermSpace } from "../lib/terminals";
import type { LinkTargetSpace } from "./LinkTodoDialog";
import { lazyDialog } from "./LazyOverlay";
import { lazyPage } from "../lib/lazy-page";
import { FoldScope, useFolded } from "../lib/use-folded";
import { clipsOverflow, glideBox, glideStyle, sameBox, type GlideBox, type Span } from "../lib/glide";
import { AGENTS_FOLD, onGrouping, pagedAgentList } from "../lib/sidebar-api";
import { jumpHint, useBindings } from "../lib/shortcuts";
import { markAllSeenLabel, stateCount } from "../lib/state-labels";
import { PagedSessionList, type PagedQuickFilters } from "./agents/PagedSessionList";
import type { HeldSeenGroups } from "./agents/GroupedSessions";
import { ListError } from "../ui/ListError";
import { SESSION_LIST_STYLE } from "../ui/list-metrics";
import { RUNTIME_AGENTS_PATH, supportsPagedAgents, runtimeActiveAt, runtimeAgents, runtimeIdentity, runtimeNeedsYou, runtimeOrder, runtimeParentIdentity, runtimePlace, runtimeRemoval } from "../lib/runtime-list";
import { JUST_FINISHED_KEY, SESSIONS_CATALOG_FEATURE, SESSIONS_JUST_FINISHED_FEATURE, SESSIONS_QUICK_FILTERS_FEATURE, SESSIONS_TODO_FEATURE, type GroupDimension, type TodoNames } from "../lib/session-groups";
import { filterQuery, SESSIONS_LIST_FEATURE, type ListCounts, type ListFilters } from "../lib/session-list";
import { followHinted } from "../lib/hinted-read";
import { heldCounts } from "../lib/seen-outbox";
import { heldSidesOf, useHeldVersion } from "../lib/use-child-sessions";
import { NoComputers, useAddComputer } from "./machines/AddComputerEntry";
import { BADGE_CLASS, DOT_CLASS, type GoFn, GroupCount, HISTORY_ICON, holdsRow, morePagesMenu, type NavPage, type NodeMenuFn, pageIconStyle, pageLabel, type RegFn, rowKey, type RowMenuFn, Section, SidebarGroup, GroupFoldButton, useActiveKey, useGo, useNavPages } from "./SidebarParts";
import { NavRow, PersonalAgents, PersonaRow, SessionRow, SubHeading, unreadShown } from "./SidebarRows";
import { GroupBranch, LooseSection, MachineSection, RepoMachineSection, SessionTally, ShellSpaceSection, type SpaceMenuFn, type WorkspaceTreeActions, SpaceSection, TodoTreeView } from "./SidebarTree";
import { CollapseButton, PagesTally } from "./SidebarRail";
import type { PageSection } from "../lib/sidebar-pages";
import { WindowedRows } from "./WindowedRows";
import { scoped, useScope, useScoped, useScopedInteractions, useScopedMachines, useScopedTodos } from "../lib/current-assistant";
import { useTodoBoard } from "../lib/todo-owner-needs";
import { AssistantSwitcher } from "./AssistantSwitcher";
import { sidebarPending } from "./sidebar-pending";
import { usePendingWorkSessions } from "./sidebar-pending-sessions";
import { useMainWaiting } from "./assistant/use-main-waiting";
// Declared in the files split out of this one; re-exported so the files that import them from here stay as they are.
export { useNavPages, type NavPage } from "./SidebarParts";
export { AgentRow, PersonaRow, SessionRow, ShellRow } from "./SidebarRows";
export { SidebarRail } from "./SidebarRail";

// The "herdr isn't running" rows (#1916) draw nothing until a computer's herdr is read as stopped, so their code, its
// polling and their words are off the first screen and load with the sidebar's first render (#14339). Drawn only once
// the chunk is in, never suspending: a Suspense here would hold the sidebar's other updates (a navigation) while it
// loads. A chunk that will not load is reported, and the next mount asks again.
const HerdrNoticesContent = lazyPage(() => import("./agents/HerdrNotices").then((m) => m.HerdrNotices));
const SidebarSpacesLazy = lazy(() => import("./SidebarSpaces").then((m) => ({ default: m.SidebarSpaces })));
export const HerdrNotices = memo(function HerdrNotices() {
  const [ready, setReady] = useState(HerdrNoticesContent.ready);
  useEffect(() => {
    if (ready) return;
    let live = true;
    HerdrNoticesContent.preload().then(
      () => live && setReady(true),
      (error: unknown) => reportBoundary(error, "herdr_notices", isChunkError(error)),
    );
    return () => {
      live = false;
    };
  }, [ready]);
  return ready ? <HerdrNoticesContent /> : null;
});

// Skip parent pushes with unchanged props; each leaf still receives its own state and subscription updates.
const HubNoticeLine = memo(HubNoticeLineContent);
const NodePrompt = memo(NodePromptContent);
const SidebarUsage = memo(SidebarUsageContent);
const LinkTodoDialog = memo(lazyDialog(() => import("./LinkTodoDialog").then((m) => m.LinkTodoDialog)));
const BotmuxModeChip = lazy(() => import("./BotmuxModeChip"));
// Menus and edit controls load on intent; the ordinary sidebar keeps its first-screen budget (#12017).
const ConversationRowMenu = lazy(() => import("./ConversationRowMenu"));
const SidebarConversationArrange = lazy(() => import("./assistant/SidebarConversationArrange"));
const SeenRowsNotice = lazy(() => import("./agents/SeenRowsNotice"));
const ClearSeenButton = lazy(() => import("./agents/SeenRowsNotice").then((m) => ({ default: m.ClearSeenButton })));
const MarkSeenDialog = memo(MarkSeenDialogContent);

/** One-time notice floating over the Agents list's foot once a read row stays (#12585): loaded only when shown. */
function SeenRowsNoticeHost({ shown, onNavigate }: { shown: boolean; onNavigate?: () => void }) {
  const mode = useSeenRowsMode();
  const noticed = useSeenRowsNoticed();
  if (!shown || noticed || mode !== "stay") return null;
  return (
    <div className="pointer-events-none absolute inset-x-2.5 bottom-2 z-30">
      <Suspense fallback={null}><SeenRowsNotice onNavigate={onNavigate} /></Suspense>
    </div>
  );
}

/** Releases held seen rows when the computer filter or assistant scope changes (#12585). */
function useReleaseSeenOnScope(machineKey: string, assistantId: string, onChange: (machineChanged: boolean) => void) {
  const prev = useRef({ machine: machineKey, assistant: assistantId });
  const cb = useRef(onChange);
  cb.current = onChange;
  useEffect(() => {
    const machineChanged = prev.current.machine !== machineKey;
    if (!machineChanged && prev.current.assistant === assistantId) return;
    prev.current = { machine: machineKey, assistant: assistantId };
    cb.current(machineChanged);
  }, [machineKey, assistantId]);
}

/** Brings `el` to the middle of its nearest scrolling ancestor when it is not fully in view (rows loading above it
 *  later still leave it on screen). Not `scrollIntoView`: that also scrolls the clipped (overflow-hidden) box of a
 *  Collapse that is still opening, and it stays scrolled. */
function reveal(el: HTMLElement): boolean {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const o = getComputedStyle(p).overflowY;
    if (o !== "auto" && o !== "scroll") continue;
    const r = el.getBoundingClientRect(),
      b = p.getBoundingClientRect();
    // a row under the pinned Persona row (#5581) is not in view; that row itself always is
    const pinned = p.querySelector<HTMLElement>(":scope > [data-sidebar-sticky]");
    let top = pinned && !pinned.contains(el) ? pinned.getBoundingClientRect().bottom : b.top;
    for (let group = el.closest("[data-sidebar-group]"); group; group = group.parentElement?.closest("[data-sidebar-group]") ?? null) {
      const heading = group.querySelector<HTMLElement>(":scope > [data-sidebar-group-heading]");
      if (heading && !heading.contains(el)) top = Math.max(top, heading.getBoundingClientRect().bottom);
    }
    if (r.top >= top && r.bottom <= b.bottom) return false;
    p.scrollTop += r.top + r.height / 2 - (b.top + b.height / 2);
    return true;
  }
  return false;
}

/** The gliding highlight under a list of rows (Beautiful UI's Sidebar Nav): rows register under their keys, and a
 *  hovered non-active row gets the box; the open row owns CURRENT_ROW, so the box fades out instead of overlapping it
 *  when the pointer leaves or lands on the active row. When `track` (the scroller) is provided, the box mounts inside
 *  it and scrolls natively on the compositor without scroll-listener wobble. */
function useGlide(activeKey: string) {
  const nav = useRef<HTMLDivElement>(null);
  const track = useRef<HTMLDivElement>(null);
  const rows = useRef(new Map<string, HTMLElement>());
  const lastKey = useRef<string | null>(null);
  const following = useRef(false);
  const hovered = useRef<string | null>(null);
  const [box] = useState(glideStore);
  const active = useRef(activeKey);
  active.current = activeKey;
  const sizes = useRef<ResizeObserver | null>(null);
  const measure = useRef(() => {});
  measure.current = () => {
    const host = track.current ?? nav.current;
    const k = hovered.current;
    const el = k && k !== active.current ? rows.current.get(k) : undefined;
    let next: GlideBox | null = null;
    if (host && el?.isConnected && host.contains(el)) {
      const clips: Span[] = [];
      for (let p = el.parentElement; p && p !== host; p = p.parentElement) if (clipsOverflow(getComputedStyle(p).overflowY)) clips.push(p.getBoundingClientRect());
      next = glideBox(el.getBoundingClientRect(), clips, host.getBoundingClientRect(), host.scrollTop);
    }
    // only a real move updates; snap into position when revealing from hidden instead of sliding from a stale row
    box.set((b) => (next ? (b.on && sameBox(b.at, next) ? b : { at: next, on: true, snap: !b.on }) : b.on ? { at: b.at, on: false } : b));
  };
  useEffect(() => {
    const n = nav.current;
    if (!n) return;
    const stop = () => (following.current = false);
    const events = ["wheel", "touchstart", "pointerdown", "keydown"] as const;
    for (const e of events) n.addEventListener(e, stop, { passive: true });
    // layout moves the list doesn't re-render for, measured once per frame however many come in it
    let frame = 0;
    const soon = () => {
      if (!frame) frame = requestAnimationFrame(() => ((frame = 0), measure.current()));
    };
    const ro = new ResizeObserver(soon);
    sizes.current = ro;
    // the list and its blocks (sections) change height as a group inside folds or opens, and as rows come and go;
    // the rows themselves are observed as they register (one growing moves the rest)
    const scroller = track.current;
    const watch = () => {
      ro.observe(n);
      for (const c of n.children) ro.observe(c);
      if (scroller) for (const c of scroller.children) ro.observe(c);
      for (const el of rows.current.values()) ro.observe(el);
    };
    watch();
    const mo = new MutationObserver(() => (ro.disconnect(), watch(), soon()));
    mo.observe(n, { childList: true });
    if (scroller) mo.observe(scroller, { childList: true });
    // a Collapse's transition ending (it may end without a size change, e.g. its opacity)
    n.addEventListener("transitionend", soon);
    return () => {
      events.forEach((e) => n.removeEventListener(e, stop));
      n.removeEventListener("transitionend", soon);
      mo.disconnect();
      ro.disconnect();
      sizes.current = null;
      cancelAnimationFrame(frame);
    };
  }, []);
  useLayoutEffect(() => {
    if (lastKey.current !== activeKey) {
      lastKey.current = activeKey;
      following.current = true;
    }
    const open = following.current ? rows.current.get(activeKey) : undefined;
    // a group that opened for it is still growing, and the list can't scroll past its end yet: again once it has
    if (open?.offsetHeight && reveal(open)) setTimeout(() => following.current && open.isConnected && reveal(open), 350);
    // runs after every render (rows fold, lists change)
    measure.current();
  });
  // keyedRefs keeps a key's entry for the element that holds it (a folded tab's row and its hidden pane row may share a
  // key, #2320); on top, each row's size is watched while it is registered, so a row growing or folding away moves the
  // glide (#2476). The open row drawn after the list's own render (a group opening for it mounts its rows then, a long
  // group draws its part later, WindowedRows) is brought into view the frame after, as the render's would have.
  const [reg] = useState<RegFn>(() => {
    const keyed = keyedRefs(rows.current);
    let later = 0;
    const revealLater = () => {
      later = 0;
      const open = following.current ? rows.current.get(active.current) : undefined;
      if (open?.isConnected && open.offsetHeight && reveal(open)) setTimeout(() => following.current && open.isConnected && reveal(open), 350);
    };
    return (k: string) => {
      const set = keyed(k);
      let mine: HTMLElement | null = null;
      return (el: HTMLElement | null) => {
        if (mine && mine !== el) sizes.current?.unobserve(mine);
        set(el);
        mine = el;
        if (el) sizes.current?.observe(el);
        if (el && k === active.current && following.current && !later) later = requestAnimationFrame(revealLater);
      };
    };
  });
  // one function for the list's life: it moves the box straight away (as a render's layout effect did) and changes
  // nothing the rows or the list render from
  const [hover] = useState(() => (k: string | null) => {
    hovered.current = k;
    measure.current();
  });
  const highlight = <GlideHighlight box={box} />;
  return { nav, track, reg, hover, highlight };
}

/** Where the box is: `at` is the last place it was on show; a box that goes away (`on` false) fades out there;
 *  `snap` suppresses the transform slide when appearing directly from hidden. */
type GlideState = { at: GlideBox | null; on: boolean; snap?: boolean };

/** The box's place outside React state, so moving it re-renders only GlideHighlight. */
function glideStore() {
  let state: GlideState = { at: null, on: false };
  const subs = new Set<() => void>();
  return {
    get: () => state,
    set(f: (b: GlideState) => GlideState) {
      const next = f(state);
      if (next === state) return;
      state = next;
      subs.forEach((s) => s());
    },
    subscribe(s: () => void) {
      subs.add(s);
      return () => void subs.delete(s);
    },
  };
}

function GlideHighlight({ box }: { box: ReturnType<typeof glideStore> }) {
  // box.get is the server snapshot too: the shell views are also rendered to a string (shell-views.test)
  const { at, on, snap } = useSyncExternalStore(box.subscribe, box.get, box.get);
  return <span aria-hidden data-glide className="pointer-events-none absolute rounded-[7px] bg-hover" style={glideStyle(at, on, EASE, snap)} />;
}

/** Recent's Show more / Show less, as the Agents block's Recent (not running) draws its own. */
const MORE_BUTTON = "whitespace-nowrap rounded-[7px] px-2 py-1 text-left text-[12px] text-ink-3 hover:text-ink-2 [@media(pointer:coarse)]:min-h-11";

const NO_CONVERSATIONS: Session[] = [];

/** A folded Persona chats or Recent heading's count of the chats it hides that are Unread (#13425): the Agents list's
 *  folded tally (StateTally), so the mark and its number read as they do there, spoken as chats. Nothing at 0. */
function UnreadTally({ n }: { n: number }) {
  return n > 0 ? <StateTally chats counts={{ needs_you: 0, working: 0, bg_waiting: 0, unread: n }} /> : null;
}

export function Sidebar({ route, onSearch, onNewAgent, onNavigate, workspaceActions, active = true, hideCollapse = false }: { route: Route; onSearch: () => void; onNewAgent: () => void; onNavigate?: () => void; workspaceActions?: WorkspaceTreeActions; active?: boolean; hideCollapse?: boolean }) {
  const bindings = useBindings();
  const searchHint = jumpHint(bindings);
  const assistant = useStore((s) => s.assistant);
  const manager = useShownManager();
  const assistantMachine = assistant?.machine_id;
  const liveChats = useStore((s) => (assistantMachine ? s.sessions[assistantMachine] ?? NO_CONVERSATIONS : NO_CONVERSATIONS));
  const allConversations = useMemo(
    () => sidebarConversations(assistant?.conversations ?? NO_CONVERSATIONS, liveChats, manager),
    [assistant?.conversations, liveChats, manager],
  );
  const unarchived = allConversations.length;
  const { phase, online, total, main: mainConversation, sections, more } = useNavPages();
  const mainWaiting = useMainWaiting(active ? mainConversation : undefined);
  // a phone's drawer has the pages as two rows of icons, so its first screen reaches the Agents (#1996)
  const narrow = useMedia(NARROW);
  const activeKey = useActiveKey(route);
  const workBrowserReady = useStore((s) => s.features.includes("sessions_dimensions"));
  // Persona chats are the main conversation (its row above) and the leaders, "常驻" (#4331): making a chat one and ending
  // it is the menu's 设为常驻 / 取消常驻, on the hub for every device. Each says what it has open, Working and Waiting on
  // you, and an open sidebar follows the hub's `directions` (lib/store.ts). Then "最近", the newest of the user's other
  // chats (#4873): as many as the setting says, more after each Show more (#5146); every chat is under 全部对话.
  // The background supervisor is neither: it is built in, listed with the scheduled tasks' chats under 全部对话 (#5082).
  const leaders = useMemo(
    () => (assistant?.leaders ?? []).filter((id) => id !== assistant?.main && !isBackgroundSupervisor(assistant, id)),
    [assistant],
  );
  const isLeader = (id: string) => leaders.includes(id);
  const conversationById = useMemo(
    () => new Map(allConversations.map((c) => [c.session_id, c])),
    [allConversations],
  );
  const conversationOf = (id: string) => conversationById.get(id);
  // Every direction id belongs in the key: with only conversationless directions there is still a `/leaders` fallback ask.
  const directionNames = useDirectionNames(leaders.map((id) => `${id}\u0000${conversationOf(id)?.title ?? ""}`).join("\u0001"));
  const baseCounts = useLeaderCounts();
  const directions = useMemo(
    () => resolveSidebarDirections({
      leaders,
      main: assistant?.main,
      backgroundSupervisor: assistant?.background_supervisor,
      conversations: allConversations,
      externalOf: directionExternal,
    }),
    // directionNames and baseCounts re-run when direction tree / leaders metadata refreshes directionExternal.
    [leaders, assistant?.main, assistant?.background_supervisor, allConversations, directionNames, baseCounts],
  );
  const recentCount = useRecentCount();
  const recentMore = useRecentMore();
  const ownChats = recentConversations(allConversations, assistant?.main, leaders, assistant?.background_supervisor, assistant?.recent_task_chats === true, assistant?.conversation_order);
  const [arranging, setArranging] = useState(false);
  const recent = ownChats.slice(0, recentCount * (1 + recentMore));
  const recentLeft = ownChats.length - recent.length;
  const recentLess = recentMore > 0 && ownChats.length > recentCount;
  const plans = useFeed((s) => s.plans);
  useEffect(() => { if (active && assistant?.main) void loadFeed(assistant.main); }, [active, assistant?.main, manager]);
  const counts = useMemo(() => {
    const owned = new Set(allConversations.map((c) => c.session_id));
    if (assistant?.main) owned.add(assistant.main);
    return pendingPlanCounts(baseCounts, plans, manager, owned);
  }, [baseCounts, plans, allConversations, assistant?.main, manager]);
  // the main row sits above the pages, so"Persona chats"waits for the assistant (a hub without it has none)
  const chats = !!assistant;
  // a conversation row's ⋯ / right click (iOS: its long press)
  const [convMenu, setConvMenu] = useState<{ s: Session; anchor: HTMLElement; at?: MenuAt; main?: boolean } | null>(null);
  // one function for the sidebar's life: the conversation rows and the main row keep their memo across pushes
  const onConvMenu = useCallback<RowMenuFn>((s, anchor, at) => setConvMenu((m) => (!at && m?.anchor === anchor ? null : { s, anchor, at })), []);
  const onMainMenu = useCallback<RowMenuFn>((s, anchor, at) => setConvMenu((m) => (!at && m?.anchor === anchor ? null : { s, anchor, at, main: true })), []);
  if (convMenu?.main && convMenu.s.session_id !== assistant?.main) setConvMenu(null);
  const [personaFolded, togglePersona] = useFolded("persona", false, { holds: activeKey === "history" || activeKey === "j:new" || directions.some((d) => activeKey === `j:${d.id}`) || recent.some((c) => activeKey === `j:${c.session_id}`) });
  // 最近 folds on its own too (#6228): the choice is kept in this browser, and it opens by itself while it holds the
  // open conversation, so folding never hides where a message lands. The Agents block's Recent uses another key.
  const [recentFolded, toggleRecent] = useFolded("recent-chats", false, { holds: recent.some((c) => activeKey === `j:${c.session_id}`) });
  // Folded, the Persona chats heading and Recent's still say how many of the chats they hide are Unread (#13425), with
  // the folded tally the Agents list's groups use (StateTally); open, each row marks its own. The rows are the store's,
  // which the seen outbox already holds seen (lib/store.ts holdSeen), so a chat just opened is not counted
  // A pinned direction counts through its conversation; an external one (#12557) has no chat row, so no Unread mark.
  const unreadPinned = directions.filter((d) => d.conversation !== undefined && unreadShown(d.conversation)).length;
  const unreadRecent = recent.filter(unreadShown).length;
  const leaderTotal = visibleSectionCounts(counts, directions.map((d) => d.id), recentFolded ? [] : recent.map((c) => c.session_id));
  const { nav, track, reg, hover, highlight } = useGlide(activeKey);
  const go = useGo(onNavigate);
  const [holdsSeen, setHoldsSeen] = useState(false);

  // A conversation-backed lead's row is unchanged. An external one reuses that row, but opens only its actual outside
  // Agent (never a fabricated `/persona/<direction-id>`), has no conversation menu, and is a plain row without a pair.
  const directionName = (d: SidebarDirectionEntry) => directionLabel(directionNames, d.id, d.conversation ? conversationTitle(d.conversation) : t("lib-format.untitled_session"));
  const directionRow = (d: SidebarDirectionEntry) => {
    if (d.conversation) {
      const c = d.conversation;
      return <SessionRow key={c.session_id} s={c} k={`j:${c.session_id}`} reg={reg} hover={hover} active={activeKey === `j:${c.session_id}`} to={conversationPage(c.session_id, c.machine_id, c.manager)} go={go} label={directionName(d)} hint={directionHint(counts.get(c.session_id), t) || undefined} hintFull showState={false} workingMark={counts.get(c.session_id)?.running ?? false} keepTrailing onMenu={onConvMenu} menuLabel={t("conversation-menu.actions")} />;
    }
    const { machine_id: mid, session_id: sid } = d.external ?? {};
    const key = mid && sid ? `s:${mid}/${sid}` : `j:${d.id}`;
    const label = directionName(d);
    const s: Session = {
      machine_id: mid ?? assistant?.machine_id ?? "",
      session_id: sid ?? d.id,
      title: label,
      cwd: "",
      status: "idle",
      state: "idle",
      assistant: !mid || !sid,
    };
    return <SessionRow key={d.id} s={s} k={key} reg={reg} hover={hover} active={activeKey === key} to={mid && sid ? { name: "session", mid, sid } : null} go={go} label={label} hint={directionHint(counts.get(d.id), t) || undefined} hintFull showState={false} workingMark={counts.get(d.id)?.running ?? false} keepTrailing />;
  };

  const recentRow = (c: Session) => (
    <SessionRow key={c.session_id} s={c} k={`j:${c.session_id}`} reg={reg} hover={hover} active={activeKey === `j:${c.session_id}`} to={conversationPage(c.session_id, c.machine_id, c.manager)} go={go} label={conversationTitle(c)} hint={directionHint(counts.get(c.session_id), t) || undefined} hintFull keepTrailing showState={false} onMenu={onConvMenu} menuLabel={t("conversation-menu.actions")} />
  );

  return (
    <aside data-sidebar-root className="flex h-full w-full flex-col bg-canvas">
      <div className="px-2.5 pt-2.5">
        <div className={cx("flex items-center gap-1", hideCollapse ? "mb-1" : "mb-2")}>
          <div className="min-w-0 flex-1"><AssistantSwitcher phase={phase} online={online} total={total} route={route} /></div>
          {/* folds the column to the rail of icons (a wide screen; macOS desktop hosts it in MacBayTools; the drawer closes by its scrim) */}
          {!narrow && !hideCollapse && <CollapseButton collapsed={false} chord={bindings["left-sidebar"]} />}
        </div>
        {!hideCollapse && (
          <button type="button" onClick={onSearch} className="touch-hit mb-1 flex h-8 w-full items-center gap-2 rounded-control bg-inset px-2.5 text-left shadow-hairline transition-colors hover:bg-hover">
            <Icon name="search" size={12} stroke={2} className="text-ink-3" />
            <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-3">{t("sidebar.search_sessions")}</span>
            <span className="hidden sm:inline-flex [@media(hover:none)]:hidden"><Kbd>{searchHint}</Kbd></span>
          </button>
        )}
      </div>

      {/* scroll-pt: a row reached by Tab doesn't stop under the pinned Persona row — about 62px with its 主对话 line
          (4 + 48 + 1 gap + 1 line + 8 margins), 58px on a coarse pointer (44px touch row): 64px covers both */}
      {/* isolate: the rows' own layers (the sticky Persona row and group headers at z-30, chips, segments) stay inside this
          column, so the usage popover that opens upward over it from the bottom is never drawn under them */}
      <div className="relative flex min-h-0 flex-1 flex-col">
      <div ref={nav} data-sidebar-rows onMouseLeave={() => hover(null)} className="relative isolate flex min-h-0 flex-1 flex-col scroll-pt-16 px-2.5">
        {/* the main conversation is the column's first row, Main chat with the home symbol, and only there (#2004, #5109); it
            stays at the top while the rest scrolls (#5581), with a solid divider above the scrollable rows */}
        <div data-sidebar-sticky className="sticky top-0 z-30 flex shrink-0 flex-col gap-px bg-canvas pt-1">
          <PersonaRow reg={reg} hover={hover} active={activeKey === "persona"} go={go} main={assistant?.main ? (conversationOf(assistant.main) ?? mainConversation) : mainConversation} chord={bindings.persona} waiting={mainWaiting.count} onMenu={onMainMenu} />
          <span aria-hidden data-persona-divider className="mx-2 my-1 h-px shrink-0 bg-line" />
        </div>
        <div ref={track} data-row-scroller data-sidebar-scroller className="relative min-h-0 flex-1 -mx-2.5 overflow-y-auto overscroll-contain px-2.5 pb-3">
        {highlight}
        {/* a phone's drawer has Pages as its grid of icons; every other group (#9071) is a section, as on a wide screen */}
        {narrow && <NavGrid showHubNotice={active} pages={sections.find((g) => !g.id)?.pages ?? []} more={more} activeKey={activeKey} go={go} onNavigate={onNavigate} />}
        <Suspense fallback={null}><SidebarSpacesLazy route={route} reg={reg} hover={hover} activeKey={activeKey} go={go} /></Suspense>
        {sections
          .filter((g) => (g.id ? g.pages.length > 0 : !narrow))
          .map((g, i) => <PageGroup showHubNotice={active} key={g.id || "pages"} group={g} more={g.id ? [] : more} first={i === 0 && !narrow} reg={reg} hover={hover} activeKey={activeKey} go={go} />)}

        {chats && (
          <Section
            top
            title={t("sidebar.persona_chats")}
            folded={personaFolded}
            onToggle={togglePersona}
            // New chat lives with the chats it makes (#3231, after #2303 moved it above the main chat): the compose icon at
            // the end of the section's title, as on the phone's sidebar, folded or not
            right={
              <>
                {/* folded, what the leaders have going stays: the working arc while anything under one runs, and their
                    tasks waiting on you (#6626: the arc, never "在做 N" as well) */}
                {(countsLabel(leaderTotal, t) || userCheckLabel(leaderTotal, t) || (personaFolded && leaderTotal.running)) && (
                  <span data-direction-counts data-running={leaderTotal.running || undefined} className="flex min-w-0 items-center gap-1 text-[11px] tabular-nums text-ink-3">
                    {leaderTotal.running && <StatusMark state="working" size="sm" />}
                    {countsLabel(leaderTotal, t) && <span className="truncate font-semibold text-attention">{countsLabel(leaderTotal, t)}</span>}
                    {countsLabel(leaderTotal, t) && userCheckLabel(leaderTotal, t) && <span aria-hidden>·</span>}
                    {userCheckLabel(leaderTotal, t) && <span data-direction-user-check className="truncate">{userCheckLabel(leaderTotal, t)}</span>}
                  </span>
                )}
                {personaFolded && <UnreadTally n={unreadPinned + unreadRecent} />}
                <SectionAdd icon="compose" label={t("sidebar.new_chat")} to={{ name: "persona-new" }} go={go} active={activeKey === "j:new"} />
              </>
            }
          >
            <div className="sidebar-indent">
              <SidebarDirectionsSection directions={directions} nameOf={directionName} renderRow={directionRow} reg={reg} hover={hover} go={go} onNavigate={onNavigate} />
              {arranging ? ownChats.length > 0 && (
                <Suspense fallback={null}>
                  <SidebarConversationArrange rows={ownChats} row={recentRow} onClose={() => setArranging(false)} />
                </Suspense>
              ) : recent.length > 0 && (
                <>
                  <SubHeading label={t("sidebar.recent_chats")} folded={recentFolded} onToggle={toggleRecent} help={t("sidebar.recent_chats_help")} tally={recentFolded && <UnreadTally n={unreadRecent} />} />
                  <Collapse open={!recentFolded} sidebarFold>
                    <div className="sidebar-indent" inert={recentFolded || undefined}>
                      {recent.map(recentRow)}
                      {(recentLeft > 0 || recentLess) && (
                        <div className="relative z-10 flex flex-wrap">
                          {recentLeft > 0 && (
                            <button type="button" onClick={() => setRecentMore(recentMore + 1)} className={MORE_BUTTON}>
                              {t("sidebar.show_more", { count: Math.min(recentCount, recentLeft) })}
                            </button>
                          )}
                          {recentLess && (
                            <button type="button" onClick={() => setRecentMore(0)} className={MORE_BUTTON}>
                              {t("sidebar.show_less")}
                            </button>
                          )}
                        </div>
                      )}
                    </div>
                  </Collapse>
                </>
              )}
              {assistant && (
                <NavRow k="history" reg={reg} hover={hover} active={activeKey === "history"} to={{ name: "history" }} go={go} icon="list"
                  label={t("sidebar.all_chats")}
                  hintCount={unarchived > 0 ? {
                    n: unarchived,
                    label: t("sidebar.unarchived_chat_count", { count: unarchived }),
                  } : undefined} />
              )}
            </div>
          </Section>
        )}

        <PersonalAgents reg={reg} hover={hover} activeKey={activeKey} go={go} />

        {active && (workBrowserReady ? <PendingAgents route={route} reg={reg} hover={hover} go={go} onNewAgent={onNewAgent} onNavigate={onNavigate} />
          : !(narrow && route.name === "agent-list") && <AgentsBlock reg={reg} hover={hover} activeKey={activeKey} go={go} onNewAgent={onNewAgent} onNavigate={onNavigate} workspaceActions={workspaceActions} foldable onHoldsSeen={setHoldsSeen} />)}
        {!workBrowserReady && <NavRow k="terminal" reg={reg} hover={hover} active={activeKey === "terminal"} to={{ name: "terminal" }} go={go} label={t("work-entry.title")} icon={HISTORY_ICON} />}
        </div>
      </div>
      <SeenRowsNoticeHost shown={holdsSeen} onNavigate={onNavigate} />
      </div>
      {convMenu && (() => {
        const activeSession = conversationOf(convMenu.s.session_id) ?? convMenu.s;
        return (
          <Suspense fallback={null}>
            <ConversationRowMenu
              s={activeSession}
              main={activeSession.session_id === assistant?.main}
              leader={isLeader(activeSession.session_id)}
              builtIn={isBackgroundSupervisor(assistant, activeSession.session_id)}
              anchor={convMenu.anchor}
              at={convMenu.at}
              onClose={() => setConvMenu(null)}
              onNavigate={onNavigate}
              onArrange={ownChats.includes(activeSession) ? () => setArranging(true) : undefined}
            />
          </Suspense>
        );
      })()}
      {/* subscription rate-limit windows (#2432): nothing is drawn without data */}
      <SidebarUsage />
      <NodePrompt />
    </aside>
  );
}

function PendingAgents({ route, reg, hover, go, onNewAgent, onNavigate }: {
  route: Route; reg: RegFn; hover: (key: string | null) => void; go: GoFn; onNewAgent: () => void; onNavigate?: () => void;
}) {
  const scope = useScope();
  const { sessions, loaded } = usePendingWorkSessions(scope.several ? scope.id : undefined);
  const interactions = useScopedInteractions();
  const answered = useStore((s) => s.answered);
  const machines = useScopedMachines();
  const rows = useMemo(() => sidebarPending(sessions, interactions, answered), [sessions, interactions, answered]);
  const [folded, toggle] = useFolded("work-pending", false, { all: false });
  return (
    <Section top title={t("work-entry.pending")} folded={folded} onToggle={toggle} right={<AgentsAdd onNewAgent={onNewAgent} onNavigate={onNavigate} />}>
      {loaded && !rows.length && <p className="px-2 py-1.5 text-[12px] text-ink-3">{t("work-entry.pending_empty")}</p>}
      {rows.map((row) => {
        const card = row.cards[0];
        const label = card?.session_title || (row.session && sessionTitle(row.session)) || baseName(card?.cwd) || t("work-entry.pending_request");
        const active = row.to.name === "request" ? route.name === "request" && route.id === row.to.id
          : row.to.name === "session" && showsSession(route, row.to.mid, row.to.sid);
        return <NavRow key={row.key} k={row.key} reg={reg} hover={hover} active={active} to={row.to}
          go={(to) => (e) => { onLinkClick(e, to, card ? cardState(card.id) : null); if (e.defaultPrevented) onNavigate?.(); }}
          label={label} title={[label, machineWithHub(row.machineId, machines)].filter(Boolean).join(" · ")} icon="chat" dot="needs_you"
          badge={row.cards.length > 1 ? row.cards.length : undefined} badgeTone="attention" />;
      })}
      <NavRow k="work:all" reg={reg} hover={hover} active={false} to={{ name: "terminal" }} go={go} label={t("work-entry.all")} icon={HISTORY_ICON} />
    </Section>
  );
}

/** The phone's "Agents" page (#1996): the sidebar's Agents block, the same components, filling the page. */
export function AgentsList({ route, onNewAgent, workspaceActions }: { route: Route; onNewAgent: () => void; workspaceActions?: WorkspaceTreeActions }) {
  const activeKey = useActiveKey(route);
  const { nav, reg, hover, highlight } = useGlide(activeKey);
  const go = useGo();
  // no computer and nothing to list (docs/assistant/onboarding.md 4.5): the machines page's empty state, not a bare title
  const bare = useStore((s) => s.loaded && s.machines.length === 0 && Object.keys(s.launches).length === 0);
  const none = useAgentSessions().length === 0 && bare;
  const adding = useAddComputer();
  // Back from an Agent it opened lands where the list was; a fresh visit starts at the top
  const scroller = useRef<HTMLDivElement>(null);
  useScrollMemory(scroller, "/agents");
  const [holdsSeen, setHoldsSeen] = useState(false);
  return (
    <div className="relative flex h-full flex-col">
      <div ref={scroller} data-row-scroller className="min-h-0 flex-1 overflow-y-auto overscroll-contain">
        <div ref={nav} onMouseLeave={() => hover(null)} className="relative mx-auto max-w-[640px] px-2.5 pb-6">
          {highlight}
          {none ? <NoComputers body={t("agentspage.no_computers_body")} onAdd={adding.open} /> : <AgentsBlock reg={reg} hover={hover} activeKey={activeKey} go={go} onNewAgent={onNewAgent} workspaceActions={workspaceActions} add={false} onHoldsSeen={setHoldsSeen} />}
          <NavRow k="terminal" reg={reg} hover={hover} active={activeKey === "terminal"} to={{ name: "terminal" }} go={go} label={t("work-entry.title")} icon={HISTORY_ICON} />
        </div>
        {adding.dialog}
      </div>
      <SeenRowsNoticeHost shown={holdsSeen} />
    </div>
  );
}

/** A group of the sidebar's pages (Pages, or a group #9071 brought: an extension's or the user's own), folding under
 *  its own title (#2524), each fold kept apart; Pages ends with More. */
function PageGroup({ showHubNotice, group, more, first, reg, hover, activeKey, go }: { showHubNotice: boolean; group: PageSection<NavPage>; more: NavPage[]; first: boolean; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const [folded, toggle] = useFolded(group.id ? `pages:${group.id}` : "pages", false, { all: false, holds: more.some((p) => p.k === activeKey) });
  return (
    <Section top title={group.title ?? t("sidebar.pages")} folded={folded} onToggle={toggle} right={folded ? <PagesTally pages={[...group.pages, ...more]} /> : undefined} first={first}>
      {group.pages.map((p) => (
        <Fragment key={p.k}>
          <NavRow {...p} reg={reg} hover={hover} active={activeKey === p.k} go={go} />
          {showHubNotice && p.k === "settings" && <HubNoticeLine />}
        </Fragment>
      ))}
      {more.length > 0 && <MorePages more={more} reg={reg} hover={hover} activeKey={activeKey} go={go} />}
    </Section>
  );
}

/** How many columns the phone's page grid has for `n` pages: two even rows (8 → 4 + 4), at most 5 to a row. */
export function navGridColumns(n: number): number {
  return Math.max(1, Math.min(5, Math.ceil(n / 2)));
}

/** The pages as two rows of icons in a phone's drawer: one tap away like the list, a third of its height. */
export function NavGrid({ showHubNotice, pages, more = [], activeKey, go, onNavigate }: { showHubNotice?: boolean; pages: NavPage[]; more?: NavPage[]; activeKey: string; go: GoFn; onNavigate?: () => void }) {
  const [menu, setMenu] = useState<HTMLElement | null>(null);
  const moreActive = more.some((p) => p.k === activeKey);
  const moreBadge = more.find((p) => p.badge !== undefined);
  const columns = navGridColumns(pages.length + (more.length ? 1 : 0));
  const settingsIndex = pages.findIndex((p) => p.k === "settings");
  return (
    <nav aria-label={t("sidebar.pages")} className="mb-1 grid gap-0.5" style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>
      {pages.map((p) => {
        const active = activeKey === p.k;
        return (
          <a
            key={p.k}
            href={href(p.to)}
            onClick={go(p.to)}
            title={p.title ?? p.label}
            aria-label={pageLabel(p)}
            aria-current={active ? "page" : undefined}
            className={cx("relative z-10 flex min-h-12 min-w-0 flex-col items-center justify-center gap-1 rounded-[9px] py-1.5 transition-[background-color,transform] duration-150 active:scale-[0.96]", p.k !== "settings" && "px-0.5", active ? "bg-hover" : "hover:bg-hover")}
          >
            <span className={cx("relative", active ? "text-ink" : "text-ink-3")} style={pageIconStyle(p.color, active)}>
              <Icon name={p.icon} size={17} stroke={1.8} />
              {p.notice && <NoticeDot className="absolute -top-0.5 -right-1 ring-2 ring-canvas" />}
              {p.badge !== undefined && (
                <span className={cx("absolute -top-1.5 left-2.5 flex h-4 min-w-4 items-center justify-center whitespace-nowrap rounded-full px-1 text-[10px] font-semibold tabular-nums", BADGE_CLASS[p.badgeTone ?? "accent"])}>{p.badge}</span>
              )}
            </span>
            <span className={cx("w-full truncate text-center text-[11px] leading-tight", active ? "font-medium text-ink" : "text-ink-2")}>{p.short ?? p.label}</span>
          </a>
        );
      })}
      {more.length > 0 && (
        <button
          type="button"
          onClick={(e) => setMenu(menu ? null : e.currentTarget)}
          aria-haspopup="menu"
          aria-expanded={!!menu}
          aria-label={t("sidebar.more_pages_count", { count: more.length })}
          className={cx("relative z-10 flex min-h-12 min-w-0 flex-col items-center justify-center gap-1 rounded-[9px] px-0.5 py-1.5 transition-[background-color,transform] duration-150 active:scale-[0.96]", moreActive || menu ? "bg-hover" : "hover:bg-hover")}
        >
          <span className={cx("relative", moreActive ? "text-ink" : "text-ink-3")}>
            <Icon name="more" size={17} stroke={1.8} />
            {moreBadge && <span aria-hidden className={cx("absolute -top-0.5 -right-1 size-2 rounded-full ring-2 ring-canvas", DOT_CLASS[moreBadge.badgeTone ?? "accent"])} />}
          </span>
          <span className={cx("w-full truncate text-center text-[11px] leading-tight", moreActive ? "font-medium text-ink" : "text-ink-2")}>{t("sidebar.more_pages")}</span>
        </button>
      )}
      {showHubNotice && settingsIndex >= 0 && (
        <div className="col-span-full empty:hidden" style={{ gridRowStart: Math.floor(settingsIndex / columns) + 2 }}>
          <HubNoticeLine onNavigate={onNavigate} />
        </div>
      )}
      {menu && <Menu anchor={menu} onClose={() => setMenu(null)} align="right" width={220} header={t("sidebar.more_pages")} items={morePagesMenu(more, activeKey, onNavigate)} />}
    </nav>
  );
}

/**
 * "More" at the end of the pages (#5026): the ones put away in Settings › General › Sidebar, and the extensions' pages past
 * the list's share of them. Folded by default; it opens by itself while one of its pages is open, and a count on one
 * of them shows as a dot on the row, so nothing waiting there goes unseen.
 */
function MorePages({ more, reg, hover, activeKey, go }: { more: NavPage[]; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn }) {
  const [folded, toggle] = useFolded("pages-more", true, { all: false, holds: more.some((p) => p.k === activeKey) });
  const counted = more.filter((p) => p.badge !== undefined);
  const moreLabel = t("sidebar.more_pages_count", { count: more.length });
  const countedTitle = counted.map(pageLabel).join(t("sidebar.list_separator"));
  return (
    <>
      <button
        type="button"
        onClick={toggle}
        aria-expanded={!folded}
        aria-label={moreLabel}
        title={countedTitle || moreLabel}
        className="touch-hit relative z-10 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row text-left transition-[background-color,transform] duration-150 hover:bg-hover active:scale-[0.98] [@media(pointer:coarse)]:min-h-11"
      >
        <span className="text-ink-3"><Icon name="more" size={14} stroke={1.8} /></span>
        <span className="min-w-0 flex-1 truncate text-[13px] text-ink-2">{t("sidebar.more_pages")}</span>
        {folded && counted.length > 0 && <span aria-hidden className={cx("size-1.5 rounded-full", DOT_CLASS[counted[0].badgeTone ?? "accent"])} />}
        <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 text-ink-3 transition-transform" style={{ ...sidebarFoldMotion(!folded), transform: folded ? "rotate(-90deg)" : "none" }} />
      </button>
      <Collapse open={!folded} sidebarFold>
        <div className="flex flex-col gap-px sidebar-indent" inert={folded || undefined}>
          {more.map((p) => <NavRow key={p.k} {...p} reg={reg} hover={hover} active={activeKey === p.k} go={go} />)}
        </div>
      </Collapse>
    </>
  );
}

/** The sidebar's "Agents": grouping, the quick filter, "Pinned", "Starting", the groups (spaces, machines, repositories,
 *  states or todos) and "Recent". The drawer and the phone's Agents page both show it. `add`: the title row's "+". */
/** What Settings › Agents turned on and what the Filter holds, read once by AgentsBlock for whichever list it draws. */
type AgentsDisplayProps = { showFilter: boolean; dimFilters: AgentDimensionFilters; chatList: boolean; /** the hub's display settings, for the ⋯ menu's switches (null: an older hub, or not read yet) */ displayValue: AgentsDisplay | null };
type AgentsBlockProps = { workspaceActions?: WorkspaceTreeActions; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn; onNewAgent: () => void; onNavigate?: () => void; add?: boolean; foldable?: boolean; chooseGrouping?: (g: AgentGrouping) => void; onHoldsSeen?: (held: boolean) => void };

/** The to-dos could not be read and none are known (audit item 27): By to-do has nothing to group by, so it shows By
 *  status for now, saying why with Retry (TodosUnread); a later read that answers brings it back. */
const useTodosUnread = () => useStore((s) => s.todosError !== null && s.todos.length === 0);

/** A failed read of the Agents list, the complete one's or the paged one's (audit item 4): the same words either way. */
const agentsReadFailed = (error: string) => t("sidebar.agents_read_failed", { error });

/** By to-do while its to-dos cannot be read: why the list is by status instead, and Retry. */
function TodosUnread() {
  const error = useStore((s) => s.todosError) ?? "";
  return <ListError text={t("sidebar.todos_read_failed", { error })} onRetry={() => void refreshTodos()} />;
}

function AgentsBlock(props: AgentsBlockProps) {
  const bounded = useStore((s) => supportsPagedAgents(s.features));
  const byTodo = useStore((s) => s.features.includes(SESSIONS_TODO_FEATURE));
  const todosSupported = useStore((s) => s.todosSupported);
  const todosUnread = useTodosUnread();
  // never reached since the page opened (the page's Offline, the same flag): the complete list's block says so (the
  // paged one would only load), whatever features an earlier login left (audit item 36)
  const hubUnreachable = useHubUnreachable();
  const pins = useStore((s) => s.pins);
  // The Filter is a Settings switch (hub-synced, default off): off, it is not drawn and what it held is not applied
  const showFilter = useShowFilter();
  const displayValue = useAgentsDisplay().value;
  const displayLoaded = useAgentsDisplay().value !== null;
  const storedFilters = useAgentListFilters();
  const dimFilters = showFilter ? storedFilters : DEFAULT_DIMENSION_FILTERS;
  const chatList = useChatList() && showFilter;
  useEffect(() => { if (displayLoaded && !showFilter && storedFilters !== DEFAULT_DIMENSION_FILTERS) resetAgentListFilters(); }, [displayLoaded, showFilter, storedFilters]);
  const [stored, setGrouping] = useState(() => storedValue("persona.agentGrouping"));
  useEffect(() => onGrouping(setGrouping), []);
  const grouping = agentGrouping(stored);
  // a hub without todos shows by machine, a by-todo choice kept for when it has them; to-dos that cannot be read, by
  // status until they can
  const shown = todosSupported === false && grouping === "todo" ? "machine" : todosUnread && grouping === "todo" ? "state" : grouping;
  // several assistants: the list is the current one's (docs/assistant/managers.md "Clients"), which the hub's paged list
  // cannot ask for, so the complete list is read and filtered here
  const several = useScope().several;
  // One set of groupings either way (#6662): the paged list draws them all but By repository, whose worktree level needs
  // every session, and By to-do only on a hub that lists sessions by to-do; pins need the complete list too.
  // A filter or view the hub's paged list cannot ask for (model, pinned, search scope, several computers, the chat list…)
  // is answered by the complete list, which has every one of them: never a filter that half works
  const typedViews = useStore(hasAgentViews);
  const paged = shown !== "state" && !(typedViews && showFilter) && pagedAgentList({ bounded, byTodo, todosSupported, pins, stored, several }) && !chatList && pagedFilterOf(dimFilters) !== null;
  const display: AgentsDisplayProps = { showFilter, dimFilters, chatList, displayValue };
  const chooseGrouping = useCallback((g: AgentGrouping) => {
    storeValue("persona.agentGrouping", g);
    setGrouping(g);
    // By to-do while its to-dos cannot be read (shown by status): the tap reads them again, as the note's Retry does
    const { todosError, todos } = getState();
    if (g === "todo" && todosError !== null && todos.length === 0) void refreshTodos();
    props.hover(null);
  }, [props.hover]);
  // `shown !== "repo"` only tells TypeScript what pagedAgentList already rules out
  return paged && shown !== "repo" && !hubUnreachable ? <BoundedAgentsBlock {...props} {...display} grouping={shown} todosUnread={todosUnread && grouping === "todo"} chooseGrouping={chooseGrouping} />
    : <LegacyAgentsBlock {...props} {...display} grouping={grouping} hubUnreachable={hubUnreachable} chooseGrouping={chooseGrouping} />;
}

/** The Agents block's title, the paged list's as the complete list's (#6662): unpinning changes nothing above the list. */
const AGENTS_TITLE = "min-w-0 flex-1 text-[11px] font-semibold text-ink-3";
/** The title row's icon buttons (Filter, ⋯, +) sit 4 px apart: a touch screen gives each a real 44 px box, as the fold-all
 *  button has, since centred touch-hit squares would overlap and the later button would take the earlier one's taps. */
const TITLE_ICON_TOUCH = "[@media(pointer:coarse)]:size-11";
/** And the row drops its end padding there, so its last box ends where the rows' ⋯ boxes do, its icon in their column. */
const TITLE_ROW_TOUCH = "[@media(pointer:coarse)]:pr-0";

/** The Agents title row's ⋯ (Agent list options): the archived Agents. Ordering is stable, not an activity choice (#11801). */
function ListMenuButton({ open, onClick }: { open: boolean; onClick: React.MouseEventHandler<HTMLButtonElement> }) {
  const label = t("sidebar.list_menu");
  return (
    <button type="button" title={label} aria-label={label} aria-haspopup="menu" aria-expanded={open} onClick={onClick} className={cx("touch-hit relative z-10 flex size-5 shrink-0 items-center justify-center rounded-[5px] text-ink-3 hover:bg-hover hover:text-ink-2", TITLE_ICON_TOUCH, open && "bg-hover")}>
      <Icon name="more" size={12} stroke={2} />
    </button>
  );
}

/** What the ⋯ switches show: the hub's answer (null when unread or an older hub) and the two effective values. */
type ListDisplay = { value: AgentsDisplay | null; sort: boolean; filter: boolean };
/** Local-only By repo switch (#14607): off by default, persisted in this browser. */
type EmptyWorkspacesOption = { show: boolean; onToggle: () => void };

/** The ⋯'s items, the paged list's as the complete list's (#6662). Collapse all / Expand all are the title row's button
 *  alone (#6447). The two display choices (#13727) are switches here, writing the same hub setting Settings › Agents
 *  holds, so a click shows on every device and that page follows; an older hub has neither field and gets no item. */
function agentListItems({ archives, hover, onNavigate, display, emptyWorkspaces }: { archives: boolean; hover: (k: string | null) => void; onNavigate?: () => void; display: ListDisplay; emptyWorkspaces?: EmptyWorkspacesOption }): MenuItem[] {
  const set = (change: DisplayChange) => { void saveAgentsDisplay(change); hover(null); };
  const sortable = display.value?.sort_by_activity !== undefined;
  const filterable = display.value?.show_filter !== undefined;
  const hasDisplay = sortable || filterable || Boolean(emptyWorkspaces);
  return [
    ...(sortable ? [{ key: "sort-activity", label: t("sidebar.sort_activity"), icon: "clock" as const, checked: display.sort, keepOpen: true, divider: true, onSelect: () => set({ sort_by_activity: !display.sort }) }] : []),
    ...(filterable ? [{ key: "show-filter", label: t("sidebar.show_list_filter"), icon: "filter" as const, checked: display.filter, keepOpen: true, divider: !sortable, onSelect: () => set({ show_filter: !display.filter }) }] : []),
    ...(emptyWorkspaces ? [{ key: "show-empty-workspaces", label: t("sidebar.show_empty_workspaces"), icon: "folder" as const, checked: emptyWorkspaces.show, keepOpen: true, divider: !sortable && !filterable, onSelect: () => { emptyWorkspaces.onToggle(); hover(null); } }] : []),
    ...(archives ? [{ key: "archived", label: t("agentmenus.archived_agents"), icon: "archive" as const, divider: hasDisplay, onSelect: () => { navigate({ name: "agent-archives" }); onNavigate?.(); hover(null); } }] : []),
  ];
}

/** The groupings this hub offers: By to-do only where it has to-dos. */
function useGroupings() {
  const todosSupported = useStore((s) => s.todosSupported);
  return useMemo(() => AGENT_GROUPINGS.filter((g) => g.key !== "todo" || todosSupported !== false), [todosSupported]);
}

/** What each quick filter selects on the paged list (`sessions_quick_filters`, #6662), as the complete list matches them
 *  (lib/agent-filter.ts): Working without the ones that only wait in the background, which have their own chip. */
const QUICK_SELECT: Record<AgentFilter, ListFilters> = {
  all: {}, needs: { state: "needs_you" }, unread: { state: "unread" }, working: { state: "working", waiting: "0" },
  bg_waiting: { state: "working", waiting: "1" }, feishu: { pane_source: "feishu" }, terminal: { pane_source: "terminal" },
};
/** The botmux sessions hidden: every read leaves out their origin (a botmux session is a Lark group's). */
const HIDE_BOTMUX: ListFilters = { exclude_pane_source: "feishu" };
const SHOW_ALL: ListFilters = {};
const NO_COUNTS: FilterCounts = { needs: 0, unread: 0, working: 0, bg_waiting: 0, feishu: 0, terminal: 0 };

/** The paged list's section kind for each grouping it draws (#6662): By project is the space (`group`). */
const PAGED_DIMENSION: Record<Exclude<AgentGrouping, "repo">, GroupDimension> = { todo: "todo", machine: "machine", space: "group", state: "state" };

const NO_EXCLUDED_IDS: ReadonlySet<string> = new Set();


/** The Filter's options on the paged list: the loaded sessions' harnesses, projects and models (the hub counts none of them)
 *  and the hub's own counts of the computers. Only drawn while the Filter is on, so the list does not follow every push
 *  for it otherwise. */
function usePagedFacets(machines: readonly Machine[]): AgentFacetCounts {
  const summaryAgents = useAgentSessions();
  const pins = useStore((s) => s.pins);
  const botmuxHidden = useBotmuxHidden();
  const rootCounts = useSyncExternalStore(runtimeAgents.subscribe, runtimeAgents.getSnapshot, runtimeAgents.getSnapshot).counts;
  return useMemo(() => {
    const loaded = buildAgentFacets(withoutBotmux(summaryAgents.filter(isSidebarAgent), botmuxHidden), machines, pins);
    const counted = Object.entries(rootCounts?.machine ?? {}).filter(([id, n]) => id && n > 0);
    if (!counted.length) return loaded;
    const options = counted.map(([id, count]) => ({ id, count, label: machineWithHub(id, [...machines]) || id }));
    return { ...loaded, machines: options.sort((a, b) => b.count - a.count || a.label.localeCompare(b.label)) };
  }, [summaryAgents, botmuxHidden, machines, pins, rootCounts]);
}

function PagedFilterButton({ filters, machines, chatList, onToggleChatList }: { filters: AgentDimensionFilters; machines: readonly Machine[]; chatList: boolean; onToggleChatList: () => void }) {
  const facets = usePagedFacets(machines);
  return <AgentFilterButton filters={filters} onChange={setAgentListFilters} facets={facets} chatList={chatList} onToggleChatList={onToggleChatList} />;
}

function PagedFilterChips({ filters, machines }: { filters: AgentDimensionFilters; machines: readonly Machine[] }) {
  const facets = usePagedFacets(machines);
  return <AgentFilterChips filters={filters} onChange={setAgentListFilters} facets={facets} />;
}

function BoundedAgentsBlock({ reg, hover, activeKey, go, onNewAgent, onNavigate, add = true, foldable = false, grouping, todosUnread = false, chooseGrouping, showFilter, dimFilters, chatList, displayValue, onHoldsSeen }: AgentsBlockProps & AgentsDisplayProps & { grouping: Exclude<AgentGrouping, "repo">; /** By to-do shown by status: its to-dos cannot be read */ todosUnread?: boolean; chooseGrouping: (g: AgentGrouping) => void }) {
  const [blockFolded, toggleBlock] = useFolded("agents", false, { all: false, holds: dimFilters.query.trim() !== "" });
  const folded = foldable && blockFolded;
  const machines = useStore(selectSidebarMachines);
  const mutes = useStore((s) => s.mutes);
  const scope = useScope();
  const seenMode = useSeenRowsMode();
  const visit = useSeenVisit();
  const heldOrder = useMemo(() => heldOrderFor(seenMode, visit), [seenMode, visit]);
  const rootAgents = useSyncExternalStore(runtimeAgents.subscribe, runtimeAgents.getSnapshot, runtimeAgents.getSnapshot);
  const rootRows = rootAgents.rows;
  useEffect(() => {
    if (!folded && seenMode === "stay" && rootRows.length) noteShownUnseen(needsSeenOf(rootRows, mutes), rootRows);
  }, [folded, seenMode, rootRows, mutes]);
  const allLaunches = useStore((s) => s.launches);
  const launches = useMemo(() => Object.values(allLaunches).filter((launch) => launch.phase !== "ready"), [allLaunches]);
  // Keep actively starting/sending launches out of the paged list and its groups until handoff completes so a newly
  // created session does not render in both Starting and its group at once.
  const inFlightLaunchIds = useMemo(() => {
    let ids: Set<string> | undefined;
    for (const launch of launches) {
      if (isInFlightLaunch(launch) && launch.session) {
        ids ??= new Set();
        ids.add(runtimeIdentity(launch.session));
      }
    }
    return ids ?? NO_EXCLUDED_IDS;
  }, [launches]);
  const [launchesFolded, toggleLaunches] = useFolded("agents-launching", false, { holds: launches.some((l) => activeKey === `s:${l.machineId}/${l.id}`), scope: AGENTS_FOLD });
  const catalog = useStore((s) => s.features.includes(SESSIONS_CATALOG_FEATURE));
  const justFinished = useStore((s) => s.features.includes(SESSIONS_JUST_FINISHED_FEATURE));
  // a hub that folds side sessions (#7944) leaves them out of the list: nothing to nest under a parent
  const sideFold = useStore((s) => s.features.includes(SESSIONS_SIDE_FOLD_FEATURE));
  const archives = useStore((s) => s.features.includes(AGENT_ARCHIVES_FEATURE));
  const [rowMenu, setRowMenu] = useState<{ s: Session; anchor: HTMLElement; at?: MenuAt } | null>(null);
  const [renaming, setRenaming] = useState<Session | null>(null);
  const [deleting, setDeleting] = useState<Session | null>(null);
  const [linkTarget, setLinkTarget] = useState<Session>();
  const closeLinkTarget = useCallback(() => setLinkTarget(undefined), []);
  // the ⋯ toggles the menu, as in the full list
  const onRowMenu = useCallback<RowMenuFn>((s, anchor, at) => setRowMenu((m) => (!at && m?.anchor === anchor ? null : { s, anchor, at })), []);
  // The complete list's grouping bar (#6662): the same segments, so a pin or an unpin changes nothing above the list.
  // By repository takes the complete list (AgentsBlock).
  const groupings = useGroupings();
  // by to-do: the open to-dos' titles and the To-dos page's order; the hub counts their sessions by id
  const todos = useScopedTodos();
  const todoNames = useMemo((): TodoNames => {
    const open = sortTodos(todos.filter((td) => !td.done_at));
    const titles = new Map(open.map((td) => [td.id, td.title]));
    return { order: open.map((td) => td.id), title: (id) => titles.get(id) };
  }, [todos]);
  const quickFilters = useStore((s) => s.features.includes(SESSIONS_QUICK_FILTERS_FEATURE));
  const botmuxHidden = useBotmuxHidden();
  // an agent opened here is seen at once in the chips' numbers and By state's sections too, not only on its row, while
  // the list's last read still counts it Unread (lib/seen-outbox.ts heldCounts)
  const seenMarks = useStore((s) => s.seen);
  // and a parent whose unread side sessions were all seen here (the side rows held for its row, lib/use-child-sessions.ts)
  const heldAt = useHeldVersion();
  // eslint-disable-next-line react-hooks/exhaustive-deps -- heldAt: heldSidesOf reads the held rows, which it versions
  const holdCounts = useCallback((counts: ListCounts, rows: readonly Session[]) => heldCounts(counts, rows, seenMarks, Date.now(), heldSidesOf), [seenMarks, heldAt]);
  const [savedFilter, setFilter] = useState(() => agentFilter(storedValue("persona.agentFilter")));
  const [larkCount, setLarkCount] = useState<number>();
  const reportCounts = useCallback((counts: FilterCounts | undefined) => setLarkCount(counts?.feishu), []);
  const filter = (botmuxHidden || larkCount === 0) && savedFilter === "feishu" ? "all" : savedFilter;
  const chooseFilter = useCallback((f: AgentFilter) => {
    storeValue("persona.agentFilter", f);
    setFilter(f);
    releaseAllSeen(needsSeenOf(runtimeAgents.getSnapshot().rows, getState().mutes));
    hover(null);
  }, [hover]);
  useReleaseSeenOnScope(dimFilters.machines.join("\u0000"), scope.id, () => {
    releaseAllSeen(needsSeenOf(runtimeAgents.getSnapshot().rows, getState().mutes));
  });
  const quick = useMemo((): PagedQuickFilters | undefined => quickFilters || botmuxHidden ? {
    select: quickFilters ? QUICK_SELECT[filter] : SHOW_ALL,
    exclude: botmuxHidden ? HIDE_BOTMUX : SHOW_ALL,
    onCounts: reportCounts,
    bar: (counts) => quickFilters ? <FilterBar selection={filter} counts={counts ?? NO_COUNTS} onChoose={chooseFilter} /> : null,
  } : undefined, [quickFilters, filter, botmuxHidden, chooseFilter, reportCounts]);
  const groupsRef = useRef<HTMLDivElement>(null);
  // the words ⌘K's "Filter Agents list" put on the list, its search (#6589): only its words, which the hub matches as
  // text; the Filter menu's choices are the complete list's, so a chip shows here only what this list is narrowed by
  // the Filter's choices (and ⌘K's words) that the hub's paged read can express: the words as `q`, one computer as
  // `machine_id`; AgentsBlock draws the complete list for any other
  const listQuery = dimFilters.query;
  const narrowed = useMemo(() => pagedFilterOf(dimFilters) ?? {}, [dimFilters]);
  // The activity order (Settings › Agents), held still while the pointer or the keyboard is in the sidebar; off, the
  // hub pages newest first for its cursors but the sidebar presents the same stable herdr order as the complete list.
  const sortByActivity = useSortByActivity();
  const engaged = useSidebarEngaged(groupsRef);
  const byUpdate = sortByActivity && grouping !== "state";
  const listMenu = useMenu();
  const machineOrder = useMemo(() => machines.map((m) => m.id), [machines]);
  const rowOrder = useMemo(() => machineSessionPresentation(machines), [machines]);
  const rowNeedsYou = useCallback((s: Session) => s.view ? s.view.counted === true : runtimeNeedsYou(s) && !rowCards(getState().interactions, s.machine_id, s.session_id).heldOnly, []);
  const listItems = agentListItems({ archives, hover, onNavigate, display: { value: displayValue, sort: sortByActivity, filter: showFilter } });
  const pagedHeldRows = useMemo(() => {
    if (listQuery.trim()) return [];
    return Object.values(visit.snapshots).filter((s) =>
      isHeldSeen(s, mutes, heldOrder)
      && (!botmuxHidden || !isBotmux(s))
      && (!dimFilters.machines.length || dimFilters.machines.includes(s.machine_id))
      && !inFlightLaunchIds.has(runtimeIdentity(s))
      && !isMuted(s, mutes),
    );
  }, [visit.snapshots, mutes, heldOrder, botmuxHidden, dimFilters.machines, inFlightLaunchIds, listQuery]);
  const pagedHoldsSeen = seenMode === "stay" && pagedHeldRows.length > 0 && !listQuery.trim();
  useEffect(() => { onHoldsSeen?.(pagedHoldsSeen); }, [onHoldsSeen, pagedHoldsSeen]);
  useEffect(() => () => onHoldsSeen?.(false), [onHoldsSeen]);
  const onPagedSectionRows = useCallback((rows: readonly Session[]) => {
    const currentMutes = getState().mutes;
    noteShownUnseen(needsSeenOf(rows, currentMutes), rows);
    holdNotedSeen(rows);
  }, []);
  const pagedHeld = useMemo((): HeldSeenGroups<Session> => ({
    rows: pagedHeldRows,
    stay: seenMode === "stay",
    sectionOf: (s) => {
      const place = heldPlace(s, heldOrder);
      return place === "just_finished" ? JUST_FINISHED_KEY : place;
    },
    onClear: (rows) => { releaseSeen(rows.map(agentKey)); hover(null); },
    onRows: onPagedSectionRows,
  }), [pagedHeldRows, seenMode, heldOrder, hover, onPagedSectionRows]);
  const pagedGrouping = useMemo(() => ({
    dimension: PAGED_DIMENSION[grouping],
    label: AGENT_GROUPINGS.find((g) => g.key === grouping)?.label ?? "",
    todos: todoNames,
    quick,
    // its sections follow the title row's fold-all, as the complete list's groups do
    foldScope: AGENTS_FOLD,
    rowOrder: byUpdate ? undefined : rowOrder,
    byUpdate,
    hold: engaged,
    filters: narrowed,
    bar: <GroupingBar groupings={groupings} selection={grouping} onChoose={chooseGrouping} />,
    query: listQuery,
    // the to-dos' failure under the chips, lined up with the list's own errors below (the chips' row is padded)
    chips: <>{showFilter && <PagedFilterChips filters={dimFilters} machines={machines} />}{todosUnread && <div className="-mx-1"><TodosUnread /></div>}</>,
    heldSeen: pagedHeld,
  }), [grouping, todoNames, groupings, chooseGrouping, quick, rowOrder, byUpdate, engaged, narrowed, listQuery, showFilter, dimFilters, machines, todosUnread, pagedHeld]);
  const trimmedQuery = listQuery.trim();
  const quickSelect = quick?.select ?? SHOW_ALL;
  const quickExclude = quick?.exclude ?? SHOW_ALL;
  const tallyFilters = useMemo((): ListFilters => ({
    ...(trimmedQuery ? { q: trimmedQuery } : {}),
    ...quickSelect,
    ...narrowed,
    ...quickExclude,
  }), [trimmedQuery, quickSelect, narrowed, quickExclude]);
  const tallyScoped = Object.keys(tallyFilters).length > 0;
  const rootsMode = runtimeAgents.isRoots();
  const crossMode = runtimeAgents.isCrossMachineHierarchy();
  const tallyPath = `${RUNTIME_AGENTS_PATH}&${rootsMode ? `roots=1&${crossMode ? "cross_machine_hierarchy=1&" : ""}` : ""}${filterQuery(tallyFilters)}`;
  const [scopedTally, setScopedTally] = useState<{ path: string; counts: ListCounts } | null>(null);
  useEffect(() => {
    if (!folded || !tallyScoped) return;
    return followHinted<ListCounts>("sessions_list", tallyPath, (c) => {
      if (c?.state) setScopedTally({ path: tallyPath, counts: c });
    }, () => {});
  }, [folded, tallyScoped, tallyPath]);
  const tallyCounts = useMemo((): FoldedCounts => {
    const effective = tallyScoped
      ? (scopedTally?.path === tallyPath ? scopedTally.counts : null)
      : (rootAgents.counts && runtimeAgents.usesPageCounts() ? holdCounts(rootAgents.counts, rootRows) : rootAgents.counts);
    const base = effective ? boundedCounts(effective, false) : { needs: 0, working: 0, waiting: 0, unread: 0 };
    const terms = trimmedQuery ? trimmedQuery.toLowerCase().split(/\s+/) : null;
    const pendingLaunches = (!quickSelect.state || quickSelect.state === "working") && quickSelect.waiting !== "1" && !quickSelect.pane_source
      ? launches.filter((l) =>
        isInFlightLaunch(l)
        && !l.session
        && (!dimFilters.machines.length || dimFilters.machines.includes(l.machineId))
        && (!terms || terms.every((term) => [l.text, l.nameHint, l.cwd, l.launcher, l.displayLauncher, l.displayEngine, l.model, l.displayModel, l.machineId, machines.find((m) => m.id === l.machineId)?.name].join("\n").toLowerCase().includes(term))),
      ).length
      : 0;
    return {
      needs_you: base.needs,
      working: base.working + pendingLaunches,
      bg_waiting: base.waiting,
      unread: base.unread,
    };
  }, [tallyScoped, scopedTally, tallyPath, rootAgents.counts, rootRows, holdCounts, quickSelect, launches, dimFilters.machines, trimmedQuery, machines]);
  return <div data-sidebar-agents ref={groupsRef}>
    {/* the complete list's title row (#6662): fold all, the ⋯ with its sorts and the archived Agents, and "+" */}
    <div className={cx("group/sec relative", SESSION_LIST_STYLE.heading, TITLE_ROW_TOUCH)}>
      {foldable ? (
        <button type="button" aria-expanded={!folded} onClick={toggleBlock} className="touch-hit relative z-10 flex min-w-0 flex-1 items-center gap-1 whitespace-nowrap text-left text-[11px] font-semibold text-ink-3 hover:text-ink-2">
          <span className="min-w-0 truncate">Agents</span>
          <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 opacity-0 transition-[opacity,transform] group-hover/sec:opacity-100 [@media(hover:none)]:opacity-100" style={{ ...sidebarFoldMotion(!folded), transform: folded ? "rotate(-90deg)" : "none" }} />
        </button>
      ) : (
        <span className={AGENTS_TITLE}>Agents</span>
      )}
      {folded && <StateTally counts={tallyCounts} />}
      <GroupFoldButton groups={groupsRef} onAction={() => hover(null)} block={foldable ? { folded, toggle: toggleBlock } : undefined} flat={chatList} />
      {showFilter && <PagedFilterButton filters={dimFilters} machines={machines} chatList={chatList} onToggleChatList={() => { setChatList(!chatList); hover(null); }} />}
      {listItems.length > 0 && <ListMenuButton open={Boolean(listMenu.anchor)} onClick={listMenu.open} />}
      {add && <AgentsAdd onNewAgent={onNewAgent} onNavigate={onNavigate} className={TITLE_ICON_TOUCH} />}
    </div>
    {listMenu.anchor && <Menu anchor={listMenu.anchor} onClose={listMenu.close} align="right" items={listItems} />}
    {!folded && <>
      <HerdrNotices />
      {launches.length > 0 && <Section title={t("sidebar.launching")} folded={launchesFolded} onToggle={toggleLaunches} right={<GroupCount n={launches.length} label={t("sidebar.launching_count", { count: launches.length })} />}>
        {launches.map((launch) => <NavRow key={launch.id} k={`s:${launch.machineId}/${launch.id}`} reg={reg} hover={hover} active={activeKey === `s:${launch.machineId}/${launch.id}`} to={{ name: "session", mid: launch.machineId, sid: launch.id }} go={go} icon={launch.phase === "failed" ? "retry" : "agent"} agent={launch.phase === "failed" ? undefined : launchDisplayEngine(launch)} label={launch.text || baseName(launch.cwd) || t("sidebar.launch_untitled")} hint={launch.phase === "failed" ? t("sidebar.launch_failed") : t("sidebar.launch_background")} />)}
      </Section>}
      <PagedSessionList root={runtimeAgents} path={RUNTIME_AGENTS_PATH} identity={runtimeIdentity} compare={runtimeOrder} removeIdentity={runtimeRemoval} parentOf={sideFold ? undefined : runtimeParentIdentity} excludeIdentities={inFlightLaunchIds} event="sessions_list" grouping={pagedGrouping}
        activeAt={runtimeActiveAt} needsYou={rowNeedsYou} isOpen={(s) => activeKey === `s:${s.machine_id}/${s.session_id}`}
        catalog={catalog} justFinished={justFinished} placeOf={runtimePlace} failureText={agentsReadFailed} holdCounts={holdCounts}
        machineOrder={machineOrder} label={(dimension, id) => dimension === "machine" ? machineWithHub(id, machines) : id}
        renderRow={(s, within, group) => {
          const k = `s:${s.machine_id}/${s.session_id}`;
          // under a group's heading the row leaves out what the heading says: its computer (a space of the catalog's sits
          // under its computer's heading, or the list has one computer, #13522), its space, its to-do
          const where = [within === "machine" || (within === "group" && group?.places[0]?.machine) ? "" : machineWithHub(s.machine_id, machines), within === "group" ? "" : s.space_name || baseName(s.cwd)];
          return <SessionRow s={s} k={k} reg={reg} hover={hover} active={activeKey === k} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} onMenu={onRowMenu} hint={where.filter(Boolean).join(" · ")} showTodoTag={within !== "todo"} />;
        }} />
    </>}
    {rowMenu && <AgentRowMenu s={rowMenu.s} anchor={rowMenu.anchor} at={rowMenu.at} onClose={() => setRowMenu(null)} onRename={setRenaming} onDelete={setDeleting} onLinkTodo={setLinkTarget} />}
    {renaming && <RenameDialog open session={renaming} onClose={() => setRenaming(null)} />}
    {deleting && <DeleteSessionDialog open session={deleting} machineName={machineWithHub(deleting.machine_id, machines)} onClose={() => setDeleting(null)} />}
    <LinkTodoDialog open={Boolean(linkTarget)} session={linkTarget} onClose={closeLinkTarget} />
  </div>;
}


// Tab-scoped UI memory: a reload presents the same active/Idle membership, but a new tab starts from current activity.
// Undefined means no stored membership; a stored empty set is a deliberate all-Idle layout, not a new browser tab.
const ACTIVE_SPACES_KEY = "persona.agentActiveSpaces";
const SHOW_EMPTY_WORKSPACES_KEY = "persona.showEmptyWorkspaces";
const emptyWorkspaceListeners = new Set<() => void>();
let emptyWorkspacesFallback: boolean | null = null;
function readShowEmptyWorkspaces(): boolean {
  try {
    const stored = localStorage.getItem(SHOW_EMPTY_WORKSPACES_KEY);
    if (stored !== null) return stored === "1";
    return emptyWorkspacesFallback ?? false;
  } catch { return emptyWorkspacesFallback ?? false; }
}
function toggleShowEmptyWorkspacesPref() {
  const next = !readShowEmptyWorkspaces();
  try {
    localStorage.setItem(SHOW_EMPTY_WORKSPACES_KEY, next ? "1" : "0");
    emptyWorkspacesFallback = null;
  } catch {
    emptyWorkspacesFallback = next;
  }
  emptyWorkspaceListeners.forEach((fn) => fn());
}
function subscribeShowEmptyWorkspaces(listener: () => void): () => void {
  emptyWorkspaceListeners.add(listener);
  const onStorage = (e: StorageEvent) => { if (e.key === SHOW_EMPTY_WORKSPACES_KEY || e.key === null) listener(); };
  window.addEventListener("storage", onStorage);
  return () => { emptyWorkspaceListeners.delete(listener); window.removeEventListener("storage", onStorage); };
}
function useShowEmptyWorkspaces(): boolean {
  return useSyncExternalStore(subscribeShowEmptyWorkspaces, readShowEmptyWorkspaces, () => false);
}
// By repo's catalog projection lives in its own chunk so cold first screens (which default to By to-do) do not pay for
// it. Loaded once on demand without Suspense; stale-hash chunk failures reload once via PageBoundary's recovery and offer
// Reload on repeat failure instead of retrying a deleted hash forever.
type AgentReposMod = typeof import("../lib/agent-repos");
let agentReposLoader: () => Promise<AgentReposMod> = () => import("../lib/agent-repos");
let loadedAgentRepos: AgentReposMod | null = null;
let loadingAgentRepos: Promise<AgentReposMod> | null = null;
/** Tests: override the dynamic chunk loader and reset its cache. */
export function setAgentReposLoaderForTest(loader: (() => Promise<AgentReposMod>) | null) {
  agentReposLoader = loader ?? (() => import("../lib/agent-repos"));
  loadedAgentRepos = null;
  loadingAgentRepos = null;
}
function useAgentReposModule(enabled: boolean): { mod: AgentReposMod | null; error: { message: string; chunk: boolean } | null; retry: () => void } {
  const [mod, setMod] = useState<AgentReposMod | null>(() => loadedAgentRepos);
  const [error, setError] = useState<{ message: string; chunk: boolean } | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!enabled || mod) return;
    let live = true;
    const req = (loadingAgentRepos ??= agentReposLoader().then((m) => { loadedAgentRepos = m; return m; }));
    req.then(
      (m) => { if (live) { setError(null); setMod(m); } },
      (err: unknown) => {
        if (loadingAgentRepos === req) loadingAgentRepos = null;
        if (reloadForStaleChunk(err)) return;
        const chunk = isChunkError(err);
        reportBoundary(err, "sidebar_agent_repos", chunk);
        if (live) setError({ message: err instanceof Error ? err.message : String(err), chunk });
      },
    );
    return () => { live = false; };
  }, [enabled, mod, attempt]);
  const retry = useCallback(() => { setError(null); setAttempt((n) => n + 1); }, []);
  return enabled ? { mod, error, retry } : { mod: null, error: null, retry };
}
function retainedActiveSpaces(): ReadonlySet<string> | undefined {
  try {
    const stored = sessionStorage.getItem(ACTIVE_SPACES_KEY);
    if (stored === null) return undefined;
    const value: unknown = JSON.parse(stored);
    return Array.isArray(value) ? new Set(value.filter((key): key is string => typeof key === "string")) : undefined;
  } catch {
    return undefined;
  }
}

function LegacyAgentsBlock({ reg, hover, activeKey, go, onNewAgent, onNavigate, workspaceActions, add = true, foldable = false, grouping, hubUnreachable, chooseGrouping, showFilter, dimFilters, chatList, displayValue, onHoldsSeen }: AgentsBlockProps & AgentsDisplayProps & { grouping: AgentGrouping; /** useHubUnreachable */ hubUnreachable: boolean; chooseGrouping: (g: AgentGrouping) => void }) {
  // the current assistant's computers, agents and to-dos (all of them with one assistant)
  const scope = useScope();
  const sidebarMachines = useStore(selectSidebarMachines);
  // the ids of the current assistant's computers (null with one assistant): strings, so a push of a computer's load or
  // memory leaves them equal and doesn't draw the block again
  const ownIds = useStore((s) => (scope.several ? scoped(s.machines, scope).map((m) => m.id) : null));
  const machines = useMemo(() => {
    if (!ownIds) return sidebarMachines;
    const own = new Set(ownIds);
    return sidebarMachines.filter((m) => own.has(m.id));
  }, [ownIds, sidebarMachines]);
  const structuralOrder = useMemo(() => machineSessionOrder(machines), [machines]);
  const pagedTransport = useStore((s) => s.features.includes(SESSIONS_LIST_FEATURE));
  // the whole block folds under its title in the sidebar (#2524); the phone's Agents page is the block, it doesn't
  // and opens by itself when ⌘K puts words on the list, so what was asked for is in view (#6589)
  const [blockFolded, toggleBlock] = useFolded("agents", false, { all: false, holds: dimFilters.query.trim() !== "" });
  const folded = foldable && blockFolded;
  const { sessions: every, loaded, error: listError } = useFullAgentSessions(!folded || !pagedTransport);
  const summaryAgents = useAgentSessions();
  const allLaunches = useStore((s) => s.launches);
  const launches = useMemo(() => Object.values(allLaunches).filter((launch) => launch.phase !== "ready"), [allLaunches]);
  // A session whose launch is still actively starting/sending is already listed under "Starting": keep it out of the
  // regular groups until handoff completes so it moves cleanly from Starting to Working without duplicating (#12380).
  const inFlightLaunchKeys = useMemo(() => {
    const keys = new Set<string>();
    for (const launch of launches) {
      if (isInFlightLaunch(launch) && launch.session) keys.add(`${launch.machineId}\u0000${launch.session.session_id}`);
    }
    return keys;
  }, [launches]);
  const [agents, previewAgents] = useMemo(() => {
    const visible = (rows: Session[]) => rows.filter((s) => isSidebarAgent(s) && (!inFlightLaunchKeys.size || !inFlightLaunchKeys.has(`${s.machine_id}\u0000${s.session_id}`)));
    const primary = folded && pagedTransport && !every.length ? summaryAgents : every;
    return [visible(primary), pagedTransport ? visible(summaryAgents) : []];
  }, [every, summaryAgents, pagedTransport, folded, inFlightLaunchKeys]);
  const all = useScoped(agents);
  const previewAll = useScoped(previewAgents);
  const mutes = useStore((s) => s.mutes);
  const pins = useStore((s) => s.pins);
  const interactions = useStore((s) => s.interactions);
  const todos = useScopedTodos();
  const todosSupported = useStore((s) => s.todosSupported);
  const todosLoaded = useStore((s) => s.todosLoaded);
  const todosUnread = useTodosUnread();
  const shownManagerName = useShownManager();
  const foldPreference = useStore((s) => {
    const a = s.assistant;
    return a && sameManager(a.manager ?? shownManagerName, shownManagerName) && a.todos_v2 === true
      ? a.todos_v2_fold_children !== false : undefined;
  });
  const todosV2 = foldPreference !== undefined;
  // the Filter menu's choices and the words ⌘K's "Filter Agents list" put on it (#6589): the list has no field of its own
  // (both come from AgentsBlock: off in Settings › Agents, the Filter is not drawn and holds nothing)
  const setDimFilters = setAgentListFilters;
  // chat-list rows only for someone who picked them in the Filter menu; the grouped list is the default (#4397)
  const toggleChatList = () => {
    setChatList(!chatList);
    hover(null);
  };
  // Stable, so the memoized grouping bar skips store pushes that leave its props equal.
  const chooseGroupingOutOfChatList = useCallback((g: AgentGrouping) => {
    setChatList(false);
    chooseGrouping(g);
  }, [chooseGrouping]);
  // a hub without todos shows by machine, a by-todo choice kept for when it has them; to-dos that cannot be read, by
  // status until they can (TodosUnread says so). The chat list's rows are on no grouping and need no to-dos: it is
  // drawn as it is, with no word about them
  const todosFailed = !chatList && todosUnread && grouping === "todo" && todosSupported !== false;
  const shownGrouping = todosSupported === false && grouping === "todo" ? "machine" : todosFailed ? "state" : grouping;
  const catalog = useWorkspaceCatalog(shownGrouping === "repo");
  const agentRepos = useAgentReposModule(shownGrouping === "repo");
  const wantsV2Tree = todosV2 && shownGrouping === "todo" && !chatList;
  const [v2Sidebar, setV2Sidebar] = useState<typeof import("../features/todos/board-sidebar-tree")>();
  useEffect(() => {
    if (!wantsV2Tree || v2Sidebar) return;
    let active = true;
    void import("../features/todos/board-sidebar-tree").then(
      (mod) => { if (active) setV2Sidebar(mod); },
      (err: unknown) => {
        if (active && !reloadForStaleChunk(err)) reportBoundary(err, "sidebar_todo_tree", isChunkError(err));
      },
    );
    return () => { active = false; };
  }, [wantsV2Tree, v2Sidebar]);
  const serverBoard = useTodoBoard(scope, wantsV2Tree);
  const olderClosed = useStore((s) => wantsV2Tree ? s.olderClosedTodos : undefined);
  const availableGroupings = useGroupings();
  // the title row's ⋯ (#2812): the archived Agents

  const listMenu = useMenu();
  // the quick filter ("All" / "Needs you" / "Unread" / "Working"), stacked on any grouping; what it showed when chosen stays (`kept`)
  const [savedFilter, setFilter] = useState(() => agentFilter(storedValue("persona.agentFilter")));
  const [kept, setKept] = useState<ReadonlySet<string>>(() => new Set());
  const showEmptyWorkspaces = useShowEmptyWorkspaces();
  const botmuxHidden = useBotmuxHidden();
  // The activity order (Settings › Agents), held still while the pointer or the keyboard is in the sidebar; off, the
  // stable structural order of #11801. By status keeps its own order either way.
  const sortByActivity = useSortByActivity();
  const groupsRef = useRef<HTMLDivElement>(null);
  const engaged = useSidebarEngaged(groupsRef);
  const byUpdate = sortByActivity && shownGrouping !== "state";
  const activityDates = useActivityDates(agents, engaged);
  const activity = useMemo(() => activityOrder(activityDates), [activityDates]);
  const rowOrder = byUpdate ? activity.byUpdated : structuralOrder;
  const listed = useMemo(() => heldAsWorking(withoutBotmux(all, botmuxHidden), interactions), [all, botmuxHidden, interactions]);
  const previewListed = useMemo(() => [...withoutBotmux(previewAll, botmuxHidden)].sort(rowOrder), [previewAll, botmuxHidden, rowOrder]);
  const facets = useMemo(() => buildAgentFacets(listed, machines, pins), [listed, machines, pins]);
  const dimActive = hasActiveDimensionFilters(dimFilters);
  const clock = useNow(60_000, dimActive);
  const evalCtx = useMemo(() => ({ machines, pins, interactions, now: Date.now() }), [listed, dimFilters, machines, pins, interactions, clock]);
  const { sessions: dimFiltered, plan: searchPlan } = useMemo(
    () => applyDimensionFilters(listed, dimFilters, evalCtx, facets),
    [listed, dimFilters, evalCtx, facets],
  );
  // A host pin keeps its saved conversations in the repository tree, including ones older than Recent's 40 rows.
  const { live, recent: offline } = useMemo(() => splitAgentSessions(dimFiltered, machines,
    shownGrouping === "repo" && !chatList && agentRepos.mod ? (s) => agentRepos.mod!.hasWorkspacePin(s, catalog.workspaces, catalog.manager) : undefined),
  [dimFiltered, machines, shownGrouping, chatList, catalog, agentRepos.mod]);
  const filter = savedFilter === "feishu" && !live.some((s) => matchesFilter(s, "feishu", mutes)) ? "all" : savedFilter;
  const seenMode = useSeenRowsMode();
  const heldOrder = useHeldSeenOrder(seenMode);
  const needsSeen = useMemo(() => needsSeenOf(live, mutes), [live, mutes]);
  useEffect(() => {
    if (seenMode === "stay") noteShownUnseen(needsSeen, live);
  }, [seenMode, needsSeen, live]);
  const matching = useMemo(() => matchingKeys(live, filter, mutes), [live, filter, mutes]);
  const seeded = useRef(false);
  useLayoutEffect(() => {
    // a filter saved in this browser starts from what it matches once the list has loaded; while chosen, newly matching rows join `kept` (#12585)
    if (!loaded) return;
    if (!seeded.current) {
      seeded.current = true;
      setKept(matching);
      return;
    }
    if (filter !== "all") setKept((cur) => extendKept(cur, matching));
  }, [loaded, filter, matching]);
  const filtered = filter !== "all" || dimActive;
  const panesOn = useStore((s) => s.features.includes("terminal_panes"));
  const sidebarTerminals = useSidebarTerminals();
  // Shells sit alongside Agents in their workspace. The quick filters describe Agents only.
  const wantsShells = (panesOn || sidebarTerminals) && !chatList && filter === "all" && (shownGrouping === "machine" || shownGrouping === "space");
  const { listing: panes, refresh: refreshPanes } = usePanes(machines, wantsShells);
  const visibleShellTrees = useMemo(() => {
    if (!wantsShells) return undefined;
    const machineFilter = dimFilters.machines.length > 0 ? new Set(dimFilters.machines) : null;
    const query = dimFilters.query.trim();
    const out: Record<string, TermSpace[]> = {};
    for (const m of machines) {
      if (machineFilter && !machineFilter.has(m.id)) continue;
      let spaces = shellsOnly(buildTerminalTree(panes[m.id]?.data, []));
      if (query) spaces = filterTerminalTree(spaces, query, machineWithHub(m.id, machines));
      if (spaces.length > 0) out[m.id] = spaces;
    }
    return Object.keys(out).length ? out : undefined;
  }, [wantsShells, machines, panes, dimFilters.machines, dimFilters.query]);
  const { needs, unread, working, bg_waiting, feishu, terminal } = useMemo(() => filterCounts(live, mutes), [live, mutes]);
  // A session can change its text or timestamp without changing any filter's count.
  const filterTotals = useMemo(() => ({ needs, unread, working, bg_waiting, feishu, terminal }), [needs, unread, working, bg_waiting, feishu, terminal]);
  const shown = useMemo(() => applyFilter(live, filter, mutes, kept), [live, filter, mutes, kept]);
  const legacyHoldsSeen = seenMode === "stay" && !dimFilters.query.trim() && shown.some((s) => isHeldSeen(s, mutes, heldOrder));
  useEffect(() => { onHoldsSeen?.(legacyHoldsSeen); }, [onHoldsSeen, legacyHoldsSeen]);
  useEffect(() => () => onHoldsSeen?.(false), [onHoldsSeen]);
  // a pinned agent shows once, under "Pinned" (an offline one too, unless a filter is on); the groups below are the rest
  const pinnedAgents = useMemo(() => pinnedSessions(filter !== "all" ? shown : [...shown, ...offline], pins), [filter, shown, offline, pins]);
  const unpinned = useMemo(() => {
    const keys = new Set(pinnedAgents.map(agentKey));
    return keys.size ? shown.filter((s) => !keys.has(agentKey(s))) : shown;
  }, [shown, pinnedAgents]);
  const chatListUnpinned = useMemo(() => [...unpinned].sort(rowOrder), [unpinned, rowOrder]);
  const recent = useMemo(() => (filter !== "all" ? [] : offline.filter((s) => !pinnedAgents.some((p) => agentKey(p) === agentKey(s)))), [filter, offline, pinnedAgents]);
  const { spaces, loose } = useMemo(() => {
    const built = buildSpaces(unpinned, machines);
    const placed = { spaces: built.spaces.map((sp) => ({ ...sp, sessions: [...sp.sessions].sort(rowOrder) })), loose: [...built.loose].sort(rowOrder) };
    return byUpdate ? activity.projectsByUpdate(placed) : placed;
  }, [unpinned, machines, rowOrder, byUpdate, activity]);
  const groups = useMemo(() => {
    if (shownGrouping === "space" || shownGrouping === "repo" || shownGrouping === "machine") return [];
    if (shownGrouping === "todo") return [];
    return buildAgentGroups(unpinned, machines, "state", mutes, undefined, heldOrder);
  }, [unpinned, machines, shownGrouping, mutes, todos, rowOrder, heldOrder]);
  // by todo waits for the first GET /todos: the socket pushes the sessions before it answers, and a tree drawn without
  // the todos puts every agent under No to-do for a moment, then moves them under their todos (#2812); the chat list's
  // rows are drawn at once
  const todosPending = !chatList && shownGrouping === "todo" && !todosLoaded;
  // The first summary page can orient the owner while the complete baseline is read. By to-do still waits for its
  // grouping input; neutral rows never invent provisional groups or counts, and the complete tree replaces them at once.
  const loadingPreview = !loaded && !todosPending && !chatList && !filtered && !hubUnreachable && !listError && previewListed.length > 0;
  // Keep showing progress until the grouping inputs and session list are ready. On an older hub the socket's rows
  // can already be shown while the first GET /sessions is still in flight (#2812).
  const listNote = hubUnreachable ? <p className="px-2 py-1 text-xs leading-normal text-ink-3">{t("sidebar.agents_offline")}</p>
    : !loaded && listError ? <ListError text={agentsReadFailed(listError)} onRetry={retryFullAgentSessions} />
    : shownGrouping === "repo" && !chatList && agentRepos.error ? (
      agentRepos.error.chunk
        ? <ListError text={t("pageboundary.chunk_title")} action={t("pageboundary.reload")} onRetry={reloadPage} />
        : <ListError text={agentsReadFailed(agentRepos.error.message)} onRetry={agentRepos.retry} />
    )
    : todosPending || (shownGrouping === "repo" && !chatList && !agentRepos.mod) || (!loaded && every.length === 0) ? <p className="px-2 py-1 text-[12px] text-ink-3">{t("sidebar.loading")}</p>
    : null;
  // Only inputs the tree reads rebuild it; unchanged nodes keep their objects so their rows' memo holds.
  const lastTodoTree = useRef<TodoTree | undefined>(undefined);
  // its groups of agents on no to-do stay listed while empty, to say what would be there (#6458); not while a filter or a
  // search narrows the list, nor in a list with no agent at all
  const keepEmptyGroups = !filtered && live.length + offline.length > 0;
  const todoTree = useMemo(() => {
    if (shownGrouping !== "todo" || !todosLoaded) return undefined;
    const options = { keepEmpty: keepEmptyGroups, compareSessions: rowOrder };
    const now = Date.now();
    const built = wantsV2Tree && v2Sidebar
      ? v2Sidebar.buildV2SidebarTree(unpinned, machines, todos, mutes, now, { ...options, board: serverBoard, foldChildren: foldPreference !== false, olderClosed })
      : buildTodoTree(unpinned, machines, todos, mutes, now, options);
    return shareTree(lastTodoTree.current, byUpdate ? activity.todoTreeByUpdate(built) : built);
  }, [unpinned, machines, shownGrouping, mutes, todos, todosLoaded, keepEmptyGroups, rowOrder, byUpdate, activity, wantsV2Tree, v2Sidebar, foldPreference, serverBoard, olderClosed]);
  lastTodoTree.current = todoTree;
  const repoMachines = useMemo(() => {
    if (shownGrouping !== "repo" || !agentRepos.mod) return [];
    const built = agentRepos.mod.buildRepoMachineTree(unpinned, machines, { ...catalog, hiddenSources: botmuxHidden ? ["botmux"] : undefined, navigationSessions: [...shown, ...pinnedAgents.filter((p) => !shown.includes(p))], hideEmpty: filtered, showEmptyWorkspaces, compareSessions: rowOrder });
    return byUpdate ? activity.repoMachinesByUpdate(built) : built;
  }, [unpinned, machines, shownGrouping, agentRepos.mod, catalog, shown, pinnedAgents, filtered, showEmptyWorkspaces, botmuxHidden, rowOrder, byUpdate, activity]);
  const machineTree = useMemo(() => {
    if (shownGrouping !== "machine") return [];
    const built = buildMachineTree(machines, unpinned, {
      pins,
      hideEmpty: filtered,
      keepMachine: visibleShellTrees ? (id) => Boolean(visibleShellTrees[id]?.length) : undefined,
    });
    return byUpdate ? activity.machineTreeByUpdate(built) : built;
  }, [shownGrouping, machines, unpinned, pins, filtered, visibleShellTrees, byUpdate, activity]);
  // by todo a group mixes hosts (herdr, botmux…): when the list has more than one, rows outside herdr name theirs (#1836)
  const mixedHosts = useMemo(() => shownGrouping === "todo" && hostsOf(live).size > 1, [shownGrouping, live]);
  const unreadShown = filter === "unread" ? unreadOf(shown) : [];
  // Stable event handlers read the last committed list, including pushes received since the bar last rendered.
  const filterInputs = useRef({ live, mutes, needsSeen });
  useLayoutEffect(() => { filterInputs.current = { live, mutes, needsSeen }; }, [live, mutes, needsSeen]);
  const choose = useCallback((f: AgentFilter) => {
    storeValue("persona.agentFilter", f);
    setFilter(f);
    setKept(matchingKeys(filterInputs.current.live, f, filterInputs.current.mutes));
    releaseAllSeen(filterInputs.current.needsSeen);
    hover(null);
  }, [hover]);
  useReleaseSeenOnScope(dimFilters.machines.join("\u0000"), scope.id, (machineChanged) => {
    if (machineChanged) setKept(new Set());
    releaseAllSeen(filterInputs.current.needsSeen);
  });
  const clearSectionSeen = useCallback((keys: string[]) => {
    releaseSeen(keys);
    setKept((cur) => removeKept(cur, keys));
    hover(null);
  }, [hover]);
  const [rowMenu, setRowMenu] = useState<{ s: Session; anchor: HTMLElement; at?: MenuAt } | null>(null);
  const [spaceMenu, setSpaceMenu] = useState<{ sp: Space; anchor: HTMLElement; at?: MenuAt; fold?: MenuNode["fold"] } | null>(null);
  const [nodeMenu, setNodeMenu] = useState<{ node: MenuNode; at: MenuAt; header?: string } | null>(null);
  const [renaming, setRenaming] = useState<Session | null>(null);
  const [renamingSpace, setRenamingSpace] = useState<Session | null>(null);
  const [deleting, setDeleting] = useState<Session | null>(null);
  const archives = useStore((s) => s.features.includes(AGENT_ARCHIVES_FEATURE));
  const [confirmSeen, setConfirmSeen] = useState<{ list: Session[]; release?: Session[] } | null>(null);
  // stable, like `go`: every row gets it, and a new one each render would re-render them all (SessionRow's memo)
  const onRowMenu = useCallback<RowMenuFn>((s, anchor, at) => setRowMenu((m) => (!at && m?.anchor === anchor ? null : { s, anchor, at })), []);
  // stable too: TabRow builds the menu of a tab with several panes on onNodeMenu, and that menu is its lead row's onMenu
  const onSpaceMenu = useCallback<SpaceMenuFn>((sp, anchor, at, fold) => setSpaceMenu((m) => (!at && m?.anchor === anchor ? null : { sp, anchor, at, fold })), []);
  const onNodeMenu = useCallback<NodeMenuFn>((node, at, header, toggle) => setNodeMenu((m) => (toggle && m?.at.anchor && m.at.anchor === at.anchor ? null : { node, at, header })), []);
  const [recentFolded, toggleRecent] = useFolded("recent", true, { holds: holdsRow(recent, activeKey), scope: AGENTS_FOLD });
  const [idleFolded, toggleIdle] = useFolded("idle-spaces", true, { scope: AGENTS_FOLD });
  const [pinnedFolded, togglePinned] = useFolded("agents-pinned", false, { holds: holdsRow(pinnedAgents, activeKey), scope: AGENTS_FOLD });
  const [launchesFolded, toggleLaunches] = useFolded("agents-launching", false, { holds: launches.some((l) => activeKey === `s:${l.machineId}/${l.id}`), scope: AGENTS_FOLD });
  const [showAllRecent, setShowAllRecent] = useState(false);
  const recentShown = showAllRecent ? recent : firstAndCurrent(recent, 8, (s) => rowKey(s) === activeKey);
  const recentState = worstTreeState(recent.map(agentTreeState));
  const recentRows = (
    <>
      <WindowedRows items={recentShown} holds={(s) => rowKey(s) === activeKey} render={(s) => (
        <SessionRow key={s.session_id} s={s} k={`s:${s.machine_id}/${s.session_id}`} reg={reg} hover={hover} active={activeKey === `s:${s.machine_id}/${s.session_id}`} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} onMenu={onRowMenu} />
      )} />
      {recent.length > 8 && (
        <button type="button" onClick={() => setShowAllRecent((x) => !x)} className="relative z-10 w-full rounded-[7px] px-2 py-1 text-left text-[12px] text-ink-3 hover:text-ink-2 [@media(pointer:coarse)]:min-h-11">
          {showAllRecent ? t("sidebar.show_less") : t("sidebar.show_more", { count: recent.length - recentShown.length })}
        </button>
      )}
    </>
  );
  const [linkTargetSession, setLinkTargetSession] = useState<Session | undefined>();
  const [linkTargetSpace, setLinkTargetSpace] = useState<LinkTargetSpace | undefined>();
  const closeLinkSession = useCallback(() => setLinkTargetSession(undefined), []);
  const closeLinkSpace = useCallback(() => setLinkTargetSpace(undefined), []);
  const closeMarkSeen = useCallback(() => setConfirmSeen(null), []);
  const clearKept = useCallback(() => setKept(new Set()), []);

  // With no stored membership a new tab derives the current busy layout. Once membership exists, ordinary working does
  // not pull an Idle workspace out of its section. Attention still does: needs-you, unread, replies, Just finished and
  // background waits, as does opening a row. Quieting an already active workspace leaves its membership alone.
  const retainedActive = useRef<{ value: ReadonlySet<string> | undefined } | undefined>(undefined);
  if (retainedActive.current === undefined) retainedActive.current = { value: retainedActiveSpaces() };
  const storedActive = retainedActive.current.value;
  const busy = (sp: Space) => sp.sessions.some((s) => {
    const selected = activeKey === `s:${s.machine_id}/${s.session_id}`;
    if (storedActive === undefined) return !isQuiet(s, mutes, heldOrder) || selected;
    const state = stateOf(s);
    return selected || (!isQuiet(s, mutes, heldOrder) && (state !== "working" || backgroundWaiting(s)));
  });
  // pinned spaces lead; under a quick filter nothing folds into "Idle spaces"
  const arrangedSpaces = arrangeSpaces(spaces, { pins, mutes, filtered, busy, retainedActive: storedActive });
  // A temporary filter or another grouping is not a presentation of this list and must not replace its membership.
  useLayoutEffect(() => {
    if (!loaded || filtered || shownGrouping !== "space") return;
    const keys = arrangedSpaces.active.map((sp) => sp.key);
    retainedActive.current = { value: new Set(keys) };
    try {
      const value = JSON.stringify(keys);
      if (sessionStorage.getItem(ACTIVE_SPACES_KEY) !== value) sessionStorage.setItem(ACTIVE_SPACES_KEY, value);
    } catch { /* A blocked or full store still keeps this mount's in-memory placement. */ }
  }, [arrangedSpaces, filtered, shownGrouping, loaded]);
  const { active, idle } = arrangedSpaces;
  const tree = { machines, reg, hover, activeKey, go, mutes, onSpaceMenu, onRowMenu, onNodeMenu, onNavigate };

  // the list menu, the paged list's too (agentListItems); with nothing in it the menu's button goes
  const listItems = agentListItems({
    archives, hover, onNavigate,
    display: { value: displayValue, sort: sortByActivity, filter: showFilter },
    emptyWorkspaces: shownGrouping === "repo" && !chatList ? { show: showEmptyWorkspaces, onToggle: toggleShowEmptyWorkspacesPref } : undefined,
  });

  return (
    <div data-sidebar-agents ref={groupsRef}>
    <SidebarGroup className="mt-3" heading={
      <div data-sidebar-agents-controls className="pb-1">
      {/* the sidebar's top-level heading box (#10637): its controls never make it taller than Pages' or Persona chats' */}
      <div className={cx("group/sec relative flex sidebar-heading items-center gap-1 px-2", TITLE_ROW_TOUCH)}>
        {foldable ? (
          <button type="button" aria-expanded={!folded} onClick={toggleBlock} className="touch-hit relative z-10 flex min-w-0 flex-1 items-center gap-1 whitespace-nowrap text-left text-[11px] font-semibold text-ink-3 hover:text-ink-2">
            <span className="min-w-0 truncate">Agents</span>
            <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 opacity-0 transition-[opacity,transform] group-hover/sec:opacity-100 [@media(hover:none)]:opacity-100" style={{ ...sidebarFoldMotion(!folded), transform: folded ? "rotate(-90deg)" : "none" }} />
          </button>
        ) : (
          <span className={AGENTS_TITLE}>Agents</span>
        )}
        {folded && <SessionTally sessions={live} folded />}
        <GroupFoldButton groups={groupsRef} onAction={() => hover(null)} block={foldable ? { folded, toggle: toggleBlock } : undefined} flat={chatList} />
        {/* the Filter beside the list's other menu buttons, its count on it (#6589; it sat beside the list's own search field) */}
        {showFilter && <AgentFilterButton filters={dimFilters} onChange={setDimFilters} facets={facets} chatList={chatList} onToggleChatList={toggleChatList} />}
        {listItems.length > 0 && (
          <ListMenuButton open={Boolean(listMenu.anchor)} onClick={listMenu.open} />
        )}
        {add && <AgentsAdd onNewAgent={onNewAgent} onNavigate={onNavigate} className={TITLE_ICON_TOUCH} />}
      </div>
      <Collapse open={!folded} sidebarFold>
        <GroupingBar groupings={availableGroupings} selection={shownGrouping} onChoose={chooseGroupingOutOfChatList} />
        <FilterBar selection={filter} counts={filterTotals} onChoose={choose} />
      </Collapse>
      </div>
    }>
      {listMenu.anchor && <Menu anchor={listMenu.anchor} onClose={listMenu.close} align="right" items={listItems} />}
      <FoldScope.Provider value={AGENTS_FOLD}>
      <Collapse open={!folded} sidebarFold unclipped>
      <div inert={folded || undefined}>
      {/* what filters the list, only while something does: the chips and Reset, Search as Code's plan bar */}
      {showFilter && <AgentFilterChips filters={dimFilters} onChange={setDimFilters} facets={facets} plan={searchPlan} />}
      {/* "herdr is not running on <computer>" with "Start" (#1916): before a message to an agent there fails */}
      {/* a GitHub star, once this browser started enough Agents and nothing waits (#5761) */}
      {!folded && <HerdrNotices />}

      {unreadShown.length > 0 && (
        <button type="button" onClick={() => setConfirmSeen({ list: unreadShown, release: shown })} className="relative z-10 mt-1 flex w-full items-center gap-2 rounded-[7px] px-2 sidebar-row text-left transition-[background-color,transform] duration-100 hover:bg-hover active:scale-[0.98] [@media(pointer:coarse)]:min-h-11">
          <span className="flex size-3.5 shrink-0 items-center justify-center text-green"><Icon name="check" size={13} stroke={2.4} /></span>
          <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{markAllSeenLabel()}</span>
          <span className="shrink-0 text-[11px] whitespace-nowrap text-ink-3 tabular-nums">{stateCount("unread", unreadShown.length)}</span>
        </button>
      )}

      {todosFailed && <TodosUnread />}

      {!listNote && filtered && shown.length === 0 && pinnedAgents.length === 0 && !visibleShellTrees && (
        <div className="mt-2 flex items-center gap-2 px-2 py-1.5 text-[12px] text-ink-3">
          <span className="min-w-0 flex-1 truncate">{filter !== "all" ? t("sidebar.filter_empty", { filter: AGENT_FILTERS.find((f) => f.key === filter)!.label }) : t("sidebar.no_match")}</span>
          <button type="button" onClick={() => { choose("all"); setDimFilters(DEFAULT_DIMENSION_FILTERS); }} className="touch-hit relative z-10 shrink-0 whitespace-nowrap rounded-control px-1.5 py-0.5 text-[12px] font-medium text-accent-ink hover:bg-hover">{t("sidebar.show_all")}</button>
        </div>
      )}

      {pinnedAgents.length > 0 && (
        <Section title={<span className="flex items-center gap-1"><Icon name="pin" size={10} stroke={2.2} />{t("sidebar.pinned_section")}</span>} folded={pinnedFolded} onToggle={togglePinned} right={<SessionTally sessions={pinnedAgents} folded={pinnedFolded} />}>
          {pinnedAgents.map((s) => {
            const key = `s:${s.machine_id}/${s.session_id}`;
            if (chatList) {
              return <AgentChatListRow key={key} s={s} k={key} reg={reg} hover={hover} active={activeKey === key} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} pinned mutes={mutes} filters={dimFilters} ctx={evalCtx} onMenu={onRowMenu} />;
            }
            return <SessionRow key={key} s={s} k={key} reg={reg} hover={hover} active={activeKey === key} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} onMenu={onRowMenu} />;
          })}
        </Section>
      )}

      {launches.length > 0 && <Section title={t("sidebar.launching")} folded={launchesFolded} onToggle={toggleLaunches} right={<GroupCount n={launches.length} label={t("sidebar.launching_count", { count: launches.length })} />}>
        {launches.map((launch) => <NavRow key={launch.id} k={`s:${launch.machineId}/${launch.id}`} reg={reg} hover={hover} active={activeKey === `s:${launch.machineId}/${launch.id}`} to={{ name: "session", mid: launch.machineId, sid: launch.id }} go={go} icon={launch.phase === "failed" ? "retry" : "agent"} agent={launch.phase === "failed" ? undefined : launchDisplayEngine(launch)} label={launch.text || baseName(launch.cwd) || t("sidebar.launch_untitled")} hint={launch.phase === "failed" ? t("sidebar.launch_failed") : t("sidebar.launch_background")} />)}
      </Section>}

      {loadingPreview ? <>
        {listNote}
        <div data-agents-preview className="mt-1 flex flex-col gap-px">
          <WindowedRows items={previewListed} holds={(s) => rowKey(s) === activeKey} render={(s) => (
            <SessionRow key={s.session_id} s={s} k={`s:${s.machine_id}/${s.session_id}`} reg={reg} hover={hover} active={activeKey === `s:${s.machine_id}/${s.session_id}`} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} onMenu={onRowMenu} />
          )} />
        </div>
      </> : listNote ?? (chatList ? (
        <div className="mt-1 flex flex-col gap-1" data-testid="agent-chatlist-section">
          <WindowedRows items={chatListUnpinned} holds={(s) => rowKey(s) === activeKey} render={(s) => {
            const key = `s:${s.machine_id}/${s.session_id}`;
            return <AgentChatListRow key={key} s={s} k={key} reg={reg} hover={hover} active={activeKey === key} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} pinned={false} mutes={mutes} filters={dimFilters} ctx={evalCtx} onMenu={onRowMenu} />;
          }} />
        </div>
      ) : shownGrouping === "space" ? <>
      {active.map((sp) => {
        const matched = visibleShellTrees?.[sp.machineId]?.filter((ts) => ts.label === sp.name);
        const online = machines.find((m) => m.id === sp.machineId)?.online !== false;
        return <SpaceSection key={sp.key} sp={sp} machineName={machines.length > 1 ? machineWithHub(sp.machineId, machines) : undefined} shells={matched?.length ? matched : undefined} online={online} reg={reg} hover={hover} activeKey={activeKey} go={go} pinned={isSpacePinned(sp.machineId, sp.name, pins)} muted={isSpaceMuted(sp, mutes)} onMenu={onSpaceMenu} onRowMenu={onRowMenu} onNodeMenu={onNodeMenu} onNavigate={onNavigate} />;
      })}
      {visibleShellTrees && machines.flatMap((m) =>
        (visibleShellTrees[m.id] ?? [])
          .filter((ts) => !active.some((sp) => sp.machineId === m.id && sp.name === ts.label))
          .map((ts) => (
            <ShellSpaceSection
              key={`shell:${m.id}/${ts.host}:${ts.id}`}
              mid={m.id}
              online={m.online}
              sp={ts}
              machineName={machines.length > 1 ? machineWithHub(m.id, machines) : undefined}
              reg={reg}
              hover={hover}
              activeKey={activeKey}
              go={go}
              onNodeMenu={onNodeMenu}
            />
          )),
      )}

      {idle.length > 0 && (
        <Section title={t("sidebar.idle_spaces", { count: idle.length })} folded={idleFolded} onToggle={toggleIdle}>
          <WindowedRows items={idle.flatMap((sp) => sp.sessions.map((s) => ({ sp, s })))} holds={({ s }) => rowKey(s) === activeKey} render={({ sp, s }) => {
            const fallback = sp.sessions.length > 1 && tabLabel(s);
            return <SessionRow key={s.session_id} s={s} k={`s:${s.machine_id}/${s.session_id}`} reg={reg} hover={hover} active={activeKey === `s:${s.machine_id}/${s.session_id}`} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} tabFallback={fallback ? `${sp.name} › ${fallback}` : undefined} onMenu={onRowMenu} />;
          }} />
        </Section>
      )}

      {loose.length > 0 && <LooseSection sessions={loose} reg={reg} hover={hover} activeKey={activeKey} go={go} onRowMenu={onRowMenu} />}

      </> : shownGrouping === "machine" ? machineTree.map((m) => (
        <MachineSection key={m.key} m={m} shells={visibleShellTrees?.[m.machine.id]} {...tree} />
      )) : shownGrouping === "repo" ? (
        repoMachines.map((m) => (
          <RepoMachineSection key={m.key} m={m} flat={repoMachines.length <= 1} machines={machines} reg={reg} hover={hover} activeKey={activeKey} go={go} onRowMenu={onRowMenu} {...workspaceActions} />
        ))
      ) : todoTree ? (
        <TodoTreeView tree={todoTree} showHost={mixedHosts} reg={reg} hover={hover} activeKey={activeKey} go={go} onRowMenu={onRowMenu} onNavigate={onNavigate} after={recent.length > 0 && (
          // "By to-do" draws "Recent (not running)" as a row like its to-dos' and groups' (#6458); its name says what it
          // holds, so no ⓘ, and like under the other groupings it is listed only with something in it (#6447, the owner)
          <GroupBranch branch="recent" name={t("sidebar.recent")} icon="history" n={recent.length} state={recentState} label={agentGroupLabel(recent, recentState)} folded={recentFolded} onToggle={toggleRecent}>
            {recentRows}
          </GroupBranch>
        )} />
      ) : groups.map((group) => (
        <AgentSection key={group.key} group={group} byTodo={shownGrouping === "todo"} showHost={mixedHosts} reg={reg} hover={hover} activeKey={activeKey} go={go} onRowMenu={onRowMenu} onNavigate={onNavigate} stay={seenMode === "stay"} onClearSeen={clearSectionSeen} />
      )))}

      {recent.length > 0 && !todoTree && (
        <Section title={t("sidebar.recent")} folded={recentFolded} onToggle={toggleRecent} right={<GroupCount n={recent.length} label={t("sidebar.agent_count", { count: recent.length })} />}>
          {recentRows}
        </Section>
      )}
      </div>
      </Collapse>
      </FoldScope.Provider>
    </SidebarGroup>
      {/* the row's ⋯ (and its right click): pin, rename, mute, the todo it hangs on, close, delete — a long press on iOS
          Safari sends no contextmenu, so nothing lives only there */}
      {rowMenu && <AgentRowMenu s={rowMenu.s} anchor={rowMenu.anchor} at={rowMenu.at} onClose={() => setRowMenu(null)} onRename={setRenaming} onDelete={setDeleting} onLinkTodo={setLinkTargetSession} />}
      {spaceMenu && (
        <SpaceMenu
          sp={spaceMenu.sp}
          anchor={spaceMenu.anchor}
          at={spaceMenu.at}
          fold={spaceMenu.fold}
          onClose={() => setSpaceMenu(null)}
          onRename={setRenamingSpace}
          onMarkSeen={(list, release) => setConfirmSeen({ list, release })}
          onLinkTodo={(sp) => setLinkTargetSpace({ machineId: sp.machineId, space: sp.name })}
        />
      )}
      {nodeMenu && <NodeMenu node={nodeMenu.node} at={nodeMenu.at} header={nodeMenu.header} onClose={() => setNodeMenu(null)} changed={() => { void refresh().catch(() => {}); void refreshPanes(); }} />}
      {renaming && <RenameDialog open session={renaming} onClose={() => setRenaming(null)} />}
      {renamingSpace && <RenameSpaceDialog open session={renamingSpace} onClose={() => setRenamingSpace(null)} />}
      {deleting && <DeleteSessionDialog open session={deleting} machineName={machineWithHub(deleting.machine_id, machines)} onClose={() => setDeleting(null)} />}
      <MarkSeenDialog list={confirmSeen?.list ?? null} release={confirmSeen?.release} onClose={closeMarkSeen} onDone={clearKept} />
      <LinkTodoDialog open={Boolean(linkTargetSession)} session={linkTargetSession} onClose={closeLinkSession} />
      <LinkTodoDialog open={Boolean(linkTargetSpace)} space={linkTargetSpace} onClose={closeLinkSpace} />
    </div>
  );
}

/** How Agents are grouped (#2812): one segment per grouping, "By to-do" first, above the quick filter. The segments say
 *  the short name (To-do / Machine…) so all five fit the sidebar's width on one line; the full name is their label. */
const GroupingBar = memo(function GroupingBar({ groupings, selection, onChoose }: { groupings: readonly { key: AgentGrouping; label: string; short: string }[]; selection: AgentGrouping; onChoose: (g: AgentGrouping) => void }) {
  return (
    <div role="radiogroup" aria-label={t("sidebar.grouping_header")} className="mt-1.5 mx-1 flex gap-0.5 rounded-full bg-inset p-0.5 shadow-hairline">
      {groupings.map((g) => {
        const on = g.key === selection;
        return (
          <button
            key={g.key}
            type="button"
            role="radio"
            aria-checked={on}
            aria-label={g.label}
            title={g.label}
            onClick={() => onChoose(g.key)}
            className={cx(
              "touch-hit relative z-10 flex h-6 min-w-0 flex-auto items-center justify-center whitespace-nowrap rounded-full px-1.5 text-[11.5px] transition-[background-color,color,box-shadow] duration-150",
              on ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-2 hover:bg-hover hover:text-ink",
            )}
          >
            <span className="truncate">{g.short}</span>
          </button>
        );
      })}
    </div>
  );
});

/** Quick filters describe the visible inventory; an empty Lark filter is never offered. */
const FilterBar = memo(function FilterBar({ selection, counts, onChoose }: { selection: AgentFilter; counts: FilterCounts; onChoose: (f: AgentFilter) => void }) {
  const keys = shownFilters(selection, counts);
  const format = useBotmuxMode();
  const shown = useBotmuxShown();
  const editable = useAgentsDisplayEditable();
  const botmuxMode = shown !== true ? "hide" : format;
  const showBotmuxChip = counts.feishu > 0 || shown === false;
  if (keys.length < 2 && !showBotmuxChip) return null;
  return (
    <div className="mt-1.5 flex flex-wrap gap-0.5 px-1">
      {keys.length > 1 && <div role="radiogroup" aria-label={t("sidebar.quick_filters")} className="contents">
      {keys.map((k) => {
        const f = AGENT_FILTERS.find((x) => x.key === k)!;
        const on = k === selection;
        const n = k === "all" ? undefined : counts[k];
        return (
          <button
            key={k}
            type="button"
            role="radio"
            aria-checked={on}
            aria-label={n === undefined ? f.label : `${f.label} ${n}`}
            onClick={() => onChoose(k)}
            className={cx(
              "touch-hit relative z-10 flex h-6 items-center gap-1 whitespace-nowrap rounded-full px-[7px] text-[11.5px] transition-[background-color,color,box-shadow] duration-150",
              on ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-2 hover:bg-hover hover:text-ink",
            )}
          >
            {f.state && <StatusMark state={f.state} size="sm" waiting={k === "bg_waiting"} />}
            {f.label}
            {n !== undefined && <span className={cx("tabular-nums", on ? "text-ink-2" : "text-ink-3")}>{n}</span>}
          </button>
        );
      })}
      </div>}
      {showBotmuxChip && <Suspense fallback={null}><BotmuxModeChip mode={botmuxMode} disabled={!editable} /></Suspense>}
    </div>
  );
});

// Every group in the sidebar is one of these: its title folds it, and the fold is kept in this browser (useFolded).
function AgentSection({ group, byTodo, showHost, reg, hover, activeKey, go, onRowMenu, onNavigate, stay = false, onClearSeen }: { group: AgentGroup; byTodo?: boolean; showHost?: boolean; reg: RegFn; hover: (k: string | null) => void; activeKey: string; go: GoFn; onRowMenu: RowMenuFn; onNavigate?: () => void; stay?: boolean; onClearSeen?: (keys: string[]) => void }) {
  const [folded, toggle] = useFolded(group.key, group.folded, { follow: true, holds: holdsRow(group.sessions, activeKey) });
  const row = (s: Session) => {
    const key = `s:${s.machine_id}/${s.session_id}`;
    return <SessionRow key={key} s={s} k={key} reg={reg} hover={hover} active={activeKey === key} to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={go} host={showHost && sessionHost(s) !== "herdr" ? engineName(sessionHost(s)) : undefined} showTodoTag={!byTodo} onMenu={onRowMenu} />;
  };

  const statusInfo = group.todoStatus ? TODO_STATUS_INFO[group.todoStatus as TodoStatus] : undefined;
  const statusTone = statusInfo?.tone === "red" ? "text-red" : statusInfo?.tone === "green" ? "text-green" : statusInfo?.tone === "amber" ? "text-orange" : "text-ink-3";

  const titleNode = group.todoId ? (
    <span className="flex min-w-0 items-center gap-1.5">
      <Icon name="todo" size={12} stroke={2} className="shrink-0 text-ink-3" />
      <span
        onClick={(e) => {
          e.stopPropagation();
          openTodoFromTag(group.todoId!, navigate, onNavigate);
        }}
        className="truncate normal-case tracking-normal hover:underline hover:text-ink cursor-pointer"
      >
        {group.name}
      </span>
    </span>
  ) : (
    <span className="flex min-w-0 items-center gap-1.5">
      {group.state && <span aria-hidden><StatusMark state={group.state} size="sm" waiting={group.waiting} /></span>}
      <span className="truncate">{group.name}</span>
    </span>
  );

  const heldRows = stay ? (group.heldSeen ?? []) : [];
  const counted = group.state === "needs_you" ? group.sessions.filter((s) => s.view ? s.view.counted : true).length : stay ? Math.max(0, group.sessions.length - heldRows.length) : group.sessions.length;
  const countNode = <GroupCount n={counted} label={t("sidebar.agent_count", { count: counted })} />;
  const rightNode = group.todoStatus ? (
    <span className={cx("text-[11px] font-semibold tracking-normal normal-case", statusTone)}>
      {statusInfo?.label}
    </span>
  ) : heldRows.length > 0 && onClearSeen ? (
    <span className="flex shrink-0 items-center gap-1.5">
      <Suspense fallback={null}><ClearSeenButton count={heldRows.length} onClear={() => onClearSeen(heldRows.map(agentKey))} /></Suspense>
      {countNode}
    </span>
  ) : countNode;

  return (
    <Section title={titleNode} folded={folded} onToggle={toggle} right={rightNode}>
      <WindowedRows items={group.sessions} holds={(s) => rowKey(s) === activeKey} render={row} />
    </Section>
  );
}

// A section's "+" (a new conversation, a new agent): quiet, next to the section's title.
/** A small button at the end of a section's heading: "+" (add), or another page of the section (`icon`). */
/** A section title's own action as an icon (New chat on Persona chats, New group…); `active`: the page it opens is open. */
