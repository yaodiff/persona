package persona.platform.phonetools

import android.content.ContentProvider
import android.content.ContentUris
import android.content.ContentValues
import android.content.Context
import android.database.Cursor
import android.database.MatrixCursor
import android.net.Uri
import android.provider.CalendarContract
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowContentResolver
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId

/**
 * The hub merges phone tools of the same name from every phone and may send a call to either (server/internal/hub
 * mcpTools, phoneClient), so each tool Android declares must read exactly like the iOS app's. The fixture is exported
 * from apple/Persona/PhoneTools by scripts/assets/export_ios_phone_tools.py (`--check` tells when it is out of date).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class PhoneToolCatalogTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    private val ios: JsonObject = Json.parseToJsonElement(
        requireNotNull(javaClass.classLoader!!.getResource("ios-phone-tools.json")).readText()).jsonObject

    @Test fun sameToolsAsIosPlusTheAndroidNotificationList() {
        val android = PhoneToolCatalog.all.map { it.spec.name }
        assertEquals(android.size, android.toSet().size)
        assertEquals(ios.keys, android.toSet() - "phone_notifications_list")
    }

    /**
     * Sentences an iPhone description has that Android's does not, by tool name. The fixture is the iOS text verbatim,
     * so every difference is listed here with its reason instead of being trimmed by the exporter.
     */
    private val iosOnlyDescriptionSentences = mapOf(
        // iPhone-only behavior: a locked iPhone cannot read HealthKit and answers with the last snapshot it saved. Android
        // reads Health Connect and has no such snapshot, so promising one would mislead the model when this phone is the
        // one called. Drop this entry when the Health space gives Android the same behavior or the sentence changes.
        "phone_health_summary" to " When the iPhone is locked the app cannot read HealthKit: it returns the last snapshot " +
            "saved while the device was unlocked (\"source\": \"snapshot\" with \"as_of\"), or a device_locked error " +
            "when no snapshot exists yet.",
        // iPhone-only behavior (#13740): a locked iPhone answers from the snapshot phone_health_watch keeps. Android has no
        // phone_health_watch (the hub never wakes it), so it never promises one.
        "phone_health_samples" to " When the iPhone is locked the app cannot read Apple Health: a type kept current with " +
            "phone_health_watch answers from the on-device snapshot (\"source\": \"snapshot\" with \"as_of\"; its samples " +
            "may repeat ones you already have, so match them by id), any other type answers status: device_locked.",
        "phone_health_sources" to " When the iPhone is locked it answers from the on-device snapshots of the types " +
            "phone_health_watch keeps current (\"source\": \"snapshot\").",
        // iPhone-only fields (#12926): the iOS app returns its build stamp and hub connection as "build" and "runtime"
        // (AppDebugInfo, the same value as About's Debug info). Android's build has no such stamp yet; drop this entry
        // when it returns the same fields.
        "phone_device_info" to " Also \"build\" (what this app was built from: commit, dirty, branch, built_at, " +
            "builder, xcode, swift, configuration, bundle_id; Debug builds have them all, Release builds the commit) " +
            "and \"runtime\" (hub_host, hub_version, hub_connection, launched_at); any field may be missing.",
    )

    @Test fun everyToolIsDeclaredWordForWordAsOnIos() {
        for (tool in PhoneToolCatalog.all.filter { it.spec.name in ios }) {
            val want = ios.getValue(tool.spec.name).jsonObject
            val name = tool.spec.name
            val iosDescription = want.getValue("description").jsonPrimitive.content
            val extra = iosOnlyDescriptionSentences[name]
            // The listed sentence must still be on iOS's description (a stale entry fails) and is the only difference.
            if (extra != null) assertTrue("$name: the allowlisted sentence is no longer in the iOS text", iosDescription.endsWith(extra))
            assertEquals(name, iosDescription.removeSuffix(extra.orEmpty()), tool.spec.description)
            assertEquals(name, want.getValue("source").jsonPrimitive.content, tool.source.key)
            // iOS Schema.object: additionalProperties false, properties and required only when there are some.
            val properties = want.getValue("properties").jsonObject
            val required = want.getValue("required").jsonArray
            val schema = buildJsonObject {
                put("type", "object"); put("additionalProperties", false)
                if (properties.isNotEmpty()) put("properties", properties)
                if (required.isNotEmpty()) put("required", required)
            }
            assertEquals(name, schema, tool.spec.inputSchema)
        }
        // An allowlist entry for a tool Android does not declare, or one that no longer differs, is dead weight.
        for ((name, extra) in iosOnlyDescriptionSentences) {
            assertNotEquals(name, ios.getValue(name).jsonObject.getValue("description").jsonPrimitive.content,
                PhoneToolCatalog.all.single { it.spec.name == name }.spec.description)
            assertTrue(name, extra.isNotBlank())
        }
        // register_phone's wire shape.
        assertEquals(setOf("name", "description", "input_schema"), PhoneToolCatalog.all.first().spec.toWire().keys)
    }

    @Test fun onlyWritesAskFirstAndForegroundToolsAreTheOnesAndroidRestricts() {
        assertEquals(PhoneToolCatalog.confirmedWriteNames, PhoneToolCatalog.all
            .filter { it.confirmation != null || it.confirmationNeedsPreparation }.map { it.spec.name }.toSet())
        assertEquals(setOf("phone_calendar_create_event", "phone_calendar_update_event", "phone_calendar_delete_event",
            "phone_contacts_create", "phone_contacts_update", "phone_call_start"), PhoneToolCatalog.confirmedWriteNames)
        // Updates and deletes show before → after, so their card is prepared in the suspend path; the others stay pure.
        assertEquals(setOf("phone_calendar_update_event", "phone_calendar_delete_event", "phone_contacts_update"),
            PhoneToolCatalog.all.filter { it.confirmationNeedsPreparation }.map { it.spec.name }.toSet())
        assertTrue(PhoneToolCatalog.all.filter { it.confirmationNeedsPreparation }.all { it.confirmation == null })
        assertEquals(setOf("phone_location_current", "phone_clipboard_get", "phone_clipboard_set", "phone_call_start"),
            PhoneToolCatalog.all.filter { it.foregroundOnly }.map { it.spec.name }.toSet())
        assertEquals(listOf("phone_photos_recent", "phone_photo_get"),
            PhoneToolCatalog.all.filter { it.source == PhoneSource.Photos }.map { it.spec.name })
        assertEquals(listOf("phone_call_start"),
            PhoneToolCatalog.all.filter { it.source.key == "calls" }.map { it.spec.name })
    }

    @Test fun argumentsAreReadLikeTheIosApp() {
        val args = buildJsonObject {
            put("limit", 9999); put("days", 0); put("text", "  x "); put("blank", "  "); put("number", "7"); put("half", 2.6)
        }
        assertEquals(200, args.int("limit", 50, 1, 200))
        assertEquals(1, args.intOrNull("days", 1, 3650))
        assertNull(args.intOrNull("missing", 1, 3650))
        // A number sent as a string is no number: the default applies.
        assertEquals(20, args.int("number", 20, 1, 100))
        assertEquals(3, args.int("half", 20, 1, 100))
        assertEquals("x", args.text("text"))
        assertNull(args.text("blank"))
        assertEquals("  ", args.raw("blank"))
        assertFalse(args.bool("missing"))
    }

    @Test fun timesAndTheConfirmationSummary() {
        val zone = ZoneId.of("Asia/Shanghai")
        assertEquals(Instant.parse("2026-10-24T06:00:00Z"), parseTime("2026-10-24T14:00:00+08:00", zone))
        assertEquals(Instant.parse("2026-10-24T06:00:00Z"), parseTime("2026-10-24T06:00:00Z", zone))
        assertEquals(Instant.parse("2026-10-23T16:00:00Z"), parseTime("2026-10-24", zone))
        assertEquals(Instant.parse("2026-10-24T06:30:00Z"), parseTime("2026-10-24 14:30", zone))
        assertNull(parseTime(" ", zone))
        val failure = runCatching { parseTime("tomorrow", zone) }.exceptionOrNull() as PhoneToolFailure
        assertEquals("cannot read the time \"tomorrow\"; use ISO 8601 such as 2026-10-24T14:00:00+08:00", failure.message)
        assertEquals("2026-10-24T14:00:00+08:00", iso(Instant.parse("2026-10-24T06:00:00.250Z"), zone))

        val today = LocalDate.of(2026, 10, 1)
        val event = CalendarArguments.parse(buildJsonObject {
            put("title", " 周会 "); put("start", "2026-11-01T15:00:00+08:00"); put("end", "2026-11-01T15:30:00+08:00")
            put("location", "三楼"); put("calendar", "工作")
        }, zone)
        assertEquals("新建日程「周会」\n11月1日 15:00 → 15:30\n地点：三楼\n日历：工作", event.summary(res, zone, today))
        val overnight = event.copy(end = Instant.parse("2027-01-02T01:00:00Z"), location = null, calendar = null)
        assertEquals("新建日程「周会」\n11月1日 15:00 → 2027年1月2日 09:00", overnight.summary(res, zone, today))
        val allDay = CalendarArguments.parse(buildJsonObject {
            put("title", "出差"); put("start", "2026-11-03"); put("end", "2026-11-05"); put("all_day", true)
        }, zone)
        assertEquals("新建日程「出差」\n11月3日 → 11月4日（全天）", allDay.summary(res, zone, today))
        assertEquals(Instant.parse("2026-11-03T00:00:00Z").toEpochMilli(), allDay.startMillis(zone))
        assertEquals(Instant.parse("2026-11-05T00:00:00Z").toEpochMilli(), allDay.endMillis(zone))
        assertEquals("新建日程「出差」\n11月3日（全天）",
            allDay.copy(end = parseTime("2026-11-04", zone)!!).summary(res, zone, today))

        // Same-day all-day event (even when end > start by time of day) would collapse to DTSTART == DTEND; must be rejected.
        val sameDayAllDay = runCatching {
            CalendarArguments.parse(buildJsonObject {
                put("title", "假期")
                put("start", "2026-11-03T09:00:00+08:00")
                put("end", "2026-11-03T17:00:00+08:00")
                put("all_day", true)
            }, zone)
        }.exceptionOrNull() as? PhoneToolFailure
        assertEquals("an all-day event needs an exclusive end date after its start date", sameDayAllDay?.message)

        // Timed all-day event crossing local midnight to the next day is valid and uses UTC midnights of local dates.
        val nextDayAllDay = CalendarArguments.parse(buildJsonObject {
            put("title", "假期")
            put("start", "2026-11-03T09:00:00+08:00")
            put("end", "2026-11-04T09:00:00+08:00")
            put("all_day", true)
        }, zone)
        assertEquals(Instant.parse("2026-11-03T00:00:00Z").toEpochMilli(), nextDayAllDay.startMillis(zone))
        assertEquals(Instant.parse("2026-11-04T00:00:00Z").toEpochMilli(), nextDayAllDay.endMillis(zone))
    }

    @Test fun calendarCreateUsesInjectedZoneAndRejectsSameDayAllDayBeforeInsert() {
        val inserted = mutableListOf<ContentValues>()
        val provider = object : ContentProvider() {
            override fun onCreate() = true
            override fun query(uri: Uri, projection: Array<out String>?, selection: String?, selectionArgs: Array<out String>?, sortOrder: String?): Cursor? {
                if (uri == CalendarContract.Calendars.CONTENT_URI) {
                    return MatrixCursor(arrayOf(CalendarContract.Calendars._ID, CalendarContract.Calendars.CALENDAR_DISPLAY_NAME)).apply {
                        addRow(arrayOf<Any>(7L, "Work"))
                    }
                }
                return null
            }
            override fun insert(uri: Uri, values: ContentValues?): Uri {
                inserted += ContentValues(values)
                return ContentUris.withAppendedId(uri, 99L)
            }
            override fun getType(uri: Uri): String? = null
            override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?) = 0
            override fun update(uri: Uri, values: ContentValues?, selection: String?, selectionArgs: Array<out String>?) = 0
        }
        ShadowContentResolver.registerProviderInternal(CalendarContract.AUTHORITY, provider)
        val context = ApplicationProvider.getApplicationContext<Context>()
        val newYork = ZoneId.of("America/New_York")
        val tools = CalendarTools(context, zone = newYork)

        val bad = runCatching {
            tools.create(buildJsonObject {
                put("title", "Same day")
                put("start", "2026-11-03T09:00:00-05:00")
                put("end", "2026-11-03T17:00:00-05:00")
                put("all_day", true)
            })
        }.exceptionOrNull() as? PhoneToolFailure
        assertEquals("an all-day event needs an exclusive end date after its start date", bad?.message)
        assertTrue("must not insert corrupted zero-duration all-day event", inserted.isEmpty())

        val result = tools.create(buildJsonObject {
            put("title", "Offsite")
            put("start", "2026-11-03T09:00:00-05:00")
            put("end", "2026-11-04T17:00:00-05:00")
            put("all_day", true)
        })
        val written = inserted.single()
        assertEquals(Instant.parse("2026-11-03T00:00:00Z").toEpochMilli(), written.getAsLong(CalendarContract.Events.DTSTART))
        assertEquals(Instant.parse("2026-11-04T00:00:00Z").toEpochMilli(), written.getAsLong(CalendarContract.Events.DTEND))
        assertEquals("UTC", written.getAsString(CalendarContract.Events.EVENT_TIMEZONE))
        assertEquals(1, written.getAsInteger(CalendarContract.Events.ALL_DAY))
        val body = Json.parseToJsonElement(result.content.single().jsonObject.getValue("text").jsonPrimitive.content).jsonObject
        assertEquals("2026-11-03T00:00:00-05:00", body.getValue("start").jsonPrimitive.content)
        assertEquals("2026-11-04T00:00:00-05:00", body.getValue("end").jsonPrimitive.content)
        // Timed event uses the injected zone for EVENT_TIMEZONE and returned ISO timestamps.
        val timedResult = tools.create(buildJsonObject {
            put("title", "Standup")
            put("start", "2026-11-03 09:00")
            put("end", "2026-11-03 09:30")
        })
        val timedWritten = inserted.last()
        assertEquals("America/New_York", timedWritten.getAsString(CalendarContract.Events.EVENT_TIMEZONE))
        val timedBody = Json.parseToJsonElement(timedResult.content.single().jsonObject.getValue("text").jsonPrimitive.content).jsonObject
        assertEquals("2026-11-03T09:00:00-05:00", timedBody.getValue("start").jsonPrimitive.content)
        assertEquals("2026-11-03T09:30:00-05:00", timedBody.getValue("end").jsonPrimitive.content)
    }

    @Test fun photoIdsAndScreenshots() {
        assertEquals("image" to 42L, parsePhotoId("image:42"))
        assertEquals("video" to 7L, parsePhotoId("video:7"))
        assertNull(parsePhotoId("42"))
        assertNull(parsePhotoId("audio:1"))
        assertNull(parsePhotoId("image:x"))
        assertTrue(isScreenshot("Pictures/Screenshots/"))
        assertTrue(isScreenshot("DCIM/截屏/"))
        assertFalse(isScreenshot("DCIM/Camera/"))
        assertFalse(isScreenshot(null))
    }
}

/** The calendar confirmation in the other languages: words from the resources, dates from LocalizedFormats. */
@RunWith(AndroidJUnit4::class)
class CalendarSummaryLanguagesTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    private val zone = ZoneId.of("Asia/Shanghai")
    private val event get() = CalendarArguments.parse(buildJsonObject {
        put("title", "Weekly sync"); put("start", "2026-11-01T15:00:00+08:00"); put("end", "2026-11-01T15:30:00+08:00")
        put("location", "3F")
    }, zone)

    @Config(sdk = [35], qualifiers = "en")
    @Test fun english() {
        val summary = event.summary(res, zone, LocalDate.of(2026, 10, 1))
        assertTrue(summary, summary.startsWith("New event “Weekly sync”\nNov 1 "))
        assertTrue(summary, summary.endsWith("\nLocation: 3F"))
    }

    @Config(sdk = [35], qualifiers = "ja")
    @Test fun japanese() {
        val summary = event.summary(res, zone, LocalDate.of(2026, 10, 1))
        assertEquals("新しい予定「Weekly sync」\n11月1日 15:00 → 15:30\n場所：3F", summary)
    }
}
