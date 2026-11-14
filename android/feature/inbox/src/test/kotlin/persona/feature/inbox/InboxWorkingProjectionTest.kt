package persona.feature.inbox

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.CoreText
import persona.core.domain.inbox.InboxItem
import persona.core.domain.inbox.InboxListSettings
import persona.core.domain.inbox.buildInboxList

/** The item-based successor to the old conversation-view In progress regressions (#6650). */
class InboxWorkingProjectionTest {
    private val title = CoreText.plain("In progress")

    @Test fun workingKeepsItsPlaceAndStaysOpenWhileOtherSectionsFollowSettings() {
        val records = listOf(
            record("noise", tier = "noise", status = "noise"),
            record("working", tier = "fyi", status = InboxItem.WORKING),
            record("done", tier = "action", status = "action", doneAt = "2026-11-13T01:00:00Z"),
        )
        val settings = InboxListSettings(expanded = emptySet())
        val groups = project(records, settings)

        assertEquals(listOf(InboxItem.WORKING, "noise", "done"), groups.map { it.id })
        val working = groups.single { it.id == InboxItem.WORKING }
        assertFalse(working.folded)
        assertEquals(listOf("working"), working.visibleRows.map { it.id })
        assertTrue(groups.single { it.id == "noise" }.folded)
        assertTrue(groups.single { it.id == "noise" }.visibleRows.isEmpty())
    }

    @Test fun hubAndLegacyWorkingItemsShareOneSectionAndAppearExactlyOnce() {
        val records = listOf(
            record("legacy", tier = "action", status = null, feedId = "room", agent = InboxAgent(state = "running")),
            record("hub", tier = "fyi", status = InboxItem.WORKING, feedId = "room"),
            record("fyi", tier = "fyi", status = "fyi"),
        )
        val groups = project(records)

        assertEquals(listOf(InboxItem.WORKING, "fyi"), groups.map { it.id })
        assertEquals(listOf("legacy", "hub"), groups.single { it.id == InboxItem.WORKING }.rows.map { it.id })
        assertEquals(3, groups.flatMap { it.rows }.map { it.id }.distinct().size)
        assertEquals(1, groups.flatMap { it.rows }.count { it.id == "legacy" })
        assertEquals(1, groups.flatMap { it.rows }.count { it.id == "hub" })
    }

    @Test fun aFilterThatHidesTheWorkingItemLeavesNoEmptyInProgressSection() {
        val records = listOf(
            record("working", tier = "fyi", status = InboxItem.WORKING, account = "mail-1"),
            record("other", tier = "fyi", status = "fyi", account = "mail-2"),
        )
        val groups = project(records, InboxListSettings(source = "mail-2"))

        assertNull(groups.firstOrNull { it.id == InboxItem.WORKING })
        assertEquals(listOf("other"), groups.flatMap { it.rows }.map { it.id })
    }

    @Test fun aHubWithoutStatusOnlyKeepsWhatItListed() {
        val records = listOf(record("action", tier = "action", status = null), record("fyi", tier = "fyi", status = null))

        assertEquals(listOf("action", "fyi"), project(records).map { it.id })
    }

    private fun project(records: List<InboxRecord>, settings: InboxListSettings = InboxListSettings()) =
        inboxDisplayGroups(buildInboxList(records.map { it.item }, settings = settings), title) { row ->
            records.firstOrNull { it.item.id == row.id }?.agentWorking == true
        }

    private fun record(
        id: String, tier: String, status: String?, feedId: String? = null,
        account: String = "account", doneAt: String? = null, agent: InboxAgent? = null,
    ) = InboxRecord(
        InboxItem(id, account = account, kind = if (feedId == null) "mail" else "group", title = id,
            tier = tier, status = status, updatedAt = "2026-11-13T00:00:00Z", doneAt = doneAt),
        source = if (feedId == null) "mail" else "lark", feedId = feedId, agent = agent,
    )
}
