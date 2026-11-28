package persona.feature.agents

import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.setMain
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.WorkState
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.HubPreferencesSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.domain.sort.AgentGrouping
import persona.core.domain.sort.AgentQuickFilter
import persona.core.domain.sort.AgentSeenRows
import persona.core.domain.sort.AgentSession

/**
 * Seeing a Just finished agent (#9786): Mark all as read and opening its chat take it out at once, as the seen this phone
 * has not had recorded yet; while its chat is open the row the shell held keeps Just finished's place, drawn as seen.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class AgentsJustFinishedTest {
    private val dispatcher: CoroutineDispatcher = UnconfinedTestDispatcher()
    private var model: AgentsViewModel? = null
    private val done = WorkState("done", line = "Merged", justFinished = true)
    private val sessions = MutableStateFlow(listOf(
        session("finished", "idle").copy(work = done), session("working", "working"), session("seen", "idle"),
    ))
    private val hub = MutableStateFlow(HubPreferencesSnapshot())
    private val visible = MutableStateFlow<SessionKey?>(null)
    private val held = MutableStateFlow<SessionKey?>(null)
    private val finished = MutableStateFlow<SessionKey?>(null)

    @Before fun setMain() { Dispatchers.setMain(dispatcher) }
    @After fun tearDown() {
        model?.let { current -> runBlocking {
            withTimeout(10_000) { current.viewModelScope.coroutineContext[Job]?.cancelAndJoin() }
        } }
        Dispatchers.resetMain()
    }

    @Test fun openingItHoldsItsPlaceDrawnSeenUntilTheChatIsLeft() = runBlocking {
        preferences.setSeenRows(AgentSeenRows.Leave)
        model = model()
        awaitSection("state/just_finished", "machine/finished")
        assertTrue(row("machine/finished").showsJustFinished)

        // The shell captures it before the chat's seen.
        finished.value = SessionKey("machine", "finished")
        visible.value = SessionKey("machine", "finished")
        awaitSection("state/just_finished", "machine/finished")
        // The chat marks it seen here: seen at once, in the same place.
        preferences.markSeen(listOf(sessions.value.first()))
        withTimeout(10_000) { model!!.page.first { page -> page.list.groups.flatMap { it.rows }.any { it.id == "machine/finished" && !it.showsJustFinished } } }
        awaitSection("state/just_finished", "machine/finished")
        // The hub records it and lists it without the flag: still held there.
        sessions.value = sessions.value.map { if (it.sessionId == "finished") it.copy(work = done.copy(justFinished = false)) else it }
        awaitSection("state/just_finished", "machine/finished")
        assertFalse(model!!.page.value.spaces.single { space -> space.members.any { it.id == "machine/finished" } }.quiet(model!!.page.value.preferences.mutes))

        // Left: with the seen ones.
        finished.value = null
        visible.value = null
        awaitIdle("machine/finished", "machine/seen")
    }

    @Test fun markAllAsReadTakesItOutAtOnce() = runBlocking {
        model = model()
        awaitSection("state/just_finished", "machine/finished")
        val unseen = model!!.page.value.spaces.flatMap { it.members }.filter { it.unseen }
        assertEquals(listOf("machine/finished"), unseen.map { it.id })
        model!!.markRead(unseen, clear = true)
        // Before the hub answers: the hub's list still has the flag.
        awaitIdle("machine/finished", "machine/seen")
        assertTrue(sessions.value.first().work!!.justFinished)
    }

    @Test fun fiveRowsStayInUnreadAfterTheyAreReadUntilRefresh(): Unit = runBlocking {
        val unread = (1..5).map { session("unread-$it", "unread") }
        val keys = unread.map { it.key.path }
        sessions.value += unread
        model = model()
        awaitSection("state/unread", *keys.toTypedArray())

        for (session in unread) {
            visible.value = session.key
            preferences.markSeen(listOf(session))
            visible.value = null
            withTimeout(10_000) {
                model!!.page.first { page -> page.list.groups.singleOrNull { it.id == "state/unread" }?.heldSeen?.contains(session.key.path) == true }
            }
        }
        val section = model!!.page.value.list.groups.single { it.id == "state/unread" }
        assertEquals(keys, section.heldSeen)
        assertEquals(0, section.sectionCount)

        model!!.refresh()
        withTimeout(10_000) {
            model!!.page.first { page ->
                page.list.groups.none { it.id == "state/unread" } &&
                    page.list.groups.singleOrNull { it.id == "state/idle" }?.rows?.map { it.id }?.containsAll(keys) == true
            }
        }
    }

    @Test fun aNewTurnDropsTheOldUnreadPlaceEvenAfterItFinishes(): Unit = runBlocking {
        val unread = session("new-turn", "unread")
        sessions.value += unread
        model = model()
        model!!.filter(AgentQuickFilter.Unread)
        awaitSection("state/unread", unread.key.path)
        model!!.opened(row(unread.key.path))
        preferences.markSeen(listOf(unread))
        withTimeout(10_000) { model!!.page.first { page -> page.list.groups.any { unread.key.path in it.heldSeen } } }

        for (next in listOf("working", "idle")) {
            sessions.value = sessions.value.map { if (it.key == unread.key) it.copy(state = next) else it }
            withTimeout(10_000) {
                model!!.page.first { page -> page.sessions.any { it.key == unread.key && it.state == next } }
            }
            assertFalse(model!!.page.value.list.groups.any { group -> group.rows.any { it.id == unread.key.path } })
        }
    }

    @Test fun bulkReadMovesEveryRowWhileTheFirstReceiptIsPending(): Unit = runBlocking {
        val unread = listOf(session("bulk-a", "unread"), session("bulk-b", "unread"))
        sessions.value += unread
        model = model()
        awaitSection("state/unread", *unread.map { it.key.path }.toTypedArray())
        val writeStarted = CompletableDeferred<Unit>()
        val releaseWrite = CompletableDeferred<Unit>()
        beforeSeen = {
            writeStarted.complete(Unit)
            releaseWrite.await()
        }
        try {
            model!!.markRead(unread.map { row(it.key.path) }, clear = true)
            withTimeout(10_000) {
                writeStarted.await()
                model!!.page.first { page -> page.list.groups.none { it.id == "state/unread" } }
            }
            assertTrue(hub.value.seen.isEmpty())
        } finally {
            releaseWrite.complete(Unit)
        }
    }

    @Test fun clearerLeavesOnlyUnseenRowsInTheSelectedSection() = runBlocking {
        val unread = listOf(session("unread-one", "unread"), session("unread-two", "unread"))
        sessions.value += unread
        model = model()
        awaitSection("state/unread", *unread.map { it.key.path }.toTypedArray())
        preferences.markSeen(listOf(unread.first()))
        val heldSection = withTimeout(10_000) {
            model!!.page.first { it.list.groups.any { group -> group.heldSeen.contains(unread.first().key.path) } }
        }.list.groups.single { it.id == "state/unread" }
        model!!.clearSeen(heldSection.heldSeen.toSet())
        awaitSection("state/unread", unread.last().key.path)
    }

    @Test fun markAllCountsUnreadAndReleasesHeldRowsFromTheTarget() {
        val held = AgentSession(session("held", "idle"), seenPlace = persona.core.domain.sort.AgentSeenPlace.Unread)
        val unread = AgentSession(session("unread", "unread"))
        val idle = AgentSession(session("idle", "idle"))

        val markAll = markAllSeenBatch(listOf(held, unread, idle))

        assertEquals(listOf("machine/unread"), markAll.unread.map(AgentSession::id))
        assertEquals(listOf("machine/held", "machine/unread", "machine/idle"), markAll.release.map(AgentSession::id))
    }

    @Test fun markAllReleasesAHeldRowAndTheUnreadRowTogether(): Unit = runBlocking {
        val held = session("bulk-held", "unread")
        val unread = session("bulk-unread", "unread")
        sessions.value += listOf(held, unread)
        model = model()
        awaitSection("state/unread", held.key.path, unread.key.path)

        preferences.markSeen(listOf(held))
        val section = withTimeout(10_000) {
            model!!.page.first { page ->
                page.list.groups.singleOrNull { it.id == "state/unread" }?.heldSeen == listOf(held.key.path)
            }
        }.list.groups.single { it.id == "state/unread" }
        val markAll = markAllSeenBatch(section.rows)

        model!!.markRead(markAll.unread, clear = true, release = markAll.release)

        withTimeout(10_000) { model!!.page.first { page -> page.list.groups.none { it.id == "state/unread" } } }
    }

    @Test fun aSeenChatHoldsNothingAndLeavesJustFinishedAsItIs() = runBlocking {
        model = model()
        visible.value = SessionKey("machine", "seen")
        awaitSection("state/just_finished", "machine/finished")
        awaitIdle("machine/seen", finishedLeft = false)
    }

    private var beforeSeen: suspend () -> Unit = {}
    private val preferences = AgentsPreferences(object : androidx.datastore.core.DataStore<androidx.datastore.preferences.core.Preferences> {
        override val data = MutableStateFlow(androidx.datastore.preferences.core.emptyPreferences())
        override suspend fun updateData(transform: suspend (androidx.datastore.preferences.core.Preferences) -> androidx.datastore.preferences.core.Preferences) =
            transform(data.value).also { data.value = it }
    }, object : persona.core.contracts.api.HubPreferences {
        override val preferences = hub
        override suspend fun refresh() = Unit
        override suspend fun togglePin(path: String, space: Boolean) = Unit
        override suspend fun toggleMute(path: String, space: Boolean) = Unit
        // As HubPreferencesRepository: this phone's seen shows before the hub records it.
        override suspend fun markSeen(session: SessionKey, version: String) {
            beforeSeen()
            hub.value = hub.value.copy(seen = hub.value.seen + (session.path to version))
        }
    }).also { runBlocking {
        it.grouping(AgentGrouping.State)
        it.range("", AgentRange.All)
    } }

    private fun session(id: String, state: String) = Session("machine", id, title = "Example $id", state = state,
        cwd = "/home/user/project", spaceId = "$id-space", spaceName = "Project $id", updatedAt = "2026-01-01T00:00:00Z")

    private fun model(): AgentsViewModel = AgentsViewModel(object : SessionsRepository {
        override val sessions = this@AgentsJustFinishedTest.sessions
        override val machines = MutableStateFlow(listOf(Machine("machine", "Example computer", online = true)))
        override suspend fun refresh() = Unit
    }, object : InteractionCenter {
        override val pending = MutableStateFlow<List<Interaction>>(emptyList())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }, object : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }, object : MessageSender {
        override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }, preferences, visibleChat = visible, heldChat = held, finishedChat = finished).also { model = it }

    private fun row(id: String): AgentSession = model!!.page.value.list.groups.flatMap { it.rows }.single { it.id == id }

    private suspend fun awaitSection(section: String, vararg ids: String) {
        val expected = ids.toList()
        val matched = withTimeoutOrNull(10_000) {
            model!!.page.first { page -> page.list.groups.singleOrNull { it.id == section }?.visibleRows?.map { it.id } == expected }
        }
        assertTrue("$section expected $expected; actual " + describe(), matched != null)
    }

    private suspend fun awaitIdle(vararg ids: String, finishedLeft: Boolean = true) {
        val matched = withTimeoutOrNull(10_000) {
            model!!.page.first { page ->
                val group = page.list.groups.singleOrNull { it.id == "state/idle" }
                group?.visibleRows?.isEmpty() == true && group.rows.map { it.id } == ids.toList() &&
                    (!finishedLeft || page.list.groups.none { it.id == "state/just_finished" })
            }
        }
        assertTrue("idle expected ${ids.toList()}; actual " + describe(), matched != null)
    }

    private fun describe() = model!!.page.value.list.groups.map {
        "${it.id}: rows=${it.rows.map(AgentSession::id)}, visible=${it.visibleRows.map(AgentSession::id)}"
    }
}
