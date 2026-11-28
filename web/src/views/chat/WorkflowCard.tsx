// A Claude Code workflow run (#2935, #3434; docs/protocol/agent-states.md "Background workflows and Monitor") as a card.
// In the chat, where the Workflow call that launched it was (WorkflowCallCard, #5030 after cindy's AgentTaskCard): the
// shared task card (TaskCard.tsx), its title the run's name, its status word, "Workflow · tokens · tool uses · time"
// ("Running for …" while it runs), "{phase} · done/started agents" and a strip of one square per agent. In an Agent's
// chat it opens the Background tasks side panel at its run (#5421, lib/background-panel.ts); in the assistant's chat and
// a chat drawn inside another (the Team panel's worker) it opens in place to the run's phases instead. Like every tool
// call it sits in the folded work (super rule 2).
// In the Background tasks panel, as a run's detail (WorkflowCard, in WorkflowRun.tsx with what opens below the card), always open:
// - a head: the mark, its name, its status, "169/185+" agents done of those started (the "+" while it runs: the script
//   starts more as it goes), how long it has run;
// - while it runs "Now Verify: verify:chat-rich", once it has ended what it was for (and a failed run's error);
// - a progress bar in parts (done, running, failed, stopped), moving by transform: scaleX, never by width;
// - one block per phase: "done/started" (with "+" while more may start there), its agents in the order they started,
//   at most PHASE_ROWS of them with the quiet rest folded into "n more" (where they are left out), the earliest ones the
//   hub no longer lists said above the first row. The rows only say how each agent stands: a workflow agent never opens
//   as a conversation of its own.
// Things move only when a value changes, never on the first draw, and not at all with reduced motion: counts roll,
// the lit phase moves on and the one before it gets its ✓, a new agent fades in, a finished run's chip changes color.
import { memo, Suspense, type ReactNode } from "react";
import { t } from "../../i18n";
import { openBackgroundPanel } from "../../lib/background-panel";
import { lazyPage } from "../../lib/lazy-page";
import { useStore } from "../../lib/store";
import { compactTokens } from "../../lib/tokens";
import type { Workflow } from "../../lib/types";
import { workDuration, type ToolStep } from "../../lib/work-groups";
import {
  workflowDurationMs,
  workflowName,
  workflowProgress,
  workflowStatus,
  workflowStrip,
  workflowTaskStatus,
  type AgentRunState,
  type StripCell,
} from "../../lib/workflows";
import { Collapse, cx, EASE, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { MOVE_MS } from "../../ui/motion";
import { ActionRow } from "./ActionRow";
import { useChatContext } from "./chat-context";
import { useExpanded } from "./expanded";
import { RunningFor, TaskCard, taskStatusWord } from "./TaskCard";

// What opens below the card, and the Background tasks panel's detail, are WorkflowRun.tsx: the chat's first screen
// draws only the card, so they come with the tap that opens a run (or with the panel's own chunk).
const WorkflowRunInline = lazyPage(() => import("./WorkflowRun").then((m) => m.WorkflowRunInline));


/** A color, a highlight or a fill going to its new value: the design system's curve, MOVE_MS. */
export const MOVE: React.CSSProperties = { transitionDuration: `${MOVE_MS}ms`, transitionTimingFunction: EASE };

export const PART: Record<AgentRunState, string> = {
  done: "bg-ai",
  running: "bg-ai/35",
  failed: "bg-red",
  stopped: "bg-ink-3/45",
};

/** A strip square's fill: the bar's part colors; an agent the counts do not account for, the plain chip fill. */
const CELL: Record<StripCell["state"], string> = { ...PART, unknown: "bg-field" };

/** An agent's state in the task cards' words (Running, Completed, Failed, Stopped). */
const CELL_WORD: Record<AgentRunState, Parameters<typeof taskStatusWord>[0]> = { running: "running", done: "completed", failed: "failed", stopped: "stopped" };

/** The run a Workflow call launched, in the chat in that call's place (#3434) while the hub lists the run, drawn as a
 *  task card (#5030). In an Agent's own chat its head opens the Background tasks panel at the run, with the panel mark at
 *  its end; in the assistant's chat, or a chat drawn inside a panel, it opens in place to the run's phases (closed at
 *  first; what the reader chose stays for the page's life). The hub listing no such run (an older hub, a run over a day
 *  old, one not read yet): the call's row as it always was. */
export const WorkflowCallCard = memo(function WorkflowCallCard({ step, mid, sid, scope }: { step: ToolStep; mid: string; sid: string; scope: string }) {
  const call = step.use.tool_use_id ?? "";
  const { embedded } = useChatContext();
  const { run, agent } = useStore((s) => {
    const own = (s.sessions[mid] ?? []).find((x) => x.session_id === sid);
    const session = own ?? s.assistant?.conversations.find((c) => c.session_id === sid);
    return { run: session?.workflows?.find((w) => w.tool_use_id === call), agent: Boolean(own && !own.assistant) };
  });
  const inPanel = agent && !embedded;
  const [open, setOpen] = useExpanded(`${scope}workflow:${call}`);
  // the agents' clocks go each second only while their rows show
  const now = useNow(1000, Boolean(run?.running) && open && !inPanel);
  if (!run) return <ActionRow step={step} mid={mid} sid={sid} />;
  const name = workflowName(run);
  const ids = [step.use.id, ...(step.result ? [step.result.id] : [])].join(" ");
  return (
    <TaskCard
      icon="workflow"
      title={name}
      status={workflowTaskStatus(run)}
      meta={meta(run)}
      extra={<Overview run={run} />}
      onClick={
        inPanel
          ? () => openBackgroundPanel(mid, sid, run.task_id)
          : () => {
              void WorkflowRunInline.preload().catch(() => {}); // the tap that opens it starts its chunk
              setOpen((o) => !o);
            }
      }
      ariaLabel={inPanel ? t("chat-subagentcard.open_in_background", { name }) : t(open ? "lib-workflows.collapse_run" : "lib-workflows.expand_run", { name })}
      expanded={inPanel ? undefined : open}
      trailing={inPanel ? <Icon name="sidebar" size={14} className="mt-1 shrink-0 text-ink-3" /> : undefined}
      data={{ "data-workflow-call": workflowStatus(run), "data-tool-use": call, "data-items": ids, "data-workflow-opens": inPanel ? "panel" : "inline" }}
    >
      {!inPanel && (
        <Collapse open={open} lazy className="-mx-3">
          <div className="@container flex flex-col pt-2" data-workflow-inline>
            <Suspense fallback={null}>
              <WorkflowRunInline run={run} scope={scope} now={now} />
            </Suspense>
          </div>
        </Collapse>
      )}
    </TaskCard>
  );
});

/** The second line's pieces: what ran it, the tokens and tool uses its agents used (left out when the hub does not
 *  say), and how long it ran ("Running for …", each second, while it runs). */
function meta(run: Workflow): ReactNode[] {
  const parts: ReactNode[] = [<span key="harness">{t("chat-subagentcard.harness_workflow")}</span>];
  if (run.tokens) parts.push(<span key="tokens" data-task-tokens>{t("chat-subagentcard.tokens", { value: compactTokens(run.tokens) })}</span>);
  if (run.calls) parts.push(<span key="calls" data-task-calls>{t("chat-subagentcard.tool_uses", { count: run.calls })}</span>);
  const start = Date.parse(run.started_at ?? "");
  const took = workflowDurationMs(run);
  if (run.running && Number.isFinite(start)) parts.push(<RunningFor key="time" since={start} />);
  else if (took !== null) parts.push(<span key="time" className="tabular-nums" data-task-duration>{workDuration(took)}</span>);
  return parts;
}

/** Under the second line: "{phase} · done/started agents", then a square per agent, the first STRIP_CELLS of them. */
function Overview({ run }: { run: Workflow }) {
  const progress = workflowProgress(run);
  if (!progress) return null;
  const { cells, more } = workflowStrip(run);
  return (
    <>
      <span className="mt-0.5 block truncate text-[12px] leading-4 text-ink-3" data-workflow-progress-line>
        {progress.phase
          ? t("chat-subagentcard.workflow_progress_line", { phase: progress.phase, done: progress.done, total: progress.total })
          : t("chat-subagentcard.workflow_progress_count", { done: progress.done, total: progress.total })}
      </span>
      {cells.length > 0 && (
        <span aria-hidden className="mt-1 flex flex-wrap items-center gap-1" data-workflow-strip>
          {cells.map((c, i) => (
            <span
              key={i}
              data-cell={c.state}
              title={c.state === "unknown" ? c.label : [c.label, taskStatusWord(CELL_WORD[c.state])].filter(Boolean).join(" · ")}
              className={cx("size-2 rounded-[2px] transition-colors", CELL[c.state])}
              style={MOVE}
            />
          ))}
          {more > 0 && (
            <span className="text-[11px] leading-4 text-ink-3 tabular-nums" data-strip-more>
              +{more}
            </span>
          )}
        </span>
      )}
    </>
  );
}
