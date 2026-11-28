// The paged list by group: one folding section for each computer, project, space or account (the sidebar's to-do and
// state too, #6662), with how many sessions it holds and how many of them need the user on its heading. The first few
// sections start open on their sessions (by to-do every to-do's, "Not on a to-do" folded; by state Just finished
// too, #9751);
// a section reads its own sessions while it is open, a page at a time, and stops when it is folded, so the groups of a
// long list cost nothing until someone looks into one. By space on several computers, each computer is a section of
// its own around its spaces (#13522); a space's heading starts a new Agent in its folder (#13523).
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { t } from "../../i18n";
import { neverConnected } from "../../lib/add-computer";
import { shortPath } from "../../lib/format";
import { filterQuery, PAGE_SIZE, SessionList, type ListChangeHint, type ListFilters } from "../../lib/session-list";
import { openNewAgent } from "../../lib/new-agent";
import { onHubEvent, useStore } from "../../lib/store";
import { SectionList } from "../../lib/section-list";
import { selectSidebarMachines } from "../../lib/sidebar-machines";
import { computerSections, groupMachines, holdsPlace, JUST_FINISHED_KEY, type ComputerSection, type GroupDimension, type SessionGroup, type SessionPlace } from "../../lib/session-groups";
import { onShownHint } from "../../lib/shown-hints";
import { useFolded } from "../../lib/use-folded";
import { nestUnderParents, type StableRowOrder } from "../../lib/side-sessions";
import { useHeldOrder } from "../../lib/sidebar-engaged";
import { Button, Collapse, cx, sidebarFoldMotion } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { STATE_LABEL, stateName } from "../../lib/state-labels";
import { SESSION_LIST_METRICS, SESSION_LIST_STYLE } from "../../ui/list-metrics";
import { SectionAdd } from "../SectionAdd";
import { GroupCount, NeedsCount, Section, ShowMoreRow, SidebarGroup } from "../SidebarSection";
import { ClearSeenButton } from "./SeenRowsNotice";

/** Held-seen rows kept in their state sections during a visit (#12585): merged into open sections and offered to Clear seen. */
export interface HeldSeenGroups<T> {
  rows: readonly T[];
  stay: boolean;
  sectionOf: (row: T) => string | undefined;
  onClear: (rows: readonly T[]) => void;
  onRows?: (rows: readonly T[]) => void;
}

/** How a section's own list is read: the collection the groups are of, as the list above them reads it. */
export interface GroupedSource<T> {
  path: string;
  identity: (row: T) => string;
  compare: (a: T, b: T) => number;
  removeIdentity?: (id: string) => string;
  event: "sessions_list" | "agents";
  summary: boolean;
  roots: boolean;
  crossMachineHierarchy?: boolean;
  /** The list's own parentOf (#5935): a section draws a side session under its parent as the flat list does. */
  parentOf?: (row: T) => string | undefined;
  /** Stable structural placement, separate from the transport's cursor order (#11801). */
  rowOrder?: StableRowOrder<T>;
  /** Keep an open section's rows in the order they are drawn in while the sidebar is in use (the activity order). */
  hold?: boolean;
  /** Row identities excluded from rendered section rows (e.g. in-flight launches shown under Starting). */
  excludeIdentities?: ReadonlySet<string>;
}

export interface GroupedSessionsProps<T> {
  groups: SessionGroup[];
  dimension: GroupDimension;
  /** What the open sections are remembered under in this browser: one list's groups are not another's. */
  kind: string;
  /** The search, the provider and the state: what every section's sessions are narrowed by. */
  scope: ListFilters;
  source: GroupedSource<T>;
  renderRow: (row: T, within: GroupDimension, group: SessionGroup) => ReactNode;
  groupName: (group: SessionGroup) => string;
  /** Drawn before a group's name in its heading (the personal list's account marks); `groupTip` is the name's tooltip. */
  groupLead?: (group: SessionGroup) => ReactNode;
  groupTip?: (group: SessionGroup) => string | undefined;
  machineName: (id: string) => string;
  /** Where the open conversation is: its section opens by itself. */
  open?: SessionPlace;
  /** The sections follow this scope's "Collapse all" / "Expand all" (the sidebar's Agents title row, #6662); without
   *  it they keep out of it, as an expanded one reads its sessions. */
  foldScope?: string;
  /** By space, with sessions on several computers: each computer's spaces under its heading (#13522), the computers in
   *  `machineOrder`, the hub's. */
  byComputer?: boolean;
  machineOrder?: readonly string[];
  /** Rows read during this visit kept in place (#12585). */
  heldSeen?: HeldSeenGroups<T>;
  /** Where a row lives in a grouping (used to match held-seen rows to non-state sections under the Unread filter). */
  placeOf?: (row: T) => SessionPlace;
  justFinished?: boolean;
  chipless?: boolean;
}

