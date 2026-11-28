package persona.core.data

import androidx.room.AutoMigration
import androidx.room.ColumnInfo
import androidx.room.Dao
import androidx.room.Database
import androidx.room.Entity
import androidx.room.Index
import androidx.room.Insert
import androidx.room.OnConflictStrategy
import androidx.room.PrimaryKey
import androidx.room.Query
import androidx.room.RoomDatabase
import androidx.room.migration.Migration
import androidx.sqlite.db.SupportSQLiteDatabase
import androidx.room.withTransaction
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DraftSnapshot
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map

@Entity(tableName = "outbox", indices = [Index(value = ["scope", "client_msg_id"], unique = true)])
data class OutboxRow(
    @PrimaryKey(autoGenerate = true) val position: Long = 0,
    val scope: String,
    val machine: String,
    val session: String,
    val client_msg_id: String,
    val text: String,
    val clear_draft: String?,
    val state: String,
    val error: String?,
    val created_at: Long,
    @ColumnInfo(defaultValue = "0") val main_target: Boolean = false,
    /** Null: the next attempt goes under client_msg_id. */
    val op_id: String? = null,
    val launcher: String? = null,
    val model: String? = null,
    /** The message carries out the session's waiting launcher switch (`apply_switch`, #5035). */
    @ColumnInfo(defaultValue = "0") val apply_switch: Boolean = false,
    /** External provenance; null for typed messages and rows restored from version 6. */
    val via: String? = null,
    /** The hub's own sentence for the refusal in `error` (#16531); null otherwise and for rows restored from version 7. */
    val reason: String? = null,
)

@Entity(tableName = "drafts", primaryKeys = ["scope", "machine", "session"])
data class DraftRow(
    val scope: String, val machine: String, val session: String, val text: String, val version: Long,
    @ColumnInfo(defaultValue = "'[]'") val attachments: String = "[]",
    val session_version: String? = null,
)

@Entity(tableName = "main_conversation")
data class MainConversationRow(
    @PrimaryKey val scope: String,
    val machine: String?,
    val session: String?,
)

@Dao
interface PersistentDao {
    @Query("SELECT * FROM outbox WHERE scope = :scope ORDER BY position")
    fun observeOutbox(scope: String): Flow<List<OutboxRow>>

    @Query("SELECT * FROM outbox WHERE scope = :scope AND client_msg_id = :id")
    suspend fun message(scope: String, id: String): OutboxRow?

    @Insert(onConflict = OnConflictStrategy.ABORT)
    suspend fun insertMessage(row: OutboxRow)

    @Query("UPDATE outbox SET state = :state, error = :error, reason = NULL WHERE scope = :scope AND client_msg_id = :id AND state IN (:from)")
    suspend fun transition(scope: String, id: String, from: List<String>, state: String, error: String?): Int

    @Query("UPDATE outbox SET state = :state, error = :error, op_id = :opId, reason = NULL WHERE scope = :scope AND client_msg_id = :id AND state IN (:from)")
    suspend fun transition(scope: String, id: String, from: List<String>, state: String, error: String?, opId: String): Int

    /** A null [opId] keeps the next attempt's operation id. */
    @Query("UPDATE outbox SET state = :state, error = :error, op_id = COALESCE(:opId, op_id), reason = :reason WHERE scope = :scope AND client_msg_id = :id AND state IN (:from)")
    suspend fun transition(scope: String, id: String, from: List<String>, state: String, error: String?, opId: String?, reason: String?): Int

    @Query("UPDATE outbox SET state = 'unknown' WHERE scope = :scope AND state = 'sending'")
    suspend fun recoverInterrupted(scope: String)

