// The paged list by group: one folding section for each computer, project, space or account (the sidebar's to-do and
// state too, #6662), with how many sessions it holds and how many of them need the user on its heading. The first few
// sections start open on their newest sessions (by to-do every to-do's, "Not on a to-do" folded);
// a section reads its own sessions while it is open, a page at a time, and stops when it is folded, so the groups of a
// long list cost nothing until someone looks into one.
import { useEffect, useMemo, useState, useSyncExternalStore, type ReactNode } from "react";
import { t } from "../../i18n";
import { shortPath } from "../../lib/format";
import { filterQuery, PAGE_SIZE, SessionList, type ListFilters } from "../../lib/session-list";
import { groupMachines, holdsPlace, type GroupDimension, type SessionGroup, type SessionPlace } from "../../lib/session-groups";
import { onShownHint } from "../../lib/shown-hints";
import { useFolded } from "../../lib/use-folded";
import { nestUnderParents } from "../../lib/side-sessions";
import { Button } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { SESSION_LIST_METRICS, SESSION_LIST_STYLE } from "../../ui/list-metrics";
import { GroupCount, NeedsCount, Section, ShowMoreRow } from "../SidebarSection";

/** How a section's own list is read: the collection the groups are of, as the list above them reads it. */
export interface GroupedSource<T> {
  path: string;
  identity: (row: T) => string;
  compare: (a: T, b: T) => number;
  removeIdentity?: (id: string) => string;
  event: "sessions_list" | "agents";
  summary: boolean;
  roots: boolean;
  /** The list's own parentOf (#5935): a section draws a side session under its parent as the flat list does. */
  parentOf?: (row: T) => string | undefined;
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
  machineName: (id: string) => string;
  /** Where the open conversation is: its section opens by itself. */
  open?: SessionPlace;
  /** The sections follow this scope's "Collapse all" / "Expand all" (the sidebar's Agents title row, #6662); without
   *  it they keep out of it, as an expanded one reads its sessions. */
  foldScope?: string;
}

export function GroupedSessions<T>({ groups, dimension, kind, scope, source, renderRow, groupName, machineName, open, foldScope }: GroupedSessionsProps<T>) {
  const [all, setAll] = useState(false);
  const shown = all ? groups : groups.slice(0, SESSION_LIST_METRICS.groupOptions);
  // a space or a directory names its computer only when the groups are on several
  const several = groupMachines(groups) > 1;
  return (
    <div data-session-groups={dimension}>
      {shown.map((group, i) => (
        <GroupSection key={group.key} group={group} first={i === 0} startsOpen={dimension === "todo" ? !group.rest : i < SESSION_LIST_METRICS.groupsOpen} dimension={dimension} kind={kind} scope={scope} source={source}
          renderRow={renderRow} name={groupName(group)} machineName={machineName} several={several} holds={Boolean(open && holdsPlace(group, dimension, open))} foldScope={foldScope} />
      ))}
      {groups.length > shown.length && (
        <ShowMoreRow label={t("paged-agents.more_groups", { count: groups.length - shown.length })} onClick={() => setAll(true)} />
      )}
    </div>
  );
}

function GroupSection<T>({ group, first, startsOpen, dimension, kind, scope, source, renderRow, name, machineName, several, holds, foldScope }: {
  group: SessionGroup; first: boolean; startsOpen: boolean; dimension: GroupDimension; kind: string; scope: ListFilters; source: GroupedSource<T>;
  renderRow: (row: T, within: GroupDimension, group: SessionGroup) => ReactNode; name: string; machineName: (id: string) => string; several: boolean; holds: boolean; foldScope?: string;
}) {
  // The busiest few start open, the rest folded: an open section keeps a list of its own up to date. "Expand all"
  // leaves them alone for the same reason (it would start one read per group), except in the sidebar, whose title row
  // folds and opens them as the complete list's groups (`foldScope`, #6662): someone asked. What is folded is kept.
  const [folded, toggle] = useFolded(`sessions-by:${kind}:${dimension}:${group.key}`, !startsOpen, { holds, all: Boolean(foldScope), scope: foldScope });
  // a space is one computer's: two computers' spaces of one name are told apart by it
  const computer = dimension === "group" && several && group.places[0]?.machine ? machineName(group.places[0].machine) : "";
  return (
    <div data-session-group={group.key} data-group-open={!folded}>
      <Section first={first} folded={folded} onToggle={toggle}
        title={<>{name}{computer && <span className="font-normal"> · {computer}</span>}</>}
        right={<>
          <NeedsCount n={group.needs} />
          <GroupCount n={group.count} label={t("paged-agents.count", { count: group.count })} />
        </>}>
        {!folded && <>
          {dimension === "project" && <GroupPlaces group={group} machineName={machineName} />}
          <GroupRows group={group} dimension={dimension} scope={scope} source={source} renderRow={renderRow} />
        </>}
      </Section>
    </div>
  );
}

