import { t } from "../i18n";
import { api, ApiError, enc, errorText, onCredentialChange } from "./api";
import { MANAGER_HEADER, onManagerChoice } from "./manager-choice";
import type { OwnedWorkspaceTarget, ResolvedWorktreeAction } from "./worktree-actions";

export interface WorktreeDeletionPreview {
  workspace_id: string; kind: "orca" | "herdr" | "paseo"; host_id: "local"; instance_id: string;
  path: string; branch: string; dirty: number; folder: boolean; preview_token: string;
  close_only?: boolean;
}
export interface WorktreeDeletionResult {
  workspace_id: string; removed: boolean; state: "removed" | "removing" | "unknown" | "present" | "pending";
  branch_deleted?: boolean;
}
export type WorktreeActionPost = <T>(target: OwnedWorkspaceTarget, action: "rename" | "delete", body: Record<string, unknown>, signal: AbortSignal) => Promise<T>;
export const postWorktreeAction: WorktreeActionPost = (target, action, body, signal) => api(`/machines/${enc(target.machineId)}/worktree/${action}`, {
  method: "POST", headers: { [MANAGER_HEADER]: target.manager }, json: body, signal, timeout: 100_000,
});
export interface WorktreeMutationState {
  phase: "idle" | "renaming" | "previewing" | "ready" | "deleting" | "checking" | "uncertain" | "removed" | "invalid";
  preview?: WorktreeDeletionPreview;
  result?: WorktreeDeletionResult;
  error: string;
  /** Once dispatched, this attempt can only be reconciled, never submitted again. */
  dispatched: boolean;
}
interface Dependencies {
  resolve: () => ResolvedWorktreeAction;
  current: () => boolean;
  acknowledged: (action: "rename" | "delete" | "close") => void;
  post?: WorktreeActionPost;
}

