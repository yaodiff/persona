// The earlier stretches of a conversation that moved between Claude Code and Codex (#5054, useEarlierStretches.ts),
// loaded only for a conversation that switched (off the first screen). It reads the sessions the row names, read only
// and lazily, so the chat reads as one conversation: nothing loads until the live stretch is whole (its own pages come
// from useTranscript, untouched); then each "load earlier" goes to the newest stretch not yet whole: its session's newest
// page, then its older pages (`before_seq`), as scrolling up loads a session's own. A stretch shows only once every
// later one is whole, so nothing ever moves in between what is on screen (lib/engine-stretches.ts). Each stretch is its
// own session's lines, built apart (no exchange crosses a switch; each later stretch opens with its switch divider),
// drawn above the live one. Their messages keep Copy; nothing goes to the composer, and nothing rewinds, branches or
// forwards from them: they belong to a session the conversation has left.
import { Fragment, memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api, errorText, sessionHistoryNotFound, sessionPath } from "../../lib/api";
import { planStretches, sameItems, splitStretches, stretchWindows, type StretchSource } from "../../lib/engine-stretches";
import { sessionLoadError } from "../../lib/session-link";
import { buildTaskIndex } from "../../lib/tasks";
import { chatLines, insideFold, mergeItems, type ChatLine } from "../../lib/turns";
import type { Item, ItemsPage } from "../../lib/types";
import { NO_WORKFLOW_CALLS } from "../../lib/workflows";
import { Button } from "../../ui/atoms";
import { t } from "../../i18n";
import { DayLine } from "../assistant/Tasks";
import { ChatActionsContext, useChatActions, type ChatActions } from "./chat-actions";
import { ChatContext, useChatContext, type ChatContextValue } from "./chat-context";
import { ChatLineView } from "./Lines";
import { dayRowKey, withDayDividers } from "./day-dividers";
import type { EarlierStretch, LiveTranscript, StretchesHost } from "./useEarlierStretches";
import { waitForEarlierRetry } from "./useTranscript";

const PAGE = "turns=2&fold_work=1&lazy_images=1";
/** After a page fails, loading waits this long before asking again (scrolling at the top asks on every tick), doubling up
 *  to the cap; once the wait is over the chat asks again by itself (`due`) while its top is in view. */
const RETRY_MS = 3000;
const RETRY_MAX_MS = 30_000;

type Offscreen = (line: ChatLine) => ((section: HTMLElement) => () => void) | undefined;

export interface EarlierPageRequest {
  mid: string;
  sid: string;
  beforeSeq?: number;
  signal: AbortSignal;
}

export interface EarlierFoldPageRequest {
  mid: string;
  sid: string;
  fromSeq: number;
  throughSeq: number;
}

export interface EarlierStretchLineProps {
  line: ChatLine;
  mid: string;
  sid: string;
  expandFold: EarlierStretch["expandFold"];
  offscreen?: (section: HTMLElement) => () => void;
}

export interface EarlierStretchesProps extends StretchesHost {
  offscreen?: Offscreen;
  /** Loads an earlier session page without calling the default hub API client. */
  loadPage?: (request: EarlierPageRequest) => Promise<ItemsPage>;
  /** Loads one fold-expansion page without calling the default hub API client. */
  loadFoldPage?: (request: EarlierFoldPageRequest) => Promise<ItemsPage>;
  /** Classifies a missing-history error that ends the stretch cleanly without retrying. */
  isHistoryNotFound?: (error: unknown) => boolean;
  /** Waits out retry backoff when `loadEarlier({ waitForRetry: true })` is called. */
  waitForRetry?: (ms: number, signal: AbortSignal) => Promise<void>;
  /** Replaces the default chat line view for each row rendered inside an earlier stretch. */
  renderLine?: (props: EarlierStretchLineProps) => ReactNode;
}

interface Store {
  items: Item[];
  hasMore: boolean;
  ready: boolean;
}

const NO_STORES: Record<string, Store> = {};
const NO_STRETCHES: EarlierStretch[] = [];
const NO_SUBAGENTS: never[] = [];

const defaultLoadPage = ({ mid, sid, beforeSeq, signal }: EarlierPageRequest) =>
  api<ItemsPage>(`${sessionPath(mid, sid)}/items?${PAGE}${beforeSeq !== undefined ? `&before_seq=${beforeSeq}` : ""}`, { signal });

const defaultLoadFoldPage = ({ mid, sid, fromSeq, throughSeq }: EarlierFoldPageRequest) =>
  api<ItemsPage>(`${sessionPath(mid, sid)}/items?from_seq=${fromSeq}&through_seq=${throughSeq}&lazy_images=1`);

