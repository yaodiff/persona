package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.*
import org.junit.Test
import org.junit.Rule
import org.junit.rules.TemporaryFolder

@OptIn(ExperimentalCoroutinesApi::class)
class ChatRepositoriesTest {
    @get:Rule val temporary = TemporaryFolder()

    @Test fun unobservedSessionRevisionsNeverReadBodiesAndReopeningStillVerifiesTheLatestPage() = runTest {
        val sessions = FakeSessions()
        val connection = FakeConnection()
        var requests = 0
        var cacheLoads = 0
        val cache = object : ChatItemsCache {
            override suspend fun load(session: SessionKey) = CachedChat().also { cacheLoads++ }
            override suspend fun save(session: SessionKey, snapshot: CachedChat) = Unit
        }
        val transport = FakeChatTransport().apply { fetch = {
            requests++
            ItemsResponse(listOf(item(1)), history = "h", toSeq = 1)
        } }
        val repository = HubChatRepository(transport, connection, cache, sessions, backgroundScope)
        sessions.sessions.value = (1..40).map { Session(key.machineId, "unwatched-$it", status = "working", rev = 1) }
        runCurrent()
        advanceTimeBy(2_000); runCurrent()
        assertEquals(0, requests)
        assertEquals(0, cacheLoads)
        var snapshot = ChatSnapshot()
        val watching = backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        assertTrue(snapshot.verified)
        val opened = requests
        watching.cancelAndJoin()
        connection.events.emit(RevsEvent(key.machineId, mapOf(key.sessionId to 2L)))
        advanceTimeBy(2_000); runCurrent()
        assertEquals(opened, requests)
        backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        assertTrue(requests > opened)
        assertTrue(snapshot.verified)
        assertEquals(listOf(item(1)), snapshot.items)
    }

    @Test fun aChatReopenedWithinItsReleaseStaysSubscribedAndSkipsTheColdOpen() = runTest {
        val connection = FakeConnection()
        var requests = 0
        val transport = FakeChatTransport().apply { fetch = {
            requests++
            ItemsResponse(listOf(item(1)), history = "h", toSeq = 1)
        } }
        val repository = HubChatRepository(transport, connection, FakeItemsCache(CachedChat()), FakeSessions(), backgroundScope,
            releaseAfterMillis = 60_000)
        fun unsubscribes() = connection.sent.count { it["type"]?.jsonPrimitive?.content == "unsubscribe" }
        val first = backgroundScope.launch { repository.observe(key).collect() }
        runCurrent()
        val opened = requests
        first.cancelAndJoin()
        advanceTimeBy(30_000); runCurrent()
        assertEquals(0, unsubscribes())
        var snapshot = ChatSnapshot()
        val again = backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        assertEquals("a warm reopen fetches nothing", opened, requests)
        assertTrue(snapshot.verified)
        again.cancelAndJoin()
        advanceTimeBy(60_001); runCurrent()
        assertEquals(1, unsubscribes())
    }

    @Test fun aClosedChatAwaitingItsSentMessageKeepsSyncingUntilItIsSeen() = runTest {
        val connection = FakeConnection()
        val awaiting = MutableStateFlow(emptySet<SessionKey>())
        var requests = 0
        val seen = mutableListOf<List<Item>>()
        val transport = FakeChatTransport().apply { fetch = {
            requests++
            ItemsResponse(listOf(item(1)), history = "h", toSeq = 1)
        } }
        val repository = HubChatRepository(transport, connection, FakeItemsCache(CachedChat()), FakeSessions(), backgroundScope,
            transcript = { _, items -> seen += items }, awaitingTranscript = awaiting)
        awaiting.value = setOf(key)
        runCurrent()
        connection.events.emit(RevsEvent(key.machineId, mapOf(key.sessionId to 2L)))
        advanceTimeBy(2_000); runCurrent()
        assertTrue(requests > 0)
        assertEquals(listOf(item(1)), seen.last())
        awaiting.value = emptySet()
        runCurrent()
        val settled = requests
        connection.events.emit(RevsEvent(key.machineId, mapOf(key.sessionId to 3L)))
        advanceTimeBy(2_000); runCurrent()
        assertEquals(settled, requests)
    }

    @Test fun theMostRecentlyClosedChatOutlivesIdleChatsTouchedByRevisions() = runTest {
        val connection = FakeConnection()
        val loads = mutableMapOf<SessionKey, Int>()
        val cache = object : ChatItemsCache {
            override suspend fun load(session: SessionKey) = CachedChat().also { loads.merge(session, 1, Int::plus) }
            override suspend fun save(session: SessionKey, snapshot: CachedChat) = Unit
        }
        val transport = FakeChatTransport().apply { fetch = { ItemsResponse(listOf(item(1)), history = "h", toSeq = 1) } }
        val repository = HubChatRepository(transport, connection, cache, FakeSessions(), backgroundScope)
        val open = backgroundScope.launch { repository.observe(key).collect() }
        runCurrent()
        val others = (1..4).map { SessionKey(key.machineId, "other-$it") }
        others.forEach { other ->
            backgroundScope.launch { repository.observe(other).collect() }.also { runCurrent() }.cancelAndJoin()
        }
        connection.events.emit(RevsEvent(key.machineId, others.associate { it.sessionId to 2L }))
        runCurrent()
        open.cancelAndJoin()
        backgroundScope.launch { repository.observe(key).collect() }
        runCurrent()
        assertEquals("the chat just closed is still retained", 1, loads[key])
    }

