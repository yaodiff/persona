package persona.feature.agents

import android.text.format.DateFormat
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectDragGesturesAfterLongPress
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.scrollBy
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
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.testTag
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.LayoutDirection
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.drop
import persona.core.contracts.TodoDue
import persona.core.designsystem.CalendarTokens
import persona.core.designsystem.Haptic
import persona.core.designsystem.JIcon
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.rememberHaptics
import persona.core.domain.tasks.AxisTarget
import persona.core.domain.tasks.CalendarEntry
import persona.core.domain.tasks.DayFit
import persona.core.domain.tasks.NowLabelRoom
import persona.core.domain.tasks.axisDropTarget
import persona.core.domain.tasks.calendarLanes
import persona.core.domain.tasks.dropOn
import persona.core.domain.tasks.edgeScroll
import persona.core.domain.tasks.firstHour
import persona.core.domain.tasks.fitDay
import persona.core.domain.tasks.minutes
import persona.core.domain.tasks.slotTime
import java.time.Instant
import java.time.LocalDate
import java.time.LocalTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.TextStyle
import java.util.Locale
import kotlin.math.abs
import kotlin.math.roundToInt

// The day's and the week's time axis (#4200, docs/assistant/todos.md "Calendar"): an All Day row, then the day's hours
// with each timed to-do a block at its moment, those that overlap side by side; the day view has its week's strip above
// it. A block is dragged to another moment or day (its top edge snaps to the quarter hour) or onto a day's All Day row;
// a tap on the empty axis offers a new to-do due there. iOS TodoCalendarGrids (TodoCalendarTimeline /
// TodoCalendarWeekStrip) and TodoCalendarView (TodoQuickAddSheet); iOS doesn't drag yet, so dragging follows the
// web, web/src/lib/todo-calendar.ts (dropOnAxis / dropOnDay).

/**
 * The day view's week (#4200): each day's narrow name, its date (the day shown in the accent, today in the accent's
 * color) and a dot for each of its open to-dos, up to [CalendarTokens.stripDots]; a tap shows that day.
 */
@Composable
internal fun TodoCalendarWeekStrip(
    days: List<LocalDate>,
    anchor: LocalDate,
    today: LocalDate,
    entries: Map<LocalDate, List<CalendarEntry>>,
    onPick: (LocalDate) -> Unit,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val locale = LocalConfiguration.current.locales[0]
    val res = agentResources()
    val mark = CalendarTokens.stripMark * textScale()
    Row(modifier.fillMaxWidth().padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xxs).testTag("todo-calendar-strip")) {
        for (day in days) {
            val picked = day == anchor
            val open = entries[day].orEmpty().filter { !it.done }
            val spoken = remember(day, open.size, locale) {
                DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "MMMMdEEEE"), locale).format(day) + ", " +
                    res.getQuantityString(R.plurals.agents_todo_calendar_open, open.size, open.size)
            }
            Column(
                Modifier.weight(1f).clip(RoundedCornerShape(Tokens.Radius.control)).clickable { onPick(day) }
                    .clearAndSetSemantics {
                        contentDescription = spoken
                        selected = picked
                        role = Role.Tab
                        testTag = "todo-calendar-strip-$day"
                        onClick { onPick(day); true }
                    }
                    .padding(vertical = Tokens.Space.xxs),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
            ) {
                Text(day.dayOfWeek.getDisplayName(TextStyle.NARROW, locale), style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1)
                Box(
                    Modifier.size(mark).clip(CircleShape).then(if (picked) Modifier.background(palette.accent) else Modifier),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        day.dayOfMonth.toString(),
                        style = if (picked || day == today) PersonaTheme.type.bodyStrong else PersonaTheme.type.body,
                        color = when {
                            picked -> palette.onAccent
                            day == today -> palette.accent
                            else -> palette.ink
                        },
                        maxLines = 1,
                        softWrap = false,
                    )
                }
                Row(Modifier.height(CalendarTokens.stripDot), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                    for (e in open.take(CalendarTokens.stripDots)) {
                        Box(Modifier.size(CalendarTokens.stripDot).clip(CircleShape).background(palette.entryColors(e).dot))
                    }
                }
            }
        }
    }
}

