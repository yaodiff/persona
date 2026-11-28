import { beforeAll, describe, expect, it } from "vitest";
import { initI18n } from "../../i18n";
import {
  formatRowRepoGate,
  formatRowRepoGateTitle,
  repoGatePRLabel,
  repoGateReasonLabel,
  repoGateTone,
  shortRepoName,
  shouldShowRowRepoGate,
} from "./repo-gate";

beforeAll(async () => {
  await initI18n("en");
});

describe("repo-gate presentation helpers (#15757, #17938)", () => {
  it("hides row badge for undefined, reference_only, zero targets, unstarted missing_pr, delivered ready, and single-repo non-blocking line-1 PR", () => {
    expect(shouldShowRowRepoGate(undefined)).toBe(false);
    expect(
      shouldShowRowRepoGate({
        state: "reference_only",
        target_count: 0,
        ready_count: 0,
        reference_count: 2,
        reason: "reference_only",
      }),
    ).toBe(false);
    // Unstarted / placeholder single-repo missing_pr must not show an alarm on list rows
    expect(
      shouldShowRowRepoGate({
        state: "missing_pr",
        target_count: 1,
        ready_count: 0,
        reference_count: 0,
        blocking_repo: "nwverse/persona",
        reason: "missing_pr",
      }),
    ).toBe(false);
    // Multi-repo missing_pr with 0 ready is still unstarted -> hidden
    expect(
      shouldShowRowRepoGate({
        state: "missing_pr",
        target_count: 2,
        ready_count: 0,
        reference_count: 0,
        blocking_repo: "nwverse/persona",
        reason: "missing_pr",
      }),
    ).toBe(false);
    // Multi-repo partial delivery (1/2 ready, 2nd still missing PR) -> shown
    expect(
      shouldShowRowRepoGate({
        state: "missing_pr",
        target_count: 2,
        ready_count: 1,
        reference_count: 0,
        blocking_repo: "nwverse/persona",
        reason: "missing_pr",
      }),
    ).toBe(true);
    // Delivered / awaiting-review rows omit green ready badge so the review cue owns line 2
    expect(
      shouldShowRowRepoGate(
        { state: "ready", target_count: 1, ready_count: 1, reference_count: 0, reason: "all_targets_ready" },
        { effectiveSection: "just_done", hasOpenAsk: true, askOutcome: "done" },
      ),
    ).toBe(false);
    expect(
      shouldShowRowRepoGate(
        { state: "ready", target_count: 2, ready_count: 2, reference_count: 0, reason: "all_targets_ready" },
        { effectiveSection: "just_done" },
      ),
    ).toBe(false);
    // Multi-repo active decision retains ready 2/2 badge
    expect(
      shouldShowRowRepoGate(
        { state: "ready", target_count: 2, ready_count: 2, reference_count: 1, reason: "all_targets_ready" },
        { effectiveSection: "waiting", hasOpenAsk: true, askOutcome: "decide" },
      ),
    ).toBe(true);
    // Single-repo with line-1 PR omits non-blocking states, keeps genuine PR blockers
    expect(
      shouldShowRowRepoGate(
        { state: "awaiting_review", target_count: 1, ready_count: 0, reference_count: 0, blocking_repo: "nwverse/persona" },
        { hasLine1PR: true, hasActiveDeliveryPR: true },
      ),
    ).toBe(false);
    expect(
      shouldShowRowRepoGate(
        { state: "blocked_ci", target_count: 1, ready_count: 0, reference_count: 0, blocking_repo: "nwverse/persona", reason: "ci_failed" },
        { hasLine1PR: true, hasActiveDeliveryPR: true },
      ),
    ).toBe(true);
  });

  it("maps server gate states to semantic tones and compact row text with short repo basename", async () => {
    expect(shortRepoName("nwverse/persona")).toBe("persona");
    expect(shortRepoName("acme/api/")).toBe("api");
    expect(shortRepoName("standalone")).toBe("standalone");

    expect(repoGateTone("ready")).toBe("green");
    expect(repoGateTone("blocked_ci")).toBe("red");
    expect(repoGateTone("blocked_conflict")).toBe("red");
    expect(repoGateTone("blocked_changes")).toBe("red");
    expect(repoGateTone("awaiting_review")).toBe("orange");
    expect(repoGateTone("checks_pending")).toBe("orange");
    expect(repoGateTone("missing_pr")).toBe("orange");
    expect(repoGateTone("reference_only")).toBe("muted");

    expect(
      formatRowRepoGate({
        state: "ready",
        target_count: 2,
        ready_count: 2,
        reference_count: 1,
      }),
    ).toBe("Repos 2/2 ready");

    const multiBlocked = {
      state: "blocked_ci",
      target_count: 3,
      ready_count: 2,
      reference_count: 1,
      blocking_repo: "acme/api",
      reason: "ci_failed",
    };
    expect(formatRowRepoGate(multiBlocked)).toBe("Repos 2/3 · api");
    expect(formatRowRepoGateTitle(multiBlocked)).toContain("acme/api");
    expect(formatRowRepoGateTitle(multiBlocked)).toContain("CI checks failed");

    // Single-repo genuine blocker with line-1 PR shows concise blocker reason instead of repeating repo path
    expect(
      formatRowRepoGate(
        {
          state: "blocked_ci",
          target_count: 1,
          ready_count: 0,
          reference_count: 0,
          blocking_repo: "nwverse/persona",
          reason: "ci_failed",
        },
        { hasLine1PR: true },
      ),
    ).toBe("CI failed");

    // Verify zh-Hans compact format ("仓 0/2 · persona")
    await initI18n("zh-Hans");
    expect(
      formatRowRepoGate({
        state: "blocked_ci",
        target_count: 2,
        ready_count: 0,
        reference_count: 0,
        blocking_repo: "nwverse/persona",
        reason: "ci_failed",
      }),
    ).toBe("仓 0/2 · persona");
    await initI18n("en");
  });

  it("formats reason labels and GitHub/GitLab PR labels", () => {
    expect(
      repoGateReasonLabel({
        state: "blocked_ci",
        target_count: 2,
        ready_count: 1,
        reference_count: 0,
        reason: "ci_failed",
      }),
    ).toBe("CI checks failed");
    expect(
      repoGateReasonLabel({
        state: "blocked_conflict",
        target_count: 2,
        ready_count: 1,
        reference_count: 0,
        reason: "merge_conflicts",
      }),
    ).toBe("Merge conflicts");

    expect(repoGatePRLabel("https://github.com/acme/api/pull/42")).toBe("PR #42");
    expect(repoGatePRLabel("https://gitlab.example.com/acme/api/-/merge_requests/9")).toBe("MR !9");
    expect(repoGatePRLabel("https://example.com/change/abc")).toBe("Open blocking PR");
  });
});
