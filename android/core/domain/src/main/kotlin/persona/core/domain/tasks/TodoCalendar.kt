package persona.core.domain.tasks

import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.contracts.TodoDue
import persona.core.contracts.TodoState
import persona.core.contracts.parseTime
import persona.core.contracts.state
import java.time.DayOfWeek
import java.time.Instant
import java.time.LocalDate
import java.time.LocalTime
import java.time.YearMonth
import java.time.ZoneId
import java.time.temporal.TemporalAdjusters
import java.util.Locale

// The to-dos' calendar (#2046, docs/assistant/todos.md "Calendar"): which to-dos a day shows and in what order, the days
// a day, a week or a month shows and the weekday weeks start on; on the day's and the week's time axis (#4200), which
// to-dos sit side by side and where a tap makes one; where a to-do dragged onto another day or moment lands (#2045).
// The rules of iOS apple/Shared/TodoCalendar.swift and the web's web/src/lib/todo-calendar.ts.

/** A to-do on the calendar: on [day], at [time] there, or all day when [time] is null. */
data class CalendarEntry(
    val todoId: String,
    val title: String,
    val day: LocalDate,
    val time: LocalTime?,
    /** Its state, colored and named as on the list. */
    val state: TodoState,
    val done: Boolean,
    val overdue: Boolean,
    val high: Boolean,
    /** A reminder still to be pushed (the list's bell). */
    val reminding: Boolean,
    /** Specific at-rest display status (open PR, parked, or tracking concern) overriding "Not started". */
    val restStatus: TodoRestStatus? = null,
) {
    val allDay: Boolean get() = time == null
}

/** The to-do on the calendar: at its due moment in [zone], or all day on its due day; null without either. */
fun Todo.calendarEntry(sessionLookup: (SessionKey) -> Session?, now: Instant, zone: ZoneId): CalendarEntry? {
    val at = parseTime(dueAt)?.atZone(zone)
    val day = at?.toLocalDate() ?: dueDay ?: return null
    return CalendarEntry(
        todoId = id,
        title = title,
        day = day,
        time = at?.toLocalTime(),
        state = state(sessionLookup),
        done = isDone,
        overdue = isOverdue(now, zone),
        high = priority == "high" && !isDone,
        reminding = !isDone && remind != null && remindedAt == null,
        restStatus = restStatus(sessionLookup, now.toEpochMilli()),
    )
}

/** The to-dos due from [from] up to (not including) [until], done ones only when [showDone]; each day's in [CalendarDrawOrder]. */
fun calendarEntries(
    todos: List<Todo>,
    sessionLookup: (SessionKey) -> Session?,
    showDone: Boolean,
    from: LocalDate,
    until: LocalDate,
    now: Instant,
    zone: ZoneId,
): Map<LocalDate, List<CalendarEntry>> = todos.asSequence()
    .filter { showDone || !it.isDone }
    .mapNotNull { it.calendarEntry(sessionLookup, now, zone) }
    .filter { !it.day.isBefore(from) && it.day.isBefore(until) }
    .groupBy { it.day }
    .mapValues { (_, day) -> day.sortedWith(CalendarDrawOrder) }

/** Open to-dos without a due date: not on the calendar, counted under it. */
fun undatedCount(todos: List<Todo>): Int = todos.count { !it.isDone && parseTime(it.dueAt) == null && it.dueDay == null }

/** All-day first, then by time; at one time open before done, then by title. */
val CalendarDrawOrder: Comparator<CalendarEntry> =
    compareBy<CalendarEntry>({ !it.allDay }, { it.time }, { it.done }, { it.title }, { it.todoId })

/** What a day of the month shows in [lines] lines: all of them when they fit, else a line fewer and how many more. */
data class DayFit<T>(val shown: List<T>, val more: Int)

fun <T> fitDay(entries: List<T>, lines: Int): DayFit<T> {
    if (entries.size <= lines || lines <= 0) return DayFit(entries, 0)
    val shown = entries.take(lines - 1)
    return DayFit(shown, entries.size - shown.size)
}

