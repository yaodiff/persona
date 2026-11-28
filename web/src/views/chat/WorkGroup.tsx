// One folded execution summary while running or completed; manual expansion reveals its complete trace (#2602): with
// intermediate narration and flat action groups, with one rail and one latest-text slot (#10247).
import { lazy, memo, Suspense, useContext, useEffect, useLayoutEffect, useMemo, useState } from "react";
import { childMachineOf, childSessionOf, isChildSessionStep } from "../../lib/child-cards";
import { useSessionLive } from "../../lib/live";
import { useStore } from "../../lib/store";
import type { Item } from "../../lib/types";
import { actionSummary, groupTitle, nestedLineDefault, togglesLiveOverride, workLineOpen, workLineOpens, isAgentStep, isPlanStep, isRowStep, isWorkflowStep, planTodos, workDuration, type AgentStatus, type ToolStep, type WorkGroupNode, type WorkLineStyle, type WorkNode } from "../../lib/work-groups";
import type { WorkflowCalls } from "../../lib/workflows";
import type { Step } from "../../lib/turns";
import { Collapse, cx, Spinner, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ActionRow, CHEVRON_SLOT, STATUS_SLOT } from "./ActionRow";
import { LiveClockContext } from "./chat-context";
import { useFreshWords } from "./fresh-words";
import { inOpenedFold, openFoldRange, useExpanded, useLiveOverride } from "./expanded";
import { useReveal } from "./search-reveal";
import { stepWeight, useStepBudget, withinBudget } from "./work-budget";
import { nodeItemIds } from "../../lib/chat-search";
import { Answer } from "./SegmentView";
import { NoticeRow, Thumb } from "./Steps";
import { WorkflowCallCard } from "./WorkflowCard";
import { LiveThinking as LiveThinkingLine } from "./ThinkingCard";
import { t } from "../../i18n";

// Subagent cards sit inside the folded work (super rule 2), drawn only once a reader opens it: off the first screen.
const SubagentCard = lazy(() => import("./SubagentCard").then((m) => ({ default: m.SubagentCard })));
// the plan's card loads with an opened summary, off the first screen (#5156)
const PlanCard = lazy(() => import("./PlanCard").then((m) => ({ default: m.PlanCard })));
// Thought rows too (#5155), with their emphasis parser: only an opened summary draws them.
const ThinkingRow = lazy(() => import("./ThinkingRow").then((m) => ({ default: m.ThinkingRow })));
// A child session's card (#5120) the same way: drawn only once the work summary it sits in is opened.
const ChildSessionCard = lazy(() => import("./ChildSessionCard").then((m) => ({ default: m.ChildSessionCard })));
// A Codex visualization fragment (#10321) the same way: its runtime is a lazy chunk off the first screen.
const VisualizationCard = lazy(() => import("./VisualizationCard"));

export interface WorkProps {
  mid: string;
  sid: string;
  /** Keys the reader's open / closed state (expanded.ts) to this chat. */
  scope: string;
  expandFold: (f: Item) => Promise<boolean | void>;
  agentStatus: (s: ToolStep) => AgentStatus;
  /** The Workflow calls whose run the hub lists (#3434): those draw the run's card. */
  workflowCalls?: WorkflowCalls;
  /** An action group inside the manually opened summary; it shares the outer rail and indentation. */
  nested?: boolean;
  /** Inline edits belong only to the current action run inside the manually opened trace. */
  currentRun?: boolean;
  /** Inside an open group: whether its outermost group is live. A call without its result spins while it is, in every
   *  inner group, a finished one too (the run was over, the call may not be); nested groups keep their outer's. */
  rowsLive?: boolean;
  /** The exchange these steps are of (its segment's id): what of it the reader opened is kept under it. */
  segment?: string;
  /** Show every tool's original input/result in place in an opened supervision trace. */
  trace?: boolean;
  /** How its work lines open (work-groups.ts workLineOpen): "assistant" in a Persona conversation; "agents" when unset. */
  lineStyle?: WorkLineStyle;
  /** The exchange these steps are of is still running. */
  turnLive?: boolean;
  /** The step that draws the exchange's plan card (work-groups.ts planCardStep; "" for none): its other plan updates are
   *  rows. Unset: every plan update draws a card. */
  plan?: string;
  /** The dock RunningStatusBar already shows this foreground turn's elapsed clock, so the stream header keeps its
   *  expandable summary without rendering a second competing timer (#12380). */
  hideClock?: boolean;
  /** The outermost group's fold fetch failed and is waiting for manual retry: suppress the fold loading spinner. */
  foldFailed?: boolean;
}