/** `offscreen`: ChatView's (an exchange off screen skips drawing, SegmentView `offscreen`). */
export default memo(function EarlierStretches({
  mid,
  sid,
  scope,
  history,
  live,
  report,
  offscreen,
  loadPage,
  loadFoldPage,
  isHistoryNotFound,
  waitForRetry,
  renderLine,
}: EarlierStretchesProps) {
  const callbacks = useRef({ loadPage, loadFoldPage, isHistoryNotFound, waitForRetry });
  callbacks.current = { loadPage, loadFoldPage, isHistoryNotFound, waitForRetry };
  const windows = useMemo(() => stretchWindows(history, sid), [history, sid]);
  const [state, setState] = useState<{ scope: string; stores: Record<string, Store>; page?: symbol }>({ scope, stores: NO_STORES });
  const stores = state.scope === scope ? state.stores : NO_STORES;
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const gen = useRef(0);
  const inFlight = useRef<{ controller: AbortController; promise: Promise<symbol | undefined> } | null>(null);
  const retryAt = useRef(0);
  const wait = useRef(0);
  // a failed page's wait: when it is over, `due` moves and the chat asks again (ChatView, while its top is in view)
  const retryTimer = useRef<number | undefined>(undefined);
  const [due, setDue] = useState(0);
  const stopRetry = useCallback(() => {
    window.clearTimeout(retryTimer.current);
    retryTimer.current = undefined;
  }, []);
  useEffect(() => {
    gen.current++;
    inFlight.current = null;
    retryAt.current = 0;
    wait.current = 0;
    stopRetry();
    setLoading(false);
    setError("");
    return () => {
      inFlight.current?.controller.abort();
      inFlight.current = null;
      stopRetry();
    };
  }, [scope, stopRetry]);

  const liveSource = useMemo<StretchSource>(() => ({ items: live.items, hasMore: live.hasMore, ready: !live.loading }), [live.items, live.hasMore, live.loading]);
  const sourceOf = useCallback((s: string): StretchSource | undefined => (s === sid ? liveSource : stores[s]), [sid, liveSource, stores]);
  const plan = useMemo(() => planStretches(windows, sourceOf), [windows, sourceOf]);

  // Each session's items by stretch, kept the same arrays while they hold the same items. The live session's are split
  // only when it ran an earlier stretch too (A → B → A): a reply streaming into one that did not splits nothing again.
  const several = windows.filter((w) => w.sid === sid).length > 1;
  const liveSplitting = several ? live.items : undefined;
  const lastSplit = useRef(new Map<number, Item[]>());
  const split = useMemo(() => {
    const out = new Map<number, Item[]>();
    for (const s of new Set(windows.map((w) => w.sid))) {
      if (s === sid && !liveSplitting) continue;
      for (const [i, items] of splitStretches(s === sid ? liveSplitting! : (stores[s]?.items ?? []), windows, s)) {
        const was = lastSplit.current.get(i);
        out.set(i, sameItems(was, items) ? was! : items);
      }
    }
    return out;
  }, [windows, sid, stores, liveSplitting]);
  lastSplit.current = split;

  const expandOther = useCallback(
    async (s: string, fold: Item): Promise<boolean> => {
      if (!fold.fold) return true;
      const g = gen.current;
      let from = fold.seq;
      const out: Item[] = [];
      let complete = false;
      for (let n = 0; n < 20; n++) {
        const p = await (callbacks.current.loadFoldPage ?? defaultLoadFoldPage)({ mid, sid: s, fromSeq: from, throughSeq: fold.fold.through_seq });
        if (g !== gen.current) return false;
        const batch = p.items ?? [];
        out.push(...batch);
        if (!p.more || !batch.length) {
          complete = !p.more;
          break;
        }
        from = batch[batch.length - 1].seq + 1;
      }
      const scoped = out.filter((i) => insideFold(i, fold));
      if (scoped.length) {
        setState((st) => {
          const base = st.scope === scope ? st.stores : NO_STORES;
          const cur = base[s];
          return cur ? { scope, stores: { ...base, [s]: { ...cur, items: mergeItems(cur.items, scoped) } } } : st;
        });
      }
      return complete && scoped.length > 0;
    },
    [mid, scope],
  );
  // one function per session, the same while the conversation is (a stretch's lines keep their memo)
  const folds = useMemo(() => {
    const out = new Map<string, (fold: Item) => Promise<boolean>>();
    for (const w of windows) if (w.sid !== sid && !out.has(w.sid)) out.set(w.sid, (fold) => expandOther(w.sid, fold));
    return out;
  }, [windows, sid, expandOther]);

  const lastStretches = useRef<EarlierStretch[]>(NO_STRETCHES);
  const stretches = useMemo(() => {
    const next = plan.shown.map((i): EarlierStretch => {
      const w = windows[i];
      return { key: `${i}:${w.sid}`, index: i, sid: w.sid, agent: w.agent, items: split.get(i) ?? [], expandFold: w.sid === sid ? live.expandFold : folds.get(w.sid)! };
    });
    const was = lastStretches.current;
    const same = was.length === next.length && was.every((x, k) => x.key === next[k].key && x.items === next[k].items && x.expandFold === next[k].expandFold);
    return same ? was : next.length ? next : NO_STRETCHES;
  }, [plan, windows, split, sid, live.expandFold, folds]);
  lastStretches.current = stretches;

  // read when loading, so the callback stays the same across pushes
  const now = useRef({ plan, windows, stores, live, scope });
  now.current = { plan, windows, stores, live, scope };

  const loadEarlier = useCallback<LiveTranscript["loadEarlier"]>(async (options) => {
    if (inFlight.current) return inFlight.current.promise;
    const { plan: p, windows: ws, stores: have, live: tr, scope: at } = now.current;
    if (p.next === undefined) return;
    const w = ws[p.next];
    if (w.sid === sid) return tr.loadEarlier(options);
    const delay = retryAt.current - Date.now();
    if (delay > 0 && !options?.waitForRetry) return;
    const store = have[w.sid];
    const first = store?.ready ? store.items[0] : undefined;
    const controller = new AbortController();
    const read = { controller, promise: Promise.resolve<symbol | undefined>(undefined) };
    inFlight.current = read;
    setLoading(true);
    const g = gen.current;
    const current = () => inFlight.current === read && now.current.scope === at && !controller.signal.aborted;
    read.promise = (async () => {
      try {
        if (delay > 0) await (callbacks.current.waitForRetry ?? waitForEarlierRetry)(delay, controller.signal);
        if (!current()) return;
        const page = await (callbacks.current.loadPage ?? defaultLoadPage)({ mid, sid: w.sid, beforeSeq: first?.seq, signal: controller.signal });
        if (!current()) return;
        const got = page.items ?? [];
        const pageToken = got.some((item) => !first || item.seq < first.seq) ? Symbol() : undefined;
        setState((st) => {
          const base = st.scope === at ? st.stores : NO_STORES;
          const cur = base[w.sid];
          // an empty page ends it whatever it says: asking again from the same place would get the same
          return { scope: at, page: pageToken, stores: { ...base, [w.sid]: { items: mergeItems(cur?.items ?? [], got), hasMore: Boolean(page.has_more) && got.length > 0, ready: true } } };
        });
        setError("");
        retryAt.current = 0;
        wait.current = 0;
        stopRetry();
        return pageToken;
      } catch (e) {
        if (!current()) return;
        if ((callbacks.current.isHistoryNotFound ?? sessionHistoryNotFound)(e)) {
          // its computer can't find that stretch's history (#11450): the stretch ends there, as an empty page ended
          // it, and the older ones load; asking again would get the same. The hub's own 404 (a computer it does not
          // know) is retried as any failure.
          setState((st) => {
            const base = st.scope === at ? st.stores : NO_STORES;
            return { scope: at, page: undefined, stores: { ...base, [w.sid]: { items: base[w.sid]?.items ?? [], hasMore: false, ready: true } } };
          });
          setError("");
          retryAt.current = 0;
          wait.current = 0;
          stopRetry();
          return;
        }
        wait.current = Math.min(wait.current ? wait.current * 2 : RETRY_MS, RETRY_MAX_MS);
        retryAt.current = Date.now() + wait.current;
        setError(sessionLoadError(errorText(e)));
        stopRetry();
        retryTimer.current = window.setTimeout(() => {
          retryTimer.current = undefined;
          if (g === gen.current) setDue((n) => n + 1);
        }, wait.current);
      } finally {
        if (inFlight.current === read) {
          inFlight.current = null;
          setLoading(false);
        }
      }
    })();
    return read.promise;
  }, [mid, sid, stopRetry]);

  const retry = () => {
    retryAt.current = 0;
    wait.current = 0;
    stopRetry();
    setError("");
    void loadEarlier();
  };

  // what the chat around them reads (useEarlierStretches): before paint, so it keeps its place as they come in above
  const hasMore = plan.next !== undefined;
  // Use the days actually drawn in each stretch, so a folded-only day cannot suppress another divider.
  const stretchRows = useMemo(() => stretches.map((s) => {
    const lines = chatLines(s.items).map((l) => ({ ...l, id: `${s.key}/${l.id}` }));
    const days = withDayDividers(lines, Date.now(), { includeLeadingDay: true })
      .map(dayRowKey)
      .filter((d): d is string => d !== undefined);
    return { lines, days };
  }), [stretches]);
  const dayKeys = useMemo(() => [...new Set(stretchRows.flatMap((r) => r.days))], [stretchRows]);
  const projectedLivePage = live.earlierPage;
  const projectedOtherPage = state.scope === scope ? state.page : undefined;
  useLayoutEffect(() => {
    report(scope, { hasMore, loading, loadEarlier, stretches, dayKeys, due, projectedLivePage, projectedOtherPage });
  }, [report, scope, hasMore, loading, loadEarlier, stretches, dayKeys, due, projectedLivePage, projectedOtherPage]);

  // Days already drawn by older stretches.
  const skipByStretch = useMemo(() => {
    const seen = new Set<string>();
    return stretchRows.map((r) => {
      const skip = new Set(seen);
      for (const d of r.days) seen.add(d);
      return skip;
    });
  }, [stretchRows]);

  if (!stretches.length && !error) return null;
  return (
    <>
      {error && (
        <div role="alert" className="flex flex-col items-center gap-2 py-2 text-center" data-engine-stretch-error>
          <p className="text-[13px] text-ink-2 [overflow-wrap:anywhere]">{t("chat-engine.earlier_failed", { error })}</p>
          <Button onClick={retry} icon="retry">{t("chat-chatview.retry")}</Button>
        </div>
      )}
      {stretches.map((s, i) => <StretchLines key={s.key} mid={mid} stretch={s} lines={stretchRows[i].lines} skipDays={skipByStretch[i]} offscreen={offscreen} renderLine={renderLine} />)}
    </>
  );
});

