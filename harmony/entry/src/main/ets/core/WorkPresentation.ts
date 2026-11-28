/**
 * Structured tool-turn presentation matching iOS `ToolViews.swift`, `WorkNow.swift`,
 * and `TurnChangesCard.swift`. Pure TypeScript — zero ArkUI dependencies.
 */

export type ToolIconRole =
  | 'terminal'
  | 'read'
  | 'edit'
  | 'write'
  | 'search'
  | 'find'
  | 'web'
  | 'subagent'
  | 'todo'
  | 'git'
  | 'code'
  | 'plan'
  | 'question'
  | 'skill'
  | 'thought'
  | 'fold'
  | 'tool';

export interface WorkStepView {
  readonly id: string;
  readonly kind: 'tool' | 'thought' | 'narration' | 'fold';
  readonly iconRole: ToolIconRole;
  readonly verb: string;
  readonly subject: string;
  readonly inputPreview: string;
  readonly outputPreview: string;
  readonly running: boolean;
  readonly failed: boolean;
  readonly filePath: string;
  readonly additions: number;
  readonly deletions: number;
}

export interface WorkNowLine {
  readonly id: string;
  readonly text: string;
  readonly more: number;
}

export interface TurnChangedFile {
  readonly path: string;
  readonly shown: string;
  readonly additions: number;
  readonly deletions: number;
  readonly exact: boolean;
  readonly created: boolean;
}

export interface TurnChangeSet {
  readonly files: readonly TurnChangedFile[];
  readonly totalAdditions: number;
  readonly totalDeletions: number;
  readonly exact: boolean;
}

const QUIET_WORK_NOW_TOOLS = new Set([
  'TodoWrite', 'TaskCreate', 'TaskUpdate', 'TaskList', 'TaskGet', 'ToolSearch'
]);

const KNOWN_VERBS: Record<string, string> = {
  Bash: 'Run',
  PowerShell: 'Run',
  Read: 'Read',
  NotebookRead: 'Read',
  Edit: 'Edit',
  MultiEdit: 'Edit',
  NotebookEdit: 'Edit',
  Write: 'Write',
  Grep: 'Search',
  Glob: 'Find',
  LS: 'Find',
  WebFetch: 'Open Web Page',
  WebSearch: 'Web Search',
  Task: 'Subtask',
  Agent: 'Subtask',
  TodoWrite: 'Update To-dos',
  TaskCreate: 'Add To-Do',
  TaskUpdate: 'Update To-Do',
  TaskList: 'List To-Dos',
  TaskGet: 'Read To-Do',
  TaskStop: 'Stop Task',
  TaskOutput: 'Read Output',
  BashOutput: 'Read Output',
  SendMessage: 'Message Agent',
  ListAgents: 'List Agents',
  LSP: 'Look Up Code',
  EnterWorktree: 'Enter Worktree',
  ExitWorktree: 'Leave Worktree',
  AskUserQuestion: 'Question',
  ask_user: 'Question',
  ExitPlanMode: 'Submit Plan',
  EnterPlanMode: 'Enter Plan Mode',
  Skill: 'Use Skill',
  Workflow: 'Run Workflow',
  computer_state: 'View Computer',
  computer_run: 'Control Computer',
  browser_run: 'Use Browser',
  browser_look: 'View Browser'
};

export function toolBaseName(raw: string): string {
  const trimmed = (raw || '').trim();
  if (!trimmed) return '';
  if (trimmed.startsWith('mcp__')) {
    const parts = trimmed.split('__').filter((p) => p.length > 0);
    return parts.length >= 3 ? parts[parts.length - 1] : trimmed;
  }
  return trimmed;
}

function humanizeToken(raw: string): string {
  const spaced = raw
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/[_-]+/g, ' ')
    .trim();
  if (!spaced) return '';
  return spaced
    .split(/\s+/)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ');
}

