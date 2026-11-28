// The bar above the composer while an Agent works, as cindy's RunningStatusBar (#1963; the owner asked on 2026-11-12
// that it follow cindy, #5312). Left, a short state: in an Agent session cindy's words for what the turn is doing
// ("Thinking…", "Editing files…", "Bash running…", "Spawning a subagent…", "Compacting context…"), worked out from its
// status line and its newest steps (lib/run-step.ts); in a Persona conversation cindy's plain phases ("Thinking…",
// "Replying…", "Reading files…"), for everyday people (super rules 0 and 4). Right, how long the turn has run, in the
// work summary's format, then in an Agent session either the latest speed ("42 tok/s", lib/token-rate.ts), a button
// whose card shows the speed history, or the tokens written so far ("↓ 1.2k tok"); never both. It breathes once each
// time its words or count move (output, not just time passing); when the turn finishes it says ✓ Done for a second (a
// turn waiting on you, stopped or cut off by an error keeps its last words), then fades out. Without a status line (another agent, an older hub) it counts from when the turn started.
// Once the turn is over, background work takes over in this order: workflow, subagents, waiting commands, then working
// commands. A workflow (#3434, `background_working`) says "Background workflow · Verify · 169/185+", timed from its start
// (not the turn's leftover clock); the count stays whole on a narrow screen. A subagent still running (#5030, cindy's
// background subtasks) gets cindy's Activity mark and "Background subtasks running, still consuming model usage…" with
// no time and no tokens; cindy's "Stop all" waits for a node that can stop one (#5044). Commands that only wait (#6045,
// `background_waiting`) get a still line, timed from when the wait began, with no breath and no tokens. A real
// background command or Monitor (#7341) instead gets the working treatment: a terminal mark and "Background task", then
// the command's description or one-line command and "N more", timed only from that command's own start, with no time
// when it has none and never the turn's tokens or speed. With the hub out of reach the bar says so in front of the last
// state it knew, its clock stands still (nothing new can have arrived) and it shows no speed.
import { useCallback, useEffect, useRef, useState, type CSSProperties } from "react";
import { useSessionLive } from "../../lib/live";
import { parseRetryAPIError } from "../../lib/api-errors";
import type { RunStep } from "../../lib/run-step";
import { useStore } from "../../lib/store";
import { compactTokens } from "../../lib/tokens";
import { timeOrNaN } from "../../lib/times";
import { toolDisplayName } from "../../lib/turns";
import type { SessionLive } from "../../lib/types";
import { TICKING_DURATION, workDuration } from "../../lib/work-groups";
import { workflowCounts, type BackgroundLine, type WaitLine } from "../../lib/workflows";
import { stalledText } from "../../lib/background-work";
import { stateName } from "../../lib/state-labels";
import { useNow } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { FADE_OUT_MS, LINGER_MS, useBreath, useCountUp } from "../../ui/motion";
import { Rolling } from "../../ui/Rolling";
import "../../ui/stream/stream.css";
import { t, formatNumber } from "../../i18n";

// The speed (its history, the button and its card) loads once an Agent's turn runs, off the chat's first screen: a
// speed shows only once one is measured, seconds in, and until then the time and the tokens stand as they are.
type SpeedType = typeof import("./RunningSpeedCard").RunningSpeed;
let Speed: SpeedType | undefined;
const loadSpeed = () => import("./RunningSpeedCard").then((m) => (Speed = m.RunningSpeed));

function useSpeed(want: boolean): SpeedType | undefined {
  const [, loaded] = useState(0);
  useEffect(() => {
    // a chunk that fails to load leaves the time and the tokens; the next turn tries again
    if (want && !Speed) loadSpeed().then(() => loaded((n) => n + 1), () => {});
  }, [want]);
  return Speed;
}

/** The tick in the bar's time ("1m 5s"): the work summary's format with cindy's status bar options. */
const elapsedText = (seconds: number) => workDuration(seconds * 1000, TICKING_DURATION);

