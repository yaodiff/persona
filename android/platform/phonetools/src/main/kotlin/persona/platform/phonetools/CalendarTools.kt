package persona.platform.phonetools

import android.content.ContentProviderResult
import android.content.ContentUris
import android.content.ContentValues
import android.content.Context
import android.content.res.Resources
import persona.core.designsystem.LocalizedFormats
import android.provider.CalendarContract as Calendar
import persona.core.contracts.api.PhoneToolResult
import kotlinx.serialization.json.*
import java.time.*
import java.time.format.DateTimeFormatter
import java.time.temporal.ChronoUnit
import java.util.Locale

internal data class CalendarArguments(
    val title: String, val start: Instant, val end: Instant,
    val location: String?, val notes: String?, val allDay: Boolean, val calendar: String?,
) {
    /** All-day events are stored as UTC midnights of the local dates, the end date exclusive (CalendarContract). */
    fun startMillis(zone: ZoneId = ZoneId.systemDefault()): Long =
        if (allDay) start.atZone(zone).toLocalDate().atStartOfDay(ZoneOffset.UTC).toInstant().toEpochMilli() else start.toEpochMilli()
    fun endMillis(zone: ZoneId = ZoneId.systemDefault()): Long =
        if (allDay) end.atZone(zone).toLocalDate().atStartOfDay(ZoneOffset.UTC).toInstant().toEpochMilli() else end.toEpochMilli()

    /**
     * What the phone shows before saving: the times read in the phone's own zone and written in its language
     * (LocalizedFormats), not as ISO strings.
     */
    fun summary(res: Resources, zone: ZoneId = ZoneId.systemDefault(), today: LocalDate = LocalDate.now(zone)): String {
        val locale = res.configuration.locales[0]
        val from = start.atZone(zone)
        val to = end.atZone(zone)
        fun day(date: ZonedDateTime) = LocalizedFormats.formatter(
            if (date.year == today.year) LocalizedFormats.MONTH_DAY else LocalizedFormats.DATE, locale, zone).format(date)
        fun time(date: ZonedDateTime) = LocalizedFormats.formatter(LocalizedFormats.TIME, locale, zone).format(date)
        val span = buildString {
            if (allDay) {
                // The end date is exclusive: an event on Nov 1 ends at Nov 2 00:00.
                val last = to.minusDays(1).takeIf { it.toLocalDate().isAfter(from.toLocalDate()) }
                append(day(from))
                last?.let { append(" → ").append(day(it)) }
            } else {
                append(day(from)).append(' ').append(time(from))
                append(" → ")
                if (to.toLocalDate() != from.toLocalDate()) append(day(to)).append(' ')
                append(time(to))
            }
        }
        return listOfNotNull(
            res.getString(R.string.phonetools_calendar_new, title),
            if (allDay) res.getString(R.string.phonetools_calendar_all_day, span) else span,
            location?.let { res.getString(R.string.phonetools_calendar_location, it) },
            calendar?.let { res.getString(R.string.phonetools_calendar_calendar, it) },
            notes?.let { res.getString(R.string.phonetools_calendar_notes, if (it.length > 200) it.take(200) + "…" else it) },
        ).joinToString("\n")
    }

    companion object {

        fun parse(args: JsonObject, zone: ZoneId = ZoneId.systemDefault()): CalendarArguments {
            val title = args.text("title") ?: invalid("title is required")
            val start = parseTime(args.text("start"), zone) ?: invalid("start is required, as ISO 8601 with a timezone")
            val end = parseTime(args.text("end"), zone) ?: invalid("end is required, as ISO 8601 with a timezone")
            if (!end.isAfter(start)) invalid("end must be after start")
            val allDay = args.bool("all_day")
            if (allDay && !end.atZone(zone).toLocalDate().isAfter(start.atZone(zone).toLocalDate())) {
                invalid("an all-day event needs an exclusive end date after its start date")
            }
            return CalendarArguments(title, start, end, args.text("location"), args.text("notes"), allDay, args.text("calendar"))
        }
    }
}

