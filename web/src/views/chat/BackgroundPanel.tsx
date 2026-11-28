// An Agent's Background tasks side panel (#5421, after cindy's right-sidebar Background tasks tab, BackgroundTasksBody;
// lib/background-panel.ts opens it): what keeps the Agent going besides its turn. A list in two sections: Running (the
// /loop first, Persona's own; then the workflow runs, oldest start first, and the background commands, Monitors and
// subagents) and Completed (the ended workflow runs and retained shell / Monitor outcomes the node still lists, newest
// end first). A row is a round kind mark, a status mark and the title, with "status · progress · duration · tokens ·
// tool uses" under it. A workflow row opens its detail in the panel (Back, the run's head, progress and phases:
// WorkflowCard); a background command or Monitor opens its result, full command and bounded output here, with Back to
// the list, before its output file is known too (a Monitor's arrives with its first event, #10852); a subagent opens
// only with one. The panel reads the session from the store, live; clocks tick only while something runs.
import { Fragment, useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { t } from "../../i18n";
import { credentialOwner, subscribeCredentialOwner } from "../../lib/api";
import { useBackgroundChat, type BackgroundChat, type BackgroundPanel as BackgroundPanelObject } from "../../lib/background-panel";
import { openPanel, type Panel } from "../../lib/panel";
import { composing } from "../../lib/send-history";
import { getSession, useStore } from "../../lib/store";
import { backgroundDurationMs, backgroundStatus, backgroundTitle, loopCadence, loopNext, loopTask } from "../../lib/tasks";
import { compactTokens } from "../../lib/tokens";
import type { BackgroundTask, LoopInfo, Session, Workflow } from "../../lib/types";
import { mediaMatches, NARROW } from "../../lib/viewport";
import { workDuration, type AgentStatus } from "../../lib/work-groups";
import { otherBackground, recentWorkflows, workflowDurationMs, workflowName, workflowProgress, workflowTaskStatus } from "../../lib/workflows";
import { Button, copyText, cx, IconButton, Spinner, useNow } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { PanelHeader } from "../../ui/PanelHeader";
import { taskStatusWord } from "./TaskCard";
import { WorkflowCard } from "./WorkflowRun";
import { BackgroundCommandDetail } from "./BackgroundCommandDetail";

/** When a run started, or 0 when the hub does not say. */
const startOf = (w: Workflow) => Date.parse(w.started_at ?? "") || 0;
/** When a run ended, or its last sign of life. */
const endOf = (w: Workflow) => Date.parse(w.ended_at ?? "") || Date.parse(w.updated_at ?? "") || startOf(w);

/** What the list shows, in its order: the running runs oldest start first, the background tasks as the hub lists them,
 *  the ended runs and commands newest end first. */
export function backgroundSections(s: Session | undefined, now: number): { loop?: LoopInfo; running: Workflow[]; tasks: BackgroundTask[]; completed: Workflow[]; completedTasks: BackgroundTask[] } {
  const recent = recentWorkflows(s, now);
  const ended = s?.background_ended ?? [];
  const endedIDs = new Set(ended.map((task) => task.id));
  return {
    loop: s?.loop,
    running: recent.filter((w) => w.running).sort((a, b) => startOf(a) - startOf(b)),
    tasks: otherBackground(s).filter((task) => !endedIDs.has(task.id)),
    completed: recent.filter((w) => !w.running).sort((a, b) => endOf(b) - endOf(a)),
    completedTasks: [...ended].sort((a, b) => (Date.parse(b.ended_at ?? "") || 0) - (Date.parse(a.ended_at ?? "") || 0)),
  };
}

/** Whether a row opens its detail: a command or Monitor always (its output file may come later, #10852), anything else
 *  only with an output to read. A subagent's work is its own conversation, not a file. */
const opensDetail = (task: BackgroundTask) => Boolean(task.output_file) || task.kind === "shell" || task.kind === "monitor";

/** A background task's round kind mark: a command's terminal, a Monitor's, a subagent's, a run an older hub lists. */
function taskIcon(task: BackgroundTask): IconName {
  if (task.kind === "shell") return "terminal";
  if (task.kind === "agent") return "agent";
  if (task.kind === "monitor") return "monitor";
  if (task.kind === "workflow") return "workflow";
  return "run";
}

export interface BackgroundCommandDetailViewProps {
  mid: string;
  sid: string;
  cwd?: string;
  task: BackgroundTask;
  running: boolean;
  focus: boolean;
  owner: string;
  manager?: string;
  onBack: () => void;
  onOpenFile: () => void;
}

export interface BackgroundPanelProps {
  mid: string;
  sid: string;
  opened: BackgroundPanelObject;
  onClose: () => void;
  /** Explicit session snapshot (`null` means absent) skips the default store subscription. */
  session?: Session | null;
  /** Explicit auth partition skips the default credential-owner subscription. */
  owner?: string;
  /** Live session reader for action-time fencing; defaults to the latest controlled `session` or the default store. */
  getCurrentSession?: () => Session | undefined;
  /** Live auth-partition reader for action-time fencing; defaults to the latest controlled `owner` or `credentialOwner`. */
  getCurrentOwner?: () => string;
  /** Explicit chat actions (`null` means no chat is attached) skips the default background-chat registry. */
  chat?: BackgroundChat | null;
  /** Replaces the default side-panel navigator for list, task and file transitions. */
  onOpenPanel?: (panel: Panel) => void;
  /** Replaces clipboard writes when copying a command from the list. */
  onCopyCommand?: (command: string) => void | Promise<void>;
  /** Replaces the default command-output detail view when a host supplies its own output reader or view. */
  renderCommandDetail?: (detail: BackgroundCommandDetailViewProps) => ReactNode;
}

export interface BackgroundListProps {
  session: Session | undefined;
  mid: string;
  sid: string;
  onOpenRun: (task: string) => void;
  onOpenTask: (task: BackgroundTask) => void;
  inOverview?: boolean;
  /** Explicit auth partition skips reading the default credential owner. */
  owner?: string;
  /** Live session reader for copy-action fencing; defaults to the latest controlled `session` when `owner` is supplied, else the default store. */
  getCurrentSession?: () => Session | undefined;
  /** Live auth-partition reader for copy-action fencing; defaults to the latest controlled `owner`, else `credentialOwner`. */
  getCurrentOwner?: () => string;
  /** Explicit chat actions (`null` means no chat is attached) skips the default background-chat registry. */
  chat?: BackgroundChat | null;
  /** Replaces clipboard writes when copying a command. */
  onCopyCommand?: (command: string) => void | Promise<void>;
}

/** `opened`: the panel object as last asked for (lib/panel.ts): one naming a run opens that run's detail, every time it
 *  is asked for again (a new object), as cindy consumes focusTaskId once. */
export function BackgroundPanel(props: BackgroundPanelProps) {
  if (props.session !== undefined) {
    return <BackgroundPanelOwner {...props} resolvedSession={props.session ?? undefined} sessionControlled />;
  }
  return <DefaultSessionBackgroundPanel {...props} />;
}

function DefaultSessionBackgroundPanel(props: BackgroundPanelProps) {
  const session = useStore(() => getSession(props.mid, props.sid));
  return <BackgroundPanelOwner {...props} resolvedSession={session} sessionControlled={false} />;
}

type BackgroundPanelOwnerProps = BackgroundPanelProps & {
  resolvedSession: Session | undefined;
  sessionControlled: boolean;
};

function BackgroundPanelOwner(props: BackgroundPanelOwnerProps) {
  if (props.owner !== undefined) {
    return <BackgroundPanelView {...props} resolvedOwner={props.owner} ownerControlled />;
  }
  return <DefaultOwnerBackgroundPanel {...props} />;
}

function DefaultOwnerBackgroundPanel(props: BackgroundPanelOwnerProps) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, credentialOwner);
  return <BackgroundPanelView {...props} resolvedOwner={owner} ownerControlled={false} />;
}

