package persona.feature.inbox

import android.content.res.Resources
import androidx.annotation.StringRes
import androidx.compose.runtime.compositionLocalOf
import persona.core.contracts.InboxEventFields
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.Status
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.temporal.ChronoUnit
import java.util.Locale

/** The page's clock and zone, injectable for the shared showcase without changing production defaults. */
internal val LocalInboxClock = compositionLocalOf<() -> Instant> { { Instant.now() } }
internal val LocalInboxZone = compositionLocalOf<ZoneId> { ZoneId.systemDefault() }

/**
 * The words for the hub's `now` key, from the shared table in spec/fixtures/inbox/now.json. An old hub or a key this
 * version does not know returns null, so the caller keeps its existing event mark. Follow words are already localized
 * by the processing conversation; only the fixed parts and punctuation come from Android's resources.
 */
internal fun inboxNowText(
    event: InboxEventFields,
    res: Resources,
    locale: Locale,
    now: Instant = Instant.now(),
    zone: ZoneId = ZoneId.systemDefault(),
): String? {
    val follow = event.follow
    val who = follow?.who?.takeIf { it.isNotBlank() }
    val next = follow?.next?.takeIf { it.isNotBlank() }
    val again = follow?.checkAt?.let { inboxFollowTime(it, res, locale, now, zone).takeIf(String::isNotBlank) }

    fun text(@StringRes id: Int) = res.getString(id)
    fun text(@StringRes id: Int, arg: String) = res.getString(id, arg)
    fun withNext(base: String) = base + next?.let { text(R.string.inbox_now_next, it) }.orEmpty()

    return when (event.now) {
        "waiting" -> buildString {
            append(who?.let { text(R.string.inbox_now_waiting, it) } ?: text(R.string.inbox_now_waiting_anyone))
            again?.let { append(text(R.string.inbox_now_look_again, it)) }
            next?.let { append(text(R.string.inbox_now_next, it)) }
        }
        "check_due" -> withNext(text(R.string.inbox_now_check_due))
        "failed" -> event.handling?.error?.takeIf { it.isNotBlank() }
            ?.let { text(R.string.inbox_now_failed, it) } ?: text(R.string.inbox_now_failed_plain)
        else -> {
            @StringRes val id = when (event.now) {
                "confirm_reply" -> R.string.inbox_now_confirm_reply
                "confirm_agent" -> R.string.inbox_now_confirm_agent
                "decide" -> R.string.inbox_now_decide
                "queued" -> R.string.inbox_now_queued
                "looking" -> R.string.inbox_now_looking
                "agent_working" -> R.string.inbox_now_agent_working
                "follow_up_ask" -> R.string.inbox_now_follow_up_ask
                "needs_you" -> R.string.inbox_now_needs_you
                "fyi" -> R.string.inbox_now_fyi
                "folded" -> R.string.inbox_now_folded
                "done_replied" -> R.string.inbox_now_done_replied
                "done_manager" -> R.string.inbox_now_done_manager
                "done_user" -> R.string.inbox_now_done_user
                "done_read" -> R.string.inbox_now_done_read
                "done_dismissed" -> R.string.inbox_now_done_dismissed
                "done_muted" -> R.string.inbox_now_done_muted
                "done_superseded" -> R.string.inbox_now_done_superseded
                else -> return null
            }
            text(id)
        }
    }
}

/** A row never shows a done key on an open item, even if a malformed hub response pairs the two. */
internal fun inboxNowForRow(
    record: InboxRecord,
    res: Resources,
    locale: Locale,
    now: Instant = Instant.now(),
    zone: ZoneId = ZoneId.systemDefault(),
): String? = inboxNowText(record.event, res, locale, now, zone)?.takeUnless {
    record.event.now?.startsWith("done_") == true && !record.item.isDone
}

/** The state color already used by the corresponding row mark; the new words do not invent a second state model. */
internal fun inboxNowStatus(key: String?): Status = when (key) {
    "queued", "looking", "agent_working", "waiting" -> Status.Working
    "confirm_reply", "confirm_agent", "decide", "follow_up_ask", "check_due", "failed", "needs_you" -> Status.NeedsYou
    "done_muted" -> Status.Muted
    else -> Status.Idle
}

private fun clockTime(at: Instant, locale: Locale, zone: ZoneId): String =
    DateTimeFormatter.ofPattern("HH:mm", locale).withZone(zone).format(at)

private fun inboxDays(at: Instant, now: Instant, zone: ZoneId): Long =
    ChronoUnit.DAYS.between(at.atZone(zone).toLocalDate(), now.atZone(zone).toLocalDate())

