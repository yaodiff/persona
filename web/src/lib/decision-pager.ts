import { taskRequestRouting, type AssistantEntriesInput } from "./assistant-entries";
import { sameManager } from "./manager-choice";
import { taskCardSlots, taskInConversation } from "./task-cards";
import { byTime } from "./times";
import type { Interaction } from "./types";
import type { SessionLookup } from "./todos";

export type DecisionCard = { interaction: Interaction; taskTitle?: string };

export function decisionKey(ix: Interaction): string {
  return JSON.stringify([ix.manager ?? "", ix.machine_id, ix.session_id, ix.id]);
}

type TaskDecisionInput = Pick<AssistantEntriesInput, "conversation" | "conversationMachine" | "main" | "manager" | "todos" | "interactions" | "feed" | "plans" | "cards"> & { lookup: SessionLookup };

export function taskDecisionCards(input: TaskDecisionInput): DecisionCard[] {
  const { conversation, main, manager, feed, plans, cards, lookup } = input;
  const interactions = input.interactions.filter((ix) => sameManager(ix.manager, manager));
  if (!interactions.length) return [];
  const todos = input.todos.filter((todo) => sameManager(todo.manager, manager));
  const byTodo = new Map(todos.map((todo) => [todo.id, todo]));
  const slots = taskCardSlots(conversation, cards ?? [], feed, plans, todos, main === conversation);
  const orderedTasks = slots.flatMap((card) => {
    const todo = card.todo_id ? byTodo.get(card.todo_id) : undefined;
    return todo ? [todo] : [];
  });
  // Route only the tasks that belong here; a task merely watched by the main conversation cannot take a request's home.
  const scopedTasks = orderedTasks.filter((todo) => taskInConversation(todo, conversation));
  const { requestsFor } = taskRequestRouting(scopedTasks, interactions, lookup, conversation, input.conversationMachine);
  return scopedTasks.flatMap((todo) =>
    requestsFor(todo).asks.map((interaction) => ({ interaction, taskTitle: todo.title })));
}

/** Keep the conversation's blocking cards in their incoming order, then show the tasks' oldest cards. */
export function decisionCards(own: readonly Interaction[], tasks: readonly DecisionCard[]): DecisionCard[] {
  const seen = new Set<string>();
  const cards = [...own.map((interaction) => ({ interaction })),
    ...[...tasks].sort((a, b) => byTime(a.interaction.created_at, b.interaction.created_at))];
  return cards.filter(({ interaction }) => {
    const key = decisionKey(interaction);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function decisionPosition(keys: readonly string[], current: string | undefined, previous: number): number {
  const stillThere = current === undefined ? -1 : keys.indexOf(current);
  return stillThere >= 0 ? stillThere : Math.min(Math.max(previous, 0), keys.length - 1);
}

export function decisionDots(count: number, current: number): number[] {
  const size = Math.min(7, count);
  const first = Math.min(Math.max(current - 3, 0), count - size);
  return Array.from({ length: size }, (_, offset) => first + offset);
}
