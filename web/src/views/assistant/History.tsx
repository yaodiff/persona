// "All chats" (docs/assistant/assistant.md "Managing its own conversations", the iOS app's sidebar + ArchivedConversationsView): every conversation
// Persona has, the main one first, the rest newest first by day; search on the title and the last message; a row's menu
// renames it, copies its id, archives it (the main one can't be). The archived ones are folded at the end. The background
// supervisor (#5082, lib/background-supervisor.ts) is with the scheduled tasks' chats, marked built in, and its menu only
// copies its id.
import { conversationChannel, useChannels } from "../../lib/channels";
import { ChannelMark, OriginMark } from "../../ui/ChannelMark";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, enc, errorText } from "../../lib/api";
import { ago, stateOf } from "../../lib/format";
import { plainMarkdown } from "../../lib/plain-markdown";
import { conversationPage } from "../../lib/conversation-page";
import { href, navigate, onLinkClick, type Route } from "../../lib/router";
import { onHubEvent, refreshAssistant, useStore } from "../../lib/store";
import { isResumed } from "../../lib/reconnect";
import type { AssistantInfo, Session } from "../../lib/types";
import { leaderItem } from "../ConversationMenu";
import { Button, Collapse, copyText, cx, Dialog, IconButton, Loading, Menu, StatePill, toast, useMenu, useNow, type MenuItem } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { arrange, conversationTitle, filterConversations, mergeConversations, splitTitle, type Conversation } from "./conversations";
import { Empty, inputClass, SectionTitle } from "./parts";
import { Chip } from "../inbox/InboxParts";
import { autoConversations, autoOrigin, matchesOrigin, originLabel, parseOriginFilter, type OriginFilter } from "../../lib/conversation-origins";
import { backgroundSupervisorTitle, isBackgroundSupervisor } from "../../lib/background-supervisor";
import { t } from "../../i18n";
import { ASSISTANT_SEARCH_FEATURE, markParts, mergeSearch, useAssistantSearch, type AssistantSearchHit } from "../../lib/assistant-search";
import { PHONE, useMedia } from "../../lib/viewport";

const NONE: Session[] = [];
// A row's line under its title: the matching words of a search, or else its last message.
const ROW_LINE = "mt-0.5 block truncate text-[12px] text-ink-3";
// The pill beside a row's title ("Main chat", "Built in"): one shape, each in its own colors.
const pill = (bg: string, ink: string) => `inline-flex h-5 shrink-0 items-center whitespace-nowrap rounded-full ${bg} px-2 text-[11px] font-medium ${ink}`;