export function toolVerb(raw: string): string {
  const trimmed = (raw || '').trim();
  if (!trimmed) return 'Work';
  if (trimmed.startsWith('mcp__')) {
    const parts = trimmed.split('__').filter((p) => p.length > 0);
    if (parts.length >= 3) {
      const server = humanizeToken(parts[1]);
      const action = humanizeToken(parts.slice(2).join(' '));
      return server && action ? `${server} · ${action}` : action || server;
    }
  }
  const base = toolBaseName(trimmed);
  if (KNOWN_VERBS[base]) return KNOWN_VERBS[base];
  return humanizeToken(base) || 'Work';
}

export function toolIconRole(raw: string): ToolIconRole {
  const trimmed = (raw || '').trim();
  if (trimmed.startsWith('mcp__')) return 'skill';
  const base = toolBaseName(trimmed);
  if (base === 'Bash' || base === 'PowerShell' || base === 'BashOutput') return 'terminal';
  if (base === 'Read' || base === 'NotebookRead') return 'read';
  if (base === 'Edit' || base === 'MultiEdit' || base === 'NotebookEdit') return 'edit';
  if (base === 'Write') return 'write';
  if (base === 'Grep') return 'search';
  if (base === 'Glob' || base === 'LS') return 'find';
  if (base === 'WebFetch' || base === 'WebSearch' || base === 'browser_run' || base === 'browser_look') return 'web';
  if (base === 'Task' || base === 'Agent' || base === 'ListAgents' || base === 'SendMessage') return 'subagent';
  if (base === 'TodoWrite' || base.startsWith('Task')) return 'todo';
  if (base === 'EnterWorktree' || base === 'ExitWorktree') return 'git';
  if (base === 'LSP') return 'code';
  if (base === 'ExitPlanMode' || base === 'EnterPlanMode') return 'plan';
  if (base === 'AskUserQuestion' || base === 'ask_user') return 'question';
  if (base === 'Skill' || base === 'Workflow') return 'skill';
  return 'tool';
}

function asRecord(value: Object | undefined): Record<string, Object> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
  return value as Record<string, Object>;
}

function strField(rec: Record<string, Object>, key: string): string {
  const val = rec[key];
  return typeof val === 'string' ? val.trim() : '';
}

function clip(value: string, max: number = 140): string {
  const single = value.replace(/\s+/g, ' ').trim();
  if (single.length <= max) return single;
  return single.slice(0, max - 1) + '…';
}

export function compactDisplayPath(rawPath: string): string {
  const cleaned = (rawPath || '').trim();
  if (!cleaned) return '';
  const parts = cleaned.split(/[\\/]/).filter((p) => p.length > 0);
  if (parts.length <= 2) return cleaned;
  return parts.slice(parts.length - 2).join('/');
}

function countLines(text: string): number {
  if (!text) return 0;
  let count = 1;
  for (let i = 0; i < text.length; i++) {
    if (text.charCodeAt(i) === 10) count++;
  }
  return count;
}

function estimateEditCounts(base: string, input: Record<string, Object>): { add: number; del: number; exact: boolean } {
  if (base === 'Edit') {
    const oldStr = typeof input['old_string'] === 'string' ? input['old_string'] : '';
    const newStr = typeof input['new_string'] === 'string' ? input['new_string'] : '';
    if (oldStr || newStr) {
      const oldLines = oldStr ? oldStr.split('\n') : [];
      const newLines = newStr ? newStr.split('\n') : [];
      let prefix = 0;
      while (prefix < oldLines.length && prefix < newLines.length && oldLines[prefix] === newLines[prefix]) prefix++;
      let suffix = 0;
      while (
        suffix < oldLines.length - prefix &&
        suffix < newLines.length - prefix &&
        oldLines[oldLines.length - 1 - suffix] === newLines[newLines.length - 1 - suffix]
      ) {
        suffix++;
      }
      return {
        add: Math.max(0, newLines.length - prefix - suffix),
        del: Math.max(0, oldLines.length - prefix - suffix),
        exact: true
      };
    }
  } else if (base === 'MultiEdit' && Array.isArray(input['edits'])) {
    let add = 0;
    let del = 0;
    let exact = false;
    for (const editObj of input['edits'] as Object[]) {
      const sub = estimateEditCounts('Edit', asRecord(editObj));
      if (sub.exact) {
        add += sub.add;
        del += sub.del;
        exact = true;
      }
    }
    return { add: add, del: del, exact: exact };
  } else if (base === 'Write') {
    const content = typeof input['content'] === 'string' ? input['content'] : '';
    if (content) return { add: countLines(content), del: 0, exact: true };
  }
  return { add: 0, del: 0, exact: false };
}

