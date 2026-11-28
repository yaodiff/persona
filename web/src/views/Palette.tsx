import { conversationDeleted, conversationDeletionRevision, conversationDeletionScope } from "../lib/conversation-deleted";
import { shownManager, useShownManager } from "../lib/manager-choice";
// ⌘K: Beautiful UI's Search, over every agent and Persona conversation — and the terminal panes of the computers
// (lib/terminals.ts, the /terminal page's data). `>` first (or ⌘⇧P) is command mode: the registered commands with their
// shortcuts, and whatever other modules add through registerContextCommands (lib/palette.ts). docs/web/web.md"Keyboard shortcuts".
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { credentialOwner, subscribeCredentialOwner, enc, api } from "../lib/api";
import { machineWithHub } from "../lib/mesh";
import { createPortal } from "react-dom";
import { ago, sessionDisplayTab, sessionTitle, sessionTitleDetail, shortPath, activityOf, tabLabel } from "../lib/format";
import { useHerdrTabName } from "../lib/agents-display";
import { appCommands, commandKeys, contextCommands, filterCommands, inboxHits, matchPaletteWords, orderGroups, parseQuery, placeHits, openPaletteWith, setPaletteQuery, terminalHits, usePalette, type InboxHit, type MachinePanes, type PaletteCommand, type PaletteContext, type PlaceHit, type PlaceInput, type TerminalHit } from "../lib/palette";
import { isDone, itemRoute, useInbox, type InboxItem } from "../lib/inbox";
import { useNavPages, type NavPage } from "./Sidebar";
import type { EvidenceSnippet } from "../lib/agent-chatlist-filter";
import { storedValue } from "../lib/compatibility";
import { useScope } from "../lib/current-assistant";
import { SESSIONS_LIST_FEATURE } from "../lib/session-list";
import { supportsPagedAgents } from "../lib/runtime-list";
import { SESSIONS_TODO_FEATURE } from "../lib/session-groups";
import { GROUPING_KEY, pagedAgentList } from "../lib/sidebar-api";
import { filterAgentList } from "../lib/agent-list-filters";
import { conversationPage } from "../lib/conversation-page";
import { conversationTitle } from "./assistant/conversations";
import { href, navigate, type Route } from "../lib/router";
import { composing } from "../lib/send-history";
import { useAgentSessions } from "../lib/sessions";
import { PALETTE_RECENT_CHATS, shownChats, useRecentChats } from "../lib/recent-chats";
import { chordParts, handlerOf, runShortcut, SCOPE_LABEL, useBindings, type Bindings } from "../lib/shortcuts";
import { useStore } from "../lib/store";
import { buildTerminalTree, type PanesResponse } from "../lib/terminals";
import { NO_PINS, type AgentPins } from "../lib/pins";
import type { AssistantInfo, Interaction, Machine, Session } from "../lib/types";
import { AgentMark, SessionMark } from "../ui/AgentMark";
import { CARD_OUT, EASE, EXIT_MS, IconButton, Kbd, toast, useExit } from "../ui/atoms";
import { StatusMark } from "../ui/StatusMark";
import { Icon } from "../ui/icons";
import { useEscapeClose, useModalFocus } from "../ui/overlay";
import { useCloseOnBack } from "../lib/back-close";
import { copyFileMention, FilesHeading, FilesNotice, FileResultRow, openFileHit, usePaletteFiles, type FileRow, type PaletteFiles } from "./palette/FilesSection";
import { AgentConditions } from "./palette/AgentConditions";
import { useSavedSearches, type SavedSearchPanel } from "./palette/SavedSearches";
import type { SavedAgentSearch } from "../lib/saved-agent-searches";
import { runtimeSearchKey, useRuntimeSearch } from "./palette/RuntimeSearch";
import { t } from "../i18n";
import { byTime } from "../lib/times";
import { STATE_LABEL } from "../lib/state-labels";
import { plainMarkdown } from "../lib/plain-markdown";
import { NARROW, PHONE, useMedia } from "../lib/viewport";
import type { AssistantSearchAnswer, AssistantSearchHit } from "../lib/assistant-search";

interface Hit {
  key: string;
  s: Session;
  to: Route;
  assistant?: boolean;
  score: number;
  /** A Persona chat found by what was said in it (lib/assistant-search.ts): the message, shown as the row's second line. */
  said?: AssistantSearchHit;
  /** An Agent found by what is inside it, not its name, project or computer (#6589): the line that matched, likewise. */
  evidence?: EvidenceSnippet;
}

/** One selectable row of the list: a conversation, a terminal pane, an inbox item, a page ("Go to"), a file
 *  (views/palette/FilesSection.tsx), or (command mode) a command. */
type Row =
  /** `recent`: in "Recently opened" (#2191), `previous` its first, the one ⌘K ⏎ goes back to. */
  | { kind: "session"; key: string; hit: Hit; recent?: boolean; previous?: boolean }
  | { kind: "pane"; key: string; hit: TerminalHit }
  | { kind: "inbox"; key: string; hit: InboxHit<InboxItem> }
  | { kind: "place"; key: string; hit: PlaceHit }
  | { kind: "command"; key: string; cmd: PaletteCommand }
  | { kind: "saved"; key: string; item: SavedAgentSearch }
  | FileRow;

// A result row's title, the line under it, and what its end says (a time, "Terminal"), as the Inbox, pane and
// conversation rows write them.
const ROW_TITLE = "block truncate text-[13px] font-medium text-ink";
const ROW_DETAIL = "block truncate text-[11.5px] text-ink-3";
const ROW_END = "shrink-0 text-[11px] text-ink-3";

/** A group's heading above its first row ("Inbox", "Go to"), in a label's capitals; `asWritten`: one that holds a name
 *  (the pane's, the space's), which capitals would rewrite. */
const groupHeadingClass = "px-2.5 pt-2 pb-1 text-[10.5px] font-medium tracking-[0.06em] text-ink-3";
function GroupHeading({ children, asWritten = false }: { children: React.ReactNode; asWritten?: boolean }) {
  return <div className={asWritten ? groupHeadingClass : `${groupHeadingClass} uppercase`}>{children}</div>;
}

/** One row of the list as drawn: the option, and the heading above it when a group starts there. */
interface Section {
  key: string;
  heading: ReactNode;
  row: ReactNode;
}

/** The rows in their groups: a group with a heading is a role=group named by it, so a screen reader hears the
 *  heading with the options and the listbox holds only options and groups. */
function sections(items: Section[], listId: string): ReactNode[] {
  const groups: { key: string; heading: ReactNode; rows: Section[] }[] = [];
  for (const it of items) {
    if (it.heading || !groups.length) groups.push({ key: it.key, heading: it.heading, rows: [] });
    groups[groups.length - 1].rows.push(it);
  }
  return groups.map((g, n) => {
    const rows = g.rows.map((it) => <div key={it.key} role="none">{it.row}</div>);
    if (!g.heading) return <div key={g.key} role="none">{rows}</div>;
    const id = `${listId}-g${n}`;
    return (
      <div key={g.key} role="group" aria-labelledby={id}>
        <div id={id}>{g.heading}</div>
        {rows}
      </div>
    );
  });
}

/** What a row calls a chat: a Persona chat by its readable title as the sidebar does (a `<persona-context>` tag read,
 *  "New chat" for none), an Agent by its session's. */
const chatTitle = (s: Session, persona: boolean, main = false, preferTab = false) => (main ? t("sidebar.main_chat") : persona ? conversationTitle(s) : sessionTitle(s, preferTab));

/** A chat's match by its title (starting with what was typed 100, containing it 80), then every word in its folder,
 *  computer or last line (40): a Persona chat's, and an Agent's until lib/agent-search.ts has loaded. */
function score(s: Session, q: string, machine: string, persona = false, preferTab = false): number {
  return matchPaletteWords(chatTitle(s, persona, false, preferTab), q, [s.title, s.cwd, s.space_name, s.slot_name, machine, s.last_text].join(" "));
}