/**
 * An ISO 8601 time with an offset, or a bare `yyyy-MM-dd` / `yyyy-MM-dd HH:mm` / `yyyy-MM-ddTHH:mm` read in the phone's
 * zone (iOS ToolDate.parse). Null for a missing value; an unreadable one is an argument error.
 */
internal fun parseTime(raw: String?, zone: ZoneId = ZoneId.systemDefault()): Instant? {
    val text = raw?.trim()?.takeIf { it.isNotEmpty() } ?: return null
    val _ = runCatching { return OffsetDateTime.parse(text).toInstant() }
    val _ = runCatching { return Instant.parse(text) }
    val _ = runCatching { return LocalDate.parse(text).atStartOfDay(zone).toInstant() }
    for (pattern in listOf("yyyy-MM-dd HH:mm", "yyyy-MM-dd'T'HH:mm", "yyyy-MM-dd'T'HH:mm:ss")) {
        val _ = runCatching { return LocalDateTime.parse(text, DateTimeFormatter.ofPattern(pattern, Locale.ROOT)).atZone(zone).toInstant() }
    }
    invalid("cannot read the time \"$raw\"; use ISO 8601 such as 2026-10-24T14:00:00+08:00")
}

/** ISO 8601 in the phone's zone with seconds and offset, as every tool documents (2026-10-24T14:00:00+08:00). */
internal fun iso(instant: Instant, zone: ZoneId = ZoneId.systemDefault()): String =
    instant.truncatedTo(ChronoUnit.SECONDS).atZone(zone).format(DateTimeFormatter.ISO_OFFSET_DATE_TIME)

internal fun iso(millis: Long): String = iso(Instant.ofEpochMilli(millis))

/** One expanded occurrence of a repeating event, in the provider's own times (all-day ones are UTC midnights). */
internal data class CalendarInstance(val begin: Long, val end: Long)

/** The provider reads and the batch a calendar change needs; faked in tests so operations can be inspected. */
internal interface CalendarBackend {
    fun event(id: Long): CalendarEventRecord?
    fun instances(eventId: Long, from: Long, to: Long): List<CalendarInstance>
    fun exceptions(seriesId: Long): List<CalendarEventRecord>
    fun attendees(eventId: Long): List<CalendarAttendee>
    fun reminders(eventId: Long): List<CalendarReminder>
    fun apply(writes: List<ProviderWrite>): Array<ContentProviderResult>
}