    @Query("SELECT * FROM drafts WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun draft(scope: String, machine: String, session: String): DraftRow?

    @Query("SELECT * FROM drafts WHERE scope = :scope AND machine = :machine AND session = :session")
    fun observeDraft(scope: String, machine: String, session: String): Flow<DraftRow?>

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun putDraft(row: DraftRow)

    @Query("SELECT * FROM main_conversation WHERE scope = :scope")
    suspend fun mainConversation(scope: String): MainConversationRow?

    @Insert(onConflict = OnConflictStrategy.IGNORE)
    suspend fun claimMainCreation(row: MainConversationRow): Long

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun putMainConversation(row: MainConversationRow)

    @Query("DELETE FROM main_conversation WHERE scope = :scope AND machine IS NULL AND session IS NULL")
    suspend fun releaseMainCreation(scope: String)

    @Query("DELETE FROM main_conversation WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun forgetMainConversation(scope: String, machine: String, session: String)

    @Query("UPDATE outbox SET machine = :machine, session = :session WHERE scope = :scope AND main_target = 1 AND machine = ''")
    suspend fun bindMainMessages(scope: String, machine: String, session: String)

    // Moving a merged group's data (GroupMerge.kt); queries only, the schema is unchanged.
    @Query("SELECT * FROM outbox WHERE scope = :scope ORDER BY position")
    suspend fun outbox(scope: String): List<OutboxRow>

    @Query("SELECT * FROM drafts WHERE scope = :scope")
    suspend fun drafts(scope: String): List<DraftRow>

    /** Keeps the row already there: a move retried after a crash adds nothing twice. */
    @Insert(onConflict = OnConflictStrategy.IGNORE)
    suspend fun adoptMessage(row: OutboxRow): Long
}

@Database(entities = [OutboxRow::class, DraftRow::class, MainConversationRow::class], version = 8,
    autoMigrations = [AutoMigration(from = 1, to = 2), AutoMigration(from = 2, to = 3), AutoMigration(from = 3, to = 4),
        AutoMigration(from = 4, to = 5), AutoMigration(from = 5, to = 6), AutoMigration(from = 6, to = 7),
        AutoMigration(from = 7, to = 8)],
    exportSchema = true)
abstract class PersistentDatabase : RoomDatabase() {
    abstract fun records(): PersistentDao
}

class RoomOutboxStore(
    private val database: PersistentDatabase,
    private val scope: HubScope,
    private val journal: InboxReplyJournal? = null,
) : OutboxStore {
    private val dao = database.records()
    override val entries = dao.observeOutbox(scope.path).map { rows ->
        val handOffs = journal?.read(scope.path)?.agentHandOffs.orEmpty().associateBy { it.request.operationId }
        rows.map { row ->
            OutboxEntry(row.client_msg_id, SessionKey(row.machine, row.session), row.text, row.clear_draft,
                OutboxState.entries.single { it.wire == row.state }, row.error, row.created_at, row.main_target, row.op_id,
                row.launcher, row.model, applySwitch = row.apply_switch, via = row.via, reason = row.reason)
                .withAgentHandOff(handOffs[row.client_msg_id])
        }
    }

    override suspend fun enqueue(entry: OutboxEntry) = enqueueDraft(entry, null)

    override suspend fun enqueueDraft(entry: OutboxEntry, draft: DraftSnapshot?) {
        val handOff = entry.agentHandOff?.let { it.copy(deliveryScopes = it.deliveryScopes + scope.path) }
        if (handOff != null && handOff.scope != scope.path) {
            // The target journal restores the outbox metadata; the origin journal drives recording and crash replay.
            checkNotNull(kotlinx.coroutines.currentCoroutineContext()[InboxAgentHandOffContext]?.originPersisted) {
                "Cross-pairing hand-off origin is not persisted"
            }.await()
            check(InboxReplyJournal.linkAgentHandOffTarget(handOff, scope.path, journal)) {
                "Cross-pairing hand-off origin is not linked"
            }
        }
        if (handOff != null && journal != null) {
            // The message's internal identity, item route and replay linkage are durable before the row can be sent.
            journal.update(scope.path) { it.enqueue(handOff).linkAgentHandOffTarget(handOff.request.operationId, scope.path) }
        }
        database.withTransaction {
            require(draft == null || draft.scope == scope) { "Draft belongs to another pairing" }
            val existing = dao.message(scope.path, entry.clientMessageId)
            if (existing != null) {
                require(existing.main_target == entry.mainTarget &&
                    (entry.mainTarget || (existing.machine == entry.session.machineId && existing.session == entry.session.sessionId)) &&
                    existing.text == entry.text && existing.clear_draft == entry.clearDraft &&
                    existing.launcher == entry.launcher && existing.model == entry.model && existing.via == entry.via) { "Operation id reused with different parameters" }
            } else {
                if (draft != null) {
                    val key = draft.key.storageKey()
                    val old = dao.draft(scope.path, key.machineId, key.sessionId)
                    check((old?.version ?: 0) == draft.version && old?.text.orEmpty() == draft.text) { "Draft version changed" }
                    dao.putDraft(DraftRow(scope.path, key.machineId, key.sessionId, "", Math.addExact(draft.version, 1)))
                }
                val main = if (entry.mainTarget) mainConversation() else null
                val target = main ?: entry.session
                dao.insertMessage(OutboxRow(scope = scope.path, machine = target.machineId,
                    session = target.sessionId, client_msg_id = entry.clientMessageId, text = entry.text,
                    clear_draft = entry.clearDraft, state = OutboxState.Queued.wire, error = null,
                    created_at = entry.createdAt, main_target = entry.mainTarget, op_id = entry.opId,
                        launcher = entry.launcher, model = entry.model, apply_switch = entry.applySwitch, via = entry.via))
            }
        }
    }

    override suspend fun persistAgentHandOffDelivery(entry: OutboxEntry): OutboxEntry {
        val handOff = entry.agentHandOff ?: return entry
        val delivered = entry.copyForDelivery(agentHandOff = handOff.copy(
            delivered = true, deliveryScopes = handOff.deliveryScopes + scope.path))
        if (journal != null) journal.update(scope.path) { saved ->
            saved.commitAgentHandOffDelivery(checkNotNull(delivered.agentHandOff))
        }
        // An adopted target can prove delivery, but only the originating journal may drive its inbox recording/replay.
        // If the origin is temporarily unavailable the helper returns false and the existing originCommitted recovery
        // retries this idempotent write rather than mistaking the target journal for the origin.
        InboxReplyJournal.commitAgentHandOffDelivery(checkNotNull(delivered.agentHandOff), scope.path, journal)
        return delivered
    }

    override fun agentHandOffOriginCommitted(entry: OutboxEntry): Boolean =
        entry.agentHandOff?.let { journal != null && it.scope == scope.path } ?: true

    override suspend fun mainConversation(): SessionKey? = dao.mainConversation(scope.path)?.let {
        if (it.machine != null && it.session != null) SessionKey(it.machine, it.session) else null
    }

    override suspend fun claimMainCreation(): Boolean =
        dao.claimMainCreation(MainConversationRow(scope.path, null, null)) != -1L

    override suspend fun releaseMainCreation() = dao.releaseMainCreation(scope.path)

    override suspend fun bindMainConversation(session: SessionKey) = database.withTransaction {
        require(session.machineId.isNotBlank() && session.sessionId.isNotBlank())
        dao.putMainConversation(MainConversationRow(scope.path, session.machineId, session.sessionId))
        dao.bindMainMessages(scope.path, session.machineId, session.sessionId)
    }

    override suspend fun forgetMainConversation(session: SessionKey) =
        dao.forgetMainConversation(scope.path, session.machineId, session.sessionId)

    override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?): Boolean =
        dao.transition(scope.path, id, from.map { it.wire }, to.wire, error) != 0

    override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String): Boolean =
        dao.transition(scope.path, id, from.map { it.wire }, to.wire, error, opId) != 0

