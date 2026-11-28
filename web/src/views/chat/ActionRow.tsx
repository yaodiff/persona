// One tool call in a work group (cindy's AgentActionRow): [status][verb][what it worked on][+N -N][›]. A command and
// the like open in place (what went in, what came out, in full); an edit opens the changes side panel; a read opens
// the file in the side panel.
import { Fragment, lazy, memo, Suspense, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useExpanded } from "./expanded";
import { diffLines, type Op } from "../../lib/diff-lines";
import { fileName, toolFile } from "../../lib/files";
import { bytes } from "../../lib/format";
import { openPanel, useReadOnlyFile, type Panel } from "../../lib/panel";
import { getSession } from "../../lib/store";
import { taskBadge, type TaskIndex } from "../../lib/tasks";
import { ownEntry, personaToolVerb, SUBJECT_KEYS, toolChip, toolDisplayName, toolLabel } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { toolDurationLabel, toolDurationMs, type ToolStep } from "../../lib/work-groups";
import { cx, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { parseAnsi } from "../../lib/ansi";
import { NARROW, useMedia } from "../../ui/media-query";
import { patchDiffs, withoutCutMark } from "../../lib/tool-detail";
import { changePath, editLines } from "../../lib/turn-changes";
import { useChatContext } from "./chat-context";
import { useFullText } from "./FullText";
import { hasStepDetail, showsResult } from "./step-detail-kind";
import { lazyPage } from "../../lib/lazy-page";
import { useReveal } from "./search-reveal";
import { Thumb } from "./Steps";
import { t } from "../../i18n";

// A plan or questions are drawn only once their step is opened (step-detail-kind.ts says which steps have one), so the
// detail's chunk loads then, or as the pointer or focus reaches such a row: not on every first screen.
const StepDetail = lazyPage(() => import("./StepDetail").then((m) => m.StepDetail));
const preloadStepDetail = () => void StepDetail.preload().catch(() => {});
// L3 per-step attribution badges load lazily only when a step with telemetry is expanded (#13054).
const StepTelemetry = lazy(() => import("./StepTelemetry").then((m) => ({ default: m.StepTelemetry })));

/** The row's chrome, shared with the thought rows (ThinkingRow.tsx) so a mixed list reads as one (cindy's
 *  activityRowChrome): radius, padding, the hover lift. On a touch screen a row is a finger tall (40). */
export const ROW_CHROME = "flex w-full min-w-0 gap-1.5 rounded-[8px] px-2 py-[3px] [@media(pointer:coarse)]:min-h-10 text-left";
export const ROW_HOVER = "group select-none transition-colors duration-150 hover:bg-tool-hover motion-reduce:transition-none";
const ROW = `${ROW_CHROME} items-center ${ROW_HOVER}`;
export const CHEVRON_SLOT = "ml-auto flex size-[18px] shrink-0 items-center justify-center rounded-[8px] text-tool-ink transition-colors duration-150 group-hover:bg-hover-2 motion-reduce:transition-none";
export const STATUS_SLOT = "inline-flex h-[18px] w-4 shrink-0 items-center justify-center text-tool-ink";

const COMMAND_TOOLS = new Set(["Bash", "exec_command", "shell", "PowerShell"]);
const FILE_TOOLS = new Set(["Read", "Edit", "Write", "MultiEdit", "NotebookEdit"]);
export const DIFF_TOOLS = new Set(["Edit", "Write", "MultiEdit", "NotebookEdit"]);

/** cindy's verbAggregator (its Chinese wording is the zh-Hans text). */
const VERB: Record<string, string> = {
  Edit: t("chat-actionrow.verb_edit"),
  MultiEdit: t("chat-actionrow.verb_edit"),
  NotebookEdit: t("chat-actionrow.verb_edit"),
  Write: t("chat-actionrow.verb_create"),
  Bash: t("chat-actionrow.verb_run"),
  exec_command: t("chat-actionrow.verb_run"),
  shell: t("chat-actionrow.verb_run"),
  Read: t("chat-actionrow.verb_read"),
  TodoWrite: t("chat-actionrow.verb_update"),
  Glob: t("chat-actionrow.verb_search"),
  Grep: t("chat-actionrow.verb_search"),
  LS: t("chat-actionrow.verb_list"),
  WebFetch: t("chat-actionrow.verb_visit"),
  WebSearch: t("chat-actionrow.verb_visit"),
};

/** Claude Code's newer tools (iOS ToolPresentation.knownVerb, #2986): what each did, then what it was about. */
const DONE: Record<string, string> = {
  TaskCreate: t("chat-actionrow.verb_task_create"),
  TaskUpdate: t("chat-actionrow.verb_task_update"),
  TaskList: t("chat-actionrow.verb_task_list"),
  TaskGet: t("chat-actionrow.verb_task_get"),
  TaskStop: t("chat-actionrow.verb_task_stop"),
  KillShell: t("chat-actionrow.verb_task_stop"),
  KillBash: t("chat-actionrow.verb_task_stop"),
  TaskOutput: t("chat-actionrow.verb_task_output"),
  BashOutput: t("chat-actionrow.verb_task_output"),
  SendMessage: t("chat-actionrow.verb_send_message"),
  ListAgents: t("chat-actionrow.verb_list_agents"),
  ScheduleWakeup: t("chat-actionrow.verb_schedule_wakeup"),
  CronCreate: t("chat-actionrow.verb_cron_create"),
  CronList: t("chat-actionrow.verb_cron_list"),
  CronDelete: t("chat-actionrow.verb_cron_delete"),
  RemoteTrigger: t("chat-actionrow.verb_remote_trigger"),
  Monitor: t("chat-actionrow.verb_monitor"),
  LSP: t("chat-actionrow.verb_lsp"),
  EnterWorktree: t("chat-actionrow.verb_enter_worktree"),
  ExitWorktree: t("chat-actionrow.verb_exit_worktree"),
  SendUserFile: t("chat-actionrow.verb_send_file"),
  Artifact: t("chat-actionrow.verb_artifact"),
  PushNotification: t("chat-actionrow.verb_push_notification"),
  ReportFindings: t("chat-actionrow.verb_report_findings"),
  ToolSearch: t("chat-actionrow.verb_load_tools"),
  WaitForMcpServers: t("chat-actionrow.verb_wait_mcp"),
  ListMcpResourcesTool: t("chat-actionrow.verb_list_mcp_resources"),
  ReadMcpResourceTool: t("chat-actionrow.verb_read_mcp_resource"),
  Skill: t("chat-actionrow.verb_skill"),
};

const PARAM_MAX = 60;

export function truncateParam(text: string, max = PARAM_MAX): string {
  const t = text.trim();
  return t.length <= max ? t : `${t.slice(0, Math.max(0, max - 3)).trimEnd()}...`;
}

const str = (v: unknown) => (typeof v === "string" ? v.trim() : "");
/** A command call's command (Codex sends it as an argv array). */
const commandOf = (inp: Record<string, unknown>) => (Array.isArray(inp.command) ? (inp.command as unknown[]).join(" ") : str(inp.command) || str(inp.cmd));

export interface ActionPresentation {
  /** Localized action verb, except for commands which lead directly with their command. */
  verb?: string;
  param?: { text: string; title?: string };
  /** A file the call worked on: drawn as a chip with its name. */
  file?: string;
}

/** How a call reads in its row (cindy's describeToolUse + extractDisplayParam). */
export function presentAction(use: Item): ActionPresentation {
  const name = use.tool_name ?? "";
  const inp = (use.tool_input ?? {}) as Record<string, unknown>;
  if (COMMAND_TOOLS.has(name)) {
    const cmd = commandOf(inp);
    const desc = str(inp.description);
    return { param: { text: cmd || desc || t("chat-actionrow.verb_run_command"), title: desc || cmd || undefined } };
  }
  if (FILE_TOOLS.has(name)) {
    const path = toolFile(name, inp)?.path ?? "";
    return { verb: VERB[name], ...(path ? { file: path, param: { text: truncateParam(fileName(path)), title: path } } : {}) };
  }
  if (name === "Grep" || name === "Glob") {
    const subject = [str(inp.pattern), str(inp.path) || str(inp.glob)].filter(Boolean).join(" · ");
    return { verb: VERB[name], param: { text: truncateParam(subject), title: subject } };
  }
  if (name === "WebFetch") return { verb: VERB[name], param: { text: truncateParam(str(inp.url)), title: str(inp.url) } };
  if (name === "WebSearch") return { verb: VERB[name], param: { text: truncateParam(str(inp.query)) } };
  if (name === "LS") return { verb: VERB[name], param: { text: truncateParam(str(inp.path)) } };
  if (name.startsWith("mcp__")) {
    // Persona's own tools read as what they do ("Read Agent chat"), then what they were given
    const own = personaToolVerb(name);
    if (own) {
      const what = [own.what, toolChip(use)].filter(Boolean).join(" · ");
      return { verb: own.verb, ...(what ? { param: { text: truncateParam(what), title: name } } : {}) };
    }
    // another server's tool in words ("GitHub · Create issue"), then what it was given
    const what = [toolDisplayName(name), toolChip(use)].filter(Boolean).join(" · ");
    return { verb: t("chat-actionrow.verb_call"), param: { text: truncateParam(what), title: name } };
  }
  const chip = toolChip(use);
  const done = ownEntry(DONE, name);
  if (done) return { verb: done, ...(chip ? { param: { text: truncateParam(chip), title: chip } } : {}) };
  return { verb: ownEntry(VERB, name) ?? t("chat-actionrow.verb_call"), param: { text: truncateParam(chip ? `${toolLabel(name)} · ${chip}` : toolLabel(name)), title: chip || undefined } };
}

// ── diffs ──

/** A line diff of an edit's old and new text (lib/compare.ts's Myers diff); too big to diff: all out, all in. Its
 *  lines are lib/turn-changes.ts editLines' (a final newline ends the last line where both sides allow it), which the
 *  changed-files card and the hub's folds count the same way. */
export function lineDiff(oldText: string, newText: string): Op[] {
  const [a, b] = editLines(oldText, newText);
  return diffLines(a, b) ?? [...a.map((text) => ({ kind: "del" as const, text })), ...b.map((text) => ({ kind: "add" as const, text }))];
}

/** An edit's changes, one diff per change (MultiEdit has several; Write is all new). */
export function editDiffs(use: Item): Op[][] | null {
  const inp = (use.tool_input ?? {}) as Record<string, unknown>;
  switch (use.tool_name) {
    case "Edit":
      // Codex: the patch as a unified diff (the hub maps a file change to an Edit with `diff`); Claude: old / new
      if (typeof inp.diff === "string") {
        // the hub sends an empty diff for a change with no text (an empty new file, a pure move): open the file instead
        const d = patchDiffs(inp.diff, str(inp.change));
        return d.length ? d : null;
      }
      return typeof inp.old_string === "string" || typeof inp.new_string === "string" ? [lineDiff(String(inp.old_string ?? ""), String(inp.new_string ?? ""))] : null;
    case "Write":
      return typeof inp.content === "string" ? [lineDiff("", inp.content)] : null;
    case "MultiEdit":
      return Array.isArray(inp.edits) ? (inp.edits as Record<string, unknown>[]).map((e) => lineDiff(String(e.old_string ?? ""), String(e.new_string ?? ""))) : null;
    case "NotebookEdit":
      return typeof inp.new_source === "string" ? [lineDiff("", inp.new_source)] : null;
  }
  return null;
}

/** Format diff blocks as unified diff text (`+`/`-`/` ` prefixed). */
export function formatUnifiedDiff(diffs: Op[][]): string {
  return diffs
    .map((block) =>
      block
        .map((l) => `${l.kind === "add" ? "+" : l.kind === "del" ? "-" : " "}${l.text}`)
        .join("\n"),
    )
    .join("\n@@\n");
}

export function diffStats(diffs: Op[][]): { add: number; del: number } {
  let add = 0;
  let del = 0;
  for (const d of diffs) for (const l of d) l.kind === "add" ? add++ : l.kind === "del" && del++;
  return { add, del };
}

/** What a call opens to in place: its input as text. */
export function inlineInput(use: Item): string {
  const name = use.tool_name ?? "";
  const inp = (use.tool_input ?? {}) as Record<string, unknown>;
  if (COMMAND_TOOLS.has(name)) {
    return [commandOf(inp), str(inp.cwd) && `cwd: ${str(inp.cwd)}`].filter(Boolean).join("\n");
  }
  if (name === "Grep") return [`pattern: ${str(inp.pattern)}`, ...["path", "glob", "type", "output_mode"].filter((k) => str(inp[k])).map((k) => `${k}: ${str(inp[k])}`)].join("\n");
  if (name === "Glob") return str(inp.path) ? `${str(inp.pattern)}\nin: ${str(inp.path)}` : str(inp.pattern);
  if (name === "WebFetch") return [str(inp.url), str(inp.prompt)].filter(Boolean).join("\n");
  if (name === "WebSearch") return str(inp.query);
  if (name === "Monitor") return str(inp.command) || str((inp.ws as Record<string, unknown> | undefined)?.url);
  return argumentLines(inp);
}

/** Any other call's input as `key: value` lines, not a JSON dump (iOS ToolArgumentsDetail, #2986): the telling fields
 *  first, then the rest by name; text as written, lists of words joined, anything nested as one line of JSON. */
export function argumentLines(inp: Record<string, unknown>): string {
  const keys = [...SUBJECT_KEYS.filter((k) => Object.hasOwn(inp, k)), ...Object.keys(inp).filter((k) => !SUBJECT_KEYS.includes(k)).sort()];
  const shown = (v: unknown): string => {
    if (typeof v === "string") return v.trim();
    if (v === null) return "null";
    if (Array.isArray(v) && v.every((x) => x === null || typeof x !== "object")) return v.map(String).join(", ");
    if (typeof v === "object") return Object.keys(v as object).length ? JSON.stringify(v) : "";
    return String(v);
  };
  return keys.flatMap((k) => (shown(inp[k]) ? [`${k}: ${shown(inp[k])}`] : [])).join("\n");
}

export type ActionRowPanel = Extract<Panel, { kind: "changes" | "file" }>;

export interface ActionRowBadge {
  text: string;
  failed?: boolean;
}

export interface ActionRowProps {
  step: ToolStep;
  running?: boolean;
  mid?: string;
  sid?: string;
  trace?: boolean;
  inlineDiff?: boolean;
  /** Explicit working directory (`null` for none) skips default `getSession` lookup. */
  cwd?: string | null;
  /** Explicit background task index skips `ChatContext` lookup for the row's task badge. */
  tasks?: TaskIndex;
  /** Explicit background task badge (`null` for none) skips `ChatContext` and `taskBadge` lookup. */
  taskBadge?: ActionRowBadge | null;
  /** Explicit expansion scope skips `ChatContext` lookup for `expanded.ts` and `search-reveal.ts`. */
  scope?: string;
  /** Controlled expanded state skips default-panel expansion memory (`expanded.ts`) and search reveal (`search-reveal.ts`). */
  expanded?: boolean;
  /** Called with the next expanded state when an in-place or trace row is toggled. */
  onExpandedChange?: (expanded: boolean) => void;
  /** Controlled panel handler for edit (`changes`) and file (`file`) rows, replacing default `openPanel`. */
  onOpenPanel?: (panel: ActionRowPanel) => void;
  /** Controlled full-text loader for clipped results (`result.full_size`), replacing default `useFullText` / `fullItem`. */
  loadFullText?: (item: Item) => Promise<string>;
  /** Controlled renderer for attached tool images, replacing default `Thumb`. */
  renderImage?: (item: Item, index: number) => ReactNode;
}

function useControlledFullText(
  item: Item | undefined,
  loadFullText: (item: Item) => Promise<string>,
): { text: string; more: ReactNode } {
  const key = item?.full_size ? JSON.stringify([item.id, item.text, item.full_size, item.source_fingerprint]) : "";
  const current = useRef(key);
  current.current = key;
  const loadRef = useRef(loadFullText);
  loadRef.current = loadFullText;
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const pending = useRef<{ key: string; promise: Promise<void> } | null>(null);
  const [whole, setWhole] = useState<{ key: string; text: string } | null>(null);
  const [status, setStatus] = useState<{ key: string; state: "idle" | "loading" | "failed" }>();
  const state = status?.key === key ? status.state : "idle";
  const raw = item?.text ?? "";
  const size = item?.full_size;

  const load = () => {
    if (!item || !size || whole?.key === key || pending.current?.key === key) return;
    setStatus({ key, state: "loading" });
    const promise: Promise<void> = Promise.resolve()
      .then(() => loadRef.current(item))
      .then((text) => {
        if (!mounted.current || pending.current?.promise !== promise) return;
        if (current.current === key) {
          setWhole({ key, text });
          setStatus({ key, state: "idle" });
        } else {
          setStatus((prev) => (prev?.key === key ? undefined : prev));
        }
      })
      .catch(() => {
        if (!mounted.current || pending.current?.promise !== promise) return;
        if (current.current === key) setStatus({ key, state: "failed" });
        else setStatus((prev) => (prev?.key === key ? undefined : prev));
      })
      .finally(() => {
        if (pending.current?.promise === promise) pending.current = null;
      });
    pending.current = { key, promise };
  };

  if (whole?.key === key) return { text: whole.text, more: null };
  if (!size) return { text: raw, more: null };
  const loading = state === "loading";
  return {
    text: withoutCutMark(raw),
    more: (
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          if (!loading) load();
        }}
        disabled={loading}
        aria-busy={loading || undefined}
        className={cx(
          "mt-1.5 flex h-7 w-fit items-center gap-1.5 rounded-control px-1 font-sans text-meta font-medium whitespace-nowrap transition-colors hover:bg-hover disabled:opacity-60",
          state === "failed" ? "text-red" : "text-accent-ink",
        )}
        data-full-text={state}
      >
        {loading ? <Spinner size={12} /> : <Icon name={state === "failed" ? "retry" : "external"} size={12} />}
        {loading ? t("chat-fulltext.loading") : state === "failed" ? t("chat-fulltext.failed") : t("chat-fulltext.show_all", { size: bytes(size) })}
      </button>
    ),
  };
}