/** A Workflow call that draws its run's card (the hub lists the run); one that does not is the row it always was. */
const workflowCard = (s: ToolStep, p: WorkProps) => isWorkflowStep(s) && Boolean(s.use.tool_use_id && p.workflowCalls?.known.has(s.use.tool_use_id));

/** A step drawn as an action row, sitting with the rows next to it. */
const drawnAsRow = (s: Step, p: WorkProps): s is ToolStep =>
  (p.trace && s.kind === "tool") || isRowStep(s) || (isWorkflowStep(s) && !workflowCard(s, p)) || (isPlanStep(s) && p.plan !== undefined && s.id !== p.plan);

const READ_TOOLS = new Set(["Read", "NotebookRead"]);
const EDIT_TOOLS = new Set(["Edit", "Write", "MultiEdit", "NotebookEdit"]);
const CMD_TOOLS = new Set(["Bash", "Shell", "shell", "exec_command", "PowerShell"]);
const SEARCH_TOOLS = new Set(["Grep", "Glob", "WebSearch", "WebFetch"]);
const SUBAGENT_TOOLS = new Set(["Agent", "Task"]);

/** Collect every tool call inside a list of work nodes or steps (including nested groups). */
function collectToolSteps(items: readonly (WorkNode | Step)[]): ToolStep[] {
  const out: ToolStep[] = [];
  for (const item of items) {
    if (item.kind === "group") out.push(...collectToolSteps(item.children));
    else if (item.kind === "step") {
      if (item.step.kind === "tool") out.push(item.step);
    } else if (item.kind === "tool") {
      out.push(item);
    }
  }
  return out;
}

/** Aggregate tool calls in a WorkGroup into a concise Chinese summary string (e.g. 「读 3 个文件、编辑 1 处」). */
export function summarizeWorkSteps(steps: readonly (WorkNode | Step)[]): string {
  const tools = collectToolSteps(steps);
  if (!tools.length) return "";
  let reads = 0;
  let edits = 0;
  let cmds = 0;
  let searches = 0;
  let agents = 0;
  let other = 0;
  for (const step of tools) {
    const name = step.use.tool_name ?? "";
    if (READ_TOOLS.has(name)) reads++;
    else if (EDIT_TOOLS.has(name)) edits++;
    else if (CMD_TOOLS.has(name)) cmds++;
    else if (SEARCH_TOOLS.has(name)) searches++;
    else if (SUBAGENT_TOOLS.has(name)) agents++;
    else other++;
  }
  const parts: string[] = [];
  if (reads) parts.push(t("chat-workgroup.summary_reads", { count: reads }));
  if (edits) parts.push(t("chat-workgroup.summary_edits", { count: edits }));
  if (cmds) parts.push(t("chat-workgroup.summary_cmds", { count: cmds }));
  if (searches) parts.push(t("chat-workgroup.summary_searches", { count: searches }));
  if (agents) parts.push(t("chat-workgroup.summary_agents", { count: agents }));
  if (other) parts.push(t("chat-workgroup.summary_other", { count: other }));
  return parts.join(t("chat-workgroup.summary_sep"));
}

