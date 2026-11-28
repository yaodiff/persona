// Token usage by project (#16530, docs/server/machines.md "Token usage by project"): GET /api/v1/usage/projects and
// GET /api/v1/usage/projects/detail. The hub sums every computer's usage by the project each session's start directory
// resolved to (a git remote, a repository's first commit, a folder's name) and says each project's kind, group, name,
// share and computers; the page only shows them. Today, the last 3 and the last 7 days come in one answer, so a range
// switches without asking again. The iPhone reads the same (PersonaWire UsageProjects).
import { api, ApiError } from "./api";
import { formatNumber, t } from "../i18n";

export interface ProjectTokens {
  in?: number;
  cache_write?: number;
  cache_read?: number;
  out?: number;
  req?: number;
}

export type ProjectKind = "repo" | "local" | "folder" | "home" | "temp" | "assistant" | "unknown";
export type ProjectGroup = "development" | "other" | "unknown";

export interface UsageProject {
  /** Opaque: what the breakdown takes back. */
  key: string;
  kind: ProjectKind;
  group: ProjectGroup;
  /** "owner/repo", a folder's name; empty for home, temp, assistant and unknown (projectName names them). */
  name: string;
  host?: string;
  machines: number;
  /** Of the range's total, 0…1. */
  share: number;
  tokens: ProjectTokens;
}

export interface UsageProjectRange {
  days: number;
  total: ProjectTokens;
  projects: UsageProject[];
  not_split: ProjectTokens;
}

export interface UsageProjects {
  zone: string;
  ranges: UsageProjectRange[];
  stored?: boolean;
}

export interface UsagePart {
  key: string;
  name?: string;
  tokens: ProjectTokens;
}

export interface UsagePlace {
  machine: string;
  machine_name?: string;
  place: string;
  worktree?: boolean;
  tokens: ProjectTokens;
}

export interface UsageProjectBreakdown {
  days: number;
  total: ProjectTokens;
  share: number;
  cache_hit: number;
  machines: UsagePart[];
  agents: UsagePart[];
  models: UsagePart[];
  launchers: UsagePart[];
  places: UsagePlace[];
}

export interface UsageProjectDetail {
  zone: string;
  project: UsageProject;
  ranges: UsageProjectBreakdown[];
  days: { date: string; tokens: ProjectTokens }[];
  hours: { t: number; hour: number; tokens: ProjectTokens }[];
  stored?: boolean;
}

/** The ranges, in the order the hub sends them. */
export const PROJECT_RANGES = [1, 3, 7] as const;

/** Every token, cache reads included (the calendar's count). */
export function projectTotal(t?: ProjectTokens): number {
  return (t?.in ?? 0) + (t?.cache_write ?? 0) + (t?.cache_read ?? 0) + (t?.out ?? 0);
}

/** Today's, the last 3 days' and the last 7 days' projects; null from an older hub (the section is not shown). */
export async function fetchUsageProjects(): Promise<UsageProjects | null> {
  try {
    const res = await api<{ usage: UsageProjects }>("/usage/projects");
    return res.usage?.ranges?.length ? res.usage : null;
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null; // a hub before the route
    throw e;
  }
}

/** One project's breakdown for the same ranges. */
export async function fetchUsageProjectDetail(key: string): Promise<UsageProjectDetail> {
  const res = await api<{ usage: UsageProjectDetail }>(`/usage/projects/detail?key=${encodeURIComponent(key)}`);
  return res.usage;
}

/** A range's projects in the groups the page shows them in (the hub's order kept: largest first), each group's sum. */
export function projectGroups(range: UsageProjectRange): {
  development: UsageProject[];
  other: UsageProject[];
  unknown: UsageProject[];
  sums: Record<ProjectGroup, number>;
} {
  const out = { development: [] as UsageProject[], other: [] as UsageProject[], unknown: [] as UsageProject[] };
  const sums: Record<ProjectGroup, number> = { development: 0, other: 0, unknown: 0 };
  for (const p of range.projects) {
    const group = p.group === "development" || p.group === "other" ? p.group : "unknown";
    out[group].push(p);
    sums[group] += projectTotal(p.tokens);
  }
  return { ...out, sums };
}

/** How the page names a project: its own name, or the words for what has none (never a folder that would show the
 *  account's name). */
export function projectName(p: Pick<UsageProject, "kind" | "name">): string {
  switch (p.kind) {
    case "home":
      return t("tokencalendar.project_home");
    case "temp":
      return t("tokencalendar.project_temp");
    case "assistant":
      return t("tokencalendar.project_assistant");
    case "unknown":
      return t("tokencalendar.project_unknown");
  }
  return p.name || t("tokencalendar.project_unknown");
}

/** A share as a percentage: whole above 10%, one decimal below, "<0.1%" for less. */
export function sharePercent(share: number): string {
  if (share > 0 && share < 0.001) return `<${formatNumber(0.001, { style: "percent", maximumFractionDigits: 1 })}`;
  return formatNumber(share, { style: "percent", maximumFractionDigits: share >= 0.1 ? 0 : 1 });
}
