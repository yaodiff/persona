// Pure row-summary & section-folding helpers for the slim todos_v2 left pane (#14830).
// Strictly used inside `todos_v2 === true` branches; legacy mode never calls these.

import { t } from "../i18n";
import { shouldShowRowRepoGate } from "../features/todos/repo-gate";
import { serverWaitReason, waitName, waitSummary } from "./state-labels";
import { parkedLine } from "./supervision";
import { cancellationReason } from "./todo-details";
import { isAgentTask } from "./todo-list-filter";
import { isRawTodoId } from "./todo-shared-blockers";
import { openMergePRs, type OwnerNeed, type TriageSection } from "./todo-triage";
import type { TodoWait } from "./todo-wait-wire";
import { askTone, heldDelivery, isCancelled, openAsk, responsibleOf, type Todo } from "./todos";

export type SlimCueTone = "attention" | "red" | "green" | "orange" | "muted";

export interface SlimSecondaryCue {
  tone: SlimCueTone;
  text: string;
  testId?: string;
  askOutcome?: string;
}

export type SlimLine2BadgeKind =
  | "goal_blocked"
  | "child_needs_you"
  | "repo_gate"
  | "mergetrain"
  | "parent_goal"
  | "child_working"
  | "direction"
  | "project"
  | "repo_count";

export interface SlimLine2BadgeAvailability {
  goalBlocked?: boolean;
  childNeedsYou?: boolean;
  repoGate?: boolean;
  mergeTrain?: boolean;
  parentGoal?: boolean;
  childWorking?: boolean;
  direction?: boolean;
  project?: boolean;
  repoCount?: boolean;
}

export interface SlimLine2BadgeBudgetResult {
  shown: readonly SlimLine2BadgeKind[];
  omittedCount: number;
}

const SLIM_LINE2_PRIORITY: readonly { kind: SlimLine2BadgeKind; key: keyof SlimLine2BadgeAvailability }[] = [
  { kind: "goal_blocked", key: "goalBlocked" },
  { kind: "child_needs_you", key: "childNeedsYou" },
  { kind: "repo_gate", key: "repoGate" },
  { kind: "mergetrain", key: "mergeTrain" },
  { kind: "parent_goal", key: "parentGoal" },
  { kind: "child_working", key: "childWorking" },
  { kind: "direction", key: "direction" },
  { kind: "project", key: "project" },
  { kind: "repo_count", key: "repoCount" },
];

/**
 * Enforces a strict line-2 metadata badge quota on narrow slim task rows under todos_v2 (#17696)
 * so the highest-signal wait/decision cue (`flex-1 truncate`) retains ample readable width in narrow
 * and large-text layouts. Lower-priority badges demote cleanly to the detail pane.
 */
export function selectSlimLine2Badges(
  available: SlimLine2BadgeAvailability,
  options: { hasCue: boolean; hasAction?: boolean },
): SlimLine2BadgeBudgetResult {
  const candidates: SlimLine2BadgeKind[] = [];
  for (const entry of SLIM_LINE2_PRIORITY) {
    if (available[entry.key]) candidates.push(entry.kind);
  }
  const maxBadges = options.hasCue ? (options.hasAction ? 0 : 1) : options.hasAction ? 1 : 2;
  const shown = candidates.slice(0, maxBadges);
  return {
    shown,
    omittedCount: Math.max(0, candidates.length - shown.length),
  };
}

/** Under todos_v2 (#14830), low-noise sections (parked, shelved, done, cancelled) start compactly folded by default. */
export function sectionStartsFoldedV2(section: TriageSection): boolean {
  return section === "parked" || section === "shelved" || section === "done" || section === "cancelled";
}

/**
 * Under todos_v2 (#14830), plain personal to-dos render as a single lightweight line (checkbox + title + due date),
 * visually distinct from AI-backed work goals and tasks.
 */
