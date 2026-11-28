// The To-dos page's sections (#3156, docs/assistant/todo-status-sop.md): each to-do's place comes from real signals only — its
// agents' sessions (alive, running, asking), its PR / delivery state, the latest task_review verdict and when it last moved —
// never from what an agent says about itself. The rules are the SOP's table, in its order; the first that holds wins.

import { acceptanceStands, decisionDismissed, latestReview, reviewState } from "./supervision";
import { heldDelivery, isCancelled, normalizedPR, openAsk, openMergePRs, PR_STATUS_FRESH_MS, todoDueBy, todoStatus, waitsForOwner, type PRFacts, type SessionLookup, type Todo, type TodoStatus } from "./todos";
export { normalizedPR, openMergePRs, PR_STATUS_FRESH_MS };
import { TODO_LIST_SECTIONS, TODO_REASONS } from "./todo-view-wire";

/** Where a to-do shows: merge waits join explicit holds on the web (#9325); cancelled stays apart from Done (#5732). */
export type TriageSection = "waiting" | "working" | "parked" | "just_done" | "other" | "shelved" | "done" | "cancelled" | "can_wait" | "todo";

/** The standard order (#5707). The server board provides its own order and two additional sections. */
export const TRIAGE_SECTIONS: readonly TriageSection[] = ["waiting", "working", "parked", "other", "just_done", "shelved", "done", "cancelled"];
export const ALL_TRIAGE_SECTIONS: readonly TriageSection[] = [...TRIAGE_SECTIONS, "can_wait", "todo"];

/** Sections that start folded by default: unfinished to-dos stay open (#5707); cancelled folds apart from Done (#5732). */
export const FOLDED_SECTIONS: ReadonlySet<TriageSection> = new Set(TRIAGE_SECTIONS.slice(-3));

/** No activity for this long, nothing of it running: shelved (SOP rule 11). */
export const SHELVE_AFTER_MS = 48 * 3600_000;
export const JUST_DONE_MS = 24 * 3600_000;

/** A fresh accepted, owner-ticked or cancelled outcome remains observable in its own section; older history and plain
 *  legacy review-done groups keep the folded default (#11677, #3713). Shelved always starts folded. */
export function sectionStartsFolded(section: TriageSection, groups: readonly { last: number; lead: Pick<Todo, "cancelled_at" | "done_at" | "done_by" | "supervision"> }[], now: number): boolean {
  if (!FOLDED_SECTIONS.has(section)) return false;
  if (section !== "done" && section !== "cancelled") return true;
  return !groups.some((group) => {
    if (group.last > now || now - group.last >= JUST_DONE_MS) return false;
    const outcome = group.lead.supervision?.state;
    return isCancelled(group.lead) || (group.lead.done_by !== "review" && group.lead.done_by !== "auto") || outcome === "accepted" || outcome === "cancelled";
  });
}

/** True only for a fresh open non-draft PR of the to-do with no active agent or owner decision. */
export function waitingForMerge(todo: Todo, lookup: SessionLookup | undefined, now: number): boolean {
  if (todo.done_at || !openMergePRs(todo, now).length) return false;
  const status = todoStatus(todo, lookup);
  if (status === "failed" || status === "needs_you" || status === "interrupted" || status === "working" || status === "waiting") return false;
  if (hasActiveAgentWork(todo, lookup)) return false;
  if (reviewState(todo) === "user_check") return false;
  if (!decisionDismissed(todo) && waitsForOwner(todo)) return false;
  return true;
}

/** What holds one PR (its own facts), or the to-do's current PR when none is given. */
export function prBlockers(todo: Todo, facts: PRFacts | undefined = todo.pr_status): string[] {
  if (!facts) return [];
  const blockers: string[] = [];
  if (facts.review === "required") blockers.push("review_required");
  if (facts.review === "changes_requested") blockers.push("changes_requested");
  if (facts.checks === "failure") blockers.push("checks_failed");
  if (facts.checks === "pending") blockers.push("checks_pending");
  if (facts.conflicts) blockers.push("conflicts");
  if (!blockers.length) blockers.push("waiting_maintainer");
  return blockers;
}

