package persona.feature.assistant

import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentPreferences
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** "Needs you now" on the Persona home (the old app's ShellNeedsYou, iOS's). */
class NeedsYouTest {
    @Test fun foldedChildRequestsHaveOneParentRowAndUseItsMuteAndTitle() {
        val parent = Session("m", "p", title = "Plan the release")
        val cards = listOf(request("one", "child", title = "Review").copy(foldedUnder = "p"),
            request("two", "other", title = "Test").copy(foldedUnder = "p"), request("own", "p"))
        val result = needsYou(cards, listOf(parent), AgentPreferences(), emptyList())
        assertEquals(1, result.total)
        val line = result.lines.single() as NeedsYouLine.Agent
        assertEquals(parent.key, line.session)
        assertEquals(parent.title, line.who)
        assertEquals("one", line.requestId)
        assertEquals("child", cards.first().sessionId)
        assertTrue(needsYou(cards, listOf(parent), AgentPreferences(sessions = listOf("m/p")), emptyList()).isEmpty)
    }

    private fun request(id: String, session: String, kind: String = "plan", title: String? = null, cwd: String? = null) =
        Interaction(id, machineId = "m", sessionId = session, sessionTitle = title, cwd = cwd, kind = kind)

    private fun message(id: String, updated: String, tier: String? = "action", done: String? = null, lark: Boolean = true,
                        title: String = "标题 $id", summary: String? = null, preview: String = "预览 $id") =
        NeedsYouMessage(InboxItem(id, title = title, preview = preview, summary = summary, tier = tier, updatedAt = updated, doneAt = done), lark)

    @Test fun agentsComeFirstThenTheInboxNewestFirst() {
        val needs = needsYou(
            listOf(request("r1", "a", title = "迁移")),
            emptyList(), AgentPreferences(),
            listOf(message("old", "2026-11-01T08:00:00Z"), message("new", "2026-11-02T08:00:00Z", lark = false)),
        )
        assertEquals(listOf("agent:r1", "inbox:new", "inbox:old"), needs.lines.map { it.key })
        assertEquals(0, needs.more)
        val mail = needs.lines[1] as NeedsYouLine.Inbox
        assertEquals(false, mail.lark)
        assertTrue((needs.lines[2] as NeedsYouLine.Inbox).lark)
    }

    @Test fun threeLinesThenHowManyMoreAgentsIncluded() {
        val needs = needsYou(
            (1..4).map { request("r$it", "s$it") }, emptyList(), AgentPreferences(),
            listOf(message("x", "2026-11-02T08:00:00Z")),
        )
        assertEquals(listOf("agent:r1", "agent:r2", "agent:r3"), needs.lines.map { it.key })
        assertEquals("the fourth agent and the message", 2, needs.more)
        assertEquals(5, needs.total)
    }

    @Test fun personaOwnAndMutedConversationsAreLeftOut() {
        val sessions = listOf(
            Session(machineId = "m", sessionId = "persona", assistant = true),
            Session(machineId = "m", sessionId = "quiet", spaceName = "空间"),
            Session(machineId = "m", sessionId = "allowed", spaceName = "空间"),
            Session(machineId = "m", sessionId = "muted"),
        )
        val mutes = AgentPreferences(spaces = listOf("m/空间"), sessions = listOf("m/muted"), allowed = setOf("m/allowed"))
        val needs = needsYou(
            listOf(request("r1", "persona"), request("r2", "quiet"), request("r3", "allowed"), request("r4", "muted"),
                request("r5", "main"), request("r5", "main")),
            sessions, mutes, emptyList(), isAssistant = { it == SessionKey("m", "main") },
        )
        assertEquals("muted by space unless let through; Persona's own never", listOf("agent:r3"), needs.lines.map { it.key })
    }

    @Test fun onlyTheInboxItemsStillToDoInNeedsYou() {
        val needs = needsYou(emptyList(), emptyList(), AgentPreferences(), listOf(
            message("fyi", "2026-11-02T08:00:00Z", tier = "fyi"),
            message("done", "2026-11-02T09:00:00Z", done = "2026-11-02T10:00:00Z"),
            message("untriaged", "2026-11-02T07:00:00Z", tier = null),
            message("action", "2026-11-02T06:00:00Z"),
        ))
        assertEquals("an item the hub has not sorted yet counts, as in the inbox", listOf("inbox:untriaged", "inbox:action"),
            needs.lines.map { it.key })
    }

    @Test fun whoAndWhatOnOneLine() {
        val sessions = listOf(Session(machineId = "m", sessionId = "titled", title = "博客迁移"))
        val needs = needsYou(
            listOf(
                request("r1", "titled", kind = "question", title = "Claude Code").copy(questions = listOf(Question("先迁移\n哪一篇？"))),
                request("r2", "folder", cwd = "/home/me/code/weather/"),
                request("r3", "nothing", kind = "screen"),
            ),
            sessions, AgentPreferences(),
            listOf(message("m1", "2026-11-02T08:00:00Z", title = "", summary = "要你\n回复", lark = false)),
            shown = 4,
        )
        // An agent without a title or folder, and a mail without a title or sender, have no name of their own:
        // the row reads "Agent" or "Mail" in the app's language (needsYouWho).
        assertEquals(listOf("博客迁移", "weather", "", ""), needs.lines.map { it.who })
        assertEquals("先迁移 哪一篇？", needs.lines[0].what)
        assertEquals("计划等你审批", needs.lines[1].what)
        assertEquals("要你 回复", needs.lines[3].what)
    }

    @Test fun permissionTextUsesTheHubTitleWithoutChangingTheToolType() {
        val request = request("r", "s", kind = "permission").copy(
            toolName = "Bash", toolTitle = "Run a safe command", summary = "ls",
        )
        val line = needsYou(listOf(request), emptyList(), AgentPreferences(), emptyList()).lines.single()
        assertEquals("请求使用 Run a safe command：ls", line.what)
        assertEquals("Bash", (line as NeedsYouLine.Agent).toolName)
        assertEquals(PhIcons.Terminal, needsYouIcon(line))
    }

    @Test fun nothingNeedsTheUser() {
        val needs = needsYou(emptyList(), emptyList(), AgentPreferences(), listOf(message("fyi", "2026-11-02T08:00:00Z", tier = "fyi")))
        assertTrue(needs.isEmpty)
        assertEquals(0, needs.total)
    }

    @Test fun messagesTheAiIsOnAreLeftOut() {
        // #4848: the inbox page lists these under "Persona is working on it", so the home does not ask the user about them.
        val needs = needsYou(emptyList(), emptyList(), AgentPreferences(), listOf(
            message("mine", "2026-11-02T08:00:00Z"),
            message("busy", "2026-11-02T09:00:00Z").copy(working = true),
        ))
        assertEquals(listOf("inbox:mine"), needs.lines.map { it.key })
        assertEquals(1, needs.total)
    }

    @Test fun theHubsInProgressSectionIsNotNeedsYou() {
        // The home takes "Needs you" by the hub's section, as the inbox page does: a `status` of working is not the user's.
        val needs = needsYou(emptyList(), emptyList(), AgentPreferences(), listOf(
            message("mine", "2026-11-02T08:00:00Z"),
            NeedsYouMessage(InboxItem("hub", tier = "action", status = "working", updatedAt = "2026-11-02T09:00:00Z"), lark = true),
        ))
        assertEquals(listOf("inbox:mine"), needs.lines.map { it.key })
    }
}
