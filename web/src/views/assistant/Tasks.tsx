import { conversationPage } from "../../lib/conversation-page";
// The unified entry in the assistant's conversation (docs/assistant/unified-entry.md; iOS Views/Assistant/Tasks): the plan card,
// task cards that follow their agents, the hub's lines (answered, routed; receipts, reviews and reports are not drawn,
// #8237), the "Today" card, message cards about
// a task, quick results, the dock above the composer, the tasks' questions, the route chip, and a task's detail in the
// side panel.
import { lazy, memo, Suspense, useEffect, useId, useMemo, useRef, useState } from "react";
import { taskMeter, type TaskCardMetadata } from "../../lib/task-cards";
import { agentRowKey, agentRowLabel, agentRowMark, agentRowTone, todoChipLabel, todoChipState } from "../../lib/task-row-status";
import { assistantEntries, type NeedsYouItem } from "../../lib/assistant-entries";
import { directUserInteraction, directUserWaiting, directWaitingCount, takeDirectWaitingRequest } from "../../lib/direct-waiting";
import { useRestoredTodos } from "../../lib/todo-restore";
import {
  answeredItems,
  cardFiled,
  cardSent,
  dayLabel,
  dismissPlan,
  field,
  feedNotice,
  getFeedState,
  sentByPersona,
  planUltracodeToSend,
  removeTask,
  retryKey,
  retryTask,
  routeTodoId,
  startFailure,
  startInFlight,
  setRoute,
  startPlan,
  todayFailedRow,
  useFeed,
  type FeedItem,
  type PlanTask,
  type QuickResult,
  type TaskPlan,
  type TodayBody,
} from "../../lib/feed";
import { backgroundWorkLine, type BackgroundWork } from "../../lib/background-work";
import { acceptedAtRest, awaitsReview, dockAgents, type DockAgent, isRemovable, mostUrgentAgent, openAsk, orphanedLine, personalVerificationAsk, stoppedReason, taskNow, todoStatus, todoSupervisorLabel, TODO_STATUS_INFO, updateTodo, waitingLine, type SessionLookup, type Todo, type TodoStatus } from "../../lib/todos";
import { hasActiveAgentWork } from "../../lib/todo-triage";
import { errorText } from "../../lib/api";
import { usageLimitLabel } from "../../lib/usage-limit";
import { openAgent, openTask } from "../../lib/handoff";
import { navigate } from "../../lib/router";
import { plainMarkdown } from "../../lib/plain-markdown";
import { LiveCard } from "../../ui/AssistantCard";
import type { AssistantView } from "../../lib/extension-wire";
import { getState, hubHas, putTodo, refreshTodos, taskSessionLookup, useStore, type State } from "../../lib/store";
import type { AgentState, Interaction, Item, Machine, Session } from "../../lib/types";
import { Button, Collapse, cx, Loading, Menu, toast, useNow } from "../../ui/atoms";
import { StatusMark } from "../../ui/StatusMark";
import { Icon, type IconName } from "../../ui/icons";

// A morning/evening briefing card (views/assistant/Brief.tsx) loads with the first `brief` item, not with the first
// screen; a task's open acceptance ask loads its AcceptRow through NeedsYouPart (TaskDecision.tsx already imports it).
const BriefCard = lazyPage(() => import("./Brief").then((m) => m.BriefCard));
// A plan task's brief (#3822) comes with the first plan card that has one, as the decision row does.
const PlanBrief = lazy(() => import("./TaskBrief").then((m) => ({ default: m.PlanBrief })));
const TaskActivityDetail = lazyPage(() => import("./TaskActivityDetail").then((m) => m.TaskActivityDetail));

// A decision's row comes with the tray's first decision, not with the first screen (startup-preload's budget); the
// same chunk has a suggested rule's line (#5802), which comes with the first card that carries one, and a task whose
// Agent did not start (#10365), which comes with the first such row.
const NeedsYouPart = lazy(() => import("./TaskDecision"));
/** The suggested rule's line of a card, drawn right after it; nothing for a card without the field, unless Always
 *  was answered on it here (the line then says what was made, though the hub sent the card again without the field). */
const ruleLine = (of: RuleCard) => (of.suggestion || underCard.made?.has(of.id!)) && <Suspense fallback={null}><NeedsYouPart rule={of} /></Suspense>;
underCard.line = ruleLine;
import { SentLine } from "./Sent";
import { feedWake, HandoffCard, HandoffCards, itemPlan, planTaskTodo, TASK_LINE_SLACK_MS, taskHandoffs } from "./Handoff";
import { acceptanceStands, latestReview, parkedLine, progressLine, reviewState } from "../../lib/supervision";
import { isLegacyCodexRouteNotice } from "../../lib/compatibility";
import { namedLauncher, providerIdNote, providerTitle, useProviderNames } from "../../lib/provider-names";
import { sameManager, useChosenManager, useLinkManager, useShownManager } from "../../lib/manager-choice";
import { AnsweredSummary, InteractionCard, underCard } from "../chat/Interactions";
import type { RuleCard } from "../../lib/rule-suggestion";
import { sessionTitle, stateOf } from "../../lib/format";
import { formatDate, formatList, t } from "../../i18n";
import { interactionAsk } from "../../lib/inbox";
import { answeredLookup, answeredSessions } from "./answered-lookup";
import { lazyPage } from "../../lib/lazy-page";
const PlanEditDialog = lazyPage(() => import("./PlanEdit").then((m) => m.PlanEditDialog));
// "All" opens it: its code and its lib/task-board load then, not with the first screen (#4656)
const TaskBoard = lazyPage(() => import("./TaskBoard").then((m) => m.TaskBoard));
export const ParkedTasks = lazyPage(() => import("./ParkedTaskRow").then((m) => m.ParkedTaskRows));
const TaskWaitInfo = lazyPage(() => import("./TaskWaitInfo").then((m) => m.TaskWaitInfo));
// the Finished tray's rows, seeing one and its empty line come with its first opening, in the Parked rows' chunk (#10922)
export const FinishedTasks = lazyPage(() => import("./ParkedTaskRow").then((m) => m.FinishedTaskRows));
import { TaskWorkspaceEditor, TaskWorkspaceLine } from "./TaskWorkspace";
import type { TaskWorkspace } from "../../lib/feed";
import { machineStartBlocked, ULTRACODE_FEATURE } from "../../lib/ultracode";
import { UltracodeChip } from "../chat/Ultracode";
import { byTime } from "../../lib/times";
import { useStoreRows } from "../../lib/store-rows";
import { decisionCards, decisionKey, type DecisionCard } from "../../lib/decision-pager";
import { useDecisionPager } from "./decision-dock";
import { requestHome, requestHomeMachine } from "../../lib/child-sessions";
import { stateName, waitSummary, type WaitReason } from "../../lib/state-labels";
import { statusWord, todoLook, waitOf } from "../../lib/todo-wait-reason";
import { EDITABLE } from "../../lib/shortcuts";

// ── what a task is now ──

export function useTaskLookup() {
  const sessions = useStore((s) => s.sessions);
  const sessionSummaries = useStore((s) => s.sessionSummaries);
  const taskRows = useStore((s) => s.taskRows);
  const interactions = useStore((s) => s.interactions);
  const answered = useStore((s) => s.answered);
  return useMemo(() => answeredLookup(taskSessionLookup({ sessions, sessionSummaries, taskRows }), interactions, answered),
    [sessions, sessionSummaries, taskRows, interactions, answered]);
}

// The cards in the conversation do not take the whole session list, the pending cards or the machines: their store
// selectors work out what each one draws (its status, lead agent, question, place), so a push about another agent
// gives back the same strings and objects (the sessions delta keeps an unchanged session's object) and useStore's
// shallow compare skips the render. A push about its own agent, card or machine still draws it again.

/** What a task is now, from the store: its status and its lead agent. */
function taskNowIn(s: State, todo: Todo, plans: Record<string, TaskPlan>): TaskNow {
  const lookup = answeredLookup(taskSessionLookup(s), s.interactions, s.answered);
  const status = statusOf(todo, lookup, plans);
  return { status, lead: mostUrgentAgent(todo, lookup)?.session, wait: waitOf(todo, status, lookup), active: hasActiveAgentWork(todo, lookup), supervisorLabel: todoSupervisorLabel(todo, status, lookup) };
}

/** A task's status, lead agent and, while it waits, what it waits on (#11090, lib/todo-wait-reason.ts waitOf). */
type TaskNow = { status: TodoStatus; lead?: Session; wait?: WaitReason; active: boolean; supervisorLabel?: string };

/** A task's status and lead agent (undefined without the task). */
export function useTaskNow(todo: Todo | undefined): TaskNow | undefined {
  const plans = useFeed((s) => s.plans);
  return useStore((s) => (todo ? taskNowIn(s, todo, plans) : undefined));
}

export function useTodo(id: string | undefined): Todo | undefined {
  return useStore((s) => (id ? s.todos.find((t) => t.id === id) : undefined));
}

/** A task's status, failed too when a hub from before #1077 left it open and only its plan says the start failed. */
export function statusOf(todo: Todo, lookup: SessionLookup, plans: Record<string, TaskPlan>): TodoStatus {
  const st = todoStatus(todo, lookup);
  return st === "not_started" && startFailure(todo, plans) ? "failed" : st;
}

/** Removes a task (lib/feed removeTask), saying so when it did not go. */
export function removeTaskNow(todo: Todo) {
  removeTask(todo).then(
    (gone) => gone && toast(t("assistant-tasks.removed", { title: todo.title }), "ok"),
    (e) => toast(t("assistant-tasks.remove_failed", { error: String(e instanceof Error ? e.message : e) }), "error"),
  );
}

/** The task's state as an agent state, for its dot and tone. `starting`: a not-started task's Agent is being started
 *  (startInFlight); one nobody dispatched is still, as on the To-dos page (#7552). */
function dotState(st: TodoStatus, starting = true): AgentState {
  return st === "needs_you" || st === "interrupted" ? "needs_you" : st === "usage_limit" ? "idle" : st === "working" || st === "waiting" || (st === "not_started" && starting) ? "working" : st === "unread" ? "unread" : st === "follow_up" ? "idle" : "offline"; // done, failed, not started
}

function TaskMark({ status, size = 9, starting }: { status: TodoStatus; size?: number; starting?: boolean }) {
  if (status === "usage_limit") return <Icon name="clock" size={size + 4} stroke={2.4} className="text-orange" />;
  if (status === "waiting") return <Icon name="clock" size={size + 4} stroke={2.4} className="text-ink-2" />;
  if (status === "failed" || status === "interrupted") return <Icon name="info" size={size + 4} stroke={2.4} className="text-red" />;
  if (status === "done" || status === "unread")
    return <Icon name="check" size={size + 4} stroke={2.4} className={status === "done" ? "text-ink-3" : "text-green"} />;
  return <StatusMark state={dotState(status, starting)} />;
}

/** Where a task runs: machine · folder · agent. `machines` and `plans` default to the stores' own; a selector passes
 *  them in, so what it reads is what it depends on. */