/** The follow-up's short future moment: clock today, tomorrow in words, then a localized date and 24-hour clock. */
internal fun inboxFollowTime(value: String?, res: Resources, locale: Locale, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    val at = value?.let { runCatching { Instant.parse(it) }.getOrNull() } ?: return ""
    val days = -inboxDays(at, now, zone)
    val time = clockTime(at, locale, zone)
    return when {
        days == 0L -> time
        days == 1L -> res.getString(R.string.inbox_time_tomorrow) + " " + time
        at.atZone(zone).year == now.atZone(zone).year -> LocalizedFormats.format(at, LocalizedFormats.MONTH_DAY, locale, zone) + " " + time
        else -> LocalizedFormats.format(at, LocalizedFormats.DATE, locale, zone) + " " + time
    }
}

/** A row's timestamp: clock today, yesterday, weekday within six days, then a short date, all in the injected zone. */
internal fun displayTime(value: String, locale: Locale, res: Resources, zone: ZoneId = ZoneId.systemDefault(), now: Instant = Instant.now()): String {
    val at = runCatching { Instant.parse(value) }.getOrNull() ?: return ""
    if (at.atZone(zone).year < 2000) return ""
    val days = inboxDays(at, now, zone)
    return when {
        days <= 0 -> clockTime(at, locale, zone)
        days == 1L -> res.getString(R.string.inbox_time_yesterday)
        now.toEpochMilli() - at.toEpochMilli() < 6 * 86_400_000 -> DateTimeFormatter.ofPattern("EEE", locale).withZone(zone).format(at)
        at.atZone(zone).year == now.atZone(zone).year -> LocalizedFormats.format(at, LocalizedFormats.MONTH_DAY, locale, zone)
        else -> at.atZone(zone).let { String.format(Locale.ROOT, "%04d-%02d-%02d", it.year, it.monthValue, it.dayOfMonth) }
    }
}

/** `where · sender: preview`, saying a direct chat's name once; null leaves the old row's preview untouched. */
internal fun inboxRowMeta(where: String?, sender: String?, preview: String, res: Resources): String? {
    val place = where?.takeIf { it.isNotBlank() } ?: return null
    val who = sender?.takeIf { it.isNotBlank() }
    val line = when {
        who == null -> preview
        preview.isBlank() -> who
        else -> res.getString(R.string.inbox_row_sender, who, preview)
    }
    return when {
        place == who -> line
        line.isBlank() -> place
        else -> res.getString(R.string.inbox_row_where, place, line)
    }
}

internal fun inboxRowMeta(record: InboxRecord, res: Resources): String? =
    // The domain item mirrors event.where for search; the event fallback also supports records built from wire fields alone.
    inboxRowMeta(record.event.where ?: record.item.where, record.item.sender, record.item.preview, res)

private fun senderPreview(sender: String, preview: String, res: Resources) =
    if (preview.isBlank()) sender else res.getString(R.string.inbox_row_sender, sender, preview)

/** The source-specific third line used before `where` existed (iOS InboxRow.from / web fromLine). */
internal fun inboxLegacyRowMeta(record: InboxRecord, res: Resources, translatedPreview: String? = null): String {
    val item = record.item
    val sender = item.sender.orEmpty()
    return when {
        record.source == "assistant" -> item.preview
        record.doc != null -> listOf(docTypeName(res, record.doc.type), item.preview).filter { it.isNotBlank() }.joinToString(" · ")
        record.moment != null -> senderPreview(sender, item.preview, res)
        record.connector != null -> {
            val reference = record.connector.thread.takeIf { it.contains('#') }
            val who = if (sender.isBlank()) item.preview else senderPreview(sender, item.preview, res)
            listOfNotNull(reference, who.takeIf { it.isNotBlank() }).joinToString(" · ")
        }
        !translatedPreview.isNullOrBlank() && sender.isNotBlank() -> senderPreview(sender, translatedPreview, res)
        sender.isBlank() || record.isLark && record.isGroup == false -> item.preview
        else -> senderPreview(sender, item.preview, res)
    }
}

private fun repeatsSummary(record: InboxRecord): Boolean {
    if (record.source != "assistant") return false
    val summary = record.item.summary?.trim().orEmpty()
    val preview = record.item.preview.trim()
    return summary.isNotEmpty() && (preview == summary || preview.startsWith(summary))
}

/** Every row's third line, including an older hub. Empty only when it would repeat the summary above it. */
internal fun inboxRowThirdLine(record: InboxRecord, res: Resources, translatedPreview: String? = null): String =
    if (repeatsSummary(record)) "" else inboxRowMeta(record, res) ?: inboxLegacyRowMeta(record, res, translatedPreview)