    @Test fun olderPagesNotifyTranscriptWithoutChangingTheLastItem() = runTest {
        val recent = listOf(item(3).copy(role = "user"), item(4))
        val older = listOf(item(1).copy(role = "user"), item(2))
        val transport = FakeChatTransport().apply { fetch = { query ->
            if (query.beforeSeq != null) ItemsResponse(older, hasMore = false, history = "h")
            else ItemsResponse(recent, hasMore = true, history = "h", toSeq = 4)
        } }
        val observed = mutableListOf<List<String>>()
        val repository = HubChatRepository(transport, FakeConnection(), FakeItemsCache(CachedChat()), FakeSessions(), backgroundScope,
            transcript = { _, items -> observed += items.map { it.id } })
        backgroundScope.launch { repository.observe(key).collect() }
        runCurrent()
        assertEquals(recent.map { it.id }, observed.last())
        repository.loadOlder(key)
        runCurrent()
        assertEquals((older + recent).map { it.id }, observed.last())
    }

    @Test fun listsRestoreBeforeNetworkAndKeepSnapshotWhenRefreshFails() = runTest {
        val directory = temporary.newFolder()
        val disk = ListSnapshotStore(directory)
        val cached = Session("machine", "cached", rev = 1)
        disk.save("sessions", listOf(cached))
        val requested = CompletableDeferred<Unit>()
        val response = CompletableDeferred<List<Session>>()
        val transport = FakeChatTransport().apply { listFetch = { requested.complete(Unit); response.await() } }
        val repository = HubSessionsRepository(transport, FakeConnection(), backgroundScope, ListSnapshotStore(directory))
        requested.await()
        assertEquals(listOf(cached), repository.sessions.value)
        val fresh = cached.copy(rev = 2)
        response.complete(listOf(fresh))
        repository.sessions.first { it == listOf(fresh) }
        // Wait for the refresh lock, which also covers committing the snapshot.
        transport.listFetch = { throw java.io.IOException("Offline") }
        assertTrue(runCatching { repository.refresh() }.isFailure)
        assertEquals(listOf(fresh), repository.sessions.value)
        assertEquals(listOf(fresh), disk.restore<List<Session>>("sessions"))
        val offline = FakeConnection().apply { state.value = ConnectionState.WaitingForNetwork }
        val restarted = HubSessionsRepository(transport, offline, backgroundScope, ListSnapshotStore(directory))
        assertEquals(listOf(fresh), restarted.sessions.first { it.isNotEmpty() })
    }

    private val key = SessionKey("machine", "session")
    private fun item(seq: Int, kind: String = "text") = Item("$seq#0", seq, role = "assistant", kind = kind)

    @Test fun recordedDurationsRestoreFromPagesAndCacheAndFollowTheirOwnTurn() = runTest {
        val connection = FakeConnection()
        val user = item(1).copy(role = "user")
        val answer = item(2)
        val duration = JsonCodec.decodeFromString<Item>("""{"id":"3#0","seq":3,"role":"system","kind":"turn_duration","turn_duration":{"source":"claude_turn_duration","parent_uuid":"2","duration_ms":12345}}""")
        val initial = listOf(user, answer, duration)
        val transport = FakeChatTransport().apply { fetch = { ItemsResponse(initial, history = "h", toSeq = 3) } }
        val repository = HubChatRepository(transport, connection, FakeItemsCache(CachedChat(initial, "h", 3, false)), FakeSessions(), backgroundScope)
        var snapshot = ChatSnapshot()
        backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        assertEquals(12L, snapshot.turnDurationSeconds)
        assertEquals(mapOf(user.id to 12L), snapshot.recordedTurnDurations)

        val next = item(4).copy(role = "user")
        connection.events.emit(ItemsEvent(key.machineId, key.sessionId, listOf(next), "h", 3, 4))
        runCurrent()
        assertNull(snapshot.turnDurationSeconds)
        assertEquals(12L, snapshot.recordedTurnDurations[user.id])
        val completed = item(5, "turn_duration").copy(role = "system",
            turnDuration = TurnDuration(0, "codex_task_complete", turnId = "turn-two", userItemId = next.id))
        connection.events.emit(ItemsEvent(key.machineId, key.sessionId, listOf(completed), "h", 4, 5))
        runCurrent()
        assertEquals(0L, snapshot.turnDurationSeconds)
        connection.events.emit(DeltaEvent(key.machineId, key.sessionId, "新的回复"))
        runCurrent()
        assertNull(snapshot.turnDurationSeconds)
        connection.events.emit(ItemsEvent(key.machineId, key.sessionId, listOf(next.copy(seq = 1)), "new-history", 0, 1))
        runCurrent()
        assertTrue(snapshot.recordedTurnDurations.isEmpty())
        assertNull(snapshot.turnDurationSeconds)
    }

