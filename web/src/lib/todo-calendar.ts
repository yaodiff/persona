// The to-dos' calendar (#2029, docs/assistant/todos.md "Calendar"): which entries a day, a week or a month shows, and
// where. The page draws entries, not to-dos: a to-do due at a moment or on a day is one source; the phone's calendar
// events can be another later (an entry with an end), and nothing here needs to change for it. iOS / Mac:
// apple/Shared/TodoCalendar.swift, the same rules.
import { formatDate } from "../i18n";
import { isOverdue, todoDueDay, todoStatus, type SessionLookup, type Todo, type TodoRepoGateLook, type TodoStatus, type TodoTone } from "./todos";
import { todoLook, waitOf } from "./todo-wait-reason";

export type CalendarMode = "day" | "week" | "month";

/** One thing on the calendar. */
export interface CalendarEntry {
  /** "todo:<id>": unique across sources. */
  id: string;
  /** What tapping it opens. */
  todoId: string;
  title: string;
  manager?: string;
  createdAt: string;
  updatedAt: string;
  /** The original date-only wire value, including leading zeros in the year. */
  dueDate?: string;
  /** When it is, in ms: a to-do's due moment, or the local midnight of its due day when allDay. */
  start: number;
  /** When it ends: none for a to-do, which is due at a moment; an event's end. */
  end?: number;
  allDay: boolean;
  /** Its state, colored and named as in the list: a waiting one's word and tone say what it waits on (#11090, todoLook);
   *  without `tone`, its status's. */
  status: TodoStatus;
  statusLabel: string;
  tone?: TodoTone;
  done: boolean;
  overdue: boolean;
  high: boolean;
  /** A reminder still to be pushed (the list's bell). */
  reminding: boolean;
}

/** A to-do on the calendar: at its due moment, or all day on its due day; undefined without either. */
export function todoEntry(todo: Todo, lookup?: SessionLookup, now = Date.now(), repoGateLook?: TodoRepoGateLook): CalendarEntry | undefined {
  const at = todo.due_at ? Date.parse(todo.due_at) : NaN;
  const day = todoDueDay(todo);
  const allDay = Number.isNaN(at);
  const start = allDay ? day : at;
  if (start === undefined) return undefined;
  const status = todoStatus(todo, lookup);
  const look = todoLook(todo, status, waitOf(todo, status, lookup), status, now, repoGateLook);
  const done = Boolean(todo.done_at);
  return {
    id: `todo:${todo.id}`,
    todoId: todo.id,
    title: todo.title,
    manager: todo.manager,
    createdAt: todo.created_at,
    updatedAt: todo.updated_at || todo.created_at,
    dueDate: allDay ? todo.due_date ?? undefined : undefined,
    start,
    allDay,
    status,
    statusLabel: look.label,
    tone: look.tone,
    done,
    overdue: isOverdue(todo, now),
    high: todo.priority === "high" && !done,
    reminding: !done && Boolean(todo.remind_at) && !todo.reminded_at,
  };
}

/** The to-dos with a due date in [from, to), done ones only when shown. A source that reads by date (the hub once it
 * pages its to-dos) would be asked for the same range. */
export function calendarEntries(
  todos: readonly Todo[],
  opts: { lookup?: SessionLookup; showDone: boolean; from: number; to: number; now?: number; repoGateLook?: TodoRepoGateLook },
): CalendarEntry[] {
  const out: CalendarEntry[] = [];
  for (const t of todos) {
    if (!opts.showDone && t.done_at) continue;
    const e = todoEntry(t, opts.lookup, opts.now, opts.repoGateLook);
    if (e && e.start >= opts.from && e.start < opts.to) out.push(e);
  }
  return out;
}

/** Open to-dos without a due date: not on the calendar, counted under it. */
export function undatedCount(todos: readonly Todo[]): number {
  return todos.filter((t) => !t.done_at && !(t.due_at && !Number.isNaN(Date.parse(t.due_at))) && todoDueDay(t) === undefined).length;
}

// ---- days ----

export function startOfDay(ms: number): number {
  const d = new Date(ms);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

/** The local day `n` days after ms's (a calendar day: across a daylight-saving change too). */
export function addDays(ms: number, n: number): number {
  const d = new Date(ms);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n, d.getHours(), d.getMinutes()).getTime();
}

