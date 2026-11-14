package persona.core.contracts

import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.TriageSwitches

/**
 * The closeout on a session's work (#3612), "nobody is working on this" and the decision fields on a to-do (#3618,
 * #3804), and the triage's closeout switch: all optional, read from the wire as the hub sends them, and absent from an
 * older hub without losing what stands beside them.
 */
class CloseoutContractTest {
    private fun session(work: String) = JsonCodec.decodeFromString<Session>(
        """{"machine_id":"m1","session_id":"s1","status":"done","state":"unread","work":$work}""")

    private fun todo(extra: String) = JsonCodec.decodeFromString<Todo>("""{"id":"t1","title":"Ship the importer"$extra}""")

    @Test fun aSessionCarriesItsWholeCloseoutWithTheHubsFinding() {
        val work = session("""{"phase":"","line":"Says merged, but the pull request is not merged",
            "artifacts":[{"kind":"pr","ref":"https://example.com/acme/app/pull/12"}],
            "closeout":{"status":"done","did":["Fixed the retry","Added a test"],"merged":"yes","deployed":"no",
                        "verified":"unit tests pass","missing":"a review","checked":{"merged":"no"}}}""").work!!
        assertEquals("", work.phase)
        assertEquals(listOf(WorkArtifact("pr", "https://example.com/acme/app/pull/12")), work.artifacts)
        val closeout = work.closeout!!
        assertTrue(closeout.known)
        assertEquals(listOf("Fixed the retry", "Added a test"), closeout.did)
        assertEquals("yes", closeout.merged)
        assertEquals("no", closeout.deployed)
        assertEquals("unit tests pass", closeout.verified)
        assertEquals("a review", closeout.missing)
        assertEquals(CloseoutChecked(merged = "no"), closeout.checked)
    }

    @Test fun aListRowCarriesOnlyWhereTheCloseoutStands() {
        val closeout = session("""{"phase":"blocked","on":"external","line":"Waits for CI",
            "closeout":{"status":"blocked","waiting":"CI","merged":"no"}}""").work!!.closeout!!
        assertEquals("blocked", closeout.status)
        assertEquals("CI", closeout.waiting)
        assertNull(closeout.did)
        assertNull(closeout.verified)
        assertNull(closeout.deployed)
        assertNull(closeout.checked)
    }

    @Test fun anOlderHubsWorkAndABrokenCloseoutKeepTheRestOfTheWork() {
        val older = session("""{"phase":"done","line":"Merged PR #12"}""").work!!
        assertNull(older.closeout)
        assertNull(older.artifacts)
        // a closeout of another shape is dropped; the line the lists show stays
        val broken = session("""{"phase":"done","line":"Merged PR #12","closeout":"done","artifacts":{"kind":"pr"}}""").work!!
        assertEquals("Merged PR #12", broken.line)
        assertNull(broken.closeout)
        assertNull(broken.artifacts)
        // a status this app does not know is kept as sent and is no closeout to show
        assertFalse(session("""{"phase":"done","line":"x","closeout":{"status":"parked"}}""").work!!.closeout!!.known)
    }

    @Test fun aToDoSaysNobodyIsWorkingOnItAndWhy() {
        val orphaned = todo(""","orphaned":{"reason":"error","since":"2026-11-10T08:00:00Z"}""").orphaned!!
        assertEquals("error", orphaned.reason)
        assertEquals("2026-11-10T08:00:00Z", orphaned.since)
        assertNull(todo("").orphaned)
        // a reason from a newer hub is kept as sent; a broken field does not lose the to-do
        assertEquals("paused", todo(""","orphaned":{"reason":"paused"}""").orphaned!!.reason)
        assertEquals("Ship the importer", todo(""","orphaned":true""").title)
        assertNull(todo(""","orphaned":true""").orphaned)
    }

