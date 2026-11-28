package persona.platform.phonetools

import android.content.ContentProviderResult
import android.content.ContentUris
import android.content.Context
import android.provider.CalendarContract
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import java.time.Instant
import java.time.ZoneId

/**
 * Calendar update / delete over a fake provider: it proves the operations that are generated, their order and back
 * references, and the refusal paths. It does not prove that an OEM Calendar provider accepts them: that is the real
 * provider acceptance listed in the lane report.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class CalendarWriteToolsTest {
    private val zone = ZoneId.of("Asia/Shanghai")
    private val backend = FakeCalendarBackend()
    private val tools get() = CalendarTools(ApplicationProvider.getApplicationContext<Context>(), backend, zone)

    private fun ms(iso: String) = Instant.parse(iso).toEpochMilli()
    private fun args(vararg pairs: Pair<String, Any>) = buildJsonObject {
        for ((key, value) in pairs) when (value) {
            is Boolean -> put(key, value)
            else -> put(key, value.toString())
        }
    }

    private val plain = CalendarEventRecord(
        id = 1, calendarId = 7, title = "Weekly sync", start = ms("2026-11-06T07:00:00Z"), end = ms("2026-11-06T08:00:00Z"),
        duration = null, timezone = "Asia/Shanghai", endTimezone = null, allDay = false, rrule = null, rdate = null,
        exrule = null, exdate = null, location = "3F", notes = "agenda", status = CalendarContract.Events.STATUS_CONFIRMED,
        originalId = null, originalInstanceTime = null, originalAllDay = null, organizer = "owner@example.com",
        availability = 0, accessLevel = 0, guestsCanInviteOthers = null, guestsCanModify = null, guestsCanSeeGuests = null,
        calendarName = "Work", ownerAccount = "owner@example.com", calendarAccess = CalendarContract.Calendars.CAL_ACCESS_OWNER,
    )

    /** A daily series at 15:00 (+08:00) from 2026-11-01, one hour long. */
    private val daily = plain.copy(
        id = 2, title = "Standup", start = ms("2026-11-01T07:00:00Z"), end = null, duration = "PT1H",
        rrule = "FREQ=DAILY;COUNT=10",
    )
    private val fourth = ms("2026-11-04T07:00:00Z")
    private val fourthIso = "2026-11-04T15:00:00+08:00"

    private fun seedDaily(series: CalendarEventRecord = daily) {
        backend.events[series.id] = series
        backend.instances[series.id] = (0 until 10).map {
            val begin = series.start + it * 86_400_000L
            CalendarInstance(begin, begin + 3_600_000L)
        }
    }

    @Test fun aPlainEventChangesOnlyTheApprovedValuesAndKeepsItsDuration() {
        backend.events[1] = plain
        val prepared = tools.prepareUpdate(args("id" to "1", "start" to "2026-11-06T17:00:00+08:00"))
        assertEquals(EventSpan.This, prepared.span)
        val result = tools.update(prepared)
        val write = backend.batches.single().single()
        assertEquals(ProviderWrite.Kind.Update, write.kind)
        assertEquals(1L, write.id)
        assertEquals(mapOf(
            CalendarContract.Events.DTSTART to ms("2026-11-06T09:00:00Z"),
            CalendarContract.Events.DTEND to ms("2026-11-06T10:00:00Z"),
            CalendarContract.Events.DURATION to null,
        ), write.values)
        val body = Json.parseToJsonElement(result.content.single().jsonObject.getValue("text").jsonPrimitive.content).jsonObject
        assertEquals("1", body.getValue("id").jsonPrimitive.content)
        assertEquals("this", body.getValue("span").jsonPrimitive.content)
        assertFalse(body.containsKey("recurring"))
    }

    @Test fun anEmptyLocationOrNoteRemovesItAndAnAbsentOneIsLeftAlone() {
        backend.events[1] = plain
        tools.update(tools.prepareUpdate(args("id" to "1", "location" to "", "notes" to "")))
        assertEquals(mapOf(CalendarContract.Events.EVENT_LOCATION to null, CalendarContract.Events.DESCRIPTION to null),
            backend.batches.single().single().values)
    }

    @Test fun allDayFalseIsDifferentFromNotPassingIt() {
        val allDay = plain.copy(start = ms("2026-11-03T00:00:00Z"), end = ms("2026-11-04T00:00:00Z"), timezone = "UTC", allDay = true)
        backend.events[1] = allDay
        tools.update(tools.prepareUpdate(args("id" to "1", "title" to "Trip")))
        assertEquals(mapOf(CalendarContract.Events.TITLE to "Trip"), backend.batches.last().single().values)

        tools.update(tools.prepareUpdate(args("id" to "1", "all_day" to false)))
        val values = backend.batches.last().single().values
        assertEquals(0, values[CalendarContract.Events.ALL_DAY])
        assertEquals("Asia/Shanghai", values[CalendarContract.Events.EVENT_TIMEZONE])
        // The all-day span becomes the same local midnights as timed values.
        assertEquals(ms("2026-11-02T16:00:00Z"), values[CalendarContract.Events.DTSTART])
        assertEquals(ms("2026-11-03T16:00:00Z"), values[CalendarContract.Events.DTEND])
    }

    @Test fun anUpdateThatChangesNothingOrBreaksTimeIsRefusedBeforeAnyCard() {
        backend.events[1] = plain
        fun failure(vararg pairs: Pair<String, Any>) =
            (runCatching { tools.prepareUpdate(args("id" to "1", *pairs)) }.exceptionOrNull() as PhoneToolFailure).message
        assertEquals("nothing to change: pass title, start, end, location, notes or all_day", failure())
        assertEquals("nothing to change: the event already has these values", failure("title" to "Weekly sync"))
        assertEquals("end must be after start", failure("end" to "2026-11-06T14:00:00+08:00"))
        assertEquals("span must be this or future, not \"all\"", failure("title" to "x", "span" to "all"))
        assertEquals("no event with id \"9\"; call phone_calendar_events to get a current id",
            (runCatching { tools.prepareUpdate(args("id" to "9", "title" to "x")) }.exceptionOrNull() as PhoneToolFailure).message)
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun aReadOnlyCalendarIsRefusedBeforeTheCard() {
        backend.events[1] = plain.copy(calendarAccess = CalendarContract.Calendars.CAL_ACCESS_READ)
        val failure = runCatching { tools.prepareDelete(args("id" to "1")) }.exceptionOrNull() as PhoneToolFailure
        assertTrue(failure.message, failure.message!!.contains("read-only"))
    }

    @Test fun aRepeatingEventNeedsItsOccurrenceAndAnExactOne() {
        seedDaily()
        fun failure(vararg pairs: Pair<String, Any>) =
            (runCatching { tools.prepareDelete(args("id" to "2", *pairs)) }.exceptionOrNull() as PhoneToolFailure).message!!
        assertTrue(failure().contains("repeats: pass occurrence_start"))
        // Close to an occurrence is not the occurrence: the nearest one is never guessed.
        assertTrue(failure("occurrence_start" to "2026-11-04T15:05:00+08:00").contains("no occurrence of event \"2"))
        assertTrue(failure("occurrence_start" to "2026-12-04T15:00:00+08:00").contains("no occurrence of event \"2"))
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun anExceptionOfASeriesWithoutASyncIdIsRefusedBeforeAnyWrite() {
        // The provider links an exception to its series through the sync id; a local calendar has none (measured on a device).
        seedDaily(daily.copy(hasSyncId = false))
        val change = runCatching { tools.prepareUpdate(args("id" to "2", "occurrence_start" to fourthIso, "title" to "Moved")) }.exceptionOrNull() as PhoneToolFailure
        assertTrue(change.message!!.contains("does not sync"))
        val cancel = runCatching { tools.prepareDelete(args("id" to "2", "occurrence_start" to fourthIso)) }.exceptionOrNull() as PhoneToolFailure
        assertTrue(cancel.message!!.contains("does not sync"))
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun twoInstancesAtTheSameStartAreAmbiguousAndRefused() {
        seedDaily()
        backend.instances[2] = backend.instances.getValue(2) + CalendarInstance(fourth, fourth + 1_800_000L)
        val failure = runCatching { tools.prepareDelete(args("id" to "2", "occurrence_start" to fourthIso)) }.exceptionOrNull() as PhoneToolFailure
        assertTrue(failure.message!!.contains("more than one occurrence"))
    }

    @Test fun changingOneOccurrenceCreatesAnExceptionAndLeavesTheSeriesRowsToTheProvider() {
        seedDaily()
        val attendee = CalendarAttendee("Guest", "guest@example.com", 0, 1, 1)
        backend.attendeeRows[2] = listOf(attendee)
        backend.reminderRows[2] = listOf(CalendarReminder(10, 1))
        backend.events[900] = daily.copy(id = 900, title = "Moved", rrule = null, duration = null, start = fourth,
            end = fourth + 3_600_000L, originalId = 2, originalInstanceTime = fourth)
        val result = tools.update(tools.prepareUpdate(args("id" to "2", "occurrence_start" to fourthIso, "title" to "Moved")))
        val writes = backend.batches.single()
        // The provider copies attendees and reminders onto an exception by itself (measured on the AOSP provider): writing
        // them again would double them, so the batch is the exception alone.
        assertEquals(1, writes.size)
        val exception = writes[0]
        assertEquals(ProviderWrite.Kind.Insert, exception.kind)
        assertEquals(ContentUris.withAppendedId(CalendarContract.Events.CONTENT_EXCEPTION_URI, 2), exception.uri)
        assertEquals(fourth, exception.values[CalendarContract.Events.ORIGINAL_INSTANCE_TIME])
        assertEquals("Moved", exception.values[CalendarContract.Events.TITLE])
        assertEquals(fourth, exception.values[CalendarContract.Events.DTSTART])
        // The exception stands alone for one occurrence: no recurrence keys. The provider refuses a DTEND on an exception
        // ("Exceptions can't overwrite dtend") and an explicit calendar id, so it carries its length as a DURATION.
        assertEquals("PT1H", exception.values[CalendarContract.Events.DURATION])
        for (key in listOf(CalendarContract.Events.RRULE, CalendarContract.Events.RDATE, CalendarContract.Events.EXDATE,
            CalendarContract.Events.EXRULE, CalendarContract.Events.DTEND, CalendarContract.Events.CALENDAR_ID)) {
            assertFalse(key, exception.values.containsKey(key))
        }
        // The answer reads the new exception back.
        val body = Json.parseToJsonElement(result.content.single().jsonObject.getValue("text").jsonPrimitive.content).jsonObject
        assertEquals("900", body.getValue("id").jsonPrimitive.content)
        assertEquals("Moved", body.getValue("title").jsonPrimitive.content)
    }

    @Test fun anAlreadySeparateOccurrenceIsUpdatedByItsOwnId() {
        seedDaily()
        backend.events[900] = daily.copy(id = 900, title = "Moved", rrule = null, duration = null, start = fourth,
            end = fourth + 3_600_000L, originalId = 2, originalInstanceTime = fourth)
        // Named by its own id: occurrence_start is optional.
        tools.update(tools.prepareUpdate(args("id" to "900", "location" to "Room 2")))
        val write = backend.batches.single().single()
        assertEquals(900L, write.id)
        assertEquals(mapOf(CalendarContract.Events.EVENT_LOCATION to "Room 2"), write.values)
    }

    @Test fun theChatsUndoDeletesTheEventByItsOwnIdWithNoConfirmation() {
        backend.events[1] = plain
        // #9340: the quick result's Undo takes the event it names off the phone, as iOS CalendarTool.remove does.
        val result = tools.remove(1)
        val write = backend.batches.single().single()
        assertEquals(ProviderWrite.Kind.Delete, write.kind)
        assertEquals(1L, write.id)
        val body = Json.parseToJsonElement(result.content.single().jsonObject.getValue("text").jsonPrimitive.content).jsonObject
        assertEquals("1", body.getValue("id").jsonPrimitive.content)
        assertEquals(true, body.getValue("deleted").jsonPrimitive.content.toBoolean())
    }

    @Test fun theChatsUndoRefusesAnEventThisPhoneDoesNotHave() {
        // The card can come from another phone's write or from an event deleted since: nothing is written, the chat says so.
        val failure = runCatching { tools.remove(42) }.exceptionOrNull() as PhoneToolFailure
        assertTrue(failure.message!!.contains("no event with id \"42\""))
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun deletingOneOccurrenceCancelsOnlyThatOne() {
        seedDaily()
        tools.delete(tools.prepareDelete(args("id" to "2", "occurrence_start" to fourthIso)))
        val write = backend.batches.single().single()
        assertEquals(ProviderWrite.Kind.Insert, write.kind)
        assertEquals(ContentUris.withAppendedId(CalendarContract.Events.CONTENT_EXCEPTION_URI, 2), write.uri)
        assertEquals(mapOf(
            CalendarContract.Events.ORIGINAL_INSTANCE_TIME to fourth,
            CalendarContract.Events.STATUS to CalendarContract.Events.STATUS_CANCELED,
        ), write.values)
    }

    @Test fun deletingAnExistingExceptionCancelsItInsteadOfRevivingTheSeriesInstance() {
        seedDaily()
        backend.events[900] = daily.copy(id = 900, rrule = null, duration = null, start = fourth, end = fourth + 3_600_000L,
            originalId = 2, originalInstanceTime = fourth)
        tools.delete(tools.prepareDelete(args("id" to "900")))
        val write = backend.batches.single().single()
        assertEquals(ProviderWrite.Kind.Update, write.kind)
        assertEquals(900L, write.id)
        assertEquals(mapOf(CalendarContract.Events.STATUS to CalendarContract.Events.STATUS_CANCELED), write.values)
    }

    @Test fun deletingThisAndFutureEndsTheSeriesBeforeTheSelectedOccurrenceAndKeepsThePast() {
        seedDaily()
        val result = tools.delete(tools.prepareDelete(args("id" to "2", "occurrence_start" to fourthIso, "span" to "future")))
        val write = backend.batches.single().single()
        assertEquals(ProviderWrite.Kind.Update, write.kind)
        assertEquals(2L, write.id)
        // COUNT would keep running after the cut; UNTIL stops the series just before the selected occurrence.
        assertEquals("FREQ=DAILY;UNTIL=20261104T065959Z", write.values[CalendarContract.Events.RRULE])
        val body = Json.parseToJsonElement(result.content.single().jsonObject.getValue("text").jsonPrimitive.content).jsonObject
        assertEquals("future", body.getValue("span").jsonPrimitive.content)
        assertEquals(true, body.getValue("deleted").jsonPrimitive.content.toBoolean())
    }

    @Test fun deletingThisAndFutureFromTheFirstOccurrenceRemovesTheWholeSeriesAndItsLaterExceptions() {
        seedDaily()
        backend.events[900] = daily.copy(id = 900, rrule = null, duration = null, start = fourth, end = fourth + 3_600_000L,
            originalId = 2, originalInstanceTime = fourth)
        tools.delete(tools.prepareDelete(args("id" to "2", "occurrence_start" to "2026-11-01T15:00:00+08:00", "span" to "future")))
        val writes = backend.batches.single()
        assertEquals(listOf(900L to ProviderWrite.Kind.Delete, 2L to ProviderWrite.Kind.Delete), writes.map { it.id to it.kind })
    }

    @Test fun changingThisAndFutureSplitsTheSeriesAndStartsANewOneWithTheChange() {
        seedDaily()
        backend.attendeeRows[2] = listOf(CalendarAttendee("Guest", "guest@example.com", 0, 1, 1))
        backend.reminderRows[2] = listOf(CalendarReminder(15, 1))
        backend.events[900] = daily.copy(id = 900, title = "Standup (new)")
        tools.update(tools.prepareUpdate(args("id" to "2", "occurrence_start" to fourthIso, "span" to "future",
            "title" to "Standup (new)", "start" to "2026-11-04T16:00:00+08:00")))
        val writes = backend.batches.single()
        // New series first, so the attendee and reminder rows can refer to it; then the old series is cut.
        val created = writes[0]
        assertEquals(ProviderWrite.Kind.Insert, created.kind)
        assertEquals(CalendarContract.Events.CONTENT_URI, created.uri)
        assertEquals("Standup (new)", created.values[CalendarContract.Events.TITLE])
        assertEquals(ms("2026-11-04T08:00:00Z"), created.values[CalendarContract.Events.DTSTART])
        assertNull(created.values[CalendarContract.Events.DTEND])
        assertEquals("PT1H", created.values[CalendarContract.Events.DURATION])
        // COUNT=10 with three occurrences already behind the selected one: seven remain.
        assertEquals("FREQ=DAILY;COUNT=7", created.values[CalendarContract.Events.RRULE])
        assertEquals(mapOf(CalendarContract.Attendees.EVENT_ID to 0), writes[1].backReferences)
        assertEquals(mapOf(CalendarContract.Reminders.EVENT_ID to 0), writes[2].backReferences)
        val cut = writes[3]
        assertEquals(ProviderWrite.Kind.Update, cut.kind)
        assertEquals(2L, cut.id)
        assertEquals("FREQ=DAILY;UNTIL=20261104T065959Z", cut.values[CalendarContract.Events.RRULE])
    }

    @Test fun anUntilRuleKeepsItsEndAndAnAllDaySeriesEndsOnADate() {
        val until = daily.copy(rrule = "FREQ=DAILY;UNTIL=20261110T070000Z")
        seedDaily(until)
        // The new series the split inserts is read back after the batch (the fake's first fresh id is 900).
        backend.events[900] = until.copy(id = 900, location = "Room 9", start = fourth)
        tools.update(tools.prepareUpdate(args("id" to "2", "occurrence_start" to fourthIso, "span" to "future", "location" to "Room 9")))
        val created = backend.batches.single()[0]
        assertEquals("FREQ=DAILY;UNTIL=20261110T070000Z", created.values[CalendarContract.Events.RRULE])

        val allDay = daily.copy(start = ms("2026-11-01T00:00:00Z"), timezone = "UTC", allDay = true, duration = "P1D",
            rrule = "FREQ=DAILY;COUNT=5")
        backend.batches.clear()
        backend.events[2] = allDay
        backend.instances[2] = (0 until 5).map { CalendarInstance(ms("2026-11-01T00:00:00Z") + it * 86_400_000L,
            ms("2026-11-02T00:00:00Z") + it * 86_400_000L) }
        tools.delete(tools.prepareDelete(args("id" to "2", "occurrence_start" to "2026-11-03T00:00:00+08:00", "span" to "future")))
        assertEquals("FREQ=DAILY;UNTIL=20261102", backend.batches.single().single().values[CalendarContract.Events.RRULE])
    }

    @Test fun aWeeklyCountSplitFollowsWallClockTimeAcrossADaylightSavingChange() {
        val berlin = ZoneId.of("Europe/Berlin")
        // Mondays 10:00 local from 2026-10-19; clocks go back on 2026-10-25, so the later Mondays are 09:00Z.
        val weekly = plain.copy(id = 3, start = ms("2026-10-19T08:00:00Z"), end = null, duration = "PT1H",
            timezone = "Europe/Berlin", rrule = "FREQ=WEEKLY;COUNT=6")
        val selected = ms("2026-11-02T09:00:00Z")
        val split = RecurrenceEdit.split(weekly, selected, selected, berlin)
        assertEquals("FREQ=WEEKLY;COUNT=4", split.newRrule)
        assertEquals("FREQ=WEEKLY;UNTIL=20261102T085959Z", split.oldRrule)
        assertFalse(split.deleteWholeOldSeries)
    }

    @Test fun extraAndExcludedDatesAreDividedBetweenThePastAndTheFuture() {
        val series = daily.copy(rrule = "FREQ=DAILY", rdate = "20261201T070000Z,20261103T070000Z", exdate = "20261102T070000Z,20261107T070000Z")
        val selected = ms("2026-11-04T07:00:00Z")
        val newStart = ms("2026-11-04T08:00:00Z") // one hour later
        val split = RecurrenceEdit.split(series, selected, newStart, zone)
        assertEquals("20261103T070000Z", split.oldRdate)
        assertEquals("20261102T070000Z", split.oldExdate)
        assertEquals("20261201T080000Z", split.newRdate)
        assertEquals("20261107T080000Z", split.newExdate)
    }

    @Test fun floatingRecurrenceDatesAreReadInTheSeriesZoneNotThePhones() {
        // Berlin is UTC+1 in November, so 10:00 local is 09:00Z; the phone (Shanghai) must not be used to read the dates.
        val berlin = daily.copy(timezone = "Europe/Berlin", rrule = "FREQ=DAILY", exdate = "20261102T100000,20261107T100000")
        val selected = ms("2026-11-04T09:00:00Z")
        val split = RecurrenceEdit.split(berlin, selected, selected + 3_600_000L, zone)
        assertEquals("20261102T100000", split.oldExdate)
        assertEquals("20261107T110000", split.newExdate)
    }

    @Test fun aZonePrefixedDateListKeepsItsPrefixWhateverIsDropped() {
        val selected = ms("2026-11-04T09:00:00Z")
        val shifted = selected + 3_600_000L
        // "ZONE;dates" is how the Android provider stores a zoned list; "TZID=ZONE:dates" is the iCalendar form.
        val provider = daily.copy(timezone = "Europe/Berlin", rrule = "FREQ=DAILY",
            exdate = "Europe/Berlin;20261102T100000,20261107T100000")
        val providerSplit = RecurrenceEdit.split(provider, selected, shifted, zone)
        assertEquals("Europe/Berlin;20261102T100000", providerSplit.oldExdate)
        assertEquals("Europe/Berlin;20261107T110000", providerSplit.newExdate)

        val icalendar = daily.copy(timezone = "Europe/Berlin", rrule = "FREQ=DAILY",
            rdate = "TZID=Europe/Berlin:20261201T100000,20261103T100000")
        val icalendarSplit = RecurrenceEdit.split(icalendar, selected, shifted, zone)
        // The first item belongs to the future and is dropped from the past list: the prefix must stay with the survivor.
        assertEquals("TZID=Europe/Berlin:20261103T100000", icalendarSplit.oldRdate)
        assertEquals("TZID=Europe/Berlin:20261201T110000", icalendarSplit.newRdate)

        val unknown = daily.copy(rrule = "FREQ=DAILY", exdate = "Nowhere/Land;20261102T100000")
        val failure = runCatching { RecurrenceEdit.split(unknown, selected, selected, zone) }.exceptionOrNull() as PhoneToolFailure
        assertTrue(failure.message, failure.message!!.contains("time zone"))
    }

    @Test fun anAllDaySeriesReadsItsDateListsAsUtcDates() {
        val allDay = daily.copy(start = ms("2026-11-01T00:00:00Z"), timezone = "UTC", allDay = true, duration = "P1D",
            rrule = "FREQ=DAILY", exdate = "20261102,20261107")
        val selected = ms("2026-11-04T00:00:00Z")
        val split = RecurrenceEdit.split(allDay, selected, selected, ZoneId.of("America/New_York"))
        assertEquals("20261102", split.oldExdate)
        assertEquals("20261107", split.newExdate)
    }

    @Test fun aDateOnlyUntilOnATimedSeriesIncludesThatWholeDay() {
        // UNTIL=20261104 still allows the 15:00 occurrence on the 4th: it must be cut, not kept next to the new series.
        val includesSelected = daily.copy(rrule = "FREQ=DAILY;UNTIL=20261104")
        assertEquals("FREQ=DAILY;UNTIL=20261104T065959Z", RecurrenceEdit.split(includesSelected, fourth, fourth, zone).oldRrule)
        assertEquals("FREQ=DAILY;UNTIL=20261104T065959Z", RecurrenceEdit.truncateOnly(includesSelected, fourth, zone).oldRrule)
        // A date before the selected occurrence already ends the series earlier: left alone.
        val earlier = daily.copy(rrule = "FREQ=DAILY;UNTIL=20261103")
        assertEquals("FREQ=DAILY;UNTIL=20261103", RecurrenceEdit.split(earlier, fourth, fourth, zone).oldRrule)
    }

    @Test fun anAllDayEventKeepsItsNumberOfDaysWhenOnlyTheStartMovesAcrossAClockChange() {
        val newYork = ZoneId.of("America/New_York")
        val local = CalendarTools(ApplicationProvider.getApplicationContext<Context>(), backend, newYork)
        fun midnight(day: String) = ms("${day}T00:00:00Z")
        fun allDayEvent(from: String, to: String) =
            plain.copy(start = midnight(from), end = midnight(to), timezone = "UTC", allDay = true)
        fun moved(event: CalendarEventRecord, newStart: String): Map<String, Any?> {
            backend.events[1] = event
            backend.batches.clear()
            local.update(local.prepareUpdate(args("id" to "1", "start" to newStart)))
            return backend.batches.single().single().values
        }
        // Two days, shifted onto the day the US clocks go back (a 25 hour day): still two days.
        val fallBack = moved(allDayEvent("2026-11-10", "2026-11-12"), "2026-10-31T00:00:00-04:00")
        assertEquals(midnight("2026-10-31"), fallBack[CalendarContract.Events.DTSTART])
        assertEquals(midnight("2026-11-02"), fallBack[CalendarContract.Events.DTEND])
        // Two days that include the 23 hour spring day, shifted to ordinary days: two days.
        val springForward = moved(allDayEvent("2026-03-07", "2026-03-09"), "2026-03-20T00:00:00-04:00")
        assertEquals(midnight("2026-03-20"), springForward[CalendarContract.Events.DTSTART])
        assertEquals(midnight("2026-03-22"), springForward[CalendarContract.Events.DTEND])
        // A single day moved onto the 25 hour day stays one day instead of being refused as ending before it begins.
        val single = moved(allDayEvent("2026-11-10", "2026-11-11"), "2026-11-01T00:00:00-04:00")
        assertEquals(midnight("2026-11-01"), single[CalendarContract.Events.DTSTART])
        assertEquals(midnight("2026-11-02"), single[CalendarContract.Events.DTEND])
        // Several single days one after another, each moved onto or off a clock change.
        for ((from, newStart, expectedStart) in listOf(
            Triple("2026-03-06", "2026-03-08T00:00:00-05:00", "2026-03-08"),
            Triple("2026-03-08", "2026-03-15T00:00:00-04:00", "2026-03-15"),
            Triple("2026-11-02", "2026-11-01T00:00:00-04:00", "2026-11-01"),
        )) {
            val next = java.time.LocalDate.parse(expectedStart).plusDays(1).toString()
            val values = moved(allDayEvent(from, java.time.LocalDate.parse(from).plusDays(1).toString()), newStart)
            assertEquals(from, midnight(expectedStart), values[CalendarContract.Events.DTSTART])
            assertEquals(from, midnight(next), values[CalendarContract.Events.DTEND])
        }
    }

    @Test fun aClippedNoteOnTheCardSaysHowLongItReallyIs() {
        backend.events[1] = plain.copy(notes = "n".repeat(300))
        val prepared = tools.prepareUpdate(args("id" to "1", "notes" to "x".repeat(250) + "tail"))
        val text = prepared.text(ApplicationProvider.getApplicationContext<Context>().resources)
        assertTrue(text, text.contains("300") && text.contains("254"))
        assertFalse(text, text.contains("tail"))
        // A note that fits is shown whole, with no length.
        val short = tools.prepareUpdate(args("id" to "1", "notes" to "short"))
        assertTrue(short.text(ApplicationProvider.getApplicationContext<Context>().resources).contains("short"))
    }

    @Test fun aRuleThatCannotBeSplitExactlyIsRefusedNotApproximated() {
        val monthly = daily.copy(rrule = "FREQ=MONTHLY;BYDAY=2MO;COUNT=6")
        val failure = runCatching { RecurrenceEdit.split(monthly, fourth, fourth, zone) }.exceptionOrNull() as PhoneToolFailure
        assertTrue(failure.message!!.contains("cannot be split exactly"))
        val named = daily.copy(rrule = "FREQ=WEEKLY;BYDAY=MO,WE")
        val moved = runCatching { RecurrenceEdit.split(named, fourth, fourth + 86_400_000L, zone) }.exceptionOrNull() as PhoneToolFailure
        assertTrue(moved.message!!.contains("another day"))
        // A time-only change keeps the named days.
        assertEquals("FREQ=WEEKLY;BYDAY=MO,WE", RecurrenceEdit.split(named, fourth, fourth + 3_600_000L, zone).newRrule)
    }

    @Test fun changingThisAndFutureAroundAlreadySeparateOccurrencesIsUnsupportedBeforeTheCard() {
        seedDaily()
        backend.events[900] = daily.copy(id = 900, rrule = null, duration = null, start = fourth + 86_400_000L,
            end = fourth + 90_000_000L, originalId = 2, originalInstanceTime = fourth + 86_400_000L)
        val failure = runCatching {
            tools.prepareUpdate(args("id" to "2", "occurrence_start" to fourthIso, "span" to "future", "title" to "x"))
        }.exceptionOrNull() as PhoneToolFailure
        assertTrue(failure.message!!.contains("already changed or cancelled"))
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun anEventThatChangedAfterTheCardIsRefusedWithoutAnyWrite() {
        backend.events[1] = plain
        val prepared = tools.prepareUpdate(args("id" to "1", "title" to "Renamed"))
        backend.events[1] = plain.copy(location = "Elsewhere")
        val refusal = runCatching { tools.update(prepared) }.exceptionOrNull()
        assertTrue(refusal.toString(), refusal is PhoneToolWriteRefusal)
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun anAttendeeOrReminderChangeAfterTheCardIsAlsoStale() {
        seedDaily()
        val prepared = tools.prepareDelete(args("id" to "2", "occurrence_start" to fourthIso))
        backend.reminderRows[2] = listOf(CalendarReminder(5, 1))
        assertTrue(runCatching { tools.delete(prepared) }.exceptionOrNull() is PhoneToolWriteRefusal)
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun aTargetThatVanishedAfterTheCardIsARefusalNotAnUncertainWrite() {
        backend.events[1] = plain
        val prepared = tools.prepareDelete(args("id" to "1"))
        backend.events.clear()
        assertTrue(runCatching { tools.delete(prepared) }.exceptionOrNull() is PhoneToolWriteRefusal)
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun aBatchFailureIsNotRewrittenAsARefusalSoTheRegistryAnswersUncertain() {
        backend.events[1] = plain
        val prepared = tools.prepareDelete(args("id" to "1"))
        backend.applyFailure = IllegalStateException("provider died")
        val failure = runCatching { tools.delete(prepared) }.exceptionOrNull()
        assertTrue(failure.toString(), failure is IllegalStateException && failure !is PhoneToolWriteRefusal)
    }

    @Test fun theCardTextIsFormattedWithoutReadingTheProvider() {
        // The card text is a pure function of the prepared snapshot: formatting it touches no backend.
        backend.events[1] = plain
        val prepared = tools.prepareUpdate(args("id" to "1", "title" to "Renamed", "location" to ""))
        val reads = backend.reads
        val text = prepared.text(ApplicationProvider.getApplicationContext<Context>().resources)
        assertEquals(reads, backend.reads)
        assertTrue(text, text.startsWith("修改日程「Weekly sync」") || text.startsWith("Change event “Weekly sync”"))
    }

    @Test fun changingOnlyTheEndTimeShowsTheTimeChangeOnTheConfirmationCard() {
        backend.events[1] = plain
        val resources = ApplicationProvider.getApplicationContext<Context>().resources
        // Plain is 15:00–16:00 (+08:00); extend only the end to 17:30 (+08:00).
        val prepared = tools.prepareUpdate(args("id" to "1", "end" to "2026-11-06T17:30:00+08:00"))
        assertNull(prepared.change.start)
        assertEquals(Instant.parse("2026-11-06T09:30:00Z"), prepared.change.end)
        val text = prepared.text(resources)
        val expectedBefore = calendarRange(
            Instant.parse("2026-11-06T07:00:00Z"),
            Instant.parse("2026-11-06T08:00:00Z"),
            false,
            resources,
            zone,
        )
        val expectedAfter = calendarRange(
            Instant.parse("2026-11-06T07:00:00Z"),
            Instant.parse("2026-11-06T09:30:00Z"),
            false,
            resources,
            zone,
        )
        val expectedTimeLine = resources.getString(R.string.phonetools_calendar_time_change, expectedBefore, expectedAfter)
        assertTrue("Expected confirmation card to show time change line:\n$text", expectedTimeLine in text.lines())
        tools.update(prepared)
        assertEquals(ms("2026-11-06T09:30:00Z"), backend.batches.single().single().values[CalendarContract.Events.DTEND])
    }

    @Test fun changingEndTimeAlongsideTitleShowsBothChangesOnTheConfirmationCard() {
        backend.events[1] = plain
        val resources = ApplicationProvider.getApplicationContext<Context>().resources
        val prepared = tools.prepareUpdate(args("id" to "1", "title" to "Extended sync", "end" to "2026-11-06T17:00:00+08:00"))
        assertNull(prepared.change.start)
        assertEquals(Instant.parse("2026-11-06T09:00:00Z"), prepared.change.end)
        val text = prepared.text(resources)
        val expectedBefore = calendarRange(
            Instant.parse("2026-11-06T07:00:00Z"),
            Instant.parse("2026-11-06T08:00:00Z"),
            false,
            resources,
            zone,
        )
        val expectedAfter = calendarRange(
            Instant.parse("2026-11-06T07:00:00Z"),
            Instant.parse("2026-11-06T09:00:00Z"),
            false,
            resources,
            zone,
        )
        val expectedTimeLine = resources.getString(R.string.phonetools_calendar_time_change, expectedBefore, expectedAfter)
        val expectedTitleLine = resources.getString(R.string.phonetools_calendar_title_change, "Weekly sync", "Extended sync")
        assertTrue("Expected confirmation card to show title change line:\n$text", expectedTitleLine in text.lines())
        assertTrue("Expected confirmation card to show time change line:\n$text", expectedTimeLine in text.lines())
    }
}

/** An in-memory provider: reads come from the maps, a batch is recorded (or fails), inserts get fresh ids. */
internal class FakeCalendarBackend : CalendarBackend {
    val events = mutableMapOf<Long, CalendarEventRecord>()
    val instances = mutableMapOf<Long, List<CalendarInstance>>()
    val attendeeRows = mutableMapOf<Long, List<CalendarAttendee>>()
    val reminderRows = mutableMapOf<Long, List<CalendarReminder>>()
    val batches = mutableListOf<List<ProviderWrite>>()
    var applyFailure: Exception? = null
    var reads = 0
    private var nextId = 900L

    override fun event(id: Long): CalendarEventRecord? { reads++; return events[id] }
    override fun instances(eventId: Long, from: Long, to: Long): List<CalendarInstance> {
        reads++
        return instances[eventId].orEmpty().filter { it.begin in from..to }
    }
    override fun exceptions(seriesId: Long): List<CalendarEventRecord> {
        reads++
        return events.values.filter { it.originalId == seriesId }.sortedBy { it.id }
    }
    override fun attendees(eventId: Long): List<CalendarAttendee> { reads++; return attendeeRows[eventId].orEmpty() }
    override fun reminders(eventId: Long): List<CalendarReminder> { reads++; return reminderRows[eventId].orEmpty() }

    override fun apply(writes: List<ProviderWrite>): Array<ContentProviderResult> {
        applyFailure?.let { throw it }
        batches += writes
        return Array(writes.size) { index ->
            if (writes[index].kind == ProviderWrite.Kind.Insert) {
                ContentProviderResult(ContentUris.withAppendedId(CalendarContract.Events.CONTENT_URI, nextId++))
            } else ContentProviderResult(1)
        }
    }
}