    @Test fun recordedDurationsRequireValidNativeAssociations() {
        val user = item(1).copy(role = "user")
        val answer = item(2)
        val marker = item(3, "turn_duration").copy(turnDuration = TurnDuration(12345, "claude_turn_duration", parentUuid = "2"))
        for (invalid in listOf(
            marker.copy(turnDuration = marker.turnDuration?.copy(durationMs = -1)),
            marker.copy(turnDuration = marker.turnDuration?.copy(durationMs = null)),
            marker.copy(turnDuration = marker.turnDuration?.copy(parentUuid = "missing")),
            marker.copy(turnDuration = marker.turnDuration?.copy(source = "unknown")),
            marker.copy(turnDuration = TurnDuration(1000, "codex_task_complete", userItemId = user.id)),
        )) assertTrue(recordedTurnDurations(listOf(user, answer, invalid)).isEmpty())
        assertTrue(recordedTurnDurations(listOf(answer, marker)).isEmpty())
        val newUser = item(3).copy(role = "user")
        assertTrue(recordedTurnDurations(listOf(user, answer, newUser, marker.copy(seq = 4))).isEmpty())
    }

    @Test fun reconnectWaitsForTheLastBacklogPage() = runTest {
        val connection = FakeConnection()
        val transport = FakeChatTransport().apply { fetch = { ItemsResponse(listOf(item(1)), history = "h", toSeq = 1) } }
        val repository = HubChatRepository(transport, connection, FakeItemsCache(CachedChat()), FakeSessions(), backgroundScope)
        backgroundScope.launch { repository.observe(key).collect() }
        runCurrent()
        val lastPage = CompletableDeferred<ItemsResponse>()
        val queries = mutableListOf<Int>()
        transport.fetch = { query ->
            queries += requireNotNull(query.afterSeq)
            if (query.afterSeq == 1) ItemsResponse(listOf(item(2)), history = "h", fromSeq = 1, toSeq = 2, moreAfter = true)
            else lastPage.await()
        }
        val ready = async { repository.reconcileOpened() }
        runCurrent()
        assertEquals(listOf(1, 2), queries)
        assertFalse(ready.isCompleted)
        lastPage.complete(ItemsResponse(listOf(item(3)), history = "h", fromSeq = 2, toSeq = 3))
        ready.await()
    }

    @Test fun cachedOpenSharesSubscriptionFillsGapsReloadsAndExpandsEveryRangePage() = runTest {
        val connection = FakeConnection()
        val transport = FakeChatTransport()
        val initial = CompletableDeferred<ItemsResponse>()
        val fold = item(3, "fold").copy(fold = Fold(items = 3, throughSeq = 5, throughId = "5#0"))
        val queries = mutableListOf<ItemsQuery>()
        var capped = false
        transport.fetch = { query ->
            queries += query
            when {
                query.fromSeq == 3 -> ItemsResponse(listOf(item(3), item(4)), more = true)
                query.fromSeq == 5 -> ItemsResponse(listOf(item(5)))
                query.afterSeq != null -> ItemsResponse(listOf(item(query.afterSeq!! + 1)), history = "h",
                    fromSeq = query.afterSeq, toSeq = query.afterSeq!! + 1, moreAfter = capped)
                query.beforeSeq != null -> ItemsResponse(listOf(item(1)), hasMore = false, history = "h")
                queries.count { it == ItemsQuery() } == 1 -> initial.await()
                else -> ItemsResponse(listOf(item(30)), history = "h", toSeq = 30)
            }
        }
        val cache = FakeItemsCache(CachedChat(listOf(item(2)), "h", 2, true))
        val repository = HubChatRepository(transport, connection, cache, FakeSessions(), backgroundScope)
        var snapshot = ChatSnapshot()
        val first = backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        val second = backgroundScope.launch { repository.observe(key).collect() }
        runCurrent()
        assertEquals(listOf("2#0"), snapshot.items.map { it.id })
        assertFalse(snapshot.verified)
        repository.markSeen(key, "2#0")
        assertTrue(connection.sent.none { it.type() == "seen" })
        initial.complete(ItemsResponse(listOf(item(2), fold, item(6)), true, "h", toSeq = 6))
        runCurrent()
        assertTrue(snapshot.verified)
        assertEquals(1, connection.sent.count { it.type() == "subscribe" })
        assertEquals("6", connection.sent.first { it.type() == "subscribe" }["since_seq"]!!.jsonPrimitive.content)
        repository.markSeen(key, "6#0")
        repository.markSeen(key, "2#0")
        assertEquals(1, connection.sent.count { it.type() == "seen" })

        val expanded = repository.expandFold(key, 3, 5)
        assertEquals(listOf(3, 4, 5), expanded.map { it.seq })
        assertFalse(cache.saved.items.any { it.kind == "fold" })
        repository.loadOlder(key)
        runCurrent()
        assertFalse(snapshot.hasOlder)

        capped = true
        connection.events.emit(ItemsEvent(key.machineId, key.sessionId, listOf(item(20)), "h", 19, 20))
        runCurrent()
        // Filled at once since #7102 (no settling delay): the capped pages have already ended in the latest page.
        advanceTimeBy(1500)
        runCurrent()
        assertEquals(8, queries.count { it.afterSeq != null })
        assertEquals(listOf(30), snapshot.items.map { it.seq })
        assertTrue(snapshot.verified)

        connection.events.emit(ItemsEvent(key.machineId, key.sessionId, listOf(item(1)), "replacement", 0, 1))
        runCurrent()
        assertEquals(listOf(1), snapshot.items.map { it.seq })
        repository.markSeen(key, "1#0")
        assertEquals(2, connection.sent.count { it.type() == "seen" })

        first.cancelAndJoin()
        assertEquals(0, connection.sent.count { it.type() == "unsubscribe" })
        second.cancelAndJoin()
        assertEquals(1, connection.sent.count { it.type() == "unsubscribe" })
        assertEquals(2, queries.count { it == ItemsQuery() })
    }

