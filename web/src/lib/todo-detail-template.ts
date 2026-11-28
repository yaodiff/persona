// Dual-template classification & action resolution for Web TodoDetail under todos_v2 (#14777, #14851).
// Pure logic module: decides whether a to-do uses the lightweight "personal" template or the
// action-driven "work_goal" template and resolves the primary CTA when an active Agent is running.
// All right-pane sections remain expanded by default under todos_v2 (#14851).

import { foldedState } from "./child-sessions";
import { safeWebURL } from "./format";
import { explicitTodoItemType } from "./todo-list-filter";
import { makeSessionLookup, responsibleOf, type SessionLookup, type Todo } from "./todos";
import type { AgentState, Session } from "./types";

export type TodoDetailTemplate = "personal" | "work_goal";

/** Extended todo shape supporting optional repository/PR arrays when present on newer payloads. */
export type TodoDetailInput = Todo & {
  repositories?: readonly unknown[];
  prs?: readonly (string | { url?: string | null })[];
};

export interface ActiveAgentTarget {
  machineId: string;
  sessionId: string;
  title?: string;
  state: AgentState;
}

export interface WorkGoalActions {
  primaryAction: "open_agent" | "hand_over";
  activeAgent?: ActiveAgentTarget;
  prUrl?: string;
  handOverTone: "accent" | "secondary";
  handOverMode: "hand_over" | "add_agent";
}

/** Returns a validated credential-free http(s) URL string, or undefined for missing or unsafe URLs. */
export function safeDetailHttpUrl(raw: unknown): string | undefined {
  if (typeof raw !== "string") return undefined;
  const trimmed = raw.trim();
  if (!trimmed || /\s/.test(trimmed)) return undefined;
  return safeWebURL(trimmed);
}

/** Extracts the first safe http(s) PR URL associated with a to-do, if any. */
export function firstSafePrUrl(todo: TodoDetailInput): string | undefined {
  const candidates: unknown[] = [todo.pr_status?.url, todo.pr_url];
  if (Array.isArray(todo.other_prs)) {
    for (const pr of todo.other_prs) candidates.push(pr?.url);
  }
  if (Array.isArray(todo.prs)) {
    for (const pr of todo.prs) {
      candidates.push(typeof pr === "string" ? pr : pr?.url);
    }
  }
  for (const raw of candidates) {
    const safe = safeDetailHttpUrl(raw);
    if (safe) return safe;
  }
  return undefined;
}

/** Whether the to-do carries execution metadata beyond a plain personal note/reminder. */
function hasExecutionSignals(todo: TodoDetailInput): boolean {
  return Boolean(
    todo.audience === "autonomous" ||
    todo.origin ||
    todo.plan_id ||
    todo.brief ||
    todo.stage ||
    todo.delivery ||
    todo.start_error ||
    todo.ask ||
    todo.thread ||
    responsibleOf(todo) ||
    (todo.spaces && todo.spaces.length > 0),
  );
}

/**
 * Classifies a to-do into `"personal"` (lightweight note/reminder form) or `"work_goal"`
 * (action-driven workspace with ownership strip, repo matrix, subtasks, and dynamic CTA).
 */
export function classifyTodoDetailTemplate(
  todo: TodoDetailInput,
  childCount = 0,
  options?: { repositoryCount?: number },
): TodoDetailTemplate {
  const explicit = explicitTodoItemType(todo.item_type);
  if (explicit) return explicit === "task" ? "work_goal" : "personal";
  if ((todo.agents?.length ?? 0) > 0) return "work_goal";
  if (todo.supervision) return "work_goal";
  if ((todo.repositories?.length ?? 0) > 0 || (options?.repositoryCount ?? 0) > 0) return "work_goal";
  if (
    Boolean(todo.pr_url?.trim()) ||
    Boolean(todo.pr_status?.url?.trim()) ||
    (todo.other_prs?.length ?? 0) > 0 ||
    (todo.prs?.length ?? 0) > 0
  ) {
    return "work_goal";
  }
  return childCount > 0 || hasExecutionSignals(todo) ? "work_goal" : "personal";
}

function agentPriorityRank(state: AgentState, session: Session): number {
  if (state === "needs_you") return 0;
  if (state === "working" || session.starting) return 1;
  if (state === "unread") return 2;
  return 3;
}

/**
 * Resolves the primary and secondary actions for a work-goal detail view.
 * - Prefers an active, unreleased linked Agent session (`needs_you` > `working` > `unread` > live session).
 * - When an active Agent exists, promotes `"open_agent"` to the primary CTA and demotes Hand-to-Agent
 *   to secondary `"add_agent"` to prevent accidental duplicate dispatches.
 * - Extracts the first safe `http(s)` PR link for a parallel quick-open action.
 */
export function resolveWorkGoalActions(
  todo: TodoDetailInput,
  sessions?: readonly Session[] | SessionLookup,
): WorkGoalActions {
  const prUrl = firstSafePrUrl(todo);
  const lookup: SessionLookup | undefined =
    typeof sessions === "function"
      ? sessions
      : Array.isArray(sessions)
        ? makeSessionLookup([...sessions])
        : undefined;

  const unreleased = (todo.agents ?? []).filter((a) => !a.released_at && !a.start_error);
  let best: { target: ActiveAgentTarget; rank: number; updatedAt: number } | undefined;

  for (const agent of unreleased) {
    const session = lookup?.(agent.machine_id, agent.session_id);
    if (!session) continue;
    const state = foldedState(session);
    if (state === "offline" && !session.starting) continue;

    const rank = agentPriorityRank(state, session);
    const updatedAt = Date.parse(session.updated_at || agent.linked_at || "") || 0;
    if (!best || rank < best.rank || (rank === best.rank && updatedAt > best.updatedAt)) {
      best = {
        rank,
        updatedAt,
        target: {
          machineId: agent.machine_id,
          sessionId: agent.session_id,
          title: session.title || agent.title,
          state: session.starting && state === "offline" ? "working" : state,
        },
      };
    }
  }

  if (best) {
    return {
      primaryAction: "open_agent",
      activeAgent: best.target,
      ...(prUrl ? { prUrl } : {}),
      handOverTone: "secondary",
      handOverMode: "add_agent",
    };
  }

  return {
    primaryAction: "hand_over",
    ...(prUrl ? { prUrl } : {}),
    handOverTone: "accent",
    handOverMode: "hand_over",
  };
}
