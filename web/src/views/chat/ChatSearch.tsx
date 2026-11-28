// "Search in chat" (the chat's ⋯ menu, Android ChatSearchScreen, parity N26): searches what is loaded of the chat (words,
// quoted contexts, thoughts, tool names, tool input and output, system events; the work the hub folded too, fetched while
// the panel is open), the hits by category with the match marked; picking one closes the panel and goes to it (ChatView).
// "Load earlier and keep searching" pages back through the history, up to 20 pages a go.
import { useEffect, useMemo, useRef, useState } from "react";
import type { ChatSearchHit, ChatSearchResults, SearchCategory } from "../../lib/chat-search";
import { groupHits, searchChat, snippetParts } from "../../lib/chat-search-match";
import type { Item } from "../../lib/types";
import { t } from "../../i18n";
import { Button, Dialog, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Empty } from "../assistant/parts";
import { composing } from "../../lib/send-history";

/** The wait after the last key before searching (Android's 200 ms). */
const SETTLE_MS = 200;

const CATEGORY: Record<SearchCategory, string> = {
  text: "chat-chatsearch.category_text",
  quoted: "chat-chatsearch.category_quoted",
  thinking: "chat-chatsearch.category_thinking",
  tool_name: "chat-chatsearch.category_tool_name",
  tool_input: "chat-chatsearch.category_tool_input",
  tool_output: "chat-chatsearch.category_tool_output",
  event: "chat-chatsearch.category_event",
};

const NO_RESULTS: ChatSearchResults = { hits: [], truncated: false };
const NO_ITEMS: Item[] = [];

/** How a run of "Load earlier" ended: null reached the start; a string says why it stopped. */
export type LoadOutcome = string | null;

export interface ChatSearchProps {
  open: boolean;
  query: string;
  onQuery: (q: string) => void;
  /** Loaded chat items searched by default when controlled `results` are not provided. */
  items?: Item[];
  /** The folded work is being fetched for the search (useFoldSearch). */
  foldsLoading?: boolean;
  /** Some folded work is not searched (its fetch failed, or past the budget). */
  foldsPartial?: boolean;
  /** Exchanges loaded so far. */
  rounds: number;
  hasMore: boolean;
  /** Pages back (up to 20) until the start, an error or `stop()`; `onPage` counts the pages in. */
  onLoadEarlier?: (onPage: (n: number) => void, stopped: () => boolean) => Promise<LoadOutcome> | void;
  onPick: (hit: ChatSearchHit) => void;
  onClose: () => void;
  /** Controlled search results skip the default `searchChat` pass over `items`. */
  results?: ChatSearchResults;
  /** Controlled search-in-progress state (defaults to debounce state, or false when `results` are controlled). */
  searching?: boolean;
  /** Custom matcher replacing the default `searchChat` when `results` are not controlled. */
  searchItems?: (items: Item[], query: string) => ChatSearchResults;
  /** Debounce delay in ms before running local matching (default 200). */
  settleMs?: number;
  /** Controlled earlier-loading state, page count, status notice and stop callback. */
  loadingEarlier?: boolean;
  loadedPages?: number;
  notice?: string | null;
  onStopLoadEarlier?: () => void;
}

