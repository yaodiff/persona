package persona.feature.inbox

import android.content.res.Resources
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import persona.core.contracts.SessionKey
import persona.core.inbox.*

internal const val INBOX_HAND_OFF_UNDO_MILLIS = 5_000L
internal const val INBOX_HAND_OFF_LISTED_TARGETS = 12
internal const val INBOX_HAND_OFF_MENU_TARGETS = 3
internal const val INBOX_HAND_OFF_RETAINED_MODELS = 64

/** A pairing's bar models. Every model keeps this store's fixed repository, never a currently-selected-group facade. */
class InboxHandOffBars(
    private val scope: CoroutineScope,
    private val resources: Resources,
    private val repository: InboxRepository,
) {
    private val models = LinkedHashMap<String, InboxHandOffBarModel>(0, 0.75f, true)

    @Synchronized
    fun get(itemId: String): InboxHandOffBarModel {
        val model = models.getOrPut(itemId) { InboxHandOffBarModel(itemId, repository, scope, resources) }
        trimSettledModels(keeping = itemId)
        return model
    }

    private fun trimSettledModels(keeping: String) {
        val entries = models.entries.iterator()
        while (models.size > INBOX_HAND_OFF_RETAINED_MODELS && entries.hasNext()) {
            val entry = entries.next()
            if (entry.key == keeping) continue
            when (entry.value.state.value.phase) {
                is HandOffBarPhase.Queued, is HandOffBarPhase.Sending -> Unit
                else -> entries.remove()
            }
        }
    }
}

/** The production facade switches this store when its pairing changes. */
interface InboxHandOffBarOwner {
    val handOffBars: StateFlow<InboxHandOffBars?>
}

data class HandOffBarOperation(
    val ask: String,
    val typed: Boolean,
    val target: HandOffTarget?,
    var markDone: Boolean,
    val attempt: HandOffAttempt = HandOffAttempt(),
    var text: String? = null,
    var operationId: String? = null,
)

sealed interface HandOffBarPhase {
    data object Idle : HandOffBarPhase
    data class Queued(val operation: HandOffBarOperation) : HandOffBarPhase
    data class Sending(val operation: HandOffBarOperation, val openRequested: Boolean = false) : HandOffBarPhase
    data class Sent(
        val operation: HandOffBarOperation,
        val session: SessionKey?,
        val doneError: Throwable? = null,
        val openRequested: Boolean = false,
    ) : HandOffBarPhase
    data class Failed(val operation: HandOffBarOperation, val error: Throwable) : HandOffBarPhase
}

data class HandOffBarState(
    val input: String = "",
    val target: HandOffTarget? = null,
    val markDone: Boolean = false,
    val lastTime: Boolean = false,
    val lastGone: Boolean = false,
    val phase: HandOffBarPhase = HandOffBarPhase.Idle,
)

data class HandOffDestination(val target: HandOffTarget?, val session: SessionKey?)

