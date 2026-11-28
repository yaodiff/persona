package persona.feature.agents

import android.content.Context
import android.icu.text.DateIntervalFormat
import android.icu.util.DateInterval
import android.text.format.DateFormat
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.gestures.detectDragGesturesAfterLongPress
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.snapshotFlow
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.collectLatest
import persona.core.designsystem.JTextField
import persona.core.domain.tasks.CalendarMorning
import kotlinx.coroutines.flow.drop
import persona.core.contracts.TodoDue
import persona.core.designsystem.Haptic
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JDatePickerDialog
import persona.core.designsystem.JRow
import persona.core.designsystem.JSwitchRow
import persona.core.designsystem.rowPadding
import persona.core.designsystem.JTimePickerDialog
import persona.core.designsystem.rememberHaptics
import persona.core.domain.tasks.DefaultDueTime
import persona.core.domain.tasks.dropOnDay
import persona.core.domain.tasks.edgeScroll
import persona.core.domain.tasks.monthDropIndex
import java.time.LocalTime
import kotlin.math.roundToInt
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.testTag
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.contracts.TodoState
import persona.core.designsystem.CalendarTokens
import persona.core.designsystem.JAction
import persona.core.designsystem.JIcon
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JMenu
import persona.core.designsystem.JSegment
import persona.core.designsystem.JSegmented
import persona.core.designsystem.JSheet
import persona.core.designsystem.JTag
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaPalette
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.StatusColors
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.tasks.CalendarEntry
import persona.core.domain.tasks.CalendarMode
import persona.core.domain.tasks.calendarDays
import persona.core.domain.tasks.stepAnchor
import persona.core.domain.tasks.weekOf
import persona.core.domain.tasks.calendarEntries
import persona.core.domain.tasks.fitDay
import persona.core.domain.tasks.undatedCount
import persona.core.domain.tasks.weekStart
import java.time.Instant
import java.text.FieldPosition
import java.time.LocalDate
import java.time.LocalDateTime
import java.time.YearMonth
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.TextStyle
import java.util.Locale
import kotlin.math.abs

// The to-dos as a calendar (#2046, #4200, docs/assistant/todos.md "Calendar"): a day, a week or a month of the to-dos
// with a due date, drawn as the list draws them (state, overdue, done, priority, reminder). The day and the week put them
// on a time axis (TodoCalendarTimeline.kt); tapping a day of the month lists its to-dos; those without a due date are
// counted under the calendar, a tap from the list. A to-do is moved to another day or moment by dragging it, or by
// TalkBack's "Change Due Date…" (#2045). iOS TodoCalendarView / TodoCalendarGrids (no drag yet), web TodoCalendar.

/** The to-dos page's two views: the list, or the calendar of those with a due date. */
internal enum class TodosView { List, Calendar }

/** The view, the calendar's span and "Show done" this device keeps for the to-dos page (iOS `todos.calendar.*`). */
internal class TodoCalendarPrefs(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences("todos_calendar", Context.MODE_PRIVATE)

    var view: TodosView
        get() = if (prefs.getString("view", null) == "calendar") TodosView.Calendar else TodosView.List
        set(value) = prefs.edit().putString("view", if (value == TodosView.Calendar) "calendar" else "list").apply()

    /** Day, Week or Month (#4200); the month, as the calendar first was, until another is picked. */
    var mode: CalendarMode
        get() = CalendarMode.entries.firstOrNull { it.name.lowercase(Locale.ROOT) == prefs.getString("mode", null) } ?: CalendarMode.Month
        set(value) = prefs.edit().putString("mode", value.name.lowercase(Locale.ROOT)).apply()

    var showDone: Boolean
        get() = prefs.getBoolean("show_done", true)
        set(value) = prefs.edit().putBoolean("show_done", value).apply()
}

/**
 * The top of the to-dos page: List | Calendar, then the list's [listControls] (#3740), or in the calendar Day | Week |
 * Month (#4200) and its options ("Show done"). A phone too narrow for both choices in a row puts Day | Week | Month on
 * the next line.
 */
