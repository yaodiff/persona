// Compare runs' runner (#8916, docs/web/web.md "Compare runs"): makes each run's worktree, one at a time (two `git worktree
// add` on one repository at once fight over its lock), then hands the run's start to lib/new-agent.ts without opening it.
// Each step is written to the group (lib/compare-runs.ts) as it happens, so the page shows every run from the frame New
// agent closes in. The task's words and attachments stay in this tab's memory only, for a retry here.
import { useSyncExternalStore } from "react";
import { credentialOwner, errorText, getToken } from "./api";
import type { LocalAttachment } from "./attachments";
import { addGroup, getGroup, runBranches, updateRun, type RunSpec } from "./compare-runs";
import { uuid } from "./format";
import { deliverLaunch, launchAgent } from "./new-agent";
import { whenStarted } from "./new-agent-started";
import { getState } from "./store";
import { addAgentWorktree } from "./worktree";
import { t } from "../i18n";

export interface CompareStart {
  mid: string;
  /** The folder the worktrees are made from. */
  cwd: string;
  /** Names the branches when none was typed (lib/worktree.ts agentNameHint), and is sent as the worktrees' name hint. */
  nameHint: string;
  /** The branch the runs share as a stem ("" or what New agent shows as its placeholder). */
  branch: string;
  count: number;
  spec: RunSpec & { nameHint?: string };
  /** The start goes into the workspace the engine makes for each worktree (New agent's picked Paseo or Orca workspace,
   *  #8856, #17050). */
  intoWorkspace: boolean;
  text: string;
  attachments: LocalAttachment[];
}

interface Held {
  sessionNameHint?: string;
  text: string;
  attachments: LocalAttachment[];
  nameHint: string;
  intoWorkspace: boolean;
  /** The credential the runs were started with: a sign-out or another pairing stops the rest. */
  token: string;
  owner: string;
}

const held = new Map<string, Held>();
const active = new Set<string>();
const listeners = new Set<() => void>();
let version = 0;

function changed() {
  version++;
  listeners.forEach((l) => l());
}

/** Starts the runs in the background and returns the group's id at once (its page opens before any request returns). */
export function startCompare(input: CompareStart): string {
  const id = uuid();
  const { nameHint: sessionNameHint, ...spec } = input.spec;
  addGroup({
    id,
    at: Date.now(),
    mid: input.mid,
    cwd: input.cwd,
    spec,
    attachments: input.attachments.length > 0,
    runs: runBranches(input.branch, input.count).map((branch, i) => ({ n: i + 1, branch, phase: "waiting" })),
  });
  held.set(id, { sessionNameHint, text: input.text, attachments: input.attachments, nameHint: input.nameHint, intoWorkspace: input.intoWorkspace, token: getToken(), owner: credentialOwner() });
  void work(id);
  return id;
}

/** Works through the runs waiting for their turn; one pass at a time per group. */
async function work(id: string) {
  if (active.has(id)) return;
  active.add(id);
  changed();
  try {
    for (;;) {
      const next = getGroup(id)?.runs.find((r) => r.phase === "waiting");
      if (!next) break;
      await runOne(id, next.n);
    }
  } finally {
    active.delete(id);
    changed();
  }
}

async function runOne(id: string, n: number) {
  const g = getGroup(id);
  const run = g?.runs.find((r) => r.n === n);
  const h = held.get(id);
  if (!g || !run) return;
  if (!h) return updateRun(id, n, { phase: "failed", error: t("lib-compare-runs.task_gone") });
  const fail = (error: string) => updateRun(id, n, { phase: "failed", error });
  if (h.owner !== credentialOwner()) return fail(t("lib-compare-runs.signed_out"));
  if (!getState().machines.some((m) => m.id === g.mid && m.online)) return fail(t("lib-compare-runs.offline"));
  let path = run.path;
  let workspaceId = run.workspaceId;
  // a run whose worktree was made before it failed starts in that one again
  if (!path) {
    updateRun(id, n, { phase: "worktree", error: undefined });
    try {
      const wt = await addAgentWorktree(g.mid, g.cwd, h.nameHint, run.branch, { token: h.token, tokenOwner: h.owner }, g.spec.host);
      path = wt.path;
      workspaceId = h.intoWorkspace ? wt.workspace_id || undefined : undefined;
      updateRun(id, n, { path, workspaceId });
    } catch (e) {
      return fail(errorText(e));
    }
    if (h.owner !== credentialOwner()) return fail(t("lib-compare-runs.signed_out"));
  }
  // The launch owns its previews, as New agent's own start does.
  const attachments = h.attachments.map((a) => ({ ...a, url: a.url ? URL.createObjectURL(a.file) : undefined }));
  const launch = launchAgent({ machineId: g.mid, cwd: path, workspaceId, ...g.spec, nameHint: h.sessionNameHint, text: h.text, attachments }, { open: false });
  updateRun(id, n, { phase: "launched", launch, error: undefined });
  whenStarted(launch, (s) => updateRun(id, n, { sid: s.session_id }));
}

/** A failed run again: a start that failed is delivered again under its own op ids (lib/new-agent.ts), a run whose
 *  worktree failed (or that a reload stopped) takes its turn again while this tab holds the task. */
export function retryRun(id: string, n: number) {
  const run = getGroup(id)?.runs.find((r) => r.n === n);
  if (!run) return;
  const launch = run.launch ? getState().launches[run.launch] : undefined;
  if (run.phase === "launched") {
    if (launch?.phase === "failed" && !launch.uncertain) void deliverLaunch(launch.id);
    return;
  }
  if (!held.has(id)) return;
  updateRun(id, n, { phase: "waiting", error: undefined });
  // a pass under way takes it in its turn
  void work(id);
}

/** Whether this tab is working through the group now, and whether it still holds the task for a retry. */
export function useRunner(id: string): { active: boolean; holds: boolean } {
  const v = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => version,
    () => 0,
  );
  void v;
  return { active: active.has(id), holds: held.has(id) };
}

/** Test seam: forget what this tab holds. */
export function resetRunner() {
  held.clear();
  active.clear();
  changed();
}