/** Every fold under a group, nested ones too: opening it fetches their steps. */
function foldsOf(g: WorkGroupNode): Item[] {
  return g.children.flatMap((c) => (c.kind === "group" ? foldsOf(c) : c.step.kind === "fold" ? [c.step.item] : []));
}

/** Whether a group's subtree has any visible step outside unloaded folds. */
function hasNonFoldContent(nodes: readonly WorkNode[]): boolean {
  return nodes.some((c) => (c.kind === "group" ? hasNonFoldContent(c.children) : c.step.kind !== "fold"));
}

/** The screen's status line while this exchange is live (contract §3): a thinking line at the live group's end. Read
 *  here, not handed down: it changes about once a second while the agent works, and only this line shows it (#5417). */
function useThinkingNow(mid: string, sid: string) {
  const status = useSessionLive(mid, sid);
  return status?.thinking ? <LiveThinkingLine /> : null;
}

function LiveThinking({ mid, sid }: { mid: string; sid: string }) {
  return useThinkingNow(mid, sid);
}

/** `limit`: inside an open group whose budget (work-budget.ts) does not reach all of this one, how many of its steps it
 *  draws for now. */
export const WorkGroup = memo(function WorkGroup({ g, p, limit }: { g: WorkGroupNode; p: WorkProps; limit?: number }) {
  // an outer group whose folded steps the reader just opened: open under whatever key it has once they are in
  const style = p.lineStyle ?? "agents";
  const turnLive = Boolean(p.turnLive ?? g.live) && g.live && !p.nested;
  const key = `${p.scope}${g.id}`;
  const [remembered, setRemembered] = useExpanded(key, p.nested ? (!p.rowsLive || g.live) && nestedLineDefault(style) : inOpenedFold(p.scope, g.seq));
  // All conversation styles keep manual run toggles separately, then collapse on completion (#2602).
  const runToggle = togglesLiveOverride(style, turnLive);
  const [liveOverride, setLiveOverride] = useLiveOverride(key, runToggle);
  const setOpen = runToggle ? setLiveOverride : setRemembered;
  // A run just superseded by narration closes inside the trace the reader opened.
  const [wasLive, setWasLive] = useState(g.live);
  if (wasLive !== g.live) {
    setWasLive(g.live);
    if (p.nested && p.rowsLive && wasLive) setRemembered(false);
  }
  const actions = useMemo(() => actionSummary(g), [g]);
  const heading = p.nested && actions.text ? actions.text : groupTitle(g);
  const [loading, setLoading] = useState(false);
  // a fetch that failed (the hub restarting, the network): the group says so, and "Retry" runs it again
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const opens = workLineOpens(g, style, turnLive);
  const open = workLineOpen({ style, turnLive, remembered, liveOverride }) && opens;
  const rowsLive = p.nested ? Boolean(p.rowsLive) : g.live;
  const foldFailed = p.nested ? Boolean(p.foldFailed) : failed && !loading;
  const inside = useMemo(() => ({ ...p, nested: true, rowsLive, currentRun: g.live, foldFailed }), [p, rowsLive, g.live, foldFailed]);
  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (next) for (const f of foldsOf(g)) if (f.fold) openFoldRange(p.scope, f.seq, f.fold.through_seq);
  };
  // a long trace is drawn a screenful at a time (work-budget.ts): the outermost group keeps the budget, nested ones draw
  // what it leaves them; a jump to a call in it (jump.ts) draws it whole
  const budget = useStepBudget(p.nested ? undefined : key, open && !p.nested, stepWeight(g) - 1, g.live);
  const drawn = p.nested ? limit : budget.drawn;
  // a search hit inside it (search-reveal.ts): it opens, with every step drawn so the hit's row is there to land on,
  // and "Back to where you were" closes it again
  const ids = useMemo(() => nodeItemIds(g.children), [g.children]);
  useReveal(p.scope, ids, () => {
    budget.showAll();
    if (open || !opens) return;
    toggle();
    return () => setOpen(false);
  });
  // an open group's folded steps are fetched: opened just now, or remembered open when the chat comes back (#1996: it
  // stayed at "Loading N steps" then); once in, the folds are gone and this has nothing left to do. The outer group fetches
  // its nested groups' folds too.
  const folds = useMemo(() => (open && !p.nested ? foldsOf(g) : []), [open, p.nested, g]);
  const foldKey = folds.map((f) => f.id).join(" ");
  const expandFold = p.expandFold;
  useEffect(() => {
    // every fold is in: done. A run cut short by the last fold coming in (its key changed) does not get to say so, and
    // the header's spinner kept spinning (#1614)
    if (!foldKey) {
      setFailed(false);
      setLoading(false);
      return;
    }
    let live = true;
    setLoading(true);
    setFailed(false);
    folds
      // each fold that comes in runs this again with the rest: this run stops there
      .reduce<Promise<boolean | void>>(
        (done, f) =>
          done.then(() =>
            live
              ? Promise.resolve()
                  .then(() => expandFold(f))
                  .then((ok) => {
                    if (live && ok === false) setFailed(true);
                  })
                  .catch(() => live && setFailed(true))
              : undefined,
          ),
        Promise.resolve(),
      )
      .catch(() => live && setFailed(true))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
    // the folds by their ids: a new `g` with the same folds is not a new fetch
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [foldKey, expandFold, attempt]);
  const compactHeader = !p.nested && !g.live;
  const disclosure = opens && <Icon name="chevronRight" size={14} stroke={2.2} className="shrink-0 text-tool-ink transition-transform duration-150 motion-reduce:transition-none" style={{ transform: open ? "rotate(90deg)" : "none" }} />;
  const leadingIcon = p.nested && actions.text && actions.icon
    ? <Icon name={actions.icon} size={14} />
    : g.live
      ? <Spinner size={14} />
      : null;
  const summary = useMemo(() => {
    if (p.nested && actions.text) return "";
    if (!g.live) return "";
    const count = collectToolSteps(g.children).length;
    return count > 0 ? t("chat-workgroup.live_steps", { count }) : "";
  }, [g, p.nested, actions.text]);
  return (
    <div className="flex w-full min-w-0 flex-col" data-work-group={g.live ? "live" : "done"} data-group-id={g.id}>
      <button
        type="button"
        onClick={opens ? toggle : undefined}
        disabled={!opens}
        aria-expanded={opens ? open : undefined}
        title={p.nested && actions.text ? actions.text : g.clock ? t(g.clock === "run" ? "chat-workgroup.run_clock" : "chat-workgroup.exchange_clock") : undefined}
        // on a touch screen 40 tall, the margin taking back what it adds, so nothing around it moves
        className={cx("flex w-full min-w-0 items-center gap-1.5 py-[2px] text-left select-none [@media(pointer:coarse)]:-my-[7.5px] [@media(pointer:coarse)]:min-h-10", opens ? "cursor-pointer transition-opacity hover:opacity-80" : "cursor-default")}
      >
        {!compactHeader && leadingIcon && <span className="inline-flex h-[1lh] shrink-0 items-center text-tool-ink">{leadingIcon}</span>}
        <span className={cx("translate-y-[1px] text-[length:var(--chat-secondary)] text-tool-ink", compactHeader || !summary ? "min-w-0 truncate" : "shrink-0")}>{heading}</span>
        {compactHeader && disclosure}
        {summary && (
          <span className="min-w-0 translate-y-[1px] truncate text-[length:var(--chat-secondary)] text-ink-3" data-work-summary>
            · {summary}
          </span>
        )}
        {(loading || (!p.nested && g.live && g.startedAt !== undefined)) && <span className="flex-1" />}
        {loading && <Spinner size={11} />}
        {!p.nested && g.live && g.startedAt !== undefined && (p.hideClock ? <LiveClockPresence /> : <Elapsed since={g.startedAt} />)}
        {!compactHeader && disclosure}
      </button>
      {open && failed && !loading && (
        <div className="mt-1 flex items-center gap-2 pl-5 text-[12px] text-ink-3" role="status">
          {t("chat-workgroup.load_failed")}
          <button type="button" onClick={() => setAttempt((n) => n + 1)} className="touch-hit rounded-control px-1.5 py-0.5 font-medium whitespace-nowrap text-accent-ink hover:bg-hover">{t("chat-workgroup.retry")}</button>
        </div>
      )}
      {(!foldFailed || hasNonFoldContent(g.children) || (g.live && !g.children.some((c) => c.kind === "group" && c.live))) && (
        <Collapse open={open} lazy>
          <div className={cx("mt-1 flex min-w-0 flex-col gap-2 py-[2px]", p.nested ? "pl-3" : "ml-1.5 border-l-2 border-tool-rail pl-3")} data-work-detail>
            <WorkNodes nodes={g.children} p={inside} live={rowsLive} limit={drawn} />
            {/* once, in the innermost live group: a live timeline's last run is live too */}
            {g.live && !g.children.some((c) => c.kind === "group" && c.live) && <LiveThinking mid={p.mid} sid={p.sid} />}
          </div>
        </Collapse>
      )}
    </div>
  );
});

