package persona.feature.inbox

import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.core.contracts.InboxEventFields
import persona.core.domain.inbox.InboxItem
import java.util.Locale

/**
 * F-7: whether or not the hub sends `where`, one row structure — title, the AI summary, then a third line that
 * says the latest thing. Without `where` that line keeps each source's own shape, as iOS `InboxView.from` and the
 * web `fromLine` write it; with `where` it is `where · sender: preview` (`spec/fixtures/inbox/row.json`).
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class InboxRowThirdLineReviewTest {
    // The row's own separators are the language's: ": " in English, a full-width colon in Chinese and Japanese.
    private val res: Resources = run {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val configuration = Configuration(context.resources.configuration).apply {
            setLocale(Locale.ENGLISH)
            setLayoutDirection(Locale.ENGLISH)
        }
        context.createConfigurationContext(configuration).resources
    }
    private val en = Locale.ENGLISH

    private fun record(
        kind: String = "group",
        source: String = "lark",
        sender: String? = "Alex",
        preview: String = "Can we move it to Friday?",
        summary: String? = null,
        isGroup: Boolean? = true,
        doc: InboxDocRef? = null,
        moment: InboxMomentRef? = null,
        connector: InboxConnectorRef? = null,
        where: String? = null,
    ) = InboxRecord(
        InboxItem("row", kind = kind, title = "An item", sender = sender, preview = preview, summary = summary),
        source = source, isGroup = isGroup, doc = doc, moment = moment, connector = connector, count = 1,
        event = InboxEventFields(where = where),
    )

    /** F-7's list of shapes for a hub without `where`. */
    @Test fun aHubWithoutWhereKeepsEachSourcesOwnShape() {
        // Group: "who: what".
        assertEquals("Alex: Can we move it to Friday?", inboxRowThirdLine(record(), res))
        // Direct chat: the title already is the person, so only the latest line.
        assertEquals("Can we move it to Friday?", inboxRowThirdLine(record(kind = "dm", isGroup = false), res))
        // Mail: "who: what" too.
        assertEquals("Alex: Can we move it to Friday?", inboxRowThirdLine(record(kind = "mail", source = "mail", isGroup = null), res))
        // No sender: only the line.
        assertEquals("Can we move it to Friday?", inboxRowThirdLine(record(sender = null), res))
        // A document: its type, then what it says (the preview names who).
        val docType = docTypeName(res, "docx")
        assertEquals("$docType · Can we move it to Friday?",
            inboxRowThirdLine(record(kind = "doc", doc = InboxDocRef("https://example.com/d", type = "docx")), res))
        // A moment: who commented, then the words.
        assertEquals("Alex: Can we move it to Friday?",
            inboxRowThirdLine(record(kind = "moment", moment = InboxMomentRef("p1")), res))
        // A connector whose thread names a place: the reference, then who and what.
        assertEquals("example-org/example-repo#12 · Alex: Can we move it to Friday?",
            inboxRowThirdLine(record(source = "code", connector = InboxConnectorRef("example-org/example-repo#12")), res))
        // A connector whose thread is not a reference: no leading dot, just who and what.
        assertEquals("Alex: Can we move it to Friday?",
            inboxRowThirdLine(record(source = "code", connector = InboxConnectorRef("channel-123")), res))
    }

    /** The same items with `where`: row.json's shape, and the person said once on a direct chat. */
    @Test fun aHubWithWhereUsesTheSharedLine() {
        assertEquals("Launch group · Alex: Can we move it to Friday?", inboxRowThirdLine(record(where = "Launch group"), res))
        assertEquals("Alex: Can we move it to Friday?", inboxRowThirdLine(record(kind = "dm", isGroup = false, where = "Alex"), res))
        assertEquals("Launch group · Can we move it to Friday?", inboxRowThirdLine(record(sender = null, where = "Launch group"), res))
        // where and the line both empty leaves nothing to draw.
        assertEquals("", inboxRowThirdLine(record(sender = null, preview = "", where = ""), res))
    }

    /** A translated mail's third line is the translation, never the hub's original words (F-7, as iOS and the web). */
    @Test fun aTranslatedMailShowsTheTranslation() {
        val mail = record(kind = "mail", source = "mail", isGroup = null, preview = "Can we move it to Friday?")
        assertEquals("Alex: Sie können es auf Freitag verschieben?", inboxRowThirdLine(mail, res, translatedPreview = "Sie können es auf Freitag verschieben?"))
    }

    /** The line is not drawn twice when the AI summary already is that same sentence (the assistant's own rows). */
    @Test fun aLineThatRepeatsTheSummaryIsNotDrawnAgain() {
        val assistant = record(source = "assistant", preview = "Moved the meeting", summary = "Moved the meeting")
        assertEquals("", inboxRowThirdLine(assistant, res))
        // A line that only starts with the summary is not drawn either, as the web's repeatsSummary does.
        assertEquals("", inboxRowThirdLine(assistant.copy(item = assistant.item.copy(preview = "Moved the meeting to Friday")), res))
        // Another source's identical sentence is still drawn: only the assistant's own rows dedupe (web: source === "assistant").
        assertEquals("Alex: Moved the meeting",
            inboxRowThirdLine(record(preview = "Moved the meeting", summary = "Moved the meeting"), res))
    }

    /** With `now` but no `where`, the state still reaches the row: it is not tied to the meta line's existence. */
    @Test fun aStateWithoutWhereStillHasWords() {
        val noWhere = record().copy(event = InboxEventFields(now = "confirm_reply"))
        val words = inboxNowForRow(noWhere, res, en, java.time.Instant.parse("2026-01-01T12:00:00Z"), java.time.ZoneOffset.UTC)
        assertEquals("Waiting for you to confirm a reply", words)
    }
}
