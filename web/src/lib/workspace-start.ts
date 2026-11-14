// New Agent's workspaces (#7259): the chosen computer's catalog workspaces an Agent can start in, Paseo's main checkouts,
// worktrees and folders included. The same rule as iOS's `WorkspaceStart` and Android's `NewAgentWorkspaces`: only
// engines that keep checkouts (Orca, Paseo), only rows the computer can use now, grouped by project, main checkout first,
// then worktrees, then folders.
import type { HostWorkspace } from "./workspace-catalog";

/** The engines whose workspaces are checkouts and folders a start can name (`workspace_id` + `host`). A terminal host's
 *  space (herdr) is joined from its own menu, not chosen here. */
export const START_SOURCES: ReadonlySet<string> = new Set(["orca", "paseo"]);

export type StartKind = "main" | "worktree" | "folder";
const KIND_ORDER: Record<StartKind, number> = { main: 0, worktree: 1, folder: 2 };

/** One project's workspaces; `title` "" is the last group, the workspaces in no project. */
export interface StartGroup { key: string; title: string; workspaces: HostWorkspace[] }

export function startKind(w: HostWorkspace): StartKind {
  if (w.workspace_kind === "folder") return "folder";
  return w.is_main ? "main" : "worktree";
}

function lastElement(path: string): string {
  const trimmed = path.replace(/[\\/]+$/, "");
  return trimmed.split(/[\\/]/).pop() || trimmed || path;
}

/** What the row is called: the workspace's title, else its branch, else its folder's name. */
export function startTitle(w: HostWorkspace): string {
  return [w.title, w.branch].find((name) => name?.trim()) ?? lastElement(w.cwd);
}

/** The branch said after the kind word: a Git workspace's, when the title does not already say it. */
export function startBranch(w: HostWorkspace): string | undefined {
  return startKind(w) !== "folder" && w.branch && w.branch !== startTitle(w) ? w.branch : undefined;
}

/** The project's name, else its main checkout's folder name; "" in none. */
export function startProject(w: HostWorkspace): string {
  const name = w.project_name?.trim();
  if (name) return name;
  return w.project_path ? lastElement(w.project_path) : "";
}

/** The workspaces offered for `machineId`: its own, of the engines above, that it can start in now. */
export function startChoices(catalog: readonly HostWorkspace[], machineId: string): HostWorkspace[] {
  return catalog.filter((w) => w.machine_id === machineId && START_SOURCES.has(w.source) && w.available !== false && w.online !== false);
}

const compare = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" });

export function startGroups(choices: readonly HostWorkspace[]): StartGroup[] {
  const groups = new Map<string, StartGroup>();
  for (const w of choices) {
    const title = startProject(w);
    const key = title ? `${w.source}\u001f${w.project_id || title}` : "";
    const group = groups.get(key) ?? { key, title, workspaces: [] };
    group.workspaces.push(w);
    groups.set(key, group);
  }
  for (const g of groups.values()) {
    g.workspaces.sort((a, b) => KIND_ORDER[startKind(a)] - KIND_ORDER[startKind(b)] || compare(startTitle(a), startTitle(b))
      || (a.source + a.source_id).localeCompare(b.source + b.source_id));
  }
  return [...groups.values()].sort((a, b) => Number(!a.title) - Number(!b.title) || compare(a.title, b.title) || a.key.localeCompare(b.key));
}