/** The time an all-day to-do takes when it is given one ("Change Due Date…" with All day off), as iOS `TodoPlanDraft`. */
val DefaultDueTime: LocalTime = LocalTime.of(18, 0)

/**
 * A to-do dropped on [day] as a whole (#2045): at the same time there in [zone] (the month), or all day there when
 * [allDay] (a day's All Day row, or one that already was all day), as the web's `dropOnDay`.
 */
fun CalendarEntry.dropOnDay(day: LocalDate, zone: ZoneId, allDay: Boolean = this.allDay): TodoDue =
    if (allDay) TodoDue.Day(day) else TodoDue.At(day.atTime(time ?: DefaultDueTime).atZone(zone).toInstant())

/** How a drop on the time axis snaps: the block's top edge to the nearest quarter hour (the web's `DROP_SNAP`). */
const val DropSnap = 15

/** How a tap on the empty axis snaps: down to the half hour it is in (the web's `slotAt`, iOS `slot`). */
const val TapSnap = 30

/** A to-do dropped with its block's top edge [minutes] into [day] (#4200): at the nearest quarter hour, 00:00–23:45. */
fun dropOnAxis(day: LocalDate, minutes: Float, zone: ZoneId): TodoDue =
    TodoDue.At(day.atTime(dropTime(minutes)).atZone(zone).toInstant())

/** [minutes] into a day rounded to the nearest [snap], within the day (the last [snap] its latest). */
fun dropTime(minutes: Float, snap: Int = DropSnap): LocalTime {
    val clamped = minutes.coerceIn(0f, (24 * 60 - snap).toFloat())
    return LocalTime.MIN.plusMinutes((Math.round(clamped / snap) * snap).toLong())
}

/** The moment a tap [minutes] into a day means: snapped down to [snap], within the day. */
fun slotTime(minutes: Float, snap: Int = TapSnap): LocalTime {
    val clamped = minutes.coerceIn(0f, (24 * 60 - snap).toFloat())
    return LocalTime.MIN.plusMinutes((kotlin.math.floor(clamped / snap).toInt() * snap).toLong())
}

/** Where a carried to-do would land on the day's or the week's view (#4200): a day's All Day row, or a moment on its axis. */
sealed interface AxisTarget {
    val day: LocalDate
    data class AllDay(override val day: LocalDate) : AxisTarget
    data class At(override val day: LocalDate, val time: LocalTime) : AxisTarget
}

/**
 * The due date a drop on [target] sends: due that day all day, or at that moment in [zone]. A block let go where it
 * snaps back to its own place keeps its own time ("14:07" stays, not "14:00"), so a drop in place sends nothing.
 */
fun CalendarEntry.dropOn(target: AxisTarget, zone: ZoneId): TodoDue = when (target) {
    is AxisTarget.AllDay -> dropOnDay(target.day, zone, allDay = true)
    is AxisTarget.At -> {
        val own = time?.takeIf { target.day == day && dropTime(minutes) == target.time }
        TodoDue.At(target.day.atTime(own ?: target.time).atZone(zone).toInstant())
    }
}

/**
 * Where a to-do carried over the day's or the week's view lands: the day of [days] whose column is under [x] (across
 * the columns' [width], from the right when [rtl]); on its All Day row when [onAllDay], else at its block's top edge,
 * [blockTop] down the axis of hours [hourHeight] high (the scrolled-away part counted), snapped as [dropTime] snaps.
 * Null off the columns.
 */
fun axisDropTarget(
    x: Float,
    width: Float,
    days: List<LocalDate>,
    rtl: Boolean,
    onAllDay: Boolean,
    blockTop: Float,
    hourHeight: Float,
): AxisTarget? {
    val day = axisColumn(x, width, days.size, rtl)?.let(days::getOrNull) ?: return null
    if (onAllDay) return AxisTarget.AllDay(day)
    if (hourHeight <= 0f) return null
    return AxisTarget.At(day, dropTime(blockTop / hourHeight * 60))
}

/** The column of [columns] days side by side under [x] across [width] (the first at the right when [rtl]); null off them. */
fun axisColumn(x: Float, width: Float, columns: Int, rtl: Boolean = false): Int? {
    if (columns <= 0 || width <= 0f || x < 0f || x >= width) return null
    val column = (x / (width / columns)).toInt().coerceIn(0, columns - 1)
    return if (rtl) columns - 1 - column else column
}

