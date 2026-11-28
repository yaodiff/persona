/**
 * Pure TypeScript Todo Stage projection and Todo Decisions log logic,
 * aligned with iOS (`TodoModels.swift`, `TodoStageBar.swift`, `TodoDecisions.swift`)
 * and Go Hub (`server/internal/assistant/todo_decisions.go`).
 */

export type TodoStage = 'plan' | 'develop' | 'review' | 'merge' | 'deploy' | 'verify';

export const TODO_STAGES: readonly TodoStage[] = ['plan', 'develop', 'review', 'merge', 'deploy', 'verify'];

export interface TodoStageStep {
  readonly id: string;
  readonly stage: TodoStage | 'done';
  readonly stepNumber: number;
  readonly state: 'passed' | 'current' | 'ahead';
}

export interface TodoStageProjection {
  readonly stage: TodoStage;
  readonly done: boolean;
  readonly steps: TodoStageStep[];
  readonly currentIndex: number;
  readonly currentId: string;
  readonly currentNumber: number;
  readonly totalSteps: number;
}

export function resolveTodoStage(stage: string | undefined, stageKind?: string): TodoStage | null {
  if (stageKind === 'general') return null;
  if (!stage) return null;
  const trimmed = stage.trim();
  for (let i = 0; i < TODO_STAGES.length; i++) {
    if (TODO_STAGES[i] === trimmed) return TODO_STAGES[i];
  }
  return null;
}

export function todoStageNumber(stage: TodoStage): number {
  const idx = TODO_STAGES.indexOf(stage);
  return idx >= 0 ? idx + 1 : 1;
}

export function buildTodoStageProjection(stage: TodoStage, done: boolean): TodoStageProjection {
  const stageNum = todoStageNumber(stage);
  const taken = done ? TODO_STAGES.slice(0, stageNum) : TODO_STAGES;
  const totalSteps = done ? taken.length + 1 : TODO_STAGES.length;
  const currentIndex = done ? totalSteps : stageNum - 1;
  const steps: TodoStageStep[] = [];
  for (let i = 0; i < taken.length; i++) {
    steps.push({
      id: taken[i],
      stage: taken[i],
      stepNumber: i + 1,
      state: i < currentIndex ? 'passed' : i === currentIndex ? 'current' : 'ahead'
    });
  }
  if (done) {
    steps.push({
      id: 'done',
      stage: 'done',
      stepNumber: totalSteps,
      state: 'passed'
    });
  }
  return {
    stage,
    done,
    steps,
    currentIndex,
    currentId: done ? 'done' : stage,
    currentNumber: done ? totalSteps : stageNum,
    totalSteps
  };
}

export type TodoDecisionsDisplay = 'first' | 'all' | 'folded';

export const TODO_DECISIONS_FIRST_COUNT: number = 5;

export interface TodoDecision {
  readonly id: string;
  readonly todoId: string;
  readonly question: string;
  readonly choice: string;
  readonly reason: string;
  readonly source: string;
  readonly sourceMachine: string;
  readonly sourceSession: string;
  readonly sourceMessage: string;
  readonly supersedesId: string;
  readonly supersededBy: string;
  readonly createdAt: string;
}

function asObj(raw: Object | null | undefined): Record<string, Object> | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  return raw as Record<string, Object>;
}

function asStr(v: Object | undefined): string {
  return typeof v === 'string' ? v : '';
}

export function parseTodoDecision(raw: Object | null | undefined): TodoDecision | null {
  const obj = asObj(raw);
  if (!obj) return null;
  const id = asStr(obj['id']).trim();
  if (!id) return null;
  return {
    id,
    todoId: asStr(obj['todo_id']),
    question: asStr(obj['question']),
    choice: asStr(obj['choice']),
    reason: asStr(obj['reason']),
    source: asStr(obj['source']),
    sourceMachine: asStr(obj['source_machine']),
    sourceSession: asStr(obj['source_session']),
    sourceMessage: asStr(obj['source_message']),
    supersedesId: asStr(obj['supersedes_id']),
    supersededBy: asStr(obj['superseded_by']),
    createdAt: asStr(obj['created_at'])
  };
}

export function parseTodoDecisionsResponse(raw: Object | null | undefined): TodoDecision[] {
  const obj = asObj(raw);
  const rawList = obj?.['decisions'];
  if (!Array.isArray(rawList)) return [];
  const out: TodoDecision[] = [];
  for (const item of rawList as Object[]) {
    const parsed = parseTodoDecision(item);
    if (parsed) out.push(parsed);
  }
  return out;
}

export function isDecisionCurrent(decision: TodoDecision): boolean {
  return !decision.supersededBy;
}

export function currentTodoDecisions(all: readonly TodoDecision[]): TodoDecision[] {
  return all.filter((d: TodoDecision) => isDecisionCurrent(d));
}

export function listedTodoDecisions(all: readonly TodoDecision[], showAll: boolean): TodoDecision[] {
  return showAll ? all.slice() : currentTodoDecisions(all);
}

export function shownTodoDecisions(listed: readonly TodoDecision[], display: TodoDecisionsDisplay): TodoDecision[] {
  if (display === 'folded') return [];
  if (display === 'first') return listed.slice(0, TODO_DECISIONS_FIRST_COUNT);
  return listed.slice();
}

export function supersedeCandidatesFor(decision: TodoDecision, all: readonly TodoDecision[]): TodoDecision[] {
  const idx = all.findIndex((item: TodoDecision) => item.id === decision.id);
  const end = idx >= 0 ? idx : 0;
  return all.slice(0, end).filter((item: TodoDecision) => isDecisionCurrent(item) && item.id !== decision.supersedesId);
}

export function todoDecisionsListPath(todoId: string, manager?: string): string {
  const base = '/api/v1/todos/' + encodeURIComponent(todoId) + '/decisions?all=1';
  return manager ? base + '&manager=' + encodeURIComponent(manager) : base;
}

export function todoDecisionsPostPath(todoId: string, manager?: string): string {
  const base = '/api/v1/todos/' + encodeURIComponent(todoId) + '/decisions';
  return manager ? base + '?manager=' + encodeURIComponent(manager) : base;
}

export function todoDecisionPatchPath(todoId: string, decisionId: string, manager?: string): string {
  const base = '/api/v1/todos/' + encodeURIComponent(todoId) + '/decisions/' + encodeURIComponent(decisionId);
  return manager ? base + '?manager=' + encodeURIComponent(manager) : base;
}

export function buildAddTodoDecisionPayload(
  question: string,
  choice: string,
  reason: string = '',
  supersedesId: string = ''
): Record<string, Object> | null {
  const q = question.trim();
  const c = choice.trim();
  if (!q || !c) return null;
  const payload: Record<string, Object> = {};
  payload['question'] = q;
  payload['choice'] = c;
  const r = reason.trim();
  if (r) payload['reason'] = r;
  const sup = supersedesId.trim();
  if (sup) payload['supersedes_id'] = sup;
  return payload;
}

export function formatDecisionTimestamp(createdAt: string, nowMs: number = Date.now()): string {
  if (!createdAt) return '';
  const ms = Date.parse(createdAt);
  if (!Number.isFinite(ms)) return '';
  const d = new Date(ms);
  const n = new Date(nowMs);
  const sameDay = d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate();
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  if (sameDay) return hh + ':' + mm;
  return String(d.getMonth() + 1) + '/' + String(d.getDate()) + ' ' + hh + ':' + mm;
}
