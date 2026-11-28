// The knowledge map (/kb/map, views/knowledge/KnowledgeMap.tsx): the knowledge base as a two-sided mind map, in two views.
// - Topics: the curated knowledge points (docs/assistant/knowledge.md "Knowledge points"), a branch per topic, its subtopics
//   and its points, each point with its kind, reason, sources and day.
// - Projects: what the main chat keeps on each project (goal, role, status, importance, its own timeline lines, code
//   directories, links) with the curated summary.md as sections. Agents' automatic timeline lines stay out: the project page
//   has them all.
// Pure: the data in, a tree of nodes and its layout out.
import { t } from "../i18n";
import { type KBDetail, type KBEntry } from "./kb";
import { groupPoints, type PointGroup, type TopicPoint } from "./kb-points";
import { type MilestoneState, type TreeEntry } from "./kb-tree";

export type MapKind = "root" | "branch" | "group" | "project" | "section" | "text" | "link" | "day" | "topic" | "point" | "pnode";
export type MapTone = "accent" | "green" | "orange";
/** The icon a section node shows (icons.tsx names). */
export type SectionIcon = "target" | "gauge" | "arrowRight" | "warning" | "flag" | "book" | "clock" | "calendar" | "folder" | "person" | "chat" | "light";

export interface MapNode {
  id: string;
  title: string;
  kind: MapKind;
  kids: MapNode[];
  /** right or left of the root, set on the root's children */
  side?: "R" | "L";
  /** which branch colors this node */
  tone: MapTone;
  /** a short count or label beside the title */
  meta?: string;
  /** what the panel says about the node */
  body?: string;
  /** the knowledge entry it opens: a project's or topic's id */
  open?: { kind: "projects" | "topics" | "chats" | "people"; id: string };
  status?: string;
  importance?: string;
  /** a project: the main chat's lines, newest first */
  lines?: PersonaLine[];
  collapsed?: boolean;
  /** text that wraps onto several lines */
  wrap?: boolean;
  /** a project section that came from the curated summary.md, not from the main chat's own fields */
  fromSummary?: boolean;
  /** a project: its summary.md by section */
  summary?: SummarySection[];
  /** a project without a goal */
  noGoal?: boolean;
  /** a project's goal section (the summary's, else the main chat's one line) */
  goal?: boolean;
  /** a knowledge point */
  point?: TopicPoint;
  /** a topic or subtopic in the topic tree; the root says which view it is */
  isTopic?: boolean;
  view?: "topics" | "projects" | "tree";
  /** the project tree (lib/kb-tree.ts): the project a node draws, its milestone flag, its depth (1 at the top), and in
   *  the tag view where it sits in the tree */
  project?: TreeEntry;
  flag?: { text: string; state: MilestoneState };
  depth?: number;
  where?: string;
  icon?: SectionIcon;
  /** a topic: the entries its points name, to show sources by name */
  links?: KBEntry[];
}

export interface PersonaLine {
  when: string;
  text: string;
}

/** A timeline line the main chat wrote ("<time> · Persona · <text>"), or null. The main chat sometimes repeats
 * "<time> · Persona · " inside its text; the repeats go. "Persona" is the source the hub writes for the main chat. */
