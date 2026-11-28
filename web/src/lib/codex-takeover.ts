export interface CodexTakeoverPlan {
  running: boolean;
  app_owned?: boolean;
  can_resume: boolean;
  reason?: string;
  token: string;
  cwd: string;
  transcript_path: string;
  continuation_prompt: string;
}

/** Running turns and app-owned sessions require explicit confirmation before takeover. */
export function takeoverBody(plan: Pick<CodexTakeoverPlan, "token" | "running" | "app_owned">, confirmed: boolean) {
  const needsConfirm = Boolean(plan.running || plan.app_owned);
  if (needsConfirm && !confirmed) return null;
  return { token: plan.token, confirm_stop: needsConfirm && confirmed };
}

export function continuationLaunch(plan: CodexTakeoverPlan, machineId: string, todoId?: string) {
  return {
    machineId, cwd: plan.cwd, launcher: "codex", agentType: "codex", inlinePrompt: true,
    text: plan.continuation_prompt, attachments: [] as never[], todoId,
  };
}
