import { conversationDeleted, onConversationDeleted } from "../../lib/conversation-deleted";
// A session's items: the newest page over HTTP, then live over the WebSocket (docs/protocol/protocol.md "Continuity of items").
import { startTransition, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { api, ApiError, errorText, isAbort, READ_TIMEOUT_MS, sessionHistoryNotFound, sessionPath } from "../../lib/api";
import { sessionLoadError } from "../../lib/session-link";
import { getState, listenSession, markConversationDeleted, send, type ItemsEvent } from "../../lib/store";
import { afterPull, afterPush, FRESH_TALLY, newestItemAt, notLive as tallyNotLive, RevWatch, shouldPull, type StaleTally } from "../../lib/stale-page";
import { insideFold, isUserInput, mergeItems, withoutFolded } from "../../lib/turns";
import { chosenManager } from "../../lib/manager-choice";
import { discardFeedCache, discardMemoryPage, feedCacheScope, readFeedCache, readMemoryPage, writeFeedCache, writeMemoryPage, type FeedCache } from "../../lib/feed-cache";
import type { Item, ItemsPage } from "../../lib/types";
import { createTextThrottle, type TextThrottle } from "../../ui/stream/throttle";
import { unlandedLive } from "../../lib/live-text";
import { clearLive } from "../../lib/live";
import { endPull, pullKey as gateKeyOf, resetPullGatesForTests, startPull } from "../../lib/pull-gate";
import { archivedNow } from "../../lib/agent-archives";
import { createItemCoalescer, type ItemCoalescer } from "../../lib/item-coalescer";

const PAGE = "turns=2&fold_work=1&lazy_images=1";
/** How often a page checks whether it is behind (a pure comparison; nothing is asked unless it is, lib/stale-page.ts). */
const STALE_TICK_MS = 2000;
/** When each session was last asked for what its stream may have lost: kept across remounts, so reopening a page does not ask again at once. */
const lastPulls = new Map<string, number>();
/** After an older page fails, loadEarlier waits this long before asking again (scrolling at the top calls it on every tick), doubling up to the cap. */
const EARLIER_RETRY_MS = 3000;
const EARLIER_RETRY_MAX_MS = 30_000;
/** Coalesce live push cache writes so WS bursts do not serialize the main-chat cache on every frame. */
export const FEED_CACHE_SAVE_MS = 1000;
/** Batches at or above this size commit in a React transition so large backlog frames do not block input. */
const LARGE_ITEMS_BATCH_THRESHOLD = 100;

// Independent newest-page reads still share one commit order across views of a session. Otherwise an older response
// can land after a newer one and roll its own view and the shared page cache back to a stale snapshot.
interface PageReadHub {
  committed: number;
  listeners: Set<(page: ItemsPage) => void>;
}
const pageReadHubs = new Map<string, PageReadHub>();
let nextPageReadTicket = 0;

function listenPageReads(key: string, listener: (page: ItemsPage) => void): () => void {
  let hub = pageReadHubs.get(key);
  if (!hub) {
    hub = { committed: 0, listeners: new Set() };
    pageReadHubs.set(key, hub);
  }
  hub.listeners.add(listener);
  return () => {
    hub.listeners.delete(listener);
    if (!hub.listeners.size && pageReadHubs.get(key) === hub) pageReadHubs.delete(key);
  };
}

function publishPageRead(key: string, ticket: number, page: ItemsPage): void {
  const hub = pageReadHubs.get(key);
  if (!hub || hub.committed > ticket) return;
  hub.committed = ticket;
  for (const listener of [...hub.listeners]) listener(page);
}

function pageReadSuperseded(key: string, ticket: number): boolean {
  return (pageReadHubs.get(key)?.committed ?? 0) > ticket;
}

interface EarlierRead {
  controller: AbortController;
  promise: Promise<symbol | undefined>;
  page?: symbol;
}

export function waitForEarlierRetry(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const settle = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", settle);
      resolve();
    };
    const timer = window.setTimeout(settle, ms);
    signal.addEventListener("abort", settle, { once: true });
  });
}

/** The first page refused because reading it would start the agent: one its engine let go (Paseo's closed and archived
 *  agents), which only a watched chat reads (docs/integrations/paseo.md "Content", #8132). The subscribe that follows
 *  loads it, and its backlog brings the page. */
const isNotLoaded = (e: unknown) => e instanceof ApiError && e.code === "not_loaded";

/** Tests only: forget the asks, as a fresh page load does (each sync case starts its clock at the same moment again). */
export function resetStalePullsForTests() {
  lastPulls.clear();
  resetPullGatesForTests();
}

