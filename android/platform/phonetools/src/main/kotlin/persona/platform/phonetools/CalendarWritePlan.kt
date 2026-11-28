package persona.platform.phonetools

import android.content.res.Resources
import android.provider.CalendarContract as Calendar
import persona.core.designsystem.LocalizedFormats
import kotlinx.serialization.json.JsonObject
import java.time.DayOfWeek
import java.time.Duration
import java.time.Instant
import java.time.LocalDate
import java.time.LocalDateTime
import java.time.LocalTime
import java.time.ZoneId
import java.time.ZoneOffset
import java.time.ZonedDateTime
import java.time.format.DateTimeFormatter
import java.time.temporal.ChronoUnit
import java.time.temporal.TemporalAdjusters
import java.util.Locale

internal enum class EventSpan(val wire: String) {
    This("this"), Future("future");

    companion object {
        fun parse(args: JsonObject): EventSpan = when (val value = args.text("span")) {
            null, "this" -> This
            "future" -> Future
            else -> invalid("span must be this or future, not \"$value\"")
        }
    }
}

internal data class CalendarAttendee(
    val name: String?, val email: String?, val relationship: Int, val type: Int, val status: Int,
    val identity: String? = null, val idNamespace: String? = null,
) {
    fun values(): Map<String, Any?> = mapOf(
        Calendar.Attendees.ATTENDEE_NAME to name, Calendar.Attendees.ATTENDEE_EMAIL to email,
        Calendar.Attendees.ATTENDEE_RELATIONSHIP to relationship, Calendar.Attendees.ATTENDEE_TYPE to type,
        Calendar.Attendees.ATTENDEE_STATUS to status, Calendar.Attendees.ATTENDEE_IDENTITY to identity,
        Calendar.Attendees.ATTENDEE_ID_NAMESPACE to idNamespace,
    )
}

internal data class CalendarReminder(val minutes: Int, val method: Int) {
    fun values(): Map<String, Any?> = mapOf(Calendar.Reminders.MINUTES to minutes, Calendar.Reminders.METHOD to method)
}

internal data class CalendarEventRecord(
    val id: Long,
    val calendarId: Long,
    val title: String,
    val start: Long,
    val end: Long?,
    val duration: String?,
    val timezone: String,
    val endTimezone: String?,
    val allDay: Boolean,
    val rrule: String?,
    val rdate: String?,
    val exrule: String?,
    val exdate: String?,
    val location: String?,
    val notes: String?,
    val status: Int,
    val originalId: Long?,
    val originalInstanceTime: Long?,
    val originalAllDay: Boolean?,
    val organizer: String?,
    val availability: Int?,
    val accessLevel: Int?,
    val guestsCanInviteOthers: Boolean?,
    val guestsCanModify: Boolean?,
    val guestsCanSeeGuests: Boolean?,
    val calendarName: String? = null,
    val ownerAccount: String? = null,
    val calendarAccess: Int? = null,
    /** False for an event of a calendar that never syncs (a local calendar): the provider cannot link an exception to its series. */
    val hasSyncId: Boolean = true,
) {
    val recurring: Boolean get() = !rrule.isNullOrBlank() || !rdate.isNullOrBlank() || originalId != null
    val effectiveEnd: Long get() = end ?: (start + ((durationSeconds(duration.orEmpty()) ?: 0L) * 1000))

    /** User-owned values safe to copy to an exception or a split series; sync identifiers are deliberately excluded. */
    fun values(): Map<String, Any?> = buildMap {
        put(Calendar.Events.CALENDAR_ID, calendarId)
        put(Calendar.Events.TITLE, title)
        put(Calendar.Events.DTSTART, start)
        put(Calendar.Events.DTEND, end)
        put(Calendar.Events.DURATION, duration)
        put(Calendar.Events.EVENT_TIMEZONE, timezone)
        put(Calendar.Events.EVENT_END_TIMEZONE, endTimezone)
        put(Calendar.Events.ALL_DAY, if (allDay) 1 else 0)
        put(Calendar.Events.RRULE, rrule)
        put(Calendar.Events.RDATE, rdate)
        put(Calendar.Events.EXRULE, exrule)
        put(Calendar.Events.EXDATE, exdate)
        put(Calendar.Events.EVENT_LOCATION, location)
        put(Calendar.Events.DESCRIPTION, notes)
        put(Calendar.Events.STATUS, status)
        put(Calendar.Events.ORGANIZER, organizer)
        availability?.let { put(Calendar.Events.AVAILABILITY, it) }
        accessLevel?.let { put(Calendar.Events.ACCESS_LEVEL, it) }
        guestsCanInviteOthers?.let { put(Calendar.Events.GUESTS_CAN_INVITE_OTHERS, if (it) 1 else 0) }
        guestsCanModify?.let { put(Calendar.Events.GUESTS_CAN_MODIFY, if (it) 1 else 0) }
        guestsCanSeeGuests?.let { put(Calendar.Events.GUESTS_CAN_SEE_GUESTS, if (it) 1 else 0) }
    }
}