function toolSubject(rawName: string, input: Record<string, Object>): { subject: string; filePath: string } {
  const base = toolBaseName(rawName);
  const filePath = strField(input, 'file_path') || strField(input, 'path') || strField(input, 'notebook_path');
  if (base === 'Bash' || base === 'PowerShell') {
    const desc = strField(input, 'description');
    const cmd = strField(input, 'command');
    return { subject: clip(desc || cmd, 120), filePath: '' };
  }
  if (base === 'Read' || base === 'Edit' || base === 'MultiEdit' || base === 'Write' || base === 'NotebookEdit' || base === 'NotebookRead') {
    return { subject: compactDisplayPath(filePath), filePath: filePath };
  }
  if (base === 'Grep') {
    const pat = strField(input, 'pattern');
    return { subject: clip(pat, 80), filePath: filePath };
  }
  if (base === 'Glob' || base === 'LS') {
    return { subject: clip(strField(input, 'pattern') || compactDisplayPath(filePath), 80), filePath: filePath };
  }
  if (base === 'WebFetch') return { subject: clip(strField(input, 'url'), 90), filePath: '' };
  if (base === 'WebSearch') return { subject: clip(strField(input, 'query'), 90), filePath: '' };
  if (base === 'Task' || base === 'Agent') return { subject: clip(strField(input, 'description') || strField(input, 'prompt'), 90), filePath: '' };
  const fallback = strField(input, 'description') || strField(input, 'title') || strField(input, 'query') || compactDisplayPath(filePath);
  return { subject: clip(fallback, 90), filePath: filePath };
}

function runningPhrase(rawName: string, input: Record<string, Object>): string {
  const base = toolBaseName(rawName);
  const { subject } = toolSubject(rawName, input);
  if (!subject) return toolVerb(rawName);
  if (base === 'Bash' || base === 'PowerShell') {
    return strField(input, 'description') ? subject : `Running ${subject}`;
  }
  if (base === 'Read' || base === 'NotebookRead') return `Reading ${subject}`;
  if (base === 'Edit' || base === 'MultiEdit' || base === 'NotebookEdit') return `Editing ${subject}`;
  if (base === 'Write') return `Writing ${subject}`;
  if (base === 'Grep') return `Searching for ${subject}`;
  if (base === 'Glob' || base === 'LS') return `Finding ${subject}`;
  if (base === 'WebFetch') return `Opening ${subject}`;
  if (base === 'WebSearch') return `Searching web for ${subject}`;
  return `${toolVerb(rawName)} ${subject}`;
}

export function parseRawTranscriptItems(raw: string): Record<string, Object>[] {
  if (!raw || raw === '{}') return [];
  try {
    const parsed: Object = JSON.parse(raw) as Object;
    if (!Array.isArray(parsed)) return [];
    return (parsed as Object[]).map((entry) => asRecord(entry));
  } catch (_) {
    return [];
  }
}

/**
 * Extracts structured visual steps (`WorkStepView[]`) from a work message's raw `TranscriptItem[]`.
 */
