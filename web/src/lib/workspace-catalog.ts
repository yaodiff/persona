import { useEffect, useMemo, useSyncExternalStore } from "react";
import { api, credentialOwner, onCredentialChange, subscribeCredentialOwner } from "./api";
import { shownManager, MANAGER_HEADER, onManagerChoice, useShownManager } from "./manager-choice";
import { directoryKey } from "./workspace-directory";
import type { WorkspaceTarget as RuntimeWorkspaceTarget } from "./workspace-target";

/** The host inventory, independent of board settings and of the Agents currently running. */
export interface HostWorkspace {
  id: string; machine_id: string; source: string; source_id: string; title: string; cwd: string;
  parent_source_id?: string; project_id?: string; project_name?: string; project_path?: string;
  is_main?: boolean; unread?: boolean; sidebar_status?: string; workspace_kind?: string;
  head?: string; index?: number; branch?: string; online?: boolean; available?: boolean; last_seen?: string;
  /** An engine runtime's worktree that the runtime made and removes with it (Paseo's, #6297): the ones it deletes. */
  owned?: boolean;
  /** The host's workspace pin, synchronized independently of Persona's own session and project pins. */
  pinned_at?: string;
  /** Manager-owned visibility from hidden_agent_dirs rules; sidebars drop hidden empty workspaces. */
  hidden_by_directory?: boolean;
}

/** A valid host pin's time; an old hub or an unpinned workspace has no timestamp. */
export function workspacePinTime(workspace?: Pick<HostWorkspace, "pinned_at">): number | undefined {
  const time = Date.parse(workspace?.pinned_at ?? "");
  return Number.isFinite(time) ? time : undefined;
}

/** A workspace branch or repository follows its newest visible host pin before its ordinary sort order. */
export const byWorkspacePin = (a: { latestPinTime?: number }, b: { latestPinTime?: number }) =>
  (b.latestPinTime ?? -Infinity) - (a.latestPinTime ?? -Infinity);
/** Discovered targets always carry their credential/manager partition and human label. */
export interface WorkspaceTarget extends RuntimeWorkspaceTarget {
  owner: string;
  manager: string;
  label: string;
}
export type WorkspaceCatalogOwner = Pick<WorkspaceTarget, "owner" | "manager">;
/** How often a visible page re-reads the catalog while it is watched (#5168). Worktrees change rarely, and the web's own
 *  changes (creating, renaming, removing one) refresh it at once (`refreshWorkspaceCatalog`), as does a page coming back
 *  into view. Each read asks every online computer live (`workspaces.list`), so this is 60 s, as on Apple (#4685). */
export const CATALOG_POLL_MS = 60_000;
export interface WorkspaceCatalogSnapshot extends WorkspaceCatalogOwner {
  workspaces: HostWorkspace[];
  /** Undefined until discovery answers, null for a failed request, otherwise the machines with partial results. */
  failedMachines?: string[] | null;
}
export interface WorkspaceCatalogList { workspaces: HostWorkspace[]; errors?: { machine_id: string; source?: string; error: string }[] }

export function workspaceCatalogOwner(): WorkspaceCatalogOwner {
  return { owner: credentialOwner(), manager: shownManager() };
}
export function workspaceKey(w: Pick<HostWorkspace, "machine_id" | "source" | "source_id" | "cwd">, owner: string, manager: string): string {
  const identity = [owner, manager, w.machine_id, w.source, w.source_id];
  // A desktop project or saved environment can contain several roots under one source-local id.
  if (["codex_app", "claude_desktop"].includes(w.source)) identity.push(directoryKey(w.cwd) ?? w.cwd);
  return JSON.stringify(identity);
}
export function workspaceTarget(w: HostWorkspace, owner: string, manager: string): WorkspaceTarget {
  return { owner, manager, machineId: w.machine_id, host: w.source, workspaceId: w.source_id, cwd: w.cwd, label: w.title || w.branch || w.cwd };
}

type Transport = <T>(path: string, init?: RequestInit) => Promise<T>;
const sameOwner = (a: WorkspaceCatalogOwner, b: WorkspaceCatalogOwner) => a.owner === b.owner && a.manager === b.manager;

