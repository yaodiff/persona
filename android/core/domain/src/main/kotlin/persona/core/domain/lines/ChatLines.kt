package persona.core.domain.lines

import java.time.Instant

/** Pure transcript folding, in reading order. Live state is supplied by the session owner. */
fun chatLines(items: List<LineItem>, options: LineOptions = LineOptions()): ChatSnapshot {
    val source = lineSource(items, options)
    val blocks = lineBlocks(source)
    val live = liveTurnId(blocks, options)
    val builder = LineBuilder(live, options)
    return chatSnapshot(source, blocks, blocks.flatMap(builder::lines), live, options)
}

/** The items [chatLines] draws, in reading order; a frozen window's up to the item it froze at. */
internal fun lineSource(items: List<LineItem>, options: LineOptions): List<LineItem> {
    val frozen = options.frozen
    return withoutViewFeedback(items).sortedWith(itemOrder).let { ordered ->
        if (frozen == null) ordered else ordered.takeWhile {
            positionCompare(it.seq, it.part, frozen.seq, frozen.part) <= 0
        }
    }
}

/**
 * What spans the whole chat, from its blocks and their lines: [chatLines]' and [ChatLinesMemo]'s, which keeps the lines
 * of unchanged turns.
 */
internal fun chatSnapshot(
    source: List<LineItem>, blocks: List<Block>, blockLines: List<ChatLine>, live: String?, options: LineOptions,
): ChatSnapshot {
    // Working with nothing of this turn yet (the last block is a recap): the work line still shows, as on the web.
    val lines = if (live == null && options.live && options.frozen == null && options.liveTurnId == null)
        blockLines + ChatLine.Work(LiveWorkId, emptyList(), true, null, null, workOpen(LiveWorkId, options))
    else blockLines
    // As iOS does: API errors become cards and recaps in a row one line, before open work lines unfold.
    return ChatSnapshot(LineBuilder(live, options).unfold(foldRecaps(foldApiErrors(lines))), blocks.turns(),
        TaskIndex.build(source), options.pendingInteractionIds?.let(::ChatFooter), live)
}

/**
 * [chatLines]' turns alone, built by the same code: for callers that need where turns begin (the items cache keeps the
 * last two) and not the lines drawn from them, the task index or the folds.
 */
fun chatTurns(items: List<LineItem>): List<ChatTurn> = lineBlocks(withoutViewFeedback(items).sortedWith(itemOrder)).turns()

private val viewFeedbackRe = Regex("""<persona-view-errors\b[^>]*>[\s\S]*?</persona-view-errors>\s*""")

/**
 * The node's `<persona-view-errors>` feedback after the assistant's snapshot (its last rich views failed their check) is
 * for the model, not the user's words: an older hub (#4315) gave it as a user item of its own, the line's block before
 * the message, or at the start of the message, and the bubble showed its raw tags. Taken out of user text; an item
 * left with nothing goes.
 */
fun withoutViewFeedback(items: List<LineItem>): List<LineItem> {
    fun carries(it: LineItem) = it.item.role == "user" && it.kind == "text" && it.item.text?.contains("<persona-view-errors") == true
    if (items.none(::carries)) return items
    return items.mapNotNull { line ->
        if (!carries(line)) return@mapNotNull line
        val left = viewFeedbackRe.replace(line.item.text.orEmpty(), "")
        if (left.isBlank()) null else line.copy(item = line.item.copy(text = left))
    }
}

private fun lineBlocks(source: List<LineItem>): List<Block> = buildBlocks(buildLineRows(foldCompactions(source)))

internal fun List<Block>.turns(): List<ChatTurn> = filterIsInstance<Block.Turn>().map { it.turn }

/** Running and historical work lines start collapsed; their owner supplies explicit persisted choices. */
fun workOpen(id: String, options: LineOptions): Boolean = id in options.openWork

/** A session status in which the last turn is still running: working, or blocked on you (iOS `isActive`). */
fun isLiveStatus(status: String?): Boolean = status == "working" || status == "blocked"

/** The id of the work line drawn while the agent works and no turn of its own is in the transcript yet. */
const val LiveWorkId = "w-live"

/**
 * The running turn. A frozen window and an explicit id keep the turn they name; otherwise, while the agent is at work,
 * the last block is the running turn if it is one (iOS `liveTurnID`, the web's `chatLines`). Guessing it from the last
 * user message misses a turn that took a queued message in (its id is its first message's), one a notice or a wake
 * opened, and a recap at the end.
 */
