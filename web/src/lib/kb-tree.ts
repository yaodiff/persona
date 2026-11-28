// The project tree (#12927, docs/assistant/knowledge.md "Project parents and milestones"): knowledge-base projects under
// the parents the user gave them, with their milestone flags, as the knowledge map's "Project tree" view draws them
// (views/knowledge/ProjectTree.tsx). By hierarchy: top projects as branches, sub-projects under them at any depth. By
// tag: a branch per tag, a project under each of its tags. Pure: the list in, the tree, a move's verdict and the map out.
import { t } from "../i18n";
import { type KBEntry } from "./kb";
import { type MapNode, type MapTone } from "./kb-map";

export type MilestoneState = "open" | "accepted" | "not_accepted";

/** A milestone flag on a project (meta.json `milestones`). */
export interface Milestone {
  id: string;
  title: string;
  /** YYYY-MM-DD */
  target?: string;
  state: MilestoneState;
}

/** A project row of `GET /kb/projects`, with the parent it shows under (none at the top level) and its milestones. */
export interface TreeEntry extends KBEntry {
  parent?: string;
  milestones?: Milestone[] | null;
}

/** A project as a tree names it (`tree.parent`, `tree.ancestors`, `tree.children`). */
export interface ProjectRef {
  id: string;
  name: string;
  status?: string;
  one_line?: string;
  updated?: string;
  /** how many projects sit directly under it */
  children?: number;
  milestones?: number;
}

/** `tree` of a project's detail (`GET /kb/projects/{id}`) and of a parent or milestone change's answer. */
export interface ProjectTree {
  parent?: ProjectRef | null;
  /** the way down from the top, the parent last */
  ancestors: ProjectRef[];
  children: ProjectRef[];
  milestones: Milestone[];
  parent_locked?: boolean;
  /** 1 at the top level */
  depth: number;
}

/** Deeper than this the tree says so; the hub does not refuse it. */
export const SUGGESTED_DEPTH = 3;
/** How many of the biggest tags open at first in the tag view. */
const OPEN_TAGS = 8;

export const MILESTONE_STATES: MilestoneState[] = ["open", "accepted", "not_accepted"];

export function milestoneStateLabel(s: string): string {
  return s === "accepted" ? t("lib-kb-tree.state_accepted") : s === "not_accepted" ? t("lib-kb-tree.state_not_accepted") : t("lib-kb-tree.state_open");
}

/** A project's status from the list's "kind · status". */
export function treeStatus(e: Pick<KBEntry, "extra">): string {
  return e.extra?.split(" · ")[1]?.trim() || "active";
}

/** The projects the tree shows (not merged, not "not a project") and each one's parent among them. A parent the list
 * does not show, or one that would close a cycle, leaves the project at the top. */
export function treeOf(list: TreeEntry[]): { live: TreeEntry[]; parents: Map<string, string> } {
  const live = list.filter((e) => !["merged", "ignored"].includes(treeStatus(e)));
  const ids = new Set(live.map((e) => e.id));
  const parents = new Map<string, string>();
  for (const e of live) if (e.parent && ids.has(e.parent) && e.parent !== e.id) parents.set(e.id, e.parent);
  for (const e of live) {
    const seen = new Set<string>();
    for (let at: string | undefined = e.id; at; at = parents.get(at)) {
      if (seen.has(at)) {
        parents.delete(e.id);
        break;
      }
      seen.add(at);
    }
  }
  return { live, parents };
}

/** The projects directly under each one, in the order given. */
export function childrenOf(parents: Map<string, string>, order: string[]): Map<string, string[]> {
  const out = new Map<string, string[]>();
  for (const id of order) {
    const p = parents.get(id);
    if (p) out.set(p, [...(out.get(p) ?? []), id]);
  }
  return out;
}

/** 1 at the top level. */
export function depthOf(parents: Map<string, string>, id: string): number {
  let d = 1;
  const seen = new Set([id]);
  for (let at = parents.get(id); at && !seen.has(at); at = parents.get(at)) {
    seen.add(at);
    d++;
  }
  return d;
}

/** Everything below id, at any depth. */
export function below(parents: Map<string, string>, id: string): Set<string> {
  const out = new Set<string>();
  let grew = true;
  while (grew) {
    grew = false;
    for (const [child, parent] of parents) {
      if (!out.has(child) && (parent === id || out.has(parent))) {
        out.add(child);
        grew = true;
      }
    }
  }
  return out;
}

/** Whether a move can be made: why not, or a hint when it goes past the suggested depth. */
export interface Verdict {
  ok: boolean;
  why?: string;
  warn?: string;
}

/** Can child go under target (null: the top level)? Refused: itself, a project below it, where it already is. */
export function canMove(parents: Map<string, string>, child: string, target: string | null): Verdict {
  if (target === child) return { ok: false, why: t("lib-kb-tree.why_self") };
  const kids = below(parents, child);
  if (target && kids.has(target)) return { ok: false, why: t("lib-kb-tree.why_below") };
  if ((parents.get(child) ?? null) === target) return { ok: false, why: t("lib-kb-tree.why_here") };
  let height = 0;
  for (const k of kids) height = Math.max(height, depthOf(parents, k) - depthOf(parents, child));
  const deepest = (target ? depthOf(parents, target) + 1 : 1) + height;
  return deepest > SUGGESTED_DEPTH ? { ok: true, warn: t("lib-kb-tree.warn_deep", { levels: deepest, suggested: SUGGESTED_DEPTH }) } : { ok: true };
}