internal data class CalendarTargetSnapshot(
    val requestedId: Long,
    val series: CalendarEventRecord,
    val occurrence: CalendarEventRecord,
    val exception: CalendarEventRecord?,
    val attendees: List<CalendarAttendee>,
    val reminders: List<CalendarReminder>,
    /** Every exception of the series whose original instance is at or after the selected one (the target's own included). */
    val laterExceptions: List<CalendarEventRecord> = emptyList(),
) {
    val recurring: Boolean get() = series.recurring || occurrence.originalId != null
    val originalInstanceTime: Long get() = occurrence.originalInstanceTime ?: occurrence.start
}

internal fun displayEventTime(millis: Long, allDay: Boolean, zone: ZoneId): Instant =
    if (allDay) Instant.ofEpochMilli(millis).atZone(ZoneOffset.UTC).toLocalDate().atStartOfDay(zone).toInstant()
    else Instant.ofEpochMilli(millis)

internal fun storedEventTime(instant: Instant, allDay: Boolean, zone: ZoneId): Long =
    if (allDay) instant.atZone(zone).toLocalDate().atStartOfDay(ZoneOffset.UTC).toInstant().toEpochMilli()
    else instant.toEpochMilli()

internal data class CalendarEventAfter(
    val title: String,
    val start: Instant,
    val end: Instant,
    val allDay: Boolean,
    val location: String?,
    val notes: String?,
)

internal data class CalendarEventChange(
    val title: String? = null,
    val start: Instant? = null,
    val end: Instant? = null,
    val location: String? = null,
    val notes: String? = null,
    val allDay: Boolean? = null,
) {
    companion object {
        /**
         * The end that keeps the event's length when only a new start is given. An all-day event keeps its number of
         * calendar days (a day is not always 24 hours across a clock change); a timed one keeps its elapsed time.
         */
        private fun keptLength(from: Instant, currentStart: Instant, currentEnd: Instant, allDay: Boolean, zone: ZoneId): Instant {
            if (!allDay) return from + Duration.between(currentStart, currentEnd)
            val days = ChronoUnit.DAYS.between(currentStart.atZone(zone).toLocalDate(), currentEnd.atZone(zone).toLocalDate())
            return from.atZone(zone).toLocalDate().plusDays(maxOf(days, 1L)).atStartOfDay(zone).toInstant()
        }

        fun parse(args: JsonObject, current: CalendarEventRecord, zone: ZoneId): Pair<CalendarEventChange, CalendarEventAfter> {
            val currentStart = displayEventTime(current.start, current.allDay, zone)
            val currentEnd = displayEventTime(current.effectiveEnd, current.allDay, zone)
            val requestedAllDay = args.boolOrNull("all_day")
            val newAllDay = requestedAllDay ?: current.allDay
            val rawStart = args.text("start")
            val rawEnd = args.text("end")
            var start = parseTime(rawStart, zone)
            var end = parseTime(rawEnd, zone)
            if (start != null || end != null) {
                val from = start ?: currentStart
                val to = end ?: if (start != null) keptLength(from, currentStart, currentEnd, newAllDay, zone) else currentEnd
                if (!to.isAfter(from)) invalid("end must be after start")
                start = from; end = to
            } else if (requestedAllDay != null && requestedAllDay != current.allDay) {
                if (requestedAllDay) {
                    val first = currentStart.atZone(zone).toLocalDate()
                    var last = currentEnd.atZone(zone).toLocalDate()
                    val endTime = currentEnd.atZone(zone).toLocalTime()
                    if (!last.isAfter(first) || endTime != LocalTime.MIDNIGHT) last = last.plusDays(1)
                    start = first.atStartOfDay(zone).toInstant()
                    end = last.atStartOfDay(zone).toInstant()
                } else {
                    start = currentStart
                    end = currentEnd
                }
            }
            if (start != null && newAllDay) {
                val first = start!!.atZone(zone).toLocalDate()
                val last = end!!.atZone(zone).toLocalDate()
                if (!last.isAfter(first)) invalid("an all-day event needs an exclusive end date after its start date")
            }
            val title = args.text("title")
            val location = args.raw("location")?.trim()
            val notes = args.raw("notes")
            val change = CalendarEventChange(
                title = title?.takeIf { it != current.title },
                start = start?.takeIf { it != currentStart || newAllDay != current.allDay },
                end = end?.takeIf { it != currentEnd || newAllDay != current.allDay },
                location = location?.takeIf { it != current.location.orEmpty() },
                notes = notes?.takeIf { it != current.notes.orEmpty() },
                allDay = requestedAllDay?.takeIf { it != current.allDay },
            )
            if (change == CalendarEventChange()) {
                if (title == null && rawStart == null && rawEnd == null && location == null && notes == null && requestedAllDay == null) {
                    invalid("nothing to change: pass title, start, end, location, notes or all_day")
                }
                invalid("nothing to change: the event already has these values")
            }
            val after = CalendarEventAfter(
                title = change.title ?: current.title,
                start = change.start ?: currentStart,
                end = change.end ?: currentEnd,
                allDay = newAllDay,
                location = if (change.location != null) change.location.takeIf(String::isNotEmpty) else current.location,
                notes = if (change.notes != null) change.notes.takeIf(String::isNotEmpty) else current.notes,
            )
            return change to after
        }
    }
}

