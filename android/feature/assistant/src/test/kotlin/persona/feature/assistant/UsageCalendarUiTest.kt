package persona.feature.assistant

import android.content.res.Configuration
import androidx.activity.ComponentActivity
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.click
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Machine
import persona.core.contracts.api.UsageAgentShare
import persona.core.contracts.api.UsageDay
import persona.core.contracts.api.UsageDays
import persona.core.contracts.api.UsageHistoryApi
import persona.core.contracts.api.UsageHours
import persona.core.contracts.api.UsageMachineShare
import persona.core.contracts.api.UsageProjectDetail
import persona.core.contracts.api.UsageProjects
import persona.core.contracts.api.UsageTokens
import persona.core.designsystem.AppStyle
import persona.core.designsystem.AppStyleStore
import persona.core.designsystem.LayoutDensity
import persona.core.designsystem.PersonaTheme
import java.io.IOException
import java.time.DayOfWeek
import java.time.LocalDate

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class UsageCalendarUiTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val days = UsageDays(zone = "Asia/Shanghai", stored = true, days = listOf(
        UsageDay(date = "2026-11-16", input = 1, cacheWrite = 2, cacheRead = 4, output = 8),
        UsageDay(date = "2026-11-17", input = 10, cacheWrite = 20, cacheRead = 40, output = 80),
    ))
    private val hours = UsageHours(zone = "Asia/Shanghai", stored = true,
        hours = listOf(persona.core.contracts.api.UsageHour(t = 1_760_000_000, date = "2026-11-17", hour = 9, input = 6, cacheWrite = 8, cacheRead = 10, output = 12)),
        machines = listOf(UsageMachineShare(id = "office", name = "Office", input = 6, cacheWrite = 8, cacheRead = 10, output = 12)),
        total = UsageTokens(input = 6, cacheWrite = 8, cacheRead = 10, output = 12),
    )
    private val longDays = UsageDays(zone = "Asia/Shanghai", stored = true, days = (0 until 371).map { offset ->
        val date = LocalDate.of(2026, 11, 17).minusDays((370 - offset).toLong())
        UsageDay(date.toString(), input = (offset % 9 * 700).toLong(), cacheWrite = 2, cacheRead = 4, output = 8)
    })

    @After fun resetDensity() {
        AppStyleStore(ApplicationProvider.getApplicationContext()).setDensity(LayoutDensity.Standard)
    }

    private open class Hub(
        private val days: UsageDays,
        private val hours: UsageHours,
    ) : UsageHistoryApi {
        var dayReads = 0
        var hourReads = 0
        var failure: Exception? = null
        override suspend fun usageDays(days: Int): UsageDays {
            dayReads++
            failure?.let { throw it }
            return this.days
        }
        override suspend fun usageHours(days: Int): UsageHours {
            hourReads++
            failure?.let { throw it }
            return this.hours
        }
        override suspend fun usageProjects(): UsageProjects? = null
        override suspend fun usageProjectDetail(key: String): UsageProjectDetail = error("not used")
    }

    @Test fun aDayCanBeSelectedAndTheCardOpensDetails() {
        var opened = 0
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarCard(days, onOpen = { opened++ }) } }

        rule.onNodeWithTag("assistant-usage-calendar").assertExists()
        rule.onNodeWithText("Today", substring = true).assertExists()
        rule.onNodeWithText("Last 30 days", substring = true).assertExists()
        rule.onNodeWithTag("usage-day-2026-11-16").performClick()
        rule.onNodeWithText("Nov 16", substring = true).assertExists()
        rule.onNodeWithText("Input", substring = true).assertExists()
        rule.onNodeWithText("Cache write", substring = true).assertExists()
        rule.onNodeWithText("Cache read", substring = true).assertExists()
        rule.onNodeWithText("Output", substring = true).assertExists()

        rule.onNodeWithTag("usage-day-2026-11-16").performClick()
        rule.onNodeWithText("Today", substring = true).assertExists()
        rule.onAllNodesWithText("Cache write", substring = true).assertCountEquals(0)
        rule.onNodeWithTag("usage-calendar-open").performClick()
        assertEquals(1, opened)
    }

    @Test fun homeKeepsChipsUntilDataArrivesThenReplacesThem() {
        val answer = CompletableDeferred<UsageDays>()
        val hub = object : Hub(days, hours) {
            override suspend fun usageDays(days: Int): UsageDays {
                dayReads++
                return answer.await()
            }
        }
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                UsageHomeSection(hub, listOf(Machine("mac", name = "MacBook Pro", online = true)), true, onOpen = {})
            }
        }

        rule.onNodeWithText("Online devices").assertExists()
        rule.onNodeWithText("MacBook Pro").assertExists()
        rule.onAllNodesWithTag("assistant-usage-calendar").assertCountEquals(0)
        rule.runOnIdle { answer.complete(days) }
        rule.waitForIdle()
        rule.onNodeWithTag("assistant-usage-calendar").assertExists()
        rule.onAllNodesWithText("Online devices").assertCountEquals(0)
        rule.onAllNodesWithText("MacBook Pro").assertCountEquals(0)
    }

    @Test fun aFailedFirstReadLeavesTheOriginalDevicesInsteadOfAnEmptyCalendar() {
        val hub = Hub(days, hours).apply { failure = IOException("offline") }
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                UsageHomeSection(hub, listOf(Machine("mac", name = "MacBook Pro", online = true)), true, onOpen = {})
            }
        }

        rule.waitForIdle()
        rule.onNodeWithText("Online devices").assertExists()
        rule.onNodeWithText("MacBook Pro").assertExists()
        rule.onAllNodesWithTag("assistant-usage-calendar").assertCountEquals(0)
    }

    @Test fun anEmptyRefreshKeepsTheVisibleHomeCalendar() {
        val answers = ArrayDeque(listOf(days, days.copy(days = emptyList())))
        val hub = object : Hub(days, hours) {
            override suspend fun usageDays(days: Int): UsageDays {
                dayReads++
                return answers.removeFirst()
            }
        }
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                UsageHomeSection(hub, listOf(Machine("office", name = "Office", online = true)), true, onOpen = {})
            }
        }
        rule.onNodeWithTag("assistant-usage-calendar").assertExists()

        rule.activityRule.scenario.moveToState(Lifecycle.State.CREATED)
        rule.activityRule.scenario.moveToState(Lifecycle.State.RESUMED)
        rule.waitForIdle()

        assertEquals(2, hub.dayReads)
        rule.onNodeWithTag("assistant-usage-calendar").assertExists()
        rule.onAllNodesWithText("Online devices").assertCountEquals(0)
    }

    @Test fun detailsShowHoursComputersAndAllFourKinds() {
        val hub = Hub(days, hours)
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarPage(hub, onBack = {}) } }

        rule.waitForIdle()
        rule.onNodeWithText("Last 7 days by hour", substring = true).assertExists()
        rule.onNodeWithText("By computer", substring = true).assertExists()
        rule.onNodeWithText("Office").assertExists()
        rule.onNodeWithText("By Agent · Last 7 Days").assertDoesNotExist()
        rule.onNodeWithText("By kind", substring = true).assertExists()
        rule.onNodeWithText("Input", substring = true).assertExists()
        rule.onNodeWithText("Cache write", substring = true).assertExists()
        rule.onNodeWithText("Cache read", substring = true).assertExists()
        rule.onNodeWithText("Output", substring = true).assertExists()
    }

    @Test fun agentSharesShowLocalizedTotalsAndKeepHistorySeparateFromLiveAgents() {
        val shares = listOf(
            UsageAgentShare("claude", input = 8_000, cacheWrite = 2_000, cacheRead = 1_500, output = 500, requests = 18_000),
            UsageAgentShare("", cacheRead = 500),
            UsageAgentShare("Custom-CLI", output = 250),
            UsageAgentShare("request-only", requests = 100),
        )
        val hub = Hub(days, hours.copy(agents = shares))
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarPage(hub, onBack = {}) } }
        val list = rule.onNode(hasScrollToIndexAction())
        list.performScrollToNode(hasText("By Agent · Last 7 Days"))
        listOf("Claude Code, 12,000 tokens", "Not split (before the update), 500 tokens", "Custom-CLI, 250 tokens").forEach {
            list.performScrollToNode(hasContentDescription(it))
            rule.onNodeWithContentDescription(it).assertExists()
        }
        rule.onNodeWithText("12K", useUnmergedTree = true).assertExists()
        rule.onNodeWithContentDescription("request-only", substring = true).assertDoesNotExist()
        list.performScrollToNode(hasText("By kind"))
    }

    @Test fun aRequestOnlyWeekHidesTheEntireAgentSection() {
        val hub = Hub(days, hours.copy(agents = listOf(UsageAgentShare("codex", requests = 25))))
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarPage(hub, onBack = {}) } }
        rule.onNodeWithText("By computer").assertExists()
        rule.onNodeWithText("By kind").assertExists()
        rule.onNodeWithText("By Agent · Last 7 Days").assertDoesNotExist()
    }

    @Test fun blankComputerIdsDoNotCollide() = computersWithIdsStayVisible("", "")

    @Test fun duplicateComputerIdsDoNotCollide() = computersWithIdsStayVisible("office", "office")

    private fun computersWithIdsStayVisible(vararg ids: String) {
        val machines = ids.mapIndexed { index, id ->
            UsageMachineShare(id = id, name = "Computer $index", input = index + 1L)
        }
        val failure = runCatching {
            rule.setContent {
                PersonaTheme(reduceMotion = true) {
                    UsageCalendarPage(Hub(days, hours.copy(machines = machines)), onBack = {})
                }
            }
            rule.waitForIdle()
        }.exceptionOrNull()

        assertTrue("duplicate or blank ids must not crash details: $failure", failure == null)
        ids.indices.forEach { index -> rule.onNodeWithText("Computer $index").assertExists() }
    }

    @Test fun disabledDayControlDoesNotOpenDetailsAndEnabledControlStillChangesDay() {
        var opened = 0
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarCard(days, onOpen = { opened++ }) } }
        rule.onNodeWithTag("usage-day-2026-11-16").performClick()

        rule.onNodeWithContentDescription("Previous day").performTouchInput { click(center) }
        rule.waitForIdle()
        assertEquals("A disabled boundary button must not open details", 0, opened)
        rule.onNodeWithText("Nov 16", substring = true).assertExists()

        rule.onNodeWithContentDescription("Next day").performTouchInput { click(center) }
        rule.waitForIdle()
        rule.onNodeWithText("Nov 17", substring = true).assertExists()
        assertEquals("An enabled day button changes the day without opening details", 0, opened)
    }

    @Test fun largeTextWrapsSummaryInsteadOfEllipsizingTheThirdLabel() {
        largeTextDetails()
        val today = rule.onNodeWithText("Today").getBoundsInRoot().top
        val last7 = rule.onNodeWithText("Last 7 days").getBoundsInRoot().top
        val last30 = rule.onNodeWithText("Last 30 days").getBoundsInRoot().top

        assertEquals("The first two stats stay on one row", today, last7)
        assertTrue("The third stat wraps at 2.0 font scale", last30 > today)
    }

    @Test fun largeTextGivesHourDatesEnoughWidthForTheWholeDay() {
        largeTextDetails()
        val bounds = rule.onNodeWithText("Nov 17").getBoundsInRoot()
        val width = bounds.right - bounds.left
        val required = 112.dp

        assertTrue("The date column grows with the 2.0 font scale: $width < $required", width >= required)
    }

    @Test fun largeTextGivesMonthLabelsEnoughHeight() {
        largeTextDetails()
        val monthTop = rule.onNodeWithText("Nov").getBoundsInRoot().top
        val dayTop = rule.onNodeWithTag("usage-day-2026-11-17").getBoundsInRoot().top
        val rowHeight = dayTop - monthTop

        assertTrue("The month row grows to 40dp at 2.0 font scale: $rowHeight", rowHeight >= 40.dp)
    }

    @Test fun emptyAndInvalidDayZonesDoNotCrashTheCard() {
        var zone by mutableStateOf("")
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarCard(days.copy(zone = zone), onOpen = null) } }

        rule.onNodeWithTag("assistant-usage-calendar").assertExists()
        rule.runOnIdle { zone = "invalid/zone" }
        rule.waitForIdle()
        rule.onNodeWithTag("assistant-usage-calendar").assertExists()
    }

    @Test fun emptyAndInvalidHourZonesDoNotCrashDetails() {
        var zone by mutableStateOf("")
        rule.setContent {
            PersonaTheme(reduceMotion = true) { UsageCalendarPage(Hub(days, hours.copy(zone = zone)), onBack = {}) }
        }

        rule.onNodeWithText("Last 7 days by hour", substring = true).assertExists()
        rule.runOnIdle { zone = "invalid/zone" }
        rule.waitForIdle()
        rule.onNodeWithText("Last 7 days by hour", substring = true).assertExists()
    }

    @Test fun malformedDayAndHourDatesAreIgnoredInsteadOfCrashingDetails() {
        val invalidDays = days.copy(days = days.days + days.days.first().copy(date = ""))
        val invalidHours = hours.copy(hours = hours.hours + hours.hours.single().copy(t = 1_760_000_100, date = "not-a-date"))
        rule.setContent {
            PersonaTheme(reduceMotion = true) { UsageCalendarPage(Hub(invalidDays, invalidHours), onBack = {}) }
        }

        rule.onNodeWithText("Last 7 days by hour", substring = true).assertExists()
        rule.onNodeWithTag("usage-day-2026-11-17").assertExists()
        rule.onNodeWithTag("usage-day-").assertDoesNotExist()
    }

    @Test fun compactDeviceChipsUseTheSpaceTokenOnlyOnce() {
        AppStyleStore(ApplicationProvider.getApplicationContext()).setDensity(LayoutDensity.Compact)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                UsageHomeSection(null, listOf(
                    Machine("a", name = "A", online = true), Machine("b", name = "B", online = true),
                ), null, onOpen = {})
            }
        }

        val first = rule.onNodeWithTag("usage-online-device-a").getBoundsInRoot()
        val second = rule.onNodeWithTag("usage-online-device-b").getBoundsInRoot()
        assertEquals("the fixture keeps both chips on one row", first.top, second.top)
        assertEquals("Compact keeps the 4dp single-scaled gap", 4f, (second.left - first.right).value, 0.01f)
    }

    @Test fun theFirstMonthStaysWholeInsideTheScrolledViewport() {
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarCard(longDays, onOpen = null) } }
        rule.waitForIdle()

        val viewport = rule.onNodeWithTag("usage-calendar-months").getBoundsInRoot()
        val month = rule.onNodeWithTag("usage-month-2026-07", useUnmergedTree = true).getUnclippedBoundsInRoot()
        assertTrue("Jul must not start left of the visible month header: ${month.left} < ${viewport.left}", month.left >= viewport.left)
        assertTrue("Jul must not end right of the visible month header", month.right <= viewport.right)
    }

    @Test fun largeMonthLabelsDoNotOverflowInAnyStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent {
            val base = LocalConfiguration.current
            val configuration = Configuration(base).apply { fontScale = 2f }
            PersonaTheme(reduceMotion = true, style = style) {
                CompositionLocalProvider(
                    LocalConfiguration provides configuration,
                    LocalDensity provides Density(LocalDensity.current.density, 2f),
                ) { UsageCalendarCard(longDays, onOpen = null) }
            }
        }

        for (nextStyle in AppStyle.entries) {
            rule.runOnIdle { style = nextStyle }
            rule.waitForIdle()
            val header = rule.onNodeWithTag("usage-calendar-months").getBoundsInRoot()
            for (month in listOf("2026-09", "2026-10", "2026-11")) {
                val tag = "usage-month-$month"
                val bounds = rule.onNodeWithTag(tag, useUnmergedTree = true).getUnclippedBoundsInRoot()
                assertTrue("$nextStyle keeps $month left of the header boundary", bounds.left >= header.left)
                assertTrue("$nextStyle keeps $month inside the right boundary", bounds.right <= header.right)
                val layout = textLayoutTag(tag)
                assertEquals("$nextStyle keeps $month on one line", 1, layout.lineCount)
                assertFalse("$nextStyle ellipsizes $month at 2.0 font scale", layout.isLineEllipsized(0))
            }
            rule.onNodeWithTag("usage-month-2026-08", useUnmergedTree = true).assertDoesNotExist()
        }
    }

    @Test fun weekdayLabelsShareTheDayCellRowCenters() {
        rule.setContent { PersonaTheme(reduceMotion = true) { UsageCalendarCard(longDays, onOpen = null) } }
        rule.waitForIdle()

        for ((day, date) in listOf(
            DayOfWeek.MONDAY to "2026-11-16",
            DayOfWeek.WEDNESDAY to "2026-11-11",
            DayOfWeek.FRIDAY to "2026-11-13",
        )) {
            val label = rule.onNodeWithTag("usage-weekday-${day.name}", useUnmergedTree = true).getUnclippedBoundsInRoot()
            val cell = rule.onNodeWithTag("usage-day-$date").getBoundsInRoot()
            val labelCenter = (label.top + label.bottom).value / 2
            val cellCenter = (cell.top + cell.bottom).value / 2
            assertEquals("$day must be centered on its actual day-cell row", cellCenter, labelCenter, 0.01f)
        }
    }

    @Test
    @Config(sdk = [35], qualifiers = "ja-w400dp-h900dp-xhdpi")
    fun japaneseWeekdaysStayWholeInsideTheirColumnAtDefaultAndLargeText() {
        var fontScale by mutableStateOf(1f)
        rule.setContent {
            val base = LocalConfiguration.current
            val configuration = Configuration(base).apply { this.fontScale = fontScale }
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(
                    LocalConfiguration provides configuration,
                    LocalDensity provides Density(LocalDensity.current.density, fontScale),
                ) { UsageCalendarCard(longDays, onOpen = null) }
            }
        }

        for (scale in listOf(1f, 2f)) {
            rule.runOnIdle { fontScale = scale }
            rule.waitForIdle()
            val column = rule.onNodeWithTag("usage-calendar-weekdays").getBoundsInRoot()
            for (day in listOf("MONDAY", "WEDNESDAY", "FRIDAY")) {
                val tag = "usage-weekday-$day"
                val text = rule.onNodeWithTag(tag, useUnmergedTree = true)
                val bounds = text.getUnclippedBoundsInRoot()
                assertTrue("$scale $day starts outside its weekday column", bounds.left >= column.left)
                assertTrue("$scale $day ends outside its weekday column", bounds.right <= column.right)
                val layout = textLayoutTag(tag)
                assertEquals("$scale $day stays on one line", 1, layout.lineCount)
                assertFalse("$scale $day is ellipsized", layout.isLineEllipsized(0))
                assertTrue(
                    "$scale $day line width ${layout.getLineRight(0)} exceeds layout width ${layout.size.width}",
                    layout.getLineRight(0) <= layout.size.width + 0.01f,
                )
                val lastGlyph = layout.getBoundingBox(layout.layoutInput.text.length - 1)
                assertTrue(
                    "$scale $day last glyph $lastGlyph exceeds layout width ${layout.size.width}",
                    lastGlyph.left >= 0f && lastGlyph.right <= layout.size.width + 0.01f,
                )
            }
        }
    }

    private fun textLayoutTag(tag: String): TextLayoutResult = textLayout(rule.onNodeWithTag(tag, useUnmergedTree = true))
    private fun textLayout(node: SemanticsNodeInteraction): TextLayoutResult {
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = node.fetchSemanticsNode().config.getOrNull(SemanticsActions.GetTextLayoutResult)?.action?.invoke(layouts)
        return layouts.single()
    }

    private fun largeTextDetails() {
        rule.setContent {
            val base = LocalConfiguration.current
            val configuration = Configuration(base).apply { fontScale = 2f }
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(
                    LocalConfiguration provides configuration,
                    LocalDensity provides Density(LocalDensity.current.density, 2f),
                ) {
                    UsageCalendarPage(Hub(days, hours), onBack = {})
                }
            }
        }
        rule.waitForIdle()
    }
}
