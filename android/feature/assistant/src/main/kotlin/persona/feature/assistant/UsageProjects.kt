@file:OptIn(androidx.compose.foundation.layout.ExperimentalLayoutApi::class)

package persona.feature.assistant

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.api.UsagePart
import persona.core.contracts.api.UsagePlace
import persona.core.contracts.api.UsageProject
import persona.core.contracts.api.UsageProjectBreakdown
import persona.core.contracts.api.UsageProjectDetail
import persona.core.contracts.api.UsageProjectRange
import persona.core.contracts.api.UsageProjects
import persona.core.contracts.api.UsageTokens
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.models.AgentModelPanel
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.util.Locale

private const val USAGE_PROJECTS_FIRST = 6

private data class UsageProjectKindStyle(
    val label: Int,
    val value: Long,
    val color: Color,
)

private data class UsageProjectColumn(
    val key: String,
    val day: String,
    val hour: String?,
    val axis: String,
    val tokens: UsageTokens,
)

/** The Token usage page's By project section: ranges, the development split, groups, rows and the not-split tail. */
@Composable
internal fun UsageProjectsSection(
    data: UsageProjects,
    range: Int,
    onRange: (Int) -> Unit,
    onOpen: (UsageProject) -> Unit,
) {
    val selected = data.rangeAt(range) ?: return
    Column(Modifier.fillMaxWidth().testTag("usage-projects")) {
        JSectionHeader(stringResource(R.string.assistant_usage_by_project))
        ProjectRangePicker(range, onRange, Modifier.testTag("usage-project-range").fillMaxWidth())
        JCard(Modifier.fillMaxWidth(), padding = Tokens.Space.m) {
            ProjectList(selected, onOpen)
        }
    }
}

@Composable
private fun ProjectRangePicker(value: Int, onChange: (Int) -> Unit, modifier: Modifier = Modifier) {
    JSegmented(
        listOf(
            JSegment(stringResource(R.string.assistant_usage_today_label)),
            JSegment(stringResource(R.string.assistant_usage_days_3)),
            JSegment(stringResource(R.string.assistant_usage_days_7)),
        ),
        value,
        onChange,
        modifier,
        compact = true,
    )
}

@Composable
private fun ProjectList(range: UsageProjectRange, onOpen: (UsageProject) -> Unit) {
    val locale = currentLocale
    val groups = remember(range) { usageProjectGroups(range) }
    val all = maxOf(1L, range.total.total)
    val top = remember(range) { usageProjectTop(range) }
    var developmentOpen by rememberSaveable(range.days) { mutableStateOf(false) }
    var otherOpen by rememberSaveable(range.days) { mutableStateOf(false) }
    val notSplit = range.notSplit.total
    val split = groups.sum(UsageProjectGroup.Development) + groups.sum(UsageProjectGroup.Other) +
        groups.sum(UsageProjectGroup.Unknown) + notSplit
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        if (range.projects.isEmpty() && notSplit == 0L) {
            Column(
                Modifier.fillMaxWidth().padding(vertical = Tokens.Space.m),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                JIcon(PersonaIcons.folder, null, size = Tokens.Size.wellXS, tint = PersonaTheme.palette.ink3, decorative = true)
                Text(
                    stringResource(if (range.days == 1) R.string.assistant_usage_projects_none_today else R.string.assistant_usage_projects_none),
                    style = PersonaTheme.type.body,
                    color = PersonaTheme.palette.ink2,
                )
            }
            return@Column
        }
        if (split > 0) {
            JSegmentedShareBar(
                listOf(
                    groups.sum(UsageProjectGroup.Development).toDouble() / split,
                    groups.sum(UsageProjectGroup.Other).toDouble() / split,
                    (groups.sum(UsageProjectGroup.Unknown) + notSplit).toDouble() / split,
                ),
                listOf(
                    PersonaTheme.palette.accent,
                    projectOtherColor(),
                    projectNotSplitColor(),
                ),
                Modifier.fillMaxWidth(),
            )
        }
        ProjectGroup(
            UsageProjectGroup.Development,
            groups.list(UsageProjectGroup.Development),
            groups.sum(UsageProjectGroup.Development),
            all,
            top,
            expanded = developmentOpen,
            onExpand = { developmentOpen = true },
            onOpen = onOpen,
        )
        ProjectGroup(
            UsageProjectGroup.Other,
            groups.list(UsageProjectGroup.Other),
            groups.sum(UsageProjectGroup.Other),
            all,
            top,
            expanded = otherOpen,
            onExpand = { otherOpen = true },
            onOpen = onOpen,
        )
        groups.unknown.forEach { project ->
            ProjectRow(project, top, locale, onOpen)
        }
        if (notSplit > 0) {
            ProjectNotSplitRow(notSplit, all, top, locale)
        }
    }
}