/**
 * [days] on one time axis: the day view's one column, or the week's seven. A row of the days on top when
 * [showsDayHeader] (a tap shows that day, [onPickDay]), then the All Day row, then the 24 hours, scrolled first to the
 * morning's to-dos and now ([firstHour]). [narrow] (a phone's week) draws the blocks as their titles alone, the hours
 * without minutes, and a day's All Day row in [CalendarTokens.allDayLines] lines.
 *
 * With [onMove], a long press picks a to-do up: carried over the axis its block's top edge snaps to the quarter hour of
 * the day under the finger, over the All Day row it would be due that day, all day; near the axis' top or bottom edge
 * it scrolls. With [onAdd], a tap on the empty axis offers a new to-do due at the half hour there, on the All Day row
 * due that day.
 */
@Composable
internal fun TodoCalendarTimeline(
    days: List<LocalDate>,
    entries: Map<LocalDate, List<CalendarEntry>>,
    narrow: Boolean,
    showsDayHeader: Boolean,
    today: LocalDate,
    now: Instant,
    onOpen: (String) -> Unit,
    onPickDay: (LocalDate) -> Unit,
    modifier: Modifier = Modifier,
    onAdd: ((CalendarSlot) -> Unit)? = null,
    onMove: ((String, TodoDue) -> Unit)? = null,
    onReschedule: ((String) -> Unit)? = null,
) {
    val zone = ZoneId.systemDefault()
    val locale = LocalConfiguration.current.locales[0]
    val palette = PersonaTheme.palette
    val density = LocalDensity.current
    val rtl = LocalLayoutDirection.current == LayoutDirection.Rtl
    val haptics = rememberHaptics()
    val gutter = (if (narrow) CalendarTokens.gutterNarrow else CalendarTokens.gutter) * textScale()
    val hourPx = with(density) { CalendarTokens.hour.toPx() }
    // a block is as long as its height on the axis
    val blockMinutes = CalendarTokens.block / CalendarTokens.hour * 60f
    val allDay = remember(days, entries) { days.associateWith { d -> entries[d].orEmpty().filter { it.allDay } } }
    val timed = remember(days, entries) { days.associateWith { d -> entries[d].orEmpty().filter { !it.allDay } } }
    val lanes = remember(timed, blockMinutes) { timed.mapValues { (_, day) -> calendarLanes(day, blockMinutes) } }
    val nowHere = now.atZone(zone)
    val nowMinutes = if (nowHere.toLocalDate() == today && today in days) nowHere.toLocalTime().toSecondOfDay() / 60f else null

    val scroll = rememberScrollState()
    // First to the morning's to-dos and now; once more when the first timed to-dos arrive (the list loads after the page).
    val anyTimed = timed.values.any { it.isNotEmpty() }
    var aimedAtTodos by remember { mutableStateOf(false) }
    LaunchedEffect(anyTimed) {
        if (aimedAtTodos) return@LaunchedEffect
        aimedAtTodos = anyTimed
        // a quarter of an hour above it, so the hour's label is whole under the All Day row
        scroll.scrollTo(((firstHour(days, timed.values.flatten(), now, zone) - 0.25f) * hourPx).roundToInt().coerceAtLeast(0))
    }

    var drag by remember { mutableStateOf<CalendarDrag?>(null) }
    val viewport = remember { Placed() }
    val axisArea = remember { Placed() }
    val scrollBox = remember { Placed() }
    // Where the carried to-do would land: the day under the finger, on its All Day row above the axis, else where its
    // block's top edge is on the axis (the scrolled-away part counted).
    val targetOf = { d: CalendarDrag ->
        val page = viewport.coordinates
        val axis = axisArea.coordinates
        val box = scrollBox.coordinates
        if (page == null || axis == null || box == null || !page.isAttached || !axis.isAttached || !box.isAttached || d.finger.y < 0f) null
        else {
            val boxTop = page.localPositionOf(box, Offset.Zero).y
            val left = page.localPositionOf(axis, Offset.Zero).x
            axisDropTarget(d.finger.x - left, axis.size.width.toFloat(), days, rtl, onAllDay = d.finger.y < boxTop,
                blockTop = d.finger.y - boxTop + scroll.value - d.grab.y, hourHeight = hourPx)
        }
    }
    val target by remember(drag, days, rtl, hourPx) { derivedStateOf { drag?.let(targetOf) } }
    LaunchedEffect(drag) {
        if (drag == null) return@LaunchedEffect
        // a tick at each new place, as the month's at each new day
        snapshotFlow { target }.drop(1).collect { if (it != null) haptics(Haptic.Selection) }
    }
    LaunchedEffect(drag) {
        val d = drag ?: return@LaunchedEffect
        val edge = with(density) { CalendarTokens.dragEdge.toPx() }
        val by = with(density) { CalendarTokens.dragScroll.toPx() }
        snapshotFlow {
            val page = viewport.coordinates
            val box = scrollBox.coordinates
            if (page == null || box == null || !page.isAttached || !box.isAttached) 0
            else {
                // over the axis only: above it is the All Day row, a place to drop on
                val y = d.finger.y - page.localPositionOf(box, Offset.Zero).y
                if (y < 0f) 0 else edgeScroll(y, box.size.height.toFloat(), edge)
            }
        }.collectLatest { way ->
            while (way != 0) {
                withFrameNanos { }
                if (scroll.scrollBy(way * by) == 0f) break
            }
        }
    }
    val moves = onMove?.let { move ->
        EntryMoves(
            lift = { entry, chip, at ->
                viewport.coordinates?.takeIf { it.isAttached && chip.isAttached }?.let { page ->
                    drag = CalendarDrag(entry, at, chip.size, page.localPositionOf(chip, at))
                    haptics(Haptic.Tap)
                }
            },
            carry = { chip, at ->
                val d = drag
                val page = viewport.coordinates
                if (d != null && page != null && page.isAttached && chip.isAttached) d.finger = page.localPositionOf(chip, at)
            },
            drop = {
                val d = drag
                drag = null
                val landing = d?.let(targetOf)
                if (d != null && landing != null) move(d.entry.todoId, d.entry.dropOn(landing, zone))
            },
            cancel = { drag = null },
        )
    }
    val lifted = drag?.entry?.todoId

    Box(modifier.fillMaxSize().onGloballyPositioned { viewport.coordinates = it }) {
        Column(Modifier.fillMaxSize()) {
            if (showsDayHeader) DayHeader(days, today, narrow, gutter, locale, onPickDay)
            AllDayRow(
                days = days,
                allDay = allDay,
                narrow = narrow,
                gutter = gutter,
                target = target as? AxisTarget.AllDay,
                lifted = lifted,
                moves = moves,
                onOpen = onOpen,
                onReschedule = onReschedule,
                onPickDay = onPickDay,
                onAdd = onAdd,
            )
            Box(Modifier.fillMaxWidth().height(Tokens.Size.line).background(palette.line))
            Box(Modifier.weight(1f).fillMaxWidth().onGloballyPositioned { scrollBox.coordinates = it }.verticalScroll(scroll)) {
                Column(Modifier.fillMaxWidth()) {
                    Row(Modifier.fillMaxWidth().height(CalendarTokens.hour * 24)) {
                        HourGutter(gutter, narrow, locale, nowMinutes)
                        BoxWithConstraints(Modifier.weight(1f).fillMaxHeight().onGloballyPositioned { axisArea.coordinates = it }.testTag("todo-calendar-axis")) {
                            val line = palette.line
                            val columns = days.size
                            Box(Modifier.fillMaxSize().drawBehind {
                                for (h in 1 until 24) drawLine(line, Offset(0f, h * hourPx), Offset(size.width, h * hourPx), Stroke.HairlineWidth)
                                for (i in 1 until columns) {
                                    val x = i * size.width / columns
                                    drawLine(line, Offset(x, 0f), Offset(x, size.height), Stroke.HairlineWidth)
                                }
                            })
                            val column = maxWidth / columns
                            val inset = Tokens.Space.xxs
                            if (onAdd != null) {
                                for ((i, day) in days.withIndex()) {
                                    Box(
                                        Modifier.offset(x = column * i).width(column).fillMaxHeight()
                                            .pointerInput(day) { detectTapGestures { at -> onAdd(CalendarSlot(day, slotTime(at.y / hourPx * 60))) } }
                                            .testTag("todo-calendar-axis-$day"),
                                    )
                                }
                            }
                            (target as? AxisTarget.At)?.let { t ->
                                val i = days.indexOf(t.day)
                                if (i >= 0) {
                                    val ring = RoundedCornerShape(Tokens.Radius.badge)
                                    Box(
                                        Modifier.offset(x = column * i + inset, y = CalendarTokens.hour * (t.time.toSecondOfDay() / 3600f))
                                            .size(column - inset * 2, CalendarTokens.block - inset)
                                            .background(palette.accentSoft, ring).border(CalendarTokens.dropRing, palette.accent, ring)
                                            .testTag("todo-calendar-drop-at"),
                                    )
                                }
                            }
                            for ((i, day) in days.withIndex()) {
                                for (p in lanes[day].orEmpty()) key(p.entry.todoId) {
                                    val lane = (column - inset) / p.lanes
                                    val id = p.entry.todoId
                                    AxisBlock(
                                        entry = p.entry,
                                        narrow = narrow,
                                        lifted = id == lifted,
                                        moves = moves,
                                        onOpen = { onOpen(id) },
                                        onReschedule = onReschedule?.let { { it(id) } },
                                        modifier = Modifier
                                            .offset(x = column * i + inset + lane * p.lane, y = CalendarTokens.hour * (p.entry.minutes / 60f))
                                            .size(lane - inset, CalendarTokens.block - inset),
                                    )
                                }
                            }
                            if (nowMinutes != null) {
                                val i = days.indexOf(today)
                                val y = CalendarTokens.hour * (nowMinutes / 60f)
                                Box(
                                    Modifier.offset(x = column * i, y = y - Tokens.Size.lineMedium / 2).width(column).height(Tokens.Size.lineMedium)
                                        .background(palette.danger.dot).testTag("todo-calendar-now"),
                                )
                                Box(
                                    Modifier.offset(x = column * i - CalendarTokens.nowDot / 2, y = y - CalendarTokens.nowDot / 2)
                                        .size(CalendarTokens.nowDot).clip(CircleShape).background(palette.danger.dot),
                                )
                            }
                        }
                    }
                    // room under midnight for a block due at 23:45
                    Spacer(Modifier.height(CalendarTokens.block - CalendarTokens.hour / 4))
                }
            }
        }
        drag?.let { d -> CalendarDragGhost(d, target?.let { dueWords(d.entry.dropOn(it, zone), locale, zone, now) }) }
    }
}

