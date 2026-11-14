package persona.feature.inbox

import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import androidx.test.core.app.ApplicationProvider
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.core.contracts.InboxEventFields
import persona.core.contracts.InboxHandling
import persona.core.contracts.inboxEventFields
import persona.core.designsystem.Status
import persona.core.domain.inbox.InboxItem
import java.io.File
import java.time.Instant
import java.time.ZoneOffset
import java.util.Locale

/** The shared follow-up fixtures, every case in every shipped language rather than sampled examples. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class InboxFollowTest {
    private data class Language(val wire: String, val locale: Locale, val resources: Resources)

    private val clock = Instant.parse("2026-01-01T12:00:00Z")
    private val zone = ZoneOffset.UTC
    private val languages = listOf("en" to Locale.ENGLISH, "zh-Hans" to Locale.SIMPLIFIED_CHINESE, "ja" to Locale.JAPANESE)
        .map { (wire, locale) -> Language(wire, locale, localizedResources(locale)) }

    private fun localizedResources(locale: Locale): Resources {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val configuration = Configuration(context.resources.configuration).apply {
            setLocale(locale)
            setLayoutDirection(locale)
        }
        return context.createConfigurationContext(configuration).resources
    }

    private fun fixture(name: String): JsonObject {
        val root = System.getProperty("persona.repo.root")?.let(::File) ?: generateSequence(File(".").canonicalFile) { it.parentFile }
            .first { File(it, ".git").exists() && File(it, "android").isDirectory }
        return Json.parseToJsonElement(File(root, "spec/fixtures/inbox/$name").readText()).jsonObject
    }

    private fun JsonObject.text(key: String): String? = (this[key] as? JsonPrimitive)?.contentOrNull

    private fun event(input: JsonObject, wire: String): InboxEventFields {
        val base = inboxEventFields(input.getValue("item").jsonObject)
        val translations = input["follow_by_lang"] as? JsonObject ?: return base
        val localized = translations[wire] as? JsonObject ?: return base
        val follow = base.follow ?: return base
        return base.copy(follow = follow.copy(
            who = localized.text("who") ?: follow.who,
            next = localized.text("next") ?: follow.next,
        ))
    }

    @Test fun nowWordsMatchEverySharedCaseInEveryLanguage() {
        val cases = fixture("now.json").getValue("cases").jsonArray.map { it.jsonObject }
        for (case in cases) for (language in languages) {
            val name = case.getValue("name").jsonPrimitive.content
            val input = case.getValue("input").jsonObject
            val expected = case.getValue("expected").jsonObject
            val time = expected.text("time")?.let { inboxFollowTime(it, language.resources, language.locale, clock, zone) }.orEmpty()
            val words = expected.getValue("text").jsonObject.getValue(language.wire).jsonPrimitive.content.replace("{time}", time)
            val actual = inboxNowText(event(input, language.wire), language.resources, language.locale, clock, zone)
            assertEquals("$name/${language.wire}", words, actual)
        }
    }

    @Test fun rowMetaMatchesEverySharedCaseInEveryLanguage() {
        val cases = fixture("row.json").getValue("cases").jsonArray.map { it.jsonObject }
        for (case in cases) for (language in languages) {
            val name = case.getValue("name").jsonPrimitive.content
            val item = case.getValue("input").jsonObject.getValue("item").jsonObject
            val localizedExpected = case.getValue("expected").jsonObject.getValue("meta").let { meta ->
                if (meta is JsonObject) meta[language.wire]?.jsonPrimitive?.content else null
            }
            assertEquals("$name/${language.wire}", localizedExpected,
                inboxRowMeta(item.text("where"), item.text("sender"), item.text("preview").orEmpty(), language.resources))
        }
    }

    @Test fun missingAndUnknownKeysKeepTheExistingMark() {
        val resources = languages.first().resources
        val locale = languages.first().locale
        val record = InboxRecord(
            InboxItem("old", status = InboxItem.WORKING),
            event = InboxEventFields(status = InboxItem.WORKING, handling = InboxHandling(state = "looking")),
        )
        assertNull(inboxNowForRow(record, resources, locale, clock, zone))
        assertEquals(EventMark.Working(R.string.inbox_handling_looking), eventMark(record))
        val future = record.copy(event = record.event.copy(now = "something_new"))
        assertNull(inboxNowForRow(future, resources, locale, clock, zone))
        assertEquals(EventMark.Working(R.string.inbox_handling_looking), eventMark(future))
    }

    @Test fun rowAndFollowTimesUseTheInjectedClockAndZone() {
        for (language in languages) {
            val res = language.resources
            val locale = language.locale
            assertEquals("09:00", inboxFollowTime("2026-01-01T09:00:00Z", res, locale, clock, zone))
            val tomorrow = when (language.wire) {
                "en" -> "tomorrow 09:00"
                "zh-Hans" -> "明天 09:00"
                else -> "明日 09:00"
            }
            assertEquals(tomorrow, inboxFollowTime("2026-01-02T09:00:00Z", res, locale, clock, zone))
            val later = when (language.wire) {
                "en" -> "Jan 3 09:00"
                else -> "1月3日 09:00"
            }
            assertEquals(later, inboxFollowTime("2026-01-03T09:00:00Z", res, locale, clock, zone))
            val nextYear = when (language.wire) {
                "en" -> "Jan 2, 2027 09:00"
                else -> "2027年1月2日 09:00"
            }
            assertEquals(nextYear, inboxFollowTime("2027-01-02T09:00:00Z", res, locale, clock, zone))

            assertEquals("10:40", displayTime("2026-01-01T10:40:00Z", locale, res, zone, clock))
            val yesterday = when (language.wire) {
                "en" -> "Yesterday"
                "zh-Hans" -> "昨天"
                else -> "昨日"
            }
            assertEquals(yesterday, displayTime("2025-12-31T10:40:00Z", locale, res, zone, clock))
            val weekday = when (language.wire) {
                "en" -> "Tue"
                "zh-Hans" -> "周二"
                else -> "火"
            }
            assertEquals(weekday, displayTime("2025-12-30T10:40:00Z", locale, res, zone, clock))
            val monthDay = when (language.wire) {
                "en" -> "Dec 25"
                else -> "12月25日"
            }
            assertEquals(monthDay, displayTime("2025-12-25T10:40:00Z", locale, res, zone,
                Instant.parse("2025-12-31T12:00:00Z")))
            assertEquals("2025-12-25", displayTime("2025-12-25T10:40:00Z", locale, res, zone, clock))
        }
    }

    @Test fun sharedFollowTimeFixturesMatchInEveryLanguage() {
        val root = System.getProperty("persona.repo.root")?.let(::File) ?: generateSequence(File(".").canonicalFile) { it.parentFile }
            .first { File(it, ".git").exists() && File(it, "android").isDirectory }
        val file = File(root, "spec/fixtures/inbox/time.json")
        assumeTrue(file.exists()) // The shared fixture arrives with #7203; this test exercises it as soon as it is present.
        val fixture = Json.parseToJsonElement(file.readText()).jsonObject
        for (case in fixture.getValue("cases").jsonArray.map { it.jsonObject }) for (language in languages) {
            val name = case.getValue("name").jsonPrimitive.content
            val input = case.getValue("input").jsonObject
            val expected = case.getValue("expected").jsonObject.getValue(language.wire).jsonPrimitive.content
            val actual = inboxFollowTime(input.getValue("at").jsonPrimitive.content, language.resources, language.locale,
                Instant.parse(input.getValue("now").jsonPrimitive.content), zone)
            assertEquals("$name/${language.wire}", expected, actual)
        }
    }

    @Test fun legacyThirdLinesKeepEachSourcesOwnShape() {
        val res = languages.first().resources
        val item = InboxItem("x", kind = "group", sender = "Alex", preview = "Can we move it?")
        val group = InboxRecord(item, source = "lark", isGroup = true)
        assertEquals("Alex: Can we move it?", inboxRowThirdLine(group, res))
        assertEquals("Can we move it?", inboxRowThirdLine(group.copy(isGroup = false), res))
        assertEquals("Alex: Translated", inboxRowThirdLine(group.copy(source = "mail"), res, translatedPreview = "Translated"))
        val document = group.copy(doc = InboxDocRef("https://example.com/doc", type = "docx"))
        assertEquals("${docTypeName(res, "docx")} · Can we move it?", inboxRowThirdLine(document, res))
        assertEquals("Alex: Can we move it?", inboxRowThirdLine(group.copy(moment = InboxMomentRef("post")), res))
        val connector = group.copy(source = "code", connector = InboxConnectorRef("example-org/example-repo#12"))
        assertEquals("example-org/example-repo#12 · Alex: Can we move it?", inboxRowThirdLine(connector, res))
        assertEquals("Alex: Can we move it?", inboxRowThirdLine(connector.copy(connector = InboxConnectorRef("opaque-id")), res))
        val assistant = InboxRecord(item.copy(summary = "Summary", preview = "Summary and more"), source = "assistant")
        assertEquals("", inboxRowThirdLine(assistant, res))
        assertEquals("Alex: Can we move it?", inboxRowThirdLine(group.copy(item = item.copy(summary = item.preview)), res))
    }

    @Test fun doneWordsOnlyAppearOnADoneItemAndKeepTheirTone() {
        val language = languages.first()
        val open = InboxRecord(InboxItem("open"), event = InboxEventFields(now = "done_user"))
        assertNull(inboxNowForRow(open, language.resources, language.locale, clock, zone))
        val done = open.copy(item = open.item.copy(doneAt = "2026-01-01T10:00:00Z"))
        assertEquals("You marked it done", inboxNowForRow(done, language.resources, language.locale, clock, zone))
        assertEquals(Status.Working, inboxNowStatus("waiting"))
        assertEquals(Status.NeedsYou, inboxNowStatus("confirm_reply"))
        assertEquals(Status.Muted, inboxNowStatus("done_muted"))
        assertEquals(Status.Idle, inboxNowStatus("done_user"))
    }
}