@Composable
internal fun TodosViewBar(
    view: TodosView,
    onView: (TodosView) -> Unit,
    showDone: Boolean,
    onShowDone: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
    mode: CalendarMode = CalendarMode.Month,
    onMode: (CalendarMode) -> Unit = {},
    listControls: (@Composable () -> Unit)? = null,
) {
    val views = @Composable {
        JSegmented(
            listOf(JSegment(stringResource(R.string.agents_todos_view_list)), JSegment(stringResource(R.string.agents_todos_view_calendar))),
            selected = view.ordinal,
            onSelect = { onView(TodosView.entries[it]) },
            modifier = Modifier.testTag("todos-view"),
        )
    }
    if (view == TodosView.List) {
        Row(modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            views()
            Spacer(Modifier.weight(1f))
            listControls?.invoke()
        }
        return
    }
    val gap = Tokens.Space.m
    val lineGap = Tokens.Space.s
    Layout(
        content = {
            views()
            JSegmented(
                listOf(
                    JSegment(stringResource(R.string.agents_todo_calendar_mode_day)),
                    JSegment(stringResource(R.string.agents_todo_calendar_mode_week)),
                    JSegment(stringResource(R.string.agents_todo_calendar_mode_month)),
                ),
                selected = mode.ordinal,
                onSelect = { onMode(CalendarMode.entries[it]) },
                modifier = Modifier.testTag("todo-calendar-mode"),
            )
            var open by remember { mutableStateOf(false) }
            Box {
                JIconButton(PhIcons.DotsThree, stringResource(R.string.agents_todo_calendar_options), { open = true },
                    modifier = Modifier.testTag("todo-calendar-options"))
                JMenu(open, { open = false }, listOf(
                    JAction(stringResource(R.string.agents_todo_calendar_show_done), checked = showDone) {
                        open = false
                        onShowDone(!showDone)
                    },
                ))
            }
        },
        modifier = modifier.fillMaxWidth(),
    ) { measurables, constraints ->
        val loose = constraints.copy(minWidth = 0, minHeight = 0)
        val (a, b, o) = measurables.map { it.measure(loose) }
        val width = constraints.maxWidth
        val space = gap.roundToPx()
        if (a.width + b.width + o.width + 2 * space <= width) {
            val height = maxOf(a.height, b.height, o.height)
            layout(width, height) {
                a.placeRelative(0, (height - a.height) / 2)
                b.placeRelative(width - o.width - space - b.width, (height - b.height) / 2)
                o.placeRelative(width - o.width, (height - o.height) / 2)
            }
        } else {
            val top = maxOf(a.height, o.height)
            val below = top + lineGap.roundToPx()
            layout(width, below + b.height) {
                a.placeRelative(0, (top - a.height) / 2)
                o.placeRelative(width - o.width, (top - o.height) / 2)
                b.placeRelative(0, below)
            }
        }
    }
}

/** Where a new to-do made on the calendar is due (#4200): at [time] on [day], or all day there when it is null (iOS `TodoCalendarSlot`). */
internal data class CalendarSlot(val day: LocalDate, val time: LocalTime?) {
    /** Kept across a configuration change: "2026-11-12", or "2026-11-12T14:30". */
    fun saved(): String = time?.let { day.atTime(it).toString() } ?: day.toString()

    companion object {
        fun parse(saved: String): CalendarSlot =
            if ('T' in saved) LocalDateTime.parse(saved).let { CalendarSlot(it.toLocalDate(), it.toLocalTime()) }
            else CalendarSlot(LocalDate.parse(saved), null)
    }
}

/**
 * The calendar (#4200): the span's title with Today, previous and next (a sideways swipe turns it too), then [mode]'s
 * page around [anchor]: the day (its week's strip over its time axis), the week (seven columns on one axis), or the
 * month. "N without a due date" is the page's bottom bar ([TodoCalendarUndatedBar]), so the glass page's soft bottom
 * edge never covers it.
 *
 * With [onMove], a to-do is picked up by a long press (the system's long-press time) and dropped on another day or
 * moment (#2045): it fades where it was, follows the finger with where it would land, the place under it is outlined,
 * and near the edges of a page taller than its space the page scrolls. TalkBack and Switch Access move one through
 * "Change Due Date…" instead. With [onAdd], a tap on the empty axis of the day or the week offers a new to-do due there.
 */
