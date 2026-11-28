// The feature modules (docs/web/web.md "Modules"): registered before any route is read. Their descriptors only; each
// module's code and strings load when its page first opens.
import "./modules";
import { Suspense, useCallback, useEffect, useId, useRef, useState } from "react";
import { api, ApiError, errorText, getToken, handleUnauthorized, setToken } from "./lib/api";
import { pair, takeToken } from "./lib/auth";
import { useCloseOnBack } from "./lib/back-close";
import { SCRIM_FADE, useLingers } from "./lib/lingers";
import { PageBoundary } from "./views/PageBoundary";
import { PerAssistant } from "./views/PerAssistant";
import { lazyDialog, OverlayBoundary, useOpened } from "./views/LazyOverlay";
import { navigationCrumb } from "./lib/error-reports";
import { lazyPage, preloadPages } from "./lib/lazy-page";
import type { ToastLiftProps } from "./ui/toast-lift";
import { drawerA11y, focusBack, useDrawerDrag } from "./lib/drawer";
import { acceptHubs, here, rememberHub, takeHubs } from "./lib/hubs";
import { holdImportLink, takeImportLink } from "./lib/import-link";
import { stripBase } from "./lib/base";
import { useSessionPanels } from "./lib/panel-memory";
import { paneRoute, resolveTab, tabOfSessionLink, tabPagePane } from "./lib/herdr-tree";
import { askTeamWorker, isWorker } from "./lib/team";
import { foldsUnderMachine } from "./lib/child-sessions";
import { foldsUnder, isSide, sideFoldOn } from "./lib/side-fold";
import { askSide } from "./lib/side-panel";
import { resolveSessionLink, sessionLinkRoute, type SessionLink } from "./lib/session-link";
import { engineSwitchHome } from "./lib/engine-switch";
import { askSubagentPanel, followChildSession, splitSubagentLink } from "./lib/subagent-link";
import { href, inWorkView, navigate, onLinkClick, type Route, routePath, useRoute, withWorkView } from "./lib/router";
import { agentChatKey, noteChatShown, personaChatKey } from "./lib/recent-chats";
import { ago, sessionTitle, shortPath } from "./lib/format";
import type { Session } from "./lib/types";
import { useTabLookup } from "./lib/tab-lookup";
import { ConnectionPill } from "./views/ConnectionPill";
import { NARROW, useKeyboardFrame, useMedia } from "./lib/viewport";
import { usePhoneNavMode } from "./lib/phone-nav";
import { connect, disconnect, getState, reset, subscribe, type State, useHubUnreachable, useStore } from "./lib/store";
import { watchRoster } from "./lib/managers";
import { t } from "./i18n";
import { Button, Collapse, cx, EASE, IconButton, Loading, Spinner, Toasts } from "./ui/atoms";
import { focusables, trapTabIn } from "./ui/overlay";
import { Empty } from "./views/session/files/empty";
import { AgentsPage } from "./views/AgentsPage";
import { ChatView } from "./views/chat/ChatView";
import { preloadCloseout } from "./views/chat/closeout-chunks";
import { Home } from "./views/Home";
import { PersonaAssistant } from "./views/PersonaAssistant";
import { NewWorktreeHost } from "./views/NewWorktreeHost";
import { CloseAgentHost } from "./views/CloseAgentHost";
import { WorktreeActionsHost } from "./views/WorktreeActions";
import { openWorktreeActions } from "./lib/worktree-actions";
import { selectWorkspace } from "./lib/workspace-selection";
import { setAgentGrouping } from "./lib/sidebar-api";
import type { WorkspaceTarget } from "./lib/workspace-target";
import { ForwardHost } from "./views/chat/ForwardHost";
import { onOpenNewAgent, type NewAgentPreset } from "./lib/new-agent";
import { closeHelp, useHelpOpen } from "./lib/shortcut-help";
import { closePalette, openPalette, usePalette } from "./lib/palette";
import { sidebarRail, sidebarShown, useLayout } from "./lib/layout";
import { useMacChromeMode } from "./lib/mac-chrome";
import { useSidebarTransition, type SidebarMode } from "./lib/sidebar-transition";
import { MacBayTools, MacMainContour, type MacContourDriver } from "./views/MacChrome";
import { SIDEBAR_LABEL, useShortcuts } from "./lib/use-shortcuts";
import { Sidebar, SidebarRail } from "./views/Sidebar";
import { SidebarResize } from "./views/SidebarResize";
import { SidebarRowHoverHost } from "./views/SidebarRowHover";
import { closePanel, usePanel } from "./lib/panel";
import { ExtensionNavPage } from "./views/extensions/ExtensionSlots";
import { useExtensionPlace } from "./lib/extension-slots";
import { AttentionSignals } from "./lib/attention-signals";
import { clearDesktopIsland, DesktopIslandSignals } from "./lib/desktop-island";
import { TextLookupHost } from "./text-lookup/host";
import { openStartPage } from "./lib/start-page";
import { conversationRoute } from "./lib/conversation-page";
import { useTrackTitle } from "./lib/tracks";
import { healthReadOnly } from "./lib/health-archive";
import { connectModuleHub, moduleExtensionAt, moduleTitle, useModuleStates } from "./core/module-host";
import { ModuleExtensionPage, ModulePage } from "./core/ModulePage";

// The feature modules follow the hub's hello (its features say which it serves): docs/web/web.md "Modules".
connectModuleHub(getState, subscribe);

