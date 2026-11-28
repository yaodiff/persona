package persona.core.domain.tasks

import persona.core.domain.zhAny

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.runCurrent
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.contracts.draftPlaceholder

/** The "Reply drafted from task progress" card (#1519): sent is what the hub records on it, and it goes out once. */
class MessageCardTest {
    private fun card(id: String = "m1", extra: Map<String, String> = emptyMap()) = FeedItem(id, "c1", "2026-11-04T10:00:00Z", "message", "example:1",
        buildJsonObject { put("draft", "在修了"); extra.forEach { (k, v) -> put(k, v) } })

    @Test fun sentIsReadFromTheCard() {
        assertNull(card().sent)
        assertEquals("修好了", (card(extra = mapOf("sent_at" to "2026-11-04T10:23:00Z", "sent" to "修好了")).sent?.text).zhAny())
        assertEquals("", card(extra = mapOf("sent_at" to "2026-11-04T10:23:00Z")).sent?.text)
    }

    @Test fun aCardFiledElsewhereOffersNoSend() {
        // Without filed_at (a hub from before #4797) the card stays as it was: send offered until sent.
        assertNull(card().filedAt)
        assertTrue(card().canSend)
        val filed = card(extra = mapOf("filed_at" to "2026-11-04T10:30:00Z"))
        assertEquals("2026-11-04T10:30:00Z", filed.filedAt)
        assertFalse(filed.canSend)
        // A sent card never counts as filed: it shows what went out.
        val sent = card(extra = mapOf("sent_at" to "2026-11-04T10:23:00Z", "filed_at" to "2026-11-04T10:30:00Z"))
        assertNull(sent.filedAt)
        assertFalse(sent.canSend)
        assertTrue(card(extra = mapOf("filed_at" to "")).canSend)
    }

    @Test fun aCardSentElsewhereTakesItsNewBody() {
        val list = listOf(card())
        assertSame(list, AssistantLines.merge(list, listOf(card())))
        val sent = card(extra = mapOf("sent_at" to "2026-11-04T10:23:00Z"))
        assertEquals(listOf(sent), AssistantLines.merge(list, listOf(sent)))
    }

    @Test fun findsABlankLeftToFillIn() {
        assertEquals("【预计今晚 / 几点前】", (draftPlaceholder("确实是 bug，【预计今晚 / 几点前】修好")).zhAny())
        assertEquals("{{链接}}", (draftPlaceholder("MR 在 {{链接}}")).zhAny())
        assertNull(draftPlaceholder("确实是 bug，修好了告诉你"))
    }

