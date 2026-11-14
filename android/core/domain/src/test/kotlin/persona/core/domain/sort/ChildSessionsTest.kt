package persona.core.domain.sort

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.domain.answer.firstBySession
import persona.core.domain.answer.askingLine
import persona.core.domain.tasks.*

/** Mirrors web child-sessions/folded-rows/child-counts cases, including a waiting runtime child. */
class ChildSessionsTest {
    private val parent = Session("m", "p", title = "Lead", state = "idle", children = SessionChildren(3, working = 1, needsYou = 1))
    private val working = Session("m", "work", title = "Review", state = "working", foldedUnder = "p")
    private val asking = Session("m", "ask", state = "needs_you", askingParentSessionId = "p", foldedUnder = "p")
    private val grandchild = Session("m", "leaf", state = "idle", parentSessionId = "work", foldedUnder = "p")
    private val rows = listOf(parent, working, asking, grandchild)
    private val card = Interaction("q", machineId = "m", sessionId = "ask", kind = "question", foldedUnder = "p",
        sessionTitle = "Choose a target", questions = listOf(Question("Which target?")))

    @Test fun foldedStateKeepsOwnUnreadAndUnansweredReplyButNeedsYouAlwaysWins() {
        val childWorks = SessionChildren(1, working = 1)
        for (own in listOf("idle", "offline", "unknown", "working"))
            assertEquals(own, AgentState.Working, parent.copy(state = own, children = childWorks).foldedState)
        assertEquals(AgentState.Unread, parent.copy(state = "unread", children = childWorks).foldedState)
        assertEquals(AgentState.Idle, parent.copy(awaitsReply = true, children = childWorks).foldedState)
        assertEquals(AgentState.NeedsYou, parent.copy(state = "unread").foldedState)
        assertEquals(AgentState.NeedsYou, parent.copy(state = "needs_you", children = null).foldedState)
        assertEquals("idle", parent.state)
    }

    @Test fun everyDepthAndWaitingChildDisappearOnlyUnderAListedParentOnTheSameMachine() {
        assertEquals(listOf("p"), foldedSessions(rows).map { it.sessionId })
        assertEquals(AgentState.NeedsYou, foldedSessions(rows).single().agentState)
        assertEquals(listOf("work", "ask", "leaf"), foldedSessions(rows.drop(1)).map { it.sessionId })
        assertEquals(2, foldedSessions(listOf(parent.copy(machineId = "other"), working)).size)
        assertEquals(2, foldedSessions(listOf(parent, working.copy(foldedUnder = null, parentSessionId = "p"))).size)
        assertEquals(3, childSessions(rows, parent.key).size)
        val orphan = Session("m", "orphan", agent = "codex", sourceKind = "codex_app_server", isSubagent = true,
            parentSessionId = "gone", state = "working")
        val listed = buildAgentList(listOf(AgentSession(orphan)), settings = AgentListSettings(hierarchy = true))
        assertEquals(1, listed.counts[AgentQuickFilter.Working])
        assertEquals("orphan", listed.groups.flatMap { it.rows }.single().session.sessionId)
    }

    @Test fun listsDockCountsProjectsAndRequestsHaveOneParentHome() {
        for (grouping in AgentGrouping.selectable) {
            val list = buildAgentList(rows.map(::AgentSession), settings = AgentListSettings(grouping = grouping))
            assertEquals(grouping.name, 1, list.counts[AgentQuickFilter.NeedsYou])
            assertEquals(grouping.name, 0, list.counts[AgentQuickFilter.Working])
            assertTrue(list.children.isEmpty())
            assertEquals(setOf("p"), list.groups.flatMap { it.rows }.map { it.session.sessionId }.toSet())
        }
        assertEquals(listOf("p"), dockAgents("main", "main", rows, emptyList()).map { it.sessionId })
        assertEquals(StateTally(needsYou = 1), rows.map(::AgentSession).stateTally())
        assertEquals(1, machineProjects(rows.map(::AgentSession), "m", AgentPreferences(), AgentPreferences()).agents)
        val own = card.copy(id = "own", sessionId = "p", foldedUnder = null)
        val waiting = needsYouRows(listOf(card, own, card), needsYouWaiting(rows))
        assertEquals(1, waiting.size)
        assertEquals(parent.key, waiting.single().key)
        assertEquals(2, waiting.single().cards.size)
        assertEquals(listOf(parent.key), needsYouRows(listOf(card), needsYouWaiting(listOf(asking))).map { it.key })
        assertEquals("ask", card.sessionKey.sessionId)
        assertEquals("own", listOf(card, own).firstBySession()[parent.key]?.id)
        assertNotNull(parent.askingLine(listOf(card).firstBySession()[parent.key]))
        val entries = assistantEntries("main", "main", emptyList(), rows, listOf(card), emptyList(), emptyList(), 0L)
        assertTrue(entries.running.isEmpty())
        assertEquals(listOf(card), (entries.needsYou.single() as NeedsYouEntry.Agent).asks)
    }

    @Test fun backgroundWaitAndSubagentCountsDoNotHideOrDoubleCountChildren() {
        val s = parent.copy(state = "working", backgroundWaiting = true, runningSubagents = 3, waiting = false)
        assertFalse(AgentSession(s).waitsInBackground)
        assertEquals(3, s.backgroundWork()?.subagents)
        assertEquals(4, s.copy(children = SessionChildren(4, working = 4)).backgroundWork()?.subagents)
        assertEquals(1, parent.copy(children = SessionChildren(1, working = 1)).backgroundWork()?.subagents)
    }

    @Test fun anAuthoritativeGroupDoesNotDisableAnotherHubsLegacyHierarchy() {
        val legacyParent = Session("old", "lead", state = "working")
        val legacyChild = Session("old", "child", sourceKind = "runtime", agent = "claude", isSubagent = true,
            parentSessionId = "lead", state = "working")
        val modern = parent.copy(machineId = GroupKeys.machine("m", "g12345678"))
        val list = buildAgentList(listOf(modern, legacyParent, legacyChild).map(::AgentSession))
        assertEquals(setOf(modern.key, legacyParent.key), list.groups.flatMap { it.rows }.map { it.session.key }.toSet())
        assertEquals(listOf(legacyChild.key), list.children[legacyParent.key.path].orEmpty().map { it.session.key })
        val cycle = listOf(legacyParent.copy(parentSessionId = "child", sourceKind = "runtime", agent = "claude", isSubagent = true), legacyChild)
        assertEquals(2, dockAgents("main", "main", cycle, emptyList(), hierarchy = true).size)
    }
}
