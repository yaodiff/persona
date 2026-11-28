// A to-do row's menu on the To-dos page (#4429): right click, the menu key or Shift+F10 on a row offers what the
// to-do's page does (complete or reopen, priority, due date, reminder, hand over, delete) without opening it first.
// Pure: the page hands in what each choice does, so the rows and the moments they pick are tested without a DOM.
import { formatDate, t } from "../i18n";
import { addDays, dayKey, startOfDay, startOfWeek, weekStartDay } from "../lib/todo-calendar";
import { todoDueDay, type Todo, type TodoPatch } from "../lib/todos";
import type { MenuItem } from "../ui/menu-model";

/** What the menu's choices do; the page owns the requests, the dialogs and their toasts. */
export interface TodoRowActions {
  /** Completes a to-do, opens the report form for a concern, or reopens a done item. */
  toggleDone: () => void;
  /** Saves a priority, due date or reminder, as the to-do's page does. */
  save: (patch: TodoPatch) => void;
  /** Opens the dialog that picks any due day. */
  pickDue: () => void;
  /** Opens the hand-over dialog. */
  handOver: () => void;
  /** Opens the delete confirmation. */
  remove: () => void;
}

/** The hour a reminder for tomorrow goes off, local time. */
export const MORNING_HOUR = 9;

/** The days Due date offers, as local midnights: today, tomorrow, and the first day of next week (the week the
 *  calendar draws, from the browser's locale). */
export function dueChoices(now: number, first = weekStartDay()): { today: number; tomorrow: number; nextWeek: number } {
  const today = startOfDay(now);
  return { today, tomorrow: addDays(today, 1), nextWeek: addDays(startOfWeek(now, first), 7) };
}

/** The moments Reminder offers: an hour from now (to the minute) and tomorrow at MORNING_HOUR. */
export function remindChoices(now: number): { inHour: number; tomorrow: number } {
  const morning = new Date(addDays(startOfDay(now), 1));
  morning.setHours(MORNING_HOUR, 0, 0, 0);
  return { inHour: Math.floor((now + 3_600_000) / 60_000) * 60_000, tomorrow: morning.getTime() };
}

/** The local day a to-do is due on (midnight), whether due at a moment or on a day; undefined without one. */
export function dueOn(todo: Todo): number | undefined {
  const at = todo.due_at ? Date.parse(todo.due_at) : NaN;
  return Number.isNaN(at) ? todoDueDay(todo) : startOfDay(at);
}

/** Moves a to-do's due date to `day` (a local midnight): a to-do due at a time keeps that time, as the date field on
 *  its page does; one due on a day, or not due yet, becomes due on that day. */
export function duePatch(todo: Todo, day: number): TodoPatch {
  const at = todo.due_at ? Date.parse(todo.due_at) : NaN;
  if (Number.isNaN(at)) return { due_date: dayKey(day) };
  const time = new Date(at);
  const d = new Date(day);
  return { due_at: new Date(d.getFullYear(), d.getMonth(), d.getDate(), time.getHours(), time.getMinutes()).toISOString() };
}

const weekday = (ms: number) => formatDate(ms, { weekday: "short" });
const clockTime = (ms: number) => formatDate(ms, { hour: "2-digit", minute: "2-digit" });

/** The rows of a to-do's menu. `open`: the row's checkbox shows it open (`checksAsOpen`), so the first row does what a
 *  click on the checkbox does. A done to-do has no Hand to agent: the hub refuses it (#1856), as the page hides it. */
export function todoRowMenu(todo: Todo, open: boolean, now: number, act: TodoRowActions, first?: number): MenuItem[] {
  const due = dueChoices(now, first);
  const remind = remindChoices(now);
  const dueDay = dueOn(todo);
  const priority = todo.priority === "normal" ? "" : (todo.priority ?? "");
  const dueRow = (key: string, label: string, day: number): MenuItem => ({
    key,
    label,
    hint: weekday(day),
    checked: dueDay === day,
    onSelect: () => act.save(duePatch(todo, day)),
  });
  const priorityRow = (key: string, label: string, value: string): MenuItem => ({
    key,
    label,
    radio: true,
    checked: priority === value,
    onSelect: () => act.save({ priority: value }),
  });
  return [
    open
      ? { key: "complete", label: todo.kind === "concern" ? t("todos.report_concern_action") : t("todos.complete_todo"), icon: "check", onSelect: act.toggleDone }
      : { key: "reopen", label: t("todos.reopen_todo"), icon: "undo", onSelect: act.toggleDone },
    {
      key: "priority",
      label: t("todos.priority_label"),
      icon: "flag",
      divider: true,
      onSelect: () => {},
      children: [
        priorityRow("priority-high", t("todos.priority_option_high"), "high"),
        priorityRow("priority-normal", t("todos.priority_normal"), ""),
        priorityRow("priority-low", t("todos.priority_low"), "low"),
      ],
    },
    {
      key: "due",
      label: t("todos-menu.due"),
      icon: "calendar",
      onSelect: () => {},
      children: [
        dueRow("due-today", t("todos-menu.due_today"), due.today),
        dueRow("due-tomorrow", t("todos-menu.due_tomorrow"), due.tomorrow),
        dueRow("due-next-week", t("todos-menu.due_next_week"), due.nextWeek),
        { key: "due-pick", label: t("todos-menu.due_pick"), onSelect: act.pickDue },
        { key: "due-none", label: t("todos-menu.due_none"), divider: true, disabled: dueDay === undefined, onSelect: () => act.save({ due_at: null }) },
      ],
    },
    {
      key: "remind",
      label: t("todos.remind_label"),
      icon: "bell",
      onSelect: () => {},
      children: [
        { key: "remind-hour", label: t("todos-menu.remind_hour"), hint: clockTime(remind.inHour), onSelect: () => act.save({ remind_at: new Date(remind.inHour).toISOString() }) },
        { key: "remind-tomorrow", label: t("todos-menu.remind_tomorrow"), hint: clockTime(remind.tomorrow), onSelect: () => act.save({ remind_at: new Date(remind.tomorrow).toISOString() }) },
        { key: "remind-none", label: t("todos-menu.remind_none"), divider: true, disabled: !todo.remind_at, onSelect: () => act.save({ remind_at: null }) },
      ],
    },
    ...(todo.done_at ? [] : [{ key: "hand-over", label: t("todos-menu.hand_over"), icon: "hand", divider: true, onSelect: act.handOver } satisfies MenuItem]),
    { key: "delete", label: t("todos.delete_menu"), icon: "trash", danger: true, divider: true, onSelect: act.remove },
  ];
}
