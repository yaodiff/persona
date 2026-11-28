// "Messages" (docs/extensions/lark-migration.md "Entry", #392's narrow exception): every Feishu private and group chat, for a browser
// that turned on Settings › Connectors › Lark › Show messages on home, on a hub that reads Feishu (feature `lark`). Not a second Feishu: one list and the inbox's own chat view
// (LarkThread: reading, sending, Pins), no contacts or workbench. Wide: the list (380px) and the chat side by side;
// narrow: the chat takes the list's place, with a way back. The pinned chats sit on top as a row of faces.
// The personal Agents (lib/agents.ts) are listed with the chats, the latest message first (mixed-feed.ts), each with its
// own face and its service's name; one opens here too (`/messages/agent/<session id>`), in the Agents page's own
// conversation view without its title row (views/agents/AgentsView.tsx AgentConversation).
import { memo, useEffect, useMemo, useRef, useState } from "react";
import { providerLabel, refreshAgentSessions, togglePin, updateAgentPrefs, useAgentList, useAgentPrefs, type AgentSession } from "../../lib/agents";
import { shortTime } from "../../lib/inbox";
import { accountTitle, larkAccounts, useLarkAccounts, type LarkAccountsState } from "../../lib/lark-accounts";
import { useLarkDisplayState } from "../../lib/lark-display";
import { entryOf, type LarkEntry } from "../../lib/lark-history";
import { useLarkHome } from "./home";
import {
  arrangeConversations, conversationPreview, larkChatsFor, unreadBadge, useLarkChats, type LarkChatsStore, type LarkConversation,
} from "./lark-chats";
import { isResumed } from "../../lib/reconnect";
import { href, navigate, onLinkClick, type Route } from "../../lib/router";
import type { ModulePageProps } from "../../core/module-host";
import { onHubEvent, useLarkOn } from "../../lib/store";
import { t } from "../../i18n";
import { Button, cx, IconButton, Loading, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Chip, Count, LarkAvatar } from "../../views/inbox/InboxParts";
import { LarkThread } from "../../views/inbox/LarkThread";
import { AgentAvatar } from "../../views/agents/AgentAvatar";
import { QuotaBattery, QuotaRing } from "../../views/agents/AgentQuota";
import { AgentConversation } from "../../views/agents/AgentsView";
import { agentKey, agentPreview, arrangeFeed, FEED_FILTERS, messagesTarget, type FeedFilter, type FeedItem } from "./mixed-feed";
import { useMessagesMix } from "./mix";

const SETTINGS = "/settings?tab=connectors";

/** The page's address in the app (the lark module's route, module.ts): the list, or one chat. */
export const messagesRoute = (id?: string): Route => ({ name: "module", module: "lark", base: "messages", path: id ? [id] : [] });
/** A personal Agent opened on the page: two segments, so no Feishu chat id is read as one (mixed-feed.ts messagesTarget). */
export const agentMessagesRoute = (id: string): Route => ({ name: "module", module: "lark", base: "messages", path: ["agent", id] });
const itemRoute = (x: FeedItem): Route => (x.kind === "agent" ? agentMessagesRoute(x.session.id) : messagesRoute(x.chat.id));

/** The module's page (core/module-host.ts ModuleCode.Page): `/messages`, `/messages/<chat id>`, `/messages/agent/<id>`. */
export function MessagesPage({ path, onToggleSidebar }: ModulePageProps) {
  const target = messagesTarget(path);
  return <Messages id={target?.kind === "lark" ? target.id : undefined} agent={target?.kind === "agent" ? target.id : undefined} onToggleSidebar={onToggleSidebar ?? (() => {})} />;
}

// The list's filter, kept for this tab while the page comes and goes.
let lastFilter: FeedFilter = "all";

