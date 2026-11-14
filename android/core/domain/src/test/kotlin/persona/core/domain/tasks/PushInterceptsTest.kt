package persona.core.domain.tasks

import java.time.Clock
import java.time.Instant
import java.time.ZoneId
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.PushIntercept

class PushInterceptsTest {
    private val now = Instant.parse("2026-11-14T04:00:00Z")
    private val shanghai = Clock.fixed(now, ZoneId.of("Asia/Shanghai"))

    private fun record(at: String, level: String = "P1", reason: String = "ln_p1_held", title: String? = null,
        body: String? = null, related: String? = null, also: List<String>? = null,
        todo: String? = null, machine: String? = null, session: String? = null, url: String? = null) = PushIntercept(
        at = at, level = level, reason = reason, title = title, body = body, relatedKey = related, also = also,
        todoId = todo, machineId = machine, sessionId = session, url = url)

    @Test fun localDayGroupingAndSummaryFallback() {
        val groups = heldPushGroups(listOf(
            record("2026-11-13T16:30:00Z", title = "Today locally", also = listOf("Merged title")),
            record("2026-11-13T15:59:59Z", title = "Yesterday locally"),
            record("2026-11-14T01:00:00Z", level = "P2", reason = "ln_dedup_12h", body = "P2 dedup"),
            record("2026-11-14T01:01:00Z", reason = "ln_dedup_12h", title = "P1 dedup"),
            record("2026-11-14T01:02:00Z", level = "P0", reason = "ln_dedup_12h", title = "Already alerted"),
            record("invalid", title = "Bad date"),
        ), shanghai)
        assertEquals(listOf(listOf("Today locally", "Merged title"), listOf("P1 dedup")), groups.p1.map { it.summaries })
        assertEquals(listOf(listOf("P2 dedup")), groups.p2.map { it.summaries })
        assertEquals(listOf("Body only"), heldPushSummaries(record("2026-11-14T01:00:00Z", body = " Body\nonly ")))
    }

    @Test fun westernMidnightAndDST() {
        val westernClock = Clock.fixed(now, ZoneId.of("America/New_York"))
        val western = heldPushGroups(listOf(
            record("2026-11-14T03:30:00Z", title = "November 13 locally"),
            record("2026-11-14T05:00:00Z", title = "November 14 locally"),
        ), westernClock)
        assertEquals(listOf("November 13 locally"), western.p1.map { it.summaries.single() })

        val dstClock = Clock.fixed(Instant.parse("2026-03-08T18:00:00Z"), ZoneId.of("America/New_York"))
        val dst = heldPushGroups(listOf(
            record("2026-03-08T07:30:00Z", title = "Before spring forward"),
            record("2026-03-08T08:30:00Z", level = "P2", reason = "ln_p2_silent", title = "After spring forward"),
        ), dstClock)
        assertEquals(1, dst.p1.size)
        assertEquals(1, dst.p2.size)
    }

    @Test fun stableIDsIgnoreMergedTitlesAndDistinguishDuplicates() {
        val first = record("2026-11-14T02:00:00Z", title = "Same")
        val before = heldPushGroups(listOf(first, first), shanghai)
        val merged = heldPushGroups(listOf(
            record("2026-11-14T03:00:00Z", title = "New"),
            record("2026-11-14T02:00:00Z", title = "Changed", also = listOf("Merged")), first,
        ), shanghai)
        assertEquals(before.p1[0].id, merged.p1[1].id)
        assertEquals(before.p1[1].id, merged.p1[2].id)
        assertEquals(3, merged.p1.map { it.id }.toSet().size)
    }

    @Test fun destinationsAreTypedOrBoundedLegacyOnly() {
        val known = setOf("todo_1")
        assertEquals(HeldPushDestination.Todo("todo_1"),
            heldPushDestination(record("2026-11-14T02:00:00Z", todo = "todo_1"), known))
        assertEquals(HeldPushDestination.Session("mac", "s1"),
            heldPushDestination(record("2026-11-14T02:00:00Z", machine = "mac", session = "s1"), known))
        assertEquals(HeldPushDestination.Todo("todo_1"),
            heldPushDestination(record("2026-11-14T02:00:00Z", related = "todo:todo_1@2026-11-14T00:00:00Z"), known))
        assertEquals(HeldPushDestination.Url("https://example.com/pull/1"),
            heldPushDestination(record("2026-11-14T02:00:00Z", related = "https://example.com/pull/1"), known))
        listOf(
            record("2026-11-14T02:00:00Z", todo = "missing"),
            record("2026-11-14T02:00:00Z", related = "session-id-only"),
            record("2026-11-14T02:00:00Z", related = "ix:request-1"),
            record("2026-11-14T02:00:00Z", related = "notify:assistant/ext/id"),
            record("2026-11-14T02:00:00Z", related = "javascript:alert(1)"),
            record("2026-11-14T02:00:00Z", machine = "mac"),
        ).forEach { assertNull(heldPushDestination(it, known)) }
    }

    @Test fun exactDuplicatesRemainDistinct() {
        val groups = heldPushGroups(listOf(record("2026-11-14T02:00:00Z", title = "Same"),
            record("2026-11-14T02:00:00Z", title = "Same")), shanghai)
        assertEquals(2, groups.p1.map { it.id }.toSet().size)
        assertTrue(groups.p1[1].id.endsWith("#1"))
    }
}
