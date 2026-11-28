package persona.feature.inbox

import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.generated.InboxStoredSources
import java.io.File

class InboxSourceAvailabilityTest {
    @Test fun sharedPresenceCases() {
        val root = System.getProperty("persona.repo.root") ?: error("persona.repo.root is required")
        // fixture-inputs: spec/fixtures/inbox-sources
        val cases = JsonCodec.parseToJsonElement(File(root, "spec/fixtures/inbox-sources/presence.json").readText())
            .jsonObject.getValue("cases").jsonArray
        for (entry in cases) {
            val case = entry.jsonObject
            val input = case.getValue("input").jsonObject
            val offers = input.getValue("offers").jsonObject
            val stored = input["stored_sources"] as? JsonObject
            val expected = case.getValue("expected").jsonObject
            val actual = inboxSourceVisibility(
                larkOffered = offers.getValue("lark").jsonPrimitive.boolean,
                mailOffered = offers.getValue("mail").jsonPrimitive.boolean,
                stored = stored?.let { InboxStoredSources(it.getValue("lark").jsonPrimitive.boolean, it.getValue("mail").jsonPrimitive.boolean) },
            )
            assertEquals(case.getValue("name").jsonPrimitive.content,
                InboxSourceVisibility(expected.getValue("lark").jsonPrimitive.boolean, expected.getValue("mail").jsonPrimitive.boolean), actual)
        }
    }

    @Test fun mergedNullIsUnknown() {
        assertEquals(InboxSourceVisibility(lark = true, mail = true),
            inboxSourceVisibility(larkOffered = false, mailOffered = false, stored = null))
    }

    @Test fun emptyTextKeepsTheOriginalOnlyWhileBothSourcesAreVisible() {
        assertEquals(R.string.inbox_empty_text, inboxEmptyText(InboxSourceVisibility(lark = true, mail = true)))
        assertEquals(R.string.inbox_empty_text_lark, inboxEmptyText(InboxSourceVisibility(lark = true, mail = false)))
        assertEquals(R.string.inbox_empty_text_mail, inboxEmptyText(InboxSourceVisibility(lark = false, mail = true)))
        assertEquals(R.string.inbox_empty_text_generic, inboxEmptyText(InboxSourceVisibility(lark = false, mail = false)))
    }

    @Test fun mailKindFilterFollowsTheOfferedTimesStoredDecision() {
        // offered × stored four combinations, plus unknown (null stored).
        val shown = listOf(
            inboxSourceVisibility(larkOffered = false, mailOffered = true, stored = InboxStoredSources(false, true)),
            inboxSourceVisibility(larkOffered = false, mailOffered = true, stored = InboxStoredSources(false, false)),
            inboxSourceVisibility(larkOffered = false, mailOffered = false, stored = InboxStoredSources(false, true)),
            inboxSourceVisibility(larkOffered = false, mailOffered = false, stored = null),
        )
        for (visibility in shown) {
            val kinds = visibleInboxFilterKinds(visibility)
            assertTrue("Mail kind stays for $visibility", "mail" in kinds)
            assertEquals(INBOX_FILTER_KIND_VALUES, kinds)
        }

        val hidden = inboxSourceVisibility(
            larkOffered = false, mailOffered = false,
            stored = InboxStoredSources(false, false),
        )
        val hiddenKinds = visibleInboxFilterKinds(hidden)
        assertTrue("Mail kind hides only without an offer and without stored mail", "mail" !in hiddenKinds)
        assertEquals(listOf("mention", "dm", "group", "agent"), hiddenKinds)
    }
}