/** The due date a drop sends: null when it lands where the to-do already is (nothing is sent). */
fun Todo.moveTo(due: TodoDue): TodoDue? = due.takeIf { it != this.due }

/**
 * The day of the month grid under a point, as an index into its days (0 is the first shown): [x] across the grid's
 * [width] (seven days a row, from the right when [rtl]), [y] down its rows of [rowHeight] (the scrolled-away part
 * counted); null off the grid.
 */
fun monthDropIndex(x: Float, y: Float, width: Float, rowHeight: Float, rows: Int, rtl: Boolean = false): Int? {
    if (width <= 0f || rowHeight <= 0f || x < 0f || x >= width || y < 0f) return null
    val row = (y / rowHeight).toInt()
    if (row >= rows) return null
    val column = (x / (width / 7)).toInt().coerceIn(0, 6)
    return row * 7 + if (rtl) 6 - column else column
}

/** Which way a month taller than its space scrolls while a dragged to-do is within [edge] of the top or bottom of the [height] in sight: -1 up, 1 down, 0 not. */
fun edgeScroll(y: Float, height: Float, edge: Float): Int = when {
    y < edge -> -1
    y > height - edge -> 1
    else -> 0
}

/** The calendar's three spans (#4200), iOS `TodoCalendarMode`; each device keeps its choice. */
enum class CalendarMode { Day, Week, Month }

/** The days [mode] shows around [anchor]: the day; its week; the whole weeks its month touches. Weeks start on [first]. */
fun calendarDays(mode: CalendarMode, anchor: LocalDate, first: DayOfWeek): List<LocalDate> = when (mode) {
    CalendarMode.Day -> listOf(anchor)
    CalendarMode.Week -> weekOf(anchor, first)
    CalendarMode.Month -> monthDays(YearMonth.from(anchor), first)
}

/** The week [day] is in, weeks starting on [first] (the day view's strip and the week view's columns). */
fun weekOf(day: LocalDate, first: DayOfWeek): List<LocalDate> {
    val from = day.with(TemporalAdjusters.previousOrSame(first))
    return List(7) { from.plusDays(it.toLong()) }
}

/**
 * The anchor a page later ([n] 1) or earlier (-1): a day, a week or a month. A month keeps the day, the month's last
 * when it is shorter (Jan 31 → Feb 28), as the web's `stepAnchor`.
 */
fun stepAnchor(anchor: LocalDate, mode: CalendarMode, n: Int): LocalDate = when (mode) {
    CalendarMode.Day -> anchor.plusDays(n.toLong())
    CalendarMode.Week -> anchor.plusWeeks(n.toLong())
    CalendarMode.Month -> anchor.plusMonths(n.toLong())
}

/** A to-do on a day's axis: in lane [lane] of [lanes] side by side. */
data class AxisPlaced(val entry: CalendarEntry, val lane: Int, val lanes: Int)

/** Minutes from the start of its day to a timed entry (0 for an all-day one). */
val CalendarEntry.minutes: Float get() = (time?.toSecondOfDay() ?: 0) / 60f

/**
 * The timed entries of one day whose blocks overlap, grouped, in [CalendarDrawOrder]: a to-do's block is [blockMinutes]
 * long from its time (a fixed height, as iOS and the web draw it).
 */
fun calendarClusters(timed: List<CalendarEntry>, blockMinutes: Float): List<List<CalendarEntry>> {
    val out = mutableListOf<List<CalendarEntry>>()
    var group = mutableListOf<CalendarEntry>()
    var groupEnd = Float.NEGATIVE_INFINITY
    for (e in timed.sortedWith(CalendarDrawOrder)) {
        if (group.isNotEmpty() && e.minutes >= groupEnd) {
            out += group
            group = mutableListOf()
        }
        group += e
        groupEnd = maxOf(groupEnd, e.minutes + blockMinutes)
    }
    if (group.isNotEmpty()) out += group
    return out
}

