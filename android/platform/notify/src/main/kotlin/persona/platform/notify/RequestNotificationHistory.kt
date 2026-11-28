package persona.platform.notify

import android.content.Context

/**
 * Request cards this process or an earlier one actually posted. Dismissing a card does not make the same pending
 * request new again; resolving it or unpairing clears its record. Ids are local notification state, never sent anywhere.
 */
internal class RequestNotificationHistory(context: Context) {
    private val store = context.applicationContext.getSharedPreferences("request-notification-history", Context.MODE_PRIVATE)

    fun ids(): Set<String> = store.all.keys.mapNotNullTo(HashSet()) { it.removePrefix(PREFIX).takeIf { id -> it.startsWith(PREFIX) } }

    fun occurrences(): Map<String, String?> = store.all.mapNotNull { (key, value) ->
        key.takeIf { it.startsWith(PREFIX) }?.let {
            it.removePrefix(PREFIX) to (value as? String)?.takeIf(String::isNotEmpty)
        }
    }.toMap()

    fun mark(id: String, createdAt: String? = null) {
        store.edit().putString(PREFIX + id, createdAt.orEmpty()).apply()
    }

    /** An exact clear leaves a replacement's record alone; legacy ID-only cleanup remains available to old events. */
    fun clear(id: String, createdAt: String? = null) {
        val key = PREFIX + id
        if (createdAt == null || store.all[key] == createdAt) store.edit().remove(key).apply()
    }

    fun clear() { store.edit().clear().apply() }

    private companion object { const val PREFIX = "request:" }
}