function place(todo: Todo, lead?: Session, machines: Machine[] = getState().machines, plans: Record<string, TaskPlan> = getFeedState().plans): string {
  const a = (todo.agents ?? []).find((x) => !x.released_at) ?? todo.agents?.[0];
  const parts: string[] = [];
  const machineName = (id: string) => machines.find((m) => m.id === id)?.name ?? id;
  if (a) {
    parts.push(machineName(a.machine_id));
    if (a.cwd) parts.push(a.cwd.split("/").filter(Boolean).at(-1) ?? a.cwd);
  } else {
    // No agent (yet, or ever): where the plan meant it to run.
    const planned = todo.plan_id ? plans[todo.plan_id]?.tasks.find((t) => t.todo_id === todo.id) : undefined;
    if (planned) {
      parts.push(machineName(planned.machine));
      parts.push(planned.cwd.split("/").filter(Boolean).at(-1) ?? planned.cwd);
    }
  }
  if (lead?.agent) parts.push(lead.agent);
  return parts.join(" · ");
}

function since(iso?: string): string | undefined {
  if (!iso) return undefined;
  const m = Math.max(0, Math.floor((Date.now() - Date.parse(iso)) / 60000));
  if (m < 1) return t("assistant-tasks.just_started");
  if (m < 60) return t("assistant-tasks.minutes", { count: m });
  return m % 60 ? t("assistant-tasks.hours_minutes", { hours: Math.floor(m / 60), minutes: m % 60 }) : t("assistant-tasks.hours", { count: m / 60 });
}

/** What a task's lead agent says or does now, in a line (lib/todos taskNow): its words, or the step it is on. */
export function progressOf(lead: Session | undefined) {
  const now = taskNow(lead, !!lead && stateOf(lead) === "working");
  return now && { text: now.kind === "words" ? plainMarkdown(now.text) : now.text, for: now.kind === "step" ? since(now.since) : undefined };
}

/** The user card a task's agents wait on: its lead agent's first, else any of its other active agents'. */
export function taskInteraction(todo: Todo, interactions: Interaction[], lead?: Session): Interaction | undefined {
  // A card of a folded child belongs to its parent too (#5120).
  const direct = (ix: Interaction, mid: string, sid: string) => ix.machine_id === mid && ix.session_id === sid;
  const folded = (ix: Interaction, mid: string, sid: string) => requestHomeMachine(ix) === mid && requestHome(ix) === sid;
  const owned = interactions.filter((ix) => !ix.delegating && !ix.mirror_of);
  return (lead && (owned.find((ix) => direct(ix, lead.machine_id, lead.session_id))
    ?? owned.find((ix) => folded(ix, lead.machine_id, lead.session_id)))) ??
    owned.find((ix) => todo.agents?.some((a) => !a.released_at && (direct(ix, a.machine_id, a.session_id) || folded(ix, a.machine_id, a.session_id))));
}

/** What a waiting task needs, in a line (#3301): "Has a question: which framework?", "Waiting for your approval: swift test", "Plan awaiting approval", the words
 *  the to-dos page's agent rows use; undefined without a card. */
export function taskAsk(ix: Interaction | undefined): string | undefined {
  if (!ix) return undefined;
  const kicker =
    ix.kind === "plan" ? t("todos.kicker_plan") : ix.kind === "permission" ? t("todos.kicker_permission") : ix.kind === "screen" ? t("todos.kicker_screen") : ix.kind === "elicitation" ? t("todos.kicker_elicitation") : t("todos.kicker_question");
  const ask = plainMarkdown(interactionAsk(ix).trim()).replace(/\s+/g, " ").trim();
  return ask ? `${t("todos.kicker_line", { kicker })}${ask}` : kicker;
}


/** "Retry" on a task that did not start, or "Continue" on one the model service cut off (lib/feed retryTask), saying why when the
 *  hub refused (the machine offline…). */
export function retryTaskNow(todo: Todo, verb: "retry" | "continue" = "retry") {
  retryTask(todo).catch((e) => toast(verb === "continue" ? t("assistant-tasks.continue_failed", { error: String(e instanceof Error ? e.message : e) }) : t("assistant-tasks.retry_failed", { error: String(e instanceof Error ? e.message : e) }), "error"));
}


/** A task's "Retry" is on its way to the hub. */
export function useRetrying(todoId: string | undefined): boolean {
  return useFeed((s) => (todoId ? Boolean(s.busy[retryKey(todoId)]) : false));
}

// ── the plan card ──

const PERMISSION: Record<string, string> = { acceptEdits: t("assistant-tasks.perm_accept_edits"), plan: t("assistant-tasks.perm_plan"), auto: t("assistant-tasks.perm_auto"), bypassPermissions: t("assistant-tasks.perm_bypass"), default: t("assistant-tasks.perm_default") };

/** A task of a started plan that did not start: why. */
function PlanTaskFailed({ task }: { task: PlanTask }) {
  // A blank reason from the hub would leave the sentence hanging after the colon (#4809).
  const error = task.error?.trim() || t("assistant-tasks.unknown_reason");
  return <span className="text-red">{t("assistant-tasks.plan_task_failed", { title: task.title, error })}</span>;
}

/** Why each task's computer can't start with ultracode (#12; a known Claude Code too old), "" where it can; undefined
 *  where the hub has no ultracode, and the card shows none. Nothing is read for a plan no longer open. */
function usePlanUltracodeBlocked(plan: TaskPlan): string[] | undefined {
  const open = plan.state === "open";
  const offered = useStore((s) => open && hubHas(s.features, ULTRACODE_FEATURE));
  const rows = useStoreRows((s) => (open ? plan.tasks.map((task) => ({ why: machineStartBlocked(s, task.machine) })) : []));
  return offered ? rows.map((r) => r.why) : undefined;
}

/** A computer can't start with ultracode now (what Start sends checks it as it goes). */
const ultracodeBlockedNow = (machine: string) => Boolean(machineStartBlocked(getState(), machine));

/** What "Start these directly" remembers, said on the card (#8095): each task that changes files, by its computer and
 *  folder; the remembered kind then covers that folder's subfolders too. "" when every task only reads: those start
 *  without asking anyway, so there is nothing to remember. */
export function rememberScope(tasks: PlanTask[], machines: Machine[]): string {
  const places = [...new Set(tasks.filter((task) => !task.readonly).map((task) =>
    t("assistant-tasks.plan_remember_place", { machine: machines.find((m) => m.id === task.machine)?.name ?? task.machine, directory: task.cwd })))];
  return places.length ? t("assistant-tasks.plan_remember", { places: formatList(places) }) : "";
}

/** The scheduled task whose own run planned this (#8045), named for the card's "Let … start these by itself"; null for
 *  every other plan, which the card draws as before. */
export function planSchedule(plan: TaskPlan): { id: string; name: string } | null {
  const task = plan.tasks.find((task) => task.schedule);
  if (!task?.schedule) return null;
  return { id: task.schedule, name: task.schedule_title?.trim() || t("assistant-tasks.plan_schedule_unnamed") };
}

export function PlanCard({ plan }: { plan: TaskPlan }) {
  const busy = useFeed((s) => s.busy[plan.id]);
  const machines = useStore((s) => s.machines);
  const [remember, setRemember] = useState(false);
  const [scheduleGrant, setScheduleGrant] = useState(false);
  const grantId = useId();
  const [editing, setEditing] = useState(false);
  const [ultraChoice, setUltraChoice] = useState<Record<number, boolean>>({});
  const [workspaceChoice, setWorkspaceChoice] = useState<Record<number, TaskWorkspace>>({});
  const blocked = usePlanUltracodeBlocked(plan);
  if (plan.state !== "open") {
    const failed = plan.tasks.filter((t) => t.error);
    return (
      <div className="flex flex-col gap-1 text-meta text-ink-2">
        <span className="flex items-center gap-1.5">
          <Icon name={plan.state === "started" ? "check" : "x"} size={13} />
          {plan.state === "started" ? t("assistant-tasks.plan_started", { count: plan.tasks.length }) : t("assistant-tasks.plan_dismissed", { count: plan.tasks.length })}
        </span>
        {failed.map((task) => (
          <PlanTaskFailed key={task.title} task={task} />
        ))}
        {plan.tasks.map((task, i) => <TaskWorkspaceLine key={i} task={task} />)}
      </div>
    );
  }
  const run = (f: () => Promise<void>) => f().catch((e) => toast(t("assistant-tasks.start_failed", { error: String(e instanceof Error ? e.message : e) }), "error"));
  // each task's choice, the card's own else the assistant's: the chip draws it (off where the computer can't), "Edit"
  // opens with it (so a task moved there to a computer that can has it again), and Start sends it, off where it can't
  const ultracode = plan.tasks.map((task, i) => ultraChoice[i] ?? task.ultracode === true);
  const editedPlan = { ...plan, tasks: plan.tasks.map((task, i) => workspaceChoice[i] === undefined ? task : { ...task, workspace: workspaceChoice[i], workspace_resolved: undefined }) };
  const scope = rememberScope(editedPlan.tasks, machines);
  // offered where "Start these directly" is: a plan of only read-only tasks starts without asking anyway
  const schedule = scope ? planSchedule(plan) : null;
  const grant = Boolean(schedule) && scheduleGrant;
  const startEdits = () => {
    const edits = planUltracodeToSend(plan, ultracode, ultracodeBlockedNow) ?? plan.tasks.map(() => ({}));
    const all = edits.map((e, i) => workspaceChoice[i] === undefined ? e : { ...e, workspace: workspaceChoice[i] });
    return all.some((e) => Object.keys(e).length) ? all : undefined;
  };
  return (
    // an open plan may carry a suggested rule: its line goes under the card (#5802)
    <>
    <div className="flex flex-col gap-3 rounded-card bg-surface p-4 shadow-card">
      <div className="flex items-center gap-1.5 text-label font-medium text-ink-2">
        <Icon name="list" size={13} />
        {plan.tasks.length > 1 ? t("assistant-tasks.plan_split", { count: plan.tasks.length }) : t("assistant-tasks.plan_single")}
      </div>
      {editedPlan.tasks.map((t, i) => (
        <div key={i} className="flex min-w-0 flex-col gap-2">
          <PlanTaskRow n={i + 1} task={t} numbered={plan.tasks.length > 1} ultra={blocked && { on: ultracode[i], blocked: blocked[i] }} busy={busy} onUltra={(on) => setUltraChoice((c) => ({ ...c, [i]: on }))} />
          <TaskWorkspaceEditor plan={editedPlan} index={i} disabled={busy} onChange={(workspace) => setWorkspaceChoice((c) => ({ ...c, [i]: workspace }))} />
        </div>
      ))}
      {scope && <label className="flex cursor-pointer items-center gap-2 text-[12.5px] text-ink-2">
        <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} className="accent-[var(--accent)]" />
        {scope}
      </label>}
      {schedule && <div className="grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-0.5">
        <input id={grantId} type="checkbox" checked={scheduleGrant} onChange={(e) => setScheduleGrant(e.target.checked)} aria-describedby={`${grantId}-note`} className="accent-[var(--accent)]" />
        <label htmlFor={grantId} className="cursor-pointer text-label text-ink-2">{t("assistant-tasks.plan_schedule_grant", { name: schedule.name })}</label>
        <p id={`${grantId}-note`} className="col-start-2 text-label text-ink-3">{t("assistant-tasks.plan_schedule_grant_note")}</p>
      </div>}
      {/* Start stays in reach (#3822): a card taller than the tray it scrolls in (several tasks, an opened brief) keeps
          its buttons at the tray's bottom edge; a card that fits looks as it did (the padding it takes it gives back) */}
      <div data-plan-actions className="sticky bottom-0 z-[1] -mx-4 -mt-2 -mb-4 flex gap-2 rounded-b-card bg-surface px-4 pt-2 pb-4">
        <Button tone="accent" busy={busy} onClick={() => run(() => startPlan(plan, remember, startEdits(), grant))} className="flex-1">
          {plan.tasks.length > 1 ? t("assistant-tasks.plan_start_n", { count: plan.tasks.length }) : t("assistant-tasks.plan_start")}
        </Button>
        <Button disabled={busy} onClick={() => setEditing(true)}>
          {t("assistant-tasks.plan_edit")}
        </Button>
        <Button disabled={busy} onClick={() => run(() => dismissPlan(plan))}>
          {t("assistant-tasks.plan_dismiss")}
        </Button>
      </div>
      {editing && <Suspense fallback={<Loading />}>
        <PlanEditDialog
          plan={editedPlan}
          original={plan}
          ultracode={ultracode}
          onClose={() => setEditing(false)}
          onStart={(edits) => {
            setEditing(false);
            void run(() => startPlan(plan, remember, edits, grant));
          }}
        />
      </Suspense>}
    </div>
    {ruleLine(plan)}
    </>
  );
}