/** Which SOP rule placed a to-do (for tests and the SOP's wording); the section follows from it. */
export type TriageRule =
  | "failed_start"
  | "cancelled"
  | "superseded"
  | "agent_asks"
  | "owner_decides"
  | "delivery_ok"
  | "agent_running"
  | "parked"
  | "closed"
  | "accepted"
  | "finished_unseen"
  | "seen"
  | "pr_open"
  | "stale"
  | "rest";

export interface Triage {
  section: TriageSection;
  rule: TriageRule;
  /** The last moment anything of it moved, ms (0 when nothing says). */
  last: number;
}

function ms(at: string | null | undefined): number {
  if (!at) return 0;
  const v = Date.parse(at);
  return Number.isNaN(v) ? 0 : v;
}

/** Unread on one session does not hide another session's live turn or background work. */
export function hasActiveAgentWork(todo: Todo, lookup: SessionLookup | undefined): boolean {
  return (todo.agents ?? []).some((a) => {
    if (a.released_at || a.start_error) return false;
    const background = lookup?.(a.machine_id, a.session_id)?.background_working;
    return background || ["working", "waiting"].includes(todoStatus({ ...todo, agents: [a] }, lookup));
  });
}

/** The last moment the to-do or any of its agents moved: its own times, its supervision's (reviews, reports), its
 *  delivery and ask, and each linked agent's session. A restore from Shelved counts as a move (`restoredAt`). */
export function lastActivity(todo: Todo, lookup?: SessionLookup, restoredAt = 0): number {
  const sup = todo.supervision;
  let last = Math.max(
    ms(todo.updated_at),
    ms(todo.created_at),
    ms(todo.reopened_at),
    ms(todo.done_at),
    ms(sup?.updated_at),
    ms(sup?.progress?.at),
    ms(latestReview(sup)?.at),
    ms(todo.delivery?.at),
    ms(todo.ask?.asked_at),
    restoredAt,
  );
  for (const a of todo.agents ?? []) {
    if (a.released_at) continue;
    const s = lookup?.(a.machine_id, a.session_id);
    if (s) last = Math.max(last, ms(s.updated_at), ms(s.state_since));
  }
  return last;
}

/** Whether a to-do may be shelved at all: it is a task (an agent was linked, or a conversation handed it out) with no
 *  due date ahead. The user's own to-dos without agents — reminders, errands — never shelve themselves. An open concern
 *  never shelves either: it waits for its report (docs/assistant/todos.md "Concerns", #3582). */
function shelvable(todo: Todo, now: number): boolean {
  if (todo.kind === "concern") return false;
  const due = todoDueBy(todo);
  if (due !== undefined && due >= now) return false;
  return (todo.agents ?? []).length > 0 || Boolean(todo.origin);
}

/**
 * A to-do's section by the SOP's table (docs/assistant/todo-status-sop.md, "Decision table"), first match wins:
 *
 *  1. failed_start    the task's agent never started                    → Waiting on you (retry or remove), Done after 48 h;
 *     superseded      … and a related to-do (its row) really finished   → Done immediately (#3713)
 *  2. agent_asks      an agent asks (question, approval) or was cut off → Waiting on you
 *  3. owner_decides   review says needs_user, a decision / "cannot" ask, or delivery_ok → Waiting on you
 *  4. agent_running   an agent is working or starting                   → Working
 *  5. closed          done_at set                                       → Done immediately
 *  5a. parked         an explicit hold                                   → Parked until the hub changes its state
 *  6. accepted        the latest review accepted it (even reopened)     → Done immediately
 *  7. finished_unseen its delivery is live, not accepted                → Delivered · awaiting review, with no timeout
 *  8. finished_unseen an agent finished unseen, awaits review, or a done ask → Delivered · awaiting review
 *  9. pr_open         fresh matching open non-draft PR facts, nothing active or awaiting owner → Parked (merge wait)
 *  9a. seen           rule 8 without supervision, seen since it last moved, no PR open → Done
 *
 * Seen (#5707) files only an unsupervised finished turn. A supervised task or an unaccepted live delivery remains
 * awaiting review even after the owner opens it (#5732).
 * 10. stale           a task with no activity for 48 h                  → Shelved
 * 11. rest            anything else                                     → Other open
 */