/** Pairing-owned hand-off state. Only the local wait can be undone; a retry reuses the first prompt and operation id. */
class InboxHandOffBarModel internal constructor(
    val itemId: String,
    private val repository: InboxRepository,
    private val scope: CoroutineScope,
    private val resources: Resources,
    private val undoWindowMillis: Long = INBOX_HAND_OFF_UNDO_MILLIS,
) {
    private val mutableState = MutableStateFlow(HandOffBarState())
    val state = mutableState.asStateFlow()
    private var timer: Job? = null
    private var preselected = false
    private var doneChosen = false

    @Synchronized
    fun restoreTarget(key: String?) {
        if (key == null || mutableState.value.phase !is HandOffBarPhase.Idle) return
        val target = liveTargets().firstOrNull { it.key == key } ?: return
        mutableState.update { it.copy(target = target, markDone = handOffMarkDone(target, if (doneChosen) it.markDone else null)) }
    }

    @Synchronized
    fun preselect(entries: List<HandOffMemoryEntry>) {
        if (preselected || mutableState.value.phase !is HandOffBarPhase.Idle) return
        if (!repository.snapshot.value.targetsReady) return
        val record = repository.snapshot.value.records.inboxRecord(itemId) ?: return
        preselected = true
        val key = InboxHandOffMemory.key(record.source, record.mutes) ?: return
        val targets = liveTargets()
        val resolved = InboxHandOffMemory.resolve(InboxHandOffMemory.recall(entries, key)) { path -> targets.any { it.key == path } } ?: return
        val agent = resolved.target.removePrefix("agent:").takeIf { resolved.target.startsWith("agent:") }
        val target = agent?.let { path -> targets.firstOrNull { it.key == path } }
        mutableState.update {
            it.copy(target = target, markDone = handOffMarkDone(target, if (doneChosen) it.markDone else null),
                lastTime = resolved.lastTime, lastGone = resolved.gone)
        }
    }

    fun liveTargets(): List<HandOffTarget> {
        val snapshot = repository.snapshot.value
        return handOffTargets(snapshot.sessions.map { it.session }, snapshot.machines, limit = INBOX_HAND_OFF_LISTED_TARGETS)
    }

    @Synchronized
    fun selectTarget(target: HandOffTarget?) {
        if (mutableState.value.phase !is HandOffBarPhase.Idle && mutableState.value.phase !is HandOffBarPhase.Sent) return
        mutableState.update {
            it.copy(target = target, markDone = handOffMarkDone(target, if (doneChosen) it.markDone else null), lastTime = false, lastGone = false)
        }
    }

    @Synchronized
    fun setMarkDone(on: Boolean) {
        doneChosen = true
        when (val phase = mutableState.value.phase) {
            is HandOffBarPhase.Failed -> {
                phase.operation.markDone = on
                mutableState.update { it.copy(markDone = on) }
            }
            is HandOffBarPhase.Idle, is HandOffBarPhase.Sent -> mutableState.update { it.copy(markDone = on) }
            else -> Unit
        }
    }

    @Synchronized
    fun updateInput(value: String) = mutableState.update { it.copy(input = value) }

    @Synchronized
    fun queue(ask: String, typed: Boolean) {
        val clean = ask.trim()
        if (clean.isEmpty() || mutableState.value.phase is HandOffBarPhase.Sending) return
        timer?.cancel()
        val operation = HandOffBarOperation(clean, typed, mutableState.value.target, mutableState.value.markDone)
        mutableState.update {
            it.copy(input = if (typed) "" else it.input, lastGone = false, phase = HandOffBarPhase.Queued(operation))
        }
        timer = scope.launch {
            delay(undoWindowMillis)
            fire(operation, openRequested = false)
        }
    }

    @Synchronized
    fun undo(): String? {
        val queued = mutableState.value.phase as? HandOffBarPhase.Queued ?: return null
        timer?.cancel()
        timer = null
        mutableState.update {
            it.copy(input = if (queued.operation.typed) queued.operation.ask else it.input, phase = HandOffBarPhase.Idle)
        }
        return queued.operation.ask
    }

    @Synchronized
    fun viewQueued() {
        when (val phase = mutableState.value.phase) {
            is HandOffBarPhase.Queued -> {
                timer?.cancel()
                timer = null
                fire(phase.operation, openRequested = true)
            }
            is HandOffBarPhase.Sending -> mutableState.update { it.copy(phase = phase.copy(openRequested = true)) }
            else -> Unit
        }
    }

    @Synchronized
    fun retry() {
        val failed = mutableState.value.phase as? HandOffBarPhase.Failed ?: return
        fire(failed.operation, openRequested = false)
    }

    fun openSent(): HandOffDestination? {
        val sent = mutableState.value.phase as? HandOffBarPhase.Sent ?: return null
        return HandOffDestination(sent.operation.target, sent.session)
    }

    @Synchronized
    fun openHandled() {
        val sent = mutableState.value.phase as? HandOffBarPhase.Sent ?: return
        if (sent.openRequested) mutableState.update { it.copy(phase = sent.copy(openRequested = false)) }
    }

    private fun fire(operation: HandOffBarOperation, openRequested: Boolean) {
        scope.launch {
            val start = synchronized(this@InboxHandOffBarModel) {
                when (val phase = mutableState.value.phase) {
                    is HandOffBarPhase.Queued -> if (phase.operation === operation) {
                        mutableState.update { it.copy(phase = HandOffBarPhase.Sending(operation, openRequested)) }
                        true
                    } else false
                    is HandOffBarPhase.Failed -> if (phase.operation === operation) {
                        mutableState.update { it.copy(phase = HandOffBarPhase.Sending(operation, openRequested = false)) }
                        true
                    } else false
                    is HandOffBarPhase.Sending -> {
                        if (phase.operation === operation && openRequested) {
                            mutableState.update { it.copy(phase = phase.copy(openRequested = true)) }
                        }
                        false
                    }
                    else -> false
                }
            }
            if (start) send(operation)
        }
    }

    private suspend fun send(operation: HandOffBarOperation) {
        try {
            var known: InboxRecord? = null
            if (operation.text == null) {
                // A finished item opened from history is not in the live list; the page's own lookup finds it.
                val record = repository.item(itemId) ?: error("The Inbox item is no longer available")
                known = record
                operation.text = handoffPrompt(resources, handOffItem(record, repository.personaContext(itemId)?.text), operation.ask)
            }
            val text = checkNotNull(operation.text)
            val target = operation.target
            val operationId = operation.operationId ?: operation.attempt.operationId(text, target).also { operation.operationId = it }
            val session = repository.handOff(InboxHandOffRequest(itemId, text, target?.machineId, target?.sessionId, operationId))
            operation.attempt.done()
            val record = repository.snapshot.value.records.inboxRecord(itemId) ?: known
            val memoryKey = record?.let { InboxHandOffMemory.key(it.source, it.mutes) }
            if (memoryKey != null) {
                try {
                    val entries = InboxHandOffMemory.remember(repository.handOffMemory.first(), memoryKey,
                        target?.let(InboxHandOffMemory::agent) ?: InboxHandOffMemory.MAIN)
                    repository.saveHandOffMemory(entries)
                } catch (_: CancellationException) {
                    // The accepted hand-off still settles if the pairing closes during follow-up.
                } catch (_: Exception) { }
            }
            var doneError: Throwable? = null
            if (operation.markDone && record?.item?.isDone != true) {
                try { repository.setDone(itemId, true) }
                catch (e: CancellationException) { doneError = e }
                catch (e: Exception) { doneError = e }
            }
            synchronized(this) {
                val sending = mutableState.value.phase as? HandOffBarPhase.Sending
                if (sending?.operation === operation) {
                    mutableState.update {
                        it.copy(lastGone = false, phase = HandOffBarPhase.Sent(operation, session, doneError, sending.openRequested))
                    }
                }
            }
        } catch (e: CancellationException) {
            if (!scope.coroutineContext.isActive) throw e
            fail(operation, e)
        } catch (e: Exception) {
            fail(operation, e)
        }
    }

    private fun fail(operation: HandOffBarOperation, error: Throwable) {
        synchronized(this) {
            val sending = mutableState.value.phase as? HandOffBarPhase.Sending
            if (sending?.operation === operation) mutableState.update { it.copy(phase = HandOffBarPhase.Failed(operation, error)) }
        }
    }
}
