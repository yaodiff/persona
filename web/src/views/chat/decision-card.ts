import { createContext, type ReactNode } from "react";
import type { Interaction } from "../../lib/types";

export type QuestionDraft = { version: string; page: number; picks: Record<number, string[]>; custom: Record<number, string> };

/** The one mounted interaction in the assistant's decision pager. Drafts live only in this chat's dock. */
export const DecisionCardContext = createContext<{
  interaction: Interaction;
  task: boolean;
  drafts: Map<string, QuestionDraft>;
  navigation?: ReactNode;
  skipHint?: string;
} | null>(null);
