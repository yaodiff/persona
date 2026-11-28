package persona.feature.assistant

import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.UsageDay
import persona.core.contracts.api.UsageTokens
import persona.core.designsystem.AgentMarkKind
import persona.core.designsystem.AgentMarkSpec
import java.io.File

class UsageCalendarDataTest {
    @Test fun everyTokenKindCountsButRepliesDoNot() {
        val tokens = UsageTokens(input = 1, cacheWrite = 2, cacheRead = 4, output = 8, requests = 16)
        assertEquals(15L, tokens.total)
    }

    @Test fun usageAgentIdsUseProductNamesAndAndroidMarks() {
        val fixture = File(System.getProperty("persona.repo.root"), "spec/fixtures/agent-names/names.json")
        val names = JsonCodec.parseToJsonElement(fixture.readText()).jsonObject.getValue("names").jsonObject
        val brands = mapOf(
            "claude" to AgentMarkKind.Claude, "codex" to AgentMarkKind.Codex, "trae" to AgentMarkKind.Trae,
            "traex" to AgentMarkKind.Trae, "traecli" to AgentMarkKind.Trae,
            "pi" to AgentMarkKind.Pi, "opencode" to AgentMarkKind.OpenCode,
            "gemini" to AgentMarkKind.Gemini, "cursor" to AgentMarkKind.Cursor,
        )
        for ((id, name) in names) {
            val mark = requireNotNull(usageAgentMark(id))
            assertEquals(name.jsonPrimitive.content, mark.name)
            assertEquals(brands[id], mark.kind)
            assertNull(mark.surface)
        }
        assertEquals(AgentMarkSpec(AgentMarkKind.Trae, "TRAE CLI", null), usageAgentMark("trae-cli"))
        val runtime = requireNotNull(usageAgentMark("My-Runtime"))
        assertEquals("My-Runtime", runtime.name)
        assertNull(runtime.kind)
        assertNull(usageAgentMark(""))
    }

    @Test fun heatStepsRankTiesAtTheirSharedUpperRank() {
        assertEquals(
            listOf(0, 1, 3, 3, 4),
            usageHeatSteps(listOf(0, 10, 20, 20, 40)),
        )
        assertEquals(listOf(4), usageHeatSteps(listOf(7)))
        assertEquals(listOf(0, 0), usageHeatSteps(listOf(0, 0)))
    }

    @Test fun calendarStartsMondayAndEndsInTheCurrentWeek() {
        val days = (11L..17L).map { day -> UsageDay(date = "2026-11-$day", input = day) }
        val weeks = usageCalendarWeeks(days)

        assertEquals(2, weeks.size)
        assertEquals(listOf(null, null) + days.take(5), weeks[0])
        assertEquals(days.drop(5) + List(5) { null }, weeks[1])
        assertEquals("2026-11-17", weeks.last().filterNotNull().last().date)
    }

    @Test fun malformedDatesAreSkippedWithoutChangingTheValidWeek() {
        val monday = UsageDay(date = "2026-11-16", input = 16)
        val wednesday = UsageDay(date = "2026-11-18", input = 18)
        val malformed = listOf(UsageDay(date = ""), UsageDay(date = "not-a-date"))
        val result = runCatching { usageCalendarWeeks(listOf(monday) + malformed + listOf(wednesday)) }
        assertTrue("malformed hub dates must not throw: ${result.exceptionOrNull()}", result.isSuccess)
        val weeks = result.getOrThrow()

        assertEquals(1, weeks.size)
        assertEquals(listOf(monday, null, wednesday, null, null, null, null), weeks.single())
    }
}
