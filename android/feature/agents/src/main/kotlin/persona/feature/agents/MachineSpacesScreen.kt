package persona.feature.agents

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.engines.Engines
import persona.core.domain.sort.*
import java.time.Instant

/**
 * A computer's projects (iOS `MachineSpacesView`, #4338 A11): its summary, then the projects with something going on and
 * the idle ones, each opening its page ([onOpenSpace]); the toolbar opens the computer's details ([onDetails]). Opened from
 * a space page's computer. [rows] are the Agents list's agents; [machine] is null until the computers are read, and the page
 * is then named by [machineId]. Glass draws iOS's large title; Brief and Board a compact bar, with the same rows.
 */
@Composable
internal fun MachineSpacesScreen(
    machineId: String,
    machine: Machine?,
    rows: List<AgentSession>,
    preferences: AgentsPreferencesSnapshot,
    now: Instant,
    loaded: Boolean,
    onBack: () -> Unit,
    onDetails: () -> Unit,
    onOpenSpace: (AgentSpace) -> Unit,
    onOpenAgent: (AgentSession) -> Unit,
    sessions: List<Session> = emptyList(),
    onOpenSide: ((AgentSession, SessionKey) -> Unit)? = null,
) {
    val shown = remember(rows, machineId, preferences.pins, preferences.mutes) { machineProjects(rows, machineId, preferences.pins, preferences.mutes) }
    val visibleSessions = remember(sessions, preferences) { sessions.map { it.listSession(preferences).session } }
    val visibleSessionsByKey = remember(visibleSessions) { visibleSessions.associateBy { it.key } }
    val resources = agentResources()
    val working = stringResource(R.string.agents_projects_working)
    val idle = stringResource(R.string.agents_projects_idle)
    val summary = stringResource(R.string.agents_projects_summary,
        pluralStringResource(R.plurals.agents_projects_count, shown.projects, shown.projects),
        pluralStringResource(persona.core.designsystem.R.plurals.designsystem_tab_agents, shown.agents, shown.agents))
    val agentsOf = { count: Int -> resources.getQuantityString(persona.core.designsystem.R.plurals.designsystem_tab_agents, count, count) }
    JListPage(
        title = machine?.displayName ?: machineId, onBack = if (LocalIsSidebarRoot.current) null else onBack, largeTitle = PersonaTheme.style == AppStyle.Glass,
        actions = { JIconButton(PersonaIcons.info, stringResource(R.string.agents_projects_details), onDetails) },
    ) {
        if (machine != null) item("summary", contentType = "summary") {
            JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s), grouped = true) {
                AgentMachineSummary(machine.deviceIcon, machine.online, machine.hostname?.takeIf(String::isNotBlank), summary,
                    shown.tally.counts, waiting = shown.tally.waiting)
            }
        }
        projectSection("busy", working, Status.Working, shown.busy, busy = true, preferences, resources, now, agentsOf, onOpenSpace, onOpenAgent, visibleSessions, visibleSessionsByKey, onOpenSide)
        projectSection("idle", idle, Status.Idle, shown.idle, busy = false, preferences, resources, now, agentsOf, onOpenSpace, onOpenAgent, visibleSessions, visibleSessionsByKey, onOpenSide)
        if (loaded && shown.projects == 0) item("empty", contentType = "empty") {
            JEmptyState(stringResource(R.string.agents_projects_empty_title),
                // the computer's own engine, by name (#17054): herdr, Orca…
                if (machine?.online == false) stringResource(R.string.agents_projects_empty_offline)
                else stringResource(R.string.agents_projects_empty_hint, machine?.takeUnless(Engines::isNone)?.let(Engines::currentName) ?: Engines.DEFAULT),
                icon = machine?.deviceIcon ?: PersonaIcons.machine)
        }
    }
}

/** What a section lists: a project's row, or one of the agents that are in no space (each is a row of its own). */
private sealed interface ProjectLine {
    val key: String
    data class Project(val project: MachineProject) : ProjectLine { override val key get() = project.space.id }
    data class Agent(val project: MachineProject, val agent: AgentSession) : ProjectLine { override val key get() = "agent:${agent.id}" }
}

/**
 * A section of the page: its heading with how many projects it holds, then their rows in one card, a [busy] one's with its
 * counts and its lead, an idle one's with how many agents and which tabs.
 */
private fun LazyListScope.projectSection(
    id: String, title: String, tone: Status, projects: List<MachineProject>, busy: Boolean, preferences: AgentsPreferencesSnapshot,
    resources: android.content.res.Resources, now: Instant, agentsOf: (Int) -> String,
    onOpenSpace: (AgentSpace) -> Unit, onOpenAgent: (AgentSession) -> Unit,
    visibleSessions: List<Session>, visibleSessionsByKey: Map<SessionKey, Session>,
    onOpenSide: ((AgentSession, SessionKey) -> Unit)?,
) {
    if (projects.isEmpty()) return
    item("$id-header", contentType = "header") { JSectionHeader(title, count = projects.size, countTone = tone) }
    val lines = projects.flatMap { project ->
        if (project.outsideSpace) project.space.members.map { ProjectLine.Agent(project, it) } else listOf(ProjectLine.Project(project))
    }
    itemsIndexed(lines, key = { _, line -> "$id:${line.key}" }, contentType = { _, line -> if (line is ProjectLine.Agent) "agent" else "project" }) { index, line ->
        JGroupItem(GroupPosition.of(index, lines.size), grouped = true) {
            when (line) {
                is ProjectLine.Agent -> Box(Modifier.padding(horizontal = rowPadding)) {
                    val sideUnread = unreadSideSummary(line.agent.session, visibleSessionsByKey[line.agent.session.key], visibleSessions)
                    // As iOS: the agent's row, no menu, a tap opens it.
                    SlotRow(line.agent, null, AgentRowMetadata(), now, compact = false, pinned = line.agent.id in preferences.pins.sessions,
                        muted = preferences.mutes.mutes(line.agent), depth = 0, position = GroupPosition.Single, located = false,
                        onOpen = { onOpenAgent(line.agent) }, menuItems = null,
                        sideUnread = sideUnread, onOpenSide = onOpenSide?.let { cb -> { side -> cb(line.agent, side) } },
                        preferHerdrTabName = preferences.prefersHerdrTabName(line.agent.session))
                }
                is ProjectLine.Project -> {
                    val project = line.project
                    val time = relativeTime(resources, project.updatedAt, now)
                    JRow(onClick = { onOpenSpace(project.space) }) {
                        if (busy) {
                            val lead = project.lead?.session
                            AgentProjectLine(project.space.name, project.state.designStatus, project.tally.counts, waiting = project.tally.waiting,
                                pinned = project.pinned, muted = project.muted, lead = lead?.let { it.slotLabel ?: it.displayTitle },
                                words = lead?.lastText, time = time)
                        } else AgentIdleProjectLine(project.space.name, idleDetail(project, agentsOf), time = time)
                    }
                }
            }
        }
    }
}

/** "2 agents · review · research" (iOS `QuietSpaceRow`): how many agents and the first three tab names. */
private fun idleDetail(project: MachineProject, agentsOf: (Int) -> String): String =
    (listOf(agentsOf(project.space.members.size)) + project.space.members.mapNotNull { it.session.slotLabel }.take(3)).joinToString(" · ")

/** What a heading or the summary counts: needing you, working, unread, idle ([StateCounts]; the waiting clock is its own count). */
internal val StateTally.counts: List<Pair<Status, Int>>
    get() = listOf(Status.NeedsYou to needsYou, Status.Working to working, Status.Unread to unread, Status.Idle to idle)