function planDispatchReason(dispatch: PlanTask["dispatch"]): string {
  if (!dispatch || !dispatch.reason || dispatch.reason === "only") return "";
  const rawArgs = dispatch.args ?? {};
  const args: Record<string, string | number> = {};
  for (const [k, v] of Object.entries(rawArgs)) {
    if (typeof v === "string" || typeof v === "number") args[k] = v;
  }
  // dispatch.candidate is the 0-based index into the tier's candidates (the same index as /candidates); people count from 1
  const candidate = typeof dispatch.candidate === "number" && dispatch.candidate >= 0 ? dispatch.candidate + 1 : 1;
  const tier = args.tier ?? dispatch.tier ?? "";
  const base: Record<string, string | number> = { ...args, tier, candidate };
  const detail = args.detail ?? args.cause ?? args.provider ?? "";

  switch (dispatch.reason) {
    case "first":
      return t("assistant-tasks.dispatch_reason_first", base);
    case "score":
      if (args.minutes !== undefined && args.first_pass !== undefined && args.headroom !== undefined) {
        return t("assistant-tasks.dispatch_reason_score", base);
      }
      return t("assistant-tasks.dispatch_reason_score_short", base);
    case "quota":
      return detail
        ? t("assistant-tasks.dispatch_reason_quota_detail", { ...base, detail })
        : t("assistant-tasks.dispatch_reason_quota", base);
    case "unavailable":
      return t("assistant-tasks.dispatch_reason_unavailable", base);
    case "kept":
      return t("assistant-tasks.dispatch_reason_kept", base);
    case "switched":
      return detail
        ? t("assistant-tasks.dispatch_reason_switched_detail", { ...base, detail })
        : t("assistant-tasks.dispatch_reason_switched", base);
    case "over_pace":
      return t("assistant-tasks.dispatch_reason_over_pace", base);
    default:
      return t("assistant-tasks.dispatch_reason_fallback", { ...base, reason: dispatch.reason });
  }
}

function PlanTaskRow({ n, task, numbered, ultra, busy, onUltra }: { n: number; task: PlanTask; numbered: boolean; ultra?: { on: boolean; blocked: string }; busy?: boolean; onUltra: (on: boolean) => void }) {
  const machines = useStore((s) => s.machines);
  const machine = machines.find((m) => m.id === task.machine)?.name ?? task.machine;
  // its launcher by the provider's display name, the startup ID in the chip's tooltip (#5029): the providers of the
  // assistant the chat belongs to, else the current one's
  const chatManager = useLinkManager();
  const chosen = useChosenManager();
  const names = useProviderNames(chatManager ?? chosen ?? "");
  const launcher = task.launcher && !task.model ? namedLauncher(task.launcher, names) : null;
  const chips: { icon: IconName; text: string; title?: string }[] = [
    { icon: "machine", text: machine },
    { icon: "folder", text: task.cwd.split("/").filter(Boolean).at(-1) ?? task.cwd },
    // an explicit model wins over the preset's (resolved at the start, with the profile in use then); a launcher of its own
    // is said with it: a different one runs instead of the preset's
    { icon: "agent", text: task.model || [launcher && providerTitle(launcher), task.tier && t("assistant-tasks.preset", { tier: task.tier })].filter(Boolean).join(" · ") || t("assistant-tasks.default_model"), title: (launcher && providerIdNote(launcher)) || undefined },
  ];
  return (
    <div className={cx("flex flex-col gap-1.5", n > 1 && "border-t border-line pt-3")}>
      <div className="flex items-baseline gap-2 text-card-title font-semibold">
        {numbered && <span className="flex size-[18px] shrink-0 items-center justify-center rounded-full bg-accent-tint text-[11px] font-bold text-accent">{n}</span>}
        {task.title}
      </div>
      <div className="flex flex-wrap gap-1.5">
        {chips.map((c) => (
          <span key={c.icon} title={c.title} className="inline-flex h-6 items-center gap-1 rounded-full bg-field px-2 text-label text-ink-2">
            <Icon name={c.icon} size={12} />
            {c.text}
          </span>
        ))}
        <span className={cx("inline-flex h-6 items-center rounded-full px-2 text-label", task.readonly ? "bg-accent-tint text-accent" : "bg-field text-ink-2")}>
          {task.readonly ? t("assistant-tasks.readonly") : PERMISSION[task.permission || "default"] ?? task.permission}
        </span>
        {ultra && <UltracodeChip on={ultra.on} blocked={ultra.blocked} disabled={busy} onChange={onUltra} />}
      </div>
      {/* it is asked for where it can't run: say it starts without */}
      {ultra?.blocked && ultra.on && <div className="text-label text-orange">{t("ultracode.plan_blocked", { reason: ultra.blocked })}</div>}
      {task.why && <div className="text-label text-ink-2">{task.why}</div>}
      {(() => {
        const dispatchLine = planDispatchReason(task.dispatch);
        return dispatchLine ? <div data-testid="plan-task-dispatch" className="text-label text-ink-3">{dispatchLine}</div> : null;
      })()}
      {/* what the task will be held to, shown before "Start" confirms it (#3822) */}
      {task.brief && (
        <Suspense fallback={null}>
          <PlanBrief brief={task.brief} title={task.title} />
        </Suspense>
      )}
    </div>
  );
}

// ── the hub's lines ──

// Memoized: the chat re-renders on every streamed item and session push, while an item keeps its identity until its body
// changes (mergeInto); the plan, todo and "Later" it shows come through its own selectors.
export const FeedLineView = memo(function FeedLineView({ item, simplified = false }: { item: FeedItem; simplified?: boolean }) {
  const plan = useFeed((s) => (item.kind === "plan" && item.ref ? s.plans[item.ref] : undefined));
  const isLater = useFeed((s) => !!s.later[item.id]);
  const title = useStore((s) => s.todos.find((todo) => todo.id === item.ref)?.title) ?? t("assistant-tasks.task_fallback");
  const known = useStore((s) => s.todos.some((t) => t.id === item.ref));
  const planned = useFeed((s) => item.kind === "task" && Object.values(s.plans).some((p) => p.conversation === item.conversation && p.tasks.some((task) => task.todo_id === item.ref)));
  // the computer of the Agent a message started for its task (#4754): from the hub's line, else (a hub before it said) the to-do's
  const newAgent = item.kind === "routed" ? field(item, "new_agent") : undefined;
  const newAgentMachine = useStore((s) => (newAgent ? s.todos.find((x) => x.id === item.ref)?.agents?.find((a) => a.session_id === newAgent)?.machine_id : undefined));
  // how a task goes on (its receipts, its Agent's reports, its reviews) is its row in Needs you / In progress, never a
  // card in the chat (#8237; displayLines leaves them out too)
  if (item.kind === "receipt" || item.kind === "review" || item.kind === "report") return null;
  if (simplified) {
    // the Agents a dispatch handed work to, a card each that opens its page (#5096, cindy's hand-off card); never the
    // whole In progress tray (#3700)
    // a plan not started yet is its card where it was made, Start / Edit / Dismiss included, as on iOS (FeedLine,
    // #8193); it is in the Needs you tray as well. The item's own body draws it when the store has none (a missed event).
    if (item.kind === "plan") {
      const shown = plan ?? itemPlan(item);
      if (!shown) return null;
      return shown.state === "started" ? <PlanHandoffs plan={shown} since={item.at} groupKey={item.id} /> : <PlanCard plan={shown} />;
    }
    if (item.kind === "task") return planned ? null : <TaskHandoffs item={item} />;
    if (item.kind === "message" && !cardSent(item) && !cardFiled(item)) return null;
  }
  switch (item.kind) {
    case "answered":
      return <AnsweredLine item={item} title={title} onOpenTask={known && item.ref ? () => openTask(item.ref!) : undefined} />;
    case "routed":
      // Persona's own message to the task's agent is folded, never the user's bubble (#1542)
      if (sentByPersona(item)) return <SentLine item={item} title={title} onOpenTask={known && item.ref ? () => openTask(item.ref!) : undefined} />;
      // what the user said straight to the task (its card's Message); interrupted: from a hub before #1542
      return (
        <div className="flex flex-col gap-2">
          <div className="flex flex-col items-end gap-1 pl-10">
            <span className="inline-flex items-center gap-1 text-label font-medium text-accent">
              <Icon name={item.body?.interrupted === true ? "stop" : "send"} size={11} />
              {item.body?.interrupted === true ? t("assistant-tasks.said_to_interrupting", { title }) : t("assistant-tasks.say_to", { title })}
            </span>
            <div className="rounded-[18px] bg-field px-3.5 py-2 text-card-title">{field(item, "text")}</div>
          </div>
          {/* every Agent of the task had stopped, so the message started a new one for it (#4754): its hand-off card */}
          {newAgent && <NewAgentHandoff sid={newAgent} mid={field(item, "new_agent_machine") || newAgentMachine} title={title} todo={known ? item.ref : undefined} />}
        </div>
      );
    case "today":
      return <TodayCard body={item.body as unknown as TodayBody} conversation={item.conversation} />;
    case "brief":
      return <Suspense fallback={null}><BriefCard item={item} /></Suspense>;
    case "message":
      return item.ref && !isLater ? <MessageCard item={item} /> : null;
    case "inbox_suggestion":
      return item.ref ? <Suspense fallback={null}><LazyHandoffCard item={item} /></Suspense> : null;
    case "urgent_call":
      // an urgent call (docs/assistant/urgent-call.md): its row, loaded with the first one
      return <Suspense fallback={null}><LazyUrgentCallLine item={item} /></Suspense>;
    case "notice": {
      // a line the hub wrote (#4305): small and centered, as the chat's own notices
      const text = feedNotice(item);
      return text ? (
        <p data-testid="feed-notice" className="px-6 text-center text-label text-ink-2 [overflow-wrap:anywhere]">
          {text}
        </p>
      ) : null;
    }
    case "view":
      // a live card: drawn where it was first shown, again at each version (ui/AssistantCard.tsx)
      return item.ref ? <LiveCard conversation={item.conversation} id={item.ref} first={(item.body as { view?: AssistantView } | undefined)?.view} /> : null;
    default:
      return null;
  }
});

/** Under a message to a task whose Agents had all stopped: the hand-off card of the new Agent it started (#4754, #5096).
 *  Without its computer (an older hub, a to-do not refreshed yet) the card opens the task, which leads to the same Agent. */
function NewAgentHandoff({ sid, mid, title, todo }: { sid: string; mid?: string; title: string; todo?: string }) {
  return <HandoffCard handoff={{ key: sid, title, wake: "created", mid, sid: mid ? sid : undefined, todo }} />;
}

