package persona.feature.chat

import persona.core.chatlist.ChatListWindow
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.domain.lines.*
import persona.core.designsystem.StatusLineState
import persona.core.designsystem.StatusLineKind
import persona.core.designsystem.resolveStatusLine
import persona.core.domain.lines.Fold as LineFold
import persona.core.domain.lines.TaskNote as LineTask
import java.util.concurrent.TimeUnit
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock

internal fun Item.asLineItem() = LineItem(this,
    fold?.let { LineFold(it.items, it.tools, it.thoughts, it.throughSeq, it.throughId, parseTime(it.until)) },
    task?.let { LineTask(it.kind, it.title, it.status, it.toolUseId, it.taskId, it.detail, it.exitCode, it.outputFile) })

internal fun itemBlockPart(id: String): Int {
    val hash = id.lastIndexOf('#')
    if (hash < 0 || hash == id.lastIndex) return 0
    val start = hash + 1
    val first = id[start]
    if (first == '-' || first == '+') return id.substring(start).toIntOrNull() ?: 0
    var value = 0
    for (i in start until id.length) {
        val digit = id[i] - '0'
        if (digit !in 0..9) return 0
        if (value > (Int.MAX_VALUE - digit) / 10) return 0
        value = value * 10 + digit
    }
    return value
}

internal val chatItemOrder = Comparator<Item> { a, b ->
    val bySeq = a.seq.compareTo(b.seq)
    if (bySeq != 0) bySeq else itemBlockPart(a.id).compareTo(itemBlockPart(b.id))
}

internal fun ChatLine.lastItem(): Item? = when (this) {
    is ChatLine.Row -> row.item?.item
    is ChatLine.UserImages -> images.lastOrNull()?.item
    is ChatLine.Work -> steps.mapNotNull { it.lastItem() }.maxWithOrNull(chatItemOrder)
    is ChatLine.Step -> step.lastItem()
    is ChatLine.Wakes -> turn.wakes.lastOrNull()?.item
    is ChatLine.ApiError -> item.item
    is ChatLine.Recaps -> items.lastOrNull()?.item
    else -> null
}

/** The transcript item a visible line stands for when read receipts count it: a row, or the answer an error card is. */
internal fun ChatLine.seenItem(): Item? = when (this) {
    is ChatLine.Row -> row.item?.item
    is ChatLine.ApiError -> item.item
    else -> null
}

private fun WorkItem.lastItem(): Item? = when (this) {
    is WorkItem.Tool -> (call.result ?: call.use).item
    is WorkItem.Thought -> item.item
    is WorkItem.Narration -> item.item
    is WorkItem.Folded -> item.item.copy(seq = item.fold?.throughSeq ?: item.seq)
}