/** Each credential/manager partition keeps its last successful inventory. Only the newest read can replace it. */
export class WorkspaceCatalog {
  private state: WorkspaceCatalogSnapshot;
  private listeners = new Set<() => void>();
  private reads = 0;
  private controller?: AbortController;
  private watchers = 0;
  private stopPolling?: () => void;
  constructor(scope: WorkspaceCatalogOwner, private request: Transport = api, private currentOwner = workspaceCatalogOwner) {
    this.state = { ...scope, workspaces: [] };
  }
  snapshot = () => this.state;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  cancel = () => { this.reads++; this.controller?.abort(); this.controller = undefined; };
  async refresh(): Promise<void> {
    if (!sameOwner(this.state, this.currentOwner())) return;
    this.cancel();
    const read = this.reads;
    const controller = new AbortController();
    this.controller = controller;
    try {
      // Pin even the default manager: a roster switch while this request is in flight cannot redirect it.
      const [result, { reconcileInventory }] = await Promise.all([
        this.request<WorkspaceCatalogList>("/workspaces", { signal: controller.signal, headers: { [MANAGER_HEADER]: this.state.manager } }),
        import("./workspace-inventory"),
      ]);
      if (controller.signal.aborted || read !== this.reads || !sameOwner(this.state, this.currentOwner())) return;
      if (!Array.isArray(result.workspaces)) throw new Error("Invalid workspace inventory");
      const next = reconcileInventory(this.state, result);
      if (next === this.state) return;
      this.state = next;
      this.listeners.forEach((listener) => listener());
    } catch {
      if (!controller.signal.aborted && read === this.reads && sameOwner(this.state, this.currentOwner()) && this.state.failedMachines !== null) {
        this.state = { ...this.state, failedMachines: null };
        this.listeners.forEach(listener => listener());
      }
    } finally {
      if (read === this.reads) this.controller = undefined;
    }
  }
  /** Shared by sidebar and phone consumers. Hidden pages neither poll nor accept an outstanding read. */
  watch(page: Document = document): () => void {
    this.watchers++;
    if (this.watchers === 1) {
      let timer: ReturnType<typeof setTimeout> | undefined;
      let generation = 0;
      const visibility = () => {
        const current = ++generation;
        if (timer) clearTimeout(timer);
        timer = undefined;
        this.cancel();
        if (page.visibilityState === "hidden") return;
        const poll = async () => {
          await this.refresh();
          if (current === generation) timer = setTimeout(() => { void poll(); }, CATALOG_POLL_MS);
        };
        void poll();
      };
      page.addEventListener("visibilitychange", visibility);
      visibility();
      this.stopPolling = () => {
        generation++;
        page.removeEventListener("visibilitychange", visibility);
        if (timer) clearTimeout(timer);
        this.cancel();
      };
    }
    return () => { if (--this.watchers === 0) { this.stopPolling?.(); this.stopPolling = undefined; } };
  }
}

let cachedOwner = "";
const catalogs = new Map<string, WorkspaceCatalog>();
function catalogFor(scope: WorkspaceCatalogOwner): WorkspaceCatalog {
  if (cachedOwner !== scope.owner) {
    catalogs.forEach((catalog) => catalog.cancel());
    catalogs.clear();
    cachedOwner = scope.owner;
  }
  let catalog = catalogs.get(scope.manager);
  if (!catalog) { catalog = new WorkspaceCatalog(scope); catalogs.set(scope.manager, catalog); }
  return catalog;
}
// Retire in-flight reads immediately, including a switch away and back before a response completes.
onManagerChoice(() => catalogs.forEach((catalog) => catalog.cancel()));
onCredentialChange(() => catalogs.forEach((catalog) => catalog.cancel()));

/** Refresh after an acknowledged mutation. Returning to a hidden page always refreshes before resuming polling. */
export function refreshWorkspaceCatalog(): Promise<void> {
  return typeof document !== "undefined" && document.visibilityState === "hidden"
    ? Promise.resolve() : catalogFor(workspaceCatalogOwner()).refresh();
}
export function useWorkspaceCatalog(enabled: boolean): WorkspaceCatalogSnapshot {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  const manager = useShownManager();
  const catalog = useMemo(() => catalogFor({ owner, manager }), [owner, manager]);
  const snapshot = useSyncExternalStore(catalog.subscribe, catalog.snapshot, catalog.snapshot);
  useEffect(() => enabled ? catalog.watch() : undefined, [catalog, enabled]);
  return snapshot;
}