type BackgroundPanelViewProps = BackgroundPanelOwnerProps & {
  resolvedOwner: string;
  ownerControlled: boolean;
};

function BackgroundPanelView({
  mid,
  sid,
  opened,
  onClose,
  resolvedSession: session,
  sessionControlled,
  resolvedOwner: owner,
  ownerControlled,
  getCurrentSession,
  getCurrentOwner,
  chat,
  onOpenPanel,
  onCopyCommand,
  renderCommandDetail,
}: BackgroundPanelViewProps) {
  const latestSession = useRef(session);
  latestSession.current = session;
  const latestOwner = useRef(owner);
  latestOwner.current = owner;
  const openPanelRef = useRef(onOpenPanel ?? openPanel);
  openPanelRef.current = onOpenPanel ?? openPanel;
  const readSession = () => (getCurrentSession ? getCurrentSession() : sessionControlled ? latestSession.current : getSession(mid, sid));
  const readOwner = () => (getCurrentOwner ? getCurrentOwner() : ownerControlled ? latestOwner.current : credentialOwner());
  const firstOwner = useRef(owner);
  const owned = firstOwner.current === owner;
  type Selection = { kind: "run"; id: string; fromRow: boolean }
    | { kind: "command"; task: BackgroundTask; cwd?: string; owner: string; manager?: string; fromRow: boolean };
  const asks = (p: BackgroundPanelObject, previous?: Selection | null): Selection | null => {
    if (p.run) return { kind: "run", id: p.run, fromRow: p.from === "overview" };
    if (!p.task) return null;
    const task = [...otherBackground(session), ...(session?.background_ended ?? [])].find((entry) => entry.id === p.task);
    const same = previous?.kind === "command" && previous.task.id === p.task && previous.owner === owner && previous.manager === session?.manager;
    return task ? { kind: "command", task, cwd: same ? previous.cwd : session?.cwd, owner, manager: session?.manager, fromRow: p.from === "overview" || Boolean(same && previous.fromRow) }
      : same ? previous : null;
  };
  const [asked, setAsked] = useState(opened);
  const [shown, setShown] = useState<Selection | null>(() => asks(opened));
  if (asked !== opened) {
    setAsked(opened);
    setShown(asks(opened, shown));
  }
  const back = useRef<{ kind: "run" | "command"; id: string; top: number } | null>(null);
  const listTop = useRef(0);
  const list = useRef<HTMLDivElement>(null);
  const container = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (shown || !back.current) return;
    const scroller = list.current?.querySelector<HTMLElement>("[data-background-list]");
    if (scroller) scroller.scrollTop = back.current.top;
    const attr = back.current.kind === "run" ? "data-bg-run" : "data-bg-task";
    const row = [...(list.current?.querySelectorAll<HTMLElement>(`[${attr}]`) ?? [])].find((el) => el.getAttribute(attr) === back.current!.id);
    (row?.querySelector<HTMLElement>("[data-bg-row-button]") ?? container.current?.querySelector<HTMLElement>("header button"))?.focus({ preventScroll: true });
    back.current = null;
  }, [shown]);
  // a run gone from the session (the node no longer lists it), or not there when asked for: the list, and the ask is
  // dropped, so the panel never jumps to it later
  const run = shown?.kind === "run" ? session?.workflows?.find((w) => w.task_id === shown.id) : undefined;
  const command = shown?.kind === "command" ? shown : undefined;
  const commandCurrent = Boolean(command && owned && session && command.owner === owner && command.manager === session.manager);
  const runningCommand = commandCurrent && command ? otherBackground(session).find((entry) => entry.id === command.task.id) : undefined;
  const endedCommand = commandCurrent && command ? session?.background_ended?.find((entry) => entry.id === command.task.id) : undefined;
  const currentCommand = endedCommand ?? runningCommand;
  // Keep the last task metadata when it leaves both lists; disappearance does not report its outcome.
  if (command && currentCommand && command.task !== currentCommand) setShown({ ...command, task: currentCommand });
  useEffect(() => {
    const retired = (shown?.kind === "run" && !run) || (command && !commandCurrent);
    if (retired) setShown(null);
    // A fallback list has no overview to return to. Clear the origin so ChatView shows the overview again and a row
    // opened from this list keeps the list's own Back behavior.
    if (opened.from === "overview" && (retired || !shown)) openPanelRef.current({ kind: "background", mid, sid });
  }, [shown, run, command, commandCurrent, opened.from, mid, sid]);
  useEffect(() => { if (!owned) onClose(); }, [owned, onClose]);
  const rememberList = () => { listTop.current = list.current?.querySelector<HTMLElement>("[data-background-list]")?.scrollTop ?? 0; };
  const goBack = () => {
    if (!shown) return;
    if (opened.from === "overview") { onClose(); return; }
    back.current = { kind: shown.kind, id: shown.kind === "run" ? shown.id : shown.task.id, top: listTop.current };
    setShown(null);
    openPanelRef.current({ kind: "background", mid, sid });
  };
  if (!owned) return null;
  const commandDetail: BackgroundCommandDetailViewProps | undefined = command && commandCurrent ? {
    mid,
    sid,
    cwd: command.cwd,
    task: currentCommand ?? command.task,
    running: Boolean(runningCommand && !endedCommand),
    focus: command.fromRow,
    owner: command.owner,
    manager: command.manager,
    onBack: goBack,
    onOpenFile: () => {
      const current = readSession();
      const path = (currentCommand ?? command.task).output_file;
      if (!path || readOwner() !== command.owner || !current || current.manager !== command.manager) return;
      openPanelRef.current({ kind: "file", mid, path, cwd: command.cwd });
    },
  } : undefined;
  return (
    <div ref={container} className="flex min-h-0 flex-1 flex-col" data-background-panel onKeyDown={(event) => {
      if (!commandCurrent || event.key !== "Escape" || event.defaultPrevented || document.querySelector("[role=menu],[role=dialog]")) return;
      // Preserve IME cancellation, including Safari's 229 sentinel, without also closing the outer panel.
      if (composing(event.nativeEvent)) { event.stopPropagation(); return; }
      event.preventDefault(); event.stopPropagation(); goBack();
    }}>
      <PanelHeader icon="list" title={t("sidepanel.background")} onClose={onClose} />
      {run ? (
        <WorkflowDetail
          key={run.task_id}
          run={run}
          mid={mid}
          sid={sid}
          focus={Boolean(shown?.fromRow)}
          onBack={goBack}
          onClose={onClose}
          chat={chat}
        />
      ) : command && commandDetail ? (
        renderCommandDetail ? (
          <Fragment key={JSON.stringify([mid, sid, command.task.id])}>{renderCommandDetail(commandDetail)}</Fragment>
        ) : (
          // Not the output file: a Monitor's arrives later, and the detail switches to it in place (#10852).
          <BackgroundCommandDetail key={JSON.stringify([mid, sid, command.task.id])} {...commandDetail} />
        )
      ) : (
        <div ref={list} className="flex min-h-0 flex-1 flex-col">
          <BackgroundList
            session={session}
            mid={mid}
            sid={sid}
            owner={owner}
            getCurrentSession={readSession}
            getCurrentOwner={readOwner}
            chat={chat}
            onCopyCommand={onCopyCommand}
            onOpenRun={(id) => {
              rememberList(); setShown({ kind: "run", id, fromRow: true });
            }}
            onOpenTask={(task) => {
              const current = readSession();
              if (owner !== readOwner() || !current || current.manager !== session?.manager) return;
              rememberList();
              setShown({ kind: "command", task, cwd: session?.cwd, owner, manager: session?.manager, fromRow: true });
              openPanelRef.current({ kind: "background", mid, sid, task: task.id });
            }}
          />
        </div>
      )}
    </div>
  );
}