/** A card of a task's agent the user dealt with, kept as a line "✓ task · answer". Click it for every question with its
 *  answer in full (#1231), selectable, and again to fold it; the task's name opens the task. */
export function AnsweredLine({ item, title, onOpenTask }: { item: FeedItem; title: string; onOpenTask?: () => void }) {
  const [open, setOpen] = useState(false);
  const items = answeredItems(item);
  const expandable = items.length > 0;
  return (
    <div className={cx("flex max-w-full flex-col self-start bg-field text-label text-ink-2", open ? "w-full rounded-card" : "rounded-full")}>
      <div className="flex min-w-0 items-center gap-1.5 px-3 py-1.5">
        <Icon name="check" size={12} className="shrink-0" />
        {onOpenTask ? (
          <button type="button" onClick={onOpenTask} className="max-w-[45%] shrink-0 truncate text-left hover:text-ink hover:underline">
            {title}
          </button>
        ) : (
          <span className="max-w-[45%] shrink-0 truncate">{title}</span>
        )}
        <button
          type="button"
          aria-expanded={expandable ? open : undefined}
          disabled={!expandable}
          onClick={() => setOpen(!open)}
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left enabled:cursor-pointer"
        >
          <span className="min-w-0 flex-1 truncate">· {field(item, "answer") ?? t("assistant-tasks.answered")}</span>
          {expandable && (
            <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3 transition-transform duration-200" style={{ transform: open ? "rotate(90deg)" : "none" }} />
          )}
        </button>
      </div>
      <Collapse open={open} lazy>
        <div className="flex flex-col gap-2.5 px-3 pt-0.5 pb-2.5 select-text" data-testid="answered-detail">
          {items.map((x, i) => (
            <div key={i} className="flex flex-col gap-0.5 whitespace-pre-wrap [overflow-wrap:anywhere]">
              {x.question && <div className="font-medium text-ink-3">{x.question}</div>}
              {x.answer && <div className="text-meta leading-[1.55] text-ink">{x.answer}</div>}
            </div>
          ))}
        </div>
      </Collapse>
    </div>
  );
}

export function QuickResultCard({ result }: { result: QuickResult }) {
  const when = result.when ? formatDate(result.when, { weekday: "short", hour: "2-digit", minute: "2-digit" }) : undefined;
  return (
    <div className="flex items-center gap-3 rounded-card bg-field px-3.5 py-3">
      <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-control", result.kind === "reminder" ? "bg-orange-tint text-orange" : "bg-accent-tint text-accent")}>
        <Icon name={result.kind === "reminder" ? "bell" : "clock"} size={16} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[13.5px] font-semibold">{result.kind === "reminder" ? t("assistant-tasks.reminder_added") : t("assistant-tasks.calendar_added")}</div>
        <div className="truncate text-label text-ink-2">{[when, result.title, result.place].filter(Boolean).join(" · ")}</div>
      </div>
      <span className="shrink-0 text-[11.5px] text-ink-3">{t("assistant-tasks.on_iphone")}</span>
    </div>
  );
}

// ── Today ──

/** The "Today" card's "Needs you" row's lines under its title (#4478), one per task, each truncated on its own: what its task
 *  waits on ("Has a question: …"), or for several, up to three "<title>: <what it waits on>" (the title alone when the store
 *  has no card) and "N more" past them. A card just answered here is not one it waits on (answeredLookup). */
const TODAY_WAITING_MAX = 3;
function todayWaitingLines(s: State, waiting: { id: string; title: string }[]): string[] {
  const lookup = answeredLookup(taskSessionLookup(s), s.interactions, s.answered);
  const askOf = (id: string) => {
    const todo = s.todos.find((x) => x.id === id);
    return todo ? taskAsk(taskInteraction(todo, s.interactions, mostUrgentAgent(todo, lookup)?.session)) : undefined;
  };
  if (waiting.length === 1) {
    const ask = askOf(waiting[0].id);
    return ask ? [ask] : [];
  }
  const out = waiting.slice(0, TODAY_WAITING_MAX).map((w) => {
    const ask = askOf(w.id);
    return ask ? t("assistant-tasks.dock_task_asks", { title: w.title, ask }) : w.title;
  });
  if (waiting.length > TODAY_WAITING_MAX) out.push(t("assistant-tasks.more_asks", { count: waiting.length - TODAY_WAITING_MAX }));
  return out;
}

function TodayCard({ body, conversation }: { body: TodayBody; conversation: string }) {
  const say = useSayToPersona(conversation);
  const [board, setBoard] = useState(false);
  // one string, so useStore's compare keeps the card still while nothing it shows changes
  const waitingText = useStore((s) => (body.waiting?.length ? todayWaitingLines(s, body.waiting).join("\n") : ""));
  const rows: { icon: IconName; tint: string; title: string; detail?: string; lines?: string[]; action?: string; run?: () => void }[] = [];
  if (body.done?.length)
    rows.push({ icon: "check", tint: "bg-green-tint text-green", title: body.done.length === 1 ? t("assistant-tasks.today_done_single", { title: body.done[0].title }) : t("assistant-tasks.today_done_n", { count: body.done.length }), detail: body.done.length > 1 ? body.done.map((d) => d.title).join(t("assistant-tasks.list_separator")) : undefined, action: t("assistant-tasks.look"), run: () => openTask(body.done![0].id) });
  // What did not go through overnight (#4124): "View" opens the newest, a task beside the conversation or a run's Scheduled tasks
  const failed = todayFailedRow(body);
  if (failed) {
    const first = failed.first;
    rows.push({ icon: "info", tint: "bg-red-tint text-red", title: failed.title, detail: failed.detail, action: t("assistant-tasks.go_look"), run: first.kind === "task" ? () => openTask(first.id) : () => navigate({ name: "routines" }) });
  }
  if (body.waiting?.length)
    rows.push({ icon: "hand", tint: "bg-field text-attention", title: body.waiting.length === 1 ? t("assistant-tasks.today_waiting_single", { title: body.waiting[0].title }) : t("assistant-tasks.today_waiting_n", { count: body.waiting.length }), lines: waitingText ? waitingText.split("\n") : undefined, action: t("assistant-tasks.go_look"), run: () => openTask(body.waiting![0].id) });
  if (body.pending_review?.length)
    rows.push({ icon: "hand", tint: "bg-field text-attention", title: body.pending_review.length === 1 ? t("assistant-tasks.today_pending_review_single", { title: body.pending_review[0].title }) : t("assistant-tasks.today_pending_review_n", { count: body.pending_review.length }), action: t("assistant-tasks.go_look"), run: () => openTask(body.pending_review![0].id) });
  if (body.working) rows.push({ icon: "gear", tint: "bg-orange-tint text-orange", title: t("assistant-tasks.today_working", { count: body.working }), action: t("assistant-tasks.all"), run: () => setBoard(true) });
  for (const m of (body.meetings ?? []).slice(0, 3)) {
    const at = formatDate(m.start, { hour: "2-digit", minute: "2-digit" });
    rows.push({ icon: "clock", tint: "bg-accent-tint text-accent", title: t("assistant-tasks.today_meeting", { time: at, title: m.title }), action: t("assistant-tasks.prepare"), run: () => say(t("assistant-tasks.prepare_prompt", { time: at, title: m.title })) });
  }
  if (body.at_me?.length)
    rows.push({
      icon: "at",
      tint: "bg-accent-tint text-accent",
      title: body.unread ? t("assistant-tasks.today_at_me_unread", { count: body.at_me.length, unread: body.unread }) : t("assistant-tasks.today_at_me", { count: body.at_me.length }),
      detail: body.at_me.slice(0, 2).map((c) => (c.preview ? t("assistant-tasks.chat_preview", { name: c.name, preview: c.preview }) : c.name)).join("\n"),
      action: t("assistant-tasks.review"),
      run: () => say(t("assistant-tasks.review_prompt")),
    });
  return (
    <div className="flex flex-col rounded-card bg-surface p-4 shadow-card">
      <div className="mb-2 flex items-center gap-1.5 text-label font-medium text-ink-2">
        <Icon name="sun" size={13} />
        {t("assistant-tasks.today_title")}
      </div>
      {rows.length === 0 && <div className="text-meta text-ink-2">{t("assistant-tasks.today_empty")}</div>}
      {rows.map((r, i) => (
        <div key={i} className={cx("flex items-center gap-3 py-2", i > 0 && "border-t border-line")}>
          <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-control", r.tint)}>
            <Icon name={r.icon} size={15} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="text-[13.5px] font-medium">{r.title}</div>
            {r.detail && <div className="line-clamp-2 whitespace-pre-line text-label text-ink-2">{r.detail}</div>}
            {r.lines?.map((l, j) => (
              <div key={j} data-today-line className="truncate text-label text-ink-2" title={l}>
                {l}
              </div>
            ))}
          </div>
          {r.action && r.run && <Button onClick={r.run}>{r.action}</Button>}
        </div>
      ))}
      {board && <Suspense fallback={null}><TaskBoard open conversation={conversation} onClose={() => setBoard(false)} /></Suspense>}
    </div>
  );
}

/** Says something to Persona in this conversation (the "Today" card's actions). */
function useSayToPersona(conversation: string) {
  const assistant = useStore((s) => s.assistant);
  return (text: string) => {
    const conv = assistant?.conversations.find((c) => c.session_id === conversation);
    if (!conv) return;
    window.dispatchEvent(new CustomEvent("persona:say", { detail: { mid: conv.machine_id, sid: conv.session_id, text } }));
  };
}

// ── message cards ──

const LazyMessageDraft = lazy(() => import("./MessageDraft"));
// a hand-off conversation's write-back (#5932): loaded with the first such card
const LazyHandoffCard = lazy(() => import("./HandoffCard").then((m) => ({ default: m.HandoffCard })));
const LazyUrgentCallLine = lazy(() => import("./UrgentCallLine").then((m) => ({ default: m.UrgentCallLine })));
export const draftTasks = { useTodo, useTaskNow, progressOf, TaskMark, openTask };

function MessageCard({ item }: { item: FeedItem }) {
  return <Suspense fallback={null}><LazyMessageDraft item={item} tasks={draftTasks} /></Suspense>;
}

// ── above the composer ──
// The chat re-renders on every streamed item and hands these only the conversation: they are memo and read the rest
// from the store and the feed themselves, so an item that changes nothing they show renders none of them.

/** The oldest question the conversation's tasks' agents ask, under its task's name ("N more"). */
function questionsIn(s: State, conversation: string) {
  const out: { todo: Todo; ix: Interaction }[] = [];
  const keys = new Set<string>();
  for (const t of s.todos) {
    if (t.origin !== conversation || t.done_at) continue;
    const mine = (t.agents ?? []).filter((a) => !a.released_at).map((a) => `${a.machine_id}/${a.session_id}`);
    mine.forEach((k) => keys.add(k));
    for (const ix of s.interactions) if (mine.includes(`${ix.machine_id}/${ix.session_id}`)) out.push({ todo: t, ix });
  }
  // just answered here: shown answered for a moment above the next one
  const done = Object.values(s.answered).filter((a) => keys.has(`${a.ix.machine_id}/${a.ix.session_id}`));
  return { asks: out.sort((a, b) => byTime(a.ix.created_at, b.ix.created_at)), answered: done };
}

