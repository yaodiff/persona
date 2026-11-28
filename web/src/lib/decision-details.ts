import type { Interaction, Item } from "./types";

export function blockedDecisionSkip(ix: Interaction): "terminal" | "terminal_chat" | "permission" | "plan" | undefined {
  if (ix.kind === "permission" || ix.kind === "plan") return ix.kind;
  if (ix.kind === "screen") {
    if (ix.dialog?.command) return "permission";
    return ix.dialog?.options?.some((option) => option.kind === "chat") ? "terminal_chat" : "terminal";
  }
  return undefined;
}

const isPersonaQuestion = (name: string | undefined): boolean => Boolean(name === "ask_user" || name?.endsWith("__ask_user"));
const isQuestionTool = (name: string | undefined): boolean => isPersonaQuestion(name) || name === "AskUserQuestion" || name === "request_user_input";

function firstQuestion(input: unknown): unknown {
  if (typeof input !== "object" || input === null || !("questions" in input) || !Array.isArray(input.questions)) return undefined;
  const first: unknown = input.questions[0];
  return typeof first === "object" && first !== null && "question" in first ? first.question : undefined;
}

/** The message just before the asking tool in the same turn. A task's terminal card uses its latest question call. */
export function decisionSource(ix: Interaction, items: readonly Item[], task: boolean): string | undefined {
  if (!task && ix.kind !== "question") return undefined;
  const question = ix.questions?.[0]?.question;
  let call = items.length - 1;
  for (; call >= Math.max(0, items.length - 400); call--) {
    const item = items[call];
    if (item.kind !== "tool_use") continue;
    if (ix.tool_use_id) {
      if (item.tool_use_id === ix.tool_use_id) break;
    } else if (task) {
      if (isQuestionTool(item.tool_name)) break;
    } else if (question && isPersonaQuestion(item.tool_name) && firstQuestion(item.tool_input) === question) break;
  }
  if (call < Math.max(0, items.length - 400)) return undefined;
  for (let i = call - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind === "text" && item.role === "user" && !item.tool_use_id) return item.id;
    if (item.kind === "text" && item.role === "assistant" && item.text?.trim()) return item.id;
  }
  return undefined;
}