export const ActionRow = memo(function ActionRow(props: ActionRowProps) {
  const needsTasks = props.taskBadge === undefined && props.tasks === undefined;
  const needsScope = props.expanded === undefined && props.scope === undefined;
  if (needsTasks || needsScope) return <ContextActionRow {...props} />;
  return <ActionRowExpansion {...props} tasks={props.tasks} scope={props.scope ?? ""} />;
});

function ContextActionRow(props: ActionRowProps) {
  const ctx = useChatContext();
  return <ActionRowExpansion {...props} tasks={props.tasks ?? ctx.tasks} scope={props.scope ?? ctx.scope} />;
}

interface ActionRowResolvedContextProps extends ActionRowProps {
  scope: string;
}

function ActionRowExpansion(props: ActionRowResolvedContextProps) {
  if (props.expanded !== undefined) {
    return (
      <ActionRowFullText
        {...props}
        open={props.expanded}
        onToggle={() => props.onExpandedChange?.(!props.expanded)}
      />
    );
  }
  return <DefaultExpandedActionRow {...props} />;
}

function DefaultExpandedActionRow(props: ActionRowResolvedContextProps) {
  const { step, scope, trace, onExpandedChange } = props;
  const { use, result } = step;
  const name = use.tool_name ?? "";
  // kept by the chat, not the row (expanded.ts): a row drawn again (a group regrouped, a line rebuilt) stays as the reader left it
  const [open, setOpen] = useExpanded(`${scope}act:${use.id}`);
  const inPlace = Boolean(trace || !FILE_TOOLS.has(name));
  const ids = useMemo(() => [use.id, ...(result ? [result.id] : [])], [use.id, result]);
  const changeRef = useRef(onExpandedChange);
  changeRef.current = onExpandedChange;
  // a search hit in its input or output (search-reveal.ts): its detail opens in place
  useReveal(scope, ids, () => {
    if (!inPlace || open) return;
    setOpen(true);
    changeRef.current?.(true);
    return () => {
      setOpen(false);
      changeRef.current?.(false);
    };
  });
  const onToggle = () => {
    if (onExpandedChange) {
      let next = !open;
      setOpen((o) => {
        next = !o;
        return next;
      });
      onExpandedChange(next);
    } else {
      setOpen((o) => !o);
    }
  };
  return <ActionRowFullText {...props} open={open} onToggle={onToggle} />;
}

