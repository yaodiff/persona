// The groups the paged Agents list is browsed by (computer, project, space or account, and the sidebar's to-do and
// state, #6662): what each is called, how many sessions it holds and which filter lists them. A hub with `sessions_catalog` names projects and spaces itself
// (docs/protocol/protocol.md "Projects and spaces"): a repository checked out on several computers is one project with
// a place for each directory, and a space is one computer's. Without it the groups are the count maps' keys, as before.
import { t } from "../i18n";
import { baseName, shortPath, WINDOWS_PATH } from "./format";
import { LIST_PAIRS_MAX, listPair, type ListCounts, type ListFilters } from "./session-list";
import { STATE_LABEL, stateName } from "./state-labels";
import type { AgentState } from "./types";

export const SESSIONS_CATALOG_FEATURE = "sessions_catalog";
/** The hub lists and counts sessions by open to-do (`todo=<id>` / `todo=none`, docs/protocol/protocol.md "Sessions by
 *  to-do", #6662). */
export const SESSIONS_TODO_FEATURE = "sessions_todo";
/** The hub selects the sidebar's quick filters on the paged list: `waiting=0|1` and `exclude_pane_source=<origin>`
 *  (docs/protocol/protocol.md "Quick filters", #6662). */
export const SESSIONS_QUICK_FILTERS_FEATURE = "sessions_quick_filters";
/** The hub lists the Just finished rows apart: `just_finished=0|1` and the counts' `just_finished`
 *  (docs/protocol/protocol.md "Just finished rows", #9751). */
export const SESSIONS_JUST_FINISHED_FEATURE = "sessions_just_finished";

export type GroupDimension = "machine" | "project" | "group" | "state" | "todo";

/** The states a list by state has sections for, in the quick filters' order: the ones that need someone first. */
const STATE_ORDER = ["needs_you", "working", "unread", "idle", "offline", "unknown"];

/** What a list by to-do knows of the to-dos beyond the hub's counts: their order (the To-dos page's) and titles. */
export interface TodoNames { order: readonly string[]; title: (id: string) => string | undefined }

// needs_you: how many of them need the user, left out when none do
export interface CatalogPlace { machine_id: string; path: string; total: number; needs_you?: number }
export interface CatalogProject { id: string; name: string; origin?: string; total: number; needs_you?: number; places: CatalogPlace[] }
/** `path`: the folder its rows are in, from a hub that says it (#13522). */
export interface CatalogSpace { machine_id: string; id: string; name: string; path?: string; total: number; needs_you?: number }
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
      typeof s.name === "string" && (s.path === undefined || typeof s.path === "string") && count(s.total) && optionalCount(s.needs_you));
}

/** One directory of a project, or the computer of a space and its folder ("" when the hub does not say). */
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
  /** What tells it apart from a group of the same name beside it: a space's parent folder, where another space of its
   *  computer has its name (`~/Desktop` for `app` in `~/Desktop/app`, #13522). */
  qualifier?: string;
  /** What lists its sessions, on top of the search and the state. */
  filters: ListFilters;
}

// A key no id, path or pair can be: the group of the rows without the attribute.
const REST_KEY = "\u0000rest";

/** Projects and spaces have a fixed name order; sessions without a place stay last. Counts and activity only
 *  change their badges and rows, never their position (#11801). */
const byGroupPlace = (a: SessionGroup, b: SessionGroup) => Number(Boolean(a.rest)) - Number(Boolean(b.rest)) || a.name.localeCompare(b.name) || a.key.localeCompare(b.key);

/** The folder a path is in, by its computer's own separator ("" for a path with none): `~/Desktop` of `~/Desktop/app`. */
export function parentFolder(path: string): string {
  const windows = WINDOWS_PATH.test(path);
  const trimmed = path.replace(windows ? /[\\/]+$/ : /\/+$/, "");
  const cut = windows ? Math.max(trimmed.lastIndexOf("\\"), trimmed.lastIndexOf("/")) : trimmed.lastIndexOf("/");
  if (cut < 0) return "";
  // a drive's or the root's own folder keeps its separator: `C:\` of `C:\app`, `/` of `/srv`
  return cut === 0 || (windows && /^[A-Za-z]:$/.test(trimmed.slice(0, cut))) ? trimmed.slice(0, cut + 1) : trimmed.slice(0, cut);
}

/** Spaces that share a name on one computer are told apart by their parent folder, where the hub says their folder. */
function qualifySpaces(groups: SessionGroup[]): SessionGroup[] {
  const named = new Map<string, number>();
  const nameKey = (g: SessionGroup) => JSON.stringify([g.places[0]?.machine ?? "", g.name.toLocaleLowerCase()]);
  for (const g of groups) if (!g.rest) named.set(nameKey(g), (named.get(nameKey(g)) ?? 0) + 1);
  return groups.map((g) => {
    const folder = g.places[0]?.path;
    if (g.rest || !folder || (named.get(nameKey(g)) ?? 0) < 2) return g;
    const parent = parentFolder(folder);
    return parent ? { ...g, qualifier: shortPath(parent) } : g;
  });
}

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
    }).sort(byGroupPlace);
  }
  // A space and a computer's sessions in no space (No project) are that computer's: listed under it when the list is
  // on several computers (`computerSections`, #13522), the latter last among its spaces.
  return qualifySpaces(catalog.spaces.filter((s) => s.total > 0).map((s): SessionGroup => {
    const pair = listPair(s.machine_id, s.id);
    return {
      key: pair, name: s.id ? s.name || s.id : t("paged-agents.no_space"), count: s.total, needs: s.needs_you ?? 0, rest: !s.id || undefined,
      members: [pair], filters: { space: [pair] }, places: [{ machine: s.machine_id, path: s.id ? s.path ?? "" : "", count: s.total }],
    };
  })).sort(byGroupPlace);
}

