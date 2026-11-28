import { useMemo } from "react";
import { t } from "../../i18n";
import { assistantEntries } from "../../lib/assistant-entries";
import { useFeed } from "../../lib/feed";
import { openAgent, openTask } from "../../lib/handoff";
import { defaultManager } from "../../lib/manager-choice";
import { refreshTodos, useStore } from "../../lib/store";
import { reviewState, reportText } from "../../lib/supervision";
import { mostUrgentAgent, todoSupervisorLabel, type SessionLookup, type Todo } from "../../lib/todos";
import { todoLook, waitOf } from "../../lib/todo-wait-reason";
import { Button, Loading } from "../../ui/atoms";
import { PanelHeader } from "../../ui/PanelHeader";
import { Empty } from "../assistant/parts";
import { NeedsYouBody, progressOf, statusOf, useTaskLookup } from "../assistant/Tasks";

export function conversationTasks(todos: readonly Todo[], sid: string, manager: string, fallbackManager: string) {
  return todos.filter((todo) => (todo.manager ?? fallbackManager) === manager &&
    (todo.origin === sid || todo.supervision?.supervisor === sid));
}

export function ConversationTasksPanel({ sid, manager, onClose }: { sid: string; manager: string; onClose: () => void }) {
  const todos = useStore((s) => conversationTasks(s.todos, sid, manager, defaultManager()));
  const loaded = useStore((s) => s.todosLoaded);
  const error = useStore((s) => s.todosError);
  const lookup = useTaskLookup();
  const plans = useFeed((s) => s.plans);
  const interactions = useStore((s) => s.interactions);
  const deferred = useFeed((s) => s.later);
  const needsYou = useMemo(() => assistantEntries({
    conversation: sid, todos, sessions: [], interactions, plans, feed: [], lookup, deferred,
    cards: todos.map((todo) => ({
      id: `task:${todo.id}`, todo_id: todo.id, at: todo.created_at, updated_at: todo.updated_at, state: "not_started",
    })),
  }).needsYou, [sid, todos, interactions, plans, lookup, deferred]);
  const waiting = new Set(needsYou.flatMap((item) => item.kind === "task" ? [item.todo.id] : []));
  const rows = todos.filter((todo) => !waiting.has(todo.id))
    .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));

  return <>
    <PanelHeader icon="todo" title={t("sidepanel.conversation_tasks")} onClose={onClose} />
    <div className="min-h-0 flex-1 overflow-y-auto p-3">
      {!loaded ? <Loading /> : error && todos.length === 0 ?
        <Empty icon="todo" title={t("todos.load_failed")} action={
          <Button tone="secondary" icon="retry" onClick={() => void refreshTodos()}>{t("todos.retry")}</Button>
        }>{error}</Empty> : rows.length === 0 && needsYou.length === 0 ? <p className="text-sm text-ink-3">{t("sidepanel.conversation_tasks_empty")}</p> :
        <ul className="flex flex-col gap-2">
          {needsYou.map((item) => <li key={item.key}>
            <NeedsYouBody item={item} onOpenChat={onClose} />
            {item.kind === "task" && <TaskAgentLink todo={item.todo} lookup={lookup} />}
          </li>)}
          {rows.map((todo) => {
            const status = statusOf(todo, lookup, plans);
            const session = mostUrgentAgent(todo, lookup)?.session;
            const summary = todo.wait != null ? todo.wait.reason : reportText(todo.supervision) || progressOf(session)?.text || todo.start_error;
            const label = todoSupervisorLabel(todo, status, lookup) ??
              (todo.wait == null && reviewState(todo) === "parked" ? t("assistant-tasks.review_parked") : todoLook(todo, status, waitOf(todo, status, lookup)).label);
            return <li key={todo.id} className="rounded-control bg-field p-3">
              <div className="flex items-baseline justify-between gap-2">
                <button type="button" className="touch-hit min-w-0 truncate text-left text-sm font-semibold hover:underline" title={todo.title} onClick={() => openTask(todo.id)}>{todo.title}</button>
                <span className="shrink-0 text-xs text-ink-2">{label}</span>
              </div>
              {summary && <p className="truncate text-xs text-ink-2" title={summary}>{summary}</p>}
              <TaskAgentLink todo={todo} lookup={lookup} />
            </li>;
          })}
        </ul>}
    </div>
  </>;
}


function TaskAgentLink({ todo, lookup }: { todo: Todo; lookup: SessionLookup }) {
  const agent = mostUrgentAgent(todo, lookup)?.agent ??
    todo.agents?.find((agent) => agent.session_id && !agent.released_at) ??
    todo.agents?.find((agent) => agent.session_id);
  if (!agent) return null;
  return <button type="button" className="touch-hit text-xs text-accent hover:underline"
    onClick={() => openAgent(agent.machine_id, agent.session_id)}>{t("sidepanel.agent")}</button>;
}
