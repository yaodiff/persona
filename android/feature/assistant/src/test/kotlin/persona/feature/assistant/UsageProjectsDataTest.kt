package persona.feature.assistant

import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.api.UsageProject
import persona.core.contracts.api.UsageProjectDetail
import persona.core.contracts.api.UsageProjectRange
import persona.core.contracts.api.UsageProjects
import persona.core.contracts.api.UsageTokens
import java.util.Locale

class UsageProjectsDataTest {
    @Test fun unknownKindsAndGroupsFallBackInsteadOfDisappearing() {
        assertEquals(UsageProjectKind.Unknown, UsageProjectKind.of("mystery"))
        assertEquals(UsageProjectKind.Unknown, UsageProjectKind.of(null))
        assertEquals(UsageProjectGroup.Unknown, UsageProjectGroup.of("mystery"))
        assertEquals(UsageProjectGroup.Other, UsageProjectGroup.of("OTHER"))
    }

    @Test fun groupsKeepTheHubsOrderAndSumEveryProject() {
        val range = UsageProjectRange(
            days = 7,
            total = UsageTokens(input = 100),
            projects = listOf(
                UsageProject(key = "dev-large", group = "development", share = 0.5, tokens = UsageTokens(input = 50)),
                UsageProject(key = "other", group = "other", share = 0.2, tokens = UsageTokens(input = 20)),
                UsageProject(key = "dev-small", group = "development", share = 0.2, tokens = UsageTokens(input = 20)),
                UsageProject(key = "unknown", group = "mystery", share = 0.05, tokens = UsageTokens(input = 5)),
            ),
            notSplit = UsageTokens(input = 5),
        )

        val groups = usageProjectGroups(range)
        assertEquals(listOf("dev-large", "dev-small"), groups.development.map { it.key })
        assertEquals(listOf("other"), groups.other.map { it.key })
        assertEquals(listOf("unknown"), groups.unknown.map { it.key })
        assertEquals(70L, groups.sum(UsageProjectGroup.Development))
        assertEquals(20L, groups.sum(UsageProjectGroup.Other))
        assertEquals(5L, groups.sum(UsageProjectGroup.Unknown))
        assertEquals(50L, usageProjectTop(range))
    }

    @Test fun sharesUseWholePercentAboveTenAndOneDecimalBelow() {
        assertEquals("42%", usageProjectShare(0.42, Locale.ENGLISH))
        assertEquals("4.5%", usageProjectShare(0.045, Locale.ENGLISH))
        assertEquals("<0.1%", usageProjectShare(0.0005, Locale.ENGLISH))
        assertEquals("0%", usageProjectShare(0.0, Locale.ENGLISH))
    }

    @Test fun aRangeOrBreakdownMissingFromAnOlderAnswerFallsBackToTheLast() {
        val first = UsageProjectRange(days = 1)
        val last = UsageProjectRange(days = 7)
        val projects = UsageProjects(ranges = listOf(first, last))
        assertEquals(last, projects.rangeAt(2))
        assertEquals(last, projects.rangeAt(99))

        val breakdown = persona.core.contracts.api.UsageProjectBreakdown(days = 7)
        val detail = UsageProjectDetail(ranges = listOf(persona.core.contracts.api.UsageProjectBreakdown(days = 1), breakdown))
        assertEquals(breakdown, detail.breakdownAt(2))
    }
}