export function personaLine(raw: string): PersonaLine | null {
  const m = /^-?\s*(\d{4}-\d{2}-\d{2} \d{2}:\d{2}) · (.+?) · (.*)$/.exec(raw.trim());
  if (!m || m[2] !== "Persona") return null;
  let text = m[3];
  for (;;) {
    // i18n-ignore: the part-of-day words the main chat writes in its Chinese timeline lines
    const next = text.replace(/^((\d{4}-)?\d{2}-\d{2}[ T]?)?(\d{2}:\d{2})?\s*(早间|午间|晚间|上午|下午)?\s*([·:：]\s*)?(Persona\s*·\s*)?/, ""); // i18n-ignore
    if (next === text || !next) break;
    text = next;
  }
  return { when: m[1], text: text.replace(/[*`#]+/g, "").replace(/\s+/g, " ").trim() };
}

export interface SummaryItem {
  text: string;
  kids: SummaryItem[];
}
export interface SummarySection {
  title: string;
  items: SummaryItem[];
}

/** A summary.md as its "## " sections, each a list of items: "- " / "1. " list items nested by indentation (two spaces
 * a level), other lines as items of their own. The "# <name>" heading and emphasis marks are dropped; text before the
 * first section goes under `lead` (the section the caller names). */
export function parseSummary(md: string, lead = ""): SummarySection[] {
  const out: SummarySection[] = [];
  let sec: SummarySection | null = null;
  const stack: { indent: number; item: SummaryItem }[] = [];
  for (const raw of md.split("\n")) {
    if (!raw.trim() || /^#\s/.test(raw)) continue;
    const h = /^#{2,}\s+(.*)$/.exec(raw.trim());
    if (h) {
      sec = { title: clean(h[1]), items: [] };
      out.push(sec);
      stack.length = 0;
      continue;
    }
    sec ??= out[out.push({ title: lead, items: [] }) - 1];
    const m = /^(\s*)(?:[-*+]|\d+[.)])\s+(.*)$/.exec(raw);
    const indent = m ? m[1].replace(/\t/g, "  ").length : 0;
    const item: SummaryItem = { text: clean(m ? m[2] : raw), kids: [] };
    if (!item.text) continue;
    while (stack.length && stack[stack.length - 1].indent >= indent) stack.pop();
    if (m && stack.length) stack[stack.length - 1].item.kids.push(item);
    else sec.items.push(item);
    if (m) stack.push({ indent, item });
  }
  return out.filter((x) => x.items.length);
}

function clean(s: string): string {
  return s.replace(/\*\*|__|`/g, "").replace(/\[([^\]]+)\]\([^)]+\)/g, "$1").replace(/\s+/g, " ").trim();
}

// The curated summary's section names are the ones its prompt asks for (server/internal/prompts/kb/project.md): Chinese
// text the hub writes, matched to pick an icon and the sections open at first.
const SECTION_ICONS: [RegExp, SectionIcon][] = [
  [/现状|进度|进展/, "gauge"], // i18n-ignore
  [/下一步|待办|计划/, "arrowRight"], // i18n-ignore
  [/风险|阻塞|问题/, "warning"], // i18n-ignore
  [/决定|决策|结论/, "flag"], // i18n-ignore
  [/这是什么|背景|摘要/, "book"], // i18n-ignore
  [/目标/, "target"], // i18n-ignore
  [/角色|人/, "person"], // i18n-ignore
];
const GOAL = /^目标$/; // i18n-ignore
const OPEN_SECTIONS = /^(目标|现状)$/; // i18n-ignore
const ROLE = /^我的角色$/; // i18n-ignore

function sectionIcon(title: string): SectionIcon {
  return SECTION_ICONS.find(([re]) => re.test(title))?.[1] ?? "book";
}

const IMPORTANCE = ["P0", "P1", "P2", "P3"];

/** How much map height (px at 100%) what opens at first may add (#16206): about three views of the map at the opening 65%
 * zoom. A large knowledge base starts with fewer branches open, so Fit to view still shows it whole; a small one opens as
 * before. Only the start: what the owner opens or closes afterwards is theirs. */
const OPEN_BUDGET = 3200;
/** The layout's height for a row under an open branch: a subtopic, section, group or "Other" (28 + 12 between rows), a
 * point (two lines, 44 + 12), a project card (58 + 12). */
const ROW = { other: 40, point: 56, project: 70 };

function rowsHeight(kids: MapNode[]): number {
  return kids.reduce((h, k) => h + (k.point ? ROW.point : k.kind === "project" ? ROW.project : ROW.other), 0);
}

let seq = 0;
const node = (title: string, kind: MapKind, tone: MapTone, kids: MapNode[] = [], extra: Partial<MapNode> = {}): MapNode => ({
  id: `m${++seq}`, title, kind, tone, kids, ...extra,
});

function str(v: unknown): string {
  return typeof v === "string" ? v.trim() : "";
}

/** What a project row says: its status from the detail, else from the list's "kind · status". */
function statusOf(e: KBEntry, d?: KBDetail): string {
  return str(d?.meta?.status) || e.extra?.split(" · ")[1]?.trim() || "active";
}