    @Test fun aDecisionCarriesItsQuestionOptionsDefaultAndFixedAnswers() {
        val ask = todo(""","ask":{"outcome":"decide","verdict":"Needs your decision: release now?","recommend":"Release now",
            "asked_at":"2026-11-10T08:00:00Z","question":"Release 2.4 to everyone now?","why_owner":"irreversible",
            "background":"From your message on Nov 8; tests pass, not deployed.",
            "options":[{"label":"Release now","leads_to":"Everyone gets 2.4 today"},{"label":"Wait for the fix"}],
            "cost_of_waiting":"The fix for the crash stays unreleased","default":"never","deadline":"2026-11-11T08:00:00Z",
            "responses":["decide_yourself","not_what_i_asked","explain_first"],"response":"explain_first"}""").ask!!
        assertEquals("Release 2.4 to everyone now?", ask.question)
        assertEquals("irreversible", ask.whyOwner)
        assertEquals(listOf(AskOption("Release now", "Everyone gets 2.4 today"), AskOption("Wait for the fix")), ask.options)
        assertEquals("From your message on Nov 8; tests pass, not deployed.", ask.background)
        assertEquals("The fix for the crash stays unreleased", ask.costOfWaiting)
        assertEquals("never", ask.default)
        assertEquals("2026-11-11T08:00:00Z", ask.deadline)
        assertEquals(listOf("decide_yourself", "not_what_i_asked", "explain_first"), ask.responses)
        assertEquals("explain_first", ask.response)
    }

    @Test fun anOlderHubsAskHasNoneOfTheDecisionFields() {
        val ask = todo(""","ask":{"outcome":"decide","verdict":"Needs your decision","recommend":"Release now","asked_at":"2026-11-10T08:00:00Z"}""").ask!!
        assertEquals("Release now", ask.recommend)
        assertNull(ask.question)
        assertNull(ask.options)
        assertNull(ask.responses)
        assertNull(ask.default)
        // options of another shape leave the ask answerable the old way
        val broken = todo(""","ask":{"outcome":"decide","verdict":"v","asked_at":"2026-11-10T08:00:00Z","options":"Release now"}""").ask
        assertNotNull(broken)
        assertNull(broken!!.options)
    }

    @Test fun theCloseoutSwitchIsAbsentFromAnOlderHubAndSentOnlyWhenSet() {
        val older = JsonCodec.decodeFromString<TriageSwitches>("""{"enabled":true}""")
        assertNull(older.closeout)
        // a save of another switch does not send it, so the hub keeps its own value
        assertFalse("closeout" in JsonCodec.parseToJsonElement(JsonCodec.encodeToString(TriageSwitches.serializer(), older.copy(wake = true))).jsonObject)
        val read = JsonCodec.decodeFromString<TriageSwitches>("""{"enabled":true,"closeout":false}""")
        assertEquals(false, read.closeout)
        val off = JsonCodec.parseToJsonElement(JsonCodec.encodeToString(TriageSwitches.serializer(), read)).jsonObject
        assertFalse(off.getValue("closeout").jsonPrimitive.boolean)
        val on = JsonCodec.parseToJsonElement(JsonCodec.encodeToString(TriageSwitches.serializer(), read.copy(closeout = true))).jsonObject
        assertTrue(on.getValue("closeout").jsonPrimitive.boolean)
    }

    @Test fun theCloseoutAskSwitchStaysOnTheWireAndAnOldPayloadDoesNotGainIt() {
        val older = JsonCodec.decodeFromString<TriageSwitches>("""{"enabled":true}""")
        assertNull(older.closeoutAsk)
        val olderSaved = JsonCodec.parseToJsonElement(JsonCodec.encodeToString(TriageSwitches.serializer(), older.copy(wake = true))).jsonObject
        assertFalse("closeout_ask" in olderSaved)

        for (value in listOf(false, true)) {
            val read = JsonCodec.decodeFromString<TriageSwitches>("""{"enabled":true,"closeout_ask":$value}""")
            assertEquals(value, read.closeoutAsk)
            val saved = JsonCodec.parseToJsonElement(JsonCodec.encodeToString(TriageSwitches.serializer(), read.copy(closeoutAsk = !value))).jsonObject
            assertEquals(!value, saved.getValue("closeout_ask").jsonPrimitive.boolean)
        }
    }
}