export function triage(todo: Todo, lookup: SessionLookup | undefined, now: number, restoredAt = 0, superseded = false): Triage {
  const last = lastActivity(todo, lookup, restoredAt);
  const status: TodoStatus = todoStatus(todo, lookup);
  const review = reviewState(todo);
  // Seen files an unsupervised finished turn (#5707), never a task that still needs acceptance (#5732).
  // Explicit closure and acceptance are already Done; Seen does not move them through awaiting review.
  const seenAt = ms(todo.seen_at);
  const seen = (at: number) => seenAt > 0 && seenAt >= at;
  const fresh = (at: number) => now - at < JUST_DONE_MS && !seen(at);
  const ask = openAsk(todo);
  const at = (section: TriageSection, rule: TriageRule): Triage => ({ section, rule, last });
  // An open concern closes only with its report: nothing else files it with the finished ones.
  const concern = todo.kind === "concern" && !todo.done_at;

  if (isCancelled(todo)) return at("cancelled", "cancelled");
  if (status === "failed") {
    // the same piece of work finished in a related to-do (`supersededFailedStarts`): history, nothing left to retry
    if (superseded) return at("done", "superseded");
    if (now - last < SHELVE_AFTER_MS) return at("waiting", "failed_start");
    if (concern) return at("other", "rest");
    return at("done", "failed_start");
  }
  if (status === "needs_you" || status === "interrupted") return at("waiting", "agent_asks");
  if (!todo.done_at && reviewState(todo) === "user_check") return at("waiting", "owner_decides");
  // a decision the owner chose Don't handle on no longer waits on them (decisionDismissed)
  if (!decisionDismissed(todo) && waitsForOwner(todo)) return at("waiting", "owner_decides");
  if (heldDelivery(todo)?.unblock === "user") return at("waiting", "delivery_ok");
  // A projected wait includes run, step and active-blocker waits that have no local park or PR record.
  // Live owner requests above still have an actionable home.
  if (todo.wait != null && !todo.done_at) return at("parked", todo.wait.kind === "pr_merge" ? "pr_open" : "parked");
  // rule 8's signals, read here so rule 9 yields to them: a turn or review still awaiting acceptance stays Just finished
  const finished = status === "unread" || review === "pending_review" || (ask && !ask.outcome) || ask?.outcome === "done";
  const acceptanceRequired = review === "pending_review" || Boolean(todo.supervision?.supervisor);
  // a parked hold is its supervisor's reading of the latest turn: an agent session left unread does not hide its PR
  // accepted or delivered work keeps its finished semantics whatever its PR facts say (#7614, #5732)
  const settled = !concern && (acceptanceStands(todo) || todo.delivery?.state === "live");
  if (!settled && !(finished && (!seen(last) || acceptanceRequired) && review !== "parked") && waitingForMerge(todo, lookup, now)) {
    // Only work with no acceptance still required becomes an external merge wait.
    return at("parked", "pr_open");
  }
  if (status === "working" || status === "waiting") return at("working", "agent_running");
  if (todo.done_at) return at(todo.kind === "concern" && fresh(ms(todo.done_at)) ? "just_done" : "done", "closed");
  if (!settled && hasActiveAgentWork(todo, lookup)) return at("working", "agent_running");
  // A fallback date is not a state transition: keep overdue waits visible until the hub actually wakes them.
  if (review === "parked") return at("parked", "parked");
  if (!concern && acceptanceStands(todo)) return at("done", "accepted");
  // Live is delivery, not acceptance: an unreviewed live task stays open and awaiting review, however old or seen (#5732).
  if (!concern && todo.delivery?.state === "live") return at("just_done", "finished_unseen");
  // Live returned above. Every other supervised or review-pending task still needs acceptance after Seen.
  if (finished && (!seen(last) || acceptanceRequired)) return at("just_done", "finished_unseen");
  // seen since it last moved, and no PR waits on the owner (an open concern is never Done open)
  if (finished && !concern) return at("done", "seen");
  if (shelvable(todo, now) && now - last >= SHELVE_AFTER_MS) return at("shelved", "stale");
  return at("other", "rest");
}