interface ActionRowExpansionProps extends ActionRowResolvedContextProps {
  open: boolean;
  onToggle: () => void;
}

function ActionRowFullText(props: ActionRowExpansionProps) {
  if (props.loadFullText) return <ControlledFullTextActionRow {...props} loadFullText={props.loadFullText} />;
  return <DefaultFullTextActionRow {...props} />;
}

function DefaultFullTextActionRow(props: ActionRowExpansionProps) {
  const full = useFullText(props.step.result, props.mid ?? "", props.sid ?? "");
  return <ActionRowView {...props} fullText={full.text} fullMore={full.more} />;
}

function ControlledFullTextActionRow(props: ActionRowExpansionProps & { loadFullText: (item: Item) => Promise<string> }) {
  const full = useControlledFullText(props.step.result, props.loadFullText);
  return <ActionRowView {...props} fullText={full.text} fullMore={full.more} />;
}

interface ActionRowViewProps extends ActionRowExpansionProps {
  fullText: string;
  fullMore: ReactNode;
}

function ActionRowView({
  step,
  running,
  mid = "",
  sid = "",
  trace,
  inlineDiff,
  cwd,
  tasks,
  taskBadge: controlledBadge,
  open,
  onToggle,
  onOpenPanel,
  renderImage,
  fullText,
  fullMore,
}: ActionRowViewProps) {
  const { use, result, images } = step;
  const readOnly = useReadOnlyFile();
  const name = use.tool_name ?? "";
  const narrow = useMedia(NARROW);
  const p = useMemo(() => presentAction(use), [use]);
  const diffs = useMemo(() => (DIFF_TOOLS.has(name) ? editDiffs(use) : null), [name, use]);
  const stats = diffs ? diffStats(diffs) : null;
  const inPlace = trace || (readOnly && Boolean(diffs)) || !FILE_TOOLS.has(name);
  const call = use.tool_use_id;
  const bg =
    controlledBadge !== undefined
      ? controlledBadge
      : call && tasks && (tasks.started.has(call) || tasks.notes.has(call))
        ? taskBadge(tasks.notes.get(call) ?? [])
        : null;
  const error = Boolean(result?.is_error);
  // the call's own elapsed time (#8163): a quiet suffix on wide rows, the row's hover title on compact layouts
  const duration = toolDurationLabel(step);
  const rawDurationMs = toolDurationMs(step);
  const hasTelemetry =
    (rawDurationMs !== null && rawDurationMs > 0) ||
    Boolean(use.usage?.length || result?.usage?.length || (typeof use.ttft_ms === "number" && use.ttft_ms >= 0));
  const stepItems = useMemo(() => [use, result], [use, result]);

  const resolveCwd = () => (cwd !== undefined ? (cwd ?? undefined) : getSession(mid, sid)?.cwd);
  const openActionPanel = (panel: ActionRowPanel) => {
    if (onOpenPanel) onOpenPanel(panel);
    else openPanel(panel);
  };
  const activate = () => {
    if (trace) return onToggle();
    if (diffs) {
      if (readOnly) return onToggle();
      const path = p.file ?? "";
      const resolvedCwd = resolveCwd();
      return openActionPanel({ kind: "changes", mid, sid, at: { path: path ? changePath(path, resolvedCwd) : "", call } });
    }
    if (inPlace) return onToggle();
    const f = toolFile(name, use.tool_input);
    if (f) openActionPanel({ kind: "file", mid, path: f.path, cwd: resolveCwd(), line: f.line, ...(readOnly ? { readOnly: true } : {}) });
  };
  const ids = useMemo(() => [use.id, ...(result ? [result.id] : [])], [use.id, result]);
  const label = [p.verb, p.param?.text].filter(Boolean).join(" ");
  // on a compact layout the duration stays in the hover title, not in the crowded row
  const rowTitle = [p.param?.title, narrow && duration].filter(Boolean).join(" · ") || undefined;
  // A supervision trace is historical: show the original parameters/result, never today's file in the side panel.
  const structured = !trace && hasStepDetail(use);
  const detail = open && structured;
  const input = open && !detail ? trace ? JSON.stringify(use.tool_input ?? {}, null, 2) : inlineInput(use) : "";
  const output = result && (trace || showsResult(name, result)) ? fullText : "";
  return (
    <div className="flex min-w-0 flex-col" data-action-row={name} data-tool-use={call} data-items={ids.join(" ")}>
      <button type="button" onClick={activate} onPointerEnter={structured ? preloadStepDetail : undefined} onFocus={structured ? preloadStepDetail : undefined} aria-expanded={inPlace ? open : undefined} aria-label={label || name} title={rowTitle} className={ROW}>
        <span role="img" aria-label={running ? t("chat-actionrow.status_running") : error ? t("chat-actionrow.status_error") : t("chat-actionrow.status_done")} className={cx(STATUS_SLOT, error && "text-red")}>
          {running ? <Spinner size={13} /> : <Icon name={error ? "x" : "check"} size={13} />}
        </span>
        {p.verb && <span className="shrink-0 text-[length:var(--chat-secondary)] whitespace-nowrap text-tool-ink">{p.verb}</span>}
        {p.param &&
          (p.file ? (
            <span className="inline-flex max-w-full min-w-0 items-center gap-[3px] rounded-[4px] border border-line bg-field px-[5px] py-[0.5px] font-mono text-meta leading-[1.385] whitespace-nowrap text-ink-2 transition-colors group-hover:bg-hover-2" data-file-chip>
              <Icon name="read" size={12} className="shrink-0 text-ink-3" />
              <span className="truncate">{p.param.text}</span>
            </span>
          ) : (
            <span className="min-w-0 truncate text-[length:var(--chat-secondary)] font-medium text-tool-ink">{p.param.text}</span>
          ))}
        <span className="flex-1" />
        {stats && (stats.add > 0 || stats.del > 0) && (
          <span className="flex shrink-0 gap-1 font-mono text-meta font-medium tabular-nums">
            <span className="text-diff-add-fg">+{stats.add}</span>
            <span className="text-diff-del-fg">-{stats.del}</span>
          </span>
        )}
        {duration && (
          <span data-tool-duration className="max-lg:hidden shrink-0 font-mono text-label text-tool-ink tabular-nums">
            {duration}
          </span>
        )}
        {bg && <span className={cx("shrink-0 rounded-[4px] px-1 text-submeta font-medium whitespace-nowrap", bg.failed ? "bg-red-tint text-red" : "bg-field text-ink-2")}>{bg.text}</span>}
        <span aria-hidden className={CHEVRON_SLOT}>
          <Icon name={inPlace && open ? "chevron" : "chevronRight"} size={13} stroke={2.2} />
        </span>
      </button>
      {inlineDiff && !trace && diffs && !error && (
        <div className="mx-2 overflow-x-auto" data-live-edit-diff><DiffBlocks diffs={foldDiffs(diffs).shown} /></div>
      )}
      {images.length > 0 && (
        <div className="flex min-w-0 flex-wrap gap-1.5 py-2 pl-8">
          {images.map((im, i) =>
            renderImage ? (
              <Fragment key={im.id}>{renderImage(im, i)}</Fragment>
            ) : (
              <Thumb key={im.id} item={im} mid={mid} sid={sid} />
            ),
          )}
        </div>
      )}
      {open && (
        <div className="mx-2 mt-1 mb-1 cursor-text rounded-[6px] border border-line bg-neutral-bubble px-[10px] py-2 font-mono text-label leading-[17px] text-ink select-text" data-inline-detail>
          {hasTelemetry && (
            <Suspense fallback={null}>
              <StepTelemetry items={stepItems} durationMs={rawDurationMs} className="mb-2 border-b border-line pb-1.5" />
            </Suspense>
          )}
          {detail && (
            <div className="font-sans">
              <Suspense fallback={null}>
                <StepDetail use={use} result={result} />
              </Suspense>
            </div>
          )}
          {COMMAND_TOOLS.has(name) && str((use.tool_input as Record<string, unknown> | undefined)?.description) && <div className="mb-2 font-sans">{str((use.tool_input as Record<string, unknown>).description)}</div>}
          {readOnly && diffs ? <DiffBlocks diffs={diffs} /> : input && <pre className="m-0 whitespace-pre-wrap [overflow-wrap:anywhere]">{input}</pre>}
          {output && (
            <>
              {(input || detail || (readOnly && diffs)) && <div className="my-2 h-px bg-tool-ink/20" />}
              <pre className={cx("m-0 whitespace-pre-wrap [overflow-wrap:anywhere]", error ? "text-red" : "text-tool-ink")}><AnsiOutput text={output} /></pre>
              {fullMore}
            </>
          )}
          {!input && !detail && !output && !(readOnly && diffs) && <span className="text-tool-ink">{t("chat-actionrow.no_content")}</span>}
        </div>
      )}
    </div>
  );
}

