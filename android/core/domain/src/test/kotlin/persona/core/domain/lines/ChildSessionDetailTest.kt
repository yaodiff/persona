package persona.core.domain.lines

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.ChatSnapshot

class ChildSessionDetailTest {
    private fun prompt(id: String, text: String) = Item(id, role = "user", kind = "text", text = text)
    private fun call(id: String, seq: Int, name: String) = Item(id, seq = seq, kind = "tool_use", toolName = name)
    private val result = Item("answer", role = "assistant", kind = "text", text = "Latest words", fullSize = 1000)

    @Test fun readsTheFirstPromptAndFoldedLastCallWithoutLosingTheResultsFullTextIdentity() = runTest {
        val reads = mutableListOf<IntRange>()
        val reader = ChildSessionDetailReader { from, to ->
            reads += from..to
            if (from == 0) listOf(prompt("first", "<persona-context kind=\"context\">Private context</persona-context>\nOriginal task"))
            else listOf(call("last", 98, "Bash"))
        }
        val snapshot = ChatSnapshot(items = listOf(prompt("later", "Follow up"), call("visible", 42, "Read"),
            Item("work", seq = 50, kind = "fold", fold = Fold(throughSeq = 100)), result), hasOlder = true)
        val detail = reader.read(snapshot)
        assertEquals("Original task", detail.prompt)
        assertEquals("Bash", detail.lastTool)
        assertEquals(result, detail.result)
        assertEquals(listOf(0..40, 92..100), reads)
        assertEquals(detail, reader.read(snapshot))
        assertEquals(2, reads.size)
    }

    @Test fun aNewerVisibleCallWinsAndToolResultsAreNeverPrompts() = runTest {
        val reader = ChildSessionDetailReader { _, _ -> fail("No extra read for complete visible detail"); emptyList() }
        val snapshot = ChatSnapshot(items = listOf(prompt("tool-result", "Not a question").copy(toolUseId = "call"),
            prompt("first", "Actual task"), Item("work", seq = 5, kind = "fold", fold = Fold(throughSeq = 10)),
            call("last", 11, "Read"), result))
        assertEquals(ChildSessionDetail("Actual task", result, "Read"), reader.read(snapshot))
    }

    @Test fun failedSupplementaryReadsKeepVisibleDetailAndCancellationEscapes() = runTest {
        val snapshot = ChatSnapshot(items = listOf(prompt("first", "Visible task"), call("last", 1, "Read"),
            Item("work", seq = 2, kind = "fold", fold = Fold(throughSeq = 10)), result), hasOlder = true)
        val failed = ChildSessionDetailReader { _, _ -> error("Offline") }
        assertEquals(ChildSessionDetail("Visible task", result, "Read"), failed.read(snapshot))
        try {
            ChildSessionDetailReader { _, _ -> throw CancellationException("Closed") }.read(snapshot)
            fail("A closed card must cancel its reads")
        } catch (_: CancellationException) { }
    }
}
