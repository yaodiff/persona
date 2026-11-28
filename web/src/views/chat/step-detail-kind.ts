// Which opened steps draw their own detail (StepDetail.tsx) instead of their input as text. Kept apart from that
// component so the row (ActionRow) can ask on every render while the detail itself loads only when one opens.
import { baseToolName, toolQuestions } from "../../lib/tool-detail";
import { canonicalMcpToolName } from "../../lib/turns";
import type { Item } from "../../lib/types";

const STRUCTURED = new Set(["ExitPlanMode", "AskUserQuestion", "ask_user"]);

export const stepToolName = (name?: string): string => baseToolName(canonicalMcpToolName(name) ?? "");

/** The step draws its own detail (StepDetail) instead of its input as text: a plan, or questions, to show. */
export function hasStepDetail(use: Item): boolean {
  const name = stepToolName(use.tool_name);
  if (!STRUCTURED.has(name)) return false;
  const input = (use.tool_input ?? {}) as Record<string, unknown>;
  return name === "ExitPlanMode" ? typeof input.plan === "string" && input.plan.trim() !== "" : toolQuestions(input).length > 0;
}

/** ask_user's result is what the detail ticks: it is not shown again under it, unless it failed. */
export function showsResult(name: string, result: Item | undefined): boolean {
  return stepToolName(name) !== "ask_user" || Boolean(result?.is_error);
}
