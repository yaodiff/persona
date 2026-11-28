// A task's detail beside the assistant's conversation (docs/assistant/unified-entry.md "Task details"; iOS TaskDetailSheet): its
// agent's own chat — everything the Agents page does works here — and, with several agents, a switch between them. Opened
// while Agents open in the side panel (lib/agent-open.ts, #5424); loaded with the first one (SidePanel).

import { lazy, Suspense, useState } from "react";
import { isRemovable, openAsk, todoStatusLabel } from "../../lib/todos";
import { decisionLaterKey } from "../../lib/assistant-entries";
import { statusWord } from "../../lib/todo-wait-reason";
import { TodoAskLine } from "../TodoAsk";
import { decisionDismissed, reportText, reviewState } from "../../lib/supervision";
import { VerificationReport } from "./VerificationReport";
import { openPanel } from "../../lib/panel";
import { TaskWaitInfo } from "./TaskWaitInfo";
import { LatestReport } from "./Supervision";
import { useStore } from "../../lib/store";
import { Button, IconButton, Menu, useMenu, type MenuItem } from "../../ui/atoms";
import { ChatView } from "../chat/ChatView";
import { PanelHeader } from "../../ui/PanelHeader";
import { NeedsYouBody, place, removeTaskNow, retryTaskNow, useRetrying, useTaskState, useTodo } from "./Tasks";
import { retryOnLauncherNow, useFailedStart } from "./FailedTaskRow";
import { stateOf } from "../../lib/format";
import { usageLimitLabel } from "../../lib/usage-limit";
import { href, type Route } from "../../lib/router";
import { t } from "../../i18n";
import { startInFlight, useFeed } from "../../lib/feed";
import { TaskWorkspaceLine } from "./TaskWorkspace";

// the question a failed start's tab stopped on, answered here (#10365): only for such a failure. After the imports: in
// development Vite turns `import { lazy }` into a const at its place, which a call above it would read too early.
const ShellQuestion = lazy(() => import("../ShellQuestionCard"));

/** ⋯'s "Open in new window": the page the panel shows, as its own tab (⌘-click on the link does the same). */
function openInWindow(to: Route): MenuItem {
  return { key: "open-window", label: t("assistant-taskpanel.open_in_new_window"), icon: "external", onSelect: () => void window.open(href(to), "_blank", "noopener") };
}

