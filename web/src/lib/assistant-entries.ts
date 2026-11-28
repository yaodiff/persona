import { backgroundWork, taskBackgroundWork, type BackgroundWork } from "./background-work";
import { cardDismissed, cardFiled, cardSent, startFailure, type FeedItem, type TaskPlan } from "./feed";
import { cardBucket, taskCardSlots, type TaskCardMetadata } from "./task-cards";
import { decisionDismissed, linkedParkInteraction, reviewState, withoutParkInteraction } from "./supervision";
import { finishedAt, hubNeedsYou, JUST_DONE_MS, openByView, supersededFailedStarts, triage } from "./todo-triage";
import { awaitsReview, dockAgents, heldDelivery, heldForSupervisor, makeSessionLookup, openAsk, taskReviewDecisionAsk, todoRank, todoStatus, withInteractionLookup, type SessionLookup, type Todo, type TodoStatus } from "./todos";
import { byTime } from "./times";
import { homeKey } from "./folded-rows";
import { sameManager } from "./manager-choice";
import type { Interaction, Session } from "./types";

/**
 * The Assistant's entries above the composer (docs/assistant/unified-entry.md "Needs you and In progress"):
 * Needs you lists the user's turn, In progress the active work, and Parked the external waits, including ready PRs.
 * Each thing has one home and one count. Finished things are not open work: the handed-out ones that finished in the
 * last 24 hours are under Finished (`finished`, #10922), everything else on the board. A
 * task waiting for its supervisor's review is in neither: the web lists it under Persona reviewing (`review`, #8678),
 * however long it has waited. A task is one row whatever its agents: they are not listed on their own. In the main
 * conversation an Agent that belongs to no task (opened by hand) is a row of its own.
 */
type NeedsYouTask = { kind: "task"; key: string; todo: Todo; status: TodoStatus; asks: Interaction[]; countsOpen: boolean; steps?: NeedsYouTask[] };

type RankedTask = { item: NeedsYouTask; rank: number; at: string };

export type NeedsYouItem =
  /** A task whose agent asks something (`asks`, oldest first), or that waits for the user's decision.
   *  `countsOpen` says whether the To-dos page's "Not done" counts the to-do (#3331): the row stays visible whatever
   *  it says, the chip badge counts it only when it does. */
  | NeedsYouTask
  /** An Agent that belongs to no task, asking something. */
  | { kind: "agent"; key: string; session: Session; asks: Interaction[] }
  /** A plan waiting for Start. */
  | { kind: "plan"; key: string; plan: TaskPlan }
  /** A message card waiting for Send. */
  | { kind: "message"; key: string; item: FeedItem };

export type RunningItem =
  /** A dispatched task not finished and not waiting on the user. `countsOpen`: see NeedsYouItem. */
  | { kind: "task"; key: string; card: TaskCardMetadata; todo: Todo; status: TodoStatus; activity?: BackgroundWork; countsOpen: boolean }
  /** A working Agent that belongs to no task. */
  | { kind: "agent"; key: string; session: Session; activity?: BackgroundWork };

/** A task the conversation handed out that finished in the last 24 hours (Finished, #10922). `fresh`: the owner has not
 *  opened it since it finished (the To-dos page's Just finished), so it is drawn as new; seeing it changes only that.
 *  `at`: when it finished (ms, `finishedAt`). */
export interface FinishedItem { kind: "task"; key: string; card: TaskCardMetadata; todo: Todo; status: TodoStatus; fresh: boolean; at: number }

