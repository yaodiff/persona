package persona.core.contracts.api

import persona.core.contracts.FeedItem
import persona.core.contracts.LnModeResponse
import persona.core.contracts.PushInterceptsResponse
import persona.core.contracts.FeedResponse
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo

/** A confirmed card-scoped dismissal, retaining the token required for a lightweight Undo. */
data class MessageDraftDismissal(val item: FeedItem, val dismissalId: String)

/** One bounded feed range, with its request owner captured before the first page. */
fun interface FeedPageReader {
    suspend fun read(since: String?, cursor: String?): FeedResponse
}

/** Tasks dispatched from an assistant conversation (docs/assistant/unified-entry.md; docs/protocol/protocol.md "Tasks and the chat feed"). */
interface TasksApi {
    /** Low-distraction mode and today's held pushes; demo/older test hubs keep the entry hidden. */
    suspend fun lnMode(): LnModeResponse = LnModeResponse(false)
    suspend fun pushIntercepts(): PushInterceptsResponse = PushInterceptsResponse(emptyList())
    /** Exact metadata for a bound agent, independent of the Agents list filter and page. */
    suspend fun taskAgent(key: persona.core.contracts.SessionKey): persona.core.contracts.Session? = null
    suspend fun todos(): List<Todo>
    /** A new to-do (`POST /api/v1/todos`), due at [due] when given (`due_at` or `due_date`: the calendar's tap, #4200). */
    suspend fun createTodo(title: String, notes: String = "", due: persona.core.contracts.TodoDue? = null): Todo = error("createTodo not implemented")
    suspend fun patchTodo(id: String, title: String? = null, notes: String? = null, done: Boolean? = null): Todo = error("patchTodo not implemented")
    /** Files a Just finished to-do under Done, or removes only that seen mark for Undo (`PUT` / `DELETE …/{id}/seen`). */
    suspend fun setTodoSeen(id: String, seen: Boolean): Todo = error("setTodoSeen not implemented")
    /** Moves a to-do's due date (`PATCH /api/v1/todos/{id}` with `due_at` or `due_date`, the other cleared; #2045). */
    suspend fun setTodoDue(id: String, due: persona.core.contracts.TodoDue): Todo = error("setTodoDue not implemented")
    /** The detail page's due date, reminder and priority, all three in one PATCH (#4636). */
    suspend fun setTodoPlan(id: String, plan: persona.core.contracts.TodoPlan): Todo = error("setTodoPlan not implemented")
    suspend fun deleteTodo(id: String) = Unit
    suspend fun linkTodoAgent(todoId: String, machineId: String, sessionId: String) = Unit
    suspend fun unlinkTodoAgent(todoId: String, machineId: String, sessionId: String) = Unit
    /** "Attach to to-do" a whole project (PUT /api/v1/todos/{id}/spaces/{mid}/{space}): its agents follow the todo. */
    suspend fun linkTodoSpace(todoId: String, machineId: String, space: String): Unit = error("linkTodoSpace not implemented")
    /** "Remove from to-do": the project and the agents it brought leave the todo. */
    suspend fun unlinkTodoSpace(todoId: String, machineId: String, space: String): Unit = error("unlinkTodoSpace not implemented")
    /** A to-do's decisions (#1618), oldest first, the revised ones included (`GET /api/v1/todos/{id}/decisions?all=1`). */
    suspend fun todoDecisions(todoId: String): List<persona.core.contracts.TodoDecision> = error("todoDecisions not implemented")
    /** Records a decision by hand; with [supersedesId], one that revises that earlier decision. A blank reason is not sent. */
    suspend fun addTodoDecision(todoId: String, question: String, choice: String, reason: String = "", supersedesId: String = ""):
        persona.core.contracts.TodoDecision = error("addTodoDecision not implemented")