// The pages a first screen doesn't need load when they are opened (#1996), or as a link to their route is reached
// (lib/lazy-page.ts); "Chat", Persona and "Overview" stay in the main bundle (the extensions' page too: the sidebar already has
// ExtensionSlots for its icons).
// The sign-in page is only for a browser that isn't signed in yet (#4603): every load after that would carry it on the
// first screen for nothing. Its chunk (and its words, the login namespace) loads when it is drawn.
const Login = lazyPage(() => import("./views/Login").then((m) => m.Login));
const AgentsView = lazyPage(() => import("./views/agents/AgentsView").then((m) => m.AgentsView), { routes: ["agents"] });
const AgentArchives = lazyPage(() => import("./views/agents/AgentArchives").then((m) => m.AgentArchives), { routes: ["agent-archives"] });
const CompareRuns = lazyPage(() => import("./views/CompareRuns").then((m) => m.CompareRuns), { routes: ["compare"] });
const RequestPage = lazyPage(() => import("./views/RequestPage").then((m) => m.RequestPage), { routes: ["request"] });
const WorkspaceBoard = lazyPage(() => import("./views/WorkspaceBoard").then((m) => m.WorkspaceBoard), { routes: ["workspace-board"] });
const Machines = lazyPage(() => import("./views/Machines").then((m) => m.Machines), { routes: ["machines", "machine"] });
const TerminalPage = lazyPage(() => import("./views/terminal/TerminalPage").then((m) => m.TerminalPage), { routes: ["terminal"] });
const LarkOAuthReturn = lazyPage(() => import("./views/settings/LarkOAuthReturn").then((m) => m.LarkOAuthReturn), { routes: ["lark-oauth"] });
const SubscriptionsPage = lazyPage(() => import("./views/subscriptions/SubscriptionsPage").then((m) => m.SubscriptionsPage), { routes: ["subscriptions"] });
const SpacePage = lazyPage(() => import("./views/SpacePage").then((m) => m.SpacePage), { routes: ["space"] });
const Settings = lazyPage(() => import("./views/Settings").then((m) => m.Settings), { routes: ["settings"] });
const SpaceExtensionEntry = lazyPage(() => import("./views/spaces/SpaceExtensionEntry").then((m) => m.SpaceExtensionEntry), { routes: ["track", "track-new"] });
const Space = lazyPage(() => import("./views/spaces/Space").then((m) => m.Space), { routes: ["track"] });
const History = lazyPage(() => import("./views/assistant/History").then((m) => m.History), { routes: ["history"] });
const Profile = lazyPage(() => import("./views/assistant/Profile").then((m) => m.Profile), { routes: ["profile"] });
const Routines = lazyPage(() => import("./views/assistant/Routines").then((m) => m.Routines), { routes: ["routines"] });
const Inbox = lazyPage(() => import("./views/inbox/Inbox").then((m) => m.Inbox), { routes: ["inbox"] });
const StarredMessages = lazyPage(() => import("./views/StarredMessages").then((m) => m.StarredMessages), { routes: ["starred"] });
const Knowledge = lazyPage(() => import("./views/knowledge/Knowledge").then((m) => m.Knowledge), { routes: ["kb"] });
const TodosView = lazyPage(() => import("./views/Todos").then((m) => m.TodosView), { routes: ["todos"] });
// an address that is no page (lib/router.ts "not-found"): loaded only by one
const NotFound = lazyPage(() => import("./views/NotFound").then((m) => m.NotFound));
// Keep the shortcut palette warm. The new-agent dialog loads only when opened.
// A closed side panel is not part of startup. Keep it mounted after first use so its hooks preserve focus and layout.
const SidePanel = lazyPage(() => import("./views/SidePanel").then((m) => m.SidePanel));
function SidePanelHost() {
  const open = Boolean(usePanel());
  if (!useOpened(open)) return null;
  return (
    <OverlayBoundary open={open} onClose={closePanel}>
      <Suspense fallback={open && <div className="fixed right-4 top-4 z-40"><Loading /></div>}>
        <SidePanel />
      </Suspense>
    </OverlayBoundary>
  );
}
const Palette = lazyPage(() => import("./views/Palette").then((m) => m.Palette), { idle: true });
// Where the toasts sit on a narrow screen (ui/toast-lift.tsx): off the first screen and out of the idle tier, which a
// wide screen loads too; Shell loads it once the screen is narrow. A load that fails leaves the toasts at the bottom of
// the page until the next toast tries again.
const ToastLift = lazyPage(() => import("./ui/toast-lift").then((m) => m.ToastLift));
// the phone tab bar (lib/phone-nav.ts) loads only where it is drawn: a narrow screen that chose it
const PhoneTabBar = lazyPage(() => import("./views/PhoneTabBar").then((m) => m.PhoneTabBar));
function PhoneToasts(props: ToastLiftProps) {
  return (
    <OverlayBoundary open onClose={() => {}}>
      <ToastLift {...props} />
    </OverlayBoundary>
  );
}
const NewAgent = lazyPage(() => import("./views/NewAgent").then((m) => m.NewAgent));
const StartingAgent = lazyPage(() => import("./views/StartingAgent").then((m) => m.StartingAgent));
const ShortcutHelpDialog = lazyDialog(() => import("./views/ShortcutHelp").then((m) => m.ShortcutHelp));
// The keyboard opener stays in the eager shell even before this dialog's first load.
function ShortcutHelp() {
  return <ShortcutHelpDialog open={useHelpOpen()} onClose={closeHelp} />;
}

// A link with the hub's token (`?token=…` or `#token=…`) signs in without the form. The token leaves the address bar
// before anything else runs (history, bookmarks, a copied link), and is traded for this browser's own one.
const linked = typeof window === "undefined" ? null : takeToken(window.location.href);
if (linked) window.history.replaceState(window.history.state, "", linked.rest);

// Switching from another hub's page hands its list of hubs over in the fragment (`#hubs=`, lib/hubs.ts): take it into
// this browser's list and out of the address bar the same way.
const carried = typeof window === "undefined" ? null : takeHubs(window.location.href);
if (carried) {
  acceptHubs(carried.hubs, carried.removed);
  window.history.replaceState(window.history.state, "", carried.rest);
}

// An opening at the root address shows the start page the user chose (lib/start-page.ts), before the first paint.
if (typeof window !== "undefined") openStartPage();

