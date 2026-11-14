// The knowledge list: search, the six kinds as chips with a tag filter, the kind's entries grouped the way the app
// groups them (docs/assistant/knowledge.md "Pages"), and search results merged by entry.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api, ApiError, enc, errorText, isAbort } from "../../lib/api";
import { ago } from "../../lib/format";
import {
  groupEntries,
  groupResults,
  hitView,
  KB_KINDS,
  KIND_TITLE,
  projectInfo,
  rowTags,
  statusLabel,
  topicPointCount,
  type KBEntry,
  type KBKind,
  type KBList,
  type KBResult,
} from "../../lib/kb";
import { href, navigate } from "../../lib/router";
import { onHubEvent } from "../../lib/store";
import { Button, Collapse, cx, Dialog, EASE, IconButton, Loading, Menu, Spinner, toast } from "../../ui/atoms";
import { contextMenuProps } from "../../ui/context-menu";
import type { MenuAt } from "../../ui/menu-model";
import { Icon } from "../../ui/icons";
import { lists, listKey, remember, searches, searchKey } from "./cache";
import { Badges, Card, KBIcon, KIND_ICON, Marked, onPlainClick, SectionTitle, Tile } from "./KBParts";
import { t } from "../../i18n";

/** Rows drawn at once in a section; more on "Show more" (people run to thousands). */
const PAGE = 150;

