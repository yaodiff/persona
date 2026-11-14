package persona.core.domain.tasks

import java.time.Clock
import java.time.Duration
import java.time.Instant
import java.time.LocalDate
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import persona.core.contracts.AgentState
import persona.core.contracts.AssistantFeedEvent
import persona.core.contracts.AssistantViewActionEvent
import persona.core.contracts.AssistantViewBlockEvent
import persona.core.contracts.AssistantViewEvent
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.Session
import persona.core.contracts.PlanTask
import persona.core.contracts.RuleSuggestion
import persona.core.contracts.RuleSuggestionMade
import persona.core.contracts.TaskPlan
import persona.core.contracts.TaskPlanEvent
import persona.core.contracts.Todo
import persona.core.contracts.TodoDue
import persona.core.contracts.TodoAsk
import persona.core.contracts.TodoSpace
import persona.core.contracts.TodosEvent
import persona.core.contracts.draftPlaceholder
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.RequestManager
import persona.core.contracts.api.hubCodedFailure
import persona.core.contracts.api.TasksApi
import persona.core.domain.views.AssistantCardActions
import persona.core.domain.views.AssistantViewStore

/**
 * The to-dos last read from this pairing's hub, kept on the phone (iOS TodoStore's StateCache): a cold start or an open
 * without the hub shows them instead of "No to-dos" until the hub answers.
 */
/** A brief row's durable action result, shared by every copy of the same round in the conversation. */
enum class BriefOutcome { Taken, Completed, MarkedHandled, AlreadyHandled, HubTooOld }
fun briefAcceptOutcomeKey(todoId: String, askedAt: String) = "accept:$todoId:$askedAt"
fun briefLaterOutcomeKey(questionId: String) = "later:$questionId"

/**
 * What became of a card's suggested rule answered from here (#4929, docs/assistant/unified-entry.md "A rule after the same
 * answer three times").
 */
sealed interface RuleOutcome {
    /** Always was taken: what it stored ([RuleSuggestionMade.creates]; "" with the setting to turn on instead). */
    data class Made(val made: RuleSuggestionMade) : RuleOutcome

    /** Not now: the line goes, and the hub leaves it out for 30 days. */
    data object NotNow : RuleOutcome

    /** The answers do not earn it any more, or it was answered elsewhere (404): the line goes, nothing was made. */
    data object Gone : RuleOutcome
}

/** The [suggestion] a card carried when it was answered from here, and what became of it. */
data class RuleAnswer(val suggestion: RuleSuggestion, val outcome: RuleOutcome)

/**
 * The key a to-do's decision keeps its suggested rule under: the decision's own round ([askedAt]), so what was answered
 * on one decision is not drawn under the to-do's next one.
 */
fun todoRuleCard(todoId: String, askedAt: String) = "todo:$todoId:$askedAt"

/** The [TasksStore.busy] keys of a card's suggested rule: Always and Not now, one of them at a time. */
fun ruleAlwaysKey(card: String) = "rule-always:$card"
fun ruleNotNowKey(card: String) = "rule-not-now:$card"

interface TodoCache {
    suspend fun read(): List<Todo>?
    suspend fun write(todos: List<Todo>)
}

/** How long a Today card is held before an open asks the hub again whether its day has moved on (#5194). */
private val todayRecheck = Duration.ofMinutes(10)
private const val maximumFeedPages = 64

