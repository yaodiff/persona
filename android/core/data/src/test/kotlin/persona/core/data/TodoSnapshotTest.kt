package persona.core.data

import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.TodoSpace

/** The to-dos kept in the pairing's list snapshots read back as the hub gave them (#4533). */
class TodoSnapshotTest {
    @get:Rule val temporary = TemporaryFolder()

    @Test fun oversizedSnapshotIsSkippedWithoutChangingItsOriginal() = runTest {
        val store = ListSnapshotStore(temporary.newFolder())
        store.write("large", "123456789")
        assertNull(store.read("large", maxBytes = 8))
        assertEquals("123456789", store.read("large", maxBytes = 9))
    }

    @Test fun toDosWrittenToTheSnapshotsReadBackTheSame() = runTest {
        val directory = temporary.newFolder()
        assertNull(ListSnapshotStore(directory).todoCache().read())
        val todos = listOf(
            Todo(id = "a", title = "写周报", notes = "周五前", createdAt = "2026-11-05T08:00:00Z",
                agents = listOf(TodoAgent("mac", "s1")), spaces = listOf(TodoSpace("mac", "web"))),
            Todo(id = "b", title = "done", doneAt = "2026-11-05T09:30:00Z", doneBy = "user", quiet = true),
        )
        ListSnapshotStore(directory).todoCache().write(todos)
        // A new store over the same directory is the next start.
        assertEquals(todos, ListSnapshotStore(directory).todoCache().read())
    }

    @Test fun oversizedTodoSnapshotFallsBackToOpenTodosAndSkipsWhenStillOverBudget() = runTest {
        val directory = temporary.newFolder()
        val openTodo = Todo(id = "open-1", title = "Keep me")
        val hugeDone = Todo(
            id = "done-1",
            title = "Completed",
            notes = "x".repeat(2048),
            doneAt = "2026-11-05T09:30:00Z",
            doneBy = "user",
        )
        val bounded = ListSnapshotStore(directory).todoCache(maxBytes = 512)
        bounded.write(listOf(openTodo, hugeDone))
        assertEquals(listOf(openTodo), bounded.read())
        val snapshotFile = java.io.File(directory, snapshotHash("todos"))
        org.junit.Assert.assertTrue(snapshotFile.length() <= 512)

        // When even open todos exceed the cap, write skips gracefully without throwing or corrupting the prior file.
        val tiny = ListSnapshotStore(directory).todoCache(maxBytes = 64)
        tiny.write(listOf(Todo(id = "huge-open", title = "Too big", notes = "y".repeat(512))))
        assertEquals(listOf(openTodo), bounded.read())
        // Reading with a maxBytes smaller than the existing file skips it without deleting it.
        assertNull(ListSnapshotStore(directory).todoCache(maxBytes = snapshotFile.length() - 1).read())
        assertEquals(listOf(openTodo), bounded.read())
        // No leaked temp files left behind.
        assertEquals(listOf(snapshotHash("todos")), directory.list()?.toList().orEmpty())
    }
}
