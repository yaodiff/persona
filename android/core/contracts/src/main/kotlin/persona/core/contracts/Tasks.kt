package persona.core.contracts

import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.encoding.Encoder
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.intOrNull
import persona.core.contracts.generated.TodoWait

// Tasks dispatched from an assistant conversation (docs/assistant/unified-entry.md; docs/protocol/protocol.md "Tasks and the chat feed"): the plans the
// assistant hands the user, the todos that are tasks, and the items the hub inserts into a conversation (its feed).

@KeepGeneratedSerializer
@Serializable(with = PlanTaskSerializer::class)
data class PlanTask(
    val title: String = "",
    val prompt: String = "",
    val machine: String = "",
    val cwd: String = "",
    val launcher: String? = null,
    val model: String? = null,
    val permission: String? = null,
    val readonly: Boolean? = null,
    val why: String? = null,
    @SerialName("todo_id") val todoId: String? = null,
    val error: String? = null,
    /** Why the start failed; its arguments carry a launcher name or a terminal question. */
    @SerialName("error_code") val errorCode: String? = null,
    @SerialName("error_args") val errorArgs: Map<String, String>? = null,
    /** Other launchers the hub can try, in its suggested order (feature `task_retry_launcher`). */
    @SerialName("retry_with") val retryWith: List<String>? = null,
    /** The Agent preset to resolve when this planned task starts; null on an older hub. */
    val tier: String? = null,
    /** The hub-owned brief Start confirms; decoded for the card, never sent back by PlanTaskSerializer. */
    val brief: TaskBrief? = null,
) {
    /** The directory's last part: how the user names a project. */
    val project get() = compactFolderName(cwd)
}

@KeepGeneratedSerializer
@Serializable(with = TaskPlanSerializer::class)
data class TaskPlan(
    val id: String,
    val conversation: String = "",
    val tasks: List<PlanTask> = emptyList(),
    @WireEnum("open", "started", "dismissed", "unknown")
    val state: String = "unknown",
    @WireTime @SerialName("created_at") val createdAt: String? = null,
    /** The rule an open plan suggests after three Starts of its kind (#4929): the hub fills it in on every read; null otherwise. */
    val suggestion: RuleSuggestion? = null,
)

/** A message card's reply that went out: when, and what it said. */
data class SentReply(val at: String, val text: String)

/** A blank a draft leaves to fill in (a full-width-bracketed note, {{link}}): it would go out as it is. Null when it has none. */
fun draftPlaceholder(text: String): String? = Regex("【[^】\n]*】|\\{\\{[^}\n]*\\}\\}").find(text)?.value // i18n-ignore: the blank marks drafts use

