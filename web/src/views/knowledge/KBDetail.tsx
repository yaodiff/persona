// One knowledge entry (/kb/<kind>/<id>): its tile, name, badges, one line and tags, "Ask Persona"; then the places a
// search found, the Agents on a project, a chat's Inbox items ("In Inbox", #15356), the timeline, a project's lessons from finished tasks, the profile, Moments,
// related entries by kind, code directories, a chat's last 7 days (a row an Inbox item holds opens it) and the folder (docs/apple/ios.md "Information
// architecture", knowledge base). "Tag" in the header.
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { api, ApiError, credentialOwner, enc, errorText, getToken, isAbort, subscribeCredentialOwner } from "../../lib/api";
import { MANAGER_HEADER, shownManager, useChosenManager, useKnownDefault } from "../../lib/manager-choice";
import { ConfirmedFacts } from "./ConfirmedFacts";
// A meeting's and an all-day recording's own sections come from the recording module through the `kb.detail` slot.
import { ModuleKBDetail, useKBKindShown } from "../../core/ModuleKBDetail";
import type { ModuleKBDetailParts } from "../../core/module-host";
import { Relationships } from "./Relationships";
import { RecordingReadOnlyDetail } from "./RecordingReadOnlyDetail";
import { TopicPoints } from "./TopicPoints";
import { ProjectLessons, projectLessonsDeps } from "./ProjectLessons";
import { ago, sessionPreview, sessionTitle, shortPath, stateOf, tabLabel } from "../../lib/format";
import { plainMarkdown } from "../../lib/plain-markdown";
import {
  agentPreset,
  askPersonaText,
  dayTitle,
  dirsOf,
  entryFromDetail,
  factsOf,
  formatAt,
  hitView,
  inboxOfRows,
  isKind,
  kindTitle,
  messageRowKey,
  parseMessageLine,
  parseRecent,
  profile,
  projectInfo,
  shortWhen,
  statusLabel,
  timeline,
  type KBDetailResponse,
  type KBEntry,
  type KBMatch,
  type KBMomentRef,
} from "../../lib/kb";
import { machineWithHub } from "../../lib/mesh";
import { openPanel } from "../../lib/panel";
import { href, onLinkClick } from "../../lib/router";
import { openedFromHere } from "../../lib/handoff";
import { getState, onHubEvent, useStore } from "../../lib/store";
import type { Session } from "../../lib/types";
import { Button, Collapse, copyText, cx, IconButton, Loading, StatePill, toast } from "../../ui/atoms";
import { StatusMark } from "../../ui/StatusMark";
import { Icon } from "../../ui/icons";
import { BrandMark } from "../../ui/BrandMark";
import { connectorBrand } from "../../lib/connectors";
import { Markdown } from "../../ui/Markdown";
import { known, remember } from "./cache";
import { askPersona } from "../../lib/outbox";
import { openNewAgent } from "../../lib/new-agent";
import { DayDialog, MessageView, MomentDialog, TagEditor } from "./KBDialogs";
import { ErrorBox } from "./KBList";
import { Empty } from "../session/files/parts";
import { Badges, Card, importedMeetingOf, importedNotesLabel, KBIcon, Marked, MoreRow, onPlainClick, SectionTitle, Tile, TonePill } from "./KBParts";
import { ChatInbox, InboxMessageLink } from "./KBInbox";
import { t } from "../../i18n";

/** Timeline lines shown before "Show all". */
const LOG_SHOWN = 6;
/** Related entries of one kind (and Moments posts) shown before "All". */
const LINKS_SHOWN = 4;
/** Search matches shown before "Show all". */
const MATCHES_SHOWN = 5;

/** The side beside the list with no entry open: an empty bar of the list's height (#16462), so the line under the list's
 *  bar runs across the page as on Inbox and To-dos, and what to do here under it. Only shown beside the list. */
export function EmptyDetail() {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div data-testid="kb-empty-detail-bar" aria-hidden className="h-14 shrink-0 border-b border-line" />
      <div className="flex flex-1 flex-col items-center justify-center px-8 pb-[8vh] text-center" style={{ animation: `fade-in 300ms ease-out both` }}>
        <span className="mb-3 flex size-11 items-center justify-center rounded-[12px] bg-field text-ink-3">
          <Icon name="book" size={20} />
        </span>
        <p className="text-[14px] font-medium text-ink">{t("knowledge-kbdetail.empty_title")}</p>
        <p className="mt-1 max-w-[320px] text-[12.5px] leading-relaxed text-ink-3">{t("knowledge-kbdetail.empty_body")}</p>
      </div>
    </div>
  );
}