function AnsiOutput({ text }: { text: string }) {
  const runs = useMemo(() => parseAnsi(text), [text]);
  if (!runs) return <>{text}</>;
  return (
    <>
      {runs.map((run, i) =>
        run.fg || run.bold || run.dim || run.italic || run.underline ? (
          <span
            key={i}
            className={cx(
              run.fg,
              run.bold && "font-semibold",
              run.dim && "opacity-65",
              run.italic && "italic",
              run.underline && "underline",
            )}
          >
            {run.text}
          </span>
        ) : (
          <span key={i}>{run.text}</span>
        ),
      )}
    </>
  );
}

/** How many lines of a diff the phone's full-screen view shows before "Expand". */
export const DIFF_FOLD_LINES = 40;

/** The first `max` lines of an edit's changes (cut inside the change they end in), and how many lines that leaves out. */
export function foldDiffs(diffs: Op[][], max = DIFF_FOLD_LINES): { shown: Op[][]; hidden: number } {
  const total = diffs.reduce((n, d) => n + d.length, 0);
  if (total <= max) return { shown: diffs, hidden: 0 };
  const shown: Op[][] = [];
  let left = max;
  for (const d of diffs) {
    if (left <= 0) break;
    shown.push(d.slice(0, left));
    left -= d.length;
  }
  return { shown, hidden: total - max };
}

