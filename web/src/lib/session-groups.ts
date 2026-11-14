// The groups the paged Agents list is browsed by (computer, project, space or account, and the sidebar's to-do and
// state, #6662): what each is called, how many sessions it holds and which filter lists them. A hub with `sessions_catalog` names projects and spaces itself
// (docs/protocol/protocol.md "Projects and spaces"): a repository checked out on several computers is one project with
// a place for each directory, and a space is one computer's. Without it the groups are the count maps' keys, as before.
import { t } from "../i18n";
import { baseName } from "./format";
import { LIST_PAIRS_MAX, listPair, type ListCounts, type ListFilters } from "./session-list";
import { STATE_LABEL } from "./state-labels";
import type { AgentState } from "./types";

export const SESSIONS_CATALOG_FEATURE = "sessions_catalog";
/** The hub lists and counts sessions by open to-do (`todo=<id>` / `todo=none`, docs/protocol/protocol.md "Sessions by
 *  to-do", #6662). */
export const SESSIONS_TODO_FEATURE = "sessions_todo";
/** The hub selects the sidebar's quick filters on the paged list: `waiting=0|1` and `exclude_pane_source=<origin>`
 *  (docs/protocol/protocol.md "Quick filters", #6662). */
export const SESSIONS_QUICK_FILTERS_FEATURE = "sessions_quick_filters";

export type GroupDimension = "machine" | "project" | "group" | "state" | "todo";

/** The states a list by state has sections for, in the quick filters' order: the ones that need someone first. */
const STATE_ORDER = ["needs_you", "working", "unread", "idle", "offline", "unknown"];

/** What a list by to-do knows of the to-dos beyond the hub's counts: their order (the To-dos page's) and titles. */
export interface TodoNames { order: readonly string[]; title: (id: string) => string | undefined }

// needs_you: how many of them need the user, left out when none do
export interface CatalogPlace { machine_id: string; path: string; total: number; needs_you?: number }
export interface CatalogProject { id: string; name: string; origin?: string; total: number; needs_you?: number; places: CatalogPlace[] }
export interface CatalogSpace { machine_id: string; id: string; name: string; total: number; needs_you?: number }
export interface SessionCatalog {
  revision: string;
  total: number;
  /** Some directories' computers were still being asked what repository they hold: read again a moment later. */
  pending?: boolean;
  projects: CatalogProject[];
  spaces: CatalogSpace[];
}

const record = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null;
const count = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
const optionalCount = (v: unknown) => v === undefined || count(v);

/** A hub that ignores `catalog=1` (an older one) answers with a page: never taken for a catalog. */
export function validCatalog(v: unknown): v is SessionCatalog {
  if (!record(v) || typeof v.revision !== "string" || !count(v.total) || !Array.isArray(v.projects) || !Array.isArray(v.spaces)) return false;
  return v.projects.every((p: unknown) => record(p) && typeof p.id === "string" && typeof p.name === "string" && count(p.total) && optionalCount(p.needs_you) &&
      (p.origin === undefined || typeof p.origin === "string") && Array.isArray(p.places) && p.places.length > 0 &&
      p.places.every((x: unknown) => record(x) && typeof x.machine_id === "string" && x.machine_id !== "" && typeof x.path === "string" && count(x.total))) &&
    v.spaces.every((s: unknown) => record(s) && typeof s.machine_id === "string" && s.machine_id !== "" && typeof s.id === "string" &&
      typeof s.name === "string" && count(s.total) && optionalCount(s.needs_you));
}

/** One directory of a project, or the computer of a space (no path). */
export interface GroupPlace { machine: string; path: string; count: number }

export interface SessionGroup {
  /** What the value pill holds and what a section is remembered by; unique within its dimension. */
  key: string;
  name: string;
  count: number;
  /** How many of its sessions need the user; 0 when none do or it was not read. */
  needs: number;
  /** A project's remote as its computer spells it (`host/path`). */
  origin?: string;
  /** A project's directories, most used first, or a space's computer. Empty for a group the hub did not place. */
  places: GroupPlace[];
  /** The sessions with no folder or in no space: listed after the others. */
  rest?: boolean;
  /** The machine and value pairs its sessions have (`holdsPlace`); absent when the key itself is the value. */
  members?: string[];
  /** What lists its sessions, on top of the search and the state. */
  filters: ListFilters;
}

// A key no id, path or pair can be: the group of the rows without the attribute.
const REST_KEY = "\u0000rest";