/** Keep the actual item snapshot: changing turn keys cannot move a historical reading window. */
internal class ChatTimeline(
    /** Optional hook invoked right before memoized line projection, used by concurrency tests to stall [lines]. */
    private val beforeMemo: (() -> Unit)? = null,
) {
    private data class ExpandedFold(val fold: Item, val items: List<Item>, val anchorId: String?)

    private data class ControlState(
        val frozen: Boolean = false,
        val freezeToken: Long = 0L,
        val frozenLiveId: String? = null,
        val frozenLiveSeq: Long = 0L,
        val frozenLiveTargetSeq: Long = 0L,
        val frozenLivePending: Boolean = false,
        val frozenItemsRef: List<Item>? = null,
        val expanded: Map<String, ExpandedFold> = emptyMap(),
    )

    private data class LinesCache(
        val sourceItems: List<Item>,
        val frozen: Boolean,
        val freezeToken: Long,
        val live: Boolean,
        val liveTurnId: String?,
        val open: Set<String>,
        val expanded: Map<String, ExpandedFold>,
        val snapshot: persona.core.domain.lines.ChatSnapshot,
    )

    private val window = ChatListWindow<Item>({ it.id },
        chatItemOrder,
        { it.role == "assistant" && it.kind == "text" })
    private val stateLock = Any()
    private val memoLock = ReentrantLock()
    private val liveBackfillDone = memoLock.newCondition()
    @Volatile private var control = ControlState()
    @Volatile private var shownLiveId: String? = null
    private var shownLiveSeq = 0L
    private var nextLivePassSeq = 0L
    private val inFlightLivePassSeqs = HashSet<Long>()
    private var nextFreezeToken = 0L
    @Volatile private var lastLines: LinesCache? = null

    /** Keeps the lines of turns that did not change since the last [lines]: a chat changes at its end. */
    private val memo = ChatLinesMemo()

    /** A frozen window keeps the turn that was running when it froze, even after a newer one starts. */
    fun freeze(items: List<Item>) {
        val _ = freezeChanged(items)
    }

    /** Freezes only on a following -> frozen transition without contending with background [lines] or [searchItems]. */
    internal fun freezeChanged(items: List<Item>): Boolean {
        if (control.frozen) return false
        synchronized(stateLock) {
            if (control.frozen) return false
            window.freeze(items)
            val targetSeq = inFlightLivePassSeqs.maxOrNull() ?: 0L
            val pending = targetSeq > shownLiveSeq
            control = control.copy(
                frozen = true,
                freezeToken = ++nextFreezeToken,
                frozenLiveId = shownLiveId,
                frozenLiveSeq = shownLiveSeq,
                frozenLiveTargetSeq = if (pending) targetSeq else 0L,
                frozenLivePending = pending,
                frozenItemsRef = items,
            )
            return true
        }
    }

    fun follow() {
        val _ = followChanged()
    }

    /** Resumes following only on a frozen -> following transition without contending with background computation. */
    internal fun followChanged(): Boolean {
        if (!control.frozen) return false
        synchronized(stateLock) {
            if (!control.frozen) return false
            window.follow()
            control = control.copy(
                frozen = false,
                frozenLiveId = null,
                frozenLiveSeq = 0L,
                frozenLiveTargetSeq = 0L,
                frozenLivePending = false,
                frozenItemsRef = null,
            )
            return true
        }
    }

    fun isFrozen(): Boolean = control.frozen

    fun newMessageCount(items: List<Item>, below: Item?, lastRead: Item?, defer: Boolean = false): Int =
        window.newMessageCount(items, below, lastRead, defer)

    fun unfold(fold: Item, items: List<Item>) {
        val anchorId = items.asSequence()
            .filter { it.kind != "tool_result" && !(it.kind == "image" && it.toolUseId != null) }
            .minWithOrNull(chatItemOrder)
            ?.id
        synchronized(stateLock) {
            control = control.copy(expanded = control.expanded + (fold.id to ExpandedFold(fold, items, anchorId)))
        }
    }

    /**
     * Whether [lines] for ([items], [live], [open]) would already match the last computed snapshot, so a freeze or
     * follow transition does not need to restart background projection or recompose the page.
     */
    internal fun matchesLastProjection(items: List<Item>, live: Boolean, open: Set<String>): Boolean {
        val cached = lastLines ?: return false
        val state = control
        if (cached.expanded !== state.expanded || cached.open != open) return false
        if (state.frozen) {
            if (state.frozenLivePending) return false
            if (state.frozenItemsRef !== items || cached.sourceItems !== items) return false
            return cached.snapshot.liveTurnId == state.frozenLiveId
        }
        return !cached.frozen && cached.sourceItems === items && cached.live == live
    }

    /**
     * A frozen window keeps the fold it froze with and also takes in the work the store put in its place when the fold
     * was opened: the fold's own items then come twice, and every step of the open work line counted twice.
     */
    fun searchItems(items: List<Item>): List<Item> {
        val state = control
        return resolveItems(items, state.frozen, state.expanded)
    }

    private fun resolveItems(items: List<Item>, frozen: Boolean, folds: Map<String, ExpandedFold>): List<Item> {
        val windowed = if (frozen) window.items(items) else items
        if (folds.isEmpty()) {
            // A frozen window's snapshot is already keyed uniquely by item.id.
            return if (windowed !== items) windowed else deduplicateItems(windowed)
        }
        val seen = HashSet<String>(windowed.size)
        val out = ArrayList<Item>(windowed.size)
        for (item in windowed) {
            val expanded = folds[item.id]?.takeIf { it.fold.fold == item.fold }
            if (expanded != null) {
                for (step in expanded.items) {
                    if (seen.add(step.id)) out += step
                }
            } else if (seen.add(item.id)) {
                out += item
            }
        }
        return out
    }

    private fun deduplicateItems(items: List<Item>): List<Item> {
        if (items.size <= 1) return items
        val seen = HashSet<String>(items.size)
        var duplicate = false
        for (item in items) {
            if (!seen.add(item.id)) {
                duplicate = true
                break
            }
        }
        if (!duplicate) return items
        seen.clear()
        val out = ArrayList<Item>(items.size)
        for (item in items) {
            if (seen.add(item.id)) out += item
        }
        return out
    }

    /**
     * Records a completed unfrozen pass under [memoLock]. Returns true when this pass is the newest completed
     * unfrozen pass (so callers do not overwrite [lastLines] with an older pass's snapshot).
     */
    private fun finishUnfrozenUnderMemoLock(passSeq: Long, liveTurnId: String?): Boolean {
        var shouldSignal = false
        var isNewest = false
        synchronized(stateLock) {
            if (passSeq >= shownLiveSeq) {
                shownLiveId = liveTurnId
                shownLiveSeq = passSeq
                isNewest = true
            }
            val current = control
            if (current.frozen && current.frozenLivePending && passSeq <= current.frozenLiveTargetSeq) {
                // Keep the freshest completed pass up to the freeze target, and clear pending only once the
                // newest pass that was in flight at freeze finishes.
                val updatedId = if (passSeq > current.frozenLiveSeq) liveTurnId else current.frozenLiveId
                val updatedSeq = if (passSeq > current.frozenLiveSeq) passSeq else current.frozenLiveSeq
                val reachedTarget = passSeq >= current.frozenLiveTargetSeq
                control = current.copy(
                    frozenLiveId = updatedId,
                    frozenLiveSeq = updatedSeq,
                    frozenLiveTargetSeq = if (reachedTarget) 0L else current.frozenLiveTargetSeq,
                    frozenLivePending = !reachedTarget,
                )
                shouldSignal = reachedTarget
            }
        }
        if (shouldSignal) liveBackfillDone.signalAll()
        return isNewest
    }

    /** [live]: the agent is at work; [open] is the same persisted choice for live and historical lines. */
    fun lines(items: List<Item>, live: Boolean, open: Set<String>): persona.core.domain.lines.ChatSnapshot {
        var passSeq = 0L
        val stateBefore = synchronized(stateLock) {
            val current = control
            if (!current.frozen) {
                passSeq = ++nextLivePassSeq
                val _ = inFlightLivePassSeqs.add(passSeq)
            }
            current
        }
        try {
            val resolved = resolveItems(items, stateBefore.frozen, stateBefore.expanded)
            return memoLock.withLock {
                if (stateBefore.frozen) {
                    while (true) {
                        val current = control
                        if (!current.frozen || current.freezeToken != stateBefore.freezeToken || !current.frozenLivePending) {
                            break
                        }
                        try {
                            val _ = liveBackfillDone.await(50L, TimeUnit.MILLISECONDS)
                        } catch (_: InterruptedException) {
                            Thread.currentThread().interrupt()
                            break
                        }
                    }
                }
                val currentControl = control
                val effectiveLiveId = if (stateBefore.frozen) {
                    if (currentControl.frozen && currentControl.freezeToken == stateBefore.freezeToken) {
                        currentControl.frozenLiveId
                    } else {
                        stateBefore.frozenLiveId
                    }
                } else {
                    null
                }
                val cached = lastLines
                if (cached != null &&
                    cached.sourceItems === resolved &&
                    cached.frozen == stateBefore.frozen &&
                    (!stateBefore.frozen || cached.freezeToken == stateBefore.freezeToken) &&
                    (if (stateBefore.frozen) cached.liveTurnId == effectiveLiveId else cached.live == live) &&
                    cached.open == open &&
                    cached.expanded === stateBefore.expanded
                ) {
                    if (!stateBefore.frozen) {
                        val _ = finishUnfrozenUnderMemoLock(passSeq, cached.snapshot.liveTurnId)
                    }
                    return@withLock cached.snapshot
                }
                beforeMemo?.invoke()
                val options = if (stateBefore.frozen) LineOptions(liveTurnId = effectiveLiveId, openWork = open)
                    else LineOptions(openWork = open, live = live)
                val foldAnchors = if (stateBefore.expanded.isEmpty()) emptyMap() else buildMap(stateBefore.expanded.size) {
                    for ((foldId, entry) in stateBefore.expanded) {
                        val anchor = entry.anchorId
                        if (anchor != null) this[anchor] = "tools-$foldId"
                    }
                }
                val source = ArrayList<LineItem>(resolved.size)
                for (item in resolved) {
                    if (item.kind == "turn_duration") continue
                    val lineItem = item.asLineItem()
                    val anchor = foldAnchors[item.id]
                    source += if (anchor != null) lineItem.copy(exchangeId = anchor) else lineItem
                }
                val built = memo.lines(source, options)
                val isNewestUnfrozen = if (!stateBefore.frozen) {
                    finishUnfrozenUnderMemoLock(passSeq, built.liveTurnId)
                } else {
                    true
                }
                val stateAfter = control
                if (isNewestUnfrozen &&
                    stateAfter.frozen == stateBefore.frozen &&
                    stateAfter.freezeToken == stateBefore.freezeToken &&
                    (!stateBefore.frozen || stateAfter.frozenLiveId == effectiveLiveId)
                ) {
                    lastLines = LinesCache(
                        sourceItems = resolved,
                        frozen = stateBefore.frozen,
                        freezeToken = stateBefore.freezeToken,
                        live = live,
                        liveTurnId = built.liveTurnId,
                        open = open.toSet(),
                        expanded = stateBefore.expanded,
                        snapshot = built,
                    )
                }
                built
            }
        } finally {
            if (!stateBefore.frozen) {
                val clearedPending = synchronized(stateLock) {
                    val _ = inFlightLivePassSeqs.remove(passSeq)
                    val current = control
                    if (current.frozen && current.frozenLivePending && passSeq == current.frozenLiveTargetSeq) {
                        // Target pass aborted before reaching finishUnfrozenUnderMemoLock: fall back to the newest
                        // remaining in-flight pass up to the target, or release waiters with the freshest available ID.
                        val fallbackTarget = inFlightLivePassSeqs.asSequence()
                            .filter { it <= current.frozenLiveTargetSeq && it > current.frozenLiveSeq }
                            .maxOrNull()
                        if (fallbackTarget != null) {
                            control = current.copy(frozenLiveTargetSeq = fallbackTarget)
                            false
                        } else {
                            control = current.copy(frozenLivePending = false, frozenLiveTargetSeq = 0L)
                            true
                        }
                    } else {
                        false
                    }
                }
                if (clearedPending) {
                    memoLock.withLock { liveBackfillDone.signalAll() }
                }
            }
        }
    }
}

