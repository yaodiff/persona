package persona.core.domain.sort

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.contracts.SessionTodo

/**
 * An Agents row's line 2 state word and line 3 parts (#4338 A9), with iOS's cases (`AgentRowStyleTests`, `SlotRow` in
 * `ChatsView.swift`, the Avatar style in `AgentRowStyles.swift`).
 */
class AgentRowContentTest {
    private fun row(state: String = "working", f: Session.() -> Session = { this }) = AgentSession(
        Session(machineId = "m", sessionId = "s", title = "Ship the widget", cwd = "/w/app", state = state, agent = "claude").f())

    private fun AgentRowContent.words() = place.joinToString(" · ") { it.english() }

    @Test fun thePlaceLineNamesWhereNotWhat() {
        val s = row { copy(spaceName = "weather", slotName = "widget") }
        // Not "Claude": the row's mark says what runs there.
        assertEquals("weather › widget", s.rowContent(AgentRowContext(located = true)).words())
        // The computer where the list names it (it mixes computers).
        assertEquals("weather › widget · MacBook", s.rowContent(AgentRowContext(located = true, machine = "MacBook")).words())
        // The tab alone where the section is the space; the tree's heading names the computer too.
        assertEquals("widget", s.rowContent().words())
        assertEquals("widget", s.rowContent(AgentRowContext(machine = "MacBook")).words())
    }

    @Test fun theTabIsHerdrsNumberOrItsNameAndTheSpaceFallsBackToTheFolder() {
        assertEquals("#2", row { copy(slotIndex = 2) }.rowContent().words())
        assertEquals("#2", row { copy(slotIndex = 2, slotName = "2") }.rowContent().words())
        assertEquals("app › #2", row { copy(slotIndex = 2) }.rowContent(AgentRowContext(located = true)).words())
        assertEquals("app", row().rowContent(AgentRowContext(located = true)).words())
        assertEquals("", row().rowContent().words())
        // The space page writes the tab its own way.
        assertEquals("Tab 3", row { copy(slotIndex = 3) }.rowContent(AgentRowContext(slot = "Tab 3")).words())
    }

    @Test fun anAgentInATabOfSeveralIsNamedThereAndTheTabIsLeftToItsRow() {
        val s = row { copy(spaceName = "weather", slotName = "widget") }
        assertEquals("Claude · Started it · weather", s.rowContent(AgentRowContext(located = true, pane = AgentPane("Claude", true))).words())
        assertEquals("Codex 2", s.rowContent(AgentRowContext(pane = AgentPane("Codex 2", false))).words())
    }

    @Test fun aSourceIsSaidFirstWithItsAgentsName() {
        // iOS adds why it is view only; Android keeps that among the row's critical facts, which never give way.
        val codex = row { copy(sourceKind = "codex_history", agentName = "Reviewer", capabilities = SessionCapabilities(read = true), slotIndex = 1) }
        assertEquals("Codex history · Reviewer · #1", codex.rowContent().words())
        val live = row { copy(sourceKind = "codex_app_server", capabilities = SessionCapabilities(read = true, send = true)) }
        assertEquals("Codex live", live.rowContent().words())
        // A pane's name wins over the source's (iOS `pane?.name ?? sourceSubtitle`).
        assertEquals("Codex", live.rowContent(AgentRowContext(pane = AgentPane("Codex", false))).words())
    }

    @Test fun theHostOnlyWhereTheListMixesThemAndNeverHerdr() {
        val botmux = row { copy(paneId = "botmux:abc", slotIndex = 1) }
        assertEquals("#1", botmux.rowContent().words())
        assertEquals("botmux · #1", botmux.rowContent(AgentRowContext(host = true)).words())
        assertEquals("#1", row { copy(paneId = "w1:p2", slotIndex = 1) }.rowContent(AgentRowContext(host = true)).words())
        // the host by its name, never its id (#17054)
        assertEquals("Herdr App · #1", row { copy(paneId = "herdr-core:t1", slotIndex = 1) }.rowContent(AgentRowContext(host = true)).words())
        assertEquals("Orca · #1", row { copy(paneId = "orca:tab1:leaf1", slotIndex = 1) }.rowContent(AgentRowContext(host = true)).words())
    }

    @Test fun theSourceTagIsWhereItWasOpened() {
        assertEquals(AgentSource.Terminal, row { copy(paneId = "w1:p2") }.rowContent().source)
        assertEquals(AgentSource.Feishu, row { copy(paneId = "botmux:abc") }.rowContent().source)
        assertNull(row().rowContent().source)
        assertNull(row { copy(paneId = "w1:p2", assistant = true) }.rowContent().source)
    }