internal fun calendarRange(
    start: Instant,
    end: Instant,
    allDay: Boolean,
    res: Resources,
    zone: ZoneId,
    today: LocalDate = LocalDate.now(zone),
): String {
    val locale = res.configuration.locales[0]
    val from = start.atZone(zone)
    val to = end.atZone(zone)
    fun day(date: ZonedDateTime) = LocalizedFormats.formatter(
        if (date.year == today.year) LocalizedFormats.MONTH_DAY else LocalizedFormats.DATE, locale, zone).format(date)
    fun time(date: ZonedDateTime) = LocalizedFormats.formatter(LocalizedFormats.TIME, locale, zone).format(date)
    val range = buildString {
        if (allDay) {
            val last = to.minusDays(1).takeIf { it.toLocalDate().isAfter(from.toLocalDate()) }
            append(day(from)); last?.let { append(" → ").append(day(it)) }
        } else {
            append(day(from)).append(' ').append(time(from)); append(" → ")
            if (to.toLocalDate() != from.toLocalDate()) append(day(to)).append(' ')
            append(time(to))
        }
    }
    return if (allDay) res.getString(R.string.phonetools_calendar_all_day, range) else range
}

private const val NOTES_PREVIEW_LENGTH = 200

/** A long text cut for the card says so and how long it really is: a replaced note is approved as a whole. */
private fun clipped(res: Resources, text: String): String {
    if (text.length <= NOTES_PREVIEW_LENGTH) return text
    var cut = NOTES_PREVIEW_LENGTH
    if (Character.isHighSurrogate(text[cut - 1])) cut--
    return res.getQuantityString(R.plurals.phonetools_text_clipped, text.length, text.substring(0, cut), text.length)
}

internal data class CalendarUpdatePreparation(
    val args: JsonObject,
    val snapshot: CalendarTargetSnapshot,
    val span: EventSpan,
    val change: CalendarEventChange,
    val after: CalendarEventAfter,
    val zone: ZoneId,
) {
    val text: PhoneConfirmationText = { res ->
        val current = snapshot.occurrence
        val currentStart = displayEventTime(current.start, current.allDay, zone)
        val currentEnd = displayEventTime(current.effectiveEnd, current.allDay, zone)
        val title = current.title.ifEmpty { res.getString(R.string.phonetools_untitled) }
        val lines = mutableListOf(res.getString(R.string.phonetools_calendar_change, title))
        current.calendarName?.let { lines += res.getString(R.string.phonetools_calendar_calendar, it) }
        if (change.start != null || change.end != null) {
            lines += res.getString(R.string.phonetools_calendar_time_change,
                calendarRange(currentStart, currentEnd, current.allDay, res, zone),
                calendarRange(after.start, after.end, after.allDay, res, zone))
        } else if (change.allDay != null) {
            lines += res.getString(if (after.allDay) R.string.phonetools_calendar_all_day_on else R.string.phonetools_calendar_all_day_off)
        }
        change.title?.let { lines += res.getString(R.string.phonetools_calendar_title_change, title, it) }
        change.location?.let {
            lines += res.getString(R.string.phonetools_calendar_location_change,
                current.location?.takeIf(String::isNotEmpty) ?: res.getString(R.string.phonetools_not_set),
                it.ifEmpty { res.getString(R.string.phonetools_not_set) })
        }
        change.notes?.let {
            lines += res.getString(R.string.phonetools_calendar_notes_change,
                clipped(res, current.notes?.takeIf(String::isNotEmpty) ?: res.getString(R.string.phonetools_not_set)),
                clipped(res, it.ifEmpty { res.getString(R.string.phonetools_not_set) }))
        }
        if (snapshot.recurring) lines += res.getString(if (span == EventSpan.Future)
            R.string.phonetools_calendar_future_occurrences else R.string.phonetools_calendar_this_occurrence)
        lines.joinToString("\n")
    }
}

internal data class CalendarDeletePreparation(
    val args: JsonObject,
    val snapshot: CalendarTargetSnapshot,
    val span: EventSpan,
    val zone: ZoneId,
) {
    val text: PhoneConfirmationText = { res ->
        val current = snapshot.occurrence
        val title = current.title.ifEmpty { res.getString(R.string.phonetools_untitled) }
        val lines = mutableListOf(res.getString(R.string.phonetools_calendar_delete, title))
        lines += calendarRange(displayEventTime(current.start, current.allDay, zone),
            displayEventTime(current.effectiveEnd, current.allDay, zone), current.allDay, res, zone)
        current.calendarName?.let { lines += res.getString(R.string.phonetools_calendar_calendar, it) }
        if (snapshot.recurring) lines += res.getString(if (span == EventSpan.Future)
            R.string.phonetools_calendar_future_occurrences else R.string.phonetools_calendar_this_occurrence)
        lines.joinToString("\n")
    }
}

