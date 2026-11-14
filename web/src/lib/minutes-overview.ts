// The minutes' summary card (#6563, docs/assistant/meeting-minutes.md "Summary card (#6563)"): `minutes.overview` read
// on its own, apart from the legacy lists, and cleaned as the contract's "Clients" says. The page shows the summary card
// only when a bullet is left; anything else (no overview, a broken one) shows the minutes as before. The mock hub, the
// iPhone and this page check the same cases (spec/fixtures/minutes-overview/cases.json).
import type { IconName } from "../ui/icons";

/** The topic icons the hub writes; any other value reads as `general`, so the list can grow. */
export const TOPIC_ICONS = ["progress", "schedule", "release", "performance", "metrics", "tooling", "quality", "design", "people", "customer", "budget", "risk", "general"] as const;
export type TopicIcon = (typeof TOPIC_ICONS)[number];

/** Each topic icon's glyph in ui/icons.tsx. */
export const TOPIC_GLYPH: Record<TopicIcon, IconName> = {
  progress: "trendUp",
  schedule: "calendar",
  release: "send",
  performance: "gauge",
  metrics: "chart",
  tooling: "tool",
  quality: "shield",
  design: "layers",
  people: "users",
  customer: "person",
  budget: "money",
  risk: "shieldAlert",
  general: "list",
};

/** A bullet's or a conclusion's citation: `at` (seconds into the recording) only when it is a non-negative number,
 *  `photo` only when it is a positive whole number. */
export interface OverviewCite {
  at?: number;
  photo?: number;
}

export interface OverviewLine extends OverviewCite {
  text: string;
  /** The places of `text` set in bold, found by `boldParts`. */
  strong: string[];
}

export interface OverviewBullet extends OverviewLine {
  who?: string;
  /** A decision (the boolean `true` on the wire); a point otherwise. */
  decision: boolean;
}

export interface OverviewTopic {
  title: string;
  icon: TopicIcon;
  bullets: OverviewBullet[];
}

export interface OverviewGroup {
  /** "" for the untitled group, which has no heading. */
  title: string;
  topics: OverviewTopic[];
}

export interface MinutesOverview {
  headline?: string;
  conclusions: OverviewLine[];
  groups: OverviewGroup[];
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const text = (v: unknown): string | undefined => (typeof v === "string" && v.trim() ? v : undefined);
const objects = (v: unknown): Obj[] => (Array.isArray(v) ? v.filter(isObj) : []);

function cite(o: Obj): OverviewCite {
  const out: OverviewCite = {};
  if (typeof o.at === "number" && Number.isFinite(o.at) && o.at >= 0) out.at = o.at;
  if (Number.isInteger(o.photo) && (o.photo as number) > 0) out.photo = o.photo as number;
  return out;
}

function line(o: Obj): OverviewLine | null {
  const words = text(o.text);
  if (!words) return null;
  const strong = Array.isArray(o.strong) ? o.strong.filter((s): s is string => typeof s === "string" && s !== "") : [];
  return { text: words, strong, ...cite(o) };
}

function bullet(o: Obj): OverviewBullet | null {
  const l = line(o);
  if (!l) return null;
  const who = text(o.who);
  return { ...l, ...(who ? { who } : {}), decision: o.decision === true };
}

function topic(o: Obj): OverviewTopic | null {
  const title = text(o.title);
  if (!title || !Array.isArray(o.bullets)) return null;
  const icon = (TOPIC_ICONS as readonly unknown[]).includes(o.icon) ? (o.icon as TopicIcon) : "general";
  const bullets = objects(o.bullets).map(bullet).filter((b): b is OverviewBullet => !!b);
  return bullets.length ? { title, icon, bullets } : null;
}

function group(o: Obj): OverviewGroup | null {
  if (!Array.isArray(o.topics)) return null;
  const topics = objects(o.topics).map(topic).filter((t): t is OverviewTopic => !!t);
  return topics.length ? { title: text(o.title) ?? "", topics } : null;
}

/** The overview as the summary card shows it, or null when the page shows the minutes as before: a missing, null or
 *  non-object overview, `groups` that is not an array, or nothing left after the cleaning (no bullet). */
export function minutesOverview(v: unknown): MinutesOverview | null {
  if (!isObj(v) || !Array.isArray(v.groups)) return null;
  const groups = objects(v.groups).map(group).filter((g): g is OverviewGroup => !!g);
  if (!groups.length) return null;
  const headline = text(v.headline);
  const conclusions = objects(v.conclusions).map(line).filter((c): c is OverviewLine => !!c);
  return { ...(headline ? { headline } : {}), conclusions, groups };
}

export interface BoldPart {
  text: string;
  bold: boolean;
}

/** `text` cut into runs with the `strong` places in bold ("Bold (`strong`)"): each entry is the first exact occurrence
 *  at or after the end of the previous one found, so a figure that appears twice is bolded where the hub marked it; an
 *  entry not found bolds nothing. */
export function boldParts(text: string, strong: readonly string[]): BoldPart[] {
  const out: BoldPart[] = [];
  let from = 0;
  for (const s of strong) {
    if (!s) continue;
    const at = text.indexOf(s, from);
    if (at < 0) continue;
    if (at > from) out.push({ text: text.slice(from, at), bold: false });
    out.push({ text: s, bold: true });
    from = at + s.length;
  }
  if (from < text.length) out.push({ text: text.slice(from), bold: false });
  return out;
}

/** How many of a topic's bullets show before "Show N more". */
export const TOPIC_BULLETS_SHOWN = 4;

/** The four topic tints, by the group's position (the first group gets the first). */
export const TOPIC_TINTS = 4;
