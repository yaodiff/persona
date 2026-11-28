package persona.core.data

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.InboxAssistantConnection
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.SessionsRepository

class AssistantStartLauncherTest {
    private class Transport(var main: String) : InboxAssistantTransport {
        /** Each create: whether it was the main one, and the launcher it named (null: the default overload). */
        val created = mutableListOf<Pair<Boolean, String?>>()
        override val scope = "scope"
        override suspend fun assistant() = buildJsonObject { put("machine_id", "mac"); put("main", main); put("conversations", buildJsonArray { }) }
        override suspend fun createConversation(main: Boolean) = create(main, null)
        override suspend fun createConversation(main: Boolean, launcher: String) = create(main, launcher)
        override suspend fun createConversationOf(
            manager: String?,
            main: Boolean,
            pick: persona.core.contracts.ModelPick,
        ): JsonObject {
            created += main to listOfNotNull(pick.launcher, pick.model).joinToString(":")
            return buildJsonObject {
                put("session", buildJsonObject {
                    put("machine_id", "mac")
                    put("session_id", "c${created.size}")
                    put("assistant", true)
                })
            }
        }
        private fun create(main: Boolean, launcher: String?): JsonObject {
            created += main to launcher
            return buildJsonObject { put("session", buildJsonObject { put("machine_id", "mac"); put("session_id", "c${created.size}"); put("assistant", true) }) }
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

    @Test fun aLauncherGoesToItsOwnOverloadAndMainFollowsTheHub() = runTest {
        val transport = Transport(main = "")
        val connection = object : InboxAssistantConnection { override val current = MutableStateFlow<InboxAssistantTransport?>(transport) }
        val repository = RealAssistantRepository(connection, Sessions(), backgroundScope)

        // No main yet: the first conversation, on a launcher, becomes the main one.
        assertEquals(SessionKey("mac", "c1"), repository.startConversation("cc_x"))
        transport.main = "c1"
        assertEquals(SessionKey("mac", "c2"), repository.startConversation("cc_y"))
        assertEquals(SessionKey("mac", "c3"), repository.startConversation())
        assertEquals(listOf(true to "cc_x", false to "cc_y", false to null), transport.created)
    }

    @Test fun startConversationWithModelPickCreatesMainWhenNoneExists() = runTest {
        val transport = Transport(main = "")
        val connection = object : InboxAssistantConnection { override val current = MutableStateFlow<InboxAssistantTransport?>(transport) }
        val repository = RealAssistantRepository(connection, Sessions(), backgroundScope)

        val created = repository.startConversation(persona.core.contracts.ModelPick("gemini", "gemini-2.5-flash"))
        assertEquals(SessionKey("mac", "c1"), created)
        assertEquals(listOf(true to "gemini:gemini-2.5-flash"), transport.created)
        assertEquals(SessionKey("mac", "c1"), repository.assistant.value.mainSession)
    }
}