export function BackgroundList({
  session,
  mid,
  sid,
  onOpenRun,
  onOpenTask,
  inOverview,
  owner: controlledOwner,
  getCurrentSession,
  getCurrentOwner,
  chat,
  onCopyCommand,
}: BackgroundListProps) {
  const ownerControlled = controlledOwner !== undefined || getCurrentOwner !== undefined;
  const owner = controlledOwner ?? (getCurrentOwner ? getCurrentOwner() : credentialOwner());
  const latestSession = useRef(session);
  latestSession.current = session;
  const latestOwner = useRef(owner);
  latestOwner.current = owner;
  const readOwner = () => (getCurrentOwner ? getCurrentOwner() : controlledOwner !== undefined ? latestOwner.current : credentialOwner());
  const readSession = () => (getCurrentSession ? getCurrentSession() : ownerControlled ? latestSession.current : getSession(mid, sid));
  const copyCommand = onCopyCommand ?? copyText;
  // an ended run leaves the list a day after it ended (recentWorkflows): the list's clock moves while something runs
  const busy = Boolean(session?.loop) || (session?.background?.length ?? 0) > 0 || (session?.workflows ?? []).some((w) => w.running);
  const now = useNow(60_000, busy);
  const { loop, running, tasks: allTasks, completed, completedTasks } = backgroundSections(session, now);
  const subagentIds = inOverview ? new Set(session?.subagents?.map((agent) => agent.id)) : undefined;
  const tasks = subagentIds ? allTasks.filter((task) => task.kind !== "agent" || !subagentIds.has(task.id)) : allTasks;
  const runningId = useId();
  const completedId = useId();
  const commandRow = (task: BackgroundTask, listedRunning: boolean) => {
    const status = backgroundStatus(task, listedRunning);
    const since = Date.parse(task.started_at ?? "");
    const took = backgroundDurationMs(task, status === "running", now);
    return (
      <TaskRow
        key={task.id}
        icon={taskIcon(task)}
        title={backgroundTitle(task)}
        status={status}
        meta={[<span key="status">{status ? taskStatusWord(status) : t("chat-activity.output_not_running")}</span>,
          ...(status === "running" && Number.isFinite(since) ? [<LiveDuration key="time" since={since} />]
            : took !== null ? [<span key="time" className="tabular-nums">{workDuration(took)}</span>] : [])]}
        command={task.command}
        onCopy={() => {
          const current = readSession();
          if (owner === readOwner() && current && current.manager === session?.manager && task.command) void copyCommand(task.command);
        }}
        onClick={opensDetail(task) ? () => onOpenTask(task) : undefined}
        data={{ "data-bg-task": task.id }}
      />
    );
  };
  if (!loop && !running.length && !tasks.length && !completed.length && !completedTasks.length) {
    if (inOverview) return null;
    return (
      <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center" data-background-empty>
        <Icon name="list" size={20} className="text-ink-3" />
        <p className="text-[12px] text-ink-3">{t("chat-activity.empty")}</p>
      </div>
    );
  }
  return (
    <div className={inOverview ? "pb-2" : "min-h-0 flex-1 overflow-y-auto px-1 pb-2"} data-background-list>
      {inOverview && <h3 className="px-2 pt-3 text-xs font-semibold text-ink-2">{t("sidepanel.background")}</h3>}
      {(loop || running.length > 0 || tasks.length > 0) && (
        <section aria-labelledby={runningId} data-background-section="running">
          <SectionHeader id={runningId} label={t("chat-activity.section_running")} />
          {loop && <LoopRow loop={loop} mid={mid} sid={sid} chat={chat} />}
          {running.map((w) => (
            <RunRow key={w.task_id} run={w} onOpen={onOpenRun} />
          ))}
          {tasks.map((task) => commandRow(task, true))}
        </section>
      )}
      {(completed.length > 0 || completedTasks.length > 0) && (
        <section aria-labelledby={completedId} data-background-section="completed">
          <SectionHeader id={completedId} label={t("chat-activity.section_completed")} />
          {completed.map((w) => (
            <RunRow key={w.task_id} run={w} onOpen={onOpenRun} />
          ))}
          {completedTasks.map((task) => commandRow(task, false))}
        </section>
      )}
    </div>
  );
}

