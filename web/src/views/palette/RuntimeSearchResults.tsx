import { useEffect, useLayoutEffect, useRef } from "react";
import type { RuntimeSearchSnapshot } from "../../lib/runtime-search";
import type { EvidenceSnippet } from "../../lib/agent-chatlist-filter";
import type { Route } from "../../lib/router";
import type { Session } from "../../lib/types";
import { Button } from "../../ui/atoms";
import { t } from "../../i18n";
import { runtimeSearchKey, type useRuntimeSearch } from "./RuntimeSearch";
export { RuntimeSearchModel } from "../../lib/runtime-search";

/** Keep fields a summary omits. Only the same version may donate longer display text to the authoritative result. */
export function mergeRuntimeSession(remote: Session, local?: Session): Session {
  if (!local) return remote;
  const sameVersion = remote.rev !== undefined && local.rev !== undefined ? remote.rev === local.rev
    : !!remote.updated_at && remote.updated_at === local.updated_at;
  const merged = sameVersion ? { ...local, ...remote } : { ...remote };
  if (sameVersion) {
    for (const field of ["title", "last_text", "activity"] as const) {
      const full = local[field], summary = remote[field];
      if (full && summary && full.length > summary.length && full.startsWith(summary.replace(/…$/, ""))) merged[field] = full;
    }
  }
  return merged;
}

/** Outside the listbox: these controls change the result window, they are not selectable search results. */
export function RuntimeSearchNotice({ search, onReturnFocus }: { search: ReturnType<typeof useRuntimeSearch>; onReturnFocus?: () => void }) {
  const controls = useRef<HTMLDivElement>(null);
  const pendingFocus = useRef<HTMLElement | null>(null);
  // Loading disables the initiating button, and completing the last page removes it. Remember ownership through
  // that transition, but retire it as soon as the user deliberately focuses or presses somewhere else.
  useEffect(() => {
    const changed = (event: Event) => {
      if (pendingFocus.current && event.target instanceof Node && !controls.current?.contains(event.target)) pendingFocus.current = null;
    };
    document.addEventListener("focusin", changed);
    document.addEventListener("pointerdown", changed);
    return () => { document.removeEventListener("focusin", changed); document.removeEventListener("pointerdown", changed); };
  }, []);
  useLayoutEffect(() => {
    const previous = pendingFocus.current;
    if (previous && !previous.isConnected && !search.loading && !search.loadingMore) {
      pendingFocus.current = null;
      if (document.activeElement === document.body) {
        // An expired cursor replaces More with Search again. Keep focus beside that explanation, rather than
        // returning to the combobox and scrolling the selected first result over the error.
        const retry = controls.current?.querySelector<HTMLButtonElement>('[data-testid="runtime-search-retry"]');
        if ((search.error || search.moreError) && retry && !retry.disabled) retry.focus({ preventScroll: true });
        else onReturnFocus?.();
      }
    }
  });
  const run = (action: () => void) => {
    pendingFocus.current = controls.current?.contains(document.activeElement) ? document.activeElement as HTMLElement : null;
    action();
  };
  if (!search.enabled && !search.fallback) return null;
  return <div ref={controls} data-runtime-search-status className="relative z-10 space-y-2 px-2.5 py-2 text-xs text-ink-3">
    {search.fallback && <p role="status">{t("runtime-search.loaded_only")}</p>}
    {search.loading && <p role="status">{t("runtime-search.searching")}</p>}
    {(search.error || search.moreError) && <div className="flex flex-wrap items-center gap-2">
      <p role="alert" className="min-w-0 flex-1 break-words text-red">{search.error || search.moreError}</p>
      {search.supported && search.retryable && <Button testId="runtime-search-retry" size="sm" disabled={search.loading || search.loadingMore} onClick={() => run(search.retry)}>{t(search.expired ? "runtime-search.search_again" : "runtime-search.retry")}</Button>}
    </div>}
    {!search.loading && !search.error && search.loaded && search.supported && search.enabled && <p role="status">{t("runtime-search.loaded_count", { count: search.hits.length, total: search.total })}</p>}
    {search.hasMore && !search.expired && !search.fallback && <Button size="sm" busy={search.loadingMore} disabled={search.loading || search.loadingMore} onClick={() => run(search.more)}>{t("runtime-search.more")}</Button>}
  </div>;
}


interface RuntimePaletteHit { key: string; s: Session; to: Route; score: number; evidence?: EvidenceSnippet }

/** Local matches arrive immediately. The first server page owns membership and scores, including matches
 * beyond summary truncation. Tuple identity is shared with the render keys before and after the local cache fills. */
export function mergeRuntimeHits(localHits: RuntimePaletteHit[], sessions: readonly Session[], search: RuntimeSearchSnapshot): { hits: RuntimePaletteHit[]; remoteKeys: Set<string> } {
  const confirmed = new Set(search.hits.map((hit) => runtimeSearchKey(hit.session)));
  const authoritative = search.loaded && search.supported;
  const hits = localHits.filter((hit) => !authoritative || confirmed.has(runtimeSearchKey(hit.s)));
  const local = new Map(sessions.map((session) => [runtimeSearchKey(session), session]));
  const positions = new Map(hits.map((hit, index) => [runtimeSearchKey(hit.s), index]));
  const remoteKeys = new Set<string>();
  for (const result of search.hits) {
    const identity = runtimeSearchKey(result.session);
    const s = mergeRuntimeSession(result.session, local.get(identity));
    const existing = positions.get(identity), key = `s:${identity}`;
    const hit: RuntimePaletteHit = { key, s, to: { name: "session", mid: s.machine_id, sid: s.session_id }, score: result.score, evidence: result.evidence };
    if (existing === undefined) { positions.set(identity, hits.length); hits.push(hit); } else hits[existing] = hit;
    remoteKeys.add(key);
  }
  return { hits, remoteKeys };
}
