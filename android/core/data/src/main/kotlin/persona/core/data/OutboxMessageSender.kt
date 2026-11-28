package persona.core.data

import persona.core.contracts.GroupKeys
import persona.core.contracts.HubScope
import persona.core.contracts.Item
import persona.core.contracts.MessageEcho
import persona.core.contracts.SessionKey
import persona.core.contracts.api.MessageEvidence
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.MessageTransport
import persona.core.contracts.api.MessageTransportResult
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.DraftMessageSender
import persona.core.contracts.api.DraftSnapshot
import persona.core.contracts.api.MainConversationResolver
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.PendingMainConversation
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ComposerDelivery
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.hubCodedFailure
import persona.core.domain.SetupFailureCodes
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull

/**
 * Hub-authoritative delivery of one durable outbox message. Unlike [DeliveryState], this is never inferred from manual
 * confirmation, an HTTP error's delivered hint, or the grace that turns a sent bubble into a shown one.
 *
 * The app routes Agent sends through its grouped MessageSender facade. A process-local core/data stream lets the real
 * outbox reach the repository through that unchanged facade; the pairing scope prevents cross-hub delivery.
 */
internal data class AuthoritativeMessageDelivery(
    val scope: String,
    val clientMessageId: String,
    val session: SessionKey,
    val text: String,
    val agentHandOff: InboxAgentHandOff? = null,
    /** False when the target outbox committed evidence but the originating inbox still must commit it before recording. */
    val originCommitted: Boolean = true,
)

internal object AuthoritativeMessageDeliveries {
    private val mutable = MutableSharedFlow<AuthoritativeMessageDelivery>(extraBufferCapacity = Int.MAX_VALUE)
    val events: SharedFlow<AuthoritativeMessageDelivery> = mutable
    /** Unacknowledged hand-offs only: a same-process origin reopen need not wait for its disk replay. */
    private val awaitingOrigin = LinkedHashMap<Pair<String, String>, AuthoritativeMessageDelivery>()

    /** Buffered so recording a hand-off can never hold up or change message delivery. */
    suspend fun publish(delivery: AuthoritativeMessageDelivery) {
        val handOff = delivery.agentHandOff
        val originCommitted = when {
            handOff == null -> delivery.originCommitted
            delivery.originCommitted -> true
            else -> InboxReplyJournal.commitAgentHandOffDelivery(handOff, delivery.scope)
        }
        val committed = delivery.copy(originCommitted = originCommitted)
        if (handOff != null) synchronized(awaitingOrigin) {
            val key = handOff.scope to handOff.request.operationId
            awaitingOrigin.remove(key)
            awaitingOrigin[key] = committed
            while (awaitingOrigin.size > 256) awaitingOrigin.remove(awaitingOrigin.keys.first())
        }
        mutable.emit(committed)
    }

    internal fun replay(scope: String): List<AuthoritativeMessageDelivery> = synchronized(awaitingOrigin) {
        awaitingOrigin.filterKeys { it.first == scope }.values.toList()
    }

    internal fun acknowledge(delivery: AuthoritativeMessageDelivery) {
        val handOff = delivery.agentHandOff ?: return
        synchronized(awaitingOrigin) {
            val key = handOff.scope to handOff.request.operationId
            if (awaitingOrigin[key] === delivery) awaitingOrigin.remove(key)
        }
    }
}

