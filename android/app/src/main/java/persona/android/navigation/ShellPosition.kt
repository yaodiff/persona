package persona.android.navigation

import android.content.Context
import android.util.AtomicFile
import persona.core.contracts.JsonCodec
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import kotlinx.serialization.Serializable
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json
import java.io.File

/** Reject retired enum values instead of coercing just that field and restoring the rest of an old shell. */
private val positionCodec = Json(JsonCodec) { coerceInputValues = false }

@Serializable
internal data class ShellPosition(
    val tab: AppTab = AppTab.Persona,
    val machine: String = "",
    val chats: Map<AppTab, ChatRoute> = emptyMap(),
    /** Inbox provenance for conversations whose visible NavKey stays unchanged to retain their UI state. */
    val inboxReturns: List<ChatRoute>? = null,
)

/** Navigation is discarded with the pairing; retired writers cannot recreate it after cleanup. */
internal class ShellPositionStore(context: Context, scope: String) {
    private val file = File(context.noBackupFilesDir, "hub-data/$scope/shell-position.json")
    private var retired = false

    init { synchronized(lock) { stores.add(this) } }

    fun load(): ShellPosition = synchronized(lock) {
        if (retired) return@synchronized ShellPosition()
        runCatching { positionCodec.decodeFromString<ShellPosition>(AtomicFile(file).openRead().bufferedReader().use { it.readText() }) }
            .getOrDefault(ShellPosition())
    }

    fun save(position: ShellPosition) = synchronized(lock) {
        // Scoped storage creates this directory. Never create a discarded pairing's directory here.
        if (retired || !file.parentFile!!.isDirectory) return@synchronized
        val atomic = AtomicFile(file)
        val output = atomic.startWrite()
        try {
            output.write(JsonCodec.encodeToString(position).toByteArray())
            atomic.finishWrite(output)
        } catch (error: Exception) {
            atomic.failWrite(output)
            throw error
        }
    }

    companion object {
        private val lock = Any()
        private val stores = java.util.Collections.newSetFromMap(java.util.WeakHashMap<ShellPositionStore, Boolean>())

        fun clear(context: Context) = synchronized(lock) {
            stores.forEach { it.retired = true; AtomicFile(it.file).delete() }
            stores.clear()
            // Previous versions stored pairing-scoped preferences outside the scope directories.
            File(context.applicationInfo.dataDir, "shared_prefs").listFiles().orEmpty()
                .filter { it.name.startsWith("shell-") && it.name.endsWith(".xml") }
                .forEach { check(context.deleteSharedPreferences(it.name.removeSuffix(".xml"))) }
        }
    }
}
