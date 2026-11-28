import { describe, expect, it } from "vitest";
import { selectSlimLine2Badges } from "./todo-slim-row";

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
