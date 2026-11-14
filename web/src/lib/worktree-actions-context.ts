import { t } from "../i18n";
import { credentialOwner } from "./api";
import { currentHosts, ENGINE_HOSTS } from "./engines";
import type { SpaceLike } from "./agent-filter";
import { defaultManager, shownManager } from "./manager-choice";
import { sessionWorkspace } from "./agent-repos";
import { normalizePath } from "./files";
import type { Machine, Session } from "./types";
import { workspaceTarget, type HostWorkspace, type WorkspaceCatalogSnapshot } from "./workspace-catalog";
import type { WorkspaceTarget } from "./workspace-target";

export const worktreeActionKey = (target: WorkspaceTarget) => JSON.stringify([
  target.owner, target.manager, target.machineId, target.host, target.workspaceId, target.cwd,
]);

export interface WorktreeActionContext {
  catalog: WorkspaceCatalogSnapshot;
  machines: readonly Machine[];
  /** Granted only after an owner-bound full-device permission check. */
  writable: boolean;
  linkedWorktree?: boolean;
}

/** Hosts that make worktrees of a workspace: herdr and Orca, and Paseo of its Git workspaces, where Paseo makes them itself
 *  (docs/integrations/paseo.md, "Capabilities"); a Paseo folder has none. */
const makesWorktrees = (host: string, workspace?: HostWorkspace) =>
  ["herdr", "orca"].includes(host) || host === "paseo" && workspace?.workspace_kind === "git";

/** All entry points resolve the exact host-owned catalog row; paths are checked for replacement, never used to find it. */
export function resolveWorktreeAction(target: WorkspaceTarget, context: WorktreeActionContext) {
  const { catalog, machines, writable } = context;
  const owned = Boolean(target.owner && target.manager && target.owner === credentialOwner()
    && target.manager === shownManager() && catalog.owner === target.owner && catalog.manager === target.manager);
  const matches = owned ? catalog.workspaces.filter((w) => w.machine_id === target.machineId
    && w.source === target.host && w.source_id === target.workspaceId && w.source !== "session-directory") : [];
  const workspace = matches.length === 1 && matches[0].cwd === target.cwd ? matches[0] : undefined;
  const computers = machines.filter((m) => m.id === target.machineId && (m.manager || defaultManager()) === target.manager);
  const machine = computers.length === 1 ? computers[0] : undefined;
  const available = Boolean(workspace && machine?.online && workspace.online !== false && workspace.available !== false
    && currentHosts(machine).hosts.includes(target.host) && !machine.engines?.some((e) => e.id === target.host && !e.available));
  const reason = !owned ? t("worktree-actions.owner_changed") : !workspace || !available
    ? t("worktree-actions.unavailable") : !writable ? t("worktree-actions.read_only") : "";
  const parentRows = workspace?.parent_source_id ? catalog.workspaces.filter((w) => w.machine_id === target.machineId
    && w.source === target.host && w.source_id === workspace.parent_source_id && w.source_id !== workspace.source_id) : [];
  const parent = parentRows.length === 1 ? workspaceTarget(parentRows[0], catalog.owner, catalog.manager) : undefined;
  const hasChildren = Boolean(workspace && catalog.workspaces.some((w) => w.machine_id === target.machineId
    && w.source === target.host && w.parent_source_id === workspace.source_id));
  return { workspace, machine, parent, available, writable: !reason, reason, hasChildren,
    // These adapters implement host.Organizer + host.Launcher, and Paseo titles its workspaces itself. Other runtimes
    // retain copy/open compatibility.
    canRename: !reason && ["herdr", "orca", "paseo"].includes(target.host),
    canCreate: !reason && makesWorktrees(target.host, workspace),
    canReconcile: owned && writable && Boolean(machine?.online && currentHosts(machine).hosts.includes(target.host)
      && !machine.engines?.some((e) => e.id === target.host && !e.available))
      && (matches.length === 0 || Boolean(workspace && workspace.online !== false && workspace.available !== false)),
    canClose: !reason && target.host === "herdr",
    // Paseo deletes a worktree it made (its row says owned) by archiving its workspace; it would only archive another.
    canDelete: !reason && (target.host === "orca" || target.host === "herdr" && context.linkedWorktree === true
      || target.host === "paseo" && workspace?.workspace_kind === "git" && workspace.is_main === false && workspace.owned === true)
      && workspace?.is_main !== true && !hasChildren,
  };
}
export type ResolvedWorktreeAction = ReturnType<typeof resolveWorktreeAction>;

/** Creation sources for a logical space. Runtime folders suggest sources for the reviewed creation dialog only;
 *  they never establish workspace ownership for closing, renaming or deletion. */
export function spaceWorktreeSources(space: SpaceLike, catalog: WorkspaceCatalogSnapshot, machines: readonly Machine[]) {
  const sessions = space.sessions.filter((s) => s.machine_id === space.machineId && (!s.manager || s.manager === catalog.manager));
  const bound = new Set(sessions.map((s) => sessionWorkspace(s, catalog.workspaces, catalog.manager)));
  const path = (cwd: string) => normalizePath(cwd.replaceAll("\\", "/"));
  const folders = new Set(sessions.filter((s) => s.source_kind === "runtime" && !s.workspace_id
    && !ENGINE_HOSTS.includes(s.space_kind || s.host || ""))
    .map((s) => path(s.worktree || s.workspace_cwd || s.cwd || "")).filter(Boolean));
  return catalog.workspaces.filter((w) => w.machine_id === space.machineId && (bound.has(w) || folders.has(path(w.cwd))))
    .map((w) => workspaceTarget(w, catalog.owner, catalog.manager))
    .filter((target) => {
      const action = resolveWorktreeAction(target, { catalog, machines, writable: false });
      return action.available && makesWorktrees(target.host, action.workspace);
    });
}

/** A Paseo space (its project) is renamed in Paseo: offered while the catalog has the project's workspaces from Paseo on
 *  that computer, which a bridge that renames them lists (docs/integrations/paseo.md "Spaces and workspaces"). */
export function renamesRuntimeSpace(s: Session | undefined, catalog: WorkspaceCatalogSnapshot): boolean {
  if (!s?.space_id || s.source_kind !== "runtime" || (s.space_kind || s.host) !== "paseo") return false;
  if (s.manager && s.manager !== catalog.manager) return false;
  return catalog.workspaces.some((w) => w.machine_id === s.machine_id && w.source === "paseo" && w.project_id === s.space_id);
}

/** Preserve caller order, including pins, while using M2's exact attribution instead of matching cwd. */
export function firstDirectWorktreeAgent(target: WorkspaceTarget, catalog: WorkspaceCatalogSnapshot, sessions: readonly Session[]): Session | undefined {
  if (target.owner !== catalog.owner || target.manager !== catalog.manager) return;
  return sessions.find((session) => {
    const w = sessionWorkspace(session, catalog.workspaces, catalog.manager);
    return w?.machine_id === target.machineId && w.source === target.host && w.source_id === target.workspaceId;
  });
}

/** A catalog entry's safety-relevant metadata; title changes do not replace a runtime destination. */
export function worktreeSafetyStamp(workspace: HostWorkspace): string {
  return JSON.stringify([workspace.machine_id, workspace.source, workspace.source_id, workspace.cwd, workspace.workspace_kind, workspace.is_main]);
}
