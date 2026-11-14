import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./router", () => ({ href: () => "/", navigate: vi.fn() }));

import { ApiError, errorText } from "./api";
import { navigate } from "./router";
import {
  addAgentWorktree, agentBranch, agentNameHint, branchProblem, branchSlug, createWorktree, emptyForm, formProblem, onOpenNewWorktree, openCreated, openNewWorktree, prepareWorktree,
  removeAgentWorktree, suggestedPath, typeBranch, typePath, WORKTREE_COPY, worktreeLoss, worktreeProblem, worktreeStatus,
} from "./worktree";

function response(body: unknown, status = 200) {
  return { ok: status < 400, status, text: async () => JSON.stringify(body) };
}

beforeEach(() => {
  vi.stubGlobal("localStorage", { getItem: () => "test" });
  vi.clearAllMocks();
});
afterEach(() => vi.unstubAllGlobals());

describe("the checkout path", () => {
  it("names the folder the way herdr does", () => {
    expect(branchSlug("Feat/A_b.c")).toBe("feat-a-b-c");
    expect(branchSlug("feat/2431-worktree")).toBe("feat-2431-worktree");
    expect(branchSlug("fix/中文")).toBe("fix");
    expect(branchSlug("  --a//b--")).toBe("a-b");
    expect(branchSlug("中文")).toBe("");
  });
  it("is the branch's folder under the repository's directory", () => {
    expect(suggestedPath("/srv/wt/proj", "feat/x")).toBe("/srv/wt/proj/feat-x");
    expect(suggestedPath("/srv/wt/proj/", "feat/x")).toBe("/srv/wt/proj/feat-x");
    expect(suggestedPath("C:\\wt\\proj", "feat/x")).toBe("C:\\wt\\proj\\feat-x");
    expect(suggestedPath("/srv/wt/proj", "中文")).toBe("");
    expect(suggestedPath("", "feat/x")).toBe("");
  });
});

describe("the branch name", () => {
  it("takes what git takes", () => {
    for (const ok of ["feat/x", "fix-1", "release/1.2.3", "a@b", "feat/中文", "  spare  "]) expect(branchProblem(ok), ok).toBe("");
  });
  it("tells an empty name, then what git would refuse", () => {
    expect(branchProblem("  ")).toBe(WORKTREE_COPY.branchRequired);
    for (const bad of ["-x", "a..b", "a b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", "a@{b", "@", "/a", "a/", "a//b", "a.", "a.lock", ".a", "a/.b", "a.lock/b"])
      expect(branchProblem(bad), bad).not.toBe("");
  });
});

describe("the form", () => {
  const base = "/srv/wt/proj";
  it("fills the path in from the branch while the path is not edited", () => {
    let f = typeBranch(emptyForm, base, "feat/x");
    expect(f).toEqual({ branch: "feat/x", path: "/srv/wt/proj/feat-x", pathEdited: false });
    f = typeBranch(f, base, "feat/xy ");
    expect(f.path).toBe("/srv/wt/proj/feat-xy");
  });
  it("stops following once the path is edited by hand", () => {
    let f = typeBranch(emptyForm, base, "feat/x");
    f = typePath(f, base, "/elsewhere/mine");
    expect(f.pathEdited).toBe(true);
    f = typeBranch(f, base, "feat/other");
    expect(f).toEqual({ branch: "feat/other", path: "/elsewhere/mine", pathEdited: true });
  });
  it("follows again when the path is emptied", () => {
    let f = typePath(typeBranch(emptyForm, base, "feat/x"), base, "/elsewhere/mine");
    f = typePath(f, base, "");
    expect(f).toEqual({ branch: "feat/x", path: "/srv/wt/proj/feat-x", pathEdited: false });
  });
  it("is sent only with a good branch and a path", () => {
    expect(formProblem(emptyForm)).toBe(WORKTREE_COPY.branchRequired);
    expect(formProblem({ branch: "a..b", path: "/p", pathEdited: true })).not.toBe("");
    expect(formProblem({ branch: "中文", path: "", pathEdited: false })).toBe(WORKTREE_COPY.pathRequired);
    expect(formProblem({ branch: "feat/x", path: "/p/x", pathEdited: false })).toBe("");
  });
});

