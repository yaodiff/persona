package persona.core.hub

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.sync.withPermit
import kotlinx.coroutines.withContext
import kotlinx.serialization.DeserializationStrategy
import kotlinx.serialization.ExperimentalSerializationApi
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.decodeFromStream
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.put
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.LnModeResponse
import persona.core.contracts.PushIntercept
import persona.core.contracts.PushInterceptsResponse
import persona.core.contracts.JsonCodec
import persona.core.contracts.PlanTask
import kotlinx.serialization.builtins.ListSerializer
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoDecision
import persona.core.contracts.TodoDecisionResponse
import persona.core.contracts.TodoDecisionsResponse
import persona.core.contracts.TodoDue
import persona.core.contracts.TodosPageResponse
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.AssistantViewAnswer
import persona.core.contracts.api.TasksApi
import persona.core.contracts.generated.ExtensionAssistantHTMLActionInput
import persona.core.contracts.generated.ExtensionAssistantHTMLOpenResult
import persona.core.contracts.generated.ExtensionViewAction
import persona.core.contracts.generated.ExtensionViewActionInput
import persona.core.contracts.generated.ExtensionViewChangedError

private const val START_ANSWER_READ_TIMEOUT_SECONDS = 30L
private const val TODO_REVISION_FEATURE = "todo_revision"
private const val TODO_REVISION_HEADER = "X-Persona-Todos-Revision"
private const val TODO_PAGE_LIMIT = 200
private const val MAX_TODO_PAGES = 256

internal data class TasksRequestOptions(
    val headers: Map<String, String> = emptyMap(),
    val buffered: Boolean = true,
    val slow: Boolean = false,
    val allowNotModified: Boolean = false,
)

