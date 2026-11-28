package persona.feature.inbox

import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.IOException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxActionsTransport
import persona.core.contracts.api.InboxItemContext
import persona.core.inbox.*

@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class InboxHandOffBarModelTest {
    private val resources get() = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources

    private fun model(repository: HandOffBarTestRepository, scope: CoroutineScope) =
        InboxHandOffBarModel(repository.record.item.id, repository, scope, resources)

    @Test fun allThreeChipsWaitThenFireExactlyOnce() = runTest {
        for (preset in handoffPresets(resources)) {
            val repository = HandOffBarTestRepository()
            val model = model(repository, backgroundScope)
            model.queue(preset, typed = false)
            runCurrent()
            advanceTimeBy(4_999)
            runCurrent()
            assertTrue(repository.handOffRequests.isEmpty())
            advanceTimeBy(1)
            runCurrent()
            assertEquals(1, repository.handOffRequests.size)
            assertTrue(repository.handOffRequests.single().text.endsWith(preset))
        }
    }

    @Test fun undoAt4999RestoresTypedTextAndNeverCallsTheHub() = runTest {
        val repository = HandOffBarTestRepository()
        val model = model(repository, backgroundScope)
        model.updateInput("Typed ask")
        model.queue(model.state.value.input, typed = true)
        assertEquals("", model.state.value.input)
        runCurrent()
        advanceTimeBy(4_999)
        runCurrent()
        assertEquals("Typed ask", model.undo())
        advanceTimeBy(10_000)
        runCurrent()
        assertTrue(repository.handOffRequests.isEmpty())
        assertEquals("Typed ask", model.state.value.input)
        assertEquals(HandOffBarPhase.Idle, model.state.value.phase)
        assertNull(model.undo())
    }

    @Test fun sendingAndSentNeverOfferUndo() = runTest {
        val repository = HandOffBarTestRepository()
        repository.handOffGate = CompletableDeferred()
        val model = model(repository, backgroundScope)
        model.queue("Ask", typed = false)
        advanceTimeBy(5_000)
        runCurrent()
        assertTrue(model.state.value.phase is HandOffBarPhase.Sending)
        assertNull(model.undo())
        repository.handOffGate?.complete(Unit)
        runCurrent()
        assertTrue(model.state.value.phase is HandOffBarPhase.Sent)
        assertNull(model.undo())
    }

    @Test fun queuedViewSendsImmediatelyAndSentViewOnlyReadsDestination() = runTest {
        val repository = HandOffBarTestRepository()
        val model = model(repository, backgroundScope)
        model.queue("Ask", typed = false)
        model.viewQueued()
        runCurrent()
        val sent = model.state.value.phase as HandOffBarPhase.Sent
        assertTrue(sent.openRequested)
        assertEquals(1, repository.handOffRequests.size)
        val destination = model.openSent()
        assertEquals(repository.result, destination?.session)
        model.openHandled()
        assertEquals(destination, model.openSent())
        assertFalse((model.state.value.phase as HandOffBarPhase.Sent).openRequested)
        assertEquals(1, repository.handOffRequests.size)
    }

    @Test fun replacementAndDuplicateViewStillFireOneRequestPerIntent() = runTest {
        val repository = HandOffBarTestRepository()
        val model = model(repository, backgroundScope)
        model.queue("First", typed = false)
        model.queue("Second", typed = false)
        model.viewQueued()
        model.viewQueued()
        runCurrent()
        assertEquals(1, repository.handOffRequests.size)
        assertTrue(repository.handOffRequests.single().text.endsWith("Second"))
        model.queue("Third", typed = false)
        advanceTimeBy(INBOX_HAND_OFF_UNDO_MILLIS)
        runCurrent()
        assertEquals(2, repository.handOffRequests.size)
        assertTrue(repository.handOffRequests.last().text.endsWith("Third"))
    }

    @Test fun explicitRetryKeepsTheFirstPromptAndOperationWithoutAutomaticRetry() = runTest {
        val repository = HandOffBarTestRepository()
        repository.failure = IOException("assistant_in_doubt")
        val model = model(repository, backgroundScope)
        model.queue("Original ask", typed = false)
        advanceTimeBy(INBOX_HAND_OFF_UNDO_MILLIS)
        runCurrent()
        assertTrue(model.state.value.phase is HandOffBarPhase.Failed)
        val first = repository.handOffRequests.single()
        repository.snapshot.value = repository.snapshot.value.copy(records = listOf(
            repository.record.copy(item = repository.record.item.copy(title = "Changed title")),
        ))
        advanceTimeBy(60_000)
        runCurrent()
        assertEquals(1, repository.handOffRequests.size)
        repository.failure = null
        model.retry()
        runCurrent()
        assertEquals(2, repository.handOffRequests.size)
        val retry = repository.handOffRequests.last()
        assertEquals(first.operationId, retry.operationId)
        assertEquals(first.text, retry.text)
        assertTrue(model.state.value.phase is HandOffBarPhase.Sent)
    }

    @Test fun sourceMemoryPreselectsAndAnEndedAgentFallsBackOnce() = runTest {
        val key = "mail|sender:sender@example.com"
        val repository = HandOffBarTestRepository(agentCount = 15)
        repository.handOffMemory.value = listOf(HandOffMemoryEntry(key, "agent:machine/agent-1"))
        val model = model(repository, backgroundScope)
        model.preselect(repository.handOffMemory.value)
        assertEquals("machine/agent-1", model.state.value.target?.key)
        assertFalse(model.state.value.markDone)
        assertTrue(model.state.value.lastTime)
        assertEquals(12, model.liveTargets().size)
        assertEquals(3, model.liveTargets().take(INBOX_HAND_OFF_MENU_TARGETS).size)

        val gone = HandOffBarTestRepository()
        val goneModel = model(gone, backgroundScope)
        goneModel.preselect(listOf(HandOffMemoryEntry(key, "agent:machine/gone")))
        assertNull(goneModel.state.value.target)
        assertTrue(goneModel.state.value.lastGone)
        goneModel.queue("Ask", typed = false)
        assertFalse(goneModel.state.value.lastGone)
    }

    @Test fun markDoneStartsOffForEveryTargetAndAChoiceSurvivesTargetChanges() = runTest {
        val repository = HandOffBarTestRepository(agentCount = 2)
        val model = model(repository, backgroundScope)
        val agent = model.liveTargets().first()
        model.selectTarget(agent)
        assertFalse(model.state.value.markDone)
        model.setMarkDone(true)
        model.selectTarget(null)
        assertTrue(model.state.value.markDone)
        model.selectTarget(agent)
        assertTrue(model.state.value.markDone)

        val restored = model(HandOffBarTestRepository(agentCount = 2), backgroundScope)
        restored.restoreTarget(restored.liveTargets().first().key)
        assertFalse(restored.state.value.markDone)
    }

    @Test fun aFinishedItemOpenedFromHistoryStillHandsOff() = runTest {
        val repository = HandOffBarTestRepository(agentCount = 1)
        repository.historyRecord = repository.record
        repository.snapshot.value = repository.snapshot.value.copy(records = emptyList())
        val model = model(repository, backgroundScope)
        model.selectTarget(model.liveTargets().single())
        model.queue("Ask", typed = false)
        advanceTimeBy(INBOX_HAND_OFF_UNDO_MILLIS)
        runCurrent()
        assertEquals(1, repository.handOffRequests.size)
        assertTrue(model.state.value.phase is HandOffBarPhase.Sent)
    }

    @Test fun markDoneSuccessAndFailureNeverRepeatAnAcceptedHandOff() = runTest {
        val successRepository = HandOffBarTestRepository()
        val success = model(successRepository, backgroundScope)
        success.selectTarget(success.liveTargets().single())
        success.setMarkDone(true)
        success.queue("Ask", typed = false)
        advanceTimeBy(INBOX_HAND_OFF_UNDO_MILLIS)
        runCurrent()
        assertEquals(1, successRepository.doneCalls)
        assertNull((success.state.value.phase as HandOffBarPhase.Sent).doneError)

        val failedRepository = HandOffBarTestRepository()
        failedRepository.doneFailure = IOException("done failed")
        val failed = model(failedRepository, backgroundScope)
        failed.selectTarget(failed.liveTargets().single())
        failed.setMarkDone(true)
        failed.queue("Ask", typed = false)
        advanceTimeBy(INBOX_HAND_OFF_UNDO_MILLIS)
        runCurrent()
        assertTrue(failed.state.value.phase is HandOffBarPhase.Sent)
        assertEquals(failedRepository.doneFailure, (failed.state.value.phase as HandOffBarPhase.Sent).doneError)
        failed.retry()
        runCurrent()
        assertEquals(1, failedRepository.handOffRequests.size)
        assertEquals(1, failedRepository.doneCalls)
    }

    @Test fun pairingStoreReusesModelsAndCloseCancelsTheWait() = runTest {
        val owner = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val repository = HandOffBarTestRepository()
        val store = InboxHandOffBars(owner, resources, repository)
        val first = store.get(repository.record.item.id)
        assertSame(first, store.get(repository.record.item.id))
        first.queue("Ask", typed = false)
        owner.cancel()
        advanceTimeBy(10_000)
        runCurrent()
        assertTrue(repository.handOffRequests.isEmpty())
    }

    @Test fun delayedTargetCatalogKeepsMemoryPendingInsteadOfReportingLastGone() = runTest {
        val key = "mail|sender:sender@example.com"
        val repository = HandOffBarTestRepository(agentCount = 1)
        val loadedTargets = repository.snapshot.value.sessions
        repository.handOffMemory.value = listOf(HandOffMemoryEntry(key, "agent:machine/agent-1"))
        repository.snapshot.value = repository.snapshot.value.copy(sessions = emptyList(), targetsReady = false)
        val model = model(repository, backgroundScope)

        model.preselect(repository.handOffMemory.value)
        assertNull(model.state.value.target)
        assertFalse(model.state.value.lastGone)
        assertFalse(model.state.value.lastTime)

        repository.snapshot.value = repository.snapshot.value.copy(sessions = loadedTargets, targetsReady = true)
        model.preselect(repository.handOffMemory.value)
        assertEquals("machine/agent-1", model.state.value.target?.key)
        assertTrue(model.state.value.lastTime)
        assertFalse(model.state.value.lastGone)
    }

    @Test fun queuedWorkKeepsItsOriginalRepositoryWhenTwoGroupsShareAnItemId() = runTest {
        val oldGroup = HandOffBarTestRepository()
        val newGroup = HandOffBarTestRepository()
        val oldScope = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val newScope = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val oldStore = InboxHandOffBars(oldScope, resources, oldGroup)
        val newStore = InboxHandOffBars(newScope, resources, newGroup)
        val oldModel = oldStore.get(oldGroup.record.item.id)
        oldModel.queue("Old group ask", typed = false)

        newStore.get(newGroup.record.item.id)
        advanceTimeBy(5_000)
        runCurrent()

        assertEquals(1, oldGroup.handOffRequests.size)
        assertTrue(oldGroup.handOffRequests.single().text.endsWith("Old group ask"))
        assertTrue(newGroup.handOffRequests.isEmpty())
        assertEquals(0, newGroup.doneCalls)
        assertTrue(newGroup.handOffMemory.value.isEmpty())
    }

    @Test fun handOffAlreadyInFlightKeepsMemoryAndDoneInItsOriginalGroup() = runTest {
        val oldGroup = HandOffBarTestRepository()
        val newGroup = HandOffBarTestRepository()
        oldGroup.handOffGate = CompletableDeferred()
        val oldScope = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val newScope = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val oldStore = InboxHandOffBars(oldScope, resources, oldGroup)
        val newStore = InboxHandOffBars(newScope, resources, newGroup)
        val oldModel = oldStore.get(oldGroup.record.item.id)
        oldModel.selectTarget(oldModel.liveTargets().single())
        oldModel.setMarkDone(true)
        oldModel.queue("Old group ask", typed = false)
        oldModel.viewQueued()
        runCurrent()
        assertEquals(1, oldGroup.handOffRequests.size)

        newStore.get(newGroup.record.item.id)
        oldGroup.handOffGate?.complete(Unit)
        runCurrent()

        assertTrue(oldModel.state.value.phase is HandOffBarPhase.Sent)
        assertEquals(1, oldGroup.doneCalls)
        assertTrue(oldGroup.handOffMemory.value.isNotEmpty())
        assertTrue(newGroup.handOffRequests.isEmpty())
        assertEquals(0, newGroup.doneCalls)
        assertTrue(newGroup.handOffMemory.value.isEmpty())
    }

    @Test fun doneAlreadyInFlightCannotBeRetargetedByAGroupSwitch() = runTest {
        val oldGroup = HandOffBarTestRepository()
        val newGroup = HandOffBarTestRepository()
        oldGroup.doneGate = CompletableDeferred()
        val oldScope = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val newScope = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val oldStore = InboxHandOffBars(oldScope, resources, oldGroup)
        val newStore = InboxHandOffBars(newScope, resources, newGroup)
        val oldModel = oldStore.get(oldGroup.record.item.id)
        oldModel.selectTarget(oldModel.liveTargets().single())
        oldModel.setMarkDone(true)
        oldModel.queue("Old group ask", typed = false)
        oldModel.viewQueued()
        runCurrent()
        assertEquals(1, oldGroup.doneCalls)

        newStore.get(newGroup.record.item.id)
        oldGroup.doneGate?.complete(Unit)
        runCurrent()

        assertTrue(oldModel.state.value.phase is HandOffBarPhase.Sent)
        assertEquals(1, oldGroup.handOffRequests.size)
        assertEquals(1, oldGroup.doneCalls)
        assertTrue(newGroup.handOffRequests.isEmpty())
        assertEquals(0, newGroup.doneCalls)
        assertTrue(newGroup.handOffMemory.value.isEmpty())
    }

    @Test fun settledModelsAreBoundedWithoutEvictingQueuedWork() = runTest {
        val repository = HandOffBarTestRepository()
        val store = InboxHandOffBars(backgroundScope, resources, repository)
        val queued = store.get("active")
        queued.queue("Keep waiting", typed = false)
        val settled = store.get("settled")
        repeat(INBOX_HAND_OFF_RETAINED_MODELS + 8) { store.get("item-$it") }

        assertNotSame(settled, store.get("settled"))
        assertEquals(HandOffBarPhase.Idle, store.get("settled").state.value.phase)
        assertTrue(repository.handOffRequests.isEmpty())
        assertSame(queued, store.get("active"))
        queued.undo()
    }

    @Test fun experienceRepositoryKeepsOneBarPerItemInItsOwnerScope() = runTest {
        val owner = CoroutineScope(backgroundScope.coroutineContext + SupervisorJob(backgroundScope.coroutineContext[Job]))
        val repository = HandOffBarTestRepository()
        val storage = object : InboxStateStorage {
            override suspend fun read(): String? = null
            override suspend fun write(value: String) = Unit
        }
        val actions = object : InboxActionsTransport {
            override suspend fun collectNow() = Unit
            override suspend fun context(itemId: String) = InboxItemContext("Context")
        }
        val experience = InboxExperienceRepository(repository, storage, actions,
            MutableStateFlow(ConnectionState.Online), owner, { _, _, _ -> }, resources = resources)
        runCurrent()
        assertSame(experience.handOffBar(repository.record.item.id), experience.handOffBar(repository.record.item.id))
        owner.cancel()
    }

    @Test fun replyComposersStayPerItemAndKeepTheirInitialDraft() = runTest {
        val repository = HandOffBarTestRepository()
        val composers = ReplyComposers(repository, backgroundScope)
        val first = composers.get(ReplyTarget.Inbox("one"), "First draft")
        assertSame(first, composers.get(ReplyTarget.Inbox("one"), "Replacement must not win"))
        assertNotSame(first, composers.get(ReplyTarget.Inbox("two"), "Other draft"))
        runCurrent()
        assertEquals("First draft", first.state.value.draft.text)
    }
}