internal class CalendarTools(
    context: Context,
    private val backend: CalendarBackend = AndroidCalendarBackend(context),
    private val zone: ZoneId = ZoneId.systemDefault(),
) {
    private val resolver = context.contentResolver

    fun events(args: JsonObject): PhoneToolResult {
        val start = parseTime(args.text("start"), zone) ?: LocalDate.now(zone).atStartOfDay(zone).toInstant()
        val end = parseTime(args.text("end"), zone) ?: start.atZone(zone).plusDays(7).toInstant()
        if (!end.isAfter(start)) invalid("end must be after start")
        val limit = args.int("limit", 50, 1, 200)
        val query = args.text("query")?.lowercase()
        val uri = Calendar.Instances.CONTENT_URI.buildUpon().also {
            ContentUris.appendId(it, start.toEpochMilli()); ContentUris.appendId(it, end.toEpochMilli())
        }.build()
        val rows = mutableListOf<JsonObject>()
        var truncated = false
        resolver.query(uri, arrayOf(Calendar.Instances.EVENT_ID, Calendar.Instances.TITLE, Calendar.Instances.BEGIN,
            Calendar.Instances.END, Calendar.Instances.ALL_DAY, Calendar.Instances.CALENDAR_DISPLAY_NAME,
            Calendar.Instances.EVENT_LOCATION, Calendar.Instances.DESCRIPTION, Calendar.Instances.OWNER_ACCOUNT,
            Calendar.Instances.RRULE, Calendar.Instances.RDATE), null, null,
            "${Calendar.Instances.BEGIN} ASC, ${Calendar.Instances.EVENT_ID} ASC")?.use { cursor ->
            while (cursor.moveToNext()) {
                val title = cursor.getString(1).orEmpty()
                val location = cursor.getString(6).orEmpty()
                val notes = cursor.getString(7).orEmpty()
                if (query != null && listOf(title, location, notes).none { it.lowercase().contains(query) }) continue
                if (rows.size >= limit) { truncated = true; break }
                val allDay = cursor.getInt(4) != 0
                rows += buildJsonObject {
                    put("id", cursor.getLong(0).toString())
                    if (title.isNotEmpty()) put("title", title)
                    put("start", eventTime(cursor.getLong(2), allDay, zone)); put("end", eventTime(cursor.getLong(3), allDay, zone))
                    put("all_day", allDay)
                    // A repeating event needs occurrence_start to be changed or deleted: say so.
                    if (!cursor.getString(9).isNullOrBlank() || !cursor.getString(10).isNullOrBlank()) put("recurring", true)
                    cursor.getString(5)?.takeIf { it.isNotEmpty() }?.let { put("calendar", it) }
                    if (location.isNotEmpty()) put("location", location)
                    if (notes.isNotEmpty()) put("notes", notes.take(300))
                    attendees(cursor.getLong(0), cursor.getString(8)).takeIf { it.isNotEmpty() }?.let { put("attendees", it) }
                }
            }
        } ?: unavailable("系统日历暂不可用 / The calendar provider is not available on this phone.")  // i18n-ignore: tool result for the model
        return PhoneToolResult.text(buildJsonObject {
            put("start", iso(start, zone)); put("end", iso(end, zone))
            put("count", rows.size); put("truncated", truncated); put("events", JsonArray(rows))
        }.toString())
    }

    /** All-day instances are UTC midnights; report them as the local date's midnight, as the iOS app does. */
    private fun eventTime(millis: Long, allDay: Boolean, zone: ZoneId): String =
        if (!allDay) iso(Instant.ofEpochMilli(millis), zone)
        else iso(Instant.ofEpochMilli(millis).atZone(ZoneOffset.UTC).toLocalDate().atStartOfDay(zone).toInstant(), zone)

    private fun attendees(eventId: Long, owner: String?) =
        attendeesJson(queryAttendees(resolver, eventId).take(MAX_ATTENDEES), owner)

    fun create(args: JsonObject): PhoneToolResult {
        val event = CalendarArguments.parse(args, zone)
        val (id, name) = writableCalendar(event.calendar)
        val startMillis = event.startMillis(zone)
        val endMillis = event.endMillis(zone)
        val values = ContentValues().apply {
            put(Calendar.Events.CALENDAR_ID, id); put(Calendar.Events.TITLE, event.title)
            put(Calendar.Events.DTSTART, startMillis); put(Calendar.Events.DTEND, endMillis)
            put(Calendar.Events.EVENT_TIMEZONE, if (event.allDay) "UTC" else zone.id)
            put(Calendar.Events.ALL_DAY, if (event.allDay) 1 else 0)
            put(Calendar.Events.EVENT_LOCATION, event.location); put(Calendar.Events.DESCRIPTION, event.notes)
        }
        val uri = resolver.insert(Calendar.Events.CONTENT_URI, values)
            ?: unavailable("日程没有保存 / The calendar did not take the event.")  // i18n-ignore: tool result for the model
        return PhoneToolResult.text(buildJsonObject {
            put("id", ContentUris.parseId(uri).toString()); put("title", event.title)
            put("start", if (event.allDay) eventTime(startMillis, true, zone) else iso(event.start, zone))
            put("end", if (event.allDay) eventTime(endMillis, true, zone) else iso(event.end, zone))
            name?.takeIf { it.isNotEmpty() }?.let { put("calendar", it) }
        }.toString())
    }

    /**
     * The calendar named [wanted] (exact, then partial match, ignoring case), else the primary writable one, else the
     * first writable one. A phone with none gets a local Persona calendar instead of losing the event.
     */
    private fun writableCalendar(wanted: String?): Pair<Long, String?> {
        val calendars = mutableListOf<Pair<Long, String>>()
        resolver.query(Calendar.Calendars.CONTENT_URI, arrayOf(Calendar.Calendars._ID, Calendar.Calendars.CALENDAR_DISPLAY_NAME),
            "${Calendar.Calendars.CALENDAR_ACCESS_LEVEL} >= ?", arrayOf(Calendar.Calendars.CAL_ACCESS_CONTRIBUTOR.toString()),
            "${Calendar.Calendars.IS_PRIMARY} DESC, ${Calendar.Calendars._ID} ASC")?.use { cursor ->
            while (cursor.moveToNext()) calendars += cursor.getLong(0) to cursor.getString(1).orEmpty()
        } ?: unavailable("系统日历暂不可用 / The calendar provider is not available on this phone.")  // i18n-ignore: tool result for the model
        if (wanted != null) {
            val needle = wanted.lowercase()
            val match = calendars.firstOrNull { it.second.lowercase() == needle } ?: calendars.firstOrNull { it.second.lowercase().contains(needle) }
            return match ?: invalid("no calendar named \"$wanted\"; available calendars: ${calendars.map { it.second }.sorted().joinToString(", ")}")
        }
        calendars.firstOrNull()?.let { return it }
        val uri = Calendar.Calendars.CONTENT_URI.buildUpon()
            .appendQueryParameter(Calendar.CALLER_IS_SYNCADAPTER, "true")
            .appendQueryParameter(Calendar.Calendars.ACCOUNT_NAME, "Persona")
            .appendQueryParameter(Calendar.Calendars.ACCOUNT_TYPE, Calendar.ACCOUNT_TYPE_LOCAL).build()
        val values = ContentValues().apply {
            put(Calendar.Calendars.ACCOUNT_NAME, "Persona"); put(Calendar.Calendars.ACCOUNT_TYPE, Calendar.ACCOUNT_TYPE_LOCAL)
            put(Calendar.Calendars.NAME, "Persona"); put(Calendar.Calendars.CALENDAR_DISPLAY_NAME, "Persona")
            put(Calendar.Calendars.OWNER_ACCOUNT, "Persona"); put(Calendar.Calendars.CALENDAR_ACCESS_LEVEL, Calendar.Calendars.CAL_ACCESS_OWNER)
            put(Calendar.Calendars.CALENDAR_COLOR, LOCAL_CALENDAR_COLOR); put(Calendar.Calendars.VISIBLE, 1)
            put(Calendar.Calendars.SYNC_EVENTS, 1); put(Calendar.Calendars.CALENDAR_TIME_ZONE, zone.id)
        }
        val created = resolver.insert(uri, values)
            ?: unavailable("这台手机上没有可写入的日历 / No writable calendar is configured on this phone.")  // i18n-ignore: tool result for the model
        return ContentUris.parseId(created) to "Persona"
    }

    // MARK: changing and deleting (all provider reads run on the caller's IO dispatcher, never in a card's formatter)

    /**
     * The event, or the one occurrence of a repeating event, that an update or delete names. A repeating event without
     * occurrence_start is refused: its id alone names every occurrence, and guessing one changes the wrong meeting.
     * An exception (an already separate occurrence) is named by its own id.
     */
    fun resolveTarget(args: JsonObject): CalendarTargetSnapshot {
        val rawId = args.text("id") ?: invalid("id is required")
        val missing = "no event with id \"$rawId\"; call phone_calendar_events to get a current id"
        val eventId = rawId.toLongOrNull() ?: invalid(missing)
        val occurrence = parseTime(args.text("occurrence_start"), zone)
        val event = backend.event(eventId)?.takeIf { it.status != Calendar.Events.STATUS_CANCELED } ?: invalid(missing)
        event.calendarAccess?.takeIf { it < Calendar.Calendars.CAL_ACCESS_CONTRIBUTOR }?.let {
            invalid("the calendar \"${event.calendarName.orEmpty()}\" of this event is read-only")
        }
        fun startsNear(start: Long) = occurrence == null || kotlin.math.abs(
            displayEventTime(start, event.allDay, zone).toEpochMilli() - occurrence.toEpochMilli()) < OCCURRENCE_TOLERANCE_MILLIS
        val noOccurrence = "no occurrence of event \"$rawId\" starts at ${occurrence?.let { iso(it, zone) }}; " +
            "call phone_calendar_events to get current ids and starts"

        if (!event.recurring) {
            if (!startsNear(event.start)) invalid(noOccurrence)
            return CalendarTargetSnapshot(eventId, event, event, null, backend.attendees(eventId), backend.reminders(eventId))
        }
        val exception = event.takeIf { it.originalId != null }
        val series: CalendarEventRecord
        val occurrenceRecord: CalendarEventRecord
        if (exception != null) {
            if (!startsNear(exception.start)) invalid(noOccurrence)
            series = backend.event(exception.originalId!!) ?: invalid("the repeating event of this occurrence no longer exists")
            occurrenceRecord = exception
        } else {
            val requested = occurrence ?: invalid("\"${event.title.ifEmpty { rawId }}\" repeats: pass occurrence_start (the start of the " +
                "occurrence from phone_calendar_events) and span this or future")
            val center = requested.toEpochMilli()
            val hits = backend.instances(eventId, center - WINDOW_MILLIS, center + WINDOW_MILLIS).filter { startsNear(it.begin) }
            val instance = hits.singleOrNull() ?: invalid(
                if (hits.isEmpty()) noOccurrence else "more than one occurrence of event \"$rawId\" starts at " +
                    "${iso(requested, zone)}; call phone_calendar_events to get current ids and starts")
            series = event
            occurrenceRecord = event.copy(start = instance.begin, end = instance.end, duration = null)
        }
        val selected = exception?.originalInstanceTime ?: occurrenceRecord.start
        val later = backend.exceptions(series.id).filter { (it.originalInstanceTime ?: Long.MIN_VALUE) >= selected }.sortedBy { it.id }
        val source = exception ?: series
        return CalendarTargetSnapshot(eventId, series, occurrenceRecord, exception,
            backend.attendees(source.id), backend.reminders(source.id), later)
    }

    fun prepareUpdate(args: JsonObject): CalendarUpdatePreparation {
        val snapshot = resolveTarget(args)
        val span = EventSpan.parse(args)
        val (change, after) = CalendarEventChange.parse(args, snapshot.occurrence, zone)
        return CalendarUpdatePreparation(args, snapshot, span, change, after, zone)
            .also { CalendarWrites.update(it) } // unsupported changes fail here, before anyone is asked
    }

    fun prepareDelete(args: JsonObject): CalendarDeletePreparation {
        val snapshot = resolveTarget(args)
        return CalendarDeletePreparation(args, snapshot, EventSpan.parse(args), zone)
            .also { CalendarWrites.delete(it) }
    }

    /**
     * The event [id] that a quick result's Undo takes back, deleted as a whole with no second before → after card
     * (iOS `CalendarTool.remove`): the user is undoing the assistant's own write. A repeating event is refused like any
     * other delete without its occurrence, so a card whose event repeats leaves the event alone.
     */
    fun remove(id: Long): PhoneToolResult = delete(prepareDelete(buildJsonObject { put("id", id.toString()) }))

    fun update(prepared: CalendarUpdatePreparation): PhoneToolResult {
        val plan = beforeWrite {
            requireUnchanged(prepared.args, prepared.snapshot)
            CalendarWrites.update(prepared)
        }
        val results = backend.apply(plan.writes)
        val id = plan.resultEventId ?: ContentUris.parseId(requireNotNull(results[requireNotNull(plan.resultInsertIndex)].uri))
        val record = backend.event(id) ?: error("The changed event could not be read back")
        return PhoneToolResult.text(eventJson(record, prepared.span, prepared.zone).toString())
    }

    fun delete(prepared: CalendarDeletePreparation): PhoneToolResult {
        val plan = beforeWrite {
            requireUnchanged(prepared.args, prepared.snapshot)
            CalendarWrites.delete(prepared)
        }
        backend.apply(plan.writes)
        val shown = prepared.snapshot.occurrence
        return PhoneToolResult.text(buildJsonObject {
            put("id", prepared.snapshot.requestedId.toString())
            if (shown.title.isNotEmpty()) put("title", shown.title)
            put("start", iso(displayEventTime(shown.start, shown.allDay, prepared.zone), prepared.zone))
            put("span", prepared.span.wire)
            put("deleted", true)
        }.toString())
    }

    /** The event group is read again as a whole and compared field by field with what the user approved. */
    private fun requireUnchanged(args: JsonObject, approved: CalendarTargetSnapshot) {
        val current = resolveTarget(args)
        if (current != approved) throw PhoneToolWriteRefusal(staleEventMessage)
    }

    /** The changed event as phone_calendar_events lists it, plus the span that was applied. */
    private fun eventJson(record: CalendarEventRecord, span: EventSpan, zone: ZoneId) = buildJsonObject {
        put("id", record.id.toString())
        if (record.title.isNotEmpty()) put("title", record.title)
        put("start", iso(displayEventTime(record.start, record.allDay, zone), zone))
        put("end", iso(displayEventTime(record.effectiveEnd, record.allDay, zone), zone))
        put("all_day", record.allDay)
        if (record.recurring) put("recurring", true)
        record.calendarName?.takeIf { it.isNotEmpty() }?.let { put("calendar", it) }
        record.location?.takeIf { it.isNotEmpty() }?.let { put("location", it) }
        record.notes?.takeIf { it.isNotEmpty() }?.let { put("notes", it.take(300)) }
        attendeesJson(backend.attendees(record.id).take(MAX_ATTENDEES), record.ownerAccount).takeIf { it.isNotEmpty() }
            ?.let { put("attendees", it) }
        put("span", span.wire)
    }

    private companion object {
        const val MAX_ATTENDEES = 100
        const val OCCURRENCE_TOLERANCE_MILLIS = 60_000L
        /** Search window around an occurrence's start; wide enough for the UTC midnights of all-day instances. */
        const val WINDOW_MILLIS = 2 * 86_400_000L
        const val staleEventMessage = "日程在确认后已变化，未写入 / The event changed after it was shown for confirmation. " + // i18n-ignore: tool result for the model
            "Nothing was written. Call phone_calendar_events and prepare a fresh confirmation instead of retrying this call."
        /** The local calendar's colour in the system calendar app (data, not app UI). */
        const val LOCAL_CALENDAR_COLOR = 0xff3f51b5.toInt()
    }
}

