// The hub's view of a to-do read in the web's own words (#10226; docs/web/web.md "Developing Persona"): which of today's
// sections (lib/todo-triage.ts) a row's `view` stands for, and which differences between the two the redesign decided
// (docs/design/proposals/todos-redesign.md 6.3; docs/assistant/todo-status-sop.md "The hub's view"), so the shadow
// (view-shadow.ts) reports only the ones nothing explains. Pure: no state, no clock of its own.

import { TODO_LIST_SECTIONS, TODO_REASONS, type TodoSummary } from "../../../lib/todo-view-wire";
import type { Triage, TriageRule, TriageSection } from "../../../lib/todo-triage";
import type { SessionLookup, Todo } from "../../../lib/todos";
import { activityOf } from "../../../lib/format";
import { usageLimitAgent } from "../../../lib/usage-limit";

/** lib/todo-view-wire.ts TODO_PHASES, kept local so the shared first-screen todo-view-wire chunk stays unchanged
 *  (view-legacy.test.ts keeps the two equal). */
export const KNOWN_PHASES: readonly string[] = ["needs_you", "working", "waiting", "ready", "todo", "someday", "done"];
const PHASES: ReadonlySet<string> = new Set(KNOWN_PHASES);
const SECTIONS: ReadonlySet<string> = new Set(TODO_LIST_SECTIONS);
const REASONS: ReadonlySet<string> = new Set(TODO_REASONS);

/** A view this web can read: a row with a newer hub's phase, section or reason is drawn as if it had none, and counted apart. */
export function knownView(view: TodoSummary | undefined): view is TodoSummary {
  return !!view && PHASES.has(view.phase) && SECTIONS.has(view.section) && REASONS.has(view.reason);
}

/** Today's section for each of the view's: Waiting is the web's Parked (a parked hold and an open PR wait there, 6.3
 *  rows 8 and 10), Can wait is listed under Needs you, Someday is open work, and To do is Other open, or Shelved while
 *  it carries the Inactive mark. */
const LEGACY: Record<(typeof TODO_LIST_SECTIONS)[number], TriageSection> = {
  needs_you: "waiting",
  can_wait: "waiting",
  working: "working",
  waiting: "parked",
  todo: "other",
  just_finished: "just_done",
  someday: "other",
  done: "done",
};

export function legacyOf(view: TodoSummary, freshClosedConcern = false): TriageSection | undefined {
  // #5732: an ordinary close is Done immediately; a fresh closed concern keeps the hub's Just finished row.
  if (view.reason === "closed_unseen" && !freshClosedConcern) return "done";
  // Cancelled work keeps its own section rather than being folded into Done.
  if (view.reason === "canceled") return "cancelled";
  if (view.section === "todo" && view.inactive) return "shelved";
  return LEGACY[view.section as keyof typeof LEGACY];
}

/** What the row's facts say that today's rules read differently from the hub's: its runs, read from the same sessions
 *  as the hub reads them, and the few facts the table below needs besides today's (section, rule). */
export interface RowFacts {
  /** A linked run the session lookup does not have: the web reads it as offline, the hub may list it. */
  missing: boolean;
  /** A run stopped on a usage limit that has not reset yet. */
  usageLimit: boolean;
  /** A run whose turn ended blocked on another Agent or on an outside event it names (the hub's waitsOn). */
  waits: boolean;
  /** A run whose moment's model failure the hub retries ("Continue" on its way: the hub's retrying). */
  transient: boolean;
  /** A run that stopped on a plain-text question nobody answered. */
  awaitsReply: boolean;
  /** A failed start whose failure is 48 hours old or more: the hub files it as nothing works on it (D4). */
  failedLong: boolean;
  /** It has a due date: the hub ends the day in the owner's zone, the web in the browser's. */
  due: boolean;
  /** This browser restored it after the hub's last restore, which the hub has no record of. */
  restoredHere: boolean;
  /** Fresh facts say one of its PRs is open (lib/todo-triage.ts openMergePRs). */
  prOpen: boolean;
}

/** The hub's failedStartFor: a failed start needs the owner this long after the failure. */
const FAILED_START_MS = 48 * 3_600_000;
/** The hub's retryGrace: a scheduled retry still counts this long past its wake. */
const RETRY_GRACE_MS = 5 * 60_000;

/** What the shadow knows beside the row: this browser's own restore of it, and whether a PR of it is open, both read
 *  through today's code. */
export interface RowHere {
  restored?: number;
  prOpen?: boolean;
}