export function sameDay(a: number, b: number): boolean {
  return startOfDay(a) === startOfDay(b);
}

/** Regions whose weeks start on Sunday or Saturday (CLDR's week data); the rest start on Monday. */
const SUNDAY_FIRST = new Set(("AG AS BD BR BS BT BW BZ CA CO DM DO ET GT GU HK HN ID IL IN JM JP KE KH KR LA MH MM MO MT MX " +
  "MZ NI NP PA PE PH PK PR PT PY SA SG SV TH TT TW UM US VE VI WS YE ZA ZW").split(" "));
const SATURDAY_FIRST = new Set("AE AF BH DJ DZ EG IQ IR JO KW LY OM QA SD SY".split(" "));

/**
 * The weekday weeks start on (0 Sunday … 6 Saturday) for the browser's language: its week info (`zh-CN` Monday,
 * `en-US` Sunday); a browser without week info goes by the language's region (`zh` → CN, Monday).
 */
export function weekStartDay(locale = typeof navigator !== "undefined" ? navigator.language : "en-US"): number {
  try {
    const l = new Intl.Locale(locale) as Intl.Locale & { getWeekInfo?: () => { firstDay: number }; weekInfo?: { firstDay: number } };
    const first = l.getWeekInfo?.().firstDay ?? l.weekInfo?.firstDay;
    if (first) return first % 7;
    return regionWeekStart(l.maximize().region);
  } catch {
    // an odd locale: Monday
  }
  return 1;
}

export function regionWeekStart(region: string | undefined): number {
  if (region && SUNDAY_FIRST.has(region)) return 0;
  if (region && SATURDAY_FIRST.has(region)) return 6;
  return 1;
}

export function startOfWeek(ms: number, first: number): number {
  const day = startOfDay(ms);
  const back = (new Date(day).getDay() - first + 7) % 7;
  return addDays(day, -back);
}

/** The days a mode shows around anchor: the day; its week; the weeks its month touches, whole. */
export function calendarDays(mode: CalendarMode, anchor: number, first: number): number[] {
  const day = startOfDay(anchor);
  const run = (from: number, n: number) => Array.from({ length: n }, (_, i) => addDays(from, i));
  if (mode === "day") return [day];
  if (mode === "week") return run(startOfWeek(day, first), 7);
  const d = new Date(day);
  const monthStart = new Date(d.getFullYear(), d.getMonth(), 1).getTime();
  const monthLast = new Date(d.getFullYear(), d.getMonth() + 1, 0).getTime();
  const from = startOfWeek(monthStart, first);
  const weeks = Math.round((startOfWeek(monthLast, first) - from) / 86_400_000 / 7) + 1;
  return run(from, weeks * 7);
}

/** From the start of the first day shown to the end of the last, in ms. */
export function calendarRange(mode: CalendarMode, anchor: number, first: number): [number, number] {
  const days = calendarDays(mode, anchor, first);
  // A day that starts at 01:00 after a DST jump must not carry that hour to either boundary.
  return [startOfDay(days[0]), startOfDay(addDays(days[days.length - 1], 1))];
}

/** The anchor a page later (n 1) or earlier (-1); a month later keeps the day where the month has it (Jan 31 → Feb 28). */
export function stepAnchor(anchor: number, mode: CalendarMode, n: number): number {
  if (mode === "day") return addDays(anchor, n);
  if (mode === "week") return addDays(anchor, 7 * n);
  const d = new Date(anchor);
  const last = new Date(d.getFullYear(), d.getMonth() + n + 1, 0).getDate();
  return new Date(d.getFullYear(), d.getMonth() + n, Math.min(d.getDate(), last), d.getHours(), d.getMinutes()).getTime();
}

/** All-day first, then by time; at one time open before done, then by title. */
export function drawOrder(a: CalendarEntry, b: CalendarEntry): number {
  if (a.allDay !== b.allDay) return a.allDay ? -1 : 1;
  if (a.start !== b.start) return a.start - b.start;
  if (a.done !== b.done) return a.done ? 1 : -1;
  return a.title.localeCompare(b.title) || a.id.localeCompare(b.id);
}

