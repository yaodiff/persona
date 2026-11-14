package persona.core.domain.inbox

import persona.core.domain.sort.ListFixture
import persona.core.domain.sort.listFixtures
import persona.core.domain.sort.obj
import persona.core.domain.sort.objects
import persona.core.domain.sort.optional
import persona.core.domain.sort.string
import org.junit.Assert.assertEquals
import org.junit.Test

class InboxFollowListTest {
    private fun matchingIds(items: List<InboxItem>, query: String): List<String> =
        buildInboxList(items, settings = InboxListSettings(query = query)).groups.flatMap { it.rows }.map { it.id }

    @Test fun whereExtendsSearchWithoutReadingContextOrReason() {
        val fixture = listFixtures("inbox").asSequence().map { it[0] as ListFixture }
            .single { it.name == "filters.json/search_four_fields_only" }
        val items = fixture.input.objects("items").map { value ->
            InboxItem(
                id = value.string("id"), account = value.string("account"), kind = value.string("kind"),
                title = value.string("title"), sender = value.optional("sender"), preview = value.string("preview"),
                summary = value.optional("summary"), tier = value.optional("tier"),
                updatedAt = value.optional("updated_at"), doneAt = value.optional("done_at"),
                status = value.optional("status"), urgent = value.optional("urgent") == "true",
                where = value.optional("where"),
            )
        }
        val query = fixture.input.obj("settings").string("query")
        val originalFields = listOf("title", "sender", "preview", "summary")
        // Without where, the shared case still matches only its original four fields, never context or reason.
        assertEquals(originalFields, matchingIds(items, query))

        val withWhere = items + InboxItem(
            id = "where", account = "feishu", kind = "dm", title = "Another item",
            preview = "Another preview", tier = "action", where = "needle",
        )
        assertEquals(originalFields + "where", matchingIds(withWhere, query))
    }
}
