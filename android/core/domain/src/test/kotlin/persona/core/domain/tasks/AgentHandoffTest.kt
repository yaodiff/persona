package persona.core.domain.tasks

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.PlanTask
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent

/** One card per Agent a dispatch started or a task took over (#5110, the web's taskHandoffs). */
class AgentHandoffTest {
    private val at = "2026-11-10T08:00:00Z"
    private fun agent(id: String, title: String = id, linkedAt: String = at, error: String? = null) =
        TodoAgent("computer", id, linkedAt = linkedAt, title = title, startError = error)
    private fun todo(id: String = "task", title: String = "Example task", agents: List<TodoAgent> = emptyList(), doneAt: String? = null, startError: String? = null) =
        Todo(id, title, origin = "main", createdAt = at, doneAt = doneAt, startError = startError, agents = agents)

    @Test fun aTaskWithAnAgentIsOneNewCard() {
        val cards = taskHandoffs(todo(agents = listOf(agent("agent", "Example Agent"))), null, HandoffWake.Created, emptyMap())
        assertEquals(1, cards.size)
        assertEquals("computer/agent", cards[0].key)
        assertEquals("Example Agent", cards[0].agentTitle)
        assertEquals(HandoffWake.Created, cards[0].wake)
        assertEquals("task", cards[0].todoId)
    }

    @Test fun wakeActiveAndResumedStayOnTheCard() {
        assertEquals(HandoffWake.Active, feedWake("active"))
        assertEquals(HandoffWake.Resumed, feedWake("resumed"))
        assertEquals(HandoffWake.Created, feedWake(null))
        assertEquals(HandoffWake.Created, feedWake("created"))
        val active = taskHandoffs(todo(agents = listOf(agent("agent"))), null, feedWake("active"), emptyMap())
        assertEquals(HandoffWake.Active, active.single().wake)
    }

    @Test fun anAgentLinkedLaterIsNotOnThisHandoff() {
        val later = agent("next", "Next Agent", linkedAt = "2026-11-10T09:00:00Z")
        val cards = taskHandoffs(todo(agents = listOf(agent("agent"), later)), null, HandoffWake.Active, emptyMap())
        assertEquals(listOf("computer/agent"), cards.map { it.key })
    }

    @Test fun agentsLinkedTogetherAreBothCards() {
        val second = agent("next", linkedAt = "2026-11-10T08:01:00Z")
        val cards = taskHandoffs(todo(agents = listOf(agent("agent"), second)), null, HandoffWake.Created, emptyMap())
        assertEquals(listOf("computer/agent", "computer/next"), cards.map { it.key })
    }

    @Test fun beforeAnAgentIsUpTheCardIsTheTask() {
        val cards = taskHandoffs(todo(), PlannedHandoff("task-event", "Example task", todoId = "task"), HandoffWake.Created, emptyMap())
        assertEquals("todo:task", cards.single().key)
        assertEquals("Example task", cards.single().title)
        assertNull(cards.single().sessionId)
        assertEquals(false, cards.single().ended)
        assertEquals("task", cards.single().todoId)
    }

    @Test fun aTaskThatDidNotStartSaysWhyAndOneRemovedSinceIsGone() {
        val plan = TaskPlan("plan", "main", listOf(
            PlanTask("Broken task", todoId = "broken"),
            PlanTask("Removed task", todoId = "removed"),
        ), "started", at)
        val broken = taskHandoffs(
            todo("broken", "Broken task", doneAt = at, startError = "launcher missing"),
            PlannedHandoff("plan/0", "Broken task", todoId = "broken"),
            HandoffWake.Created, mapOf(plan.id to plan),
        )
        assertEquals("launcher missing", broken.single().failure)
        assertEquals(false, broken.single().ended)
        val removed = taskHandoffs(null, PlannedHandoff("plan/1", "Removed task", todoId = "removed"), HandoffWake.Created, mapOf(plan.id to plan), loaded = true)
        assertEquals(true, removed.single().ended)
        assertNull(removed.single().todoId)
        assertEquals("Removed task", removed.single().title)
        val loading = taskHandoffs(null, PlannedHandoff("plan/1", "Removed task", todoId = "removed"), HandoffWake.Created, emptyMap(), loaded = false)
        assertEquals(false, loading.single().ended)
    }

    @Test fun aFailedStartOnTheAgentRowIsTheReason() {
        val cards = taskHandoffs(todo(agents = listOf(agent("agent", error = "no launcher"))), null, HandoffWake.Created, emptyMap())
        assertEquals("no launcher", cards.single().failure)
        assertNull(cards.single().sessionId)
    }

    @Test fun aClosedTaskWithNoAgentSaysItEnded() {
        val cards = taskHandoffs(todo(doneAt = at), null, HandoffWake.Created, emptyMap())
        assertEquals(true, cards.single().ended)
        assertEquals("task", cards.single().todoId)
    }

    @Test fun aShortIdKeepsALongOneToCindyShape() {
        assertEquals("0f3c2a91…7ed0", shortSessionId("0f3c2a91-5d7e-4b8a-9c11-2e4f6a8b7ed0"))
        assertEquals("short-id", shortSessionId("short-id"))
    }
}