export const TaskQuestions = memo(function TaskQuestions({ conversation }: { conversation: string }) {
  // a card for another agent coming or going leaves these rows as they were: no render
  const asks = useStoreRows((s) => questionsIn(s, conversation).asks);
  const answered = useStoreRows((s) => questionsIn(s, conversation).answered);
  const first = asks[0];
  if (!first && !answered.length) return null;
  return (
    <div className="flex flex-col gap-1">
      {answered.length > 0 && <AnsweredSummary answered={answered} />}
      {first && (
        <>
          <div className="flex items-center gap-1.5 px-1 text-label font-semibold text-attention">
            <Icon name="hand" size={13} />
            <span className="truncate">{first.todo.title}</span>
            <span className="flex-1" />
            {asks.length > 1 && <span className="shrink-0">{t("assistant-tasks.more_asks", { count: asks.length - 1 })}</span>}
          </div>
          <InteractionCard ix={first.ix} />
        </>
      )}
    </div>
  );
});

/** The dock's capsule: "All", the task pills and the Assistant's Pending / Progress / Agents entries share one height,
 *  radius, padding and type size, like iOS DockPillLabel (#2999). */
export const DOCK_CAPSULE = "inline-flex h-8 shrink-0 items-center whitespace-nowrap rounded-full px-3 text-[12.5px]";

/** The dock: "All N" held at its left edge (it does not scroll), then a pill for each task going on (waiting first) and,
 * in the main conversation, each agent going on that no task stands for (#1113), scrolling on their own (iOS TaskDock). */
// Failed, done and stopped tasks are not in the dock.
const DOCK_RANK: Partial<Record<TodoStatus, number>> = { needs_you: 0, interrupted: 0, usage_limit: 0, working: 1, waiting: 1, not_started: 1, unread: 2 };

/** The dock's task pills, waiting first: each task going on, its status and what it waits on (#3463). */
function dockTasksIn(s: State, conversation: string, plans: Record<string, TaskPlan>, manager?: string): { t: Todo; st: TodoStatus; ask?: string; limit?: string; supervisorLabel?: string }[] {
  const interactions = manager ? s.interactions.filter((ix) => sameManager(ix.manager, manager)) : s.interactions;
  const lookup = answeredLookup(taskSessionLookup(s), interactions, s.answered);
  return s.todos
    .filter((t) => t.origin === conversation && (!manager || sameManager(t.manager, manager)))
    .map((t) => ({ t, st: statusOf(t, lookup, plans) }))
    // a to-do nobody dispatched is not going on (#7552)
    .filter((x) => DOCK_RANK[x.st] !== undefined && (x.st !== "not_started" || startInFlight(x.t, plans)))
    .sort((a, b) => DOCK_RANK[a.st]! - DOCK_RANK[b.st]! || byTime(b.t.created_at, a.t.created_at))
    .map(({ t: todo, st }) => {
      const lead = mostUrgentAgent(todo, lookup)?.session;
      return {
        t: todo,
        st,
        supervisorLabel: todoSupervisorLabel(todo, st, lookup),
        ask: st === "needs_you" ? taskAsk(taskInteraction(todo, interactions, lead)) : undefined,
        limit: todo.wait != null ? waitSummary(todo.wait) : st === "usage_limit" ? (usageLimitLabel(lead) ?? t("lib-usage-limit.short")) : undefined,
      };
    });
}

export type AssistantTray = "needs" | "verify" | "running" | "parked" | "review" | "finished";
/** Each entry's mark: Needs you waits on the user, In progress is work under way; To verify (`user_check`) is calm
 *  orange apart from red Needs you; Persona reviewing is neutral, since nothing in it is the user's (#8678); Finished
 *  is the green of a finished row's check (#10922). */
const TRAY_DOT: Record<AssistantTray, string> = { needs: "bg-attention", verify: "bg-orange", running: "bg-accent", parked: "bg-ink-3", review: "bg-ink-3", finished: "bg-green" };
const EMPTY_FEED: FeedItem[] = [];
const EMPTY_CARDS: TaskCardMetadata[] = [];

/** A started plan's tasks in the conversation (#3700, #5096): a hand-off card for each Agent they started (one per task
 *  while its Agent is starting, or says why it did not start), in plan order; live to-do titles over the plan's. `since`:
 *  the plan line's time, so each card keeps the Agents this plan started (#11854). */
function PlanHandoffs({ plan, since, groupKey }: { plan: TaskPlan; since?: string; groupKey?: string }) {
  const plans = useFeed((s) => s.plans);
  const handoffs = useStoreRows((s) =>
    plan.tasks.flatMap((task, i) => {
      const id = planTaskTodo(plan, i, s.todos);
      // a later plan that continued the to-do holds it now; this plan's todo_id was cleared then (tasks_start.go)
      const superseded = Boolean(id && !task.todo_id && Object.values(plans).some((p) => p.id !== plan.id && p.tasks.some((x) => x.todo_id === id)));
      return taskHandoffs(id ? s.todos.find((x) => x.id === id) : undefined, { key: `${plan.id}/${i}`, title: task.title, error: task.error, todoId: id, superseded }, task.continues ? "continuation" : "created", plans, s.todosLoaded, since);
    }),
  );
  return <HandoffCards handoffs={handoffs} groupKey={groupKey ?? plan.id} />;
}

/** A task the conversation dispatched on its own (agent_start), or one made of an Agent already running
 *  (`body.wake` "active": agent_supervise, agent_send with a task): its Agents' hand-off cards. */
function TaskHandoffs({ item }: { item: FeedItem }) {
  const plans = useFeed((s) => s.plans);
  const wake = feedWake(field(item, "wake"));
  const handoffs = useStoreRows((s) => (item.ref ? taskHandoffs(s.todos.find((x) => x.id === item.ref), { key: item.id, title: "", todoId: item.ref }, wake, plans, s.todosLoaded, item.at, TASK_LINE_SLACK_MS) : []));
  return <HandoffCards handoffs={handoffs} groupKey={item.id} />;
}

/** One slot, one row: live task/session state wins over historical feed metadata. `fresh`: a Finished row the owner has
 *  not opened since it finished (#10922), drawn as new with Just finished's mark and word, as the To-dos page names it. */
export function ProgressTaskRow({ card, conversation, activity, countsOpen, supervisorReview, inFinished, fresh = false, onOpen }: { card: TaskCardMetadata; conversation: string; activity?: BackgroundWork; countsOpen: boolean; supervisorReview?: boolean; inFinished?: boolean; fresh?: boolean; onOpen?: () => void }) {
  const todo = useTodo(card.todo_id);
  const task = useTaskNow(todo);
  const plans = useFeed((s) => s.plans);
  const feed = useFeed((s) => s.feeds[conversation] ?? EMPTY_FEED);
  const planned = card.plan_id !== undefined && card.index !== undefined ? plans[card.plan_id]?.tasks[card.index] : undefined;
  const title = todo?.title ?? planned?.title ?? t("assistant-tasks.task_fallback");
  const accepted = !!todo && ((!!todo.done_at && todo.supervision?.state === "accepted") || acceptedAtRest(todo, task?.status ?? "not_started"));
  let state = task?.status ?? (card.state === "followup" ? "follow_up" : card.state);
  if (todo && accepted) state = "accepted";
  else if (todo?.done_at && todo.supervision?.state === "cancelled") state = "cancelled";
  else if (!todo && card.plan_id && plans[card.plan_id]?.state === "dismissed") state = "cancelled";
  const finished = state === "done" || state === "accepted";
  const clock = useNow(30_000, state === "usage_limit"); // the limit's grace running out
  const blocked = ["needs_you", "interrupted", "usage_limit", "failed"].includes(state) || (!finished && todo?.supervision?.progress?.kind === "blocked");
  const meter = taskMeter(todo, task?.lead);
  const rawReport = feed.find((it) => it.id === card.report_id && it.kind === "report");
  const report = rawReport && !(field(rawReport, "kind") === "progress" && isLegacyCodexRouteNotice(field(rawReport, "text"))) ? rawReport : undefined;
  const receipt = feed.find((it) => it.id === card.receipt_id);
  const pr = todo?.pr_url ?? (receipt && field(receipt, "pr"));
  const prNumber = pr ? /\/(\d+)\/?$/.exec(pr)?.[1] : undefined;
  const toStart = !todo && !!card.plan_id && plans[card.plan_id]?.state === "open";
  // Starting only while an Agent is being started for it; an accepted concern still waits for its report, not a start.
  const starting = !todo || (!(todo.kind === "concern" && acceptanceStands(todo)) && startInFlight(todo, plans));
  const liveWork = !finished && state !== "cancelled" ? activity : undefined;
  const liveLine = backgroundWorkLine(liveWork);
  const agentActive = !finished && state !== "cancelled" && task?.active;
  const active = liveWork || agentActive || ["working", "waiting", "unread"].includes(state);
  // parked by its supervisor (#3804): the row says so, until when and why, ahead of what its agent last did
  const parked = todo?.wait != null || active || finished || state === "cancelled" ? undefined : parkedLine(todo);
  // the status column is the Agent's own state, drawn with the Agents' marks; the to-do's own state is its chip (#6492)
  const agentKey = agentRowKey({ live: !!liveWork, status: state, lead: task?.lead, todo: !!todo, starting });
  const rowIsFinished = !countsOpen && finished;
  // a Finished row nobody has opened since it finished (#10922): Just finished's mark and word in the unread green, as the
  // To-dos page and the Agents list name it, whatever it ended as (its chip says that); once seen, the finished row's own
  const isNew = fresh && !countsOpen;
  const chip = todoChipState({ toStart, status: active ? "working" : state, done: finished, stage: todo?.stage, pr, todo });
  // judged finished and with its supervisor for review (the Persona reviewing tray, lib/assistant-entries `review`): the row says whose turn it is
  const reviewing = !!supervisorReview && !liveWork && !finished && state !== "cancelled";
  const ask = useStore((s) => taskAsk(todo ? taskInteraction(todo, s.interactions, task?.lead) : undefined));
  // what a waiting task waits for (#3274): its agent's own line for it
  const waits = useStore((s) => (todo && todo.wait == null && state === "waiting" ? waitingLine(todo, taskSessionLookup(s)) : undefined));
  // stopped and not done: the hub's completion check says why (#3180); an agent's open question or waiting line says more
  const why = ask || waits ? undefined : stoppedReason(todo, state);
  const stopped = why === undefined ? undefined : why ? t("assistant-tasks.progress_stopped_reason", { reason: why }) : t("assistant-tasks.progress_stopped");
  // nobody is working on it (#3618): said ahead of why it stopped and of its agent's last report
  const orphan = todo && !ask ? orphanedLine(todo) : undefined;
  const nobody = orphan && (orphan.reason ? `${orphan.lead} · ${orphan.reason}` : orphan.lead);
  // a usage limit says what stopped it and when it goes on (the status word on the right says only that it did)
  const limit = todo?.wait == null && state === "usage_limit" ? usageLimitLabel(task?.lead, clock) : undefined;
  const review = latestReview(todo?.supervision);
  const progress = todo?.supervision?.progress;
  const progressText = progressLine(todo?.supervision);
  const resultReady = finished || inFinished || reviewing || (!liveWork && todo && task && awaitsReview(todo, task.status));
  const explicitConclusion = resultReady
    ? (review?.verdict === "accepted" && review.note?.trim())
      || todo?.done_reason?.trim()
      || (progress?.kind === "done" && progressText)
      || (report && field(report, "kind") === "done" && field(report, "text")?.trim())
      || (receipt && field(receipt, "state") === "done" && field(receipt, "summary")?.trim())
      || undefined
    : undefined;
  const lastReply = resultReady ? plainMarkdown(task?.lead?.last_text?.trim() ?? "") || undefined : undefined;
  const progressAt = progress?.at ? Date.parse(progress.at) : NaN;
  const replyAt = task?.lead?.updated_at ? Date.parse(task.lead.updated_at) : NaN;
  const progressNewerThanReply = Boolean(progressText) && Number.isFinite(progressAt) && Number.isFinite(replyAt) && progressAt > replyAt;
  const reportBeatsReply = Boolean(progressText) && (progress?.kind === "question" || progress?.kind === "blocked" || progressNewerThanReply);
  const conclusion = explicitConclusion || (!reportBeatsReply ? lastReply : undefined);
  const finishedDetail = explicitConclusion || lastReply;
  const todoFinished = Boolean((inFinished || finished) && (!todo || todo.done_at || accepted));
  const detail = state === "cancelled" ? review?.note ?? todo?.done_reason ?? (receipt && field(receipt, "summary"))
    : todoFinished ? finishedDetail
    : conclusion ?? limit ?? liveLine ?? parked ?? waits ?? nobody ?? stopped ?? progressText ?? ask ?? task?.lead?.plan?.current ?? progressOf(task?.lead)?.text ?? (report && field(report, "text"));
  const row = (
    <button type="button" data-task-slot={card.id} data-counts-open={countsOpen ? "true" : "false"} disabled={!todo && !card.plan_id} onClick={() => {
      onOpen?.();
      if (card.todo_id) openTask(card.todo_id);
      else window.dispatchEvent(new CustomEvent("persona:task-start", { detail: conversation }));
    }} className="flex w-full min-w-0 items-center gap-3 px-3 py-2.5 text-left hover:bg-hover disabled:cursor-default">
      <span data-task-mark={isNew ? "just_finished" : rowIsFinished ? "done" : reviewing ? "follow_up" : agentKey} className="flex w-4 shrink-0 justify-center">
        {isNew ? <StatusMark state="idle" finished size="sm" /> : rowIsFinished ? <TaskMark status="done" size={8} /> : reviewing ? <TaskMark status="follow_up" size={8} /> : <StatusMark state={agentRowMark(agentKey)} size="sm" />}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-1.5">
          <span className="min-w-0 truncate text-sm font-semibold" title={title}>{title}</span>
          {chip && <span data-task-todo-state className="max-w-1/2 shrink-0 truncate rounded-full bg-field px-1.5 text-xs text-ink-2">{todoChipLabel(chip)}</span>}
        </div>
        {(todo?.wait == null || liveWork) && <Suspense fallback={null}><TaskActivityDetail activity={liveWork} detail={detail} pr={prNumber ? t("assistant-tasks.progress_pr", { number: prNumber }) : undefined} todo={todo?.wait == null && active && todo?.supervision?.parked?.waiting_on === "date" ? todo : undefined} /></Suspense>}
        {!finished && state !== "cancelled" && meter && <TaskProgressMeter value={meter.value} total={meter.total} blocked={blocked} title={title} />}
      </div>
      {/* the status in its own right-aligned column (the rows' states scan down it), then the row's end: › where it opens
          something, ✓ once finished, ⓘ for a usage limit (#5569) */}
      <span data-task-status={isNew ? "just_finished" : rowIsFinished ? "done" : reviewing ? "review" : agentKey} data-task-wait={!isNew && agentKey === "waiting" ? task?.wait ?? "unknown" : undefined} className={cx("max-w-[40%] shrink-0 truncate text-right text-xs", isNew ? "text-green" : rowIsFinished || reviewing ? "text-ink-2" : agentRowTone(agentKey, task?.wait))}>
        {isNew ? stateName("just_finished") : rowIsFinished ? (accepted ? t("assistant-tasks.review_accepted") : TODO_STATUS_INFO.done.label) : reviewing ? t("assistant-tasks.review_supervisor") : task?.supervisorLabel ?? (agentKey === "not_started" && todo ? todoLook(todo, "not_started", task?.wait).label : agentRowLabel(agentKey, task?.wait))}
      </span>
      <span className="flex w-4 shrink-0 justify-center">
        {finished ? <Icon name="check" className="text-green" /> : state === "usage_limit" ? <Icon name="info" className="text-orange" /> : <Icon name="chevronRight" className="text-ink-3" />}
      </span>
    </button>
  );
  if (todo?.wait == null) return row;
  return <div>{row}<div className="px-3 pb-2.5"><Suspense fallback={null}><TaskWaitInfo todo={todo} /></Suspense></div></div>;
}