export function rowFacts(todo: Todo, lookup: SessionLookup | undefined, now: number, here: RowHere = {}): RowFacts {
  const failedAt = todo.start_error ? ms(todo.done_at) || ms(todo.updated_at) : 0;
  const out: RowFacts = {
    missing: false,
    usageLimit: false,
    waits: false,
    transient: false,
    awaitsReply: false,
    failedLong: failedAt > 0 && now - failedAt >= FAILED_START_MS,
    due: Boolean(todo.due_at || todo.due_date),
    restoredHere: (here.restored ?? 0) > ms(todo.restored_at),
    prOpen: Boolean(here.prOpen),
  };
  for (const a of todo.agents ?? []) {
    if (a.released_at || a.start_error) continue;
    const s = lookup?.(a.machine_id, a.session_id);
    if (!s) {
      out.missing = true;
      continue;
    }
    if (usageLimitAgent(s, now)) out.usageLimit = true;
    const st = activityOf(s);
    if (s.awaits_reply && (st === "unread" || st === "idle")) out.awaitsReply = true;
    // the hub's blockedOn: what a turn that is over and not on a card ended blocked on
    const w = s.work;
    if (w?.phase !== "blocked" || st === "working" || st === "needs_you" || st === "unread") continue;
    // another Agent, or an outside event it names; never a usage limit's own wait, nor background commands or a /loop
    if (w.on === "agent" || (w.on === "external" && (w.wait || w.agent) && !w.resets_at)) out.waits = true;
    const wake = ms(w.wake_at);
    if (w.on === "transient" && wake && now < wake + RETRY_GRACE_MS) out.transient = true;
  }
  return out;
}

function ms(at: string | null | undefined): number {
  const v = at ? Date.parse(at) : NaN;
  return Number.isNaN(v) ? 0 : v;
}

/** The restore the shadow gives today's triage: this browser's own, the hub's (`restored_at`), and a Someday that has
 *  passed, which the hub counts as activity too, so both read the same moves. */
export function restoredAt(todo: Todo, local: number, now: number): number {
  const back = ms(todo.someday_until);
  return Math.max(local, ms(todo.restored_at), back && back <= now ? back : 0);
}

/** A difference the redesign decided, by the 6.3 row (or decision) that changed today's rule. */
export type ByDesign =
  | "failed_start"
  | "steps"
  | "run_waits"
  | "usage_limit"
  | "awaits_reply"
  | "stopped_unfinished"
  | "finished_unseen"
  | "finished_recent"
  | "finished_seen"
  | "someday"
  | "inactive"
  | "supervisor_holds";

const FINISHED_DONE: ReadonlySet<TriageRule> = new Set(["seen", "accepted"]);
const AT_REST: ReadonlySet<TriageRule> = new Set(["rest", "stale"]);
/** Today's rules for what the hub places above row 4 (rows 1 to 3) or as closed (rows 6 and 7). */
const ABOVE_CAN_WAIT: ReadonlySet<TriageRule> = new Set(["failed_start", "superseded", "agent_asks", "owner_decides", "closed"]);
/** The reasons a run places a to-do by: what the web's one usage-limit status hides of its other runs. */
const RUN_PLACED: ReadonlySet<string> = new Set(["agent_running", "starting", "background", "persona_working", "usage_limit", "someone_else", "finished_unseen"]);
/** The sections a step carries its parent into (the hub's stepOrder), and those a step's Can wait lifts it from. */
const STEP_SECTIONS: ReadonlySet<string> = new Set(["needs_you", "working", "waiting", "can_wait"]);
const CAN_WAIT_LIFTS: ReadonlySet<TriageSection> = new Set(["other", "just_done", "shelved", "done"]);
/** lib/todo-triage.ts TRIAGE_SECTIONS, which this chunk may not import (view-legacy.test.ts keeps the two equal). */
export const PAGE_ORDER: readonly TriageSection[] = ["waiting", "working", "parked", "other", "just_done", "shelved", "done", "cancelled"];
const ahead = (a: TriageSection | undefined, b: TriageSection) => a !== undefined && PAGE_ORDER.indexOf(a) < PAGE_ORDER.indexOf(b);

/** The decided differences, first that holds: each names the view it expects and the web's (section, rule) it replaces.
 *  Parked in Waiting and Can wait under Needs you need no entry: the mapping above files them where the web does. */
