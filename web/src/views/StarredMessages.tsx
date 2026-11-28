import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { t } from "../i18n";
import { conversationPage } from "../lib/conversation-page";
import { shortTime } from "../lib/format";
import { href, navigate, onLinkClick, type Route } from "../lib/router";
import { restoredStarKey } from "../lib/starred-backup";
import { positionalStar, type Star, useStarMark } from "../lib/starred-rows";
import { checkStarSource, reloadStarred, removeStar, useStarredList } from "../lib/starred";
import { Button, copyText, IconButton, Loading } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { Markdown } from "../ui/Markdown";
import { SearchField } from "../ui/SearchField";
import { lazyOverlay } from "./LazyOverlay";

// Loaded on first open so backup/restore logic stays off StarredMessages' initial chunk.
const StarredBackupDialog = lazyOverlay(() => import("./StarredBackupDialog").then((m) => m.StarredBackupDialog));

const PAGE_SIZE = 24;
const PREVIEW_CHARS = 420;
const PREVIEW_LINES = 6;

export function starRoute(star: Star): Route {
  const { machine_id: mid, session_id: sid, item_id: item } = star;
  const at = positionalStar(item) ? (star.source_fingerprint && `star:${star.source_fingerprint}:${item}`) || undefined : item;
  return star.conversation === "assistant" ? conversationPage(sid, mid, star.source_manager, at) : { name: "session", mid, sid, at, view: "chat" };
}

function starSpeaker(star: Star): string {
  return star.speaker || t(star.role === "user" ? "starred.you" : star.conversation === "agent" ? "starred.agent" : "starred.assistant");
}

function searchText(value: string): string {
  return value.normalize("NFKC").toLocaleLowerCase();
}

export function searchStars(stars: readonly Star[], query: string): readonly Star[] {
  const terms = searchText(query.trim()).split(/\s+/).filter(Boolean);
  if (!terms.length) return stars;
  return stars.filter((star) => {
    const value = searchText(`${star.text}\n${star.title}\n${starSpeaker(star)}`);
    return terms.every((term) => value.includes(term));
  });
}

function snapshotPreview(text: string): string {
  const lines = text.split("\n", PREVIEW_LINES + 1);
  const inView = lines.length > PREVIEW_LINES ? lines.slice(0, PREVIEW_LINES).join("\n") : text;
  return [...inView].slice(0, PREVIEW_CHARS).join("");
}