internal fun ChatLine.recordedDuration(durations: Map<String, Long>): Long? = when (this) {
    is ChatLine.Work -> durations[id.removePrefix("w-").removePrefix("dw-")]
    else -> null
}

internal fun recordedDurationLabel(res: android.content.res.Resources, seconds: Long?): String? =
    seconds?.takeIf { it >= 0 }?.let { res.getString(R.string.chat_state_took_seconds, it) }

internal typealias StatusKind = StatusLineKind

/** How long a message of yours, the newest thing in the chat, counts as the start of a turn the hub has not reported. */
internal const val AWAIT_TURN_MILLIS = 60_000L

/**
 * The session's status says it is not at work, nor offline or waiting on you, and it did not fail to start: the hub may
 * just not have caught up.
 */
internal fun mayBeCatchingUp(status: String?, startFailed: Boolean) =
    !startFailed && (status == null || status == "idle" || status == "done" || status == "unknown")

/** Your message, when it is the newest thing in the transcript (nothing has answered it yet); one pass, no copy. */
internal fun awaitedMessage(items: List<Item>): Item? =
    items.asSequence().filter { it.kind != "turn_duration" }.maxWithOrNull(chatItemOrder)
        ?.takeIf { it.role == "user" && it.kind == "text" }

/**
 * [message] ([awaitedMessage]) is unanswered while the session's status still says it is not at work (#3909, web #3654):
 * the transcript has the message before the node's next sessions push says working, and in between the chat showed
 * neither the work line nor "Working". True for [AWAIT_TURN_MILLIS] after the message's time, which a phone clock behind
 * the hub's can stretch by up to as much again; never while the hub says working, waiting on you or offline, nor
 * after a failed start. Like the web, any message counts (typed in the terminal too): it starts a turn all the same.
 */