/** An item the hub inserted into an assistant conversation, drawn among the transcript's lines by [at]. */
@KeepGeneratedSerializer
@Serializable(with = FeedItemSerializer::class)
data class FeedItem(
    val id: String,
    val conversation: String = "",
    @WireTime val at: String? = null,
    @WireEnum("plan", "task", "answered", "receipt", "review", "report", "routed", "today", "brief", "message", "view", "notice", "unknown")
    val kind: String = "unknown",
    val ref: String? = null,
    val body: JsonElement? = null,
) {
    /** A plan card carries the plan in its body: drawn even when the `task_plan` event for it was missed or raced. */
    val plan: TaskPlan? get() = ((body as? JsonObject)?.get("plan"))?.let { runCatching { Json.decodeFromJsonElement<TaskPlan>(it) }.getOrNull() }

    /** A string field of the body (receipt's title / summary / pr, routed's text, message's chat / draft…). */
    fun field(name: String): String? = ((body as? JsonObject)?.get(name) as? JsonPrimitive)?.takeIf { it.isString }?.contentOrNull
    fun number(name: String): Int? = ((body as? JsonObject)?.get(name) as? JsonPrimitive)?.intOrNull

    /**
     * `message`: the reply went out — from this card or from the inbox, on any device (the hub records it on the
     * card, #1519): when, and the recorded text if available. Null while not sent.
     */
    val sent: SentReply? get() = field("sent_at")?.let { SentReply(it, field("sent").orEmpty()) }

    /**
     * `message`: the thing the card is about was dealt with elsewhere and nothing went out from this card (#4797):
     * when the hub saw it filed. Never set on a sent card; gone again once the item is back in the inbox.
     */
    val filedAt: String? get() = if (sent != null) null else field("filed_at")?.takeIf(String::isNotEmpty)

    /** `message`: this particular draft was dismissed without sending or closing its task. */
    val dismissedAt: String? get() = field("dismissed_at")?.takeIf(String::isNotEmpty)

    /** `message`: the card still offers its draft to send. */
    val canSend: Boolean get() = sent == null && filedAt == null && dismissedAt == null

    /**
     * `answered`: every question of the card with the user's answer in full, newlines kept (#1231). A record from
     * before it has only its line (question, answer), which stands in; empty when there is nothing to show.
     */
    fun answeredItems(): List<AnsweredItem> {
        fun JsonElement?.text() = (this as? JsonPrimitive)?.takeIf { it.isString }?.contentOrNull.orEmpty()
        val listed = ((body as? JsonObject)?.get("items") as? JsonArray).orEmpty().mapNotNull { e ->
            val o = e as? JsonObject ?: return@mapNotNull null
            AnsweredItem(o["question"].text(), o["answer"].text()).takeIf { it.question.isNotEmpty() || it.answer.isNotEmpty() }
        }
        if (listed.isNotEmpty()) return listed
        val line = AnsweredItem(field("question").orEmpty(), field("answer").orEmpty())
        return if (line.question.isEmpty() && line.answer.isEmpty()) emptyList() else listOf(line)
    }
}

/** One question of an answered card and what the user answered ([FeedItem.answeredItems]). */
data class AnsweredItem(val question: String, val answer: String)

@KeepGeneratedSerializer
@Serializable(with = TodoSpaceSerializer::class)
data class TodoSpace(
    @SerialName("machine_id") val machineId: String = "",
    val space: String = "",
    @WireTime @SerialName("linked_at") val linkedAt: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = TodoAgentSerializer::class)
data class TodoAgent(
    @SerialName("machine_id") val machineId: String = "",
    @SerialName("session_id") val sessionId: String = "",
    @WireTime @SerialName("linked_at") val linkedAt: String? = null,
    val title: String? = null,
    val cwd: String? = null,
    val agent: String? = null,
    @SerialName("start_error") val startError: String? = null,
    @WireTime @SerialName("released_at") val releasedAt: String? = null,
    val source: String? = null,
    val via: String? = null,
    @WireTime @SerialName("dismissed_at") val dismissedAt: String? = null,
) {
    val key: SessionKey get() = SessionKey(machineId, sessionId)
    val isCurrent: Boolean get() = releasedAt == null
}

/** One to-do in a task's takeover history; links point to the previous and next cards (#5732). */
@KeepGeneratedSerializer
@Serializable(with = TodoLineageStepSerializer::class)
data class TodoLineageStep(
    @SerialName("todo_id") val todoId: String,
    val title: String = "",
    val agent: String? = null,
    val machine: String? = null,
    @SerialName("session_id") val sessionId: String? = null,
    @WireTime val started: String? = null,
    @WireTime val ended: String? = null,
    val why: String? = null,
    val continues: String? = null,
    @SerialName("superseded_by") val supersededBy: String? = null,
    val current: Boolean = false,
)

