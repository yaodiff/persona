package persona.core.domain.views

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Cards read and checked as CheckAssistantView does (sdk/go/wire/assistantview.go), with the shared examples.
class AssistantCardsTest {
    private fun example(name: String): String =
        generateSequence(File(System.getProperty("persona.repo.root") ?: System.getProperty("user.dir")).absoluteFile) { it.parentFile }
            .map { File(it, "schema/extension/examples/assistant/$name.json") }.first { it.exists() }.readText()

    @Test fun everySharedExampleReads() {
        for (name in listOf("decision", "compare", "progress", "progress-updated", "form", "chart", "html")) {
            assertNotNull(name, AssistantCards.parse(example(name)))
        }
        val decision = AssistantCards.parse(example("decision"))!!
        assertEquals("选 B：队列去重", decision.actions.getValue("choose-b").reply)
        assertEquals(3, decision.root.sections.single().items.size)
        val form = AssistantCards.parse(example("form"))!!
        assertEquals(listOf("task", "priority", "notify"), form.fields(form.actions.getValue("submit")))
        val html = AssistantCards.parse(example("html"))!!.html.single()
        assertEquals("guide", html.key)
        assertEquals(320, html.height)
    }

    /** Share as a picture draws every example but the HTML page, which only runs live (#7261). */
    @Test fun whatAPictureDraws() {
        for (name in listOf("decision", "compare", "progress", "progress-updated", "form", "chart")) {
            assertTrue(name, AssistantCards.parse(example(name))!!.drawsInPicture)
        }
        assertTrue(!AssistantCards.parse(example("html"))!!.drawsInPicture)
    }

    @Test fun pagesAndOneComponent() {
        val card = AssistantCards.parse("""{"fallback":"f","sections":[{"items":[
            {"type":"row","title":"详情","action":{"id":"more","push":{"page":"detail"}}}]}],
            "pages":{"detail":{"title":"详情","sections":[{"items":[{"type":"button","title":"返回","action":{"id":"back","pop":true}},
            {"type":"button","title":"打开","action":{"id":"o","open":"persona://session/m1/s1"}}]}]}}}""")!!
        assertEquals("detail", card.actions.getValue("more").push)
        assertEquals("detail", card.actions.getValue("back").page)
        assertEquals("详情", card.page("detail")!!.title)
        val one = AssistantCards.parse("""{"type":"metric","label":"完成","value":"3","fallback":"完成 3"}""")!!
        assertEquals("metric", one.root.sections.single().items.single().type)
    }

    // The fields each action sends, as the hub's viewReplyMessage takes them: a submit must carry the search and filter.
    @Test fun searchAndFilterGoWithTheirActions() {
        val card = AssistantCards.parse("""{"fallback":"f","search":{"placeholder":"搜","value":"周","action":{"id":"find","reply":"按这个搜"}},
            "filter":{"name":"folder","choices":[{"value":"all"},{"value":"work"}],"value":"work","action":{"id":"pick","reply":"换文件夹"}},
            "sections":[{"items":[{"type":"field","name":"note","label":"备注","kind":"text"},
            {"type":"button","title":"提交","action":{"id":"go","reply":"就这样","submit":true}},
            {"type":"row","title":"选这个","action":{"id":"one","reply":"选这个"}}],
            "more":{"title":"再来一批","action":{"id":"more","reply":"再来一批"}}}]}""")!!
        fun fields(id: String) = card.fields(card.actions.getValue(id)).sorted()
        assertEquals(listOf("folder", "note", "q"), fields("go"))
        assertEquals(listOf("q"), fields("find"))
        assertEquals(listOf("folder", "q"), fields("pick"))
        assertEquals(emptyList<String>(), fields("one"))
        assertEquals(emptyList<String>(), fields("more"))
        assertEquals(mapOf("q" to "周", "folder" to "work"), card.searchValues(card.root))
    }

