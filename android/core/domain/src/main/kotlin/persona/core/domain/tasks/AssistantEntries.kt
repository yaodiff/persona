package persona.core.domain.tasks

import persona.core.contracts.AgentState
import persona.core.contracts.FeedItem
import persona.core.contracts.GroupKeys
import persona.core.contracts.Interaction
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.parseTime
import java.time.Instant

/**
 * The Assistant's two entries above the composer (#3387, web `lib/assistant-entries.ts`, spec/fixtures/entries):
 * Needs you lists everything waiting on the user, In progress everything running that does not. Each thing is one row
 * in exactly one of them, Needs you first; finished things are in neither (the board has them), and each entry's count
 * is its rows. A task is one row whatever its agents: they are never rows of their own. In the main conversation an
 * Agent that belongs to no task (opened by hand) is a row of its own.
 */
sealed interface NeedsYouEntry {
    val key: String

    /** A task whose agent asks something ([asks], oldest first), or that waits for the user's decision or review. */
    data class Task(val todo: Todo, val status: TriageStatus, val asks: List<Interaction>) : NeedsYouEntry {
        override val key get() = "task:${todo.id}"
    }

    /** An Agent that belongs to no task, asking something. */
    data class Agent(val session: Session, val asks: List<Interaction>) : NeedsYouEntry {
        override val key get() = "agent:${session.machineId}/${session.sessionId}"
    }

    /** A plan waiting for Start: one row whatever its task count. */
    data class Plan(val plan: TaskPlan) : NeedsYouEntry {
        override val key get() = "plan:${plan.id}"
    }

    /** A message card waiting for Send. */
    data class Message(val item: FeedItem) : NeedsYouEntry {
        override val key get() = "message:${item.id}"
    }
}

sealed interface RunningEntry {
    val key: String

    /**
     * A dispatched task not finished and not waiting on the user. [waiting]: nothing of it runs and one of its agents waits to
     * be brought back (iOS `TaskState.waiting`), by what it waits on or by background work that only waits.
     * [hasAgentsElsewhere]: some current Agents have another listed task as their request home.
     */
    data class Task(val todo: Todo, val status: TriageStatus, val activity: BackgroundWork? = null, val waiting: Boolean = false,
                    val hasAgentsElsewhere: Boolean = false) : RunningEntry {
        override val key get() = "task:${todo.id}"
    }

    /** A working Agent that belongs to no task. [waiting]: it works only because its background work waits (iOS `DockAgent.backgroundWaiting`). */
    data class Agent(val session: Session, val activity: BackgroundWork? = session.backgroundWork(),
                     val waiting: Boolean = activity == null && session.waitsInBackground) : RunningEntry {
        override val key get() = "agent:${session.machineId}/${session.sessionId}"
    }
}

/** [shelved]: open tasks with no progress for 48 hours and nothing running, folded under In progress and not counted. */
data class AssistantEntries(val needsYou: List<NeedsYouEntry>, val running: List<RunningEntry>, val shelved: List<RunningEntry>)

/** The task waits for the user's decision or review: an open ask (a decided one only while the task is open), or its
 *  supervisor's `pending_review` / `needs_user` on an open task. */
fun Todo.awaitsDecision(): Boolean {
    openAsk?.let { return !(isDone && it.outcome == "decide") }
    return !isDone && (supervision?.state == "pending_review" || supervision?.state == "needs_user")
}

/** The web's `todoRank` (what the user must act on first), on the triage status. */
private fun Todo.entryRank(status: TriageStatus): Int {
    if ((supervision?.state == "pending_review" || openAsk != null) &&
        status in setOf(TriageStatus.FollowUp, TriageStatus.Unread, TriageStatus.NotStarted)) return 20
    return when (status) {
        TriageStatus.NeedsYou -> 0
        TriageStatus.Interrupted -> 10
        TriageStatus.Failed -> 30
        TriageStatus.Working -> 40
        TriageStatus.NotStarted -> 50
        TriageStatus.Unread -> 60
        TriageStatus.FollowUp -> 70
        TriageStatus.Done -> 80
    }
}

