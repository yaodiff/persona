package persona.core.data

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.fail
import org.junit.Test
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.InboxAssistantConnection
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.SessionsRepository

/**
 * What is written for one manager goes to that manager (docs/assistant/managers.md step 4): each request names the manager it
 * was made for, whatever the assistant page shows by the time it goes; the main queue's lookups are the default's.
 */
class AssistantManagerPinTest {
    @After fun reset() = AssistantManagerChoice.reset()

    /** Each manager's main conversation (absent: none yet); every call records the manager it named. */
    private class Transport(val mains: MutableMap<String?, String>) : InboxAssistantTransport {
        val asked = mutableListOf<String>()
        var failFor: String? = null
        var onAsk: () -> Unit = {}
        override val scope = "scope"
        override suspend fun assistant() = error("every lookup names its manager")
        override suspend fun createConversation(main: Boolean) = error("every create names its manager")
        override suspend fun assistantOf(manager: String?): JsonObject {
            asked += "get ${manager ?: "-"}"; onAsk()
            if (manager != null && manager == failFor) throw java.io.IOException("offline")
            return buildJsonObject { put("machine_id", "mac"); put("main", mains[manager].orEmpty()); put("conversations", buildJsonArray { }) }
        }
        override suspend fun createConversationOf(manager: String?, main: Boolean, launcher: String?): JsonObject {
            asked += "create ${manager ?: "-"}"
            val id = "${manager ?: "default"}-new"
            if (main) mains[manager] = id
            return buildJsonObject { put("session", buildJsonObject { put("machine_id", "mac"); put("session_id", id); put("assistant", true) }) }
        }
        override suspend fun pins() = buildJsonObject { put("sessions", buildJsonArray { }) }
        override suspend fun inbox() = error("unused")
        override suspend fun setDone(itemId: String, done: Boolean) = error("unused")
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = error("unused")
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = error("unused")
        override suspend fun mutes() = error("unused")
        override suspend fun media(ref: String) = error("unused")
    }

    private class Sessions : SessionsRepository {
        override val machines: StateFlow<List<Machine>> = MutableStateFlow(emptyList())
        override val sessions: StateFlow<List<Session>> = MutableStateFlow(emptyList())
        override suspend fun refresh() {}
    }

    @Test fun aManagersMainConversationIsAskedOfItAloneAndMadeThereIfMissing() = runTest {
        val transport = Transport(mutableMapOf(null to "d-main", "m-3" to "x-main"))
        val connection = object : InboxAssistantConnection { override val current = MutableStateFlow<InboxAssistantTransport?>(transport) }
        val repository = RealAssistantRepository(connection, Sessions(), backgroundScope)
        AssistantManagerChoice.choose("hub", "m-3", "assistant")
        // The page moved to m-4 while the message for m-3 was being sent: it still asks m-3.
        transport.onAsk = { AssistantManagerChoice.choose("hub", "m-4", "assistant") }
        transport.asked.clear()
        assertEquals(SessionKey("mac", "x-main"), repository.mainConversationOf("m-3"))
        assertEquals(listOf("get m-3"), transport.asked)
        // m-5 has none yet: made on m-5, as its main one.
        transport.asked.clear()
        assertEquals(SessionKey("mac", "m-5-new"), repository.mainConversationOf("m-5"))
        assertEquals(listOf("get m-5", "create m-5"), transport.asked)
        // Not reachable: it fails rather than answer another manager's (the words stay in the composer).
        transport.failFor = "m-3"; transport.asked.clear()
        try { repository.mainConversationOf("m-3"); fail("expected a failure") } catch (_: java.io.IOException) { }
        assertEquals(listOf("get m-3"), transport.asked)
    }

    @Test fun anExistingManagersMainCanBeReadWithoutCreatingOne() = runTest {
        val transport = Transport(mutableMapOf(null to "d-main", "m-3" to "x-main"))
        val connection = object : InboxAssistantConnection { override val current = MutableStateFlow<InboxAssistantTransport?>(transport) }
        val repository = RealAssistantRepository(connection, Sessions(), backgroundScope)
        AssistantManagerChoice.choose("hub", null, "assistant")

        assertNull(repository.mainConversationOrNull("assistant"))
        assertEquals(emptyList<String>(), transport.asked)

        assertEquals(SessionKey("mac", "x-main"), repository.mainConversationOrNull("m-3"))
        assertEquals(listOf("get m-3"), transport.asked)
        assertEquals(emptyList<String>(), transport.asked.filter { it.startsWith("create") })

        transport.mains.remove("m-3")
        transport.asked.clear()
        assertNull(repository.mainConversationOrNull("m-3"))
        assertEquals(listOf("get m-3"), transport.asked)
        assertEquals(emptyList<String>(), transport.asked.filter { it.startsWith("create") })
    }

    @Test fun theMainQueueIsTheDefaultsWhateverThePageShows() = runTest {
        val transport = Transport(mutableMapOf(null to "d-main", "m-3" to "x-main"))
        val resolver = AssistantMainConversationResolver(transport)
        AssistantManagerChoice.choose("hub", "m-3", "assistant")
        assertEquals(SessionKey("mac", "d-main"), resolver.findMainConversation())
        transport.mains.remove(null)
        assertEquals(SessionKey("mac", "default-new"), resolver.createMainConversation())
        assertEquals(listOf("get -", "get -", "create -"), transport.asked)
    }

    @Test fun aNewConversationStaysWithTheManagerItWasStartedFor() = runTest {
        val transport = Transport(mutableMapOf(null to "d-main", "m-3" to "x-main"))
        val connection = object : InboxAssistantConnection { override val current = MutableStateFlow<InboxAssistantTransport?>(transport) }
        val repository = RealAssistantRepository(connection, Sessions(), backgroundScope)
        AssistantManagerChoice.choose("hub", "m-3", "assistant")
        transport.onAsk = { AssistantManagerChoice.choose("hub", null, "assistant") }
        transport.asked.clear()
        repository.startConversation()
        assertEquals(listOf("get m-3", "create m-3"), transport.asked)
    }
}