const DIFF_CONTEXT = 3;
const INTRALINE_MAX_CHARS = 500;
const INTRALINE_MAX_CELLS = 2500;

export interface DiffSegment {
  text: string;
  changed: boolean;
}

function pushSegment(out: DiffSegment[], text: string, changed: boolean) {
  if (!text) return;
  const last = out[out.length - 1];
  if (last && last.changed === changed) last.text += text;
  else out.push({ text, changed });
}

function tokenizeLine(line: string): string[] {
  return line.match(/[\p{L}\p{N}_$]+|\s+|./gu) ?? [];
}

/** Fast intraline word/token diff with prefix/suffix trimming and O(N*M) cap for long lines. */
export function computeIntralineDiff(oldLine: string, newLine: string): { oldParts: DiffSegment[]; newParts: DiffSegment[] } {
  if (oldLine === newLine) {
    return {
      oldParts: oldLine ? [{ text: oldLine, changed: false }] : [],
      newParts: newLine ? [{ text: newLine, changed: false }] : [],
    };
  }
  if (oldLine.length > INTRALINE_MAX_CHARS || newLine.length > INTRALINE_MAX_CHARS) {
    return {
      oldParts: oldLine ? [{ text: oldLine, changed: false }] : [],
      newParts: newLine ? [{ text: newLine, changed: false }] : [],
    };
  }
  const a = tokenizeLine(oldLine);
  const b = tokenizeLine(newLine);
  let prefix = 0;
  while (prefix < a.length && prefix < b.length && a[prefix] === b[prefix]) prefix++;
  let suffix = 0;
  while (suffix < a.length - prefix && suffix < b.length - prefix && a[a.length - 1 - suffix] === b[b.length - 1 - suffix]) suffix++;

  const prefixText = a.slice(0, prefix).join("");
  const suffixText = suffix > 0 ? a.slice(a.length - suffix).join("") : "";
  const midA = a.slice(prefix, a.length - suffix);
  const midB = b.slice(prefix, b.length - suffix);

  const oldParts: DiffSegment[] = [];
  const newParts: DiffSegment[] = [];
  pushSegment(oldParts, prefixText, false);
  pushSegment(newParts, prefixText, false);

  if (midA.length === 0 || midB.length === 0 || midA.length * midB.length > INTRALINE_MAX_CELLS) {
    pushSegment(oldParts, midA.join(""), true);
    pushSegment(newParts, midB.join(""), true);
  } else {
    const m = midA.length;
    const n = midB.length;
    const dp = new Uint16Array((m + 1) * (n + 1));
    const stride = n + 1;
    for (let i = m - 1; i >= 0; i--) {
      for (let j = n - 1; j >= 0; j--) {
        dp[i * stride + j] =
          midA[i] === midB[j]
            ? dp[(i + 1) * stride + (j + 1)] + 1
            : Math.max(dp[(i + 1) * stride + j], dp[i * stride + (j + 1)]);
      }
    }
    let i = 0;
    let j = 0;
    while (i < m && j < n) {
      if (midA[i] === midB[j]) {
        pushSegment(oldParts, midA[i], false);
        pushSegment(newParts, midB[j], false);
        i++;
        j++;
      } else if (dp[(i + 1) * stride + j] >= dp[i * stride + (j + 1)]) {
        pushSegment(oldParts, midA[i++], true);
      } else {
        pushSegment(newParts, midB[j++], true);
      }
    }
    while (i < m) pushSegment(oldParts, midA[i++], true);
    while (j < n) pushSegment(newParts, midB[j++], true);
  }

  pushSegment(oldParts, suffixText, false);
  pushSegment(newParts, suffixText, false);
  return { oldParts, newParts };
}