@Composable
internal fun TodoCalendar(
    mode: CalendarMode,
    onMode: (CalendarMode) -> Unit,
    anchor: LocalDate,
    onAnchor: (LocalDate) -> Unit,
    todos: List<Todo>,
    sessionLookup: (SessionKey) -> Session?,
    showDone: Boolean,
    now: Instant,
    onOpenTodo: (String) -> Unit,
    /** The page's own bottom inset (its bottom bar: "N without a due date"), under the calendar's last line. */
    bottomPadding: Dp,
    modifier: Modifier = Modifier,
    /** A to-do dropped on another day or moment, or given a due date through "Change Due Date…" (#2045); null: none moves. */
    onMove: ((String, TodoDue) -> Unit)? = null,
    /** A tap on the empty axis: a new to-do due there (#4200); null: none is made here. */
    onAdd: ((CalendarSlot) -> Unit)? = null,
) {
    val zone = ZoneId.systemDefault()
    val locale = LocalConfiguration.current.locales[0]
    val first = remember(locale) { weekStart(locale) }
    val today = now.atZone(zone).toLocalDate()
    val days = remember(mode, anchor, first) { calendarDays(mode, anchor, first) }
    // the day view's strip shows the whole week's dots
    val span = remember(mode, days, anchor, first) { if (mode == CalendarMode.Day) weekOf(anchor, first) else days }
    val entries = remember(todos, sessionLookup, showDone, span, now) {
        calendarEntries(todos, sessionLookup, showDone, span.first(), span.last().plusDays(1), now, zone)
    }
    val step = rememberUpdatedState { n: Int -> onAnchor(stepAnchor(anchor, mode, n)) }
    var picked by rememberSaveable { mutableStateOf<String?>(null) }
    var rescheduling by rememberSaveable { mutableStateOf<String?>(null) }
    val reschedule: ((String) -> Unit)? = if (onMove != null) { id -> rescheduling = id } else null

    Column(modifier) {
        CalendarHeader(mode, days, locale, onToday = { onAnchor(today) }, onStep = { step.value(it) })
        val swipe = CalendarTokens.pageSwipe
        Box(
            Modifier.weight(1f).fillMaxWidth().pointerInput(Unit) {
                var dx = 0f
                detectHorizontalDragGestures(onDragStart = { dx = 0f }, onDragEnd = {
                    if (abs(dx) > swipe.toPx()) step.value(if (dx < 0) 1 else -1)
                }) { _, delta -> dx += delta }
            },
        ) {
            when (mode) {
                CalendarMode.Month -> TodoCalendarMonth(
                    month = YearMonth.from(anchor),
                    days = days,
                    entries = entries,
                    today = today,
                    now = now,
                    onPick = { picked = it.toString() },
                    onMove = onMove,
                    onReschedule = reschedule,
                )
                CalendarMode.Day -> Column(Modifier.fillMaxSize()) {
                    TodoCalendarWeekStrip(span, anchor, today, entries, onPick = onAnchor)
                    // a page of its own for each day, so a new day starts at its own first hour
                    key(anchor) {
                        TodoCalendarTimeline(days, entries, narrow = false, showsDayHeader = false, today = today, now = now,
                            onOpen = onOpenTodo, onPickDay = onAnchor, modifier = Modifier.weight(1f), onAdd = onAdd,
                            onMove = onMove, onReschedule = reschedule)
                    }
                }
                CalendarMode.Week -> BoxWithConstraints(Modifier.fillMaxSize()) {
                    key(days.first()) {
                        TodoCalendarTimeline(days, entries, narrow = maxWidth < CalendarTokens.wide, showsDayHeader = true,
                            today = today, now = now, onOpen = onOpenTodo,
                            onPickDay = { day -> onAnchor(day); onMode(CalendarMode.Day) },
                            onAdd = onAdd, onMove = onMove, onReschedule = reschedule)
                    }
                }
            }
        }
        Spacer(Modifier.height(bottomPadding))
    }

    picked?.let { LocalDate.parse(it) }?.let { day ->
        TodoCalendarDaySheet(day, entries[day].orEmpty(), locale, onDismiss = { picked = null }, onOpen = { id ->
            picked = null
            onOpenTodo(id)
        }, onReschedule = if (onMove != null) { id -> picked = null; rescheduling = id } else null, onShowDay = {
            picked = null
            onAnchor(day)
            onMode(CalendarMode.Day)
        })
    }
    rescheduling?.let { id -> todos.firstOrNull { it.id == id } }?.let { todo ->
        TodoRescheduleSheet(todo, zone, locale, onDismiss = { rescheduling = null }, onSave = { due ->
            rescheduling = null
            onMove?.invoke(todo.id, due)
        })
    }
}

/** The span's title ("Thu, Nov 12", "Nov 9 – 15", "November 2026"), Today, previous and next, as iOS `TodoCalendarView.header`. */
@Composable
private fun CalendarHeader(mode: CalendarMode, days: List<LocalDate>, locale: Locale, onToday: () -> Unit, onStep: (Int) -> Unit) {
    val title = remember(mode, days, locale) { calendarTitle(mode, days, locale) }
    val (previous, next) = when (mode) {
        CalendarMode.Day -> R.string.agents_todo_calendar_previous_day to R.string.agents_todo_calendar_next_day
        CalendarMode.Week -> R.string.agents_todo_calendar_previous_week to R.string.agents_todo_calendar_next_week
        CalendarMode.Month -> R.string.agents_todo_calendar_previous to R.string.agents_todo_calendar_next
    }
    Row(
        Modifier.fillMaxWidth().padding(start = Tokens.Space.l, end = Tokens.Space.s, top = Tokens.Space.s, bottom = Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        Text(title, style = PersonaTheme.type.title, color = PersonaTheme.palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f).semantics { heading() }.testTag("todo-calendar-title"))
        JTextButton(stringResource(R.string.agents_todo_calendar_today), onToday)
        JIconButton(PhIcons.CaretLeft, stringResource(previous), { onStep(-1) })
        JIconButton(PhIcons.CaretRight, stringResource(next), { onStep(1) })
    }
}

/** The title of [days] shown in [mode]: the day ("Thu, Nov 12"), the week's first and last ("Nov 9 – 15"), or the month ("November 2026"). */
private fun calendarTitle(mode: CalendarMode, days: List<LocalDate>, locale: Locale): String = when (mode) {
    CalendarMode.Day -> DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "MMMdEEE"), locale).format(days.first())
    CalendarMode.Week -> {
        val zone = ZoneId.systemDefault()
        val millis = { day: LocalDate -> day.atTime(LocalTime.NOON).atZone(zone).toInstant().toEpochMilli() }
        DateIntervalFormat.getInstance("MMMd", locale)
            .format(DateInterval(millis(days.first()), millis(days.last())), StringBuffer(), FieldPosition(0)).toString()
    }
    CalendarMode.Month -> DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "yMMMM"), locale)
        .format(YearMonth.from(days[days.size / 2]))
}

/**
 * The month: the weekdays from the one weeks start on, and a day's to-dos in up to [CalendarTokens.monthLines] lines;
 * a tap on a day lists them ([onPick]). With [onMove] its to-dos are dragged to another day, at the same time there.
 */
