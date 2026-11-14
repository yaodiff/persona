// Worktree creation transport and legacy terminal-menu helpers. Advanced choices require the owner’s advertised capabilities.
import { t as tr } from "../i18n";
import { api, ApiError, credentialOwner, enc, errorText } from "./api";
import type { MenuNode } from "./node-actions";
import { navigate } from "./router";
import { defaultManager, MANAGER_HEADER } from "./manager-choice";
import { getState } from "./store";
import type { WorkspaceTarget } from "./workspace-target";

export interface SparsePreset { id: string; name?: string; directories: string[] }
export interface WorktreeSource { type: "pr" | "issue"; number: number; title: string; url: string; branchName?: string; baseRefName?: string; isCrossRepository?: boolean }
export interface ComposerOptions { project: string; base_ref: string; refs: string[]; presets: SparsePreset[]; capabilities: string[] }
/** Repository metadata and capabilities returned by worktree/prepare. */
export interface WorktreePrepare {
  composer?: ComposerOptions;
  choose_parent?: boolean;
  automatic_name?: boolean;
  root: string;
  repoName: string;
  /** The branch checked out in the workspace; "" on a detached HEAD. */
  branch: string;
  /** The directory the computer puts this repository's checkouts under; a checkout of a branch is `path/<slug>`. */
  path: string;
}

/** What worktree/create opened. */
export interface WorktreeCreated {
  parent_workspace_id?: string;
  workspace_id: string;
  tab_id?: string;
  pane?: string;
  /** The terminal to open ("pane:<id>") when the workspace has a pane. */
  terminal?: string;
  path: string;
  branch: string;
}

export const WORKTREE_COPY = {
  reading: tr("worktree.reading"),
  branchRequired: tr("worktree.branch_required"),
  pathRequired: tr("worktree.path_required"),
  failed: tr("worktree.failed"),
  needsUpdate: tr("worktree.needs_update"),
  readFailed: tr("worktree.read_failed"),
};

/** The directory name herdr gives a branch's checkout: lower case, every run of characters other than ASCII letters and
 *  digits one "-", none at either end ("Feat/A_b.c" is "feat-a-b-c"). The same rule as the node's WorktreeSlug. */
export function branchSlug(branch: string): string {
  return branch.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
}

/** The checkout path a branch gets under `base` ("" when the branch has no letters or digits to name a folder with). */
export function suggestedPath(base: string, branch: string): string {
  const slug = branchSlug(branch);
  if (!base || !slug) return "";
  const sep = base.includes("\\") && !base.includes("/") ? "\\" : "/";
  return `${base.replace(/[\\/]+$/, "")}${sep}${slug}`;
}

/** Why git would not take this name for a branch, or "" when it would (git check-ref-format --branch). The computer
 *  checks again; this is so a typo is told as it is typed. */