internal fun liveTurnId(blocks: List<Block>, options: LineOptions): String? = when {
    options.frozen != null -> options.frozen.liveTurnId
    options.liveTurnId != null -> options.liveTurnId
    options.live -> (blocks.lastOrNull() as? Block.Turn)?.turn?.id
    else -> null
}

internal fun buildLineRows(items: List<LineItem>): List<ChatRow> {
    val taskCalls = TaskIndex.build(items).calls
    fun resultCall(item: LineItem): String? = when {
        item.kind == "tool_result" -> item.item.toolUseId ?: taskCalls[item.id]
        item.kind == "notice" && item.task?.kind == "agent" -> taskCalls[item.id]
        else -> null
    }
    val results = items.mapNotNull { item -> resultCall(item)?.let { it to item } }
        .groupBy({ it.first }, { it.second })
    val images = items.filter { it.kind == "image" && it.item.toolUseId != null && it.item.image != null }
        .groupBy { it.item.toolUseId }
    val rows = mutableListOf<ChatRow>()
    val run = mutableListOf<WorkItem>()
    val thoughts = mutableListOf<LineItem>()
    fun flushThoughts(asRows: Boolean) {
        if (asRows) rows += thoughts.map { ChatRow("thinking", it) }
        else run += thoughts.map { WorkItem.Thought(it) }
        thoughts.clear()
    }
    fun flushRun() {
        if (run.isNotEmpty()) rows += ChatRow("tools", steps = run.toList())
        run.clear()
    }
    for (it in items) {
        when (it.kind) {
            "tool_use" -> {
                flushThoughts(run.isEmpty())
                val callId = it.item.toolUseId
                val reports = results[callId].orEmpty()
                run += WorkItem.Tool(ToolCall(it, reports.lastOrNull(), images[callId].orEmpty(), reports))
            }
            "thinking" -> thoughts += it
            "fold" -> {
                flushThoughts(run.isEmpty())
                run += WorkItem.Folded(it)
            }
            "tool_result" -> if (it.task?.kind == "agent" && it.item.wake == true) {
                // A subagent's report that woke the idle agent: drawn under its call, and where the next turn starts.
                flushThoughts(run.isEmpty())
                flushRun()
                rows += ChatRow(WakeRow, it)
            }
            else -> {
                if (resultCall(it) != null || (it.kind == "image" && it.item.toolUseId != null)) continue
                flushThoughts(run.isEmpty())
                flushRun()
                val type = when (it.kind) {
                    "text" -> if (it.item.role == "user") "user" else "reply"
                    "notice", "recap", "image" -> it.kind
                    else -> continue
                }
                rows += ChatRow(type, it)
            }
        }
    }
    flushThoughts(run.isEmpty())
    flushRun()
    return rows
}

/** A subagent's report that woke the idle agent (a `tool_result` with `wake`): it opens a turn and draws no line. */
internal const val WakeRow = "wake"

/** What woke the idle agent (docs/protocol/protocol.md `wake`): a background task's notice or a subagent's report. */
private val ChatRow.isWake get() = (type == "notice" || type == WakeRow) && item?.item?.wake == true

internal sealed interface Block {
    data class Row(val row: ChatRow) : Block
    data class Turn(val turn: ChatTurn) : Block
}

private class RawTurn(val id: String) {
    val users = mutableListOf<ChatRow>()
    val process = mutableListOf<ChatRow>()
    val trailing = mutableListOf<ChatRow>()
    val wakes = mutableListOf<LineItem>()
    val ordered = mutableListOf<ChatRow>()

    fun finish(): ChatTurn {
        val order = ordered.toList()
        var work = process.toList()
        var afterWake = emptyList<ChatRow>()
        var tail = emptyList<ChatRow>()
        if (wakes.isNotEmpty()) {
            // A woken turn opens with its wake: what the wake led to, and what came after the turn's last words.
            // Both stay in ordered/process, where the lines are drawn from.
            afterWake = order.filterNot { it.isUserInput }
            val lastReply = order.indexOfLast { it.type == "reply" && !it.item?.text.isNullOrBlank() }
            if (lastReply >= 0) tail = order.drop(lastReply + 1).filterNot { it.isUserInput }
        }
        val steps = work
        val answerIndex = work.indexOfLast { it.type == "reply" && !it.item?.text.isNullOrBlank() }
        val answer = work.getOrNull(answerIndex)
        work = work.filterIndexed { i, _ -> i != answerIndex }
        return ChatTurn(id, users.toList(), work, answer, trailing.toList(), wakes.toList(), afterWake, tail, order,
            (users + steps).firstNotNullOfOrNull { it.start },
            (steps + trailing).asReversed().firstNotNullOfOrNull { it.end })
    }
}

