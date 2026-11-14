package persona.feature.inbox

import androidx.test.core.app.ApplicationProvider
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxChainLink
import persona.core.contracts.Session
import persona.core.contracts.inboxEventFields
import persona.core.designsystem.InboxNowIcon
import persona.core.designsystem.InlineTone
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.inboxNowIcon
import persona.core.designsystem.resolve
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.stateWord
import java.io.File

/** Every shared timeline case, plus the live-session lookup behind an Agent step. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "en")
class InboxTimelineTest {
    @Test fun everySharedCaseMatchesItsRowsIconsAndReportedFlags() {
        val root = System.getProperty("persona.repo.root")?.let(::File) ?: generateSequence(File(".").canonicalFile) { it.parentFile }
            .first { File(it, ".git").exists() && File(it, "android").isDirectory }
        val fixture = Json.parseToJsonElement(File(root, "spec/fixtures/inbox/timeline.json").readText()).jsonObject
        for (case in fixture.getValue("cases").jsonArray.map { it.jsonObject }) {
            val name = case.getValue("name").jsonPrimitive.content
            val chain = inboxEventFields(case.getValue("input").jsonObject.getValue("item").jsonObject).chain
            val expected = case.getValue("expected").jsonObject
            val sessions = case.getValue("input").jsonObject["sessions"]?.jsonArray?.map { AgentSession.fromJson(it.jsonObject) }.orEmpty()
            val liveStates = expected["live_state"]?.jsonArray?.associate {
                it.jsonObject.getValue("id").jsonPrimitive.content to it.jsonObject.getValue("session_state").jsonPrimitive.content
            }.orEmpty()
            for (entry in chain) {
                assertEquals("$name/${entry.id}/live_state", liveStates.containsKey(entry.id), showsAgentState(chain, entry, sessions))
                liveStates[entry.id]?.let { state ->
                    assertEquals("$name/${entry.id}/session_state", state, chainAgentSession(entry, sessions)?.state?.wire)
                }
            }
            assertEquals("$name/folded", expected.getValue("folded").jsonArray.map { it.jsonPrimitive.content }, shownChain(chain, false).map { it.id })
            assertEquals("$name/expanded", expected.getValue("expanded").jsonArray.map { it.jsonPrimitive.content }, shownChain(chain, true).map { it.id })
            for (step in expected.getValue("steps").jsonArray.map { it.jsonObject }) {
                val entry = chain.single { it.id == step.getValue("id").jsonPrimitive.content }
                assertEquals("$name/${entry.id}/icon", step.getValue("icon").jsonPrimitive.content, chainIconMeaning(entry))
                assertEquals("$name/${entry.id}/reported", step.getValue("reported").jsonPrimitive.content == "true", entry.kind == "reported")
            }
        }
    }

    @Test fun nowIconsDistinguishWaitingWorkNeedsYouFailureAndStaticStates() {
        assertEquals(InboxNowIcon(InlineTone.Busy, PersonaIcons.time), inboxNowIcon("waiting"))
        for (key in listOf("queued", "looking", "agent_working")) assertEquals(InboxNowIcon(InlineTone.Busy, null), inboxNowIcon(key))
        for (key in listOf("confirm_reply", "confirm_agent", "decide", "follow_up_ask", "check_due", "needs_you")) {
            assertEquals(InboxNowIcon(InlineTone.Caution, null), inboxNowIcon(key))
        }
        assertEquals(InboxNowIcon(InlineTone.Failed, null), inboxNowIcon("failed"))
        for (key in listOf("done_replied", "done_manager", "done_user", "done_read", "done_dismissed", "done_muted",
            "done_superseded", "fyi", "folded")) assertEquals(InboxNowIcon(InlineTone.Done, null), inboxNowIcon(key))
    }

    @Test fun reportedUsesDetailOnceWithoutAnInlineDuplicate() {
        val record = InboxRecord(InboxItem("event"))
        val reported = InboxChainEntry("r1", kind = "reported", text = "Assistant says: sent it", detail = "sent it")
        assertEquals("sent it", chainEntryText(reported))
        assertEquals(ChainTarget.None, chainRowTarget(reported, record))
        assertEquals("Assistant says: sent it", chainEntryText(reported.copy(detail = "  ")))
        val linked = reported.copy(link = InboxChainLink(type = "session", machineId = "m1", sessionId = "s1"))
        assertTrue(chainRowTarget(linked, record) is ChainTarget.Page)
    }

    @Test fun agentStateAppearsOnlyOnTheNewestEntryForEachSession() {
        val working = agent("m1", "s1", "working")
        val other = agent("m2~g12345678", "s2", "unread")
        val offline = agent("m3", "s3", "offline")
        val sessions = listOf(working, other, offline)
        val first = entry("agent", "m1", "s1")
        val latest = entry("agent_stopped", "m1", "s1")
        val second = entry("agent", "m2", "s2")
        val third = entry("agent", "m3", "s3")
        val chain = listOf(first, latest, second, third)
        assertFalse(showsAgentState(chain, first, sessions))
        assertTrue(showsAgentState(chain, latest, sessions))
        assertTrue(showsAgentState(chain, second, sessions))
        // Offline is a real state too and uses the same stateWord as the Agents list; only a missing session has no words.
        assertTrue(showsAgentState(chain, third, sessions))
        assertFalse(showsAgentState(chain, latest, emptyList()))
    }

    private fun agent(machine: String, session: String, state: String, awaits: Boolean = false) = AgentSession(
        Session(machineId = machine, sessionId = session, status = state, state = state, awaitsReply = awaits),
    )

    private fun entry(kind: String, machine: String = "m1", session: String = "s-agent") = InboxChainEntry(
        "c1", kind = kind, link = InboxChainLink(type = "session", machineId = machine, sessionId = session),
    )

    @Test fun agentStepsReadTheCurrentSessionStateIncludingGroupSuffixes() {
        val res = ApplicationProvider.getApplicationContext<android.content.Context>().resources
        val working = agent("m1", "s-agent", "working")
        val stopped = agent("m2~g12345678", "s-agent", "offline")
        val asking = agent("m3", "s-agent", "idle", awaits = true)
        assertSame(working, chainAgentSession(entry("agent"), listOf(working)))
        assertSame(stopped, chainAgentSession(entry("agent_stopped", machine = "m2"), listOf(stopped)))
        assertSame(asking, chainAgentSession(entry("agent", machine = "m3"), listOf(asking)))
        assertEquals("Working", working.stateWord(false).resolve(res))
        assertEquals("Not running", stopped.stateWord(false).resolve(res))
        assertEquals("Awaiting reply", asking.stateWord(false).resolve(res))
        assertNull(chainAgentSession(entry("handling"), listOf(working)))
        assertNull(chainAgentSession(entry("agent", machine = "unknown"), listOf(working)))
        assertNull(chainAgentSession(InboxChainEntry("c2", kind = "agent"), listOf(working)))
    }
}
