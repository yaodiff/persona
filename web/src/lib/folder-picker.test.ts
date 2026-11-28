import { describe, expect, it } from "vitest";
import { isWorktreePath, orderRecentFolders, pathBreadcrumbs } from "./folder-picker";

describe("pathBreadcrumbs", () => {
  it("shortens macOS, Linux, and data-home paths to ~ with full ancestor paths", () => {
    expect(pathBreadcrumbs("/Users/alice/Code/my-app")).toEqual([
      { label: "~", path: "/Users/alice" },
      { label: "Code", path: "/Users/alice/Code" },
      { label: "my-app", path: "/Users/alice/Code/my-app" },
    ]);
    expect(pathBreadcrumbs("/home/me")).toEqual([{ label: "~", path: "/home/me" }]);
    expect(pathBreadcrumbs("/data00/home/alice/work/")).toEqual([
      { label: "~", path: "/data00/home/alice" },
      { label: "work", path: "/data00/home/alice/work" },
    ]);
  });

  it("handles root, non-home POSIX, tilde, Windows, and empty paths", () => {
    expect(pathBreadcrumbs("/")).toEqual([{ label: "/", path: "/" }]);
    expect(pathBreadcrumbs("/srv/work/beta")).toEqual([
      { label: "/", path: "/" },
      { label: "srv", path: "/srv" },
      { label: "work", path: "/srv/work" },
      { label: "beta", path: "/srv/work/beta" },
    ]);
    expect(pathBreadcrumbs("~/code/app")).toEqual([
      { label: "~", path: "~" },
      { label: "code", path: "~/code" },
      { label: "app", path: "~/code/app" },
    ]);
    expect(pathBreadcrumbs("C:\\Users\\alice\\Code")).toEqual([
      { label: "C:\\", path: "C:\\" },
      { label: "Users", path: "C:\\Users" },
      { label: "alice", path: "C:\\Users\\alice" },
      { label: "Code", path: "C:\\Users\\alice\\Code" },
    ]);
    expect(pathBreadcrumbs("")).toEqual([]);
  });
});

describe("orderRecentFolders", () => {
  it("identifies common worktree paths and moves them after primary folders", () => {
    expect(isWorktreePath("/srv/app_worktrees/feat-1")).toBe(true);
    expect(isWorktreePath("/srv/app/.worktrees/fix-2")).toBe(true);
    expect(isWorktreePath("/srv/app/.git/worktrees/topic")).toBe(true);
    expect(isWorktreePath("/srv/app-worktrees/branch")).toBe(true);
    expect(isWorktreePath("/srv/app")).toBe(false);

    const ordered = orderRecentFolders([
      { path: "/srv/app_worktrees/feat-1", name: "feat-1" },
      { path: "/srv/primary-a", name: "primary-a" },
      { path: "/srv/app/.worktrees/fix-2", name: "fix-2" },
      { path: "/srv/primary-a/", name: "primary-a" },
      { path: "/srv/primary-b", name: "primary-b" },
    ]);
    expect(ordered.map((d) => d.path)).toEqual([
      "/srv/primary-a",
      "/srv/primary-b",
      "/srv/app_worktrees/feat-1",
      "/srv/app/.worktrees/fix-2",
    ]);
  });

  it("handles empty or undefined input", () => {
    expect(orderRecentFolders(undefined)).toEqual([]);
    expect(orderRecentFolders([])).toEqual([]);
  });
});
