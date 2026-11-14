import { describe, expect, it } from "vitest";
import type { HostWorkspace } from "./workspace-catalog";
import { startBranch, startChoices, startGroups, startKind, startProject, startTitle } from "./workspace-start";

const row = (id: string, extra: Partial<HostWorkspace> = {}): HostWorkspace => ({
  id: `box/paseo/${id}`, machine_id: "box", source: "paseo", source_id: id, title: id, cwd: `/src/${id}`,
  project_id: "p1", project_name: "app", workspace_kind: "git", ...extra,
});

describe("New Agent's workspaces (#7259)", () => {
  it("offers only the chosen computer's Orca and Paseo workspaces that can start now", () => {
    const catalog = [
      row("paseo-main", { is_main: true }), row("orca-tree", { source: "orca" }), row("herdr", { source: "herdr" }),
      row("botmux", { source: "botmux" }), row("laptop", { machine_id: "laptop" }), row("gone", { available: false }),
      row("offline", { online: false }),
    ];
    expect(startChoices(catalog, "box").map((w) => w.source_id)).toEqual(["paseo-main", "orca-tree"]);
  });

  it("groups by project, main checkout first, then worktrees, then folders, and no project last", () => {
    const groups = startGroups([
      row("notes", { project_id: undefined, project_name: undefined, workspace_kind: "folder" }),
      row("b-fix", { title: "fix-login", branch: "fix-login" }),
      row("a-main", { title: "app", branch: "main", is_main: true }),
      row("a-docs", { title: "Docs", branch: "docs" }),
      row("api-main", { title: "api", project_id: "p2", project_name: "api", is_main: true }),
      row("scratch", { title: "", cwd: "/tmp/scratch", workspace_kind: "folder" }),
    ]);
    expect(groups.map((g) => g.title)).toEqual(["api", "app", ""]);
    expect(groups[1].workspaces.map((w) => w.source_id)).toEqual(["a-main", "a-docs", "b-fix", "scratch"]);
    expect(groups[1].workspaces.map(startKind)).toEqual(["main", "worktree", "worktree", "folder"]);
    expect(groups[2].workspaces.map((w) => w.source_id)).toEqual(["notes"]);
  });

  it("keeps two engines' projects of the same name apart", () => {
    expect(startGroups([row("p", { project_id: "x" }), row("o", { source: "orca", project_id: "x" })])).toHaveLength(2);
  });

  it("names a row by its title, then its branch, then its folder, and says the branch once", () => {
    expect(startTitle(row("t", { title: "Login work", branch: "fix-login" }))).toBe("Login work");
    expect(startBranch(row("t", { title: "Login work", branch: "fix-login" }))).toBe("fix-login");
    expect(startTitle(row("b", { title: " ", branch: "fix-login" }))).toBe("fix-login");
    expect(startBranch(row("b", { title: " ", branch: "fix-login" }))).toBeUndefined();
    expect(startTitle(row("f", { title: "", cwd: "/src/notes/", workspace_kind: "folder" }))).toBe("notes");
    expect(startBranch(row("f", { branch: "main", workspace_kind: "folder" }))).toBeUndefined();
    expect(startProject(row("x", { project_name: " ", project_path: "/src/app" }))).toBe("app");
    expect(startProject(row("y", { project_name: undefined }))).toBe("");
  });
});
