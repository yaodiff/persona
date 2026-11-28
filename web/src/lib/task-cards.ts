import type { FeedItem, TaskPlan } from "./feed";
import { todoBucket, type TodoBucket } from "./todo-triage";
import { reviewState } from "./supervision";
import { taskReviewDecisionAsk, type SessionLookup, type Todo } from "./todos";
import type { Session } from "./types";

/** Feed upserts identify a plan slot before and after a to-do is attached to it. */
export interface TaskCardMetadata {
  id: string;
  todo_id?: string;
  plan_id?: string;
  index?: number;
  at: string;
  updated_at: string;
  state: string;
  receipt_id?: string;
  report_id?: string;
}

/** Missing/empty pages are not tombstones. Older replay pages cannot roll a slot back. */
export function upsertTaskCards(previous: TaskCardMetadata[], incoming?: TaskCardMetadata[] | null): TaskCardMetadata[] {
  if (!incoming?.length) return previous;
  const cards = new Map(previous.map((card) => [card.id, card]));
  for (const card of incoming) {
    const old = cards.get(card.id);
    if (!old || Date.parse(card.updated_at) >= Date.parse(old.updated_at)) {
      cards.set(card.id, { ...old, ...card, at: old?.at ?? card.at });
    }
  }
  return [...cards.values()].sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || a.id.localeCompare(b.id));
}

/** Whether a task belongs in this conversation's tray: its origin, or a review this conversation supervises. */
export function taskInConversation(todo: Todo, conversation: string, everyTodo = false): boolean {
  const supervisedReview = todo.supervision?.supervisor === conversation && (reviewState(todo) === "user_check" || Boolean(taskReviewDecisionAsk(todo)));
  return everyTodo || todo.origin === conversation || supervisedReview;
}

/** Legacy hubs keep their audit events. Fold those events into the same plan-slot identities. `everyTodo`: the main
 *  conversation watches every task (docs/assistant/unified-entry.md), so each to-do gets a row there, whatever its origin,
 *  and its In progress entry counts what the To-dos page counts (#3331). */
export function taskCardSlots(conversation: string, metadata: TaskCardMetadata[], feed: FeedItem[], plans: Record<string, TaskPlan>, todos: Todo[], everyTodo = false): TaskCardMetadata[] {
  const slots = new Map<string, TaskCardMetadata>();
  const linked = new Map<string, string>();
  const authoritative = new Set(metadata.map((card) => card.id));
  const byTodo = new Map(todos.map((todo) => [todo.id, todo]));
  const prefer = (candidate: TaskCardMetadata, previous: TaskCardMetadata) => {
    const currentPlan = candidate.todo_id ? byTodo.get(candidate.todo_id)?.plan_id : undefined;
    if (currentPlan) {
      const candidateCurrent = candidate.plan_id === currentPlan;
      const previousCurrent = previous.plan_id === currentPlan;
      if (candidateCurrent !== previousCurrent) return candidateCurrent;
    }
    return Date.parse(candidate.updated_at) > Date.parse(previous.updated_at);
  };
  const bind = (todoId: string, id: string) => {
    const previous = linked.get(todoId);
    if (!previous || prefer(slots.get(id)!, slots.get(previous)!)) linked.set(todoId, id);
  };
  for (const plan of Object.values(plans)) {
    if (plan.conversation !== conversation) continue;
    plan.tasks.forEach((task, index) => {
      const id = `plan:${plan.id}:${index}`;
      const at = plan.created_at ?? feed.find((it) => it.kind === "plan" && it.ref === plan.id)?.at ?? "";
      slots.set(id, { id, plan_id: plan.id, index, todo_id: task.todo_id, at, updated_at: at, state: plan.state === "dismissed" ? "cancelled" : "not_started" });
      if (task.todo_id) bind(task.todo_id, id);
    });
  }
  for (const card of metadata) {
    slots.set(card.id, { ...slots.get(card.id), ...card });
    if (card.todo_id) bind(card.todo_id, card.id);
  }
  for (const item of feed) {
    if (!["task", "receipt", "review", "report"].includes(item.kind) || !item.ref) continue;
    const id = linked.get(item.ref) ?? `task:${item.ref}`;
    const old = slots.get(id);
    const todo = todos.find((todo) => todo.id === item.ref);
    const at = old?.at || todo?.created_at || item.at;
    const bodyState = typeof item.body?.state === "string" ? item.body.state : undefined;
    slots.set(id, { id, todo_id: item.ref, plan_id: todo?.plan_id, state: "not_started", updated_at: item.at, ...old, at,
      ...(old && (Date.parse(old.updated_at) > Date.parse(item.at) || (authoritative.has(id) && old.updated_at === item.at)) ? {} : {
        updated_at: item.at,
        state: item.kind === "review" && item.body?.verdict === "accepted" ? "accepted" : bodyState ?? old?.state ?? "not_started",
        ...(item.kind === "receipt" ? { receipt_id: item.id } : {}),
        ...(item.kind === "report" ? { report_id: item.id } : {}),
      }),
    });
  }
  for (const todo of todos) {
    if (!taskInConversation(todo, conversation, everyTodo) || linked.has(todo.id)) continue;
    const id = `task:${todo.id}`;
    if (!slots.has(id)) slots.set(id, { id, todo_id: todo.id, plan_id: todo.plan_id, at: todo.created_at, updated_at: todo.updated_at, state: "not_started" });
  }
  // A slot learned later supersedes the earlier task-only fallback, never adds another card.
  for (const [todoId, id] of linked) if (id !== `task:${todoId}`) slots.delete(`task:${todoId}`);
  const selected = new Map<string, TaskCardMetadata>();
  for (const card of slots.values()) {
    if (!card.todo_id) continue;
    const previous = selected.get(card.todo_id);
    if (!previous || prefer(card, previous)) selected.set(card.todo_id, card);
  }
  // Reused to-dos can remain in old plan payloads and cached upserts. Their current plan owns the row.
  return [...slots.values()]
    .filter((card) => !card.todo_id || selected.get(card.todo_id)?.id === card.id)
    .sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || a.id.localeCompare(b.id));
}

/** Only real reported counts drive a meter; no stage-to-percentage approximation. */
export function taskMeter(todo?: Todo, lead?: Session): { value: number; total: number } | undefined {
  const progress = todo?.supervision?.progress;
  const counts = progress?.steps ? { value: progress.step, total: progress.steps } : lead?.plan ? { value: lead.plan.done, total: lead.plan.total } : undefined;
  if (!counts || typeof counts.value !== "number" || !Number.isFinite(counts.value) || !Number.isFinite(counts.total) || counts.total <= 0 || counts.value < 0 || counts.value > counts.total) return undefined;
  return { value: counts.value, total: counts.total };
}

/** Where an In progress row stands, by the To-dos page's rule (lib/todo-triage `todoBucket`, #3331): a task is open,
 *  shelved (folded, not counted) or closed (a task the hub no longer sends counts as closed; a failed start whose work
 *  finished in a related to-do, `superseded`, #3713). A plan task with no to-do
 *  yet is `start` while its plan waits for the user's Start: its plan is one Needs you row (#3387), so In progress has
 *  none of it. */
export function cardBucket(card: TaskCardMetadata, todo: Todo | undefined, plans: Record<string, TaskPlan>, lookup: SessionLookup | undefined, now: number, restoredAt = 0, superseded = false): TodoBucket | "start" {
  if (card.todo_id) return todo ? todoBucket(todo, lookup, now, restoredAt, superseded) : "closed";
  return card.plan_id && plans[card.plan_id]?.state === "open" ? "start" : "closed";
}
