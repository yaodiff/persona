import type { AgentPhase } from "./agent-view-wire";
// The one set of names for an agent's and a task's state on the web (#3415; docs/protocol/i18n.md "Agent states",
// docs/protocol/agent-states.md): every page takes its state words from here, so a state is never Needs you on one page and Waiting for you
// on the next. The words are in lib-state-labels.json; state-labels.test.ts keeps other namespaces from defining them.
import { t } from "../i18n";
import type { TodoWait, TodoWaitKind } from "./todo-wait-wire";
import type { StatusMarkState } from "./types";

/** An agent's state (StatusMarkState), Waiting, Waiting in the background (#6045), Awaiting reply (#5096), Just finished (an agent's turn or a to-do, #9751), and the states only a task or a summary has: Done, Interrupted, Follow-up, Not started, Didn't start. A task whose agent waits to be brought back (#3274) is named by what it waits on (waitName). */
export type StateName = StatusMarkState | "waiting" | "bg_waiting" | "awaits_reply" | "just_finished" | "done" | "interrupted" | "follow_up" | "not_started" | "failed";

export function stateName(s: StateName): string {
  return t(`lib-state-labels.${s}`);
}

/** What a waiting task waits on (#11090, lib/todo-wait-reason.ts waitReason): its owner (`owner`, the sidebar badge's
 *  waitsOnOwner), a prerequisite, a merge or deploy, something outside Persona, its own background work, the hub's
 *  automatic retry, or nothing it can tell (`unknown`). Only `owner` asks anything of the owner. */
export type LegacyWaitReason = "owner" | "prerequisite" | "delivery" | "external" | "background" | "retry" | "unknown";

export type WaitReason = LegacyWaitReason | TodoWaitKind;

const waitKindLabels: Record<TodoWaitKind, string> = {
  person_reply: "task-wait.kind_person_reply",
  merge_review: "task-wait.kind_merge_review",
  pr_merge: "task-wait.kind_pr_merge",
  external_event: "task-wait.kind_external_event",
  date: "task-wait.kind_date",
  todo_handoff: "task-wait.kind_todo_handoff",
  agent: "task-wait.kind_agent",
  usage_limit: "lib-usage-limit.short",
  parked: "lib-state-labels.waiting",
};

/** A waiting task's state word: Waiting on you, Waiting on a prerequisite … Paused (nothing needed from you). */
export function waitName(r: WaitReason): string {
  if (Object.hasOwn(waitKindLabels, r)) return t(waitKindLabels[r as TodoWaitKind]);
  return t(`lib-state-labels.waits_${r}`);
}

export function waitSummary(wait: TodoWait): string {
  const label = waitName(serverWaitReason(wait));
  return wait.waiting_on ? label + t("task-wait.target", { target: wait.waiting_on }) : wait.reason || label;
}

export function serverWaitReason(wait: TodoWait): TodoWaitKind {
  return Object.hasOwn(waitKindLabels, wait.kind) ? wait.kind : "parked";
}

/** The states a list counts in its headers and summaries: "3 need you". */
export type CountedState = "needs_you" | "working" | "bg_waiting" | "unread" | "done";

export function stateCount(s: CountedState, count: number): string {
  return t(`lib-state-labels.count_${s}`, { count });
}

/** The label for marking all finished, unseen agents as seen, optionally with a count. */
export function markAllSeenLabel(count?: number): string {
  return count === undefined ? t("lib-state-labels.mark_all_seen") : t("lib-state-labels.mark_all_seen_count", { count });
}

export function phaseName(phase: AgentPhase): string {
  return phase === "idle" ? t("lib-state-labels.phase_idle") : stateName(phase);
}

export const STATE_LABEL: Record<StatusMarkState, string> = {
  needs_you: stateName("needs_you"),
  working: stateName("working"),
  done: stateName("done"),
  error: stateName("error"),
  unread: stateName("unread"),
  idle: stateName("idle"),
  offline: stateName("offline"),
  unknown: stateName("unknown"),
};
