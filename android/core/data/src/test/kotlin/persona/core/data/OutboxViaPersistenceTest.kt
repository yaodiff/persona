package persona.core.data

import android.database.sqlite.SQLiteDatabase
import androidx.room.Room
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageTransport
import persona.core.contracts.api.MessageTransportResult
import java.io.File

/** Provenance survives an actual database reopen, a manual retry and the version-6 migration. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class OutboxViaPersistenceTest {
    @get:Rule val folder = TemporaryFolder()
    private val scope = HubScope("hub", "via-test")
    private var database: PersistentDatabase? = null

    @After fun close() {
        database?.close()
        database = null
    }

    private fun open(file: File): PersistentDatabase = Room.databaseBuilder(
        RuntimeEnvironment.getApplication(), PersistentDatabase::class.java, file.absolutePath,
    ).allowMainThreadQueries().build().also { database = it }

    private class Hub : MessageTransport {
        override val readyToSend = MutableStateFlow(false)
        override val supportsClientOps = false
        val requests = MutableStateFlow<List<MessageRequest>>(emptyList())
        override suspend fun send(request: MessageRequest): MessageTransportResult {
            requests.value += request
            return MessageTransportResult.Accepted
        }
    }

    @Test fun aFileBackedRetryRestoresUnknownProvenance() = runBlocking {
        val file = folder.newFile("outbox.db")
        var db = open(file)
        val entry = OutboxEntry("shared", SessionKey("machine", "session"), "shared text", createdAt = 1, via = "future-source")
        RoomOutboxStore(db, scope).enqueue(entry)
        assertTrue(RoomOutboxStore(db, scope).transition(entry.clientMessageId, setOf(OutboxState.Queued), OutboxState.Refused, "offline"))
        db.close()
        database = null

        db = open(file)
        val restored = RoomOutboxStore(db, scope).entries.first().single()
        assertEquals("future-source", restored.via)
        assertEquals(OutboxState.Refused, restored.state)
        val hub = Hub()
        val senderScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        val sender = OutboxMessageSender(scope, RoomOutboxStore(db, scope), hub, senderScope)
        try {
            assertTrue(sender.retry(entry.clientMessageId))
            hub.readyToSend.value = true
            val sent = withTimeout(5_000) { hub.requests.first { it.isNotEmpty() } }.single()
            assertEquals("future-source", sent.via)
            assertEquals("shared", sent.clientMessageId)
        } finally {
            sender.close()
            senderScope.cancel()
        }
    }

    @Test fun versionSixRowsMigrateWithTypedProvenance() = runBlocking {
        val file = folder.newFile("migration.db")
        val sqlite = SQLiteDatabase.openOrCreateDatabase(file, null)
        try {
            sqlite.execSQL("CREATE TABLE IF NOT EXISTS outbox (position INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, scope TEXT NOT NULL, machine TEXT NOT NULL, session TEXT NOT NULL, client_msg_id TEXT NOT NULL, text TEXT NOT NULL, clear_draft TEXT, state TEXT NOT NULL, error TEXT, created_at INTEGER NOT NULL, main_target INTEGER NOT NULL DEFAULT 0, op_id TEXT, launcher TEXT, model TEXT, apply_switch INTEGER NOT NULL DEFAULT 0)")
            sqlite.execSQL("CREATE UNIQUE INDEX IF NOT EXISTS index_outbox_scope_client_msg_id ON outbox (scope, client_msg_id)")
            sqlite.execSQL("CREATE TABLE IF NOT EXISTS drafts (scope TEXT NOT NULL, machine TEXT NOT NULL, session TEXT NOT NULL, text TEXT NOT NULL, version INTEGER NOT NULL, attachments TEXT NOT NULL DEFAULT '[]', session_version TEXT, PRIMARY KEY(scope, machine, session))")
            sqlite.execSQL("CREATE TABLE IF NOT EXISTS main_conversation (scope TEXT NOT NULL, machine TEXT, session TEXT, PRIMARY KEY(scope))")
            sqlite.execSQL("CREATE TABLE IF NOT EXISTS room_master_table (id INTEGER PRIMARY KEY, identity_hash TEXT)")
            sqlite.execSQL("INSERT INTO room_master_table (id, identity_hash) VALUES (42, '3a22cc54d102e0cf579679244f34aab2')")
            sqlite.execSQL("INSERT INTO outbox (scope, machine, session, client_msg_id, text, state, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
                arrayOf<Any>(scope.path, "machine", "session", "old", "typed", "queued", 1))
            sqlite.version = 6
        } finally {
            sqlite.close()
        }
        val db = open(file)
        val row = db.records().message(scope.path, "old")!!
        assertNull(row.via)
        assertEquals(8, db.openHelper.readableDatabase.version)
        assertEquals("typed", row.text)
    }
}
