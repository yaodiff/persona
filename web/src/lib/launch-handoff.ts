// Transient working state while a newly launched Agent hands off from Starting to its active first turn (#12380).
// Between POST /sessions (+ POST /messages) and the node's first working push, the session row can briefly report
// idle/unread with starting=false, making sidebar groups (e.g. "Not on a to-do") flash "Done unread" before flipping
// to "Working". This keeps the launched session projected as working until the hub confirms working/terminal state or
// the handoff window expires, without mutating the underlying hub rows in the store.
import { activityOf, stateOf } from "./format";
import type { AgentLaunch } from "./new-agent";
import type { Session } from "./types";

/** How long a ready launch with an initial prompt is held in working state while waiting for the hub's first working push. */
export const LAUNCH_HANDOFF_MS = 60_000;

/** Whether a launch carries an initial prompt or attachment that starts a first turn. */
export function launchHasFirstTurn(launch: Pick<AgentLaunch, "text" | "attachments" | "outgoing">): boolean {
  return Boolean(launch.text.trim() || launch.attachments.length > 0 || launch.outgoing?.text || launch.outgoing?.images.length);
}

/** Whether a launch is still actively starting/sending and shown under the sidebar's "Starting" section. */
export function isInFlightLaunch(launch: AgentLaunch): boolean {
  return launch.phase === "creating" || launch.phase === "uploading" || launch.phase === "sending";
}

/** The launch tracking `(s.machine_id, s.session_id)`, if any. */
export function launchForSession(launches: Record<string, AgentLaunch> | undefined, s: Pick<Session, "machine_id" | "session_id">): AgentLaunch | undefined {
  if (!launches) return undefined;
  for (const launch of Object.values(launches)) {
    if (launch.machineId === s.machine_id && launch.session?.session_id === s.session_id) return launch;
  }
  return undefined;
}

/** Whether the hub row (or clock) has moved into or past the launched first turn, ending the transient handoff window. */
export function launchHandoffSettled(s: Session, launch: AgentLaunch, now: number): boolean {
  if (launch.phase === "failed" || launch.handoffAt === undefined) return true;
  if (now - launch.handoffAt >= LAUNCH_HANDOFF_MS) return true;
  if (launch.phase !== "ready") return false;
  const st = activityOf(s);
  if (st === "working" || st === "needs_you" || st === "offline") return true;
  if (s.start_error || s.failure_reason || s.turn_error) return true;
  if (s.awaits_reply || s.work) return true;
  if (Boolean(s.last_text?.trim()) && s.last_text !== launch.session?.last_text) return true;
  return false;
}

/** Projects a newly launched session with an active first turn as working until its handoff settles. */
export function heldLaunchWorking(s: Session, launch: AgentLaunch | undefined, now: number): Session {
  if (!launch || launch.phase === "failed" || launch.handoffAt === undefined) return s;
  if (launch.machineId !== s.machine_id || launch.session?.session_id !== s.session_id) return s;
  if (launchHandoffSettled(s, launch, now)) return s;
  if (stateOf(s) === "working") return s;
  return { ...s, status: "working", state: "working", state_since: s.state_since ?? new Date(launch.handoffAt).toISOString() };
}

/** Applies `heldLaunchWorking` across `rows`, keeping the same array reference when nothing changes. */
export function heldLaunchRows(rows: Session[], launches: Record<string, AgentLaunch> | undefined, now: number): Session[] {
  if (!launches || !rows.length) return rows;
  let active: Map<string, AgentLaunch> | undefined;
  for (const launch of Object.values(launches)) {
    if (launch.phase === "failed" || launch.handoffAt === undefined || !launch.session) continue;
    active ??= new Map();
    active.set(`${launch.machineId}\u0000${launch.session.session_id}`, launch);
  }
  if (!active) return rows;
  let out = rows;
  rows.forEach((s, i) => {
    const shown = heldLaunchWorking(s, active!.get(`${s.machine_id}\u0000${s.session_id}`), now);
    if (shown === s) return;
    if (out === rows) out = [...rows];
    out[i] = shown;
  });
  return out;
}

/** Clears `handoffAt` on any launch whose hub row has settled (or whose handoff window expired). */
export function settledLaunches(launches: Record<string, AgentLaunch>, rows: readonly Session[], now: number): Record<string, AgentLaunch> {
  let next: Record<string, AgentLaunch> | undefined;
  let byKey: Map<string, Session[]> | undefined;
  for (const [id, launch] of Object.entries(launches)) {
    if (launch.handoffAt === undefined) continue;
    let done = launch.phase === "failed" || now - launch.handoffAt >= LAUNCH_HANDOFF_MS;
    if (!done && launch.phase === "ready" && launch.session) {
      if (!byKey) {
        byKey = new Map();
        for (const r of rows) {
          const k = `${r.machine_id}\u0000${r.session_id}`;
          const list = byKey.get(k);
          if (list) list.push(r);
          else byKey.set(k, [r]);
        }
      }
      const matches = byKey.get(`${launch.machineId}\u0000${launch.session.session_id}`);
      if (matches?.some((r) => launchHandoffSettled(r, launch, now))) done = true;
    }
    if (!done) continue;
    next ??= { ...launches };
    next[id] = { ...launch, handoffAt: undefined };
  }
  return next ?? launches;
}

/** Milliseconds until the earliest active ready-launch handoff expires, or undefined when none is pending. */
export function nextLaunchExpiry(launches: Record<string, AgentLaunch>, now: number): number | undefined {
  let soonest = Infinity;
  for (const launch of Object.values(launches)) {
    if (launch.phase !== "ready" || launch.handoffAt === undefined) continue;
    soonest = Math.min(soonest, Math.max(0, launch.handoffAt + LAUNCH_HANDOFF_MS - now));
  }
  return soonest === Infinity ? undefined : soonest;
}

type LaunchRowObserver = (rows: readonly Session[]) => void;
let rowObserver: LaunchRowObserver | undefined;

/** Connects store settlement to out-of-store full-list snapshots without adding store imports to sessions.ts. */
export function registerLaunchRowObserver(observer: LaunchRowObserver) {
  rowObserver = observer;
}

export function observeLaunchRows(rows: readonly Session[]) {
  rowObserver?.(rows);
}