export type ClusterCategory = "read" | "search" | "command";

export interface WorkClusterNode {
  kind: "cluster";
  id: string;
  category: ClusterCategory;
  label: string;
  children: ToolStep[];
  hasError: boolean;
  hasRunning: boolean;
}

export type ClusteredWorkNode = WorkNode | WorkClusterNode;

const MIN_CLUSTER_SIZE = 3;

function clusterCategoryOf(node: WorkNode): ClusterCategory | null {
  if (node.kind !== "step" || node.step.kind !== "tool" || !isRowStep(node.step)) return null;
  const name = node.step.use.tool_name ?? "";
  if (READ_TOOLS.has(name)) return "read";
  if (SEARCH_TOOLS.has(name)) return "search";
  if (CMD_TOOLS.has(name)) return "command";
  return null;
}

function clusterLabel(category: ClusterCategory, count: number): string {
  switch (category) {
    case "read":
      return t("chat-workgroup.cluster_read", { count });
    case "search":
      return t("chat-workgroup.cluster_search", { count });
    case "command":
      return t("chat-workgroup.cluster_command", { count });
  }
}

/** Group >= 3 consecutive step nodes of the same clusterable category (read / search / command) into a collapsible cluster. */
export function clusterWorkChildren(children: readonly WorkNode[]): ClusteredWorkNode[] {
  const out: ClusteredWorkNode[] = [];
  let i = 0;
  while (i < children.length) {
    const cat = clusterCategoryOf(children[i]);
    if (!cat) {
      out.push(children[i++]);
      continue;
    }
    let j = i + 1;
    while (j < children.length && clusterCategoryOf(children[j]) === cat) j++;
    const count = j - i;
    if (count >= MIN_CLUSTER_SIZE) {
      const steps: ToolStep[] = [];
      for (let k = i; k < j; k++) {
        const node = children[k];
        if (node.kind === "step" && node.step.kind === "tool") steps.push(node.step);
      }
      out.push({
        kind: "cluster",
        id: `cluster-${cat}-${steps[0].id}`,
        category: cat,
        label: clusterLabel(cat, steps.length),
        children: steps,
        hasError: steps.some((s) => Boolean(s.result?.is_error)),
        hasRunning: steps.some((s) => !s.result),
      });
    } else {
      for (let k = i; k < j; k++) out.push(children[k]);
    }
    i = j;
  }
  return out;
}

