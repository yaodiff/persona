// An exact owner-action reply is an authenticated app send, not a card
// resolution and not text for the model. Only the server-owned proposal may
// supply its target; quoteReply and ordinary composer text never do.
import { t } from "../i18n";
import { errorText, sessionPath } from "./api";
import { uuid } from "./format";
import { apiOp } from "./ops";
import type { Interaction } from "./types";

export interface OwnerApprovalReply {
  ok?: boolean;
  approval_reply?: boolean;
  interaction_id?: string;
}

export interface OwnerApprovalAttempt {
  target: string;
  id: string;
  createdAt?: string;
}

const ownerApprovalAttempts = new Map<string, OwnerApprovalAttempt>();
const ownerApprovalAttemptKey = (ix: Interaction) => `${ix.id}\u0000${ix.created_at ?? ""}`;

/** One operation ID survives an uncertain request, remount and user retry; a different card starts a new operation. */
export function ownerApprovalAttempt(ix: Interaction, previous: OwnerApprovalAttempt | null): OwnerApprovalAttempt {
  if (previous?.target === ix.id && previous.createdAt === ix.created_at) return previous;
  const key = ownerApprovalAttemptKey(ix);
  const existing = ownerApprovalAttempts.get(key);
  if (existing) return existing;
  const attempt = { target: ix.id, id: uuid(), createdAt: ix.created_at };
  ownerApprovalAttempts.set(key, attempt);
  return attempt;
}

/** Returns an error to show, or the empty string after the exact reply won. */
export async function replyOwnerApproval(ix: Interaction, phrase: string, clientMsgID = uuid()): Promise<string> {
  if (!ix.owner_proposal || !ix.approval_batch_id || !phrase || !ix.owner_approval_phrases?.includes(phrase) || !clientMsgID) {
    return t("chat-interactions.owner_reply_unavailable");
  }
  try {
    const reply = await apiOp<OwnerApprovalReply>(`${sessionPath(ix.machine_id, ix.session_id)}/messages`, {
      method: "POST",
      opId: clientMsgID,
      json: {
        text: phrase,
        client_msg_id: clientMsgID,
        approval_reply_to: ix.id,
        ...(ix.created_at ? { approval_reply_created_at: ix.created_at } : {}),
      },
    });
    if (reply?.approval_reply !== true || reply.interaction_id !== ix.id) {
      return t("chat-interactions.owner_reply_unavailable");
    }
    ownerApprovalAttempts.delete(ownerApprovalAttemptKey(ix));
    return "";
  } catch (e) {
    return t("chat-interactions.send_failed", { error: errorText(e) });
  }
}