const EDIT_TOOLS = new Set(["Edit", "Write", "MultiEdit", "NotebookEdit", "apply_patch"]);
const READ_TOOLS = new Set(["Read", "NotebookRead"]);
const SEARCH_TOOLS = new Set(["Grep", "Glob", "LS"]);
const START: RunStep = { kind: "start" };

/** The left side's words, and whether they say the context is being compacted (cindy's Layers mark). `plain`: the
 *  Persona conversation's everyday words, which never name a tool. */
export function statusWords(live: SessionLive | null, step: RunStep = START, plain = false): { text: string; compacting: boolean } {
  if (live?.verb && /^compact/i.test(live.verb)) return { text: plain ? t("chat-runningstatusbar.organizing") : t("chat-runningstatusbar.compacting"), compacting: true };
  return { text: live?.thinking ? t("chat-runningstatusbar.thinking") : (plain ? plainWord : agentWord)(step), compacting: false };
}

function agentWord(step: RunStep): string {
  switch (step.kind) {
    case "streaming":
    case "text":
      return t("chat-runningstatusbar.generating");
    case "after":
      return t("chat-runningstatusbar.working");
    case "tool": {
      const tool = step.tool ?? "";
      if (EDIT_TOOLS.has(tool)) return t("chat-runningstatusbar.editing_files");
      if (tool === "WebSearch") return t("chat-runningstatusbar.searching_web");
      if (step.spawning) return t("chat-runningstatusbar.spawning_agent");
      return t("chat-runningstatusbar.running_tool", { tool: toolDisplayName(tool) });
    }
    default:
      return t("chat-runningstatusbar.thinking");
  }
}

/** cindy's plain phases (publicToolPhase): only what everyone can follow, nothing of the tool or its target. */
function plainWord(step: RunStep): string {
  switch (step.kind) {
    case "streaming":
      return t("chat-runningstatusbar.replying");
    case "text":
    case "after":
      return t("chat-runningstatusbar.processing");
    case "tool": {
      const tool = step.tool ?? "";
      if (READ_TOOLS.has(tool)) return t("chat-runningstatusbar.reading_files");
      if (EDIT_TOOLS.has(tool)) return t("chat-runningstatusbar.updating_files");
      if (SEARCH_TOOLS.has(tool)) return t("chat-runningstatusbar.searching_files");
      if (tool === "WebSearch") return t("chat-runningstatusbar.searching_web");
      if (tool === "WebFetch") return t("chat-runningstatusbar.reading_web");
      return t("chat-runningstatusbar.processing");
    }
    default:
      return t("chat-runningstatusbar.thinking");
  }
}

export interface RunningStatusProps {
  working: boolean;
  /** Once it is not working: the turn finished (ChatView turnDone), so the bar says ✓ Done; otherwise its last words. */
  done?: boolean;
  startedAt?: string;
  /** No live work line ticks in the chat: only an older hub's guess at a background workflow reads it (inBackground). */
  clock?: boolean;
  /** The Persona conversation: plain words and the time, no tokens and no speed. */
  plain?: boolean;
  /** What the turn is doing, from its newest steps (ChatView, lib/run-step.ts). */
  step?: RunStep;
  background?: BackgroundLine | null;
  subtasks?: boolean;
  /** A real background command after the turn, with its own optional start (#7341). */
  command?: WaitLine | null;
  onStop?: () => void;
}

export function RunningStatusBar({ mid, sid, wait, ...props }: RunningStatusProps & { mid: string; sid: string; wait?: WaitLine | null }) {
  const live = useSessionLive(mid, sid);
  const offline = useStore((s) => s.conn !== "online");
  if (wait && props.working) return <WaitingView wait={wait} offline={offline} />;
  return <RunningStatusView {...props} live={live} offline={offline} speedKey={`${mid}/${sid}`} />;
}