const NO_HELD: readonly never[] = [];
/** Keep concurrent open-section fetches below HTTP/1.1's 6-connection origin limit so active-route APIs and static chunks stay unblocked. */
const MAX_CONCURRENT_SECTION_LOADS = 2;

interface QueuedSectionLoad {
  priority: number;
  signal: AbortSignal;
  run: () => Promise<void>;
  resolve: () => void;
  reject: (err: unknown) => void;
  cleanup: () => void;
}

let activeSectionLoads = 0;
const queuedSectionLoads: QueuedSectionLoad[] = [];

function drainSectionLoads(): void {
  while (activeSectionLoads < MAX_CONCURRENT_SECTION_LOADS && queuedSectionLoads.length > 0) {
    let best = 0;
    for (let i = 1; i < queuedSectionLoads.length; i++) {
      if (queuedSectionLoads[i]!.priority > queuedSectionLoads[best]!.priority) best = i;
    }
    const next = queuedSectionLoads.splice(best, 1)[0]!;
    next.cleanup();
    if (next.signal.aborted) {
      next.resolve();
      continue;
    }
    activeSectionLoads++;
    void next.run().then(next.resolve, next.reject).finally(() => {
      activeSectionLoads--;
      drainSectionLoads();
    });
  }
}

function scheduleSectionLoad(priority: number, run: () => Promise<void>, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.resolve();
  if (activeSectionLoads < MAX_CONCURRENT_SECTION_LOADS && queuedSectionLoads.length === 0) {
    activeSectionLoads++;
    return run().finally(() => {
      activeSectionLoads--;
      drainSectionLoads();
    });
  }
  return new Promise<void>((resolve, reject) => {
    const onAbort = () => {
      const idx = queuedSectionLoads.indexOf(entry);
      if (idx >= 0) queuedSectionLoads.splice(idx, 1);
      entry.cleanup();
      resolve();
    };
    const entry: QueuedSectionLoad = {
      priority,
      signal,
      run,
      resolve,
      reject,
      cleanup: () => signal.removeEventListener("abort", onAbort),
    };
    signal.addEventListener("abort", onAbort, { once: true });
    queuedSectionLoads.push(entry);
    drainSectionLoads();
  });
}

type SectionProps<T> = Pick<GroupedSessionsProps<T>, "dimension" | "kind" | "scope" | "source" | "renderRow" | "groupName" | "groupLead" | "groupTip" | "machineName" | "open" | "foldScope" | "heldSeen" | "placeOf" | "justFinished" | "chipless">;
type SidebarMachine = ReturnType<typeof selectSidebarMachines>[number];

export function GroupedSessions<T>({ groups, byComputer = false, machineOrder, ...p }: GroupedSessionsProps<T>) {
  return (
    <div data-session-groups={p.dimension}>
      {byComputer ? <Computers groups={groups} machineOrder={machineOrder} {...p} /> : <GroupList groups={groups} {...p} />}
    </div>
  );
}

/** A space's New agent (#13523): the one New agent sheet, on the space's computer and in its folder, with that
 *  computer's usual launcher. A runtime space only (an account is no place to start one), on a computer that is online,
 *  whose folder the hub says (the catalog's `path`); not No project, which has none. */
function startIn(group: SessionGroup, kind: string, dimension: GroupDimension, machines: readonly SidebarMachine[]): (() => void) | undefined {
  const place = group.places[0];
  if (kind !== "runtime" || dimension !== "group" || group.rest || !place?.path) return undefined;
  if (!machines.some((m) => m.id === place.machine && m.online)) return undefined;
  return () => openNewAgent({ machineId: place.machine, cwd: place.path });
}

