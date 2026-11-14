// ⌘K: Beautiful UI's Search, over every agent and Persona conversation — and the terminal panes of the computers
// (lib/terminals.ts, the /terminal page's data). `>` first (or ⌘⇧P) is command mode: the registered commands with their
// shortcuts, and whatever other modules add through registerContextCommands (lib/palette.ts). docs/web/web.md"Keyboard shortcuts".
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { enc, api } from "../lib/api";
import { machineWithHub } from "../lib/mesh";
import { createPortal } from "react-dom";
import { ago, sessionTitle, shortPath, stateOf, tabLabel } from "../lib/format";
import { appCommands, commandKeys, contextCommands, filterCommands, inboxHits, matchPaletteWords, orderGroups, parseQuery, placeHits, openPaletteWith, setPaletteQuery, terminalHits, usePalette, type InboxHit, type MachinePanes, type PaletteCommand, type PlaceHit, type PlaceInput, type TerminalHit } from "../lib/palette";
import { isDone, itemRoute, useInbox, type InboxItem } from "../lib/inbox";
import { useNavPages } from "./Sidebar";
import type { EvidenceSnippet } from "../lib/agent-chatlist-filter";
import { storedValue } from "../lib/compatibility";
import { useScope } from "../lib/current-assistant";
import { SESSIONS_LIST_FEATURE } from "../lib/session-list";
import { SESSIONS_TODO_FEATURE } from "../lib/session-groups";
import { GROUPING_KEY, pagedAgentList } from "../lib/sidebar-api";
import { filterAgentList } from "../lib/agent-list-filters";
import { conversationPage } from "../lib/conversation-page";
import { conversationTitle } from "./assistant/conversations";
import { href, navigate, type Route } from "../lib/router";
import { composing } from "../lib/send-history";
import { useAgentSessions } from "../lib/sessions";
import { PALETTE_RECENT_CHATS, shownChats, useRecentChats } from "../lib/recent-chats";
import { chordParts, handlerOf, runShortcut, SCOPE_LABEL, useBindings } from "../lib/shortcuts";
import { useStore } from "../lib/store";
import { buildTerminalTree, type PanesResponse } from "../lib/terminals";
import type { Session } from "../lib/types";
import { AgentMark, SessionMark } from "../ui/AgentMark";
import { CARD_OUT, EASE, EXIT_MS, IconButton, Kbd, toast, useExit } from "../ui/atoms";
import { StatusMark } from "../ui/StatusMark";
import { Icon } from "../ui/icons";
import { useEscapeClose, useModalFocus } from "../ui/overlay";
import { copyFileMention, FilesHeading, FilesNotice, FileResultRow, openFileHit, usePaletteFiles, type FileRow } from "./palette/FilesSection";
import { AgentConditions } from "./palette/AgentConditions";
import { useSavedSearches, type SavedSearchPanel } from "./palette/SavedSearches";
import type { SavedAgentSearch } from "../lib/saved-agent-searches";
import { runtimeSearchKey, useRuntimeSearch } from "./palette/RuntimeSearch";
import { t } from "../i18n";
import { byTime } from "../lib/times";
import { STATE_LABEL } from "../lib/state-labels";
import { plainMarkdown } from "../lib/plain-markdown";
import { PHONE, useMedia } from "../lib/viewport";
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
const chatTitle = (s: Session, persona: boolean) => (persona ? conversationTitle(s) : sessionTitle(s));

/** A chat's match by its title (starting with what was typed 100, containing it 80), then every word in its folder,
 *  computer or last line (40): a Persona chat's, and an Agent's until lib/agent-search.ts has loaded. */
function score(s: Session, q: string, machine: string, persona = false): number {
  return matchPaletteWords(chatTitle(s, persona), q, [s.cwd, s.space_name, s.slot_name, machine, s.last_text].join(" "));
}

const HOME: Route = { name: "home" };