internal object RecurrenceEdit {
    private val basicUtc: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyyMMdd'T'HHmmss'Z'", Locale.ROOT).withZone(ZoneOffset.UTC)
    private val basicLocal: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyyMMdd'T'HHmmss", Locale.ROOT)
    private val basicDate: DateTimeFormatter = DateTimeFormatter.BASIC_ISO_DATE

    data class Split(
        val oldRrule: String?, val oldRdate: String?, val oldExdate: String?,
        val newRrule: String?, val newRdate: String?, val newExdate: String?,
        val deleteWholeOldSeries: Boolean,
    )

    private val patternParts = setOf("BYDAY", "BYMONTHDAY", "BYMONTH", "BYSETPOS", "BYYEARDAY", "BYWEEKNO", "BYHOUR", "BYMINUTE", "BYSECOND")

    private fun requireSplittable(series: CalendarEventRecord) {
        if (!series.exrule.isNullOrBlank()) {
            unavailable("这条重复日程带有 EXRULE，暂不能安全拆分，未写入 / This repeating event has an EXRULE and cannot be split safely; nothing was written.")  // i18n-ignore: tool result for the model
        }
    }

    /** Floating recurrence dates belong to the series' own zone (UTC for an all-day one), not to the phone's. */
    private fun seriesZone(series: CalendarEventRecord, phone: ZoneId): ZoneId =
        if (series.allDay) ZoneOffset.UTC else runCatching { ZoneId.of(series.timezone) }.getOrElse { phone }

    fun split(series: CalendarEventRecord, selected: Long, newStart: Long, zone: ZoneId): Split {
        requireSplittable(series)
        val rule = series.rrule?.let(::parts)
        if (rule != null && rule.keys.any { it in patternParts } &&
            localDate(newStart, series.allDay, zone) != localDate(selected, series.allDay, zone)) {
            // A pattern anchored on named days would keep its days while the first occurrence moved: not the same series.
            unavailable("这条重复规则带有固定的星期 / 日期，不能把这次及以后移到另一天，未写入 / The repeat rule names fixed days, so this and later occurrences cannot move to another day; nothing was written.")  // i18n-ignore: tool result for the model
        }
        val dateZone = seriesZone(series, zone)
        val oldRrule = series.rrule?.let { truncateRule(it, selected, series.allDay, dateZone) }
        val oldRdate = filterDates(series.rdate, selected, keepFuture = false, shiftMillis = 0, zone = dateZone)
        val oldExdate = filterDates(series.exdate, selected, keepFuture = false, shiftMillis = 0, zone = dateZone)
        val delta = newStart - selected
        val newRrule = series.rrule?.let { remainingRule(it, series, selected, zone) }
        val newRdate = filterDates(series.rdate, selected, keepFuture = true, shiftMillis = delta, zone = dateZone)
        val newExdate = filterDates(series.exdate, selected, keepFuture = true, shiftMillis = delta, zone = dateZone)
        val noPast = selected <= series.start && oldRdate.isNullOrBlank()
        return Split(oldRrule, oldRdate, oldExdate, newRrule, newRdate, newExdate, noPast)
    }

    fun truncateOnly(series: CalendarEventRecord, selected: Long, zone: ZoneId): Split {
        requireSplittable(series)
        val dateZone = seriesZone(series, zone)
        val oldRrule = series.rrule?.let { truncateRule(it, selected, series.allDay, dateZone) }
        val oldRdate = filterDates(series.rdate, selected, keepFuture = false, shiftMillis = 0, zone = dateZone)
        val oldExdate = filterDates(series.exdate, selected, keepFuture = false, shiftMillis = 0, zone = dateZone)
        return Split(oldRrule, oldRdate, oldExdate, null, null, null,
            selected <= series.start && oldRdate.isNullOrBlank())
    }

    private fun parts(rule: String): LinkedHashMap<String, String> {
        val out = LinkedHashMap<String, String>()
        for (part in rule.split(';').filter(String::isNotBlank)) {
            val pieces = part.split('=', limit = 2)
            if (pieces.size == 2) out[pieces[0].uppercase(Locale.ROOT)] = pieces[1]
        }
        return out
    }

    private fun render(parts: LinkedHashMap<String, String>): String = parts.entries.joinToString(";") { "${it.key}=${it.value}" }

    private fun localDate(millis: Long, allDay: Boolean, zone: ZoneId): LocalDate =
        Instant.ofEpochMilli(millis).atZone(if (allDay) ZoneOffset.UTC else zone).toLocalDate()

    private fun truncateRule(rule: String, selected: Long, allDay: Boolean, seriesZone: ZoneId): String {
        val parts = parts(rule)
        parts.remove("COUNT")
        // An all-day series ends on a date, a timed one an instant before the selected occurrence.
        val cutoff = if (allDay) basicDate.format(Instant.ofEpochMilli(selected).atZone(ZoneOffset.UTC).toLocalDate().minusDays(1))
            else basicUtc.format(Instant.ofEpochMilli(selected - 1))
        val existing = parts["UNTIL"]?.let { untilEnd(it, allDay, seriesZone) }
        if (existing == null || existing >= selected) parts["UNTIL"] = cutoff
        return render(parts)
    }

