package persona.feature.inbox

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.InboxActionsTransport
import persona.core.contracts.api.InboxItemContext
import persona.core.contracts.api.ConnectionState
import persona.core.domain.inbox.InboxItem
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json
import org.junit.Assert.*
import org.junit.Test
import persona.core.inbox.*

@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxFeatureTest {
    /** The app's strings; Robolectric runs in Simplified Chinese (robolectric.properties). */
    private val res get() = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources
    @Test fun mailConflictKeepsDraftAndRequiresNewConfirmedTarget() = runTest {
        val base = FakeInboxRepository()
        val requests = mutableListOf<ReplyRequest>()
        val repository = object : InboxRepository by base {
            override suspend fun reply(target: ReplyTarget, request: ReplyRequest) {
                requests += request
                if (requests.size == 1) throw persona.core.contracts.api.MailReplyTargetChanged()
            }
        }
        val composer = ReplyComposer(repository, ReplyTarget.Inbox("item"), backgroundScope)
        runCurrent()
        composer.update("保留这段回复")
        runCurrent()
        composer.send(persona.core.contracts.api.MailReplyTarget(7, "old"))
        runCurrent()
        assertTrue(composer.state.value.targetChanged)
        assertEquals("保留这段回复", composer.state.value.draft.text)
        assertEquals(ReplyDelivery.Ready, composer.state.value.delivery)
        assertNotEquals(requests.single().clientId, composer.state.value.draft.clientId)
        assertEquals(1, requests.size)
        composer.send(persona.core.contracts.api.MailReplyTarget(8, "new"))
        runCurrent()
        assertTrue(composer.state.value.sent)
        assertEquals(8L, requests.last().mailTarget?.uid)
        assertNotEquals(requests.first().clientId, requests.last().clientId)
    }

    @Test
    fun lostReceiptRestoresCacheFiltersAndDraftWithoutResending() = runTest {
        val repository = FakeInboxRepository()
        val storage = MemoryInboxStorage()
        val target = ReplyTarget.Inbox("item")
        val connection = MutableStateFlow(ConnectionState.Connecting)
        val owner = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val actions = object : InboxActionsTransport {
            override suspend fun collectNow() = Unit
            override suspend fun context(itemId: String) = InboxItemContext("事项上下文")
        }
        val first = InboxExperienceRepository(repository, storage, actions, connection, owner, { _, _, _ -> }, resources = res)
        repository.snapshot.value = InboxSnapshot(records = listOf(InboxRecord(InboxItem("item", title = "事项"), feedId = "feed")))
        runCurrent()
        first.savePreferences(InboxPreferences(source = "account", kind = "mention", query = "搜索"))
        val composer = first.composers.get(target, "初始草稿")
        runCurrent()
        composer.update("修改后的回复")
        runCurrent()
        composer.send()
        composer.send()
        runCurrent()
        assertEquals(1, repository.requests.size)
        assertEquals(ReplyDelivery.Unknown, composer.state.value.delivery)
        val request = repository.requests.single()
        assertEquals(request.clientId, first.readDraft(target)?.clientId)
        assertTrue(Json.encodeToString(request).contains("\"client_id\""))
        composer.send()
        runCurrent()
        assertEquals(1, repository.requests.size)
        owner.cancel()

        repository.snapshot.value = InboxSnapshot()
        val restored = InboxExperienceRepository(repository, storage, actions, connection, backgroundScope, { _, _, _ -> }, resources = res)
        runCurrent()
        assertEquals("事项", restored.snapshot.value.records.single().item.title)
        assertEquals("account", restored.preferences.first().source)
        val reopened = restored.composers.get(target)
        runCurrent()
        assertEquals(request.clientId, reopened.state.value.draft.clientId)
        assertFalse(reopened.state.value.canEdit)
        repository.messages.value = LarkConversation(messages = listOf(LarkMessage("remote", "sender", "发送者",
            parts = listOf(LarkPart.Text(request.text)), clientId = "different-id")))
        reopened.send()
        runCurrent()
        assertFalse(reopened.state.value.sent)
        repository.messages.value = repository.messages.value.copy(messages = repository.messages.value.messages.map { it.copy(clientId = request.clientId) })
        reopened.send()
        runCurrent()
        assertTrue(reopened.state.value.sent)
        assertEquals(1, repository.requests.size)
        assertEquals("", reopened.state.value.draft.text)
        assertEquals(ReplyDelivery.Sent, restored.replyStatus(target, request.clientId))
    }

    // A background refresh of a shown list does not go through loading, so the refresh count is what lets a list that
    // became empty (items expired, a source closed) replace the shown one; a fresh, empty delegate still does not.
    @Test
    fun backgroundRefreshToAnEmptyListEmptiesTheExperience() = runTest {
        val repository = FakeInboxRepository()
        val owner = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val actions = object : InboxActionsTransport {
            override suspend fun collectNow() = Unit
            override suspend fun context(itemId: String) = InboxItemContext("")
        }
        val experience = InboxExperienceRepository(repository, MemoryInboxStorage(), actions,
            MutableStateFlow(ConnectionState.Connecting), owner, { _, _, _ -> }, resources = res)
        val record = InboxRecord(InboxItem("item", title = "事项"), feedId = "feed")
        repository.snapshot.value = InboxSnapshot(records = listOf(record), refreshes = 1)
        runCurrent()
        assertEquals(listOf("item"), experience.snapshot.value.records.map { it.item.id })

        repository.snapshot.value = InboxSnapshot()
        runCurrent()
        assertEquals("an empty delegate that has not refreshed keeps the shown list",
            listOf("item"), experience.snapshot.value.records.map { it.item.id })

        repository.snapshot.value = InboxSnapshot(records = listOf(record), refreshes = 2)
        runCurrent()
        repository.snapshot.value = InboxSnapshot(refreshes = 3)
        runCurrent()
        assertEquals(emptyList<String>(), experience.snapshot.value.records.map { it.item.id })
        owner.cancel()
    }

    @Test
    fun leavingPageDoesNotCancelDraftWrite() = runTest {
        val repository = FakeInboxRepository()
        val page = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val composer = ReplyComposer(repository, ReplyTarget.Lark("feed"), page)
        runCurrent()
        val gate = CompletableDeferred<Unit>()
        repository.draftGate = gate
        composer.update("切页前的草稿")
        page.cancel()
        gate.complete(Unit)
        runCurrent()
        assertEquals("切页前的草稿", repository.draft?.text)
    }

    @Test
    fun itemBoundaryBreaksSenderGroupingAndNamesDoNotIdentifySenders() {
        fun message(id: String, sender: String, position: Long) = LarkMessage(id, sender, "发送者", position, parts = listOf(LarkPart.Text("消息")))
        val earlier = message("earlier", "sender-a", 9)
        val first = message("first", "sender-a", 10)
        val next = message("next", "sender-a", 11)
        val another = message("another", "sender-b", 12)
        assertTrue(startsInboxItem(first, earlier, 10))
        assertTrue(showsSender(first, earlier, startsInboxItem(first, earlier, 10)))
        assertFalse(startsInboxItem(next, first, 10))
        assertFalse(showsSender(next, first, false))
        assertTrue(showsSender(another, next, false))
        assertFalse(startsInboxItem(first, earlier, null))
    }
}

