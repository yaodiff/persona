// Pure presentation helpers for server-computed Target Repo weakest-link ("barrel rule")
// delivery gates under todos_v2 (#15757, backed by PR #15729 + #15750).
import { t } from "../../i18n";
import type { TodoRepoGate, TodoTone } from "../../lib/todos";

export type RepoGateTone = "green" | "red" | "orange" | "muted";

/** Semantic tone for a server-computed repo_gate state. */
export function repoGateTone(state: string): RepoGateTone {
  switch (state) {
    case "ready":
      return "green";
    case "blocked_ci":
    case "blocked_conflict":
    case "blocked_changes":
      return "red";
    case "awaiting_review":
    case "checks_pending":
    case "missing_pr":
      return "orange";
    default:
      return "muted";
  }
}

/** Whether a list row should display a compact repo_gate badge (omits reference-only repos). */
export function shouldShowRowRepoGate(gate: TodoRepoGate | undefined): gate is TodoRepoGate {
  return Boolean(gate && gate.state !== "reference_only" && gate.target_count > 0);
}

/** Compact single-line summary for a to-do list row under todos_v2. */
export function formatRowRepoGate(gate: TodoRepoGate): string {
  const ready = gate.ready_count;
  const total = gate.target_count;
  if (gate.state === "ready") {
    return t("todos.repo_gate_row_ready", { ready, total });
  }
  const repo = gate.blocking_repo?.trim();
  if (repo) {
    return t("todos.repo_gate_row_blocked", { ready, total, repo });
  }
  return t("todos.repo_gate_row_pending", { ready, total });
}

/** Localized human-readable reason for a server repo_gate reason or state code. */
export function repoGateReasonLabel(gate: TodoRepoGate): string {
  const code = (gate.reason || gate.state || "").trim();
  switch (code) {
    case "ci_failed":
    case "blocked_ci":
      return t("todos.repo_gate_reason_ci_failed");
    case "merge_conflicts":
    case "blocked_conflict":
      return t("todos.repo_gate_reason_merge_conflicts");
    case "changes_requested":
    case "blocked_changes":
      return t("todos.repo_gate_reason_changes_requested");
    case "draft_pr":
      return t("todos.repo_gate_reason_draft_pr");
    case "review_required":
    case "awaiting_review":
      return t("todos.repo_gate_reason_review_required");
    case "checks_pending":
      return t("todos.repo_gate_reason_checks_pending");
    case "missing_pr":
      return t("todos.repo_gate_reason_missing_pr");
    case "pr_open":
      return t("todos.repo_gate_reason_pr_open");
    case "all_targets_ready":
    case "ready":
      return t("todos.repo_gate_reason_ready");
    case "reference_only":
      return t("todos.repo_gate_reason_reference_only");
    default:
      return code;
  }
}

/** Short label for a blocking PR URL (e.g. "PR #42" or "MR !7"), falling back to a generic label. */
export function repoGatePRLabel(url: string): string {
  const gh = url.match(/\/pull\/(\d+)/i);
  if (gh) return t("todos.stage_pr_n", { n: gh[1] });
  const gl = url.match(/\/merge_requests\/(\d+)/i);
  if (gl) return t("todos.stage_mr_n", { n: gl[1] });
  return t("todos.repo_gate_open_pr");
}

export function repoGatePRLook(gate: TodoRepoGate | undefined): { label: string; tone: TodoTone } | undefined {
  if (!gate || gate.target_count <= 0 || gate.reason === "draft_pr") return undefined;
  switch (gate.state) {
    case "blocked_conflict":
      return { label: t("todos.status_pr_conflicts"), tone: "red" };
    case "blocked_changes":
      return { label: t("todos.status_pr_changes_requested"), tone: "amber" };
    case "blocked_ci":
      return { label: t("todos.status_pr_checks_failed"), tone: "red" };
    case "awaiting_review":
      return { label: t("todos.status_pr_in_review"), tone: "idle" };
    case "checks_pending":
      return {
        label: gate.reason === "checks_pending" ? t("todos.status_pr_checks_pending") : t("todos.status_pr_waiting_merge"),
        tone: "idle",
      };
    default:
      return undefined;
  }
}