internal fun buildBlocks(rows: List<ChatRow>): List<Block> {
    val blocks = mutableListOf<Block>()
    var current: RawTurn? = null
    fun close() {
        val raw = current ?: return
        blocks += Block.Turn(raw.finish())
        current = null
    }
    // A background task woke the idle agent: the turn before it was over, and the last words of that turn stay its
    // answer. The work it woke is a turn of its own that opens with the notice, where a message would be (#9140: kept
    // in the turn before it, each woken turn's last words took the answer's place and the real answer folded into the
    // work). After a recap it follows the recap.
    fun wake(row: ChatRow) {
        close()
        current = RawTurn(row.id).also { it.wakes += row.item!!; it.process += row; it.ordered += row }
    }
    for (row in rows) {
        when (row.type) {
            "user" -> {
                if (row.item?.item?.queued != true || current == null) {
                    close()
                    current = RawTurn(row.id)
                }
                current!!.users += row
                current!!.ordered += row
            }
            "recap" -> {
                close()
                blocks += Block.Row(row)
            }
            WakeRow -> wake(row)
            "notice" -> {
                val item = row.item!!
                if (item.item.modelRefusal != null) {
                    // #2528: an upstream refusal (and the model Claude Code moved to) is a line of its own after the
                    // exchange, never folded into the work row where it would go unseen (web buildBlocks)
                    if (current == null) current = RawTurn(row.id)
                    current!!.trailing += row
                } else if (row.isWake) {
                    wake(row)
                } else if (current == null) {
                    current = RawTurn(row.id).also { it.process += row; it.ordered += row }
                } else if (item.task == null && item.text.orEmpty().contains("中断")) { // i18n-ignore: the hub's interrupt notice
                    current!!.trailing += row
                } else {
                    current!!.process += row
                    current!!.ordered += row
                }
            }
            else -> {
                if (current == null) current = RawTurn(row.exchangeId)
                current!!.process += row
                current!!.ordered += row
            }
        }
    }
    close()
    return blocks
}

private data class Segment(val user: ChatRow?, val rows: List<ChatRow>)
private fun segments(ordered: List<ChatRow>): List<Segment> {
    val out = mutableListOf<Segment>()
    var user: ChatRow? = null
    var rows = mutableListOf<ChatRow>()
    var started = false
    for (row in ordered) {
        if (row.type == "user") {
            if (started) out += Segment(user, rows)
            user = row
            rows = mutableListOf()
        } else rows += row
        started = true
    }
    if (started) out += Segment(user, rows)
    return out
}

private val ChatRow.isCompaction get() = type == "notice" && item?.compaction != null

private fun photos(rows: List<ChatRow>): List<LineItem> = rows.filter {
    it.type == "image" && it.isUserInput && it.item?.item?.image != null
}.map { it.item!! }

private fun workItems(rows: List<ChatRow>): List<WorkItem> = rows.flatMap {
    when (it.type) {
        "thinking" -> if (it.item?.text.isNullOrBlank()) emptyList() else listOf(WorkItem.Thought(it.item!!))
        "tools" -> it.steps
        "notice" -> listOf(WorkItem.Thought(it.item!!))
        "reply" -> listOf(WorkItem.Narration(it.item!!))
        else -> emptyList()
    }
}

internal class LineBuilder(val liveId: String?, val options: LineOptions) {
    fun unfold(lines: List<ChatLine>): List<ChatLine> = lines.flatMap { line ->
        if (line is ChatLine.Work && line.open && line.steps.isNotEmpty()) {
            listOf(line) + line.steps.mapIndexed { i, step -> ChatLine.Step(line.id, step, i == 0) } +
                ChatLine.End(line.id, line.steps.sumOf {
                    if (it is WorkItem.Folded) it.item.fold?.let { f -> f.tools + f.thoughts } ?: 1 else 1
                })
        } else listOf(line)
    }

    fun lines(block: Block): List<ChatLine> = when (block) {
        is Block.Row -> listOf(ChatLine.Row(block.row))
        is Block.Turn -> turnLines(block.turn)
    }