// A provider import link (`/settings?tab=models#import=…`, #5420) left the address bar before anything loaded (main.tsx,
// lib/import-link.ts says why). Pasted into the address bar of a page already open, only the fragment changes and
// nothing loads: it is taken then, the rest of the fragment with it (this page is signed in: nothing reads it now).
function takeImportLinkHere() {
  const importing = takeImportLink(window.location.href);
  if (!importing) return;
  // the fragment goes first: navigate does nothing for an address that differs only in it, and on an open page another
  // address goes through the router, or the page the link was pasted over would stay
  window.history.replaceState(window.history.state, "", location.pathname + location.search);
  navigate(stripBase(importing.rest.replace(/#.*$/, "")), true);
  holdImportLink(importing.text);
}
if (typeof window !== "undefined") window.addEventListener("hashchange", takeImportLinkHere);

const workspaceActions = { onWorkspaceMenu: openWorktreeActions };
function openWorkspaceInTree(target: WorkspaceTarget) {
  selectWorkspace(target);
  setAgentGrouping("repo");
  navigate({ name: "agent-list" });
}

// Developing Persona's To-dos view shadow (features/todos/model/view-shadow.ts) reads today's triage through this
// module: its recorder can load before App, so importing lib/todo-triage.ts there would split it out of the first
// screen's chunk (docs/lessons/web.md #10226). The cost is development only: under `npm run dev` an edit to
// lib/todo-triage.ts changes App's exports, so the page reloads where it used to refresh in place.
export { openMergePRs, supersededFailedStarts, triage } from "./lib/todo-triage";

export function App() {
  const [authed, setAuthed] = useState(() => !linked?.token && Boolean(getToken()));
  const [joining, setJoining] = useState(() => Boolean(linked?.token));
  const [linkError, setLinkError] = useState("");

  useEffect(() => {
    if (!linked?.token) return;
    let live = true;
    // Already signed in here (the link opened again): keep this browser's token instead of minting another one.
    const signedIn = getToken() ? api("/machines").then(() => true, () => false) : Promise.resolve(false);
    signedIn
      .then((ok) => (ok ? undefined : pair(linked.token)))
      .then(() => live && setAuthed(true))
      .catch((err) => {
        if (!live) return;
        const msg = errorText(err);
        const invalid = (err instanceof ApiError && err.status === 401) || msg === "invalid token";
        setLinkError(invalid ? t("app.link_token_invalid") : msg);
        setAuthed(Boolean(getToken())); // still signed in from before: stay in
      })
      .finally(() => live && setJoining(false));
    return () => {
      live = false;
    };
  }, []);

  useEffect(() => {
    handleUnauthorized(() => {
      setToken("");
      reset();
      // and the notch island's agents (#7669), as signing out clears them: not left to the shell's unmount
      clearDesktopIsland();
      // say why the sign-in page is back: this browser's credential stopped working (revoked, or the hub forgot it)
      setLinkError(t("app.signed_out"));
      setAuthed(false);
    });
  }, []);

  useEffect(() => {
    if (!authed) return;
    connect();
    // the managers' roster from the start (docs/assistant/managers.md "Clients"): what goes by the default waits for it
    watchRoster();
    return () => disconnect();
  }, [authed]);

  return (
    <>
      {joining ? (
        <Joining />
      ) : authed ? (
        <Shell />
      ) : (
        <PageBoundary page="login">
          <Suspense fallback={<PageLoading />}>
            <Login error={linkError} onDone={() => setAuthed(true)} />
          </Suspense>
        </PageBoundary>
      )}
      <Toasts lift={PhoneToasts} />
    </>
  );
}

// A page whose chunk is still on its way: shown only if that takes a moment, so a quick load does not flash it.
function PageLoading() {
  return (
    <div className="flex h-full items-center justify-center" style={{ animation: "fade-in 150ms ease-out 250ms both" }}>
      <Loading />
    </div>
  );
}

// A machine's sessions as a `/s/…` or `/t/…` page looks them up: the complete rows, then the summaries not among them.
function pageRows(s: State, mid: string): Session[] {
  const complete = s.sessions[mid] ?? [];
  const details = new Set(complete.map((row) => row.session_id));
  return [...complete, ...s.sessionSummaries.filter((row) => row.machine_id === mid && !details.has(row.session_id))];
}

/** The browser tab's name for an Agent's page: the session's title as the page's header has it (sessionTitle), so
 *  several open tabs, the history and bookmarks tell them apart; "" until its row is in. */
function paneTitle(s: State, mid: string, sid: string): string {
  const row = (s.sessions[mid] ?? []).find((r) => r.session_id === sid) ?? s.sessionSummaries.find((r) => r.machine_id === mid && r.session_id === sid);
  return row ? sessionTitle(row) : "";
}

/** What a `/s/…` or `/t/…` page opens, settled from its machine's rows. Only plain values: a push for another session on
 *  the machine leaves it equal (useStore's shallow compare), so the shell and everything it draws are left alone. */
interface PageTarget {
  /** How a `/s/…` link's id settled (lib/session-link.ts); undefined on a `/t/…` page. */
  link?: SessionLink["kind"];
  /** Where the link really goes, replacing it in place (Back doesn't come here again); "" when it stays. */
  to: string;
  /** The page's machine: a tab's panes are all on it. */
  mid: string;
  /** The conversation shown; undefined while there is none to show. */
  sid?: string;
  /** The link is a child session's (#5120): the session it folds under, whose chat `to` opens at its card. */
  parent?: string;
  /** The link is a lead's worker's (#5180): the worker, which the lead's page (`to`, `parent` the lead) shows in its Team panel. */
  worker?: string;
  /** The link is a side session's, or a row's that belongs to one (#7944): the side, which its parent's page (`to`,
   *  `parent`) shows in its Side sessions panel; `child`, the row of the side's own that the link named. */
  side?: string;
  child?: string;
  /** The link is a subagent's (`<parent>@<agent>` or folded child session): opens the parent page + subagent detail panel. */
  subagent?: { mid: string; sid: string };
}

/** A side session's link (#7944), or a link to a row of a side's own (`folded_side`): the parent's page, its Side
 *  sessions panel on the side; undefined when `row` is neither. */
function sideTarget(rows: Session[], mid: string, row: Session | undefined, view: boolean): PageTarget | undefined {
  if (!row) return undefined;
  const side = isSide(row) ? row.session_id : sideFoldOn() && row.folded_side ? row.folded_side : undefined;
  const parent = side ? row.folded_under : undefined;
  if (!side || !parent || parent === row.session_id) return undefined;
  const parentRow = rows.find((r) => r.session_id === parent);
  const to = routePath(withWorkView(parentRow ? paneRoute(parentRow) : { name: "session", mid, sid: parent }, view));
  return { link: undefined, to, mid, sid: undefined, parent, side, child: side === row.session_id ? undefined : row.session_id };
}

/** A worker's link (#5180): the lead's page, its Team panel on the worker; undefined when `row` is not a worker. */
function workerTarget(rows: Session[], mid: string, row: Session | undefined, view: boolean): PageTarget | undefined {
  const lead = row && isWorker(row) && row.folded_under && row.folded_under !== row.session_id ? row.folded_under : undefined;
  if (!lead || !row) return undefined;
  const leadRow = rows.find((r) => r.session_id === lead);
  const to = routePath(withWorkView(leadRow ? paneRoute(leadRow) : { name: "session", mid, sid: lead }, view));
  return { link: undefined, to, mid, sid: undefined, parent: lead, worker: row.session_id };
}

function pageTarget(s: State, route: Route): PageTarget | undefined {
  if (route.name === "tab") {
    // the pane a tab opens on: the asked-for one, its session, or the tab's most urgent (lib/herdr-tree.ts); a worker's
    // is its lead's page, the worker in its Team panel (#5180)
    const rows = pageRows(s, route.mid);
    const at = resolveTab(rows, route);
    // a link naming a worker: the lead's chat at once (the tab is the lead's too), its panel on the worker
    const named = at && route.sid === at.session_id ? workerTarget(rows, route.mid, at, inWorkView(route)) : undefined;
    if (named) return { ...named, sid: named.parent };
    // a tab opened without a pane whose most urgent one is a worker: its lead, the panel left as the lead's page has it
    const lead = at && isWorker(at) && !named ? rows.find((r) => r.session_id === at.folded_under) : undefined;
    const pane = tabPagePane(lead ?? at, route, s.loaded);
    return { link: undefined, to: "", mid: route.mid, sid: pane?.sid };
  }
  if (route.name !== "session") return undefined;
  // A compact card, explicit standalone page ("Open as page"), or message link opens this conversation directly.
  const exact = (): PageTarget => ({ link: "exact", to: "", mid: route.mid, sid: route.sid });
  // An outbox refusal names the pane to open, even when its chat normally redirects to Assistant or a parent.
  if (route.standalone && route.view === "term") return exact();
  // Deep link to an embedded subagent (`<parent>@<agent>`): open its parent session + subagent detail panel (#14323).
  const sub = splitSubagentLink(route.sid);
  if (sub) {
    if (route.at || route.standalone) return exact();
    const parentRow = pageRows(s, route.mid).find((r) => r.session_id === sub.parent);
    const dest = parentRow?.assistant
      ? conversationRoute(route.mid, sub.parent, s.sessions, s.assistant, parentRow)
      : { ...route, sid: sub.parent };
    return { link: undefined, to: routePath(dest), mid: route.mid, sid: undefined, parent: sub.parent, subagent: { mid: route.mid, sid: route.sid } };
  }
  const rows = pageRows(s, route.mid);
  const row = rows.find((r) => r.session_id === route.sid);
  // a Persona conversation's `/s/…` link (a push, a copied address): its own page, which draws its plan cards, questions
  // and tasks; the agent chat here shows none of them
  if (row?.assistant && !route.at) return { link: undefined, to: routePath(conversationRoute(route.mid, route.sid, s.sessions, s.assistant, row)), mid: route.mid, sid: undefined };
  // a lead's worker's (#5180): the lead's page, the worker in its Team panel
  const worker = workerTarget(rows, route.mid, row, inWorkView(route));
  if (worker && !route.standalone) return worker;
  // a side session's (#7944): its parent's page, the side in its Side sessions panel
  const side = sideTarget(rows, route.mid, row, inWorkView(route));
  if (side && !route.standalone) return route.at && side.child ? exact() : side;
  // a child session's (#5120, #14323): open its parent's page + subagent detail panel unless opened standalone
  const parent = row ? foldsUnder(row) : undefined;
  const parentMid = row ? foldsUnderMachine(row) : route.mid;
  if (parent && !(parentMid === route.mid && parent === route.sid)) {
    if (route.at || route.standalone) return exact();
    const parentRow = pageRows(s, parentMid).find((r) => r.session_id === parent);
    const dest = parentRow?.assistant
      ? conversationRoute(parentMid, parent, s.sessions, s.assistant, parentRow)
      : withWorkView({ name: "session", mid: parentMid, sid: parent }, inWorkView(route));
    return { link: undefined, to: routePath(dest), mid: parentMid, sid: undefined, parent, subagent: { mid: route.mid, sid: route.sid } };
  }
  // an earlier session of a conversation that moved between Claude Code and Codex (#5054): the conversation's own row,
  // whose chat reads that session's stretch in its place
  const home = engineSwitchHome(rows, route.sid);
  if (home) return route.at ? exact() : { link: undefined, to: routePath(withWorkView({ name: "session", mid: route.mid, sid: home.session_id }, inWorkView(route))), mid: route.mid, sid: undefined };
  // an old `/s/…` link of a pane in a tab becomes that tab's `/t/…`
  const tab = tabOfSessionLink(rows, route);
  // a `/s/…` link with only the front of an id (the 8-character ones shown everywhere, #2464): the whole id's link;
  // several sessions starting with it are listed to pick from (SessionChoices)
  const link = resolveSessionLink(rows, route.mid, route.sid, s.loaded);
  const resolved = tab ?? (link.kind === "prefix" ? sessionLinkRoute(rows, link.session, route.view) : undefined);
  const landing = resolved && (resolved.name === "session" || resolved.name === "tab") && route.at ? { ...resolved, at: route.at } : resolved;
  const to = landing ? routePath(withWorkView(landing, inWorkView(route))) : "";
  const shown = link.kind === "exact" || link.kind === "unlisted";
  return { link: link.kind, to, mid: route.mid, sid: shown ? route.sid : undefined };
}

function Shell() {
  // a module switched on or off, or the hub's features changing what it serves, routes the address again
  useModuleStates();
  const route = useRoute();
  // an error report's breadcrumb: the kind of page only (its name), never the ids or the query in its address
  useEffect(() => navigationCrumb(route.name), [route.name]);
  const conn = useStore((s) => s.conn);
  // this hub goes on the list of hubs to switch between, under the name it gives itself
  const hubName = useStore((s) => s.hubName);
  useEffect(() => void rememberHub(here(), hubName), [hubName]);
  // the conversation on the page: a session's (`/s/…`), or the pane a tab (`/t/…`) opens on
  const target = useStore((s) => pageTarget(s, route));
  const loaded = useStore((s) => s.loaded);
  const tabLinkPath = target?.to ?? "";
  const linkAt = route.name === "session" || route.name === "tab" ? route.at : undefined;
  const linkParent = target?.parent;
  const linkWorker = target?.worker;
  const linkSide = target?.side;
  const linkChild = target?.child;
  const linkSubagentMid = target?.subagent?.mid;
  const linkSubagentSid = target?.subagent?.sid;
  const targetMid = target?.mid ?? "";
  useEffect(() => {
    if (!tabLinkPath) return;
    if (linkWorker && linkParent) askTeamWorker(targetMid, linkParent, linkWorker, linkAt);
    else if (linkSide && linkParent) {
      askSide(targetMid, linkParent, linkSide, linkAt);
      // a row of the side's own (its Codex child): the side's chat opens at its card
      if (linkChild) followChildSession(targetMid, linkChild, linkSide);
    } else if (linkSubagentMid && linkSubagentSid && linkParent) {
      askSubagentPanel(targetMid, linkParent, { mid: linkSubagentMid, sid: linkSubagentSid, from: "link" });
    }
    navigate(tabLinkPath, true);
  }, [tabLinkPath, linkAt, linkParent, linkWorker, linkSide, linkChild, linkSubagentMid, linkSubagentSid, targetMid]);
  // a tab not among the page's rows (a paged hub lists only active and pinned ones): asked of the hub before it is gone
  const tabRoute = route.name === "tab" ? route : undefined;
  const lookup = useTabLookup(tabRoute, !!tabRoute && loaded && !target?.sid);
  const found = lookup.state === "found" ? { mid: lookup.mid, sid: lookup.sid } : undefined;
  const pane = target?.sid ? { mid: target.mid, sid: target.sid } : found;
  const launch = useStore((s) => (pane ? s.launches[pane.sid] : undefined));
  const starting = useStore((s) => (pane ? (s.sessions[pane.mid] ?? []).find((session) => session.session_id === pane.sid)?.starting : undefined));
  const agentTitle = useStore((s) => (pane ? paneTitle(s, pane.mid, pane.sid) : ""));
  const shell = useRef<HTMLDivElement>(null);
  useKeyboardFrame(shell);
  // a tab opened without a pane settles on the one it picked: another pane needing you later does not swap the page
  const picked = route.name === "tab" && !route.sid ? pane?.sid : undefined;
  useEffect(() => {
    if (picked && route.name === "tab") navigate({ ...route, sid: picked }, true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [picked]);
  const palette = usePalette();
  const layout = useLayout();
  const [newAgent, setNewAgent] = useState(false);
  const [preset, setPreset] = useState<NewAgentPreset | null>(null);
  useEffect(() => preloadPages(), []);
  useEffect(() => preloadCloseout(), []);
  useEffect(() => onOpenNewAgent((p) => {
    setPreset(p);
    setNewAgent(true);
  }), []);
  const openNewAgent = useCallback(() => setNewAgent(true), []);
  useEffect(() => {
    if (newAgent) void StartingAgent.preload().catch(() => {});
  }, [newAgent]);
  const closeNewAgent = useCallback(() => {
    setNewAgent(false);
    setPreset(null);
  }, []);
  const paletteOpened = useOpened(palette.open);
  const newAgentOpened = useOpened(newAgent);

  // ── the sidebar: a column on a wide screen, a drawer over the page on a narrow one (#1996) ──
  const narrow = useMedia(NARROW);
  const phoneNav = usePhoneNavMode();
  // the toasts' placement, in before the first toast in practice
  useEffect(() => {
    if (narrow) void ToastLift.preload().catch(() => {});
  }, [narrow]);
  const [drawer, setDrawer] = useState(false);
  const toggle = useCallback(() => setDrawer((d) => !d), []);
  const close = useCallback(() => setDrawer(false), []);
  // The scrim leaves the page once it has faded out (#2102): kept there at opacity 0, iOS took its colour (the page's
  // under 20% black) for the status bar strip of the installed app.
  const scrimShown = useLingers(drawer, SCRIM_FADE);
  const drawerBox = useRef<HTMLDivElement>(null);
  const sidebarId = useId();
  const scrim = useRef<HTMLDivElement>(null);
  const mainBox = useRef<HTMLElement | null>(null);
  const [mainEl, setMainEl] = useState<HTMLElement | null>(null);
  const setMainRef = useCallback((el: HTMLElement | null) => {
    mainBox.current = el;
    setMainEl(el);
  }, []);
  const a11y = drawerA11y(narrow, drawer);
  // folded to its icons on a wide screen (⌘⌥B, kept in this browser); a narrow screen keeps the full drawer
  const rail = !narrow && sidebarRail(layout);
  const macChrome = useMacChromeMode();
  const macWindowed = macChrome === "windowed" && !narrow;
  const sidebarMode: SidebarMode = rail ? "rail" : sidebarShown(layout) ? "expanded" : "hidden";
  const macBay = macWindowed ? sidebarMode : undefined;
  const fullLayerRef = useRef<HTMLDivElement>(null);
  const railLayerRef = useRef<HTMLDivElement>(null);
  const contourDriverRef = useRef<MacContourDriver>(null);
  const { animating: sidebarAnimating, leaving: leavingSidebar } = useSidebarTransition({
    shellRef: shell,
    columnRef: drawerBox,
    mainEl,
    fullLayerRef,
    railLayerRef,
    contourDriverRef,
    mode: sidebarMode,
    narrow,
    macWindowed,
  });
  useEffect(() => {
    if (!narrow) setDrawer(false);
  }, [narrow]);
  // dragged out and back with a finger anywhere on the page or the drawer (not on a full-screen side panel)
  useDrawerDrag({
    root: shell,
    drawer: drawerBox,
    scrim,
    open: drawer,
    setOpen: setDrawer,
    enabled: narrow,
    scope: (t) => [mainBox, drawerBox, scrim].some((r) => r.current?.contains(t)),
  });
  // Back closes it first
  useCloseOnBack(drawer, close, narrow);
  // Its Search hands over to the palette: closed in the same commit, the drawer's history entry becomes the palette's
  // (lib/back-close.ts), so one Back closes the palette, and a pick's page replaces it with no copy of this page left under.
  // (A wide screen has no drawer open to close.)
  const searchFromSidebar = useCallback(() => {
    close();
    openPalette("jump");
  }, [close]);
  // Esc closes it (a menu or a dialog over it closes first); Tab stays in it, like a dialog's
  useEffect(() => {
    if (!a11y.modal) return;
    const key = (e: KeyboardEvent) => {
      if (document.querySelector("[role=menu]") || document.querySelectorAll("[role=dialog]").length > 1) return;
      if (e.key === "Tab" && drawerBox.current) {
        const list = focusables(drawerBox.current);
        const to = trapTabIn(drawerBox.current, list, e.shiftKey);
        if (to === null) return;
        e.preventDefault();
        list[to]?.focus();
        return;
      }
      if (e.key !== "Escape") return;
      e.preventDefault();
      e.stopPropagation();
      close();
    };
    addEventListener("keydown", key, true);
    return () => removeEventListener("keydown", key, true);
  }, [a11y.modal, close]);
  // open, focus moves into it; shut, back to what opened it
  const opener = useRef<HTMLElement | null>(null);
  useEffect(() => {
    const box = drawerBox.current;
    if (!box || !narrow) return;
    if (drawer) {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      focusables(box)[0]?.focus({ preventScroll: true });
      return;
    }
    const back = focusBack(opener.current, document.activeElement, box.contains(document.activeElement), document.body);
    opener.current = null;
    back?.focus({ preventScroll: true });
  }, [drawer, narrow]);

  // the keyboard shortcuts (⌘K and the rest, lib/shortcuts.ts): what they do here, and the listener that runs them
  const workPane = useRef<{ mid: string; sid: string } | undefined>(undefined);
  const reportWorkPane = useCallback((available?: { mid: string; sid: string }) => { workPane.current = available; }, []);
  const workPaneAvailable = (at: { mid: string; sid: string }) => workPane.current?.mid === at.mid && workPane.current?.sid === at.sid;
  useShortcuts({ route, pane, narrow, toggleDrawer: toggle, closeDrawer: close, workPaneAvailable });

  useEffect(() => setDrawer(false), [route.name, pane ? pane.sid : route.name === "persona" ? route.sid : route.name === "agents" ? route.id : ""]);
  // a session's files / Git belong to that conversation: moving to another page closes them (a link and a task stay),
  // and coming back to it opens them again where they were (#5150, lib/panel-memory.ts); a Persona chat's panel too
  // (#6199)
  const chat = route.name === "persona" ? (route.sid ?? "") : undefined;
  // the conversation on the page goes first among the recently opened ones, ⌘K's first group (#2191): an Agent's, or a
  // Persona conversation (the main one when the page names none); any other page shows none
  const personaMain = useStore((s) => s.assistant?.main ?? "");
  const personaShown = route.name === "persona" ? (route.sid || personaMain) : "";
  const shownChat = pane ? agentChatKey(pane.mid, pane.sid) : personaShown ? personaChatKey(personaShown) : "";
  useEffect(() => noteChatShown(shownChat), [shownChat]);
  const at = pane ? `${pane.mid}/${pane.sid}` : chat !== undefined ? `persona/${chat}` : route.name;
  useSessionPanels(at, pane, chat);

  const trackTitle = useTrackTitle(route.name === "track" ? route.slug : "");
  const extensionTitle = useExtensionPlace(route.name === "extension" ? route.extension : "", route.name === "extension" ? route.ui : "")?.title;
  const title =
    inWorkView(route) || route.name === "terminal" ? t("work-entry.title") : pane ? agentTitle || "Agent" : route.name === "persona" ? "Persona" : route.name === "persona-new" ? t("app.title_new_chat") : route.name === "history" ? t("app.title_history") : route.name === "module" ? moduleTitle(route) ?? t("app.title_home") : route.name === "track" ? trackTitle || t("app.title_space") : route.name === "track-new" ? t("app.title_new_space") : route.name === "profile" ? t("app.title_profile") : route.name === "inbox" ? t("app.title_inbox") : route.name === "starred" ? t("starred.title")
      : route.name === "request" ? t("app.title_request") : route.name === "routines" ? t("app.title_routines") : route.name === "kb" ? t("app.title_kb") : route.name === "workspace-board" ? t("app.title_workspace_board") : route.name === "todos" ? t("app.title_todos") : route.name === "machines" || route.name === "machine" ? t("app.title_machines") : route.name === "space" ? route.space : route.name === "settings" || route.name === "lark-oauth" ? t("app.title_settings")
      : route.name === "agents" ? t("app.title_agents") : route.name === "agent-list" ? "Agents" : route.name === "agent-archives" ? t("app.title_agent_archives") : route.name === "compare" ? t("app.title_compare_runs") : route.name === "extension" ? extensionTitle || t("app.title_extension") : route.name === "subscriptions" ? t("app.title_subscriptions") : route.name === "not-found" ? t("app.title_not_found") : t("app.title_home");

  // never reached the hub: say so with a way to try again, instead of pages loading forever (kept through a retry); the
  // Agents list reads the same flag, so the two always agree
  const offline = useHubUnreachable();
  let main: React.ReactNode;
  if (offline) main = <Offline onToggleSidebar={toggle} trying={conn === "connecting"} />;
  else switch (route.name) {
    case "request":
      main = <RequestPage id={route.id} onToggleSidebar={toggle} />;
      break;
    case "session":
    case "tab": {
      const detail = (terminalAvailable?: boolean) =>
        route.name === "session" && target?.link === "ambiguous" ? <SessionChoices mid={route.mid} sid={route.sid} onToggleSidebar={toggle} />
          : !pane ? (loaded && route.name === "tab" && lookup.state === "missing" ? <TabGone onToggleSidebar={toggle} /> : <Loading />)
          : launch ? <StartingAgent launch={launch} onToggleSidebar={toggle} />
          : <ChatView key={`${pane.mid}/${pane.sid}`} mid={pane.mid} sid={pane.sid} starting={starting} onToggleSidebar={terminalAvailable === undefined ? toggle : undefined} inPage terminalAvailable={terminalAvailable} landAt={route.at} />;
      main = inWorkView(route)
        ? <TerminalPage onToggleSidebar={toggle} onNewAgent={openNewAgent} detail={{ selected: pane, render: detail }} onWorkPane={reportWorkPane} />
        : detail();
      break;
    }
    case "persona":
      main = <PersonaAssistant sid={route.sid} routed={route.mid || route.manager ? { mid: route.mid, manager: route.manager } : undefined} at={route.at} onToggleSidebar={toggle} />;
      break;
    case "persona-new":
      main = <PersonaAssistant fresh onToggleSidebar={toggle} />;
      break;
    case "history":
      main = <History origin={route.origin} onToggleSidebar={toggle} />;
      break;
    case "module":
      main = <ModulePage key={route.module} route={route} onToggleSidebar={toggle} />;
      break;
    case "track": {
      // Health's existing data stays in the default panel without its module (including a core-only build), rather
      // than depending on the optional Space Conversations extension or opening an editable new-conversation home.
      const archive = healthReadOnly(route.slug);
      main = route.sid || route.fresh || archive ? <Space key={`${route.slug}/${route.sid ?? "home"}`} slug={route.slug} sid={route.sid} fresh={route.fresh || archive} onToggleSidebar={toggle} /> : <SpaceExtensionEntry slug={route.slug} />;
      break;
    }
    case "track-new":
      main = <SpaceExtensionEntry fresh />;
      break;
    case "profile":
      main = <Profile onToggleSidebar={toggle} />;
      break;
    case "inbox":
      main = <Inbox id={route.id} manager={route.manager} onToggleSidebar={toggle} />;
      break;
    case "starred":
      main = <StarredMessages onToggleSidebar={toggle} />;
      break;
    case "routines":
      main = <Routines onToggleSidebar={toggle} />;
      break;
    case "kb":
      main = <Knowledge kind={route.kind} id={route.id} onToggleSidebar={toggle} />;
      break;
    case "workspace-board":
      main = <WorkspaceBoard onToggleSidebar={toggle} />;
      break;
    case "todos":
      main = <TodosView selectedId={route.id} directionId={route.direction} manager={route.manager} view={route.view} mode={route.mode} date={route.date} onToggleSidebar={toggle} />;
      break;
    case "agents":
      main = <AgentsView key={route.id ?? ""} id={route.id} onToggleSidebar={toggle} />;
      break;
    case "agent-list":
      main = <AgentsPage route={route} onToggleSidebar={toggle} onNewAgent={openNewAgent} workspaceActions={workspaceActions} />;
      break;
    case "agent-archives":
      main = <AgentArchives onToggleSidebar={toggle} />;
      break;
    case "compare":
      main = <CompareRuns id={route.id} onToggleSidebar={toggle} />;
      break;
    case "machine":
    case "machines":
      main = <Machines onToggleSidebar={toggle} />;
      break;
    case "terminal":
      main = <TerminalPage route={route} onToggleSidebar={toggle} onNewAgent={openNewAgent} />;
      break;
    case "space":
      main = <SpacePage key={`${route.mid}/${route.workspace ?? route.space}`} workspaceId={route.workspace} mid={route.mid} space={route.space} onToggleSidebar={toggle} />;
      break;
    case "settings":
      main = <Settings onToggleSidebar={toggle} />;
      break;
    case "subscriptions":
      main = <SubscriptionsPage onToggleSidebar={toggle} />;
      break;
    case "lark-oauth":
      main = <LarkOAuthReturn onToggleSidebar={toggle} />;
      break;
    case "extension":
      // a reserved extension-page id a feature module draws (the health module's water and protein pages, #13176)
      main = moduleExtensionAt(route.extension)
        ? <ModuleExtensionPage key={`${route.extension}/${route.ui}${location.search}`} extension={route.extension} ui={route.ui} onToggleSidebar={toggle} />
        : <ExtensionNavPage key={`${route.extension}/${route.ui}`} extension={route.extension} ui={route.ui} onToggleSidebar={toggle} />;
      break;
    case "not-found":
      main = <NotFound onToggleSidebar={toggle} />;
      break;
    default:
      main = <Home onNewAgent={openNewAgent} onToggleSidebar={toggle} />;
  }

  return (
    // #root is 100dvh (index.html); with a soft keyboard up (iOS) the shell is only the part above it (lib/viewport.ts)
    <div
      ref={shell}
      data-app-shell
      data-sidebar-hidden={!sidebarShown(layout) || undefined}
      data-mac-chrome={macChrome}
      data-mac-bay={macBay}
      className="flex h-full overflow-hidden bg-page"
    >
      {/* wide: a fixed column; narrow: a drawer over the page, clear of a landscape notch and the home indicator */}
      <div
        ref={drawerBox}
        id={sidebarId}
        data-sidebar-column
        role={a11y.modal ? "dialog" : undefined}
        aria-modal={a11y.modal || undefined}
        aria-label={a11y.modal ? SIDEBAR_LABEL : undefined}
        inert={a11y.inert || undefined}
        className={cx(
          "fixed inset-y-0 left-0 z-30 w-[284px] border-r border-line transition-transform duration-300 max-lg:w-[calc(284px+env(safe-area-inset-left))] max-lg:bg-canvas max-lg:pb-[env(safe-area-inset-bottom)] max-lg:pl-[env(safe-area-inset-left)] lg:relative lg:z-auto lg:translate-x-0 lg:shadow-none",
          drawer ? "translate-x-0 shadow-overlay" : "-translate-x-full",
          // ⌘⌥B folds it to the rail, ⌘⇧↵ hides it, on a wide screen; unfolded, its right edge drags (SidebarResize)
          rail ? "lg:w-14" : "lg:w-[var(--sidebar-w,284px)]",
          !sidebarShown(layout) && !sidebarAnimating && "lg:hidden",
        )}
        style={{ transitionTimingFunction: EASE }}
      >
        {(narrow || sidebarMode === "expanded" || leavingSidebar === "expanded") && (
          <div
            key="full"
            ref={fullLayerRef}
            inert={leavingSidebar === "expanded" || undefined}
            aria-hidden={leavingSidebar === "expanded" || undefined}
            className={cx(
              "h-full",
              leavingSidebar === "expanded"
                ? "pointer-events-none absolute inset-y-0 left-0 overflow-hidden"
                : sidebarAnimating
                  ? "overflow-hidden"
                  : "w-full",
            )}
            style={leavingSidebar === "expanded" || (sidebarAnimating && sidebarMode === "expanded") ? { width: "var(--sidebar-w, 284px)" } : undefined}
          >
            <Sidebar active={narrow ? drawer : sidebarMode === "expanded" || leavingSidebar === "expanded"} route={route} onSearch={searchFromSidebar} onNewAgent={openNewAgent} onNavigate={close} workspaceActions={workspaceActions} hideCollapse={macWindowed} />
          </div>
        )}
        {!narrow && (sidebarMode === "rail" || leavingSidebar === "rail") && (
          <div
            key="rail"
            ref={railLayerRef}
            inert={leavingSidebar === "rail" || undefined}
            aria-hidden={leavingSidebar === "rail" || undefined}
            className={cx(
              "h-full",
              leavingSidebar === "rail"
                ? "pointer-events-none absolute inset-y-0 left-0 w-14 overflow-hidden"
                : sidebarAnimating
                  ? "w-14 overflow-hidden"
                  : "w-full",
            )}
          >
            <SidebarRail route={route} onSearch={() => openPalette("jump")} onNewAgent={openNewAgent} hideCollapse={macWindowed} />
          </div>
        )}
        {!narrow && !rail && sidebarShown(layout) && <SidebarResize column={sidebarId} />}
      </div>
      {/* the scrim fades out as well as in; a drag sets its opacity as it goes (lib/drawer.ts) */}
      <div
        ref={scrim}
        aria-hidden
        className={cx("fixed inset-0 z-20 bg-black/20 transition-opacity duration-200 lg:hidden", drawer ? "opacity-100 starting:opacity-0" : "pointer-events-none opacity-0", !scrimShown && "hidden")}
        onClick={close}
      />
      <main ref={setMainRef} inert={a11y.modal || undefined} className="relative flex min-w-0 flex-1 flex-col">
        {macChrome !== undefined && <MacMainContour mainEl={mainEl} bay={macBay} animating={macWindowed && sidebarAnimating} contourRef={contourDriverRef} />}
        {/* dropped after loading: which phase, and a click explains with "Retry now" (views/ConnectionPill.tsx). A strip
            over the page that pushes it down while it shows, never a layer over its header, its fields or its title
            (web usability audit item 87); its details open over the page. */}
        <Collapse open={conn !== "online" && loaded && !offline} lazy unclipped className="shrink-0">
          <div className="flex justify-center px-4 pt-2 pb-1">
            <ConnectionPill />
          </div>
        </Collapse>
        <div className="relative min-h-0 flex-1">
          <PageBoundary page={href(route)}><Suspense fallback={<PageLoading />}><PerAssistant route={route}>{main}</PerAssistant></Suspense></PageBoundary>
        </div>
        {/* Settings › Advanced › Appearance › Phone navigation: Tab bar puts the four main places along the bottom (#16761) */}
        {narrow && phoneNav === "tabs" && <Suspense fallback={null}><PhoneTabBar route={route} /></Suspense>}
      </main>
      {macBay && <MacBayTools bay={macBay} onSearch={() => openPalette("jump")} />}
      <SidebarRowHoverHost />
      <SidePanelHost />
      {paletteOpened && (
        <OverlayBoundary open={palette.open} onClose={closePalette}>
          <Suspense fallback={null}>
            <Palette open={palette.open} onClose={closePalette} onNewAgent={openNewAgent} pane={pane} route={route} listShown={!narrow && !rail && sidebarShown(layout)} />
          </Suspense>
        </OverlayBoundary>
      )}
      <ShortcutHelp />
      {newAgentOpened && (
        <OverlayBoundary open={newAgent} onClose={closeNewAgent}>
          <Suspense fallback={null}>
            <NewAgent open={newAgent} preset={preset} onClose={closeNewAgent} />
          </Suspense>
        </OverlayBoundary>
      )}
      <NewWorktreeHost />
      <CloseAgentHost />
      <ForwardHost />
      <WorktreeActionsHost onOpenWorkspace={openWorkspaceInTree} />
      {/* tab title with the "Needs you" count, favicon badge, optional sounds (#2332): a leaf, so a sessions push re-renders
          only it, not the whole shell */}
      <AttentionSignals label={title} pane={pane} />
      {/* the counts for the desktop app's island at the MacBook notch (#7669), only in a desktop app that has it */}
      {window.personaDesktop?.setIslandState && <DesktopIslandSignals />}
      <TextLookupHost />
    </div>
  );
}

// The hub was never reached (offline, or it is down): an empty state that says so and tries again on a tap, instead of
// every page loading forever. The socket keeps retrying on its own as well; once in, the page comes back by itself.
function Offline({ onToggleSidebar, trying }: { onToggleSidebar: () => void; trying: boolean }) {
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("app.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
      </header>
      <div className="flex min-h-0 flex-1 flex-col pb-[10vh]" role="status">
        <Empty
          icon="hub"
          title={t("app.offline_title")}
          detail={t("app.offline_detail")}
          action={<Button tone="secondary" icon="retry" busy={trying} onClick={connect}>{trying ? t("app.connecting") : t("app.retry")}</Button>}
        />
      </div>
    </div>
  );
}

// A tab link whose tab is gone (closed, or herdr restarted with new ids) and no pane to fall back to.
function TabGone({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  return (
    <NoticePage onToggleSidebar={onToggleSidebar}>
      <p className="text-[13px] text-ink-2">{t("app.tab_gone")}</p>
    </NoticePage>
  );
}

// A notice in the middle of a page with no header of its own, under Offline's: the sidebar button where every page has
// it. A wide screen shows the sidebar itself, and the notice stays as it was there.
function NoticePage({ onToggleSidebar, children }: { onToggleSidebar: () => void; children: React.ReactNode }) {
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center px-3 sm:px-4 lg:hidden">
        <IconButton icon="sidebar" label={t("app.sidebar")} onClick={onToggleSidebar} />
      </header>
      <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 px-6 text-center">{children}</div>
    </div>
  );
}

// A `/s/…` link whose id prefix more than one session on the machine starts with (#2464): pick one, none is guessed.
function SessionChoices({ mid, sid, onToggleSidebar }: { mid: string; sid: string; onToggleSidebar: () => void }) {
  // read here, not in the shell: the list keeps up with the machine's pushes without drawing the whole app again
  const all = useStore((s) => pageRows(s, mid));
  const loaded = useStore((s) => s.loaded);
  const link = resolveSessionLink(all, mid, sid, loaded);
  const candidates = link.kind === "ambiguous" ? link.candidates : [];
  return (
    <NoticePage onToggleSidebar={onToggleSidebar}>
      <p className="text-[13px] text-ink-2">{t("app.session_choices", { count: candidates.length, sid })}</p>
      <ul className="flex w-full max-w-96 flex-col gap-1">
        {candidates.map((s) => {
          const to = sessionLinkRoute(all, s);
          return (
            <li key={s.session_id}>
              <a href={href(to)} onClick={(e) => onLinkClick(e, to)} className="flex flex-col items-start gap-0.5 rounded-control px-3 py-2 text-left hover:bg-hover">
                <span className="w-full truncate text-[13px] font-medium text-ink">{sessionTitle(s)}</span>
                <span className="w-full truncate font-mono text-[11.5px] text-ink-3">{s.session_id}</span>
                {(s.cwd || s.activity_at || s.updated_at) && (
                  <span className="w-full truncate text-[11.5px] text-ink-3">{[shortPath(s.cwd), ago(s.activity_at || s.updated_at)].filter(Boolean).join(" · ")}</span>
                )}
              </a>
            </li>
          );
        })}
      </ul>
    </NoticePage>
  );
}

// Signing in with the link's token: a moment, no form.
function Joining() {
  return (
    <div className="flex min-h-full items-center justify-center bg-page" role="status" aria-live="polite">
      <span className="flex items-center gap-2 text-[13px] text-ink-2">
        <Spinner />
        {t("app.joining")}
      </span>
    </div>
  );
}