/** The conversation "Go back" opens (#2191): the newest opened here, other than the one on the page, that still exists. */
function previousChat(recent: { entries: string[]; current: string }, sessions: Session[], conversations: Session[]): { s: Session; to: Route } | null {
  const find = (k: string): { s: Session; to: Route } | null => {
    if (k.startsWith("j:")) {
      const s = conversations.find((c) => `j:${c.session_id}` === k);
      return s ? { s, to: { name: "persona", sid: s.session_id } } : null;
    }
    const s = sessions.find((c) => `s:${c.machine_id}/${c.session_id}` === k);
    return s ? { s, to: { name: "session", mid: s.machine_id, sid: s.session_id } } : null;
  };
  const [key] = shownChats(recent.entries, recent.current, (k) => find(k) !== null, 1);
  return key ? find(key) : null;
}

/** `listShown`: the sidebar's column, with its Agents list, is on screen (not a narrow screen's drawer or the rail). */
export function Palette({ open, onClose, onNewAgent, pane, route = HOME, listShown = true }: { open: boolean; onClose: () => void; onNewAgent: () => void; pane?: { mid: string; sid: string }; route?: Route; listShown?: boolean }) {
  // what is typed lives in the store (lib/palette.ts): the shortcuts open it in a mode (`>` for commands) and read it
  const q = usePalette().query;
  const setQ = setPaletteQuery;
  const { mode, text } = parseQuery(q);
  const [savedPanel, setSavedPanel] = useState<SavedSearchPanel | null>(null);
  const saved = useSavedSearches(open && mode === "jump", savedPanel);
  const managing = savedPanel?.scope === saved.scope ? savedPanel : null;
  useEffect(() => { if (savedPanel && !managing) setSavedPanel(null); }, [savedPanel, managing]);
  // Both search tools arrive with the opened palette, outside its idle chunk. Their data lifetimes remain
  // independent: saved queries follow the default manager; runtime matches are hub-wide and require typed input.
  const agentSearch = saved.module;
  // `machine:`, `project:`, `is:pinned`, `since:`… speak of Agent sessions: with one, only they are listed (#6589)
  const operators = mode === "jump" && Boolean(agentSearch?.hasSearchOperators(text));
  const [selection, setSelection] = useState<{ query: string; key: string }>({ query: q, key: "" });
  const [box, setBox] = useState<{ top: number; height: number } | null>(null);
  const sessions = useAgentSessions();
  // the conversations opened here, newest first, and the one on the page (#2191)
  const recentChats = useRecentChats();
  const assistant = useStore((s) => s.assistant);
  const machines = useStore((s) => s.machines);
  const pins = useStore((s) => s.pins);
  const interactions = useStore((s) => s.interactions);
  const bySession = useStore((s) => s.sessions);
  // "Filter Agents list" narrows the complete list with what was typed, the hub's paged list with its words alone (the
  // hub matches the query as text; its operators are the complete list's)
  const bounded = useStore((s) => s.features.includes(SESSIONS_LIST_FEATURE));
  const runtimeSearchOn = useStore((s) => s.features.includes("sessions_search"));
  const runtimeSearch = useRuntimeSearch({ open: open && !managing, jump: mode === "jump", query: text, capable: runtimeSearchOn, bounded, module: agentSearch, loadError: saved.failed ? t("palette.saved_searches_failed") : "", owner: saved.owner });
  const byTodo = useStore((s) => s.features.includes(SESSIONS_TODO_FEATURE));
  const todosSupported = useStore((s) => s.todosSupported);
  const several = useScope().several;
  const completeList = !pagedAgentList({ bounded, byTodo, todosSupported, pins, stored: storedValue(GROUPING_KEY), several });
  const panesOn = useStore((s) => s.features.includes("terminal_panes"));
  // Persona chats found by what was said in them too (#6283): the manager's search, merged into the local matches
  const searchOn = useStore((s) => s.features.includes("assistant_search"));
  const bindings = useBindings();
  // "Inbox" and "Go to" (the Mac's quick open): the open inbox items, and the sidebar's pages with Persona and "All chats"
  const inboxItems = useInbox((s) => s.items);
  // every page, the ones under More too: finding a page is what this is for
  const { all: pages } = useNavPages();
  // the pages come anew each render: kept by what they are, so the list (and the highlight under it) stays put
  const hasAssistant = Boolean(assistant);
  const pagesKey = pages.map((p) => [p.k, p.label, p.icon, href(p.to)].join("\u0001")).join("\n");
  const places = useMemo<PlaceInput[]>(
    () => [
      ...(hasAssistant ? [{ key: "persona", title: t("palette.place_persona"), icon: "spark" as const, to: { name: "persona" } as Route }, { key: "history", title: t("sidebar.all_chats"), icon: "list" as const, to: { name: "history" } as Route }] : []),
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

  // The terminal panes of the online computers, read when the palette opens (the /terminal page's endpoint).
  const onlineKey = machines.filter((m) => m.online).map((m) => m.id).join("\n");
  const [trees, setTrees] = useState<Record<string, PanesResponse>>({});
  useEffect(() => {
    if (!open || !panesOn || !onlineKey) return;
    let live = true;
    for (const mid of onlineKey.split("\n")) {
      api<PanesResponse>(`/machines/${enc(mid)}/panes`).then(
        (res) => live && setTrees((t) => ({ ...t, [mid]: res })),
        () => {}, // a computer that cannot be read just has no panes here
      );
    }
    return () => {
      live = false;
    };
  }, [open, panesOn, onlineKey]);
  const machinePanes = useMemo<MachinePanes[]>(
    () =>
      machines.flatMap((m) =>
        trees[m.id] ? [{ mid: m.id, machine: machineWithHub(m.id, machines), spaces: buildTerminalTree(trees[m.id], bySession[m.id] ?? []), sessionIds: new Set((bySession[m.id] ?? []).map((x) => x.session_id)) }] : [],
      ),
    [machines, trees, bySession],
  );

  // loaded on intent (the palette is idle-tier, src/startup-preload.ts IDLE_TIER_BUDGET): the first query typed loads
  // lib/assistant-search.ts; asked 200 ms after the last key, each query cancelling the one before
  const [found, setFound] = useState<{ q: string; answer?: AssistantSearchAnswer; marks?: typeof import("../lib/assistant-search").markParts }>({ q: "" });
  // a found chat's line starts just before its match, as much of a lead-in as the row's width holds (#7045)
  const phone = useMedia(PHONE);
  const asked = open && !managing && mode === "jump" && !operators && searchOn && Boolean(assistant) ? text.trim() : "";
  useEffect(() => {
    if (!asked) return;
    const ctrl = new AbortController();
    const id = window.setTimeout(() => {
      void import("../lib/assistant-search")
        .then((m) => m.searchAssistantChats(asked, ctrl.signal).then((answer) => setFound({ q: asked, answer: answer ?? undefined, marks: m.markParts })))
        .catch(() => {}); // a failed search leaves the local matches
    }, 200);
    return () => {
      clearTimeout(id);
      ctrl.abort();
    };
  }, [asked]);
  const said = { answer: asked && found.q === asked ? found.answer : undefined };
  const hits = useMemo((): Row[] => {
    if (mode !== "jump") return [];
    const query = text.toLowerCase();
    let out: Hit[] = [];
    // an Agent as the list's search found it: its operators, then its name, its project or computer, what is inside it
    const ctx = { machines, pins, interactions, now: Date.now() };
    for (const s of sessions) {
      const found = agentSearch ? agentSearch.agentSearchHit(s, text, ctx) : { score: score(s, query, machineWithHub(s.machine_id, machines)), snippet: undefined };
      if (found.score) out.push({ key: `s:${runtimeSearchKey(s)}`, s, to: { name: "session", mid: s.machine_id, sid: s.session_id }, score: found.score + (stateOf(s) === "offline" ? 0 : 5), evidence: found.snippet });
    }
    // The server's grouped matches and evidence are never matched again against truncated summaries.
    const merged = runtimeSearch.mergeHits?.(out, sessions, runtimeSearch);
    const remoteKeys = merged?.remoteKeys ?? new Set<string>();
    out = merged?.hits ?? out;
    for (const s of operators ? [] : assistant?.conversations ?? []) {
      const sc = score(s, query, "persona", true);
      if (sc) out.push({ key: `j:${s.session_id}`, s, to: conversationPage(s.session_id, s.machine_id, s.manager), assistant: true, score: sc });
    }
    // what the manager found: a chat found by a message opens at it (not one found by its title); one not listed here
    // (archived) is the result's own row
    for (const r of said.answer?.results ?? []) {
      const key = `j:${r.session_id}`;
      const hit = r.hits[0];
      const at = r.title_match ? undefined : hit?.item_id;
      const known = out.find((h) => h.key === key);
      if (known) {
        known.said = hit;
        known.to = conversationPage(r.session_id, known.s.machine_id, known.s.manager, at);
        continue;
      }
      const s = (assistant?.conversations ?? []).find((c) => c.session_id === r.session_id)
        ?? ({ machine_id: r.machine_id, session_id: r.session_id, title: r.title, cwd: "", status: "idle", assistant: true, archived: r.archived, updated_at: r.updated_at } as Session);
      out.push({ key, s, to: conversationPage(r.session_id, s.machine_id, s.manager, at), assistant: true, score: r.title_match ? 80 : 40, said: hit });
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
    const go = (operators ? [] : placeHits(places, text)).map((h) => ({ kind: "place" as const, key: h.key, hit: h, score: h.score }));
    const savedRows: Row[] = !query && !saved.state.unsupported ? (saved.state.document?.searches ?? []).slice(0, 5).map((item) => ({ kind: "saved", key: `saved:${item.id}`, item })) : [];
    // Loaded runtime pages stay reachable; unrelated local groups retain their existing cap.
    let others = 0;
    const window = all.filter((row) => remoteKeys.has(row.key) || others++ < 40);
    return [...recentRows, ...savedRows, ...orderGroups<Row & { score: number }>([window, mail, go]).flat()];
  }, [mode, text, operators, agentSearch, sessions, assistant, machines, pins, interactions, machinePanes, inboxItems, places, recentChats, said.answer, saved.state.document, saved.state.unsupported, runtimeSearch.hits, runtimeSearch.loaded, runtimeSearch.supported, runtimeSearch.loading, runtimeSearch.loadingMore, runtimeSearch.hasMore, runtimeSearch.error, runtimeSearch.moreError, runtimeSearch.mergeHits]);

  // command mode: what other modules offer for here, then the registered commands with their shortcuts
  const commands = useMemo((): Row[] => {
    if (!open || mode !== "command") return [];
    const run = (id: string, digit?: number) => {
      if (!runShortcut(id, { digit })) toast(t("palette.command_unavailable"));
    };
    // Go back to the previous conversation (#2191): the newest opened here other than the one on the page that still exists
    const back = previousChat(recentChats, sessions, assistant?.conversations ?? []);
    const extra: PaletteCommand[] = [
      ...(back ? [{ id: "app:previous-chat", title: t("palette.previous_chat"), detail: chatTitle(back.s, back.to.name === "persona"), section: SCOPE_LABEL.global, icon: "undo" as const, keywords: "back previous last recent 返回 上一个 最近", run: () => navigate(back.to) }] : []), // i18n-ignore: search words in both languages, as PLACE_WORDS
      { id: "app:new-agent", title: t("palette.new_agent"), section: SCOPE_LABEL.global, icon: "plus", keywords: "new agent", run: onNewAgent },
      { id: "app:new-conversation", title: t("palette.new_conversation"), section: SCOPE_LABEL.global, icon: "chat", keywords: "persona new", run: () => navigate({ name: "persona-new" }) },
    ];
    const all = [...contextCommands({ route, pane }), ...appCommands({ bindings, available: (id) => Boolean(handlerOf(id)), run, machines: machines.map((m) => ({ id: m.id, name: m.name })), extra })];
    return filterCommands(all, text).map((cmd) => ({ kind: "command" as const, key: cmd.id, cmd }));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `route` is parsed anew each render; its path says it all
  }, [open, mode, text, bindings, machines, pane?.mid, pane?.sid, href(route), onNewAgent, recentChats, sessions, assistant]);
  // the files under the directory of the page (#2438): listed after the conversations and panes
  const files = usePaletteFiles({ open: open && !managing, jump: mode === "jump" && !operators, text, route, pane, machinePanes });
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
  // the field takes the focus on every open (opened again on its way out, it is not mounted anew)
  useEffect(() => {
    if (open && !managing) input.current?.focus({ preventScroll: true });
  }, [open, managing]);

  if (managing && saved.module && saved.model) {
    const finish = (query: string) => { setSavedPanel(null); openPaletteWith(query); };
    return <saved.module.SavedSearchesDialog key={saved.scope} model={saved.model} initialQuery={managing.initialQuery} onReturn={() => finish(managing.returnQuery)} onApply={finish} />;
  }
  if (!exit.mounted) return null;
  const pick = (r: Row) => {
    if (r.kind === "saved") { setQ(r.item.query); input.current?.focus({ preventScroll: true }); return; }
    onClose();
    if (r.kind === "command") r.cmd.run();
    else if (r.kind === "file") openFileHit(r);
    else if (r.kind === "inbox") navigate(itemRoute(r.hit.item));
    else if (r.kind === "place") navigate(r.hit.place.to);
    else navigate(r.hit.to);
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
  const listable = !command && text !== "" && (completeList || (Boolean(agentSearch) && !operators)) && hits.some((r) => r.kind === "session" && !r.hit.assistant);
  const filterList = () => {
    onClose();
    filterAgentList(text);
    if (!listShown) navigate({ name: "agent-list" });
  };

  return createPortal(
    <div ref={backdrop} data-closing={exit.leaving || undefined} inert={exit.leaving || undefined} className="fixed inset-0 z-40 flex items-start justify-center bg-black/20 px-4 pt-[14vh] backdrop-blur-[2px] dark:bg-black/45" style={{ animation: "fade-in 120ms ease-out both" }} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={card} role="dialog" aria-modal="true" tabIndex={-1} data-shortcut-layer={open || undefined} aria-label={label} className="w-full max-w-[560px] overflow-hidden rounded-[14px] bg-surface shadow-overlay outline-none" style={{ animation: `slide-up 200ms ${EASE} both` }}>
        {/* on the way out it shows what it showed when open: closed, the lists below come back empty */}
        {open ? (kept.current = <>
        <label className="flex h-12 items-center gap-2.5 border-b border-line px-4">
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
                  void copyFileMention(row);
                } else if (row) pick(row);
              }
            }}
            placeholder={command ? t("palette.command_placeholder") : files.ctx ? t("files-find.searching_placeholder") : t("palette.search_placeholder")}
            className="min-w-0 flex-1 bg-transparent text-[14px] text-ink outline-none placeholder:text-ink-3"
          />
          {/* the Agents list's filter choices, written into the query as its operators (#6589) */}
          {!command && <AgentConditions query={text} onQuery={(next) => setQ(next && `${next} `)} search={agentSearch} want={saved.retry} sessions={sessions} onDone={() => input.current?.focus({ preventScroll: true })} />}
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
            const st = stateOf(h.s);
            // a Persona chat says what was said last (its folder is the assistant's own, the same for every chat); an Agent
            // where it runs
            const meta = h.assistant ? plainMarkdown(h.s.last_text) || "Persona" : [[machineWithHub(h.s.machine_id, machines), h.s.space_name, tabLabel(h.s)].filter(Boolean).join(" › "), shortPath(h.s.cwd)].filter(Boolean).join(" · ");
            return {
              key: h.key,
              heading: sessionsHeading,
              row: (
              <button ref={ref} type="button" {...option} onMouseMove={() => setActive(i)} onClick={() => pick(r)} className={rowClass}>
                {!h.assistant && <SessionMark s={h.s} size={14} />}
                <span className="flex size-5 shrink-0 items-center justify-center">{h.assistant ? <Icon name="spark" size={13} className="text-ink-3" /> : <StatusMark state={st} />}</span>
                <span className="min-w-0 flex-1">
                  <span className="flex min-w-0 items-center gap-1.5">
                    <span className="truncate text-[13px] font-medium text-ink">{chatTitle(h.s, Boolean(h.assistant))}</span>
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
        {listable && saved.module && <saved.module.FilterAgentsList query={text} onFilter={filterList} />}
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