function SectionHeader({ id, label }: { id: string; label: string }) {
  return (
    <h3 id={id} className="px-2 pt-3 pb-1 text-[11px] font-medium tracking-[0.5px] text-ink-3 uppercase">
      {label}
    </h3>
  );
}

/** The 20 px round kind mark at a row's start (and the detail bar's). */
function KindChip({ icon, className }: { icon: IconName; className?: string }) {
  return (
    <span className={cx("inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-field text-ink-2", className)} data-bg-kind={icon}>
      <Icon name={icon} size={12} />
    </span>
  );
}

/** The status mark before a row's title, as the task cards draw it (TaskCard): spinning while it runs, else the
 *  outcome's icon; only a failure is red. */
function RowStatus({ status }: { status: AgentStatus }) {
  return (
    <span className={cx("flex size-[13px] shrink-0 items-center justify-center", status === "failed" ? "text-red" : "text-ink-2")} data-bg-status={status}>
      {status === "running" ? <Spinner size={11} /> : <Icon name={status === "failed" ? "warning" : status === "stopped" ? "stop" : "check"} size={13} />}
    </span>
  );
}

/** How long it has run so far, counted each second in its own leaf (the list does not redraw for it). */
function LiveDuration({ since }: { since: number }) {
  const now = useNow(1000);
  return (
    <span className="tabular-nums" data-bg-elapsed>
      {workDuration(Math.max(0, now - since))}
    </span>
  );
}

