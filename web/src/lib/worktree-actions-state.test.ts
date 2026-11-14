// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, credentialOwner, setToken } from "./api";
import { chooseManager, resetManagerChoice, setRosterDefault } from "./manager-choice";
import { WorktreeMutation, postWorktreeAction, type WorktreeActionPost, type WorktreeDeletionPreview } from "./worktree-actions-state";
import type { OwnedWorkspaceTarget, ResolvedWorktreeAction } from "./worktree-actions";

let target: OwnedWorkspaceTarget;
let context: ResolvedWorktreeAction;
let current: boolean;
let calls: { action: string; body: Record<string, unknown>; signal: AbortSignal; resolve: (value: unknown) => void; reject: (error: Error) => void }[];
let model: WorktreeMutation;
const acknowledged = vi.fn();
const preview = (extra: Partial<WorktreeDeletionPreview> = {}): WorktreeDeletionPreview => ({ workspace_id: "work", kind: "orca", host_id: "local", instance_id: "instance", path: "/repo/work", branch: "topic", dirty: 0, folder: false, preview_token: "opaque-preview", ...extra });
const post: WorktreeActionPost = (_target, action, body, signal) => new Promise((resolve, reject) => calls.push({ action, body, signal, resolve: resolve as (value: unknown) => void, reject }));
const ready = async (extra: Partial<WorktreeDeletionPreview> = {}) => { const pending = model.preview(); calls.at(-1)!.resolve(preview(extra)); await pending; };
const done = () => ({ workspace_id: "work", removed: true, state: "removed" });
beforeEach(() => {
  resetManagerChoice(); setRosterDefault("manager"); setToken("synthetic-mutations"); current = true; calls = []; acknowledged.mockClear();
  target = { owner: credentialOwner(), manager: "manager", machineId: "computer", host: "orca", workspaceId: "work", cwd: "/repo/work" };
  context = { writable: true, available: true, canClose: false, canDelete: true, canReconcile: true, canRename: true, canCreate: true, hasChildren: false, reason: "", workspace: undefined, machine: undefined, parent: undefined };
  model = new WorktreeMutation(target, { resolve: () => context, current: () => current, acknowledged, post });
});
afterEach(() => { model.dispose(); vi.unstubAllGlobals(); });

it("binds herdr close confirmation and reconciliation to the file-preserving operation", async () => {
  target.host = "herdr"; context.canClose = true; context.canDelete = false;
  const pending = model.preview(true);
  expect(calls[0].body).toEqual({ workspace_id: "work", kind: "herdr", preview: true, close_only: true });
  calls[0].resolve(preview({ kind: "herdr", instance_id: "", folder: true, close_only: true })); await pending;
  const removal = model.remove(true, true);
  expect(calls[1].body).toEqual({ workspace_id: "work", kind: "herdr", preview_token: "opaque-preview", close_only: true });
  calls[1].reject(new Error("Connection lost")); await removal;
  const check = model.reconcile();
  expect(calls[2].body).toEqual({ workspace_id: "work", kind: "herdr", preview_token: "opaque-preview", reconcile: true, close_only: true });
  calls[2].resolve(done()); await check;
  expect(acknowledged).toHaveBeenCalledExactlyOnceWith("close");
});

it("rejects a deletion preview returned for a close request", async () => {
  target.host = "herdr"; context.canClose = true;
  const pending = model.preview(true); calls[0].resolve(preview({ kind: "herdr", instance_id: "" })); await pending;
  expect(model.snapshot().phase).toBe("idle"); expect(model.snapshot().error).toBeTruthy();
  await model.remove(true, true); expect(calls).toHaveLength(1);
});

it("retires a parent-close confirmation refused by herdr so reopening gets a fresh preview", async () => {
  target.host = "herdr"; context.canClose = true;
  const pending = model.preview(true);
  calls[0].resolve(preview({ kind: "herdr", instance_id: "", folder: true, close_only: true })); await pending;
  const close = model.remove(false, false);
  calls[1].reject(new ApiError(409, "Close the linked workspaces first", { code: "worktree_herdr_children" })); await close;
  expect(model.snapshot().phase).toBe("invalid");
  expect(model.snapshot().error).toBe("Close the linked workspaces first");
});