export function History({ origin, onToggleSidebar }: { origin?: string; onToggleSidebar: () => void }) {
  const filter = parseOriginFilter(origin);
  const assistant = useStore((s) => s.assistant);
  const mid = assistant?.machine_id;
  const live = useStore((s) => (mid ? s.sessions[mid] ?? NONE : NONE));
  const [fetched, setFetched] = useState<Conversation[] | null>(null);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [showArchived, setShowArchived] = useState(false);
  const [renaming, setRenaming] = useState<Conversation | null>(null);
  /** Titles just given here: the sessions pushed by the hub may still carry the old one for a while. */
  const [titles, setTitles] = useState<Record<string, string>>({});
  /** Archived or brought back here: shown so until the hub pushes the session with that mark (the live sessions carry
   *  the old one until then, and the push may come after the answer), or the hub refused it. */
  const [marked, setMarked] = useState<Record<string, boolean>>({});
  useEffect(() => {
    setMarked((m) => {
      const settled = Object.keys(m).filter((id) => {
        const s = live.find((x) => x.session_id === id);
        return !s || (s.archived === true) === m[id];
      });
      if (!settled.length) return m;
      const rest = { ...m };
      settled.forEach((id) => delete rest[id]);
      return rest;
    });
  }, [live]);
  const nowMs = useNow(60_000);

  /** Reads the list; true when it went through. */
  const load = useCallback(async () => {
    try {
      const a = await api<AssistantInfo & { conversations: Conversation[] }>("/assistant?archived=1");
      setFetched(a.conversations ?? []);
      setError("");
      return true;
    } catch (e) {
      setError(errorText(e));
      return false;
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  // read again after a reconnect (#4954's rule, as the scheduled tasks are): always when the read failed while the hub
  // was away, else when the hub could not resume, as the pushes missed meanwhile are not replayed
  const failed = error !== "";
  useEffect(() => onHubEvent("hello", (msg) => (failed || !isResumed(msg)) && void load()), [load, failed]);
  const [retrying, setRetrying] = useState(false);
  const [retried, setRetried] = useState(0);
  const retry = () => {
    setRetrying(true);
    void load().then((ok) => {
      setRetrying(false);
      if (ok) setRetried((x) => x + 1);
    });
  };
  // Retry goes with the error once the read works: focus that fell to the page's body goes to the first chat instead
  const page = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!retried || (document.activeElement && document.activeElement !== document.body)) return;
    page.current?.querySelector<HTMLElement>("a[href]")?.focus();
  }, [retried]);

  const all = useMemo(
    () =>
      mergeConversations(fetched ?? [], assistant?.conversations ?? [], live.filter((s) => s.assistant)).map((s) => {
        const title = titles[s.session_id];
        const archived = marked[s.session_id];
        return title || archived !== undefined ? { ...s, ...(title ? { title } : {}), ...(archived !== undefined ? { archived } : {}) } : s;
      }),
    [fetched, assistant?.conversations, live, titles, marked],
  );
  const background = assistant?.background_supervisor;
  // what was said in them too (#6283): the manager's search, merged into the local matches (titles, last messages), as
  // the filter chips narrow them; a chat found by a message shows it and opens at it
  const searchOn = useStore((s) => s.features.includes(ASSISTANT_SEARCH_FEATURE));
  const said = useAssistantSearch(query, searchOn);
  // a found chat's line starts just before its match, as much of a lead-in as the row's width holds (#7045)
  const fit = useMedia(PHONE) ? "phone" : "wide";
  const { hits, saidBy } = useMemo(() => {
    const shown = all.filter((s) => matchesOrigin(s, filter, background));
    const merged = mergeSearch(filterConversations(shown, query), said.answer, all, (r): Conversation => ({
      machine_id: r.machine_id, session_id: r.session_id, title: r.title, cwd: "", status: "idle", assistant: true, archived: r.archived, updated_at: r.updated_at, origin: r.origin,
    })).filter(({ chat }) => matchesOrigin(chat, filter, background));
    const byId = new Map<string, AssistantSearchHit>();
    for (const r of said.answer?.results ?? []) if (r.hits[0] && !r.title_match) byId.set(r.session_id, r.hits[0]);
    return { hits: merged.map((m) => m.chat), saidBy: byId };
  }, [all, query, filter, background, said.answer]);
  // the sources there are (#3264): "All" · "Mine" · one per kind of automatic conversation, when there are any
  const sources = useMemo(() => autoConversations(all, assistant?.main, background).groups, [all, assistant?.main, background]);
  const { main, days, archived } = useMemo(() => arrange(hits, assistant?.main, new Date(nowMs)), [hits, assistant?.main, nowMs]);
  const total = all.filter((s) => !s.archived).length;
  const searching = query.trim() !== "";

  const after = async () => {
    await Promise.allSettled([refreshAssistant(), load()]);
  };

  const archive = async (s: Conversation, on: boolean) => {
    // shown at once (one the node no longer lists goes by what was read); what the hub lists next is what stays
    setFetched((list) => (list ?? []).map((x) => (x.session_id === s.session_id ? { ...x, archived: on } : x)));
    setMarked((m) => ({ ...m, [s.session_id]: on }));
    try {
      await api(`/assistant/conversations/${enc(s.session_id)}/${on ? "archive" : "unarchive"}`, { method: "POST", json: {} });
      toast(on ? t("assistant-history.archived_toast", { title: conversationTitle(s) }) : t("assistant-history.unarchived_toast", { title: conversationTitle(s) }), "ok");
    } catch (e) {
      toast(errorText(e), "error");
      // as it was, in what was read too: the reload below can fail as well (the hub is unreachable), and one the node
      // no longer lists goes by what was read
      setFetched((list) => list?.map((x) => (x.session_id === s.session_id ? { ...x, archived: s.archived } : x)) ?? list);
      setMarked(({ [s.session_id]: _, ...rest }) => rest);
    }
    await after();
  };

  const loading = fetched === null && !error;

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("assistant-history.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <h1 className="min-w-0 flex-1 truncate text-[14px] font-semibold text-ink">
          {t("assistant-history.title")}
          {total > 0 && <span className="ml-2 text-[12px] font-normal text-ink-3 tabular-nums">{total}</span>}
        </h1>
        <IconButton icon="retry" label={t("assistant-history.refresh")} onClick={() => void after()} />
        <Button tone="primary" icon="plus" onClick={() => navigate({ name: "persona-new" })}>{t("assistant-history.new_chat")}</Button>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div ref={page} className="mx-auto max-w-[760px] px-4 py-5 sm:px-8 sm:py-6">
          <label className="mb-5 flex h-9 items-center gap-2 rounded-control bg-surface px-3 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
            <Icon name="search" size={13} stroke={2} className="text-ink-3" />
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && setQuery("")}
              placeholder={searchOn ? t("assistant-history.search_label") : t("assistant-history.search_placeholder")}
              aria-label={t("assistant-history.search_label")}
              className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
            />
            {query && <IconButton icon="x" label={t("assistant-history.clear_search")} size={22} iconSize={12} onClick={() => setQuery("")} />}
          </label>

          {(sources.length > 0 || filter !== "all") && (
            <div role="group" aria-label={t("assistant-conversations.origin_filter")} className="-mt-2 mb-5 flex flex-wrap gap-1.5">
              {(["all", "mine", ...sources.map((g) => g.origin)] as OriginFilter[]).map((f) => (
                <Chip key={f} on={filter === f} onClick={() => navigate({ name: "history", origin: f === "all" ? undefined : f }, true)}>
                  {f === "all" ? t("assistant-conversations.origin_all") : f === "mine" ? t("assistant-conversations.origin_mine") : originLabel(f)}
                  {f !== "all" && f !== "mine" && <span className="ml-1 tabular-nums opacity-60">{sources.find((g) => g.origin === f)?.count}</span>}
                </Chip>
              ))}
            </div>
          )}

          {loading && (
            <div className="flex justify-center py-16">
              <Loading />
            </div>
          )}
          {/* The read failed: in place of the list when there is nothing else to show, else one line over the chats the
              page has from elsewhere (the sidebar's, the live sessions), which may leave out the archived ones. */}
          {error && fetched === null && all.length === 0 && (
            <Empty icon="info" title={t("assistant-history.load_failed")} action={<Button icon="retry" busy={retrying} onClick={retry}>{t("assistant-history-load.retry")}</Button>}>
              {error}
            </Empty>
          )}
          {error && fetched === null && all.length > 0 && (
            <div role="status" className="-mt-3 mb-4 flex items-center gap-2 px-1 text-label text-ink-3">
              <span className="min-w-0 flex-1" title={error}>{t("assistant-history-load.load_partial")}</span>
              <Button size="sm" tone="quiet" icon="retry" busy={retrying} onClick={retry} className="touch-hit">{t("assistant-history-load.retry")}</Button>
            </div>
          )}
          {!loading && !error && all.length === 0 && (
            <Empty icon="chat" title={t("assistant-history.empty_title")}>
              {t("assistant-history.empty_body")}
            </Empty>
          )}
          {searching && said.answer?.partial && <p className="-mt-3 mb-4 px-1 text-[12px] text-ink-3" role="status">{t("assistant-history.search_partial")}</p>}
          {!loading && searching && hits.length === 0 && all.length > 0 && <Empty icon="search" title={t("assistant-history.no_results", { query: query.trim() })} />}

          {main && (
            <section className="mb-5">
              <div className="overflow-hidden rounded-card bg-surface shadow-card">
                <ConvRow s={main} main now={nowMs} said={searching ? saidBy.get(main.session_id) : undefined} fit={fit} onRename={setRenaming} onArchive={archive} />
              </div>
            </section>
          )}

          {days.map((d) => (
            <section key={d.title} className="mb-5">
              <SectionTitle>{d.title}</SectionTitle>
              <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
                {d.items.map((s) => (
                  <ConvRow key={s.session_id} s={s} now={nowMs} said={searching ? saidBy.get(s.session_id) : undefined} fit={fit} onRename={setRenaming} onArchive={archive} />
                ))}
              </div>
            </section>
          ))}

          {archived.length > 0 && (
            <section className="mb-5">
              <button
                type="button"
                aria-expanded={showArchived || searching}
                onClick={() => setShowArchived((v) => !v)}
                className="mb-1.5 flex items-center gap-1.5 whitespace-nowrap rounded-[6px] px-1 text-[12px] font-medium text-ink-3 transition-colors hover:text-ink-2"
              >
                <Icon name="archive" size={13} />
                {t("assistant-history.archived_count", { count: archived.length })}
                <Icon name="chevron" size={12} stroke={2.2} className={cx("transition-transform duration-200", !(showArchived || searching) && "-rotate-90")} />
              </button>
              <Collapse open={showArchived || searching}>
                {/* folded: out of the tab order and the accessibility tree */}
                <div inert={!(showArchived || searching)}>
                <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
                  {archived.map((s) => (
                    <ConvRow key={s.session_id} s={s} now={nowMs} said={searching ? saidBy.get(s.session_id) : undefined} fit={fit} onRename={setRenaming} onArchive={archive} />
                  ))}
                </div>
                <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("assistant-history.archived_footer")}</p>
                </div>
              </Collapse>
            </section>
          )}
        </div>
      </div>

      {renaming && (
        <RenameDialog
          s={renaming}
          onClose={() => setRenaming(null)}
          onDone={(title) => {
            setTitles((t) => ({ ...t, [renaming.session_id]: title }));
            void after();
          }}
        />
      )}
    </div>
  );
}