    override suspend fun transition(
        id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String?, reason: String?,
    ): Boolean = dao.transition(scope.path, id, from.map { it.wire }, to.wire, error, opId, reason) != 0

    override suspend fun recoverInterrupted() = dao.recoverInterrupted(scope.path)
}

@Entity(tableName = "items", primaryKeys = ["scope", "machine", "session", "id"])
data class ItemRow(
    val scope: String, val machine: String, val session: String,
    val seq: Int, val part: Int, val id: String, val json: String,
)

@Entity(tableName = "cursors", primaryKeys = ["scope", "machine", "session"])
data class SessionCursorRow(
    val scope: String, val machine: String, val session: String,
    val history: String?, val covered: Int, val has_more: Boolean?,
    @ColumnInfo(defaultValue = "0") val accessed_at: Long = 0,
)

data class SessionCacheSize(val machine: String, val session: String, val bytes: Long, val accessed: Long)

/** One stored row of a session without its key columns: what a save compares the new window with. */
data class StoredItem(val id: String, val seq: Int, val part: Int, val json: String)

@Dao
interface ItemsDao {
    @Query("SELECT i.machine, i.session, SUM(length(CAST(i.json AS BLOB))) AS bytes, " +
        "COALESCE(c.accessed_at, 0) AS accessed FROM items i LEFT JOIN cursors c ON " +
        "i.scope = c.scope AND i.machine = c.machine AND i.session = c.session " +
        "WHERE i.scope = :scope GROUP BY i.machine, i.session ORDER BY accessed, i.machine, i.session")
    suspend fun sizes(scope: String): List<SessionCacheSize>

