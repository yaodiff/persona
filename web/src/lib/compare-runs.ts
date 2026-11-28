// Compare runs (#8916, docs/web/web.md "Compare runs"): one New agent start run 2–4 times, each in a worktree of its own,
// and the page that shows the runs side by side. This file is the group as this browser keeps it and what each run shows;
// lib/compare-runner.ts makes the worktrees and starts the runs. Like the recent setups (lib/recent-setups.ts) the list
// stays in this browser, for this hub, and holds ids only, never the task's words: those and the attachments live in
// the tab that started the runs, for a retry there.
import { useSyncExternalStore } from "react";
import { BASE } from "./base";
import type { AgentLaunch } from "./new-agent";
import type { Session } from "./types";
import { t } from "../i18n";

export const MIN_RUNS = 2;
export const MAX_RUNS = 4;
/** Groups kept: the latest ones, not every comparison ever started. */
export const MAX_GROUPS = 20;

/** What every run starts with: New agent's choices, the same for each (never a model of its own, principle 5). */
export interface RunSpec {
  launcher: string;
  agentType?: string;
  inlinePrompt?: boolean;
  model?: string;
  effort?: string;
  serviceTier?: string;
  launcherDefaultEffort?: boolean;
  permissionMode?: string;
  host?: string;
  ultracode?: boolean;
}

/** One run as kept. `phase` is how far this browser got with it: `waiting` for its turn, `worktree` while its worktree
 *  is made, `launched` once its start was handed to lib/new-agent.ts (that launch then says how the start goes), `failed`
 *  when its worktree could not be made (`error`). */
export interface CompareRun {
  n: number;
  branch: string;
  phase: "waiting" | "worktree" | "launched" | "failed";
  /** The worktree made for it, and the runtime workspace an engine made with it (Paseo). */
  path?: string;
  workspaceId?: string;
  /** The local launch (lib/new-agent.ts; this tab only) and the session it became. */
  launch?: string;
  sid?: string;
  error?: string;
}

export interface CompareGroup {
  id: string;
  /** Epoch ms, when it was started. */
  at: number;
  /** The computer and the folder the worktrees were made from. */
  mid: string;
  cwd: string;
  spec: RunSpec;
  /** The start had attachments: a retry after a reload cannot bring them back. */
  attachments: boolean;
  runs: CompareRun[];
}

export const compareRunsKey = (base = BASE) => (base ? `persona.compareRuns.${base}` : "persona.compareRuns");

const PHASES = new Set<CompareRun["phase"]>(["waiting", "worktree", "launched", "failed"]);
const str = (v: unknown) => (typeof v === "string" ? v : undefined);

function readRun(v: unknown): CompareRun | null {
  if (!v || typeof v !== "object") return null;
  const r = v as Record<string, unknown>;
  if (typeof r.n !== "number" || typeof r.branch !== "string" || !PHASES.has(r.phase as CompareRun["phase"])) return null;
  const run: CompareRun = { n: r.n, branch: r.branch, phase: r.phase as CompareRun["phase"] };
  for (const k of ["path", "workspaceId", "launch", "sid", "error"] as const) {
    const s = str(r[k]);
    if (s) run[k] = s;
  }
  return run;
}

function readSpec(v: unknown): RunSpec | null {
  if (!v || typeof v !== "object") return null;
  const s = v as Record<string, unknown>;
  if (typeof s.launcher !== "string") return null;
  const spec: RunSpec = { launcher: s.launcher };
  for (const k of ["agentType", "model", "effort", "serviceTier", "permissionMode", "host"] as const) {
    const x = str(s[k]);
    if (x) spec[k] = x;
  }
  if (s.inlinePrompt === true) spec.inlinePrompt = true;
  if (s.launcherDefaultEffort === true) spec.launcherDefaultEffort = true;
  if (s.ultracode === true) spec.ultracode = true;
  return spec;
}