class OutboxMessageSender(
    private val hubScope: HubScope,
    private val store: OutboxStore,
    private var transport: MessageTransport,
    parentScope: CoroutineScope,
    private val reviewTimeoutMillis: Long = 30_000,
    private val reconciliationTimeoutMillis: Long = 3_000,
    /** How long a delivered message waits for its copy in the transcript before its bubble goes anyway (iOS, web). */
    private val sentGraceMillis: Long = 60_000,
    // Last: callers pass the clock as a trailing lambda.
    private val now: () -> Long = System::currentTimeMillis,
) : MessageSender, DraftMessageSender, MainConversationSender {
    private val job = SupervisorJob(parentScope.coroutineContext[Job])
    private val scope = CoroutineScope(parentScope.coroutineContext + job)
    private val receiptState = MessageSendReceiptState(now)
    override val sendReceipts = receiptState.receipts
    private var watchingSend: suspend (SessionKey, suspend () -> MessageTransportResult) -> MessageTransportResult = { _, send -> send() }
    private var receiptsConnected = false
    private val wake = Channel<Unit>(Channel.CONFLATED)
    private val lifecycle = Mutex()
    private val processing = Mutex()
    private val initialized = CompletableDeferred<Unit>()
    private val reconcilers = mutableListOf<OutboxReconciler>()
    /**
     * Messages that came back waiting while the link was up, each with the code it fails with ([stall]). Memory only,
     * under its own lock; the link going down starts every run again. Declared before `init`: its readiness collector
     * clears them.
     */
    private val stalls = mutableMapOf<String, Stall>()
    /** The one retry of the stalled messages, due in [StallRetryMillis]. */
    private var stallRetry: Job? = null
    @Volatile private var mainResolver: MainConversationResolver? = null
    /** Transcript items that already showed a message (observeTranscript): the same item never shows a second. */
    private val claimed = mutableSetOf<Pair<SessionKey, String>>()
    /** One process-local publication per client id; the repository's durable tombstone covers process recreation. */
    private val publishedDeliveries = mutableSetOf<String>()
    /** A publication interrupted at an unknown store point is recovered by the origin, not blocked on that store again. */
    private val interruptedDeliveries = mutableSetOf<String>()
    /** A composer owns these visible messages until its actual HTTP result or cancellation arrives. Access under [lifecycle]. */
    private val composerAttempts = mutableSetOf<String>()
    private fun composerActive(id: String) = synchronized(composerAttempts) { id in composerAttempts }
    private fun claimComposer(id: String) = synchronized(composerAttempts) { composerAttempts.add(id) }
    private fun releaseComposer(id: String) = synchronized(composerAttempts) { composerAttempts.remove(id) }
    /** Whether a session is at work (connectActivity): its turn may write a delivered message only when it ends. */
    @Volatile private var busy: (SessionKey) -> Boolean = { false }

    override val writtenAt = store.entries.map { entries ->
        entries.associate { it.clientMessageId to it.createdAt }
    }.stateIn(scope, SharingStarted.Eagerly, emptyMap())

    override val outbox = store.entries.map { entries ->
        entries.filter { it.state !in hidden }.map {
            OutboxMessage(it.clientMessageId, it.session, it.text, it.state.delivery, it.error, it.mainTarget, it.createdAt, it.opId,
                it.reason)
        }
    }.stateIn(scope, SharingStarted.Eagerly, emptyList())

    init {
        require(reviewTimeoutMillis > 0 && reconciliationTimeoutMillis > 0)
        scope.launch {
            store.recoverInterrupted()
            initialized.complete(Unit)
            wake.trySend(Unit)
            for (ignored in wake) processing.withLock { drain() }
        }
        scope.launch {
            transport.readyToSend.collect { if (it) wake.trySend(Unit) else clearStalls() }
        }
        scope.launch {
            val deadlines = mutableMapOf<String, Job>()
            store.entries.collect { entries ->
                val unresolved = entries.filter { it.state in reviewable }.map { it.clientMessageId }.toSet()
                deadlines.keys.toList().filter { it !in unresolved }.forEach { deadlines.remove(it)?.cancel() }
                for (id in unresolved) if (id !in deadlines) {
                    deadlines[id] = scope.launch {
                        delay(reviewTimeoutMillis)
                        // An Unknown row can belong to a composer retry that is still in flight. Use the same
                        // ownership guard as automatic sending: a deadline must not turn that active operation
                        // into NeedsConfirmation and force the composer to fail its durable start.
                        transitionAutomatic(id, reviewable, OutboxState.NeedsConfirmation)
                        wake.trySend(Unit)
                    }
                }
            }
        }
        scope.launch {
            // A delivered message whose copy never shows (a /compact becomes a notice, Codex writes its queue when the
            // turn ends, other words) would wait for ever: after the grace, and once its session is not at work (for
            // up to 15 more minutes), its bubble goes, as on iOS (AppModel.settleSend) and the web.
            val waits = mutableMapOf<String, Job>()
            store.entries.collect { entries ->
                val sent = entries.filter { it.state == OutboxState.Sent }.associateBy { it.clientMessageId }
                waits.keys.toList().filter { it !in sent }.forEach { waits.remove(it)?.cancel() }
                for ((id, entry) in sent) if (id !in waits) {
                    waits[id] = scope.launch {
                        delay(sentGraceMillis)
                        var checks = 0
                        while (checks++ < 90 && busy(entry.session)) delay(10_000)
                        store.transition(id, setOf(OutboxState.Sent), OutboxState.Shown)
                    }
                }
            }
        }
    }

    /**
     * Sessions holding a delivered message not yet seen in their transcript. Only Sent: it settles within the grace and
     * busy wait, while undecided states can last until the user acts and would keep their chats syncing.
     */
    val awaitingTranscript: Flow<Set<SessionKey>> = store.entries
        .map { entries -> entries.filter { it.state == OutboxState.Sent }.mapTo(mutableSetOf()) { it.session } }
        .distinctUntilChanged()

    /** Tells the outbox which sessions are at work (the pairing's session list). */
    fun connectActivity(busy: (SessionKey) -> Boolean) { this.busy = busy }

    override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) =
        enqueue(OutboxEntry(clientMessageId, session, text, clearDraft, createdAt = now()))

    override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?, launcher: String?, model: String?) =
        send(session, text, clientMessageId, clearDraft, launcher, model, null)

    override suspend fun send(
        session: SessionKey, text: String, clientMessageId: String, clearDraft: String?, launcher: String?, model: String?, via: String?,
    ) = enqueue(OutboxEntry(clientMessageId, session, text, clearDraft, createdAt = now(), launcher = launcher, model = model, via = via))

    override suspend fun sendApplyingSwitch(session: SessionKey, text: String, clientMessageId: String) =
        enqueue(OutboxEntry(clientMessageId, session, text, createdAt = now(), applySwitch = true))

    private suspend fun enqueue(entry: OutboxEntry) = lifecycle.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        require(entry.text.isNotBlank() && entry.clientMessageId.isNotBlank())
        require(entry.session.machineId.isNotBlank() && entry.session.sessionId.isNotBlank())
        val handOff = currentCoroutineContext()[InboxAgentHandOffContext]?.handOff
        val stored = if (handOff == null) entry else {
            require(handOff.request.operationId == entry.clientMessageId && handOff.request.text == entry.text &&
                GroupKeys.rawKey(handOff.session) == GroupKeys.rawKey(entry.session)) {
                "Hand-off does not match its outbox message"
            }
            entry.copyForDelivery(agentHandOff = handOff)
        }
        store.enqueue(stored)
        if (transport.readyToSend.value) receiptState.preparing(stored.session, stored.clientMessageId, false)
        wake.trySend(Unit)
        Unit
    }

    /** Not sent yet, or refused (by the computer, or confirmed as never delivered): nothing reached the agent. */
    override suspend fun cancel(clientMessageId: String): Boolean =
        change(clientMessageId, dispatchable + OutboxState.Refused, OutboxState.Cancelled)

    override fun restore(message: OutboxMessage) {
        val to = when (message.state) {
            DeliveryState.Queued -> OutboxState.Queued
            DeliveryState.WaitingForNetwork -> OutboxState.WaitingForNetwork
            DeliveryState.Refused, DeliveryState.Rejected -> OutboxState.Refused
            else -> return
        }
        // The sender's own scope: the toast's Undo can come after the chat page that showed it has gone.
        scope.launch { if (job.isActive) change(message.clientMessageId, setOf(OutboxState.Cancelled), to, message.error, message.reason) }
    }

    override suspend fun discard(clientMessageId: String): Boolean =
        change(clientMessageId, undecided, OutboxState.Cancelled)

    override suspend fun retry(clientMessageId: String): Boolean {
        val entries = store.entries.first()
        val entry = entries.firstOrNull { it.clientMessageId == clientMessageId } ?: return false
        // Still on its way: the queue looks again now rather than at its next wake (iOS's Retry Now on a waiting message).
        if (entry.state in dispatchable) {
            wake.trySend(Unit)
            return true
        }
        if (entry.state == OutboxState.NeedsConfirmation) {
            return retryNeedsConfirmation(entry, entries)
        }
        return change(clientMessageId, setOf(OutboxState.Refused), OutboxState.Queued)
    }

    private suspend fun retryNeedsConfirmation(entry: OutboxEntry, entries: List<OutboxEntry>): Boolean {
        check(job.isActive) { "Pairing scope is closed" }
        val id = entry.clientMessageId
        val evidence = if (transport.readyToSend.value) {
            try {
                withTimeoutOrNull(reconciliationTimeoutMillis) {
                    transport.reconcile(
                        request(entry),
                        entry.createdAt,
                        entries.count { it.session == entry.session && it.text == entry.text } == 1,
                    )
                }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                null
            }
        } else null
        return when (evidence) {
            MessageEvidence.Shown -> {
                val (moved, publish) = lifecycle.withLock {
                    check(job.isActive) { "Pairing scope is closed" }
                    val current = store.entries.first().firstOrNull { it.clientMessageId == id }
                        ?: return@withLock (false to false)
                    if (current.operationId != entry.operationId) return@withLock (false to false)
                    val transitioned = store.transition(id, reconcilable, OutboxState.Shown)
                    if (transitioned) wake.trySend(Unit)
                    transitioned to (transitioned || current.state == OutboxState.Shown)
                }
                if (publish) publishAuthoritative(entry)
                moved
            }
            is MessageEvidence.Receipt -> {
                if (evidence.result.provesServerReceived()) {
                    val settled = lifecycle.withLock {
                        check(job.isActive) { "Pairing scope is closed" }
                        settleLocked(entry, setOf(OutboxState.NeedsConfirmation), evidence.result)
                    }
                    if (settled.publish) publishAuthoritative(entry)
                    if (settled.moved) wake.trySend(Unit)
                    settled.moved
                } else {
                    // The hub finalized this opId as failed or unknown; re-POSTing it would replay 202 unknown forever.
                    // The user explicitly confirmed resend, so rotate to a fresh opId and requeue.
                    lifecycle.withLock {
                        check(job.isActive) { "Pairing scope is closed" }
                        val current = store.entries.first().firstOrNull { it.clientMessageId == id }
                            ?: return@withLock false
                        if (current.state != OutboxState.NeedsConfirmation || current.operationId != entry.operationId) {
                            return@withLock false
                        }
                        store.transition(id, setOf(OutboxState.NeedsConfirmation), OutboxState.Queued, null, newOperationId()).also {
                            if (it) wake.trySend(Unit)
                        }
                    }
                }
            }
            null -> {
                // 404, pending, or offline: keep the same opId so hub deduplication prevents duplicates.
                lifecycle.withLock {
                    check(job.isActive) { "Pairing scope is closed" }
                    val current = store.entries.first().firstOrNull { it.clientMessageId == id }
                        ?: return@withLock false
                    if (current.state != OutboxState.NeedsConfirmation || current.operationId != entry.operationId) {
                        return@withLock false
                    }
                    store.transition(id, setOf(OutboxState.NeedsConfirmation), OutboxState.Queued).also {
                        if (it) wake.trySend(Unit)
                    }
                }
            }
        }
    }

    override suspend fun retryRefused(session: SessionKey, code: String): Int = lifecycle.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        val candidates = store.entries.first().filter { it.session == session && it.state == OutboxState.Refused && it.error == code }
        var changed = 0
        for (candidate in candidates) {
            // The conditional transition rechecks after the snapshot: a newly uncertain or delivered message never goes again.
            val current = store.entries.first().firstOrNull { it.clientMessageId == candidate.clientMessageId }
            if (current?.session == session && current.state == OutboxState.Refused && current.error == code &&
                store.transition(candidate.clientMessageId, setOf(OutboxState.Refused), OutboxState.Queued)) changed++
        }
        if (changed > 0) wake.trySend(Unit)
        changed
    }

    override suspend fun confirm(clientMessageId: String, delivered: Boolean): Boolean = change(
        clientMessageId, undecided,
        if (delivered) OutboxState.Sent else OutboxState.Refused,
        opId = if (delivered) null else newOperationId())

    /** [reason]: the hub's sentence for the refusal [error], which Undo puts back with it. */
    private suspend fun change(
        id: String, from: Set<OutboxState>, to: OutboxState, error: String? = null, reason: String? = null, opId: String? = null,
    ): Boolean = lifecycle.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        when {
            reason != null -> store.transition(id, from, to, error, opId, reason)
            opId != null -> store.transition(id, from, to, error, opId)
            else -> store.transition(id, from, to, error)
        }.also {
            if (it) {
                if (to == OutboxState.Cancelled) receiptState.discard(id)
                wake.trySend(Unit)
            }
        }
    }

    /** Install the pairing's receipt consumer before opening its socket or exposing sending to pages. */
    fun connectSendReceipts(
        connection: persona.core.contracts.api.HubConnection,
        sessions: kotlinx.coroutines.flow.StateFlow<List<persona.core.contracts.Session>>,
        watch: suspend (SessionKey, suspend () -> MessageTransportResult) -> MessageTransportResult = { _, send -> send() },
    ) {
        check(!receiptsConnected)
        receiptsConnected = true
        val trust = Any()
        var trustedHello = connection.currentHello.value
        fun currentHello(): persona.core.contracts.api.SocketHello? {
            val current = connection.currentHello.value
            if (current !== trustedHello) {
                receiptState.reconnect()
                trustedHello = current
            }
            return current
        }
        watchingSend = { key, send ->
            if (synchronized(trust) { currentHello()?.hello?.messageSendState == 1 }) watch(key, send) else send()
        }
        scope.launch(start = kotlinx.coroutines.CoroutineStart.UNDISPATCHED) {
            connection.currentHello.collect { synchronized(trust) { currentHello() } }
        }
        scope.launch(start = kotlinx.coroutines.CoroutineStart.UNDISPATCHED) {
            connection.events.collect { event -> when (event) {
                is persona.core.contracts.MessageSendStateEvent -> synchronized(trust) {
                    // A feature string or cached hello cannot establish this hub-owned frame's version or socket epoch.
                    val hello = currentHello()
                    if (hello?.hello?.messageSendState == 1 && event.socketGeneration == hello.generation) receiptState.event(event)
                }
                is persona.core.contracts.DeltaEvent -> if (event.turnFinished) receiptState.finishTurn(SessionKey(event.machineId, event.sessionId))
                is persona.core.contracts.HookEvent -> if (event.event == "Stop") receiptState.finishTurn(SessionKey(event.machineId, event.sessionId))
                else -> Unit
            } }
        }
        scope.launch(start = kotlinx.coroutines.CoroutineStart.UNDISPATCHED) { sessions.collect(receiptState::sessions) }
        scope.launch {
            connection.state.collect { if (it != persona.core.contracts.api.ConnectionState.Online) receiptState.reconnect() }
        }
        scope.launch { while (job.isActive) { delay(60_000); receiptState.expire() } }
    }

    /** Looks at the queue again: messages were written into the store directly (GroupMerge.kt). */
    internal fun nudge() { wake.trySend(Unit) }

    /** Called before the transport releases sending after connection catch-up. */
    suspend fun onReady() {
        initialized.await()
        processing.withLock {
            reconcilers.forEach { it.onReady() }
            reconcile()
            // The main conversation may have moved while this pairing was away (another machine, another device).
            checkMain()
        }
    }

    /** Register a scope-owned queue without changing public message dispatch. */
    suspend fun connectReconciler(reconciler: OutboxReconciler) = processing.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        reconcilers += reconciler
    }

    /** Install the resolver owned by this pairing before exposing the main sender to features. */
    suspend fun connectMainConversation(resolver: MainConversationResolver) = lifecycle.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        check(mainResolver == null || mainResolver === resolver) { "Main resolver already connected" }
        mainResolver = resolver
        wake.trySend(Unit)
        Unit
    }

    /** Install one composer decorator without replacing the original transport's readiness gate. */
    suspend fun connectComposer(factory: (MessageTransport) -> ChatComposerService): ChatComposerService = processing.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        val composer = factory(transport)
        require(composer is MessageTransport)
        transport = composer
        composer
    }

    /**
     * Manual refusal retries must update the durable outbox as well as the composer overlay. [opId] is the operation
     * id the retry goes under (a new one: the refused one would be answered from its first try). [result] is the actual
     * transport outcome, or null while sending or when cancellation leaves it unknown; only explicit acceptance is
     * authoritative hand-off delivery. In particular, a Sent overlay can instead carry an HTTP delivered=yes hint.
     */
    suspend fun beginComposerRetry(id: String, opId: String) {
        // The claim must precede the lock: a suspended automatic write checks it after committing and then yields.
        // Once that write finishes, this retry re-reads and durably binds its own operation before any HTTP call.
        check(claimComposer(id)) { "A composer retry is already in flight" }
        try {
            lifecycle.withLock { beginComposerRetryLocked(id, opId) }
        } catch (error: Throwable) {
            releaseComposer(id)
            throw error
        }
    }

    /** Under [lifecycle], with this composer claimed by the HTTP begin callback or the legacy record callback below. */
    private suspend fun beginComposerRetryLocked(id: String, opId: String) {
        check(job.isActive) { "Pairing scope is closed" }
        check(composerActive(id) || claimComposer(id)) { "A composer retry is already in flight" }
        val entry = store.entries.first().firstOrNull { it.clientMessageId == id } ?: error("Message state changed")
        // An automatic attempt may have moved the refused row to Sending but not finished yet. Bind this manual
        // attempt before its HTTP call, so that old attempt cannot start another under its own operation id.
        // An automatic write may commit Unknown after this claim was published but before it can yield the lock.
        // This already-claimed retry owns the next state: restore its active Sending row and bind its own operation.
        // Never start over any other state, particularly a transcript's Shown or a completion's Sent result.
        val from = setOf(OutboxState.Refused, OutboxState.Sending, OutboxState.Unknown)
        val moved = store.transition(id, from, OutboxState.Sending, error = null, opId = opId)
        if (!moved) {
            val current = store.entries.first().firstOrNull { it.clientMessageId == id }
            check(current?.state == OutboxState.Sending && current.operationId == opId) {
                "Message state changed: state=${current?.state} operation=${current?.operationId} requested=$opId"
            }
        }
        receiptState.begin(request(entry).copy(opId = opId), entry.mainTarget)
    }

    suspend fun recordComposerDelivery(
        id: String,
        delivery: ComposerDelivery,
        opId: String? = null,
        result: MessageTransportResult? = null,
    ) = lifecycle.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        if (delivery.state == DeliveryState.Sending && result == null && opId != null && !composerActive(id)) {
            beginComposerRetryLocked(id, opId)
        }
        val entry = store.entries.first().firstOrNull { it.clientMessageId == id } ?: error("Message state changed")
        val starting = delivery.state == DeliveryState.Sending && result == null
        if (starting) {
            check(composerActive(id) || claimComposer(id)) { "A composer retry is already in flight" }
        } else if (opId != null) {
            releaseComposer(id)
        }
        if (!starting && opId != null && opId != entry.operationId) return@withLock
        if (!starting && entry.state !in awaitingReceipt) {
            // A transcript or receipt can settle the same operation before the HTTP call returns.
            if (entry.state in settled && result == MessageTransportResult.Accepted) publishAuthoritative(entry)
            return@withLock
        }
        val attempt = request(entry).copy(opId = opId ?: entry.operationId)
        val state = if (result == MessageTransportResult.Kept) OutboxState.Kept
            else OutboxState.entries.first { it.delivery == delivery.state }
        val from = if (starting) setOf(OutboxState.Refused, OutboxState.Sending) else awaitingReceipt
        // Refused again: nothing was delivered under that id either, so a later retry takes another.
        val next = when {
            // Bind the live composer POST to its own operation id. Its completion must be distinguishable from
            // a late reply to an older attempt while the visible client id stays the same.
            starting -> opId
            state == OutboxState.Refused -> newOperationId()
            state == OutboxState.Sending -> opId
            else -> null
        }
        val moved = if (next != null) store.transition(id, from, state, delivery.refusal?.code, next)
            else store.transition(id, from, state, delivery.refusal?.code)
        if (starting && !moved) {
            // A catch-up can observe the active composer row between its state and operation-id writes.
            // It is the same attempt only when both already match; otherwise this is not a retry to repeat.
            val current = store.entries.first().firstOrNull { it.clientMessageId == id }
            check(current?.state == OutboxState.Sending && current.operationId == opId) { "Message state changed" }
        } else check(moved) { "Message state changed" }
        // Composer retries bypass attempt(), but their fresh operation still belongs to this same visible message.
        if (state == OutboxState.Sending) receiptState.begin(attempt, entry.mainTarget)
        else receiptState.result(attempt, when (state) {
            OutboxState.Kept, OutboxState.Held -> MessageTransportResult.Kept
            OutboxState.Sent, OutboxState.Shown -> MessageTransportResult.Accepted
            else -> MessageTransportResult.NoReceipt
        })
        if (result == MessageTransportResult.Accepted) publishAuthoritative(entry)
    }

    /** Attachments need the same durably bound target before uploading machine-local files. */
    suspend fun prepareMainConversation(via: String? = null): SessionKey? = processing.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        if (transport.readyToSend.value) (resolveMain(via) as? MainTarget.Found)?.session else null
    }

    override suspend fun sendDraft(session: SessionKey, draft: DraftSnapshot, clientMessageId: String) = lifecycle.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        require(draft.scope == hubScope && draft.text.isNotBlank() && clientMessageId.isNotBlank())
        require(session.machineId.isNotBlank() && session.sessionId.isNotBlank())
        store.enqueueDraft(OutboxEntry(clientMessageId, session, draft.text, createdAt = now()), draft)
        if (transport.readyToSend.value) receiptState.preparing(session, clientMessageId, false)
        wake.trySend(Unit)
        Unit
    }

    override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) =
        sendToMain(text, clientMessageId, draft, null)

    override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?, via: String?) = lifecycle.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        require(text.isNotBlank() && clientMessageId.isNotBlank())
        require(draft == null || draft.scope == hubScope)
        store.enqueueDraft(OutboxEntry(clientMessageId, PendingMainConversation, text,
            createdAt = now(), mainTarget = true, via = via), draft)
        if (transport.readyToSend.value) receiptState.preparing(PendingMainConversation, clientMessageId, true)
        wake.trySend(Unit)
        Unit
    }

    // Under the processing lock: a drain that already read the old binding finishes before it goes.
    override suspend fun mainReplaced(previous: SessionKey) = processing.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        store.forgetMainConversation(previous)
        wake.trySend(Unit)
        Unit
    }

    override suspend fun mainChanged() = processing.withLock {
        check(job.isActive) { "Pairing scope is closed" }
        checkMain()
        Unit
    }

    /**
     * The hub decides which conversation is the main one: a binding it no longer names (moved with the assistant to
     * another machine, replaced from another device) goes, and the next message resolves the main conversation again.
     * Kept when the hub cannot be asked, and while a creation is claimed (no binding yet).
     */
    private suspend fun checkMain() {
        val bound = store.mainConversation() ?: return
        val resolver = mainResolver ?: return
        // Boxed: a lookup that timed out (null) is not the hub saying there is none.
        val current = try {
            withTimeoutOrNull(reconciliationTimeoutMillis) { Result.success(resolver.findMainConversation()) } ?: return
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            return
        }
        if (current.getOrNull() == bound) return
        store.forgetMainConversation(bound)
        wake.trySend(Unit)
    }

    /**
     * The session's transcript holds [items]: authoritative (from the hub), never an optimistic local echo. A message
     * sent from here that is among them is shown, and its bubble goes. An item the hub tagged with a client message id
     * shows that message only, whatever its kind (a /compact becomes a notice). Untagged, Claude Code keeps its own id
     * for the message, so it is found by its words ([MessageEcho]) unless the runtime kept the client message id. One
     * transcript item shows one message (the oldest it can be), so a second identical message stays until its own copy
     * arrives.
     */
    suspend fun observeTranscript(session: SessionKey, items: List<Item>) {
        receiptState.transcript(session, items)
        // Not the `outbox` flow: it lags the store, and an echo that beats it would be passed over.
        if (items.none(MessageEcho::candidate)) return
        lifecycle.withLock {
            check(job.isActive) { "Pairing scope is closed" }
            val pending = store.entries.first().filter { it.session == session && it.state in catchUpStates }
                // An untagged copy still belongs to the oldest unsettled message; Shown is only the fallback.
                .sortedWith(compareBy<OutboxEntry> { it.state == OutboxState.Shown }.thenBy { it.createdAt })
                .toMutableList()
            for (item in items) {
                if (pending.isEmpty()) break
                if (!MessageEcho.candidate(item) || (session to item.id) in claimed) continue
                val entry = pending.firstOrNull { MessageEcho.carries(item, it.clientMessageId) ||
                    it.opId?.let { op -> MessageEcho.carries(item, op) } == true }
                    ?: pending.firstOrNull { it.state != OutboxState.Held && MessageEcho.arrived(it.text, it.createdAt, item) } ?: continue
                pending.remove(entry)
                val moved = store.transition(entry.clientMessageId, reconcilable, OutboxState.Shown)
                // A restored Shown row is only display state; this newly observed transcript item is fresh evidence.
                if (moved || entry.state == OutboxState.Shown) {
                    claimed += session to item.id
                    publishAuthoritative(entry)
                    wake.trySend(Unit)
                }
            }
        }
    }

    suspend fun close() = lifecycle.withLock { job.cancelAndJoin() }

    private suspend fun reconcile() {
        // Bound the whole catch-up so unresolved entries cannot hold the sending gate forever.
        withTimeoutOrNull(reconciliationTimeoutMillis) {
            val entries = store.entries.first()
            // Startup and group adoption already turn interrupted sends into Unknown. A row still Sending
            // belongs to a live POST; another message or a reconnect can wake this drain while a composer retry runs.
            for (entry in entries.filter { it.state in catchUpStates && it.state != OutboxState.Sending }) {
                if (entry.state in reviewable) store.transition(entry.clientMessageId, reviewable, OutboxState.Verifying)
                val evidence = try {
                    transport.reconcile(request(entry), entry.createdAt,
                        entry.state != OutboxState.Held && entries.count { it.session == entry.session && it.text == entry.text } == 1)
                } catch (cancelled: CancellationException) { throw cancelled }
                catch (_: Exception) { null }
                when (evidence) {
                    MessageEvidence.Shown -> {
                        val moved = store.transition(entry.clientMessageId, reconcilable, OutboxState.Shown)
                        // Fresh validated evidence still counts when a restored row was already Shown.
                        if (moved || entry.state == OutboxState.Shown) publishAuthoritative(entry)
                    }
                    is MessageEvidence.Receipt -> {
                        // A late success can settle manual review; an unknown receipt must keep that choice visible.
                        val from = if (evidence.result.provesServerReceived())
                            reviewable + OutboxState.NeedsConfirmation else reviewable
                        settle(entry, from, evidence.result)
                    }
                    null -> settle(entry, setOf(OutboxState.Verifying), MessageTransportResult.NoReceipt)
                }
            }
        }
        // Timeout or an interrupted lookup must remain visible and eligible for manual review.
        store.entries.first().filter { it.state == OutboxState.Verifying }.forEach { entry ->
            if (!composerActive(entry.clientMessageId)) settle(entry, setOf(OutboxState.Verifying), MessageTransportResult.NoReceipt)
        }
    }

    private suspend fun drain() {
        if (!transport.readyToSend.value) {
            store.entries.first().filter { it.state == OutboxState.Queued }.forEach {
                receiptState.stopPreparing(it.clientMessageId)
                store.transition(it.clientMessageId, setOf(OutboxState.Queued), OutboxState.WaitingForNetwork)
            }
            return
        }
        store.entries.first().filter { it.state in dispatchable }.forEach {
            receiptState.preparing(it.session, it.clientMessageId, it.mainTarget, it.operationId)
        }
        reconcile()
        for (queued in store.entries.first()) {
            var entry = queued
            if (!transport.readyToSend.value) return
            // The composer owns its manual retry until the actual callback. An automatic catch-up/send must not
            // start another attempt under the visible id and overwrite that result (including cancellation/Unknown).
            if (composerActive(entry.clientMessageId)) continue
            val allowed = if (transport.supportsClientOps) dispatchable + OutboxState.Unknown else dispatchable
            if (entry.mainTarget && entry.session == PendingMainConversation) {
                if (entry.state !in dispatchable) continue
                val target = when (val main = resolveMain(entry.via)) {
                    is MainTarget.Found -> main.session
                    // The hub said why it cannot start (no computer, no Claude Code…): nothing was started, so the
                    // message is not queued behind it (docs/assistant/onboarding.md 4.4); the user fixes the step and retries.
                    is MainTarget.Refused -> {
                        receiptState.stopPreparing(entry.clientMessageId)
                        store.transition(entry.clientMessageId, dispatchable, OutboxState.Refused, main.code)
                        unstall(entry.clientMessageId)
                        continue
                    }
                    is MainTarget.NotYet -> {
                        receiptState.stopPreparing(entry.clientMessageId)
                        if (store.transition(entry.clientMessageId, dispatchable, OutboxState.WaitingForNetwork,
                                "main_not_ready")) stall(entry.clientMessageId, main.code)
                        return
                    }
                }
                entry = entry.copyForDelivery(session = target)
            }
            if (entry.state !in allowed) continue
            if (!transitionAutomatic(entry.clientMessageId, allowed, OutboxState.Sending)) continue
            var result = attempt(entry)
            // One immediate retry is safe only when the hub durably deduplicates this exact id. A manual retry can
            // start while that first attempt is in flight; neither its state nor its operation may be adopted here.
            if (result == MessageTransportResult.NoReceipt && transport.supportsClientOps && transport.readyToSend.value) {
                if (!transitionAutomatic(entry.clientMessageId, setOf(OutboxState.Sending), OutboxState.Unknown)) continue
                if (!transitionAutomatic(entry.clientMessageId, setOf(OutboxState.Unknown), OutboxState.Sending)) continue
                result = attempt(entry)
            }
            if (settle(entry, setOf(OutboxState.Sending), result) == OutboxState.WaitingForNetwork) return
        }
    }

    /** If a composer claims during a suspended write, keep its operation authoritative. */
    private suspend fun transitionAutomatic(id: String, from: Set<OutboxState>, to: OutboxState): Boolean = lifecycle.withLock {
        if (composerActive(id)) return@withLock false
        if (!store.transition(id, from, to)) return@withLock false
        if (!composerActive(id)) return@withLock true
        // The manual claim is already visible but its durable start is queued behind this lock. An automatic
        // Unknown settlement, the same attempt's immediate Unknown→Sending retry, and a review deadline moving
        // Unknown/Verifying to NeedsConfirmation all belong to the old operation. Restore only an unsettled row
        // written by that automatic transition with its current operation, never a Sent/Shown result.
        // In particular, a manual claim during the second write must not let the old operation replace its opId.
        val current = store.entries.first().firstOrNull { it.clientMessageId == id }
        if (current?.state == OutboxState.Sending || current?.state == OutboxState.Unknown ||
            (to == OutboxState.NeedsConfirmation && current?.state == OutboxState.NeedsConfirmation)
        ) {
            store.transition(id, setOf(current.state), OutboxState.Sending, current.error, current.operationId)
        }
        false
    }

    /**
     * Records [result] for the message's current operation id. When the hub said nothing was delivered, that id is
     * spent (the hub answers it again from this try: 202 unknown, the stored 409), so the next attempt takes a new one,
     * as on iOS. Without an answer the id stays: the hub sends a repeated id once.
     */
    private suspend fun settle(entry: OutboxEntry, from: Set<OutboxState>, result: MessageTransportResult): OutboxState {
        val settled = lifecycle.withLock { settleLocked(entry, from, result) }
        if (settled.publish) publishAuthoritative(entry)
        return settled.state
    }

    private data class Settlement(val state: OutboxState, val publish: Boolean, val moved: Boolean = false)

    /** Under [lifecycle]: the operation check and its durable write cannot interleave with a composer or transcript. */
    private suspend fun settleLocked(entry: OutboxEntry, from: Set<OutboxState>, result: MessageTransportResult): Settlement {
        val id = entry.clientMessageId
        // A late Accepted receipt is new evidence even after manual confirmation or an earlier settlement. Do not move
        // Shown back to Sent (or manufacture a same-state transition): publish that evidence once for this sender.
        if (result == MessageTransportResult.Accepted && entry.state in settled) {
            unstall(id)
            return Settlement(entry.state, publish = true)
        }
        val state = classify(result)
        val failure = result as? MessageTransportResult.HubError
        val error = failure?.let { it.code ?: it.error }
        // The hub's own sentence for a refusal, kept with its code: the chat shows it for a code it has no words for
        // (agy open in a terminal, #16531), as iOS and the web do.
        val reason = failure?.takeIf { state == OutboxState.Refused }?.hubWords()
        val current = store.entries.first().firstOrNull { it.clientMessageId == id } ?: return Settlement(state, publish = false)
        // The operation that started this attempt may have been superseded by the composer's fresh retry id.
        // Its late result must not write into the new attempt or change the visible message back to Sending.
        if (current.operationId != entry.operationId) return Settlement(current.state, publish = false)
        if (current.state in settled) {
            // An older transport result, including a timeout or a generic failure, cannot erase a message the hub
            // already showed. Only fresh authoritative evidence may settle it further; it never regresses to Unknown.
            if (result != MessageTransportResult.Accepted) return Settlement(current.state, publish = false)
        }
        if (state == OutboxState.Sending) {
            // A composer completion is newer than an automatic attempt that was already in flight. Do not let
            // that older attempt put an already settled visible message back into Sending.
            if (current.state in settled) return Settlement(current.state, publish = false)
        }
        val moved = when {
            failure?.notDelivered() != true -> store.transition(id, from, state, error)
            reason != null -> store.transition(id, from, state, error, newOperationId(), reason)
            else -> store.transition(id, from, state, error, newOperationId())
        }
        val publish = result == MessageTransportResult.Accepted && (moved || store.entries.first().any {
            it.clientMessageId == id && it.state in settled
        })
        if (moved && state == OutboxState.WaitingForNetwork) stall(id, error) else unstall(id)
        return Settlement(state, publish, moved)
    }

    private suspend fun publishAuthoritative(entry: OutboxEntry) {
        val first = synchronized(publishedDeliveries) { publishedDeliveries.add(entry.clientMessageId) }
        if (!first) return
        val recovering = synchronized(interruptedDeliveries) { interruptedDeliveries.remove(entry.clientMessageId) }
        var published = false
        try {
            var storeCommitted = false
            val persisted = if (recovering) {
                entry.copyForDelivery(agentHandOff = entry.agentHandOff?.copy(delivered = true))
            } else try {
                store.persistAgentHandOffDelivery(entry).also { storeCommitted = true }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                // Recording metadata must never change an already authoritative message result.
                entry.copyForDelivery(agentHandOff = entry.agentHandOff?.copy(delivered = true))
            }
            // Recovery means the store outcome is unknown, not that its origin committed. Publish repairs it idempotently.
            val originCommitted = storeCommitted && store.agentHandOffOriginCommitted(persisted)
            // Reconciliation has a short timeout. Once fresh evidence retries an interrupted commit, finish that small
            // durable repair and its publication rather than cancelling between the journal write and the event.
            if (recovering) withContext(NonCancellable) {
                AuthoritativeMessageDeliveries.publish(persisted.delivery(originCommitted))
            } else {
                AuthoritativeMessageDeliveries.publish(persisted.delivery(originCommitted))
            }
            published = true
        } finally {
            // Cancellation or another publication failure must not consume the only later chance to recover.
            if (!published) {
                synchronized(publishedDeliveries) { publishedDeliveries.remove(entry.clientMessageId) }
                if (entry.agentHandOff != null) synchronized(interruptedDeliveries) {
                    interruptedDeliveries += entry.clientMessageId
                }
            }
        }
    }

    private fun OutboxEntry.delivery(originCommitted: Boolean = true) = AuthoritativeMessageDelivery(
        hubScope.path, clientMessageId, session, text, agentHandOff, originCommitted,
    )

    /**
     * [id] came back waiting. Waiting for the link is what the outbox is for; while the link is up it means the hub
     * cannot pass it on yet (the assistant restarting, a 5xx without a code, the main conversation not started), and
     * nothing else would wake the queue: as on iOS (`OutboxWait`), it goes again every [StallRetryMillis] and after
     * [StallLimitMillis] it is refused with [code], so the user sees why and can retry.
     */
    private fun stall(id: String, code: String?) {
        if (!transport.readyToSend.value) return
        synchronized(stalls) {
            val stall = stalls[id]
            if (stall != null) stall.code = code
            else stalls[id] = Stall(scope.launch { delay(StallLimitMillis); giveUp(id) }, code)
            if (stallRetry?.isActive != true) stallRetry = scope.launch { delay(StallRetryMillis); wake.trySend(Unit) }
        }
    }

    private fun unstall(id: String) = synchronized(stalls) { stalls.remove(id)?.giveUp?.cancel() }

    private fun clearStalls() = synchronized(stalls) {
        stalls.values.forEach { it.giveUp.cancel() }
        stalls.clear()
        stallRetry?.cancel()
        stallRetry = null
    }

    private suspend fun giveUp(id: String) {
        processing.withLock {
            val stall = synchronized(stalls) { stalls.remove(id) } ?: return
            // The messages queued behind it go now.
            if (store.transition(id, setOf(OutboxState.WaitingForNetwork), OutboxState.Refused, stall.code)) wake.trySend(Unit)
        }
    }

    private class Stall(val giveUp: Job, var code: String?)

    private fun request(entry: OutboxEntry) = MessageRequest(
        hubScope, entry.session, entry.text, entry.clientMessageId, entry.clearDraft,
        opId = entry.operationId, launcher = entry.launcher, model = entry.model, applySwitch = entry.applySwitch, via = entry.via,
    )

    private sealed interface MainTarget {
        data class Found(val session: SessionKey) : MainTarget
        /** The hub refused to start it with a setup code: nothing was created. */
        data class Refused(val code: String) : MainTarget
        /** Not started yet; [code] is why, if the hub said (the computer asleep, the assistant restarting). */
        data class NotYet(val code: String = "main_not_ready") : MainTarget
    }

    private suspend fun resolveMain(via: String? = null): MainTarget {
        store.mainConversation()?.let { return MainTarget.Found(it) }
        val resolver = mainResolver ?: return MainTarget.NotYet()
        return try {
            val target = resolver.findMainConversation() ?: if (store.claimMainCreation()) {
                resolver.createMainConversation(via)
            } else null
            if (target != null) store.bindMainConversation(target)
            target?.let { MainTarget.Found(it) } ?: MainTarget.NotYet()
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            // A 409 with a setup code is the hub refusing before it creates anything: the claim goes back, so the
            // next try (after the user adds a computer…) starts it. Any other failure may have created it remotely:
            // only look up the main pointer on retry.
            val coded = hubCodedFailure(error)
            val refusal = coded?.takeIf { it.status == 409 }?.code?.takeIf { it in SetupFailureCodes }
            if (refusal != null) {
                store.releaseMainCreation()
                MainTarget.Refused(refusal)
            } else {
                // The computer asleep or the assistant restarting: the hub answered and started nothing, so the next
                // try (still a lookup first) may start it once they are back; the message waits meanwhile ([stall]).
                val later = coded?.code?.takeIf { it in RetryLaterCodes }
                if (later != null) store.releaseMainCreation()
                later?.let { MainTarget.NotYet(it) } ?: MainTarget.NotYet()
            }
        }
    }

    private suspend fun attempt(entry: OutboxEntry): MessageTransportResult {
        val request = request(entry)
        receiptState.begin(request, entry.mainTarget)
        val result = try {
            withTimeoutOrNull(reviewTimeoutMillis) { watchingSend(entry.session) { transport.send(request) } }
                ?: MessageTransportResult.NoReceipt
        } catch (cancelled: CancellationException) {
            receiptState.result(request, MessageTransportResult.NoReceipt)
            withContext(NonCancellable) {
                store.transition(entry.clientMessageId, setOf(OutboxState.Sending), OutboxState.Unknown)
            }
            throw cancelled
        } catch (_: Exception) {
            // The transport must positively identify pre-send failures with NotSent.
            MessageTransportResult.NoReceipt
        }
        receiptState.result(request, result)
        return result
    }

    companion object {
        /** Refusals of the start that come back by themselves (docs/assistant/onboarding.md 4.6): nothing was created. */
        private val RetryLaterCodes = setOf("node_unreachable", "assistant_offline")
        private val reviewable = setOf(OutboxState.Unknown, OutboxState.Verifying)
        /** Delivery unknown, with or without the review deadline passed: the user decides (confirm or discard). */
        private val undecided = reviewable + OutboxState.NeedsConfirmation
        private val awaitingReceipt = undecided + OutboxState.Sending
        private val settled = setOf(OutboxState.Sent, OutboxState.Shown)
        private val reconcilable = awaitingReceipt + OutboxState.Sent + OutboxState.Held
        /** Read-only catch-up may also find late evidence for an already shown message; transcript matching may not. */
        private val catchUpStates = reconcilable + OutboxState.Shown
        private val dispatchable = setOf(OutboxState.Queued, OutboxState.WaitingForNetwork)
        private val hidden = setOf(OutboxState.Cancelled, OutboxState.Shown, OutboxState.Kept, OutboxState.Held)
        private val refusals = setOf("agent_stopped", "input_draft", "dialog_open", "codex_not_ready", "codex_first_text", "codex_waiting", "op_id_reused", "invalid", "stale_session")
        /** A message that keeps coming back waiting while the link is up goes again this often (iOS retryOutboxSoon). */
        private const val StallRetryMillis = 10_000L
        /** …and is refused after this long, about six tries (iOS OutboxWait.connectedLimit). */
        private const val StallLimitMillis = 60_000L

        /** The hub takes UUIDs only as operation ids. */
        fun newOperationId(): String = java.util.UUID.randomUUID().toString()

        /**
         * The hub's own sentence for a coded refusal (its `error`, in the request's `Accept-Language`): none without a
         * code, for an `internal` failure's detail, the bare code, or the status the transport put in for a missing body.
         */
        private fun MessageTransportResult.HubError.hubWords(): String? = error?.trim()?.takeIf {
            code != null && code != "internal" && it.isNotEmpty() && it != code && !it.startsWith("HTTP ")
        }

        /** True when the hub confirmed nothing reached the node, including older hubs omitting delivered on 503 assistant_offline. */
        private fun MessageTransportResult.HubError.notDelivered(): Boolean =
            delivered == "none" || (code == "assistant_offline" && delivered == null)

        private fun MessageTransportResult.provesServerReceived(): Boolean =
            this == MessageTransportResult.Accepted || this == MessageTransportResult.Kept ||
                this == MessageTransportResult.Held || (this as? MessageTransportResult.HubError)?.delivered == "yes"

        fun classify(result: MessageTransportResult): OutboxState = when (result) {
            MessageTransportResult.Held -> OutboxState.Held
            MessageTransportResult.Accepted -> OutboxState.Sent
            MessageTransportResult.Kept -> OutboxState.Kept
            MessageTransportResult.NotSent -> OutboxState.WaitingForNetwork
            MessageTransportResult.NoReceipt -> OutboxState.Unknown
            is MessageTransportResult.HubError -> when {
                // Its first paste may already be in Codex. Only the user can decide whether to send it again.
                result.code == "codex_first_unconfirmed" && result.delivered == "unknown" -> OutboxState.NeedsConfirmation
                result.delivered == "unknown" -> OutboxState.Verifying
                result.delivered == "yes" -> OutboxState.Sent
                result.code in refusals || result.code in SetupFailureCodes -> OutboxState.Refused
                // The computer asleep: it would wait for a link that is already up; refused at once with Retry (iOS).
                result.code == "node_unreachable" && result.delivered == "none" -> OutboxState.Refused
                result.code == "assistant_offline" && result.notDelivered() -> OutboxState.WaitingForNetwork
                result.delivered == "none" && result.retryable -> OutboxState.WaitingForNetwork
                result.delivered == "none" -> OutboxState.Refused
                else -> OutboxState.Unknown
            }
        }
    }
}
