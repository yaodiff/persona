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
 * in exactly one of them, Needs you first; finished things are in neither: the handed-out ones that finished in the last
 * 24 hours are under Finished ([AssistantEntries.finished], #10922), every finished one on the board. Each entry's count
 * is its rows. A task waiting for its supervisor's review is In progress for the first hour of the wait
 * ([supervisorReviewing]), then Needs you. A task is one row whatever its agents: they are never rows of their own. In the main conversation an
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
     * [supervisorReview]: it is here because its supervisor's review is under way, and its row says so.
     */
    data class Task(val todo: Todo, val status: TriageStatus, val activity: BackgroundWork? = null, val waiting: Boolean = false,
                    val hasAgentsElsewhere: Boolean = false, val supervisorReview: Boolean = false) : RunningEntry {
        override val key get() = "task:${todo.id}"
    }

    /** A working Agent that belongs to no task. [waiting]: it works only because its background work waits (iOS `DockAgent.backgroundWaiting`). */
    data class Agent(val session: Session, val activity: BackgroundWork? = session.backgroundWork(),
                     val waiting: Boolean = activity == null && session.waitsInBackground) : RunningEntry {
        override val key get() = "agent:${session.machineId}/${session.sessionId}"
    }
}

/**
 * A task the conversation handed out that finished in the last 24 hours: a row of the Finished entry (#10922, web
 * `FinishedItem`). [fresh]: the owner has not opened it since it finished (the To-dos page's Just finished), so it is
 * drawn as new; seeing it changes only that. [at]: when it finished, epoch ms ([finishedAt]).
 */
data class FinishedEntry(val todo: Todo, val status: TriageStatus, val fresh: Boolean, val at: Long) {
    val key get() = "task:${todo.id}"
}

/**
 * [shelved]: open tasks with no progress for 48 hours and nothing running, folded under In progress and not counted.
 * [finished]: handed-out tasks that finished in the last 24 hours ([JUST_DONE_MS], the To-dos page's Just finished
 * window), seen or not, newest finished first and the same moment by id: the Finished entry (#10922). How a task ended is
 * told there, not by a card in the chat (#8237). Not open work: its count is its rows, and opening one marks it seen
 * without taking it out. Until #10922 only the unseen ones were listed, as a group under In progress that went as soon
 * as one was opened.
 */
data class AssistantEntries(
    val needsYou: List<NeedsYouEntry>,
    val running: List<RunningEntry>,
    val shelved: List<RunningEntry>,
    val finished: List<FinishedEntry> = emptyList(),
)

/** How long a task's review is its supervisor's alone (docs/assistant/unified-entry.md "Needs you and In progress"):
 *  past it the review is overdue and the task is the owner's, under Needs you. The web's `REVIEW_OVERDUE_MS`. */
const val REVIEW_OVERDUE_MS = 60 * 60 * 1000L

/**
 * The task waits for its supervisor's review, which is not the owner's to do yet: open, in `pending_review` for less than
 * [REVIEW_OVERDUE_MS] at [now] (epoch ms). False past that, and when the hub does not say since when (an older hub): the
 * review is then the owner's, as it was. An open ask or an asking Agent is the owner's whatever this says. An owner hold
 * its Agent reported that the supervisor has not read yet (`unblock` supervisor, #13191) is the supervisor's however old.
 */
fun Todo.supervisorReviewing(now: Long): Boolean {
    if (hold?.unblock == HoldUnblock.Supervisor) return true // an owner hold its supervisor reads first (#13191)
    if (isDone || supervision?.state != "pending_review") return false
    val since = parseTime(supervision?.pendingSince)?.toEpochMilli() ?: return false
    return now - since < REVIEW_OVERDUE_MS
}

/** The task waits for the user's decision or review: an open task's ask, or its supervisor's `needs_user` on an open
 *  task, or a `pending_review` that is the owner's: one waiting an hour or more at [now], or one the hub gives no start
 *  for ([supervisorReviewing]). A younger review is its supervisor's to do. A finished task never waits, whatever ask
 *  it still carries: the hub's To-dos triage files it with the finished ones (#12974). */