const StretchLines = memo(function StretchLines({ mid, stretch, lines, skipDays, offscreen, renderLine }: { mid: string; stretch: EarlierStretch; lines: ChatLine[]; skipDays?: ReadonlySet<string>; offscreen?: Offscreen; renderLine?: (props: EarlierStretchLineProps) => ReactNode }) {
  const base = useChatContext();
  const actions = useChatActions();
  const { items, key, sid } = stretch;
  // a DayLine over the first line of each day inside this stretch, its first line included (#8160); dividers for days
  // an older stretch already covered are dropped, and day-only keys stay stable as its pages load
  const dayRows = useMemo(() => withDayDividers(lines, Date.now(), { includeLeadingDay: true }).filter((row) => {
    const d = dayRowKey(row);
    return d === undefined || !skipDays?.has(d);
  }), [lines, skipDays]);
  const tasks = useMemo(() => buildTaskIndex(items), [items]);
  const ctx = useMemo<ChatContextValue>(
    () => ({ tasks, subagents: NO_SUBAGENTS, scope: `${mid}/${sid}:${key}:`, workflowCalls: NO_WORKFLOW_CALLS, lineStyle: base.lineStyle }),
    [tasks, mid, sid, key, base.lineStyle],
  );
  const readOnly = useMemo<ChatActions | null>(
    () => (actions ? { ...actions, canCompose: false, forward: undefined, fork: undefined, rewind: undefined, images: () => items.filter((i) => i.kind === "image") } : null),
    [actions, items],
  );
  if (!lines.length) return null;
  return (
    <ChatContext.Provider value={ctx}>
      <ChatActionsContext.Provider value={readOnly}>
        <div className="contents" data-engine-stretch={sid} data-stretch-index={stretch.index} data-stretch-first={items[0]?.seq}>
          {dayRows.map((row) => row.kind === "day" ? (
            <div key={row.id} data-day-divider={row.id}><DayLine date={row.date} /></div>
          ) : renderLine ? (
            <Fragment key={row.line.id}>
              {renderLine({ line: row.line, mid, sid, expandFold: stretch.expandFold, offscreen: offscreen?.(row.line) })}
            </Fragment>
          ) : (
            <ChatLineView key={row.line.id} line={row.line} mid={mid} sid={sid} expandFold={stretch.expandFold} offscreen={offscreen?.(row.line)} />
          ))}
        </div>
      </ChatActionsContext.Provider>
    </ChatContext.Provider>
  );
});