    private fun work(id: String, steps: List<WorkItem>, live: Boolean, start: Instant?, end: Instant?) =
        ChatLine.Work(id, steps, live, start, end, workOpen(id, options))

    fun turnLines(turn: ChatTurn): List<ChatLine> = buildList {
        var ordered = turn.ordered
        // Work a background task woke the agent for opens with that notice, where a message would be (a subagent's
        // report that woke it is drawn under its call, so that one opens with the work).
        val woke = ordered.firstOrNull()?.takeIf { it.isWake }
        if (woke != null) {
            if (woke.type == "notice") add(ChatLine.Row(woke))
            ordered = ordered.drop(1)
        }
        val wokeAt = woke?.start
        // Woken and at work with nothing of it here yet: its work line.
        val segments = segments(ordered).ifEmpty {
            if (woke != null && turn.id == liveId) listOf(Segment(null, emptyList())) else emptyList()
        }
        for ((k, seg) in segments.withIndex()) {
            // The work a wake opened is known by the wake: its line keeps its id from before its first step.
            val opening = if (k == 0 && seg.user == null) woke?.id else null
            val live = turn.id == liveId && k == segments.lastIndex
            val photos = photos(seg.rows)
            if (photos.isNotEmpty()) add(ChatLine.UserImages(photos))
            seg.user?.let { add(ChatLine.Row(it)) }
            val last = seg.rows.indexOfLast { it.type == "reply" && !it.item?.text.isNullOrBlank() }
            // A compaction after the answer (idle, a /compact of its own) is a divider after it, not a step hidden in
            // the turn's work line.
            val after = if (last < 0) emptySet() else
                seg.rows.indices.filter { it > last && seg.rows[it].isCompaction }.toSet()
            val rows = seg.rows.filterIndexed { i, _ -> i != last && i !in after }
            val steps = workItems(rows)
            val id = "w-" + (seg.user?.id ?: opening ?: seg.rows.firstOrNull()?.exchangeId ?: turn.id)
            if (!live && steps.isNotEmpty() && steps.all { it is WorkItem.Thought && it.item.compaction != null }) {
                // Only compactions: dividers across the chat, no work line to open.
                addAll(rows.filter { it.isCompaction }.map { ChatLine.Row(it) })
            } else if (steps.isNotEmpty() || live) {
                add(work(id, steps, live, seg.user?.start ?: (if (k == 0) wokeAt else null) ?:
                    seg.rows.firstOrNull()?.start ?: turn.start, seg.rows.lastOrNull()?.end))
            }
            // Between the work and the answer: what the work looked at.
            ViewedImages.of(steps).takeIf { it.isNotEmpty() }?.let { add(ChatLine.Viewed("vi-" + id.removePrefix("w-"), it)) }
            // Only the latest text stays outside the summary, in one stable slot throughout the turn.
            seg.rows.getOrNull(last)?.let { add(ChatLine.Row(it, contentKey = "$id/latest-text")) }
            // Then what it sent to Lark (a BotMux session), after the turn's last words (web SegmentView).
            botmuxReplies(steps).takeIf { it.isNotEmpty() }?.let { add(ChatLine.BotmuxReplies("bm-" + id.removePrefix("w-"), it)) }
            addAll(after.sorted().map { ChatLine.Row(seg.rows[it]) })
        }
        addAll(turn.trailing.map { ChatLine.Row(it) })
    }
}

/** Synchronous state operations make each result a complete snapshot, including late tool results. */
class LineStore {
    var items: List<LineItem> = emptyList()
        private set

    fun merge(page: List<LineItem>) {
        val folds = page.filter { f -> f.kind == "fold" && items.none { it.id == f.id && it.fold == f.fold } }
        val retained = items.filterNot { it.isWork && folds.any { fold -> fold.covers(it) } }
        items = (retained + page).distinctBy { it.id }.sortedWith(itemOrder)
    }

    fun replace(page: List<LineItem>) {
        items = page.distinctBy { it.id }.sortedWith(itemOrder)
    }

    fun unfold(foldId: String, page: List<LineItem>) {
        if (items.none { it.id == foldId }) return
        items = items.filterNot { it.id == foldId }
        merge(page.filterNot { it.kind == "fold" })
    }

    fun snapshot(options: LineOptions = LineOptions()): ChatSnapshot = chatLines(items, options)
}
