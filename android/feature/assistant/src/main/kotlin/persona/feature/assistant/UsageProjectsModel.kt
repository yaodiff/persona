package persona.feature.assistant

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import persona.core.contracts.api.UsageHistoryApi
import persona.core.contracts.api.UsageProjectDetail
import persona.core.contracts.api.UsageProjects

internal data class UsageProjectsState(
    val projects: UsageProjects? = null,
    val loading: Boolean = false,
    val failure: Boolean = false,
    val detailKey: String? = null,
    val detail: UsageProjectDetail? = null,
    val detailLoading: Boolean = false,
    val detailFailure: Boolean = false,
)

/** The project list and one project's detail; a failed refresh keeps the last good answer. */
internal class UsageProjectsModel(private val api: UsageHistoryApi) {
    private val mutable = MutableStateFlow(UsageProjectsState())
    val state = mutable.asStateFlow()
    private var projectsRevision = 0L
    private var detailRevision = 0L

    suspend fun loadProjects() {
        val read = ++projectsRevision
        mutable.update { it.copy(loading = true, failure = false) }
        try {
            val projects = api.usageProjects()?.takeIf { it.ranges.isNotEmpty() }
            currentCoroutineContext().ensureActive()
            if (read == projectsRevision) mutable.update {
                it.copy(projects = projects, loading = false, failure = false)
            }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            if (read == projectsRevision) mutable.update { it.copy(loading = false, failure = true) }
        }
    }

    suspend fun loadDetail(key: String) {
        require(key.isNotBlank()) { "project key is required" }
        val read = ++detailRevision
        mutable.update { state ->
            state.copy(
                detailKey = key,
                detail = state.detail.takeIf { state.detailKey == key },
                detailLoading = true,
                detailFailure = false,
            )
        }
        try {
            val detail = api.usageProjectDetail(key)
            currentCoroutineContext().ensureActive()
            if (read == detailRevision && mutable.value.detailKey == key) mutable.update {
                it.copy(detail = detail, detailLoading = false, detailFailure = false)
            }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            if (read == detailRevision && mutable.value.detailKey == key) mutable.update {
                it.copy(detailLoading = false, detailFailure = true)
            }
        }
    }

    fun clearDetail() {
        detailRevision++
        mutable.update { it.copy(detailKey = null, detail = null, detailLoading = false, detailFailure = false) }
    }
}