/** parents after a move (target null or "": the top level). */
export function moved(parents: Map<string, string>, child: string, target: string | null): Map<string, string> {
  const next = new Map(parents);
  if (target) next.set(child, target);
  else next.delete(child);
  return next;
}

/** The flag a project's milestones show on the map: the next open one's date (or title), else how they ended. */
export function milestoneFlag(ms: Milestone[] | null | undefined): { text: string; state: MilestoneState } | null {
  if (!ms?.length) return null;
  const open = ms.filter((m) => m.state === "open").sort((a, b) => (a.target || "9999").localeCompare(b.target || "9999"));
  if (open.length) {
    const m = open[0];
    return { text: m.target ? m.target.slice(5) : m.title, state: "open" };
  }
  const failed = ms.some((m) => m.state === "not_accepted");
  return { text: failed ? t("lib-kb-tree.flag_not_accepted") : t("lib-kb-tree.flag_accepted"), state: failed ? "not_accepted" : "accepted" };
}

const TONE: Record<string, MapTone> = { done: "green", paused: "orange" };

/** The tree view's id for a project's node ("p:<slug>"; in the tag view "p:<slug>@<tag>"). */
export const nodeId = (id: string, tag?: string) => (tag === undefined ? `p:${id}` : `p:${id}@${tag}`);

/** The project tree as a map. `closed` holds what the user opened or closed (by node id), over the defaults: by
 * hierarchy everything open; by tag the biggest tags open. Counts are of projects, as the knowledge list counts them. */
export function buildTreeMap(list: TreeEntry[], parents: Map<string, string>, mode: "tree" | "tag", closed: Map<string, boolean>): MapNode {
  const { live } = treeOf(list);
  const by = new Map(live.map((e) => [e.id, e]));
  const order = [...live].sort((a, b) => a.name.localeCompare(b.name)).map((e) => e.id);
  const kids = childrenOf(parents, order);
  const shut = (id: string, dflt: boolean) => closed.get(id) ?? dflt;
  const card = (e: TreeEntry, tone: MapTone, extra: Partial<MapNode>): MapNode => {
    const flag = milestoneFlag(e.milestones);
    return {
      id: nodeId(e.id), title: e.name, kind: "pnode", tone, kids: [], project: e, status: treeStatus(e), importance: e.importance,
      flag: flag ?? undefined, open: { kind: "projects", id: e.id }, ...extra,
    };
  };
  if (mode === "tag") {
    const tags = new Map<string, TreeEntry[]>();
    const none: TreeEntry[] = [];
    for (const id of order) {
      const e = by.get(id)!;
      if (!e.tags?.length) none.push(e);
      for (const tag of e.tags ?? []) tags.set(tag, [...(tags.get(tag) ?? []), e]);
    }
    const groups = [...tags].sort(([a, x], [b, y]) => y.length - x.length || a.localeCompare(b));
    if (none.length) groups.push(["", none]);
    const branches = groups.map(([tag, es], i): MapNode => {
      const id = `tag:${tag}`;
      return {
        id, title: tag ? `#${tag}` : t("lib-kb-tree.no_tag"), kind: "branch", tone: tag ? "accent" : "orange", side: i % 2 ? "L" : "R", meta: String(es.length),
        collapsed: shut(id, i >= OPEN_TAGS), icon: "book", view: "tree",
        kids: es.map((e) => {
          const way: string[] = [];
          const seen = new Set([e.id]);
          for (let at = parents.get(e.id); at && !seen.has(at); at = parents.get(at)) {
            seen.add(at);
            way.unshift(by.get(at)?.name ?? at);
          }
          const also = (e.tags?.length ?? 0) - 1;
          return card(e, tag ? "accent" : "orange", { id: nodeId(e.id, tag), where: way.join(" › ") || undefined, meta: also > 0 ? t("lib-kb-tree.also_under", { count: also }) : undefined });
        }),
      };
    });
    return { id: "p:root", title: t("lib-kb-tree.root"), kind: "root", tone: "accent", kids: branches, view: "tree", meta: t("lib-kb-tree.root_by_tag") };
  }
  const make = (id: string, depth: number, tone: MapTone, seen: Set<string>): MapNode => {
    const e = by.get(id)!;
    const under = (kids.get(id) ?? []).filter((k) => !seen.has(k));
    const next = new Set([...seen, id]);
    const n = card(e, tone, { depth, collapsed: under.length ? shut(nodeId(id), false) : undefined });
    n.kids = under.map((k) => make(k, depth + 1, tone, next));
    if (under.length) n.meta = t("lib-kb-tree.sub_count", { count: under.length });
    return n;
  };
  const tops = order.filter((id) => !parents.has(id));
  // Sides alternate in name order, so a move inside the tree never flips a branch to the other side.
  const branches = tops.map((id, i) => {
    const n = make(id, 1, TONE[treeStatus(by.get(id)!)] ?? "accent", new Set());
    n.side = i % 2 ? "L" : "R";
    return n;
  });
  return { id: "p:root", title: t("lib-kb-tree.root"), kind: "root", tone: "accent", kids: branches, view: "tree", meta: t("lib-kb-tree.root_count", { count: live.length }) };
}
