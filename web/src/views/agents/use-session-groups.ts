// Reads the groups a paged list is browsed by: the hub's catalog for projects and spaces where it has one (it counts
// each group's sessions that need the user too), the count maps otherwise, with one more read of those sessions while
// there are any.
import { useEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, errorText } from "../../lib/api";
import { filterQuery, type ListCounts, type ListFilters } from "../../lib/session-list";
import { catalogGroups, countGroups, validCatalog, type GroupDimension, type SessionCatalog, type SessionGroup, type TodoNames } from "../../lib/session-groups";
import { onShownHint } from "../../lib/shown-hints";
import { SESSION_LIST_METRICS } from "../../ui/list-metrics";

/** How soon a catalog whose computers were still answering is read again: a second after the first answer, then
 * half as long again each time up to ten seconds, for as long as it says so. A hub with many directories to ask takes
 * a while, and nothing else tells the page when it is done. */
const PENDING_RETRY = { first: 1000, grow: 1.5, most: 10_000 } as const;

const validCounts = (v: unknown): v is ListCounts => typeof v === "object" && v !== null &&
  ["state", "group", "project", "machine"].every((key) => typeof (v as Record<string, unknown>)[key] === "object" && (v as Record<string, unknown>)[key] !== null);

/** `path`'s answer, read now and after a hint, the last good one kept while a newer one loads. Hints that come faster
 * than `groupsRefresh` share one read at its end: the groups of a list change far less often than its rows. A catalog
 * that says it is pending is read again shortly. `path` null: nothing is read and nothing kept. */
function useHinted<T>(path: string | null, valid: (v: unknown) => v is T, event: string, pending?: (v: T) => boolean) {
  const [read, setRead] = useState<{ path: string; value?: T; error?: string } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const check = useRef({ valid, pending });
  check.current = { valid, pending };
  useEffect(() => {
    if (!path) { setRead(null); return; }
    let live = true;
    let sequence = 0;
    let retries = 0;
    let started = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const abort = new AbortController();
    const load = () => {
      const request = ++sequence;
      clearTimeout(timer);
      timer = undefined;
      started = Date.now();
      void api<unknown>(path, { signal: abort.signal }).then((value) => {
        if (!live || request !== sequence) return;
        // only a hub that says it has the answer is asked: anything else in its place is a broken answer
        if (!check.current.valid(value)) { setRead({ path, error: t("paged-agents.invalid_page") }); return; }
        setRead({ path, value });
        if (check.current.pending?.(value) && !timer) timer = setTimeout(load, Math.min(PENDING_RETRY.first * PENDING_RETRY.grow ** retries++, PENDING_RETRY.most));
      }, (e) => { if (live && request === sequence) setRead((was) => ({ path, value: was?.path === path ? was.value : undefined, error: errorText(e) })); });
    };
    load();
    const off = onShownHint(event, () => {
      retries = 0;
      const wait = started + SESSION_LIST_METRICS.groupsRefresh - Date.now();
      if (wait <= 0) load();
      else if (!timer) timer = setTimeout(load, wait); // one read for the hints of this stretch
    });
    return () => { live = false; clearTimeout(timer); abort.abort(); off(); };
  }, [path, event, attempt]);
  return { value: read?.value, error: read?.error, retry: () => setAttempt((n) => n + 1) };
}

export interface SessionGroupsQuery {
  dimension: GroupDimension | "";
  /** The hub names this collection's projects and spaces (`sessions_catalog`). */
  catalog: boolean;
  /** The collection's path up to its query: `/sessions?` or `/sessions?roots=1&`. */
  countsPath: string;
  /** The search, the provider, the state and, on a hub without the catalog, a space's computer: all but the group. */
  scope: ListFilters;
  /** The parent's counts of the scope without its state, and that read's query: used as they are when they fit. */
  facets: ListCounts | null;
  facetQuery: string;
  /** Why the parent's facets read failed ("" none), and its Retry: the groups' own error while they are those facets and
   *  none has come yet, so a failed read never leaves them loading for good. */
  facetsError?: string;
  retryFacets?: () => void;
  /** How many of the scope's sessions need the user, by the state chips' count; undefined while unknown. */
  waiting: number | undefined;
  event: "sessions_list" | "agents";
  /** By to-do: the to-dos' order and titles (the hub counts them by id). */
  todos?: TodoNames;
}

export interface SessionGroups {
  /** null until the first answer. */
  groups: SessionGroup[] | null;
  /** The sessions the groups hold together; null until known. */
  total: number | null;
  error: string;
  retry: () => void;
}

export function useSessionGroups({ dimension, catalog, countsPath, scope, facets, facetQuery, facetsError = "", retryFacets, waiting, event, todos }: SessionGroupsQuery): SessionGroups {
  const fromCatalog = catalog && (dimension === "project" || dimension === "group");
  const query = useMemo(() => filterQuery(scope), [scope]);
  // the groups' own counts are the parent's facets when no state narrows them: one read serves both
  const shared = !fromCatalog && query === facetQuery;
  const groupsPath = !dimension ? null : fromCatalog ? `${countsPath}${filterQuery(scope, { catalog: "1" })}` : shared ? null : `${countsPath}${query}`;
  // Which of them need the user. Narrowed to a state, a group's count already is of that state: no second number
  // beside it. The catalog says it with the groups; the count maps take a second read, made only while some do.
  const badge = Boolean(dimension) && dimension !== "state" && !scope.state;
  const needsScope = useMemo(() => ({ ...scope, state: "needs_you" }), [scope]);
  const needsPath = badge && !fromCatalog && (waiting ?? 0) > 0 ? `${countsPath}${filterQuery(needsScope)}` : null;

  const catalogRead = useHinted<SessionCatalog>(fromCatalog ? groupsPath : null, validCatalog, event, (c) => Boolean(c.pending));
  const countsRead = useHinted<ListCounts>(fromCatalog ? null : groupsPath, validCounts, event);
  const countsNeeds = useHinted<ListCounts>(needsPath, validCounts, event);

  return useMemo((): SessionGroups => {
    if (!dimension) return { groups: null, total: null, error: "", retry: () => {} };
    if (fromCatalog) {
      const c = catalogRead.value;
      const groups = c ? catalogGroups(c, dimension as "project" | "group").map((g) => (badge ? g : { ...g, needs: 0 })) : null;
      return { groups, total: c ? c.total : null, error: catalogRead.error ?? "", retry: catalogRead.retry };
    }
    const counts = shared ? facets : countsRead.value ?? null;
    const groups = counts ? countGroups(counts, dimension, { machine: scope.machine_id, needs: badge ? countsNeeds.value : null, todos }) : null;
    // from the facets: their failure while there are none (a later failed refresh keeps the last ones, as a read does)
    if (shared) return { groups, total: counts ? counts.total : null, error: counts ? "" : facetsError, retry: retryFacets ?? (() => {}) };
    return { groups, total: counts ? counts.total : null, error: countsRead.error ?? "", retry: countsRead.retry };
    // the retry functions are new on each render and do the same thing
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dimension, fromCatalog, shared, badge, facets, facetsError, scope, catalogRead.value, catalogRead.error, countsRead.value, countsRead.error, countsNeeds.value, todos]);
}