type LineRow = { kind: "same" | "add" | "del"; text: string; oldLine: number | null; newLine: number | null; segments?: DiffSegment[] };

type NumberedRow = LineRow | { kind: "gap"; skipped?: number };

/** Number each line of a diff block (old / new), collapse long unchanged stretches into `⋯` gap rows,
 *  and compute intraline word-diffs for equal-length contiguous `del`/`add` hunks. */
function buildDiffRows(diffs: Op[][]): NumberedRow[] {
  const rows: NumberedRow[] = [];
  diffs.forEach((block, blockIdx) => {
    if (blockIdx > 0) rows.push({ kind: "gap" });
    let oldLine = 1;
    let newLine = 1;
    const numbered: LineRow[] = block.map((l) => {
      if (l.kind === "same") return { kind: "same", text: l.text, oldLine: oldLine++, newLine: newLine++ };
      if (l.kind === "del") return { kind: "del", text: l.text, oldLine: oldLine++, newLine: null };
      return { kind: "add", text: l.text, oldLine: null, newLine: newLine++ };
    });
    const hasChanges = numbered.some((r) => r.kind !== "same");
    if (!hasChanges) {
      rows.push(...numbered);
      return;
    }
    const keep = new Uint8Array(numbered.length);
    for (let i = 0; i < numbered.length; i++) {
      if (numbered[i].kind !== "same") {
        const from = Math.max(0, i - DIFF_CONTEXT);
        const to = Math.min(numbered.length - 1, i + DIFF_CONTEXT);
        for (let j = from; j <= to; j++) keep[j] = 1;
      }
    }
    let i = 0;
    while (i < numbered.length) {
      if (keep[i]) {
        rows.push(numbered[i++]);
      } else {
        let j = i;
        while (j < numbered.length && !keep[j]) j++;
        // only collapse if skipping more than 1 line; otherwise just show the single line
        if (j - i > 1) rows.push({ kind: "gap", skipped: j - i });
        else rows.push(numbered[i]);
        i = j;
      }
    }
  });
  // Pair contiguous equal-length del/add blocks for intraline word-diff highlighting
  let i = 0;
  while (i < rows.length) {
    if (rows[i].kind !== "del") {
      i++;
      continue;
    }
    let delEnd = i;
    while (delEnd < rows.length && rows[delEnd].kind === "del") delEnd++;
    let addEnd = delEnd;
    while (addEnd < rows.length && rows[addEnd].kind === "add") addEnd++;
    const delCount = delEnd - i;
    const addCount = addEnd - delEnd;
    if (delCount > 0 && delCount === addCount) {
      for (let k = 0; k < delCount; k++) {
        const delRow = rows[i + k] as LineRow;
        const addRow = rows[delEnd + k] as LineRow;
        const { oldParts, newParts } = computeIntralineDiff(delRow.text, addRow.text);
        delRow.segments = oldParts;
        addRow.segments = newParts;
      }
    }
    i = addEnd;
  }
  return rows;
}