/** EventKit's participant roles (iOS): the organizer chairs; optional and resource attendees keep their kind. */
internal fun attendeeRole(relationship: Int, type: Int): String = when {
    relationship == Calendar.Attendees.RELATIONSHIP_ORGANIZER -> "chair"
    type == Calendar.Attendees.TYPE_OPTIONAL -> "optional"
    type == Calendar.Attendees.TYPE_RESOURCE -> "non_participant"
    type == Calendar.Attendees.TYPE_REQUIRED -> "required"
    else -> "unknown"
}

internal fun attendeeStatus(status: Int): String = when (status) {
    Calendar.Attendees.ATTENDEE_STATUS_ACCEPTED -> "accepted"
    Calendar.Attendees.ATTENDEE_STATUS_DECLINED -> "declined"
    Calendar.Attendees.ATTENDEE_STATUS_TENTATIVE -> "tentative"
    Calendar.Attendees.ATTENDEE_STATUS_INVITED -> "pending"
    else -> "unknown"
}

internal fun attendeesJson(attendees: List<CalendarAttendee>, owner: String?) = buildJsonArray {
    for (attendee in attendees) add(buildJsonObject {
        attendee.name?.takeIf { it.isNotBlank() }?.let { put("name", it) }
        val email = attendee.email?.takeIf { it.isNotBlank() }
        email?.let { put("email", it) }
        put("role", attendeeRole(attendee.relationship, attendee.type))
        put("status", attendeeStatus(attendee.status))
        put("is_current_user", email != null && owner != null && email.equals(owner, ignoreCase = true))
    })
}

