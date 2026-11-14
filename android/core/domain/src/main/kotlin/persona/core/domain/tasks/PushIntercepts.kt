package persona.core.domain.tasks

import java.net.URI
import java.time.Clock
import java.time.Instant
import java.time.LocalDate
import java.time.format.DateTimeFormatter
import persona.core.contracts.PushIntercept

/** View-ready held pushes, computed once per response rather than in a composable body. */
data class HeldPushItem(
    val id: String,
    val date: Instant,
    val record: PushIntercept,
    val summaries: List<String>,
)

data class HeldPushGroups(val p1: List<HeldPushItem> = emptyList(), val p2: List<HeldPushItem> = emptyList()) {
    val total: Int get() = p1.size + p2.size
}

sealed interface HeldPushDestination {
    data class Todo(val id: String) : HeldPushDestination
    data class Session(val machineId: String, val sessionId: String, val group: String? = null) : HeldPushDestination
    data class Url(val url: String) : HeldPushDestination
}

private fun oneLine(value: String?): String = value.orEmpty().trim().replace(Regex("\\s+"), " ")

fun heldPushSummaries(record: PushIntercept): List<String> {
    val primary = listOf(record.summary, record.title, record.body).map(::oneLine).firstOrNull { it.isNotEmpty() }.orEmpty()
    val merged = record.also.orEmpty().map(::oneLine).filter(String::isNotEmpty)
    val all = if (primary.isEmpty()) merged else listOf(primary) + merged
    return all.ifEmpty { listOf("") }
}

private fun instant(value: String): Instant? = runCatching {
    Instant.from(DateTimeFormatter.ISO_OFFSET_DATE_TIME.parse(value))
}.getOrNull()

fun heldPushGroups(records: List<PushIntercept>, clock: Clock = Clock.systemDefaultZone()): HeldPushGroups {
    val today = LocalDate.now(clock)
    val occurrences = mutableMapOf<String, Int>()
    val p1 = mutableListOf<HeldPushItem>()
    val p2 = mutableListOf<HeldPushItem>()
    records.forEach { record ->
        if (record.level != "P1" && record.level != "P2") return@forEach
        if (record.reason.isBlank()) return@forEach
        val date = instant(record.at) ?: return@forEach
        if (date.atZone(clock.zone).toLocalDate() != today) return@forEach
        val base = listOf(record.at, record.level, record.reason, record.relatedKey.orEmpty(), record.manager.orEmpty()).joinToString("\u001e")
        val occurrence = occurrences[base] ?: 0
        occurrences[base] = occurrence + 1
        val item = HeldPushItem(if (occurrence == 0) base else "$base#$occurrence", date, record, heldPushSummaries(record))
        if (record.level == "P1") p1 += item else p2 += item
    }
    return HeldPushGroups(p1, p2)
}

private fun httpUrl(value: String?): String? {
    value ?: return null
    val uri = runCatching { URI(value) }.getOrNull() ?: return null
    return if ((uri.scheme.equals("https", true) || uri.scheme.equals("http", true)) && !uri.host.isNullOrBlank()) uri.toString() else null
}

/** Typed destinations first; only a known todo and an absolute http(s) related key are legacy-safe. */
fun heldPushDestination(record: PushIntercept, knownTodoIds: Set<String>): HeldPushDestination? {
    record.todoId?.trim()?.takeIf(knownTodoIds::contains)?.let { return HeldPushDestination.Todo(it) }
    val machine = record.machineId?.trim().orEmpty()
    val session = record.sessionId?.trim().orEmpty()
    if (machine.isNotEmpty() && session.isNotEmpty()) return HeldPushDestination.Session(machine, session, record.group)
    httpUrl(record.url)?.let { return HeldPushDestination.Url(it) }
    val related = record.relatedKey.orEmpty().trim()
    if (related.startsWith("todo:")) {
        val id = related.removePrefix("todo:").substringBefore('@')
        if (id in knownTodoIds) return HeldPushDestination.Todo(id)
    }
    return httpUrl(related)?.let(HeldPushDestination::Url)
}
