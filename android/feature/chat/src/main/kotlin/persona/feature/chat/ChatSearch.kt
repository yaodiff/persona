package persona.feature.chat

import android.content.res.Resources
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.domain.decodeXmlEntities
import persona.core.domain.injectedOf
import persona.core.domain.isInterjectContext
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.WorkItem
import persona.core.domain.messageText
import persona.core.domain.parseAttachedFiles
import persona.core.domain.parseContexts
import persona.core.domain.views.AssistantFences
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*

internal data class ChatSearchHit(val itemId: String, val category: String, val snippet: String, val ranges: List<IntRange>)
internal data class ChatSearchResults(val hits: List<ChatSearchHit> = emptyList(), val truncated: Boolean = false)

private fun JsonElement.strings(): Sequence<String> = when (this) {
    is JsonObject -> values.asSequence().flatMap { it.strings() }
    is JsonArray -> asSequence().flatMap { it.strings() }
    is JsonPrimitive -> if (isString) sequenceOf(content) else emptySequence()
}

private val viewErrors = Regex("""<persona-view-errors\b[^>]*>[\s\S]*?</persona-view-errors>\s*""")
private val cardTag = Regex("""\n<persona-action view="([^"<>]*)" action="([^"<>]*)">(\{[^<>]*\})</persona-action>$""")
private val snippetBreaks = Regex("""\s*\n\s*""")

/** At most this much of a chat's folded work is fetched for its search (text and tool input, in UTF-16 units). */
internal const val FOLD_SEARCH_BUDGET = 4 shl 20

/**
 * What of [this] search in chat matches, as `(group, text)` (spec/fixtures/chat-search): what the chat can show,
 * including behind a disclosure, and nothing it never shows. Groups are `text`, `quoted`, `thinking`, `tool_name`,
 * `tool_input`, `tool_output` and `event`.
 */
internal fun Item.searchFields(): List<Pair<String, String>> = buildList {
    fun add(group: String, text: String?) {
        if (!text.isNullOrBlank()) add(group to text)
    }
    when (kind) {
        "text" -> if (role == "user") {
            // A message Persona wrote for the model: the line the chat shows (and what a scheduled task asks).
            val injected = injectedOf(this@searchFields)
            if (injected != null) add("event", listOfNotNull(injected.line, injected.detail).joinToString("\n"))
            else userSearchFields(text.orEmpty()).forEach { (group, value) -> add(group, value) }
        } else add("text", assistantCardText(text.orEmpty()))
        "thinking" -> add("thinking", text)
        "tool_use" -> {
            add("tool_input", text)
            add("tool_name", toolName)
            add("tool_input", toolInput?.strings()?.joinToString("\n"))
        }
        "tool_result" -> add("tool_output", text)
        "notice", "recap" -> add("event", text)
        else -> return@buildList
    }
    task?.let { add("event", listOf(it.title, it.detail, it.status).filter { part -> !part.isNullOrBlank() }.joinToString("\n")) }
}

/** A reply with each card as the words it falls back to (sdk/go/wire AssistantCardText): its fallback, an old
 *  persona-card's title, never its JSON. */
internal fun assistantCardText(markdown: String): String {
    if (!AssistantFences.mentions(markdown)) return markdown
    val blocks = AssistantFences.blocks(markdown)
    if (blocks.isEmpty()) return markdown
    val out = StringBuilder()
    var at = 0
    for (block in blocks) {
        val title = if (block.language == AssistantFences.Card) jsonString(block.raw, "title") else ""
        val fallback = AssistantFences.fallback(block.raw).orEmpty()
        out.append(markdown.substring(at, block.start.coerceIn(0, markdown.length)))
        out.append(title.ifBlank { fallback })
        out.append('\n')
        at = block.end.coerceIn(0, markdown.length)
    }
    if (at < markdown.length) out.append(markdown.substring(at))
    return out.toString()
}

/** The user's words without the tags the bubble hides, a quoted context as its own field, and a file's name. */
private fun userSearchFields(raw: String): List<Pair<String, String>> {
    val stripped = if (raw.contains("<persona-view-errors")) viewErrors.replace(raw, "") else raw
    val (contexts, written) = parseContexts(stripped)
    val (files, rest) = parseAttachedFiles(written)
    val words = cardReplyText(messageText(rest, files = false).text)
    return buildList {
        if (words.isNotBlank()) add("text" to words)
        for (context in contexts) {
            if (context.isInterjectContext() || context.kind == "skill-instructions") continue
            val quoted = listOf(context.title, context.body).filter { it.isNotBlank() }.joinToString("\n")
            if (quoted.isNotBlank()) add("quoted" to quoted)
        }
        for (file in files) if (file.name.isNotBlank()) add("text" to file.name)
    }
}

