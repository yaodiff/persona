package persona.feature.assistant

import persona.core.contracts.api.UsageProject
import persona.core.contracts.api.UsageProjectBreakdown
import persona.core.contracts.api.UsageProjectDetail
import persona.core.contracts.api.UsageProjectRange
import persona.core.contracts.api.UsageProjects
import java.text.NumberFormat
import java.util.Locale

internal const val USAGE_PROJECT_DEFAULT_RANGE = 2

internal enum class UsageProjectKind {
    Repo, Local, Folder, Home, Temp, Assistant, Unknown;

    companion object {
        fun of(raw: String?): UsageProjectKind = when (raw?.lowercase()) {
            "repo" -> Repo
            "local" -> Local
            "folder" -> Folder
            "home" -> Home
            "temp" -> Temp
            "assistant" -> Assistant
            else -> Unknown
        }
    }
}

internal enum class UsageProjectGroup {
    Development, Other, Unknown;

    companion object {
        fun of(raw: String?): UsageProjectGroup = when (raw?.lowercase()) {
            "development" -> Development
            "other" -> Other
            else -> Unknown
        }
    }
}

internal data class UsageProjectGroups(
    val development: List<UsageProject> = emptyList(),
    val other: List<UsageProject> = emptyList(),
    val unknown: List<UsageProject> = emptyList(),
    val sums: Map<UsageProjectGroup, Long> = emptyMap(),
) {
    fun list(group: UsageProjectGroup): List<UsageProject> = when (group) {
        UsageProjectGroup.Development -> development
        UsageProjectGroup.Other -> other
        UsageProjectGroup.Unknown -> unknown
    }

    fun sum(group: UsageProjectGroup): Long = sums[group] ?: 0L
}

/** The range at [index], or the last one when an older answer omits one. */
internal fun UsageProjects.rangeAt(index: Int): UsageProjectRange? =
    ranges.getOrNull(index) ?: ranges.lastOrNull()

internal fun UsageProjectDetail.breakdownAt(index: Int): UsageProjectBreakdown? =
    ranges.getOrNull(index) ?: ranges.lastOrNull()

/** A range's projects in the three groups the page draws, keeping the hub's largest-first order. */
internal fun usageProjectGroups(range: UsageProjectRange): UsageProjectGroups {
    val development = mutableListOf<UsageProject>()
    val other = mutableListOf<UsageProject>()
    val unknown = mutableListOf<UsageProject>()
    val sums = mutableMapOf(
        UsageProjectGroup.Development to 0L,
        UsageProjectGroup.Other to 0L,
        UsageProjectGroup.Unknown to 0L,
    )
    for (project in range.projects) {
        val group = UsageProjectGroup.of(project.group)
        when (group) {
            UsageProjectGroup.Development -> development += project
            UsageProjectGroup.Other -> other += project
            UsageProjectGroup.Unknown -> unknown += project
        }
        sums[group] = sums.getValue(group) + project.tokens.total
    }
    return UsageProjectGroups(development, other, unknown, sums)
}

/** A bar's denominator: the largest project, the not-split rest, or one so an empty range never divides by zero. */
internal fun usageProjectTop(range: UsageProjectRange): Long =
    maxOf(1L, range.projects.maxOfOrNull { it.tokens.total } ?: 0L, range.notSplit.total)

/** A share as the clients write it: whole above 10%, one decimal below, and "<0.1%" for a positive sliver. */
internal fun usageProjectShare(share: Double, locale: Locale): String {
    if (share > 0.0 && share < 0.001) {
        val sliver = NumberFormat.getPercentInstance(locale).apply { maximumFractionDigits = 1 }.format(0.001)
        return "<$sliver"
    }
    val format = NumberFormat.getPercentInstance(locale).apply {
        maximumFractionDigits = if (share >= 0.1) 0 else 1
    }
    return format.format(share.coerceIn(0.0, 1.0))
}