    /**
     * The last instant an UNTIL still allows. A date-only UNTIL on a timed series includes that whole day, so it is read
     * as the end of the day in the later of UTC and the series zone: reading it too early would keep an UNTIL that still
     * produces the selected occurrence next to the new series, while reading it too late only rewrites it to the cutoff.
     */
    private fun untilEnd(raw: String, allDay: Boolean, seriesZone: ZoneId): Long? {
        val start = parseRuleDate(raw.trim(), seriesZone) ?: return null
        if (allDay || raw.trim().length != 8) return start
        val date = Instant.ofEpochMilli(start).atZone(ZoneOffset.UTC).toLocalDate().plusDays(1)
        return maxOf(date.atStartOfDay(ZoneOffset.UTC).toInstant().toEpochMilli(), date.atStartOfDay(seriesZone).toInstant().toEpochMilli()) - 1
    }

    private fun remainingRule(rule: String, series: CalendarEventRecord, selected: Long, zone: ZoneId): String {
        val parts = parts(rule)
        val count = parts["COUNT"]?.toIntOrNull()
        if (count != null) {
            val ordinal = occurrenceOrdinal(parts, series, selected, zone, count)
                ?: unavailable("无法精确拆分这条重复规则，未写入 / This recurrence rule cannot be split exactly; nothing was written.")  // i18n-ignore: tool result for the model
            val remaining = count - ordinal
            if (remaining <= 0) invalid("the selected occurrence is outside this recurrence rule")
            parts["COUNT"] = remaining.toString()
        } else {
            // An RDATE-only selected occurrence cannot re-anchor a separate RRULE without changing its phase.
            val onRule = occurrenceOrdinal(parts, series, selected, zone, 10_000)
            if (onRule == null && containsDate(series.rdate, selected, seriesZone(series, zone))) {
                unavailable("无法把额外日期与其余重复规则安全拆分，未写入 / The extra date cannot be safely split from the recurrence rule; nothing was written.")  // i18n-ignore: tool result for the model
            }
        }
        return render(parts)
    }

    private fun occurrenceOrdinal(
        parts: LinkedHashMap<String, String>, series: CalendarEventRecord, selected: Long, zone: ZoneId, limit: Int,
    ): Int? {
        val frequency = parts["FREQ"]?.uppercase(Locale.ROOT) ?: return null
        val interval = parts["INTERVAL"]?.toIntOrNull()?.takeIf { it > 0 } ?: 1
        val supported = parts.keys - setOf("FREQ", "INTERVAL", "COUNT", "UNTIL", "BYDAY", "WKST")
        if (supported.isNotEmpty()) return null
        if (frequency != "WEEKLY" && (parts.containsKey("BYDAY") || parts.containsKey("WKST"))) return null
        val timeZone = if (series.allDay) ZoneOffset.UTC else runCatching { ZoneId.of(series.timezone) }.getOrElse { zone }
        val start = Instant.ofEpochMilli(series.start).atZone(timeZone)
        var index = 0
        for (candidate in sequence(start, frequency, interval, parts["BYDAY"], parts["WKST"])) {
            if (index >= limit) return null
            val millis = candidate.toInstant().toEpochMilli()
            if (millis == selected) return index
            if (millis > selected) return null
            index++
        }
        return null
    }

    private fun sequence(
        start: ZonedDateTime, frequency: String, interval: Int, byDay: String?, weekStart: String?,
    ): Sequence<ZonedDateTime> = sequence {
        when (frequency) {
            "DAILY" -> {
                var current = start
                while (true) { yield(current); current = current.plusDays(interval.toLong()) }
            }
            "WEEKLY" -> {
                val days = byDay?.split(',')?.mapNotNull(::dayOfWeek)?.distinct().orEmpty()
                if (byDay != null && days.size != byDay.split(',').size) return@sequence
                val firstWeekDay = weekStart?.let(::dayOfWeek) ?: DayOfWeek.MONDAY
                var week = start.with(TemporalAdjusters.previousOrSame(firstWeekDay))
                while (true) {
                    val candidates = if (days.isEmpty()) listOf(week.with(TemporalAdjusters.nextOrSame(start.dayOfWeek)))
                    else days.sortedBy { (it.value - firstWeekDay.value + 7) % 7 }
                        .map { week.with(TemporalAdjusters.nextOrSame(it)) }
                    for (candidate in candidates) if (!candidate.isBefore(start)) yield(candidate)
                    week = week.plusWeeks(interval.toLong())
                }
            }
            "MONTHLY" -> {
                var step = 0L
                while (true) {
                    val candidate = start.plusMonths(step)
                    if (candidate.dayOfMonth == start.dayOfMonth) yield(candidate)
                    step += interval
                }
            }
            "YEARLY" -> {
                var step = 0L
                while (true) {
                    val candidate = start.plusYears(step)
                    if (candidate.month == start.month && candidate.dayOfMonth == start.dayOfMonth) yield(candidate)
                    step += interval
                }
            }
        }
    }