@Composable
private fun TodoCalendarMonth(
    month: YearMonth,
    days: List<LocalDate>,
    entries: Map<LocalDate, List<CalendarEntry>>,
    today: LocalDate,
    now: Instant,
    onPick: (LocalDate) -> Unit,
    onMove: ((String, TodoDue) -> Unit)?,
    onReschedule: ((String) -> Unit)?,
) {
    val zone = ZoneId.systemDefault()
    val locale = LocalConfiguration.current.locales[0]
    var drag by remember { mutableStateOf<CalendarDrag?>(null) }
    val viewport = remember { Placed() }
    val haptics = rememberHaptics()

    Column(Modifier.fillMaxSize()) {
        val names = remember(days, locale) { days.take(7).map { it.dayOfWeek.getDisplayName(TextStyle.NARROW, locale) } }
        Row(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xs)) {
            for (name in names) {
                Text(name, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1, textAlign = TextAlign.Center,
                    modifier = Modifier.weight(1f))
            }
        }
        val line = PersonaTheme.palette.line
        BoxWithConstraints(Modifier.weight(1f).fillMaxWidth().onGloballyPositioned { viewport.coordinates = it }) {
            val rows = (days.size / 7).coerceAtLeast(1)
            val cell = maxOf(CalendarTokens.monthCell, maxHeight / rows)
            val scrolls = cell * rows > maxHeight
            val scroll = rememberScrollState()
            val density = LocalDensity.current
            val rtl = LocalLayoutDirection.current == LayoutDirection.Rtl
            val width = constraints.maxWidth.toFloat()
            val height = constraints.maxHeight.toFloat()
            val cellPx = with(density) { cell.toPx() }
            // The day under the finger, the month scrolled or not.
            val dayUnder = { d: CalendarDrag -> monthDropIndex(d.finger.x, d.finger.y + scroll.value, width, cellPx, rows, rtl)?.let(days::getOrNull) }
            val target by remember(drag, days, width, cellPx, rows, rtl) { derivedStateOf { drag?.let(dayUnder) } }
            LaunchedEffect(drag) {
                if (drag == null) return@LaunchedEffect
                // A tick at each new day, as iOS (Haptics.selection)
                snapshotFlow { target }.drop(1).collect { if (it != null) haptics(Haptic.Selection) }
            }
            if (scrolls) LaunchedEffect(drag) {
                val d = drag ?: return@LaunchedEffect
                val edge = with(density) { CalendarTokens.dragEdge.toPx() }
                val by = with(density) { CalendarTokens.dragScroll.toPx() }
                snapshotFlow { edgeScroll(d.finger.y, height, edge) }.collectLatest { way ->
                    while (way != 0) {
                        withFrameNanos { }
                        if (scroll.scrollBy(way * by) == 0f) break
                    }
                }
            }
            val moves = onMove?.let { move ->
                EntryMoves(
                    lift = { entry, chip, at ->
                        viewport.coordinates?.takeIf { it.isAttached && chip.isAttached }?.let { grid ->
                            drag = CalendarDrag(entry, at, chip.size, grid.localPositionOf(chip, at))
                            haptics(Haptic.Tap)
                        }
                    },
                    carry = { chip, at ->
                        val d = drag
                        val grid = viewport.coordinates
                        if (d != null && grid != null && grid.isAttached && chip.isAttached) d.finger = grid.localPositionOf(chip, at)
                    },
                    drop = {
                        val d = drag
                        drag = null
                        val day = d?.let(dayUnder)
                        if (d != null && day != null) move(d.entry.todoId, d.entry.dropOnDay(day, zone))
                    },
                    cancel = { drag = null },
                )
            }
            Column(Modifier.fillMaxWidth().then(if (scrolls) Modifier.verticalScroll(scroll) else Modifier)) {
                for (r in 0 until rows) {
                    Row(Modifier.fillMaxWidth().height(cell).drawBehind { drawLine(line, Offset.Zero, Offset(size.width, 0f), Stroke.HairlineWidth) }) {
                        for (c in 0 until 7) {
                            val day = days[r * 7 + c]
                            MonthDay(
                                day = day,
                                inMonth = YearMonth.from(day) == month,
                                today = day == today,
                                entries = entries[day].orEmpty(),
                                locale = locale,
                                onPick = { onPick(day) },
                                modifier = Modifier.weight(1f).fillMaxHeight().drawBehind {
                                    if (c > 0) drawLine(line, Offset.Zero, Offset(0f, size.height), Stroke.HairlineWidth)
                                },
                                lifted = drag?.entry?.todoId,
                                dropTarget = drag != null && target == day,
                                moves = moves,
                                onReschedule = onReschedule,
                            )
                        }
                    }
                }
            }
            drag?.let { d -> CalendarDragGhost(d, target?.let { dueWords(d.entry.dropOnDay(it, zone), locale, zone, now) }) }
        }
    }
}

/** Where a laid-out part is, for gestures to read (never drawn, so not state). */
internal class Placed { var coordinates: LayoutCoordinates? = null }