export function branchProblem(raw: string): string {
  const b = raw.trim();
  if (!b) return WORKTREE_COPY.branchRequired;
  if (b.startsWith("-")) return tr("worktree.branch_dash");
  if (b === "@" || /[\s~^:?*[\\\u0000-\u001f\u007f]/.test(b)) return tr("worktree.branch_chars");
  if (b.includes("..") || b.includes("@{") || b.includes("//")) return tr("worktree.branch_pattern");
  if (b.startsWith("/") || b.endsWith("/") || b.endsWith(".") || b.endsWith(".lock")) return tr("worktree.branch_edges");
  if (b.split("/").some((part) => part.startsWith(".") || part.endsWith(".lock"))) return tr("worktree.branch_parts");
  return "";
}

/** The form: the branch and the path, and whether the path was put in by hand (then the branch stops moving it). */
export interface WorktreeForm {
  branch: string;
  path: string;
  pathEdited: boolean;
}

export const emptyForm: WorktreeForm = { branch: "", path: "", pathEdited: false };

/** The branch was typed: the path follows it, unless it was edited by hand. */
export function typeBranch(form: WorktreeForm, base: string, branch: string): WorktreeForm {
  return { ...form, branch, path: form.pathEdited ? form.path : suggestedPath(base, branch.trim()) };
}

/** The path was typed. Emptied, it goes back to following the branch. */
export function typePath(form: WorktreeForm, base: string, path: string): WorktreeForm {
  if (path === "") return { ...form, path: suggestedPath(base, form.branch.trim()), pathEdited: false };
  return { ...form, path, pathEdited: true };
}

/** What stops the form being sent, "" when nothing does. */
export function formProblem(form: WorktreeForm): string {
  return branchProblem(form.branch) || (form.path.trim() ? "" : WORKTREE_COPY.pathRequired);
}

/** The error of a worktree call as the dialog says it: an older hub or computer that has no such call is told to update. */
export function worktreeProblem(e: unknown, what: string): { text: string; needsUpdate: boolean } {
  const message = errorText(e);
  const old =
    e instanceof ApiError
      ? e.status === 404 || e.status === 405 || e.status === 501 || e.code === "unknown_method" || /unknown method|^unsupported/i.test(e.message)
      : false;
  if (old) return { text: WORKTREE_COPY.needsUpdate, needsUpdate: true };
  return { text: message ? tr("worktree.failed_with", { what, message }) : what, needsUpdate: false };
}

export interface WorktreeTarget {
  owner?: string;
  manager?: string;
  cwd?: string;
  mid: string;
  workspaceId: string;
  /** The host kind of the workspace ("herdr"); unset: the computer's own. */
  host?: string;
  label?: string;
}

const kindOf = (t: WorktreeTarget) => (t.host ? { kind: t.host } : {});

/** Every composer request stays bound to its original credentials and discovery manager. */
export function worktreeAdmission(target: WorktreeTarget): boolean {
  return (!target.owner || target.owner === credentialOwner())
    && (!target.manager || target.manager === (getState().machines.find((m) => m.id === target.mid)?.manager || defaultManager()));
}

export function worktreeHeaders(target: WorktreeTarget): Record<string, string> {
  if (!worktreeAdmission(target)) throw new Error(tr("worktree.owner_changed"));
  return { [MANAGER_HEADER]: target.manager || getState().machines.find((m) => m.id === target.mid)?.manager || defaultManager() };
}

export function prepareWorktree(t: WorktreeTarget, options: { composer?: boolean; signal?: AbortSignal } = {}): Promise<WorktreePrepare> {
  return api<WorktreePrepare>(`/machines/${enc(t.mid)}/worktree/prepare`, { method: "POST", signal: options.signal, timeout: 45_000,
    headers: worktreeHeaders(t), json: { workspace_id: t.workspaceId, ...kindOf(t), ...(options.composer !== undefined && t.host === "orca" ? { composer: options.composer } : {}) } });
}

export function worktreeSources(t: WorktreeTarget, query: string, signal?: AbortSignal) {
  return api<{ items: WorktreeSource[]; errors?: string[] | Record<string, { message?: string }>; warning?: string }>(`/machines/${enc(t.mid)}/worktree/source`, {
    method: "POST", headers: worktreeHeaders(t), signal, timeout: 45_000, json: { workspace_id: t.workspaceId, ...kindOf(t), query },
  });
}

export function saveWorktreePreset(t: WorktreeTarget, name: string, directories: string) {
  return api<{ preset: SparsePreset }>(`/machines/${enc(t.mid)}/worktree/preset`, {
    method: "POST", headers: worktreeHeaders(t), timeout: 45_000, json: { workspace_id: t.workspaceId, ...kindOf(t), name, directories },
  });
}

export type WorktreeCreateBody = { workspace_id: string; kind?: string; composer?: true; branch?: string; path?: string; auto_name?: true;
  name?: string; branch_override?: string; base_ref?: string; parent_workspace_id?: string; no_parent?: true; note?: string;
  sparse_preset_id?: string; source_id?: string; source_url?: string };
export type CreateOutcome = WorktreeCreated | { state: "pending" | "unknown" };

export function worktreeReceipt(t: WorktreeTarget, opId: string) {
  return api<{ state: string }>(`/ops/${enc(opId)}`, { headers: worktreeHeaders(t) });
}

export function sendWorktreeCreate(t: WorktreeTarget, body: WorktreeCreateBody, opId?: string): Promise<CreateOutcome> {
  return api<CreateOutcome>(`/machines/${enc(t.mid)}/worktree/create`, { method: "POST", timeout: 165_000,
    headers: { ...worktreeHeaders(t), ...(opId ? { "X-Persona-Op-Id": opId } : {}) }, json: body });
}

export async function createWorktree(t: WorktreeTarget, form: WorktreeForm): Promise<WorktreeCreated> {
  const result = await sendWorktreeCreate(t, { workspace_id: t.workspaceId, branch: form.branch.trim(), path: form.path.trim(), ...kindOf(t) });
  if (!isWorktreeCreated(result)) throw new Error(tr("worktree.unknown"));
  return result;
}

export function isWorktreeCreated(result: CreateOutcome): result is WorktreeCreated {
  return !("state" in result) && typeof result.workspace_id === "string" && Boolean(result.workspace_id)
    && typeof result.path === "string" && Boolean(result.path) && typeof result.branch === "string";
}

/** Opens the new workspace on the "Terminal" page (its first pane, else the machine's list). */
export function openCreated(mid: string, created: WorktreeCreated) {
  navigate(created.terminal ? { name: "terminal", mid, id: created.terminal } : { name: "terminal", mid });
}

// ── opening the dialog from a menu or the palette ────────

export interface WorktreeRequest {
  target: WorktreeTarget;
  /** The computer's tree changed (the new workspace is in it): read it again. */
  changed: () => void;
  source?: WorkspaceTarget;
  /** Undefined follows the source; null requests a top-level workspace. */
  parent?: WorkspaceTarget | null;
  onCreated?: (created: WorktreeCreated, target: WorkspaceTarget) => void;
}

const openers = new Set<(r: WorktreeRequest) => void>();

/** Opens "New worktree…" (the app shell listens) for a workspace node. */
export function openNewWorktree(node: Pick<MenuNode, "mid" | "workspaceId" | "host" | "label">, changed: () => void = () => {}) {
  if (!node.workspaceId) return;
  const request = { target: { mid: node.mid, workspaceId: node.workspaceId, host: node.host, label: node.label }, changed };
  openers.forEach((l) => l(request));
}

/** Delivers an already assembled creation request to the app shell. */
export function emitWorktreeRequest(request: WorktreeRequest) {
  openers.forEach((l) => l(request));
}

export function onOpenNewWorktree(l: (r: WorktreeRequest) => void) {
  openers.add(l);
  return () => {
    openers.delete(l);
  };
}

// ── a new agent's own worktree (#6115) ──
// worktree/add makes one from the agent's folder on persona/<its name>; worktree/status says whether a folder is one of
// Persona's and what removing it would lose; worktree/remove takes it away when the agent is closed and the user says so.

/** What worktree/status says of a folder (server/internal/proto/panes.go WorktreeStatus). `path` is set for any folder
 *  in a repository (its top level), `worktree` only for a linked worktree, `ours` only for one Persona made. */
export interface WorktreeStatus {
  worktree: boolean;
  ours: boolean;
  path?: string;
  branch?: string;
  root?: string;
  main_branch?: string;
  /** Where a new agent's worktree of this repository goes (newer nodes). */
  checkouts?: string;
  dirty: number;
  unmerged: number;
}

export interface WorktreeAdded {
  path: string;
  branch: string;
  root: string;
}

/** The branch a new agent's worktree gets, as the node names it: persona/<its name as a slug, 40 at most>, "agent"
 *  when the name has no ASCII letters or digits (node agentBranch). */
export function agentBranch(nameHint: string): string {
  let slug = branchSlug(nameHint);
  if (slug.length > 40) slug = slug.slice(0, 40).replace(/-+$/, "");
  return `persona/${slug || "agent"}`;
}

/** What names the agent's branch: its prompt, or its folder's name when the prompt has no ASCII letters or digits to
 *  name a branch with (a prompt in Chinese makes persona/<folder>, not persona/agent). */
export function agentNameHint(prompt: string, cwd: string): string {
  const p = prompt.trim();
  return branchSlug(p) ? p : (cwd.trim().replace(/\/+$/, "").split("/").pop() ?? "");
}

/** Null when the computer cannot say (an older node, offline): the switch and the cleanup then stay out of the way.
 *  kind: the engine the agent opens in (New Agent) or ran in (closing it: its `host`, which made its worktree; Paseo
 *  makes its own); none, the computer's default. */
export async function worktreeStatus(mid: string, cwd: string, signal?: AbortSignal, kind?: string): Promise<WorktreeStatus | null> {
  try {
    return await api<WorktreeStatus>(`/machines/${enc(mid)}/worktree/status`, { method: "POST", json: { cwd, ...(kind ? { kind } : {}) }, signal });
  } catch {
    return null;
  }
}

/** kind: the engine the agent opens in, which makes the worktree (Paseo, as a workspace of its own); none, the
 *  computer's default. */
export function addAgentWorktree(mid: string, cwd: string, nameHint: string, branch = "", owner: Pick<NonNullable<Parameters<typeof api>[1]>, "signal" | "token" | "tokenOwner"> = {}, kind?: string): Promise<WorktreeAdded> {
  return api<WorktreeAdded>(`/machines/${enc(mid)}/worktree/add`, {
    method: "POST",
    json: { cwd, ...(nameHint ? { name_hint: nameHint } : {}), ...(branch.trim() ? { branch: branch.trim() } : {}), ...(kind ? { kind } : {}) },
    ...owner,
  });
}

/** What removing a worktree would lose: changed files, and with the branch, its commits the main branch lacks. */
export function worktreeLoss(st: WorktreeStatus, deleteBranch: boolean): { dirty: number; unmerged: number } {
  return { dirty: st.dirty, unmerged: deleteBranch ? st.unmerged : 0 };
}

/** Removes it once its agent is closed: the node may still list the agent for a moment (worktree_in_use), so a few
 *  tries a second apart. `force` only when the user confirmed what is lost; `kind` the engine the agent ran in (its
 *  `host`). */
export async function removeAgentWorktree(mid: string, path: string, o: { deleteBranch: boolean; force: boolean; kind?: string }, tries = 6, wait = 1000): Promise<{ removed: boolean; branch_deleted: boolean }> {
  for (let i = 1; ; i++) {
    try {
      return await api<{ removed: boolean; branch_deleted: boolean }>(`/machines/${enc(mid)}/worktree/remove`, {
        method: "POST",
        json: { path, ...(o.deleteBranch ? { delete_branch: true } : {}), ...(o.force ? { force: true } : {}), ...(o.kind ? { kind: o.kind } : {}) },
      });
    } catch (e) {
      if (i >= tries || !(e instanceof ApiError) || e.code !== "worktree_in_use") throw e;
      await new Promise((r) => setTimeout(r, wait));
    }
  }
}
