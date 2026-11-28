import { decisionDismissed, reviewState } from "./supervision";
import { decisionLaterKey, type NeedsYouItem } from "./assistant-entries";
import { heldDelivery, openAsk, taskReviewDecisionAsk } from "./todos";
import { hubNeedsYou } from "./todo-triage";
import type { Interaction } from "./types";

/** A supervisor (`ask_escalate`) or the assistant (`agents.delegate`) explicitly passed this card to the owner with
 *  context (`server/internal/assistant/ask_route.go` `ringAsks`). */
function hasEscalationContext(ix: Interaction): boolean {
  return Boolean(ix.delegate && Object.values(ix.delegate).some((v) => typeof v === "string" && v.trim()));
}

/** Current cards only its user can answer, or explicitly escalated to them (`ask_escalate`, non-delegating with
 *  `delegate` context). Unescalated Agent plan/permission approvals and terminal dialogs, and cards still held by a
 *  supervisor (`delegating`), stay out. */
export function directUserInteraction(ix: Interaction): boolean {
  return !ix.delegating && (
    ix.kind === "question" ||
    ix.kind === "elicitation" ||
    (ix.kind === "permission" && ix.owner_proposal === true) ||
    hasEscalationContext(ix)
  );
}

/** Pending cards shown inside a task row whose hub view places it in Needs you. Cards still held by a supervisor
 *  (`delegating`) never show; non-delegating terminal dialogs, permissions and plans stay visible when the hub says
 *  an agent of this task asks (`agent_asks`, or names the card in `view.asks`) so the owner can respond in place. */
function hubTaskInteraction(todo: Extract<NeedsYouItem, { kind: "task" }>["todo"], ix: Interaction): boolean {
  if (ix.delegating) return false;
  if (directUserInteraction(ix)) return true;
  const v = todo.view;
  return v?.section === "needs_you" && (v.reason === "agent_asks" || Boolean(v.asks?.some((a) => a.interaction === ix.id)));
}

/** A current owner decision or an explicit acceptance, not a generic retry, acknowledgement or supervisor review. */
function ownerDecision(item: Extract<NeedsYouItem, { kind: "task" }>, deferred: Record<string, unknown>): boolean {
  const todo = item.todo;
  if (todo.done_at || item.status === "failed") return false;
  // Delivery acceptance is its own explicit owner gate, not the dismissed review round that preceded it.
  if (heldDelivery(todo)?.unblock === "user") return true;
  if (item.status === "not_started" || decisionDismissed(todo) || deferred[decisionLaterKey(todo)]) return false;
  const review = reviewState(todo);
  const ask = openAsk(todo);
  if (review === "user_check") {
    return Boolean(ask && (ask.questions?.length || ask.outcome === "decide" || ask.outcome === "cannot"));
  }
  if (ask && (ask.questions?.length || ask.outcome === "decide" || !ask.outcome || ask.why_owner)) return true;
  return review === "needs_user";
}

/** The one projection behind both the Needs-you number and the rows that number opens. */
export function directUserWaiting(items: NeedsYouItem[], deferred: Record<string, unknown> = {}): NeedsYouItem[] {
  const out: NeedsYouItem[] = [];
  for (const item of items) {
    // A plan waiting for Start is the owner's decision (one is carded only when it needs the owner); a message draft
    // still needs its owner's explicit Send authorization.
    if (item.kind === "plan") { out.push(item); continue; }
    if (item.kind === "message") { out.push(item); continue; }
    // a to-do the hub sent a view of is the owner's exactly when the hub counts it in needs_you (assistant-entries
    // already left out a step its parent carries): the tray, the To-dos badge and the hub say one number
    const hub = item.kind === "task" ? hubNeedsYou(item.todo, () => false) : undefined;
    if (item.kind === "task" && hub !== undefined) {
      // assistantEntries already selects the explicit review asks that belong in this tray.
      const quietOwnerAsk = item.todo.quiet && item.todo.view?.section === "needs_you" &&
        (reviewState(item.todo) === "user_check" || taskReviewDecisionAsk(item.todo) && !deferred[decisionLaterKey(item.todo)]);
      if (hub || quietOwnerAsk) {
        out.push({ ...item, asks: item.asks.filter((ix) => hubTaskInteraction(item.todo, ix)) });
        continue;
      }
      // A quiet main-chat task is left out of the global To-dos badge (`hubNeedsYou` returns false), and a cached
      // `todo.view` may briefly lag a live `ask_escalate` push: still surface cards explicitly escalated to the owner.
      // Unescalated Agent permissions, plans and terminal dialogs on quiet tasks stay with the supervisor (D19).
      const asks = item.asks.filter((ix) => !ix.delegating && hasEscalationContext(ix));
      if (asks.length) out.push({ ...item, asks });
      continue;
    }
    if (item.kind === "task" && (item.status === "failed" || (item.status === "not_started" && heldDelivery(item.todo)?.unblock !== "user"))) continue;
    const asks = item.asks.filter(directUserInteraction);
    if (asks.length) out.push({ ...item, asks });
    else if (item.kind === "task" && ownerDecision(item, deferred)) out.push({ ...item, asks: [] });
  }
  return out;
}

/** The same row-count rule in the main chat and its sidebar rows. Other Agent and To-dos counts do not use this. */
export function directWaitingCount(items: NeedsYouItem[], ownAsks: Interaction[]): number {
  return ownAsks.filter(directUserInteraction).length + items.length;
}

let requested = "";

/** A sidebar click survives navigation: the main chat consumes this when its existing action bar mounts. */
export function requestDirectWaiting(conversation: string): void {
  requested = conversation;
  window.dispatchEvent(new CustomEvent("persona:task-pending", { detail: conversation }));
}

export function takeDirectWaitingRequest(conversation: string): boolean {
  if (requested !== conversation) return false;
  requested = "";
  return true;
}