internal fun awaitingTurn(message: Item?, status: String?, startFailed: Boolean, now: java.time.Instant): Boolean {
    if (message == null || !mayBeCatchingUp(status, startFailed)) return false
    val at = parseTime(message.ts) ?: return false
    val age = java.time.Duration.between(at, now).toMillis()
    return age > -AWAIT_TURN_MILLIS && age < AWAIT_TURN_MILLIS
}

/**
 * Delivered but not in the transcript yet, while the session's status may still say idle: the status line says "Working"
 * for it too. No clock of its own: OutboxMessageSender drops a sent bubble after its grace (a minute, longer only
 * while the session is at work), and this goes with it.
 */
internal fun awaitingDelivered(messages: List<OutboxMessage>, status: String?, startFailed: Boolean): Boolean =
    mayBeCatchingUp(status, startFailed) && messages.any { it.state == DeliveryState.Sent }

/**
 * The session waits on you by the hub's word (#2483): its unified state says needs you, which also covers a question, plan
 * or permission a hook holds while herdr's status stays working (docs/protocol/agent-states.md), or its status says blocked
 * (an older hub without the state).
 */
internal fun Session?.needsYou(): Boolean = this?.status == "blocked" || this?.agentState == AgentState.NeedsYou

/**
 * The current turn waits on you rather than works (#2483): the session [needsYou], or one of its own requests is pending
 * here before the hub's state says so. A task agent's request (Persona's chat lists those too) does not count: this
 * session's own turn may still be at work. Whenever this holds, the header says "Waiting for you" too ([chatStatusState]).
 */
