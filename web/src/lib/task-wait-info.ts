import type { TodoWait, TodoWaitContact } from "./todo-wait-wire";
import { timeMs } from "./times";
import type { Parked } from "./supervision";
import { sentWaitContact, waitSource, waitStart } from "./task-wait";
import { openAsk, type PRFacts, type Todo } from "./todos";

export interface TaskWait {
  kind: TodoWait["kind"] | "legacy";
  target?: string;
  since?: number;
  sinceRequest: boolean;
  contact?: TodoWaitContact;
  wakeAt?: number;
  /** Whether a missing contact is worth saying. An external event or a date never carries one (park_evidence.go refuses
   *  it), so saying none was recorded tells the owner nothing; a legacy park or an implicit PR wait could have had one. */
  contactExpected: boolean;
  wake?: string;
  reason?: string;
  until?: number;
  overdue: boolean;
  canRemind: boolean;
  sources: string[];
  handoff?: Parked["handoff"];
}

function reminderDue(kind: TaskWait["kind"], contact: TodoWaitContact | undefined, now: number): boolean {
  const sent = timeMs(contact?.sent_at);
  return (kind === "person_reply" || kind === "merge_review") && !!contact?.request && !!waitSource(contact.url) &&
    sent > 0 && sent + (kind === "merge_review" ? 72 : 24) * 3_600_000 <= now;
}

/** Lazy display projection. The caller supplies only fresh, eligible merge waits; no lifecycle classification or write. */
export function taskWait(todo: Todo, now: number, merges: readonly PRFacts[] = []): TaskWait | undefined {
  const wait = todo.wait;
  if (wait != null) {
    const until = timeMs(wait.until) || undefined;
    const source = waitSource(wait.url);
    const contactDue = reminderDue(wait.kind, wait.contact, now);
    return {
      kind: wait.kind, target: wait.waiting_on, reason: wait.reason, wake: wait.wake_condition,
      wakeAt: timeMs(wait.wake_at) || undefined, since: timeMs(wait.since) || undefined, sinceRequest: true,
      until, overdue: (until !== undefined && until <= now) || contactDue, canRemind: contactDue,
      contact: wait.contact, contactExpected: wait.kind === "person_reply" || wait.kind === "merge_review",
      sources: source ? [source] : [],
      handoff: wait.handoff_todo_id ? { todo_id: wait.handoff_todo_id, title: wait.waiting_on ?? "", check: wait.wake_condition ?? "" } : undefined,
    };
  }
  // Compatibility with hubs that do not send the projection.
  if (todo.done_at) return undefined;
  const sup = todo.supervision;
  if (sup?.state === "needs_user" || sup?.state === "pending_review" || openAsk(todo)) return undefined;
  const p = sup?.state === "parked" ? sup.parked : undefined;
  if (!p && !merges.length) return undefined;
  const e = p?.evidence;
  const kinds: string[] = ["person_reply", "merge_review", "external_event", "date"];
  const kind = e && kinds.includes(e.kind) ? e.kind : p ? "legacy" : "merge_review";
  const contactKind = kind === "person_reply" || kind === "merge_review";
  const contact = contactKind ? sentWaitContact(e, now) : undefined;
  const deadline = p?.until ? Date.parse(p.until) : NaN;
  const until = Number.isFinite(deadline) && deadline > 0 ? deadline : undefined;
  const start = waitStart(todo, now);
  if (p?.handoff) {
    return {
      kind: "todo_handoff", handoff: p.handoff, since: start.at, sinceRequest: true,
      until, overdue: until !== undefined && until <= now, contactExpected: false, canRemind: false, sources: [],
    };
  }
  const contactDue = reminderDue(kind, contact, now);
  return {
    kind,
    target: e?.target?.trim() || undefined,
    since: start.at,
    sinceRequest: start.request,
    contact,
    contactExpected: contactKind || kind === "legacy",
    wake: e?.wake?.trim() || undefined,
    reason: p?.reason,
    until,
    overdue: until !== undefined && until <= now || contactDue,
    canRemind: contactDue,
    // A task's other PRs are not evidence for this recorded wait.
    sources: [...new Set((e ? [e.url] : merges.map((pr) => pr.url)).map(waitSource).filter((url): url is string => !!url))],
  };
}