/** An honest count meter with a readable value and a larger hover target on its task row. */
function TaskProgressMeter({ value, total, blocked, title }: { value: number; total: number; blocked: boolean | undefined; title: string }) {
  const label = t("assistant-tasks.progress_count", { value, total });
  return (
    <div className="mt-1.5 flex items-center gap-2" title={label}>
      <div role="progressbar" aria-label={title} aria-valuemin={0} aria-valuemax={total} aria-valuenow={value} aria-valuetext={label} className="task-count-meter min-w-0 flex-1 overflow-hidden rounded-full bg-field">
        <span className={cx("block h-full origin-left rounded-full", blocked ? "bg-orange" : "bg-accent")} style={{ width: `${value / total * 100}%` }} />
      </div>
      <span className="shrink-0 text-xs text-ink-2 tabular-nums">{label}</span>
    </div>
  );
}

/** The task's line in Needs you when no card of its agent stands for it: what it waits for (a waiting one's, `wait`,
 *  #11090). */
export function waitsLabel(todo: Todo, status: TodoStatus, wait?: WaitReason): string {
  if (todo.wait != null) return waitSummary(todo.wait);
  if (todo.supervision?.state === "pending_review") return t("assistant-tasks.review_pending");
  if (todo.supervision?.state === "needs_user") return t("assistant-tasks.review_needs_user");
  return TODO_STATUS_INFO[status] ? statusWord(status, wait) : t("assistant-tasks.review_needs_user");
}

/** A decision or review the task waits on, as the row has it (a decision with options, or a review only the user can settle). */
export function isDecision(todo: Todo): boolean {
  return openAsk(todo)?.outcome === "decide" || todo.supervision?.state === "needs_user";
}


/** An additional way to the task or Agent page at the end of an opened Needs you row. */
export function OpenEntry({ title, onOpen }: { title: string; onOpen: () => void }) {
  return (
    <div className="flex justify-end px-1 pt-1">
      <button type="button" data-needs-open-entry onClick={onOpen} title={t("assistant-brief.open_row", { title })} className="inline-flex min-h-8 items-center text-xs font-semibold text-accent hover:underline [@media(pointer:coarse)]:min-h-10">
        {t("assistant-brief.open")}
      </button>
    </div>
  );
}

/** One-click Pass row for a `user_check` task whose ask payload is absent (e.g. recovered or legacy ledger). */
function VerifyPassRow({ todo, rowKey, countsOpenAttr }: { todo: Todo; rowKey?: string; countsOpenAttr: { "data-counts-open"?: string } }) {
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);
  const note = todo.supervision?.reviews?.at(-1)?.note;
  const pass = () => {
    if (submitting.current) return;
    submitting.current = true;
    setBusy(true);
    updateTodo(todo.id, { done: true }).then(
      (updated) => {
        putTodo({ ...updated, ask: updated.ask });
        void refreshTodos();
        toast(t("assistant-brief.accepted"), "ok");
      },
      (e) => {
        submitting.current = false;
        setBusy(false);
        toast(t("assistant-brief.accept_failed", { error: errorText(e) }), "error");
      },
    );
  };
  return (
    <div data-needs-row={rowKey} data-verify-pass-row={todo.id} {...countsOpenAttr} className="flex flex-wrap items-center gap-2 rounded-control bg-field px-3 py-2.5 text-sm">
      {note && <div className="w-full text-sm leading-relaxed text-ink-2 [overflow-wrap:anywhere]">{note}</div>}
      <Button onClick={() => openTask(todo.id)}>{t("assistant-brief.open")}</Button>
      <Button tone="primary" busy={busy} disabled={busy} onClick={pass}>{t("assistant-tasks.verify_pass")}</Button>
    </div>
  );
}

/** The whole card of a Needs you row, or the one line it is. `nested`: inside its opened row, which carries the row's key.
 *  `calmVerify`: rendered from the calm To verify tray (#15680), so a verification with no Pass round yet offers the
 *  one-click Pass fallback instead of the urgent Needs-you tray's open-task line (#16002). */
export function NeedsYouBody({ item, nested = false, calmVerify = false, onOpenChat }: { item: NeedsYouItem; nested?: boolean; calmVerify?: boolean; onOpenChat?: () => void }) {
  const rowKey = nested ? undefined : item.key;
  if (item.kind === "plan") return <div><PlanCard plan={item.plan} /><OpenEntry title={t("assistant-tasks.needs_group_another")} onOpen={() => navigate(conversationPage(item.plan.conversation, undefined, item.plan.manager))} /></div>;
  if (item.kind === "message") return <MessageCard item={item.item} />;
  const taskOpenAttr = item.kind === "task" && !nested ? { "data-counts-open": item.countsOpen ? "true" : "false" } : {};
  if (item.asks.length) {
    const title = item.kind === "agent" ? sessionTitle(item.session) : item.todo.title;
    const open = () => item.kind === "agent" ? openAgent(item.session.machine_id, item.session.session_id) : openTask(item.todo.id);
    return <div data-needs-row={rowKey} {...taskOpenAttr}>
      {!nested && <button type="button" onClick={open} className="block max-w-full truncate px-1 text-left text-sm font-semibold hover:underline">{title}</button>}
      {item.asks.map((ix) => <InteractionCard key={ix.id} ix={ix} />)}
      {nested && <OpenEntry title={title} onOpen={open} />}
    </div>;
  }
  if (item.kind === "agent") {
    const { session } = item;
    return <button type="button" data-needs-row={rowKey} onClick={() => openAgent(session.machine_id, session.session_id)} className="flex items-center gap-2 rounded-control bg-field px-3 py-2 text-left text-sm">
      <StatusMark state="needs_you" /><span className="min-w-0 flex-1 truncate">{sessionTitle(session)}</span><span className="shrink-0 text-xs text-ink-2">{stateName("needs_you")}</span>
    </button>;
  }
  const { todo, status } = item;
  const verifying = reviewState(todo) === "user_check";
  const listedAsk = openAsk(todo);
  // #16002's open Pass round carries no outcome yet; #15680's acceptance round is the outcome:"done" ask. Both render
  // the same Accept/Pass row, while an unrelated ask must not stand in for the verification round.
  const ask = verifying
    ? (personalVerificationAsk(todo) ?? (listedAsk?.outcome === "done" ? listedAsk : undefined))
    : listedAsk;
  // The urgent Needs-you tray keeps #16002's one-line link to a verification whose Pass round has not arrived. The
  // calm To verify tray falls through to VerifyPassRow's one-click Pass instead.
  if (verifying && !ask && !calmVerify) return (
    <button type="button" data-needs-row={rowKey} {...taskOpenAttr} onClick={() => openTask(todo.id)} className="flex min-w-0 items-center gap-2 rounded-control bg-field px-3 py-2 text-left text-sm hover:bg-hover">
      <StatusMark state="needs_you" />
      <span className="min-w-0 flex-1 truncate">{todo.title}</span>
      <span className="shrink-0 text-xs text-ink-2">{t("assistant-tasks.review_user_check")}</span>
    </button>
  );
  // Both structured decisions and older review-only decisions have the same deferral controls.
  if (isDecision(todo))
    return (
      <Suspense fallback={<div data-needs-row={rowKey} {...taskOpenAttr} className="truncate rounded-control bg-field px-3 py-2.5 text-label text-ink-2">{todo.title}</div>}>
        <NeedsYouPart key={todo.id + (todo.supervision?.reviews?.at(-1)?.at || todo.decision_review?.review_at || ask?.asked_at)} rowKey={rowKey} hideTitle={nested} decision={todo} onOpenChat={onOpenChat} />
      </Suspense>
    );
  if (ask)
    return (
      <Suspense fallback={<div data-needs-row={rowKey} {...taskOpenAttr} className="truncate rounded-control bg-field px-3 py-2.5 text-label text-ink-2">{todo.title}</div>}>
        <NeedsYouPart key={todo.id + ask.asked_at} rowKey={rowKey} hideTitle={nested} accept={todo} ask={ask} countsOpenAttr={taskOpenAttr} />
      </Suspense>
    );
  if (reviewState(todo) === "user_check") return <VerifyPassRow todo={todo} rowKey={rowKey} countsOpenAttr={taskOpenAttr} />;
  if (status === "failed")
    return (
      <Suspense fallback={<div data-needs-row={item.key} {...taskOpenAttr} className="truncate rounded-control bg-danger-wash px-3 py-2 text-sm">{todo.title}</div>}>
        <NeedsYouPart rowKey={item.key} failed={todo} countsOpenAttr={taskOpenAttr} />
      </Suspense>
    );
  return (
    <Suspense fallback={<div data-needs-row={rowKey} {...taskOpenAttr} className={cx("truncate rounded-control px-3 py-2 text-sm", item.status === "failed" ? "bg-danger-wash" : "bg-field")}>{item.todo.title}</div>}>
      <NeedsYouPart task={item.todo} status={item.status} rowKey={rowKey} hideTitle={nested} countsOpenAttr={taskOpenAttr} onOpenChat={onOpenChat} />
    </Suspense>
  );
}

