// Personal agents: one Lark gateway personal agent session — its messages (older pages on demand), a send box with an
// idempotent clientId, marked read on open; re-pulled when the hub broadcasts {"type":"agents"}.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { AgentAvatar, type AvatarSession } from "./AgentAvatar";
import {
  blockView,
  fetchMessages,
  personalAgentDraftKey,
  fetchAgentOutbox,
  arrangeSessions,
  togglePin,
  updateAgentPrefs,
  useAgentPrefs,
  markRead,
  mergeMessages,
  onAgentsChanged,
  pendingOutgoing,
  providerLabel,
  sendMessage,
  setLocalRead,
  useAgentList,
  type AgentMessage,
  type Outgoing,
} from "../../lib/agents";
import {
  applyPersonalOutbox,
  discardPersonalOutgoing,
  personalOutgoingStore,
  personalOutgoingStoreCurrent,
  reconcilePersonalOutgoing,
  restorePersonalOutgoing,
  savePersonalOutgoing,
} from "../../lib/agent-outgoing";
import { ApiError, errorText } from "../../lib/api";
import { ago, clock, uuid } from "../../lib/format";
import { href, navigate, onLinkClick } from "../../lib/router";
import { Button, Collapse, cx, EASE, IconButton, Loading, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { Empty } from "../session/files/parts";
import { SearchField } from "../../ui/SearchField";
import { formatDate, t } from "../../i18n";
import { composing } from "../../lib/send-history";
import { storedPageDraft, storePageDraft } from "../../lib/drafts";
import { fetchHiddenSessions, personalHideDeps, personalOrder, personalSessions, restoreAgentSession, type AgentSession } from "../../lib/agents";
import { SESSIONS_LIST_FEATURE } from "../../lib/session-list";
import { useStore } from "../../lib/store";
import { PagedSessionList } from "./PagedSessionList";
import { useAccountGroups } from "./account-groups";
import { SESSION_LIST_METRICS, SESSION_LIST_STYLE } from "../../ui/list-metrics";

export function AgentsView({ id, onToggleSidebar }: { id?: string; onToggleSidebar: () => void }) {
  // one mount per conversation: its outgoing list and storage scope belong to that id, never the previous one (#12555)
  return id ? <AgentSessionView key={id} id={id} onToggleSidebar={onToggleSidebar} /> : <AgentList onToggleSidebar={onToggleSidebar} />;
}

/** One personal Agent conversation without the page's own title row, for a page that draws its own over it (the
 *  Messages page's right-hand side, modules/lark/Messages.tsx): the same reading, sending, outbox reconciling, drafts,
 *  marking read and live refresh as `/agents/<id>`. */
export function AgentConversation({ id }: { id: string }) {
  return <AgentSessionView key={id} id={id} embedded onToggleSidebar={() => {}} />;
}

function Header({ title, sub, avatar, onToggleSidebar }: { title: string; sub?: string; avatar?: AvatarSession; onToggleSidebar: () => void }) {
  return (
    <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
      <IconButton icon="sidebar" label={t("agents-agentsview.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
      {avatar && <AgentAvatar s={avatar} size={28} />}
      <div className="min-w-0 flex-1">
        <h1 className="truncate text-[14px] font-semibold text-ink">{title}</h1>
        {sub && <p className="truncate text-[11.5px] text-ink-3">{sub}</p>}
      </div>
    </header>
  );
}

const personalIdentity = (s: AgentSession) => s.id;
function AgentList({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  const bounded = useStore((s) => s.features.includes(SESSIONS_LIST_FEATURE));
  return bounded ? <BoundedAgentList onToggleSidebar={onToggleSidebar} /> : <LegacyAgentList onToggleSidebar={onToggleSidebar} />;
}

function BoundedAgentList({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  const prefs = useAgentPrefs();
  const { sessions, loaded, error, hints } = useAgentList();
  const accounts = useAccountGroups();
  return <div className="flex h-full flex-col">
    <Header title={t("agents-agentsview.title")} sub={t("agents-agentsview.subtitle")} onToggleSidebar={onToggleSidebar} />
    <div className="min-h-0 flex-1 overflow-y-auto"><div className={SESSION_LIST_STYLE.personalPage}>
      {loaded && !error && sessions.length > 0 && <SourceNotice hints={hints} />}
      <PagedSessionList root={personalSessions} path="/agents/sessions" identity={personalIdentity} compare={personalOrder} event="agents" personal label={accounts.label} accountMark={accounts.accountMark} providerOptions={accounts.providerOptions} renderRow={(s) => <div className={SESSION_LIST_STYLE.personalRow}>
        <a href={href({ name: "agents", id: s.id })} onClick={(e) => onLinkClick(e, { name: "agents", id: s.id })} className="flex min-w-0 flex-1 items-center gap-2">
          <AgentAvatar s={s} size={SESSION_LIST_METRICS.avatar} /><span className={SESSION_LIST_STYLE.personalTitle}>{s.title || t("lib-format.untitled_session")}</span>
          {s.unreadCount > 0 && <span className={SESSION_LIST_STYLE.personalBadge}>{s.unreadCount}</span>}
        </a>
        <button type="button" aria-label={t(prefs.pins.includes(s.id) ? "agents-agentsview.unpin" : "agents-agentsview.pin")} onClick={() => updateAgentPrefs((x) => togglePin(x, s.id))} className={SESSION_LIST_STYLE.pinButton}><Icon name="pin" size={SESSION_LIST_METRICS.icon} /></button>
      </div>} />
      {loaded && !error && !sessions.length && hints.map((h) => <p key={h} className="mb-1 px-2 text-[13px] text-ink-3">{h}</p>)}
      <RemovedAgents />
    </div></div>
  </div>;
}

/** Sources that list nothing (failing, or not set up) while others list Agents: one quiet notice above those Agents, so
 *  the missing ones don't go unnoticed. An empty list shows the same hints on their own. */
export function SourceNotice({ hints }: { hints: string[] }) {
  if (!hints.length) return null;
  return (
    <div role="status" className="mb-3 flex items-start gap-2 px-2 py-1.5">
      <span className="mt-[3px] flex size-3.5 shrink-0 items-center justify-center text-orange"><Icon name="warning" size={13} stroke={2.2} /></span>
      <div className="min-w-0 flex-1">
        <p className="text-[13px] leading-[1.45] text-ink-2">{t("agents-agentsview.sources_unavailable")}</p>
        {hints.map((h) => <p key={h} className="text-[12px] leading-[1.45] text-ink-3">{h}</p>)}
      </div>
    </div>
  );
}

/** "Removed (N)" at the end of the page, folded (as All chats keeps its archived ones): the conversations removed from
 *  the list, each with Restore. Re-read when the list changes (a hide, a restore, a new message bringing one back). */
export function RemovedAgents() {
  const [list, setList] = useState<AgentSession[]>([]);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    let live = true;
    const read = () => void fetchHiddenSessions().then((x) => live && setList(x)).catch(() => {});
    read();
    const off = onAgentsChanged(() => read());
    return () => {
      live = false;
      off();
    };
  }, []);
  if (!list.length) return null;
  return (
    <section className="mt-2 mb-4">
      <button type="button" aria-expanded={open} onClick={() => setOpen((v) => !v)} className="mb-1.5 flex items-center gap-1.5 whitespace-nowrap rounded-[6px] px-1 text-[12px] font-medium text-ink-3 transition-colors hover:text-ink-2">
        <Icon name="archive" size={13} />
        {t("lib-agents.removed_count", { count: list.length })}
        <Icon name="chevron" size={12} stroke={2.2} className={cx("transition-transform duration-200", !open && "-rotate-90")} />
      </button>
      <Collapse open={open}>
        <div inert={!open}>
          <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
            {list.map((s) => (
              <div key={s.id} className={SESSION_LIST_STYLE.personalRow}>
                <AgentAvatar s={s} size={SESSION_LIST_METRICS.avatar} />
                <span className="min-w-0 flex-1 truncate text-[13px] text-ink-2">{s.title || t("lib-format.untitled_session")}</span>
                <Button tone="quiet" onClick={() => void restoreAgentSession(s, personalHideDeps).then((ok) => ok && setList((x) => x.filter((y) => y.id !== s.id)))}>{t("agents-agentsview.restore")}</Button>
              </div>
            ))}
          </div>
          <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("lib-agents.removed_footer")}</p>
        </div>
      </Collapse>
    </section>
  );
}

function LegacyAgentList({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  const { sessions, loaded, error, hints } = useAgentList();
  const [q, setQ] = useState("");
  const prefs = useAgentPrefs();
  const groups = arrangeSessions(sessions, prefs, q);
  return (
    <div className="flex h-full flex-col">
      <Header title={t("agents-agentsview.title")} sub={t("agents-agentsview.subtitle")} onToggleSidebar={onToggleSidebar} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className={SESSION_LIST_STYLE.personalPage}>
          <SearchField tone="inset" size="md" icon={false} className="mb-4" value={q} onChange={setQ} placeholder={t("agents-agentsview.search_placeholder")} />
          {!loaded && <Loading />}
          {error && <p className="text-[13px] text-red">{error}</p>}
          {loaded && !error && !groups.length && (q || !hints.length) && <p className="text-[13px] text-ink-3">{q ? t("agents-agentsview.no_match") : t("agents-agentsview.empty")}</p>}
          {loaded && !error && !sessions.length && !q && hints.map((h) => <p key={h} className="mb-1 text-[13px] text-ink-3">{h}</p>)}
          {loaded && !error && sessions.length > 0 && <SourceNotice hints={hints} />}
          {groups.map((g, i) => (
            <section key={g.key} className="mb-4 overflow-hidden rounded-card bg-surface shadow-card" style={{ animation: `fade-up 420ms ${EASE} ${i * 60}ms both` }}>
              <h2 className="flex items-center gap-2 px-4 pt-3 pb-1 text-[12px] font-medium text-ink-3">
                <span className="flex-1">{g.label || t("agents-agentsview.group_all")}</span>
                <span className="tabular-nums">{g.sessions.length}</span>
              </h2>
              {g.sessions.map((s) => (
                <a key={s.id} href={href({ name: "agents", id: s.id })} onClick={(e) => onLinkClick(e, { name: "agents", id: s.id })} className={SESSION_LIST_STYLE.personalRow}>
                  <AgentAvatar s={s} size={SESSION_LIST_METRICS.avatar} />
                  <span className={cx("min-w-0 flex-1 truncate text-[13px]", s.unreadCount ? "font-medium text-ink" : "text-ink-2")}>{s.title || t("lib-format.untitled_session")}</span>
                  {s.unreadCount > 0 && <span className={SESSION_LIST_STYLE.personalBadge}>{s.unreadCount}</span>}
                  <span className="shrink-0 text-[11px] text-ink-3">{s.rankTimeMs ? ago(new Date(s.rankTimeMs).toISOString()) : ""}</span>
                  <button
                    type="button"
                    title={prefs.pins.includes(s.id) ? t("agents-agentsview.unpin") : t("agents-agentsview.pin")}
                    aria-label={prefs.pins.includes(s.id) ? t("agents-agentsview.unpin") : t("agents-agentsview.pin")}
                    onClick={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                      updateAgentPrefs((x) => togglePin(x, s.id));
                    }}
                    className={cx("flex size-6 shrink-0 items-center justify-center rounded-[6px] hover:bg-hover", prefs.pins.includes(s.id) ? "text-accent" : "text-ink-3")}
                  >
                    <Icon name="pin" size={SESSION_LIST_METRICS.icon} />
                  </button>
                </a>
              ))}
            </section>
          ))}
          <RemovedAgents />
        </div>
      </div>
    </div>
  );
}

function AgentSessionView({ id, onToggleSidebar, embedded = false }: { id: string; onToggleSidebar: () => void; embedded?: boolean }) {
  const { sessions } = useAgentList();
  const fromList = sessions.find((s) => s.id === id);
  const [pageSession, setPageSession] = useState(fromList);
  const session = fromList ?? pageSession;
  const [messages, setMessages] = useState<AgentMessage[] | null>(null);
  const [cursor, setCursor] = useState<string | undefined>();
  const [hasMore, setHasMore] = useState(false);
  const [older, setOlder] = useState(false);
  const [error, setError] = useState("");
  // the hub has no such conversation (an old or mistyped link, or one removed since): said plainly, with the way back
  const [missing, setMissing] = useState(false);
  const [outgoingStore] = useState(() => personalOutgoingStore(id));
  const [out, setOutState] = useState<Outgoing[]>(() => restorePersonalOutgoing(outgoingStore));
  const outRef = useRef(out);
  // Persist synchronously, before a composer clears or a late response can be overtaken by Discard. React only renders
  // the committed list; the ref is the serial send/reconcile/discard order for this mounted conversation.
  const commitOut = useCallback((next: Outgoing[], persist = true): Outgoing[] => {
    if (next === outRef.current) return next;
    const saved = persist ? savePersonalOutgoing(outgoingStore, next) : next;
    outRef.current = saved;
    setOutState(saved);
    return saved;
  }, [outgoingStore]);
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const keepFromBottom = useRef<number | null>(null);
  // the newest message id at the last /read: a read's own change event reloads with the same tail and must not read again
  const readTail = useRef<string | null>(null);

  const loadLatest = useCallback(
    async (first: boolean) => {
      try {
        const [p, metadata] = await Promise.all([
          fetchMessages(id),
          // An older hub or a failed metadata read must not take the transcript down with it. Null changes no status.
          fetchAgentOutbox(id).then((r) => r.outbox).catch(() => null),
        ]);
        if (!personalOutgoingStoreCurrent(outgoingStore)) return;
        if (p.session) setPageSession(p.session);
        setMessages((cur) => (cur && !first ? mergeMessages(cur, p.messages) : p.messages));
        commitOut(reconcilePersonalOutgoing(outRef.current, p.messages, metadata));
        if (first) {
          setCursor(p.nextCursor);
          setHasMore(p.hasMore);
        }
        setError("");
        setMissing(false);
        const tail = p.messages.at(-1)?.externalId ?? "";
        if (first || tail !== readTail.current) {
          readTail.current = tail;
          void markRead(id).then(() => setLocalRead(id)).catch(() => {});
        }
      } catch (e) {
        if (!personalOutgoingStoreCurrent(outgoingStore)) return;
        if (e instanceof ApiError && e.status === 404) setMissing(true);
        setError(errorText(e));
      }
    },
    [id, outgoingStore, commitOut],
  );

  useEffect(() => {
    void loadLatest(true);
    return onAgentsChanged((ids) => {
      if (!ids.length || ids.includes(id)) void loadLatest(false);
    });
  }, [id, loadLatest]);

  const loadOlder = async () => {
    if (!cursor || older) return;
    setOlder(true);
    try {
      const p = await fetchMessages(id, cursor);
      const el = scroller.current;
      keepFromBottom.current = el ? el.scrollHeight - el.scrollTop : null;
      setMessages((cur) => mergeMessages(p.messages, cur ?? []));
      setCursor(p.nextCursor);
      setHasMore(p.hasMore && Boolean(p.nextCursor));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setOlder(false);
    }
  };

  const shown = pendingOutgoing(out, messages ?? []);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (keepFromBottom.current !== null) {
      el.scrollTop = el.scrollHeight - keepFromBottom.current;
      keepFromBottom.current = null;
    } else if (stick.current) el.scrollTop = el.scrollHeight;
  }, [messages, shown.length]);

  const deliver = async (o: Outgoing) => {
    const started: Outgoing = { ...o, status: "sending", error: undefined, updatedAt: Date.now() };
    const staged = commitOut([...outRef.current.filter((x) => x.clientId !== o.clientId), started]);
    // Another mounted view may have discarded this id. Its tombstone wins; only a newly composed message gets a new id.
    if (!staged.some((x) => x.clientId === o.clientId)) return;
    stick.current = true;
    try {
      const r = await sendMessage(id, o.clientId, o.text);
      if (!personalOutgoingStoreCurrent(outgoingStore)) return;
      const failed = r.outbox?.status === "failed";
      commitOut(outRef.current.flatMap((x) => {
        if (x.clientId !== o.clientId) return [x];
        if (!r.outbox) return [{ ...x, status: "sent" as const, error: undefined, updatedAt: Date.now() }];
        const receipt = failed ? { ...r.outbox, error: r.outbox.error || t("agents-agentsview.send_failed") } : r.outbox;
        const next = applyPersonalOutbox(x, receipt);
        return next ? [next] : [];
      }));
      void loadLatest(false);
    } catch (e) {
      if (!personalOutgoingStoreCurrent(outgoingStore)) return;
      commitOut(outRef.current.map((x) => (x.clientId === o.clientId ? { ...x, status: "failed", error: errorText(e), updatedAt: Date.now() } : x)));
    }
  };

  const canSend = session?.canSend ?? false;
  if (missing && !session) return <MissingAgent onToggleSidebar={onToggleSidebar} embedded={embedded} />;
  return (
    <div className="flex h-full min-h-0 flex-col">
      {!embedded && <Header title={session?.title || t("lib-format.untitled_session")} sub={session ? providerLabel(session.provider) : undefined} avatar={session ?? undefined} onToggleSidebar={onToggleSidebar} />}
      <div
        ref={scroller}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
        className="min-h-0 flex-1 overflow-y-auto"
      >
        <div className="mx-auto flex max-w-[760px] flex-col gap-3 px-5 py-6 sm:px-8">
          {hasMore && (
            <div className="flex justify-center">
              <Button tone="quiet" busy={older} onClick={() => void loadOlder()}>{t("agents-agentsview.load_older")}</Button>
            </div>
          )}
          {!messages && !error && <Loading />}
          {error && <p className="text-[13px] text-red">{error}</p>}
          {messages && !messages.length && !shown.length && <p className="text-center text-[13px] text-ink-3">{t("agents-agentsview.no_messages")}</p>}
          {messages?.map((m) => <MessageRow key={m.externalId} m={m} />)}
          {shown.map((o) => (
            <div key={o.clientId} className="flex flex-col items-end gap-1">
              <div className={cx("max-w-[85%] rounded-[14px] bg-accent-tint px-3 py-2 text-[13.5px] break-words whitespace-pre-wrap text-ink", o.status === "failed" && "ring-1 ring-red")}>{o.text}</div>
              <div className="flex items-center gap-2 text-[11px] text-ink-3">
                {o.status === "sending" && <><Spinner size={10} /> {t("agents-agentsview.sending")}</>}
                {o.status === "sent" && t("agents-agentsview.sent")}
                {o.status === "failed" && (
                  <>
                    <span className="text-red">{o.error || t("agents-agentsview.send_failed")}</span>
                    <button type="button" onClick={() => void deliver(o)} className="flex items-center gap-1 text-accent hover:underline">
                      <Icon name="retry" size={11} /> {t("agents-agentsview.retry")}
                    </button>
                    <button type="button" onClick={() => commitOut(discardPersonalOutgoing(outgoingStore, outRef.current, o.clientId), false)} className="hover:text-ink-2">{t("agents-agentsview.discard")}</button>
                  </>
                )}
              </div>
            </div>
          ))}
        </div>
      </div>
      <AgentComposer id={id} disabled={!canSend} reason={session ? t("agents-agentsview.cannot_send") : t("agents-agentsview.loading")} onSend={(text) => { const at = Date.now(); void deliver({ clientId: uuid(), text, at, updatedAt: at, status: "sending" }); }} />
    </div>
  );
}

