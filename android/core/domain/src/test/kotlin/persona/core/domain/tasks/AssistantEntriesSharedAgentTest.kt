package persona.core.domain.tasks

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.Session
import persona.core.contracts.SessionTodo
import persona.core.contracts.Supervision
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.TodoAsk
import java.time.Instant

/** Mirrors the web's "an Agent several to-dos list" cases (#7531, web `assistant-entries.test.ts`). */
class AssistantEntriesSharedAgentTest {
    private val at = "2026-11-10T08:00:00Z"
    private val agent = TodoAgent("box", "tests", linkedAt = at)
    private val asking = Session(machineId = "box", sessionId = "tests", title = "Network tests", cwd = "/w",
        state = "needs_you", status = "blocked", updatedAt = at)
    private val approval = Interaction("perm", machineId = "box", sessionId = "tests", kind = "permission",
        toolName = "Bash", createdAt = at)

    private fun todo(id: String) = Todo(id, title = id, origin = "main", createdAt = at, updatedAt = at, agents = listOf(agent))
    private fun run(todos: List<Todo>, session: Session = asking, conversation: String = "main",
                    interactions: List<Interaction> = listOf(approval), sessions: List<Session> = listOf(session)) =
        assistantEntries(conversation, "main", todos, sessions, interactions, emptyList(), emptyList(), Instant.parse(at).toEpochMilli())

    private fun withAsks(entries: AssistantEntries) = entries.needsYou.filter {
        when (it) {
            is NeedsYouEntry.Task -> it.asks.isNotEmpty()
            is NeedsYouEntry.Agent -> it.asks.isNotEmpty()
            else -> false
        }
    }.map { it.key }

    @Test fun asksUnderTheTodoTheHubTiesItsSessionToAndTheOthersAreInProgress() {
        val got = run(listOf(todo("cache"), todo("release"), todo("tests")),
            asking.copy(todo = SessionTodo("release", "release")))
        assertEquals(listOf("task:release"), withAsks(got))
        assertEquals(listOf("task:release"), got.needsYou.map { it.key })
        assertEquals(listOf("task:cache", "task:tests"), got.running.map { it.key }.sorted())
        assertTrue(got.running.all { (it as RunningEntry.Task).status == TriageStatus.Working })
        assertTrue(got.running.all { (it as RunningEntry.Task).hasAgentsElsewhere })
        assertTrue(got.shelved.isEmpty())
    }

    @Test fun asksUnderTheFirstListerWhenTheHubTiesItToNoneOfThem() {
        for (tied in listOf(null, SessionTodo("elsewhere", "Elsewhere"))) {
            val got = run(listOf(todo("cache"), todo("tests")), asking.copy(todo = tied))
            assertEquals(listOf("task:cache"), withAsks(got))
            assertEquals(listOf("task:cache"), got.needsYou.map { it.key })
            assertEquals(listOf("task:tests"), got.running.map { it.key })
        }
    }

    @Test fun keepsAnotherTodosOwnDecisionWithoutTheAgentsCard() {
        val decisions = listOf(
            todo("tests").copy(ask = TodoAsk(askedAt = at, outcome = "decide", verdict = "needs_user", question = "Which framework?")),
            todo("tests").copy(supervision = Supervision(supervisor = "main", state = "pending_review")),
            todo("tests").copy(supervision = Supervision(supervisor = "main", state = "needs_user")),
        )
        for (decide in decisions) {
            val got = run(listOf(todo("release"), decide), asking.copy(todo = SessionTodo("release", "release")))
            assertEquals(listOf("task:release"), withAsks(got))
            assertEquals(listOf("task:release", "task:tests"), got.needsYou.map { it.key })
            assertTrue((got.needsYou.last() as NeedsYouEntry.Task).asks.isEmpty())
            assertTrue(got.running.isEmpty())
        }
    }

    @Test fun onlyCurrentLinksInThisConversationsListCanBeTheHome() {
        val base = todo("excluded").copy(origin = "side")
        val excluded = listOf(
            base.copy(doneAt = at),
            base.copy(agents = listOf(agent.copy(releasedAt = at))),
            base.copy(origin = "other"),
            base.copy(agents = listOf(agent.copy(machineId = "another-box"))),
        )
        for (other in excluded) {
            val scoped = run(listOf(other, todo("cache").copy(origin = "side"), todo("tests").copy(origin = "side")),
                asking.copy(todo = SessionTodo(other.id, other.title)), conversation = "side")
            assertEquals(listOf("task:cache"), withAsks(scoped))
            assertEquals(listOf("task:cache"), scoped.needsYou.map { it.key })
        }
    }

    @Test fun keepsItsOwnInterruptedAgentAndFailedStartWaiting() {
        val worker = TodoAgent("box", "own", linkedAt = at)
        val interrupted = Session(machineId = "box", sessionId = "own", state = "idle", turnError = "Synthetic model error")
        val tied = asking.copy(todo = SessionTodo("release", "release"))
        val interruptedTodo = todo("tests").copy(agents = listOf(agent, worker))
        val failedTodo = todo("retry").copy(startError = "Synthetic start failure", doneAt = at)
        val got = run(listOf(todo("release"), interruptedTodo, failedTodo), sessions = listOf(tied, interrupted))
        assertEquals(listOf("task:release"), withAsks(got))
        assertEquals(listOf("task:release", "task:tests", "task:retry"), got.needsYou.map { it.key })
        assertEquals(TriageStatus.Interrupted, (got.needsYou[1] as NeedsYouEntry.Task).status)
        assertTrue(got.running.isEmpty())
    }

    @Test fun keepsTheHomeWaitingBeforeItsCardsLoad() {
        val got = run(listOf(todo("cache"), todo("tests")), asking.copy(todo = SessionTodo("tests", "tests")),
            interactions = emptyList())
        assertEquals(listOf("task:tests"), got.needsYou.map { it.key })
        assertEquals(listOf("task:cache"), got.running.map { it.key })
        assertTrue((got.running.single() as RunningEntry.Task).hasAgentsElsewhere)
    }

    @Test fun anUnsharedAgentsTaskDoesNotHaveAgentsElsewhere() {
        val got = run(listOf(todo("only")), asking.copy(state = "idle", status = "idle"), interactions = emptyList())
        assertEquals(listOf("task:only"), got.running.map { it.key })
        assertFalse((got.running.single() as RunningEntry.Task).hasAgentsElsewhere)
    }
}