/** The buttons under a Needs you task row's words (Continue; Retry and Remove), lined up with them. */
export const ROW_ACTIONS = "flex items-center gap-4 pl-6 text-[12.5px]";

/** The conversation's upward trays (#3387): Needs you, everything waiting on the user, and In progress, everything
 *  running that does not; each thing in one of them only (lib/assistant-entries). What finished in the last 24 hours is
 *  under Finished (#10922), everything finished on the board (All). */
const NO_ASKS: Interaction[] = [];
const NO_DECISIONS: readonly DecisionCard[] = [];
const LazyDecisionPager = lazyPage(() => import("./DecisionPager").then((module) => module.DecisionPager));
const LazyDecisionEntry = lazyPage(() => import("./DecisionPager").then((module) => module.DecisionEntry));
const LazyAssistantTray = lazyPage(() => import("./AssistantTrayContents").then((module) => module.AssistantTrayContents));

export const AssistantActionBar = memo(function AssistantActionBar({ conversation, ownAsks = NO_ASKS, turnAsks = NO_ASKS,
  taskDecisions = NO_DECISIONS, turnItems, focusDecision, inCall = false, openDecisionSource = () => {}, sourceOpen = false }: {
  conversation: string; ownAsks?: Interaction[]; turnAsks?: readonly Interaction[]; taskDecisions?: readonly DecisionCard[]; focusDecision?: string; inCall?: boolean;
  turnItems?: readonly Item[]; openDecisionSource?: (card: DecisionCard, target?: string) => void; sourceOpen?: boolean;
}) {
  const [tray, setTrayNow] = useState<AssistantTray | null>(null);
  // the open tray filling the chat column (#4656), for going through many cards; closing or switching trays undoes it
  const [expanded, setExpanded] = useState(false);
  // the Needs you rows opened in place (#5569), by key; every row is folded again when the tray changes
  const [openRows, setOpenRows] = useState<ReadonlySet<string>>(new Set());
  const bar = useRef<HTMLDivElement>(null);
  const lastOwn = useRef<string | null>(null);
  const setTray = (next: AssistantTray | null) => {
    setTrayNow(next);
    setExpanded(false);
    setOpenRows(new Set());
    if (next) entryButtons.current[next]?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  };
  const toggleRow = (key: string) => setOpenRows((rows) => {
    const next = new Set(rows);
    if (!next.delete(key)) next.add(key);
    return next;
  });
  const [board, setBoard] = useState(false);
  const [showShelved, setShowShelved] = useState(false);
  const trayId = useId();
  const decisionId = useId();
  const cards = useMemo(() => decisionCards(turnAsks, taskDecisions), [turnAsks, taskDecisions]);
  const pager = useDecisionPager(cards);
  const focusedDecision = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!focusDecision) focusedDecision.current = undefined;
    else if (focusedDecision.current !== focusDecision && pager.reveal(focusDecision)) {
      focusedDecision.current = focusDecision;
      setTrayNow(null);
    }
  }, [focusDecision, pager]);
  const showPager = !sourceOpen && pager.count > 0 && (inCall || !tray && !pager.collapsed);
  const decisionPanel = showPager && (
    <Suspense fallback={<div className="flex h-11 shrink-0 items-center justify-center"><Loading /></div>}>
      <LazyDecisionPager pager={pager} id={decisionId} items={turnItems} openSource={(card, target) => {
        if (card.taskTitle === undefined && !inCall) pager.collapse();
        openDecisionSource(card, target);
      }} inCall={inCall} />
    </Suspense>
  );
  const manager = useShownManager();
  const plans = useFeed((s) => s.plans);
  const feed = useFeed((s) => s.feeds[conversation] ?? EMPTY_FEED);
  const metadata = useFeed((s) => s.taskCards[conversation] ?? EMPTY_CARDS);
  const deferred = useFeed((s) => s.later);
  const todos = useStore((s) => s.todos);
  const sessionRows = useStoreRows<Session>((s) => s.features.includes("sessions_list") ? s.sessionSummaries : Object.values(s.sessions).flat());
  const interactions = useStore((s) => s.interactions);
  const answered = useStore((s) => s.answered);
  const main = useStore((s) => s.assistant?.main);
  const lookup = useTaskLookup();
  const [restored] = useRestoredTodos();
  // a minute's tick moves tasks across the 48 h shelving line while nothing else changes, as on the To-dos page
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const id = window.setInterval(() => setTick((n) => n + 1), 60_000);
    return () => window.clearInterval(id);
  }, []);
  const entries = useMemo(
    () => assistantEntries({ conversation, main, manager, todos, sessions: answeredSessions(sessionRows, interactions, answered), interactions, plans, feed, cards: metadata, deferred, lookup, restored, now: Date.now() }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- tick: re-read the clock once a minute
    [conversation, main, manager, todos, sessionRows, interactions, answered, plans, feed, metadata, deferred, lookup, restored, tick],
  );
  const directWaiting = main === conversation;
  const waiting = useMemo(() => directWaiting ? directUserWaiting(entries.needsYou, deferred) : entries.needsYou, [directWaiting, entries.needsYou, deferred]);
  const countedOwnAsks = useMemo(() => directWaiting ? ownAsks.filter(directUserInteraction) : ownAsks, [directWaiting, ownAsks]);
  const stackedAsks = useMemo(() => {
    const paged = new Set(cards.map(({ interaction }) => decisionKey(interaction)));
    return countedOwnAsks.filter((ix) => !paged.has(decisionKey(ix)));
  }, [cards, countedOwnAsks]);
  // The badge and the rows it opens share directUserWaiting: current direct questions, owner-only decisions,
  // explicit acceptance, and escalated or hub-projected task cards. Unstarted/failed work, unescalated Agent
  // permissions/terminal dialogs, delegating cards and generic retries stay out.
  // Other trays keep the existing To-dos open-work rule (#3331).
  const badgeOf = (items: { kind: string; countsOpen?: boolean }[]) =>
    items.reduce((n, item) => n + (item.kind === "task" ? (item.countsOpen ? 1 : 0) : 1), 0);
  // Finished counts its rows (#10922): finished work, never open work, so no To-dos count to agree with
  const counts: Record<AssistantTray, number> = { needs: directWaiting ? directWaitingCount(waiting, ownAsks) : countedOwnAsks.length + badgeOf(waiting), verify: entries.verify.length, running: badgeOf(entries.running), parked: badgeOf(entries.parked), review: badgeOf(entries.review), finished: entries.finished.length };
  // the card is in the chat already: close the tray onto it and bring it into view. A folded stack shows only its first
  // card, so use its existing expander before looking for another one (#10347).
  const scrollOwn = (id: string, keepBar = false) => {
    const scope = bar.current?.closest("[data-chat-dock]") ?? document;
    const card = [...scope.querySelectorAll<HTMLElement>("[data-card]")].find((el) => el.dataset.card === id);
    if (!card) return false;
    card.firstElementChild?.scrollIntoView?.({ block: "nearest" });
    // Revealing another card can scroll the capsule row out of the dock's own scroll box; keep Next clickable for the
    // following card and the wrap to the first (#10347). A Needs you row keeps its existing landing.
    if (keepBar) bar.current?.scrollIntoView?.({ block: "nearest" });
    return true;
  };
  const openOwn = (id: string, keepBar = false) => {
    setTray(null);
    if (pager.reveal(id)) return;
    requestAnimationFrame(() => {
      if (scrollOwn(id, keepBar)) return;
      const dock = bar.current?.closest("[data-chat-dock]");
      // On a narrow screen a keyboard or short height first folds the whole stack to one row; that row is the first
      // expander, then the position chip opens the other cards. Desktop starts at the position-chip step.
      dock?.querySelector<HTMLButtonElement>("[data-chat-cards-position][aria-expanded='false'], [data-chat-cards-fold]")?.click();
      requestAnimationFrame(() => {
        if (scrollOwn(id, keepBar)) return;
        const queue = dock?.querySelector<HTMLButtonElement>("[data-chat-cards-position][aria-expanded='false']");
        if (!queue) return;
        queue.click();
        requestAnimationFrame(() => scrollOwn(id, keepBar));
      });
    });
  };
  const openNextOwn = () => {
    if (!stackedAsks.length) return;
    const previous = stackedAsks.findIndex((ix) => ix.id === lastOwn.current);
    const next = stackedAsks[(previous + 1) % stackedAsks.length];
    lastOwn.current = next.id;
    openOwn(next.id, true);
  };
  useEffect(() => {
    const openRequested = () => { if (takeDirectWaitingRequest(conversation)) setTray("needs"); };
    openRequested();
    const needs = (event: Event) => {
      if ((event as CustomEvent).detail !== conversation) return;
      takeDirectWaitingRequest(conversation);
      setTray("needs");
    };
    const followUp = (event: Event) => {
      if ((event as CustomEvent).detail === conversation) setTray(null);
    };
    window.addEventListener("persona:verification-follow-up", followUp);
    window.addEventListener("persona:task-pending", needs);
    window.addEventListener("persona:task-start", needs);
    return () => {
      window.removeEventListener("persona:verification-follow-up", followUp);
      window.removeEventListener("persona:task-pending", needs);
      window.removeEventListener("persona:task-start", needs);
    };
  }, [conversation]);
  const labels: Record<AssistantTray, string> = { needs: t("assistant-tasks.tray_needs_you"), verify: t("assistant-tasks.tray_verify"), running: t("assistant-tasks.tray_running"), parked: t("todos.section_parked"), review: t("assistant-tasks.tray_review"), finished: t("assistant-tasks.tray_finished") };
  // Parked is a peer, not folded inside In progress; keep an emptied tray reachable until it closes. To verify
  // (`user_check`) sits beside In progress as a calm non-attention entry while present. Finished comes last, as what
  // just finished comes after the unfinished work on the To-dos page (#10922).
  const entryKeys: AssistantTray[] = ["needs", "running", ...(entries.verify.length > 0 || tray === "verify" ? ["verify" as const] : []), ...(entries.parked.length > 0 || tray === "parked" ? ["parked" as const] : []), ...(entries.review.length > 0 || tray === "review" ? ["review" as const] : []), ...(entries.finished.length > 0 || tray === "finished" ? ["finished" as const] : [])];
  const entryButtons = useRef<Partial<Record<AssistantTray, HTMLButtonElement | null>>>({});
  // Esc closes the open tray from its entry, its header and its rows, and hands the focus back to the entry; it stops
  // there, never on to the window, where the chat's Esc would stop a working turn. Not while a menu or dialog is open
  // over it (theirs), and not from a field being typed in or a waiting card in the tray: a card's Esc goes back to the
  // composer (lib/card-keys.ts) and closing the tray under it would throw away an answer being written.
  const onTrayKey = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "Escape" || !tray || e.defaultPrevented || e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229 || !e.currentTarget.contains(e.target as Node)) return;
    if ([...document.querySelectorAll("[role=dialog],[role=menu],[role=alertdialog]")].some((el) => !el.closest("[data-closing],[inert]"))) return;
    const target = e.target instanceof HTMLElement ? e.target : null;
    if (target && (target.isContentEditable || target.closest(`${EDITABLE}, [data-card-keys]`))) return;
    e.preventDefault();
    e.stopPropagation();
    const entry = entryButtons.current[tray];
    setTray(null);
    entry?.focus();
  };
  if (inCall) return decisionPanel;
  return (
    // shrinks with the dock (ChatView's DOCK_MAX): an open tray gives up its height and scrolls inside, instead of
    // standing taller than the dock and leaving its bottom (a card's answer row) and the entries under the composer;
    // closed it is the capsules alone, which never give way to the cards over them (#10347, #11794)
    <div ref={bar} className={cx("flex min-h-0 flex-col gap-2", !tray && !showPager ? "shrink-0" : "shrink")} data-assistant-actionbar onKeyDownCapture={onTrayKey}>
      {decisionPanel}
      {tray && (
        <Suspense fallback={<Loading />}>
          <LazyAssistantTray tray={tray} id={trayId} label={labels[tray]} expanded={expanded}
            toggleExpanded={() => setExpanded((value) => !value)} openBoard={() => setBoard(true)} close={() => setTray(null)}
            conversation={conversation} entries={entries} waiting={waiting} directWaiting={directWaiting}
            needsCount={counts.needs} ownAsks={countedOwnAsks} openRows={openRows} toggleRow={toggleRow}
            openOwn={openOwn} showShelved={showShelved} toggleShelved={() => setShowShelved((value) => !value)} />
        </Suspense>
      )}
      {/* The entries are the dock's old "All" / task capsules (DOCK_CAPSULE), content-wide at the leading edge; the
          open tray is the selected capsule. The scroller pads by the focus ring's width and takes it back, as the
          dock's did, so a large font scrolls sideways instead of clipping the ring. */}
      <div className="scrollbar-none -my-1 flex min-w-0 shrink-0 items-center gap-2 overflow-x-auto py-1">
        {pager.count > 0 && <Suspense fallback={null}>
          <LazyDecisionEntry pager={pager} expanded={showPager} controls={decisionId} capsule={DOCK_CAPSULE} onToggle={() => {
            if (showPager) pager.collapse();
            else { setTray(null); pager.open(); }
          }} />
        </Suspense>}
        {stackedAsks.length > 0 && (
          <button type="button" data-next-own-ask onClick={openNextOwn} className={cx(DOCK_CAPSULE, "gap-2 bg-field font-medium text-ink hover:bg-hover")}>
            <span className="whitespace-nowrap">{t("assistant-tasks.next_unanswered")}</span>
            <span className="tabular-nums text-ink-2">{stackedAsks.length}</span>
          </button>
        )}
        {entryKeys.map((key) => {
          const open = tray === key;
          // An empty entry is drawn quiet (grey), Needs you and In progress alike, and still opens its "nothing here" line.
          const quiet = !counts[key];
          return (
            <button
              key={key}
              ref={(el) => void (entryButtons.current[key] = el)}
              type="button"
              data-tray-entry={key}
              aria-expanded={open}
              aria-controls={open ? trayId : undefined}
              onClick={() => setTray(open ? null : key)}
              className={cx(DOCK_CAPSULE, "gap-2 font-medium", open ? "bg-ink text-surface" : quiet ? "bg-field text-ink-3 hover:bg-hover" : key === "verify" || key === "review" || key === "parked" || key === "finished" ? "bg-field text-ink-2 hover:bg-hover" : "bg-field text-ink hover:bg-hover")}
            >
              <span aria-hidden className={cx("size-2 shrink-0 rounded-full", quiet ? "bg-ink-3" : TRAY_DOT[key])} />
              <span>{labels[key]}</span>
              {counts[key] > 0 && <span className={cx("tabular-nums", !open && "text-ink-2")}>{counts[key]}</span>}
            </button>
          );
        })}
      </div>
      {board && <Suspense fallback={null}><TaskBoard open conversation={conversation} onClose={() => setBoard(false)} /></Suspense>}
    </div>
  );
});

