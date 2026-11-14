package persona.platform.phonetools

import android.content.res.Resources
import androidx.annotation.StringRes
import persona.core.contracts.api.PhoneToolSpec
import kotlinx.serialization.json.*

/** [modelName]: what tool results call the source, the same words whichever language the phone shows. */
enum class PhoneSource(@StringRes val title: Int, @StringRes val detail: Int, internal val key: String, internal val modelName: String) {
    Contacts(R.string.phonetools_source_contacts, R.string.phonetools_source_contacts_detail, "contacts", "通讯录"),  // i18n-ignore: named so in tool results for the model
    Calendar(R.string.phonetools_source_calendar, R.string.phonetools_source_calendar_detail, "calendar", "日历"),  // i18n-ignore: named so in tool results for the model
    Location(R.string.phonetools_source_location, R.string.phonetools_source_location_detail, "location", "位置"),  // i18n-ignore: named so in tool results for the model
    Health(R.string.phonetools_source_health, R.string.phonetools_source_health_detail, "health", "健康"),  // i18n-ignore: named so in tool results for the model
    Photos(R.string.phonetools_source_photos, R.string.phonetools_source_photos_detail, "photos", "照片"),  // i18n-ignore: named so in tool results for the model
    Clipboard(R.string.phonetools_source_clipboard, R.string.phonetools_source_clipboard_detail, "clipboard", "剪贴板"),  // i18n-ignore: named so in tool results for the model
    Device(R.string.phonetools_source_device, R.string.phonetools_source_device_detail, "device", "设备信息"),  // i18n-ignore: named so in tool results for the model
    Notifications(R.string.phonetools_source_notifications, R.string.phonetools_source_notifications_detail, "notifications", "通知"),  // i18n-ignore: named so in tool results for the model
}

/** A confirmation's text, written out with the resources of the language the phone shows. */
typealias PhoneConfirmationText = (Resources) -> String

internal data class ToolDefinition(
    val spec: PhoneToolSpec, val source: PhoneSource, val foregroundOnly: Boolean = false,
    /**
     * What the phone asks the user before a write runs, written out in the phone's language when it is shown; it may
     * throw for arguments that cannot be shown.
     */
    val confirmation: ((JsonObject) -> PhoneConfirmationText)? = null,
)

/**
 * The phone_* tools, declared exactly as the iOS app declares them (apple/Persona/PhoneTools): the hub merges tools of
 * the same name from every phone and may send a call to either one, so names, descriptions and parameters must not
 * differ (PhoneToolCatalogTest compares them with scripts/assets/export_ios_phone_tools.py's fixture). As on iOS, numbers out
 * of range are clamped instead of refused and unknown arguments are ignored. phone_notifications_list is Android's own.
 */
internal object PhoneToolCatalog {
    private fun string(description: String, values: List<String> = emptyList()) = buildJsonObject {
        put("type", "string"); put("description", description)
        if (values.isNotEmpty()) put("enum", JsonArray(values.map(::JsonPrimitive)))
    }
    private fun integer(description: String, min: Int, max: Int) = buildJsonObject {
        put("type", "integer"); put("description", description); put("minimum", min); put("maximum", max)
    }
    private fun boolean(description: String) = buildJsonObject { put("type", "boolean"); put("description", description) }
    private fun array(description: String, items: JsonObject) = buildJsonObject {
        put("type", "array"); put("description", description); put("items", items)
    }

    /** iOS Schema.object: no `properties` or `required` key when there are none. */
    private fun tool(
        name: String, description: String, source: PhoneSource,
        properties: Map<String, JsonObject> = emptyMap(), required: List<String> = emptyList(),
        foreground: Boolean = false, confirmation: ((JsonObject) -> PhoneConfirmationText)? = null,
    ) = ToolDefinition(PhoneToolSpec(name, description, buildJsonObject {
        put("type", "object"); put("additionalProperties", false)
        if (properties.isNotEmpty()) put("properties", JsonObject(properties))
        if (required.isNotEmpty()) put("required", JsonArray(required.map(::JsonPrimitive)))
    }), source, foreground, confirmation)

