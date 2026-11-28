// Pure row-summary & section-folding helpers for the slim todos_v2 left pane (#14830).
// Strictly used inside `todos_v2 === true` branches; legacy mode never calls these.

import { t } from "../i18n";
import { waitSummary } from "./state-labels";
import { parkedLine } from "./supervision";
import { cancellationReason } from "./todo-details";
import { isAgentTask } from "./todo-list-filter";
import { openMergePRs, type OwnerNeed, type TriageSection } from "./todo-triage";
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
    (todo.repo_gate && todo.repo_gate.state !== "reference_only" && todo.repo_gate.target_count > 0)
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

/**
 * Picks the single highest-signal secondary cue for line 2 of a slim task row under todos_v2 (#14830),
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

  if (todo.wait != null) return { tone: "muted", text: waitSummary(todo.wait), testId: "todo-slim-cue" };
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