export function isPersonalSlimRow(
  todo: Todo,
  options: {
    hasGoalSummary: boolean;
    hasParentGoal: boolean;
    effectiveSection?: TriageSection;
  },
): boolean {
  if (todo.wait != null || isAgentTask(todo)) return false;
  if (options.hasGoalSummary || options.hasParentGoal) return false;
  if (options.effectiveSection === "parked" || options.effectiveSection === "shelved") return false;
  if (
    openAsk(todo) ||
    todo.pr_url?.trim() ||
    todo.pr_status?.url?.trim() ||
    todo.delivery ||
    todo.start_error ||
    responsibleOf(todo) ||
    shouldShowRowRepoGate(todo.repo_gate, {
      effectiveSection: options.effectiveSection,
      done: Boolean(todo.done_at),
      hasExecutionFailure: Boolean(todo.start_error),
    })
  ) {
    return false;
  }
  return true;
}

/** Count of bound repositories (or legacy linked spaces) attached to a to-do. */
export function repoCountOf(todo: Todo & { repositories?: readonly unknown[] }): number {
  const repos = Array.isArray(todo.repositories) ? todo.repositories.length : 0;
  const spaces = Array.isArray(todo.spaces) ? todo.spaces.length : 0;
  return Math.max(repos, spaces);
}