/** When a to-do the triage filed with the finished ones (Just finished or Done) finished, ms: the moment its rule reads
 *  above — its close, its accepting review, its deploy — else its last move (a finished turn the owner has seen since).
 *  The Assistant's Finished entry lists what finished less than JUST_DONE_MS ago, seen or not (#10922). 0 when nothing
 *  says. */
export function finishedAt(todo: Todo, t: Triage): number {
  switch (t.rule) {
    case "closed": return ms(todo.done_at);
    case "accepted": return ms(latestReview(todo.supervision)?.at);
    case "finished_unseen": return todo.delivery?.state === "live" ? ms(todo.delivery.at) || t.last : t.last;
    case "superseded": return ms(todo.done_at) || t.last;
    default: return t.last;
  }
}

/** Whether a to-do counts toward the header's open count: not closed, accepted or shelved. An unaccepted delivery
 *  still counts after being seen; only an unsupervised finished turn can be filed by Seen (#5707, #5732). */
export function countsAsOpen(todo: Todo, t: Triage): boolean {
  // a failed start has done_at too (the hub ends it), yet while it waits on the owner it is theirs to retry: open
  if (t.rule === "failed_start") return t.section === "waiting";
  if (todo.done_at || t.section === "cancelled" || t.section === "shelved" || t.section === "done") return false;
  return t.rule !== "accepted";
}

/** Where a to-do stands for every count of open work (#3331): the To-dos page's "Not done" and the conversation's
 *  Progress entry read this one rule, so the two numbers agree. `open` counts, `shelved` folds (no activity for 48 h,
 *  nothing running), `closed` is finished (done, accepted, an old failed start). */
export type TodoBucket = "open" | "shelved" | "closed";

export function todoBucket(todo: Todo, lookup: SessionLookup | undefined, now: number, restoredAt = 0, superseded = false): TodoBucket {
  const t = triage(todo, lookup, now, restoredAt, superseded);
  if (countsAsOpen(todo, t)) return "open";
  return t.section === "shelved" ? "shelved" : "closed";
}

// ── Waiting on the owner, and the sidebar's badge ─────────────

/** What a to-do waits on its owner for (waitsOnOwner): its Agent never started and is theirs to retry (rule 1), an Agent
 *  of it asks something (a question, an approval) or was cut off (rule 2), a decision or review only they can make
 *  (rule 3), or its finished work is held short of live for their OK (`delivery.unblock` user, heldDelivery). */
export type OwnerWait = "failed_start" | "agent_asks" | "owner_decides" | "delivery_ok";

/**
 * Whether a to-do waits on its owner in person, and for what: the one reading of "it needs you" that the sidebar's
 * badge (ownerBadge) and the To-dos page's labels share, so a to-do that says it waits for you is the one the badge
 * counts. Pure: it reads only the to-do and its placement.
 *
 * @param todo the to-do
 * @param t    its placement: `triage(todo, …)`, or the page's `TriageGroup.triage.get(todo.id)`
 * @returns    what it waits on the owner for, or null when it does not wait on them (closed, shelved, superseded, or
 *             waiting on an Agent, a PR's reviewers, CI or anyone else). Only an open to-do (countsAsOpen) waits.
 */
export function waitsOnOwner(todo: Todo, t: Triage): OwnerWait | null {
  if (!countsAsOpen(todo, t)) return null;
  const rule = t.rule;
  if (t.section === "waiting" && (rule === "failed_start" || rule === "agent_asks" || rule === "owner_decides" || rule === "delivery_ok")) return rule;
  return heldDelivery(todo)?.unblock === "user" ? "delivery_ok" : null;
}

/** Whether an Agent is on it: the page files it under Working (rule 4), so one of its own Agents is working or starting,
 *  or waits for something other than the owner to bring it back. A stopped, finished or archived Agent is not on it. */
