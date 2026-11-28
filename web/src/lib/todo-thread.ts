// The chat thread a todo is bound to, as the todo page shows it (#5279, docs/assistant/todos.md "Chat thread"). The web only shows
// the binding and lets the user unbind it: binding needs a list of the bot's recent topics, which the hub has no
// route for, so a binding is made by requesting a to-do in a registered topic group or by an Agent (`todo_bind_thread`).
import { t } from "../i18n";
import { api, enc } from "./api";
import { hubHas } from "./store";
import type { Todo, TodoThread } from "./todos";

/** The hub binds to-dos to chat threads (hello `features`): the routes and `thread` on a to-do. A hub without it never sends one. */
export const TODO_THREADS_FEATURE = "todo_threads";

/** Whether the page shows anything of to-do threads: only when the hub says it has them (not "unknown": an older hub has none). */
export function todoThreadsOn(features: string[]): boolean {
  return hubHas(features, TODO_THREADS_FEATURE);
}

/** How many characters of a thread id are kept before it is cut in the middle. */
const SHORT_ID_MAX = 12;

/** A thread id short enough for a line: `omt_1a2b3c4d5e6f` → `omt_1a…5e6f`. Short ids are kept whole. */
export function shortThreadId(id: string): string {
  const chars = [...id.trim()];
  if (chars.length <= SHORT_ID_MAX) return chars.join("");
  return `${chars.slice(0, 6).join("")}…${chars.slice(-4).join("")}`;
}

/** What names the thread: its title, else its shortened id. */
export function threadName(thread: TodoThread): string {
  return thread.title.trim() || shortThreadId(thread.thread_id);
}

/** The kind of thread it is: Lark's own wording for a Lark topic; any other channel by its id. */
export function threadKind(thread: TodoThread): string {
  return thread.channel === "feishu" ? t("lib-todos.thread_feishu") : t("lib-todos.thread_channel", { channel: thread.channel });
}

/** The line the todo page shows: "Lark topic · <title or short id>". */
export function threadLine(thread: TodoThread): string {
  return t("lib-todos.thread_line", { kind: threadKind(thread), name: threadName(thread) });
}

/** The todo's thread when it has a usable one (the three ids that name it are there); else undefined. */
export function boundThread(todo: Pick<Todo, "thread">): TodoThread | undefined {
  const th = todo.thread;
  return th && th.channel && th.chat_id && th.thread_id ? th : undefined;
}

/** Unbind the todo from its thread; the thread's messages are untouched. */
export async function unbindTodoThread(todoId: string): Promise<void> {
  await api(`/todos/${enc(todoId)}/thread`, { method: "DELETE" });
}
