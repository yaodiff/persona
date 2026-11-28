package persona.feature.assistant

import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.UsageDay
import persona.core.contracts.api.UsageDays
import persona.core.contracts.api.UsageHistoryApi
import persona.core.contracts.api.UsageHours
import persona.core.contracts.api.UsageProjectDetail
import persona.core.contracts.api.UsageProjects
import java.io.IOException

class UsageCalendarModelTest {
    private val days = UsageDays(zone = "Asia/Shanghai", days = listOf(UsageDay(date = "2026-11-17", input = 5)))
    private val hours = UsageHours(zone = "Asia/Shanghai")

    private class Hub(
        var days: UsageDays,
        var hours: UsageHours,
    ) : UsageHistoryApi {
        var daysFailure: Exception? = null
        var hoursFailure: Exception? = null
        var dayReads = 0
        var hourReads = 0

        override suspend fun usageDays(days: Int): UsageDays {
            dayReads++
            daysFailure?.let { throw it }
            return this.days
        }

        override suspend fun usageHours(days: Int): UsageHours {
            hourReads++
            hoursFailure?.let { throw it }
            return this.hours
        }

        override suspend fun usageProjects(): UsageProjects? = null

        override suspend fun usageProjectDetail(key: String): UsageProjectDetail = error("not used")
    }

    @Test fun aSuccessfulHomeReadPublishesTheDays() = runTest {
        val hub = Hub(days, hours)
        val model = UsageCalendarModel(hub)

        model.loadDays()

        assertSame(days, model.state.value.days)
        assertEquals(1, hub.dayReads)
        assertEquals(0, hub.hourReads)
        assertFalse(model.state.value.loading)
        assertFalse(model.state.value.failure)
    }

    @Test fun aFailedRefreshKeepsTheLastDataAndRetryRecovers() = runTest {
        val hub = Hub(days, hours)
        val model = UsageCalendarModel(hub)
        model.loadDays()

        hub.daysFailure = IOException("offline")
        model.loadDays()
        assertSame(days, model.state.value.days)
        assertTrue(model.state.value.failure)
        assertFalse(model.state.value.loading)

        hub.daysFailure = null
        model.loadDays()
        assertSame(days, model.state.value.days)
        assertFalse(model.state.value.failure)
        assertEquals(3, hub.dayReads)
    }

    @Test fun anEmptyRefreshKeepsTheLastNonemptyAnswerButAnInitialEmptyAnswerStaysEmpty() = runTest {
        val hub = Hub(days, hours)
        val model = UsageCalendarModel(hub)
        model.loadDays()

        hub.days = days.copy(days = emptyList())
        model.loadDays()
        assertSame(days, model.state.value.days)
        assertFalse(model.state.value.failure)

        val empty = days.copy(days = emptyList())
        val initial = UsageCalendarModel(Hub(empty, hours))
        initial.loadDays()
        assertSame(empty, initial.state.value.days)
    }

    @Test fun theDetailsReadIncludesTheHourlyBreakdown() = runTest {
        val hub = Hub(days, hours)
        val model = UsageCalendarModel(hub)

        model.loadDetails()

        assertSame(days, model.state.value.days)
        assertSame(hours, model.state.value.hours)
        assertEquals(1, hub.dayReads)
        assertEquals(1, hub.hourReads)
    }
}