export interface Transcript {
  items: Item[];
  /** Identity of the currently loaded history, independent of how many earlier pages are visible. */
  history?: string;
  /** Monotonic counter bumped whenever a full-page replacement commits (e.g. history rewrite or gap-fill fallback). */
  replaceEpoch?: number;
  loading: boolean;
  /** The hub's first page came or failed: until then a cached page says nothing about what went in since. */
  firstRead: boolean;
  error: string;
  hasMore: boolean;
  loadingEarlier: boolean;
  /** Identifies the last older page applied to items. */
  earlierPage?: symbol;
  /** Returns the page's identity if older items were queued. Navigation may wait out a failed read's backoff. */
  loadEarlier: (options?: { waitForRetry: boolean }) => Promise<symbol | undefined>;
  expandFold: (fold: Item) => Promise<boolean | void>;
  /** The reply as it is written, before the transcript has it: the headless assistant's, or an agent's as Claude shows it
   *  (a Claude session Persona started; docs/integrations/claude-integration.md "Live replies"). "" for none. */
  streaming: string;
  /** The stream ended while its final transcript item is still on the way. */
  streamEnded?: boolean;
  /** Transcript cursor when the reply preview arrived. A newer user echo belongs to a different turn. */
  streamEndedThrough?: number;
  /** The stream keeps losing items (the page got them by asking): say so, with a reload. */
  notLive: boolean;
  /** A user-triggered reload is currently in flight. */
  reloading?: boolean;
  reload: () => void;
}

/** One delta: the reply so far is throttled; "" (the turn's end) goes out at once and drops what was pending. */
export function applyDelta(t: TextThrottle, text: string): void {
  if (text) t.push(text);
  else t.flush("");
}

export interface TranscriptCache {
  scope: string;
  manager?: string;
}

/** Without `cache` (any chat but the main conversation) the page comes from this tab's memory. */
function cachedPage(mid: string, sid: string, cache?: TranscriptCache): FeedCache | null {
  if (!cache) return readMemoryPage(feedCacheScope(), mid, sid);
  const page = readFeedCache(cache.scope);
  return page?.machineID === mid && page.sessionID === sid && (page.manager ?? "") === (cache.manager ?? "") ? page : null;
}

/** `updatedAt` and `rev`: the session's `updated_at` and `rev` from the list (a subagent's `updated_at` from its parent's;
 *  it has no rev), what the stale-page check compares; the hub's `revs` event moves the rev too.
 *  `assistant`: a Persona conversation, whose live text is the headless assistant's whole reply so far (any reply item
 *  ends it); otherwise it is an agent's message as Claude shows it, which the transcript replaces by its start. */
