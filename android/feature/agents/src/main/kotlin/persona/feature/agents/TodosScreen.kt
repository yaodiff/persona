package persona.feature.agents

import persona.feature.chat.SessionModes
import persona.feature.chat.TodoRestores
import persona.core.domain.tasks.orphanedLine
import persona.core.domain.tasks.HoldStep
import persona.core.domain.tasks.HoldUnblock
import persona.core.domain.tasks.TodoHold
import persona.core.domain.tasks.hold
import persona.core.designsystem.text
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import persona.core.domain.tasks.Triage
import persona.core.domain.tasks.TriageRule
import persona.core.domain.tasks.TodoRestStatus
import persona.core.domain.tasks.TriageSection
import persona.core.domain.tasks.restStatus
import persona.core.domain.tasks.acceptedAtRest
import persona.core.domain.tasks.deliveredAwaitingReview
import persona.core.domain.tasks.shownState
import persona.core.domain.tasks.waitsForOwner
import persona.core.domain.tasks.checksAsOpen
import persona.core.domain.tasks.seenCanFile
import persona.core.domain.tasks.supersededFailedStarts
import persona.core.domain.tasks.triageTodos
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.draw.clip
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.toggleableState
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.launch
import persona.core.domain.tasks.moveTo
import persona.core.domain.tasks.TodoListFilter
import persona.core.domain.tasks.TodoListSort
import persona.core.domain.tasks.TodoListGrouping
import persona.core.domain.tasks.directionFoldKey
import persona.core.domain.tasks.todoRows
import persona.core.domain.tasks.filterCounts
import persona.core.domain.tasks.hiddenWaiting
import persona.core.domain.tasks.listed
import persona.core.domain.tasks.matches
import persona.core.domain.tasks.sortedRows
import persona.core.domain.tasks.weekStart
import persona.core.domain.tasks.CalendarMode
import persona.core.contracts.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.answer.askingLine
import persona.core.domain.answer.firstBySession
import persona.core.domain.modelShortName
import persona.core.domain.plainMarkdown
import persona.core.domain.tasks.TasksStore
import persona.core.navigation.NewTodoRequests
import android.text.format.DateFormat
import androidx.compose.ui.platform.LocalConfiguration
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.util.Locale
import java.time.format.DateTimeFormatter

data class TodoMostUrgent(
    val agent: TodoAgent,
    val session: Session?,
    val status: AgentState,
    val isFailed: Boolean,
)

fun mostUrgentAgent(todo: Todo, sessionLookup: (SessionKey) -> Session?): TodoMostUrgent? {
    val active = todo.currentAgents
    if (active.isEmpty()) return null

    fun score(st: AgentState, isFailed: Boolean): Int = when {
        isFailed -> 1
        st == AgentState.NeedsYou -> 4
        st == AgentState.Unread -> 3
        st == AgentState.Working -> 2
        else -> 0
    }

    var best: TodoMostUrgent? = null
    var bestScore = -1

    for (a in active) {
        val isFailed = !a.startError.isNullOrBlank()
        val s = sessionLookup(a.key)
        val st = if (isFailed) AgentState.Offline else (s?.agentState ?: AgentState.Offline)
        val sc = score(st, isFailed)
        if (sc > bestScore || best == null) {
            bestScore = sc
            best = TodoMostUrgent(a, s, st, isFailed)
        }
    }
    return best
}

private fun taskLeadAgent(todo: Todo, sessionLookup: (SessionKey) -> Session?): Session? {
    fun movedAt(session: Session) = listOfNotNull(parseTime(session.updatedAt), parseTime(session.stateSince)).maxOrNull() ?: Instant.EPOCH
    var lead: Session? = null
    for (agent in todo.currentAgents) {
        val candidate = sessionLookup(agent.key) ?: continue
        val current = lead
        if (current == null) {
            lead = candidate
            continue
        }
        val working = candidate.agentState == AgentState.Working
        val leadWorking = current.agentState == AgentState.Working
        if (if (working != leadWorking) working else movedAt(candidate) > movedAt(current)) lead = candidate
    }
    return lead
}

