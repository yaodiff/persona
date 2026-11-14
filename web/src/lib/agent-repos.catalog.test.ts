import { describe, expect, it } from "vitest";
import { buildRepoMachineTree, sessionWorkspace, showsWorktreeLevel, type RepoCatalogOptions, type RepoWorktree } from "./agent-repos";
import type { HostWorkspace } from "./workspace-catalog";
import type { Machine, Session } from "./types";

const machines: Machine[] = [{ id: "machine-a", name: "Computer A", online: true }, { id: "machine-b", name: "Computer B", online: true }];
const workspace = (id: string, patch: Partial<HostWorkspace> = {}): HostWorkspace => ({ id: `qualified-${id}`, machine_id: "machine-a", source: "orca", source_id: id, title: id, cwd: `/code/${id}`, project_id: "project-a", project_name: "Project A", ...patch });
const session = (id: string, workspaceId: string, patch: Partial<Session> = {}): Session => ({ session_id: id, machine_id: "machine-a", title: id, cwd: "/elsewhere", state: "idle", status: "idle", space_kind: "orca", space_id: workspaceId, ...patch });
const options = (workspaces: HostWorkspace[], patch: Partial<RepoCatalogOptions> = {}): RepoCatalogOptions => ({ owner: "credential-a", manager: "manager-a", workspaces, ...patch });
const flatten = (rows: RepoWorktree[]): RepoWorktree[] => rows.flatMap((w) => [w, ...flatten(w.children)]);
const rows = (all: Session[], opts: RepoCatalogOptions) => buildRepoMachineTree(all, machines, opts).flatMap((m) => m.repos.flatMap((r) => flatten(r.worktrees)));