function project(e: KBEntry, d: KBDetail | undefined, tone: MapTone): MapNode {
  const meta = d?.meta ?? {};
  const lines = (d?.log ?? []).map(personaLine).filter((l): l is PersonaLine => l !== null).reverse();
  const kids: MapNode[] = [];
  const goal = str(meta.goal), role = str(meta.my_role);
  const summary = parseSummary(d?.summary ?? "", t("lib-kb-map.summary"));
  const item = (i: SummaryItem): MapNode => node(i.text, "text", tone, i.kids.map(item), { wrap: true, collapsed: i.kids.length > 0, fromSummary: true });
  const hasGoal = summary.some((x) => GOAL.test(x.title)), hasRole = summary.some((x) => ROLE.test(x.title));
  // The summary's own sections first (its goal is the fuller one); the main chat's one-line goal / role only when it has none.
  if (goal && !hasGoal) kids.push(node(t("lib-kb-map.goal"), "section", tone, [node(goal, "text", tone, [], { wrap: true })], { icon: "target", goal: true }));
  for (const sec of summary) {
    kids.push(node(sec.title, "section", tone, sec.items.map(item), { meta: t("lib-kb-map.from_summary"), fromSummary: true, icon: sectionIcon(sec.title), goal: GOAL.test(sec.title), collapsed: !OPEN_SECTIONS.test(sec.title) }));
  }
  if (role && !hasRole) kids.push(node(t("lib-kb-map.my_role"), "section", tone, [node(role, "text", tone, [], { wrap: true })], { icon: "person" }));
  if (lines.length) {
    kids.push(node(t("lib-kb-map.latest"), "section", tone, [node(lines[0].text, "text", tone, [], { wrap: true, body: lines[0].text, meta: lines[0].when })], { meta: lines[0].when.slice(5), icon: "clock" }));
    const days = new Map<string, PersonaLine[]>();
    for (const l of lines) days.set(l.when.slice(0, 10), [...(days.get(l.when.slice(0, 10)) ?? []), l]);
    kids.push(node(t("lib-kb-map.main_chat_notes"), "section", tone, [...days].map(([day, ls]) =>
      node(day.slice(5), "day", tone, ls.map((l) => node(`${l.when.slice(11)} · ${l.text}`, "text", tone, [], { body: l.text, meta: l.when })), { meta: String(ls.length), collapsed: true, icon: "calendar" })),
    { meta: String(lines.length), collapsed: true, icon: "clock" }));
  }
  const dirs = Array.isArray(meta.dirs) ? meta.dirs.filter((x): x is string => typeof x === "string") : [];
  if (dirs.length) kids.push(node(t("lib-kb-map.code_dirs"), "section", tone, dirs.map((x) => node(x, "text", tone)), { meta: String(dirs.length), collapsed: true, icon: "folder" }));
  const links = d?.links ?? [];
  const chats = links.filter((l) => l.kind === "chats"), people = links.filter((l) => l.kind === "people");
  if (chats.length) kids.push(node(t("lib-kb-map.related_chats"), "section", tone, chats.map((l) => node(l.name, "link", tone, [], { open: { kind: "chats", id: l.id } })), { meta: String(chats.length), collapsed: true, icon: "chat" }));
  if (people.length) kids.push(node(t("lib-kb-map.related_people"), "section", tone, people.map((l) => node(l.name, "link", tone, [], { open: { kind: "people", id: l.id } })), { meta: String(people.length), collapsed: true, icon: "person" }));
  return node(e.name, "project", tone, kids, {
    open: { kind: "projects", id: e.id }, status: statusOf(e, d), importance: e.importance, lines, collapsed: true, summary, noGoal: !goal && !hasGoal,
    meta: [e.importance, statusLabel(statusOf(e, d))].filter(Boolean).join(" · "),
  });
}

function statusLabel(s: string): string {
  return s === "done" ? t("lib-kb-map.status_done") : s === "paused" ? t("lib-kb-map.status_paused") : t("lib-kb-map.status_active");
}

/** The project map from the project list and each project's detail (a missing detail still gives the project's card).
 * Active projects to the right, grouped by importance; done and paused ones to the left. Merged and "not a project"
 * ones stay out, as in the list. */
