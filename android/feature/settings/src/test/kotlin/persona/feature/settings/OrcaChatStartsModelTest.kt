package persona.feature.settings

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.decodeFromString
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine

/**
 * "Start new Agents as Orca chats" (#17650): a change shows at once and settles on the hub's answer; a failed save puts
 * the old value back; a push moves the switch only while nothing is being saved; no switch where the hub sends none.
 */
class OrcaChatStartsModelTest {
    private val box = Machine("m1", "box", online = true, engineHosts = listOf("herdr", "orca"), orcaChatStarts = "off")

    @Test fun showsAtOnceAndSettlesOnTheHubsAnswer() = runTest {
        val answer = CompletableDeferred<Boolean>()
        val sent = mutableListOf<Pair<String, Boolean>>()
        val model = OrcaChatStartsModel(false) { id, on -> sent += id to on; answer.await() }
        val change = async { model.change("m1", true) }
        testScheduler.runCurrent()
        assertEquals(OrcaChatStartsModel.State(shown = true, saving = true), model.state.value)
        model.synchronize(box) // a push from before the save lands meanwhile: it does not move the switch
        assertEquals(true, model.state.value.shown)
        answer.complete(true)
        assertNull(change.await())
        assertEquals(listOf("m1" to true), sent)
        assertEquals(OrcaChatStartsModel.State(shown = true, saving = false), model.state.value)
        model.synchronize(box.copy(orcaChatStarts = "off")) // another device turns it off
        assertEquals(false, model.state.value.shown)
    }

    @Test fun theMachineReadsTheClosedSet() {
        assertEquals(true, JsonCodec.decodeFromString<Machine>("""{"id":"m1","orca_chat_starts":"on"}""").orcaChatSwitch)
        assertEquals(false, JsonCodec.decodeFromString<Machine>("""{"id":"m1","orca_chat_starts":"off"}""").orcaChatSwitch)
        assertNull(JsonCodec.decodeFromString<Machine>("""{"id":"m1"}""").orcaChatSwitch)
        assertNull(JsonCodec.decodeFromString<Machine>("""{"id":"m1","orca_chat_starts":"later-state"}""").orcaChatSwitch)
    }

    @Test fun aFailedSavePutsTheOldValueBack() = runTest {
        val model = OrcaChatStartsModel(false) { _, _ -> error("offline") }
        val failure = model.change("m1", true)
        assertTrue(failure is IllegalStateException)
        assertEquals(OrcaChatStartsModel.State(shown = false, saving = false), model.state.value)
    }

    @Test fun noSwitchWhereTheHubSendsNone() = runTest {
        var calls = 0
        val model = OrcaChatStartsModel(null) { _, on -> calls++; on }
        assertNull(model.change("m1", true))
        assertEquals(0, calls)
        model.synchronize(box)
        assertEquals(false, model.state.value.shown)
        model.synchronize(box.copy(orcaChatStarts = null)) // Orca left the computer's engine set
        assertNull(model.state.value.shown)
        model.synchronize(box.copy(orcaChatStarts = "on"))
        assertEquals(true, model.state.value.shown)
        model.synchronize(box.copy(orcaChatStarts = "later-state")) // a state a later hub may add: no switch, never on or off
        assertNull(model.state.value.shown)
        assertFalse(model.state.value.saving)
    }
}