export function Messages({ id: larkId, agent, onToggleSidebar }: { id?: string; agent?: string; onToggleSidebar: () => void }) {
  const id = agent ? undefined : larkId;
  const open = Boolean(id || agent);
  const lark = useLarkOn();
  const larkSwitch = useLarkHome();
  const on = lark && larkSwitch;
  const accounts = useLarkAccounts();
  // The chosen Feishu account's list (docs/extensions/feishu.md "Multiple accounts"): each account keeps its own.
  const larkChats = larkChatsFor(accounts.request);
  const s = useLarkChats(larkChats);
  const [pins, setPins] = useState(false);
  const chat = useMemo(() => (id ? [...s.pinned, ...s.rows].find((c) => c.id === id) : undefined), [id, s.pinned, s.rows]);
  // Where the opened chat lands (its first unread message), taken from its row once, when the row (or the list without
  // it) is first there: reading moves the row's read position, and the chat must not jump to it.
  const entryKey = `${accounts.request ?? ""}:${id ?? ""}`;
  // A row the loaded pages do not hold is looked for further down the list first.
  const [entry, setEntry] = useState<{ key: string; at: LarkEntry } | null>(null);
  if (id && entry?.key !== entryKey && chat) setEntry({ key: entryKey, at: entryOf(chat) });
  const missing = Boolean(id) && !chat && s.loaded && entry?.key !== entryKey;
  useEffect(() => {
    if (!missing || !id) return;
    let live = true;
    void larkChats.find(id).then((row) => live && setEntry((cur) => (cur?.key === entryKey ? cur : { key: entryKey, at: entryOf(row) })));
    return () => {
      live = false;
    };
  }, [missing, id, entryKey, larkChats]);
  const landing = entry?.key === entryKey ? entry.at : null;

  useEffect(() => setPins(false), [id]);
  // The personal Agents: the app keeps their list (lib/store.ts reads it on sign-in, on an `agents` push and after a
  // reconnect it could not resume); a page opened before it was ever read reads it now.
  const agentList = useAgentList();
  const agentPrefs = useAgentPrefs();
  const mix = useMessagesMix();
  const agentSession = agent ? agentList.sessions.find((x) => x.id === agent) : undefined;
  const agentPinned = Boolean(agent && agentPrefs.pins.includes(agent));
  useEffect(() => {
    if (on && (mix || agent) && !agentList.loaded) void refreshAgentSessions(false);
  }, [on, mix, agent, agentList.loaded]);
  useEffect(() => {
    if (!on) return;
    void larkChats.refresh();
    void larkAccounts.refresh();
    // A chat changed (the hub's coalesced `lark` push): read the loaded window again (and the accounts' unread counts),
    // once things settle.
    let timer: ReturnType<typeof setTimeout> | undefined;
    const settle = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        void larkChats.refresh();
        void larkAccounts.refresh();
      }, 800);
    };
    const off = onHubEvent("lark", (msg) => {
      if (Array.isArray(msg.feed_ids) && !msg.feed_ids.length) return;
      settle();
    });
    // A reconnect the hub could not resume: the `lark` pushes missed meanwhile are not replayed.
    const offHello = onHubEvent("hello", (msg) => !isResumed(msg) && settle());
    return () => {
      off();
      offHello();
      clearTimeout(timer);
    };
  }, [on, larkChats]);

  // Another account picked: an open chat of the account left behind closes (not when the list first arrives and puts
  // the page on the account chosen last time).
  const shownAccount = useRef(accounts.request);
  const listed = useRef(false);
  useEffect(() => {
    const switched = listed.current && shownAccount.current !== accounts.request;
    shownAccount.current = accounts.request;
    listed.current = accounts.accounts.length > 0;
    if (switched && id) navigate(messagesRoute());
  }, [accounts.request, accounts.accounts.length, id]);
  const title = agent ? agentSession?.title || t("lib-format.untitled_session") : chat?.name || t("lark-messages.lark_chat");

  if (!on) {
    return (
      <div className="flex h-full flex-col">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
          <IconButton icon="sidebar" label={t("lark-messages.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
          <h1 className="min-w-0 flex-1 truncate text-[14px] font-semibold text-ink">{t("lark-messages.title")}</h1>
        </header>
        <div className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center">
          <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3"><Icon name="chat" size={18} /></span>
          {lark ? (
            <>
              <p className="text-[13.5px] font-medium text-ink">{t("lark-messages.off_title")}</p>
              <p className="max-w-[340px] text-tree-row text-ink-3">{t("lark-messages.off_hint")}</p>
              <Button tone="secondary" icon="gear" className="mt-2" onClick={() => navigate(SETTINGS)}>{t("lark-messages.goto_lark_settings")}</Button>
            </>
          ) : (
            <>
              <p className="text-[13.5px] font-medium text-ink">{t("lark-messages.no_lark_title")}</p>
              <p className="max-w-[340px] text-tree-row text-ink-3">{t("lark-messages.no_lark_hint")}</p>
            </>
          )}
        </div>
      </div>
    );
  }

  return (
    <div className="@container flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("lark-messages.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        {open && <IconButton icon="chevronLeft" label={t("lark-messages.back")} onClick={() => navigate(messagesRoute())} className="@4xl:hidden" />}
        <h1 className="min-w-0 flex-1 truncate text-[14px] font-semibold text-ink">
          {open ? <><span className="@4xl:hidden">{title}</span><span className="hidden @4xl:inline">{t("lark-messages.title")}</span></> : t("lark-messages.title")}
        </h1>
        {s.loading && s.loaded && <Spinner />}
        {id && <IconButton icon="pin" label={t("lark-messages.pin_messages")} onClick={() => setPins(true)} />}
        {agent && (
          <IconButton icon="pin" label={agentPinned ? t("lark-messages.unpin_agent") : t("lark-messages.pin_agent")} onClick={() => updateAgentPrefs((x) => togglePin(x, agent))}
            className={agentPinned ? "text-accent" : undefined} />
        )}
        <IconButton icon="retry" label={t("lark-messages.refresh")} onClick={() => void larkChats.refresh()} disabled={s.loading} />
        <IconButton icon="gear" label={t("lark-messages.lark_settings")} onClick={() => navigate(SETTINGS)} />
      </header>
      <div className="flex min-h-0 flex-1">
        <section aria-label={t("lark-messages.chat_list")} className={cx("min-h-0 w-full flex-col @4xl:flex @4xl:w-[380px] @4xl:shrink-0 @4xl:border-r @4xl:border-line", open ? "hidden" : "flex")}>
          <ChatList selected={agent ? agentKey(agent) : id} store={larkChats} accounts={accounts} agents={mix ? agentList.sessions : []} agentPins={agentPrefs.pins} />
        </section>
        <section aria-label={t("lark-messages.chat")} className={cx("min-h-0 min-w-0 flex-1 flex-col @4xl:flex", open ? "flex" : "hidden")}>
          {agent ? (
            <div className="flex h-full min-h-0 flex-col">
              <AgentHeader session={agentSession} />
              <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
                <AgentConversation id={agent} />
              </div>
            </div>
          ) : id ? (
            <div className="flex h-full min-h-0 flex-col">
              <ChatHeader chat={chat} accountName={accounts.selectedName} account={accounts.selected} />
              <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
                {landing ? (
                  <LarkThread key={entryKey} chat={{ feed: id, title: chat?.name || t("lark-messages.lark_chat"), topic: chat?.is_topic, account: accounts.request,
                    group: chat?.is_group, ...landing }} pinsOpen={pins} onPinsOpen={setPins} />
                ) : <div className="p-5"><Loading label={t("inbox-larkthread.loading")} /></div>}
              </div>
            </div>
          ) : (
            <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
              <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3"><Icon name="chat" size={18} /></span>
              <p className="text-[13.5px] font-medium text-ink">{t("lark-messages.pick_title")}</p>
              <p className="text-tree-row text-ink-3">{t("lark-messages.pick_hint")}</p>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}

/** The opened chat's name over it (wide only: narrow puts it in the page's title row); whose account it is while
 * more than one is signed in. */
function ChatHeader({ chat, accountName, account }: { chat?: LarkConversation; accountName?: string; account?: number }) {
  if (!chat) return null;
  return (
    <div className="hidden shrink-0 items-center gap-3 border-b border-line px-4 py-2.5 @4xl:flex sm:px-6">
      <LarkAvatar name={chat.name} size={28} avatarURL={chat.avatar_url} avatar={chat.avatar} account={account} />
      <div className="min-w-0 flex-1">
        <p className="truncate text-[13.5px] font-semibold text-ink">{chat.name}</p>
        <p className="truncate text-submeta text-ink-3">{[chat.is_topic ? t("lark-messages.kind_topic") : chat.is_group ? t("lark-messages.kind_group") : t("lark-messages.kind_dm"), accountName].filter(Boolean).join(" · ")}</p>
      </div>
    </div>
  );
}

/** The opened Agent's face and name over its conversation (wide only, as ChatHeader), with its service's name. */
function AgentHeader({ session }: { session?: AgentSession }) {
  if (!session) return null;
  return (
    <div className="hidden shrink-0 items-center gap-3 border-b border-line px-4 py-2.5 @4xl:flex sm:px-6">
      <QuotaRing quota={session.quota} size={28}><AgentAvatar s={session} size={28} /></QuotaRing>
      <div className="min-w-0 flex-1">
        <p className="flex min-w-0 items-center gap-2"><span className="truncate text-[13.5px] font-semibold text-ink">{session.title || t("lib-format.untitled_session")}</span><QuotaBattery quota={session.quota} /></p>
        <p className="truncate text-submeta text-ink-3">{[t("lark-messages.kind_agent"), providerLabel(session.provider)].join(" · ")}</p>
      </div>
    </div>
  );
}

const filterLabel = (f: FeedFilter) => (f === "lark" ? t("lark-messages.filter_lark") : f === "agents" ? t("lark-messages.filter_agents") : t("lark-messages.filter_all"));

function ChatList({ selected, store: larkChats, accounts, agents, agentPins }: {
  selected?: string; store: LarkChatsStore; accounts: LarkAccountsState; agents: readonly AgentSession[]; agentPins: readonly string[];
}) {
  // Keeps the account's message display settings loaded and reread after a reconnect, ready for the chat that opens.
  useLarkDisplayState(accounts.request);
  const s = useLarkChats(larkChats);
  const [query, setQuery] = useState("");
  const [filterState, setFilterState] = useState<FeedFilter>(lastFilter);
  const end = useRef<HTMLDivElement>(null);
  // Agents shown at all: no filter row (and the Feishu list as before) while there are none.
  const withAgents = agents.length > 0;
  const filter = withAgents ? filterState : "all";
  const setFilter = (f: FeedFilter) => setFilterState((lastFilter = f));
  const { pinned: larkPinned, rest } = arrangeConversations(s.pinned, s.rows);
  const feed = arrangeFeed({ larkPinned, larkRows: rest, larkMore: Boolean(s.cursor), agents, agentPins, mix: withAgents, filter, query });
  const shown = feed.rows;
  const showLark = filter !== "agents";

  // The next page as the list nears its end (not while searching: the search covers what is loaded).
  useEffect(() => {
    const el = end.current;
    if (!el || !s.cursor || query || s.moreProblem || !showLark) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && void larkChats.more(), { rootMargin: "240px" });
    io.observe(el);
    return () => io.disconnect();
  }, [s.cursor, query, s.moreProblem, s.loading, s.rows.length, showLark]);

  useEffect(() => {
    if (!selected) return;
    document.querySelector(`[data-chat="${CSS.escape(selected)}"]`)?.scrollIntoView({ block: "nearest" });
  }, [selected, s.loaded]);

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto max-w-[760px] px-3 pt-3 pb-16 sm:px-4">
        <label className="flex h-8 items-center gap-2 rounded-control bg-surface px-2.5 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
          <Icon name="search" size={13} stroke={2} className="shrink-0 text-ink-3" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("lark-messages.search_placeholder")} aria-label={t("lark-messages.search_label")} className="min-w-0 flex-1 bg-transparent text-tree-row text-ink outline-none placeholder:text-ink-3" />
          {query && <IconButton icon="x" label={t("lark-messages.clear")} size={20} iconSize={11} onClick={() => setQuery("")} />}
        </label>

        {withAgents && (
          <div role="group" aria-label={t("lark-messages.filter_label")} className="mt-2.5 flex gap-1.5 overflow-x-auto pb-0.5">
            {FEED_FILTERS.map((f) => <Chip key={f} on={f === filter} onClick={() => setFilter(f)}>{filterLabel(f)}</Chip>)}
          </div>
        )}

        {showLark && accounts.accounts.length > 1 && (
          <div role="group" aria-label={t("lark-messages.accounts")} className="mt-2.5 flex gap-1.5 overflow-x-auto pb-0.5">
            {accounts.accounts.map((a) => (
              <Chip key={a.id} on={a.id === accounts.selected} onClick={() => larkAccounts.select(a.id)} title={a.tenant}>
                <span className="inline-flex items-center gap-1.5">
                  {accountTitle(a)}
                  {a.unread > 0 && <span className="text-[11px] tabular-nums opacity-70">{a.unread > 99 ? "99+" : a.unread}</span>}
                </span>
              </Chip>
            ))}
          </div>
        )}

        {showLark && s.problem && (
          <div className="mt-3 flex items-start gap-3 rounded-[14px] bg-surface px-3 py-3 shadow-card">
            <Icon name="info" size={14} className="mt-0.5 shrink-0 text-orange" />
            <p className="min-w-0 flex-1 text-tree-row leading-relaxed text-ink-2">{s.problem.text}</p>
            {s.problem.kind === "other" ? (
              <Button tone="secondary" icon="retry" busy={s.loading} onClick={() => void larkChats.refresh()}>{t("lark-messages.retry")}</Button>
            ) : (
              <Button tone="secondary" icon="gear" onClick={() => navigate(SETTINGS)}>{t("lark-messages.lark_settings")}</Button>
            )}
          </div>
        )}
        {showLark && !s.loaded && !s.rows.length && <div className="mt-6 px-1"><Loading label={t("lark-messages.loading")} /></div>}

        {!query && feed.pinned.length > 0 && <PinnedShelf items={feed.pinned} selected={selected} account={accounts.selected} />}

        {(s.loaded || !showLark) && !shown.length && !(showLark && s.problem) && (
          <div className="mt-10 flex flex-col items-center gap-2 text-center">
            <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3"><Icon name={query ? "search" : "chat"} size={17} /></span>
            <p className="text-[13.5px] font-medium text-ink">{query ? t("lark-messages.empty_no_match") : t("lark-messages.empty_none")}</p>
            <p className="text-tree-row text-ink-3">{query ? t("lark-messages.empty_no_match_hint") : t("lark-messages.empty_none_hint")}</p>
          </div>
        )}

        {shown.length > 0 && (
          <div className="mt-3 flex flex-col gap-1.5">
            {shown.map((x) => x.kind === "agent"
              ? <AgentRow key={x.key} s={x.session} selected={x.key === selected} time={x.at ? shortTime(new Date(x.at).toISOString()) : ""} />
              : <ChatRow key={x.key} c={x.chat} account={accounts.selected} selected={x.key === selected} time={shortTime(x.chat.updated_at)} />)}
          </div>
        )}
        {!query && showLark && s.moreProblem && (
          <div className="mt-3 flex items-center gap-3 px-1">
            <p className="min-w-0 flex-1 truncate text-label text-red">{s.moreProblem}</p>
            <Button tone="secondary" icon="retry" onClick={() => void larkChats.more()}>{t("lark-messages.retry")}</Button>
          </div>
        )}
        {!query && showLark && s.cursor && !s.moreProblem && <div ref={end} className="flex justify-center py-4"><Spinner /></div>}
      </div>
    </div>
  );
}

/** "Pinned": the pinned chats and Agents, a row of faces with their names and unread counts (Feishu's chats in the
 * gateway's order, the Agents put in by their latest message). */
function PinnedShelf({ items, selected, account }: { items: FeedItem[]; selected?: string; account?: number }) {
  return (
    <section aria-label={t("lark-messages.pinned_label")} className="mt-3">
      <p className="px-1 pb-1.5 text-label font-medium text-ink-3">{t("lark-messages.pinned")} <span className="text-submeta tabular-nums">{items.length}</span></p>
      <div className="flex gap-1 overflow-x-auto pb-1">
        {items.map((x) => {
          const to = itemRoute(x);
          const name = x.kind === "agent" ? x.session.title || t("lib-format.untitled_session") : x.chat.name;
          const b = x.kind === "agent" ? { count: x.session.unreadCount || undefined, dot: false, muted: false } : unreadBadge(x.chat);
          return (
            <a
              key={x.key}
              data-chat={x.key}
              href={href(to)}
              onClick={(e) => onLinkClick(e, to)}
              aria-current={x.key === selected ? "page" : undefined}
              title={x.kind === "agent" ? `${name} · ${providerLabel(x.session.provider)}` : name}
              className={cx("flex w-[64px] shrink-0 flex-col items-center gap-1 rounded-[12px] px-1 py-2 transition-colors hover:bg-hover", x.key === selected && "bg-inset shadow-[0_0_0_1.5px_var(--accent)]")}
            >
              <span className="relative">
                {x.kind === "agent" ? <QuotaRing quota={x.session.quota} size={40}><AgentAvatar s={x.session} size={40} /></QuotaRing> : <LarkAvatar name={x.chat.name} size={40} avatarURL={x.chat.avatar_url} avatar={x.chat.avatar} account={account} />}
                {(b.count || b.dot) && (
                  <span className="absolute -top-1 -right-1">
                    {b.count ? <Count n={b.count} accent={!b.muted} /> : <span className={cx("block size-2.5 rounded-full ring-2 ring-page", b.muted ? "bg-ink-3" : "bg-accent")} />}
                  </span>
                )}
              </span>
              <span className="w-full truncate text-center text-submeta text-ink-2">{name}</span>
            </a>
          );
        })}
      </div>
    </section>
  );
}

/** One personal Agent of the list, drawn like a chat: its own face, its service's name, its latest message and unread
 * count. */
const AgentRow = memo(function AgentRow({ s, selected, time }: { s: AgentSession; selected: boolean; time: string }) {
  const to = agentMessagesRoute(s.id);
  const preview = agentPreview(s, (text) => t("lark-chats.preview_mine", { text }));
  const unread = s.unreadCount > 0;
  return (
    <a
      data-chat={agentKey(s.id)}
      href={href(to)}
      onClick={(e) => onLinkClick(e, to)}
      aria-current={selected ? "page" : undefined}
      className={cx("flex scroll-my-3 gap-3 rounded-[14px] p-3 outline-offset-[-2px] transition-[background-color,box-shadow] duration-150 hover:bg-inset", selected ? "bg-inset shadow-[0_0_0_1.5px_var(--accent)]" : "bg-surface shadow-card")}
    >
      <QuotaRing quota={s.quota} size={40}><AgentAvatar s={s} size={40} /></QuotaRing>
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-1.5">
          <span className={cx("truncate text-[13.5px]", unread ? "font-semibold text-ink" : "font-medium text-ink")}>{s.title || t("lib-format.untitled_session")}</span>
          <span className="shrink-0 rounded-full bg-field px-1.5 text-submeta leading-[16px] font-medium whitespace-nowrap text-ink-3">{providerLabel(s.provider)}</span>
          <QuotaBattery quota={s.quota} />
          <span className="flex-1" />
          <span className="shrink-0 text-submeta text-ink-3 tabular-nums">{time}</span>
        </span>
        <span className="mt-0.5 flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-tree-row text-ink-2">{preview || t("lark-messages.kind_agent")}</span>
          {unread && <Count n={s.unreadCount} accent />}
        </span>
      </span>
    </a>
  );
});

/** One chat of the list. It draws again only when its props change: a refresh keeps the object of a chat that did not
 * change (LarkChatsStore), and the list works out the time label each time it draws, so a new day still moves it on. */
const ChatRow = memo(function ChatRow({ c, selected, time, account }: { c: LarkConversation; selected: boolean; time: string; account?: number }) {
  const to = messagesRoute(c.id);
  const b = unreadBadge(c);
  const preview = conversationPreview(c);
  return (
    <a
      data-chat={c.id}
      href={href(to)}
      onClick={(e) => onLinkClick(e, to)}
      aria-current={selected ? "page" : undefined}
      className={cx("flex scroll-my-3 gap-3 rounded-[14px] p-3 outline-offset-[-2px] transition-[background-color,box-shadow] duration-150 hover:bg-inset", selected ? "bg-inset shadow-[0_0_0_1.5px_var(--accent)]" : "bg-surface shadow-card")}
    >
      <LarkAvatar name={c.name} size={40} avatarURL={c.avatar_url} avatar={c.avatar} account={account} />
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-1.5">
          <span className={cx("truncate text-[13.5px]", b.count || b.dot ? "font-semibold text-ink" : "font-medium text-ink")}>{c.name}</span>
          {c.is_group && <Icon name="layers" size={11} className="shrink-0 text-ink-3" />}
          {c.muted && <Icon name="bellOff" size={11} className="shrink-0 text-ink-3" />}
          <span className="flex-1" />
          <span className="shrink-0 text-submeta text-ink-3 tabular-nums">{time}</span>
        </span>
        <span className="mt-0.5 flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-tree-row text-ink-2">
            {c.at_me && <span className="mr-1 font-medium text-attention">{c.at_all ? t("lark-messages.at_all") : t("lark-messages.at_me")}</span>}
            {preview}
          </span>
          {b.count ? <Count n={b.count} accent={!b.muted} /> : b.dot ? <span className={cx("size-2 shrink-0 rounded-full", b.muted ? "bg-ink-3" : "bg-accent")} /> : null}
        </span>
      </span>
    </a>
  );
});