/** A to-do being carried (#2045): which, where in its chip it was picked up, the chip's size, and the finger in the page's part in sight. */
@Stable
internal class CalendarDrag(val entry: CalendarEntry, val grab: Offset, val size: IntSize, finger: Offset) {
    var finger by mutableStateOf(finger)
}

/** What a to-do drawn in a day does when it is picked up, carried and let go: from its chip's coordinates and a point in them. */
internal class EntryMoves(
    val lift: (CalendarEntry, LayoutCoordinates, Offset) -> Unit,
    val carry: (LayoutCoordinates, Offset) -> Unit,
    val drop: () -> Unit,
    val cancel: () -> Unit,
)

/** The carried to-do under the finger, its chip or block lifted off the page, and above it where it would land ("Thu, Nov 19, 18:00"). */
@Composable
internal fun CalendarDragGhost(drag: CalendarDrag, landingWords: String?) {
    val palette = PersonaTheme.palette
    val density = LocalDensity.current
    val gap = Tokens.Space.xs
    val chip = RoundedCornerShape(CalendarTokens.chipRadius)
    val label = RoundedCornerShape(Tokens.Radius.control)
    Layout(
        content = {
            Row(
                Modifier.size(with(density) { drag.size.width.toDp() }, with(density) { drag.size.height.toDp() })
                    .clip(chip).background(palette.surfaceOpaque).border(CalendarTokens.liftLine, palette.accent, chip)
                    .testTag("todo-calendar-dragging"),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Box(Modifier.width(CalendarTokens.edge).fillMaxHeight().background(palette.entryColors(drag.entry).dot))
                Text(
                    drag.entry.title,
                    style = PersonaTheme.type.micro,
                    color = if (drag.entry.overdue) palette.danger.text else palette.ink,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(start = Tokens.Space.xs, end = Tokens.Space.xxs),
                )
            }
            if (landingWords != null) {
                Text(
                    landingWords,
                    style = PersonaTheme.type.caption,
                    color = palette.ink,
                    maxLines = 1,
                    modifier = Modifier.clip(label).background(palette.surfaceOpaque).border(CalendarTokens.liftLine, palette.line, label)
                        .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xxs).testTag("todo-calendar-landing"),
                )
            }
        },
        modifier = Modifier.fillMaxSize(),
    ) { measurables, constraints ->
        val loose = constraints.copy(minWidth = 0, minHeight = 0)
        val carried = measurables[0].measure(loose)
        val landing = measurables.getOrNull(1)?.measure(loose)
        layout(constraints.maxWidth, constraints.maxHeight) {
            // read here, so a move only places them again
            val x = (drag.finger.x - drag.grab.x).roundToInt()
            val y = (drag.finger.y - drag.grab.y).roundToInt()
            carried.place(x, y)
            landing?.let {
                val left = (x + carried.width / 2 - it.width / 2).coerceIn(0, (constraints.maxWidth - it.width).coerceAtLeast(0))
                it.place(left, (y - gap.roundToPx() - it.height).coerceAtLeast(0))
            }
        }
    }
}

/**
 * A day of the month: its date (today in a circle), its to-dos, "+N" for those that do not fit; a tap lists them all.
 * For TalkBack it is one element, with "Change Due Date: <title>…" for each of its to-dos when they can move ([onReschedule]).
 */
@Composable
private fun MonthDay(
    day: LocalDate,
    inMonth: Boolean,
    today: Boolean,
    entries: List<CalendarEntry>,
    locale: Locale,
    onPick: () -> Unit,
    modifier: Modifier = Modifier,
    /** The to-do being carried, faded here while it is. */
    lifted: String? = null,
    /** It would land here: outlined in the accent. */
    dropTarget: Boolean = false,
    moves: EntryMoves? = null,
    onReschedule: ((String) -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val res = agentResources()
    val fit = fitDay(entries, CalendarTokens.monthLines)
    val spoken = remember(day, entries, locale) {
        (listOf(DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "yMMMMdEEEE"), locale).format(day)) +
            entries.map { it.title }).joinToString(", ")
    }
    val actions = if (onReschedule == null) emptyList() else entries.map { e ->
        CustomAccessibilityAction(res.getString(R.string.agents_todo_calendar_change_due_of, e.title)) { onReschedule(e.todoId); true }
    }
    val ring = RoundedCornerShape(CalendarTokens.chipRadius)
    Column(
        modifier.clickable(onClick = onPick)
            .clearAndSetSemantics {
                contentDescription = spoken
                role = Role.Button
                testTag = "todo-calendar-day-$day"
                onClick { onPick(); true }
                if (actions.isNotEmpty()) customActions = actions
            }
            .then(if (dropTarget) Modifier.background(palette.accentSoft, ring).border(CalendarTokens.dropRing, palette.accent, ring) else Modifier)
            .alpha(if (inMonth) 1f else CalendarTokens.outsideFade)
            .padding(horizontal = Tokens.Space.xxs, vertical = Tokens.Space.xs),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
    ) {
        Box(
            Modifier.align(Alignment.CenterHorizontally).size(CalendarTokens.dayMark).clip(CircleShape)
                .then(if (today) Modifier.background(palette.accent) else Modifier),
            contentAlignment = Alignment.Center,
        ) {
            Text(
                day.dayOfMonth.toString(),
                style = if (today) PersonaTheme.type.micro.copy(fontWeight = FontWeight.Bold) else PersonaTheme.type.micro,
                color = if (today) palette.onAccent else palette.ink,
                maxLines = 1,
            )
        }
        for (e in fit.shown) EntryChip(e, lifted = e.todoId == lifted, moves = moves)
        if (fit.more > 0) {
            Text(stringResource(R.string.agents_todo_calendar_more, fit.more), style = PersonaTheme.type.micro, color = palette.ink3,
                maxLines = 1, modifier = Modifier.padding(start = Tokens.Space.xs))
        }
    }
}