it("pins the exact manager and canonical runtime id in the existing API transport/envelope", async () => {
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 })); vi.stubGlobal("fetch", fetcher);
  await postWorktreeAction(target, "rename", { workspace_id: target.workspaceId, kind: target.host, name: "New name" }, new AbortController().signal);
  const [url, init] = fetcher.mock.calls[0];
  expect(url).toBe("/api/v1/machines/computer/worktree/rename");
  expect(new Headers(init.headers).get("X-Persona-Manager")).toBe("manager");
  expect(JSON.parse(init.body)).toEqual({ workspace_id: "work", kind: "orca", name: "New name" });
});
it("renames only the worktree, serializes fast submissions and refreshes only on acknowledgement", async () => {
  const pending = model.rename("  New name  "); void model.rename("Again");
  expect(calls).toHaveLength(1); expect(calls[0].body).toEqual({ workspace_id: "work", kind: "orca", name: "New name" });
  expect(acknowledged).not.toHaveBeenCalled(); calls[0].reject(new Error("rename refused")); await pending;
  expect(model.snapshot().error).toBe("rename refused"); expect(acknowledged).not.toHaveBeenCalled();
  const retry = model.rename("New name"); calls[1].resolve({}); await retry; expect(acknowledged).toHaveBeenCalledExactlyOnceWith("rename");
});
for (const state of ["pending", "unknown"]) it(`never acknowledges a ${state} rename`, async () => {
  const pending = model.rename("new"); calls[0].resolve({ state }); await pending; expect(acknowledged).not.toHaveBeenCalled(); expect(model.snapshot().error).toBeTruthy();
});