    @Test fun theModelIsNamedByItsShortNameElseTheLaunchers() {
        // Never the raw id (DESIGN_SYSTEM.md 3.4), unlike iOS's row.
        assertEquals("Sonnet 4.5", row { copy(model = " claude-sonnet-4-5 ", launcher = "deepseek") }.rowContent().model)
        assertEquals("Deepseek", row { copy(launcher = "deepseek") }.rowContent().model)
        assertNull(row { copy(model = " ") }.rowContent().model)
        assertNull(row().rowContent().model)
    }

    @Test fun theLineShowsWhenItSaysSomethingButNotForTheModelAlone() {
        assertFalse(row { copy(model = "opus") }.rowContent().hasPlaceLine)
        assertTrue(row { copy(paneId = "w1:p2") }.rowContent().hasPlaceLine)
        assertTrue(row { copy(slotIndex = 1) }.rowContent().hasPlaceLine)
        assertTrue(row { copy(todo = SessionTodo("t", "Ship 1.2")) }.rowContent().hasPlaceLine)
        assertEquals("Ship 1.2", row { copy(todo = SessionTodo("t", "Ship 1.2")) }.rowContent().todo)
        assertNull(row { copy(todo = SessionTodo("t", " ")) }.rowContent().todo)
        val muted = row().rowContent(muted = true)
        assertTrue(muted.muted)
        assertTrue(muted.hasPlaceLine)
    }

    @Test fun lineTwoSpellsOutOnlyWhatTheDotCannot() {
        fun word(r: AgentSession, muted: Boolean = false, section: Boolean = false) =
            r.rowContent(AgentRowContext(stateSection = section), muted).stateWord?.english()
        assertEquals("Needs you", word(row("needs_you")))
        assertEquals("Awaiting reply", word(row("idle") { copy(awaitsReply = true) }))
        assertEquals("Waiting in the background", word(row { copy(backgroundWaiting = true) }))
        assertNull(word(row()))
        assertNull(word(row("unread")))
        assertNull(word(row("idle")))
        // Muted, nothing catches the eye; under a section that names the state, the row does not repeat it.
        assertNull(word(row("needs_you"), muted = true))
        assertNull(word(row("idle") { copy(awaitsReply = true) }, muted = true))
        assertNull(word(row("needs_you"), section = true))
    }

    @Test fun aListMixesComputersWhenItsRowsRunOnMoreThanOne() {
        fun at(id: String, machine: String, state: String = "working") =
            AgentSession(Session(machineId = machine, sessionId = id, title = id, cwd = "/w/$id", state = state))
        val machines = listOf(Machine("a", "Alpha", online = true), Machine("b", "Beta", online = true))
        val two = listOf(at("1", "a"), at("2", "b"))
        for (grouping in listOf(AgentGrouping.State, AgentGrouping.Todo, AgentGrouping.Repo, AgentGrouping.MachineSpace)) {
            val list = buildAgentList(two, machines, settings = AgentListSettings(grouping = grouping))
            assertTrue("$grouping", list.mixesComputers(""))
            // One computer picked: its rows say nothing new by naming it.
            assertFalse("$grouping", buildAgentList(two, machines, settings = AgentListSettings(grouping = grouping, machineFilter = "a")).mixesComputers("a"))
            assertFalse("$grouping", buildAgentList(listOf(at("1", "a"), at("3", "a")), machines, settings = AgentListSettings(grouping = grouping)).mixesComputers(""))
        }
        // An offline agent on another computer does not make a mix (iOS counts the live ones).
        assertFalse(buildAgentList(listOf(at("1", "a"), at("2", "b", "offline")), machines,
            settings = AgentListSettings(grouping = AgentGrouping.State)).mixesComputers(""))
    }

    @Test fun aListMixesHostsWhenItsAgentsRunInMoreThanOne() {
        fun at(id: String, pane: String?) = AgentSession(Session(machineId = "m", sessionId = id, title = id, cwd = "/w", state = "working", paneId = pane))
        val settings = AgentListSettings(grouping = AgentGrouping.Todo)
        assertTrue(buildAgentList(listOf(at("1", "w1:p1"), at("2", "botmux:x")), settings = settings).mixesHosts())
        assertFalse(buildAgentList(listOf(at("1", "w1:p1"), at("2", "w1:p2"), at("3", null)), settings = settings).mixesHosts())
    }
}