/** Compact collapsible row for a cluster of consecutive read/search/command steps. */
export const ClusterRow = memo(function ClusterRow({ cluster, p, live }: { cluster: WorkClusterNode; p: WorkProps; live?: boolean }) {
  const running = Boolean(live && cluster.hasRunning);
  const hasError = cluster.hasError;
  const autoOpen = running || hasError;
  const [open, setOpen] = useState(autoOpen);
  useEffect(() => {
    if (autoOpen) setOpen(true);
  }, [autoOpen]);
  return (
    <div className="flex min-w-0 flex-col" data-work-cluster={cluster.category}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="group flex w-full min-w-0 items-center gap-1.5 rounded-[8px] px-2 py-[3px] text-left select-none transition-colors duration-150 hover:bg-tool-hover motion-reduce:transition-none [@media(pointer:coarse)]:min-h-10"
      >
        <span role="img" aria-label={running ? t("chat-actionrow.status_running") : hasError ? t("chat-actionrow.status_error") : t("chat-actionrow.status_done")} className={cx(STATUS_SLOT, hasError && !running && "text-red")}>
          {running ? <Spinner size={12} /> : <Icon name={hasError ? "x" : "layers"} size={13} />}
        </span>
        <span className="min-w-0 truncate text-[length:var(--chat-secondary)] font-medium text-tool-ink">{cluster.label}</span>
        <span className="flex-1" />
        {running && (
          <span className="shrink-0 rounded-[4px] bg-field px-1.5 py-[1px] text-submeta font-medium whitespace-nowrap text-ink-2" data-cluster-badge="running">
            {t("chat-workgroup.cluster_running")}
          </span>
        )}
        {hasError && (
          <span className="shrink-0 rounded-[4px] bg-red-tint px-1.5 py-[1px] text-submeta font-medium whitespace-nowrap text-red" data-cluster-badge="error">
            {t("chat-workgroup.cluster_error")}
          </span>
        )}
        <span aria-hidden className={CHEVRON_SLOT}>
          <Icon name={open ? "chevron" : "chevronRight"} size={13} stroke={2.2} />
        </span>
      </button>
      <Collapse open={open} lazy>
        <div className="mt-0.5 ml-3 flex min-w-0 flex-col border-l border-tool-rail/80 pl-2" data-cluster-children>
          {cluster.children.map((s) => (
            <ActionRow key={s.id} step={s} running={live && !s.result} mid={p.mid} sid={p.sid} trace={p.trace} inlineDiff={Boolean(p.nested && p.currentRun && p.rowsLive)} />
          ))}
        </div>
      </Collapse>
    </div>
  );
});