describe("one-use safe removal", () => {
  it("previews before mutation and keeps the branch by default", async () => {
    await model.remove(true, true); expect(calls).toHaveLength(0);
    await ready(); expect(calls[0].body).toEqual({ workspace_id: "work", kind: "orca", preview: true });
    const pending = model.remove(false, false); void model.remove(false, false);
    expect(calls).toHaveLength(2); expect(calls[1].body).toEqual({ workspace_id: "work", kind: "orca", preview_token: "opaque-preview" });
    calls[1].resolve(done()); await pending; expect(acknowledged).toHaveBeenCalledExactlyOnceWith("delete");
  });
  it("requires an explicit dirty acknowledgement and never waives archive or PTY guards", async () => {
    await ready({ dirty: 3 }); await model.remove(false, false); expect(calls).toHaveLength(1);
    const pending = model.remove(true, true);
    expect(calls[1].body).toEqual({ workspace_id: "work", kind: "orca", preview_token: "opaque-preview", force: true, delete_branch: true });
    calls[1].reject(new Error("Archive hook refused")); await pending;
    await model.remove(true, true); await model.preview(); expect(calls).toHaveLength(2); expect(acknowledged).not.toHaveBeenCalled();
    const check = model.reconcile(); expect(calls[2].body).toEqual({ workspace_id: "work", kind: "orca", preview_token: "opaque-preview", reconcile: true });
    calls[2].resolve({ workspace_id: "work", removed: false, state: "present" }); await check;
    expect(model.snapshot().phase).toBe("uncertain");
  });
  it("deletes a Paseo worktree through the same preview, acknowledgement and one-use token", async () => {
    target.host = "paseo";
    const pending = model.preview(); calls[0].resolve(preview({ kind: "paseo", dirty: 2 })); await pending;
    expect(calls[0].body).toEqual({ workspace_id: "work", kind: "paseo", preview: true });
    await model.remove(false, false); expect(calls).toHaveLength(1);
    const removing = model.remove(true, false);
    expect(calls[1].body).toEqual({ workspace_id: "work", kind: "paseo", preview_token: "opaque-preview", force: true });
    calls[1].resolve(done()); await removing; expect(acknowledged).toHaveBeenCalledExactlyOnceWith("delete");
  });
  it("folder unregister cannot send force or branch deletion even with dirty metadata", async () => {
    await ready({ folder: true, dirty: 8 }); const pending = model.remove(true, true);
    expect(calls[1].body).toEqual({ workspace_id: "work", kind: "orca", preview_token: "opaque-preview" }); calls[1].resolve(done()); await pending;
  });
  for (const result of [{ removed: false, state: "removed" }, { removed: true, state: "removing" }, { removed: false, state: "unknown" }, { removed: false, state: "pending" }, { removed: false, state: "present" }]) {
    it(`retains ${JSON.stringify(result)} and only checks status with the same token`, async () => {
      await ready(); const pending = model.remove(false, false); calls[1].resolve({ workspace_id: "work", ...result }); await pending;
      expect(acknowledged).not.toHaveBeenCalled(); expect(model.snapshot().phase).toBe("uncertain");
      await model.remove(true, true); await model.preview(); expect(calls).toHaveLength(2);
      const check = model.reconcile(); expect(calls[2].body).toEqual({ workspace_id: "work", kind: "orca", preview_token: "opaque-preview", reconcile: true }); calls[2].resolve(done()); await check;
      expect(acknowledged).toHaveBeenCalledExactlyOnceWith("delete");
    });
  }
  it("a timeout cannot turn into a fresh preview or deletion after close/reopen", async () => {
    await ready(); const pending = model.remove(false, false); model.cancel(); calls[1].resolve(done()); await pending;
    expect(calls[1].signal.aborted).toBe(true); expect(model.snapshot().phase).toBe("uncertain"); expect(acknowledged).not.toHaveBeenCalled();
    await model.remove(true, true); await model.preview(); expect(calls).toHaveLength(2);
  });
  for (const extra of [{ workspace_id: "other" }, { path: "/other" }, { host_id: "remote" }, { instance_id: "" }, { preview_token: "" }, { dirty: -1 }, { dirty: 0.5 }]) {
    it(`refuses malformed or replaced preview ${JSON.stringify(extra)}`, async () => {
      await ready(extra as Partial<WorktreeDeletionPreview>); expect(model.snapshot().preview).toBeUndefined(); expect(model.snapshot().error).toBeTruthy();
      await model.remove(true, true); expect(calls).toHaveLength(1);
    });
  }
  it("keeps unsupported old runtime and backend main/children refusals visible", async () => {
    const pending = model.preview(); calls[0].reject(new Error("Runtime cannot safely remove worktrees")); await pending;
    expect(model.snapshot().error).toContain("cannot safely"); await model.remove(true, true); expect(calls).toHaveLength(1);
    context.canDelete = false; context.hasChildren = true; await model.preview(); expect(calls).toHaveLength(1);
  });
});
for (const action of ["rename", "preview", "remove"] as const) {
  for (const change of ["credential", "manager", "choice", "target", "availability"] as const) it(`fences a late ${action} result after ${change} changes`, async () => {
    if (action === "remove") await ready();
    const pending = action === "rename" ? model.rename("new") : action === "preview" ? model.preview() : model.remove(false, false);
    if (change === "credential") { setToken("synthetic-other"); setToken("synthetic-mutations"); }
    if (change === "manager") { setRosterDefault("other"); setRosterDefault("manager"); }
    if (change === "choice") { chooseManager("other"); chooseManager(null); }
    if (change === "target") { current = false; model.cancel(); }
    if (change === "availability") { context.writable = false; context.canRename = false; context.canDelete = false; model.invalidate(); }
    calls.at(-1)!.resolve(action === "rename" ? {} : action === "preview" ? preview() : done()); await pending;
    expect(acknowledged).not.toHaveBeenCalled();
    expect(model.snapshot().phase).not.toBe("removed");
  });
}
