package persona.feature.inbox

import android.content.res.Resources

import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxActionsTransport
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json
import persona.core.inbox.*

/** One instance per pairing. Its scope outlives pages and is cancelled when the pairing is discarded. */
@OptIn(ExperimentalCoroutinesApi::class)
class InboxExperienceRepository(
    private val delegate: InboxRepository,
    private val storage: InboxStateStorage,
    private val actions: InboxActionsTransport,
    override val connectionState: StateFlow<ConnectionState>,
    private val ownerScope: CoroutineScope,
    private val sendToPersona: suspend (itemId: String, action: InboxAiAction, context: String) -> Unit,
    invalidations: Flow<Unit> = emptyFlow(),
    /** The app's strings, for the errors the snapshot carries. */
    private val resources: Resources,
) : InboxRepository by delegate, InboxHandOffBarOwner {
    private val json = Json { ignoreUnknownKeys = true }
    private val lock = Mutex()
    private val sends = Mutex()
    private val current = MutableStateFlow(InboxSnapshot(loading = true))
    override val snapshot = current.asStateFlow()
    private val local = MutableStateFlow(InboxLocalState())
    override val preferences = local.map { it.preferences }.distinctUntilChanged()
    internal val composers = ReplyComposers(this, ownerScope)
    private val bars = InboxHandOffBars(ownerScope, resources, this)
    override val handOffBars = MutableStateFlow<InboxHandOffBars?>(bars)

    /** One bar per item in this pairing: its wait and send outlive the event page, never the pairing. */
    internal fun handOffBar(itemId: String): InboxHandOffBarModel = bars.get(itemId)

    private val ready = ownerScope.async {
        val document = storage.read()?.let { json.decodeFromString<InboxLocalState>(it) } ?: InboxLocalState()
        local.value = document
        current.value = InboxSnapshot(records = document.records.map { it.record() })
    }

    init {
        ownerScope.launch {
            try {
                ready.await()
                // The delegate starts empty. Only a successful refresh may replace the disk snapshot with an empty list.
                // A refresh of a shown list leaves `loading` alone, so the refresh count tells when one has completed.
                var refreshes = delegate.snapshot.value.refreshes
                delegate.snapshot.collect { value ->
                    val completed = value.refreshes > refreshes && value.error == null
                    val hasRecords = value.records.isNotEmpty() || completed
                    current.value = decorate(value.copy(records = if (hasRecords) value.records else current.value.records))
                    if (!value.loading && value.error == null && hasRecords) {
                        update { it.copy(records = value.records.map(InboxRecord::cached)) }
                    }
                    refreshes = value.refreshes
                }
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { current.update { it.copy(loading = false, error = resources.getString(R.string.inbox_local_read_failed)) } }
        }
        ownerScope.launch {
            ready.await()
            merge(connectionState.filter { it == ConnectionState.Online }.map { Unit }, invalidations).collect {
                try {
                    refresh()
                    local.value.replies.values.filter { it.delivery != ReplyDelivery.Sent }.forEach {
                        verifyReply(it.target, it.request.clientId)
                    }
                } catch (e: CancellationException) { throw e }
                catch (_: Exception) { current.update { it.copy(loading = false, error = resources.getString(R.string.inbox_sync_incomplete)) } }
            }
        }
    }

    private suspend fun update(change: (InboxLocalState) -> InboxLocalState) = lock.withLock {
        ownerScope.coroutineContext.ensureActive()
        val next = change(local.value)
        storage.write(json.encodeToString(next))
        local.value = next
        current.value = decorate(current.value)
    }

    private fun decorate(value: InboxSnapshot) = value.copy(records = value.records.map { record ->
        val keys = listOfNotNull(ReplyTarget.Inbox(record.item.id).localKey(), record.feedId?.let { ReplyTarget.Lark(it).localKey() })
        val pending = local.value.replies.values.lastOrNull { it.target.localKey() in keys && it.delivery != ReplyDelivery.Sent }
        val draft = keys.firstNotNullOfOrNull { local.value.drafts[it]?.text?.takeIf(String::isNotBlank) }
        record.copy(localStatus = when {
            pending != null -> LOCAL_UNCONFIRMED
            draft != null -> LOCAL_DRAFT + draft
            local.value.replies.values.any { it.target.localKey() in keys } -> null
            else -> record.localStatus
        })
    })

    override suspend fun refresh() {
        ready.await()
        delegate.refresh()
        val value = delegate.snapshot.value
        update { it.copy(records = value.records.map(InboxRecord::cached)) }
        current.value = decorate(value)
    }

    override suspend fun refreshNow() { ready.await(); actions.collectNow(); refresh() }
    override suspend fun savePreferences(value: InboxPreferences) { ready.await(); update { it.copy(preferences = value) } }
    override suspend fun readDraft(target: ReplyTarget): ReplyDraft? {
        ready.await()
        return local.value.drafts[target.localKey()] ?: delegate.readDraft(target)
    }
    override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) {
        ready.await()
        update { it.copy(drafts = it.drafts + (target.localKey() to draft)) }
    }
    override suspend fun replyStatus(target: ReplyTarget, clientId: String): ReplyDelivery {
        ready.await()
        return local.value.replies[clientId]?.delivery ?: ReplyDelivery.Ready
    }

    override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = sends.withLock {
        ready.await()
        val previous = local.value.replies[request.clientId]
        if (previous != null) {
            require(previous.target == target && previous.request == request)
            if (previous.delivery == ReplyDelivery.Sent || verifyReply(target, request.clientId) == ReplyDelivery.Sent) return@withLock
            error("Reply outcome is unknown")
        }
        val feed = when (target) {
            is ReplyTarget.Lark -> target.feedId
            is ReplyTarget.Inbox -> current.value.records.firstOrNull { it.item.id == target.itemId }?.feedId
        }
        // Persist uncertainty before transport: process death cannot turn it into a fresh send.
        val pending = StoredInboxReply(target, request, feed)
        update { it.copy(replies = it.replies + (request.clientId to pending)) }
        delegate.reply(target, request)
        confirm(request.clientId)
    }

    private suspend fun confirm(clientId: String) = update { state ->
        val reply = state.replies.getValue(clientId)
        val key = reply.target.localKey()
        state.copy(replies = state.replies + (clientId to reply.copy(delivery = ReplyDelivery.Sent)),
            drafts = if (state.drafts[key]?.clientId == clientId) state.drafts - key else state.drafts)
    }

    override suspend fun verifyReply(target: ReplyTarget, clientId: String): ReplyDelivery {
        ready.await()
        val reply = local.value.replies[clientId] ?: return ReplyDelivery.Ready
        require(reply.target == target || target is ReplyTarget.Lark && reply.feedId == target.feedId)
        if (reply.delivery == ReplyDelivery.Sent) return ReplyDelivery.Sent
        val feed = reply.feedId ?: return ReplyDelivery.Unknown
        // A missing echo is never proof that a send failed. Do not compare message text.
        val flow = delegate.conversation(feed, null)
        flow.first { !it.loading }
        delegate.refreshConversation(feed)
        val window = flow.first { !it.loading }
        if (window.messages.any { it.clientId == clientId && it.delivery == LarkDelivery.Sent }) {
            confirm(clientId)
            return ReplyDelivery.Sent
        }
        return ReplyDelivery.Unknown
    }

    override suspend fun askPersona(itemId: String, action: InboxAiAction) {
        ready.await()
        sendToPersona(itemId, action, actions.context(itemId).text)
    }
}