    private fun dayOfWeek(value: String): DayOfWeek? = when (value) {
        "MO" -> DayOfWeek.MONDAY; "TU" -> DayOfWeek.TUESDAY; "WE" -> DayOfWeek.WEDNESDAY
        "TH" -> DayOfWeek.THURSDAY; "FR" -> DayOfWeek.FRIDAY; "SA" -> DayOfWeek.SATURDAY; "SU" -> DayOfWeek.SUNDAY
        else -> null
    }

    /**
     * A stored RDATE / EXDATE value: comma separated dates with an optional leading zone, either `ZONE;dates` (the
     * Android provider's form) or `TZID=ZONE:dates` (iCalendar). The prefix is kept as written and written back once.
     */
    private class DateList(val prefix: String, val zone: ZoneId, val items: List<String>) {
        fun render(kept: List<String>): String? = kept.takeIf { it.isNotEmpty() }?.joinToString(",", prefix)
    }

    private fun dateList(raw: String, fallback: ZoneId): DateList {
        val colon = raw.indexOf(':')
        val semicolon = raw.indexOf(';')
        val split = when {
            colon >= 0 -> colon
            semicolon >= 0 -> semicolon
            else -> return DateList("", fallback, raw.split(',').map(String::trim).filter(String::isNotEmpty))
        }
        val head = raw.substring(0, split)
        val tzid = if (split == colon) head.split(';').firstOrNull { it.startsWith("TZID=", ignoreCase = true) }?.substring(5) else head
        val zone = tzid?.let { id -> runCatching { ZoneId.of(id.trim()) }.getOrNull()
            ?: unavailable("无法读取重复日期的时区，未写入 / The time zone of a recurrence date cannot be read; nothing was written.") }  // i18n-ignore: tool result for the model
            ?: fallback
        return DateList(raw.substring(0, split + 1), zone, raw.substring(split + 1).split(',').map(String::trim).filter(String::isNotEmpty))
    }

    private fun filterDates(raw: String?, selected: Long, keepFuture: Boolean, shiftMillis: Long, zone: ZoneId): String? {
        if (raw.isNullOrBlank()) return raw
        val list = dateList(raw, zone)
        val output = list.items.mapNotNull { item ->
            if (item.contains('/')) unavailable("含 PERIOD 的 RDATE 暂不能安全拆分 / RDATE periods cannot be split safely.")  // i18n-ignore: tool result for the model
            val parsed = parseRuleDate(item, list.zone) ?: unavailable("无法读取重复日期，未写入 / A recurrence date cannot be read; nothing was written.")  // i18n-ignore: tool result for the model
            val keep = if (keepFuture) parsed >= selected else parsed < selected
            if (!keep) return@mapNotNull null
            formatRuleDate(item, parsed + shiftMillis, list.zone)
        }
        return list.render(output)
    }

    private fun containsDate(raw: String?, selected: Long, zone: ZoneId): Boolean {
        if (raw.isNullOrBlank()) return false
        val list = dateList(raw, zone)
        return list.items.any { parseRuleDate(it, list.zone) == selected }
    }

    /** A bare value is a date (UTC), a UTC instant (`Z`) or a floating time in [zone]. */
    private fun parseRuleDate(raw: String, zone: ZoneId): Long? = when {
        raw.length == 8 -> runCatching { LocalDate.parse(raw, basicDate).atStartOfDay(ZoneOffset.UTC).toInstant().toEpochMilli() }.getOrNull()
        raw.endsWith('Z') -> runCatching { ZonedDateTime.parse(raw, basicUtc).toInstant().toEpochMilli() }.getOrNull()
        else -> runCatching { LocalDateTime.parse(raw, basicLocal).atZone(zone).toInstant().toEpochMilli() }.getOrNull()
    }

    private fun formatRuleDate(original: String, millis: Long, zone: ZoneId): String = when {
        original.length == 8 -> Instant.ofEpochMilli(millis).atZone(ZoneOffset.UTC).toLocalDate().format(basicDate)
        original.endsWith('Z') -> basicUtc.format(Instant.ofEpochMilli(millis))
        else -> basicLocal.format(Instant.ofEpochMilli(millis).atZone(zone))
    }
}

internal fun durationSeconds(duration: String): Long? {
    val match = Regex("^P(?:(\\d+)W)?(?:(\\d+)D)?(?:T(?:(\\d+)H)?(?:(\\d+)M)?(?:(\\d+)S)?)?$").find(duration) ?: return null
    val values = match.groupValues.drop(1).map { it.toLongOrNull() ?: 0L }
    return values[0] * 7 * 86_400 + values[1] * 86_400 + values[2] * 3_600 + values[3] * 60 + values[4]
}

internal fun durationString(start: Instant, end: Instant, allDay: Boolean, zone: ZoneId): String {
    if (allDay) {
        val days = ChronoUnit.DAYS.between(start.atZone(zone).toLocalDate(), end.atZone(zone).toLocalDate())
        return "P${days}D"
    }
    var seconds = Duration.between(start, end).seconds
    val days = seconds / 86_400; seconds %= 86_400
    val hours = seconds / 3_600; seconds %= 3_600
    val minutes = seconds / 60; seconds %= 60
    return buildString {
        append('P'); if (days != 0L) append(days).append('D')
        if (hours != 0L || minutes != 0L || seconds != 0L || days == 0L) {
            append('T'); if (hours != 0L) append(hours).append('H')
            if (minutes != 0L) append(minutes).append('M')
            if (seconds != 0L || (days == 0L && hours == 0L && minutes == 0L)) append(seconds).append('S')
        }
    }
}