/** A todo (docs/assistant/todos.md); a task is one whose [origin] is an assistant conversation. */
@KeepGeneratedSerializer
@Serializable(with = TodoSerializer::class)
data class Todo(
    val id: String,
    val title: String = "",
    val notes: String = "",
    @WireTime @SerialName("done_at") val doneAt: String? = null,
    @WireTime @SerialName("created_at") val createdAt: String? = null,
    @WireTime @SerialName("updated_at") val updatedAt: String? = null,
    val origin: String? = null,
    @SerialName("plan_id") val planId: String? = null,
    @SerialName("done_by") val doneBy: String? = null,
    @SerialName("done_reason") val doneReason: String? = null,
    @WireTime @SerialName("reopened_at") val reopenedAt: String? = null,
    /** When the owner last looked at it in Just finished: Got it, or opening it (#5707); one seen since it got there is Done. */
    @WireTime @SerialName("seen_at") val seenAt: String? = null,
    /** Why the task's agent did not start (#1077): the hub ended the todo with it; "Retry" clears it (#1396). */
    @SerialName("start_error") val startError: String? = null,
    /** Opened from the main conversation while "Notify main-chat tasks only in the main chat" was on (#1731): not in the to-dos badge. */
    val quiet: Boolean = false,
    val agents: List<TodoAgent> = emptyList(),
    val spaces: List<TodoSpace>? = null,
    /** The main conversation supervises it (#3124): what done means and where its review stands; null from an older hub. */
    val supervision: Supervision? = null,
    /** How far its finished work went out, and short of live where it is held (#3306); null from an older hub or before judged. */
    val delivery: TodoDelivery? = null,
    /** The hub's latest automatic completion check of its work while still current (#3180); null before one, after a
     *  reopen or a new turn, and from an older hub. */
    val check: TodoCheck? = null,
    /** When it is due (#1683): a moment, or a day ("yyyy-MM-dd", due by its end in the user's zone); at most one. */
    @WireTime @SerialName("due_at") val dueAt: String? = null,
    @SerialName("due_date") val dueDate: String? = null,
    /** Where its work stands (#2776, docs/assistant/todos.md "Delivery"): plan, develop, review, merge, deploy or verify; the hub
     *  merges it from `assistant.todo_stages`. Null when none, and a name this build doesn't know draws nothing. */
    val stage: String? = null,
    /** When the phone is pushed about it, and when that was done. */
    @WireTime @SerialName("remind_at") val remindAt: String? = null,
    @WireTime @SerialName("reminded_at") val remindedAt: String? = null,
    /** "high" or "low"; null or "" is normal. */
    val priority: String? = null,
    /** The owner's current acceptance round; always submit its captured askedAt. */
    val ask: TodoAsk? = null,
    /** An open task whose latest review waits on the owner: that review's time, and the owner's Don't handle for exactly
     *  that review (web #4874, docs/protocol/protocol.md `GET /api/v1/todos`); null otherwise and from an older hub. */
    @SerialName("decision_review") val decisionReview: DecisionReview? = null,
    @SerialName("pr_url") val prUrl: String? = null,
    @SerialName("pr_status") val prStatus: TodoPRStatus? = null,
    @SerialName("other_prs") val otherPrs: List<TodoPRStatus>? = null,
    /** "code" or "general" (#3380): general work draws no stage bar, its status already says how it goes; null from an
     *  older hub, which keeps the bar for any stage. */
    @SerialName("stage_kind") val stageKind: String? = null,
    /** Closed with nothing left to deploy (#11590): the stage bar ends with Done after [stage]. False while open, for work
     *  that went out or waits to, and from an older hub. */
    @SerialName("stage_done") val stageDone: Boolean = false,
    @WireTime @SerialName("cancelled_at") val cancelledAt: String? = null,
    @SerialName("cancelled_reason") val cancelledReason: String? = null,
    /** Takeover history, oldest first; null from an older hub or for a task with no lineage. */
    val lineage: List<TodoLineageStep>? = null,
    /** The manager it belongs to, as the merged list stamps it (docs/assistant/managers.md "Ownership"); null: the default manager's. */
    val manager: String? = null,
    /** Nobody is working on it (#3618): every agent of this open to-do is gone and nothing brings one back; null otherwise
     *  and from an older hub. */
    val orphaned: TodoOrphaned? = null,
    /** The hub-computed reason this task waits (#16168): render it directly; null when it is not waiting or from an older hub. */
    @SerialName("wait") val wait: TodoWait? = null,
    /** Empty for an ordinary to-do or task; "concern" for one of the owner's concerns. Unknown values stay ordinary. */
    val kind: String = "",
) {
    val isDone: Boolean get() = doneAt != null
    /** Closed by cancellation, including the legacy supervision-only representation; never counted as completed (#5732). */
    val isCancelled: Boolean get() = cancelledAt != null || doneBy == "cancelled" || doneBy == "review" && doneAt != null && supervision?.state == "cancelled"
    val cancellationReason: String? get() = if (isCancelled) cancelledReason ?: doneReason else null
    val activeAgents get() = agents.filter { it.releasedAt == null }
    val currentAgents: List<TodoAgent> get() = activeAgents
    val releasedAgents: List<TodoAgent> get() = agents.filter { it.releasedAt != null }
    val created: Instant? get() = parseTime(createdAt)
    val updated: Instant? get() = parseTime(updatedAt)
    val done: Instant? get() = parseTime(doneAt)
    val remind: Instant? get() = parseTime(remindAt)

    /** The day of [dueDate], or null without a readable one. */
    val dueDay: LocalDate? get() = dueDate?.let { runCatching { LocalDate.parse(it) }.getOrNull() }

    /** The moment it is due by in [zone]: its [dueAt], or the end of its due day; null without either (#1683). */
    fun dueBy(zone: ZoneId = ZoneId.systemDefault()): Instant? =
        parseTime(dueAt) ?: dueDay?.plusDays(1)?.atStartOfDay(zone)?.toInstant()?.minusSeconds(1)

    /** Open, and its due moment has passed. */
    fun isOverdue(now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): Boolean =
        !isDone && (dueBy(zone)?.isBefore(now) ?: false)

    /** Its due date as a write sets it: its moment, else its day; null without either. */
    val due: TodoDue? get() = parseTime(dueAt)?.let { TodoDue.At(it) } ?: dueDay?.let { TodoDue.Day(it) }

    /** The to-do due at [due] instead, as the hub keeps it: a moment or a day, one replacing the other (#2045). */
    fun withDue(due: TodoDue): Todo = when (due) {
        is TodoDue.At -> copy(dueAt = due.at.toString(), dueDate = null)
        is TodoDue.Day -> copy(dueAt = null, dueDate = due.day.toString())
    }
}