internal fun queryAttendees(resolver: android.content.ContentResolver, eventId: Long): List<CalendarAttendee> = buildList {
    resolver.query(Calendar.Attendees.CONTENT_URI,
        arrayOf(Calendar.Attendees.ATTENDEE_NAME, Calendar.Attendees.ATTENDEE_EMAIL, Calendar.Attendees.ATTENDEE_RELATIONSHIP,
            Calendar.Attendees.ATTENDEE_TYPE, Calendar.Attendees.ATTENDEE_STATUS, Calendar.Attendees.ATTENDEE_IDENTITY,
            Calendar.Attendees.ATTENDEE_ID_NAMESPACE),
        "${Calendar.Attendees.EVENT_ID} = ?", arrayOf(eventId.toString()), "Attendees.${Calendar.Attendees._ID} ASC")?.use { cursor ->
        while (cursor.moveToNext()) add(CalendarAttendee(cursor.getString(0), cursor.getString(1), cursor.getInt(2),
            cursor.getInt(3), cursor.getInt(4), cursor.getString(5), cursor.getString(6)))
    }
}

private class AndroidCalendarBackend(context: Context) : CalendarBackend {
    private val resolver = context.applicationContext.contentResolver

    override fun event(id: Long): CalendarEventRecord? {
        val cursor = resolver.query(ContentUris.withAppendedId(Calendar.Events.CONTENT_URI, id), PROJECTION, null, null, null)
            ?: unavailable("系统日历暂不可用 / The calendar provider is not available on this phone.")  // i18n-ignore: tool result for the model
        return cursor.use { if (it.moveToFirst()) record(it) else null }
    }

