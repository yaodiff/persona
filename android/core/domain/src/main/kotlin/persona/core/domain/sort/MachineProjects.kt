package persona.core.domain.sort

import persona.core.contracts.AgentState
import java.time.Instant

// A computer's projects page (iOS `MachineSpacesView` with `BusySpaceRow` and `QuietSpaceRow`, #4338 A11): the projects (herdr
// spaces) its agents are in, the ones with something going on first, the idle ones below. The page draws it; the rules are
// here, with their tests.

/**
 * How many of some agents are in each state a heading or a summary counts (iOS `StateCounts`): needing you, working (not
 * those that only wait in the background: [waiting], drawn apart with a clock), unread, and [idle] (Seen or unknown, not
 * those that wait). A subtask a parent holds is no agent of its own here ([isNestedSubtask]), as on the Agents list.
 */
data class StateTally(val needsYou: Int = 0, val working: Int = 0, val unread: Int = 0, val waiting: Int = 0, val idle: Int = 0)

/** The [StateTally] of these agents. */
fun List<AgentSession>.stateTally(): StateTally {
    val rows = foldRows(this).filterNot { it.isNestedSubtask }
    return StateTally(
        needsYou = rows.count { it.state == AgentState.NeedsYou },
        working = rows.count { it.state == AgentState.Working && !it.waitsInBackground },
        unread = rows.count { it.state == AgentState.Unread },
        waiting = rows.count { it.waitsInBackground },
        idle = rows.count { it.state == AgentState.Idle || it.state == AgentState.Unknown },
    )
}

/**
 * One project of a computer, as its page draws it: the [space] and what its row says. [state] is the most urgent of its
 * agents' states, an agent that only waits in the background counting as idle (iOS `SpaceGroup.state`, `countedState`);
 * [lead] the agent that most wants attention, the most urgent state and then the latest (iOS `BusySpaceRow`); [updatedAt]
 * when its newest agent last moved.
 */
data class MachineProject(
    val space: AgentSpace,
    val pinned: Boolean,
    val muted: Boolean,
    val state: AgentState,
    val tally: StateTally,
    val lead: AgentSession?,
    val updatedAt: Instant?,
) {
    /**
     * Its agents are in no herdr space (a session of another host): there is no space page, and the page lists each of them
     * as a row of its own (iOS `spaceID == "-"`).
     */
    val outsideSpace: Boolean get() = space.members.first().session.spaceId == null
}

/**
 * A computer's projects (iOS `MachineSpacesView`): [busy] the pinned ones first, then those with something going on (its
 * "Working" section); [idle] those where every agent is idle and none waits to be woken or for the user's reply (its
 * "Idle" section). [agents] and [tally] are all its agents; [projects] counts the group of agents outside any space as one.
 */
data class MachineProjects(
    val busy: List<MachineProject>,
    val idle: List<MachineProject>,
    val agents: Int,
    val tally: StateTally,
) {
    val projects: Int get() = busy.size + idle.size
}

/**
 * The projects of computer [machineId] from the Agents list's [rows]: its live agents that are not the assistant's and not a
 * subtask a parent holds. The projects go by their most urgent state, then their newest agent, then their id, as iOS orders
 * its spaces (`SpaceGroup.build`); a [pins]ned one is in [MachineProjects.busy] whatever it does, a quiet one by iOS's rule
 * ([AgentSpace.quiet], with the [mutes]) is in [MachineProjects.idle].
 */
fun machineProjects(rows: List<AgentSession>, machineId: String, pins: AgentPreferences, mutes: AgentPreferences): MachineProjects {
    val mine = foldRows(rows).filter { it.session.machineId == machineId && it.isLive && !it.session.assistant && !it.isNestedSubtask }
    val all = agentSpaces(mine)
        .sortedWith(compareBy<AgentSpace> { space -> space.members.minOf { it.countedPriority } }
            .thenByDescending { space -> space.members.mapNotNull { it.sortDate }.maxOrNull() ?: Instant.MIN }
            .thenBy { it.id })
        .map { space ->
            MachineProject(
                space, pinned = space.preferenceKey in pins.spaces, muted = space.preferenceKey in mutes.spaces,
                state = space.members.minBy { it.countedPriority }.let { if (it.waitsInBackground) AgentState.Idle else it.state },
                tally = space.members.stateTally(),
                lead = space.members.minWithOrNull(compareBy<AgentSession> { it.state.priority }.thenByDescending { it.updatedAt ?: Instant.MIN }),
                updatedAt = space.updatedAt,
            )
        }
    return MachineProjects(
        busy = all.filter { it.pinned } + all.filter { !it.pinned && !it.space.quiet(mutes) },
        idle = all.filter { !it.pinned && it.space.quiet(mutes) },
        agents = mine.size,
        tally = mine.stateTally(),
    )
}

/** The state an agent counts under: one that only waits in the background is idle, not working (iOS `Session.countedState`). */
private val AgentSession.countedPriority: Int get() = (if (waitsInBackground) AgentState.Idle else state).priority