/**
 * A to-do in a row of its own (a day of the month, a day's All Day row): its title on a thin edge in its state's color,
 * red once overdue, faded and struck through when done. With [moves], a long press picks it up and the finger carries it
 * (#2045); moving first is the page's (a swipe turns it, a drag scrolls it). In the month a tap is the day's; with
 * [onOpen] (the All Day row) a tap opens it, and it is a button of its own for TalkBack, with "Change Due Date…".
 */
@Composable
internal fun EntryChip(
    entry: CalendarEntry,
    modifier: Modifier = Modifier,
    lifted: Boolean = false,
    moves: EntryMoves? = null,
    onOpen: (() -> Unit)? = null,
    onReschedule: (() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val placed = remember { Placed() }
    val current by rememberUpdatedState(entry)
    val handler by rememberUpdatedState(moves)
    val spoken = entrySpoken(entry)
    val changeDue = stringResource(R.string.agents_todo_calendar_change_due_action)
    Row(
        modifier.fillMaxWidth().height(IntrinsicSize.Min)
            .then(if (onOpen == null) Modifier else Modifier.clickable(onClick = onOpen).clearAndSetSemantics {
                contentDescription = spoken
                role = Role.Button
                testTag = "todo-calendar-entry-${entry.todoId}"
                onClick { onOpen(); true }
                if (onReschedule != null) customActions = listOf(CustomAccessibilityAction(changeDue) { onReschedule(); true })
            })
            .then(if (moves == null) Modifier else Modifier.onGloballyPositioned { placed.coordinates = it }.pointerInput(entry.todoId) {
                detectDragGesturesAfterLongPress(
                    onDragStart = { at -> placed.coordinates?.let { handler?.lift?.invoke(current, it, at) } },
                    onDrag = { change, _ ->
                        change.consume()
                        placed.coordinates?.let { handler?.carry?.invoke(it, change.position) }
                    },
                    onDragEnd = { handler?.drop?.invoke() },
                    onDragCancel = { handler?.cancel?.invoke() },
                )
            })
            .clip(RoundedCornerShape(CalendarTokens.chipRadius)).background(palette.sunken)
            .alpha(when {
                lifted -> CalendarTokens.liftedFade
                entry.done -> CalendarTokens.doneFade
                else -> 1f
            }),
    ) {
        Box(Modifier.width(CalendarTokens.edge).fillMaxHeight().background(palette.entryColors(entry).dot))
        Text(
            entry.title,
            style = PersonaTheme.type.micro.copy(textDecoration = if (entry.done) TextDecoration.LineThrough else null),
            color = if (entry.overdue) palette.danger.text else palette.ink,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(start = Tokens.Space.xs, end = Tokens.Space.xxs, top = Tokens.Space.xxs, bottom = Tokens.Space.xxs),
        )
    }
}

/** What TalkBack reads for a to-do on the calendar: its title, its state and when ("All day", "18:00", "Was due 11:30"), as iOS `CalendarEntryLook.spoken`. */
@Composable
internal fun entrySpoken(entry: CalendarEntry): String =
    listOf(entry.title, entry.restStatus?.label ?: entry.state.label, entryWhen(entry, LocalConfiguration.current.locales[0])).joinToString(", ")

/** When a to-do on the calendar is due, as its day's list says: "All day", "18:00", or "Was due 11:30" once past. */
@Composable
internal fun entryWhen(entry: CalendarEntry, locale: Locale): String {
    val time = entry.time?.let { DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "HHmm"), locale).format(it) }
    return when {
        time == null -> stringResource(R.string.agents_todo_calendar_all_day)
        entry.overdue -> stringResource(R.string.agents_todo_was_due, time)
        else -> time
    }
}

/** "N without a due date · See list": back to the list, where they are; nothing when every open to-do has a due date. */
@Composable
internal fun TodoCalendarUndatedBar(todos: List<Todo>, onShowList: () -> Unit, modifier: Modifier = Modifier) {
    val count = remember(todos) { undatedCount(todos) }
    if (count == 0) return
    val palette = PersonaTheme.palette
    val line = palette.line
    Row(
        modifier.fillMaxWidth()
            .drawBehind { drawLine(line, Offset.Zero, Offset(size.width, 0f), Stroke.HairlineWidth) }
            .clickable(onClick = onShowList)
            .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.m)
            .testTag("todo-calendar-undated"),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s, Alignment.CenterHorizontally),
    ) {
        JIcon(PhIcons.ListChecks, null, size = Tokens.Space.l, tint = palette.ink3)
        Text(pluralStringResource(R.plurals.agents_todo_calendar_undated, count, count), style = PersonaTheme.type.caption,
            color = palette.ink2, maxLines = 1)
        Text(stringResource(R.string.agents_todo_calendar_see_list), style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold),
            color = palette.accent, maxLines = 1)
    }
}

