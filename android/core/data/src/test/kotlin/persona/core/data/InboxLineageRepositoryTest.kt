package persona.core.data

import java.io.File
import java.io.IOException
import java.nio.file.Files
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.job
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.api.HubCodedFailure
import persona.core.contracts.api.InboxAssistantConnection
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.InboxDoneTarget
import persona.core.contracts.api.InboxParentRef
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MailReplyTarget
import persona.core.contracts.api.RequestManager
import persona.core.contracts.api.SessionsRepository
import persona.feature.inbox.InboxItemKey
import persona.feature.inbox.InboxRecord

@OptIn(ExperimentalCoroutinesApi::class)
class InboxLineageRepositoryTest {
    @Test fun treeUsesTheRawHubIdAndManagerAndMapsReturnedItemsToAppIds() = runTest {
        val fixture = Fixture(this, listOf(item("shared", "Current", manager = "m2")))
        try {
            fixture.repository.refresh()
            val current = fixture.repository.snapshot.value.records.single()
            assertEquals(InboxItemKey.appId("shared", "m2"), current.item.id)
            val line = fixture.repository.inboxLine(current)!!
            assertEquals(listOf("shared" to "m2"), fixture.transport.treeCalls)
            assertEquals(current.item.id, line.item?.item?.id)
            assertEquals(InboxItemKey.appId("parent", "m2"), line.ancestors[0].item?.item?.id)
            assertEquals("m2", line.ancestors[0].item?.manager)
            assertNull(line.ancestors[1].item)
            assertNull(line.ancestors[2].item)
            assertEquals(InboxItemKey.appId("child", "m2"), line.children.single().item.id)
            assertTrue(line.children.single().hubItemId == "child")
        } finally { fixture.close() }
    }

    @Test fun aSuccessfulParentWriteUsesRawIdsAppliesTheAnswerAndCanRefreshTheLine() = runTest {
        val fixture = Fixture(this, listOf(item("shared", "Before", manager = "m2")))
        try {
            fixture.repository.refresh()
            val current = fixture.repository.snapshot.value.records.single()
            fixture.repository.setInboxParent(current, InboxParentRef("inbox", "parent"))
            assertEquals(listOf(Triple("shared", InboxParentRef("inbox", "parent"), "m2")), fixture.transport.parentCalls)
            assertEquals("After", fixture.repository.snapshot.value.records.single().item.title)
            assertEquals("After", fixture.repository.inboxLine(current)?.item?.item?.title)
            assertEquals(listOf("shared" to "m2"), fixture.transport.treeCalls)

            fixture.transport.parentCalls.clear()
            fixture.repository.setInboxParent(current, null)
            assertEquals(listOf(Triple("shared", null, "m2")), fixture.transport.parentCalls)
        } finally { fixture.close() }
    }

    @Test fun aFailedWriteLeavesTheShownItemAloneAndDoesNotReadANewLine() = runTest {
        val fixture = Fixture(this, listOf(item("shared", "Before", manager = "m2"))).apply { transport.failParent = true }
        try {
            fixture.repository.refresh()
            val current = fixture.repository.snapshot.value.records.single()
            try { fixture.repository.setInboxParent(current, null); fail("Expected the write to fail") } catch (_: IOException) { }
            assertEquals("Before", fixture.repository.snapshot.value.records.single().item.title)
            assertTrue(fixture.transport.treeCalls.isEmpty())
        } finally { fixture.close() }
    }

    @Test fun anOldHubHasNoLineButOtherReadFailuresStayFailures() = runTest {
        val fixture = Fixture(this, listOf(item("shared", "Current", manager = "m2")))
        try {
            fixture.repository.refresh()
            val current = fixture.repository.snapshot.value.records.single()
            for (status in listOf(404, 405)) {
                fixture.transport.treeFailure = CodedFailure(status)
                assertNull(fixture.repository.inboxLine(current))
            }
            fixture.transport.treeFailure = UnsupportedOperationException()
            assertNull(fixture.repository.inboxLine(current))
            fixture.transport.treeFailure = CodedFailure(500)
            try { fixture.repository.inboxLine(current); fail("Expected the read to fail") } catch (_: CodedFailure) { }
        } finally { fixture.close() }
    }

    private class CodedFailure(override val status: Int?) : IOException("hub refused"), HubCodedFailure {
        override val code: String? = null
    }

    private class Fixture(scope: TestScope, items: List<JsonObject>) {
        val transport = FakeTransport(items)
        private val directory: File = Files.createTempDirectory("inbox-lineage-test").toFile()
        private val owner = CoroutineScope(scope.coroutineContext + SupervisorJob())
        val repository = RealInboxRepository(
            object : InboxAssistantConnection { override val current = MutableStateFlow<InboxAssistantTransport?>(transport) },
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
            }, owner,
        ) { _, _ -> error("unused") }

        init { scope.runCurrent() }

        suspend fun close() {
            owner.coroutineContext.job.cancelAndJoin()
            directory.deleteRecursively()
        }
    }

    private class FakeTransport(items: List<JsonObject>) : InboxAssistantTransport {
        override val scope = "lineage/test"
        private val items = JsonArray(items)
        val treeCalls = mutableListOf<Pair<String, String?>>()
        val parentCalls = mutableListOf<Triple<String, InboxParentRef?, String?>>()
        var treeFailure: Exception? = null
        var failParent = false

        override suspend fun tree(kind: String, id: String): JsonObject {
            treeCalls += id to currentCoroutineContext()[RequestManager]?.id
            treeFailure?.let { throw it }
            return buildJsonObject {
                put("node", buildJsonObject { put("kind", kind); put("id", id) })
                put("item", item(id, "After"))
                put("ancestors", JsonArray(listOf(
                    buildJsonObject {
                        put("kind", "inbox"); put("id", "parent")
                        put("item", item("parent", "Parent"))
                    },
                    buildJsonObject { put("kind", "todo"); put("id", "todo-1") },
                    buildJsonObject { put("kind", "inbox"); put("id", "gone") },
                )))
                put("children", JsonArray(listOf(item("child", "Child"))))
            }
        }

        override suspend fun setParent(itemId: String, parent: InboxParentRef?): JsonObject {
            parentCalls += Triple(itemId, parent, currentCoroutineContext()[RequestManager]?.id)
            if (failParent) throw IOException("write failed")
            return item(itemId, "After")
        }

        override suspend fun inbox() = buildJsonObject { put("items", items) }
        override suspend fun mutes() = buildJsonObject {
            put("spaces", JsonArray(emptyList())); put("sessions", JsonArray(emptyList())); put("allowed", JsonArray(emptyList()))
        }
        override suspend fun setDone(itemId: String, done: Boolean) = JsonObject(emptyMap())
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = JsonObject(emptyMap())
        override suspend fun replyMail(itemId: String, text: String, clientId: String, target: MailReplyTarget) = JsonObject(emptyMap())
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = JsonObject(emptyMap())
        override suspend fun assistant() = JsonObject(emptyMap())
        override suspend fun createConversation(main: Boolean) = JsonObject(emptyMap())
        override suspend fun pins() = JsonObject(emptyMap())
        override suspend fun media(ref: String) = ByteArray(0)
        override suspend fun setDone(items: List<InboxDoneTarget>, done: Boolean) = JsonObject(emptyMap())
    }

    companion object {
        private fun item(id: String, title: String, manager: String? = null) = buildJsonObject {
            put("id", id); put("title", title); put("status", "action"); put("updated_at", "2026-11-13T00:00:00Z")
            manager?.let { put("manager", it) }
        }
    }
}