    private class Api(val gate: CompletableDeferred<Unit>, var items: List<FeedItem>, val page: Int = 500) : TasksApi {
        val replies = mutableListOf<Triple<String, String, String?>>()
        val dismissals = mutableListOf<Pair<String, String>>()
        val owners = mutableListOf<Pair<String, String?>>()
        override fun noteFeedOwner(item: FeedItem, manager: String?) { owners += item.id to manager }
        val undos = mutableListOf<Pair<String, String>>()
        var dismissGate: CompletableDeferred<Unit>? = null
        var undoGate: CompletableDeferred<Unit>? = null
        var feedGate: CompletableDeferred<Unit>? = null
        var dismissError: Throwable? = null
        var undoError: Throwable? = null
        var pageReader: persona.core.contracts.api.FeedPageReader? = null
        override fun feedPages(conversation: String) = pageReader ?: super<TasksApi>.feedPages(conversation)
        override suspend fun todos() = emptyList<Todo>()
        // As the hub: after `since`, only items made later and cards sent later (filed_at is worked out on each read, no event).
        override suspend fun feed(conversation: String, since: String?): FeedResponse {
            val response = FeedResponse((if (since == null) items else items.filter { (it.at ?: "") > since ||
                (it.field("sent_at") ?: "") > since || (it.field("dismissal_updated_at") ?: "") > since }).take(page))
            feedGate?.await()
            return response
        }
        override suspend fun dismissMessage(cardId: String, draft: String): persona.core.contracts.api.MessageDraftDismissal {
            dismissals += cardId to draft
            dismissGate?.await()
            dismissError?.let { throw it }
            val old = items.single { it.id == cardId }
            assertEquals(old.field("draft"), draft)
            val updated = old.copy(body = JsonObject((old.body as JsonObject) + mapOf(
                "dismissed_at" to JsonPrimitive("2026-11-11T10:00:00Z"), "dismissal_id" to JsonPrimitive("dismissal-one"))))
            items = items.map { if (it.id == cardId) updated else it }
            return persona.core.contracts.api.MessageDraftDismissal(updated, "dismissal-one")
        }
        override suspend fun undoDismissMessage(cardId: String, dismissalId: String): FeedItem {
            undos += cardId to dismissalId
            val old = items.single { it.id == cardId }
            undoGate?.await()
            undoError?.let { throw it }
            val updated = old.copy(body = JsonObject((old.body as JsonObject) - setOf("dismissed_at", "dismissal_id")))
            items = items.map { if (it.id == cardId) updated else it }
            return updated
        }
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) {
            replies += Triple(itemId, text, card)
            gate.await()
        }
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = true
    }

    @Test fun sendsOnceHoweverManyTapsAndDrawsItSent() = runBlocking {
        val gate = CompletableDeferred<Unit>()
        val api = Api(gate, listOf(card()))
        val store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        store.load("c1")
        val errors = mutableListOf<Throwable>()
        store.sendMessage(card(), "修好了") { errors += it }
        store.sendMessage(card(), "修好了") { errors += it }
        assertTrue("m1" in store.busy.value)
        gate.complete(Unit)
        assertEquals(listOf(Triple("example:1", "修好了", "m1")), api.replies)
        assertEquals("修好了", (store.feeds.value["c1"]?.single()?.sent?.text).zhAny())
        assertTrue("m1" !in store.busy.value)
        store.sendMessage(card(), "修好了") { errors += it } // a stale copy: already sent
        assertEquals(1, api.replies.size)
        assertTrue(errors.isEmpty())
    }

    /** A card dealt with elsewhere (#4797) is not sent from a stale copy either: the hub would refuse it with a 409 (#5095). */
    @Test fun aCardFiledElsewhereIsNotSent() = runBlocking {
        val filed = card(extra = mapOf("filed_at" to "2026-11-04T10:30:00Z"))
        val api = Api(CompletableDeferred(Unit), listOf(filed))
        val store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        store.load("c1")
        val errors = mutableListOf<Throwable>()
        store.sendMessage(card(), "修好了") { errors += it } // the copy drawn before it was filed
        assertTrue(api.replies.isEmpty())
        assertTrue(errors.isEmpty())
    }

    /** Filing the item elsewhere, or moving it back, reaches a card already read: an incremental read would not carry it (#5693). */
    @Test fun aReloadSeesTheCardFiledAndBackAgain() = runBlocking {
        val api = Api(CompletableDeferred(Unit), listOf(card()))
        val store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        store.load("c1")
        assertTrue(store.feeds.value["c1"]!!.single().canSend)
        api.items = listOf(card(extra = mapOf("filed_at" to "2026-11-04T10:30:00Z")))
        store.load("c1")
        assertFalse(store.feeds.value["c1"]!!.single().canSend)
        api.items = listOf(card())
        store.load("c1")
        assertTrue(store.feeds.value["c1"]!!.single().canSend)
    }

    /** The hub pages its feed oldest first, so the reread starts at the unsent card, not at the start: later items still arrive. */
    @Test fun aReloadPastAPageSeesTheCardAndLaterItems() = runBlocking {
        val old = FeedItem("t0", "c1", "2026-11-04T09:00:00Z", "task", "example:0")
        val api = Api(CompletableDeferred(Unit), listOf(old, card()), page = 2)
        val store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        store.load("c1")
        val later = FeedItem("t1", "c1", "2026-11-04T11:00:00Z", "task", "example:2")
        api.items = listOf(old, card(extra = mapOf("filed_at" to "2026-11-04T10:30:00Z")), later)
        store.load("c1")
        assertEquals(listOf("t0", "m1", "t1"), store.feeds.value["c1"]!!.map { it.id })
        assertFalse(store.feeds.value["c1"]!!.single { it.id == "m1" }.canSend)
        api.items = listOf(old, card(), later)
        store.load("c1")
        assertTrue(store.feeds.value["c1"]!!.single { it.id == "m1" }.canSend)
    }

    @Test fun dismissedCardsDoNotPinTheReadWhileNewProgressAndMissedUndoStillArrive() = kotlinx.coroutines.test.runTest {
        val start = java.time.Instant.parse("2026-11-04T00:00:00Z")
        val old = (0 until 12).map { index ->
            card("old-$index", mapOf("draft" to "Earlier reply $index", "dismissed_at" to start.plusSeconds(index * 3600L + 60).toString(),
                "dismissal_id" to "dismissal-$index", "dismissal_updated_at" to start.plusSeconds(index * 3600L + 60).toString()))
                .copy(at = start.plusSeconds(index * 3600L).toString())
        }
        val api = Api(CompletableDeferred(Unit), old, page = old.size)
        val store = TasksStore(api, Connection(), backgroundScope)
        store.load("c1"); runCurrent()
        val progress = FeedItem("new-progress", "c1", start.plusSeconds(12 * 3600L).toString(), "task", "task-one")
        api.items = old + progress
        store.load("c1"); runCurrent()
        assertEquals(progress, store.feeds.value["c1"]!!.last())
        val restored = old.first().copy(body = JsonObject((old.first().body as JsonObject) - setOf("dismissed_at", "dismissal_id") +
            ("dismissal_updated_at" to JsonPrimitive(start.plusSeconds(13 * 3600L).toString()))))
        api.items = listOf(restored) + old.drop(1) + progress
        store.load("c1"); runCurrent()
        assertEquals(restored, store.feeds.value["c1"]!!.first())
        assertTrue(store.feeds.value["c1"]!!.first().canSend)
        assertEquals(progress, store.feeds.value["c1"]!!.last())
    }

    @Test fun stablePaginationReachesProgressBeyondAFullPageOfUpdatedOldDismissals() = kotlinx.coroutines.test.runTest {
        val start = java.time.Instant.parse("2026-11-04T00:00:00Z")
        val old = (0 until 500).map { index ->
            card("old-$index", mapOf("draft" to "Old reply $index", "dismissed_at" to start.plusSeconds(1000).toString(),
                "dismissal_updated_at" to start.plusSeconds(1000).toString()))
                .copy(at = start.plusSeconds(index.toLong()).toString())
        }
        val api = Api(CompletableDeferred(Unit), old)
        val store = TasksStore(api, Connection(), backgroundScope)
        store.load("c1"); runCurrent()
        val progress = FeedItem("new-progress", "c1", start.plusSeconds(500).toString(), "task", "task-one")
        val reads = mutableListOf<Pair<String?, String?>>()
        api.pageReader = persona.core.contracts.api.FeedPageReader { since, cursor ->
            reads += since to cursor
            when (cursor) {
                null -> FeedResponse(old, nextCursor = "opaque-next")
                "opaque-next" -> FeedResponse(listOf(progress), nextCursor = "")
                else -> error("unexpected cursor")
            }
        }
        store.load("c1"); runCurrent()
        assertEquals(listOf(old.last().at to null, old.last().at to "opaque-next"), reads)
        assertEquals(progress, store.feeds.value["c1"]!!.last())
        assertEquals(501, store.feeds.value["c1"]!!.size)
    }

    @Test fun paginationUsesExplicitEofAndContinuesAcrossEmptyPages() = kotlinx.coroutines.test.runTest {
        val original = card(extra = mapOf("draft" to "Original"))
        val api = Api(CompletableDeferred(Unit), emptyList())
        val reads = mutableListOf<String?>()
        api.pageReader = persona.core.contracts.api.FeedPageReader { _, cursor ->
            reads += cursor
            when (cursor) {
                null -> FeedResponse(emptyList(), nextCursor = "next")
                "next" -> FeedResponse(List(500) { original.copy(id = "card-$it") }, nextCursor = "")
                else -> error("explicit EOF was ignored")
            }
        }
        val store = TasksStore(api, Connection(), backgroundScope)
        store.load("c1"); runCurrent()
        assertEquals(listOf(null, "next"), reads)
        assertEquals(500, store.feeds.value["c1"]!!.size)
        val legacy = TasksStore(Api(CompletableDeferred(Unit), listOf(original)), Connection(), backgroundScope)
        legacy.load("c1"); runCurrent()
        assertEquals(listOf(original), legacy.feeds.value["c1"])
    }

    @Test fun aLaterPageCannotResurrectADismissalAcceptedAfterTheFirstPage() = kotlinx.coroutines.test.runTest {
        val original = card(extra = mapOf("draft" to "Original"))
        val api = Api(CompletableDeferred(Unit), listOf(original))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.load("c1"); runCurrent()
        val gate = CompletableDeferred<Unit>()
        val progress = FeedItem("new-progress", "c1", kind = "task", ref = "task-one")
        api.pageReader = persona.core.contracts.api.FeedPageReader { _, cursor ->
            if (cursor == null) FeedResponse(listOf(original), nextCursor = "next")
            else { gate.await(); FeedResponse(listOf(original, progress), nextCursor = "") }
        }
        store.load("c1", whole = true); runCurrent()
        store.dismissMessage(original, { throw it }) { }; runCurrent()
        gate.complete(Unit); runCurrent()
        assertFalse(store.feeds.value["c1"]!!.first { it.id == original.id }.canSend)
        assertEquals(progress, store.feeds.value["c1"]!!.first { it.id == progress.id })
    }

    @Test fun failedOrRepeatedCursorRangesKeepConfirmedDataAndRestartWithoutAdvancingSince() = kotlinx.coroutines.test.runTest {
        val original = card(extra = mapOf("draft" to "Original", "dismissed_at" to "2026-11-04T10:01:00Z"))
        val api = Api(CompletableDeferred(Unit), listOf(original))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.load("c1"); runCurrent()
        var reads = 0
        val partial = original.copy(id = "partial")
        api.pageReader = persona.core.contracts.api.FeedPageReader { _, _ ->
            reads++
            FeedResponse(listOf(partial), nextCursor = "repeated")
        }
        store.load("c1"); runCurrent()
        assertEquals(2, reads)
        assertEquals(listOf(original), store.feeds.value["c1"])
        var retrySince: String? = "not-read"
        api.pageReader = persona.core.contracts.api.FeedPageReader { since, _ ->
            retrySince = since
            FeedResponse(listOf(original), nextCursor = "")
        }
        store.load("c1"); runCurrent()
        assertNull(retrySince)
        reads = 0
        api.pageReader = persona.core.contracts.api.FeedPageReader { _, cursor ->
            reads++
            if (cursor != null) throw IllegalStateException("Page refused")
            FeedResponse(listOf(partial), nextCursor = "next")
        }
        store.load("c1"); runCurrent()
        assertEquals(2, reads)
        assertEquals(listOf(original), store.feeds.value["c1"])
    }

    @Test fun paginationStopsAtTheFinitePageBudgetWithoutPublishingPartialRows() = kotlinx.coroutines.test.runTest {
        val original = card(extra = mapOf("draft" to "Original"))
        val api = Api(CompletableDeferred(Unit), listOf(original))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.load("c1"); runCurrent()
        var reads = 0
        api.pageReader = persona.core.contracts.api.FeedPageReader { _, _ ->
            reads++
            FeedResponse(listOf(original.copy(id = "partial-$reads")), nextCursor = "next-$reads")
        }
        store.load("c1"); runCurrent()
        assertEquals(64, reads)
        assertEquals(listOf(original), store.feeds.value["c1"])
    }

    @Test fun websocketOnlyCardsCaptureTheirOwnerBeforeTheyAreShown() = kotlinx.coroutines.test.runTest {
        val api = Api(CompletableDeferred(Unit), emptyList())
        val connection = Connection()
        val store = TasksStore(api, connection, backgroundScope)
        runCurrent()
        val defaultCard = card("default-live", mapOf("draft" to "Default draft"))
        val otherCard = card("other-live", mapOf("draft" to "Other assistant draft"))
        connection.events.emit(persona.core.contracts.AssistantFeedEvent(item = defaultCard))
        connection.events.emit(persona.core.contracts.AssistantFeedEvent(item = otherCard, manager = "assistant-one"))
        runCurrent()
        assertEquals(listOf("default-live" to null, "other-live" to "assistant-one"), api.owners)
        assertEquals(listOf(defaultCard, otherCard), store.feeds.value["c1"])
    }

    @Test fun dismissalKeepsTheTaskAndOtherDraftsAndSurvivesReload() = kotlinx.coroutines.test.runTest {
        val original = card(extra = mapOf("draft" to "Work is in progress"))
        val sibling = card("m2", mapOf("draft" to "New progress"))
        val task = FeedItem("task", "c1", kind = "task", ref = "todo-one")
        val api = Api(CompletableDeferred(Unit), listOf(original, sibling, task))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.load("c1"); runCurrent()
        var receipt: persona.core.contracts.api.MessageDraftDismissal? = null
        val errors = mutableListOf<Throwable>()
        store.dismissMessage(original, { errors += it }) { receipt = it }
        runCurrent()
        assertEquals(listOf("m1" to "Work is in progress"), api.dismissals)
        assertTrue(api.replies.isEmpty())
        assertFalse(store.feeds.value["c1"]!!.first { it.id == "m1" }.canSend)
        assertEquals(sibling, store.feeds.value["c1"]!!.first { it.id == "m2" })
        assertEquals(task, store.feeds.value["c1"]!!.first { it.id == "task" })
        store.load("c1", whole = true); runCurrent()
        val fresh = TasksStore(api, Connection(), backgroundScope)
        fresh.load("c1"); runCurrent()
        assertFalse(fresh.feeds.value["c1"]!!.first { it.id == "m1" }.canSend)
        store.undoDismissMessage(receipt!!, { errors += it }); runCurrent()
        assertEquals(listOf("m1" to "dismissal-one"), api.undos)
        assertEquals(original, store.feeds.value["c1"]!!.first { it.id == "m1" })
        assertTrue(errors.isEmpty())
    }

    @Test fun sendAndDismissShareTheBusyGuardAndLaterUsesCardIdentity() = kotlinx.coroutines.test.runTest {
        val original = card(extra = mapOf("draft" to "Original draft"))
        val sibling = card("m2", mapOf("draft" to "Next update"))
        val api = Api(CompletableDeferred(Unit), listOf(original, sibling))
        api.dismissGate = CompletableDeferred()
        val store = TasksStore(api, Connection(), backgroundScope)
        store.load("c1"); runCurrent()
        val errors = mutableListOf<Throwable>()
        store.dismissMessage(original, { errors += it }) { }
        store.dismissMessage(original, { errors += it }) { }
        store.sendMessage(original, "Edited reply", { errors += it })
        runCurrent()
        assertEquals(listOf("m1" to "Original draft"), api.dismissals)
        assertTrue(api.replies.isEmpty())
        api.dismissGate!!.complete(Unit); runCurrent()
        assertTrue(store.busy.value.isEmpty())
        store.putOff(sibling.id)
        assertEquals(setOf("m2"), store.later.value)
        assertTrue(original.ref !in store.later.value)
        assertTrue(errors.isEmpty())
    }

    @Test fun dismissAndUndoFailuresKeepOnlyTheirCurrentDraftState() = kotlinx.coroutines.test.runTest {
        val original = card(extra = mapOf("draft" to "Original draft"))
        val api = Api(CompletableDeferred(Unit), listOf(original))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.load("c1"); runCurrent()
        val errors = mutableListOf<Throwable>()
        api.dismissError = IllegalStateException("Dismiss refused")
        store.dismissMessage(original, { errors += it }) { error("failed dismissal offered Undo") }
        runCurrent()
        assertEquals(listOf(original), store.feeds.value["c1"])
        assertTrue(store.busy.value.isEmpty())
        api.dismissError = null
        var receipt: persona.core.contracts.api.MessageDraftDismissal? = null
        store.dismissMessage(original, { errors += it }) { receipt = it }; runCurrent()
        api.undoError = IllegalStateException("Draft superseded")
        store.undoDismissMessage(receipt!!, { errors += it }); runCurrent()
        assertFalse(store.feeds.value["c1"]!!.single().canSend)
        assertTrue(store.busy.value.isEmpty())
        assertEquals(2, errors.size)
    }

    @Test fun readsCapturedBeforeDismissAndUndoCannotResurrectOrRedismiss() = kotlinx.coroutines.test.runTest {
        val original = card(extra = mapOf("draft" to "Original draft"))
        val api = Api(CompletableDeferred(Unit), listOf(original))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.load("c1"); runCurrent()
        api.feedGate = CompletableDeferred()
        store.load("c1", whole = true); runCurrent()
        var receipt: persona.core.contracts.api.MessageDraftDismissal? = null
        store.dismissMessage(original, { throw it }) { receipt = it }; runCurrent()
        api.feedGate!!.complete(Unit); runCurrent()
        assertFalse(store.feeds.value["c1"]!!.single().canSend)
        api.feedGate = CompletableDeferred()
        store.load("c1", whole = true); runCurrent()
        store.undoDismissMessage(receipt!!, { throw it }); runCurrent()
        api.feedGate!!.complete(Unit); runCurrent()
        assertEquals(original, store.feeds.value["c1"]!!.single())
    }

    @Test fun aLateUndoReceiptDoesNotOverwriteNewProgress() = kotlinx.coroutines.test.runTest {
        val original = card(extra = mapOf("draft" to "Original draft"))
        val api = Api(CompletableDeferred(Unit), listOf(original))
        val connection = Connection()
        val store = TasksStore(api, connection, backgroundScope)
        store.load("c1"); runCurrent()
        var receipt: persona.core.contracts.api.MessageDraftDismissal? = null
        store.dismissMessage(original, { throw it }) { receipt = it }; runCurrent()
        api.undoGate = CompletableDeferred()
        store.undoDismissMessage(receipt!!, { throw it }); runCurrent()
        val progress = card(extra = mapOf("draft" to "New progress must stay"))
        connection.events.emit(persona.core.contracts.AssistantFeedEvent(item = progress))
        runCurrent()
        api.undoGate!!.complete(Unit); runCurrent()
        assertEquals(progress, store.feeds.value["c1"]!!.single())
    }

    @Test fun aDraftWithABlankIsNotSent() {
        val gate = CompletableDeferred(Unit)
        val api = Api(gate, emptyList())
        val store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        val errors = mutableListOf<Throwable>()
        store.sendMessage(card(), "【预计今晚 / 几点前】修好") { errors += it }
        assertTrue(api.replies.isEmpty())
        assertTrue(errors.single().message.orEmpty().contains("【预计今晚 / 几点前】"))
    }
}