@Composable
fun TodosScreen(
    todos: List<Todo>,
    sessions: List<Session>,
    tasks: TasksStore?,
    onBack: (() -> Unit)?,
    onOpenTodo: (String) -> Unit,
    now: Instant = Instant.now(),
    onError: (String) -> Unit = {},
    /** The cards waiting for an answer: a waiting agent's line says what its card asks (#3964). */
    pending: List<Interaction> = emptyList(),
    /** The launcher's "New to-do": each request brings the sheet up, also on a list kept from an earlier use (#5718). */
    newTodoRequests: NewTodoRequests? = null,
    /** The assistant whose to-dos [todos] are: while it is offline its list is out of reach, not empty (JAssistantOffline). */
    assistant: persona.core.contracts.api.CurrentAssistant? = null,
    /** The hub's direction tree (#4352): the list can then be grouped by direction. Null (an older hub): status only. */
    directions: DirectionTree? = null,
    /** Computer display names for the task's lead-Agent metadata (#10904). */
    machines: List<Machine> = emptyList(),
) {
    val cards = remember(pending) { pending.firstBySession() }
    var query by rememberSaveable { mutableStateOf("") }
    var creating by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(newTodoRequests) {
        val requests = newTodoRequests ?: return@LaunchedEffect
        requests.requests.collect { at -> if (at != null && requests.take(at)) creating = true }
    }
    var newTitle by remember { mutableStateOf("") }
    var newNotes by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    val res = agentResources()
    val context = LocalContext.current
    // List | Calendar and the calendar's "Show done" (#2046), kept on this device.
    val calendarPrefs = remember(context) { TodoCalendarPrefs(context) }
    var view by remember(calendarPrefs) { mutableStateOf(calendarPrefs.view) }
    var showDone by remember(calendarPrefs) { mutableStateOf(calendarPrefs.showDone) }
    // Day | Week | Month (#4200), kept on this device; the day the calendar shows, today when the page opens.
    var calendarMode by remember(calendarPrefs) { mutableStateOf(calendarPrefs.mode) }
    val setCalendarMode = { m: CalendarMode -> calendarMode = m; calendarPrefs.mode = m }
    var calendarAnchor by rememberSaveable { mutableStateOf(now.atZone(ZoneId.systemDefault()).toLocalDate().toString()) }
    // A new to-do being made on the calendar, due where it was tapped (CalendarSlot.saved).
    var adding by rememberSaveable { mutableStateOf<String?>(null) }
    // The list's filter and sort (#3740), kept on this device.
    val listPrefs = remember(context) { TodoListPrefs(context) }
    var listFilter by remember(listPrefs) { mutableStateOf(listPrefs.filter) }
    var listSort by remember(listPrefs) { mutableStateOf(listPrefs.sort) }
    val setFilter = { f: TodoListFilter -> listFilter = f; listPrefs.filter = f }
    val setSort = { o: TodoListSort -> listSort = o; listPrefs.sort = o }
    // Status | Direction (#4352), kept on this device; by direction only while the hub has the tree.
    var listGrouping by remember(listPrefs) { mutableStateOf(listPrefs.grouping) }
    val setGrouping = { g: TodoListGrouping -> listGrouping = g; listPrefs.grouping = g }
    val grouping = if (directions == null) TodoListGrouping.Status else listGrouping
    val zone = ZoneId.systemDefault()
    // The week is the calendar's ([weekStart], the region of the app's language).
    val appLocale = LocalConfiguration.current.locales[0]
    val firstDay = remember(appLocale) { weekStart(appLocale) }

    val sessionMap = remember(sessions) { sessions.associateBy { it.key } }
    val sessionLookup: (SessionKey) -> Session? = remember(sessionMap) { { key: SessionKey -> sessionMap[key] } }
    val machineMap = remember(machines) { machines.associateBy { it.id } }
    val hubFeatures = tasks?.features?.collectAsState()
    val seenOn = hubFeatures?.value?.contains("todo_seen") == true

    val searched = remember(todos, query) {
        if (query.isBlank()) todos
        else {
            val q = query.trim()
            todos.filter { it.title.contains(q, ignoreCase = true) || it.notes.contains(q, ignoreCase = true) }
        }
    }
    // A search, then the filter, pick the to-dos; the sections place what is left (#3740), so their counts follow.
    val filtered = remember(searched, listFilter, now, firstDay) { searched.filter { it.matches(listFilter, now, zone, firstDay) } }
    // what each filter would show, under it in the menu
    val counts = remember(searched, now, firstDay) { filterCounts(searched, now, zone, firstDay) }
    val viewBar: @Composable (Modifier) -> Unit = { modifier ->
        TodosViewBar(
            view = view,
            onView = { view = it; calendarPrefs.view = it },
            showDone = showDone,
            onShowDone = { showDone = it; calendarPrefs.showDone = it },
            modifier = modifier,
            mode = calendarMode,
            onMode = setCalendarMode,
            listControls = { TodoListControls(listFilter, listSort, counts, setFilter, setSort,
                grouping = grouping, onGrouping = setGrouping.takeIf { directions != null }) },
        )
    }

    // Status sections (#3206, docs/assistant/todo-status-sop.md), the web's rules: related to-dos share a row.
    val restores = remember(context) { TodoRestores(context) }
    var restored by remember(restores) { mutableStateOf(restores.read()) }
    val nowMs = now.toEpochMilli()
    // Whether a failed start was superseded by finished work (#3713) is decided on every to-do: a search or a filter that
    // leaves the finished one out must not put it back in Needs you.
    val superseded = remember(todos) { supersededFailedStarts(todos) }
    val triaged = remember(filtered, sessionMap, restored, nowMs, superseded) {
        triageTodos(filtered, sessionLookup, nowMs, restored, known = superseded)
    }
    // With a filter, Needs you is filtered too: the line over the list says how many it leaves out.
    val hidden = remember(searched, filtered, sessionMap, restored, nowMs, listFilter, superseded) {
        if (listFilter == TodoListFilter.All) 0 else hiddenWaiting(searched, filtered, sessionLookup, nowMs, restored, superseded = superseded)
    }
    // Which sections the user folded or unfolded: Shelved and Done start folded, the rest open (#5707).
    var toggled by rememberSaveable { mutableStateOf(emptyList<String>()) }
    fun isFolded(section: TriageSection): Boolean = section.folded != (section.name in toggled)
    var expanded by rememberSaveable { mutableStateOf(emptyList<String>()) }
    val setDone: (Todo, Boolean) -> Unit = { todo, done ->
        scope.launch {
            try {
                tasks?.setTodoDone(todo, done)
            } catch (e: Exception) {
                onError(res.getString(if (done) R.string.agents_todo_done_failed else R.string.agents_todo_reopen_failed, e.message.orEmpty()))
            }
        }
    }
    // A to-do moved on the calendar (#2045, iOS TodoCalendarMove): drawn there at once; once the hub has it, a success
    // buzz and "Moved to …" with Undo (which moves it back, without a second notice); a failed save is put back and said
    // as the shell says failures (an error buzz, a nudge while offline).
    val feedback = LocalAppFeedback.current
    val haptics = rememberHaptics()
    val locale = LocalConfiguration.current.locales[0]
    fun seenFailed(e: Exception) {
        if (feedback?.failure(e) != true) onError(res.getString(R.string.agents_todo_seen_failed, e.message.orEmpty()))
    }
    val gotIt: (Todo, Long) -> Unit = gotIt@{ todo, after ->
        val store = tasks ?: return@gotIt
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            // The local move finishes before the click returns; the hub write does not hold Undo (#5707, principle 7).
            val undo = FeedbackAction(res.getString(R.string.agents_todo_seen_undo)) {
                scope.launch {
                    try { store.setTodoSeen(todo.id, false) }
                    catch (e: CancellationException) { throw e }
                    catch (e: Exception) { seenFailed(e) }
                }
            }
            val moved = if (todo.kind == "concern" && !todo.isDone) R.string.agents_todo_seen_moved_open else R.string.agents_todo_seen_moved
            feedback?.notice(res.getString(moved), undo)
            try { store.setTodoSeen(todo.id, true, after) }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { seenFailed(e) }
        }
    }
    fun moveFailed(e: Exception) {
        if (feedback?.failure(e) != true) onError(res.getString(R.string.agents_todo_calendar_move_failed, e.message.orEmpty()))
    }
    val moveTodo: (String, TodoDue) -> Unit = move@{ id, due ->
        val store = tasks ?: return@move
        val todo = todos.firstOrNull { it.id == id } ?: return@move
        val back = todo.due ?: return@move
        val to = todo.moveTo(due) ?: return@move
        scope.launch {
            try {
                store.setTodoDue(id, to)
                haptics(Haptic.Success)
                val undo = FeedbackAction(res.getString(R.string.agents_todo_calendar_undo)) {
                    scope.launch {
                        try { store.setTodoDue(id, back) }
                        catch (e: CancellationException) { throw e }
                        catch (e: Exception) { moveFailed(e) }
                    }
                }
                feedback?.notice(res.getString(R.string.agents_todo_calendar_moved, dueWords(to, locale, ZoneId.systemDefault(), now)), undo)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                moveFailed(e)
            }
        }
    }
    val delete: (Todo) -> Unit = { todo ->
        scope.launch {
            try {
                tasks?.deleteTodo(todo.id)
            } catch (e: Exception) {
                onError(res.getString(R.string.agents_todo_delete_failed, e.message.orEmpty()))
            }
        }
    }

    if (creating) {
        JSheet(
            onDismiss = {
                creating = false
                newTitle = ""
                newNotes = ""
            },
            title = stringResource(R.string.agents_todo_new)
        ) {
            Column(
                Modifier
                    .fillMaxWidth()
                    .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.s),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)
            ) {
                JTextField(
                    newTitle,
                    { newTitle = it },
                    label = stringResource(R.string.agents_todo_title_label),
                    placeholder = stringResource(R.string.agents_todo_title_placeholder),
                    singleLine = true
                )
                JTextField(
                    newNotes,
                    { newNotes = it },
                    label = stringResource(R.string.agents_todo_notes_optional),
                    placeholder = stringResource(R.string.agents_todo_notes_placeholder),
                    minLines = 3,
                    maxLines = 6
                )
                Spacer(Modifier.height(Tokens.Space.s))
                JButton(
                    text = stringResource(R.string.agents_todo_create),
                    onClick = {
                        val title = newTitle.trim()
                        val notes = newNotes.trim()
                        if (title.isNotBlank()) {
                            creating = false
                            newTitle = ""
                            newNotes = ""
                            scope.launch {
                                try {
                                    tasks?.createTodo(title, notes)
                                } catch (e: Exception) {
                                    onError(res.getString(R.string.agents_todo_create_failed, e.message.orEmpty()))
                                }
                            }
                        }
                    },
                    enabled = newTitle.isNotBlank(),
                    modifier = Modifier.fillMaxWidth()
                )
            }
        }
    }

    adding?.let { CalendarSlot.parse(it) }?.let { slot ->
        TodoQuickAddSheet(
            slot = slot,
            onAdd = { title, due -> tasks?.createTodo(title, "", due) },
            onFailed = { e -> if (feedback?.failure(e) != true) onError(res.getString(R.string.agents_todo_create_failed, e.message.orEmpty())) },
            onDismiss = { adding = null },
        )
    }

    val listState = rememberLazyListState()
    // The view toggles stay put below the bar, inside the reach of its blurred edge: show the edge only while the
    // list has scrolled under it. Nothing on the calendar ever does; its grid and time axis scroll in their own boxes.
    val underTop = remember(view) { if (view == TodosView.Calendar) { { false } } else { { listState.canScrollBackward } } }
    JPage(
        title = stringResource(R.string.agents_todos_title),
        onBack = if (LocalIsSidebarRoot.current) null else onBack,
        underTop = underTop,
        actions = {
            // on the calendar a new to-do is due on the day it shows, all day (iOS TodosView)
            JIconButton(PhIcons.Plus, stringResource(R.string.agents_todo_new), {
                if (view == TodosView.Calendar && tasks != null) adding = CalendarSlot(LocalDate.parse(calendarAnchor), null).saved()
                else creating = true
            })
        },
        bottomBar = if (view == TodosView.Calendar) ({
            TodoCalendarUndatedBar(todos, onShowList = { view = TodosView.List; calendarPrefs.view = TodosView.List })
        }) else null,
    ) { padding ->
        if (view == TodosView.Calendar) {
            Column(Modifier.fillMaxSize().padding(top = padding.calculateTopPadding() + Tokens.Space.s)) {
                viewBar(Modifier.padding(horizontal = Tokens.Space.l))
                TodoCalendar(
                    mode = calendarMode,
                    onMode = setCalendarMode,
                    anchor = LocalDate.parse(calendarAnchor),
                    onAnchor = { calendarAnchor = it.toString() },
                    todos = todos,
                    sessionLookup = sessionLookup,
                    showDone = showDone,
                    now = now,
                    onOpenTodo = onOpenTodo,
                    bottomPadding = padding.calculateBottomPadding(),
                    modifier = Modifier.weight(1f),
                    onMove = if (tasks != null) moveTodo else null,
                    onAdd = if (tasks != null) { slot -> adding = slot.saved() } else null,
                )
            }
            return@JPage
        }
        LazyColumn(
            Modifier.fillMaxSize(),
            state = listState,
            contentPadding = PaddingValues(
                top = padding.calculateTopPadding() + Tokens.Space.s,
                bottom = padding.calculateBottomPadding() + Tokens.Space.l,
                start = Tokens.Space.l,
                end = Tokens.Space.l
            ),
            verticalArrangement = Arrangement.spacedBy(ContentTokens.rowGap)
        ) {
            item(key = "view") { viewBar(Modifier) }
            item(key = "search") {
                JSearchField(query, { query = it }, placeholder = stringResource(R.string.agents_todos_search))
            }

            if (hidden > 0) {
                item(key = "hidden-waiting") {
                    JNotice(
                        stringResource(R.string.agents_todos_filter_hidden_waiting, hidden, listFilter.title),
                        Modifier.testTag("todos-hidden-waiting"),
                        status = Status.NeedsYou,
                        icon = PhIcons.WarningCircle,
                        actionLabel = stringResource(R.string.agents_todos_filter_show_all),
                        onAction = { setFilter(TodoListFilter.All) },
                        inlineAction = true,
                    )
                }
            }
            if (filtered.isEmpty() && searched.isNotEmpty() && listFilter != TodoListFilter.All) {
                item(key = "empty-filter") {
                    JEmptyState(
                        title = listFilter.emptyTitle,
                        description = stringResource(R.string.agents_todos_filter_empty_hint),
                        icon = PhIcons.FunnelSimple,
                        action = { JButton(stringResource(R.string.agents_todos_filter_show_all), { setFilter(TodoListFilter.All) }) },
                    )
                }
            } else if (filtered.isEmpty()) {
                // The hub's list leaves out an assistant that is offline: its to-dos are out of reach, not none (a search that
                // finds nothing among kept ones is "no match").
                if (assistant?.offline != null && todos.isEmpty()) item(key = "offline") { JAssistantOffline(assistant) }
                else item(key = "empty") {
                    JEmptyState(
                        title = stringResource(if (query.isBlank()) R.string.agents_todos_empty else R.string.agents_todos_no_match),
                        description = stringResource(if (query.isBlank()) R.string.agents_todos_empty_hint else R.string.agents_todos_no_match_hint),
                        icon = PhIcons.ListChecks,
                        action = if (query.isBlank()) ({
                            JButton(stringResource(R.string.agents_todo_new), { creating = true }, icon = PhIcons.Plus)
                        }) else null,
                    )
                }
            } else {
                // By direction (#4352): one section per direction of the hub's tree, in its order, with its two counts; the
                // rows are the tree's to-dos in its order (those the search and filter leave); the rest keep their status sections.
                val tree = directions?.takeIf { grouping == TodoListGrouping.Direction }
                val directionRows = if (tree == null) emptyList() else {
                    val placed = triaged.sections.values.flatten()
                        .flatMap { group -> group.members.map { member -> member.id to (member to group.triage.getValue(member.id)) } }.toMap()
                    tree.todoRows(placed) { (_, triage) -> triage.section == TriageSection.Done || triage.section == TriageSection.Cancelled }
                }
                if (tree != null) {
                    for ((direction, rows) in directionRows) {
                        val foldKey = directionFoldKey(direction.id)
                        val folded = foldKey in toggled
                        item(key = "direction:${direction.id}") {
                            JSectionHeader(
                                direction.localizedTitle,
                                onClick = { toggled = if (folded) toggled - foldKey else toggled + foldKey },
                                open = !folded,
                                action = {
                                    JDirectionCounts(direction)
                                    Spacer(Modifier.width(Tokens.Space.s))
                                    JIcon(if (folded) PersonaIcons.expand else PersonaIcons.collapse, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink3)
                                },
                            )
                        }
                        if (folded) continue
                        items(rows, key = { "direction-row:${direction.id}/${it.first.id}" }) { (todo, triage) ->
                            TodoTriageRow(
                                todo = todo,
                                triage = triage,
                                related = 0,
                                relatedOpen = false,
                                onToggleRelated = {},
                                sessionLookup = sessionLookup,
                                machines = machineMap,
                                cards = cards,
                                now = now,
                                onOpen = { onOpenTodo(todo.id) },
                                onDone = setDone,
                                onDelete = delete,
                                onRestore = { restored = restores.restore(todo.id, System.currentTimeMillis()) },
                                onSeen = if (seenOn && triage.section == TriageSection.JustDone && todo.seenCanFile) ({
                                    gotIt(todo, triage.last)
                                }) else null,
                            )
                        }
                    }
                }
                // The to-dos the tree does not list (shelved ones, Completed, any other the hub leaves out) keep their
                // status sections after the directions, so grouping by direction hides none of them.
                val rest = triaged.without(directionRows.flatMap { (_, rows) -> rows.map { it.first.id } }.toSet())
                // No "N open · N shelved · N done" line: each section title carries its count (#3712).
                for (section in TriageSection.entries) {
                    val rows = sortedRows(rest.rows(section), listSort, zone)
                    if (rows.isEmpty()) continue
                    val folded = isFolded(section)
                    val awaitingOnly = section == TriageSection.JustDone && rows.all { row ->
                        row.members.all { row.triage.getValue(it.id).rule == TriageRule.FinishedUnseen }
                    }
                    item(key = "section:${section.name}") {
                        JSectionHeader(
                            section.label(awaitingOnly),
                            count = rest.listed(section),
                            status = section.status,
                            countTone = section.status ?: Status.Idle,
                            onClick = { toggled = if (section.name in toggled) toggled - section.name else toggled + section.name },
                            sub = false,
                            open = !folded,
                            action = { JIcon(if (folded) PhIcons.CaretDown else PhIcons.CaretUp, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink3) },
                        )
                    }
                    if (folded) continue
                    items(rows, key = { "row:${it.key}" }) { group ->
                        val open = group.key in expanded
                        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                            TodoTriageRow(
                                todo = group.lead,
                                triage = group.triage.getValue(group.lead.id),
                                related = group.members.size - 1,
                                relatedOpen = open,
                                onToggleRelated = { expanded = if (open) expanded - group.key else expanded + group.key },
                                sessionLookup = sessionLookup,
                                machines = machineMap,
                                cards = cards,
                                now = now,
                                onOpen = { onOpenTodo(group.lead.id) },
                                onDone = setDone,
                                onDelete = delete,
                                onRestore = { restored = restores.restore(group.lead.id, System.currentTimeMillis()) },
                                onSeen = if (seenOn && section == TriageSection.JustDone && group.lead.seenCanFile) ({
                                    gotIt(group.lead, group.triage.getValue(group.lead.id).last)
                                }) else null,
                            )
                            if (open) {
                                for (m in group.members.drop(1)) {
                                    TodoRelatedLine(m, group.triage.getValue(m.id), Modifier.padding(start = Tokens.Space.xxl)) { onOpenTodo(m.id) }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

/** A section's name on its header. */
private val TriageSection.label: String @Composable get() = stringResource(when (this) {
    TriageSection.Waiting -> R.string.agents_todos_section_waiting
    TriageSection.Working -> R.string.agents_todos_section_working
    TriageSection.JustDone -> R.string.agents_todos_section_just_done
    TriageSection.Pr -> R.string.agents_todos_section_pr
    TriageSection.Other -> R.string.agents_todos_section_other
    TriageSection.Shelved -> R.string.agents_todos_section_shelved
    TriageSection.Done -> R.string.agents_todos_done
    TriageSection.Cancelled -> R.string.agents_todo_cancelled
})

/** A mixed Just finished section keeps its original name; only a wholly unaccepted-delivery section is awaiting review. */
@Composable
private fun TriageSection.label(awaitingReview: Boolean): String =
    if (this == TriageSection.JustDone && awaitingReview) stringResource(R.string.agents_todos_section_awaiting_review) else label

/** The state colour a section's header borrows (the Board style draws it). */
private val TriageSection.status: Status? get() = when (this) {
    TriageSection.Waiting -> Status.NeedsYou
    TriageSection.Working -> Status.Working
    TriageSection.JustDone -> Status.Unread
    else -> null
}

/** "PR #9" from a pull request's address; "PR" when it ends in no number. */
@Composable
private fun prLabel(url: String): String {
    val n = url.trim().trimEnd('/').substringAfterLast('/').takeIf { it.isNotEmpty() && it.all(Char::isDigit) }
    val pr = stringResource(R.string.agents_todo_pr)
    return if (n != null) "$pr #$n" else pr
}

/** A related to-do under its row's lead (#3206): its title and section, a tap opens it. */
@Composable
private fun TodoRelatedLine(todo: Todo, triage: Triage, modifier: Modifier = Modifier, onOpen: () -> Unit) {
    if (todo.kind == "concern") {
        Column(
            modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape).clickable(onClick = onOpen).padding(vertical = Tokens.Space.xs),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        ) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
            ) {
                Text(todo.title, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1,
                    overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                JTag(triage.section.label(triage.rule == TriageRule.FinishedUnseen))
            }
            val label = stringResource(R.string.agents_todo_concern)
            JTag(label, Modifier.clearAndSetSemantics { contentDescription = label })
        }
    } else {
        Row(
            modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape).clickable(onClick = onOpen).padding(vertical = Tokens.Space.xs),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            Text(todo.title, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1,
                overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            JTag(triage.section.label(triage.rule == TriageRule.FinishedUnseen))
        }
    }
}

/**
 * One row of the status list: an open to-do's card (state, what its most urgent agent says, where its work is held, its
 * plan), or a finished one's; PR to merge rows carry the PR and how long since it moved, Shelved rows a Restore.
 */
@Composable
private fun TodoTriageRow(
    todo: Todo,
    triage: Triage,
    related: Int,
    relatedOpen: Boolean,
    onToggleRelated: () -> Unit,
    sessionLookup: (SessionKey) -> Session?,
    machines: Map<String, Machine>,
    cards: Map<SessionKey, Interaction>,
    now: Instant,
    onOpen: () -> Unit,
    onDone: (Todo, Boolean) -> Unit,
    onDelete: (Todo) -> Unit,
    onRestore: () -> Unit,
    onSeen: (() -> Unit)?,
) {
    val res = agentResources()
    // a failed start waiting on the owner reads open, says why in red, and its checkbox closes it (#3713)
    val finished = !todo.checksAsOpen(triage)
    val state = todo.shownState(sessionLookup)
    val restStatus = todo.restStatus(sessionLookup, now.toEpochMilli())
    val accepted = triage.rule == TriageRule.Accepted || todo.acceptedAtRest(sessionLookup)
    val urgent = mostUrgentAgent(todo, sessionLookup)
    val lead = remember(todo, sessionLookup) { taskLeadAgent(todo, sessionLookup) }
    val leadEffort = SessionModes.effortLabel(lead?.effort, res)?.takeIf(String::isNotEmpty)
    val agentMetadata = remember(lead, machines, leadEffort) {
        lead?.let { session ->
            listOfNotNull(
                machines[session.machineId]?.displayName ?: session.machineId,
                modelShortName(session.model).ifBlank { null },
                leadEffort,
            ).joinToString(" · ")
        }
    }
    val lastMoved = if (triage.last > 0) Instant.ofEpochMilli(triage.last) else todo.updated ?: todo.created
    JSwipeActions(
        start = if (finished) SwipeAction(stringResource(R.string.agents_todo_reopen), PhIcons.ArrowUUpLeft, Status.Working, { onDone(todo, false) })
        else SwipeAction(stringResource(R.string.agents_todo_done_action), PhIcons.Check, Status.Working, { onDone(todo, true) }),
        end = SwipeAction(stringResource(R.string.agents_todo_delete_action), PhIcons.Trash, Status.NeedsYou, { onDelete(todo) }, destructive = true),
    ) {
        JCard(modifier = Modifier.fillMaxWidth(), onClick = onOpen) {
            Column(Modifier.padding(Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    // TalkBack reads the tick as a "Done" checkbox whose checked state says whether it is done, and the
                    // double-tap hint says what a tap does: Done or Reopen (#4077)
                    val tickName = stringResource(R.string.agents_todo_done_action)
                    val tickLabel = stringResource(if (finished) R.string.agents_todo_reopen else R.string.agents_todo_done_action)
                    Box(
                        Modifier.size(Tokens.Space.Standard.xl).clip(CircleShape)
                            .clickable(onClickLabel = tickLabel, role = Role.Checkbox) { onDone(todo, !finished) }
                            .semantics {
                                contentDescription = tickName
                                toggleableState = ToggleableState(finished)
                            },
                        contentAlignment = Alignment.Center,
                    ) {
                        if (todo.isCancelled) {
                            JIcon(PersonaIcons.end, null, size = ContentTokens.statusIcon, selected = true, tint = PersonaTheme.palette.ink3)
                        } else if (finished) {
                            JIcon(PhIcons.CheckCircle, null, size = ContentTokens.statusIcon, selected = true, tint = PersonaTheme.palette.done.dot)
                        } else {
                            Box(Modifier.size(ContentTokens.statusIcon).border(ContentTokens.optionOutline, PersonaTheme.palette.ink3, CircleShape))
                        }
                    }
                    Text(
                        todo.title,
                        style = if (finished) PersonaTheme.type.body.copy(textDecoration = TextDecoration.LineThrough) else PersonaTheme.type.headline,
                        color = if (finished) PersonaTheme.palette.ink3 else PersonaTheme.palette.ink,
                        // A concern keeps its trailing state tag; one title wrap keeps both readable at large text sizes.
                        maxLines = if (todo.kind == "concern") 2 else 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f),
                    )
                    when {
                        // an accepted task the hub reopened with more work reads Accepted, not Follow up (#3156)
                        accepted && state != TodoState.Working && state != TodoState.NeedsYou ->
                            JTag(stringResource(R.string.agents_todo_state_accepted), Tone.Positive)
                        todo.waitsForOwner() -> JTag(stringResource(R.string.agents_todos_section_waiting), status = Status.NeedsYou)
                        todo.deliveredAwaitingReview(sessionLookup) -> JTag(stringResource(R.string.agents_todos_section_awaiting_review), status = Status.Unread)
                        todo.isCancelled -> JTag(stringResource(R.string.agents_todo_cancelled))
                        finished && todo.doneBy == "auto" -> JTag(stringResource(R.string.agents_todo_auto_done))
                        finished -> {}
                        // the hub closed it, yet it waits on the owner: not Done (#3713)
                        !todo.startError.isNullOrEmpty() -> JTag(stringResource(R.string.agents_todo_state_didnt_start), Tone.Danger)
                        restStatus != null -> TodoRestStatusTag(restStatus)
                        else -> JTag(state.label)
                    }
                }
                if (todo.kind == "concern") {
                    val label = stringResource(R.string.agents_todo_concern)
                    // Directly below the title, never taking its width at large text sizes.
                    JTag(label, Modifier.padding(start = Tokens.Space.xxl).clearAndSetSemantics { contentDescription = label })
                }
                if (finished) {
                    todo.cancellationReason?.takeIf { it.isNotBlank() }?.let { reason ->
                        Text(stringResource(R.string.agents_todo_cancelled_reason, reason), style = PersonaTheme.type.caption,
                            color = PersonaTheme.palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.padding(start = Tokens.Space.xxl))
                    } ?: todo.autoDoneReason?.let { reason ->
                        Text(reason, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 2,
                            overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(start = Tokens.Space.xxl))
                    }
                } else {
                    // One line of what is going on (iOS TodoRow): why it finished on its own, the most urgent
                    // agent's latest (red while it waits for you), the notes, or that no agent has it yet.
                    TodoActivityLine(todo, urgent, urgent?.let { cards[it.agent.key] }, startFailed = todo.startError?.takeIf { it.isNotEmpty() }, suppressNotHanded = restStatus != null)
                    // Done as asked but not live yet (#3306): where it is held, while its agents are not at it again.
                    todo.hold?.takeIf { state != TodoState.Working && state != TodoState.NeedsYou }?.let {
                        TodoHoldLine(it, Modifier.padding(start = Tokens.Space.xxl))
                    }
                    todo.wait?.let { TodoWaitLine(it, now) }
                    TodoPlanLine(todo, Modifier.padding(start = Tokens.Space.xxl))
                    // A decision held for the next briefing (#4930): nothing was pushed for it, and the row says so.
                    if (todo.ask?.forBriefing == true) Text(stringResource(R.string.agents_todo_for_briefing),
                        Modifier.padding(start = Tokens.Space.xxl).testTag("todo-for-briefing-${todo.id}"),
                        style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                if (triage.section == TriageSection.Pr) {
                    // PR to merge: the PR, and how long since anything of it moved, however old (#3206)
                    val links = LocalUriHandler.current
                    val url = todo.prUrl?.takeIf { it.isNotBlank() }
                    Text(
                        stringResource(R.string.agents_todo_pr_moved, url?.let { prLabel(it) } ?: stringResource(R.string.agents_todo_pr), relativeTime(res, lastMoved, now)),
                        style = PersonaTheme.type.caption,
                        color = PersonaTheme.palette.accent,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.padding(start = Tokens.Space.xxl)
                            .then(if (url != null) Modifier.clickable { val _ = runCatching { links.openUri(url) } } else Modifier),
                    )
                }
                Row(
                    Modifier.fillMaxWidth().padding(start = Tokens.Space.xxl, top = Tokens.Space.xxs),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                ) {
                    val timeStr = relativeTime(res, lastMoved, now)
                    if (timeStr.isNotBlank() && triage.section != TriageSection.Pr) {
                        Text(timeStr, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
                    }
                    if (related > 0) {
                        Text(
                            pluralStringResource(R.plurals.agents_todos_related, related, related),
                            style = PersonaTheme.type.micro,
                            color = PersonaTheme.palette.accent,
                            maxLines = 1,
                            modifier = Modifier.clip(PersonaTheme.shapes.controlShape).clickable(onClick = onToggleRelated)
                                .padding(horizontal = Tokens.Space.xs),
                        )
                    }
                    if (onSeen != null || triage.section == TriageSection.Shelved || todo.currentAgents.isEmpty()) {
                        Spacer(Modifier.weight(1f))
                    }
                    if (onSeen != null) {
                        JTextButton(stringResource(R.string.agents_todo_seen), onSeen)
                    } else if (triage.section == TriageSection.Shelved) {
                        JTextButton(stringResource(R.string.agents_todo_restore), onRestore, icon = PhIcons.ArrowUUpLeft)
                    } else if (!finished && todo.currentAgents.isNotEmpty()) {
                        Row(Modifier.weight(1f), horizontalArrangement = Arrangement.End, verticalAlignment = Alignment.CenterVertically) {
                            Text(
                                agentMetadata ?: pluralStringResource(R.plurals.agents_todo_agents, todo.currentAgents.size, todo.currentAgents.size),
                                style = PersonaTheme.type.micro,
                                color = PersonaTheme.palette.ink3,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                                modifier = Modifier.weight(1f, fill = false),
                            )
                            if (agentMetadata != null && todo.currentAgents.size > 1) {
                                Spacer(Modifier.width(Tokens.Space.xs))
                                Text(stringResource(R.string.agents_todo_calendar_more, todo.currentAgents.size - 1),
                                    style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
                            }
                        }
                    }
                }
            }
        }
    }
}

/** Where a task's finished work is held short of live, in a line (iOS `TodoHold.line`): Held at PR · Blocked by"…" · Needs your approval. */
@Composable
internal fun TodoHoldLine(hold: TodoHold, modifier: Modifier = Modifier) {
    val step = stringResource(when (hold.step) {
        HoldStep.Pr -> R.string.agents_todo_hold_pr
        HoldStep.Merged -> R.string.agents_todo_hold_merged
        HoldStep.Off -> R.string.agents_todo_hold_off
    })
    val gate = hold.gate?.let { stringResource(R.string.agents_todo_hold_gate, it) }
    val who = when (hold.unblock) {
        HoldUnblock.User -> stringResource(R.string.agents_todo_hold_user)
        HoldUnblock.Agent -> stringResource(R.string.agents_todo_hold_agent)
        HoldUnblock.Other -> stringResource(R.string.agents_todo_hold_other)
        HoldUnblock.Supervisor -> stringResource(R.string.agents_todo_hold_supervisor)
        null -> null
    }
    Text(
        listOfNotNull(step, gate, who).joinToString(" · "),
        style = PersonaTheme.type.caption,
        color = PersonaTheme.palette.cautionText,
        maxLines = 2,
        overflow = TextOverflow.Ellipsis,
        modifier = modifier
    )
}

/** A due date in words, as a to-do's line says it: "Thu, Nov 12, 18:00", or the day alone; the year only when not [now]'s. */
internal fun dueWords(due: TodoDue, locale: Locale, zone: ZoneId, now: Instant): String {
    val year = now.atZone(zone).year
    fun format(skeleton: String, of: Int) =
        DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, if (of == year) skeleton else "y$skeleton"), locale)
    return when (due) {
        is TodoDue.At -> due.at.atZone(zone).let { format("EEEMMMdHHmm", it.year).format(it) }
        is TodoDue.Day -> format("EEEMMMd", due.day.year).format(due.day)
    }
}

/** A to-do's due date ("Due Thu, Nov 12, 18:00", red once past), high priority and a reminder still to push, in a line
 * (iOS `TodoPlanLine`, #1683); nothing without them. */
@Composable
internal fun TodoPlanLine(todo: Todo, modifier: Modifier = Modifier) {
    val zone = ZoneId.systemDefault()
    val locale = LocalConfiguration.current.locales[0]
    val now = Instant.now()
    val sameYear = { i: Instant -> i.atZone(zone).year == now.atZone(zone).year }
    val withTime = { i: Instant ->
        DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, if (sameYear(i)) "EEEMMMdHHmm" else "yEEEMMMdHHmm"), locale)
    }
    val dueText = todo.due?.let { dueWords(it, locale, zone, now) }
    val remind = todo.remind?.takeIf { todo.remindedAt == null && !todo.isDone }
    val high = todo.priority == "high" && !todo.isDone
    if (dueText == null && remind == null && !high) return
    val overdue = todo.isOverdue(now, zone)
    val parts = listOfNotNull(
        stringResource(R.string.agents_todo_priority_high).takeIf { high },
        dueText?.let { stringResource(if (overdue) R.string.agents_todo_was_due else R.string.agents_todo_due, it) },
        remind?.let { stringResource(R.string.agents_todo_remind_at, withTime(it).format(it.atZone(zone))) },
    )
    Text(
        parts.joinToString(" · "),
        style = PersonaTheme.type.caption,
        color = if (overdue) PersonaTheme.palette.danger.text else PersonaTheme.palette.ink3,
        maxLines = 1,
        overflow = TextOverflow.Ellipsis,
        modifier = modifier
    )
}

/** Why the to-do finished on its own, when the hub said; null otherwise (iOS `Todo.autoDoneReason`). */
internal val Todo.autoDoneReason: String?
    get() = doneReason?.trim()?.takeIf { doneBy == "auto" && it.isNotEmpty() }

/** What the most urgent agent of a to-do says on its row (iOS TodosView.mostUrgentSummary). */
internal sealed interface TodoUrgentSummary {
    data class StartFailed(val error: String) : TodoUrgentSummary
    data class Words(val text: String) : TodoUrgentSummary
    /** What the card it waits on asks (#3964). */
    data class Asks(val text: CoreText) : TodoUrgentSummary
}

/**
 * iOS mostUrgentSummary: a failed start with its error; else what its [card] asks while it waits for you; else the
 * session's last words as plain text, or its title; else the agent's own title. Nothing when none of them says
 * anything, so the notes or "Not handed to an agent yet" show instead.
 */
internal fun mostUrgentSummary(urgent: TodoMostUrgent?, card: Interaction? = null): TodoUrgentSummary? {
    urgent ?: return null
    val error = urgent.agent.startError?.trim().orEmpty()
    if (urgent.isFailed && error.isNotEmpty()) return TodoUrgentSummary.StartFailed(error)
    urgent.session?.let { session ->
        if (!urgent.isFailed) session.askingLine(card)?.let { return TodoUrgentSummary.Asks(it) }
        val last = session.lastText?.let(::plainMarkdown)?.takeIf(String::isNotBlank)
        return TodoUrgentSummary.Words(last ?: session.displayTitle)
    }
    return urgent.agent.title?.takeIf(String::isNotBlank)?.let(TodoUrgentSummary::Words)
}

/** The open row's line under the title: the auto-done reason, the most urgent agent, the notes, or "Not handed to an agent yet". */
@Composable
private fun TodoActivityLine(todo: Todo, urgent: TodoMostUrgent?, card: Interaction?, startFailed: String? = null, suppressNotHanded: Boolean = false) {
    val palette = PersonaTheme.palette
    val reason = todo.autoDoneReason
    // a failed start waiting on the owner: why it did not start, in red (DESIGN_SYSTEM 5.6)
    val summary = if (startFailed != null) stringResource(R.string.agents_todo_start_failed, startFailed) else when (val line = mostUrgentSummary(urgent, card)) {
        is TodoUrgentSummary.StartFailed -> stringResource(R.string.agents_todo_start_failed, line.error)
        is TodoUrgentSummary.Words -> line.text
        is TodoUrgentSummary.Asks -> line.text.text()
        null -> null
    }
    val notes = todo.notes.trim().takeIf { it.isNotEmpty() }
    val waiting = reason == null && summary != null && urgent?.status == AgentState.NeedsYou
    // Nobody is working on it (#3618): ahead of its agents' last words, which are from before they went; not ahead
    // of a failed start or an agent that waits for the user.
    val orphaned = todo.orphanedLine?.takeIf { reason == null && startFailed == null && !waiting }?.text()
    val text = reason ?: orphaned ?: summary ?: notes ?: if (suppressNotHanded) return else stringResource(R.string.agents_todo_not_handed)
    Row(
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        modifier = Modifier.padding(start = Tokens.Space.xxl)
    ) {
        if (reason == null && orphaned == null && urgent != null) {
            JStatusDot(when (urgent.status) {
                AgentState.NeedsYou -> Status.NeedsYou
                AgentState.Unread -> Status.Unread
                AgentState.Working -> Status.Working
                else -> Status.Idle
            })
        }
        Text(
            text,
            style = PersonaTheme.type.caption,
            color = when {
                startFailed != null && reason == null -> palette.danger.text
                orphaned != null -> palette.cautionText
                waiting -> palette.needsYou.text
                else -> palette.ink2
            },
            maxLines = if (notes != null && text == notes) 1 else 2,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f)
        )
    }
}

/** A to-do's state in the language on screen (core:contracts' `TodoState.title` is the Chinese). */
internal val TodoState.label: String @Composable get() = stringResource(when (this) {
    TodoState.Done -> R.string.agents_todos_done
    TodoState.NeedsYou -> R.string.agents_todo_state_needs_you
    TodoState.Unread -> R.string.agents_todo_state_unread
    TodoState.Working -> R.string.agents_todo_state_working
    TodoState.FollowUp -> R.string.agents_todo_state_follow_up
    TodoState.NotStarted -> R.string.agents_todo_state_not_started
})

internal val TodoRestStatus.label: String @Composable get() = stringResource(when (this) {
    TodoRestStatus.PrConflicts -> R.string.agents_todo_state_pr_conflicts
    TodoRestStatus.PrChangesRequested -> R.string.agents_todo_state_pr_changes_requested
    TodoRestStatus.PrChecksFailed -> R.string.agents_todo_state_pr_checks_failed
    TodoRestStatus.PrInReview -> R.string.agents_todo_state_pr_in_review
    TodoRestStatus.PrChecksPending -> R.string.agents_todo_state_pr_checks_pending
    TodoRestStatus.PrWaitingMerge -> R.string.agents_todo_state_pr_waiting_merge
    TodoRestStatus.ParkedMergeReview -> R.string.agents_todo_state_parked_merge_review
    TodoRestStatus.Parked -> R.string.agents_todo_state_parked
    TodoRestStatus.ConcernTracking -> R.string.agents_todo_state_tracking
})

@Composable
internal fun TodoRestStatusTag(rest: TodoRestStatus) = when (rest) {
    TodoRestStatus.PrConflicts, TodoRestStatus.PrChecksFailed -> JTag(rest.label, Tone.Danger)
    TodoRestStatus.PrChangesRequested -> JTag(rest.label, Tone.Caution)
    else -> JTag(rest.label)
}
