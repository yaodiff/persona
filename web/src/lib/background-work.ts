import { t } from "../i18n";
import { stateOf } from "./format";
import type { Session, Subagent, Workflow } from "./types";
import type { SessionLookup, Todo } from "./todos";
import { backgroundCommandLine, backgroundWaitLine, waitingOnWorkflow, workflowCounts, type WaitLine } from "./workflows";
import { runningChildren } from "./child-sessions";
import { timeOrNaN } from "./times";

/** Live work, not a waiting label: summary counts win over omitted detail arrays. */
export interface BackgroundWork {
  tasks: number;
  subagents: number;
  workflows: number;
  workflow?: Workflow;
}

export function backgroundWork(session: Session | undefined): BackgroundWork | undefined {
  if (!session) return undefined;
  const background = session.background ?? [];
  const summary = typeof session.waiting === "boolean";
  const workflow = summary ? session.workflow_progress : session.workflows?.find((w) => w.running);
  const workflows = summary ? session.running_workflows ?? 0 : session.workflows?.filter((w) => w.running).length
    ?? background.filter((b) => b.kind === "workflow").length;
  const own = summary ? session.running_subagents ?? 0 : session.subagents?.filter((a) => a.running).length
    ?? background.filter((b) => b.kind === "agent").length;
  // its child sessions at work (#5120) are its subagents too; a Paseo parent may list the same ones as background
  // agents already, so the larger count stands rather than their sum
  const subagents = Math.max(own, runningChildren(session));
  // Old summaries expose only a raw background count, which may include workflows and agents. It cannot be split
  // reliably: retain a generic active-work line rather than invent an independent job count.
  const tasks = session.background_tasks_count ?? (summary ? 0
    : background.filter((b) => b.kind !== "workflow" && b.kind !== "agent").length);
  return tasks || subagents || workflows || session.background_working || (summary && session.background_tasks_count === undefined && (session.background_count ?? 0) > 0)
    ? { tasks, subagents, workflows, workflow } : undefined;
}

/** Background commands that look stuck (#13744, Session.background_stalled_since): the Agent is still Working (a running
 *  tool is work), and this says how long they have used no CPU and written no output. Null when the hub says nothing. */
export interface BackgroundStall {
  /** The short line: "Background commands idle for 3 hr". */
  text: string;
  /** What it means, for a tooltip. */
  title: string;
}

export function backgroundStall(s: Session | undefined, now = Date.now()): BackgroundStall | null {
  if (!s || stateOf(s) !== "working" || !s.background_working || s.background_waiting) return null;
  const text = stalledText(s.background_stalled_since, now);
  return text ? { text, title: t("lib-state-labels.bg_stalled_hint") } : null;
}

/** "Background commands quiet for 3 hr" from the time they went quiet; "" when there is none (or it is Go's zero time). */
export function stalledText(since: string | undefined, now = Date.now()): string {
  const at = timeOrNaN(since);
  if (!Number.isFinite(at) || at < 86400_000) return "";
  const minutes = Math.max(1, Math.floor((now - at) / 60_000));
  if (minutes < 60) return t("lib-state-labels.bg_stalled_minutes", { count: minutes });
  return minutes < 2880 ? t("lib-state-labels.bg_stalled_hours", { count: Math.floor(minutes / 60) })
    : t("lib-state-labels.bg_stalled_days", { count: Math.floor(minutes / 1440) });
}

/** The hub says the agent's turn is over and a subagent it launched in the background still runs (#5030, cindy's
 *  background subtasks): the chat draws that turn finished (its summary folded, the subagent's card running inside it),
 *  and the bar above the composer says model usage goes on. A running workflow says so first (workflows.ts
 *  backgroundLine). */
export function waitingOnSubagents(s: Session | undefined, subagents: Subagent[] | undefined = s?.subagents): boolean {
  return Boolean(s?.background_working) && !waitingOnWorkflow(s) && (subagents ?? []).some((a) => a.running && a.background);
}

export type BackgroundTurnKind = "workflow" | "subtasks" | "waiting" | "command";

export interface BackgroundActivity {
  streaming: boolean;
  outgoing: boolean;
  awaiting: boolean;
}

/** The one background kind that represents an ended turn, in display priority order. Null leaves the turn live. A
 *  command requires the hub's explicit background_working flag: a foreground turn, and an older hub without the flag,
 *  keep their old rendering even when background tasks are listed. */
export function backgroundTurn(s: Session | undefined, subagents: Subagent[] | undefined, activity: BackgroundActivity): { kind: BackgroundTurnKind; line?: WaitLine } | null {
  if (!s || activity.streaming || activity.outgoing || activity.awaiting) return null;
  if (waitingOnWorkflow(s)) return { kind: "workflow" };
  if (waitingOnSubagents(s, subagents)) return { kind: "subtasks" };
  const wait = backgroundWaitLine(s);
  if (wait) return { kind: "waiting", line: wait };
  // Some runtimes list delegated work only as background agents / workflows, without the detail arrays above. Those
  // are not a shell-command fallback, and a runtime that sends only background_working still gets the label alone.
  if (s.background?.some((b) => b.kind === "agent" || b.kind === "workflow")) return null;
  const command = backgroundCommandLine(s);
  return command ? { kind: "command", line: command } : null;
}

/** One task can have several active agents: do not mistake a quiet lead for an idle task. */
export function taskBackgroundWork(todo: Todo | undefined, lookup: SessionLookup): BackgroundWork | undefined {
  if (!todo || todo.done_at) return undefined;
  // Deduplicate identities before lookup: an answered-state adapter may return a fresh object on each call.
  const bindings = new Map((todo.agents ?? []).filter((a) => !a.released_at).map((a) => [JSON.stringify([a.machine_id, a.session_id]), a]));
  const work = [...bindings.values()].map((a) => backgroundWork(lookup(a.machine_id, a.session_id))).filter((w): w is BackgroundWork => !!w);
  if (!work.length) return undefined;
  const workflows = work.flatMap((w) => w.workflow ? [w.workflow] : []).sort((a, b) => (b.started_at ?? "").localeCompare(a.started_at ?? ""));
  return { tasks: work.reduce((n, w) => n + w.tasks, 0), subagents: work.reduce((n, w) => n + w.subagents, 0),
    workflows: work.reduce((n, w) => n + w.workflows, 0), workflow: workflows[0] };
}

/** Same phase and done/started count as the Agent's running footer; other work stays explicit. */
export function backgroundWorkLine(work: BackgroundWork | undefined): string | undefined {
  if (!work) return undefined;
  const parts: string[] = [];
  if (work.workflows) {
    parts.push([t("chat-runningstatusbar.background_workflow"), work.workflow?.phase, work.workflow ? workflowCounts(work.workflow) : undefined,
      work.workflows > 1 ? t("chat-runningstatusbar.more_workflows", { count: work.workflows - 1 }) : undefined].filter(Boolean).join(" · "));
  }
  if (work.tasks) parts.push(t("assistant-tasks.background_tasks_running", { count: work.tasks }));
  if (work.subagents) parts.push(t("assistant-tasks.subagents_count_running", { count: work.subagents }));
  return parts.join(" · ") || t("assistant-tasks.background_running");
}
