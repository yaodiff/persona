import { Wire } from './Wire';
import { TODO_LIST_SECTIONS, parseTodoListSection, type TodoListSection } from './TodoViewWire';

// GET /api/v1/todos, as specified by schema/wire/todo-view.schema.json. The
// Hub owns the rank and counts. New reason and line keys leave the row readable.
// The row's section is read key by key as the hub's text (TodoHubRow), so a
// newer hub's section stays a string here and is drawn in no section.
export const TODO_HUB_SECTIONS: TodoListSection[] = [...TODO_LIST_SECTIONS];
export const TODO_HUB_CHOICES: string[] = ['all', 'needs_you', 'in_progress', 'todo', 'done'];

const needsYou: TodoListSection = TODO_LIST_SECTIONS[0];
const canWait: TodoListSection = TODO_LIST_SECTIONS[1];
const working: TodoListSection = TODO_LIST_SECTIONS[2];
const waiting: TodoListSection = TODO_LIST_SECTIONS[3];
const todo: TodoListSection = TODO_LIST_SECTIONS[4];
const justFinished: TodoListSection = TODO_LIST_SECTIONS[5];
const someday: TodoListSection = TODO_LIST_SECTIONS[6];
const done: TodoListSection = TODO_LIST_SECTIONS[7];

// The label of a section or of a Filter choice ('in_progress' is no section).
export function todoHubLabel(value: string): string {
  switch (value) {
    case canWait: return 'flow_todo_can_wait';
    case someday: return 'flow_todo_someday';
    case justFinished: return 'agent_group_just_finished';
    case todo: return 'flow_todo_plain';
    case 'in_progress': return 'flow_inbox_progress';
    default: return value;
  }
}

export function todoHubChoiceIncludes(choice: string, section: string): boolean {
  switch (choice) {
    case 'needs_you': return section === needsYou || section === canWait;
    case 'in_progress': return section === working || section === waiting || section === justFinished;
    case 'todo': return section === todo || section === someday;
    case 'done': return section === done;
    default: return true;
  }
}

function count(data: Record<string, Object>, key: string): number {
  const value = Wire.number(data, key, -1);
  return value >= 0 && Number.isSafeInteger(value) ? value : -1;
}

export class TodoHubCounts {
  open: number = 0;
  needsYou: number = 0;
  inProgress: number = 0;
  sections: Map<string, number> = new Map();

  static read(result: Record<string, Object>): TodoHubCounts | undefined {
    const data = Wire.object(result, 'counts');
    const sections = Wire.object(data, 'sections');
    const counts = new TodoHubCounts();
    counts.open = count(data, 'open');
    counts.needsYou = count(data, 'needs_you');
    counts.inProgress = count(data, 'in_progress');
    if (Math.min(counts.open, counts.needsYou, counts.inProgress) < 0) return undefined;
    for (const key of TODO_HUB_SECTIONS) {
      const value = count(sections, key);
      if (value < 0) return undefined;
      counts.sections.set(key, value);
    }
    return counts;
  }

  choiceCount(key: string): number {
    if (key === 'needs_you') return this.needsYou;
    if (key === 'in_progress') return this.inProgress;
    return this.sections.get(key) || 0;
  }

  choices(chosen: string): string[] {
    return TODO_HUB_CHOICES.filter((key: string) => key === 'all' || key === 'needs_you' || key === chosen || this.choiceCount(key) > 0);
  }
}

export class TodoHubRow {
  section: string = '';
  phase: string = '';
  rank: number = 0;
  // Text actually supplied by the hub. An unknown future line never becomes a label.
  line: string = '';

  static read(data: Record<string, Object>): TodoHubRow | undefined {
    const view = Wire.object(data, 'view');
    const row = new TodoHubRow();
    row.section = Wire.text(view, 'section');
    row.phase = Wire.text(view, 'phase');
    row.rank = count(view, 'rank');
    // No view, or a section this build does not know (parsed as unknown, and counted): the local rules file the row.
    if (!row.section || typeof parseTodoListSection(row.section) !== 'string' || !row.phase || row.rank < 0) return undefined;
    const line = Wire.object(view, 'line');
    const args = Wire.object(line, 'args');
    const key = Wire.text(line, 'key');
    switch (key) {
      case 'failed_start': row.line = Wire.text(args, 'error'); break;
      case 'agent_asks':
      case 'working':
      case 'awaits_reply':
        row.line = [Wire.text(args, 'run'), Wire.text(args, 'text')].filter((value: string) => !!value).join(' · '); break;
      case 'decision':
      case 'decision_default':
      case 'persona_working': row.line = Wire.text(args, 'text'); break;
      case 'parked':
      case 'canceled': row.line = Wire.text(args, 'reason'); break;
    }
    return row;
  }
}
