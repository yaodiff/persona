package persona.core.domain.lines

import persona.core.contracts.Item
import persona.core.contracts.TokenUsage
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import java.time.Duration

/**
 * What a finished exchange used (#5470, the web's `lib/turn-usage.ts` and `lib/turns.ts` `lineUsage` from #5426).
 * Every non-user item adds the tokens it carries, including a fold and Codex's `turn_duration` (those rows are not
 * drawn). A subagent's report (an entry with an id) counts once, at its largest counts, in the exchange that launched it.
 */

/** The tokens an exchange's items carry, in order, then each subagent it launched. [first]: no earlier exchange used any. */
data class TurnUsage(val entries: List<TokenUsage>, val first: Boolean = false)

/** One exchange after the chat's own placement of reports. [answerId] is its last reply, where the label is drawn. */
data class ExchangeUsage(val usage: TurnUsage?, val answerId: String?, val cut: Boolean = false)

private class Seg {
    var entry: String? = null
    val userIds = mutableListOf<String>()
    val items = mutableListOf<Item>()
    val entries = mutableListOf<TokenUsage>()
    var answerId: String? = null
    var cut = false
    var first = false
    fun usage(): TurnUsage? = if (entries.isEmpty()) null else TurnUsage(entries.toList(), first)
}

/** What each exchange carries on its own items, before reports are placed and before the first round is marked. */
fun segmentUsage(items: List<Item>): List<TurnUsage?> = split(readingOrder(items)).map { it.usage() }

/** What each exchange used once reports sit in the exchange that launched them, and which reply the label belongs to. */
fun chatUsage(items: List<Item>): List<ExchangeUsage> {
    val ordered = readingOrder(items)
    val segs = split(ordered)
    placeReports(segs, ordered)
    segs.firstOrNull { it.entries.isNotEmpty() }?.first = true
    return segs.map { ExchangeUsage(it.usage(), it.answerId, it.cut) }
}

/**
 * Model ids worth asking the catalog about. Empty when a model has no name or ran at the fast speed: the exchange
 * then has no price, and nothing is asked (the web's `TurnUsage` ids).
 */
fun catalogIds(summary: UsageSummary): List<String> =
    if (summary.models.all { it.model.isNotEmpty() && !it.fast }) summary.models.map { it.model }.distinct() else emptyList()

/**
 * The work time under a reply, only when the exchange has exactly one finished work line (the web's one work group).
 * Keyed by the reply item's id.
 */
fun replyUsageMillis(lines: List<ChatLine>): Map<String, Long> {
    val out = LinkedHashMap<String, Long>()
    val pending = mutableListOf<Long>()
    for (line in lines) when (line) {
        is ChatLine.Row -> when {
            line.row.isUserInput -> pending.clear()
            line.row.type == "reply" -> {
                val id = line.row.item?.id
                if (id != null && pending.size == 1) out[id] = pending[0]
                pending.clear()
            }
            else -> Unit
        }
        is ChatLine.Work -> if (!line.live && line.start != null && line.end != null) {
            val ms = Duration.between(line.start, line.end).toMillis()
            if (ms > 0) pending += ms
        }
        else -> Unit
    }
    return out
}

private fun readingOrder(items: List<Item>): List<Item> =
    items.withIndex().sortedWith(compareBy<IndexedValue<Item>> { it.value.seq }
        .thenBy { blockPart(it.value.id) }
        .thenBy { it.index }).map { it.value }

private fun split(items: List<Item>): List<Seg> {
    val attached = attachedAgentNotices(items)
    val out = mutableListOf<Seg>()
    var cur: Seg? = null
    for (it in items) {
        if (it.id in attached || viewFeedbackOnly(it)) continue
        if (isUserMessage(it)) {
            val entry = it.id.substringBefore('#')
            val open = cur
            if (open != null && open.entry == entry && open.items.isEmpty()) open.userIds += it.id
            else {
                val seg = Seg()
                seg.entry = entry
                seg.userIds += it.id
                out += seg
                cur = seg
            }
            continue
        }
        val seg = cur ?: Seg().also { cur = it; out += it }
        seg.items += it
        val own = itemUsage(it, reports = false)
        if (own.isNotEmpty()) seg.entries.addAll(own)
        if (it.role == "assistant" && it.kind == "text" && !it.text.isNullOrBlank()) seg.answerId = it.id
    }
    for (seg in out) {
        val at = seg.items.filter { !isInterrupt(it) }.maxOfOrNull { it.seq } ?: Int.MIN_VALUE
        seg.cut = seg.items.any { isInterrupt(it) && it.seq > at }
    }
    return out
}