export const BY_DESIGN: readonly { why: ByDesign; holds: (local: Triage, view: TodoSummary, facts: RowFacts) => boolean }[] = [
  // D4, row 1: a failed start needs the owner for 48 h from the failure, then is filed as nothing works on it (To do,
  // or Someday); the web counts its 48 h from the last activity and files it in Done (an open concern in Other open)
  { why: "failed_start", holds: (l, v, f) => f.failedLong && (l.rule === "failed_start" || AT_REST.has(l.rule)) && (v.section === "todo" || v.section === "someday") },
  // 3.4 "Steps roll up": the row carries its most urgent step's state (its line names the step) when that is ahead of
  // its own; a step's Can wait lifts only a parent in To do, Just finished or Someday. The web reads the parent alone
  {
    why: "steps",
    holds: (l, v) =>
      v.line?.args?.step !== undefined && STEP_SECTIONS.has(v.section) && ahead(legacyOf(v), l.section) && (v.section !== "can_wait" || CAN_WAIT_LIFTS.has(l.section)),
  },
  // D11: a run that waits on another Agent or an outside event leaves Working for Waiting (or Just finished, when row 9
  // holds first); a moment's model failure the hub retries is Working (background), which the web reads as cut off
  {
    why: "run_waits",
    holds: (l, v, f) =>
      (l.rule === "agent_running" && f.waits && v.reason !== "usage_limit" && (v.section === "waiting" || v.section === "just_finished")) ||
      (l.rule === "agent_asks" && f.transient && v.reason === "background"),
  },
  // row 10: a usage limit waits for its reset; the web's one status for it hides every other run, so it files the to-do
  // at rest where the hub places it by its runs (one that works, waits or finished unseen first). When another run's
  // external wait keeps the web in Working, the hub's usage-limit view still takes precedence over that wait.
  { why: "usage_limit", holds: (l, v, f) => f.usageLimit && ((AT_REST.has(l.rule) && RUN_PLACED.has(v.reason)) || (l.rule === "agent_running" && v.reason === "usage_limit")) },
  // row 4: a run that ended on a plain-text question is Can wait, ahead of everything below row 3 but closed work
  { why: "awaits_reply", holds: (l, v, f) => v.reason === "awaits_reply" && f.awaitsReply && !ABOVE_CAN_WAIT.has(l.rule) },
  // #9823: an unfinished stop unattended for an hour needs follow-up; the legacy page still reads its unread reply
  // or pending review as a finish. The assistant tray already uses the closeout; this compares the old page only.
  { why: "stopped_unfinished", holds: (l, v) => v.section === "needs_you" && v.reason === "stopped_unfinished" && l.section === "just_done" && l.rule === "finished_unseen" },
  // row 9: accepted work nobody has seen stays in Just finished however long (the web: Done after 24 h)
  { why: "finished_unseen", holds: (l, v) => v.reason === "finished_unseen" && l.rule === "accepted" && l.section === "done" },
  // row 9: finished work stays in Just finished for 24 h after it was seen (the web: Done once seen), ahead of an open
  // PR (row 10), which the web puts first, or reads as no wait at all for a review not done yet
  {
    why: "finished_recent",
    holds: (l, v, f) =>
      v.reason === "finished_recent" && ((FINISHED_DONE.has(l.rule) && l.section === "done") || l.rule === "pr_open" || (l.rule === "rest" && f.prOpen)),
  },
  // row 11, rule 9a: finished work seen over 24 h ago and still open is a To do to tick, never Done
  { why: "finished_seen", holds: (l, v) => v.reason === "finished_seen" && FINISHED_DONE.has(l.rule) && l.section === "done" },
  // row 12 (D7): Someday is the hub's alone; the web may shelve a to-do put off for long
  { why: "someday", holds: (l, v) => v.section === "someday" && AT_REST.has(l.rule) },
  // row 13: the hub reads the last activity and who may be shelved as the web does (the shadow gives today's triage the
  // hub's restores), so the Inactive mark differs from Shelved only where the hub knows otherwise: a computer that does
  // not answer (whose runs the web lacks too), a due day in the owner's zone, and a restore of this browser's own
  { why: "inactive", holds: (l, v, f) => v.section === "todo" && AT_REST.has(l.rule) && (f.missing || f.due || f.restoredHere) },
  // rows 1, 2: a supervised task's failed start, and a run's card its supervisor holds (delegating), are the supervising
  // conversation's until it passes them up: Working, said as Persona's. The web's triage reads the session's state and
  // the start error alone and files both as waiting on the owner
  { why: "supervisor_holds", holds: (l, v) => (l.rule === "failed_start" || l.rule === "agent_asks") && v.section === "working" && v.reason === "persona_working" },
];

/** How a row's view compares with today's triage of the same row. */
export type ViewVerdict =
  | { kind: "same" }
  | { kind: "by_design"; why: ByDesign }
  /** A section or reason this web does not know: a newer hub's. */
  | { kind: "unknown" }
  /** The web lacks a run's session (outside the Agents window), so it reads the run as offline. */
  | { kind: "client_gap" }
  /** The view changes by time alone within BOUNDARY_MS, or already has (`next_change_at`): the hub sends no new list
   *  for that, and the web read its clock a moment later. */
  | { kind: "boundary" }
  | { kind: "mismatch" };

export const BOUNDARY_MS = 2 * 60_000;

function outdated(view: TodoSummary, now: number): boolean {
  const at = ms(view.next_change_at);
  return at > 0 && at <= now + BOUNDARY_MS;
}

export function classify(local: Triage, view: TodoSummary, facts: RowFacts, now: number): ViewVerdict {
  if (!knownView(view)) return { kind: "unknown" };
  if (legacyOf(view, local.rule === "closed" && local.section === "just_done") === local.section) return { kind: "same" };
  const hit = BY_DESIGN.find((d) => d.holds(local, view, facts));
  if (hit) return { kind: "by_design", why: hit.why };
  if (facts.missing) return { kind: "client_gap" };
  if (outdated(view, now)) return { kind: "boundary" };
  return { kind: "mismatch" };
}