const HOME: Route = { name: "home" };
const NO_INTERACTIONS: Interaction[] = [];
const NO_SESSIONS_BY_MACHINE: Record<string, Session[]> = {};
const NO_FEATURES: string[] = [];
const EMPTY_BINDINGS: Bindings = {};
const EMPTY_TREES: Record<string, PanesResponse> = {};
const EMPTY_ROWS: Row[] = [];
const NEVER_DELETED = () => false;
const NOOP = () => {};
const defaultNotify = (message: string) => { toast(message); };
const defaultAvailable = (id: string) => Boolean(handlerOf(id));
const nodeLoaderIds = new WeakMap<() => Promise<void>, number>();
let nextNodeLoaderId = 0;
function nodeLoaderIdOf(loader: () => Promise<void>): number {
  let id = nodeLoaderIds.get(loader);
  if (id === undefined) {
    id = ++nextNodeLoaderId;
    nodeLoaderIds.set(loader, id);
  }
  return id;
}

function markSnippetParts(snippet: string, ranges: readonly [number, number][]): [string, boolean][] {
  const out: [string, boolean][] = [];
  let at = 0;
  for (const [a0, b0] of [...ranges].sort((x, y) => x[0] - y[0])) {
    const a = Math.max(at, Math.min(a0, snippet.length));
    const b = Math.max(a, Math.min(b0, snippet.length));
    if (a > at) out.push([snippet.slice(at, a), false]);
    if (b > a) out.push([snippet.slice(a, b), true]);
    at = b;
  }
  if (at < snippet.length) out.push([snippet.slice(at), false]);
  return out.length ? out : [[snippet, false]];
}

export type PaletteNavPage = Pick<NavPage, "k" | "label" | "icon" | "to">;
export type PaletteRecentChats = { entries: string[]; current: string };
export type PaletteSavedSearches = ReturnType<typeof useSavedSearches>;
export type PaletteRuntimeSearch = ReturnType<typeof useRuntimeSearch>;

export interface PaletteProps {
  open: boolean;
  onClose: () => void;
  onNewAgent: () => void;
  pane?: { mid: string; sid: string };
  route?: Route;
  listShown?: boolean;
  /** Controlled query string; skips `usePalette()` and `setPaletteQuery`. */
  query?: string;
  /** Controlled query setter replacing `setPaletteQuery`. */
  onQueryChange?: (next: string) => void;
  /** Controlled reopen callback when returning from Saved searches; replaces `openPaletteWith`. */
  onOpenWithQuery?: (query: string) => void;
  /** Controlled Agent sessions; skips `useAgentSessions()`. */
  sessions?: readonly Session[];
  /** Controlled Assistant info (`null` when absent); skips `useStore(s => s.assistant)`. */
  assistant?: AssistantInfo | null;
  /** Controlled machines list; skips `useStore(s => s.machines)`. */
  machines?: readonly Machine[];
  /** Controlled Agent pins; skips `useStore(s => s.pins)`. */
  pins?: AgentPins;
  /** Controlled interactions; skips `useStore(s => s.interactions)`. */
  interactions?: readonly Interaction[];
  /** Controlled per-machine sessions for terminal trees; skips `useStore(s => s.sessions)`. */
  sessionsByMachine?: Record<string, Session[]>;
  /** Controlled hub feature flags; skips `useStore(s => s.features)`. */
  features?: readonly string[];
  /** Controlled to-dos support flag; skips `useStore(s => s.todosSupported)`. */
  todosSupported?: boolean;
  /** Controlled complete-list flag; skips `useScope()` and `localStorage`. */
  completeList?: boolean;
  /** Controlled inbox items; skips `useInbox()`. */
  inboxItems?: readonly InboxItem[];
  /** Controlled navigation pages for "Go to"; skips `useNavPages()`. */
  pages?: readonly PaletteNavPage[];
  /** Controlled recent chats ledger; skips `useRecentChats()`. */
  recentChats?: PaletteRecentChats;
  /** Controlled shortcut bindings; skips `useBindings()`. */
  bindings?: Bindings;
  /** Controlled Herdr tab-name preference; skips `useHerdrTabName()`. */
  preferTab?: boolean;
  /** Controlled credential owner scope key; skips global credential subscription. */
  credentialOwner?: string;
  /** Controlled active manager scope key; skips `useShownManager()`. */
  manager?: string;
  /** Controlled deletion epoch/scope; skips `useStore(conversationDeletionScope)`. */
  deletionScope?: number;
  /** Controlled deletion revision; skips `useStore(conversationDeletionRevision)`. */
  deletionRevision?: number;
  /** Controlled deleted-conversation predicate; skips `conversationDeleted()`. */
  isConversationDeleted?: (machineId: string, sessionId: string) => boolean;
  /** Controlled saved-searches & search-module handle; skips `useSavedSearches`. */
  savedSearches?: PaletteSavedSearches;
  /** Controlled runtime search state/actions; skips `useRuntimeSearch`. */
  runtimeSearch?: PaletteRuntimeSearch;
  /** Controlled files section state; skips `usePaletteFiles`. */
  files?: PaletteFiles;
  /** Controlled file open callback replacing `openFileHit`. */
  onOpenFile?: (row: FileRow) => void;
  /** Controlled file mention copy callback replacing `copyFileMention`. */
  onCopyFileMention?: (row: FileRow) => Promise<void> | void;
  /** Controlled terminal panes map per machine; skips `/machines/:mid/panes` requests. */
  panesByMachine?: Record<string, PanesResponse>;
  /** Controlled terminal panes loader replacing `api("/machines/:mid/panes")`. */
  loadMachinePanes?: (machineId: string, signal: AbortSignal) => Promise<PanesResponse>;
  /** Controlled assistant chat search replacing `searchAssistantChats`. */
  searchAssistant?: (query: string, signal: AbortSignal) => Promise<AssistantSearchAnswer | null | undefined>;
  /** Controlled assistant snippet highlighter paired with `searchAssistant`. */
  markAssistantSnippet?: typeof import("../lib/assistant-search").markParts;
  /** Controlled command list for `>` mode; skips `node-palette`, `contextCommands`, `handlerOf`, and `runShortcut`. */
  commands?: readonly PaletteCommand[];
  /** Controlled context commands replacing global `contextCommands({ route, pane })`. */
  contextCommands?: readonly PaletteCommand[] | ((ctx: PaletteContext) => PaletteCommand[]);
  /** Controlled command availability predicate replacing `Boolean(handlerOf(id))`. */
  isCommandAvailable?: (id: string) => boolean;
  /** Controlled command runner replacing `runShortcut(id, { digit })`. Return `false` to notify unavailable. */
  onRunCommand?: (id: string, digit?: number) => boolean | void;
  /** Controlled node-palette loader (`false` disables lazy `import("../lib/node-palette")`). */
  loadNodeCommands?: (() => Promise<void>) | false;
  /** Controlled route href builder replacing `href(route)`. */
  resolveHref?: (route: Route) => string;
  /** Controlled navigation callback replacing `navigate(route)`. */
  onNavigate?: (route: Route) => void;
  /** Controlled conversation route builder replacing `conversationPage(...)`. */
  resolveConversationRoute?: (session: Session, at?: string) => Route;
  /** Controlled inbox route builder replacing `itemRoute(...)`. */
  resolveInboxRoute?: (item: InboxItem) => Route;
  /** Controlled Agents-list filter callback replacing `filterAgentList(query)`. */
  onFilterAgentList?: (query: string) => void;
  /** Controlled visibility of "Filter Agents list"; skips `useShowFilter()`. */
  showAgentsFilter?: boolean;
  /** Controlled notification callback replacing `toast(message)`. */
  onNotify?: (message: string) => void;
  /** Set `false` to disable browser-history `useCloseOnBack` on narrow screens. */
  closeOnBack?: boolean;
}

interface PaletteSources {
  route: Route;
  listShown: boolean;
  q: string;
  hasControlledQuery: boolean;
  setQ: (next: string) => void;
  sessions: readonly Session[];
  assistant: AssistantInfo | null;
  machines: readonly Machine[];
  pins: AgentPins;
  interactions: readonly Interaction[];
  bySession: Record<string, Session[]>;
  features: readonly string[];
  completeList: boolean;
  panesOn: boolean;
  searchOn: boolean;
  inboxItems: readonly InboxItem[];
  pages: readonly PaletteNavPage[];
  recentChats: PaletteRecentChats;
  bindings: Bindings;
  preferTab: boolean;
  owner: string;
  manager: string;
  epoch: number;
  revision: number;
  controlledScope: boolean;
  isDeleted: (machineId: string, sessionId: string) => boolean;
}

