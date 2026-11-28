package persona.feature.agents

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.launch
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.UiText

@OptIn(ExperimentalCoroutinesApi::class)
class HerdrNoticeTest {
    private val off = HerdrState(false, false, true)
    private val stopped = Machine("desk", "Office", online = true)

    @Test fun onlyOnlineClassicHerdrMachinesKnownToBeStoppedOfferStart() {
        assertTrue(stopped.herdrStopped(off))
        assertTrue(stopped.copy(engine = "herdr-both").herdrStopped(off))
        assertFalse(stopped.copy(online = false).herdrStopped(off))
        assertFalse(stopped.herdrStopped(off.copy(known = false)))
        assertFalse(stopped.herdrStopped(null))
        assertFalse(stopped.herdrStopped(off.copy(running = true)))
        for (engine in listOf("herdr-core", "orca", "tern", "rex", "paseo", "cindy", "t3code", "future"))
            assertFalse(engine, stopped.copy(engine = engine).herdrStopped(off))
    }

    /** The empty host set keeps `engine` herdr; the choice is "No terminal engine", so no stopped-herdr notice and no start button. */
    @Test fun aMachineWithNoTerminalEngineNeverOffersToStartHerdr() {
        val none = stopped.copy(engine = "herdr", engineHosts = emptyList())
        assertFalse(none.usesHerdr)
        assertFalse(none.herdrStopped(off))
        assertFalse(none.copy(engine = "herdr-both").herdrStopped(off))
        // A hub with no host sets (an older one) and a set that has an engine are read as before.
        assertTrue(stopped.copy(engine = "herdr", engineHosts = null).herdrStopped(off))
        assertTrue(stopped.copy(engine = "herdr", engineHosts = listOf("herdr")).herdrStopped(off))
        assertTrue(stopped.copy(engine = "herdr", engineHosts = listOf("herdr", "orca")).herdrStopped(off))
    }

    /** The notice follows the engine set, not its default: herdr enabled beside an Orca default still offers Start (#17037). */
    @Test fun herdrInTheSetCountsWhateverTheDefault() {
        assertTrue(stopped.copy(engine = "orca", engineHosts = listOf("herdr", "orca"), engineDefault = "orca").herdrStopped(off))
        assertFalse(stopped.copy(engine = "orca", engineHosts = listOf("herdr-core", "orca"), engineDefault = "orca").herdrStopped(off))
    }

    @Test fun choosingNoEngineHidesTheNoticeAndPickingHerdrBringsItBack() = runTest {
        val api = Api()
        val repository = Repository(stopped.copy(engine = "herdr", engineHosts = listOf("herdr")))
        val starter = HerdrStarter(api, repository, Connection(), HubGroups.None, backgroundScope) { fail(it.toString()) }
        starter.refresh()
        val machine = { repository.machines.value.single() }
        assertTrue(machine().herdrStopped(starter.states.value["desk"]))
        // No terminal engine: herdr's last answer is still kept, but it no longer shows and cannot be started.
        repository.machines.value = listOf(machine().copy(engineHosts = emptyList()))
        assertFalse(machine().herdrStopped(starter.states.value["desk"]))
        starter.start("desk"); runCurrent()
        assertTrue(api.calls.isEmpty())
        assertTrue(starter.starting.value.isEmpty())
        // Back to the engine it had: the notice is there again.
        repository.machines.value = listOf(machine().copy(engineHosts = listOf("herdr")))
        starter.refresh()
        assertTrue(machine().herdrStopped(starter.states.value["desk"]))
    }

    @Test fun herdrIsNotReadForAMachineWithNoTerminalEngine() = runTest {
        val api = Api()
        val repository = Repository(stopped.copy(engine = "herdr", engineHosts = emptyList()))
        val starter = HerdrStarter(api, repository, Connection(), HubGroups.None, backgroundScope) { fail(it.toString()) }
        starter.refresh()
        assertTrue(starter.states.value.isEmpty())
        assertTrue(api.reads.isEmpty())
    }