export function TaskPanel({ todoId, onClose }: { todoId: string; onClose: () => void }) {
  const todo = useTodo(todoId);
  const st = useTaskState(todo);
  const sessions = useStore((s) => s.sessions);
  const [picked, setPicked] = useState<string | null>(null);
  const retrying = useRetrying(todoId);
  const { task: failedTask, instead } = useFailedStart(todo);
  const plans = useFeed((s) => s.plans);
  const deferred = useFeed((s) => todo?.part_of ? s.later[decisionLaterKey(todo)] : undefined);
  const planned = Object.values(plans).flatMap((p) => p.tasks).find((t) => t.todo_id === todoId);
  if (!todo || !st) {
    return (
      <>
        <PanelHeader icon="todo" title={t("assistant-taskpanel.title")} onClose={onClose} />
        <div className="p-6 text-[13px] text-ink-2">{t("assistant-taskpanel.gone")}</div>
      </>
    );
  }
  if (reviewState(todo) === "user_check") {
    return <>
      <PanelHeader icon="todo" title={todo.title} subtitle={t("assistant-taskpanel.report")} onClose={onClose} />
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <VerificationReport todo={todo} onOpenWorker={(mid, sid) => openPanel({ kind: "agent", mid, sid }, { push: true })} />
      </div>
    </>;
  }
  const agents = (todo.agents ?? []).filter((a) => !a.released_at);
  const lead = st.lead;
  const chosen = agents.find((a) => `${a.machine_id}/${a.session_id}` === picked) ?? (lead ? agents.find((a) => a.session_id === lead.session_id) : agents[0]);
  const starting = st.status === "not_started" && startInFlight(todo, plans);
  // the header also stands for the agent shown: after ⌄ picks another, its machine and project, not the lead's
  const shown = chosen ? (sessions[chosen.machine_id] ?? []).find((x) => x.session_id === chosen.session_id) : undefined;
  // The chat header prepends the usage-limit line of the agent it shows, so the status word is not said twice.
  const ask = openAsk(todo);
  const hasStepAction = todo.part_of && todo.view?.section === "needs_you" && ask && !deferred && !decisionDismissed(todo);
  const head = hasStepAction ? todoStatusLabel(todo, "needs_you") :
    st.status === "usage_limit" && usageLimitLabel(shown ?? lead) ? "" : starting ? t("assistant-tasks.status_starting") : st.supervisorLabel ?? statusWord(st.status, st.wait);
  const subtitle = [head, chosen ? place({ ...todo, agents: [chosen] }, shown ?? lead) : place(todo, lead)].filter(Boolean).join(" · ");
  const taskItems: MenuItem[] = [
    openInWindow(chosen ? { name: "session", mid: chosen.machine_id, sid: chosen.session_id } : { name: "todos", id: todo.id }),
    ...(isRemovable(st.status) ? [{ key: "remove", label: t("assistant-taskpanel.remove_ellipsis"), icon: "trash" as const, danger: true, onSelect: () => removeTaskNow(todo) }] : []),
  ];
  // several agents: the ⌄ beside the title switches between them (iOS #3255)
  const switcher: MenuItem[] = agents.map((a) => {
    const s = (sessions[a.machine_id] ?? []).find((x) => x.session_id === a.session_id);
    const key = `${a.machine_id}/${a.session_id}`;
    return { key, label: s?.title || a.title || a.agent || a.session_id.slice(0, 8), state: s ? stateOf(s) : undefined, checked: chosen === a, onSelect: () => setPicked(key) };
  });
  // Steps opened from Needs you use the same complete, round-bound action as the tray.
  const stepAction = hasStepAction ? (
    <div className="border-b border-line bg-inset p-2.5">
      <NeedsYouBody item={{ kind: "task", key: todo.id, todo, status: st.status, asks: [], countsOpen: true }} nested />
    </div>
  ) : null;
  const waitInfo = todo.wait != null && <div className="border-b border-line px-4 py-2"><TaskWaitInfo todo={todo} /></div>;
  if (chosen) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <ChatView
          key={`${chosen.machine_id}/${chosen.session_id}`}
          mid={chosen.machine_id}
          sid={chosen.session_id}
          // the bubble before × opens the Agent shown (the one ⌄ picked) as its own page
          panel={{ onClose, page: { name: "session", mid: chosen.machine_id, sid: chosen.session_id }, title: todo.title, sub: subtitle, items: taskItems, beside: switcher.length > 1 ? <AgentSwitch agents={switcher} /> : undefined }}
          // what the task's result waits on from the user (#2056), and what its agent last reported, as it wrote it (#3181),
          // over its agent's chat
          banner={waitInfo || ask || reportText(todo.supervision) || planned?.workspace ? (
            <>
              {waitInfo}
              {planned?.workspace && <div className="border-b border-line px-4 py-2"><TaskWorkspaceLine task={planned} /></div>}
              {stepAction ?? (ask && <TodoAskLine todo={todo} className="border-b border-line bg-inset px-4 py-2.5" />)}
              <LatestReport todo={todo} banner />
            </>
          ) : undefined}
        />
      </div>
    );
  }
  return (
    <>
      <PanelHeader icon="todo" title={todo.title} subtitle={subtitle} actions={<PanelMore items={taskItems} />} onClose={onClose} />
      <div className="flex min-h-0 flex-1 flex-col">
        {waitInfo}
        {planned?.workspace && <div className="border-b border-line px-4 py-2"><TaskWorkspaceLine task={planned} /></div>}
        {stepAction ?? (st.failure || todo.agents?.some((a) => a.start_error) ? (
          <div className="flex flex-col items-start gap-3 p-6 text-[13px]">
            <div className="font-semibold text-red">{t("assistant-taskpanel.start_failed")}</div>
            <div className="text-ink-2">{st.failure ?? todo.agents?.find((a) => a.start_error)?.start_error}</div>
            {st.status === "failed" && failedTask?.error_code === "shell_waiting" && <Suspense fallback={null}><ShellQuestion todo={todo} task={failedTask} className="w-full" /></Suspense>}
            <div className="flex flex-wrap gap-2">
              {/* its launcher can't start there: on the computer's other launcher first (#10365) */}
              {st.status === "failed" && instead && (
                <Button icon="retry" tone="accent" busy={retrying} disabled={retrying} onClick={() => retryOnLauncherNow(todo, instead)} testId="task-retry-launcher">
                  {t("assistant-task-actions.use_launcher_instead", { launcher: instead })}
                </Button>
              )}
              {st.status === "failed" && (
                <Button icon="retry" busy={retrying} disabled={retrying} onClick={() => retryTaskNow(todo)}>
                  {t("assistant-taskpanel.retry")}
                </Button>
              )}
              <Button icon="trash" onClick={() => removeTaskNow(todo)}>
                {t("assistant-taskpanel.remove")}
              </Button>
            </div>
          </div>
        ) : (
          <div className="p-6 text-[13px] text-ink-2">{starting ? t("assistant-taskpanel.starting") : t("assistant-taskpanel.no_agent")}</div>
        ))}
      </div>
    </>
  );
}

/** Beside the title of a task with several agents: ⌄ switches between them (iOS #3255). */
function AgentSwitch({ agents }: { agents: MenuItem[] }) {
  const pick = useMenu();
  return (
    <>
      <IconButton icon="chevron" label={t("assistant-taskpanel.switch_agent")} onClick={pick.open} active={Boolean(pick.anchor)} className="-ml-1.5 shrink-0" />
      {pick.anchor && <Menu anchor={pick.anchor} onClose={pick.close} items={agents} width={320} />}
    </>
  );
}

/** The panel header's ⋯ while no agent's chat (and so no chat header) is there yet. */
function PanelMore({ items }: { items: MenuItem[] }) {
  const more = useMenu();
  return (
    <>
      <IconButton icon="more" label={t("assistant-taskpanel.more")} onClick={more.open} active={Boolean(more.anchor)} />
      {more.anchor && <Menu anchor={more.anchor} onClose={more.close} align="right" items={items} />}
    </>
  );
}

/** An agent the main conversation's dock shows that no task stands for (#1113; iOS opens it in the task's sheet): its
 * own chat, whose header is the panel's (its state, title and machine; ⋯ opens it in a new window). */
export function AgentPanel({ mid, sid, onClose }: { mid: string; sid: string; onClose: () => void }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ChatView key={`${mid}/${sid}`} mid={mid} sid={sid} panel={{ onClose, page: { name: "session", mid, sid }, items: [openInWindow({ name: "session", mid, sid })] }} />
    </div>
  );
}