/** A card's answer without the hub's trailing `<persona-action>` tag (an HTML card's `html: true` included). */
private fun cardReplyText(message: String): String {
    val match = cardTag.find(message) ?: return message
    val payload = runCatching { JsonCodec.parseToJsonElement(match.groupValues[3]) }.getOrNull() as? JsonObject ?: return message
    if (payload.keys.any { it != "values" && it != "html" }) return message
    if ("html" in payload && (payload["html"] as? JsonPrimitive)?.booleanOrNull != true) return message
    val values = payload["values"] ?: JsonObject(emptyMap())
    if (values !is JsonObject || values.any { (_, value) -> (value as? JsonPrimitive)?.isString != true }) return message
    if (decodeXmlEntities(match.groupValues[1]).isEmpty() || decodeXmlEntities(match.groupValues[2]).isEmpty()) return message
    return message.substring(0, match.range.first)
}

private fun jsonString(raw: String, name: String): String {
    val obj = runCatching { JsonCodec.parseToJsonElement(raw) }.getOrNull() as? JsonObject ?: return ""
    return (obj[name] as? JsonPrimitive)?.contentOrNull?.trim().orEmpty()
}

private fun Resources.searchCategory(group: String): String = getString(when (group) {
    "text" -> R.string.chat_search_category_text
    "quoted" -> R.string.chat_search_category_quoted
    "thinking" -> R.string.chat_search_category_thinking
    "tool_name" -> R.string.chat_search_category_tool_name
    "tool_input" -> R.string.chat_search_category_tool_input
    "tool_output" -> R.string.chat_search_category_tool_output
    else -> R.string.chat_search_category_event
})

/** Literal matching preserves UTF-16 offsets used by Compose text spans; [res] names each hit's category. */
internal suspend fun searchChat(res: Resources, items: List<Item>, query: String): ChatSearchResults {
    val needle = query.trim()
    if (needle.isEmpty()) return ChatSearchResults()
    val hits = mutableListOf<ChatSearchHit>()
    // An exact duplicate (the live item, then the same step cached from a fold) keeps the first copy. The same id at a
    // newer seq wins once the list is ordered newest first.
    val unique = LinkedHashMap<String, Item>()
    for (item in items) unique.putIfAbsent("${item.id}\u0000${item.seq}", item)
    val seen = HashSet<String>()
    for (item in unique.values.sortedWith(chatItemOrder).asReversed()) {
        if (!seen.add(item.id)) continue
        currentCoroutineContext().ensureActive()
        for ((group, text) in item.searchFields()) {
            val category = res.searchCategory(group)
            val first = text.indexOf(needle, ignoreCase = true)
            if (first < 0) continue
            if (hits.size == 100) return ChatSearchResults(hits, truncated = true)
            var start = (first - 45).coerceAtLeast(0)
            var end = (first + needle.length + 110).coerceAtMost(text.length)
            if (start > 0 && text[start].isLowSurrogate()) start--
            if (end < text.length && text[end].isLowSurrogate()) end++
            val body = snippetBreaks.replace(text.substring(start, end), " ")
            val snippet = (if (start > 0) "…" else "") + body + (if (end < text.length) "…" else "")
            val ranges = mutableListOf<IntRange>()
            var at = snippet.indexOf(needle, ignoreCase = true)
            while (at >= 0) {
                ranges += at until at + needle.length
                at = snippet.indexOf(needle, at + needle.length, ignoreCase = true)
            }
            hits += ChatSearchHit(item.id, category, snippet, ranges)
        }
    }
    return ChatSearchResults(hits)
}

internal data class ChatSearchTarget(val rowId: String, val expand: Set<String> = emptySet())

private fun WorkItem.containsItem(id: String): Boolean = when (this) {
    is WorkItem.Tool -> call.use.id == id || call.results.any { it.id == id }
    is WorkItem.Thought -> item.id == id
    is WorkItem.Narration -> item.id == id
    is WorkItem.Folded -> item.id == id
}

internal fun searchTarget(lines: List<ChatLine>, itemId: String): ChatSearchTarget? {
    // Prefer visible rows over the same items retained in work summaries.
    lines.forEach { line ->
        if (line is ChatLine.Row && line.row.id == itemId) return ChatSearchTarget(line.contentKey)
        if (line is ChatLine.Step && line.step.containsItem(itemId)) return ChatSearchTarget(line.id)
        if (line is ChatLine.ApiError && line.item.id == itemId) return ChatSearchTarget(line.id)
        if (line is ChatLine.Recaps && line.items.any { it.id == itemId }) return ChatSearchTarget(line.id)
    }
    lines.forEach { line ->
        when (line) {
            is ChatLine.Work -> line.steps.firstOrNull { it.containsItem(itemId) }?.let {
                return ChatSearchTarget("${line.id}/${it.id}", setOf(line.id))
            }
            else -> Unit
        }
    }
    return null
}