/** A day's entries in the order they are drawn. */
export function entriesOn(day: number, entries: readonly CalendarEntry[]): CalendarEntry[] {
  return entries.filter((e) => sameDay(e.start, day)).sort(drawOrder);
}

// ---- the time axis ----

/** Minutes from the start of day to ms on the clock, as the axis's hour lines, slotAt and dropOnAxis count them: on a day
 * the clocks change, 10:00 is still 600 minutes in, not the 540 or 660 that have passed since midnight (#9851). */
export function minutesInto(ms: number, day: number): number {
  const d = new Date(ms);
  const days = Math.round((startOfDay(ms) - startOfDay(day)) / 86_400_000);
  return days * 24 * 60 + d.getHours() * 60 + d.getMinutes() + (d.getSeconds() * 1000 + d.getMilliseconds()) / 60_000;
}

function blockEnd(e: CalendarEntry, minutes: number): number {
  return Math.max(e.end ?? e.start, e.start + minutes * 60_000);
}

/** The timed entries of a day whose blocks overlap, grouped: a block is `minutes` long from its start (a to-do's is a
 * fixed height), or until its end when that is later. */
export function clusters(timed: readonly CalendarEntry[], minutes: number): CalendarEntry[][] {
  const out: CalendarEntry[][] = [];
  let group: CalendarEntry[] = [];
  let end = -Infinity;
  for (const e of [...timed].sort(drawOrder)) {
    if (group.length && e.start >= end) {
      out.push(group);
      group = [];
    }
    group.push(e);
    end = Math.max(end, blockEnd(e, minutes));
  }
  if (group.length) out.push(group);
  return out;
}

export interface Placed {
  entry: CalendarEntry;
  lane: number;
  lanes: number;
}

/** Side by side where blocks overlap: each in the first lane free at its start, a cluster as narrow as its lanes need. */
export function lanes(timed: readonly CalendarEntry[], minutes: number): Placed[] {
  return clusters(timed, minutes).flatMap((group) => {
    const ends: number[] = [];
    const laned = group.map((e) => {
      let lane = ends.findIndex((x) => x <= e.start);
      if (lane < 0) lane = ends.push(-Infinity) - 1;
      ends[lane] = blockEnd(e, minutes);
      return { entry: e, lane };
    });
    return laned.map((p) => ({ ...p, lanes: ends.length }));
  });
}

/** The moment a click `minutes` into day means: snapped down to `snap` minutes, within the day. */
export function slotAt(day: number, minutes: number, snap = 30): number {
  const clamped = Math.min(Math.max(minutes, 0), 24 * 60 - snap);
  const snapped = Math.floor(clamped / snap) * snap;
  const d = new Date(startOfDay(day));
  return new Date(d.getFullYear(), d.getMonth(), d.getDate(), Math.floor(snapped / 60), snapped % 60).getTime();
}

// ---- moving a to-do (#2045) ----

/** Where a moved to-do is due: at a moment (`at`), or all day on `day` (its local midnight) without one. */
export interface CalendarDrop {
  day: number;
  at?: number;
}

/** What a drag on the axis snaps to, in minutes. */
export const DROP_SNAP = 15;

/** A block dropped with its top edge `minutes` into day: due at the nearest quarter hour, within the day. */
export function dropOnAxis(day: number, minutes: number, snap = DROP_SNAP): CalendarDrop {
  const clamped = Math.min(Math.max(minutes, 0), 24 * 60 - snap);
  const snapped = Math.round(clamped / snap) * snap;
  const d = new Date(startOfDay(day));
  return { day: d.getTime(), at: new Date(d.getFullYear(), d.getMonth(), d.getDate(), Math.floor(snapped / 60), snapped % 60).getTime() };
}

/** e dropped on day as a whole: at the same time of day there (the month), or all day there (the All day row, or one
 * that already was all day). */
export function dropOnDay(e: CalendarEntry, day: number, allDay = e.allDay): CalendarDrop {
  const d = new Date(startOfDay(day));
  if (allDay) return { day: d.getTime() };
  const t = new Date(e.start);
  return { day: d.getTime(), at: new Date(d.getFullYear(), d.getMonth(), d.getDate(), t.getHours(), t.getMinutes(), t.getSeconds()).getTime() };
}