    override fun exceptions(seriesId: Long): List<CalendarEventRecord> {
        val cursor = resolver.query(Calendar.Events.CONTENT_URI, PROJECTION, "${Calendar.Events.ORIGINAL_ID} = ?",
            arrayOf(seriesId.toString()), "${Calendar.Events._ID} ASC")
            ?: unavailable("系统日历暂不可用 / The calendar provider is not available on this phone.")  // i18n-ignore: tool result for the model
        return cursor.use { buildList { while (it.moveToNext()) add(record(it)) } }
    }

    override fun instances(eventId: Long, from: Long, to: Long): List<CalendarInstance> {
        val uri = Calendar.Instances.CONTENT_URI.buildUpon().also {
            ContentUris.appendId(it, from); ContentUris.appendId(it, to)
        }.build()
        val cursor = resolver.query(uri, arrayOf(Calendar.Instances.BEGIN, Calendar.Instances.END),
            "${Calendar.Instances.EVENT_ID} = ?", arrayOf(eventId.toString()), "${Calendar.Instances.BEGIN} ASC")
            ?: unavailable("系统日历暂不可用 / The calendar provider is not available on this phone.")  // i18n-ignore: tool result for the model
        return cursor.use { buildList { while (it.moveToNext()) add(CalendarInstance(it.getLong(0), it.getLong(1))) } }
    }

