package persona.core.domain.sort

import persona.core.contracts.Machine
import persona.core.contracts.Session
import org.junit.Assert.*
import org.junit.Test

/** "By state" / "Recently updated" (iOS AgentSort), "Lark group" / "Terminal" (#2526) and the botmux switch (web #2786). */
class AgentSortSourceTest {
    private fun row(id: String, state: String = "working", date: Int = 1, pane: String? = "w1:p$id", space: String = "space", cwd: String = "") =
        AgentSession(Session(machineId = "m", sessionId = id, spaceId = space, spaceName = space, state = state, title = id,
            paneId = pane, cwd = cwd, updatedAt = "2026-01-0${date}T00:00:00Z"))
    private val machines = listOf(Machine("m", online = true))

    @Test fun hostKindAndSourceFollowThePaneId() {
        assertEquals(AgentSource.Terminal, row("a", pane = "w1:p1").session.agentSource)
        assertEquals(AgentSource.Terminal, row("a", pane = "herdr-core:1").session.agentSource)
        assertEquals(AgentSource.Feishu, row("a", pane = "botmux:topic").session.agentSource)
        assertTrue(row("a", pane = "botmux:topic").session.isBotmux)
        // An Orca terminal is a terminal; an Orca chat (the bridge's, no pane) Orca's own app (#17054).
        assertEquals(AgentSource.Terminal, row("a", pane = "orca:1").session.agentSource)
        val chat = row("a", pane = null).session.copy(sourceKind = "runtime", sourceInstanceId = "orca", spaceKind = "orca")
        assertEquals(AgentSource.Desktop, chat.agentSource)
        assertNull(chat.copy(sourceInstanceId = "paseo").agentSource)
        // A Tern pane is a block's number behind its kind; it is a terminal like herdr's.
        assertTrue(row("a", pane = "tern:201863462919").session.isTern)
        assertEquals(AgentSource.Terminal, row("a", pane = "tern:201863462919").session.agentSource)
        assertNull(row("a", pane = null).session.agentSource)
    }

    @Test fun statusFirstRanksAndRecentlyUpdatedGoesByTimeOnly() {
        val rows = listOf(row("old-needs", "needs_you", 1), row("new-idle", "unread", 3), row("mid", "working", 2))
        val byState = buildAgentList(rows, machines, settings = AgentListSettings(grouping = AgentGrouping.State))
        assertEquals(listOf("m/old-needs", "m/new-idle", "m/mid"), byState.groups.flatMap { it.rows }.map { it.id })
        val bySpace = AgentListSettings(grouping = AgentGrouping.MachineSpace)
        assertEquals(listOf("m/old-needs", "m/new-idle", "m/mid"), buildAgentList(rows, machines, settings = bySpace).groups.single().rows.map { it.id })
        assertEquals(listOf("m/new-idle", "m/mid", "m/old-needs"),
            buildAgentList(rows, machines, settings = bySpace.copy(sort = AgentSort.Updated)).groups.single().rows.map { it.id })
    }

    @Test fun recentlyUpdatedOrdersBusySpacesAndRepositoriesByTheirLatestAgent() {
        val rows = listOf(row("a", "needs_you", 1, space = "first", cwd = "/src/alpha"), row("b", "working", 4, space = "second", cwd = "/src/beta"))
        val spaces = AgentListSettings(grouping = AgentGrouping.MachineSpace)
        assertEquals(listOf("m/first", "m/second"), buildAgentList(rows, machines, settings = spaces).groups.map { it.id })
        assertEquals(listOf("m/second", "m/first"), buildAgentList(rows, machines, settings = spaces.copy(sort = AgentSort.Updated)).groups.map { it.id })
        val repos = buildAgentList(rows, machines, settings = AgentListSettings(sort = AgentSort.Updated)).groups.map { it.id }
        assertTrue(repos.toString(), repos.indexOfFirst { "beta" in it } < repos.indexOfFirst { "alpha" in it })
    }

    @Test fun sourceFiltersCountAndShowOnlyWhileTheyHaveAgents() {
        val rows = listOf(row("t"), row("f", pane = "botmux:topic"), row("o", pane = "orca:1"))
        val list = buildAgentList(rows, machines, settings = AgentListSettings(quick = AgentQuickFilter.Feishu))
        // the Orca pane is in a tab like herdr's (#17054)
        assertEquals(1, list.counts[AgentQuickFilter.Feishu]); assertEquals(2, list.counts[AgentQuickFilter.Terminal])
        assertEquals(listOf("m/f"), list.groups.flatMap { it.rows }.map { it.id })
        val none = mapOf(AgentQuickFilter.NeedsYou to 1, AgentQuickFilter.Unread to 0, AgentQuickFilter.Feishu to 0, AgentQuickFilter.Terminal to 0)
        assertEquals(listOf(AgentQuickFilter.All, AgentQuickFilter.NeedsYou), AgentQuickFilter.shown(AgentQuickFilter.All, none))
        assertEquals(listOf(AgentQuickFilter.All, AgentQuickFilter.NeedsYou, AgentQuickFilter.Unread),
            AgentQuickFilter.shown(AgentQuickFilter.Unread, none))
        assertTrue(AgentQuickFilter.Terminal in AgentQuickFilter.shown(AgentQuickFilter.Terminal, none))
        assertTrue(AgentQuickFilter.Feishu in AgentQuickFilter.shown(AgentQuickFilter.All, none + (AgentQuickFilter.Feishu to 2)))
    }

    @Test fun hiddenBotmuxLeavesTheListAndItsCounts() {
        val rows = listOf(row("t"), row("f", pane = "botmux:topic"))
        val list = buildAgentList(rows, machines, settings = AgentListSettings(grouping = AgentGrouping.MachineSpace, hideBotmux = true))
        assertEquals(listOf("m/t"), list.groups.flatMap { it.rows }.map { it.id })
        assertEquals(0, list.counts[AgentQuickFilter.Feishu])
    }
}