/** A to-do's due date as `PATCH /api/v1/todos/{id}` sets it: a moment (`due_at`) or a day in the user's time zone (`due_date`). */
sealed interface TodoDue {
    data class At(val at: Instant) : TodoDue
    data class Day(val day: LocalDate) : TodoDue
}

/**
 * A to-do's due date, reminder and priority as the detail page sets them together (#4636, iOS `TodoPlanChange`,
 * docs/assistant/todos.md PATCH): [due] null clears it, [remindAt] null clears it, [priority] "" normal, "high" or "low".
 */
data class TodoPlan(val due: TodoDue?, val remindAt: Instant?, val priority: String)

/**
 * Why an open to-do has nobody working on it (docs/assistant/todos.md "Nobody is working on this"): [reason] error (closed
 * after a model error), offline (its computer is not connected) or closed, kept as sent; [since] when the hub first saw it.
 */
@Serializable
data class TodoOrphaned(val reason: String? = null, @WireTime val since: String? = null)

/**
 * What a to-do waits on from the owner (docs/assistant/todos.md "Waiting for the owner"). A decision (#3804, "Decisions the
 * owner can answer") also carries what is to be decided ([question]), why only the owner can ([whyOwner]), its
 * [options] with the recommended one first, where the task came from and stands in one line ([background]), what waiting
 * costs, whether the recommendation is taken at [deadline] ([default] "taken") or never ("never"), the fixed answers the
 * hub takes ([responses]) and the one already given while it still waits ([response]). A decision that can wait is held
 * for the next briefing, with nothing pushed for it, while [forBriefing] (#4930, "Decisions that can wait"); one the owner
 * gave the same go-ahead for three times carries the rule it suggests ([suggestion], #4929). All absent from an older hub.
 */