/** A personal agent's link to a conversation the hub doesn't have: what happened in plain words and the way back to the
 *  list, as a computer's page that isn't there does (machines/MachineDetail.tsx), with no box that waits forever. */
function MissingAgent({ onToggleSidebar, embedded }: { onToggleSidebar: () => void; embedded: boolean }) {
  return (
    <div className="flex h-full flex-col">
      {/* as a space that is gone (SpacePage): the header names where this is (the list's name), the shared empty state
          says what is missing once, with the way back, and there is no composer; embedded, the page's own header is
          the way back */}
      {!embedded && <Header title={t("agents-agentsview.title")} onToggleSidebar={onToggleSidebar} />}
      <div className="flex min-h-0 flex-1 flex-col pb-[10vh]" role="status">
        <Empty
          icon="chat"
          title={t("not-found.agent_title")}
          detail={t("not-found.agent_detail")}
          action={embedded ? undefined : <Button tone="secondary" icon="arrowLeft" onClick={() => navigate({ name: "agents" })}>{t("not-found.agent_back")}</Button>}
        />
      </div>
    </div>
  );
}

function MessageRow({ m }: { m: AgentMessage }) {
  const mine = m.role === "user";
  return (
    <div className={cx("flex flex-col gap-1", mine ? "items-end" : "items-start")}>
      {m.blocks.map((b, i) => {
        const v = blockView(b);
        if (v.kind === "text")
          return mine ? (
            <div key={i} className="max-w-[85%] rounded-[14px] bg-accent-tint px-3 py-2 text-[13.5px] break-words whitespace-pre-wrap text-ink">{v.text}</div>
          ) : (
            <div key={i} className="max-w-full min-w-0 text-[13.5px] text-ink"><Markdown text={v.text} /></div>
          );
        if (v.kind === "reaction")
          return (
            <span key={i} className="inline-flex items-center gap-1 rounded-full bg-inset px-2 py-0.5 text-[12px] text-ink-2 shadow-hairline" title={t("agents-agentsview.reaction_title")}>
              <span className="text-[15px] leading-none">{v.emoji}</span> {t("agents-agentsview.reacted")}
            </span>
          );
        if (v.kind === "tool")
          return (
            <details key={i} className="max-w-full rounded-control bg-inset px-2.5 py-1 text-[12px] text-ink-2 shadow-hairline">
              <summary className="flex cursor-pointer items-center gap-1.5 select-none"><Icon name="tool" size={11} /> {v.name}</summary>
              {v.detail ? <pre className="mt-1 max-h-60 overflow-auto text-[11px] whitespace-pre-wrap text-ink-3">{v.detail}</pre> : <p className="mt-1 text-[11px] text-ink-3">{t("agents-agentsview.no_detail")}</p>}
            </details>
          );
        return (
          <pre key={i} className="max-w-full overflow-auto rounded-control bg-inset px-2.5 py-1.5 text-[11px] whitespace-pre-wrap text-ink-3 shadow-hairline">{v.json}</pre>
        );
      })}
      <span className="text-[10.5px] text-ink-3" title={formatDate(m.createTimeMs, { dateStyle: "medium", timeStyle: "short" })}>
        {m.role !== "user" && m.role !== "assistant" ? `${m.role} · ` : ""}
        {clock(new Date(m.createTimeMs).toISOString())}
      </span>
    </div>
  );
}

