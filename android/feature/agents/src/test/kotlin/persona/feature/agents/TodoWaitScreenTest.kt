package persona.feature.agents

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.time.Instant
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Todo
import persona.core.contracts.generated.TodoID
import persona.core.contracts.generated.TodoWait
import persona.core.contracts.generated.TodoWaitContact
import persona.core.contracts.generated.TodoWaitKind
import persona.core.contracts.generated.TodoWaitPRBlocker
import persona.core.designsystem.PersonaTheme

/** Android renders the hub's typed `wait` directly on To-dos rows and the detail page (#16170). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1600dp")
class TodoWaitScreenTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.parse("2026-11-18T10:00:00Z")

    private fun todo(wait: TodoWait?) = Todo(
        id = "t1",
        title = "Confirm venue booking",
        createdAt = "2026-11-17T08:00:00Z",
        updatedAt = "2026-11-18T09:00:00Z",
        wait = wait,
    )

    private fun showRow(todo: Todo) {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                TodosScreen(listOf(todo), emptyList(), null, onBack = {}, onOpenTodo = {}, now = now)
            }
        }
        rule.waitForIdle()
    }

    private fun showDetail(todo: Todo, onOpenTodo: (String) -> Unit = {}, onUri: (String) -> Unit = {}) {
        val uriHandler = object : UriHandler {
            override fun openUri(uri: String) = onUri(uri)
        }
        rule.setContent {
            CompositionLocalProvider(LocalUriHandler provides uriHandler) {
                PersonaTheme(reduceMotion = true) {
                    TodoDetailScreen(
                        todo.id,
                        listOf(todo),
                        machines = emptyList(),
                        sessions = emptyList(),
                        tasks = null,
                        onBack = {},
                        onOpenSession = {},
                        onHandOff = {},
                        onOpenTodo = onOpenTodo,
                        now = now,
                    )
                }
            }
        }
        rule.waitForIdle()
    }

    @Test
    fun rowShowsServerSummaryAndPRBlockers() {
        val wait = TodoWait(
            kind = TodoWaitKind.PrMerge,
            waitingOn = "PR #88",
            reason = "Fix CSV escaping",
            prBlockers = listOf(TodoWaitPRBlocker.ReviewRequired, TodoWaitPRBlocker.ChecksPending),
        )
        showRow(todo(wait))

        rule.onNodeWithTag("todo-wait-line", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("PR #88 · Needs review · Checks running", useUnmergedTree = true).assertExists()
    }

    @Test
    fun rowShowsUsageLimitAndScheduledReset() {
        val wait = TodoWait(
            kind = TodoWaitKind.UsageLimit,
            waitingOn = "Feedback Worker",
            wakeAt = "2026-11-18T12:00:00Z",
            since = "2026-11-18T09:30:00Z",
        )
        showRow(todo(wait))

        rule.onNodeWithText("Feedback Worker reached its usage limit", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Back", substring = true, useUnmergedTree = true).assertExists()
    }

    @Test
    fun rowShowsExternalEventSummary() {
        val wait = TodoWait(
            kind = TodoWaitKind.ExternalEvent,
            waitingOn = "App Store Review",
            reason = "Waiting for App Store review to finish",
            until = "2026-11-21T09:00:00Z",
        )
        showRow(todo(wait))

        rule.onNodeWithText("Waiting for App Store review to finish", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Until", substring = true, useUnmergedTree = true).assertExists()
    }

    @Test
    fun rowShowsDateSummary() {
        val wait = TodoWait(
            kind = TodoWaitKind.Date,
            waitingOn = "Launch day",
            reason = "Wait until launch day morning",
            wakeAt = "2026-11-25T09:00:00Z",
        )
        showRow(todo(wait))

        rule.onNodeWithText("Wait until launch day morning", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Wakes", substring = true, useUnmergedTree = true).assertExists()
    }

    @Test
    fun rowShowsParkedSummary() {
        val wait = TodoWait(
            kind = TodoWaitKind.Parked,
            reason = "Deferred until after quarterly review",
            until = "2026-11-22T09:00:00Z",
        )
        showRow(todo(wait))

        rule.onNodeWithText("Deferred until after quarterly review", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Until", substring = true, useUnmergedTree = true).assertExists()
    }

    @Test
    fun detailShowsEveryServerFactAndLinks() {
        val opened = mutableListOf<String>()
        val wait = TodoWait(
            kind = TodoWaitKind.PersonReply,
            waitingOn = "Alex",
            reason = "Waiting for Alex to confirm the room quote",
            wakeCondition = "Alex replies in the email thread",
            wakeAt = "2026-11-19T12:00:00Z",
            until = "2026-11-20T18:00:00Z",
            since = "2026-11-18T08:45:00Z",
            url = "https://github.com/example/app/pull/418",
            contact = TodoWaitContact(
                channel = "email",
                sender = "Persona",
                sentAt = "2026-11-18T08:45:00Z",
                url = "https://mail.example.com/threads/1",
                request = "Asked whether the conference room is open on Nov 25",
            ),
        )
        showDetail(todo(wait), onUri = { opened += it })

        rule.onNodeWithTag("todo-wait-section", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Waiting on", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Alex", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Waiting for Alex to confirm the room quote", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Wakes when", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Scheduled wake", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Until", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Sent via email", substring = true, useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Asked whether the conference room is open on Nov 25", useUnmergedTree = true).assertExists()
        rule.onNodeWithTag("todo-wait-contact", useUnmergedTree = true).assertExists()
        rule.onNodeWithTag("todo-wait-source", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Waiting since", substring = true, useUnmergedTree = true).assertExists()

        rule.onNodeWithTag("todo-wait-contact", useUnmergedTree = true).performClick()
        rule.onNodeWithTag("todo-wait-source", useUnmergedTree = true).performClick()
        assertEquals(
            listOf("https://mail.example.com/threads/1", "https://github.com/example/app/pull/418"),
            opened,
        )
    }

    @Test
    fun detailShowsPRBlockers() {
        val wait = TodoWait(
            kind = TodoWaitKind.PrMerge,
            waitingOn = "PR #88",
            reason = "Fix CSV escaping",
            url = "https://github.com/example/app/pull/88",
            prBlockers = listOf(TodoWaitPRBlocker.ReviewRequired, TodoWaitPRBlocker.Conflicts),
        )
        showDetail(todo(wait))

        rule.onNodeWithText("Blocked by", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Needs review · Merge conflicts", useUnmergedTree = true).assertExists()
        rule.onNodeWithTag("todo-wait-source", useUnmergedTree = true).assertExists()
    }

    @Test
    fun handoffTargetOpensTheLinkedTodo() {
        var opened: String? = null
        val wait = TodoWait(
            kind = TodoWaitKind.TodoHandoff,
            waitingOn = "Migrate API schema",
            reason = "Waiting for integration tests",
            handoffTodoId = TodoID("todo_api_schema"),
        )
        showDetail(todo(wait), onOpenTodo = { opened = it })

        rule.onNodeWithTag("todo-wait-handoff", useUnmergedTree = true).performClick()
        assertEquals("todo_api_schema", opened)
    }

    @Test
    fun handoffWithOnlyIdFallsBackToTheIdAndOpensTheLinkedTodo() {
        var opened: String? = null
        val wait = TodoWait(
            kind = TodoWaitKind.TodoHandoff,
            reason = "Waiting for integration tests",
            handoffTodoId = TodoID("todo_api_schema"),
        )
        showDetail(todo(wait), onOpenTodo = { opened = it })

        rule.onNodeWithText("todo_api_schema", useUnmergedTree = true).assertExists()
        rule.onNodeWithTag("todo-wait-handoff", useUnmergedTree = true).performClick()
        assertEquals("todo_api_schema", opened)
    }

    @Test
    fun noWaitDrawsNoRowLine() {
        showRow(todo(null))
        rule.onNodeWithTag("todo-wait-line", useUnmergedTree = true).assertDoesNotExist()
    }

    @Test
    fun noWaitDrawsNoDetailSection() {
        showDetail(todo(null))
        rule.onNodeWithTag("todo-wait-section", useUnmergedTree = true).assertDoesNotExist()
    }
}
