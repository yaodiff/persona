package persona.feature.settings

import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import persona.core.contracts.api.AgentTriage
import persona.core.contracts.api.TriageSwitches
import persona.core.contracts.api.TriageUnsupported
import persona.core.hub.HubError
import persona.core.hub.HubFailure

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class TriageSettingsStateTest {
    private val res = RuntimeEnvironment.getApplication().resources

    private class FakeTriage(var switches: TriageSwitches = TriageSwitches()) : AgentTriage {
        var readFailure: Exception? = null
        var saveFailure: Exception? = null
        var reads = 0
        override suspend fun switches(): TriageSwitches { reads++; readFailure?.let { throw it }; return switches }
        override suspend fun put(value: TriageSwitches): TriageSwitches { saveFailure?.let { throw it }; switches = value; return value }
    }

    @Test fun aFailedReadSaysWhyAndReadingAgainBringsTheSwitches() = runBlocking {
        val api = FakeTriage(TriageSwitches(enabled = true)).apply { readFailure = HubFailure(HubError.Timeout) }
        val model = TriageSettingsModel(api)
        model.load()
        val failed = model.state.value
        assertNull(failed.switches)
        assertFalse(failed.loading)
        assertFalse(failed.unsupported)
        assertEquals("读不到 Hub 上的设置：连不上 hub", failed.readError?.resolve(res))

        api.readFailure = null
        model.load()
        val read = model.state.value
        assertEquals(2, api.reads)
        assertEquals(TriageSwitches(enabled = true), read.switches)
        assertNull(read.readError)
    }

    @Test fun theCloseoutSwitchIsSavedAndGoesBackWhenTheHubRefusesIt() = runBlocking {
        val api = FakeTriage(TriageSwitches(enabled = true, closeout = false))
        val model = TriageSettingsModel(api)
        model.load()
        model.change(model.state.value.switches!!.copy(closeout = true))
        assertEquals(true, api.switches.closeout)
        assertEquals(true, model.state.value.switches?.closeout)
        assertNull(model.state.value.saveError)

        api.saveFailure = HubFailure(HubError.Timeout)
        model.change(model.state.value.switches!!.copy(closeout = false))
        assertEquals(true, model.state.value.switches?.closeout)
        assertEquals("没改成：连不上 hub", model.state.value.saveError?.resolve(res))
        assertFalse(model.state.value.busy)
    }

    @Test fun theCloseoutAskSwitchIsSavedWithoutChangingTheOtherSwitches() = runBlocking {
        val before = TriageSwitches(enabled = true, autoContinue = true, wake = true, decide = true,
            replyReminder = false, closeout = true, closeoutAsk = false)
        val after = before.copy(closeoutAsk = true)
        val api = FakeTriage(before)
        val model = TriageSettingsModel(api)
        model.load()
        model.change(after)
        assertEquals(after, api.switches)
        assertEquals(after, model.state.value.switches)
        assertFalse(model.state.value.busy)

        api.saveFailure = HubFailure(HubError.Timeout)
        model.change(after.copy(closeoutAsk = false))
        assertEquals(after, model.state.value.switches)
        assertEquals("没改成：连不上 hub", model.state.value.saveError?.resolve(res))
        assertFalse(model.state.value.busy)
    }

    @Test fun anOlderHubHasNoCloseoutSwitch() = runBlocking {
        val model = TriageSettingsModel(FakeTriage(TriageSwitches(enabled = true)))
        model.load()
        assertNull(model.state.value.switches?.closeout)
        assertNull(model.state.value.switches?.closeoutAsk)
    }

    @Test fun aHubWithoutTriageIsNotOfferedARetry() = runBlocking {
        val model = TriageSettingsModel(FakeTriage().apply { readFailure = TriageUnsupported() })
        model.load()
        assertTrue(model.state.value.unsupported)
        assertNotNull(model.state.value.readError)
        assertNull(model.state.value.switches)
    }

    @Test fun aSecondChangeWaitsForTheFirst() = runBlocking {
        val gate = CompletableDeferred<Unit>()
        val api = object : AgentTriage {
            val saved = mutableListOf<TriageSwitches>()
            override suspend fun switches() = TriageSwitches()
            override suspend fun put(value: TriageSwitches): TriageSwitches { gate.await(); saved += value; return value }
        }
        val model = TriageSettingsModel(api)
        model.load()
        val first = launch(start = CoroutineStart.UNDISPATCHED) { model.change(TriageSwitches(enabled = true)) }
        assertTrue(model.state.value.busy)
        model.change(TriageSwitches(wake = true))
        gate.complete(Unit)
        first.join()
        assertEquals(listOf(TriageSwitches(enabled = true)), api.saved)
        assertFalse(model.state.value.busy)
    }

    @Test fun aRefusedChangeGoesBackAndSaysWhy() = runBlocking {
        val api = FakeTriage(TriageSwitches(enabled = false))
        val model = TriageSettingsModel(api)
        model.load()
        api.saveFailure = HubFailure(HubError.Server, "数据库没连上", 500)
        model.change(TriageSwitches(enabled = true))
        val s = model.state.value
        assertEquals(TriageSwitches(enabled = false), s.switches)
        assertFalse(s.busy)
        assertEquals("没改成：数据库没连上", s.saveError?.resolve(res))

        api.saveFailure = null
        model.change(TriageSwitches(enabled = true))
        assertEquals(TriageSwitches(enabled = true), model.state.value.switches)
        assertNull(model.state.value.saveError)
    }

    @Test fun nothingChangesBeforeTheSwitchesAreRead() = runBlocking {
        val api = FakeTriage().apply { readFailure = HubFailure(HubError.NoNetwork) }
        val model = TriageSettingsModel(api)
        model.load()
        model.change(TriageSwitches(enabled = true))
        assertNull(model.state.value.switches)
        assertEquals(TriageSwitches(), api.switches)
    }
}