/** The meta line's pieces joined with " · ". */
function joined(parts: ReactNode[]): ReactNode[] {
  return parts.flatMap((p, i) => (i ? [<span key={`sep${i}`} aria-hidden>{" · "}</span>, p] : [p]));
}

/** A button when it opens a run or command detail, else a plain row. The command belongs to the main row;
 *  copying is a separate action so it does not also open the output. */
function TaskRow({ icon, title, status, meta, hint, command, onCopy, onClick, trailing, data }: {
  icon: IconName;
  title: string;
  status?: AgentStatus;
  meta: ReactNode[];
  /** The title's hover text, when the row says less than there is. */
  hint?: string;
  command?: string;
  onCopy?: () => void;
  onClick?: () => void;
  trailing?: ReactNode;
  data?: Record<string, string>;
}) {
  const body = (
    <>
      <KindChip icon={icon} className="mt-px" />
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-1.5">
          {status && <RowStatus status={status} />}
          <span className="min-w-0 truncate text-[13px] leading-5 text-ink" title={hint}>
            {title}
          </span>
        </span>
        {meta.length > 0 && (
          <span className="mt-0.5 block truncate text-[11px] leading-4 text-ink-3" data-bg-meta>
            {joined(meta)}
          </span>
        )}
        {command && command !== title && (
          <span className="mt-0.5 block truncate font-mono text-[11px] leading-4 text-ink-3" title={command} data-bg-command>{command}</span>
        )}
      </span>
    </>
  );
  const row = "flex min-w-0 flex-1 items-start gap-2 rounded-[8px] px-2 py-1.5 text-left";
  return (
    <div className={cx("group flex flex-col rounded-lg transition-colors", (onClick || command) && "hover:bg-hover focus-within:bg-hover")} {...data}>
      <div className="flex items-start gap-1">
        {onClick ? (
          <button type="button" onClick={onClick} className={row} data-bg-row-button>
            {body}
          </button>
        ) : (
          <div className={row}>{body}</div>
        )}
        {command && (
          <span className="self-end pr-2 pb-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 pointer-coarse:opacity-100">
            <IconButton icon="copy" label={t("chat-activity.copy_command")} size={24} iconSize={12} onClick={onCopy} />
          </span>
        )}
        {trailing}
      </div>
    </div>
  );
}