/** A run of sections, a bounded number of them at first (a computer's spaces, or the whole list). */
function GroupList<T>({ groups, onExcluded, dimension, kind, scope, source, renderRow, groupName, groupLead, groupTip, machineName, open, foldScope, heldSeen, placeOf, justFinished = false, chipless = false, nested = false }: SectionProps<T> & { groups: readonly SessionGroup[]; onExcluded?: (key: string, n: number) => void; nested?: boolean }) {
  const machines = useStore(selectSidebarMachines);
  const [all, setAll] = useState(false);
  const adjusted = useMemo(() => {
    if (dimension !== "state" || !heldSeen?.rows.length || (!chipless && scope.state !== "unread")) return groups;
    const unreadHeld = heldSeen.rows.filter((r) => heldSeen.sectionOf(r) === "unread").length;
    const finishedHeld = justFinished && chipless ? heldSeen.rows.filter((r) => heldSeen.sectionOf(r) === JUST_FINISHED_KEY).length : 0;
    const heldTotal = unreadHeld + finishedHeld;
    if (!heldTotal) return groups;
    const next = groups.flatMap((g) => {
      if (g.key !== "idle") return [g];
      const count = Math.max(0, g.count - heldTotal);
      return count > 0 ? [{ ...g, count }] : [];
    });
    if (unreadHeld > 0 && !next.some((g) => g.key === "unread")) {
      const at = next.findIndex((g) => g.key !== "needs_you" && g.key !== "working");
      const item: SessionGroup = { key: "unread", name: STATE_LABEL.unread, count: 0, needs: 0, places: [], filters: { state: "unread" } };
      if (at < 0) next.push(item); else next.splice(at, 0, item);
    }
    if (finishedHeld > 0 && !next.some((g) => g.key === JUST_FINISHED_KEY)) {
      const at = next.findIndex((g) => g.key === "idle" || g.key === "offline" || g.key === "unknown");
      const item: SessionGroup = { key: JUST_FINISHED_KEY, name: stateName("just_finished"), count: 0, needs: 0, places: [], filters: { state: "idle", just_finished: "1" } };
      if (at < 0) next.push(item); else next.splice(at, 0, item);
    }
    return next;
  }, [groups, dimension, heldSeen, chipless, scope.state, justFinished]);
  const shown = all ? adjusted : adjusted.slice(0, SESSION_LIST_METRICS.groupOptions);
  const opens = (group: SessionGroup, i: number) => dimension === "todo" ? !group.rest && (i < SESSION_LIST_METRICS.groupsOpen || group.needs > 0) : i < SESSION_LIST_METRICS.groupsOpen || (dimension === "state" && group.key === JUST_FINISHED_KEY);
  const hideIdleIds = useMemo(() => (dimension === "state" && heldSeen?.rows.length ? new Set(heldSeen.rows.map(source.identity)) : undefined), [dimension, heldSeen, source.identity]);
  const several = !nested && groupMachines(adjusted) > 1;
  return <>
    {shown.map((group, i) => {
      const sectionHeld = !heldSeen?.rows.length ? (NO_HELD as readonly T[])
        : dimension === "state" ? heldSeen.rows.filter((r) => heldSeen.sectionOf(r) === group.key)
        : scope.state === "unread" && placeOf ? heldSeen.rows.filter((r) => holdsPlace(group, dimension, placeOf(r)))
        : (NO_HELD as readonly T[]);
      const hideIds = dimension === "state" && group.key === "idle" ? hideIdleIds : undefined;
      return <GroupSection key={group.key} group={group} first={i === 0} startsOpen={sectionHeld.length > 0 || opens(group, i)} dimension={dimension} kind={kind} scope={scope} source={source}
        renderRow={renderRow} name={groupName(group)} lead={groupLead?.(group)} tip={groupTip?.(group)} machineName={machineName} several={several}
        holds={Boolean((open && holdsPlace(group, dimension, open)) || (dimension === "todo" && !group.rest && group.needs > 0))} foldScope={foldScope} onExcluded={onExcluded}
        heldRows={sectionHeld} hideIds={hideIds} stay={Boolean(heldSeen?.stay)} onClearSeen={heldSeen?.onClear} onRows={heldSeen?.onRows}
        start={startIn(group, kind, dimension, machines)} />;
    })}
    {adjusted.length > shown.length && (
      <ShowMoreRow label={t("paged-agents.more_groups", { count: adjusted.length - shown.length })} onClick={() => setAll(true)} />
    )}
  </>;
}