/** What a move is as PATCH /api/v1/todos/{id} takes it (either due field replaces the other); none when e would land
 * where it already is. */
export type DuePatch = { due_at: string; due_date?: undefined } | { due_date: string; due_at?: undefined };

export function dropPatch(e: CalendarEntry, drop: CalendarDrop): DuePatch | undefined {
  if (drop.at !== undefined) return !e.allDay && e.start === drop.at ? undefined : { due_at: new Date(drop.at).toISOString() };
  return e.allDay && sameDay(e.start, drop.day) ? undefined : { due_date: dayKey(drop.day) };
}

/** todo as it is once moved: drawn there at once, before the hub has answered. */
export function movedTodo(todo: Todo, patch: DuePatch): Todo {
  return { ...todo, due_at: patch.due_at ?? null, due_date: patch.due_date ?? null };
}

/** Where a day without anything on it starts. */
export const MORNING = 8;

/** Where the axis first scrolls to on the days shown: the earlier of an hour and a half before now (when today is shown)
 * and an hour before their first timed entry, so the morning's to-dos are in sight; the morning without either. */
export function firstHour(days: readonly number[], timed: readonly CalendarEntry[], now = Date.now()): number {
  const hours: number[] = [];
  if (days.some((d) => sameDay(d, now))) {
    const back = now - 90 * 60_000;
    hours.push(sameDay(back, now) ? new Date(back).getHours() : 0);
  }
  const shown = timed.filter((e) => days.some((d) => sameDay(e.start, d)));
  if (shown.length) hours.push(Math.max(0, new Date(Math.min(...shown.map((e) => e.start))).getHours() - 1));
  return hours.length ? Math.min(...hours) : MORNING;
}

// ---- the month ----

/** What a day of the month shows in `lines` lines: all when they fit, else a line fewer and how many more. */
export function fitDay<T>(entries: readonly T[], lines: number): { shown: T[]; more: number } {
  if (entries.length <= lines || lines <= 0) return { shown: [...entries], more: 0 };
  const shown = entries.slice(0, lines - 1);
  return { shown, more: entries.length - shown.length };
}

// ---- words and addresses ----

/** The period's heading: "Mon, Nov 9" · "Nov 9 – 15" · "November 2026", as the language writes them. */
export function calendarTitle(mode: CalendarMode, anchor: number, first: number): string {
  if (mode === "day") return formatDate(anchor, { weekday: "short", month: "short", day: "numeric" });
  if (mode === "month") return formatDate(anchor, { year: "numeric", month: "long" });
  const days = calendarDays("week", anchor, first);
  const from = days[0], to = days[6];
  // in the page's language ("Nov 9 – 15"); across a month or a year, both days in full
  const sameMonth = new Date(from).getMonth() === new Date(to).getMonth();
  const sameYear = new Date(from).getFullYear() === new Date(to).getFullYear();
  const start = formatDate(from, { month: "short", day: "numeric", year: sameYear ? undefined : "numeric" });
  const end = formatDate(to, sameMonth ? { day: "numeric" } : { month: "short", day: "numeric", year: sameYear ? undefined : "numeric" });
  return `${start} – ${end}`;
}

/** The weekdays' short names in the order the weeks start, in the page's language ("Mon"; narrow: "M"). */
export function weekdayNames(first: number, narrow: boolean): string[] {
  // 2023-01-01 was a Sunday
  return Array.from({ length: 7 }, (_, i) => formatDate(new Date(2023, 0, 1 + ((first + i) % 7)), { weekday: narrow ? "narrow" : "short" }));
}

/** A day as the address and the hub write it: "YYYY-MM-DD", local. */
export function dayKey(ms: number): string {
  const d = new Date(ms);
  return `${String(d.getFullYear()).padStart(4, "0")}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** A "YYYY-MM-DD" day's local midnight; undefined for anything else. */
export function parseDayKey(s: string | null | undefined): number | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s ?? "");
  if (!m) return undefined;
  const ms = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])).getTime();
  return Number.isNaN(ms) ? undefined : ms;
}

export function isCalendarMode(s: string | null | undefined): s is CalendarMode {
  return s === "day" || s === "week" || s === "month";
}
