package persona.core.data

import persona.core.contracts.SessionKey
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.DraftSnapshot
import kotlinx.coroutines.flow.Flow

enum class OutboxState(val wire: String, val delivery: DeliveryState) {
    Queued("queued", DeliveryState.Queued),
    Sending("sending", DeliveryState.Sending),
    WaitingForNetwork("waitingForNetwork", DeliveryState.WaitingForNetwork),
    Unknown("unknown", DeliveryState.Unknown),
    Refused("refused", DeliveryState.Refused),
    Verifying("verifying", DeliveryState.Verifying),
    Held("held", DeliveryState.Held),
    Sent("sent", DeliveryState.Sent),
    NeedsConfirmation("needsReview", DeliveryState.NeedsConfirmation),
    Cancelled("cancelled", DeliveryState.Rejected),
    Shown("shown", DeliveryState.Sent),
    /** The assistant owns delivery now: hidden locally, never published as delivered or retried by this outbox. */
    Kept("kept", DeliveryState.Rejected),
}

data class OutboxEntry(
    val clientMessageId: String,
    val session: SessionKey,
    val text: String,
    val clearDraft: String? = null,
    val state: OutboxState = OutboxState.Queued,
    val error: String? = null,
    val createdAt: Long,
    val mainTarget: Boolean = false,
    /** The operation id of the next attempt when it is no longer [clientMessageId] (the hub said none was delivered). */
    val opId: String? = null,
    /** The original submission's Persona overrides, kept for every retry and after process restoration. */
    val launcher: String? = null,
    val model: String? = null,
    /** It carries out the session's waiting launcher switch ([persona.core.contracts.api.MessageSender.sendApplyingSwitch]). */
    val applySwitch: Boolean = false,
    /** External provenance ([persona.core.contracts.api.MessageVia]); old rows and typed messages restore null. */
    val via: String? = null,
    /**
     * The hub's own sentence for the refusal [error] codes, in the app's language; kept only by the transition that
     * recorded the refusal ([OutboxStore.transition] with a reason) and cleared by every other one (#16531).
     */
    val reason: String? = null,
) {
    /** Journal-owned delivery evidence stays out of the public data-class and generated copy signatures. */
    internal var agentHandOff: InboxAgentHandOff? = null
        private set

    val operationId: String get() = opId ?: clientMessageId

    /** Attaches evidence to a new entry or a fresh public copy. */
    internal fun withAgentHandOff(handOff: InboxAgentHandOff?): OutboxEntry = apply { agentHandOff = handOff }

    /** The internal counterpart of the public generated copy, including journal-owned delivery evidence. */
    internal fun copyForDelivery(
        session: SessionKey = this.session,
        state: OutboxState = this.state,
        error: String? = this.error,
        opId: String? = this.opId,
        agentHandOff: InboxAgentHandOff? = this.agentHandOff,
        reason: String? = this.reason,
    ): OutboxEntry = copy(session = session, state = state, error = error, opId = opId, reason = reason)
        .withAgentHandOff(agentHandOff)
}

/** All methods operate on exactly one pairing scope. */
interface OutboxStore {
    val entries: Flow<List<OutboxEntry>>
    /** Persist without clearing an editor whose revision is unknown. */
    suspend fun enqueue(entry: OutboxEntry)
    /**
     * Commit authoritative hand-off evidence before it is published. The Room store overrides this to commit the
     * pairing's reply journal; the returned entry always carries the evidence. Ordinary messages are unchanged.
     */
    suspend fun persistAgentHandOffDelivery(entry: OutboxEntry): OutboxEntry = entry.copyForDelivery(
        agentHandOff = entry.agentHandOff?.copy(delivered = true),
    )
    /** Whether [persistAgentHandOffDelivery] also committed the originating inbox's replay journal. */
    fun agentHandOffOriginCommitted(entry: OutboxEntry): Boolean = true
    /** Compare the draft revision, persist the message and clear that draft in one transaction. */
    suspend fun enqueueDraft(entry: OutboxEntry, draft: DraftSnapshot?) {
        check(draft == null) { "Versioned drafts are not supported by this store" }
        enqueue(entry)
    }
    suspend fun mainConversation(): SessionKey? = null
    suspend fun claimMainCreation(): Boolean = error("Main conversation creation is not supported by this store")
    suspend fun bindMainConversation(session: SessionKey): Unit = error("Main conversation binding is not supported by this store")
    /** Gives back an unbound [claimMainCreation]: the hub refused to start it, so the next try asks again. */
    suspend fun releaseMainCreation() = Unit
    /** Unbinds [session] if it is the bound main conversation (it was put away); messages already bound to it stay. */
    suspend fun forgetMainConversation(session: SessionKey) = Unit
    suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String? = null): Boolean
    /** [transition], and the message's next attempt goes under [opId]. */
    suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String): Boolean =
        throw UnsupportedOperationException("Operation ids are not supported by this store")
    /**
     * [transition] that also keeps [reason], the hub's own sentence for the refusal [error] codes ([OutboxEntry.reason]);
     * with an [opId] the next attempt goes under it. The other transitions clear the sentence. A store that cannot keep
     * it records the code alone, and the app words the refusal by its code.
     */
    suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String?, reason: String?): Boolean =
        if (opId == null) transition(id, from, to, error) else transition(id, from, to, error, opId)
    suspend fun recoverInterrupted()
}