/**
 * A day's to-dos, all of them in the month's order: state, title, when, high priority and reminder; a tap opens one,
 * and TalkBack's "Change Due Date…" moves one ([onReschedule], #2045).
 */
@Composable
private fun TodoCalendarDaySheet(
    day: LocalDate,
    entries: List<CalendarEntry>,
    locale: Locale,
    onDismiss: () -> Unit,
    onOpen: (String) -> Unit,
    onReschedule: ((String) -> Unit)? = null,
    /** The day on its own time axis (#4200), as a tap on a day of iOS's month shows it. */
    onShowDay: (() -> Unit)? = null,
) {
    val title = remember(day, locale) { DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "MMMMdEEEE"), locale).format(day) }
    JSheet(onDismiss = onDismiss, title = title, scrollable = true) {
        Column(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.l).testTag("todo-calendar-day-sheet")) {
            if (onShowDay != null) {
                JTextButton(stringResource(R.string.agents_todo_calendar_show_day), onShowDay,
                    Modifier.align(Alignment.End).testTag("todo-calendar-show-day"))
            }
            if (entries.isEmpty()) {
                Text(stringResource(R.string.agents_todo_calendar_nothing_due), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink3,
                    modifier = Modifier.padding(vertical = Tokens.Space.m))
            }
            for (e in entries) DayEntryRow(e, locale, onReschedule?.let { { it(e.todoId) } }) { onOpen(e.todoId) }
        }
    }
}

@Composable
private fun DayEntryRow(entry: CalendarEntry, locale: Locale, onReschedule: (() -> Unit)?, onOpen: () -> Unit) {
    val palette = PersonaTheme.palette
    val changeDue = stringResource(R.string.agents_todo_calendar_change_due_action)
    val whenText = entryWhen(entry, locale)
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onOpen)
            .then(if (onReschedule == null) Modifier else Modifier.semantics {
                customActions = listOf(CustomAccessibilityAction(changeDue) { onReschedule(); true })
            })
            .alpha(if (entry.done) CalendarTokens.doneFade else 1f)
            .padding(vertical = Tokens.Space.m),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
    ) {
        Box(Modifier.size(CalendarTokens.dot).clip(CircleShape).background(palette.entryColors(entry).dot))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            Text(entry.title, style = PersonaTheme.type.body.copy(textDecoration = if (entry.done) TextDecoration.LineThrough else null),
                color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
            Text(whenText, style = PersonaTheme.type.caption, color = if (entry.overdue) palette.danger.text else palette.ink3, maxLines = 1)
        }
        if (entry.high) JIcon(PhIcons.Flag, stringResource(R.string.agents_todo_priority_high), size = Tokens.Space.l, tint = palette.cautionText)
        if (entry.reminding) JIcon(PhIcons.BellSimple, stringResource(R.string.agents_todo_calendar_reminder), size = Tokens.Space.l, tint = palette.ink3)
        entry.restStatus?.let { TodoRestStatusTag(it) } ?: JTag(entry.state.label)
    }
}

/**
 * A to-do's due date set without dragging it (#2045): TalkBack's and Switch Access's "Change Due Date…", as iOS
 * `TodoRescheduleSheet`. A day, All day, and the time when it is not (18:00 for an all-day one given a time); saved as a
 * drop is, with Undo.
 */
@Composable
private fun TodoRescheduleSheet(todo: Todo, zone: ZoneId, locale: Locale, onSave: (TodoDue) -> Unit, onDismiss: () -> Unit) {
    val start = todo.due
    var allDay by rememberSaveable(todo.id) { mutableStateOf(start is TodoDue.Day) }
    var day by rememberSaveable(todo.id) {
        mutableStateOf(when (start) {
            is TodoDue.At -> start.at.atZone(zone).toLocalDate()
            is TodoDue.Day -> start.day
            null -> LocalDate.now(zone)
        }.toString())
    }
    var time by rememberSaveable(todo.id) { mutableStateOf(((start as? TodoDue.At)?.at?.atZone(zone)?.toLocalTime() ?: DefaultDueTime).toString()) }
    var pickingDay by remember { mutableStateOf(false) }
    var pickingTime by remember { mutableStateOf(false) }
    val date = LocalDate.parse(day)
    val at = LocalTime.parse(time)
    val dayText = remember(date, locale) { DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "yMMMdEEE"), locale).format(date) }
    val timeText = remember(at, locale) { DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "HHmm"), locale).format(at) }
    val timeLabel = stringResource(R.string.agents_todo_calendar_time)
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.agents_todo_calendar_change_due)) {
        Column(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.l).testTag("todo-calendar-reschedule")) {
            Text(todo.title, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.padding(start = rowPadding, end = rowPadding, bottom = Tokens.Space.xs))
            JRow(stringResource(R.string.agents_todo_calendar_date), value = dayText, onClick = { pickingDay = true })
            JSwitchRow(stringResource(R.string.agents_todo_calendar_all_day), allDay, { allDay = it })
            if (!allDay) JRow(timeLabel, value = timeText, onClick = { pickingTime = true })
            Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.m), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JButton(stringResource(R.string.agents_todo_cancel), onDismiss, Modifier.weight(1f), kind = JButtonKind.Secondary)
                JButton(stringResource(R.string.agents_todo_save), {
                    onSave(if (allDay) TodoDue.Day(date) else TodoDue.At(date.atTime(at).atZone(zone).toInstant()))
                }, Modifier.weight(1f).testTag("todo-calendar-reschedule-save"))
            }
        }
    }
    if (pickingDay) JDatePickerDialog(date, { day = it.toString(); pickingDay = false }, { pickingDay = false })
    if (pickingTime) JTimePickerDialog(at, { time = it.toString(); pickingTime = false }, { pickingTime = false }, title = timeLabel)
}