/** How much the hours' column and the dates' circles grow with the text: as it does, up to [CalendarTokens.textScaleMaximum]. */
@Composable
private fun textScale(): Float = LocalDensity.current.fontScale.coerceIn(1f, CalendarTokens.textScaleMaximum)

/** The week's days over their columns: the name and the date, today's in the accent; a tap shows that day. */
@Composable
private fun DayHeader(days: List<LocalDate>, today: LocalDate, narrow: Boolean, gutter: Dp, locale: Locale, onPick: (LocalDate) -> Unit) {
    val palette = PersonaTheme.palette
    val mark = Tokens.Size.wellXS * textScale()
    Row(Modifier.fillMaxWidth().padding(vertical = Tokens.Space.xxs), verticalAlignment = Alignment.CenterVertically) {
        Spacer(Modifier.width(gutter))
        for (day in days) {
            val isToday = day == today
            val spoken = remember(day, locale) { DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "MMMMdEEEE"), locale).format(day) }
            val name = day.dayOfWeek.getDisplayName(if (narrow) TextStyle.NARROW else TextStyle.SHORT, locale)
            val date = @Composable {
                Box(
                    Modifier.size(mark).clip(CircleShape).then(if (isToday) Modifier.background(palette.accent) else Modifier),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(day.dayOfMonth.toString(), style = PersonaTheme.type.labelSmall, color = if (isToday) palette.onAccent else palette.ink, maxLines = 1, softWrap = false)
                }
            }
            val cell = Modifier.weight(1f).clip(RoundedCornerShape(Tokens.Radius.control)).clickable { onPick(day) }
                .clearAndSetSemantics {
                    contentDescription = spoken
                    role = Role.Button
                    testTag = "todo-calendar-day-header-$day"
                    onClick { onPick(day); true }
                }
            if (narrow) {
                Column(cell, horizontalAlignment = Alignment.CenterHorizontally) {
                    Text(name, style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1)
                    date()
                }
            } else {
                Row(cell, verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs, Alignment.CenterHorizontally)) {
                    Text(name, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1)
                    date()
                }
            }
        }
    }
}