/** A task's review decision as the hub tracks it (#4874): [reviewAt] is the latest review's `at`, [dismissalId] set while
 *  the owner chose Don't handle for that review; a new review restores it. */
@Serializable
data class DecisionReview(
    @WireTime @SerialName("review_at") val reviewAt: String? = null,
    @SerialName("dismissal_id") val dismissalId: String? = null,
)

@Serializable
data class TodoAsk(
    val outcome: String? = null,
    val verdict: String = "",
    val recommend: String? = null,
    @WireTime @SerialName("asked_at") val askedAt: String = "",
    val question: String? = null,
    @SerialName("why_owner") val whyOwner: String? = null,
    val options: List<AskOption>? = null,
    @SerialName("cost_of_waiting") val costOfWaiting: String? = null,
    val background: String? = null,
    @WireTime val deadline: String? = null,
    val default: String? = null,
    val responses: List<String>? = null,
    val response: String? = null,
    @SerialName("for_briefing") val forBriefing: Boolean = false,
    val suggestion: RuleSuggestion? = null,
    val summary: String? = null,
    val link: String? = null,
)

/** One option of a decision: its [label], which is the answer, and what choosing it leads to. */
@Serializable
data class AskOption(val label: String = "", @SerialName("leads_to") val leadsTo: String? = null)

/** Fresh observed facts for one PR of a task (#7935); older hubs omit them. */
@KeepGeneratedSerializer
@Serializable(with = TodoPRStatusSerializer::class)
data class TodoPRStatus(
    val url: String = "",
    val state: String = "",
    val draft: Boolean = false,
    val review: String = "",
    val checks: String = "",
    val conflicts: Boolean = false,
    @WireTime @SerialName("checked_at") val checkedAt: String? = null,
)

/**
 * How far a task's finished work went out (#3306, docs/assistant/todos.md"Delivery"): [state] live, pr, merged, off or none (kept as
 * the hub sent it), [gate] what holds it, [unblock] who can unhold it (user, agent, other).
 */
@KeepGeneratedSerializer
@Serializable(with = TodoDeliverySerializer::class)
data class TodoDelivery(
    val state: String = "",
    val gate: String? = null,
    val unblock: String? = null,
    @WireTime val at: String? = null,
)

/**
 * The hub's latest automatic completion check of a task (#3180, docs/assistant/todos.md "Automatic completion"): [verdict] done,
 * not_done, waiting, needs_user, undelivered or unsure (a later one kept as sent), [reason] the judge's line in the user's
 * language, [at] when it was judged and [workSince] the start of the work it judged.
 */
@KeepGeneratedSerializer
@Serializable(with = TodoCheckSerializer::class)
data class TodoCheck(
    val verdict: String = "",
    val reason: String? = null,
    @WireTime val at: String? = null,
    @WireTime @SerialName("work_since") val workSince: String? = null,
)

/** One criterion as a review found it (#3124). */
@KeepGeneratedSerializer
@Serializable(with = ReviewCheckSerializer::class)
data class ReviewCheck(
    val criterion: String = "",
    val met: Boolean = false,
    val evidence: String? = null,
)

/** One review of a supervised task: accepted, rework (with its round), needs_user or cancelled. */
@KeepGeneratedSerializer
@Serializable(with = TaskReviewSerializer::class)
data class TaskReview(
    @WireTime val at: String? = null,
    val verdict: String = "",
    val round: Int = 0,
    val checks: List<ReviewCheck> = emptyList(),
    val note: String? = null,
)

/**
 * A supervised task's ledger (#3124, docs/assistant/unified-entry.md "Supervision"): its supervisor (the conversation that handed it
 * out), what done means, how long it should take, and its review: [state] "" while it goes on, then pending_review,
 * rework ([round] counts them), needs_user, accepted or cancelled.
 */
