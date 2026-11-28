package persona.core.contracts

// The direction tree (#4352, docs/assistant/unified-entry.md): `GET /api/v1/assistant/directions/tree`,
// built once by the hub. Clients only draw it: they never group the to-dos by supervisor or work out a task's state.

/** The hub's tree: every current direction in `GET /assistant/leaders` order, then the secretary group last. */
data class DirectionTree(val directions: List<Direction> = emptyList()) {
    /** The tree with every agent's and outside target's machine id passed through [machine] (plain ids into merged ones). */
    fun mapMachines(machine: (String) -> String): DirectionTree =
        copy(directions = directions.map { d ->
            d.copy(external = d.external?.mapMachine(machine),
                tasks = d.tasks.map { t -> t.copy(agents = t.agents.map { it.mapMachines(machine) }) })
        })

    /** The direction led by conversation [id], or the secretary group when [id] is its (the main conversation's). */
    fun direction(id: String): Direction? = directions.firstOrNull { it.id == id }

    /** The secretary group: the to-dos no current direction supervises. */
    val secretary: Direction? get() = directions.firstOrNull { it.secretary }

    /** The current directions (leaders) in the hub's order, never the secretary group: the drawer's 常驻 rows. */
    val leaders: List<Direction> get() = directions.filter { !it.secretary && it.id.isNotEmpty() }

    /** The current directions' ids in the hub's order. */
    val ids: List<String> get() = leaders.map { it.id }

    /** Each current direction's name by leader conversation id, those with a name only. */
    val names: Map<String, String> get() = leaders.filter { it.name.isNotEmpty() }.associate { it.id to it.name }
}

/**
 * One direction (a leader conversation or an explicitly outside direction) or, [secretary], the group of to-dos supervised by the main conversation or by
 * no current direction; [id] is then the main conversation's and [name] is empty.
 */
data class Direction(
    val id: String,
    val name: String = "",
    val secretary: Boolean = false,
    val counts: DirectionCounts = DirectionCounts(),
    val tasks: List<DirectionTask> = emptyList(),
    /**
     * Something under it is at work right now: an Agent of one of its tasks, or one under it, whose session is working,
     * whatever the task's state (the hub's `running`, its one rule, #6626). Every direction row's activity mark spins on
     * it; the working count is not shown. From a hub without it: whether it counts a working task, as before.
     */
    val running: Boolean = false,
    /** Non-null only when the hub explicitly marks this as an outside direction, with no Persona conversation. */
    val external: ExternalDirection? = null,
) {
    /**
     * What a heading calls it: the secretary group is named by the app, a lead the hub has no name for ("" when its
     * conversation has no title either) reads as an untitled chat, as the drawer's row does.
     */
    val title: CoreText get() = when {
        secretary -> CoreText.of(CoreText.Key.DirectionSecretary)
        name.isBlank() -> CoreText.of(CoreText.Key.DirectionUntitled)
        else -> CoreText.plain(name)
    }
}

/** An outside direction's optional Agent target. Missing or incomplete ids leave its row visible but not tappable. */
data class ExternalDirection(
    val machineId: String = "",
    val sessionId: String = "",
) {
    val target: SessionKey?
        get() = if (machineId.isNotBlank() && sessionId.isNotBlank()) SessionKey(machineId, sessionId) else null

    fun mapMachine(machine: (String) -> String): ExternalDirection =
        copy(machineId = machineId.takeIf { it.isNotBlank() }?.let(machine).orEmpty())
}

/** How many of a direction's listed tasks are in each state (`counts`): the row shows [needsYou] (#6626: what runs
 *  under it is [Direction.running], its spinner, never a working count). */
data class DirectionCounts(val working: Int = 0, val needsYou: Int = 0, val stuck: Int = 0, val review: Int = 0,
    val planned: Int = 0)

/** An open to-do of a direction, in the hub's order (needs you, to verify, stuck, working, not started; newest activity first). */
data class DirectionTask(
    val id: String,
    val title: String = "",
    val state: DirectionTaskState = DirectionTaskState.Working,
    val agents: List<DirectionAgent> = emptyList(),
)

/**
 * A task's one state, worked out by the hub only. [wire] is the tree's `state`; an unknown one reads as [Working], the
 * state of every other open task.
 */
enum class DirectionTaskState(val wire: String, val label: CoreText.Key) {
    NeedsYou("needs_you", CoreText.Key.DirectionNeedsYou),
    Review("review", CoreText.Key.DirectionReview),
    Stuck("stuck", CoreText.Key.DirectionStuck),
    Working("working", CoreText.Key.DirectionDoing),
    Planned("planned", CoreText.Key.DirectionPlanned);

    companion object {
        fun of(wire: String?): DirectionTaskState = entries.firstOrNull { it.wire == wire } ?: Working
    }
}

/**
 * An agent working on a task, with what it started under it ([children]). [state] is the session's state as the hub's
 * session rows say it ("" when the hub did not find the session); an in-session sub-agent's [sessionId] is
 * `<parent>@<id>`, which is no session of its own ([isSubagent]).
 */
data class DirectionAgent(
    val machineId: String,
    val sessionId: String,
    val title: String = "",
    val agent: String = "",
    val state: String = "",
    val children: List<DirectionAgent> = emptyList(),
) {
    val key: SessionKey get() = SessionKey(machineId, sessionId)

    /** A sub-agent inside its parent's session (`<parent>@<id>`): it opens as its parent. */
    val isSubagent: Boolean get() = '@' in sessionId

    /** The session a tap opens: the parent's for an in-session sub-agent. */
    val openKey: SessionKey get() = SessionKey(machineId, sessionId.substringBefore('@'))

    /** The agent and everything under it, depth first. */
    fun flatten(): List<DirectionAgent> = listOf(this) + children.flatMap { it.flatten() }

    internal fun mapMachines(machine: (String) -> String): DirectionAgent =
        copy(machineId = machine(machineId), children = children.map { it.mapMachines(machine) })
}
