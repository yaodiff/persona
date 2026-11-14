package persona.feature.machines

import persona.core.contracts.AgentState
import persona.core.contracts.Machine
import persona.core.contracts.MachineMetrics
import persona.core.contracts.Session

/** What brings a computer to the front of Machines, most pressing first. */
internal enum class MachineConcern {
    Pending,
    Offline,
    Critical,
    Warning,
    NeedsYou,
    Mismatch,
    None,
}

/** The live agents on a computer, counted the same way on its card and in the page summary. */
internal data class AgentTally(
    val needsYou: Int = 0,
    val working: Int = 0,
    val idle: Int = 0,
) {
    constructor(sessions: List<Session>) : this(
        needsYou = sessions.count { it.agentState == AgentState.NeedsYou },
        working = sessions.count { it.agentState == AgentState.Working },
        idle = sessions.count { it.agentState != AgentState.NeedsYou && it.agentState != AgentState.Working },
    )

    val total: Int get() = needsYou + working + idle
}

internal data class MachineListEntry(
    val id: String,
    val machine: Machine?,
    val metrics: MachineMetrics,
    val tally: AgentTally,
    val concern: MachineConcern,
)

internal data class MachineSummary(
    val online: Int,
    val total: Int,
    val attention: Int,
    val needsYou: Int,
)

/** The hub's order first, including metrics-only computers and every manager-scoped input supplied by the caller. */
internal fun machineListIds(machines: List<Machine>, metrics: Map<String, MachineMetrics>): List<String> =
    (machines.map { it.id } + metrics.keys).distinct()

internal fun machineConcern(
    machine: Machine?,
    online: Boolean,
    metricAlerts: List<MetricAlert>,
    needsYou: Int,
): MachineConcern = when {
    machine?.pending == true -> MachineConcern.Pending
    !online -> MachineConcern.Offline
    metricAlerts.any { it.critical } -> MachineConcern.Critical
    metricAlerts.isNotEmpty() -> MachineConcern.Warning
    needsYou > 0 -> MachineConcern.NeedsYou
    showsDeploy(machine) -> MachineConcern.Mismatch
    else -> MachineConcern.None
}

/**
 * Projects the list once for ordering, cards and the summary. Sorting by the original index as well as the concern
 * keeps equal-ranked computers in hub order when only their samples or heartbeats change.
 */
internal fun machineListEntries(
    ids: List<String>,
    machines: List<Machine>,
    metrics: Map<String, MachineMetrics>,
    sessions: List<Session>,
    now: Long,
    hierarchy: (String) -> Boolean = { false },
): List<MachineListEntry> {
    val machinesById = machines.associateBy { it.id }
    return ids.mapIndexed { index, id ->
        val machine = machinesById[id]
        val metric = (metrics[id] ?: MachineMetrics(id, machine?.online ?: false, machine?.system)).live(machine)
        val tally = AgentTally(machineSessions(sessions, id, hierarchy(id)))
        val currentAlerts = alerts(metric.current(now), (metric.system ?: machine?.system)?.cores)
        val concern = machineConcern(machine, metric.online, currentAlerts, tally.needsYou)
        Triple(index, concern, MachineListEntry(id, machine, metric, tally, concern))
    }.sortedWith(compareBy<Triple<Int, MachineConcern, MachineListEntry>>({ it.second.ordinal }, { it.first })).map { it.third }
}

internal fun machineSummary(entries: List<MachineListEntry>): MachineSummary = MachineSummary(
    online = entries.count { it.metrics.online },
    total = entries.size,
    attention = entries.count { it.concern != MachineConcern.None },
    needsYou = entries.sumOf { it.tally.needsYou },
)

internal const val MACHINE_AGENTS_PREVIEW = 5

internal fun visibleMachineAgents(agents: List<Session>, showAll: Boolean): List<Session> =
    if (showAll) agents else agents.take(MACHINE_AGENTS_PREVIEW)