@KeepGeneratedSerializer
@Serializable(with = SupervisionSerializer::class)
data class Supervision(
    val supervisor: String = "",
    @SerialName("done_when") val doneWhen: List<String> = emptyList(),
    @SerialName("expect_minutes") val expectMinutes: Int = 0,
    val state: String = "",
    val round: Int = 0,
    val reviews: List<TaskReview> = emptyList(),
    @WireTime @SerialName("updated_at") val updatedAt: String? = null,
    /** When the task entered `pending_review`; null in every other state, and from an older hub. */
    @WireTime @SerialName("pending_since") val pendingSince: String? = null,
    /** The agent's explicit report; missing counts never imply a percentage. */
    val progress: TaskProgress? = null,
)

/**
 * A supervised task's agent's latest report: [text] is its own words in one line, [line] the hub's one-sentence paraphrase
 * in the user's language (#3181; "" until written, with no model, or from an older hub), [detail] the whole report as
 * written, Markdown ("" when it is [text]).
 */
@Serializable
data class TaskProgress(
    val kind: String = "",
    val step: Int = 0,
    val steps: Int = 0,
    val text: String = "",
    val line: String = "",
    val detail: String = "",
    @WireTime val at: String? = null,
) {
    /** What the Progress row says: the paraphrase when there is one, else the agent's words. */
    val rowText: String get() = line.takeIf(String::isNotBlank) ?: text

    /** The report as the agent wrote it, for the task's details. */
    val reportText: String get() = detail.takeIf(String::isNotBlank) ?: text
}

/**
 * Todo states in order of rank (umbrella.final.md / docs/assistant/todos.md "State calculation rules"):
 * Done -> Needs you -> To review -> In progress -> To follow up -> Not started
 */
enum class TodoState(val title: CoreText, val rank: Int) {
    Done(CoreText.of(CoreText.Key.TodoDone), 0),
    NeedsYou(CoreText.of(CoreText.Key.TaskNeedsYou), 1),
    Unread(CoreText.of(CoreText.Key.TodoUnread), 2),
    Working(CoreText.of(CoreText.Key.TodoWorking), 3),
    FollowUp(CoreText.of(CoreText.Key.TodoFollowUp), 4),
    NotStarted(CoreText.of(CoreText.Key.TodoNotStarted), 5);
}

/**
 * Calculates a todo's state from its current active agents and their session states.
 */
fun Todo.state(sessionLookup: (SessionKey) -> Session?): TodoState {
    if (isDone) return TodoState.Done
    val cur = currentAgents
    if (cur.isEmpty()) return TodoState.NotStarted
    val sessions = cur.map { a -> sessionLookup(a.key) }
    val states = sessions.map { it?.agentState ?: AgentState.Idle }
    val isStarting = sessions.any { it?.starting == true }
    return when {
        states.any { it == AgentState.NeedsYou } -> TodoState.NeedsYou
        states.any { it == AgentState.Unread } -> TodoState.Unread
        isStarting || states.any { it == AgentState.Working } -> TodoState.Working
        else -> TodoState.FollowUp
    }
}

/**
 * Sorts todos: uncompleted first — what needs the user (needs you, unread) on top, then by due date, earliest (an
 * overdue one) first, those without one after (#1683), then in TodoState rank order, newest created first, by id;
 * completed todos at the bottom (newest done/created first).
 */
fun List<Todo>.ordered(sessionLookup: (SessionKey) -> Session?, zone: ZoneId = ZoneId.systemDefault()): List<Todo> {
    val uncompleted = filter { !it.isDone }
    val completed = filter { it.isDone }
    val urgent = setOf(TodoState.NeedsYou, TodoState.Unread)
    val sortedUncompleted = uncompleted.sortedWith(
        compareBy<Todo> { it.state(sessionLookup).let { s -> if (s in urgent) s.rank else Int.MAX_VALUE } }
            .thenBy { it.dueBy(zone) ?: Instant.MAX }
            .thenBy { it.state(sessionLookup).rank }
            .thenByDescending { it.created ?: Instant.EPOCH }
            .thenBy { it.id }
    )
    val sortedCompleted = completed.sortedWith(
        compareByDescending<Todo> { it.done ?: it.created ?: Instant.EPOCH }
            .thenBy { it.id }
    )
    return sortedUncompleted + sortedCompleted
}