/** Only the fields of a group: whatever else an entry carries is never kept. */
function readGroup(v: unknown): CompareGroup | null {
  if (!v || typeof v !== "object") return null;
  const g = v as Record<string, unknown>;
  const spec = readSpec(g.spec);
  if (typeof g.id !== "string" || !g.id || typeof g.at !== "number" || typeof g.mid !== "string" || !g.mid || typeof g.cwd !== "string"
    || !spec || !Array.isArray(g.runs)) return null;
  const runs = g.runs.map(readRun).filter((r): r is CompareRun => r !== null);
  if (runs.length < MIN_RUNS) return null;
  return { id: g.id, at: g.at, mid: g.mid, cwd: g.cwd, spec, attachments: g.attachments === true, runs };
}

export function loadGroups(store: Storage = localStorage): CompareGroup[] {
  try {
    const raw = store.getItem(compareRunsKey());
    const list: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(list)) return [];
    return list.map(readGroup).filter((g): g is CompareGroup => g !== null).slice(0, MAX_GROUPS);
  } catch {
    return [];
  }
}

function saveGroups(groups: CompareGroup[], store: Storage) {
  try {
    if (groups.length) store.setItem(compareRunsKey(), JSON.stringify(groups.slice(0, MAX_GROUPS)));
    else store.removeItem(compareRunsKey());
  } catch {
    // storage full or blocked: the runs go on, and this tab still shows them
  }
}

// ── the list, shared by the page, the runner and the chat menu ──

let cache: CompareGroup[] | null = null;
const listeners = new Set<() => void>();

function groupsNow(): CompareGroup[] {
  // a render with no browser storage (the server's) has none
  if (typeof localStorage === "undefined") return [];
  if (!cache) cache = loadGroups();
  return cache;
}

function publish(next: CompareGroup[]) {
  cache = next.slice(0, MAX_GROUPS);
  saveGroups(cache, localStorage);
  listeners.forEach((l) => l());
}

export function subscribeGroups(l: () => void): () => void {
  listeners.add(l);
  // another tab of this hub started or moved a comparison
  const onStorage = (e: StorageEvent) => {
    if (e.key !== compareRunsKey()) return;
    cache = null;
    l();
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(l);
    window.removeEventListener("storage", onStorage);
  };
}

export function getGroups(): CompareGroup[] {
  return groupsNow();
}

export function getGroup(id: string): CompareGroup | undefined {
  return groupsNow().find((g) => g.id === id);
}

export function useCompareGroup(id: string): CompareGroup | undefined {
  return useSyncExternalStore(subscribeGroups, () => getGroup(id), () => undefined);
}

/** A new group goes first; the oldest beyond MAX_GROUPS are forgotten (their sessions and worktrees stay). */
export function addGroup(group: CompareGroup) {
  publish([group, ...groupsNow().filter((g) => g.id !== group.id)]);
}

/** Changes one run of a group (nothing when either is gone: forgotten in another tab). */
export function updateRun(id: string, n: number, patch: Partial<CompareRun>) {
  const list = groupsNow();
  const g = list.find((x) => x.id === id);
  if (!g || !g.runs.some((r) => r.n === n)) return;
  const next = { ...g, runs: g.runs.map((r) => (r.n === n ? dropUndefined({ ...r, ...patch }) : r)) };
  publish(list.map((x) => (x.id === id ? next : x)));
}

function dropUndefined(run: CompareRun): CompareRun {
  for (const k of Object.keys(run) as (keyof CompareRun)[]) if (run[k] === undefined) delete run[k];
  return run;
}

/** Test seam: forget the in-memory copy so the next read loads storage again. */
export function resetGroupsCache() {
  cache = null;
}

// ── what each run shows ──

/** The branch each run gets: the one typed (or the one New agent would name) with -1 … -N, so the runs never share one. */
export function runBranches(base: string, count: number): string[] {
  const b = base.trim().replace(/[-/]+$/, "");
  return Array.from({ length: count }, (_, i) => `${b}-${i + 1}`);
}

