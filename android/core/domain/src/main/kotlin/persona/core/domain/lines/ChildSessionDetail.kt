package persona.core.domain.lines

import kotlinx.coroutines.CancellationException
import persona.core.contracts.Item
import persona.core.contracts.api.ChatSnapshot
import persona.core.domain.parseContexts

/** The same bounded detail as web child-detail: the first prompt, latest words, and last call. */
data class ChildSessionDetail(val prompt: String?, val result: Item?, val lastTool: String?) {
    companion object {
        fun of(items: List<Item>) = ChildSessionDetail(
            items.asSequence().filter { it.role == "user" && it.kind == "text" && it.toolUseId == null }
                .map { parseContexts(it.text.orEmpty()).second.trim() }.firstOrNull { it.isNotEmpty() },
            items.lastOrNull { it.role == "assistant" && it.kind == "text" && !it.text.isNullOrBlank() },
            items.lastOrNull { it.kind == "tool_use" && !it.toolName.isNullOrBlank() }?.toolName,
        )
    }
}

/** Reads only while the card is open; token pushes reuse the already-read head and fold tail. */
class ChildSessionDetailReader(private val range: suspend (Int, Int) -> List<Item>) {
    private var head: List<Item>? = null
    private var tail: Pair<IntRange, List<Item>>? = null

    suspend fun read(snapshot: ChatSnapshot): ChildSessionDetail {
        val own = ChildSessionDetail.of(snapshot.items)
        if (snapshot.hasOlder && head == null) head = read(0, 40)
        val fold = snapshot.items.lastOrNull { it.kind == "fold" && it.fold != null }
        val call = snapshot.items.lastOrNull { it.kind == "tool_use" }
        val lastTool = if (fold != null && (call == null || fold.seq > call.seq)) {
            val to = requireNotNull(fold.fold).throughSeq
            val from = maxOf(fold.seq, to - 8)
            val bounds = from..to
            if (tail?.first != bounds) tail = bounds to read(from, to)
            ChildSessionDetail.of(tail?.second.orEmpty()).lastTool
        } else null
        return own.copy(prompt = head?.let { ChildSessionDetail.of(it).prompt } ?: own.prompt,
            lastTool = lastTool ?: own.lastTool)
    }

    private suspend fun read(from: Int, to: Int): List<Item> = try { range(from, to) }
    catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) { emptyList() }
}