    val all = listOf(
        tool("phone_contacts_search",
            "Search the phone's contacts by name, company, job title, email or phone number. Returns up to `limit` people " +
                "with their name, company, job title, phone numbers (with labels such as mobile/work), email addresses and " +
                "birthday (yyyy-MM-dd). Use this to look up a colleague's number before calling or drafting a message.",
            PhoneSource.Contacts, mapOf(
                "query" to string("Text to match against name, company, job title, email or phone number. Matching is " +
                    "case-insensitive and looks for the text anywhere in the field; a phone number matches with or without " +
                    "spaces and dashes. Pass an empty string to get the first contacts in the address book's own order."),
                "limit" to integer("Maximum number of contacts to return, default 10, max 50.", 1, 50),
            ), listOf("query")),
        tool("phone_calendar_events",
            "List calendar events in a time window. Defaults to the next 7 days starting at 00:00 today. Times are ISO 8601 " +
                "with a timezone offset (e.g. 2026-10-24T14:00:00+08:00). Returns each event's id, title, start, end, all-day " +
                "flag, calendar name, location, the first 300 characters of notes and the attendees. Use it to check whether " +
                "the user is free.",
            PhoneSource.Calendar, mapOf(
                "start" to string("Window start, ISO 8601 (e.g. 2026-10-24T00:00:00+08:00). Default: 00:00 today."),
                "end" to string("Window end, ISO 8601. Default: 7 days after start."),
                "query" to string("Only return events whose title, location or notes contain this text."),
                "limit" to integer("Maximum number of events to return, default 50, max 200.", 1, 200),
            )),
        tool("phone_calendar_create_event",
            "Create a calendar event. `start` and `end` are ISO 8601 with a timezone (e.g. 2026-10-24T14:00:00+08:00). For " +
                "an all-day event pass all_day=true and dates at 00:00. The user must approve the event on the phone before " +
                "it is saved. Returns the new event's identifier.",
            PhoneSource.Calendar, mapOf(
                "title" to string("Event title."),
                "start" to string("Start time, ISO 8601 with a timezone."),
                "end" to string("End time, ISO 8601 with a timezone. Must be after start."),
                "location" to string("Optional location."),
                "notes" to string("Optional notes."),
                "all_day" to boolean("Mark the event as all-day. Default false."),
                "calendar" to string("Calendar name (title) to put the event in. Default: the default calendar for new events."),
            ), listOf("title", "start", "end"),
            confirmation = { args -> CalendarArguments.parse(args).let { event -> { res: Resources -> event.summary(res) } } }),
        tool("phone_location_current",
            "Get the phone's current location. Returns latitude, longitude, horizontal accuracy in meters, altitude, the " +
                "reverse-geocoded address (country, province, city, district, street, formatted address), the device " +
                "timezone and the timestamp of the fix (ISO 8601). Waits at most 15 seconds for a fix. Only call this when " +
                "the user asks something that needs their location.",
            PhoneSource.Location, foreground = true),
        tool("phone_health_summary",
            "Summarize the user's health data per day. Metrics: steps, distance (meters), " +
                "active_energy, resting_energy, dietary_energy (kcal, daily totals), " +
                "protein, carbs, fat (grams, daily totals), heart_rate (bpm, average/min/max over the day), " +
                "resting_heart_rate (bpm), sleep (hours asleep), weight (kg). " +
                "Defaults to steps over the last 7 days. Only the requested metrics are read. Days run from local midnight to " +
                "midnight; today's numbers are partial. Returns one row per day with only the days that have data. Read access is " +
                "never reported to apps, so an empty result can also mean the user denied read access.",
            PhoneSource.Health, mapOf(
                "metrics" to array("Metrics to summarize. Default [\"steps\"].", string("One metric.", Health.metricNames)),
                "days" to integer("How many days back from today, default 7, max 90.", 1, 90),
            )),
        tool("phone_photos_recent",
            "List recent photos and videos, newest first. Each row has the Photos local identifier, creation date (ISO " +
                "8601), media type (image/video), pixel width and height, whether it is a screenshot, whether it is a " +
                "favorite, duration in seconds for videos, and the latitude/longitude where the photo was taken when it is " +
                "geotagged. Pass an id to phone_photo_get to actually see the image.",
            PhoneSource.Photos, mapOf(
                "limit" to integer("Maximum number of assets, default 20, max 100.", 1, 100),
                "days" to integer("Only assets from the last N days. Default: no limit.", 1, 3650),
                "screenshots_only" to boolean("Only screenshots. Default false."),
            )),
        tool("phone_photo_get",
            "Get one photo as an image so you can look at it (read a screenshot, describe a photo, transcribe a " +
                "whiteboard). Pass the id from phone_photos_recent. The image is JPEG, at most max_size pixels on the long " +
                "edge (default 1280, max 2048), quality 0.7. Returns the image plus a short text description.",
            PhoneSource.Photos, mapOf(
                "id" to string("Photos local identifier from phone_photos_recent."),
                "max_size" to integer("Longest edge in pixels, default 1280, max 2048.", 256, 2048),
            ), listOf("id")),
        tool("phone_device_info",
            "Describe the phone: model identifier and name, iOS version, battery percentage and charging state, low power " +
                "mode, free and total storage in bytes, current network type (wifi/cellular/wired/offline), timezone and " +
                "locale, and the Persona app's version, build number and build channel (debug/release). Cheap and safe to " +
                "call when you need context about the device.",
            PhoneSource.Device),
        tool("phone_clipboard_get",
            "Read the phone's clipboard as text. iOS may show an \"Allow Paste?\" prompt the first time, which the user has " +
                "to confirm. Returns \"text\" and its character count, or text: null when the clipboard is empty or holds " +
                "something that is not text.",
            PhoneSource.Clipboard, foreground = true),
        tool("phone_clipboard_set",
            "Replace the phone's clipboard with the given text, for example a link or a draft the user asked for. " +
                "Overwrites whatever was there.",
            PhoneSource.Clipboard, mapOf("text" to string("Text to place on the clipboard.")), listOf("text"), foreground = true),
        tool("phone_notify",
            "Post a notification on the phone. Use it to tell the user about something that happened while they were not " +
                "looking (a long task finished, a build failed). delay_seconds schedules it a few seconds or minutes later; " +
                "omit it to notify immediately. The first call asks for notification permission. Returns whether the " +
                "notification was scheduled.",
            PhoneSource.Notifications, mapOf(
                "title" to string("Notification title, short."),
                "body" to string("Notification body."),
                "delay_seconds" to integer("Seconds from now to deliver it. Default 0 (immediately), max 86400.", 0, 86_400),
            ), listOf("title", "body")),
        // Android only: iOS has no API for an app's own notifications that is worth a tool.
        tool("phone_notifications_list",
            "List the notifications Persona itself is showing on this Android phone, newest first: id, title, body and when " +
                "each was posted. It cannot see other apps' notifications. Use it to check what the user has already been " +
                "told before sending another phone_notify.",
            PhoneSource.Notifications, mapOf(
                "limit" to integer("Maximum number of notifications to return, default 50, max 100.", 1, 100),
            )),
    )
}

