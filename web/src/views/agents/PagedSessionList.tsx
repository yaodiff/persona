// Bounded collection views: filters/counts belong to the server; only the loaded prefix lives in React.
import { Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { followHinted } from "../../lib/hinted-read";
import { onShownHint } from "../../lib/shown-hints";
import { filterQuery, SessionList, type ListCounts, type ListFilters } from "../../lib/session-list";
import { boundedCounts } from "../../lib/sessions";
import { groupMachines, holdsPlace, JUST_FINISHED_KEY, type GroupDimension, type SessionGroup, type SessionPlace, type TodoNames } from "../../lib/session-groups";
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
import { useHeldOrder } from "../../lib/sidebar-engaged";
import { useFolded } from "../../lib/use-folded";
import { nestUnderParents, type StableRowOrder } from "../../lib/side-sessions";
import { sameValue } from "../../lib/stable-lines";
import { lazyPage } from "../../lib/lazy-page";
import { NeedsCount, ShowMoreRow } from "../SidebarSection";
import type { GroupedSessionsProps, GroupedSource, HeldSeenGroups } from "./GroupedSessions";
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
  /** The personal list's Browse by account: the mark drawn before an account's name (its provider's), in its group's
   *  heading and in the account picker. The id `label` replaces stays the name's tooltip there. */
  accountMark?: (account: string) => ReactNode;
  /** The personal list's provider choices: the products its loaded sessions actually use, by display name. */
  providerOptions?: { key: string; label: string }[];
  /** The hub's computer order, shared with the complete list; count maps alone cannot carry it (#11801). */
  machineOrder?: readonly string[];
  /** The hub names this collection's projects and spaces (`sessions_catalog`): a repository checked out on several
   *  computers is one project, a space is one computer's. Without it the groups are the count maps' paths and ids. */
  catalog?: boolean;
  /** The hub lists the Just finished rows apart (`sessions_just_finished`, #9751): By state draws them a section. */
  justFinished?: boolean;
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
  /** Row identities excluded from both flat rows and grouped sections (e.g. in-flight launches shown under Starting). */
  excludeIdentities?: ReadonlySet<string>;
  /** The sidebar's groupings (#6662): the caller chooses the dimension with its own bar, the complete list's, so both
   *  lists show the same control; this list draws that bar first, then the state chips and what ⌘K put on it, as the
   *  complete list does, with no value pill or search field of its own. A search lists its matches flat. `label` names
   *  the dimension. */
  grouping?: PagedGrouping<T>;
  /** The words a failed read of the list's rows or groups is said in, given its reason (the reason alone by default): the
   *  sidebar's says it could not read the Agents list, as its complete list does. */
  failureText?: (reason: string) => string;
  /** The hub's counts of this list's scope as the page shows them, given rows read in that scope: the sidebar's leaves
   *  out of Unread an agent seen on this page that those rows, and so the counts read with them, still have Unread
   *  (lib/seen-outbox.ts heldCounts). The chips' numbers and By state's sections follow it. */
  holdCounts?: (counts: ListCounts, rows: readonly T[]) => ListCounts;
}

export interface PagedGrouping<T> {
  dimension: GroupDimension; bar: ReactNode; label: string; todos?: TodoNames; quick?: PagedQuickFilters;
  /** The sections follow this scope's fold-all (useFolded). */ foldScope?: string;
  /** Stable structural placement. The transport still uses `compare`, whose newest-first order binds the hub's cursors. */
  rowOrder?: StableRowOrder<T>;
  /** "Sort Agents by recent activity" (Settings › Agents): the computers' and spaces' sections by their newest session,
   *  the rows in the transport's newest-first order (no `rowOrder`). */
  byUpdate?: boolean;
  /** Keep the order the list is drawn in while someone is using the sidebar (lib/sidebar-engaged.ts); the activity
   *  order then moves once they look away. Rows and sections still update in place. */
  hold?: boolean;
  /** What the Filter button narrows the list by, beyond the chips and ⌘K's words (the computer, as `machine_id`). */
  filters?: ListFilters;
  /** The words ⌘K's "Filter Agents list" put on the list (#6589), sent as the search: the sidebar's list has no field of
   *  its own, `chips` says what narrows it where the field was. */
  query?: string; chips?: ReactNode;
  /** Rows read during this visit kept in place (#12585). */
  heldSeen?: HeldSeenGroups<T>;
}