export function StarredMessages({ onToggleSidebar }: { onToggleSidebar?: () => void }) {
  const list = useStarredList();
  const [filter, setFilter] = useState("");
  const [limit, setLimit] = useState(PAGE_SIZE);
  const [retrying, setRetrying] = useState(false);
  const [backupOpen, setBackupOpen] = useState(false);
  const closeBackup = useCallback(() => setBackupOpen(false), []);
  const search = useRef<HTMLDivElement>(null);
  const found = useMemo(() => searchStars(list.stars, filter), [list.stars, filter]);
  const retry = async () => {
    setRetrying(true);
    try { await reloadStarred(); }
    finally { setRetrying(false); }
  };
  const afterRemove = useCallback(() => {
    requestAnimationFrame(() => {
      if (document.activeElement === document.body) search.current?.querySelector("input")?.focus();
    });
  }, []);
  return (
    <div className="flex h-full min-h-0 flex-col bg-page">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line bg-surface px-3 sm:px-4">
        {onToggleSidebar && <IconButton icon="sidebar" label={t("app.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />}
        <h1 className="min-w-0 flex-1 truncate text-base font-semibold text-ink">{t("starred.title")}</h1>
        {list.status === "ready" && <Button onClick={() => setBackupOpen(true)}>{t("starred.backup")}</Button>}
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-3xl flex-col gap-4 px-4 py-5 sm:px-6">
          {list.status === "loading" && !list.error && <Loading />}
          {list.status === "unsupported" && <Notice icon="star" title={t("starred.unsupported")} detail={t("starred.unsupported_hint")} />}
          {list.error && (
            <div role="alert" className="flex flex-wrap items-center justify-between gap-2 rounded-card border border-line bg-surface px-4 py-3">
              <p className="min-w-0 break-words text-sm text-ink-2">{list.error}</p>
              <Button icon="retry" onClick={() => void retry()} busy={retrying}>{t("starred.retry")}</Button>
            </div>
          )}
          {list.status === "ready" && (
            <>
              <div ref={search}>
                <SearchField size="md" value={filter} onChange={(text) => { setFilter(text); setLimit(PAGE_SIZE); }} placeholder={t("starred.search")} />
              </div>
              {!found.length ? (filter.trim()
                ? <Notice icon="search" title={t("starred.no_matches")} />
                : <Notice icon="star" title={t("starred.empty")} detail={t("starred.empty_hint")} />)
                : <ol aria-label={t("starred.title")} className="flex min-w-0 flex-col gap-3">
                    {found.slice(0, limit).map((star) => <StarCard key={star.id} star={star} canWrite={list.canWrite} afterRemove={afterRemove} />)}
                  </ol>}
              {found.length > limit && <div className="self-center"><Button onClick={() => setLimit((n) => n + PAGE_SIZE)}>{t("starred.more")}</Button></div>}
            </>
          )}
        </div>
      </div>
      {backupOpen && list.status === "ready" && <StarredBackupDialog stars={list.stars} canWrite={list.canWrite}
        speaker={starSpeaker} onClose={closeBackup} onRestored={() => void reloadStarred()} />}
    </div>
  );
}

function Notice({ icon, title, detail }: { icon: "star" | "search"; title: string; detail?: string }) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-12 text-center">
      <Icon name={icon} size={28} className="text-ink-3" />
      <h2 className="text-base font-semibold text-ink">{title}</h2>
      {detail && <p className="max-w-sm text-sm text-ink-2">{detail}</p>}
    </div>
  );
}

const StarCard = memo(function StarCard({ star, canWrite, afterRemove }: { star: Star; canWrite: boolean; afterRemove: () => void }) {
  const [expanded, setExpanded] = useState(false);
  const openRequest = useRef<symbol | undefined>(undefined);
  useEffect(() => () => { openRequest.current = undefined; }, []);
  const [sourceStatus, setSourceStatus] = useState<"checking" | "unavailable" | "failed">();
  const mark = useStarMark(star);
  const preview = useMemo(() => snapshotPreview(star.text), [star.text]);
  const title = star.title || t("starred.conversation");
  const speaker = starSpeaker(star);
  const source = starRoute(star);
  const positional = positionalStar(star.item_id);
  const unavailable = sourceStatus === "unavailable" || positional && !star.source_fingerprint;
  const openOriginal = (event: React.MouseEvent<HTMLAnchorElement>) => {
    if (!positional) {
      onLinkClick(event, source);
      return;
    }
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
    event.preventDefault();
    if (sourceStatus === "checking") return;
    const request = Symbol();
    openRequest.current = request;
    setSourceStatus("checking");
    void checkStarSource(star).then((result) => {
      if (openRequest.current !== request || result === "retired") return;
      openRequest.current = undefined;
      setSourceStatus(result === "verified" ? undefined : result);
      if (result === "verified") navigate(source);
    });
  };
  const date = star.written_at || star.starred_at;
  const restored = !!restoredStarKey(star);
  return (
    <li className="min-w-0 rounded-card border border-line bg-surface">
      <article className="flex min-w-0 flex-col gap-3 px-4 py-3">
        <header className="min-w-0">
          <h2 className="truncate text-sm font-semibold text-ink" title={title}>{title}</h2>
          <p className="mt-0.5 flex flex-wrap items-baseline gap-x-2 text-xs text-ink-2">
            <span className="break-words">{speaker}</span>
            <time dateTime={date}>{shortTime(date)}</time>
            {restored && <span>{t("starred.backup_origin")}</span>}
          </p>
        </header>
        <div className="min-w-0 break-words">
          <Markdown text={expanded ? star.text : preview} small compact lineBreaks askImages />
          {star.omitted_chars ? <p className="mt-2 text-xs text-ink-2">{t("starred.truncated", { count: star.omitted_chars })}</p> : null}
          {preview !== star.text && (
            <button type="button" onClick={() => setExpanded(!expanded)} aria-expanded={expanded} className="mt-1 rounded-control px-1 py-1 text-xs font-medium text-accent-ink hover:bg-hover">
              {t(expanded ? "starred.show_less" : "starred.show_snapshot")}
            </button>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          {!restored && !unavailable && <a href={href(source)} onClick={openOriginal} aria-busy={sourceStatus === "checking" || undefined}
            className="inline-flex min-h-11 pointer-fine:min-h-8 items-center gap-1.5 whitespace-nowrap rounded-control px-2.5 text-sm font-medium text-accent-ink hover:bg-hover">
            <Icon name="external" />{t(sourceStatus === "checking" ? "common.loading" : "starred.open")}
          </a>}
          <Button icon="copy" className="min-h-11 pointer-fine:min-h-8" onClick={() => void copyText(star.text)}>{t("starred.copy")}</Button>
          {canWrite && <Button icon="star" className="min-h-11 pointer-fine:min-h-8" busy={mark.pending === "remove"} onClick={() => void removeStar(star).then((removed) => { if (removed) afterRemove(); })}>{t("starred.unstar")}</Button>}
        </div>
        {!restored && (unavailable || sourceStatus === "failed") && <p role="status" className="text-xs text-ink-2">
          {t(unavailable ? "starred.source_unavailable" : "starred.source_failed")}
        </p>}
      </article>
    </li>
  );
});
