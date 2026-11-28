package persona.feature.assistant

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.Dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import persona.core.contracts.Machine
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.navigation.Navigator
import java.time.DayOfWeek
import java.time.Instant
import java.time.ZoneId
import java.time.format.TextStyle

private object UsageCalendarTokens {
    val cell = Tokens.Size.markXS
    val gap = Tokens.Space.Standard.xxs + Tokens.Size.line
    val weekdayWidth = Tokens.Size.wellXS
    val monthHeight = Tokens.Size.wellXS
    val monthWidth = Tokens.Size.icon * 2
    val corner = Tokens.Space.Standard.xxs
    val hourlyCell = Tokens.Space.Standard.s
    val hourlyGap = Tokens.Space.Standard.xxs
    val hourDateWidth = Tokens.Size.wellXS * 2
    val levels = listOf(0f, 0.22f, 0.45f, 0.7f, 1f)
}

/** The home card: today's and the month's totals, a daily calendar, and an optional selected-day breakdown. */
@Composable
internal fun UsageCalendarCard(
    days: UsageDays,
    onOpen: (() -> Unit)?,
    modifier: Modifier = Modifier,
    showLegend: Boolean = false,
) {
    val validDays = remember(days) { days.validDates() }
    var selectedDate by rememberSaveable(days.zone) { mutableStateOf<String?>(null) }
    val byDate = remember(validDays) { validDays.days.associateBy { it.date } }
    LaunchedEffect(byDate.keys) { if (selectedDate !in byDate.keys) selectedDate = null }
    val selected = selectedDate?.let(byDate::get)
    val zone = remember(days.zone) { usageZone(days.zone) }
    JCard(modifier.testTag("assistant-usage-calendar"), padding = Tokens.Space.m) {
        Column(
            Modifier.fillMaxWidth().then(if (onOpen == null) Modifier else Modifier.testTag("usage-calendar-open")
                .clickable(onClickLabel = stringResource(R.string.assistant_usage_open), role = Role.Button, onClick = onOpen)),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            UsageDaySummary(validDays, selected, zone)
            UsageDayGrid(validDays, selectedDate, zone) { date -> selectedDate = if (selectedDate == date) null else date }
            if (showLegend) UsageLegend()
            if (selected != null) {
                val index = validDays.days.indexOf(selected)
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    JIconButton(PersonaIcons.back, stringResource(R.string.assistant_usage_previous_day),
                        enabled = index > 0, onClick = { selectedDate = validDays.days[index - 1].date })
                    JIconButton(PersonaIcons.forward, stringResource(R.string.assistant_usage_next_day),
                        enabled = index < validDays.days.lastIndex, onClick = { selectedDate = validDays.days[index + 1].date })
                }
            }
        }
    }
}

@Composable
@OptIn(ExperimentalLayoutApi::class)
private fun UsageDaySummary(days: UsageDays, selected: UsageDay?, zone: ZoneId) {
    val locale = currentLocale
    Box(Modifier.testTag("usage-card-summary")) {
        if (selected == null) {
            FlowRow(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween,
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Text(stringResource(R.string.assistant_usage_today, compactTokens(days.days.lastOrNull()?.total ?: 0, locale)),
                    style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink, maxLines = 1)
                Text(stringResource(R.string.assistant_usage_last_30, compactTokens(days.days.takeLast(30).sumOf { it.total }, locale)),
                    style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1)
            }
        } else {
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Text(stringResource(R.string.assistant_usage_day, formatDay(selected.date, zone), compactTokens(selected.total, locale)),
                    style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
                FlowRow(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    UsageKindText(R.string.assistant_usage_input, selected.input)
                    UsageKindText(R.string.assistant_usage_output, selected.output)
                    UsageKindText(R.string.assistant_usage_cache_write, selected.cacheWrite)
                    UsageKindText(R.string.assistant_usage_cache_read, selected.cacheRead)
                }
            }
        }
    }
}

@Composable
private fun UsageKindText(label: Int, value: Long, modifier: Modifier = Modifier) {
    Text("${stringResource(label)} ${compactTokens(value, currentLocale)}", modifier,
        style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink2, maxLines = 2)
}

