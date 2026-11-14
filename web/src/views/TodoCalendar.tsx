// The to-dos as a calendar (#2029, docs/assistant/todos.md "Calendar"): a day, a week or a month of those with a due
// date, drawn as the list draws them (state, overdue, done, priority, reminder). Clicking one opens it, clicking the
// empty axis makes a new one due there; those without a due date are counted under it, a click from the list. The
// page (Todos.tsx) keeps the mode and the day in the address. A to-do can be dragged to another time or day (#2045,
// useDrag below), saved at once. What goes where: lib/todo-calendar.ts.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type RefObject } from "react";
import { t } from "../i18n";
import {
  calendarDays,
  calendarEntries,
  calendarRange,
  calendarTitle,
  dropOnAxis,
  dropOnDay,
  dropPatch,
  entriesOn,
  firstHour,
  fitDay,
  lanes,
  minutesInto,
  movedTodo,
  sameDay,
  slotAt,
  startOfDay,
  stepAnchor,
  undatedCount,
  weekdayNames,
  weekStartDay,
  type CalendarDrop,
  type CalendarEntry,
  type CalendarMode,
  type DuePatch,
} from "../lib/todo-calendar";
import { TODO_STATUS_INFO, type SessionLookup, type Todo } from "../lib/todos";
import { PHONE, useMedia } from "../lib/viewport";
import { Button, cx, useNow } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { Segmented } from "./assistant/parts";

/** An hour on the axis, in px: a to-do's block (BLOCK px high) takes most of one. */
const HOUR = 48;
const BLOCK = 44;
const BLOCK_MINUTES = (BLOCK / HOUR) * 60;
const GUTTER = 56;
/** Room the axis opens with above its first hour: that hour's label sits across its line and would be cut. */
const LABEL_ROOM = HOUR / 4;
/** Lines a day of the month shows: its entries, or one fewer and "N more". */
const MONTH_LINES = 3;

/** A touch drags after this long held still (before it, a swipe scrolls and turns pages); a mouse after MOVE_SLOP px. */
const HOLD_MS = 350;
const MOVE_SLOP = 5;
/** Within this many px of the axis's top or bottom edge, a drag scrolls it. */
const EDGE = 32;
/** Today's date in the week's head and the month: the accent fill of the design system's buttons (ui/atoms Button
 *  `accent`), whose words keep 4.5:1 in both schemes (theme-contrast.test.ts, `--accent-ink` on `--surface`); white on
 *  the lighter `--accent` was 2.9:1 in dark and 3.6:1 in light (audit item 79). */
const TODAY = "bg-accent-ink font-semibold text-surface";

/** Where a new to-do made on the calendar is due: at `at`, or all day on `day` without it. */
export interface CalendarSlot {
  day: number;
  at?: number;
}

/** An entry's colors: its state's, red once overdue (before the state: what is late is what to see first). */
function look(e: CalendarEntry): { bar: string; ground: string; text: string } {
  if (e.overdue) return { bar: "bg-red", ground: "bg-red/10", text: "text-red" };
  switch (TODO_STATUS_INFO[e.status].tone) {
    case "red":
      return { bar: "bg-red", ground: "bg-red/10", text: "text-red" };
    case "green":
      return { bar: "bg-green", ground: "bg-green-tint", text: "text-green" };
    case "amber":
      return { bar: "bg-orange", ground: "bg-orange-tint", text: "text-orange" };
    default:
      return { bar: "bg-ink-3", ground: "bg-hover", text: "text-ink-3" };
  }
}

const clock = (ms: number) => new Date(ms).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });

function timeText(e: CalendarEntry): string {
  const at = clock(e.start);
  return e.overdue ? t("todos.calendar_was_due", { when: at }) : at;
}

function spoken(e: CalendarEntry): string {
  return [e.title, e.statusLabel, e.allDay ? t("todos.calendar_all_day") : timeText(e)].join(", ");
}

function Marks({ e }: { e: CalendarEntry }) {
  return (
    <>
      {e.high && <Icon name="flag" size={11} className="shrink-0 text-orange" />}
      {e.reminding && <Icon name="bell" size={11} className="shrink-0 text-ink-3" />}
    </>
  );
}