export function agentOnIt(t: Triage): boolean {
  return t.section === "working";
}

/** Why the sidebar's To-dos badge counts a to-do (ownerNeeds): what it waits on the owner for (rule 1, OwnerWait), or
 *  `unattended`, a to-do of the owner's own that no Agent is on and none of its pieces carries (rule 4). The To-dos page
 *  says it on the row's mark (#11838); a later reason is one more member here, decided in ownerNeeds, so the badge and
 *  the mark keep one rule. */
export type OwnerNeed = OwnerWait | "unattended";

const VIEW_SECTIONS: ReadonlySet<string> = new Set(TODO_LIST_SECTIONS);
const VIEW_REASONS: ReadonlySet<string> = new Set(TODO_REASONS);

/**
 * What a to-do waits on its owner for by the hub's view of it (`view`, #10226), when the hub sent one this web knows:
 * Needs you only, and Can wait's own decisions. The view already leaves with their supervisor what a supervising
 * conversation handles (a supervised task's failed start, a card its supervisor holds): those wait on Persona, not on
 * the owner. undefined: no view this web can read, so the web's own triage decides (waitsOnOwner).
 */
export function viewWaitsOnOwner(todo: Todo): OwnerWait | null | undefined {
  const v = todo.view;
  if (!v || !VIEW_SECTIONS.has(v.section) || !VIEW_REASONS.has(v.reason)) return undefined;
  if (heldDelivery(todo)?.unblock === "user" && v.phase === "needs_you") return "delivery_ok";
  if (v.section === "can_wait") return v.reason === "can_wait" ? "owner_decides" : null;
  if (v.section !== "needs_you") return null;
  switch (v.reason) {
    case "failed_start":
      return "failed_start";
    case "agent_asks":
    case "interrupted":
    case "stopped_unfinished":
      return "agent_asks";
    case "owner_decides":
    case "verify":
    case "steps_proposed":
    case "signin":
    case "can_wait":
    case "awaits_reply":
    case "persona_working":
    case "agent_running":
    case "starting":
    case "queued":
    case "background":
    case "closed_unseen":
    case "completed":
    case "canceled":
    case "reported":
    case "superseded":
    case "parked":
    case "finished_unseen":
    case "finished_recent":
    case "pr_open":
    case "usage_limit":
    case "computer_offline":
    case "scheduled_report":
    case "someone_else":
    case "finished_seen":
    case "deferred":
    case "rest":
      return "owner_decides";
  }
  // TodoReason is closed and VIEW_REASONS let through only its values: a new one fails to compile above.
  const exhaustive: never = v.reason;
  return exhaustive;
}

/**
 * Whether the hub counts this to-do in its `needs_you` (projection Counts, `GET todos` counts.needs_you): its view's
 * section is Needs you, it is not quiet, and it is not a step an open parent carries (a step is counted with its
 * parent; a to-do the owner answers on its own, `todo_od_…`, never is). undefined: no view this web can read.
 * `open` says whether a to-do of this list is still open by its view, for the parent test.
 */
export function hubNeedsYou(todo: Todo, open: (id: string) => boolean): boolean | undefined {
  const v = todo.view;
  if (!v || !VIEW_SECTIONS.has(v.section) || !VIEW_REASONS.has(v.reason)) return undefined;
  if (v.section !== "needs_you" || todo.quiet) return false;
  return !(todo.part_of && todo.part_of !== todo.id && !todo.id.startsWith("todo_od_") && open(todo.part_of));
}

/** The open test hubNeedsYou takes, over a list: a to-do's view is not Done (one with no view: not done_at). */
export function openByView(todos: readonly Todo[]): (id: string) => boolean {
  const byId = new Map(todos.map((td) => [td.id, td]));
  return (id) => {
    const td = byId.get(id);
    if (!td) return false;
    return td.view ? td.view.phase !== "done" : !td.done_at;
  };
}