@Composable
private fun UsageDayGrid(days: UsageDays, selectedDate: String?, zone: ZoneId, onSelect: (String) -> Unit) {
    val weeks = remember(days) { usageCalendarWeeks(days.days) }
    val stepByDate = remember(days) { days.days.map { it.date }.zip(usageHeatSteps(days.days.map { it.total })).toMap() }
    val today = days.days.lastOrNull()?.date
    val scroll = rememberScrollState()
    LaunchedEffect(weeks.size, today) {
        snapshotFlow { scroll.maxValue }.first { it > 0 }
        scroll.scrollTo(scroll.maxValue)
    }
    val labelScale = LocalDensity.current.fontScale.coerceIn(1f, 2f)
    val weekdayWidth = (UsageCalendarTokens.weekdayWidth + UsageCalendarTokens.cell) * labelScale
    val weekdayLabelHeight = UsageCalendarTokens.monthHeight * labelScale
    val monthHeight = UsageCalendarTokens.monthHeight * labelScale
    val monthWidth = UsageCalendarTokens.monthWidth * labelScale
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Column(
            Modifier.width(weekdayWidth).padding(top = monthHeight).testTag("usage-calendar-weekdays"),
            verticalArrangement = Arrangement.spacedBy(UsageCalendarTokens.gap),
        ) {
            for (day in DayOfWeek.entries) {
                Box(Modifier.size(UsageCalendarTokens.cell), contentAlignment = Alignment.Center) {
                    if (day in listOf(DayOfWeek.MONDAY, DayOfWeek.WEDNESDAY, DayOfWeek.FRIDAY)) {
                        Text(day.getDisplayName(TextStyle.SHORT, currentLocale), Modifier.requiredHeight(weekdayLabelHeight).testTag("usage-weekday-${day.name}"),
                            style = PersonaTheme.type.micro,
                            color = PersonaTheme.palette.ink3, maxLines = 1)
                    }
                }
            }
        }
        Column(Modifier.weight(1f)) {
            UsageMonthHeader(weeks, zone, scroll, monthHeight, monthWidth)
            Row(Modifier.horizontalScroll(scroll).testTag("usage-calendar-weeks"), horizontalArrangement = Arrangement.spacedBy(UsageCalendarTokens.gap)) {
                weeks.forEach { week ->
                    Column(verticalArrangement = Arrangement.spacedBy(UsageCalendarTokens.gap)) {
                        week.forEach { day ->
                            if (day == null) Spacer(Modifier.size(UsageCalendarTokens.cell))
                            else UsageDayCell(day, stepByDate.getValue(day.date), selectedDate == day.date, today == day.date, zone, onSelect)
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun UsageMonthHeader(
    weeks: List<List<UsageDay?>>,
    zone: ZoneId,
    scroll: ScrollState,
    monthHeight: Dp,
    monthWidth: Dp,
) {
    val weekMonths = remember(weeks) { weeks.map { week -> week.filterNotNull().firstOrNull()?.date?.take(7).orEmpty() } }
    BoxWithConstraints(Modifier.fillMaxWidth().height(monthHeight).testTag("usage-calendar-months"), contentAlignment = Alignment.CenterStart) {
        if (weekMonths.isNotEmpty()) {
            val density = LocalDensity.current
            val stridePx = with(density) { (UsageCalendarTokens.cell + UsageCalendarTokens.gap).toPx() }
            val viewportPx = with(density) { maxWidth.toPx() }
            val first = (scroll.value / stridePx).toInt().coerceIn(0, weekMonths.lastIndex)
            val last = ((scroll.value + viewportPx) / stridePx).toInt().coerceIn(first, weekMonths.lastIndex)
            val visible = weekMonths.subList(first, last + 1).filter(String::isNotEmpty).distinct()
            val labelLimit = (maxWidth / monthWidth).toInt().coerceAtLeast(1)
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                visible.takeLast(labelLimit).forEach { month ->
                    Text(formatMonth("$month-01", zone), Modifier.testTag("usage-month-$month"),
                        style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1, softWrap = false)
                }
            }
        }
    }
}

@Composable
private fun UsageDayCell(
    day: UsageDay,
    step: Int,
    selected: Boolean,
    today: Boolean,
    zone: ZoneId,
    onSelect: (String) -> Unit,
) {
    val locale = currentLocale
    val description = stringResource(R.string.assistant_usage_token_description,
        formatDay(day.date, zone), LocalizedFormats.number(day.total, locale))
    val palette = PersonaTheme.palette
    val borderColor = when {
        selected -> palette.accent
        today -> palette.ink3
        else -> null
    }
    Box(
        Modifier.size(UsageCalendarTokens.cell).clip(RoundedCornerShape(UsageCalendarTokens.corner))
            .background(heatColor(step))
            .then(if (borderColor == null) Modifier else Modifier.border(Tokens.Size.line, borderColor, RoundedCornerShape(UsageCalendarTokens.corner)))
            .testTag("usage-day-${day.date}")
            .semantics(mergeDescendants = true) {
                contentDescription = description
                role = Role.Button
                this.selected = selected
            }
            .clickable { onSelect(day.date) },
    )
}

@Composable
private fun heatColor(step: Int): Color {
    val palette = PersonaTheme.palette
    return if (step <= 0) palette.sunken else palette.accent.copy(alpha = UsageCalendarTokens.levels[step.coerceIn(1, 4)])
}

@Composable
private fun UsageLegend() {
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(UsageCalendarTokens.gap)) {
        Text(stringResource(R.string.assistant_usage_less), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3)
        for (step in 0..4) Box(Modifier.size(UsageCalendarTokens.cell).clip(RoundedCornerShape(UsageCalendarTokens.corner)).background(heatColor(step)))
        Text(stringResource(R.string.assistant_usage_more), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3)
    }
}

/** The devices section remains exactly as it was until a real usage answer can replace it. */
@Composable
internal fun UsageHomeSection(api: UsageHistoryApi?, machines: List<Machine>, phoneToolsAvailable: Boolean?, onOpen: () -> Unit) {
    if (api == null) {
        AssistantOnlineDevices(machines, phoneToolsAvailable)
        return
    }
    val model = remember(api) { UsageCalendarModel(api) }
    val state by model.state.collectAsStateWithLifecycle()
    val lifecycle = LocalLifecycleOwner.current
    LaunchedEffect(model, lifecycle) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) { model.loadDays() }
    }
    val days = state.days
    if (days?.days?.isNotEmpty() == true) key(api) { UsageCalendarCard(days, onOpen) }
    else AssistantOnlineDevices(machines, phoneToolsAvailable)
}

@Composable
@OptIn(ExperimentalLayoutApi::class)
private fun AssistantOnlineDevices(machines: List<Machine>, phoneToolsAvailable: Boolean?) {
    Column {
        JSectionHeader(stringResource(R.string.assistant_online_devices))
        FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            machines.filter { it.online }.distinctBy { it.id }.forEach { machine ->
                JChip(machine.displayName, Modifier.testTag("usage-online-device-${machine.id}"), icon = PersonaIcons.machine)
            }
            JChip(when (phoneToolsAvailable) {
                true -> stringResource(R.string.assistant_this_phone)
                false -> stringResource(R.string.assistant_phone_tools_unavailable)
                null -> stringResource(R.string.assistant_phone_permissions_pending)
            }, icon = PersonaIcons.thisDevice)
        }
    }
}

/** The full usage page. It reads on every resumed appearance and offers pull-to-refresh, never a timer. */
@Composable
internal fun UsageCalendarDestination(navigator: Navigator) {
    val host = LocalAssistantHost.current
    val choice by AssistantManagerChoice.current.collectAsStateWithLifecycle()
    val management = remember(host?.management, choice.shown) { host?.management?.forManager(choice.shown) }
    key(management) { UsageCalendarPage(management as? UsageHistoryApi, navigator::back) }
}

/** The full usage page. It reads on every resumed appearance and offers pull-to-refresh, never a timer. */
@Composable
internal fun UsageCalendarPage(api: UsageHistoryApi?, onBack: () -> Unit, listState: LazyListState = rememberLazyListState()) {
    if (api == null) {
        JListPage(title = stringResource(R.string.assistant_usage_title), onBack = onBack) {
            item("unavailable") {
                JEmptyState(stringResource(R.string.assistant_usage_unavailable), stringResource(R.string.assistant_usage_unavailable_detail))
            }
        }
        return
    }
    val model = remember(api) { UsageCalendarModel(api) }
    val projectsModel = remember(api) { UsageProjectsModel(api) }
    val state by model.state.collectAsStateWithLifecycle()
    val projectsState by projectsModel.state.collectAsStateWithLifecycle()
    val lifecycle = LocalLifecycleOwner.current
    val scope = rememberCoroutineScope()
    var range by rememberSaveable { mutableStateOf(USAGE_PROJECT_DEFAULT_RANGE) }
    var selectedProjectKey by rememberSaveable { mutableStateOf<String?>(null) }
    val projects = projectsState.projects
    val selectedProject = remember(projects, selectedProjectKey) {
        selectedProjectKey?.let { key ->
            projects?.ranges?.asSequence()?.flatMap { it.projects.asSequence() }?.firstOrNull { it.key == key }
        }
    }
    LaunchedEffect(model, projectsModel, lifecycle) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) {
            coroutineScope {
                launch { model.loadDetails() }
                launch { projectsModel.loadProjects() }
            }
        }
    }
    LaunchedEffect(selectedProject?.key, projectsModel, lifecycle) {
        val key = selectedProject?.key ?: return@LaunchedEffect
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.RESUMED) { projectsModel.loadDetail(key) }
    }
    BackHandler(enabled = selectedProjectKey != null) {
        selectedProjectKey = null
        projectsModel.clearDetail()
    }
    if (selectedProject != null) {
        UsageProjectPage(
            selectedProject,
            range,
            onRange = { range = it },
            state = projectsState,
            onBack = {
                selectedProjectKey = null
                projectsModel.clearDetail()
            },
            onRefresh = { scope.launch { projectsModel.loadDetail(selectedProject.key) } },
        )
        return
    }
    val days = state.days
    JListPage(title = stringResource(R.string.assistant_usage_title), onBack = onBack, state = listState,
        refreshing = (state.loading || projectsState.loading) && days != null, onRefresh = {
            scope.launch {
                coroutineScope {
                    launch { model.loadDetails() }
                    launch { projectsModel.loadProjects() }
                }
            }
        }) {
        if (state.failure) item("failure") {
            JNotice(stringResource(R.string.assistant_usage_read_failed), status = Status.NeedsYou,
                actionLabel = stringResource(R.string.assistant_usage_retry), onAction = { scope.launch { model.loadDetails() } })
        }
        if (days == null && state.loading) item("loading") { JLoading() }
        if (days != null) {
            item("summary") {
                val stats = listOf(
                    JStat(stringResource(R.string.assistant_usage_today_label), compactTokens(days.days.lastOrNull()?.total ?: 0, currentLocale)),
                    JStat(stringResource(R.string.assistant_usage_last_7), compactTokens(days.days.takeLast(7).sumOf { it.total }, currentLocale)),
                    JStat(stringResource(R.string.assistant_usage_last_30_label), compactTokens(days.days.takeLast(30).sumOf { it.total }, currentLocale)),
                )
                val rows = if (LocalDensity.current.fontScale > 1f) stats.chunked(2) else listOf(stats)
                Column(Modifier.padding(bottom = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    rows.forEach { JStatGrid(it) }
                }
            }
            projects?.let { data ->
                item("projects") {
                    UsageProjectsSection(data, range, onRange = { range = it }, onOpen = { selectedProjectKey = it.key })
                }
            }
            item("calendar") { UsageCalendarCard(days, onOpen = null, showLegend = true) }
            state.hours?.let { hours ->
                item("hourly-title") { JSectionHeader(stringResource(R.string.assistant_usage_by_hour), Modifier.padding(top = Tokens.Space.m)) }
                item("hourly") { UsageHourlyGrid(hours) }
                item("computers-title") { JSectionHeader(stringResource(R.string.assistant_usage_by_computer), Modifier.padding(top = Tokens.Space.m)) }
                hours.machines.forEachIndexed { index, machine ->
                    item("computer-$index") {
                        JGroupItem(GroupPosition.of(index, hours.machines.size)) {
                            JRow(machine.name.ifBlank { stringResource(R.string.assistant_usage_unknown_machine) },
                                value = compactTokens(machine.total, currentLocale))
                        }
                    }
                }
                val agents = hours.agents.filter { it.total > 0 }
                if (agents.isNotEmpty()) {
                    item("agents-title") { JSectionHeader(stringResource(R.string.assistant_usage_by_agent), Modifier.padding(top = Tokens.Space.m)) }
                    val largest = agents.maxOf { it.total }
                    agents.forEachIndexed { index, share ->
                        item("agent-${share.agent}") {
                            JGroupItem(GroupPosition.of(index, agents.size)) { UsageAgentRow(share, largest) }
                        }
                    }
                }
                item("kinds-title") { JSectionHeader(stringResource(R.string.assistant_usage_by_kind), Modifier.padding(top = Tokens.Space.m)) }
                val kinds = listOf(
                    R.string.assistant_usage_input to hours.total.input,
                    R.string.assistant_usage_output to hours.total.output,
                    R.string.assistant_usage_cache_write to hours.total.cacheWrite,
                    R.string.assistant_usage_cache_read to hours.total.cacheRead,
                )
                kinds.forEachIndexed { index, (label, value) ->
                    item("kind-$label") {
                        JGroupItem(GroupPosition.of(index, kinds.size)) {
                            JRow(stringResource(label), value = compactTokens(value, currentLocale))
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun UsageAgentRow(share: UsageAgentShare, largest: Long) {
    val mark = usageAgentMark(share.agent)
    val name = mark?.name ?: stringResource(R.string.assistant_usage_before_agent_split)
    val total = share.total
    val spoken = stringResource(R.string.assistant_usage_token_description, name, LocalizedFormats.number(total, currentLocale))
    JRow(Modifier.clearAndSetSemantics { contentDescription = spoken }) {
        Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                if (mark != null) JAgentMark(mark)
                Text(name, Modifier.weight(1f), style = PersonaTheme.type.body,
                    color = if (mark == null) PersonaTheme.palette.ink2 else PersonaTheme.palette.ink)
                Text(compactTokens(total, currentLocale), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2, maxLines = 1)
            }
            JLinearProgress((total.toDouble() / largest).toFloat())
        }
    }
}

@Composable
private fun UsageHourlyGrid(history: UsageHours) {
    val zone = remember(history.zone) { usageZone(history.zone) }
    val validHistory = remember(history) { history.validDates() }
    val rows = remember(validHistory) { validHistory.hours.groupBy { it.date }.entries.toList() }
    val steps = remember(validHistory) {
        validHistory.hours.map { it.t }.zip(usageHeatSteps(validHistory.hours.map { it.total })).toMap()
    }
    val dateWidth = UsageCalendarTokens.hourDateWidth * LocalDensity.current.fontScale.coerceIn(1f, 2f)
    Column(Modifier.horizontalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(UsageCalendarTokens.hourlyGap)) {
        rows.forEach { row ->
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(UsageCalendarTokens.hourlyGap)) {
                Text(formatDay(row.key, zone, full = false), Modifier.width(dateWidth),
                    style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink2, maxLines = 1)
                row.value.forEach { hour ->
                    val description = stringResource(R.string.assistant_usage_hour_description,
                        formatDay(hour.date, zone, full = false),
                        LocalizedFormats.format24(Instant.ofEpochSecond(hour.t), LocalizedFormats.TIME_24, currentLocale, zone),
                        LocalizedFormats.number(hour.total, currentLocale))
                    Box(Modifier.size(UsageCalendarTokens.hourlyCell).clip(RoundedCornerShape(UsageCalendarTokens.corner))
                        .background(heatColor(steps.getValue(hour.t)))
                        .semantics { contentDescription = description })
                }
            }
        }
    }
}

internal fun usageZone(raw: String): ZoneId = raw.takeIf { it.isNotBlank() }
    ?.let { runCatching { ZoneId.of(it) }.getOrNull() }
    ?: ZoneId.systemDefault()

@Composable
internal fun compactTokens(value: Long, locale: java.util.Locale): String = ChartMath.compact(value.toDouble(), locale)

@Composable
private fun formatDay(date: String, zone: ZoneId, full: Boolean = true): String {
    val parsed = parseUsageDate(date) ?: return ""
    return LocalizedFormats.format(parsed.atStartOfDay(zone).toInstant(),
        if (full) LocalizedFormats.DATE else LocalizedFormats.MONTH_DAY, currentLocale, zone)
}

@Composable
private fun formatMonth(date: String, zone: ZoneId): String {
    val parsed = parseUsageDate(date) ?: return ""
    return LocalizedFormats.format(parsed.atStartOfDay(zone).toInstant(), "MMM", currentLocale, zone)
}