export function ChatSearch({
  open,
  query,
  onQuery,
  items = NO_ITEMS,
  foldsLoading = false,
  foldsPartial = false,
  rounds,
  hasMore,
  onLoadEarlier,
  onPick,
  onClose,
  results: controlledResults,
  searching: controlledSearching,
  searchItems,
  settleMs = SETTLE_MS,
  loadingEarlier: controlledLoadingEarlier,
  loadedPages: controlledLoadedPages,
  notice: controlledNotice,
  onStopLoadEarlier,
}: ChatSearchProps) {
  const hasControlledResults = controlledResults !== undefined;
  const [settledQuery, setSettledQuery] = useState(query);
  useEffect(() => {
    if (hasControlledResults || settleMs <= 0) return;
    const id = window.setTimeout(() => setSettledQuery(query), settleMs);
    return () => clearTimeout(id);
  }, [query, hasControlledResults, settleMs]);
  const effectiveApplied = settleMs <= 0 ? query : settledQuery;
  // A controlled result object belongs to the query that was current when that object first arrived. Keeping the
  // object as the only dependency lets a parent update query and results together, while a query-only update hides
  // the old hits until the parent supplies a new object.
  const controlledResultQuery = useMemo(
    () => controlledResults === undefined ? undefined : query,
    [controlledResults], // eslint-disable-line react-hooks/exhaustive-deps -- the result object, not query, starts a new result set
  );
  const controlledResultsStale = hasControlledResults && controlledResultQuery !== query;
  const searching = hasControlledResults
    ? query.trim() !== "" && (Boolean(controlledSearching) || controlledResultsStale)
    : controlledSearching ?? effectiveApplied !== query;
  const activeQuery = hasControlledResults ? query : effectiveApplied;
  // only while the panel shows: the query stays after a pick, and the chat keeps changing under it. A controlled
  // result set or matcher replaces searchChat but keeps the same debounce path.
  const results = useMemo(() => {
    if (!open) return NO_RESULTS;
    if (hasControlledResults) return controlledResultsStale ? NO_RESULTS : controlledResults;
    return (searchItems ?? searchChat)(items, effectiveApplied);
  }, [open, items, effectiveApplied, hasControlledResults, controlledResults, controlledResultsStale, searchItems]);
  const groups = useMemo(() => groupHits(results.hits), [results.hits]);

  const [internalLoading, setInternalLoading] = useState(false);
  const [internalPages, setInternalPages] = useState(0);
  const [internalNotice, setInternalNotice] = useState<string | null>(null);
  const loadingEarlier = controlledLoadingEarlier ?? internalLoading;
  const loadedPages = controlledLoadedPages ?? internalPages;
  const notice = controlledNotice !== undefined ? controlledNotice : internalNotice;
  const stop = useRef(false);
  const loadRun = useRef(0);
  const wasOpen = useRef(open);
  // the panel closing stops a run going
  useEffect(() => {
    const closed = wasOpen.current && !open;
    wasOpen.current = open;
    if (!open) {
      stop.current = true;
      loadRun.current++;
      if (closed && loadingEarlier) onStopLoadEarlier?.();
      setInternalLoading(false);
      setInternalPages(0);
      setInternalNotice(null);
    }
  }, [open, loadingEarlier, onStopLoadEarlier]);
  useEffect(() => () => {
    stop.current = true;
    loadRun.current++;
  }, []);
  const stopLoading = () => {
    stop.current = true;
    onStopLoadEarlier?.();
  };
  const loadEarlier = async () => {
    if (!onLoadEarlier) return;
    const run = ++loadRun.current;
    stop.current = false;
    setInternalLoading(true);
    setInternalPages(0);
    setInternalNotice(null);
    try {
      const outcome = await onLoadEarlier(
        (n) => {
          if (loadRun.current === run) setInternalPages(n);
        },
        () => stop.current || loadRun.current !== run,
      );
      if (loadRun.current === run && outcome !== undefined) setInternalNotice(outcome);
    } catch {
      if (loadRun.current === run) setInternalNotice(t("chat-chatview.search_load_failed"));
    } finally {
      if (loadRun.current === run) setInternalLoading(false);
    }
  };

  const count = `${results.hits.length}${results.truncated ? "+" : ""}`;
  const footer = (
    <div className="flex w-full min-w-0 items-center gap-3">
      <span role="status" className="min-w-0 flex-1 truncate text-[12px] text-ink-3">
        {loadingEarlier ? t("chat-chatsearch.loading_pages", { count: loadedPages }) : notice ?? (foldsLoading ? t("chat-chatsearch.searching_folds") : foldsPartial ? t("chat-chatsearch.folds_partial") : hasMore ? "" : t("chat-chatsearch.at_start"))}
      </span>
      {loadingEarlier ? (
        <Button tone="quiet" onClick={stopLoading}>
          {t("chat-chatsearch.stop_loading")}
        </Button>
      ) : (
        hasMore && onLoadEarlier && (
          <Button onClick={() => void loadEarlier()}>
            {t("chat-chatsearch.load_earlier")}
          </Button>
        )
      )}
    </div>
  );

  return (
    <Dialog open={open} onClose={onClose} title={t("chat-chatsearch.title")} width={640} footer={footer}>
      <div className="flex flex-col gap-2" data-chat-search>
        <label className="flex min-w-0 items-center gap-2 rounded-control bg-field px-3 py-2">
          <Icon name="search" size={14} className="shrink-0 text-ink-3" />
          <input
            autoFocus
            value={query}
            onChange={(e) => onQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key !== "Enter" || composing(e.nativeEvent)) return;
              const first = results.hits[0];
              if (first && !searching) {
                e.preventDefault();
                onPick(first);
              }
            }}
            placeholder={t("chat-chatsearch.placeholder")}
            aria-label={t("chat-chatsearch.placeholder")}
            className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
          />
          {searching && <Spinner size={12} />}
        </label>
        <div className="flex flex-col gap-0.5 px-1 text-[12px] text-ink-3">
          <span>{t("chat-chatsearch.summary", { hits: count, rounds })}</span>
          {results.truncated && <span>{t("chat-chatsearch.truncated")}</span>}
        </div>
        <div className="-mx-1 max-h-[52vh] overflow-y-auto px-1 max-sm:max-h-none" role="list">
          {!searching && results.hits.length === 0 ? (
            <Empty icon="search" title={activeQuery.trim() ? t("chat-chatsearch.no_hits") : t("chat-chatsearch.enter_keyword")}>
              {t("chat-chatsearch.scope")}
            </Empty>
          ) : (
            groups.map(([category, hits]) => (
              <section key={category} className="flex flex-col gap-1 pb-3">
                <h3 className="px-1 pt-1 text-[11.5px] font-semibold text-ink-3">{t(CATEGORY[category])}</h3>
                {hits.map((hit, i) => (
                  <button
                    key={`${hit.itemId}/${i}`}
                    type="button"
                    role="listitem"
                    onClick={() => onPick(hit)}
                    className="flex w-full min-w-0 items-start gap-2 rounded-control px-2 py-2 text-left transition-colors hover:bg-hover"
                    data-search-result={category}
                  >
                    <Icon name="search" size={13} className="mt-[3px] shrink-0 text-ink-3" />
                    <span className="line-clamp-3 min-w-0 text-[13px] leading-relaxed text-ink [overflow-wrap:anywhere]">
                      {snippetParts(hit).map(([text, match], k) => (match ? <mark key={k} className="rounded-[3px] bg-accent-tint text-ink">{text}</mark> : <span key={k}>{text}</span>))}
                    </span>
                  </button>
                ))}
              </section>
            ))
          )}
          <p className="px-1 py-2 text-[11.5px] leading-relaxed text-ink-3">{t("chat-chatsearch.footnote")}</p>
        </div>
      </div>
    </Dialog>
  );
}