export interface AssistantEntries {
  needsYou: NeedsYouItem[];
  /** Completed tasks accepted by their supervisor awaiting optional owner verification (`user_check`): visible with
   *  one-click Pass/Accept in a calm secondary tray, without inflating the urgent red Needs you count. */
  verify: Extract<NeedsYouItem, { kind: "task" }>[];
  running: RunningItem[];
  /** Open tasks with no progress for 48 hours and nothing running (the To-dos page's shelved, #3331): folded under
   *  In progress, not counted. */
  shelved: RunningItem[];
  /** Explicit holds and fresh ready PR waits, outside In progress; still open tasks. */
  parked: RunningItem[];
  /** Tasks waiting for their supervisor's review (`pending_review`) with nothing of them running or asking, however long
   *  the review has waited (web only, #8678): Persona's to do, not the owner's, so outside Needs you; still open tasks,
   *  counted like the other entries. An asking agent, an open `ask` or `needs_user` makes such a task Needs you instead. */
  review: RunningItem[];
  /** Handed-out tasks that finished in the last 24 hours (JUST_DONE_MS, the To-dos page's Just finished window), seen or
   *  not, newest finished first: the Finished entry (#10922). How a task ended is told here, not by a card in the chat
   *  (#8237). Not open work: its count is its rows, and opening one marks it seen without taking it out. Until #10922
   *  only the unseen ones were listed, as a group under In progress that went as soon as one was opened. */
  finished: FinishedItem[];
}

export interface AssistantEntriesInput {
  conversation: string;
  /** The conversation's machine, when its row is not in `sessions` (the main chat's, say). */
  conversationMachine?: string;
  /** The main conversation's id: it watches every task and the Agents opened by hand. */
  main?: string;
  manager?: string;
  todos: Todo[];
  /** Every session, as the lists show them (answered cards applied). */
  sessions: Session[];
  /** Pending cards. */
  interactions: Interaction[];
  plans: Record<string, TaskPlan>;
  /** The conversation's feed. */
  feed: FeedItem[];
  /** The hub's task card slots for the conversation (`task_cards`). */
  cards?: TaskCardMetadata[];
  /** Message cards and task-decision rounds the user put off locally (`later`). */
  deferred?: Record<string, unknown>;
  /** Looks up a task's agents; defaults to `sessions`. */
  lookup?: SessionLookup;
  now?: number;
  /** Tasks the user brought back from shelved, by id: when (ms). */
  restored?: ReadonlyMap<string, number>;
}

/** Task states that wait on the user: an agent asks, or its turn broke off on an error (a failed start is below). */
const WAITS_ON_YOU: ReadonlySet<TodoStatus> = new Set(["needs_you", "interrupted"]);

const sessionKey = (mid: string, sid: string) => `${mid}/${sid}`;

/** A task's status when some of its Agents ask under another task (`own`: its status from its own Agents alone): it
 *  does not wait on the user for them, so a Needs you that only those bring is the task at work instead. */
function withNeeds(all: TodoStatus, own: TodoStatus): TodoStatus {
  return all === "needs_you" && own !== "needs_you" ? (own === "not_started" ? "working" : own) : all;
}

/** A task's status, failed too when only its plan says the start failed (a hub from before #1077). */
export function entryStatus(todo: Todo, lookup: SessionLookup, plans: Record<string, TaskPlan>): TodoStatus {
  const st = todoStatus(todo, lookup);
  return st === "not_started" && startFailure(todo, plans) ? "failed" : st;
}

/** The task waits for the user's decision: an open task's ask, or its supervisor's `needs_user` on an open task. A
 *  finished task never does, whatever ask it still carries: the hub's To-dos triage files it with the finished ones
 *  and the tray has nothing to answer, so it would keep the badge up for good (#12974). A `pending_review` is its
 *  supervisor's to do, however long it has waited (#8678). */
export function taskAwaitsDecision(todo: Todo): boolean {
  if (todo.done_at) return false;
  const ask = openAsk(todo);
  const review = reviewState(todo);
  if (review === "user_check") {
    return Boolean(ask && (ask.questions?.length || ask.outcome === "decide" || ask.outcome === "cannot"));
  }
  if (ask) return true;
  return review === "needs_user";
}