@Composable
private fun ProjectGroup(
    group: UsageProjectGroup,
    projects: List<UsageProject>,
    sum: Long,
    all: Long,
    top: Long,
    expanded: Boolean,
    onExpand: () -> Unit,
    onOpen: (UsageProject) -> Unit,
) {
    if (projects.isEmpty()) return
    val shown = if (expanded) projects else projects.take(USAGE_PROJECTS_FIRST)
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        Row(
            Modifier.fillMaxWidth().padding(top = Tokens.Space.s),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            Box(Modifier.size(Tokens.Size.dotS).clip(CircleShape).background(projectGroupColor(group)))
            Text(
                stringResource(
                    if (group == UsageProjectGroup.Development) R.string.assistant_usage_group_development
                    else R.string.assistant_usage_group_other,
                ),
                Modifier.weight(1f),
                style = PersonaTheme.type.label,
                color = PersonaTheme.palette.ink3,
            )
            Text(
                stringResource(R.string.assistant_usage_project_group_sum, compactTokens(sum, currentLocale), usageProjectShare(sum.toDouble() / all, currentLocale)),
                style = PersonaTheme.type.label,
                color = PersonaTheme.palette.ink3,
                maxLines = 1,
            )
        }
        shown.forEach { ProjectRow(it, top, currentLocale, onOpen) }
        val more = projects.size - shown.size
        if (more > 0) {
            JTextButton(
                stringResource(R.string.assistant_usage_projects_more, more),
                onExpand,
                Modifier.testTag("usage-projects-more-${group.name.lowercase()}"),
            )
        }
    }
}

