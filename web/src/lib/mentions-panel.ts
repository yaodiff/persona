// Panel-only helpers for the composer's lazy + / @ menu (ComposerPanel.tsx, AssistantComposerPanel.tsx).
// Kept separate from lib/mentions.ts so session/workspace matching and lib-mentions strings stay off the first screen.
import { t } from "../i18n";
import { listedKeys, rowKey } from "./child-sessions";
import { foldRows } from "./folded-rows";
import { shortPath } from "./format";
import { agentLabel, displayTitle, folderName } from "./mentions";
import { byTime } from "./times";
import type { Session } from "./types";
import type { Workspace } from "./workspace-board";

/** A path as typed after @: relative to the session's folder when inside it. */
export function relPath(path: string, cwd?: string): string {
  if (!cwd) return path;
  const base = cwd.replace(/\/+$/, "");
  return path.startsWith(`${base}/`) ? path.slice(base.length + 1) : path;
}

/** Where to list for a query with a slash in it: the folder part (relative to cwd, or absolute / ~) and the rest. */
export function browseTarget(query: string, cwd?: string): { dir: string; typed: string; filter: string } | null {
  const slash = query.lastIndexOf("/");
  if (slash < 0) return null;
  const typed = query.slice(0, slash + 1); // what stays typed in front of a picked name
  const filter = query.slice(slash + 1);
  const part = typed.replace(/\/+$/, "");
  let dir: string;
  if (typed.startsWith("/")) dir = part || "/";
  else if (typed.startsWith("~")) dir = part || "~";
  else if (cwd) dir = `${cwd.replace(/\/+$/, "")}/${part}`;
  else return null;
  return { dir, typed, filter };
}

/** Whether a name matches what is typed: anywhere in it, case-insensitive; hidden files only when asked for. */
export function nameMatches(name: string, filter: string): boolean {
  const f = filter.toLowerCase();
  if (name.startsWith(".") && !f.startsWith(".")) return false;
  return name.toLowerCase().includes(f);
}

/** Ranks matches: names that start with the query first, then the rest, each keeping its order. */
export function byPrefix<T>(rows: T[], name: (t: T) => string, filter: string): T[] {
  const f = filter.toLowerCase();
  if (!f) return rows;
  const head = rows.filter((r) => name(r).toLowerCase().startsWith(f));
  return [...head, ...rows.filter((r) => !name(r).toLowerCase().startsWith(f))];
}

/** The sessions this page holds, each once (#12983): the chats opened here as their complete rows (the store's
 *  `sessions`), then the Agents list's rows of the others (`sessionSummaries`). A paging hub pushes no `sessions`
 *  (docs/lessons/web.md), so an Agent nobody opened here is only in the list. Child sessions fold into their parent,
 *  as in every list (#5120). */
export function pageSessions(sessions: Record<string, Session[]>, listed: readonly Session[]): Session[] {
  const complete = Object.values(sessions).flat();
  const opened = listedKeys(complete);
  return foldRows([...complete, ...listed.filter((s) => !opened.has(rowKey(s.machine_id, s.session_id)))]);
}

/** Other agents' sessions, most recently active first, at most `limit` (PromptItems.sessions). */
export function otherSessions(all: Session[], except: string | undefined, query: string, limit = 8): Session[] {
  const q = query.toLowerCase();
  return all
    .filter((s) => !s.assistant && `${s.machine_id}/${s.session_id}` !== except)
    .filter((s) => !q || displayTitle(s).toLowerCase().includes(q) || sessionDetail(s).toLowerCase().includes(q))
    .sort((a, b) => byTime(b.updated_at, a.updated_at))
    .slice(0, limit);
}

export function sessionDetail(s: Session): string {
  return [agentLabel(s), folderName(s.cwd)].filter(Boolean).join(" · ");
}

/** The quote for a picked session, sent as a <persona-context> before the message (PromptItems.quote). */
export function sessionQuote(s: Session): { title: string; url: string; text: string } {
  const lines = [t("lib-mentions.quote_session", { title: displayTitle(s), agent: agentLabel(s) }), t("lib-mentions.quote_cwd", { cwd: s.cwd })];
  const last = s.last_text?.trim();
  if (last) lines.push(t("lib-mentions.quote_last", { text: [...last].slice(0, 600).join("") }));
  return { title: displayTitle(s), url: `persona://session/${s.machine_id}/${s.session_id}`, text: lines.join("\n") };
}

/** Workspaces (GET /workspaces, docs/web/workspace-board.md) matching the query by title, folder or computer: ones whose
 * title starts with it first, then available before unavailable, each keeping the hub's order. One per computer and
 * folder (a host space and a session folder can name the same place); folderless host spaces can't be worked in. */
export function workspaceMatches(all: Workspace[], query: string, machineName: (id: string) => string, limit = 8): Workspace[] {
  const q = query.toLowerCase();
  const seen = new Set<string>();
  const hits = all.filter((w) => {
    if (!w.cwd) return false;
    const key = `${w.machine_id}\n${w.cwd.replace(/\/+$/, "")}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return !q || [workspaceTitle(w), w.cwd, machineName(w.machine_id), w.branch ?? ""].some((x) => x.toLowerCase().includes(q));
  });
  const ranked = byPrefix(hits, workspaceTitle, q);
  return [...ranked.filter((w) => w.available !== false), ...ranked.filter((w) => w.available === false)].slice(0, limit);
}

/** A workspace's name: the host's title, else its folder's. */
export function workspaceTitle(w: Pick<Workspace, "title" | "cwd">): string {
  return w.title?.trim() || folderName(w.cwd) || w.cwd;
}

/** The @ menu's second line for a workspace: computer · folder (· branch). */
export function workspaceDetail(w: Workspace, machineName: (id: string) => string): string {
  return [machineName(w.machine_id), shortPath(w.cwd), w.branch].filter(Boolean).join(" · ");
}

/** The context for a picked workspace: `<persona-context kind="workspace" machine="…" cwd="…">`. The hub reads the two
 * attributes and adds what it knows about the place (docs/assistant/assistant.md "Workspace mentions"); the body is for people
 * and clients that show it as is. */
export function workspaceContext(w: Workspace, machineName: (id: string) => string): { title: string; url: string; text: string; attrs: Record<string, string> } {
  const title = workspaceTitle(w);
  const lines = [t("lib-mentions.workspace_title", { title }), t("lib-mentions.workspace_machine", { machine: machineName(w.machine_id) }), t("lib-mentions.quote_cwd", { cwd: w.cwd })];
  if (w.branch) lines.push(t("lib-mentions.workspace_branch", { branch: w.branch }));
  return { title, url: "", text: lines.join("\n"), attrs: { machine: w.machine_id, cwd: w.cwd, ...(w.branch ? { branch: w.branch } : {}) } };
}