interface ResolvedPaletteProps extends PaletteProps {
  sources: PaletteSources;
}

function hasControlledSources(p: PaletteProps): boolean {
  return p.query !== undefined && p.sessions !== undefined && p.machines !== undefined && p.assistant !== undefined
    && p.inboxItems !== undefined && p.pages !== undefined && p.recentChats !== undefined
    && (p.bindings !== undefined || p.commands !== undefined);
}

/** The conversation "Go back" opens (#2191): the newest opened here, other than the one on the page, that still exists. */
function previousChat(
  recent: PaletteRecentChats,
  sessions: readonly Session[],
  conversations: readonly Session[],
  resolveConversationRoute?: (session: Session) => Route,
): { s: Session; to: Route } | null {
  const find = (k: string): { s: Session; to: Route } | null => {
    if (k.startsWith("j:")) {
      const s = conversations.find((c) => `j:${c.session_id}` === k);
      return s ? { s, to: resolveConversationRoute ? resolveConversationRoute(s) : { name: "persona", sid: s.session_id } } : null;
    }
    const s = sessions.find((c) => `s:${c.machine_id}/${c.session_id}` === k);
    return s ? { s, to: { name: "session", mid: s.machine_id, sid: s.session_id } } : null;
  };
  const [key] = shownChats(recent.entries, recent.current, (k) => find(k) !== null, 1);
  return key ? find(key) : null;
}

/** `listShown`: the sidebar's column, with its Agents list, is on screen (not a narrow screen's drawer or the rail). */
export function Palette(props: PaletteProps) {
  if (hasControlledSources(props)) {
    const features = props.features ?? NO_FEATURES;
    return (
      <PaletteWithSaved
        {...props}
        sources={{
          route: props.route ?? HOME,
          listShown: props.listShown ?? true,
          q: props.query!,
          hasControlledQuery: true,
          setQ: props.onQueryChange ?? NOOP,
          sessions: props.sessions!,
          assistant: props.assistant!,
          machines: props.machines!,
          pins: props.pins ?? NO_PINS,
          interactions: props.interactions ?? NO_INTERACTIONS,
          bySession: props.sessionsByMachine ?? NO_SESSIONS_BY_MACHINE,
          features,
          completeList: props.completeList ?? true,
          panesOn: features.includes("terminal_panes"),
          searchOn: Boolean(props.searchAssistant) || features.includes("assistant_search"),
          inboxItems: props.inboxItems!,
          pages: props.pages!,
          recentChats: props.recentChats!,
          bindings: props.bindings ?? EMPTY_BINDINGS,
          preferTab: props.preferTab ?? false,
          owner: props.credentialOwner ?? "",
          manager: props.manager ?? "",
          epoch: props.deletionScope ?? 0,
          revision: props.deletionRevision ?? 0,
          controlledScope: true,
          isDeleted: props.isConversationDeleted ?? NEVER_DELETED,
        }}
      />
    );
  }
  return <DefaultPaletteSources {...props} />;
}

function DefaultPaletteSources(props: PaletteProps) {
  const defaultPreferTab = useHerdrTabName();
  const defaultOwner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const defaultManager = useShownManager();
  const defaultEpoch = useStore(conversationDeletionScope);
  const defaultRevision = useStore(conversationDeletionRevision);
  const defaultQuery = usePalette().query;
  const defaultSessions = useAgentSessions();
  const defaultRecentChats = useRecentChats();
  const defaultAssistant = useStore((s) => s.assistant);
  const defaultMachines = useStore((s) => s.machines);
  const defaultPins = useStore((s) => s.pins);
  const defaultInteractions = useStore((s) => s.interactions);
  const defaultBySession = useStore((s) => s.sessions);
  const defaultFeatures = useStore((s) => s.features);
  const defaultTodosSupported = useStore((s) => s.todosSupported);
  const several = useScope().several;
  const defaultBindings = useBindings();
  const defaultInboxItems = useInbox((s) => s.items);
  const { all: defaultPages } = useNavPages();
  const features = props.features ?? defaultFeatures;
  const pins = props.pins ?? defaultPins;
  const todosSupported = props.todosSupported ?? defaultTodosSupported;
  const completeList = props.completeList ?? !pagedAgentList({
    bounded: supportsPagedAgents(features as string[]),
    byTodo: features.includes(SESSIONS_TODO_FEATURE),
    todosSupported,
    pins,
    stored: storedValue(GROUPING_KEY),
    several,
  });
  const hasControlledQuery = props.query !== undefined;
  return (
    <PaletteWithSaved
      {...props}
      sources={{
        route: props.route ?? HOME,
        listShown: props.listShown ?? true,
        q: hasControlledQuery ? props.query! : defaultQuery,
        hasControlledQuery,
        setQ: props.onQueryChange ?? (hasControlledQuery ? NOOP : setPaletteQuery),
        sessions: props.sessions ?? defaultSessions,
        assistant: props.assistant !== undefined ? props.assistant : defaultAssistant,
        machines: props.machines ?? defaultMachines,
        pins,
        interactions: props.interactions ?? defaultInteractions,
        bySession: props.sessionsByMachine ?? defaultBySession,
        features,
        completeList,
        panesOn: features.includes("terminal_panes"),
        searchOn: Boolean(props.searchAssistant) || features.includes("assistant_search"),
        inboxItems: props.inboxItems ?? defaultInboxItems,
        pages: props.pages ?? defaultPages,
        recentChats: props.recentChats ?? defaultRecentChats,
        bindings: props.bindings ?? defaultBindings,
        preferTab: props.preferTab ?? defaultPreferTab,
        owner: props.credentialOwner ?? defaultOwner,
        manager: props.manager ?? defaultManager,
        epoch: props.deletionScope ?? defaultEpoch,
        revision: props.deletionRevision ?? defaultRevision,
        controlledScope: props.credentialOwner !== undefined || props.manager !== undefined || props.deletionScope !== undefined,
        isDeleted: props.isConversationDeleted ?? conversationDeleted,
      }}
    />
  );
}

interface PaletteWithSavedProps extends ResolvedPaletteProps {
  saved: PaletteSavedSearches;
  savedPanel: SavedSearchPanel | null;
  setSavedPanel: Dispatch<SetStateAction<SavedSearchPanel | null>>;
}

function PaletteWithSaved(props: ResolvedPaletteProps) {
  const [savedPanel, setSavedPanel] = useState<SavedSearchPanel | null>(null);
  if (props.savedSearches !== undefined) {
    return <PaletteWithRuntime {...props} saved={props.savedSearches} savedPanel={savedPanel} setSavedPanel={setSavedPanel} />;
  }
  return <DefaultSavedSearchesBridge {...props} savedPanel={savedPanel} setSavedPanel={setSavedPanel} />;
}

function DefaultSavedSearchesBridge(props: ResolvedPaletteProps & { savedPanel: SavedSearchPanel | null; setSavedPanel: Dispatch<SetStateAction<SavedSearchPanel | null>> }) {
  const { mode } = parseQuery(props.sources.q);
  const saved = useSavedSearches(props.open && mode === "jump", props.savedPanel);
  return <PaletteWithRuntime {...props} saved={saved} />;
}

interface PaletteWithRuntimeProps extends PaletteWithSavedProps {
  runtime: PaletteRuntimeSearch;
}

function PaletteWithRuntime(props: PaletteWithSavedProps) {
  if (props.runtimeSearch !== undefined) {
    return <PaletteWithFiles {...props} runtime={props.runtimeSearch} />;
  }
  return <DefaultRuntimeSearchBridge {...props} />;
}

function DefaultRuntimeSearchBridge(props: PaletteWithSavedProps) {
  const { mode, text } = parseQuery(props.sources.q);
  const managing = props.savedPanel?.scope === props.saved.scope ? props.savedPanel : null;
  const runtime = useRuntimeSearch({
    open: props.open && !managing,
    jump: mode === "jump",
    query: text,
    capable: props.sources.features.includes("sessions_search"),
    bounded: props.sources.features.includes(SESSIONS_LIST_FEATURE),
    module: props.saved.module,
    loadError: props.saved.failed ? t("palette.saved_searches_failed") : "",
    owner: props.saved.owner,
  });
  return <PaletteWithFiles {...props} runtime={runtime} />;
}