type SplitDiffRow = { kind: "gap"; skipped?: number } | { kind: "pair"; left?: LineRow; right?: LineRow };

function toSplitDiffRows(rows: NumberedRow[]): SplitDiffRow[] {
  const out: SplitDiffRow[] = [];
  for (let i = 0; i < rows.length; ) {
    const r = rows[i];
    if (r.kind === "gap") {
      out.push({ kind: "gap", skipped: r.skipped });
      i++;
      continue;
    }
    if (r.kind === "same") {
      out.push({ kind: "pair", left: r, right: r });
      i++;
      continue;
    }
    const dels: LineRow[] = [];
    const adds: LineRow[] = [];
    while (i < rows.length && rows[i].kind === "del") dels.push(rows[i++] as LineRow);
    while (i < rows.length && rows[i].kind === "add") adds.push(rows[i++] as LineRow);
    const n = Math.max(dels.length, adds.length);
    for (let k = 0; k < n; k++) out.push({ kind: "pair", left: dels[k], right: adds[k] });
  }
  return out;
}

function renderDiffLineText(r: LineRow) {
  return r.segments && r.segments.length > 0
    ? r.segments.map((seg, si) =>
        seg.changed ? (
          <span
            key={si}
            className={cx("rounded-[2px] font-medium", r.kind === "del" ? "bg-diff-del-fg/20" : "bg-diff-add-fg/20")}
            data-diff-intraline
          >
            {seg.text}
          </span>
        ) : (
          <span key={si}>{seg.text}</span>
        ),
      )
    : r.text || " ";
}

