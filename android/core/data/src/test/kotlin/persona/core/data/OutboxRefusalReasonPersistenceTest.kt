package persona.core.data

import android.database.sqlite.SQLiteDatabase
import androidx.room.Room
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
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
import java.io.File

/** The hub's words for a refusal (#16531) survive a database reopen, go with the next transition and migrate from version 7. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class OutboxRefusalReasonPersistenceTest {
    @get:Rule val folder = TemporaryFolder()
    private val scope = HubScope("hub", "reason-test")
    private var database: PersistentDatabase? = null

    @After fun close() {
        database?.close()
        database = null
    }

    private fun open(file: File): PersistentDatabase = Room.databaseBuilder(
        RuntimeEnvironment.getApplication(), PersistentDatabase::class.java, file.absolutePath,
    ).allowMainThreadQueries().build().also { database = it }

    @Test fun theHubsWordsSurviveAReopenAndGoWithTheNextTransition() = runBlocking {
        val file = folder.newFile("outbox.db")
        val words = "This Antigravity conversation is open in agy in a terminal on that computer. Nothing was sent."
        var db = open(file)
        val entry = OutboxEntry("agy", SessionKey("machine", "agy-session"), "to agy", createdAt = 1)
        RoomOutboxStore(db, scope).enqueue(entry)
        assertTrue(RoomOutboxStore(db, scope).transition(entry.clientMessageId, setOf(OutboxState.Queued),
            OutboxState.Refused, "agy_in_terminal", "next-op", words))
        db.close()
        database = null

        db = open(file)
        val store = RoomOutboxStore(db, scope)
        val restored = store.entries.first().single()
        assertEquals(OutboxState.Refused, restored.state)
        assertEquals("agy_in_terminal", restored.error)
        assertEquals(words, restored.reason)
        assertEquals("next-op", restored.opId)

        // A transition without words clears them (Delete), and Undo's puts them back without moving the operation id.
        assertTrue(store.transition(entry.clientMessageId, setOf(OutboxState.Refused), OutboxState.Cancelled))
        assertNull(store.entries.first().single().reason)
        assertTrue(store.transition(entry.clientMessageId, setOf(OutboxState.Cancelled), OutboxState.Refused,
            "agy_in_terminal", null, words))
        assertEquals(restored, store.entries.first().single())
        // A retry's next attempt leaves nothing of the refusal.
        assertTrue(store.transition(entry.clientMessageId, setOf(OutboxState.Refused), OutboxState.Sending, null, "retry-op"))
        val retried = store.entries.first().single()
        assertNull(retried.reason)
        assertNull(retried.error)
        assertEquals("retry-op", retried.opId)
    }

    @Test fun versionSevenRowsMigrateWithoutWords() = runBlocking {
        val file = folder.newFile("migration.db")
        val sqlite = SQLiteDatabase.openOrCreateDatabase(file, null)
        try {
            sqlite.execSQL("CREATE TABLE IF NOT EXISTS outbox (position INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, scope TEXT NOT NULL, machine TEXT NOT NULL, session TEXT NOT NULL, client_msg_id TEXT NOT NULL, text TEXT NOT NULL, clear_draft TEXT, state TEXT NOT NULL, error TEXT, created_at INTEGER NOT NULL, main_target INTEGER NOT NULL DEFAULT 0, op_id TEXT, launcher TEXT, model TEXT, apply_switch INTEGER NOT NULL DEFAULT 0, via TEXT)")
            sqlite.execSQL("CREATE UNIQUE INDEX IF NOT EXISTS index_outbox_scope_client_msg_id ON outbox (scope, client_msg_id)")
            sqlite.execSQL("CREATE TABLE IF NOT EXISTS drafts (scope TEXT NOT NULL, machine TEXT NOT NULL, session TEXT NOT NULL, text TEXT NOT NULL, version INTEGER NOT NULL, attachments TEXT NOT NULL DEFAULT '[]', session_version TEXT, PRIMARY KEY(scope, machine, session))")
            sqlite.execSQL("CREATE TABLE IF NOT EXISTS main_conversation (scope TEXT NOT NULL, machine TEXT, session TEXT, PRIMARY KEY(scope))")
            sqlite.execSQL("CREATE TABLE IF NOT EXISTS room_master_table (id INTEGER PRIMARY KEY, identity_hash TEXT)")
            sqlite.execSQL("INSERT INTO room_master_table (id, identity_hash) VALUES (42, '27c211dfb39445055ad55d1c5c186a45')")
            sqlite.execSQL("INSERT INTO outbox (scope, machine, session, client_msg_id, text, state, error, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                arrayOf<Any>(scope.path, "machine", "agy-session", "old", "typed", "refused", "agy_in_terminal", 1))
            sqlite.version = 7
        } finally {
            sqlite.close()
        }
        val db = open(file)
        assertEquals(8, db.openHelper.readableDatabase.version)
        val restored = RoomOutboxStore(db, scope).entries.first().single()
        assertEquals("agy_in_terminal", restored.error)
        assertNull(restored.reason)
    }
}