export function extractWorkStepsFromRaw(raw: string, turnRunning: boolean = false): WorkStepView[] {
  const items = parseRawTranscriptItems(raw);
  if (!items.length) return [];

  const resultMap = new Map<string, Record<string, Object>[]>();
  for (const item of items) {
    const kind = strField(item, 'kind');
    if (kind === 'tool_result') {
      const refId = strField(item, 'tool_use_id') || strField(item, 'id');
      if (refId) {
        const list = resultMap.get(refId) || [];
        list.push(item);
        resultMap.set(refId, list);
      }
    }
  }

  const steps: WorkStepView[] = [];
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    const kind = strField(item, 'kind');
    const id = strField(item, 'id') || `step-${i}`;
    if (kind === 'tool_use') {
      const rawName = strField(item, 'tool_name') || strField(item, 'tool') || strField(item, 'name');
      const input = asRecord(item['tool_input'] || item['input']);
      const useId = strField(item, 'tool_use_id') || id;
      const results = resultMap.get(useId) || [];
      const failed = results.some((r) => r['is_error'] === true);
      const running = turnRunning && results.length === 0;
      const base = toolBaseName(rawName);
      const { subject, filePath } = toolSubject(rawName, input);
      const counts = estimateEditCounts(base, input);
      const inputKeys = Object.keys(input);
      const inputPreview = base === 'Bash' || base === 'PowerShell'
        ? strField(input, 'command')
        : inputKeys.length > 0
          ? JSON.stringify(input, null, 2).slice(0, 600)
          : strField(item, 'text');
      const outputPreview = results
        .map((r) => strField(r, 'text') || strField(r, 'content'))
        .filter((s) => s.length > 0)
        .join('\n')
        .slice(0, 600);
      steps.push({
        id: id,
        kind: 'tool',
        iconRole: toolIconRole(rawName),
        verb: toolVerb(rawName),
        subject: subject,
        inputPreview: inputPreview,
        outputPreview: outputPreview,
        running: running,
        failed: failed,
        filePath: filePath,
        additions: counts.add,
        deletions: counts.del
      });
    } else if (kind === 'thinking') {
      const text = strField(item, 'text');
      if (!text) continue;
      let firstLine = text.split('\n')[0].trim();
      if (firstLine.length > 4 && firstLine.startsWith('**') && firstLine.endsWith('**')) {
        firstLine = firstLine.slice(2, -2).trim();
      }
      steps.push({
        id: id,
        kind: 'thought',
        iconRole: 'thought',
        verb: 'Thinking',
        subject: clip(firstLine, 120),
        inputPreview: '',
        outputPreview: text.slice(0, 800),
        running: false,
        failed: false,
        filePath: '',
        additions: 0,
        deletions: 0
      });
    } else if (kind === 'fold') {
      const toolCount = typeof item['tool_count'] === 'number' ? item['tool_count'] as number : 0;
      const thoughtCount = typeof item['thought_count'] === 'number' ? item['thought_count'] as number : 0;
      steps.push({
        id: id,
        kind: 'fold',
        iconRole: 'fold',
        verb: 'Collapsed steps',
        subject: String(toolCount + thoughtCount),
        inputPreview: '',
        outputPreview: '',
        running: false,
        failed: false,
        filePath: '',
        additions: 0,
        deletions: 0
      });
    }
  }
  return steps;
}

/**
 * Mirrors iOS `WorkNow.of(_:)`: computes the live running summary line for a work pill.
 */
export function extractWorkNowFromRaw(raw: string, turnRunning: boolean): WorkNowLine | undefined {
  if (!turnRunning) return undefined;
  const items = parseRawTranscriptItems(raw);
  if (!items.length) return undefined;

  const answered = new Set<string>();
  for (const item of items) {
    if (strField(item, 'kind') === 'tool_result') {
      const refId = strField(item, 'tool_use_id') || strField(item, 'id');
      if (refId) answered.add(refId);
    }
  }

  const runningCalls: Record<string, Object>[] = [];
  let latestThought: Record<string, Object> | undefined;
  let latestCall: Record<string, Object> | undefined;

  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    const kind = strField(item, 'kind');
    if (kind === 'tool_use') {
      const rawName = strField(item, 'tool_name') || strField(item, 'tool') || strField(item, 'name');
      if (QUIET_WORK_NOW_TOOLS.has(toolBaseName(rawName))) continue;
      const useId = strField(item, 'tool_use_id') || strField(item, 'id');
      if (!answered.has(useId)) runningCalls.push(item);
      if (!latestCall) latestCall = item;
    } else if (kind === 'thinking' && !latestThought && !latestCall) {
      latestThought = item;
    }
  }

  if (runningCalls.length > 0) {
    const allReads = runningCalls.length > 1 && runningCalls.every((c) => {
      const name = strField(c, 'tool_name') || strField(c, 'tool') || strField(c, 'name');
      return toolBaseName(name) === 'Read';
    });
    const newest = runningCalls[0];
    const id = strField(newest, 'id') || 'work-now';
    if (allReads) {
      return { id: id, text: `Reading ${runningCalls.length} files`, more: 0 };
    }
    const rawName = strField(newest, 'tool_name') || strField(newest, 'tool') || strField(newest, 'name');
    const input = asRecord(newest['tool_input'] || newest['input']);
    return { id: id, text: runningPhrase(rawName, input), more: Math.max(0, runningCalls.length - 1) };
  }

  if (latestCall) {
    const rawName = strField(latestCall, 'tool_name') || strField(latestCall, 'tool') || strField(latestCall, 'name');
    const input = asRecord(latestCall['tool_input'] || latestCall['input']);
    return { id: strField(latestCall, 'id') || 'work-now', text: runningPhrase(rawName, input), more: 0 };
  }

  if (latestThought) {
    let line = strField(latestThought, 'text').split('\n')[0].trim();
    if (line.length > 4 && line.startsWith('**') && line.endsWith('**')) line = line.slice(2, -2).trim();
    return { id: strField(latestThought, 'id') || 'work-thought', text: clip(line || 'Thinking', 120), more: 0 };
  }

  return undefined;
}