    // #3601: a read covers the rev moves heard when it was sent. Lines 4 and 5 lose their push and the rev moves; while
    // the catch-up read for them is out, line 6 is written (its push lost too) and the rev moves again. The read comes
    // back with 4 and 5 only, as the node read the file before line 6: the chat reads once more and gets it.
    @Test fun aRevMoveWhileTheCatchUpReadIsOutReadsAgain() = runTest {
        val connection = FakeConnection()
        val transport = FakeChatTransport()
        val held = CompletableDeferred<ItemsResponse>()
        val queries = mutableListOf<ItemsQuery>()
        transport.fetch = { query ->
            queries += query
            when (query.afterSeq) {
                null -> ItemsResponse(listOf(item(1), item(2), item(3)), history = "h", toSeq = 3)
                3 -> held.await()
                5 -> ItemsResponse(listOf(item(6)), history = "h", fromSeq = 5, toSeq = 6)
                else -> ItemsResponse(emptyList(), history = "h", fromSeq = query.afterSeq, toSeq = query.afterSeq)
            }
        }
        val repository = HubChatRepository(transport, connection, FakeItemsCache(CachedChat(emptyList(), null, 0, null)),
            FakeSessions(), backgroundScope)
        var snapshot = ChatSnapshot()
        backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        assertEquals(listOf(1, 2, 3), snapshot.items.map { it.seq })

        connection.events.emit(RevsEvent(key.machineId, mapOf(key.sessionId to 5L)))
        advanceTimeBy(1500)
        runCurrent()
        assertEquals(listOf(3), queries.mapNotNull { it.afterSeq })
        connection.events.emit(RevsEvent(key.machineId, mapOf(key.sessionId to 6L))) // while the read is out
        runCurrent()
        held.complete(ItemsResponse(listOf(item(4), item(5)), history = "h", fromSeq = 3, toSeq = 5))
        advanceTimeBy(5000)
        runCurrent()
        assertEquals(listOf(1, 2, 3, 4, 5, 6), snapshot.items.map { it.seq })
        assertEquals(listOf(3, 5), queries.mapNotNull { it.afterSeq }.take(2))
        assertTrue(snapshot.verified)
    }

    @Test fun aBurstOfItemsEventsSavesOnceMoreWithTheLatestState() = runTest {
        val connection = FakeConnection()
        val transport = FakeChatTransport().apply { fetch = { ItemsResponse(listOf(item(1)), history = "h", toSeq = 1) } }
        val cache = SlowCache()
        val repository = HubChatRepository(transport, connection, cache, FakeSessions(), backgroundScope)
        var snapshot = ChatSnapshot()
        backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        val opened = cache.saves.size
        val disk = CompletableDeferred<Unit>().also { cache.disk = it }
        for (seq in 2..11) {
            connection.events.emit(ItemsEvent(key.machineId, key.sessionId, listOf(item(seq)), "h", seq - 1, seq))
            runCurrent() // the first event's save starts and waits for the disk
        }
        disk.complete(Unit)
        runCurrent()
        // The save already writing, then one that reads the state once it starts: not one an event.
        assertEquals(2, cache.saves.size - opened)
        assertEquals((1..11).toList(), cache.saves.last().items.map { it.seq })
        assertEquals(snapshot.items.map { it.id }, cache.saves.last().items.map { it.id })
    }

