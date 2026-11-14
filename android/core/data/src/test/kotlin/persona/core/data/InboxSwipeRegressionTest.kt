package persona.core.data

import java.io.IOException
import java.nio.file.Files
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*

/** Controlled transport timings, not device performance measurements. */
@OptIn(ExperimentalCoroutinesApi::class)
class InboxSwipeRegressionTest {
    private class Transport(val now: () -> Long, override val scope: String = "synthetic/generation") : InboxAssistantTransport {
        var items = listOf(itemRow("a"), itemRow("b"), itemRow("c"))
        var lastWriteAt = 0L
        var lists = 0
        var writes = 0
        var listDelay = 0L
        var writeDelay = 1_000L
        var onWrite: suspend () -> Unit = {}
        var failList = false
        var failWrite = false
        var conflict = false
        val routes = mutableListOf<String?>()
        override suspend fun inbox(): JsonObject {
            lists++
            val captured = items
            delay(listDelay)
            if (failList) throw IOException("Synthetic list failure")
            return buildJsonObject { put("items", JsonArray(captured)) }
        }
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): JsonObject {
            writes++
            routes += currentCoroutineContext()[RequestManager]?.id
            delay(writeDelay)
            onWrite()
            lastWriteAt = now()
            if (failWrite) throw IOException("Synthetic write failure")
            val changed = items.map { target -> itemRow(target.id, done = action != InboxBulkAction.Reopen) }
            if (conflict) {
                this.items = this.items.map { old ->
                    if (items.any { it.id == old["id"]?.jsonPrimitive?.content }) itemRow(old.getValue("id").jsonPrimitive.content, version = "v2") else old
                }
                return buildJsonObject { put("skipped", JsonArray(items.map { target -> buildJsonObject { put("id", target.id); put("reason", "changed") } })) }
            }
            this.items = this.items.map { old -> changed.firstOrNull { it["id"] == old["id"] } ?: old }
            return buildJsonObject { put("changed", JsonArray(changed)) }
        }
        override suspend fun setDone(itemId: String, done: Boolean) = error("Versioned bulk expected")
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = error("No reply")
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = JsonObject(emptyMap())
        override suspend fun assistant() = JsonObject(emptyMap())
        override suspend fun createConversation(main: Boolean) = JsonObject(emptyMap())
        override suspend fun pins() = JsonObject(emptyMap())
        override suspend fun mutes() = JsonObject(emptyMap())
        override suspend fun media(ref: String) = error("No media")
    }
    companion object {
        private fun itemRow(id: String, done: Boolean = false, version: String = "v1", manager: String? = null) = buildJsonObject {
            put("id", id); put("version", version); put("source", "mail"); put("kind", "mail"); put("title", id)
            if (done) put("done_at", "2026-11-09T00:00:00Z")
            manager?.let { put("manager", it) }
        }
    }
    private class Fixture(val test: TestScope) {
        val owner = CoroutineScope(SupervisorJob() + StandardTestDispatcher(test.testScheduler))
        val directory = Files.createTempDirectory("inbox-swipe-test").toFile()
        val transport = Transport({ test.testScheduler.currentTime })
        val pairing = MutableStateFlow<InboxAssistantTransport?>(transport)
        val cache = ListSnapshotStore(directory.resolve("snapshots"))
        fun repository() = RealInboxRepository(
            object : InboxAssistantConnection { override val current = pairing },
            InboxReplyJournal(directory, transport.scope),
            object : SessionsRepository {
                override val machines = MutableStateFlow<List<Machine>>(emptyList())
                override val sessions = MutableStateFlow<List<Session>>(emptyList())
                override suspend fun refresh() = Unit
            },
            object : InteractionCenter {
                override val pending = MutableStateFlow<List<Interaction>>(emptyList())
                override suspend fun refresh() = Unit
                override suspend fun resolve(id: String, resolution: Resolution) = Unit
            }, owner, snapshots = cache,
        ) { _, _ -> error("No download") }
        suspend fun start(): RealInboxRepository {
            val repository = repository()
            test.runCurrent()
            repository.refresh()
            return repository
        }
        suspend fun stop() {
            owner.coroutineContext[Job]!!.cancelAndJoin()
            directory.deleteRecursively()
        }
    }
    @Test fun consecutiveSlowWritesAndRefreshMeasurement() = runTest {
        val f = Fixture(this)
        try {
            val repository = f.start()
            val start = currentTime
            f.transport.listDelay = 5_000
            val refreshing = async { repository.refresh() }
            runCurrent()
            val writes = listOf("a", "b", "c").map { id -> async { repository.bulk(listOf(InboxDoneTarget(id, "v1")), InboxBulkAction.Done) } }
            runCurrent()
            val immediate = repository.snapshot.value.records.count { it.item.isDone }
            val started = f.transport.writes
            writes.awaitAll()
            println("SWIPE_METRIC list_delay_ms=5000 write_delay_ms=1000 count=3 immediate_done=$immediate requests_started=$started writes=${f.transport.writes} lists=${f.transport.lists - 1} network_writes_complete_ms=${f.transport.lastWriteAt - start}")
            refreshing.await()
            assertEquals(3, immediate)
            assertEquals(3, started)
            assertEquals(1_000L, f.transport.lastWriteAt - start)
            assertEquals(3, f.transport.writes)
            assertEquals(2, f.transport.lists)
            assertTrue(repository.snapshot.value.records.all { it.item.isDone })
        } finally { f.stop() }
    }
    @Test fun conflictDoesNotWaitForFailingFullRefreshMeasurement() = runTest {
        val f = Fixture(this)
        try {
            val repository = f.start()
            f.transport.conflict = true
            f.transport.listDelay = 5_000
            f.transport.failList = true
            val start = currentTime
            repository.bulk(listOf(InboxDoneTarget("a", "v1")), InboxBulkAction.Done)
            println("CONFLICT_METRIC write_delay_ms=1000 list_delay_ms=5000 result_ms=${currentTime - start} lists=${f.transport.lists - 1}")
            assertEquals(1_000L, currentTime - start)
            assertFalse(repository.snapshot.value.records.first { it.item.id == "a" }.item.isDone)
            withTimeout(10_000) { repository.snapshot.first { it.error != null } }
            assertEquals(2, f.transport.lists)
            f.transport.failList = false
            repository.refresh()
            assertNull(repository.snapshot.value.error)
            assertEquals("v2", repository.snapshot.value.records.first { it.item.id == "a" }.item.version)
        } finally { f.stop() }
    }
    @Test fun acceptedStateSurvivesRestartWithoutAnotherSuccessfulFetch() = runTest {
        val f = Fixture(this)
        try {
            val repository = f.start()
            repository.bulk(listOf(InboxDoneTarget("a", "v1")), InboxBulkAction.Done)
            f.transport.failList = true
            val restored = f.repository()
            val records = withContext(Dispatchers.Default) { withTimeout(5_000) { restored.snapshot.first { it.records.isNotEmpty() } } }.records
            assertTrue(records.first { it.item.id == "a" }.item.isDone)
            assertFalse(records.first { it.item.id == "b" }.item.isDone)
        } finally { f.stop() }
    }

    @Test fun newMessageSeenDuringWriteWinsOverBothResponseAndRollback() = runTest {
        for (failure in listOf(false, true)) {
            val f = Fixture(this)
            try {
                val repository = f.start()
                val release = CompletableDeferred<Unit>()
                f.transport.writeDelay = 0
                f.transport.onWrite = { release.await() }
                f.transport.failWrite = failure
                val write = async { runCatching { repository.bulk(listOf(InboxDoneTarget("a", "v1")), InboxBulkAction.Done) } }
                runCurrent()
                assertTrue(repository.snapshot.value.records.first { it.item.id == "a" }.item.isDone)
                f.transport.items = listOf(itemRow("a", version = "v2"), itemRow("b"), itemRow("c"))
                repository.refresh()
                assertFalse(repository.snapshot.value.records.first { it.item.id == "a" }.item.isDone)
                release.complete(Unit)
                assertEquals(failure, write.await().isFailure)
                val latest = repository.snapshot.value.records.first { it.item.id == "a" }.item
                assertEquals("v2", latest.version)
                assertFalse(latest.isDone)
            } finally {
                f.stop()
            }
        }
    }

    @Test fun duplicateHubIdsUseSeparateManagerRequestsAndDurableEntries() = runTest {
        val f = Fixture(this)
        try {
            AssistantManagerChoice.choose("synthetic", null, "default")
            f.transport.items = listOf(itemRow("same", manager = "default"), itemRow("same", manager = "other"))
            val repository = f.start()
            val other = repository.snapshot.value.records.single { it.manager == "other" }
            repository.bulk(listOf(InboxDoneTarget(other.item.id, "v1")), InboxBulkAction.Done)
            assertEquals(listOf("other"), f.transport.routes)
            assertFalse(repository.snapshot.value.records.single { it.manager == "default" }.item.isDone)
            assertTrue(repository.snapshot.value.records.single { it.manager == "other" }.item.isDone)
            f.transport.failList = true
            val restored = f.repository()
            val records = withContext(Dispatchers.Default) { withTimeout(5_000) { restored.snapshot.first { it.records.size == 2 } } }.records
            assertFalse(records.single { it.manager == "default" }.item.isDone)
            assertTrue(records.single { it.manager == "other" }.item.isDone)
        } finally { AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems(); f.stop() }
    }

    @Test fun anOldPairingsWriteCannotChangeTheNewPairing() = runTest {
        val f = Fixture(this)
        try {
            val repository = f.start()
            val release = CompletableDeferred<Unit>()
            f.transport.onWrite = { release.await() }
            val write = async { repository.bulk(listOf(InboxDoneTarget("a", "v1")), InboxBulkAction.Done) }
            runCurrent()
            val next = Transport({ currentTime })
            f.pairing.value = next
            runCurrent()
            repository.refresh()
            release.complete(Unit)
            write.await()
            assertTrue(repository.snapshot.value.records.none { it.item.isDone })
            assertEquals(0, next.writes)
        } finally { f.stop() }
    }

    @Test fun sixWritesAreOptimisticButNetworkConcurrencyIsBounded() = runTest {
        val f = Fixture(this)
        try {
            f.transport.items = (1..6).map { itemRow(it.toString()) }
            val repository = f.start()
            val writes = (1..6).map { id -> async { repository.bulk(listOf(InboxDoneTarget(id.toString(), "v1")), InboxBulkAction.Done) } }
            runCurrent()
            assertEquals(6, repository.snapshot.value.records.count { it.item.isDone })
            assertEquals(4, f.transport.writes)
            writes.awaitAll()
            assertEquals(6, f.transport.writes)
            assertEquals(2_000L, f.transport.lastWriteAt)
        } finally { f.stop() }
    }

    @Test fun threeConflictsShareOneFullRefresh() = runTest {
        val f = Fixture(this)
        try {
            val repository = f.start()
            f.transport.conflict = true
            f.transport.listDelay = 5_000
            listOf("a", "b", "c").map { id -> async { repository.bulk(listOf(InboxDoneTarget(id, "v1")), InboxBulkAction.Done) } }.awaitAll()
            withContext(Dispatchers.Default) {
                withTimeout(5_000) { while (repository.snapshot.value.records.any { it.item.version != "v2" }) delay(10) }
            }
            println("REFRESH_METRIC writes=${f.transport.writes} full_refreshes_after_three_conflicts=${f.transport.lists - 1}")
            assertEquals(3, f.transport.writes)
            assertEquals(2, f.transport.lists)
            assertTrue(repository.snapshot.value.records.none { it.item.isDone })
        } finally { f.stop() }
    }

}