/** The session a run became: the one recorded, else the computer's session in its worktree (one started before a reload,
 *  or from another tab, is found by its folder). */
export function runSession(run: CompareRun, mid: string, sessions: Record<string, Session[]>): Session | undefined {
  const list = sessions[mid] ?? [];
  if (run.sid) {
    const s = list.find((x) => x.session_id === run.sid);
    if (s) return s;
  }
  if (!run.path) return undefined;
  const path = trimSlash(run.path);
  return list.find((x) => [x.cwd, x.workspace_cwd, x.worktree].some((p) => p && trimSlash(p) === path));
}

const trimSlash = (p: string) => p.replace(/\/+$/, "");

/** The comparison a session is a run of (its chat's ⋯ › Compare runs), if this browser keeps one. */
export function groupOfSession(s: Session): CompareGroup | undefined {
  const one = { [s.machine_id]: [s] };
  return groupsNow().find((g) => g.mid === s.machine_id && g.runs.some((r) => runSession(r, g.mid, one) === s));
}

/** The chat menu's item for a run (views/chat/ChatView.tsx loads this file after the first screen, so its words are here). */
export function compareMenu(s: Session): { id: string; label: string } | undefined {
  const g = groupOfSession(s);
  return g && { id: g.id, label: t("compare-runs.title") };
}

/** How a run stands, as its card says it:
 *  - `waiting`: its turn has not come (the runs' worktrees are made one at a time);
 *  - `worktree`: its worktree is being made;
 *  - `starting`: its start was handed over and has not been answered;
 *  - `started`: it is a session (its own state from then on);
 *  - `failed`: its worktree or its start failed (`error`); `retry` says what a retry repeats: the run's turn (its
 *    worktree, unless one was made, then its start), or the start alone; none when this tab cannot;
 *  - `stopped`: this tab no longer runs it (the page reloaded before its turn); `retry` only while this tab holds the task;
 *  - `elsewhere`: handed over, but this tab no longer follows the start (a reload): it shows once its session appears. */
export type RunStatus =
  | { kind: "waiting" | "worktree" | "starting" | "elsewhere" }
  | { kind: "started"; session: Session }
  | { kind: "failed"; error: string; retry: "run" | "launch" | null; uncertain?: boolean }
  | { kind: "stopped"; retry: boolean };

export interface RunContext {
  sessions: Record<string, Session[]>;
  launches: Record<string, AgentLaunch>;
  /** This tab is working through the group's runs now. */
  active: boolean;
  /** This tab still holds the task's words and attachments, so a run can be made again. */
  holds: boolean;
}

export function runStatus(run: CompareRun, group: Pick<CompareGroup, "mid">, ctx: RunContext): RunStatus {
  const session = runSession(run, group.mid, ctx.sessions);
  if (session) return { kind: "started", session };
  if (run.phase === "failed") return { kind: "failed", error: run.error ?? "", retry: ctx.holds ? "run" : null };
  if (run.phase === "launched") {
    const launch = run.launch ? ctx.launches[run.launch] : undefined;
    if (!launch) return { kind: "elsewhere" };
    if (launch.phase === "ready" && launch.session) return { kind: "started", session: launch.session };
    // lib/new-agent.ts deliverLaunch keeps the start's op ids, so its retry never starts a second Agent; a start the hub
    // could not say the outcome of is the person's call, on the launch's own page (never retried from here)
    if (launch.phase === "failed") return launch.uncertain
      ? { kind: "failed", error: launch.error ?? "", retry: null, uncertain: true }
      : { kind: "failed", error: launch.error ?? "", retry: "launch" };
    return { kind: "starting" };
  }
  if (ctx.active) return { kind: run.phase };
  return { kind: "stopped", retry: ctx.holds };
}