interface PaletteViewProps extends PaletteWithRuntimeProps {
  scope: string;
  currentScope: () => boolean;
  machinePanes: MachinePanes[];
  resolvedFiles: PaletteFiles;
}

function PaletteWithFiles(props: PaletteWithRuntimeProps) {
  const { open, panesByMachine, loadMachinePanes, sources } = props;
  const { owner, epoch, manager, controlledScope, machines, bySession, panesOn } = sources;
  const scope = JSON.stringify([owner, epoch, manager]);
  const scopeRef = useRef(scope);
  scopeRef.current = scope;
  const currentScope = useCallback(
    () => scopeRef.current === scope && (controlledScope || scope === JSON.stringify([credentialOwner(), conversationDeletionScope(), shownManager()])),
    [scope, controlledScope],
  );
  const loadPanesRef = useRef(loadMachinePanes);
  loadPanesRef.current = loadMachinePanes;
  const hasLoadPanes = Boolean(loadMachinePanes);
  const hasControlledPanes = panesByMachine !== undefined;
  const shouldFetchPanes = !hasControlledPanes && (hasLoadPanes || panesOn);
  const onlineKey = machines.filter((m) => m.online).map((m) => m.id).join("\n");
  const panesSeqRef = useRef(0);
  const [treesState, setTreesState] = useState<{ scope: string; token: number; data: Record<string, PanesResponse> }>({ scope: "", token: 0, data: EMPTY_TREES });
  useLayoutEffect(() => {
    panesSeqRef.current += 1;
    setTreesState({ scope, token: 0, data: EMPTY_TREES });
  }, [scope]);
  useEffect(() => {
    if (!open || !shouldFetchPanes || !onlineKey) return;
    const token = ++panesSeqRef.current;
    const ctrl = new AbortController();
    for (const mid of onlineKey.split("\n")) {
      const req = loadPanesRef.current
        ? Promise.resolve().then(() => loadPanesRef.current!(mid, ctrl.signal))
        : api<PanesResponse>(`/machines/${enc(mid)}/panes`, { signal: ctrl.signal });
      req.then(
        (res) => {
          if (ctrl.signal.aborted || panesSeqRef.current !== token || !currentScope()) return;
          setTreesState((prev) => ({ scope, token, data: prev.token === token ? { ...prev.data, [mid]: res } : { [mid]: res } }));
        },
        () => {}, // a computer that cannot be read just has no panes here
      );
    }
    return () => {
      panesSeqRef.current += 1;
      ctrl.abort();
    };
  }, [open, shouldFetchPanes, onlineKey, scope, currentScope]);
  const trees = panesByMachine ?? (treesState.scope === scope ? treesState.data : EMPTY_TREES);
  const machinePanes = useMemo<MachinePanes[]>(
    () =>
      machines.flatMap((m) =>
        trees[m.id] ? [{ mid: m.id, machine: machineWithHub(m.id, machines as Machine[]), spaces: buildTerminalTree(trees[m.id], bySession[m.id] ?? []), sessionIds: new Set((bySession[m.id] ?? []).map((x) => x.session_id)) }] : [],
      ),
    [machines, trees, bySession],
  );
  if (props.files !== undefined) {
    return <PaletteView {...props} scope={scope} currentScope={currentScope} machinePanes={machinePanes} resolvedFiles={props.files} />;
  }
  return <DefaultFilesBridge {...props} scope={scope} currentScope={currentScope} machinePanes={machinePanes} />;
}

function DefaultFilesBridge(props: Omit<PaletteViewProps, "resolvedFiles">) {
  const { mode, text } = parseQuery(props.sources.q);
  const managing = props.savedPanel?.scope === props.saved.scope ? props.savedPanel : null;
  const operators = mode === "jump" && Boolean(props.saved.module?.hasSearchOperators(text));
  const resolvedFiles = usePaletteFiles({
    open: props.open && !managing,
    jump: mode === "jump" && !operators,
    text,
    route: props.sources.route,
    pane: props.pane,
    machinePanes: props.machinePanes,
  });
  return <PaletteView {...props} resolvedFiles={resolvedFiles} />;
}