/** The provider operations of an approved calendar change, and which event the answer reads back. */
internal data class CalendarWritePlan(
    val writes: List<ProviderWrite>,
    /** An existing event to read back after the batch; null when [resultInsertIndex] names a created one. */
    val resultEventId: Long? = null,
    val resultInsertIndex: Int? = null,
)

/**
 * Pure planning over a [CalendarTargetSnapshot]: the same plan is dry-run before the card is shown (so an unsupported
 * change is refused up front) and run again from the re-read snapshot before the batch. Nothing here touches a provider.
 */
internal object CalendarWrites {
    private val events = Calendar.Events.CONTENT_URI
    private val recurrenceKeys = listOf(Calendar.Events.RRULE, Calendar.Events.RDATE, Calendar.Events.EXRULE, Calendar.Events.EXDATE)

    private fun exceptionUri(seriesId: Long) = android.content.ContentUris.withAppendedId(Calendar.Events.CONTENT_EXCEPTION_URI, seriesId)

    /**
     * The provider hides a replaced or cancelled occurrence by matching the exception's original sync id to the series' sync id.
     * A series without one (a local calendar) gets the exception row but keeps showing the old occurrence, and the exception
     * insert even empties the series' expanded occurrences until the provider next re-expands them: refuse instead.
     */
    private fun requireExceptionSupport(series: CalendarEventRecord) {
        if (series.hasSyncId) return
        unavailable("这条重复日程所在的日历不同步，系统日历无法单独改动或取消其中一次，未写入 / This repeating event is in a calendar that does not sync, so the system calendar cannot change or cancel a single occurrence of it; nothing was written. Change or delete the whole series, or this and later occurrences, instead.")  // i18n-ignore: tool result for the model
    }

    fun update(p: CalendarUpdatePreparation): CalendarWritePlan {
        val s = p.snapshot
        if (!s.recurring) return partial(s.series, p)
        if (p.span == EventSpan.This) {
            s.exception?.let { return partial(it, p) }
            requireExceptionSupport(s.series)
            // The provider copies the series' attendees and reminders onto the exception itself; copying them again doubles them.
            return CalendarWritePlan(listOf(ProviderWrite(ProviderWrite.Kind.Insert, exceptionUri(s.series.id),
                exceptionValues(s.occurrence, p) + (Calendar.Events.ORIGINAL_INSTANCE_TIME to s.originalInstanceTime))), resultInsertIndex = 0)
        }
        // this and future
        if (s.laterExceptions.isNotEmpty()) {
            unavailable("这次及以后的范围里已有单独改过或取消的日程，暂不能一起修改，未写入 / Some occurrences in this range were already changed or cancelled on their own, so this and later occurrences cannot be changed together yet; nothing was written. Change them one at a time with span \"this\".")  // i18n-ignore: tool result for the model
        }
        val series = s.series
        val newStart = storedEventTime(p.after.start, p.after.allDay, p.zone)
        val split = RecurrenceEdit.split(series, s.originalInstanceTime, newStart, p.zone)
        val writes = mutableListOf<ProviderWrite>()
        writes += ProviderWrite(ProviderWrite.Kind.Insert, events, newSeriesValues(s.occurrence, p, split, newStart))
        writes += copiedRows(s, 0)
        writes += truncation(series, split)
        return CalendarWritePlan(writes, resultInsertIndex = 0)
    }

    fun delete(p: CalendarDeletePreparation): CalendarWritePlan {
        val s = p.snapshot
        if (!s.recurring) return CalendarWritePlan(listOf(ProviderWrite(ProviderWrite.Kind.Delete, events, id = s.series.id)))
        if (p.span == EventSpan.This) {
            val cancel = Calendar.Events.STATUS to Calendar.Events.STATUS_CANCELED
            if (s.exception == null) requireExceptionSupport(s.series)
            return CalendarWritePlan(listOf(s.exception?.let {
                ProviderWrite(ProviderWrite.Kind.Update, events, mapOf(cancel), id = it.id)
            } ?: ProviderWrite(ProviderWrite.Kind.Insert, exceptionUri(s.series.id),
                mapOf(Calendar.Events.ORIGINAL_INSTANCE_TIME to s.originalInstanceTime, cancel))))
        }
        val split = RecurrenceEdit.truncateOnly(s.series, s.originalInstanceTime, p.zone)
        val writes = mutableListOf<ProviderWrite>()
        for (exception in s.laterExceptions) writes += ProviderWrite(ProviderWrite.Kind.Delete, events, id = exception.id)
        writes += truncation(s.series, split)
        return CalendarWritePlan(writes)
    }