export function ListPane({
  kind,
  selected,
  detailOpen,
  reloadKey,
  onToggleSidebar,
  onOpen,
}: {
  kind: KBKind;
  selected?: { kind: string; id: string };
  detailOpen: boolean;
  reloadKey: number;
  onToggleSidebar: () => void;
  onOpen: (e: KBEntry, query?: string) => void;
}) {
  const [tags, setTags] = useState<Partial<Record<KBKind, string>>>({});
  const tag = tags[kind];
  const key = listKey(kind, tag);
  const [list, setList] = useState<KBList | undefined>(() => lists.get(key));
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);

  useEffect(() => {
    setList(lists.get(key));
    setError("");
    const ac = new AbortController();
    api<KBList>(`/kb/${kind}${tag ? `?tag=${enc(tag)}` : ""}`, { signal: ac.signal })
      .then((l) => {
        const v = { items: l.items ?? [], tags: l.tags ?? [] };
        lists.set(key, v);
        remember(v.items);
        setList(v);
      })
      .catch((e) => !isAbort(e) && setError(errorText(e)));
    return () => ac.abort();
  }, [key, kind, tag, reloadKey, retry]);

  // ── search ──
  const [query, setQuery] = useState("");
  const [found, setFound] = useState<{ q: string; results: KBResult[] } | null>(null);
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState("");
  const [searchRetry, setSearchRetry] = useState(0);
  const q = query.trim();

  useEffect(() => {
    setSearchError("");
    if (!q) {
      setFound(null);
      setSearching(false);
      return;
    }
    // A cached search paints at once and is still asked again (the hub may have learned new entries since), like the
    // lists above; a failed refresh keeps the cached results without an error.
    const sk = searchKey(q);
    const cached = searches.get(sk);
    if (cached) setFound({ q, results: cached });
    setSearching(!cached);
    const ac = new AbortController();
    const timer = setTimeout(() => {
      api<{ results: KBResult[] | null }>(`/kb-search?q=${enc(q)}&results=1`, { signal: ac.signal })
        .then((r) => {
          const results = r.results ?? [];
          searches.set(sk, results);
          remember(results.map((x) => x.entry));
          setFound({ q, results });
        })
        .catch((e) => !isAbort(e) && !cached && setSearchError(errorText(e)))
        .finally(() => !ac.signal.aborted && setSearching(false));
    }, 300); // typing
    return () => {
      clearTimeout(timer);
      ac.abort();
    };
  }, [q, reloadKey, searchRetry]);

  // A read that failed while the hub was away is read again once it is back (its hello), as Retry would, so the page
  // does not stay on the error. Reads that went through are not repeated on a hello.
  useEffect(() => {
    if (!error && !searchError) return;
    return onHubEvent("hello", () => {
      if (error) setRetry((x) => x + 1);
      if (searchError) setSearchRetry((x) => x + 1);
    });
  }, [error, searchError]);

  const sections = useMemo(() => (list?.items ? groupEntries(kind, list.items, tag) : []), [list, kind, tag]);
  const [open, setOpen] = useState<Set<string>>(new Set());
  const [limits, setLimits] = useState<Record<string, number>>({});
  const toggle = (k: string) =>
    setOpen((s) => {
      const n = new Set(s);
      if (n.has(k)) n.delete(k);
      else n.add(k);
      return n;
    });
  const isSel = (e: KBEntry) => selected?.kind === e.kind && selected.id === e.id;
  const setTag = (t?: string) => setTags((x) => ({ ...x, [kind]: t }));

  // ── deleting a project (soft, with Undo: docs/assistant/knowledge.md "Deleting a project") ──
  const [rowMenu, setRowMenu] = useState<{ e: KBEntry; at: MenuAt } | null>(null);
  const [confirm, setConfirm] = useState<KBEntry | null>(null);
  const [deleting, setDeleting] = useState(false);
  const deleteProject = async (e: KBEntry) => {
    if (deleting) return;
    setDeleting(true);
    let trash: { id: string };
    try {
      trash = await api<{ id: string }>(`/kb/projects/${enc(e.id)}`, { method: "DELETE" });
    } catch (err) {
      toast(t("knowledge-kblist.delete_failed", { error: errorText(err) }), "error");
      return;
    } finally {
      setDeleting(false);
    }
    setConfirm(null);
    // Gone on the hub: drop it from every cached projects list now rather than after a reload.
    for (const [k, v] of lists) if (k.includes("|projects|")) lists.set(k, { ...v, items: (v.items ?? []).filter((x) => x.id !== e.id) });
    searches.clear();
    setList((l) => l && { ...l, items: (l.items ?? []).filter((x) => !(x.kind === e.kind && x.id === e.id)) });
    if (isSel(e)) navigate({ name: "kb", kind: "projects" });
    toast(t("knowledge-kblist.deleted", { name: e.name }), "ok", { label: t("knowledge-kblist.undo"), run: () => void restoreProject(e, trash.id) });
  };
  const restoreProject = async (e: KBEntry, trashId: string) => {
    try {
      await api(`/kb-trash/${enc(trashId)}/restore`, { method: "POST" });
    } catch (err) {
      toast(err instanceof ApiError && err.status === 409 ? t("knowledge-kblist.restore_conflict", { name: e.name }) : t("knowledge-kblist.restore_failed", { error: errorText(err) }), "error");
      return;
    }
    searches.clear();
    setRetry((x) => x + 1);
    toast(t("knowledge-kblist.restored", { name: e.name }), "ok");
  };
  const onRowMenu = (e: KBEntry) => (e.kind === "projects" ? (at: MenuAt) => setRowMenu({ e, at }) : undefined);

  return (
    <section className={cx("min-h-0 min-w-0 flex-col @3xl:w-[400px] @3xl:shrink-0 @3xl:border-r @3xl:border-line", detailOpen ? "hidden @3xl:flex" : "flex w-full")}>
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("knowledge-kblist.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <h1 className="flex-1 truncate text-[14px] font-semibold text-ink">{t("knowledge-kblist.title")}</h1>
        <Button tone="secondary" icon="branch" title={t("knowledge-kblist.map_title")} onClick={() => navigate({ name: "kb", kind: "map" })}>
          {t("knowledge-kblist.map")}
        </Button>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-[760px] px-3 pt-3 pb-16 sm:px-4">
          <label className="flex h-9 items-center gap-2 rounded-control bg-surface px-3 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
            {searching ? <Spinner size={13} /> : <Icon name="search" size={13} stroke={2} className="shrink-0 text-ink-3" />}
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && setQuery("")}
              placeholder={t("knowledge-kblist.search_placeholder")}
              aria-label={t("knowledge-kblist.search_label")}
              className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
            />
            {query && <IconButton icon="x" label={t("knowledge-kblist.clear")} size={22} iconSize={12} onClick={() => setQuery("")} />}
          </label>

          {q ? (
            <SearchResults
              q={q}
              found={found}
              error={searchError}
              searching={searching}
              isSel={isSel}
              onOpen={onOpen}
              onRetry={() => setSearchRetry((x) => x + 1)}
            />
          ) : (
            <>
              <div className="mt-3 flex items-start gap-2">
                {/* Search covers every kind: the chips (one of them lit) would say otherwise, so they go while searching.
                    The chips carry no count: a count on the selected one only changed its width, so picking another kind
                    re-wrapped the row and moved every chip after it; each section under them says how many it holds. With
                    nothing that changes their width they stay put, while a kind loads too. */}
                <div role="tablist" aria-label={t("knowledge-kblist.kinds")} className="flex min-w-0 flex-1 flex-wrap gap-1">
                  {KB_KINDS.map((k) => {
                    const on = k === kind;
                    return (
                      <a
                        key={k}
                        role="tab"
                        aria-selected={on}
                        href={href({ name: "kb", kind: k })}
                        onClick={(e) => onPlainClick(e, () => navigate({ name: "kb", kind: k }))}
                        className={cx(
                          "inline-flex h-7 items-center gap-1 whitespace-nowrap rounded-full px-2.5 text-[12.5px] font-medium transition-[background-color,color,transform] duration-150 active:scale-[0.96]",
                          on ? "bg-ink text-surface" : "bg-field text-ink-2 hover:bg-hover-2 hover:text-ink",
                        )}
                      >
                        {KIND_TITLE[k]}
                      </a>
                    );
                  })}
                </div>
                <TagFilter tags={list?.tags ?? lists.get(listKey(kind))?.tags ?? []} value={tag} onChange={setTag} />
              </div>
              {tag && (
                <div className="mt-2 flex items-center gap-1.5 px-1 text-[12px] text-ink-3">
                  {t("knowledge-kblist.only")}
                  <button type="button" onClick={() => setTag(undefined)} className="inline-flex h-6 items-center gap-1 whitespace-nowrap rounded-full bg-accent-tint px-2 font-medium text-accent-ink hover:brightness-105" title={t("knowledge-kblist.clear_filter_title")}>
                    #{tag}
                    <Icon name="x" size={11} stroke={2.4} />
                  </button>
                </div>
              )}

              <div className="mt-4 flex flex-col gap-5">
                {!list && !error && (
                  <div className="py-6">
                    <Loading />
                  </div>
                )}
                {error && !list && <ErrorBox text={error} onRetry={() => setRetry((x) => x + 1)} />}
                {list && sections.length === 0 && <Empty kind={kind} tag={tag} />}
                {sections.map((s) => {
                  const k = `${kind}:${s.key}`;
                  const shown = !s.fold || open.has(k);
                  const limit = limits[k] ?? PAGE;
                  const rows = s.items.slice(0, limit).map((e) => <EntryRow key={`${e.kind}/${e.id}`} e={e} tag={tag} selected={isSel(e)} onOpen={onOpen} onMenu={onRowMenu(e)} />);
                  const more = s.items.length - limit;
                  const moreRow = more > 0 && (
                    <button type="button" onClick={() => setLimits((x) => ({ ...x, [k]: limit + PAGE }))} className="flex h-9 w-full items-center whitespace-nowrap px-4 text-[12.5px] font-medium text-accent-ink hover:bg-hover">
                      {t("knowledge-kblist.show_more", { count: Math.min(more, PAGE), more })}
                    </button>
                  );
                  return (
                    <div key={k} style={{ animation: `fade-up 320ms ${EASE} both` }}>
                      {s.fold ? (
                        <>
                          <Card>
                            <button type="button" onClick={() => toggle(k)} aria-expanded={shown} className="flex h-10 w-full items-center gap-2 px-3.5 text-left transition-colors hover:bg-hover">
                              <span className="whitespace-nowrap text-[13px] text-ink-2">{s.title}</span>
                              <span className="text-[12px] text-ink-3 tabular-nums">{s.items.length}</span>
                              <span className="flex-1" />
                              <Icon name="chevronRight" size={12} stroke={2.2} className={cx("text-ink-3 transition-transform duration-200", shown && "rotate-90")} />
                            </button>
                            <Collapse open={shown}>
                              {shown && (
                                <div className="divide-y divide-line border-t border-line">
                                  {rows}
                                  {moreRow}
                                </div>
                              )}
                            </Collapse>
                          </Card>
                          {s.footer && <p className="mt-1.5 px-1 text-[11.5px] leading-relaxed text-ink-3">{s.footer}</p>}
                        </>
                      ) : (
                        <>
                          <SectionTitle title={s.title} count={s.items.length} />
                          <Card>
                            {rows}
                            {moreRow}
                          </Card>
                        </>
                      )}
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </div>
      </div>
      {rowMenu && (
        <Menu
          anchor={rowMenu.at.anchor ?? null}
          point={rowMenu.at.point}
          onClose={() => setRowMenu(null)}
          items={[{ key: "delete", label: t("knowledge-kblist.delete"), icon: "trash", danger: true, onSelect: () => setConfirm(rowMenu.e) }]}
        />
      )}
      <Dialog open={Boolean(confirm)} onClose={() => setConfirm(null)} title={t("knowledge-kblist.delete_title")}>
        <div className="py-2 text-[13.5px] text-ink-2">{confirm && t("knowledge-kblist.delete_confirm", { name: confirm.name })}</div>
        <div className="mt-4 flex justify-end gap-2">
          <Button tone="secondary" onClick={() => setConfirm(null)}>
            {t("common.cancel")}
          </Button>
          <Button tone="danger" busy={deleting} onClick={() => confirm && void deleteProject(confirm)}>
            {t("knowledge-kblist.delete")}
          </Button>
        </div>
      </Dialog>
    </section>
  );
}

/** One ranked entry: the kind tile carrying the importance, the name, its one line, then badges and the tags that fit. */
function EntryRow({ e, tag, selected, onOpen, onMenu }: { e: KBEntry; tag?: string; selected: boolean; onOpen: (e: KBEntry) => void; onMenu?: (at: MenuAt) => void }) {
  const tags = rowTags(e, tag);
  const when = ago(e.updated);
  const status = projectInfo(e)?.status === "paused" ? statusLabel("paused") : null;
  const badges = e.importance || e.relevance > 0 || tags.length || status || e.kind === "projects" || topicPointCount(e) !== null;
  return (
    <a
      href={href({ name: "kb", kind: e.kind, id: e.id })}
      onClick={(ev) => onPlainClick(ev, () => onOpen(e))}
      aria-current={selected ? "page" : undefined}
      aria-haspopup={onMenu ? "menu" : undefined}
      className={cx("flex gap-3 px-3.5 py-3 transition-colors", selected ? "bg-hover-2" : "hover:bg-hover")}
      {...(onMenu ? contextMenuProps((at) => onMenu(at)) : {})}
    >
      <Tile entry={e} />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          {/* Two lines: documents and meetings are told apart by the end of their titles. */}
          <span className="line-clamp-2 min-w-0 flex-1 text-[13.5px] font-medium leading-snug text-ink">{e.name}</span>
          {when && <span className="shrink-0 whitespace-nowrap text-[11px] text-ink-3">{when}</span>}
        </span>
        {e.one_line && <span className="mt-0.5 line-clamp-2 text-[12.5px] leading-snug text-ink-2">{e.one_line}</span>}
        {badges && (
          // One line high: tags that would wrap fall below it and out of sight, so only whole ones show.
          <span className="mt-1.5 flex h-[18px] flex-wrap items-center gap-x-1.5 gap-y-4 overflow-hidden">
            <Badges entry={e} status={status} />
            {tags.map((t) => (
              <span key={t} className="whitespace-nowrap text-[11px] text-ink-3">
                #{t}
              </span>
            ))}
          </span>
        )}
      </span>
    </a>
  );
}

function SearchResults({
  q,
  found,
  error,
  searching,
  isSel,
  onOpen,
  onRetry,
}: {
  q: string;
  found: { q: string; results: KBResult[] } | null;
  error: string;
  searching: boolean;
  isSel: (e: KBEntry) => boolean;
  onOpen: (e: KBEntry, query?: string) => void;
  onRetry: () => void;
}) {
  // Results of an older query only stand in (dimmed) while the current one runs; once it fails, its error wins.
  if (error && found?.q !== q) return <div className="mt-4"><ErrorBox text={error} onRetry={onRetry} /></div>;
  if (!found)
    return (
      <div className="py-10">
        <Loading label={t("knowledge-kblist.searching")} />
      </div>
    );
  if (!found.results.length) {
    return (
      <div className={cx("mt-10 flex flex-col items-center px-6 text-center transition-opacity", searching && "opacity-50")}>
        <span className="mb-3 flex size-10 items-center justify-center rounded-[11px] bg-field text-ink-3">
          <Icon name="search" size={18} />
        </span>
        <p className="text-[13.5px] font-medium text-ink">{t("knowledge-kblist.no_results", { query: found.q })}</p>
        <p className="mt-1 text-[12.5px] text-ink-3">{t("knowledge-kblist.no_results_hint")}</p>
      </div>
    );
  }
  return (
    <div className={cx("mt-4 flex flex-col gap-5 transition-opacity", searching && "opacity-60")}>
      {groupResults(found.results).map((g) => (
        <div key={g.kind}>
          <SectionTitle title={g.title} count={g.results.length} />
          <Card>
            {g.results.map((r) => (
              <ResultRow key={`${r.entry.kind}/${r.entry.id}`} r={r} q={found.q} selected={isSel(r.entry)} onOpen={(e) => onOpen(e, found.q)} />
            ))}
          </Card>
        </div>
      ))}
    </div>
  );
}

/** One entry a search found: its tile and name, then up to two matching lines with the words in bold. */
function ResultRow({ r, q, selected, onOpen }: { r: KBResult; q: string; selected: boolean; onOpen: (e: KBEntry) => void }) {
  const hits = (r.hits ?? []).slice(0, 2).map((h) => hitView(h));
  return (
    <a
      href={href({ name: "kb", kind: r.entry.kind, id: r.entry.id })}
      onClick={(ev) => onPlainClick(ev, () => onOpen(r.entry))}
      aria-current={selected ? "page" : undefined}
      className={cx("flex gap-3 px-3.5 py-2.5 transition-colors", selected ? "bg-hover-2" : "hover:bg-hover")}
    >
      <Tile entry={r.entry} size={30} />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">
            <Marked text={r.entry.name} query={q} />
          </span>
          {r.count > hits.length && <span className="shrink-0 whitespace-nowrap text-[11px] text-ink-3">{r.count >= 99 ? t("knowledge-kblist.hits_99_plus") : t("knowledge-kblist.hits", { count: r.count })}</span>}
        </span>
        {!hits.length && r.entry.one_line && (
          <span className="mt-0.5 line-clamp-2 text-[12.5px] leading-snug text-ink-2">
            <Marked text={r.entry.one_line} query={q} />
          </span>
        )}
        {hits.map((h, i) => (
          <span key={i} className="mt-1 block">
            <span className="block truncate text-[11px] text-ink-3">{h.source}</span>
            <span className="line-clamp-2 text-[12.5px] leading-snug text-ink-2">
              <Marked text={h.text} query={q} />
            </span>
          </span>
        ))}
      </span>
    </a>
  );
}

const EMPTY_TITLE: Record<KBKind, string> = {
  projects: t("knowledge-kblist.empty_projects"),
  chats: t("knowledge-kblist.empty_chats"),
  people: t("knowledge-kblist.empty_people"),
  docs: t("knowledge-kblist.empty_docs"),
  meetings: t("knowledge-kblist.empty_meetings"),
  events: t("knowledge-kblist.empty_events"),
  recordings: t("knowledge-kblist.empty_recordings"),
  topics: t("knowledge-kblist.empty_topics"),
};

/** Where a kind's entries come from, for its empty page: topics are curated, events come from a connected calendar and
 *  recordings from the phone; the rest from the archived chats. Under a tag the page is empty because of the filter, so
 *  it says how to see the rest instead. */
const EMPTY_BODY: Partial<Record<KBKind, string>> = {
  topics: t("knowledge-kblist.empty_topics_body"),
  events: t("knowledge-kblist.empty_events_body"),
  recordings: t("knowledge-kblist.empty_recordings_body"),
};

function Empty({ kind, tag }: { kind: KBKind; tag?: string }) {
  return (
    <div className="flex flex-col items-center px-6 py-10 text-center">
      <span className="mb-3 flex size-10 items-center justify-center rounded-[11px] bg-field text-ink-3">
        <KBIcon name={KIND_ICON[kind]} size={18} />
      </span>
      <p className="text-[13.5px] font-medium text-ink">{tag ? t("knowledge-kblist.empty_tag", { tag, kind: KIND_TITLE[kind] }) : EMPTY_TITLE[kind]}</p>
      <p className="mt-1 max-w-[340px] text-[12.5px] leading-relaxed text-ink-3">{tag ? t("knowledge-kblist.empty_tag_body", { kind: KIND_TITLE[kind] }) : EMPTY_BODY[kind] ?? t("knowledge-kblist.empty_body")}</p>
    </div>
  );
}

export function ErrorBox({ text, onRetry }: { text: string; onRetry?: () => void }) {
  return (
    <div className="flex items-center gap-2 rounded-card bg-red-tint px-3 py-2.5 text-[12.5px] text-red">
      <span className="min-w-0 flex-1">{text}</span>
      {onRetry && (
        <button type="button" onClick={onRetry} className="shrink-0 whitespace-nowrap rounded-[6px] px-2 py-1 font-medium hover:bg-surface">
          {t("knowledge-kblist.retry")}
        </button>
      )}
    </div>
  );
}

/** The filter icon and its popover: every tag of this kind as a chip (a menu ran off the screen with thirty). */
export function TagFilter({ tags, value, onChange }: { tags: string[]; value?: string; onChange: (t?: string) => void }) {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  return (
    <>
      <span className="relative">
        <IconButton icon="filter" label={value ? t("knowledge-kblist.filter_current", { tag: value }) : t("knowledge-kblist.filter")} active={Boolean(value) || Boolean(anchor)} onClick={(e) => { const t = e.currentTarget; setAnchor((a) => (a ? null : t)); }} />
        {value && <span aria-hidden className="pointer-events-none absolute top-1 right-1 size-1.5 rounded-full bg-accent" />}
      </span>
      {anchor && (
        <TagPopover
          anchor={anchor}
          tags={tags}
          value={value}
          onPick={(t) => {
            onChange(t);
            setAnchor(null);
          }}
          onClose={() => setAnchor(null)}
        />
      )}
    </>
  );
}

function TagPopover({ anchor, tags, value, onPick, onClose }: { anchor: HTMLElement; tags: string[]; value?: string; onPick: (t?: string) => void; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<React.CSSProperties>({ visibility: "hidden" });
  const focused = useRef(false);
  useLayoutEffect(() => {
    const r = anchor.getBoundingClientRect();
    const width = Math.min(300, innerWidth - 16);
    setPos({ top: r.bottom + 6, left: Math.max(8, Math.min(r.right - width, innerWidth - width - 8)), width });
  }, [anchor]);
  // The popover is portalled to the end of <body>, so Tab from the trigger would walk the rest of the page: once it is
  // placed (a hidden element cannot take focus), move focus to the current chip.
  useLayoutEffect(() => {
    if (focused.current || pos.visibility === "hidden") return;
    focused.current = true;
    const chips = chipButtons(ref.current);
    (chips.find((b) => b.getAttribute("aria-pressed") === "true") ?? chips[0])?.focus({ preventScroll: true });
  }, [pos]);
  // On close (Esc, a pick, the trigger again or a click outside) hand focus back to the trigger when it was in here or
  // dropped to <body>; a click outside then moves it on to what was clicked as usual. A layout cleanup runs before the
  // portal leaves the DOM, while the focused chip is still inside it.
  useLayoutEffect(() => {
    const node = ref.current;
    return () => {
      const active = document.activeElement;
      if (anchor.isConnected && (!active || active === document.body || node?.contains(active))) anchor.focus({ preventScroll: true });
    };
  }, [anchor]);
  useEffect(() => {
    const down = (e: MouseEvent) => {
      if (ref.current?.contains(e.target as Node) || anchor.contains(e.target as Node)) return;
      onClose();
    };
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    addEventListener("mousedown", down);
    addEventListener("keydown", key);
    return () => {
      removeEventListener("mousedown", down);
      removeEventListener("keydown", key);
    };
  }, [anchor, onClose]);
  const chip = (label: string, on: boolean, pick: () => void) => (
    <button
      key={label}
      type="button"
      onClick={pick}
      aria-pressed={on}
      className={cx("inline-flex h-7 max-w-full items-center gap-1 whitespace-nowrap rounded-full px-2.5 text-[12px] font-medium transition-colors", on ? "bg-ink text-surface" : "bg-field text-ink-2 hover:bg-hover-2 hover:text-ink")}
    >
      {on && <Icon name="check" size={11} stroke={2.6} />}
      <span className="truncate">{label}</span>
    </button>
  );
  return createPortal(
    <div ref={ref} role="dialog" aria-label={t("knowledge-kblist.filter")} onKeyDown={(e) => moveAmongChips(e, ref.current)} className="fixed z-50 rounded-[12px] bg-surface p-2.5 shadow-overlay" style={{ ...pos, animation: `pop-in 160ms ${EASE} both`, transformOrigin: "top right" }}>
      <div className="mb-2 flex items-center px-0.5">
        <span className="flex-1 text-[11.5px] font-medium text-ink-3">{t("knowledge-kblist.common_tags")}</span>
        {value && (
          <button type="button" onClick={() => onPick(undefined)} className="whitespace-nowrap text-[11.5px] font-medium text-accent-ink hover:underline">
            {t("knowledge-kblist.clear_filter")}
          </button>
        )}
      </div>
      <div data-chips className="flex max-h-[min(55vh,340px)] flex-wrap gap-1.5 overflow-y-auto">
        {chip(t("knowledge-kblist.all"), !value, () => onPick(undefined))}
        {tags.map((t) => chip(`#${t}`, value === t, () => onPick(t)))}
        {!tags.length && <p className="w-full px-0.5 py-2 text-[12px] text-ink-3">{t("knowledge-kblist.no_tags")}</p>}
      </div>
    </div>,
    document.body,
  );
}

const chipButtons = (root: HTMLElement | null) => [...(root?.querySelectorAll<HTMLButtonElement>("[data-chips] > button") ?? [])];

/** Arrow keys, Home and End move between the chips (they wrap), so a long list does not take a Tab per tag. */
function moveAmongChips(e: React.KeyboardEvent, root: HTMLElement | null) {
  const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
  if (step === undefined && e.key !== "Home" && e.key !== "End") return;
  const chips = chipButtons(root);
  const at = chips.indexOf(document.activeElement as HTMLButtonElement);
  if (at < 0 || !chips.length) return;
  e.preventDefault();
  const next = e.key === "Home" ? 0 : e.key === "End" ? chips.length - 1 : (at + step! + chips.length) % chips.length;
  chips[next].focus({ preventScroll: true });
}