/** The task waits for its supervisor's review and nothing of it runs: an open `pending_review` whose status is a
 *  finished turn (`awaitsReview`), or an owner hold its Agent reported that its supervisor has not read yet
 *  (`heldForSupervisor`). An agent at work again keeps it In progress. */
function underReview(todo: Todo, status: TodoStatus): boolean {
  if (heldForSupervisor(todo) && (status === "follow_up" || status === "unread" || status === "not_started")) return true; // #13191
  return !todo.done_at && reviewState(todo) === "pending_review" && awaitsReview(todo, status);
}

/** The message card still waits for Send: not sent, filed, dismissed, or put off. */
export function messageWaits(it: FeedItem, deferred: Record<string, unknown> = {}): boolean {
  return it.kind === "message" && !cardSent(it) && !cardFiled(it) && !cardDismissed(it) && !deferred[it.id];
}

/** The review round, not the task id alone: a later decision must remain visible. */
export function decisionRound(todo: Todo): string {
  return todo.supervision?.reviews?.at(-1)?.at || todo.decision_review?.review_at || todo.ask?.asked_at || "";
}

export function decisionLaterKey(todo: Todo): string {
  return `decision:${todo.manager || ""}:${todo.id}:${decisionRound(todo)}`;
}

/** A request's task home is shared by Needs you and the decision pager. */
export function taskRequestRouting(todos: readonly Todo[], interactions: readonly Interaction[], lookup: SessionLookup, conversation: string, conversationMachine?: string) {
  const activeAgents = (todo: Todo) => (todo.done_at ? [] : (todo.agents ?? []).filter((a) => !a.released_at));
  const homeAgents = (todo: Todo) => {
    if (todo.done_at) return [];
    const linked = linkedParkInteraction(todo, interactions);
    return (todo.agents ?? []).filter((a) => !a.released_at ||
      (linked !== undefined && a.machine_id === linked.machine_id && a.session_id === linked.session_id));
  };
  // One home per request (docs/design/DESIGN_SYSTEM.md 4.8): an Agent several of these tasks list asks under one of them,
  // the task the hub ties its session to (`todo`), else the first that lists it. The others neither repeat its cards nor
  // wait on the user for them; they are in progress.
  const listing = new Map<string, string[]>();
  for (const todo of todos) {
    for (const a of homeAgents(todo)) {
      const k = sessionKey(a.machine_id, a.session_id);
      const ids = listing.get(k) ?? [];
      if (!ids.includes(todo.id)) listing.set(k, [...ids, todo.id]);
    }
  }
  const homeOf = (mid: string, sid: string): string | undefined => {
    const ids = listing.get(sessionKey(mid, sid));
    const tied = lookup(mid, sid)?.todo?.id;
    return tied && ids?.includes(tied) ? tied : ids?.[0];
  };
  const asksOf = new Map<string, Interaction[]>();
  const conversationKey = conversationMachine ? sessionKey(conversationMachine, conversation) : undefined;
  for (const ix of [...interactions].sort((a, b) => byTime(a.created_at, b.created_at) || a.id.localeCompare(b.id))) {
    // Child requests follow their parent Agent. A task Agent's own request can fold directly under
    // the supervising conversation and still belongs to the task that lists that Agent.
    const home = homeKey(ix);
    const origin = sessionKey(ix.machine_id, ix.session_id);
    const foldedUnderConversation = conversationKey !== undefined && home === conversationKey && origin !== conversationKey;
    const key = foldedUnderConversation && !listing.has(home) && listing.has(origin) ? origin : home;
    asksOf.set(key, [...(asksOf.get(key) ?? []), ix]);
  }
  function requestsFor(sourceTodo: Todo) {
    const candidate = linkedParkInteraction(sourceTodo, interactions);
    const linkedHome = candidate ? homeOf(candidate.machine_id, candidate.session_id) : undefined;
    const todo = linkedHome !== undefined && linkedHome !== sourceTodo.id ? withoutParkInteraction(sourceTodo) : sourceTodo;
    const active = activeAgents(todo);
    const own = active.filter((a) => homeOf(a.machine_id, a.session_id) === todo.id);
    const linked = linkedParkInteraction(todo, interactions);
    // a card the task's supervising conversation holds (delegating) is that conversation's until it passes it up
    const routed = own.flatMap((a) => asksOf.get(sessionKey(a.machine_id, a.session_id)) ?? []);
    const ownAsks = routed.filter((ix) => !ix.delegating);
    const asks = linked && !ownAsks.some((ix) => ix.id === linked.id) ?
      [...ownAsks, linked].sort((a, b) => byTime(a.created_at, b.created_at) || a.id.localeCompare(b.id)) : ownAsks;
    return { todo, active, own, linked, routed, ownAsks, asks };
  }
  return { asksOf, requestsFor };
}