    override fun attendees(eventId: Long): List<CalendarAttendee> = queryAttendees(resolver, eventId)

    override fun reminders(eventId: Long): List<CalendarReminder> = buildList {
        resolver.query(Calendar.Reminders.CONTENT_URI, arrayOf(Calendar.Reminders.MINUTES, Calendar.Reminders.METHOD),
            "${Calendar.Reminders.EVENT_ID} = ?", arrayOf(eventId.toString()), "Reminders.${Calendar.Reminders._ID} ASC")?.use { cursor ->
            while (cursor.moveToNext()) add(CalendarReminder(cursor.getInt(0), cursor.getInt(1)))
        }
    }

    override fun apply(writes: List<ProviderWrite>): Array<ContentProviderResult> =
        resolver.applyProviderWrites(Calendar.AUTHORITY, writes)

    private fun record(c: android.database.Cursor): CalendarEventRecord {
        fun string(i: Int) = c.getString(i)
        fun long(i: Int) = if (c.isNull(i)) null else c.getLong(i)
        fun flag(i: Int) = if (c.isNull(i)) null else c.getInt(i) != 0
        return CalendarEventRecord(
            id = c.getLong(0), calendarId = c.getLong(1), title = string(2).orEmpty(),
            start = c.getLong(3), end = long(4), duration = string(5),
            timezone = string(6).orEmpty(), endTimezone = string(7), allDay = c.getInt(8) != 0,
            rrule = string(9), rdate = string(10), exrule = string(11), exdate = string(12),
            location = string(13), notes = string(14),
            status = if (c.isNull(15)) Calendar.Events.STATUS_CONFIRMED else c.getInt(15),
            originalId = long(16), originalInstanceTime = long(17), originalAllDay = flag(18),
            organizer = string(19), availability = long(20)?.toInt(), accessLevel = long(21)?.toInt(),
            guestsCanInviteOthers = flag(22), guestsCanModify = flag(23), guestsCanSeeGuests = flag(24),
            calendarName = string(25), ownerAccount = string(26), calendarAccess = long(27)?.toInt(),
            hasSyncId = !string(28).isNullOrEmpty(),
        )
    }