describe("what goes to the hub", () => {
  const target = { mid: "m1", workspaceId: "w1", host: "herdr" };
  it("prepare names the workspace and the host", async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ root: "/r", repoName: "proj", branch: "main", path: "/wt/proj" }));
    vi.stubGlobal("fetch", fetcher);
    expect(await prepareWorktree(target)).toMatchObject({ repoName: "proj", path: "/wt/proj" });
    expect(fetcher.mock.calls[0][0]).toBe("/api/v1/machines/m1/worktree/prepare");
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ workspace_id: "w1", kind: "herdr" });
  });
  it("create sends the trimmed branch and path, and nothing that forces", async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ workspace_id: "w2", terminal: "pane:w2:p1", path: "/wt/proj/x", branch: "x" }));
    vi.stubGlobal("fetch", fetcher);
    await createWorktree({ mid: "m1", workspaceId: "w1" }, { branch: " feat/x ", path: " /wt/proj/feat-x ", pathEdited: false });
    expect(fetcher.mock.calls[0][0]).toBe("/api/v1/machines/m1/worktree/create");
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ workspace_id: "w1", branch: "feat/x", path: "/wt/proj/feat-x" });
  });
  it("opens the new workspace's terminal, else the machine's list", () => {
    openCreated("m1", { workspace_id: "w2", terminal: "pane:w2:p1", path: "/p", branch: "b" });
    expect(navigate).toHaveBeenLastCalledWith({ name: "terminal", mid: "m1", id: "pane:w2:p1" });
    openCreated("m1", { workspace_id: "w2", path: "/p", branch: "b" });
    expect(navigate).toHaveBeenLastCalledWith({ name: "terminal", mid: "m1" });
  });
});

describe("errors", () => {
  it("an older hub or computer is told to update", () => {
    expect(worktreeProblem(new ApiError(404, "HTTP 404", {}, false), WORKTREE_COPY.failed)).toEqual({ text: WORKTREE_COPY.needsUpdate, needsUpdate: true });
    expect(worktreeProblem(new ApiError(502, "unknown method worktree.prepare", { code: "internal" }), WORKTREE_COPY.readFailed).needsUpdate).toBe(true);
    expect(worktreeProblem(new ApiError(409, "unsupported: herdr cannot do this", {}), WORKTREE_COPY.failed).needsUpdate).toBe(true);
    expect(worktreeProblem(new ApiError(500, "unsupported: herdr cannot do this", {}), WORKTREE_COPY.failed)).toEqual({ text: WORKTREE_COPY.needsUpdate, needsUpdate: true });
  });
  it("keeps other failures' readable detail without asking for an update", () => {
    const failed = new ApiError(502, "新建 worktree 失败：'x' is already used by worktree", {});
    expect(worktreeProblem(failed, WORKTREE_COPY.failed)).toEqual({
      text: `没能新建 worktree：${errorText(failed)}`, needsUpdate: false,
    });
    expect(worktreeProblem(new ApiError(403, "changing a computer's terminals needs a full device", {}), WORKTREE_COPY.failed).needsUpdate).toBe(false);
  });
});

describe("opening the dialog", () => {
  it("hands the shell the workspace, and ignores a node without one", () => {
    const seen = vi.fn();
    const off = onOpenNewWorktree(seen);
    openNewWorktree({ mid: "m1", host: "herdr", label: "proj" });
    expect(seen).not.toHaveBeenCalled();
    openNewWorktree({ mid: "m1", workspaceId: "w1", host: "herdr", label: "proj" });
    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ target: { mid: "m1", workspaceId: "w1", host: "herdr", label: "proj" } }));
    off();
    openNewWorktree({ mid: "m1", workspaceId: "w1" });
    expect(seen).toHaveBeenCalledTimes(1);
  });
});