export function buildProjectMap(list: KBEntry[], details: Map<string, KBDetail>): MapNode {
  seq = 0;
  const live = list.filter((e) => !["merged", "ignored"].includes(statusOf(e, details.get(e.id))));
  const rank = (e: KBEntry) => (IMPORTANCE.includes(e.importance ?? "") ? IMPORTANCE.indexOf(e.importance!) : 9);
  const sorted = [...live].sort((a, b) => rank(a) - rank(b) || (b.updated ?? "").localeCompare(a.updated ?? ""));
  const by = (s: string) => sorted.filter((e) => statusOf(e, details.get(e.id)) === s);
  const active = sorted.filter((e) => !["done", "paused"].includes(statusOf(e, details.get(e.id))));
  const groups = new Map<string, MapNode[]>();
  for (const e of active) {
    const g = e.importance && IMPORTANCE.includes(e.importance) ? e.importance : t("lib-kb-map.unrated");
    groups.set(g, [...(groups.get(g) ?? []), project(e, details.get(e.id), "accent")]);
  }
  // The importance groups open in order, at most two, each only while its projects fit in what is left of OPEN_BUDGET. The
  // first that does not fit and every group after it start closed: a less important group never opens before a more
  // important one.
  let left = OPEN_BUDGET, open = true;
  const activeKids = [...groups].map(([g, ns], i) => {
    open &&= i < 2 && rowsHeight(ns) <= left;
    if (open) left -= rowsHeight(ns);
    return node(g, "group", "accent", ns, { meta: String(ns.length), collapsed: !open });
  });
  const done = by("done"), paused = by("paused");
  const branches = [node(t("lib-kb-map.branch_active"), "branch", "accent", activeKids, { side: "R", meta: String(active.length), icon: "folder" })];
  if (done.length) branches.push(node(t("lib-kb-map.branch_done"), "branch", "green", done.map((e) => project(e, details.get(e.id), "green")), { side: "L", meta: String(done.length), collapsed: true }));
  if (paused.length) branches.push(node(t("lib-kb-map.branch_paused"), "branch", "orange", paused.map((e) => project(e, details.get(e.id), "orange")), { side: "L", meta: String(paused.length), collapsed: true }));
  return node(t("lib-kb-map.projects_root"), "root", "accent", branches, { view: "projects", meta: t("lib-kb-map.projects_root_sub") });
}

// ── topic tree ──────────────────────────────────────────

/** Topics with more loose points than this, beside subtopics, keep them under one closed "Other" node. */
const LOOSE_SHOWN = 3;
/** How many of the biggest topics open at first, at most. */
const OPEN_TOPICS = 6;

export interface TopicData {
  entry: KBEntry;
  detail?: KBDetail;
}

function groupSize(g: PointGroup): number {
  return g.points.length + g.groups.reduce((n, x) => n + groupSize(x), 0);
}

/** How many points a topic has: its detail's, else what its list entry says. */
export function topicSize(x: TopicData): number {
  if (x.detail?.points) return groupSize(groupPoints(x.detail.points));
  const n = Number.parseInt(x.entry.extra ?? "", 10);
  return Number.isFinite(n) ? n : 0;
}

/** The topic tree as a map: the root, one branch per topic (split between the sides by size), subtopics, points. At
 * first it shows structure: the biggest topics open onto their subtopics, every subtopic closed, as many as OPEN_BUDGET
 * allows. */
export function buildTopicMap(topics: TopicData[]): MapNode {
  seq = 0;
  const tones: MapTone[] = ["accent", "green", "orange"];
  const order = [...topics].sort((a, b) => topicSize(b) - topicSize(a) || a.entry.name.localeCompare(b.entry.name));
  const pointNode = (p: TopicPoint, tone: MapTone, links: KBEntry[]) => node(p.text, "point", tone, [], { point: p, wrap: true, links });
  const group = (g: PointGroup, name: string, tone: MapTone, depth: number, links: KBEntry[], extra: Partial<MapNode>): MapNode => {
    const subs = g.groups.map((x) => group(x, x.path[x.path.length - 1], tone, depth + 1, links, {}));
    const points = g.points.map((p) => pointNode(p, tone, links));
    // Beside subtopics, a handful of loose points stay; more go under one closed "Other", so they don't bury the subtopics.
    const kids = subs.length && points.length > LOOSE_SHOWN
      ? [...subs, node(t("lib-kb-map.other"), "topic", tone, points, { meta: String(points.length), isTopic: true, collapsed: true, body: t("lib-kb-map.other_body", { topic: name }) })]
      : [...subs, ...points];
    return node(name, depth === 1 ? "branch" : "topic", tone, kids, { meta: String(groupSize(g)), isTopic: true, collapsed: depth > 1, ...extra });
  };
  const tops = order.map((x, i) => {
    const tone = tones[i % tones.length];
    const g = groupPoints(x.detail?.points);
    const n = group(g, x.entry.name, tone, 1, x.detail?.links ?? [], { body: x.entry.one_line, open: { kind: "topics", id: x.entry.id }, collapsed: true, icon: "light" });
    if (!x.detail) n.meta = String(topicSize(x));
    return n;
  });
  // The biggest open first, at most OPEN_TOPICS, each only when what it shows fits in what is left of OPEN_BUDGET; one that
  // does not fit stays closed and the next is tried, so a large base still opens a few mid-sized topics.
  let left = OPEN_BUDGET, opened = 0;
  for (const n of tops) {
    if (opened === OPEN_TOPICS || rowsHeight(n.kids) > left) continue;
    n.collapsed = false;
    opened++;
    left -= rowsHeight(n.kids);
  }
  // Fill the lighter side first, biggest topics first, so the two sides come out about as tall.
  const weight = { R: 0, L: 0 };
  order.forEach((x, i) => {
    const side = weight.R <= weight.L ? "R" : "L";
    tops[i].side = side;
    weight[side] += topicSize(x) + 3;
  });
  return node(t("lib-kb-map.topics_root"), "root", "accent", tops, { view: "topics", meta: t("lib-kb-map.topics_root_sub") });
}

