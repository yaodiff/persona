// Search in chat's folded work (#6281, docs/design/proposals/assistant-search.md section 8): while the panel is open, the
// steps of the folds loaded in the chat are fetched (lib/chat-search.ts fetchFoldsForSearch, the pages a fold's own
// opening reads) and searched with the rest. Kept apart from the transcript: putting them in it would open the folds
// (useTranscript's apply), which only going to a hit does. Kept while the chat stays the same (session and history);
// a later go fetches only the folds not in yet, within what is left of the budget.
import { useEffect, useMemo, useRef, useState } from "react";
import { api, sessionPath } from "../../lib/api";
import { fetchFoldsForSearch, FOLD_SEARCH_BUDGET, foldItemsSize } from "../../lib/chat-search";
import type { Item, ItemsPage } from "../../lib/types";

export interface FoldSearch {
  /** The fetched folds' steps. */
  items: Item[];
  /** Each fetched fold and its steps, by the fold's id. */
  fetched: ReadonlyMap<string, { fold: Item; items: Item[] }>;
  /** Folds are being fetched. */
  loading: boolean;
  /** Some folds are not searched: their fetch failed, or the budget ran out. */
  partial: boolean;
}

interface Cache {
  key: string;
  fetched: Map<string, { fold: Item; items: Item[] }>;
  spent: number;
  partial: boolean;
}

const NO_ITEMS: Item[] = [];

/** A fold's steps, page by page (useTranscript's expandFold reads the same). */
async function loadFold(mid: string, sid: string, fold: Item): Promise<Item[]> {
  const through = fold.fold!.through_seq;
  let from = fold.seq;
  const out: Item[] = [];
  for (let n = 0; n < 20; n++) {
    const p = await api<ItemsPage>(`${sessionPath(mid, sid)}/items?from_seq=${from}&through_seq=${through}&lazy_images=1`);
    out.push(...(p.items ?? []));
    if (!p.more || !p.items?.length) break;
    from = p.items[p.items.length - 1].seq + 1;
  }
  return out;
}

export function useFoldSearch(mid: string, sid: string, history: string | undefined, items: Item[], on: boolean): FoldSearch {
  const key = `${mid}/${sid}/${history ?? ""}`;
  const cache = useRef<Cache>({ key, fetched: new Map(), spent: 0, partial: false });
  if (cache.current.key !== key) cache.current = { key, fetched: new Map(), spent: 0, partial: false };
  const [version, setVersion] = useState(0);
  const [loading, setLoading] = useState(false);
  // a fold stays in the items until a step of its range comes in (mergeItems): the ones still there are unopened
  const folds = useMemo(() => items.filter((i) => i.kind === "fold" && i.fold), [items]);
  const foldsKey = on ? `${key}\n${folds.map((f) => `${f.id}@${f.fold!.through_seq}`).join("\n")}` : "";

  useEffect(() => {
    const own = cache.current;
    const wanted = foldsKey ? folds.filter((f) => {
      const seen = own.fetched.get(f.id);
      return !seen || seen.fold.fold!.through_seq !== f.fold!.through_seq || seen.fold.fold!.through_id !== f.fold!.through_id;
    }) : [];
    if (!wanted.length) return;
    let stop = false;
    setLoading(true);
    void fetchFoldsForSearch({
      folds: wanted,
      load: (f) => loadFold(mid, sid, f),
      stopped: () => stop || cache.current !== own,
      budget: FOLD_SEARCH_BUDGET - own.spent,
      onFold: (f, its) => {
        const before = own.fetched.get(f.id);
        own.fetched.set(f.id, { fold: f, items: its });
        own.spent = Math.max(0, own.spent + foldItemsSize(its) - foldItemsSize(before?.items ?? []));
        setVersion((v) => v + 1);
      },
    }).then((r) => {
      if (r.stopped) return;
      own.partial = r.failed > 0 || r.capped;
      setLoading(false);
      setVersion((v) => v + 1);
    });
    return () => {
      stop = true;
      setLoading(false);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- foldsKey says which folds and chat these are
  }, [foldsKey]);

  const fetched = cache.current.fetched;
  const flat = useMemo(() => (fetched.size ? [...fetched.values()].flatMap((f) => f.items) : NO_ITEMS),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- version counts each fold that came in
    [fetched, version]);
  return { items: flat, fetched, loading, partial: cache.current.partial };
}
