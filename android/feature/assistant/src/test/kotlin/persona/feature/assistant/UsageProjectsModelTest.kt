package persona.feature.assistant

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.UsageDays
import persona.core.contracts.api.UsageHistoryApi
import persona.core.contracts.api.UsageHours
import persona.core.contracts.api.UsageProject
import persona.core.contracts.api.UsageProjectBreakdown
import persona.core.contracts.api.UsageProjectDetail
import persona.core.contracts.api.UsageProjectRange
import persona.core.contracts.api.UsageProjects
import java.io.IOException

@OptIn(ExperimentalCoroutinesApi::class)
class UsageProjectsModelTest {
    private val older = UsageProjects(ranges = listOf(UsageProjectRange(
        days = 7,
        projects = listOf(UsageProject(key = "older", kind = "repo", group = "development", name = "acme/older")),
    )))
    private val newer = UsageProjects(ranges = listOf(UsageProjectRange(
        days = 7,
        projects = listOf(UsageProject(key = "newer", kind = "repo", group = "development", name = "acme/newer")),
    )))
    private val detail = UsageProjectDetail(
        project = UsageProject(key = "newer", kind = "repo", group = "development", name = "acme/newer"),
        ranges = listOf(UsageProjectBreakdown(days = 7)),
    )

    private class Hub : UsageHistoryApi {
        var projects: suspend () -> UsageProjects? = { null }
        var projectDetail: suspend (String) -> UsageProjectDetail = { error("not used") }
        var detailKeys = mutableListOf<String>()

        override suspend fun usageDays(days: Int) = UsageDays()
        override suspend fun usageHours(days: Int) = UsageHours()
        override suspend fun usageProjects() = projects()
        override suspend fun usageProjectDetail(key: String): UsageProjectDetail {
            detailKeys += key
            return projectDetail(key)
        }
    }

    @Test fun aProjectListReadPublishesAndARefreshFailureKeepsIt() = runTest {
        val hub = Hub().apply { projects = { newer } }
        val model = UsageProjectsModel(hub)

        model.loadProjects()
        assertSame(newer, model.state.value.projects)
        assertFalse(model.state.value.loading)
        assertFalse(model.state.value.failure)

        hub.projects = { throw IOException("offline") }
        model.loadProjects()
        assertSame(newer, model.state.value.projects)
        assertTrue(model.state.value.failure)
        assertFalse(model.state.value.loading)
    }

    @Test fun anOlderHubs404ClearsTheSectionWithoutAnError() = runTest {
        val hub = Hub().apply { projects = { null } }
        val model = UsageProjectsModel(hub)

        model.loadProjects()

        assertNull(model.state.value.projects)
        assertFalse(model.state.value.failure)
    }

    @Test fun onlyTheNewestProjectListReadWins() = runTest {
        val first = CompletableDeferred<UsageProjects?>()
        val second = CompletableDeferred<UsageProjects?>()
        val answers = ArrayDeque(listOf(first, second))
        val hub = Hub().apply { projects = { answers.removeFirst().await() } }
        val model = UsageProjectsModel(hub)

        val firstRead = async { model.loadProjects() }
        runCurrent()
        val secondRead = async { model.loadProjects() }
        runCurrent()
        second.complete(newer)
        secondRead.await()
        first.complete(older)
        firstRead.await()

        assertSame(newer, model.state.value.projects)
    }

    @Test fun detailLoadsItsOwnKeyAndAFailedRefreshKeepsTheLastGoodBreakdown() = runTest {
        val hub = Hub().apply { projectDetail = { detail } }
        val model = UsageProjectsModel(hub)

        model.loadDetail("newer")
        assertEquals(listOf("newer"), hub.detailKeys)
        assertSame(detail, model.state.value.detail)
        assertEquals("newer", model.state.value.detailKey)
        assertFalse(model.state.value.detailFailure)

        hub.projectDetail = { throw IOException("offline") }
        model.loadDetail("newer")
        assertSame(detail, model.state.value.detail)
        assertTrue(model.state.value.detailFailure)
        assertFalse(model.state.value.detailLoading)
    }

    @Test fun onlyTheNewestDetailReadWinsWhenAnotherProjectIsOpened() = runTest {
        val first = CompletableDeferred<UsageProjectDetail>()
        val second = CompletableDeferred<UsageProjectDetail>()
        val hub = Hub().apply {
            projectDetail = { key -> if (key == "first") first.await() else second.await() }
        }
        val model = UsageProjectsModel(hub)

        val firstRead = async { model.loadDetail("first") }
        runCurrent()
        assertTrue(model.state.value.detailLoading)
        val secondRead = async { model.loadDetail("second") }
        runCurrent()
        second.complete(detail.copy(project = detail.project.copy(key = "second")))
        secondRead.await()
        first.complete(detail.copy(project = detail.project.copy(key = "first")))
        firstRead.await()

        assertEquals("second", model.state.value.detail?.project?.key)
        assertEquals("second", model.state.value.detailKey)
    }
}