describe("catalog-backed By repo projection", () => {
  const runtime = (id: string, patch: Partial<Session> = {}) => session(id, "/code/app", {
    source_kind: "runtime", space_kind: "agy", cwd: "/code/app", repo_root: "/code/app", worktree: "/code/app", workspace_cwd: "/code/app", ...patch,
  });
  const native = () => workspace("native", { source: "herdr", cwd: "/code/app", title: "app", project_id: undefined, project_name: undefined });

  it("groups native and logical-folder runtime Agents at one verified checkout exactly once", () => {
    const catalog = [native()];
    const codex = session("codex", "native", { space_kind: "herdr", workspace_id: "native", cwd: "/code/app", repo_root: "/code/app", worktree: "/code/app" });
    const agents = [codex, runtime("agy"), runtime("other-agy"), runtime("omp", { space_kind: "omp-history" })];
    const tree = buildRepoMachineTree(agents, machines, options(catalog));
    expect(tree[0].repos).toHaveLength(1);
    expect(tree[0].repos[0].count).toBe(4);
    const worktrees = tree[0].repos[0].worktrees;
    expect(worktrees).toHaveLength(1);
    expect(new Set(worktrees[0].sessions.map((s) => s.session_id))).toEqual(new Set(agents.map((s) => s.session_id)));
    expect(worktrees[0].navigationTree).toHaveLength(4);
    expect(worktrees[0].target?.workspaceId).toBe("native");
    expect(sessionWorkspace(agents[1], catalog, "manager-a")).toBeUndefined();
  });

  it("keeps runtime pinned navigation in the shared row without drawing or counting it twice", () => {
    const regular = runtime("regular"), pinned = runtime("pinned", { state: "needs_you", status: "blocked" });
    const tree = buildRepoMachineTree([regular], machines, options([native()], { navigationSessions: [regular, pinned], hideEmpty: true }));
    expect(tree[0].repos).toHaveLength(1); expect(tree[0].repos[0].count).toBe(1);
    const row = tree[0].repos[0].worktrees[0];
    expect(row.sessions).toEqual([regular]); expect(row.directSessions).toEqual([pinned, regular]);
    expect(row.navigationTree).toEqual([pinned, regular]);
    expect(rows([regular], options([native()], { search: "regular" }))[0].sessions).toEqual([regular]);
  });

  it("requires verified checkout metadata and preserves explicit native bindings", () => {
    const catalog = [native()];
    for (const agent of [runtime("unverified", { worktree: undefined }), runtime("explicit", { workspace_id: "missing" }),
      runtime("native-binding", { space_kind: "herdr", space_id: "missing" }), runtime("foreign-manager", { manager: "manager-b" }),
      runtime("clone", { cwd: "/other/app", repo_root: "/other/app", worktree: "/other/app" }), runtime("different-tree", { worktree: "/code/app/.worktrees/other" })]) {
      expect(rows([agent], options(catalog)).find((row) => row.workspace === catalog[0])?.sessions).toEqual([]);
    }
  });

  it("retains ambiguous host identities, including when one source is hidden", () => {
    const catalog = [native(), workspace("other", { cwd: "/code/app" })];
    for (const hiddenSources of [undefined, ["orca"]]) {
      const actual = rows([runtime("runtime")], options(catalog, { hiddenSources }));
      expect(actual.filter((row) => row.workspace).every((row) => row.sessions.length === 0)).toBe(true);
      expect(actual.filter((row) => !row.workspace).flatMap((row) => row.sessions).map((s) => s.session_id)).toEqual(["runtime"]);
    }
  });

  it("normalizes checkout paths while retaining computer partitions", () => {
    const catalog = [native(), { ...native(), id: "other-machine", machine_id: "machine-b" }];
    const actual = rows([runtime("local"), runtime("remote", { machine_id: "machine-b" })], options(catalog));
    expect(actual.filter((row) => row.workspace).map((row) => row.sessions.map((s) => s.session_id))).toEqual([["local"], ["remote"]]);
    const windows = workspace("windows", { cwd: "C:\\code\\app", source: "herdr" });
    expect(rows([runtime("windows", { cwd: "c:/code/app/src", worktree: "c:/code/app/", repo_root: "c:/code/app" })], options([windows]))[0].sessions).toHaveLength(1);
  });

  it("retains a runtime Agent when its catalog source is hidden among other visible sources", () => {
    const agent = runtime("runtime");
    const catalog = [native(), workspace("unrelated")];
    const tree = buildRepoMachineTree([agent], machines, options(catalog, { hiddenSources: ["herdr"], navigationSessions: [agent] }));
    expect(tree.flatMap((m) => m.repos.flatMap((r) => r.worktrees.flatMap((w) => w.sessions)))).toEqual([agent]);
  });

  it("preserves three empty ancestors, a standalone empty workspace, metadata and zero Agent counts", () => {
    const catalog = [workspace("root", { is_main: true }), workspace("middle", { parent_source_id: "root" }), workspace("leaf", { parent_source_id: "middle", workspace_kind: "folder", head: "abc" }), workspace("standalone")];
    const tree = buildRepoMachineTree([], machines, options(catalog));
    expect(tree).toHaveLength(1);
    const repo = tree[0].repos[0];
    expect(repo.count).toBe(0);
    expect(flatten(repo.worktrees).map((w) => w.name)).toEqual(["root", "middle", "leaf", "standalone"]);
    expect(rows([], options(catalog)).find((w) => w.name === "leaf")?.workspace).toBe(catalog[2]);
    expect(repo.worktrees[0].childStates).toEqual({ needs_you: 0, working: 0, done: 0 });
    expect(showsWorktreeLevel(buildRepoMachineTree([], machines, options([catalog[0]]))[0].repos[0])).toBe(true);
  });

  it("follows a same-host parent in another project and retains the child's own project/main metadata", () => {
    const catalog = [workspace("root"), workspace("child", { parent_source_id: "root", project_id: "project-b", project_name: "Project B", is_main: true })];
    const tree = buildRepoMachineTree([session("agent", "child")], machines, options(catalog));
    expect(tree[0].repos).toHaveLength(1);
    expect(tree[0].repos[0].count).toBe(1);
    const root = tree[0].repos[0].worktrees[0];
    expect(root.children[0].workspace?.project_name).toBe("Project B");
    expect(root.children[0].main).toBe(true);
    expect(root.sessions).toEqual([]);
    expect(root.tree.map((s) => s.session_id)).toEqual(["agent"]);
  });

  it("never merges equal paths or equal source IDs across machines, hosts or owners", () => {
    const catalog = [workspace("shared", { cwd: "/code/shared" }), workspace("shared", { cwd: "/code/shared", source: "herdr" }), workspace("shared", { machine_id: "machine-b", cwd: "/code/shared" })];
    const agents = [session("same", "shared"), session("same", "shared", { space_kind: "herdr", workspace_id: "shared" }), session("same", "shared", { machine_id: "machine-b" })];
    const actual = rows(agents, options(catalog));
    expect(new Set(actual.map((w) => w.key)).size).toBe(3);
    expect(actual.every((w) => w.sessions.length === 1)).toBe(true);
    expect(rows(agents, options(catalog, { owner: "credential-b" })).every((w) => !actual.some((a) => a.key === w.key))).toBe(true);
    expect(rows(agents, options(catalog, { manager: "manager-b" })).every((w) => !actual.some((a) => a.key === w.key))).toBe(true);
  });

  it("uses explicit placement ahead of directory inference and refuses a foreign host's binding", () => {
    const catalog = [workspace("a", { cwd: "/code/a" }), workspace("b", { cwd: "/code/b" })];
    expect(sessionWorkspace(session("agent", "b", { cwd: "/code/a", workspace_id: "b" }), catalog)?.source_id).toBe("b");
    expect(sessionWorkspace(session("foreign", "b", { cwd: "/code/a", space_kind: "herdr", workspace_id: "b" }), catalog)).toBeUndefined();
    expect(sessionWorkspace(session("unknown", "missing", { cwd: "/code/a" }), catalog)).toBeUndefined();
    expect(sessionWorkspace(session("other-manager", "a", { manager: "manager-b" }), catalog, "manager-a")).toBeUndefined();
  });

  it("binds a Paseo Agent to its workspace by id, not its project; an empty Paseo workspace is a row of the project", () => {
    const paseo = (id: string, patch: Partial<HostWorkspace> = {}) => workspace(id, { source: "paseo", project_id: "prj_app", project_name: "app", workspace_kind: "git", ...patch });
    const catalog = [paseo("wks_main", { cwd: "/code/app", is_main: true }), paseo("wks_fix", { cwd: "/paseo/worktrees/abc/fix", is_main: false, branch: "fix" })];
    const agent = session("paseo-1", "prj_app", { space_kind: "paseo", host: "paseo", source_kind: "runtime", workspace_id: "wks_fix", cwd: "/paseo/worktrees/abc/fix" });
    expect(sessionWorkspace(agent, catalog)?.source_id).toBe("wks_fix");
    expect(sessionWorkspace({ ...agent, workspace_id: undefined }, catalog)).toBeUndefined();
    const tree = buildRepoMachineTree([agent], machines, options(catalog));
    expect(tree[0].repos.map((r) => [r.name, r.count])).toEqual([["app", 1]]);
    expect(flatten(tree[0].repos[0].worktrees).map((w) => [w.name, w.sessions.length])).toEqual([["wks_main", 0], ["wks_fix", 1]]);
  });

  it("attributes unbound native Agents only to a unique deepest containing directory and respects worktree markers", () => {
    const a = workspace("a", { cwd: "/code/app" }), b = workspace("b", { cwd: "/code/app/child" });
    const native = session("native", "", { space_kind: undefined, cwd: "/code/app/child/src" });
    expect(sessionWorkspace(native, [a, b])).toBe(b);
    expect(sessionWorkspace(native, [a, b, { ...b, source_id: "other-id" }])).toBeUndefined();
    expect(sessionWorkspace(native, [a, b, { ...b, source: "herdr" }])).toBeUndefined();
    expect(sessionWorkspace({ ...native, space_id: "b" }, [a, b, { ...b, cwd: "/different", source: "herdr" }])).toBeUndefined();
    expect(sessionWorkspace({ ...native, cwd: "/tmp" }, [workspace("root", { cwd: "/" })])?.source_id).toBe("root");
    expect(sessionWorkspace({ ...native, cwd: "/code/app-copy/src" }, [a])).toBeUndefined();
    expect(sessionWorkspace({ ...native, cwd: "/code/app/.worktrees/independent" }, [a])).toBeUndefined();
    expect(sessionWorkspace({ ...native, cwd: "/outside", workspace_cwd: "/code/app/child" }, [a, b])).toBe(b);
    expect(sessionWorkspace({ ...native, machine_id: "machine-b" }, [a, b])).toBeUndefined();
  });

  it("keeps same-host orphans and cycles visible and never resolves parents across hosts or machines", () => {
    const catalog = [workspace("a", { parent_source_id: "b" }), workspace("b", { parent_source_id: "a" }), workspace("self", { parent_source_id: "self" }), workspace("orphan", { parent_source_id: "missing" }), workspace("foreign", { parent_source_id: "b", source: "herdr" }), workspace("elsewhere", { parent_source_id: "a", machine_id: "machine-b" })];
    const actual = rows([], options(catalog));
    expect(actual).toHaveLength(6);
    expect(actual.every((w) => w.children.length === 0)).toBe(true);
  });

  it("state/source filters retain only actual matches and their empty ancestors, including pinned direct navigation", () => {
    const catalog = [workspace("root"), workspace("middle", { parent_source_id: "root" }), workspace("leaf", { parent_source_id: "middle" }), workspace("empty")];
    const pinned = session("pinned", "leaf", { state: "needs_you", status: "needs_you" });
    const actual = rows([], options(catalog, { navigationSessions: [pinned], hideEmpty: true }));
    expect(actual.map((w) => w.name)).toEqual(["root", "middle", "leaf"]);
    expect(actual.every((w) => w.sessions.length === 0)).toBe(true);
    expect(actual[0].directSessions).toEqual([]);
    expect(actual[2].directSessions).toEqual([pinned]);
    expect(rows([], options(catalog, { navigationSessions: [], hideEmpty: true }))).toEqual([]);
  });

  it("search preserves a matching empty workspace path or Agent path without retaining unrelated siblings", () => {
    const catalog = [workspace("root"), workspace("middle", { parent_source_id: "root" }), workspace("leaf", { parent_source_id: "middle" }), workspace("unrelated")];
    expect(rows([], options(catalog, { search: "leaf" })).map((w) => w.name)).toEqual(["root", "middle", "leaf"]);
    expect(rows([session("needle", "leaf")], options(catalog, { search: "needle" })).map((w) => w.name)).toEqual(["root", "middle", "leaf"]);
  });

  it("navigates direct pinned Agents in display order without drawing them or counting them again", () => {
    const catalog = [workspace("root"), workspace("child", { parent_source_id: "root" })];
    const pinned = session("pinned", "root", { state: "needs_you", status: "needs_you" });
    const ordinary = session("ordinary", "root");
    const descendant = session("descendant", "child");
    const actual = rows([ordinary, descendant], options(catalog, { navigationSessions: [ordinary, pinned, descendant] }));
    expect(actual[0].directSessions?.map((s) => s.session_id)).toEqual(["pinned", "ordinary"]);
    expect(actual[0].sessions).toEqual([ordinary]);
    expect(actual[0].tree).toHaveLength(2);
  });

  it("keeps attribution ambiguous when a source is hidden and retains legacy pinned navigation without duplicate counts", () => {
    const a = workspace("a", { cwd: "/code/app" }), b = workspace("b", { cwd: "/code/app", source: "botmux" });
    const native = session("native", "", { space_kind: undefined, cwd: "/code/app" });
    const actual = rows([native], options([a, b], { hiddenSources: ["botmux"], navigationSessions: [native] }));
    expect(actual.find((w) => w.workspace === a)?.sessions).toEqual([]);
    expect(actual.some((w) => w.workspace === b)).toBe(false);
    const pinned = session("pinned", "project::/code/app");
    const legacy = rows([], options([], { navigationSessions: [pinned] }));
    expect(legacy[0].directSessions).toEqual([pinned]);
    expect(legacy[0].sessions).toEqual([]);
    expect(legacy[0].tree).toEqual([]);
  });

  it("retains offline/empty folder workspaces and uses session-only fallback when discovery is absent", () => {
    const offline = workspace("folder", { workspace_kind: "folder", online: false, available: false });
    expect(rows([], options([offline]))[0].target?.workspaceId).toBe("folder");
    const agent = session("legacy", "", { cwd: "/code/legacy", space_kind: undefined });
    expect(buildRepoMachineTree([agent], machines, options([]))).toEqual(buildRepoMachineTree([agent], machines));
  });
});