/** A failure the model can act on (iOS ToolFailure): its message goes back as the tool result. */
internal class PhoneToolFailure(val kind: Kind, message: String) : Exception(message) {
    enum class Kind { Denied, InvalidArgument, Unavailable }
}

internal fun invalid(message: String): Nothing = throw PhoneToolFailure(PhoneToolFailure.Kind.InvalidArgument, message)
internal fun unavailable(message: String): Nothing = throw PhoneToolFailure(PhoneToolFailure.Kind.Unavailable, message)

/** A string argument trimmed, null when missing, not a string or blank (iOS ToolArgs.string). */
internal fun JsonObject.text(key: String): String? =
    (get(key) as? JsonPrimitive)?.takeIf { it.isString }?.content?.trim()?.takeIf { it.isNotEmpty() }

/** The exact string as sent, blank included; null when missing or not a string. */
internal fun JsonObject.raw(key: String): String? = (get(key) as? JsonPrimitive)?.takeIf { it.isString }?.content

/** An integer within bounds, [fallback] when missing or not a number (iOS ToolArgs.int(_:_:fallback:min:max:)). */
internal fun JsonObject.int(key: String, fallback: Int, min: Int, max: Int): Int =
    (number(key) ?: fallback.toLong()).coerceIn(min.toLong(), max.toLong()).toInt()

/** An optional integer within bounds; null when missing or not a number. */
internal fun JsonObject.intOrNull(key: String, min: Int, max: Int): Int? =
    number(key)?.coerceIn(min.toLong(), max.toLong())?.toInt()

private fun JsonObject.number(key: String): Long? {
    val value = (get(key) as? JsonPrimitive)?.takeUnless { it.isString } ?: return null
    return value.longOrNull ?: value.doubleOrNull?.takeIf { it.isFinite() }?.let { kotlin.math.round(it).toLong() }
}

internal fun JsonObject.bool(key: String): Boolean = (get(key) as? JsonPrimitive)?.takeUnless { it.isString }?.booleanOrNull ?: false