/** Side by side where blocks overlap: each entry in the first lane free at its time, every entry of a cluster as narrow as the cluster's lanes need. */
fun calendarLanes(timed: List<CalendarEntry>, blockMinutes: Float): List<AxisPlaced> =
    calendarClusters(timed, blockMinutes).flatMap { group ->
        val ends = mutableListOf<Float>()
        val laned = group.map { e ->
            var lane = ends.indexOfFirst { it <= e.minutes }
            if (lane < 0) {
                ends += Float.NEGATIVE_INFINITY
                lane = ends.lastIndex
            }
            ends[lane] = e.minutes + blockMinutes
            e to lane
        }
        laned.map { (e, lane) -> AxisPlaced(e, lane, ends.size) }
    }

/** Where a day without anything on it starts (iOS `TodoCalendar.morning`). */
const val CalendarMorning = 8

/** Minutes either side of now where an hour's label gives way to the now line's (iOS `nowLabelRoom`). */
const val NowLabelRoom = 15

/**
 * The hour the axis first scrolls to on [days]: the earlier of an hour and a half before now (when today is shown) and
 * an hour before their first timed entry, so the morning's to-dos are in sight; [CalendarMorning] without either.
 */
fun firstHour(days: List<LocalDate>, timed: List<CalendarEntry>, now: Instant, zone: ZoneId): Int {
    val hours = mutableListOf<Int>()
    val here = now.atZone(zone)
    if (here.toLocalDate() in days) {
        val back = here.minusMinutes(90)
        hours += if (back.toLocalDate() == here.toLocalDate()) back.hour else 0
    }
    timed.filter { it.time != null && it.day in days }.minByOrNull { it.time!! }?.let { hours += maxOf(0, it.time!!.hour - 1) }
    return hours.minOrNull() ?: CalendarMorning
}

/** The days the month grid shows: the whole weeks [month] touches, weeks starting on [first]. */
fun monthDays(month: YearMonth, first: DayOfWeek): List<LocalDate> {
    val from = month.atDay(1).with(TemporalAdjusters.previousOrSame(first))
    val to = month.atEndOfMonth().with(TemporalAdjusters.nextOrSame(first.minus(1)))
    return generateSequence(from) { it.plusDays(1) }.takeWhile { !it.isAfter(to) }.toList()
}

/** The weekdays in the order the weeks start. */
fun weekdays(first: DayOfWeek): List<DayOfWeek> = List(7) { first.plus(it.toLong()) }

/**
 * The weekday weeks start on for [locale]: its region's (CLDR's week data, as the web reads it: `zh-CN` Monday, `en-US`
 * Sunday); a locale without a region goes by its language's usual one (`zh` → CN, Monday).
 */
fun weekStart(locale: Locale): DayOfWeek = regionWeekStart(locale.country.ifEmpty { likelyRegion(locale) })

fun regionWeekStart(region: String): DayOfWeek = when (region.uppercase(Locale.ROOT)) {
    in SUNDAY_FIRST -> DayOfWeek.SUNDAY
    in SATURDAY_FIRST -> DayOfWeek.SATURDAY
    else -> DayOfWeek.MONDAY
}

/** The region a language is most often written in, for the app's languages and their neighbors; "" for the rest (Monday). */
private fun likelyRegion(locale: Locale): String = when (locale.language) {
    "zh" -> if (locale.script == "Hant") "TW" else "CN"
    "ja" -> "JP"
    "en" -> "US"
    "ko" -> "KR"
    else -> ""
}

/** Regions whose weeks start on Sunday or Saturday (CLDR's week data, as web/src/lib/todo-calendar.ts lists them). */
private val SUNDAY_FIRST = ("AG AS BD BR BS BT BW BZ CA CO DM DO ET GT GU HK HN ID IL IN JM JP KE KH KR LA MH MM MO MT MX " +
    "MZ NI NP PA PE PH PK PR PT PY SA SG SV TH TT TW UM US VE VI WS YE ZA ZW").split(" ").toSet()
private val SATURDAY_FIRST = "AE AF BH DJ DZ EG IQ IR JO KW LY OM QA SD SY".split(" ").toSet()
