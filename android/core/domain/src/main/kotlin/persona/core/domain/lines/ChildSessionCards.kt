package persona.core.domain.lines

import persona.core.contracts.AgentState
import persona.core.contracts.Item
import persona.core.contracts.Session
import persona.core.contracts.parseTime
import java.time.Instant

/** Placement is independent of a child's live updates (web childPlaces/placeChildren/withChildSessions). */
class ChildSessionPlaces {
    private val firstSeen = mutableMapOf<String, String?>()

    fun places(rows: List<Session>): List<ChildPlace> = rows.map { row ->
        if (!firstSeen.containsKey(row.sessionId)) firstSeen[row.sessionId] = row.updatedAt
        ChildPlace(row.sessionId, parseTime(row.startedAt ?: firstSeen[row.sessionId]))
    }.sortedWith(compareBy<ChildPlace> { it.at ?: Instant.MAX }.thenBy { it.session })
}

data class ChildPlace(val session: String, val at: Instant?)

object ChildSessionCards {
    const val Prefix = "child:"
    fun call(session: String): String = Prefix + session
    fun session(call: ToolCall): String? = call.id.takeIf { it.startsWith(Prefix) }?.removePrefix(Prefix)

    fun status(row: Session): SubagentStatus = when {
        row.agentState in setOf(AgentState.Working, AgentState.NeedsYou) || row.backgroundWorking == true -> SubagentStatus.Running
        !row.turnError.isNullOrBlank() -> SubagentStatus.Failed
        row.status == "interrupted" -> SubagentStatus.Stopped
        else -> SubagentStatus.Completed
    }

    /** Chronological lines; older children wait for complete history, and remain inside collapsed work. */
    fun lines(lines: List<ChatLine>, children: List<ChildPlace>, complete: Boolean, open: Set<String>): List<ChatLine> {
        if (lines.isEmpty() || children.isEmpty()) return lines
        val inputs = lines.indices.filter { lines[it].isInput }
        val starts = (if (inputs.firstOrNull() == 0) inputs else listOf(0) + inputs).distinct()
        val byExchange = children.groupBy { child ->
            if (child.at == null) starts.last()
            else starts.lastOrNull { index -> start(lines[index])?.let { it <= child.at } == true }
                ?: starts.first().takeIf { complete }
        }
        val out = mutableListOf<ChatLine>()
        starts.forEachIndexed { n, from ->
            val exchange = lines.subList(from, starts.getOrNull(n + 1) ?: lines.size)
            val places = byExchange[from].orEmpty()
            if (places.isEmpty()) { out += exchange; return@forEachIndexed }
            val work = exchange.filterIsInstance<ChatLine.Work>().firstOrNull()
            val group = work?.id ?: "children-${exchange.first().id}"
            val steps = work?.steps.orEmpty().toMutableList()
            for (place in places) {
                val index = steps.indexOfLast { step -> step.start?.let { place.at == null || it <= place.at } == true } + 1
                val near = steps.getOrNull(index - 1) ?: steps.getOrNull(index)
                val id = call(place.session)
                val item = Item(id, ts = (near?.start ?: place.at)?.toString(), role = "assistant", kind = "tool_use",
                    toolName = "Agent", toolUseId = id)
                steps.add(index, WorkItem.Tool(ToolCall(LineItem(item), null, emptyList())))
            }
            val shown = (work ?: ChatLine.Work(group, emptyList(), false, start(exchange.first()), null, false))
                .copy(steps = steps, open = group in open)
            var inserted = false
            for (line in exchange) {
                if (line is ChatLine.Step && line.groupId == group || line is ChatLine.End && line.groupId == group) continue
                if (line == work || work == null && !inserted && !line.isInput && line !is ChatLine.UserImages) {
                    out += shown
                    if (shown.open) {
                        out += steps.mapIndexed { i, step -> ChatLine.Step(group, step, i == 0) }
                        out += ChatLine.End(group, steps.size)
                    }
                    inserted = true
                    if (line == work) continue
                }
                out += line
            }
            if (!inserted) {
                out += shown
                if (shown.open) out += steps.mapIndexed { i, step -> ChatLine.Step(group, step, i == 0) } + ChatLine.End(group, steps.size)
            }
        }
        return out
    }

    private fun start(line: ChatLine): Instant? = when (line) {
        is ChatLine.Row -> line.row.start
        is ChatLine.Work -> line.start
        is ChatLine.UserImages -> line.images.firstOrNull()?.time
        else -> null
    }
}