    @Test fun localDiscardSurvivesReplayReconnectAndRestoredHistoryWithoutSending() = runTest {
        val disk = ListSnapshotStore(temporary.newFolder())
        val connection = FakeConnection()
        val transport = FakeChatTransport()
        val old = Interaction("old", kind = "question")
        val permission = Interaction("permission", kind = "permission")
        val fresh = Interaction("fresh", kind = "question", canCancel = true)
        transport.pending = listOf(old, permission, fresh)
        val center = HubInteractionCenter(transport, connection, backgroundScope, disk)
        center.refresh()
        center.discardLocal(old.id)
        center.discardLocal(permission.id)
        center.discardLocal(old.id)
        assertNull(transport.resolved)
        assertTrue(center.answered.value.isEmpty())
        center.refresh()
        connection.events.emit(InteractionEvent(old))
        connection.events.emit(InteractionEvent(permission))
        connection.state.value = ConnectionState.WaitingForNetwork
        runCurrent()
        connection.state.value = ConnectionState.Online
        runCurrent()
        assertEquals(listOf(fresh), center.pending.value)

        // Even an old history snapshot left on disk cannot resurrect a discarded card.
        disk.save("interactions", listOf(old, permission, fresh))
        val restored = HubInteractionCenter(transport, FakeConnection(), backgroundScope, disk)
        transport.pendingFetch = { error("Offline") }
        assertNotNull(runCatching { restored.refresh() }.exceptionOrNull())
        assertEquals(listOf(fresh), restored.pending.value)
        transport.pendingFetch = null
        restored.refresh()
        assertEquals(listOf(fresh), restored.pending.value)
        val otherGroup = HubInteractionCenter(transport, FakeConnection(), backgroundScope, ListSnapshotStore(temporary.newFolder()))
        otherGroup.refresh()
        assertEquals(listOf(old, permission, fresh), otherGroup.pending.value)
        restored.resolveOutcome(fresh.id, Resolution(answers = mapOf("Format?" to "JSON")))
        assertEquals(fresh.id, transport.resolved)
        assertTrue(restored.pending.value.isEmpty())
    }

    @Test fun failedLocalDiscardWriteKeepsCardAndDoesNotSend() = runTest {
        val directory = temporary.newFolder()
        val disk = ListSnapshotStore(directory)
        val transport = FakeChatTransport().apply { pending = listOf(Interaction("old", kind = "question")) }
        val center = HubInteractionCenter(transport, FakeConnection(), backgroundScope, disk)
        center.refresh()
        assertTrue(directory.deleteRecursively())
        assertTrue(directory.createNewFile())
        assertNotNull(runCatching { center.discardLocal("old") }.exceptionOrNull())
        assertEquals(transport.pending, center.pending.value)
        assertNull(transport.resolved)
    }

    @Test fun cardsThatEndedHereStayGoneFromLaterSnapshotsAndFrames() = runTest {
        val connection = FakeConnection()
        val transport = FakeChatTransport()
        val center = HubInteractionCenter(transport, connection, backgroundScope)
        val answered = Interaction("answered", kind = "permission")
        val gone = Interaction("gone", kind = "question")
        val other = Interaction("other", kind = "plan")
        transport.pending = listOf(answered, gone, other)
        center.refresh()
        assertEquals(3, center.pending.value.size)

        transport.outcome = ResolveOutcome(already = true, status = "resolved", resolution = Resolution(allow = false))
        assertEquals(Resolution(allow = false), center.resolveOutcome(answered.id, Resolution(allow = true)).resolution)
        transport.goneStatus = "expired"
        val error = runCatching { center.resolveOutcome(gone.id, Resolution(answers = mapOf())) }.exceptionOrNull()
        assertEquals("expired", (error as InteractionGoneException).status)
        assertEquals(listOf(other), center.pending.value)

        // A snapshot the hub served before it caught up still lists both: they do not come back.
        center.refresh()
        assertEquals(listOf(other), center.pending.value)

        // Nor does an `interaction` frame for one: the snapshot after a resumed hello, before the node ended it (#2767).
        connection.events.emit(InteractionEvent(answered))
        connection.events.emit(InteractionEvent(gone))
        runCurrent()
        assertEquals(listOf(other), center.pending.value)
        center.refresh()
        assertEquals(listOf(other), center.pending.value)
    }

    @Test fun listUsesBroadcastRevsAndInteractionsReconcileWithoutResurrectingResolvedCards() = runTest {
        val connection = FakeConnection()
        val transport = FakeChatTransport()
        transport.list = listOf(Session(key.machineId, key.sessionId, rev = 1))
        val sessions = HubSessionsRepository(transport, connection, backgroundScope)
        val center = HubInteractionCenter(transport, connection, backgroundScope)
        val ix = Interaction("request", key.machineId, key.sessionId, kind = "permission")
        transport.pending = listOf(ix)
        runCurrent()
        assertEquals(listOf(ix), center.pending.value)
        connection.events.emit(RevsEvent(key.machineId, mapOf(key.sessionId to 2L)))
        runCurrent()
        assertEquals(2L, sessions.sessions.value.single().rev)
        assertTrue(connection.sent.isEmpty())

        val response = CompletableDeferred<List<Interaction>>()
        transport.pendingFetch = { response.await() }
        val refresh = backgroundScope.launch { center.refresh() }
        runCurrent()
        connection.events.emit(InteractionResolvedEvent(ix.id))
        response.complete(listOf(ix))
        runCurrent()
        refresh.join()
        assertTrue(center.pending.value.isEmpty())

        transport.pendingFetch = null
        transport.pending = emptyList()
        connection.events.emit(InteractionEvent(ix))
        runCurrent()
        assertEquals(1, center.pending.value.size)
        connection.state.value = ConnectionState.Connecting
        runCurrent()
        connection.state.value = ConnectionState.Online
        runCurrent()
        assertTrue(center.pending.value.isEmpty())
        connection.events.emit(InteractionEvent(ix))
        runCurrent()
        center.resolve(ix.id, Resolution(allow = true))
        assertTrue(center.pending.value.isEmpty())
        assertEquals(ix.id, transport.resolved)
    }