/**
 * The days' all-day to-dos over their columns, "All day" in the hours' column: each a chip that opens it (a button
 * of its own for TalkBack, with "Change Due Date…"); a tap on a day's empty part offers a new to-do due that day.
 */
@Composable
private fun AllDayRow(
    days: List<LocalDate>,
    allDay: Map<LocalDate, List<CalendarEntry>>,
    narrow: Boolean,
    gutter: Dp,
    target: AxisTarget.AllDay?,
    lifted: String?,
    moves: EntryMoves?,
    onOpen: (String) -> Unit,
    onReschedule: ((String) -> Unit)?,
    onPickDay: (LocalDate) -> Unit,
    onAdd: ((CalendarSlot) -> Unit)?,
) {
    val palette = PersonaTheme.palette
    val line = palette.line
    Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min)) {
        Text(
            stringResource(R.string.agents_todo_calendar_all_day),
            style = PersonaTheme.type.micro,
            color = palette.ink3,
            maxLines = 2,
            textAlign = TextAlign.End,
            modifier = Modifier.width(gutter).padding(end = Tokens.Space.sm, top = Tokens.Space.xs, bottom = Tokens.Space.xs),
        )
        for (day in days) {
            val ring = RoundedCornerShape(CalendarTokens.chipRadius)
            val all = allDay[day].orEmpty()
            val fit = if (narrow) fitDay(all, CalendarTokens.allDayLines) else DayFit(all, 0)
            Column(
                Modifier.weight(1f).fillMaxHeight().heightIn(min = CalendarTokens.allDayRow)
                    .drawBehind {
                        val x = if (layoutDirection == LayoutDirection.Rtl) size.width else 0f
                        drawLine(line, Offset(x, 0f), Offset(x, size.height), Stroke.HairlineWidth)
                    }
                    .then(if (target?.day == day) Modifier.background(palette.accentSoft, ring).border(CalendarTokens.dropRing, palette.accent, ring) else Modifier)
                    .then(if (onAdd == null) Modifier else Modifier.pointerInput(day) { detectTapGestures { onAdd(CalendarSlot(day, null)) } })
                    .testTag("todo-calendar-allday-$day")
                    .padding(Tokens.Space.xxs),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
            ) {
                for (e in fit.shown) key(e.todoId) {
                    EntryChip(e, lifted = e.todoId == lifted, moves = moves, onOpen = { onOpen(e.todoId) },
                        onReschedule = onReschedule?.let { { it(e.todoId) } })
                }
                if (fit.more > 0) {
                    Text(stringResource(R.string.agents_todo_calendar_more, fit.more), style = PersonaTheme.type.micro, color = palette.ink3,
                        maxLines = 1, modifier = Modifier.clickable { onPickDay(day) }.padding(start = Tokens.Space.xxs))
                }
            }
        }
    }
}

