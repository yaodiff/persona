package persona.feature.assistant

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.Stable
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import persona.core.contracts.Session
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantSearchAnswer
import persona.core.contracts.api.AssistantSearchHit
import persona.core.contracts.api.AssistantSearchResult
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive

/** The hub feature for finding a chat by what was said in it (`GET /api/v1/assistant/search`). */
internal const val AssistantSearchFeature = "assistant_search"

/** How long a query sits before it is asked. A newer query cancels the one in flight. */
internal const val SEARCH_SETTLE_MS = 200L

/** Characters kept before the first match. A phone row shows about 17, and the manager's snippet can start 40 before. */
internal const val SNIPPET_LEAD = 8

/** A chat the drawer lists for a search, with the message it was found by. [hit] is null for a title or a local match. */
internal data class FoundChat(val session: Session, val hit: AssistantSearchHit?)

/**
 * A row for a result the app has not loaded (an archived chat whose list is still closed). Only when it can be opened:
 * both ids are present. The row uses the result's own title, computer, archived mark and time.
 */
internal fun sessionFromSearch(result: AssistantSearchResult): Session? {
    if (result.sessionId.isBlank() || result.machineId.isBlank()) return null
    return Session(machineId = result.machineId, sessionId = result.sessionId, title = result.title,
        archived = result.archived, updatedAt = result.updatedAt, origin = result.origin, assistant = true)
}

/**
 * The manager's results first (a known chat, or one built from the result when the app has not loaded it, with its
 * newest hit; a title match opens as usual), then the local matches it did not return, in the order they already had.
 * No answer yet: the local matches alone, in that same list.
 */
internal fun mergeAssistantSearch(local: List<Session>, answer: AssistantSearchAnswer?, known: List<Session>): List<FoundChat> {
    if (answer == null) return local.map { FoundChat(it, null) }
    val byId = LinkedHashMap<String, Session>()
    for (session in known) byId.putIfAbsent(session.sessionId, session)
    val out = ArrayList<FoundChat>()
    val seen = HashSet<String>()
    for (result in answer.results) {
        if (!seen.add(result.sessionId)) continue
        val session = byId[result.sessionId] ?: sessionFromSearch(result) ?: continue
        out.add(FoundChat(session, if (result.titleMatch) null else result.hits.firstOrNull()))
    }
    for (session in local) if (seen.add(session.sessionId)) out.add(FoundChat(session, null))
    return out
}

/**
 * What the drawer lists for [query]. While it is non-blank and the hub lists assistant search, the results list stays:
 * the local title and last-message matches at once, and [answer] (only when it is already for this query) merges in.
 * Null keeps today's groups: no query, or a hub without the feature.
 */
internal fun shownSearchRows(
    query: String, enabled: Boolean, local: List<Session>, known: List<Session>, answer: AssistantSearchAnswer?,
): List<FoundChat>? {
    if (!enabled || query.trim().isEmpty()) return null
    return mergeAssistantSearch(local, answer, known)
}

/**
 * [snippet] from [lead] code points before its first match, "…" in front when that cuts it, with the ranges (UTF-16)
 * moved to match. A match near the start is left as the manager wrote it.
 */
internal fun leadSnippet(snippet: String, ranges: List<List<Int>>, lead: Int = SNIPPET_LEAD): Pair<String, List<List<Int>>> {
    val first = ranges.mapNotNull { it.takeIf { range -> range.size == 2 }?.get(0) }.minOrNull()
        ?: return snippet to ranges
    if (first <= 0 || first > snippet.length) return snippet to ranges
    val before = snippet.substring(0, first)
    val points = before.codePointCount(0, before.length)
    if (points <= lead + 1) return snippet to ranges
    val kept = before.substring(before.offsetByCodePoints(0, points - lead))
    val shift = first - kept.length - 1 // "…" is one UTF-16 unit
    val shown = "…" + kept + snippet.substring(first)
    return shown to ranges.map { range -> if (range.size == 2) listOf(range[0] - shift, range[1] - shift) else range }
}

/** The snippet with each match bold, cut so the match stays on a one-line row. */
internal fun markedSnippet(hit: AssistantSearchHit, lead: Int = SNIPPET_LEAD): AnnotatedString {
    val (shown, ranges) = leadSnippet(hit.snippet, hit.ranges, lead)
    return buildAnnotatedString {
        append(shown)
        for (range in ranges.sortedBy { it.firstOrNull() ?: 0 }) {
            if (range.size != 2) continue
            val start = range[0].coerceIn(0, shown.length)
            val end = range[1].coerceIn(start, shown.length)
            if (end > start) addStyle(SpanStyle(fontWeight = FontWeight.Bold), start, end)
        }
    }
}

/**
 * One drawer's search. The answer is kept only for the query it was asked for. A manager that answers 404 is not asked
 * again this run. A blank query, or a hub without the feature, forgets the answer so the local filter stands alone.
 */
@Stable
internal class AssistantChatSearch {
    var answer by mutableStateOf<AssistantSearchAnswer?>(null)
        private set
    var forQuery by mutableStateOf("")
        private set
    private val unsupported = HashSet<String>()
    private var ticket by mutableIntStateOf(0)

    /** The answer when it is for [query]; null while it is being asked, or when there will not be one. */
    fun current(query: String): AssistantSearchAnswer? {
        val q = query.trim().take(200)
        return answer.takeIf { forQuery == q && q.isNotEmpty() }
    }

    /**
     * Asks [search] for [query] once the keys settle ([on]: the hub lists the feature). [key] is this hub and manager.
     * [search] returns null for a 404.
     */
    suspend fun ask(query: String, on: Boolean, key: String, search: suspend (String) -> AssistantSearchAnswer?) {
        val mine = ++ticket
        val q = query.trim().take(200)
        if (!on || q.isEmpty()) {
            answer = null
            forQuery = ""
            return
        }
        if (key in unsupported) return
        kotlinx.coroutines.delay(SEARCH_SETTLE_MS)
        if (mine != ticket) return
        try {
            when (val got = search(q)) {
                null -> if (mine == ticket) unsupported += key
                else -> if (mine == ticket) {
                    currentCoroutineContext().ensureActive()
                    answer = got
                    forQuery = q
                }
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* offline or failed: the local matches stay the answer */ }
    }

    internal fun remembers(key: String) = key in unsupported
}

/**
 * The rows a search shows, and whether the manager is still reading older chats. While the query is non-blank and the
 * hub lists the feature, the rows are the results list (local matches at once). Null rows: today's groups.
 */
@Composable
internal fun rememberFoundChats(
    query: String, local: List<Session>, known: List<Session>, enabled: Boolean, api: AssistantManagement?, hubKey: String,
): Pair<List<FoundChat>?, Boolean> {
    val search = rememberAssistantSearch(query, enabled, api, hubKey)
    val answer = search.current(query)
    val found = remember(query, enabled, local, answer, known) { shownSearchRows(query, enabled, local, known, answer) }
    return found to (answer?.partial == true)
}

@Composable
internal fun rememberAssistantSearch(
    query: String, on: Boolean, api: AssistantManagement?, key: String,
): AssistantChatSearch {
    val search = remember { AssistantChatSearch() }
    LaunchedEffect(query, on, api, key) {
        search.ask(query, on && api != null, key) { q -> requireNotNull(api).searchChats(q) }
    }
    return search
}