fun Todo.awaitsDecision(now: Long): Boolean {
    if (isDone) return false
    if (openAsk != null) return true
    return supervision?.state == "user_check" || supervision?.state == "needs_user" || (supervision?.state == "pending_review" && !supervisorReviewing(now))
}

/** Nothing of the task runs or asks (the web's `awaitsReview` statuses). */
private val RESTING = setOf(TriageStatus.FollowUp, TriageStatus.Unread, TriageStatus.NotStarted)

/** The review a decision belongs to (web `decisionRound`): the latest review's time, else the hub's, else the ask's. */
internal fun Todo.decisionRound(): Instant? =
    listOf(supervision?.reviews?.lastOrNull()?.at, decisionReview?.reviewAt, ask?.askedAt)
        .firstOrNull { !it.isNullOrBlank() }?.let(::parseTime)

/** The owner chose Don't handle for this decision's current review (#4874): the main chat no longer waits on them for it.
 *  A new review is a new round and shows it again. */
fun Todo.decisionDismissed(): Boolean {
    val review = decisionReview ?: return false
    if (review.dismissalId.isNullOrBlank()) return false
    val at = parseTime(review.reviewAt) ?: return false
    return at == decisionRound()
}

/** The web's `todoRank` (what the user must act on first), on the triage status and its display state. */
private fun Todo.entryRank(status: TriageStatus): Int {
    if (waitsForOwner()) return 0
    if (kind != "concern" && !isDone && acceptanceStands && (status in RESTING || status == TriageStatus.UsageLimit)) return 80
    val reviewRequired = supervision?.state == "pending_review" || openAsk != null || (kind != "concern" && delivery?.state == "live")
    if (reviewRequired && status in RESTING) return 20
    return when (status) {
        TriageStatus.NeedsYou -> 0
        TriageStatus.Interrupted -> 10
        TriageStatus.UsageLimit -> 15
        TriageStatus.Failed -> 30
        TriageStatus.Working -> 40
        TriageStatus.Waiting -> 45
        TriageStatus.NotStarted -> 50
        TriageStatus.Unread -> 60
        TriageStatus.FollowUp -> 70
        TriageStatus.Done -> 80
    }
}