/**
 * The to-dos the sidebar's To-dos badge counts (#11085), by id, each with why (OwnerNeed): the open ones (countsAsOpen)
 * that need the owner, not those Agents are on. Quiet tasks (#1731) are left out, as before. Among the rest, the first
 * rule that holds wins:
 *
 *  1. it waits on the owner (the hub's view, viewWaitsOnOwner; without one, waitsOnOwner) → counts, for what it waits on
 *  2. one of its own Agents is on it (agentOnIt)                                    → does not count
 *  3. its pieces carry it: a to-do under it (`part_of`, at any depth) has an Agent on it or waits on the owner,
 *     and a piece that waits counts by itself, so the request is not counted twice   → does not count
 *  4. a to-do of the owner's own (no conversation handed it out: no `origin`) with no sign of an Agent on it (none
 *     was ever on it, its Agents stopped, finished work nobody has looked at)        → counts, `unattended`
 *  5. anything else: a conversation's task with nothing running is its supervising conversation's to pick up or close,
 *     not the owner's, until that conversation asks them                             → does not count
 *
 * A to-do the hub sent a view of is counted by the hub's own rule instead (hubNeedsYou), so the badge, the main
 * conversation's tray and the hub's needs_you are one number; the rules above decide only for to-dos without one (an
 * older hub).
 *
 * The page's "Not done" stays countsAsOpen, so the badge is never more than it. A `part_of` loop is walked once. Its
 * size is the badge's number, and the To-dos page marks exactly these rows (lib/todo-owner-needs.ts reads it for both).
 */
export function ownerNeeds(todos: Todo[], lookup: SessionLookup | undefined, now: number, restored: ReadonlyMap<string, number> = new Map()): ReadonlyMap<string, OwnerNeed> {
  // decided on every to-do, as the page decides it (quiet ones too: their Agents and asks still carry a request)
  const superseded = supersededFailedStarts(todos);
  const placed = new Map(todos.map((td) => [td.id, triage(td, lookup, now, restored.get(td.id) ?? 0, superseded.has(td.id))]));
  const waits = (td: Todo, t: Triage): OwnerWait | null => {
    const v = viewWaitsOnOwner(td);
    return v === undefined ? waitsOnOwner(td, t) : v;
  };
  const pieces = new Map<string, Todo[]>();
  for (const td of todos) {
    if (!td.part_of) continue;
    const list = pieces.get(td.part_of);
    if (list) list.push(td);
    else pieces.set(td.part_of, [td]);
  }
  const carried = (id: string): boolean => {
    const seen = new Set([id]);
    const stack = [...(pieces.get(id) ?? [])];
    while (stack.length) {
      const td = stack.pop()!;
      if (seen.has(td.id)) continue;
      seen.add(td.id);
      const t = placed.get(td.id)!;
      if (agentOnIt(t) || waits(td, t)) return true;
      stack.push(...(pieces.get(td.id) ?? []));
    }
    return false;
  };
  const counted = new Map<string, OwnerNeed>();
  const open = openByView(todos);
  for (const td of todos) {
    const hub = hubNeedsYou(td, open);
    if (hub !== undefined) {
      if (hub) counted.set(td.id, viewWaitsOnOwner(td) || "owner_decides");
      continue;
    }
    const t = placed.get(td.id)!;
    if (td.quiet || !countsAsOpen(td, t)) continue;
    // Completed tasks awaiting optional owner verification (`user_check`) and unescalated model errors on supervised
    // tasks stay out of the urgent Needs you badge, matching the hub projection.
    const rawStatus = todoStatus(td, lookup);
    const userCheckUrgent =
      rawStatus === "needs_you" ||
      (rawStatus === "interrupted" && !td.supervision?.supervisor) ||
      heldDelivery(td)?.unblock === "user" ||
      waitsForOwner(td) ||
      Boolean(openAsk(td)?.questions?.length);
    if (!td.done_at && reviewState(td) === "user_check" && !userCheckUrgent) continue;
    if (td.supervision?.supervisor && rawStatus === "interrupted" && !waitsForOwner(td) && heldDelivery(td)?.unblock !== "user") continue;
    const wait = waits(td, t);
    if (wait) counted.set(td.id, wait);
    else if (!td.origin && !agentOnIt(t) && !carried(td.id)) counted.set(td.id, "unattended");
  }
  return counted;
}