/** One exact destination and one delete token. Even a transport failure permanently spends local confirmation. */
export class WorktreeMutation {
  private state: WorktreeMutationState = { phase: "idle", error: "", dispatched: false };
  private listeners = new Set<() => void>();
  private generation = 0;
  private controller?: AbortController;
  private off: (() => void)[] = [];
  constructor(readonly target: OwnedWorkspaceTarget, private deps: Dependencies) { this.activate(); }
  activate() {
    if (this.off.length) return;
    this.off = [onCredentialChange(() => this.invalidate()), onManagerChoice(() => this.invalidate())];
  }
  updateDependencies(deps: Dependencies) { this.deps = deps; }
  snapshot = () => this.state;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  private set(next: Partial<WorktreeMutationState>) {
    this.state = { ...this.state, ...next };
    this.listeners.forEach((listener) => listener());
  }
  cancel = () => {
    this.generation++;
    this.controller?.abort();
    this.controller = undefined;
    if (["renaming", "previewing", "deleting", "checking"].includes(this.state.phase)) {
      this.set({ phase: this.state.dispatched ? "uncertain" : "idle", error: this.state.dispatched ? t("worktree-actions.unknown") : "" });
    }
  };
  invalidate = () => {
    this.cancel();
    this.set({ phase: "invalid", error: t("worktree-actions.invalidated") });
  };
  dispose = () => { this.cancel(); this.off.forEach((off) => off()); this.off = []; };
  private admit(action: "rename" | "delete" | "close" | "reconcile") {
    if (this.state.phase === "invalid" || !this.deps.current()) return false;
    const context = this.deps.resolve();
    if (!(action === "rename" ? context.canRename : action === "reconcile" ? context.canReconcile : action === "close" ? context.canClose : context.canDelete)) {
      this.set({ error: context.reason || t(context.hasChildren ? "worktree-actions.has_children" : "worktree-actions.protected") });
      return false;
    }
    return true;
  }
  private begin() {
    this.controller?.abort();
    const controller = new AbortController();
    this.controller = controller;
    const generation = ++this.generation;
    return { signal: controller.signal, current: () => generation === this.generation && !controller.signal.aborted && this.deps.current() };
  }
  async rename(name: string): Promise<void> {
    name = name.trim();
    if (!name || this.state.dispatched || ["renaming", "removed"].includes(this.state.phase) || !this.admit("rename")) return;
    const attempt = this.begin();
    this.set({ phase: "renaming", error: "" });
    try {
      const result = await (this.deps.post || postWorktreeAction)<{ state?: string }>(this.target, "rename", {
        workspace_id: this.target.workspaceId, kind: this.target.host, name,
      }, attempt.signal);
      if (!attempt.current() || !this.admit("rename")) return;
      if (result.state === "pending" || result.state === "unknown") throw new Error(t("worktree-actions.rename_unknown"));
      this.set({ phase: "idle" });
      this.deps.acknowledged("rename");
    } catch (error) {
      if (attempt.current()) this.set({ phase: "idle", error: errorText(error) });
    }
  }
  async preview(closeOnly = false): Promise<void> {
    if (this.state.dispatched || ["previewing", "removed"].includes(this.state.phase) || !this.admit(closeOnly ? "close" : "delete")) return;
    const attempt = this.begin();
    this.set({ phase: "previewing", error: "", preview: undefined });
    try {
      const info = await (this.deps.post || postWorktreeAction)<WorktreeDeletionPreview>(this.target, "delete", {
        workspace_id: this.target.workspaceId, kind: this.target.host, preview: true, ...(closeOnly ? { close_only: true } : {}),
      }, attempt.signal);
      if (!attempt.current() || !this.admit(closeOnly ? "close" : "delete")) return;
      if (info.workspace_id !== this.target.workspaceId || info.kind !== this.target.host || info.host_id !== "local"
        || info.kind === "orca" && !info.instance_id || Boolean(info.close_only) !== closeOnly || closeOnly && !info.folder
        || !info.preview_token || info.path !== this.target.cwd || typeof info.branch !== "string"
        || typeof info.folder !== "boolean" || !Number.isSafeInteger(info.dirty) || info.dirty < 0) {
        throw new Error(t("worktree-actions.preview_invalid"));
      }
      this.set({ phase: "ready", preview: info });
    } catch (error) {
      if (attempt.current()) this.set({ phase: "idle", error: errorText(error) });
    }
  }
  async remove(discardChanges: boolean, deleteBranch: boolean): Promise<void> {
    const info = this.state.preview;
    if (this.state.phase !== "ready" || !info || this.state.dispatched || !this.admit(info.close_only ? "close" : "delete")) return;
    // Folder unregister never authorizes file or branch loss.
    if (!info.folder && info.dirty > 0 && !discardChanges) return;
    const attempt = this.begin();
    this.set({ phase: "deleting", error: "", dispatched: true });
    await this.finish({ workspace_id: this.target.workspaceId, kind: this.target.host, preview_token: info.preview_token,
      ...(info.close_only ? { close_only: true } : {}),
      ...(!info.folder && info.dirty > 0 && discardChanges ? { force: true } : {}),
      ...(!info.folder && deleteBranch ? { delete_branch: true } : {}),
    }, attempt);
  }
  async reconcile(): Promise<void> {
    const info = this.state.preview;
    if (!info || !this.state.dispatched || this.state.phase !== "uncertain" || !this.admit("reconcile")) return;
    const attempt = this.begin();
    this.set({ phase: "checking", error: "" });
    await this.finish({ workspace_id: this.target.workspaceId, kind: this.target.host, preview_token: info.preview_token, reconcile: true,
      ...(info.close_only ? { close_only: true } : {}),
    }, attempt);
  }
  private async finish(body: Record<string, unknown>, attempt: { signal: AbortSignal; current: () => boolean }) {
    try {
      const result = await (this.deps.post || postWorktreeAction)<WorktreeDeletionResult>(this.target, "delete", body, attempt.signal);
      if (!attempt.current() || !this.admit("reconcile")) return;
      if (result.workspace_id !== this.target.workspaceId) throw new Error(t("worktree-actions.preview_invalid"));
      if (result.removed === true && result.state === "removed") {
        this.set({ phase: "removed", result });
        this.deps.acknowledged(this.state.preview?.close_only ? "close" : "delete");
      } else {
        this.set({ phase: "uncertain", result, error: t("worktree-actions.unknown") });
      }
    } catch (error) {
      if (!attempt.current()) return;
      if (error instanceof ApiError && error.code === "worktree_herdr_children") {
        this.set({ phase: "invalid", error: errorText(error) });
        return;
      }
      this.set({ phase: "uncertain", error: `${errorText(error)}\n${t("worktree-actions.unknown")}` });
    }
  }
}
