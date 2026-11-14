// Bounded collection views: filters/counts belong to the server; only the loaded prefix lives in React.
import { Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { t } from "../../i18n";
import { api, errorText } from "../../lib/api";
import { onShownHint } from "../../lib/shown-hints";
import { filterQuery, SessionList, type ListCounts, type ListFilters } from "../../lib/session-list";
import { groupMachines, holdsPlace, type GroupDimension, type SessionGroup, type SessionPlace, type TodoNames } from "../../lib/session-groups";
import { SESSION_LIST_METRICS, SESSION_LIST_STYLE } from "../../ui/list-metrics";
import { SearchField } from "../../ui/SearchField";
import { STATE_LABEL } from "../../lib/state-labels";
import type { AgentState } from "../../lib/types";
import type { FilterCounts } from "../../lib/agent-filter";
import { cx, Menu, sidebarFoldMotion, useMenu, useNow, type MenuItem } from "../../ui/atoms";
import { ListError } from "../../ui/ListError";
import { Icon } from "../../ui/icons";
import { SegmentedTrack } from "../../ui/Segmented";
import { StatusMark } from "../../ui/StatusMark";
import { shortPath } from "../../lib/format";
import { removeStoredValue, storedValue, storeValue } from "../../lib/compatibility";
import { ACTIVE_WINDOWS, activeWindow, splitByActivity, windowStart, type ActiveWindow } from "../../lib/session-activity";
import { useFolded } from "../../lib/use-folded";
import { nestUnderParents } from "../../lib/side-sessions";
import { sameValue } from "../../lib/stable-lines";
import { lazyPage } from "../../lib/lazy-page";
import { NeedsCount, ShowMoreRow } from "../SidebarSection";
import type { GroupedSessionsProps, GroupedSource } from "./GroupedSessions";
import { useSessionGroups } from "./use-session-groups";

// The list drawn by group is a chunk of its own, loaded when a Browse by is chosen (or was, last time): the default
// list, newest first, is what a first screen draws (src/startup-preload.ts FIRST_SCREEN_BUDGET).
const GroupedSessions = lazyPage(() => import("./GroupedSessions").then((m) => m.GroupedSessions)) as <T>(props: GroupedSessionsProps<T>) => ReactNode;

export interface PagedSessionListProps<T> {
  root: SessionList<T>;
  path: string;
  identity: (row: T) => string;
  compare: (a: T, b: T) => number;
  removeIdentity?: (id: string) => string;
  event: "sessions_list" | "agents";
  /** `within` and `group`: the group the row is listed under (Browse by a computer, project or space), so it need not
   *  repeat what the heading says and can say what the heading leaves open (which of a project's computers). */
  renderRow: (row: T, within?: GroupDimension, group?: SessionGroup) => ReactNode;
  personal?: boolean;
  label?: (dimension: string, value: string) => string;
  /** The hub names this collection's projects and spaces (`sessions_catalog`): a repository checked out on several
   *  computers is one project, a space is one computer's. Without it the groups are the count maps' paths and ids. */
  catalog?: boolean;
  /** Where a row is, as the hub groups by it: the group holding the open row opens by itself. */
  placeOf?: (row: T) => SessionPlace;
  /** "Active within" (#4236): a row's last activity in ms, the key `compare` orders by, newest first. Given, the list
   *  offers the window menu and folds the rows active before the window into one Earlier group at its end. */
  activeAt?: (row: T) => number;
  /** A row that needs the user, by the rule the server counts `state.needs_you` with: counted on the Earlier header. */
  needsYou?: (row: T) => boolean;
  /** The open row: the Earlier group opens by itself while it holds that row. */
  isOpen?: (row: T) => boolean;
  /** The identity of the row a row is drawn under (#5935, `parent_agent_id`): given, a row whose parent is loaded comes
   *  under it a level in, each group where its most recent member was (lib/side-sessions.ts). Rows and counts stay the
   *  server's; only the order of the loaded rows changes. */
  parentOf?: (row: T) => string | undefined;
  /** The sidebar's groupings (#6662): the caller chooses the dimension with its own bar, the complete list's, so both
   *  lists show the same control; this list draws that bar first, then the state chips and what ⌘K put on it, as the
   *  complete list does, with no value pill or search field of its own. A search lists its matches flat. `label` names
   *  the dimension. */
  grouping?: PagedGrouping;
  /** The words a failed read of the list's rows or groups is said in, given its reason (the reason alone by default): the
   *  sidebar's says it could not read the Agents list, as its complete list does. */
  failureText?: (reason: string) => string;
}

export interface PagedGrouping {
  dimension: GroupDimension; bar: ReactNode; label: string; todos?: TodoNames; quick?: PagedQuickFilters;
  /** The sections follow this scope's fold-all (useFolded). */ foldScope?: string;
  /** "Recently updated" (the title row's ⋯): the computers' and projects' sections by their newest session. */ byUpdate?: boolean;
  /** The words ⌘K's "Filter Agents list" put on the list (#6589), sent as the search: the sidebar's list has no field of
   *  its own, `chips` says what narrows it where the field was. */
  query?: string; chips?: ReactNode;
}

/** The complete list's quick filters on the paged list (`sessions_quick_filters`, #6662), in place of its state chips:
 *  what the chosen chip selects, what every read leaves out (hidden botmux), and the chips, drawn from the counts of
 *  the list without the chip (one read for every chip's number). The caller keeps the choice, the complete list's. */
export interface PagedQuickFilters { select: ListFilters; exclude: ListFilters; bar: (counts: FilterCounts | undefined) => ReactNode }

const NONE: ListFilters = {};
/** The keys a chip selects by: the scope its numbers are counted in leaves them out. */
const CHIP_KEYS = ["state", "waiting", "pane_source"] as const;

// The chosen "Active within" window, kept per list on this browser.
const windowKey = (personal: boolean) => personal ? "persona.personalActiveWindow" : "persona.agentActiveWindow";
const windowLabel = (w: ActiveWindow) => t(`paged-agents.window_${w || "any"}`);
// the Earlier header's place among the rows: a key no row identity has
const EARLIER_KEY = "\u0000earlier";
// The chosen Browse by, kept per list on this browser: how someone reads the list, like a grouping, not a one-off filter.
const browseKey = (personal: boolean) => personal ? "persona.personalBrowseBy" : "persona.agentBrowseBy";
const browseBy = (stored: string | null, personal: boolean): GroupDimension | "" =>
  stored === "group" || (!personal && (stored === "machine" || stored === "project")) ? stored : "";

/** A counts query in a stable key order, so the same scope reached through different filters compares equal. */
const countsQuery = (filters: ListFilters) => filterQuery(filters);

const sameText = (reason: string) => reason;

export function scrollParent(el: HTMLElement | null): HTMLElement | null {
  for (let p = el?.parentElement; p; p = p.parentElement) {
    if (/auto|scroll/.test(getComputedStyle(p).overflowY)) return p;
  }
  return null;
}

export function PagedSessionList<T>({ root, path, identity, compare, removeIdentity, event, renderRow, personal = false, label, catalog = false, placeOf, activeAt, needsYou, isOpen, parentOf, grouping, failureText = sameText }: PagedSessionListProps<T>) {
  const [query, setQuery] = useState("");
  const [typed, setTyped] = useState("");
  // the sidebar's groupings take ⌘K's words (#6589); every other list its own field's, once typing pauses
  const q = grouping ? grouping.query?.trim() ?? "" : typed;
  const [state, setState] = useState("");
  // Browse by: "" is the one list, newest first; a dimension is that list in groups, or one of them once picked
  const [ownDimension, setDimension] = useState<GroupDimension | "">(() => browseBy(storedValue(browseKey(personal)), personal));
  const dimension = grouping ? grouping.dimension : ownDimension;
  const [picked, setPicked] = useState<SessionGroup | null>(null);
  const [machine, setMachine] = useState("");
  // the caller's bar moved to another grouping: nothing of the last one stays picked
  const chosen = grouping?.dimension;
  useEffect(() => { setPicked(null); setMachine(""); }, [chosen]);
  const [provider, setProvider] = useState("");
  const [providerQuery, setProviderQuery] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => { setTyped(query.trim()); setProvider(providerQuery.trim()); }, SESSION_LIST_METRICS.searchDelay);
    return () => clearTimeout(timer);
  }, [query, providerQuery]);
  // the hub's catalog names projects and spaces; computers, and every group of a hub without it, come from the counts
  const cataloged = catalog && !personal && (dimension === "project" || dimension === "group");
  // a space id is one computer's: without the catalog a second pill picks the computer it is read on
  const spaceMachine = dimension === "group" && !cataloged ? machine : "";
  const value = picked?.key ?? "";
  // the picked group's own filter, the same object while it selects the same sessions
  const pickedFilters = useRef<ListFilters>({});
  if (!sameValue(pickedFilters.current, picked?.filters ?? {})) pickedFilters.current = picked?.filters ?? {};
  const groupFilters = pickedFilters.current;
  // the chosen chip: the complete list's quick filter where the caller has them, else a state
  const quick = grouping?.quick;
  const stateChip = useMemo((): ListFilters => (state ? { state } : NONE), [state]);
  const chip = quick ? quick.select : stateChip;
  const exclude = quick?.exclude ?? NONE;
  const filters: ListFilters = useMemo(() => ({
    ...(q ? { q } : {}), ...chip, ...(provider ? { provider } : {}),
    ...groupFilters,
    ...(spaceMachine ? { machine_id: spaceMachine } : {}),
    ...exclude,
  }), [q, chip, groupFilters, spaceMachine, provider, exclude]);
  const filtered = Object.keys(filters).length > 0;
  // a dimension with no group picked: the list is drawn as its groups, and the one flat list is not read; in the
  // sidebar's groupings a search lists its matches flat
  const sections = Boolean(dimension) && !picked && !(grouping && q);
  const summaryMode = root.isSummary();
  // a hub that folds child sessions (#5120): every read and count of this list leaves them out, as the root's do
  const rootsMode = root.isRoots();
  const countsPath = rootsMode ? `${path}?roots=1&` : `${path}?`;
  const list = useMemo(() => filtered ? new SessionList<T>(path, identity, compare, undefined, removeIdentity, { summary: summaryMode, roots: rootsMode, pageCounts: () => root.usesPageCounts() }) : root, [filtered, path, identity, compare, removeIdentity, summaryMode, rootsMode, root, filters]);
  const page = useSyncExternalStore(list.subscribe, list.getSnapshot, list.getSnapshot);
  const [facets, setFacets] = useState<ListCounts | null>(null);
  const [facetError, setFacetError] = useState("");
  // Retry on a failed scoped facets read: read it again now
  const [facetAttempt, setFacetAttempt] = useState(0);
  const [scopeCounts, setScopeCounts] = useState<ListCounts | null>(null);
  const [scopeError, setScopeError] = useState("");
  const scopeQuery = useMemo(() => {
    const scope = { ...filters };
    for (const key of CHIP_KEYS) delete scope[key];
    return countsQuery(scope);
  }, [filters]);
  const rootPage = useSyncExternalStore(root.subscribe, root.getSnapshot, root.getSnapshot);
  // Counts without state keep other state tabs reachable; machine scopes disambiguate computer-local groups.
  const facetFilters = useMemo(() => ({ ...(q ? { q } : {}), ...(provider ? { provider } : {}), ...(spaceMachine ? { machine_id: spaceMachine } : {}), ...exclude }), [q, provider, spaceMachine, exclude]);
  // Unscoped counts are the root's own: follow them. Scoped counts are read on their own, now and once per hint; they
  // do not follow the root's counts, which land for that same hint (a second read of the same counts every time).
  const facetScoped = Object.keys(facetFilters).length > 0;
  const facetQuery = useMemo(() => countsQuery(facetFilters), [facetFilters]);
  // With no state tab and no browsed value the filtered list's own per-hint counts read is the facet query: follow it.
  // Drawn as groups the filtered list is not read, so the facets are read on their own.
  const facetsFromList = filtered && !sections && countsQuery(filters) === facetQuery;
  useEffect(() => {
    if (facetScoped) return;
    setFacets(rootPage.counts);
    setFacetError("");
  }, [facetScoped, rootPage.counts]);
  useEffect(() => {
    // Keep the last counts while the list loads; a failed read shows as the list's own error.
    if (!facetsFromList) return;
    if (page.counts) setFacets(page.counts);
    setFacetError("");
  }, [facetsFromList, page.counts]);
  useEffect(() => {
    if (!facetScoped || facetsFromList) return;
    let live = true;
    let sequence = 0;
    const controller = new AbortController();
    const read = () => {
      const request = ++sequence;
      void api<ListCounts>(`${countsPath}${facetQuery}`, { signal: controller.signal }).then((counts) => {
        if (live && request === sequence && counts.state && counts.group && counts.project && counts.machine) { setFacets(counts); setFacetError(""); }
      }, (e) => { if (live && request === sequence) setFacetError(errorText(e)); });
    };
    read();
    const off = onShownHint(event, read);
    return () => { live = false; controller.abort(); off(); };
  }, [countsPath, event, facetScoped, facetsFromList, facetQuery, facetAttempt]);
  useEffect(() => {
    // Keep the last counts while a refresh or a new scope is loading, including a root rebaseline.
    if (scopeQuery !== "counts=1") return;
    if (rootPage.counts) setScopeCounts(rootPage.counts);
    setScopeError("");
  }, [scopeQuery, rootPage.counts]);
  // Without a browsed value the scope is the facets' query: one read serves both.
  const scopeFromFacets = scopeQuery !== "counts=1" && scopeQuery === facetQuery;
  useEffect(() => {
    if (!scopeFromFacets) return;
    if (facets) setScopeCounts(facets);
    setScopeError("");
  }, [scopeFromFacets, facets]);
  useEffect(() => {
    if (scopeQuery === "counts=1" || scopeFromFacets) return;
    let sequence = 0;
    const abort = new AbortController();
    const read = () => {
      const request = ++sequence;
      void api<ListCounts>(`${countsPath}${scopeQuery}`, { signal: abort.signal }).then((counts) => {
        if (request === sequence && counts.state) { setScopeCounts(counts); setScopeError(""); }
      }, (e) => { if (request === sequence) setScopeError(errorText(e)); });
    };
    read();
    const off = onShownHint(event, read);
    return () => { sequence++; abort.abort(); off(); };
  }, [scopeQuery, scopeFromFacets, countsPath, event]);
  useEffect(() => {
    // the root list is kept loaded either way (its counts are the unscoped facets); a filtered one only while shown
    if (filtered && sections) return;
    if (filtered) void list.setFilters(filters).catch(() => {});
    else if (!list.getSnapshot().loaded) void list.load().catch(() => {});
    const off = filtered ? onShownHint(event, () => list.invalidate()) : () => {};
    return () => { off(); if (filtered) list.reset(); };
  }, [list, filters, filtered, sections, event]);

  // The groups of the chosen dimension, for the value pill and the sections: everything that narrows the list but the
  // group itself. Their counts follow the state tab, so a group's number is the number of rows it lists.
  const groupScope: ListFilters = useMemo(() => ({ ...facetFilters, ...chip }), [facetFilters, chip]);
  const scopeNeeds = scopeCounts?.state?.needs_you;
  // the facets' own read when the groups are drawn from them: its failure is theirs (audit item 4: the root list's read
  // failing left the sections on "Loading…" for good)
  const facetsFailure = facetScoped ? facetError : rootPage.error ?? "";
  const retryFacets = useCallback(() => {
    if (facetScoped) setFacetAttempt((n) => n + 1);
    else void root.retry().catch(() => {});
  }, [facetScoped, root]);
  const grouped = useSessionGroups({ dimension, catalog: cataloged, countsPath, scope: groupScope, facets, facetQuery, facetsError: facetsFailure, retryFacets, waiting: sections ? scopeNeeds : 0, event, todos: grouping?.todos });
  // "Recently updated" as the complete list orders its computers and spaces (lib/agent-sort.ts): by their newest session,
  // as far as the root's newest page tells (the hub reads no group's time); the ones it has none of keep their order
  // after them, and the sessions on no space stay last. Its to-dos keep the To-dos page's order there too.
  const byUpdate = Boolean(grouping?.byUpdate) && (dimension === "machine" || dimension === "group");
  const allGroups = useMemo(() => {
    if (!byUpdate || !grouped.groups || !placeOf) return grouped.groups;
    const places = rootPage.rows.map(placeOf);
    const rank = (g: SessionGroup) => {
      if (g.rest) return places.length + 1;
      const at = places.findIndex((p) => holdsPlace(g, dimension as GroupDimension, p));
      return at < 0 ? places.length : at;
    };
    return grouped.groups.map((g) => ({ g, at: rank(g) })).sort((a, b) => a.at - b.at).map(({ g }) => g);
  }, [byUpdate, grouped.groups, placeOf, rootPage.rows, dimension]);
  // the picked group follows what the hub says of it now (a project gains a directory); gone from the scope, it stays picked
  useEffect(() => {
    if (!picked || !allGroups) return;
    const now = allGroups.find((g) => g.key === picked.key);
    if (now && !sameValue(now, picked)) setPicked(now);
  }, [allGroups, picked]);

  // A session started from another one comes under it (#5935): the loaded rows in that order, each group where its most
  // recent member was. Only the order changes: the rows, the total and every count stay the server's.
  const nesting = useMemo(() => parentOf ? nestUnderParents(page.rows, identity, parentOf) : null, [page.rows, identity, parentOf]);
  const rows = nesting?.rows ?? page.rows;
  // a group is as recent as its most recent member, so Earlier never splits one
  const groupActiveAt = useMemo(() => activeAt && nesting ? (row: T) => activeAt(nesting.lead.get(row) ?? row) : activeAt, [activeAt, nesting]);

  // "Active within" (#4236): the rows active before the window fold into Earlier, after the recent ones. The split
  // moves with the clock; the list itself, its total and its filters stay the server's.
  const [win, setWin] = useState<ActiveWindow>(() => activeAt ? activeWindow(storedValue(windowKey(personal))) : "");
  const chooseWindow = (w: ActiveWindow) => {
    setWin(w);
    if (w) storeValue(windowKey(personal), w);
    else removeStoredValue(windowKey(personal));
  };
  const clock = useNow(60_000, Boolean(activeAt && win));
  const start = activeAt ? windowStart(win, clock) : null;
  // the Needs you chip's number for this scope: the server's, by the same rule as the chip
  const needsYouTotal = chip.state ? (chip.state === "needs_you" ? page.total : 0) : chip.pane_source ? page.counts?.state?.needs_you : scopeCounts?.state?.needs_you;
  const split = groupActiveAt && start !== null
    ? splitByActivity(rows, groupActiveAt, start, { total: page.total, hasMore: page.hasMore, needsYou: needsYou ?? (() => false), needsYouTotal })
    : null;
  const earlier = split?.reached && split.earlier > 0 ? split : null;
  const [folded, toggleEarlier] = useFolded(personal ? "personal-sessions-earlier" : "sessions-earlier", true, {
    holds: Boolean(earlier && isOpen && rows.slice(earlier.recent).some(isOpen)),
  });
  // folded, the list stops at its header: no further pages are read until it opens
  const foldedAway = Boolean(earlier && folded);
  const hasMore = page.hasMore && !foldedAway;
  const itemCount = sections ? 0 : (foldedAway ? earlier!.recent : rows.length) + (earlier ? 1 : 0);
  // the Earlier header sits between the recent rows and the earlier ones; null is the header
  const rowAt = (i: number): T | null => !earlier ? rows[i] : i === earlier.recent ? null : rows[i > earlier.recent ? i - 1 : i];

  const container = useRef<HTMLDivElement>(null);
  const [margin, setMargin] = useState(0);
  const getScrollElement = useCallback(() => scrollParent(container.current), []);
  const virtual = useVirtualizer({
    count: itemCount,
    getScrollElement,
    estimateSize: () => SESSION_LIST_METRICS.estimate,
    getItemKey: (i) => { const row = rowAt(i); return row === null ? EARLIER_KEY : identity(row); },
    overscan: SESSION_LIST_METRICS.overscan,
    scrollMargin: margin,
  });
  useLayoutEffect(() => {
    const el = container.current;
    const parent = getScrollElement();
    if (!el || !parent) return;
    const measure = () => setMargin(el.getBoundingClientRect().top - parent.getBoundingClientRect().top + parent.scrollTop);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(parent);
    if (el.parentElement) ro.observe(el.parentElement);
    return () => ro.disconnect();
  }, [getScrollElement, page.loaded, dimension, sections, facetError]);
  const totalSize = virtual.getTotalSize();
  const scrolled = useRef(false);
  useEffect(() => {
    scrolled.current = false;
    const parent = getScrollElement();
    if (parent) parent.scrollTop = 0;
  }, [list, getScrollElement]);
  useEffect(() => {
    const parent = getScrollElement();
    if (!parent) return;
    const maybeMore = () => {
      if (!sections && scrolled.current && hasMore && !page.loading && !page.error &&
          parent.scrollTop + parent.clientHeight >= margin + totalSize - SESSION_LIST_METRICS.overscan * SESSION_LIST_METRICS.estimate) {
        void list.more().catch(() => {});
      }
    };
    const scroll = () => { scrolled.current = true; maybeMore(); };
    parent.addEventListener("scroll", scroll, { passive: true });
    maybeMore();
    return () => parent.removeEventListener("scroll", scroll);
  }, [list, getScrollElement, sections, hasMore, page.loading, page.error, margin, totalSize]);

  const stateTotals = scopeCounts?.state ?? {};
  // the quick filters' numbers, as the complete list counts them: Working without the ones waiting in the background
  const quickCounts = useMemo((): FilterCounts | undefined => {
    if (!scopeCounts) return undefined;
    const by = scopeCounts.state ?? {}, waiting = scopeCounts.background_waiting ?? 0, origin = scopeCounts.pane_source ?? {};
    return { needs: by.needs_you ?? 0, unread: by.unread ?? 0, working: Math.max(0, (by.working ?? 0) - waiting), bg_waiting: waiting, feishu: origin.feishu ?? 0, terminal: origin.terminal ?? 0 };
  }, [scopeCounts]);
  // the most used groups first: the menu lists a bounded number of them, and the picked one whatever its place
  const groups = useMemo(() => {
    const listed = (allGroups ?? []).slice(0, SESSION_LIST_METRICS.groupOptions);
    return picked && !listed.some((g) => g.key === picked.key) ? [...listed, allGroups?.find((g) => g.key === picked.key) ?? { ...picked, count: 0, needs: 0 }] : listed;
  }, [allGroups, picked]);
  const machines = useMemo(() => Object.entries(rootPage.counts?.machine ?? {}).filter(([id]) => id).sort(([a, x], [b, y]) => y - x || a.localeCompare(b)), [rootPage.counts]);
  const dimensions: { key: typeof dimension; label: string }[] = personal
    ? [{ key: "", label: t("paged-agents.recent") }, { key: "group", label: t("paged-agents.account") }]
    : [{ key: "", label: t("paged-agents.recent") }, { key: "machine", label: t("paged-agents.machine") }, { key: "project", label: t("paged-agents.project") }, { key: "group", label: t("paged-agents.group") }];
  const machineName = (id: string) => label?.("machine", id) ?? id;
  // a to-do and a state come named; a computer, and a space or account a hub without the catalog knows by id, by the caller
  const groupName = (g: SessionGroup) => g.members || dimension === "project" || dimension === "todo" || dimension === "state" ? g.name : label?.(dimension, g.key) ?? g.name;
  // the lines under a group's name in the menu: a project's remote and directories, a space's computer
  const severalMachines = groupMachines(allGroups ?? []) > 1;
  const groupDetail = (g: SessionGroup): string | undefined => {
    if (!g.members) return g.places.map((p) => p.path).join("\n") || undefined; // a hub without the catalog: the path, as it was
    if (dimension === "group") return severalMachines && g.places[0]?.machine ? machineName(g.places[0].machine) : undefined;
    const places = g.places.map((p) => `${machineName(p.machine)} · ${shortPath(p.path)}${g.places.length > 1 ? ` · ${p.count}` : ""}`);
    return [...(g.origin ? [g.origin] : []), ...places].join("\n") || undefined;
  };
  const allOf = t(dimension === "machine" ? "paged-agents.all_machines" : dimension === "project" ? "paged-agents.all_projects" : personal ? "paged-agents.all_accounts" : "paged-agents.all_spaces");
  const dimensionLabel = grouping ? grouping.label : dimensions.find((d) => d.key === dimension)?.label;
  const chooseDimension = (d: typeof dimension) => {
    setDimension(d); setPicked(null); setMachine("");
    if (d) storeValue(browseKey(personal), d);
    else removeStoredValue(browseKey(personal));
  };
  const chooseGroup = (key: string) => setPicked(key ? groups.find((g) => g.key === key) ?? null : null);
  // an open section reads the collection as the list above it does
  const source: GroupedSource<T> = useMemo(() => ({ path, identity, compare, removeIdentity, event, summary: summaryMode, roots: rootsMode, parentOf }),
    [path, identity, compare, removeIdentity, event, summaryMode, rootsMode, parentOf]);
  const openRow = sections && isOpen && placeOf ? rootPage.rows.find(isOpen) : undefined;
  const autoMore = useRef(0);
  useEffect(() => {
    // Load to the open row's group, stopping on a repeated page (#7327).
    if (!sections || !isOpen || !placeOf || openRow || !rootPage.hasMore || rootPage.loading || rootPage.error) return;
    const rows = rootPage.rows.length;
    if (autoMore.current > rows) autoMore.current = 0;
    if (autoMore.current === rows) return;
    autoMore.current = rows;
    void root.more().catch(() => {});
  }, [sections, isOpen, placeOf, openRow, root, rootPage]);
  const openPlace = useMemo(() => openRow && placeOf ? placeOf(openRow) : undefined, [openRow, placeOf]);
  const shownTotal = sections ? grouped.total : page.total;
  const states = personal ? ["unread", "idle"] : ["needs_you", "working", "unread", "idle", "offline", "unknown"];
  const loadingLine = <p role="status" className="px-2 py-2 text-xs text-ink-3">{t("sidebar.loading")}</p>;
  const searchField = <SearchField value={query} onChange={setQuery} placeholder={t("paged-agents.search")} />;
  const stateChips = (
    <div role="radiogroup" aria-label={t("sidebar.quick_filters")} className="flex flex-wrap gap-0.5">
      {["", ...states.filter((s) => (stateTotals[s] ?? 0) > 0 || state === s)].map((s) => {
        const on = state === s;
        const n = s && scopeCounts ? stateTotals[s] ?? 0 : undefined;
        const name = s ? STATE_LABEL[s as AgentState] : t("paged-agents.all");
        return (
          <button key={s} type="button" role="radio" aria-checked={on} aria-label={n === undefined ? name : `${name} ${n}`} onClick={() => setState(s)}
            className={cx("touch-hit relative z-10 flex h-6 items-center gap-1 whitespace-nowrap rounded-full px-[7px] text-[11.5px] transition-[background-color,color,box-shadow] duration-150", on ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-2 hover:bg-hover hover:text-ink")}>
            {s && <StatusMark state={s as AgentState} size="sm" />}
            {name}
            {n !== undefined && <span className={cx("tabular-nums", on ? "text-ink-2" : "text-ink-3")}>{n}</span>}
          </button>
        );
      })}
    </div>
  );
  return (
    <div data-paged-sessions={personal ? "personal" : "runtime"} data-loaded-sessions={page.rows.length} data-has-more={page.hasMore}>
      {/* under the caller's title row its bar sits where the complete list's does: no padding of this list's above it */}
      <div className={cx("space-y-2 px-1 pb-2", !grouping && "pt-2")}>
        {/* the caller's bar and chips where the complete list has them: its padding, not this list's as well */}
        {grouping ? <><div className="-mx-1">{grouping.bar}{quick ? quick.bar(quickCounts) : stateChips}</div>{grouping.chips}</> : <>{searchField}
        <SegmentedTrack role="radiogroup" label={t("paged-agents.group_by")} className="flex gap-0.5 rounded-full p-0.5" thumbClassName="rounded-full bg-surface shadow-btn">
          {dimensions.map((d) => {
            const on = d.key === dimension;
            return (
              <button key={d.key || "recent"} type="button" role="radio" aria-checked={on} data-selected={on} onClick={() => chooseDimension(d.key)}
                className={cx("touch-hit relative z-[1] flex h-6 min-w-0 flex-auto items-center justify-center whitespace-nowrap rounded-full px-1.5 text-[11.5px] transition-colors duration-150", on ? "font-medium text-ink" : "text-ink-2 hover:text-ink")}>
                <span className="truncate">{d.label}</span>
              </button>
            );
          })}
        </SegmentedTrack>
        {stateChips}</>}
        {(dimension && !grouping || personal) && <div className="flex flex-wrap gap-1">
          {dimension === "group" && !personal && !cataloged && <PickChip label={t("paged-agents.machine")} header={t("paged-agents.machine")} value={machine} shown={machine ? machineName(machine) : t("paged-agents.all_machines")}
            items={[{ key: "", label: t("paged-agents.all_machines") }, ...machines.map(([id, n]) => ({ key: id, label: machineName(id), hint: String(n) }))]}
            onChoose={(id) => { setMachine(id); setPicked(null); }} />}
          {dimension && <PickChip label={t("paged-agents.group_filter")} header={dimensionLabel} value={value} shown={picked ? groupName(picked) : allOf} grow
            items={[{ key: "", label: allOf }, ...groups.map((g) => ({ key: g.key, label: groupName(g), hint: String(g.count), detail: groupDetail(g) }))]}
            onChoose={chooseGroup} />}
          {personal && <SearchField value={providerQuery} onChange={setProviderQuery} placeholder={t("paged-agents.provider")} tone="inset" icon={false} className="min-w-0 flex-1" />}
        </div>}
        <div className="flex min-w-0 items-center justify-between gap-2">
          <p className="min-w-0 truncate text-xs text-ink-3" data-session-total>{shownTotal === null ? "\u00a0" : t("paged-agents.count", { count: shownTotal })}</p>
          {/* a display choice of the one flat list: its groups are sections of their own */}
          {activeAt && !sections && <ActiveWindowMenu value={win} onChange={chooseWindow} />}
        </div>
      </div>
      {/* not twice: the list's own error with its Retry says it (its groups', or its rows') */}
      {(facetError || scopeError) && !(sections ? grouped.error : page.error) && <p role="alert" className="px-2 text-xs text-red">{facetError || scopeError}</p>}
      {sections && <>
        {allGroups && allGroups.length > 0 && <Suspense fallback={loadingLine}>
          <GroupedSessions groups={allGroups} dimension={dimension as GroupDimension} kind={personal ? "personal" : "runtime"} scope={groupScope} source={source}
            renderRow={renderRow} groupName={groupName} machineName={machineName} open={openPlace} foldScope={grouping?.foldScope} />
        </Suspense>}
        {!allGroups && !grouped.error && loadingLine}
        {grouped.error && <ListError text={failureText(grouped.error)} onRetry={grouped.retry} />}
        {allGroups && !allGroups.length && !grouped.error && <p className="px-2 py-2 text-xs text-ink-3">{t("sidebar.no_match")}</p>}
      </>}
      {!sections && <><div ref={container} data-virtual-sessions className="relative" style={{ height: totalSize }}>
        {virtual.getVirtualItems().map((item) => {
          const row = rowAt(item.index);
          return (
            <div key={item.key} ref={virtual.measureElement} data-index={item.index} {...(row === null ? { "data-earlier": "" } : { "data-session-row": "" })} className="absolute top-0 left-0 w-full" style={{ transform: `translateY(${item.start - margin}px)` }}>
              {row === null ? <EarlierHeader count={earlier!.earlier} needs={earlier!.earlierNeedsYou} window={win} folded={folded} onToggle={toggleEarlier} />
                : nesting?.nested.has(row) ? <div data-nested-session className={SESSION_LIST_STYLE.nestedRow}>{renderRow(row)}</div> : renderRow(row)}
            </div>
          );
        })}
      </div>
      {page.loading && loadingLine}
      {page.error && <ListError text={failureText(page.error)} onRetry={() => void list.retry().catch(() => {})} />}
      {page.loaded && !page.loading && !page.error && !page.rows.length && <p className="px-2 py-2 text-xs text-ink-3">{t("sidebar.no_match")}</p>}
      {hasMore && !page.loading && !page.error && <ShowMoreRow label={t("paged-agents.more")} onClick={() => void list.more().catch(() => {})} />}
      </>}
    </div>
  );
}

/** The list's "Active within" display choice (#4236): a quiet menu beside the count, naming the window it keeps. */
function ActiveWindowMenu({ value, onChange }: { value: ActiveWindow; onChange: (w: ActiveWindow) => void }) {
  const menu = useMenu();
  const items: MenuItem[] = ACTIVE_WINDOWS.map((w, i) => ({ key: w || "any", label: windowLabel(w), radio: true, checked: w === value, divider: i === 1, onSelect: () => onChange(w) }));
  return <>
    <button type="button" aria-haspopup="menu" aria-expanded={Boolean(menu.anchor)} aria-label={t("paged-agents.active_within_value", { window: windowLabel(value) })} title={t("paged-agents.active_within")} onClick={menu.open}
      className={cx("touch-hit relative z-10 flex h-6 min-w-0 shrink-0 items-center gap-1 whitespace-nowrap rounded-[6px] px-1.5 text-[11.5px] transition-colors duration-150 hover:bg-hover hover:text-ink", value ? "font-medium text-ink" : "text-ink-3")}>
      <Icon name="clock" size={11} className="shrink-0" />
      <span className="truncate">{windowLabel(value)}</span>
      <Icon name="chevron" size={11} className="shrink-0 text-ink-3" />
    </button>
    {menu.anchor && <Menu items={items} anchor={menu.anchor} onClose={menu.close} header={t("paged-agents.active_within")} align="right" width={200} />}
  </>;
}

/** The fold over the sessions last active before the window: how many, and how many of them need you (DESIGN_SYSTEM 3.4). */
function EarlierHeader({ count, needs, window, folded, onToggle }: { count: number; needs: number; window: ActiveWindow; folded: boolean; onToggle: () => void }) {
  return (
    <div className="group/sec flex items-center gap-1 px-2 pt-2 pb-0.5">
      <button type="button" aria-expanded={!folded} onClick={onToggle} title={t("paged-agents.earlier_hint", { count, window: windowLabel(window) })}
        className="relative z-10 flex min-h-7 min-w-0 flex-1 items-center gap-1.5 whitespace-nowrap text-left [@media(pointer:coarse)]:min-h-11">
        <span className="min-w-0 truncate text-[12.5px] font-semibold text-ink-2">{t("paged-agents.earlier")}</span>
        <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{count}</span>
        <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 text-ink-3 transition-transform" style={{ ...sidebarFoldMotion(!folded), transform: folded ? "rotate(-90deg)" : "none" }} />
      </button>
      <NeedsCount n={needs} />
    </div>
  );
}

/** An element's width, followed while it is on screen: the sidebar it sits in can be dragged wider or narrower. */
function useWidth(el: HTMLElement | null): number {
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    if (!el) return;
    const measure = () => setWidth(Math.round(el.getBoundingClientRect().width));
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [el]);
  return width;
}