export function DiffBlocks({ diffs, mode = "unified" }: { diffs: Op[][]; mode?: "unified" | "split" }) {
  const rows = useMemo(() => buildDiffRows(diffs), [diffs]);
  const split = useMemo(() => (mode === "split" ? toSplitDiffRows(rows) : null), [mode, rows]);
  if (split) {
    return (
      <div className="min-w-fit overflow-hidden rounded-[8px] py-1 font-mono text-xs leading-[1.6] shadow-hairline" data-diff-mode="split">
        {split.map((row, i) =>
          row.kind === "gap" ? (
            <div key={i} className="flex min-w-fit items-center bg-inset/80 py-0.5 text-submeta text-ink-3 select-none" data-diff-gap>
              <span className="w-9 shrink-0 border-r border-line/60 pr-1.5 text-right tabular-nums">⋯</span>
              <span className="px-3 text-submeta text-ink-3">{row.skipped ? t("chat-actionrow.diff_skipped", { count: row.skipped }) : "⋯"}</span>
            </div>
          ) : (
            <div key={i} className="grid min-w-full grid-cols-2" data-diff-split-row>
              <div className={cx("flex min-w-0 border-r border-line/60 pr-2", row.left?.kind === "del" && "bg-diff-del-bg", !row.left && "bg-inset/40")} data-diff-line={row.left?.kind}>
                <span className={cx("w-9 shrink-0 border-r border-line/60 pr-1.5 text-right text-submeta tabular-nums select-none", row.left?.kind === "del" ? "text-diff-del-fg/75" : "text-ink-3/70")}>
                  {row.left?.oldLine ?? ""}
                </span>
                <span className={cx("w-5 shrink-0 text-center select-none", row.left?.kind === "del" ? "text-diff-del-fg" : "text-ink-3")}>
                  {row.left?.kind === "del" ? "−" : " "}
                </span>
                <span className="min-w-0 flex-1 whitespace-pre-wrap [overflow-wrap:anywhere] text-ink">
                  {row.left ? renderDiffLineText(row.left) : " "}
                </span>
              </div>
              <div className={cx("flex min-w-0 pr-2", row.right?.kind === "add" && "bg-diff-add-bg", !row.right && "bg-inset/40")} data-diff-line={row.right?.kind}>
                <span className={cx("w-9 shrink-0 border-r border-line/60 pr-1.5 text-right text-submeta tabular-nums select-none", row.right?.kind === "add" ? "text-diff-add-fg/75" : "text-ink-3/70")}>
                  {row.right?.newLine ?? ""}
                </span>
                <span className={cx("w-5 shrink-0 text-center select-none", row.right?.kind === "add" ? "text-diff-add-fg" : "text-ink-3")}>
                  {row.right?.kind === "add" ? "+" : " "}
                </span>
                <span className="min-w-0 flex-1 whitespace-pre-wrap [overflow-wrap:anywhere] text-ink">
                  {row.right ? renderDiffLineText(row.right) : " "}
                </span>
              </div>
            </div>
          ),
        )}
      </div>
    );
  }
  return (
    <div className="min-w-fit overflow-hidden rounded-[8px] py-1 font-mono text-xs leading-[1.6] shadow-hairline" data-diff-mode="unified">
      {rows.map((r, i) =>
        r.kind === "gap" ? (
          <div key={i} className="flex min-w-fit items-center bg-inset/80 py-0.5 text-submeta text-ink-3 select-none" data-diff-gap>
            <span className="w-9 shrink-0 pr-1.5 text-right tabular-nums">⋯</span>
            <span className="w-9 shrink-0 border-r border-line/60 pr-1.5 text-right tabular-nums">⋯</span>
            <span className="w-6 shrink-0 text-center">⋯</span>
            {r.skipped ? <span className="pr-3 text-submeta text-ink-3">{t("chat-actionrow.diff_skipped", { count: r.skipped })}</span> : null}
          </div>
        ) : (
          <div key={i} className={cx("flex min-w-fit pr-3", r.kind === "add" && "bg-diff-add-bg", r.kind === "del" && "bg-diff-del-bg")} data-diff-line={r.kind}>
            <span className={cx("w-9 shrink-0 pr-1.5 text-right text-submeta tabular-nums select-none", r.kind === "add" ? "text-diff-add-fg/75" : r.kind === "del" ? "text-diff-del-fg/75" : "text-ink-3/70")}>
              {r.oldLine ?? ""}
            </span>
            <span className={cx("w-9 shrink-0 border-r border-line/60 pr-1.5 text-right text-submeta tabular-nums select-none", r.kind === "add" ? "text-diff-add-fg/75" : r.kind === "del" ? "text-diff-del-fg/75" : "text-ink-3/70")}>
              {r.newLine ?? ""}
            </span>
            <span className={cx("w-6 shrink-0 text-center select-none", r.kind === "add" ? "text-diff-add-fg" : r.kind === "del" ? "text-diff-del-fg" : "text-ink-3")}>
              {r.kind === "add" ? "+" : r.kind === "del" ? "−" : " "}
            </span>
            <span className="whitespace-pre text-ink">{renderDiffLineText(r)}</span>
          </div>
        ),
      )}
    </div>
  );
}