internal fun turnWaitsOnYou(session: Session?, key: SessionKey, requests: List<Interaction>): Boolean =
    session.needsYou() || requests.any { it.sessionKey == key }

internal fun chatStatusState(
    res: android.content.res.Resources,
    snapshot: persona.core.contracts.api.ChatSnapshot,
    session: Session?, machine: Machine?, messages: List<OutboxMessage>,
    requests: List<Interaction>, answering: Boolean,
    operationUnknown: Boolean = false,
    /** A turn of yours the hub has not reported yet ([awaitingTurn], [awaitingDelivered]). */
    awaiting: Boolean = false,
    submittingId: String? = null,
    receipts: List<MessageSendReceipt> = emptyList(),
): StatusLineState = StatusLineState(
    ended = snapshot.ended,
    replaced = snapshot.replaced,
    machineOffline = machine?.online == false,
    submitting = (submittingId != null && receipts.none { it.clientMessageId == submittingId && it.state != "sending" }) ||
        receipts.any { it.state == "sending" } ||
        messages.any { message -> message.state == DeliveryState.Sending && receipts.none { it.clientMessageId == message.clientMessageId && it.state != "sending" } },
    processing = receipts.any { it.processing },
    answering = answering,
    unconfirmed = operationUnknown || messages.any { it.state.resultUnknown } || receipts.any { it.state == "unknown" },
    pendingRequests = requests.size,
    blocked = session.needsYou(),
    requestSummary = requests.singleOrNull()?.headline?.shown,
    stopping = snapshot.stopping,
    working = session?.status == "working" || awaiting,
    waiting = session?.loop != null || !session?.background.isNullOrEmpty() || session?.runningWorkflows?.isNotEmpty() == true,
    waitingDetail = when {
        session?.loop != null -> res.getString(R.string.chat_state_looping)
        // a running workflow is in background too: counted once (#2935)
        (session?.backgroundCount ?: 0) > 0 -> session?.backgroundCount?.let { res.getQuantityString(R.plurals.chat_state_background, it, it) }
        else -> null
    },
)

internal fun chatStatus(
    res: android.content.res.Resources,
    snapshot: persona.core.contracts.api.ChatSnapshot,
    session: Session?, machine: Machine?, messages: List<OutboxMessage>,
    requests: List<Interaction>, answering: Boolean,
) = resolveStatusLine(chatStatusState(res, snapshot, session, machine, messages, requests, answering))

internal fun ChatLine.contentType(): String = when (this) {
    is ChatLine.Row -> row.type
    is ChatLine.Work -> "work"
    is ChatLine.Step -> when (step) { is WorkItem.Tool -> "tool"; is WorkItem.Folded -> "fold"; else -> "thought" }
    is ChatLine.End -> "work_end"
    is ChatLine.UserImages -> "images"
    is ChatLine.Wakes -> "wakes"
    is ChatLine.ApiError -> "api_error"
    is ChatLine.Recaps -> "recaps"
    is ChatLine.Viewed -> "viewed"
    is ChatLine.BotmuxReplies -> "botmux_replies"
    is ChatLine.Extra -> "extra"
    is ChatLine.ModelChange -> "model_change"
    is ChatLine.EngineChanged -> "engine_change"
    is ChatLine.ModeChange -> "mode_change"
}

internal fun interactionSource(res: android.content.res.Resources, interaction: Interaction, machine: Machine?): String = listOfNotNull(
    interaction.sessionTitle?.takeIf { it.isNotBlank() },
    machine?.takeIf { it.id == interaction.machineId }?.name?.takeIf { it.isNotBlank() } ?: res.getString(R.string.chat_state_unknown_machine),
).joinToString(" · ")

/** Where a request comes from, in the page's language. */
@androidx.compose.runtime.Composable
internal fun interactionSource(interaction: Interaction, machine: Machine?): String =
    interactionSource(androidx.compose.ui.platform.LocalResources.current, interaction, machine)

internal fun interactionDeadline(expires: java.time.Instant, now: java.time.Instant): String {
    return requireNotNull(persona.core.domain.answer.RequestExpiry.label(expires.toString(), now)).shown
}