export function AgentComposer({ id, disabled, reason, onSend }: { id: string; disabled: boolean; reason: string; onSend: (text: string) => void }) {
  const draftKey = personalAgentDraftKey(id);
  const [text, setText] = useState(() => storedPageDraft(draftKey));
  useEffect(() => {
    storePageDraft(draftKey, text);
  }, [draftKey, text]);
  const submit = () => {
    const body = text.trim();
    if (!body || disabled) return;
    onSend(body);
    setText("");
    // even if the send navigates away before the effect above runs, no words stay stored
    storePageDraft(draftKey, "");
  };
  return (
    <div className="shrink-0 border-t border-line px-3 py-3 sm:px-4">
      <div className="mx-auto flex max-w-[760px] items-end gap-2 rounded-card bg-surface p-2 shadow-card">
        <textarea
          value={text}
          disabled={disabled}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !composing(e.nativeEvent)) {
              e.preventDefault();
              submit();
            }
          }}
          rows={Math.min(8, Math.max(1, text.split("\n").length))}
          placeholder={disabled ? reason : t("agents-agentsview.composer_placeholder")}
          className="min-h-8 flex-1 resize-none bg-transparent px-1.5 py-1 text-[13.5px] text-ink outline-none placeholder:text-ink-3 disabled:cursor-not-allowed"
        />
        <IconButton icon="up" label={t("agents-agentsview.send")} disabled={disabled || !text.trim()} onClick={submit} />
      </div>
    </div>
  );
}