const MARK_TONES = ["bg-accent-tint text-accent-ink", "bg-orange-tint text-orange", "bg-green-tint text-green"];

/** A soft color picked by the id, so neighbours differ (the iOS app's ConversationMark). */
function markTone(id: string): string {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) & 0xffff;
  return MARK_TONES[h % MARK_TONES.length];
}

/** The latest channel a conversation's messages came from, as a small mark beside its title. */
function ConvChannel({ ids }: { ids?: string[] }) {
  const list = useChannels();
  const via = conversationChannel(ids, list);
  if (!via) return null;
  const kind = list.find((c) => c.id === via.id)?.kind ?? "im";
  return (
    <span title={t("assistant-history.via", { name: via.name })} className="flex shrink-0">
      <ChannelMark c={{ id: via.id, kind }} size={16} />
    </span>
  );
}

function ConvRow({ s, main, now, said, fit, onRename, onArchive }: { s: Conversation; main?: boolean; now: number; said?: AssistantSearchHit; fit?: "phone" | "wide"; onRename: (s: Conversation) => void; onArchive: (s: Conversation, on: boolean) => void }) {
  const menu = useMenu();
  // found by what was said in it: opens at that message (#6283)
  const route: Route = conversationPage(s.session_id, s.machine_id, s.manager, said?.item_id);
  // the background supervisor is built in (#5082): its own name and mark, and nothing to rename, archive or make 常驻
  const builtIn = useStore((x) => isBackgroundSupervisor(x.assistant, s.session_id));
  const title = builtIn ? backgroundSupervisorTitle(s) : conversationTitle(s, main);
  const split = builtIn ? null : splitTitle(title);
  const st = stateOf(s);
  const flagged = st === "needs_you" || st === "working" || st === "unread";
  const last = plainMarkdown(s.last_text);
  // opened by the hub itself (#3758, #5195): what opened it, in the channel's place, as on its sidebar row; decided as the
  // filters decide it, the background supervisor with the scheduled tasks' whatever its row says (#5082)
  const origin = autoOrigin(s, builtIn ? s.session_id : undefined);
  // the chats outside the sidebar live here: 设为常驻 puts one in its 常驻 group (#4331)
  const leader = useStore((x) => !!x.assistant?.leaders?.includes(s.session_id));
  const own = !main && !builtIn;
  const items: MenuItem[] = [
    ...(own && !s.archived ? [leaderItem(s, leader)] : []),
    ...(own ? [{ key: "rename", label: t("assistant-history.rename_ellipsis"), icon: "pencil" as const, onSelect: () => onRename(s) }] : []),
    { key: "copy", label: t("assistant-history.copy_session_id"), icon: "copy", onSelect: () => void copyText(s.session_id) },
    ...(own
      ? [s.archived
          ? { key: "unarchive", label: t("assistant-history.unarchive"), icon: "archive" as const, onSelect: () => onArchive(s, false) }
          : { key: "archive", label: t("assistant-history.archive"), icon: "archive" as const, onSelect: () => onArchive(s, true) }]
      : []),
  ];
  return (
    <div className={cx("group relative flex items-center gap-1 pr-2 transition-colors hover:bg-hover", menu.anchor && "bg-hover")}>
      <a
        href={href(route)}
        onClick={(e) => onLinkClick(e, route)}
        title={[title, last].filter(Boolean).join("\n")}
        className="flex min-w-0 flex-1 items-center gap-3 py-2.5 pl-4 focus-visible:outline-none"
      >
        <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-full", main ? "bg-ink text-surface" : markTone(s.session_id))}>
          <Icon name={main ? "spark" : builtIn ? "target" : "chat"} size={main ? 13 : 14} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-center gap-2">
            <span className={cx("truncate text-[13.5px]", st === "unread" ? "font-semibold text-ink" : "font-medium text-ink")}>
              {split ? (
                <>
                  <span className="font-normal text-ink-3">{split.head}</span>
                  {split.topic}
                </>
              ) : (
                title
              )}
            </span>
            {main && <span className={pill("bg-accent-tint", "text-accent-ink")}>{t("assistant-history.main_chat")}</span>}
            {builtIn && <span data-testid="built-in" className={pill("bg-field", "text-ink-2")}>{t("assistant-conversations.built_in")}</span>}
            {origin ? <OriginMark origin={origin} size={16} /> : <ConvChannel ids={s.channels} />}
          </span>
          {said ? (
            <span className={ROW_LINE} data-said>
              {markParts(said.snippet, said.ranges, fit).map(([words, match], k) => (match ? <mark key={k} className="rounded-[3px] bg-accent-tint text-ink">{words}</mark> : <span key={k}>{words}</span>))}
            </span>
          ) : (
            last && <span className={ROW_LINE}>{last}</span>
          )}
        </span>
        <span className="flex shrink-0 flex-col items-end gap-1">
          <span className="text-[11px] text-ink-3 tabular-nums">{ago(s.updated_at, now)}</span>
          {flagged && <StatePill state={st} />}
        </span>
      </a>
      <IconButton
        icon="more"
        label={t("assistant-history.more")}
        onClick={menu.open}
        active={!!menu.anchor}
        className={menu.anchor ? "" : "sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100"}
      />
      {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} items={items} align="right" width={180} />}
    </div>
  );
}

