import { beforeAll, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import {
  selectSlimLine2Badges,
  slimSecondaryCue,
  slimWaitCue,
  waitTargetDuplicatesLine1PR,
} from "./todo-slim-row";
import type { Todo } from "./todos";

beforeAll(async () => {
  await initI18n("zh-Hans");
});

const baseTodo = (patch: Partial<Todo> = {}): Todo => ({
  id: "todo_row_1",
  title: "Sample task",
  created_at: "2026-11-28T00:00:00Z",
  updated_at: "2026-11-28T01:00:00Z",
  ...patch,
});

describe("selectSlimLine2Badges (#17696)", () => {
  it("caps badges to 1 highest-signal item when a wait/decision cue is present", () => {
    const result = selectSlimLine2Badges(
      {
        parentGoal: true,
        goalBlocked: true,
        childWorking: true,
        mergeTrain: true,
        direction: true,
        project: true,
        repoGate: true,
        repoCount: true,
      },
      { hasCue: true },
    );
    expect(result.shown).toEqual(["goal_blocked"]);
    expect(result.omittedCount).toBe(7);
  });

  it("prioritizes repo_gate over mergetrain, parent_goal, direction, project, and repo_count when cue is present", () => {
    const result = selectSlimLine2Badges(
      {
        repoGate: true,
        mergeTrain: true,
        parentGoal: true,
        direction: true,
        project: true,
        repoCount: true,
      },
      { hasCue: true },
    );
    expect(result.shown).toEqual(["repo_gate"]);
    expect(result.omittedCount).toBe(5);
  });

  it("allows up to 2 highest-signal badges when no primary cue is present", () => {
    const result = selectSlimLine2Badges(
      {
        childNeedsYou: true,
        childWorking: true,
        direction: true,
        project: true,
        repoCount: true,
      },
      { hasCue: false },
    );
    expect(result.shown).toEqual(["child_needs_you", "child_working"]);
    expect(result.omittedCount).toBe(3);
  });

  it("reserves full space for cue when an inline action button is also shown", () => {
    const withCueAndAction = selectSlimLine2Badges(
      { direction: true, project: true, repoGate: true },
      { hasCue: true, hasAction: true },
    );
    expect(withCueAndAction.shown).toEqual([]);
    expect(withCueAndAction.omittedCount).toBe(3);

    const noCueWithAction = selectSlimLine2Badges(
      { direction: true, project: true },
      { hasCue: false, hasAction: true },
    );
    expect(noCueWithAction.shown).toEqual(["direction"]);
    expect(noCueWithAction.omittedCount).toBe(1);
  });
});

describe("slimWaitCue & slimSecondaryCue (#17938)", () => {
  it("detects when a wait target duplicates the PR already shown on line 1", () => {
    expect(waitTargetDuplicatesLine1PR("PR #1446 (nwverse/persona)", "PR #1446")).toBe(true);
    expect(waitTargetDuplicatesLine1PR("nwverse/persona#1446", "PR #1446")).toBe(true);
    expect(waitTargetDuplicatesLine1PR("PR #1446", "PR #1446")).toBe(true);
    expect(waitTargetDuplicatesLine1PR("PR #1447 (nwverse/persona)", "PR #1446")).toBe(false);
    expect(waitTargetDuplicatesLine1PR("治理 Steam 扩展全链路", "PR #1446")).toBe(false);
  });

  it("uses concise prerequisite wording without verbose 等待外部变化： prefix and resolves raw todo_... IDs", async () => {
    await initI18n("zh-Hans");
    const todosById = new Map<string, Pick<Todo, "id" | "title">>([
      ["todo_e8acf635da201b7a", { id: "todo_e8acf635da201b7a", title: "治理 Steam 扩展全链路" }],
    ]);

    // Prerequisite blocker with readable title
    const prereqTodo = baseTodo({
      active_blockers: [{ id: "b1", kind: "todo_done", target_ref: "todo_e8acf635da201b7a", state: "active" }],
      wait: { kind: "external_event", waiting_on: "治理 Steam 扩展全链路" },
    });
    const prereqCue = slimWaitCue(prereqTodo, prereqTodo.wait!, { todosById });
    expect(prereqCue.text).toBe("等前置：治理 Steam 扩展全链路");
    expect(prereqCue.text).not.toContain("等待外部变化");

    // Raw todo_... wait target resolves via todosById
    const rawTodoWait = baseTodo({
      wait: { kind: "external_event", waiting_on: "todo_e8acf635da201b7a" },
    });
    expect(slimWaitCue(rawTodoWait, rawTodoWait.wait!, { todosById }).text).toBe("等前置：治理 Steam 扩展全链路");

    // Unknown raw todo_... never leaks the raw ID
    const unknownRawWait = baseTodo({
      wait: { kind: "external_event", waiting_on: "todo_missing99999" },
    });
    const unknownText = slimWaitCue(unknownRawWait, unknownRawWait.wait!, { todosById }).text;
    expect(unknownText).not.toContain("todo_missing99999");
    expect(unknownText).toBe("等前置：前置任务");

    // Generic external wait uses concise "等：..." prefix
    const genericWait = baseTodo({
      wait: { kind: "external_event", waiting_on: "上游发布镜像" },
    });
    expect(slimWaitCue(genericWait, genericWait.wait!).text).toBe("等：上游发布镜像");
  });

  it("omits duplicate line-1 PR number on wait cues and maps PR blockers concisely across locales", async () => {
    await initI18n("zh-Hans");
    const dupExternal = baseTodo({
      pr_url: "https://github.com/nwverse/persona/pull/1446",
      wait: { kind: "external_event", waiting_on: "PR #1446 (nwverse/persona)" },
    });
    const cue1 = slimSecondaryCue(
      dupExternal,
      { urgentLead: "", urgentText: "", orphaned: false },
      { failedOpen: false, held: null, overdue: false, line1PrText: "PR #1446" },
    );
    expect(cue1?.text).toBe("待合入");
    expect(cue1?.text).not.toContain("1446");

    const dupReview = baseTodo({
      pr_url: "https://github.com/nwverse/persona/pull/1446",
      wait: { kind: "merge_review", waiting_on: "PR #1446 (nwverse/persona)" },
    });
    expect(slimWaitCue(dupReview, dupReview.wait!, { line1PrText: "PR #1446" }).text).toBe("待审阅");

    const dupPrBlocker = baseTodo({
      pr_url: "https://github.com/nwverse/persona/pull/1446",
      wait: { kind: "pr_merge", waiting_on: "PR #1446", pr_blockers: ["checks_failing"] },
    });
    const ciCue = slimWaitCue(dupPrBlocker, dupPrBlocker.wait!, { line1PrText: "PR #1446" });
    expect(ciCue.text).toBe("CI 失败");
    expect(ciCue.tone).toBe("red");

    // English and Japanese parity
    await initI18n("en");
    expect(slimWaitCue(dupReview, dupReview.wait!, { line1PrText: "PR #1446" }).text).toBe("Awaiting review");
    expect(slimWaitCue(dupPrBlocker, dupPrBlocker.wait!, { line1PrText: "PR #1446" }).text).toBe("CI failed");

    await initI18n("ja");
    expect(slimWaitCue(dupReview, dupReview.wait!, { line1PrText: "PR #1446" }).text).toBe("レビュー待ち");
    expect(slimWaitCue(dupPrBlocker, dupPrBlocker.wait!, { line1PrText: "PR #1446" }).text).toBe("CI 失敗");
    await initI18n("zh-Hans");
  });
});