internal class FakeInboxRepository : InboxRepository {
    override val entries = MutableStateFlow(emptyList<InboxEntry>())
    override val snapshot = MutableStateFlow(InboxSnapshot())
    val messages = MutableStateFlow(LarkConversation())
    var draftGate: CompletableDeferred<Unit>? = null
    var draft: ReplyDraft? = null
    val requests = mutableListOf<ReplyRequest>()
    val events = mutableListOf<String>()
    override suspend fun refresh() = Unit
    override fun conversation(feedId: String, startPosition: Long?) = messages
    override suspend fun loadOlder(feedId: String) = Unit
    override suspend fun refreshConversation(feedId: String) = Unit
    override suspend fun setDone(itemId: String, done: Boolean) = Unit
    override suspend fun readDraft(target: ReplyTarget) = draft
    override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) {
        draftGate?.await()
        this.draft = draft
        events += "save"
    }
    override suspend fun reply(target: ReplyTarget, request: ReplyRequest) {
        events += "reply"
        requests += request
        if (requests.size == 1) error("Simulated transport failure")
        if (draft?.clientId == request.clientId) draft = null
    }
    override suspend fun downloadFile(file: LarkPart.File): String = error("Not used")
}

private class MemoryInboxStorage : InboxStateStorage {
    private var value: String? = null
    override suspend fun read() = value
    override suspend fun write(value: String) { this.value = value }
}