function RenameDialog({ s, onClose, onDone }: { s: Conversation; onClose: () => void; onDone: (title: string) => void }) {
  // starts from the title as the list shows it (a chat titled by its context tag: the tag's title, not the tag)
  const [title, setTitle] = useState(() => { const shown = conversationTitle(s); return shown === t("assistant-conversations.untitled") ? "" : shown; });
  const [busy, setBusy] = useState(false);
  const trimmed = title.trim();
  const save = async () => {
    if (!trimmed || busy) return;
    setBusy(true);
    try {
      await api(`/sessions/${enc(s.machine_id)}/${enc(s.session_id)}/rename`, { method: "POST", json: { title: trimmed } });
      toast(t("assistant-history.renamed"), "ok");
      onDone(trimmed);
      onClose();
    } catch (e) {
      toast(errorText(e), "error");
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={t("assistant-history.rename_title")}
      width={420}
      footer={
        <>
          <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="primary" busy={busy} disabled={!trimmed || trimmed === s.title} onClick={() => void save()}>{t("common.save")}</Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <input className={inputClass} value={title} autoFocus onFocus={(e) => e.currentTarget.select()} onChange={(e) => setTitle(e.target.value)} placeholder={t("assistant-history.title_placeholder")} aria-label={t("assistant-history.title_placeholder")} />
        <p className="mt-1.5 text-[11.5px] leading-relaxed text-ink-3">{t("assistant-history.rename_hint")}</p>
      </form>
    </Dialog>
  );
}
