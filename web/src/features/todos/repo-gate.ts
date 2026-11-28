// Pure presentation helpers for server-computed Target Repo weakest-link ("barrel rule")
// delivery gates under todos_v2 (#15757, backed by PR #15729 + #15750).
import { t } from "../../i18n";
import type { TodoRepoGate, TodoTone } from "../../lib/todos";

export type RepoGateTone = "green" | "red" | "orange" | "muted";

export interface RowRepoGateContext {
  /** Current status section of the row (e.g. "just_done", "done"). */
  effectiveSection?: string;
  /** Whether the task currently has an open owner ask. */
  hasOpenAsk?: boolean;
  /** Outcome of the active open ask ("decide", "done", "cannot", etc.). */
  askOutcome?: string;
  /** Whether the task is already closed (`done_at`). */
  done?: boolean;
  /** Whether line 1 already displays a PR anchor (`PR #N`). */
  hasLine1PR?: boolean;
  /** Whether the task has an active delivery PR (open PR URL, PR status, or gate blocking PR). */
  hasActiveDeliveryPR?: boolean;
  /** Whether the task has an active execution failure (`start_error`). */
  hasExecutionFailure?: boolean;
}

function isNonActiveGateState(state: string): boolean {
  return state === "reference_only" || state === "not_started" || state === "unstarted" || state === "idle";
}

/** Whether a server repo_gate state represents a genuine PR delivery blocker (CI, conflicts, or requested changes). */
export function isGenuineRepoGateBlocker(state: string): boolean {
  return state === "blocked_ci" || state === "blocked_conflict" || state === "blocked_changes";
}

/** Short display name for a repository path (e.g. "nwverse/persona" -> "persona"). */
export function shortRepoName(repo: string): string {
  const trimmed = repo.trim().replace(/\/+$/, "");
  if (!trimmed) return "";
  const slash = trimmed.lastIndexOf("/");
  return slash >= 0 && slash < trimmed.length - 1 ? trimmed.slice(slash + 1) : trimmed;
}

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

/**
 * Whether a slim list row should display a compact repo_gate badge under todos_v2 (#15757, #17938):
 * - Omits reference-only, zero-target, and unstarted/placeholder bound repos with no active delivery PR.
 * - Omits green ready badges on delivered / awaiting-review rows so the review cue owns line 2.
 * - Omits redundant single-repo non-blocking badges when line 1 already displays the PR link.
 */
export function shouldShowRowRepoGate(
  gate: TodoRepoGate | undefined,
  context?: RowRepoGateContext,
): gate is TodoRepoGate {
  if (!gate || gate.target_count <= 0 || isNonActiveGateState(gate.state)) return false;

  if (gate.state === "missing_pr") {
    // Only surface missing_pr on multi-repo tasks where at least one sibling target is already ready.
    return gate.target_count >= 2 && gate.ready_count > 0;
  }

  if (
    gate.state === "blocked" &&
    !gate.blocking_pr?.trim() &&
    context &&
    !context.hasActiveDeliveryPR &&
    !context.hasExecutionFailure
  ) {
    return false;
  }

  if (gate.state === "ready") {
    if (context?.done || context?.effectiveSection === "just_done" || context?.effectiveSection === "done") {
      return false;
    }
    if (context?.hasOpenAsk && (context.askOutcome !== "decide" || gate.target_count <= 1)) {
      return false;
    }
    if (context?.hasLine1PR && gate.target_count <= 1) {
      return false;
    }
    return true;
  }

  if (gate.target_count === 1 && context?.hasLine1PR && !isGenuineRepoGateBlocker(gate.state)) {
    return false;
  }

  return true;
}

/** Compact single-line summary for a to-do list row under todos_v2 (#15757, #17938). */
export function formatRowRepoGate(gate: TodoRepoGate, options?: { hasLine1PR?: boolean }): string {
  const ready = gate.ready_count;
  const total = gate.target_count;
  if (gate.state === "ready") {
    return t("todos.repo_gate_row_ready", { ready, total });
  }
  if (total === 1 && options?.hasLine1PR && isGenuineRepoGateBlocker(gate.state)) {
    switch (gate.state) {
      case "blocked_ci":
        return t("todos.slim_pr_ci_failed");
      case "blocked_conflict":
        return t("todos.slim_pr_conflicts");
      case "blocked_changes":
        return t("todos.slim_pr_changes");
    }
  }
  const repo = gate.blocking_repo ? shortRepoName(gate.blocking_repo) : "";
  if (repo) {
    return t("todos.repo_gate_row_blocked", { ready, total, repo });
  }
  return t("todos.repo_gate_row_pending", { ready, total });
}

/** Full tooltip text for a slim row repo_gate badge, retaining full repo path and reason on hover. */
export function formatRowRepoGateTitle(gate: TodoRepoGate): string {
  const summary = formatRowRepoGate(gate);
  const fullRepo = gate.blocking_repo?.trim();
  const reason = repoGateReasonLabel(gate);
  const parts: string[] = [];
  if (fullRepo) parts.push(fullRepo);
  if (reason && reason !== gate.state) parts.push(reason);
  return parts.length > 0 ? `${summary} (${parts.join(" · ")})` : summary;
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