/** By space on several computers (#13522, as iOS's By Project): a section per computer, in the hub's order, with its
 *  spaces in it and its sessions in no space last. */
function Computers<T>({ groups, machineOrder, ...p }: SectionProps<T> & { groups: readonly SessionGroup[]; machineOrder?: readonly string[] }) {
  const machines = useStore(selectSidebarMachines);
  const sections = useMemo(() => computerSections(groups, machineOrder), [groups, machineOrder]);
  return <>{sections.map((section, i) => (
    <ComputerGroup key={section.machine} section={section} first={i === 0} machine={machines.find((m) => m.id === section.machine)} {...p} />
  ))}</>;
}

/** A computer's heading, as the complete list's By machine has it: its online state, its name, then how many of its
 *  sessions need the user and how many it holds (the sums of its spaces'). A computer that is away starts folded. Its
 *  spaces sit a level in; the first few of them start open, as a list's first sections do. */
function ComputerGroup<T>({ section, first, machine, ...p }: SectionProps<T> & { section: ComputerSection; first: boolean; machine?: SidebarMachine }) {
  const { dimension, kind, open, foldScope } = p;
  const online = Boolean(machine?.online);
  const holds = Boolean(open && section.groups.some((g) => holdsPlace(g, dimension, open)));
  const [folded, toggle] = useFolded(`sessions-by:${kind}:${dimension}:computer:${section.machine}`, !online, { holds, all: Boolean(foldScope), scope: foldScope });
  // the rows an open space leaves out (in-flight starts) are left out of the computer's number too
  const [excluded, setExcluded] = useState<Record<string, number>>({});
  const onExcluded = useCallback((key: string, n: number) => setExcluded((was) => (was[key] ?? 0) === n ? was : { ...was, [key]: n }), []);
  const left = section.groups.reduce((n, g) => n + (excluded[g.key] ?? 0), 0);
  const count = Math.max(0, section.count - left);
  const state = online ? t("sidebar.online") : machine && neverConnected(machine) ? t("sidebar.not_connected_yet") : t("sidebar.offline");
  return (
    <div data-session-computer={section.machine} data-group-open={!folded}>
      <SidebarGroup className={first ? "mt-1" : "mt-3"} heading={
        <div className="group/sec relative flex select-none items-center gap-1 px-2 pb-1">
          <button type="button" aria-expanded={!folded} onClick={toggle} className="touch-hit relative z-10 flex min-w-0 flex-1 items-center gap-1.5 whitespace-nowrap text-left">
            <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", online ? "bg-green" : "bg-line-strong")} />
            <span className={cx("min-w-0 truncate text-label font-semibold", online ? "text-ink" : "text-ink-3")}>{p.machineName(section.machine)}</span>
            <span className="sr-only">{state}</span>
            <Icon name="chevron" size={10} stroke={2.4} className="shrink-0 text-ink-3 transition-transform" style={{ ...sidebarFoldMotion(!folded), transform: folded ? "rotate(-90deg)" : "none" }} />
          </button>
          {!online && <span aria-hidden className="shrink-0 whitespace-nowrap text-submeta text-ink-3">{state}</span>}
          <NeedsCount n={section.needs} />
          <GroupCount n={count} label={t("paged-agents.count", { count })} />
        </div>
      }>
        <Collapse open={!folded} sidebarFold unclipped>
          <div className="sidebar-indent" inert={folded || undefined}>
            <GroupList groups={section.groups} {...p} onExcluded={onExcluded} nested />
          </div>
        </Collapse>
      </SidebarGroup>
    </div>
  );
}

/** A space heading's + (#13523), as the complete list's space ⋯ shows: on hover with a pointer and on keyboard focus,
 *  always on a touch screen. The heading's numbers step aside for it then, rather than being covered. */