/** The unified entry's routes on the active pairing (docs/protocol/protocol.md "Tasks and the chat feed"). */
class HttpTasksApi internal constructor(
    private val requestWithOptions: suspend (path: String, method: String, body: JsonElement?, options: TasksRequestOptions) -> Response,
    override val features: kotlinx.coroutines.flow.StateFlow<Set<String>>? = null,
) : TasksApi {
    internal constructor(
        request: suspend (path: String, method: String, body: JsonElement?) -> Response,
        features: kotlinx.coroutines.flow.StateFlow<Set<String>>? = null,
        slowRequest: (suspend (path: String, method: String, body: JsonElement?) -> Response)? = null,
    ) : this({ path, method, body, options ->
        (if (options.slow) slowRequest ?: request else request)(path, method, body)
    }, features)

    /** On [connection], whose hello's features say what its cards may do. */
    constructor(connection: OkHttpHubConnection) : this({ path, method, body, options ->
        connection.request(
            path = path,
            method = method,
            body = body?.toString()?.toRequestBody("application/json".toMediaType()),
            headers = options.headers,
            buffered = options.buffered,
            readTimeoutSeconds = if (options.slow) START_ANSWER_READ_TIMEOUT_SECONDS else null,
            allowNotModified = options.allowNotModified,
        )
    }, connection.serverFeatures)

    private val todosFetchLock = Mutex()
    private var lastTodoRevision: String? = null
    private var lastTodos: List<Todo>? = null

    private sealed interface RevisionCheck {
        data object NotModified : RevisionCheck
        data class Current(val tag: String) : RevisionCheck
        data object Unavailable : RevisionCheck
    }

    private suspend fun call(path: String, method: String = "GET", body: JsonElement? = null, slow: Boolean = false): JsonObject =
        requestWithOptions(path, method, body, TasksRequestOptions(slow = slow)).use {
            if (!it.isSuccessful) throw HubFailure.from(it)
            val text = it.body?.string().orEmpty()
            if (text.isBlank()) JsonObject(emptyMap()) else JsonCodec.parseToJsonElement(text).jsonObject
        }

    @OptIn(ExperimentalSerializationApi::class)
    private fun <T> decodeStream(response: Response, deserializer: DeserializationStrategy<T>, empty: () -> T): T {
        val body = response.body ?: return empty()
        return body.byteStream().buffered().use { input ->
            while (true) {
                input.mark(1)
                val first = input.read()
                if (first == -1) return@use empty()
                if (first != 9 && first != 10 && first != 13 && first != 32) {
                    input.reset()
                    break
                }
            }
            JsonCodec.decodeFromStream(deserializer, input)
        }
    }

    /**
     * Stream a potentially large JSON response on [Dispatchers.IO] without buffering the full body or
     * materializing a whole-response `JsonObject` DOM, serialized behind the shared decode gate (#1446).
     */
    private suspend fun <T> streamGet(
        path: String,
        deserializer: DeserializationStrategy<T>,
        empty: () -> T,
    ): Pair<T, String?> = withContext(Dispatchers.IO) {
        HttpInboxAssistantTransport.jsonDecodeGate.withPermit {
            currentCoroutineContext().ensureActive()
            requestWithOptions(path, "GET", null, TasksRequestOptions(buffered = false)).use { response ->
                if (!response.isSuccessful) throw HubFailure.from(response)
                val revision = response.header(TODO_REVISION_HEADER)?.takeIf(String::isNotBlank)
                decodeStream(response, deserializer, empty) to revision
            }
        }
    }

    private suspend fun checkTodoRevision(ifNoneMatch: String?): RevisionCheck = withContext(Dispatchers.IO) {
        try {
            val headers = if (ifNoneMatch != null) mapOf("If-None-Match" to ifNoneMatch) else emptyMap()
            requestWithOptions(
                path("todos", "revision"),
                "GET",
                null,
                TasksRequestOptions(headers = headers, buffered = false, allowNotModified = true),
            ).use { response ->
                when {
                    response.code == 304 -> RevisionCheck.NotModified
                    response.isSuccessful -> {
                        val headerTag = response.header(TODO_REVISION_HEADER)?.takeIf(String::isNotBlank)
                            ?: response.header("ETag")?.takeIf(String::isNotBlank)
                        val bodyTag = if (headerTag != null) null else runCatching {
                            val text = response.body?.string().orEmpty()
                            if (text.isBlank()) null
                            else (JsonCodec.parseToJsonElement(text).jsonObject["revision"] as? JsonPrimitive)
                                ?.content?.takeIf(String::isNotBlank)
                        }.getOrNull()
                        val tag = headerTag ?: bodyTag
                        if (tag != null) RevisionCheck.Current(tag) else RevisionCheck.Unavailable
                    }
                    else -> RevisionCheck.Unavailable
                }
            }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (failure: HubFailure) {
            if (failure.kind == HubError.PairingExpired) throw failure
            RevisionCheck.Unavailable
        } catch (_: Exception) {
            RevisionCheck.Unavailable
        }
    }

    private fun path(vararg segments: String, query: Map<String, String> = emptyMap()): String {
        val url = HttpUrl.Builder().scheme("https").host("example.com").addPathSegments("api/v1")
        segments.forEach { url.addPathSegment(it) }
        query.forEach { (k, v) -> url.addQueryParameter(k, v) }
        val built = url.build()
        return built.encodedPath + (built.encodedQuery?.let { "?$it" } ?: "")
    }

    override suspend fun lnMode(): LnModeResponse =
        JsonCodec.decodeFromJsonElement<LnModeResponse>(call(path("push", "ln-mode")))

    override suspend fun pushIntercepts(): PushInterceptsResponse {
        val rows = call(path("push", "intercepts"))["intercepts"]?.jsonArray?.mapNotNull {
            runCatching { JsonCodec.decodeFromJsonElement<PushIntercept>(it) }.getOrNull()
        }.orEmpty()
        return PushInterceptsResponse(rows)
    }

    override suspend fun taskAgent(key: persona.core.contracts.SessionKey): persona.core.contracts.Session? {
        if (features?.value?.contains("sessions_lookup") != true) return null
        val response = call(path("sessions", query = mapOf("summary" to "0", "limit" to "1",
            "machine_id" to key.machineId, "session_id" to key.sessionId)))
        return JsonCodec.decodeFromJsonElement<persona.core.contracts.SessionsResponse>(response)
            .sessions.firstOrNull { it.key == key }
    }

    private data class TodosFetchResult(
        val todos: List<Todo>,
        val headerRevision: String?,
        val paginated: Boolean,
    )

    private suspend fun fetchTodosStreaming(): TodosFetchResult {
        val (firstPage, firstHeaderRevision) = try {
            streamGet(
                path("todos", query = mapOf("limit" to TODO_PAGE_LIMIT.toString())),
                TodosPageResponse.serializer(),
            ) { TodosPageResponse() }
        } catch (failure: HubFailure) {
            val fallbackUnpaginated = failure.status == 501 ||
                (failure.status == 400 && failure.code == "todos_pagination_manager_required")
            if (!fallbackUnpaginated) throw failure
            val (full, revision) = streamGet(path("todos"), TodosPageResponse.serializer()) { TodosPageResponse() }
            return TodosFetchResult(full.todos, revision, paginated = false)
        }

        // Older hubs ignore ?limit and omit has_more while answering the complete list in one stream.
        val firstHasMore = firstPage.hasMore
        if (firstHasMore == null) {
            return TodosFetchResult(firstPage.todos, firstHeaderRevision, paginated = false)
        }
        if (!firstHasMore || firstPage.nextCursor.isNullOrEmpty()) {
            return TodosFetchResult(firstPage.todos, firstHeaderRevision, paginated = true)
        }

        val collected = LinkedHashMap<String, Todo>(firstPage.todos.size)
        firstPage.todos.forEach { collected[it.id] = it }
        val seenCursors = HashSet<String>()
        var nextCursor: String? = firstPage.nextCursor
        var pages = 1
        while (!nextCursor.isNullOrEmpty()) {
            check(pages < MAX_TODO_PAGES) { "Todos page limit exceeded" }
            check(seenCursors.add(nextCursor)) { "Repeated todos cursor" }
            val (page, _) = streamGet(
                path("todos", query = mapOf("limit" to TODO_PAGE_LIMIT.toString(), "cursor" to nextCursor)),
                TodosPageResponse.serializer(),
            ) { TodosPageResponse() }
            page.todos.forEach { collected[it.id] = it }
            pages++
            nextCursor = if (page.hasMore == true) page.nextCursor?.takeIf(String::isNotEmpty) else null
        }
        return TodosFetchResult(collected.values.toList(), headerRevision = null, paginated = true)
    }

    // The list is every manager's, each to-do with its `manager` (docs/protocol/protocol.md "Per-manager routing"): noted, so the
    // requests for one name its manager, as an inbox item's do.
    override suspend fun todos(): List<Todo> = todosFetchLock.withLock {
        val supportsRevision = features?.value?.contains(TODO_REVISION_FEATURE) == true
        var revisionBefore: String? = null
        if (!supportsRevision) {
            lastTodoRevision = null
            lastTodos = null
        } else {
            val cachedRevision = lastTodoRevision
            val cachedTodos = lastTodos
            if (cachedRevision != null && cachedTodos != null) {
                when (val check = checkTodoRevision(cachedRevision)) {
                    RevisionCheck.NotModified -> return@withLock cachedTodos
                    is RevisionCheck.Current -> revisionBefore = check.tag
                    RevisionCheck.Unavailable -> {
                        lastTodoRevision = null
                        revisionBefore = null
                    }
                }
            } else {
                revisionBefore = (checkTodoRevision(null) as? RevisionCheck.Current)?.tag
            }
        }

        val fetched = fetchTodosStreaming()
        fetched.todos.forEach { AssistantManagerChoice.noteItem(listOf("todos", it.id), it.manager) }

        if (!supportsRevision) {
            lastTodoRevision = null
            lastTodos = null
        } else if (fetched.headerRevision != null) {
            lastTodoRevision = fetched.headerRevision
            lastTodos = fetched.todos
        } else if (fetched.paginated && revisionBefore != null) {
            // Confirm no write raced the paginated read before caching the lightweight validator.
            if (checkTodoRevision(revisionBefore) == RevisionCheck.NotModified) {
                lastTodoRevision = revisionBefore
                lastTodos = fetched.todos
            } else {
                lastTodoRevision = null
                lastTodos = null
            }
        } else {
            lastTodoRevision = null
            lastTodos = null
        }
        fetched.todos
    }

    // A new to-do is the current assistant's (the client names it on `POST todos`), or the one the caller named for it
    // (a RequestManager: an agent's own assistant when it is linked at once; "" the default's).
    override suspend fun createTodo(title: String, notes: String, due: TodoDue?): Todo {
        val named = kotlinx.coroutines.currentCoroutineContext()[persona.core.contracts.api.RequestManager]
        val owner = if (named != null) named.id.ifEmpty { null } else AssistantManagerChoice.current.value.manager
        val made: Todo = JsonCodec.decodeFromJsonElement(call(path("todos"), "POST", buildJsonObject {
            put("title", title)
            put("notes", notes)
            when (due) {
                is TodoDue.At -> put("due_at", due.at.toString())
                is TodoDue.Day -> put("due_date", due.day.toString())
                null -> Unit
            }
        })["todo"] ?: error("no todo in the answer"))
        return (if (made.manager == null) made.copy(manager = owner) else made)
            .also { AssistantManagerChoice.noteItem(listOf("todos", it.id), it.manager) }
    }

    override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?): Todo =
        stamped(JsonCodec.decodeFromJsonElement(call(path("todos", id), "PATCH", buildJsonObject {
            title?.let { put("title", it) }
            notes?.let { put("notes", it) }
            done?.let { put("done", it) }
        })["todo"] ?: error("no todo in the answer")))

    override suspend fun setTodoSeen(id: String, seen: Boolean): Todo =
        stamped(JsonCodec.decodeFromJsonElement(call(path("todos", id, "seen"), if (seen) "PUT" else "DELETE",
            if (seen) JsonObject(emptyMap()) else null)["todo"] ?: error("no todo in the answer")))

    override suspend fun setTodoPlan(id: String, plan: persona.core.contracts.TodoPlan): Todo =
        stamped(JsonCodec.decodeFromJsonElement(call(path("todos", id), "PATCH", todoPlanBody(plan))["todo"] ?: error("no todo in the answer")))

    override suspend fun setTodoDue(id: String, due: TodoDue): Todo =
        stamped(JsonCodec.decodeFromJsonElement(call(path("todos", id), "PATCH", buildJsonObject {
            when (due) {
                is TodoDue.At -> put("due_at", due.at.toString())
                is TodoDue.Day -> put("due_date", due.day.toString())
            }
        })["todo"] ?: error("no todo in the answer")))

    /** One to-do's answer comes from its manager unstamped: it keeps the manager its list showed it under. */
    private fun stamped(todo: Todo): Todo =
        if (todo.manager != null) todo else todo.copy(manager = AssistantManagerChoice.itemHeader(listOf("todos", todo.id)))

    override suspend fun deleteTodo(id: String) {
        call(path("todos", id), "DELETE")
    }

    override suspend fun todoDecisions(todoId: String): List<TodoDecision> =
        JsonCodec.decodeFromJsonElement<TodoDecisionsResponse>(call(path("todos", todoId, "decisions", query = mapOf("all" to "1"))))
            .decisions.orEmpty()

    override suspend fun addTodoDecision(todoId: String, question: String, choice: String, reason: String, supersedesId: String): TodoDecision =
        JsonCodec.decodeFromJsonElement<TodoDecisionResponse>(call(path("todos", todoId, "decisions"), "POST", buildJsonObject {
            put("question", question.trim())
            put("choice", choice.trim())
            reason.trim().takeIf { it.isNotEmpty() }?.let { put("reason", it) }
            supersedesId.takeIf { it.isNotEmpty() }?.let { put("supersedes_id", it) }
        })).decision

    override suspend fun linkTodoAgent(todoId: String, machineId: String, sessionId: String) {
        call(path("todos", todoId, "agents", machineId, sessionId), "PUT", JsonObject(emptyMap()))
    }

    override suspend fun unlinkTodoAgent(todoId: String, machineId: String, sessionId: String) {
        call(path("todos", todoId, "agents", machineId, sessionId), "DELETE")
    }

    override suspend fun linkTodoSpace(todoId: String, machineId: String, space: String) {
        call(path("todos", todoId, "spaces", machineId, space), "PUT", JsonObject(emptyMap()))
    }

    override suspend fun unlinkTodoSpace(todoId: String, machineId: String, space: String) {
        call(path("todos", todoId, "spaces", machineId, space), "DELETE")
    }

    private val feedOwners = java.util.concurrent.ConcurrentHashMap<String, String>()
    private val conversationOwners = java.util.concurrent.ConcurrentHashMap<String, String>()

    override fun noteFeedOwner(item: FeedItem, manager: String?) {
        val owner = persona.core.contracts.pushManager(manager)
        feedOwners[item.id] = owner
        conversationOwners[item.conversation] = owner
    }

    private fun feedOwner(conversation: String): String = conversationOwners[conversation]
        ?: AssistantManagerChoice.itemManager(listOf("assistant", "conversations", conversation))
        ?: AssistantManagerChoice.current.value.defaultId

    override suspend fun feed(conversation: String, since: String?): FeedResponse =
        readFeed(conversation, since, null, feedOwner(conversation), paginated = false)

    override fun feedPages(conversation: String): persona.core.contracts.api.FeedPageReader {
        val owner = feedOwner(conversation)
        return persona.core.contracts.api.FeedPageReader { since, cursor -> readFeed(conversation, since, cursor, owner, paginated = true) }
    }

    @OptIn(ExperimentalSerializationApi::class)
    private suspend fun readFeed(conversation: String, since: String?, cursor: String?, owner: String, paginated: Boolean): FeedResponse {
        val query = buildMap {
            if (paginated) put(ManagerQuery, owner)
            if (cursor != null) put("cursor", cursor)
            else {
                if (paginated) put("paginate", "1")
                since?.let { put("since", it) }
            }
        }
        val (response, _) = streamGet(
            path("assistant", "conversations", conversation, "feed", query = query),
            FeedResponse.streamingSerializer(),
        ) { FeedResponse() }
        response.items.forEach { feedOwners.putIfAbsent(it.id, owner) }
        conversationOwners.putIfAbsent(conversation, owner)
        return response
    }

    override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = startPlan(id, remember, null)

    override suspend fun startPlan(id: String, remember: Boolean, tasks: List<PlanTask>?): TaskPlan =
        plan(call(path("task-plans", id, "start"), "POST", buildJsonObject {
            tasks?.let { put("tasks", JsonCodec.encodeToJsonElement(ListSerializer(PlanTask.serializer()), it)) }
            put("remember", remember)
        }))

    override suspend fun dismissPlan(id: String): TaskPlan = plan(call(path("task-plans", id, "dismiss"), "POST", JsonObject(emptyMap())))

    override suspend fun retryTask(todoId: String): Todo =
        stamped(JsonCodec.decodeFromJsonElement(call(path("todos", todoId, "retry"), "POST", JsonObject(emptyMap()))["todo"]
            ?: error("no todo in the answer")))

    override suspend fun retryTaskOnLauncher(todoId: String, launcher: String): Todo =
        stamped(JsonCodec.decodeFromJsonElement(call(path("todos", todoId, "retry"), "POST", buildJsonObject {
            put("launcher", launcher)
        })["todo"] ?: error("no todo in the answer")))

    override suspend fun answerTaskStart(todoId: String, choice: String): Todo =
        stamped(JsonCodec.decodeFromJsonElement(call(path("todos", todoId, "start", "answer"), "POST", buildJsonObject {
            put("choice", choice)
        }, slow = true)["todo"] ?: error("no todo in the answer")))

    override suspend fun acceptTask(todoId: String, askedAt: String) {
        call(path("todos", todoId, "accept"), "POST", buildJsonObject { put("asked_at", askedAt) })
    }

    override suspend fun answerTask(todoId: String, askedAt: String, answer: persona.core.contracts.api.AskAnswer) {
        call(path("todos", todoId, "accept"), "POST", buildJsonObject {
            put("asked_at", askedAt)
            when (answer) {
                is persona.core.contracts.api.AskAnswer.Option -> put("option", answer.label)
                is persona.core.contracts.api.AskAnswer.Response -> put("response", answer.code)
            }
        })
    }

    override suspend fun markLaterHandled(questionId: String) {
        call(path("later", questionId, "handled"), "POST", JsonObject(emptyMap()))
    }

    override suspend fun ruleAlways(suggestionId: String): persona.core.contracts.RuleSuggestionMade =
        JsonCodec.decodeFromJsonElement(call(path("assistant", "rule-suggestions", suggestionId, "always"), "POST", JsonObject(emptyMap())))

    override suspend fun ruleNotNow(suggestionId: String) {
        call(path("assistant", "rule-suggestions", suggestionId, "not-now"), "POST", JsonObject(emptyMap()))
    }

    override suspend fun removeTask(todoId: String) {
        call(path("todos", todoId), "DELETE")
    }

    override suspend fun route(conversation: String, todoId: String, text: String): FeedItem =
        item(call(path("assistant", "conversations", conversation, "route"), "POST",
            buildJsonObject { put("todo_id", todoId); put("text", text) }))

    override suspend fun today(conversation: String): FeedItem =
        item(call(path("assistant", "conversations", conversation, "today"), "POST", JsonObject(emptyMap())))

    override suspend fun mainConversation(): String? =
        (call(path("assistant"))["main"] as? kotlinx.serialization.json.JsonPrimitive)?.content?.takeIf(String::isNotBlank)

    override suspend fun inboxReply(itemId: String, text: String, card: String?) {
        call(path("inbox", itemId, "reply"), "POST", buildJsonObject { put("text", text); card?.let { put("card", it) } })
    }

    override suspend fun dismissMessage(cardId: String, draft: String): persona.core.contracts.api.MessageDraftDismissal {
        val owner = feedOwners[cardId] ?: persona.core.contracts.api.ManagerRoster.ASSISTANT_MANAGER
        feedOwners[cardId] = owner
        val response = call(path("assistant", "feed", cardId, "dismiss", query = mapOf("manager" to owner)), "POST", buildJsonObject { put("draft", draft) })
        val token = (response["dismissal_id"] as? kotlinx.serialization.json.JsonPrimitive)?.content?.takeIf(String::isNotBlank)
            ?: error("no dismissal token in the answer")
        return persona.core.contracts.api.MessageDraftDismissal(item(response), token)
    }

    override suspend fun undoDismissMessage(cardId: String, dismissalId: String): FeedItem =
        item(call(path("assistant", "feed", cardId, "undo-dismiss", query = mapOf("manager" to
            (feedOwners[cardId] ?: persona.core.contracts.api.ManagerRoster.ASSISTANT_MANAGER))), "POST", buildJsonObject { put("dismissal_id", dismissalId) }))

    override suspend fun viewAction(conversation: String, key: String, input: ExtensionViewActionInput): AssistantViewAnswer =
        cardCall(path("assistant", "conversations", conversation, "views", key, "actions"),
            JsonCodec.encodeToJsonElement(ExtensionViewActionInput.serializer(), input))

    override suspend fun viewHtmlAction(conversation: String, key: String, input: ExtensionAssistantHTMLActionInput): AssistantViewAnswer =
        cardCall(path("assistant", "conversations", conversation, "views", key, "html-actions"),
            JsonCodec.encodeToJsonElement(ExtensionAssistantHTMLActionInput.serializer(), input))

    /**
     * A card's action: the recorded answer, an HTML page's checked link, or one of the two 409s with what their body
     * carries (the recorded answer; the card's current version and view, `ViewChangedError`).
     */
    private suspend fun cardCall(path: String, body: JsonElement): AssistantViewAnswer {
        val o = try { call(path, "POST", body) } catch (failure: HubFailure) {
            if (failure.status != 409) throw failure
            val error = failure.body?.let { runCatching { JsonCodec.parseToJsonElement(it) as? JsonObject }.getOrNull() }
            return if (failure.code == "already_answered") AssistantViewAnswer.AlreadyAnswered(
                error?.get("action")?.let { runCatching { JsonCodec.decodeFromJsonElement(ExtensionViewAction.serializer(), it) }.getOrNull() })
            else {
                // view_changed, or an older hub's plain conflict for the same version check.
                val changed = error?.let { runCatching { JsonCodec.decodeFromJsonElement(ExtensionViewChangedError.serializer(), it) }.getOrNull() }
                AssistantViewAnswer.ViewChanged(changed?.version?.takeIf { it > 0 }, changed?.view)
            }
        }
        if (o["open"] != null) {
            val open = JsonCodec.decodeFromJsonElement(ExtensionAssistantHTMLOpenResult.serializer(), o)
            if (open.open.isEmpty() || open.version <= 0) error("no checked link in the answer")
            return AssistantViewAnswer.Open(open.open, open.version)
        }
        return AssistantViewAnswer.Recorded(JsonCodec.decodeFromJsonElement(ExtensionViewAction.serializer(),
            o["action"] ?: error("no action in the answer")))
    }

    private fun plan(o: JsonObject): TaskPlan = JsonCodec.decodeFromJsonElement(o["plan"] ?: error("no plan in the answer"))
    private fun item(o: JsonObject): FeedItem = JsonCodec.decodeFromJsonElement(o["item"] ?: error("no item in the answer"))
}

/** The PATCH body of a to-do's plan (#4636), as iOS `TodoPlanChange` encodes it: every field sent, null clears. */
internal fun todoPlanBody(plan: persona.core.contracts.TodoPlan) = buildJsonObject {
    when (val due = plan.due) {
        null -> put("due_at", JsonNull)
        is TodoDue.At -> put("due_at", due.at.toString())
        is TodoDue.Day -> put("due_date", due.day.toString())
    }
    put("remind_at", plan.remindAt?.toString()?.let(::JsonPrimitive) ?: JsonNull)
    put("priority", plan.priority)
}