/**
 * The hours down the axis' side, each centered on its line; while today is shown, the moment in red on the now line,
 * the hours within [NowLabelRoom] minutes of it giving way.
 */
@Composable
private fun HourGutter(width: Dp, narrow: Boolean, locale: Locale, nowMinutes: Float?) {
    val palette = PersonaTheme.palette
    val hourFormat = remember(locale, narrow) { DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, if (narrow) "H" else "HHmm"), locale) }
    val nowFormat = remember(locale) { DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, "HHmm"), locale) }
    val hours = (1 until 24).filter { h -> nowMinutes == null || abs(h * 60 - nowMinutes) >= NowLabelRoom }
    val end = Tokens.Space.sm
    Layout(
        content = {
            for (h in hours) {
                Text(hourFormat.format(LocalTime.of(h, 0)), style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1, softWrap = false)
            }
            if (nowMinutes != null) {
                Text(
                    nowFormat.format(LocalTime.MIN.plusMinutes(nowMinutes.toLong())),
                    style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.Bold),
                    color = palette.danger.text,
                    maxLines = 1,
                    softWrap = false,
                    modifier = Modifier.testTag("todo-calendar-now-label"),
                )
            }
        },
        modifier = Modifier.width(width).fillMaxHeight(),
    ) { measurables, constraints ->
        val loose = constraints.copy(minWidth = 0, minHeight = 0)
        val labels = measurables.map { it.measure(loose) }
        val w = constraints.maxWidth
        val hourPx = CalendarTokens.hour.toPx()
        layout(w, constraints.maxHeight) {
            labels.forEachIndexed { i, label ->
                val y = if (i < hours.size) hours[i] * hourPx else nowMinutes!! / 60f * hourPx
                label.placeRelative((w - end.roundToPx() - label.width).coerceAtLeast(0), (y - label.height / 2f).roundToInt().coerceAtLeast(0))
            }
        }
    }
}