/** Nodes in order; tool calls next to each other sit together without a gap, like cindy's tool segments; each subagent
 *  is a card of its own, in the order it was launched (#5030). `fade`: the agent's words among them that just
 *  came in fade in (fresh-words.ts); not inside a group. `limit`: only the first that many steps for now (an open
 *  group's budget, work-budget.ts); absent, all of them. */
export function WorkNodes({ nodes, p, live, fade, limit }: { nodes: WorkNode[]; p: WorkProps; live?: boolean; fade?: boolean; limit?: number }) {
  const { shown, limits } = withinBudget(nodes, limit);
  const runs: (WorkNode | ToolStep[])[] = [];
  for (const n of shown) {
    const last = runs[runs.length - 1];
    if (n.kind === "step" && drawnAsRow(n.step, p)) {
      if (Array.isArray(last)) last.push(n.step);
      else runs.push([n.step]);
    } else {
      runs.push(n);
    }
  }
  return (
    <>
      {runs.map((run) =>
        Array.isArray(run) ? (
          <div key={run[0].id} className="flex min-w-0 flex-col">
            {run.map((s) => (
              <ActionRow key={s.id} step={s} running={live && !s.result} mid={p.mid} sid={p.sid} trace={p.trace} inlineDiff={Boolean(p.nested && p.currentRun && p.rowsLive)} />
            ))}
          </div>
        ) : run.kind === "group" ? (
          <WorkGroup key={run.id} g={run} p={p} limit={limits?.get(run)} />
        ) : (
          <StepView key={run.step.id} step={run.step} p={p} live={live} fade={fade} />
        ),
      )}
    </>
  );
}