/** A workflow run's row: it opens the run's detail. */
function RunRow({ run, onOpen }: { run: Workflow; onOpen: (task: string) => void }) {
  const status = workflowTaskStatus(run);
  const parts: ReactNode[] = [<span key="status">{taskStatusWord(status)}</span>];
  const progress = workflowProgress(run);
  if (progress) parts.push(<span key="agents" className="tabular-nums">{t("chat-subagentcard.workflow_progress_count", { done: progress.done, total: progress.total })}</span>);
  const start = Date.parse(run.started_at ?? "");
  const took = workflowDurationMs(run);
  if (run.running && Number.isFinite(start)) parts.push(<LiveDuration key="time" since={start} />);
  else if (took !== null) parts.push(<span key="time" className="tabular-nums">{workDuration(took)}</span>);
  if (run.tokens) parts.push(<span key="tokens">{t("chat-subagentcard.tokens", { value: compactTokens(run.tokens) })}</span>);
  if (run.calls) parts.push(<span key="calls">{t("chat-subagentcard.tool_uses", { count: run.calls })}</span>);
  return (
    <TaskRow
      icon="workflow"
      title={workflowName(run)}
      hint={run.summary}
      status={status}
      meta={parts}
      onClick={() => onOpen(run.task_id)}
      data={{ "data-bg-run": run.task_id }}
    />
  );
}

/** The /loop driving the Agent (Persona's own; cindy lists none): what it repeats, how often and when it wakes next, and
 *  a Stop that asks first (the loop ends by a message the chat sends, lib/tasks.ts STOP_LOOP_MESSAGE). */
function LoopRow({ loop, mid, sid, chat }: { loop: LoopInfo; mid: string; sid: string; chat?: BackgroundChat | null }) {
  if (chat !== undefined) return <LoopRowView loop={loop} chat={chat ?? undefined} />;
  return <DefaultLoopRow loop={loop} mid={mid} sid={sid} />;
}

function DefaultLoopRow({ loop, mid, sid }: { loop: LoopInfo; mid: string; sid: string }) {
  return <LoopRowView loop={loop} chat={useBackgroundChat(mid, sid)} />;
}