/** The next step toward a search hit: open the work line that holds it, wait while its fold loads, or scroll. */
internal sealed interface SearchGo {
    data class Expand(val groups: Set<String>) : SearchGo
    data class Scroll(val rowId: String) : SearchGo
    /** The fold is open and its steps are still on the way. */
    data object Waiting : SearchGo
    data object Missing : SearchGo
}

/** Where to go for [itemId]. [foldId] is the unopened fold the hit was fetched from; [foldFailed] are folds that did not load. */
internal fun searchGo(lines: List<ChatLine>, itemId: String, expanded: Set<String>, foldId: String?, foldFailed: Set<String>): SearchGo {
    val target = searchTarget(lines, itemId)
    if (target != null) {
        if (!expanded.containsAll(target.expand)) return SearchGo.Expand(expanded + target.expand)
        return SearchGo.Scroll(target.rowId)
    }
    if (foldId != null && foldId !in foldFailed) {
        val line = foldWorkLine(lines, foldId) ?: return SearchGo.Missing
        if (line !in expanded) return SearchGo.Expand(expanded + line)
        return SearchGo.Waiting
    }
    return SearchGo.Missing
}

/** The work line that shows [foldId], including one inside a wake drawer. */
internal fun foldWorkLine(lines: List<ChatLine>, foldId: String): String? {
    fun visit(list: List<ChatLine>): String? {
        for (line in list) when (line) {
            is ChatLine.Work -> if (line.steps.any { it is WorkItem.Folded && it.item.id == foldId }) return line.id
            is ChatLine.Wakes -> visit(line.drawer)?.let { return it }
            else -> Unit
        }
        return null
    }
    return visit(lines)
}

/** How much a page of a fold's items counts against [FOLD_SEARCH_BUDGET]. */
internal fun foldItemsSize(items: List<Item>): Int {
    var n = 0
    for (item in items) {
        n += item.text?.length ?: 0
        if (item.toolInput != null) n += item.toolInput.toString().length
    }
    return n
}

internal data class FoldFetchOutcome(val failed: Int, val capped: Boolean, val stopped: Boolean)

/**
 * Fetches [folds] for search without opening them: newest first, [concurrency] at a time, each through [load]
 * (the pages a fold's own opening reads), until [budget] is spent or [stopped]. A fold that fails counts and the
 * others go on.
 */
internal suspend fun fetchFoldsForSearch(
    folds: List<Item>,
    load: suspend (Item) -> List<Item>,
    onFold: (Item, List<Item>) -> Unit,
    stopped: () -> Boolean,
    budget: Int = FOLD_SEARCH_BUDGET,
    concurrency: Int = 2,
): FoldFetchOutcome {
    val queue = folds.filter { it.kind == "fold" && it.fold != null }.sortedByDescending { it.seq }.toMutableList()
    val lock = Mutex()
    var left = budget
    var failed = 0
    var capped = false
    coroutineScope {
        repeat(concurrency.coerceAtLeast(1)) {
            launch {
                while (currentCoroutineContext().isActive) {
                    val fold = lock.withLock {
                        if (stopped() || left <= 0) {
                            if (left <= 0 && queue.isNotEmpty()) capped = true
                            queue.clear()
                            null
                        } else queue.removeFirstOrNull()
                    } ?: return@launch
                    if (stopped()) return@launch
                    try {
                        val items = load(fold)
                        if (stopped()) return@launch
                        lock.withLock { left -= foldItemsSize(items) }
                        onFold(fold, items)
                    } catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) { lock.withLock { failed++ } }
                }
            }
        }
    }
    return FoldFetchOutcome(failed, capped, stopped())
}

/** One fold's steps, the pages [ChatRepository.expandFold] reads, without opening it. */
internal suspend fun loadFoldForSearch(repository: ChatRepository, key: SessionKey, fold: Item): List<Item> {
    val through = fold.fold?.throughSeq ?: return emptyList()
    if (through < fold.seq) return emptyList()
    return repository.loadRange(key, fold.seq, through)
}

/** Only the existing continuous history cursor is used; no disjoint search window is merged. */
internal suspend fun loadEarlierForSearch(res: Resources, repository: ChatRepository, key: SessionKey, onPage: (Int) -> Unit): String? {
    repeat(20) { page ->
        val before = repository.observe(key).first()
        if (!before.hasOlder) return null
        if (before.loading || before.loadingOlder || !before.verified) return res.getString(R.string.chat_search_updating)
        repository.loadOlder(key)
        val after = repository.observe(key).first { !it.loadingOlder }
        if (after.error != null) return res.getString(R.string.chat_search_older_failed)
        if (!after.verified || (after.items.minOfOrNull { it.seq } ?: Int.MAX_VALUE) >=
            (before.items.minOfOrNull { it.seq } ?: Int.MAX_VALUE)) return res.getString(R.string.chat_search_older_none)
        onPage(page + 1)
        if (!after.hasOlder) return null
    }
    return res.getString(R.string.chat_search_older_limit, 20)
}
