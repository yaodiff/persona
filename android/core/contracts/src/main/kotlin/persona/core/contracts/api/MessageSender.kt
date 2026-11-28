package persona.core.contracts.api

import persona.core.contracts.SessionKey
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

enum class DeliveryState {
    Sending, WaitingForNetwork, Sent, Rejected, NeedsConfirmation,
    Queued, Unknown, Refused, Verifying, Held,
}

data class OutboxMessage(
    val clientMessageId: String,
    val session: SessionKey,
    val text: String,
    val state: DeliveryState,
    val error: String? = null,
    val mainTarget: Boolean = false,
    /** When this phone wrote it, epoch millis. Absent on an outbox restored from before that was stored. */
    val createdAt: Long? = null,
    /** The active attempt's wire id after the manager explicitly refused an earlier attempt. */
    val opId: String? = null,
    /**
     * The hub's own sentence for the refusal [error] codes, in the app's language (the `error` of its refusal); null when
     * it gave none, for every other state, and for an outbox restored from before it was stored.
     */
    val reason: String? = null,
)

private val NoWrittenAt = MutableStateFlow(emptyMap<String, Long>())

interface MessageSender : MessageSendReceiptSource {
    val outbox: StateFlow<List<OutboxMessage>>
    /** When this phone wrote each send, including ones already in the transcript. Epoch millis, by client message id. */
    val writtenAt: StateFlow<Map<String, Long>> get() = NoWrittenAt
    suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String? = null)
    /** [launcher] and [model] apply to this message; null keeps the hub's configured default. */
    suspend fun send(
        session: SessionKey, text: String, clientMessageId: String, clearDraft: String?,
        launcher: String?, model: String?,
    ) {
        require(launcher == null && model == null) { "Launcher/model overrides are not supported" }
        send(session, text, clientMessageId, clearDraft)
    }

    /** A marked send must be implemented explicitly; an old sender must never silently drop its provenance. */
    suspend fun send(
        session: SessionKey, text: String, clientMessageId: String, clearDraft: String?,
        launcher: String?, model: String?, via: String?,
    ) {
        require(via == null) { "Message provenance is not supported" }
        send(session, text, clientMessageId, clearDraft, launcher, model)
    }
    /**
     * [send] for the user's own message from a chat whose session shows a launcher switch waiting
     * (`pending_switch`, docs/protocol/protocol.md "Switching a running Agent to another provider"): it goes with
     * `apply_switch`, so the hub carries the switch out first. Every other message leaves the switch waiting.
     */
    suspend fun sendApplyingSwitch(session: SessionKey, text: String, clientMessageId: String) = send(session, text, clientMessageId)
    suspend fun cancel(clientMessageId: String): Boolean = false
    /**
     * Undo of a [cancel] (#4645): [message], as the outbox listed it before, back in its state with its refusal, so it
     * shows (and a queued or waiting one goes out) again. Nothing once it is no longer cancelled.
     */
    fun restore(message: OutboxMessage) = Unit
    /**
     * Sends a refused message, or one whose delivery is to be confirmed, again; one still on its way (queued, waiting for
     * the network) is tried at once.
     */
    suspend fun retry(clientMessageId: String): Boolean = false
    /**
     * After a confirmed start answer, requeue only this session's known-undelivered refusals with exactly [code].
     * Uncertain deliveries are never included. Returns how many durable entries changed, in their existing queue order.
     */
    suspend fun retryRefused(session: SessionKey, code: String): Int = 0
    /** Drops a message whose delivery is unknown: off the list and the queue, never retried or reconciled. */
    suspend fun discard(clientMessageId: String): Boolean = false
    suspend fun confirm(clientMessageId: String, delivered: Boolean): Boolean = false
}
