package persona.core.domain.tasks

import persona.core.contracts.AssistantTaskCard
import persona.core.contracts.FeedItem
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.domain.lines.ChatLine

/** One task's plan, current execution and retained receipts occupy the original planning slot. */
data class MergedTaskCard(
    val identity: AssistantTaskCard,
    val plan: TaskPlan?,
    val todo: Todo?,
    val records: List<FeedItem>,
) {
    val receipt get() = latest("receipt")
    val report get() = latest("report")
    private fun latest(kind: String): FeedItem? {
        val started = AssistantLines.time(plan?.createdAt) ?: AssistantLines.time(identity.at)
        return records.lastOrNull { record -> record.kind == kind &&
            (started == null || AssistantLines.time(record.at)?.isBefore(started) != true) }
    }
}

/**
 * One task a reply dispatched (#3700, #5110): its to-do (null when the hub could not start it), the title to show until
 * the to-do arrives, the plan task's [error], and a standalone task item's [wake] (`active` / `resumed`; null is New).
 * [key] keeps the card stable across title changes.
 */
data class DispatchedTask(
    val todoId: String?,
    val title: String,
    val error: String? = null,
    val wake: String? = null,
    val key: String? = null,
)

/** The hand-off cards under a reply that dispatched work: the tasks, in plan order. Each card opens its Agent. */
data class DispatchHint(val tasks: List<DispatchedTask>) : AssistantExtra {
    val count: Int get() = tasks.size
}

object AssistantTaskCards {
    private val lifecycle = setOf("plan", "task", "receipt", "review", "report")

    /**
     * Explicit hub housekeeping, drafts and assistant-to-Agent instructions live behind the trays. With [inline] (Glass,
     * #4339 C13) the conversation draws the hub's records as iOS's `FeedLine` does: receipts, reviews, reports, Persona's
     * hand-offs to tasks, message cards and the hub's notice lines are lines of it, and only plans and task cards stay
     * behind (the dispatched line, [DispatchHint], and Needs you stand for them). That is the record's place
     * ([backgroundFeed]); [lines] still draws a plan the user has not started ([drawnPlans]).
     */
    fun isBackgroundEvent(item: FeedItem, inline: Boolean = false): Boolean = if (inline) item.kind in inlineHidden
        else (item.kind in lifecycle && !interruptShown(item) && !isBackgroundSupervisorReview(item)) ||
            item.kind in setOf("notice", "message") || (item.kind == "routed" && item.field("from") == "assistant")

    private val inlineHidden = setOf("plan", "task")

    /**
     * A review the background supervisor wrote (#5084, docs/assistant/unified-entry.md "Background supervisor"): the hub
     * marks it `by: "background"`. It took no turn of the conversation that handed the task out, so nothing in the
     * transcript says the task was accepted or sent back: the entry stays in the conversation as one line that opens the
     * task. Every other review (the conversation's own, a leader's, a sampled acceptance, any from a hub without the
     * mark) stays behind the trays as before.
     */
    fun isBackgroundSupervisorReview(item: FeedItem): Boolean = item.kind == "review" && item.field("by") == REVIEW_BY_BACKGROUND

    private const val REVIEW_BY_BACKGROUND = "background"

    /** An interrupted receipt the hub worked out stays in the chat as its record (#3834): a batch's card, a handled line
     *  or a pending one; a batch's other receipts are drawn inside its head. */
    private fun interruptShown(item: FeedItem): Boolean = InterruptLook.of(item).let { it != null && it != InterruptLook.Hidden }

    fun backgroundFeed(feed: List<FeedItem>, inline: Boolean = false): List<FeedItem> = feed.filter { isBackgroundEvent(it, inline) }

    /**
     * The feed items of the plans the conversation draws itself with [inline] (Glass, #4339 C13, iOS `FeedLine`'s `PlanCard`): a
     * plan nobody started yet, whatever came of it, in the place of its item. An open one is the card with Start, Edit and Don't
     * start; a dismissed one reads "Not started · N tasks". A started plan is the dispatched line ([DispatchHint]) instead, as
     * iOS's `DispatchedTasks.entries` makes it; a plan the app has no body for leaves nothing to draw.
     */
    fun drawnPlans(feed: List<FeedItem>, plans: Map<String, TaskPlan>, dispatched: Set<String>): Set<String> =
        feed.filter { item ->
            item.kind == "plan" && (item.ref?.let(plans::get) ?: item.plan)?.let { it.id !in dispatched } == true
        }.mapTo(HashSet()) { it.id }