/** The ids ownerNeeds counts: the badge's to-dos, its number their count. */
export function ownerBadge(todos: Todo[], lookup: SessionLookup | undefined, now: number, restored: ReadonlyMap<string, number> = new Map()): ReadonlySet<string> {
  return new Set(ownerNeeds(todos, lookup, now, restored).keys());
}

/** Whether a to-do's checkbox shows it open: not closed, or a failed start still waiting on the owner (rule 1), which
 *  the hub closed itself and the page counts as open (#3713). Ticking an open one closes it (`done: true`: for a failed
 *  start the hub clears its start error, so it moves to Just finished); unticking a closed one reopens it. */
export function checksAsOpen(todo: Todo, section?: TriageSection): boolean {
  if (!todo.done_at) return true;
  return Boolean(todo.start_error) && section === "waiting";
}

// ── Related to-dos ────────────────────────────────────────────

const ISSUE_REF = /#(\d{2,7})\b/;

/** The keys that make two to-dos the same piece of work: the same PR, the same issue number in the title, the same
 *  title once case, spaces and punctuation are dropped, or the same plan (its subtasks under one request). */
export function relatedKeys(todo: Todo): string[] {
  const keys: string[] = [];
  const pr = (todo.pr_url ?? "").trim().replace(/[#?].*$/, "").replace(/\/+$/, "").toLowerCase();
  if (pr) keys.push("pr:" + pr);
  const issue = ISSUE_REF.exec(todo.title);
  if (issue) keys.push("issue:" + issue[1]);
  const title = todo.title.toLowerCase().replace(/[\s\p{P}\p{S}]+/gu, "");
  // long enough that two short generic titles ("test it") do not merge
  if ([...title].length >= 6) keys.push("title:" + title);
  if (todo.plan_id) keys.push("plan:" + todo.plan_id);
  return keys;
}

/** The row each to-do is in: the root of its related to-dos (union of shared keys). Closed to-dos and open ones never
 *  share a row: a closed one is history, not part of the open work. An open concern always has a row of its own; a closed
 *  concern groups like any closed to-do. */
function relatedRoots(list: Todo[]): (id: string) => string {
  const parent = new Map(list.map((td) => [td.id, td.id]));
  const find = (id: string): string => {
    let r = id;
    while (parent.get(r) !== r) r = parent.get(r)!;
    parent.set(id, r);
    return r;
  };
  const owner = new Map<string, string>();
  for (const td of list) {
    if (!td.done_at && td.kind === "concern") continue;
    const side = isCancelled(td) ? "x" : td.done_at ? "c" : "o";
    for (const k of relatedKeys(td)) {
      const key = side + "|" + k;
      const other = owner.get(key);
      if (other === undefined) owner.set(key, td.id);
      else parent.set(find(td.id), find(other));
    }
  }
  return find;
}

/**
 * Failed starts (rule 1) whose row has a to-do that really finished: closed without a start error, so done by the user,
 * a review or the hub's completion check (#3713). The work got done another way; the failed start is history, not the
 * owner's to retry: it goes to Just finished / Done and counts as closed, on the page, the badge and Progress alike.
 */
export function supersededFailedStarts(todos: Todo[]): Set<string> {
  const root = relatedRoots(todos);
  const finishedRows = new Set(todos.filter((td) => td.done_at && !td.start_error && !isCancelled(td)).map((td) => root(td.id)));
  return new Set(todos.filter((td) => td.start_error && finishedRows.has(root(td.id))).map((td) => td.id));
}

/** Canonical stable key for a cluster of related to-dos: its lexicographically smallest member id, independent of
 *  which member currently leads by recency or urgency. */
export function triageGroupKey(members: readonly Pick<Todo, "id">[]): string {
  let key = members[0].id;
  for (let i = 1; i < members.length; i++) {
    if (members[i].id < key) key = members[i].id;
  }
  return key;
}

export interface TriageGroup {
  /** Stable cluster key (lexicographically smallest member id), independent of transient lead recency. */
  key: string;
  section: TriageSection;
  /** The member whose section is the group's, most recently active among those. */
  lead: Todo;
  /** Every member, the lead first, then by section and recency. */
  members: Todo[];
  /** Each member's triage, by id. */
  triage: ReadonlyMap<string, Triage>;
  last: number;
}

export interface TriageResult {
  sections: Record<TriageSection, TriageGroup[]>;
  /** To-dos (not rows) per section. */
  todoCounts: Record<TriageSection, number>;
  /** To-dos the header calls open (countsAsOpen), shelved, and finished (closed or accepted). */
  open: number;
  shelved: number;
  finished: number;
  /** Cancelled to-dos, counted separately from finished / completed ones (#5732). */
  cancelled: number;
}

const ORDER = new Map(TRIAGE_SECTIONS.map((s, i) => [s, i]));

/**
 * Every to-do placed in its section, related ones merged into one row (union of shared keys). Closed to-dos are not
 * merged into open ones' rows: a group is open members only, or closed members only. An open concern always has a row of
 * its own. A group takes its most urgent member's section (a superseded failed start never leads); rows in a section go
 * most recently active first. `quiet: false` leaves quiet tasks (#1731) out,
 * as the sidebar's badge (ownerBadge) does; the page lists them.
 */
export function triageTodos(
  todos: Todo[],
  lookup: SessionLookup | undefined,
  now: number,
  restored: ReadonlyMap<string, number> = new Map(),
  { quiet = true, superseded: known }: { quiet?: boolean; superseded?: ReadonlySet<string> } = {},
): TriageResult {
  const list = quiet ? todos : todos.filter((td) => !td.quiet);
  // a caller that narrows the list (search, a filter) passes the set decided on every to-do
  const superseded = known ?? supersededFailedStarts(list);
  const placed = new Map(list.map((td) => [td.id, triage(td, lookup, now, restored.get(td.id) ?? 0, superseded.has(td.id))]));
  const find = relatedRoots(list);
  const byRoot = new Map<string, Todo[]>();
  for (const td of list) {
    const r = find(td.id);
    const arr = byRoot.get(r);
    if (arr) arr.push(td);
    else byRoot.set(r, [td]);
  }

  const sections = Object.fromEntries(ALL_TRIAGE_SECTIONS.map((s) => [s, [] as TriageGroup[]])) as Record<TriageSection, TriageGroup[]>;
  const todoCounts = Object.fromEntries(ALL_TRIAGE_SECTIONS.map((s) => [s, 0])) as Record<TriageSection, number>;
  let open = 0;
  for (const td of list) {
    const tr = placed.get(td.id)!;
    todoCounts[tr.section]++;
    if (countsAsOpen(td, tr)) open++;
  }
  const shelved = todoCounts.shelved;
  const cancelled = todoCounts.cancelled;
  const finished = list.length - open - shelved - cancelled;
  for (const members of byRoot.values()) {
    members.sort((a, b) => {
      const ta = placed.get(a.id)!;
      const tb = placed.get(b.id)!;
      // a superseded failed start never leads the row of the work that finished
      const behind = (t: Triage) => (t.rule === "superseded" ? 1 : 0);
      return ORDER.get(ta.section)! - ORDER.get(tb.section)! || behind(ta) - behind(tb) || tb.last - ta.last || a.id.localeCompare(b.id);
    });
    const lead = members[0];
    const lt = placed.get(lead.id)!;
    const triageOf = new Map(members.map((m) => [m.id, placed.get(m.id)!]));
    sections[lt.section].push({ key: triageGroupKey(members), section: lt.section, lead, members, triage: triageOf, last: Math.max(...members.map((m) => placed.get(m.id)!.last)) });
  }
  for (const s of TRIAGE_SECTIONS) sections[s].sort((a, b) => b.last - a.last || a.key.localeCompare(b.key));
  return { sections, todoCounts, open, shelved, finished, cancelled };
}