const REVEALED = "opacity-0 transition-opacity duration-150 group-hover/sec:opacity-100 focus-visible:opacity-100 [@media(hover:none)]:opacity-100";
const STEP_ASIDE = "transition-transform duration-150 group-hover/sec:-translate-x-6 group-has-[[data-space-add]_:focus-visible]/sec:-translate-x-6 [@media(hover:none)]:mr-6";

function GroupSection<T>({ group, first, startsOpen, dimension, kind, scope, source, renderRow, name, lead, tip, machineName, several, holds, foldScope, onExcluded, start, heldRows, hideIds, stay, onClearSeen, onRows }: {
  group: SessionGroup; first: boolean; startsOpen: boolean; dimension: GroupDimension; kind: string; scope: ListFilters; source: GroupedSource<T>;
  renderRow: (row: T, within: GroupDimension, group: SessionGroup) => ReactNode; name: string; lead?: ReactNode; tip?: string; machineName: (id: string) => string; several: boolean; holds: boolean; foldScope?: string;
  /** Hears how many of its rows it leaves out while open (a computer's heading counts without them too). */
  onExcluded?: (key: string, n: number) => void;
  /** Its heading's + (#13523): a new Agent where its sessions are. */
  start?: () => void;
  heldRows: readonly T[]; hideIds?: ReadonlySet<string>; stay: boolean; onClearSeen?: (rows: readonly T[]) => void; onRows?: (rows: readonly T[]) => void;
}) {
  // The first few start open, the rest folded: an open section keeps a list of its own up to date. "Expand all"
  // leaves them alone for the same reason (it would start one read per group), except in the sidebar, whose title row
  // folds and opens them as the complete list's groups (`foldScope`, #6662): someone asked. What is folded is kept.
  const [folded, toggle] = useFolded(`sessions-by:${kind}:${dimension}:${group.key}`, !startsOpen, { holds, all: Boolean(foldScope), scope: foldScope });
  const [excluded, setExcluded] = useState(0);
  const left = folded ? 0 : excluded;
  const shownCount = Math.max(0, group.count - left);
  const key = group.key;
  useEffect(() => {
    if (!onExcluded) return;
    onExcluded(key, left);
    return () => onExcluded(key, 0);
  }, [onExcluded, key, left]);
  // a space whose name another of its computer's spaces has says where it is (#13522); without computer headings, a
  // space or a directory names its computer only when the groups are on several, and a project only when all of its
  // directories are on that one (a repository checked out on two computers is not either's, #16877)
  const on = [...new Set(group.places.map((place) => place.machine).filter(Boolean))];
  const computer = several && on.length === 1 ? machineName(on[0]) : undefined;
  const title = <>{name}{group.qualifier ? <span className="font-normal"> · {group.qualifier}</span> : computer && <span className="font-normal"> · {computer}</span>}</>;
  return (
    <div data-session-group={group.key} data-group-open={!folded}>
      <Section first={first} folded={folded} onToggle={toggle}
        title={lead || tip ? <span className="flex min-w-0 items-center gap-1.5">{lead}<span className="min-w-0 truncate" title={tip}>{title}</span></span> : title}
        right={<>
          {/* the numbers step aside for the + where it shows: under the pointer, on keyboard focus, always on a touch screen */}
          <span className={cx("flex shrink-0 items-center gap-1", start && STEP_ASIDE)}>
            {dimension === "state" && stay && onClearSeen && heldRows.length > 0 && (
              <ClearSeenButton count={heldRows.length} onClear={() => onClearSeen(heldRows)} />
            )}
            <NeedsCount n={group.needs} />
            <GroupCount n={shownCount} label={t("paged-agents.count", { count: shownCount })} />
          </span>
          {start && <span data-space-add className="absolute top-0 right-2 bottom-1 z-10 flex items-center">
            <SectionAdd label={t("agents-groupedsessions.new_agent_in", { name: group.qualifier ? `${name} · ${group.qualifier}` : name })} onClick={start} className={REVEALED} />
          </span>}
        </>}>
        {!folded && <>
          {dimension === "project" && <GroupPlaces group={group} machineName={machineName} />}
          <GroupRows group={group} dimension={dimension} scope={scope} source={source} renderRow={renderRow} onExcluded={setExcluded} heldRows={heldRows} hideIds={hideIds} onRows={onRows} priority={group.needs > 0 || holds ? 1 : 0} />
        </>}
      </Section>
    </div>
  );
}