    private companion object {
        val PROJECTION = arrayOf(
            Calendar.Events._ID, Calendar.Events.CALENDAR_ID, Calendar.Events.TITLE, Calendar.Events.DTSTART,
            Calendar.Events.DTEND, Calendar.Events.DURATION, Calendar.Events.EVENT_TIMEZONE,
            Calendar.Events.EVENT_END_TIMEZONE, Calendar.Events.ALL_DAY, Calendar.Events.RRULE, Calendar.Events.RDATE,
            Calendar.Events.EXRULE, Calendar.Events.EXDATE, Calendar.Events.EVENT_LOCATION, Calendar.Events.DESCRIPTION,
            Calendar.Events.STATUS, Calendar.Events.ORIGINAL_ID, Calendar.Events.ORIGINAL_INSTANCE_TIME,
            Calendar.Events.ORIGINAL_ALL_DAY, Calendar.Events.ORGANIZER, Calendar.Events.AVAILABILITY,
            Calendar.Events.ACCESS_LEVEL, Calendar.Events.GUESTS_CAN_INVITE_OTHERS, Calendar.Events.GUESTS_CAN_MODIFY,
            Calendar.Events.GUESTS_CAN_SEE_GUESTS, Calendar.Events.CALENDAR_DISPLAY_NAME, Calendar.Events.OWNER_ACCOUNT,
            Calendar.Events.CALENDAR_ACCESS_LEVEL, Calendar.Events._SYNC_ID,
        )
    }
}