/** The projects or spaces of a hub's catalog, each with how many of its sessions need the user, as the hub counted. */
export function catalogGroups(catalog: SessionCatalog, dimension: "project" | "group"): SessionGroup[] {
  if (dimension === "project") {
    return catalog.projects.filter((p) => p.total > 0).map((p): SessionGroup => {
      const members = p.places.map((x) => listPair(x.machine_id, x.path));
      return {
        key: p.id || REST_KEY, name: p.id ? p.name : t("paged-agents.no_folder"), count: p.total, needs: p.needs_you ?? 0,
        // the hub takes a bounded number of places: the busiest ones of a folder name with more
        origin: p.origin || undefined, rest: !p.id || undefined, members, filters: { place: members.slice(0, LIST_PAIRS_MAX) },
        places: p.id ? p.places.map((x) => ({ machine: x.machine_id, path: x.path, count: x.total })) : [],
      };
    });
  }
  const groups: SessionGroup[] = [];
  // the computers' sessions in no space are one group: nobody looks for them by computer
  const rest: SessionGroup = { key: REST_KEY, name: t("paged-agents.no_space"), count: 0, needs: 0, places: [], rest: true, members: [], filters: {} };
  for (const s of catalog.spaces) {
    if (s.total <= 0) continue;
    const pair = listPair(s.machine_id, s.id);
    if (s.id) {
      groups.push({ key: pair, name: s.name || s.id, count: s.total, needs: s.needs_you ?? 0, members: [pair], filters: { space: [pair] },
        places: [{ machine: s.machine_id, path: "", count: s.total }] });
    } else {
      rest.count += s.total;
      rest.needs += s.needs_you ?? 0;
      rest.members!.push(pair);
    }
  }
  if (rest.count > 0) groups.push({ ...rest, filters: { space: rest.members!.slice(0, LIST_PAIRS_MAX) } });
  return groups;
}

/** The groups of a counts read: computers on any hub, and on a hub without the catalog (or for personal agents)
 * projects by path and spaces or accounts by id. The busiest first. A computer, a space id and an account are named by
 * their key: the list has their names. `machine` narrows a space id to one computer. */
export function countGroups(counts: ListCounts | null, dimension: GroupDimension, o: { machine?: string; needs?: ListCounts | null; todos?: TodoNames } = {}): SessionGroup[] {
  // states and to-dos have orders of their own
  if (dimension === "state") return stateGroups(counts);
  if (dimension === "todo") return todoGroups(counts, o.todos, o.needs?.todo ?? {});
  const waiting = o.needs?.[dimension] ?? {};
  return Object.entries(counts?.[dimension] ?? {}).filter(([key, n]) => key && n > 0)
    .sort(([a, x], [b, y]) => y - x || a.localeCompare(b))
    .map(([key, n]): SessionGroup => ({
      key, count: n, needs: waiting[key] ?? 0,
      name: dimension === "project" ? baseName(key) || key : key,
      places: dimension === "project" && baseName(key) !== key ? [{ machine: "", path: key, count: n }] : [],
      filters: dimension === "machine" ? { machine_id: key } : dimension === "project" ? { project: key }
        : { group: key, ...(o.machine ? { machine_id: o.machine } : {}) },
    }));
}

/** A list by state (#6662): a section per state with sessions, in the quick filters' order. Its own number is the
 *  needs-you count, so no badge beside it. */
function stateGroups(counts: ListCounts | null): SessionGroup[] {
  const by = counts?.state ?? {};
  return STATE_ORDER.filter((s) => (by[s] ?? 0) > 0)
    .map((s): SessionGroup => ({ key: s, name: STATE_LABEL[s as AgentState] ?? s, count: by[s]!, needs: 0, places: [], filters: { state: s } }));
}

/** A list by to-do (#6662): a section per open to-do with sessions, those with sessions that need the user first, then
 *  the To-dos page's order; one the page does not know (another assistant's, not loaded yet) after them; the sessions on
 *  none last, as "Not on a to-do" (`rest`). */
function todoGroups(counts: ListCounts | null, todos: TodoNames | undefined, waiting: Record<string, number>): SessionGroup[] {
  const by = counts?.todo ?? {};
  const rank = new Map((todos?.order ?? []).map((id, i) => [id, i]));
  const place = (id: string) => rank.get(id) ?? rank.size;
  const groups = Object.entries(by).filter(([id, n]) => id && n > 0)
    .sort(([a], [b]) => Number((waiting[b] ?? 0) > 0) - Number((waiting[a] ?? 0) > 0) || place(a) - place(b) || a.localeCompare(b))
    .map(([id, n]): SessionGroup => ({ key: id, name: todos?.title(id) || t("lib-agent-groups.seg_todo"), count: n, needs: waiting[id] ?? 0, places: [], filters: { todo: id } }));
  const none = by[""] ?? 0;
  if (none > 0) groups.push({ key: REST_KEY, name: t("lib-todos.unassigned"), count: none, needs: waiting[""] ?? 0, places: [], rest: true, filters: { todo: "none" } });
  return groups;
}

/** Where a session is: its computer, its project directory and its space, as the hub groups by them; its open to-do
 *  ("" for none) and its state for the sidebar's lists by to-do and by state. */
export interface SessionPlace { machine: string; project: string; group: string; todo?: string; state?: string }

/** Whether a session at `where` is one of the group's. */
export function holdsPlace(group: SessionGroup, dimension: GroupDimension, where: SessionPlace): boolean {
  if (dimension === "todo") return group.rest ? !where.todo : group.key === where.todo;
  if (dimension === "state") return group.key === where.state;
  const value = dimension === "machine" ? where.machine : dimension === "project" ? where.project : where.group;
  return group.members ? group.members.includes(listPair(where.machine, value)) : group.key === value;
}

/** How many computers the groups' places are on: a space or a directory names its computer only when there are several. */
export function groupMachines(groups: readonly SessionGroup[]): number {
  return new Set(groups.flatMap((g) => g.places.map((p) => p.machine)).filter(Boolean)).size;
}