/** A project's remote and its directories, each with its computer: which checkouts the sessions below are in. */
function GroupPlaces({ group, machineName }: { group: SessionGroup; machineName: (id: string) => string }) {
  if (!group.origin && !group.places.length) return null;
  return (
    <ul aria-label={t("agents-groupedsessions.places")} className="flex flex-col gap-0.5 px-2 pb-1 text-xs text-ink-3">
      {group.origin && (
        <li className="flex min-w-0 items-center gap-1.5">
          <Icon name="git" size={11} className="shrink-0" />
          <span className="min-w-0 truncate" title={group.origin}>{group.origin}</span>
        </li>
      )}
      {group.places.map((place) => {
        const where = [place.machine && machineName(place.machine), shortPath(place.path)].filter(Boolean).join(" · ");
        return (
          <li key={`${place.machine}\n${place.path}`} className="flex min-w-0 items-center gap-1.5">
            <Icon name="folder" size={11} className="shrink-0" />
            <span className="min-w-0 flex-1 truncate" title={[place.machine && machineName(place.machine), place.path].filter(Boolean).join(" · ")}>{where}</span>
            {group.places.length > 1 && <GroupCount n={place.count} label={t("paged-agents.count", { count: place.count })} className="shrink-0" />}
          </li>
        );
      })}
    </ul>
  );
}