const PR_NUM_RE = /(?:#|!|\/(?:pull|merge_requests)\/)(\d+)/i;

function extractPrNumber(raw?: string): string | undefined {
  if (!raw) return undefined;
  return PR_NUM_RE.exec(raw)?.[1];
}

/**
 * Reports whether a wait target string merely names the same PR/MR already shown on line 1
 * (e.g. "PR #1446", "PR #1446 (nwverse/persona)", "nwverse/persona#1446", or its URL).
 */
export function waitTargetDuplicatesLine1PR(target: string, line1PrText?: string): boolean {
  const num = extractPrNumber(line1PrText);
  if (!num) return false;
  const trimmed = target.trim();
  if (!trimmed) return false;
  const escaped = num.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const matchRe = new RegExp(`(?:\\b(?:PR|MR)\\s*[#!]?${escaped}\\b|[#!]${escaped}\\b|\\/(?:pull|merge_requests)\\/${escaped}\\b)`, "i");
  if (!matchRe.test(trimmed)) return false;
  // Strip PR/MR label, repo slug, URL, and punctuation: if nothing substantive remains, it's a pure duplicate.
  const remainder = trimmed
    .replace(/https?:\/\/\S+/gi, "")
    .replace(/\b(?:PR|MR)\s*[#!]?\d+\b/gi, "")
    .replace(/[a-z0-9_.-]+\/[a-z0-9_.-]+(?:[#!]?\d+)?/gi, "")
    .replace(/[#!]?\d+/g, "")
    .replace(/[\s()[\]·,:：\-_/]+/g, "");
  return remainder.length === 0;
}

function resolveWaitTarget(
  wait: TodoWait,
  todosById?: ReadonlyMap<string, Pick<Todo, "id" | "title">>,
): { target: string; fromTodo: boolean } {
  const handoffId = wait.handoff_todo_id?.trim();
  const raw = wait.waiting_on?.trim() ?? "";

  if (raw && !isRawTodoId(raw)) {
    return { target: raw, fromTodo: Boolean(handoffId) };
  }

  const lookupId = (raw && isRawTodoId(raw) ? raw : handoffId) || "";
  if (lookupId) {
    const resolved = todosById?.get(lookupId)?.title?.trim();
    if (resolved) return { target: resolved, fromTodo: true };
    if (raw && isRawTodoId(raw)) {
      return { target: t("todos.shared_blockers_unknown_todo"), fromTodo: true };
    }
  }
  return { target: "", fromTodo: Boolean(handoffId) };
}

function isPrerequisiteWait(todo: Todo, wait: TodoWait, resolvedFromTodo: boolean): boolean {
  if (wait.kind === "todo_handoff" || Boolean(wait.handoff_todo_id) || resolvedFromTodo) return true;
  if (todo.active_blockers?.some((b) => b.state === "active" && (b.kind === "todo_done" || b.kind === "todo_stage"))) {
    return true;
  }
  return Boolean(todo.prerequisite_blockers?.length);
}

function prBlockerCue(wait: TodoWait): { tone: SlimCueTone; label: string } | undefined {
  const blockers = wait.pr_blockers;
  if (!blockers?.length) return undefined;
  if (blockers.includes("conflicts")) return { tone: "red", label: t("todos.slim_pr_conflicts") };
  if (blockers.includes("checks_failing")) return { tone: "red", label: t("todos.slim_pr_ci_failed") };
  if (blockers.includes("changes_requested")) return { tone: "orange", label: t("todos.slim_pr_changes") };
  if (blockers.includes("review_required")) return { tone: "muted", label: t("todos.slim_wait_merge_review") };
  if (blockers.includes("checks_pending")) return { tone: "muted", label: t("todos.slim_pr_checks_pending") };
  return undefined;
}

function gateFallbackCueForLine1PR(todo: Todo): SlimSecondaryCue {
  switch (todo.repo_gate?.state) {
    case "blocked_conflict":
      return { tone: "red", text: t("todos.slim_pr_conflicts"), testId: "todo-slim-cue" };
    case "blocked_ci":
      return { tone: "red", text: t("todos.slim_pr_ci_failed"), testId: "todo-slim-cue" };
    case "blocked_changes":
      return { tone: "orange", text: t("todos.slim_pr_changes"), testId: "todo-slim-cue" };
    case "awaiting_review":
      return { tone: "muted", text: t("todos.slim_wait_merge_review"), testId: "todo-slim-cue" };
    case "checks_pending":
      return {
        tone: "muted",
        text: todo.repo_gate.reason === "checks_pending" ? t("todos.slim_pr_checks_pending") : t("todos.slim_wait_pr_merge"),
        testId: "todo-slim-cue",
      };
    default:
      return { tone: "muted", text: t("todos.slim_wait_pr_merge"), testId: "todo-slim-cue" };
  }
}

/**
 * Formats a concise, high-signal wait cue for slim task rows under todos_v2 (#17938):
 * - Avoids verbose prefixes like "等待外部变化：" that truncate real prerequisite titles.
 * - Drops redundant PR numbers already rendered on line 1.
 * - Resolves any raw `todo_...` wait targets via `todosById` so raw IDs never surface.
 */
export function slimWaitCue(
  todo: Todo,
  wait: TodoWait,
  options: {
    line1PrText?: string;
    todosById?: ReadonlyMap<string, Pick<Todo, "id" | "title">>;
  } = {},
): SlimSecondaryCue {
  const kind = serverWaitReason(wait);
  const { target, fromTodo } = resolveWaitTarget(wait, options.todosById);
  const dupPR = Boolean(target && waitTargetDuplicatesLine1PR(target, options.line1PrText));
  const prereq = isPrerequisiteWait(todo, wait, fromTodo);
  const reason = wait.reason?.trim() ?? "";

  switch (kind) {
    case "pr_merge": {
      const blocker = prBlockerCue(wait);
      if (blocker) {
        return {
          tone: blocker.tone,
          text: target && !dupPR ? `${blocker.label} · ${target}` : blocker.label,
          testId: "todo-slim-cue",
        };
      }
      return {
        tone: "muted",
        text: target && !dupPR ? t("todos.slim_wait_pr_merge_target", { target }) : t("todos.slim_wait_pr_merge"),
        testId: "todo-slim-cue",
      };
    }
    case "merge_review":
      return {
        tone: "muted",
        text: target && !dupPR ? t("todos.slim_wait_merge_review_target", { target }) : t("todos.slim_wait_merge_review"),
        testId: "todo-slim-cue",
      };
    case "todo_handoff":
      return {
        tone: "muted",
        text: target
          ? t("todos.slim_wait_prereq_target", { target })
          : reason || t("todos.slim_wait_prereq"),
        testId: "todo-slim-cue",
      };
    case "person_reply":
      return {
        tone: "muted",
        text: target
          ? t("todos.slim_wait_reply_target", { target })
          : reason || t("todos.slim_wait_reply"),
        testId: "todo-slim-cue",
      };
    case "date":
      return {
        tone: "muted",
        text: target
          ? t("todos.slim_wait_date_target", { target })
          : reason || t("todos.slim_wait_date"),
        testId: "todo-slim-cue",
      };
    case "agent":
      return {
        tone: "muted",
        text: target ? t("todos.slim_wait_agent_target", { target }) : reason || waitName("agent"),
        testId: "todo-slim-cue",
      };
    case "usage_limit":
      return { tone: "muted", text: waitSummary(wait), testId: "todo-slim-cue" };
    case "external_event": {
      if (dupPR) {
        return gateFallbackCueForLine1PR(todo);
      }
      if (target) {
        return {
          tone: "muted",
          text: prereq ? t("todos.slim_wait_prereq_target", { target }) : t("todos.slim_wait_target", { target }),
          testId: "todo-slim-cue",
        };
      }
      if (reason && !isRawTodoId(reason)) {
        return { tone: "muted", text: reason, testId: "todo-slim-cue" };
      }
      return { tone: "muted", text: prereq ? t("todos.slim_wait_prereq") : t("todos.slim_wait_external"), testId: "todo-slim-cue" };
    }
    default: {
      if (dupPR) return gateFallbackCueForLine1PR(todo);
      if (target) {
        return {
          tone: "muted",
          text: prereq ? t("todos.slim_wait_prereq_target", { target }) : t("todos.slim_wait_target", { target }),
          testId: "todo-slim-cue",
        };
      }
      if (reason && !isRawTodoId(reason)) {
        return { tone: "muted", text: reason, testId: "todo-slim-cue" };
      }
      return { tone: "muted", text: waitName("parked"), testId: "todo-slim-cue" };
    }
  }
}

/**
 * Picks the single highest-signal secondary cue for line 2 of a slim task row under todos_v2 (#14830, #17938),
 * dropping redundant status labels that repeat the section heading and verbose 3rd-line process prose.
 */
export function slimSecondaryCue(
  todo: Todo,
  view: { urgentLead: string; urgentText: string; orphaned: boolean; owner?: OwnerNeed; supervisorLabel?: string },
  options: {
    effectiveSection?: TriageSection;
    failedOpen: boolean;
    held: ReturnType<typeof heldDelivery>;
    due?: string;
    overdue: boolean;
    now?: number;
    line1PrText?: string;
    todosById?: ReadonlyMap<string, Pick<Todo, "id" | "title">>;
  },
): SlimSecondaryCue | null {
  if (options.failedOpen) {
    return {
      tone: "red",
      text: t("todos.start_failed", { error: todo.start_error ?? "" }),
      testId: "todo-start-failed",
    };
  }

  const ask = openAsk(todo);
  if (ask) {
    return {
      tone: askTone(ask),
      text: ask.verdict,
      testId: "todo-ask",
      askOutcome: ask.outcome || "deployed",
    };
  }

  if (todo.wait != null) {
    return slimWaitCue(todo, todo.wait, {
      line1PrText: options.line1PrText,
      todosById: options.todosById,
    });
  }
  if (view.supervisorLabel) return { tone: "muted", text: view.supervisorLabel, testId: "todo-slim-cue" };

  if (options.effectiveSection === "waiting") {
    const urgent = `${view.urgentLead}${view.urgentText}`.trim();
    if (urgent) {
      return {
        tone: view.orphaned ? "orange" : "attention",
        text: urgent,
        testId: "todo-slim-cue",
      };
    }
    if (view.owner) {
      return {
        tone: "attention",
        text: t(`todos.owner_why_${view.owner}`),
        testId: "todo-slim-cue",
      };
    }
  }

  if (options.effectiveSection === "parked") {
    const parked = parkedLine(todo);
    if (parked) {
      return { tone: "muted", text: parked, testId: "todo-slim-cue" };
    }
    if (openMergePRs(todo, options.now ?? Date.now()).length > 0) {
      return { tone: "muted", text: t("todos.parked_folded_hint"), testId: "todo-slim-cue" };
    }
  }

  if (options.effectiveSection === "just_done" && options.held) {
    return {
      tone: "orange",
      text: t(`todos.delivery_${options.held.state}`),
      testId: "todo-slim-cue",
    };
  }

  if (isCancelled(todo)) {
    const reason = cancellationReason(todo);
    if (reason) {
      return { tone: "muted", text: reason, testId: "todo-cancelled-reason" };
    }
  }

  if (options.due) {
    return {
      tone: options.overdue ? "red" : "muted",
      text: t(options.overdue ? "todos.was_due" : "todos.due", { when: options.due }),
      testId: "todo-due-compact",
    };
  }

  return null;
}