/** Every point under a node, its subtopics included. */
export function pointsUnder(n: MapNode): MapNode[] {
  const out: MapNode[] = [];
  walk(n, (k) => {
    if (k.point) out.push(k);
  });
  return out;
}

export function walk(n: MapNode, f: (n: MapNode, parent: MapNode | null, depth: number) => void, parent: MapNode | null = null, depth = 0) {
  f(n, parent, depth);
  for (const k of n.kids) walk(k, f, n, depth + 1);
}

// ── layout ──────────────────────────────────────────────

export interface Box {
  w: number;
  h: number;
  lines: string[];
}
export interface Placed {
  node: MapNode;
  parent: MapNode | null;
  depth: number;
  side: "R" | "L";
  x: number;
  y: number;
  box: Box;
}
export interface Layout {
  placed: Placed[];
  width: number;
  height: number;
  cx: number;
  cy: number;
}

/** Two-sided layout: the root in the middle, its children split by side, columns by depth, each parent centered on its
 * visible children. `measure` sizes a node's box. */
export function layout(root: MapNode, measure: (n: MapNode, depth: number) => Box): Layout {
  const vis = (n: MapNode) => (n.collapsed ? [] : n.kids);
  const boxes = new Map<MapNode, Box>();
  const placed: Placed[] = [];
  const rootBox = measure(root, 0);
  boxes.set(root, rootBox);
  const sides: Record<"R" | "L", { span: number; cols: number[]; items: Placed[] }> = { R: { span: 0, cols: [], items: [] }, L: { span: 0, cols: [], items: [] } };
  for (const side of ["R", "L"] as const) {
    const tops = root.kids.filter((k) => (k.side ?? "R") === side);
    const maxW: number[] = [];
    const size = (n: MapNode, d: number) => {
      const b = measure(n, d);
      boxes.set(n, b);
      maxW[d] = Math.max(maxW[d] ?? 0, b.w);
      vis(n).forEach((k) => size(k, d + 1));
    };
    tops.forEach((k) => size(k, 1));
    const cols = [0, rootBox.w / 2 + 70];
    for (let d = 1; d < maxW.length; d++) cols[d + 1] = cols[d] + (maxW[d] ?? 0) + (d === 1 ? 64 : 44);
    let y = 0;
    const lay = (n: MapNode, parent: MapNode, d: number): number => {
      const ks = vis(n), b = boxes.get(n)!;
      let cy: number;
      if (!ks.length) {
        cy = y + b.h / 2;
        y += b.h + (d <= 2 ? 12 : 6);
      } else {
        const ys = ks.map((k) => lay(k, n, d + 1));
        cy = (ys[0] + ys[ys.length - 1]) / 2;
      }
      sides[side].items.push({ node: n, parent, depth: d, side, x: cols[d], y: cy, box: b });
      return cy;
    };
    tops.forEach((k, i) => {
      if (i) y += 34;
      lay(k, root, 1);
    });
    sides[side].span = y;
    sides[side].cols = cols;
  }
  const deepest = (s: "R" | "L") => sides[s].items.reduce((m, p) => Math.max(m, sides[s].cols[p.depth] + p.box.w), rootBox.w / 2);
  const left = deepest("L"), right = deepest("R");
  const span = Math.max(sides.R.span, sides.L.span, 320);
  const cx = left + 40, cy = span / 2 + 60;
  for (const side of ["R", "L"] as const) {
    const off = cy - sides[side].span / 2;
    for (const p of sides[side].items) {
      p.y += off;
      p.x = side === "R" ? cx + p.x : cx - p.x - p.box.w;
      placed.push(p);
    }
  }
  placed.unshift({ node: root, parent: null, depth: 0, side: "R", x: cx - rootBox.w / 2, y: cy, box: rootBox });
  return { placed, width: cx + right + 60, height: span + 120, cx, cy };
}