type EntryDetailProps = {
  kind: string;
  id: string;
  /** Opened from a search: the places it was found come first. */
  query?: string;
  reloadKey: number;
  onToggleSidebar: () => void;
  onBack: () => void;
  onOpen: (e: KBEntry) => void;
  onSaved: () => void;
};

export function EntryDetail(props: EntryDetailProps) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  // the current assistant's entry (an address names no assistant: docs/assistant/managers.md "Clients"): a kept pick is
  // known at once, the default once the roster says which it is
  const picked = useChosenManager();
  const knownDefault = useKnownDefault();
  const manager = picked ?? knownDefault;
  if (!manager || !getToken()) return <Loading />;
  // Retained detail and editors belong to the requested route, owner and assistant: whatever is read and written here
  // (the tags, confirmed facts, relationships, minutes, a chat's days, a post) names that one assistant, and another
  // made current opens it again.
  return <OwnedEntryDetail key={JSON.stringify([props.kind, props.id, owner, manager])} {...props} owner={owner} manager={manager} />;
}

function OwnedEntryDetail({ kind, id, query, reloadKey, onToggleSidebar, onBack, onOpen, onSaved, owner, manager }: EntryDetailProps & { owner: string; manager: string }) {
  const [token] = useState(getToken);
  const current = useCallback(() => credentialOwner() === owner && shownManager() === manager, [owner, manager]);
  const auth = useMemo(() => ({ token, tokenOwner: owner, headers: { [MANAGER_HEADER]: manager } }), [token, owner, manager]);
  // A project's lessons from finished tasks (#12295): read by the section itself, drawn only when there are some.
  const lessonsDeps = useMemo(() => projectLessonsDeps(id, auth), [id, auth]);
  const request = useRef(0);
  const [resp, setResp] = useState<KBDetailResponse | null>(null);
  const [error, setError] = useState("");
  // the hub has no such entry (deleted, renamed, or an old link): said plainly, with nothing to retry
  const [gone, setGone] = useState(false);
  // The page's own read (opening, after a save elsewhere on the page, Retry) failed while an earlier detail is shown:
  // the detail stays and this says it may be out of date. Sections reading it again themselves show their own errors.
  const [reloadError, setReloadError] = useState("");
  const [retry, setRetry] = useState(0);
  const [editing, setEditing] = useState(false);
  const [asking, setAsking] = useState(false);
  const [allLog, setAllLog] = useState(false);
  const [allMatches, setAllMatches] = useState(false);
  const [allMoments, setAllMoments] = useState(false);
  const [showRecent, setShowRecent] = useState(false);
  const [day, setDay] = useState<{ day: string; focus?: number; query?: string } | null>(null);
  const [moment, setMoment] = useState<KBMomentRef | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  // Existing recordings stay readable through the default panel when their module is off. Other kinds that only a
  // module draws are not there while that module is off.
  const shown = kind === "recordings" || useKBKindShown(kind);

  const load = useCallback(async (signal?: AbortSignal) => {
    const version = ++request.current;
    const valid = () => current() && version === request.current && !signal?.aborted;
    try {
      // Each read owns its response and cancellation: a photos change may invalidate an older read before it answers.
      const r = await api<KBDetailResponse>(`/kb/${enc(kind)}/${enc(id)}${query ? `?q=${enc(query)}` : ""}`, { signal, ...auth, dedupe: false });
      if (!valid()) return false;
      // The server canonicalizes source IDs (including recording UUID case); the request owns this response.
      if (r.detail.kind !== kind) throw new Error(t("knowledge-confirmedfacts.entry_changed"));
      remember(r.detail.links ?? []);
      setResp(r);
      setError("");
      setGone(false);
      setReloadError("");
      return true;
    } catch (err) {
      if (!valid()) return false;
      const missing = err instanceof ApiError && err.status === 404;
      setGone(missing);
      if (!isAbort(err)) setError(errorText(err));
      throw err;
    }
  }, [kind, id, query, auth, current]);

  useEffect(() => {
    setError("");
    setReloadError("");
    const ac = new AbortController();
    void load(ac.signal).catch((err) => {
      if (!ac.signal.aborted && !isAbort(err)) setReloadError(err instanceof ApiError && err.status === 404 ? t("not-found.kb_gone") : errorText(err));
    });
    return () => { ac.abort(); request.current++; };
  }, [load, reloadKey, retry]);
  // The error the page shows (a failed read while the hub was away) is read again once it is back (its hello), as Retry
  // would. A section's own detail read that failed (a meeting's photos) keeps its own rules for when to read again.
  const failed = Boolean(resp ? reloadError : error);
  useEffect(() => (failed ? onHubEvent("hello", () => setRetry((x) => x + 1)) : undefined), [failed]);

  const d = resp?.detail;

  const base = known(kind, id);
  const e: KBEntry = d ? entryFromDetail(base, d) : (base ?? { kind, id, name: id, relevance: 0 });
  const status = statusLabel(projectInfo(e)?.status);

  // the main conversation opens at once with the message on its way (no main one yet: this starts it)
  const ask = async () => {
    setAsking(true);
    try {
      await askPersona(askPersonaText(e, d));
    } catch (err) {
      toast(t("knowledge-kbdetail.send_failed", { error: errorText(err) }), "error");
    } finally {
      setAsking(false);
    }
  };

  const facts = d ? factsOf(d.kind, d.meta) : null;
  // A recording that stands for an imported meeting too (#8872): its own notes there, one tap away.
  const imported = importedMeetingOf(d);
  const importedBrand = imported ? connectorBrand(imported.source ?? "") : undefined;
  const log = timeline(d?.log);
  const dirs = dirsOf(d?.meta);
  const recent = d?.kind === "chats" ? parseRecent(d.recent) : [];
  // a chat's Inbox items (#15356), and the rows of its recent messages each holds
  const inbox = d?.kind === "chats" ? resp?.inbox ?? [] : [];
  const rowItems = inboxOfRows(inbox);
  const into = typeof d?.meta?.merged_into === "string" ? d.meta.merged_into : "";
  const moments = d?.moments ?? [];

  // An entry the hub has no such entry for: the shared empty state and the way back, as a page or a personal Agent's
  // conversation that isn't there (NotFound, AgentsView), not the entry's page with its id, disabled buttons and a red
  // box (web usability audit item 42). The header names the list it would be in.
  if ((gone && !d) || !shown)
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-1.5 border-b border-line px-3 sm:px-4">
          <IconButton icon="sidebar" label={t("knowledge-kbdetail.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
          <IconButton icon="chevronLeft" label={t("knowledge-kbdetail.back")} onClick={onBack} className="@3xl:hidden" />
          <span className="min-w-0 flex-1 truncate text-meta font-medium text-ink-2">{isKind(kind) ? kindTitle(kind) : t("app.title_kb")}</span>
        </header>
        <div className="flex min-h-0 flex-1 flex-col pb-[10vh]" role="status">
          <Empty
            icon="book"
            title={t("not-found.kb_title")}
            detail={t("not-found.kb_detail")}
            action={<Button tone="secondary" icon="arrowLeft" onClick={onBack}>{t("knowledge-kbdetail.back")}</Button>}
          />
        </div>
      </div>
    );

  // The page, with the parts a module adds for this kind (core/ModuleKBDetail.tsx): none for most kinds.
  const page = (parts: ModuleKBDetailParts) => {
    // A topic's summary is its points as Markdown (for search and Persona): the page shows the points themselves. A
    // module that shows the summary itself (an all-day recording's recap) leaves it out too.
    const prof = d?.kind === "topics" || parts.hideProfile ? "" : profile(d?.summary, e.one_line, e.name);
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-1.5 border-b border-line px-3 sm:px-4">
          <IconButton icon="sidebar" label={t("knowledge-kbdetail.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
          <IconButton icon="chevronLeft" label={t("knowledge-kbdetail.back")} onClick={onBack} className="@3xl:hidden" />
          <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink-2">{kindTitle(kind)}</span>
          <Button tone="secondary" onClick={() => setEditing(true)} disabled={!d}>
            <KBIcon name="tag" size={14} stroke={2.1} />
            {t("knowledge-kbdetail.tag")}
          </Button>
        </header>

        <div ref={scroller} className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto w-full max-w-[720px] px-4 pt-6 pb-20 sm:px-8">
            {/* Header: what it is at a glance, and the way out (handing it to Persona). */}
            <div>
              <div className="flex items-center gap-3.5">
                <Tile entry={e} size={48} />
                <div className="min-w-0 flex-1">
                  <h1 className="line-clamp-3 text-[20px] font-semibold leading-snug tracking-tight text-ink">{e.name}</h1>
                  <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                    <Badges entry={e} status={status} asProject />
                  </div>
                </div>
              </div>
              {e.one_line && <p className="mt-3.5 text-[14px] leading-relaxed text-ink">{e.one_line}</p>}
              {e.tags && e.tags.length > 0 && (
                <div className="mt-3 flex flex-wrap gap-1.5">
                  {e.tags.map((t) => (
                    <TonePill key={t}>#{t}</TonePill>
                  ))}
                </div>
              )}
              <div className="mt-4 flex flex-wrap gap-2">
                <Button tone="accent" icon="spark" busy={asking} disabled={!d} onClick={ask} title={t("knowledge-kbdetail.ask_title")}>
                  {t("knowledge-kbdetail.ask")}
                </Button>
                {parts.actions}
                {kind === "projects" && (
                  <Button
                    tone="secondary"
                    icon="plus"
                    disabled={!d}
                    onClick={() => openNewAgent(agentPreset(kind, e, d, getState().machines.filter((m) => m.online).map((m) => m.id)))}
                    title={t("knowledge-kbdetail.new_agent_title")}
                  >
                    {t("knowledge-kbdetail.new_agent")}
                  </Button>
                )}
              </div>
            </div>

            {!d && !error && (
              <div className="py-12">
                <Loading />
              </div>
            )}
            {(d ? reloadError : error) && (
              <div className="mt-6">
                <ErrorBox text={d ? reloadError : error} onRetry={() => setRetry((x) => x + 1)} />
              </div>
            )}

            {d && (
              <div className="mt-7 flex flex-col gap-6" style={{ animation: "fade-in 200ms ease-out both" }}>
                {parts.lead}
                {facts && (
                  <section>
                    <Card>
                      {facts.at && (
                        <div className="flex items-center gap-3 px-4 py-2.5 text-[13px]">
                          <span className="flex-1 text-ink-2">{facts.whenLabel}</span>
                          <span className="whitespace-nowrap text-ink tabular-nums">{formatAt(facts.at)}</span>
                        </div>
                      )}
                      {facts.url && (
                        <button type="button" onClick={() => openPanel({ kind: "link", url: facts.url!, title: e.name })} className="flex w-full items-center gap-2 whitespace-nowrap px-4 py-2.5 text-left text-[13px] font-medium text-accent-ink hover:bg-hover">
                          <Icon name="external" size={14} />
                          {facts.openLabel}
                        </button>
                      )}
                    </Card>
                  </section>
                )}
                {imported && (
                  <section>
                    <Card>
                      <button type="button" onClick={() => openPanel({ kind: "link", url: imported.url, title: importedNotesLabel(imported.source) })} className="flex w-full items-center gap-2 whitespace-nowrap px-4 py-2.5 text-left text-meta font-medium text-accent-ink hover:bg-hover">
                        {importedBrand ? <BrandMark brand={importedBrand} size={16} fit="badge" /> : <Icon name="external" size={14} />}
                        {importedNotesLabel(imported.source)}
                      </button>
                    </Card>
                  </section>
                )}

                {parts.sections}

                {query && resp?.matches && (
                  <Matches
                    matches={resp.matches}
                    query={query}
                    all={allMatches}
                    onToggle={() => setAllMatches((x) => !x)}
                    onOpenDay={d.kind === "chats" ? (m) => setDay({ day: m.day!, focus: m.n, query }) : undefined}
                  />
                )}

                {resp?.agents && resp.agents.length > 0 && <Agents agents={resp.agents} />}

                {inbox.length > 0 && <ChatInbox items={inbox} manager={manager} />}

                {log.length > 0 && (
                  <section>
                    <SectionTitle title={t("knowledge-kbdetail.timeline")} count={log.length} />
                    <Card>
                      <ol className="px-4 pt-1.5 pb-1">
                        {(allLog ? log : log.slice(0, LOG_SHOWN)).map((l, i, shown) => {
                          const first = i === 0;
                          const last = i === shown.length - 1 && (allLog || log.length <= LOG_SHOWN);
                          return (
                            <li key={i} className="relative flex gap-3">
                              {/* The rail joins the dots: none above the newest, none below the oldest shown. */}
                              <span aria-hidden className="relative w-3 shrink-0">
                                {!first && <span className="absolute top-0 left-1/2 h-4 w-px -translate-x-1/2 bg-line-strong" />}
                                {!last && <span className="absolute top-4 bottom-0 left-1/2 w-px -translate-x-1/2 bg-line-strong" />}
                                <span className={cx("absolute top-[13px] left-1/2 size-2 -translate-x-1/2 rounded-full", first ? "bg-accent" : "bg-ink-3 opacity-60")} />
                              </span>
                              <div className="min-w-0 flex-1 py-2">
                                {(l.when || l.source) && (
                                  <div className={cx("flex min-w-0 items-baseline gap-2 text-[11.5px]", first ? "text-accent-ink" : "text-ink-3")}>
                                    {l.when && <span className="shrink-0 font-semibold tabular-nums">{shortWhen(l.when)}</span>}
                                    {l.source && <span className="truncate">{l.source}</span>}
                                  </div>
                                )}
                                <p className="mt-0.5 text-[13px] leading-relaxed text-ink">{l.text}</p>
                              </div>
                            </li>
                          );
                        })}
                      </ol>
                      {log.length > LOG_SHOWN && <MoreRow open={allLog} label={t("knowledge-kbdetail.show_all_entries", { count: log.length })} onClick={() => setAllLog((x) => !x)} />}
                    </Card>
                  </section>
                )}

                {d.kind === "projects" && <ProjectLessons key={id} deps={lessonsDeps} current={current} />}

                {isKind(kind) && <ConfirmedFacts
                  kind={kind}
                  id={id}
                  value={typeof d.meta?.pinned === "string" ? d.meta.pinned : ""}
                  auth={auth}
                  current={current}
                  refresh={load}
                  onSaved={onSaved}
                />}

                {d.kind === "topics" && <TopicPoints detail={d} onOpen={onOpen} />}

                {prof && (
                  <section>
                    <SectionTitle title={t("knowledge-kbdetail.profile")} />
                    <Card>
                      <div className="px-4 py-3.5">
                        <Markdown text={prof} small />
                      </div>
                    </Card>
                  </section>
                )}

                {into && (
                  <section>
                    <Card>
                      <a
                        href={href({ name: "kb", kind: "projects", id: into })}
                        onClick={(ev) => onPlainClick(ev, () => onOpen({ kind: "projects", id: into, name: into, relevance: 0 }))}
                        className="flex items-center gap-2 whitespace-nowrap px-4 py-2.5 text-[13px] font-medium text-accent-ink hover:bg-hover"
                      >
                        <Icon name="link" size={14} />
                        {t("knowledge-kbdetail.merged")}
                        <span className="flex-1" />
                        <Icon name="chevronRight" size={12} stroke={2.2} className="text-ink-3" />
                      </a>
                    </Card>
                    <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("knowledge-kbdetail.merged_hint")}</p>
                  </section>
                )}

                {moments.length > 0 && (
                  <section>
                    <SectionTitle title={t("knowledge-kbdetail.moments")} count={moments.length} />
                    <Card>
                      {(allMoments ? moments : moments.slice(0, LINKS_SHOWN)).map((m) => (
                        <button key={m.id} type="button" onClick={() => setMoment(m)} className="block w-full px-4 py-2.5 text-left transition-colors hover:bg-hover">
                          <span className="flex items-center gap-2 text-[11.5px]">
                            <span className="truncate font-semibold text-ink">{m.author}</span>
                            {ago(m.at) && <span className="shrink-0 text-ink-3">{ago(m.at)}</span>}
                            <span className="flex-1" />
                            {m.comments > 0 && (
                              <span className="flex shrink-0 items-center gap-1 text-ink-3 tabular-nums" aria-label={t("knowledge-kbdetail.comments", { count: m.comments })}>
                                <Icon name="chat" size={12} />
                                {m.comments}
                              </span>
                            )}
                          </span>
                          {m.note && <span className="mt-0.5 block truncate text-[12.5px] font-medium text-accent-ink">{m.note}</span>}
                          <span className="mt-0.5 line-clamp-2 text-[12.5px] leading-snug text-ink-2">{m.text}</span>
                        </button>
                      ))}
                      {moments.length > LINKS_SHOWN && <MoreRow open={allMoments} label={t("knowledge-kbdetail.all_entries", { count: moments.length })} onClick={() => setAllMoments((x) => !x)} />}
                    </Card>
                  </section>
                )}

                <Relationships
                  detail={d}
                  name={e.name}
                  auth={auth}
                  current={current}
                  onOpen={onOpen}
                  onRefreshed={(r) => {
                    if (!current()) return;
                    request.current++;
                    remember(r.detail.links ?? []);
                    setResp(r);
                    setError("");
                  }}
                  onSaved={onSaved}
                />

                {dirs.length > 0 && <Dirs dirs={dirs} />}

                {parts.tail}

                {recent.length > 0 && (
                  <section>
                    <Card>
                      <button type="button" onClick={() => setShowRecent((x) => !x)} aria-expanded={showRecent} className="flex h-10 w-full items-center gap-2 px-4 text-left transition-colors hover:bg-hover">
                        <Icon name="chat" size={14} className="text-ink-3" />
                        <span className="flex-1 whitespace-nowrap text-[13px] text-ink">{t("knowledge-kbdetail.last_7_days")}</span>
                        <span className="text-[12px] text-ink-3 tabular-nums">{recent.reduce((n, x) => n + x.messages.length, 0)}</span>
                        <Icon name="chevronRight" size={12} stroke={2.2} className={cx("text-ink-3 transition-transform duration-200", showRecent && "rotate-90")} />
                      </button>
                      <Collapse open={showRecent}>
                        {showRecent && (
                          <div className="border-t border-line px-4 pt-2 pb-3">
                            {recent.map((g, i) => (
                              <div key={g.day ?? i} className="mt-2 first:mt-0">
                                {g.day && (
                                  <div className="mb-1 flex items-center gap-2">
                                    <span className="text-[11.5px] font-semibold text-ink-3">{dayTitle(g.day)}</span>
                                    <span className="flex-1" />
                                    <button type="button" onClick={() => setDay({ day: g.day! })} className="whitespace-nowrap rounded-[6px] px-1.5 py-0.5 text-[11.5px] font-medium text-accent-ink hover:bg-hover">
                                      {t("knowledge-kbdetail.view_day")}
                                    </button>
                                  </div>
                                )}
                                <div className="flex flex-col gap-2">
                                  {g.messages.map((m, j) => {
                                    const item = rowItems.get(messageRowKey(g.day, m));
                                    return item ? (
                                      <InboxMessageLink key={j} item={item} manager={manager}>
                                        <MessageView m={m} />
                                      </InboxMessageLink>
                                    ) : (
                                      <MessageView key={j} m={m} />
                                    );
                                  })}
                                </div>
                              </div>
                            ))}
                          </div>
                        )}
                      </Collapse>
                    </Card>
                  </section>
                )}

                <section>
                  <SectionTitle title={t("knowledge-kbdetail.directory")} />
                  <Card>
                    <div className="flex items-center gap-2 px-4 py-2">
                      <span className="min-w-0 flex-1 font-mono text-[12px] break-all text-ink-2 select-all">{d.path}</span>
                      <IconButton icon="copy" label={t("knowledge-kbdetail.copy_directory")} onClick={() => void copyText(d.path)} />
                    </div>
                  </Card>
                  <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">{t("knowledge-kbdetail.directory_hint")}</p>
                </section>
              </div>
            )}
          </div>
        </div>

        {d && <TagEditor entry={e} auth={auth} open={editing} onClose={() => setEditing(false)} onSaved={onSaved} />}
        {parts.dialogs}
        {d?.kind === "chats" && <DayDialog chat={d.id} chatName={e.name} auth={auth} day={day?.day ?? null} focus={day?.focus} query={day?.query} onClose={() => setDay(null)} />}
        <MomentDialog moment={moment} auth={auth} onClose={() => setMoment(null)} />
      </div>
    );
  };

  return (
    <ModuleKBDetail kind={kind} id={id} query={query} detail={d} entry={e} auth={auth} current={current} manager={manager} reload={load} onSaved={onSaved} onOpen={onOpen} scroller={scroller} readOnly={kind === "recordings" ? RecordingReadOnlyDetail : undefined}>
      {page}
    </ModuleKBDetail>
  );
}

/** Where the search is in this entry: each place with what is around it; a message opens its day at it. */
function Matches({ matches, query, all, onToggle, onOpenDay }: { matches: KBMatch[]; query: string; all: boolean; onToggle: () => void; onOpenDay?: (m: KBMatch) => void }) {
  const shown = all ? matches : matches.slice(0, MATCHES_SHOWN);
  return (
    <section>
      <SectionTitle title={t("knowledge-kbdetail.search_title", { query })} count={matches.length >= 40 ? "40+" : matches.length} />
      <Card>
        {!matches.length && <p className="px-4 py-2.5 text-[12.5px] text-ink-2">{t("knowledge-kbdetail.search_in_name", { query })}</p>}
        {shown.map((m) => {
          const v = hitView(m);
          const body = (
            <>
              {/* a message shows its own time below */}
              <span className="block truncate text-[11px] text-ink-3">{m.day ? t("knowledge-kbdetail.chat_history_day", { day: dayTitle(m.day) }) : v.source}</span>
              {m.day ? (
                <span className="mt-0.5 flex flex-col gap-0.5">
                  {m.before?.length ? <span className="truncate text-[11.5px] text-ink-3">{line(m.before[m.before.length - 1])}</span> : null}
                  <MessageView m={parseMessageLine(m.line)} query={query} clamp />
                  {m.after?.length ? <span className="truncate text-[11.5px] text-ink-3">{line(m.after[0])}</span> : null}
                </span>
              ) : (
                <span className="mt-0.5 line-clamp-4 text-[13px] leading-relaxed text-ink-2">
                  <Marked text={v.text} query={query} />
                </span>
              )}
            </>
          );
          return m.day && onOpenDay ? (
            <button key={`${m.path}#${m.n}`} type="button" onClick={() => onOpenDay(m)} className="block w-full px-4 py-2.5 text-left transition-colors hover:bg-hover">
              {body}
            </button>
          ) : (
            <div key={`${m.path}#${m.n}`} className="px-4 py-2.5">
              {body}
            </div>
          );
        })}
        {matches.length > MATCHES_SHOWN && <MoreRow open={all} label={t("knowledge-kbdetail.show_all_matches", { count: matches.length })} onClick={onToggle} />}
      </Card>
    </section>
  );
}

function line(raw: string) {
  const m = parseMessageLine(raw);
  return m.sender ? t("knowledge-kbdetail.message_line", { sender: m.sender, text: m.text }) : m.text;
}

/** The Agents working in the project's code directories, live from the store when it has them. */
function Agents({ agents }: { agents: Session[] }) {
  const sessions = useStore((s) => s.sessions);
  const machines = useStore((s) => s.machines);
  const live = agents.map((a) => (sessions[a.machine_id] ?? []).find((x) => x.session_id === a.session_id) ?? a);
  return (
    <section>
      <SectionTitle title={t("knowledge-kbdetail.agents")} count={live.length} />
      <Card>
        {live.map((s) => {
          // a drill-down: Back and the sidebar stay with this project (#12055)
          const to = { name: "session" as const, mid: s.machine_id, sid: s.session_id };
          const st = stateOf(s);
          const preview = plainMarkdown(sessionPreview(s));
          return (
            <a key={`${s.machine_id}/${s.session_id}`} href={href(to)} onClick={(ev) => onLinkClick(ev, to, openedFromHere())} className="flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-hover">
              <span className="flex w-3 shrink-0 justify-center">
                <StatusMark state={st} />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13px] font-medium text-ink">{sessionTitle(s)}</span>
                <span className="block truncate text-[11.5px] text-ink-3">
                  {[machineWithHub(s.machine_id, machines), s.space_name, tabLabel(s)].filter(Boolean).join(" › ")}
                  {preview ? ` · ${preview}` : ""}
                </span>
              </span>
              <StatePill state={st} />
            </a>
          );
        })}
      </Card>
    </section>
  );
}

function Dirs({ dirs }: { dirs: { machine: string; path: string }[] }) {
  const machines = useStore((s) => s.machines);
  return (
    <section>
      <SectionTitle title={t("knowledge-kbdetail.code_dirs")} count={dirs.length} />
      <Card>
        {dirs.map((x) => (
          <div key={`${x.machine}:${x.path}`} className="flex items-center gap-2.5 px-4 py-2">
            <Icon name="folder" size={14} className="shrink-0 text-ink-3" />
            <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-ink" title={x.path}>
              {shortPath(x.path)}
            </span>
            {x.machine && <span className="shrink-0 whitespace-nowrap text-[11.5px] text-ink-3">{machineWithHub(x.machine, machines)}</span>}
            <IconButton icon="copy" label={t("knowledge-kbdetail.copy_path")} size={24} iconSize={13} onClick={() => void copyText(x.path)} />
          </div>
        ))}
      </Card>
    </section>
  );
}
