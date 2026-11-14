package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.chatlist.ChatReadingPosition
import persona.core.contracts.Fold
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.domain.lines.chatLines
import persona.core.domain.lines.LineOptions
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.*
import org.junit.Test
import java.io.File
import java.io.IOException

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChatSearchTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    @Test fun searchesNestedStringsAndAllReadingCategoriesWithLiteralHighlightsAndLimit() = runTest {
        val items = listOf(
            Item("text", kind = "text", text = "正文 Needle"),
            Item("thought", kind = "thinking", text = "思考 needle"),
            Item("call", kind = "tool_use", toolName = "NeedleTool", toolInput =
                Json.parseToJsonElement("""{"nested":[{"value":"前文 NEEdle 后文"}],"number":123,"flag":true}""")),
            Item("result", kind = "tool_result", text = "输出 needle"),
            Item("notice", kind = "notice", text = "事件 needle"),
        )
        val results = searchChat(res, items, " needle ")
        assertEquals(setOf("正文", "思考", "工具名", "工具输入", "工具输出", "系统事件"), results.hits.map { it.category }.toSet())
        results.hits.forEach { hit -> hit.ranges.forEach { range -> assertEquals("needle", hit.snippet.substring(range).lowercase()) } }
        assertTrue(searchChat(res, items, "123").hits.isEmpty())
        assertTrue(searchChat(res, items, "true").hits.isEmpty())
        assertTrue(searchChat(res, items, " ").hits.isEmpty())
        val literal = searchChat(res, listOf(Item("literal", kind = "text", text = "😀前文 a.* a.*")), "a.*").hits.single()
        assertEquals(2, literal.ranges.size)
        val capped = searchChat(res, (1..101).map { Item("item-$it", it, kind = "text", text = "needle") }, "needle")
        assertEquals(100, capped.hits.size)
        assertTrue(capped.truncated)
        assertEquals("item-101", capped.hits.first().itemId)
        assertFalse(searchChat(res, (1..100).map { Item("item-$it", it, kind = "text", text = "needle") }, "needle").truncated)
        val live = Item("r#0", 4, kind = "tool_result", role = "user", text = "NewNeedle")
        assertEquals(listOf("r#0"), searchChat(res, listOf(live, live.copy(text = "OldNeedle")), "NewNeedle").hits.map { it.itemId })
        assertTrue(searchChat(res, listOf(live, live.copy(text = "OldNeedle")), "OldNeedle").hits.isEmpty())
        val older = Item("same#0", 3, kind = "text", text = "OlderNeedle")
        val newer = Item("same#0", 4, kind = "text", text = "NewerNeedle")
        assertEquals(listOf("same#0"), searchChat(res, listOf(older, newer), "NewerNeedle").hits.map { it.itemId })
        assertTrue(searchChat(res, listOf(older, newer), "OlderNeedle").hits.isEmpty())
    }

    @Test fun resolvesFoldedTargetsAndLoadsOnlyContinuousBoundedPagesWithoutLosingReturnAnchor() = runTest {
        val items = listOf(
            Item("user", 30, role = "user", kind = "text", text = "开始"),
            Item("call", 31, kind = "tool_use", toolUseId = "tool", toolName = "Read"),
            Item("result", 32, kind = "tool_result", toolUseId = "tool", text = "结果"),
            Item("reply", 33, role = "assistant", kind = "text", text = "完成"),
        )
        val lines = chatLines(items.map(Item::asLineItem)).lines
        val target = searchTarget(lines, "result")!!
        assertEquals(setOf("w-user"), target.expand)
        val opened = chatLines(items.map(Item::asLineItem), LineOptions(openWork = target.expand)).lines
        assertEquals(target.rowId, searchTarget(opened, "result")?.rowId)
        assertTrue(opened.any { it.id == target.rowId })
        val state = ChatSearchState()
        val origin = ChatReadingPosition("reply", -75, false, setOf("existing"))
        state.origin = origin
        state.choose(ChatSearchHit("result", "工具输出", "结果", emptyList()))
        state.choose(ChatSearchHit("user", "正文", "开始", emptyList()))
        assertEquals(origin, state.origin)
        val key = SessionKey("machine", "session")
        val snapshot = MutableStateFlow(ChatSnapshot(items = items, loading = false, hasOlder = true))
        var calls = 0
        var fail = false
        val repository = object : ChatRepository {
            override fun observe(session: SessionKey) = snapshot
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
            override suspend fun loadOlder(session: SessionKey) {
                calls++
                if (fail) { snapshot.value = snapshot.value.copy(error = "失败"); return }
                val seq = snapshot.value.items.minOf { it.seq } - 1
                snapshot.value = snapshot.value.copy(items = listOf(Item("older-$seq", seq, kind = "text")) + snapshot.value.items)
            }
        }
        val pages = mutableListOf<Int>()
        assertNotNull(loadEarlierForSearch(res, repository, key, pages::add))
        assertEquals((1..20).toList(), pages)
        assertEquals(20, calls)
        assertEquals(10, snapshot.value.items.minOf { it.seq })
        fail = true
        assertNotNull(loadEarlierForSearch(res, repository, key) { org.junit.Assert.fail("Failed page must not advance progress") })
        assertEquals(21, calls)
    }

    @Test fun matchesTheSharedFixtureAndNothingItHides() = runTest {
        val root = generateSequence(File(System.getProperty("persona.repo.root") ?: System.getProperty("user.dir")).absoluteFile) { it.parentFile }
            .first { File(it, "spec/fixtures/chat-search/cases.json").isFile }
        val cases = JsonCodec.parseToJsonElement(File(root, "spec/fixtures/chat-search/cases.json").readText())
            .jsonObject.getValue("cases").jsonArray.map { it.jsonObject }
        val items = cases.map { JsonCodec.decodeFromJsonElement<Item>(it.getValue("item")) }
        for ((case, item) in cases.zip(items)) {
            val fields = case.getValue("fields").jsonArray.map { row ->
                val pair = row.jsonArray
                pair[0].jsonPrimitive.content to pair[1].jsonPrimitive.content
            }
            assertEquals(case.getValue("name").jsonPrimitive.content, fields, item.searchFields())
        }
        for (word in listOf("ActionSecret", "persona-action", "ViewErr", "beach.jpg", "InjectedRaw", "RawFollowup", "persona-supervision", "fallback", "persona-view"))
            assertTrue(word, searchChat(res, items, word).hits.isEmpty())
        for (hit in searchChat(res, items, "路线").hits) {
            assertFalse(hit.snippet, hit.snippet.contains("persona-") || hit.snippet.contains("fallback"))
        }
    }

    @Test fun fetchesFoldedStepsNewestFirstWithoutOpeningAndHonorsTheBudget() = runTest {
        val folds = (1..3).map { Item("fold-$it", seq = it * 10, kind = "fold", fold = Fold(throughSeq = it * 10 + 5, throughId = "end-$it")) }
        val seen = mutableListOf<String>()
        val capped = fetchFoldsForSearch(folds, load = { fold ->
            seen += fold.id
            listOf(Item("step-" + fold.id, kind = "thinking", text = "x".repeat(100)))
        }, onFold = { _, _ -> }, stopped = { false }, budget = 50, concurrency = 1)
        assertEquals(listOf("fold-3"), seen)
        assertTrue(capped.capped)
        assertEquals(0, capped.failed)
        seen.clear()
        val failed = fetchFoldsForSearch(folds, load = { fold ->
            seen += fold.id
            if (fold.id == "fold-2") throw IOException("offline")
            listOf(Item("step-" + fold.id, kind = "text", text = "word"))
        }, onFold = { _, _ -> }, stopped = { false }, concurrency = 1)
        assertEquals(listOf("fold-3", "fold-2", "fold-1"), seen)
        assertEquals(1, failed.failed)
        assertFalse(failed.capped)
    }

    @Test fun openingAHitInsideAFoldWaitsUntilTheFoldLoads() {
        val fold = Item("fold1", 10, kind = "fold", fold = Fold(throughSeq = 12, throughId = "x"))
        val lines = chatLines(listOf(Item("user", 1, role = "user", kind = "text", text = "hi"), fold).map(Item::asLineItem)).lines
        val line = foldWorkLine(lines, "fold1")
        assertNotNull(line)
        assertTrue(searchGo(lines, "inner", emptySet(), "fold1", emptySet()) is SearchGo.Expand)
        assertEquals(SearchGo.Waiting, searchGo(lines, "inner", setOf(line!!), "fold1", emptySet()))
        assertEquals(SearchGo.Missing, searchGo(lines, "inner", setOf(line), "fold1", setOf("fold1")))
        assertEquals(SearchGo.Missing, searchGo(lines, "inner", emptySet(), null, emptySet()))
    }
}