    /** One session's share of [sizes], read along the primary key's prefix. */
    @Query("SELECT COALESCE(SUM(length(CAST(json AS BLOB))), 0) FROM items " +
        "WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun size(scope: String, machine: String, session: String): Long

    @Query("UPDATE cursors SET accessed_at = :accessed WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun touch(scope: String, machine: String, session: String, accessed: Long)

    @Query("DELETE FROM cursors WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun clearCursor(scope: String, machine: String, session: String)

    @Query("SELECT * FROM cursors WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun cursor(scope: String, machine: String, session: String): SessionCursorRow?

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun putCursor(cursor: SessionCursorRow)

    @Query("SELECT * FROM items WHERE scope = :scope AND machine = :machine AND session = :session ORDER BY seq, part, id")
    fun observe(scope: String, machine: String, session: String): Flow<List<ItemRow>>

    @Query("DELETE FROM items WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun clear(scope: String, machine: String, session: String)

    /** One session's rows as a save compares them, read along the primary key's prefix. */
    @Query("SELECT id, seq, part, json FROM items WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun stored(scope: String, machine: String, session: String): List<StoredItem>

    /** Takes three variables besides [ids]: callers keep [ids] under SQLite's limit (999 before 3.32, API 30). */
    @Query("DELETE FROM items WHERE scope = :scope AND machine = :machine AND session = :session AND id IN (:ids)")
    suspend fun delete(scope: String, machine: String, session: String, ids: List<String>)

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun insert(items: List<ItemRow>)
}

@Database(entities = [ItemRow::class, SessionCursorRow::class], version = 7,
    autoMigrations = [AutoMigration(from = 2, to = 3)], exportSchema = true)
abstract class ItemsDatabase : RoomDatabase() {
    abstract fun items(): ItemsDao

    companion object {
        val MIGRATION_1_2 = object : Migration(1, 2) {
            override fun migrate(db: SupportSQLiteDatabase) {
                db.execSQL("CREATE TABLE IF NOT EXISTS `cursors` (`scope` TEXT NOT NULL, `machine` TEXT NOT NULL, " +
                    "`session` TEXT NOT NULL, `history` TEXT, `covered` INTEGER NOT NULL, `has_more` INTEGER, " +
                    "PRIMARY KEY(`scope`, `machine`, `session`))")
            }
        }

        /**
         * The same tables; what an older build kept is dropped, since it decoded items without fields added since (a
         * refusal notice's `model_refusal`, #2528) and the chat keeps a cached item over the hub's newer copy. A cache:
         * the chats load again from the hub.
         */
        val MIGRATION_3_4 = object : Migration(3, 4) {
            override fun migrate(db: SupportSQLiteDatabase) {
                db.execSQL("DELETE FROM `items`")
                db.execSQL("DELETE FROM `cursors`")
            }
        }

        /** Codex commentary cached as thinking must be read again as assistant text; these are only cache rows. */
        val MIGRATION_4_5 = object : Migration(4, 5) {
            override fun migrate(db: SupportSQLiteDatabase) {
                db.execSQL("DELETE FROM `items`")
                db.execSQL("DELETE FROM `cursors`")
            }
        }

        /**
         * Paseo's input to an agent cached as the user's raw <paseo-system> text must be read again as another
         * session's message, a subagent's task note or a notice (#8220); these are only cache rows.
         */
        val MIGRATION_5_6 = object : Migration(5, 6) {
            override fun migrate(db: SupportSQLiteDatabase) {
                db.execSQL("DELETE FROM `items`")
                db.execSQL("DELETE FROM `cursors`")
            }
        }

        /**
         * Codex sleeps, subagent activity and subagent waits cached as notices must be read again as the tool calls the
         * hub now serves under the same ids (#10436); these are only cache rows.
         */
        val MIGRATION_6_7 = object : Migration(6, 7) {
            override fun migrate(db: SupportSQLiteDatabase) {
                db.execSQL("DELETE FROM `items`")
                db.execSQL("DELETE FROM `cursors`")
            }
        }
    }
}
