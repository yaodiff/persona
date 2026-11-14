package persona.feature.inbox

import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import java.time.Instant
import java.time.ZoneOffset
import java.util.Locale

/** Inbox, Feishu and Moments times follow the app's language, never a fixed `MM-dd HH:mm` (#4043). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxTimeLanguagesTest {
    private val zone = ZoneOffset.UTC
    private val res = ApplicationProvider.getApplicationContext<android.content.Context>().resources
    private val at = "2026-11-05T14:30:00Z"
    private val ms = Instant.parse(at).toEpochMilli().toDouble()

    @Test fun rowUsesTwentyFourHoursAndMomentTimesKeepTheirLocale() {
        val now = Instant.parse("2026-11-05T10:00:00Z")
        for (locale in listOf(Locale.SIMPLIFIED_CHINESE, Locale.US, Locale.JAPANESE)) {
            assertEquals("14:30", displayTime(at, locale, res, zone, now))
        }
        for (format in listOf<(Locale) -> String>({ formatMomentTime(ms, it, zone) })) {
            val zh = format(Locale.SIMPLIFIED_CHINESE)
            assertTrue(zh, zh.startsWith("11月5日") && zh.endsWith("14:30"))
            val en = format(Locale.US)
            assertTrue(en, en.startsWith("Nov 5") && en.contains("2:30") && en.contains("PM"))
            val ja = format(Locale.JAPANESE)
            assertTrue(ja, ja.startsWith("11月5日") && ja.endsWith("14:30"))
            assertTrue(!zh.contains("11-05") && !en.contains("11-05") && !ja.contains("11-05"))
        }
    }

    @Test fun messageTimesUseTheLanguagesClock() {
        assertEquals("14:30", larkTime(at, Locale.SIMPLIFIED_CHINESE, zone))
        assertEquals("14:30", larkTime(at, Locale.JAPANESE, zone))
        val en = larkTime(at, Locale.US, zone)
        assertTrue(en, en.startsWith("2:30") && en.endsWith("PM"))
    }

    @Test fun badInputStaysQuiet() {
        assertEquals("", displayTime("not a time", Locale.US, res, zone))
        assertEquals("", formatMomentTime(null, Locale.US, zone))
        assertEquals("not a time", larkTime("not a time", Locale.US, zone))
    }

    @Test fun shortDatesAreTheTimeTodayAndTheDayOtherwise() {
        val now = Instant.parse("2026-11-05T10:00:00Z")
        assertEquals("09:30", larkShortDate(Instant.parse("2026-11-05T09:30:00Z"), Locale.SIMPLIFIED_CHINESE, now, zone))
        assertTrue(larkShortDate(Instant.parse("2025-03-02T09:30:00Z"), Locale.SIMPLIFIED_CHINESE, now, zone).contains("2025"))
    }

    @Test fun shortDatesTodayAreTheTimeInTheLanguage() {
        val now = Instant.parse("2026-11-05T20:00:00Z")
        val en = larkShortDate(Instant.parse(at), Locale.US, now, zone)
        assertTrue(en, en.startsWith("2:30") && en.endsWith("PM"))
        assertEquals("Mar 2", larkShortDate(Instant.parse("2026-03-02T09:30:00Z"), Locale.US, now, zone))
        assertEquals("3月2日", larkShortDate(Instant.parse("2026-03-02T09:30:00Z"), Locale.JAPANESE, now, zone))
        val past = larkShortDate(Instant.parse("2025-03-02T09:30:00Z"), Locale.US, now, zone)
        assertTrue(past, past.startsWith("Mar 2") && past.endsWith("2025"))
    }

    @Test fun pinTimesAreTheFullDateAndTimeInTheLanguage() {
        val en = larkPinTime(at, Locale.US, zone).orEmpty()
        assertTrue(en, en.startsWith("Nov 5, 2026") && en.endsWith("PM"))
        val ja = larkPinTime(at, Locale.JAPANESE, zone).orEmpty()
        assertTrue(ja, ja.startsWith("2026年11月5日") && ja.endsWith("14:30"))
        assertEquals(null, larkPinTime("not a time", Locale.US, zone))
    }
}