/** Picks up an entry to drag it (useDrag). */
type DragStart = (e: CalendarEntry, ev: ReactPointerEvent<HTMLElement>) => void;

/** An entry in a row of its own: the all-day row, a day of the month. */
function Chip({ e, onOpen, time, compact, onDrag, moving, ghost }: { e: CalendarEntry; onOpen: (id: string) => void; time?: boolean; compact?: boolean; onDrag?: DragStart; moving?: boolean; ghost?: boolean }) {
  const l = look(e);
  return (
    <button
      type="button"
      data-testid={ghost ? "todo-calendar-drop" : "todo-calendar-entry"}
      data-todo-id={e.todoId}
      aria-label={spoken(e)}
      aria-hidden={ghost || undefined}
      tabIndex={ghost ? -1 : undefined}
      onPointerDown={onDrag && ((ev) => onDrag(e, ev))}
      onClick={(ev) => {
        ev.stopPropagation();
        onOpen(e.todoId);
      }}
      className={cx(
        "relative flex w-full min-w-0 items-center gap-1 overflow-hidden rounded-chip text-left select-none hover:brightness-95 [-webkit-touch-callout:none]",
        compact ? "h-[18px] pr-1 pl-2 text-[11px]" : "h-[22px] pr-1.5 pl-2.5 text-[12px]",
        l.ground,
        e.done && "opacity-50",
        moving && "opacity-30",
        ghost && "pointer-events-none shadow-card ring-2 ring-accent",
      )}
    >
      <span aria-hidden className={cx("absolute inset-y-0 left-0", compact ? "w-0.5" : "w-[3px]", l.bar)} />
      {time && !e.allDay && <span className="shrink-0 text-ink-3 tabular-nums">{clock(e.start)}</span>}
      <span className={cx("truncate", e.overdue ? "text-red" : "text-ink", e.done && "line-through")}>{e.title}</span>
      {!compact && <Marks e={e} />}
    </button>
  );
}

/** A to-do on the time axis: its top edge is when it is due. */
function Block({ e, onOpen, selected, onDrag, moving }: { e: CalendarEntry; onOpen: (id: string) => void; selected: boolean; onDrag?: DragStart; moving?: boolean }) {
  const l = look(e);
  return (
    <button
      type="button"
      data-testid="todo-calendar-entry"
      data-todo-id={e.todoId}
      aria-label={spoken(e)}
      onPointerDown={onDrag && ((ev) => onDrag(e, ev))}
      onClick={(ev) => {
        ev.stopPropagation();
        onOpen(e.todoId);
      }}
      className={cx(
        "relative flex h-full w-full flex-col overflow-hidden rounded-chip py-1 pr-1.5 pl-2.5 text-left select-none hover:brightness-95 [-webkit-touch-callout:none]",
        l.ground,
        e.done && "opacity-50",
        selected && "ring-2 ring-accent",
        moving && "opacity-30",
      )}
    >
      <span aria-hidden className={cx("absolute inset-y-0 left-0 w-[3px]", l.bar)} />
      <span className="flex min-w-0 items-center gap-1">
        <span className={cx("truncate text-[12.5px] font-semibold text-ink", e.done && "line-through")}>{e.title}</span>
        <Marks e={e} />
      </span>
      <span className="truncate text-[11px] text-ink-3">
        <span className={e.overdue ? "text-red" : l.text}>{e.statusLabel}</span>
        <span className={e.overdue ? "text-red" : undefined}> · {timeText(e)}</span>
      </span>
    </button>
  );
}

/** A day or a week on a time axis: the all-day row, then the hours, each to-do at its due time, side by side where they
 * overlap; a red line at the moment. Clicking the empty axis offers a new to-do due there. */
