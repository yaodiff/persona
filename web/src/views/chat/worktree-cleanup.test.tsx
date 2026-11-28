import { readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { cleanupReady, WorktreeCleanup, type Cleanup } from "./SessionWorktreeCleanup";

// The worktree a closed or deleted agent worked in (#6115): keep it by default; removing it says what it loses, with the
// counts, and waits for the user to confirm that.
describe("WorktreeCleanup", () => {
  const st = { worktree: true, ours: true, path: "/home/me/.herdr/worktrees/proj/persona-fix", branch: "persona/fix", main_branch: "main", dirty: 2, unmerged: 1 };
  const keep: Cleanup = { remove: false, deleteBranch: false, confirmed: false };

  it("keeps it by default, with nothing to confirm", () => {
    const html = renderToStaticMarkup(<WorktreeCleanup st={st} value={keep} onChange={() => {}} />);
    expect(html).toContain("persona/fix");
    expect(html).toMatch(/aria-checked="true"[^>]*>保留 worktree/);
    expect(html).not.toContain("data-wt-dirty");
    expect(cleanupReady(st, keep)).toBe(true);
  });

  it("says what removing it loses and waits for the user to confirm", () => {
    const remove = { ...keep, remove: true };
    const html = renderToStaticMarkup(<WorktreeCleanup st={st} value={remove} onChange={() => {}} />);
    expect(html).toContain("有 2 个文件包含未提交的改动");
    expect(html).not.toContain("data-wt-unmerged"); // the branch stays: its commits are not lost
    expect(html).toContain("我了解这些改动将会丢失");
    expect(cleanupReady(st, remove)).toBe(false);
    expect(cleanupReady(st, { ...remove, confirmed: true })).toBe(true);
    const branch = renderToStaticMarkup(<WorktreeCleanup st={st} value={{ ...remove, deleteBranch: true }} onChange={() => {}} />);
    expect(branch).toContain("该分支有 1 个提交尚未合并到 main");
    // nothing to lose: no confirmation needed
    expect(cleanupReady({ ...st, dirty: 0, unmerged: 0 }, { ...remove, deleteBranch: true })).toBe(true);
  });
});

describe("WorktreeCleanup's loading boundary", () => {
  it("stays with the intent-loaded dialogs and out of the startup catalog", () => {
    const eager = readFileSync(join(__dirname, "SessionDialogs.tsx"), "utf8");
    expect(eager).not.toContain("WorktreeCleanup");
    expect(eager).not.toContain("chat-sessiondialogs.wt_");
    for (const name of ["CloseAgentDialog.tsx", "SessionMenuDialogs.tsx"]) {
      expect(readFileSync(join(__dirname, name), "utf8")).toContain('from "./SessionWorktreeCleanup"');
    }
    const startup = JSON.parse(readFileSync(join(__dirname, "../../i18n/startup-namespaces.json"), "utf8")) as string[];
    expect(startup).not.toContain("chat-sessiondialogs.wt_");
  });
});