@Composable
private fun ProjectRow(project: UsageProject, top: Long, locale: Locale, onOpen: (UsageProject) -> Unit) {
    val name = usageProjectName(project)
    val tokens = compactTokens(project.tokens.total, locale)
    val share = usageProjectShare(project.share, locale)
    JRow(
        Modifier.testTag("usage-project-${project.key}"),
        onClick = project.key.takeIf(String::isNotBlank)?.let { { onOpen(project) } },
        onClickLabel = stringResource(R.string.assistant_usage_project_open, name),
    ) {
        Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JIcon(
                usageProjectIcon(project.kind),
                null,
                size = Tokens.Size.markS,
                tint = projectGroupColor(UsageProjectGroup.of(project.group)),
                decorative = true,
            )
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                FlowRow(
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
                ) {
                    Text(name, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    project.host?.takeIf { it.isNotBlank() && it != "github.com" }?.let {
                        Text(it, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
                    }
                    if (project.machines > 1) {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                            JIcon(PersonaIcons.machine, null, size = Tokens.Size.markXS, tint = PersonaTheme.palette.ink3, decorative = true)
                            Text(projectComputers(project.machines), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
                        }
                    }
                }
                JLinearProgress(
                    project.tokens.total.toDouble().div(top).toFloat(),
                    color = projectGroupColor(UsageProjectGroup.of(project.group)),
                )
            }
            Column(horizontalAlignment = Alignment.End) {
                Text(tokens, style = PersonaTheme.type.label, color = PersonaTheme.palette.ink2, maxLines = 1)
                Text(share, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
            }
        }
    }
}

@Composable
private fun ProjectNotSplitRow(tokens: Long, all: Long, top: Long, locale: Locale) {
    Row(
        Modifier.fillMaxWidth().padding(vertical = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        JIcon(PersonaIcons.folder, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.ink3, decorative = true)
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Text(
                stringResource(R.string.assistant_usage_before_agent_split),
                style = PersonaTheme.type.body,
                color = PersonaTheme.palette.ink3,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            JLinearProgress(tokens.toDouble().div(top).toFloat(), color = projectNotSplitColor())
        }
        Column(horizontalAlignment = Alignment.End) {
            Text(compactTokens(tokens, locale), style = PersonaTheme.type.label, color = PersonaTheme.palette.ink2, maxLines = 1)
            Text(usageProjectShare(tokens.toDouble() / all, locale), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
        }
    }
}

/** One project's page: totals, the selected range's chart and its breakdowns. */
@Composable
internal fun UsageProjectPage(
    project: UsageProject,
    range: Int,
    onRange: (Int) -> Unit,
    state: UsageProjectsState,
    onBack: () -> Unit,
    onRefresh: () -> Unit,
    listState: LazyListState = rememberLazyListState(),
) {
    val detail = state.detail?.takeIf { state.detailKey == project.key }
    val shown = detail?.project ?: project
    val breakdown = detail?.breakdownAt(range)
    JListPage(
        usageProjectName(shown),
        modifier = Modifier.testTag("usage-project-detail"),
        onBack = onBack,
        state = listState,
        refreshing = state.detailLoading && detail != null,
        onRefresh = onRefresh,
    ) {
        if (state.detailFailure) item("project-failure") {
            JNotice(
                stringResource(R.string.assistant_usage_project_read_failed),
                status = Status.NeedsYou,
                actionLabel = stringResource(R.string.assistant_usage_retry),
                onAction = onRefresh,
            )
        }
        if (detail == null) {
            if (state.detailLoading) item("project-loading") { JLoading() }
            else if (!state.detailFailure) item("project-empty") {
                JEmptyState(stringResource(R.string.assistant_usage_project_unavailable), stringResource(R.string.assistant_usage_project_unavailable_detail))
            }
        } else if (breakdown == null) {
            item("project-empty") {
                JEmptyState(stringResource(R.string.assistant_usage_project_unavailable), stringResource(R.string.assistant_usage_project_unavailable_detail))
            }
        } else {
            item("project-summary") { ProjectSummary(shown, breakdown, range, onRange) }
            item("project-chart-title") {
                JSectionHeader(
                    stringResource(if (range == 0) R.string.assistant_usage_project_hourly else R.string.assistant_usage_project_each_day),
                    Modifier.padding(top = Tokens.Space.m),
                )
            }
            item("project-chart") { ProjectColumns(detail, range) }
            if (breakdown.machines.isNotEmpty()) item("project-computers") {
                ProjectParts(
                    stringResource(R.string.assistant_usage_by_computer_plain),
                    breakdown.machines.map { it to partName(it, R.string.assistant_usage_unknown_machine) },
                )
            }
            if (breakdown.agents.isNotEmpty()) item("project-agents") {
                ProjectParts(
                    stringResource(R.string.assistant_usage_by_agent_plain),
                    breakdown.agents.map { it to agentPartName(it) },
                    mark = { usageAgentMark(it.key) },
                )
            }
            if (breakdown.models.isNotEmpty()) item("project-models") {
                ProjectParts(
                    stringResource(R.string.assistant_usage_by_model),
                    breakdown.models.map { it to modelPartName(it) },
                )
            }
            if (breakdown.launchers.isNotEmpty()) item("project-launchers") {
                ProjectParts(
                    stringResource(R.string.assistant_usage_by_launcher),
                    breakdown.launchers.map { it to launcherPartName(it) },
                )
            }
            if (breakdown.places.isNotEmpty()) item("project-places") {
                ProjectPlaces(breakdown.places)
            }
        }
    }
}

@Composable
private fun ProjectSummary(project: UsageProject, breakdown: UsageProjectBreakdown, range: Int, onRange: (Int) -> Unit) {
    val locale = currentLocale
    val all = maxOf(1L, breakdown.total.total)
    JCard {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JIcon(
                    usageProjectIcon(project.kind),
                    null,
                    size = Tokens.Size.iconS,
                    tint = projectGroupColor(UsageProjectGroup.of(project.group)),
                    decorative = true,
                )
                project.host?.takeIf { it.isNotBlank() }?.let {
                    Text(it, style = PersonaTheme.type.label, color = PersonaTheme.palette.ink3, maxLines = 1)
                }
                if (project.machines > 1) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                        JIcon(PersonaIcons.machine, null, size = Tokens.Size.markXS, tint = PersonaTheme.palette.ink3, decorative = true)
                        Text(projectComputers(project.machines), style = PersonaTheme.type.label, color = PersonaTheme.palette.ink3, maxLines = 1)
                    }
                }
            }
            ProjectRangePicker(range, onRange, Modifier.fillMaxWidth())
            JStatGrid(listOf(
                JStat(stringResource(R.string.assistant_usage_project_tokens), compactTokens(breakdown.total.total, locale)),
                JStat(stringResource(R.string.assistant_usage_project_share), usageProjectShare(breakdown.share, locale)),
                JStat(stringResource(R.string.assistant_usage_project_requests), LocalizedFormats.number(breakdown.total.requests, locale)),
                JStat(
                    stringResource(R.string.assistant_usage_project_cache_hit),
                    usageProjectShare(breakdown.cacheHit, locale),
                    tone = if (breakdown.cacheHit >= 0.8) Tone.Positive else Tone.Neutral,
                ),
            ))
            val kinds = usageProjectKinds(breakdown.total)
            JSegmentedShareBar(kinds.map { it.value.toDouble() / all }, kinds.map { it.color }, Modifier.fillMaxWidth())
            FlowRow(
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                kinds.forEach { kind ->
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                        Box(Modifier.size(Tokens.Size.dotS).clip(CircleShape).background(kind.color))
                        Text(
                            stringResource(
                                R.string.assistant_usage_project_kind_value,
                                stringResource(kind.label),
                                compactTokens(kind.value, locale),
                                usageProjectShare(kind.value.toDouble() / all, locale),
                            ),
                            style = PersonaTheme.type.label,
                            color = PersonaTheme.palette.ink2,
                            maxLines = 1,
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun usageProjectKinds(total: UsageTokens): List<UsageProjectKindStyle> {
    val palette = PersonaTheme.palette
    return listOf(
        UsageProjectKindStyle(R.string.assistant_usage_cache_read, total.cacheRead, palette.hue(JHue.Green).dot),
        UsageProjectKindStyle(R.string.assistant_usage_cache_write, total.cacheWrite, palette.hue(JHue.Purple).dot),
        UsageProjectKindStyle(R.string.assistant_usage_output, total.output, palette.hue(JHue.Orange).dot),
        UsageProjectKindStyle(R.string.assistant_usage_input, total.input, palette.hue(JHue.Blue).dot),
    )
}

@Composable
private fun ProjectColumns(detail: UsageProjectDetail, range: Int) {
    val locale = currentLocale
    val zone = remember(detail.zone) { usageProjectZone(detail.zone) }
    val today = stringResource(R.string.assistant_usage_today_label)
    val days = remember(range) { listOf(1, 3, 7).getOrElse(range) { 7 } }
    val points = remember(detail, days, zone, locale, today) {
        if (days == 1) detail.hours.sortedBy { it.t }.map { hour ->
            UsageProjectColumn(
                key = hour.t.toString(),
                day = today,
                hour = formatProjectHour(hour.t, locale, zone),
                axis = if (hour.hour % 6 == 0) formatProjectHourOfDay(hour.hour, locale, zone) else "",
                tokens = hour.tokens,
            )
        } else {
            val recent = detail.days.takeLast(days)
            recent.mapIndexed { index, day ->
                val last = index == recent.lastIndex
                UsageProjectColumn(
                    key = day.date,
                    day = if (last) today else formatProjectDate(day.date, locale, zone),
                    hour = null,
                    axis = if (last) today else formatProjectAxisDate(day.date, locale, zone),
                    tokens = day.tokens,
                )
            }
        }
    }
    if (points.isEmpty()) {
        JEmptyState(stringResource(R.string.assistant_usage_project_no_chart), stringResource(R.string.assistant_usage_project_no_chart_detail))
        return
    }
    val largest = points.maxByOrNull { it.tokens.total }?.key
    var picked by rememberSaveable(detail.project.key, days) { mutableStateOf<String?>(null) }
    val shown = points.firstOrNull { it.key == picked } ?: points.firstOrNull { it.key == largest } ?: points.first()
    val top = maxOf(1L, points.maxOf { it.tokens.total })
    JCard(Modifier.testTag("usage-project-chart"), padding = Tokens.Space.m) {
        val line = projectColumnLine(shown, locale)
        Text(line, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, minLines = 1)
        Spacer(Modifier.height(Tokens.Space.s))
        Row(
            Modifier.fillMaxWidth().height(Tokens.Size.box80),
            horizontalArrangement = Arrangement.spacedBy(Tokens.Size.line),
            verticalAlignment = Alignment.Bottom,
        ) {
            points.forEach { point ->
                val description = projectColumnLine(point, locale)
                val isShown = point.key == shown.key
                Box(
                    Modifier.weight(1f).fillMaxHeight()
                        .clip(RoundedCornerShape(Tokens.Space.xxs))
                        .semantics {
                            contentDescription = description
                            role = Role.Button
                            selected = isShown
                        }
                        .clickable { picked = point.key },
                    contentAlignment = Alignment.BottomCenter,
                ) {
                    Box(
                        Modifier.fillMaxWidth()
                            .fillMaxHeight((point.tokens.total.toDouble() / top).toFloat().coerceIn(0f, 1f))
                            .background(if (isShown) PersonaTheme.palette.accent else PersonaTheme.palette.accent.copy(alpha = 0.55f)),
                    )
                }
            }
        }
        Spacer(Modifier.height(Tokens.Space.xs))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Size.line)) {
            points.forEach { point ->
                Text(
                    point.axis,
                    Modifier.weight(1f),
                    style = PersonaTheme.type.micro,
                    color = PersonaTheme.palette.ink3,
                    maxLines = 1,
                    overflow = TextOverflow.Visible,
                )
            }
        }
    }
}

@Composable
private fun projectColumnLine(column: UsageProjectColumn, locale: Locale): String =
    if (column.hour != null) {
        stringResource(
            R.string.assistant_usage_hour_description,
            column.day,
            column.hour,
            ChartMath.compact(column.tokens.total.toDouble(), locale),
        )
    } else {
        stringResource(R.string.assistant_usage_day, column.day, ChartMath.compact(column.tokens.total.toDouble(), locale))
    }

@Composable
private fun ProjectParts(title: String, parts: List<Pair<UsagePart, String>>, mark: ((UsagePart) -> AgentMarkSpec?)? = null) {
    if (parts.isEmpty()) return
    val top = maxOf(1L, parts.maxOf { it.first.tokens.total })
    JSectionHeader(title, Modifier.padding(top = Tokens.Space.m))
    parts.forEachIndexed { index, (part, name) ->
        JGroupItem(GroupPosition.of(index, parts.size)) {
            val agentMark = mark?.invoke(part)
            JRow(Modifier.testTag("usage-project-part-${part.key.ifBlank { index.toString() }}")) {
                if (agentMark != null) {
                    JAgentMark(agentMark)
                    Spacer(Modifier.width(Tokens.Space.xs))
                }
                Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        Text(name, Modifier.weight(1f), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text(compactTokens(part.tokens.total, currentLocale), style = PersonaTheme.type.label, color = PersonaTheme.palette.ink3, maxLines = 1)
                    }
                    JLinearProgress(part.tokens.total.toDouble().div(top).toFloat())
                }
            }
        }
    }
}

@Composable
private fun ProjectPlaces(places: List<UsagePlace>) {
    JSectionHeader(stringResource(R.string.assistant_usage_project_places), Modifier.padding(top = Tokens.Space.m))
    val top = maxOf(1L, places.maxOf { it.tokens.total })
    places.forEachIndexed { index, place ->
        JGroupItem(GroupPosition.of(index, places.size)) {
            JRow(Modifier.testTag("usage-project-place-$index")) {
                JIcon(
                    if (place.worktree) PersonaIcons.gitBranch else PersonaIcons.folder,
                    stringResource(if (place.worktree) R.string.assistant_usage_project_worktree else R.string.assistant_usage_project_folder),
                    size = Tokens.Size.markS,
                    tint = PersonaTheme.palette.accent,
                )
                Spacer(Modifier.width(Tokens.Space.s))
                Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        Text(place.place, Modifier.weight(1f), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        Text(compactTokens(place.tokens.total, currentLocale), style = PersonaTheme.type.label, color = PersonaTheme.palette.ink3, maxLines = 1)
                    }
                    Text(
                        place.machineName?.takeIf(String::isNotBlank) ?: stringResource(R.string.assistant_usage_unknown_machine),
                        style = PersonaTheme.type.micro,
                        color = PersonaTheme.palette.ink3,
                        maxLines = 1,
                    )
                    JLinearProgress(place.tokens.total.toDouble().div(top).toFloat(), color = PersonaTheme.palette.accent)
                }
            }
        }
    }
}

@Composable
private fun usageProjectName(project: UsageProject): String = when (UsageProjectKind.of(project.kind)) {
    UsageProjectKind.Home -> stringResource(R.string.assistant_usage_project_home)
    UsageProjectKind.Temp -> stringResource(R.string.assistant_usage_project_temp)
    UsageProjectKind.Assistant -> stringResource(R.string.assistant_usage_project_assistant)
    UsageProjectKind.Unknown -> stringResource(R.string.assistant_usage_project_unknown)
    else -> project.name.ifBlank { stringResource(R.string.assistant_usage_project_unknown) }
}

private fun usageProjectIcon(kind: String) = when (UsageProjectKind.of(kind)) {
    UsageProjectKind.Repo, UsageProjectKind.Local -> PersonaIcons.gitBranch
    UsageProjectKind.Home -> PersonaIcons.home
    UsageProjectKind.Temp -> PersonaIcons.backgroundWaiting
    UsageProjectKind.Assistant -> PersonaIcons.ai
    UsageProjectKind.Folder, UsageProjectKind.Unknown -> PersonaIcons.folder
}

@Composable
private fun projectGroupColor(group: UsageProjectGroup): Color = when (group) {
    UsageProjectGroup.Development -> PersonaTheme.palette.accent
    UsageProjectGroup.Other -> projectOtherColor()
    UsageProjectGroup.Unknown -> projectNotSplitColor()
}

@Composable
private fun projectOtherColor(): Color = PersonaTheme.palette.hue(JHue.Teal).dot

@Composable
private fun projectNotSplitColor(): Color = PersonaTheme.palette.ink3.copy(alpha = 0.4f)

@Composable
private fun projectComputers(count: Int): String = stringResource(
    if (count == 1) R.string.assistant_usage_project_computer else R.string.assistant_usage_project_computers,
    count,
)

@Composable
private fun partName(part: UsagePart, missing: Int): String = part.name?.takeIf(String::isNotBlank) ?: stringResource(missing)

@Composable
private fun agentPartName(part: UsagePart): String =
    usageAgentMark(part.key)?.name ?: stringResource(R.string.assistant_usage_before_agent_split)

@Composable
private fun modelPartName(part: UsagePart): String =
    part.key.takeIf(String::isNotBlank)?.let(AgentModelPanel::modelLabel) ?: stringResource(R.string.assistant_usage_project_unknown_model)

@Composable
private fun launcherPartName(part: UsagePart): String =
    part.key.takeIf(String::isNotBlank) ?: stringResource(R.string.assistant_usage_project_terminal)

private fun usageProjectZone(raw: String): ZoneId = raw.takeIf { it.isNotBlank() }
    ?.let { runCatching { ZoneId.of(it) }.getOrNull() }
    ?: ZoneId.systemDefault()

private fun projectDate(raw: String): LocalDate? = runCatching { LocalDate.parse(raw) }.getOrNull()

private fun formatProjectDate(raw: String, locale: Locale, zone: ZoneId): String =
    projectDate(raw)?.let { LocalizedFormats.format(it.atStartOfDay(zone).toInstant(), LocalizedFormats.MONTH_DAY, locale, zone) }.orEmpty()

private fun formatProjectAxisDate(raw: String, locale: Locale, zone: ZoneId): String =
    projectDate(raw)?.let { LocalizedFormats.format(it.atStartOfDay(zone).toInstant(), LocalizedFormats.MONTH_DAY, locale, zone) }.orEmpty()

private fun formatProjectHour(t: Long, locale: Locale, zone: ZoneId): String =
    LocalizedFormats.format(Instant.ofEpochSecond(t), "j", locale, zone)

private fun formatProjectHourOfDay(hour: Int, locale: Locale, zone: ZoneId): String =
    LocalizedFormats.format(LocalDate.of(2024, 1, 1).atTime(hour, 0).atZone(zone).toInstant(), "j", locale, zone)