/** Words that fade in if they just came in: each reads its own id in fresh-words.ts, so a new reply renders its own
 *  words, not the chat's. Memo: a streamed delta re-renders the segment, not these. */
const FreshAnswer = memo(function FreshAnswer({ item, mid, sid }: { item: Item; mid: string; sid: string }) {
  const fade = useFreshWords(mid, sid, item.id);
  return <Answer item={item} mid={mid} sid={sid} fade={fade} />;
});

/** A step on its own: in the open, or inside an open group. Memo: its props stay the same objects while the reply
 *  streams (SegmentView memoizes steps and p on the segment), so a delta skips every step of the turn. */
export const StepView = memo(function StepView({ step, p, live, fade }: { step: Step; p: WorkProps; live?: boolean; fade?: boolean }) {
  switch (step.kind) {
    case "tool":
      if (p.trace) return <ActionRow step={step} running={live && !step.result} mid={p.mid} sid={p.sid} trace />;
      if (isChildSessionStep(step)) return <Suspense fallback={null}><ChildSessionCard mid={childMachineOf(step, p.mid)} sid={childSessionOf(step)} parentMid={p.mid} parentSid={p.sid} scope={p.scope} /></Suspense>;
      if (isAgentStep(step)) return <Suspense fallback={null}><SubagentCard step={step} status={p.agentStatus(step)} mid={p.mid} sid={p.sid} scope={p.scope} /></Suspense>;
      if (isPlanStep(step)) return <Suspense fallback={null}><PlanCard id={step.id} todos={planTodos(step)} live={live} /></Suspense>;
      if (workflowCard(step, p)) return <WorkflowCallCard step={step} mid={p.mid} sid={p.sid} scope={p.scope} />;
      return <ActionRow step={step} running={live && !step.result} mid={p.mid} sid={p.sid} />;
    case "thinking":
      return <Suspense fallback={null}><ThinkingRow item={step.item} scope={p.scope} /></Suspense>;
    case "text":
      return fade ? <FreshAnswer item={step.item} mid={p.mid} sid={p.sid} /> : <Answer item={step.item} mid={p.mid} sid={p.sid} />;
    case "notice":
      return <NoticeRow item={step.item} mid={p.mid} sid={p.sid} />;
    case "image":
      return (
        <div className="py-1">
          <Thumb item={step.item} mid={p.mid} sid={p.sid} />
        </div>
      );
    case "visualization":
      return (
        <div className="py-1">
          <Suspense fallback={null}><VisualizationCard item={step.item} mid={p.mid} sid={p.sid} /></Suspense>
        </div>
      );
    case "fold":
      if (p.foldFailed) return null;
      return (
        <div className="flex h-7 items-center gap-2 px-2 text-[13px] text-tool-ink">
          <Spinner size={11} /> {t("chat-workgroup.loading_steps", { count: step.item.fold?.items ?? 0 })}
        </div>
      );
  }
});

/** Reports a live group's presence to LiveClockContext without rendering a duplicate elapsed clock (#12380). */
function LiveClockPresence() {
  const count = useContext(LiveClockContext);
  useLayoutEffect(() => {
    count?.(1);
    return () => count?.(-1);
  }, [count]);
  return null;
}

/** A live group's clock, every 500 ms; its own leaf, so the ticking redraws nothing else. While it shows, the running
 *  bar shows no time of its own (#3534). */
function Elapsed({ since }: { since: number }) {
  // stands still with the hub out of reach (the running bar says so): nothing can be known to go on
  const now = useNow(500, useStore((s) => s.conn === "online"));
  const count = useContext(LiveClockContext);
  // before paint: the bar's time goes in the same frame this one comes in
  useLayoutEffect(() => {
    count?.(1);
    return () => count?.(-1);
  }, [count]);
  return <span data-work-elapsed className="shrink-0 font-mono text-[12px] text-tool-ink tabular-nums">{workDuration(Math.max(0, now - since))}</span>;
}