export const TaskDock = memo(function TaskDock({ conversation }: { conversation: string }) {
  const manager = useShownManager();
  const plans = useFeed((s) => s.plans);
  const [menuFor, setMenuFor] = useState<{ todo: Todo; el: HTMLElement } | null>(null);
  const [board, setBoard] = useState(false);
  // rows of strings and the store's own objects: a push about an agent or a card none of them shows draws nothing
  const agents = useStoreRows<DockAgent>((s) => dockAgents(
    answeredSessions(
      Object.values(s.sessions).flat().filter((row) => sameManager(row.manager, manager)),
      s.interactions.filter((ix) => sameManager(ix.manager, manager)),
      s.answered,
    ),
    s.todos.filter((t) => sameManager(t.manager, manager)),
    conversation,
    s.assistant?.main,
  ));
  const going = useStoreRows((s) => dockTasksIn(s, conversation, plans, manager));
  const mine = useStore((s) => s.todos.filter((t) => t.origin === conversation && sameManager(t.manager, manager)).length);
  if (mine === 0 && agents.length === 0) return null;
  const capsule = DOCK_CAPSULE;
  const pill = cx(capsule, "max-w-[240px] gap-2 font-semibold");
  return (
    // Top-aligned, so a scrollbar an engine still draws under the pills cannot push "All" down from them (#1381).
    <div className="flex min-w-0 items-start gap-2" data-testid="task-dock">
      <button
        type="button"
        data-testid="task-dock-board"
        aria-haspopup="dialog"
        onClick={() => setBoard(true)}
        className={cx(capsule, "bg-field font-medium text-ink-2 hover:bg-hover")}
      >
        {t("assistant-tasks.dock_all", { count: mine + agents.length })}
      </button>
      {(going.length > 0 || agents.length > 0) && (
        // The scroller pads by the focus ring's width and takes it back, so the ring is not clipped and the row
        // stays a pill high.
        <div className="scrollbar-none -my-1 flex min-w-0 flex-1 items-center gap-2 overflow-x-auto py-1">
          {going.map(({ t: todo, st, ask, limit, supervisorLabel }) => {
            // a waiting task says what it waits on when hovered or read out (#3463)
            const label = ask ? t("assistant-tasks.dock_task_asks", { title: todo.title, ask }) : limit ? `${todo.title} · ${limit}` : undefined;
            return (
            <button
              key={todo.id}
              type="button"
              title={label}
              aria-label={label}
              onClick={() => openTask(todo.id)}
              onContextMenu={(e) => {
                if (!isRemovable(st)) return;
                e.preventDefault();
                setMenuFor({ todo, el: e.currentTarget });
              }}
              className={cx(pill, st === "needs_you" || st === "interrupted" ? "bg-field text-attention" : "bg-field text-ink hover:bg-hover")}
            >
              <TaskMark status={st} size={8} />
              <span className="truncate">{todo.title}</span>
              {st === "needs_you" && <span className="shrink-0">{stateName("needs_you")}</span>}
              {supervisorLabel && <span className="shrink-0">{supervisorLabel}</span>}
              {st === "interrupted" && <span className="shrink-0">{stateName("interrupted")}</span>}
              {st === "usage_limit" && <span className="shrink-0">{t("lib-usage-limit.short")}</span>}
            </button>
            );
          })}
          {agents.map(({ session: s, state }) => (
            <button
              key={`${s.machine_id}/${s.session_id}`}
              type="button"
              aria-label={t("assistant-tasks.dock_agent", { title: sessionTitle(s), state: stateName(state === "needs_you" ? "needs_you" : "working") })}
              onClick={() => openAgent(s.machine_id, s.session_id)}
              className={cx(pill, state === "needs_you" ? "bg-field text-attention" : "bg-field text-ink hover:bg-hover")}
            >
              <StatusMark state={state} />
              <Icon name="agent" size={13} className={cx("shrink-0", state !== "needs_you" && "text-ink-2")} />
              <span className="truncate">{sessionTitle(s)}</span>
              {state === "needs_you" && <span className="shrink-0">{stateName("needs_you")}</span>}
            </button>
          ))}
        </div>
      )}
      {menuFor && (
        <Menu
          anchor={menuFor.el}
          side="top"
          onClose={() => setMenuFor(null)}
          items={[{ key: "remove", label: t("assistant-tasks.remove_ellipsis"), icon: "trash", danger: true, onSelect: () => removeTaskNow(menuFor.todo) }]}
        />
      )}
      {board && <Suspense fallback={null}><TaskBoard open conversation={conversation} onClose={() => setBoard(false)} /></Suspense>}
    </div>
  );
});

/** "Talking to "xx"" while the next message goes straight to a task; × gives it back to Persona. */
export const RouteChip = memo(function RouteChip({ conversation }: { conversation: string }) {
  const route = useFeed((s) => s.routes[conversation]);
  const todoId = routeTodoId(route);
  const todo = useTodo(todoId);
  if (!todo) return null;
  return (
    <div className="flex flex-col items-start gap-1">
      <span title={typeof route === "object" ? t("todos.ask_chat_hint") : undefined} className="inline-flex h-7 max-w-full items-center gap-1.5 rounded-full bg-accent-tint pl-3 pr-1 text-[12.5px] font-semibold text-accent">
        <Icon name="send" size={12} />
        <span className="min-w-0 truncate">{t(typeof route === "object" ? "todos.ask_chat_route" : "assistant-tasks.say_to", { title: todo.title })}</span>
        <button type="button" aria-label={t("assistant-tasks.give_back")} onClick={() => setRoute(conversation, null)} className="flex size-5 items-center justify-center rounded-full hover:bg-hover">
          <Icon name="x" size={11} />
        </button>
      </span>
      {typeof route === "object" && <span className="text-xs text-ink-2">{t("todos.ask_chat_hint")}</span>}
    </div>
  );
});

export function DayLine({ date }: { date: Date }) {
  return <div className="py-1 text-center text-label text-ink-3">{dayLabel(date)}</div>;
}

/** A task's state for other views (the side panel's header). */
export function useTaskState(todo?: Todo): (TaskNow & { failure?: string }) | undefined {
  const now = useTaskNow(todo);
  const plans = useFeed((s) => s.plans);
  if (!todo || !now) return undefined;
  return { ...now, failure: now.status === "failed" ? startFailure(todo, plans) : undefined };
}

export { TaskMark, place };