/** The complete list's quick filters on the paged list (`sessions_quick_filters`, #6662), in place of its state chips:
 *  what the chosen chip selects, what every read leaves out (hidden botmux), and the chips, drawn from the counts of
 *  the list without the chip (one read for every chip's number). The caller keeps the choice, the complete list's. */
export interface PagedQuickFilters { select: ListFilters; exclude: ListFilters; bar: (counts: FilterCounts | undefined) => ReactNode; onCounts?: (counts: FilterCounts | undefined) => void }

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

export function PagedSessionList<T>({ root, path, identity, compare, removeIdentity, event, renderRow, personal = false, label, accountMark, providerOptions = [], machineOrder, catalog = false, justFinished = false, placeOf, activeAt, needsYou, isOpen, parentOf, excludeIdentities, grouping, failureText = sameText, holdCounts }: PagedSessionListProps<T>) {
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
  useEffect(() => {
    const timer = setTimeout(() => setTyped(query.trim()), SESSION_LIST_METRICS.searchDelay);
    return () => clearTimeout(timer);
  }, [query]);
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
  const narrowed = grouping?.filters ?? NONE;
  const filters: ListFilters = useMemo(() => ({
    ...(q ? { q } : {}), ...chip, ...(provider ? { provider } : {}),
    ...groupFilters,
    ...(spaceMachine ? { machine_id: spaceMachine } : {}),
    ...narrowed,
    ...exclude,
  }), [q, chip, groupFilters, spaceMachine, provider, narrowed, exclude]);
  const filtered = Object.keys(filters).length > 0;
  // a dimension with no group picked: the list is drawn as its groups, and the one flat list is not read; in the
  // sidebar's groupings a search lists its matches flat
  const sections = Boolean(dimension) && !picked && !(grouping && q);
  const summaryMode = root.isSummary();
  // a hub that folds child sessions (#5120): every read and count of this list leaves them out, as the root's do
  const rootsMode = root.isRoots();
  const crossMachineMode = root.isCrossMachineHierarchy();
  const queryPath = `${path}${path.includes("?") ? "&" : "?"}`;
  const countsPath = rootsMode ? `${queryPath}roots=1&${crossMachineMode ? "cross_machine_hierarchy=1&" : ""}` : queryPath;
  const rowOrder = grouping?.rowOrder;
  const presentation = useMemo(() => rowOrder ? { ...rowOrder, parentOf } : undefined, [rowOrder, parentOf]);
  const list = useMemo(() => filtered ? new SessionList<T>(path, identity, compare, undefined, removeIdentity, { summary: summaryMode, roots: rootsMode, crossMachineHierarchy: crossMachineMode, pageCounts: () => root.usesPageCounts(), presentation }) : root,
    [filtered, path, identity, compare, removeIdentity, summaryMode, rootsMode, crossMachineMode, root, filters, presentation]);
  // The root is shared with the hub's counts and invalidation. Apply the same retention as a filtered list instead of
  // letting an activity-only transport prefix displace a flat list's shown rows (#11801).
  // The sidebar's list also lets go of it when the activity order is chosen (Settings › Agents), which has none.
  const sidebarList = Boolean(grouping);
  useLayoutEffect(() => { if (presentation || sidebarList) root.setPresentation(presentation); }, [root, presentation, sidebarList]);
  const page = useSyncExternalStore(list.subscribe, list.getSnapshot, list.getSnapshot);
  const rootPage = useSyncExternalStore(root.subscribe, root.getSnapshot, root.getSnapshot);
  const scopeQuery = useMemo(() => {
    const scope = { ...filters };
    for (const key of CHIP_KEYS) delete scope[key];
    return countsQuery(scope);
  }, [filters]);
  // Counts without state keep other state tabs reachable; machine scopes disambiguate computer-local groups.
  const facetFilters = useMemo(() => ({ ...(q ? { q } : {}), ...(provider ? { provider } : {}), ...(spaceMachine ? { machine_id: spaceMachine } : {}), ...narrowed, ...exclude }), [q, provider, spaceMachine, narrowed, exclude]);
  // Unscoped counts are the root's own: follow them. Scoped counts are read on their own, now and after hints (one read
  // per hint window, lib/hinted-read.ts); they do not follow the root's counts, which land for that same hint.
  const facetScoped = Object.keys(facetFilters).length > 0;
  const [facets, setFacets] = useState<ListCounts | null>(() => (facetScoped ? null : rootPage.counts));
  const [facetError, setFacetError] = useState("");
  // Retry on a failed scoped facets read: read it again now
  const [facetAttempt, setFacetAttempt] = useState(0);
  const [scopeCounts, setScopeCounts] = useState<ListCounts | null>(() => (scopeQuery === "counts=1" ? rootPage.counts : null));
  const [scopeError, setScopeError] = useState("");
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
    // read now and after hints, one read per window for every list in the page that follows the same counts (#15910)
    return followHinted<ListCounts>(event, `${countsPath}${facetQuery}`, (counts) => {
      if (counts?.state && counts.group && counts.project && counts.machine) { setFacets(counts); setFacetError(""); }
    }, (e) => setFacetError(errorText(e)));
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
    return followHinted<ListCounts>(event, `${countsPath}${scopeQuery}`, (counts) => {
      if (counts?.state) { setScopeCounts(counts); setScopeError(""); }
    }, (e) => setScopeError(errorText(e)));
  }, [scopeQuery, scopeFromFacets, countsPath, event]);
  useEffect(() => {
    // Even a grouped, filtered list needs the root counts for the unfiltered chips.
    if (!root.getSnapshot().loaded) void root.load().catch(() => {});
  }, [root, summaryMode, rootsMode]);
  useEffect(() => {
    if (!filtered || sections) return;
    void list.setFilters(filters).catch(() => {});
    const off = onShownHint(event, () => list.invalidate());
    return () => { off(); list.reset(); };
  }, [list, filters, filtered, sections, event]);

  // The groups of the chosen dimension, for the value pill and the sections: everything that narrows the list but the
  // group itself. Their counts follow the state tab, so a group's number is the number of rows it lists.
  const groupScope: ListFilters = useMemo(() => ({ ...facetFilters, ...chip }), [facetFilters, chip]);
  const scopeNeeds = scopeCounts ? boundedCounts(scopeCounts, false).needs : undefined;
  // the facets' own read when the groups are drawn from them: its failure is theirs (audit item 4: the root list's read
  // failing left the sections on "Loading…" for good)
  const facetsFailure = facetScoped ? facetError : rootPage.error ?? "";
  const retryFacets = useCallback(() => {
    if (facetScoped) setFacetAttempt((n) => n + 1);
    else void root.retry().catch(() => {});
  }, [facetScoped, root]);
  const grouped = useSessionGroups({ dimension, catalog: cataloged, countsPath, scope: groupScope, facets, facetQuery, facetsError: facetsFailure, retryFacets, waiting: sections ? scopeNeeds : 0, event, todos: grouping?.todos, justFinished, machineOrder });
  // The scope's counts as the page shows them (holdCounts): only against the rows the same answer brought (the root's,
  // or the filtered list's own page, on a hub that sends a page's counts with it), so a newer count is never taken for
  // an older row's or the other way round. Counts read on their own (a scope only the hub can count, such as hidden
  // botmux sessions in a grouping) are the hub's: they are read again after hints (lib/hinted-read.ts), the one after
  // the hub records a seen too.
  const coupled = !root.usesPageCounts() ? undefined
    : scopeQuery === "counts=1" ? (rootPage.counts ? { counts: rootPage.counts, rows: rootPage.rows } : undefined)
    : scopeFromFacets && facetsFromList && page.counts ? { counts: page.counts, rows: page.rows } : undefined;
  const coupledCounts = coupled?.counts, coupledRows = coupled?.rows;
  const held = useMemo(() => (coupledCounts && coupledRows && holdCounts ? holdCounts(coupledCounts, coupledRows) : undefined),
    [coupledCounts, coupledRows, holdCounts]);
  const shownCounts = held && held !== coupledCounts ? held : scopeCounts;
  // By state drawn from those same counts (no chip narrows the groups): the sections the held agents left (Unread, Just
  // finished) are smaller, Seen larger
  const chipless = !Object.keys(filters).some((key) => (CHIP_KEYS as readonly string[]).includes(key));
  const heldSeen = grouping?.heldSeen;
  const countedGroups = useMemo(() => {
    const groups = grouped.groups;
    if (!groups || dimension !== "state" || !chipless || !held || !coupledCounts || held === coupledCounts) return groups;
    const unread = (coupledCounts.state.unread ?? 0) - (held.state.unread ?? 0);
    const finished = (coupledCounts.just_finished ?? 0) - (held.just_finished ?? 0);
    const less = (key: string) => (key === "unread" ? unread : key === JUST_FINISHED_KEY ? finished : key === "idle" ? -(unread + finished) : 0);
    return groups.map((g) => (less(g.key) ? { ...g, count: Math.max(0, g.count - less(g.key)) } : g));
  }, [grouped.groups, dimension, chipless, held, coupledCounts]);
  // "Sort Agents by recent activity": the computers' and spaces' sections by their newest session, as far as the root's
  // newest page tells (the hub reads no group's time); the ones it has none of keep their order after them, and the
  // sessions on no space stay last. Its to-dos, projects and states keep their own orders there too.
  const byUpdate = Boolean(grouping?.byUpdate) && (dimension === "machine" || dimension === "group");
  const rankedGroups = useMemo(() => {
    if (!byUpdate || !countedGroups || !placeOf) return countedGroups;
    const places = rootPage.rows.map(placeOf);
    const rank = (g: SessionGroup) => {
      if (g.rest) return places.length + 1;
      const at = places.findIndex((p) => holdsPlace(g, dimension as GroupDimension, p));
      return at < 0 ? places.length : at;
    };
    return countedGroups.map((g) => ({ g, at: rank(g) })).sort((a, b) => a.at - b.at).map(({ g }) => g);
  }, [byUpdate, countedGroups, placeOf, rootPage.rows, dimension]);
  const heldGroups = useHeldOrder(rankedGroups ?? [], (g) => g.key, Boolean(grouping?.hold) && byUpdate);
  const allGroups = rankedGroups ? (heldGroups as SessionGroup[]) : rankedGroups;
  // the picked group follows what the hub says of it now (a project gains a directory); gone from the scope, it stays picked
  useEffect(() => {
    if (!picked || !allGroups) return;
    const now = allGroups.find((g) => g.key === picked.key);
    if (now && !sameValue(now, picked)) setPicked(now);
  }, [allGroups, picked]);

  const rawTransportRows = list.getTransportRows();
  const transportRows = useMemo(
    () => excludeIdentities?.size ? rawTransportRows.filter((row) => !excludeIdentities.has(identity(row))) : rawTransportRows,
    [rawTransportRows, excludeIdentities, identity],
  );
  const visibleRows = useMemo(
    () => excludeIdentities?.size ? page.rows.filter((row) => !excludeIdentities.has(identity(row))) : page.rows,
    [page.rows, excludeIdentities, identity],
  );
  // held still while someone uses the sidebar under the activity order: a row updates in place, it does not move
  const pageRows = useHeldOrder(visibleRows, identity, Boolean(grouping?.hold) && Boolean(grouping?.byUpdate)) as T[];
  const visiblePageTotal = Math.max(0, page.total - (page.rows.length - visibleRows.length));
  // Activity totals need their own newest-first transport groups: structural presentation can put an unrelated older
  // row between a recent side session and its parent, which must not end the transport's recent prefix (#11801).
  const activityNesting = useMemo(() => activeAt && parentOf ? nestUnderParents(transportRows, identity, parentOf, activeAt) : null, [transportRows, identity, parentOf, activeAt]);
  const activityRows = activityNesting?.rows ?? transportRows;
  // SessionList has already applied the one structural presentation order. This separate view draws nested rows and
  // partitions the retained presentation; its lead never substitutes for the transport's activity lead.
  const nesting = useMemo(() => parentOf ? nestUnderParents(pageRows, identity, parentOf, activeAt) : null, [pageRows, identity, parentOf, activeAt]);
  const rows = nesting?.rows ?? pageRows;
  const orderedRows = rows;
  // a group is as recent as its most recent member, so Earlier never splits one
  const groupActiveAt = useMemo(() => activeAt && nesting ? (row: T) => activeAt(nesting.lead.get(row) ?? row) : activeAt, [activeAt, nesting]);
  const activityActiveAt = useMemo(() => activeAt && activityNesting ? (row: T) => activeAt(activityNesting.lead.get(row) ?? row) : activeAt, [activeAt, activityNesting]);

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
  const needsYouTotal = chip.state ? (chip.state === "needs_you" ? visiblePageTotal : 0) : chip.pane_source ? (page.counts ? boundedCounts(page.counts, false).needs : undefined) : scopeNeeds;
  const rowNeedsYou = needsYou ?? (() => false);
  const split = activityActiveAt && start !== null
    ? splitByActivity(activityRows, activityActiveAt, start, { total: visiblePageTotal, hasMore: page.hasMore, needsYou: rowNeedsYou, needsYouTotal })
    : null;
  // Transport's newest-first split supplies the server totals. Presentation partitions its own retained rows too: an
  // older retained row needs Earlier even when every row left in the current transport prefix is recent.
  const presentedRows = useMemo((): readonly T[] => {
    if (!groupActiveAt || start === null) return orderedRows;
    return [...orderedRows.filter((row) => groupActiveAt(row) >= start), ...orderedRows.filter((row) => !(groupActiveAt(row) >= start))];
  }, [groupActiveAt, start, orderedRows]);
  const firstEarlier = groupActiveAt && start !== null ? presentedRows.findIndex((row) => !(groupActiveAt(row) >= start)) : -1;
  const recent = firstEarlier === -1 ? presentedRows.length : firstEarlier;
  const presentedEarlier = presentedRows.length - recent;
  const hasEarlier = presentedEarlier > 0 || Boolean(split?.reached && split.earlier > 0);
  // A retained recent row can fall outside the transport prefix while a newly recent transport row is not presented.
  // Count their identity union: subtracting only the transport's recent rows charges both cases to Earlier (#11801).
  const loaded = new Set(transportRows.map(identity));
  const retainedRecent = presentedRows.slice(0, recent).filter((row) => !loaded.has(identity(row)));
  const earlierCount = hasEarlier && split ? Math.max(0, visiblePageTotal - split.recent - retainedRecent.length, presentedEarlier) : 0;
  const recentNeedsYou = activityRows.slice(0, split?.recent ?? 0).filter(rowNeedsYou).length + retainedRecent.filter(rowNeedsYou).length;
  const earlierNeedsYou = needsYouTotal === undefined ? presentedRows.slice(recent).filter(rowNeedsYou).length
    : Math.min(earlierCount, Math.max(0, needsYouTotal - recentNeedsYou));
  const earlier = hasEarlier && earlierCount > 0 ? { recent, reached: true, earlier: earlierCount, earlierNeedsYou } : null;
  const [folded, toggleEarlier] = useFolded(personal ? "personal-sessions-earlier" : "sessions-earlier", true, {
    holds: Boolean(earlier && isOpen && presentedRows.slice(earlier.recent).some(isOpen)),
  });
  // folded, the list stops at its header: no further pages are read until it opens
  const foldedAway = Boolean(earlier && folded);
  const hasMore = page.hasMore && !foldedAway;
  const itemCount = sections ? 0 : (foldedAway ? earlier!.recent : rows.length) + (earlier ? 1 : 0);
  // the Earlier header sits between the recent rows and the earlier ones; null is the header
  const rowAt = (i: number): T | null => !earlier ? presentedRows[i] : i === earlier.recent ? null : presentedRows[i > earlier.recent ? i - 1 : i];

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
      if (!sections && scrolled.current && hasMore && !page.loadingMore && !page.error &&
          parent.scrollTop + parent.clientHeight >= margin + totalSize - SESSION_LIST_METRICS.overscan * SESSION_LIST_METRICS.estimate) {
        void list.more().catch(() => {});
      }
    };
    const scroll = () => { scrolled.current = true; maybeMore(); };
    parent.addEventListener("scroll", scroll, { passive: true });
    maybeMore();
    return () => parent.removeEventListener("scroll", scroll);
  }, [list, getScrollElement, sections, hasMore, page.loadingMore, page.error, margin, totalSize]);

  const adjusted = useMemo(() => (shownCounts ? boundedCounts(shownCounts, false) : undefined), [shownCounts]);
  const stateTotals: Record<string, number> = adjusted ? { ...shownCounts!.state, needs_you: adjusted.needs, working: adjusted.working + adjusted.waiting } : shownCounts?.state ?? {};
  // the quick filters' numbers, as the complete list counts them: Working without the ones waiting in the background,
  // and agents waiting only on supervisor-held cards counted Working instead of Needs you (lib/sessions.ts boundedCounts)
  const quickCounts = useMemo((): FilterCounts | undefined => adjusted ? {
    needs: adjusted.needs, unread: adjusted.unread, working: adjusted.working, bg_waiting: adjusted.waiting,
    feishu: shownCounts!.pane_source?.feishu ?? 0, terminal: shownCounts!.pane_source?.terminal ?? 0,
  } : undefined, [shownCounts, adjusted]);
  const onQuickCounts = quick?.onCounts;
  useEffect(() => { onQuickCounts?.(quickCounts); }, [onQuickCounts, quickCounts]);
  // the fixed group order: the menu lists a bounded number of them, and the picked one whatever its place
  const groups = useMemo(() => {
    const listed = (allGroups ?? []).slice(0, SESSION_LIST_METRICS.groupOptions);
    return picked && !listed.some((g) => g.key === picked.key) ? [...listed, allGroups?.find((g) => g.key === picked.key) ?? { ...picked, count: 0, needs: 0 }] : listed;
  }, [allGroups, picked]);
  const machines = useMemo(() => Object.entries(rootPage.counts?.machine ?? {}).filter(([id]) => id).sort(([a], [b]) => a.localeCompare(b)), [rootPage.counts]);
  // By space with sessions on more than one computer: each computer's spaces under its heading (#13522, as iOS's By
  // Project). The whole list's computers decide it, so a chip that leaves one computer's sessions moves no level.
  const computers = rootPage.counts ? machines.filter(([, n]) => n > 0).length : groupMachines(allGroups ?? []);
  const byComputer = cataloged && dimension === "group" && computers > 1;
  const dimensions: { key: typeof dimension; label: string }[] = personal
    ? [{ key: "", label: t("paged-agents.recent") }, { key: "group", label: t("paged-agents.account") }]
    : [{ key: "", label: t("paged-agents.recent") }, { key: "machine", label: t("paged-agents.machine") }, { key: "project", label: t("paged-agents.project") }, { key: "group", label: t("paged-agents.group") }];
  const machineName = (id: string) => label?.("machine", id) ?? id;
  // a to-do and a state come named; a computer, and a space or account a hub without the catalog knows by id, by the caller
  const groupName = (g: SessionGroup) => g.members || dimension === "project" || dimension === "todo" || dimension === "state" ? g.name : label?.(dimension, g.key) ?? g.name;
  // only the personal list's accounts: every other grouping keeps its plain name
  const accounts = personal && dimension === "group";
  const groupLead = accounts && accountMark ? (g: SessionGroup) => (g.members ? undefined : accountMark(g.key)) : undefined;
  const groupTip = accounts ? (g: SessionGroup) => { const name = groupName(g); return name !== g.key ? g.key : undefined; } : undefined;
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
  const source: GroupedSource<T> = useMemo(() => ({ path, identity, compare, removeIdentity, event, summary: summaryMode, roots: rootsMode, crossMachineHierarchy: crossMachineMode, parentOf, rowOrder, excludeIdentities, hold: Boolean(grouping?.hold) && Boolean(grouping?.byUpdate) }),
    [path, identity, compare, removeIdentity, event, summaryMode, rootsMode, crossMachineMode, parentOf, rowOrder, excludeIdentities, grouping?.hold, grouping?.byUpdate]);
  const openRow = sections && isOpen && placeOf ? rootPage.rows.find(isOpen) : undefined;
  const autoMore = useRef(0);
  useEffect(() => {
    // Load to the open row's group, stopping on a repeated page (#7327).
    if (!sections || !isOpen || !placeOf || openRow || !rootPage.hasMore || rootPage.loadingMore || rootPage.error) return;
    const rows = rootPage.rows.length;
    if (autoMore.current > rows) autoMore.current = 0;
    if (autoMore.current === rows) return;
    autoMore.current = rows;
    void root.more().catch(() => {});
  }, [sections, isOpen, placeOf, openRow, root, rootPage]);
  const openPlace = useMemo(() => openRow && placeOf ? placeOf(openRow) : undefined, [openRow, placeOf]);
  const shownTotal = sections ? grouped.total : visiblePageTotal;
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
    <div data-paged-sessions={personal ? "personal" : "runtime"} data-loaded-sessions={pageRows.length} data-has-more={page.hasMore}>
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
          {dimension && <PickChip label={t("paged-agents.group_filter")} header={dimensionLabel} value={value} shown={picked ? groupName(picked) : allOf} lead={picked ? groupLead?.(picked) : undefined} grow
            items={[{ key: "", label: allOf }, ...groups.map((g) => ({ key: g.key, label: groupName(g), hint: String(g.count), detail: groupDetail(g), lead: groupLead?.(g), tip: groupTip?.(g) }))]}
            onChoose={chooseGroup} />}
          {personal && providerOptions.length > 0 && <PickChip
            label={t("paged-agents.provider")}
            header={t("paged-agents.provider")}
            value={provider}
            shown={provider ? (providerOptions.find((p) => p.key === provider)?.label ?? provider) : t("paged-agents.all_providers")}
            items={[{ key: "", label: t("paged-agents.all_providers") }, ...providerOptions]}
            onChoose={(id) => { setProvider(id); setPicked(null); }}
            grow
          />}
        </div>}
        <div className="-mx-1 flex min-w-0 items-center justify-between gap-2 px-2">
          <p className="min-w-0 truncate text-xs text-ink-3" data-session-total>{shownTotal === null ? "\u00a0" : t("paged-agents.count", { count: shownTotal })}</p>
          {/* a display choice of the one flat list: its groups are sections of their own */}
          {activeAt && !sections && <ActiveWindowMenu value={win} onChange={chooseWindow} />}
        </div>
      </div>
      {/* not twice: the list's own error with its Retry says it (its groups', or its rows') */}
      {(facetError || scopeError) && !(sections ? grouped.error : page.error) && <p role="alert" className="px-2 text-xs text-red">{facetError || scopeError}</p>}
      {sections && <>
        {allGroups && (allGroups.length > 0 || (dimension === "state" && Boolean(heldSeen?.rows.length) && (chipless || chip.state === "unread"))) && <Suspense fallback={loadingLine}>
          <GroupedSessions groups={allGroups} dimension={dimension as GroupDimension} kind={personal ? "personal" : "runtime"} scope={groupScope} source={source}
            renderRow={renderRow} groupName={groupName} groupLead={groupLead} groupTip={groupTip} machineName={machineName} open={openPlace} foldScope={grouping?.foldScope} heldSeen={heldSeen} placeOf={placeOf} justFinished={justFinished} chipless={chipless} byComputer={byComputer} machineOrder={machineOrder} />
        </Suspense>}
        {!allGroups && !grouped.error && loadingLine}
        {grouped.error && <ListError text={failureText(grouped.error)} onRetry={grouped.retry} />}
        {allGroups && !allGroups.length && !(dimension === "state" && Boolean(heldSeen?.rows.length) && (chipless || chip.state === "unread")) && !grouped.error && <p className="px-2 py-2 text-xs text-ink-3">{t("sidebar.no_match")}</p>}
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
      {/* a background refresh moves nothing: only a first read or a page someone asked for says Loading (#9215) */}
      {page.loading && (!page.loaded || page.loadingMore) && loadingLine}
      {page.error && <ListError text={failureText(page.error)} onRetry={() => void list.retry().catch(() => {})} />}
      {page.loaded && !page.loading && !page.error && !pageRows.length && <p className="px-2 py-2 text-xs text-ink-3">{t("sidebar.no_match")}</p>}
      {hasMore && !page.loadingMore && !page.error && <ShowMoreRow label={t("paged-agents.more")} onClick={() => void list.more().catch(() => {})} />}
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
function PickChip({ label, header = label, value, shown, lead, items, onChoose, grow = false }: { label: string; header?: string; value: string; shown: string; lead?: ReactNode; items: { key: string; label: string; hint?: string; detail?: string; lead?: ReactNode; tip?: string }[]; onChoose: (key: string) => void; grow?: boolean }) {
  const menu = useMenu();
  const width = Math.max(useWidth(menu.anchor), SESSION_LIST_METRICS.pickMenuMin);
  const rows: MenuItem[] = items.map((it, i) => ({ ...it, radio: true, checked: it.key === value, divider: i === 1, onSelect: () => onChoose(it.key) }));
  return <>
    <button type="button" aria-label={label} aria-haspopup="menu" aria-expanded={Boolean(menu.anchor)} title={shown} onClick={menu.open}
      className={cx("touch-hit relative z-10 flex h-6 min-w-0 items-center gap-1 whitespace-nowrap rounded-full bg-inset px-2 text-[11.5px] shadow-hairline transition-colors duration-150 hover:bg-hover", value ? "font-medium text-ink" : "text-ink-2", grow && "flex-1")}>
      {lead}
      <span className="min-w-0 flex-1 truncate text-left">{shown}</span>
      <Icon name="chevron" size={11} className="shrink-0 text-ink-3" />
    </button>
    {menu.anchor && <Menu items={rows} anchor={menu.anchor} onClose={menu.close} header={header} width={width} />}
  </>;
}