export function assistantEntries(input: AssistantEntriesInput): AssistantEntries {
  const { conversation, main, feed } = input;
  const owns = (owner: string | undefined) => !input.manager || sameManager(owner, input.manager);
  const todos = input.manager ? input.todos.filter((t) => owns(t.manager)) : input.todos;
  const sessions = input.manager ? input.sessions.filter((s) => owns(s.manager)) : input.sessions;
  const interactions = input.manager ? input.interactions.filter((ix) => owns(ix.manager)) : input.interactions;
  const plans = input.manager
    ? Object.fromEntries(Object.entries(input.plans).filter(([, plan]) => owns(plan.manager)))
    : input.plans;
  const now = input.now ?? Date.now();
  const lookup = withInteractionLookup(input.lookup ?? makeSessionLookup(sessions), interactions);
  const conversationMachine = input.conversationMachine ?? sessions.find((session) => session.session_id === conversation)?.machine_id;
  const isMain = !!main && main === conversation;
  const byTodo = new Map(todos.map((todo) => [todo.id, todo]));
  // failed starts whose work finished in a related to-do: closed, as the To-dos page has them (#3713)
  const superseded = supersededFailedStarts(todos);
  const asking: NeedsYouTask[] = [];
  const deciding: RankedTask[] = [];
  const verify: { item: Extract<NeedsYouItem, { kind: "task" }>; rank: number; at: string }[] = [];
  const open: { item: RunningItem; rank: number; at: string }[] = [];
  const shelved: { item: RunningItem; at: string }[] = [];
  const parked: { item: RunningItem; at: string }[] = [];
  const review: { item: RunningItem; rank: number; at: string }[] = [];
  const finished: FinishedItem[] = [];
  const folded = new Set<string>();
  const slots = taskCardSlots(conversation, input.cards ?? [], feed, plans, todos, isMain);
  const openOf = openByView(todos);
  // The hub counts the parent once. Include its waiting steps even if a different conversation dispatched them.
  const slotIds = new Set(slots.map((card) => card.todo_id));
  const carried = new Map<string, RankedTask[]>();
  const stepRows = new Map<string, RankedTask[]>();
  for (const step of todos) {
    const parent = step.part_of ? byTodo.get(step.part_of) : undefined;
    if (!parent || parent === step || !slotIds.has(parent.id) || !parent.view?.steps ||
        !hubNeedsYou(parent, openOf) || step.view?.section !== "needs_you" || step.id.startsWith("todo_od_")) continue;
    const rows = stepRows.get(parent.id) ?? [];
    stepRows.set(parent.id, rows);
    carried.set(step.id, rows);
    if (!slotIds.has(step.id)) {
      slots.push({ id: `task:${step.id}`, todo_id: step.id, at: step.created_at, updated_at: step.updated_at, state: "not_started" });
    }
  }
  const { asksOf, requestsFor } = taskRequestRouting(slots.flatMap((card) => {
    const todo = card.todo_id ? byTodo.get(card.todo_id) : undefined;
    return todo ? [todo] : [];
  }), interactions, lookup, conversation, conversationMachine);
  for (const card of slots) {
    const sourceTodo = card.todo_id ? byTodo.get(card.todo_id) : undefined;
    // a plan task with no to-do yet is its plan's row (below); a to-do the hub no longer sends is finished
    if (!sourceTodo) continue;
    const { todo, active, own, linked, routed, ownAsks, asks } = requestsFor(sourceTodo);
    // what the task waits on the user for is its own Agents' (an Agent another task holds asks there)
    const status = own.length < active.length ? withNeeds(entryStatus(todo, lookup, plans), todoStatus({ ...todo, agents: own }, lookup)) : entryStatus(todo, lookup, plans);
    const key = `task:${todo.id}`;
    const bucket = cardBucket(card, todo, plans, lookup, now, input.restored?.get(todo.id) ?? 0, superseded.has(todo.id));
    const countsOpen = bucket === "open";
    // a failed start carries done_at (the hub ends it), yet it is the user's to retry while its bucket says open; a
    // supervised task's failed start is its supervisor's to retry or remedy, and an Agent whose every card its
    // supervisor holds waits on that conversation, not on the user
    const supervised = !!todo.supervision?.supervisor;
    // #15763: a card set the supervisor holds (delegating) is that conversation's whenever this task has no
    // direct owner card of its own, supervised or routed by handNonOwnerCard.
    const held = routed.length > 0 && ownAsks.length === 0;
    // #15638: an interrupted run (e.g. a transient 503) under a direction supervisor stays with that supervisor
    // until it escalates; an unsupervised personal terminal still waits on its owner.
    const supervisedInterrupted = status === "interrupted" && supervised;
    const waits = status === "failed"
      ? bucket === "open" && !supervised
      : !todo.done_at && WAITS_ON_YOU.has(status) && !supervisedInterrupted && !held;
    const decision = taskAwaitsDecision(todo);
    const deliveryApproval = heldDelivery(todo)?.unblock === "user";
    const userCheck = !todo.done_at && reviewState(todo) === "user_check";
    // The global badge skips quiet tasks. Explicit reviews can still ask the owner in the conversation tray. A quiet
    // user_check the hub itself still lists as needs_you (reason verify) is un-quieted here so that authoritative view
    // is honored — this also covers an older hub during rolling upgrade. With #15680's projection the hub instead sends
    // a non-needs_you view for user_check, so `hub` is false below and it falls through to the calm To verify layer.
    const supervisesHere = todo.supervision?.supervisor === conversation;
    const quietOwnerAskHere = todo.quiet && (isMain || supervisesHere) &&
      (reviewState(todo) === "user_check" ||
        Boolean(taskReviewDecisionAsk(todo) && !input.deferred?.[decisionLaterKey(todo)]));
    // The hub's view decides when there is one, so this tray, the To-dos badge and the hub's needs_you agree.
    const hub = hubNeedsYou(quietOwnerAskHere ? { ...todo, quiet: false } : todo, (id) => carried.has(todo.id) && openOf(id));
    const item: NeedsYouTask = { kind: "task", key, todo, status, asks, countsOpen };
    // Only the review decision is hidden; a live Agent question still has its home.
    // A user_check round follows the same Later/Dismiss rules as a decision. Its parent may still carry live
    // confirmation steps, so hiding the verification must not hide those independently actionable steps.
    const hiddenDecision = !deliveryApproval && status !== "needs_you" && (decision || userCheck) &&
      (decisionDismissed(todo) || input.deferred?.[decisionLaterKey(todo)]);
    const parentSteps = carried.get(todo.id);
    if (parentSteps) {
      if (asks.length || (!hiddenDecision && (waits || decision || deliveryApproval || userCheck))) {
        parentSteps.push({ item, rank: todoRank(todo, status), at: card.at });
      }
    } else if (asks.length) asking.push(item);
    else if (hub ?? (waits || decision || deliveryApproval)) {
      if (!hiddenDecision) deciding.push({ item, rank: todoRank(todo, status), at: card.at });
    }
    // Completed tasks awaiting optional owner verification (`user_check`) sit in their own calm To verify entry rather
    // than inflating urgent red Needs you or disappearing into In progress.
    else if (userCheck) verify.push({ item: { kind: "task", key, todo, status, asks, countsOpen }, rank: todoRank(todo, status), at: card.at });
    // its supervisor's review (#8678): Persona's own work, not the owner's and not a wait on the upstream maintainer, so
    // its own group whatever the To-dos page files it under, even with a fresh open PR; once reviewed (accepted, or parked
    // on the PR) the PR rule below files it
    else if (underReview(todo, status)) review.push({ item: { kind: "task", key, card, todo, status, countsOpen }, rank: todoRank(todo, status), at: card.at });
    // Use the To-dos page's wait classification. Live work and owner decisions outrank a hold or an open PR.
    else if (reviewState(todo) !== "pending_review" && triage(todo, lookup, now, input.restored?.get(todo.id) ?? 0, superseded.has(todo.id)).section === "parked") parked.push({ item: { kind: "task", key, card, todo, status, countsOpen }, at: card.at });
    else if (bucket === "open" || reviewState(todo) === "pending_review") open.push({ item: { kind: "task", key, card, todo, status, activity: taskBackgroundWork(todo, lookup), countsOpen }, rank: todoRank(todo, status), at: card.at });
    else if (bucket === "shelved") shelved.push({ item: { kind: "task", key, card, todo, status, countsOpen: false }, at: card.at });
    else {
      // finished work a conversation handed out, for a day after it finished, seen or not (#10922); a failed start a
      // related to-do finished is history, not news
      const tr = triage(todo, lookup, now, input.restored?.get(todo.id) ?? 0, superseded.has(todo.id));
      const ended = finishedAt(todo, tr);
      const seenAt = Date.parse(todo.seen_at ?? "");
      if ((tr.section === "just_done" || tr.section === "done") && status !== "failed" && todo.origin && ended > 0 && now - ended < JUST_DONE_MS) {
        // Closed and accepted work is already Done on the To-dos page (#5732); Finished has its own unseen marker.
        finished.push({ kind: "task", key, card, todo, status, fresh: Number.isNaN(seenAt) || seenAt < ended, at: ended });
      }
      continue;
    }
    for (const agent of active) folded.add(sessionKey(agent.machine_id, agent.session_id));
    if (linked) folded.add(sessionKey(linked.machine_id, linked.session_id));
  }

  // A pending step needs its own row when its parent has no visible Needs you row.
  if (stepRows.size) {
    const visibleTasks = new Set(asking.map((item) => item.todo.id));
    for (const { item } of deciding) visibleTasks.add(item.todo.id);
    for (const [parent, steps] of stepRows) {
      if (visibleTasks.has(parent)) continue;
      for (const step of steps) {
        if (step.item.asks.length) asking.push(step.item);
        else deciding.push(step);
      }
    }
  }

  // Agents that belong to no task, the main conversation's only (dockAgents): asking ones wait on the user, working ones run.
  const agentsAsking: NeedsYouItem[] = [];
  const agentsRunning: RunningItem[] = [];
  for (const { session, state } of dockAgents(sessions.map((s) => lookup(s.machine_id, s.session_id) ?? s), todos, conversation, main)) {
    const sk = sessionKey(session.machine_id, session.session_id);
    if (folded.has(sk)) continue;
    const key = `agent:${sk}`;
    const agentAsks = asksOf.get(sk) ?? [];
    const userAsks = agentAsks.filter((ix) => !ix.delegating);
    if (state === "needs_you" && (agentAsks.length === 0 || userAsks.length > 0)) {
      agentsAsking.push({ kind: "agent", key, session, asks: userAsks });
    } else {
      agentsRunning.push({ kind: "agent", key, session, activity: backgroundWork(session) });
    }
  }

  const plansWaiting: NeedsYouItem[] = Object.values(plans)
    .filter((plan) => ((isMain && plan.space !== "health") || plan.conversation === conversation) && plan.state === "open")
    .sort((a, b) => byTime(a.created_at, b.created_at) || a.id.localeCompare(b.id))
    .map((plan) => ({ kind: "plan", key: `plan:${plan.id}`, plan }));
  const messages: NeedsYouItem[] = feed.filter((it) => messageWaits(it, input.deferred)).map((item) => ({ kind: "message", key: `message:${item.id}`, item }));

  const newest = (a: { at: string; rank?: number }, b: { at: string; rank?: number }) => (a.rank ?? 0) - (b.rank ?? 0) || byTime(b.at, a.at);
  const firstAsk = (item: NeedsYouItem) => (item.kind === "task" || item.kind === "agent" ? item.asks[0]?.created_at : undefined);
  const needsYou = [
    ...[...asking, ...agentsAsking].sort((a, b) => byTime(firstAsk(a), firstAsk(b))),
    ...plansWaiting,
    ...deciding.sort(newest).map((row) => row.item),
    ...messages,
  ].filter((item) => {
    if (item.kind !== "task") return true;
    const steps = stepRows.get(item.todo.id);
    if (!steps) return true;
    item.steps = steps.map(({ item }) => item);
    return steps.length > 0 || item.asks.length > 0 || taskAwaitsDecision(item.todo) ||
      heldDelivery(item.todo)?.unblock === "user" || WAITS_ON_YOU.has(item.status);
  });
  return {
    needsYou,
    verify: verify.sort(newest).map((row) => row.item),
    running: [...open.sort(newest).map((row) => row.item), ...agentsRunning],
    shelved: shelved.sort(newest).map((row) => row.item),
    parked: parked.sort(newest).map((row) => row.item),
    review: review.sort(newest).map((row) => row.item),
    // newest finished first; the same moment by id, so every client lists them in one order
    finished: finished.sort((a, b) => b.at - a.at || (a.todo.id < b.todo.id ? -1 : a.todo.id > b.todo.id ? 1 : 0)),
  };
}