class TasksStore(
    private val api: TasksApi,
    connection: HubConnection,
    private val scope: CoroutineScope,
    private val cache: TodoCache? = null,
    private val clock: Clock = Clock.systemDefaultZone(),
) : TodoDecisionsSource {
    private val _todos = MutableStateFlow<List<Todo>>(emptyList())
    val todos: StateFlow<List<Todo>> = _todos.asStateFlow()
    /** The connected hub's declared capabilities; seen controls are absent without `todo_seen`. */
    val features: StateFlow<Set<String>>? = api.features
    /** Guards [answered] with the list: the cached list never replaces one the hub has given. */
    private val todosLock = Any()
    /** The hub's last answer (a list, or an older hub's 404 as none); null until it answers, while the cache shows. */
    private var answered: List<Todo>? = null
    private val acceptedAsks = mutableSetOf<Pair<String, String>>()
    /** Confirmed writes fence only reads already in flight; a later read may legitimately reopen the task. */
    private val finishedTasks = mutableMapOf<String, Pair<TodoAsk, Todo>>()
    private val writing = Mutex()
    private val refreshingTodos = Mutex()
    private val agentLookup = TaskAgentLookup(api, scope)
    val agentSessions = agentLookup.sessions
    val agentLookupFailures = agentLookup.failures

    /** Called by the visible conversation; exact lookups never change the Agents list window. */
    fun watchAgents(keys: Set<persona.core.contracts.SessionKey>) = agentLookup.watch(keys)

    /**
     * The whole row of one session, as the hub holds it: list rows are summaries, which carry only where a closeout
     * stands (#3612), not what the agent said it did and verified. Null from a hub without the exact lookup.
     */
    suspend fun sessionDetail(key: persona.core.contracts.SessionKey): persona.core.contracts.Session? = api.taskAgent(key)
    private var written: List<Todo>? = null
    private val _todosSupported = MutableStateFlow(true)
    val todosSupported: StateFlow<Boolean> = _todosSupported.asStateFlow()
    private val _plans = MutableStateFlow<Map<String, TaskPlan>>(emptyMap())
    val plans: StateFlow<Map<String, TaskPlan>> = _plans.asStateFlow()
    private val _feeds = MutableStateFlow<Map<String, List<FeedItem>>>(emptyMap())
    private val feedLock = Any()
    private val feedVersions = mutableMapOf<String, Long>()
    val feeds: StateFlow<Map<String, List<FeedItem>>> = _feeds.asStateFlow()
    private val _taskCards = MutableStateFlow<Map<String, List<persona.core.contracts.AssistantTaskCard>>>(emptyMap())
    val taskCards: StateFlow<Map<String, List<persona.core.contracts.AssistantTaskCard>>> = _taskCards.asStateFlow()
    /** Conversations whose whole feed has been read once: a live item alone does not make a later load incremental (#4689). */
    private val loaded = MutableStateFlow<Set<String>>(emptySet())
    /** Conversation → the task the next message goes straight to ("Say to 'xx'"). */
    val routes = MutableStateFlow<Map<String, String>>(emptyMap())
    val busy = MutableStateFlow<Set<String>>(emptySet())
    private val _briefOutcomes = MutableStateFlow<Map<String, BriefOutcome>>(emptyMap())
    val briefOutcomes = _briefOutcomes.asStateFlow()
    private val _ruleAnswers = MutableStateFlow<Map<String, RuleAnswer>>(emptyMap())
    /**
     * Card → the answer given here to the suggested rule it carried. Kept by the card, never by the suggestion's id: the
     * hub's ids name a kind and a scope, so the same id comes back on a later card (and, for the delegate, on another
     * manager's), and that card is to be answered on its own. What Always made stays for the card although the hub sends
     * it again without its suggestion; Not now and a stale suggestion last until the hub does ([ruleSettled]).
     */
    val ruleAnswers: StateFlow<Map<String, RuleAnswer>> = _ruleAnswers.asStateFlow()
    /** Message card ids put off with "Later"; another draft for the same inbox item remains visible. */
    val later = MutableStateFlow<Set<String>>(emptySet())
    /** Conversation → when the Today card was last asked for, and the day of the card the hub gave (null while asking). */
    private val todayAsked = HashMap<String, Pair<Instant, LocalDate?>>()
    /** This hub's assistant cards (docs/assistant/assistant-views.md): live cards, answers and registered reply blocks. */
    val cards = AssistantViewStore()
    val cardActions = AssistantCardActions(api, cards, reload = { load(it, whole = true) })

    private val _lnModeEnabled = MutableStateFlow(false)
    val lnModeEnabled: StateFlow<Boolean> = _lnModeEnabled.asStateFlow()
    private val _heldPushes = MutableStateFlow(HeldPushGroups())
    val heldPushes: StateFlow<HeldPushGroups> = _heldPushes.asStateFlow()
    private val _heldPushesLoading = MutableStateFlow(false)
    val heldPushesLoading: StateFlow<Boolean> = _heldPushesLoading.asStateFlow()
    private val _heldPushesFailed = MutableStateFlow(false)
    val heldPushesFailed: StateFlow<Boolean> = _heldPushesFailed.asStateFlow()
    private val heldPushesRevision = java.util.concurrent.atomic.AtomicLong(0L)

    init {
        if (cache != null) scope.launch(start = CoroutineStart.UNDISPATCHED) {
            val cached = try { cache.read() } catch (e: CancellationException) { throw e } catch (_: Exception) { null }
            if (!cached.isNullOrEmpty()) synchronized(todosLock) {
                if (answered == null && _todos.value.isEmpty()) { written = cached; _todos.value = cached }
            }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            connection.events.collect { e ->
                when (e) {
                    is TodosEvent -> reloadTodos()
                    is persona.core.contracts.SessionsListEvent -> agentLookup.refresh()
                    is AssistantFeedEvent -> { api.noteFeedOwner(e.item, e.manager); received(listOf(e.item)) }
                    is TaskPlanEvent -> _plans.update { it + (e.plan.id to e.plan) }
                    is AssistantViewEvent -> cards.view(e.conversation, e.view)
                    is AssistantViewActionEvent -> cards.answer(e.conversation, e.action)
                    is AssistantViewBlockEvent -> cards.block(e.conversation, e.key)
                    else -> Unit
                }
            }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            connection.state.collect {
                if (it == ConnectionState.Online) {
                    reloadTodos()
                    agentLookup.refresh()
                    loaded.value.forEach(::load)
                    refreshPushIntercepts()
                } else {
                    heldPushesRevision.incrementAndGet()
                    _heldPushesLoading.value = false
                }
            }
        }
    }

    /** Refresh only while the main Assistant page is visible or a hub reconnects; older responses cannot land late. */
    fun refreshPushIntercepts() {
        scope.launch {
            val ticket = heldPushesRevision.incrementAndGet()
            _heldPushesLoading.value = true
            try {
                val mode = api.lnMode()
                if (ticket != heldPushesRevision.get()) return@launch
                _lnModeEnabled.value = mode.enabled
                if (!mode.enabled) {
                    _heldPushes.value = HeldPushGroups()
                    _heldPushesFailed.value = false
                    return@launch
                }
                val response = api.pushIntercepts()
                if (ticket != heldPushesRevision.get()) return@launch
                val groups = heldPushGroups(response.intercepts, clock)
                if (groups != _heldPushes.value) _heldPushes.value = groups
                _heldPushesFailed.value = false
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                if (ticket != heldPushesRevision.get()) return@launch
                _heldPushesFailed.value = true
                val status = error::class.java.methods.firstOrNull { it.name == "getStatus" }?.invoke(error) as? Int
                if (status == 404 || error.message?.contains("404") == true) {
                    _lnModeEnabled.value = false
                    _heldPushes.value = HeldPushGroups()
                }
            } finally {
                if (ticket == heldPushesRevision.get()) _heldPushesLoading.value = false
            }
        }
    }

    fun reloadTodos() {
        scope.launch {
            refreshingTodos.withLock {
                try {
                    val completionsBeforeRead = synchronized(todosLock) { finishedTasks.toMap() }
                    val list = api.todos()
                    _todosSupported.value = true
                    todosLoaded = true
                    synchronized(todosLock) {
                        val shown = list.map { todo ->
                            val completion = finishedTasks[todo.id]?.takeIf { it != completionsBeforeRead[todo.id] }
                            when {
                                completion != null && (todo.ask == null || todo.ask == completion.first) -> completion.second
                                todo.ask?.askedAt?.let { (todo.id to it) in acceptedAsks } == true -> todo.copy(ask = null)
                                else -> todo
                            }
                        }
                        finishedTasks.keys.retainAll(shown.map { it.id }.toSet())
                        answered = shown
                        // The list first, then the flag, in this block: a card must not see "the list is in" while it
                        // still holds the previous one and call a just-dispatched task gone (#5110).
                        _todos.value = shown
                        todosReady.value = true
                    }
                    dropStaleRoutes()
                    remember()
                } catch (cancelled: CancellationException) {
                    throw cancelled
                } catch (error: Exception) {
                    val status = error::class.java.methods.firstOrNull { it.name == "getStatus" }?.invoke(error) as? Int
                    if (status == 404 || error.message?.contains("404") == true) {
                        _todosSupported.value = false
                        synchronized(todosLock) {
                            answered = emptyList()
                            _todos.value = emptyList()
                            todosReady.value = true
                        }
                        remember()
                    }
                    // Failed reads retain the last confirmed binding; a reconnect or open retries the read.
                }
            }
        }
    }

    /**
     * Keeps the hub's latest answer for the next start, one write at a time (two reads can answer out of order); a failed
     * write only costs that start its first paint.
     */
    private suspend fun remember() {
        if (cache == null) return
        writing.withLock {
            val latest = synchronized(todosLock) { answered } ?: return
            if (latest == written) return
            try { cache.write(latest); written = latest }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { }
        }
    }

    suspend fun createTodo(title: String, notes: String = "", due: TodoDue? = null): Todo {
        val created = api.createTodo(title, notes, due)
        reloadTodos()
        return created
    }

    suspend fun setTodoDone(todo: Todo, done: Boolean): Todo {
        val before = _todos.value
        val now = Instant.now().toString()
        _todos.update { list -> list.map { if (it.id == todo.id) it.copy(doneAt = if (done) now else null, doneBy = if (done) "user" else null) else it } }
        return try {
            val updated = api.patchTodo(todo.id, done = done)
            _todos.update { list -> list.map { if (it.id == updated.id) updated else it } }
            updated
        } catch (t: Throwable) {
            _todos.value = before
            throw t
        }
    }

    /** Got it / Undo (#5707): only seen_at changes, at once; [after] keeps a slow phone's mark after the finish it saw. */
    suspend fun setTodoSeen(id: String, seen: Boolean, after: Long = 0): Todo {
        val before = _todos.value.firstOrNull { it.id == id } ?: error("no to-do $id")
        val seenAt = if (seen) Instant.ofEpochMilli(maxOf(clock.millis(), after)).toString() else null
        val moved = before.copy(seenAt = seenAt)
        _todos.update { list -> list.map { if (it.id == id) moved else it } }
        return try {
            val updated = api.setTodoSeen(id, seen)
            _todos.update { list -> list.map { if (it.id == updated.id && it == moved) updated else it } }
            updated
        } catch (t: Throwable) {
            _todos.update { list -> list.map { if (it.id == id && it == moved) before else it } }
            throw t
        }
    }

    /**
     * Moves a to-do's due date (#2045, the calendar): drawn there at once, then the hub's answer; a failed save puts
     * back only this to-do, unless something else has changed it since, and throws.
     */
    suspend fun setTodoDue(id: String, due: TodoDue): Todo {
        val before = _todos.value.firstOrNull { it.id == id } ?: error("no to-do $id")
        val moved = before.withDue(due)
        _todos.update { list -> list.map { if (it.id == id) moved else it } }
        return try {
            val updated = api.setTodoDue(id, due)
            _todos.update { list -> list.map { if (it.id == updated.id) updated else it } }
            updated
        } catch (t: Throwable) {
            _todos.update { list -> list.map { if (it.id == id && it == moved) before else it } }
            throw t
        }
    }

    /** The detail page's due date, reminder and priority (#4636); the list follows the hub's answer. */
    suspend fun setTodoPlan(id: String, plan: persona.core.contracts.TodoPlan): Todo {
        val updated = api.setTodoPlan(id, plan)
        _todos.update { list -> list.map { if (it.id == updated.id) updated else it } }
        return updated
    }

    suspend fun updateTodo(id: String, title: String? = null, notes: String? = null): Todo {
        val updated = api.patchTodo(id, title = title, notes = notes)
        _todos.update { list -> list.map { if (it.id == updated.id) updated else it } }
        return updated
    }

    suspend fun deleteTodo(id: String) {
        api.deleteTodo(id)
        _todos.update { list -> list.filterNot { it.id == id } }
        dropStaleRoutes()
    }

    /** Routes to to-dos no longer listed (deleted here or on another device) go: their chip is hidden already. */
    private fun dropStaleRoutes() {
        val ids = _todos.value.mapTo(HashSet()) { it.id }
        routes.update { r -> r.filterValues { it in ids }.takeIf { it.size != r.size } ?: r }
    }

    /** A hub's router answers 404 for a route it doesn't have (isLegacySpacesRoute): one from before decisions shows none. */
    override suspend fun decisions(todoId: String): List<persona.core.contracts.TodoDecision>? = try {
        api.todoDecisions(todoId)
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        if (isLegacySpacesRoute(e)) null else throw e
    }

    override suspend fun addDecision(todoId: String, question: String, choice: String, reason: String, supersedesId: String) =
        api.addTodoDecision(todoId, question, choice, reason, supersedesId)

    suspend fun linkTodoAgent(todoId: String, machineId: String, sessionId: String) {
        api.linkTodoAgent(todoId, machineId, sessionId)
        reloadTodos()
    }

    suspend fun unlinkTodoAgent(todoId: String, machineId: String, sessionId: String) {
        api.unlinkTodoAgent(todoId, machineId, sessionId)
        reloadTodos()
    }

    /**
     * A read of the to-dos has answered: until then a missing to-do is still on its way, not gone (#5110).
     * A hub switch builds a new [TasksStore] (`AppServices`), so this starts false again with that hub.
     */
    val todosReady = MutableStateFlow(false)

    /** A read of the to-dos has answered: until then nothing tells whether the hub knows projects. */
    @Volatile private var todosLoaded = false
    /** The hub answered "Attach to to-do" with its router's 404: it has no such route (iOS TodosLogic.isLegacySpacesRoute). */
    @Volatile private var legacySpaces = false

    /**
     * Whether this hub knows projects on todos (#1616): an older one leaves `spaces` out altogether (iOS supportsSpaces).
     * False until the first read answers, and for good once the hub has no route for it.
     */
    val spacesSupported: Boolean
        get() = todosLoaded && !legacySpaces && _todos.value.let { list -> list.isEmpty() || list.any { it.spaces != null } }

    /**
     * "Attach to to-do" / "Remove from to-do" a whole project. A project is on one todo at a time: linking it takes it, and the agents it
     * brought, off any other first. The change shows at once; the hub then supplies the agents it attached.
     */
    suspend fun setTodoSpace(todoId: String, machineId: String, space: String, linked: Boolean) {
        val before = _todos.value
        val via = "$machineId/$space"
        _todos.update { list ->
            list.map { todo ->
                if (!linked && todo.id != todoId) return@map todo
                val others = todo.spaces?.filterNot { it.machineId == machineId && it.space == space }
                val spaces = if (linked && todo.id == todoId) others.orEmpty() + TodoSpace(machineId, space) else others
                val agents = if (!linked || todo.id != todoId) todo.agents.filterNot { it.source == "space" && it.via == via } else todo.agents
                todo.copy(spaces = spaces, agents = agents)
            }
        }
        try {
            if (linked) api.linkTodoSpace(todoId, machineId, space) else api.unlinkTodoSpace(todoId, machineId, space)
        } catch (t: Throwable) {
            _todos.value = before
            if (isLegacySpacesRoute(t)) legacySpaces = true
            throw t
        } finally {
            reloadTodos()
        }
    }

    /**
     * Pulls [conversation]'s feed: the items after the last one held (all of them with [whole] or before its first read; from the
     * oldest unsent message card on while it holds one, whose `filed_at` the hub works out on each read and sends no event for,
     * #5693 — not from the start, as the hub pages oldest first), and every time the full set of its cards, answers and registered blocks.
     */
    fun load(conversation: String, whole: Boolean = false) {
        scope.launch {
            val held = _feeds.value[conversation].orEmpty()
            val unsent = held.indexOfFirst { it.kind == "message" && it.sent == null && it.dismissedAt == null }
            val since = when {
                whole || conversation !in loaded.value -> null
                unsent >= 0 -> held.take(unsent).lastOrNull { it.at != held[unsent].at }?.at // reads are after `since`: skip ties
                else -> held.lastOrNull()?.at
            }
            val versions = synchronized(feedLock) { feedVersions.toMap() }
            runCatching { readFeedPages(conversation, since) }.onSuccess { res ->
                if (res.plans.isNotEmpty()) _plans.update { it + res.plans }
                res.taskCards?.takeIf { it.isNotEmpty() }?.let { cards ->
                    _taskCards.update { held ->
                        val merged = held[conversation].orEmpty().associateBy { it.id }.toMutableMap()
                        cards.forEach { card ->
                            val old = merged[card.id]
                            val previousAt = AssistantLines.time(old?.updatedAt)
                            val updatedAt = AssistantLines.time(card.updatedAt)
                            if (old == null || previousAt == null || (updatedAt != null && !updatedAt.isBefore(previousAt)))
                                merged[card.id] = card
                        }
                        held + (conversation to merged.values.toList())
                    }
                }
                cards.snapshot(conversation, res.views, res.viewActions, res.viewBlocks)
                received(res.items, conversation, versions)
                loaded.update { it + conversation }
            }.onFailure { error ->
                if (error is CancellationException) throw error
                // Do not advance the next read past a partial range, including rows with tied creation times.
                loaded.update { it - conversation }
            }
        }
    }

    private suspend fun readFeedPages(conversation: String, since: String?): FeedResponse {
        val reader = api.feedPages(conversation)
        val seen = HashSet<String>()
        var cursor: String? = null
        var result = FeedResponse()
        repeat(maximumFeedPages) {
            val page = reader.read(since, cursor)
            val heldCards = result.taskCards
            val pageCards = page.taskCards
            result = page.copy(items = result.items + page.items, plans = result.plans + page.plans,
                taskCards = when {
                    heldCards == null -> pageCards
                    pageCards == null -> heldCards
                    else -> heldCards + pageCards
                })
            val next = page.nextCursor
            if (next.isNullOrEmpty()) return result // Missing: an older hub; empty: explicit EOF, even on a full page.
            check(seen.add(next)) { "Repeated feed cursor" }
            cursor = next
        }
        error("Feed page limit exceeded")
    }

    private var main: String? = null
    /** The main assistant conversation's id once read: its dock also shows the agents no task stands for (#1113). */
    val mainConversation = MutableStateFlow<String?>(null)

    /**
     * The "Today" card, for the main conversation only (as iOS). A conversation that isn't the one held asks the hub again:
     * "New main chat", a move to another machine or another device replaces the main (#4690).
     */
    fun loadTodayIfMain(conversation: String) {
        scope.launch {
            val id = main.takeIf { it == conversation }
                ?: runCatching { api.mainConversation() }.getOrNull()?.also { main = it; mainConversation.value = it }
            if (id == conversation) loadToday(conversation)
        }
    }

    /** What [conversation]'s dock shows after its tasks (iOS `DockAgent.shown`); see [dockAgents]. */
    fun agentsOf(conversation: String, sessions: List<Session>): List<Session> =
        dockAgents(conversation, mainConversation.value, sessions, _todos.value, "sessions_hierarchy" in features?.value.orEmpty())

    /**
     * The hub counts the day in the assistant's time zone, which need not be the phone's (#5194): the card is asked
     * again on the phone's new day, and on an open [todayRecheck] after the last ask unless the card's day is already
     * past the phone's (that hub cannot move on before the phone does).
     */
    fun loadToday(conversation: String) {
        val now = clock.instant()
        val today = LocalDate.now(clock)
        todayAsked[conversation]?.let { (at, day) ->
            // at.atZone(…).toLocalDate(), not LocalDate.ofInstant: that one is Java 9, on Android only from API 34, and this
            // module is plain JVM (no lint, no desugaring), so it built fine and threw NoSuchMethodError on API 30–33 (#5227).
            if (at.atZone(clock.zone).toLocalDate() == today &&
                (day == null || day > today || now < at + todayRecheck)) return
        }
        todayAsked[conversation] = now to null
        scope.launch {
            runCatching { api.today(conversation) }.onSuccess { card ->
                todayAsked[conversation] = now to (card.ref?.let { runCatching { LocalDate.parse(it) }.getOrNull() } ?: today)
                received(listOf(card))
            }.onFailure { todayAsked.remove(conversation) }
        }
    }

    private fun received(items: List<FeedItem>, conversation: String? = null, readVersions: Map<String, Long>? = null) = synchronized(feedLock) {
        // A response captured before a write or live update cannot resurrect a dismissed draft or undo an Undo.
        val current = items.filter { readVersions == null || readVersions[it.id] == feedVersions[it.id] }
        // A live card's creation item carries the card as it was then: drawn even if its assistant_view was missed.
        current.filter { it.kind == "view" }.forEach { item ->
            ((item.body as? JsonObject)?.get("view"))?.let { cards.view(item.conversation, it) }
        }
        current.forEach { item ->
            val old = _feeds.value[item.conversation].orEmpty().firstOrNull { it.id == item.id }
            if (old != item) feedVersions[item.id] = (feedVersions[item.id] ?: 0L) + 1L
        }
        _feeds.update { feeds ->
            var next = conversation?.let { if (it in feeds) feeds else feeds + (it to emptyList()) } ?: feeds
            for ((c, list) in current.groupBy { it.conversation }) next = next + (c to AssistantLines.merge(next[c].orEmpty(), list))
            next
        }
        if (current.any { it.kind == "task" || it.kind == "receipt" }) reloadTodos()
    }

    fun tasksOf(conversation: String): List<Todo> = _todos.value.filter { it.origin == conversation }

    /** The task's agents among [sessions], and its state. */
    fun agentsOf(todo: Todo, sessions: List<Session>): List<Session> =
        todo.activeAgents.filter { it.dismissedAt == null && it.machineId.isNotBlank() && it.sessionId.isNotBlank() }.map { a ->
            val listed = sessions.firstOrNull { it.key == a.key }
            val exact = agentSessions.value[a.key]
            listOfNotNull(listed, exact).maxByOrNull { it.rev ?: 0L } ?: Session(
                machineId = a.machineId, sessionId = a.sessionId, title = a.title.orEmpty(), cwd = a.cwd.orEmpty(),
                agent = a.agent, state = "offline", status = "offline", startError = a.startError,
            )
        }

    fun stateOf(todo: Todo, sessions: List<Session>): TaskState =
        agentsOf(todo, sessions).let { a -> TaskState.of(todo, a.map { it.agentState }, a.any { it.interrupted }) }

    /** The agent to open first: the one waiting for the user, then one cut off (#1516), then working, then the latest. */
    fun lead(todo: Todo, sessions: List<Session>): Session? =
        agentsOf(todo, sessions).minWithOrNull(compareBy<Session>({ it.agentState != AgentState.NeedsYou }, { !it.interrupted },
            { it.agentState.priority }).thenByDescending { it.updatedAt.orEmpty() })

    /** [edits] is "Edit": the plan's tasks as the user changed them, by index (null: as planned). */
    fun startPlan(plan: TaskPlan, remember: Boolean, onError: (Throwable) -> Unit, edits: List<PlanTask>? = null, manager: String? = null) = act(plan.id, onError) {
        val p = withContext(planOwner(plan, manager)) { if (edits == null) api.startPlan(plan.id, remember) else api.startPlan(plan.id, remember, edits) }
        _plans.update { it + (p.id to p) }; reloadTodos()
    }

    fun dismissPlan(plan: TaskPlan, onError: (Throwable) -> Unit, manager: String? = null) = act(plan.id, onError) {
        val p = withContext(planOwner(plan, manager)) { api.dismissPlan(plan.id) }; _plans.update { it + (p.id to p) }
    }

    /**
     * A plan's routes (`task-plans/<id>/…`) name no conversation, so the hub would take them to the default manager: they
     * name the manager of the plan's conversation (the one a page listed it under), whichever assistant is current, as
     * every other request about another assistant's thing does (docs/assistant/managers.md "Clients"). The conversation's own
     * session row says it ([manager]; it survives a restart, a list's registry doesn't), else the manager a page listed it under.
     * None for the default's.
     */
    private fun planOwner(plan: TaskPlan, manager: String?): kotlin.coroutines.CoroutineContext =
        (manager ?: AssistantManagerChoice.itemHeader(listOf("assistant", "conversations", plan.conversation)))?.let(::RequestManager)
            ?: kotlin.coroutines.EmptyCoroutineContext

    /**
     * "Retry" on a task whose agent did not start (#1396): the hub starts the same task again in the same todo, which it
     * answers reopened — put in at once, so its card shows it starting where it was. Taps while one is on its way do
     * nothing ([busy] under [retryKey]); the hub starts one agent however many arrive. On a task the model service cut
     * off (#1516) it is "Continue": the hub tells its agent to go on, once.
     */
    fun retryTask(todo: Todo, onError: (Throwable) -> Unit) = act(retryKey(todo.id), onError) {
        val t = api.retryTask(todo.id)
        _todos.update { list -> list.map { if (it.id == t.id) t else it } }
        reloadTodos()
    }

    /** Removes a task that does not run (one whose agent never started): its todo goes, and any route to it. */
    fun removeTask(todo: Todo, onError: (Throwable) -> Unit) = act("remove:${todo.id}", onError) {
        api.removeTask(todo.id)
        _todos.update { list -> list.filterNot { it.id == todo.id } }
        routes.update { r -> r.filterValues { it != todo.id } }
        reloadTodos()
    }

    fun acceptTask(todo: Todo, onError: (Throwable) -> Unit, onChanged: () -> Unit) {
        val ask = todo.ask ?: return
        if (ask.askedAt.isBlank()) return
        act("accept:${todo.id}", onError) {
            if (_todos.value.firstOrNull { it.id == todo.id }?.ask != ask) { onChanged(); return@act }
            api.acceptTask(todo.id, ask.askedAt)
            synchronized(todosLock) {
                acceptedAsks += todo.id to ask.askedAt
                _todos.update { rows -> rows.map { if (it.id == todo.id && it.ask == ask) it.copy(ask = null) else it } }
            }
            reloadTodos()
        }
    }

    /**
     * Answers a decision's round as the owner read it (#3804): one of its options, or a fixed response. [onDone] hears
     * whether the hub took it; a round answered elsewhere meanwhile (409) is not an error: it took nothing, and the
     * to-dos are read again. An option ends the decision, so its ask goes at once; a fixed response leaves it to the hub.
     */
    fun answerTask(todo: Todo, answer: persona.core.contracts.api.AskAnswer, onError: (Throwable) -> Unit,
        onChanged: () -> Unit, onDone: (taken: Boolean) -> Unit) {
        val ask = todo.ask ?: return
        if (ask.askedAt.isBlank()) return
        act("accept:${todo.id}", onError) {
            if (_todos.value.firstOrNull { it.id == todo.id }?.ask != ask) { onChanged(); return@act }
            val taken = try {
                withManager(todo.manager) { api.answerTask(todo.id, ask.askedAt, answer) }
                true
            } catch (error: Throwable) {
                if (error is CancellationException || hubCodedFailure(error)?.status != 409) throw error
                false
            }
            onDone(taken)
            if (taken && answer is persona.core.contracts.api.AskAnswer.Option) synchronized(todosLock) {
                acceptedAsks += todo.id to ask.askedAt
                _todos.update { rows -> rows.map { if (it.id == todo.id && it.ask == ask) it.copy(ask = null) else it } }
            }
            reloadTodos()
        }
    }

    /** Ask the hub to accept the exact round carried by a brief; a superseded round ends in 409. */
    fun acceptBrief(todoId: String, askedAt: String, onError: (Throwable) -> Unit, manager: String? = null) {
        if (todoId.isBlank() || askedAt.isBlank()) return
        val key = briefAcceptOutcomeKey(todoId, askedAt)
        if (key in _briefOutcomes.value) return
        act("accept:$todoId", onError) {
            try {
                withManager(manager) { api.acceptTask(todoId, askedAt) }
                synchronized(todosLock) {
                    acceptedAsks += todoId to askedAt
                    _todos.update { rows -> rows.map { if (it.id == todoId && it.ask?.askedAt == askedAt) it.copy(ask = null) else it } }
                }
                _briefOutcomes.update { it + (key to BriefOutcome.Taken) }
                reloadTodos()
            } catch (error: Throwable) {
                if (!briefActionTerminal(key, error)) throw error
            }
        }
    }

    /** Complete only the decision round still current in the shared to-do list. */
    fun finishBrief(todoId: String, askedAt: String, onError: (Throwable) -> Unit, manager: String? = null) {
        if (todoId.isBlank() || askedAt.isBlank()) return
        val key = briefAcceptOutcomeKey(todoId, askedAt)
        if (key in _briefOutcomes.value) return
        finishDecision(todoId, askedAt, expectedAsk = null, manager = manager, onError = onError,
            onChanged = { _briefOutcomes.update { it + (key to BriefOutcome.AlreadyHandled) } },
            onCompleted = { _briefOutcomes.update { it + (key to BriefOutcome.Completed) } })
    }

    /** Remove a question from the hub's later-briefing queue. */
    fun markBriefHandled(questionId: String, onError: (Throwable) -> Unit, manager: String? = null) {
        if (questionId.isBlank()) return
        val key = briefLaterOutcomeKey(questionId)
        if (key in _briefOutcomes.value) return
        act("later:$questionId", onError) {
            try {
                withManager(manager) { api.markLaterHandled(questionId) }
                _briefOutcomes.update { it + (key to BriefOutcome.MarkedHandled) }
            } catch (error: Throwable) {
                if (!briefActionTerminal(key, error)) throw error
            }
        }
    }

    /**
     * "Always" on the suggested rule [card] carries: the hub counts the answers again and makes the rule only if they
     * still earn it. What it made is kept for the card ([ruleAnswers]); a suggestion that is gone (404) only loses its
     * line. Any other refusal (403 without the full-access role, 409 when the standing authorizations changed meanwhile,
     * 503) goes to [onError] and leaves the line to answer again.
     */
    fun ruleAlways(card: String, suggestion: RuleSuggestion, onError: (Throwable) -> Unit, manager: String? = null) {
        if (!ruleAnswerable(card, suggestion)) return
        act(ruleAlwaysKey(card), onError) {
            try {
                val made = withManager(manager) { api.ruleAlways(suggestion.id) }
                _ruleAnswers.update { it + (card to RuleAnswer(suggestion, RuleOutcome.Made(made))) }
            } catch (error: Throwable) {
                if (!ruleSuggestionGone(card, suggestion, error)) throw error
            }
            reloadTodos()
        }
    }

    /** "Not now" on the suggested rule [card] carries: its line goes at once. */
    fun ruleNotNow(card: String, suggestion: RuleSuggestion, onError: (Throwable) -> Unit, manager: String? = null) {
        if (!ruleAnswerable(card, suggestion)) return
        act(ruleNotNowKey(card), onError) {
            try {
                withManager(manager) { api.ruleNotNow(suggestion.id) }
                _ruleAnswers.update { it + (card to RuleAnswer(suggestion, RuleOutcome.NotNow)) }
            } catch (error: Throwable) {
                if (!ruleSuggestionGone(card, suggestion, error)) throw error
            }
            reloadTodos()
        }
    }

    /**
     * The hub sent [card] without a suggestion: a line put away here (Not now, stale) is settled, and a suggestion the
     * card earns later, under the same id, is drawn again. What Always made stays.
     */
    fun ruleSettled(card: String) {
        _ruleAnswers.update { answers -> if (answers[card]?.outcome is RuleOutcome.Made) answers else answers - card }
    }

    /** Still to answer on [card], with neither of its two actions on the way. */
    private fun ruleAnswerable(card: String, suggestion: RuleSuggestion): Boolean {
        if (suggestion.id.isBlank() || _ruleAnswers.value[card]?.suggestion?.id == suggestion.id) return false
        return ruleAlwaysKey(card) !in busy.value && ruleNotNowKey(card) !in busy.value
    }

    /** A stale suggestion (404) is cleared, not an error to show; cancellation and every other failure are the caller's. */
    private fun ruleSuggestionGone(card: String, suggestion: RuleSuggestion, error: Throwable): Boolean {
        if (error is CancellationException || hubCodedFailure(error)?.status != 404) return false
        _ruleAnswers.update { it + (card to RuleAnswer(suggestion, RuleOutcome.Gone)) }
        return true
    }

    /** 409 / 404 are terminal answers, not a retry that could repeat a completed action. */
    private fun briefActionTerminal(key: String, error: Throwable): Boolean {
        val outcome = when (hubCodedFailure(error)?.status) {
            409 -> BriefOutcome.AlreadyHandled
            404 -> BriefOutcome.HubTooOld
            else -> null
        } ?: return false
        _briefOutcomes.update { it + (key to outcome) }
        return true
    }

    /** User completion is not acceptance: no recommendation is sent and no agent is resumed. */
    fun finishTaskNow(todo: Todo, onError: (Throwable) -> Unit, onChanged: () -> Unit) {
        val ask = todo.ask?.takeIf { it.outcome == "decide" } ?: return
        if (todo.isDone || todo.cancelledAt != null) return
        finishDecision(todo.id, ask.askedAt, expectedAsk = ask, manager = null, onError = onError, onChanged = onChanged)
    }

    /** Both direct-completion entries share the current-round fence and the confirmed local merge. */
    private fun finishDecision(
        todoId: String,
        askedAt: String,
        expectedAsk: TodoAsk?,
        manager: String?,
        onError: (Throwable) -> Unit,
        onChanged: () -> Unit,
        onCompleted: () -> Unit = {},
    ) {
        // Shares acceptance's guard so contradictory actions cannot be submitted together.
        act("accept:$todoId", onError) {
            val current = _todos.value.firstOrNull { it.id == todoId }
            if (current == null) {
                // No hub answer yet is not proof that the round ended; load it and leave the action available.
                if (synchronized(todosLock) { answered != null }) onChanged() else reloadTodos()
                return@act
            }
            val ask = current.ask
            if (ask == null || ask.askedAt != askedAt || ask.outcome != "decide" ||
                expectedAsk != null && ask != expectedAsk || current.isDone || current.cancelledAt != null) {
                onChanged()
                return@act
            }
            val completed = withManager(manager) { api.patchTodo(todoId, done = true) }
            check(completed.id == todoId && completed.isDone) { "Completion was not confirmed" }
            synchronized(todosLock) {
                val confirmed = completed.copy(ask = null)
                finishedTasks[todoId] = ask to confirmed
                _todos.update { rows -> rows.map { if (it.id == todoId && it.ask == ask) confirmed else it } }
                answered = (answered ?: written)?.map { if (it.id == todoId && it.ask == ask) confirmed else it }
            }
            routes.update { routes -> routes.filterValues { it != todoId } }
            remember()
            onCompleted()
            reloadTodos()
        }
    }

    private suspend fun <T> withManager(manager: String?, block: suspend () -> T): T =
        if (manager == null) block() else withContext(RequestManager(manager)) { block() }

    private fun act(id: String, onError: (Throwable) -> Unit, block: suspend () -> Unit) {
        if (id in busy.value) return
        busy.update { it + id }
        scope.launch {
            try { block() }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Throwable) { onError(error) }
            finally { busy.update { it - id } }
        }
    }

    fun setRoute(conversation: String, todoId: String?) {
        routes.update { if (todoId == null) it - conversation else it + (conversation to todoId) }
    }

    /**
     * Sends text straight to the task the conversation is routed to; false when there is none or its to-do is gone.
     * The route is consumed only by the hub's accepting response. A failure or cancellation leaves it ready to retry.
     */
    suspend fun sendRouted(conversation: String, text: String): Boolean {
        val todoId = routes.value[conversation] ?: return false
        if (_todos.value.none { it.id == todoId }) {
            setRoute(conversation, null)
            return false // the hub would refuse it (409): Persona gets it instead
        }
        val routed = api.route(conversation, todoId, text)
        received(listOf(routed))
        routes.update { current -> if (current[conversation] == todoId) current - conversation else current }
        return true
    }

    /**
     * "Send" on a message card: the (edited) draft goes out through the inbox; the hub records it on the card, whose
     * `assistant_feed` event draws it sent (put in at once here too). Taps while one is on its way, and on a card
     * already sent or dealt with elsewhere (`filed_at`, #5095), do nothing ([busy] under the card's id); a draft with a
     * blank to fill in is not sent.
     */
    fun sendMessage(card: FeedItem, text: String, onError: (Throwable) -> Unit) {
        val itemId = card.ref ?: return
        val latest = { _feeds.value[card.conversation]?.firstOrNull { it.id == card.id } ?: card }
        if (!latest().canSend) return
        draftPlaceholder(text)?.let { onError(DraftPlaceholderLeft(it)); return }
        act(card.id, onError) {
            api.inboxReply(itemId, text, card.id)
            val now = latest()
            if (now.sent == null) {
                val body = JsonObject(((now.body as? JsonObject) ?: JsonObject(emptyMap())) +
                    mapOf("sent_at" to JsonPrimitive(Instant.now().toString()), "sent" to JsonPrimitive(text)))
                received(listOf(now.copy(body = body)))
            }
        }
    }

    fun putOff(cardId: String) = later.update { it + cardId }

    fun dismissMessage(card: FeedItem, onError: (Throwable) -> Unit, onDismissed: (persona.core.contracts.api.MessageDraftDismissal) -> Unit) {
        val latest = _feeds.value[card.conversation]?.firstOrNull { it.id == card.id } ?: card
        val original = card.field("draft") ?: return
        if (latest.kind != "message" || !latest.canSend || latest.field("draft") != original) return
        act(card.id, onError) {
            val dismissal = api.dismissMessage(card.id, original)
            check(dismissal.item.id == card.id && dismissal.item.conversation == card.conversation)
            synchronized(feedLock) {
                val current = _feeds.value[card.conversation]?.firstOrNull { it.id == card.id } ?: card
                if (current.field("draft") == original && current.sent == null && current.filedAt == null) {
                    received(listOf(dismissal.item))
                    onDismissed(dismissal)
                }
            }
        }
    }

    fun undoDismissMessage(dismissal: persona.core.contracts.api.MessageDraftDismissal, onError: (Throwable) -> Unit) {
        val card = dismissal.item
        val captured = _feeds.value[card.conversation]?.firstOrNull { it.id == card.id } ?: return
        if (captured.field("dismissal_id") != dismissal.dismissalId || captured.sent != null || captured.filedAt != null) return
        act(card.id, onError) {
            val restored = api.undoDismissMessage(card.id, dismissal.dismissalId)
            check(restored.id == card.id && restored.conversation == card.conversation)
            synchronized(feedLock) {
                val current = _feeds.value[card.conversation]?.firstOrNull { it.id == card.id }
                if (current == captured) {
                    later.update { it - card.id }
                    received(listOf(restored))
                }
            }
        }
    }

    companion object {
        /** The [busy] key of a task's "Retry". */
        fun retryKey(todoId: String) = "retry:$todoId"
    }
}