private class ReportAcc(val id: String, val model: String?, val fast: Boolean) {
    var input = 0L
    var output = 0L
    var cacheRead = 0L
    var cacheWrite = 0L
    var cacheWrite1h = 0L
    var k = 0
    var launched = false

    fun take(u: TokenUsage) {
        input = maxOf(input, u.input.coerceAtLeast(0))
        output = maxOf(output, u.output.coerceAtLeast(0))
        cacheRead = maxOf(cacheRead, u.cacheRead.coerceAtLeast(0))
        cacheWrite = maxOf(cacheWrite, u.cacheWrite.coerceAtLeast(0))
        cacheWrite1h = maxOf(cacheWrite1h, u.cacheWrite1h.coerceAtLeast(0))
    }

    fun toUsage() = TokenUsage(id, model, input, output, cacheRead, cacheWrite, cacheWrite1h, fast)
}

private fun placeReports(segs: List<Seg>, items: List<Item>) {
    if (items.none { itemUsage(it, reports = true).isNotEmpty() }) return
    val at = HashMap<String, Int>()
    fun own(id: String?, k: Int) { if (!id.isNullOrEmpty() && id !in at) at[id] = k }
    val lastResult = HashMap<String, String>()
    for (it in items) {
        val call = it.toolUseId
        if (it.kind == "tool_result" && !call.isNullOrEmpty()) lastResult[call] = it.id
    }
    segs.forEachIndexed { k, seg ->
        for (id in seg.userIds) own(id, k)
        val leadingWake = seg.userIds.isEmpty() && seg.items.firstOrNull()?.let { isWake(it) } == true
        for (it in seg.items) {
            if (isInterrupt(it) || it.kind == "turn_duration" || it.kind == "tool_result") continue
            if (leadingWake && it.id == seg.items.first().id) continue
            if (it.kind == "text" && it.text.isNullOrBlank()) continue
            own(it.id, k)
            val call = it.toolUseId
            if (it.kind == "tool_use" && !call.isNullOrEmpty()) {
                own("call:$call", k)
                own(lastResult[call], k)
            }
        }
        own(seg.answerId, k)
    }
    val calls = taskCalls(items)
    val reports = LinkedHashMap<String, ReportAcc>()
    var here = 0
    for (it in items) {
        val owned = at[it.id]
        if (owned != null) here = owned
        val list = itemUsage(it, reports = true)
        if (list.isEmpty()) continue
        val launch = launchIndex(it, calls, at)
        for (u in list) {
            val id = u.id ?: continue
            val key = "$id\n${u.model.orEmpty()}\n${if (u.fast) 1 else 0}"
            val acc = reports.getOrPut(key) {
                ReportAcc(id, u.model?.takeIf { it.isNotEmpty() }, u.fast).also { row -> row.k = here }
            }
            acc.take(u)
            if (launch != null && (!acc.launched || launch < acc.k)) {
                acc.k = launch
                acc.launched = true
            }
        }
    }
    for (acc in reports.values) segs.getOrNull(acc.k)?.entries?.add(acc.toUsage())
}

private fun launchIndex(it: Item, calls: Map<String, String>, at: Map<String, Int>): Int? {
    for (call in listOfNotNull(it.toolUseId, it.task?.toolUseId, calls[it.id])) {
        val k = at["call:$call"]
        if (k != null) return k
    }
    return null
}

private fun itemUsage(it: Item, reports: Boolean): List<TokenUsage> =
    (it.usage + it.fold?.usage.orEmpty()).filter { it.report == reports }

private fun isUserMessage(it: Item) =
    it.role == "user" && (it.kind == "text" || (it.kind == "image" && it.toolUseId == null))

private fun isWake(it: Item) = it.kind == "notice" && it.wake == true

/** The transcript's "Interrupted" notice. Its tokens belong to the exchange it stopped; the notice stays its own line. */
private fun isInterrupt(it: Item) =
    it.kind == "notice" && it.task == null && (it.text ?: "").contains("中断") // i18n-ignore: the transcript's interrupt notice

