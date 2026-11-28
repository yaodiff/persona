package persona.core.domain.sort

import persona.core.contracts.Machine
import persona.core.contracts.Session
import org.junit.Assert.*
import org.junit.Test

/** The "By machine and space" tree these tests were written for (the default grouping is now "By repository"). */
private val MachineSpaceTree = AgentListSettings(grouping = AgentGrouping.MachineSpace)

class AgentSpaceTest {
    @Test fun projectHeadingsUseTheMostCommonFolderAndQualifyNamesOnTheSameComputer() {
        fun row(id: String, machine: String, space: String?, name: String, cwd: String, physical: String? = null) =
            AgentSession(Session(machineId = machine, sessionId = id, state = "working", spaceId = space,
                spaceName = name, cwd = cwd, workspaceCwd = physical))

        val mac = Machine("mac", online = true, home = "/Users/sample")
        val pc = Machine("pc", online = true, home = "C:\\Users\\Sample")
        val rows = listOf(
            row("a", mac.id, "desk", "site", "/tmp/site", "/Users/sample/Desktop/site"),
            row("b", mac.id, "desk", "site", "/Users/sample/Desktop/site"),
            row("c", mac.id, "desk", "site", "/Users/sample/other/site"),
            row("d", mac.id, "code", "SITE", "/Users/sample/code/site"),
            row("e", pc.id, "remote", "site", "C:\\Users\\Sample\\code\\site"),
            row("f", pc.id, "other", "work", "C:\\Users\\Sample\\work\\docs"),
            row("g", pc.id, "more", "Work", "C:\\Users\\Sample\\tmp\\docs"),
            row("h", mac.id, null, "No project", "/Users/sample/tmp"),
            AgentSession(row("i", mac.id, null, "Workspace", "/Users/sample/fixtures/workspace").session.copy(workspaceId = "workspace")),
        )
        val groups = buildAgentList(rows, listOf(mac, pc), settings = MachineSpaceTree).groups.associateBy { it.id }
        assertEquals(AgentProjectFolder(mac.id, "/Users/sample/Desktop/site", "~/Desktop"), groups["mac/desk"]?.projectFolder)
        assertEquals("~/code", groups["mac/code"]?.projectFolder?.qualifier)
        assertNull(groups.getValue("pc/remote").projectFolder?.qualifier)
        assertEquals("~\\work", groups.getValue("pc/other").projectFolder?.qualifier)
        assertNull(groups.getValue("mac/-").projectFolder)
        assertEquals("/Users/sample/fixtures/workspace", groups.getValue("mac/workspace").projectFolder?.path)
    }

    @Test fun detailKeepsTabOrderAndMuteExceptionsOverrideSpace() {
        val idle = AgentSession(Session(machineId = "machine", sessionId = "idle", state = "idle", spaceId = "workspace", spaceName = "项目", slotIndex = 1))
        val active = AgentSession(idle.session.copy(sessionId = "active", state = "working", slotIndex = 2))
        val space = agentSpaces(listOf(active, idle, AgentSession(idle.session.copy(sessionId = "closed", state = "offline")))).single()
        assertEquals(listOf(idle.id, active.id), space.members.map { it.id })
        val pinned = AgentPreferences().togglePin(idle.id).togglePin(active.id)
        assertEquals(listOf(idle.id, active.id), pinned.sessions)
        assertEquals(listOf(idle.id, active.id), space.members.map { it.id })
        assertEquals(listOf(active.id), buildAgentList(listOf(idle, active), settings = MachineSpaceTree).groups.single().visibleRows.map { it.id })
        val muted = AgentPreferences(spaces = listOf(space.preferenceKey))
        val allowed = muted.toggleMute(idle.id, space.preferenceKey)
        assertFalse(allowed.mutes(idle))
        assertEquals(setOf(idle.id), allowed.allowed)
        val remuted = allowed.toggleMute(idle.id, space.preferenceKey)
        assertTrue(remuted.mutes(idle))
        // Muting again inside a muted space is the agent's own mute (iOS rule): it survives unmuting the space.
        assertEquals(listOf(idle.id), remuted.sessions)
        assertTrue(remuted.allowed.isEmpty())
        assertTrue(remuted.copy(spaces = emptyList()).mutes(idle))
        assertTrue(allowed.copy(sessions = listOf(idle.id)).mutes(idle))
        assertFalse(AgentPreferences().toggleMute(idle.id, null).toggleMute(idle.id, null).mutes(idle))
    }

