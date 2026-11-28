package persona.core.data

import java.io.BufferedOutputStream
import java.io.File
import java.io.FileOutputStream
import java.io.FilterOutputStream
import java.io.IOException
import java.io.OutputStream
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import java.security.MessageDigest
import persona.core.contracts.JsonCodec
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.encodeToString
import kotlinx.serialization.ExperimentalSerializationApi
import kotlinx.serialization.json.DecodeSequenceMode
import kotlinx.serialization.json.decodeToSequence
import kotlinx.serialization.json.encodeToStream

/**
 * Cap the on-disk to-do snapshot so a large history does not trigger multi-megabyte heap copies on
 * cold start or after every refresh. Lists above this cap fall back to open to-dos, or skip writing.
 */
const val TODO_SNAPSHOT_MAX_BYTES: Long = 2L * 1024 * 1024

private class SnapshotSizeExceeded : IOException()

private class BoundedOutputStream(
    out: OutputStream,
    private val maxBytes: Long,
) : FilterOutputStream(out) {
    private var written = 0L

    override fun write(b: Int) {
        if (written + 1L > maxBytes) throw SnapshotSizeExceeded()
        out.write(b)
        written += 1L
    }

    override fun write(b: ByteArray, off: Int, len: Int) {
        if (len <= 0) return
        if (written + len.toLong() > maxBytes) throw SnapshotSizeExceeded()
        out.write(b, off, len)
        written += len.toLong()
    }
}

/** Atomic, pairing-scoped snapshots. Failed reads never delete or replace the original file. */
class ListSnapshotStore(private val directory: File) {
    private val lock = Mutex()
    suspend fun read(key: String, maxBytes: Long = Long.MAX_VALUE): String? = withContext(Dispatchers.IO) { lock.withLock {
        val file = File(directory, snapshotHash(key))
        if (file.exists() && file.length() <= maxBytes) file.readText() else null
    } }
    /** Decode only the first window, including when an older app left a full history snapshot. */
    @OptIn(ExperimentalSerializationApi::class)
    internal suspend fun sessionWindow(limit: Int): List<persona.core.contracts.Session>? = withContext(Dispatchers.IO) {
        lock.withLock {
            val file = File(directory, snapshotHash("sessions"))
            if (!file.exists()) null else file.inputStream().use { input ->
                JsonCodec.decodeToSequence<persona.core.contracts.Session>(input, DecodeSequenceMode.ARRAY_WRAPPED)
                    .take(limit).toList()
            }
        }
    }

    @OptIn(ExperimentalSerializationApi::class)
    internal suspend fun readTodosStream(maxBytes: Long): List<persona.core.contracts.Todo>? = withContext(Dispatchers.IO) {
        lock.withLock {
            val file = File(directory, snapshotHash("todos"))
            if (!file.exists() || file.length() > maxBytes) null
            else file.inputStream().buffered().use { input ->
                JsonCodec.decodeToSequence<persona.core.contracts.Todo>(input, DecodeSequenceMode.ARRAY_WRAPPED).toList()
            }
        }
    }

    /**
     * Streams JSON directly into a temp file without an intermediate full-payload [String] or [ByteArray].
     * Returns `false` and leaves any existing file untouched when the encoded bytes exceed [maxBytes].
     */
    internal suspend fun writeStreamBounded(key: String, maxBytes: Long, encode: (OutputStream) -> Unit): Boolean =
        withContext(Dispatchers.IO) {
            lock.withLock {
                check(directory.isDirectory || directory.mkdirs())
                val temp = File.createTempFile("snapshot-", ".tmp", directory)
                try {
                    FileOutputStream(temp).use { fileOut ->
                        val buffered = BufferedOutputStream(fileOut)
                        val bounded = BoundedOutputStream(buffered, maxBytes)
                        encode(bounded)
                        bounded.flush()
                        fileOut.fd.sync()
                    }
                    Files.move(temp.toPath(), File(directory, snapshotHash(key)).toPath(),
                        StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
                    true
                } catch (_: SnapshotSizeExceeded) {
                    false
                } finally {
                    temp.delete()
                }
            }
        }

    suspend fun write(key: String, value: String) = withContext(Dispatchers.IO) { lock.withLock {
        check(directory.isDirectory || directory.mkdirs())
        val temp = File.createTempFile("snapshot-", ".tmp", directory)
        try {
            FileOutputStream(temp).use { it.write(value.toByteArray()); it.fd.sync() }
            Files.move(temp.toPath(), File(directory, snapshotHash(key)).toPath(),
                StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
            Unit
        } finally { temp.delete() }
    } }
}

internal fun snapshotHash(value: String): String = MessageDigest.getInstance("SHA-256")
    .digest(value.toByteArray()).joinToString("") { "%02x".format(it) }

/** A file over [maxBytes] is skipped before it is read, and left as it is. */
internal suspend inline fun <reified T> ListSnapshotStore?.restore(key: String, maxBytes: Long = Long.MAX_VALUE): T? {
    var result: T? = null
    quietly { this?.read(key, maxBytes)?.let { result = JsonCodec.decodeFromString<T>(it) } }
    return result
}

internal suspend inline fun <reified T> ListSnapshotStore?.save(key: String, value: T) {
    quietly { this?.write(key, JsonCodec.encodeToString(value)) }
}

/** The pairing's to-dos as last read from its hub, for TasksStore's first paint (#4533). */
@OptIn(ExperimentalSerializationApi::class)
fun ListSnapshotStore.todoCache(maxBytes: Long = TODO_SNAPSHOT_MAX_BYTES): persona.core.domain.tasks.TodoCache =
    object : persona.core.domain.tasks.TodoCache {
        private val serializer = ListSerializer(persona.core.contracts.Todo.serializer())

        override suspend fun read(): List<persona.core.contracts.Todo>? {
            var result: List<persona.core.contracts.Todo>? = null
            quietly { result = readTodosStream(maxBytes) }
            return result
        }

        override suspend fun write(todos: List<persona.core.contracts.Todo>) {
            quietly {
                val written = writeStreamBounded("todos", maxBytes) { out ->
                    JsonCodec.encodeToStream(serializer, todos, out)
                }
                if (!written) {
                    val open = todos.filter { !it.isDone }
                    if (open.isNotEmpty() && open.size < todos.size) {
                        writeStreamBounded("todos", maxBytes) { out ->
                            JsonCodec.encodeToStream(serializer, open, out)
                        }
                    }
                }
            }
        }
    }
