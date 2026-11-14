package persona.feature.chat

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.withContext
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.navigation.MessageJumps

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class ChatMessageJumpPagingTest {
    @get:Rule val rule = createComposeRule()

    private val key = SessionKey("macbook", "weather")
    private val recent = Item("recent", 20, role = "assistant", kind = "text", text = "Recent reply")
    private val older = Item("older-hit", 10, role = "assistant", kind = "text", text = "Older search hit")

    private inner class PagingRepository(loadingOlder: Boolean = false) : ChatRepository {
        val snapshot = MutableStateFlow(ChatSnapshot(
            items = listOf(recent), loading = false, hasOlder = true, loadingOlder = loadingOlder))
        var attempts = 0
        var cancelled = 0

        override fun observe(session: SessionKey) = snapshot

        override suspend fun loadOlder(session: SessionKey) {
            attempts++
            snapshot.value = snapshot.value.copy(loadingOlder = true)
            try {
                // Let the loadingOlder=true snapshot recompose ChatMessageJump before the page arrives.
                withContext(Dispatchers.IO) { delay(250) }
                snapshot.value = snapshot.value.copy(items = listOf(older) + snapshot.value.items, hasOlder = false)
            } catch (e: CancellationException) {
                cancelled++
                throw e
            } finally {
                snapshot.value = snapshot.value.copy(loadingOlder = false)
            }
        }

        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
    }

    @Test fun olderSearchHitLoadsOnePageWithoutCancellingIt() {
        val chats = PagingRepository()
        showChat(chats)
        requestJump()

        awaitOlderTarget(chats)
        assertEquals("cancelled page loads", 0, chats.cancelled)
        assertEquals("page loads", 1, chats.attempts)
    }

    @Test fun jumpWaitsForAnUnrelatedLoadThenStartsPaging() {
        val chats = PagingRepository(loadingOlder = true)
        showChat(chats)
        requestJump()
        rule.waitUntil(5_000) { MessageJumps.pending == null }
        rule.waitForIdle()
        assertEquals("the unrelated load must finish before jump paging starts", 0, chats.attempts)

        rule.runOnIdle { chats.snapshot.value = chats.snapshot.value.copy(loadingOlder = false) }

        awaitOlderTarget(chats)
        assertEquals("cancelled page loads", 0, chats.cancelled)
        assertEquals("page loads", 1, chats.attempts)
    }

    private fun showChat(chats: ChatRepository) {
        val done = Session(machineId = key.machineId, sessionId = key.sessionId, title = "Weather", status = "done")
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(emptyList<Machine>())
            override val sessions = MutableStateFlow(listOf(done))
            override suspend fun refresh() = Unit
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }

        rule.setContent { ChatScreen(key, chats, sender, interactions, sessions, onBack = {}) }
        rule.waitUntil(10_000) {
            rule.onAllNodesWithText("Recent reply", substring = true).fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun requestJump() {
        rule.runOnIdle { MessageJumps.request(key.machineId, key.sessionId, older.id) }
    }

    private fun awaitOlderTarget(chats: PagingRepository) {
        try {
            rule.waitUntil(5_000) { chats.snapshot.value.items.any { it.id == older.id } }
            rule.waitUntil(5_000) { !chats.snapshot.value.loadingOlder }
            rule.waitForIdle()
        } catch (error: Throwable) {
            throw AssertionError("older page was not published; attempts=${chats.attempts} " +
                "cancelled=${chats.cancelled} snapshot=${chats.snapshot.value}", error)
        }
    }
}