/** A project's remote and its directories, each with its computer: which checkouts the sessions below are in. */
function GroupPlaces({ group, machineName }: { group: SessionGroup; machineName: (id: string) => string }) {
  if (!group.origin && !group.places.length) return null;
  return (
    <ul aria-label={t("paged-agents.places")} className="flex flex-col gap-0.5 px-2 pb-1 text-xs text-ink-3">
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

/** An open section's sessions: the list's own rows, the newest first. A few at first, then a page at a time. */
function GroupRows<T>({ group, dimension, scope, source, renderRow }: {
  group: SessionGroup; dimension: GroupDimension; scope: ListFilters; source: GroupedSource<T>; renderRow: (row: T, within: GroupDimension, group: SessionGroup) => ReactNode;
}) {
  const { path, identity, compare, removeIdentity, event, summary, roots, parentOf } = source;
  // rows only: the section's numbers are the ones its heading already has
  const list = useMemo(() => new SessionList<T>(path, identity, compare, undefined, removeIdentity, { summary, roots, counts: false }),
    [path, identity, compare, removeIdentity, summary, roots]);
  // the groups are built again with every read: the list follows what they select, not which object says it
  const selected = filterQuery({ ...scope, ...group.filters }, {});
  const filters = useMemo(() => ({ ...scope, ...group.filters }), [selected]); // eslint-disable-line react-hooks/exhaustive-deps
  const page = useSyncExternalStore(list.subscribe, list.getSnapshot, list.getSnapshot);
  useEffect(() => {
    void list.setFilters(filters).catch(() => {});
    const off = onShownHint(event, () => list.invalidate());
    return () => { off(); list.reset(); };
  }, [list, filters, event]);
  // the newest few first, so several open sections fit a screen; "Load more" shows what is read, then reads on
  const [limit, setLimit] = useState<number>(SESSION_LIST_METRICS.groupPreview);
  const shown = page.rows.length > limit ? page.rows.slice(0, limit) : page.rows;
  // a session started from another one shown here comes under it, a level in (#5935), as in the flat list
  const nesting = useMemo(() => parentOf ? nestUnderParents(shown, identity, parentOf) : null, [shown, identity, parentOf]);
  const rows = nesting?.rows ?? shown;
  const more = () => {
    if (page.rows.length <= limit) void list.more().catch(() => {});
    setLimit(limit + PAGE_SIZE);
  };
  return <>
    {rows.map((row) => <div key={identity(row)} data-session-row="">
      {nesting?.nested.has(row) ? <div data-nested-session className={SESSION_LIST_STYLE.nestedRow}>{renderRow(row, dimension, group)}</div> : renderRow(row, dimension, group)}
    </div>)}
    {page.loading && !page.loaded && <p role="status" className="px-2 py-1 text-xs text-ink-3">{t("sidebar.loading")}</p>}
    {page.error && <div className="px-2 py-1"><p role="alert" className="text-xs text-red">{page.error}</p><Button onClick={() => void list.retry().catch(() => {})}>{t("sidebar.retry")}</Button></div>}
    {page.loaded && !page.loading && !page.error && !page.rows.length && <p className="px-2 py-1 text-xs text-ink-3">{t("sidebar.no_match")}</p>}
    {(page.rows.length > limit || page.hasMore) && !page.loading && !page.error && <ShowMoreRow label={t("paged-agents.more")} onClick={more} />}
  </>;
}