    @Test fun whatIsRefused() {
        val refused = mapOf(
            "no fallback" to """{"sections":[]}""",
            "blank fallback" to """{"fallback":" ","sections":[]}""",
            "two ways" to """{"fallback":"f","sections":[{"items":[{"type":"button","title":"x","action":{"id":"a","reply":"r","pop":true}}]}]}""",
            "no way" to """{"fallback":"f","sections":[{"items":[{"type":"button","title":"x","action":{"id":"a"}}]}]}""",
            "submit without reply" to """{"fallback":"f","sections":[{"items":[{"type":"button","title":"x","action":{"id":"a","pop":true,"submit":true}}]}]}""",
            "repeated id" to """{"fallback":"f","sections":[{"items":[{"type":"row","title":"x","action":{"id":"a","reply":"r"}},{"type":"row","title":"y","action":{"id":"a","reply":"s"}}]}]}""",
            "missing page" to """{"fallback":"f","sections":[{"items":[{"type":"row","title":"x","action":{"id":"a","push":{"page":"nope"}}}]}]}""",
            "extension push" to """{"fallback":"f","sections":[{"items":[{"type":"row","title":"x","action":{"id":"a","push":{"ui":"other"}}}]}]}""",
            "nested pages" to """{"fallback":"f","sections":[],"pages":{"p":{"sections":[],"pages":{"q":{"sections":[]}}}}}""",
            "bad page name" to """{"fallback":"f","sections":[],"pages":{"a b":{"sections":[]}}}""",
            "a secret" to """{"fallback":"f","sections":[{"items":[{"type":"field","name":"p","label":"密码","kind":"secret"}]}]}""",
            "bad open" to """{"fallback":"f","sections":[{"items":[{"type":"row","title":"x","action":{"id":"a","open":"http://example.com"}}]}]}""",
            "too deep" to "{\"fallback\":\"f\",\"x\":" + "[".repeat(40) + "]".repeat(40) + "}",
            "not JSON" to "invalid",
        )
        for ((why, raw) in refused) assertNull(why, AssistantCards.parse(raw))
        assertNull("too large", AssistantCards.parse("""{"fallback":"${"x".repeat(AssistantCards.MostBytes)}","sections":[]}"""))
    }

    @Test fun openTargets() {
        assertEquals(AssistantOpenTarget.Web("https://example.com/a?b=1"), AssistantOpen.target("https://example.com/a?b=1"))
        assertEquals(AssistantOpenTarget.Session("mac", "s 1"), AssistantOpen.target("persona://session/mac/s%201"))
        assertEquals(AssistantOpenTarget.Session("mac", "s1"), AssistantOpen.target("persona://session/mac/s1"))
        assertEquals(AssistantOpenTarget.Task("t1"), AssistantOpen.target("persona://task/t1"))
        assertEquals(AssistantOpenTarget.Knowledge("people/a.md"), AssistantOpen.target("persona://kb/people/a.md"))
        assertEquals(AssistantOpenTarget.File("mac", "/tmp/a b.txt"), AssistantOpen.target("persona://file/mac/%2Ftmp%2Fa%20b.txt"))
        val refused = listOf("http://example.com", "https://user:pw@example.com", "javascript:alert(1)", "file:///etc/passwd",
            "persona://kb/../secret", "persona://kb/a/%2E%2E/b", "persona://session/m", "persona://session/m/s/x", "persona://task/a%2Fb",
            "persona://task/t?x=1", "persona://task/t#x", "persona://task/%zz", "persona://pair/x", "persona://file/m/relative",
            "persona://task/a\u0001", "persona://task/a\\b", "intent://x", "persona://task/%C3%28", "persona://task/+%2b")
        for (url in refused.dropLast(1)) assertNull(url, AssistantOpen.target(url))
        // A plus stays a plus; only percent escapes are undone.
        assertEquals(AssistantOpenTarget.Task("++"), AssistantOpen.target("persona://task/+%2b"))
        assertTrue(AssistantOpen.target("https://example.com") is AssistantOpenTarget.Web)
    }
}
