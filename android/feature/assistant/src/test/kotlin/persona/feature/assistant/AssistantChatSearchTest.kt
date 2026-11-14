package persona.feature.assistant

import persona.core.contracts.Session
import persona.core.contracts.api.AssistantSearchAnswer
import persona.core.contracts.api.AssistantSearchHit
import persona.core.contracts.api.AssistantSearchResult
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

@OptIn(ExperimentalCoroutinesApi::class)
class AssistantChatSearchTest {
    private fun session(id: String, archived: Boolean = false) =
        Session(machineId = "m", sessionId = id, title = id, archived = archived)

    private fun answer(vararg results: AssistantSearchResult) = AssistantSearchAnswer(results = results.toList())

    @Test fun serverResultsComeFirstThenLocalMatchesAndAChatThatCannotBeOpenedIsLeftOut() {
        val open = session("open")
        val localOnly = session("local")
        val archived = session("old", archived = true)
        val known = listOf(open, localOnly, archived)
        val local = listOf(localOnly, open)
        val hit = AssistantSearchHit(itemId = "i1", snippet = "said", ranges = listOf(listOf(0, 4)))
        val merged = mergeAssistantSearch(local, answer(
            AssistantSearchResult(sessionId = "missing", hits = listOf(hit)),
            AssistantSearchResult(sessionId = "old", hits = listOf(hit)),
            AssistantSearchResult(sessionId = "open", titleMatch = true, hits = listOf(hit)),
            AssistantSearchResult(sessionId = "open", hits = listOf(hit)),
        ), known)
        assertEquals(listOf("old", "open", "local"), merged.map { it.session.sessionId })
        assertEquals("i1", merged[0].hit?.itemId)
        assertNull(merged[1].hit)
        assertNull(merged[2].hit)
        assertEquals(local.map { it.sessionId }, mergeAssistantSearch(local, null, known).map { it.session.sessionId })
    }

    @Test fun anArchivedHitTheAppHasNotLoadedIsARowFromTheResult() {
        val open = session("open")
        val hit = AssistantSearchHit(itemId = "i1", snippet = "青石巷", ranges = listOf(listOf(0, 3)))
        val merged = mergeAssistantSearch(listOf(open), answer(
            AssistantSearchResult(sessionId = "old-trip", machineId = "box", title = "旧的旅行", archived = true,
                updatedAt = "2026-11-01T00:00:00Z", origin = "user", hits = listOf(hit)),
        ), known = listOf(open))
        val built = merged[0].session
        assertEquals(listOf("old-trip", "open"), merged.map { it.session.sessionId })
        assertEquals("box", built.machineId)
        assertEquals("旧的旅行", built.title)
        assertTrue(built.archived)
        assertEquals("2026-11-01T00:00:00Z", built.updatedAt)
        assertEquals("user", built.origin)
        assertEquals("i1", merged[0].hit?.itemId)
        val known = session("old-trip", archived = true).copy(title = "已加载")
        val kept = mergeAssistantSearch(emptyList(), answer(
            AssistantSearchResult(sessionId = "old-trip", machineId = "box", title = "结果里的标题", archived = true, hits = listOf(hit)),
        ), known = listOf(known))
        assertEquals("已加载", kept.single().session.title)
    }

    @Test fun aNonBlankQueryKeepsTheResultsListUntilTheManagerAnswers() = runTest {
        val local = listOf(session("trip"))
        assertNull(shownSearchRows("青", enabled = false, local, local, null))
        assertNull(shownSearchRows("  ", enabled = true, local, local, null))
        val waiting = shownSearchRows("青", enabled = true, local, local, null)!!
        assertEquals(listOf("trip"), waiting.map { it.session.sessionId })
        assertNull(waiting.single().hit)
        val search = AssistantChatSearch()
        search.ask("one", on = true, key = "k") { AssistantSearchAnswer(partial = true) }
        assertTrue(search.current("one")?.partial == true)
        val next = launch { search.ask("青", on = true, key = "k") { AssistantSearchAnswer() } }
        assertNull(search.current("青"))
        val rows = shownSearchRows("青", enabled = true, local, local, search.current("青"))!!
        assertEquals(listOf("trip"), rows.map { it.session.sessionId })
        advanceTimeBy(SEARCH_SETTLE_MS)
        next.join()
        assertNotNull(search.current("青"))
        assertFalse(search.current("青")?.partial == true)
    }

    @Test fun theSnippetStartsEightCodePointsBeforeTheMatchAndKeepsEmojiWhole() {
        val plain = leadSnippet("一二三四五六七八九十MATCH", listOf(listOf(10, 15)))
        assertEquals("…三四五六七八九十MATCH", plain.first)
        assertEquals(listOf(listOf(9, 14)), plain.second)
        val emoji = "😀".repeat(12) + "MATCH"
        val led = leadSnippet(emoji, listOf(listOf(24, 29)))
        assertTrue(led.first.startsWith("…"))
        assertFalse(led.first.contains("\uD83D") && !led.first.contains("\uDE00"))
        assertEquals(8, led.first.removePrefix("…").substringBefore("MATCH").codePointCount(0, led.first.removePrefix("…").substringBefore("MATCH").length))
        val near = leadSnippet("短 MATCH", listOf(listOf(2, 7)))
        assertEquals("短 MATCH", near.first)
        assertEquals(listOf(listOf(2, 7)), near.second)
    }

    @Test fun aMissingRouteIsNotAskedAgainAndAFailureLeavesTheLocalAnswer() = runTest {
        val search = AssistantChatSearch()
        var calls = 0
        search.ask("hello", on = true, key = "hub|main") { calls++; null }
        assertEquals(1, calls)
        assertTrue(search.remembers("hub|main"))
        assertNull(search.current("hello"))
        search.ask("again", on = true, key = "hub|main") { calls++; null }
        assertEquals(1, calls)
        search.ask("hello", on = true, key = "other") { throw IOException("offline") }
        assertNull(search.current("hello"))
        assertFalse(search.remembers("other"))
        search.ask("hello", on = true, key = "other") { AssistantSearchAnswer(partial = true) }
        assertTrue(search.current("hello")?.partial == true)
        search.ask("  ", on = true, key = "other") { error("blank") }
        assertNull(search.current("hello"))
        search.ask("hello", on = false, key = "other") { error("off") }
        assertNull(search.current("hello"))
    }

    @Test fun aNewerQueryDropsTheAnswerOfTheOneItCancelled() = runTest {
        val search = AssistantChatSearch()
        val gate = CompletableDeferred<AssistantSearchAnswer>()
        val first = launch { search.ask("one", on = true, key = "k") { gate.await() } }
        advanceTimeBy(SEARCH_SETTLE_MS)
        search.ask("", on = true, key = "k") { error("blank") }
        gate.complete(AssistantSearchAnswer(partial = true))
        first.join()
        assertNull(search.current("one"))
        assertNull(search.current(""))
    }
}