    @Test fun realtimeRowsKeepIdentityUntilTranscriptArrivesAndNeverEnterCoverageOrCache() = runTest {
        val connection = FakeConnection()
        val user = item(1).copy(role = "user", text = "示例问题")
        var transcript = listOf(user)
        val transport = FakeChatTransport().apply {
            fetch = { query -> ItemsResponse(transcript.filter { it.seq > (query.afterSeq ?: 0) },
                history = "h", fromSeq = query.afterSeq, toSeq = transcript.last().seq) }
        }
        val cache = FakeItemsCache(CachedChat(emptyList(), "h", 0, false))
        val repository = HubChatRepository(transport, connection, cache, FakeSessions(), backgroundScope)
        var snapshot = ChatSnapshot()
        backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        suspend fun event(event: ServerEvent) { connection.events.emit(event); runCurrent() }

        event(DeltaEvent(key.machineId, key.sessionId, "示例"))
        val draftId = snapshot.items.last().id
        assertEquals(setOf(draftId), snapshot.streamingItemIds)
        repository.markSeen(key, draftId)
        assertTrue(connection.sent.none { it.type() == "seen" })
        event(DeltaEvent(key.machineId, key.sessionId, "示例回复"))
        assertEquals(draftId, snapshot.items.last().id)
        assertEquals("示例回复", snapshot.items.last().text)
        event(DeltaEvent(key.machineId, key.sessionId))
        assertEquals(draftId, snapshot.items.last().id)
        assertTrue(snapshot.streamingItemIds.isEmpty())

        val args = buildJsonObject { put("query", "示例参数") }
        event(ToolCallEvent("call", "phone_example", args))
        assertEquals(2, snapshot.items.size)
        val tool = item(2, "tool_use").copy(toolUseId = "call")
        val reply = item(3).copy(text = "示例回复完成")
        transcript = listOf(user, tool, reply)
        event(ItemsEvent(key.machineId, key.sessionId, listOf(tool, reply), "h", 1, 3))
        assertEquals(listOf(user.id, tool.id, draftId), snapshot.items.map { it.id })
        assertEquals(reply.text, snapshot.items.last().text)
        assertEquals("phone_example", snapshot.items[1].toolName)
        assertEquals(args, snapshot.items[1].toolInput)
        assertEquals(listOf(user.id, tool.id, reply.id), cache.saved.items.map { it.id })
        assertEquals(3, cache.saved.covered)
        repository.markSeen(key, draftId)
        assertEquals(draftId, repository.observe(key).first().lastSeenMessageId)
        repository.fullText(key, draftId)
        assertEquals(reply.id, transport.fullItemId)
        // #5305: forking the shown reply sends the node its transcript id.
        var forkedAt: String? = null
        repository.trackActions(object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun fork(session: Session, atItemId: String) = "forked".also { forkedAt = atItemId }
        }).fork(Session(key.machineId, key.sessionId), draftId)
        assertEquals(reply.id, forkedAt)

        event(ScreenEvent(key.machineId, key.sessionId, "屏幕内容", "working"))
        val screenId = snapshot.items.last().id
        event(ScreenEvent(key.machineId, key.sessionId, "更新的屏幕", "blocked"))
        assertEquals(screenId, snapshot.items.last().id)
        assertEquals("更新的屏幕", snapshot.items.last().text)
        event(ScreenEvent(key.machineId, key.sessionId))
        assertEquals(3, snapshot.items.size)
        event(ToolCallEvent("call", "phone_updated", args))
        assertEquals("phone_updated", snapshot.items[1].toolName)
        event(ErrorEvent(key.machineId, "other", "其他会话错误"))
        assertNull(snapshot.error)
        event(ErrorEvent(key.machineId, key.sessionId, "示例错误"))
        assertEquals("示例错误", snapshot.error)

        val nextUser = user.copy(id = "4#0", seq = 4)
        transcript = transcript + nextUser
        event(ItemsEvent(key.machineId, key.sessionId, listOf(nextUser), "h", 3, 4))
        assertNull(snapshot.error)
        event(DeltaEvent(key.machineId, key.sessionId, "示例回复"))
        val nextDraftId = snapshot.items.last().id
        assertNotEquals(draftId, nextDraftId)
        val nextReply = reply.copy(id = "5#0", seq = 5)
        transcript = transcript + nextReply
        event(ItemsEvent(key.machineId, key.sessionId, listOf(nextReply), "h", 4, 5))
        event(DeltaEvent(key.machineId, key.sessionId, "示例回复完"))
        assertEquals(nextReply.text, snapshot.items.last().text)
        event(DeltaEvent(key.machineId, key.sessionId))
        assertEquals(nextDraftId, snapshot.items.last().id)
        assertEquals(2, snapshot.items.count { it.role == "assistant" && it.kind == "text" })