    @Test fun filteringOrPinningRowsDoesNotMoveTheProjectsFolder() {
        fun row(id: String, state: String, cwd: String) =
            AgentSession(Session(machineId = "mac", sessionId = id, state = state, spaceId = "desk",
                spaceName = "site", cwd = cwd))
        val rows = listOf(
            row("usual-a", "idle", "/work/main/site"),
            row("usual-b", "idle", "/work/main/site"),
            row("elsewhere", "working", "/work/elsewhere/site"),
        )
        val mac = Machine("mac", online = true, home = "/Users/sample")

        val filtered = buildAgentList(rows, listOf(mac),
            settings = AgentListSettings(grouping = AgentGrouping.Space, quick = AgentQuickFilter.Working)).groups.single()
        assertTrue(filtered.visibleRows.any { it.id == "mac/elsewhere" })
        assertEquals("/work/main/site", filtered.projectFolder?.path)

        val pinned = AgentPreferences(sessions = listOf("mac/usual-a", "mac/usual-b"))
        val pinnedList = buildAgentList(rows, listOf(mac), pins = pinned,
            settings = AgentListSettings(grouping = AgentGrouping.Space)).groups.single { it.id == "mac/desk" }
        assertTrue(pinnedList.rows.any { it.id == "mac/elsewhere" })
        assertEquals("/work/main/site", pinnedList.projectFolder?.path)

        val machineFiltered = buildAgentList(rows, listOf(mac),
            settings = AgentListSettings(grouping = AgentGrouping.MachineSpace, quick = AgentQuickFilter.Working)).groups.single()
        assertEquals(listOf("elsewhere"), machineFiltered.visibleRows.map { it.session.sessionId })
        assertEquals("/work/main/site", machineFiltered.projectFolder?.path)

        val machineSearched = buildAgentList(rows, listOf(mac),
            settings = AgentListSettings(grouping = AgentGrouping.MachineSpace, query = "elsewhere")).groups.single { it.id == "mac/desk" }
        assertEquals(listOf("elsewhere"), machineSearched.visibleRows.map { it.session.sessionId })
        assertEquals("/work/main/site", machineSearched.projectFolder?.path)

        val machinePinned = buildAgentList(rows, listOf(mac), pins = pinned,
            settings = AgentListSettings(grouping = AgentGrouping.MachineSpace)).groups.single { it.id == "mac/desk" }
        assertEquals(setOf("elsewhere"), machinePinned.rows.map { it.session.sessionId }.toSet())
        assertEquals("/work/main/site", machinePinned.projectFolder?.path)
    }
    @Test fun searchingByMachineKeepsOnlyMatchingProjectsAndRows() {
        fun row(id: String, spaceId: String, cwd: String, title: String) =
            AgentSession(Session(machineId = "mac", sessionId = id, state = "working", spaceId = spaceId,
                spaceName = spaceId, cwd = cwd, title = title))
        val rows = listOf(
            row("match", "desk", "/work/main/site", "unique needle"),
            row("desk-other", "desk", "/work/main/site", "other desk work"),
            row("unrelated", "other", "/work/other/docs", "unrelated work"),
        )
        val mac = Machine("mac", online = true, home = "/Users/sample")

        val searched = buildAgentList(rows, listOf(mac),
            settings = AgentListSettings(grouping = AgentGrouping.MachineSpace, query = "needle"))

        assertEquals(listOf("mac/desk"), searched.groups.map { it.id })
        assertEquals(listOf("match"), searched.groups.single().visibleRows.map { it.session.sessionId })
    }
}