    /** Pure fallback for older hubs, also joining live events to incremental server projection upserts. */
    fun merge(feed: List<FeedItem>, plans: Map<String, TaskPlan>, todos: List<Todo>, projected: List<AssistantTaskCard> = emptyList()): List<MergedTaskCard> {
        val planRows = feed.filter { it.kind == "plan" }.mapNotNull { item ->
            (item.ref?.let(plans::get) ?: item.plan)?.let { item to it }
        }
        val identities = linkedMapOf<String, AssistantTaskCard>()
        planRows.forEach { (item, plan) -> plan.tasks.forEachIndexed { index, task ->
            val todo = todos.firstOrNull { it.id == task.todoId }
            val card = AssistantTaskCard("plan:${plan.id}:$index", todo?.id ?: task.todoId, plan.id, index,
                plan.createdAt ?: item.at, state = plan.state)
            identities[card.id] = card
        } }
        projected.forEach { identities[it.id] = it }
        // A todo may be dispatched again under a new plan while both old cache slots remain held.
        todos.filter { todo -> todo.planId != null && identities.values.any { it.todoId == todo.id } }.forEach { todo ->
            val plan = plans[todo.planId] ?: planRows.firstOrNull { it.second.id == todo.planId }?.second
            val index = plan?.tasks?.indexOfFirst { it.todoId == todo.id }?.takeIf { it >= 0 }
                ?: if (plan?.tasks?.size == 1) 0 else null
            if (index != null) {
                val id = "plan:${todo.planId}:$index"
                identities[id] = identities[id]?.copy(todoId = todo.id)
                    ?: AssistantTaskCard(id, todo.id, todo.planId, index, plan?.createdAt ?: todo.createdAt)
            } else if (identities.values.none { it.todoId == todo.id && it.planId == todo.planId }) {
                identities["todo:${todo.id}"] = AssistantTaskCard("todo:${todo.id}", todo.id, todo.planId,
                    at = plan?.createdAt ?: todo.createdAt)
            }
        }
        val byTodo = identities.values.mapNotNull { card -> card.todoId?.let { it to card.id } }.toMap().toMutableMap()
        // A plan can reach the client before its todo_id fields. Resolve only an unambiguous task, never by title alone.
        todos.forEach { todo ->
            if (todo.id !in byTodo && todo.planId != null) {
                val plan = plans[todo.planId] ?: planRows.firstOrNull { it.second.id == todo.planId }?.second
                val index = plan?.tasks?.indexOfFirst { it.todoId == todo.id }?.takeIf { it >= 0 }
                    ?: if (plan?.tasks?.size == 1) 0 else null
                if (index != null) {
                    val id = "plan:${todo.planId}:$index"
                    identities[id]?.let { identities[id] = it.copy(todoId = todo.id); byTodo[todo.id] = id }
                }
            }
        }
        feed.filter { it.kind in lifecycle && it.kind != "plan" && !it.ref.isNullOrBlank() }.forEach { item ->
            val todoId = item.ref!!
            if (todoId !in byTodo) {
                val todo = todos.firstOrNull { it.id == todoId }
                val id = "todo:$todoId"
                identities[id] = AssistantTaskCard(id, todoId, todo?.planId, at = todo?.createdAt ?: item.at)
                byTodo[todoId] = id
            }
        }
        val todoById = todos.associateBy { it.id }
        val canonical = identities.values.filter { it.todoId == null } + identities.values.filter { it.todoId != null }
            .groupBy { it.todoId }.map { (todoId, cards) ->
                val current = todoById[todoId]?.planId
                cards.maxWith(compareBy<AssistantTaskCard> { current != null && it.planId == current }
                    .thenBy { it.index != null }.thenBy { AssistantLines.time(it.at) }
                    .thenBy { AssistantLines.time(it.updatedAt) }.thenBy { it.id })
            }
        return canonical.map { card ->
            val plan = card.planId?.let(plans::get) ?: planRows.firstOrNull { it.second.id == card.planId }?.second
            val todo = todos.firstOrNull { it.id == card.todoId }
            val records = feed.filter { it.kind in lifecycle && it.kind != "plan" &&
                ((card.todoId != null && it.ref == card.todoId) || it.id == card.receiptId || it.id == card.reportId) }
                .distinctBy { it.id }.sortedWith(compareBy({ AssistantLines.time(it.at) }, { it.id }))
            MergedTaskCard(card, plan, todo, records)
        }.sortedWith(compareBy({ AssistantLines.time(it.identity.at) }, { it.identity.id }))
    }

    /** [inline]: the records drawn among the lines ([isBackgroundEvent]). */
    fun lines(lines: List<ChatLine>, feed: List<FeedItem>, plans: Map<String, TaskPlan>, todos: List<Todo>,
        projected: List<AssistantTaskCard> = emptyList(), inline: Boolean = false): List<ChatLine> {
        val cards = merge(feed, plans, todos, projected)
        val dispatched = cards.groupBy { it.identity.planId ?: it.identity.id }.filterValues { group ->
            group.any { it.todo != null || it.plan?.state == "started" || it.records.any { record -> record.kind == "task" } }
        }
        val payloads = dispatched.mapValues { (_, group) ->
            DispatchHint(group.sortedBy { it.identity.index ?: Int.MAX_VALUE }.map { card ->
                val planned = card.identity.index?.let { card.plan?.tasks?.getOrNull(it) }
                // the to-do's current title, the plan's until the to-do arrives
                DispatchedTask(card.todo?.id ?: card.identity.todoId,
                    card.todo?.title ?: planned?.title.orEmpty(),
                    error = planned?.error,
                    // A plan's tasks are New. A standalone task item's wake is the badge (#5110).
                    wake = if (card.identity.index == null) card.records.lastOrNull { it.kind == "task" }?.field("wake") else null,
                    key = card.identity.id)
            })
        }
        val drawn = if (inline) drawnPlans(feed, plans, dispatched.keys) else emptySet()
        val combined = feed.filterNot { isBackgroundEvent(it, inline) && it.id !in drawn } + dispatched.map { (id, group) ->
            FeedItem(id, at = group.first().identity.at, kind = "unknown")
        }
        return AssistantLines.lines(lines, combined.sortedWith(compareBy({ AssistantLines.time(it.at) }, { it.id }))).map { line ->
            val id = ((line as? ChatLine.Extra)?.payload as? AssistantExtra.Feed)?.item?.id
            payloads[id]?.let { ChatLine.Extra("dispatch-$id", it) } ?: line
        }
    }
}