/** A pill that names what is chosen and opens the choices in the design system's menu (no native select). The menu is
 *  as wide as its pill, like a select's list, and never narrower than a menu is anywhere else. */
function PickChip({ label, header = label, value, shown, items, onChoose, grow = false }: { label: string; header?: string; value: string; shown: string; items: { key: string; label: string; hint?: string; detail?: string }[]; onChoose: (key: string) => void; grow?: boolean }) {
  const menu = useMenu();
  const width = Math.max(useWidth(menu.anchor), SESSION_LIST_METRICS.pickMenuMin);
  const rows: MenuItem[] = items.map((it, i) => ({ ...it, radio: true, checked: it.key === value, divider: i === 1, onSelect: () => onChoose(it.key) }));
  return <>
    <button type="button" aria-label={label} aria-haspopup="menu" aria-expanded={Boolean(menu.anchor)} title={shown} onClick={menu.open}
      className={cx("touch-hit relative z-10 flex h-6 min-w-0 items-center gap-1 whitespace-nowrap rounded-full bg-inset px-2 text-[11.5px] shadow-hairline transition-colors duration-150 hover:bg-hover", value ? "font-medium text-ink" : "text-ink-2", grow && "flex-1")}>
      <span className="min-w-0 flex-1 truncate text-left">{shown}</span>
      <Icon name="chevron" size={11} className="shrink-0 text-ink-3" />
    </button>
    {menu.anchor && <Menu items={rows} anchor={menu.anchor} onClose={menu.close} header={header} width={width} />}
  </>;
}
