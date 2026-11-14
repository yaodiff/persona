package persona.feature.inbox

import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.core.contracts.InboxEventFields
import persona.core.contracts.InboxFollow
import persona.core.contracts.InboxHandling
import java.time.Instant
import java.time.ZoneOffset
import java.util.Locale

/**
 * The parts of `compose` in spec/fixtures/inbox/now.json that its cases do not reach: `waiting` without a time to look
 * again or without a name, `check_due` without a next step, and a reason that only an Agent recorded. Also the shape of
 * `{time}`: the app's own short date for a future moment, never the hub's RFC 3339 string.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class InboxNowComposeReviewTest {
    private val clock = Instant.parse("2026-01-01T12:00:00Z")
    private val zone = ZoneOffset.UTC

    private fun resources(locale: Locale): Resources {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val configuration = Configuration(context.resources.configuration).apply {
            setLocale(locale)
            setLayoutDirection(locale)
        }
        return context.createConfigurationContext(configuration).resources
    }

    private val en = Locale.ENGLISH
    private val zh = Locale.SIMPLIFIED_CHINESE
    private val ja = Locale.JAPANESE
    private val enRes = resources(en)
    private val zhRes = resources(zh)
    private val jaRes = resources(ja)

    private fun waiting(follow: InboxFollow?): InboxEventFields =
        InboxEventFields(now = "waiting", handling = InboxHandling(state = "waiting"), follow = follow)

    @Test fun waitingWithoutALookAgainTimeJoinsOnlyWhatItHas() {
        // No check_at: no " · look again …", the next step still follows its own separator.
        val noTime = waiting(InboxFollow(who = "Alex to confirm the date", next = "ask whether Friday works"))
        assertEquals("Waiting for Alex to confirm the date: ask whether Friday works",
            inboxNowText(noTime, enRes, en, clock, zone))
        assertEquals("等Alex to confirm the date：ask whether Friday works", inboxNowText(noTime, zhRes, zh, clock, zone))
        assertEquals("Alex to confirm the dateを待っています：ask whether Friday works",
            inboxNowText(noTime, jaRes, ja, clock, zone))

        // Nobody named and nothing else: the plain words alone.
        assertEquals("Waiting for a reply", inboxNowText(waiting(InboxFollow()), enRes, en, clock, zone))
        assertEquals("等对方回复", inboxNowText(waiting(null), zhRes, zh, clock, zone))
        // A blank who is the same as none (the model may write spaces).
        assertEquals("相手の返信待ち", inboxNowText(waiting(InboxFollow(who = "  ")), jaRes, ja, clock, zone))
        // A next step without a who: plain words, then the next step.
        assertEquals("Waiting for a reply: ask whether Friday works",
            inboxNowText(waiting(InboxFollow(next = "ask whether Friday works")), enRes, en, clock, zone))
    }

    @Test fun checkDueWithoutANextStepIsItsOwnWords() {
        val event = InboxEventFields(now = "check_due", follow = InboxFollow(checkAt = "2026-01-01T08:30:00Z"))
        assertEquals("Time to look again", inboxNowText(event, enRes, en, clock, zone))
        assertEquals("到了再看的时间", inboxNowText(event, zhRes, zh, clock, zone))
        assertEquals("再確認の時間です", inboxNowText(event, jaRes, ja, clock, zone))
    }

    @Test fun onlyTheHandlingErrorIsTheReason() {
        // The contract: {reason} is handling.error, never agent.error; without one the plain words.
        val agentOnly = InboxEventFields(now = "failed", handling = InboxHandling(state = "failed"))
        assertEquals("AI couldn't handle it", inboxNowText(agentOnly, enRes, en, clock, zone))
        val withReason = agentOnly.copy(handling = InboxHandling(state = "failed", error = "timed out"))
        assertEquals("AI couldn't finish: timed out", inboxNowText(withReason, enRes, en, clock, zone))
        assertEquals("AI 没处理完：timed out", inboxNowText(withReason, zhRes, zh, clock, zone))
        assertEquals("AIが処理を終えられませんでした：timed out", inboxNowText(withReason, jaRes, ja, clock, zone))
        // A blank reason is no reason.
        val blank = agentOnly.copy(handling = InboxHandling(state = "failed", error = "   "))
        assertEquals("AI couldn't handle it", inboxNowText(blank, enRes, en, clock, zone))
    }

    @Test fun theTimeToLookAgainIsTheAppsOwnShortTime() {
        // Tomorrow is said in words (four clients agree on this wording, as the web's followTime does), never the wire string.
        val event = waiting(InboxFollow(checkAt = "2026-01-02T09:00:00Z"))
        val enText = inboxNowText(event, enRes, en, clock, zone)!!
        val zhText = inboxNowText(event, zhRes, zh, clock, zone)!!
        val jaText = inboxNowText(event, jaRes, ja, clock, zone)!!
        for (text in listOf(enText, zhText, jaText)) assertFalse(text, text.contains("2026-01-02T"))
        assertEquals("Waiting for a reply · look again tomorrow 09:00", enText)
        assertEquals("等对方回复 · 明天 09:00 再看", zhText)
        assertEquals("相手の返信待ち · 明日 09:00に再確認", jaText)

        // Further ahead, the language's own date and the 24-hour clock.
        val later = waiting(InboxFollow(checkAt = "2026-01-05T09:00:00Z"))
        assertTrue(inboxNowText(later, enRes, en, clock, zone)!!.startsWith("Waiting for a reply · look again Jan 5 09:00"))
        assertTrue(inboxNowText(later, zhRes, zh, clock, zone)!!.startsWith("等对方回复 · 1月5日 09:00 再看"))

        // Today is the clock alone, in 24-hour time in every language.
        val today = waiting(InboxFollow(checkAt = "2026-01-01T15:30:00Z"))
        assertEquals("Waiting for a reply · look again 15:30", inboxNowText(today, enRes, en, clock, zone))
        assertTrue(inboxNowText(today, zhRes, zh, clock, zone)!!.startsWith("等对方回复 · 15:30 再看"))
    }

    @Test fun aKeyTheWordsTableDoesNotKnowKeepsTheAppsOwnMark() {
        val res = zhRes
        for (key in listOf("something_new", "done", "confirm", "", "WAITING", "waiting ")) {
            assertNull(key, inboxNowText(InboxEventFields(now = key), res, zh, clock, zone))
        }
        assertNull(inboxNowText(InboxEventFields(now = null), res, zh, clock, zone))
    }
}