/** The open todo [space] on [machineId] is on (a project is on one at a time), as iOS TodosLogic.findTodoForSpace. */
fun List<Todo>.forSpace(machineId: String, space: String): Todo? {
    if (machineId.isBlank() || space.isBlank()) return null
    return firstOrNull { todo -> !todo.isDone && todo.spaces.orEmpty().any { it.machineId == machineId && it.space == space } }
}

/**
 * Formats the todo context tag as in protocol.md and docs/assistant/todos.md, as iOS `TodosLogic.formatTodoContext` does: the
 * title escaped as an attribute, the notes through [contextBody] so they cannot close the tag.
 */
fun todoContextXml(title: String, notes: String): String {
    val t = title.replace("&", "&amp;").replace("\"", "&quot;").replace("<", "&lt;").replace(">", "&gt;")
    return """<persona-context kind="context" title="待办 · $t" url="">${contextBody(notes)}</persona-context>""" // i18n-ignore: the tag docs/assistant/todos.md specifies for the agent
}

/**
 * The prompt to send when handing off a todo to an agent.
 */
fun todoHandOffPrompt(title: String, notes: String, instruction: String): String {
    val contextTag = todoContextXml(title, notes)
    val ins = instruction.trim()
    return if (ins.isEmpty()) contextTag else "$contextTag\n\n$ins"
}

@KeepGeneratedSerializer
@Serializable(with = TodosResponseSerializer::class)
data class TodosResponse(val todos: List<Todo> = emptyList())

/**
 * Streaming wire envelope for `GET /api/v1/todos` (complete or paginated with `?limit`/`?cursor`).
 * Uses the generated serializer so the whole payload is never materialized into a `JsonObject` DOM;
 * each [Todo] element still decodes through [TodoSerializer].
 */
@Serializable
data class TodosPageResponse(
    val todos: List<Todo> = emptyList(),
    @SerialName("next_cursor") val nextCursor: String? = null,
    @SerialName("has_more") val hasMore: Boolean? = null,
)

/** A lifecycle card's stable identity; incremental feeds upsert these without replacing the held set. */
@Serializable
data class AssistantTaskCard(
    val id: String,
    @SerialName("todo_id") val todoId: String? = null,
    @SerialName("plan_id") val planId: String? = null,
    val index: Int? = null,
    val at: String? = null,
    @SerialName("updated_at") val updatedAt: String? = null,
    val state: String = "",
    @SerialName("receipt_id") val receiptId: String? = null,
    @SerialName("report_id") val reportId: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = FeedResponseSerializer::class)
data class FeedResponse(
    val items: List<FeedItem> = emptyList(),
    val plans: Map<String, TaskPlan> = emptyMap(),
    /**
     * The conversation's live cards by id, each a whole AssistantView (docs/assistant/assistant-views.md "Live cards"): the full set
     * on every read, kept raw so one card this app cannot read does not drop the others ([AssistantViewWire]).
     */
    val views: Map<String, JsonElement> = emptyMap(),
    /** The persisted card answers by card key (a ViewAction each), the full set on every read. */
    @SerialName("view_actions") val viewActions: Map<String, JsonElement> = emptyMap(),
    /** The keys of the reply blocks the hub registered from a trusted transcript (`<item id>/<n>`). */
    @SerialName("view_blocks") val viewBlocks: List<String> = emptyList(),
    /** Null on older hubs or a failed projection read; an empty incremental page removes nothing. */
    @SerialName("task_cards") val taskCards: List<AssistantTaskCard>? = null,
    /** Stable pagination: null on older hubs, empty at EOF, otherwise an opaque continuation cursor. */
    @SerialName("next_cursor") val nextCursor: String? = null,
) {
    companion object {
        /**
         * Generated serializer for streaming `InputStream` decoding without materializing a whole-response
         * `JsonObject` DOM; nested [FeedItem] and [TaskPlan] entries still decode through their [WireSerializer]s.
         */
        fun streamingSerializer(): kotlinx.serialization.KSerializer<FeedResponse> = generatedSerializer()
    }
}