    /** A conversation's feed after [since] (an ISO time; null: all of it), with the plans its `plan` items name. */
    suspend fun feed(conversation: String, since: String?): FeedResponse
    /** Paginated reads retain the original since and owner; transports without cursors keep their existing read. */
    fun feedPages(conversation: String): FeedPageReader = FeedPageReader { since, _ -> feed(conversation, since) }
    suspend fun startPlan(id: String, remember: Boolean): TaskPlan
    /** "Start" after "Edit": [tasks] replaces the plan's tasks by index with what the user changed (null: as planned). */
    suspend fun startPlan(id: String, remember: Boolean, tasks: List<persona.core.contracts.PlanTask>?): TaskPlan = startPlan(id, remember)
    suspend fun dismissPlan(id: String): TaskPlan
    /**
     * "Retry" on a task whose agent did not start (#1396): the hub starts the same task again in the same todo and answers
     * it reopened, without its error. Refused (409) when the machine is offline or the task did not fail to start.
     */
    suspend fun retryTask(todoId: String): Todo
    /** Removes a task: its todo goes; its agents and their chats stay. */
    suspend fun removeTask(todoId: String)
    /** Accept only the owner's displayed round, never a newer ask under the same todo id. */
    suspend fun acceptTask(todoId: String, askedAt: String): Unit = error("Task acceptance unavailable")
    /**
     * Answer a decision's round through the same accept route (#3804, docs/assistant/todos.md "Decisions the owner can
     * answer"): one of its options, or a fixed answer that says the owner cannot answer it as asked.
     */
    suspend fun answerTask(todoId: String, askedAt: String, answer: AskAnswer): Unit = error("Task answers unavailable")
    /** Mark a question left for a briefing as handled (`POST /api/v1/later/{id}/handled`). */
    suspend fun markLaterHandled(questionId: String): Unit = error("Later-question handling unavailable")
    /**
     * "Always" on a card's suggested rule (#4929, `POST /api/v1/assistant/rule-suggestions/{id}/always`): the hub counts
     * the answers again and makes the rule only if they still earn it. 404: not earned any more, or already answered.
     */
    suspend fun ruleAlways(suggestionId: String): persona.core.contracts.RuleSuggestionMade = error("Rule suggestions unavailable")
    /** "Not now": the line is not shown for its kind and scope for 30 days (`…/rule-suggestions/{id}/not-now`). */
    suspend fun ruleNotNow(suggestionId: String): Unit = error("Rule suggestions unavailable")
    /** Sends text straight to a task's agent; the answer is the conversation's new `routed` item. */
    suspend fun route(conversation: String, todoId: String, text: String): FeedItem
    /** The conversation's "Today" card for the user's day (made on the first call of the day). */
    suspend fun today(conversation: String): FeedItem
    /** The main assistant conversation's id (GET /api/v1/assistant `main`), null when there is none. */
    suspend fun mainConversation(): String?
    /**
     * "Send" on a message card: the draft goes out through the inbox, which files the item as done and records it on
     * the card ([card], the card's id: sent once per card however many taps or devices).
     */
    suspend fun inboxReply(itemId: String, text: String, card: String? = null)

    /** Retain the authoritative live event owner before a card can be acted on, including the unstamped built-in manager. */
    fun noteFeedOwner(item: FeedItem, manager: String?) = Unit
    /** Dismiss only the displayed message card, checking its exact original draft on the hub. */
    suspend fun dismissMessage(cardId: String, draft: String): MessageDraftDismissal = error("Draft dismissal unavailable")
    /** Undo only this dismissal; the hub rejects a sent, filed or superseded draft. */
    suspend fun undoDismissMessage(cardId: String, dismissalId: String): FeedItem = error("Draft dismissal undo unavailable")

    /**
     * The features the hub these requests go to said in its last hello (`assistant_views`, `assistant_html_bridge`);
     * null when this API cannot tell, which counts as none: a card answers only on a hub that says it can.
     */
    val features: kotlinx.coroutines.flow.StateFlow<Set<String>>? get() = null

    /** A card's action (docs/assistant/assistant-views.md "Persistent submission"): [key] is the card's, sent as one escaped path segment. */
    suspend fun viewAction(conversation: String, key: String, input: persona.core.contracts.generated.ExtensionViewActionInput): AssistantViewAnswer =
        error("viewAction not implemented")

    /** What an assistant's HTML page asked for through its bridge, after the user confirmed it. */
    suspend fun viewHtmlAction(conversation: String, key: String, input: persona.core.contracts.generated.ExtensionAssistantHTMLActionInput): AssistantViewAnswer =
        error("viewHtmlAction not implemented")
}

/** An owner's answer to a decision (`POST /api/v1/todos/{id}/accept`): an option by its label, or a fixed response. */
sealed interface AskAnswer {
    data class Option(val label: String) : AskAnswer

    /** [code]: `decide_yourself`, `not_what_i_asked` or `explain_first`. */
    data class Response(val code: String) : AskAnswer
}