/** In front of the last state the bar knows, while the hub is out of reach. */
function Disconnected() {
  return <span className="shrink-0 text-ink-3" data-running-offline>{t("chat-runningstatusbar.disconnected")} ·</span>;
}

/** The bar of an agent that only waits in the background (#6045): still, timed from when it began. */
export function WaitingView({ wait, offline }: { wait: WaitLine; offline?: boolean }) {
  const now = useNow(1000, !offline);
  const since = timeOrNaN(wait.since);
  // what it waits on: the header's title line says the state ("Waiting in the background"), once (#3956); the state's name
  // only stands in when the wait has no words of its own
  const words = [wait.what || stateName("bg_waiting"), wait.more > 0 ? t("chat-runningstatusbar.more_waits", { count: wait.more }) : ""].filter(Boolean).join(" · ");
  return (
    <div data-running-status="on" data-running-background="waiting" className="flex select-none items-center justify-between gap-3 px-2 py-1.5 text-[13px] font-medium">
      <div className="flex min-w-0 items-center gap-1.5 text-ink-2">
        <Icon name="clock" size={14} className="shrink-0 -translate-y-px text-accent-ink" />
        {offline && <Disconnected />}
        <span className="truncate" data-running-verb>{words}</span>
      </div>
      {Number.isFinite(since) && <span className="shrink-0 whitespace-nowrap text-ink-3 tabular-nums" data-running-elapsed>{elapsedText(Math.max(0, Math.floor((now - since) / 1000)))}</span>}
    </div>
  );
}

/** Whether the bar speaks of the background workflow: the hub says the turn is over, or (a hub from before
 *  background_working) a workflow runs and no turn of the agent's own shows: no status line, no live work line
 *  ticking in the chat (`clock`) and no call it waits on (a subagent launched in the foreground, say). */
function inBackground(background: BackgroundLine | null | undefined, live: SessionLive | null, clock: boolean, step: RunStep | undefined): background is BackgroundLine {
  return Boolean(background && (!background.guessed || (!live?.retry_error && clock && !(live?.verb || live?.thinking) && step?.kind !== "tool")));
}

/** The words for it: "Background workflow · Verify · 169/185+ · 1 more". Only the words before the count shrink (the
 *  name for it first, then the phase), so a narrow bar keeps the count and the other runs whole. */
function BackgroundWords({ line }: { line: BackgroundLine }) {
  const w = line.run;
  return (
    <span className="flex min-w-0 items-center whitespace-pre tabular-nums" data-running-verb>
      <span className="min-w-0 truncate">{t("chat-runningstatusbar.background_workflow")}</span>
      {/* not `truncate`: its nowrap would drop the space the piece starts with */}
      {w.phase && <span className="max-w-[40%] shrink-0 overflow-hidden text-ellipsis">{` · ${w.phase}`}</span>}
      <span className="shrink-0" data-running-counts>
        {" · "}
        <Rolling value={workflowCounts(w)} />
      </span>
      {line.more > 0 && <span className="shrink-0" data-running-more>{` · ${t("chat-runningstatusbar.more_workflows", { count: line.more })}`}</span>}
    </span>
  );
}

const CRON_WHAT = /^Cron:\s*([^\r\n]+?)(?:,\s*Prompt:\s*([\s\S]*))?$/i;

function formatCronSchedule(expr: string): string {
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return "";
  const [min, hour, dom, mon, dow] = parts;
  if (dom !== "*" || mon !== "*" || dow !== "*") return "";
  const everyMin = /^\*\/(\d+)$/.exec(min);
  if (everyMin && hour === "*") {
    const count = Number(everyMin[1]);
    if (count > 0) return t("chat-runningstatusbar.every_minutes", { count });
  }
  if (min === "*" && hour === "*") return t("chat-runningstatusbar.every_minutes", { count: 1 });
  if (min === "0" && hour === "*") return t("chat-runningstatusbar.hourly");
  const everyHour = /^\*\/(\d+)$/.exec(hour);
  if (min === "0" && everyHour) {
    const count = Number(everyHour[1]);
    if (count === 1) return t("chat-runningstatusbar.hourly");
    if (count > 0) return t("chat-runningstatusbar.every_hours", { count });
  }
  if (/^\d{1,2}$/.test(min) && /^\d{1,2}$/.test(hour)) {
    const h = Number(hour);
    const m = Number(min);
    if (h >= 0 && h <= 23 && m >= 0 && m <= 59) {
      return t("chat-runningstatusbar.daily_at", { time: `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}` });
    }
  }
  return "";
}