/** The todos changed: pull them again. */
@KeepGeneratedSerializer
@Serializable(with = TodosEventSerializer::class)
class TodosEvent : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = AssistantFeedEventSerializer::class)
data class AssistantFeedEvent(val conversation: String = "", val item: FeedItem, val manager: String? = null) : ServerEvent

@KeepGeneratedSerializer
@Serializable(with = TaskPlanEventSerializer::class)
data class TaskPlanEvent(val plan: TaskPlan) : ServerEvent

/** A live card was created or replaced (`{conversation, view: {id, version, view, updated_at}}`). */
@KeepGeneratedSerializer
@Serializable(with = AssistantViewEventSerializer::class)
data class AssistantViewEvent(val conversation: String = "", val view: JsonElement) : ServerEvent

/** A card's persisted answer changed (`{conversation, action: ViewAction}`); its revision orders the changes. */
@KeepGeneratedSerializer
@Serializable(with = AssistantViewActionEventSerializer::class)
data class AssistantViewActionEvent(val conversation: String = "", val action: JsonElement) : ServerEvent

/** The hub registered a reply block from a trusted transcript: its key may now take answers. */
@KeepGeneratedSerializer
@Serializable(with = AssistantViewBlockEventSerializer::class)
data class AssistantViewBlockEvent(val conversation: String = "", val key: String = "") : ServerEvent

object PlanTaskSerializer : WireSerializer<PlanTask>(PlanTask.generatedSerializer()) {
    override fun serialize(encoder: Encoder, value: PlanTask) = super.serialize(encoder, value.copy(brief = null))
}
object TaskPlanSerializer : WireSerializer<TaskPlan>(TaskPlan.generatedSerializer())
object FeedItemSerializer : WireSerializer<FeedItem>(FeedItem.generatedSerializer())
object TodoSpaceSerializer : WireSerializer<TodoSpace>(TodoSpace.generatedSerializer())
object TodoAgentSerializer : WireSerializer<TodoAgent>(TodoAgent.generatedSerializer())
object TodoLineageStepSerializer : WireSerializer<TodoLineageStep>(TodoLineageStep.generatedSerializer())
object TodoPRStatusSerializer : WireSerializer<TodoPRStatus>(TodoPRStatus.generatedSerializer())
object TodoSerializer : WireSerializer<Todo>(Todo.generatedSerializer())
object ReviewCheckSerializer : WireSerializer<ReviewCheck>(ReviewCheck.generatedSerializer())
object TaskReviewSerializer : WireSerializer<TaskReview>(TaskReview.generatedSerializer())
object SupervisionSerializer : WireSerializer<Supervision>(Supervision.generatedSerializer())
object TodoDeliverySerializer : WireSerializer<TodoDelivery>(TodoDelivery.generatedSerializer())
object TodoCheckSerializer : WireSerializer<TodoCheck>(TodoCheck.generatedSerializer())
object TodosResponseSerializer : WireSerializer<TodosResponse>(TodosResponse.generatedSerializer())
object FeedResponseSerializer : WireSerializer<FeedResponse>(FeedResponse.generatedSerializer())
object TodosEventSerializer : WireSerializer<TodosEvent>(TodosEvent.generatedSerializer())
object AssistantFeedEventSerializer : WireSerializer<AssistantFeedEvent>(AssistantFeedEvent.generatedSerializer())
object TaskPlanEventSerializer : WireSerializer<TaskPlanEvent>(TaskPlanEvent.generatedSerializer())
object AssistantViewEventSerializer : WireSerializer<AssistantViewEvent>(AssistantViewEvent.generatedSerializer())
object AssistantViewActionEventSerializer : WireSerializer<AssistantViewActionEvent>(AssistantViewActionEvent.generatedSerializer())
object AssistantViewBlockEventSerializer : WireSerializer<AssistantViewBlockEvent>(AssistantViewBlockEvent.generatedSerializer())