/** One computer's groups in a list by space on several computers (#13522), as iOS's By Project lists them. */
export interface ComputerSection { machine: string; groups: SessionGroup[]; count: number; needs: number }

/** Spaces under their computers: the computers in the hub's order (any it does not list after, by id), each with its
 *  spaces in their fixed order and its sessions in no space last, and its numbers the sum of theirs. */
export function computerSections(groups: readonly SessionGroup[], machineOrder: readonly string[] = []): ComputerSection[] {
  const order = new Map(machineOrder.map((id, i) => [id, i]));
  const byMachine = new Map<string, ComputerSection>();
  for (const g of groups) {
    const machine = g.places[0]?.machine ?? "";
    let section = byMachine.get(machine);
    if (!section) byMachine.set(machine, section = { machine, groups: [], count: 0, needs: 0 });
    section.groups.push(g);
    section.count += g.count;
    section.needs += g.needs;
  }
  const place = (id: string) => order.get(id) ?? order.size;
  return [...byMachine.values()].sort((a, b) => place(a.machine) - place(b.machine) || a.machine.localeCompare(b.machine));
}

/** The groups of a counts read: computers on any hub, and on a hub without the catalog (or for personal agents)
 * projects by path and spaces or accounts by id. A computer, a space id and an account are named by their key: the
 * list has their names. `machine` narrows a space id to one computer. */
export function countGroups(counts: ListCounts | null, dimension: GroupDimension, o: { machine?: string; machineOrder?: readonly string[]; needs?: ListCounts | null; todos?: TodoNames; justFinished?: boolean } = {}): SessionGroup[] {
  // states and to-dos have orders of their own
  if (dimension === "state") return stateGroups(counts, o.justFinished ?? false);
  if (dimension === "todo") return todoGroups(counts, o.todos, o.needs?.todo ?? {});
  const waiting = o.needs?.[dimension] ?? {};
  const machinePlace = new Map((o.machineOrder ?? []).map((id, i) => [id, i]));
  const place = (group: SessionGroup) => machinePlace.get(group.key) ?? machinePlace.size;
  return Object.entries(counts?.[dimension] ?? {}).filter(([key, n]) => key && n > 0)
    .map(([key, n]): SessionGroup => ({
      key, count: n, needs: waiting[key] ?? 0,
      name: dimension === "project" ? baseName(key) || key : key,
      places: dimension === "project" && baseName(key) !== key ? [{ machine: "", path: key, count: n }] : [],
      filters: dimension === "machine" ? { machine_id: key } : dimension === "project" ? { project: key }
        : { group: key, ...(o.machine ? { machine_id: o.machine } : {}) },
    })).sort(dimension === "machine" ? (a, b) => place(a) - place(b) || byGroupPlace(a, b) : byGroupPlace);
}

/** A list by state (#6662): a section per state with sessions, in the quick filters' order. Its own number is the
 *  needs-you count, so no badge beside it. On a hub that lists them apart (`justFinished`), the seen rows whose done
 *  turn nobody looked at have a Just finished section before Seen, whose number leaves them out (#9751). */
function stateGroups(counts: ListCounts | null, justFinished: boolean): SessionGroup[] {
  const by = counts?.state ?? {};
  const finished = Math.min(counts?.just_finished ?? 0, by.idle ?? 0);
  const group = (key: string, name: string, count = 0, filters: ListFilters = { state: key }): SessionGroup[] =>
    count > 0 ? [{ key, name, count, needs: 0, places: [], filters }] : [];
  return STATE_ORDER.flatMap((s) => s === "idle" && justFinished
    ? [...group(JUST_FINISHED_KEY, stateName("just_finished"), finished, { state: s, just_finished: "1" }),
      ...group(s, STATE_LABEL.idle, (by.idle ?? 0) - finished, { state: s, just_finished: "0" })]
    : group(s, STATE_LABEL[s as AgentState] ?? s, by[s]));
}

/** The key of a list by state's Just finished section: what a Just finished session's place says it is in. */
export const JUST_FINISHED_KEY = "just_finished";

/** A list by to-do (#6662): a section per open to-do with sessions, in the To-dos page's order; one the page does not
 *  know (another assistant's, not loaded yet) after them; the sessions on none last, as "Not on a to-do" (`rest`).
 *  Needs-you counts update each heading without moving it (#11801). */
function todoGroups(counts: ListCounts | null, todos: TodoNames | undefined, waiting: Record<string, number>): SessionGroup[] {
  const by = counts?.todo ?? {};
  const rank = new Map((todos?.order ?? []).map((id, i) => [id, i]));
  const place = (id: string) => rank.get(id) ?? rank.size;
  const groups = Object.entries(by).filter(([id, n]) => id && n > 0)
    .sort(([a], [b]) => place(a) - place(b) || a.localeCompare(b))
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
