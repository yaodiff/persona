import { api, enc } from "./api";
import { repositoryRequestOptions, type RepositoryLayout, type RepositoryRequestContext, type RequestedRef, type TaskRepositoryPreparationSnapshot } from "./task-repositories";

export const WORKSPACE_NAMING_VERSION = "workspace-naming/v1";
export const RULE_FIELDS = ["base_directory", "task_directory_template", "branch_template"] as const;
export type RuleField = typeof RULE_FIELDS[number];
export type WorkspaceRules = Partial<Record<RuleField, string>>;
export type WorkspaceRulesPatch = Record<RuleField, string | null>;
export type WorkspaceNamingSettings = {
  manager?: string; machine_id?: string; workspace_naming_version?: string;
  workspace_rules?: WorkspaceRules; effective_workspace_rules?: WorkspaceRules; system_defaults?: WorkspaceRules;
};
export type WorkspaceRulesRecord = { todo_id: string; revision: number; workspace_rules: WorkspaceRules };
export type NamingDiagnostic = { code: string; field: string; repo_id?: string; severity: "error" | "warning" };
export type WorkspaceNamingPlan = {
  version: string; digest: string; base_path: string; root_path: string; task_slug: string;
  node: { home: string; persona_home?: string; path_flavor?: string };
  source: { manager: string; todo_id: string; title: string; machine_id: string; planned_at: string; timezone: string;
    rules: WorkspaceRules; rule_sources?: Record<RuleField, string>; layout: RepositoryLayout; repository_snapshot: TaskRepositoryPreparationSnapshot };
  repositories: Array<{ repo_id: string; requested_ref: RequestedRef; relative_path: string; absolute_path: string; task_branch: string }>;
};
export type WorkspaceNamingPreview = {
  status: "complete" | "incomplete" | "invalid"; sample: boolean; plan?: WorkspaceNamingPlan; diagnostics: NamingDiagnostic[];
  system_defaults?: WorkspaceRules;
  branch_sources?: Array<{ repo_id: string; kind: string; branch?: string }>;
};
export function rulesPatch(rules: WorkspaceRules): WorkspaceRulesPatch {
  return Object.fromEntries(RULE_FIELDS.map((key) => [key, rules[key] ?? null])) as WorkspaceRulesPatch;
}
export function sameRules(a: WorkspaceRules, b: WorkspaceRules): boolean { return RULE_FIELDS.every((key) => a[key] === b[key]); }
export async function loadWorkspaceRules(context: RepositoryRequestContext, todoId: string) {
  const record = await api<WorkspaceRulesRecord>(`/todos/${enc(todoId)}/workspace-rules`, repositoryRequestOptions(context));
  if (record.todo_id !== todoId || !Number.isSafeInteger(record.revision) || record.revision < 0) throw new Error("Unconfirmed workspace rules");
  return record;
}
export async function saveWorkspaceRules(context: RepositoryRequestContext, todoId: string | undefined, revision: number, rules: WorkspaceRules) {
  const path = todoId ? `/todos/${enc(todoId)}/workspace-rules` : "/assistant";
  const result = await api<WorkspaceRulesRecord & WorkspaceNamingSettings>(path, {
    ...repositoryRequestOptions(context), method: "PUT", json: { workspace_rules: rulesPatch(rules), ...(todoId ? { expected_revision: revision } : {}) },
  });
  if (!sameRules(result.workspace_rules ?? {}, rules) || (todoId ? result.todo_id !== todoId || result.revision !== revision + 1 : result.manager !== context.manager || result.workspace_naming_version !== WORKSPACE_NAMING_VERSION)) throw new Error("Unconfirmed workspace rules");
  return result;
}
export async function previewWorkspaceNaming(context: RepositoryRequestContext, todoId: string | undefined, input: {
  machine_id: string; layout: RepositoryLayout; selected_repo_ids: string[]; workspace_rules: WorkspaceRulesPatch;
}) {
  const result = await api<WorkspaceNamingPreview>(todoId ? `/todos/${enc(todoId)}/workspace-naming/preview` : "/assistant/workspace-naming/preview", {
    ...repositoryRequestOptions(context), method: "POST", json: input,
  });
  if (!["complete", "incomplete", "invalid"].includes(result.status) || !Array.isArray(result.diagnostics)
    || result.plan && (result.plan.version !== WORKSPACE_NAMING_VERSION || result.plan.source.machine_id !== input.machine_id || result.plan.source.manager !== context.manager || todoId && result.plan.source.todo_id !== todoId)
    || result.status === "complete" && !result.plan) throw new Error("Unconfirmed workspace preview");
  return result;
}
