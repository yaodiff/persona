import { useEffect, useMemo, useSyncExternalStore } from "react";
import type { RuntimeSearchHit, RuntimeSearchSnapshot } from "../../lib/runtime-search";
import type { Session } from "../../lib/types";

const EMPTY: RuntimeSearchSnapshot = { query: "", hits: [], loaded: false, loading: false, loadingMore: false, error: "", moreError: "", hasMore: false, total: 0, expired: false, supported: true, retryable: true };
const empty = () => EMPTY;
const noSubscription = () => () => {};

/** A query's own bounded search, loaded only on intent. The model owns the 200 ms debounce and request cancellation. */
export function useRuntimeSearch({ open, jump, query, capable, bounded, module, loadError, owner }: {
  open: boolean; jump: boolean; query: string; capable: boolean; bounded: boolean;
  owner: string; module: typeof import("./SearchSupport") | null; loadError: string;
}) {
  const querying = open && jump && !!query.trim();
  const enabled = querying && capable;
  const model = useMemo(() => module && capable ? new module.RuntimeSearchModel() : null, [module, owner, capable]);
  const state = useSyncExternalStore(model?.subscribe ?? noSubscription, model?.snapshot ?? empty, model?.snapshot ?? empty);
  useEffect(() => {
    model?.search(enabled ? query : "");
    return () => { model?.search(""); };
  }, [model, enabled, query]);
  useEffect(() => () => model?.dispose(), [model]);
  const current = enabled && state.query === query.trim();
  const visible = current ? state : EMPTY;
  const error = enabled ? loadError || visible.error : "";
  const loading = enabled && !error && (!model || !current || visible.loading);
  const fallback = querying && bounded && (!capable || (current && !state.supported));
  return {
    ...visible, enabled, loading, error, fallback,
    Notice: module?.RuntimeSearchNotice, mergeHits: module?.mergeRuntimeHits,
    hits: current && state.supported ? state.hits : EMPTY.hits,
    retry: () => void model?.retry(),
    more: () => void model?.more(),
  };
}

/** Machine/session is a tuple, not a path: two identifiers containing a slash must not collide. */
export const runtimeSearchKey = (session: Session) => JSON.stringify([session.machine_id, session.session_id]);


export type { RuntimeSearchHit };
