package persona.core.domain

import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.Fold
import persona.core.contracts.Item

class ContinuityNarrationTest {
    @Test fun foldedPageReplacesExpandedNarrationWithoutTouchingTheFinalAnswer() {
        val reducer = ContinuityReducer()
        val prompt = Item("1#0", 1, role = "user", kind = "text", text = "go")
        val answer = Item("20#0", 20, role = "assistant", kind = "text", text = "done")
        val narration = listOf("first", "second", "third").mapIndexed { index, text ->
            Item("${index + 2}#0", index + 2, role = "assistant", kind = "text", text = text)
        }
        val fold = Item("fold:2#0", 2, role = "assistant", kind = "fold",
            fold = Fold(items = 3, throughSeq = 4, throughId = "4#0"))
        reducer.latest(listOf(prompt, fold, answer), "history", 20, false)
        reducer.latest(narration, "history", 4)
        assertEquals(listOf(1, 2, 3, 4, 20), reducer.items.map { it.seq })

        reducer.latest(listOf(prompt, fold, answer), "history", 20, false)
        assertEquals(listOf("1#0", "fold:2#0", "20#0"), reducer.items.map { it.id })
    }
}