/** Cleans raw `Cron: ..., Prompt: ...` strings into a concise single-line summary. */
export function formatBackgroundWhat(raw: string): string {
  const trimmed = raw.trim();
  const match = CRON_WHAT.exec(trimmed);
  if (!match) return raw;
  const schedule = formatCronSchedule(match[1]);
  const prompt = (match[2] ?? "").replace(/\s+/g, " ").trim();
  if (schedule && prompt) return `${schedule} · ${prompt}`;
  if (prompt) return prompt;
  if (schedule) return schedule;
  return match[1].trim();
}

function BackgroundCommandWords({ line, now }: { line: WaitLine; now: number }) {
  const stalled = stalledText(line.stalled, now);
  const what = line.what ? formatBackgroundWhat(line.what) : "";
  return (
    <span className="flex min-w-0 items-center whitespace-pre" data-running-verb>
      <span className="shrink-0">{t("chat-runningstatusbar.background_task")}</span>
      {what && (
        <>
          <span className="shrink-0" data-running-command-separator>{` · `}</span>
          <span className="min-w-0 truncate">{what}</span>
        </>
      )}
      {line.more > 0 && <span className="shrink-0">{` · ${t("chat-runningstatusbar.more_waits", { count: line.more })}`}</span>}
      {stalled && <span className="min-w-0 truncate font-normal text-ink-3" data-running-stalled title={t("lib-state-labels.bg_stalled_hint")}>{` · ${stalled}`}</span>}
    </span>
  );
}

type Phase = "on" | "linger" | "fading" | "off";

/** The bar for a given status line (RunningStatusBar subscribes; tests render this). `speedKey`: the conversation its
 *  speed history is kept for while the page is open. */
