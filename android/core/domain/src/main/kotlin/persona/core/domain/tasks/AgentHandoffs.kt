package persona.core.domain.tasks

import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent

// One card per Agent a dispatch started or a task took over (#5110, the web's taskHandoffs in views/assistant/Handoff.tsx).
// The first Agents of a task, linked within two minutes of each other, share that hand-off; one linked later has its
// own card where that happened. While a task has no Agent yet, one card stands for the task.

/** How the Agent came to the task: started for it, already running, or closed and resumed by the hand-off. */
enum class HandoffWake { Created, Active, Resumed }

/** What one card stands for. Flat, so a list of them compares field by field. */
data class AgentHandoff(
    /** Unique among the cards of one feed line. */
    val key: String,
    /** The task's title: the card's until its Agent's session has one. */
    val title: String,
    val wake: HandoffWake,
    /** The Agent's computer and session; null while the task has no Agent yet. */
    val machineId: String? = null,
    val sessionId: String? = null,
    /** What the to-do calls its Agent, before the session list has it. */
    val agentTitle: String? = null,
    /** The to-do the task is, once there is one. Absent when the task was removed. */
    val todoId: String? = null,
    /** Why the task did not start (no Agent). */
    val failure: String? = null,
    /** The conversation's failed receipt says why: the card only says it did not start. */
    val failureBelow: Boolean = false,
    /** The task has no Agent and never will: closed before one started, or removed since. */
    val ended: Boolean = false,
)

/** A plan task, for its title and error before the to-do arrives (or after it was removed). */
data class PlannedHandoff(
    val key: String,
    val title: String,
    val error: String? = null,
    val todoId: String? = null,
)

/** How long after a task's first Agent another counts as handed off with it. */
const val SAME_HANDOFF_MS = 120_000L

/** A `task` feed item's wake; anything else (none, an older hub) is New. */
fun feedWake(wake: String?): HandoffWake = when (wake) {
    "active" -> HandoffWake.Active
    "resumed" -> HandoffWake.Resumed
    else -> HandoffWake.Created
}

/**
 * Why a task's agent did not start: the to-do's own word, else — from a hub before it, which left the to-do open —
 * its plan's, while it has no agent. Null while it may still start.
 */
fun startFailure(todo: Todo, plans: Map<String, TaskPlan>): String? {
    todo.startError?.takeIf { it.isNotBlank() }?.let { return it }
    if (todo.agents.isNotEmpty() || todo.planId.isNullOrBlank()) return null
    return plans[todo.planId]?.tasks?.firstOrNull { it.todoId == todo.id }?.error?.takeIf { it.isNotBlank() }
}

/** The hub's word on the to-do, its plan's, or the Agent's own row. */
fun whyNotStarted(todo: Todo, plans: Map<String, TaskPlan>): String? =
    startFailure(todo, plans) ?: todo.agents.firstOrNull { !it.startError.isNullOrBlank() }?.startError

/**
 * A task's cards: one per Agent the hand-off went to, or one for the task while it has none.
 * [loaded]: the to-do list is in, so a to-do it lacks is gone rather than on its way.
 */
fun taskHandoffs(
    todo: Todo?,
    planned: PlannedHandoff?,
    wake: HandoffWake,
    plans: Map<String, TaskPlan>,
    loaded: Boolean = true,
): List<AgentHandoff> {
    val started = todo?.agents.orEmpty().filter { it.startError.isNullOrBlank() && it.sessionId.isNotBlank() }
    val shown = agentsOfThisHandoff(started)
    if (todo != null && shown.isNotEmpty()) {
        return shown.map { agent ->
            AgentHandoff(
                key = "${agent.machineId}/${agent.sessionId}",
                title = todo.title,
                wake = wake,
                machineId = agent.machineId,
                sessionId = agent.sessionId,
                agentTitle = agent.title,
                todoId = todo.id,
            )
        }
    }
    val failure = (todo?.let { whyNotStarted(it, plans) } ?: planned?.error)?.trim()?.takeIf { it.isNotEmpty() }
    val ended = failure == null && if (todo != null) todo.doneAt != null else planned?.todoId != null && loaded
    return listOf(
        AgentHandoff(
            key = todo?.let { "todo:${it.id}" } ?: planned?.key ?: "task",
            title = todo?.title?.takeIf { it.isNotEmpty() } ?: planned?.title.orEmpty(),
            wake = wake,
            todoId = todo?.id,
            failure = failure,
            ended = ended,
        ),
    )
}

/** The hand-off's own Agents: those linked within [SAME_HANDOFF_MS] of the first. */
internal fun agentsOfThisHandoff(started: List<TodoAgent>): List<TodoAgent> {
    if (started.isEmpty()) return emptyList()
    val at = { agent: TodoAgent -> AssistantLines.time(agent.linkedAt)?.toEpochMilli() ?: 0L }
    val first = started.minOf(at)
    return started.filter { at(it) - first <= SAME_HANDOFF_MS }
}

/** cindy's short id, only for a tooltip: the Assistant's chat keeps ids out of sight. */
fun shortSessionId(id: String): String = if (id.length <= 13) id else "${id.take(8)}…${id.takeLast(4)}"
