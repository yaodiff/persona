package persona.feature.assistant

import androidx.activity.ComponentActivity
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToIndex
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.UsageDay
import persona.core.contracts.api.UsageDays
import persona.core.contracts.api.UsageHistoryApi
import persona.core.contracts.api.UsageHour
import persona.core.contracts.api.UsageHours
import persona.core.contracts.api.UsagePart
import persona.core.contracts.api.UsagePlace
import persona.core.contracts.api.UsageProject
import persona.core.contracts.api.UsageProjectBreakdown
import persona.core.contracts.api.UsageProjectDay
import persona.core.contracts.api.UsageProjectDetail
import persona.core.contracts.api.UsageProjectHour
import persona.core.contracts.api.UsageProjectRange
import persona.core.contracts.api.UsageProjects
import persona.core.contracts.api.UsageTokens
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class UsageProjectsUiTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private class Hub(var projects: UsageProjects?) : UsageHistoryApi {
        var projectReads = 0
        var detailReads = 0
        var detailKey: String? = null
        var detailFailure: Exception? = null

        override suspend fun usageDays(days: Int) = UsageDays(
            zone = "Asia/Shanghai",
            days = listOf(UsageDay(date = "2026-11-17", input = 10, cacheWrite = 20, cacheRead = 40, output = 80)),
        )

        override suspend fun usageHours(days: Int) = UsageHours(zone = "Asia/Shanghai")

        override suspend fun usageProjects(): UsageProjects? {
            projectReads++
            return projects
        }

        override suspend fun usageProjectDetail(key: String): UsageProjectDetail {
            detailReads++
            detailKey = key
            detailFailure?.let { throw it }
            return detail(key)
        }

        private fun detail(key: String): UsageProjectDetail {
            val project = UsageProject(
                key = key,
                kind = "repo",
                group = "development",
                name = "acme/selected",
                host = "git.example",
                machines = 2,
                share = 0.4,
                tokens = tokens(40),
            )
            val breakdown = UsageProjectBreakdown(
                days = 7,
                total = tokens(100),
                share = 0.4,
                cacheHit = 0.85,
                machines = listOf(UsagePart("mac", "MacBook", tokens(60)), UsagePart("studio", "Studio", tokens(40))),
                agents = listOf(UsagePart("codex", tokens = tokens(70)), UsagePart("", tokens = tokens(30))),
                models = listOf(UsagePart("gpt-6.2-sol", tokens = tokens(100))),
                launchers = listOf(UsagePart("", tokens = tokens(100))),
                places = listOf(
                    UsagePlace("mac", "MacBook", "src/app", worktree = false, tokens = tokens(60)),
                    UsagePlace("mac", "MacBook", "src/worktree", worktree = true, tokens = tokens(40)),
                ),
            )
            return UsageProjectDetail(
                zone = "Asia/Shanghai",
                project = project,
                ranges = listOf(breakdown.copy(days = 1), breakdown.copy(days = 3), breakdown),
                days = (11..17).map { UsageProjectDay("2026-11-$it", tokens(it.toLong())) },
                hours = (0..23).map { UsageProjectHour(1_760_000_000L + it * 3600L, it, tokens(it.toLong())) },
            )
        }
    }

    @Test fun byProjectDefaultsToSevenDaysAndShowsSixBeforeExpanding() {
        val hub = Hub(projects)
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarPage(hub, onBack = {}) } }
        waitForProjects(hub)

        rule.onNodeWithText("By project").assertExists()
        rule.onNodeWithText("7 days").assertIsSelected()
        rule.onNodeWithTag("usage-project-dev-0").assertExists()
        rule.onNodeWithTag("usage-project-dev-6").assertDoesNotExist()
        rule.onNodeWithText("git.example").assertExists()
        rule.onNodeWithText("2 computers").assertExists()

        list().performScrollToNode(hasTestTag("usage-projects-more-development"))
        rule.onNodeWithTag("usage-projects-more-development").performClick()
        rule.onNodeWithTag("usage-project-dev-6").assertExists()
        rule.onNodeWithTag("usage-project-dev-7").assertExists()
        rule.onNodeWithTag("usage-projects-more-development").assertDoesNotExist()

        list().performScrollToNode(hasTestTag("usage-projects-more-other"))
        rule.onNodeWithTag("usage-projects-more-other").performClick()
        rule.onNodeWithTag("usage-project-other-6").assertExists()
    }

    @Test fun changingTheRangeUsesTheAlreadyReadAnswer() {
        val hub = Hub(projects)
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarPage(hub, onBack = {}) } }
        waitForProjects(hub)
        val reads = hub.projectReads

        rule.onNodeWithText("3 days").performClick()
        rule.onNodeWithText("3 days").assertIsSelected()
        rule.onNodeWithTag("usage-project-three").assertExists()
        rule.onNodeWithTag("usage-project-dev-0").assertDoesNotExist()
        assertEquals(reads, hub.projectReads)
    }

    @Test fun openingAProjectLoadsItsDetailAndBackKeepsTheSelectedRange() {
        val hub = Hub(projects)
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarPage(hub, onBack = {}) } }
        waitForProjects(hub)

        list().performScrollToNode(hasTestTag("usage-project-dev-0"))
        rule.onNodeWithTag("usage-project-dev-0").performClick()
        rule.onNodeWithTag("usage-project-detail").assertExists()
        rule.waitUntil(5_000) { rule.onAllNodesWithTag("usage-project-chart").fetchSemanticsNodes().isNotEmpty() }
        assertEquals("dev-0", hub.detailKey)
        rule.onNodeWithText("Each day").assertExists()
        rule.onNodeWithText("By computer").assertExists()
        rule.onNodeWithText("By agent").assertExists()

        rule.onNode(hasText("Today") and hasClickAction()).performClick()
        rule.onNode(hasText("Today") and hasClickAction()).assertIsSelected()
        rule.onNodeWithText("By the hour").assertExists()

        rule.onNodeWithContentDescription("Back").performClick()
        rule.onNodeWithTag("usage-projects").assertExists()
        rule.onNode(hasText("Today") and hasClickAction()).assertIsSelected()
    }

    @Test fun anOlderHubHidesByProjectAndKeepsTheCalendar() {
        val hub = Hub(null)
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarPage(hub, onBack = {}) } }

        rule.onNodeWithTag("assistant-usage-calendar").assertExists()
        rule.onNodeWithTag("usage-projects").assertDoesNotExist()
    }

    @Test fun anEmptyRangeShowsItsOwnEmptyState() {
        val empty = UsageProjects(ranges = (1..3).map { UsageProjectRange(days = listOf(1, 3, 7)[it - 1]) })
        val hub = Hub(empty)
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarPage(hub, onBack = {}) } }
        waitForProjects(hub)

        rule.onNodeWithText("Nothing used in these days").assertExists()
    }

    private fun list() = rule.onNode(hasScrollToIndexAction())

    private fun waitForProjects(hub: Hub) {
        rule.waitUntil(5_000) { hub.projectReads > 0 }
        rule.waitForIdle()
        list().performScrollToIndex(1)
        rule.waitUntil(5_000) { rule.onAllNodesWithTag("usage-projects").fetchSemanticsNodes().isNotEmpty() }
    }

    private companion object {
        val projects = UsageProjects(
            zone = "Asia/Shanghai",
            ranges = listOf(
                UsageProjectRange(
                    days = 1,
                    total = tokens(20),
                    projects = listOf(project("today", "Today project", "development", "repo")),
                ),
                UsageProjectRange(
                    days = 3,
                    total = tokens(30),
                    projects = listOf(project("three", "Three-day project", "development", "repo")),
                ),
                UsageProjectRange(
                    days = 7,
                    total = tokens(500),
                    projects = (0..7).map {
                        project(
                            "dev-$it",
                            "Development $it",
                            "development",
                            "repo",
                            host = if (it == 1) "git.example" else null,
                            machines = if (it == 2) 2 else 1,
                        )
                    } +
                        (0..6).map { project("other-$it", "Other $it", "other", "folder") } +
                        project("assistant", "", "unknown", "assistant"),
                    notSplit = tokens(30),
                ),
            ),
        )

        fun project(
            key: String,
            name: String,
            group: String,
            kind: String,
            host: String? = null,
            machines: Int = 1,
        ) = UsageProject(
            key = key,
            kind = kind,
            group = group,
            name = name,
            host = host ?: if (kind == "repo") "github.com" else null,
            machines = machines,
            share = 0.1,
            tokens = tokens(10),
        )

        fun tokens(input: Long) = UsageTokens(input = input, requests = 3)
    }
}