fun assistantEntries(
    conversation: String, main: String?, todos: List<Todo>, sessions: List<Session>, interactions: List<Interaction>,
    plans: Collection<TaskPlan>, feed: List<FeedItem>, now: Long, restored: Map<String, Long> = emptyMap(),
    hierarchy: Boolean = false,
): AssistantEntries {
    val isMain = !main.isNullOrBlank() && main == conversation
    val bySession = persona.core.domain.sort.foldedSessions(sessions, hierarchy).associateBy { GroupKeys.rawKey(it.key) }
    val lookup = { key: SessionKey -> bySession[GroupKeys.rawKey(key)] }
    val asksOf = interactions.sortedWith(compareBy<Interaction> { parseTime(it.createdAt) ?: Instant.MIN }.thenBy { it.id })
        .groupBy { GroupKeys.rawKey(it.homeKey) }
    val time = { at: String? -> parseTime(at) ?: Instant.MIN }

    val asking = mutableListOf<NeedsYouEntry>()
    val deciding = mutableListOf<Pair<NeedsYouEntry.Task, Int>>()
    val open = mutableListOf<Pair<RunningEntry.Task, Int>>()
    val shelved = mutableListOf<RunningEntry.Task>()
    val finished = mutableListOf<FinishedEntry>()
    val folded = mutableSetOf<SessionKey>()
    // failed starts whose work finished in a related to-do: closed, as the To-dos page has them (#3713)
    val superseded = supersededFailedStarts(todos)
    // the main conversation watches every task (docs/assistant/unified-entry.md); another one the tasks it dispatched or supervises
    val listed = todos.filter { isMain || it.origin == conversation || it.supervision?.supervisor == conversation }
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
        val allStatus = todo.triageStatus(lookup, now)
        val status = if (hasAgentsElsewhere && allStatus == TriageStatus.NeedsYou) {
            when (val ownStatus = todo.copy(agents = own).triageStatus(lookup, now)) {
                TriageStatus.NotStarted -> TriageStatus.Working
                else -> ownStatus
            }
        } else allStatus
        val asks = own.flatMap { asksOf[GroupKeys.rawKey(it.key)].orEmpty() }
        val placed = todo.triage(lookup, now, restored[todo.id] ?: 0L, superseded = todo.id in superseded)
        val bucket = when {
            todo.countsAsOpen(placed) -> TodoBucket.Open
            placed.section == TriageSection.Shelved -> TodoBucket.Shelved
            else -> TodoBucket.Closed
        }
        // a failed start carries done_at (the hub ends it), yet it is the user's to retry while its bucket says open
        val waits = if (status == TriageStatus.Failed) bucket == TodoBucket.Open
            else !todo.isDone && (status == TriageStatus.NeedsYou || status == TriageStatus.Interrupted)
        val decision = todo.awaitsDecision(now)
        val deliveryApproval = todo.hold?.unblock == HoldUnblock.User
        // its supervisor's review is under way (not the owner's yet): in progress, whatever the To-dos page files it under
        val reviewing = !decision && !deliveryApproval && todo.supervisorReviewing(now)
        when {
            asks.isNotEmpty() -> asking += NeedsYouEntry.Task(todo, status, asks)
            waits || decision || deliveryApproval -> {
                // Only the review decision is hidden once dismissed (web `assistantEntries`, #8262): an Agent that asks still shows.
                val dismissed = status != TriageStatus.NeedsYou && decision && todo.decisionDismissed()
                if (deliveryApproval || !dismissed) deciding += NeedsYouEntry.Task(todo, status, asks) to todo.entryRank(status)
            }
            bucket == TodoBucket.Open || reviewing -> {
                val activity = todo.backgroundWork(lookup)
                val waiting = activity == null && todo.activeAgents.mapNotNull { lookup(it.key) }.any { it.waitsWithoutBackground() || it.waitsInBackground }
                open += RunningEntry.Task(todo, status, activity, waiting, hasAgentsElsewhere,
                    supervisorReview = reviewing && status in RESTING) to todo.entryRank(status)
            }
            bucket == TodoBucket.Shelved -> shelved += RunningEntry.Task(todo, status, hasAgentsElsewhere = hasAgentsElsewhere)
            else -> {
                // finished work a conversation handed out, for a day after it finished, seen or not (#10922); a failed
                // start a related to-do finished is history, not news
                val ended = todo.finishedAt(placed)
                if ((placed.section == TriageSection.JustDone || placed.section == TriageSection.Done) &&
                    status != TriageStatus.Failed && !todo.origin.isNullOrBlank() && ended > 0 && now - ended < JUST_DONE_MS) {
                    finished += FinishedEntry(todo, status, fresh = todo.showsJustFinished(placed, now), at = ended)
                }
                continue
            }
        }
        folded += active.map { it.key }
    }

    // Agents that belong to no task, the main conversation's only: asking ones wait on the user, working ones run.
    val agentsAsking = mutableListOf<NeedsYouEntry>()
    val agentsRunning = mutableListOf<RunningEntry>()
    for (session in dockAgents(conversation, main, sessions, todos, hierarchy)) {
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
        // newest finished first; the same moment by id, so every client lists them in one order
        finished = finished.sortedWith(compareByDescending<FinishedEntry> { it.at }.thenBy { it.todo.id }),
    )
}

/**
 * What a finished task did, in one line (#8237): its latest review's note, why it closed, its latest receipt's summary,
 * else its Agent's last words. Null when none of those has words.
 */
fun finishedSummary(todo: Todo, feed: List<FeedItem>, lead: Session?): String? {
    val receipt = feed.lastOrNull { it.kind == "receipt" && it.ref == todo.id && receiptState(it) != "failed" }?.field("summary")
    return listOf(todo.supervision?.latestReview?.note, todo.doneReason, receipt, lead?.lastText)
        .firstNotNullOfOrNull { it?.trim()?.takeIf(String::isNotEmpty) }
}