/**
 * A timed to-do on the axis: its title on its state's edge, red once overdue, faded and struck through when done; wide,
 * its priority flag, reminder bell, and state · when. A tap opens it; for TalkBack it is a button with "Change Due
 * Date…"; with [moves] a long press picks it up.
 */
@Composable
private fun AxisBlock(
    entry: CalendarEntry,
    narrow: Boolean,
    lifted: Boolean,
    moves: EntryMoves?,
    onOpen: () -> Unit,
    onReschedule: (() -> Unit)?,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val locale = LocalConfiguration.current.locales[0]
    val placed = remember { Placed() }
    val current by rememberUpdatedState(entry)
    val handler by rememberUpdatedState(moves)
    val spoken = entrySpoken(entry)
    val whenText = entryWhen(entry, locale)
    val changeDue = stringResource(R.string.agents_todo_calendar_change_due_action)
    val colors = palette.entryColors(entry)
    val stateText = entry.restStatus?.label ?: entry.state.label
    val strike = if (entry.done) TextDecoration.LineThrough else null
    Row(
        modifier.clip(RoundedCornerShape(Tokens.Radius.badge))
            .clickable(onClick = onOpen)
            .clearAndSetSemantics {
                contentDescription = spoken
                role = Role.Button
                testTag = "todo-calendar-entry-${entry.todoId}"
                onClick { onOpen(); true }
                if (onReschedule != null) customActions = listOf(CustomAccessibilityAction(changeDue) { onReschedule(); true })
            }
            // after the click, so a long press let go without moving is the drag's, never a tap
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
            .background(palette.sunken)
            .alpha(when {
                lifted -> CalendarTokens.liftedFade
                entry.done -> CalendarTokens.doneFade
                else -> 1f
            }),
    ) {
        Box(Modifier.width(Tokens.Size.bar).fillMaxHeight().background(colors.dot))
        Column(Modifier.weight(1f).padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xxs)) {
            val titleColor = if (entry.overdue) palette.danger.text else palette.ink
            if (narrow) {
                Text(entry.title, style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.SemiBold, textDecoration = strike), color = titleColor,
                    maxLines = 2, overflow = TextOverflow.Ellipsis)
            } else {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                    Text(entry.title, style = PersonaTheme.type.labelSmall.copy(textDecoration = strike), color = titleColor,
                        maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    if (entry.high) JIcon(PersonaIcons.highPriority, null, size = Tokens.Size.markS, tint = palette.cautionText)
                    if (entry.reminding) JIcon(PersonaIcons.reminder, null, size = Tokens.Size.markS, tint = palette.ink3)
                }
                Text(
                    buildAnnotatedString {
                        withStyle(SpanStyle(color = colors.text)) { append(stateText) }
                        append(" · ")
                        withStyle(SpanStyle(color = if (entry.overdue) palette.danger.text else palette.ink3)) { append(whenText) }
                    },
                    style = PersonaTheme.type.micro,
                    color = palette.ink3,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
    }
}