function LoopRowView({ loop, chat }: { loop: LoopInfo; chat: BackgroundChat | undefined }) {
  const now = useNow(30_000);
  const [confirming, setConfirming] = useState(false);
  // Stop goes through the chat page showing the session: none (the panel outlived it), no Stop rather than one that does nothing
  const task = loopTask(loop);
  const meta = [loopCadence(loop), loopNext(loop, now)].filter(Boolean).map((m, i) => <span key={i}>{m}</span>);
  const hint = [task, loop.reason].filter(Boolean).join("\n") || undefined;
  return (
    <div className="flex flex-col" data-bg-loop>
      <TaskRow
        icon="loop"
        title={task || t("chat-activity.loop")}
        hint={hint}
        meta={meta}
        trailing={
          chat && <span className="pt-1.5">
            <IconButton icon="square" label={t("chat-activity.stop_loop")} size={24} iconSize={12} active={confirming} onClick={() => setConfirming((c) => !c)} />
          </span>
        }
      />
      {confirming && chat && (
        <div role="group" aria-label={t("chat-activity.stop_loop")} className="mr-2 mb-1.5 ml-9 flex flex-col gap-2 rounded-[8px] bg-surface px-3 py-2.5 shadow-hairline" data-bg-loop-confirm>
          <p className="text-[12.5px] leading-relaxed text-ink-2">{t("chat-activity.stop_loop_explain")}</p>
          <div className="flex justify-end gap-2">
            <Button tone="quiet" size="sm" onClick={() => setConfirming(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              tone="danger"
              size="sm"
              onClick={() => {
                setConfirming(false);
                chat.stopLoop();
              }}
            >
              {t("chat-activity.stop_loop")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

/** cindy's WorkflowDetail: Back, the run's mark and name, then the run's own card (its head, where it is or what it was
 *  for, the progress bar and the phases) and "Show in chat" while the chat page can bring its call into view. */
function WorkflowDetail({ run, mid, sid, onBack, onClose, focus, chat }: { run: Workflow; mid: string; sid: string; onBack: () => void; onClose: () => void; focus: boolean; chat?: BackgroundChat | null }) {
  if (chat !== undefined) {
    return <WorkflowDetailView run={run} mid={mid} sid={sid} onBack={onBack} onClose={onClose} focus={focus} jumpToCall={chat?.jumpToCall} />;
  }
  return <DefaultWorkflowDetail run={run} mid={mid} sid={sid} onBack={onBack} onClose={onClose} focus={focus} />;
}

function DefaultWorkflowDetail({ run, mid, sid, onBack, onClose, focus }: { run: Workflow; mid: string; sid: string; onBack: () => void; onClose: () => void; focus: boolean }) {
  const jumpToCall = useBackgroundChat(mid, sid)?.jumpToCall;
  return <WorkflowDetailView run={run} mid={mid} sid={sid} onBack={onBack} onClose={onClose} focus={focus} jumpToCall={jumpToCall} />;
}

function WorkflowDetailView({ run, mid, sid, onBack, onClose, focus, jumpToCall }: { run: Workflow; mid: string; sid: string; onBack: () => void; onClose: () => void; focus: boolean; jumpToCall?: (call: string) => void }) {
  const bar = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (focus) bar.current?.querySelector<HTMLElement>("button")?.focus();
    // only as it opens
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const jump = jumpToCall
    ? (call: string) => {
        jumpToCall(call);
        // narrow, the panel covers the chat: the call shows only once it is gone
        if (mediaMatches(NARROW)) onClose();
      }
    : undefined;
  return (
    <div className="flex min-h-0 flex-1 flex-col" data-background-detail={run.task_id}>
      <div ref={bar} className="flex shrink-0 items-center gap-1.5 border-b border-line px-2 py-1.5">
        <IconButton icon="arrowLeft" label={t("chat-activity.back")} size={24} iconSize={14} onClick={onBack} />
        <KindChip icon="workflow" />
        <h3 className="min-w-0 flex-1 truncate text-[13px] leading-5 font-medium text-ink" title={run.summary}>
          {workflowName(run)}
        </h3>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
        <WorkflowCard run={run} scope={`${mid}/${sid}:`} onJump={jump} />
      </div>
    </div>
  );
}
