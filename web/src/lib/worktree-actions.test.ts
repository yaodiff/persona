// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { setToken, credentialOwner } from "./api";
import { chooseManager, resetManagerChoice, setRosterDefault } from "./manager-choice";
import type { Machine, Session } from "./types";
import type { HostWorkspace, WorkspaceCatalogSnapshot } from "./workspace-catalog";
import type { OwnedWorkspaceTarget } from "./worktree-actions";
import { firstDirectWorktreeAgent, renamesRuntimeSpace, resolveWorktreeAction, spaceWorktreeSources } from "./worktree-actions-context";
import type { SpaceLike } from "./agent-filter";
import { sessionWorkspace } from "./agent-repos";

let target: OwnedWorkspaceTarget;
let catalog: WorkspaceCatalogSnapshot;
let machines: Machine[];
const row = (id: string, extra: Partial<HostWorkspace> = {}): HostWorkspace => ({ id, machine_id: "computer", source: "orca", source_id: id, title: id, cwd: `/repo/${id}`, ...extra });
const resolve = (writable = true) => resolveWorktreeAction(target, { catalog, machines, writable });
beforeEach(() => {
  resetManagerChoice(); setRosterDefault("manager"); setToken("synthetic-actions");
  catalog = { owner: credentialOwner(), manager: "manager", workspaces: [row("work"), row("parent")] };
  target = { owner: catalog.owner, manager: catalog.manager, machineId: "computer", host: "orca", workspaceId: "work", cwd: "/repo/work", label: "Work" };
  machines = [{ id: "computer", manager: "manager", name: "Computer", online: true, engine_hosts: ["orca", "herdr"] }];
});
afterEach(() => resetManagerChoice());

describe("worktree creation sources in a logical space", () => {
  const space = (extra: Partial<Session> = {}): SpaceLike => ({ key: "runtime-space", machineId: "computer", name: "Work", sessions: [{
    machine_id: "computer", session_id: "agy-session", title: "Runtime Agent", status: "idle", cwd: "/repo/work",
    source_kind: "runtime", space_kind: "agy", space_id: "/repo/work", ...extra,
  }] });
  it("uses the native source id for a runtime folder without changing its ownership", () => {
    const sp = space();
    expect(sessionWorkspace(sp.sessions[0], catalog.workspaces, catalog.manager)).toBeUndefined();
    expect(spaceWorktreeSources(sp, catalog, machines)).toEqual([{ ...target, label: "work" }]);
    expect(resolve(false).canDelete).toBe(false);
  });
  it("retains exact native bindings even when another source shares the directory", () => {
    catalog.workspaces.push(row("native", { source: "herdr", cwd: target.cwd }));
    expect(spaceWorktreeSources(space({ source_kind: undefined, space_kind: "herdr", space_id: "native" }), catalog, machines))
      .toMatchObject([{ host: "herdr", workspaceId: "native" }]);
    expect(spaceWorktreeSources(space({ space_kind: "herdr", space_id: "missing" }), catalog, machines)).toEqual([]);
  });
  it("returns every matching native source for explicit selection and excludes other computers", () => {
    catalog.workspaces.push(row("native", { source: "herdr", cwd: target.cwd }), row("elsewhere", { machine_id: "other", cwd: target.cwd }));
    expect(spaceWorktreeSources(space(), catalog, machines).map((source) => source.workspaceId)).toEqual(["work", "native"]);
  });
  it("uses a runtime's verified Git worktree when the Agent works in a subdirectory", () => {
    expect(spaceWorktreeSources(space({ cwd: "/repo/work/src", worktree: "/repo/work/" }), catalog, machines)).toEqual([{ ...target, label: "work" }]);
    expect(spaceWorktreeSources(space({ cwd: "/repo/work-other" }), catalog, machines)).toEqual([]);
  });
  it("excludes unsupported, offline, unavailable and duplicate native identities", () => {
    machines[0].online = false; expect(spaceWorktreeSources(space(), catalog, machines)).toEqual([]); machines[0].online = true;
    catalog.workspaces[0].available = false; expect(spaceWorktreeSources(space(), catalog, machines)).toEqual([]); catalog.workspaces[0].available = true;
    catalog.workspaces.push(row("work")); expect(spaceWorktreeSources(space(), catalog, machines)).toEqual([]);
    catalog.workspaces = [row("work", { source: "session-directory" })];
    expect(spaceWorktreeSources(space(), catalog, machines)).toEqual([]);
  });
  it("offers a Paseo space's Git workspace its Agents are in, never a Paseo folder", () => {
    machines[0].engine_hosts = ["paseo"];
    catalog.workspaces = [row("wks_0000000000000001", { source: "paseo", cwd: "/repo/work", workspace_kind: "git", is_main: true }),
      row("wks_0000000000000002", { source: "paseo", cwd: "/notes", workspace_kind: "folder" })];
    const paseo = (workspaceId: string, cwd: string) => space({ space_kind: "paseo", host: "paseo", space_id: "prj_0000000000000001", workspace_id: workspaceId, cwd });
    expect(spaceWorktreeSources(paseo("wks_0000000000000001", "/repo/work"), catalog, machines))
      .toMatchObject([{ host: "paseo", workspaceId: "wks_0000000000000001", cwd: "/repo/work" }]);
    expect(spaceWorktreeSources(paseo("wks_0000000000000002", "/notes"), catalog, machines)).toEqual([]);
  });
  it("binds suggestions to the current credential and discovery manager", () => {
    expect(spaceWorktreeSources(space({ manager: "elsewhere" }), catalog, machines)).toEqual([]);
    chooseManager("other"); expect(spaceWorktreeSources(space(), catalog, machines)).toEqual([]);
    resetManagerChoice(); setToken("replacement"); expect(spaceWorktreeSources(space(), catalog, machines)).toEqual([]);
  });
});

