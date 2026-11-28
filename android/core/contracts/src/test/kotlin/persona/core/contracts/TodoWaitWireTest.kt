package persona.core.contracts

import java.io.File
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.generated.TodoWait
import persona.core.contracts.generated.TodoWaitKind
import persona.core.contracts.generated.TodoWaitPRBlocker

/** Every expected TodoWait in spec/fixtures/todo-wait (#16168) reads into TodoWait and writes back identically. */
class TodoWaitWireTest {
    private val codec = Json { explicitNulls = false }

    @Test fun writesEveryWaitBack() {
        val root = File(System.getProperty("persona.repo.root"))
        val cases = Json.parseToJsonElement(File(root, "spec/fixtures/todo-wait/todo_wait.json").readText())
            .jsonObject.getValue("cases").jsonArray
        var n = 0
        for (case in cases) {
            val obj = case.jsonObject
            val name = obj.getValue("name").jsonPrimitive.content
            val wait = obj.getValue("expected").jsonObject["wait"] ?: continue
            if (wait is JsonNull) continue
            val decoded = codec.decodeFromJsonElement<TodoWait>(wait)
            assertFalse(name, decoded.kind is TodoWaitKind.Unknown)
            assertEquals(name, wait, codec.encodeToJsonElement(decoded))
            n++
        }
        assertTrue(n > 0)
    }

    @Test fun keepsWhatANewerHubSends() {
        val wait = codec.decodeFromString<TodoWait>("""{"kind":"future_wait","pr_blockers":["future_blocker"],"waiting_on":"Upstream"}""")
        assertEquals(TodoWaitKind.Unknown("future_wait"), wait.kind)
        assertEquals(listOf(TodoWaitPRBlocker.Unknown("future_blocker")), wait.prBlockers)
        assertEquals("Upstream", wait.waitingOn)
    }
}