    /** The old series ends before the selected occurrence, or goes away when nothing of it would be left. */
    private fun truncation(series: CalendarEventRecord, split: RecurrenceEdit.Split): List<ProviderWrite> =
        if (split.deleteWholeOldSeries) listOf(ProviderWrite(ProviderWrite.Kind.Delete, events, id = series.id))
        else listOf(ProviderWrite(ProviderWrite.Kind.Update, events, mapOf(
            // The AOSP provider re-expands a shortened series only when DTSTART is written with its new rule.
            Calendar.Events.DTSTART to series.start, Calendar.Events.RRULE to split.oldRrule,
            Calendar.Events.RDATE to split.oldRdate, Calendar.Events.EXDATE to split.oldExdate), id = series.id))

    /** A new series does not get the old series' attendees and reminders by itself: copy them with back references to [index]. */
    private fun copiedRows(s: CalendarTargetSnapshot, index: Int): List<ProviderWrite> =
        s.attendees.map { ProviderWrite(ProviderWrite.Kind.Insert, Calendar.Attendees.CONTENT_URI, it.values(),
            backReferences = mapOf(Calendar.Attendees.EVENT_ID to index)) } +
            s.reminders.map { ProviderWrite(ProviderWrite.Kind.Insert, Calendar.Reminders.CONTENT_URI, it.values(),
                backReferences = mapOf(Calendar.Reminders.EVENT_ID to index)) }

    /** Only what the user approved, on an event that already stands alone (a plain event or an existing exception). */
    private fun partial(target: CalendarEventRecord, p: CalendarUpdatePreparation): CalendarWritePlan {
        val change = p.change
        val values = linkedMapOf<String, Any?>()
        change.title?.let { values[Calendar.Events.TITLE] = it }
        change.location?.let { values[Calendar.Events.EVENT_LOCATION] = it.ifEmpty { null } }
        change.notes?.let { values[Calendar.Events.DESCRIPTION] = it.ifEmpty { null } }
        if (change.allDay != null) {
            values[Calendar.Events.ALL_DAY] = if (p.after.allDay) 1 else 0
            values[Calendar.Events.EVENT_TIMEZONE] = timezoneFor(target, p)
            if (p.after.allDay) values[Calendar.Events.EVENT_END_TIMEZONE] = null
        }
        if (change.start != null || change.end != null) {
            values[Calendar.Events.DTSTART] = storedEventTime(p.after.start, p.after.allDay, p.zone)
            values[Calendar.Events.DTEND] = storedEventTime(p.after.end, p.after.allDay, p.zone)
            values[Calendar.Events.DURATION] = null
        }
        return CalendarWritePlan(listOf(ProviderWrite(ProviderWrite.Kind.Update, events, values, id = target.id)), resultEventId = target.id)
    }

    /** All-day events live in UTC; a timed one keeps its own zone, falling back to the phone's. */
    private fun timezoneFor(base: CalendarEventRecord, p: CalendarUpdatePreparation): String =
        if (p.after.allDay) "UTC" else base.timezone.takeUnless { it.isBlank() || (it == "UTC" && base.allDay) } ?: p.zone.id

    private fun fullValues(base: CalendarEventRecord, p: CalendarUpdatePreparation): MutableMap<String, Any?> {
        val values = base.values().toMutableMap()
        values[Calendar.Events.TITLE] = p.after.title
        values[Calendar.Events.EVENT_LOCATION] = p.after.location
        values[Calendar.Events.DESCRIPTION] = p.after.notes
        values[Calendar.Events.ALL_DAY] = if (p.after.allDay) 1 else 0
        values[Calendar.Events.EVENT_TIMEZONE] = timezoneFor(base, p)
        if (p.after.allDay) values[Calendar.Events.EVENT_END_TIMEZONE] = null
        values[Calendar.Events.DTSTART] = storedEventTime(p.after.start, p.after.allDay, p.zone)
        return values
    }

    /** An exception is a standalone event for one occurrence: it carries no recurrence of its own. */
    private fun exceptionValues(occurrence: CalendarEventRecord, p: CalendarUpdatePreparation): Map<String, Any?> {
        val values = fullValues(occurrence, p)
        // The exception URI inherits the series calendar; setting it explicitly is rejected by the AOSP provider.
        values.remove(Calendar.Events.CALENDAR_ID)
        for (key in recurrenceKeys) values.remove(key)
        values.remove(Calendar.Events.DTEND)
        values[Calendar.Events.DURATION] = durationString(p.after.start, p.after.end, p.after.allDay, p.zone)
        return values
    }

    private fun newSeriesValues(
        occurrence: CalendarEventRecord, p: CalendarUpdatePreparation, split: RecurrenceEdit.Split, newStart: Long,
    ): Map<String, Any?> {
        val values = fullValues(occurrence, p)
        values[Calendar.Events.DTSTART] = newStart
        values[Calendar.Events.RRULE] = split.newRrule
        values[Calendar.Events.RDATE] = split.newRdate
        values[Calendar.Events.EXRULE] = null
        values[Calendar.Events.EXDATE] = split.newExdate
        // A repeating event is stored with a DURATION and no end.
        values[Calendar.Events.DTEND] = null
        values[Calendar.Events.DURATION] = durationString(p.after.start, p.after.end, p.after.allDay, p.zone)
        return values
    }
}