describe("exact worktree action admission", () => {
  it("uses the chosen assistant's catalog and rejects the previous assistant's target", () => {
    chooseManager("other");
    expect(resolve().writable).toBe(false);
    catalog = { ...catalog, manager: "other" };
    target = { ...target, manager: "other" };
    machines = [{ ...machines[0], manager: "other" }];
    expect(resolve().canRename).toBe(true);
  });
  it("resolves same-host cross-project parent without guessing by directory", () => {
    catalog.workspaces[0].parent_source_id = "parent";
    catalog.workspaces[0].project_id = "other-project";
    catalog.workspaces.push(row("parent", { machine_id: "other" }), row("parent", { source: "herdr" }));
    expect(resolve().parent).toMatchObject({ owner: target.owner, manager: "manager", machineId: "computer", host: "orca", workspaceId: "parent" });
  });
  it("does not invent an ambiguous parent", () => {
    catalog.workspaces[0].parent_source_id = "parent";
    catalog.workspaces.push(row("parent"));
    expect(resolve().parent).toBeUndefined();
  });
  for (const field of ["owner", "manager", "machineId", "host", "workspaceId", "cwd"] as const) {
    it(`rejects a mismatched ${field} even when paths and labels look alike`, () => {
      target = { ...target, [field]: "other" };
      expect(resolve().writable).toBe(false);
    });
  }
  it("fails closed for missing owner/manager, duplicate identity and compatibility rows", () => {
    expect(resolveWorktreeAction({ ...target, owner: undefined }, { catalog, machines, writable: true }).writable).toBe(false);
    catalog.workspaces.push(row("work")); expect(resolve().writable).toBe(false);
    catalog.workspaces = [row("work", { source: "session-directory" })]; target.host = "session-directory";
    expect(resolve().workspace).toBeUndefined();
  });
  it("blocks readonly, offline, unavailable and disabled hosts", () => {
    expect(resolve(false).canRename).toBe(false);
    machines[0].online = false; expect(resolve().writable).toBe(false); machines[0].online = true;
    catalog.workspaces[0].available = false; expect(resolve().writable).toBe(false); catalog.workspaces[0].available = true;
    machines[0].engine_hosts = ["herdr"]; expect(resolve().writable).toBe(false);
    machines[0].engine_hosts = ["orca"]; machines[0].engines = [{ id: "orca", name: "Orca", available: false }];
    expect(resolve().writable).toBe(false);
  });
  it("blocks deletion of main and parents, while preserving supported non-Orca actions", () => {
    catalog.workspaces[0].is_main = true; expect(resolve().canDelete).toBe(false);
    catalog.workspaces[0].is_main = false; catalog.workspaces.push(row("child", { parent_source_id: "work" })); expect(resolve().canDelete).toBe(false);
    target.host = "herdr"; catalog.workspaces[0].source = "herdr";
    expect(resolve()).toMatchObject({ canRename: true, canCreate: true, canDelete: false });
    target.host = "future"; catalog.workspaces[0].source = "future"; machines[0].engine_hosts = ["future"];
    expect(resolve()).toMatchObject({ writable: true, canRename: false, canCreate: false, canDelete: false });
  });
  it("makes Paseo worktrees of its Git workspaces, titles any of its workspaces, and deletes only a worktree, never by closing", () => {
    target.host = "paseo"; machines[0].engine_hosts = ["herdr", "paseo"];
    catalog.workspaces[0] = { ...catalog.workspaces[0], source: "paseo", workspace_kind: "git", is_main: true };
    expect(resolve()).toMatchObject({ writable: true, canCreate: true, canRename: true, canClose: false, canDelete: false });
    catalog.workspaces[0].is_main = false;
    // a worktree Paseo did not make: it would only archive it, so no Delete
    expect(resolve()).toMatchObject({ canCreate: true, canRename: true, canClose: false, canDelete: false });
    catalog.workspaces[0].owned = true;
    expect(resolve()).toMatchObject({ canCreate: true, canRename: true, canClose: false, canDelete: true });
    catalog.workspaces[0].workspace_kind = "folder"; catalog.workspaces[0].is_main = undefined;
    expect(resolve()).toMatchObject({ writable: true, canCreate: false, canRename: true, canDelete: false });
    machines[0].engines = [{ id: "paseo", name: "Paseo", available: false }];
    catalog.workspaces[0].workspace_kind = "git";
    expect(resolve()).toMatchObject({ writable: false, canCreate: false, canRename: false, canDelete: false });
  });
  it("renames a Paseo space in Paseo only while the catalog has its project's workspaces from Paseo", () => {
    const paseo: Session = { machine_id: "computer", session_id: "paseo-1", title: "Agent", status: "idle", source_kind: "runtime", space_kind: "paseo",
      space_id: "prj_0000000000000001", workspace_id: "wks_0000000000000001", cwd: "/repo/work" };
    catalog.workspaces.push(row("wks_0000000000000001", { source: "paseo", project_id: "prj_0000000000000001" }));
    expect(renamesRuntimeSpace(paseo, catalog)).toBe(true);
    expect(renamesRuntimeSpace({ ...paseo, machine_id: "other" }, catalog)).toBe(false);
    expect(renamesRuntimeSpace({ ...paseo, manager: "elsewhere" }, catalog)).toBe(false);
    expect(renamesRuntimeSpace({ ...paseo, space_id: "prj_0000000000000002" }, catalog)).toBe(false);
    expect(renamesRuntimeSpace({ ...paseo, space_kind: "agy" }, catalog)).toBe(false);
    expect(renamesRuntimeSpace({ ...paseo, source_kind: undefined }, catalog)).toBe(false);
  });
  it("closes herdr workspaces and deletes only a verified linked checkout", () => {
    target.host = "herdr"; catalog.workspaces[0].source = "herdr";
    expect(resolve()).toMatchObject({ canClose: true, canDelete: false });
    expect(resolveWorktreeAction(target, { catalog, machines, writable: true, linkedWorktree: true }).canDelete).toBe(true);
    expect(resolve(false).canClose).toBe(false);
  });
});

it("opens the first direct Agent, including pins, without using descendants or another owner/machine/host", () => {
  catalog.workspaces.push(row("child", { parent_source_id: "work" }));
  const agent = (id: string, extra: Partial<Session>): Session => ({ machine_id: "computer", session_id: id, title: id, cwd: target.cwd, host: "orca", workspace_id: "work", status: "idle", ...extra });
  const ordered = [agent("descendant", { workspace_id: "child" }), agent("wrong-manager", { manager: "other" }), agent("wrong-host", { host: "herdr" }), agent("wrong-computer", { machine_id: "other" }), agent("pinned-direct", {}), agent("direct", {})];
  expect(firstDirectWorktreeAgent(target, catalog, ordered)?.session_id).toBe("pinned-direct");
  expect(firstDirectWorktreeAgent(target, catalog, ordered.slice(0, 4))).toBeUndefined();
  expect(firstDirectWorktreeAgent({ ...target, owner: "old" }, catalog, ordered)).toBeUndefined();
});