describe("a new agent's own worktree (#6115)", () => {
  it("names its branch as the node does", () => {
    expect(agentBranch("Fix the login crash!")).toBe("persona/fix-the-login-crash");
    expect(agentBranch("修登录")).toBe("persona/agent");
    // a prompt with nothing to name a branch with: the folder's name
    expect(agentBranch(agentNameHint("修登录", "/home/me/code/weather/"))).toBe("persona/weather");
    expect(agentBranch(agentNameHint(" Fix login ", "/home/me/code/weather"))).toBe("persona/fix-login");
    expect(agentNameHint("", "")).toBe("");
    expect(agentBranch("a".repeat(30) + " " + "b".repeat(30))).toBe("persona/" + "a".repeat(30) + "-" + "b".repeat(9));
  });

  it("counts what removing it loses: the branch's commits only with the branch", () => {
    const st = { worktree: true, ours: true, path: "/wt/x", branch: "persona/x", dirty: 2, unmerged: 3 };
    expect(worktreeLoss(st, false)).toEqual({ dirty: 2, unmerged: 0 });
    expect(worktreeLoss(st, true)).toEqual({ dirty: 2, unmerged: 3 });
  });

  it("asks the computer, and stays out of the way when it cannot say", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(response({ worktree: false, ours: false, path: "/r", dirty: 0, unmerged: 0 })).mockResolvedValueOnce(response({ error: "unknown method" }, 409));
    vi.stubGlobal("fetch", fetcher);
    expect((await worktreeStatus("m", "/r/sub"))?.path).toBe("/r");
    expect(await worktreeStatus("m", "/r/sub")).toBeNull();
    expect(fetcher.mock.calls[0][0]).toBe("/api/v1/machines/m/worktree/status");
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ cwd: "/r/sub" });
  });

  it("asks the engine the agent opens in, or ran in: the one that made its worktree", async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ worktree: true, ours: false, path: "/wt/x", dirty: 0, unmerged: 0 }));
    vi.stubGlobal("fetch", fetcher);
    await worktreeStatus("m", "/wt/x", undefined, "paseo");
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ cwd: "/wt/x", kind: "paseo" });
    // none: the computer's default decides
    await worktreeStatus("m", "/wt/x", undefined, undefined);
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({ cwd: "/wt/x" });
  });

  it("makes it with the agent's name, a branch only when typed, by the engine it opens in", async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ path: "/wt/proj/persona-x", branch: "persona/x", root: "/r" }));
    vi.stubGlobal("fetch", fetcher);
    await addAgentWorktree("m", "/r", "x");
    await addAgentWorktree("m", "/r", "x", " feat/y ");
    await addAgentWorktree("m", "/r", "x", "", {}, "paseo");
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ cwd: "/r", name_hint: "x" });
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({ cwd: "/r", name_hint: "x", branch: "feat/y" });
    expect(JSON.parse(fetcher.mock.calls[2][1].body)).toEqual({ cwd: "/r", name_hint: "x", kind: "paseo" });
  });

  it("removes it once the closed agent is gone, trying again while the node still lists it", async () => {
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({ code: "worktree_in_use", error: "an agent still runs in this worktree" }, 409))
      .mockResolvedValueOnce(response({ removed: true, branch_deleted: true }));
    vi.stubGlobal("fetch", fetcher);
    await expect(removeAgentWorktree("m", "/wt/x", { deleteBranch: true, force: false }, 3, 1)).resolves.toEqual({ removed: true, branch_deleted: true });
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({ path: "/wt/x", delete_branch: true });
    // another refusal is said at once
    fetcher.mockReset().mockResolvedValue(response({ code: "worktree_dirty", error: "the worktree has uncommitted changes" }, 409));
    await expect(removeAgentWorktree("m", "/wt/x", { deleteBranch: false, force: false }, 3, 1)).rejects.toBeInstanceOf(ApiError);
    expect(fetcher).toHaveBeenCalledTimes(1);
    // by the engine the agent ran in
    fetcher.mockReset().mockResolvedValue(response({ removed: true, branch_deleted: false }));
    await removeAgentWorktree("m", "/wt/x", { deleteBranch: false, force: false, kind: "paseo" }, 3, 1);
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ path: "/wt/x", kind: "paseo" });
  });
});
