package persona.android.di

import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*

class ChildPageGroupTest {
    @Test fun childDeltasKeepTheirHubForRowsAndRemovals() = runBlocking {
        var requested: SessionKey? = null
        var checkpoint: String? = null
        val source = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun childPage(session: Session, since: String?): SessionListPage {
                requested = session.key; checkpoint = since
                return SessionListPage(listOf(Session("m", "child", foldedUnder = "parent")),
                    remove = listOf("""["m","old/child"]"""), revision = "r2", total = 1)
            }
        }
        val names = GroupNames("g12345678")
        val page = source.inGroup(names).childPage(Session(names.machineId("m"), "parent"), "r1")
        assertEquals(SessionKey("m", "parent"), requested)
        assertEquals("r1", checkpoint)
        assertEquals(names.machineId("m"), page.sessions.single().machineId)
        assertEquals(listOf(names.machineId("m"), "old/child"), JsonCodec.decodeFromString<List<String>>(page.remove.single()))
        assertEquals("r2", page.revision)
    }
}