export function RunningStatusView({ live, working, done: finished, startedAt, clock = true, plain, step, background, subtasks, command, onStop, offline, speedKey = "" }: RunningStatusProps & { live: SessionLive | null; offline?: boolean; speedKey?: string }) {
  const phase = usePhase(working);
  const [pinned, setPinned] = useState(false);
  // after the turn: what it last said, while it lingers and fades. Its status line goes a moment before the turn ends,
  // so the last one of this turn stays for that (not one from a turn before)
  const last = useRef({ live, background, clock, subtasks, command, step });
  const wasWorking = useRef(working);
  if (working) last.current = { live: live ?? (wasWorking.current ? last.current.live : null), background, clock, subtasks, command, step };
  wasWorking.current = working;
  const kept = working ? { live, background, clock, subtasks, command, step } : last.current;
  const shown = kept.live;
  // Retry text is transient: never resurrect it from `last` after a null, normal-work or turn-end update.
  const retry = working && live?.retry_error && (live.retry_attempt ?? 0) > 0 && (live.retry_max ?? 0) > 0
    ? { error: live.retry_error, attempt: live.retry_attempt!, max: live.retry_max!, info: parseRetryAPIError(live.retry_error) }
    : null;
  const bg = retry ? null : kept.background;
  const behind = inBackground(bg, shown, kept.clock, kept.step);
  // the turn is over and background subagents run on: neither the turn's time nor its tokens say anything of them
  const usage = !retry && Boolean(kept.subtasks);
  // a real background command has its own optional clock; there is no turn-clock or mount-time fallback for it
  const task = retry ? null : kept.command;
  const commandStart = timeOrNaN(task?.since);
  // The hub can serialize Go's zero time as 0001-01-01: that is an absent start, not a gigantic elapsed time.
  const commandTimed = Number.isFinite(commandStart) && commandStart > 0;
  // the turn's own work, measured: an Agent's, not the Persona conversation's, not background work's
  const own = !plain && !behind && !usage && !task;
  const now = useNow(task?.stalled && !commandTimed ? 60_000 : 1000, phase === "on" && !offline && !usage && (!task || commandTimed || Boolean(task.stalled)));
  // from the run's start; a run that does not say when it started, from the turn's (not from when the bar appeared).
  // After the turn it stays as it last was (cindy): the turn's start and its status line are gone by then.
  const counted = useElapsed(behind || task ? null : shown, behind ? (bg.since ?? startedAt) : task ? undefined : startedAt, now);
  const commandCounted = task && commandTimed ? Math.max(0, Math.floor((now - commandStart) / 1000)) : 0;
  const visibleCounted = task ? commandCounted : counted;
  const lastElapsed = useRef(visibleCounted);
  if (working) lastElapsed.current = visibleCounted;
  const elapsed = working ? visibleCounted : lastElapsed.current;
  const tokens = useCountUp(shown?.tokens_down ?? 0);
  const done = !working && Boolean(finished) && !behind && !usage && !task;
  const state = statusWords(shown, kept.step, plain);
  const commandWords = task ? [t("chat-runningstatusbar.background_task"), task.what ? formatBackgroundWhat(task.what) : "", task.more > 0 ? t("chat-runningstatusbar.more_waits", { count: task.more }) : ""].filter(Boolean).join(" · ") : "";
  const words = retry ? t("chat-runningstatusbar.retrying", { attempt: formatNumber(retry.attempt), max: formatNumber(retry.max), reason: retry.info?.reason ?? t("lib-api-errors.failed") }) : behind ? "" : usage ? t("chat-runningstatusbar.background_subtasks") : task ? commandWords : done ? t("chat-runningstatusbar.done") : state.text;
  const compacting = !retry && !behind && !usage && !task && !done && state.compacting;
  const breath = useBreath(useMoves(`${words}\n${shown?.tokens_down ?? ""}`), phase === "on");
  const onPinned = useCallback((p: boolean) => setPinned(p), []);
  const SpeedView = useSpeed(working && own);
  // a card pinned open keeps its button through the end of the turn and after it (cindy)
  if (phase === "off" && !pinned) return null;
  const hidden = phase === "off";
  // each side fades on its own: a pinned card's button stays as it is (cindy)
  const fade: CSSProperties | undefined = hidden ? undefined : { opacity: phase === "fading" ? 0 : 1, transition: `opacity ${FADE_OUT_MS}ms ease-out` };
  const icon: IconName = retry ? "retry" : behind ? "workflow" : usage ? "activity" : task ? "terminal" : done ? "check" : compacting ? "layers" : "sparkles";
  const iconTone = retry ? "text-status-working" : behind ? "text-ai" : "text-accent-ink";
  const count = own && shown?.tokens_down ? (
    <>
      <Icon name="arrowDown" size={13} className="shrink-0" />
      <span data-running-tokens>{t("chat-runningstatusbar.token_count", { tokens: compactTokens(tokens) })}</span>
    </>
  ) : null;
  const time = <span data-running-elapsed>{elapsedText(elapsed)}</span>;
  return (
    <div
      data-running-status={phase}
      data-running-background={behind ? "workflow" : usage ? "subtasks" : task ? "command" : undefined}
      className="flex select-none items-center justify-between gap-3 px-2 py-1.5 text-[13px] font-medium"
    >
      <div
        onAnimationEnd={done ? undefined : breath.ended}
        aria-hidden={hidden || undefined}
        className={`flex min-w-0 items-center gap-1.5 ${retry ? "text-orange" : "text-ink-2"} ${hidden ? "invisible" : done ? "status-done" : breath.cycle ? "status-breath" : ""}`}
        data-running-done={done || undefined}
        style={breath.cycle && !done && !hidden && !(breath.cycle % 2) ? { ...fade, animationName: "status-breath-again" } : fade}
      >
        <Icon name={icon} size={14} stroke={done ? 2.5 : undefined} className={`shrink-0 ${iconTone} ${done ? "" : "-translate-y-px"}`} />
        {offline && <Disconnected />}
        {behind ? <BackgroundWords line={bg} /> : task ? <BackgroundCommandWords line={task} now={now} />
          : <span className={!retry && phase === "on" && !done ? "chat-shimmer truncate" : "truncate"} data-running-verb title={retry?.error}>{words}</span>}
      </div>
      <div className="flex shrink-0 items-center gap-1.5 whitespace-nowrap text-ink-3 tabular-nums" style={pinned ? undefined : fade}>
        {usage || (task && !commandTimed) ? null : own && SpeedView ? (
          <SpeedView speedKey={speedKey} turn={startedAt} live={shown} working={working} paused={kept.step?.kind === "tool"} offline={offline} now={now} elapsed={elapsedText(elapsed)} time={time} count={count} onPinned={onPinned} />
        ) : (
          <>
            {time}
            {count && <span aria-hidden>·</span>}
            {count}
          </>
        )}
        {working && onStop && (
          <button
            type="button"
            onClick={onStop}
            aria-label={t("chat-runningstatusbar.interrupt")}
            title={t("chat-runningstatusbar.interrupt_hint")}
            className="ml-1 hidden items-center gap-1 rounded-[6px] border border-line bg-surface/80 px-1.5 py-0.5 text-label font-medium text-ink-2 transition-colors hover:border-line-strong hover:bg-hover hover:text-ink sm:inline-flex"
          >
            <span>{t("chat-runningstatusbar.interrupt")}</span>
            <kbd className="rounded-[4px] bg-inset px-1 py-px font-mono text-submeta leading-tight text-ink-3">Esc</kbd>
          </button>
        )}
      </div>
    </div>
  );
}

