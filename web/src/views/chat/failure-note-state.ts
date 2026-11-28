import { attentionReason, failureReason, sessionCan, stateOf } from "../../lib/format";
import type { Session } from "../../lib/types";

export function showsFailureNote(session: Session, { retried }: { retried?: string } = {}): boolean {
  const reason = failureReason(session);
  if (!reason || reason === retried) return false;
  if (attentionReason(session) || stateOf(session) === "needs_you") return false;
  return sessionCan(session, "send");
}
