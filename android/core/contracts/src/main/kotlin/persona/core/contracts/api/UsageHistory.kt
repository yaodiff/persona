package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** The four token kinds the hub reports for a day, an hour, a computer, or an agent. */
interface UsageTokenValues {
    val input: Long
    val cacheWrite: Long
    val cacheRead: Long
    val output: Long

    /** Every token kind, cache reads included; request counts are not tokens. */
    val total: Long get() = input + cacheWrite + cacheRead + output
}

@Serializable
data class UsageTokens(
    @SerialName("in") override val input: Long = 0,
    @SerialName("cache_write") override val cacheWrite: Long = 0,
    @SerialName("cache_read") override val cacheRead: Long = 0,
    @SerialName("out") override val output: Long = 0,
    @SerialName("req") val requests: Int = 0,
) : UsageTokenValues

@Serializable
data class UsageDay(
    val date: String = "",
    @SerialName("in") override val input: Long = 0,
    @SerialName("cache_write") override val cacheWrite: Long = 0,
    @SerialName("cache_read") override val cacheRead: Long = 0,
    @SerialName("out") override val output: Long = 0,
    @SerialName("req") val requests: Int = 0,
) : UsageTokenValues

@Serializable
data class UsageDays(
    val zone: String = "",
    val days: List<UsageDay> = emptyList(),
    val stored: Boolean = false,
)

@Serializable
data class UsageHour(
    val t: Long = 0,
    val date: String = "",
    val hour: Int = 0,
    @SerialName("in") override val input: Long = 0,
    @SerialName("cache_write") override val cacheWrite: Long = 0,
    @SerialName("cache_read") override val cacheRead: Long = 0,
    @SerialName("out") override val output: Long = 0,
    @SerialName("req") val requests: Int = 0,
) : UsageTokenValues

@Serializable
data class UsageMachineShare(
    val id: String = "",
    val name: String = "",
    @SerialName("in") override val input: Long = 0,
    @SerialName("cache_write") override val cacheWrite: Long = 0,
    @SerialName("cache_read") override val cacheRead: Long = 0,
    @SerialName("out") override val output: Long = 0,
    @SerialName("req") val requests: Int = 0,
) : UsageTokenValues

/** The empty agent is usage from before the hub tracked agents separately. */
@Serializable
data class UsageAgentShare(
    val agent: String = "",
    @SerialName("in") override val input: Long = 0,
    @SerialName("cache_write") override val cacheWrite: Long = 0,
    @SerialName("cache_read") override val cacheRead: Long = 0,
    @SerialName("out") override val output: Long = 0,
    @SerialName("req") val requests: Int = 0,
) : UsageTokenValues

@Serializable
data class UsageHours(
    val zone: String = "",
    val hours: List<UsageHour> = emptyList(),
    val machines: List<UsageMachineShare> = emptyList(),
    val total: UsageTokens = UsageTokens(),
    val stored: Boolean = false,
    val agents: List<UsageAgentShare> = emptyList(),
)

/** One project's tokens in one of the three ranges. */
@Serializable
data class UsageProject(
    val key: String = "",
    val kind: String = "unknown",
    val group: String = "unknown",
    val name: String = "",
    val host: String? = null,
    val machines: Int = 0,
    val share: Double = 0.0,
    val tokens: UsageTokens = UsageTokens(),
)

@Serializable
data class UsageProjectRange(
    val days: Int = 0,
    val total: UsageTokens = UsageTokens(),
    val projects: List<UsageProject> = emptyList(),
    @SerialName("not_split") val notSplit: UsageTokens = UsageTokens(),
)

@Serializable
data class UsageProjects(
    val zone: String = "",
    val ranges: List<UsageProjectRange> = emptyList(),
    val stored: Boolean = false,
)

/** One named part of a project's breakdown. */
@Serializable
data class UsagePart(
    val key: String = "",
    val name: String? = null,
    val tokens: UsageTokens = UsageTokens(),
)

/** One folder or linked worktree a project used on a computer. */
@Serializable
data class UsagePlace(
    val machine: String = "",
    @SerialName("machine_name") val machineName: String? = null,
    val place: String = "",
    val worktree: Boolean = false,
    val tokens: UsageTokens = UsageTokens(),
)

@Serializable
data class UsageProjectBreakdown(
    val days: Int = 0,
    val total: UsageTokens = UsageTokens(),
    val share: Double = 0.0,
    @SerialName("cache_hit") val cacheHit: Double = 0.0,
    val machines: List<UsagePart> = emptyList(),
    val agents: List<UsagePart> = emptyList(),
    val models: List<UsagePart> = emptyList(),
    val launchers: List<UsagePart> = emptyList(),
    val places: List<UsagePlace> = emptyList(),
)

@Serializable
data class UsageProjectDay(
    val date: String = "",
    val tokens: UsageTokens = UsageTokens(),
)

@Serializable
data class UsageProjectHour(
    val t: Long = 0,
    val hour: Int = 0,
    val tokens: UsageTokens = UsageTokens(),
)

@Serializable
data class UsageProjectDetail(
    val zone: String = "",
    val project: UsageProject = UsageProject(),
    val ranges: List<UsageProjectBreakdown> = emptyList(),
    val days: List<UsageProjectDay> = emptyList(),
    val hours: List<UsageProjectHour> = emptyList(),
    val stored: Boolean = false,
)

/** Token history for the selected assistant's manager. */
interface UsageHistoryApi {
    /** GET /api/v1/usage/days: at most 371 complete daily buckets, oldest first and today last. */
    suspend fun usageDays(days: Int = 371): UsageDays

    /** GET /api/v1/usage/hours: one to seven days by the clock, including repeated clock-change hours. */
    suspend fun usageHours(days: Int = 7): UsageHours

    /** GET /api/v1/usage/projects: today, the last 3 and 7 days; null from a hub before the route. */
    suspend fun usageProjects(): UsageProjects?

    /** GET /api/v1/usage/projects/detail: one project's same three ranges and its days, hours and parts. */
    suspend fun usageProjectDetail(key: String): UsageProjectDetail
}