/** A number that goes up each time `key` changes: what the bar breathes on (cindy: its words or its count moved). */
function useMoves(key: string): number {
  const moves = useRef({ key, n: 0 });
  if (moves.current.key !== key) moves.current = { key, n: moves.current.n + 1 };
  return moves.current.n;
}

/** on while working; after it, a second as it was, then the fade, then gone. */
function usePhase(working: boolean): Phase {
  const [phase, setPhase] = useState<Phase>(working ? "on" : "off");
  useEffect(() => {
    if (working) {
      setPhase("on");
      return;
    }
    if (phase === "off") return;
    const a = window.setTimeout(() => setPhase("fading"), LINGER_MS);
    const b = window.setTimeout(() => setPhase("off"), LINGER_MS + FADE_OUT_MS);
    return () => {
      clearTimeout(a);
      clearTimeout(b);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [working]);
  return working ? "on" : phase === "on" ? "linger" : phase;
}

/**
 * Seconds the turn has run: the status line's count, going on locally between its updates (about one a second);
 * without one, from `startedAt` (or from when the bar appeared).
 */
function useElapsed(live: SessionLive | null, startedAt: string | undefined, now: number): number {
  const base = useRef<{ s: number; at: number } | null>(null);
  const mounted = useRef(Date.now());
  if (live?.elapsed_s === undefined) base.current = null;
  else if (base.current?.s !== live.elapsed_s) base.current = { s: live.elapsed_s, at: Date.now() };
  if (base.current) return base.current.s + Math.max(0, Math.floor((now - base.current.at) / 1000));
  const from = timeOrNaN(startedAt);
  return Math.max(0, Math.floor((now - (Number.isNaN(from) ? mounted.current : from)) / 1000));
}