/**
 * Mirrors iOS `TurnChangesCard`: aggregates files edited or written during a turn.
 */
export function extractTurnChangesFromRaw(raw: string): TurnChangeSet | undefined {
  const items = parseRawTranscriptItems(raw);
  if (!items.length) return undefined;

  const byPath = new Map<string, { add: number; del: number; exact: boolean; created: boolean }>();
  for (const item of items) {
    if (strField(item, 'kind') !== 'tool_use') continue;
    const rawName = strField(item, 'tool_name') || strField(item, 'tool') || strField(item, 'name');
    const base = toolBaseName(rawName);
    if (base !== 'Edit' && base !== 'MultiEdit' && base !== 'Write' && base !== 'NotebookEdit') continue;
    const input = asRecord(item['tool_input'] || item['input']);
    const path = strField(input, 'file_path') || strField(input, 'path') || strField(input, 'notebook_path');
    if (!path) continue;
    const counts = estimateEditCounts(base, input);
    const prev = byPath.get(path);
    if (prev) {
      prev.add += counts.add;
      prev.del += counts.del;
      prev.exact = prev.exact && counts.exact;
    } else {
      byPath.set(path, { add: counts.add, del: counts.del, exact: counts.exact, created: base === 'Write' });
    }
  }

  if (byPath.size === 0) return undefined;
  const files: TurnChangedFile[] = [];
  let totalAdd = 0;
  let totalDel = 0;
  let allExact = true;
  byPath.forEach((stats, path) => {
    totalAdd += stats.add;
    totalDel += stats.del;
    if (!stats.exact) allExact = false;
    files.push({
      path: path,
      shown: compactDisplayPath(path),
      additions: stats.add,
      deletions: stats.del,
      exact: stats.exact,
      created: stats.created
    });
  });

  return {
    files: files,
    totalAdditions: totalAdd,
    totalDeletions: totalDel,
    exact: allExact && (totalAdd > 0 || totalDel > 0)
  };
}

export const TURN_CHANGES_INITIAL_VISIBLE = 3;

export interface UnifiedDiffSummary {
  readonly hunks: number;
  readonly additions: number;
  readonly deletions: number;
}

export function summarizeUnifiedDiff(patch: string): UnifiedDiffSummary {
  if (!patch) return { hunks: 0, additions: 0, deletions: 0 };
  const lines = patch.replace(/\r\n?/g, '\n').split('\n');
  let hunks = 0;
  let additions = 0;
  let deletions = 0;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.startsWith('@@')) {
      hunks++;
    } else if (line.startsWith('+') && !line.startsWith('+++')) {
      additions++;
    } else if (line.startsWith('-') && !line.startsWith('---')) {
      deletions++;
    }
  }
  return { hunks: hunks, additions: additions, deletions: deletions };
}

export function gitMarkBadgeLabel(mark: string): string {
  const trimmed = (mark || '').trim();
  if (!trimmed) return 'M';
  if (trimmed === '??' || trimmed === '?' || trimmed === 'U') return '?';
  return trimmed.slice(0, 1).toUpperCase();
}
