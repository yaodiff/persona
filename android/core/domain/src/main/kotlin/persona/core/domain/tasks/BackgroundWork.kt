package persona.core.domain.tasks

import persona.core.contracts.AgentState
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.contracts.Workflow

/** Live counts come from summary scalars when present, never from missing detail arrays. */
data class BackgroundWork(val tasks: Int, val subagents: Int, val workflows: Int, val workflow: Workflow? = null)

/** Working, but all the background work it still has only waits (iOS `Session.waitsInBackground`, the hub's `background_waiting`, #6045). */
val Session.waitsInBackground: Boolean get() = foldedState == AgentState.Working && backgroundWaiting &&
    (children?.working ?: 0) == 0 && (children?.needsYou ?: 0) == 0

/**
 * What a session runs in the background, or null when it runs none. A session the hub says only waits in the background
 * ([waitsInBackground]) reports no work of its own beyond what it counts: its background commands sleep or watch, and the
 * tray draws its clock, not "Working (Background)" (iOS `AssistantTrayActivity.reportedBackgroundWork`).
 */
fun Session.backgroundWork(): BackgroundWork? {
    val background = background.orEmpty()
    val summary = waiting != null
    val workflow = if (summary) workflowProgress else workflows.orEmpty().firstOrNull { it.isRunning }
    val workflows = if (summary) runningWorkflowsCount ?: 0 else workflows?.count { it.isRunning }
        ?: background.count { it.kind == "workflow" }
    val ownSubagents = if (summary) runningSubagents ?: 0 else subagents?.count { it.running }
        ?: background.count { it.kind == "agent" }
    val subagents = maxOf(ownSubagents, children?.working ?: 0)
    // A legacy raw background count may include workflows and agents; it cannot define an independent job count.
    val tasks = backgroundTasksCount ?: if (summary) 0
        else background.count { it.kind != "workflow" && it.kind != "agent" }
    return if (tasks > 0 || subagents > 0 || workflows > 0 || (backgroundWorking == true && !backgroundWaiting) ||
        (summary && backgroundTasksCount == null && (backgroundCountHint ?: 0) > 0))
        BackgroundWork(tasks, subagents, workflows, workflow) else null
}

/** Every active binding contributes, including work on a non-lead agent. */
fun Todo.backgroundWork(lookup: (SessionKey) -> Session?): BackgroundWork? {
    if (isDone) return null
    val work = activeAgents.mapNotNull { lookup(it.key) }.distinctBy { it.key }.mapNotNull { it.backgroundWork() }
    if (work.isEmpty()) return null
    return BackgroundWork(work.sumOf { it.tasks }, work.sumOf { it.subagents }, work.sumOf { it.workflows },
        work.mapNotNull { it.workflow }.maxByOrNull { it.startedAt.orEmpty() })
}

/** A real external wait without any background work; user decisions remain in Needs you. */
fun Session.waitsWithoutBackground(): Boolean = backgroundWork() == null &&
    agentState !in setOf(AgentState.Working, AgentState.NeedsYou, AgentState.Unread) &&
    work?.phase == "blocked" && work?.on in setOf("agent", "external", "transient")