/**
 * A new to-do due where the calendar was tapped (#4200), as iOS `TodoQuickAddSheet`: its title to type, the day and the
 * time already set from [slot] (all day on the All Day row), changeable here; the rest is set on the to-do's page. While
 * it saves the sheet stays; a failed save keeps what was typed and says why ([onFailed]).
 */
@Composable
internal fun TodoQuickAddSheet(
    slot: CalendarSlot,
    onAdd: suspend (String, TodoDue) -> Unit,
    onFailed: (Exception) -> Unit,
    onDismiss: () -> Unit,
) {
    val zone = ZoneId.systemDefault()
    val locale = LocalConfiguration.current.locales[0]
    var title by rememberSaveable(slot) { mutableStateOf("") }
    var allDay by rememberSaveable(slot) { mutableStateOf(slot.time == null) }
    var day by rememberSaveable(slot) { mutableStateOf(slot.day.toString()) }
    // an all-day slot given a time starts the hour after the morning's
    var time by rememberSaveable(slot) { mutableStateOf((slot.time ?: LocalTime.of(CalendarMorning + 1, 0)).toString()) }
    var saving by remember { mutableStateOf(false) }
    var pickingDay by remember { mutableStateOf(false) }
    var pickingTime by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val focus = remember { FocusRequester() }
    val date = LocalDate.parse(day)
    val at = LocalTime.parse(time)
    val dayText = remember(date, locale) { DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "yMMMdEEE"), locale).format(date) }
    val timeText = remember(at, locale) { DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "HHmm"), locale).format(at) }
    val timeLabel = stringResource(R.string.agents_todo_calendar_time)
    val canAdd = title.isNotBlank() && !saving
    val add = {
        if (canAdd) {
            saving = true
            val due = if (allDay) TodoDue.Day(date) else TodoDue.At(date.atTime(at).atZone(zone).toInstant())
            scope.launch {
                try {
                    onAdd(title.trim(), due)
                    onDismiss()
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    saving = false
                    onFailed(e)
                }
            }
        }
    }
    LaunchedEffect(Unit) { focus.requestFocus() }
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.agents_todo_new), dismissible = !saving) {
        Column(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.l).testTag("todo-calendar-new")) {
            JTextField(
                title,
                { title = it },
                modifier = Modifier.fillMaxWidth().padding(horizontal = rowPadding).focusRequester(focus).testTag("todo-calendar-new-title"),
                label = stringResource(R.string.agents_todo_title_label),
                placeholder = stringResource(R.string.agents_todo_title_placeholder),
                singleLine = true,
                enabled = !saving,
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Sentences, imeAction = ImeAction.Done),
                keyboardActions = KeyboardActions(onDone = { add() }),
            )
            Spacer(Modifier.height(Tokens.Space.s))
            JRow(stringResource(R.string.agents_todo_calendar_due), value = dayText, onClick = { pickingDay = true })
            JSwitchRow(stringResource(R.string.agents_todo_calendar_all_day), allDay, { allDay = it })
            if (!allDay) JRow(timeLabel, value = timeText, onClick = { pickingTime = true })
            Text(stringResource(R.string.agents_todo_calendar_add_hint), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3,
                modifier = Modifier.padding(start = rowPadding, end = rowPadding, top = Tokens.Space.xs))
            Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.m), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JButton(stringResource(R.string.agents_todo_cancel), onDismiss, Modifier.weight(1f), kind = JButtonKind.Secondary, enabled = !saving)
                JButton(stringResource(R.string.agents_todo_calendar_add), { add() }, Modifier.weight(1f).testTag("todo-calendar-new-add"),
                    enabled = canAdd, loading = saving)
            }
        }
    }
    if (pickingDay) JDatePickerDialog(date, { day = it.toString(); pickingDay = false }, { pickingDay = false })
    if (pickingTime) JTimePickerDialog(at, { time = it.toString(); pickingTime = false }, { pickingTime = false }, title = timeLabel)
}

/** An entry's colors, as the list draws its to-do: its state's, red once overdue (what is late is what to see first). */
internal fun PersonaPalette.entryColors(entry: CalendarEntry): StatusColors = when {
    entry.overdue -> danger
    entry.state == TodoState.NeedsYou -> status(Status.NeedsYou)
    entry.state == TodoState.Unread -> status(Status.Unread)
    entry.state == TodoState.Working -> status(Status.Working)
    else -> status(Status.Idle)
}