        event(DeltaEvent(key.machineId, key.sessionId, "尚未落盘"))
        event(ItemsEvent(key.machineId, key.sessionId, listOf(user), "new-history", 0, 1))
        assertEquals(listOf(user), snapshot.items)
        assertFalse(snapshot.ended)
    }

    @Test fun reconnectAfterTheTurnEndedOfflineDropsTheCursorAndShowsTheCutRecord() = runTest {
        val connection = FakeConnection()
        val user = item(1).copy(role = "user", text = "示例问题")
        var transcript = listOf(user)
        val working = Session(key.machineId, key.sessionId, status = "working")
        val transport = FakeChatTransport().apply {
            list = listOf(working)
            machineList = listOf(Machine(key.machineId, online = true))
            fetch = { query -> ItemsResponse(transcript.filter { it.seq > (query.afterSeq ?: 0) },
                history = "h", fromSeq = query.afterSeq, toSeq = transcript.last().seq) }
        }
        val sessions = HubSessionsRepository(transport, connection, backgroundScope)
        val repository = HubChatRepository(transport, connection, FakeItemsCache(CachedChat(emptyList(), "h", 0, false)),
            sessions, backgroundScope)
        var snapshot = ChatSnapshot()
        backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        suspend fun event(event: ServerEvent) { connection.events.emit(event); runCurrent() }
        suspend fun reconnect() {
            connection.state.value = ConnectionState.Connecting
            runCurrent()
            connection.state.value = ConnectionState.Online
            runCurrent()
        }
        val whole = "示".repeat(30_000)
        event(DeltaEvent(key.machineId, key.sessionId, whole.take(500)))
        assertEquals(1, snapshot.streamingItemIds.size)

        // Still working after a reconnect: the reply keeps streaming.
        reconnect()
        assertEquals(1, snapshot.streamingItemIds.size)

        // The turn ends while offline: the closing empty delta never arrives.
        connection.state.value = ConnectionState.Connecting
        runCurrent()
        val cut = item(2).copy(text = whole.take(8_000) + "\n…（已截断）", fullSize = 90_000)
        transcript = listOf(user, cut)
        transport.list = listOf(working.copy(status = "idle"))
        connection.state.value = ConnectionState.Online
        runCurrent()

        assertTrue(snapshot.streamingItemIds.isEmpty())
        val reply = snapshot.items.single { it.role == "assistant" }
        assertEquals(cut.text, reply.text)
        assertEquals(90_000, reply.fullSize)
    }

    @Test fun reopenAfterTheTurnEndedWhileAwayDropsTheCursorAndShowsTheCutRecord() = runTest {
        val connection = FakeConnection()
        val user = item(1).copy(role = "user", text = "示例问题")
        var transcript = listOf(user)
        val working = Session(key.machineId, key.sessionId, status = "working")
        val transport = FakeChatTransport().apply {
            list = listOf(working)
            machineList = listOf(Machine(key.machineId, online = true))
            fetch = { query -> ItemsResponse(transcript.filter { it.seq > (query.afterSeq ?: 0) },
                history = "h", fromSeq = query.afterSeq, toSeq = transcript.last().seq) }
        }
        val sessions = HubSessionsRepository(transport, connection, backgroundScope)
        val repository = HubChatRepository(transport, connection, FakeItemsCache(CachedChat(emptyList(), "h", 0, false)),
            sessions, backgroundScope)
        var snapshot = ChatSnapshot()
        var job = backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        suspend fun event(event: ServerEvent) { connection.events.emit(event); runCurrent() }
        val whole = "示".repeat(30_000)
        event(DeltaEvent(key.machineId, key.sessionId, whole.take(500)))
        assertEquals(1, snapshot.streamingItemIds.size)

        // Leaving the screen stops observing (references drop to 0).
        job.cancel()
        runCurrent()

        // Still working when returning: the reply keeps streaming.
        job = backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        assertEquals(1, snapshot.streamingItemIds.size)

        // Leave again and the turn ends while away: closing empty delta is lost.
        job.cancel()
        runCurrent()
        val cut = item(2).copy(text = whole.take(8_000) + "\n…（已截断）", fullSize = 90_000)
        transcript = listOf(user, cut)
        transport.list = listOf(working.copy(status = "idle"))
        event(SessionsEvent(key.machineId, listOf(working.copy(status = "idle"))))

        // Reopening the chat settles the stale draft and shows the settled record.
        backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()

        assertTrue(snapshot.streamingItemIds.isEmpty())
        val reply = snapshot.items.single { it.role == "assistant" }
        assertEquals(cut.text, reply.text)
        assertEquals(90_000, reply.fullSize)
    }

    @Test fun lifecycleRequiresVerifiedListsAndConfirmedActionsAndKeepsRecordedDurationOnly() = runTest {
        val connection = FakeConnection()
        val working = Session(key.machineId, key.sessionId, status = "working", paneId = "pane")
        val transport = FakeChatTransport().apply {
            list = listOf(working)
            machineList = listOf(Machine(key.machineId, online = true))
            fetch = { ItemsResponse(listOf(item(1)), history = "h", fromSeq = it.afterSeq, toSeq = 1) }
        }
        val sessions = HubSessionsRepository(transport, connection, backgroundScope)
        val repository = HubChatRepository(transport, connection,
            FakeItemsCache(CachedChat(emptyList(), null, 0, false)), sessions, backgroundScope,
            foreground = MutableStateFlow(false))
        var snapshot = ChatSnapshot()
        backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        suspend fun event(event: ServerEvent) { connection.events.emit(event); runCurrent() }
        var result: ChatActionResult = ChatActionResult.Unknown
        val actions = repository.trackActions(object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = result
            override suspend fun verify(session: Session, action: ChatAction) = result
        })
        assertNull(snapshot.turnDurationSeconds)
        actions.execute(working, ChatAction.Interrupt)
        runCurrent()
        assertFalse(snapshot.stopping)
        result = ChatActionResult.Done
        actions.execute(working, ChatAction.Interrupt)
        runCurrent()
        assertTrue(snapshot.stopping)
        event(ScreenEvent(key.machineId, key.sessionId, status = "idle"))
        assertFalse(snapshot.stopping)
        actions.execute(working, ChatAction.Interrupt)
        actions.verify(working, ChatAction.Interrupt)
        runCurrent()
        assertFalse(snapshot.stopping)
        actions.execute(working, ChatAction.Interrupt)
        event(HookEvent(key.machineId, key.sessionId, "Stop"))
        assertFalse(snapshot.stopping)
        actions.execute(working, ChatAction.Interrupt)
        event(SessionsEvent(key.machineId, listOf(working.copy(status = "blocked"))))
        event(SessionsEvent(key.machineId, listOf(working)))
        assertFalse(snapshot.stopping)
        repository.recordTurnDuration(key, 12)
        runCurrent()
        assertEquals(12L, snapshot.turnDurationSeconds)
        event(DeltaEvent(key.machineId, key.sessionId, "新的回复"))
        assertNull(snapshot.turnDurationSeconds)
        event(MachinesEvent(listOf(Machine(key.machineId, online = false))))
        event(SessionsEvent(key.machineId, emptyList()))
        assertFalse(snapshot.ended)
        event(MachinesEvent(transport.machineList))
        assertFalse(snapshot.ended)
        event(SessionsEvent(key.machineId, listOf(working.copy(status = "offline"))))
        assertFalse(snapshot.ended)

        connection.state.value = ConnectionState.Connecting
        runCurrent()
        val refresh = CompletableDeferred<List<Session>>()
        transport.listFetch = { refresh.await() }
        connection.state.value = ConnectionState.Online
        runCurrent()
        assertFalse(snapshot.ended)
        assertFalse(snapshot.replaced)
        val replacement = working.copy(sessionId = "new-session")
        refresh.complete(listOf(replacement))
        runCurrent()
        assertTrue(snapshot.ended)
        assertTrue(snapshot.replaced)
        assertFalse(snapshot.stopping)
    }

    @Test fun resumedSessionListedAgainIsNoLongerEnded() = runTest {
        val connection = FakeConnection()
        val working = Session(key.machineId, key.sessionId, status = "working", paneId = "pane")
        val transport = FakeChatTransport().apply {
            list = listOf(working)
            machineList = listOf(Machine(key.machineId, online = true))
            fetch = { ItemsResponse(listOf(item(1)), history = "h", fromSeq = it.afterSeq, toSeq = 1) }
        }
        val sessions = HubSessionsRepository(transport, connection, backgroundScope)
        val repository = HubChatRepository(transport, connection,
            FakeItemsCache(CachedChat(emptyList(), null, 0, false)), sessions, backgroundScope,
            foreground = MutableStateFlow(false))
        var snapshot = ChatSnapshot()
        backgroundScope.launch { repository.observe(key).collect { snapshot = it } }
        runCurrent()
        suspend fun event(event: ServerEvent) { connection.events.emit(event); runCurrent() }
        val actions = repository.trackActions(object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
        })
        event(SessionsEvent(key.machineId, emptyList()))
        assertTrue(snapshot.ended)
        // Resume (#4986) lists the same id again.
        event(SessionsEvent(key.machineId, listOf(working)))
        assertFalse(snapshot.ended)

        // A confirmed close stays ended while the old list still names the session, then clears once it is relisted.
        actions.execute(working, ChatAction.Close)
        runCurrent()
        assertTrue(snapshot.ended)
        event(SessionsEvent(key.machineId, listOf(working)))
        assertTrue(snapshot.ended)
        event(SessionsEvent(key.machineId, emptyList()))
        assertTrue(snapshot.ended)
        event(SessionsEvent(key.machineId, listOf(working)))
        assertFalse(snapshot.ended)

        // A replacement in the same pane ends it; the old id coming back undoes that too.
        val replacement = working.copy(sessionId = "new-session")
        event(SessionsEvent(key.machineId, listOf(replacement)))
        assertTrue(snapshot.replaced)
        event(SessionsEvent(key.machineId, listOf(replacement, working)))
        assertFalse(snapshot.ended)
        assertFalse(snapshot.replaced)
    }

    private fun JsonObject.type() = getValue("type").jsonPrimitive.content

    /** Records each save as it starts, then waits for [disk] when one is set. */
    private class SlowCache : ChatItemsCache {
        val saves = mutableListOf<CachedChat>()
        var disk: CompletableDeferred<Unit>? = null
        override suspend fun load(session: SessionKey) = CachedChat()
        override suspend fun save(session: SessionKey, snapshot: CachedChat) { saves += snapshot; disk?.await() }
    }
}