function Axis({
  days,
  entries,
  now,
  showHeader,
  selectedId,
  onOpen,
  onAdd,
  onPickDay,
  drag,
  onDrag,
}: {
  days: number[];
  entries: CalendarEntry[];
  now: number;
  showHeader: boolean;
  selectedId?: string;
  onOpen: (id: string) => void;
  onAdd: (slot: CalendarSlot) => void;
  onPickDay: (day: number) => void;
  drag: Dragging | null;
  onDrag?: DragStart;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const names = weekdayNames(new Date(days[0]).getDay(), false);
  const timed = entries.filter((e) => !e.allDay);
  const today = days.find((d) => sameDay(d, now));
  // Open where the day's to-dos and the moment are in sight; again whenever the days change.
  const start = firstHour(days, timed, now);
  useLayoutEffect(() => {
    if (scroller.current) scroller.current.scrollTop = Math.max(0, start * HOUR - LABEL_ROOM);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [days[0], days.length]);
  const cols = { gridTemplateColumns: `${GUTTER}px repeat(${days.length}, minmax(0, 1fr))` };
  const nowY = today !== undefined ? (minutesInto(now, today) / 60) * HOUR : undefined;

  return (
    <div className="flex h-full flex-col" data-testid="todo-calendar-axis">
      {showHeader && (
        <div className="grid shrink-0 border-b border-line" style={cols}>
          <span />
          {days.map((d, i) => {
            const isToday = sameDay(d, now);
            return (
              <button
                key={d}
                type="button"
                data-testid="todo-calendar-day-header"
                onClick={() => onPickDay(d)}
                className="flex items-center gap-1.5 border-l border-line px-2.5 py-2 text-left hover:bg-hover"
              >
                <span className="text-[12px] text-ink-3">{names[i]}</span>
                <span className={cx("inline-flex h-6 min-w-6 items-center justify-center rounded-full px-1 text-[14px] tabular-nums", isToday ? TODAY : "text-ink")} data-today={isToday || undefined}>
                  {new Date(d).getDate()}
                </span>
              </button>
            );
          })}
        </div>
      )}
      <div className="grid shrink-0 border-b border-line-strong" style={cols}>
        <span className="pt-1.5 pr-2 text-right text-[11px] text-ink-3">{t("todos.calendar_all_day")}</span>
        {days.map((d) => {
          const target = drag?.drop && drag.drop.at === undefined && sameDay(drag.drop.day, d);
          return (
            <div
              key={d}
              data-testid="todo-calendar-all-day"
              data-drop="all-day"
              data-day={d}
              onClick={() => onAdd({ day: d })}
              className={cx("flex min-h-7 min-w-0 cursor-cell flex-col gap-0.5 border-l border-line p-1", target && "bg-accent/10")}
            >
              {entriesOn(d, entries)
                .filter((e) => e.allDay)
                .map((e) => (
                  <Chip key={e.id} e={e} onOpen={onOpen} onDrag={onDrag} moving={drag?.entry.id === e.id} />
                ))}
              {target && <Chip e={drag.entry} onOpen={() => {}} ghost />}
            </div>
          );
        })}
      </div>
      <div ref={scroller} data-drop-scroll className="relative min-h-0 flex-1 overflow-y-auto">
        <div className="relative grid" style={{ ...cols, height: 24 * HOUR }}>
          <div className="relative">
            {Array.from({ length: 23 }, (_, i) => i + 1)
              .filter((h) => nowY === undefined || Math.abs(h * HOUR - nowY) > HOUR / 4)
              .map((h) => (
                <span key={h} className="absolute right-2 -translate-y-1/2 text-[11px] text-ink-3 tabular-nums" style={{ top: h * HOUR }}>
                  {clock(new Date(2023, 0, 1, h).getTime())}
                </span>
              ))}
            {nowY !== undefined && (
              <span className="absolute right-2 -translate-y-1/2 text-[11px] font-bold text-red tabular-nums" style={{ top: nowY }}>
                {clock(now)}
              </span>
            )}
          </div>
          {days.map((d) => {
            const mine = entriesOn(d, timed);
            return (
              <div
                key={d}
                data-testid="todo-calendar-column"
                data-drop="axis"
                data-day={d}
                className="relative cursor-cell border-l border-line"
                style={{ backgroundImage: `repeating-linear-gradient(to bottom, var(--line) 0 1px, transparent 1px ${HOUR}px)` }}
                onClick={(ev) => {
                  const box = ev.currentTarget.getBoundingClientRect();
                  onAdd({ day: d, at: slotAt(d, ((ev.clientY - box.top) / HOUR) * 60) });
                }}
              >
                {lanes(mine, BLOCK_MINUTES).map((p) => (
                  <BlockAt key={p.entry.id} placed={p} day={d} onOpen={onOpen} selected={p.entry.todoId === selectedId} onDrag={onDrag} moving={drag?.entry.id === p.entry.id} />
                ))}
                {drag?.drop?.at !== undefined && sameDay(drag.drop.day, d) && <DropBlock e={drag.entry} at={drag.drop.at} day={d} />}
                {sameDay(d, now) && nowY !== undefined && (
                  <div aria-hidden className="pointer-events-none absolute right-0 left-0 border-t-[1.5px] border-red" style={{ top: nowY }}>
                    <span className="absolute -top-[4.5px] -left-1 size-2 rounded-full bg-red" />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function BlockAt({ placed, day, onOpen, selected, onDrag, moving }: { placed: { entry: CalendarEntry; lane: number; lanes: number }; day: number; onOpen: (id: string) => void; selected: boolean; onDrag?: DragStart; moving?: boolean }) {
  const top = (minutesInto(placed.entry.start, day) / 60) * HOUR;
  const width = 100 / placed.lanes;
  return (
    <div className="absolute" style={{ top: top + 1, height: BLOCK - 2, left: `calc(${placed.lane * width}% + 3px)`, width: `calc(${width}% - 5px)` }}>
      <Block e={placed.entry} onOpen={onOpen} selected={selected} onDrag={onDrag} moving={moving} />
    </div>
  );
}

/** Where a dragged to-do would land on the axis, and the time it would be due. */
function DropBlock({ e, at, day }: { e: CalendarEntry; at: number; day: number }) {
  const l = look(e);
  const top = (minutesInto(at, day) / 60) * HOUR;
  return (
    <div data-testid="todo-calendar-drop" aria-hidden className="pointer-events-none absolute right-[3px] left-[3px] z-10 rounded-chip bg-surface shadow-card" style={{ top: top + 1, height: BLOCK - 2 }}>
      <div className={cx("relative flex h-full flex-col overflow-hidden rounded-chip py-1 pr-1.5 pl-2.5 ring-2 ring-accent", l.ground)}>
        <span aria-hidden className={cx("absolute inset-y-0 left-0 w-[3px]", l.bar)} />
        <span className="truncate text-[12.5px] font-semibold text-ink">{e.title}</span>
        <span data-testid="todo-calendar-drop-time" className="truncate text-[11px] font-semibold text-accent-ink tabular-nums">
          {clock(at)}
        </span>
      </div>
    </div>
  );
}

/** A month: the weeks it touches, each day with its first entries and how many more; clicking a day opens it. */
function Month({
  anchor,
  first,
  entries,
  now,
  compact,
  onOpen,
  onPickDay,
  drag,
  onDrag,
}: {
  anchor: number;
  first: number;
  entries: CalendarEntry[];
  now: number;
  compact: boolean;
  onOpen: (id: string) => void;
  onPickDay: (day: number) => void;
  drag: Dragging | null;
  onDrag?: DragStart;
}) {
  const days = calendarDays("month", anchor, first);
  const month = new Date(anchor).getMonth();
  const names = weekdayNames(first, compact);
  return (
    <div className="flex h-full flex-col" data-testid="todo-calendar-month">
      <div className="grid shrink-0 grid-cols-7 border-b border-line">
        {names.map((n, i) => (
          <span key={i} className={cx("py-2 text-[12px] text-ink-3", compact ? "text-center" : "px-2.5")}>
            {n}
          </span>
        ))}
      </div>
      <div className="grid min-h-0 flex-1 grid-cols-7" style={{ gridTemplateRows: `repeat(${days.length / 7}, minmax(0, 1fr))` }}>
        {days.map((d, i) => {
          const inMonth = new Date(d).getMonth() === month;
          const isToday = sameDay(d, now);
          const fit = fitDay(entriesOn(d, entries), MONTH_LINES);
          const date = new Date(d);
          const target = drag?.drop && sameDay(drag.drop.day, d);
          return (
            <div
              key={d}
              data-testid="todo-calendar-month-day"
              data-drop="month"
              data-day={d}
              onClick={() => onPickDay(d)}
              className={cx(
                "flex min-h-0 min-w-0 cursor-pointer flex-col gap-0.5 overflow-hidden p-1 hover:bg-hover",
                i >= 7 && "border-t border-line",
                i % 7 > 0 && "border-l border-line",
                !inMonth && "bg-inset",
                isToday && "bg-accent/5",
                target && "bg-accent/10",
              )}
            >
              <span className={cx("mb-0.5 flex", compact && "justify-center")}>
                <span
                  className={cx(
                    "inline-flex h-[22px] min-w-[22px] items-center justify-center rounded-full px-1 text-[12.5px] tabular-nums",
                    isToday ? TODAY : inMonth ? "text-ink" : "text-ink-3",
                  )}
                  data-today={isToday || undefined}
                >
                  {date.getDate() === 1 && !compact ? formatMonthDay(d) : date.getDate()}
                </span>
              </span>
              {target && drag.drop && <Chip e={{ ...drag.entry, start: drag.drop.at ?? drag.drop.day }} onOpen={() => {}} time={!compact} compact={compact} ghost />}
              {fit.shown.map((e) => (
                <Chip key={e.id} e={e} onOpen={compact ? () => onPickDay(d) : onOpen} time={!compact} compact={compact} onDrag={onDrag} moving={drag?.entry.id === e.id} />
              ))}
              {fit.more > 0 && (
                <span className="truncate pl-1 text-[11.5px] text-ink-3">{compact ? `+${fit.more}` : t("todos.calendar_more", { count: fit.more })}</span>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function formatMonthDay(ms: number): string {
  return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

/** A to-do being dragged (#2045), and where it would land now (none: nowhere it can go). */
interface Dragging {
  entry: CalendarEntry;
  drop?: CalendarDrop;
}

/** Where entry would land with the pointer at x, y: on an axis column its top edge `grab` px above the pointer (it
 * stays where it was picked up), on a day's All day row all day, on a day of the month at the same time. */
function dropAt(entry: CalendarEntry, x: number, y: number, grab: number): CalendarDrop | undefined {
  const el = document.elementFromPoint(x, y)?.closest<HTMLElement>("[data-drop]");
  const day = Number(el?.dataset.day);
  if (!el || !Number.isFinite(day)) return undefined;
  switch (el.dataset.drop) {
    case "axis":
      return dropOnAxis(day, ((y - grab - el.getBoundingClientRect().top) / HOUR) * 60);
    case "all-day":
      return dropOnDay(entry, day, true);
    case "month":
      return dropOnDay(entry, day);
  }
  return undefined;
}

/** The click a drag ends with (on the entry, or the axis under it) neither opens the to-do nor makes a new one. */
function swallowNextClick() {
  const stop = (e: MouseEvent) => {
    e.stopPropagation();
    e.preventDefault();
  };
  window.addEventListener("click", stop, { capture: true, once: true });
  setTimeout(() => window.removeEventListener("click", stop, { capture: true }), 0);
}

/** Dragging an entry to another time or day: a mouse once it moves a few px (a click still opens the to-do), a touch
 * once it is held still (a swipe still scrolls and turns pages). The axis scrolls when the pointer nears its edge;
 * Escape puts it back; letting go where it can land calls onDrop. */
function useDrag(root: RefObject<HTMLElement | null>, onDrop: (entry: CalendarEntry, drop: CalendarDrop) => void) {
  const [dragging, setDragging] = useState<Dragging | null>(null);
  const start = useCallback<DragStart>(
    (entry, ev) => {
      if (ev.button !== 0 || ev.ctrlKey || ev.metaKey || ev.shiftKey || ev.altKey) return;
      const el = ev.currentTarget;
      const id = ev.pointerId;
      const x0 = ev.clientX;
      const y0 = ev.clientY;
      const grab = y0 - el.getBoundingClientRect().top;
      const touch = ev.pointerType === "touch";
      let active = false;
      let cancelled = false;
      let current: Dragging | null = null;
      const show = (d: Dragging | null) => {
        current = d;
        setDragging(d);
      };
      const begin = () => {
        active = true;
        try {
          el.setPointerCapture(id);
        } catch {
          // the pointer is gone already: its up or cancel ends this
        }
        show({ entry });
      };
      const hold = touch ? setTimeout(begin, HOLD_MS) : undefined;
      const scroll = (y: number) => {
        const box = root.current?.querySelector<HTMLElement>("[data-drop-scroll]");
        if (!box) return;
        const r = box.getBoundingClientRect();
        if (y < r.top + EDGE) box.scrollTop -= EDGE / 2;
        else if (y > r.bottom - EDGE) box.scrollTop += EDGE / 2;
      };
      const move = (e: PointerEvent) => {
        if (e.pointerId !== id || cancelled) return;
        if (!active) {
          if (Math.hypot(e.clientX - x0, e.clientY - y0) <= MOVE_SLOP) return;
          if (touch) return end(); // moved before the hold: a swipe, the page's to scroll
          begin();
        }
        scroll(e.clientY);
        show({ entry, drop: dropAt(entry, e.clientX, e.clientY, grab) });
      };
      // once held, a touch moves the to-do, not the page
      const still = (e: TouchEvent) => {
        if (active) e.preventDefault();
      };
      const up = (e: PointerEvent) => {
        if (e.pointerId !== id) return;
        const drop = current?.drop;
        end();
        if (!active) return;
        swallowNextClick();
        if (!cancelled && drop) onDrop(entry, drop);
      };
      const lost = (e: PointerEvent) => {
        if (e.pointerId === id) end();
      };
      const key = (e: KeyboardEvent) => {
        if (e.key !== "Escape" || !active || cancelled) return;
        e.preventDefault();
        e.stopPropagation();
        cancelled = true;
        show(null);
      };
      const menu = (e: Event) => {
        if (active) e.preventDefault();
      };
      function end() {
        clearTimeout(hold);
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
        window.removeEventListener("pointercancel", lost);
        window.removeEventListener("keydown", key, true);
        window.removeEventListener("touchmove", still);
        window.removeEventListener("contextmenu", menu, true);
        if (current) show(null);
      }
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
      window.addEventListener("pointercancel", lost);
      window.addEventListener("keydown", key, true);
      window.addEventListener("touchmove", still, { passive: false });
      window.addEventListener("contextmenu", menu, true);
    },
    [root, onDrop],
  );
  return { dragging, start };
}

export function TodoCalendar({
  todos,
  lookup,
  mode,
  anchor,
  showDone,
  selectedId,
  onMode,
  onAnchor,
  onShowDone,
  onOpen,
  onAdd,
  onShowList,
  onMove,
}: {
  todos: readonly Todo[];
  lookup: SessionLookup;
  mode: CalendarMode;
  anchor: number;
  showDone: boolean;
  selectedId?: string;
  onMode: (mode: CalendarMode, day?: number) => void;
  onAnchor: (day: number) => void;
  onShowDone: (on: boolean) => void;
  onOpen: (id: string) => void;
  onAdd: (slot: CalendarSlot) => void;
  onShowList: () => void;
  /** Saves a to-do dragged to another time or day (#2045); resolves once saved, or failed (said by the page). Without
   * it nothing is dragged. */
  onMove?: (id: string, patch: DuePatch) => Promise<unknown>;
}) {
  const phone = useMedia(PHONE);
  const now = useNow(60_000);
  const first = useMemo(() => weekStartDay(), []);
  // A phone's width has no room for seven columns: its week is the day.
  const shown: CalendarMode = phone && mode === "week" ? "day" : mode;
  const [from, to] = calendarRange(shown, anchor, first);
  // A to-do dropped somewhere is drawn there at once, and back where it was if saving it fails.
  const [moves, setMoves] = useState<Record<string, DuePatch>>({});
  const shownTodos = useMemo(() => (Object.keys(moves).length ? todos.map((td) => (moves[td.id] ? movedTodo(td, moves[td.id]) : td)) : todos), [todos, moves]);
  const entries = useMemo(() => calendarEntries(shownTodos, { lookup, showDone, from, to, now }), [shownTodos, lookup, showDone, from, to, now]);
  const drop = useCallback(
    async (entry: CalendarEntry, at: CalendarDrop) => {
      const patch = dropPatch(entry, at);
      if (!patch || !onMove) return;
      setMoves((m) => ({ ...m, [entry.todoId]: patch }));
      try {
        await onMove(entry.todoId, patch);
      } finally {
        setMoves(({ [entry.todoId]: _done, ...rest }) => rest);
      }
    },
    [onMove],
  );
  const root = useRef<HTMLDivElement>(null);
  const { dragging, start: startDrag } = useDrag(root, drop);
  const onDrag = onMove ? startDrag : undefined;
  const undated = useMemo(() => undatedCount(todos), [todos]);
  const step = useCallback((n: number) => onAnchor(stepAnchor(anchor, shown, n)), [anchor, shown, onAnchor]);

  // ← → turn the page and T comes back to today, while nothing is being typed.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (e.metaKey || e.ctrlKey || e.altKey || (el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)))) return;
      if (e.key === "ArrowLeft") step(-1);
      else if (e.key === "ArrowRight") step(1);
      else if (e.key === "t" || e.key === "T") onAnchor(startOfDay(Date.now()));
      else return;
      e.preventDefault();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [step, onAnchor]);

  const modes: CalendarMode[] = phone ? ["day", "month"] : ["day", "week", "month"];
  const modeName: Record<CalendarMode, string> = { day: t("todos.calendar_day"), week: t("todos.calendar_week"), month: t("todos.calendar_month") };

  return (
    <div ref={root} className={cx("flex min-h-0 flex-1 flex-col", dragging && "cursor-grabbing select-none")} data-testid="todo-calendar" data-mode={shown} data-dragging={dragging ? "" : undefined}>
      <div className="flex shrink-0 flex-wrap items-center gap-2 px-3 py-3 sm:px-6">
        <Button tone="secondary" icon="chevronLeft" onClick={() => step(-1)} className="h-8 px-2" title={t("todos.calendar_previous")}>
          {null}
        </Button>
        <Button tone="secondary" onClick={() => onAnchor(startOfDay(Date.now()))} className="h-8 text-[12.5px]">
          {t("todos.calendar_today")}
        </Button>
        <Button tone="secondary" icon="chevronRight" onClick={() => step(1)} className="h-8 px-2" title={t("todos.calendar_next")}>
          {null}
        </Button>
        <h2 data-testid="todo-calendar-title" className="ml-1 truncate text-[16px] font-semibold text-ink">
          {calendarTitle(shown, anchor, first)}
        </h2>
        <div className="ml-auto flex items-center gap-3">
          <Segmented label={t("todos.calendar_mode")} value={shown} options={modes.map((v) => ({ v, label: modeName[v] }))} onChange={(v) => onMode(v)} compact />
          <label className="flex cursor-pointer items-center gap-1.5 text-[12.5px] whitespace-nowrap text-ink-2">
            <input type="checkbox" data-testid="todo-calendar-show-done" checked={showDone} onChange={(e) => onShowDone(e.target.checked)} className="accent-accent" />
            {t("todos.calendar_show_done")}
          </label>
        </div>
      </div>
      <div className="mx-3 min-h-0 flex-1 overflow-hidden rounded-card bg-surface shadow-card sm:mx-6">
        {shown === "month" ? (
          <Month anchor={anchor} first={first} entries={entries} now={now} compact={phone} onOpen={onOpen} onPickDay={(d) => onMode("day", d)} drag={dragging} onDrag={onDrag} />
        ) : (
          <Axis
            days={shown === "day" ? [startOfDay(anchor)] : calendarDays("week", anchor, first)}
            entries={entries}
            now={now}
            showHeader={shown === "week"}
            selectedId={selectedId}
            onOpen={onOpen}
            onAdd={onAdd}
            onPickDay={(d) => onMode("day", d)}
            drag={dragging}
            onDrag={onDrag}
          />
        )}
      </div>
      <div className="flex h-11 shrink-0 items-center gap-2 px-4 text-[12.5px] text-ink-3 sm:px-7">
        {undated > 0 && (
          <>
            <Icon name="list" size={13} />
            <span>{t("todos.calendar_undated", { count: undated })}</span>
            <button type="button" data-testid="todo-calendar-undated" onClick={onShowList} className="font-medium text-accent-ink hover:underline">
              {t("todos.calendar_see_list")}
            </button>
          </>
        )}
      </div>
    </div>
  );
}