private val viewErrors = Regex("""<persona-view-errors\b[^>]*>[\s\S]*?</persona-view-errors>\s*""")

private fun viewFeedbackOnly(it: Item): Boolean {
    val text = it.text ?: return false
    return it.role == "user" && it.kind == "text" && "<persona-view-errors" in text &&
        text.replace(viewErrors, "").isBlank()
}

private val launchedTask = Regex(
    """(?:running in background with ID:?|agentId:|Monitor started \(task:?|moved to the background \(ID:?)\s*([A-Za-z0-9_-]+)""",
)

private fun launchedTaskId(text: String?): String? = text?.let { launchedTask.find(it)?.groupValues?.getOrNull(1) }

/** The call that launched each item's task (the web's `taskCalls`). */
private fun taskCalls(items: List<Item>): Map<String, String> {
    val byTask = HashMap<String, String>()
    for (it in items) {
        val call = it.toolUseId ?: it.task?.toolUseId ?: continue
        val task = it.task?.taskId ?: if (it.kind == "tool_result") launchedTaskId(it.text) else null
        if (task != null) byTask[task] = call
    }
    val calls = HashMap<String, String>()
    for (it in items) {
        val task = it.task ?: continue
        val call = it.toolUseId ?: task.toolUseId ?: task.taskId?.let(byTask::get)
        if (call != null) calls[it.id] = call
    }
    return calls
}

/** Agent notices whose launching call is on the page: not a line of their own, and not counted where they sit. */
private fun attachedAgentNotices(items: List<Item>): Set<String> {
    val launches = items.filter { it.kind == "tool_use" && (it.toolName == "Agent" || it.toolName == "Task") }
        .mapNotNull { it.toolUseId }.toSet()
    val calls = taskCalls(items)
    return items.filter { it.kind == "notice" && it.task?.kind == "agent" && calls[it.id] in launches }
        .map { it.id }.toSet()
}

/** The usage as one string. The same usage is the same string. [first] is omitted when it is false. */
fun usageKey(usage: TurnUsage?): String? {
    usage ?: return null
    return buildJsonObject {
        put("entries", JsonArray(usage.entries.map { entryJson(it) }))
        if (usage.first) put("first", JsonPrimitive(true))
    }.toString()
}

internal fun decodeUsage(key: String): TurnUsage {
    val obj = Json.parseToJsonElement(key).jsonObject
    val rows = obj["entries"] as? JsonArray ?: throw IllegalArgumentException("entries")
    val entries = rows.map { el ->
        val row = el.jsonObject
        TokenUsage(
            id = row.text("id"),
            model = row.text("model"),
            input = row.long("in"),
            output = row.long("out"),
            cacheRead = row.long("cache_read"),
            cacheWrite = row.long("cache_write"),
            cacheWrite1h = row.long("cache_write_1h"),
            fast = (row["fast"] as? JsonPrimitive)?.booleanOrNull == true,
        )
    }
    return TurnUsage(entries, first = (obj["first"] as? JsonPrimitive)?.booleanOrNull == true)
}

private fun entryJson(u: TokenUsage) = buildJsonObject {
    val id = u.id
    val model = u.model
    if (!id.isNullOrEmpty()) put("id", JsonPrimitive(id))
    if (!model.isNullOrEmpty()) put("model", JsonPrimitive(model))
    if (u.input > 0) put("in", JsonPrimitive(u.input))
    if (u.output > 0) put("out", JsonPrimitive(u.output))
    if (u.cacheRead > 0) put("cache_read", JsonPrimitive(u.cacheRead))
    if (u.cacheWrite > 0) put("cache_write", JsonPrimitive(u.cacheWrite))
    if (u.cacheWrite1h > 0) put("cache_write_1h", JsonPrimitive(u.cacheWrite1h))
    if (u.fast) put("fast", JsonPrimitive(true))
}

private fun JsonObject.text(name: String) = (this[name] as? JsonPrimitive)?.contentOrNull
private fun JsonObject.long(name: String): Long {
    val n = (this[name] as? JsonPrimitive)?.longOrNull ?: return 0
    return if (n > 0) n else 0
}
