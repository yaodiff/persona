// Six code-task steps with aria-current="step"; general tasks hide them, and PR/MR links remain independent.
// Missing or unknown stage kinds preserve the existing stage behavior. Finished work with nothing left to deploy
// (stage_done, #11590) ends with Done after the steps it went through.
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { t } from "../i18n";
import { TODO_STAGES } from "../lib/todo-stage";
import { TodoStageBar } from "./TodoStageBar";

const html = (stage?: string, prURL?: string, stageKind?: string, done?: boolean) =>
  renderToStaticMarkup(<TodoStageBar stage={stage} stageKind={stageKind} prURL={prURL} done={done} />);

/** Each step's state, in the order the bar draws them. */
function states(markup: string): Record<string, string> {
  return Object.fromEntries([...markup.matchAll(/data-stage="([\w-]+)" data-state="([\w-]+)"/g)].map((m) => [m[1], m[2]]));
}

function currentSteps(markup: string): string[] {
  return [...markup.matchAll(/data-stage="([\w-]+)"[^>]*aria-current="step"/g)].map((m) => m[1]);
}

describe("TodoStageBar", () => {
  it("draws nothing without a stage", () => {
    expect(html()).toBe("");
    expect(html(undefined, "")).toBe("");
  });

  it("draws nothing for a stage it doesn't know", () => {
    expect(html("shipped")).toBe("");
    expect(html("PLAN")).toBe("");
  });

  it("draws no stage bar or replacement status for general tasks at any stage", () => {
    [...TODO_STAGES, undefined, "shipped"].forEach((stage) => {
      expect(html(stage, undefined, "general")).toBe("");
    });
  });

  it("keeps PR and MR links on general tasks without drawing the six steps", () => {
    ["https://github.com/example/project/pull/7", "https://example.com/group/project/-/merge_requests/8"].forEach((url) => {
      const markup = html("develop", url, "general");
      expect(markup).not.toContain("<ol");
      expect(states(markup)).toEqual({});
      expect(markup).toContain(`href="${url}"`);
    });
  });

  it("keeps the existing behavior for missing and unknown stage kinds", () => {
    TODO_STAGES.forEach((stage) => {
      [undefined, "", "future-kind", "GENERAL"].forEach((kind) => {
        expect(html(stage, undefined, kind)).toBe(html(stage, undefined, "code"));
      });
    });
    expect(html("shipped", undefined, "future-kind")).toBe("");
    expect(html(undefined, undefined, "code")).toBe("");
  });

  it("marks plan current when the hub sends it for code tasks awaiting approval", () => {
    const markup = html("plan", undefined, "code");
    expect(currentSteps(markup)).toEqual(["plan"]);
    expect(states(markup)).toEqual({ plan: "current", develop: "ahead", review: "ahead", merge: "ahead", deploy: "ahead", verify: "ahead" });
  });

  it("marks the steps before the current one done, the current one, and the rest ahead", () => {
    TODO_STAGES.forEach((stage, current) => {
      const markup = html(stage);
      expect(Object.keys(states(markup))).toEqual([...TODO_STAGES]);
      TODO_STAGES.forEach((s, i) => {
        expect(states(markup)[s], `${stage} → ${s}`).toBe(i < current ? "done" : i === current ? "current" : "ahead");
      });
      expect(currentSteps(markup)).toEqual([stage]);
    });
  });

  it("is an ordered list of the six steps, in order and named", () => {
    const markup = html("review");
    expect(markup).toContain("<ol ");
    expect(markup).toContain('aria-label="' + t("todos.stage_label") + '"');
    expect(states(html("plan"))).toEqual({ plan: "current", develop: "ahead", review: "ahead", merge: "ahead", deploy: "ahead", verify: "ahead" });
    const labels = ["stage_plan", "stage_develop", "stage_review", "stage_merge", "stage_deploy", "stage_verify"].map((k) => t(`todos.${k}`));
    const at = labels.map((label) => markup.indexOf(label));
    expect(at.every((i) => i >= 0)).toBe(true);
    expect([...at].sort((a, b) => a - b)).toEqual(at);
  });

  it("links the PR when the todo has one, opening it like the app's other external links", () => {
    const markup = html("develop", "https://example.com/pull/7");
    expect(markup).toContain('href="https://example.com/pull/7"');
    expect(markup).toContain('target="_blank"');
    expect(markup).toContain('rel="noreferrer noopener"');
    expect(markup).toContain(t("todos.stage_pr_n", { n: "7" }));
    expect(markup).toContain('aria-label="' + t("todos.stage_pr_label", { pr: t("todos.stage_pr_n", { n: "7" }), host: "example.com" }) + '"');
  });

  it.each([
    "https://example.com/group/project/-/merge_requests/42",
    "https://review.example.org/group/subgroup/project/merge_requests/42",
    "http://example.net/group/project/-/merge_requests/42/",
    "https://github.com/group/project/merge_requests/42?view=diff#changes",
  ])("shows a code task's merge request as MR !N on any host: %s", (url) => {
    const markup = html("review", url, "code");
    expect(Object.keys(states(markup))).toEqual([...TODO_STAGES]);
    expect(currentSteps(markup)).toEqual(["review"]);
    expect(markup).toContain(`href="${url}"`);
    expect(markup).toContain("MR !42");
    expect(markup).not.toContain(t("todos.stage_pr_n", { n: "42" }));
    expect(markup).toContain('aria-label="' + t("todos.stage_pr_label", { pr: t("todos.stage_mr_n", { n: "42" }), host: new URL(url).host }) + '"');
    expect(markup).toContain('target="_blank"');
    expect(markup).toContain('rel="noreferrer noopener"');
  });

  it.each([
    "https://github.com/example/project/pull/7",
    "https://example.com/project/pulls/7/",
    "https://example.com/project/issues/7",
    "https://example.com/not_merge_requests/7",
  ])("keeps the existing PR label for other numbered URLs: %s", (url) => {
    expect(html("review", url, "code")).toContain(t("todos.stage_pr_n", { n: "7" }));
  });

  it("names a PR address without a number just PR, and draws no link for anything but http(s)", () => {
    expect(html(undefined, "https://example.com/merge_requests")).toContain(">" + t("todos.stage_pr") + "<");
    ["javascript:alert(1)", "data:text/html,x", "ftp://example.com/project/merge_requests/42", "not a url", "https://user:pass@example.com/project/pull/7", "https://evil.example\\@example.com/project/pull/7", "https:/example/project/pull/7"].forEach((url) => {
      expect(html(undefined, url)).toBe("");
      expect(html("review", url, "general")).toBe("");
      expect(html("review", url, "code")).not.toContain("href=");
    });
  });

  it("shows the PR link even when the stage is unknown, and only that", () => {
    const markup = html("shipped", "https://example.com/pull/7");
    expect(markup).not.toContain("<ol");
    expect(markup).toContain('href="https://example.com/pull/7"');
  });

  it("ends finished work with Done after the steps it went through, none of them current", () => {
    const markup = html("develop", undefined, "code", true);
    expect(states(markup)).toEqual({ plan: "done", develop: "done", done: "done" });
    expect(currentSteps(markup)).toEqual([]);
    expect(markup).toContain(t("todos.stage_done"));
    // Review, Merge, Deploy and Verified never happened: they are not drawn, let alone ticked.
    [t("todos.stage_review"), t("todos.stage_deploy"), t("todos.stage_verify")].forEach((label) => expect(markup).not.toContain(label));
    expect(markup.indexOf(t("todos.stage_develop"))).toBeLessThan(markup.indexOf(t("todos.stage_done")));
  });

  it("ends a merged change with nothing to deploy with Done after Merge", () => {
    const markup = html("merge", "https://github.com/example/project/pull/7", "code", true);
    expect(Object.keys(states(markup))).toEqual(["plan", "develop", "review", "merge", "done"]);
    expect(Object.values(states(markup)).every((state) => state === "done")).toBe(true);
    expect(markup).toContain('href="https://github.com/example/project/pull/7"');
  });

  it("draws no finished bar for general work, nor without a stage, and the open bar when not done", () => {
    expect(html("develop", undefined, "general", true)).toBe("");
    expect(html(undefined, undefined, "code", true)).toBe("");
    expect(html("develop", undefined, "code", false)).toBe(html("develop", undefined, "code"));
    expect(currentSteps(html("develop", undefined, "code", false))).toEqual(["develop"]);
  });
});
