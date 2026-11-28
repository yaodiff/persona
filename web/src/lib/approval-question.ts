// Clarifying an assistant-owned approval is deliberately not resolving it. This
// request has no Resolution or field-value envelope, and its stable message ID
// makes a retry of the same draft one owner message in the conversation.
import { enc } from "./api";
import { apiOp } from "./ops";
import type { Interaction } from "./types";

export const APPROVAL_QUESTION_MAX = 1000;

export function approvalQuestionReady(text: string): boolean {
  const n = Array.from(text.trim()).length;
  return n > 0 && n <= APPROVAL_QUESTION_MAX;
}

export async function askApprovalQuestion(ix: Interaction, text: string, clientMsgID: string): Promise<void> {
  await apiOp(`/interactions/${enc(ix.id)}/clarify`, {
    method: "POST",
    opId: clientMsgID,
    json: { text, client_msg_id: clientMsgID },
    headers: ix.created_at ? { "X-Persona-Interaction-Created-At": ix.created_at } : undefined,
  });
}