/** Needs you's rows by conversation (#4314, docs/assistant/assistant.md "Background rounds"): review decisions
 *  follow their supervisor. Personal verifications are in Main's own group and their supervisor's tray. Rows
 *  showing a live Agent interaction follow their origin. This conversation's group comes
 *  first, then the others in the order their first row appears; the rows and their order within a group are as given,
 *  so the entry's count is unchanged. One group when every row is this conversation's. */
export function needsYouBySupervisor(items: readonly NeedsYouItem[], conversation: string, isMain = false): { supervisor: string; items: NeedsYouItem[] }[] {
  const groups = new Map<string, NeedsYouItem[]>([[conversation, []]]);
  for (const item of items) {
    let supervisor = conversation;
    if (item.kind === "task") {
      const { todo, asks } = item;
      const shown = !asks.length && !openAsk(todo) && !taskReviewDecisionAsk(todo) && item.steps?.[0] ? item.steps[0] : item;
      const target = shown.todo;
      if (!shown.asks.length && reviewState(target) === "user_check") {
        supervisor = isMain ? conversation : target.supervision?.supervisor || target.origin || conversation;
      } else {
        supervisor = (!shown.asks.length && taskReviewDecisionAsk(target) && target.supervision?.supervisor) || todo.origin || conversation;
      }
    } else if (item.kind === "plan") {
      supervisor = item.plan.conversation || conversation;
    }
    const group = groups.get(supervisor);
    if (group) group.push(item);
    else groups.set(supervisor, [item]);
  }
  return [...groups].map(([supervisor, rows]) => ({ supervisor, items: rows }));
}