/** An open section's sessions: the list's own rows in presentation order. A few at first, then a page at a time. */
function GroupRows<T>({ group, dimension, scope, source, renderRow, onExcluded, heldRows, hideIds, onRows, priority }: {
  group: SessionGroup; dimension: GroupDimension; scope: ListFilters; source: GroupedSource<T>; renderRow: (row: T, within: GroupDimension, group: SessionGroup) => ReactNode; onExcluded?: (n: number) => void;
  heldRows: readonly T[]; hideIds?: ReadonlySet<string>; onRows?: (rows: readonly T[]) => void; priority: number;
}) {
  const { path, identity, compare, removeIdentity, event, summary, roots, crossMachineHierarchy, parentOf, rowOrder, excludeIdentities, hold = false } = source;
  // rows only: the section's numbers are the ones its heading already has. A runtime section passes over the hints that
  // name none of its rows (#9176): with several open, each read for every agent's update anywhere.
  const follows = event === "sessions_list";
  const priorityRef = useRef(priority);
  priorityRef.current = priority;
  const list = useMemo(() => new (follows ? SectionList : SessionList)<T>(path, identity, compare, undefined, removeIdentity, {
    summary, roots, crossMachineHierarchy, counts: false, presentation: rowOrder ? { ...rowOrder, parentOf } : undefined,
    schedule: (run, signal) => scheduleSectionLoad(priorityRef.current, run, signal),
  }), [path, identity, compare, removeIdentity, summary, roots, crossMachineHierarchy, follows, rowOrder, parentOf]);
  // the groups are built again with every read: the list follows what they select, not which object says it
  const selected = filterQuery({ ...scope, ...group.filters }, {});
  const filters = useMemo(() => ({ ...scope, ...group.filters }), [selected]); // eslint-disable-line react-hooks/exhaustive-deps
  const page = useSyncExternalStore(list.subscribe, list.getSnapshot, list.getSnapshot);
  useEffect(() => {
    if (page.rows.length) onRows?.(page.rows);
  }, [page.rows, onRows]);
  const heading = `${group.count}/${group.needs}`;
  const countAtStart = useRef(group.count);
  const headingRead = useRef<string | null>(null);
  useEffect(() => {
    countAtStart.current = group.count;
    headingRead.current = null;
    void list.setFilters(filters).catch(() => {});
    // the hub's message for the hint onShownHint acts on (registered first, so it is the one the hint is for); after a
    // hidden stretch pass no hint so SectionList always reconciles instead of skipping a chain across hidden time
    let hint: ListChangeHint | undefined;
    const offHint = onHubEvent(event, (msg) => { hint = msg; });
    const off = onShownHint(event, (replayed) => list.invalidate(replayed ? undefined : hint));
    return () => { offHint(); off(); list.reset(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- countAtStart captures the group count at filter start; later heading changes run the effect below
  }, [list, filters, event]);
  // What no hint names, a session joining the section, shows in its heading's numbers (the read that named the groups):
  // when they change, the section reads. If the group's count changes while the first read is still in flight, catch up
  // once `page.loaded` flips true (without treating the initial async `needs` badge fetch as a stale read).
  useEffect(() => {
    if (!page.loaded) return;
    const prev = headingRead.current;
    headingRead.current = heading;
    if (prev === null) {
      if (group.count !== countAtStart.current) list.invalidate();
      return;
    }
    if (prev !== heading) list.invalidate();
  }, [list, heading, group.count, page.loaded]);
  const [limit, setLimit] = useState<number>(SESSION_LIST_METRICS.groupPreview);
  const unheldRows = useMemo(
    () => excludeIdentities?.size ? page.rows.filter((row) => !excludeIdentities.has(identity(row))) : page.rows,
    [page.rows, excludeIdentities, identity],
  );
  const excluded = page.rows.length - unheldRows.length;
  const shownBase = useMemo(() => {
    const base = hideIds?.size ? unheldRows.filter((row) => !hideIds.has(identity(row))) : unheldRows;
    if (!heldRows.length) return base;
    const seenIds = new Set(base.map(identity));
    const extras = heldRows.filter((row) => !seenIds.has(identity(row)) && (!excludeIdentities?.size || !excludeIdentities.has(identity(row))));
    if (!extras.length) return base;
    return [...base, ...extras].sort(rowOrder?.compare ?? compare);
  }, [unheldRows, hideIds, heldRows, excludeIdentities, identity, rowOrder, compare]);
  const visibleRows = useHeldOrder(shownBase, identity, hold) as T[];
  useEffect(() => {
    onExcluded?.(excluded);
    return () => onExcluded?.(0);
  }, [onExcluded, excluded]);
  const preview = visibleRows.length > limit ? visibleRows.slice(0, limit) : visibleRows;
  // a session started from another one shown here comes under it, a level in (#5935), as in the flat list
  const sourceRows = rowOrder ? visibleRows : preview;
  const nesting = useMemo(() => parentOf ? nestUnderParents(sourceRows, identity, parentOf) : null, [sourceRows, identity, parentOf]);
  // The stable list retains displaced rows and sorts every admitted page by current structural placement.
  // Other lists keep the transport's newest-first order (#11801).
  const arranged = nesting?.rows ?? sourceRows;
  const rows = arranged.length > limit ? arranged.slice(0, limit) : arranged;
  const more = () => {
    if (visibleRows.length <= limit) void list.more().catch(() => {});
    setLimit(limit + PAGE_SIZE);
  };
  return <>
    {rows.map((row) => <div key={identity(row)} data-session-row="">
      {nesting?.nested.has(row) ? <div data-nested-session className={SESSION_LIST_STYLE.nestedRow}>{renderRow(row, dimension, group)}</div> : renderRow(row, dimension, group)}
    </div>)}
    {page.loading && (!page.loaded || page.loadingMore) && !rows.length && <p role="status" className="px-2 py-1 text-xs text-ink-3">{t("sidebar.loading")}</p>}
    {page.error && <div className="px-2 py-1"><p role="alert" className="text-xs text-red">{page.error}</p><Button onClick={() => void list.retry().catch(() => {})}>{t("sidebar.retry")}</Button></div>}
    {page.loaded && !page.loading && !page.error && !visibleRows.length && <p className="px-2 py-1 text-xs text-ink-3">{t("sidebar.no_match")}</p>}
    {/* not hidden by a background refresh (only `loading`): on a busy fleet one is nearly always out, and the button
        vanished under the pointer (#9215). A click during one queues its page. */}
    {(visibleRows.length > limit || page.hasMore) && !page.loadingMore && !page.error && <ShowMoreRow label={t("paged-agents.more")} onClick={more} />}
  </>;
}