function PaletteView({
  open,
  onClose,
  onNewAgent,
  pane,
  sources,
  onOpenWithQuery,
  saved,
  savedPanel,
  setSavedPanel,
  runtime: runtimeSearch,
  scope,
  currentScope,
  machinePanes,
  resolvedFiles: files,
  onOpenFile,
  onCopyFileMention,
  searchAssistant,
  markAssistantSnippet,
  commands: controlledCommands,
  contextCommands: controlledContextCommands,
  isCommandAvailable,
  onRunCommand,
  loadNodeCommands,
  resolveHref,
  onNavigate,
  resolveConversationRoute,
  resolveInboxRoute,
  onFilterAgentList,
  showAgentsFilter,
  onNotify,
  closeOnBack = true,
}: PaletteViewProps) {
  const {
    route, listShown, q, hasControlledQuery, setQ, sessions, assistant, machines, pins, interactions,
    completeList, panesOn, searchOn, inboxItems, pages, recentChats, bindings, preferTab, owner, epoch, revision, isDeleted,
  } = sources;
  const account = JSON.stringify([owner, epoch]);
  const priorAccount = useRef(account);
  const { mode, text } = parseQuery(q);
  // The node the user is on adds its menu as `>` commands (lib/node-palette.ts registers them when imported). They load
  // the first time the palette is open, off the first screen and the idle tier; the list is drawn again once they have.
  // A failed load is not kept: the next opening asks again (Palette.node-commands-retry.test.tsx).
  // Controlled loaders are scoped by active scope + loader identity and reset on scope/loader change or close/reopen,
  // while default module import preserves its one-time load and retry-on-failure lifecycle.
  const customNodeLoader = typeof loadNodeCommands === "function" ? loadNodeCommands : undefined;
  const shouldLoadNode = loadNodeCommands !== false && (Boolean(customNodeLoader) || (controlledCommands === undefined && controlledContextCommands === undefined));
  const nodeLoadKey = !shouldLoadNode
    ? ""
    : customNodeLoader
      ? JSON.stringify(["custom", scope, nodeLoaderIdOf(customNodeLoader)])
      : "default";
  const nodeLoadKeyRef = useRef(nodeLoadKey);
  nodeLoadKeyRef.current = nodeLoadKey;
  const nodeLoadSeqRef = useRef(0);
  const [nodeReady, setNodeReady] = useState<{ key: string; token: number }>({ key: "", token: 0 });
  const nodeCommandsReady = nodeLoadKey !== "" && nodeReady.key === nodeLoadKey && (!customNodeLoader || open);
  const nodeCommandsToken = nodeCommandsReady ? nodeReady.token : 0;
  useLayoutEffect(() => {
    if (!nodeLoadKey || customNodeLoader) {
      nodeLoadSeqRef.current += 1;
      setNodeReady((prev) => (prev.key ? { key: "", token: 0 } : prev));
      return;
    }
    setNodeReady((prev) => (prev.key && prev.key !== nodeLoadKey ? { key: "", token: 0 } : prev));
  }, [open, nodeLoadKey, customNodeLoader]);
  useEffect(() => {
    if (!open || !nodeLoadKey || nodeCommandsReady) return;
    const token = ++nodeLoadSeqRef.current;
    let live = true;
    let req: Promise<unknown>;
    if (customNodeLoader) {
      try {
        req = Promise.resolve(customNodeLoader());
      } catch {
        return;
      }
    } else {
      req = import("../lib/node-palette");
    }
    void req.then(
      () => {
        if (!live || nodeLoadSeqRef.current !== token || nodeLoadKeyRef.current !== nodeLoadKey || !currentScope()) return;
        setNodeReady({ key: nodeLoadKey, token });
      },
      () => {},
    );
    return () => {
      live = false;
      nodeLoadSeqRef.current += 1;
    };
  }, [open, nodeLoadKey, nodeCommandsReady, customNodeLoader, currentScope]);
  const managing = savedPanel?.scope === saved.scope ? savedPanel : null;
  useEffect(() => { if (savedPanel && !managing) setSavedPanel(null); }, [savedPanel, managing, setSavedPanel]);
  // Both search tools arrive with the opened palette, outside its idle chunk. Their data lifetimes remain
  // independent: saved queries follow the default manager; runtime matches are hub-wide and require typed input.
  const agentSearch = saved.module;
  // `machine:`, `project:`, `is:pinned`, `since:`… speak of Agent sessions: with one, only they are listed (#6589)
  const operators = mode === "jump" && Boolean(agentSearch?.hasSearchOperators(text));
  const [selection, setSelection] = useState<{ query: string; key: string }>({ query: q, key: "" });
  const [box, setBox] = useState<{ top: number; height: number } | null>(null);
  const formatHref = resolveHref ?? (onNavigate ? JSON.stringify : href);
  const go = onNavigate ?? navigate;
  const conversationRouteFor = useCallback(
    (s: Session, at?: string) => (resolveConversationRoute ? resolveConversationRoute(s, at) : conversationPage(s.session_id, s.machine_id, s.manager, at)),
    [resolveConversationRoute],
  );
  const inboxRouteFor = useCallback(
    (item: InboxItem) => (resolveInboxRoute ? resolveInboxRoute(item) : itemRoute(item)),
    [resolveInboxRoute],
  );
  const notify = onNotify ?? defaultNotify;
  const openFile = onOpenFile ?? openFileHit;
  const copyFile = onCopyFileMention ?? copyFileMention;
  const applyAgentListFilter = onFilterAgentList ?? filterAgentList;
  // the pages come anew each render: kept by what they are, so the list (and the highlight under it) stays put
  const hasAssistant = Boolean(assistant);
  const pagesKey = pages.map((p) => [p.k, p.label, p.icon, formatHref(p.to)].join("\u0001")).join("\n");
  const places = useMemo<PlaceInput[]>(
    () => [
      ...(hasAssistant ? [{ key: "persona", title: t("palette.place_persona"), icon: "home" as const, to: { name: "persona" } as Route }, { key: "history", title: t("sidebar.all_chats"), icon: "list" as const, to: { name: "history" } as Route }] : []),
      ...pages.map((p) => ({ key: p.k, title: p.label, icon: p.icon, to: p.to })),
      // the Sessions and Terminal page's Terminals list (#4754): every plain terminal, to open one and type
      ...(panesOn ? [{ key: "terminals", title: t("palette.place_terminals"), icon: "terminal" as const, to: { name: "terminal", view: "terminals" } as Route }] : []),
    ].map((p) => ({ ...p, words: agentSearch?.placeWords(p.key) ?? "" })),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `pages` is new each render; pagesKey says what it holds
    [hasAssistant, pagesKey, panesOn, agentSearch],
  );
  const rows = useRef<(HTMLButtonElement | null)[]>([]);
  const list = useRef<HTMLDivElement>(null);
  // the listbox and its options, for the field's aria-controls / aria-activedescendant
  const listId = useId();
  const optionId = (i: number) => `${listId}-${i}`;

  useLayoutEffect(() => {
    if (open) setSelection({ query: q, key: "" });
    // Query changes independently start at the first result. Reopening also drops the previous choice.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // loaded on intent (the palette is idle-tier, src/startup-preload.ts IDLE_TIER_BUDGET): the first query typed loads
  // lib/assistant-search.ts; asked 200 ms after the last key, each query cancelling the one before
  const searchAssistantRef = useRef(searchAssistant);
  searchAssistantRef.current = searchAssistant;
  const hasSearchAssistant = Boolean(searchAssistant);
  const markSnippetRef = useRef(markAssistantSnippet);
  markSnippetRef.current = markAssistantSnippet;
  const searchSeqRef = useRef(0);
  const [found, setFound] = useState<{ q: string; scope?: string; token: number; answer?: AssistantSearchAnswer; marks?: typeof import("../lib/assistant-search").markParts }>({ q: "", token: 0 });
  // a found chat's line starts just before its match, as much of a lead-in as the row's width holds (#7045)
  const phone = useMedia(PHONE);
  const asked = open && !managing && mode === "jump" && !operators && searchOn && Boolean(assistant) ? text.trim() : "";
  useLayoutEffect(() => {
    if (priorAccount.current !== account) { priorAccount.current = account; onClose(); }
  }, [account, onClose]);
  useEffect(() => {
    const token = ++searchSeqRef.current;
    setFound({ q: "", scope, token });
    if (!asked) return;
    const ctrl = new AbortController();
    const id = window.setTimeout(() => {
      if (searchAssistantRef.current) {
        Promise.resolve()
          .then(() => searchAssistantRef.current!(asked, ctrl.signal))
          .then((answer) => {
            if (ctrl.signal.aborted || searchSeqRef.current !== token || !currentScope()) return;
            setFound({ q: asked, scope, token, answer: answer ?? undefined, marks: markSnippetRef.current ?? markSnippetParts });
          })
          .catch(() => {});
        return;
      }
      void import("../lib/assistant-search")
        .then((m) => {
          if (ctrl.signal.aborted || searchSeqRef.current !== token || !currentScope()) return;
          return m.searchAssistantChats(asked, ctrl.signal).then((answer) => {
            if (!ctrl.signal.aborted && searchSeqRef.current === token && currentScope()) setFound({ q: asked, scope, token, answer: answer ?? undefined, marks: m.markParts });
          });
        })
        .catch(() => {}); // a failed search leaves the local matches
    }, 200);
    return () => {
      searchSeqRef.current += 1;
      clearTimeout(id);
      ctrl.abort();
    };
  }, [asked, scope, currentScope, hasSearchAssistant]);
  useEffect(() => { setFound((f) => f.answer ? { ...f, answer: { ...f.answer, results: f.answer.results.filter((r) => !isDeleted(r.machine_id, r.session_id)) } } : f); }, [revision, isDeleted]);
  const answer = asked && found.q === asked && found.scope === scope ? found.answer : undefined;
  const hits = useMemo((): Row[] => {
    if (mode !== "jump") return EMPTY_ROWS;
    const query = text.toLowerCase();
    let out: Hit[] = [];
    // an Agent as the list's search found it: its operators, then its name, its project or computer, what is inside it
    const ctx = { machines: machines as Machine[], pins, interactions: interactions as Interaction[], now: Date.now() };
    for (const s of sessions) {
      const found = agentSearch ? agentSearch.agentSearchHit(s, text, ctx, preferTab) : { score: score(s, query, machineWithHub(s.machine_id, machines as Machine[]), false, preferTab), snippet: undefined };
      if (found.score) out.push({ key: `s:${runtimeSearchKey(s)}`, s, to: { name: "session", mid: s.machine_id, sid: s.session_id }, score: found.score + (activityOf(s) === "offline" ? 0 : 5), evidence: found.snippet });
    }
    // The server's grouped matches and evidence are never matched again against truncated summaries.
    const merged = runtimeSearch.mergeHits?.(out, sessions, runtimeSearch);
    const remoteKeys = merged?.remoteKeys ?? new Set<string>();
    out = merged?.hits ?? out;
    for (const s of operators ? [] : assistant?.conversations ?? []) {
      const sc = Math.max(score(s, query, "persona", true), s.session_id === assistant?.main ? matchPaletteWords(t("sidebar.main_chat"), query, "") : 0);
      if (sc) out.push({ key: `j:${s.session_id}`, s, to: conversationRouteFor(s), assistant: true, score: sc });
    }
    // what the manager found: a chat found by a message opens at it (not one found by its title); one not listed here
    // (archived) is the result's own row
    for (const r of answer?.results ?? []) {
      if (isDeleted(r.machine_id, r.session_id)) continue;
      const key = `j:${r.session_id}`;
      const hit = r.hits[0];
      const at = r.title_match ? undefined : hit?.item_id;
      const known = out.find((h) => h.key === key);
      if (known) {
        known.said = hit;
        known.to = conversationRouteFor(known.s, at);
        continue;
      }
      const s = (assistant?.conversations ?? []).find((c) => c.session_id === r.session_id)
        ?? ({ machine_id: r.machine_id, session_id: r.session_id, title: r.title, cwd: "", status: "idle", assistant: true, archived: r.archived, updated_at: r.updated_at } as Session);
      out.push({ key, s, to: conversationRouteFor(s, at), assistant: true, score: r.title_match ? 80 : 40, said: hit });
    }
    // nothing typed: the conversations opened here come first, newest first, without the one on the page (#2191); the
    // first is the one before it, picked already, so ⌘K ⏎ goes back to it
    // The recent-chat ledger predates tuple render identities. Resolve its existing keys without changing what
    // was recorded; result keys themselves stay stable when a remote-only match later enters the local cache.
    const byKey = new Map(out.map((h) => [h.assistant ? h.key : `s:${h.s.machine_id}/${h.s.session_id}`, h]));
    const recent = query ? [] : shownChats(recentChats.entries, recentChats.current, (k) => byKey.has(k), PALETTE_RECENT_CHATS);
    const recentRows: Row[] = recent.map((k, i) => ({ kind: "session" as const, key: byKey.get(k)!.key, hit: byKey.get(k)!, recent: true, previous: i === 0 }));
    const recentKeys = new Set(recentRows.map((row) => row.key));
    const all: (Row & { score: number; at: string })[] = [
      ...out.filter((h) => !recentKeys.has(h.key)).map((h) => ({ kind: "session" as const, key: h.key, hit: h, score: h.score, at: h.s.updated_at ?? "" })),
      ...(operators ? [] : terminalHits(machinePanes, text)).map((h) => ({ kind: "pane" as const, key: h.key, hit: h, score: h.score, at: "" })),
    ];
    all.sort((a, b) => b.score - a.score || (agentSearch && a.kind === "session" && b.kind === "session" && !a.hit.assistant && !b.hit.assistant
      ? agentSearch.runtimeOrder(a.hit.s, b.hit.s) : byTime(b.at, a.at)));
    const mail = (operators ? [] : inboxHits(inboxItems.filter((it) => !isDone(it)), text)).map((h) => ({ kind: "inbox" as const, key: h.key, hit: h, score: h.score }));
    const goRows = (operators ? [] : placeHits(places, text)).map((h) => ({ kind: "place" as const, key: h.key, hit: h, score: h.score }));
    const savedRows: Row[] = !query && !saved.state.unsupported ? (saved.state.document?.searches ?? []).slice(0, 5).map((item) => ({ kind: "saved", key: `saved:${item.id}`, item })) : [];
    // Loaded runtime pages stay reachable; unrelated local groups retain their existing cap.
    let others = 0;
    const window = all.filter((row) => remoteKeys.has(row.key) || others++ < 40);
    return [...recentRows, ...savedRows, ...orderGroups<Row & { score: number }>([window, mail, goRows]).flat()];
  }, [mode, text, operators, agentSearch, sessions, assistant, machines, pins, interactions, machinePanes, inboxItems, places, recentChats, answer, saved.state.document, saved.state.unsupported, runtimeSearch.hits, runtimeSearch.loaded, runtimeSearch.supported, runtimeSearch.loading, runtimeSearch.loadingMore, runtimeSearch.hasMore, runtimeSearch.error, runtimeSearch.moreError, runtimeSearch.mergeHits, revision, preferTab, isDeleted, conversationRouteFor]);

  // command mode: what other modules offer for here, then the registered commands with their shortcuts
  const commands = useMemo((): Row[] => {
    if (!open || mode !== "command") return EMPTY_ROWS;
    if (controlledCommands !== undefined) {
      return filterCommands([...controlledCommands], text).map((cmd) => ({ kind: "command" as const, key: cmd.id, cmd }));
    }
    const run = (id: string, digit?: number) => {
      const ok = onRunCommand ? onRunCommand(id, digit) !== false : runShortcut(id, { digit });
      if (!ok) notify(t("palette.command_unavailable"));
    };
    const available = isCommandAvailable ?? defaultAvailable;
    // Go back to the previous conversation (#2191): the newest opened here other than the one on the page that still exists
    const back = previousChat(recentChats, sessions, assistant?.conversations ?? [], resolveConversationRoute);
    const extra: PaletteCommand[] = [
      ...(back ? [{ id: "app:previous-chat", title: t("palette.previous_chat"), detail: chatTitle(back.s, back.to.name === "persona", false, preferTab), section: SCOPE_LABEL.global, icon: "undo" as const, keywords: "back previous last recent 返回 上一个 最近", run: () => go(back.to) }] : []), // i18n-ignore: search words in both languages, as PLACE_WORDS
      { id: "app:new-agent", title: t("palette.new_agent"), section: SCOPE_LABEL.global, icon: "plus", keywords: "new agent", run: onNewAgent },
      { id: "app:new-conversation", title: t("palette.new_conversation"), section: SCOPE_LABEL.global, icon: "chat", keywords: "persona new", run: () => go({ name: "persona-new" }) },
    ];
    const ctxCmds = controlledContextCommands === undefined
      ? contextCommands({ route, pane })
      : typeof controlledContextCommands === "function"
        ? controlledContextCommands({ route, pane })
        : [...controlledContextCommands];
    const all = [...ctxCmds, ...appCommands({ bindings, available, run, machines: machines.map((m) => ({ id: m.id, name: m.name })), extra })];
    return filterCommands(all, text).map((cmd) => ({ kind: "command" as const, key: cmd.id, cmd }));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `route` is parsed anew each render; its path says it all
  }, [open, mode, text, controlledCommands, controlledContextCommands, isCommandAvailable, onRunCommand, bindings, machines, pane?.mid, pane?.sid, formatHref(route), onNewAgent, recentChats, sessions, assistant, preferTab, nodeCommandsToken, go, notify, resolveConversationRoute]);
  const shown = useMemo(() => (mode === "command" ? commands : [...hits, ...files.rows]), [mode, commands, hits, files.rows]);

  // Saved shortcuts can arrive or move while the palette is open. Keep the highlighted identity rather than
  // opening whichever row later happens to occupy that index; a removed choice waits for another arrow press.
  const hasChoice = selection.query === q && selection.key !== "";
  const active = hasChoice ? shown.findIndex((row) => row.key === selection.key) : 0;
  const activeKey = shown[active]?.key;
  const setActive = (index: number) => setSelection({ query: q, key: shown[index]?.key ?? "" });
  useLayoutEffect(() => { if (open && activeKey && !hasChoice) setSelection({ query: q, key: activeKey }); }, [open, q, activeKey, hasChoice]);
  useLayoutEffect(() => {
    const el = rows.current[active];
    setBox(el ? { top: el.offsetTop, height: el.offsetHeight } : null);
    if (input.current === document.activeElement) el?.scrollIntoView({ block: "nearest" });
  }, [active, shown]);

  // It leaves as Dialog does (ui/atoms.tsx useExit): a fade and the card back down, showing what it last showed.
  const backdrop = useRef<HTMLDivElement>(null);
  const card = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const kept = useRef<ReactNode>(null);
  const keptAt = JSON.stringify([scope, revision]);
  const keptScope = useRef(keptAt);
  if (keptScope.current !== keptAt) kept.current = null;
  keptScope.current = keptAt;
  const exit = useExit(open, EXIT_MS.card, () => [
    backdrop.current?.animate([{ opacity: 1 }, { opacity: 0 }], { duration: EXIT_MS.card, easing: "ease-out", fill: "forwards" }),
    card.current?.animate(CARD_OUT, { duration: EXIT_MS.card, easing: EASE, fill: "forwards" }),
  ]);
  // a modal for the keyboard (ui/overlay.ts): Tab stays in, and closing gives the focus back to where it was. Called
  // before the effect below, so it notes that place before the field takes the focus.
  useModalFocus(card, open && !managing);
  // Esc closes it wherever the focus is in it (the field, a row Tab reached, the Filter), and only it: a Dialog under it
  // stays open, a menu over it (the Filter's) closes first
  useEscapeClose(open && !managing, onClose);
  // on a narrow screen Back closes it first, as it does the drawer and the sheets (lib/back-close.ts); a pick that goes
  // to a page takes its entry over, and a sheet opened from it (Saved searches, a command's) reuses it
  const narrow = useMedia(NARROW);
  useCloseOnBack(open && !managing, onClose, closeOnBack && narrow);
  // the field takes the focus on every open (opened again on its way out, it is not mounted anew)
  useEffect(() => {
    if (open && !managing) input.current?.focus({ preventScroll: true });
  }, [open, managing]);

  if (managing && saved.module && saved.model) {
    const finish = (query: string) => {
      setSavedPanel(null);
      if (onOpenWithQuery) onOpenWithQuery(query);
      else if (hasControlledQuery) setQ(query);
      else openPaletteWith(query);
    };
    return <saved.module.SavedSearchesDialog key={saved.scope} model={saved.model} initialQuery={managing.initialQuery} onReturn={() => finish(managing.returnQuery)} onApply={finish} />;
  }
  if (!exit.mounted) return null;
  const pick = (r: Row) => {
    if (!currentScope() || (r.kind === "session" && isDeleted(r.hit.s.machine_id, r.hit.s.session_id))) return;
    if (r.kind === "saved") { setQ(r.item.query); input.current?.focus({ preventScroll: true }); return; }
    onClose();
    if (r.kind === "command") r.cmd.run();
    else if (r.kind === "file") openFile(r);
    else if (r.kind === "inbox") go(inboxRouteFor(r.hit.item));
    else if (r.kind === "place") go(r.hit.place.to);
    else go(r.hit.to);
  };
  const manage = (save = false) => {
    if (!saved.module || !saved.model) return;
    setSavedPanel({ scope: saved.scope, returnQuery: q, ...(save ? { initialQuery: text } : {}) });
    onClose();
  };
  const command = mode === "command";
  const label = command ? t("palette.command_palette") : t("palette.search_sessions");
  // "Filter Agents list" (#6589): what was typed narrows the Agents list in place, as its own field did, while it finds an
  // Agent; on a narrow screen or beside the rail the list is the Agents page
  const listable = !command && text !== "" && showAgentsFilter !== false && (completeList || (Boolean(agentSearch) && !operators)) && hits.some((r) => r.kind === "session" && !r.hit.assistant);
  const filterList = () => {
    onClose();
    applyAgentListFilter(text);
    if (!listShown) go({ name: "agent-list" });
  };

  // On a narrow screen it sits in the part a soft keyboard leaves (--kb-top / --kb-h, lib/viewport.ts), the card no
  // taller than that: the list scrolls its last row above the keyboard. With no keyboard that part is the whole screen
  // and the gap under the card is 0, so a card that fits stays where it was.
  return createPortal(
    <div ref={backdrop} data-closing={exit.leaving || undefined} inert={exit.leaving || undefined} className="fixed inset-0 z-40 flex items-start justify-center bg-black/20 px-4 pt-[14vh] backdrop-blur-[2px] dark:bg-black/45 max-lg:top-(--kb-top) max-lg:bottom-auto max-lg:h-(--kb-h) max-lg:pb-[min(0.75rem,calc(100dvh-var(--kb-h)))]" style={{ animation: "fade-in 120ms ease-out both" }} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={card} role="dialog" aria-modal="true" tabIndex={-1} data-shortcut-layer={open || undefined} aria-label={label} className="w-full max-w-[560px] overflow-hidden rounded-[14px] bg-surface shadow-overlay outline-none max-lg:flex max-lg:max-h-full max-lg:flex-col" style={{ animation: `slide-up 200ms ${EASE} both` }}>
        {/* on the way out it shows what it showed when open: closed, the lists below come back empty */}
        {open ? (kept.current = <>
        <label className="flex h-12 shrink-0 items-center gap-2.5 border-b border-line px-4">
          <Icon name="search" size={15} stroke={2} className="text-ink-3" />
          <input
            ref={input}
            role="combobox"
            aria-label={label}
            aria-autocomplete="list"
            aria-expanded={shown.length > 0}
            aria-controls={shown.length > 0 ? listId : undefined}
            aria-activedescendant={shown[active] ? optionId(active) : undefined}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (composing(e.nativeEvent)) {
                if (e.key === "Escape") e.stopPropagation(); // it ends the input method's word, not the palette
                return;
              }
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                if (shown.length) setActive(active < 0 ? (e.key === "ArrowDown" ? 0 : shown.length - 1) : (active + (e.key === "ArrowDown" ? 1 : shown.length - 1)) % shown.length);
              } else if (e.key === "Enter" && e.shiftKey && listable) {
                e.preventDefault();
                filterList();
              } else if (e.key === "Enter") {
                e.preventDefault();
                const row = shown[active];
                if (row?.kind === "file" && (e.metaKey || e.ctrlKey)) {
                  onClose();
                  void copyFile(row);
                } else if (row) pick(row);
              }
            }}
            placeholder={command ? t("palette.command_placeholder") : files.ctx ? t("files-find.searching_placeholder") : t("palette.search_placeholder")}
            className="min-w-0 flex-1 bg-transparent text-[14px] text-ink outline-none placeholder:text-ink-3"
          />
          {/* the Agents list's filter choices, written into the query as its operators (#6589) */}
          {!command && <AgentConditions query={text} onQuery={(next) => setQ(next && `${next} `)} search={agentSearch} want={saved.retry} sessions={sessions as Session[]} machines={machines as Machine[]} pins={pins} onDone={() => input.current?.focus({ preventScroll: true })} />}
          {!command && <IconButton icon="book" label={t("palette.saved_searches")} disabled={!saved.module || !saved.model} onClick={() => manage()} />}
          {/* the key hints are for a keyboard: a touch screen has none (#1996) */}
          <span className="contents [@media(pointer:coarse)]:hidden">
            <Kbd>Esc</Kbd>
          </span>
        </label>
        <div ref={list} className="relative max-h-[52vh] overflow-y-auto p-1.5" onMouseLeave={() => {}}>
          <span aria-hidden className="pointer-events-none absolute inset-x-1.5 rounded-[8px] bg-hover" style={{ top: box?.top ?? 0, height: box?.height ?? 0, opacity: box ? 1 : 0, transition: `top 180ms ${EASE}, height 180ms ${EASE}` }} />
          {shown.length > 0 && (
          <div role="listbox" id={listId} aria-label={label}>
          {sections(shown.map((r, i): Section => {
            const ref = (el: HTMLButtonElement | null) => {
              rows.current[i] = el;
            };
            const rowClass = "relative z-10 flex w-full items-center gap-3 rounded-[8px] px-2.5 py-2 text-left";
            const option = { id: optionId(i), role: "option" as const, "aria-selected": i === active };
            if (r.kind === "command") {
              const c = r.cmd;
              const heading = c.section && c.section !== (shown[i - 1] as Extract<Row, { kind: "command" }> | undefined)?.cmd.section;
              return {
                key: r.key,
                heading: heading ? <GroupHeading asWritten={c.sectionAsWritten}>{c.section}</GroupHeading> : null,
                row: (
                  <button ref={ref} type="button" {...option} onMouseMove={() => setActive(i)} onClick={() => pick(r)} className={rowClass}>
                    <span className="flex size-5 shrink-0 items-center justify-center">{c.icon && <Icon name={c.icon} size={13} className={c.danger ? "text-red" : "text-ink-3"} />}</span>
                    <span className={`min-w-0 flex-1 truncate text-[13px] ${c.danger ? "text-red" : "text-ink"}`}>
                      {c.title}
                      {c.detail && <span className="ml-2 text-[12px] text-ink-3">{c.detail}</span>}
                    </span>
                    {c.chord && (
                      <span className="flex shrink-0 items-center gap-1 [@media(pointer:coarse)]:hidden">
                        {commandKeys(c).map((k, n) => (
                          <Kbd key={n}>{k}</Kbd>
                        ))}
                      </span>
                    )}
                  </button>
                ),
              };
            }
            if (r.kind === "saved") return { key: r.key, heading: shown[i - 1]?.kind !== "saved" ? <GroupHeading>{t("palette.saved_searches")}</GroupHeading> : null,
              row: saved.module ? <saved.module.SavedSearchQuickRow ref={ref} item={r.item} option={option} className={rowClass} onMouseMove={() => setActive(i)} onClick={() => pick(r)} /> : null };
            if (r.kind === "file") {
              const first = (shown[i - 1] as Row | undefined)?.kind !== "file";
              return { key: r.key, heading: first ? <FilesHeading /> : null, row: <FileResultRow ref={ref} row={r} option={option} className={rowClass} onMouseMove={() => setActive(i)} onClick={() => pick(r)} /> };
            }
            if (r.kind === "inbox" || r.kind === "place") {
              const heading = (shown[i - 1] as Row | undefined)?.kind !== r.kind;
              const body =
                r.kind === "inbox" ? (
                  <>
                    <span className="flex size-5 shrink-0 items-center justify-center"><Icon name="inbox" size={13} className="text-ink-3" /></span>
                    <span className="min-w-0 flex-1">
                      <span className={ROW_TITLE}>{r.hit.item.title}</span>
                      <span className={ROW_DETAIL}>{[r.hit.item.sender, r.hit.item.preview].filter(Boolean).join(t("palette.sender_separator"))}</span>
                    </span>
                    <span className={ROW_END}>{ago(r.hit.item.updated_at)}</span>
                  </>
                ) : (
                  <>
                    <span className="flex size-5 shrink-0 items-center justify-center"><Icon name={r.hit.place.icon} size={13} className="text-ink-3" /></span>
                    <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">{r.hit.place.title}</span>
                  </>
                );
              return {
                key: r.key,
                heading: heading ? <GroupHeading>{r.kind === "inbox" ? t("palette.group_inbox") : t("palette.group_places")}</GroupHeading> : null,
                row: (
                  <button ref={ref} type="button" {...option} onMouseMove={() => setActive(i)} onClick={() => pick(r)} className={rowClass}>
                    {body}
                  </button>
                ),
              };
            }
            // the conversations and panes have a heading only after another group ("Recently opened", "Inbox" or "Go to");
            // "Recently opened" has its own
            const before = shown[i - 1] as Row | undefined;
            const prev = before?.kind;
            const recent = r.kind === "session" && r.recent;
            const afterRecent = before?.kind === "session" && before.recent && !recent;
            const sessionsHeading = recent
              ? (i === 0 ? <GroupHeading>{t("palette.group_recent")}</GroupHeading> : null)
              : (i > 0 && ((prev !== "session" && prev !== "pane") || afterRecent) ? <GroupHeading>{t("palette.group_sessions")}</GroupHeading> : null);
            if (r.kind === "pane") {
              const h = r.hit;
              return {
                key: r.key,
                heading: sessionsHeading,
                row: (
                <button ref={ref} type="button" {...option} onMouseMove={() => setActive(i)} onClick={() => pick(r)} className={rowClass}>
                  {h.state !== "shell" && h.agent && <AgentMark agent={h.agent} surface={h.surface} size={14} />}
                  <span className="flex size-5 shrink-0 items-center justify-center">{h.state === "shell" ? <Icon name="terminal" size={13} className="text-ink-3" /> : <StatusMark state={h.state} />}</span>
                  <span className="min-w-0 flex-1">
                    <span className={ROW_TITLE}>{h.title}</span>
                    <span className={ROW_DETAIL}>{[h.trail, h.detail].filter(Boolean).join(" · ")}</span>
                  </span>
                  <span className={ROW_END}>{t("palette.terminal")}</span>
                </button>
                ),
              };
            }
            const h = r.hit;
            const st = activityOf(h.s);
            const isMain = Boolean(h.assistant && h.s.session_id === assistant?.main && h.s.machine_id === assistant.machine_id);
            // a Persona chat says what was said last (its folder is the assistant's own, the same for every chat); an Agent
            // where it runs
            const cli = h.assistant ? "" : sessionTitleDetail(h.s, preferTab);
            const tab = sessionDisplayTab(h.s, preferTab) ? "" : tabLabel(h.s);
            const meta = h.assistant ? plainMarkdown(h.s.last_text) || "Persona" : [cli, [machineWithHub(h.s.machine_id, machines as Machine[]), h.s.space_name, tab].filter(Boolean).join(" › "), shortPath(h.s.cwd)].filter(Boolean).join(" · ");
            return {
              key: h.key,
              heading: sessionsHeading,
              row: (
              <button ref={ref} type="button" {...option} onMouseMove={() => setActive(i)} onClick={() => pick(r)} className={rowClass}>
                {!h.assistant && <SessionMark s={h.s} size={14} />}
                <span className="flex size-5 shrink-0 items-center justify-center">{h.assistant ? <Icon name={isMain ? "home" : "spark"} size={14} className="text-ink-3" /> : <StatusMark state={st} />}</span>
                <span className="min-w-0 flex-1">
                  <span className="flex min-w-0 items-center gap-1.5">
                    <span className="truncate text-[13px] font-medium text-ink" title={cli || undefined}>{chatTitle(h.s, Boolean(h.assistant), isMain, preferTab)}</span>
                    {r.previous && <span className="shrink-0 rounded-[5px] bg-field px-1.5 text-[10.5px] text-ink-3 shadow-hairline">{t("palette.previous")}</span>}
                  </span>
                  {h.evidence ? (
                    <span className={ROW_DETAIL} data-evidence={h.evidence.role}>
                      {h.evidence.before}
                      <mark className="rounded-[3px] bg-accent-tint text-ink">{h.evidence.match}</mark>
                      {h.evidence.after}
                    </span>
                  ) : h.said ? (
                    <span className={ROW_DETAIL} data-said>
                      {(found.marks?.(h.said.snippet, h.said.ranges, phone ? "phone" : "wide") ?? [[h.said.snippet, false] as [string, boolean]]).map(([words, match], k) => (match ? <mark key={k} className="rounded-[3px] bg-accent-tint text-ink">{words}</mark> : <span key={k}>{words}</span>))}
                    </span>
                  ) : (
                    <span className={ROW_DETAIL}>{meta}</span>
                  )}
                </span>
                <span className={ROW_END}>{st === "offline" || h.assistant ? ago(h.s.updated_at) : STATE_LABEL[st]}</span>
              </button>
              ),
            };
          }), listId)}
          </div>
          )}
          {shown.length === 0 && command && (
            <div className="px-4 py-10 text-center">
              <p className="text-[13px] font-medium text-ink">{t("palette.no_commands")}</p>
              <p className="mt-0.5 text-[12px] text-ink-3">{t("palette.no_commands_hint")}</p>
            </div>
          )}
          {!command && runtimeSearch.Notice && <runtimeSearch.Notice search={runtimeSearch} onReturnFocus={() => input.current?.focus({ preventScroll: true })} />}
          {!command && <FilesNotice status={files.status} text={text} count={files.rows.length} />}
          {shown.length === 0 && !command && files.status !== "loading" && !runtimeSearch.loading && !runtimeSearch.error && !runtimeSearch.moreError && saved.module &&
            <saved.module.EmptySearch query={q} onNewAgent={() => { onClose(); onNewAgent(); }} />}
        </div>
        {!command && saved.module && <saved.module.SavedSearchFooter query={text} state={saved.state} onSave={() => manage(true)} />}
        {!command && saved.failed && <div role="alert" className="px-4 py-2 text-xs text-red">{t("palette.saved_searches_failed")} <button type="button" className="text-accent-ink underline" onClick={saved.retry}>{t("palette.saved_searches_retry")}</button></div>}
        {listable && saved.module && <saved.module.FilterAgentsList query={text} onFilter={filterList} show={showAgentsFilter} />}
        <div className="flex items-center gap-3 border-t border-line bg-inset px-4 py-2 text-[11px] text-ink-3 [@media(pointer:coarse)]:hidden">
          <span className="flex items-center gap-1">
            <Kbd>↑</Kbd>
            <Kbd>↓</Kbd> {t("palette.select")}
          </span>
          <span className="flex items-center gap-1">
            <Kbd>↵</Kbd> {command ? t("palette.run") : t("palette.open")}
          </span>
          {shown[active]?.kind === "file" && (
            <span className="flex items-center gap-1">
              <Kbd>{chordParts("Mod+Enter")[0]}</Kbd>
              <Kbd>↵</Kbd> {t("files-find.copy_mention")}
            </span>
          )}
          {!command && (
            <span className="flex items-center gap-1">
              <Kbd>&gt;</Kbd> {t("palette.commands")}
            </span>
          )}
        </div>
        </>) : kept.current}
      </div>
    </div>,
    document.body,
  );
}