fun assistantEntries(
    conversation: String, main: String?, todos: List<Todo>, sessions: List<Session>, interactions: List<Interaction>,
    plans: Collection<TaskPlan>, feed: List<FeedItem>, now: Long, restored: Map<String, Long> = emptyMap(),
): AssistantEntries {
    val isMain = !main.isNullOrBlank() && main == conversation
    val bySession = sessions.associateBy { GroupKeys.rawKey(it.key) }
    val lookup = { key: SessionKey -> bySession[GroupKeys.rawKey(key)] }
    val asksOf = interactions.sortedWith(compareBy<Interaction> { parseTime(it.createdAt) ?: Instant.MIN }.thenBy { it.id })
        .groupBy { GroupKeys.rawKey(it.sessionKey) }
    val time = { at: String? -> parseTime(at) ?: Instant.MIN }

    val asking = mutableListOf<NeedsYouEntry>()
    val deciding = mutableListOf<Pair<NeedsYouEntry.Task, Int>>()
    val open = mutableListOf<Pair<RunningEntry.Task, Int>>()
    val shelved = mutableListOf<RunningEntry.Task>()
    val folded = mutableSetOf<SessionKey>()
    // failed starts whose work finished in a related to-do: closed, as the To-dos page has them (#3713)
    val superseded = supersededFailedStarts(todos)
    // the main conversation watches every task (docs/assistant/unified-entry.md); another one the tasks it dispatched
    val listed = todos.filter { isMain || it.origin == conversation }
    // One home per request: prefer the task the hub ties the session to, else the first current task listing it.
    // Other tasks neither repeat its cards nor wait on the user for them (web `assistantEntries`, #7531).
    val listing = mutableMapOf<SessionKey, MutableSet<String>>()
    for (todo in listed.filter { !it.isDone }) {
        for (agent in todo.activeAgents) listing.getOrPut(agent.key) { linkedSetOf() }.add(todo.id)
    }
    val homes = listing.mapValues { (key, ids) -> lookup(key)?.todo?.id?.takeIf { it in ids } ?: ids.first() }
    for (todo in listed) {
        val active = if (todo.isDone) emptyList() else todo.activeAgents
        val own = active.filter { homes[it.key] == todo.id }
        val hasAgentsElsewhere = own.size < active.size
        val allStatus = todo.triageStatus(lookup)
        val status = if (hasAgentsElsewhere && allStatus == TriageStatus.NeedsYou) {
            when (val ownStatus = todo.copy(agents = own).triageStatus(lookup)) {
                TriageStatus.NotStarted -> TriageStatus.Working
                else -> ownStatus
            }
        } else allStatus
        val asks = own.flatMap { asksOf[GroupKeys.rawKey(it.key)].orEmpty() }
        val bucket = todo.bucket(lookup, now, restored[todo.id] ?: 0L, superseded = todo.id in superseded)
        // a failed start carries done_at (the hub ends it), yet it is the user's to retry while its bucket says open
        val waits = if (status == TriageStatus.Failed) bucket == TodoBucket.Open
            else !todo.isDone && (status == TriageStatus.NeedsYou || status == TriageStatus.Interrupted)
        when {
            asks.isNotEmpty() -> asking += NeedsYouEntry.Task(todo, status, asks)
            waits || todo.awaitsDecision() -> deciding += NeedsYouEntry.Task(todo, status, asks) to todo.entryRank(status)
            bucket == TodoBucket.Open -> {
                val activity = todo.backgroundWork(lookup)
                val waiting = activity == null && todo.activeAgents.mapNotNull { lookup(it.key) }.any { it.waitsWithoutBackground() || it.waitsInBackground }
                open += RunningEntry.Task(todo, status, activity, waiting, hasAgentsElsewhere) to todo.entryRank(status)
            }
            bucket == TodoBucket.Shelved -> shelved += RunningEntry.Task(todo, status, hasAgentsElsewhere = hasAgentsElsewhere)
            else -> continue
        }
        folded += active.map { it.key }
    }

    // Agents that belong to no task, the main conversation's only: asking ones wait on the user, working ones run.
    val agentsAsking = mutableListOf<NeedsYouEntry>()
    val agentsRunning = mutableListOf<RunningEntry>()
    for (session in dockAgents(conversation, main, sessions, todos)) {
        if (session.key in folded) continue
        if (session.agentState == AgentState.NeedsYou) agentsAsking += NeedsYouEntry.Agent(session, asksOf[GroupKeys.rawKey(session.key)].orEmpty())
        else agentsRunning += RunningEntry.Agent(session)
    }

    val firstAsk = { entry: NeedsYouEntry ->
        when (entry) {
            is NeedsYouEntry.Task -> entry.asks.firstOrNull()?.createdAt
            is NeedsYouEntry.Agent -> entry.asks.firstOrNull()?.createdAt
            else -> null
        }.let(time)
    }
    val plansWaiting = plans.filter { it.conversation == conversation && it.state == "open" }
        .sortedWith(compareBy<TaskPlan> { time(it.createdAt) }.thenBy { it.id }).map { NeedsYouEntry.Plan(it) }
    val messages = feed.filter { it.kind == "message" && it.canSend }.map { NeedsYouEntry.Message(it) }
    return AssistantEntries(
        needsYou = (asking + agentsAsking).sortedBy(firstAsk) + plansWaiting +
            deciding.sortedWith(compareBy<Pair<NeedsYouEntry.Task, Int>> { it.second }.thenByDescending { time(it.first.todo.createdAt) }).map { it.first } +
            messages,
        running = open.sortedWith(compareBy<Pair<RunningEntry.Task, Int>> { it.second }.thenByDescending { time(it.first.todo.createdAt) }).map { it.first } +
            agentsRunning,
        shelved = shelved.sortedByDescending { time(it.todo.createdAt) },
    )
}