export function useTranscript(mid: string, sid: string, opts: { starting?: boolean; updatedAt?: string; rev?: number; cache?: TranscriptCache; assistant?: boolean; readOnly?: boolean } = {}): Transcript {
  const [initial] = useState(() => cachedPage(mid, sid, opts.cache));
  const [items, setItems] = useState<Item[]>(initial?.items ?? []);
  const [loading, setLoading] = useState(!initial);
  const [firstRead, setFirstRead] = useState(false);
  const [error, setError] = useState("");
  // The latest page's read is unanswered or said its computer can't find this history (#11450): no older page is read,
  // which would answer an empty one, end paging and take the error away.
  const historyMissing = useRef(false);
  const [hasMore, setHasMore] = useState(initial?.hasMore ?? false);
  const hasMoreRef = useRef(hasMore);
  const updateHasMore = useCallback((next: boolean) => {
    hasMoreRef.current = next;
    setHasMore(next);
  }, []);
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [earlierPage, setEarlierPage] = useState<symbol>();
  const [replaceEpoch, setReplaceEpoch] = useState(0);
  const [streaming, setStreaming] = useState("");
  const [streamEndedThrough, setStreamEndedThrough] = useState<number>();
  const reread = useRef<() => void>(() => {});
  const replacing = useRef(false);
  const [notLive, setNotLive] = useState(false);
  const [reloading, setReloading] = useState(false);
  const cacheScope = opts.cache?.scope;
  const cacheManager = opts.cache?.manager ?? "";
  const memoryScope = cacheScope ? null : feedCacheScope();
  // This is only the latest page and its live additions, never older pages or expanded folds.
  const snapshot = useRef<{ scope: string; page: FeedCache } | null>(initial && cacheScope ? { scope: cacheScope, page: initial } : null);
  const updatedAt = useRef(opts.updatedAt);
  updatedAt.current = opts.updatedAt;
  const rev = useRef(opts.rev);
  rev.current = opts.rev;
  const revWatch = useRef<RevWatch | null>(null); // the shown conversation's, for the list's rev as it changes
  const covered = useRef(0); // the last transcript line we have without a gap
  const history = useRef("");
  const itemsRef = useRef<Item[]>([]);
  itemsRef.current = items;
  const gapTimer = useRef<number | undefined>(undefined);
  // Bumped whenever the shown conversation starts over (another session, or the file was rewritten): an older page or a
  // fold asked for before that is dropped when it lands, so it never merges into the conversation shown by then.
  const generation = useRef(0);
  const earlierInFlight = useRef<EarlierRead | null>(null);
  const expanded = useRef(new Map<string, Item["fold"]>());
  const rewrite = useRef<(history?: string) => void>(() => {}); // the effect's cache invalidation, shared with older-page reads below
  // The back-off after a failed older page: when loadEarlier may ask again, and how long the last wait was.
  const earlierRetryAt = useRef(0);
  const earlierWait = useRef(0);
  // The live text (the whole message so far: the assistant's about every 80 ms, an agent's a batch of lines at a time;
  // "" when it ends) re-renders the chat at most once per throttle interval (ui/stream/throttle.ts), the last one never
  // dropped; the end and the reply's item at once. Only the newest items touch it: an older page or an opened fold is
  // never the reply being written.
  const deltas = useRef<TextThrottle>(null!);
  deltas.current ??= createTextThrottle(setStreaming);
  const assistant = Boolean(opts.assistant);
  // Read-only audit views page over HTTP but never subscribe/unsubscribe or otherwise act as the visible chat.
  const readOnly = opts.readOnly === true;
  // an agent's last delta as it came, and what of it the page shows (lib/live-text.ts)
  const liveRaw = useRef("");
  const liveShown = useRef("");
  const coalescer = useRef<ItemCoalescer | null>(null);

  const apply = useCallback((batch: Item[]) => {
    if (!batch.length) return;
    if (batch.length >= LARGE_ITEMS_BATCH_THRESHOLD) {
      startTransition(() => {
        setItems((have) => mergeItems(have, batch));
      });
      return;
    }
    setItems((have) => mergeItems(have, batch));
  }, []);

  const invalidateHistoryReads = useCallback(() => {
    generation.current++;
    expanded.current.clear();
    const earlier = earlierInFlight.current;
    earlierInFlight.current = null;
    earlier?.controller.abort();
    earlierRetryAt.current = 0;
    earlierWait.current = 0;
  }, []);

  useLayoutEffect(() => {
    // A caller arriving after HTTP completes but before these items commit still joins that page.
    if (earlierPage && earlierInFlight.current?.page === earlierPage) earlierInFlight.current = null;
  }, [earlierPage]);

  useLayoutEffect(() => {
    let dead = false;
    let pageRequest = 0;
    let subscribed = false;
    let replacementReplay: ItemsEvent | undefined;
    let pendingDelta: string | undefined;
    let pendingEnd = false;
    let handoffTimer: number | undefined;
    let savePending = false;
    let saveTimer: number | undefined;
    let saveIdle: number | undefined;
    const cancelSave = () => {
      savePending = false;
      if (saveTimer !== undefined) {
        clearTimeout(saveTimer);
        saveTimer = undefined;
      }
      if (saveIdle !== undefined) {
        window.cancelIdleCallback?.(saveIdle);
        saveIdle = undefined;
      }
    };
    replacing.current = false;
    const ctl = new AbortController();
    const itemCoalescer = createItemCoalescer(apply);
    coalescer.current = itemCoalescer;
    // Older pages and folds still on their way were asked for from what is shown now: they must not land on what replaces it.
    const disownReads = () => {
      invalidateHistoryReads();
      setEarlierPage(undefined);
      setLoadingEarlier(false);
    };
    const offDeleted = onConversationDeleted((machine, session) => {
      if (machine !== mid || session !== sid) return;
      dead = true;
      cancelSave();
      clearTimeout(handoffTimer);
      ctl.abort();
      itemCoalescer.cancel();
      if (coalescer.current === itemCoalescer) coalescer.current = null;
      disownReads();
      snapshot.current = null;
      setItems([]);
      setStreaming("");
      deltas.current.cancel();
      if (!readOnly) send({ type: "unsubscribe", machine_id: mid, session_id: sid });
    });
    disownReads();
    const kept = snapshot.current;
    const cached = cacheScope && kept?.scope === cacheScope && kept.page.machineID === mid && kept.page.sessionID === sid && (kept.page.manager ?? "") === cacheManager
      ? kept.page : cachedPage(mid, sid, opts.cache);
    setItems(cached?.items ?? []);
    setLoading(!cached);
    setFirstRead(false);
    setError("");
    historyMissing.current = false;
    updateHasMore(cached?.hasMore ?? false);
    setStreamEndedThrough(undefined);
    deltas.current.flush("");
    liveRaw.current = liveShown.current = "";
    covered.current = 0;
    history.current = cached?.history ?? "";
    setNotLive(false);
    setReloading(false);
    let tally: StaleTally = FRESH_TALLY;
    let lastArrival = Date.now();
    let visibleSince = 0;
    let reloadInFlight: Promise<void> | null = null;
    let ready = false; // the newest read has settled
    let authoritative = false; // a newest page was actually received for this history
    let pulling = false;
    let checkedFor: string | undefined;
    // Starts from the rev the first read below covers: a move heard while that read is on its way is asked about.
    const revs = new RevWatch(rev.current);
    revWatch.current = revs;
    const pullKey = `${mid}/${sid}`;
    let latest = cached?.items ?? [];
    let latestMore = cached?.hasMore ?? false;
    let pendingItems: Item[] = []; // pushes after the HTTP request started win over its answer
    let pendingHistory = "";
    // The first read was refused as not_loaded (isNotLoaded): the chat keeps loading until the subscribe's items come,
    // instead of showing the refusal for the second or three the load takes (#8321). It shows the refusal, with Retry,
    // only when none came within a read's time (this timer): past the hub's own limits for a backlog (the watch's, then
    // the read's), so a backlog the hub could not read shows then too.
    let refused: number | undefined;
    const endRefused = (error = "") => {
      clearTimeout(refused);
      refused = undefined;
      setError(error);
      setLoading(false);
      setFirstRead(true);
    };
    const recovered = () => {
      historyMissing.current = false;
      setError("");
    };
    // The newest page may start after everything the cached one holds, with turns missing between them: showing both
    // would hide that hole, so the page takes over and older history loads on scroll.
    const skipsCached = (page: Item[]) => page.length > 0 && page[0].seq > (cached?.items.at(-1)?.seq ?? Infinity);
    const save = () => {
      if (!cacheScope) {
        writeMemoryPage(memoryScope, { machineID: mid, sessionID: sid, items: latest, hasMore: latestMore, history: history.current || undefined });
        return;
      }
      if ((chosenManager() ?? "") !== cacheManager) return;
      // The store may have accepted a new main before React unmounts this chat. Its old request must not
      // recreate the discarded cache in that interval, even though the credential scope is unchanged.
      const assistant = getState().assistant;
      if (assistant && (assistant.main !== sid || assistant.machine_id !== mid)) return;
      const page: FeedCache = { machineID: mid, sessionID: sid, manager: cacheManager, items: latest, hasMore: latestMore, history: history.current || undefined };
      snapshot.current = { scope: cacheScope, page };
      writeFeedCache(cacheScope, page);
    };
    const flushSave = () => {
      const pending = savePending;
      cancelSave();
      if (!pending || dead || replacing.current || !ready) return;
      save();
    };
    const scheduleSave = () => {
      savePending = true;
      if (saveTimer !== undefined || saveIdle !== undefined) return;
      saveTimer = window.setTimeout(() => {
        saveTimer = undefined;
        if (!savePending || dead || replacing.current || !ready) return;
        if (typeof window.requestIdleCallback === "function") {
          saveIdle = window.requestIdleCallback(() => {
            saveIdle = undefined;
            flushSave();
          }, { timeout: FEED_CACHE_SAVE_MS });
        } else {
          flushSave();
        }
      }, FEED_CACHE_SAVE_MS);
    };
    const remember = (batch: Item[], gap = false) => {
      if (!ready) pendingItems = mergeItems(pendingItems, batch);
      latest = mergeItems(latest, batch);
      if (gap) cancelSave();
      else if (ready) scheduleSave(); // a page with a hole is not kept: the fill saves once it is closed
    };
    // Keep this session's last committed view on screen while the new history is read. Old answers to
    // page, fold and gap reads cannot join it. The socket keeps collecting pushes for the replacement.
    const rewritten = (nextHistory = "") => {
      disownReads();
      cancelSave();
      itemCoalescer.cancel();
      replacing.current = true;
      authoritative = false;
      ready = false;
      covered.current = 0;
      latest = [];
      pendingItems = [];
      pendingHistory = nextHistory;
      replacementReplay = undefined;
      pendingDelta = undefined;
      pendingEnd = false;
      clearTimeout(gapTimer.current);
      gapTimer.current = undefined;
      if (cacheScope) discardFeedCache(cacheScope);
      else discardMemoryPage(memoryScope, mid, sid);
      snapshot.current = null;
      void readPage();
    };
    rewrite.current = rewritten;

    // The live text against the newest items, after they changed (`batch`) or a delta came: the assistant's ends at
    // its reply's item; an agent's shows what of its message the latest one does not hold (lib/live-text.ts).
    const endLive = () => {
      clearTimeout(handoffTimer);
      handoffTimer = undefined;
      liveRaw.current = liveShown.current = "";
      deltas.current.flush("");
      setStreamEndedThrough(undefined);
    };
    const showLive = (batch?: Item[]) => {
      // A preview without an input can claim a late echo. Once bound, the next input supersedes it,
      // even when the previous turn's end marker has not arrived yet.
      const input = liveRaw.current && batch?.findLast((i) => i.seq > liveThrough && !i.queued && isUserInput(i));
      if (input) {
        const message = input.id.split("#")[0];
        if (message !== liveInput && (liveInput !== undefined || handoffTimer !== undefined)) return endLive();
        liveInput = message;
        liveThrough = input.seq;
        if (handoffTimer !== undefined) setStreamEndedThrough(liveThrough);
      }
      if (assistant) {
        if (batch?.some((i) => i.role === "assistant" && i.kind === "text" && i.seq >= liveThrough)) endLive();
        return;
      }
      const want = liveRaw.current && unlandedLive(liveRaw.current, latest);
      if (!batch && liveRaw.current && want !== liveRaw.current) itemCoalescer.flush();
      if (!want && liveRaw.current) return endLive();
      if (want === liveShown.current) return;
      liveShown.current = want;
      if (batch) deltas.current.flush(want);
      else deltas.current.push(want);
    };
    let liveThrough = 0;
    let liveInput: string | undefined;
    const onDelta = (text: string) => {
      if (dead) return;
      if (replacing.current) {
        if (text) pendingDelta = text;
        pendingEnd = !text;
        return;
      }
      lastArrival = Date.now();
      if (text && tally !== FRESH_TALLY) {
        tally = afterPush(tally);
        setNotLive(false);
      }
      if (!text) {
        if (!liveRaw.current || handoffTimer !== undefined) return;
        // The end marker and saved words travel separately. If a run ended without an item, release its preview.
        setStreamEndedThrough(liveThrough);
        deltas.current.flush(liveShown.current);
        handoffTimer = window.setTimeout(endLive, READ_TIMEOUT_MS);
        return;
      }
      if (!liveRaw.current || handoffTimer !== undefined) {
        liveThrough = Math.max(covered.current, latest.at(-1)?.seq ?? 0);
        const boundary = latest.findLast((item) => !item.queued &&
          (isUserInput(item) || (item.role === "assistant" && item.kind === "text")));
        liveInput = boundary && isUserInput(boundary) ? boundary.id.split("#")[0] : undefined;
      }
      clearTimeout(handoffTimer);
      handoffTimer = undefined;
      setStreamEndedThrough(undefined);
      liveRaw.current = text;
      if (assistant) {
        liveShown.current = text;
        applyDelta(deltas.current, text);
      } else showLive();
    };

    const subscribe = () => {
      if (readOnly || dead || conversationDeleted(mid, sid)) return;
      send({ type: "subscribe", machine_id: mid, session_id: sid, since_seq: covered.current || undefined, lazy_images: true, fold_work: true });
      subscribed = true;
    };

    // Items after what we have, a page at a time (past 8 the newest page is cheaper); how many of them were new.
    const pull = async (): Promise<number> => {
      const version = pageRequest;
      const sent = revs.sending();
      let fresh = 0;
      for (let n = 0; n < 8 && !dead; n++) {
        const p = await api<ItemsPage>(`${sessionPath(mid, sid)}/items?after_seq=${covered.current}&lazy_images=1`, { signal: ctl.signal, dedupe: false });
        if (dead || version !== pageRequest) return fresh;
        if (p.history && history.current && p.history !== history.current) {
          rewritten(p.history);
          return fresh;
        }
        const got = p.items ?? [];
        // Not what the stream brought while this was on its way or already delivered past a gap: not a miss.
        const knownIds = new Set(latest.map((i) => i.id));
        fresh += got.filter((i) => i.seq > covered.current && !knownIds.has(i.id)).length;
        if (got.length) {
          itemCoalescer.flush();
          apply(got);
        }
        remember(got, Boolean(p.more_after));
        showLive(got);
        if (p.to_seq) covered.current = Math.max(covered.current, p.to_seq);
        lastArrival = Date.now();
        if (!p.more_after) {
          revs.answered(sent);
          return fresh;
        }
      }
      if (!dead && version === pageRequest) rewritten(history.current);
      return fresh;
    };
    // Every automatic read of what came after goes through the session's gate (lib/pull-gate.ts): one at a time across
    // this tab's views of the session, and after a failure the next waits longer (#13677).
    const gateKey = gateKeyOf("after", mid, sid);
    const gatedPull = (): Promise<number> | undefined => {
      if (startPull(gateKey, Date.now()) > 0) return undefined;
      return pull().then(
        (fresh) => { endPull(gateKey, "ok", Date.now()); return fresh; },
        (e: unknown) => { endPull(gateKey, isAbort(e) ? "aborted" : "failed", Date.now()); throw e; },
      );
    };
    const queueGapFill = () => {
      // Schedule once per gap so frequent pushes cannot postpone the fill.
      gapTimer.current ??= window.setTimeout(() => {
        gapTimer.current = undefined;
        void gatedPull()?.catch(() => {});
      }, 1500);
    };
    const keepExpanded = (batch: Item[]) => batch.filter(i => {
      const open = expanded.current.get(i.id);
      return !open || !i.fold || open.through_seq !== i.fold.through_seq || open.through_id !== i.fold.through_id;
    });

    // The safety net (lib/stale-page.ts): the list says the transcript grew past what we hold and no push came. Not while
    // a refused first read waits for the backlog: that read is the one asked for, and the runtime refuses this one too.
    const checkStale = () => {
      if (dead || !ready || refused || replacing.current) return;
      const now = Date.now();
      const asked = updatedAt.current;
      if (!shouldPull({
        updatedAt: asked, lastItemAt: newestItemAt(itemsRef.current), revUnread: revs.unread, revMovedAt: revs.movedAt, lastArrival,
        visibleSince, lastPull: lastPulls.get(pullKey) ?? 0, checkedFor, now, visible: document.visibilityState === "visible", pulling,
      })) return;
      // An archived session gets no new lines: nothing to catch up on, and on a slow link every ask costs (#13677).
      if (archivedNow(mid, sid)) return;
      const asking = gatedPull();
      if (!asking) return; // another view of it is asking, or the last ask failed and the wait is not over
      lastPulls.set(pullKey, now);
      pulling = true;
      asking
        .then((fresh) => {
          checkedFor = asked;
          tally = afterPull(tally, fresh > 0 ? "found" : "empty", Date.now());
        })
        .catch((e) => {
          if (!isAbort(e)) tally = afterPull(tally, "failed", Date.now());
        })
        .finally(() => {
          pulling = false;
          // The next ask is timed from when this one ended: a slow ask (29.5 s) no longer lets the next go at once.
          lastPulls.set(pullKey, Date.now());
          if (!dead) setNotLive(tallyNotLive(tally));
        });
    };
    const staleTimer = window.setInterval(checkStale, STALE_TICK_MS);
    const onVisibility = () => {
      if (document.visibilityState === "hidden") flushSave();
      else visibleSince = Date.now();
    };
    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener("pagehide", flushSave);

    const onItems = (e: ItemsEvent) => {
      if (dead) return;
      const known = replacing.current ? pendingHistory : history.current;
      if (e.history && known && e.history !== known) rewritten(e.history);
      if (replacing.current) {
        if (e.history) pendingHistory = e.history;
        pendingItems = mergeItems(pendingItems, e.items ?? []);
        if (e.has_more !== undefined) replacementReplay = e;
        if (ready && replacementReplay) {
          // A failed/refused GET may recover from the authoritative newest page on the socket.
          commitPage(replacementReplay);
          endRefused();
        }
        return;
      }
      if (e.history) history.current = e.history;
      if (!ready && e.history) pendingHistory = e.history;
      if (refused) endRefused(); // the watch brings the chat its first read could not
      const batch = e.items ?? [];
      let visible = batch;
      const replay = e.has_more !== undefined && authoritative;
      if (e.has_more !== undefined) {
        latestMore = e.has_more;
        recovered();
        // The first replay can stand in for a failed read. Later replays describe only the newest two turns.
        const skips = !replay && skipsCached(batch);
        if (skips) itemCoalescer.cancel();
        else itemCoalescer.flush();
        if (!replay) updateHasMore(latestMore);
        if (skips) disownReads();
        latest = skips ? [] : withoutFolded(latest, batch);
        if (replay) visible = keepExpanded(batch);
        authoritative = true;
        const folds = visible;
        setItems(have => skips ? [] : withoutFolded(have, folds));
      }
      const gap = covered.current > 0 && ((e.from_seq ?? covered.current) > covered.current || (replay && batch.length > 0 && batch[0].seq > covered.current + 1));
      remember(e.items ?? [], gap);
      const prevLiveShown = liveShown.current;
      showLive(e.items ?? []);
      const liveHandoff = liveShown.current !== prevLiveShown;
      lastArrival = Date.now();
      if (e.items?.length && tally !== FRESH_TALLY) {
        tally = afterPush(tally); // the stream works
        setNotLive(false);
      }
      itemCoalescer.push(visible, e.has_more !== undefined || liveHandoff);
      if (!gap && e.to_seq) covered.current = Math.max(covered.current, e.to_seq);
      if (gap || e.more_after) queueGapFill();
    };

    const off = listenSession(mid, sid, {
      items: onItems,
      delta: onDelta,
      rev: (r) => {
        if (!dead) revs.heard(r, Date.now());
      },
      // Every hello (the first one too) resubscribes from what the chat has. Before the first page is in that is
      // nothing: a subscribe without since_seq has the hub read and send that whole page again, while the read
      // below is on its way (a page load opens the socket and reads at once). The read subscribes when it is done.
      watchCursor: () => subscribed && !dead ? covered.current : undefined,
      reconnected: () => {
        if (ready || replacing.current) {
          subscribe();
          // A shorter, rewritten file may have no rows after our old cursor.
          if (ready) void readPage(false);
        }
      },
    });

    function commitPage(p: ItemsPage) {
      const batch = p.items ?? [];
      const changed = Boolean(p.history && history.current && p.history !== history.current);
      const active = authoritative && !replacing.current && !changed;
      const replace = replacing.current || changed || !batch.length || (!active && skipsCached(batch));
      const buffered = replace ? (itemCoalescer.cancel(), []) : itemCoalescer.drain();
      const gap = active && !replace && batch[0].seq > covered.current + 1;
      if (replace) disownReads();
      if (replacing.current || changed) setReplaceEpoch((n) => n + 1);
      const pushes = changed && !replacing.current && pendingHistory !== p.history ? [] : pendingItems;
      const refreshed = (have: Item[], page: Item[]) => mergeItems(mergeItems(replace ? [] : withoutFolded(have, page), page), withoutFolded(pushes, page));
      const visible = active ? keepExpanded(batch) : batch;
      setItems(have => refreshed(buffered.length ? mergeItems(have, buffered) : have, visible));
      latest = refreshed(latest, batch);
      latestMore = Boolean(p.has_more);
      if (replace || !active) updateHasMore(latestMore);
      history.current = p.history || pendingHistory || history.current;
      if (replacing.current || changed) endLive();
      replacing.current = false;
      authoritative = true;
      recovered();
      replacementReplay = undefined;
      pendingItems = [];
      pendingHistory = "";
      if (gap) {
        cancelSave();
        queueGapFill();
      } else {
        cancelSave();
        save();
        covered.current = Math.max(replace ? 0 : covered.current, p.to_seq ?? Math.max(0, ...batch.map((i) => i.seq)));
      }
      showLive(batch);
      if (pendingDelta) {
        onDelta(pendingDelta);
        if (pendingEnd) onDelta("");
      }
      pendingDelta = undefined;
      pendingEnd = false;
    }

    const pageReadKey = sessionPath(mid, sid);
    const offPageReads = listenPageReads(pageReadKey, (p) => {
      if (dead) return;
      if (pendingHistory && p.history && pendingHistory !== p.history) {
        rewritten(); // Take a fresh snapshot after both conflicting observations.
        return;
      }
      commitPage(p);
    });

    async function readPage(openWatch = true, options?: { forceResubscribe?: boolean }) {
      const before = history.current;
      const sent = revs.sending();
      const asked = updatedAt.current;
      const version = ++pageRequest;
      const ticket = ++nextPageReadTicket;
      ready = false;
      historyMissing.current = true;
      if (!itemsRef.current.length && !liveRaw.current) setLoading(true);
      clearTimeout(refused);
      refused = undefined;
      try {
        const p = await api<ItemsPage>(`${sessionPath(mid, sid)}/items?${PAGE}`, { signal: ctl.signal, dedupe: false });
        if (dead || version !== pageRequest) return;
        publishPageRead(pageReadKey, ticket, p);
        revs.answered(sent);
        checkedFor = asked;
        tally = FRESH_TALLY;
        setNotLive(false);
      } catch (e) {
        if (dead || version !== pageRequest) return;
        if (!pageReadSuperseded(pageReadKey, ticket)) {
          if (e instanceof ApiError && e.code === "conversation_deleted") { markConversationDeleted(mid, sid); return; }
          if (opts.starting) recovered();
          // A subscribe may already have supplied the page while the HTTP read was pending.
          if (replacing.current && replacementReplay) {
            commitPage(replacementReplay);
            endRefused();
          } else if (!opts.starting) {
            const text = sessionLoadError(errorText(e));
            historyMissing.current = sessionHistoryNotFound(e);
            if (isNotLoaded(e)) refused = window.setTimeout(() => endRefused(text), READ_TIMEOUT_MS);
            else setError(text);
          }
        }
      } finally {
        if (!dead && version === pageRequest) {
          if (!refused) { setLoading(false); setFirstRead(true); }
          lastArrival = Date.now();
          ready = true;
        }
      }
      if (!dead && version === pageRequest && (openWatch || before !== history.current)) {
        if (options?.forceResubscribe && subscribed && !readOnly && !conversationDeleted(mid, sid)) {
          send({ type: "unsubscribe", machine_id: mid, session_id: sid });
          subscribed = false;
        }
        subscribe();
      }
    }
    reread.current = () => {
      if (dead || reloadInFlight) return;
      setReloading(true);
      const run = readPage(true, { forceResubscribe: true }).finally(() => {
        if (reloadInFlight === run) reloadInFlight = null;
        if (!dead) setReloading(false);
      });
      reloadInFlight = run;
    };
    void readPage();

    return () => {
      flushSave();
      dead = true;
      invalidateHistoryReads();
      ctl.abort();
      offDeleted();
      off();
      offPageReads();
      clearTimeout(refused);
      clearTimeout(handoffTimer);
      itemCoalescer.cancel();
      if (coalescer.current === itemCoalescer) coalescer.current = null;
      deltas.current.cancel();
      clearTimeout(gapTimer.current);
      gapTimer.current = undefined; // the next session's first gap starts its own
      clearInterval(staleTimer);
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("pagehide", flushSave);
      clearLive(mid, sid);
      if (!readOnly) send({ type: "unsubscribe", machine_id: mid, session_id: sid });
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mid, sid, cacheScope, memoryScope, cacheManager, assistant, readOnly, invalidateHistoryReads, updateHasMore]);

  // After the effect above, which starts each conversation's watch from the rev it has then.
  useEffect(() => {
    revWatch.current?.heard(opts.rev, Date.now());
  }, [opts.rev]);

  const loadEarlier = useCallback<Transcript["loadEarlier"]>((options) => {
    if (replacing.current || conversationDeleted(mid, sid)) return Promise.resolve(undefined);
    if (earlierInFlight.current) return earlierInFlight.current.promise;
    const delay = earlierRetryAt.current - Date.now();
    if (!itemsRef.current.length || !hasMoreRef.current || historyMissing.current || (delay > 0 && !options?.waitForRetry)) return Promise.resolve(undefined);
    const read: EarlierRead = { controller: new AbortController(), promise: Promise.resolve(undefined) };
    earlierInFlight.current = read;
    setLoadingEarlier(true);
    read.promise = (async () => {
      const { signal } = read.controller;
      const current = () => earlierInFlight.current === read && !signal.aborted && !historyMissing.current;
      try {
        if (delay > 0) await waitForEarlierRetry(delay, signal);
        if (!current() || !hasMoreRef.current) return;
        const first = itemsRef.current[0];
        if (!first) return;
        const p = await api<ItemsPage>(`${sessionPath(mid, sid)}/items?${PAGE}&before_seq=${first.seq}`, { signal });
        if (!current()) return;
        if (p.history && history.current && p.history !== history.current) {
          rewrite.current(p.history);
          return;
        }
        const older = p.items ?? [];
        if (older.length) {
          coalescer.current?.flush();
          apply(older);
        }
        updateHasMore(Boolean(p.has_more));
        setError("");
        earlierRetryAt.current = 0;
        earlierWait.current = 0;
        if (p.items?.some((item) => item.seq < first.seq)) {
          read.page = Symbol();
          setEarlierPage(read.page);
          return read.page;
        }
      } catch (e) {
        if (!current()) return;
        // Kept in state for the caller to show; the next try waits instead of firing on every scroll tick.
        earlierWait.current = Math.min(earlierWait.current ? earlierWait.current * 2 : EARLIER_RETRY_MS, EARLIER_RETRY_MAX_MS);
        earlierRetryAt.current = Date.now() + earlierWait.current;
        setError(sessionLoadError(errorText(e)));
      } finally {
        if (earlierInFlight.current === read) {
          if (!read.page) earlierInFlight.current = null;
          setLoadingEarlier(false);
        }
      }
    })();
    return read.promise;
  }, [mid, sid, apply, updateHasMore]);

  const expandFold = useCallback(
    async (fold: Item): Promise<boolean> => {
      if (replacing.current) return false;
      if (!fold.fold) return true;
      const gen = generation.current;
      let from = fold.seq;
      const out: Item[] = [];
      let complete = false;
      for (let n = 0; n < 20; n++) {
        const p = await api<ItemsPage>(`${sessionPath(mid, sid)}/items?from_seq=${from}&through_seq=${fold.fold.through_seq}&lazy_images=1`);
        if (gen !== generation.current) return false;
        const batch = p.items ?? [];
        out.push(...batch);
        if (!p.more || !batch.length) {
          complete = !p.more;
          break;
        }
        from = batch[batch.length - 1].seq + 1;
      }
      // from_seq..through_seq is line-granular, so boundary lines can carry blocks of an adjacent fold (#13859).
      const expandedItems = out.filter((i) => insideFold(i, fold));
      const resolved = complete && expandedItems.length > 0;
      if (resolved) expanded.current.set(fold.id, fold.fold);
      if (expandedItems.length) {
        coalescer.current?.flush();
        apply(expandedItems);
      }
      // The closeout's evidence reader needs to know a capped or superseded read; existing fold UI keeps the partial steps.
      return resolved;
    },
    [mid, sid, apply],
  );

  return { items, history: history.current, replaceEpoch, loading, firstRead, error, hasMore, loadingEarlier, earlierPage,
    loadEarlier, expandFold, streaming, streamEnded: streamEndedThrough !== undefined, streamEndedThrough, notLive, reloading, reload: () => reread.current() };
}