    private class Repository(machine: Machine) : SessionsRepository {
        override val machines = MutableStateFlow(listOf(machine))
        override val sessions = MutableStateFlow<List<Session>>(emptyList())
        override suspend fun refresh() = Unit
        override suspend fun applyMachine(machine: Machine) { machines.value = machines.value.map { if (it.id == machine.id) machine else it } }
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: kotlinx.serialization.json.JsonObject) = true
    }

    private class Api : MachineManagement {
        val calls = mutableListOf<String>()
        val reads = mutableListOf<String>()
        val answer = CompletableDeferred<HerdrState>()
        var status: suspend () -> HerdrState = { HerdrState(false, false, true) }
        override suspend fun herdr(machineId: String): HerdrState { reads += machineId; return status() }
        override suspend fun startHerdr(machineId: String): HerdrState { calls += machineId; return answer.await() }
        override suspend fun launchers(machineId: String) = error("unused")
        override suspend fun launcherQuota(machineId: String, name: String, refresh: Boolean) = error("unused")
        override suspend fun reconnect(machineId: String) = error("unused")
        override suspend fun deploy(machineId: String) = error("unused")
        override suspend fun setEngine(machineId: String, engine: String) = error("unused")
        override suspend fun claudeSettings(machineId: String) = error("unused")
        override suspend fun setClaudeSetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) = error("unused")
        override suspend fun codexSettings(machineId: String) = error("unused")
        override suspend fun setCodexSetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) = error("unused")
    }

    @Test fun startIsExplicitDeduplicatedAndUsesTheConfirmedResult() = runTest {
        val api = Api()
        val repository = Repository(stopped)
        val notices = mutableListOf<AgentText>()
        val starter = HerdrStarter(api, repository, Connection(), HubGroups.None, backgroundScope, notices::add)
        runCurrent()
        assertTrue(api.calls.isEmpty())
        starter.refresh()
        starter.start("desk"); starter.start("desk")
        assertEquals(setOf("desk"), starter.starting.value)
        runCurrent()
        assertEquals(listOf("desk"), api.calls)
        repository.machines.value = listOf(stopped.copy(name = "Renamed", nodeVersion = "new"))
        api.answer.complete(HerdrState(false, true, true))
        runCurrent()
        assertTrue(starter.starting.value.isEmpty())
        assertTrue(starter.states.value.getValue("desk").running)
        assertEquals("new", repository.machines.value.single().nodeVersion)
        assertEquals(listOf(AgentText(R.string.agents_herdr_started, "Renamed")), notices)
        starter.start("desk"); runCurrent()
        assertEquals(1, api.calls.size)
    }

    @Test fun failureKeepsTheNoticeAndDoesNotRetryOnItsOwn() = runTest {
        val api = Api()
        val repository = Repository(stopped)
        val notices = mutableListOf<AgentText>()
        val starter = HerdrStarter(api, repository, Connection(), HubGroups.None, backgroundScope, notices::add)
        starter.refresh()
        starter.start("desk"); runCurrent()
        api.answer.completeExceptionally(IllegalStateException("engine_missing")); runCurrent()
        assertTrue(stopped.herdrStopped(starter.states.value["desk"]))
        assertTrue(starter.starting.value.isEmpty())
        assertEquals(1, api.calls.size)
        assertEquals(R.string.agents_herdr_failed, notices.single().id)
        assertEquals(UiText.Plain("engine_missing"), notices.single().args.single())
    }

    @Test fun disconnectedUnknownAndGoneMachinesCannotStart() = runTest {
        val api = Api()
        val repository = Repository(stopped)
        val connection = Connection()
        val starter = HerdrStarter(api, repository, connection, HubGroups.None, backgroundScope) { fail(it.toString()) }
        connection.state.value = ConnectionState.WaitingForNetwork
        starter.start("desk")
        connection.state.value = ConnectionState.Online
        api.status = { off.copy(known = false) }; starter.refresh()
        starter.start("desk"); starter.start("gone"); runCurrent()
        assertTrue(api.calls.isEmpty())
        assertTrue(starter.starting.value.isEmpty())
    }

    private val work = stopped.copy(id = "mac~g0a1b2c3d")

    private class Groups(home: ConnectionState, work: ConnectionState) : HubGroups by HubGroups.None {
        override val groups = MutableStateFlow(listOf(
            HubGroupInfo("g11111111", "Home", null, true, home, emptyList()),
            HubGroupInfo("g0a1b2c3d", "Work", "g0a1b2c3d", false, work, emptyList())))
    }

    /** With several hub groups a computer goes through its own group's connection, not the selected one's (#5726). */
    @Test fun anOnlineGroupsComputerIsReadAndStartedWhileTheSelectedOneIsDown() = runTest {
        val api = Api()
        val connection = Connection().apply { state.value = ConnectionState.Connecting }
        val starter = HerdrStarter(api, Repository(work), connection, Groups(ConnectionState.Connecting, ConnectionState.Online), backgroundScope) { }
        starter.refresh()
        assertEquals(listOf(work.id), api.reads)
        assertTrue(work.herdrStopped(starter.states.value[work.id]))
        starter.start(work.id); runCurrent()
        assertEquals(listOf(work.id), api.calls)
    }

    @Test fun anOfflineGroupsComputerIsNeitherReadNorStartedWhileTheSelectedOneIsOnline() = runTest {
        val api = Api()
        val groups = Groups(ConnectionState.Online, ConnectionState.Online)
        val starter = HerdrStarter(api, Repository(work), Connection(), groups, backgroundScope) { fail(it.toString()) }
        starter.refresh()
        groups.groups.value = groups.groups.value.map { if (it.selected) it else it.copy(state = ConnectionState.Connecting) }
        starter.start(work.id); starter.refresh(); runCurrent()
        assertEquals(listOf(work.id), api.reads)
        assertTrue(api.calls.isEmpty())
    }

    @Test fun anUnconfirmedReplyDoesNotClaimSuccess() = runTest {
        val api = Api()
        val repository = Repository(stopped)
        val notices = mutableListOf<AgentText>()
        val starter = HerdrStarter(api, repository, Connection(), HubGroups.None, backgroundScope, notices::add)
        starter.refresh()
        starter.start("desk"); runCurrent()
        api.answer.complete(HerdrState(true, false, true)); runCurrent()
        assertTrue(stopped.herdrStopped(starter.states.value["desk"]))
        assertEquals(listOf(AgentText(R.string.agents_herdr_unconfirmed)), notices)
    }

    @Test fun cancellationClearsPendingWithoutClaimingAResult() = runTest {
        val api = Api()
        val repository = Repository(stopped)
        val notices = mutableListOf<AgentText>()
        val starter = HerdrStarter(api, repository, Connection(), HubGroups.None, backgroundScope, notices::add)
        starter.refresh()
        starter.start("desk"); runCurrent()
        api.answer.cancel(); runCurrent()
        assertTrue(starter.starting.value.isEmpty())
        assertTrue(stopped.herdrStopped(starter.states.value["desk"]))
        assertTrue(notices.isEmpty())
    }

    @Test fun anOldStatusReadCannotUndoAConfirmedStart() = runTest {
        val api = Api()
        val repository = Repository(stopped)
        val starter = HerdrStarter(api, repository, Connection(), HubGroups.None, backgroundScope) { }
        starter.refresh()
        val stale = CompletableDeferred<HerdrState>()
        api.status = { stale.await() }
        val refreshing = backgroundScope.launch { starter.refresh() }
        runCurrent()
        starter.start("desk"); runCurrent()
        api.answer.complete(HerdrState(false, true, true)); runCurrent()
        stale.complete(off); refreshing.join()
        assertTrue(starter.states.value.getValue("desk").running)
    }

    @Test fun anUnavailableStatusIsUnknownAndCannotOfferStart() = runTest {
        val api = Api()
        val starter = HerdrStarter(api, Repository(stopped), Connection(), HubGroups.None, backgroundScope) { fail(it.toString()) }
        starter.refresh()
        assertTrue(starter.states.value.getValue("desk").known)
        api.status = { throw IllegalStateException("old hub") }
        starter.refresh()
        assertTrue(starter.states.value.isEmpty())
        starter.start("desk"); runCurrent()
        assertTrue(api.calls.isEmpty())
    }

    @Test fun aFailedStartStaysOnTheRowUntilTheNextStartOrUntilHerdrRuns() = runTest {
        val api = Api()
        val repository = Repository(stopped)
        val starter = HerdrStarter(api, repository, Connection(), HubGroups.None, backgroundScope) { }
        starter.refresh()
        starter.start("desk"); runCurrent()
        api.answer.completeExceptionally(IllegalStateException("engine_missing")); runCurrent()
        val failure = starter.failures.value.getValue("desk")
        assertEquals(AgentText(R.string.agents_text_raw, UiText.Plain("engine_missing")), failure.text)
        assertEquals("engine_missing", failure.detail)
        api.status = { HerdrState(false, true, true) }
        starter.refresh()
        assertTrue(starter.failures.value.isEmpty())
    }

    @Test fun anUnconfirmedReplyIsKeptAsItsOwnFailure() = runTest {
        val api = Api()
        val starter = HerdrStarter(api, Repository(stopped), Connection(), HubGroups.None, backgroundScope) { }
        starter.refresh()
        starter.start("desk"); runCurrent()
        api.answer.complete(HerdrState(true, false, true)); runCurrent()
        assertEquals(AgentText(R.string.agents_herdr_why_no_answer), starter.failures.value.getValue("desk").text)
    }

    @Test fun theNodesKnownFailuresAreSaidInWords() {
        assertEquals(AgentText(R.string.agents_herdr_why_exited_code, "2"),
            HerdrFailure.says("herdr server stopped right after starting: exit status 2"))
        assertEquals(AgentText(R.string.agents_herdr_why_killed, "killed"),
            HerdrFailure.says("herdr server stopped right after starting: signal: killed"))
        assertEquals(AgentText(R.string.agents_herdr_why_not_installed), HerdrFailure.says("herdr is not installed"))
        assertEquals(AgentText(R.string.agents_herdr_why_silent_for, "15"), HerdrFailure.says("herdr did not answer on sock within 15s"))
        assertEquals(AgentText(R.string.agents_herdr_why_old_node), HerdrFailure.says("unknown method herdr.server.start"))
        assertEquals(AgentText(R.string.agents_herdr_why_timeout), HerdrFailure.says("context deadline exceeded"))
        assertEquals(AgentText(R.string.agents_herdr_why_timeout), HerdrFailure.says("Request Timed Out"))
        assertEquals(AgentText.raw("something else"), HerdrFailure.says(" something else "))
    }

    @Test fun theNodesOwnWordsAreReadUnderALocalizedSummary() {
        fun failure(original: String) = persona.core.hub.HubFailure(persona.core.hub.HubError.Server, "示例摘要", 502, "internal", original = original)
        assertEquals(AgentText(R.string.agents_herdr_why_old_node), HerdrFailure.of(failure("unknown method herdr.server.start")).text)
        assertEquals("words it does not know stay the hub's", AgentText.raw("示例摘要"), HerdrFailure.of(failure("something else")).text)
    }
}
