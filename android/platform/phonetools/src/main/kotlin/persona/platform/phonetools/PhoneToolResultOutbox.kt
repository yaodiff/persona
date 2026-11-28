package persona.platform.phonetools

import persona.core.contracts.api.PhoneToolResult

/**
 * Holds the *results* of phone tool calls that could not be sent on the live socket right now (the user tapped
 * Allow just as the connection dropped, or a confirmed write's local execution finished after a transient
 * disconnect). The outbox is scoped to one pairing runtime, bounded, and lives only in memory: a process death
 * never replays it.
 *
 * Only results are kept, never the call itself or a runnable. `flush` re-sends results, it never re-executes a
 * tool. The hub matches a `tool_result` to the original call by `call_id`, so a result delivered on a later
 * socket is still correct; the hub decides whether it still waits for that call and otherwise ignores it.
 *
 * A new result for an already-held `call_id` replaces the held one (a confirmed write that produced an uncertain
 * result first and a concrete result later must not be sent twice). Insertion order is preserved on flush.
 */
internal class PhoneToolResultOutbox(
    private val capacity: Int = DEFAULT_CAPACITY,
) {
    private data class Held(val callId: String, val result: PhoneToolResult, val seq: Long)

    private val lock = Any()
    private val queue = ArrayDeque<Held>()
    private val deliveredSeq = LinkedHashMap<String, Long>()
    private var nextSeq = 0L
    private var closed = false

    /**
     * Try to send [result] now through [send]. Returns true if the send succeeded (or no entry was held); false
     * if the result was queued for a later flush or the outbox is closed.
     *
     * The [send] block is the live socket send and returns its success. It is invoked outside the lock.
     */
    fun send(callId: String, result: PhoneToolResult, send: () -> Boolean): Boolean {
        val seq = synchronized(lock) { ++nextSeq }
        if (send()) {
            synchronized(lock) {
                queue.removeAll { it.callId == callId && it.seq < seq }
                recordDeliveredLocked(callId, seq)
            }
            return true
        }
        synchronized(lock) {
            if (closed) return false
            if ((deliveredSeq[callId] ?: 0L) > seq) return false
            if (queue.any { it.callId == callId && it.seq > seq }) return false
            queue.removeAll { it.callId == callId }
            queue.addLast(Held(callId, result, seq))
            while (queue.size > capacity) queue.removeFirst()
        }
        return false
    }

    /**
     * Re-send every held result in FIFO order through [send]. Stops at the first failure: the connection dropped
     * again and the remaining results must keep waiting. Each successful send removes exactly that entry, so a
     * concurrent `send` cannot lose it.
     */
    fun flush(send: (callId: String, result: PhoneToolResult) -> Boolean) {
        while (true) {
            val next = synchronized(lock) {
                if (closed) return
                queue.firstOrNull()
            } ?: return
            if (!send(next.callId, next.result)) return
            synchronized(lock) {
                if (queue.firstOrNull() == next) queue.removeFirst()
                else queue.removeAll { it.callId == next.callId && it.seq <= next.seq }
                recordDeliveredLocked(next.callId, next.seq)
            }
        }
    }

    /** Test seam: how many results are held. */
    fun size(): Int = synchronized(lock) { queue.size }

    /** The pairing runtime is going away: drop everything. Nothing is persisted. */
    fun close() {
        synchronized(lock) {
            closed = true
            queue.clear()
            deliveredSeq.clear()
        }
    }

    private fun recordDeliveredLocked(callId: String, seq: Long) {
        if (closed) return
        val prev = deliveredSeq.remove(callId) ?: 0L
        deliveredSeq[callId] = maxOf(prev, seq)
        val cap = maxOf(capacity * 4, DEFAULT_DELIVERED_CAPACITY)
        while (deliveredSeq.size > cap) deliveredSeq.remove(deliveredSeq.keys.first())
    }

    private companion object {
        const val DEFAULT_CAPACITY = 16
        const val DEFAULT_DELIVERED_CAPACITY = 64
    }
}
